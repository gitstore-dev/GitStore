# Implementation Plan: Namespace API Semantics: Spec Writes, Status Updates, Concurrency

**Branch**: `046-namespace-api-semantics` | **Date**: 2026-08-17 | **Spec**: [spec.md](spec.md)
**Input**: Feature specification from `/specs/046-namespace-api-semantics/spec.md`

## Summary

Adopt `docs/ADRs/0002-namespace-lifecycle.md` in full: make Git the canonical write path for every non-bootstrap `Namespace`. Two well-known bootstrap namespaces (`gitstore-system`, `default`) are created directly in the datastore at API startup. Every other namespace is authored as a manifest in `gitstore-system/gitstore-system` and admitted through the existing pre-receive/post-receive pipeline, extended with a fifth admitted kind (`Namespace`) alongside Product/ProductVariant/CategoryTaxonomy/Collection. `createNamespace`/`updateNamespace` become thin wrappers that commit the equivalent manifest via the existing `GitWriter.CommitFile` gRPC path and wait for admission. Deletion gains the codebase's first real finalizer/`Terminating` state machine, reusing spec 041's existing `HasRepositories` check as its sole drain condition. A new `gitstore-controller-manager` reconciler provisions each admitted namespace's own per-namespace `gitstore-system` repository and drives `SystemRepoReady`/`Ready`/removal, mirroring the existing CategoryTaxonomy reconciler pattern (spec 026/039).

## Technical Context

