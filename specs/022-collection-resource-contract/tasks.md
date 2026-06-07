# Tasks: Collection Resource Contract with Label Selectors

**Input**: Design documents from `/specs/022-collection-resource-contract/`
**Prerequisites**: plan.md ✅, spec.md ✅, research.md ✅, data-model.md ✅, contracts/ ✅, quickstart.md ✅

**Tests**: Test-First Development (Constitution Principle I — NON-NEGOTIABLE). Tests MUST be written before implementation.

**Organization**: Tasks are grouped by user story to enable independent implementation and testing.

**User input applied**:
- ScyllaDB migration replaces `collections` table inline in `001_initial_schema.cql` (no `003` migration file).
- Status fields (`MembersResolved`, `Ready`, `observedGeneration`, `resolved`) are computed by the controller reconciliation loop (deferred to spec #244). Only `AdmissionAccepted` condition is populated at admission time.
- Black-box integration tests live in `tests/integration/` at the repository root.

## Format: `[ID] [P?] [Story] Description`

- **[P]**: Can run in parallel (different files, no dependencies)
- **[Story]**: Which user story this task belongs to (US1–US4)

---

## Phase 1: Setup (Shared Infrastructure)

**Purpose**: New catalog type scaffolding — independent of user story work.

- [ ] T001 Create `gitstore-api/internal/catalog/collection.go` with `CollectionResource`, `CollectionSpec`, `LabelSelector`, `LabelSelectorRequirement` structs and YAML + validate tags
- [ ] T002 [P] Create `gitstore-api/internal/catalog/selector.go` with `MatchesLabels(selector LabelSelector, labels map[string]string) bool` pure-Go evaluation function
- [ ] T003 [P] Create unit tests `gitstore-api/internal/catalog/selector_test.go` covering all four operators (`In`, `NotIn`, `Exists`, `DoesNotExist`), combined `matchLabels` + `matchExpressions`, and empty-selector → false

---

## Phase 2: Foundational (Blocking Prerequisites)

**Purpose**: Core infrastructure that MUST be complete before ANY user story can be implemented.

**⚠️ CRITICAL**: No user story work can begin until this phase is complete.

- [ ] T004 Replace the `collections` table DDL in `gitstore-api/internal/datastore/scylla/migrations/001_initial_schema.cql`: remove the legacy flat table and add the three Kubernetes-style tables — `collection` (namespace-partitioned), `collection_by_name`, `collection_by_uid` — following the `category_taxonomy` pattern; also remove the `collections_by_id` and `collections_by_slug` index entries from `002_add_initial_indices.cql`
- [ ] T005 Replace the legacy `Collection` struct in `gitstore-api/internal/datastore/entities.go` with the Kubernetes-style entity (all fields from data-model.md: `UID`, `Namespace`, `Name`, `APIVersion`, `Kind`, `Generation`, `ResourceVersion`, `CreationTimestamp`, `Revision`, `Labels`, `Annotations`, `GitCommitSHA`, `GitRef`, `Spec json.RawMessage`, `Body`, `Status json.RawMessage`)
- [ ] T006 Add five new `Datastore` interface methods to `gitstore-api/internal/datastore/datastore.go`: `CreateCollection`, `GetCollection`, `UpdateCollection`, `GetCollectionByName`, `ListCollections`; also add `ListProductsByLabelSelector(ctx, namespace string, selector catalog.LabelSelector) ([]*Product, error)`
- [ ] T007 Rebuild the `collection` memdb table in `gitstore-api/internal/datastore/memdb/schema.go`: replace `id`+`slug` indexes with `id` (UUID), `name_namespace` (compound unique on `Name`+`Namespace`), and `namespace` (non-unique) indexes
- [ ] T008 Implement all six new Datastore methods for the memdb backend in `gitstore-api/internal/datastore/memdb/backend.go`, following the `CategoryTaxonomy` implementation pattern
- [ ] T009 Add `CollectionRow`, `CollectionByNameRow`, `CollectionByUIDRow` structs to `gitstore-api/internal/datastore/scylla/models.go`
- [ ] T010 Implement all six new Datastore methods for the ScyllaDB backend in `gitstore-api/internal/datastore/scylla/backend.go`, following the `CategoryTaxonomy` three-table pattern
- [ ] T011 Wrap all six new Datastore methods in `gitstore-api/internal/datastore/instrumented.go` with `InstrumentedDatastore`
- [ ] T012 Extend `gitstore-api/internal/validate/validator.go`: add `Collection *catalog.CollectionResource` to `ParsedResource`; add `case "Collection":` to `ParseResource` switch with `validateCollectionSpec` (title required, operator enum, values constraints for `In`/`NotIn`, `targetRef.kind == "Product"` if present)
- [ ] T013 Write datastore contract tests for all Collection CRUD methods in `gitstore-api/tests/contract/datastore/contract_test.go`, covering `CreateCollection`, `GetCollectionByName`, `GetCollection` (by UID), `ListCollections` (pagination), `UpdateCollection`, and `ListProductsByLabelSelector` for both memdb and ScyllaDB backends

**Checkpoint**: Foundation ready — user story implementation can now begin in parallel.

---

## Phase 3: User Story 1 — Define a Collection via git push (Priority: P1) 🎯 MVP

**Goal**: A valid `Collection` document pushed to the catalog repository is admitted, persisted, and the push is rejected for invalid documents.

**Independent Test**: Push a single valid `Collection` document; verify it is stored and the pre-receive hook rejects an invalid one (missing title).

### Tests for User Story 1

> **Write these tests FIRST, ensure they FAIL before implementation.**

- [ ] T014 [P] [US1] Write integration test in `tests/integration/collection_test.go`: push a valid `Collection` document and assert the push succeeds (exit 0) and no error appears in pre-receive output
- [ ] T015 [P] [US1] Write integration test in `tests/integration/collection_test.go`: push a `Collection` document with missing `spec.title` and assert the push is rejected with a descriptive error message
- [ ] T016 [P] [US1] Write integration test in `tests/integration/collection_test.go`: push a `Collection` with `spec.selector.targetRef.kind` set to `Category` and assert rejection

### Implementation for User Story 1

- [ ] T017 [US1] Add `Collection` admission branch to `gitstore-api/internal/cataloggrpc/server.go` in `AdmitResources`: parse via `ParseResource`, upsert via `CreateCollection`/`UpdateCollection`, write `AdmissionAccepted` condition to `status.conditions` at admission time; defer `MembersResolved`/`Ready`/`resolved.memberCount` to controller reconciliation (spec #244)
- [ ] T018 [US1] Add `DatastoreCollectionToGraphQL` converter function to `gitstore-api/internal/graph/converters.go` that maps the `Collection` entity to `model.Collection`, including `spec.selector` and `spec.media` hydration from `Spec json.RawMessage`

**Checkpoint**: At this point a valid Collection push is admitted and persisted. Invalid pushes are rejected.

---

## Phase 4: User Story 2 — Query a Collection (Priority: P1)

**Goal**: A storefront developer can query a Collection by name or ID and retrieve metadata, spec, status, and an empty `products` connection placeholder.

**Independent Test**: Query `collection(by: {namespacePath: ...})` for a pushed Collection and assert `spec.title`, `spec.selector`, `status.conditions[AdmissionAccepted]` are present.

### Tests for User Story 2

> **Write these tests FIRST, ensure they FAIL before implementation.**

- [ ] T019 [P] [US2] Write integration test in `tests/integration/collection_test.go`: push a Collection and query it via GraphQL `collection(by: namespacePath)`; assert `metadata.name`, `spec.title`, `spec.selector.matchLabels`, and `status.conditions` are correct
- [ ] T020 [P] [US2] Write integration test in `tests/integration/collection_test.go`: query `collections(first: 10)` and assert the pushed Collection appears in the connection edges

### Implementation for User Story 2

- [ ] T021 [US2] Rewrite `shared/schemas/collection.graphqls` from the contract in `specs/022-collection-resource-contract/contracts/collection.graphqls`: Kubernetes-style `Collection` type with `CollectionObjectMeta`, `CollectionSpec`, `LabelSelector`, `LabelSelectorRequirement`, `LabelSelectorOperator`, `CollectionStatus`, `CollectionCondition`, `ResolvedCollectionDefinition`, `CollectionBy @oneOf`, `CollectionNamespacePath`, `CollectionConnection`, `CollectionEdge`; legacy mutation stubs
- [ ] T022 [US2] Add shared types to `shared/schemas/schema.graphqls`: `LabelSelector`, `LabelSelectorRequirement`, `LabelSelectorOperator` (enum), `CollectionBy`, `CollectionNamespacePath`
- [ ] T023 [US2] Run gqlgen code generation: `go generate ./internal/graph/...` in `gitstore-api/`
- [ ] T024 [US2] Replace legacy collection resolvers in `gitstore-api/internal/graph/collection.resolvers.go`: implement `Collection` (lookup by ID or namespacePath via `@oneOf`), `Collections` (paginated listing), and stub legacy mutations to return `"collection mutations are managed via git push"`
- [ ] T025 [US2] Add `GetCollectionByUID`, `GetCollectionByName`, `ListCollections` service wrappers to `gitstore-api/internal/graph/service.go`

**Checkpoint**: `collection(by: ...)` and `collections(...)` queries work end-to-end. Legacy mutations return informative errors.

---

## Phase 5: User Story 3 — Selector-driven membership via `collection.products` (Priority: P2)

**Goal**: `collection.products(first, last, ...)` returns products whose labels satisfy the collection's selector, using snapshot-at-query-time cursor semantics.

**Independent Test**: Push a Collection with `matchLabels: {gitstore.dev/brand: apple}`, push products with and without that label, query `collection.products(first: 10)`, and assert only matching products are returned.

### Tests for User Story 3

> **Write these tests FIRST, ensure they FAIL before implementation.**

- [ ] T026 [P] [US3] Write integration test in `tests/integration/collection_test.go`: push a Collection with `matchLabels`, push two products (one matching, one not), query `collection.products(first: 10)`, assert only the matching product is returned
- [ ] T027 [P] [US3] Write integration test in `tests/integration/collection_test.go`: verify snapshot cursor — fetch page 1, relabel a product to no longer match, fetch page 2 using the page-1 cursor, assert the previously matching product is still returned on page 2 (snapshot semantics)
- [ ] T028 [P] [US3] Write integration test in `tests/integration/collection_test.go`: push a Collection with no `spec.selector`; assert `collection.products(first: 10).edges` is empty

### Implementation for User Story 3

- [ ] T029 [US3] Implement `ListProductsByLabelSelector` in `gitstore-api/internal/datastore/memdb/backend.go`: fetch all products in the namespace, filter using `catalog.MatchesLabels`, return ordered slice
- [ ] T030 [US3] Implement `ListProductsByLabelSelector` in `gitstore-api/internal/datastore/scylla/backend.go`: scan `products_by_namespace` partition, filter using `catalog.MatchesLabels`, return ordered slice
- [ ] T031 [US3] Add `ListProductsBySelector` service wrapper to `gitstore-api/internal/graph/service.go`
- [ ] T032 [US3] Implement `collection.Products` resolver in `gitstore-api/internal/graph/collection.resolvers.go` with snapshot-at-query-time cursor: on first page, evaluate selector via `ListProductsBySelector`, encode ordered UID list into opaque cursor; on subsequent pages, decode cursor and slice; build `ProductConnection` via `BuildProductConnection`

**Checkpoint**: `collection.products` returns correctly filtered products with stable pagination.

---

## Phase 6: User Story 4 — Update and re-validate a Collection (Priority: P2)

**Goal**: Pushing an updated Collection re-validates the document and updates the persisted spec; `AdmissionAccepted` condition is refreshed.

**Independent Test**: Push a Collection, then push an updated version with a changed title; query and assert the updated title is returned and `AdmissionAccepted` reflects the new revision.

### Tests for User Story 4

> **Write these tests FIRST, ensure they FAIL before implementation.**

- [ ] T033 [P] [US4] Write integration test in `tests/integration/collection_test.go`: push a Collection, then push the same Collection with a changed `spec.title`; query and assert `spec.title` reflects the update and `metadata.generation` has incremented
- [ ] T034 [P] [US4] Write integration test in `tests/integration/collection_test.go`: push a Collection with a wide selector (many matches), then push with a narrower selector; assert `collection.products(first: 50)` reflects only the narrowed set after the second push

### Implementation for User Story 4

- [ ] T035 [US4] Verify `AdmitResources` in `gitstore-api/internal/cataloggrpc/server.go` correctly uses `UpdateCollection` for an existing `(namespace, name)` pair (upsert logic): increment `Generation`, update `ResourceVersion`, set `Revision` to new git SHA, refresh `AdmissionAccepted` condition timestamp

**Checkpoint**: Collection updates are idempotent and correctly increment generation.

---

## Phase 7: Polish & Cross-Cutting Concerns

- [ ] T036 [P] Add unit tests for `DatastoreCollectionToGraphQL` in `gitstore-api/internal/graph/converters_test.go`: spec media hydration, nil-spec → empty media, nil input → nil output, selector hydration
- [ ] T037 [P] Add unit tests for `Collection` and `Collections` resolvers in `gitstore-api/internal/graph/collection_resolver_test.go` following the pattern in `category_resolver_test.go`
- [ ] T038 [P] Write author guide `docs/products/collection.md` documenting Collection frontmatter schema, selector syntax, `collection.products` semantics, and example documents (model on `docs/products/category-taxonomy.md`)
- [ ] T039 Run `make pr-ready` (build + test + lint + license-check) and resolve all failures

---

## Dependencies & Execution Order

### Phase Dependencies

- **Phase 1 (Setup)**: No dependencies — start immediately; T002 and T003 can run in parallel with T001.
- **Phase 2 (Foundational)**: Depends on Phase 1 completion. T004–T013 can proceed: T004/T005 parallel, T006 after T005, T007–T008 after T006, T009–T010 after T006, T011 after T006/T008/T010, T012 after T001, T013 after T008/T010/T011.
- **Phase 3 (US1)**: Depends on Phase 2 completion. T014–T016 (tests) written first; T017–T018 implement.
- **Phase 4 (US2)**: Depends on Phase 3. T019–T020 (tests) first; T021–T022 can run in parallel; T023 after T021/T022; T024–T025 after T023.
- **Phase 5 (US3)**: Depends on Phase 4 (needs `collection.products` field in schema). T026–T028 (tests) first; T029/T030 [P]; T031–T032 after T029/T030.
- **Phase 6 (US4)**: Depends on Phase 3 foundation (upsert logic). T033/T034 [P]; T035 implementation.
- **Phase 7 (Polish)**: Depends on all prior phases. T036/T037/T038 [P].

### User Story Dependencies

- **US1 (P1)**: Depends only on Foundational (Phase 2) — no cross-story dependency.
- **US2 (P1)**: Depends on US1 (entity admitted before it can be queried).
- **US3 (P2)**: Depends on US2 (schema + resolver scaffolding needed for `collection.products` field).
- **US4 (P2)**: Depends on US1 (upsert path must exist).

### Parallel Opportunities

- T002 + T003 (selector impl + selector tests) in parallel after T001.
- T004 + T005 in parallel (different files in foundational phase).
- T007 + T009 in parallel (memdb schema + scylla models, different files).
- T008 + T010 in parallel (memdb backend + scylla backend).
- T014 + T015 + T016 in parallel (all integration test stubs for US1).
- T019 + T020 in parallel (integration test stubs for US2).
- T021 + T022 in parallel (schema rewrites in different files).
- T026 + T027 + T028 in parallel (integration test stubs for US3).
- T029 + T030 in parallel (memdb + scylla selector implementations).
- T033 + T034 in parallel (integration test stubs for US4).
- T036 + T037 + T038 in parallel (polish).

---

## Parallel Example: User Story 1

```bash
# Write tests first (all in parallel):
T014: "push valid Collection → assert success"
T015: "push Collection missing title → assert rejection"
T016: "push Collection with bad targetRef → assert rejection"

# Then implement (sequential):
T017: AdmitResources Collection branch (depends on T014–T016 failing)
T018: DatastoreCollectionToGraphQL converter
```

---

## Implementation Strategy

### MVP (User Stories 1 + 2)

1. Complete Phase 1 (Setup) — T001–T003
2. Complete Phase 2 (Foundational) — T004–T013
3. Complete Phase 3 (US1: push admission) — T014–T018
4. Complete Phase 4 (US2: query) — T019–T025
5. **STOP and VALIDATE**: push a Collection and query it via GraphQL
6. Demo / ship P1 increment

### Incremental Delivery

1. Setup + Foundational → entity + datastore ready
2. US1 → push admission working
3. US2 → query working → **P1 complete, shippable**
4. US3 → `collection.products` with selector → P2 membership
5. US4 → update handling → P2 complete
6. Polish → docs + coverage

---

## Notes

- `AdmissionAccepted` is the only status condition populated at admission time. `MembersResolved`, `Ready`, `observedGeneration`, and `resolved.memberCount` are set by the controller reconciliation loop (deferred to spec #244 / GH#244).
- ScyllaDB migration replaces the legacy `collections` table inline in `001_initial_schema.cql` — no separate `003` migration file needed.
- Black-box integration tests live in `tests/integration/` at the repository root (not inside `gitstore-api/`).
- `collection.products` snapshot cursor: encode ordered UID list (result of selector evaluation) into an opaque token on the first page; subsequent pages decode and slice — no external cache needed for correctness, though an LRU can be added later as an optimisation.
- Each task should be committed after completion or as a logical group; run `make test` before each commit.
