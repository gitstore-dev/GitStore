# Implementation Plan: Namespace/Repository Deletion Ordering and System Repository Bootstrap

**Branch**: `041-namespace-repo-finalizers` | **Date**: 2026-08-10 | **Spec**: [spec.md](./spec.md)
**Input**: Feature specification from `/specs/041-namespace-repo-finalizers/spec.md`

## Summary

`gitstore-api`'s `DeleteNamespace` and `DeleteRepository` mutations currently perform
no real precondition checks: `hasRepositories()` is a stub that always returns `false`
(`gitstore-api/internal/graph/resolver/service.go:336-339`), and `DeleteRepository` has
no check at all for existing catalog resources. `CreateNamespace` also never provisions
the well-known `gitstore-system` repository. This plan replaces the stub with a real
existence check against the already-present `RepositoryID` field on every catalog
entity (a direct memdb index lookup and a namespace-partition-scoped ScyllaDB query),
adds an equivalent existence check for repositories-in-namespace, and
adds `gitstore-system` repository auto-provisioning to `CreateNamespace`. All three
checks are synchronous, precondition-style rejections performed inline in the mutation
— matching ADR-0002/ADR-0003 steps 1-2 exactly. This plan explicitly does **not**
implement the ADRs' later async `Terminating`/`foregroundDeletion`-finalizer steps
(3-7), because neither `Namespace` nor `Repository` has a `Status` field or a
controller today, and adding either is out of scope per the spec's own Assumptions
(declarative schema is GH#170/#249; watch/reconcile loop is GH#174). See
research.md Decision 1 for the full rationale.

## Technical Context

**Language/Version**: Go 1.25 (`gitstore-api`)
**Primary Dependencies**: `github.com/99designs/gqlgen` (existing GraphQL resolver/mutation layer), `go.uber.org/zap` (existing structured logging), `github.com/vektah/gqlparser/v2/gqlerror` (existing error type for mutation rejections)
**Storage**: `go-memdb` (dev) and ScyllaDB (prod) — existing `datastore.Datastore` interface (`gitstore-api/internal/datastore/datastore.go`); no new storage technology
**Testing**: Go `testing` + existing integration test harness (`tests/integration/`, `newPushHelper()`/`getEnv()` fixtures) and existing contract test pattern (`gitstore-api/tests/contract/`)
**Target Platform**: Linux server (existing `gitstore-api` deployment)
**Project Type**: Backend service — GraphQL API mutation/resolver layer within the existing `gitstore-api` module
**Performance Goals**: Existence checks must avoid full-table scans and counts. memdb uses direct indexes; ScyllaDB binds the existing namespace partition and applies `LIMIT 1` while filtering by repository ID within that partition.
**Constraints**: Must not introduce a new `Status`/finalizer state machine for Namespace or Repository (out of scope per spec Assumptions); must not change the `Namespace` or `Repository` datastore entity's existing fields in a breaking way; existence checks must work correctly against both `go-memdb` and ScyllaDB backends
**Scale/Scope**: 3 mutations changed (`DeleteNamespace`, `DeleteRepository`, `CreateNamespace`); 2 new indexed existence-check datastore methods; no new resource kinds, no new GraphQL types

## Constitution Check

*GATE: Must pass before Phase 0 research. Re-check after Phase 1 design.*

- **I. Test-First Development (NON-NEGOTIABLE)**: PASS (with plan). Each of the three
  user stories gets contract/integration tests written first, verified to fail against
  the current stub/missing-check behavior, then implementation follows. This is
  tracked explicitly in tasks.md phase ordering (not yet generated at plan time).
- **II. API-First Design**: PASS. No new GraphQL schema types are introduced; the
  three affected mutations (`deleteNamespace`, `deleteRepository`, `createNamespace`)
  already have stable GraphQL contracts (input/payload types unchanged). Only their
  resolver-level behavior changes. The two new datastore existence-check methods are
  internal `Datastore` interface additions, reviewed as part of this plan's
  data-model.md rather than a separate GraphQL contract.
- **III. Clear Contracts & Versioning**: PASS. This is a bug-fix/completion of
  documented-but-unimplemented behavior (ADR-0002 §Delete, ADR-0003 §Delete), not a
  breaking change to any existing contract. Error messages for rejected deletions are
  new `gqlerror` outputs on paths that previously always succeeded — additive from the
  caller's perspective (a previously-succeeding unsafe call now correctly fails; no
  previously-failing call starts succeeding differently).
