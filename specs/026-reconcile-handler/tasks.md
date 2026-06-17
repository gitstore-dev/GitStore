# Tasks: Reconcile Handler Contract for Core and CRD Kinds

**Input**: Design documents from `/specs/026-reconcile-handler/`  
**Branch**: `026-reconcile-handler`  
**Prerequisites**: plan.md ✅, spec.md ✅, research.md ✅, data-model.md ✅, contracts/ ✅

**Tests**: Test-First Development (Constitution Principle I — NON-NEGOTIABLE). Tests MUST be written before implementation and verified to fail before proceeding.

**Scope notes**:
- US2 (CRD kind) is satisfied by the kind-agnostic dispatch path alone — no `HotRegister` machinery. CRD reconcilers register via `Register()` before `Start()`, identical to core kinds. Hot-registration deferred to a future spec pending #149/#164. See research.md §7.
- No README or diagram updates — ALPHA stage, no stable APIs.

## Format: `[ID] [P?] [Story] Description`

- **[P]**: Can run in parallel (different files, no dependencies on incomplete tasks)
- **[Story]**: User story label (US1–US4)

---

## Phase 1: Setup

**Purpose**: Extend existing project structure for new packages; no new external dependencies.

- [X] T001 Create `gitstore-controller-manager/internal/status/` directory and empty `patch.go` file
- [X] T002 Create `gitstore-controller-manager/internal/cache/accessor.go` file (empty, package declaration only)
- [X] T003 Create `gitstore-controller-manager/internal/manager/panic.go` file (empty, package declaration only)

---

## Phase 2: Foundational (Blocking Prerequisites)

**Purpose**: Replace the `(Result, error)` reconciler return with the `ReconcileResult` sealed interface. All user stories depend on this type change. Existing tests must be updated to compile before any story work begins.

**⚠️ CRITICAL**: No user story work can begin until this phase is complete and `go test ./...` passes.

- [X] T004 Add `ReconcileResult` sealed interface and four concrete variants (`Success`, `RequeueAfter`, `TransientFailure`, `TerminalFailure`) with constructor functions (`ResultOK`, `ResultAfter`, `ResultTransient`, `ResultTerminal`) to `gitstore-controller-manager/internal/types/types.go`; change `Reconciler.Reconcile` signature from `(Result, error)` to `ReconcileResult`; keep `Result` struct temporarily as a type alias until all callers are updated
- [X] T005 Update all existing reconciler stubs in `gitstore-controller-manager/tests/contract/reconciler_contract_test.go` and `gitstore-controller-manager/tests/contract/manager_dispatch_test.go` to return `types.ResultOK()` instead of `(manager.Result{}, nil)` so the package compiles
- [X] T006 Update `gitstore-controller-manager/internal/manager/manager.go` `dispatch()` to call `ks.reg.Reconciler.Reconcile(rctx, key)` and receive a single `ReconcileResult`; replace the `(result, err)` pair with a type-switch stub (all cases log and call the existing quarantine/success paths as placeholders); ensure `go build ./...` passes
- [X] T007 Remove the now-unused `Result` struct and `(Result, error)` signature remnants from `gitstore-controller-manager/internal/types/types.go`; run `go build ./...` to confirm clean compile

**Checkpoint**: `go build ./...` and `go test ./...` must pass before proceeding.

---

## Phase 3: User Story 1 — Core-Kind Reconciler Interface (Priority: P1) 🎯 MVP

**Goal**: The `ReconcileResult` type-switch is fully wired in the dispatch engine. All four result variants are handled correctly. Panic recovery is in place. Cache-sync gating holds dispatch until the cache is ready.

**Independent Test**: A `CategoryTaxonomy` reconciler stub that returns each of the four result variants can be registered, work items enqueued, and the correct manager behaviour asserted — no live API, no persistence, no CRD machinery.

### Tests (write first, verify they FAIL before T013)

