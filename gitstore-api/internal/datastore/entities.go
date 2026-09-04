// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package datastore

import (
	"encoding/json"
	"time"
)

// NamespaceTier is the enumeration of allowed namespace tiers.
type NamespaceTier string

const (
	NamespaceTierUser         NamespaceTier = "user"
	NamespaceTierOrganization NamespaceTier = "organisation"
)

// Namespace is the cluster-scoped isolation boundary for repositories.
type Namespace struct {
	APIVersion string
	Kind       string
	UID        string
	Name       string

	Generation      int64
	ResourceVersion string
	Revision        string

	CreationTimestamp time.Time
	CreationActor     string
	UpdateTimestamp   time.Time
	UpdateActor       string

	Labels          map[string]string
	Annotations     map[string]string
	OwnerReferences json.RawMessage
	Finalizers      []string

	DeletionTimestamp *time.Time

	SourcePath   string
	GitCommitSHA string
	GitRef       string

	Spec   json.RawMessage
	Body   string
	Status json.RawMessage

	Title string
	Tier  NamespaceTier

	// ID is retained during the datastore naming migration. New code uses UID.
	ID string
}

// NamespaceWatchEventType is the normalized durable Namespace transition.
type NamespaceWatchEventType string

const (
	NamespaceWatchAdded    NamespaceWatchEventType = "ADDED"
	NamespaceWatchModified NamespaceWatchEventType = "MODIFIED"
	NamespaceWatchDeleted  NamespaceWatchEventType = "DELETED"
	NamespaceWatchBookmark NamespaceWatchEventType = "BOOKMARK"
)

// NamespaceWatchCursor identifies one ordered event inside a journal epoch.
// Its external encoding is owned by internal/watchjournal.
type NamespaceWatchCursor struct {
	Epoch    string
	Sequence uint64
}

// NamespaceWatchEvent is the backend-neutral durable journal record. Payload
// is a full committed Namespace postimage for ADDED/MODIFIED and nil for
// DELETED/BOOKMARK. SelectorLabels preserves the postimage labels (or the
// last-known labels for DELETED), while PreviousSelectorLabels preserves the
// MODIFIED preimage labels. Together they let filtered watches express a
// resource entering or leaving a selector without exposing deleted payloads.
type NamespaceWatchEvent struct {
	Epoch                  string
	Sequence               uint64
	Type                   NamespaceWatchEventType
	Name                   string
	Payload                json.RawMessage
	SelectorLabels         map[string]string
	PreviousSelectorLabels map[string]string
	DeduplicationKey       string
	FencingToken           uint64
	At                     time.Time
}

// NamespaceWatchBounds is the retained interval in one journal epoch.
type NamespaceWatchBounds struct {
	Epoch     string
	Oldest    uint64
	HighWater uint64
	UpdatedAt time.Time
	// BookmarkAt is the timestamp of the latest actual durable BOOKMARK.
	// Unlike UpdatedAt, ordinary data events do not advance it.
	BookmarkAt time.Time
	// ProgressAt is the last time the CDC reader consumed a change or
	// successfully advanced an empty query window. Durable bookmarks do not
	// advance it.
	ProgressAt time.Time
}

// NamespaceWatchLease carries the materializer fencing token.
type NamespaceWatchLease struct {
	Holder       string
	FencingToken uint64
	ExpiresAt    time.Time
}

// NamespaceCDCProgress stores a source checkpoint or the published frontier's
// bounded recovery manifest after its journal append.
type NamespaceCDCProgress struct {
	StreamID  string
	Position  []byte
	UpdatedAt time.Time
}

