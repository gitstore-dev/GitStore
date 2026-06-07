# Tasks: Collection Frontmatter Integration Tests and Documentation

**Input**: Design documents from `/specs/023-collection-integration-tests/`  
**Prerequisites**: plan.md ✅, spec.md ✅, research.md ✅, data-model.md ✅, contracts/ ✅, quickstart.md ✅

**Tests**: Test-First Development (Constitution Principle I - NON-NEGOTIABLE). Tests MUST be written before implementation.

**Organization**: Tasks are grouped by user story to enable independent implementation and testing of each story.

## Format: `[ID] [P?] [Story] Description`

- **[P]**: Can run in parallel (different files, no dependencies)
- **[Story]**: Which user story this task belongs to
- Include exact file paths in descriptions

---

## Phase 1: Setup (Shared Infrastructure)

**Purpose**: Add the `commitCollection` push helper so all subsequent test phases have the scaffolding they need.

- [x] T001 Add `commitCollection(filename, content string)` helper method to `pushHelper` struct in `tests/integration/githelper_test.go`, following the same pattern as `commitProduct` and `commitCategory`

---

## Phase 2: Foundational (Blocking Prerequisites)

**Purpose**: Add Collection fixture functions that all integration test phases depend on.

**⚠️ CRITICAL**: No user story test work can begin until this phase is complete.

- [x] T002 Add fixture functions `minimalCollectionFixture(name, ns string) string`, `collectionWithMatchLabels(name, ns string, labels map[string]string) string`, `collectionWithMatchExpression(name, ns, key, operator string, values []string) string`, `invalidCollectionMissingTitle(name, ns string) string`, and `invalidCollectionBadTargetRef(name, ns, targetKind string) string` in new file `tests/integration/collection_test.go` (package `integration`, build constraint: none)

**Checkpoint**: Push helper and fixtures ready — user story test implementation can now begin.

---

## Phase 3: User Story 1 — Verify Valid Collection Document Is Accepted (Priority: P1) 🎯 MVP

**Goal**: Prove the end-to-end happy path: a valid Collection document is admitted, status conditions are set, membership is resolved, and the collection is queryable via GraphQL.

**Independent Test**: Run `TestCollection_ValidPushAccepted`, `TestCollection_WithSelectorMatchesProducts`, and `TestCollection_OptionalMediaAbsent` against a live memdb stack.

### Tests for User Story 1 (write first — verify they FAIL before any infra changes)

- [x] T003 [US1] Write `TestCollection_ValidPushAccepted` (T050) in `tests/integration/collection_test.go`: push `minimalCollectionFixture` (title only, no selector), assert push succeeds, GraphQL `collection` query returns `status.conditions[Ready].status == "True"` and `status.resolved.memberCount == 0`
- [x] T004 [US1] Write `TestCollection_WithSelectorMatchesProducts` (T051) in `tests/integration/collection_test.go`: seed 3 products with label `gitstore.dev/brand: apple`, push `collectionWithMatchLabels` with `gitstore.dev/brand: apple`, assert push succeeds, `collection.products` edges length >= 3, `status.resolved.memberCount >= 3`
- [x] T005 [US1] Write `TestCollection_OptionalMediaAbsent` (T052) in `tests/integration/collection_test.go`: push collection with `media: [{fileRef: {name: missing-hero, kind: File, optional: true}}]`, assert push succeeds and `status.resolved.media` is empty

**Checkpoint**: Run tests — all three MUST FAIL (stack up but collection tests are new code paths). Then proceed.

### Implementation for User Story 1

- [x] T006 [US1] Implement GraphQL helper `queryCollection(t, apiURL, namespace, name string)` in `tests/integration/collection_test.go` that executes the `collection(by: {namespacePath: {...}})` query and returns parsed status + products connection (mirrors `queryCategory` / `queryProduct` pattern)

**Checkpoint**: User Story 1 tests pass against a live memdb stack. Run `go test -v -run TestCollection_Valid ./tests/integration/` and `go test -v -run TestCollection_With ./tests/integration/` and `go test -v -run TestCollection_Optional ./tests/integration/`.

