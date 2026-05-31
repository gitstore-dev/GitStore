# Implementation Plan: Move Git Smart HTTP Server into gitstore-api

**Branch**: `012-smart-http-api` | **Date**: 2026-05-30 | **Spec**: [spec.md](spec.md)  
**Input**: Feature specification from `/specs/012-smart-http-api/spec.md`

## Summary

Move Git smart HTTP transport (`info/refs`, `git-upload-pack`, `git-receive-pack`) from
`gitstore-git-service` into `gitstore-api` on port 5000. All pack operations are delegated to
`gitstore-git-service` via three new gRPC RPCs: `InfoRefs` (unary), `ReceivePack`
(client-streaming — eliminates full-packfile memory buffering), and `UploadPack`
(server-streaming). The WebSocket server in `gitstore-git-service` and the WebSocket client
in `gitstore-api` are both removed entirely in preparation for GH#139.

## Technical Context

**Language/Version**: Go 1.25 (`gitstore-api`) · Rust edition 2021, MSRV 1.82 (`gitstore-git-service`)  
**Primary Dependencies**:
- Go: `tonic`-generated gRPC stubs (already present), `net/http` (stdlib — no new framework dependency), `go.uber.org/zap`
- Rust: `tonic 0.14`, `prost 0.14`, `gix 0.83`, `gix-pack`, `gix-packetline`, `tokio 1.35`; **removing** `axum 0.8`, `tokio_tungstenite`, `tungstenite`  
**Storage**: Bare Git repositories on local filesystem (unchanged) · quarantine via `tempfile::TempDir`  
**Testing**: `go test` (unit + integration) · `cargo test` · integration tests via `git` CLI against running stack  
**Target Platform**: Linux server (Docker Compose) · local macOS for development  
**Project Type**: Multi-service web backend (polyglot Go + Rust)  
**Performance Goals**: Clone/fetch of ≤100 MB pack within 30 s · push of ≥1 GB pack without OOM  
**Constraints**: Packfile MUST NOT be fully buffered in memory in either service · fail-fast on gRPC unavailability (no internal retry)  
**Scale/Scope**: Same as existing stack (up to 10,000 products, ≤500 MB repositories)

## Constitution Check

*GATE: Must pass before Phase 0 research. Re-checked after Phase 1 design.*

| Principle                         | Status | Notes                                                                                                                                          |
|-----------------------------------|--------|------------------------------------------------------------------------------------------------------------------------------------------------|
| I. Test-First                     | ✅ PASS | Contract tests for all three new RPCs written before implementation; integration tests for clone/fetch/push written before HTTP handler code   |
| II. API-First                     | ✅ PASS | Proto contract defined in `contracts/grpc.git_service.proto` before any Rust/Go handler code                                                   |
| III. Clear Contracts & Versioning | ✅ PASS | Proto is additive-only (no field removals); new RPCs added with new message types                                                              |
| IV. Observability                 | ✅ PASS | FR-012 requires structured logs at all lifecycle points from both services                                                                     |
| V. User Story Driven              | ✅ PASS | All tasks map to US1, US2, or US3                                                                                                              |
| VI. Incremental Delivery          | ✅ PASS | US2 (smart HTTP migration) is P1 MVP; US3 (WebSocket removal) is P2 and independently deployable                                               |
| VII. Simplicity/YAGNI             | ✅ PASS | No new dependencies in `gitstore-api` (uses stdlib `net/http`); net dependency reduction in `gitstore-git-service` (removes axum, tungstenite) |

**Constitution note on architecture**: The `constitution.md` still references "websocket notifications" in the Architecture Constraints section. This is stale after this feature; that section should be updated as part of the documentation task.

## Project Structure

### Documentation (this feature)

```text
specs/012-smart-http-api/
├── plan.md              ← this file
├── research.md          ← Phase 0 complete
├── data-model.md        ← Phase 1 complete
├── quickstart.md        ← Phase 1 complete
├── contracts/
│   ├── grpc.git_service.proto    ← updated proto contract
│   └── http.git_endpoints.md    ← HTTP contract for port 5000
└── tasks.md             ← Phase 2 output (/speckit.tasks — not yet created)
```

### Source Code (repository root)

