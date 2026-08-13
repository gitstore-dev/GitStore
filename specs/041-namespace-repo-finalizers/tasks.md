---

description: "Task list for Namespace/Repository Deletion Ordering and System Repository Bootstrap"
---

# Tasks: Namespace/Repository Deletion Ordering and System Repository Bootstrap

**Input**: Design documents from `/specs/041-namespace-repo-finalizers/`
**Prerequisites**: plan.md, spec.md, research.md, data-model.md, contracts/deletion-preconditions.md, quickstart.md

**Tests**: Test-First Development (Constitution Principle I — NON-NEGOTIABLE, confirmed in plan.md's Constitution Check). Tests MUST be written before implementation and verified to fail against current stub/missing-check behavior first.

**Organization**: Tasks are grouped by user story (US1, US2, US3 per spec.md priorities P1, P1, P2) to enable independent implementation and testing of each story.

## Format: `[ID] [P?] [Story] Description`

- **[P]**: Can run in parallel (different files, no dependencies)
- **[Story]**: Which user story this task belongs to (US1, US2, US3)
- Every task includes an exact file path

## Path Conventions

Single project (`gitstore-api` module). All paths are relative to the repository root.

---

## Phase 1: Setup

**Purpose**: No new project scaffolding is needed — this is an additive change to an existing module. This phase only confirms the existing baseline the rest of the tasks build on.

- [X] T001 Confirm `make build` and `make test` (root Makefile targets) both pass on a clean checkout of `041-namespace-repo-finalizers` before any change, establishing the pre-change baseline referenced by T002-T004's "verify fails" step

**Checkpoint**: Baseline confirmed green. No foundational blocking work exists for this feature (see Phase 2 note) — proceed directly to per-story work.

---

## Phase 2: Foundational (Blocking Prerequisites)

**Purpose**: Per plan.md's Technical Context and data-model.md, this feature has no shared infrastructure that multiple user stories independently need built first — each of the two new `Datastore` interface methods (`HasRepositories`, `HasCatalogResources`) is consumed by exactly one user story (US1 and US2 respectively), and US3 (`gitstore-system` bootstrap) touches neither. There is no cross-story blocking dependency.

**⚠️ CRITICAL**: This phase is intentionally empty of tasks. Proceed directly to Phase 3 (US1).

---

## Phase 3: User Story 1 - Deleting a namespace that still has repositories is rejected (Priority: P1) 🎯 MVP

**Goal**: `deleteNamespace` rejects deletion when the namespace has ≥1 repository, using a real check instead of the `hasRepositories()` stub that always returns `false` today (`gitstore-api/internal/graph/resolver/service.go:336-339`).

**Independent Test**: Create a namespace, create a repository under it, attempt `deleteNamespace` — it must fail and both records must remain queryable. Create a second namespace with zero repositories and confirm its deletion succeeds. This is fully testable without any other user story's changes.

### Tests for User Story 1 ⚠️

> Write these tests FIRST. Run them and confirm they FAIL (the stub returns `false` unconditionally today, so a namespace-with-a-repository deletion currently incorrectly succeeds) before writing any implementation task below.

- [X] T002 [P] [US1] Add `HasRepositories` case to the backend-agnostic contract suite in `gitstore-api/tests/contract/datastore/contract_test.go`: assert `(false, nil)` for a namespace with zero repositories, `(true, nil)` after creating one repository under it, and `(false, nil)` again after that repository is deleted. This single test runs against both backends automatically via `memdb_test.go` and `scylla_test.go`'s existing `RunContractSuite` wiring — no separate per-backend test file needed.
- [X] T003 [P] [US1] Add `TestDeleteNamespace_withRepository_rejected` to `gitstore-api/internal/graph/resolver/namespace_service_test.go` (alongside the existing `TestDeleteNamespace_*` tests at lines 153-204): create a namespace, create one repository under it via `svc.CreateRepository`, call `svc.DeleteNamespace`, assert an error containing `contains repositories and cannot be deleted`, then assert `svc.GetNamespaceByIdentifier` still finds the namespace and `svc.Store().ListRepositoriesByNamespace` still lists the repository.
- [X] T004 [US1] Add `TestDeleteNamespace_afterRepositoriesRemoved_succeeds` to `gitstore-api/internal/graph/resolver/namespace_service_test.go`: create a namespace and one repository, delete the repository, then confirm `svc.DeleteNamespace` now succeeds — this guards FR-003 explicitly (must not require the "run this test after T003" implicitly; write as a standalone test creating its own namespace/repository).

### Implementation for User Story 1

- [X] T005 [US1] Add `HasRepositories(ctx context.Context, namespaceID string) (bool, error)` to the `Datastore` interface in `gitstore-api/internal/datastore/datastore.go`, grouped under the existing "Namespace operations" comment block (line ~177-182 today), per data-model.md's exact signature and doc comment.
- [X] T006 [P] [US1] Implement `HasRepositories` in `gitstore-api/internal/datastore/memdb/backend.go` as an indexed existence lookup on the existing `NamespaceID` index (reuse whichever index `ListRepositoriesByNamespace` already uses; add a minimal index in `gitstore-api/internal/datastore/memdb/schema.go` only if none exists for this field, per data-model.md's backend implementation note).
- [X] T007 [P] [US1] Implement `HasRepositories` in `gitstore-api/internal/datastore/scylla/backend.go` (or `gitstore-api/internal/datastore/scylla/repository.go` if the per-resource file split applies) as a partition-scoped query on `namespace_id` with a `LIMIT 1` equivalent — no `COUNT(*)`, no unscoped scan.
- [X] T008 [P] [US1] Add a `HasRepositories` passthrough to the metrics-wrapping decorator in `gitstore-api/internal/datastore/instrumented.go`, matching the existing instrumentation pattern already applied to every other `Datastore` method in that file.
- [X] T009 [P] [US1] Add a test-controlled `HasRepositories` implementation to the stub datastore in `gitstore-api/internal/testutil/stubstore.go`, matching the existing stub pattern for other `Datastore` methods.
- [X] T010 [US1] Replace the `hasRepositories()` stub call in `DeleteNamespace` (`gitstore-api/internal/graph/resolver/service.go:311-333`) with a real call to `s.store.HasRepositories(ctx, ns.ID)`; on `true`, return the existing `gqlerror.Errorf("namespace %q contains repositories and cannot be deleted", ns.Identifier)` (line 318's message, now actually reachable) before calling `s.store.DeleteNamespace`; delete the now-dead `hasRepositories()` function (lines 336-339) entirely.
- [X] T011 [US1] Add a structured log entry (via the existing `s.logger` zap pattern already used in `service.go`) when `DeleteNamespace` rejects due to existing repositories, per plan.md's Constitution Check (Principle IV) commitment — log at `Info` level with the namespace identifier and repository count is not required (existence check only returns bool; log the identifier and rejection reason).
- [X] T012 [US1] Run T002-T004 and confirm all now PASS. Run the full existing `namespace_service_test.go` and `gitstore-api/tests/contract/datastore/...` suites and confirm no regressions (in particular, confirm the existing `TestDeleteNamespace_owner_success`, `TestDeleteNamespace_admin_canDeleteAny`, and `TestDeleteNamespace_withoutAuthorizationCheck_serviceAllowsDelete` tests still pass unchanged — they all create namespaces with zero repositories, so they remain valid under the new check per research.md's verification).

**Checkpoint**: User Story 1 is fully functional and independently testable/deployable. This alone closes the highest-severity gap (FR-001 through FR-003).

---

## Phase 4: User Story 2 - Deleting a repository that still has catalog content is rejected (Priority: P1)

**Goal**: `deleteRepository` rejects deletion when the repository has ≥1 admitted catalog resource (Product, ProductVariant, CategoryTaxonomy, or Collection), where today it has no such check at all (`gitstore-api/internal/graph/resolver/service.go:568-604`).

**Independent Test**: Create a repository, admit a catalog resource into it, attempt `deleteRepository` — it must fail and the repository's storage/metadata and the catalog resource must all remain intact. Create a second, empty repository and confirm its deletion succeeds. Fully testable independently of User Story 1 and User Story 3.

### Tests for User Story 2 ⚠️

> Write these tests FIRST. Run them and confirm they FAIL (no check exists today, so a repository-with-catalog-resources deletion currently incorrectly succeeds and destroys storage) before writing any implementation task below.

- [X] T013 [P] [US2] Add a `HasCatalogResources` case to the backend-agnostic contract suite in `gitstore-api/tests/contract/datastore/contract_test.go`: assert `(false, nil)` for a repository with no catalog resources, then `(true, nil)` after creating one of each kind in turn (Product, ProductVariant, CategoryTaxonomy, Collection — reuse the existing `newProduct()`/`newCategoryTaxonomy()`-style fixtures already in this file, setting `RepositoryID`), and `(false, nil)` again after all are deleted. Runs against both backends automatically via the existing `RunContractSuite` wiring.
- [X] T014 [P] [US2] Add `TestDeleteRepository_withCatalogResource_rejected` to `gitstore-api/internal/graph/resolver/repository_resolver_test.go` (alongside the existing `TestDeleteRepository_callsGRPCAndRemovesMapping` test at line 120): create a namespace and repository, admit one `CategoryTaxonomy` record with that `RepositoryID` directly via the store, call `svc.DeleteRepository`, assert an error containing `contains catalog resources and cannot be deleted`, then assert the repository is still resolvable via `svcStore(t, svc).GetRepository` and the catalog record is still resolvable, AND assert the mock git writer's `deleteRepoCalls` is still empty (per contracts/deletion-preconditions.md's explicit short-circuit requirement — the precondition check must run before `s.gitWriter.DeleteRepository` is ever called, not just before the final commit step).
- [X] T015 [US2] Add `TestDeleteRepository_afterCatalogResourcesRemoved_succeeds` to `gitstore-api/internal/graph/resolver/repository_resolver_test.go`: create a repository, admit a catalog resource, delete that resource directly via the store, then confirm `svc.DeleteRepository` now succeeds — standalone test, not dependent on T014 having run first.

### Implementation for User Story 2

- [X] T016 [US2] Add `HasCatalogResources(ctx context.Context, repoID string) (bool, error)` to the `Datastore` interface in `gitstore-api/internal/datastore/datastore.go`, grouped under the existing "Repository operations" comment block, per data-model.md's exact signature, doc comment, and placement rationale (one cross-cutting query, not four per-kind methods).
- [X] T017 [P] [US2] Implement `HasCatalogResources` in `gitstore-api/internal/datastore/memdb/backend.go`: check for existence across all four catalog tables (`Product`, `ProductVariant`, `CategoryTaxonomy`, `Collection`) filtered by the existing `RepositoryID` field, short-circuiting on the first match found (do not check all four unconditionally if the first check already returns `true`).
- [X] T018 [P] [US2] Implement `HasCatalogResources` in `gitstore-api/internal/datastore/scylla/backend.go`: resolve the repository namespace, issue a namespace-partition-scoped `LIMIT 1` query per catalog table filtered by `repository_id`, and short-circuit on the first match, avoiding `COUNT(*)`, global scans, or a new schema migration.
- [X] T019 [P] [US2] Add a `HasCatalogResources` passthrough to the metrics-wrapping decorator in `gitstore-api/internal/datastore/instrumented.go`, matching the existing instrumentation pattern.
- [X] T020 [P] [US2] Add a test-controlled `HasCatalogResources` implementation to the stub datastore in `gitstore-api/internal/testutil/stubstore.go`, matching the existing stub pattern.
- [X] T021 [US2] Add the precondition check to `DeleteRepository` (`gitstore-api/internal/graph/resolver/service.go:568-604`): immediately after the existing `s.store.GetRepository` lookup and before `s.gitWriter.DeleteRepository` is called, call `s.store.HasCatalogResources(ctx, repoID)`; on `true`, return a new `gqlerror.Errorf("repository %q contains catalog resources and cannot be deleted", repo.Name)` (new message text, matching the style of the existing namespace-deletion rejection per contracts/deletion-preconditions.md) without calling `s.gitWriter.DeleteRepository`, `s.store.DeleteNamespaceMapping`, or `s.store.DeleteRepository`.
- [X] T022 [US2] Add a structured log entry (existing `s.logger` zap pattern) when `DeleteRepository` rejects due to existing catalog resources, per plan.md's Constitution Check (Principle IV).
- [X] T023 [US2] Run T013-T015 and confirm all now PASS. Run the full existing `repository_resolver_test.go` and datastore contract suites and confirm no regressions (in particular, confirm `TestDeleteRepository_callsGRPCAndRemovesMapping` still passes unchanged — it creates a repository with zero catalog resources, so it remains valid under the new check).

**Checkpoint**: User Stories 1 AND 2 both work independently. Combined, they close FR-001 through FR-006 and FR-009 through FR-011 (concurrency/atomicity requirements are satisfied by the existing `ErrNotFound` handling already present in both methods, per research.md Decision 5 — no additional task needed beyond what T010/T021 already changed).

---

## Phase 5: User Story 3 - Every new namespace starts with its system repository in place (Priority: P2)

**Goal**: `createNamespace` provisions the well-known `gitstore-system` repository as part of namespace creation, with no separate manual step and no duplicate repository on a retried creation attempt.

**Independent Test**: Create a new namespace and immediately list its repositories — the system repository must already be present. Retry creation for the same identifier and confirm no duplicate system repository results. Fully testable independently of User Story 1 and User Story 2 (touches only `CreateNamespace`, not either delete path).

### Tests for User Story 3 ⚠️

> Write these tests FIRST. Run them and confirm they FAIL (no provisioning happens today) before writing any implementation task below.

- [X] T024 [P] [US3] Add `TestCreateNamespace_provisionsSystemRepository` to `gitstore-api/internal/graph/resolver/namespace_service_test.go` (alongside the existing `TestCreateNamespace_*` tests at lines 18-102): create a namespace via `svc.CreateNamespace`, then call `svc.Store().ListRepositoriesByNamespace` for that namespace and assert exactly one repository is returned whose name matches the well-known system repository name.
- [X] T025 [P] [US3] Add `TestCreateNamespace_retriedSystemRepositoryProvisioning_noDuplicate` to `gitstore-api/internal/graph/resolver/namespace_service_test.go`: create a namespace (system repository provisioned), then directly invoke the same provisioning step a second time against the same namespace ID (simulating a retried partial-creation per research.md Decision 4 — this may require exposing the provisioning step as an internally-callable method on `Service`, not necessarily re-calling the full `CreateNamespace`, since the namespace-identifier-uniqueness check would otherwise block a literal second `CreateNamespace` call for the same identifier before provisioning is ever reached); assert no error is returned and `ListRepositoriesByNamespace` still shows exactly one system repository, not two.

### Implementation for User Story 3

- [X] T026 [US3] Add a well-known system repository name constant near the existing `reservedIdentifiers`/namespace constants in `gitstore-api/internal/graph/resolver/service.go` (or wherever the codebase's existing `gitstore-system` naming convention constant already lives — check for reuse before introducing a new one, since `gitstore-system` is already referenced as a well-known name elsewhere in the ADRs and possibly in existing code for the bootstrap namespace itself).
- [X] T027 [US3] Add a private provisioning method to `Service` (e.g. `provisionSystemRepository`) in `gitstore-api/internal/graph/resolver/service.go`: calls `s.store.CreateRepository` (or the existing `CreateRepository` service method, whichever avoids duplicating the UUID-assignment logic already in `CreateRepository`) with the well-known system repository name for the given namespace ID; on `datastore.ErrAlreadyExists`, treat as a successful idempotent no-op per research.md Decision 4 (return `nil`, not the error).
- [X] T028 [US3] Call the new provisioning method from `CreateNamespace` (`gitstore-api/internal/graph/resolver/service.go:229-272`) immediately after the existing `s.store.CreateNamespace(ctx, ns)` call succeeds, before returning `ns` to the caller.
- [X] T029 [US3] Add a structured log entry (existing `s.logger` zap pattern) for both successful system-repository provisioning and the idempotent-no-op retry case, per plan.md's Constitution Check (Principle IV).
- [X] T030 [US3] Run T024-T025 and confirm all now PASS. Run the full existing `namespace_service_test.go` suite and confirm no regressions (in particular, confirm every existing `TestCreateNamespace_*` test still passes — none of them assert on repository count today, so none should be affected by the new provisioning side effect).

**Checkpoint**: All three user stories are independently functional. FR-007 and FR-008 are closed.

---

## Phase 6: Polish & Cross-Cutting Concerns

**Purpose**: Documentation and end-to-end validation spanning all three user stories.

- [X] T031 [P] Update `shared/schemas/repository.graphqls`'s `deleteRepository` mutation docstring to state the new precondition (`"Delete a repository and its storage. Deletion is blocked when the repository contains catalog resources."`), matching the style already present on `deleteNamespace`'s docstring in `shared/schemas/namespace.graphqls`.
- [X] T032 [P] Update `docs/implementation/033-phase-1-control-plane-implementation-plan.md`'s Phase 1 exit criteria checklist: mark "Foreground finalizers enforce deletion ordering for Namespace → Repository → catalog resources" and "`gitstore-system` repository is auto-provisioned on namespace bootstrap" as complete (with a note that only the synchronous precondition-check half of the ADR-described finalizer flow was implemented, per research.md Decision 1 — not the full async `Terminating`/finalizer state machine), and update GH#165/#173 references accordingly.
- [X] T033 Remove the "Implementation status" / "Not yet implemented" note at the bottom of `specs/041-namespace-repo-finalizers/quickstart.md` once all prior tasks are complete, and manually run through quickstart.md's three verification steps against a locally running `make api` to confirm the documented behavior matches reality end-to-end.
- [X] T034 Run `make pr-ready` (per AGENTS.md's PR-readiness gate) and resolve any lint/build/test failures before this feature is considered done.

**Checkpoint**: Feature complete, documented, and verified end-to-end via quickstart.md.

---

## Dependencies & Execution Order

### Phase Dependencies

- **Setup (Phase 1)**: No dependencies — can start immediately.
- **Foundational (Phase 2)**: Empty — no blocking prerequisites exist for this feature. User stories may begin immediately after Phase 1.
- **User Stories (Phase 3-5)**: Each depends only on Phase 1. US1, US2, and US3 have **no dependencies on each other** — they touch disjoint code paths (`DeleteNamespace`+`HasRepositories` / `DeleteRepository`+`HasCatalogResources` / `CreateNamespace`+provisioning) and can proceed in parallel if staffed, or sequentially in priority order (US1 → US2 → US3, per spec.md's P1/P1/P2 ordering — US1 and US2 are equal priority per spec.md, so either order between them is acceptable; US3 is lower priority and should come last if working sequentially).
- **Polish (Phase 6)**: Depends on all three user stories being complete (T031-T032 touch documentation that describes all three; T033-T034 validate the full feature).

### Within Each User Story

- Tests (T002-T004, T013-T015, T024-T025) MUST be written and confirmed FAILING before their corresponding implementation tasks.
- New `Datastore` interface method declaration (T005/T016) before backend implementations (T006-T007/T017-T018) before decorator/stub updates (T008-T009/T019-T020) before service-layer wiring (T010/T021, T027-T028).
- Service-layer wiring before the "confirm tests now pass" verification task (T012/T023/T030).

### Parallel Opportunities

- T006, T007, T008, T009 (US1 backend implementations) can all run in parallel — different files, all depend only on T005.
- T017, T018, T019, T020 (US2 backend implementations) can all run in parallel — different files, all depend only on T016.
- T002, T003 (US1 tests) can run in parallel — different files.
- T013, T014 (US2 tests) can run in parallel — different files.
- T024, T025 (US3 tests) can run in parallel — different files (or same file, but independent test functions with no shared state).
- T031, T032 (Polish docs) can run in parallel — different files.
- Once Phase 1 completes, all of Phase 3 (US1), Phase 4 (US2), and Phase 5 (US3) can proceed in parallel by different developers, since none shares a blocking dependency.

---

## Parallel Example: User Story 1

```bash
# Launch both US1 tests together:
Task: "Add HasRepositories case to contract_test.go"
Task: "Add TestDeleteNamespace_withRepository_rejected to namespace_service_test.go"

# After T005 (interface declaration) lands, launch all four backend/decorator/stub tasks together:
Task: "Implement HasRepositories in memdb/backend.go"
Task: "Implement HasRepositories in scylla/backend.go"
Task: "Add HasRepositories passthrough in instrumented.go"
Task: "Add HasRepositories stub in stubstore.go"
```

---

## Implementation Strategy

### MVP First (User Story 1 Only)

1. Complete Phase 1: Setup (T001).
2. Skip Phase 2 (empty).
3. Complete Phase 3: User Story 1 (T002-T012).
4. **STOP and VALIDATE**: Confirm namespace deletion is now safe — the highest-severity gap identified in spec.md is closed.
5. Deploy/demo if ready — this alone is a complete, shippable safety fix.

### Incremental Delivery

1. Setup → Phase 1 done, no foundational blockers.
2. Add User Story 1 (T002-T012) → test independently → deploy/demo (MVP — namespace deletion safety).
3. Add User Story 2 (T013-T023) → test independently → deploy/demo (repository deletion safety).
4. Add User Story 3 (T024-T030) → test independently → deploy/demo (system repository bootstrap).
5. Polish (T031-T034) → docs and final end-to-end validation.
6. Each story adds value without breaking the others — verified by each story's own regression-check task (T012, T023, T030) confirming existing tests still pass.

### Parallel Team Strategy

With three developers, after Phase 1:
- Developer A: User Story 1 (T002-T012)
- Developer B: User Story 2 (T013-T023)
- Developer C: User Story 3 (T024-T030)

All three integrate independently at Phase 6 (Polish), since none of the three stories' implementation tasks touch a shared file (the only shared files across stories are the design docs touched in Phase 6, and `service.go`/`datastore.go`, where each story's edits are additive to disjoint sections — coordinate on `datastore.go`/`instrumented.go`/`stubstore.go` edits landing as separate, easily-mergeable diffs since each story adds one distinct method).

---

## Notes

- [P] tasks = different files, no dependencies on incomplete tasks within the same story.
- [Story] label maps each task to US1, US2, or US3 for traceability back to spec.md.
- Every "confirm tests now pass" task (T012, T023, T030) also explicitly re-runs the pre-existing test suite for the same file to guard against regressions — this was called out explicitly per research.md's finding that this codebase has a history of documented-but-unimplemented ADR behavior, making regression verification especially important here.
- Per research.md Decision 2: this feature does NOT fix the equivalent missing precondition checks for CategoryTaxonomy/Collection deletion (tracked as a separate, pre-existing gap — see the `project_deletion_precondition_gaps` note) or add File to the `HasCatalogResources` check (File is not yet a queryable resource per spec.md's Assumptions).
- Per research.md Decision 1: no tasks in this list add a `Status` field, `deletionTimestamp`, or `foregroundDeletion` finalizer to `Namespace` or `Repository` — that is explicitly out of scope.
- Commit after each task or logical group, per this repository's Conventional Commits convention (see AGENTS.md).
- Run `make pr-ready` before opening a PR (T034 is the final gate for this).
