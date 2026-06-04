---
description: "Task list for Product Resource Contract — Kubernetes-style Frontmatter Schema"
---

# Tasks: Product Resource Contract — Kubernetes-style Frontmatter Schema

**Input**: Design documents from `specs/014-product-frontmatter/`  
**Prerequisites**: plan.md ✅, spec.md ✅, research.md ✅, data-model.md ✅, contracts/ ✅  
**Tests**: Test-First Development (Constitution Principle I — NON-NEGOTIABLE). Tests MUST be written before implementation.  
**Organization**: Tasks grouped by user story to enable independent implementation and testing.

## Format: `[ID] [P?] [Story] Description`

- **[P]**: Can run in parallel (different files, no unmet dependencies)
- **[Story]**: Which user story ([US1], [US2], [US3])
- Exact file paths included in every description

---

## Phase 1: Setup (Package Scaffolding)

**Purpose**: Create the `catalog` package directory and ensure the test fixture uses K8s-style frontmatter before any user story work begins.

- [X] T001 Create gitstore-api/internal/catalog/ package directory with an empty Go source file declaring `package catalog`
- [X] T002 Update gitstore-api/internal/validate/testdata/macbook-pro-64gb-1tb-ssd-m4.md to use full Kubernetes-style frontmatter (apiVersion, kind, metadata with name/namespace/labels/annotations, spec with title/categoryRef/tags/options/media) per the example in quickstart.md

**Checkpoint**: Package scaffold exists; fixture file uses K8s-style frontmatter — US1/US2 tests can now be written.

---

## Phase 2: Foundational (Blocking Prerequisites)

**Purpose**: No shared infrastructure tasks beyond Phase 1 — the `catalog` package created in Phase 1 is the only prerequisite. All user story phases depend only on Phase 1 completion.

⚠️ `github.com/adrg/frontmatter v0.2.0` and `go-playground/validator/v10 v10.30.3` are already present in `gitstore-api/go.mod` (confirmed in research.md D-001). No dependency changes required.

**Checkpoint**: Phase 1 complete → all user story phases can begin.

---

## Phase 3: User Story 1 — Author a Product Catalogue File (Priority: P1) 🎯 MVP

**Goal**: A correctly structured product file can be parsed from YAML frontmatter with all top-level envelope fields (`apiVersion`, `kind`, `metadata`) extracted. Schema violations (wrong kind, missing name, legacy format, forbidden `status`, forbidden read-only metadata fields) are rejected with descriptive errors.

**Independent Test**: `cd gitstore-api && go test ./internal/catalog/... ./internal/validate/...` — the catalog struct-parsing tests and the acceptance/rejection validator tests all pass.

### Tests for User Story 1 ⚠️ Write first — these MUST FAIL before implementation

- [X] T003 [US1] Write failing tests in gitstore-api/internal/catalog/product_test.go: parse valid ProductResource from fixture, assert APIVersion/Kind/Metadata fields; assert parsing a document without `kind` returns an error; assert parsing with wrong kind returns a "kind must be Product" error; assert parsing with no `metadata.name` returns a "name is required" error
- [X] T004 [US1] Rewrite gitstore-api/internal/validate/validator_test.go: valid product accepted without error; wrong kind (`kind: Category`) rejected; missing `metadata.name` rejected; legacy frontmatter (no `apiVersion`) rejected with "migration is not supported in alpha" message; `status:` key in author file rejected; read-only metadata field (`uid`) in author file rejected

### Implementation for User Story 1

