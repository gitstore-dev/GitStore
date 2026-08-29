---

description: "Implementation tasks for Namespace Validation and Admission Matrix"
---

# Tasks: Namespace Validation and Admission Matrix

**Input**: Design documents from `/specs/047-namespace-admission-matrix/`
**Prerequisites**: `plan.md`, `spec.md`, `research.md`, `data-model.md`, `contracts/`, `quickstart.md`

**Tests**: Required by the specification and Constitution Principle I. Write each
test task first and verify it fails for the expected reason before implementing
the corresponding behavior.

**Organization**: Tasks are grouped by user story so structural/policy
classification, deletion outcomes, and status inspection remain independently
testable.

## Format: `[ID] [P?] [Story] Description`

- **[P]**: Can run in parallel because it changes different files and has no
  dependency on another incomplete task.
- **[Story]**: Maps the task to US1, US2, or US3 from `spec.md`.
- Every task names the exact file it changes or validates.

## Phase 1: Setup (Shared Test Scaffolding)

**Purpose**: Establish reusable fixtures without changing runtime behavior.

- [X] T001 Create reusable Namespace validation request builders and policy-call spies in `gitstore-api/internal/cataloggrpc/namespace_validation_test.go`
- [X] T002 [P] Create reusable GraphQL error-extension assertions in `gitstore-api/internal/graph/resolver/namespace_error_test.go`

---

## Phase 2: Foundational (Blocking Prerequisites)

**Purpose**: Define the shared machine-readable decision vocabulary used by
create, update, and delete paths.

**⚠️ CRITICAL**: Complete this phase before implementing any user story.

- [X] T003 [P] Add failing tests for structural-phase reasons (including immutable name), policy reasons, conflict codes, and deletion-blocker constants in `gitstore-api/internal/namespace/decision_test.go`
- [X] T004 [P] Add failing tests for GraphQL error extensions (`code`, `phase`, `reason`, `reasons`) in `gitstore-api/internal/graph/resolver/namespace_error_test.go`
- [X] T005 Implement the structural and policy Namespace phases, stable reason constants, deletion outcomes, and deterministic blocker ordering in `gitstore-api/internal/namespace/decision.go`
- [X] T006 Implement Namespace `gqlerror.Error` constructors that expose stable extensions without leaking internal identifiers in `gitstore-api/internal/graph/resolver/namespace_error.go`
- [X] T007 [P] Add failing bounded-label tests for Namespace rejection counters, deletion outcomes, and validation latency histograms in `gitstore-api/internal/namespace/metrics_test.go`
- [X] T008 Implement bounded Namespace validation/deletion counters and latency histograms keyed only by phase and stable reason/outcome in `gitstore-api/internal/namespace/metrics.go`

**Checkpoint**: Shared reason, error, and metric contracts are ready for all
stories.

---

## Phase 3: User Story 1 - Distinguishable Validation and Policy Failures (Priority: P1) 🎯 MVP

**Goal**: Evaluate structural checks (including immutable-name validation)
before policy checks and return machine-readable reasons without allowing stale
or terminating Namespace updates.

**Independent Test**: Submit one structurally invalid request and prove no
policy lookup occurs; submit an immutable-name change and receive the immutable
reason; submit valid tier-demotion and terminating-target updates and receive
distinct policy reasons.

### Tests for User Story 1

- [X] T009 [US1] Add failing request-wide tests proving structural failures suppress policy evaluation and non-Namespace resource kinds never invoke Namespace policy in `gitstore-api/internal/cataloggrpc/namespace_validation_test.go`
- [X] T010 [US1] Add failing pre-receive tests for same-path `metadata.name` changes, path-and-name changes treated as new declarations, and stable `ValidationError.constraint` values in `gitstore-api/internal/cataloggrpc/namespace_validation_test.go`
- [X] T011 [P] [US1] Add failing admission tests for duplicate-name creation, tier demotion, terminating-target rejection, and durable recheck after a concurrent version change in `gitstore-api/internal/namespace/admission_test.go`
- [X] T012 [P] [US1] Add failing GraphQL create/update tests for structural, immutable, policy, not-found, and conflict extension codes in `gitstore-api/internal/graph/resolver/namespace_error_test.go`

