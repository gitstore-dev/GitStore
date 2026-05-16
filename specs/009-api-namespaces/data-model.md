# Data Model: API-Driven Namespace Lifecycle Management

**Feature**: `009-api-namespaces` | **Date**: 2026-05-15

---

## Entities

### Namespace

The primary isolation boundary. Uniquely identified by a human-readable `identifier` (globally unique across all tiers) and a system-generated UUID `ID`.

| Field                | Type            | Constraints                                                                    |
|----------------------|-----------------|--------------------------------------------------------------------------------|
| `ID`                 | `string` (UUID) | Required, system-generated, unique primary key                                 |
| `Identifier`         | `string`        | Required, globally unique, DNS label (a-z0-9 + hyphens, 1–63 chars), lowercase |
| `DisplayName`        | `string`        | Optional, max 255 chars                                                        |
| `Tier`               | `NamespaceTier` | Required, one of: `user`, `organisation`, `enterprise`                         |
| `ParentEnterpriseID` | `*string`       | Optional UUID; set at creation time for `organisation` tier only               |
| `CreatedAt`          | `time.Time`     | Set on create, immutable                                                       |
| `CreatedBy`          | `string`        | Username of the caller who created the namespace; set on create, immutable     |
| `UpdatedAt`          | `time.Time`     | Updated on every write                                                         |
| `UpdatedBy`          | `string`        | Username of the last caller to modify the namespace                            |

**Validation rules** (applied in the service layer before persistence):
- `Identifier` MUST match `^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$` (DNS label, 1–63 chars, no leading/trailing hyphen).
- `Identifier` MUST NOT be in the reserved names list (see `research.md` Decision 6).
- `Identifier` MUST be globally unique across all tiers.
- `Tier` MUST be one of the three allowed values.
- `ParentEnterpriseID`, when set, MUST reference an existing namespace with `Tier == "enterprise"`.
- `ParentEnterpriseID` MUST only be set for `organisation` tier namespaces.

**State transitions**:
- A namespace is created and immediately active; there is no draft/pending state.
- A namespace can be deleted only when it contains no repositories (enforced by service layer).
- `ParentEnterpriseID` cannot be changed after creation (reassignment is out of scope per spec).

---

### NamespaceTier

Enumeration of allowed namespace tiers.

| Value          | Description                                                              |
|----------------|--------------------------------------------------------------------------|
| `user`         | Owns repositories directly; created by any authenticated user            |
| `organisation` | Owns repositories directly; may declare a parent enterprise at creation  |
| `enterprise`   | Organises organisation namespaces; does NOT own repositories directly    |

---

## Indices

### memdb

```go
"namespaces": {
    Name: "namespaces",
    Indexes: map[string]*memdb.IndexSchema{
        "id": {
            Name:    "id",
            Unique:  true,
            Indexer: &memdb.StringFieldIndex{Field: "ID"},
        },
        "identifier": {
            Name:    "identifier",
            Unique:  true,
            Indexer: &memdb.StringFieldIndex{Field: "Identifier"},
        },
        "tier": {
            Name:    "tier",
            Unique:  false,
            Indexer: &memdb.StringFieldIndex{Field: "Tier"},
        },
    },
}
```

- `id` (unique): Primary key lookup for `GetNamespace(id)`.
- `identifier` (unique): Supports `GetNamespaceByIdentifier(identifier)` and enforces global uniqueness invariant.
- `tier` (non-unique): Supports future `ListNamespacesByTier(tier)` without full scan.

### ScyllaDB

```sql
CREATE TABLE IF NOT EXISTS namespaces (
    id                   text       PRIMARY KEY,
    identifier           text,
    display_name         text,
    tier                 text,
    parent_enterprise_id text,
    created_at           bigint,
    created_by           text,
    updated_at           bigint,
    updated_by           text
);

CREATE INDEX IF NOT EXISTS namespaces_by_identifier ON namespaces (identifier);
```

- `id` as `text` PRIMARY KEY (consistent with all other tables in the schema).
- `identifier` secondary index with application-enforced uniqueness (read-before-write guard + unique index).
- Timestamps stored as `bigint` (milliseconds via `UnixMilli()`), consistent with `productRow`, `categoryRow`, `collectionRow`.
- `tier` and `parent_enterprise_id` stored as `text`; the Go layer enforces allowed values.

---

## Go Struct

Located in `gitstore-api/internal/datastore/entities.go` (new addition alongside `Product`, `Category`, `Collection`):

```go
// NamespaceTier is the enumeration of allowed namespace tiers.
type NamespaceTier string

const (
    NamespaceTierUser         NamespaceTier = "user"
    NamespaceTierOrganisation NamespaceTier = "organisation"
    NamespaceTierEnterprise   NamespaceTier = "enterprise"
)

// Namespace is the primary isolation boundary for repositories.
type Namespace struct {
    ID                   string
    Identifier           string
    DisplayName          string
    Tier                 NamespaceTier
    ParentEnterpriseID   *string
    CreatedAt            time.Time
    CreatedBy            string
    UpdatedAt            time.Time
    UpdatedBy            string
}
```

---

## Datastore Interface Extension

New methods added to `gitstore-api/internal/datastore/datastore.go`:

```go
// Namespace operations
CreateNamespace(ctx context.Context, ns *Namespace) error
GetNamespace(ctx context.Context, id string) (*Namespace, error)
GetNamespaceByIdentifier(ctx context.Context, identifier string) (*Namespace, error)
ListNamespaces(ctx context.Context) ([]*Namespace, error)
DeleteNamespace(ctx context.Context, id string) error
```

**Error contract** (consistent with existing methods):
- `ErrNotFound`: namespace with the given key does not exist.
- `ErrAlreadyExists`: `identifier` conflict on `CreateNamespace`.
- `ErrInvalidArgument`: empty `id` or nil namespace passed.

Note: There is no `UpdateNamespace` operation in this spec. The spec supports create, list, get, and delete. `UpdatedAt`/`UpdatedBy` are populated on create (set equal to `CreatedAt`/`CreatedBy`) and will be used when an update operation is added in a future spec.

---

## Query Patterns

| Operation                         | Lookup path             | Backend implementation                                                          |
|-----------------------------------|-------------------------|---------------------------------------------------------------------------------|
| `GetNamespace(id)`                | Primary key             | memdb: `txn.First("namespaces","id",id)` / ScyllaDB: `SELECT … WHERE id = ?`    |
| `GetNamespaceByIdentifier(ident)` | Identifier unique index | memdb: `txn.First("namespaces","identifier",ident)` / ScyllaDB: secondary index |
| `ListNamespaces()`                | Full scan               | memdb: `txn.Get("namespaces","id")` / ScyllaDB: `SELECT … FROM namespaces`      |
| `DeleteNamespace(id)`             | Primary key (write)     | Existence check first; then delete by `id`                                      |
