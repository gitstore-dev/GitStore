# Tasks: Resource OwnerReferences and Deletion Semantics

**Input**: Design documents from `specs/054-repository-ownerreferences-backfill/`
**Prerequisites**: `plan.md`, `spec.md`, `research.md`, `data-model.md`, `contracts/`, `quickstart.md`

**Tests**: Test-first is required by the implementation plan and Constitution Principle I. Write the focused tests below before the corresponding implementation, and confirm they fail for the missing behavior.

**Organization**: Tasks are grouped by the three P1 user stories in `spec.md`.

## Phase 1: Setup

**Purpose**: No project initialization is needed. Reuse the existing Go modules, datastore backends, `gitctl`, controller, and integration harness named in `plan.md`.

## Phase 2: Foundational

**Purpose**: Establish a backend-neutral contract for the ownership matrix and bounded reverse-dependent lookup before implementing story-specific behavior.

- [X] T001 [P] Add backend-neutral owner-reference lifecycle contract tests for every ownership edge, blocking value, and bounded dependent lookup in `gitstore-api/tests/contract/datastore/contract_test.go`

**Checkpoint**: The shared contract is explicit and can be implemented by the memdb and Scylla backends.

## Phase 3: User Story 1 - Every resource reports its lifecycle owner (Priority: P1)

**Goal**: Maintain exactly the system-owned owner references in the matrix across all resource kinds, including the empty root metadata for Namespace and ServiceAccount.

**Independent Test**: Exercise every edge in FR-002 and read each dependent; verify the correct owner identity and blocking flag, verify unrelated references are preserved, and verify Namespace and ServiceAccount have no synthetic owner.

### Tests for User Story 1

- [X] T002 [P] [US1] Add admission tests for all repository and domain ownership edges, caller-forged metadata rejection, and UID/scope validation in `gitstore-api/internal/cataloggrpc/server_test.go`
- [X] T003 [P] [US1] Extend memdb and Scylla owner-reference projection tests to cover every edge, multiple owners, replacement, and removal in `gitstore-api/internal/datastore/memdb/owner_references_test.go` and `gitstore-api/internal/datastore/scylla/owner_references_integration_test.go`
- [X] T004 [P] [US1] Add GraphQL metadata projection tests for catalog resources and the non-null empty ServiceAccount owner-reference list in `gitstore-api/internal/admin/graph/resolver/converters_test.go` and `gitstore-api/internal/admin/graph/resolver/serviceaccount_service_test.go`

### Implementation for User Story 1

- [X] T005 [US1] Implement canonical owner identity, relationship resolution, and preservation of unrelated system-managed references in `gitstore-api/internal/catalog/status.go` and `gitstore-api/internal/cataloggrpc/server.go`
- [X] T006 [US1] Synchronize the reverse owner projection for every resource write, including Collection, File, and simultaneous Repository plus domain ownership, in `gitstore-api/internal/datastore/entities.go`, `gitstore-api/internal/datastore/memdb/owner_references.go`, and `gitstore-api/internal/datastore/scylla/owner_references.go`
- [X] T007 [US1] Ensure GraphQL resource converters expose the maintained owner references and ServiceAccount remains a cluster-scoped root with empty references in `gitstore-api/internal/admin/graph/resolver/converters.go` and `gitstore-api/internal/admin/graph/resolver/serviceaccount_service.go`
- [X] T008 [US1] Clarify the consumer-facing `OwnerReference` docstring without changing its GraphQL shape in `shared/schemas/admin/schema.graphqls`, then regenerate embedded schema sources with `cd gitstore-api && go generate ./...`

**Checkpoint**: The metadata contract is independently verifiable through admission, both datastore backends, and GraphQL reads.

## Phase 4: User Story 2 - Deleting an owner treats all dependents consistently (Priority: P1)

**Goal**: A live `blockOwnerDeletion: true` dependent prevents owner removal; a `false` dependent survives as an orphan. Neither path cascades deletion, and Git and GraphQL entry points agree.

**Independent Test**: For each owner kind and supported deletion entry point, test blocking, non-blocking orphaning, mixed dependents, asynchronous finalization, and repeated or concurrent requests. Confirm the owner and blocking dependent remain intact and no dependent is cascaded.

