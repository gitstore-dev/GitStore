# Tasks: Scylla Query and Recovery Hardening

**Input**: Design documents from `/specs/048-scylla-query-design/`
**Prerequisites**: `plan.md`, `spec.md`, `research.md`, `data-model.md`, `contracts/`, `quickstart.md`

**Tests**: Required by Constitution Principle I and feature requirements FR-017/FR-018. Test tasks precede implementation tasks in every story.

**Organization**: Tasks are grouped by user story. Issue #359 remains a documented deferral; no Product selector storage implementation is included.

## Format: `[ID] [P?] [Story] Description`

- **[P]**: Can run in parallel because it changes different files and does not depend on incomplete tasks.
- **[Story]**: Maps the task to its user story.
- Every task includes exact repository-relative file paths.

## Phase 1: Setup

**Purpose**: Establish focused commands and reusable test harnesses for the hardening work.

- [X] T001 Add `test-scylla-hardening`, `test-scylla-integration`, and env-gated `test-scylla-capacity` targets to `Makefile`, and document their variables in `AGENTS.md`
- [X] T002 [P] Add deterministic resource, bucket, and projection assertion helpers in `gitstore-api/tests/contract/datastore/hardening_helpers_test.go`
- [X] T003 [P] Add a mutation-step failure-injection harness covering before/after statement failures in `gitstore-api/internal/datastore/scylla/failure_injection_test.go`
- [X] T004 [P] Add production-dataset, concurrency, duration, and reporting configuration helpers in `gitstore-api/internal/datastore/scylla/capacity_test.go`

---

## Phase 2: Foundational Recovery and Observability

**Purpose**: Provide shared error, execution, metric, and multi-client primitives required by all mutation stories.

**Critical**: Complete this phase before user-story implementation.

- [X] T005 Add failing tests for original-error retention, failed-step context, and `errors.Is` behavior of repair-required failures in `gitstore-api/internal/datastore/recovery_test.go`
- [X] T006 [P] Add failing tests for bounded projection, compensation, consistency-finding, and mutation-latency metric labels in `gitstore-api/internal/datastore/instrumented_test.go`
- [X] T007 Implement `RepairRequiredError`, `MutationStep`, and `ProjectionFinding` with structured context in `gitstore-api/internal/datastore/recovery.go`
- [X] T008 Extend datastore metrics and structured observation helpers for projection writes, compensation, and findings in `gitstore-api/internal/datastore/metrics.go` and `gitstore-api/internal/datastore/instrumented.go`
- [X] T009 Implement an individually awaited Scylla statement executor with conditional execution, step naming, failure injection, and reverse-order compensation in `gitstore-api/internal/datastore/scylla/recovery.go` and wire it in `gitstore-api/internal/datastore/scylla/backend.go`
- [X] T010 Add two-independent-session Scylla datastore factories for replica-safety and concurrent-LWT tests in `gitstore-api/tests/contract/datastore/scylla_test.go` and `gitstore-api/internal/datastore/scylla/backend_test.go`

**Checkpoint**: Shared recovery failures, instrumentation, and multi-replica test seams are available.

---

## Phase 3: User Story 1 - Predictable Namespace and Repository Queries (Priority: P1) MVP

**Goal**: Normalize authoritative resource storage and provide direct, bounded Namespace and Repository reads with byte-preserved manifest bodies.

**Independent Test**: Populate full resource envelopes and Namespace/Repository records across at least three monthly buckets, then verify direct UID/name reads and forward/backward pages return complete ordered resources without scans, duplicates, omissions, or body changes.

### Tests for User Story 1

