# Tasks: Git Smart-HTTP Authentication

**Input**: Design documents from `specs/035-git-http-auth/`  
**Prerequisites**: plan.md ✅ · spec.md ✅ · research.md ✅ · data-model.md ✅ · contracts/ ✅ · quickstart.md ✅

**Tests**: Test-First Development (Constitution Principle I — NON-NEGOTIABLE). Tests MUST be written before implementation and MUST fail before implementation begins.

**Organization**: Tasks are grouped by user story to enable independent implementation and testing of each story.

## Format: `[ID] [P?] [Story] Description`

- **[P]**: Can run in parallel (different files, no dependencies on each other)
- **[Story]**: Which user story this task belongs to (US1–US4)

---

## Phase 1: Setup (Shared Infrastructure)

**Purpose**: Ensure proto toolchain and generated bindings are in place before any implementation begins.

- [x] T001 Add `PushContext`, `AuthContext`, `PushPolicy` messages to `shared/proto/gitstore/git/v1/git_service.proto` per `contracts/git_service.proto` (field numbers per data-model.md)
- [x] T002 Add `push_context PushContext = 4` field to `ReceivePackRequest` in `shared/proto/gitstore/git/v1/git_service.proto` with comment "MUST be set on the first chunk only"
- [x] T003 Regenerate Go bindings: `gitstore-api/gen/gitstore/git/v1/git_service.pb.go` and `git_service_grpc.pb.go`
- [x] T004 Regenerate Rust bindings in `gitstore-git-service` (run `cargo build` to verify compilation)
- [x] T005 [P] Add `MaxPackSizeBytes int64` and `MaxFileSizeBytes int64` fields to `datastore.Repository` struct in `gitstore-api/internal/datastore/entities.go`
- [x] T006 [P] Update memdb schema in `gitstore-api/internal/datastore/memdb/schema.go` to include the two new policy fields (no-op: memdb indexes only id and namespace_id; scalar fields round-trip via the stored struct)
- [x] T007 [P] Update scylla DDL in `gitstore-api/internal/datastore/scylla/` to add the two new policy columns (zero default)

**Checkpoint**: Proto bindings compile in both Go and Rust; `datastore.Repository` struct has push policy fields.

---

## Phase 2: Foundational (Blocking Prerequisites)

**Purpose**: Unit tests for the two new data surfaces (proto + datastore entity) that all user stories depend on. Must be written and failing before Phase 3 begins.

**⚠️ CRITICAL**: No user story work can begin until this phase is complete.

- [x] T008 Write unit test in `gitstore-api/internal/datastore/memdb/backend_test.go` verifying that `MaxPackSizeBytes = 0` and `MaxFileSizeBytes = 0` round-trip correctly and are returned by `GetRepository`
- [x] T009 [P] Write unit test verifying proto serialisation: a `ReceivePackRequest` with `push_context` set on the first chunk and absent on subsequent chunks encodes/decodes correctly
- [x] T010 Implement: verify T008 and T009 tests now pass (no other implementation needed — proto + entity changes from Phase 1 should be sufficient)

**Checkpoint**: Foundation ready — `datastore.Repository` round-trips policy fields; proto `PushContext` encodes correctly. User story phases may now begin.

---

## Phase 3: User Story 1 — AuthN Gate (Priority: P1) 🎯 MVP

**Goal**: Every Git smart-HTTP request passes through `BasicAuthenticator`; transient errors return 503, credential rejections return 401, valid credentials pass through. Auth outcomes are counted in Prometheus and queryable from `/metrics`.

**Independent Test**: `curl -s http://localhost:4000/metrics | grep gitstore_git_http_auth` returns non-zero counters after an authenticated and an unauthenticated request.

### Tests for User Story 1

> **Write these tests FIRST — verify they FAIL before implementation**

