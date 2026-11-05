# Operations and Recovery

Runbooks are **conditional procedures**, not a list of recurring chores.
Start with deployment configuration and health monitoring. Use a recovery
guide when its symptom appears, or an upgrade/rotation guide when planning
that change.

Controllers normally retry transient failures, reconnect watches, and relist
after an expired cursor automatically. A growing backlog during an outage
does not require manual queue clearing. Intervene when recovery stops making
progress, data is quarantined, or a documented compatibility change requires
action.

## Deployment and planned changes

| Need | Guide |
| --- | --- |
| Configure and use GitStore | [User guide](../user-guide.md) |
| Provision the reference cloud deployment | [DigitalOcean provisioning](digitalocean-provisioning.md) |
| Upgrade versions, policies, clients, or schemas | [Upgrade compatibility](upgrades.md) |
| Rotate credential or signing material | [Secret material rotation](secret-material-rotation.md) |

Keep backups and retention appropriate to your deployment. Restarts do not
require discarding Git, datastore, or controller checkpoint storage. The Git
service supports exactly one active process: retain its storage and use
non-overlapping replacement, not concurrent Git replicas.

## Diagnose an incident

| Symptom | Guide |
| --- | --- |
| Controller lag, expired replay cursor, or quarantined work | [Controller recovery](controller-recovery.md) |
| Watch disconnects, status conflicts, or journal readiness | [Watch and status diagnostics](controller-watch-status.md) |
| Controller token exchange or credential failures | [Controller authentication](controller-auth.md) |
| Missing or inconsistent Scylla projections | [Projection audit and repair](scylla-projection-repair.md) |
| Namespace admission rejection or termination blocked | [Namespace admission](namespace-admission.md) |
| Product or Category deletion not completing | [Catalog lifecycle](catalog-lifecycle.md) |
| Containers, ports, Compose, or local startup failing | [Docker troubleshooting](docker-troubleshooting.md) |

Repair and requeue procedures are exception handling. Diagnose the cause first;
do not routinely rewrite projections, delete checkpoints, remove finalizers,
or reset persistent data.

## Contributor procedures

Building a release and proving production readiness are contributor work,
not deployment maintenance:

- [Release process](../development/release-process.md)
- [Production-readiness testing](../development/production-readiness-testing.md)
- [Capacity profiles and benchmark observations](../../tests/capacity/README.md)
