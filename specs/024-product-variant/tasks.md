# Tasks: ProductVariant Catalog Item

**Input**: Design documents from `specs/024-product-variant/`
**Prerequisites**: plan.md ✅, spec.md ✅, research.md ✅, data-model.md ✅, contracts/ ✅, quickstart.md ✅

**Tests**: Test-First Development (Constitution Principle I - NON-NEGOTIABLE). Tests MUST be written before implementation code, verified to FAIL first.

**Organization**: Tasks are grouped by user story to enable independent implementation and testing.

## Format: `[ID] [P?] [Story] Description`

- **[P]**: Can run in parallel (different files, no dependencies on incomplete tasks)
- **[Story]**: Which user story this task belongs to (US1–US5)

## Push Context Design Note

A `PushContext` / `AdmissionContext` struct is populated **once** per gRPC request — resolving `repository_id` → namespace via a single DB lookup — and threaded through all per-file validation and admission helpers. This prevents repeated identical lookups as the number of resources per push grows. The refactor also cleans up function signatures: `admitProduct`, `admitCollection`, and `admitCategoryTaxonomyWithContext` currently take `req *catalogv1.AdmitResourcesRequest` + loose `repoNamespace`, `revision`, `now` params; these collapse into a single `AdmissionContext` argument.

---

## Phase 1: Setup

**Purpose**: Introduce the only new external dependency.

- [ ] T001 Add `github.com/google/cel-go` to `gitstore-api/go.mod` and run `go mod tidy`

---

## Phase 2: Foundational (Blocking Prerequisites)

**Purpose**: Core infrastructure that MUST be complete before ANY user story can be implemented.

**⚠️ CRITICAL**: No user story work can begin until this phase is complete.

- [ ] T002 Define `ValidationContext` and `AdmissionContext` structs (repositoryID, namespace, commitSHA, refName, revision, now) in `gitstore-api/internal/cataloggrpc/context.go`
- [ ] T003 [P] Add `ProductVariantResource`, `ProductVariantSpec`, `InventoryDefinition`, `PricingDefinition`, `PriceSet`, `PriceTemplate`, `QuantityDefinition`, `StrategyDefinition`, `EligibilityDefinition`, `PriceRuleConstraint`, `SelectedOptionDefinition`, `ProductVariantStatus`, `ResolvedProductVariantDefinition`, `ResolvedProductRef`, `ResolvedPriceSetDefinition`, `ResolvedInventoryDefinition` structs in `gitstore-api/internal/catalog/product_variant.go`
- [ ] T004 [P] Add `ConditionProductResolved`, `ConditionOptionsAccepted`, `ConditionPricingAccepted` constants (check for duplicates first) in `gitstore-api/internal/catalog/status.go`
- [ ] T005 [P] Add `ProductVariant` entity struct with denormalised `SKU` and `ProductRefName` string fields alongside the standard envelope (`UID`, `Namespace`, `Name`, `Spec json.RawMessage`, `Status json.RawMessage`, etc.) in `gitstore-api/internal/datastore/entities.go`
- [ ] T006 Add `CreateProductVariant`, `UpdateProductVariant`, `GetProductVariantByUID`, `GetProductVariantByName`, `GetProductVariantBySKU`, `ListProductVariants`, `ListProductVariantsByProductRef` to the `Datastore` interface in `gitstore-api/internal/datastore/datastore.go` (depends on T005)
- [ ] T007 Add `"product_variant"` memdb table with indexes `"id"` (UUID, unique), `"name_namespace"` (compound Namespace+Name, unique), `"namespace"` (non-unique), `"sku_namespace"` (compound Namespace+SKU, unique), `"product_ref"` (compound Namespace+ProductRefName, non-unique) in `gitstore-api/internal/datastore/memdb/schema.go` (depends on T005)
- [ ] T008 Implement all `ProductVariant` Datastore methods on the memdb backend in `gitstore-api/internal/datastore/memdb/backend.go` (depends on T006, T007)
- [ ] T009 [P] Add full `ProductVariant` GraphQL schema (types, enums, connection, `Product.productVariants` extension, `ProductVariantBy`/`ProductVariantNamespacePath` inputs) from `specs/024-product-variant/contracts/product_variant.graphqls` into `shared/schemas/product_variant.graphqls` and add `ProductVariantBy`/`ProductVariantNamespacePath` input types to `shared/schemas/schema.graphqls`
- [ ] T010 Run `go generate ./...` in `gitstore-api/` to regenerate gqlgen models and resolver stubs after T009 (depends on T009)
- [ ] T011 Refactor `ValidateResources` to populate a `ValidationContext` (resolve namespace once from `req.RepositoryId`); refactor `AdmitResources` to populate an `AdmissionContext` once and update signatures of `admitProduct`, `admitCollection`, `admitCategoryTaxonomyWithContext` to accept `AdmissionContext` instead of `req + repoNamespace + revision + now` params in `gitstore-api/internal/cataloggrpc/server.go` (depends on T002; existing tests must pass after refactor)

