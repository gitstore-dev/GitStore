# Repository Storage Identity and Path Strategy

## Overview

Repositories have two distinct identities:

- **External identity**: `namespace/repo-name` — human-readable, mutable (rename, transfer)
- **Internal identity**: `repo_id` — a UUIDv7, immutable, assigned at creation

Storage is keyed solely on `repo_id`. Renaming or transferring a repository updates only
database mappings; no files move on disk.

## Lookup Flow

```
Client: GET /alice/configs/info/refs
          │
          ▼
  gitstore-api (GraphQL / HTTP)
  ┌──────────────────────────────────────┐
  │ 1. GetNamespaceByIdentifier("alice") │
  │    → namespace_id (UUID)             │
  │ 2. LookupRepository(ns_id, "configs")│
  │    → repo_id (UUIDv7)                │
  └────────────────┬─────────────────────┘
                   │ gRPC (repo_id only)
                   ▼
  gitstore-git-service (Rust)
  ┌─────────────────────────────────────┐
  │ 3. fanout_path(data_root, repo_id)  │
  │    → /data/xx/yy/{repo_id}.git      │
  │ 4. Open / serve bare repository     │
  └─────────────────────────────────────┘
```

## Fanout Path Formula

Given `repo_id = "01960abc-def0-7000-8000-000000000001"`:

```
hex  = "01960abcdef07000800000000000001"  (hyphens stripped)
l1   = hex[0..2]  = "01"
l2   = hex[2..4]  = "96"
path = {data_root}/01/96/01960abc-def0-7000-8000-000000000001.git
```

The full UUID (with hyphens) is used as the directory name. Only the first four hex digits
(two bytes) are used for the two-level fan-out prefix.

## Data Model

| Table                | Primary Key            | Purpose                                              |
|----------------------|------------------------|------------------------------------------------------|
| `repositories`       | `id` (UUIDv7)          | Repository metadata (name, namespace, storage class) |
| `namespace_mappings` | `(namespace_id, name)` | Maps `namespace/name` → `repo_id`                    |

`StoragePath` is **never stored** — it is derived on the fly from `repo_id` in both Go and Rust.

## Mutations and their Effects

| Mutation             | DB change                                                             | Storage change                        | gRPC call          |
|----------------------|-----------------------------------------------------------------------|---------------------------------------|--------------------|
| `createRepository`   | Insert `repository` + `namespace_mapping`                             | Create fanout dir + `git init --bare` | `CreateRepository` |
| `renameRepository`   | Delete old `namespace_mapping`, insert new                            | None                                  | None               |
| `transferRepository` | Delete old mapping, insert new mapping, update `namespace_id` on repo | None                                  | None               |
| `deleteRepository`   | Delete `namespace_mapping` + `repository`                             | Remove bare repo dir                  | `DeleteRepository` |

## Operator Runbook

### Find the storage path for a repository

Via GraphQL:
```graphql
query {
  repository(by: { namespacePath: { namespace: "alice", name: "configs" } }) {
    id
    storagePath
  }
}
```

Or compute it manually:
1. Look up `repo_id` from the `namespace_mappings` table.
2. Strip hyphens from the UUID; take `hex[0..2]` as `l1` and `hex[2..4]` as `l2`.
3. Path: `{DATA_ROOT}/{l1}/{l2}/{repo_id}.git`

### Orphaned storage (mapping deleted, files remain)

If a `namespace_mapping` row is missing but the `.git` directory exists, the repository is
unreachable via any API. To recover, either re-insert the mapping row directly or delete the
directory after confirming no other mapping points to the same `repo_id`.

### Missing mapping (repo row exists, no mapping)

Run `LookupNamespaceByRepoID(repo_id)` to check if any mapping exists. If none, insert a
`namespace_mapping` row for the correct `(namespace_id, name)` pair.

### Malformed UUID in storage path

`fanout_path` rejects any `repo_id` that:
- is not exactly 36 characters
- contains `/`, `\`, or `..`
- has non-hex characters after stripping hyphens

If a directory exists under `data_root` with a non-UUID name it was created outside the normal
API flow and can be safely removed after confirming it holds no live data.