- [X] T005 [P] [US1] Implement `ProductResource` and `ObjectMeta` (author-writable fields only) in gitstore-api/internal/catalog/product.go using yaml struct tags and validate tags from contracts/go-types.md; include `ProductSpec` as a defined struct (fields may be incomplete stubs — completed in T010)
- [X] T006 [P] [US1] Implement production frontmatter validation in gitstore-api/internal/validate/validator.go: `Parse(r io.Reader) (*catalog.ProductResource, []byte, error)` using `frontmatter.Parse` + `go-playground/validator/v10`; enforce forbidden `status` key and forbidden read-only metadata keys (`uid`, `resourceVersion`, `generation`, `creationTimestamp`, `revision`, `ownerReferences`) as pre-parse checks on the raw YAML map before struct binding
- [X] T007 [P] [US1] Rewrite shared/schemas/product.graphqls with the full Kubernetes-style schema from contracts/graphql-schema.md: `Product`, `ProductObjectMeta`, `ProductSpec`, `ProductStatus`, `ProductCondition`, `ProductConditionType` enum, `ConditionStatus` enum, `CatalogObjectReference`, `OwnerReference`, `MediaDefinition`, `FileReference`, `ProductOptionDefinition`, `ResolvedProductDefinition`, `ResolvedCategoryDefinition`, `PriceRangeDefinition`, `VariantSummaryDefinition`, `ResolvedFileDefinition`, `ProductConnection`, `ProductEdge`; replace `product(by: ProductBy!)` query with `product(namespace: String!, name: String!)` and `products(namespace: String!, ...)` with pagination; remove all mutation types (`CreateProductInput`, `UpdateProductInput`, `DeleteProductInput`, `CreateProductPayload`, `UpdateProductPayload`, `DeleteProductPayload`, `OptimisticLockConflict`, `ProductBy`, `InventoryStatus`)

**Checkpoint**: `go test ./internal/catalog/... ./internal/validate/...` passes. Product envelope parsing and schema validation are complete. GraphQL schema contract is published.

---

## Phase 4: User Story 2 — Use ProductSpec to Declare Product Attributes (Priority: P1)

**Goal**: A product spec with all supported fields (`title`, `categoryRef`, `tags`, `media`, `options`) round-trips correctly — values written in are values read out. Optional fields default to empty/nil. Validation rejects missing `options.name` and duplicate option names.

**Independent Test**: `cd gitstore-api && go test ./internal/catalog/...` — ProductSpec round-trip tests all pass with no field data loss.

### Tests for User Story 2 ⚠️ Write first — these MUST FAIL before implementation

- [X] T008 [US2] Extend gitstore-api/internal/catalog/product_test.go: ProductSpec round-trip test using the updated fixture — assert all fields populated (title, categoryRef.kind/name, tags slice, media[0].fileRef.name/kind/optional, options[0..n].name/title/values); assert spec with only `title` and `categoryRef` parses without error with nil/empty for omitted fields
- [X] T009 [US2] Extend gitstore-api/internal/validate/validator_test.go: options entry missing `name` field returns "spec.options[N].name is required"; duplicate option names returns "spec.options contains duplicate name 'X'"; `metadata.labels` key exceeding 63 chars per segment returns label key length error

### Implementation for User Story 2

- [X] T010 [P] [US2] Complete `ProductSpec`, `ObjectReference`, `MediaDefinition`, `FileReference`, and `ProductOptionDefinition` in gitstore-api/internal/catalog/product.go using yaml struct tags and validate tags from contracts/go-types.md
- [X] T011 [US2] Extend gitstore-api/internal/validate/validator.go with spec-level validation: `options[].name` required (index in error message); `options[].name` unique within the list (duplicate detection); `metadata.labels` key/value length following Kubernetes label conventions (63-char max per key segment, 253-char max with prefix, 63-char max value)

**Checkpoint**: `go test ./internal/catalog/... ./internal/validate/...` passes. All ProductSpec fields round-trip and spec-level validation is enforced.

---

## Phase 5: User Story 3 — Read System-Populated ProductStatus (Priority: P2)

**Goal**: A `ProductStatus` object with all documented fields (`observedGeneration`, `lastAppliedRevision`, `conditions`, `resolved`) serializes and deserializes without data loss. All six condition types are representable. ScyllaDB and memdb schemas are updated to the new `(namespace, name)` primary key convention.