**Checkpoint**: Foundation complete — all user story phases can now begin.

---

## Phase 3: User Story 1 — Author and push a ProductVariant (Priority: P1) 🎯 MVP

**Goal**: A valid `ProductVariant` document pushed to the catalog is admitted and queryable by name.

**Independent Test**: Push a single valid `ProductVariant` document; verify it appears in `productVariant(by: {namespacePath: ...})` with all authored fields preserved.

### Tests for User Story 1

> **Write these FIRST — verify they FAIL before T014**

- [ ] T012 [US1] Write integration test: push valid `ProductVariant` → admitted and retrievable via `GetProductVariantByName` in `tests/integration/product_variant_test.go`
- [ ] T013 [P] [US1] Write integration test: push `ProductVariant` missing `spec.sku` → pre-receive rejects with descriptive error in `tests/integration/product_variant_test.go`

### Implementation for User Story 1

- [ ] T014 [US1] Add `"ProductVariant"` case to `ParseResource` kind dispatcher in `gitstore-api/internal/validate/validator.go` (depends on T003)
- [ ] T015 [US1] Implement `validateProductVariantSpec` (pre-receive structural rules: required fields, `metadata.name` DNS-label format, `kind`/`apiVersion` check) in `gitstore-api/internal/validate/validator.go` (depends on T014)
- [ ] T016 [US1] Implement `admitProductVariant` using `AdmissionContext` — resolve namespace from context, SKU uniqueness check via `GetProductVariantBySKU`, persist via `CreateProductVariant`/`UpdateProductVariant`, write `AdmissionAccepted` condition — in `gitstore-api/internal/cataloggrpc/server.go` (depends on T008, T011, T015)
- [ ] T017 [US1] Dispatch `ProductVariant` resources in the `AdmitResources` loop in `gitstore-api/internal/cataloggrpc/server.go` (depends on T016)

**Checkpoint**: Push a `ProductVariant` → admitted and persisted. T012–T013 must pass.

---

## Phase 4: User Story 2 — Query a ProductVariant (Priority: P1)

**Goal**: Storefront developers can query `productVariant` by name or ID, list `productVariants` by namespace, and traverse `product.productVariants`.

**Independent Test**: Query `productVariant(by: {namespacePath: ...})` and assert `spec.title`, `spec.sku`, `status.resolved.product.name`, `status.resolved.priceSet.priceCount`, and `status.resolved.inventory.availableQuantity` are present and correct.

### Tests for User Story 2

> **Write these FIRST — verify they FAIL before T019**

- [X] T018 [US2] Write integration tests: `productVariant` by name, `productVariant` by ID, `productVariants` paginated listing, `product.productVariants` connection — assert all `spec` and `status.resolved` fields in `tests/integration/product_variant_test.go`

### Implementation for User Story 2

- [X] T019 [P] [US2] Implement `DatastoreProductVariantToGraphQL` converter (spec, metadata, status, resolved fields) in `gitstore-api/internal/graph/converters.go` (depends on T003, T010)
- [X] T020 [P] [US2] Add `GetProductVariantByUID`, `GetProductVariantByName`, `ListProductVariants`, `ListProductVariantsByProductRef` delegation methods to `Service` in `gitstore-api/internal/graph/service.go` (depends on T008)
- [X] T021 [US2] Implement `productVariant`, `productVariants`, and `productVariantResolver.Spec`/`Status` resolvers in `gitstore-api/internal/graph/product_variant.resolvers.go` (depends on T019, T020)
- [X] T022 [US2] Implement `Product.productVariants` resolver on `productResolver` in `gitstore-api/internal/graph/product.resolvers.go` (depends on T020, T021)
- [X] T023 [US2] Register `ProductVariantResolver` on `Resolver` struct and wire to generated resolver interface in `gitstore-api/internal/graph/resolver.go` (depends on T021)

**Checkpoint**: All query paths functional. T018 must pass.

---

## Phase 5: User Story 3 — Parent product link and option compatibility (Priority: P1)

**Goal**: Admission validates `productRef` resolution and `selectedOptions` compatibility; co-pushed variants are admitted with deferred reconciliation.

