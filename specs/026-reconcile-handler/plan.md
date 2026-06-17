# Implementation Plan: Reconcile Handler Contract for Core and CRD Kinds

**Branch**: `026-reconcile-handler` | **Date**: 2026-06-12 | **Spec**: [spec.md](spec.md)  
**Input**: Feature specification from `/specs/026-reconcile-handler/spec.md`

## Summary

Define and implement the typed reconciler interface contract for the GitStore controller manager, extending the runtime foundations from spec 025. The primary change is replacing the current implicit `(Result, error)` reconciler return with a sealed four-variant `ReconcileResult` type (`Success`, `TransientFailure`, `TerminalFailure`, `RequeueAfter`), adding a typed `CacheAccessor[T]` read-only cache view, a `StatusPatch` partial-merge status-update mechanism with optimistic concurrency, panic recovery, and hot-registration support for CRD kinds.

## Technical Context

**Language/Version**: Go 1.25  
**Primary Dependencies**: `go.uber.org/zap`, `github.com/cenkalti/backoff/v5 v5.0.3`, `github.com/prometheus/client_golang v1.23.2`, `github.com/alitto/pond/v2 v2.7.1`, `runtime/debug` (stdlib — for stack traces)  
**Storage**: In-memory only (`sync.RWMutex` maps) — no persistence added in this spec  
**Testing**: `go test ./...`; contract tests in `tests/contract/`, unit tests co-located in `internal/`  
**Target Platform**: Linux server (controller manager binary)  
**Project Type**: Library + service (internal Go packages consumed by `cmd/controller/main.go`)  
**Performance Goals**: No-op reconcile path under 500 ms (SC-001); hot-registration dispatchable within 1 second (SC-005)  
**Constraints**: Zero manager crashes from reconciler panics (SC-002); 100% status-patch suppression under steady state (SC-004)  
**Scale/Scope**: One manager process, multiple registered kinds (core + CRD), O(100s) concurrent reconcile items

## Constitution Check

*GATE: Must pass before Phase 0 research. Re-check after Phase 1 design.*

| Principle | Status | Notes |
|-----------|--------|-------|
| I. Test-First | ✅ | Contract tests written before implementation. Existing tests in `tests/contract/` updated; new tests for ReconcileResult variants, panic recovery, cache-sync gating, hot-registration, status-patch suppression |
| II. API-First | ✅ | `contracts/` directory defines interface before implementation. `ReconcileResult` sealed interface, `CacheAccessor[T]`, `StatusClient`, `ReconcilerRegistry` API all specified |
| III. Clear Contracts | ✅ | All interfaces in `internal/types`, `internal/cache`, `internal/status`, `internal/manager` follow existing module versioning. No breaking changes to existing `Reconciler` callers without migration path |
| IV. Observability | ✅ | Structured ERROR log + `gitstore_controller_reconcile_total{result="transient_failure"}` on panic (FR-004); `registered` field on `KindStat` (FR-011) |
| V. User Story Driven | ✅ | P1=reconciler interface, P2=CRD registration, P3=status writeback, P4=startup observability. All tasks labelled US1–US4 |
| VI. Incremental Delivery | ✅ | P1 (interface + dispatch changes) delivers immediately. P2 (CRD) and P3 (status) are additive |
| VII. Simplicity/YAGNI | ✅ | No new external dependencies. `internal/status` is a new package but justified by FR-006/FR-007. Hot-registration via channel — no external registry dependency |

**Post-design re-check**: No violations. The `internal/status` package is justified by the new `StatusPatch` entity (FR-006, FR-007). No fourth project introduced. All additions are additive to the existing `gitstore-controller-manager` module.

## Project Structure

### Documentation (this feature)

```text
specs/026-reconcile-handler/
├── plan.md              ✅ (this file)
├── research.md          ✅ Phase 0 output
├── data-model.md        ✅ Phase 1 output
├── quickstart.md        ✅ Phase 1 output
├── contracts/
│   ├── reconciler-interface.md     ✅ Phase 1 output
│   ├── reconciler-registry-api.md  ✅ Phase 1 output
│   └── status-patch-api.md         ✅ Phase 1 output
└── tasks.md             ⬜ Phase 2 output (/speckit.tasks command)
```

### Source Code

