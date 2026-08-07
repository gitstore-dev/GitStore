# Quickstart: Wiring List-Then-Watch and Checkpointing (spec 036)

## Prerequisites

- `gitstore-controller-manager` built: `go build ./...` from `gitstore-controller-manager/`
- A `ListWatcher[T]` implementation for your resource type. This spec ships no concrete transport (see research.md §1) — for tests, use a hand-written stub; for production, implement `ListWatcher[T]`/`Watcher[T]` against whatever transport the consuming spec chooses (e.g. a GraphQL subscription).
- A `*cache.Cache[T]` and a registered `Reconciler` for the kind (spec 026) — the `Runner` populates the same cache the reconciler reads from.

## Wiring a Kind's List-Then-Watch Loop

### 1. Implement (or stub) a ListWatcher

```go
package mycontroller

import (
    "context"

    "github.com/gitstore-dev/gitstore/controller-manager/internal/listwatch"
)

type MyResource struct {
    Namespace, Name, ResourceVersion string
    // ... resource-specific fields
}

type myListWatcher struct{ /* holds a GraphQL client, gRPC stream, etc. */ }

func (lw *myListWatcher) List(ctx context.Context) (listwatch.ListResponse[MyResource], error) {
    // Fetch a full snapshot + the resourceVersion at snapshot time.
    ...
}

func (lw *myListWatcher) Watch(ctx context.Context, resourceVersion string) (listwatch.Watcher[MyResource], error) {
    // Open a stream starting after resourceVersion. Return ErrWatchExpired-
    // wrapping errors when the cursor has been compacted.
    ...
}
```

### 2. Construct the CheckpointStore once, shared across kinds

```go
// cmd/controller/main.go
store, err := checkpoint.NewFilesystemStore(cfg.Controller.CheckpointDir)
if err != nil {
    log.Fatal("failed to init checkpoint store", zap.Error(err))
}
```

### 3. Register the reconciler (spec 026, unchanged) and construct the Runner

```go
myCache := cache.New[MyResource]()

if err := mgr.Register(manager.ReconcilerRegistration{
    Kind:       "MyResource",
    Reconciler: mycontroller.NewMyReconciler(myCache, statusClient),
    Cache:      myCache, // same cache instance — Runner writes it, Reconciler reads it read-only
}); err != nil {
    log.Fatal("failed to register reconciler", zap.Error(err))
}

runner := &listwatch.Runner[MyResource]{
    Kind:        "MyResource",
    ListWatcher: &myListWatcher{},
    Cache:       myCache,
    Store:       store,
    Enqueue:     func(k types.WorkItemKey) error { return mgr.Enqueue(k) },
    KeyFunc: func(obj MyResource) types.WorkItemKey {
        return types.WorkItemKey{Kind: "MyResource", Namespace: obj.Namespace, Name: obj.Name}
    },
    RevisionFunc:         func(obj MyResource) string { return obj.ResourceVersion },
    FlushIntervalEvents:  cfg.Controller.CheckpointFlushIntervalEvents,
    MaxBackoff:           cfg.Controller.MaxWatchBackoff,
    Log:                  log,
}
```

### 4. Start the Runner alongside the Manager

```go
go func() {
    if err := runner.Run(ctx); err != nil && ctx.Err() == nil {
        log.Error("list-watch runner exited unexpectedly", zap.String("kind", "MyResource"), zap.Error(err))
    }
}()

if err := mgr.Start(ctx); err != nil {
    log.Fatal("manager exited", zap.Error(err))
}
```

`Runner.Run` and `Manager.Start` run concurrently: `Run` populates `myCache` and calls `mgr.Enqueue`; `Manager.Start`'s dispatch loop for `"MyResource"` blocks on `myCache.HasSynced()` until `Run`'s bootstrap (or resume) completes, exactly as it already does for any other cache-sync gate (spec 025/026 — unmodified).

## What Happens On Restart

```
No checkpoint file at cfg.Controller.CheckpointDir/MyResource.checkpoint.json
  → Runner performs a full List, populates the cache, persists the snapshot and replay keys,
    then marks the cache synced and enqueues everything.

Valid checkpoint file present
  → Runner restores the cache snapshot, marks it synced, re-enqueues snapshot resources and
    deletion replay keys, and opens Watch directly at the checkpointed resourceVersion — no re-list.

Checkpoint file missing/corrupt/unreadable/semantically invalid
  → Treated identically to "no checkpoint" — falls back to full List-then-watch (FR-008).
```

## Running Tests

```bash
# From gitstore-controller-manager/
go test ./...                        # all tests
go test ./tests/contract/...         # contract tests, including listwatch_bootstrap/resume/expiry
go test ./tests/checkpoint/...       # filesystem/memory store tests
go test ./internal/...               # unit tests only
```

## Verifying Checkpoint Metrics

```bash
make controller   # starts on :5001
curl -s http://localhost:5001/metrics | grep checkpoint
# gitstore_controller_checkpoint_last_write_timestamp_seconds{kind="MyResource"} 1.7...e+09
# gitstore_controller_checkpoint_write_failures_total{kind="MyResource"} 0
# gitstore_controller_checkpoint_replay_backlog{kind="MyResource"} 0
```

Checkpoint age is derived, not stored directly: `time() - gitstore_controller_checkpoint_last_write_timestamp_seconds{kind="MyResource"}` in a Prometheus query, the standard idiom for "time since last X" gauges.
