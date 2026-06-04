# Implementation Plan: Product Resource Contract — Kubernetes-style Frontmatter Schema

**Branch**: `014-product-frontmatter` | **Date**: 2026-06-01 | **Spec**: [spec.md](spec.md)  
**Input**: Feature specification from `specs/014-product-frontmatter/spec.md`

## Summary

Define the canonical `Product` resource contract using Kubernetes-style frontmatter (`apiVersion`, `kind`, `metadata`, `spec`, `status`). This is a **contract definition task** — no resolver wiring, no runtime validation logic — it establishes the typed Go structs, Rust admission types, replacement GraphQL schema, rewritten ScyllaDB table, and replacement memdb schema that downstream tasks (GH#185, GH#186, GH#187) depend on. 
Storage model: git is the authoritative source for what authors write; the datastore holds the fully hydrated view (spec + all metadata + status) so all reads are single-store, zero merge cost.

## Technical Context

**Language/Version**: Go 1.25 (`gitstore-api`), Rust edition 2021 MSRV 1.82 (`gitstore-git-service`)  
**Primary Dependencies**: `github.com/adrg/frontmatter v0.2.0`, `go-playground/validator/v10 v10.30.3`, `gqlgen v0.17.90` (Go) · `gix 0.84.0`, `async-trait 0.1` (Rust — no new deps; #013 already delivered `AdmissionHandler`)  
**Storage**: ScyllaDB 5.x+ (production) / `go-memdb v1.3.5` (development) — via `datastore.Datastore` interface  
**Testing**: `go test ./...` with `testify`, `cargo test`  
**Target Platform**: Linux server (Docker Compose + bare metal)  
**Project Type**: Multi-service (git server + GraphQL API)  
**Performance Goals**: Git push validation < 5 seconds for 100-file push (constitution target)  
**Constraints**: Alpha software — no migration tooling, no backwards compatibility shims  
**Scale/Scope**: Up to 10,000 products (constitution constraint)

## Constitution Check

*GATE: Must pass before Phase 0 research. Re-check after Phase 1 design.*

| Principle                         | Status | Notes                                                                                                                  |
|-----------------------------------|--------|------------------------------------------------------------------------------------------------------------------------|
| I. Test-First                     | ✅ Pass | Tests written before implementation in every task; contract tests for frontmatter parsing fail-first                   |
| II. API-First                     | ✅ Pass | GraphQL schema additions defined in `contracts/graphql-schema.md` before resolvers                                     |
| III. Clear Contracts & Versioning | ✅ Pass | `apiVersion: catalog.gitstore.dev/v1beta1` pinned; full rewrite is the v1beta1 contract — alpha has no prior consumers |
| IV. Observability                 | ✅ Pass | Admission rejections use structured `HookRejection` with phase + reason; existing tracing infrastructure               |
| V. User Story Driven              | ✅ Pass | Three user stories (US1 P1, US2 P1, US3 P2) with independent test criteria                                             |
| VI. Incremental Delivery          | ✅ Pass | P1 (author a file + parse spec) deliverable before P2 (status)                                                         |
| VII. Simplicity & YAGNI           | ✅ Pass | No new services; full rewrite avoids split-read complexity; single-store reads; deferred: SKU fate, collection refs    |

**Post-design re-check**: All gates pass. No violations to justify.

## Project Structure

### Documentation (this feature)

```text
specs/014-product-frontmatter/
├── plan.md              ← this file
├── research.md          ← Phase 0 output
├── data-model.md        ← Phase 1 output
├── quickstart.md        ← Phase 1 output
├── contracts/
│   ├── go-types.md      ← Go type signatures
│   ├── graphql-schema.md ← GraphQL schema additions
│   └── rust-types.md    ← Rust admission types
├── checklists/
│   └── requirements.md  ← spec quality checklist
└── tasks.md             ← Phase 2 output (/speckit.tasks — not created here)
```

### Source Code (repository root)

```text
gitstore-api/
├── internal/
│   ├── catalog/                      ← NEW package
│   │   ├── product.go                ← ProductResource, ObjectMeta, ProductSpec types
│   │   ├── status.go                 ← ProductStatus, Condition, Resolved* types
│   │   └── product_test.go           ← contract tests (test-first)
│   ├── validate/
│   │   ├── validator.go              ← NEW: production frontmatter validation logic
│   │   ├── validator_test.go         ← REWRITE: typed struct assertions
│   │   └── testdata/
│   │       └── macbook-pro-64gb-1tb-ssd-m4.md  (existing fixture, reused)
│   └── datastore/
│       ├── memdb/
│       │   └── schema.go             ← REWRITE: replace product table indexes
│       └── scylla/
│           └── migrations/
│               └── 001_initial_schema.cql  ← REWRITE: new products table schema
shared/
└── schemas/
    └── product.graphqls              ← REWRITE: replace with K8s-style Product type

gitstore-git-service/
└── (no changes — #013 already delivered AdmissionHandler; concrete impl deferred to GH#105/106)
```

**Structure Decision**: Existing multiservice layout. Go API gets a new `catalog` package for the typed resource contract. **No Rust changes** — the git service is a dumb transport layer; `AdmissionHandler` delegation to the API is the integration point, already wired by #013. Three existing Go/GraphQL/ScyllaDB files are rewritten because their schemas are incompatible with the new model.

## Complexity Tracking

No constitution violations. No complexity justification required.

## Phase 0: Research

**Status**: Complete — see [research.md](research.md)

Key findings:
- `gitstore-api/internal/validate/` is a stub (test only, no production code). Production parser must be written.
- `github.com/adrg/frontmatter v0.2.0` is already in `go.mod` — no new dependency.
- Existing `Product` model uses flat fields; no typed YAML struct exists.
- ScyllaDB `001_initial_schema.cql` is rewritten — primary key change (`bucket+created_at+id` → `namespace+name`) requires full table rebuild; alpha means no data to migrate.
- Datastore holds the fully hydrated view; all reads are single-store with no git blob lookups on the hot path.
- `AdmissionHandler` trait and `NoopAdmissionHandler` are already in `src/git/hooks.rs` (#013). Git service is a dumb transport — no Rust changes in this feature.
- Two independent callouts: schema validation at `pre-receive` (blocking, `GITSTORE_SCHEMA_VALIDATION__PHASE`) and admission control at `post-receive` (fire-and-forget, `GITSTORE_ADMISSION_CONTROL__PHASE`). Rust config refactor (`remove validating_admission_policy`, add `schema_validation.phase`) deferred to GH#105/106.

## Phase 1: Design & Contracts

**Status**: Complete

| Artifact                | Path                          | Status |
|-------------------------|-------------------------------|--------|
| Research                | `research.md`                 | ✅ Done |
| Data model              | `data-model.md`               | ✅ Done |
| Go type contract        | `contracts/go-types.md`       | ✅ Done |
| GraphQL schema contract | `contracts/graphql-schema.md` | ✅ Done |
| Rust admission contract | `contracts/rust-types.md`     | ✅ Done |
| Quickstart              | `quickstart.md`               | ✅ Done |

## Implementation Notes

### Task ordering (for /speckit.tasks)

Implementation tasks must follow the constitution's test-first workflow:

1. **Go catalog types** (US1+US2): Write `catalog/product_test.go` (failing) → implement `catalog/product.go` + `catalog/status.go`
2. **Go validator** (US1+US2): Extend `validate/validator_test.go` (failing) → implement `validate/validator.go`
3. **Go admission validator** (US1): Write `validate/validator_test.go` cases for pre-receive (forbidden fields, legacy format) → implement `validate/validator.go` callable from the admission handler wired by GH#105/106
4. **ScyllaDB schema** (US3): Write schema test → rewrite `001_initial_schema.cql` with new `products` table
5. **memdb schema** (US3): Rewrite memdb schema tests → replace `product` table indexes in `schema.go`
6. **GraphQL schema** (US1+US3): Rewrite `shared/schemas/product.graphqls` with K8s-style types from `contracts/graphql-schema.md`

### Dependencies for downstream tasks

- GH#185 (validation semantics) and GH#186 (domain constraints) are unblocked once this feature's types are merged.
- The `ProductResource`, `ObjectMeta`, `ProductSpec` structs are the canonical import source for both.
- The `FrontmatterAdmissionHandler` stub is the integration point for GH#106's policy engine.
