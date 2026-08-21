# Tasks: File Resource Contract

**Input**: Design documents from `/specs/051-file-resource-contract/`
**Prerequisites**: `plan.md`, `spec.md`, `research.md`, `data-model.md`, `contracts/file-frontmatter.md`, `quickstart.md`

**Tests**: Required by the specification and Constitution Principle I. Add each
new test before the implementation it verifies and demonstrate the expected
failure before making it pass.

## Phase 1: Setup

- [X] T001 Confirm the 051 resource paths and test targets in `gitstore-api/internal/catalog`, `gitstore-api/internal/validate`, `gitstore-api/internal/cataloggrpc`, and `gitstore-api/internal/datastore`
- [X] T002 [P] Add the File worked-example fixture from `specs/051-file-resource-contract/contracts/file-frontmatter.md` to validator contract-test fixtures under `gitstore-api/internal/validate/testdata/file`
- [X] T003 [P] Record the File storage API version, kind, and deferred Phase 2 boundaries in `docs/resource-storage/git-backed.md`

## Phase 2: Foundational

- [X] T004 [P] Add the `File` resource envelope, spec, source, processing, variant, status, resolved, and SecretRef Go types in `gitstore-api/internal/catalog/file.go`
- [X] T005 [P] Add File-specific condition constants and kind-aware condition validation without broadening other resource kinds in `gitstore-api/internal/catalog/status.go`
- [X] T006 [P] Add the File datastore entity fields, including identity, Git provenance, spec/body, status, finalizers, and deletion timestamp, in `gitstore-api/internal/datastore/entities.go`
- [X] T007 [P] Add File rows, namespace/name indexes, and CRUD/query interfaces to `gitstore-api/internal/datastore/datastore.go`
- [X] T008 [P] Add the forward Scylla File table/index migration and model bindings in `gitstore-api/internal/datastore/scylla/migrations` and `gitstore-api/internal/datastore/scylla`
- [X] T009 [P] Add go-memdb File schema, indexes, and backend operations in `gitstore-api/internal/datastore/memdb`
- [X] T010 Add failing shared-model, datastore, and migration contract tests covering File identity, version defaults, system-field ownership, and status JSON round trips in `gitstore-api/internal/catalog/*_test.go` and `gitstore-api/internal/datastore/**/*_test.go`
- [X] T011 [P] Extend the generic resource/status/watch GraphQL contract to represent File status and resolved JSON without adding File-specific mutations in `shared/schemas/schema.graphqls`
- [X] T012 Add failing generic status/watch projection tests for File resource kind, namespace/name identity, resourceVersion guards, and resolved payload handling in `gitstore-api/internal/graph` and `gitstore-api/internal/cataloggrpc`

**Checkpoint**: Shared File types, persistence contracts, backend scaffolding, and generic status/watch seams are ready; user-story work can proceed.

## Phase 3: User Story 1 - Author a File Manifest (Priority: P1) 🎯 MVP

**Goal**: Parse and admit a self-identifying `File` document, preserve its body
as alt text, inherit namespace context, and reject wrong/missing envelope fields.

**Independent Test**: A valid `storage.gitstore.dev/v1beta1` / `File` fixture is
parsed and admitted into the datastore; missing/wrong kind, missing name, and
empty-body cases produce the specified results without affecting other resource
kinds.

### Tests for User Story 1

- [X] T013 [P] [US1] Add parser acceptance and round-trip tests for valid File frontmatter, metadata, namespace inheritance, and non-empty/empty Markdown body in `gitstore-api/internal/validate/validator_test.go`
- [X] T014 [P] [US1] Add parser rejection tests for missing kind, wrong kind, wrong API version, missing metadata.name, missing spec, author status, and read-only metadata in `gitstore-api/internal/validate/validator_test.go`
- [X] T015 [P] [US1] Add admission tests for valid File create, duplicate namespace/name identity, body persistence, and actionable per-file errors in `gitstore-api/internal/cataloggrpc/server_test.go` and `gitstore-api/internal/cataloggrpc/admission_operations_test.go`
- [X] T016 [P] [US1] Add datastore backend tests for File create/read/delete and repository-scoped owner-reference hydration in `gitstore-api/internal/datastore/memdb/backend_test.go` and `gitstore-api/internal/datastore/scylla/backend_test.go`