// Product is the fully hydrated catalogue product record stored in the
// datastore. It merges author-supplied frontmatter (APIVersion, Kind,
// Namespace, Name, Labels, Annotations, Spec, Body) with system-assigned
// metadata (UID, ResourceVersion, Generation, CreationTimestamp, Revision)
// and system-written status (Status JSON). Git is the authoritative source
// for what the author wrote; the datastore is the authoritative source for
// what consumers read.
type Product struct {
	// Identity (primary key: Namespace + Name)
	UID       string // UUID assigned on first admission
	Namespace string
	Name      string

	// Resource envelope
	APIVersion string
	Kind       string

	// Versioning
	Generation        int64
	ResourceVersion   string
	CreationTimestamp time.Time
	CreationActor     string
	UpdateTimestamp   time.Time
	UpdateActor       string
	Revision          string // e.g. "main@sha1:abc123"

	// Author-supplied classification
	Labels      map[string]string
	Annotations map[string]string

	// Ownership and lifecycle
	OwnerReferences   json.RawMessage
	Finalizers        []string
	DeletionTimestamp *time.Time

	// Git provenance
	RepositoryID string
	SourcePath   string
	GitCommitSHA string
	GitRef       string

	// Spec and body — stored as JSON blob and raw Markdown respectively
	Spec json.RawMessage
	Body string

	// Status — system-only JSON blob
	Status json.RawMessage
}

type File struct {
	UID               string
	Namespace         string
	Name              string
	APIVersion        string
	Kind              string
	Generation        int64
	ResourceVersion   string
	CreationTimestamp time.Time
	CreationActor     string
	UpdateTimestamp   time.Time
	UpdateActor       string
	Revision          string
	Labels            map[string]string
	Annotations       map[string]string
	OwnerReferences   json.RawMessage
	Finalizers        []string
	DeletionTimestamp *time.Time
	RepositoryID      string
	SourcePath        string
	GitCommitSHA      string
	GitRef            string
	Spec              json.RawMessage
	Body              string
	Status            json.RawMessage
}

// CategoryTaxonomy is the git-backed Kubernetes-style category resource.
// Mirrors the Product entity structure. Distinct from the legacy Category entity.
type CategoryTaxonomy struct {
	// Identity (primary key: Namespace + Name)
	UID       string
	Namespace string
	Name      string

	// Resource envelope
	APIVersion string
	Kind       string

	// Versioning
	Generation        int64
	ResourceVersion   string
	CreationTimestamp time.Time
	CreationActor     string
	UpdateTimestamp   time.Time
	UpdateActor       string
	Revision          string // e.g. "main@sha1:abc123"

	// Author-supplied classification
	Labels      map[string]string
	Annotations map[string]string

	// Ownership and lifecycle
	OwnerReferences   json.RawMessage
	Finalizers        []string
	DeletionTimestamp *time.Time

	// Hierarchy — adjacency pointer + materialized path
	ParentName   string // spec.parentRef.name; empty string for root categories
	AncestorPath string // slash-separated from root to self, e.g. "electronics/computers/laptops"

	// Git provenance
	RepositoryID string
	SourcePath   string
	GitCommitSHA string
	GitRef       string

	// Spec and body
	Spec json.RawMessage // JSON-encoded CategoryTaxonomySpec
	Body string          // Markdown description

	// Status — system-only JSON blob (written at admission; controller fills Resolved fields)
	Status json.RawMessage
}

// Collection is the git-backed Kubernetes-style collection resource.
// Membership is determined at query time via label selector evaluation,
// not by a stored product list.
type Collection struct {
	// Identity (primary key: Namespace + Name)
	UID       string
	Namespace string
	Name      string

	// Resource envelope
	APIVersion string
	Kind       string

	// Versioning
	Generation        int64
	ResourceVersion   string
	CreationTimestamp time.Time
	CreationActor     string
	UpdateTimestamp   time.Time
	UpdateActor       string
	Revision          string // e.g. "main@sha1:abc123"

	// Author-supplied classification
	Labels      map[string]string
	Annotations map[string]string

	// Ownership and lifecycle
	OwnerReferences   json.RawMessage
	Finalizers        []string
	DeletionTimestamp *time.Time

	// Git provenance
	RepositoryID string
	SourcePath   string
	GitCommitSHA string
	GitRef       string

	// Spec and body
	Spec json.RawMessage // JSON-encoded CollectionSpec
	Body string          // Markdown description

	// Status — system-only JSON blob
	Status json.RawMessage
}