- [X] T008 [P] [US1] Add `TestReconcileResult_AllFourVariants` to `gitstore-controller-manager/tests/contract/reconciler_contract_test.go`: construct `ResultOK()`, `ResultAfter(d)`, `ResultTransient(err)`, `ResultTerminal(err)` and assert each satisfies `ReconcileResult` interface and carries the correct fields
- [X] T009 [P] [US1] Add `TestManager_TerminalFailure_QuarantinesImmediately` to `gitstore-controller-manager/tests/contract/manager_dispatch_test.go`: register a reconciler that returns `ResultTerminal(err)`, enqueue one item, assert it is quarantined in the same dispatch cycle with zero retry attempts and the `gitstore_controller_reconcile_total{result="terminal_failure"}` counter incremented
- [X] T010 [P] [US1] Add `TestManager_RequeueAfter_DelaysReenqueue` to `gitstore-controller-manager/tests/contract/manager_dispatch_test.go`: reconciler returns `ResultAfter(50ms)`, assert item is re-enqueued after the delay and not before
- [X] T011 [P] [US1] Add `TestManager_ReconcilerPanic_RecoveredAsTransient` and `TestManager_ReconcilerPanic_LogsStackTrace` to `gitstore-controller-manager/tests/contract/manager_dispatch_test.go`: reconciler panics with a string value; assert manager does not crash, item enters retry cycle, `gitstore_controller_reconcile_total{result="transient_failure"}` incremented, and the `PanicError.Stack` is non-empty
- [X] T012 [P] [US1] Add `TestManager_DispatchHeldUntilCacheSynced` to `gitstore-controller-manager/tests/contract/manager_dispatch_test.go`: register a reconciler with a cache whose `HasSynced()` returns false; enqueue an item; assert reconciler is NOT called within 200 ms; call `MarkSynced()`; assert reconciler IS called within 500 ms after sync

### Implementation

- [X] T013 [US1] Implement the full `ReconcileResult` type-switch in `gitstore-controller-manager/internal/manager/manager.go` `dispatch()`: `Success` → update lastSuccess + increment `reconcile_total{result="success"}`; `TerminalFailure` → quarantine immediately without entering retry loop + increment `reconcile_total{result="terminal_failure"}`; `TransientFailure` → pass to retry engine with optional `BackoffHint` as override for `InitialInterval`; `RequeueAfter` → schedule re-enqueue after `After` duration via time.AfterFunc
- [X] T014 [US1] Update `gitstore-controller-manager/internal/retry/retry.go` `RunWithRetry` to accept an optional `initialIntervalOverride time.Duration` parameter; when non-zero, use it as the backoff's `InitialInterval` for this invocation (supports `TransientFailure.BackoffHint`)
- [X] T015 [US1] Implement `PanicError` struct (`Value any`, `Stack []byte`) and `safeReconcile` wrapper function in `gitstore-controller-manager/internal/manager/panic.go`; `safeReconcile` wraps a `Reconciler.Reconcile` call in a deferred `recover()` that captures `debug.Stack()` and returns `ResultTransient(&PanicError{...})`
- [X] T016 [US1] Replace the direct `ks.reg.Reconciler.Reconcile(rctx, key)` call in `gitstore-controller-manager/internal/manager/manager.go` with `safeReconcile(ks.reg.Reconciler, key)(rctx)`; add `errors.As` check in `dispatch()` for `*PanicError` after the type-switch to emit structured ERROR log with `zap.ByteString("stacktrace", pe.Stack)` and increment `health.ReconcileTotal.WithLabelValues(key.Kind, "transient_failure")`
- [X] T017 [US1] Add `syncChecker interface { HasSynced() bool }` to `gitstore-controller-manager/internal/manager/types.go`; add `Cache syncChecker` field to `ReconcilerRegistration`; add `cache syncChecker` field to `kindState` in `gitstore-controller-manager/internal/manager/manager.go`
- [X] T018 [US1] Add cache-sync gate to `runDispatchLoop` in `gitstore-controller-manager/internal/manager/manager.go`: after `Dequeue` and before `pool.Submit`, spin on `!ks.cache.HasSynced()` polling every 50 ms with a `select` on `ctx.Done()` to exit cleanly
- [X] T019 [P] [US1] Implement `CacheAccessor[T]` interface (`Get(key WorkItemKey) (T, bool)`) and `readOnlyCache[T]` unexported wrapper with `AsReadOnly[T](c *Cache[T]) CacheAccessor[T]` constructor in `gitstore-controller-manager/internal/cache/accessor.go`

