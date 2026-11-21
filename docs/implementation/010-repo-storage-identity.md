# Repository Storage Identity and Path Strategy
**Status**: ✅ Shipped

## Overview

Repositories have two distinct identities:

- **External path**: `namespace/repo-name` — human-readable; names and namespace
  ownership are immutable after admission in the current contract.
- **Internal identity**: Repository UID — an immutable UUIDv7 assigned at
  creation, passed to GitService as `repository_id`.

Storage is keyed solely on that UID. Repository rename and transfer are not
implemented; stable UID-derived placement must not be mistaken for supported
rename/transfer APIs.

## Lookup Flow

```
Client: GET /alice/configs.git/info/refs
          │
          ▼
  gitstore-api (GraphQL / HTTP)
  ┌──────────────────────────────────────┐
  │ 1. GetNamespaceByName("alice")       │
  │    → canonical namespace            │
  │ 2. Resolve path to Repository UID  │
  │    → repository_id (UUIDv7)         │
  └────────────────┬─────────────────────┘
                   │ gRPC (repo_id only)
                   ▼
  gitstore-git-service (Rust)
  ┌─────────────────────────────────────┐
  │ 3. fanout_path(data_root, UID)     │
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

`repositories_by_uid` stores the authoritative Repository envelope.
Namespace/path and ordering projections resolve resource UIDs and hydrate that
record; they are not alternate sources of desired state. See
[datastore resource storage](../architecture/datastore-resource-storage.md).

`StoragePath` is **never stored** — it is derived on the fly from `repo_id` in both Go and Rust.

## Mutations and their Effects

| Mutation             | DB change                                                             | Storage change                        | gRPC call          |
|----------------------|-----------------------------------------------------------------------|---------------------------------------|--------------------|
| `createRepository`   | Commit/admit the manifest, create authoritative state and projections | Controller provisions the UID-derived bare repository natively | `CreateRepository` |
| `updateRepository`   | Commit/admit supported desired-state changes                         | UID-derived path unchanged            | Per lifecycle operation |
| `deleteRepository`   | Begin guarded foreground termination; keep identity until completion | UID-bound cleanup before final removal | `DeleteRepository` |

## Operator Runbook

### Find the storage path for a repository

Via GraphQL:
```graphql
query {
  repository(by: { namespacePath: { namespace: "alice", name: "configs" } }) {
    id
    metadata { name }
    status { resolved { storagePath } }
  }
}
```

Or compute it manually:
1. Obtain the authoritative Repository UID, not an encoded Relay node ID.
2. Strip hyphens from the UUID; take `hex[0..2]` as `l1` and `hex[2..4]` as `l2`.
3. Path: `{DATA_ROOT}/{l1}/{l2}/{repo_id}.git`

### Orphaned storage (mapping deleted, files remain)

A missing path projection can make retained repository storage unreachable.
Do not insert guessed mapping rows or delete repository directories. First
identify the authoritative resource and use the existing
[projection audit/repair procedure](../runbooks/scylla-projection-repair.md).
Preserve storage throughout recovery.

### Missing mapping (repo row exists, no mapping)

Audit authoritative state and derived projections with quiescent writers.
Repair rolls forward from authoritative state; it must not recreate ownership
from an arbitrary filesystem directory.

### Malformed UUID in storage path

`fanout_path` rejects any `repo_id` that:
- is not exactly 36 characters
- contains `/`, `\`, or `..`
- has non-hex characters after stripping hyphens

An unexpected directory name is a diagnostic, not permission to delete it.
Preserve retained assets and investigate resource ownership and backups before
any explicitly scoped maintenance.
