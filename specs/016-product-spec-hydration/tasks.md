# Tasks: Product Spec and Status Hydration

**Input**: Design documents from `/specs/016-product-spec-hydration/`
**Prerequisites**: plan.md ✓, spec.md ✓, research.md ✓, data-model.md ✓, contracts/converter.md ✓, quickstart.md ✓

**Tests**: Test-First Development (Constitution Principle I — NON-NEGOTIABLE). Tests written before implementation, verified failing before code is written.

**No-skip rule (user directive)**: Every test MUST pass or fail. `t.Skip` / `t.Skipf` calls are forbidden. If a test requires a running Scylla cluster, ensure the cluster is up (`make scylla`) before running — do not skip. Failed preconditions are fixed, not skipped.

**Organization**: Tasks grouped by user story for independent implementation and testing.

## Format: `[ID] [P?] [Story] Description`

- **[P]**: Can run in parallel (different files, no dependencies)
- **[Story]**: User story label (US1, US2, US3, US4)
- Exact file paths included in every task

---

## Phase 1: Setup (Schema + Migration — Blocks All Stories)

**Purpose**: GraphQL schema edits and CQL migration inline edit. Must complete before gqlgen regeneration and any story work.

- [X] T001 Edit `shared/schemas/product.graphqls` line 8: replace `product(namespace: String!, name: String!): Product` with `product(by: ProductBy!): Product`
- [X] T002 Edit `shared/schemas/schema.graphqls` lines 102–108: replace the dead `input ProductBy @oneOf { id: ID, sku: String }` block with the new `@oneOf` definition (`id: ID` arm + `namespacePath: ProductNamespacePath` arm) and add the `input ProductNamespacePath { namespace: String!, name: String! }` type
- [X] T003 Run `cd gitstore-api && go generate ./...` to regenerate `models_gen.go` and resolver stubs from the updated schema; confirm compilation succeeds after regeneration
- [X] T004 Inline-edit `gitstore-api/internal/datastore/scylla/migrations/001_initial_schema.cql`: replace the existing `products` table definition and its `products_by_uid` secondary index with the three new tables — `products_by_namespace` (partition `namespace`, clustering `creation_timestamp DESC, uid DESC`), `products_by_name` (partition `namespace`, clustering `name`), `products_by_uid` (partition `uid`) — per the CQL from data-model.md

**Checkpoint**: Schema compiles (`go build ./...`) and migration file contains the three new tables.

---

## Phase 2: Foundational — Scylla Backend Rewrite (Blocks US3 Contract Tests)

**Purpose**: Replace the single-table Scylla backend with the three-table, logged-batch design. Required before any Scylla contract tests can run.

- [X] T005 Edit `gitstore-api/internal/datastore/scylla/models.go`: remove the `Product` table model; add `ProductByNamespace`, `ProductByName`, and `ProductByUID` using `table.New(...)` with column sets matching the three new CQL tables from T004
- [X] T006 [P] Edit `gitstore-api/internal/datastore/scylla/pagination.go`: generalise `buildPaginatedSelect` so it accepts `partitionCol string` and `partitionVal any` parameters instead of hardcoding `bucket = ?`; update all existing callers (categories, collections, namespaces) to pass `"bucket", BucketAll`
- [X] T007 Edit `gitstore-api/internal/datastore/scylla/backend.go`: rewrite `CreateProduct`, `UpdateProduct`, and `DeleteProduct` to execute a single `gocql.LoggedBatch` that writes to all three tables (`products_by_namespace`, `products_by_name`, `products_by_uid`) atomically
- [X] T008 Edit `gitstore-api/internal/datastore/scylla/backend.go`: rewrite `GetProduct(uid)` as a two-step lookup (`products_by_uid → namespace + creation_timestamp → products_by_namespace`) and `GetProductByName(namespace, name)` as a two-step lookup (`products_by_name → uid + creation_timestamp → products_by_namespace`)
- [X] T009 Edit `gitstore-api/internal/datastore/scylla/backend.go`: rewrite `ListProducts` to call the generalised `buildPaginatedSelect` with `partitionCol="namespace"`, `partitionVal=namespace`; remove any `paginateProductsInMemory` shim; apply local `reverseRows` for backward pagination (`last/before`)

