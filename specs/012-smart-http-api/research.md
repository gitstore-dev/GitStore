# Research: Move Git Smart HTTP Server into gitstore-api

**Branch**: `012-smart-http-api` | **Date**: 2026-05-30

## Decision 1 — gRPC streaming pattern for `git-receive-pack` (push)

**Decision**: Client-streaming RPC — `rpc ReceivePack(stream ReceivePackChunk) returns (ReceivePackResponse)`

**Rationale**: The HTTP request body for a push contains ref-update pkt-lines (small, < 1 KB typical) followed by a raw PACK file (potentially gigabytes). `gitstore-api` reads the HTTP request body as it arrives from the git client and streams chunks over gRPC to `gitstore-git-service` without accumulating the full body in memory. The git service buffers only until the ref-update pkt-line section ends (flush packet `0000`), then writes PACK bytes progressively to quarantine storage via a streaming writer. The response (report-status pkt-lines) is a single small message.

**Alternatives considered**:
- *Unary with full body*: rejected — this is the current in-memory buffering problem the feature exists to fix.
- *Bidirectional streaming*: rejected — unnecessary complexity; the response is small enough for a single message.

**Key implementation note**: The first `ReceivePackChunk` carries `repository_id`; subsequent chunks carry raw body bytes. The git service maintains a small state machine: buffer incoming bytes until the pkt-line flush that terminates the ref-update section, then pipe remaining bytes directly to a quarantine `Write` sink.

---

## Decision 2 — gRPC streaming pattern for `git-upload-pack` (clone/fetch)

**Decision**: Server-streaming RPC — `rpc UploadPack(UploadPackRequest) returns (stream UploadPackChunk)`

**Rationale**: The HTTP request body for clone/fetch contains want/have pkt-lines from the git client. This is small in practice (SHA-1 OIDs + pkt-line overhead, < 1 MB even for repos with thousands of refs). `gitstore-api` can safely send it in a single message. The response, however, is a PACK file that can be the entire repository history — it must be streamed back in chunks. The git service uses `gix_pack::data::output::bytes::FromEntriesIter` (already present in `handle_upload_pack`) and streams its output as `UploadPackChunk` messages rather than accumulating into a `Vec<u8>`.

**Alternatives considered**:
- *Bidirectional streaming*: evaluated but rejected — the request body is small enough for one message; adding bidirectional streaming adds complexity with no benefit here.
- *Unary with full response*: rejected — defeats the purpose for large repositories.

---

## Decision 3 — gRPC pattern for `info/refs`

**Decision**: Unary RPC — `rpc InfoRefs(InfoRefsRequest) returns (InfoRefsResponse)`

**Rationale**: The ref advertisement is metadata (SHA-1 OIDs + capability strings). Even for a repository with 50,000 refs, the advertisement is a few megabytes at most — well within a single gRPC message (default max: 4 MB; can be raised). The spec's memory concern is specifically the PACK file. Buffering the ref advertisement is acceptable and avoids the complexity of streaming for an inherently bounded payload.

**Alternatives considered**:
- *Server-streaming*: technically possible but unnecessary; adds complexity to the gitstore-api handler for no measurable benefit given the bounded advertisement size.

---

## Decision 4 — Second HTTP server in gitstore-api on port 5000

**Decision**: Separate `net/http.Server` struct with its own `ServeMux`, started as a second goroutine in `main.go`, included in the existing graceful shutdown block.

**Rationale**: `gitstore-api` already uses `net/http` directly (no framework). Go 1.22+ `ServeMux` supports `{namespace}` and `{repo}` path variables natively. A second independent server is the standard Go pattern for serving two ports from one process. No new dependency is required.

**Implementation notes**:
- Git HTTP mux registers six route patterns (with and without `.git` suffix for namespace/repo paths).
- `.git` suffix is stripped from `{repo}` before the datastore lookup.
- The same `RequestIDMiddleware` is applied; CORS middleware is omitted (git clients do not require CORS headers).
- The `ApiConfig` struct gains a new `GitPort int` field (default `5000`); env override: `GITSTORE_API__GIT_PORT`.

---

## Decision 5 — Reuse existing pack logic in gitstore-git-service

**Decision**: Adapt `HttpPackServer` methods for use from gRPC handlers rather than rewriting them.

**Rationale**: `advertise_upload_pack_refs()`, `advertise_receive_pack_refs()`, `handle_upload_pack()`, and `parse_receive_pack_body()` are well-tested, protocol-correct implementations. The only structural change needed is:
- `handle_upload_pack`: pipe the output iterator into a gRPC server stream instead of collecting into `Vec<u8>`.
- `handle_receive_pack`: split into (a) parse ref-update pkt-lines, (b) write PACK bytes from a `Read` stream into quarantine using `gix_pack::Bundle::write_to_directory` with a piped writer.

The module can be renamed from `http_git_server` to the more general `git_pack_handler` or kept as-is with the HTTP-specific parts removed.

