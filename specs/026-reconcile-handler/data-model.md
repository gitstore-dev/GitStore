# Data Model: Reconcile Handler Contract (spec 026)

## Entities

### ReconcileResult (sum type / sealed interface)

**Package**: `github.com/gitstore-dev/gitstore/controller-manager/internal/types`

**Description**: The typed outcome of a reconciler invocation. Replaces the current `(Result, error)` pair.

| Variant | Fields | When used |
|---------|--------|-----------|
| `Success` | — | Reconcile completed; remove from queue, update last-success timestamp |
| `RequeueAfter` | `After time.Duration` | Re-enqueue after specified delay (deduplicated) |
| `TransientFailure` | `Err error`, `BackoffHint time.Duration` | All transient/infrastructure errors; enters retry cycle. `BackoffHint == 0` uses default policy |
| `TerminalFailure` | `Err error` | Unrecoverable resource-level error; quarantine immediately, no retry |

**Sealed marker**: unexported method `reconcileResult()` on each variant — prevents external packages defining additional variants.

**Invariants**:
- `TransientFailure.Err` MUST be non-nil
- `TerminalFailure.Err` MUST be non-nil
- `RequeueAfter.After` MUST be > 0

---

### WorkItemKey (unchanged from spec 025)

**Package**: `internal/types`

```
WorkItemKey {
  Kind      string  // resource kind name; sole discriminator for reconciler lookup
  Namespace string
  Name      string
}
```

---

### CacheAccessor[T]

**Package**: `internal/cache`

**Description**: Read-only view of the per-kind informer cache exposed to reconcilers. Generic over the resource type `T`.

| Method | Signature | Behaviour |
|--------|-----------|-----------|
| `Get` | `(key WorkItemKey) (T, bool)` | Returns `(zero, false)` if resource absent (NotFound). Reconciler MUST treat `ok == false` as deletion — return `TerminalFailure`, no retry |

**Relationships**:
- `*cache.Cache[T]` implements `CacheAccessor[T]` structurally
- `readOnlyCache[T]` wrapper prevents type-assertion escapes (unexported, constructable via `cache.AsReadOnly[T](c)`)

**NotFound semantics**: `(zero, false)` — not a sentinel error. Reconciler code: `if _, ok := accessor.Get(key); !ok { return types.ResultTerminal(ErrResourceDeleted) }`.

---

### syncChecker (internal interface)

**Package**: `internal/manager`

**Description**: Type-erased interface for cache-sync gating. Used on `kindState` so the manager holds heterogeneous per-kind caches without carrying the type parameter.

```go
type syncChecker interface {
    HasSynced() bool
}
```

`*cache.Cache[T]` satisfies `syncChecker` via its existing `HasSynced() bool` method.

---

### StatusPatch

**Package**: `internal/status`  (new package)

**Description**: Partial-merge update applied to a resource's `.status` sub-resource. Fields match the common status shape shared by all core GitStore resource kinds (from issue #40 / `shared/schemas/`).

| Field | Type | Notes |
|-------|------|-------|
| `ResourceVersion` | `string` | Required. Current `metadata.resourceVersion` from cache. Sent as optimistic-lock token |
| `ObservedGeneration` | `*int64` | MUST be set on successful reconcile to suppress feedback loops (FR-008). Maps to `status.observedGeneration` in the GraphQL schema |
| `LastAppliedRevision` | `*string` | Git revision of the last successfully applied push, e.g. `main@sha1:a1b2c3d`. `nil` = leave unchanged |
| `Conditions` | `[]*Condition` | Full replacement of the conditions slice when non-nil; `nil` = leave unchanged |

> `resolved` is kind-specific (each kind has a distinct `Resolved*` type) and is NOT part of the generic patch. Reconcilers that need to write `resolved` do so via a kind-specific status mutation.

**Partial-merge**: Only non-nil/non-empty pointer fields are included in the API request. All other status fields are left unchanged.

**Idempotency** (`IsNoOp`): Before issuing the API call, compare each non-nil field to the current observed value from the cache. If all are equal, skip the call entirely (FR-007, SC-004).

**Conflict handling**: API rejects the patch when `ResourceVersion` does not match. The status-update client wraps this into `types.ErrConflict`. Callers propagate as `TransientFailure`.

**State transitions**:
```
Reconciler reads cache → constructs StatusPatch (ResourceVersion = observed)
→ IsNoOp? → skip (FR-007)
→ call API → conflict? → ErrConflict → TransientFailure (FR-006)
           → success  → log success, update lastSuccess
```

---

