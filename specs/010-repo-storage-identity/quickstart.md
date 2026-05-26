# Developer Quickstart: Repository Storage Identity

**Branch**: `010-repo-storage-identity`

## What this feature adds

Two new first-class entities (`Repository` and `NamespaceMapping`) plus a fanout storage path resolver. After this feature:

- Repository storage is keyed on a stable `repo_id` (UUIDv7), not the namespace path.
- `gitstore-api` resolves `namespace/repo-name → repo_id` before every gRPC call.
- `gitstore-git-service` derives storage paths as `{data_dir}/{xx}/{yy}/{repo_id}.git`.
- Rename and transfer change only the database mapping; no files move on disk.

## Touch points by service

### `gitstore-api`

| Area                                                               | Change                                                                                                                   |
|--------------------------------------------------------------------|--------------------------------------------------------------------------------------------------------------------------|
| `internal/datastore/entities.go`                                   | Add `Repository` and `NamespaceMapping` structs                                                                          |
| `internal/datastore/datastore.go`                                  | Add repository and mapping methods to the interface                                                                      |
| `internal/datastore/memdb/schema.go`                               | Add `repository` and `namespace_mapping` tables                                                                          |
| `internal/datastore/memdb/memdb.go`                                | Implement new interface methods                                                                                          |
| `internal/datastore/scylla/models.go`                              | Add Scylla table models for both new tables                                                                              |
| `internal/datastore/scylla/*.go`                                   | Implement new interface methods                                                                                          |
| `internal/datastore/scylla/migrations/001_initial_schema.cql`      | Add `repositories` and `namespace_mappings` tables (in-place update)                                                     |
| `internal/datastore/scylla/migrations/002_add_initial_indices.cql` | Add indices for new tables (in-place update)                                                                             |
| `graph/schema.graphqls` (or `shared/schemas/`)                     | Merge `graphql.repository.graphqls` contract                                                                             |
| `graph/resolver*.go`                                               | Implement `createRepository`, `renameRepository`, `transferRepository`, `deleteRepository`, `repository`, `repositories` |

### `gitstore-git-service`

| Area                                             | Change                                                                                                |
|--------------------------------------------------|-------------------------------------------------------------------------------------------------------|
| `src/grpc/server.rs`                             | Replace `resolve_repo_path` with fanout formula; validate UUID format                                 |
| `shared/proto/gitstore/git/v1/git_service.proto` | Merge `grpc.git_service.proto` contract (additive: `storage_class`, `storage_path`, updated comments) |

## Storage path formula

```
repo_id  (with hyphens): 0196f3a2-4b1c-7e9d-a301-8b2c4d5e6f7a
hex_digits (no hyphens): 0196f3a24b1c7e9da3018b2c4d5e6f7a
l1 = hex_digits[0:2]  → "01"
l2 = hex_digits[2:4]  → "96"
path = {data_dir}/01/96/0196f3a2-4b1c-7e9d-a301-8b2c4d5e6f7a.git
```

The path is derived purely from `repo_id` — no database lookup required in git-service.

## Lookup flow (per git request)

```
Client: GET /acme/my-catalog/info/refs
    ↓
gitstore-api (HTTP handler, feature #103):
  1. Parse namespace slug "acme" + repo name "my-catalog"
  2. namespace = datastore.GetNamespaceByIdentifier("acme")  → namespace_id UUID
  3. mapping   = datastore.LookupRepository(namespace_id, "my-catalog") → repo_id UUID
  4. gRPC: GitService.GetFile(repository_id = repo_id, ...)
    ↓
gitstore-git-service:
  5. Validate repo_id is well-formed UUID
  6. path = fanout(data_dir, repo_id)
  7. Open path, serve response
```

## Running tests

```bash
# Go (gitstore-api)
cd gitstore-api
go test -count=1 -v -race ./internal/datastore/...
go test -count=1 -v -race ./graph/...

# Rust (gitstore-git-service)
cd gitstore-git-service
cargo test --verbose
```

## Key invariants to test

1. `fanout(data_dir, repo_id)` is stable — same input always yields same output.
2. Rename: `LookupRepository(ns_id, new_name)` returns same `repo_id`; `LookupRepository(ns_id, old_name)` returns `ErrNotFound`.
3. Transfer: `LookupRepository(new_ns_id, name)` succeeds; `LookupRepository(old_ns_id, name)` returns `ErrNotFound`.
4. `repo_id` UUID format validation in Rust rejects strings containing `/`, `..`, or non-hex characters.
5. gRPC `CreateRepository` response includes `storage_path`; the path matches the fanout formula.
