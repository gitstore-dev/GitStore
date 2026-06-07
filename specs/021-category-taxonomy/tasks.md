# Tasks: CategoryTaxonomy Frontmatter and Hierarchy Enforcement

**Input**: Design documents from `specs/021-category-taxonomy/`
**Branch**: `021-category-taxonomy`
**Prerequisites**: plan.md ✓, spec.md ✓, research.md ✓, data-model.md ✓, contracts/ ✓

**Tests**: Test-First Development (Constitution Principle I — NON-NEGOTIABLE). Tests MUST be written before implementation and must fail before implementation begins.

**Organization**: Tasks grouped by user story. Each story is independently implementable and testable.

## Format: `[ID] [P?] [Story] Description`

- **[P]**: Can run in parallel (different files, no shared dependencies within a phase)
- **[Story]**: Which user story this task belongs to (US1–US4)

---

## Phase 1: Setup — Replace Legacy Category

**Purpose**: Replace the legacy `Category` entity in-place everywhere it exists. Alpha software — no migration path, no backwards compatibility. All category storage is now `CategoryTaxonomy`-shaped.

> **Warning**: This phase modifies the `Datastore` interface and all its implementations atomically. All callers (service.go, converters.go, pagination.go, resolvers) must compile by end of phase. Use `go build ./...` as a gate.

- [X] T001 Replace `Category` struct in `gitstore-api/internal/datastore/entities.go` with `CategoryTaxonomy` struct having fields: `UID string`, `Namespace string`, `Name string`, `APIVersion string`, `Kind string`, `Generation int64`, `ResourceVersion string`, `CreationTimestamp time.Time`, `Revision string`, `Labels map[string]string`, `Annotations map[string]string`, `ParentName string`, `AncestorPath string`, `GitCommitSHA string`, `GitRef string`, `Spec json.RawMessage`, `Body string`, `Status json.RawMessage`. Delete the old `Category` struct entirely.

- [X] T002 Replace all `Category` operations in `gitstore-api/internal/datastore/datastore.go` interface with `CategoryTaxonomy` operations: `CreateCategoryTaxonomy(ctx, *CategoryTaxonomy) error`, `GetCategoryTaxonomyByName(ctx, namespace, name string) (*CategoryTaxonomy, error)`, `ListCategoryTaxonomies(ctx, namespace string, page PageParams) (*PageResult[CategoryTaxonomy], error)`, `UpdateCategoryTaxonomy(ctx, *CategoryTaxonomy) error`. Remove `GetCategoryBySlug`, `GetCategory(id)`, `DeleteCategory` — these have no equivalent in the Kubernetes-style model.

- [X] T003 Replace the `category` memdb table in `gitstore-api/internal/datastore/memdb/schema.go` with a `category_taxonomy` table with indexes: `id` (UUIDFieldIndex on `UID`), `name_namespace` (CompoundIndex on `Namespace`+`Name`, unique), `namespace` (StringFieldIndex on `Namespace`), `parent_name` (StringFieldIndex on `ParentName`), `ancestor_path` (StringFieldIndex on `AncestorPath`).

- [X] T004 Replace all `Category` CRUD methods in `gitstore-api/internal/datastore/memdb/backend.go` with `CategoryTaxonomy` equivalents. `CreateCategoryTaxonomy` inserts into `category_taxonomy` table keyed on `UID` and `Namespace+Name` (unique). `GetCategoryTaxonomyByName` looks up by `name_namespace` compound index. `ListCategoryTaxonomies` filters by `namespace` index and paginates. `UpdateCategoryTaxonomy` replaces by `name_namespace`. Delete old `CreateCategory`, `GetCategory`, `GetCategoryBySlug`, `ListCategories`, `UpdateCategory`, `DeleteCategory` methods entirely.

- [X] T005 Replace the ScyllaDB `categories` table in `gitstore-api/internal/datastore/scylla/migrations/001_initial_schema.cql` in-place with a `category_taxonomy` table having columns: `namespace text`, `name text`, `uid uuid`, `api_version text`, `kind text`, `generation bigint`, `resource_version text`, `creation_ts timestamp`, `revision text`, `labels map<text,text>`, `annotations map<text,text>`, `parent_name text`, `ancestor_path text`, `git_commit_sha text`, `git_ref text`, `spec text`, `body text`, `status text`, `PRIMARY KEY (namespace, name)`. Add secondary index on `parent_name`. Drop the old `categories` DDL.