- [x] T011 [P] [US1] Write `TestBasicAuthTransientError` in `gitstore-api/internal/middleware/security/secure_test.go`: inject auth chain that returns `err != nil`; assert response status 503 and no `WWW-Authenticate` header
- [x] T012 [P] [US1] Write `TestBasicAuthCredentialRejection` in `gitstore-api/internal/middleware/security/secure_test.go`: inject auth chain returning `OutcomeDeny` with `err == nil`; assert status 401 and `WWW-Authenticate: Basic realm="GitStore"` header
- [x] T013 [P] [US1] Write `TestBasicAuthAllow` in `gitstore-api/internal/middleware/security/secure_test.go`: inject auth chain returning `OutcomeAllow`; assert request reaches next handler
- [x] T014 [P] [US1] Write `TestMetricsEndpoint` in `gitstore-api/internal/health/health_test.go`: assert `GET /metrics` returns 200 and content-type `text/plain`
- [ ] T015 [US1] Write integration test in `tests/integration/` asserting `gitstore_git_http_auth_requests_total{outcome="allow"}` and `{outcome="deny"}` counters are non-zero after one authenticated and one rejected request against the running server

### Implementation for User Story 1

- [x] T016 [US1] Fix `basicAuth` in `gitstore-api/internal/middleware/security/secure.go`: check `err != nil` first → 503 + `zap.Error` log; check `OutcomeDeny` with `err == nil` → 401 + `WWW-Authenticate` header
- [x] T017 [US1] Add `gitstore_git_http_auth_requests_total` `CounterVec` (labels: `outcome`, `service`) to `gitstore-api/internal/middleware/security/secure.go`; register on injected `prometheus.Registerer`; wire into `NewAuthenticate`; increment in `basicAuth` on allow/deny/error
- [x] T018 [US1] Add `Metrics(c *gin.Context)` handler to `gitstore-api/internal/health/health.go` that serves `promhttp.Handler()`
- [x] T019 [US1] Register `GET /metrics` route in `gitstore-api/internal/app/server.go` `healthHandler` function (alongside `/health` and `/ready`)
- [x] T020 [US1] Call `gitclient.RegisterClientMetrics(prometheus.DefaultRegisterer)` in `gitstore-api/internal/app/server.go` `NewServer` after `gitClient` is created

**Checkpoint**: `BasicAuthenticator` returns 503 on transient errors, 401 on rejections, pass-through on allow. `/metrics` is accessible. Auth counters appear after a push attempt.

---

## Phase 4: User Story 2 — AuthZ Gate (Priority: P1)

**Goal**: After authentication, every Git smart-HTTP request is authorised against the repository. `repository.read` required for upload-pack; `repository.write` required for receive-pack. Missing repository returns 404. All checks performed by dedicated middleware before any handler runs.

**Independent Test**: Configure a read-only principal, attempt `git push`, confirm 403 is returned and the git-service gRPC connection is never opened.

### Tests for User Story 2

> **Write these tests FIRST — verify they FAIL before implementation**

- [x] T021 [P] [US2] Write `TestRepoResolverNotFound` in `gitstore-api/internal/githttp/handler_test.go`: request for unknown namespace/repo; assert 404 pkt-line response
- [x] T022 [P] [US2] Write `TestRepoResolverSetsContext` in `gitstore-api/internal/githttp/handler_test.go`: request for known repo; assert `repoID` is set in gin context via `c.Get("repoID")`
- [x] T023 [P] [US2] Write `TestGitHttpAuthorizerReadOnly` in `gitstore-api/internal/githttp/handler_test.go`: principal with `repository.read` + receive-pack route; assert 403
- [x] T024 [P] [US2] Write `TestGitHttpAuthorizerWriteAllowed` in `gitstore-api/internal/githttp/handler_test.go`: principal with `repository.write` + receive-pack route; assert pass-through
- [x] T025 [P] [US2] Write `TestGitHttpAuthorizerMissingContext` in `gitstore-api/internal/middleware/security/secure_test.go`: `GitHttpAuthorizer` called without `RepoResolver` having run; assert 500

### Implementation for User Story 2

