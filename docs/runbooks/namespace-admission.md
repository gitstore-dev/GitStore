# Namespace admission operations

## Scope

This runbook covers Namespace structural validation, stateful policy admission,
GraphQL create/update/delete behavior, and the server-first activation of
`DeleteNamespacePayload.outcome`. Authorization must complete before validation,
policy, lifecycle, or blocker details are returned.

## Repository lifecycle fence

### Git-backed deletion rollout

The deletion protocol requires a coordinated writer transition, distinct from
the older fence-only rollout below. Quiesce infrastructure deletes and old
controllers, upgrade the singleton Git service with retained storage and
non-overlapping replacement, upgrade all API writers, then upgrade controllers.
Resume deletion only after old writers are drained. An old Git service ignores
additive deletion preconditions; an old API can still bypass Git removal.
Do not roll back to either while new-format deletion operations exist.

Scylla deployments must also apply migration `012_repository_catalog_index.cql`
and backfill the catalog blocker index before resuming deletion. After draining
old API writers, keep catalog writes, repository creation/rename/transfer,
lifecycle transitions, and controller writes quiesced throughout repair and
verification. Run the existing commands from `gitstore-api/`:

```bash
go run ./cmd/gitctl scylla-projection-audit --hosts "$HOSTS" --keyspace "$KEYSPACE"
go run ./cmd/gitctl scylla-projection-repair --dry-run --hosts "$HOSTS" --keyspace "$KEYSPACE"
go run ./cmd/gitctl scylla-projection-repair --confirm --hosts "$HOSTS" --keyspace "$KEYSPACE"
go run ./cmd/gitctl scylla-projection-audit --hosts "$HOSTS" --keyspace "$KEYSPACE"
```

Supply database passwords through `GITSTORE_API__DATASTORE__SCYLLA__PASSWORD`.
The repair commands cover all projections, not just the catalog blocker index.
Pre-existing repositories without a readiness marker fail closed rather than
appearing empty. Repair publishes readiness only after complete verification;
an interrupted repair can be retried with a fresh audit and confirmed repair.
These markers guard initial backfill, not subsequent manual index corruption.
Resume writers and upgraded controllers only after the final audit is clean.

Controller `completeNamespaceDeletion` and `completeRepositoryDeletion` inputs
must carry the current GraphQL `metadata.uid` as `uid`, in addition to
`resourceVersion` and name. The schema field is additive/nullable for discovery,
but upgraded servers reject omission. A conflict requires a fresh resource read,
never retrying stale work against a same-name replacement.

An empty, verified system repository does not block Namespace initiation. Its
catalog resources do, as do all ordinary repositories (including terminating
ones). Completion cleans up system storage by UID before releasing the Namespace.
Neither path cascades catalog resources or ordinary repositories.

`DeletionPending=True` records recoverable mutation work before the Git call.
Controllers resume that work after API replacement. `SUPERSEDED` means the
manifest content changed: do not overwrite it or clear the operation blindly.
`DELETION_REPAIR_REQUIRED` blocks legacy termination without removal evidence.
Inventory retained manifests and terminating rows before the transition, compare
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

For a legacy terminating resource with a retained manifest, use an authenticated
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

### Upgrading from a release with the fence rollout setting

Earlier releases controlled the fence with `features.namespace_repository_fence`.
Remove that key, and the `GITSTORE_FEATURES__NAMESPACE_REPOSITORY_FENCE`
environment variable, before deploying. The API refuses to start while either is
present.

A fence-only rolling upgrade needs no extra quiescing (this does not apply to
the Git-backed deletion protocol above):

- Older replicas with the fence disabled (the `auto` default on Scylla) do not
  run the three mutations unfenced. They reject them with
  `NAMESPACE_REPOSITORY_FENCE_DISABLED`.
- Older replicas with the fence enabled, and all upgraded replicas, enforce it.

No replica can therefore let repository creation race namespace deletion. Until
the last older replica is replaced, requests that reach one fail and can be
retried. Rolling back to such a release has the same effect: the three
mutations are rejected until the fence is enabled on the older fleet.

A binary built before the per-resource schema baseline is **not** a supported
rollback artifact. gocqlx correctly rejects a keyspace whose migration history
it does not recognise. Reverting an API replica before disabling `outcome`
selections also causes GraphQL validation failures on requests routed to the old
schema.

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
