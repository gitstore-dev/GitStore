# Data Model: Controller Integration Tests + Operations Runbook

**Feature**: [spec.md](./spec.md) | **Plan**: [plan.md](./plan.md)

This feature adds no persistent data model or new production entities — it exercises and documents entities already defined in `gitstore-controller-manager`. This document maps the spec's Key Entities to their existing concrete types so tests and runbooks reference the real names.

## Entity Mapping

| Spec Key Entity | Concrete Type | Location | Notes |
|---|---|---|---|
| Reconcile Outcome | `types.ReconcileResult` (sealed: `Success`, `RequeueAfter`, `TransientFailure`, `TerminalFailure`) | `internal/types/types.go:23` | Attempt count is tracked by `retry.PoisonItem.Attempts` once quarantined; in-flight attempt count lives in the `retry` package's backoff loop, not a persisted field. |
| Checkpoint | `checkpoint.Record{Kind, ResourceVersion, Snapshot, ReplayKeys, WrittenAt}` | `internal/checkpoint/checkpoint.go:20` | Persisted via `checkpoint.Store.Save`; `MemoryStore` and `FilesystemStore` are the two existing implementations under test. |
| Status Condition | `status.Condition{Type, Status, ObservedGeneration, LastTransitionTime, Reason, Message}` | `internal/status/patch.go:16` | Written as part of a `status.StatusPatch`; conflict detection surfaces as `types.ErrConflict` from `status.StatusClient.Apply`. |
| Runbook | New markdown documents (no Go type) | `docs/runbooks/*.md` | Structure: Symptom → Diagnostic Steps → Recovery Actions → Verification (see research.md). |
| Observability Signal | Prometheus metrics in `health` package: `QueueDepth`, `ActiveWorkers`, `PoisonItemsTotal`, `StalledWorkers`, `ReconcileTotal`, `CheckpointLastWriteTimestamp`, `CheckpointWriteFailuresTotal`, `CheckpointReplayBacklog` | `internal/health/metrics.go` | Also includes the poison-item HTTP API (`GET /controller/v1/poison/{kind}`, `GET /controller/v1/poison/_all`, `POST /controller/v1/poison/{namespace}/{kind}/{name}/requeue`) registered in `cmd/controller/main.go`. |

## Supporting Types Used by Tests (no changes)

- `manager.ReconcilerRegistration{Kind, Reconciler, Cache, MaxAttempts, InitialInterval, MaxInterval, Multiplier, StallThreshold, WorkerCount}` — `internal/manager/types.go:32`. Tests register fake reconcilers per scenario the same way `tests/contract/manager_dispatch_test.go` does.
- `retry.PoisonItem{Key, Attempts, LastError}` — `internal/retry/quarantine.go`. Read via `Manager.QuarantineStore(kind)` / `Manager.AllPoisonItems()`.
- `listwatch.Runner[T]{Kind, ListWatcher, Cache, Store, Enqueue, KeyFunc, RevisionFunc, FlushIntervalEvents, MaxBackoff, Log}` — `internal/listwatch/runner.go:50`. Tests supply a fake `ListWatcher[T]` (same pattern as `tests/contract/listwatch_resume_test.go`) to simulate list/watch/disconnect/expiry behavior deterministically.
- `listwatch.ErrWatchExpired` — `internal/listwatch/types.go:47`. Injected by the fake `ListWatcher[T]` to trigger replay-window-exceeded fallback (FR-006).

## State Transitions Relevant to Test Assertions

**Reconcile Outcome lifecycle** (per work item, driven by `manager.dispatch`/`handleTransient` in `internal/manager/manager.go`):

```
enqueued → dispatched → Success                         (FR-001)
enqueued → dispatched → TransientFailure → retry(N) → Success   (FR-002)
enqueued → dispatched → TransientFailure → retry(MaxAttempts) → quarantined (FR-007)
enqueued → dispatched → TerminalFailure → quarantined immediately (FR-007, no retry budget consumed)
```

**Checkpoint lifecycle** (driven by `listwatch.Runner[T].Run` in `internal/listwatch/runner.go`):

```
(no record) → List → Save(checkpoint) → Watch → [event]* → periodic Save   (bootstrap, FR-001 precondition)
Save(checkpoint) → process restart → Load(checkpoint) → resume Watch(from ResourceVersion), skip List   (FR-003)
Watch disconnected → reconnect with backoff → resume Watch(from currentRV)                              (FR-005)
Watch cursor expired (ErrWatchExpired) → discard checkpoint → re-List → Save(new checkpoint) → Watch      (FR-006)
```

**Status write lifecycle** (driven by `status.StatusClient.Apply` contract):

```
reconcile success → build StatusPatch(ResourceVersion=observed) → Apply()
  → success: status updated
  → ErrConflict (stale ResourceVersion): re-fetch current resource → re-run reconcile → Apply() again   (FR-004)
```

No new state machines, fields, or persisted entities are introduced by this feature.