### Implementation for User Story 1

- [X] T017 Add `File` to `validate.ParsedResource`, exact-kind parser dispatch, and common pre-parse validation in `gitstore-api/internal/validate/validator.go`
- [X] T018 Add File body-as-alt-text and namespace inheritance to parsed-resource conversion in `gitstore-api/internal/cataloggrpc/admission_operations.go`
- [X] T019 Add File create/update/delete/path-change dispatch and identity handling to `gitstore-api/internal/cataloggrpc/server.go`
- [X] T020 Implement File persistence and read conversion in `gitstore-api/internal/datastore/memdb` and `gitstore-api/internal/datastore/scylla`
- [X] T021 Initialize system-owned File metadata/status on successful admission and ensure author status/read-only fields never overwrite it in `gitstore-api/internal/cataloggrpc`

**Checkpoint**: US1 is independently usable: valid File manifests are parsed, persisted, readable, and safely rejected when structurally invalid.

## Phase 4: User Story 2 - Declare Source, Content Type, and Processing Hints (Priority: P1)

**Goal**: Validate and persist all supported FileSpec fields, including source
types/URIs, checksum references, same-namespace credentials, named variants,
and immutable content type.

**Independent Test**: A complete FileSpec round-trips through parser, admission,
and datastore storage; each invalid source/variant/credentials/content-type
case returns a field-specific error.

### Tests for User Story 2

- [X] T022 [P] [US2] Add FileSpec round-trip tests for contentType, free-form type, source, checksum, credentialsRef, and image variants in `gitstore-api/internal/validate/validator_test.go`
- [X] T023 [P] [US2] Add validation tests for unsupported source types, empty source URI, missing checksum members, unnamed variants, and cross-namespace credentialsRef in `gitstore-api/internal/validate/validator_test.go`
- [X] T024 [P] [US2] Add admission transition tests proving contentType immutability and allowing unrelated supported spec/body updates in `gitstore-api/internal/cataloggrpc/admission_operations_test.go`
- [X] T025 [P] [US2] Add Scylla and memdb round-trip tests for FileSpec JSON and indexed namespace/name lookup in `gitstore-api/internal/datastore/scylla/backend_test.go` and `gitstore-api/internal/datastore/memdb/backend_test.go`

### Implementation for User Story 2

- [X] T026 Implement FileSpec, FileSourceDefinition, SecretRef, processing, checksum, and variant validation in `gitstore-api/internal/catalog/file.go` and `gitstore-api/internal/validate/validator.go`
- [X] T027 Add source/variant/credentials validation error paths to the existing aggregated admission response in `gitstore-api/internal/cataloggrpc/server.go`
- [X] T028 Add contentType immutability and generation/resourceVersion transition checks to File admission comparison in `gitstore-api/internal/cataloggrpc/admission_operations.go`
- [X] T029 Persist and hydrate the complete FileSpec JSON across both datastore backends in `gitstore-api/internal/datastore/memdb` and `gitstore-api/internal/datastore/scylla`

**Checkpoint**: US1 and US2 independently support the complete author-controlled File manifest contract without external source I/O.

## Phase 5: User Story 3 - Read File Status Conditions (Priority: P2)

**Goal**: Represent system-owned FileStatus, validate the fixed File condition
vocabulary, initialize admission conditions, and expose status through generic
resource status/watch contracts.

**Independent Test**: A File status with all documented fields serializes and
round-trips through the datastore and generic API; invalid condition status or
type is rejected, and newly admitted Files expose AdmissionAccepted=True and
Ready=True without a phase field.

### Tests for User Story 3

- [X] T030 [P] [US3] Add FileStatus serialization tests for observedGeneration, lastAppliedRevision, conditions, resolvedVariants, and absent phase in `gitstore-api/internal/catalog/file_test.go`
- [X] T031 [P] [US3] Add kind-aware condition tests for valid File condition types/statuses and rejection of unknown types/status values in `gitstore-api/internal/catalog/status_test.go`
- [X] T032 [P] [US3] Add admission status initialization tests for AdmissionAccepted=True, Ready=True, absent SourceResolved/ProcessingComplete, and system-only ownership in `gitstore-api/internal/cataloggrpc/server_test.go`
- [X] T033 [P] [US3] Add generic status mutation/watch tests for File resourceVersion guards, resolved JSON, duplicate/out-of-order events, and namespace isolation in `gitstore-api/internal/graph` and `gitstore-controller-manager/tests/contract`