---

## Phase 4: User Story 2 — Verify Invalid Collection Document Is Rejected (Priority: P1)

**Goal**: Prove the validation/rejection path: every mandatory field violation, kind mismatch, invalid targetRef, and malformed selector expression produces a rejected push with a descriptive error message.

**Independent Test**: Run `TestCollection_MissingTitle`, `TestCollection_WrongKind`, `TestCollection_InvalidTargetRefKind`, `TestCollection_InvalidOperatorInExpression`, `TestCollection_InOperatorEmptyValues` — each must see a non-zero exit code and matching error substring in push output.

### Tests for User Story 2 (write first — verify they FAIL before any infra changes)

- [x] T007 [US2] Write `TestCollection_MissingTitle` (T053) in `tests/integration/collection_test.go`: push `invalidCollectionMissingTitle`, assert push rejected, output contains `"title"`
- [x] T008 [P] [US2] Write `TestCollection_WrongKind` (T054) in `tests/integration/collection_test.go`: push a Collection-path document with `kind: Product`, assert push rejected, output contains kind-related error substring
- [x] T009 [P] [US2] Write `TestCollection_InvalidTargetRefKind` (T055) in `tests/integration/collection_test.go`: push `invalidCollectionBadTargetRef("coll-invalid", ns, "CategoryTaxonomy")`, assert push rejected, output contains `"targetRef.kind"`
- [x] T010 [P] [US2] Write `TestCollection_InvalidOperatorInExpression` (T056) in `tests/integration/collection_test.go`: push collection with `matchExpressions: [{key: "k", operator: "Between", values: ["x"]}]`, assert push rejected, output contains `"matchExpressions"`
- [x] T011 [P] [US2] Write `TestCollection_InOperatorEmptyValues` (T057) in `tests/integration/collection_test.go`: push collection with `matchExpressions: [{key: "k", operator: "In", values: []}]`, assert push rejected, output contains `"requires at least one value"`

**Checkpoint**: All five rejection tests pass — push returns non-zero exit and error messages match expected substrings.

---

## Phase 5: User Story 3 — Selector Semantics and Deterministic Membership (Priority: P2)

**Goal**: Prove all five LabelSelector operator variants work correctly and that membership resolution is deterministic across repeated queries.

**Independent Test**: Run T058–T062 against a live memdb stack; all operator variants produce correct membership; two resolutions of the same collection return identical product IDs.

### Tests for User Story 3 (write first — verify they FAIL before any infra changes)

- [x] T012 [US3] Write `TestCollection_DeterministicMembership` (T058) in `tests/integration/collection_test.go`: push a collection with `matchLabels`, resolve `collection.products` twice, assert both responses return identical ordered sets of product names
- [x] T013 [P] [US3] Write `TestCollection_SelectorNotIn` (T059) in `tests/integration/collection_test.go`: seed product A with `brand: apple`, product B with `brand: samsung`, push collection with `matchExpressions: [{key: gitstore.dev/brand, operator: NotIn, values: [apple]}]`, assert only product B in `collection.products`
- [x] T014 [P] [US3] Write `TestCollection_SelectorExists` (T060) in `tests/integration/collection_test.go`: seed one product with label `gitstore.dev/featured: "true"`, one without, push collection with `matchExpressions: [{key: gitstore.dev/featured, operator: Exists}]`, assert only featured product included
- [x] T015 [P] [US3] Write `TestCollection_SelectorDoesNotExist` (T061) in `tests/integration/collection_test.go`: seed one product with label `gitstore.dev/sale: "true"`, one without, push collection with `matchExpressions: [{key: gitstore.dev/sale, operator: DoesNotExist}]`, assert only the non-sale product included
- [x] T016 [US3] Write `TestCollection_NewProductAppearsAfterPush` (T062) in `tests/integration/collection_test.go`: push collection, verify initial `memberCount`; push additional product with matching label; re-query collection, assert `memberCount` increased by 1