**Checkpoint**: `go build ./internal/datastore/scylla/...` succeeds. No in-memory pagination shim for products.

---

## Phase 3: User Story 1 — Spec Hydration (Priority: P1) 🎯 MVP

**Goal**: `DatastoreProductToGraphQL` returns fully populated `ProductSpec` from the stored JSON blob. Empty blob → empty (not nil) spec.

**Independent Test**: `go test -race ./internal/graph/... -run TestDatastoreProductToGraphQL_SpecHydration -v`

### Tests for US1 (write FIRST — must FAIL before T012)

- [X] T010 [P] [US1] Write unit tests in `gitstore-api/internal/graph/converters_test.go` (create if absent) for `specFromJSON`: (a) nil blob → empty spec with non-nil `Tags`/`Media`/`Options` slices; (b) valid JSON blob → fully populated `*model.ProductSpec` matching input; (c) malformed JSON → empty spec (no panic); (d) populated spec returned by `DatastoreProductToGraphQL` when `p.Spec` is a valid blob
- [X] T011 [P] [US1] Write unit tests in `gitstore-api/internal/graph/converters_test.go` for `ownerRefsFromJSON`: (a) nil blob → empty `[]*model.OwnerReference{}`; (b) valid JSON array → populated slice; (c) malformed JSON → empty slice

### Implementation for US1

- [X] T012 [US1] Implement `specFromJSON(raw json.RawMessage) *model.ProductSpec` helper in `gitstore-api/internal/graph/converters.go`; ensure nil/empty blob returns `&model.ProductSpec{Tags: []string{}, Media: []*model.MediaDefinition{}, Options: []*model.ProductOptionDefinition{}}`; on unmarshal error emit structured WARN log with `uid` field and return empty spec
- [X] T013 [US1] Implement `ownerRefsFromJSON(raw json.RawMessage) []*model.OwnerReference` helper in `gitstore-api/internal/graph/converters.go`; on nil/error return `[]*model.OwnerReference{}`; emit WARN on unmarshal error
- [X] T014 [US1] Edit `DatastoreProductToGraphQL` in `gitstore-api/internal/graph/converters.go`: replace the hardcoded `Spec: &model.ProductSpec{...}` with `Spec: specFromJSON(p.Spec)` and replace the hardcoded `Metadata.OwnerReferences: []*model.OwnerReference{}` with `ownerRefsFromJSON(p.OwnerRefs)`

**Checkpoint**: `go test -race ./internal/graph/... -run TestDatastoreProductToGraphQL` passes. US1 unit tests green.

---

## Phase 4: User Story 2 — Status Hydration (Priority: P1)

**Goal**: `DatastoreProductToGraphQL` returns fully populated `*ProductStatus` when `p.Status` is non-nil; nil when absent.

**Independent Test**: `go test -race ./internal/graph/... -run TestDatastoreProductToGraphQL_StatusHydration -v`

### Tests for US2 (write FIRST — must FAIL before T017)

- [X] T015 [P] [US2] Write unit tests in `gitstore-api/internal/graph/converters_test.go` for `statusFromJSON`: (a) nil blob → `nil` `*model.ProductStatus`; (b) valid JSON blob → populated `*model.ProductStatus` with correct `Conditions`, `ObservedGeneration`, `LastAppliedRevision`; (c) malformed JSON → `nil` (no panic); (d) `DatastoreProductToGraphQL` with valid status blob returns product with non-nil `Status` containing at least one condition
- [X] T016 [P] [US2] Write unit test in `gitstore-api/internal/graph/converters_test.go`: product with nil `p.Status` returns product where `Status` is `nil` (not an empty struct)

