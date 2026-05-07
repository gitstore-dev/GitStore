# Tasks: Decouple API from Git Storage via gRPC Git Service

**Input**: Design documents from `/specs/004-grpc-git-service/`
**Prerequisites**: plan.md ✅, spec.md ✅, research.md ✅, data-model.md ✅, contracts/ ✅

**Tests**: Test-First Development (Constitution Principle I — NON-NEGOTIABLE). Tests MUST be written before implementation code and verified to FAIL before implementation begins.

**Organization**: Tasks are grouped by user story to enable independent implementation and testing of each story.

**Integration test note**: `testcontainers-go` integration tests live in `gitstore-api/tests/integration/` and start a real git-service container within the Go test process. The repo-level E2E suite (`tests/integration/`) runs against externally started services (docker compose in CI) and is unaffected by this feature.

## Format: `[ID] [P?] [Story] Description`

- **[P]**: Can run in parallel (different files, no dependencies)
- **[Story]**: Which user story this task belongs to (US1–US4)

---

## Phase 1: Setup (Shared Infrastructure)

**Purpose**: Establish the proto toolchain, buf workspace, and generated stub directories before any service code is written.

- [x] T001 Create `shared/proto/gitstore/git/v1/git_service.proto` — copy from `specs/004-grpc-git-service/contracts/gitstore.git.v1.proto` (source of truth moves here)
- [x] T002 Create `shared/proto/buf.yaml` (module definition, package `gitstore.git.v1`, lint rules: DEFAULT)
- [x] T003 Create `buf.gen.rust.yaml` at repo root — configure `protoc-gen-prost` + `protoc-gen-tonic` output to `gitstore-git-service/gen/`
- [x] T004 Create `buf.gen.go.yaml` at repo root — configure `protoc-gen-go` + `protoc-gen-go-grpc` output to `gitstore-api/gen/`
- [x] T005 Run `buf generate shared/proto/ --template buf.gen.rust.yaml` and commit generated Rust stubs to `gitstore-git-service/gen/` (Rust stubs generated at build time via build.rs; no buf needed locally)
- [x] T006 Run `buf generate shared/proto/ --template buf.gen.go.yaml` and commit generated Go stubs to `gitstore-api/gen/` (generated with protoc directly)
- [x] T007 Add `buf breaking shared/proto/ --against '.git#branch=main'` as a CI check in `.github/workflows/`

**Checkpoint**: `shared/proto/` contains the canonical `.proto`; both `gitstore-git-service/gen/` and `gitstore-api/gen/` contain generated stubs. `buf lint` passes.

---

## Phase 2: Foundational (Blocking Prerequisites)

**Purpose**: Wire gRPC dependencies into both services and establish the gRPC server skeleton in git-service and gRPC client skeleton in API. No business logic yet — just compile-clean stubs.

**⚠️ CRITICAL**: No user story work can begin until this phase is complete.

### git-service (Rust)

- [x] T008 Add tonic, prost, prost-types, prometheus, tonic-prometheus-layer to `gitstore-git-service/Cargo.toml`; add tonic-build to `[build-dependencies]`
- [x] T009 Create `gitstore-git-service/build.rs` — configure `tonic_build::configure().compile_protos()` pointing at `shared/proto/gitstore/git/v1/git_service.proto`
- [x] T010 Create `gitstore-git-service/src/grpc/mod.rs` — declare `grpc` module
- [x] T011 Create `gitstore-git-service/src/grpc/server.rs` — empty `GitServiceImpl` struct implementing the tonic-generated `GitService` trait; all RPCs return `UNIMPLEMENTED`
- [x] T012 Create `gitstore-git-service/src/grpc/metrics.rs` — register `tonic-prometheus-layer` Tower layer; expose `grpc_server_handled_total` and `grpc_server_handling_seconds` on the existing Prometheus registry
- [x] T013 Update `gitstore-git-service/src/main.rs` — bind gRPC server (default port `GITSTORE_GRPC_PORT` env var, fallback `50051`) alongside existing git-protocol and websocket servers using `tokio::join!`
- [x] T014 Verify `cargo build` passes with the new gRPC skeleton (no business logic required yet)

### API (Go)