**Checkpoint**: All selector operator tests pass. SC-002 satisfied (all five variants covered: `matchLabels` via T051/T058, `In` via existing validation test, `NotIn` via T059, `Exists` via T060, `DoesNotExist` via T061).

---

## Phase 6: User Story 3 — Datastore Contract Tests (Supplement)

**Goal**: Verify the datastore layer's label-selector filtering methods work correctly at the CRUD abstraction level (both memdb and ScyllaDB).

**Independent Test**: Run `go test -v ./tests/contract/datastore/...` (memdb) and `go test -tags scylla -v ./tests/contract/datastore/...` (ScyllaDB with `GITSTORE_TEST_SCYLLA_ADDR` set).

### Tests for contract layer

- [x] T017 [US3] Extend `RunContractSuite` in `gitstore-api/tests/contract/datastore/contract_test.go` with sub-test `Collection/LabelSelector_MatchLabels`: create 3 collections — two with label `tier: premium`, one without; call `ListCollectionsByLabelSelector` (or equivalent) with `matchLabels: {tier: premium}`; assert exactly 2 returned
- [x] T018 [P] [US3] Extend `RunContractSuite` in `gitstore-api/tests/contract/datastore/contract_test.go` with sub-test `Collection/LabelSelector_NoMatch`: call `ListCollectionsByLabelSelector` with a label key that no collection has; assert empty slice returned (not an error)
- [x] T019 [P] [US3] Extend `RunContractSuite` in `gitstore-api/tests/contract/datastore/contract_test.go` with sub-test `Collection/LabelSelector_MatchExpressions_In`: create collections with labels `{env: prod}` and `{env: staging}`; query with `matchExpressions: [{key: env, operator: In, values: [prod]}]`; assert only the `prod` collection returned

**Checkpoint**: `go test -v ./tests/contract/datastore/...` passes for memdb. `go test -tags scylla -v ./tests/contract/datastore/...` passes with a live ScyllaDB.

---

## Phase 7: User Story 4 — Access Documentation and Examples (Priority: P2)

**Goal**: Produce the `docs/collection.md` reference document that enables a developer to write a valid Collection document without consulting source code.

**Independent Test**: A developer can follow the documentation to produce a valid Collection document and push it without errors.

- [x] T020 [P] [US4] Create `docs/collection.md` with the following sections: (1) Overview, (2) Document schema field reference table (all `CollectionSpec` + `ObjectMeta` author-writable fields), (3) `LabelSelector` operator reference with one example per variant (`matchLabels`, `In`, `NotIn`, `Exists`, `DoesNotExist`), (4) `CollectionStatus` field reference (`conditions`, `lastAppliedRevision`, `observedGeneration`, `resolved`), (5) Validation error table (all entries from `specs/023-collection-integration-tests/contracts/collection-validation-errors.md`), (6) Three complete copy-pasteable examples: minimal, full with selector+media, zero-member (no selector), (7) GraphQL query examples for `collection` and `collections`

**Checkpoint**: SC-003 and SC-004 satisfied. All `CollectionSpec` and `CollectionStatus` fields documented with descriptions and examples.

---

## Phase 8: Polish & Cross-Cutting Concerns

**Purpose**: Final validation, CI job verification, and quickstart alignment.

- [x] T021 Verify the integration test file `tests/integration/collection_test.go` has the AGPL-3.0-or-later license header and copyright line matching the project standard
- [x] T022 [P] Verify `gitstore-api/tests/contract/datastore/contract_test.go` license header is present and unchanged
- [x] T023 Run `go vet ./...` in `tests/integration/` and `gitstore-api/` to confirm no compilation errors or vet warnings introduced
- [x] T024 Confirm the `.github/workflows/ci-integration.yml` `integration-test` job (memdb) and `integration-test-scylla` job (ScyllaDB) already cover the `tests/integration/` module without changes — if a new job is required for the contract test `Collection/LabelSelector_*` group under ScyllaDB, add it; otherwise document that the existing `datastore-contract-test-scylla` job covers it
- [x] T025 [P] Run quickstart.md validation: start `make compose DETACH=1`, run `make bootstrap ADMIN_PASSWORD=admin123`, run `cd tests/integration && go test -v -run TestCollection ./...` and confirm all collection tests pass

