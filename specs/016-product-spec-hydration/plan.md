# Implementation Plan: Product Spec and Status Hydration

**Branch**: `016-product-spec-hydration` | **Date**: 2026-06-04 | **Spec**: [spec.md](./spec.md)
**Input**: Feature specification from `/specs/016-product-spec-hydration/spec.md`

## Summary

Four GraphQL/storage changes are bundled together:

1. **Spec hydration** — every product currently returns an empty `spec`; the converter is updated to deserialise the stored JSON blob.
2. **Status hydration** — `status` is never returned today; the converter now deserialises the stored status blob (and its pass-through `resolved` sub-fields).
3. **Cursor pagination** — `products(namespace)` ignores cursor arguments today and always returns the first page; the ScyllaDB schema is redesigned around `(namespace, creation_timestamp, uid)` so CQL itself drives keyset pagination.
4. **Unified `product(by: ProductBy!)` `@oneOf` selector** — the existing `product(namespace, name)` field is replaced with a `@oneOf` selector taking either `id` (globally unique Relay ID) or `namespacePath { namespace, name }` (composite, since `metadata.name` is unique only per namespace). Mirrors the established `RepositoryBy`/`NamespaceBy`/`CategoryBy`/`CollectionBy` convention. The dead `ProductBy { id, sku }` definition in `schema.graphqls` is removed in the same edit.

In addition, US4 confirms ingest-time validation of `spec.title` length, `spec.media[].fileRef` presence, and option-name uniqueness via tests against the existing struct-tag enforcement (no new validator code).

The schema redesign is a deliberate departure from a previously-considered in-memory pagination shim. Per the user's directive, ScyllaDB best practices win: model the table around the read pattern (`products(namespace)` newest-first), use a logged batch to maintain denormalised lookup tables (`products_by_name`, `products_by_uid`), and keep the application layer thin. See `research.md` Decision 3. The `@oneOf` selector (Decision 5a) leans directly on the new lookup tables — both arms become a single-row dispatch on the storage side.

## Technical Context