**Checkpoint**: `go test ./tests/contract/` must pass for all T008–T012 tests. Run `go test ./...` for full suite.

---

## Phase 4: User Story 2 — CRD-Kind Reconciler Registration (Priority: P2)

**Goal**: `Register` validates its input and returns a descriptive error on duplicate or invalid registration. CRD reconcilers register via the identical `Register()` API as core kinds — no kind-specific dispatch branches anywhere.

**Independent Test**: Define a synthetic CRD kind `BackfillJob`, register a reconciler for it via `Register()`, enqueue a work item, assert dispatch reaches the reconciler using the same code path as any core kind.

### Tests (write first, verify they FAIL before T024)

- [X] T020 [P] [US2] Add `TestManager_DuplicateRegistration_ReturnsError` to `gitstore-controller-manager/tests/contract/manager_dispatch_test.go`: call `Register` twice with the same kind; assert second call returns a non-nil error containing the kind name
- [X] T021 [P] [US2] Add `TestManager_NilReconciler_ReturnsError` and `TestManager_NilCache_ReturnsError` to `gitstore-controller-manager/tests/contract/manager_dispatch_test.go`: pass `nil` for each required field; assert `Register` returns a descriptive error
- [X] T022 [P] [US2] Add `TestManager_CRDKind_DispatchedOnSamePathAsCoreKind` to `gitstore-controller-manager/tests/contract/manager_dispatch_test.go`: register reconcilers for `CategoryTaxonomy` (core) and `BackfillJob` (synthetic CRD); enqueue one item each; assert both reconcilers are called and neither triggers a kind-specific branch
- [X] T023 [P] [US2] Add `TestManager_UnregisteredKind_EmitsSignalNoPanic` to `gitstore-controller-manager/tests/contract/manager_dispatch_test.go`: enqueue an item for an unregistered kind; assert `ErrKindNotRegistered` is returned and the manager does not panic

### Implementation

- [X] T024 [US2] Update `Manager.Register` in `gitstore-controller-manager/internal/manager/manager.go` to return `error`; validate `reg.Kind` non-empty, `reg.Reconciler != nil`, `reg.Cache != nil`, and no existing entry for `reg.Kind`; return `fmt.Errorf(...)` for each violation; store `reg.Cache` on `kindState.cache`
- [X] T025 [US2] Update `gitstore-controller-manager/cmd/controller/main.go` to check and fatal-log on `mgr.Register(...)` errors

**Checkpoint**: `go test ./tests/contract/` passes T020–T023. `go test ./...` passes.

---

## Phase 5: User Story 3 — Status Writeback (Priority: P3)

**Goal**: Reconcilers can write computed `.status` fields back to the API via a typed `StatusPatch` with optimistic concurrency and idempotent write suppression.

**Independent Test**: A reconciler stub that reads `.status` from its cache, builds a `StatusPatch`, calls `MockStatusClient.Apply`, and asserts the call is issued exactly once when fields differ and zero times when `IsNoOp` returns true — no queue, no dispatch, no network.

### Tests (write first, verify they FAIL before T031)

