# Implementation Plan: CategoryTaxonomy Frontmatter and Hierarchy Enforcement

**Branch**: `021-category-taxonomy` | **Date**: 2026-06-06 | **Spec**: [spec.md](spec.md)
**Input**: Feature specification from `specs/021-category-taxonomy/spec.md`

## Summary

Extend the existing Kubernetes-style frontmatter push pipeline to support a new `CategoryTaxonomy` resource kind. The pre-receive `ValidateResources` gRPC handler gains multi-kind routing and validates `CategoryTaxonomy` schema. The post-receive `AdmitResources` handler gains parentRef existence checking, cycle detection, and category storage. The datastore gains a `CategoryTaxonomy` entity that parallels `Product` (UID, namespace, materialized ancestor path, spec+status JSON blobs). The GraphQL schema gains `CategoryTaxonomy`-backed fields additive to the existing `Category` type. No new services are introduced — all work extends `gitstore-api` (Go) and the existing `gitstore-git-service` (Rust) hook pipeline contract.

Controller reconciliation (FR-013 status computation) and File reference condition checking (FR-009 controller portion) are **explicitly out of scope** — deferred to GH#244, blocked on GH#40 + GH#165.

---

## Technical Context

**Language/Version**: Go 1.25 (`gitstore-api`); Rust edition 2021 MSRV 1.82 (`gitstore-git-service`)
**Primary Dependencies**: `gqlgen v0.17.90`, `go-playground/validator/v10 v10.30.3`, `github.com/adrg/frontmatter v0.2.0`, `gopkg.in/yaml.v3`, `go.uber.org/zap`, `google/uuid`, `gocqlx/v3 v3.0.4` (ScyllaDB prod), `go-memdb v1.3.5` (dev)
**Storage**: `go-memdb` (dev) / ScyllaDB 5.x+ (prod) via `Datastore` interface
**Testing**: `go test ./...`, `cargo test`
**Target Platform**: Linux server (Docker / bare metal)
**Project Type**: Multi-service web service (git-backed catalog API)
**Performance Goals**: Push validation < 5 seconds for 100-file push (SC-006); catalogue queries < 500ms
**Constraints**: Push pipeline is synchronous and fail-closed; admission is fire-and-forget
**Scale/Scope**: Up to 10,000 products; up to several hundred categories

---

## Constitution Check

*GATE: Must pass before Phase 0 research. Re-check after Phase 1 design.*

| Principle | Status | Notes |
|-----------|--------|-------|
| I. Test-First | **PASS** — all tasks order tests before implementation | Must be enforced in tasks.md |
| II. API-First | **PASS** — GraphQL schema and proto contracts defined in Phase 1 before resolvers | `category.graphqls` updated before resolver code |
| III. Clear Contracts | **PASS** — existing `Category` GraphQL type is extended additively; no breaking changes | `ValidateResources` / `AdmitResources` proto unchanged |
| IV. Observability | **PASS** — all new validation and admission paths must log via `zap.Logger` | Structured log fields: `kind`, `namespace`, `name`, `ancestor_path`, `cycle_detected` |
| V. User Story Driven | **PASS** — 4 user stories (P1/P1/P2/P3), all independently testable | |
| VI. Incremental | **PASS** — P1 (schema + hierarchy) ships independently of P2 (product constraint) and P3 (media) | |
| VII. Simplicity | **PASS** — no new services; minimal new abstractions | `validate.Parse` extended by kind-routing not a new abstraction layer |

No constitution violations.

---

## Project Structure

### Documentation (this feature)

```text
specs/021-category-taxonomy/
├── plan.md              # This file
├── research.md          # Phase 0 output
├── data-model.md        # Phase 1 output
├── quickstart.md        # Phase 1 output
├── contracts/           # Phase 1 output
│   ├── category.graphqls.diff   # Updated GraphQL schema additions
│   └── catalog_service.proto    # (no change — proto is kind-agnostic)
└── tasks.md             # Phase 2 output (/speckit.tasks command)
```

### Source Code (repository root)

```text
gitstore-api/
├── internal/
│   ├── catalog/
│   │   ├── product.go               # existing — ObjectMeta, ObjectReference, MediaDefinition reused
│   │   ├── category.go              # NEW — CategoryTaxonomyResource, CategoryTaxonomySpec, MediaDefinition alias
│   │   └── status.go                # EXTEND — CategoryTaxonomyStatus, CategoryTaxonomyConditionTypes
│   ├── validate/
│   │   ├── validator.go             # EXTEND — kind-routing: route to category validator when kind=CategoryTaxonomy
│   │   └── validator_test.go        # EXTEND — CategoryTaxonomy validation test cases
│   ├── cataloggrpc/
│   │   ├── server.go                # EXTEND — ValidateResources routes by kind; AdmitResources handles CategoryTaxonomy
│   │   └── server_test.go           # EXTEND — category admission + parentRef + cycle tests
│   └── datastore/
│       ├── entities.go              # EXTEND — add CategoryTaxonomy entity struct
│       ├── datastore.go             # EXTEND — add CategoryTaxonomy CRUD to Datastore interface
│       ├── memdb/
│       │   ├── schema.go            # EXTEND — category_taxonomy table + indexes
│       │   └── backend.go           # EXTEND — CategoryTaxonomy CRUD methods
│       └── scylla/
│           ├── migrations/
│           │   └── 003_category_taxonomy.cql   # NEW — category_taxonomy table migration
│           ├── models.go            # EXTEND — ScyllaCategoryTaxonomy model
│           └── backend.go           # EXTEND — CategoryTaxonomy CRUD methods

shared/schemas/
└── category.graphqls                # EXTEND — additive fields on Category type (labels, apiVersion, title, status)

gitstore-git-service/
└── (no changes — hook pipeline contract is kind-agnostic; Rust side unchanged)

tests/
└── e2e/
    └── category_taxonomy_test.go    # NEW — E2E push acceptance tests (US1–US3)
```

**Structure Decision**: Single existing project extension (no new modules). All work is in `gitstore-api`. The Rust `gitstore-git-service` is unchanged — the existing `ValidateResources` and `AdmitResources` RPCs are kind-agnostic and pass raw file blobs without caring about resource kind.

---

## Complexity Tracking

No violations requiring justification. The existing `Category` datastore entity (legacy flat struct) coexists with the new `CategoryTaxonomy` entity during this spec. A separate migration spec will consolidate them once GH#40 defines controller contracts.
