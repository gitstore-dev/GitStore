# Tasks: Controller Manager Runtime Foundations

**Input**: Design documents from `/specs/025-controller-manager-runtime/`
**Prerequisites**: plan.md ✅, spec.md ✅, research.md ✅, data-model.md ✅, contracts/ ✅

**Tests**: Test-First Development (Constitution Principle I - NON-NEGOTIABLE). Tests MUST be written before implementation.

**Organization**: Tasks are grouped by user story to enable independent implementation and testing of each story.

## Format: `[ID] [P?] [Story] Description`

- **[P]**: Can run in parallel (different files, no dependencies)
- **[Story]**: Which user story this task belongs to (US1–US4)
- Include exact file paths in descriptions

---

## Phase 1: Setup (Shared Infrastructure)

**Purpose**: Initialize `gitstore-controller-manager` module structure, dependencies, and CI/CD.

- [x] T001 Add 4 new dependencies to `gitstore-controller-manager/go.mod`: `golang.org/x/time v0.15.0`, `github.com/alitto/pond/v2 v2.7.1`, `github.com/cenkalti/backoff/v5 v5.0.3`, `github.com/prometheus/client_golang v1.23.2`
- [x] T002 [P] Create directory structure per plan.md: `gitstore-controller-manager/internal/{manager,queue,worker,retry,cache,health,api}/` and `gitstore-controller-manager/tests/{contract,integration}/`
- [x] T003 [P] Create `docker/controller-manager.Dockerfile` — multi-stage Go build matching `docker/api.Dockerfile` pattern; `EXPOSE 5001`; `ENV GITSTORE_CONTROLLER__PORT=5001`
- [x] T004 [P] Add `CONTROLLER_IMAGE` env var and `build-controller-manager-image` job to `.github/workflows/cd.yml` following the existing `build-api-image` job pattern; add job to `needs` list of `deploy-staging` and `deploy-production`
- [x] T005 [P] Add `make controller` target to root `Makefile` to run `gitstore-controller-manager` locally (matching `make api` pattern)

**Checkpoint**: Module structure exists, Dockerfile compiles, CD job wired.

---

## Phase 2: Foundational (Blocking Prerequisites)

**Purpose**: Core types, interfaces, and structured logging that every user story depends on.

**⚠️ CRITICAL**: No user story work can begin until this phase is complete.

- [x] T006 Define shared types in `gitstore-controller-manager/internal/manager/types.go`: `WorkItemKey`, `Result`, `Reconciler` interface, `ReconcilerRegistration` struct — exact shapes from `data-model.md`
- [x] T007 [P] Set up structured logging (`go.uber.org/zap`) in `gitstore-controller-manager/internal/manager/logger.go` — consistent with `gitstore-api` logger setup
- [x] T008 [P] Create `gitstore-controller-manager/internal/manager/errors.go`: define sentinel errors `ErrNotFound`, `ErrQueueShutdown`, `ErrKindNotRegistered`
- [x] T009 Implement config struct in `gitstore-controller-manager/internal/config/config.go`: `GITSTORE_CONTROLLER__PORT`, `GITSTORE_CONTROLLER__API_URI`, `GITSTORE_CONTROLLER__DEFAULT_MAX_ATTEMPTS`, `GITSTORE_CONTROLLER__DEFAULT_STALL_THRESHOLD` — use `viper` consistent with `gitstore-api/internal/config`

**Checkpoint**: Foundational types, interfaces, logging, and config compile; user story phases can now proceed.

---

## Phase 3: User Story 1 — Reconciler Registration and Dispatch (Priority: P1) 🎯 MVP

**Goal**: Controller authors can register a `Reconciler` for a resource kind; the manager routes enqueued `WorkItemKey`s exclusively to the matching reconciler, deduplicated, one dispatch at a time.

**Independent Test**: Register a no-op reconciler for kind `"Widget"`, enqueue the same key 5 times, assert the reconciler is invoked exactly once per quiescent moment; assert a key enqueued for an unregistered kind returns `ErrKindNotRegistered`.

### Tests for User Story 1 ⚠️ Write first — must FAIL before implementation

- [x] T010 [P] [US1] Write contract test in `gitstore-controller-manager/tests/contract/reconciler_contract_test.go`: verify `Reconciler` interface is callable with `WorkItemKey`; verify `Queue` interface dedup invariant (5 `Enqueue` calls → 1 `Dequeue`); verify `Done` re-enqueues dirty keys exactly once
- [x] T011 [P] [US1] Write integration test in `gitstore-controller-manager/tests/integration/manager_dispatch_test.go`: start manager, register no-op reconciler for `"Widget"`, enqueue key 5 times, assert handler called once; assert unregistered kind returns `ErrKindNotRegistered`

