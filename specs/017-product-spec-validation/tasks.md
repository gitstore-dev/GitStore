# Tasks: Product Spec/Status Validation Semantics and Integration Tests

**Input**: Design documents from `/specs/017-product-spec-validation/`
**Branch**: `017-product-spec-validation`
**Issues**: #186 (Validation Semantics), #187 (Integration Tests and Documentation)

**Tests**: Test-First Development (Constitution Principle I - NON-NEGOTIABLE). Tests MUST be written before implementation.

**Organization**: Tasks are grouped by user story to enable independent implementation and testing of each story.

## Format: `[ID] [P?] [Story] Description`

- **[P]**: Can run in parallel (different files, no dependencies)
- **[Story]**: Which user story this task belongs to (US1, US2, US3, US4)

---

## Phase 1: Setup (Shared Infrastructure)

**Purpose**: Prepare testdata and documentation example files that are shared across all user stories.

- [X] T001 Create `docs/products/` directory and `docs/products/product-spec.md` field reference with valid example (FR-016)
- [X] T002 [P] Create `docs/products/examples/valid-product.md` — complete valid product file, verbatim-parseable (FR-016, FR-017)
- [X] T003 [P] Create `docs/products/examples/invalid-status.md` — product file with `status:` key, for rejection docs (FR-016, FR-017)
- [X] T004 [P] Create `docs/products/examples/invalid-title.md` — product file with `spec.title` > 200 chars, for rejection docs (FR-016, FR-017)
- [X] T005 [P] Create `docs/products/examples/invalid-media.md` — product file with `spec.media[0].fileRef.name` absent, for rejection docs (FR-016, FR-017)

**Checkpoint**: Documentation examples created — parseable by `validate.Parse` and loadable by integration tests.

---

## Phase 2: Foundational (Blocking Prerequisites)

**Purpose**: Core validator improvements that US1 and US2 both depend on. Must be complete before either story's implementation tasks.

**⚠️ CRITICAL**: No user story implementation can begin until this phase is complete.

- [X] T006 Add `TestParse_MultipleReadOnlyFieldsReportedTogether` in `gitstore-api/internal/validate/validator_test.go` — verify both `uid` and `resourceVersion` appear in a single rejection error; run it and confirm it FAILS
- [X] T007 Add `TestParse_SpecMedia_FieldPathInError` in `gitstore-api/internal/validate/validator_test.go` — verify error contains `spec.media[0].fileref.name` (full qualified path); run it and confirm it FAILS
- [X] T008 Extend `preParseChecks` in `gitstore-api/internal/validate/validator.go` to collect ALL forbidden read-only metadata fields before returning (not fail-fast); join into single error string (FR-008, FR-009) — verify T006 now passes
- [X] T009 Add `fieldPath` helper in `gitstore-api/internal/validate/validator.go` using `fe.StructNamespace()` to produce dotted lowercase paths (e.g. `spec.media[0].fileref.name`) and update `toFriendlyError` to use it (FR-002) — verify T007 now passes

**Checkpoint**: Foundation complete — `preParseChecks` accumulates all violations; struct-tag errors carry full field paths. Run `cd gitstore-api && go test ./internal/validate/... -v` — all pass.

---

## Phase 3: User Story 1 — Author receives precise errors when spec fields are invalid (Priority: P1) 🎯 MVP

**Goal**: Every spec constraint violation produces a field-scoped error naming the exact field and constraint. Multiple violations are reported together.

**Independent Test**: Push a product file with `spec.title` = 201 characters; verify the rejection message names `spec.title` and the 200-character limit.

### Tests for User Story 1

> **Write these FIRST — they MUST FAIL before implementation**

- [X] T010 [P] [US1] Add `TestParse_SpecTitle_TooLong_FieldNamedInError` in `gitstore-api/internal/validate/validator_test.go` — assert error contains `"spec.title"` and `"200"` (FR-001); run and confirm FAILS
- [X] T011 [P] [US1] Add `TestParse_SpecMedia_MissingFileRefName_IndexedError` in `gitstore-api/internal/validate/validator_test.go` — assert error contains `"spec.media[0]"` and `"fileref.name"` (FR-002); run and confirm FAILS
- [X] T012 [P] [US1] Add `TestParse_CategoryRef_MissingName_Rejected` in `gitstore-api/internal/validate/validator_test.go` — assert error contains `"categoryref.name"` (FR-005); run and confirm FAILS
- [X] T013 [P] [US1] Add `TestParse_OptionsEmptyList_Accepted` and `TestParse_OptionsAbsent_Accepted` in `gitstore-api/internal/validate/validator_test.go` — verify both pass without error (FR-006 edge case); run and confirm FAILS
- [X] T014 [P] [US1] Add `TestParse_MediaOptionalTrue_NoFileRefName_Rejected` in `gitstore-api/internal/validate/validator_test.go` — `optional: true` does NOT waive `name` requirement; run and confirm FAILS

