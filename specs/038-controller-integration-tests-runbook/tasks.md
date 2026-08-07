# Tasks: Controller Integration Tests + Operations Runbook

**Input**: Design documents from `/specs/038-controller-integration-tests-runbook/`
**Prerequisites**: plan.md, spec.md, research.md, data-model.md, contracts/, quickstart.md

**Tests**: This feature *is* test authorship (Constitution Principle I). Every functional requirement maps to one or more test tasks below; there is no separate "implementation" layer to test against beyond the minimal instrumentation fixes an audit (US5) might uncover.

**Organization**: Tasks are grouped by user story (from spec.md) to enable independent implementation and testing of each story.

## Format: `[ID] [P?] [Story] Description`

- **[P]**: Can run in parallel (different files/functions, no dependencies)
- **[Story]**: Which user story this task belongs to (US1–US5)
- All file paths are relative to the repository root

## Path Conventions

Single Go module (`gitstore-controller-manager`), per plan.md's Project Structure:
- New test package: `gitstore-controller-manager/tests/integration/`
- New docs: `docs/runbooks/`

---

## Phase 1: Setup

**Purpose**: Establish the new test package and documentation entry points

- [X] T001 Create `gitstore-controller-manager/tests/integration/doc_test.go` with a `package integration_test` doc comment describing the package's purpose (end-to-end scenarios wiring real `manager.Manager` + `listwatch.Runner` + `checkpoint.Store`, per plan.md's Structure Decision). **Deviation from original task text**: named `doc_test.go`, not `doc.go` — a non-`_test.go` file cannot declare the `_test`-suffixed package name `integration_test` (Go only permits `_test`-suffixed packages in files ending `_test.go`); this surfaced as a `go vet` failure only once a second file was added, not immediately, so the filename is deliberately corrected here
- [X] T002 [P] Add a `tests/integration` row (after the existing `tests/contract` row) to the "Important directories" table in `docs/developer-guide.md`'s `gitstore-controller-manager` section (around line 242)

---

## Phase 2: Foundational (Blocking Prerequisites)

**Purpose**: Shared test fixtures every user story's tests depend on. `tests/integration` is a separate Go package from `tests/contract`, so equivalent unexported test doubles must be duplicated here rather than imported.

**⚠️ CRITICAL**: No user story test can be written until this phase is complete

- [X] T003 Implement the `widget` fixture type and helpers in `gitstore-controller-manager/tests/integration/fixtures_test.go`: `type widget struct{ Namespace, Name, ResourceVersion string }`, `func widgetKey(w widget) types.WorkItemKey`, `func widgetRevision(w widget) string` (mirrors `tests/contract/listwatch_bootstrap_test.go` lines 23-31)
- [X] T004 Implement `stubWatcher[T]` and `stubListWatcher[T]` test doubles in `gitstore-controller-manager/tests/integration/fixtures_test.go`, satisfying `listwatch.Watcher[T]` and `listwatch.ListWatcher[T]` respectively, with configurable list/watch failure counts and scripted event sequences (mirrors `tests/contract/listwatch_bootstrap_test.go` lines 33-100+) (depends on T003)
- [X] T005 Implement a `newRunner(t *testing.T, lw *stubListWatcher[widget], store checkpoint.Store) (*listwatch.Runner[widget], *cache.Cache[widget], *enqueueRecorder)` helper in `gitstore-controller-manager/tests/integration/fixtures_test.go` that wires a real `listwatch.Runner[widget]` against a real `cache.Cache[widget]`, mirroring `tests/contract/listwatch_bootstrap_test.go::newRunner` (depends on T003, T004)
- [X] T006 [P] Implement a `scriptedReconciler` fake in `gitstore-controller-manager/tests/integration/fixtures_test.go` implementing `types.Reconciler`: configurable to return a scripted sequence of `types.ReconcileResult` values across successive `Reconcile` calls, tracking call count via `atomic.Int64` (generalizes `countingReconciler` from `tests/contract/manager_dispatch_test.go` and the always-failing reconciler from `tests/contract/retry_quarantine_test.go`)
- [X] T007 [P] Implement a `fakeStatusClient` in `gitstore-controller-manager/tests/integration/fixtures_test.go` implementing `status.StatusClient`: records every applied `*status.StatusPatch` keyed by `types.WorkItemKey`, configurable to return `types.ErrConflict` on a specific call number for a given key

**Checkpoint**: Foundation ready — all five user stories' test files can now be written.

---