### Implementation for User Story 1

- [X] T013 [US1] Refactor `ValidateResources` into request-wide structural/pre-receive and stateful Namespace policy phases in `gitstore-api/internal/cataloggrpc/server.go`
- [X] T014 [US1] Detect same-path Namespace `metadata.name` mutation from old/proposed blobs, emit the `immutable` structural constraint, and avoid inferring path-and-name changes as renames in `gitstore-api/internal/cataloggrpc/server.go`
- [X] T015 [US1] Add keyed Namespace policy validation for bootstrap targets, tier demotion, and terminating updates while preserving spec 046 resource-version retries in `gitstore-api/internal/namespace/admission.go`
- [X] T016 [US1] Route GraphQL create/update validation and admission failures through the shared Namespace error constructors in `gitstore-api/internal/graph/resolver/service.go`
- [X] T017 [US1] Emit structured operation, phase, reason, Namespace name, and conflict fields and record bounded rejection/latency metrics in `gitstore-api/internal/cataloggrpc/server.go` and `gitstore-api/internal/graph/resolver/service.go`
- [X] T018 [US1] Run `go test ./internal/cataloggrpc ./internal/namespace ./internal/graph/resolver` from `gitstore-api/` and record the independently passing US1 scenarios in `specs/047-namespace-admission-matrix/quickstart.md`

**Checkpoint**: US1 is independently complete when malformed requests never
run policy checks and every immutable/policy rejection has a stable distinct
reason.

---

## Phase 4: User Story 2 - Complete Namespace Deletion Outcomes (Priority: P1)

**Goal**: Return every applicable deletion blocker and distinguish an
already-terminating no-op from a newly started termination.

**Independent Test**: Exercise non-empty-only, bootstrap-only, combined
bootstrap/non-empty, already-terminating, and eligible-empty Namespace deletes;
verify blocker arrays, payload outcomes, resource versions, and lifecycle state.

### Tests for User Story 2

- [X] T019 [P] [US2] Add failing schema contract tests for `NamespaceDeletionOutcome` and required `DeleteNamespacePayload.outcome` in `gitstore-api/internal/graph/resolver/namespace_manifest_contract_test.go`
- [X] T020 [P] [US2] Add failing service tests for bootstrap-only, non-empty-only, combined blockers, first delete, repeated delete, and recreated-identifier protection in `gitstore-api/internal/graph/resolver/namespace_deletion_matrix_test.go`
- [X] T021 [P] [US2] Add failing resolver tests for `TERMINATION_STARTED`, `ALREADY_TERMINATING`, and deterministic blocker extensions in `gitstore-api/internal/graph/resolver/namespace_deletion_resolver_test.go`
- [X] T022 [P] [US2] Add failing authorization tests proving denied callers cannot observe Namespace blocker or lifecycle details in `gitstore-api/internal/middleware/security/graphql_test.go`

### Implementation for User Story 2

- [X] T023 [US2] Add `NamespaceDeletionOutcome` and `DeleteNamespacePayload.outcome` to `shared/schemas/namespace.graphqls`, then run `go generate ./...` from `gitstore-api/` to update `gitstore-api/internal/graph/model/models_gen.go` and `gitstore-api/internal/graph/generated/namespace.generated.go`
- [X] T024 [US2] Change Namespace deletion service results to carry `TERMINATION_STARTED` or `ALREADY_TERMINATING` and aggregate bootstrap/non-empty blockers in `gitstore-api/internal/graph/resolver/service.go`
- [X] T025 [US2] Return the successful deletion outcome and stable blocker extensions from `gitstore-api/internal/graph/resolver/namespace.resolvers.go`
- [X] T026 [US2] Record deletion outcome/blocker metrics and structured logs while preserving expected-resource-version writes and idempotent no-write repeats in `gitstore-api/internal/graph/resolver/service.go`
- [X] T027 [US2] Run `go test ./internal/graph/resolver ./internal/middleware/security` from `gitstore-api/` and record the independently passing US2 matrix in `specs/047-namespace-admission-matrix/quickstart.md`