### Implementation for User Story 1

- [X] T015 [US1] Verify `spec.title` max=200 constraint surfaces the qualified path `spec.title` via the updated `fieldPath` helper in `gitstore-api/internal/validate/validator.go`; adjust `toFriendlyError` max-tag case to include length if needed (FR-001) — verify T010 now passes
- [X] T016 [US1] Verify `spec.media[N].fileRef.name` and `spec.media[N].fileRef.kind` required constraints surface indexed paths via `fieldPath` in `gitstore-api/internal/validate/validator.go` (FR-002) — verify T011 now passes
- [X] T017 [US1] Verify `spec.categoryRef.name` required constraint surfaces the qualified path via `fieldPath` in `gitstore-api/internal/validate/validator.go` (FR-005) — verify T012 now passes
- [X] T018 [US1] Confirm `spec: {}`, absent `options`, and empty `options` list all accepted without error in `gitstore-api/internal/validate/validator.go` — no code change expected; verify T013 now passes
- [X] T019 [US1] Confirm `fileRef.name` required constraint fires even when `optional: true` is set in `gitstore-api/internal/catalog/product.go` `FileReference` struct — no code change expected; verify T014 now passes

**Checkpoint**: All US1 unit tests pass. Run `cd gitstore-api && go test ./internal/validate/... -v` — zero failures.

---

## Phase 4: User Story 2 — Author is blocked from setting system-managed fields (Priority: P1)

**Goal**: `status` key and any read-only metadata field in a pushed file are rejected with a message naming each forbidden field.

**Independent Test**: Push a product file containing `status: {}` (empty map); verify rejection message identifies `status` as system-managed without storing anything.

### Tests for User Story 2

> **Write these FIRST — they MUST FAIL before implementation**

- [X] T020 [P] [US2] Add `TestParse_StatusEmptyMap_Rejected` in `gitstore-api/internal/validate/validator_test.go` — `status: {}` must be rejected; run and confirm FAILS
- [X] T021 [P] [US2] Add `TestParse_MultipleReadOnlyFields_AllNamed` in `gitstore-api/internal/validate/validator_test.go` — a file with `uid`, `resourceVersion`, and `generation` must name all three in the error (FR-008, FR-009); run and confirm FAILS

### Implementation for User Story 2

- [X] T022 [US2] Verify `status: {}` (empty map) fires the forbidden-key guard in `preParseChecks` in `gitstore-api/internal/validate/validator.go` (FR-007) — guard checks key presence, not value; no code change expected if already correct; verify T020 now passes
- [X] T023 [US2] Confirm updated `preParseChecks` (from T008) correctly accumulates all forbidden metadata fields into a single error — verify T021 now passes

**Checkpoint**: All US2 unit tests pass. Run `cd gitstore-api && go test ./internal/validate/... -v` — zero failures including T006, T020, T021.

---

## Phase 5: User Story 3 — Operator queries a product and receives accurate pipeline status (Priority: P2)

**Goal**: The GraphQL API returns the full `ProductStatus` blob written by the controller, with Kubernetes TitleCase normalised to GraphQL enums. Products with no recorded status return `null` status.

**Independent Test**: Store a product with a status blob containing all six condition types in Kubernetes TitleCase; query via the API and verify all six conditions are present with correct enum values and no conditions are dropped.

### Tests for User Story 3

> **Write these FIRST — they MUST FAIL before implementation**

- [X] T024 [P] [US3] Add `TestStatusFromJSON_AllSixConditionTypes_Normalised` in `gitstore-api/internal/graph/converters_test.go` — all six K8s TitleCase types map to correct GraphQL enums; run and confirm result (FR-012)
- [X] T025 [P] [US3] Add `TestStatusFromJSON_UnrecognisedConditionType_PassedThrough` in `gitstore-api/internal/graph/converters_test.go` — unknown type uppercased and not dropped (edge case); run and confirm result
- [X] T026 [P] [US3] Add `TestStatusFromJSON_JPY_PriceRange_NoLoss` in `gitstore-api/internal/graph/converters_test.go` — JPY `priceRange` values survive JSON round-trip without truncation (FR-013, SC-005); run and confirm result
- [X] T027 [P] [US3] Add `TestProductResolver_StatusAbsent_ReturnsNilStatus` in `gitstore-api/internal/graph/product_resolver_test.go` — product with nil `Status` blob returns `null` in GraphQL response (FR-011); run and confirm result

