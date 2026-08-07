# Runbook: Replay Window Exceeded

## Symptom

A controller's watch stream for a resource kind was disconnected for long enough that the API's event log compacted past the controller's checkpointed cursor. The `listwatch.Runner` for that kind logs a watch-expired / relist event, and the kind's checkpoint may appear stale in the interim.

## Diagnostic Steps

1. Check how long ago the kind's checkpoint was last written:

   ```promql
   time() - gitstore_controller_checkpoint_last_write_timestamp_seconds{kind="<Kind>"}
   ```

   A large value (approaching or exceeding the API's compaction/replay window) is consistent with the cursor having expired before the controller reconnected.

2. Check the replay backlog for the kind:

   ```promql
   gitstore_controller_checkpoint_replay_backlog{kind="<Kind>"}
   ```

   This tracks the number of watch events enqueued as work items but not yet dispatched. A spike here after a reconnect is expected — it reflects the burst of re-enqueued items from a fresh list — and should drain as workers catch up.

3. Search controller-manager logs for the affected kind for the structured log line `"watch cursor expired; re-listing"` (emitted by `internal/listwatch/runner.go` when `Watch` returns an error satisfying `errors.Is(err, listwatch.ErrWatchExpired)`). Its presence confirms the Runner detected the expired cursor and is recovering, rather than being stuck.

## Recovery Actions

**No manual relist is required.** The `listwatch.Runner` self-heals from an expired watch cursor automatically: it discards the stale checkpoint, performs a full `List` against the API, persists a fresh checkpoint at the new `resourceVersion`, and resumes watching from there (this is FR-006 of spec 038, exercised by `TestIntegration_ReplayWindowExceeded_FallsBackToFullBootstrap` in `gitstore-controller-manager/tests/integration/disconnect_reconnect_test.go`). If you observe the symptom, the correct action is almost always to **wait and confirm recovery**, not to intervene.

If the checkpoint timestamp does *not* advance after a reasonable wait (several times the kind's `MaxWatchBackoff`), that indicates the Runner is not recovering on its own — this is a bug, not the expected self-healing path; escalate rather than trying to force a relist manually (there is no supported manual-relist operation).

If replay backlog remains elevated long after a relist (rather than draining), treat it as a [controller-lag](./controller-lag.md) symptom instead — the relist succeeded but the worker pool can't keep up with the re-enqueued backlog.

## Verification

- `gitstore_controller_checkpoint_last_write_timestamp_seconds{kind}` advances to a recent value (the fresh post-relist checkpoint).
- `gitstore_controller_checkpoint_replay_backlog{kind}` drains back to its steady-state baseline.
- No further watch-expired log lines recur for the same kind shortly after recovery.
