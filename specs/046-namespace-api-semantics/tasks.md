# Tasks: Namespace API Semantics: Spec Writes, Status Updates, Concurrency

**Input**: Design documents from `/specs/046-namespace-api-semantics/`
**Prerequisites**: plan.md (required), spec.md (required for user stories), data-model.md, contracts/, research.md, quickstart.md

**Tests**: Test-first development is required for the admission, datastore, resolver, and controller work in this feature.

**Organization**: Tasks are grouped by user story to enable independent implementation and validation.

## Format: `[ID] [P?] [Story] Description with exact file path`

## Phase 1: Setup (Shared Infrastructure)

**Purpose**: Prepare the namespace lifecycle implementation surfaces and verify the shared test harnesses.

- [x] T001 Establish the namespace feature working set and confirm the bootstrap namespace assumptions in `gitstore-api/internal/datastore/`, `gitstore-git-service/`, and `gitstore-controller-manager/`
- [x] T002 [P] Add failing namespace lifecycle test scaffolding in `gitstore-api/internal/datastore/`, `gitstore-api/internal/graph/resolver/`, and `gitstore-controller-manager/internal/namespace/`

---

## Phase 2: Foundational (Blocking Prerequisites)

**Purpose**: Land the validation, datastore, and startup plumbing before user-story implementation begins.

**Checkpoint**: The shared enforcement points are in place; US1–US4 can proceed independently.

- [x] T003 Confirm the existing generic Git-service HookContext enforcement requires no Namespace-specific Rust change
- [x] T004 Add `Namespace` datastore contract fields and the memdb/query-first Scylla update path in `gitstore-api/internal/datastore/entities.go`, `gitstore-api/internal/datastore/datastore.go`, and the alpha baseline `gitstore-api/internal/datastore/scylla/migrations/001_initial_schema.cql`
- [x] T005 [P] Implement `NormalizeNamespaceContract` / `AdvanceNamespaceSpecVersion` / `AdvanceNamespaceSystemVersion` in `gitstore-api/internal/datastore/namespace_contract.go`
- [x] T006 [P] Add `Namespace` admission dispatch and bootstrap/reject rules in `gitstore-api/internal/cataloggrpc/server.go`
- [x] T007 Add API-startup bootstrap creation for `gitstore-system` and `default` and their system repositories in `gitstore-api/cmd/server/main.go` or the startup wiring that provisions bootstrap namespaces

---

## Phase 3: User Story 1 - Every non-bootstrap namespace is created and updated as a reviewable Git change (Priority: P1) 🎯 MVP

**Goal**: Git becomes the canonical write path for namespace spec changes and the namespace record reflects the admitted manifest.

**Independent Test**: Push a valid `Namespace` manifest to `gitstore-system/gitstore-system`, confirm the namespace is created/updated and `AdmissionAccepted=True`, then verify generation/resourceVersion advance.

### Tests for User Story 1

- [x] T008 [P] [US1] Add failing namespace create/update admission tests in `tests/integration/namespace_lifecycle_test.go`
- [x] T009 [P] [US1] Confirm existing Git-service tests cover generic API-supplied HookContext enforcement; no Namespace-specific Rust test is required

### Implementation for User Story 1

- [x] T010 [P] [US1] Implement the namespace manifest path and manifest rewrite logic in `gitstore-api/internal/cataloggrpc/server.go` and `gitstore-api/internal/catalog/`
- [x] T011 [US1] Implement namespace versioning and accepted-condition writes in `gitstore-api/internal/datastore/namespace_contract.go` and the repository admission flow
- [x] T012 [US1] Update namespace conversion and status hydration in `gitstore-api/internal/graph/resolver/converters.go`
- [x] T013 [US1] Ensure `Namespace` manifests reject bootstrap names and tier demotion in the admission path and error mapping

**Checkpoint**: User Story 1 should be functional and independently verifiable by push-to-admission flow.

---

## Phase 4: User Story 2 - The createNamespace and updateNamespace mutations remain usable without requiring manual git push (Priority: P1)

**Goal**: API callers can create/update namespaces by committing equivalent manifests via the Git writer and waiting for admission.

**Independent Test**: Call `createNamespace` and `updateNamespace` with the declarative envelope, confirm a `GitWriter.CommitFile` operation is issued to `gitstore-system/gitstore-system`, and verify the returned namespace reflects the admitted state.

### Tests for User Story 2

- [x] T014 [P] [US2] Add failing mutation delegation tests in `gitstore-api/internal/graph/resolver/namespace_service_test.go`
- [x] T015 [P] [US2] Add bootstrap rejection tests for `createNamespace`/`updateNamespace` in `gitstore-api/internal/graph/resolver/namespace_service_test.go`

### Implementation for User Story 2

- [x] T016 [US2] Implement the namespace mutation delegation flow in `gitstore-api/internal/graph/resolver/service.go`
- [x] T017 [US2] Implement the declarative GraphQL mutation wrappers (`apiVersion` / `kind` / `metadata` / `spec` envelope) in `gitstore-api/internal/graph/resolver/namespace.resolvers.go`
- [x] T018 [US2] Add mutation-input envelope validation, bootstrap-name rejection, and error propagation to `gitstore-api/internal/graph/resolver/`
- [x] T019 [US2] Validate commit-and-wait semantics and bootstrap guardrails against `gitstore-api/internal/graph/resolver/service.go`

**Checkpoint**: User Story 2 should succeed without callers constructing or pushing Git manifests themselves.

---

