# Quickstart: Implementing a Reconciler (spec 026)

## Prerequisites

- `gitstore-controller-manager` built: `go build ./...` from `gitstore-controller-manager/`
- A running `gitstore-api` (for status writeback in production; not needed for unit tests)

## Writing a Reconciler

### 1. Implement the Reconciler interface

```go
package mycontroller

import (
    "context"
    "errors"

    "github.com/gitstore-dev/gitstore/controller-manager/internal/cache"
    "github.com/gitstore-dev/gitstore/controller-manager/internal/status"
    "github.com/gitstore-dev/gitstore/controller-manager/internal/types"
)

// MyResource is the resource type held in the cache.
// ObjectMeta and the Status sub-field are defined by the reconciler author.
type MyResource struct {
    ResourceVersion string
    Generation      int64
    // ... resource-specific fields
    Status status.ResourceStatus
}

type MyReconciler struct {
    cache  cache.CacheAccessor[MyResource]
    client status.StatusClient
}

func NewMyReconciler(c *cache.Cache[MyResource], s status.StatusClient) *MyReconciler {
    return &MyReconciler{cache: cache.AsReadOnly(c), client: s}
}

func (r *MyReconciler) Reconcile(ctx context.Context, key types.WorkItemKey) types.ReconcileResult {
    obj, ok := r.cache.Get(key)
    if !ok {
        // Resource deleted between enqueue and dispatch — terminal, no retry.
        return types.ResultTerminal(errors.New("resource deleted"))
    }

    // Compute desired state.
    revision := "main@sha1:" + obj.ResourceVersion
    conditions := computeConditions(obj)

    // Build status patch.
    patch := &status.StatusPatch{
        ResourceVersion:     obj.ResourceVersion,
        ObservedGeneration:  &obj.Generation, // REQUIRED on success (FR-008)
        LastAppliedRevision: &revision,
        Conditions:          conditions,
    }

    // Skip write if nothing changed.
    if patch.IsNoOp(obj.Status) {
        return types.ResultOK()
    }

    // Apply status patch.
    if err := r.client.Apply(ctx, key, patch); err != nil {
        if errors.Is(err, types.ErrConflict) {
            return types.ResultTransient(err) // retry with fresh cache state
        }
        return types.ResultTransient(err)
    }

    return types.ResultOK()
}
```

### 2. Register at startup

```go
// cmd/controller/main.go

myCache := cache.New[MyResource]()
myReconciler := mycontroller.NewMyReconciler(myCache, statusClient)

mgr := manager.New().WithLogger(logger)

if err := mgr.Register(manager.ReconcilerRegistration{
    Kind:       "MyResource",
    Reconciler: myReconciler,
    Cache:      myCache,       // required for sync-gating (FR-013)
}); err != nil {
    logger.Fatal("failed to register reconciler", zap.Error(err))
}

if err := mgr.Start(ctx); err != nil {
    logger.Fatal("manager exited", zap.Error(err))
}
```

### 3. Populate and sync the cache

Before dispatching any items, mark the cache synced after the initial list:

```go
// In your watch/list handler (spec 027 / issue #182)
for _, obj := range initialList {
    key := types.WorkItemKey{Kind: "MyResource", Namespace: obj.Namespace, Name: obj.Name}
    myCache.Set(key, obj)
}
myCache.MarkSynced() // FR-013: dispatch loop unblocks for this kind
```

### 4. Register a CRD kind before Start

CRD reconcilers use the same `Register` API as core kinds — there is no separate hot-registration path in this release (deferred to a future spec):

```go
if err := mgr.Register(manager.ReconcilerRegistration{
    Kind:       "BackfillJob",
    Reconciler: backfillReconciler,
    Cache:      backfillCache,
}); err != nil {
    logger.Fatal("failed to register BackfillJob reconciler", zap.Error(err))
}
// Call mgr.Start(ctx) after all Register calls.
```

## Returning Results

```go
types.ResultOK()                          // success — item dequeued
types.ResultTerminal(err)                 // quarantine immediately — no retry
types.ResultTransient(err)                // retry with default backoff
types.ResultTransient(err, 5*time.Second) // retry, but hint a 5s initial backoff
types.ResultAfter(10*time.Minute)         // re-enqueue after 10 minutes
```

## Running Tests

```bash
# From gitstore-controller-manager/
go test ./...                       # all tests
go test ./tests/contract/...       # contract tests only
go test ./internal/...             # unit tests only
```

## Verifying Health Surface

```bash
make controller   # starts on :5001
curl http://localhost:5001/health | jq .
# {
#   "status": "ok",
#   "kinds": {
#     "MyResource": { "activeWorkers": 0, "queueDepth": 0, "poisonItems": 0, "stalled": false, "registered": true }
#   }
# }
```
