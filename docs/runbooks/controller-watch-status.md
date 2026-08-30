# Runbook: Controller Watch API and Status-Write Diagnostics

## Symptom

A controller's `watchCategories`/`watchResources` subscription disconnects and reconnects (a normal occurrence), or its `updateCategoryStatus`/`updateResourceStatus` writes are being rejected. This runbook helps distinguish a transient, self-healing disconnect from a cursor that has actually expired (requiring a re-list), and helps interpret a sustained status-write-conflict rate.

## Diagnostic Steps: Watch Disconnects

1. Check the rate of subscriptions being opened for the affected kind:

   ```promql
   rate(gitstore_eventbus_subscriptions_opened_total{kind="<Kind>"}[5m])
   ```

   Split by the `resume` label. A `resume="true"` open immediately following a disconnect, with no corresponding `watch_expired_total` increment for that kind, is a normal transient reconnect — the controller resumed from its in-memory `resourceVersion` cursor and picked up where it left off. This requires no operator action.

2. Check whether the disconnect was actually an expired-cursor rejection:

   ```promql
   rate(gitstore_eventbus_watch_expired_total{kind="<Kind>"}[5m])
   ```

   Any non-zero value here means at least one `Subscribe` call for that kind was rejected because its requested `resourceVersion` predates the server's retained event window. This is not the same failure mode as a transient network disconnect — it means the controller fell behind far enough (or was disconnected long enough) that in-memory replay is no longer possible, and it must re-list.

3. Confirm the controller reacted correctly to an expired cursor by checking `gitstore-api` logs for the affected kind:

   ```text
   "watch cursor expired; controller must re-list" kind=<Kind> resource_version=<rv>
   ```

   This log line (level `WARN`) is emitted by the server on every `WATCH_EXPIRED`-extension response. If you see this line but the controller's own logs show it treating the response as an ordinary error (retrying the same stale cursor instead of re-listing), that is a controller-side bug, not a server-side problem — the server has already signaled the correct recovery action.

4. Check whether events are being dropped for the kind (a sign the watcher is slow, not merely disconnected):

   ```promql
   rate(gitstore_eventbus_events_dropped_total{kind="<Kind>"}[5m])
   ```

   A non-zero rate means the subscriber's buffered channel filled up and events were dropped rather than delivered. This does not corrupt the stream — the dropped events widen the gap between the subscriber's last-seen `resourceVersion` and the server's current position, which will eventually manifest as a `WATCH_EXPIRED` on the next resume attempt once that gap exceeds the retained window. A sustained non-zero rate for one kind, while others are at zero, points to that kind's specific reconciler being too slow to drain its watch channel, not a server-wide problem.

## Recovery Actions: Watch Disconnects

- **Transient reconnect (resume="true", no `watch_expired_total` increment)**: no action needed — this is expected behavior under normal network conditions.
- **Expired cursor (`watch_expired_total` incrementing)**: confirm the controller's `ListWatcher` implementation re-lists on `errors.Is(err, listwatch.ErrWatchExpired)` rather than retrying the same cursor (see `specs/036-controller-startup-resume/quickstart.md`'s `Runner[T].recoverFromExpiry` pattern). If the controller is already re-listing correctly and cursors are still expiring frequently, the retained event window (`eventBusCapacity` in `gitstore-api/internal/app/server.go`) may be too small for that kind's write volume relative to how long controllers are typically disconnected — consider increasing it.
- **Events being dropped (`events_dropped_total` incrementing)**: the affected kind's controller is not draining its watch channel fast enough. Check that kind's own queue-depth/worker-saturation signals (see [controller-lag.md](./controller-lag.md)) — a controller stuck processing a backlog will also be slow to read from its watch subscription.

## Diagnostic Steps: Status-Write Conflicts

1. Check the conflict rate for the affected kind:

   ```promql
   rate(gitstore_status_write_conflicts_total{kind="<Kind>"}[5m])
   ```

   A `StatusConflict` response is expected occasionally (any concurrent write to the same resource can trigger one) — the reconciler pattern already handles this by retrying with fresh cache state (see `specs/026-reconcile-handler/quickstart.md`). A sustained non-zero rate, rather than an occasional blip, is the signal worth investigating.

2. Check `gitstore-api` logs for the specific resources involved:

   ```text
   "status write conflict" kind=<Kind> namespace=<ns> name=<name>
   ```

   If the same `namespace`/`name` pair appears repeatedly in a short window, two writers are racing on that specific resource. If many different resources of the same kind are conflicting, the reconciler for that kind may be operating on a stale cache snapshot (e.g. a watch resume gap — cross-reference with the watch-disconnect signals above).

## Recovery Actions: Status-Write Conflicts