```text
shared/proto/gitstore/git/v1/
└── git_service.proto              ← add InfoRefs, ReceivePack, UploadPack RPCs

gitstore-git-service/
├── src/
│   ├── main.rs                    ← remove WS + HTTP server startup; keep gRPC only
│   ├── config.rs                  ← remove [http] and [ws] port config sections
│   ├── git/
│   │   ├── pack_server.rs         ← refactor: extract streaming-capable pack helpers
│   │   └── events.rs              ← DELETE (GitEvent moved to GH#139)
│   ├── grpc/
│   │   └── git_service.rs         ← ADD InfoRefs, ReceivePack, UploadPack handlers
│   ├── websocket/                 ← DELETE entire module
│   └── http_git_server.rs         ← DELETE entire file
└── Cargo.toml                     ← remove axum, tokio_tungstenite, tungstenite

gitstore-api/
├── cmd/server/main.go             ← add second http.Server on cfg.Api.GitPort (5000)
├── internal/
│   ├── config/
│   │   └── config.go             ← remove Ws + Http from GitConfig; add GitPort to ApiConfig
│   ├── githttp/                  ← NEW package: smart HTTP handlers for port 5000
│   │   ├── handler.go            ← info_refs, upload_pack, receive_pack handlers
│   │   └── handler_test.go       ← contract tests (written first)
│   ├── gitclient/
│   │   ├── grpc_client.go        ← add InfoRefs, ReceivePack, UploadPack methods
│   │   └── stream.go             ← NEW: streaming helpers for receive/upload pack
│   └── websocket/                ← DELETE entire package
├── go.mod                        ← remove github.com/gorilla/websocket
├── go.sum                        ← remove gorilla/websocket entries
├── .env                          ← remove GITSTORE_GIT__WS__URI, GITSTORE_GIT__HTTP__URI
└── .env.example                  ← same removals + add GITSTORE_API__GIT_PORT

tests/integration/                ← ADD git clone/fetch/push integration tests
compose.yml                       ← add port 5000 healthcheck for gitstore-api
docs/                             ← update architecture diagrams and port table
```

## Complexity Tracking

No constitution violations. Net complexity is negative (removes more than it adds):
- Removes: `axum`, `tokio_tungstenite`, `tungstenite`, Rust HTTP server, WS server (6 modules), `gorilla/websocket`, `internal/websocket` Go package
- Adds: `internal/githttp` Go package (new HTTP handlers), 3 gRPC RPC implementations in Rust, streaming helpers in Go

## Implementation Phases

### Phase A — Contract & Test Infrastructure (US2, US1)

**Goal**: Proto updated, generated code refreshed, contract tests written and failing.

1. Update `shared/proto/gitstore/git/v1/git_service.proto` with `InfoRefs`, `ReceivePack`, `UploadPack` RPCs and all new message types (see `contracts/grpc.git_service.proto`).
2. Regenerate gRPC stubs for Go (`gitstore-api`) and Rust (`gitstore-git-service`).
3. Write contract tests in `gitstore-api/internal/githttp/handler_test.go`:
   - `GET /info/refs?service=git-upload-pack` returns correct Content-Type and pkt-line header
   - `GET /info/refs?service=git-receive-pack` returns correct Content-Type and pkt-line header
   - `POST /git-upload-pack` streams response chunks with correct Content-Type
   - `POST /git-receive-pack` does not buffer request body; streams chunks to gRPC stub
   - Unknown repo returns 404 with Git pkt-line error body
   - git-service unavailable returns 503 with Git pkt-line error body (fast-fail, no retry)
4. Write integration test stubs (skeleton `TestGitClone`, `TestGitFetch`, `TestGitPush`) — these will fail until Phase C.

### Phase B — gRPC Handlers in gitstore-git-service (US1, US2)

**Goal**: Three new gRPC handlers implemented; pack logic refactored for streaming.

1. Refactor `git/pack_server.rs`:
   - Extract `parse_ref_commands(reader: impl Read)` — reads pkt-lines without holding the full body.
   - Extract `stage_pack_from_reader(repo, reader: impl Read) -> Result<TempDir>` — writes PACK bytes progressively from any `Read` source.
   - Adapt `handle_upload_pack` to return an iterator/stream of sideband chunks rather than a `Vec<u8>`.
