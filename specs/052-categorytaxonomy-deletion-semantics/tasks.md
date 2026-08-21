# Tasks: CategoryTaxonomy Deletion Semantics

**Input**: Design documents from `/specs/052-categorytaxonomy-deletion-semantics/`
**Prerequisites**: `plan.md`, `spec.md`, `research.md`, `data-model.md`, `contracts/deletion-semantics.md`, `quickstart.md`

**Tests**: Test-first is required by the constitution. Write every listed test,
observe its expected failure, then implement the corresponding behavior.

**Organization**: Tasks are grouped by user story so each outcome can be completed
and tested independently after the shared foundation is available.

## Phase 1: Setup

**Purpose**: Establish reusable deletion fixtures and make the test scenarios
repeatable without changing product behavior.

- [x] T001 [P] Add parent/child CategoryTaxonomy admission fixtures and Git-delete request helpers in `gitstore-api/internal/cataloggrpc/server_test.go`
- [x] T002 [P] Add owner-reference and terminating-record fixture builders in `gitstore-api/internal/datastore/testutil_test.go`
- [x] T003 [P] Add a two-controller/restart fixture that can seed category and Product caches in `gitstore-controller-manager/tests/integration/categorytaxonomy_deletion_test.go`
- [x] T004 [P] Add a failed-admission mock response and rejected-push assertion helper in `gitstore-git-service/src/git/hooks/admission_handler.rs`

---

## Phase 2: Foundational

**Purpose**: Add the additive metadata, bounded reverse lookup, lifecycle transport,
and observability that all deletion stories require.

**⚠️ CRITICAL**: Complete this phase before implementing any user story.

- [x] T005 [P] Add failing catalog and GraphQL model tests for `OwnerReference.blockOwnerDeletion` and legacy-default decoding in `gitstore-api/internal/catalog/status_test.go` and `gitstore-api/internal/graph/model/models_gen_test.go`
- [x] T006 [P] Add failing reverse-owner lookup contract tests for limit-one blocking checks and keyset Product pages in `gitstore-api/internal/datastore/memdb/owner_references_test.go` and `gitstore-api/internal/datastore/scylla/owner_references_test.go`
- [x] T007 [P] Add failing list/watch decoding tests for deletion timestamp, finalizers, and owner references in `gitstore-controller-manager/internal/listwatch/graphql_listwatcher_test.go`
- [x] T008 [P] Add failing lifecycle status-patch and optimistic-completion conflict tests in `gitstore-controller-manager/internal/categorytaxonomy/reconciler_test.go` and `gitstore-api/internal/datastore/datastore_test.go`
- [x] T009 Add `BlockOwnerDeletion` to catalog, GraphQL, and generated model mappings in `gitstore-api/internal/catalog/status.go`, `gitstore-api/shared/schemas/category.graphqls`, and `gitstore-api/internal/graph/model/models_gen.go`
- [x] T010 Add the scoped owner-dependent query and CategoryTaxonomy mark/complete lifecycle methods to `gitstore-api/internal/datastore/datastore.go`
- [x] T011 Implement the indexed memdb owner-reference projection and atomic source-record maintenance in `gitstore-api/internal/datastore/memdb/schema.go` and `gitstore-api/internal/datastore/memdb/backend.go`
- [x] T012 Implement the scope-partitioned Scylla owner-reference projection, keyset query, and recovery behavior in `gitstore-api/internal/datastore/scylla/models.go`, `gitstore-api/internal/datastore/scylla/backend.go`, and `gitstore-api/internal/datastore/scylla/recovery.go`
- [x] T013 Extend CategoryTaxonomy GraphQL list/watch selection and cache records with finalizers, deletion timestamp, and owner references in `gitstore-controller-manager/internal/listwatch/graphql_listwatcher.go` and `gitstore-controller-manager/internal/categorytaxonomy/reconciler.go`
- [x] T014 Add structured lifecycle metrics and logs for dependent lookup latency, blocked deletes, Product page progress, conflicts, and retries in `gitstore-controller-manager/internal/categorytaxonomy/reconciler.go` and `gitstore-api/internal/cataloggrpc/server.go`

**Checkpoint**: Both datastore backends support bounded owner-dependent lookup, and
API/controller can observe lifecycle metadata without enabling deletion behavior.

---

## Phase 3: User Story 1 - Deleting a category with live children is rejected (Priority: P1) 🎯 MVP

**Goal**: A Git push cannot delete a category while any child has a blocking owner
reference; deletion succeeds immediately once no blocking child remains.

**Independent Test**: Admit a parent and child, delete the parent manifest through
Git admission, and verify rejected push plus unchanged record/status. Reparent or
delete the child and verify the same parent delete is accepted.

