# Owner-Reference Lifecycle Verification

This guide is the implementation acceptance path for
[Spec 054](./spec.md). Run all deployed checks only against explicitly
test-owned environments. Do not target an operator deployment.

## 1. Run backend-neutral datastore contracts

```sh
make test TARGET=datastore DATASTORE=memdb
```

Verify owner-reference writes, replacement/removal, bounded blocking queries,
backfill cursor/resume, duplicate retries, and fail-closed completion behavior
for every resource kind in the ownership matrix.

## 2. Run Scylla datastore contracts

Against an isolated test-owned Scylla keyspace:

```sh
SCYLLA_TEST_ADDR=127.0.0.1:9042 make test TARGET=datastore DATASTORE=scylla
```

Verify the additive migration, durable progress/lease behavior, resource
version compare-and-set, reverse projection parity, and restart/resume. Do not
edit or reseed an unrelated keyspace.

## 3. Verify API and controller outcomes

Run the focused API admission/resolver and controller tests, then the
repository-level test suite:

```sh
make test
```

For each ownership edge, verify:

- persisted owner reference fields and reverse dependent row;
- `true` dependent blocks deletion through every supported request path;
- `false` dependent survives owner deletion and loses only its stale
  system-managed reference;
- no dependent is cascaded;
- same-push deletion/reparent races and repeated requests converge;
- ServiceAccount returns an empty non-null owner-reference list and remains
  cluster-scoped.

## 4. Exercise backfill rollout

On a test-owned deployment, upgrade all API writers to the dual-write version
while retaining existing deletion safeguards. Confirm old API writers are
drained before running the operator command against the explicitly selected
test-owned Scylla deployment:

```sh
gitctl owner-reference-backfill run \
  --hosts <test-owned-scylla-hosts> \
  --keyspace <test-owned-keyspace> \
  --confirm \
  --page-size 100 \
  --max-pages 100

gitctl owner-reference-backfill status \
  --hosts <test-owned-scylla-hosts> \
  --keyspace <test-owned-keyspace>
```

`run` is mutating and requires `--confirm`, explicit `--hosts` and `--keyspace`,
and bounded page/page-count limits. The default lease holder uses the host and
process ID; `--holder` may be supplied for an operator identity. If Scylla
authentication is enabled, pass `--username` and provide the password through
`GITSTORE_API__DATASTORE__SCYLLA__PASSWORD`. The command does not print
credentials.

The JSON status reports whether progress is initialized, global completion,
and per-kind cursor, scanned, repaired, unresolved, and complete fields.
Interrupt a run after a checkpoint and rerun the same command to resume. A
completed scan with unresolved relationships exits unsuccessfully and keeps
the completion marker unset; repair or explicitly reconcile the reported
relationships before retrying. Do not treat a partial status as success.

Verify per-kind progress, no skipped rows, projection parity, and a durable
`completed` status with zero unresolved relationships. Only after all old
writers are drained and this completion marker is durable may index-backed
deletion checks pass. Before that, the API fails closed rather than treating an
incomplete projection as an empty dependent set.

## 5. Validate deployed capacity and recovery

Use the supported Repository lifecycle acceptance, which exercises the retained
five-million-Product dataset, Git admission, two API replicas, and two
controller replicas. Extend its correctness oracle to check owner-reference
and deletion outcomes: a Repository exposes its blocking Namespace owner;
deleting a CategoryTaxonomy with a blocking child is rejected without removing
either resource; deleting a CategoryTaxonomy with a Product's non-blocking
reference preserves the Product and clears only that stale reference.

```sh
make capacity TARGET=repository PROFILE=lifecycle MODE=production
```

Run the existing controller replacement fault profile against the same
test-owned environment:

```sh
CHAOS_TARGET=<test-owned-controller-container> \
make chaos CHAOS_PROFILE=controller-restart
```

The test-owned controller-restart run on 2026-11-06 completed the injected
restart successfully. The replacement process became ready 21.4 seconds after
startup, within the profile's 60-second recovery objective. The Product work
queue was still about 4.67 million with active workers at the observation point;
this measures controller recovery, not completion of the retained dataset's
reconciliation backlog.

The retained-data owner-reference backfill remains incomplete: Namespace
scanned 41 rows, repaired 2, and has 0 unresolved relationships; Repository
scanned 639 rows, repaired 21, and has 0 unresolved relationships but is not
complete. Product scanning has not started. A keyspace-wide projection audit
was OOM-killed before producing output, so its repair dry-run was not reached
and no projection repair was applied. Rerun the audit from a controlled
operator host with enough memory for the retained dataset before attempting
repair or claiming T024/T026 complete.

Use the actual owned-deployment configuration and receipts required by the
capacity and chaos guides. A diagnostic or local test pass is not production
certification. Git remains one active process with retained storage and
non-overlapping replacement.
