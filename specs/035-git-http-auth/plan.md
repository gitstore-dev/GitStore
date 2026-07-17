# Implementation Plan: Git Smart-HTTP Authentication

**Branch**: `035-git-http-auth` | **Date**: 2026-07-12 | **Spec**: [spec.md](spec.md)  
**Input**: Feature specification from `specs/035-git-http-auth/spec.md`

## Summary

Gate all Git smart-HTTP traffic through the pluggable AuthN/AuthZ framework and enforce per-push policies end-to-end. The API layer adds a `RepoResolver` middleware (single datastore lookup per request), `BasicAuthenticator` (already wired, needs 503/401 split and metrics), `GitHttpAuthorizer` (authz check), and `PushContextInserter` (receive-pack only). The git-service adds `PushContext` / `PushPolicy` / `AuthContext` proto messages, first-chunk validation, pack/blob size enforcement, and typed `HookContext` propagation through the hook pipeline.

## Technical Context

**Language/Version**: Go 1.25 (gitstore-api) · Rust 1.x (gitstore-git-service)  
**Primary Dependencies**: `github.com/gin-gonic/gin`, `go-grpc-prometheus`, `prometheus/client_golang`, `gix 0.84.0`, `tonic 0.14`  
**Storage**: Push policy fields added to `datastore.Repository` struct; resolved via existing `store.GetRepository` after `LookupRepository`  
**Testing**: `go test ./...` (Go) · `cargo test` (Rust) · integration tests in `test/integration`  
**Target Platform**: Linux server (Docker Compose + native)  
**Project Type**: Multi-service web API + gRPC backend  
**Performance Goals**: Git push validation < 5 s for 100-file push (existing constitution target); auth middleware overhead < 5 ms p95  
**Constraints**: Zero regression to GraphQL auth chain; no new external dependencies  
**Scale/Scope**: Single-tenant initially; all limits enforced per-repository

## Constitution Check

*GATE: Must pass before Phase 0 research. Re-check after Phase 1 design.*

| Principle                | Status | Notes                                                                                                             |
|--------------------------|--------|-------------------------------------------------------------------------------------------------------------------|
| I. Test-First            | PASS   | All tasks below specify test task before implementation task                                                      |
| II. API-First            | PASS   | Proto contract (`contracts/git_service.proto`) defined in Phase 1 before any implementation                       |
| III. Clear Contracts     | PASS   | Additive proto changes only; no field removals; field 4 used for `push_context`                                   |
| IV. Observability        | PASS   | `gitstore_git_http_auth_requests_total` counter + `/metrics` endpoint; structured zap logging for all auth events |
| V. User Story Driven     | PASS   | All tasks labelled US1–US4                                                                                        |
| VI. Incremental Delivery | PASS   | P1 (AuthN/AuthZ gate) independently deployable before P2 (push policy)                                            |
| VII. Simplicity          | PASS   | No new packages, no new external deps; `context.WithValue` pattern matches existing auth code                     |

*Post-design re-check*: No violations introduced. `RepoResolver` eliminates duplicate lookups (simpler, not more complex).

## Project Structure

### Documentation (this feature)

```text
specs/035-git-http-auth/
├── plan.md              # This file
├── research.md          # Phase 0 output
├── data-model.md        # Phase 1 output
├── quickstart.md        # Phase 1 output
├── contracts/
│   └── git_service.proto  # Updated proto contract (additive)
└── tasks.md             # Phase 2 output (/speckit.tasks)
```

### Source Code

```text
gitstore-api/
├── internal/
│   ├── githttp/
│   │   ├── handler.go              # extend: read repoID from context; wire new middleware in NewMux
│   │   ├── handler_test.go         # extend: auth + authz test cases
│   │   └── resolver.go             # NEW: RepoResolver middleware + repoIDContextKey
│   ├── middleware/security/
│   │   ├── secure.go               # extend: basicAuth 503/401 split; GitHttpAuthorizer impl; PushContextInserter; metrics
│   │   └── secure_test.go          # extend: 503 transient test; 403 authz test; push context test
│   ├── datastore/
│   │   └── entities.go             # extend: add push policy fields to Repository struct
│   └── health/
│       └── health.go               # extend: add Metrics gin handler (promhttp.Handler)
├── gen/gitstore/git/v1/            # regenerated from proto
└── cmd/server/
    └── main.go                     # extend: call gitclient.RegisterClientMetrics

shared/proto/gitstore/git/v1/
└── git_service.proto               # extend: add PushContext, AuthContext, PushPolicy, push_context field

gitstore-git-service/src/
├── grpc/
│   └── server.rs                   # extend: validate push_context on first chunk; enforce pack/blob limits
└── git/hooks/
    ├── mod.rs                      # extend: add HookContext struct; propagate to pipeline stages
    ├── admission_handler.rs        # extend: accept HookContext; log actor subject
    └── validation_handler.rs       # extend: accept HookContext; log actor subject
```

## Complexity Tracking

No constitution violations — no entries required.

## Implementation Phases

### Foundational (blocks all stories)

