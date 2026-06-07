# Implementation Plan: Collection Frontmatter Integration Tests and Documentation

**Branch**: `023-collection-integration-tests` | **Date**: 2026-06-07 | **Spec**: [spec.md](spec.md)  
**Input**: Feature specification from `/specs/023-collection-integration-tests/spec.md`

## Summary

Add end-to-end integration tests for the `Collection` resource kind (introduced in spec 022) that exercise both the memdb and ScyllaDB backends, covering the full push-admission-query cycle. Extend the existing `tests/integration` module with a `collection_test.go` file and a `commitCollection` push helper. Add contract tests for Collection-specific datastore operations. Publish developer-facing documentation in `docs/collection.md`.

## Technical Context

**Language/Version**: Go 1.25 (`gitstore-api`, `tests/integration`)  
**Primary Dependencies**: `go-playground/validator/v10 v10.30.3` (validation), `gqlgen v0.17.90` (GraphQL), `go-memdb v1.3.5` (dev backend), `gocqlx/v3 v3.0.4` + `gocql` (ScyllaDB backend)  
**Storage**: `go-memdb` (dev/test default) and ScyllaDB 5.4 (prod/CI); backend selected at runtime via `GITSTORE_DATASTORE__BACKEND` env var  
**Testing**: `go test` with no build tags for memdb; `go test -tags scylla` for ScyllaDB contract tests; live compose stack for integration tests  
**Target Platform**: Linux (CI), macOS (local dev)  
**Project Type**: Integration test module + documentation  
**Performance Goals**: Individual integration test ≤ 30s; full `go test ./...` in integration suite ≤ 120s  
**Constraints**: Tests must pass against both memdb and ScyllaDB stacks without code changes; no new external dependencies  
**Scale/Scope**: ~10 new integration test cases; ~1 new datastore contract test group (Collection label-selector); 1 documentation file

## Constitution Check

| Principle | Status | Notes |
|-----------|--------|-------|
| I. Test-First | ✅ Pass | Tests written before any documentation prose; test cases fail until matching code/infra exists |
| II. API-First | ✅ Pass | GraphQL contract already defined in spec 022 (`collection.graphqls`); no new schema changes needed |
| III. Clear Contracts & Versioning | ✅ Pass | Validation error contract documented in `contracts/collection-validation-errors.md` |
| IV. Observability | ✅ Pass | Existing structured logging in admission handler covers collection paths; no new logging required |
| V. User Story Driven | ✅ Pass | All tasks map to US1–US4 with independent test criteria |
| VI. Incremental Delivery | ✅ Pass | P1 (push acceptance + rejection tests) is independently shippable before P2 (selector + docs) |
| VII. Simplicity/YAGNI | ✅ Pass | No new dependencies, no new services, no new abstractions |

**No gate violations. Plan is clear to proceed.**

## Project Structure

### Documentation (this feature)

```text
specs/023-collection-integration-tests/
├── plan.md              # This file
├── research.md          # Phase 0 — decisions documented
├── data-model.md        # Phase 1 — entity reference
├── quickstart.md        # Phase 1 — developer quickstart
├── contracts/
│   └── collection-validation-errors.md   # validation error contract
└── tasks.md             # Phase 2 output (/speckit.tasks — NOT created here)
```

### Source Code (impacted paths)

```text
tests/integration/
├── githelper_test.go          # ADD: commitCollection() helper
└── collection_test.go         # NEW: all collection integration tests (US1–US3)

gitstore-api/tests/contract/datastore/
└── contract_test.go           # EXTEND: add Collection label-selector contract tests

docs/
└── collection.md              # NEW: Collection resource reference documentation
```

**Structure Decision**: Extends existing modules at their natural integration points. No new Go modules, no new binaries. The `tests/integration` module is the home for all end-to-end push-pipeline tests; `gitstore-api/tests/contract/datastore/` is the home for CRUD + label-selector contract tests.

## Phase 0 Findings Summary

All NEEDS CLARIFICATION items resolved. See [research.md](research.md) for full decision rationale.

Key resolved decisions:
1. Tests live in `tests/integration/collection_test.go` — same module as existing push tests
2. Backend selection is infra-level (compose overlay), not code-level — no build tags needed
3. `spec.selector` is **optional** — test the absent-selector (zero-member) path explicitly
4. `spec.title` is **required** — fixture for invalid-title test omits this field
5. Three implemented kinds: Product, CategoryTaxonomy, Collection — `targetRef.kind` must be `"Product"`
6. Collection documents pushed under `collections/<name>.md` in the catalog repository
7. Documentation goes in `docs/collection.md`

## Phase 1 Design

### Integration Test Cases (`tests/integration/collection_test.go`)

#### User Story 1 — Valid Collection Accepted (P1)

