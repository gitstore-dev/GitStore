// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package datastore

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"math/big"
	"reflect"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/gitstore-dev/gitstore/api/internal/catalog"
	"github.com/google/uuid"
)

const MaxTranslationTargetIntentBytes = 16 << 10

func TranslationTargetFenceID(target TranslationTargetKey) (string, error) {
	if !utf8.ValidString(target.Name) || strings.ContainsRune(target.Name, 0) ||
		validateTranslationIndexIdentity(TranslationIndexKey{Kind: TranslationTargetNameIndex, NamespaceUID: target.NamespaceUID, Target: target}) != nil {
		return "", ErrInvalidArgument
	}
	raw, _ := json.Marshal(target)
	digest := sha256.Sum256(raw)
	return hex.EncodeToString(digest[:]), nil
}

func validateTranslationTargetIntent(intent TranslationTargetWriteIntent) error {
	if intent.ID == "" {
		return ErrInvalidArgument
	}
	size := 0
	for _, field := range []string{intent.ID, intent.RepositoryID, intent.GitRef, intent.GitCommitSHA, intent.SourcePath,
		intent.PreviousUID, intent.NextUID, intent.ExpectedResourceVersion, intent.SourceDigest, intent.SourceProof} {
		if len(field) > MaxTranslationTargetIntentBytes-size || !utf8.ValidString(field) || strings.ContainsRune(field, 0) {
			return ErrInvalidArgument
		}
		size += len(field)
	}
	for _, id := range []string{intent.ID, intent.PreviousUID, intent.NextUID, intent.RepositoryID} {
		if id == "" {
			continue
		}
		if parsed, err := uuid.Parse(id); err != nil || parsed.String() != id {
			return ErrInvalidArgument
		}
	}
	if (intent.PreviousUID == "") != (intent.ExpectedResourceVersion == "") ||
		(intent.Deleting && (intent.PreviousUID == "" || intent.NextUID != "")) ||
		(intent.Invalidating && (intent.Deleting || intent.PreviousUID == "" || intent.PreviousUID != intent.NextUID)) ||
		(intent.Backfilling && (intent.Deleting || intent.Invalidating || intent.PreviousUID == "" ||
			intent.PreviousUID != intent.NextUID || intent.SourceProof == "")) ||
		(!intent.Deleting && (intent.NextUID == "" || (!intent.Invalidating && intent.SourceDigest == ""))) || len(intent.SourceDigest) > 256 {
		return ErrInvalidArgument
	}
	if intent.ExpectedResourceVersion != "" {
		if _, err := AdvanceTranslationIndexVersion(intent.ExpectedResourceVersion); err != nil {
			return ErrInvalidArgument
		}
	}
	raw, err := json.Marshal(intent)
	if err != nil || len(raw) > MaxTranslationTargetIntentBytes {
		return ErrInvalidArgument
	}
	return nil
}

func validateTranslationTargetEvidence(target TranslationTargetKey, evidence *catalog.TranslationTargetEvidence) error {
	if evidence == nil {
		return nil
	}
	if evidence.APIVersion != target.APIVersion || evidence.Kind != target.Kind || evidence.Name != target.Name ||
		evidence.Namespace == "" || len(evidence.Namespace) > MaxTranslationTargetIntentBytes || !utf8.ValidString(evidence.Namespace) ||
		strings.ContainsRune(evidence.Namespace, 0) || evidence.Generation < 1 || evidence.ContentDigest == "" || len(evidence.ContentDigest) > 256 ||
		!utf8.ValidString(evidence.ContentDigest) || strings.ContainsRune(evidence.ContentDigest, 0) ||
		len(evidence.Revision) > MaxTranslationTargetIntentBytes || !utf8.ValidString(evidence.Revision) || strings.ContainsRune(evidence.Revision, 0) {
		return ErrInvalidArgument
	}
	if parsed, err := uuid.Parse(evidence.UID); err != nil || parsed.String() != evidence.UID {
		return ErrInvalidArgument
	}
	if _, err := AdvanceTranslationIndexVersion(evidence.ResourceVersion); err != nil {
		return ErrInvalidArgument
	}
	return nil
}

func ValidateTranslationTargetFence(fence *TranslationTargetFence) error {
	if fence == nil {
		return ErrRepairRequired
	}
	if _, err := TranslationTargetFenceID(fence.Target); err != nil {
		return ErrRepairRequired
	}
	if parsed, err := uuid.Parse(fence.Token); err != nil || parsed.String() != fence.Token ||
		validateTranslationTargetEvidence(fence.Target, fence.Evidence) != nil {
		return ErrRepairRequired
	}
	switch fence.Phase {
	case TranslationTargetStable:
		if fence.Intent != nil {
			return ErrRepairRequired
		}
	case TranslationTargetUpdating:
		if fence.Intent == nil || validateTranslationTargetIntent(*fence.Intent) != nil ||
			(fence.Intent.Backfilling && fence.Evidence != nil) ||
			(fence.Evidence != nil && fence.Evidence.UID != fence.Intent.PreviousUID) {
			return ErrRepairRequired
		}
	default:
		return ErrRepairRequired
	}
	return nil
}

func CloneTranslationTargetFence(fence *TranslationTargetFence) *TranslationTargetFence {
	if fence == nil {
		return nil
	}
	next := *fence
	if fence.Intent != nil {
		intent := *fence.Intent
		next.Intent = &intent
	}
	if fence.Evidence != nil {
		evidence := *fence.Evidence
		next.Evidence = &evidence
	}
	return &next
}

