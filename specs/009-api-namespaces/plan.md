# Implementation Plan: API-Driven Namespace Lifecycle Management

**Branch**: `009-api-namespaces` | **Date**: 2026-05-15 | **Spec**: [spec.md](spec.md)  
**Input**: Feature specification from `/specs/009-api-namespaces/spec.md`

**Note**: This plan was filled in by the `/speckit.plan` command.

## Summary

Introduce namespace lifecycle management (create, list, get, delete) to `gitstore-api` via GraphQL, backed by the existing datastore abstraction from feature 006. Namespaces are globally-unique isolation boundaries with three tiers (user, organisation, enterprise). Authorization uses the existing `IsAdmin` JWT claim as the "elevated platform role". No changes to `gitstore-git-service`.

## Technical Context

**Language/Version**: Go 1.25 (`gitstore-api`)  
**Primary Dependencies**: `gqlgen v0.17.90`, `go-memdb v1.3.5`, `gocqlx/v3 v3.0.4` (ScyllaDB), `go-playground/validator/v10`, `go.uber.org/zap`, `google/uuid`  
**Storage**: `go-memdb` (development / in-memory backend) / ScyllaDB 5.x+ (production backend) — via the `datastore.Datastore` interface from feature 006  
**Testing**: `go test`, `testify/assert`, `testcontainers-go` (ScyllaDB contract tests)  
**Target Platform**: Linux server  
**Project Type**: Web service (GraphQL API)  
**Performance Goals**: Namespace create/list/get in < 1s under normal load (SC-001); 100% deterministic conflict and success responses (SC-002, SC-004)  
**Constraints**: No changes to `gitstore-git-service` (FR-011); namespace logic lives exclusively in `gitstore-api`  
**Scale/Scope**: Initial platform setup — namespace count in the hundreds; no pagination needed for list in this spec

## Constitution Check

*GATE: Must pass before Phase 0 research. Re-check after Phase 1 design.*

| Principle                         | Status | Notes                                                                                                                                     |
|-----------------------------------|--------|-------------------------------------------------------------------------------------------------------------------------------------------|
| I. Test-First Development         | ✅ PASS | Integration tests specified in FR-012; acceptance scenarios drive test-first task ordering                                                |
| II. API-First Design              | ✅ PASS | GraphQL contract (`namespace.graphql`) defined before implementation; datastore interface extended before backend code                    |
| III. Clear Contracts & Versioning | ✅ PASS | GraphQL schema is additive (new types/fields only); no breaking changes to existing schema; datastore interface extended                  |
| IV. Observability                 | ✅ PASS | `InstrumentedDatastore` wrapper automatically covers namespace operations once interface is extended; resolver errors logged via zap      |
| V. User Story Driven              | ✅ PASS | Three user stories (create, list/get, delete) with P1/P2/P3 priority and independent test criteria                                        |
| VI. Incremental Delivery          | ✅ PASS | P1 (create) delivers immediate value; P2 (list/get) and P3 (delete) are independently deployable                                          |
| VII. Simplicity                   | ✅ PASS | No new IAM system; reuses `IsAdmin` as elevated role; no new framework dependencies; deletion guard is a stub pending the repository spec |

**Post-design re-check** (after Phase 1):
- API contract defined in `contracts/namespace.graphql` before implementation ✅
- Data model defined in `data-model.md` with all validation rules ✅
- No new dependencies introduced ✅
- Complexity justified: `parentEnterpriseIdentifier` lookup on create adds one extra datastore read; justified by FR-001 and spec clarification Q5 ✅

## Project Structure

### Documentation (this feature)

```text
specs/009-api-namespaces/
├── plan.md              # This file (/speckit.plan command output)
├── research.md          # Phase 0 output (/speckit.plan command)
├── data-model.md        # Phase 1 output (/speckit.plan command)
├── quickstart.md        # Phase 1 output (/speckit.plan command)
├── contracts/
│   ├── namespace.graphql        # GraphQL schema contract (Phase 1)
│   └── datastore-extension.go  # Datastore interface extension (Phase 1)
└── tasks.md             # Phase 2 output (/speckit.tasks command - NOT created by /speckit.plan)
```

### Source Code (repository root)

```text
shared/schemas/
├── schema.graphqls       # MODIFY: add namespace queries/mutations to Query + Mutation roots
└── namespace.graphqls    # CREATE: Namespace type, NamespaceTier enum, inputs, payloads

gitstore-api/
├── internal/
│   ├── datastore/
│   │   ├── entities.go            # ADD: Namespace struct, NamespaceTier type
│   │   ├── datastore.go           # EXTEND: add 5 namespace methods to Datastore interface
│   │   ├── instrumented.go        # EXTEND: add instrumented wrappers for 5 namespace methods
│   │   ├── memdb/
│   │   │   ├── schema.go          # EXTEND: add "namespaces" table schema
│   │   │   └── backend.go         # EXTEND: implement 5 namespace methods
│   │   └── scylla/
│   │       ├── backend.go         # EXTEND: implement 5 namespace methods + namespaceRow struct
│   │       └── migrations/
│   │           └── 002_namespaces.cql  # CREATE: namespaces table + identifier index
│   ├── graph/
│   │   ├── namespace.resolvers.go # CREATE (gqlgen-generated stub, then implemented)
│   │   ├── service.go             # EXTEND: add NamespaceService methods
│   │   └── model/
│   │       └── models_gen.go      # AUTO-GENERATED by gqlgen (do not edit manually)
│   └── graph/generated/
│       └── generated.go           # AUTO-GENERATED by gqlgen (do not edit manually)
└── tests/
    └── contract/
        ├── datastore/
        │   ├── contract_test.go   # EXTEND: add namespace contract tests
        │   ├── memdb_test.go      # EXTEND: namespace memdb tests
        │   └── scylla_test.go     # EXTEND: namespace scylla tests
        └── graphql/               # CREATE: namespace GraphQL integration tests
            └── namespace_test.go
```

**Structure Decision**: Single-service extension. All changes are within `gitstore-api`. The `shared/schemas/` directory holds the GraphQL schema files consumed by gqlgen — a new `namespace.graphqls` is added there and the roots in `schema.graphqls` are extended. `gitstore-git-service` is untouched (FR-011).

## Complexity Tracking

No constitution violations. The only non-trivial complexity is the `parentEnterpriseIdentifier` lookup on create (an extra datastore read to resolve identifier → ID), which is justified by FR-001 and the spec clarification that parent enterprise is declared at creation time.