- [X] T006 Replace the `Category` table model in `gitstore-api/internal/datastore/scylla/models.go` with a `CategoryTaxonomy` table model (`category_taxonomy`) with columns matching the new CQL schema. Remove `categoryRow` struct. Add `categoryTaxonomyRow` struct with `db:` tags. Delete old `Category` table var.

- [X] T007 Replace all `Category` CRUD methods in `gitstore-api/internal/datastore/scylla/backend.go` with `CategoryTaxonomy` equivalents. `CreateCategoryTaxonomy` inserts into `category_taxonomy`. `GetCategoryTaxonomyByName` queries by `(namespace, name)` primary key. `ListCategoryTaxonomies` queries by namespace partition. `UpdateCategoryTaxonomy` does a conditional upsert. Remove `toCategoryRow`, `fromCategoryRow`, all old Category methods, and the `categoryTable` field from `scyllaDatastore`. Update `scyllaDatastore` struct and constructor to use `categoryTaxonomyTable *table.Table` instead.

- [X] T008 Replace `InstrumentedDatastore` `Category` methods in `gitstore-api/internal/datastore/instrumented.go` with `CategoryTaxonomy` equivalents: `CreateCategoryTaxonomy`, `GetCategoryTaxonomyByName`, `ListCategoryTaxonomies`, `UpdateCategoryTaxonomy`. Remove the old six `Category` passthrough methods.

- [X] T009 Update `gitstore-api/internal/graph/service.go`: replace `GetCategories`, `GetCategoryByID`, `GetCategoryBySlug`, `CreateCategory`, `UpdateCategory`, `DeleteCategory` service methods with `GetCategoryTaxonomies(ctx, namespace string, params PageParams) (*datastore.PageResult[datastore.CategoryTaxonomy], error)` and `GetCategoryTaxonomyByName(ctx, namespace, name string) (*datastore.CategoryTaxonomy, error)`. The create/update/delete service methods are dropped (git is the write path). This will break `category.resolvers.go` and `converters.go` — those are fixed in subsequent tasks.

- [X] T010 Update `gitstore-api/internal/graph/converters.go`: replace `DatastoreCategoryToGraphQL(c *datastore.Category)` with `DatastoreCategoryTaxonomyToGraphQL(c *datastore.CategoryTaxonomy) *model.Category`. Map: `UID→ID` (encode as `cat_` node ID), `Name→Name`, `Spec→title` (unmarshal spec JSON for `title`), `Body→Body`, `AncestorPath→Path` (split by `/`), `AncestorPath depth→Depth`. Labels map to `[]*model.KeyValuePair`. Set `Children: []*model.Category{}`, `Products: empty connection`, `CreatedAt/UpdatedAt` from `CreationTimestamp`.

- [X] T011 Update `gitstore-api/internal/graph/pagination.go`: replace `BuildCategoryConnection(*datastore.PageResult[datastore.Category])` with `BuildCategoryConnection(*datastore.PageResult[datastore.CategoryTaxonomy]) *model.CategoryConnection`. Cursor key function uses `c.CreationTimestamp` and `c.UID`.

- [X] T012 Update `gitstore-api/internal/graph/category.resolvers.go`: replace `CreateCategory`, `UpdateCategory`, `DeleteCategory`, `ReorderCategories` resolver implementations with stubs returning `nil, errors.New("category mutations are managed via git push")`. Update `Category` query resolver to call `r.service.GetCategoryTaxonomyByName`. Update `Categories` resolver to call `r.service.GetCategoryTaxonomies`. Fix `categoryResolver.Products` to use UID from `CategoryTaxonomy`.

- [X] T013 [P] Update `gitstore-api/internal/graph/query_helpers.go`: the `Node` resolver for `nodeKindCategory` currently calls `GetCategoryByID` — replace with a lookup by UID via `GetCategoryTaxonomyByName` (will require searching by UID; add a `GetCategoryTaxonomyByUID` method if needed, or accept that global node ID lookup for categories returns `nil` until a UID index is available — document the limitation).

