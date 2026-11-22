# Translation safety and recovery

## Availability and activation

**Translation admission and reconciliation are disabled by default; an enabled
deployment is not yet certified.** Leave
`GITSTORE_API__TRANSLATION__ENABLED=false` and
`GITSTORE_CONTROLLER__TRANSLATION__ENABLED=false` for production deployments.
A repaired index, saved Verified result or healthy endpoint is not an
activation certificate.

There is no supported activation command in this release. Do not bypass the
gate by inserting rows, replaying unsigned callbacks, editing status or
relaxing authorization. An enabled release must provide deployment-wide
reader/writer/controller compatibility, compatible descriptors, complete
target-writer fencing and connected upgrade/recovery acceptance.

Enabled API and controller configuration requires an explicit matching
`translation.descriptor_digest` in each component's configuration. A missing
pin or any configured mismatching pin fails startup. A matching pin establishes
only local semantic compatibility, not the deployment-wide prerequisites above.

Use this guide to preserve retained data, diagnose disabled-feature errors and
perform the already available bounded maintenance operations. It does not
certify an enabled deployment or a sustainable workload.

## Diagnose the boundary first

| Symptom | Action |
| --- | --- |
| Startup rejects a Translation capability pin | Keep production activation disabled. Check for a missing enabled-component pin or incompatible build/configuration; do not remove a pin merely to make startup pass. |
| Translation receive, query or replay reports unavailable | Check activation and the requested runtime role first. Do not retry by writing directly to storage. |
| Target proof is unavailable or repair-required | Inspect Namespace lifetime, target-writer health and bounded audit results; do not classify the target as missing. |
| A target-read or status write is forbidden | Repair scoped policy or controller enrollment. Do not grant target material writes to solve a validation failure. |
| Status is absent or not current | Do not interpret it as Ready. Retain the source and stored observation while investigating authorization, proof and controller progress. |
| An old watch event has no status | Historical replay does not assert current validation; obtain a current authorized read. |

`lastAppliedRevision` refers to the Translation's reconciled source, not a
write to its target. `Condition.lastTransitionTime` records condition changes,
not every validation check. Neither value alone proves that evidence is current.
Translation never applies localized content to a target.

For general outages, expired cursors, quarantined work or enrollment failures,
use [controller recovery](controller-recovery.md),
[watch/status diagnostics](controller-watch-status.md) and
[controller authentication](controller-auth.md). Do not clear checkpoints,
binding history, finalizers or durable work as routine maintenance.

## Bounded maintenance

Prerequisites: an authorized operator host, the intended Scylla configuration,
an approved backup/retention policy and the **current Namespace UID**. Confirm
deployment ownership before changing state. Keep credentials in the existing
environment/secret mechanism, not command arguments or shared evidence.

Follow [projection audit and repair](scylla-projection-repair.md#bounded-translation-maintenance)
for the supported commands and Scylla safety precautions. From `gitstore-api/`,
preview one target-proof turn without writing:

```bash
go run ./cmd/server gitctl scylla-projection-audit \
  --kind Translation --audit-scope targets --namespace-uid "$NAMESPACE_UID" \
  --page-size 256 --timeout 1m
```

Use `--audit-scope indexes` to inspect source/index traversal instead.
Both scopes accept page sizes from 1 to 256 and a positive deadline.
Preview and repair dry-run do not advance progress.

After reviewing findings and approving the mutation, perform one bounded turn:

```bash
go run ./cmd/server gitctl scylla-projection-repair --confirm \
  --kind Translation --audit-scope targets --namespace-uid "$NAMESPACE_UID" \
  --page-size 256 --timeout 1m
```

Repeat confirmed turns only as needed, observing the server's persisted
progress. After a conflict or uncertain reply, reload current progress rather
than reusing an old cursor or plan. Stop and investigate persistent conflicts,
malformed reservations or missing original Namespace birth authority; never
reconstruct authority from a reused name.

An index traversal ending in Scanned is not a consistency certificate. Target
Verified records a completed verification pass under its coverage epoch, not
permanent coverage or participation by every deployed writer. Pending material
remains unavailable. Check current audit/proof results and normal reads after
repair; a saved successful result is insufficient.

## Rollback and preservation

Before any Translation has been accepted, keep admission disabled during
rollback and retain the additive schema. Verify that every receiving, reading
and replaying instance remains in the disabled configuration.

If retained accepted Translation data exists, do not roll readers or replay
consumers back to a build that rejects or skips the kind. Keep the last capable
build while forward-fixing; escalate if compatibility cannot be established.
Disabling new authoring does not make an incapable reader safe.

Preserve accepted Git documents, UIDs, binding history, the datastore, journal
and each controller's independent checkpoint storage. Never reset a keyspace,
rewrite Git history, drop additive tables or delete data to make a downgrade
appear compatible. Recheck source identity/revision and unchanged target
authored/resolved content after recovery.

The Git service remains singleton-only. If it needs replacement, stop the old
process before starting one replacement on retained storage. Do not overlap
Git processes or claim zero-downtime failover.