### Implementation for User Story 1

- [x] T012 [P] [US1] Implement `gitstore-controller-manager/internal/queue/queue.go`: typed generic dedup queue with `dirty`/`processing`/`queue` three-set pattern + `golang.org/x/time/rate` token-bucket admission; implements `Queue` interface from `data-model.md`
- [x] T013 [P] [US1] Implement `gitstore-controller-manager/internal/queue/queue_test.go`: unit tests for dedup semantics, rate limiting, `ShutDown` behaviour
- [x] T014 [P] [US1] Implement `gitstore-controller-manager/internal/worker/pool.go`: per-kind `pond/v2` pool wrapper; `Submit(fn)`, `Resize(n)`, `ActiveWorkers() int64`, `WaitingTasks() uint64`, `Stop()`
- [x] T015 [P] [US1] Implement `gitstore-controller-manager/internal/worker/pool_test.go`: unit tests for bounded concurrency, `Resize`, graceful stop
- [x] T016 [US1] Implement `gitstore-controller-manager/internal/manager/manager.go`: `Manager` struct with `Register(ReconcilerRegistration)`, `Start(ctx)`, `Stop()`, dispatch loop wiring queue → worker pool → reconciler; emit structured log on each dispatch
- [x] T017 [US1] Implement `gitstore-controller-manager/cmd/controller/main.go`: wire `Config`, `Manager`, HTTP server skeleton; graceful shutdown on `SIGTERM`/`SIGINT`
- [x] T018 [US1] Verify T010 and T011 tests pass

**Checkpoint**: `make controller` starts; register a reconciler, enqueue a key, observe dispatch in logs. US1 independently functional.

---

## Phase 4: User Story 2 — Retry / Backoff / Poison-Item Quarantine (Priority: P2)

**Goal**: Failed reconcile calls are retried with exponential backoff; items exceeding `MaxAttempts` are quarantined as poison items without blocking other kinds; operators can list and re-queue poison items via HTTP.

**Independent Test**: Register a reconciler that always fails for key `"poison-widget"`. Assert item appears in quarantine after `MaxAttempts`. Assert other keys for the same kind process normally. Re-queue via `POST /controller/v1/poison/Widget/ns/poison-widget/requeue`; assert item re-enters the active queue with attempt count reset to 0.

### Tests for User Story 2 ⚠️ Write first — must FAIL before implementation

- [x] T019 [P] [US2] Write contract test in `gitstore-controller-manager/tests/contract/quarantine_contract_test.go`: verify `QuarantineStore` interface — `Put`, `Get`, `Delete`, `List`, `Len`; verify `RetryRecord` and `PoisonItem` structs match `data-model.md`
- [x] T020 [P] [US2] Write integration test in `gitstore-controller-manager/tests/integration/retry_quarantine_test.go`: register always-failing reconciler; assert item quarantined after `MaxAttempts`; assert retry signals emitted (FR-004); assert other items unaffected (SC-002); assert requeue resets attempt count

### Implementation for User Story 2

- [x] T021 [P] [US2] Implement `gitstore-controller-manager/internal/retry/retry.go`: `cenkalti/backoff/v5` retry loop; `RetryNotify` emits structured log + observable signal per attempt; `backoff.Permanent(err)` short-circuits to quarantine; populates `RetryRecord`
- [x] T022 [P] [US2] Implement `gitstore-controller-manager/internal/retry/quarantine.go`: `QuarantineStore` — `sync.RWMutex`-protected `map[WorkItemKey]*PoisonItem`; thread-safe `Put/Get/Delete/List/Len`
- [x] T023 [P] [US2] Implement `gitstore-controller-manager/internal/retry/retry_test.go`: unit tests for backoff delays, `Permanent` short-circuit, quarantine after max attempts
- [x] T024 [US2] Wire retry engine into `gitstore-controller-manager/internal/manager/manager.go` dispatch loop: wrap reconciler call with retry; on quarantine move item to `QuarantineStore`; emit `gitstore_controller_reconcile_total{result="poison"}` counter
- [x] T025 [P] [US2] Implement `gitstore-controller-manager/internal/api/poison.go`: `GET /controller/v1/poison/{kind}` (list quarantined items) and `POST /controller/v1/poison/{kind}/{namespace}/{name}/requeue` (re-queue with reset budget); JSON error format from `contracts/http-api.md`
- [x] T026 [US2] Register poison HTTP handlers in `gitstore-controller-manager/cmd/controller/main.go`
- [x] T027 [US2] Verify T019 and T020 tests pass

