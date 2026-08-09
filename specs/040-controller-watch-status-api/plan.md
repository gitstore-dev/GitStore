# Implementation Plan: Controller Watch API and Status Subresource Contract

**Branch**: `040-controller-watch-status-api` | **Date**: 2026-08-07 | **Spec**: [spec.md](./spec.md)  
**Input**: Feature specification from `/specs/040-controller-watch-status-api/spec.md`

## Summary

Add the two missing server-side capabilities that block every controller-manager reconciler from doing real work against `gitstore-api`: a list-then-watch mechanism (GraphQL `Subscription`, per-kind for core resources plus a generic `watchResources(kind:)` for CRDs) with a resumable `resourceVersion` cursor and explicit expired-cursor signaling, and a status-subresource mutation contract (partial-merge, `resourceVersion`-preconditioned, spec-write-rejecting, controller-authorized) that satisfies the existing `status.StatusClient` interface already defined in `gitstore-controller-manager`. Implemented first against `CategoryTaxonomy` — the only kind with an immediate consumer (spec 039 / issue #244) — using gqlgen's existing WebSocket subscription transport and the existing `AuthZProvider`/rbac-local action-string authorization model, so no new transport or auth infrastructure is introduced.

## Technical Context

**Language/Version**: Go 1.25 (`gitstore-api`, `gitstore-controller-manager`)
**Primary Dependencies**: `github.com/99designs/gqlgen v0.17.90` (GraphQL server + subscription transport, already wired via `transport.Websocket` in `gitstore-api/internal/app/server.go`), existing `internal/auth.AuthZProvider`/rbac-local action-string model, existing `internal/listwatch.ListWatcher[T]`/`Watcher[T]`/`WatchEvent[T]` interfaces in `gitstore-controller-manager` (defined by spec 036, no concrete implementation yet), existing `internal/status.StatusClient` interface in `gitstore-controller-manager` (defined by spec 026, no concrete implementation yet)
**Storage**: No new storage. Reuses existing `datastore.Datastore` (`go-memdb` dev / ScyllaDB prod) `CategoryTaxonomy` rows and their existing `resource_version` column/field — no schema migration required for the resourceVersion mechanism itself, since it already exists and is incremented on every `UpdateCategoryTaxonomy` call (see `nextResourceVersion` in `gitstore-api/internal/cataloggrpc/server.go`)
**Testing**: `go test ./...` (Go table/contract tests) for both `gitstore-api` and `gitstore-controller-manager`; integration tests under `gitstore-api/tests/contract/` and `gitstore-controller-manager/tests/integration/` following the existing per-repo pattern
**Target Platform**: Linux server (existing deployment target for both services)
**Project Type**: Backend service extension (GraphQL API + controller-manager client) — no frontend/mobile component
**Performance Goals**: Consistent with existing constitution targets — no new performance goal introduced by this spec; watch delivery latency is not required to be sub-second (reconciliation is level-triggered and tolerant of at-least-once, delayed delivery per spec 026)
**Constraints**: Must not require a schema or server-code change per newly introduced CRD kind (FR-006); must preserve `.spec`/`.metadata` immutability through the status-update path (FR-010); must not break the existing `createCategory`/`updateCategory`/`deleteCategory` legacy `Category` mutations, which remain untouched and out of scope
**Scale/Scope**: Initial implementation targets one core kind (`CategoryTaxonomy`) end-to-end, plus the generic CRD-kind-parameterized watch/status mechanism validated against at least one CRD-style kind in integration tests (per SC-005) — not a full rollout to `Product`/`ProductVariant`/`Collection`, which is designed for but deferred (see spec Assumptions)

## Constitution Check

*GATE: Must pass before Phase 0 research. Re-check after Phase 1 design.*

- **I. Test-First Development (NON-NEGOTIABLE)**: PASS (planned) — Phase 2 tasks will require contract tests for the new `Subscription` fields and status-update mutations, and integration tests for watch resume/expiry and status conflict/authz rejection, written before resolver implementation, mirroring the existing `tests/contract/` and `tests/integration/` structure already used by specs 026/036/038.
- **II. API-First Design**: PASS — this spec's entire purpose is defining the GraphQL contract (`Subscription` fields, status-update mutation shape) before any resolver code is written; contracts are captured in Phase 1 `contracts/`.
- **III. Clear Contracts & Versioning**: PASS — additive-only schema changes (`extend type Subscription`, `extend type Mutation` for status writes); no existing field is removed or changed incompatibly.
- **IV. Observability & Debuggability**: PASS (planned) — FR-014 explicitly requires documented operator signals for watch reconnect vs. expiry and status-write-conflict rate; structured logging follows the existing zap pattern used throughout `gitstore-api`.
- **V. User Story Driven Development**: PASS — spec defines P1 (watch), P1 (status write), P2 (operator diagnosis) stories; tasks will carry [US1]/[US2]/[US3] labels per constitution requirement.
- **VI. Incremental Delivery**: PASS — P1 stories (watch + status write) together form the MVP that unblocks spec 039; P2 (operator docs) can ship after.
- **VII. Simplicity & YAGNI**: PASS — deliberately reuses gqlgen's existing WebSocket subscription transport (already imported and wired) instead of introducing a new streaming protocol; reuses the existing `AuthZProvider` action-string model instead of a new authorization subsystem; scopes live implementation to one kind (`CategoryTaxonomy`) rather than all four core kinds, per the spec's own Assumptions.

No violations requiring the Complexity Tracking table.

## Project Structure

### Documentation (this feature)

```text
specs/[###-feature]/
├── plan.md              # This file (/speckit.plan command output)
├── research.md          # Phase 0 output (/speckit.plan command)
├── data-model.md        # Phase 1 output (/speckit.plan command)
├── quickstart.md        # Phase 1 output (/speckit.plan command)
├── contracts/           # Phase 1 output (/speckit.plan command)
└── tasks.md             # Phase 2 output (/speckit.tasks command - NOT created by /speckit.plan)
```

### Source Code (repository root)

```text
shared/schemas/
├── category.graphqls          # extend type Subscription { watchCategories(...) }; extend type Mutation { updateCategoryStatus(...) }
└── schema.graphqls            # extend type Subscription { watchResources(kind: String!, ...) } — generic CRD entry point

gitstore-api/
├── internal/graph/
│   ├── resolver/
│   │   ├── category_resolver.go            # watchCategories subscription resolver, updateCategoryStatus mutation resolver (new files/additions)
│   │   └── watch_resources_resolver.go     # generic watchResources(kind) subscription resolver + updateResourceStatus mutation resolver (new)
│   └── generated/                          # gqlgen-regenerated output (go generate ./... in gitstore-api)
├── internal/eventbus/                      # new: in-process change-notification fan-out from admission (cataloggrpc) to subscription resolvers
├── internal/middleware/security/
│   └── graphql.go                          # extend GraphQLFieldAuthorizer with a status-write action check (e.g. "category.status.write")
└── tests/contract/
    └── watch_status_test.go                # new: contract tests for Subscription delivery, resourceVersion resume, expired-cursor signal, status-update conflict/authz/spec-rejection

gitstore-controller-manager/
├── internal/listwatch/
│   └── graphql_listwatcher.go               # new: concrete ListWatcher[T]/Watcher[T] implementing the existing internal/listwatch interfaces against the gitstore-api Subscription
├── internal/status/
│   └── graphql_status_client.go             # new: concrete StatusClient implementing the existing internal/status.StatusClient interface against the new mutation
└── tests/integration/
    └── watch_status_integration_test.go     # new: end-to-end list-then-watch + status-write-back against a real gitstore-api test instance

docs/
└── runbooks/controller-watch-status.md      # new: FR-014 operator guidance (transient reconnect vs. expired-cursor, status-write-conflict rate interpretation)
```

**Structure Decision**: Backend-only extension across the existing two-service layout (no frontend/mobile involved). All new code lands inside the existing `gitstore-api` and `gitstore-controller-manager` module trees, following each repo's established `internal/<package>` + `tests/contract|integration` convention (the same layout specs 025/026/036/038 used on the controller-manager side, and specs 027/028/035 used on the API side). No new top-level service or directory is introduced — this is additive work inside `gitstore-api/internal/graph`, a new `gitstore-api/internal/eventbus` package for fan-out, and two new concrete adapters in `gitstore-controller-manager` that satisfy interfaces already defined by earlier specs (`internal/listwatch.ListWatcher[T]`, `internal/status.StatusClient`).

## Complexity Tracking

No constitution violations — table not needed.
