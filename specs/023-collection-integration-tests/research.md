# Research: Collection Frontmatter Integration Tests and Documentation

## Decision 1: Integration test location and module

**Decision**: New integration tests go in `tests/integration/collection_test.go` inside the existing `integration` Go module at `tests/integration/`.

**Rationale**: All existing end-to-end integration tests (product lifecycle, category taxonomy, git HTTP, health) live in this module. It already wires `pushHelper`, `API_URL`/`GIT_URL` env vars, and `newPushHelper` as a shared scaffold. Introducing a new module would duplicate this infrastructure with no benefit.

**Alternatives considered**:
- New top-level `tests/collection-e2e/` module — rejected; duplicates `pushHelper` and adds another `go.mod` to maintain.
- Unit tests inside `gitstore-api/internal/validate/` — rejected; spec requires end-to-end push → admission → GraphQL query coverage, not just parsing unit coverage.

---

## Decision 2: Backend selection for integration tests

**Decision**: Tests in `tests/integration/` run against both backends without code changes:
- **memdb**: Default CI job (`integration-test`) — runs against the compose stack started with `compose.yml` only (no scylla overlay). No build tags needed.
- **ScyllaDB**: The existing `integration-test-scylla` CI job starts the full compose stack with `compose.yml` + `compose.scylla.yml` (which injects `GITSTORE_DATASTORE__BACKEND=scylla`). The same test binaries execute unchanged.

**Rationale**: The `tests/integration` module has no build tags and no backend-aware code; backend selection is an infrastructure concern (which compose overlay is active) not a test concern. This is the exact model used by `product_lifecycle_test.go` and `category_taxonomy_test.go`.

**Alternatives considered**:
- Build tag `//go:build scylla` on collection-specific tests — rejected; the product and category tests don't use build tags for the integration suite, and consistency is more important than selectively skipping.
- Separate `datastore-contract-test` target (which does use `//go:build scylla`) — already covers CRUD correctness; this spec adds push-pipeline and GraphQL query coverage on top.

---

## Decision 3: Selector optionality in tests

**Decision**: Tests MUST cover both the `selector`-absent case (resolves to zero members, `conditions[MembersResolved].status: "True"` with `reason: NoProductsMatched`) and the `selector`-present case. The `spec.title` field is required and MUST be present in every valid fixture.

**Rationale**: The user confirmed `selector` is optional and `title` is required. The `CollectionSpec` struct already reflects this (`Title` has `validate:"required"`, `Selector` is `*LabelSelector` without required tag). The spec was corrected accordingly (FR-002 updated, US2 acceptance scenario updated).

**Alternatives considered**:
- Only testing selector-present case — rejected; the zero-member and absent-selector paths are distinct code paths in `validateCollectionSpec` and in the admission handler.

---

## Decision 4: targetRef and resource kind scope

**Decision**: `spec.targetRef` is optional; only `kind: Product` is valid when present. Tests verify rejection of any other kind value. The three currently implemented resource kinds are **Product**, **CategoryTaxonomy**, and **Collection** — no others are valid as `targetRef.kind`.

**Rationale**: Confirmed from source (`validateCollectionSpec` in `validator.go`, `ParsedResource` struct, memdb schema tables, `admitResources` switch). The user explicitly noted this.

**Alternatives considered**: None — this is a code-confirmed fact, not a design decision.

---

## Decision 5: Collection fixture directory convention

**Decision**: Collection documents pushed in tests will live under `collections/<name>.md` in the repository (mirroring `products/` and `categories/` paths used by existing tests). A new `commitCollection(filename, content)` helper will be added to `githelper_test.go` following the same pattern as `commitProduct` and `commitCategory`.

**Rationale**: The pre-receive hook scans all changed files regardless of directory — the path is not semantically significant for validation. Using `collections/` is consistent with the established naming pattern and makes test diffs readable.

**Alternatives considered**:
- `products/` — confusing, wrong kind.
- Root-level files — diverges from established pattern.

---

## Decision 6: GraphQL query coverage for tests

**Decision**: Integration tests will verify collection membership via the `collection.products` GraphQL connection (paginated) AND via `collection.status.resolved.memberCount` (cached hint). Both fields are required per FR-001 and the contracts defined in spec 022 (`collection.graphqls`).

**Rationale**: `memberCount` is a cached value written at admission time; `collection.products` is the live authoritative source. Testing both verifies the admission-time snapshot and the live resolver independently.

**Alternatives considered**:
- Only testing `memberCount` — insufficient; the live resolver path is separate code.
- Only testing `collection.products` — misses status condition reporting required by FR-001.

---

## Decision 7: Documentation location and format

**Decision**: Documentation goes in `docs/collection.md` as a standalone reference document, following the file-per-resource-kind convention already established by prior features. It will include: full frontmatter field reference, complete valid example, validation error table, and `CollectionStatus` field descriptions.

**Rationale**: `docs/` is the project's documentation directory per CLAUDE.md. A standalone file per resource kind is consistent with the `CategoryTaxonomy` documentation pattern.

**Alternatives considered**:
- Inline in the existing `docs/catalog.md` — would make that file grow unwieldy; separate files are easier to link and update.
- ADR format — overkill for a reference document; reserved for architectural decisions.
