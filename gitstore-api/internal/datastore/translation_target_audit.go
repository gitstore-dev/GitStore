// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package datastore

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"time"

	"github.com/gitstore-dev/gitstore/translation"
	"github.com/google/uuid"
)

type TranslationTargetSourceAuditHint struct {
	Target            TranslationTargetKey
	Namespace         string
	UID               string
	CreationTimestamp time.Time
	ResourceVersion   string
}

type TranslationTargetSourceAuditPage struct {
	Items   []TranslationTargetSourceAuditHint
	Next    string
	Scanned int
}

// Epochs observe participating source writes, not namespace completeness or
// legacy ownership. An unresolved native source grant returns ErrConflict.
type TranslationTargetMutationEpochStore interface {
	ReadTranslationTargetMutationEpoch(context.Context, string) (string, error)
}

type TranslationTargetCoverageEpochStore interface {
	ReadTranslationTargetCoverageEpoch(context.Context, string) (string, error)
}

type TranslationTargetCoverageReader interface {
	TranslationTargetCoverageEpochStore
	GetTranslationTargetAuditProgress(context.Context, string) (TranslationAuditProgress, error)
}

type TranslationTargetCoverageAuditStore interface {
	TranslationTargetSourceAuditStore
	TranslationTargetCoverageEpochStore
	VerifyTranslationTargetSourceCoverage(context.Context, TranslationTargetSourceAuditHint) error
}

// Target source discovery is independent of name/UID lookup projections and
// fences. Recovery verifies or initializes known-birth source proof;
// traversal alone cannot certify namespace completeness.
type TranslationTargetSourceAuditStore interface {
	TranslationTargetMutationEpochStore
	ListTranslationTargetSourceAudit(context.Context, string, string, int) (TranslationTargetSourceAuditPage, error)
	RecoverTranslationTargetSourceAudit(context.Context, TranslationTargetSourceAuditHint) error
}

// Target coverage observes participating writers only. It is not a capability
// certificate for old replicas, Translation indexes or deployment activation.
type TranslationTargetAuditStore interface {
	TranslationTargetCoverageAuditStore
	TranslationTargetCoverageReader
	RepairTranslationTargetAuditTurn(context.Context, string, int) (TranslationAuditProgress, error)
}

func ValidateTranslationTargetAuditProgress(progress TranslationAuditProgress) error {
	for _, value := range []string{progress.NamespaceUID, progress.RunID} {
		if id, err := uuid.Parse(value); err != nil || id.String() != value {
			return ErrRepairRequired
		}
	}
	if progress.ResourceVersion == "" || len(progress.ResourceVersion) > 256 ||
		progress.DescriptorDigest == "" || len(progress.DescriptorDigest) > 256 ||
		len(progress.Cursor) > 32<<10 || progress.Scanned < 0 {
		return ErrRepairRequired
	}
	if _, err := AdvanceTranslationIndexVersion(progress.ResourceVersion); err != nil {
		return ErrRepairRequired
	}
	switch progress.Phase {
	case "Repairing":
		if progress.TargetMutationEpoch != "" {
			return ErrRepairRequired
		}
	case "Verifying", "Verified":
		if id, err := uuid.Parse(progress.TargetMutationEpoch); err != nil || id.String() != progress.TargetMutationEpoch {
			return ErrRepairRequired
		}
		if progress.Phase == "Verified" && progress.Cursor != "" {
			return ErrRepairRequired
		}
	default:
		return ErrRepairRequired
	}
	return nil
}