**Checkpoint**: US2 is independently complete when combined blockers are
returned together and repeated deletion is a distinguishable successful no-op.

---

## Phase 5: User Story 3 - Inspectable Admission and Termination Status (Priority: P2)

**Goal**: Preserve and verify the durable `AdmissionAccepted` condition and the
separate derived `Terminating` state.

**Independent Test**: Create and update a Namespace, read it without the
original mutation response, and verify `AdmissionAccepted=True` references the
current generation; mark it for deletion and verify `Terminating` remains
separate from admission acceptance.

### Tests for User Story 3

- [X] T028 [P] [US3] Add admission regression tests for `AdmissionAccepted=True` and `observedGeneration` after create/update in `gitstore-api/internal/namespace/admission_test.go`
- [X] T029 [P] [US3] Add GraphQL projection tests proving `AdmissionAccepted` and derived `Terminating` are simultaneously distinguishable in `gitstore-api/internal/graph/resolver/converters_test.go`
- [X] T030 [P] [US3] Add controller regression tests proving reconciliation preserves `AdmissionAccepted` while updating `SystemRepoReady` and `Ready` in `gitstore-controller-manager/internal/namespace/reconciler_test.go`

### Contract Verification and Documentation for User Story 3

- [X] T031 [US3] Add a query-level regression test that reads Namespace status without the originating mutation response in `gitstore-api/internal/graph/resolver/namespace_status_contract_test.go`
- [X] T032 [US3] Add a regression test proving rejected Namespace updates leave the last accepted status and generation unchanged in `gitstore-api/internal/namespace/admission_test.go`
- [X] T033 [US3] Document the create/update/delete phase matrix, stable reasons, and status-condition ownership in `docs/namespace/namespace-spec.md`
- [X] T034 [US3] Run `go test ./internal/namespace ./internal/graph/resolver` from `gitstore-api/` and `go test ./internal/namespace` from `gitstore-controller-manager/`, then record the independently passing US3 checks in `specs/047-namespace-admission-matrix/quickstart.md`

**Checkpoint**: US3 is independently complete when accepted configuration and
termination state can be diagnosed from a later Namespace read.

---

## Phase 6: Polish & Cross-Cutting Concerns

**Purpose**: Validate replica safety, rollout compatibility, capacity,
documentation, and repository-wide quality.

- [X] T035 [P] Add two-replica concurrent Namespace update/delete tests proving stale policy decisions conflict rather than overwrite in `gitstore-api/internal/cataloggrpc/namespace_replica_test.go`
- [X] T036 [P] Add mixed-version compatibility tests proving legacy Git-service consumers accept the unchanged validation protobuf shape in `gitstore-api/internal/cataloggrpc/namespace_rolling_upgrade_test.go`
- [X] T037 [P] Add GraphQL rollout tests proving legacy selections work on old/new schemas and `outcome` activation waits for full API-fleet convergence in `gitstore-api/internal/graph/resolver/namespace_rolling_upgrade_test.go`
- [X] T038 [P] Add end-to-end authorization/isolation coverage for Namespace create, update, and delete reason visibility in `gitstore-api/internal/middleware/security/graphql_namespace_admission_test.go`
- [X] T039 Add an opt-in two-replica 30-minute Namespace validation soak in `gitstore-api/internal/cataloggrpc/namespace_capacity_test.go` that enforces 500 files, at most 50 Namespace manifests, 10 requests/second through 20 active workers, p95 ≤100 ms, p99 ≤250 ms, internal errors <0.1%, zero incorrect decisions, continued traffic during replica replacement, under-load throughput/error recovery ≤30 seconds, CPU <80%, memory growth <10%, and goroutines within 5% of baseline; expose and document the target in the root `Makefile` and `AGENTS.md`
- [X] T040 [P] Add operational guidance for server-first GraphQL rollout/rollback, stable rejection codes, metrics, saturation thresholds, log fields, and deletion troubleshooting in `docs/runbooks/namespace-admission.md`
- [X] T041 Run the manual structural, policy, combined-blocker, idempotent-delete, and server-first rollout scenarios from `specs/047-namespace-admission-matrix/quickstart.md`
- [X] T042 Run `graphify update .` from the 047 worktree and verify the updated graph includes the Namespace validation decision and deletion outcome surfaces in `graphify-out/`
- [X] T043 Run the root `Makefile` target `make pr-ready` and resolve only failures caused by spec 047 changes