---

## Dependencies & Execution Order

### Phase Dependencies

- **Setup (Phase 1)**: No dependencies — start immediately
- **Foundational (Phase 2)**: Depends on Phase 1 completion — BLOCKS all user story phases
- **User Story Phases (3–7)**: All depend on Phase 2 completion
  - Phase 3 (US1 P1) and Phase 4 (US2 P1) can run in parallel once Phase 2 is done
  - Phase 5+6 (US3 P2) can start after Phase 2; may run in parallel with Phase 3/4
  - Phase 7 (US4 P2) has no code dependencies — can run any time after Phase 2
- **Polish (Phase 8)**: Depends on all user story phases being complete

### User Story Dependencies

- **US1 (P1)**: After Phase 2 — no dependency on US2/US3/US4
- **US2 (P1)**: After Phase 2 — no dependency on US1/US3/US4
- **US3 (P2)**: After Phase 2 — uses same fixtures as US1; independently testable
- **US4 (P2)**: After Phase 2 — pure documentation; no code dependencies

### Within Each Phase

1. Test functions written and confirmed failing (run `go test ./...` against live stack — test not found or compiles but fails)
2. Implementation / helper code added to make tests pass
3. Tests pass — checkpoint confirmed

### Parallel Opportunities

- T008, T009, T010, T011 (US2 rejection tests) — all target `collection_test.go` but are independent test functions and can be written in one pass
- T013, T014, T015 (US3 selector tests NotIn/Exists/DoesNotExist) — independent test functions
- T017, T018, T019 (contract tests) — independent sub-tests within `RunContractSuite`
- T020 (docs) — completely independent of all test work; can be done any time after Phase 2
- T021, T022 (license checks) — independent file checks

---

## Parallel Example: User Story 2

```bash
# Write all rejection test stubs in one editing pass (same file, sequential):
T007: TestCollection_MissingTitle
T008: TestCollection_WrongKind
T009: TestCollection_InvalidTargetRefKind
T010: TestCollection_InvalidOperatorInExpression
T011: TestCollection_InOperatorEmptyValues

# Confirm all five fail against the live stack before any implementation:
cd tests/integration && go test -v -run "TestCollection_Missing|TestCollection_Wrong|TestCollection_Invalid|TestCollection_In" ./...
```

---

## Implementation Strategy

### MVP First (User Stories 1 + 2 only — both P1)

1. Complete Phase 1: Add `commitCollection` helper
2. Complete Phase 2: Add all fixture functions
3. Complete Phase 3: Valid push tests (T003–T006)
4. Complete Phase 4: Rejection tests (T007–T011)
5. **STOP and VALIDATE**: `go test -v -run TestCollection ./tests/integration/` — all 8 tests pass
6. **Deploy/demo**: P1 user stories independently verified

### Incremental Delivery

1. Phases 1–2: Infrastructure → Foundation ready
2. Phase 3: US1 happy path → independently testable
3. Phase 4: US2 rejection path → independently testable (P1 complete)
4. Phases 5–6: US3 selector semantics + contract tests → SC-002/SC-005 satisfied
5. Phase 7: US4 documentation → SC-003/SC-004 satisfied
6. Phase 8: Polish → feature merge-ready

---

## Notes

- [P] tasks = different files or independent functions, no blocking dependencies
- All integration tests in `tests/integration/` run against both backends without code changes (backend = compose overlay)
- ScyllaDB contract tests require `//go:build scylla` tag and a live ScyllaDB at `GITSTORE_TEST_SCYLLA_ADDR`
- Commit after each phase checkpoint, not after individual tasks
- Each user story phase produces a working, independently testable increment