**Independent Test**: Push a `ProductVariant` with an option name not on the parent product → rejected at admission with the incompatible option identified.

### Tests for User Story 3

> **Write these FIRST — verify they FAIL before T027**

- [X] T024 [US3] Write integration test: `productRef` not found in datastore → variant admitted with `ProductResolved: False`; simulate reconciler re-check → condition transitions to `True` in `tests/integration/product_variant_test.go`
- [X] T025 [P] [US3] Write integration test: `selectedOptions` name not in parent product → rejected at admission with error identifying the option in `tests/integration/product_variant_test.go`
- [X] T026 [P] [US3] Write integration test: push product + variant in same commit → both admitted; variant `ProductResolved: False` initially in `tests/integration/product_variant_test.go`

### Implementation for User Story 3

- [X] T027 [US3] Extend `admitProductVariant` with: `productRef` datastore lookup (found → `ProductResolved: True`; not found → `ProductResolved: False`, deferred), `selectedOptions` name+value compatibility check against parent product options, `selectedOptions` fingerprint uniqueness check across existing variants for same parent, write `ProductResolved` and `OptionsAccepted` conditions in `gitstore-api/internal/cataloggrpc/server.go` (depends on T016)

**Checkpoint**: Option validation and co-push semantics working. T024–T026 must pass.

---

## Phase 6: User Story 4 — Pricing and inventory schema validation (Priority: P2)

**Goal**: Invalid CEL expressions, bad inventory policy, inverted time windows, and impossible quantity ranges are caught — pre-receive for structural rules, admission for CEL syntax.

**Independent Test**: Push a `ProductVariant` with an invalid CEL expression → rejected at admission with the offending expression identified.

### Tests for User Story 4

> **Write these FIRST — verify they FAIL before T032**

- [X] T029 [US4] Write integration test: invalid CEL expression in `eligibility.constraints` → rejected at admission with expression identified in `tests/integration/product_variant_test.go`
- [X] T030 [P] [US4] Write integration tests: `validFromTime > validUntilTime` → pre-receive rejects; `quantity.min > quantity.max` → pre-receive rejects in `tests/integration/product_variant_test.go`
- [X] T031 [P] [US4] Write integration test: valid `priceSet` → `status.resolved.priceSet` populated with correct `priceCount`, `currencies`, `strategies` in `tests/integration/product_variant_test.go`

### Implementation for User Story 4

- [X] T032 [US4] Extend `validateProductVariantSpec` with pricing pre-receive rules: `inventory.policy` enum check, `strategy.type` recognised value check, `validFromTime ≤ validUntilTime` guard, `quantity.min ≤ quantity.max` guard in `gitstore-api/internal/validate/validator.go` (depends on T015)
- [X] T033 [US4] Extend `admitProductVariant` with CEL syntax validation loop (using `cel.NewEnv().Parse(expr)` for each `eligibility.constraints[*].expression`); write `PricingAccepted` condition in `gitstore-api/internal/cataloggrpc/server.go` (depends on T001, T027)
- [X] T034 [US4] Compute and populate `status.resolved.priceSet` summary (`hash`, `compiledExpressions`, `priceCount`, `currencies`, `strategies`) and `status.resolved.inventory` in `admitProductVariant` in `gitstore-api/internal/cataloggrpc/server.go` (depends on T033)

**Checkpoint**: All pricing/inventory validation working. T029–T031 must pass.

---

## Phase 7: User Story 5 — Update a ProductVariant (Priority: P2)

**Goal**: Re-pushing a modified `ProductVariant` re-validates and updates `status.resolved`.

**Independent Test**: Push a `ProductVariant`, then push an update adding a new price rule → `status.resolved.priceSet.priceCount` increases by one.

### Tests for User Story 5

> **Write these FIRST — verify they FAIL before T037**

- [X] T035 [US5] Write integration test: update `spec.pricing` → `status.resolved.priceSet` reflects new rules in `tests/integration/product_variant_test.go`
- [X] T036 [P] [US5] Write integration test: update `spec.selectedOptions` with invalid option → push rejected, stored variant unchanged in `tests/integration/product_variant_test.go`

### Implementation for User Story 5

- [X] T037 [US5] Ensure `admitProductVariant` handles the update path: detect existing resource via `GetProductVariantByName`, call `UpdateProductVariant` instead of `Create`, increment `Generation`, preserve `CreationTimestamp` and `UID`, re-run all admission checks on updated spec in `gitstore-api/internal/cataloggrpc/server.go` (depends on T034; note: `admitProduct` update path is the existing reference implementation)