### Implementation for US2

- [X] T017 [US2] Implement `statusFromJSON(raw json.RawMessage) *model.ProductStatus` helper in `gitstore-api/internal/graph/converters.go`; on nil/empty blob return `nil`; on unmarshal error emit structured WARN log with `uid` and return `nil`
- [X] T018 [US2] Edit `DatastoreProductToGraphQL` in `gitstore-api/internal/graph/converters.go`: add `Status: statusFromJSON(p.Status)` to the returned `model.Product` struct

**Checkpoint**: `go test -race ./internal/graph/... -run TestDatastoreProductToGraphQL` passes. All US1 + US2 converter tests green.

---

## Phase 5: User Story 3 — Cursor Pagination (Priority: P1)

**Goal**: Successive `products(namespace)` pages return distinct, non-overlapping results via CQL-native keyset pagination.

**Independent Test**: `go test -race ./tests/contract/datastore/... -run TestPagination_Products -v` (requires `make scylla`)

### Tests for US3 (write FIRST — must FAIL before implementation completes; requires Scylla)

- [X] T019 [P] [US3] Write three-page forward cursor traversal test in `gitstore-api/tests/contract/datastore/contract_test.go`: create 25 products in a namespace; fetch page 1 (`first: 10`); fetch page 2 using `endCursor`; fetch page 3 using `endCursor`; assert pages return 10, 10, 5 distinct products; assert all 25 UIDs appear with no duplicates; assert `pageInfo.hasNextPage` false on page 3
- [X] T020 [P] [US3] Write backward cursor test in `gitstore-api/tests/contract/datastore/contract_test.go`: create 15 products; fetch last 5 (`last: 5`); fetch previous 5 using `startCursor`; assert distinct products and correct ordering (newest-first)
- [X] T021 [P] [US3] Write `GetProductByName` round-trip test in `gitstore-api/tests/contract/datastore/contract_test.go`: create product via `CreateProduct`; look up via `GetProductByName(namespace, name)`; assert returned product matches the created one (verifies `products_by_name` denormalised table)
- [X] T022 [P] [US3] Write `GetProduct(uid)` round-trip test in `gitstore-api/tests/contract/datastore/contract_test.go`: create product via `CreateProduct`; look up via `GetProduct(uid)`; assert returned product matches (verifies `products_by_uid` lookup table)
- [X] T023 [P] [US3] Write `UpdateProduct` batch fan-out test in `gitstore-api/tests/contract/datastore/contract_test.go`: create product; update it; verify updated values are consistent across lookups via `GetProduct(uid)`, `GetProductByName(namespace, name)`, and `ListProducts`
- [X] T024 [P] [US3] Write `DeleteProduct` batch fan-out test in `gitstore-api/tests/contract/datastore/contract_test.go`: create product; delete it; assert that `GetProduct(uid)`, `GetProductByName(namespace, name)`, and `ListProducts` all return not-found (verifies deletion from all three tables)
- [X] T025 [US3] Audit `gitstore-api/tests/contract/datastore/contract_test.go` and `gitstore-api/internal/datastore/scylla/backend_test.go` for any `t.Skip` or `t.Skipf` calls; replace every skip with `t.Fatal` or a proper setup assertion so tests fail rather than skip when Scylla is unavailable

### Implementation for US3

- [X] T026 [US3] Update `gitstore-api/internal/datastore/scylla/backend_test.go`: update existing product-related test cases to use the three new table names; remove any references to the old single `products` table
- [X] T027 [US3] Edit `gitstore-api/internal/graph/product.resolvers.go`: update the `Product` resolver signature to `Product(ctx context.Context, by model.ProductBy) (*model.Product, error)`; implement the `switch` dispatch — `by.ID` arm calls `GetProductByUID` (via `decodeNodeID`); `by.NamespacePath` arm calls `GetProductByName`; default arm returns error
- [X] T028 [US3] Scan all test files in `gitstore-api/` for calls using the old `product(namespace: ..., name: ...)` query shape; update each to use `product(by: { namespacePath: { namespace: ..., name: ... } })` or the `id` arm as appropriate
- [X] T029 [US3] Add a `product_resolver_test.go` in `gitstore-api/internal/graph/` (create if absent): write resolver unit tests for the `ProductBy.id` arm and the `ProductBy.namespacePath` arm; use an in-memory datastore mock; verify each arm routes to the correct datastore method and the result passes through `DatastoreProductToGraphQL`