- [X] T026 [P] [US3] Create `gitstore-controller-manager/tests/contract/status_patch_test.go`; add `TestStatusPatch_IsNoOp_AllFieldsMatch`: construct a `StatusPatch` whose every non-nil field matches `ResourceStatus`; assert `IsNoOp` returns true
- [X] T027 [P] [US3] Add `TestStatusPatch_IsNoOp_OneFieldDiffers` to `gitstore-controller-manager/tests/contract/status_patch_test.go`: one non-nil field differs from `ResourceStatus`; assert `IsNoOp` returns false
- [X] T028 [P] [US3] Add `TestStatusPatch_ObservedGenerationRequired` to `gitstore-controller-manager/tests/contract/status_patch_test.go`: construct a patch with `ObservedGeneration == nil`; assert `IsNoOp(current)` returns false when `current.ObservedGeneration != 0` (ensuring the reconciler must always set it on success)
- [X] T029 [P] [US3] Add `TestStatusClient_Conflict_ReturnsErrConflict` to `gitstore-controller-manager/tests/contract/status_patch_test.go`: use a `MockStatusClient` that returns a conflict error wrapping `types.ErrConflict`; assert `errors.Is(err, types.ErrConflict)` is true
- [X] T030 [P] [US3] Add `TestStatusClient_NoOpPatch_SkipsApply` to `gitstore-controller-manager/tests/contract/status_patch_test.go`: reconciler builds a no-op patch, calls `IsNoOp`, and asserts `MockStatusClient.Apply` is never called

### Implementation

- [X] T031 [US3] Add `ErrConflict = errors.New("optimistic concurrency conflict")` to `gitstore-controller-manager/internal/types/types.go`
- [X] T032 [US3] Implement `Condition` struct, `ResourceStatus` struct, `StatusPatch` struct, `IsNoOp(current ResourceStatus) bool` method, and `StatusClient` interface in `gitstore-controller-manager/internal/status/patch.go`; field list per data-model.md: `ResourceVersion string`, `ObservedGeneration *int64`, `LastAppliedRevision *string`, `Conditions []*Condition`
- [X] T033 [US3] Implement `MockStatusClient` in `gitstore-controller-manager/internal/status/patch.go` (or a `_test.go` file in the same package) with a configurable return error and a call counter; used by contract tests

**Checkpoint**: `go test ./tests/contract/` passes T026–T030. `go test ./...` passes.

---

## Phase 6: User Story 4 — Per-Kind Registration Visibility (Priority: P4)

**Goal**: The `/health` surface lists all registered kinds with `registered: true`. Startup registration errors are reported before any dispatch begins.

**Independent Test**: Start the manager with two registered reconcilers; query `KindStats()`; assert both kinds are present with `Registered: true`. Startup with duplicate registration returns a fatal error before `Start()` is called.

### Tests (write first, verify they FAIL before T037)

- [X] T034 [P] [US4] Update `gitstore-controller-manager/tests/contract/health_test.go`: add `TestHealth_RegisteredKindsListed` — register reconcilers for `CategoryTaxonomy` and `Collection`, call `mgr.KindStats()`, assert both kinds appear in the map with `Registered == true`
- [X] T035 [P] [US4] Add `TestHealth_DuplicateKind_FatalBeforeStart` to `gitstore-controller-manager/tests/contract/health_test.go`: assert that `Register` returns an error for a duplicate kind and that no dispatch goroutines are started (call count on reconciler remains zero after attempted `Start` with error-halted registration)

### Implementation

- [X] T036 [US4] Add `Registered bool` field to `health.KindStat` in `gitstore-controller-manager/internal/health/handler.go`; set it to `true` for all kinds returned by `KindStats()` in `gitstore-controller-manager/internal/manager/manager.go`; update the JSON health response accordingly

**Checkpoint**: `go test ./tests/contract/` passes T034–T035. Full `go test ./...` passes. `make pr-ready` passes.

---

## Phase 7: Polish & Cross-Cutting Concerns