// ProductVariant is the purchasable SKU unit. A Product is the non-sellable
// parent descriptor; each ProductVariant carries its own pricing, inventory,
// and selected option combination.
//
// SKU and ProductRefName are denormalised from Spec to support memdb
// field-based indexing without JSON parsing on every lookup.
type ProductVariant struct {
	// Identity (primary key: Namespace + Name)
	UID       string
	Namespace string
	Name      string

	// Resource envelope
	APIVersion string
	Kind       string

	// Versioning
	Generation        int64
	ResourceVersion   string
	CreationTimestamp time.Time
	CreationActor     string
	UpdateTimestamp   time.Time
	UpdateActor       string
	Revision          string // e.g. "main@sha1:abc123"

	// Author-supplied classification
	Labels      map[string]string
	Annotations map[string]string

	// Ownership and lifecycle
	OwnerReferences   json.RawMessage
	Finalizers        []string
	DeletionTimestamp *time.Time

	// Denormalised index fields (always kept in sync with Spec)
	SKU            string // spec.sku
	ProductRefName string // spec.productRef.name

	// Git provenance
	RepositoryID string
	SourcePath   string
	GitCommitSHA string
	GitRef       string

	// Spec and body
	Spec json.RawMessage // JSON-encoded ProductVariantSpec
	Body string          // Markdown description

	// Status — system-only JSON blob
	Status json.RawMessage
}

// Repository represents a git repository with a stable internal identity.
// The physical storage path is derived from UID using the fanout formula and is never stored.
type Repository struct {
	APIVersion string
	Kind       string
	UID        string
	Namespace  string
	Name       string

	Generation      int64
	ResourceVersion string
	Revision        string

	CreationTimestamp time.Time
	CreationActor     string
	UpdateTimestamp   time.Time
	UpdateActor       string

	Labels          map[string]string
	Annotations     map[string]string
	OwnerReferences json.RawMessage
	Finalizers      []string

	DeletionTimestamp *time.Time

	RepositoryID string
	SourcePath   string
	GitCommitSHA string
	GitRef       string

	Spec   json.RawMessage
	Body   string
	Status json.RawMessage

	DefaultBranch string
	StorageClass  string

	// Push policy limits. Zero means no limit enforced (FR-015).
	MaxPackSizeBytes int64
	MaxFileSizeBytes int64

	// ID and NamespaceID are retained during the datastore naming migration.
	// New code uses UID and Namespace.
	ID          string
	NamespaceID string
}

// NamespaceMapping is the join record binding (Namespace, Name) → RepositoryID.
type NamespaceMapping struct {
	Namespace    string
	Name         string
	RepositoryID string

	// NamespaceID and RepoID are retained during the datastore naming migration.
	NamespaceID string
	RepoID      string
}

// ServiceAccount is the persistent, namespaced, non-human identity record
// backing the serviceaccount-assertion/serviceaccount-jwt AuthN providers
// (spec 061). Datastore-backed (memdb + Scylla) from the first implementation
// phase — unlike the assertion-replay cache and WebSocket connection
// registry, which are intentionally in-memory-only (single-instance scope).
type ServiceAccount struct {
	// Identity (primary key: Namespace + Name; subject string is derived as
	// "serviceaccount:<Namespace>:<Name>", never stored redundantly)
	UID       string // stable, survives Disabled toggles; changes only on delete+recreate
	Namespace string // convention string, e.g. "controllers" — not GitStore's Namespace resource
	Name      string // e.g. "gitstore-controller-manager" — names the *process*, not one of its reconcilers

	Disabled bool // true blocks new assertion exchange and new access-token authentication immediately

	Generation      int64  // advances only on PublicKeys change (author-controlled state)
	ResourceVersion string // advances on every persisted change, including Disabled toggles

	CreationTimestamp time.Time
	CreationActor     string // subject of the admin principal that created this record
	UpdateTimestamp   time.Time
	UpdateActor       string

	PublicKeys []ServiceAccountPublicKey

	DeletionTimestamp *time.Time // set on deleteServiceAccount; hard-delete is immediate in Phase 1 (no finalizer/Terminating lifecycle)
}

// ServiceAccountPublicKey is one enrolled public key, supporting an overlap
// window during rotation (multiple entries may be simultaneously valid).
type ServiceAccountPublicKey struct {
	KeyID      string // "kid" — protected-header value an assertion's kid must match
	Algorithm  string // "Ed25519" (preferred) or "ECDSA-P256"
	PublicKey  []byte // raw public key bytes (PEM-decoded at load, stored decoded)
	EnrolledAt time.Time
}
