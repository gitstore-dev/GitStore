// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package datastore

import (
	"context"
	"encoding/base64"
	"encoding/json"

	"github.com/google/uuid"
)

type TranslationSourceAuditPage struct {
	Items   []TranslationMaintenanceIntent
	Next    string
	Scanned int
}

// Source audit discovery does not depend on derived anchors or UID locators.
// A scan is not a completeness proof; certification requires mutation fencing.
type TranslationSourceAuditStore interface {
	ListTranslationSourceAudit(context.Context, string, string, int) (TranslationSourceAuditPage, error)
	RecoverTranslationSourceAudit(context.Context, TranslationMaintenanceIntent) error
	GetTranslationSourceAuditWatermark(context.Context, string) (*TranslationMaintenanceIntent, error)
}

type TranslationIndexAuditPage struct {
	Tokens  []string
	Next    string
	Scanned int
}

// TranslationIndexAuditStore visits source-index reservation authority.
// It does not certify coverage of thin cells lacking reservation records.
type TranslationIndexAuditStore interface {
	ListTranslationIndexAudit(context.Context, string, string, int) (TranslationIndexAuditPage, error)
	RecoverTranslationIndexAudit(context.Context, string, string) error
}

type TranslationAuditProgress struct {
	NamespaceUID        string
	RunID               string
	ResourceVersion     string
	Phase               string
	Cursor              string
	Scanned             int64
	TargetMutationEpoch string
	DescriptorDigest    string
}

// Scanned progress records traversal, never mutation-fenced completeness.
type TranslationAuditRepairStore interface {
	RepairTranslationAuditTurn(context.Context, string, int) (TranslationAuditProgress, error)
	GetTranslationAuditProgress(context.Context, string) (TranslationAuditProgress, error)
}

func IsTranslationSourceIndex(kind TranslationIndexKind) bool {
	return kind == TranslationNamespaceIndex || kind == TranslationTargetNameIndex || kind == TranslationTargetUIDIndex
}

// RecoverTranslationSourceAuditPage returns bounded, rechecked source versions,
// not a namespace-wide repair certificate. Consumers must fence any persisted
// watermark against subsequent source mutations before relying on completeness.
func RecoverTranslationSourceAuditPage(ctx context.Context, store TranslationSourceAuditStore, namespace, cursor string, limit int) (TranslationSourceAuditPage, error) {
	fail := func(err error) (TranslationSourceAuditPage, error) {
		return TranslationSourceAuditPage{}, err
	}
	if err := ctx.Err(); err != nil {
		return fail(err)
	}
	if id, err := uuid.Parse(namespace); err != nil || id.String() != namespace ||
		limit < 1 || limit > MaxTranslationPageSize || len(cursor) > 32<<10 {
		return fail(ErrInvalidArgument)
	}
	page, err := store.ListTranslationSourceAudit(ctx, namespace, cursor, limit)
	if err != nil {
		return fail(err)
	}
	if page.Scanned < 0 || page.Scanned > limit || len(page.Items) > page.Scanned ||
		len(page.Next) > 32<<10 || (page.Next != "" && page.Next == cursor) {
		return fail(ErrRepairRequired)
	}
	seen := make(map[string]struct{}, len(page.Items))
	for _, hint := range page.Items {
		if err := ValidateTranslationSourceAuditHint(hint); err != nil || hint.NamespaceUID != namespace {
			return fail(ErrRepairRequired)
		}
		if _, duplicate := seen[hint.TranslationUID]; duplicate {
			return fail(ErrRepairRequired)
		}
		seen[hint.TranslationUID] = struct{}{}
	}
	recovered := make([]TranslationMaintenanceIntent, 0, len(page.Items))
	for _, hint := range page.Items {
		if err := ctx.Err(); err != nil {
			return fail(err)
		}
		if err := store.RecoverTranslationSourceAudit(ctx, hint); err != nil {
			return fail(err)
		}
		state, err := store.GetTranslationSourceAuditWatermark(ctx, hint.TranslationUID)
		if err != nil {
			return fail(err)
		}
		if state == nil || ValidateTranslationSourceAuditHint(*state) != nil ||
			state.NamespaceUID != namespace || state.TranslationUID != hint.TranslationUID ||
			!state.CreationTimestamp.Equal(hint.CreationTimestamp) ||
			(state.Phase != "Stable" && state.Phase != "Retired") {
			return fail(ErrRepairRequired)
		}
		recovered = append(recovered, *state)
	}
	for _, accepted := range recovered {
		if err := ctx.Err(); err != nil {
			return fail(err)
		}
		current, err := store.GetTranslationSourceAuditWatermark(ctx, accepted.TranslationUID)
		if err != nil {
			return fail(err)
		}
		if current == nil || !accepted.CreationTimestamp.Equal(current.CreationTimestamp) {
			return fail(ErrConflict)
		}
		accepted.CreationTimestamp = current.CreationTimestamp
		if accepted != *current {
			return fail(ErrConflict)
		}
	}
	if err := ctx.Err(); err != nil {
		return fail(err)
	}
	if len(page.Items) > 0 {
		page.Items = recovered
	}
	return page, nil
}

