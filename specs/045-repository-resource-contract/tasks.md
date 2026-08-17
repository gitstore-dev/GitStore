# Tasks: Repository Resource Contract

**Input**: Design documents from `/specs/045-repository-resource-contract/`
**Prerequisites**: plan.md, spec.md, research.md, data-model.md, contracts/repository.graphqls, quickstart.md

**Tests**: Test-First Development (Constitution Principle I - NON-NEGOTIABLE). Write each listed test first, run it, and confirm it fails for the expected missing behavior before implementing its production task.

**Organization**: The complete SDL, persistence model, transition helpers, and pure GraphQL converter are shared foundation. After that foundation, US1 read projection and US2 lifecycle/version behavior are independently completable, testable, and deployable.

## Format: `[ID] [P?] [Story] Description`

- **[P]**: Can run in parallel because it changes different files and has no dependency on another incomplete task in the same batch
- **[Story]**: Maps the task to US1 or US2 from spec.md
- Every task includes an exact repository-relative file path

## Phase 1: Setup (Shared Contract Prerequisite)

**Purpose**: Deterministically verify the shared Namespace contract types required by the Repository SDL.

- [X] T001 Verify that PR #345 head `fefadbea951959c42a982d5e0d7824dbf175209c` or a merged descendant provides `Long`, `RepositoryVisibility`, `ReceivePackHookDefaults`, `HookToggle`, `SchemaValidationDefaults`, and `AdmissionControlDefaults` in `shared/schemas/namespace.graphqls`, `shared/schemas/schema.graphqls`, `gitstore-api/internal/graph/scalar/scalars.go`, and `gitstore-api/gqlgen.yml`; if absent, stop with prerequisite guidance and do not merge or rebase another branch during task execution

---

## Phase 2: Foundational (Blocking Prerequisites)

**Purpose**: Establish the complete additive schema, persistence contract, transition helpers, and reusable Repository projection required by both user stories.

**CRITICAL**: Complete this phase before starting either user story.

### Tests for the foundation

> Write and run T002-T006 first; confirm each fails for the expected missing contract behavior before starting T007.

- [X] T002 [P] Add failing GraphQL schema contract tests for the Repository envelope, nullability, shared metadata/policy/condition type reuse, reserved visibility/extended-policy fields, every required legacy-field deprecation, and non-deprecated Relay `id` in `gitstore-api/internal/graph/resolver/repository_schema_contract_test.go`
- [X] T003 [P] Add failing unit tests for canonical legacy defaults, malformed resourceVersion normalization, spec-version increments, and system-only resourceVersion increments in `gitstore-api/internal/datastore/repository_contract_test.go`
- [X] T004 [P] Add failing memdb create/get/list/update round-trip tests for `Generation`, `ResourceVersion`, and full `Status`, including zero-value legacy rows, in `gitstore-api/internal/datastore/memdb/backend_test.go`
- [X] T005 [P] Add failing Scylla migration and repository round-trip tests for the new `generation`, `resource_version`, and `status` columns in `gitstore-api/internal/datastore/scylla/migration_test.go` and `gitstore-api/internal/datastore/scylla/backend_test.go`
- [X] T006 [P] Add failing pure-converter tests for constants, ObjectMeta hydration, reserved `PRIVATE` visibility, null extended policy groups, maximum-size projection, default status, empty condition vocabulary, resolved storage values, legacy-field equivalence, and malformed-status logging fallback in `gitstore-api/internal/graph/resolver/converters_test.go`

### Implementation for the foundation

- [X] T007 Add the complete additive Repository envelope, `RepositorySpec`, `RepositoryPushPolicy`, `RepositoryStatus`, `ResolvedRepositoryDefinition`, and all required legacy-field `@deprecated` directives in one edit to `shared/schemas/repository.graphqls`
- [X] T008 Regenerate gqlgen models and execution code once with `go generate ./...` from `gitstore-api/`, updating `gitstore-api/internal/graph/model/models_gen.go` and `gitstore-api/internal/graph/generated/repository.generated.go`
- [X] T009 Extend `datastore.Repository` and implement canonical status normalization plus spec/system version-transition helpers in `gitstore-api/internal/datastore/entities.go` and `gitstore-api/internal/datastore/repository_contract.go`
- [X] T010 [P] Persist and normalize Repository generation, resourceVersion, and full status in all memdb create/get/list/update paths in `gitstore-api/internal/datastore/memdb/backend.go`
- [X] T011 [P] Add `003_repository_resource_contract.cql` and update Scylla table metadata, row conversion, select, insert, and update mappings in `gitstore-api/internal/datastore/scylla/migrations/003_repository_resource_contract.cql`, `gitstore-api/internal/datastore/scylla/models.go`, and `gitstore-api/internal/datastore/scylla/repository.go`
- [X] T012 Implement a reusable pure Repository GraphQL converter for metadata/spec/status, reserved configuration defaults, legacy-field parity, status JSON decoding, explicit initial-status fallback logging, and resolved storage hydration in `gitstore-api/internal/graph/resolver/converters.go`
- [X] T013 Run the shared schema, converter, and datastore tests with `go test ./internal/graph/resolver ./internal/datastore ./internal/datastore/memdb ./internal/datastore/scylla` from `gitstore-api/`