2. Implement `grpc/git_service.rs` handlers:
   - `InfoRefs`: calls `advertise_upload_pack_refs()` or `advertise_receive_pack_refs()`, returns advertisement bytes.
   - `ReceivePack`: client-streaming handler — first chunk provides `repository_id` + `ref_commands`; subsequent chunks piped to `stage_pack_from_reader`; on `is_last`, finalise quarantine, fire pre-receive in-process lifecycle event (`Err` → discard quarantine), validate ref old-OID matches, fire per-ref update lifecycle event (filters accepted set), commit atomically, promote quarantine, fire post-receive lifecycle event (best-effort, error logged not propagated).
   - `UploadPack`: server-streaming handler — parses request body, builds pack using `gix_pack` iterator, streams chunks back.
3. Remove WebSocket server:
   - Delete `src/websocket/` module.
   - Delete `src/git/events.rs`.
   - Remove WS startup block from `src/main.rs`.
   - Remove `broadcaster` from `GitServerState`.
   - Remove `[ws]` config section.
   - Remove `tokio_tungstenite`, `tungstenite` from `Cargo.toml`.
4. Remove HTTP server:
   - Delete `src/http_git_server.rs`.
   - Remove HTTP startup block from `src/main.rs`.
   - Remove `[http]` config section.
   - Remove `axum` from `Cargo.toml`.
5. Add structured tracing calls (`tracing::info!`/`tracing::error!`) at all lifecycle points per FR-012.

### Phase C — Smart HTTP Handlers in gitstore-api (US2, US1)

**Goal**: `gitstore-api` serves Git smart HTTP on port 5000; integration tests pass.

1. Add `internal/githttp/handler.go`:
   - `infoRefsHandler`: resolves `(namespace, repo)` → `repo_id` via datastore; calls `gitclient.InfoRefs`; writes pkt-line header + advertisement to response.
   - `uploadPackHandler`: resolves repo_id; buffers small request body; calls `gitclient.UploadPack`; streams response chunks to HTTP response writer with `Transfer-Encoding: chunked`.
   - `receivePackHandler`: resolves repo_id; opens gRPC client stream; pipes HTTP request body in 64 KiB chunks; closes stream; writes `ReceivePackResponse.report_status` to HTTP response.
   - All handlers: fast-fail with Git pkt-line error on gRPC unavailability (no retry).
   - All handlers: structured `zap` log entries at lifecycle points per FR-012.
2. Register routes in `cmd/server/main.go` on a new `gitMux`:
   - `GET /{namespace}/{repo}/info/refs` and `GET /{namespace}/{repo}.git/info/refs`
   - `POST /{namespace}/{repo}/git-upload-pack` and `POST /{namespace}/{repo}.git/git-upload-pack`
   - `POST /{namespace}/{repo}/git-receive-pack` and `POST /{namespace}/{repo}.git/git-receive-pack`
   - `GET /health` and `GET /ready` (reuse existing health handlers)
3. Start second `http.Server{Addr: fmt.Sprintf(":%d", cfg.Api.GitPort)}` in its own goroutine; include in graceful shutdown block.
4. Remove WebSocket client from `gitstore-api`:
   - Delete `internal/websocket/` package.
   - Remove `gorilla/websocket` from `go.mod` / `go.sum`.
   - Remove `Ws GitEndpointConfig` from `GitConfig`.
   - Remove `Http GitEndpointConfig` from `GitConfig`.
   - Add `GitPort int` to `ApiConfig` with default `5000`.
   - Remove `git.ws.uri`, `git.http.uri` defaults and log lines from `config.go`.
   - Remove `GITSTORE_GIT__WS__URI` and `GITSTORE_GIT__HTTP__URI` from `.env`, `.env.example`.
5. Run integration tests (`TestGitClone`, `TestGitFetch`, `TestGitPush`) — should now pass.

### Phase D — Infrastructure & Documentation (US2, US3)

**Goal**: Compose, health checks, and docs updated.

1. Update `compose.yml`:
   - Expose port 5000 for `gitstore-api`.
   - Add health check probe for port 5000 (`curl http://localhost:5000/health`).
   - Remove any `ws.port` or `http.port` env vars from `gitstore-git-service` service definition.
2. Update `docs/` architecture diagrams and port table to reflect:
   - `gitstore-api` serves port 4000 (GraphQL) + port 5000 (Git smart HTTP).
   - `gitstore-git-service` serves only port 50051 (gRPC). No HTTP. No WebSocket.
3. Update `constitution.md` Architecture Constraints section: replace WebSocket reference with "gRPC notification stream (GH#139, pending)".
4. Verify `make pr-ready` passes (build + test + lint + licence-check).