- [x] T015 [P] [US1] Add API proposed-tree validation tests for child-owned-reference rejection, childless success, atomic child deletion, and reparenting in `gitstore-api/internal/cataloggrpc/server_test.go`
- [x] T016 [P] [US1] Add Rust pre-receive hook tests that map the API precondition outcome to a rejected Git push and skip API calls for creates/updates in `gitstore-git-service/src/git/hooks/category_taxonomy_deletion_handler.rs`
- [x] T017 [P] [US1] Add failing parent-resolution and reparenting tests that write/replace/remove the child blocking owner reference in `gitstore-api/internal/cataloggrpc/server_test.go`
- [x] T018 [US1] Write and maintain CategoryTaxonomy parent `blockOwnerDeletion=true` owner references during admission and resolution in `gitstore-api/internal/cataloggrpc/server.go`
- [x] T019 [US1] Replace direct CategoryTaxonomy hard deletion with scoped blocking-dependent lookup and idempotent mark-delete behavior in `gitstore-api/internal/cataloggrpc/server.go` and `gitstore-api/internal/cataloggrpc/admission_operations.go`
- [x] T020 [US1] Propagate failed delete operations from `AdmitResources` instead of logging-and-continuing in `gitstore-api/internal/cataloggrpc/server.go`
- [x] T021 [US1] Convert the CategoryTaxonomy deletion precondition to a stable rejected-push response in `gitstore-git-service/src/git/hooks/category_taxonomy_deletion_handler.rs`

**Checkpoint**: Git deletion with a child is rejected; a childless deletion enters
the durable deletion lifecycle and remains compatible with existing non-category
admission operations.

---

## Phase 4: User Story 2 - Products are decoupled, not blockers (Priority: P1)

**Goal**: Products never block category deletion, but they become observably
uncategorized and lose their non-blocking owner reference.

**Independent Test**: Delete a childless category with assigned Products and verify
the delete is accepted, each Product preserves `spec.categoryRef`, its owner
reference is removed, and `CategoryResolved=False/CategoryDeleted` eventually holds.

- [x] T022 [P] [US2] Add failing Product owner-reference resolution and terminating-category admission-rejection tests in `gitstore-api/internal/cataloggrpc/server_test.go` and `gitstore-api/internal/admission/catalog/product_policy_test.go`
- [x] T023 [P] [US2] Add failing bounded Product decoupling, continuation, and idempotent retry tests in `gitstore-controller-manager/internal/categorytaxonomy/products_test.go`
- [x] T024 [P] [US2] Add controller integration coverage proving Products do not block category deletion and preserve Git-authored `spec.categoryRef` in `gitstore-controller-manager/tests/integration/categorytaxonomy_deletion_test.go`
- [x] T025 [US2] Write/remove Product `blockOwnerDeletion=false` category owner references during category resolution and reject Product references to terminating categories in `gitstore-api/internal/cataloggrpc/server.go` and `gitstore-api/internal/admission/catalog/product_policy.go`
- [x] T026 [US2] Add bounded non-blocking Product dependent paging, owner-reference removal, and `CategoryDeleted` status patching to `gitstore-controller-manager/internal/categorytaxonomy/products.go`
- [x] T027 [US2] Integrate Product decoupling retries and continuation enqueueing into the CategoryTaxonomy reconciler in `gitstore-controller-manager/internal/categorytaxonomy/reconciler.go` and `gitstore-controller-manager/cmd/controller/main.go`

**Checkpoint**: Product assignment does not reject deletion and all affected Products
converge to an explicit decoupled state.

---

## Phase 5: User Story 3 - GraphQL uses the existing `deleteCategory` surface (Priority: P1)

**Goal**: The established `<verb>Category` GraphQL API applies the same child-block,
Product-nonblock, and idempotent mark-delete semantics as Git admission.

**Independent Test**: Call `deleteCategory` for categories with a child, with only
Products, with neither, and twice while terminating; validate the documented result
for each case.

- [x] T028 [P] [US3] Add failing GraphQL contract tests for `deleteCategory` child rejection, Product-tolerant marking, and redundant requests in `gitstore-api/internal/graph/resolver/category.resolvers_test.go`
- [x] T029 [P] [US3] Add authorization and namespace/repository isolation tests for `deleteCategory` in `gitstore-api/internal/graph/resolver/category.resolvers_test.go`
- [x] T030 [US3] Implement `deleteCategory` using the existing CategoryTaxonomy-backed resolver/service path in `gitstore-api/internal/graph/resolver/category.resolvers.go` and `gitstore-api/internal/graph/resolver/service.go`
- [x] T031 [US3] Regenerate and verify the existing `<verb>Category` schema/model bindings without adding `*CategoryTaxonomy` mutations in `gitstore-api/shared/schemas/category.graphqls` and `gitstore-api/internal/graph/generated/generated.go`

