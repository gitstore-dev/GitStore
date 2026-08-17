# Implementation Plan: Namespace Resource Contract

**Branch**: `044-namespace-spec-status-schema` | **Date**: 2026-08-16 | **Spec**: [spec.md](spec.md)
**Input**: Feature specification from `/specs/044-namespace-spec-status-schema/spec.md`

## Summary

Add a versioned Kubernetes-style `apiVersion`/`kind`/`metadata`/`spec`/`status` envelope to the GraphQL Namespace output while retaining the previous flat fields as deprecated compatibility fields. The implementation will define a dedicated namespace-less metadata type, typed repository and push-policy defaults, shared condition-based status, and deterministic read-time hydration from the existing flat Namespace datastore row. This phase does not add Git write delegation, admission, watch behavior, policy resolution, or datastore migrations; those remain with GH#172, GH#173, GH#174, and GH#249.

## Technical Context

**Language/Version**: Go 1.25 (`gitstore-api`); GraphQL SDL
**Primary Dependencies**: `github.com/99designs/gqlgen v0.17.90`, existing GraphQL scalar helpers, `go-memdb v1.3.5`, `gocqlx/v3 v3.0.4`, `go.uber.org/zap`, standard library `time`/`strconv`; no new dependency
**Storage**: Existing Namespace rows in memdb development storage and ScyllaDB production storage; no migration or new persisted field in this feature
**Testing**: Go contract/unit/integration tests, gqlgen generation checks, root `make test`/`make build`/`make pr-ready`
**Target Platform**: Linux server and Darwin development hosts supported by the existing Go API
**Project Type**: GraphQL web service contract and read-model projection
**Performance Goals**: No feature-specific latency target; Namespace hydration is a constant-time projection and remains covered by existing API performance gates
**Constraints**: API-first and test-first; generated gqlgen files are never edited directly; Namespace has no owning namespace; GraphQL `ObjectMeta.namespace` remains non-null for namespace-scoped resources; existing flat Namespace fields remain deprecated until a future major GraphQL API release; write/status/watch semantics remain out of scope
**Scale/Scope**: All existing Namespace rows and all in-repository Namespace GraphQL consumers; no change to product/repository scale limits or service topology

## Constitution Check

*GATE: Must pass before Phase 0 research. Re-checked after Phase 1 design.*

### Pre-design gate

| Principle                         | Result                         | Plan evidence                                                                                                                                                                                                                                                            |
|-----------------------------------|--------------------------------|--------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------|
| I. Test-First Development         | PASS                           | Schema, converter, resolver, scalar, and integration contract tests are written and observed failing before implementation.                                                                                                                                              |
| II. API-First Design              | PASS                           | `contracts/namespace.graphqls` and the reviewed SDL are the source of truth before gqlgen regeneration or resolver changes.                                                                                                                                              |
| III. Clear Contracts & Versioning | PASS | The declarative fields are additive, existing flat fields are deprecated with migration guidance, and removal is reserved for a future major GraphQL API release. |
| IV. Observability & Debuggability | PASS                           | No new endpoint or asynchronous operation is added. Conversion and scalar failures return explicit GraphQL errors; no silent fallback represents invalid input.                                                                                                          |
| V. User Story Driven Development  | PASS                           | Work maps to US1 schema separation, US2 identity/versioning consistency, and US3 coordinated Alpha consumer migration.                                                                                                                                                   |
| VI. Incremental Delivery          | PASS                           | The contract/read projection unblocks later write, admission, and watch stories without implementing them prematurely.                                                                                                                                                   |
| VII. Simplicity & YAGNI           | PASS                           | No datastore migration, new service, controller, cache, queue, or external dependency is introduced.                                                                                                                                                                     |

**Gate result**: PASS.

### Post-design gate

Phase 1 design preserves the pre-design result:

- dedicated `NamespaceMetadata` avoids weakening shared `ObjectMeta`;
- typed policy defaults avoid an opaque JSON contract;
- read-time defaults avoid premature persistence semantics;
- a small local `Long` scalar is the only new infrastructure and introduces no dependency;
- GraphQL SDL, data model, validated examples, and tests remain the contract sources;
- deprecated flat fields preserve incremental delivery and public-interface versioning.

**Post-design result**: PASS.

## Project Structure

### Documentation (this feature)