---

## Phase 7: Final Review Corrections

**Purpose**: Close descendant-admission, authored-state, validation-parity, and
repository lifecycle races found in final review.

- [X] T044 Add failing deterministic GraphQL and catalog gRPC tests for disjoint and same-resource descendant commits in `gitstore-api/internal/graph/resolver/namespace_commit_order_test.go` and `gitstore-api/internal/cataloggrpc/namespace_descendant_test.go`
- [X] T045 Converge stale Namespace admission from the current Git head without replaying stale non-Namespace resources in `gitstore-api/internal/graph/resolver/service.go` and `gitstore-api/internal/cataloggrpc/server.go`
- [X] T046 Add failing complete-authored-state/version tests and persist Namespace envelope, labels, annotations, full spec, body, and provenance in `gitstore-api/internal/namespace/admission_test.go` and `gitstore-api/internal/namespace/admission.go`
- [X] T047 Add Git malformed/reserved identifier tests and route GraphQL plus Git parsing through the shared validator in `gitstore-api/internal/namespace/validation.go`
- [X] T048 Add deterministic two-replica create/delete races and implement `CreateRepositoryInActiveNamespace` plus `MarkNamespaceDeletion` for memdb and Scylla, including migration 005 and backend contracts
- [X] T049 Document descendant convergence, authored-state versioning, shared validation, and durable repository lifecycle coordination in `specs/047-namespace-admission-matrix/`
- [X] T050 Run targeted tests, datastore hardening/contracts, race tests, affected-module `go test ./...`, `graphify update .`, and `make pr-ready`

---

## Phase 8: Final Audit Corrections

**Purpose**: Establish honest rollback/rollout contracts, close transfer and
authorship races, and preserve authored content.

- [X] T051 Prove a supported rollback artifact retains migrations 001-005 and
  boots after migration 005 while a simulated 001-004 artifact is rejected as
  database-ahead
- [X] T052 Add and wire the Namespace repository fence rollout gate with
  memdb-compatible defaults, Scylla-safe defaults, stable gate errors, and exact
  two-phase rollout/rollback documentation
- [X] T053 Fence repository transfer into the target Namespace in memdb and
  Scylla, with deterministic contract and live hardening races
- [X] T054 Preserve the existing Git Markdown body during GraphQL
  `UpdateNamespace`
- [X] T055 Make label/annotation comparisons presence-aware and prove
  empty-valued key replacement advances generation
- [X] T056 Ensure stale same-resource descendant handlers never attribute
  descendant content to a stale actor/timestamp while disjoint convergence
  retains request attribution
- [X] T057 Run targeted tests, datastore contracts/hardening, race tests,
  graph update, and `make pr-ready`

---

## Dependencies & Execution Order

### Phase Dependencies

- **Phase 1 (Setup)**: No dependencies.
- **Phase 2 (Foundational)**: Depends on Phase 1 and blocks every user story.
- **Phase 3 (US1)**: Depends on Phase 2.
- **Phase 4 (US2)**: Depends on Phase 2; can proceed in parallel with US1 except
  where both modify `resolver/service.go`.
- **Phase 5 (US3)**: Depends on Phase 2; its API tests may proceed in parallel
  with US1/US2, while final status integration follows their resolver changes.
- **Phase 6 (Polish)**: Depends on all selected user stories.
- **Phase 7 (Final Review Corrections)**: Depends on Phase 6 and completes only
  after T050 validation succeeds or records an external blocker.

