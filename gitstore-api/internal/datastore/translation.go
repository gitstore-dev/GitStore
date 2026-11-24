// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package datastore

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/gitstore-dev/gitstore/api/internal/catalog"
	"github.com/gitstore-dev/gitstore/translation"
	"github.com/google/uuid"
)

const (
	MaxTranslationPageSize     = 256
	TranslationIndexShards     = 64
	MaxTranslationSegmentRows  = 16_384
	MaxTranslationSegmentBytes = 64 << 20
	MaxTranslationIndexProbes  = 16
	MaxTranslationEpochs       = 256
)

// TranslationCapable keeps disabled deployments and resource-only test doubles
// compatible. Activation additionally requires the binding, index and fence stores.
type TranslationCapable interface {
	TranslationStore() TranslationStore
}

type TranslationStore interface {
	CreateTranslation(context.Context, *Translation) error
	GetTranslation(context.Context, string) (*Translation, error)
	GetTranslationByName(context.Context, string, string) (*Translation, error)
	ListTranslations(context.Context, string, PageParams) (*TranslationPage, error)
	UpdateTranslation(context.Context, *Translation, string) error
	UpdateTranslationStatus(context.Context, string, string, json.RawMessage) (*Translation, error)
	DeleteTranslation(context.Context, string, string) error
	// RequestTranslationRevalidation coalesces one durable journal hint per
	// source lifetime/version without changing the public resource or binding.
	RequestTranslationRevalidation(context.Context, *Translation) (bool, error)
}

func ValidateTranslationRevalidationRequest(source *Translation) error {
	if source == nil || source.CreationTimestamp.IsZero() || source.ResourceVersion == "" || len(source.ResourceVersion) > 256 {
		return ErrInvalidArgument
	}
	for _, raw := range []string{source.UID, source.NamespaceID} {
		if id, err := uuid.Parse(raw); err != nil || id.String() != raw {
			return ErrInvalidArgument
		}
	}
	return nil
}

type TranslationPage struct {
	PageResult[Translation]
	NextCursor, PreviousCursor string
	ItemCursors                map[string]string
}

func (p PageParams) BoundedTranslationPage() (PageParams, error) {
	if p.First < 0 || p.Last < 0 || (p.First > 0 && p.Last > 0) ||
		(p.After != "" && p.Before != "") || (p.Last > 0 && p.After != "") ||
		(p.Before != "" && p.Last == 0) {
		return PageParams{}, ErrInvalidArgument
	}
	p.First = min(p.First, MaxTranslationPageSize)
	p.Last = min(p.Last, MaxTranslationPageSize)
	return p, nil
}

type translationCursor struct {
	Version int
	Scope   string
	PageCursor
}

func EncodeTranslationCursor(scope string, position PageCursor) string {
	raw, _ := json.Marshal(translationCursor{Version: 1, Scope: scope, PageCursor: position})
	return base64.RawURLEncoding.EncodeToString(raw)
}

func DecodeTranslationCursor(scope, encoded string) (PageCursor, error) {
	var cursor translationCursor
	if len(encoded) > 2048 {
		return PageCursor{}, ErrInvalidArgument
	}
	raw, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil || json.Unmarshal(raw, &cursor) != nil || cursor.Version != 1 || cursor.Scope != scope {
		return PageCursor{}, ErrInvalidArgument
	}
	if _, err := uuid.Parse(cursor.ID); err != nil || cursor.CreatedAt.IsZero() {
		return PageCursor{}, ErrInvalidArgument
	}
	return cursor.PageCursor, nil
}

