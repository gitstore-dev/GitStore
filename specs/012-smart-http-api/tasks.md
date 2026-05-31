---
description: "Task list for 012-smart-http-api"
---

# Tasks: Move Git Smart HTTP Server into gitstore-api

**Input**: Design documents from `/specs/012-smart-http-api/`  
**Prerequisites**: plan.md ✅ · spec.md ✅ · research.md ✅ · data-model.md ✅ · contracts/ ✅

**Tests**: Test-First Development (Constitution Principle I — NON-NEGOTIABLE). Tests MUST be written before implementation and MUST fail before any implementation code is added.

**Organization**: Tasks are grouped by user story to enable independent implementation and testing. US1 and US2 are both P1; US3 is P2.

## Format: `[ID] [P?] [Story] Description`

- **[P]**: Can run in parallel (different files, no shared dependencies)
- **[Story]**: Which user story this task belongs to (US1, US2, US3)
- Exact file paths are included in every description

---

## Phase 1: Setup (Shared Infrastructure)

**Purpose**: Proto updated, generated stubs refreshed, new package skeletons created. No story work can begin until this phase is complete.

- [x] T001 Update `shared/proto/gitstore/git/v1/git_service.proto` — add `Service` enum, `InfoRefs`, `ReceivePack`, `UploadPack` RPCs and all new message types (`InfoRefsRequest`, `InfoRefsResponse`, `ReceivePackChunk`, `RefCommand`, `ReceivePackResponse`, `UploadPackRequest`, `UploadPackChunk`) per `specs/012-smart-http-api/contracts/grpc.git_service.proto`
- [x] T002 Regenerate Go gRPC stubs from updated proto in `gitstore-api/api/gen/gitstore/git/v1/`
- [x] T003 Regenerate Rust gRPC stubs from updated proto in `gitstore-git-service/src/gen/` (or equivalent `build.rs` output path)
- [x] T004 [P] Create package skeleton `gitstore-api/internal/githttp/` with empty `handler.go` (package declaration + imports only)
- [x] T005 [P] Create `gitstore-git-service/src/grpc/` module directory with empty `git_service.rs` stub (module declaration only)

---

## Phase 2: Foundational (Blocking Prerequisites)

**Purpose**: Contract tests written and failing; streaming helpers in place. These must exist before any user story implementation begins.

**⚠️ CRITICAL**: No US1/US2/US3 implementation can begin until this phase is complete.

- [x] T006 Write contract tests in `gitstore-api/internal/githttp/handler_test.go` for `infoRefsHandler` — assert `GET /{namespace}/{repo}/info/refs?service=git-upload-pack` returns Content-Type `application/x-git-upload-pack-advertisement` and pkt-line service header; assert same for `git-receive-pack` variant; use a mock gRPC client (`InfoRefs` stub returns canned advertisement bytes)
- [x] T007 Write contract tests in `gitstore-api/internal/githttp/handler_test.go` for `uploadPackHandler` — assert `POST /{namespace}/{repo}/git-upload-pack` returns Content-Type `application/x-git-upload-pack-result`; assert response is streamed (chunked) and not buffered; use mock `UploadPack` stub that emits two chunks then `is_last=true`
- [x] T008 Write contract tests in `gitstore-api/internal/githttp/handler_test.go` for `receivePackHandler` — assert `POST /{namespace}/{repo}/git-receive-pack` pipes request body in 64 KiB chunks to the gRPC stream without accumulating the full body; assert mock `ReceivePack` stub receives the chunks and returns a `report_status` payload that is written to the HTTP response
- [x] T009 [P] Write contract test in `gitstore-api/internal/githttp/handler_test.go` — assert unknown namespace/repo returns `404` with Git pkt-line error body `ERR repository not found`
- [x] T010 [P] Write contract test in `gitstore-api/internal/githttp/handler_test.go` — assert gRPC unavailability returns `503` with Git pkt-line error body `ERR service unavailable` and no internal retry is attempted
- [x] T011 Write integration test skeletons in `tests/integration/git_http_test.go` — `TestGitClone`, `TestGitFetch`, `TestGitPush` (skeleton only; `t.Skip("not yet implemented")` until Phase 4)
- [x] T012 Add `InfoRefs`, `ReceivePack`, `UploadPack` method signatures to `gitstore-api/internal/gitclient/grpc_client.go` (stubs that return `errors.New("not implemented")` — makes T006–T010 compile)
- [x] T013 Create `gitstore-api/internal/gitclient/stream.go` — streaming helper types: `ReceivePackSender` (wraps a client stream, exposes `SendFirst(repoID, cmds, data)` and `SendChunk(data)` and `CloseSend()`), `UploadPackReceiver` (wraps a server stream, exposes `NextChunk()`)

