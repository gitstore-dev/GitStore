# Implementation Plan: Collection Resource Contract with Label Selectors

**Branch**: `022-collection-resource-contract` | **Date**: 2026-06-07 | **Spec**: [spec.md](spec.md)
**Input**: Feature specification from `/specs/022-collection-resource-contract/spec.md`

## Summary

Replace the legacy flat `Collection` entity with a Kubernetes-style git-backed `Collection` resource following the `CategoryTaxonomy` envelope pattern. Authors define collections via YAML frontmatter with a `LabelSelector` that determines product membership. The GraphQL API exposes `collection(by: ...)`, paginated `collections`, and a `collection.products(first, last, ...)` connection with snapshot-at-query-time cursor semantics. Legacy mutations become stubs. Label selector evaluation is implemented in pure Go with no new external dependencies.

## Technical Context

**Language/Version**: Go 1.25 (`gitstore-api`); Rust edition 2021 MSRV 1.82 (`gitstore-git-service` — no changes needed)
**Primary Dependencies**: `gqlgen v0.17.90`, `go-memdb v1.3.5` (dev), `gocqlx/v3 v3.0.4` + `gocql` (ScyllaDB prod), `go-playground/validator/v10`, `go.uber.org/zap`, `github.com/adrg/frontmatter v0.2.0`, `gopkg.in/yaml.v3`
**Storage**: ScyllaDB 5.x+ (prod) via three-table pattern; `go-memdb` (dev/test)
**Testing**: `go test ./...`; contract tests in `tests/contract/datastore/`; integration tests in `tests/integration/`
**Target Platform**: Linux server (Docker Compose / Kubernetes)
**Performance Goals**: `collection.products(first: 20)` under 2 seconds for namespaces with up to 10,000 products
**Constraints**: No new external Go dependencies; label selector evaluation in pure Go
**Scale/Scope**: Up to 10,000 products per namespace; up to 50 collections per namespace (SC-005)

## Constitution Check

| Principle | Gate | Status |
|-----------|------|--------|
| I. Test-First | Contract + unit tests written before implementation | ✅ Pass — tasks ordered test-first |
| II. API-First | GraphQL schema defined before resolvers | ✅ Pass — contract in `contracts/collection.graphqls` |
| III. Clear Contracts | Schema follows additive evolution; legacy mutations stubbed | ✅ Pass |
| IV. Observability | Admission and query logging follow existing `zap` pattern | ✅ Pass |
| V. User Story Driven | All tasks labelled US1–US4 | ✅ Pass |
| VI. Incremental Delivery | P1 (push + query) shippable before P2 (selector membership) | ✅ Pass |
| VII. Simplicity | No new deps; selector eval ~150 lines pure Go | ✅ Pass |

No violations. Complexity tracking section not required.

## Project Structure

### Documentation (this feature)

```text
specs/022-collection-resource-contract/
├── plan.md              ← this file
├── research.md          ← Phase 0 output
├── data-model.md        ← Phase 1 output
├── quickstart.md        ← Phase 1 output
├── contracts/
│   └── collection.graphqls   ← Phase 1 output
├── checklists/
│   └── requirements.md
└── tasks.md             ← Phase 2 output (/speckit.tasks)
```

### Source Code (repository root)

```text
gitstore-api/
├── internal/
│   ├── catalog/
│   │   ├── collection.go          # NEW — CollectionResource, CollectionSpec, LabelSelector types
│   │   └── selector.go            # NEW — MatchesLabels evaluation function
│   ├── validate/
│   │   └── validator.go           # MODIFIED — add Collection case to ParseResource
│   ├── datastore/
│   │   ├── entities.go            # MODIFIED — replace flat Collection with K8s-style struct
│   │   ├── datastore.go           # MODIFIED — add Collection + ListProductsByLabelSelector methods
│   │   ├── instrumented.go        # MODIFIED — wrap new methods
│   │   ├── memdb/
│   │   │   ├── schema.go          # MODIFIED — rebuild collection table with name_namespace index
│   │   │   └── backend.go         # MODIFIED — implement Collection CRUD + selector query
│   │   └── scylla/
│   │       ├── migrations/
│   │       │   └── 003_collection_kubernetes_schema.cql  # NEW
│   │       ├── models.go          # MODIFIED — add CollectionRow, CollectionByNameRow, CollectionByUIDRow
│   │       └── backend.go         # MODIFIED — implement Collection CRUD + selector query
│   ├── cataloggrpc/
│   │   └── server.go              # MODIFIED — add Collection admission branch
│   └── graph/
│       ├── collection.resolvers.go  # MODIFIED — replace legacy resolvers with K8s-style
│       └── converters.go            # MODIFIED — DatastoreCollectionToGraphQL
├── tests/
│   ├── contract/datastore/
│   │   └── contract_test.go       # MODIFIED — add Collection CRUD contract tests
│   └── integration/
│       └── collection_test.go     # NEW — E2E push + query integration tests
shared/
└── schemas/
    ├── collection.graphqls        # REWRITTEN — K8s envelope schema
    └── schema.graphqls            # MODIFIED — add LabelSelector, LabelSelectorRequirement, LabelSelectorOperator, CollectionBy, CollectionNamespacePath
```

