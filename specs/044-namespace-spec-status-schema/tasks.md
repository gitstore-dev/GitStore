# Tasks: Namespace Resource Contract

**Input**: Design documents from `/specs/044-namespace-spec-status-schema/`
**Prerequisites**: plan.md, spec.md, research.md, data-model.md, contracts/, quickstart.md

**Tests**: Test-First Development is mandatory. Write each test task, run it, and confirm the expected failure before starting its dependent implementation task.

**Organization**: Tasks are grouped by user story so each story has an explicit goal and independent verification point.

## Format: `[ID] [P?] [Story] Description`

- **[P]**: Can run in parallel because it changes different files and does not depend on another incomplete task in the same batch
- **[Story]**: User story traceability label
- Every task names the exact file or generated-file set it changes

## Phase 1: Setup (Shared Test Infrastructure)

**Purpose**: Establish the shared red-test surfaces used by the GraphQL contract and scalar implementation.

- [X] T001 Add failing signed-64-bit `Long` scalar boundary, overflow, fractional, and invalid-type tests in `gitstore-api/internal/graph/scalar/scalars_test.go`
- [X] T002 [P] Create reusable generated-schema execution, introspection, and Namespace fixture helpers in `gitstore-api/internal/graph/resolver/namespace_contract_test.go`
- [X] T003 [P] Create the Namespace GraphQL integration-test fixture and API bootstrap helpers in `tests/integration/namespace_contract_test.go`

---

## Phase 2: Foundational (Blocking Prerequisites)

**Purpose**: Add the shared 64-bit scalar support required by Namespace push-policy fields.

**⚠️ CRITICAL**: Complete this phase before implementing any user story.

- [X] T004 Implement explicit `MarshalLong` and `UnmarshalLong` functions with range-checked GraphQL errors in `gitstore-api/internal/graph/scalar/scalars.go`
- [X] T005 Declare `scalar Long` in `shared/schemas/schema.graphqls` and bind it to Go `int64` plus the scalar helpers in `gitstore-api/gqlgen.yml`
- [X] T006 Run and make the new scalar tests pass in `gitstore-api/internal/graph/scalar/scalars_test.go` without generating Namespace code or changing datastore schemas

**Checkpoint**: The API can represent signed 64-bit byte limits and all scalar tests pass.

---

## Phase 3: User Story 1 - Read desired and observed Namespace state separately (Priority: P1) 🎯 MVP

**Goal**: Add the standard envelope, dedicated namespace-less metadata, typed desired state, and non-null default status while retaining deprecated flat GraphQL fields for existing callers.

**Independent Test**: Query, list, and create a Namespace backed by the existing flat datastore row and verify both the preferred `apiVersion`/`kind`/`metadata`/`spec`/`status` fields and the deprecated flat compatibility fields; verify status defaults to observed generation `0`, null revision, and empty conditions.

### Tests for User Story 1

> Write and run T007-T010 first; confirm they fail against the current flat-only Namespace schema and missing documentation.

- [X] T007 [P] [US1] Add GraphQL contract assertions for the declarative envelope, `NamespaceMetadata`, typed repository/push-policy defaults, deprecated flat fields, and unchanged non-null `ObjectMeta.namespace` in `gitstore-api/internal/graph/resolver/namespace_contract_test.go`
- [X] T008 [P] [US1] Add flat-row projection tests for declarative fields, deprecated compatibility fields, empty labels/annotations, null default groups, and initial status in `gitstore-api/internal/graph/resolver/converters_test.go`
- [X] T009 [P] [US1] Add query, connection-list, `createNamespace` payload, and unchanged `deleteNamespace` behavior integration tests for the additive contract in `tests/integration/namespace_contract_test.go`
- [X] T010 [P] [US1] Add a documentation-contract test that validates the create/update YAML examples and the `Ready`/`AdmissionAccepted`/`DeletionBlocked` vocabulary in `docs/namespace/namespace-spec.md` from `gitstore-api/internal/graph/resolver/namespace_manifest_contract_test.go`

### Implementation for User Story 1