### Implementation for User Story 3

- [X] T034 Implement FileStatus and resolved-variant JSON conversion in `gitstore-api/internal/catalog/file.go` and `gitstore-api/internal/graph/resolver`
- [X] T035 Add File condition validation and admission status initialization to `gitstore-api/internal/catalog/status.go` and `gitstore-api/internal/cataloggrpc`
- [X] T036 Wire File into generic status update and watch resource dispatch with resourceVersion-guarded persistence in `shared/schemas/schema.graphqls`, `gitstore-api/internal/graph`, and `gitstore-api/internal/cataloggrpc`
- [X] T037 Ensure File finalizers/deletionTimestamp are hydrated but no payload-processing finalizer or deletion controller is introduced in `gitstore-api/internal/datastore` and `gitstore-api/internal/cataloggrpc`

**Checkpoint**: All three user stories are independently testable; File status is durable and generic-contract compatible while source processing remains deferred.

## Phase 6: Polish & Cross-Cutting Concerns

- [X] T038 [P] Add File schema and admission documentation, including the worked example and deferred Phase 2 boundaries, in `docs/resource-storage/git-backed.md` and `docs/resource-storage/README.md`
- [ ] T039 [P] Add multi-replica/process-replacement tests for File identity, status writes, and duplicate admission behavior in `gitstore-api/internal/cataloggrpc` and `gitstore-api/internal/datastore`
  - Blocked: no multi-process or replica test harness exists in this repository; adding one would require external deployment infrastructure.
- [ ] T040 [P] Add rolling-upgrade compatibility coverage for old/new API replicas ignoring or accepting File records safely in `gitstore-api/internal/cataloggrpc`
  - Blocked: no old/new API-version or rolling-upgrade harness exists in this repository.
- [ ] T041 [P] Add authenticated namespace-isolation and SecretRef boundary tests for File admission/status/watch paths in `gitstore-api/internal/auth`, `gitstore-api/internal/cataloggrpc`, and `gitstore-api/internal/graph`
  - Blocked: existing auth integration covers repository/namespace authorization, but has no File admission/status/watch endpoint harness; SecretRef validation is covered by runnable unit tests only.
- [X] T042 Run the focused File suites and `make pr-ready`, then validate the commands in `specs/051-file-resource-contract/quickstart.md`

## Dependencies & Execution Order

### Phase Dependencies

- Setup (Phase 1) precedes Foundational (Phase 2).
- Foundational tasks block all user stories.
- US1 and US2 both depend on the foundational parser/model/datastore seams; US2
  also depends on US1's File admission comparison path.
- US3 depends on foundational status/watch contracts and US1 admission status
  initialization, but its serialization/condition tests can begin in parallel.
- Polish follows the desired user-story completion.

### User Story Dependencies

- **US1 (P1)**: Depends on Phase 2; MVP scope.
- **US2 (P1)**: Depends on Phase 2 and US1's parsed-resource/materialization path.
- **US3 (P2)**: Depends on Phase 2 and the admission persistence path from US1.

### Parallel Opportunities

- Phase 2 tasks T004-T009 and T011 can proceed in parallel; T010/T012 follow
  their respective contracts.
- US1 tests T013-T016 can proceed in parallel before T017-T021.
- US2 tests T022-T025 can proceed in parallel before T026-T029.
- US3 tests T030-T033 can proceed in parallel before T034-T037.
- Polish tasks T038-T041 can proceed in parallel before T042.

## Implementation Strategy

1. Complete Setup and Foundational phases.
2. Deliver US1 as the MVP and validate it independently.
3. Add US2's complete spec validation and immutable content-type behavior.
4. Add US3's durable status and generic status/watch exposure.
5. Complete cross-cutting replica, upgrade, auth/isolation, documentation, and
   `make pr-ready` validation.