**Checkpoint**: All contract tests (T006–T010) compile and fail. Skeletons in place. Foundation ready for US1/US2 work.

---

## Phase 3: User Story 1 — Large Push Completes Without OOM (Priority: P1) 🎯 MVP

**Goal**: `git push` with a large packfile succeeds without in-memory buffering; peak memory in the git service does not scale linearly with pack size.

**Independent Test**: Run `git push` against a repository whose pack exceeds 512 MB. Verify the push succeeds and no OOM event is logged. Verify `gitstore-git-service` peak RSS does not grow proportionally to pack size.

### Implementation for User Story 1

- [x] T014 [US1] Refactor `gitstore-git-service/src/git/pack_server.rs` — extract `parse_ref_commands(reader: impl Read) -> Result<Vec<RefCommand>>` that reads pkt-line ref-update commands from any `Read` source without holding the full body in memory; replaces the `parse_receive_pack_body(body: &[u8])` slice-based function for the receive path
- [x] T015 [US1] Refactor `gitstore-git-service/src/git/pack_server.rs` — extract `stage_pack_from_reader(repo: &Repository, reader: impl Read, temp_dir: &TempDir) -> Result<()>` that writes PACK bytes progressively to quarantine using `gix_pack::Bundle::write_to_directory` piped from the reader; replaces the in-memory buffer approach in `stage_pack_to_quarantine`
- [x] T016 [US1] Implement `ReceivePack` gRPC handler in `gitstore-git-service/src/grpc/git_service.rs` — client-streaming handler: receive first chunk (extract `repository_id`, `ref_commands`, initial `pack_data`); pipe subsequent chunk `pack_data` bytes through `stage_pack_from_reader` via an async channel/pipe bridging the gRPC stream to the sync `Read` impl; on `is_last=true` finalise pack index
- [x] T017 [US1] Complete `ReceivePack` handler in `gitstore-git-service/src/grpc/git_service.rs` — after pack finalisation: fire pre-receive in-process lifecycle event (`Err` → drop quarantine `TempDir`, return gRPC error); validate each `RefCommand.old_oid` matches current ref tip (mismatch → non-fast-forward error for that ref, not full abort); fire per-ref update lifecycle event (filter accepted set); commit accepted ref edits atomically via `repo.edit_references`; promote quarantine (`fs::rename` pack+idx into `objects/pack/`); fire post-receive lifecycle event (best-effort, log error, do not propagate); return `ReceivePackResponse { report_status }`
- [x] T018 [US1] Implement `ReceivePack` method in `gitstore-api/internal/gitclient/grpc_client.go` — opens client-streaming call to git service; uses `ReceivePackSender` from `stream.go`; resolves `repository_id` from context; streams HTTP request body in 64 KiB chunks without buffering the full body; returns `ReceivePackResponse`
- [x] T019 [US1] Implement `receivePackHandler` in `gitstore-api/internal/githttp/handler.go` — resolves `(namespace, repo)` → `repo_id` via datastore abstraction; calls `gitclient.ReceivePack`; on gRPC unavailability fails fast with Git pkt-line error (no retry); on success writes `report_status` bytes to HTTP response with Content-Type `application/x-git-receive-pack-result`; emits structured `zap` log entries: stream start (repo_id, request_id), each chunk sent (chunk_index, bytes), stream complete (total_chunks, total_bytes), ref update result per ref, any gRPC stream error
- [x] T020 [US1] Add `tracing::info!`/`tracing::error!` structured log calls to `gitstore-git-service/src/grpc/git_service.rs` ReceivePack handler at: stream start (repo_id), each chunk received (chunk_index, bytes), pack finalisation result, quarantine promotion result, each ref update result, any error

**Checkpoint**: `git push` succeeds end-to-end against a running stack. Large push (≥512 MB) completes without OOM. T006 (receive-pack contract test) passes.

---

## Phase 4: User Story 2 — Git Smart HTTP Lives in gitstore-api on Port 5000 (Priority: P1)

**Goal**: `git clone`, `git fetch`, and `git push` all work via `http://localhost:5000/<namespace>/<repo>[.git]`; `gitstore-git-service` exposes no HTTP listener.

**Independent Test**: Start the stack; run `git clone http://localhost:5000/gitstore/catalog`, `git fetch`, `git push`. All three succeed. Confirm no HTTP port open on `gitstore-git-service` (`ss -tlnp` shows only port 50051).

### Implementation for User Story 2

