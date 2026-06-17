# Research: Reconcile Handler Contract (spec 026)

## 1. ReconcileResult Type Design

**Decision**: Replace the current `(Result, error)` return with a sealed-interface sum type — `ReconcileResult` — that has four concrete variants.

**Rationale**: The existing `type Result struct { Requeue bool; RequeueAfter time.Duration }` paired with an error return was sufficient for spec 025's binary success/failure model. Spec 026 introduces an explicit `TerminalFailure` state (quarantine immediately, no retry) that cannot be represented as an error alone without runtime `errors.As` dispatch and implicit coupling. A sealed interface with an unexported marker method (`reconcileResult()`) makes the four-state contract self-documenting and enables exhaustive handling via type-switch at the dispatch site.

**Design**:
```go
// in internal/types/types.go
type ReconcileResult interface{ reconcileResult() }

type Success          struct{}
type RequeueAfter     struct{ After time.Duration }
type TransientFailure struct { Err error; BackoffHint time.Duration } // BackoffHint zero = default policy
type TerminalFailure  struct{ Err error }
```

Constructor helpers live in the same package:
```go
func ResultOK() ReconcileResult                           { return Success{} }
func ResultAfter(d time.Duration) ReconcileResult         { return RequeueAfter{After: d} }
func ResultTransient(err error, hint ...time.Duration) ReconcileResult { ... }
func ResultTerminal(err error) ReconcileResult            { return TerminalFailure{Err: err} }
```

Reconciler interface changes from `(Result, error)` to `ReconcileResult`:
```go
type Reconciler interface {
    Reconcile(ctx context.Context, req WorkItemKey) ReconcileResult
}
```

**Alternatives considered**: sentinel error (`TerminalError(err)`) + flat struct — used by controller-runtime/Crossplane, works but is implicit and requires `errors.As` at every dispatch site. Rejected in favour of the explicit variant.

---

## 2. StatusPatch Partial-Merge & Optimistic Concurrency

**Decision**: Typed struct with pointer fields; `ResourceVersion` embedded in the struct; client-side idempotency check; `ErrConflict` sentinel in `types`.

**Rationale**:
- Pointer fields (`*bool`, `*int64`, `*string`) are idiomatic Go for "specified vs unspecified" without resorting to `map[string]any`. They're compile-time safe and extensible.
- `ResourceVersion` embedded in `StatusPatch` prevents callers from forgetting to supply it and ensures patch + token travel atomically.
- Client-side idempotency check (FR-007) avoids a network round-trip under steady state (SC-004 requires 100% suppression). The reconciler already holds the observed state from the cache accessor.
- A sentinel `ErrConflict` in `types` (alongside `ErrNotFound`, `ErrQueueShutdown`) keeps the dependency boundary clean; the API's 409-equivalent response is wrapped at the HTTP/GraphQL client layer.

**Design**:
```go
// internal/types/types.go
var ErrConflict = errors.New("optimistic concurrency conflict")

// internal/status/patch.go
// Fields match the common status shape from issue #40 / shared/schemas/.
// resolved is kind-specific and excluded from the generic patch.
type StatusPatch struct {
    ResourceVersion     string
    ObservedGeneration  *int64       // maps to status.observedGeneration
    LastAppliedRevision *string      // e.g. "main@sha1:a1b2c3d"
    Conditions          []*Condition // nil = leave unchanged; non-nil = full replacement
}

func (p *StatusPatch) IsNoOp(current ResourceStatus) bool { ... }
```

**Alternatives considered**: Full-replace semantics — rejected, multiple reconcilers can own distinct status sub-fields without coupling. `map[string]any` — rejected, loses compile-time safety. Including `resolved` in the generic patch — rejected, it is a distinct type per kind (`ResolvedCategoryTaxonomy`, `ResolvedCollectionDefinition`, etc.) and must be handled in kind-specific mutations.

**Deferred: shared `Condition` type across modules**. `gitstore-api` defines the canonical `Condition` struct in `internal/catalog/status.go`. The `internal` path makes it un-importable by `gitstore-controller-manager`, so the controller manager defines its own leaner `Condition` in `internal/status/` (no `validate` tag — that tag encodes API admission logic, not a data shape). Both derive from `shared/schemas/*.graphqls` as the source of truth. Revisit extraction into a shared Go module (e.g. `gitstore-types` under the workspace) if the controller manager ever needs to unmarshal API response bodies directly — at that point the proto types in `shared/proto/` or a new `shared/go/` workspace member would be the right vehicle.

---

## 3. CacheAccessor & Cache-Sync Gating

**Decision**: Generic `CacheAccessor[T]` interface; sync gate in `runDispatchLoop` post-dequeue/pre-submit; `syncChecker` type erasure on `kindState`; `(T, bool)` return.

**Rationale**:
- `CacheAccessor[T]` with unexported-impl wrapper enforces read-only access without polluting the `Reconciler` interface.
- Gate post-dequeue/pre-submit in `runDispatchLoop` (50 ms poll) isolates each kind's blocking; gating at registration time is wrong (cache may not be synced yet); gating inside `dispatch()` wastes a pool worker slot.
- `(T, bool)` is idiomatic (mirrors map lookups, `sync.Map`); `ErrNotFound` is reserved for the queue/requeue API only. Reconciler returns `TerminalFailure` when `ok == false`.
- `syncChecker interface { HasSynced() bool }` on `kindState` erases the type parameter at the manager level; the reconciler captures its typed `CacheAccessor[T]` at constructor time — `any` stays out of the `Reconciler` interface entirely.