### Condition

**Package**: `internal/status`

Mirrors the common condition shape from the GraphQL schema (issue #40).

| Field | Type | Notes |
|-------|------|-------|
| `Type` | `string` | Condition type name (kind-specific, e.g. `PUBLISHED`, `ADMISSION_ACCEPTED`) |
| `Status` | `string` | `"True"`, `"False"`, or `"Unknown"` |
| `ObservedGeneration` | `int64` | Generation this condition was computed from |
| `LastTransitionTime` | `time.Time` | When the condition last changed |
| `Reason` | `string` | Short machine-readable reason code |
| `Message` | `string` | Human-readable detail |

---

### ReconcilerRegistry

**Package**: `internal/manager` (within `Manager`)

**Description**: Holds the `kind → *kindState` mapping. Consulted before every dispatch.

| Method | Signature | Behaviour |
|--------|-----------|-----------|
| `Register` | `(ReconcilerRegistration) error` | Pre-startup registration. Returns error on duplicate kind |
| `HotRegister` | `(ReconcilerRegistration) error` | Post-startup registration. Sends to `registrationCh`; coordinator validates and spawns dispatch goroutine |
| `KindStats` | `() map[string]KindStat` | Returns snapshot for health surface (FR-011) — reads under RWMutex |

**Duplicate detection**: Both `Register` and `HotRegister` must check for existing kind entry. On duplicate: return `fmt.Errorf("kind %q already registered", kind)`. Callers call `os.Exit(1)` for fatal startup violations.

---

### PanicError

**Package**: `internal/manager`

**Description**: Error type wrapping a reconciler panic for structured logging and metric emission.

| Field | Type | Notes |
|-------|------|-------|
| `Value` | `any` | The recovered panic value |
| `Stack` | `[]byte` | Output of `runtime/debug.Stack()` at recovery time |

`PanicError.Error()` returns `"reconciler panic: <value>"` — this flows automatically into `PoisonItem.LastError`.

---

### ReconcilerRegistration (extended)

**Package**: `internal/manager`

Added field over spec 025:

| Field | Type | Notes |
|-------|------|-------|
| `Cache` | `syncChecker` | Required. The per-kind informer cache. Used for FR-013 sync-gating in dispatch loop |

All other fields unchanged from spec 025.

---

### kindState (internal, extended)

Added field:

| Field | Type | Notes |
|-------|------|-------|
| `cache` | `syncChecker` | Held for dispatch-loop sync gating |

---

## Errors (types package)

| Sentinel | Existing? | Notes |
|----------|-----------|-------|
| `ErrNotFound` | ✅ | Queue/requeue API only — NOT used by CacheAccessor |
| `ErrQueueShutdown` | ✅ | Unchanged |
| `ErrKindNotRegistered` | ✅ | Unchanged |
| `ErrConflict` | 🆕 | Optimistic-concurrency conflict from status-patch API call |

## Status Field Reference (from issue #40 / `shared/schemas/`)

All core GitStore resource kinds carry these status fields. `resolved` is kind-specific and excluded from the generic patch.

| Field | GraphQL type | Notes |
|-------|-------------|-------|
| `observedGeneration` | `Int!` | Generation of spec this status was computed from |
| `lastAppliedRevision` | `String` / `String!` (kind-dependent) | Git revision of last admitted push |
| `conditions` | `[*Condition!]!` | Named condition set; replaces full slice on patch |
| `resolved` | Kind-specific | e.g. `ResolvedCategoryTaxonomy`, `ResolvedCollectionDefinition` — NOT in generic patch |

---

## Relationships Diagram

```
Manager
  ├─ kinds: map[string]*kindState       (RWMutex-guarded)
  │    ├─ reg: ReconcilerRegistration
  │    │    ├─ Reconciler               (interface — implemented by controller authors)
  │    │    └─ Cache: syncChecker       (type-erased *cache.Cache[T])
  │    ├─ q: *queue.Queue
  │    ├─ pool: *worker.Pool
  │    └─ quarantine: *retry.QuarantineStore
  │
  ├─ registrationCh: chan ReconcilerRegistration  (for HotRegister)
  └─ wg: sync.WaitGroup                (supervisor holds count for full lifetime)

Reconciler.Reconcile(ctx, WorkItemKey) ReconcileResult
  └─ returns one of: Success | RequeueAfter | TransientFailure | TerminalFailure

StatusPatch  (constructed by reconciler, sent to internal/status client)
  └─ ResourceVersion + pointer fields → IsNoOp check → API call → ErrConflict or success
```