- [X] T014 Verify `go build ./...` in `gitstore-api/` compiles with zero errors. All legacy `Category` references must be gone. Run `grep -rn "datastore\.Category[^T]" ./internal --include="*.go"` and confirm zero matches.

**Checkpoint**: Legacy `Category` entity fully replaced. `go build ./...` passes. All category queries now route through `CategoryTaxonomy`.

---

## Phase 2: Foundational — Catalog Go Types and Validator Kind-Routing

**Purpose**: Define the `CategoryTaxonomyResource` Go struct, extend the `validate` package for multi-kind routing, and add the `CategoryTaxonomyStatus` types. This is required by all user story phases.

- [X] T015 [P] Create `gitstore-api/internal/catalog/category.go` with `CategoryTaxonomyResource`, `CategoryTaxonomySpec` structs. Reuse `ObjectMeta`, `ObjectReference`, `MediaDefinition` from `product.go` — do not copy them. `CategoryTaxonomyResource`: fields `APIVersion string yaml:"apiVersion" validate:"required,eq=catalog.gitstore.dev/v1beta1"`, `Kind string yaml:"kind" validate:"required,eq=CategoryTaxonomy"`, `Metadata ObjectMeta yaml:"metadata" validate:"required"`, `Spec CategoryTaxonomySpec yaml:"spec"`. `CategoryTaxonomySpec`: `Title string yaml:"title" validate:"required"`, `ParentRef *ObjectReference yaml:"parentRef"`, `Media []MediaDefinition yaml:"media" validate:"omitempty,dive"`.

- [X] T016 [P] Extend `gitstore-api/internal/catalog/status.go` with `CategoryTaxonomy`-specific condition types and status struct. Add constants: `ConditionParentResolved ConditionType = "ParentResolved"`, `ConditionAcyclic ConditionType = "Acyclic"`. Add `CategoryTaxonomyStatus struct` with fields `ObservedGeneration int64`, `LastAppliedRevision string`, `Conditions []Condition`, `Resolved *ResolvedCategoryTaxonomy`. Add `ResolvedCategoryTaxonomy struct` with `Depth int8`, `AncestorPath string`, `Ancestors []ObjectReference`, `ChildCount int64`, `ProductCount int64`. Update the `Condition.Type` validator tag to include the new condition types: `oneof=Published AdmissionAccepted CategoryResolved OptionsAccepted VariantsResolved Ready ParentResolved Acyclic`.

- [X] T017 Extend `gitstore-api/internal/validate/validator.go` with `ParseResource(r io.Reader) (*ParsedResource, []byte, error)` that dispatches on `kind`. Define `ParsedResource struct` with `Kind string`, `Product *catalog.ProductResource`, `CategoryTaxonomy *catalog.CategoryTaxonomyResource`. The implementation: call `preParseChecks` (shared), extract `kind` from the raw YAML map, bind into the appropriate struct, run per-kind struct-tag validation and spec-level validation. For unrecognized `kind`: return a `ValidationError`-style error with message `"kind %q is not a recognized catalog resource type"`. For `kind: CategoryTaxonomy`: run `validateCategorySpec(spec CategoryTaxonomySpec) error` (validate `title` required, validate `parentRef.name != metadata.name` for self-reference). The existing `Parse(r io.Reader)` function remains unchanged for backward compatibility with existing callers.

- [X] T018 Write unit tests for `ParseResource` in `gitstore-api/internal/validate/validator_test.go` BEFORE implementing `ParseResource`. Tests must cover: (a) `kind: CategoryTaxonomy` with all required fields → parses successfully; (b) missing `spec.title` → validation error; (c) missing `metadata.name` → validation error; (d) `kind: UnknownKind` → kind-not-recognized error; (e) `spec.parentRef.name == metadata.name` → self-reference rejection; (f) `kind: Product` still parses correctly via `ParseResource` (regression).

  > **TDD gate**: Run `go test ./internal/validate/...` and confirm all T018 tests fail before implementing T017.

