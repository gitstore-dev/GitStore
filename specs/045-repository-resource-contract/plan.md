# Implementation Plan: Repository Resource Contract

**Branch**: `045-repository-resource-contract` | **Date**: 2026-08-16 | **Spec**: [spec.md](spec.md)
**Input**: Feature specification from `/specs/045-repository-resource-contract/spec.md`

## Summary

Add an additive Kubernetes-style read contract to the existing GraphQL
`Repository` type with `apiVersion`, `kind`, shared system metadata, a
declarative `RepositorySpec` projection, and a non-null `RepositoryStatus`. Retain every
existing flat field and mutation contract, but mark duplicate legacy outputs
deprecated using the Namespace PR #345 migration pattern. Repository spec
includes persisted default branch and maximum-size policy values plus reserved,
deterministically defaulted visibility and extended-policy fields; status
includes common observed-state fields plus resolved storage values. Persist new
versioning and complete common status state on the existing
repository record and project existing maximum-size policy fields without
additional round trips.

## Technical Context

**Language/Version**: Go 1.25; GraphQL SDL generated with gqlgen v0.17.90

**Primary Dependencies**: `github.com/99designs/gqlgen`, `github.com/hashicorp/go-memdb`, `github.com/scylladb/gocqlx/v3`, `github.com/gocql/gocql`, `go.uber.org/zap`, `encoding/json`; shared `Long` and repository-policy GraphQL types from PR #345, verified against head `fefadbea951959c42a982d5e0d7824dbf175209c` or a merged descendant

**Storage**: Existing `Repository` record in go-memdb and ScyllaDB; add `generation`, `resource_version`, and `status` columns/fields, with no new table

**Testing**: Go `testing`, `testify`, resolver/service contract tests, memdb backend tests, Scylla integration tests, gqlgen generation validation

**Target Platform**: Linux server; Darwin/Linux developer environments

**Project Type**: GraphQL web service with dual datastore backends

**Performance Goals**: Preserve current single-row repository reads and pagination behavior; no additional datastore or git-service round trips

**Constraints**: Additive GraphQL evolution only; duplicate legacy output fields must be deprecated rather than removed; no Repository controller/watch/status mutation API; existing create/rename/transfer/delete behavior must remain unchanged; status/resolved must never be null for legacy rows

**Scale/Scope**: One GraphQL resource contract, one datastore entity, two backends, existing repository service/resolver paths, generated GraphQL models, and focused tests

## Constitution Check

*GATE: Must pass before Phase 0 research. Re-check after Phase 1 design.*

| Principle | Gate evaluation |
|---|---|
| I. Test-First Development | PASS — implementation tasks must add failing schema/converter/service/backend tests before production changes. |
| II. API-First Design | PASS — `contracts/repository.graphqls` defines the additive GraphQL contract before resolver work. |
| III. Clear Contracts & Versioning | PASS — all schema changes are additive; existing fields remain with explicit deprecation guidance and mutations remain intact. |
| IV. Observability & Debuggability | PASS — no new endpoint or failure class is introduced; existing structured logging remains on repository operations. |
| V. User Story Driven Development | PASS — the complete schema/converter contract is shared foundation; read projection and lifecycle semantics are parallel, independently testable stories. |
| VI. Incremental Delivery | PASS — the declarative read shape is independently useful and does not require future controller/watch work. |
| VII. Simplicity & YAGNI | PASS — reuses `ObjectMeta` and `Condition`, adds three persistence fields, and avoids a new table, controller, or mutation API. |

**Pre-design gate result**: PASS. No justified violations.

## Project Structure

### Documentation (this feature)

```text
specs/045-repository-resource-contract/
├── plan.md
├── research.md
├── data-model.md
├── quickstart.md
├── contracts/
│   └── repository.graphqls
└── tasks.md
```

### Source Code (repository root)

```text
shared/schemas/
└── repository.graphqls

gitstore-api/
├── internal/datastore/
│   ├── entities.go
│   ├── memdb/
│   │   ├── backend.go
│   │   └── backend_test.go
│   └── scylla/
│       ├── migrations/
│       │   ├── 001_initial_schema.cql
│       │   └── 002_secondary_indexes.cql
│       ├── models.go
│       ├── repository.go
│       └── backend_test.go
├── internal/graph/
│   ├── generated/
│   ├── model/models_gen.go
│   └── resolver/
│       ├── converters.go
│       ├── converters_test.go
│       ├── pagination.go
│       ├── repository.resolvers.go
│       ├── repository_resolver_test.go
│       └── service.go
└── generate.go
```

