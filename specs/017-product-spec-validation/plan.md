# Implementation Plan: Product Spec/Status Validation Semantics and Integration Tests

**Branch**: `017-product-spec-validation` | **Date**: 2026-06-05 | **Spec**: [spec.md](spec.md)
**Input**: Feature specification from `/specs/017-product-spec-validation/spec.md`

## Summary

Extend the existing product validation pipeline (already covering struct-tag and pre-parse rules) with field-scoped multi-error reporting, tighten status hydration to guarantee no condition is silently dropped, and ship a full integration test suite plus documentation examples that can be parsed verbatim.

The two issues tracked here are:
- **#186** — validation semantics (FR-001 through FR-013): field-scoped errors, multi-error collection, status hydration contract.
- **#187** — integration tests and documentation (FR-014 through FR-017): full lifecycle tests, zero skips, parseable examples.

## Technical Context

**Language/Version**: Go 1.25 (`gitstore-api`)  
**Primary Dependencies**: `gqlgen v0.17.90`, `go-playground/validator/v10 v10.30.3`, `github.com/adrg/frontmatter v0.2.0`, `gopkg.in/yaml.v3`, `go.uber.org/zap`, `shopspring/decimal`  
**Storage**: `go-memdb v1.3.5` (dev) / `gocqlx/v3 v3.0.4` + `gocql` (ScyllaDB prod)  
**Testing**: `go test` with `testify`, build tag `-tags scylla` for Scylla-backed integration tests  
**Target Platform**: Linux server (Docker Compose and native)  
**Project Type**: web-service (GraphQL API)  
**Performance Goals**: git push validation < 5s; storefront query < 500ms at 1 000+ products  
**Constraints**: FR-015 — zero `t.Skip`/`t.Skipf` calls in integration suite  
**Scale/Scope**: up to 10 000 products, single-tenant initially

## Constitution Check

*GATE: Must pass before Phase 0 research. Re-check after Phase 1 design.*

| Principle | Status | Notes |
|-----------|--------|-------|
| I. Test-First | PASS | Tests written before implementation for all FRs; unit tests already exist for parser and converters; integration tests are a primary deliverable |
| II. API-First | PASS | GraphQL schema (`ProductStatus`, `ProductCondition`) was defined before status hydration code; no schema changes needed for this feature |
| III. Clear Contracts / Versioning | PASS | `catalog.gitstore.dev/v1beta1` is stable; additive-only; no breaking changes |
| IV. Observability | PASS | `converterLogger.Warn` already in converters.go; validation errors propagate through the git-receive-pack pipeline |
| V. User Story Driven | PASS | Four user stories defined (P1–P3); all tasks map to story labels |
| VI. Incremental Delivery | PASS | P1 (validation errors) is independently shippable; P2 (status hydration) adds operator value; P3 (integration tests + docs) completes the contract |
| VII. Simplicity / YAGNI | PASS | No new dependencies needed; `errors.Join` (stdlib) for multi-error; existing `validate.Parse` extended, not replaced |

**Complexity violations**: None — no new projects, no new external dependencies.

## Project Structure

### Documentation (this feature)

```text
specs/017-product-spec-validation/
├── plan.md              # This file (/speckit.plan command output)
├── research.md          # Phase 0 output (/speckit.plan command)
├── data-model.md        # Phase 1 output (/speckit.plan command)
├── quickstart.md        # Phase 1 output (/speckit.plan command)
├── contracts/           # Phase 1 output (/speckit.plan command)
└── tasks.md             # Phase 2 output (/speckit.tasks command - NOT created by /speckit.plan)
```

### Source Code (repository root)

```text
gitstore-api/
├── internal/
│   ├── validate/
│   │   ├── validator.go            # [EXTEND] multi-error preParseChecks, field-indexed messages
│   │   ├── validator_test.go       # [EXTEND] new rejection + multi-error test cases
│   │   └── testdata/
│   │       ├── macbook-pro-64gb-1tb-ssd-m4.md       # existing — valid
│   │       ├── invalid-title-too-long.md             # [ADD] FR-001 rejection example
│   │       ├── invalid-status-present.md             # [ADD] FR-007 rejection example
│   │       └── invalid-missing-fileref-name.md       # [ADD] FR-002 rejection example
│   ├── catalog/
│   │   ├── product.go              # [READ-ONLY for this feature]
│   │   └── status.go               # [READ-ONLY for this feature]
│   └── graph/
│       ├── converters.go           # [VERIFY] statusFromJSON unknown-condition passthrough
│       └── converters_test.go      # [EXTEND] Kubernetes-casing round-trip, unrecognised type passthrough
└── go.mod                          # no changes expected

tests/integration/                  # root-level separate Go module (github.com/gitstore-dev/gitstore/tests/integration)
├── main_test.go                    # existing — GIT_SERVER_GIT_URL / API_URL setup
├── product_lifecycle_test.go       # [ADD] FR-014 full lifecycle tests (push → query → status → query)
└── go.mod                          # existing — add gitstore-api as replace dependency if needed

docs/
└── products/
    ├── product-spec.md             # [ADD] field reference + valid example (FR-016/FR-017)
    └── examples/
        ├── valid-product.md        # [ADD] FR-016 valid example
        ├── invalid-status.md       # [ADD] FR-016 rejection: status present
        ├── invalid-title.md        # [ADD] FR-016 rejection: title too long
        └── invalid-media.md        # [ADD] FR-016 rejection: missing fileRef.name
```

**Structure Decision**: Go changes live in `gitstore-api/`. Integration tests go in the root-level `tests/integration/` module — a separate Go module already wired to run against live services over HTTP/GraphQL (not inside `gitstore-api/tests/`). Documentation goes in `docs/products/` (new subdirectory under the existing `docs/` tree).

## Complexity Tracking

No violations — this feature extends existing code paths without introducing new services, packages, or external dependencies.