**Checkpoint**: `ParseResource` routes by kind, validates `CategoryTaxonomy` schema, rejects self-parenting and unknown kinds. `go test ./internal/validate/...` passes.

---

## Phase 3: User Story 1 — Publish a CategoryTaxonomy Document (Priority: P1) 🎯 MVP

**Goal**: A valid `CategoryTaxonomy` document pushed to a repository is accepted, stored as a `CategoryTaxonomy` datastore entity, and becomes queryable via GraphQL.

**Independent Test**: Push a single `CategoryTaxonomy` file to a test repository; verify it is accepted and `category(by: {name: "electronics"})` returns it with `name`, `title`, and `body`.

### Tests for User Story 1 ⚠️ Write first, verify they FAIL

- [X] T019 [P] [US1] Write `ValidateResources` unit tests for `CategoryTaxonomy` schema validation in `gitstore-api/internal/cataloggrpc/server_test.go`. Tests: (a) valid `CategoryTaxonomy` blob → `accepted: true`; (b) missing `spec.title` → `accepted: false` with error on `spec.title`; (c) missing `metadata.name` → `accepted: false`; (d) `kind: CategoryTaxonomy` with `status` field set → `accepted: false`; (e) `kind: CategoryTaxonomy` with read-only metadata field → `accepted: false`; (f) unknown kind → `accepted: false` with kind-not-recognized message; (g) Product blob + CategoryTaxonomy blob in same request → validates both independently.

- [X] T020 [P] [US1] Write `AdmitResources` unit tests for `CategoryTaxonomy` admission in `gitstore-api/internal/cataloggrpc/server_test.go`. Tests: (a) single valid `CategoryTaxonomy` → `CreateCategoryTaxonomy` called with correct fields; (b) existing `CategoryTaxonomy` → `UpdateCategoryTaxonomy` called (generation incremented); (c) `AdmissionAccepted=True` condition written to status; (d) root category (no parentRef) → `AncestorPath == metadata.name`, `ParentName == ""`; (e) child category with existing parent in DB → `AncestorPath == parent.AncestorPath + "/" + name`, `ParentResolved=True`.

- [X] T021 [P] [US1] Write memdb backend unit tests for `CategoryTaxonomy` CRUD in `gitstore-api/internal/datastore/memdb/backend_test.go` (create new file or extend). Tests: (a) `CreateCategoryTaxonomy` stores and `GetCategoryTaxonomyByName` retrieves; (b) duplicate name+namespace → `ErrAlreadyExists`; (c) `ListCategoryTaxonomies` paginates and filters by namespace; (d) `UpdateCategoryTaxonomy` replaces fields; (e) `GetCategoryTaxonomyByName` not found → `ErrNotFound`.

  > **TDD gate**: Run `go test ./internal/cataloggrpc/... ./internal/datastore/memdb/...` and confirm T019–T021 tests fail.

### Implementation for User Story 1

- [X] T022 [US1] Extend `gitstore-api/internal/cataloggrpc/server.go` `ValidateResources` to call `validate.ParseResource` instead of `validate.Parse`. Route: `kind == "CategoryTaxonomy"` → validate against `CategoryTaxonomyResource` struct; `kind == "Product"` → existing path; unknown kind → emit `ValidationError` with constraint `"kind"` and message `"kind %q is not a recognized catalog resource type"`. No DB lookups. Structured log for each kind routed.

- [X] T023 [US1] Extend `gitstore-api/internal/cataloggrpc/server.go` `AdmitResources` to handle `kind: CategoryTaxonomy` blobs. For each `CategoryTaxonomy` blob: unmarshal spec, look up parent by name in DB (if `parentRef` set), compute `AncestorPath` (`name` if root; `parent.AncestorPath + "/" + name` if parent found), set `ParentResolved` condition (`True` if parent found in DB or same push; `False` if parent not found). Build initial `CategoryTaxonomyStatus` with `AdmissionAccepted=True`, `ParentResolved=True/False`. Call `CreateCategoryTaxonomy` or `UpdateCategoryTaxonomy`. Log each category admitted with fields `kind`, `namespace`, `name`, `ancestor_path`, `parent_resolved`.

