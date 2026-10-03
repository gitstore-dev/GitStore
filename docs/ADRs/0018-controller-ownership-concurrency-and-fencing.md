# ADR 0018: Controller Ownership, Concurrency and Fencing

**Status**: Proposed

**Date**: 2026-10-03

**Audience**: controller-manager, API and Git service maintainers; authors of any design that adds a
controller, a status writer, or a side effect driven by reconciliation.

**Replaces**: `docs/architecture/controller-ownership-and-replica-groups.md` (removed). This ADR is
the single authoritative source for controller ownership, concurrent writers, leases and fencing.
Other ADRs and implementation docs cite its sections; they don't restate the mechanics.

## Context

GitStore runs every core service as independently scaled replicas. For controllers this raises
three separate questions, which earlier designs mixed together:

1. **Who may write what?** Which controller owns a kind's status, finalizers and side effects.
2. **What keeps concurrent writers correct?** Several replicas, or replicas mid-failover, act at
   the same time.
3. **What stops duplicate work?** Two replicas doing the same expensive or external action.

Today:

- The API authorizes controller writes and applies optimistic `resourceVersion` checks. It
  validates no controller lease and no fencing token, so replicas of one controller can reconcile
  concurrently.
- `internal/watchjournal.LeaseManager` elects and fences the single process that turns CDC records
  into the durable watch journal. It doesn't elect a controller, reserve a finalizer, or fence
  GraphQL status writes.
- `conditions` is a single JSON array. A status write can replace the whole array when the writer
  meant to change one condition.
- Newer designs ([ADR 0014](0014-catalog-release-and-publication.md),
  [ADR 0015](0015-resource-lifecycle-hooks.md),
  [039](../implementation/039-resource-lifecycle-hooks.md),
  [037](../implementation/037-custom-commerce-workflows.md)) introduce non-idempotent and external
  side effects: release tags, a public target pointer, outbound webhook delivery, workflow actions.

The research basis for the decision is:

- **A lease alone is not safe.** Leases are time-bounded grants that assume bounded clock drift
  (Gray & Cheriton 1989). A holder can stall (GC pause, VM freeze, network delay) and act after
  its lease has expired. Chubby therefore passes a *sequencer* that the target server checks
  (Burrows 2006). Kleppmann (2016; 2017, ch. 8) generalises this as **fencing tokens**: a
  monotonically increasing number sent with every write and checked by the resource being written.
  Without that check, a lock or lease is an efficiency optimisation, not a safety mechanism.
  Kubernetes' client-go leader election states the same about itself: it "does not guarantee that
  only one client is acting as a leader (a.k.a. fencing)".
- **Conditional writes are safe without any lease.** Optimistic concurrency control (Kung &
  Robinson 1981) plus idempotent, level-triggered reconciliation (Helland 2012) tolerates any number
  of concurrent replicas. A conflicting write is rejected and recomputed from fresh state.
- **The unit of serialisation should be the entity, not the system.** Helland (2007) argues that
  scalable systems serialise per entity and never globally. A per-group leader serialises a whole
  kind behind one process: failover stalls the kind, and one slow key delays all others.
- **Fine-grained leases scale when they are partitioned.** Centrifuge (Adya et al. 2010) and Slicer
  (Adya et al. 2016) grant leases on key *partitions* through a lease manager rather than on
  individual keys, which keeps renewal load bounded.

## Decision

### 1. Ownership

- **One primary controller group per kind.** A group is one logical controller deployment,
  identified by controller kind and configuration. Its replicas share ownership rules and one
  checkpoint namespace. Scaling one group must not change another group's concurrency or failure
  domain.
- Only the primary group may manage the kind's controller finalizer and its controller-owned status
  fields.
- A controller may watch secondary kinds to enqueue its primary objects. Secondary watches are
  read-only: they give no right to mutate the secondary object or its status. A secondary watch
  cursor belongs to the consuming group.
