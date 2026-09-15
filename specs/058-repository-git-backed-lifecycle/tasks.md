# Tasks: Repository Git-Backed Lifecycle, Admission, and Reconciler

**Input**: Design documents in `specs/058-repository-git-backed-lifecycle/`
**Tests**: Required by Constitution Principle I; write and observe failure before implementation.

## Phase 1: Setup

- [X] T001 Validate the Repository lifecycle contract and defaulted shared envelope in `specs/058-repository-git-backed-lifecycle/contracts/`.
- [ ] T002 [P] Add shared Repository lifecycle test fixtures in `gitstore-api/internal/testutil/`.
- [ ] T003 [P] Add controller Repository test fixtures in `gitstore-controller-manager/internal/repository/`.

## Phase 2: Foundational Admission and Schema

- [X] T004 [P] Add Rust pre-receive rejection tests for Repository authoring targets in `gitstore-git-service/src/git/hooks/validation_handler.rs`.
- [X] T005 Implement Repository authoring-target validation in `gitstore-git-service/src/git/hooks/validation_handler.rs`.
- [X] T006 [P] Add failing Repository admission tests for create, mutable update, immutable change, storage downgrade, bootstrap, terminating namespace, authored status/owner-reference rejection, and immediate canonical Namespace owner-reference creation in `gitstore-api/internal/cataloggrpc/server_test.go`.
- [X] T007 Implement `Repository` admission dispatch, Namespace owner-reference resolution/persistence, and contract-preserving persistence in `gitstore-api/internal/cataloggrpc/server.go`.
- [X] T008 [P] Add GraphQL contract tests for `MetadataInput`, defaulted `apiVersion`/`kind`, explicit mismatch rejection, and the invariant that every Repository result has `metadata.uid == id` (the canonical encoded Relay Repository ID, never the raw datastore UID) in `gitstore-api/internal/graph/resolver/repository_lifecycle_test.go`.
- [X] T009 Add shared `MetadataInput`, defaulted Repository envelope fields, `updateRepository`, and rename/transfer deprecations in `shared/schemas/repository.graphqls`.
- [X] T010 Regenerate gqlgen contracts and update generated models in `gitstore-api/internal/graph/generated/` and `gitstore-api/internal/graph/model/`.

## Phase 3: User Story 1 — Git-backed repository admission (P1)

**Independent Test**: A manifest pushed to its namespace system repository creates or updates only that repository.

- [X] T011 [US1] Add push-to-admission contract coverage, including direct lookup, connection, and `node(id:)` identity regression cases asserting `metadata.uid == id`, in `tests/integration/repository_lifecycle_test.go`.
- [X] T012 [US1] Implement Repository manifest parsing and immutable/mutable validation in `gitstore-api/internal/cataloggrpc/server.go`.
- [X] T013 [US1] Add admission status and version-transition coverage in `gitstore-api/internal/graph/resolver/repository_lifecycle_test.go`.

## Phase 4: User Story 2 — Delegating mutations (P1)

**Independent Test**: Create and update mutations commit the defaulted envelope then return only after admission.

- [X] T014 [US2] Add failing create/update delegation, defaulting, mismatch, and bootstrap rejection tests in `gitstore-api/internal/graph/resolver/repository_lifecycle_test.go`.
- [X] T015 [US2] Implement manifest serialization and Git commit-and-await admission in `gitstore-api/internal/graph/resolver/service.go`.
- [X] T016 [US2] Implement the `updateRepository` resolver in `gitstore-api/internal/graph/resolver/repository.resolvers.go`.

## Phase 5: User Story 3 — Finalizer-protected deletion (P1)

- [X] T017 [US3] Add deletion blocker, idempotency, terminating, and storage-confirmation tests in `gitstore-api/internal/graph/resolver/repository_lifecycle_test.go`.
- [X] T018 [US3] Implement Repository deletion marker and finalizer behavior in `gitstore-api/internal/graph/resolver/service.go`.
- [X] T019 [US3] Add Repository reconciler lifecycle tests in `gitstore-controller-manager/internal/repository/reconciler_test.go`.
- [X] T020 [US3] Implement provisioning, status patching, retry, and finalizer completion in `gitstore-controller-manager/internal/repository/reconciler.go`; permit background GC only after finalizer clearance and never cascade catalog resources.
- [X] T021 [US3] Register Repository reconciliation in `gitstore-controller-manager/cmd/controller/main.go`.

## Phase 6: User Story 4 — Deferred rename and transfer (P2)

- [X] T022 [US4] Add resolver and introspection regressions for deprecated `Unimplemented` rename/transfer behavior in `gitstore-api/internal/graph/resolver/repository_lifecycle_test.go`.
- [X] T023 [US4] Replace direct rename/transfer writes with `Unimplemented` responses in `gitstore-api/internal/graph/resolver/service.go`.

## Phase 7: User Story 5 — Status ownership (P2)