- **IV. Observability & Debuggability**: PASS (with plan). Every rejection path and
  every `gitstore-system` auto-provisioning attempt must emit structured log entries
  via the existing `s.logger` (zap) pattern already used in `service.go`, consistent
  with existing `CreateNamespace`/`DeleteRepository` logging.
- **V. User Story Driven Development**: PASS. Spec already defines 3 independently
  testable, priority-ordered user stories (P1, P1, P2); this plan and the eventual
  tasks.md preserve that structure.
- **VI. Incremental Delivery**: PASS. User Story 1 (namespace deletion safety) and
  User Story 2 (repository deletion safety) are independently shippable and do not
  depend on User Story 3 (system repository bootstrap) or on each other.
- **VII. Simplicity & YAGNI**: PASS. Explicitly rejects building the fuller
  ADR-described async `Terminating`/finalizer state machine in favor of the minimal
  synchronous precondition-check that satisfies the spec's actual requirements — see
  research.md Decision 1. No new dependencies, no new services, no speculative
  abstraction for hypothetical future resource kinds.

No violations requiring Complexity Tracking justification.

## Project Structure

### Documentation (this feature)

```text
specs/041-namespace-repo-finalizers/
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
│   ├── graph/
│   │   └── resolver/
│   │       ├── service.go              # DeleteNamespace, DeleteRepository, CreateNamespace — behavior changes here
│   │       ├── namespace.resolvers.go   # DeleteNamespace GraphQL resolver — unchanged signature, calls into service.go
│   │       └── repository.resolvers.go  # DeleteRepository GraphQL resolver — unchanged signature, calls into service.go
│   └── datastore/
│       ├── datastore.go                 # Datastore interface — add HasRepositories/HasCatalogResources-style methods
│       ├── instrumented.go              # Metrics-wrapping decorator — must forward new methods
│       ├── memdb/backend.go             # go-memdb implementation of the new existence-check methods
│       └── scylla/backend.go            # ScyllaDB implementation of the new existence-check methods (+ repository.go if repo-scoped)
└── internal/testutil/stubstore.go       # Test double — must implement new Datastore methods

tests/
├── contract/                            # gitstore-api/tests/contract/ — mutation-level contract tests for reject/accept paths
└── integration/                         # tests/integration/ — end-to-end namespace/repository/catalog-resource lifecycle tests
```

**Structure Decision**: Single project, additive change within the existing
`gitstore-api` module. No new services, packages, or directories are created. All
changes land in the existing `internal/graph/resolver` (mutation behavior) and
`internal/datastore` (new existence-check methods across both backend
implementations) packages, following the general `Datastore` interface + per-backend
implementation layering the codebase already uses for every other query
(`ListRepositoriesByNamespace` is the closest existing analog). Note: research.md
Decision 2 found that no comparable delete-precondition check actually exists in code
today for CategoryTaxonomy or File, despite ADR-0006/ADR-0008 describing one — this
spec is establishing the first real implementation of this pattern, not reusing an
existing one.

## Complexity Tracking

No Constitution Check violations. This section is not applicable.

## Post-Design Constitution Re-Check

*Re-evaluated after Phase 1 design (research.md, data-model.md, contracts/,
quickstart.md).*

- **I. Test-First Development**: Still PASS. `data-model.md` and `contracts/deletion-preconditions.md`
  now define exact test-case tables per mutation (reject-path and accept-path for
  each), giving tasks.md concrete contract/integration test targets to write first.
- **II. API-First Design**: Still PASS. Confirmed via direct schema read
  (`shared/schemas/namespace.graphqls`, `shared/schemas/repository.graphqls`) that no
  GraphQL type/input/payload changes are needed — `deleteNamespace`'s docstring
  already states the intended precondition behavior this feature makes real.
- **III. Clear Contracts & Versioning**: Still PASS. No schema version bump needed.
- **IV. Observability & Debuggability**: Still PASS. `contracts/deletion-preconditions.md`'s
  cross-cutting note confirms the new rejection paths reuse the existing `gqlerror.Errorf`
  mechanism uniformly — no new error taxonomy to document separately.
- **V. User Story Driven Development**: Still PASS.
- **VI. Incremental Delivery**: Still PASS.
- **VII. Simplicity & YAGNI**: Still PASS, and reinforced by research.md Decision 3
  (existence checks, not denormalized counters) and Decision 5 (no new transactional/
  locking primitives) — both explicitly chose the simpler option consistent with the
  codebase's existing concurrency posture.

No new violations introduced by the Phase 1 design. Gate remains PASS.