- [x] T021 [US2] Implement `InfoRefs` gRPC handler in `gitstore-git-service/src/grpc/git_service.rs` — dispatches on `Service` enum: `GIT_UPLOAD_PACK` → calls `advertise_upload_pack_refs(repo_path)`; `GIT_RECEIVE_PACK` → calls `advertise_receive_pack_refs(repo_path)`; returns `InfoRefsResponse { advertisement, service }`
- [x] T022 [US2] Implement `InfoRefs` method in `gitstore-api/internal/gitclient/grpc_client.go` — calls git service `InfoRefs` unary RPC; returns `(advertisement []byte, service Service, err error)`
- [x] T023 [US2] Implement `infoRefsHandler` in `gitstore-api/internal/githttp/handler.go` — resolves `(namespace, repo)` → `repo_id`; strips `.git` suffix from `{repo}` before lookup; reads `service` query param; calls `gitclient.InfoRefs`; writes pkt-line service header (`001e# service=git-upload-pack\n0000` or `001f# service=git-receive-pack\n0000`) followed by advertisement bytes; sets correct Content-Type per service; fast-fails with `ERR` pkt-line on gRPC unavailability or 404 on unknown repo; emits structured log entries at stream start and completion
- [x] T024 [US2] Refactor `gitstore-git-service/src/git/pack_server.rs` — adapt `handle_upload_pack` to yield sideband-encoded chunks via an iterator/stream (returns `impl Iterator<Item=Result<Vec<u8>>>` of 64 KiB pkt-line chunks) rather than accumulating into a `Vec<u8>`
- [x] T025 [US2] Implement `UploadPack` gRPC handler in `gitstore-git-service/src/grpc/git_service.rs` — receives `UploadPackRequest { repository_id, body }`; calls `parse_wants_and_haves(&body)` and `build_pack_for_wants`; streams sideband chunks back as `UploadPackChunk` messages with `chunk_index` and `is_last`; emits structured tracing at stream start, each chunk sent, stream complete, any error
- [x] T026 [US2] Implement `UploadPack` method in `gitstore-api/internal/gitclient/grpc_client.go` — buffers small request body (want/have negotiation, bounded); calls `UploadPack` server-streaming RPC using `UploadPackReceiver` from `stream.go`; returns a `io.Reader` that yields chunks for the HTTP handler to stream
- [x] T027 [US2] Implement `uploadPackHandler` in `gitstore-api/internal/githttp/handler.go` — resolves repo_id; buffers request body; calls `gitclient.UploadPack`; streams response chunks to HTTP response writer with `Transfer-Encoding: chunked` and Content-Type `application/x-git-upload-pack-result`; fast-fails on unavailability; emits structured log entries at stream start, each chunk, stream complete, any error
- [x] T028 [US2] Add port 5000 `http.Server` to `gitstore-api/cmd/server/main.go` — create `gitMux` (`net/http.ServeMux`) registering the six Git smart HTTP route patterns (with and without `.git` suffix) plus `/health` and `/ready`; wrap with `RequestIDMiddleware` only; create `gitSrv := &http.Server{Addr: fmt.Sprintf(":%d", cfg.Api.GitPort), ...}`; start in separate goroutine; include in graceful shutdown block alongside existing API server
- [x] T029 [US2] Add `GitPort int` field to `ApiConfig` in `gitstore-api/internal/config/config.go` with default `5000`; add `v.SetDefault("api.git_port", 5000)` and log line; add `"api.git_port": true` to required-keys map
- [x] T030 [US2] Remove `Ws GitEndpointConfig` and `Http GitEndpointConfig` fields from `GitConfig` in `gitstore-api/internal/config/config.go`; remove `git.ws.uri` and `git.http.uri` defaults, log lines, and entries from required-keys map; update `config_test.go` accordingly
- [x] T031 [US2] Remove HTTP server startup block from `gitstore-git-service/src/main.rs` (lines starting WS and HTTP goroutines; keep only gRPC startup); remove `[http]` port config section from `gitstore-git-service/src/config.rs`; remove `axum` from `gitstore-git-service/Cargo.toml`
- [x] T032 [US2] Activate integration tests in `tests/integration/git_http_test.go` — remove `t.Skip`; implement `TestGitClone`, `TestGitFetch`, `TestGitPush` using `exec.Command("git", ...)` against `http://localhost:5000/gitstore/catalog`; assert exit codes and verify object storage after push

**Checkpoint**: `git clone`, `git fetch`, `git push` over port 5000 all pass. `gitstore-git-service` has no HTTP listener. T007 (upload-pack contract test) and T008 (receive-pack contract test) pass. Integration tests `TestGitClone`, `TestGitFetch`, `TestGitPush` pass.