## Phase 3: User Story 1 - Verify reconcile, retry, and resume behavior end-to-end (Priority: P1) 🎯 MVP

**Goal**: Prove that a real `Manager` + `Runner` + `Store` reconcile successfully on the first attempt, retry transient failures to eventual success, and resume cleanly from a checkpoint after a restart (FR-001, FR-002, FR-003).

**Independent Test**: `cd gitstore-controller-manager && go test ./tests/integration/... -run TestIntegration_Reconcile -race -v && go test ./tests/integration/... -run TestIntegration_Restart -race -v` — all pass.

### Tests for User Story 1

- [X] T008 [P] [US1] Write `TestIntegration_Reconcile_SucceedsOnFirstAttempt` in `gitstore-controller-manager/tests/integration/reconcile_retry_resume_test.go`: register a `scriptedReconciler` (T006) returning `types.Success{}` on the first call with a real `manager.Manager`, enqueue one `WorkItemKey`, assert via `testutil.ToFloat64` that `health.ReconcileTotal{kind,"success"}` increments by exactly 1 and the reconciler was called exactly once (per contracts/integration-test-scenarios.md)
- [X] T009 [P] [US1] Write `TestIntegration_Reconcile_TransientFailureThenSucceeds` in `gitstore-controller-manager/tests/integration/reconcile_retry_resume_test.go`: configure `scriptedReconciler` (T006) to return `types.TransientFailure{}` N times then `types.Success{}`, assert the item's final observed state is success and the reconciler was called N+1 times. **Deviation from original task text**: `health.ReconcileTotal{transient_failure}` is only incremented by `internal/manager/manager.go`'s `handleTransient` when the retry budget is exhausted (quarantine/terminal path), never on an eventual success — this was verified against the real implementation, not assumed, so the test asserts only the `success` counter delta and documents why in a comment
- [X] T010 [US1] Write `TestIntegration_Restart_ResumesFromCheckpoint_NoLostOrDuplicateWork` in `gitstore-controller-manager/tests/integration/reconcile_retry_resume_test.go`: using `newRunner` (T005) and a shared `checkpoint.MemoryStore`, bootstrap a `Runner[widget]` + `manager.Manager`, reconcile some items to success, cancel the context mid-run with other items still pending/in-flight, then construct a second `Runner[widget]` + `manager.Manager` pair against the same `Store` and start it. Wire `ReconcilerRegistration.OnSuccess` to `Runner.MarkCompleted` so the checkpoint's durable replay set contains only unfinished work; assert the pending item completes once after restart and the already-completed item is not dispatched again (depends on T003-T007)

**Checkpoint**: User Story 1 is fully functional and independently testable — reconcile/retry/resume path is proven end-to-end.

---

## Phase 4: User Story 2 - Verify status-conflict handling (Priority: P1)

**Goal**: Prove that a stale status write is rejected with `types.ErrConflict` and that a controller under `Manager` correctly re-fetches and retries rather than treating the conflict as fatal (FR-004).

**Independent Test**: `cd gitstore-controller-manager && go test ./tests/integration/... -run TestIntegration_StatusConflict -race -v` — all pass.

### Tests for User Story 2

- [X] T011 [P] [US2] Write `TestIntegration_StatusConflict_StaleWriteRejected` in `gitstore-controller-manager/tests/integration/status_conflict_test.go`: using `fakeStatusClient` (T007), submit two `status.StatusPatch`es for the same `WorkItemKey` where the second call is configured to reject the first's now-stale `ResourceVersion`; assert the stale `Apply` call returns `types.ErrConflict` and only the newer write is recorded by the fake
- [X] T012 [US2] Write `TestIntegration_StatusConflict_ControllerRetriesAfterConflict` in `gitstore-controller-manager/tests/integration/status_conflict_test.go`: a purpose-built `statusConflictReconciler` (rather than `scriptedReconciler`, since this scenario needs the reconciler to itself call `fakeStatusClient.Apply` and branch on the result) invokes `Apply`, receives `types.ErrConflict` on the first call, returns `types.TransientFailure{}` in response; the retried second call succeeds. Registered with a real `manager.Manager`; asserts the final result reaches success (`mgr.IsQuarantined` is false), `Apply` was called exactly twice, and exactly one patch was applied (depends on T006, T007)

**Checkpoint**: User Stories 1 AND 2 both work independently — reconcile/retry/resume and status-conflict handling are both proven.

---

## Phase 5: User Story 3 - Verify disconnect, reconnect, and replay-recovery behavior (Priority: P2)

