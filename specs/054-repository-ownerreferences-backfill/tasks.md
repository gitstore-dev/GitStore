# Tasks: Resource OwnerReferences and Deletion Semantics

**Input**: Design documents from `specs/054-repository-ownerreferences-backfill/`
**Prerequisites**: `plan.md`, `spec.md`, `research.md`, `data-model.md`,
`contracts/`, and `quickstart.md`

## Ownership and metadata

- [X] T001 Add backend-neutral owner-reference lifecycle contract tests for every ownership edge, blocking value, and bounded dependent lookup.
- [X] T002 Add admission tests for repository/domain ownership, caller-forged metadata rejection, and UID/scope validation.
- [X] T003 Extend memdb and Scylla projection tests for all edges, multiple owners, replacement, and removal.
- [X] T004 Add GraphQL metadata tests, including the empty ServiceAccount owner-reference list.
- [X] T005 Implement canonical owner identity, relationship resolution, and preservation of unrelated system-managed references.
- [X] T006 Synchronize reverse projections for every resource write, including Collection, File, and simultaneous Repository plus domain ownership.
- [X] T007 Ensure GraphQL converters expose maintained references and ServiceAccount remains a cluster-scoped root.
- [X] T008 Clarify `OwnerReference` documentation without changing the GraphQL shape and regenerate embedded schema sources.

## Deletion semantics

- [X] T009 Add Git admission and GraphQL deletion tests for blocking, orphaning, mixed dependents, and no-cascade outcomes.
- [X] T010 Add concurrent dependent-create/reparent versus owner-removal coverage using shared Scylla state.
- [X] T011 Add bounded Product orphan and retry tests that preserve authored category references.
- [X] T012 Apply consistent blocking-dependent outcomes while retaining Namespace and Repository preconditions.
- [X] T013 Orphan non-blocking dependents without cascade deletion; preserve existing finalizer/CAS workflows.

## Write-time repair and validation

- [X] T014 Add a regression test that starts with a legacy resource missing managed references, performs an ordinary update, and verifies metadata and reverse-projection repair.
- [X] T015 Run focused API tests, memdb datastore contracts, and Scylla datastore unit tests; address regressions.
- [ ] T016 Run `make capacity TARGET=repository PROFILE=lifecycle MODE=production` against an explicitly test-owned environment and record the owner-reference correctness result.
- [X] T017 Run the controller-restart fault profile against the test-owned environment; record the measured 21.4-second recovery.
- [ ] T018 Validate API/controller replica safety, rolling-upgrade behavior, and AuthN/AuthZ isolation; record unverified deployed paths.

Full retained-data backfill, durable backfill progress, and deletion gated on
backfill completion are deferred until a concrete rollout need is identified.
An ordinary resource update repairs the resource being touched; untouched
legacy data is not claimed to be fully reconciled.