---

## Phase 5: User Story 3 — WebSocket Removed from Both Services (Priority: P2)

**Goal**: No WebSocket listener in `gitstore-git-service`, no WebSocket client in `gitstore-api`, no related dependencies, config keys, or env vars in either service.

**Independent Test**: Start both services; `ss -tlnp` shows no WebSocket port (formerly 8080). Run `git push`; confirm push succeeds and no WebSocket log lines appear in either service. `grep -r "websocket" gitstore-api/internal/` returns nothing. `grep "gorilla/websocket" gitstore-api/go.mod` returns nothing.

### Implementation for User Story 3

- [x] T033 [US3] Delete `gitstore-git-service/src/websocket/` module directory (server.rs, connections.rs, broadcast.rs, mod.rs) and remove `mod websocket;` declaration from `gitstore-git-service/src/lib.rs` or `main.rs`
- [x] T034 [US3] Delete `gitstore-git-service/src/git/events.rs` and remove all imports of `GitEvent`, `Broadcaster` from remaining files in `gitstore-git-service/src/`
- [x] T035 [US3] Remove WebSocket server startup block and `broadcaster` construction from `gitstore-git-service/src/main.rs`; remove `broadcaster` field from `GitServerState` struct; remove `ws_handle` from the shutdown join set
- [x] T036 [US3] Remove `[ws]` config section and `ws: PortConfig` field from `gitstore-git-service/src/config.rs`; remove `GITSTORE_WS__PORT` from any documentation or example config files in `gitstore-git-service/`
- [x] T037 [US3] Remove `tokio_tungstenite` and `tungstenite` from `gitstore-git-service/Cargo.toml`; run `cargo check` to confirm clean build
- [x] T038 [P] [US3] Delete `gitstore-api/internal/websocket/` package (client.go and any test files)
- [x] T039 [P] [US3] Remove `github.com/gorilla/websocket` from `gitstore-api/go.mod` and `gitstore-api/go.sum`; run `go mod tidy` to confirm clean
- [x] T040 [P] [US3] Remove `GITSTORE_GIT__WS__URI` and `GITSTORE_GIT__HTTP__URI` from `gitstore-api/.env`, `gitstore-api/.env.example`, and any documentation referencing them
- [x] T041 [US3] Verify `gitstore-git-service` builds and starts with only the gRPC server running; verify no port 8080 or 9418 listener appears in `ss -tlnp`
- [x] T042 [US3] Verify `gitstore-api` builds, starts, and passes all existing tests with WebSocket package and config fields removed; confirm `go test ./...` is clean

**Checkpoint**: Both services build and start cleanly. No WebSocket code, dependencies, config keys, or env vars remain in either service. A `git push` succeeds and neither service logs any WebSocket activity. T009 and T010 pass.

---

## Phase 6: Polish & Cross-Cutting Concerns

**Purpose**: Infrastructure, documentation, and constitution updates that span all three stories.

- [x] T043 Update `compose.yml` — expose port 5000 for `gitstore-api`; add healthcheck probe `curl -f http://localhost:5000/health` with `interval: 10s`, `timeout: 5s`, `retries: 3`; remove `GITSTORE_WS__PORT`, `GITSTORE_HTTP__PORT`, `GITSTORE_GIT__WS__URI`, `GITSTORE_GIT__HTTP__URI` from the git-service and api service environment sections
- [x] T044 [P] Update architecture documentation in `docs/` — update service topology diagram and port table: `gitstore-api` serves port 4000 (GraphQL/REST) and port 5000 (Git smart HTTP); `gitstore-git-service` serves only port 50051 (gRPC); remove any reference to WebSocket or git-service HTTP port
- [x] T045 [P] Update `specs/012-smart-http-api/quickstart.md` environment variable table if any env var names changed during implementation (verify against actual `.env.example` state)
- [x] T046 Update `constitution.md` Architecture Constraints section — in the "Git Server (Rust)" bullet replace "websocket notifications" with "gRPC notification stream (pending GH#139)"; update performance targets table to remove `Websocket notification delivery: < 100ms` line
- [x] T047 Run `make pr-ready` from repo root and resolve any build, lint, or licence-check failures; confirm `make test` is green

---

## Dependencies & Execution Order

### Phase Dependencies