### Implementation for User Story 3

- [X] T028 [US3] Verify `statusFromJSON` in `gitstore-api/internal/graph/converters.go` correctly maps all six K8s TitleCase condition types — add any missing entries to `k8sConditionTypeToGraphQL`; verify T024 passes (FR-012)
- [X] T029 [US3] Verify unknown condition type passthrough in `statusFromJSON` in `gitstore-api/internal/graph/converters.go` — no code change expected; verify T025 passes
- [X] T030 [US3] Verify `shopspring/decimal` JSON round-trip for JPY zero-decimal currency in `gitstore-api/internal/catalog/status.go` `PriceRangeDefinition` — no code change expected; verify T026 passes (FR-013)
- [X] T031 [US3] Verify `DatastoreProductToGraphQL` in `gitstore-api/internal/graph/converters.go` returns `nil` `Status` field when `p.Status` is empty — no code change expected; verify T027 passes (FR-011)

**Checkpoint**: All US3 tests pass. Run `cd gitstore-api && go test ./internal/graph/... -v` — zero failures.

---

## Phase 6: User Story 4 — Developer validates the full lifecycle via documented examples (Priority: P3)

**Goal**: An integration test suite exercises the full product lifecycle against a running service. Documentation examples are tested by the actual parser. Zero `t.Skip` calls.

**Independent Test**: Run `cd tests/integration && go test ./... -v` against a running stack; every test passes with zero failures and zero skips.

### Tests for User Story 4

> **Write these FIRST — they MUST FAIL before implementation (requires running stack)**

- [X] T032 [P] [US4] Add `TestProductLifecycle_ValidFile_AcceptedAndQueryable` in `tests/integration/product_lifecycle_test.go` — git-push a valid product file via HTTP, query via GraphQL `product(by)`, assert spec fields match (FR-014)
- [X] T033 [P] [US4] Add `TestProductLifecycle_InvalidTitle_PushRejected` in `tests/integration/product_lifecycle_test.go` — push a file with `spec.title` > 200 chars, assert push is rejected with error naming `spec.title` and the limit (FR-014, FR-015)
- [X] T034 [P] [US4] Add `TestProductLifecycle_StatusPresent_PushRejected` in `tests/integration/product_lifecycle_test.go` — push a file with `status:` key, assert rejected with system-managed message (FR-014, FR-015)
- [X] T035 [P] [US4] Add `TestProductLifecycle_MissingFileRefName_PushRejected` in `tests/integration/product_lifecycle_test.go` — push a file missing `fileRef.name`, assert rejection names `spec.media[N].fileRef.name` (FR-014, FR-015)
- [X] T036 [P] [US4] Add `TestProductLifecycle_StatusHydration` in `tests/integration/product_lifecycle_test.go` — ingest product, write controller status blob to datastore directly, query via GraphQL, assert all conditions returned with correct enum values (FR-014)
- [X] T037 [P] [US4] Add `TestDocumentationExamples_ParseCorrectly` in `tests/integration/product_lifecycle_test.go` — load each file from `docs/products/examples/` and call `validate.Parse`; assert valid example accepted, rejection examples rejected with expected messages (FR-017)

### Implementation for User Story 4

- [X] T038 [US4] Create `tests/integration/product_lifecycle_test.go` with `package integration` header and imports for `validate`, HTTP client, GraphQL client (FR-014)
- [X] T039 [US4] Implement `TestProductLifecycle_ValidFile_AcceptedAndQueryable` in `tests/integration/product_lifecycle_test.go` using `githelper_test.go` push helper and GraphQL query helper; no `t.Skip` calls (FR-014, FR-015) — verify T032 passes
- [X] T040 [US4] Implement `TestProductLifecycle_InvalidTitle_PushRejected`, `TestProductLifecycle_StatusPresent_PushRejected`, `TestProductLifecycle_MissingFileRefName_PushRejected` in `tests/integration/product_lifecycle_test.go`; assert exact documented error messages (FR-014, FR-015) — verify T033, T034, T035 pass
- [X] T041 [US4] Implement `TestProductLifecycle_StatusHydration` in `tests/integration/product_lifecycle_test.go`; write status blob to datastore via API mutation or direct store call, then query and assert conditions (FR-014) — verify T036 passes
- [X] T042 [US4] Implement `TestDocumentationExamples_ParseCorrectly` in `tests/integration/product_lifecycle_test.go`; use `os.Open` to load files from `../../docs/products/examples/`; no `t.Skip` (FR-017) — verify T037 passes
- [X] T043 [US4] Audit all files in `tests/integration/` for `t.Skip`/`t.Skipf` calls and remove or convert to `t.Fatal` with a clear prerequisite message (FR-015)

