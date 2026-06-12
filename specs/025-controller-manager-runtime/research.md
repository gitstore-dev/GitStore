# Research: Controller Manager Runtime Foundations

**Branch**: `025-controller-manager-runtime` | **Date**: 2026-06-11  
**Spec**: [spec.md](spec.md)

---

## Decision 1: Work Queue with Rate Limiting and Deduplication

**Decision**: Hand-rolled typed generic queue using `chan` + `sync.Mutex`-protected maps, with `golang.org/x/time/rate` for token-bucket rate limiting.

**Rationale**: `k8s.io/client-go/util/workqueue` cannot be vendored standalone — it drags in `k8s.io/apimachinery`, `k8s.io/utils`, `k8s.io/klog/v2`, and the full k8s ecosystem. `golang.org/x/time/rate` has zero non-stdlib dependencies and provides a `Limiter` with `Wait(ctx)` for blocking admission. The dedup queue replicates the k8s `TypedInterface[T]` pattern in ~60 lines: a **dirty set**, a **processing set**, and a **queue slice**.

**How deduplication works**: Three internal structures per queue:
- `queue []T` — ordered FIFO slice of items ready for `Get()`
- `dirty sets.Set[T]` — keys that need processing (single entry per key regardless of how many `Add` calls)
- `processing sets.Set[T]` — keys currently held by a worker

`Add(key)` is a no-op if key is already in `dirty`. If key is in `processing`, it goes into `dirty` but not `queue` (re-enqueued after `Done()`). This means 50 rapid `Add` calls for the same key result in exactly one additional reconcile after the current one finishes.

**Alternatives considered**:
- `k8s.io/client-go/util/workqueue` — disqualified (full k8s ecosystem dependency)
- `github.com/eapache/queue` — no dedup, no rate limiting
- Channel-only — cannot dedup without the dirty/processing map

**Recommended interface**:
```go
type WorkItemKey struct {
    Kind      string
    Namespace string
    Name      string
}

type Queue[T comparable] interface {
    Enqueue(item T) error              // deduplicated; blocks under rate limit
    Dequeue() (item T, shutdown bool)  // blocks until item available
    Done(item T)                       // signals completion; re-enqueues if dirty
    Len() int
    ShutDown()
}
```

**New dependency**: `golang.org/x/time v0.15.0`

---

## Decision 2: Worker Pool Per Kind

**Decision**: `github.com/alitto/pond/v2` — one pool per registered resource kind.

**Rationale**: `pond/v2` is fully generic, supports `Resize(n)` for live reconfiguration without restart (satisfies FR-010), and exports `RunningWorkers()`, `WaitingTasks()`, `FailedTasks()` that feed directly into the health surface. `WithQueueSize(n)` provides bounded backlog with back-pressure. Zero heavy transitive dependencies.

**Alternatives considered**:
- `golang.org/x/sync/errgroup` with `SetLimit(n)` — panics if `SetLimit` is called while goroutines are running; cancels everything on first error (wrong for long-running pool)
- `golang.org/x/sync/semaphore` — requires hand-rolling all lifecycle and metrics
- `github.com/gammazero/workerpool` — no generics, no `Resize`, unbounded internal queue

**New dependency**: `github.com/alitto/pond/v2 v2.7.1`

---

## Decision 3: Retry / Exponential Backoff + Poison-Item Quarantine

**Decision**: `github.com/cenkalti/backoff/v5` for retry engine; stdlib `sync.RWMutex`-protected map for the `QuarantineStore`.

**Rationale**: cenkalti/backoff v5 adds generic `RetryWithData[T]`, `WithContext`, `Permanent(err)` for non-retryable terminal signals, and `RetryNotify` which delivers `(error, time.Duration)` to an observable signal — directly feeding FR-004. The quarantine store is ~20 lines of stdlib. Operator re-queue (Decision 6) removes the key from quarantine and re-enqueues through the rate limiter.

**Alternatives considered**:
- `github.com/avast/retry-go/v4` — `OnRetry` callback doesn't expose the delay value
- Hand-rolled with `time.Sleep` — cenkalti's context-aware cancellation and jitter are non-trivial to replicate correctly

**New dependency**: `github.com/cenkalti/backoff/v5 v5.0.3`

---

## Decision 4: Informer Pattern — In-Memory Cache Populated from Watch Stream

**Decision**: `sync.RWMutex`-protected typed generic map per kind (stdlib only). No third-party library.

**Rationale**: The Kubernetes SharedInformer pattern has three layers:
1. **Reflector** (list+watch) → feeds a delta queue
2. **Delta processor** → updates the local cache first, then fires event handlers
3. **Event handler** → extracts `Name+Namespace` only, enqueues that key into the work queue (discards the event payload)