- Every group has:
  - a unique finalizer name for the lifecycle work it owns;
  - an RBAC identity ([ADR 0010](0010-authorization-model.md)) limited to its primary
    spec/status/finalizer operations plus read/watch on secondary kinds;
  - a unique durable checkpoint key or directory, shared only by its own replicas.
- Finalizers, permissions and checkpoints are never reused across unrelated groups.

### 2. Status writes

- A controller updates only its documented portion of status and preserves unknown fields written
  by others. This is logical ownership enforced by code and RBAC. GitStore has no field managers or
  server-side apply.
- Until the API enforces per-condition ownership (a merge-by-type operation), only the primary
  group writes `conditions`. It reads the latest `resourceVersion`, preserves condition types it
  doesn't own, and retries a conflict by recomputing the complete update.
- Before a second group may write the same status object or overlapping fields, the API must
  provide field managers, including merge-by-type ownership for conditions.

### 3. Safety comes from conditional writes, never from clocks

Every write that a stale or concurrent replica could make incorrect must be **conditional at the
resource being written**. In order of preference:

| Mechanism                                  | Use when                                                                                                     | Examples                                                                                                                                                                                     |
|--------------------------------------------|--------------------------------------------------------------------------------------------------------------|----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------|
| Optimistic version check                   | Updating a resource that has a `resourceVersion`                                                             | Status patches; spec writes through the API                                                                                                                                                  |
| Native compare-and-swap on expected state  | The target can compare against an expected prior value                                                       | Git ref update with an expected old object ID (zero for create-if-absent), as for release tags; Scylla lightweight transaction `UPDATE … IF pointer = :old` for a publication target pointer |
| Fencing token validated by the API         | The operation is non-idempotent and has no natural expected state, or several writes must be fenced together | Advancing an event-delivery cursor; a multi-step command recorded under one operation                                                                                                        |
| Idempotency key with observe-before-create | The target can't check anything (third-party systems)                                                        | Outbound webhooks; external workflow actions                                                                                                                                                 |

- All reconciliation and status operations are idempotent. A conflict is a signal to read fresh
  state and recompute, never to replay a stale patch.