- [x] T026 [US2] Create `gitstore-api/internal/githttp/resolver.go`: define `const repoIDKey = "repoID"`; implement `RepoResolver` gin middleware that calls `store.GetNamespaceByIdentifier` then `store.LookupRepository`, stores result via `c.Set(repoIDKey, repoID)`, aborts with 404 pkt-line on miss
- [x] T027 [US2] Implement `GitHttpAuthorizer` method on `security.Authorize` in `gitstore-api/internal/middleware/security/secure.go`: read `repoID` from `c.Get(repoIDKey)` (500 if missing); read `Principal` from context; determine action (`repository.read` for upload-pack, `repository.write` for receive-pack) from route path; call `registry.AuthZ().Authorize`; abort with 403 on deny
- [x] T028 [US2] Wire `RepoResolver` and `GitHttpAuthorizer` into `githttp.NewMux` in `gitstore-api/internal/githttp/handler.go`: order must be `BasicAuthenticator` → `RepoResolver` → `GitHttpAuthorizer`; `PushContextInserter` on receive-pack route (next phase)
- [x] T029 [US2] Remove per-handler `resolveRepo` calls from `infoRefsHandler`, `uploadPackHandler`, `receivePackHandler` in `gitstore-api/internal/githttp/handler.go`; replace with `c.MustGet(repoIDKey).(string)`
- [x] T030 [US2] Update `SmartHttpDeps` in `gitstore-api/internal/githttp/handler.go` to include `Store datastore.Datastore` field; update `NewMux` wiring in `gitstore-api/internal/app/server.go` accordingly

**Checkpoint**: 404 for unknown repos, 403 for insufficient permissions, 401 for bad credentials — all enforced before any handler or git-service contact.

---

## Phase 5: User Story 3 — Push Policy Enforcement (Priority: P2)

**Goal**: Every receive-pack stream carries a `PushContext` on its first chunk. The git-service rejects streams without it, enforces pack/blob size limits, and forwards policy to the hook pipeline.

**Independent Test**: Set `MaxFileSizeBytes = 1048576` on a repo, push a commit with a 2 MB file, confirm rejection with a policy-limit error message and no objects written to disk.

### Tests for User Story 3

> **Write these tests FIRST — verify they FAIL before implementation**

- [x] T031 [P] [US3] Write Rust test `test_receive_pack_rejects_missing_push_context` in `gitstore-git-service/src/grpc/server.rs` (or a test module): send first chunk without `push_context`; assert stream is rejected before any ref command is processed
- [x] T032 [P] [US3] Write Rust test `test_pack_size_limit_enforced`: configure `max_pack_size_bytes = 1`; push a pack exceeding the limit; assert rejection with no objects written
- [x] T033 [P] [US3] Write Rust test `test_file_size_limit_enforced`: configure `max_file_size_bytes = 1`; push a commit containing a larger blob; assert rejection (also implements T042)
- [x] T034 [P] [US3] Write Rust test `test_zero_limits_mean_unlimited`: configure both limits to 0; push a large pack; assert it succeeds
- [x] T035 [P] [US3] Write `TestPushContextInserter` in `gitstore-api/internal/middleware/security/secure_test.go`: valid `repoID` + principal in context; assert `PushContext` proto is stored in request context with correct `repository_id`, `actor.subject`, and policy fields
- [x] T036 [US3] Write `TestReceivePackAttachesPushContext` in `gitstore-api/internal/githttp/handler_test.go`: assert that the first `ReceivePackRequest` chunk sent to the mock git-service contains a non-nil `push_context`

### Implementation for User Story 3

- [x] T037 [US3] Implement `PushContextInserter` method on `security.Authorize` in `gitstore-api/internal/middleware/security/secure.go`: read `repoID` via `c.Get(repoIDKey)` and `Principal` from context; call `store.GetRepository(ctx, repoID)` to fetch policy fields; build `gitv1.PushContext` proto with actor and policy; store in request context via `context.WithValue` (escapes gin into `ReceivePack` gRPC call)
- [x] T038 [US3] Wire `PushContextInserter` into `githttp.NewMux` as route-level middleware on the `POST /:namespace/:repo/git-receive-pack` route only in `gitstore-api/internal/githttp/handler.go`
- [x] T039 [US3] Extend `gitclient.ReceivePack` in `gitstore-api/internal/gitclient/grpc_client.go`: read `PushContext` from `context.Value`; attach to the first `ReceivePackRequest` chunk as field `PushContext`
- [x] T040 [US3] Extend `receive_pack` in `gitstore-git-service/src/grpc/server.rs`: reject stream with gRPC `InvalidArgument` if first chunk has no `push_context`; reject stream if `push_context.repository_id != chunk.repository_id` (FR-011)
- [x] T041 [US3] Implement pack size enforcement in `gitstore-git-service/src/grpc/server.rs`: maintain running byte counter across chunks; if `push_context.policy.max_pack_size_bytes > 0` and counter exceeds limit, abort stream with descriptive error before writing any objects
- [x] T042 [US3] Implement blob size enforcement in `gitstore-git-service/src/git/pack_server.rs` (or `server.rs`): during pack indexing, check each blob size against `max_file_size_bytes`; if `> 0` and exceeded, reject before promoting quarantine