- **Occasional conflicts, low rate**: no action needed — this is the optimistic-concurrency mechanism working as intended.
- **Sustained conflicts on the same resource**: identify the two writers (e.g. two controller replicas reconciling the same kind, which is unsupported per this feature's Assumptions — leader election is out of scope) and ensure only one writer is active.
- **Sustained conflicts across many resources of one kind, correlated with watch-expiry or event-drop signals for that kind**: the reconciler is working from stale cache data. Fix the underlying watch-consumption lag first (see above); the conflict rate should subside once the cache catches up.

## Verification

- `rate(gitstore_eventbus_watch_expired_total{kind}[5m])` returns to `0` (or the controller correctly re-lists whenever it is non-zero).
- `rate(gitstore_eventbus_events_dropped_total{kind}[5m])` returns to `0`.
- `rate(gitstore_status_write_conflicts_total{kind}[5m])` returns to its prior baseline (occasional, not sustained).

## Namespace durable-watch rollout and recovery

Namespace uses a durable Scylla CDC journal rather than the process-local event
bus described above. Keep spec 047 deployed; do not reopen or roll back its
Namespace lifecycle contract.

Roll out in this order:

1. Apply migration 006 everywhere and verify the Namespace base table has full
   preimage/postimage CDC with the 14-day TTL plus the journal-event table and
   partition-local clock/lease/progress table.
2. Override both alpha-default-on Namespace watch gates to `false` while any
   API replica lacks the new schema/code. Deny Namespace watch ingress
   fleet-wide during mixed-version operation; an old replica cannot honor the
   durable cursor contract.
3. Enable `MATERIALIZER_ENABLED` on the converged fleet. Exactly one healthy
   replica should report materializer leader `1`; wait for a durable BOOKMARK
   and independently persisted CDC query progress below 60 seconds. A fresh
   BOOKMARK alone does not certify CDC health.
4. Enable `READERS_ENABLED`, restore watch ingress, and run the cross-replica
   bootstrap/resume probe before declaring rollout complete.

For rollback, first deny watch ingress and disable readers. Disable the
materializer only after readers are drained. Migration 006 is a supported,
additive artifact and may remain during application rollback; do not drop CDC
or journal tables while any issued cursor could still be presented.

Namespace signals have bounded labels only (`path` and `reason`; never a
Namespace name, UID, cursor, holder ID, or replica ID):

- `gitstore_namespace_watch_materializer_leader` — alert if the fleet sum is
  zero for 30 seconds or above one for two lease TTLs.
- `gitstore_namespace_watch_cdc_lag_seconds` and
  `gitstore_namespace_watch_bookmark_age_seconds` — warn above 30 seconds and
  page above the 60-second readiness bound. Both report `+Inf` until their
  first durable observation; bookmark age advances only from an actual
  `BOOKMARK`, not ordinary journal activity.
- `gitstore_namespace_watch_journal_oldest_sequence` and
  `gitstore_namespace_watch_journal_high_water_sequence` — alert if high water
  stops advancing during acknowledged mutations or the retained span shrinks
  unexpectedly.
- `gitstore_namespace_watch_subscribers{path="typed|generic"}` — capacity
  gauge; compare with the planned 1,000-subscriber envelope.
- `gitstore_namespace_watch_expired_total{reason}` and
  `gitstore_namespace_watch_overflow_total` — alert on any
  `JOURNAL_DISCONTINUITY`; warn on sustained overflow or expiry above 0.1% of
  subscription attempts.
- `gitstore_namespace_watch_append_errors_total` — page on any sustained
  non-zero rate because acknowledged mutations may be awaiting CDC recovery.
- replay and delivery histograms — alert if 10,000-event replay p95 exceeds 5
  seconds, delivery p95 exceeds 1 second, or delivery p99 exceeds 3 seconds.

During replacement, the old leader may finish or lose its lease;
partition-local conditional writes stop a stale holder from
publishing/progressing. A replacement should acquire the
lease, resume durable CDC progress, write a BOOKMARK, and restore readiness in
30 seconds. Duplicates after append-before-progress recovery are safe and must
be deduplicated by cursor; missing sequences are not safe and fail closed.

Recovery by wire code:

- `WATCH_UNAVAILABLE/MATERIALIZER_NOT_READY`: retain the cursor, back off, and
  retry another ready replica. Check leader, CDC lag, bookmark age, and append
  errors.
- `WATCH_EXPIRED`: discard the cursor and repeat the documented
  bootstrap/list/drain algorithm. For `SUBSCRIBER_OVERFLOW`, also repair the
  slow consumer before reconnecting. For `JOURNAL_DISCONTINUITY`, page the
  datastore owner and preserve affected journal/CDC rows for diagnosis.
