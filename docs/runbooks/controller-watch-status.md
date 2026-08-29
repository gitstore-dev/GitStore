# Runbook: Controller Watch API and Status-Write Diagnostics

## Symptom

A controller's `watchCategories`/`watchResources` subscription disconnects and reconnects (a normal occurrence), or its `updateCategoryStatus`/`updateResourceStatus` writes are being rejected. This runbook helps distinguish a transient, self-healing disconnect from a cursor that has actually expired (requiring a re-list), and helps interpret a sustained status-write-conflict rate.

## Diagnostic Steps: Subscription Authorization Denials

Every watch subscription field (`watchCategories`, `watchFiles`, `watchProducts`, and the generic `watchResources`) is authorized once, at subscribe time, by `GraphQLFieldAuthorizer` (`gitstore-api/internal/middleware/security/graphql.go`) — the same seam that already gates mutations like `updateCategoryStatus`. A denied subscribe attempt fails immediately with a GraphQL error carrying `extensions.code == "FORBIDDEN"` and never opens an event channel (the resolver never runs, so no partial/empty stream is returned).

1. If a controller or client's subscription is rejected outright (not disconnecting/reconnecting, but failing on the initial `subscribe`), check the derived action string in the error's `permission denied: <reason>` message. The action follows `<kind>.watch` (e.g. `categoryTaxonomy.watch`, `file.watch`, `product.watch`, or `<kind>.watch` for whatever kind was passed to `watchResources`).
2. Under the default `allow-all` AuthZ provider (local/dev `docker compose`), this check always passes — denials are only possible when the deployment is configured with `rbac-local` (`GITSTORE_AUTH__AUTHZ__PROVIDER=rbac-local`) or another `AuthZProvider` that enforces per-namespace policy.
3. If a deployment switched to `rbac-local` and a previously-working controller (e.g. `gitstore-controller-manager`, which always calls these subscriptions without a `namespace` argument — i.e. it watches every namespace) starts failing to subscribe, grant its bound role the corresponding `<kind>.watch` action (or `"*"`) in `policy.yaml`, the same way the existing `controller` role convention already grants `*.status.write` (see `specs/040-controller-watch-status-api/research.md` R5).
4. This check runs once per subscription, not once per delivered event — a subscription that was authorized to open cannot be revoked mid-stream by a later policy change (the caller must resubscribe for a new decision to take effect).

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