- [x] T015 Add `google.golang.org/grpc`, `google.golang.org/protobuf`, `github.com/grpc-ecosystem/go-grpc-prometheus` to `gitstore-api/go.mod` via `go get`
- [x] T016 Create `gitstore-api/internal/gitclient/grpc_client.go` — `Client` struct with `grpc.NewClient`, `GITSTORE_GIT_GRPC` env var config, exponential-backoff retry interceptor, and `Close()` method
- [x] T017 Create `gitstore-api/internal/gitclient/metrics.go` — wire `grpcprom.UnaryClientInterceptor` + `grpcprom.StreamClientInterceptor` into the `grpc.NewClient` dial options; register metrics on the existing Prometheus registry
- [x] T018 Verify `go build ./...` passes with the new gRPC client skeleton

**Checkpoint**: Both services compile cleanly with gRPC dependencies wired. git-service binds a gRPC port at startup (all RPCs return UNIMPLEMENTED). API dials git-service on startup (no calls yet).

---

## Phase 3: User Story 1 — Operator Scales API Without Shared Storage (Priority: P1) 🎯 MVP

**Goal**: API reads catalogue entirely via gRPC; no shared volume mount required. Three replicas can start independently and serve identical catalogue data.

**Independent Test**: Remove the `volumes` entry from the API service in `compose.yml`, set `GITSTORE_GIT_GRPC=git-service:50051`, start three API instances, push a catalogue tag, verify all three serve the same product list.

### Tests for User Story 1 ⚠️ Write FIRST — verify FAIL before implementing

- [x] T019 [P] [US1] Write integration test `gitstore-api/tests/integration/grpc_catalogue_load_test.go` — uses testcontainers-go to start a real git-service container; asserts API loads catalogue via gRPC with no shared volume (tag: `integration`)
- [x] T020 [P] [US1] Write unit test `gitstore-api/internal/gitclient/read_test.go` — stubs gRPC server with `bufconn`; tests `GetFile`, `ListFiles`, `GetLatestTag` client calls and error mapping
- [x] T021 [P] [US1] Write unit test `gitstore-api/internal/catalog/loader_test.go` — mocks gRPC client interface; verifies `LoadFromTag` and `LoadFromLatestTag` call the correct RPCs and parse returned bytes correctly

### Implementation for User Story 1

- [x] T022 [US1] Implement gRPC read operations in `gitstore-git-service/src/grpc/server.rs` — `GetFile`: open bare repo, resolve ref to tree, return raw blob bytes; `ListFiles`: walk tree under prefix, return `FileEntry` list
- [x] T023 [US1] Implement `GetLatestTag` and `ListTags` in `gitstore-git-service/src/grpc/server.rs` — reuse existing `git/repo.rs` `list_tags()` helper; add semver sort for `GetLatestTag`
- [x] T024 [P] [US1] Implement `GetFileStream` in `gitstore-git-service/src/grpc/server.rs` — server-streaming RPC that chunks file bytes at 256 KiB; used by catalogue loader for large repos
- [x] T025 [US1] Create `gitstore-api/internal/gitclient/read.go` — `ReadFile(ctx, path, ref)`, `ListFiles(ctx, prefix, ref)`, `ListTags(ctx, prefix)`, `GetLatestTag(ctx)` methods wrapping generated gRPC stubs
- [x] T026 [US1] Update `gitstore-api/internal/catalog/loader.go` — replace all `git.PlainOpen` / `go-git` calls with calls to the new `gitclient.Client` read methods; keep `LoadFromTag` and `LoadFromLatestTag` signatures unchanged
- [x] T027 [US1] Remove `github.com/go-git/go-git/v5` import from `gitstore-api/internal/catalog/loader.go`; run `go mod tidy` to prune transitive go-git dependencies
- [x] T028 [US1] Update `compose.yml` — remove `volumes` entry from the `api` service (`${GITSTORE_DATA_DIR:-git-data}:/data/repos:ro`); add `GITSTORE_GIT_GRPC=git-service:50051` env var; add port `50051` to git-service
- [x] T029 [US1] Update `gitstore-api/cmd/server/main.go` — replace `GITSTORE_GIT_REPO` local-path wiring with `GITSTORE_GIT_GRPC` address; remove shared-volume startup check

**Checkpoint**: API starts with no shared volume. `docker compose up` succeeds. Three replicas all serve the correct catalogue. `go test ./tests/integration/... -tags integration` passes.

