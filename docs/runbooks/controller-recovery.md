# Controller Recovery

Use this guide for a stalled backlog, an expired replay cursor, or quarantined
work. Watch reconnects, transient retry, and expired-cursor relisting are
automatic; they are not scheduled maintenance.

## Lag and stalled work

Compare queue, worker, and outcome signals for the affected resource kind:

- `gitstore_controller_queue_depth{kind}` counts queued work and work waiting
  for a pool slot, not actively running reconciles.
- `gitstore_controller_active_workers{kind}` distinguishes occupied workers
  from queued work.
- `gitstore_controller_stalled_workers{kind}` is `1` when no reconcile has
  succeeded within the kind's stall threshold, not a count of hung workers.
- `gitstore_controller_reconcile_total{kind,result}` distinguishes `success`,
  `transient_failure`, `terminal_failure`, and `requeue_after`.

If workers are busy, investigate downstream latency and dependency health
before tuning concurrency. If the queue grows while workers are idle, check
cache synchronization and watch readiness. For widespread failures, restore
the shared dependency first; pending work should then drain automatically.
Do not treat additional controller replicas as a guaranteed throughput fix.

Category moves and Product changes can legitimately fan out into work for
affected categories. Retain the Product checkpoint across replacement so former
affected categories can still be reconciled after deletion or reassignment.
A category cycle intentionally freezes resolved path
and depth with `Acyclic=False`; child counts can still update. Fix the cycle
through normal desired-state changes. A missing parent is a different
diagnostic, not evidence of a cycle.

Verify that successful reconciles resume, the backlog decreases, and workers
stop stalling. See [watch and status diagnostics](controller-watch-status.md)
if watch lag or repeated status conflicts persist.

## Expired replay cursor

When a checkpoint is older than the durable journal's replay window, the
list/watch runner detects the expired cursor, relists resources, persists a
fresh checkpoint, and resumes watching automatically.

Allow recovery to progress, then verify that the checkpoint timestamp advances,
the backlog drains, and watches resume from the fresh cursor. There is no
manual relist command. Do not delete checkpoints as a routine response to
expiry.

Use `gitstore_controller_checkpoint_last_write_timestamp_seconds{kind}` to
check checkpoint age and `gitstore_controller_checkpoint_replay_backlog{kind}`
to follow recovery progress. The log `"watch cursor expired; re-listing"`
confirms automatic recovery started. A temporary backlog spike after relisting
is expected; repeated expiry without progress is not.

If relisting does not progress, check API availability, authorization,
list latency, and watch readiness. `WATCH_UNAVAILABLE` is not expiry:
restore the journal/materializer and retain the cursor rather than falling
back to an unreplayable stream.

## Quarantined work

Quarantine follows an immediate terminal reconcile failure or exhausted
transient retries. It persists across replacement on the same per-replica
checkpoint volume; periodic resync does not clear it.

### Inspect

Read the protected controller diagnostic endpoint:

```text
GET /controller/v1/poison/<Kind>
GET /controller/v1/poison/_all
```

Responses are bounded to 256 items per page. Use `?limit=<n>` to request fewer
items and follow the opaque `X-Next-Cursor` with `?after=<cursor>`. Follow all
pages; an empty final page is valid. Storage errors return HTTP 503, not an
empty successful inventory.

Inspect `LastError` and the controller quarantine log. Error text is capped
at 2,048 bytes plus a truncation suffix; logs provide longer detail and any
recovered-panic stack trace.

Distinguish invalid resource data from a shared service outage. Many kinds
failing with the same error usually warrant one dependency investigation,
not independent data repairs. A `"<kind>: <namespace>/<name> not found in cache"`
error can indicate a reconciler defect: queued keys can outlive deleted
resources. Report and fix that cause rather than repeatedly requeuing.

### Recover

Fix invalid desired state through the normal push/API path, or restore the
failed dependency. Only then requeue the affected item:

```text
POST /controller/v1/poison/<namespace>/<Kind>/<name>/requeue
```

For cluster-scoped Namespace keys, omit the namespace segment:

```text
POST /controller/v1/poison/Namespace/<name>/requeue
```

HTTP 204 confirms removal from quarantine and re-enqueue with a fresh retry
budget. There is no bulk-requeue endpoint; affected items must be requeued
individually.

Do not requeue while the cause remains present. A newer resource or dependency
change can enqueue fresh work; completion from an older attempt must not clear
that newer obligation.

### Verify

The item should disappear from the paginated poison inventory,
`gitstore_controller_poison_items_total{kind}` should decrease, and
`gitstore_controller_reconcile_total{kind,result="success"}` should record
successful work for the affected kind.
