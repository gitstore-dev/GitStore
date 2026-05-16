// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

// Datastore contract extension for feature 009-api-namespaces.
//
// This file is a design artifact — it is NOT compiled.
// The authoritative implementation lives at:
//   gitstore-api/internal/datastore/datastore.go   (interface)
//   gitstore-api/internal/datastore/entities.go    (Namespace struct)
//   gitstore-api/internal/datastore/memdb/         (memdb backend)
//   gitstore-api/internal/datastore/scylla/        (scylla backend)
//   gitstore-api/internal/datastore/instrumented.go (wrapper)

package datastore

import (
	"context"
	"time"
)

// ─── NamespaceTier ────────────────────────────────────────────────────────────

// NamespaceTier is the enumeration of allowed namespace tiers.
type NamespaceTier string

const (
	NamespaceTierUser         NamespaceTier = "user"
	NamespaceTierOrganisation NamespaceTier = "organisation"
	NamespaceTierEnterprise   NamespaceTier = "enterprise"
)

// ─── Namespace entity ─────────────────────────────────────────────────────────

// Namespace is the primary isolation boundary for repositories.
// Identifier is globally unique across all tiers.
type Namespace struct {
	ID                 string
	Identifier         string        // DNS label, globally unique, lowercase, 1–63 chars
	DisplayName        string        // optional
	Tier               NamespaceTier // user | organisation | enterprise
	ParentEnterpriseID *string       // optional UUID; organisation tier only
	CreatedAt          time.Time
	CreatedBy          string // username of creator
	UpdatedAt          time.Time
	UpdatedBy          string // username of last modifier
}

// ─── Datastore interface extension ───────────────────────────────────────────

// The following methods are added to the existing Datastore interface.
// They follow the same error contract as all existing methods:
//   - ErrNotFound:       entity does not exist
//   - ErrAlreadyExists:  identifier conflict on CreateNamespace
//   - ErrInvalidArgument: empty id or nil namespace

type DatastoreWithNamespaces interface {
	Datastore // all existing methods

	// CreateNamespace stores a new namespace.
	// Returns ErrAlreadyExists if a namespace with the same identifier already exists.
	CreateNamespace(ctx context.Context, ns *Namespace) error

	// GetNamespace retrieves a namespace by its UUID.
	// Returns ErrNotFound if the namespace does not exist.
	GetNamespace(ctx context.Context, id string) (*Namespace, error)

	// GetNamespaceByIdentifier retrieves a namespace by its human-readable identifier.
	// Returns ErrNotFound if no namespace has the given identifier.
	GetNamespaceByIdentifier(ctx context.Context, identifier string) (*Namespace, error)

	// ListNamespaces returns all namespaces. Order is implementation-defined.
	ListNamespaces(ctx context.Context) ([]*Namespace, error)

	// DeleteNamespace removes a namespace by its UUID.
	// Returns ErrNotFound if the namespace does not exist.
	// The caller is responsible for verifying the namespace has no repositories
	// before calling (enforced at the service layer, not here).
	DeleteNamespace(ctx context.Context, id string) error
}

// ─── memdb schema addition ───────────────────────────────────────────────────

// Add to the memdb DBSchema.Tables map in schema.go:
//
// "namespaces": {
//     Name: "namespaces",
//     Indexes: map[string]*memdb.IndexSchema{
//         "id": {
//             Name:    "id",
//             Unique:  true,
//             Indexer: &memdb.StringFieldIndex{Field: "ID"},
//         },
//         "identifier": {
//             Name:    "identifier",
//             Unique:  true,
//             Indexer: &memdb.StringFieldIndex{Field: "Identifier"},
//         },
//         "tier": {
//             Name:    "tier",
//             Unique:  false,
//             Indexer: &memdb.StringFieldIndex{Field: "Tier"},
//         },
//     },
// },
