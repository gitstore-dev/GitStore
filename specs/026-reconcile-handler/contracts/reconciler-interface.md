# Contract: Reconciler Interface

**Package**: `github.com/gitstore-dev/gitstore/controller-manager/internal/types`  
**Stability**: Stable (all controller authors implement this)

## Interface

```go
// Reconciler is implemented by controller authors.
// It receives only a WorkItemKey; current resource state MUST be read
// from the injected CacheAccessor at dispatch time (level-triggered).
type Reconciler interface {
    Reconcile(ctx context.Context, req WorkItemKey) ReconcileResult
}
```

## ReconcileResult (sealed interface)

```go
type ReconcileResult interface{ reconcileResult() }

// Success — item removed from queue; last-success timestamp updated.
type Success struct{}

// RequeueAfter — re-enqueue after After elapses; deduplicated.
type RequeueAfter struct { After time.Duration }

// TransientFailure — enter retry cycle with exponential backoff.
// BackoffHint zero = use registration default policy.
// Use for: API timeout, conflict, cache miss, any recoverable error.
type TransientFailure struct { Err error; BackoffHint time.Duration }

// TerminalFailure — quarantine immediately; no retry budget consumed.
// Use for: invalid spec, unresolvable reference, any unrecoverable error.
type TerminalFailure struct { Err error }

// Constructors
func ResultOK() ReconcileResult
func ResultAfter(d time.Duration) ReconcileResult
func ResultTransient(err error, hint ...time.Duration) ReconcileResult
func ResultTerminal(err error) ReconcileResult
```

## WorkItemKey

```go
type WorkItemKey struct {
    Kind      string
    Namespace string
    Name      string
}
```

## Dispatch Contract (caller obligations)

1. The controller manager MUST NOT pass the original event payload to `Reconcile`. Only `WorkItemKey` is passed.
2. `Reconcile` MUST NOT be called until `Cache.HasSynced()` returns `true` for the kind (FR-013).
3. If `Reconcile` panics, the manager MUST recover it, treat it as `TransientFailure`, log it at ERROR level with stack trace, and increment `gitstore_controller_reconcile_total{result="transient_failure"}`.
4. `TerminalFailure` MUST quarantine the item in the same dispatch cycle — zero additional retry attempts.
5. The same `WorkItemKey` MUST NOT be dispatched concurrently to the same reconciler (queue deduplication + Done protocol ensures this).

## Implementor obligations

1. Implementor MUST read current resource state from its injected `CacheAccessor[T]`.
2. If `CacheAccessor.Get` returns `(zero, false)`, the resource has been deleted — return `TerminalFailure` (no retry needed).
3. On successful reconcile, implementor MUST apply a `StatusPatch` with `ObservedGeneration` set to the `metadata.generation` observed at dispatch time.
4. Implementor MUST NOT issue a status patch when `StatusPatch.IsNoOp` returns true (FR-007).
5. On receiving `ErrConflict` from the status-patch call, implementor MUST return `TransientFailure` (FR-006).
6. Implementor MUST NOT modify the cache directly.

## Example

```go
type CategoryTaxonomyReconciler struct {
    cache cache.CacheAccessor[CategoryTaxonomy]
    status StatusClient
}

func (r *CategoryTaxonomyReconciler) Reconcile(ctx context.Context, key types.WorkItemKey) types.ReconcileResult {
    obj, ok := r.cache.Get(key)
    if !ok {
        return types.ResultTerminal(ErrResourceDeleted)
    }
    // ... compute desired state (e.g. build resolved ancestor path) ...
    revision := "main@sha1:" + obj.Meta.ResourceVersion
    patch := &status.StatusPatch{
        ResourceVersion:     obj.Meta.ResourceVersion,
        ObservedGeneration:  &obj.Meta.Generation, // required on success (FR-008)
        LastAppliedRevision: &revision,
        Conditions:          computeConditions(obj),
    }
    if patch.IsNoOp(obj.Status) {
        return types.ResultOK()
    }
    if err := r.status.Apply(ctx, key, patch); err != nil {
        if errors.Is(err, types.ErrConflict) {
            return types.ResultTransient(err)
        }
        return types.ResultTransient(err)
    }
    return types.ResultOK()
}
```