- [X] T011 [US1] Add the declarative Namespace envelope, `NamespaceMetadata`, `NamespaceSpec`, typed policy-default types, `NamespaceStatus`, and deprecated legacy output fields with migration reasons in `shared/schemas/namespace.graphqls`
- [X] T012 [US1] Regenerate GraphQL models, execution code, and resolver interfaces once from `gitstore-api/generate.go` into `gitstore-api/internal/graph/model/models_gen.go`, `gitstore-api/internal/graph/generated/namespace.generated.go`, `gitstore-api/internal/graph/generated/root_.generated.go`, and `gitstore-api/internal/graph/generated/schema.generated.go`
- [X] T013 [US1] Hydrate declarative fields, deprecated flat projections, and non-null initial status from the existing flat row in `gitstore-api/internal/graph/resolver/converters.go`
- [X] T014 [US1] Route create payloads, direct queries, Relay node queries, and connections through the additive Namespace converter in `gitstore-api/internal/graph/resolver/namespace.resolvers.go`, `gitstore-api/internal/graph/resolver/query_helpers.go`, and `gitstore-api/internal/graph/resolver/pagination.go`
- [X] T015 [US1] Publish the canonical ownership matrix, condition vocabulary, additive migration guidance, and create/update manifests in `docs/namespace/namespace-spec.md`
- [X] T016 [US1] Make the US1 contract, converter, manifest, and integration suites pass in `gitstore-api/internal/graph/resolver/namespace_contract_test.go`, `gitstore-api/internal/graph/resolver/converters_test.go`, `gitstore-api/internal/graph/resolver/namespace_manifest_contract_test.go`, and `tests/integration/namespace_contract_test.go`

**Checkpoint**: User Story 1 is independently deployable; existing callers continue using deprecated fields while new callers use the declarative contract.

---

## Phase 4: User Story 2 - Match shared identity and versioning semantics (Priority: P1)

**Goal**: Expose stable UID, initial opaque resource version `"1"`, initial generation `1`, creation timestamp, Relay ID, and a schema contract aligned with CategoryTaxonomy.

**Independent Test**: Convert and query two Namespace rows and verify independent UID/Relay identity, per-resource `resourceVersion: "1"`, `generation: 1`, creation timestamps, and GraphQL type/nullability parity with CategoryTaxonomy.

### Tests for User Story 2

> Write and run T017-T018 first; confirm the identity/default and parity assertions fail before implementing the constants.

- [X] T017 [P] [US2] Add converter tests for UID mapping, `resourceVersion: "1"`, `generation: 1`, creation timestamp, and independent values across rows in `gitstore-api/internal/graph/resolver/converters_test.go`
- [X] T018 [P] [US2] Add GraphQL introspection and node/query tests for CategoryTaxonomy type/nullability parity, Relay `id`, `metadata.uid`, and the intentional `metadata.namespace`/`status.resolved` exceptions in `gitstore-api/internal/graph/resolver/namespace_contract_test.go` and `gitstore-api/internal/graph/resolver/resolver_global_id_test.go`

### Implementation for User Story 2

- [X] T019 [US2] Implement the initial numeric resource-version/generation constants and exact metadata defaults in `gitstore-api/internal/graph/resolver/converters.go`
- [X] T020 [US2] Ensure direct queries, connections, repository-embedded Namespace values, and Relay node lookup reuse the same identity projection in `gitstore-api/internal/graph/resolver/query_helpers.go`, `gitstore-api/internal/graph/resolver/pagination.go`, and `gitstore-api/internal/graph/resolver/converters.go`
- [X] T021 [US2] Make all identity/versioning, schema-parity, and global-ID tests pass in `gitstore-api/internal/graph/resolver/converters_test.go`, `gitstore-api/internal/graph/resolver/namespace_contract_test.go`, and `gitstore-api/internal/graph/resolver/resolver_global_id_test.go`

**Checkpoint**: User Stories 1 and 2 expose a consistent declarative and concurrency-ready schema without implementing GH#172 advancement behavior.

---

## Phase 5: User Story 3 - Migrate consumers to preferred fields (Priority: P3)

**Goal**: Remove dependence on deprecated flat Namespace selections across in-repository consumers while retaining those schema fields until a future major GraphQL API release.

**Independent Test**: API/admin/integration sources select declarative Namespace fields, while schema introspection confirms every legacy output field remains present with a non-empty deprecation reason.

### Tests for User Story 3

> Write and run T022 first; confirm it identifies current deprecated-field selections before updating consumers.

- [X] T022 [US3] Add a migration regression test that scans in-repository GraphQL operations for deprecated Namespace selections and verifies schema deprecation metadata in `gitstore-api/internal/graph/resolver/namespace_consumer_migration_test.go`

### Implementation for User Story 3

- [X] T023 [P] [US3] Update repository, Relay-node, and Namespace resolver expectations to use `metadata.name`, `spec.title`, `spec.tier`, and `status` in `gitstore-api/internal/graph/resolver/repository_resolver_test.go`, `gitstore-api/internal/graph/resolver/global_id_test.go`, and `gitstore-api/internal/graph/resolver/resolver_global_id_test.go`
- [X] T024 [P] [US3] Defer admin GraphQL client regeneration until the stalled admin Phase 1 (Alpha) lands, per user direction; no admin files are changed by this feature
- [X] T025 [P] [US3] Update integration GraphQL selections that receive Namespace payloads to the declarative shape in `tests/integration/admission_operations_test.go`, `tests/integration/product_variant_test.go`, and `tests/integration/namespace_contract_test.go`
- [X] T026 [US3] Make the consumer migration regression, API resolver tests, and integration tests pass while keeping deprecated fields in `shared/schemas/namespace.graphqls`; admin validation is deferred with T024