---

## Phase 4: User Story 2 — Developer Performs Write Mutations Through Service Boundary (Priority: P2)

**Goal**: All GraphQL write mutations (createProduct, updateProduct, deleteProduct, createCategory, etc.) route through the gRPC `CommitFile` / `DeleteFile` RPCs. API holds no git working directory state.

**Independent Test**: Submit ten simultaneous `createProduct` mutations via GraphQL; verify all ten products appear in the next catalogue tag; verify zero artefacts remain on the API host filesystem.

### Tests for User Story 2 ⚠️ Write FIRST — verify FAIL before implementing

- [x] T030 [P] [US2] Write integration test `gitstore-api/tests/integration/grpc_mutations_test.go` — uses testcontainers-go; exercises concurrent `CommitFile` and `DeleteFile` calls; asserts no filesystem artefacts on API side (tag: `integration`)
- [x] T031 [P] [US2] Write unit test `gitstore-api/internal/gitclient/write_test.go` — bufconn stub; tests `CommitFile` and `DeleteFile` client calls, error surface, and retry behaviour on transient failures
- [x] T032 [P] [US2] Write unit tests for mutations in `gitstore-api/internal/graph/mutations_test.go` — assert mutations call gRPC client methods (not CommitBuilder) and correctly propagate errors

### Implementation for User Story 2

- [x] T033 [US2] Implement `CommitFile` in `gitstore-git-service/src/grpc/server.rs` — acquire write lock on repo, apply file write to a temporary worktree clone, commit, push to bare repo, return commit SHA; clean up temp dir on success and failure
- [x] T034 [US2] Implement `DeleteFile` in `gitstore-git-service/src/grpc/server.rs` — same pattern as CommitFile but removes the file before committing
- [x] T035 [US2] Implement `CreateTag` in `gitstore-git-service/src/grpc/server.rs` — create annotated tag on HEAD (or supplied commit SHA) using `git2`
- [x] T036 [P] [US2] Create `gitstore-api/internal/gitclient/write.go` — `CommitFile(ctx, path, content, message, author)`, `DeleteFile(ctx, path, message, author)`, `CreateTag(ctx, name, message, target)` wrapping generated gRPC stubs
- [x] T037 [US2] Update `gitstore-api/internal/graph/mutations.go` — replace all `CommitBuilder` usages with calls to `gitclient.Client` write methods; keep existing GraphQL resolver signatures unchanged
- [x] T038 [US2] Delete `gitstore-api/internal/gitclient/commit.go`, `push.go`, `pool.go`, `tag.go`, `http_client.go` — replaced by gRPC client; update any remaining import references
- [x] T039 [US2] Move markdown generation helpers (`ProductFrontMatter`, `CategoryFrontMatter`, `CollectionFrontMatter`, `GenerateProductMarkdown`, etc.) from `gitstore-api/internal/gitclient/writer.go` to `gitstore-api/internal/catalog/writer.go` — pure serialisation, no I/O, no git dependency
- [x] T040 [US2] Delete `gitstore-api/internal/gitclient/writer.go` after helpers are moved; run `go build ./...` to confirm no broken imports

**Checkpoint**: All mutations route through gRPC. `go test ./internal/graph/...` passes. Concurrent mutation integration test passes. `gitclient/` contains only `grpc_client.go`, `read.go`, `write.go`, `metrics.go`.

---

## Phase 5: User Story 3 — Operator Retains Real-Time Catalogue Update Propagation (Priority: P3)

**Goal**: Websocket release-tag notifications continue to trigger catalogue reloads, but the reload now fetches via gRPC instead of pulling from a local working directory.

**Independent Test**: Push a release tag; confirm websocket notification arrives at API; confirm API calls `GetLatestTag` + `ListFiles` + `GetFile` (not a git pull); confirm updated catalogue is served within 30 seconds.

### Tests for User Story 3 ⚠️ Write FIRST — verify FAIL before implementing

- [x] T041 [P] [US3] Write integration test `gitstore-api/tests/integration/grpc_reload_test.go` — pushes a tag via testcontainers-go git-service; verifies API receives WS notification and reloads catalogue via gRPC within 30s (tag: `integration`)
- [x] T042 [P] [US3] Write unit test `gitstore-api/internal/cache/manager_test.go` — asserts that on WS `tag_push` event the cache manager calls gRPC read methods (not local git pull); tests coalescing of rapid-fire notifications

