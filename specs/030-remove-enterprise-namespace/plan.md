# Implementation Plan: Namespace Types — Remove Enterprise

**Branch**: `030-remove-enterprise-namespace` | **Date**: 2026-06-19 | **Spec**: [spec.md](spec.md)  
**Input**: Feature specification from `specs/030-remove-enterprise-namespace/spec.md`

## Summary

Remove `ENTERPRISE` as a valid `NamespaceTier` value across the entire `gitstore-api` stack and rename `ORGANISATION` → `ORGANIZATION` (American spelling) in all user-facing surfaces. After this change the namespace type contract accepts exactly two values — `USER` and `ORGANIZATION` — and any request supplying `ENTERPRISE` is rejected at the GraphQL schema layer before reaching the resolver. The `parent_enterprise_id` column is dropped from Scylla via a new migration. The change is scoped to `gitstore-api` (Go) and shared schemas; the git service (Rust), proto definitions, admin UI, and controller manager are unaffected.

## Technical Context

**Language/Version**: Go 1.25 (`gitstore-api`)  
**Primary Dependencies**: `gqlgen v0.17.90` (code generation), `gocqlx/v3` + `gocql` (Scylla datastore), `go-playground/validator/v10`, `go.uber.org/zap`  
**Storage**: ScyllaDB 5.x+ in production; `go-memdb` in development  
**Testing**: `go test ./...` (unit + integration); `tests/contract/datastore/` (contract tests)  
**Target Platform**: Linux server (API service)  
**Project Type**: GraphQL web service  
**Performance Goals**: No change — this is a pure removal  
**Constraints**: `parent_enterprise_id` Scylla column dropped via `ALTER TABLE DROP` (safe — no production data); generated files regenerated via `make generate`, never hand-edited  
**Scale/Scope**: Single service (`gitstore-api`); touches one enum, two struct fields, one schema file, resolver logic, converters, and tests

## Constitution Check

| Principle                         | Status                                                                                                                                                            | Notes                                           |
|-----------------------------------|-------------------------------------------------------------------------------------------------------------------------------------------------------------------|-------------------------------------------------|
| I. Test-First Development         | REQUIRED — regression test `TestCreateNamespace_enterpriseTier_rejected` must be written and verified failing before removing the enterprise tier from the schema | Tests task precedes schema/implementation tasks |
| II. API-First Design              | PASS — GraphQL schema (`shared/schemas/namespace.graphqls`) is updated first; generated code follows via `make generate`                                          |
| III. Clear Contracts & Versioning | PASS — `ENTERPRISE` was never in a stable public release; removal is not a breaking change under semver                                                           |
| IV. Observability & Debuggability | PASS — no new log points required; validation errors already propagate structured error context                                                                   |
| V. User Story Driven              | PASS — three user stories map to three task groups                                                                                                                |
| VI. Incremental Delivery          | PASS — P1 (API contract) can be deployed independently; P2 (docs) and P3 (regression test) follow                                                                 |
| VII. Simplicity & YAGNI           | PASS — removing complexity, not adding it; `parent_enterprise_id` column dropped cleanly via `ALTER TABLE DROP` with no data loss                                 |

## Project Structure

### Documentation (this feature)

```text
specs/030-remove-enterprise-namespace/
├── plan.md              # This file
├── spec.md              # Feature specification
├── research.md          # Phase 0 research findings
├── data-model.md        # Phase 1 entity definitions
├── quickstart.md        # Phase 1 usage guide
├── contracts/
│   └── namespace.graphqls   # Target state of the GraphQL schema
├── checklists/
│   └── requirements.md
└── tasks.md             # Phase 2 output (/speckit.tasks — not yet created)
```

### Source Code (repository root)

```text
shared/schemas/
└── namespace.graphqls          # ← primary schema change (remove ENTERPRISE, rename ORGANISATION→ORGANIZATION, remove parentEnterpriseId/parentEnterpriseIdentifier)

gitstore-api/
├── internal/
│   ├── datastore/
│   │   ├── entities.go                        # Remove NamespaceTierEnterprise + ParentEnterpriseID; rename NamespaceTierOrganisation→NamespaceTierOrganization
│   │   └── scylla/
│   │       ├── models.go                      # Remove ParentEnterpriseID from namespaceRow + column list
│   │       └── backend.go                     # Remove "parent_enterprise_id" from column list
│   └── graph/
│       ├── model/
│       │   └── models_gen.go                  # Regenerated (make generate)
│       ├── generated/
│       │   ├── namespace.generated.go         # Regenerated (make generate)
│       │   └── root_.generated.go             # Regenerated (make generate)
│       └── resolver/
│           ├── service.go                     # Remove enterprise tier logic; update tier converter to NamespaceTierOrganization
│           ├── converters.go                  # Remove enterprise cases; update NamespaceTierOrganization mapping; remove ParentEnterpriseID
│           └── namespace_service_test.go      # Replace enterprise tests with rejection regression test
└── tests/
    └── contract/
        └── datastore/
            └── contract_test.go               # Remove enterprise namespace fixture

docs/
├── architecture.md       # Update namespace tier table, remove parentEnterpriseId from examples
├── api-reference.md      # Remove ENTERPRISE from tier table, remove parentEnterpriseIdentifier field docs
└── resources/
    └── git-backed.md     # Remove parentEnterpriseRef from namespace resource spec
```

**Structure Decision**: Single-project (`gitstore-api`) with a shared schema file. No new directories required.

## Complexity Tracking

No constitution violations. This feature removes complexity rather than adding it.