**Checkpoint**: receive-pack streams without `push_context` are rejected; pack and blob limits are enforced; zero = unlimited.

---

## Phase 6: User Story 4 — Hook Pipeline Typed Actor Context (Priority: P2)

**Goal**: Every hook pipeline stage receives a typed `HookContext` carrying actor identity and push policy. No stage reads auth or policy state from environment variables. Admission log entries include the actor subject.

**Independent Test**: Perform an authenticated push; inspect admission handler log output and confirm `actor_subject` field appears. Grep codebase confirms no `std::env::var` for auth/policy in hook stages.

### Tests for User Story 4

> **Write these tests FIRST — verify they FAIL before implementation**

- [x] T043 [P] [US4] Write Rust test `test_hook_context_actor_subject_logged` in `gitstore-git-service/src/git/hooks/admission_handler.rs` (test module): call admission handler with a `HookContext` carrying `actor_subject = "test-user"`; assert log output contains `"test-user"`
- [x] T044 [P] [US4] Write Rust test `test_hook_context_propagated_to_validation` in `gitstore-git-service/src/git/hooks/validation_handler.rs` (test module): call validation handler with a `HookContext`; assert it receives the context without panicking
- [x] T045 [US4] Write Rust test `test_inconsistent_repo_id_rejected` in `gitstore-git-service/src/grpc/server.rs`: send first chunk with `push_context.repository_id != chunk.repository_id`; assert stream rejected before any ref command is processed

### Implementation for User Story 4

- [x] T046 [US4] Add `HookContext` struct to `gitstore-git-service/src/git/hooks/mod.rs` with fields `actor_subject: String`, `actor_auth_method: String`, `max_pack_size_bytes: i64`, `max_file_size_bytes: i64`, `config_resource_version: String`; derive `Clone, Debug`
- [x] T047 [US4] Add `From<&PushContext>` implementation for `HookContext` in `gitstore-git-service/src/git/hooks/mod.rs` (or `server.rs`): map proto fields to struct fields at stream-open, after `push_context` is validated
- [x] T048 [US4] Update `ValidationHandler` trait signature in `gitstore-git-service/src/git/hooks/mod.rs` to accept `&HookContext` parameter; update `NoopValidationHandler` and `SchemaValidationHandler` accordingly
- [x] T049 [US4] Update `AdmissionHandler` trait signature in `gitstore-git-service/src/git/hooks/mod.rs` to accept `&HookContext` parameter; update `NoopAdmissionHandler` and `AdmissionControlHandler` accordingly
- [x] T050 [US4] Update `HookPipeline` run methods in `gitstore-git-service/src/git/hooks/mod.rs` to accept and pass `&HookContext` to each stage
- [x] T051 [US4] Update `receive_pack` in `gitstore-git-service/src/grpc/server.rs` to build `HookContext` from validated `PushContext` and pass to all `HookPipeline` calls
- [x] T052 [US4] Update `AdmissionControlHandler::run` in `gitstore-git-service/src/git/hooks/admission_handler.rs` to log `actor_subject` from `HookContext` on every admission decision (using `tracing::info!`)
- [x] T053 [US4] Audit `admission_handler.rs` and `validation_handler.rs` for any `std::env::var` calls reading auth or policy state; remove all such reads (FR-010)

**Checkpoint**: All hook stages receive typed `HookContext`; admission logs include actor subject; no env-var reads for auth/policy in hook stages.

---

## Phase 7: Polish & Cross-Cutting Concerns

**Purpose**: Observability, docs, and validation across all stories.