### Implementation for User Story 3

- [x] T043 [US3] Update `gitstore-api/internal/cache/manager.go` — replace local git pull/fetch logic (if any) in the WS event handler with a call to `gitclient.Client.GetLatestTag()` + `LoadFromTag()`; preserve exponential-backoff retry on gRPC error
- [x] T044 [US3] Implement notification coalescing in `gitstore-api/internal/cache/manager.go` — if a reload is in progress when a new WS notification arrives, queue it once; subsequent arrivals while queued are dropped (last-writer-wins)
- [x] T045 [US3] Verify `gitstore-api/internal/websocket/client.go` requires no changes — it delivers `GitEvent` to the handler; the handler is updated in T043

**Checkpoint**: Push a tag → websocket fires → API reloads via gRPC → updated catalogue served. No shared volume anywhere in the flow.

---

## Phase 6: User Story 4 — Developer Validates the Service Contract with Automated Tests (Priority: P4)

**Goal**: CI enforces the gRPC contract so changes to either service that break the contract are caught before merge.

**Independent Test**: Run `go test ./gitstore-api/tests/integration/... -tags integration` in CI against a real git-service container; all read and write paths pass including error and concurrency scenarios.

### Tests for User Story 4 ⚠️ Write FIRST — verify FAIL before implementing

- [x] T046 [P] [US4] Write contract error-path test `gitstore-api/tests/integration/grpc_errors_test.go` — covers: file not found, ref not found, commit on read-only state, tag already exists (tag: `integration`)
- [x] T047 [P] [US4] Write concurrency test `gitstore-api/tests/integration/grpc_concurrency_test.go` — 10 simultaneous `CommitFile` calls; asserts all succeed with distinct commit SHAs; no conflicts (tag: `integration`)

### Implementation for User Story 4

- [x] T048 [US4] Add `buf breaking shared/proto/ --against '.git#branch=main'` step to CI workflow `.github/workflows/ci.yml` — fails PR if proto contract has breaking changes
- [x] T049 [US4] Add integration test step to CI workflow `.github/workflows/ci.yml` — `go test ./gitstore-api/tests/integration/... -tags integration -timeout 5m` using testcontainers-go (Docker required on CI runner)
- [x] T050 [US4] Add Rust gRPC server unit tests in `gitstore-git-service/src/grpc/server.rs` (`#[cfg(test)]`) — covers: `GetFile` happy path, `GetFile` with unknown ref returns NOT_FOUND, `CommitFile` creates a real commit in a temp repo, `DeleteFile` on nonexistent file returns NOT_FOUND
- [x] T051 [US4] Update `gitstore-api/tests/testutil/graphql.go` — add `grpcAddr` helper to start an in-process gRPC stub server using `bufconn` for unit-level contract tests that don't need Docker

**Checkpoint**: `buf breaking` runs in CI. Integration test suite passes in CI with testcontainers-go. All error paths and concurrency scenarios covered.

---

## Phase 7: Polish & Cross-Cutting Concerns

- [x] T052 [P] Update architecture docs in `docs/` — add service boundary diagram showing API → gRPC → git-service; document that API no longer mounts a git volume
- [x] T053 [P] Update `docs/` developer setup guide — replace `GITSTORE_GIT_REPO` with `GITSTORE_GIT_GRPC`; add buf toolchain setup instructions from `quickstart.md`
- [x] T054 Run `cargo clippy --all-targets --all-features -- -D warnings` on `gitstore-git-service` and fix any warnings introduced by gRPC code
- [x] T055 Run `staticcheck ./...` and `go vet ./...` on `gitstore-api` and fix any warnings
- [x] T056 [P] Verify all new Go files carry the AGPL license header (run `./scripts/check-go-license-headers.sh --all`)
- [x] T057 [P] Verify all new Rust files carry the AGPL license header (run `./scripts/check-rust-license-headers.sh --all`)
- [x] T058 Run `go mod tidy` in `gitstore-api/` and confirm `go-git` and all its transitive deps are absent from `go.sum`
- [x] T059 Validate `quickstart.md` end-to-end: follow every step in `specs/004-grpc-git-service/quickstart.md` from a clean checkout and confirm all commands succeed

---