### Tests for User Story 2

- [X] T009 [P] [US2] Add Git admission and GraphQL deletion tests for blocking, orphaning, mixed dependents, and no-cascade outcomes in `gitstore-api/internal/cataloggrpc/owner_reference_deletion_test.go` and `gitstore-api/internal/admin/graph/resolver/owner_reference_deletion_test.go`
- [X] T010 [P] [US2] Add concurrent dependent-create/reparent versus owner-removal coverage using shared Scylla state in `gitstore-api/internal/datastore/scylla/owner_references_integration_test.go`
- [X] T011 [P] [US2] Add bounded Product orphan and retry tests that preserve authored category references while clearing only stale system-owned metadata in `gitstore-controller-manager/internal/categorytaxonomy/products_test.go`

### Implementation for User Story 2

- [X] T012 [US2] Apply the same blocking-dependent outcome to Git admission and resource deletion checks, retaining existing Namespace and Repository preconditions, in `gitstore-api/internal/cataloggrpc/server.go` and `gitstore-api/internal/admin/graph/resolver/infrastructure_deletion.go`
- [X] T013 [US2] Orphan non-blocking dependents without cascade deletion, clear only obsolete system-managed owner metadata, and preserve existing finalizer/CAS workflows in `gitstore-api/internal/cataloggrpc/server.go` and `gitstore-controller-manager/internal/categorytaxonomy/products.go`

**Checkpoint**: Existing deletion entry points agree on blocker and orphan outcomes; deletion remains safe while indexed enforcement is still gated on backfill completion.

## Phase 5: User Story 3 - Existing resources become safe before enforcement (Priority: P1)

**Goal**: Repair legacy relationships with a bounded, idempotent, resumable operator command and enable index-backed deletion only after verified completion.

**Independent Test**: Seed legacy relationships for every matrix edge, interrupt and resume backfill, and verify exact-once metadata, projection parity, durable per-kind progress, explicit unresolved relationships, and fail-closed deletion before completion.

### Tests for User Story 3

- [X] T014 [P] [US3] Add datastore contract tests for durable cursor advancement, lease/fencing, retries, interruption/resume, verification, and incomplete-state deletion failures in `gitstore-api/internal/datastore/owner_reference_backfill_test.go`
- [X] T015 [P] [US3] Add Scylla migration and process-restart tests for durable progress and completion state in `gitstore-api/internal/datastore/scylla/migration_test.go` and `gitstore-api/internal/datastore/scylla/owner_references_integration_test.go`
- [X] T016 [P] [US3] Add `gitctl` command tests for explicit deployment selection, bounded pages, status, unresolved relationships, interruption, and resume in `gitstore-api/cmd/gitctl/owner_reference_backfill_test.go`

### Implementation for User Story 3

- [X] T017 [US3] Define backend-neutral owner-reference backfill progress, lease, cursor, and completion contracts in `gitstore-api/internal/datastore/datastore.go`
- [X] T018 [US3] Implement deterministic backfill progress and completion behavior for contract tests in `gitstore-api/internal/datastore/memdb/owner_reference_backfill.go`
- [X] T019 [US3] Add incremental migration `013_owner_reference_backfill.cql` and implement shared durable Scylla progress, lease/fencing, and verification state in `gitstore-api/internal/datastore/scylla/migrations/013_owner_reference_backfill.cql` and `gitstore-api/internal/datastore/scylla/owner_reference_backfill.go`; do not edit applied migrations
- [X] T020 [US3] Implement bounded per-kind scanning, UID/resource-version-safe metadata repair, durable checkpointing, verification, and status reporting in `gitstore-api/cmd/gitctl/owner_reference_backfill.go` and register the subcommand in `gitstore-api/cmd/gitctl/main.go`
- [X] T021 [US3] Gate index-backed deletion on the verified durable completion marker and fail closed on missing, incomplete, or unreadable progress state in `gitstore-api/internal/datastore/scylla/owner_references.go` and `gitstore-api/internal/cataloggrpc/server.go`