- [X] T011 [P] [US1] Add failing schema-contract tests for canonical envelope columns/types, `uid`/`repository_id`/`namespace` naming, Repository projection tables, and removed secondary indexes in `gitstore-api/internal/datastore/scylla/migration_test.go` and `gitstore-api/internal/datastore/scylla/namespace_model_test.go`
- [X] T012 [P] [US1] Add failing backend-neutral full-envelope, owner-reference, finalizer, audit, provenance, spec, body, and status round-trip tests for all six manifest-backed resources in `gitstore-api/tests/contract/datastore/contract_test.go`
- [X] T013 [P] [US1] Add failing Namespace and Repository body/version/normalization tests for memdb in `gitstore-api/internal/datastore/memdb/namespace_contract_test.go` and `gitstore-api/internal/datastore/memdb/backend_test.go`
- [X] T014 [P] [US1] Add failing direct UID, path, reverse-path, full-envelope, body, and query-shape tests for Scylla Namespace and Repository storage in `gitstore-api/internal/datastore/scylla/backend_test.go`
- [X] T015 [P] [US1] Add failing three-month forward/backward Namespace, Namespace-scoped Repository, and global Repository pagination tests, including empty buckets and `TotalCount = -1` instead of historical scans, in `gitstore-api/tests/contract/datastore/pagination_test.go`
- [X] T016 [P] [US1] Add failing GraphQL integration scenarios for Namespace/Repository body and envelope parity across direct and connection reads, plus two-user namespace isolation regression coverage, in `tests/integration/namespace_contract_test.go`, `tests/integration/repository_read_contract_test.go`, and `tests/integration/authz_repository_contract_test.go`

### Implementation for User Story 1

- [X] T017 [US1] Normalize `Namespace`, `Repository`, `NamespaceMapping`, and catalogue entities to `UID`, `Namespace`, `RepositoryID`, `OwnerReferences`, and the canonical envelope/body fields in `gitstore-api/internal/datastore/entities.go`
- [X] T018 [US1] Extend datastore contracts with global Repository listing and canonical normalization/version behavior in `gitstore-api/internal/datastore/datastore.go`, `gitstore-api/internal/datastore/namespace_contract.go`, and `gitstore-api/internal/datastore/repository_contract.go`
- [X] T019 [US1] Update memdb indexes, copies, Namespace/Repository listing, mappings, global listing, and full-envelope persistence in `gitstore-api/internal/datastore/memdb/schema.go` and `gitstore-api/internal/datastore/memdb/backend.go`
- [X] T020 [US1] Replace the alpha baseline with canonical authoritative columns, `namespaces_by_uid`, Repository UID/month/mapping projections, and no Repository secondary indexes in `gitstore-api/internal/datastore/scylla/migrations/001_initial_schema.cql` and `gitstore-api/internal/datastore/scylla/migrations/002_secondary_indexes.cql`
- [X] T021 [US1] Align Scylla row structs and table metadata with the canonical envelope and new Namespace/Repository projections in `gitstore-api/internal/datastore/scylla/models.go` and `gitstore-api/internal/datastore/scylla/backend.go`
- [X] T022 [US1] Update Namespace create/get/name lookup/list/update/delete to use `namespaces_by_uid`, preserve body/envelope fields, and hydrate monthly pages in `gitstore-api/internal/datastore/scylla/backend.go` and `gitstore-api/internal/datastore/scylla/pagination.go`
- [X] T023 [US1] Implement direct Repository UID reads, Namespace/month and global/month pagination, `TotalCount = -1` for expensive counts, authoritative hydration, and body preservation in `gitstore-api/internal/datastore/scylla/repository.go` and `gitstore-api/internal/datastore/scylla/pagination.go`
- [X] T024 [US1] Normalize path and reverse-path mappings to Namespace names and `repository_id` with direct projection lookups in `gitstore-api/internal/datastore/scylla/namespace_mapping.go` and `gitstore-api/internal/datastore/scylla/models.go`
- [X] T025 [US1] Persist Namespace body and canonical author/system metadata and apply generation changes for metadata/spec/body updates in `gitstore-api/internal/cataloggrpc/server.go`
- [X] T026 [US1] Return canonical Namespace/Repository envelopes and bodies and pass immutable Namespace names to datastore lookups in `gitstore-api/internal/graph/resolver/converters.go`, `gitstore-api/internal/graph/resolver/service.go`, `gitstore-api/internal/graph/resolver/repository.resolvers.go`, and `gitstore-api/internal/graph/resolver/pagination.go`
- [X] T027 [US1] Document authoritative versus projection ownership, hydration, count projections, and canonical naming in `docs/resource-storage/README.md` and `docs/architecture.md`
- [X] T028 [US1] Run the focused US1 contract and integration commands and record reproducible evidence in `specs/048-scylla-query-design/quickstart.md`

**Checkpoint**: User Story 1 is independently complete and is the suggested MVP.

---

## Phase 4: User Story 2 - Safe Concurrent Catalogue Writes (Priority: P1)

**Goal**: Make unique reservations deterministic across API replicas and make every catalogue projection mutation idempotent, observable, and recoverable.