func ValidateTranslationSource(source *Translation) error {
	if source == nil || source.APIVersion != "i18n.gitstore.dev/v1beta1" || source.Kind != "Translation" ||
		source.Namespace == "" || source.Name == "" || source.NamespaceID == "" || source.CreationTimestamp.IsZero() ||
		source.Generation < 1 || source.ResourceVersion == "" || len(source.ResourceVersion) > 256 || strings.ContainsRune(source.Namespace, 0) ||
		len(source.RawSource) == 0 || len(source.RawSource) > translation.MaxDocumentBytes ||
		len(source.Spec) > translation.MaxDocumentBytes || len(source.Body) > translation.MaxDocumentBytes ||
		len(source.Status) > translation.MaxDocumentBytes || len(source.OwnerReferences) > translation.MaxDocumentBytes ||
		!utf8.Valid(source.RawSource) || !utf8.ValidString(source.Body) || !utf8.Valid(source.Spec) ||
		!json.Valid(source.Spec) ||
		(len(source.Status) > 0 && !json.Valid(source.Status)) {
		return ErrInvalidArgument
	}
	if len(source.OwnerReferences) > 0 {
		var references []catalog.OwnerReference
		if err := json.Unmarshal(source.OwnerReferences, &references); err != nil {
			return ErrInvalidArgument
		}
	}
	if _, err := uuid.Parse(source.UID); err != nil {
		return ErrInvalidArgument
	}
	return nil
}

func ApplyTranslationStatus(source *Translation, expected string, raw json.RawMessage) error {
	if source == nil {
		return ErrInvalidArgument
	}
	if source.ResourceVersion != expected {
		return ErrConflict
	}
	if len(raw) > translation.MaxDocumentBytes || !json.Valid(raw) {
		return ErrInvalidArgument
	}
	var status catalog.TranslationStatus
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&status); err != nil {
		return fmt.Errorf("%w: invalid Translation observation: %v", ErrInvalidArgument, err)
	}
	if status.ObservedGeneration != source.Generation || status.LastAppliedRevision != source.Revision {
		return fmt.Errorf("%w: observation does not match accepted source", ErrConflict)
	}
	source.Status = append(json.RawMessage(nil), raw...)
	source.ResourceVersion = nextResourceVersion(source.ResourceVersion)
	return nil
}

type TranslationTargetKey struct {
	NamespaceUID, APIVersion, Kind, Name string
}

// Epochs retain first-bound UIDs even when optional authored pins are toggled.
type TranslationBindingEpoch struct {
	Epoch      string
	Target     TranslationTargetKey
	UIDPin     string
	VersionPin string
	BoundUID   string
}

type TranslationBinding struct {
	TranslationUID  string
	ResourceVersion string
	SourceVersion   string
	CurrentEpoch    string
	Epochs          []TranslationBindingEpoch
}

type TranslationBindingCapable interface {
	TranslationBindingStore() TranslationBindingStore
}

type TranslationBindingStore interface {
	GetTranslationBinding(context.Context, string) (*TranslationBinding, error)
	CompareAndSwapTranslationBinding(context.Context, *TranslationBinding, string) error
}

type TranslationIndexKind string

const (
	TranslationNamespaceIndex                  TranslationIndexKind = "Namespace"
	TranslationTargetNameIndex                 TranslationIndexKind = "TargetName"
	TranslationTargetUIDIndex                  TranslationIndexKind = "TargetUID"
	TranslationTargetPendingIndex              TranslationIndexKind = "TargetPending"
	TranslationTargetMaintenanceNamespaceIndex TranslationIndexKind = "TargetMaintenanceNamespace"
)

type TranslationIndexKey struct {
	Kind         TranslationIndexKind
	NamespaceUID string
	Target       TranslationTargetKey
	TargetUID    string
	Shard        uint8
	Segment      uint64
}

type TranslationIndexEntry struct {
	TranslationUID, SourceVersion, ReferenceEpoch string
	CreationTimestamp                             time.Time
	Slot                                          uint32
}

type TranslationIndexReservation struct {
	Index                                  TranslationIndexKey
	Entry                                  TranslationIndexEntry
	Token                                  string
	ResourceVersion                        string
	PayloadBytes                           int64
	Phase                                  TranslationReservationPhase
	ExpectedHeadVersion, AllocationVersion string
	ReservedBytes                          int64
}

type TranslationReservationPhase string

const (
	TranslationReservationPreparing TranslationReservationPhase = "Preparing"
	TranslationReservationReserved  TranslationReservationPhase = "Reserved"
	TranslationReservationCommitted TranslationReservationPhase = "Committed"
	TranslationReservationRemoved   TranslationReservationPhase = "Removed"
)