This is exactly level-triggered semantics: the reconciler receives only a `WorkItemKey`, reads current state from the local cache at dispatch time, and the cache was already updated before the key was enqueued. For `gitstore-controller-manager`, the Watch stream source is the future gRPC event stream (issue #131/#139); the queue accepts items from any caller in this spec.

Key guarantees to preserve (from Kubernetes design):
- Cache is updated **before** the event handler fires, so the reconciler always reads post-event state
- `HasSynced()` must return true before the reconcile loop starts
- Reconcilers must never mutate cached objects (deep-copy before modifying)
- Periodic resync emits synthetic events for all known objects, providing a correctness backstop if events are missed

**Alternatives considered**:
- `go-cache` (`github.com/patrickmn/go-cache`) — untyped `interface{}`, TTL-based eviction (wrong semantics), module marked `+incompatible`
- `sync.Map` — untyped, no event handler callbacks, worse read/write mixed workload performance

**New dependency**: None — stdlib only.

---

## Decision 5: Health Surface

**Decision**: Prometheus `GaugeVec`/`CounterVec` with a `kind` label; `prometheus/client_golang` is already a dependency of `gitstore-api` (v1.23.2) and will be added to `gitstore-controller-manager`.

**Rationale**: `GaugeVec` with `kind` label directly models all required per-kind metrics. Stall detection runs as a background goroutine comparing `last_reconcile_timestamp_seconds{kind}` against a configurable threshold. The health surface is queryable at `/metrics` (Prometheus scrape) and a human-readable `/health` (JSON summary) endpoint over `net/http`.

**Metric definitions**:
```
gitstore_controller_active_workers{kind}          — gauge
gitstore_controller_queue_depth{kind}             — gauge
gitstore_controller_poison_items_total{kind}      — gauge
gitstore_controller_last_reconcile_timestamp{kind} — gauge (Unix seconds)
gitstore_controller_stalled_workers{kind}         — gauge (0=healthy, 1=stalled)
gitstore_controller_reconcile_total{kind,result}  — counter (result: success|error|poison)
```

**New dependency**: `github.com/prometheus/client_golang v1.23.2`

---

## Decision 6: Operator Re-Queue of Poison Items

**Decision**: HTTP `POST /controller/v1/poison/{kind}/{namespace}/{name}/requeue` — `net/http` handler, no new libraries, served on `GITSTORE_CONTROLLER__PORT` (default `5001`). Atomically removes from quarantine store, resets retry budget, re-enqueues through the rate limiter.

**Rationale**: The spec requires explicit operator-initiated re-queue without restart (FR-003a). The implementation acquires the quarantine store mutex, removes the item, resets `RetryRecord.Attempts` and `History` to zero, then calls `queue.Enqueue(key)`. Returns `404` if not in quarantine, `409` if queue is shutting down. `GET /controller/v1/poison/{kind}` lists quarantined items.

**New dependency**: None — `net/http` + existing Prometheus client.

---

## Decision 7: Level-Triggered Reconciliation (Design Principle)

**Decision**: Enforce level-triggered design as a core constraint: reconcilers receive only a `WorkItemKey`, never an event payload.

**Rationale**: From Kubernetes controller-runtime design docs: _"Request does NOT contain information about any specific Event or the object contents itself."_ The reconciler reads current state from the informer cache at dispatch time. This makes reconcilers correct under:
- Event drops (watch reconnect gap)
- Rapid churn (50 events coalesce to one reconcile)
- Process restart (all pending keys re-reconcile from current state)
- Concurrent updates between enqueue and dispatch

**The Reconciler interface** (mirrors `controller-runtime/pkg/reconcile.TypedReconciler`):
```go
type Result struct {
    Requeue      bool
    RequeueAfter time.Duration
}

type Reconciler interface {
    Reconcile(ctx context.Context, req WorkItemKey) (Result, error)
}
```

Returning `error` or `Result{Requeue: true}` triggers backoff-controlled re-enqueue. Returning `backoff.Permanent(err)` (or a typed `TerminalError`) skips backoff and goes straight to quarantine.

---

## Summary: New Dependencies for `gitstore-controller-manager`

| Package | Version | Purpose |
|---|---|---|
| `golang.org/x/time` | v0.15.0 | Queue rate limiting (zero transitive deps) |
| `github.com/alitto/pond/v2` | v2.7.1 | Per-kind worker pools with live resize |
| `github.com/cenkalti/backoff/v5` | v5.0.3 | Exponential backoff + retry notify |
| `github.com/prometheus/client_golang` | v1.23.2 | Per-kind health metrics |

All other areas — typed queue, informer cache, quarantine store, re-queue handler, stall detector — use stdlib only (`sync`, `context`, `net/http`, `time`).

---

## References

- Kubernetes contributor docs — [sig-api-machinery/controllers.md](https://github.com/kubernetes/community/blob/master/contributors/devel/sig-api-machinery/controllers.md)
- controller-runtime reconcile interface — [pkg.go.dev/sigs.k8s.io/controller-runtime/pkg/reconcile](https://pkg.go.dev/sigs.k8s.io/controller-runtime/pkg/reconcile)
- client-go workqueue — [pkg.go.dev/k8s.io/client-go/util/workqueue](https://pkg.go.dev/k8s.io/client-go/util/workqueue)
- "Level Triggering and Reconciliation in Kubernetes" — Red Hat Engineering Blog
- Kubebuilder Book — [book.kubebuilder.io/cronjob-tutorial/controller-overview](https://book.kubebuilder.io/cronjob-tutorial/controller-overview)