// Planning performs one bounded page; backends must CAS the complete prior
// progress before publishing the result. A failed page never advances a cursor.
func PlanTranslationTargetAuditTurn(ctx context.Context, store TranslationTargetCoverageAuditStore, namespace string, current TranslationAuditProgress, limit int) (TranslationAuditProgress, error) {
	fail := func(err error) (TranslationAuditProgress, error) { return TranslationAuditProgress{}, err }
	if err := ctx.Err(); err != nil {
		return fail(err)
	}
	if id, err := uuid.Parse(namespace); err != nil || id.String() != namespace || limit < 1 || limit > MaxTranslationPageSize {
		return fail(ErrInvalidArgument)
	}
	if current != (TranslationAuditProgress{}) &&
		(ValidateTranslationTargetAuditProgress(current) != nil || current.NamespaceUID != namespace) {
		return fail(ErrRepairRequired)
	}
	epoch, err := store.ReadTranslationTargetCoverageEpoch(ctx, namespace)
	if err != nil {
		return fail(err)
	}
	if id, err := uuid.Parse(epoch); err != nil || id.String() != epoch {
		return fail(ErrRepairRequired)
	}
	digest := translation.CapabilityDigest()
	next := current
	if current == (TranslationAuditProgress{}) || current.DescriptorDigest != digest ||
		(current.Phase != "Repairing" && current.TargetMutationEpoch != epoch) {
		next = TranslationAuditProgress{
			NamespaceUID: namespace, RunID: uuid.NewString(), Phase: "Repairing", DescriptorDigest: digest,
		}
	} else if current.Phase == "Verified" {
		return current, nil
	}
	page, err := store.ListTranslationTargetSourceAudit(ctx, namespace, next.Cursor, limit)
	if err != nil {
		return fail(err)
	}
	if err := validateTranslationTargetSourceAuditPage(namespace, next.Cursor, limit, page); err != nil {
		return fail(err)
	}
	for _, hint := range page.Items {
		if err := ctx.Err(); err != nil {
			return fail(err)
		}
		err := store.VerifyTranslationTargetSourceCoverage(ctx, hint)
		if errors.Is(err, ErrRepairRequired) && next.Phase == "Repairing" {
			err = store.RecoverTranslationTargetSourceAudit(ctx, hint)
			if err == nil {
				err = store.VerifyTranslationTargetSourceCoverage(ctx, hint)
			}
		}
		if err != nil {
			return fail(err)
		}
	}
	if next.Scanned > math.MaxInt64-int64(page.Scanned) {
		return fail(ErrRepairRequired)
	}
	next.Scanned += int64(page.Scanned)
	next.Cursor = page.Next
	after, err := store.ReadTranslationTargetCoverageEpoch(ctx, namespace)
	if err != nil {
		return fail(err)
	}
	if id, err := uuid.Parse(after); err != nil || id.String() != after {
		return fail(ErrRepairRequired)
	}
	if err := ctx.Err(); err != nil {
		return fail(err)
	}
	if next.Phase == "Verifying" && after != epoch {
		return fail(ErrConflict)
	}
	switch next.Phase {
	case "Repairing":
		if next.Cursor == "" {
			next.Phase, next.TargetMutationEpoch = "Verifying", after
		}
	case "Verifying":
		if next.TargetMutationEpoch != epoch {
			return fail(ErrConflict)
		}
		if next.Cursor == "" {
			next.Phase = "Verified"
		}
	}
	next.ResourceVersion = "1"
	if current.ResourceVersion != "" {
		next.ResourceVersion, err = AdvanceTranslationIndexVersion(current.ResourceVersion)
		if err != nil {
			return fail(err)
		}
	}
	return next, nil
}

// Readers must recheck the epoch, not trust a persisted Verified phase alone.
func ReadTranslationTargetAuditCoverage(ctx context.Context, store TranslationTargetCoverageReader, namespace string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if id, err := uuid.Parse(namespace); err != nil || id.String() != namespace {
		return "", ErrInvalidArgument
	}
	progress, err := store.GetTranslationTargetAuditProgress(ctx, namespace)
	if err != nil {
		return "", err
	}
	if ValidateTranslationTargetAuditProgress(progress) != nil || progress.NamespaceUID != namespace ||
		progress.Phase != "Verified" || progress.DescriptorDigest != translation.CapabilityDigest() {
		return "", ErrRepairRequired
	}
	epoch, err := store.ReadTranslationTargetCoverageEpoch(ctx, namespace)
	if err != nil {
		return "", err
	}
	if epoch != progress.TargetMutationEpoch {
		return "", ErrConflict
	}
	return epoch, nil
}

// Recovery rechecks every source observation and the namespace mutation epoch
// before returning its continuation.
// The resulting page is not a namespace-wide mutation or completeness proof.
func RecoverTranslationTargetSourceAuditPage(ctx context.Context, store TranslationTargetSourceAuditStore, namespace, cursor string, limit int) (TranslationTargetSourceAuditPage, error) {
	fail := func(err error) (TranslationTargetSourceAuditPage, error) {
		return TranslationTargetSourceAuditPage{}, err
	}
	if err := ctx.Err(); err != nil {
		return fail(err)
	}
	if id, err := uuid.Parse(namespace); err != nil || id.String() != namespace ||
		limit < 1 || limit > MaxTranslationPageSize || len(cursor) > 32<<10 {
		return fail(ErrInvalidArgument)
	}
	readEpoch := func() (string, error) {
		epoch, err := store.ReadTranslationTargetMutationEpoch(ctx, namespace)
		if err != nil {
			return "", err
		}
		if id, err := uuid.Parse(epoch); err != nil || id.String() != epoch {
			return "", ErrRepairRequired
		}
		return epoch, nil
	}
	epoch, err := readEpoch()
	if err != nil {
		return fail(err)
	}
	page, err := store.ListTranslationTargetSourceAudit(ctx, namespace, cursor, limit)
	if err != nil {
		return fail(err)
	}
	if err := validateTranslationTargetSourceAuditPage(namespace, cursor, limit, page); err != nil {
		return fail(err)
	}
	for range 2 {
		for _, hint := range page.Items {
			if err := ctx.Err(); err != nil {
				return fail(err)
			}
			if err := store.RecoverTranslationTargetSourceAudit(ctx, hint); err != nil {
				return fail(err)
			}
		}
		current, err := readEpoch()
		if err != nil {
			return fail(err)
		}
		if current != epoch {
			return fail(ErrConflict)
		}
	}
	if err := ctx.Err(); err != nil {
		return fail(err)
	}
	return page, nil
}

