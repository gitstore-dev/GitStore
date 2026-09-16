# Controller Ownership and Replica Groups

**Status:** Future design guidance; not an implemented fencing contract.

GitStore controllers should scale as independent replica groups. A group is one
logical controller deployment, identified by controller kind and configuration,
whose replicas share ownership rules and a checkpoint namespace. Scaling one
group must not change the concurrency or failure domain of another group.

## Ownership model

Each resource kind has one primary controller group. Only that group may manage
the kind's controller finalizer and controller-owned status fields. A controller
may watch secondary kinds to enqueue its primary objects, but those secondary
watches are read-only: they do not confer permission to mutate the secondary
object or its status.

Every group needs:

- a unique finalizer name for lifecycle work it owns;
- an RBAC identity limited to its primary spec/status/finalizer operations and
  read/watch access for secondary resources;
- a unique durable checkpoint key or directory, shared only by replicas of that
  group.

Finalizers, permissions, and checkpoints must not be reused across unrelated
controller groups. A secondary watch cursor belongs to the consuming group, not
to the controller that owns the watched kind.

## Status ownership

Controllers should update only their documented portion of status and preserve
unknown fields written by other actors. This is logical ownership enforced by
code and RBAC; GitStore does not currently provide Kubernetes-style field
managers or server-side apply.

Conditions need special care. The current status contract treats `conditions`
as one JSON array, so a write can replace the full slice even when the writer
intended to change one condition. Until condition entries have server-enforced
ownership or a merge-by-type operation, only the primary controller should
write the slice. It must read the latest resource version, preserve condition
types it does not own, and retry conflicts by recomputing the complete update.

All reconciliation and status operations must be idempotent. Writers use
optimistic concurrency, treat a resource-version conflict as a signal to read
fresh state, and recalculate rather than replaying a stale patch. External side
effects need stable idempotency keys or observe-before-create semantics so a
retry or replica handoff is safe.

## Replicas and fencing

The API currently authorizes controller writes and applies optimistic
resource-version checks, but it does not validate a controller-group lease or
fencing token. Multiple replicas of one group may therefore reconcile
concurrently. Correctness must come from idempotency, conflict retries, and
single-group field ownership; health or process identity does not elect a
writer.

The CDC watch materializer lease is separate. It elects and fences the process
that converts CDC records into the durable watch journal. It does not elect a
controller primary, reserve a finalizer, or fence GraphQL status writes, and it
must not be cited as controller ownership protection.

Add stronger coordination when either of these becomes true:

- more than one controller group must mutate the same status object or
  overlapping fields: introduce API-enforced field managers (including
  merge-by-type ownership for conditions) before enabling the second writer;
- a group performs non-idempotent work, requires active/passive execution, or
  cannot tolerate concurrent leaders during failover: introduce a durable
  group lease whose monotonically increasing fencing token is required and
  validated by every protected API write and external side effect.

Group leases do not replace field ownership, and field managers do not prevent
two replicas from performing an unsafe external action. Designs that need both
properties must implement and test both mechanisms, including lease expiry,
stale-writer rejection, rolling replacement, and checkpoint recovery.
