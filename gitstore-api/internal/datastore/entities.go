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
// DELETED/BOOKMARK. SelectorLabels preserves the last-known labels needed to
// filter payload-less DELETED events without exposing a deleted resource.
type NamespaceWatchEvent struct {
	Epoch            string
	Sequence         uint64
	Type             NamespaceWatchEventType
	Name             string
	Payload          json.RawMessage
	SelectorLabels   map[string]string
	DeduplicationKey string
	FencingToken     uint64
	At               time.Time
}

// NamespaceWatchBounds is the retained interval in one journal epoch.
type NamespaceWatchBounds struct {
	Epoch     string
	Oldest    uint64
	HighWater uint64
	UpdatedAt time.Time
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

// NamespaceCDCProgress is saved per CDC stream only after its event append.
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