## Dependencies & Execution Order

### Phase Dependencies

- **Phase 1 (Setup)**: No dependencies — start immediately
- **Phase 2 (Foundational)**: Depends on Phase 1 (generated stubs must exist) — **BLOCKS all user stories**
- **Phase 3 (US1)**: Depends on Phase 2 — delivers MVP
- **Phase 4 (US2)**: Depends on Phase 2; integrates with Phase 3 gRPC client
- **Phase 5 (US3)**: Depends on Phase 3 (catalogue load via gRPC must work before reload via gRPC can be tested)
- **Phase 6 (US4)**: Depends on Phase 3 + Phase 4 (needs full read+write contract in place)
- **Phase 7 (Polish)**: Depends on all user stories

### User Story Dependencies

- **US1 (P1)**: Can start after Phase 2 — no dependencies on other stories
- **US2 (P2)**: Can start after Phase 2 — can proceed in parallel with US1 (different files)
- **US3 (P3)**: Depends on US1 complete (gRPC catalogue load must work before gRPC reload can be tested)
- **US4 (P4)**: Depends on US1 + US2 complete (needs full read+write contract exercised)

### Within Each User Story

1. Write tests first, verify they FAIL
2. Implement server-side gRPC handler (git-service, Rust)
3. Implement client-side wrapper (API, Go)
4. Update callers (catalog/loader.go, mutations.go, cache/manager.go)
5. Verify tests now PASS
6. Run `cargo build` + `go build ./...` before moving on

### Parallel Opportunities

- T019, T020, T021 (US1 tests) — all parallel, different files
- T022, T023, T024 (US1 server impl) — T022+T023 share server.rs; T024 is separate streaming RPC, parallelisable
- T025, T026, T027, T028, T029 — sequential (each depends on previous)
- T030, T031, T032 (US2 tests) — all parallel
- T033, T034, T035 share server.rs (sequential); T036 is parallel
- T046, T047 (US4 tests) — parallel
- T052, T053, T056, T057 (Polish) — all parallel

---

## Parallel Example: User Story 1

```bash
# Step 1 — launch tests in parallel (all fail initially):
Task T019: grpc_catalogue_load_test.go (testcontainers-go integration)
Task T020: read_test.go (bufconn unit)
Task T021: loader_test.go (mocked gRPC client)

# Step 2 — implement server-side read RPCs (T022+T023 share server.rs, T024 parallel):
Task T022+T023: GetFile, ListFiles, GetLatestTag, ListTags in server.rs
Task T024:      GetFileStream in server.rs (separate RPC, can be parallel if two developers)

# Step 3 — implement client-side and update callers (sequential):
Task T025 → T026 → T027 → T028 → T029
```

---

## Implementation Strategy

### MVP First (User Story 1 Only)

1. Complete Phase 1: Setup (buf toolchain + stubs)
2. Complete Phase 2: Foundational (gRPC skeleton compiles in both services)
3. Complete Phase 3: User Story 1 (read path + catalogue load + compose.yml update)
4. **STOP and VALIDATE**: Three API replicas, no shared volume, identical catalogue served
5. Ship MVP — US1 alone delivers the primary production blocker

### Incremental Delivery

1. Phase 1 + 2 → both services compile with gRPC wired
2. US1 → catalogue reads via gRPC, no shared volume → **MVP**
3. US2 → mutations via gRPC, go-git removed from API
4. US3 → WS-triggered reload via gRPC
5. US4 → CI contract enforcement + full integration test coverage
6. Polish → docs, lint, license headers

### Parallel Team Strategy

After Phase 2:
- Developer A: US1 (gRPC reads, catalogue loader, compose.yml)
- Developer B: US2 (gRPC writes, CommitFile/DeleteFile server impl, mutations.go migration)
- These two can proceed in parallel — different files, no conflicts

---

## Notes

- `[P]` tasks = different files, no runtime dependencies on incomplete tasks
- `[Story]` label maps task to specific user story for traceability
- Constitution Principle I is enforced: every implementation phase is preceded by a test task block
- `tests/integration/` at repo root is untouched — it tests the full stack via docker compose
- `gitstore-api/tests/integration/` gets new testcontainers-go tests for gRPC-specific scenarios
- Verify `go-git` is fully absent from `go.sum` after T058 — this is the hard proof of FR-005