**Independent Test**: `cd gitstore-api && go test ./internal/catalog/... ./internal/datastore/...` — ProductStatus serialization tests pass; memdb schema tests use the new compound index.

### Tests for User Story 3 ⚠️ Write first — these MUST FAIL before implementation

- [X] T012 [US3] Extend gitstore-api/internal/catalog/product_test.go: ProductStatus JSON round-trip test — marshal a fully-populated ProductStatus (all 6 condition types, complete resolved block with category/priceRange/totalInventory/variantSummary/defaultVariantRef/media), unmarshal it back, assert no field data loss; assert a ProductStatus with empty conditions parses without error; assert a Condition with `status` outside `True|False|Unknown` fails validate.Struct
- [X] T013 [US3] Extend gitstore-api/internal/datastore/memdb/backend_test.go: product insert uses struct with `UID`, `Name`, and `Namespace` fields; product lookup by `(namespace, name)` compound index succeeds; product lookup by `UID` succeeds; lookup by old `SKU` index does not compile (index removed)

### Implementation for User Story 3

- [X] T014 [P] [US3] Implement `ProductStatus`, `Condition`, `ConditionType`, `ConditionStatus` constants, `ResolvedProductDefinition`, `ResolvedCategoryDefinition`, `PriceRangeDefinition`, `VariantSummaryDefinition`, `ResolvedFileDefinition`, `SystemObjectMeta`, and `OwnerReference` in gitstore-api/internal/catalog/status.go using json struct tags and validate tags from contracts/go-types.md
- [X] T015 [P] [US3] Rewrite the `product` table in gitstore-api/internal/datastore/memdb/schema.go: replace the existing `id`/`sku`/`category_id` indexes with `id` (UUIDFieldIndex on `UID`), `name_namespace` (CompoundIndex on `Namespace`+`Name`, unique), and `namespace` (StringFieldIndex on `Namespace`, non-unique) per data-model.md memdb schema
- [X] T016 [US3] Rewrite gitstore-api/internal/datastore/scylla/migrations/001_initial_schema.cql: replace the existing `products` table with the new schema (`namespace` partition key + `name` clustering key, `uid`, `api_version`, `kind`, `generation`, `resource_version`, `creation_timestamp`, `revision`, `labels`, `annotations`, `owner_refs`, `git_commit_sha`, `git_ref`, `spec`, `body`, `status` columns, secondary index `products_by_uid`) per data-model.md ScyllaDB schema; remove legacy columns (`bucket`, `sku`, `title`, `price`, `currency`, `inventory_status`, `inventory_quantity`, `category_id`, `collection_ids`, `images`, `metadata`)

**Checkpoint**: `go test ./internal/catalog/... ./internal/datastore/...` passes. Status types are serializable, memdb uses the new schema, ScyllaDB CQL is rewritten.

---

## Phase 6: Polish & Cross-Cutting Concerns

**Purpose**: Validate the full implementation, ensure tests compile clean, and update documentation.

- [X] T017 Run `cd gitstore-api && go test ./internal/catalog/... ./internal/validate/... ./internal/datastore/...` and verify all tests pass with no race conditions (`-race` flag)
- [X] T018 [P] Run `cd gitstore-api && go build ./...` to verify the GraphQL schema rewrite in shared/schemas/product.graphqls does not break the gqlgen-generated code (resolve any type mismatches in the graph/ package)
- [X] T019 [P] Update docs/ with product frontmatter schema documentation: canonical worked example from quickstart.md, validation rule table from data-model.md, and state transition diagram

---

## Dependencies & Execution Order

### Phase Dependencies

- **Setup (Phase 1)**: No dependencies — start immediately
- **Foundational (Phase 2)**: Effectively Phase 1 only — no separate Phase 2 tasks
- **US1 (Phase 3)**: Depends on Phase 1 (catalog directory + fixture update)
- **US2 (Phase 4)**: Depends on Phase 3 completion (ProductResource + ObjectMeta must exist before ProductSpec can be fully filled in T010)
- **US3 (Phase 5)**: Can start after Phase 1; T014 is independent of US1/US2; T015/T016 are independent datastore changes
- **Polish (Phase 6)**: Depends on all previous phases