```text
gitstore-controller-manager/
├── internal/
│   ├── types/
│   │   └── types.go                  MODIFY: add ReconcileResult sealed interface + 4 variants
│   ├── cache/
│   │   ├── cache.go                  unchanged
│   │   └── accessor.go               NEW: CacheAccessor[T] interface + AsReadOnly[T]
│   ├── status/
│   │   └── patch.go                  NEW: StatusPatch, StatusClient interface, IsNoOp, ResourceStatus
│   ├── manager/
│   │   ├── types.go                  MODIFY: add Cache syncChecker to ReconcilerRegistration
│   │   ├── manager.go                MODIFY: Register returns error; HotRegister; sync-gate in dispatch; panic recovery; ReconcileResult type-switch
│   │   ├── panic.go                  NEW: PanicError + safeReconcile wrapper
│   │   ├── errors.go                 unchanged
│   │   └── logger.go                 unchanged
│   ├── health/
│   │   └── handler.go                MODIFY: KindStat.Registered field added
│   ├── retry/
│   │   ├── retry.go                  MODIFY: accept ReconcileResult.BackoffHint
│   │   └── quarantine.go             unchanged
│   └── queue/, worker/, config/,
│       api/                          unchanged
├── tests/
│   └── contract/
│       ├── reconciler_contract_test.go   MODIFY: add ReconcileResult variant tests
│       ├── manager_dispatch_test.go       MODIFY: add TerminalFailure, panic, sync-gate tests
│       ├── cache_contract_test.go         MODIFY: add CacheAccessor read-only tests
│       ├── level_triggered_test.go        unchanged (already tests cache-reading pattern)
│       ├── retry_quarantine_test.go       MODIFY: add BackoffHint test
│       ├── quarantine_contract_test.go    unchanged
│       ├── health_test.go                 MODIFY: add registered=true assertion
│       └── status_patch_test.go           NEW: IsNoOp, conflict, observedGeneration tests
└── cmd/
    └── controller/
        └── main.go                   MODIFY: Register returns error → fatal on error
```

**Structure Decision**: Single project (`gitstore-controller-manager`). All new code is additive within existing packages or new internal packages (`cache/accessor.go`, `status/patch.go`, `manager/panic.go`). No new external dependencies required.

## Complexity Tracking

No constitution violations. No complexity justifications required.

## Implementation Phases

### Phase 1 — Core Interface Change (US1 — P1)

Goal: Replace `(Result, error)` with `ReconcileResult` sealed interface. Update dispatch engine to type-switch on variants.

**Files**: `internal/types/types.go`, `internal/manager/manager.go`, `internal/manager/types.go`, `internal/retry/retry.go`