// Validation bounds a discovery hint; it does not establish source authority.
func ValidateTranslationSourceAuditHint(hint TranslationMaintenanceIntent) error {
	for _, value := range []string{hint.NamespaceUID, hint.TranslationUID} {
		if id, err := uuid.Parse(value); err != nil || id.String() != value {
			return ErrInvalidArgument
		}
	}
	if hint.CreationTimestamp.IsZero() || hint.SourceVersion == "" || len(hint.SourceVersion) > 256 || len(hint.ResourceVersion) > 256 ||
		hint.ID != hint.TranslationUID+"/"+hint.ResourceVersion {
		return ErrInvalidArgument
	}
	if hint.Phase == "Unpublished" {
		if hint.ResourceVersion != "0" || hint.SourceVersion != "1" {
			return ErrInvalidArgument
		}
		return nil
	}
	switch hint.Phase {
	case "Creating", "Stable", "Updating", "Deleting", "Retired":
	default:
		return ErrInvalidArgument
	}
	if _, err := AdvanceTranslationIndexVersion(hint.ResourceVersion); err != nil {
		return ErrInvalidArgument
	}
	return nil
}

type translationSourceAuditCursor struct {
	Version      int    `json:"v"`
	NamespaceUID string `json:"namespaceUID"`
	Backend      string `json:"backend"`
	Scope        string `json:"scope,omitempty"`
	Payload      []byte `json:"payload"`
}

func DecodeTranslationSourceAuditCursor(namespace, backend, cursor string, limit int) ([]byte, error) {
	return decodeTranslationAuditCursor(namespace, backend, cursor, limit, "")
}

func DecodeTranslationIndexAuditCursor(namespace, backend, cursor string, limit int) ([]byte, error) {
	return decodeTranslationAuditCursor(namespace, backend, cursor, limit, "IndexReservation")
}

func decodeTranslationAuditCursor(namespace, backend, cursor string, limit int, scope string) ([]byte, error) {
	if id, err := uuid.Parse(namespace); err != nil || id.String() != namespace ||
		(backend != "memdb" && backend != "scylla") || limit < 1 || limit > MaxTranslationPageSize || len(cursor) > 32<<10 {
		return nil, ErrInvalidArgument
	}
	if cursor == "" {
		return nil, nil
	}
	raw, err := base64.RawURLEncoding.DecodeString(cursor)
	if err != nil {
		return nil, ErrInvalidArgument
	}
	var decoded translationSourceAuditCursor
	if json.Unmarshal(raw, &decoded) != nil || decoded.Version != 1 || decoded.NamespaceUID != namespace || decoded.Scope != scope ||
		decoded.Backend != backend || len(decoded.Payload) == 0 || len(decoded.Payload) > 16<<10 {
		return nil, ErrInvalidArgument
	}
	return decoded.Payload, nil
}

func EncodeTranslationSourceAuditCursor(namespace, backend string, payload []byte) (string, error) {
	return encodeTranslationAuditCursor(namespace, backend, payload, "")
}

func EncodeTranslationIndexAuditCursor(namespace, backend string, payload []byte) (string, error) {
	return encodeTranslationAuditCursor(namespace, backend, payload, "IndexReservation")
}

func encodeTranslationAuditCursor(namespace, backend string, payload []byte, scope string) (string, error) {
	if len(payload) == 0 || len(payload) > 16<<10 {
		return "", ErrInvalidArgument
	}
	if _, err := DecodeTranslationSourceAuditCursor(namespace, backend, "", 1); err != nil {
		return "", err
	}
	raw, err := json.Marshal(translationSourceAuditCursor{Version: 1, NamespaceUID: namespace, Backend: backend, Scope: scope, Payload: payload})
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}