**Goal**: Prove that a `Runner[T]` reconnects with backoff after a watch disconnect, reconciles every resource changed during the outage exactly once, and falls back to a full list-then-watch bootstrap when its checkpoint's replay window has been exceeded (FR-005, FR-006).

**Independent Test**: `cd gitstore-controller-manager && go test ./tests/integration/... -run "TestIntegration_Disconnect|TestIntegration_ReplayWindowExceeded" -race -v` — all pass.

### Tests for User Story 3

- [X] T013 [P] [US3] Write `TestIntegration_Disconnect_ReconnectsWithBackoff` in `gitstore-controller-manager/tests/integration/disconnect_reconnect_test.go`: configure `stubListWatcher[widget]` (T004) so its first `stubWatcher` closes with an error mid-stream; run a real `listwatch.Runner[widget]` (T005) and assert `Watch` is called more than once (reconnect attempted), with the delay between calls consistent with the Runner's configured backoff (bounded by `MaxBackoff`)
- [X] T014 [US3] Write `TestIntegration_Disconnect_ResourcesChangedDuringOutageReconciledExactlyOnce` in `gitstore-controller-manager/tests/integration/disconnect_reconnect_test.go`: script `stubListWatcher[widget]` (T004) so its second `Watch` call delivers a Modified `listwatch.WatchEvent[widget]` for one resource representing a change made "during" a simulated outage; asserts on the Runner's own `enqueueRecorder` (an unchanged resource is enqueued exactly once from bootstrap only, the changed resource exactly twice — bootstrap plus the one outage-era event) rather than routing through a `manager.Manager`, since the enqueue callback is the externally observable contract point for this scenario per FR-013 (depends on T003-T006)
- [X] T015 [US3] Write `TestIntegration_ReplayWindowExceeded_FallsBackToFullBootstrap` in `gitstore-controller-manager/tests/integration/disconnect_reconnect_test.go`: configure a scripted `stubWatcher` (T004) to return `listwatch.ErrWatchExpired` (`internal/listwatch/types.go:47`); assert the Runner discards its checkpoint, performs a fresh `List`, persists a new checkpoint via the shared `checkpoint.Store`, and resumes watching (a second `Watch` call after the re-list) rather than terminating (depends on T003-T005)

**Checkpoint**: User Stories 1, 2, AND 3 all work independently.

---

## Phase 6: User Story 4 - Diagnose and recover from operational failure modes using runbooks (Priority: P2)