- [x] T054 [P] Update `docs/implementation/pluggable_auth_architecture.md` to document the Git smart-HTTP auth flow: middleware chain order, `PushContext` propagation, and hook context
- [x] T055 [P] Run `make pr-ready` from repo root and fix any lint or test failures
- [ ] T056 [P] Validate quickstart scenarios from `specs/035-git-http-auth/quickstart.md`: authenticated clone/push, unauthenticated 401, 403 on read-only principal, policy rejection, `/metrics` counter verification (requires running stack — validate manually)
- [x] T057 Update `.github/workflows/ci-integration.yml` if any new test tags or services are needed for the Phase 5–6 Rust tests

---

## Dependencies & Execution Order

### Phase Dependencies

- **Phase 1 (Setup)**: No dependencies — start immediately. T005–T007 are parallelisable with T001–T004.
- **Phase 2 (Foundational)**: Depends on Phase 1 completion. **Blocks all user story phases.**
- **Phase 3 (US1 — AuthN)**: Depends on Phase 2. No dependency on US2, US3, or US4.
- **Phase 4 (US2 — AuthZ)**: Depends on Phase 2. No dependency on US3 or US4. Integrates with US1 middleware chain.
- **Phase 5 (US3 — Push Policy)**: Depends on Phase 2 + Phase 4 (needs `RepoResolver` in place for `c.Get(repoIDKey)`).
- **Phase 6 (US4 — Hook Context)**: Depends on Phase 5 (`PushContext` must be on the wire before hooks can receive it).
- **Phase 7 (Polish)**: Depends on all desired stories being complete.

### User Story Dependencies

| Story | Depends On | Can Parallelise With |
|-------|-----------|---------------------|
| US1 (AuthN) | Phase 2 | US2 (different files) |
| US2 (AuthZ) | Phase 2 | US1 (different files) |
| US3 (Push Policy) | US2 complete | — |
| US4 (Hook Context) | US3 complete | — |

### Within Each Story

1. Tests written and **FAIL** confirmed
2. Implementation tasks
3. Checkpoint validation before moving to next story

---

## Parallel Execution Examples

### Phase 3 + Phase 4 (US1 + US2 — both P1)

```
Parallel after Phase 2 checkpoint:
  Thread A: T011 → T012 → T013 → T014 → T015 → T016 → T017 → T018 → T019 → T020
  Thread B: T021 → T022 → T023 → T024 → T025 → T026 → T027 → T028 → T029 → T030
```

### Within Phase 3 (US1 tests are parallelisable)

```
Parallel:  T011, T012, T013, T014
Then sequential: T015 (integration, needs server) → T016 → T017 → T018 → T019 → T020
```

### Within Phase 5 (US3 Rust tests are parallelisable)

```
Parallel:  T031, T032, T033, T034, T035, T036
Then sequential: T037 → T038 → T039 → T040 → T041 → T042
```

---

## Implementation Strategy

### MVP First (P1 Stories — US1 + US2 Only)

1. Complete Phase 1: Setup
2. Complete Phase 2: Foundational
3. Complete Phase 3: US1 — AuthN Gate
4. Complete Phase 4: US2 — AuthZ Gate
5. **STOP and VALIDATE**: authenticated push succeeds; unauthenticated push returns 401; read-only principal returns 403; `/metrics` shows counters
6. Deploy/demo as MVP

### Incremental Delivery

1. Setup + Foundational → proto and entity foundation ready
2. US1 → AuthN gate closed; metrics visible → demo
3. US2 → AuthZ enforcement live → demo
4. US3 → Push policy enforced end-to-end → demo
5. US4 → Actor attribution in admission logs → demo

---

## Notes

- `[P]` tasks = different files, no shared state — safe to run concurrently
- `[US1]`–`[US4]` labels map to user stories in spec.md
- `repoID` is propagated via `c.Set`/`c.Get` (gin-internal); `Principal` and `PushContext` use `context.WithValue` (escape gin into gRPC/datastore)
- `PushContextInserter` uses `context.WithValue` (not `c.Set`) because the value must survive past gin into `gitclient.ReceivePack`
- Constitution Principle I: every test task must be confirmed FAILING before its paired implementation task begins