**Language/Version**: Go 1.25 (`gitstore-api`)
**Primary Dependencies**: `gqlgen v0.17.90`, `go-memdb v1.3.5`, `gocqlx/v3 v3.0.4`, `gocql` (Scylla driver), `encoding/json` (stdlib), `go-playground/validator/v10 v10.30.3`, `go.uber.org/zap`
**Storage**: ScyllaDB 5.x+ (production) via the `datastore.Datastore` interface (feature #006); `go-memdb` (development). Schema change is an **inline edit** of `migrations/001_initial_schema.cql` (alpha software, no consumers — see research.md Decision 3): the legacy `products` table is replaced with three query-driven tables — `products_by_namespace`, `products_by_name`, `products_by_uid`.
**Testing**: `go test -race` for unit and contract tests. Scylla contract tests require a running cluster (`make scylla`).
**Target Platform**: Linux server (containerised; `compose.yml` + `compose.scylla.yml`).
**Project Type**: Backend service (Go GraphQL API). No frontend or Rust changes in this feature.
**Performance Goals**: Storefront catalogue queries < 500ms for 1 000+ products (constitution target). The Scylla-native pagination decision is explicitly chosen to keep paginated reads as a single sliced range scan with `LIMIT N+1` rather than full-partition fetches in Go.
**Constraints**: One **breaking** GraphQL change: `product(namespace, name)` becomes `product(by: ProductBy!)`. Acceptable per the alpha, no-consumers stance. Other GraphQL types (`Product`, `ProductSpec`, `ProductStatus`, `ProductConnection`, `PageInfo`) are unchanged. Cursors remain opaque base64 `keyset|<RFC3339Nano>|<uid>`. Forward + backward pagination both supported (Relay Connection Spec). Memdb backend behaviour is unchanged. Schema change is an inline edit of `001_initial_schema.cql`; the migration-runner checksum guard will reject it against an already-migrated keyspace — the correct alarm for alpha. Operators wipe the dev keyspace (`docker compose down -v` against the Scylla volume) and re-migrate.
**Scale/Scope**: ≤ 10 000 products per namespace (constitution scale target). Three tables × N rows per `CreateProduct`; one-partition range read per paginated `products(namespace)` call.

## Constitution Check

*GATE: Must pass before Phase 0 research. Re-check after Phase 1 design.*

| Principle | Status | Notes |
|---|---|---|
| I. Test-First Development | ✅ Pass | research.md ships a gap analysis enumerating every missing test (converter unit tests, three-page cursor traversal contract test, batch-write fan-out tests, ingest validation tests). Tasks will land tests before implementation per the Red-Green-Refactor rule. |
| II. API-First Design | ✅ Pass | The new `ProductBy`/`ProductNamespacePath` inputs are committed to `shared/schemas/*.graphqls` first; gqlgen regenerates the model + resolver stubs from the schema (contract-first). The dead `ProductBy { id, sku }` definition is removed in the same commit. |
| III. Clear Contracts & Versioning | ⚠️ Justified | One breaking change: `product(namespace, name)` → `product(by: ProductBy!)`. Project is alpha, no consumers; the breaking change is paid down once now rather than carried as drift between schema convention and product API. Cursor format is preserved verbatim. The Scylla schema change is an inline edit to `001_initial_schema.cql` and is internal. |
| IV. Observability & Debuggability | ✅ Pass | Blob-deserialisation failures emit structured WARN logs with `uid` and `field`. Existing instrumented datastore wrapper (`internal/datastore/instrumented.go`) continues to record latency and error metrics for `ListProducts`, `CreateProduct`, etc. |
| V. User Story Driven Development | ✅ Pass | Four prioritised user stories (US1–US4); each independently testable per spec.md. Tasks will carry `[USn]` labels. |
| VI. Incremental Delivery | ✅ Pass | US1 (spec hydration), US2 (status hydration), and US3 (pagination) are independent and individually shippable. US4 (validation tests) is purely additive test coverage. |
| VII. Simplicity & YAGNI | ⚠️ Justified | The three-table denormalisation is more complex than a single table, but it is the canonical Scylla pattern already used by `repositories`/`namespace_mappings` and is required to deliver server-side pagination at the constitution's performance target. The simpler alternative (in-memory keyset over a full-partition fetch) was rejected per user directive and on Scylla best-practice grounds. See Complexity Tracking below. |

**Verdict**: PASS with one justified complexity entry. Proceed to Phase 0/1.

### Post-Design Re-evaluation (after Phase 1)

Re-checked after research.md, data-model.md, and contracts/converter.md were finalised. No new violations introduced:

- The decision to **inline-edit `001_initial_schema.cql`** (alpha, no consumers) does not change any constitution gate — it only simplifies the migration story.
- The three-table denormalisation is still the only justified complexity; the entry below stands.
- The `@oneOf` selector (Decision 5a) is one breaking GraphQL change, justified under Principle III above.
- All test gaps are catalogued in research.md's gap analysis and will be fed into `tasks.md` as test-first tasks.

**Verdict (post-design)**: PASS — proceed to Phase 2 task generation via `/speckit.tasks`.

## Project Structure

### Documentation (this feature)

```text
specs/016-product-spec-hydration/
├── plan.md              # This file (/speckit.plan command output)
├── research.md          # Phase 0 output (Decisions 1–5; rationale for Scylla-native pagination)
├── data-model.md        # Phase 1 output (datastore.Product, Scylla tables, GraphQL model mapping)
├── quickstart.md        # Phase 1 output (files to change; key implementation snippets)
├── contracts/
│   └── converter.md     # Behavioural contract for DatastoreProductToGraphQL
├── checklists/
│   └── requirements.md  # Pre-existing requirements checklist
└── tasks.md             # Phase 2 output (created by /speckit.tasks — not in this command)
```

### Source Code (repository root)

```text
gitstore-api/
├── internal/
│   ├── graph/
│   │   ├── converters.go            # ← hydrate Spec / Status / OwnerRefs
│   │   ├── converters_test.go       # ← new (or extended) unit tests
│   │   ├── pagination.go            # BuildProductConnection (unchanged behaviour)
│   │   ├── product.resolvers.go     # caller of DatastoreProductToGraphQL
│   │   ├── category.resolvers.go    # caller (products within a category)
│   │   └── collection.resolvers.go  # caller (products within a collection)
│   ├── datastore/
│   │   ├── datastore.go             # interface — unchanged
│   │   ├── memdb/backend.go         # unchanged
│   │   └── scylla/
│   │       ├── backend.go           # ← rewrite Create/Update/Delete (logged batch); rewrite Get/GetByName (two-step); rewrite ListProducts (CQL keyset)
│   │       ├── models.go            # ← replace `Product` table with three new table.New(...) entries
│   │       ├── pagination.go        # ← generalise buildPaginatedSelect partition predicate
│   │       └── migrations/
│   │           └── 001_initial_schema.cql              # ← inline edit: replace `products` table + secondary index with `products_by_namespace`, `products_by_name`, `products_by_uid`
│   └── validate/
│       └── validator_test.go        # ← add spec.title max-length, spec.media[].fileRef tests
└── tests/
    └── contract/
        └── datastore/
            └── contract_test.go     # ← spec/status round-trip; three-page cursor; batch-write fan-out
```

**Structure Decision**: Single Go service (`gitstore-api`). All changes are confined to `gitstore-api/internal/graph/`, `gitstore-api/internal/datastore/scylla/`, `gitstore-api/internal/validate/`, and the corresponding test trees. No Rust (`gitstore-git-service`) changes; no admin UI changes; no GraphQL schema changes.

## Complexity Tracking

| Violation | Why Needed | Simpler Alternative Rejected Because |
|-----------|------------|-------------------------------------|
| Three denormalised Scylla tables (`products_by_namespace`, `products_by_name`, `products_by_uid`) instead of one | Each query path (`ListProducts(namespace)`, `GetProductByName`, `GetProduct(uid)`) becomes a single-partition read with no `ALLOW FILTERING` and supports server-side keyset pagination via `(creation_timestamp, uid)` clustering. This is the canonical ScyllaDB pattern (cf. `repositories` + `namespace_mappings` already in this codebase) and is required to meet the constitution's `< 500ms / 1 000+ products` performance target. | A single `(namespace, name)` table with in-memory pagination requires fetching the entire namespace partition into the API process on every paginated request — read amplification grows linearly with partition size, defeating the cluster. `ALLOW FILTERING` was also rejected (full-partition predicate scan, against gocqlx guidance). Materialised views were rejected (asynchronous propagation; documented consistency caveats). |
