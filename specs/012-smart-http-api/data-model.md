# Data Model: Move Git Smart HTTP Server into gitstore-api

**Branch**: `012-smart-http-api` | **Date**: 2026-05-30

## Entities

### ReceivePackCommand

Represents a single ref-update instruction from a `git push` request body.

| Field      | Type                          | Constraints                                                          |
|------------|-------------------------------|----------------------------------------------------------------------|
| `old_oid`  | 40-char hex SHA-1 or zero OID | Required; zero OID (`0000...`) means ref does not exist yet (create) |
| `new_oid`  | 40-char hex SHA-1 or zero OID | Required; zero OID means delete the ref                              |
| `ref_name` | string                        | Required; must be a valid Git ref name (e.g. `refs/heads/main`)      |

**State transitions**:
- `create`: `old_oid` is zero OID, `new_oid` is non-zero → ref does not exist, will be created
- `update`: both non-zero → ref exists, will be updated; `old_oid` must match current tip (non-fast-forward rejection if it does not)
- `delete`: `old_oid` is non-zero, `new_oid` is zero → ref will be removed

---

### PackfileStream

Represents the in-flight transfer of raw PACK bytes from `gitstore-api` to `gitstore-git-service` during a push.

| Field           | Type                   | Constraints                                                                                                 |
|-----------------|------------------------|-------------------------------------------------------------------------------------------------------------|
| `repository_id` | UUIDv7 string          | Present only on the first chunk; identifies the target repository                                           |
| `ref_commands`  | `[]ReceivePackCommand` | Present only on the first chunk; the full list of ref-update commands parsed from pkt-lines before the PACK |
| `pack_data`     | bytes                  | Present on all subsequent chunks; raw PACK bytes (64 KiB default chunk size)                                |
| `is_last`       | bool                   | `true` on the final chunk only                                                                              |

**Lifecycle**:
1. First chunk arrives: git service validates `repository_id`, initialises quarantine `TempDir`, opens a streaming writer.
2. Subsequent chunks arrive: bytes appended to the quarantine writer.
3. `is_last = true`: git service finalises pack indexing, runs pre-receive hook, validates ref commands, atomically updates refs, promotes quarantine to live ODB.
4. On any error before step 3 completes: quarantine `TempDir` is dropped (automatic clean-up via `Drop`); error returned to `gitstore-api`; client receives a Git push error.

---

### UploadPackRequest

Represents the `want`/`have` negotiation from a `git clone` or `git fetch`.

| Field           | Type          | Constraints                                                                     |
|-----------------|---------------|---------------------------------------------------------------------------------|
| `repository_id` | UUIDv7 string | Required; identifies the target repository                                      |
| `body`          | bytes         | Required; complete pkt-line payload (wants + haves + flush) from the git client |

**Note**: The request body is small in practice (< 1 MB for any realistic negotiation). It is safe to buffer in one message.

---

### UploadPackChunk

A single chunk of the PACK response sent by `gitstore-git-service` during clone/fetch.

| Field         | Type   | Constraints                                           |
|---------------|--------|-------------------------------------------------------|
| `chunk_index` | uint32 | Zero-based index                                      |
| `data`        | bytes  | Sideband-encoded pkt-line bytes; max 64 KiB per chunk |
| `is_last`     | bool   | `true` on the final chunk                             |

---

### InfoRefsRequest

Parameters for a `GET /info/refs?service=...` call.

| Field           | Type           | Constraints                                       |
|-----------------|----------------|---------------------------------------------------|
| `repository_id` | UUIDv7 string  | Required                                          |
| `service`       | enum `Service` | Required: `GIT_UPLOAD_PACK` or `GIT_RECEIVE_PACK` |

---

### InfoRefsResponse

The complete ref advertisement returned to the git client.