**Language/Version**: Rust 1.x (`gitstore-git-service`); Go 1.25 (`gitstore-api`, `gitstore-controller-manager`)
**Primary Dependencies**: Existing `CatalogService.ValidateResources`/`AdmitResources` gRPC contract; existing `gitclient.Client.CommitFile` (already wired, currently unused by any resolver); `gix 0.84.0`; `github.com/99designs/gqlgen v0.17.90`; `go-memdb v1.3.5` / `gocqlx/v3 v3.0.4` + `gocql`; existing `internal/types.Reconciler`, `internal/status.StatusClient`, `internal/listwatch.ListWatcher[T]`/`Runner[T]`, `internal/cache.Cache[T]` (all from spec 026/036/039, reused unchanged); `go.uber.org/zap`. No new external dependency in either service.
**Storage**: New persisted columns on the `Namespace` entity — `Generation int64`, `ResourceVersion string`, `Status json.RawMessage`, `DeletionTimestamp *time.Time`, `Finalizers []string` — mirroring the additions spec 045 already made to `Repository` (`gitstore-api/internal/datastore/repository_contract.go`). Requires a memdb schema addition and a new Scylla migration (`004_namespace_resource_contract.cql`), following the `003_repository_resource_contract.cql` precedent.
**Testing**: Go contract/unit/integration tests (mirroring `namespace_contract_test.go`, `repository_read_contract_test.go`); Rust unit/integration tests for the new pre-receive repository-restriction rule (mirroring `admission_handler.rs`'s existing per-kind tests); controller reconciler tests (mirroring `categorytaxonomy/reconciler_test.go`); root `make test`/`make build`/`make pr-ready`.
**Target Platform**: Linux server and Darwin development hosts already supported by all three services.
**Project Type**: Multi-service feature spanning all three GitStore services (git validation, GraphQL API + admission, controller reconciliation).
**Performance Goals**: No new feature-specific target; namespace admission and reconciliation reuse the existing per-push and per-reconcile-tick budgets already covered by constitution performance targets (git push validation < 5ms/500 files; controller reconciliation is not on the storefront read path).
**Constraints**: API-first and test-first; generated gqlgen/gRPC files are never hand-edited; the two bootstrap namespaces are the only permitted datastore-only, non-Git-backed namespace records; `gitstore-system/gitstore-system` is the sole valid `Namespace` manifest authoring target; existing `createNamespace`/`updateNamespace`/`deleteNamespace` GraphQL signatures are preserved (no breaking schema change) even though their internal write path changes completely.
**Scale/Scope**: All existing and future non-bootstrap namespaces; namespace count is small relative to the product catalogue (constitution scale targets are catalogue-sized, not namespace-count-sized), so no new scale ceiling is introduced.

## Constitution Check

*GATE: Must pass before Phase 0 research. Re-checked after Phase 1 design.*

### Pre-design gate

| Principle | Result | Plan evidence |
|---|---|---|
| I. Test-First Development | PASS | Failing pre-receive, admission, resolver, and reconciler tests are written before each corresponding implementation change (Phase 2 task ordering). |
| II. API-First Design | PASS | The admitted `Namespace` manifest shape (already documented in `docs/namespace/namespace-spec.md`) and the unchanged GraphQL mutation signatures are the contract; no resolver code is written before the contract tests exist. |
| III. Clear Contracts & Versioning | PASS | `createNamespace`/`updateNamespace`/`deleteNamespace` keep their existing GraphQL signatures; the write-path change is internal. New status conditions are additive. No breaking schema change. |
| IV. Observability & Debuggability | PASS | Every admission, commit, and reconciliation step gets structured logging (mirroring the existing CategoryTaxonomy admission/reconciler logging); mutation-to-admission latency is logged for the new synchronous wait. |
| V. User Story Driven Development | PASS | Work maps to US1 (Git-backed create/update), US2 (mutation delegation), US3 (safe finalizer-based delete), US4 (status integrity). |
| VI. Incremental Delivery | PASS | US1–US3 are independently testable per spec; the reconciler (US1's provisioning half) can ship after the admission path without blocking reads. |
| VII. Simplicity & YAGNI | JUSTIFIED COMPLEXITY | See Complexity Tracking — this plan implements a previously-`Proposed`, unimplemented ADR (0002) in full, which is inherently larger than a typical incremental spec. The alternative (a narrower, non-Git write path) was explicitly rejected by the project owner in favor of contract consistency with every other catalog resource. |

**Gate result**: PASS (with one justified, explicitly-approved complexity exception).

### Post-design gate

Phase 1 design preserves the pre-design result:

- the new admitted kind reuses the existing `switch e.parsed.Kind` dispatch shape in `cataloggrpc/server.go` rather than introducing a parallel admission mechanism;
- the new reconciler reuses the existing `Reconciler`/`StatusClient`/`ListWatcher`/`Cache` interfaces unchanged (spec 026/036/039) rather than inventing new abstractions;
- the finalizer-drain condition is exactly the existing `HasRepositories` check (spec 041) — no new drain-condition framework;
- `UpdateNamespace`'s optimistic-concurrency shape (`expectedResourceVersion` + `ErrConflict`) is copied verbatim from `UpdateRepository` (spec 045), not redesigned.

**Post-design result**: PASS.

## Project Structure

### Documentation (this feature)

```text
specs/046-namespace-api-semantics/
├── plan.md
├── research.md
├── data-model.md
├── quickstart.md
├── contracts/
│   ├── namespace-admission.md      # manifest path, pre-receive rule, admission outcome contract
│   └── namespace-lifecycle.graphqls # mutation delegation notes (no schema shape change)
└── tasks.md                         # created later by /speckit-tasks
```

### Source Code (repository root)

```text
gitstore-git-service/
└── src/git/hooks/
    ├── validation_handler.rs        # pre-receive: kind==Namespace accepted only in gitstore-system/gitstore-system
    └── admission_handler.rs         # unchanged path-diff mechanics; Namespace manifests flow through the same changed-paths computation

gitstore-api/
└── internal/
    ├── cataloggrpc/
    │   └── server.go                 # new `case "Namespace":` in the admission dispatch switch; admitNamespace()
    ├── datastore/
    │   ├── entities.go                # Namespace gains Generation/ResourceVersion/Status/DeletionTimestamp/Finalizers
    │   ├── namespace_contract.go      # NormalizeNamespaceContract/AdvanceNamespaceSpecVersion/AdvanceNamespaceSystemVersion (mirrors repository_contract.go)
    │   ├── datastore.go                # UpdateNamespace(ctx, ns, expectedResourceVersion) added to the Datastore interface
    │   ├── memdb/backend.go            # UpdateNamespace, optimistic-concurrency check-then-insert (mirrors UpdateRepository)
    │   ├── scylla/
    │   │   ├── namespace.go            # UpdateNamespace via `IF resource_version=?` LWT (mirrors scylla/repository.go)
    │   │   └── migrations/004_namespace_resource_contract.cql
    │   └── entities_test.go
    └── graph/resolver/
        ├── service.go                 # CreateNamespace/UpdateNamespace/DeleteNamespace rewritten to commit-and-wait-for-admission; bootstrap-namespace rejection
        ├── namespace.resolvers.go     # status conditions surfaced from the new Status column
        └── namespace_lifecycle_test.go

gitstore-controller-manager/
├── cmd/controller/main.go            # registerNamespace(...), mirroring registerCategoryTaxonomy(...)
└── internal/namespace/
    ├── reconciler.go                  # provisions per-namespace gitstore-system repo, sets SystemRepoReady/Ready, drives finalizer removal
    └── reconciler_test.go

tests/integration/
└── namespace_lifecycle_test.go       # push-to-admission, mutation-delegation, and finalizer-drain end-to-end coverage
```

**Structure Decision**: Extend the existing three-service pipeline in place — new admitted kind in the existing Rust validation/Go admission dispatch, a new controller-manager package mirroring `categorytaxonomy/`, and datastore additions mirroring spec 045's `Repository` pattern. No new service, no new datastore backend, no new external dependency.

## Phase 0: Research Outcomes

Research decisions are recorded in [research.md](research.md):

1. Adopt ADR-0002 in full; treat it as binding design, not merely advisory, per the project owner's explicit choice.
2. `Namespace` becomes a fifth admitted kind in the existing `cataloggrpc` dispatch switch; no parallel admission mechanism.
3. `Namespace` gains real persisted `Generation`/`ResourceVersion`/`Status`/`DeletionTimestamp`/`Finalizers` columns, mirroring spec 045's `Repository` additions exactly (same helper-function shape: `Normalize*Contract`, `Advance*SpecVersion`, `Advance*SystemVersion`).
4. `createNamespace`/`updateNamespace` delegate to `GitWriter.CommitFile` against `gitstore-system/gitstore-system` and synchronously await admission; no GraphQL schema change to their input/output types.
5. Deletion introduces the codebase's first real finalizer/`Terminating` implementation, but scopes its only drain condition to the already-existing `HasRepositories` check — it does not depend on Repository's own (unimplemented) ADR-0003 finalizer machinery.
6. Reconciliation reuses the existing controller-manager reconciler/listwatch/cache/status abstractions verbatim (spec 026/036/039); a new `internal/namespace` package is added, registered in `cmd/controller/main.go` alongside the existing CategoryTaxonomy registration.
7. Bootstrap namespace creation (API startup) is a one-time, idempotent startup step, not a request-time code path.
8. Spec 047 (GH#173) is updated in this same round to drop its now-stale "no Terminating state" assumption and align its validation/admission matrix with this spec's lifecycle.

All technical unknowns are resolved; no `NEEDS CLARIFICATION` remains.

## Phase 1: Design and Contracts

### Data model

[data-model.md](data-model.md) defines:

- the updated `Namespace` datastore entity (new versioning/status/deletion fields);
- the admission state machine (`AdmissionAccepted` → `SystemRepoReady` → `Ready`; `Terminating` → finalizer removed → hard-deleted);
- the bootstrap-vs-git-backed namespace distinction and its invariants;
- optimistic-concurrency semantics for `UpdateNamespace` (datastore-level, used internally by admission — not a caller-facing precondition, since callers go through Git).

### Interface contracts

- [contracts/namespace-admission.md](contracts/namespace-admission.md): the manifest path (`namespaces/<name>.md` within `gitstore-system/gitstore-system`), the pre-receive repository-restriction rule, and the admission outcome contract (created/updated/rejected, and why).
- [contracts/namespace-lifecycle.graphqls](contracts/namespace-lifecycle.graphqls): documents that `createNamespace`/`updateNamespace`/`deleteNamespace` keep their existing SDL shape; the delegation behavior is internal and does not change the public schema.
- [quickstart.md](quickstart.md): test-first implementation order across all three services, plus manual verification steps (push a manifest, call a mutation, delete a namespace, observe `Terminating`).

### Implementation sequence

1. Add failing Rust pre-receive tests asserting `Namespace` manifests are rejected outside `gitstore-system/gitstore-system`.
2. Add failing Go admission tests for the new `Namespace` case (create, update, tier-demotion rejection, bootstrap-name rejection).
3. Add the `Generation`/`ResourceVersion`/`Status`/`DeletionTimestamp`/`Finalizers` columns, the memdb schema addition, and the Scylla migration; add failing datastore `UpdateNamespace` optimistic-concurrency tests mirroring `UpdateRepository`'s.
4. Implement `namespace_contract.go`, the admission dispatch case, and the datastore `UpdateNamespace` methods until the Phase 1–3 tests pass.
5. Rewrite `Service.CreateNamespace`/`UpdateNamespace`/`DeleteNamespace` to commit-and-wait-for-admission (create/update) and finalizer-mark (delete), with bootstrap-namespace short-circuit rejection; add failing resolver tests first.
6. Add the API-startup bootstrap step that creates `gitstore-system`/`default` and provisions their system repositories idempotently; add a failing startup test first.
7. Add the `gitstore-controller-manager/internal/namespace` reconciler (provision per-namespace system repo, set `SystemRepoReady`/`Ready`, drive finalizer removal once `HasRepositories` is false), registered in `cmd/controller/main.go`; add failing reconciler tests first.
8. Add end-to-end integration coverage: push-to-admission, mutation-delegation, and full create→update→delete→Terminating→removed lifecycle.
9. Update `docs/namespace/namespace-spec.md` and `docs/ADRs/0002-namespace-lifecycle.md`'s status; run targeted tests, `make build`, `make test`, `make pr-ready`.

## Complexity Tracking

| Violation | Why Needed | Simpler Alternative Rejected Because |
|---|---|---|
| New admitted resource kind + new controller-manager reconciler for a single spec, rather than the smaller direct-datastore-write design this spec originally drafted | Adopting `docs/ADRs/0002-namespace-lifecycle.md` in full was an explicit decision by the project owner, made after being shown that the narrower alternative (Git as an audit trail only, no new admission kind or controller) was available and smaller | The narrower alternative was presented and explicitly rejected in favor of full ADR-0002 adoption, to keep Namespace's contract consistent with every other catalog resource (reviewable, auditable, rollback-capable via Git) rather than leaving it as a special case |
| First real finalizer/`Terminating` implementation lands on Namespace before Repository's own (ADR-0003, still `Proposed`/unimplemented) | Namespace deletion safety cannot be deferred further, and its only drain condition (`HasRepositories`) does not require Repository to have its own finalizer machinery first | Waiting for ADR-0003 to ship first was rejected — it is not a dependency of this spec's correctness, only of a stricter cascade (Repository's own Terminating state), which remains a separate future spec |