```text
specs/044-namespace-spec-status-schema/
├── plan.md
├── research.md
├── data-model.md
├── quickstart.md
├── contracts/
│   ├── namespace.graphqls
│   └── namespace-manifest.md
└── tasks.md                         # created later by /speckit-tasks
```

### Source Code (repository root)

```text
shared/schemas/
├── namespace.graphqls               # authoritative API Namespace types
└── schema.graphqls                  # Long scalar declaration if shared scalar SDL lives here

gitstore-api/
├── gqlgen.yml                       # Long scalar/type bindings
├── internal/graph/
│   ├── scalar/scalars.go            # int64 Long marshal/unmarshal
│   ├── resolver/
│   │   ├── converters.go            # flat row -> declarative Namespace hydration
│   │   ├── converters_test.go
│   │   ├── namespace.resolvers.go   # return hydrated Namespace output
│   │   └── namespace_service_test.go
│   ├── model/models_gen.go           # regenerated
│   └── generated/
│       ├── namespace.generated.go    # regenerated
│       └── schema.generated.go       # regenerated
└── internal/datastore/
    ├── entities.go                   # retained flat Namespace persistence model
    ├── memdb/                        # no schema change
    └── scylla/                       # no migration

tests/integration/
└── namespace_contract_test.go        # declarative read and default-status coverage

docs/namespace/
└── namespace-spec.md                 # canonical contract, mutability matrix, examples

gitstore-admin/
└── src/graphql/generated.ts          # regenerated/updated if admin codegen consumes Namespace
```

**Structure Decision**: Keep implementation in the existing Go GraphQL API and shared SDL. The datastore remains the legacy flat persistence source during GH#171; the resolver converter is the sole projection boundary. Generated Go/TypeScript files follow their existing generators and are not hand-edited.

## Phase 0: Research Outcomes

Research decisions are recorded in [research.md](research.md):

1. schema/read hydration only; behavior remains in downstream issues;
2. dedicated `NamespaceMetadata` preserves namespace-scoped `ObjectMeta` invariants;
3. no memdb/Scylla migration;
4. deterministic `"1"` resource-version and generation defaults for pre-existing rows;
5. Relay `id` remains alongside `metadata.uid`;
6. typed partial policy defaults and a local GraphQL `Long` scalar;
7. shared condition status without `phase` or `resolved`;
8. additive declarative fields, deprecated flat fields, and major-version removal boundaries;
9. SDL/test-first gqlgen workflow;
10. no new runtime dependency.

All technical unknowns are resolved; no `NEEDS CLARIFICATION` remains.

## Phase 1: Design and Contracts

### Data model

[data-model.md](data-model.md) defines:

- the GraphQL Namespace envelope and dedicated metadata type;
- author-controlled spec and typed default-policy objects;
- system-controlled status and shared conditions;
- legacy-row hydration mappings and default values;
- mutability, ownership, nullability, and deferred state transitions.

### Interface contracts

- [contracts/namespace.graphqls](contracts/namespace.graphqls): reviewable GraphQL contract for the replacement Namespace output types and `Long` scalar.
- [contracts/namespace-manifest.md](contracts/namespace-manifest.md): author/system ownership rules and canonical create/update frontmatter.
- [quickstart.md](quickstart.md): test-first implementation order, code generation, targeted validation, and expected query shape.

### Implementation sequence

1. Add failing GraphQL introspection/contract tests that assert the envelope, metadata omission, status defaults, policy types, deprecated flat fields, and the unchanged non-null `ObjectMeta.namespace` invariant; validate condition vocabulary through the documentation contract.
2. Add failing converter/resolver tests for existing and newly created flat Namespace rows.
3. Add failing `Long` scalar boundary/error tests.
4. Update the shared Namespace SDL and scalar mapping once, then run gqlgen once.
5. Implement deterministic Namespace hydration plus deprecated flat-field projections in the converter and wire resolver outputs through it.
6. Publish `docs/namespace/namespace-spec.md` and add an automated documentation-contract test for its create/update manifests.
7. Add integration coverage for read/list/create payloads returning both the preferred declarative fields and deprecated compatibility fields.
8. Migrate in-repository GraphQL queries, generated admin types, and affected tests to the declarative fields without removing the deprecated schema fields.
9. Run targeted tests, code generation consistency checks, aggregate tests/build, and `make pr-ready`.

## Complexity Tracking

No constitution violations or complexity exceptions are required.
