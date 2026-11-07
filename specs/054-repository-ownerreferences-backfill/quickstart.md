# Owner-Reference Lifecycle Verification

This guide is the acceptance path for [Spec 054](./spec.md). Run deployed
checks only against explicitly test-owned environments. Do not target an
operator deployment.

## 1. Run datastore and API tests

```sh
make test TARGET=datastore DATASTORE=memdb
cd gitstore-api && go test ./internal/cataloggrpc ./internal/admin/graph/resolver ./internal/datastore/...
```

Verify every ownership edge, create/update repair of missing or stale
references, reverse-projection updates, bounded blocking queries, and
foreground/background outcomes. A touched resource should be repaired by its
ordinary update; do not run a fleet-wide scan as part of this acceptance path.

## 2. Run Scylla projection tests

Against an isolated test-owned Scylla keyspace:

```sh
SCYLLA_TEST_ADDR=127.0.0.1:9042 make test TARGET=datastore DATASTORE=scylla
```

Verify owner-reference projection parity, resource-version compare-and-set,
and deletion races. Do not edit or reseed an unrelated keyspace.

## 3. Verify deletion outcomes

For each applicable owner kind and supported deletion entry point, verify:

- a live `true` dependent blocks owner removal;
- a `false` dependent survives as an orphan with only stale system-managed
  metadata cleared;
- no dependent is cascaded;
- same-push deletion/reparent races and repeated requests converge;
- ServiceAccount remains cluster-scoped and exposes an empty non-null
  owner-reference list.

## 4. Validate deployed capacity and recovery

Use the supported Repository lifecycle acceptance, which exercises the
retained five-million-Product dataset, Git admission, two API replicas, and
two controller replicas:

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
queue was still about 4.67 million with active workers at the observation
point; this measured controller recovery, not completion of the retained
dataset's reconciliation backlog.

## 5. Retained-data notes

The earlier, explicitly authorized exploratory backfill stopped before
Product scanning. It repaired owner-reference metadata for 2 Namespace and
21 Repository records; those updates remain. No Product scan, projection
repair, or resource deletion was performed. These partial updates are not
evidence of complete historical coverage.

The temporary backfill migration was applied only to the test-owned keyspace
`gitstore451_admitted_20261021b`. Its dedicated table and matching
`gocqlx_migrate` record are removed as part of this scope rollback. The
retained resource tables and five-million-Product data are not changed.

T024 production capacity validation and T026 replica/upgrade/AuthN/AuthZ
validation remain incomplete. A diagnostic or local test pass is not
production certification. Git remains one active process with retained
storage and non-overlapping replacement.