**Checkpoint**: The complete contract and reusable projection exist, both backends expose canonical state, and either user story can now proceed independently.

---

## Phase 3: User Story 1 - Read Declarative State Without Breaking Flat Reads (Priority: P1) MVP

**Goal**: Return identical declarative and legacy Repository projections through single lookup, global Node lookup, and namespace-list paths without additional datastore lookups.

**Independent Test**: With only the shared foundation complete, query legacy and newly created repositories by ID and namespace/name and list repositories. Every path returns the complete envelope, correct reserved defaults, equivalent legacy fields, required deprecations, and unchanged Relay identity.

### Tests for User Story 1

> Write and run T014-T015 first; confirm both fail because read-path wiring is not implemented.

- [X] T014 [P] [US1] Add failing single-query, global-Node, and namespace-list tests that assert identical declarative envelopes, equivalent legacy flat fields, and no per-row namespace lookup in `gitstore-api/internal/graph/resolver/repository_read_contract_test.go`
- [X] T015 [P] [US1] Add failing end-to-end GraphQL read tests for legacy and newly created repositories, including introspected deprecations and non-deprecated Relay `id`, in the US1-owned file `tests/integration/repository_read_contract_test.go`

### Implementation for User Story 1

- [X] T016 [US1] Wire the shared Repository converter through single reads, global Node reads, and connection construction while passing the already-resolved namespace identifier and storage data directory without per-row lookups in `gitstore-api/internal/graph/resolver/repository.resolvers.go`, `gitstore-api/internal/graph/resolver/query_helpers.go`, and `gitstore-api/internal/graph/resolver/pagination.go`
- [X] T017 [US1] Run the US1 resolver and integration tests with `go test ./internal/graph/resolver -run 'Repository(Read|Schema)Contract'` from `gitstore-api/` and `go test ./... -run RepositoryReadContract` from `tests/integration/`

**Checkpoint**: US1 independently delivers the additive Repository read contract and flat-reader compatibility as the MVP.

---

## Phase 4: User Story 2 - Preserve Identity, Versioning, and Lifecycle Compatibility (Priority: P1)

**Goal**: Initialize and advance Repository identity/version state correctly across existing lifecycle operations while preserving mutation success/error behavior.

**Independent Test**: With only the shared foundation complete, create, rename, transfer, and delete repositories. Verify stable Relay ID/UID, canonical initial state, required counter transitions, preserved status/history, and unchanged mutation behavior.

### Tests for User Story 2

> Write and run T018-T023 first; confirm the assertions fail before changing service or mutation-path behavior.

- [X] T018 [P] [US2] Add failing create/rename/transfer tests for generation, resourceVersion, UID stability, namespace changes, and status preservation in `gitstore-api/internal/graph/resolver/repository_resolver_test.go`
- [X] T019 [P] [US2] Add failing memdb transition tests proving rename/spec helpers advance both counters while transfer/system helpers preserve generation in `gitstore-api/internal/datastore/memdb/backend_test.go`
- [X] T020 [P] [US2] Add failing Scylla transition tests proving persisted counters and full status survive update/read cycles in `gitstore-api/internal/datastore/scylla/backend_test.go`
- [X] T021 [P] [US2] Add failing end-to-end create/rename/transfer version assertions in the independent US2-owned file `tests/integration/repository_version_contract_test.go`
- [X] T022 [P] [US2] Add failing global Node identity assertions for Repository metadata UID and Relay ID stability in `gitstore-api/internal/graph/resolver/resolver_global_id_test.go`
- [X] T023 [P] [US2] Extend mutation regression tests to assert unchanged create/rename/transfer/delete payloads, authorization, validation, and error behavior in `gitstore-api/internal/graph/resolver/repository_resolver_test.go`

### Implementation for User Story 2

- [X] T024 [US2] Initialize canonical Repository contract state on create and invoke the correct spec/system version-transition helpers during rename and transfer in `gitstore-api/internal/graph/resolver/service.go`
- [X] T025 [US2] Wire mutation Repository payloads to the shared converter while preserving existing fields, inputs, authorization, validation, error messages, and rollback behavior in `gitstore-api/internal/graph/resolver/repository.resolvers.go` and `gitstore-api/internal/graph/resolver/converters.go`
- [X] T026 [US2] Run the US2 service, datastore, global-ID, mutation-regression, and integration tests with `go test ./internal/graph/resolver ./internal/datastore/memdb ./internal/datastore/scylla -run 'Repository.*(Create|Rename|Transfer|Delete|Version|Identity)'` from `gitstore-api/` and `go test ./... -run RepositoryVersionContract` from `tests/integration/`

**Checkpoint**: US2 independently delivers stable identity/version history and unchanged existing lifecycle behavior.