**Key changes**:
1. Define `ReconcileResult` sealed interface + `Success`, `RequeueAfter`, `TransientFailure`, `TerminalFailure` variants with constructors in `types.go`.
2. Change `Reconciler.Reconcile` signature from `(Result, error)` to `ReconcileResult`.
3. Update `dispatch()` in `manager.go` to type-switch instead of checking `err != nil`. `TerminalFailure` quarantines immediately without consuming retry budget.
4. Thread `BackoffHint` from `TransientFailure` into `RunWithRetry` (override `InitialInterval` for that attempt's backoff).
5. Update all existing `stubReconciler`/`countingReconciler` stubs in tests to return `types.ResultOK()`.

**Test-first**: Add `TestReconcileResult_Variants` (all four constructors), `TestManager_TerminalFailure_QuarantinesImmediately`, `TestManager_RequeueAfter_DelaysReenqueue` to `tests/contract/` — write failing tests first.

### Phase 2 — Panic Recovery (US1 — P1)

Goal: Reconciler panics are recovered and treated as TransientFailure.

**Files**: `internal/manager/panic.go` (new), `internal/manager/manager.go`

**Key changes**:
1. `PanicError` struct with `Value any` + `Stack []byte`.
2. `safeReconcile(r Reconciler, key WorkItemKey) func(context.Context) ReconcileResult` — deferred `recover()` returns `ResultTransient(&PanicError{...})`.
3. Replace direct `ks.reg.Reconciler.Reconcile` call with `safeReconcile(...)` in `dispatch()`.
4. After `RunWithRetry` returns, `errors.As`-check for `*PanicError` → structured ERROR log + `health.ReconcileTotal.WithLabelValues(kind, "transient_failure").Inc()`.

**Test-first**: `TestManager_ReconcilerPanic_RecoveredAsTransient`, `TestManager_ReconcilerPanic_LogsStackTrace`.

### Phase 3 — CacheAccessor & Sync Gating (US1/US2 — P1/P2)

Goal: Typed read-only cache view; dispatch held until `HasSynced()`.

**Files**: `internal/cache/accessor.go` (new), `internal/manager/types.go`, `internal/manager/manager.go`

**Key changes**:
1. `CacheAccessor[T]` interface + `readOnlyCache[T]` + `AsReadOnly[T]()` in `cache/accessor.go`.
2. `syncChecker` interface in `manager` package.
3. `Cache syncChecker` field on `ReconcilerRegistration`.
4. Pre-dispatch spin loop in `runDispatchLoop`: poll `ks.cache.HasSynced()` every 50 ms, return on `ctx.Done()`.

**Test-first**: `TestManager_DispatchHeldUntilCacheSynced`, `TestCacheAccessor_ReadOnly` (ensure write methods not accessible), `TestCacheAccessor_NotFound_IsTerminal`.

### Phase 4 — Registry Validation & Health Surface (US2/US4 — P2/P4)

Goal: `Manager.Register` returns error on invalid/duplicate registration; registered kinds appear in `/health`.

**Scope note**: `HotRegister` and the supervisor-channel machinery are deferred — they require #149 and #164. The kind-agnostic dispatch path (no kind-specific branches) already supports CRD reconcilers registered before `Start()` via the same `Register()` API as core kinds. See research.md §7.

**Files**: `internal/manager/manager.go`, `internal/manager/types.go`, `cmd/controller/main.go`, `internal/health/handler.go`

**Key changes**:
1. `Manager.Register(reg) error` — validate non-nil `Reconciler` and `Cache`; check for duplicate kind; return descriptive error, do not panic.
2. `main.go`: wrap `mgr.Register(...)` calls in `if err != nil { log.Fatal(...) }`.
3. Add `Registered bool` to `KindStat`; always `true` for any kind in the map (FR-011).
4. `/health` response includes `"registered": true` per kind.

**Test-first**: `TestManager_DuplicateRegistration_ReturnsError`, `TestManager_NilReconciler_ReturnsError`, `TestManager_NilCache_ReturnsError`, `TestManager_KindNotRegistered_EmitsSignal`, `TestHealth_RegisteredKindsListed`.

### Phase 5 — StatusPatch (US3 — P3)

Goal: Typed partial-merge status update with optimistic concurrency and idempotency suppression.

**Files**: `internal/status/patch.go` (new), `internal/types/types.go` (add `ErrConflict`)

**Key changes**:
1. `StatusPatch` struct: `ResourceVersion string`, `ObservedGeneration *int64`, `LastAppliedRevision *string`, `Conditions []*Condition` (matches common status shape from issue #40 / `shared/schemas/`). `resolved` excluded — kind-specific.
2. `Condition` struct mirroring the GraphQL condition shape (`Type`, `Status`, `ObservedGeneration`, `LastTransitionTime`, `Reason`, `Message`).
3. `IsNoOp(current ResourceStatus) bool` — field-by-field comparison of non-nil patch fields.
4. `StatusClient` interface: `Apply(ctx, key, *StatusPatch) error`.
5. `ErrConflict = errors.New("optimistic concurrency conflict")` in `types.go`.
6. `MockStatusClient` test double for contract tests (does not make network calls).

**Test-first**: `TestStatusPatch_IsNoOp_AllMatch`, `TestStatusPatch_IsNoOp_FieldDiffers`, `TestStatusPatch_Conflict_ReturnsErrConflict`, `TestStatusPatch_ObservedGenerationSet` — all in `tests/contract/status_patch_test.go`.

### Phase 6 — Health Surface Extension (US4 — P4)

Goal: Registered kinds listed in `/health` response; startup duplicate detection halts manager.

**Files**: `internal/health/handler.go`

**Key changes**:
1. `KindStat.Registered bool` — set to `true` for all kinds present in the registry.
2. `/health` JSON response now includes `"registered": true` per kind.
3. Startup validation (already covered in Phase 4 via `Register` returning error).

**Test-first**: `TestHealth_RegisteredKindsListed` — update `tests/contract/health_test.go`.