**Checkpoint**: GraphQL and Git deletion requests share the same lifecycle transition
and precondition semantics.

---

## Phase 6: User Story 4 - Termination is visible and safe under races (Priority: P2)

**Goal**: A marked category visibly terminates, rechecks children on every reconcile,
and completes exactly once only after children drain.

**Independent Test**: Observe `Terminating=True`, add a child after marking,
verify completion is withheld, remove the child, restart one of two controller
replicas, and verify exactly one final removal.

- [x] T032 [P] [US4] Add failing reconciler tests for `Terminating=True`, repeated child checks, stale completion conflict, and no-op re-reconcile after removal in `gitstore-controller-manager/internal/categorytaxonomy/reconciler_test.go`
- [x] T033 [P] [US4] Add failing two-replica/restart integration tests for child-create races and exactly-once completion in `gitstore-controller-manager/tests/integration/categorytaxonomy_deletion_test.go`
- [x] T034 [US4] Implement terminating-condition calculation, per-reconcile child recheck, and resource-version-guarded finalizer completion in `gitstore-controller-manager/internal/categorytaxonomy/reconciler.go`
- [x] T035 [US4] Add controller lifecycle GraphQL/status client operations and conflict/not-found handling in `gitstore-controller-manager/internal/graphqlclient/client.go` and `gitstore-controller-manager/internal/status/patch.go`

**Checkpoint**: Termination is externally visible, race safe, restart resilient, and
does not alter existing hierarchy-derived fields.

---

## Phase 7: Polish and Cross-Cutting Readiness

**Purpose**: Backfill safe legacy data, validate production behavior, and document
operations and rollout.

- [x] T036 [P] Add an idempotent owner-reference/projection backfill command and its resumability tests in `gitstore-api/cmd/backfill-owner-references/main.go` and `gitstore-api/cmd/backfill-owner-references/main_test.go`
- [x] T037 [P] Add Scylla and memdb migration/rollback compatibility tests for legacy records without owner references in `gitstore-api/internal/datastore/scylla/owner_references_test.go` and `gitstore-api/internal/datastore/memdb/owner_references_test.go`
- [x] T038 [P] Add sustained-load coverage for limit-one child lookup and paged Product decoupling at the configured high-cardinality fixture in `gitstore-controller-manager/tests/integration/categorytaxonomy_deletion_capacity_test.go`
- [x] T039 [P] Document staged rollout, rollback, alerts, dashboards, and operator remediation in `docs/runbooks/categorytaxonomy-deletion.md`
- [x] T040 Validate every quickstart scenario and update results/commands in `specs/052-categorytaxonomy-deletion-semantics/quickstart.md`
- [x] T041 Run `make pr-ready` and record any required remediation in `specs/052-categorytaxonomy-deletion-semantics/tasks.md` (passed 2026-08-20 after adding bounded page caps, lifecycle observability, backfill, and deletion coverage)

---

## Dependencies & Execution Order

```text
Phase 1 Setup
  └── Phase 2 Foundation
        ├── US1: Git child-block deletion (MVP)
        ├── US2: Product decoupling
        ├── US3: Existing deleteCategory GraphQL surface
        └── US4: Visible/race-safe termination
              └── Phase 7: Backfill, capacity, operations, PR readiness
```

- **US1** depends only on the foundation and is the MVP.
- **US2** depends on lifecycle metadata from the foundation and can proceed after
  US1's CategoryTaxonomy admission changes stabilize.
- **US3** depends on the lifecycle service from US1 and is otherwise independent of
  Product drain implementation.
- **US4** depends on US1's mark-delete transition and uses US2's Product work without
  treating Product drain as a completion gate.
- Phase 7 depends on all delivered stories.

## Parallel Opportunities

- T001–T004 can run in parallel.
- T005–T008 can run in parallel; T011 and T012 can proceed in parallel after T010.
- Within US1, T015–T017 are parallel test work; within US2, T022–T024 are parallel;
  within US3, T028 and T029 are parallel; within US4, T032 and T033 are parallel.
- T036–T039 are independent after the feature behavior is complete.

## Implementation Strategy

1. Complete Setup and Foundation, including additive schema/projection support.
2. Deliver US1 and prove Git child-block rejection before enabling any async drain.
3. Add US2 Product decoupling, then expose the already-established `deleteCategory`
   surface for US3.
4. Add US4's two-replica race/restart guarantees.
5. Backfill legacy records, run capacity/rollout validation, and execute
   `make pr-ready`.