**Independent Test**: Run 100 concurrent-create trials per unique key through two Scylla clients and inject failure after every create/update/delete step; verify one winner and convergence to the committed or exact prior state.

### Tests for User Story 2

- [X] T029 [P] [US2] Add failing 100-trial concurrent name, UID, SKU, and Repository-path reservation tests to the shared hardening suite in `gitstore-api/tests/contract/datastore/hardening_test.go`
- [X] T030 [P] [US2] Add failing create-step failure-injection tests for Product, ProductVariant, Collection, and CategoryTaxonomy projections in `gitstore-api/internal/datastore/scylla/backend_recovery_test.go`
- [X] T031 [P] [US2] Add failing update roll-forward, delete restoration, stale-version no-write, and compensation-failure tests in `gitstore-api/internal/datastore/scylla/backend_recovery_test.go`

### Implementation for User Story 2

- [X] T032 [US2] Implement owner-aware `INSERT ... IF NOT EXISTS` reservation and conditional-release helpers for names, UIDs, SKUs, and paths in `gitstore-api/internal/datastore/scylla/recovery.go`
- [X] T033 [US2] Replace Product read-before-write checks and logged batches with reservation-first idempotent create/update/delete steps in `gitstore-api/internal/datastore/scylla/backend.go`
- [X] T034 [US2] Replace CategoryTaxonomy read-before-write checks and logged batches with reservation-first idempotent create/update/delete steps in `gitstore-api/internal/datastore/scylla/backend.go`
- [X] T035 [US2] Replace Collection read-before-write checks and logged batches with reservation-first idempotent create/update/delete steps in `gitstore-api/internal/datastore/scylla/backend.go`
- [X] T036 [US2] Replace ProductVariant name/UID/SKU checks and logged batches with reservation-first idempotent create/update/delete steps in `gitstore-api/internal/datastore/scylla/backend.go`
- [X] T037 [US2] Implement authoritative-version-first update convergence that rolls projections forward and returns contextual repair-required failures in `gitstore-api/internal/datastore/scylla/recovery.go` and `gitstore-api/internal/datastore/scylla/backend.go`
- [X] T038 [US2] Implement projection-first deletes with expected-identity/version conditions and reconstruction from retained authoritative state in `gitstore-api/internal/datastore/scylla/recovery.go` and `gitstore-api/internal/datastore/scylla/backend.go`
- [X] T039 [US2] Detect dangling or conflicting reservations during reads/retries and emit consistency findings without silently overwriting valid owners in `gitstore-api/internal/datastore/scylla/backend.go`

**Checkpoint**: Catalogue writes are deterministic and retry-safe across concurrent API replicas.

---

## Phase 5: User Story 3 - Recoverable Repository Rename and Transfer (Priority: P1)

**Goal**: Make Repository rename and transfer converge to exactly one valid path after any partial failure.

**Independent Test**: Fail after target reservation, authoritative update, reverse mapping, and old-path cleanup; retry each operation and verify one authoritative path, one reverse mapping, no conflicting active mapping, and correct version transitions.

### Tests for User Story 3

- [X] T040 [P] [US3] Add failing Scylla rename/transfer saga tests for target conflicts, every failure point, retry convergence, and stale old mappings in `gitstore-api/internal/datastore/scylla/repository_recovery_test.go`
- [X] T041 [P] [US3] Add failing backend-neutral and memdb idempotent rename/transfer mapping tests in `gitstore-api/tests/contract/datastore/hardening_test.go` and `gitstore-api/internal/datastore/memdb/backend_test.go`
- [X] T042 [P] [US3] Extend real Git/GraphQL Repository version contracts with interrupted/repeated rename and transfer scenarios in `tests/integration/repository_version_contract_test.go`

### Implementation for User Story 3

- [X] T043 [US3] Implement owner-aware target reservation, direct reverse mapping, and conditional old-mapping deletion primitives in `gitstore-api/internal/datastore/scylla/namespace_mapping.go`
- [X] T044 [US3] Implement the rename saga in target-reserve, authoritative-update, reverse-map, old-path-cleanup order with compensation in `gitstore-api/internal/datastore/scylla/namespace_mapping.go` and `gitstore-api/internal/datastore/scylla/repository.go`
- [X] T045 [US3] Implement the transfer saga with target Namespace validation, conditional cleanup, and retry convergence in `gitstore-api/internal/datastore/scylla/namespace_mapping.go` and `gitstore-api/internal/datastore/scylla/repository.go`
- [X] T046 [US3] Preserve identical rename/transfer outcomes and version semantics in the atomic reference backend in `gitstore-api/internal/datastore/memdb/backend.go`
- [X] T047 [US3] Emit and meter stale, duplicate, and contradictory Repository mapping findings during lookup, mutation, and retry in `gitstore-api/internal/datastore/scylla/namespace_mapping.go` and `gitstore-api/internal/datastore/scylla/recovery.go`