func ValidateTranslationTargetSourceAuditHint(hint TranslationTargetSourceAuditHint) error {
	_, targetErr := TranslationTargetFenceID(hint.Target)
	id, uidErr := uuid.Parse(hint.UID)
	if targetErr != nil || uidErr != nil || id.String() != hint.UID ||
		len(hint.ResourceVersion) > 256 || hint.CreationTimestamp.IsZero() ||
		hint.Namespace == "" || len(hint.Namespace) > 253 {
		return ErrRepairRequired
	}
	if _, err := AdvanceTranslationIndexVersion(hint.ResourceVersion); err != nil {
		return ErrRepairRequired
	}
	return nil
}

func validateTranslationTargetSourceAuditPage(namespace, cursor string, limit int, page TranslationTargetSourceAuditPage) error {
	if page.Scanned < 0 || page.Scanned > limit || len(page.Items) > page.Scanned ||
		len(page.Next) > 32<<10 || (page.Next != "" && page.Next == cursor) {
		return ErrRepairRequired
	}
	seen := make(map[string]struct{}, len(page.Items))
	for _, hint := range page.Items {
		if ValidateTranslationTargetSourceAuditHint(hint) != nil || hint.Target.NamespaceUID != namespace {
			return ErrRepairRequired
		}
		key := hint.Target.APIVersion + "/" + hint.Target.Kind + "/" + hint.UID
		if _, duplicate := seen[key]; duplicate {
			return ErrRepairRequired
		}
		seen[key] = struct{}{}
	}
	return nil
}

// Coverage proves a recoverable head, not current material or target absence.
// Material/token drift is allowed only under a separately checked coverage epoch.
func ValidateTranslationTargetSourceCoverage(hint TranslationTargetSourceAuditHint, source TranslationTargetSourceIdentity, fence *TranslationTargetFence) error {
	if ValidateTranslationTargetSourceAuditHint(hint) != nil ||
		source.NamespaceUID != hint.Target.NamespaceUID || source.ContentDigest == "" || len(source.ContentDigest) > 256 ||
		ValidateTranslationTargetFence(fence) != nil || fence.Target != hint.Target ||
		(fence.Evidence != nil && fence.Evidence.Namespace != hint.Namespace) {
		return ErrRepairRequired
	}
	if token, err := uuid.Parse(source.WriteToken); err != nil || token.String() != source.WriteToken {
		return ErrRepairRequired
	}
	if fence.Phase == TranslationTargetStable && fence.Evidence != nil &&
		fence.Evidence.UID == hint.UID && fence.CreationTimestamp.Equal(hint.CreationTimestamp) {
		return nil
	}
	if fence.Phase == TranslationTargetUpdating && fence.Intent != nil &&
		(fence.Intent.PreviousUID == hint.UID || fence.Intent.NextUID == hint.UID) &&
		fence.Intent.CreationTimestamp.Equal(hint.CreationTimestamp) {
		return nil
	}
	return ErrConflict
}

type translationTargetSourceAuditCursor struct {
	Layout int    `json:"layout"`
	State  []byte `json:"state,omitempty"`
}

func DecodeTranslationTargetSourceAuditCursor(namespace, backend, cursor string, limit int) (int, []byte, error) {
	raw, err := decodeTranslationAuditCursor(namespace, backend, cursor, limit, "TargetSource")
	if err != nil {
		return 0, nil, err
	}
	if len(raw) == 0 {
		return 0, nil, nil
	}
	var decoded translationTargetSourceAuditCursor
	if json.Unmarshal(raw, &decoded) != nil || decoded.Layout < 0 || decoded.Layout > 4 {
		return 0, nil, ErrInvalidArgument
	}
	return decoded.Layout, decoded.State, nil
}

func EncodeTranslationTargetSourceAuditCursor(namespace, backend string, layout int, state []byte) (string, error) {
	if layout < 0 || layout > 4 {
		return "", ErrInvalidArgument
	}
	raw, err := json.Marshal(translationTargetSourceAuditCursor{Layout: layout, State: state})
	if err != nil {
		return "", err
	}
	return encodeTranslationAuditCursor(namespace, backend, raw, "TargetSource")
}