- [X] T037 [P] Update `gitstore-controller-manager/tests/contract/cache_contract_test.go` to add `TestCacheAccessor_ReadOnly`: assert that `cache.AsReadOnly(c)` returns a value that satisfies `CacheAccessor[T]` and does not expose `Set`, `Delete`, or `MarkSynced` methods
- [X] T038 [P] Update `gitstore-controller-manager/tests/contract/retry_quarantine_test.go` to add `TestRetry_BackoffHint_OverridesInitialInterval`: pass a `TransientFailure` with `BackoffHint = 200ms` and assert the retry engine uses it as the initial interval instead of the registration default
- [X] T039 Run `make pr-ready` from the repo root; fix any lint, license-header, or test failures before marking the branch ready for review
- [X] T040 [P] Verify the quickstart in `specs/026-reconcile-handler/quickstart.md` compiles against the implemented API by tracing each code snippet against the final signatures in `internal/types/types.go`, `internal/cache/accessor.go`, and `internal/status/patch.go`; update any snippets that no longer match

---

## Dependencies & Execution Order

### Phase Dependencies

- **Phase 1 (Setup)**: No dependencies — start immediately
- **Phase 2 (Foundational)**: Depends on Phase 1 — **blocks all user stories**
- **Phase 3 (US1)**: Depends on Phase 2
- **Phase 4 (US2)**: Depends on Phase 2; integrates with Phase 3 output (`Register` returns error)
- **Phase 5 (US3)**: Depends on Phase 2 only — independently testable with `MockStatusClient`
- **Phase 6 (US4)**: Depends on Phase 4 (`Register` returning error is the startup-validation mechanism)
- **Phase 7 (Polish)**: Depends on Phases 3–6

### User Story Dependencies

- **US1 (P1)**: Depends only on Phase 2 — no other story dependencies
- **US2 (P2)**: Depends on Phase 2; `Register` error-return is the only US1 output it consumes (T024 completes what T013 started)
- **US3 (P3)**: Depends on Phase 2 only — fully independent of US1 and US2 at the test level
- **US4 (P4)**: Depends on US2 (T024 must be complete for `Registered` to be meaningful)

### Parallel Opportunities

Within Phase 3: T008–T012 (all tests) can run in parallel; T019 (`CacheAccessor`) can run in parallel with T013–T018.  
Within Phase 4: T020–T023 (all tests) can run in parallel.  
Within Phase 5: T026–T030 (all tests) can run in parallel.  
Within Phase 6: T034–T035 (all tests) can run in parallel.  
Phases 3, 5 can be worked in parallel by different developers after Phase 2 completes.

---

## Parallel Example: User Story 1

```bash
# Write all US1 tests concurrently (different test functions, same file is fine):
Task: T008 — TestReconcileResult_AllFourVariants
Task: T009 — TestManager_TerminalFailure_QuarantinesImmediately
Task: T010 — TestManager_RequeueAfter_DelaysReenqueue
Task: T011 — TestManager_ReconcilerPanic_RecoveredAsTransient
Task: T012 — TestManager_DispatchHeldUntilCacheSynced

# After tests fail, implement in order:
Task: T013 — type-switch in dispatch()
Task: T014 — BackoffHint in retry engine      [parallel with T015, T019]
Task: T015 — PanicError + safeReconcile       [parallel with T014, T019]
Task: T019 — CacheAccessor interface          [parallel with T014, T015]
Task: T016 — wire safeReconcile into dispatch
Task: T017 — syncChecker + Cache field
Task: T018 — cache-sync gate in runDispatchLoop
```

---

## Implementation Strategy

### MVP (User Story 1 only)

1. Phase 1: Setup (T001–T003)
2. Phase 2: Foundational (T004–T007) — `go build ./...` must pass
3. Phase 3: US1 tests (T008–T012, all failing) then implementation (T013–T019)
4. **STOP**: `go test ./tests/contract/` passes; manually verify with a `CategoryTaxonomy` stub reconciler

### Incremental Delivery

1. Phase 2 → Phase 3 (US1): typed dispatch, panic recovery, cache gating — **deploy-ready MVP**
2. Phase 4 (US2): `Register` validation, CRD kind on same path — additive, no breaking changes
3. Phase 5 (US3): `StatusPatch` — additive new package, no changes to dispatch
4. Phase 6 (US4): health surface extension — one field addition
5. Phase 7: polish + `make pr-ready`