- [X] T024 [US1] Update the GraphQL `shared/schemas/category.graphqls` with additive changes from `specs/021-category-taxonomy/contracts/category.graphqls.diff`: add `apiVersion`, `kind`, `title`, `labels`, `categoryStatus` fields to `Category` type; add `CategoryTaxonomyStatus`, `CategoryCondition`, `ResolvedCategoryTaxonomy`, `KeyValuePair` types; add `name: String` to `CategoryBy` input. Do not remove any existing fields.

- [X] T025 [US1] Run `go generate ./...` (or `gqlgen generate`) in `gitstore-api/` to regenerate `internal/graph/generated/` and `internal/graph/model/models_gen.go` from the updated schema. Commit the generated files.

- [X] T026 [US1] Update `gitstore-api/internal/graph/converters.go` `DatastoreCategoryTaxonomyToGraphQL` to populate the new GraphQL fields: `APIVersion`, `Kind`, `Title` (from spec JSON), `Labels` (as `[]*model.KeyValuePair`), `CategoryStatus` (from status JSON). Update `BuildCategoryConnection` in `gitstore-api/internal/graph/pagination.go` if signature changed by T011.

- [X] T027 [US1] Update `gitstore-api/internal/graph/category.resolvers.go` `Category` query resolver to support `by: {name: "..."}` using `GetCategoryTaxonomyByName`. Update `Categories` resolver to pass `namespace` (from auth context or request) to `GetCategoryTaxonomies`. Compile and verify.

**Checkpoint**: Push a valid `CategoryTaxonomy` document, query it via GraphQL, get `name`, `title`, `body`, `apiVersion`, `kind` back. `go test ./internal/validate/... ./internal/cataloggrpc/... ./internal/datastore/memdb/...` all pass.

---

## Phase 4: User Story 2 — Build a Parent-Child Category Hierarchy (Priority: P1)

**Goal**: A child `CategoryTaxonomy` with `spec.parentRef` links to an existing parent; `AncestorPath` is computed correctly; self-parenting is rejected at push time; intra-push mutual cycles are detected at admission time.

**Independent Test**: Push parent `electronics`, then push child `computers` with `parentRef.name: electronics`. Query `computers`; verify `path` is `["electronics", "computers"]` and `depth` is `1`.

### Tests for User Story 2 ⚠️ Write first, verify they FAIL

- [X] T028 [P] [US2] Write hierarchy validation unit tests in `gitstore-api/internal/validate/validator_test.go`. Tests: (a) `parentRef.name == metadata.name` → `ParseResource` returns self-reference error; (b) valid `parentRef` with different name → `ParseResource` accepts.

- [X] T029 [P] [US2] Write intra-push cycle detection unit tests in `gitstore-api/internal/cataloggrpc/server_test.go`. Tests: (a) push with A→parentRef=B and B→parentRef=A → both stored with `Acyclic=False`; (b) push with A→parentRef=B and B→no parentRef (valid chain) → both stored with `Acyclic=True`; (c) push with A→parentRef=A (self) → caught pre-receive (T028 covers this, regression test here).

- [X] T030 [P] [US2] Write ancestor path computation unit tests in `gitstore-api/internal/cataloggrpc/server_test.go`. Tests: (a) root category → `AncestorPath = name`; (b) child with stored parent → `AncestorPath = parent.AncestorPath + "/" + name`; (c) child with parent in same push → `AncestorPath = parent.name + "/" + child.name` (parent is root); (d) child with no parent found in DB and not in push → `AncestorPath = name` (treated as tentative root), `ParentResolved=False`.

  > **TDD gate**: Run `go test ./internal/validate/... ./internal/cataloggrpc/...` and confirm T028–T030 tests fail.

### Implementation for User Story 2

- [X] T031 [US2] Add self-reference check to `gitstore-api/internal/validate/validator.go` `validateCategorySpec`: if `spec.ParentRef != nil && spec.ParentRef.Name == metadata.Name`, return error `"spec.parentRef.name must not reference the category itself"`. This check runs in `ValidateResources` (pre-receive) — no DB needed.