## Phase 5: User Story 3 - Deleting a namespace is safe, ordered, and never silently loses in-progress work (Priority: P1)

**Goal**: Deletion is finalizer-based, guarded by repository existence, and safe for bootstrap namespaces and redundant delete calls.

**Independent Test**: Delete an empty namespace, ensure it enters a visible `Terminating` state before removal; reject a namespace that still owns repositories; reject bootstrap deletion attempts.

### Tests for User Story 3

- [x] T020 [P] [US3] Add failing delete lifecycle tests in `gitstore-api/internal/graph/resolver/namespace_service_test.go`
- [x] T021 [P] [US3] Add failing controller finalizer tests in `gitstore-controller-manager/internal/namespace/reconciler_test.go`

### Implementation for User Story 3

- [x] T022 [US3] Implement safe delete semantics, `HasRepositories` checks, and redundant-delete handling in `gitstore-api/internal/graph/resolver/service.go`
- [x] T023 [US3] Add finalizer/deletion-marker plumbing to the datastore layer in `gitstore-api/internal/datastore/` and `gitstore-api/internal/graph/resolver/`
- [x] T024 [US3] Implement namespace reconciler logic for finalizer drain and deletion completion in `gitstore-controller-manager/internal/namespace/reconciler.go`
- [x] T025 [US3] Register the namespace reconciler in `gitstore-controller-manager/cmd/controller/main.go`

**Checkpoint**: User Story 3 should reject unsafe deletes and complete safe deletes via a visible `Terminating` state.

---

## Phase 6: User Story 4 - System-computed status can never be set or corrupted through a spec write, and reflects real admission/reconciliation outcomes (Priority: P2)

**Goal**: Namespace status is system-owned and reflects actual observed outcomes, not author-provided status content.

**Independent Test**: Push a `Namespace` manifest containing a `status` block, confirm status is ignored; read the admitted namespace and verify `AdmissionAccepted` / `SystemRepoReady` / `Ready` are computed and persisted.

### Tests for User Story 4

- [x] T026 [P] [US4] Add failing status-ignores-author-input tests in `tests/integration/namespace_lifecycle_test.go`
- [x] T027 [P] [US4] Add failing status condition tests in `gitstore-api/internal/graph/resolver/namespace_service_test.go`

### Implementation for User Story 4

- [x] T028 [US4] Enforce system-only status creation and ignore `status` in authored Namespace manifests in the API admission path; no Namespace-specific Git-service change is required
- [x] T029 [US4] Populate `AdmissionAccepted`, `SystemRepoReady`, and `Ready` conditions in the admission/reconciler flow in `gitstore-api/internal/graph/resolver/` and `gitstore-controller-manager/internal/namespace/reconciler.go`
- [x] T030 [US4] Confirm `observedGeneration` and `lastAppliedRevision` semantics across create/update/status writes in `gitstore-api/internal/datastore/` and `gitstore-api/internal/graph/resolver/`

**Checkpoint**: User Story 4 should prove status integrity and real system-owned readouts.

---

## Phase 7: Polish & Cross-Cutting Concerns

**Purpose**: Finalize the namespace lifecycle contract and validate the feature end-to-end.

- [x] T031 [P] Update the namespace API docs and examples in `docs/namespace/namespace-spec.md` and `docs/ADRs/0002-namespace-lifecycle.md`
- [x] T032 Run targeted validation and keep the feature within the repo's required `make build` / `make test` / `make pr-ready` flow

---

## Dependencies & Execution Order

### Phase Dependencies

- **Setup (Phase 1)**: No dependencies; start immediately.
- **Foundational (Phase 2)**: Must complete before user story work starts.
- **User Stories (Phases 3–6)**: All depend on Phase 2 and can proceed in parallel once the foundation is green.
- **Polish (Phase 7)**: Must wait for all desired user stories to complete.

### User Story Dependencies

- **User Story 1 (P1)**: No story dependency; primary MVP.
- **User Story 2 (P1)**: Depends on US1's acceptance path and commit-writer support, but can be developed in parallel once the shared manifest/admission plumbing is available.
- **User Story 3 (P1)**: Depends on the namespace datastore contract and finalizer plumbing from the foundation; should be verified independently after US1/US2.
- **User Story 4 (P2)**: Depends on the admission and reconciler paths already landed by US1–US3.

### Parallel Opportunities

- `T002`, `T005`, `T006`, `T008`, `T009`, `T014`, `T015`, `T020`, `T021`, `T026`, `T027`, and `T031` are parallelizable across independent files and test surfaces.
- US1, US2, US3, and US4 can all be advanced in parallel once the foundational namespace contract and admission plumbing are in place.

---

## Parallel Example: User Story 1

```bash
# Run the namespace admission tests together after the foundational phase is green.
# tests/integration/namespace_lifecycle_test.go
# gitstore-git-service/* validation test files
# gitstore-api/internal/cataloggrpc/* tests
```

---

## Implementation Strategy

### MVP First (User Story 1 Only)

1. Complete Phase 1 and Phase 2.
2. Complete the Git-backed namespace create/update path from the Rust pre-receive and Go admission flow.
3. Validate the namespace record and status in the first end-to-end namespace push.
4. Stop and confirm the story passes before moving on to mutation delegation and finalizer-based delete flows.

### Incremental Delivery

- US1 delivers the core reviewable Git-backed namespace lifecycle.
- US2 overlays the GraphQL mutation delegation contract without changing the underlying manifest-first semantics.
- US3 adds safe deletion and finalizer drain semantics.
- US4 closes the loop on status integrity, observed generation, and system-owned conditions.
