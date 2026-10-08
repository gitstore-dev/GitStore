# Namespace admission operations

## Scope

This runbook covers Namespace structural validation, stateful policy admission,
GraphQL create/update/delete behavior, and the server-first activation of
`DeleteNamespacePayload.outcome`. Authorization must complete before validation,
policy, lifecycle, or blocker details are returned.

## Repository lifecycle fence

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

A rolling upgrade needs no extra quiescing:

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

| Code | Phase | Typical reasons |
|---|---|---|
| `NAMESPACE_STRUCTURAL_VALIDATION_FAILED` | `STRUCTURAL` | `INVALID_ENVELOPE`, `INVALID_IDENTIFIER`, `RESERVED_IDENTIFIER`, `INVALID_TIER`, `INVALID_AUTHORING_TARGET`, `DUPLICATE_IDENTITY` |
| `NAMESPACE_IMMUTABLE_FIELD` | `STRUCTURAL` | `IMMUTABLE_NAME` |
| `NAMESPACE_POLICY_REJECTED` | `POLICY` | `BOOTSTRAP_NAMESPACE`, `TIER_DEMOTION`, `NAMESPACE_TERMINATING`, `NAMESPACE_ALREADY_EXISTS`, `NAMESPACE_NOT_FOUND` |
| `NAMESPACE_DELETION_BLOCKED` | deletion | `BOOTSTRAP_NAMESPACE`, `NAMESPACE_NOT_EMPTY` |
| `NAMESPACE_CONFLICT` | write | `RESOURCE_VERSION_CONFLICT` |

Deletion blockers are ordered `BOOTSTRAP_NAMESPACE`, then
`NAMESPACE_NOT_EMPTY`. Successful deletion returns `TERMINATION_STARTED` or the
idempotent no-write result `ALREADY_TERMINATING`, including when another replica
wins a concurrent deletion request.

## Metrics

Monitor:

- `gitstore_namespace_validation_rejections_total{phase,reason}`
- `gitstore_namespace_validation_duration_seconds{phase}`
- `gitstore_namespace_deletion_rejections_total{reason}`
- `gitstore_namespace_deletion_outcomes_total{outcome}`

Alert on a sustained increase in `RESOURCE_VERSION_CONFLICT`,
`NAMESPACE_TERMINATING`, or internal GraphQL/gRPC errors. Correlate policy
rejections with deployments and client changes; do not treat expected user
rejections as server failures.

## Capacity and saturation

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