**F-1 [TEST]** Write proto contract: add `PushContext`, `AuthContext`, `PushPolicy` messages to `gitstore/git/v1`; add `push_context` field 4 to `ReceivePackRequest`. Regenerate Go + Rust bindings. Verify compilation only (no behaviour yet).

**F-2 [TEST]** Add push policy fields to `datastore.Repository` (`MaxPackSizeBytes int64`, `MaxFileSizeBytes int64`). Update memdb schema and scylla DDL. Write unit test verifying zero values round-trip.

**F-3 [IMPL]** Implement proto changes and regenerate. Implement datastore entity changes.

### User Story 1 — AuthN Gate (P1)

**US1-1 [TEST]** Write `handler_test.go` cases: unauthenticated request → 401 with `WWW-Authenticate`; valid credentials → pass-through; transient provider error → 503.

**US1-2 [TEST]** Write `secure_test.go` case: `basicAuth` with `err != nil` returns 503; `OutcomeDeny` with `err == nil` returns 401.

**US1-3 [IMPL]** Fix `basicAuth` in `secure.go`: split `err != nil` (503) from `OutcomeDeny` (401). Add `gitstore_git_http_auth_requests_total` `CounterVec` with labels `outcome`, `service`; register on injected `prometheus.Registerer`. Increment on allow/deny/error. Call `gitclient.RegisterClientMetrics(prometheus.DefaultRegisterer)` in `NewServer`. Add `Metrics` handler to `health.Handler`; register `GET /metrics` in `server.go`.

**US1-4 [TEST]** Integration test: after allow + deny, query `/metrics` and assert counter values are non-zero.

### User Story 2 — AuthZ Gate (P1)

**US2-1 [TEST]** Write `handler_test.go` cases: principal with only `repository.read` + receive-pack → 403; principal with `repository.write` + receive-pack → pass-through; missing repo → 404.

**US2-2 [TEST]** Write `secure_test.go` for `GitHttpAuthorizer`: repo not in context → 500 (middleware misconfiguration guard); read principal denied → 403.

**US2-3 [IMPL]** Implement `RepoResolver` middleware in `githttp/resolver.go`: reads `:namespace`, `:repo` params, calls `store.GetNamespaceByIdentifier` then `store.LookupRepository`, stores `repoID` via `c.Set("repoID", repoID)`, aborts with 404 on miss. Remove per-handler `resolveRepo` calls. (`c.Set` is correct here — `repoID` is consumed only within the gin chain; `context.WithValue` is reserved for values that must escape gin, such as `Principal`.)

**US2-4 [IMPL]** Implement `GitHttpAuthorizer` in `secure.go`: reads `repoID` from context, reads `Principal` from context, calls `registry.AuthZ().Authorize(ctx, principal, action, resource)` where action is `repository.read` (upload-pack) or `repository.write` (receive-pack), aborts with 403 on deny. Wire `RepoResolver` + `GitHttpAuthorizer` into `githttp.NewMux` after `BasicAuthenticator`.

### User Story 3 — Push Policy (P2)

**US3-1 [TEST]** Write git-service test: `receive_pack` stream missing first-chunk `push_context` → stream rejected before any ref command processed.

**US3-2 [TEST]** Write git-service test: push with pack exceeding `max_pack_size_bytes` → rejected; push with blob exceeding `max_file_size_bytes` → rejected; zero values → no rejection.

**US3-3 [TEST]** Write `secure_test.go` for `PushContextInserter`: valid repo + principal → `PushContext` stored in context with correct fields; missing repo → middleware should not reach (RepoResolver already 404'd).

**US3-4 [IMPL]** Implement `PushContextInserter` in `secure.go`: reads `repoID` + `Principal` from context, calls `store.GetRepository` to read push policy fields, builds `PushContext` proto, stores via `context.WithValue`. Wire into `githttp.NewMux` on the receive-pack route only (route-level middleware).

**US3-5 [IMPL]** Extend `gitclient.ReceivePack`: read `PushContext` from context; attach to first `ReceivePackRequest` chunk as field 4.

**US3-6 [IMPL]** Extend git-service `receive_pack` in `server.rs`: reject stream if first chunk has no `push_context`; enforce `max_pack_size_bytes` (running byte counter across chunks) and `max_file_size_bytes` (per-blob check during pack indexing); zero = unlimited.

### User Story 4 — Hook Context (P2)

**US4-1 [TEST]** Write Rust test: hook pipeline receives `HookContext` with actor subject from push context; admission log entries include actor subject; no env-var reads in hook stages.

**US4-2 [IMPL]** Add `HookContext` struct to `git/hooks/mod.rs`: fields `actor_subject: String`, `policy: PushPolicy`. Derive from `PushContext` at stream-open. Thread through `HookPipeline::run_pre_receive`, `run_post_receive`, `ValidationHandler::run`, `AdmissionHandler::run`. Update admission and validation handlers to log `actor_subject`.

**US4-3 [IMPL]** Validate repo ID consistency: if `push_context.repository_id` ≠ stream's `repository_id` → reject. Implement in `receive_pack` before any pack processing.