- [X] T024 [US5] Add system-owned status/owner-reference tests, including authored-status/owner-reference rejection and the canonical blocking Namespace owner-reference projection, in `gitstore-api/internal/cataloggrpc/server_test.go`.
- [X] T025 [US5] Implement Repository condition projection in `gitstore-api/internal/graph/resolver/converters.go`.
- [X] T026 [US5] Add `updateRepositoryStatus` schema and failing GraphQL-error/authorization tests in `shared/schemas/repository.graphqls` and `gitstore-api/internal/graph/resolver/repository_lifecycle_test.go`.
- [X] T027 [US5] Implement partial Repository status writes and `RESOURCE_VERSION_CONFLICT` extensions in `gitstore-api/internal/graph/resolver/repository.resolvers.go`.

## Phase 8: Durable Repository watch and controller bootstrap (P1)

**Goal**: Reuse spec 050's replica-safe journal architecture so the Repository reconciler receives a complete, resumable lifecycle stream rather than process-local events or polling.

- [X] T028 [P] Add Repository journal cursor, ordering, retention, replay, lease/fencing, append-before-progress, and idle-bookmark contract tests under `gitstore-api/internal/watchjournal/` and `gitstore-api/tests/contract/datastore/`.
- [X] T029 [P] Add Repository CDC migration and authoritative-write classification tests proving committed create/update/status/finalizer/delete events and no events for rejected/conflicting/no-op writes in `gitstore-api/internal/datastore/scylla/`.
- [X] T030 Add the Repository CDC/journal migration, memdb journal capability, Scylla journal/progress/lease backend, materializer wiring, readiness, bounded metrics, and migration-first fleet-wide watch deny/enablement in `gitstore-api/internal/datastore/`, `gitstore-api/internal/watchjournal/`, `gitstore-api/internal/app/server.go`, and `gitstore-api/internal/config/`.
- [X] T031 [P] Add typed/generic Repository watch schema, payload, authorization-before-cursor-disclosure, bootstrap BOOKMARK, selector, expiry, and terminal-WebSocket-error tests in `gitstore-api/internal/graph/resolver/` and `gitstore-api/internal/middleware/security/`.
- [X] T032 Add `watchRepositories`, `RepositoryWatchEvent`, gqlgen generation, durable generic `watchResources(kind: "Repository")` routing, `repository.watch` authorization, and journal-to-GraphQL conversion in `shared/schemas/repository.graphqls`, `gitstore-api/internal/graph/`, and `gitstore-api/internal/middleware/security/`.
- [X] T033 [P] Add `RepositoryListWatcher` bootstrap/list/drain, resume, `WATCH_EXPIRED` relist, and idempotent runner/cache tests in `gitstore-controller-manager/internal/listwatch/repository_listwatcher_test.go` and `gitstore-controller-manager/internal/repository/`.
- [X] T034 Register the Repository `ListWatcher`/`Runner`/checkpoint/cache and reconciler in `gitstore-controller-manager/cmd/controller/main.go` and `gitstore-controller-manager/internal/listwatch/repository_listwatcher.go`.

## Phase 9: Cross-cutting validation

- [ ] T035 [P] Add two-replica admission/reconciler/watch correctness coverage in `tests/integration/repository_lifecycle_test.go`.
- [X] T036 [P] Add authorization and namespace-isolation coverage in `gitstore-api/internal/graph/resolver/repository_authorization_test.go`.
- [ ] T037 [P] Add bounded-load, replay, overflow, rolling-replacement, and recovery validation to `tests/integration/repository_lifecycle_test.go`.
- [X] T038 Update Repository lifecycle/watch documentation in `docs/repository/repository-spec.md`, `docs/repository/repository-watch.md`, and `docs/ADRs/0003-repository-lifecycle.md`.
- [X] T039 Validate `specs/058-repository-git-backed-lifecycle/quickstart.md`, run focused Repository watch tests, `make build`, `make test`, and `make pr-ready`.
- [X] T040 Replace `StatusConflict` payload outcomes with stable GraphQL conflict errors for CategoryTaxonomy, Namespace, File, and Product status writes in `shared/schemas/` and `gitstore-api/internal/graph/resolver/`.
- [X] T041 Add retry and null-data compatibility tests for status-write GraphQL errors in `gitstore-api/internal/graph/resolver/` and `tests/integration/`.

## Follow-up: controller provisioning reuse

- [X] T042 Replace `UpdateRepositoryStatusInput.resolved: JSON` with a typed Repository resolved-status input, persist the partial field in Repository status, regenerate gqlgen, and add merge/GraphQL tests.
- [X] T043 Reuse the Namespace controller's existing GraphQL repository-provisioning client for Repository reconciliation, activate `registerRepository` in `gitstore-controller-manager/cmd/controller/main.go`, and add idempotency, authorization, retry, and two-replica registration coverage.
- [X] T044 Replace the Namespace controller's legacy `createRepository` bootstrap call with a dedicated, controller-only Namespace bootstrap-repository ensure path. It must preserve the bootstrap exception and never route `gitstore-system` through normal Repository admission or reconciliation.

## Dependencies

T004–T010 block user-story implementation. US1 and US2 then proceed together; US3 depends on admission and schema work; US4 and US5 may proceed in parallel after T010. The Repository watch foundation begins after Repository authoritative writes exist; controller registration depends on it. Cross-cutting validation follows all stories.
