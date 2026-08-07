# Contract: Runbook → Signal Mapping

**Stability**: New in this spec

Per FR-011 and SC-003, every signal a runbook instructs an engineer to check MUST be validated by at least one test in `tests/integration/observability_test.go`. This table is the authoritative cross-reference — each row must have a corresponding passing test before the runbook can reference that signal, and each runbook must reference only rows below (no aspirational signals).

| Runbook | Signal | Type | Source | Validating Test |
|---|---|---|---|---|
| `controller-lag.md` | `gitstore_controller_queue_depth{kind}` | Prometheus gauge | `health.QueueDepth` | `TestObservability_QueueDepth_ReflectsPendingItems` |
| `controller-lag.md` | `gitstore_controller_active_workers{kind}` | Prometheus gauge | `health.ActiveWorkers` | `TestObservability_ActiveWorkers_ReflectsRunningReconciles` |
| `controller-lag.md` | `gitstore_controller_stalled_workers{kind}` | Prometheus gauge | `health.StalledWorkers` | `TestObservability_StalledWorkers_SetWhenNoRecentSuccess` |
| `controller-lag.md` | `gitstore_controller_reconcile_total{kind,result}` | Prometheus counter | `health.ReconcileTotal` | `TestObservability_ReconcileTotal_LabeledByOutcome` |
| `controller-replay-window-exceeded.md` | `gitstore_controller_checkpoint_last_write_timestamp_seconds{kind}` | Prometheus gauge | `health.CheckpointLastWriteTimestamp` | `TestObservability_CheckpointLastWriteTimestamp_UpdatesOnSave` (existing coverage in `tests/contract/health_test.go`; cross-referenced, not duplicated) |
| `controller-replay-window-exceeded.md` | `gitstore_controller_checkpoint_replay_backlog{kind}` | Prometheus gauge | `health.CheckpointReplayBacklog` | `TestObservability_CheckpointReplayBacklog_TracksQueueDepth` (existing coverage in `tests/contract/health_test.go`; cross-referenced, not duplicated) |
| `controller-replay-window-exceeded.md` | Structured log: watch expired / relist triggered | zap log line | `internal/listwatch/runner.go` | `TestObservability_WatchExpired_LogsRelistTrigger` |
| `controller-poisoned-item.md` | `gitstore_controller_poison_items_total{kind}` | Prometheus gauge | `health.PoisonItemsTotal` | `TestObservability_PoisonItemsTotal_IncrementsOnQuarantine` |
| `controller-poisoned-item.md` | `GET /controller/v1/poison/{kind}`, `GET /controller/v1/poison/_all` | HTTP API | `cmd/controller/main.go` handlers | `TestIntegration_PoisonedItem_VisibleViaHTTPPoisonAPI` (in `poison_item_test.go`) |
| `controller-poisoned-item.md` | `POST /controller/v1/poison/{namespace}/{kind}/{name}/requeue` | HTTP API | `cmd/controller/main.go` handlers | `TestObservability_RequeuePoisonAPI_ClearsQuarantineAndReenqueues` |
| `controller-poisoned-item.md` | Structured log: quarantine reason (`LastError`) | zap log line | `internal/manager/manager.go` (`log.Error("terminal reconcile failure...")` / `"reconciler quarantined after exhausting retries"`) | `TestObservability_QuarantineLog_IncludesLastError` |

## Rule

If, while writing a validating test, a signal in this table is found to not exist or not behave as described (e.g., a gauge that doesn't actually update on the described trigger), the row and the corresponding runbook prose MUST be corrected together — either by writing a red test that then drives a minimal instrumentation fix (Constitution Principle I), or by rewriting the runbook to describe the true behavior. A runbook must never ship referencing a signal whose row lacks a passing `Validating Test`.