**Checkpoint**: Backfill can resume after process replacement; enforcement cannot interpret incomplete coverage as an empty dependent set.

## Phase 6: Polish & Cross-Cutting Concerns

**Purpose**: Validate upgrade safety, sustained-load correctness, operational recovery, and implementation instructions.

- [X] T022 [P] Extend the existing repository lifecycle capacity correctness oracle for owner metadata, blocking deletion, and orphan preservation in `tests/integration/repository_lifecycle_capacity_test.go`
- [X] T023 Update `specs/054-repository-ownerreferences-backfill/quickstart.md` with the implemented `gitctl` syntax, status output, rollout order, verification steps, and failure handling
- [ ] T024 Run the supported production Repository lifecycle capacity scenario and record the actual owner-reference correctness result in `specs/054-repository-ownerreferences-backfill/quickstart.md` using `make capacity TARGET=repository PROFILE=lifecycle MODE=production`
- [X] T025 Run the existing controller-restart fault profile against an explicitly test-owned environment and record the measured recovery result in `specs/054-repository-ownerreferences-backfill/quickstart.md` using `CHAOS_TARGET=<test-owned-controller-container> make chaos CHAOS_PROFILE=controller-restart`
- [ ] T026 Validate API and controller replica safety, rolling-upgrade behavior, AuthN/AuthZ isolation, and bounded backfill under the applicable integration suites; record any unverified deployed paths in `specs/054-repository-ownerreferences-backfill/quickstart.md`

## Dependencies & Execution Order

### Phase Dependencies

- **Setup (Phase 1)**: No initialization work; use the existing modules and test harness.
- **Foundational (Phase 2)**: T001 establishes the cross-backend lifecycle contract and precedes story implementation.
- **User Stories**: US1 establishes complete writes and projections. US2 consumes those relationships for consistent deletion behavior. US3 depends on US1's complete writers and is the only phase that may enable indexed enforcement after verification.
- **Polish (Phase 6)**: Depends on the implementation and focused tests for all three stories.

### User Story Dependencies

- **US1 (P1)**: Depends on T001; establishes owner metadata and projections.
- **US2 (P1)**: Depends on T001 and US1; implement and verify common deletion outcomes while retaining existing safeguards.
- **US3 (P1)**: Depends on T001 and US1; backfill can proceed after old writers drain. Indexed enforcement remains disabled until its durable completion gate is verified.

### Within Each User Story

- Write and run the focused failing tests before implementation tasks.
- Complete metadata production and projection parity before deletion relies on the relationship set.
- Keep current deletion guards until all old writers are drained and backfill verification has durably completed.
- Keep migration `013` additive; never rewrite checksummed migration history.

### Parallel Opportunities

- T002, T003, and T004 can proceed in parallel because they target separate test files and components.
- T009, T010, and T011 can proceed in parallel after US1 because they cover independent deletion paths.
- T014, T015, and T016 can proceed in parallel because they establish independent datastore, Scylla, and CLI test coverage.
- T018 and T019 can proceed in parallel after T017 and their corresponding tests; T020 follows the datastore contract and progress implementation.
- US2 testing and implementation may be developed alongside US3 progress work, but indexed enforcement must not be enabled until US3 verification succeeds.

## Parallel Example: User Story 1

```text
Task: T002 - admission ownership and spoofing tests
Task: T003 - memdb and Scylla projection parity tests
Task: T004 - GraphQL metadata and ServiceAccount projection tests
```

## Parallel Example: User Story 3

```text
Task: T014 - datastore progress and fail-closed contract tests
Task: T015 - Scylla migration and restart tests
Task: T016 - gitctl backfill command tests
```

## Implementation Strategy

1. Establish T001 and complete US1 as the first independently verifiable increment.
2. Complete US2 while retaining all existing deletion safeguards.
3. Implement and resume-verify US3; only then may indexed deletion enforcement be enabled.
4. Finish the production lifecycle capacity and controller-restart evidence before declaring rollout readiness.

**MVP scope**: US1 provides complete, system-managed relationship metadata and projection parity while existing deletion guards remain in force. It is not safe to enable new index-backed deletion enforcement until US3 backfill verification is complete.