- **Setup (Phase 1)**: No dependencies — start immediately
- **Foundational (Phase 2)**: Depends on Phase 1 (T001–T005 complete) — BLOCKS all story phases
- **US1 (Phase 3)**: Depends on Phase 2 — can proceed as soon as T006–T013 are done
- **US2 (Phase 4)**: Depends on Phase 2 — can proceed in parallel with US1 (different files)
- **US3 (Phase 5)**: Depends on Phase 2 — can proceed in parallel with US1/US2; logically cleaner to start after US2 (avoids touching git-service startup twice), but not a hard dependency
- **Polish (Phase 6)**: Depends on US1 + US2 + US3 complete

### User Story Dependencies

- **US1 (P1)**: Independent — operates on `pack_server.rs` receive path and `gitclient/grpc_client.go`
- **US2 (P1)**: Independent — operates on `pack_server.rs` upload path, `http_git_server.rs` removal, `githttp/handler.go`, `main.go` second server
- **US3 (P2)**: Independent — operates on `websocket/` deletion and `config.go` cleanup; logically sequenced after US2 since US2 already removes the HTTP server (the other WS integration point)

### Within Each User Story

- Tests (Phase 2, T006–T011) MUST be written and confirmed failing before any implementation task in Phases 3–5
- In US1: T014 and T015 (pack_server refactor) before T016–T017 (gRPC handler implementation)
- In US2: T021–T025 (git-service handlers) can proceed in parallel with T028–T030 (gitstore-api server setup); T026–T027 depend on T025
- In US3: T033–T037 (git-service WS removal) can proceed in parallel with T038–T040 (gitstore-api WS removal)

### Parallel Opportunities

- T004 and T005 (skeleton creation) can run in parallel after T001–T003
- T006–T011 (contract + integration test writing) can all run in parallel after T012–T013 compile
- T014 and T015 can run in parallel (separate function extractions in the same file)
- T021 and T022 can run in parallel with T028–T030
- T033–T037 can run in parallel with T038–T040

---

## Parallel Example: User Story 1

```bash
# These two refactors are independent (different function extractions):
Task T014: "Extract parse_ref_commands(reader) in gitstore-git-service/src/git/pack_server.rs"
Task T015: "Extract stage_pack_from_reader(repo, reader) in gitstore-git-service/src/git/pack_server.rs"

# These two are independent (different services):
Task T018: "Implement ReceivePack method in gitstore-api/internal/gitclient/grpc_client.go"
Task T016: "Implement ReceivePack gRPC handler in gitstore-git-service/src/grpc/git_service.rs"
```

## Parallel Example: User Story 2

```bash
# Git-service side and gitstore-api config can run in parallel:
Task T021: "InfoRefs handler in gitstore-git-service/src/grpc/git_service.rs"
Task T029: "Add GitPort to ApiConfig in gitstore-api/internal/config/config.go"
Task T030: "Remove Ws/Http from GitConfig in gitstore-api/internal/config/config.go"
```

## Parallel Example: User Story 3

```bash
# Removal tasks in each service are fully independent:
Task T033: "Delete gitstore-git-service/src/websocket/ module"
Task T038: "Delete gitstore-api/internal/websocket/ package"
Task T039: "Remove gorilla/websocket from gitstore-api/go.mod"
Task T040: "Remove WS/HTTP env vars from gitstore-api/.env and .env.example"
```

---

## Implementation Strategy

### MVP First (User Stories 1 + 2 — both P1)

1. Complete Phase 1: Setup (proto + stubs)
2. Complete Phase 2: Foundational (contract tests failing)
3. Complete Phase 3: US1 (receive-pack streaming — no OOM)
4. Complete Phase 4: US2 (port 5000 HTTP, info-refs, upload-pack, HTTP server removal from git-service)
5. **STOP and VALIDATE**: `git clone`, `git fetch`, `git push` over port 5000 all pass; large push succeeds without OOM
6. Deploy/demo MVP

### Incremental Delivery

1. Setup + Foundational → tests compiled and failing
2. US1 → large push works without OOM; receive-pack contract test passes
3. US2 → full `git clone`/`fetch`/`push` via port 5000 works; git-service has no HTTP port
4. US3 → WebSocket fully removed from both services
5. Polish → docs, Compose, constitution updated; `make pr-ready` green

---

## Notes

- `[P]` tasks operate on different files and have no blocking dependencies within their phase
- Each user story phase delivers a testable increment; the stack can be validated at each checkpoint
- Write tests (Phase 2) first and confirm they fail before writing any Phase 3–5 implementation code
- T031 (HTTP server removal from git-service) and T028 (port 5000 addition to gitstore-api) should ideally land in the same commit so there is no window where the old HTTP port is live alongside the new one
- The WebSocket broadcast calls in `http_git_server.rs` are already removed when T031 deletes that file entirely — T033–T036 clean up the WS server and its dependencies independently
