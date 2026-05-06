# Data Model: Decouple API from Git Storage via gRPC Git Service

**Feature**: 004-grpc-git-service
**Date**: 2026-05-06

## Overview

This feature introduces a gRPC contract layer between API and git-service. It does not add new domain entities to the catalogue (products, categories, collections remain unchanged). Instead, it defines the transport-level data model: the request/response message shapes that cross the service boundary.

All messages operate on **raw git primitives** — file bytes, paths, ref names, commit SHAs. Catalogue entity parsing (frontmatter, Markdown structure) continues to happen in the API after receiving raw bytes from git-service.

---

## Transport Entities (gRPC Messages)

### FileEntry

Represents a single file as enumerated by `ListFiles`.

| Field        | Type   | Constraints                      | Notes                                |
|--------------|--------|----------------------------------|--------------------------------------|
| `path`       | string | non-empty, relative to repo root | e.g. `products/apparel/shirt-001.md` |
| `size_bytes` | uint64 | ≥ 0                              | File size at the requested ref       |
| `blob_sha`   | string | 40-char hex SHA-1                | Git object identifier                |

### GetFileRequest

| Field  | Type   | Constraints | Notes                                |
|--------|--------|-------------|--------------------------------------|
| `path` | string | non-empty   | File path relative to repo root      |
| `ref`  | string | non-empty   | Tag name, branch name, or commit SHA |

### GetFileResponse

| Field        | Type   | Constraints     | Notes                                           |
|--------------|--------|-----------------|-------------------------------------------------|
| `path`       | string | mirrors request |                                                 |
| `content`    | bytes  | raw file bytes  | UTF-8 for markdown, but treated as opaque bytes |
| `blob_sha`   | string | 40-char hex     |                                                 |
| `size_bytes` | uint64 |                 |                                                 |

### FileChunk (streaming variant)

Used by `GetFileStream` for large files.

| Field         | Type   | Constraints              | Notes               |
|---------------|--------|--------------------------|---------------------|
| `chunk_index` | uint32 | monotonically increasing | 0-based             |
| `data`        | bytes  | max 256 KiB per chunk    |                     |
| `is_last`     | bool   |                          | True on final chunk |

### ListFilesRequest

| Field         | Type   | Constraints  | Notes                                |
|---------------|--------|--------------|--------------------------------------|
| `ref`         | string | non-empty    | Tag, branch, or commit               |
| `path_prefix` | string | may be empty | Filter to subtree (e.g. `products/`) |
| `recursive`   | bool   | default true | Include files in subdirectories      |

### ListFilesResponse

| Field            | Type               | Constraints | Notes                          |
|------------------|--------------------|-------------|--------------------------------|
| `files`          | repeated FileEntry |             | All matching files             |
| `ref_commit_sha` | string             | 40-char hex | Commit SHA the ref resolved to |

### CommitFileRequest

| Field            | Type   | Constraints | Notes                           |
|------------------|--------|-------------|---------------------------------|
| `path`           | string | non-empty   | File path relative to repo root |
| `content`        | bytes  | raw bytes   |                                 |
| `commit_message` | string | non-empty   |                                 |
| `author_name`    | string | non-empty   |                                 |
| `author_email`   | string | valid email |                                 |

### CommitFileResponse

| Field        | Type   | Constraints | Notes                            |
|--------------|--------|-------------|----------------------------------|
| `commit_sha` | string | 40-char hex | The resulting commit SHA         |
| `branch`     | string |             | Branch the commit was applied to |

### DeleteFileRequest

| Field            | Type   | Constraints | Notes          |
|------------------|--------|-------------|----------------|
| `path`           | string | non-empty   | File to delete |
| `commit_message` | string | non-empty   |                |
| `author_name`    | string | non-empty   |                |
| `author_email`   | string | valid email |                |

### DeleteFileResponse

| Field        | Type   | Constraints | Notes |
|--------------|--------|-------------|-------|
| `commit_sha` | string | 40-char hex |       |

### CreateTagRequest

| Field               | Type   | Constraints             | Notes                 |
|---------------------|--------|-------------------------|-----------------------|
| `tag_name`          | string | semver `v\d+\.\d+\.\d+` |                       |
| `message`           | string | non-empty               | Annotated tag message |
| `target_commit_sha` | string | 40-char hex or empty    | Empty = HEAD          |

### CreateTagResponse

| Field      | Type   | Constraints | Notes              |
|------------|--------|-------------|--------------------|
| `tag_name` | string |             |                    |
| `tag_sha`  | string | 40-char hex | The tag object SHA |

### ListTagsRequest

| Field    | Type   | Constraints  | Notes                   |
|----------|--------|--------------|-------------------------|
| `prefix` | string | may be empty | Filter prefix, e.g. `v` |

### ListTagsResponse

| Field  | Type              | Constraints | Notes |
|--------|-------------------|-------------|-------|
| `tags` | repeated TagEntry |             |       |

### TagEntry

| Field        | Type   | Constraints  | Notes                            |
|--------------|--------|--------------|----------------------------------|
| `name`       | string |              | Short tag name, e.g. `v1.2.0`    |
| `commit_sha` | string | 40-char hex  | Peeled commit SHA                |
| `message`    | string | may be empty | Annotated tag message if present |

### GetLatestTagRequest

| Field    | Type   | Constraints | Notes                     |
|----------|--------|-------------|---------------------------|
| `prefix` | string | default `v` | Semver release tag prefix |

### GetLatestTagResponse

| Field   | Type     | Constraints | Notes                           |
|---------|----------|-------------|---------------------------------|
| `tag`   | TagEntry |             | Latest tag by semver ordering   |
| `found` | bool     |             | False if no matching tags exist |

---

## State Transitions

### Catalogue Reload State (API-side)

```
Idle
  │
  ├─[WS notification / startup]──► Loading
  │                                    │
  │                          [gRPC reads complete]
  │                                    │
  │                               Serving (new catalog)
  │                                    │
  │                          [next notification]
  └─────────────────────────────────────►
```

**Concurrency rule**: If a `Loading` state is already active when a new notification arrives, the new notification is coalesced (queued once). The ongoing load completes first; the queued notification triggers a follow-up load. Multiple queued notifications are collapsed to one (the last one wins).

### Write Mutation State (git-service-side, opaque to API)

The API calls `CommitFile` / `DeleteFile` / `CreateTag` as atomic RPCs. git-service internally manages isolation (temporary clone or in-memory tree mutation). The API sees only: `(request) → (success with commit_sha) | (error with gRPC status code)`.

---

## Removed Entities

The following API-internal types are removed when `internal/gitclient/` is replaced:

| Type                    | File                           | Replaced by                                              |
|-------------------------|--------------------------------|----------------------------------------------------------|
| `CommitBuilder`         | `gitclient/commit.go`          | `CommitFile` / `DeleteFile` RPCs                         |
| `ProductFrontMatter`    | `gitclient/writer.go`          | Moved to `catalog/` package (pure serialisation, no I/O) |
| `CategoryFrontMatter`   | `gitclient/writer.go`          | Same as above                                            |
| `CollectionFrontMatter` | `gitclient/writer.go`          | Same as above                                            |
| `Writer` / `Push` types | `gitclient/push.go`, `pool.go` | gRPC client handles transport                            |
| `TagCreator`            | `gitclient/tag.go`             | `CreateTag` RPC                                          |
| `HTTPClient`            | `gitclient/http_client.go`     | Removed (git-over-HTTP replaced by gRPC)                 |