| Field           | Type           | Constraints                                                                               |
|-----------------|----------------|-------------------------------------------------------------------------------------------|
| `advertisement` | bytes          | Complete pkt-line advertisement bytes (service header + flush + ref lines + capabilities) |
| `service`       | enum `Service` | Echoes the request service for routing Content-Type header                                |

---

### GitConfig changes (gitstore-api)

The `GitConfig` struct loses the `Ws` and `Http` sub-fields and gains a new `GitPort` on `ApiConfig`:

**Before:**
```
GitConfig {
  Grpc: GitEndpointConfig { Uri: string }  // git.grpc.uri
  Ws:   GitEndpointConfig { Uri: string }  // git.ws.uri       ← REMOVED
  Http: GitEndpointConfig { Uri: string }  // git.http.uri     ← REMOVED
}
```

**After:**
```
GitConfig {
  Grpc: GitEndpointConfig { Uri: string }  // git.grpc.uri     (unchanged)
}

ApiConfig {
  Port:    int  // api.port (4000)          (unchanged)
  GitPort: int  // api.git_port (5000)      ← NEW
}
```

Env vars removed: `GITSTORE_GIT__WS__URI`, `GITSTORE_GIT__HTTP__URI`  
Env var added: `GITSTORE_API__GIT_PORT` (default `5000`)

---

### AppConfig changes (gitstore-git-service)

The `ws: PortConfig` field is removed. The `http: PortConfig` field is also removed (HTTP server gone). Only `grpc: PortConfig` remains in the port configuration.

**Before:**
```
AppConfig {
  http: PortConfig { port: 9418 }    ← REMOVED
  ws:   PortConfig { port: 8080 }    ← REMOVED
  grpc: PortConfig { port: 50051 }   (unchanged)
  ...
}
```

**After:**
```
AppConfig {
  grpc: PortConfig { port: 50051 }   (unchanged)
  ...
}
```

Env vars removed: `GITSTORE_HTTP__PORT`, `GITSTORE_WS__PORT`

---

## State Transitions: Push Flow

```
gitstore-api receives POST /{namespace}/{repo}/git-receive-pack
  │
  ├─► Resolve (namespace, repo) → repo_id via datastore
  │     └─ NOT FOUND → 404 with Git smart-HTTP error body
  │
  ├─► Open gRPC client-streaming call to git-service ReceivePack
  │
  ├─► Stream first chunk: { repository_id, ref_commands, pack_data[0:64KiB] }
  │
  ├─► Stream subsequent chunks: { pack_data[N*64KiB:(N+1)*64KiB] }
  │
  ├─► Send last chunk: { pack_data[...], is_last: true }
  │
  └─► Await ReceivePackResponse
        ├─ OK → write report-status body to HTTP response
        └─ Error → write Git push error body, HTTP 422 or 500

gitstore-git-service ReceivePack handler
  │
  ├─► Receive first chunk → validate repo_id, init quarantine TempDir
  │
  ├─► Receive body chunks → stream bytes to quarantine writer
  │
  ├─► Receive is_last=true → finalise pack index (gix_pack Bundle)
  │
  ├─► Fire pre-receive lifecycle event (in-process)
  │     └─ Err(_) → discard quarantine, return error to caller
  │
  ├─► Validate ref commands (old_oid must match current tip)
  │     └─ Mismatch → non-fast-forward error (ref-level, not full abort)
  │
  ├─► Fire per-ref update lifecycle event for each accepted ref (in-process)
  │     └─ Err(_) → exclude that ref from the commit set
  │
  ├─► Commit accepted ref edits atomically (repo.edit_references)
  │
  ├─► Promote quarantine → move .pack/.idx to objects/pack/
  │
  ├─► Fire post-receive lifecycle event (in-process, best-effort)
  │     └─ Err(_) logged but does not affect the push result
  │
  └─► Return ReceivePackResponse { report_status_bytes }

On any error after step 1: quarantine TempDir dropped automatically (no partial objects promoted)
```