**Checkpoint**: Repository routing remains correct and repairable after interrupted rename or transfer.

---

## Phase 6: User Story 4 - Operable Denormalized Storage (Priority: P2)

**Goal**: Expose projection health and provide tested dry-run and conditional repair procedures for partitions, tombstones, and dangling rows.

**Independent Test**: Inject dangling/missing/stale/duplicate projections and high-churn load, confirm logs and metrics identify them, run dry-run then conditional repair, and verify authoritative content remains unchanged.

### Tests for User Story 4

- [X] T048 [P] [US4] Add failing assertions for required structured log fields, bounded metric labels, compensation alerts, and finding counters in `gitstore-api/internal/datastore/instrumented_test.go`
- [X] T049 [P] [US4] Add failing dry-run, conditional repair, concurrent-writer protection, and post-repair validation tests in `gitstore-api/internal/datastore/scylla/repair_test.go` and `gitstore-api/cmd/gitctl/main_test.go`
- [X] T050 [P] [US4] Implement env-gated partition-size, bounded-page, sustained-mutation, two-client concurrency, and soak assertions in `gitstore-api/internal/datastore/scylla/capacity_test.go`

### Implementation for User Story 4

- [X] T051 [US4] Add projection/finding/compensation counters and mutation latency histograms with bounded labels in `gitstore-api/internal/datastore/metrics.go` and `gitstore-api/internal/datastore/instrumented.go`
- [X] T052 [US4] Implement authoritative-to-projection audit, dry-run plans, identity/version-conditional repair, and post-repair verification in `gitstore-api/internal/datastore/scylla/repair.go`
- [X] T053 [US4] Add `scylla-projection-audit` and confirmation-gated `scylla-projection-repair` subcommands in `gitstore-api/cmd/gitctl/main.go`
- [X] T054 [P] [US4] Write partition, tombstone, repair cadence, compaction, projection audit, dry-run, apply, and validation procedures in `docs/runbooks/scylla-projection-repair.md`
- [X] T055 [P] [US4] Document repair configuration, metrics, alert thresholds, and operator commands in `docs/configuration.md` and `docs/developer-guide.md`
- [X] T056 [US4] Record operational threshold evidence and the exact capacity/repair validation commands in `specs/048-scylla-query-design/quickstart.md`

**Checkpoint**: Operators can detect, audit, and safely repair projection inconsistencies.

---

## Phase 7: User Story 5 - Evidence-Based Product Selector Decision (Priority: P3)

**Goal**: Keep Product selector storage unchanged and document the evidence required for a future decision.

**Independent Test**: Verify the final migrations contain no Product label-selector projection or secondary index and the decision record compares all required alternatives using future measured inputs.

### Tests and Documentation for User Story 5

- [X] T057 [P] [US5] Add a migration guard test that rejects Product label-selector tables or secondary indexes introduced by this feature in `gitstore-api/internal/datastore/scylla/migration_test.go`
- [X] T058 [P] [US5] Document controller ownership, selector cardinality, update frequency, latency, and alternative-comparison evidence gates in `docs/implementation/035-collection-membership-materialization.md`

**Checkpoint**: Issue #359 remains explicitly deferred with a testable future decision gate.

---

## Phase 8: Polish and Cross-Cutting Validation

**Purpose**: Integrate CI coverage, issue traceability, production evidence, and repository-wide validation.

- [X] T059 [P] Add focused memdb, tagged Scylla, failure-injection, repair CLI, and integration jobs using the existing service pattern in `.github/workflows/ci-integration.yml`
- [X] T060 Run backend-neutral, memdb, instrumented, and targeted integration suites and record exact commands/results in `specs/048-scylla-query-design/quickstart.md`
- [ ] T061 Run the tagged Scylla hardening suite plus env-gated two-client capacity/soak validation and record dataset, duration, throughput, p95/p99, errors, and saturation in `specs/048-scylla-query-design/quickstart.md`
- [X] T062 [P] Add an issue-to-test/document traceability table for #353-#358 and #360 and explicit #359 deferral evidence in `specs/048-scylla-query-design/quickstart.md`
- [X] T063 Run `make pr-ready`, resolve feature-caused failures, and record the final readiness command in `specs/048-scylla-query-design/quickstart.md`