**Checkpoint**: Inject a broken reconciler; confirm quarantine after retries; confirm `curl .../requeue` re-processes item. US2 independently functional.

---

## Phase 5: User Story 3 — Level-Triggered Informer Cache (Priority: P3)

**Goal**: Reconcilers always read current resource state from an in-memory informer cache populated from the Watch stream, never from the original event payload; re-delivered items produce the same outcome as first-time deliveries.

**Independent Test**: Populate cache for key `"Product/ns/widget"`. Enqueue the key. In the reconciler, assert `cache.Get(key)` returns the cached object (no live API call). Update the cache for the same key. Enqueue again. Assert the reconciler observes the updated state.

### Tests for User Story 3 ⚠️ Write first — must FAIL before implementation

- [x] T028 [P] [US3] Write contract test in `gitstore-controller-manager/tests/contract/cache_contract_test.go`: verify `InformerCache[T]` interface — `Set/Get/Delete/List/HasSynced`; verify event handler callbacks fire after `Set`/`Delete`; verify `HasSynced()` returns false until explicitly set
- [x] T029 [P] [US3] Write integration test in `gitstore-controller-manager/tests/integration/level_triggered_test.go`: populate cache; enqueue key; assert reconciler reads from cache; update cache; enqueue same key; assert reconciler reads updated state; assert no API call made when cache hits

### Implementation for User Story 3

- [x] T030 [P] [US3] Implement `gitstore-controller-manager/internal/cache/cache.go`: generic `InformerCache[T]` with `sync.RWMutex` map; `AddEventHandler(EventHandler[T])`; `HasSynced()` flag; callbacks fire after cache mutation
- [x] T031 [P] [US3] Implement `gitstore-controller-manager/internal/cache/cache_test.go`: unit tests for concurrent `Set/Get`, event handler invocation order, `HasSynced` lifecycle
- [x] T032 [US3] Expose `Cache(kind string) InformerCache[T]` on `Manager` in `gitstore-controller-manager/internal/manager/manager.go`; wire `HasSynced` check before dispatch loop starts (reconcile loop blocked until cache synced)
- [x] T033 [US3] Verify T028 and T029 tests pass

**Checkpoint**: Reconciler reads from cache; replay produces correct state. US3 independently functional.

---

## Phase 6: User Story 4 — Health Surface (Priority: P4)

**Goal**: Operators can query `/health` (JSON) and `/metrics` (Prometheus) to see per-kind worker count, queue depth, poison item count, last-reconcile timestamp, and stall status; stalled workers are flagged distinctly.

**Independent Test**: Start manager with one registered kind, enqueue items, query `/health`; assert all six fields present for the kind. Simulate stall (no reconcile for > stall threshold); assert `stalled: true` in next health query.

### Tests for User Story 4 ⚠️ Write first — must FAIL before implementation

- [x] T034 [P] [US4] Write integration test in `gitstore-controller-manager/tests/integration/health_test.go`: start manager; query `/health`; assert JSON fields match `contracts/http-api.md`; query `/metrics`; assert all `gitstore_controller_*` gauge names present; simulate stall; assert `stalled: true`

### Implementation for User Story 4

- [x] T035 [P] [US4] Implement `gitstore-controller-manager/internal/health/metrics.go`: register all 6 Prometheus `GaugeVec`/`CounterVec` metrics from `data-model.md` (`active_workers`, `queue_depth`, `poison_items_total`, `last_reconcile_timestamp`, `stalled_workers`, `reconcile_total{result}`)
- [x] T036 [P] [US4] Implement `gitstore-controller-manager/internal/health/stall.go`: background goroutine per kind; compares `time.Since(lastReconcile)` against `StallThreshold`; sets `stalled_workers{kind}` gauge; ticks at `StallThreshold/2`
- [x] T037 [P] [US4] Implement `gitstore-controller-manager/internal/health/handler.go`: `GET /health` — JSON response per `contracts/http-api.md`; aggregates per-kind metrics from Prometheus collectors; top-level `status` is `"degraded"` if any kind stalled or has poison items
- [x] T038 [US4] Register `/health` and `/metrics` endpoints in `gitstore-controller-manager/cmd/controller/main.go`; wire stall detector goroutines on `Start()`; update per-kind gauges from pool metrics on each reconcile completion
- [x] T039 [US4] Verify T034 test passes