type TranslationIndexHead struct {
	Index           TranslationIndexKey
	ResourceVersion string
	Segment         uint64
	NextSlot        uint32
	ReservedBytes   int64
	PendingToken    string
}

type TranslationDependentPage struct {
	Items  []TranslationIndexEntry
	Next   string
	Probes int
}

type TranslationTargetMaintenanceStore interface {
	ListTranslationTargetMaintenanceNamespaces(context.Context, string, int) (TranslationDependentPage, error)
	RecoverTranslationTargetMaintenance(context.Context, string, string, int) (TranslationDependentPage, error)
	RecoverTranslationTargetMaintenanceTurn(context.Context) error
}

type TranslationTargetMaintenanceCapable interface {
	TranslationTargetMaintenanceStore() TranslationTargetMaintenanceStore
}

// Maintenance is a metadata view of source-authoritative work, not a second
// writable queue. Namespace anchors discover pending and unpublished sources.
type TranslationMaintenanceIntent struct {
	ID, NamespaceUID, TranslationUID, SourceVersion, ResourceVersion, Phase string
	CreationTimestamp                                                       time.Time
}

type TranslationMaintenancePage struct {
	Items  []TranslationMaintenanceIntent
	Next   string
	Probes int
}

type TranslationIndexCapable interface {
	TranslationIndexStore() TranslationIndexStore
}

type TranslationIndexStore interface {
	ReserveTranslationIndex(context.Context, TranslationIndexKey, TranslationIndexEntry, string, int64) (TranslationIndexReservation, error)
	GetTranslationIndexReservation(context.Context, string) (TranslationIndexReservation, error)
	CommitTranslationIndex(context.Context, TranslationIndexReservation) error
	RemoveTranslationIndex(context.Context, TranslationIndexReservation) error
	ListTranslationDependents(context.Context, TranslationTargetKey, string, string, int) (TranslationDependentPage, error)
	GetTranslationMaintenance(context.Context, string) (*TranslationMaintenanceIntent, error)
	ListTranslationMaintenance(context.Context, string, string, int) (TranslationMaintenancePage, error)
	RecoverTranslation(context.Context, string) error
}

type TranslationTargetPhase string

const (
	TranslationTargetStable   TranslationTargetPhase = "STABLE"
	TranslationTargetUpdating TranslationTargetPhase = "UPDATING"
)

type TranslationTargetWriteIntent struct {
	ID, RepositoryID, GitRef, GitCommitSHA, SourcePath string
	PreviousUID, NextUID, ExpectedResourceVersion      string
	SourceDigest                                       string
	SourceProof                                        string
	Deleting                                           bool
	Invalidating                                       bool
	Backfilling                                        bool `json:",omitempty"`
	CreationTimestamp                                  time.Time
}

// TranslationTargetSourceIdentity is private datastore evidence, never authored
// metadata or a substitute for matching the current name fence.
type TranslationTargetSourceIdentity struct {
	NamespaceUID  string
	WriteToken    string
	ContentDigest string
}

type TranslationTargetFence struct {
	Target            TranslationTargetKey
	Token             string
	Phase             TranslationTargetPhase
	Evidence          *catalog.TranslationTargetEvidence
	Intent            *TranslationTargetWriteIntent
	CreationTimestamp time.Time
}

type TranslationTargetWriteFenceStore interface {
	GetTranslationTargetFence(context.Context, TranslationTargetKey) (*TranslationTargetFence, error)
	BeginTranslationTargetWrite(context.Context, TranslationTargetKey, TranslationTargetWriteIntent, string) (*TranslationTargetFence, error)
	CompleteTranslationTargetWrite(context.Context, TranslationTargetKey, string, *catalog.TranslationTargetEvidence) error
}

type TranslationTargetFenceStore interface {
	TranslationTargetWriteFenceStore
	ReadTargetEvidence(context.Context, TranslationTargetKey) (*catalog.TranslationTargetEvidence, error)
}
