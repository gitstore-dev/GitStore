# Implementation Plan: ProductVariant Catalog Item

**Branch**: `024-product-variant` | **Date**: 2026-06-08 | **Spec**: [spec.md](spec.md)
**Input**: Feature specification from `specs/024-product-variant/spec.md`

## Summary

Introduce `ProductVariant` as the purchasable SKU unit in the GitStore catalog. A `Product` is the non-sellable parent descriptor; each `ProductVariant` carries its own title, SKU, pricing rules (with CEL-based eligibility), inventory controls, selected option values, and media. Variants are git-pushed as Kubernetes-style frontmatter documents, validated across two phases (pre-receive structural + admission DB-backed), and exposed via GraphQL queries.

## Technical Context

**Language/Version**: Go 1.25 (`gitstore-api`); Rust edition 2021 MSRV 1.82 (`gitstore-git-service` — minimal changes)
**Primary Dependencies**: `gqlgen v0.17.90`, `go-playground/validator/v10 v10.30.3`, `github.com/google/cel-go` (new — CEL syntax validation at admission), `github.com/adrg/frontmatter v0.2.0`, `gopkg.in/yaml.v3`, `go.uber.org/zap`, `shopspring/decimal`, `go-memdb v1.3.5` (dev), `gocqlx/v3 v3.0.4` + `gocql` (ScyllaDB prod)
**Storage**: `go-memdb` (dev/test); ScyllaDB 5.x+ (prod). Single `product_variant` table with `sku_namespace` and `product_ref` indexes.
**Testing**: `go test ./...`; integration tests in `tests/integration/` targeting memdb backend; ScyllaDB backend via `GITSTORE_DATASTORE__BACKEND=scylladb`
**Target Platform**: Linux server (Docker / Kubernetes)
**Performance Goals**: `productVariants` listing for 500 variants in < 2 seconds (SC-004); push admission < 5 seconds for 100-file push (constitution)
**Constraints**: Pre-receive must remain stateless (no DB); admission may use DB; CEL syntax check only (no runtime evaluation)
**Scale/Scope**: Up to 10,000 products × N variants per product; initial target: 500 variants per namespace

## Constitution Check

| Principle | Status | Notes |
|---|---|---|
| I. Test-First | PASS | Contract tests and integration tests defined before implementation tasks |
| II. API-First | PASS | GraphQL schema contract defined in `contracts/product_variant.graphqls` before resolver code |
| III. Clear Contracts & Versioning | PASS | `apiVersion: catalog.gitstore.dev/v1beta1`; additive schema changes only |
| IV. Observability | PASS | Structured logging on all admission, reconciler, and resolver paths; condition types cover all state transitions |
| V. User Story Driven | PASS | 5 user stories (US1–US5), each independently testable; all tasks labelled |
| VI. Incremental Delivery | PASS | US1+US2 (push + query) are P1 MVP; US3 (option validation) P1; US4+US5 (pricing/inventory + update) P2 |
| VII. Simplicity | PASS | Single table; reuses existing envelope, converters, and service patterns; `cel-go` addition justified by admission CEL check requirement |

## Project Structure

### Documentation (this feature)

```text
specs/024-product-variant/
├── plan.md              ← this file
├── research.md          ← Phase 0 output
├── data-model.md        ← Phase 1 output
├── quickstart.md        ← Phase 1 output
├── contracts/
│   └── product_variant.graphqls   ← Phase 1 output
└── tasks.md             ← Phase 2 output (/speckit.tasks — not yet created)
```

### Source Code (repository root)

