# Contract: Integration Test Scenarios

**Package**: `github.com/gitstore-dev/gitstore/controller-manager/tests/integration` (new)
**Stability**: New in this spec

Each file below is a `package integration_test` (black-box, mirroring the existing `contract_test` convention in `tests/contract/`). Every test constructs a real `manager.Manager` (and, where relevant, a real `listwatch.Runner[T]` + `checkpoint.Store`) and asserts only on externally observable state, per FR-013: resource status/conditions returned from a fake `status.StatusClient`/cache, `Manager.AllPoisonItems()` / `Manager.QuarantineStore(kind)`, and `health` package Prometheus metrics via `prometheus/client_golang/prometheus/testutil`.

## `reconcile_retry_resume_test.go`

| Test | Maps to | Given | When | Then |
|---|---|---|---|---|
| `TestIntegration_Reconcile_SucceedsOnFirstAttempt` | FR-001 | A resource key is enqueued to a `Manager` with a reconciler that always returns `Success` | The manager dispatches it | `health.ReconcileTotal{result="success"}` increments by 1; no retry occurs (reconciler call count == 1) |
| `TestIntegration_Reconcile_TransientFailureThenSucceeds` | FR-002 | A reconciler fails N times with `TransientFailure` then returns `Success` | The manager retries per its backoff config | Final state is success; call count == N+1; `health.ReconcileTotal{result="transient_failure"}` reflects the failed attempts |
| `TestIntegration_Restart_ResumesFromCheckpoint_NoLostOrDuplicateWork` | FR-003 | A `Runner[T]` bootstraps, persists a checkpoint via a real `checkpoint.Store`, and the process is torn down (context cancelled) mid-run with items still pending | A new `Runner[T]` + `Manager` are constructed against the same `Store` and started | Every item that was pending or changed since the last checkpoint is reconciled exactly once; items already successfully reconciled before shutdown are not redundantly re-dispatched |

## `status_conflict_test.go`

| Test | Maps to | Given | When | Then |
|---|---|---|---|---|
| `TestIntegration_StatusConflict_StaleWriteRejected` | FR-004 (scenario 1) | Two `StatusPatch` submissions for the same key, one captured with a resourceVersion older than the other's committed write | Both are submitted to a fake `status.StatusClient` | The stale submission returns `types.ErrConflict`; the resource's observed status reflects only the newer write |
| `TestIntegration_StatusConflict_ControllerRetriesAfterConflict` | FR-004 (scenario 2) | A reconciler's `StatusClient.Apply` call returns `types.ErrConflict` | The manager's dispatch/retry path handles the result | The controller re-fetches current state and re-attempts reconciliation (not treated as `TerminalFailure`); it eventually reaches `Success` against the current resourceVersion |

## `disconnect_reconnect_test.go`

| Test | Maps to | Given | When | Then |
|---|---|---|---|---|
| `TestIntegration_Disconnect_ReconnectsWithBackoff` | FR-005 (scenario 1) | An active `Runner[T]` watch loop, backed by a fake `ListWatcher[T]` that returns a closed/errored watch stream | The fake simulates a disconnect | The Runner attempts reconnect using its configured backoff (observable via reconnect attempt count/timing in the fake, or via a structured log assertion) |
| `TestIntegration_Disconnect_ResourcesChangedDuringOutageReconciledExactlyOnce` | FR-005 (scenario 2) | A `Runner[T]` disconnects; the fake `ListWatcher[T]` records mutations (Added/Modified/Deleted) that occurred "during" the outage | The Runner reconnects and resumes from its last checkpoint | Every changed resource is dispatched to the `Manager` exactly once; unchanged resources are not re-dispatched |
| `TestIntegration_ReplayWindowExceeded_FallsBackToFullBootstrap` | FR-006 | A `Runner[T]`'s persisted checkpoint's resourceVersion is no longer valid | The fake `ListWatcher[T]`'s `Watch` call returns `listwatch.ErrWatchExpired` | The Runner discards the checkpoint, performs a full List, persists a new checkpoint, and resumes watching — it does not fail permanently |

## `poison_item_test.go`

| Test | Maps to | Given | When | Then |
|---|---|---|---|---|
| `TestIntegration_PoisonedItem_SurfacedAsTerminalFailure` | FR-007 | A reconciler fails every attempt (either `TerminalFailure` immediately, or `TransientFailure` past `MaxAttempts`) | The manager exhausts the retry budget | The item appears in `Manager.AllPoisonItems()` / `Manager.QuarantineStore(kind)`; `health.PoisonItemsTotal{kind}` reflects the count; the item is not silently retried forever |
| `TestIntegration_PoisonedItem_VisibleViaHTTPPoisonAPI` | FR-007, FR-011 | A poisoned item exists in the `Manager` | `GET /controller/v1/poison/{kind}` and `GET /controller/v1/poison/_all` are called against the real handlers from `cmd/controller` (constructed directly in-test, not via a running server) | The response includes the poisoned item's key and last error |

## `observability_test.go`

One test per signal referenced by a runbook (FR-011); see [runbook-signal-contract.md](./runbook-signal-contract.md) for the authoritative signal list each test must cover.

## Non-goals

- No test in this package asserts on unexported struct fields or internal timing beyond what's necessary to synchronize the test (e.g., polling a public getter with a timeout, as the existing `tests/contract/manager_dispatch_test.go` already does).
- No test depends on ScyllaDB, a running `gitstore-api`, or network I/O (FR-012).
