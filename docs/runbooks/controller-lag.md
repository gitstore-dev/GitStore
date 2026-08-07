# Runbook: Controller Lag

## Symptom

The controller manager's work queue for one or more resource kinds is growing, or reconciles are visibly falling behind incoming changes (status updates on resources arrive later than expected, or an alert fires on queue depth).

## Diagnostic Steps

1. Check queue depth per kind:

   ```promql
   gitstore_controller_queue_depth{kind="<Kind>"}
   ```

   This gauge includes items still in the manager queue plus tasks submitted to the worker pool that are waiting for a worker; actively running reconciles are reported separately. A sustained upward trend (rather than a transient spike) therefore remains visible after the dispatcher has handed work to a saturated pool and indicates the controller is not draining work as fast as it arrives.

2. Check active worker utilization for the same kind:

   ```promql
   gitstore_controller_active_workers{kind="<Kind>"}
   ```

   If this is consistently at (or near) the kind's configured `WorkerCount` while queue depth grows, the worker pool is saturated — reconciles are taking too long, or there simply aren't enough workers for the incoming rate.

   If active workers is consistently low (near 0) while queue depth grows, workers are not being scheduled — check whether the informer cache for that kind has synced (`HasSynced`); dispatch is gated until it has.

3. Check whether the kind is stalled (no successful reconcile recently):

   ```promql
   gitstore_controller_stalled_workers{kind="<Kind>"}
   ```

   A value of `1` means no reconcile has succeeded within the kind's `StallThreshold`. Combined with a growing queue, this points to every reconcile attempt failing (or hanging) rather than merely being slow — treat this as higher priority than a pure throughput problem.

4. Check the outcome breakdown for recent reconciles:

   ```promql
   rate(gitstore_controller_reconcile_total{kind="<Kind>"}[5m])
   ```

   grouped by the `result` label (`success`, `transient_failure`, `terminal_failure`, `requeue_after`). A high `transient_failure` or `terminal_failure` rate relative to `success` indicates reconciles are failing rather than genuinely being slow — this changes the recovery action (see below).

5. Check controller-manager logs for the affected kind for `reconciler panic recovered` or repeated `reconcile retry` warnings, which corroborate a failing-reconciler root cause over a pure capacity problem.

## Recovery Actions

- **If workers are saturated and reconciles are succeeding, just slowly**: increase `WorkerCount` for the affected kind (`ReconcilerRegistration.WorkerCount`) and redeploy, or investigate why individual reconciles are slow (e.g. a slow downstream dependency called from within `Reconcile`).
- **If the queue is growing because reconciles are failing** (high `transient_failure`/`terminal_failure` rate): this is not a capacity problem — follow up on the specific error from the logs. If items are being quarantined as a result, see [controller-poisoned-item.md](./controller-poisoned-item.md).
- **If workers are idle while the queue grows**: confirm the kind's cache has completed its initial sync; if the informer/list-then-watch bootstrap for that kind is stuck, see [controller-replay-window-exceeded.md](./controller-replay-window-exceeded.md) for the related checkpoint diagnostics.
- **If the upstream dependency the reconciler calls is degraded**: this is not fixable from within the controller manager — treat it as an upstream incident; the controller will drain its backlog automatically once the dependency recovers, since successful reconciles are not lost, only delayed.

## Verification

- `gitstore_controller_queue_depth{kind}` trends back down toward zero (or your steady-state baseline).
- `gitstore_controller_stalled_workers{kind}` returns to `0`.
- `rate(gitstore_controller_reconcile_total{kind,result="success"}[5m])` recovers to its prior baseline rate.