**Checkpoint**: `curl localhost:5001/health` returns per-kind JSON. `curl localhost:5001/metrics` returns Prometheus scrape. US4 independently functional.

---

## Phase 7: Polish & Cross-Cutting Concerns

**Purpose**: Integration wiring, documentation, and operational readiness.

- [x] T040 [P] Update `AGENTS.md` (`Active Technologies` and `Commands` sections): add `gitstore-controller-manager` module, 4 new dependencies, `make controller` command, port 5001, config var naming conventions
- [x] T041 [P] Update `compose.yml`: add `controller-manager` service entry with `GITSTORE_CONTROLLER__PORT=5001`, `GITSTORE_CONTROLLER__API_URI=http://api:4000/graphql`; expose port 5001; add to `deploy-staging`/`deploy-production` needs in `cd.yml`
- [x] T042 [P] Add `gitstore-controller-manager` to root `Makefile` `build`, `test`, `lint`, and `pr-ready` aggregate targets
- [x] T043 [P] Write `gitstore-controller-manager/.env.example` with all config variables and comments (matching `gitstore-api/.env.example` style)
- [x] T044 Run `make pr-ready` and fix any lint/license/test failures

**Checkpoint**: `make pr-ready` passes. All 4 user stories functional end-to-end.

---

## Dependencies & Execution Order

### Phase Dependencies

- **Setup (Phase 1)**: No dependencies — can start immediately; T003/T004/T005 are parallel
- **Foundational (Phase 2)**: Depends on Phase 1 — BLOCKS all user stories
- **US1 (Phase 3)**: Depends on Phase 2 — no dependency on US2/3/4
- **US2 (Phase 4)**: Depends on Phase 2 + US1 (retry wires into manager dispatch loop)
- **US3 (Phase 5)**: Depends on Phase 2 — no dependency on US1/2 (cache is standalone)
- **US4 (Phase 6)**: Depends on Phase 2 — benefits from US1/2 wired but independently testable
- **Polish (Phase 7)**: Depends on all desired user stories complete

### User Story Dependencies

- **US1 (P1)**: Unblocked after Foundational — pure MVP
- **US2 (P2)**: Requires US1 (wraps reconciler call in dispatch loop)
- **US3 (P3)**: Unblocked after Foundational — independent of US1/US2
- **US4 (P4)**: Unblocked after Foundational — metrics wiring improves with US1/US2 wired but testable alone

### Parallel Opportunities

Within each phase, all tasks marked `[P]` can run concurrently.

After Foundational completes, US1 and US3 can start in parallel (different files, no shared dependency between them).

---

## Parallel Example: User Story 1

```bash
# Start these together (different files, no dependencies):
Task T010: "Contract test: reconciler + queue dedup"
Task T011: "Integration test: manager dispatch"
Task T012: "Implement queue.go"
Task T014: "Implement worker/pool.go"

# Then sequentially (T016 depends on T012 + T014):
Task T016: "Implement manager.go dispatch loop"
Task T017: "Wire cmd/controller/main.go"
Task T018: "Verify T010 + T011 pass"
```

---

## Implementation Strategy

### MVP First (User Story 1 Only)

1. Complete Phase 1: Setup (T001–T005)
2. Complete Phase 2: Foundational (T006–T009)
3. Complete Phase 3: US1 (T010–T018)
4. **STOP and VALIDATE**: `make controller` starts; reconciler dispatched; dedup confirmed
5. Deploy or demo if ready — core queue+dispatch is functional

### Incremental Delivery

1. Setup + Foundational → module compiles
2. US1 → queue + dispatch working (MVP)
3. US2 → retry + quarantine + re-queue API (resilience)
4. US3 → level-triggered cache (correctness under replay)
5. US4 → health surface + Prometheus (operational readiness)

### Parallel Team Strategy

After Phase 2 completes:
- Developer A: US1 (queue + dispatch)
- Developer B: US3 (informer cache — fully independent)
- Developer C: US4 (health metrics skeleton — fully independent)
- Developer A continues to US2 after US1 merges

---

## Notes

- `[P]` tasks touch different files and have no cross-task dependencies within the same phase
- Constitution Principle I is non-negotiable: T010/T011 must fail before T012–T017 begin
- `WorkItemKey` is the only thing the queue and reconciler exchange — never event payloads (level-triggered)
- `docker/controller-manager.Dockerfile` follows the same multi-stage Go pattern as `docker/api.Dockerfile`
- CD job name: `build-controller-manager-image`; image env var: `CONTROLLER_MANAGER_IMAGE`
