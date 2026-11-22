// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package datastore

import (
	"context"
	"errors"
	"time"

	"github.com/gitstore-dev/gitstore/api/internal/catalog"
)

// TranslationTargetMetadata contains only authoritative point-read metadata,
// never target spec/body content or a repaired name/UID projection.
type TranslationTargetMetadata struct {
	Evidence          catalog.TranslationTargetEvidence
	OwnerSub          string
	SourceIdentity    TranslationTargetSourceIdentity
	CreationTimestamp time.Time
	// Active requires a proved non-terminating, non-retired source.
	Active bool
}

type TranslationTargetMetadataStore interface {
	ReadTranslationTargetMetadata(context.Context, *TranslationTargetFence) (*TranslationTargetMetadata, error)
}

type TranslationTargetNamespaceMetadata struct {
	UID, Name, OwnerSub, ResourceVersion string
	Active                               bool
}

type TranslationTargetNamespaceMetadataStore interface {
	ReadTranslationTargetNamespaceMetadata(context.Context, string) (*TranslationTargetNamespaceMetadata, error)
}

// Absence requires current namespace coverage plus an unchanged missing/vacant
// serialized head. Callers must authorize before using this observation.
func ReadTranslationTargetAbsence(
	ctx context.Context,
	target TranslationTargetKey,
	coverage TranslationTargetCoverageReader,
	readFence func(context.Context, TranslationTargetKey) (*TranslationTargetFence, error),
) error {
	if _, err := TranslationTargetFenceID(target); err != nil || coverage == nil || readFence == nil {
		return ErrInvalidArgument
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	beforeEpoch, err := ReadTranslationTargetAuditCoverage(ctx, coverage, target.NamespaceUID)
	if err != nil {
		return err
	}
	readVacancy := func() (*TranslationTargetFence, error) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		fence, err := readFence(ctx, target)
		if errors.Is(err, ErrNotFound) && fence == nil {
			return nil, nil
		}
		if err != nil {
			return nil, err
		}
		if ValidateTranslationTargetFence(fence) != nil || fence.Target != target {
			return nil, ErrRepairRequired
		}
		if fence.Phase != TranslationTargetStable || fence.Evidence != nil {
			return nil, ErrConflict
		}
		return CloneTranslationTargetFence(fence), nil
	}
	before, err := readVacancy()
	if err != nil {
		return err
	}
	after, err := readVacancy()
	if err != nil {
		return err
	}
	if (before == nil) != (after == nil) ||
		(before != nil && (before.Token != after.Token || !before.CreationTimestamp.Equal(after.CreationTimestamp))) {
		return ErrConflict
	}
	afterEpoch, err := ReadTranslationTargetAuditCoverage(ctx, coverage, target.NamespaceUID)
	if err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if beforeEpoch != afterEpoch {
		return ErrConflict
	}
	return nil
}

// ReadTranslationTargetEvidence performs at most three serialized point reads.
// Callers own authorization, backend consistency and watermarked backfill.
// It neither retries nor establishes a positive cross-request cache.
func ReadTranslationTargetEvidence(
	ctx context.Context,
	target TranslationTargetKey,
	readFence func(context.Context, TranslationTargetKey) (*TranslationTargetFence, error),
	readMetadata func(context.Context, *TranslationTargetFence) (*TranslationTargetMetadata, error),
) (*catalog.TranslationTargetEvidence, error) {
	if _, err := TranslationTargetFenceID(target); err != nil || readFence == nil || readMetadata == nil {
		return nil, ErrInvalidArgument
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	before, err := readFence(ctx, target)
	if err != nil {
		return nil, translationTargetReadError(err)
	}
	if ValidateTranslationTargetFence(before) != nil || before.Target != target ||
		before.Phase != TranslationTargetStable || before.Evidence == nil || before.CreationTimestamp.IsZero() {
		return nil, ErrRepairRequired
	}
	before = CloneTranslationTargetFence(before)
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	source, err := readMetadata(ctx, CloneTranslationTargetFence(before))
	if err != nil {
		return nil, translationTargetReadError(err)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if source == nil || source.SourceIdentity.NamespaceUID != target.NamespaceUID ||
		source.SourceIdentity.WriteToken != before.Token ||
		source.SourceIdentity.ContentDigest != before.Evidence.ContentDigest ||
		!source.CreationTimestamp.Equal(before.CreationTimestamp) ||
		validateTranslationTargetEvidence(target, &source.Evidence) != nil {
		return nil, ErrRepairRequired
	}
	evidence := source.Evidence
	expected := *before.Evidence
	expected.ResourceVersion = evidence.ResourceVersion
	if expected != evidence || (evidence.ResourceVersion != before.Evidence.ResourceVersion &&
		!translationTargetVersionAfter(evidence.ResourceVersion, before.Evidence.ResourceVersion)) {
		return nil, ErrRepairRequired
	}
	if !source.Active {
		return nil, ErrConflict
	}
	after, err := readFence(ctx, target)
	if err != nil {
		return nil, translationTargetReadError(err)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if ValidateTranslationTargetFence(after) != nil {
		return nil, ErrRepairRequired
	}
	if after.Target != target || after.Phase != TranslationTargetStable || after.Token != before.Token ||
		after.Evidence == nil || *after.Evidence != *before.Evidence || !after.CreationTimestamp.Equal(before.CreationTimestamp) {
		return nil, ErrConflict
	}
	return &evidence, nil
}

func translationTargetReadError(err error) error {
	if errors.Is(err, ErrNotFound) {
		// Missing proof is unavailable evidence, not authoritative target absence.
		return ErrRepairRequired
	}
	return err
}