func PlanTranslationTargetWrite(current *TranslationTargetFence, target TranslationTargetKey, intent TranslationTargetWriteIntent, expected string) (*TranslationTargetFence, error) {
	if !intent.CreationTimestamp.IsZero() {
		intent.CreationTimestamp = intent.CreationTimestamp.UTC().Truncate(time.Millisecond)
	}
	if _, err := TranslationTargetFenceID(target); err != nil || validateTranslationTargetIntent(intent) != nil {
		return nil, ErrInvalidArgument
	}
	if current != nil {
		if ValidateTranslationTargetFence(current) != nil || current.Target != target {
			return nil, ErrRepairRequired
		}
		if current.Evidence != nil && intent.PreviousUID == intent.NextUID && !current.CreationTimestamp.IsZero() {
			if intent.CreationTimestamp.IsZero() {
				intent.CreationTimestamp = current.CreationTimestamp
			} else if !intent.CreationTimestamp.Equal(current.CreationTimestamp) {
				return nil, ErrConflict
			}
		}
		if current.Phase == TranslationTargetUpdating {
			original := *current.Intent
			original.ExpectedResourceVersion = intent.ExpectedResourceVersion
			if original == intent && (current.Intent.ExpectedResourceVersion == intent.ExpectedResourceVersion ||
				translationTargetVersionAfter(intent.ExpectedResourceVersion, current.Intent.ExpectedResourceVersion)) {
				return CloneTranslationTargetFence(current), nil
			}
			return nil, ErrConflict
		}
		if current.Token != expected || (current.Evidence == nil && intent.PreviousUID != "" && !intent.Backfilling) ||
			(current.Evidence != nil && (intent.Backfilling || current.Evidence.UID != intent.PreviousUID ||
				translationTargetVersionAfter(current.Evidence.ResourceVersion, intent.ExpectedResourceVersion))) {
			return nil, ErrConflict
		}
	} else if expected != "" {
		return nil, ErrConflict
	}
	next := CloneTranslationTargetFence(current)
	if next == nil {
		next = &TranslationTargetFence{Target: target}
	}
	next.Token, next.Phase, next.Intent = uuid.NewString(), TranslationTargetUpdating, &intent
	return next, nil
}

func PlanTranslationTargetCompletion(current *TranslationTargetFence, token string, evidence *catalog.TranslationTargetEvidence) (*TranslationTargetFence, error) {
	if err := ValidateTranslationTargetFence(current); err != nil {
		return nil, err
	}
	if current.Token != token {
		return nil, ErrConflict
	}
	if err := validateTranslationTargetEvidence(current.Target, evidence); err != nil {
		return nil, err
	}
	if current.Phase == TranslationTargetStable {
		if !reflect.DeepEqual(current.Evidence, evidence) {
			return nil, ErrConflict
		}
		return CloneTranslationTargetFence(current), nil
	}
	intent := current.Intent
	if intent.Deleting || intent.Invalidating {
		if evidence != nil {
			return nil, ErrConflict
		}
	} else {
		if evidence == nil || evidence.UID != intent.NextUID || evidence.ContentDigest != intent.SourceDigest {
			return nil, ErrConflict
		}
		if current.Evidence != nil && evidence.UID == current.Evidence.UID &&
			(evidence.Namespace != current.Evidence.Namespace || evidence.Generation < current.Evidence.Generation) {
			return nil, ErrConflict
		}
		expected := intent.ExpectedResourceVersion
		if intent.PreviousUID != intent.NextUID {
			expected = ""
		}
		if !translationTargetVersionAfter(evidence.ResourceVersion, expected) &&
			!(intent.Backfilling && evidence.ResourceVersion == expected) {
			return nil, ErrConflict
		}
	}
	next := CloneTranslationTargetFence(current)
	next.Phase, next.Intent, next.Evidence = TranslationTargetStable, nil, evidence
	next.CreationTimestamp = current.Intent.CreationTimestamp
	if evidence == nil {
		next.CreationTimestamp = time.Time{}
	}
	return CloneTranslationTargetFence(next), nil
}

// PlanTranslationTargetAbort requires durable source-CAS proof that the frozen
// writer cannot commit. An unchanged row, elapsed time or missing row alone
// cannot authorize this transition.
func PlanTranslationTargetAbort(current *TranslationTargetFence, token string, evidence *catalog.TranslationTargetEvidence) (*TranslationTargetFence, error) {
	if err := ValidateTranslationTargetFence(current); err != nil {
		return nil, err
	}
	if current.Token != token {
		return nil, ErrConflict
	}
	if err := validateTranslationTargetEvidence(current.Target, evidence); err != nil {
		return nil, err
	}
	if current.Phase == TranslationTargetStable {
		return PlanTranslationTargetCompletion(current, token, evidence)
	}
	if current.Evidence == nil {
		if current.Intent.PreviousUID != "" && !current.Intent.Backfilling {
			return nil, ErrRepairRequired
		}
		if evidence != nil {
			return nil, ErrConflict
		}
	} else {
		if evidence == nil {
			return nil, ErrConflict
		}
		previous := *current.Evidence
		previous.ResourceVersion = evidence.ResourceVersion
		if !reflect.DeepEqual(&previous, evidence) ||
			(evidence.ResourceVersion != current.Intent.ExpectedResourceVersion &&
				!translationTargetVersionAfter(evidence.ResourceVersion, current.Intent.ExpectedResourceVersion)) {
			return nil, ErrConflict
		}
	}
	next := CloneTranslationTargetFence(current)
	next.Phase, next.Intent, next.Evidence = TranslationTargetStable, nil, evidence
	return CloneTranslationTargetFence(next), nil
}

func translationTargetVersionAfter(actual, previous string) bool {
	if previous == "" {
		previous = "0"
	}
	next, nextOK := new(big.Int).SetString(actual, 10)
	prior, priorOK := new(big.Int).SetString(previous, 10)
	return nextOK && priorOK && next.Cmp(prior) > 0
}