**Key refactor**: `parse_receive_pack_body(body: &[u8])` returns a `&[u8]` slice into the original in-memory buffer. This must be refactored to parse pkt-lines from a `Read` impl, yielding `ReceivePackCommand` structs and then handing off to a `Write` sink for the PACK bytes — eliminating the need to hold the full body.

---

## Decision 6 — WebSocket removal scope

**Decision**: Remove both the WebSocket server (gitstore-git-service) and the WebSocket client (gitstore-api) completely. No stub or placeholder is left.

**Rationale**: The `internal/websocket/client.go` in gitstore-api is fully implemented but never wired into `main.go` — it has no active callers. The git-service WebSocket server is started in `main.rs` and the `Broadcaster` is threaded through `GitServerState` into `http_git_server.rs`. After the HTTP server is removed, the broadcaster has no use. GH#139 adds the replacement (gRPC event stream) as a separate feature; this feature purely removes the WebSocket machinery from both sides.

**What to delete in gitstore-git-service**:
- `src/websocket/` module directory (server.rs, connections.rs, broadcast.rs, mod.rs)
- `src/git/events.rs` (GitEvent — will be re-defined in GH#139 for gRPC)
- `[ws]` config section in `src/config.rs`
- WS server startup block in `src/main.rs` (lines 67–77)
- `broadcaster` field in `GitServerState`
- All broadcast calls in what remains of `http_git_server.rs` (the file is removed entirely anyway)
- `tokio_tungstenite` and `tungstenite` from `Cargo.toml`

**What to delete in gitstore-api**:
- `internal/websocket/` package (client.go, any test files)
- `gorilla/websocket` from `go.mod` and `go.sum`
- `Ws GitEndpointConfig` field from `GitConfig` struct in `config.go`
- `git.ws.uri` default in `SetDefaults()` and its log line in `MarshalLogObject()`
- `GITSTORE_GIT__WS__URI` from `.env`, `.env.example`, and any documentation
- The `"git.ws.uri": true` entry in the required-keys map in `config.go:151`

---

## Decision 7 — HTTP server removal from gitstore-git-service

**Decision**: Remove `src/http_git_server.rs` entirely and the HTTP server startup from `main.rs`. Also remove the `git.http.uri` config key from gitstore-api (it referenced the old git-service HTTP endpoint).

**Rationale**: With all smart HTTP logic moved to gitstore-api and delegated to git-service via gRPC, there is no longer any HTTP listener in git-service. The `git.http.uri` config key in gitstore-api (`http://localhost:9418`) becomes unused and should be removed along with the `Http GitEndpointConfig` field to keep the config clean.

**What to remove**:
- `src/http_git_server.rs` (the entire file)
- HTTP server startup in `src/main.rs`
- `[http]` port config (or keep if it is still needed for internal tooling — confirm during implementation)
- `Http GitEndpointConfig` from `GitConfig` in gitstore-api `config.go`
- `git.http.uri` default, log line, and required-keys entry in gitstore-api `config.go`

---

## Decision 8 — Quarantine clean-up on gRPC stream failure

**Decision**: Wrap quarantine storage in a RAII guard (Rust `Drop` impl). On any error, `drop` discards the `tempfile::TempDir` automatically. Only on a successful stream completion followed by successful ref validation is `promote_quarantine` called.

**Rationale**: `tempfile::TempDir` already implements `Drop` with automatic deletion. The current `stage_pack_to_quarantine` returns a value that must be explicitly promoted. By not calling `promote_quarantine` on error paths, the `Drop` of the `TempDir` handle automatically cleans up. This is already effectively the behaviour for errors in `handle_receive_pack` today, but it needs to be made explicit for the gRPC streaming path where the handler returns early on stream error.

---

## Decision 9 — gitstore-api port 5000 health check

**Decision**: Add a `/health` and `/ready` endpoint to the port 5000 mux in gitstore-api, mirroring the existing handlers already registered on port 4000.

**Rationale**: FR-010 and SC-004 require both ports to have passing health checks. The existing `healthHandler.Health` and `healthHandler.Ready` functions are stateless and can be registered on both muxes without modification. Docker Compose health check for `gitstore-api` gains a second probe targeting port 5000.

---

## Resolved Unknowns

| Item                                                   | Resolution                                                                                                    |
|--------------------------------------------------------|---------------------------------------------------------------------------------------------------------------|
| Proto field number for `repository_id` in new messages | Field 15, consistent with all existing request messages                                                       |
| Chunk size for streaming                               | 64 KiB default (matching existing `GetFileStream` 256 KiB cap, but smaller for receive-pack to bound latency) |
| `.git` suffix handling                                 | Stripped by the gitstore-api HTTP handler before datastore lookup                                             |
| gRPC interceptors on new methods                       | Existing `go-grpc-prometheus` interceptors apply automatically to all stubs                                   |
| `GitEvent` type after WS removal                       | Deleted in this feature; re-introduced as a proto message in GH#139                                           |
| `git.http.uri` key in gitstore-api config              | Removed (it referenced the old git-service HTTP port, which no longer exists)                                 |