- [X] T032 [US2] Add intra-push cycle detection to `gitstore-api/internal/cataloggrpc/server.go` `AdmitResources`. Before processing individual categories: collect all `CategoryTaxonomy` blobs from the push into a `map[name]parentRefName`. Run DFS cycle detection on this in-memory graph. For any name involved in a detected cycle, mark it as `cycleDetected=true` and store with `Acyclic=False` condition. For names not in a cycle, store with `Acyclic=True`. Log detected cycles with field `cycle_detected=true` and the names involved.

- [X] T033 [US2] Extend `AdmitResources` parent-in-same-push resolution: before looking up the parent in the DB, check if the parent name is in the current push's `CategoryTaxonomy` blob set. If yes, use the in-push parent's `AncestorPath` (which is the parent's own name since it's a root-level admission). Set `ParentResolved=True` for in-push co-creation.

**Checkpoint**: Push `electronics` (root), then push `computers` (child). Query returns correct `path`, `depth`. Push a self-referencing category — rejected pre-receive. Push two mutually referencing categories — both stored with `Acyclic=False`.

---

## Phase 5: User Story 3 — Enforce Single-Category Product Constraint (Priority: P2)

**Goal**: A `Product` push referencing more than one `CategoryTaxonomy` is rejected pre-receive; products with zero or one category reference are accepted.

**Independent Test**: Push a product with `spec.categoryRef` set to a single valid name — accepted. Attempt to push a product with a YAML array under `categoryRef` — rejected with kind/field error.

### Tests for User Story 3 ⚠️ Write first, verify they FAIL

- [X] T034 [P] [US3] Write product-category constraint unit tests in `gitstore-api/internal/validate/validator_test.go`. Tests: (a) `Product` with `spec.categoryRef` as a single `ObjectReference` → accepted; (b) `Product` with no `spec.categoryRef` → accepted; (c) `Product` with `spec.categoryRef` as a YAML sequence/array → YAML unmarshal fails or `validate` returns type mismatch error (the constraint is structurally enforced by the `*ObjectReference` type).

- [X] T035 [P] [US3] Write `ValidateResources` product-category constraint test in `gitstore-api/internal/cataloggrpc/server_test.go`. Tests: (a) blob with `spec.categoryRef` as a single object → `accepted: true`; (b) blob with `spec.categoryRef` as a YAML sequence → `accepted: false` with field error on `spec.categoryRef`; (c) blob with `spec.categoryRef.name` empty (present but name missing) → `accepted: false` with `spec.categoryref.name is required`.

  > **TDD gate**: Run `go test ./internal/validate/... ./internal/cataloggrpc/...` and confirm T034–T035 tests fail.

### Implementation for User Story 3

- [X] T036 [US3] Verify that the existing `ProductSpec.CategoryRef *ObjectReference` type already structurally enforces the single-category constraint (cannot be a list). Add an explicit validator rule in `gitstore-api/internal/validate/validator.go` `validateSpec` for the case where `categoryRef` is present: if `spec.CategoryRef != nil && spec.CategoryRef.Name == ""`, return `"spec.categoryRef.name is required"`. This covers the "present but empty name" case not caught by struct tags.

**Checkpoint**: Products with one or no category reference are accepted. Products with a YAML-array `categoryRef` are rejected at pre-receive. `go test ./internal/validate/... ./internal/cataloggrpc/...` passes.

---

## Phase 6: User Story 4 — Attach Media to a Category (Priority: P3)