**Checkpoint**: Full lifecycle (create + update) working. T035–T036 must pass.

---

## Phase 8: Polish & Cross-Cutting Concerns

- [X] T038 [P] Add structured `zap` logging for: pre-receive `validateProductVariantSpec` rejection, `admitProductVariant` SKU conflict, `productRef` deferred resolution, CEL syntax error, and successful admission in `gitstore-api/internal/cataloggrpc/server.go` and `gitstore-api/internal/graph/product_variant.resolvers.go`
- [X] T039 [P] Update `docs/` with the single-pass catalog authoring advantage: pushing an entire catalog (products + variants + categories) in one commit vs multiple sequential Admin UI clicks — document this as a key differentiator over traditional e-commerce platforms
- [X] T040 Verify all `quickstart.md` query examples execute correctly against a running local stack (`make dev`); update examples if schema or field names changed during implementation

---

## Dependencies & Execution Order

### Phase Dependencies

- **Setup (Phase 1)**: No dependencies — start immediately
- **Foundational (Phase 2)**: Depends on Phase 1 (T001) — BLOCKS all user stories
- **US1 (Phase 3)**: Depends on Phase 2 complete
- **US2 (Phase 4)**: Depends on Phase 2 complete; can run in parallel with US1 after T010, T017
- **US3 (Phase 5)**: Depends on US1 complete (T016 must exist to extend)
- **US4 (Phase 6)**: Depends on US1 (T016) for admission base; can begin after T015 for pre-receive rules
- **US5 (Phase 7)**: Depends on US1 + US3 + US4 complete (full admission path must exist)
- **Polish (Phase 8)**: Can begin any time; T040 requires all user stories complete

### Within Each User Story

1. Write test tasks → verify they FAIL
2. Implement model/entity changes
3. Implement service/admission changes
4. Implement resolver/query changes
5. Verify tests PASS — story complete

### Parallel Opportunities

- T003, T004, T005, T009 — all different files, no inter-dependency → run in parallel
- T006, T007 both depend on T005 but touch different files → run in parallel after T005
- T012, T013 — different test cases → write in parallel
- T019, T020 — different files → implement in parallel after T010
- T024, T025, T026 — different test cases → write in parallel
- T029, T030, T031 — different test cases → write in parallel
- T035, T036 — different test cases → write in parallel
- T038, T039 — different files → run in parallel

---

## Parallel Example: Foundational Phase (Phase 2)

```
# After T002 completes, these can run concurrently:
T003: catalog/product_variant.go       (structs)
T004: catalog/status.go                (conditions)
T005: datastore/entities.go            (entity)
T009: shared/schemas/                  (GraphQL schema)

# After T005 completes, these can run concurrently:
T006: datastore/datastore.go           (interface)
T007: datastore/memdb/schema.go        (table)

# After T006 + T007:
T008: datastore/memdb/backend.go       (implementation)

# After T009:
T010: go generate                      (codegen)

# After T002 + T008 + T010:
T011: cataloggrpc/server.go            (push context refactor)
```

---

## Implementation Strategy

### MVP First (US1 + US2 — the P1 stories)

1. Complete Phase 1: Setup (T001)
2. Complete Phase 2: Foundational (T002–T011)
3. Complete Phase 3: US1 — push + admit (T012–T017)
4. Complete Phase 4: US2 — query (T018–T023)
5. **STOP and VALIDATE**: push a variant, query it back — full round-trip working
6. Continue with US3 (option validation) → US4 (pricing) → US5 (updates)

### Incremental Delivery

1. Setup + Foundational → infrastructure ready
2. US1 → variants can be pushed and persisted (MVP!)
3. US2 → variants can be queried (storefront-ready)
4. US3 → option compatibility enforced (data integrity)
5. US4 → pricing/inventory validation (revenue safety)
6. US5 → lifecycle updates (operational completeness)

---

## Notes

- `[P]` tasks touch different files with no dependency on incomplete sibling tasks
- `[Story]` label maps each task to its user story for traceability
- The push context refactor (T011) is the most impactful foundational task — budget time for updating `admitProduct`, `admitCollection`, and `admitCategoryTaxonomyWithContext` call sites
- `go generate ./...` (T010) must re-run after any `.graphqls` schema change
- CEL validation (T033) uses `cel.NewEnv()` + `env.Parse(expr)` only — no variable declarations, no program build, no evaluation
- All integration tests target the memdb backend by default; ScyllaDB backend coverage is handled by CI via `GITSTORE_DATASTORE__BACKEND=scylladb`