| Test ID | Name | Scenario |
|---------|------|----------|
| T050 | `TestCollection_ValidPushAccepted` | Push minimal valid collection (title only, no selector) → push succeeds, `collection` query returns `Ready: True`, `memberCount: 0` |
| T051 | `TestCollection_WithSelectorMatchesProducts` | Seed 3 products with `gitstore.dev/brand: apple`, push collection with `matchLabels: {gitstore.dev/brand: apple}` → `memberCount >= 3`, `collection.products` edges non-empty |
| T052 | `TestCollection_OptionalMediaAbsent` | Push collection with optional `fileRef` → push accepted, `resolved.media` empty for that entry |

#### User Story 2 — Invalid Collection Rejected (P1)

| Test ID | Name | Scenario |
|---------|------|----------|
| T053 | `TestCollection_MissingTitle` | Push document with empty/absent `spec.title` → push rejected, error contains `title` |
| T054 | `TestCollection_WrongKind` | Push document with `kind: Product` via collections path → push rejected with kind mismatch error |
| T055 | `TestCollection_InvalidTargetRefKind` | Push with `targetRef.kind: CategoryTaxonomy` → push rejected, error contains `targetRef.kind` |
| T056 | `TestCollection_InvalidOperatorInExpression` | Push with `operator: Between` → push rejected, error contains `matchExpressions` |
| T057 | `TestCollection_InOperatorEmptyValues` | Push with `operator: In` and empty `values: []` → push rejected, error contains `requires at least one value` |

#### User Story 3 — Selector Semantics and Determinism (P2)

| Test ID | Name | Scenario |
|---------|------|----------|
| T058 | `TestCollection_DeterministicMembership` | Resolve same collection twice → identical product sets |
| T059 | `TestCollection_SelectorNotIn` | Push products A (brand=apple) and B (brand=samsung); collection with `NotIn: [apple]` → only B in results |
| T060 | `TestCollection_SelectorExists` | Push product with `gitstore.dev/featured` label (any value); collection with `Exists` on that key → product included |
| T061 | `TestCollection_SelectorDoesNotExist` | Products with and without `gitstore.dev/sale` label; collection `DoesNotExist` for that key → only unlabeled products |
| T062 | `TestCollection_NewProductAppearsAfterPush` | Push collection → memberCount=2; push third matching product → re-resolve → memberCount=3 |

### Contract Test Extensions (`gitstore-api/tests/contract/datastore/contract_test.go`)

Add to `RunContractSuite`:

| Test | Verifies |
|------|----------|
| `Collection/LabelSelector_MatchLabels` | `ListCollectionsByLabelSelector` with exact match returns only matching collections |
| `Collection/LabelSelector_NoMatch` | Selector that matches nothing returns empty list (not error) |
| `Collection/LabelSelector_MatchExpressions_In` | `In` operator filters correctly |

### Documentation (`docs/collection.md`)

Sections:
1. Overview — what a Collection is, how it relates to products
2. Document schema — field reference table (all CollectionSpec + ObjectMeta fields)
3. LabelSelector reference — operators, semantics, examples for all 4 variants
4. CollectionStatus reference — all status fields, condition types, reason tokens
5. Validation errors — reproduces `contracts/collection-validation-errors.md` in prose form
6. Complete examples — minimal (title only), full (selector + media), zero-member selector
7. Querying collections — example GraphQL queries for `collection` and `collections`

### Push helper addition (`tests/integration/githelper_test.go`)

```go
// commitCollection writes a Collection markdown file and commits it.
func (h *pushHelper) commitCollection(filename, content string) {
    h.t.Helper()
    dir := filepath.Join(h.workDir, "collections")
    if err := os.MkdirAll(dir, 0755); err != nil {
        h.t.Fatalf("mkdir collections: %v", err)
    }
    path := filepath.Join(dir, filename)
    if err := os.WriteFile(path, []byte(content), 0644); err != nil {
        h.t.Fatalf("write collection file: %v", err)
    }
    run(h.t, h.workDir, "git", "add", path)
    run(h.t, h.workDir, "git", "commit", "-m", fmt.Sprintf("add %s", filename))
}
```

### Collection fixture functions (new, in `collection_test.go`)

```go
// minimalCollectionFixture returns a valid Collection with title only (no selector).
func minimalCollectionFixture(name, ns string) string { ... }

// collectionWithMatchLabels returns a Collection with a matchLabels selector.
func collectionWithMatchLabels(name, ns string, labels map[string]string) string { ... }

// collectionWithMatchExpression returns a Collection with a single matchExpressions entry.
func collectionWithMatchExpression(name, ns, key, operator string, values []string) string { ... }

// invalidCollectionMissingTitle returns a document with no spec.title.
func invalidCollectionMissingTitle(name, ns string) string { ... }

// invalidCollectionBadTargetRef returns a document with targetRef.kind != Product.
func invalidCollectionBadTargetRef(name, ns, kind string) string { ... }
```

## Complexity Tracking

No constitution violations. No complexity justification required.