### User Story Dependencies

- **US1 (P1)**: Starts after Phase 1 — envelope parsing and top-level validation; no dependency on US2/US3
- **US2 (P1)**: Starts after US1 (T005 must define `ProductSpec` stub before T010 fills it in) — spec content and spec-level validation
- **US3 (P2)**: T014/T015/T016 can start after Phase 1 in parallel with US1/US2; T012/T013 can be written after Phase 1 independently

### Within Each User Story

- Test tasks MUST be written and FAIL before corresponding implementation tasks
- catalog/product.go changes are sequential (T005 before T010)
- validate/validator.go changes are sequential (T006 before T011)
- T015 (memdb schema) and T016 (ScyllaDB CQL) are independent files — can run in parallel [P marked]

### Parallel Opportunities

- T005, T006, T007 can run in parallel (different files, no cross-dependencies after T003/T004 tests are written)
- T010 and T011 can run in parallel (different files)
- T014, T015 can run in parallel (different files)
- T017, T018, T019 can run in parallel

---

## Parallel Example: User Story 1

```bash
# After T003 and T004 are written (and confirmed failing):

# These three tasks touch different files — launch in parallel:
Task T005: "Implement ProductResource and ObjectMeta in gitstore-api/internal/catalog/product.go"
Task T006: "Implement production frontmatter validation in gitstore-api/internal/validate/validator.go"
Task T007: "Rewrite shared/schemas/product.graphqls"
```

## Parallel Example: User Story 3

```bash
# After T012 and T013 are written (and confirmed failing):

# These two tasks touch different files — launch in parallel:
Task T014: "Implement ProductStatus types in gitstore-api/internal/catalog/status.go"
Task T015: "Rewrite product table in gitstore-api/internal/datastore/memdb/schema.go"
# T016 (CQL rewrite) can also run in parallel with T014 and T015
```

---

## Implementation Strategy

### MVP First (User Story 1 Only)

1. Complete Phase 1: Setup (T001, T002)
2. Write US1 tests: T003, T004 (confirm they fail)
3. Implement US1: T005, T006, T007
4. **STOP and VALIDATE**: `go test ./internal/catalog/... ./internal/validate/...`
5. Deploy/demo if GraphQL schema is ready

### Incremental Delivery

1. Setup (T001–T002) → Foundation ready
2. US1 (T003–T007) → Envelope parsing + validation + GraphQL contract → **MVP!**
3. US2 (T008–T011) → Full ProductSpec round-trip and spec validation
4. US3 (T012–T016) → ProductStatus types + datastore schema rewrite
5. Polish (T017–T019) → Clean build, docs updated

### Parallel Team Strategy

With two developers after Phase 1:

- Developer A: US1 (T003 → T004 → T005/T006/T007)
- Developer B: US3 setup tasks (T012 → T013 → T014/T015/T016) — can start independently

---

## Notes

- No Rust changes in this feature — the `AdmissionHandler` trait from feature #013 is the integration point; wiring the concrete callout is GH#105/106
- The GraphQL schema rewrite (T007) removes types — check the graph/ package for compile errors (T018)
- `shared/schemas/product.graphqls` uses `Decimal` and `DateTime` scalars defined in `shared/schemas/schema.graphqls` — do not redefine
- `OwnerReference` in the GraphQL schema is product-scoped for now (can be promoted to a shared type in a future feature)
- `PriceRangeDefinition.Min`/`Max` use `decimal.Decimal` (`shopspring/decimal v1.4.0`), matching the existing `Decimal` GraphQL scalar binding in `scalar/scalars.go` and `models_gen.go` — NOT `string`
- ScyllaDB CQL rewrite (T016) is safe for alpha — no production data to preserve (research.md D-003)
- [P] tasks = operate on different files, no unmet dependencies at execution time
- [Story] label maps each task to a specific user story for traceability
