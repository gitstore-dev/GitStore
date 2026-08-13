# Data Model: Namespace/Repository Deletion Ordering and System Repository Bootstrap

**Feature**: [spec.md](./spec.md) | **Plan**: [plan.md](./plan.md) | **Research**: [research.md](./research.md)

## Overview

No new resource kinds, no new GraphQL types, and no changes to the existing
`Namespace` or `Repository` entity struct shapes
(`gitstore-api/internal/datastore/entities.go:20-29,201-215`). This feature adds two
new read-only existence-check methods to the existing `Datastore` interface
(`gitstore-api/internal/datastore/datastore.go:134-201`) and changes the internal
behavior of three existing service methods
(`gitstore-api/internal/graph/resolver/service.go`). No datastore schema migration is
required for either backend. memdb indexes the existing `RepositoryID` fields in its
in-process schema; ScyllaDB uses the existing namespace partition and filters by the
stored `RepositoryID` with `LIMIT 1`. Repository membership uses the existing
`NamespaceID` index.

## Entities (unchanged)

### Namespace

Existing shape, no changes:

```go
type Namespace struct {
    ID          string
    Identifier  string
    DisplayName string
    Tier        NamespaceTier
    CreatedAt   time.Time
    CreatedBy   string
    UpdatedAt   time.Time
    UpdatedBy   string
}
```

Per research.md Decision 1, no `Status`/`deletionTimestamp`/finalizer fields are
added in this feature.

### Repository

Existing shape, no changes:

```go
type Repository struct {
    ID               string
    NamespaceID      string
    Name             string
    DefaultBranch    string
    StorageClass     string
    CreatedAt        time.Time
    CreatedBy        string
    UpdatedAt        time.Time
    UpdatedBy        string
    MaxPackSizeBytes int64
    MaxFileSizeBytes int64
}
```

Same note as above — no schema change.

### Catalog resources (Product, ProductVariant, CategoryTaxonomy, Collection)

Existing shape, no changes. Relevant existing field for this feature: `RepositoryID
string` (present on all four entities per `entities.go`), which is the field the new
existence check queries against.

## New Datastore interface methods

Added to the existing `Datastore` interface
(`gitstore-api/internal/datastore/datastore.go`), grouped with their respective
existing resource operations, following the file's existing grouping-by-resource
convention:

```go
// Repository operations
// HasCatalogResources reports whether at least one Product, ProductVariant,
// CategoryTaxonomy, or Collection record currently has RepositoryID == repoID.
// Used by DeleteRepository to enforce FR-004 (reject deletion while catalog
// resources remain). Must be an existence check (LIMIT 1 / equivalent), not a
// full count, per research.md Decision 3.
HasCatalogResources(ctx context.Context, repoID string) (bool, error)

// Namespace operations
// HasRepositories reports whether at least one Repository record currently has
// NamespaceID == namespaceID. Used by DeleteNamespace to enforce FR-001 (reject
// deletion while repositories remain). Must be an existence check, not a full
// count, per research.md Decision 3.
HasRepositories(ctx context.Context, namespaceID string) (bool, error)
```

**Placement rationale**: `HasCatalogResources` is grouped under "Repository
operations" (not under each of the four catalog resource operation groups) because
it is a single cross-cutting query answering one question — "does this repository
have anything blocking its deletion" — not four separate per-kind queries. This
avoids the `DeleteRepository` service method needing to call four separate `HasX`
methods and OR the results together.

**Error semantics**: Both methods return `(false, nil)` for "does not exist / has
none," matching the existing `bool, error` pattern used elsewhere in the codebase for
existence-style queries. Neither method returns `ErrNotFound` for a
namespace/repository that itself does not exist — the caller (`DeleteNamespace`/
`DeleteRepository`) already performs its own existence lookup of the namespace/
repository record before this check runs, so `HasRepositories`/`HasCatalogResources`
are only ever called with an ID already known to exist. If called with an unknown ID,
implementations return `(false, nil)` (vacuously "no blocking resources found"),
consistent with treating an absent parent as having no children.

## Backend implementation notes (for tasks.md, not prescriptive here)

- `gitstore-api/internal/datastore/memdb/backend.go`: implement via an indexed
  lookup on the existing `RepositoryID`/`NamespaceID` field (go-memdb supports
  index-based `First`/`Get` queries; use whichever existing index the schema already
  defines for these fields, or add a minimal index in `memdb/schema.go` if one does
  not already exist for `RepositoryID`/`NamespaceID` lookups — confirm at
  implementation time whether `ListRepositoriesByNamespace`'s existing index on
  `NamespaceID` can be reused directly for `HasRepositories`).
- `gitstore-api/internal/datastore/scylla/backend.go` (+ `repository.go` if the
  per-resource split file convention is followed): implement via a partition-scoped
  query (`WHERE repository_id = ? LIMIT 1` / `WHERE namespace_id = ? LIMIT 1`
  equivalent for the driver in use), avoiding `COUNT(*)` or an unscoped scan.
- `gitstore-api/internal/datastore/instrumented.go`: the metrics-wrapping decorator
  must forward both new methods with the same instrumentation pattern already applied
  to every other `Datastore` method in that file.
- `gitstore-api/internal/testutil/stubstore.go`: the test double must implement both
  new methods (test-controlled return values, matching the existing stub pattern for
  other `Datastore` methods in that file).

## Service-layer behavior changes (no new entities, described here for completeness)

### `CreateNamespace` (service.go:229-272)

After the existing namespace-record creation succeeds, attempt to create the
well-known `gitstore-system` repository for the new namespace (FR-007). Treat
`datastore.ErrAlreadyExists` from that nested create as a successful idempotent
outcome per research.md Decision 4 (FR-008) — do not surface it as an error to the
caller.

### `DeleteNamespace` (service.go:311-333)

Replace the `hasRepositories()` stub call with a real call to
`s.store.HasRepositories(ctx, ns.ID)`. If `true`, return the existing
`FailedPrecondition`-shaped `gqlerror` (message text already present at line 318,
behavior now actually enforced) instead of proceeding to `s.store.DeleteNamespace`.

### `DeleteRepository` (service.go:568-604)

Before any storage or metadata removal, call
`s.store.HasCatalogResources(ctx, repoID)`. If `true`, return a new
`FailedPrecondition`-shaped `gqlerror` (new error message, matching the existing
style of the namespace-deletion rejection) instead of proceeding to
`s.gitWriter.DeleteRepository`.
