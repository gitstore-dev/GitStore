# Namespace admission operations

## Scope

This runbook covers Namespace structural validation, stateful policy admission,
GraphQL create/update/delete behavior, and the server-first activation of
`DeleteNamespacePayload.outcome`. Authorization must complete before validation,
policy, lifecycle, or blocker details are returned.

## Repository lifecycle fence

### Git-backed deletion safety

Use matching current Git service, API, and controller builds. GitStore is under
active development; mixed-version writers and upgrade compatibility with older
binaries are not supported. Concurrent current-version API/controller replicas
and restart recovery must remain correct. Keep exactly one active Git service,
with retained storage and non-overlapping replacement.

All implemented `complete*Deletion` inputs require the resource's GraphQL
`id: ID!`, its current `resourceVersion`, and its existing name/namespace scope.
A conflict requires a fresh resource read, never retrying stale work against a
same-name replacement. The check applies equally to Namespace, Repository,
Product, and CategoryTaxonomy.

An empty, verified system repository does not block Namespace initiation.
Contained resources, including Files, do, as do all ordinary repositories
(including terminating ones). Completion cleans up system storage by UID before
releasing the Namespace. Neither path cascades resources or ordinary repositories.

### Repository membership index

Repository emptiness uses one polymorphic `resources_by_repository` table:
`PRIMARY KEY ((repository_id, shard), kind, uid)`. A new kind adds rows, not a new
table or another per-kind query. Product, ProductVariant, CategoryTaxonomy,
Collection, and File use the same membership-write and deletion-fence protocol.
Namespace/Repository child checks continue using their existing indexed
relationships; no parallel per-kind membership tables are needed.

Membership is distinct from optional semantic owner references: a Product
without a Category owner still occupies its repository, and a nonblocking owner
reference must not permit deletion of its backing repository. Membership cannot
be removed by editing `ownerReferences`. The existing owner-dependent index
continues to answer its different, owner-specific queries.

An emptiness check performs at most 32 partition-key lookups with `LIMIT 1`,
short-circuiting on the first member. It never enumerates kinds, uses
`ALLOW FILTERING`, or performs a live count. Membership is written conservatively
before authoritative admission and removed only after authoritative removal is
confirmed; errors cannot be interpreted as emptiness. The shared fence excludes
admission while the parent transitions to deletion.

These are separate bounds: table count and query fan-out are independent of
kind count, but index storage necessarily grows with resource count. Fixed hash
sharding spreads rows; it does **not** impose a hard partition-size limit, and
`LIMIT 1` bounds returned rows, not tombstone or SSTable read work. Request
deadlines and datastore errors fail closed. Capacity acceptance must measure
worst-case partition size, skew, churn/tombstones, and empty-check latency.
Do not claim unlimited-cardinality performance or introduce an undocumented
resource quota. More buckets or adaptive partitioning require measured evidence.