**Structure Decision**: Extend the existing GraphQL API and repository datastore model in place. The SDL remains in `shared/schemas`, gqlgen regenerates server/model code under `gitstore-api/internal/graph`, and both datastore backends keep the current repository table/index layout.

## Phase 0: Research Outcomes

Research is captured in [research.md](research.md). All technical unknowns are resolved:

- Add constant `apiVersion` and `kind` fields.
- Reuse shared `ObjectMeta`, `Condition`, `Long`, visibility, and policy-setting contracts.
- Keep all existing flat Repository fields with `@deprecated` guidance while adding non-null `metadata`, `spec`, and `status`.
- Put repository name in `metadata.name`; `RepositorySpec` contains persisted default branch/maximum-size values plus deterministic reserved visibility and extended-policy projections.
- Follow Namespace status conventions and place derived storage values under non-null `status.resolved`.
- Persist `Generation`, `ResourceVersion`, and a JSON common-status blob on `datastore.Repository`.
- Normalize legacy rows before reads and mutations so their canonical initial state is generation/resourceVersion `1` with empty conditions.
- Advance both counters for owner-spec changes (rename); advance only resourceVersion for transfer/system-only changes.
- Add a forward Scylla migration rather than rewriting the initial schema.

## Phase 1: Design and Contracts

### GraphQL contract

The authoritative proposed SDL is [contracts/repository.graphqls](contracts/repository.graphqls). `Repository` gains:

- `apiVersion: String!`
- `kind: String!`
- `metadata: ObjectMeta!`
- `spec: RepositorySpec!`
- `status: RepositoryStatus!`

`RepositorySpec` contains `defaultBranch`, `visibility`, and a non-null
Namespace-compatible `pushPolicy` shape. This feature projects `PRIVATE`
visibility and null extended override groups because it introduces no write
input or persistence for those reserved fields. `RepositoryStatus` contains
`observedGeneration`, `lastAppliedRevision`, shared conditions, and a non-null
`resolved` object for storage path/class. Existing fields remain populated and
gain deprecation reasons pointing to the new contract.

### Datastore and conversion design

- `datastore.Repository` gains `Generation int64`, `ResourceVersion string`, and `Status json.RawMessage` for observed generation, last-applied revision, and conditions.
- New repositories initialize to generation `1`, resourceVersion `"1"`, and `{"observedGeneration":0,"conditions":[]}`.
- A shared normalization helper treats missing legacy values as that canonical initial state. Mutation paths normalize before incrementing, so the first legacy rename becomes generation/resourceVersion `2`, while the first legacy transfer preserves generation `1` and advances resourceVersion to `2`.
- Scylla receives a new sequential migration adding the three nullable columns. Fresh and upgraded databases both run the same migration chain.
- Repository GraphQL conversion emits `gitstore.dev/v1beta1` / `Repository`,
  receives the namespace identifier needed for `metadata.namespace`, projects
  existing maximum-size limits into non-null `spec.pushPolicy`, and projects
  reserved visibility as `PRIVATE`; list pagination must pass the already-resolved
  namespace rather than perform per-row namespace lookups.
- Malformed stored status is surfaced through repository-standard logging and converted to the explicitly specified initial status; storage path/class remain derived from trusted repository fields.
- This feature emits no Repository-specific condition types. `conditions` is empty until a future writer defines its condition vocabulary and ownership contract.

### Test-first implementation order

1. Verify the pinned PR #345 shared definitions are present; stop with prerequisite guidance if they are absent.
2. Add failing shared-foundation tests for the complete SDL/deprecation contract, converter defaults, persistence, normalization, and transition helpers.
3. Update the SDL once, regenerate gqlgen once, and implement the shared datastore/converter foundation.
4. In parallel after the foundation, add failing US1 read-path/integration tests and failing US2 lifecycle/mutation-regression tests.
5. Implement US1 query/Node/list wiring independently from US2 create/rename/transfer version transitions.
6. Run focused story tests, then the API and integration suites.

### Post-design Constitution Check

| Principle | Post-design result |
|---|---|
| Test-First | PASS — explicit red-green order covers each changed layer. |
| API-First | PASS — SDL contract is complete before implementation. |
| Clear Contracts | PASS — additive fields, shared types, and compatibility rules are explicit. |
| Observability | PASS — malformed persisted status is logged rather than silently ignored. |
| User Story Driven | PASS — US1 read/flat compatibility and US2 identity/lifecycle behavior each complete independently after the shared foundation. |
| Incremental Delivery | PASS — no dependency on future Repository controllers or watches. |
| Simplicity | PASS — no new service boundary, table, dependency, or speculative policy model. |

**Post-design gate result**: PASS. No complexity exceptions required.

## Complexity Tracking

No constitution violations require justification.