### User Story Dependencies

- **US1 (P1)**: No dependency on another story; it is the suggested MVP because
  it establishes the shared validation/admission distinction.
- **US2 (P1)**: No behavioral dependency on US1 after the shared foundation;
  integration sequencing is required only for edits to
  `gitstore-api/internal/graph/resolver/service.go`.
- **US3 (P2)**: Existing spec 046 lifecycle behavior makes it independently
  testable, but final verification should run after US1 and US2 to ensure their
  changes preserve status.

### Within Each User Story

- Write and run failing tests before implementation.
- Complete contract/schema tasks before generated code or resolver changes.
- Complete service behavior before endpoint/resolver wiring.
- Preserve authorization before exposing validation or blocker details.
- Run the story's focused test command before marking its checkpoint complete.

## Parallel Opportunities

- T001 and T002 can run in parallel.
- T003, T004, and T007 can run in parallel before T005, T006, and T008.
- US1 tests T011 and T012 can run in parallel with the sequential
  `cataloggrpc` tests T009-T010.
- US2 tests T019-T022 can run in parallel because they use separate files.
- US3 tests T028-T030 can run in parallel across API admission, GraphQL
  projection, and controller packages.
- After Phase 2, US1 and US2 can be assigned to separate developers if edits to
  `resolver/service.go` are coordinated; US3 test work can proceed concurrently.
- T035-T038 and T040 can run in parallel after story implementation.

## Parallel Example: User Story 1

```text
Task T011: Add Namespace admission policy tests in gitstore-api/internal/namespace/admission_test.go
Task T012: Add GraphQL error-extension tests in gitstore-api/internal/graph/resolver/namespace_error_test.go
```

## Parallel Example: User Story 2

```text
Task T019: Add GraphQL schema contract tests in gitstore-api/internal/graph/resolver/namespace_manifest_contract_test.go
Task T020: Add deletion service matrix tests in gitstore-api/internal/graph/resolver/namespace_deletion_matrix_test.go
Task T021: Add deletion resolver outcome tests in gitstore-api/internal/graph/resolver/namespace_deletion_resolver_test.go
Task T022: Add authorization non-disclosure tests in gitstore-api/internal/middleware/security/graphql_test.go
```

## Parallel Example: User Story 3

```text
Task T028: Add admission status tests in gitstore-api/internal/namespace/admission_test.go
Task T029: Add GraphQL status projection tests in gitstore-api/internal/graph/resolver/converters_test.go
Task T030: Add controller condition-preservation tests in gitstore-controller-manager/internal/namespace/reconciler_test.go
```

## Implementation Strategy

### MVP First (User Story 1)

1. Complete Setup and Foundational phases.
2. Implement US1's ordered validation and stable error taxonomy.
3. Run the US1 focused tests and verify structural failures never execute
   policy checks.
4. Stop for review before adding deletion payload behavior.

### Incremental Delivery

1. Deliver US1 for deterministic create/update failures.
2. Deliver US2 additively for complete deletion blockers and explicit successful
   outcomes.
3. Deliver US3 status contract verification and documentation.
4. Complete replica, rollout, authorization, capacity, graph, and PR-readiness
   checks.

### Parallel Team Strategy

1. Complete the shared reason/error foundation together.
2. Assign US1 validation to one developer and US2 deletion/schema work to
   another, coordinating `resolver/service.go`.
3. Run US3 status tests in parallel because they primarily touch admission,
   converters, and controller tests.
4. Merge story slices only after each independent checkpoint passes.

## Notes

- `[P]` tasks modify different files and have no incomplete-task dependency.
- Stable reason codes are API contracts; human-readable messages may change.
- Do not add Namespace-existence checks for Repository, Product,
  CategoryTaxonomy, Collection, ProductVariant, or File requests.
- Do not persist rejected input as Namespace status.
- Keep all datastore work keyed and preserve expected-resource-version writes.
- Generated gqlgen files must be produced by `go generate ./...`, never edited
  manually.
- Commit after each task or logical group.