**Checkpoint**: `go test -race ./tests/contract/datastore/... -v` passes (no skips) with Scylla running. All three pages return distinct products.

---

## Phase 6: User Story 4 — Spec Field Validation at Ingest (Priority: P2)

**Goal**: Confirm existing struct-tag validation enforces `spec.title` ≤ 200 chars and `spec.media[].fileRef` presence. No new validator code — only test coverage.

**Independent Test**: `go test -race ./internal/validate/... -run TestParse_Spec -v`

### Tests for US4 (write FIRST — must FAIL before verification step)

- [X] T030 [P] [US4] Add test in `gitstore-api/internal/validate/validator_test.go`: submit a `catalog.ProductSpec` with `Title` of 201 characters; assert `validate.Parse` (or the validator) returns a non-nil error mentioning `spec.title` or the `max` constraint
- [X] T031 [P] [US4] Add test in `gitstore-api/internal/validate/validator_test.go`: submit a `catalog.ProductSpec` with one `MediaDefinition` whose `fileRef` is zero-value (empty `Name` and `Kind`); assert `validate.Parse` returns a non-nil error mentioning `fileRef` or the `required` constraint
- [X] T032 [US4] Confirm tests T030–T031 pass without adding new validator code (struct tags already enforce the constraints); if either fails, identify the missing struct tag and add it to the appropriate field in `gitstore-api/internal/catalog/` (do not add new validator logic — only fix the missing tag)

**Checkpoint**: `go test -race ./internal/validate/... -v` passes with zero skips.

---

## Phase 7: Polish & Full Test Execution

**Purpose**: Validate the complete implementation against running services with no skips allowed.

- [X] T033 Audit every `*_test.go` file in `gitstore-api/` for `t.Skip`/`t.Skipf` calls; for each skip: if it guards a Scylla-dependency → replace with a `t.Fatal` that prints a clear message ("Scylla required; run `make scylla` first"); if it guards a test-data precondition → fix the test setup instead; commit the audit result
- [X] T034 Start Scylla and apply the updated migration: run `docker compose down -v` against the Scylla volume, then `make scylla`, then confirm the three new tables (`products_by_namespace`, `products_by_name`, `products_by_uid`) exist in the keyspace
- [X] T035 Re-ingest bootstrap data: run `make bootstrap` against the running stack; confirm the API resolves `products(namespace: "gitstore")` without error
- [X] T036 Run `cd gitstore-api && go test -race ./internal/graph/... -v` — all converter and resolver unit tests must pass; no skips permitted; fix any failures before proceeding
- [X] T037 Run `cd gitstore-api && go test -race ./internal/validate/... -v` — all spec validation tests must pass; no skips; fix failures
- [X] T038 Run `cd gitstore-api && go test -race ./internal/datastore/scylla/... -v` — all Scylla backend unit tests must pass; no skips; fix failures
- [X] T039 Run `cd gitstore-api && go test -race ./tests/contract/datastore/... -v` against the running Scylla cluster — all contract tests (round-trip, three-page pagination, batch fan-out) must pass; no skips; fix failures
- [X] T040 Run `make test` from the repository root — confirm the full test suite passes with zero skips and zero failures; report final pass/fail counts
- [X] T041 [P] Update `docs/` with any API changes: document the new `product(by: ProductBy!)` query, the `ProductBy` `@oneOf` input, and `ProductNamespacePath`; note the breaking change from `product(namespace, name)`

---

## Dependencies & Execution Order

