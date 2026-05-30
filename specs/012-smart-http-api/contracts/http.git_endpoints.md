# HTTP Contract: Git Smart HTTP Endpoints (gitstore-api, port 5000)

**Branch**: `012-smart-http-api` | **Date**: 2026-05-30

All Git smart HTTP endpoints are served by `gitstore-api` on a dedicated port (default: 5000),
separate from the GraphQL/REST API on port 4000.

Path parameters `{namespace}` and `{repo}` are resolved to a `repo_id` (UUIDv7) via the
datastore abstraction before any gRPC call is made. The `.git` suffix on `{repo}` is stripped
before the lookup.

---

## `GET /{namespace}/{repo}[.git]/info/refs?service=git-upload-pack`

**Purpose**: Ref advertisement for clone/fetch.

**Response**:
- Status: `200 OK`
- Content-Type: `application/x-git-upload-pack-advertisement`
- Body: pkt-line service header (`001e# service=git-upload-pack\n0000`) followed by ref advertisement from `InfoRefs` gRPC call.

**Error responses**:

| Condition                | Status | Body                                           |
|--------------------------|--------|------------------------------------------------|
| Namespace/repo not found | `404`  | Git pkt-line error: `ERR repository not found` |
| git-service unavailable  | `503`  | Git pkt-line error: `ERR service unavailable`  |

---

## `GET /{namespace}/{repo}[.git]/info/refs?service=git-receive-pack`

**Purpose**: Ref advertisement for push.

**Response**:
- Status: `200 OK`
- Content-Type: `application/x-git-receive-pack-advertisement`
- Body: pkt-line service header (`001f# service=git-receive-pack\n0000`) followed by ref advertisement.

**Error responses**: same as upload-pack above.

---

## `POST /{namespace}/{repo}[.git]/git-upload-pack`

**Purpose**: Pack negotiation for clone/fetch.

**Request**:
- Content-Type: `application/x-git-upload-pack-request`
- Body: pkt-line want/have negotiation payload (fully buffered on receipt — bounded in size).

**Response**:
- Status: `200 OK`
- Content-Type: `application/x-git-upload-pack-result`
- Body: sideband-encoded PACK bytes, streamed from `UploadPack` gRPC response chunks. Transfer-Encoding: `chunked`.

**Error responses**:

| Condition                | Status | Body               |
|--------------------------|--------|--------------------|
| Namespace/repo not found | `404`  | Git pkt-line error |
| git-service unavailable  | `503`  | Git pkt-line error |
| Pack generation error    | `500`  | Git pkt-line error |

---

## `POST /{namespace}/{repo}[.git]/git-receive-pack`

**Purpose**: Accept a push.

**Request**:
- Content-Type: `application/x-git-receive-pack-request`
- Body: pkt-line ref-update commands + raw PACK bytes. NOT buffered in full — streamed in 64 KiB chunks over gRPC.

**Response**:
- Status: `200 OK`
- Content-Type: `application/x-git-receive-pack-result`
- Body: report-status pkt-lines from `ReceivePack` gRPC response.

**Error responses**:

| Condition                              | Status | Body                                                          |
|----------------------------------------|--------|---------------------------------------------------------------|
| Namespace/repo not found               | `404`  | Git pkt-line error                                            |
| git-service unavailable                | `503`  | Git pkt-line error                                            |
| Ref update rejected (non-fast-forward) | `200`  | report-status with per-ref `ng` lines (standard Git protocol) |
| Pack validation failure                | `422`  | Git pkt-line error                                            |
| Stream interrupted                     | `500`  | Git pkt-line error                                            |

**Note**: Git protocol conventions require 200 for ref rejections (e.g. non-fast-forward); the rejection details are in the report-status body.

---

## Health Endpoints (port 5000)

These mirror the existing handlers on port 4000.

### `GET /health`

- Status: `200 OK`
- Body: `{"status":"ok"}`

### `GET /ready`

- Status: `200 OK` when ready, `503` when not ready.
- Body: `{"status":"ready"}` or `{"status":"not ready"}`

---

## Structured Logging

Both `gitstore-api` and `gitstore-git-service` emit structured log entries (zap / tracing respectively) at:

| Event                       | Fields                                                            |
|-----------------------------|-------------------------------------------------------------------|
| Stream start                | `repo_id`, `service` (`upload-pack`/`receive-pack`), `request_id` |
| Chunk sent/received         | `repo_id`, `chunk_index`, `bytes`, `request_id`                   |
| Stream complete             | `repo_id`, `total_chunks`, `total_bytes`, `request_id`            |
| Quarantine promotion result | `repo_id`, `result` (`ok`/`error`), `request_id`                  |
| Ref update result           | `repo_id`, `ref_name`, `result` (`ok`/`ng`), `request_id`         |
| gRPC stream error           | `repo_id`, `error`, `request_id`                                  |