**Goal**: Prove the poisoned-item mechanism end-to-end (prerequisite for the poisoned-item runbook's claims), then author the three operational runbooks referenced by FR-008–FR-010, cross-linked from `docs/developer-guide.md`.

**Independent Test**: An engineer unfamiliar with the controller internals follows `docs/runbooks/controller-lag.md`, `controller-replay-window-exceeded.md`, and `controller-poisoned-item.md` against a manually induced instance of each failure (using `make controller` and the `/metrics` + poison HTTP endpoints) and successfully diagnoses each one using only the runbook.

### Tests for User Story 4

- [X] T016 [P] [US4] Write `TestIntegration_PoisonedItem_SurfacedAsTerminalFailure` in `gitstore-controller-manager/tests/integration/poison_item_test.go`: configure `scriptedReconciler` (T006) to return `types.TransientFailure{}` past `MaxAttempts` (and, in a subtest, `types.TerminalFailure{}` immediately); assert the key appears in `Manager.AllPoisonItems()` and `Manager.QuarantineStore(kind)`. `health.PoisonItemsTotal{kind}` is asserted too, but note it is a snapshot gauge only refreshed by `Manager.KindStats()` (called by the `/metrics` handler in production) — the test calls it directly since no HTTP server is running (depends on T006)
- [X] T017 [US4] Write `TestIntegration_PoisonedItem_VisibleViaHTTPPoisonAPI` in `gitstore-controller-manager/tests/integration/poison_item_test.go`: quarantine an item, then construct `api.ListPoisonHandler(mgr)` and `api.RequeuePoisonHandler(mgr)` directly against the real `manager.Manager` (which satisfies `api.Requeuer`) using `net/http/httptest`, registering only the single `GET /controller/v1/poison/{kind}` route (which already serves both a specific kind and `_all` — matching `cmd/controller/main.go`'s exact route registration, not a duplicate `_all` route). Assert the route returns the item including its `LastError`, and `POST .../requeue` returns 204 and triggers a re-dispatch (verified via the reconciler's call count increasing, since the always-failing reconciler from T016 would immediately re-quarantine — the reconciler script here is `[TerminalFailure, Success]` so requeue's effect is observable) (depends on T016)

### Runbooks for User Story 4

- [X] T018 [P] [US4] Author `docs/runbooks/controller-lag.md`: **Symptom** (queue depth growing / reconciles falling behind) → **Diagnostic Steps** referencing `gitstore_controller_queue_depth{kind}`, `gitstore_controller_active_workers{kind}`, `gitstore_controller_stalled_workers{kind}`, `gitstore_controller_reconcile_total{kind,result}` (per contracts/runbook-signal-contract.md rows 1-4) → **Recovery Actions** (increase `WorkerCount`, investigate a slow reconciler, check an upstream dependency) → **Verification** (confirm queue depth trending down and `StalledWorkers` returns to 0)
- [X] T019 [P] [US4] Author `docs/runbooks/controller-replay-window-exceeded.md`: **Symptom** (watch cursor expired / `ErrWatchExpired`) → **Diagnostic Steps** referencing `gitstore_controller_checkpoint_last_write_timestamp_seconds{kind}`, `gitstore_controller_checkpoint_replay_backlog{kind}`, and the Runner's structured "watch expired / relist triggered" log line (per contracts/runbook-signal-contract.md rows 5-6) → **Recovery Actions** (confirm the automatic fallback-to-full-list occurred; no manual relist is required since the Runner self-heals per FR-006) → **Verification** (confirm a fresh checkpoint timestamp and replay backlog draining)
- [X] T020 [P] [US4] Author `docs/runbooks/controller-poisoned-item.md`: **Symptom** (item repeatedly fails reconciliation) → **Diagnostic Steps** referencing `gitstore_controller_poison_items_total{kind}`, `GET /controller/v1/poison/{kind}` / `/_all`, and the quarantine structured log's `LastError` field (per contracts/runbook-signal-contract.md rows 7-10) → **Recovery Actions** (fix the underlying resource data then `POST .../requeue`, or leave it quarantined and document why per the edge case of distinguishing a genuinely poisoned item from a longer transient outage) → **Verification** (confirm the item no longer appears in `AllPoisonItems()`/the poison API after a successful requeue)
- [X] T021 [US4] Link all three runbooks from the `gitstore-controller-manager` section of `docs/developer-guide.md` (near the directory table updated in T002), per research.md's "Runbook format and location" decision (depends on T018, T019, T020)

**Checkpoint**: User Stories 1-4 all work independently — poisoned-item mechanism is proven, and all three runbooks are published and discoverable.

---

## Phase 7: User Story 5 - Validate observability signals used by the above scenarios (Priority: P3)

**Goal**: Confirm every signal referenced by the three runbooks (T018-T020) is genuinely emitted and accurate, per FR-011 and SC-003, closing the loop on "operations-ready."

**Independent Test**: `cd gitstore-controller-manager && go test ./tests/integration/... -run TestObservability -race -v` — every row in contracts/runbook-signal-contract.md has a passing corresponding test.

### Tests for User Story 5

- [X] T022 [P] [US5] Write `TestObservability_QueueDepth_ReflectsPendingItems` and `TestObservability_ActiveWorkers_ReflectsRunningReconciles` in `gitstore-controller-manager/tests/integration/observability_test.go` using `testutil.ToFloat64` against `health.QueueDepth` and `health.ActiveWorkers` (contracts/runbook-signal-contract.md rows 1-2). **Note**: `QueueDepth` is asserted with `Manager.Start` never called — the dispatch loop drains the queue near-instantly once running, making queue depth too transient to observe deterministically otherwise; enqueuing without starting keeps items parked in the queue
- [X] T023 [P] [US5] Write `TestObservability_StalledWorkers_SetWhenNoRecentSuccess` and `TestObservability_ReconcileTotal_LabeledByOutcome` in `gitstore-controller-manager/tests/integration/observability_test.go` against `health.StalledWorkers` and `health.ReconcileTotal` (contracts/runbook-signal-contract.md rows 3-4). **Real bug found and fixed**: writing this test (red first, per Constitution Principle I) revealed `Manager.KindStats()` in `internal/manager/manager.go` never called `health.StalledWorkers.WithLabelValues(kind).Set(...)` — every other gauge (`ActiveWorkers`, `QueueDepth`, `PoisonItemsTotal`, `CheckpointReplayBacklog`) was updated except this one, so the metric was permanently stuck unset/0 in production despite the internal `Stalled` bool being computed correctly. Fixed by adding the `Set(1)`/`Set(0)` call in `KindStats()` and pre-initializing the gauge to 0 in `Register()` (matching the existing pattern for the other three gauges); verified no regression via full `go test ./tests/...` in `gitstore-controller-manager`
- [X] T024 [P] [US5] Write `TestObservability_WatchExpired_LogsRelistTrigger` in `gitstore-controller-manager/tests/integration/observability_test.go` using a `go.uber.org/zap/zaptest/observer` core attached to the `Runner`'s logger, triggering the same `ErrWatchExpired` path as T015, asserting the expected structured log line fires (contracts/runbook-signal-contract.md row 6). **Note**: the actual log message, read from `internal/listwatch/runner.go`, is `"watch cursor expired; re-listing"` — the runbook (T019) and contract doc were corrected to quote this exact string rather than the originally-guessed generic phrasing
- [X] T025 [P] [US5] Write `TestObservability_PoisonItemsTotal_IncrementsOnQuarantine` and `TestObservability_RequeuePoisonAPI_ClearsQuarantineAndReenqueues` in `gitstore-controller-manager/tests/integration/observability_test.go` (contracts/runbook-signal-contract.md rows 7, 9) (depends on T016, T017)
- [X] T026 [US5] Write `TestObservability_QuarantineLog_IncludesLastError` in `gitstore-controller-manager/tests/integration/observability_test.go` using the same `zaptest/observer` pattern as T024, asserting the quarantine log line's fields include the reconciler's error text (contracts/runbook-signal-contract.md row 10)
- [X] T027 [US5] Add a code comment at the top of `gitstore-controller-manager/tests/integration/observability_test.go` cross-referencing the existing `tests/contract/health_test.go` coverage (`TestHealth_CheckpointLastWriteTimestamp_UpdatesOnSave`, `TestHealth_CheckpointReplayBacklog_TracksQueueDepth`) as satisfying contracts/runbook-signal-contract.md row 5 — verified both cross-referenced tests still exist and pass (depends on T022-T026)
- [X] T028 [US5] Reconcile the runbook prose from T018-T020 against every passing observability test (T022-T027): corrected `controller-replay-window-exceeded.md` and `controller-poisoned-item.md` to quote the exact log message strings found in source rather than generic paraphrases, per the "Rule" in contracts/runbook-signal-contract.md and SC-003 (depends on T018-T027)

**Checkpoint**: All five user stories are independently functional; every runbook-referenced signal is test-verified per SC-003.

---

## Phase 8: Polish & Cross-Cutting Concerns

**Purpose**: Final validation against the spec's measurable outcomes

- [X] T029 Run `cd gitstore-controller-manager && go test ./tests/integration/... -race -v` and confirm the full new suite completes in under 5 minutes (SC-004). Actual: ~2.2-2.7s test time (well under budget); also re-verified via 8x repeat run (`-count=8`) with no flakes, and a full `go test ./tests/...` (contract + checkpoint + integration) shows no regression
- [X] T030 [P] Run `make lint` from the repository root and fix any formatting/vet issues introduced by the new test files and runbooks. Found and fixed two genuine `U1000` unused-code lint failures: `widgetCheckpoint` and `enqueueRecorder.len()` in `fixtures_test.go` were written per the original task text but never ended up used by any test — removed both (and the now-unused `encoding/json` import) rather than keeping dead code
- [X] T031 Execute `specs/038-controller-integration-tests-runbook/quickstart.md` end-to-end exactly as written and correct any inaccuracies found. Both documented commands (`go test ./tests/integration/... -v -race` and `go test ./tests/integration/... -run TestObservability -v`) ran verbatim and passed; no corrections needed

---

## Dependencies & Execution Order

### Phase Dependencies

- **Setup (Phase 1)**: No dependencies — can start immediately
- **Foundational (Phase 2)**: Depends on Setup — BLOCKS all user stories (every story's tests import the fixtures from T003-T007)
- **User Stories (Phase 3-7)**: All depend on Foundational phase completion
  - US1, US2, US3 have no dependencies on each other and can proceed in parallel
  - US4's runbook-authoring tasks (T018-T020) can be drafted in parallel with US1-US3, but the poisoned-item behavioral tests (T016-T017) they build on are self-contained within US4
  - US5 depends on US4's runbooks existing (T018-T020) to know which signals to validate, and its tests reuse setups from US1 (T010), US3 (T015), and US4 (T016-T017) — see individual task `depends on` notes
- **Polish (Phase 8)**: Depends on all five user stories being complete

### User Story Dependencies

- **User Story 1 (P1)**: After Foundational — no dependency on other stories
- **User Story 2 (P1)**: After Foundational — no dependency on other stories
- **User Story 3 (P2)**: After Foundational — no dependency on other stories
- **User Story 4 (P2)**: After Foundational — poisoned-item tests (T016-T017) are self-contained; runbook authoring (T018-T021) references signals already implemented in spec 025/036, not new work from US1-US3
- **User Story 5 (P3)**: After Foundational and after US4's runbooks exist (T018-T020) — reuses test setups from US1/US3/US4 for several assertions (see T024-T027 `depends on`)

### Parallel Opportunities

- T002 (Setup) can run in parallel with T001
- T006, T007 (Foundational) can run in parallel with each other and with T003-T005 once T003 lands (T004/T005 depend on T003)
- Once Foundational completes: US1, US2, US3 test files can all be written in parallel by different contributors
- Within US1: T008, T009 in parallel; T010 depends on all of Foundational but not on T008/T009
- Within US4: T016 parallel with runbook drafts T018-T020; T017 depends on T016
- Within US5: T022, T023, T024, T025 in parallel; T026 depends on T024's pattern but not its result; T027-T028 are sequential wrap-up

---

## Parallel Example: User Story 1

```bash
# After Foundational (T003-T007) completes, launch both independent US1 tests together:
Task: "Write TestIntegration_Reconcile_SucceedsOnFirstAttempt in gitstore-controller-manager/tests/integration/reconcile_retry_resume_test.go"
Task: "Write TestIntegration_Reconcile_TransientFailureThenSucceeds in gitstore-controller-manager/tests/integration/reconcile_retry_resume_test.go"
# T010 (restart/resume) is written afterward in the same file, since it exercises a superset of the fixtures.
```

## Parallel Example: Foundational fixtures

```bash
# T003 must land first (widget type). T004/T005 depend on it. T006/T007 are independent of T003-T005:
Task: "Implement scriptedReconciler fake in gitstore-controller-manager/tests/integration/fixtures_test.go"
Task: "Implement fakeStatusClient in gitstore-controller-manager/tests/integration/fixtures_test.go"
```

---

## Implementation Strategy

### MVP First (User Story 1 Only)

1. Complete Phase 1: Setup (T001-T002)
2. Complete Phase 2: Foundational (T003-T007) — CRITICAL, blocks all stories
3. Complete Phase 3: User Story 1 (T008-T010)
4. **STOP and VALIDATE**: `go test ./tests/integration/... -run TestIntegration_Reconcile -race -v` and `-run TestIntegration_Restart`
5. This alone delivers CI-enforced regression protection for the core reconcile/retry/resume path shipped in specs 025/026/036 — the highest-value increment per spec.md's "Why this priority" for US1.

### Incremental Delivery

1. Setup + Foundational → foundation ready
2. Add US1 → validate independently → merge (MVP)
3. Add US2 → validate independently → merge
4. Add US3 → validate independently → merge
5. Add US4 → validate independently (manual runbook walkthrough) → merge
6. Add US5 → validate independently (all `TestObservability_*` pass, runbooks corrected if needed) → merge
7. Phase 8 polish → final SC-004/SC-003 sign-off

### Parallel Team Strategy

With multiple contributors, after Foundational (T003-T007) lands:
- Contributor A: User Story 1 (T008-T010)
- Contributor B: User Story 2 (T011-T012)
- Contributor C: User Story 3 (T013-T015)
- Contributor D: User Story 4 (T016-T021) — can start poisoned-item tests and runbook drafts immediately; drafts stay in review until US5 confirms signal accuracy
- User Story 5 (T022-T028) starts once US4's runbook drafts (T018-T020) exist, ideally by the same contributor who wrote US1/US3/US4 tests being reused

---

## Notes

- [P] tasks touch different functions within the same new file, or different files entirely, with no ordering dependency
- [Story] label maps each task to its user story for traceability back to spec.md
- No test task may be marked complete until it fails first against unmodified production code, then passes (Constitution Principle I) — for this feature, "modified production code" should rarely be needed since specs 025/026/036 already implement the behavior under test; if a test fails against correct-looking production code, treat that as a signal to investigate before assuming the test is wrong
- Commit after each task or logical group, consistent with `make pr-ready` being run before opening a PR
- Avoid: adding new production abstractions, new metrics, or new HTTP routes unless T027/T028's audit proves an existing runbook claim is otherwise unverifiable — per research.md, none are currently anticipated