---

## Phase 5: Polish & Cross-Cutting Concerns

**Purpose**: Publish the contract, verify generated artifacts, and run repository-wide quality gates.

- [X] T027 [P] Document the Repository envelope, field ownership, persisted versus reserved configuration, defaults, deprecation matrix, empty initial condition vocabulary, future condition-writer requirements, version transitions, and valid/invalid examples in `docs/repository/repository-spec.md`
- [X] T028 [P] Update Repository queries, reserved-field behavior, and field documentation in `docs/api-reference.md`
- [X] T029 Run formatting, gqlgen drift checks, focused tests, and the full API/integration suites using `gofmt` on changed Go files, `go generate ./...`, and `go test ./...` from `gitstore-api/` and `tests/integration/`
- [X] T030 Run the repository PR readiness gate with `make pr-ready` from the repository root
- [X] T031 Refresh the project knowledge graph after implementation with `graphify update .` from the repository root

---

## Dependencies & Execution Order

### Phase Dependencies

- **Phase 1 (Setup)**: Verification only; requires access to PR #345 metadata but performs no merge/rebase.
- **Phase 2 (Foundational)**: Depends on T001 and blocks both user stories.
- **Phase 3 (US1)**: Depends only on Phase 2.
- **Phase 4 (US2)**: Depends only on Phase 2 and may run in parallel with US1.
- **Phase 5 (Polish)**: Depends on all selected user stories.

### User Story Dependencies

```text
Setup verification
  └── Shared contract foundation
        ├── US1 Read projection + flat-read compatibility (P1, MVP)
        └── US2 Identity/version + lifecycle compatibility (P1)
            [US1 and US2 have no dependency on each other]
                    └── Join selected completed stories
                          └── Polish and PR readiness
```

- **US1**: Independently demonstrates the complete declarative and legacy read projections after the foundation.
- **US2**: Independently demonstrates identity/version transitions and unchanged lifecycle behavior after the foundation.

### Within Each User Story

- Write all listed story tests first and confirm expected failures.
- Implement only that story's read or lifecycle wiring.
- Complete focused tests before declaring the story checkpoint complete.
- Do not use files owned by the other story for integration coverage.

### Parallel Opportunities

- T002, T003, T004, T005, and T006 can be authored in parallel.
- T010 and T011 can run in parallel after T009.
- T014 and T015 can be authored in parallel.
- T018, T019, T020, T021, T022, and T023 can be authored in parallel.
- T027 and T028 can run in parallel after story implementation.
- US1 and US2 have no logical dependency after T013 and own separate integration files. Concurrent implementers must coordinate the shared `repository.resolvers.go` file.

---

## Parallel Example: User Story 1

```text
Task T014: Add read-path contract tests in gitstore-api/internal/graph/resolver/repository_read_contract_test.go
Task T015: Add independent read integration tests in tests/integration/repository_read_contract_test.go
```

## Parallel Example: User Story 2

```text
Task T018: Add service transition tests in gitstore-api/internal/graph/resolver/repository_resolver_test.go
Task T019: Add memdb transition tests in gitstore-api/internal/datastore/memdb/backend_test.go
Task T020: Add Scylla transition tests in gitstore-api/internal/datastore/scylla/backend_test.go
Task T021: Add independent version integration tests in tests/integration/repository_version_contract_test.go
Task T022: Add global-ID tests in gitstore-api/internal/graph/resolver/resolver_global_id_test.go
Task T023: Add mutation regression tests in gitstore-api/internal/graph/resolver/repository_resolver_test.go
```

---

## Implementation Strategy

### MVP First (User Story 1)

1. Complete T001-T013.
2. Complete T014-T017.
3. Stop and validate the declarative plus legacy read projection independently.
4. Demo the additive read contract without requiring US2.

### Incremental Delivery

1. **Foundation**: Complete and generate the additive schema once; persist and normalize canonical state; provide the shared converter.
2. **US1 MVP**: Deliver query/Node/list projection and flat-reader compatibility.
3. **US2**: Independently deliver lifecycle version semantics and mutation compatibility.
4. **Polish**: Publish documentation and run full readiness checks.

### Parallel Team Strategy

1. Complete Setup and Foundational phases together.
2. After T013:
   - Developer A implements US1 read projection.
   - Developer B implements US2 lifecycle/version semantics.
3. Integrate the independent story branches.
4. Complete documentation and repository-wide validation.

---

## Notes

- `[P]` tasks operate on different files within the same test-first batch.
- Story tasks always carry `[US1]` or `[US2]`.
- Setup, foundational, and polish tasks intentionally have no story label.
- Generated gqlgen files must be produced by `go generate ./...`, not edited manually.
- The complete SDL and all deprecations are added once in T007 and generated once in T008.
- Do not add a Repository controller, watch subscription, status mutation, condition type, or Git-driven write path in this feature.
- Preserve unrelated worktree changes and do not rewrite PR #345's shared schema concepts under Repository-specific names.