- A fencing token is monotonically increasing per lease key, issued by the API, and **checked by
  the API (or the Git service on the API's command) on every protected write**. A write carrying a
  lower token than the last accepted one is rejected.
- No correctness property depends on lease expiry, wall-clock time or process health. Clocks may
  trigger work (deadlines, retries); they never authorise a write.

### 4. Leases provide liveness, not safety

A lease exists only to avoid duplicate or wasted work, for example two replicas both building the
same snapshot or both calling a slow webhook.

- **Granularity is the side-effect key**, for example `release/<uid>`, `publication/<target-key>`
  or `subscription/<uid>`. Group-wide leader election is not used.
- **Acquire on demand.** A replica takes a key's lease only when it is about to issue a
  side-effecting command, and releases it afterwards. Keys with no pending side effect hold no
  lease.
- **Partition when key counts grow.** If capacity tests show lease renewal load is material, leases
  move to hash partitions of the key space (Centrifuge/Slicer style). The fencing token is then per
  partition, and the protected-write rule in §3 is unchanged.
- Leases are stored and granted by the API with a bounded expiry. A graceful holder releases its
  lease. An unhealthy holder is replaced only after expiry. Every command also carries a durable
  idempotency key, so an uncertain outcome is resolved by reading the recorded operation, never by
  mutating again.
- The watch journal's `LeaseManager` is the reference implementation of the acquire/renew/fence
  mechanics. Controller leases reuse those mechanics in their own keyspace. The materializer lease
  itself never protects controller writes.

### 5. When a design must add fencing

A design that adds any of the following must state, per operation, which §3 mechanism protects it
and, if it uses a lease, its key:

- a non-idempotent write;
- a side effect outside the GitStore datastore;
- active/passive execution, or intolerance of two concurrent actors during failover.

Field ownership (§2) and fencing (§3–§4) are independent. A design that needs both implements and
tests both, including lease expiry, stale-writer rejection, rolling replacement and checkpoint
recovery.

### 6. Current applications

| Design            | Protected operation                      | Safety (§3)                                            | Lease key (§4)             |
|-------------------|------------------------------------------|--------------------------------------------------------|----------------------------|
| ADR 0014          | Create release tag                       | Git create-if-absent CAS at the pinned commit          | `release/<uid>`            |
| ADR 0014          | Activate, replace or deactivate a target | Scylla LWT on the target pointer, plus a fencing token | `publication/<target-key>` |
| ADR 0015 / 039 §6 | Advance an event-delivery cursor         | Fencing token validated by the API                     | `subscription/<uid>`       |
| 037               | Workflow transition and action result    | Execution `resourceVersion`, plus an idempotency key   | per execution, on demand   |

## Consequences

Positive:

- One place defines how controllers stay correct under concurrency; feature ADRs only list their
  protected operations.
- Correctness survives GC pauses, clock skew and partitions, because safety never depends on a
  lease being held.
- Failures stay per key. Scaling controller replicas adds throughput instead of standby capacity.

Negative:

- Every protected write path in the API needs a conditional form (version, CAS or token), and tests
  that prove a stale writer is rejected.
- On-demand per-key leases add a datastore round trip before each side-effecting command.
- External systems that can't check anything stay at-least-once. Consumers must deduplicate.

## Cross-references

- [ADR 0010](0010-authorization-model.md): controller RBAC identities.
- [ADR 0014](0014-catalog-release-and-publication.md) §9, [ADR 0015](0015-resource-lifecycle-hooks.md)
  §6, [039](../implementation/039-resource-lifecycle-hooks.md) §6,
  [037](../implementation/037-custom-commerce-workflows.md): apply §3–§4.
- [021 Controller service-account auth](../implementation/021-controller_service_account_auth.md).
- `gitstore-api/internal/watchjournal/lease.go`: reference lease and fencing mechanics.

## Alternatives considered

### Group-wide leader election (one active replica per controller kind)

Rejected. It serialises a whole kind behind one process, so failover stalls every key and one slow
key delays all others (Helland 2007). Without fencing it still allows two active leaders during a
pause or partition, which is the case client-go documents.

### Leases without fencing tokens

Rejected. A paused holder can act after expiry (Kleppmann 2016). This would be acceptable only for
work whose every write is already protected by a version check or CAS, and then the lease is
optional anyway.

### No coordination at all: optimistic concurrency only

This is the default, and it remains sufficient for idempotent reconciliation. It is not sufficient
for non-idempotent or external effects, which need §3's CAS, fencing or idempotency keys.

### One lease per key, held permanently

Rejected. Renewal load grows with the number of keys rather than with active work. On-demand
acquisition, then partitioning, bounds it (Adya et al. 2010; 2016).

## References

- Adya, A., et al. (2010). Centrifuge: Integrated Lease Management and Partitioning for Cloud
  Services. *NSDI '10*.
- Adya, A., et al. (2016). Slicer: Auto-Sharding for Datacenter Applications. *OSDI '16*.
- Burrows, M. (2006). The Chubby Lock Service for Loosely-Coupled Distributed Systems. *OSDI '06*.
- Gray, C., & Cheriton, D. (1989). Leases: An Efficient Fault-Tolerant Mechanism for Distributed
  File Cache Consistency. *SOSP '89*.
- Helland, P. (2007). Life beyond Distributed Transactions: an Apostate's Opinion. *CIDR '07*.
- Helland, P. (2012). Idempotence Is Not a Medical Condition. *ACM Queue* 10(4).
- Kleppmann, M. (2016). How to do distributed locking.
  <https://martin.kleppmann.com/2016/02/08/how-to-do-distributed-locking.html>
- Kleppmann, M. (2017). *Designing Data-Intensive Applications*, ch. 8. O'Reilly.
- Kung, H. T., & Robinson, J. T. (1981). On Optimistic Methods for Concurrency Control. *ACM
  Transactions on Database Systems* 6(2).
- Kubernetes client-go `tools/leaderelection` package documentation.
