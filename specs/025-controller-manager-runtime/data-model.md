# Data Model: Controller Manager Runtime Foundations

**Branch**: `025-controller-manager-runtime` | **Date**: 2026-06-11

---

## Entities

### WorkItemKey

The identity of a unit of reconciliation work. Passed to the queue and reconciler; never carries event payload data (level-triggered design).

| Field | Type | Constraints |
|---|---|---|
| `Kind` | string | Non-empty; matches a registered reconciler kind |
| `Namespace` | string | Non-empty |
| `Name` | string | Non-empty |

**Uniqueness**: `(Kind, Namespace, Name)` is the deduplication key in the work queue.

---

### WorkItem

An enqueued unit of work. Wraps `WorkItemKey` with retry metadata tracked by the queue.

| Field | Type | Constraints |
|---|---|---|
| `Key` | WorkItemKey | Primary identity |
| `EnqueuedAt` | time.Time | Set at first enqueue |
| `Attempts` | int | Starts at 0; incremented by the retry engine |

---

### RetryRecord

Tracks the full retry history for a work item, written during the backoff loop.

| Field | Type | Constraints |
|---|---|---|
| `Key` | WorkItemKey | Identifies the work item |
| `Attempts` | int | Total attempts made |
| `LastError` | error | Error from the most recent attempt |
| `LastDelay` | time.Duration | Backoff delay applied before last retry |
| `LastAttempt` | time.Time | Timestamp of most recent attempt |
| `History` | []RetryAttempt | Ordered history of all attempts |

**RetryAttempt**:

| Field | Type |
|---|---|
| `AttemptNum` | int |
| `Error` | error |
| `Delay` | time.Duration |
| `Timestamp` | time.Time |

---

### PoisonItem

A `RetryRecord` that has been moved to the quarantine store after exceeding `MaxAttempts`. Held in the `QuarantineStore` until an operator explicitly re-queues it.

Same fields as `RetryRecord`, plus:

| Field | Type | Notes |
|---|---|---|
| `QuarantinedAt` | time.Time | Time the item was moved to quarantine |

**State transitions**:
```
WorkItem (active queue)
    → RetryRecord (retry in progress)
        → PoisonItem (quarantine store, attempts >= MaxAttempts)
            → WorkItem (active queue, operator-triggered requeue, attempts reset to 0)
```

---

### ReconcilerRegistration

Metadata about a registered reconciler, held by the controller manager.

| Field | Type | Notes |
|---|---|---|
| `Kind` | string | The resource kind this reconciler handles |
| `Reconciler` | Reconciler | The reconciler implementation |
| `MaxAttempts` | int | Retry limit before quarantine (configurable per kind) |
| `InitialInterval` | time.Duration | First backoff delay |
| `MaxInterval` | time.Duration | Backoff ceiling |
| `Multiplier` | float64 | Exponential multiplier |
| `StallThreshold` | time.Duration | Max time between reconciles before stall alert |
| `WorkerCount` | int | Initial worker pool size (resizable at runtime) |

---

### ControllerMetrics

In-memory snapshot of per-kind runtime state, projected into Prometheus gauges.

| Field | Type | Prometheus metric |
|---|---|---|
| `Kind` | string | label |
| `ActiveWorkers` | int64 | `gitstore_controller_active_workers` |
| `QueueDepth` | int | `gitstore_controller_queue_depth` |
| `PoisonItemCount` | int | `gitstore_controller_poison_items_total` |
| `LastReconcileTime` | time.Time | `gitstore_controller_last_reconcile_timestamp` |
| `Stalled` | bool | `gitstore_controller_stalled_workers` (0/1) |

---

## State Transitions

```
Enqueue(key)
    ├─ key already dirty   → no-op (deduplication)
    ├─ key in processing   → mark dirty (deferred re-enqueue on Done)
    └─ key new             → dirty + queue

Dequeue()
    → moves key from dirty+queue → processing

Reconcile(ctx, key) → (Result, error)
    ├─ success              → Done(); metrics updated
    ├─ transient error      → retry with backoff; RetryRecord updated
    ├─ exceeded MaxAttempts → PoisonItem to QuarantineStore; Done()
    └─ terminal error       → PoisonItem to QuarantineStore; Done()

Done(key)
    ├─ key still dirty     → re-enqueue once
    └─ key clean           → remove from processing

Requeue(key) [operator-triggered]
    → remove from QuarantineStore
    → reset RetryRecord (Attempts=0, History=nil)
    → Enqueue(key)
```

---

## Interfaces

```go
// Reconciler is implemented by controller authors.
type Reconciler interface {
    Reconcile(ctx context.Context, req WorkItemKey) (Result, error)
}

type Result struct {
    Requeue      bool
    RequeueAfter time.Duration
}

// Queue is the rate-limited, deduplicated work queue.
type Queue interface {
    Enqueue(key WorkItemKey) error
    Dequeue() (WorkItemKey, bool) // bool=false means shutdown
    Done(key WorkItemKey)
    Len() int
    ShutDown()
}

// QuarantineStore holds poison items.
type QuarantineStore interface {
    Put(item *PoisonItem)
    Get(key WorkItemKey) (*PoisonItem, bool)
    Delete(key WorkItemKey)
    List(kind string) []*PoisonItem // kind="" returns all
    Len() int
}

// InformerCache is the per-kind local read cache.
type InformerCache[T any] interface {
    Set(key WorkItemKey, obj T)
    Get(key WorkItemKey) (T, bool)
    Delete(key WorkItemKey)
    List() []T
    HasSynced() bool
}

// RequeueHandler is the HTTP handler for operator-triggered re-queue.
type RequeueHandler interface {
    Requeue(ctx context.Context, key WorkItemKey) error
    ListPoisonItems(ctx context.Context, kind string) ([]*PoisonItem, error)
}
```