The design follows Cassandra's
[partition sizing and bucketing guidance](https://github.com/apache/cassandra/blob/trunk/doc/modules/cassandra/pages/developing/data-modeling/data-modeling_refining.adoc)
and [CQL key restrictions, limits, and filtering semantics](https://github.com/apache/cassandra/blob/trunk/doc/modules/cassandra/pages/developing/cql/dml.adoc).
ScyllaDB's engineering discussion of
[hot partitions and goodput](https://www.scylladb.com/2024/04/17/per-partition-query-rate-limiting/)
explains why a single large, hot parent partition is not an adequate alternative.
A native counter or asynchronously materialized total is not deletion proof:
an ambiguous update or lagging projection must never authorize storage removal.

Migration history is immutable and non-destructive. The older per-kind tables
remain historical schema artifacts, but no runtime reads or writes use them.
The membership table and its per-repository readiness/tombstone metadata are
fixed schema, not a template for adding more tables per resource kind.

### Repairing incomplete projections

Missing membership readiness fails closed. Fresh repositories initialize it
when their empty authoritative record is created. For retained development data
or a damaged projection, stop relevant mutations and controller writes while
running the existing maintenance commands from `gitstore-api/`:

```bash
go run ./cmd/gitctl scylla-projection-audit --hosts "$HOSTS" --keyspace "$KEYSPACE"
go run ./cmd/gitctl scylla-projection-repair --dry-run --hosts "$HOSTS" --keyspace "$KEYSPACE"
go run ./cmd/gitctl scylla-projection-repair --confirm --hosts "$HOSTS" --keyspace "$KEYSPACE"
go run ./cmd/gitctl scylla-projection-audit --hosts "$HOSTS" --keyspace "$KEYSPACE"
```

Supply database passwords through `GITSTORE_API__DATASTORE__SCYLLA__PASSWORD`.
The repair commands cover all projections, not just repository membership.
Repair publishes readiness only after complete verification;
an interrupted repair can be retried with a fresh audit and confirmed repair.
Readiness is not a detector of subsequent manual index corruption.
Resume writers only after the final audit is clean.

`DeletionPending=True` records recoverable mutation work before the Git call.
Controllers resume that work after API replacement. `SUPERSEDED` means the
manifest content changed: do not overwrite it or clear the operation blindly.
`DELETION_REPAIR_REQUIRED` blocks termination without removal evidence.
Inspect retained manifests and terminating rows, compare
their UID, admitted path/ref/revision and current tree, and resolve ambiguous
ownership before allowing cleanup. A missing file alone is not proof of a
successful authorized mutation.

Catalog admission retries transient Namespace fence contention within a bounded
request budget rather than immediately rejecting a committed push. A confirmed
failed repository cleanup restores both name mappings and list projections
before releasing its fence. Controllers can also resume a known terminating UID
when its name mapping is missing; a different name, version, or UID is rejected.
If restoration itself fails, the fence remains held for projection repair.

Keep the Git service's UID tombstones permanently and include its private
operation refs in repository backups. They fence stale provisioning and prove
uncertain deletion outcomes. A prepared receipt can resume only while its durable
preparation marker and original expected tip still prove it was not published.
Unprovable interrupted publication, restored files, legacy unmarked receipts, and
ancestry exceeding the bounded recovery walk fail closed and require operator
reconciliation; retrying an arbitrary absent path is not sufficient.

For a terminating resource with a retained manifest, use an authenticated
Git checkout of the canonical authoring repository as the maintenance surface.
Record the GraphQL `metadata.uid` and `resourceVersion`, fetch `main`, and compare
the exact file against its admitted revision. Re-read the resource to confirm
the same UID/version, remove only that file, commit, and push with an explicit
`--force-with-lease=refs/heads/main:<reviewed-head>` precondition. Admission
records removal evidence for that terminating UID; controller completion can
then proceed. A changed manifest is rejected rather than silently discarded.
Do not fabricate a removal for an absent row, restore a manifest just to delete
it again, or clear finalizers directly. Retained files with missing records and
unverifiable provenance require manual ownership reconciliation while writers
remain quiesced.

The repository lifecycle fence (Scylla LWT columns on the Namespace row that
serialize namespace deletion against in-flight repository creation) is always
enabled on every backend; it is not configurable and has no rollout gate.
`deleteNamespace`, `completeNamespaceDeletion`, and `createRepository` always
enforce it.

## Stable response codes

Namespace mutations use the shared mutation error envelope: `extensions.code`,
plus `diagnostics[]` (each with a stable `reason`), and for
`ADMISSION_REJECTED` only, `phase` (`PRE_RECEIVE` or `POST_RECEIVE`) and, for
`POST_RECEIVE`, `commit`. No other keys are returned. Clients branch on `code`
first and on `diagnostics[].reason` for detail. Reason values are unchanged
from earlier releases.

| Code | Reasons |
|---|---|
| `ADMISSION_REJECTED` | `INVALID_ENVELOPE`, `INVALID_IDENTIFIER`, `RESERVED_IDENTIFIER`, `INVALID_TIER`, `INVALID_AUTHORING_TARGET`, `DUPLICATE_IDENTITY`, `IMMUTABLE_NAME`, `TIER_DEMOTION` |
| `FAILED_PRECONDITION` | `BOOTSTRAP_NAMESPACE`, `NAMESPACE_TERMINATING`, and for deletion `NAMESPACE_NOT_EMPTY` |
| `ALREADY_EXISTS` | `NAMESPACE_ALREADY_EXISTS` |
| `NOT_FOUND` | `NAMESPACE_NOT_FOUND` |
| `CONFLICT` | `RESOURCE_VERSION_CONFLICT`, `SUPERSEDED` |

### Migrating from the earlier codes

| Earlier `code` | Earlier extensions | Now |
|---|---|---|
| `NAMESPACE_STRUCTURAL_VALIDATION_FAILED` | `phase: STRUCTURAL`, `reason` | `ADMISSION_REJECTED`; same reason in `diagnostics[0].reason` |
| `NAMESPACE_IMMUTABLE_FIELD` | `phase: STRUCTURAL`, `reason` | `ADMISSION_REJECTED`, reason `IMMUTABLE_NAME` |
| `NAMESPACE_POLICY_REJECTED` | `phase: POLICY`, `reason` | By reason: `TIER_DEMOTION` is `ADMISSION_REJECTED`; `BOOTSTRAP_NAMESPACE` and `NAMESPACE_TERMINATING` are `FAILED_PRECONDITION`; `NAMESPACE_ALREADY_EXISTS` is `ALREADY_EXISTS`; `NAMESPACE_NOT_FOUND` is `NOT_FOUND` |
| `NAMESPACE_CONFLICT` | `phase: POLICY`, `reason` | `CONFLICT`, reason `RESOURCE_VERSION_CONFLICT` or `SUPERSEDED` |
| `NOT_FOUND` | `phase: POLICY`, `reason` | `NOT_FOUND`, reason `NAMESPACE_NOT_FOUND` |
| `NAMESPACE_DELETION_BLOCKED` | `reasons[]` | `FAILED_PRECONDITION`, one diagnostic per blocker |
| `RESOURCE_VERSION_CONFLICT` (status writes and `complete*Deletion`, all kinds) | `resourceVersion` | `CONFLICT`, reason `RESOURCE_VERSION_CONFLICT`; the current version is in the diagnostic `message` (`current resourceVersion is N`), not a `resourceVersion` key |

The `phase` values `STRUCTURAL` and `POLICY` and the top-level `reason` and
`reasons` keys no longer appear in error extensions. Update client and alert
matchers that used them.

Deletion blockers are returned as one diagnostic each, ordered
`BOOTSTRAP_NAMESPACE`, then `NAMESPACE_NOT_EMPTY`. Successful deletion returns
`TERMINATION_STARTED` or the idempotent no-write result `ALREADY_TERMINATING`,
including when another replica wins a concurrent deletion request.

Controllers accept both the earlier `RESOURCE_VERSION_CONFLICT` code and
`CONFLICT` and treat either as "re-read and retry", so the API and controller
manager can be upgraded in either order for the error-code change alone.

## Metrics

Monitor:

- `gitstore_namespace_validation_rejections_total{code,reason}`
- `gitstore_namespace_validation_duration_seconds{stage}`
- `gitstore_namespace_deletion_rejections_total{reason}`
- `gitstore_namespace_deletion_outcomes_total{outcome}`
- `gitstore_admission_rejections_total{kind,phase}` (manifests rejected by
  admission; emitted for `CategoryTaxonomy/POST_RECEIVE`,
  `Product/PRE_RECEIVE`, and `Product/POST_RECEIVE`, not for Namespace)

The `phase` label on the two `validation` series was replaced: rejections are
now labelled by `code` (keeping `reason`), and duration by `stage`
(`STRUCTURAL` or `POLICY`). Dashboards and alerts that selected on
`phase="STRUCTURAL"` or `phase="POLICY"` must be rewritten, for example
`stage="POLICY"` for latency or `code="ADMISSION_REJECTED"` for rejections.

Alert on a sustained increase in `reason="RESOURCE_VERSION_CONFLICT"` or
`code="CONFLICT"`, `NAMESPACE_TERMINATING`, or internal GraphQL/gRPC errors.
Correlate policy rejections with deployments and client changes; do not treat
expected user rejections as server failures.

## Capacity and saturation

The Repository lifecycle gate (`make capacity TARGET=repository PROFILE=lifecycle
MODE=production`) includes a bounded mutation/push deletion probe against the two
API replicas and active controllers. It fetches the authoring repository to prove
manifest removal, waits for final resource deletion, recreates the same name with
a new UID, and rejects completion carrying the old UID. It requires the existing
`GIT_URL` and an identity authorized for deletion and controller completion.
Local service tests do not replace this deployed evidence.

Run the opt-in production harness:

```bash
make capacity TARGET=namespace PROFILE=validation MODE=production
```

It uses two independent API helper processes, replaces one replica during the
run, and enforces:

- 500 files/request with at most 50 Namespace manifests;
- 10 requests/second at concurrency 20 for at least 30 minutes;
- p95 at most 100 ms and p99 at most 250 ms;
- internal errors below 0.1% and zero incorrect decisions;
- replacement recovery within 30 seconds;
- CPU below 80%, retained-memory growth below 10%, and post-soak goroutines
  within 5% of baseline for each replica process.

Treat p95 above 80 ms, p99 above 200 ms, CPU above 70%, retained-memory growth
above 8%, goroutine drift above 4%, or recovery above 20 seconds as warning
signals. The enforced limits are critical saturation thresholds. Do not raise
concurrency or request-size limits to mask saturation; first inspect parse
latency, policy datastore latency, CPU, garbage collection, and conflict rate.

## Structured logs

Namespace logs use bounded fields:

- `operation`
- `phase`
- `reason` or `reasons`
- `namespace`
- `outcome`
- `blocker_count`
- `conflict`
- `existing`
- `attempts`

Logs must not contain manifest bodies, authorization headers, credentials,
tokens, or internal datastore identifiers. Use the Namespace name, stable
reason, operation, and request ID to correlate a rejected request.

## Troubleshooting

### Structural rejection unexpectedly includes policy details

Confirm the response contains only structural errors. Structural failures,
including same-path `metadata.name` changes, short-circuit policy evaluation.
Check for mixed binaries that predate the ordered validation pipeline.

### Update rejected with `TIER_DEMOTION`

Read the durable Namespace tier. `ORGANIZATION` to `USER` is not allowed.
Submit a non-demoting update or create a migration plan; do not edit the
datastore directly.

### Update rejected with `NAMESPACE_TERMINATING`

The response code is `FAILED_PRECONDITION` with reason `NAMESPACE_TERMINATING`.

The Namespace already has a deletion timestamp. Stop retries that modify its
spec and either let foreground deletion complete or resolve its deletion
blockers.

### Deletion returns both blockers

For a bootstrap Namespace containing repositories, both
`BOOTSTRAP_NAMESPACE` and `NAMESPACE_NOT_EMPTY` are expected. Bootstrap
Namespaces are system-managed and cannot be deleted. For a non-bootstrap
Namespace, remove or transfer all repositories, then retry deletion.

### Deletion remains terminating

1. Query the Namespace and verify the foreground-deletion finalizer and
   deletion timestamp.
2. Verify `HasRepositories` is false after repository transfer/deletion.
3. Check controller health and completion retries.
4. Investigate resource-version conflicts; completion must reload and retry
   rather than remove the finalizer by hand.
5. A repeated user deletion returning `ALREADY_TERMINATING` is healthy and must
   not advance `resourceVersion`.
6. Ordinary repository create/transfer fence cleanup uses a bounded context
   detached from request cancellation. Repair-required writes intentionally
   retain the fence until a quiesced `scylla-projection-repair --confirm` run
   completes projection repair, verifies a clean audit, and clears the retained
   reservation.

### High conflict or latency rate

Confirm all replicas use the same durable datastore and authoritative Git ref,
then inspect policy-read latency and conditional-write conflicts. During a
rollout, keep clients on legacy selections until schema convergence. Run the
focused replica, rollout, authorization, and capacity-threshold tests before
resuming deployment.
