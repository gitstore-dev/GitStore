# Contract: Reconciler Registry API

**Package**: `github.com/gitstore-dev/gitstore/controller-manager/internal/manager`  
**Stability**: Stable (used by controller main and hot-registration callers)

## Registration Types

```go
// ReconcilerRegistration configures a controller for one resource kind.
type ReconcilerRegistration struct {
    Kind       string      // required; sole discriminator
    Reconciler Reconciler  // required

    // Cache is required. Provides HasSynced() for dispatch-loop gating (FR-013).
    Cache syncChecker

    // Retry / pool tuning — zero values use defaults from spec 025.
    MaxAttempts     int
    InitialInterval time.Duration
    MaxInterval     time.Duration
    Multiplier      float64
    StallThreshold  time.Duration
    WorkerCount     int
}
```

## Manager API

```go
// Register wires a reconciler for a kind. Must be called before Start.
// Returns error on duplicate kind, nil Reconciler, or nil Cache.
// Caller MUST treat a non-nil error as fatal and halt startup.
func (m *Manager) Register(reg ReconcilerRegistration) error

// Start begins dispatch loops for all registered kinds.
// Blocks until ctx is cancelled.
func (m *Manager) Start(ctx context.Context) error

// Enqueue adds key to its kind's queue.
// Returns ErrKindNotRegistered if no reconciler is registered for key.Kind.
func (m *Manager) Enqueue(key WorkItemKey) error

// KindStats returns per-kind operational snapshot (health surface, FR-011).
func (m *Manager) KindStats() map[string]KindStat
```

> **HotRegister deferred**: Post-startup CRD hot-registration (FR-012) is deferred pending issues #149 and #164. The dispatch path is already kind-agnostic — CRD reconcilers registered before `Start()` via `Register()` use the identical code path as core kinds.

## KindStat (health surface extension)

```go
// KindStat is extended from spec 025 — Registered field added.
type KindStat struct {
    ActiveWorkers int64  `json:"activeWorkers"`
    QueueDepth    int    `json:"queueDepth"`
    PoisonItems   int    `json:"poisonItems"`
    Stalled       bool   `json:"stalled"`
    Registered    bool   `json:"registered"` // always true if kind appears in the map
}
```

## Registration Rules

| Rule | Behaviour |
|------|-----------|
| Duplicate kind | `Register` returns `fmt.Errorf("kind %q already registered", kind)` |
| Missing `Cache` field | `Register` returns `fmt.Errorf("kind %q: Cache must not be nil", kind)` |
| Missing `Reconciler` field | Returns `fmt.Errorf("kind %q: Reconciler must not be nil", kind)` |
| Unknown kind on `Enqueue` | Returns `ErrKindNotRegistered` |

## Health Surface: /health response (extended)

```json
{
  "status": "ok",
  "kinds": {
    "CategoryTaxonomy": {
      "activeWorkers": 0,
      "queueDepth": 0,
      "poisonItems": 0,
      "stalled": false,
      "registered": true
    }
  }
}
```

## Dispatch-Loop Sync Gate (FR-013)

When a kind's `Cache.HasSynced()` returns `false`, the dispatch loop for that kind polls every 50 ms before submitting any work to the pool. The gate is per-kind — unaffected kinds continue dispatching normally.

```
dequeue(key)
→ for !ks.cache.HasSynced() { sleep 50ms }
→ pool.Submit(dispatch)
```

## Quickstart (hot-registration)

```go
// Hot-registration is deferred. To register a CRD reconciler today, use Register before Start:
if err := mgr.Register(manager.ReconcilerRegistration{
    Kind:       "BackfillJob",   // CRD kind — same API as core kinds
    Reconciler: backfillReconciler,
    Cache:      backfillCache,
}); err != nil {
    log.Fatal("registration failed", zap.Error(err))
}
mgr.Start(ctx)
```