**Checkpoint**: All US4 integration tests pass with zero skips. Run `cd tests/integration && go test ./... -v` against a running stack.

---

## Phase 7: Polish & Cross-Cutting Concerns

**Purpose**: Documentation completeness, final audit, and PR readiness.

- [X] T044 [P] Complete `docs/products/product-spec.md` with full field reference table, constraint summary, and links to the four example files (FR-016)
- [X] T045 [P] Audit `gitstore-api/internal/validate/validator_test.go` for any remaining `t.Skip` calls and remove them (FR-015 — applies to unit tests too)
- [X] T046 Run `make pr-ready` from repo root and resolve any lint, vet, or license-check failures
- [X] T047 Run `cd gitstore-api && go test ./... -v` and confirm zero failures across all packages
- [ ] T048 Run `cd tests/integration && go test ./... -v` against a running stack and confirm zero failures, zero skips (SC-003)

---

## Dependencies & Execution Order

### Phase Dependencies

- **Setup (Phase 1)**: No dependencies — T001–T005 can start immediately
- **Foundational (Phase 2)**: Depends on Phase 1 — BLOCKS US1 and US2 implementation
- **US1 (Phase 3)**: Depends on Phase 2 (T008, T009 complete)
- **US2 (Phase 4)**: Depends on Phase 2 (T008 complete); can run in parallel with US1
- **US3 (Phase 5)**: Independent of Phase 2 — can run in parallel with US1/US2
- **US4 (Phase 6)**: Depends on US1 (Phase 3) and US2 (Phase 4) complete; US3 preferred complete but not blocking
- **Polish (Phase 7)**: Depends on all story phases complete

### User Story Dependencies

- **US1 (P1)**: Requires Foundational phase — no dependency on US2 or US3
- **US2 (P1)**: Requires Foundational phase — no dependency on US1 or US3
- **US3 (P2)**: Independent — no dependency on US1 or US2
- **US4 (P3)**: Requires US1 and US2 complete (integration tests assert their error messages); US3 preferred but US4 can run before status hydration tests are added

### Within Each User Story

- Test tasks (T010–T014, T020–T021, T024–T027, T032–T037) MUST be written and FAIL before implementation tasks
- Implementation verifies tests pass — do not proceed if tests still fail
- All [P]-marked tasks within a phase can run concurrently

---

## Parallel Opportunities

### Phase 1 (Setup)
```
T002, T003, T004, T005 all run in parallel (independent files)
```

### Phase 3 (US1) — after T008, T009 complete
```
Tests:           T010, T011, T012, T013, T014 in parallel
Implementations: T015, T016, T017, T018, T019 in parallel (after their paired test fails)
```

### Phase 4 (US2) — after T008 complete, concurrent with Phase 3
```
Tests:           T020, T021 in parallel
```

### Phase 5 (US3) — concurrent with Phase 3/4
```
Tests:           T024, T025, T026, T027 in parallel
Implementations: T028, T029, T030, T031 in parallel
```

### Phase 6 (US4) — test stubs T032–T037 in parallel
```
Tests (stubs):   T032, T033, T034, T035, T036, T037 in parallel
```

---

## Implementation Strategy

### MVP First (US1 + US2 — both P1)

1. Complete Phase 1: Setup (documentation examples)
2. Complete Phase 2: Foundational (multi-error accumulation, field path helper)
3. Complete Phase 3: US1 (spec field validation errors)
4. Complete Phase 4: US2 (forbidden system-managed fields)
5. **STOP and VALIDATE**: `make test` — all unit tests pass

### Incremental Delivery

1. Setup + Foundational → validator improvements ready
2. US1 + US2 → field-scoped rejection errors ship (#186 closed)
3. US3 → status hydration contract verified (#186 status portion closed)
4. US4 → integration test suite + documentation examples ship (#187 closed)

### Notes

- [P] tasks = different files, no dependencies on incomplete tasks in the same phase
- Constitution Principle I: every test task must be run and confirmed FAILING before its paired implementation task
- FR-015 is a hard constraint: `grep -r 't\.Skip' tests/integration/` must return empty before PR
- `make pr-ready` must pass before any PR is opened