---

## Dependencies and Execution Order

### Phase Dependencies

- **Phase 1 Setup**: Starts immediately.
- **Phase 2 Foundational**: Depends on Phase 1 and blocks implementation stories.
- **User Story 1 (Phase 3)**: Depends on Phase 2 and establishes the canonical schema/read model.
- **User Story 2 (Phase 4)**: Depends on User Story 1 schema/model completion.
- **User Story 3 (Phase 5)**: Depends on User Story 1 Repository projections; it may proceed in parallel with User Story 2 after that point.
- **User Story 4 (Phase 6)**: Depends on User Stories 2 and 3 so repair covers their final mutation/projection behavior.
- **User Story 5 (Phase 7)**: Depends only on Phase 1 and may proceed in parallel with implementation stories.
- **Polish (Phase 8)**: Depends on all selected user stories.

### User Story Dependency Graph

```text
Setup -> Foundational -> US1 -> US2 -> US4 -> Polish
                         |      |
                         +-> US3+

Setup -------------------------> US5 -> Polish
```

### Parallel Opportunities

- T002, T003, and T004 can run in parallel after T001 starts.
- T005 and T006 can run in parallel.
- T011 through T016 can run in parallel before US1 implementation.
- T029 through T031 can run in parallel before US2 implementation.
- T040 through T042 can run in parallel before US3 implementation.
- After US1, US2 and US3 can be implemented by separate contributors.
- T048 through T050 can run in parallel before US4 implementation.
- T054 and T055 can run in parallel after operational contracts stabilize.
- T057 and T058 can run in parallel with US2-US4.
- T059 and T062 can run in parallel before final validation.

---

## Parallel Execution Examples

### User Story 1

```text
Task T011: Schema and naming contract tests in migration_test.go/namespace_model_test.go
Task T012: Backend-neutral canonical envelope round-trip tests in contract_test.go
Task T013: Memdb Namespace/Repository body and normalization tests
Task T014: Scylla direct-read and body tests
Task T015: Cross-bucket pagination tests
Task T016: GraphQL envelope/body and multi-user isolation integration tests
```

### User Story 2

```text
Task T029: Concurrent uniqueness suite
Task T030: Create failure-injection suite
Task T031: Update/delete recovery suite
```

### User Story 3

```text
Task T040: Scylla rename/transfer failure-injection tests
Task T041: Backend-neutral and memdb mapping tests
Task T042: Real Git/GraphQL rename/transfer integration tests
```

### User Story 4

```text
Task T048: Operational log/metric tests
Task T049: Audit/repair and CLI tests
Task T050: Capacity/soak tests
```

### User Story 5

```text
Task T057: Migration guard test
Task T058: Selector decision-gate documentation
```

---

## Implementation Strategy

### MVP First

1. Complete Setup and Foundational phases.
2. Complete User Story 1.
3. Validate direct reads, canonical envelope/body round trips, and bounded pagination independently.
4. Treat User Story 1 as the MVP before mutation recovery work proceeds.

### Incremental Delivery

1. **US1**: Canonical storage and predictable reads.
2. **US2**: Replica-safe catalogue writes and compensation.
3. **US3**: Recoverable Repository rename/transfer, parallelizable with US2 after US1.
4. **US4**: Metrics, capacity evidence, audit, repair, and runbook.
5. **US5**: Selector deferral guard and decision evidence.
6. **Polish**: CI, traceability, load evidence, and PR readiness.

### Completion Rules

- A test task must fail for the intended missing behavior before its implementation task begins.
- No task may reintroduce `ALLOW FILTERING`, Repository secondary-index reads, sentinel global partitions, or cross-partition logged batches.
- Multi-client tests are the replica-safety acceptance surface for datastore concurrency.
- Capacity evidence must state dataset, concurrency, duration, throughput, p95/p99, errors, and saturation.
- Every completed issue from #353-#358 and #360 must map to automated evidence or an operational verification step.