```text
gitstore-api/
├── go.mod                                               + cel-go dependency
├── internal/
│   ├── catalog/
│   │   └── product_variant.go                           NEW — resource structs + status types
│   ├── validate/
│   │   └── validator.go                                 MODIFY — add ProductVariant to ParseResource dispatcher + pre-receive validators
│   ├── datastore/
│   │   ├── entities.go                                  MODIFY — add ProductVariant entity struct
│   │   ├── datastore.go                                 MODIFY — add ProductVariant methods to Datastore interface
│   │   └── memdb/
│   │       ├── schema.go                                MODIFY — add "product_variant" table
│   │       └── backend.go                               MODIFY — implement ProductVariant Datastore methods
│   ├── cataloggrpc/
│   │   └── server.go                                    MODIFY — add admitProductVariant, validateProductVariantSpec
│   └── graph/
│       ├── product_variant.resolvers.go                 NEW — productVariant / productVariants / Product.productVariants resolvers
│       ├── converters.go                                MODIFY — add DatastoreProductVariantToGraphQL
│       └── service.go                                   MODIFY — add GetProductVariant*, ListProductVariants* methods
├── shared/schemas/
│   ├── product_variant.graphqls                         NEW — ProductVariant schema (based on contracts/product_variant.graphqls)
│   └── schema.graphqls                                  MODIFY — add ProductVariantBy, ProductVariantNamespacePath input types

tests/
└── integration/
    └── product_variant_test.go                          NEW — integration tests (US1–US5)
```

## Complexity Tracking

No constitution violations. No additional justification needed.

## Implementation Phases

### Phase 0 — Research ✅ Complete

See [research.md](research.md). All NEEDS CLARIFICATION resolved.

### Phase 1 — Design & Contracts ✅ Complete

- [data-model.md](data-model.md) — entity structs, datastore interface, memdb table, validation rules, state transitions
- [contracts/product_variant.graphqls](contracts/product_variant.graphqls) — full GraphQL schema contract
- [quickstart.md](quickstart.md) — authoring guide, validation phase table, query examples

### Phase 2 — Implementation Tasks

See `tasks.md` (generated by `/speckit.tasks`).

#### Task group summary (for planning reference)

**Foundational (blocks all user stories)**
- F1: Add `github.com/google/cel-go` to `go.mod`
- F2: `catalog/product_variant.go` — resource + status structs
- F3: `datastore/entities.go` — `ProductVariant` entity struct (with `SKU`, `ProductRefName` denorm fields)
- F4: `datastore/datastore.go` — extend `Datastore` interface
- F5: `datastore/memdb/schema.go` — add `"product_variant"` table
- F6: `datastore/memdb/backend.go` — implement all ProductVariant Datastore methods
- F7: `validate/validator.go` — add `ProductVariant` case to `ParseResource` dispatcher + `validateProductVariantSpec` (pre-receive rules)
- F8: `cataloggrpc/server.go` — `admitProductVariant` (admission rules: SKU uniqueness, productRef, option compat, CEL)
- F9: `shared/schemas/product_variant.graphqls` + `schema.graphqls` additions

**US1 — Push & admit a ProductVariant (P1)**
- Depends on F1–F8
- Contract test: push valid variant → admitted
- Contract test: push with missing sku → pre-receive rejects
- Integration test: full push → datastore persisted → queryable

**US2 — Query ProductVariant (P1)**
- Depends on F9 + US1 foundation
- `graph/product_variant.resolvers.go` — `productVariant`, `productVariants` resolvers
- `graph/service.go` additions
- `graph/converters.go` — `DatastoreProductVariantToGraphQL`
- Integration test: query by name, by ID; assert all `spec` and `status.resolved` fields

**US3 — Parent product link + option compatibility (P1)**
- Depends on F8 (admission) + US1
- Integration test: productRef not found → `ProductResolved: False`; reconciler resolves
- Integration test: invalid option name → rejected at admission
- Integration test: invalid option value → rejected at admission
- Integration test: co-push product+variant → both admitted; reconciler resolves variant

**US4 — Pricing + inventory schema validation (P2)**
- Depends on F1 (cel-go) + F7 (pre-receive) + F8 (admission)
- Integration test: invalid CEL → rejected at admission
- Integration test: invalid inventory policy → rejected pre-receive
- Integration test: `validFromTime > validUntilTime` → rejected pre-receive
- Integration test: `quantity.min > quantity.max` → rejected pre-receive
- Integration test: valid priceSet → `status.resolved.priceSet` populated

**US5 — Update a ProductVariant (P2)**
- Depends on US1–US3
- Integration test: update pricing → `status.resolved.priceSet` updated
- Integration test: update to invalid selectedOptions → rejected, variant unchanged

**Documentation**
- Update `docs/` with single-pass catalog authoring advantage (noted in clarification session)