**Goal**: A `CategoryTaxonomy` with `spec.media` entries is stored with media references in spec JSON. Optional media with missing File resources is stored with an `unresolved optional media` note in status; required media with missing File resources is stored with a status condition (File check deferred to controller GH#244).

**Independent Test**: Push a `CategoryTaxonomy` with `spec.media[0].fileRef.name: category-hero` and `optional: true` referencing a non-existent file. Verify push is accepted and `categoryStatus` reflects the unresolved optional media.

### Tests for User Story 4 ⚠️ Write first, verify they FAIL

- [X] T037 [P] [US4] Write media validation unit tests in `gitstore-api/internal/validate/validator_test.go`. Tests: (a) valid media entry with `name` and `kind` set → accepted; (b) media entry with missing `fileRef.name` → `spec.media[0].fileref.name is required`; (c) media entry with missing `fileRef.kind` → `spec.media[0].fileref.kind is required`; (d) `optional: true` with missing file (File check is deferred — push-time validation only checks struct fields, not File existence).

- [X] T038 [P] [US4] Write `AdmitResources` media admission test in `gitstore-api/internal/cataloggrpc/server_test.go`. Tests: (a) `CategoryTaxonomy` with valid media entries → spec JSON contains media; (b) `optional: false` media → admitted, no push rejection (File check deferred to controller); (c) `optional: true` media → admitted.

  > **TDD gate**: Run `go test ./internal/validate/... ./internal/cataloggrpc/...` and confirm T037–T038 tests fail.

### Implementation for User Story 4

- [X] T039 [US4] Verify that `CategoryTaxonomySpec.Media []MediaDefinition yaml:"media" validate:"omitempty,dive"` and the existing `MediaDefinition`/`FileReference` structs already enforce the push-time rules (struct tags: `fileRef` required, `name` required, `kind` required). If `dive` validation of `FileReference` is not already exercised by `ParseResource`, add a `validateCategoryMedia` function in `gitstore-api/internal/validate/validator.go` mirroring the existing product media validation path.

- [X] T040 [US4] In `gitstore-api/internal/cataloggrpc/server.go` `AdmitResources`, ensure media spec is preserved in the stored `Spec` JSON blob (it will be, automatically, since the entire `CategoryTaxonomySpec` is marshalled). No additional code needed unless media-specific status conditions are required. Add a comment noting that File existence checks are deferred to GH#244.

**Checkpoint**: `CategoryTaxonomy` documents with media entries are accepted and stored with media in `Spec`. File existence is not checked at push time. `go test ./internal/validate/... ./internal/cataloggrpc/...` passes.

---

## Phase 7: E2E Integration Tests

**Purpose**: End-to-end tests that push actual documents via the live stack and assert push outcomes and GraphQL query results.

- [X] T041 Write E2E test `TestCategoryTaxonomyPublish` in `tests/integration/category_taxonomy_test.go`. Test: bootstrap repo → push valid root `CategoryTaxonomy` → assert push accepted → query `category(by: {name: "electronics"})` → assert `name`, `title`, `apiVersion == "catalog.gitstore.dev/v1beta1"`, `kind == "CategoryTaxonomy"`.

- [X] T042 [P] Write E2E test `TestCategoryTaxonomyHierarchy` in `tests/integration/category_taxonomy_test.go`. Test: push root `electronics` → push child `computers` with `parentRef.name: electronics` → query `computers` → assert `path == ["electronics","computers"]`, `depth == 1`.

- [X] T043 [P] Write E2E test `TestCategoryTaxonomySelfRefRejected` in `tests/integration/category_taxonomy_test.go`. Test: attempt to push `CategoryTaxonomy` with `parentRef.name == metadata.name` → assert push is rejected with a message containing `"must not reference the category itself"`.

- [X] T044 [P] Write E2E test `TestCategoryTaxonomyMissingFields` in `tests/integration/category_taxonomy_test.go`. Test: push `CategoryTaxonomy` missing `spec.title` → assert push rejected with `"spec.title is required"`.

- [X] T045 [P] Write E2E test `TestCategoryTaxonomyProductSingleRef` in `tests/integration/category_taxonomy_test.go`. Test: push root category → push product with one `spec.categoryRef` → accepted. Push product with `spec.categoryRef` as YAML array → rejected.

- [X] T046 Write E2E test `TestCategoryTaxonomyCoCreation` in `tests/integration/category_taxonomy_test.go`. Test: push parent `A` and child `B` (with `parentRef.name: A`) in the same commit. Assert both are admitted and `B.ancestorPath == "A/B"`.

**Checkpoint**: All E2E tests pass against the `memdb` stack (`make dev`). Run with `go test ./tests/e2e/... -run TestCategoryTaxonomy`.

---

## Phase 8: Polish & Cross-Cutting Concerns

- [X] T047 [P] Update `docs/` with `CategoryTaxonomy` document format examples, push workflow, and status condition reference. Reference `specs/021-category-taxonomy/quickstart.md` for the canonical examples.

- [X] T048 [P] Run `make pr-ready` (build + test + lint + license-check). Fix any issues. All tests must pass.

- [X] T049 Update `CLAUDE.md` (via `AGENTS.md`) `Recent Changes` section with: `021-category-taxonomy: Replaced legacy Category entity with Kubernetes-style CategoryTaxonomy backed by git push pipeline`.

---

## Dependencies & Execution Order

### Phase Dependencies

```
Phase 1 (Replace Legacy Category)
    ↓
Phase 2 (Go Types + Validator) ──┐
    ↓                            │
Phase 3 (US1: Publish)           │ (all require Phase 2)
    ↓                            │
Phase 4 (US2: Hierarchy) ────────┘
    ↓
Phase 5 (US3: Single-Category Product) — depends on Phase 2 only (independent of US1/US2)
Phase 6 (US4: Media) — depends on Phase 2 only (independent of US1/US2/US3)
    ↓ (all phases complete)
Phase 7 (E2E Tests)
    ↓
Phase 8 (Polish)
```

### User Story Dependencies

- **US1 (P1)**: Requires Phase 1 + Phase 2. No dependency on US2/US3/US4.
- **US2 (P1)**: Requires Phase 1 + Phase 2. Builds on US1 infrastructure (same files).
- **US3 (P2)**: Requires Phase 2 only. Independent of US1/US2 (product validation, no category lookup).
- **US4 (P3)**: Requires Phase 2 only. Independent of US1/US2/US3.

### Within Each Phase

- Tests must be written and **confirmed failing** before implementation in the same phase.
- `go build ./...` must pass at the end of Phase 1 before proceeding.
- Generated files (`gqlgen generate`) must be committed before T026/T027.

### Parallel Opportunities

```bash
# Phase 1 is sequential (each task modifies cascade of dependent files)

# Phase 2 parallel:
Task T015: catalog/category.go
Task T016: catalog/status.go
# T017 depends on T015+T016

# US1 tests parallel (after Phase 2):
Task T019: cataloggrpc validate tests
Task T020: cataloggrpc admit tests
Task T021: memdb backend tests

# US2 tests parallel:
Task T028: validate hierarchy tests
Task T029: admission cycle tests
Task T030: ancestor path tests

# US4 independent of US2/US3:
Task T037: validate media tests
Task T038: admit media tests

# E2E tests parallel (after all implementation phases):
Tasks T042–T045 can run in parallel
```

---

## Parallel Example: User Story 1

```bash
# TDD gate (must fail first):
go test ./internal/validate/... ./internal/cataloggrpc/... ./internal/datastore/memdb/...

# After Phase 2 complete, launch in parallel:
# T019: ValidateResources schema tests for CategoryTaxonomy
# T020: AdmitResources admission tests for CategoryTaxonomy
# T021: memdb CRUD tests
```

---

## Implementation Strategy

### MVP First (User Story 1 only)

1. Complete Phase 1: Replace legacy Category
2. Complete Phase 2: Foundational (Go types + kind-routing)
3. Complete Phase 3: User Story 1 (Publish)
4. **STOP and VALIDATE**: `go test ./...`, push a `CategoryTaxonomy` document, query via GraphQL
5. Demo: `make dev` → push `categories/electronics.md` → `category(by: {name: "electronics"})` returns title

### Full Incremental Delivery

1. Phase 1 + 2 → foundation ready
2. Phase 3 (US1) → categories are pushable and queryable
3. Phase 4 (US2) → hierarchy works, ancestor path computed
4. Phase 5 (US3) + Phase 6 (US4) → product constraint + media (parallel)
5. Phase 7 → E2E green
6. Phase 8 → PR ready

---

## Notes

- `[P]` tasks = different files, no within-phase ordering constraint
- Every test task must be run to confirm **failure** before its implementation task runs
- Phase 1 is the riskiest phase — atomic replacement of a shared interface. `go build ./...` is the gate.
- No migration script needed — Alpha software, drop and recreate ScyllaDB data if needed (`make git-clean-data CONFIRM=1`)
- Controller work (GH#244) is not in scope: `ResolvedCategoryTaxonomy`, cross-push cycle detection, File reference conditions are all deferred
