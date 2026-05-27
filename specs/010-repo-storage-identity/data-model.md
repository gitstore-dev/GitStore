# Data Model: Repository Storage Identity and Path Strategy

**Branch**: `010-repo-storage-identity` | **Date**: 2026-05-26

## Entities

### Repository

Represents a git repository. New entity — does not exist in the current datastore.

| Field           | Type              | Description                                                                       |
|-----------------|-------------------|-----------------------------------------------------------------------------------|
| `ID`            | `string` (UUIDv7) | Internal stable identifier — the `repo_id`. Assigned at creation, immutable.      |
| `NamespaceID`   | `string` (UUIDv7) | Stable UUID of the owning namespace.                                              |
| `Name`          | `string`          | Human-readable name within the namespace (mutable on rename).                     |
| `DefaultBranch` | `string`          | Default branch name (e.g., `"main"`).                                             |
| `StorageClass`  | `string`          | Storage tier tag. Default: `"default"`. Reserved for future multi-root expansion. |
| `CreatedAt`     | `time.Time`       | Creation timestamp.                                                               |
| `CreatedBy`     | `string`          | Actor identifier.                                                                 |
| `UpdatedAt`     | `time.Time`       | Last mutation timestamp.                                                          |
| `UpdatedBy`     | `string`          | Actor identifier.                                                                 |

**Derived (not stored)**:
- `StoragePath`: computed as `{data_dir}/{repo_id[0:2]}/{repo_id[2:4]}/{repo_id}.git` (hyphens stripped for the fanout prefix, full UUID with hyphens for the final segment). See research D-002.

**Invariants**:
- `ID` is assigned once; never updated.
- `Name` is unique within a namespace (enforced at the `NamespaceMapping` level).
- `StoragePath` is stable for the lifetime of the repository.

**State transitions**: `created → active → deleted`. No intermediate states in ALPHA.

---

### NamespaceMapping

The join record binding `(namespace_id, name)` → `repo_id`. Separate from `Repository` for O(1) lookup performance.

| Field         | Type              | Description                                            |
|---------------|-------------------|--------------------------------------------------------|
| `NamespaceID` | `string` (UUIDv7) | Owning namespace stable UUID (partition key).          |
| `Name`        | `string`          | Repository name within the namespace (clustering key). |
| `RepoID`      | `string` (UUIDv7) | Target `repo_id`.                                      |

**Lookup pattern**: primary lookup is `(NamespaceID, Name) → RepoID`. Secondary lookup `RepoID → (NamespaceID, Name)` for reverse resolution (admin tooling, US4).

**Operations**:
- **Create**: Insert new row. Fails with `ErrAlreadyExists` if `(NamespaceID, Name)` already exists.
- **Rename**: Delete `(NamespaceID, old_name)`, insert `(NamespaceID, new_name)` with same `RepoID`.
- **Transfer**: Delete `(old_namespace_id, Name)`, insert `(new_namespace_id, Name)` with same `RepoID`.
- **Delete**: Delete `(NamespaceID, Name)`.

**Invariants**:
- `(NamespaceID, Name)` is unique.
- `RepoID` references a valid `Repository.ID`.
- A namespace slug rename requires **no** changes to this table (because `NamespaceID` is the stable UUID, not the slug).

---

### Namespace (updated)

Existing entity. Gains explicit `ID` UUID semantics — `ID` is already present but memdb uses `StringFieldIndex` for it. This feature treats `ID` as a UUIDv7 and adds it to the datastore contract as the stable `namespace_id`.

No field additions required. The existing `Namespace.ID` field **is** the `namespace_id` referenced by `NamespaceMapping.NamespaceID`.

---

## ScyllaDB Schema Changes

### Update `001_initial_schema.cql`

Add the `repositories` table and update `namespaces` to use `timestamp` consistently.