## Phase 0: Research

**Status**: ✅ Complete — see [research.md](research.md)

Key decisions:
- Replace (not migrate) the legacy `Collection` entity.
- Label selector in pure Go, no external dependency.
- Snapshot cursor: ordered UID list encoded in opaque cursor token, server-side LRU cache.
- Three-table ScyllaDB pattern mirroring `category_taxonomy`.
- `collection.products` is a live query; `memberCount` is a cached admission-time hint.

## Phase 1: Design & Contracts

**Status**: ✅ Complete

| Artifact | Path | Status |
|----------|------|--------|
| Data model | [data-model.md](data-model.md) | ✅ Done |
| GraphQL contract | [contracts/collection.graphqls](contracts/collection.graphqls) | ✅ Done |
| Quickstart | [quickstart.md](quickstart.md) | ✅ Done |

### Post-Design Constitution Check

All principles pass. No new complexity introduced beyond what `CategoryTaxonomy` already established.

## Implementation Sequence

Tasks are ordered: **foundational → US1 (P1) → US2 (P1) → US3 (P2) → US4 (P2) → polish**.

### Foundation (blocks all user stories)

1. `catalog/collection.go` — `CollectionResource`, `CollectionSpec`, `LabelSelector`, `LabelSelectorRequirement` structs with YAML tags and validator annotations.
2. `catalog/selector.go` — `MatchesLabels` pure-Go evaluation function (unit-tested first).
3. `validate/validator.go` — add `Collection` to `ParsedResource` and `ParseResource` switch with `validateCollectionSpec`.
4. `datastore/entities.go` — replace flat `Collection` struct with K8s-style entity.
5. `datastore/datastore.go` — add `CreateCollection`, `GetCollection`, `GetCollectionByName`, `ListCollections`, `UpdateCollection`, `ListProductsByLabelSelector`.
6. `datastore/memdb/schema.go` + `backend.go` — rebuild memdb Collection table and implement all new methods.
7. `migrations/003_collection_kubernetes_schema.cql` — three-table ScyllaDB schema.
8. `datastore/scylla/models.go` + `backend.go` — ScyllaDB Collection CRUD and `ListProductsByLabelSelector`.
9. `datastore/instrumented.go` — wrap new Datastore methods.

### US1: Define a Collection via git push (P1)

10. `cataloggrpc/server.go` — add `Collection` admission branch: parse, validate, upsert, compute `memberCount` from selector, write status.
11. Contract test: `tests/contract/datastore/contract_test.go` — Collection CRUD.
12. Integration test: `tests/integration/collection_test.go` — push valid Collection, verify admission.
13. Integration test: push invalid Collection (missing title), verify rejection.

### US2: Query a Collection (P1)

14. `shared/schemas/collection.graphqls` — rewrite with K8s envelope (from `contracts/collection.graphqls`).
15. `shared/schemas/schema.graphqls` — add `LabelSelector`, `LabelSelectorRequirement`, `LabelSelectorOperator`, `CollectionBy`, `CollectionNamespacePath`.
16. `gqlgen` code generation — run `go generate ./internal/graph/...`.
17. `graph/converters.go` — `DatastoreCollectionToGraphQL` converter.
18. `graph/collection.resolvers.go` — `Collection`, `Collections` query resolvers; legacy mutation stubs.
19. Integration test: query `collection(by: namespacePath)` → verify metadata, spec, status.

### US3: Selector-driven membership + `collection.products` (P1/P2)

20. `graph/collection.resolvers.go` — `collection.Products` resolver with snapshot-cursor pagination.
21. `internal/graph/service.go` — `ListProductsBySelector` service wrapper.
22. Unit tests: `catalog/selector_test.go` — all four operators, combined matchLabels + matchExpressions.
23. Integration test: verify `collection.products` returns only labelled products; `memberCount` correct.

### US4: Update a Collection (P2)

24. Integration test: push updated Collection with narrowed selector; verify `memberCount` decreases and `collection.products` reflects new set.

### Polish

25. `graph/category_resolver_test.go` pattern — add `graph/collection_resolver_test.go` unit tests for resolver logic.
26. `docs/products/collection.md` — author guide following `category-taxonomy.md` pattern.
