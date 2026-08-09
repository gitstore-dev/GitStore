# Implementation Plan: CategoryTaxonomy Controller Reconciliation

**Branch**: `039-category-taxonomy-reconciler` | **Date**: 2026-08-08 | **Spec**: [spec.md](./spec.md)
**Input**: Feature specification from `/specs/039-category-taxonomy-reconciler/spec.md`

## Summary

Implement a `CategoryTaxonomy` reconciler in `gitstore-controller-manager` that computes hierarchy `depth`/`path`/`childCount`/`productCount`, maintains `ParentResolved`/`Acyclic`/`Ready` conditions, and surfaces an unresolved-required-file condition — writing all of it back through the status-subresource mutation contract delivered by spec 040 (`updateCategoryStatus`, watch/list via `watchCategories`/`categories`). Because spec 040 shipped the server-side GraphQL contract but deferred the `gitstore-controller-manager`-side client adapters (no GraphQL/WebSocket client dependency exists in that module yet), this plan's first phase adds that dependency and the two concrete adapters (`CategoryTaxonomyListWatcher`, `graphqlStatusClient`) as a prerequisite, then builds the reconciler itself against the already-defined `Reconciler`/`StatusClient` interfaces from specs 025/026.

## Technical Context

**Language/Version**: Go 1.25 (`gitstore-controller-manager`)
**Primary Dependencies**: existing `internal/types.Reconciler`/`ReconcileResult` (spec 026), existing `internal/status.StatusClient`/`StatusPatch` (spec 026, extended by spec 040 with `Resolved json.RawMessage`), existing `internal/listwatch.ListWatcher[T]`/`Watcher[T]`/`Runner[T]` (spec 036), existing `internal/cache.Cache[T]`/`CacheAccessor[T]`; **new**: a GraphQL client capable of driving `POST /graphql` queries/mutations and the `graphql-transport-ws` WebSocket subscription protocol against `gitstore-api`'s already-wired `transport.Websocket` (spec 040) — no such dependency exists in `gitstore-controller-manager/go.mod` today; concrete library choice is a Phase 0 research decision
**Storage**: No new storage in `gitstore-controller-manager` (in-memory cache only, per spec 026's existing pattern). On the `gitstore-api` side, reuses the existing `status` JSON blob column on `CategoryTaxonomy` and the `catalog.ResolvedCategoryTaxonomy.Path []string` field (already renamed by spec 040 R9) — no schema or datastore changes required by this spec.
**Testing**: `go test ./...` (Go table/contract/integration tests), following the existing `gitstore-controller-manager/tests/{contract,integration}/` structure; a depth-3 hierarchy test, a cycle-detection test, and a required-file-reference test are explicitly required by FR-015
**Target Platform**: Linux server (existing deployment target)
**Project Type**: Backend service extension (controller-manager reconciler + one new datastore-client package) — no frontend/mobile component
**Performance Goals**: No new performance goal; reconciliation is level-triggered and tolerant of at-least-once, delayed delivery per spec 026. SC-001 requires "within one reconciliation cycle," not a specific latency bound.
**Constraints**: MUST NOT recompute `path`/`depth` for cycle participants (FR-008); MUST suppress redundant status writes under steady state (FR-013, SC-004); MUST NOT block or reject a push based on the required-file-reference check (FR-010, SC-003); MUST NOT read/write the separate, pre-existing `ancestor_path` datastore column or `Category.path`/`Category.depth` GraphQL fields (spec 021/admission pipeline) — this feature is scoped entirely to `status.resolved`, a field never previously written by any code path (confirmed in spec 040 research.md R10)
**Scale/Scope**: One resource kind (`CategoryTaxonomy`). Depth is unbounded (no fixed ceiling per Edge Cases) but no specific scale target is given beyond the existing constitution scale constraints (up to 1,000,000 products; category hierarchies are expected to be far smaller than the product catalogue).

## Constitution Check

*GATE: Must pass before Phase 0 research. Re-check after Phase 1 design.*

- **I. Test-First Development (NON-NEGOTIABLE)**: PASS (planned) — Phase 2 tasks will require contract/unit tests for the reconciler's hierarchy computation, condition logic, and the new `graphqlStatusClient`/`CategoryTaxonomyListWatcher` adapters, plus the three integration tests FR-015 explicitly mandates (depth-3 hierarchy, cycle detection, file-ref condition), all written and confirmed failing before implementation.
- **II. API-First Design**: PASS — the GraphQL contract this reconciler depends on (`watchCategories`, `updateCategoryStatus`) was already defined and shipped by spec 040 before this plan; no new server-side contract is introduced here.
- **III. Clear Contracts & Versioning**: PASS — no schema changes; this spec only adds a client and a reconciler consuming an existing, additive contract.
- **IV. Observability & Debuggability**: PASS (planned) — FR-016/SC-005 require a reconciliation-lag runbook; the existing `gitstore-controller-manager/internal/health` metrics (`QueueDepth`, `StalledWorkers`, `ReconcileTotal`) already cover generic lag signals per `docs/runbooks/controller-lag.md` — this spec's runbook work is to confirm those signals apply to the `CategoryTaxonomy` kind and document any kind-specific interpretation (e.g. distinguishing a cycle-blocked node from a genuinely stalled one).
- **V. User Story Driven Development**: PASS — spec defines P1 (hierarchy accuracy), P2 (condition trustworthiness), P3 (file-ref visibility) stories; tasks will carry [US1]/[US2]/[US3] labels.
- **VI. Incremental Delivery**: PASS — US1 (hierarchy) alone is a viable MVP (a reconciler that only computes depth/path/counts is useful even before conditions or file-ref checking exist); US2 and US3 layer on top without disrupting US1.
- **VII. Simplicity & YAGNI**: PASS — reuses every existing controller-manager primitive (`Reconciler`, `StatusClient`, `Cache`, `Runner[T]`) rather than inventing new ones; the one new dependency (a GraphQL client) is the minimum required to consume the contract spec 040 already defined, not a speculative addition.

No violations requiring the Complexity Tracking table.

## Project Structure

### Documentation (this feature)

```text
specs/039-category-taxonomy-reconciler/
├── plan.md              # This file (/speckit.plan command output)
├── research.md          # Phase 0 output (/speckit.plan command)
├── data-model.md        # Phase 1 output (/speckit.plan command)
├── quickstart.md        # Phase 1 output (/speckit.plan command)
├── contracts/           # Phase 1 output (/speckit.plan command)
└── tasks.md             # Phase 2 output (/speckit.tasks command - NOT created by /speckit.plan)
```

### Source Code (repository root)

```text
gitstore-controller-manager/
├── internal/
│   ├── graphqlclient/                          # new: minimal GraphQL client (query/mutation POST + graphql-transport-ws subscription dial)
│   │   ├── client.go
│   │   └── client_test.go
│   ├── listwatch/
│   │   ├── graphql_listwatcher.go               # new: CategoryTaxonomyListWatcher satisfying ListWatcher[T]/Watcher[T] (spec 036 interfaces)
│   │   └── graphql_listwatcher_test.go
│   ├── status/
│   │   ├── graphql_status_client.go             # new: graphqlStatusClient satisfying StatusClient (spec 026 interface)
│   │   └── graphql_status_client_test.go
│   └── categorytaxonomy/                        # new: the reconciler itself
│       ├── reconciler.go                        # Reconciler implementation: hierarchy computation, conditions, no-op suppression
│       ├── hierarchy.go                         # depth/path computation, cycle detection, descendant-recompute-on-ancestry-change
│       ├── fileref.go                           # required-file-reference condition (US3)
│       └── reconciler_test.go
├── cmd/controller/
│   └── main.go                                  # extend: register the CategoryTaxonomy Runner[T]/Reconciler alongside existing wiring
└── tests/
    ├── contract/
    │   └── categorytaxonomy_reconciler_test.go  # unit-level: hierarchy math, cycle detection, no-op suppression, condition transitions
    └── integration/
        └── categorytaxonomy_integration_test.go # FR-015: depth-3 hierarchy, cycle scenario, file-ref condition — against a real gitstore-api test instance

docs/runbooks/
└── controller-lag.md                            # extend: add a CategoryTaxonomy-specific note distinguishing cycle-blocked from genuinely-stalled (FR-016)
```

**Structure Decision**: Extension of the existing `gitstore-controller-manager` module, following the same `internal/<package>` + `tests/{contract,integration}` convention used by specs 025/026/036/040. Three new packages (`graphqlclient`, plus additions to existing `listwatch`/`status`) provide the concrete client-side adapters spec 040 deferred; one new package (`categorytaxonomy`) holds the reconciler itself, kept separate from the generic controller-manager infrastructure the same way a future `product` or `productvariant` reconciler would be. No new top-level service or repository is introduced.

## Complexity Tracking

No constitution violations — table not needed.