```cql
-- repositories table (new)
CREATE TABLE IF NOT EXISTS repositories (
    id              uuid      PRIMARY KEY,
    namespace_id    uuid,
    name            text,
    default_branch  text,
    storage_class   text,
    created_at      timestamp,
    created_by      text,
    updated_at      timestamp,
    updated_by      text
);

-- namespace_mappings table (new)
CREATE TABLE IF NOT EXISTS namespace_mappings (
    namespace_id uuid,
    name         text,
    repo_id      uuid,
    PRIMARY KEY  (namespace_id, name)
);
```

### Update `002_add_initial_indices.cql`

Add indices for new tables.

```cql
-- repositories lookup by namespace (list repos in a namespace)
CREATE INDEX IF NOT EXISTS repositories_by_namespace ON repositories (namespace_id);

-- namespace_mappings reverse lookup (repo_id → namespace mapping)
CREATE INDEX IF NOT EXISTS mappings_by_repo_id ON namespace_mappings (repo_id);
```

---

## go-memdb Schema Changes

New tables to add to `schema.go`:

```go
"repository": {
    Name: "repository",
    Indexes: map[string]*memdb.IndexSchema{
        "id": {
            Name:    "id",
            Unique:  true,
            Indexer: &memdb.UUIDFieldIndex{Field: "ID"},
        },
        "namespace_id": {
            Name:         "namespace_id",
            Unique:       false,
            Indexer:      &memdb.StringFieldIndex{Field: "NamespaceID"},
        },
    },
},
"namespace_mapping": {
    Name: "namespace_mapping",
    Indexes: map[string]*memdb.IndexSchema{
        "id": {
            Name:    "id",
            Unique:  true,
            Indexer: &memdb.CompoundIndex{
                Indexes: []memdb.Indexer{
                    &memdb.UUIDFieldIndex{Field: "NamespaceID"},
                    &memdb.StringFieldIndex{Field: "Name"},
                },
            },
        },
        "repo_id": {
            Name:    "repo_id",
            Unique:  true,
            Indexer: &memdb.UUIDFieldIndex{Field: "RepoID"},
        },
    },
},
```

---

## Rust Storage Path Resolver

The `resolve_repo_path` function in `gitstore-git-service/src/grpc/server.rs` is replaced:

```rust
// Derives the fanout storage path from a repo_id UUID string.
// Input: "0196f3a2-4b1c-7e9d-a301-8b2c4d5e6f7a"
// Output: {data_dir}/01/96/0196f3a2-4b1c-7e9d-a301-8b2c4d5e6f7a.git
fn resolve_repo_path(data_root: &Path, repo_id: &str) -> Result<PathBuf, Status> {
    // Validate format: must be a 36-char hyphenated UUID
    validate_repo_id_format(repo_id)?;
    let hex: String = repo_id.replace('-', "");
    let l1 = &hex[0..2];
    let l2 = &hex[2..4];
    let path = data_root.join(l1).join(l2).join(format!("{}.git", repo_id));
    Ok(path)
}
```

The path traversal attack surface is eliminated: a UUID-format-validated string cannot contain `/` or `..`.

---

## Datastore Interface Additions

New methods on the `Datastore` interface:

```go
// Repository operations
CreateRepository(ctx context.Context, r *Repository) error
GetRepository(ctx context.Context, id string) (*Repository, error)
ListRepositoriesByNamespace(ctx context.Context, namespaceID string) ([]*Repository, error)
UpdateRepository(ctx context.Context, r *Repository) error
DeleteRepository(ctx context.Context, id string) error

// NamespaceMapping operations (lookup contract)
CreateNamespaceMapping(ctx context.Context, m *NamespaceMapping) error
LookupRepository(ctx context.Context, namespaceID, name string) (*NamespaceMapping, error)
LookupNamespaceByRepoID(ctx context.Context, repoID string) (*NamespaceMapping, error)
RenameRepository(ctx context.Context, namespaceID, oldName, newName string) error
TransferRepository(ctx context.Context, repoID, fromNamespaceID, toNamespaceID string) error
DeleteNamespaceMapping(ctx context.Context, namespaceID, name string) error
```