### Phase Dependencies

- **Phase 1 (Setup)**: No dependencies — start immediately
- **Phase 2 (Foundational)**: Depends on Phase 1 (models.go references generated types)
- **Phase 3 (US1)**: Depends on Phase 1 (converter uses models_gen.go); independent of Phase 2
- **Phase 4 (US2)**: Depends on Phase 1; independent of Phase 2 and US1 (different helper function)
- **Phase 5 (US3)**: Depends on Phase 1 AND Phase 2 (Scylla backend must exist for contract tests)
- **Phase 6 (US4)**: Independent — depends only on existing `validator_test.go` infrastructure
- **Phase 7 (Polish)**: Depends on all prior phases complete

### User Story Dependencies

- **US1** (T010–T014): Can begin after Phase 1 setup; independent of US2/US3
- **US2** (T015–T018): Can begin after Phase 1 setup; independent of US1/US3
- **US3** (T019–T029): Requires Phase 1 + Phase 2; can proceed in parallel with US1/US2 on the test-writing step
- **US4** (T030–T032): Independent of all other stories; can begin at any point after project compiles

### Parallel Opportunities

```bash
# Phase 1 has strict sequential order (T001 → T002 → T003 → T004)
# T003 (go generate) depends on T001 + T002; T004 is independent of T003

# Phase 2: T006 is independent; T007/T008/T009 depend on T005
# Parallelisable: T005 + T006 in parallel; then T007 → T008 → T009

# After Phase 1+2 complete, all story test-writing tasks are parallelisable:
# T010 [US1], T011 [US1], T015 [US2], T016 [US2], T019-T025 [US3], T030 [US4], T031 [US4]

# After test tasks fail (as expected), implementation tasks can proceed:
# T012 + T013 [US1] in parallel, then T014
# T017 [US2], then T018
# T026 + T027 + T028 + T029 [US3] — T026/T027/T028/T029 are independent files
```

---

## Implementation Strategy

### MVP (US1 + US2 only — no Scylla migration)

1. Phase 1: Schema + gqlgen regeneration
2. Phase 3: US1 spec hydration (unit tests + converter helpers)
3. Phase 4: US2 status hydration (unit tests + converter helper)
4. **VALIDATE**: `go test -race ./internal/graph/...` passes
5. Deploy/demo converter correctness without the Scylla schema change

### Full Delivery (all four stories)

1. Phase 1 → Phase 2 (schema + Scylla backend rewrite)
2. Phase 3 → Phase 4 (spec + status, unit tests)
3. Phase 5 (pagination contract tests + resolver update)
4. Phase 6 (validation tests)
5. Phase 7 (full test run against live Scylla; zero skips)

### No-Skip Enforcement

Before finalising Phase 7 (T033), run:

```bash
grep -rn 't\.Skip\|t\.Skipf' gitstore-api/
```

Every hit must be converted to `t.Fatal` (missing precondition) or removed (precondition now guaranteed). The Phase 7 test runs are the acceptance gate — the feature is not done until `make test` exits 0 with zero skips.

---

## Notes

- **Breaking change**: `product(namespace, name)` → `product(by: ProductBy!)`. All callers inside this repo must be updated in T028 before Phase 7 test runs.
- **Migration wipe**: Phase 7 T034 requires `docker compose down -v` for the Scylla volume; bootstrap data is re-ingested via `make bootstrap`. Do not skip the volume wipe — the migration checksum guard will reject the modified `001_initial_schema.cql` against an already-migrated keyspace.
- **gqlgen regen (T003)**: If `go generate ./...` modifies resolver stubs, merge the generated changes carefully — do not overwrite manual resolver implementations in `product.resolvers.go`.
- **Converter test file**: `converters_test.go` may not exist yet. Create it in `gitstore-api/internal/graph/` with the correct package declaration (`package graph`).
- **t.Skip audit (T033)**: Check both unit test files and integration/contract test files. The no-skip rule is absolute per user directive.