**Checkpoint**: All in-repository consumers use preferred fields; external callers retain a deprecation window.

---

## Phase 6: Polish & Cross-Cutting Concerns

**Purpose**: Publish the broader reference, validate generation consistency, and complete repository gates.

- [X] T027 [P] Update the public Namespace query/type reference and Git-backed resource overview in `docs/api-reference.md` and `docs/resource-storage/README.md`
- [X] T028 Verify the implementation steps, additive compatibility guidance, documentation path, and example query remain executable in `specs/044-namespace-spec-status-schema/quickstart.md`
- [X] T029 Run formatting and code-generation consistency checks for `shared/schemas/namespace.graphqls`, `gitstore-api/internal/graph/scalar/scalars.go`, `gitstore-api/internal/graph/resolver/converters.go`, and generated GraphQL files
- [X] T030 Run targeted and full API/integration validation for `gitstore-api/internal/graph/scalar`, `gitstore-api/internal/graph/resolver`, and `tests/integration`
- [X] T031 Run `make pr-ready` from the repository `Makefile` and resolve only failures caused by this feature
- [X] T032 Refresh the repository knowledge graph with `graphify update .` and confirm `graphify-out/graph.json` and `graphify-out/manifest.json` include the completed Namespace contract

---

## Dependencies & Execution Order

### Phase Dependencies

- **Phase 1 (Setup)**: Starts immediately; T002 and T003 can run in parallel because they touch separate test packages.
- **Phase 2 (Foundational)**: T004-T005 depend on the failing scalar tests in T001; T006 depends on T004-T005. Phase 2 blocks every user story.
- **Phase 3 (US1)**: T007-T010 can run in parallel after Phase 2. T011 and T015 start only after their tests are observed failing; T012 depends on T011; T013 depends on T008 and T012; T014 depends on T012-T013; T016 depends on T007-T015.
- **Phase 4 (US2)**: T017-T018 can be authored after Phase 2, but T019-T021 depend on the US1 converter/envelope from T013-T016.
- **Phase 5 (US3)**: Can begin after US1 because deprecated fields keep the schema additive. T023-T025 can run in parallel after T022 captures current usages; T026 follows.
- **Phase 6 (Polish)**: Depends on all selected user stories. Validation runs after documentation and implementation are stable.

### User Story Completion Order

```text
Setup -> Foundation -> US1 (MVP) -> US2
                                  \-> US3
US2 + US3 -> Polish
```

- **US1** establishes the additive resource envelope and is independently deployable.
- **US2** depends on US1's metadata projection but is independently testable through identity/version assertions.
- **US3** depends only on US1 and may proceed independently of US2 because deprecated schema fields remain available.

### Deferred Work Boundaries

The following are deliberately absent from this task list:

- Git-delegating create/update/delete, resource-version advancement, generation advancement, and status-write behavior: GH#172
- Validation/admission matrices, policy ceilings, regex/phase validation, and mutability enforcement: GH#173
- Watch and resume semantics: GH#174
- Repository override merge and effective push-policy resolution: GH#249
- memdb or ScyllaDB Namespace migrations: not required by GH#171
- Removal of deprecated flat GraphQL fields: future major GraphQL API release

---

## Parallel Execution Examples

### User Story 1

```text
Task T007: GraphQL/deprecation/condition contract assertions
Task T008: Projection and compatibility tests
Task T009: API integration tests
Task T010: Documentation manifest validation
```

### User Story 2

```text
Task T017: Converter identity/version tests
Task T018: Schema parity and Relay tests
```

### User Story 3

After T022 identifies affected consumers:

```text
Task T023: Update Go resolver consumers/tests
Task T024: Regenerate admin GraphQL types
Task T025: Update integration GraphQL selections
```

---

## Implementation Strategy

### MVP First

1. Complete Setup and Foundational phases.
2. Complete US1 through T016.
3. Stop and run the US1 independent test.
4. Deploy/demo the additive declarative Namespace contract without breaking existing callers.

### Incremental Delivery

1. **US1**: Add the versioned envelope, typed spec, default status, documentation, and deprecated compatibility fields.
2. **US2**: Add exact initial identity/version contracts without changing storage.
3. **US3**: Migrate in-repository consumers while preserving external deprecation compatibility.
4. **Polish**: Publish broader references and run full repository gates.

### Test-First Discipline

- Run every test task before its implementation dependency and record the expected failure.
- Never hand-edit `gitstore-api/internal/graph/generated/*.generated.go`, `gitstore-api/internal/graph/model/models_gen.go`, or `gitstore-admin/src/graphql/generated.ts`.
- Do not add datastore fields, migrations, controller behavior, policy resolution, or deprecated-field removal while executing these tasks.