**Design**:
```go
// internal/cache/accessor.go
type CacheAccessor[T any] interface { Get(key types.WorkItemKey) (T, bool) }
type readOnlyCache[T any] struct{ c *Cache[T] }
func (r readOnlyCache[T]) Get(key types.WorkItemKey) (T, bool) { return r.c.Get(key) }
func AsReadOnly[T any](c *Cache[T]) CacheAccessor[T] { return readOnlyCache[T]{c} }

// internal/manager/types.go — add to ReconcilerRegistration
Cache syncChecker  // required for FR-013 gating

// internal/manager/manager.go — in runDispatchLoop
for !ks.cache.HasSynced() {
    select {
    case <-ctx.Done(): return
    case <-time.After(50 * time.Millisecond):
    }
}
ks.pool.Submit(...)
```

---

## 4. Hot-Registration & Reconciler Registry

**Decision**: Channel-based registration (`registrationCh chan ReconcilerRegistration`) supervised by a long-lived coordinator goroutine; return error (not panic) for duplicates; Prometheus pre-init inside coordinator critical section.

**Rationale**:
- A channel centralises ownership: only the supervisor goroutine writes to `m.kinds` and calls `wg.Add`, eliminating the TOCTOU race between map-write and goroutine-spawn.
- The supervisor goroutine counts itself in the WaitGroup for its full lifetime, so `wg.Add(1)` for hot-registered kinds is always safe.
- Returning `error` on duplicate is idiomatic (panics are for unrecoverable programmer errors); `Register` returns `error`, main calls `os.Exit(1)`.
- Pre-init Prometheus gauges in the coordinator immediately on write (before spawn), ensuring FR-011 visibility.
- Pre-startup duplicate detection: a preflight `Register` pass validates all static registrations before `Start`; hot-registration sends through the channel and the coordinator validates there too.

**Design**: `Manager.Register(reg ReconcilerRegistration) error` (returns error now); `Manager.HotRegister(reg ReconcilerRegistration) error` sends to `registrationCh`. Both validate for duplicates atomically.

---

## 5. Panic Recovery

**Decision**: Thin `safeReconcile` wrapper closure; `PanicError` struct with `Value any` + `Stack []byte` from `runtime/debug.Stack()`; `errors.As` branch in `dispatch()` for structured log + metric.

**Rationale**:
- `recover()` must be called in a directly-deferred function. A wrapper closure passed to `RunWithRetry` as `fn` is the cleanest approach: it doesn't couple the retry engine to panic semantics, and the panic is converted to an error before the retry engine sees it.
- `debug.Stack()` captures the full goroutine stack including the panicking frame — identical to an unrecovered panic.
- No changes needed to `QuarantineStore` or `PoisonItem`: `PanicError.Error()` returns `"reconciler panic: <value>"` which flows into `PoisonItem.LastError` automatically via the existing `lastErr.Error()` copy.
- Structured log + counter are emitted in `dispatch()` after `RunWithRetry` returns, where kind/namespace/name context is already on the logger.

**New files**: `internal/manager/panic.go` — `PanicError` + `safeReconcile` function only. Zero changes to `RunWithRetry`, `QuarantineStore`, `PoisonItem`.
---

## 6. Feedback-Loop Prevention (FR-008)

**Decision**: Guard on `metadata.generation` vs `status.observedGeneration` at the watch/enqueue layer; the controller manager itself does not need to track generation — it relies on the API's contract.

**Rationale**: The spec's clarification session established that `metadata.generation` is incremented by the API on every spec write (never on status writes). Status-only events from the watch stream will carry the same `metadata.generation` as the last spec write. The controller manager must hold: after a successful reconcile, the reconciler writes `status.observedGeneration = metadata.generation`. On the next status-update event from the watch stream, if `metadata.generation == status.observedGeneration`, the event handler MUST NOT enqueue. This check lives in the watch event handler (spec 027 / issue #182), not in the reconciler loop. This spec only defines that reconcilers MUST write `observedGeneration` on success, and that status-only API responses MUST NOT increment `metadata.generation`.

**Constraint carried into design**: `StatusPatch.ObservedGeneration *int64` is a required field on successful reconcile completion (validated in tests, not enforced by the type).

---

## 7. CRD Extensibility Scope (deferred)

**Decision**: This spec implements a *kind-agnostic dispatch path* — the manager treats kind names as opaque strings and routes work items to whichever reconciler is registered for that name. This naturally supports CRD reconcilers registered alongside core kinds. `HotRegister` (post-startup CRD registration) and the supervisor-channel machinery from the original plan are **deferred** to a future spec.

**Rationale**: Issues #149 (Dynamic GraphQL Schema Synthesis) and #164 (CRD Versioning) are not yet merged, making hot-registration of genuinely new CRD kinds premature. The architectural goal — a uniform interface where the runtime is kind-agnostic — is fully achieved by the static `Register` path. Hot-registration adds concurrency complexity (supervisor goroutine, `wg.Add` mid-run, channel reply protocol) that is not justified until there's a concrete CRD consumer.

**What this spec delivers**: Any reconciler — for a core kind or a CRD kind — registers via `Register()` before `Start()` using the identical API. The dispatch path has zero kind-specific branches.

**What is deferred**: Runtime hot-registration (`HotRegister`), supervisor channel, and FR-012 (`HotRegister` dispatchable within 1 second). These belong in the spec that introduces the first live CRD consumer.

**Broader extensibility note (to revisit)**: Beyond CRDs, an ecommerce engine has extension points that Kubernetes CRDs don't map cleanly to: pricing rules, fulfilment pipelines, tax computations, promotion engines, inventory sync adapters. A future brainstorm should consider whether a plugin/extension model (WASM sandbox, gRPC extension server, or embedded scripting) fits better than Kubernetes-style CRDs for ecommerce-specific extensibility.
