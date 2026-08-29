# Implementation Plan: Repository Git-Backed Lifecycle, Admission, and Reconciler

**Branch**: `058-repository-git-backed-lifecycle` | **Date**: 2026-08-29 | **Spec**: [spec.md](spec.md)
**Input**: Feature specification from `/specs/058-repository-git-backed-lifecycle/spec.md`

## Summary

Adopt `docs/ADRs/0003-repository-lifecycle.md`'s Phase 1 scope in full: make Git the canonical write path for every non-bootstrap `Repository`. Each namespace's own bootstrap `gitstore-system` repository (already auto-provisioned directly in the datastore by spec 041) remains the sole datastore-only exception. Every other repository is authored as a manifest at `repositories/<name>.md` inside the owning namespace's `gitstore-system` repository and admitted through the existing pre-receive/post-receive pipeline, extended with a sixth admitted kind (`Repository`) alongside Product/ProductVariant/CategoryTaxonomy/Collection/Namespace. `createRepository` gains a git-delegating rewrite and a new `updateRepository` sibling, both committing via the existing `GitWriter.CommitFile` gRPC path (already wired and already used by Namespace, spec 046) and waiting for admission. `renameRepository`/`transferRepository` are rewired to unconditionally return `Unimplemented`, reversing their current direct-datastore-write implementation to match ADR-0003. Deletion reuses the codebase's existing finalizer/`Terminating` machinery (introduced for Namespace in spec 046), with spec 041's `HasCatalogResources` as its sole drain condition. A new `gitstore-controller-manager/internal/repository` reconciler provisions each admitted repository's bare Git repository on the git-service filesystem and drives `StorageProvisioned`/`Ready`/removal, mirroring the existing `internal/namespace` reconciler pattern.

## Technical Context

**Language/Version**: Rust 1.x (`gitstore-git-service`); Go 1.25 (`gitstore-api`, `gitstore-controller-manager`)
**Primary Dependencies**: Existing `CatalogService.ValidateResources`/`AdmitResources` gRPC contract; existing `gitclient.Client.CommitFile` (already wired and already used by Namespace's git-delegating mutations, spec 046); `gix 0.84.0`; `github.com/99designs/gqlgen v0.17.90`; `go-memdb v1.3.5` / `gocqlx/v3 v3.0.4` + `gocql`; existing `internal/types.Reconciler`, `internal/status.StatusClient`, `internal/listwatch.ListWatcher[T]`/`Runner[T]`, `internal/cache.Cache[T]` (spec 026/036/039/046, reused unchanged — this is their third concrete instantiation after CategoryTaxonomy and Namespace); `go.uber.org/zap`. No new external dependency in either service.
**Storage**: The persisted `Repository` entity already carries `Generation int64`, `ResourceVersion string`, `Status json.RawMessage`, `DeletionTimestamp *time.Time`, and `Finalizers []string` (added by spec 045) — unlike Namespace before spec 046, no new columns or Scylla migration are required. `gitstore-api/internal/datastore/repository_contract.go`'s `NormalizeRepositoryContract`/`AdvanceRepositorySpecVersion`/`AdvanceRepositorySystemVersion` and `datastore.go`'s `UpdateRepository(ctx, r, expectedResourceVersion)` optimistic-concurrency method already exist (spec 045) and are reused unchanged by the new admission case.
**Testing**: Go contract/unit/integration tests (mirroring `namespace_lifecycle_test.go`, `repository_read_contract_test.go`); Rust unit/integration tests for the new pre-receive per-namespace repository-restriction rule (mirroring `admission_handler.rs`'s existing per-kind tests); controller reconciler tests (mirroring `internal/namespace/reconciler_test.go`); root `make test`/`make build`/`make pr-ready`.
**Target Platform**: Linux server and Darwin development hosts already supported by all three services.
**Project Type**: Multi-service feature spanning all three GitStore services (git validation, GraphQL API + admission, controller reconciliation).
**Performance Goals**: No new feature-specific target; repository admission and reconciliation reuse the existing per-push and per-reconcile-tick budgets already covered by constitution performance targets (git push validation < 5ms/500 files; controller reconciliation is not on the storefront read path).
**Constraints**: API-first and test-first; generated gqlgen/gRPC files are never hand-edited; a namespace's own `gitstore-system` repository is the sole valid `Repository` manifest authoring target for that namespace; the bootstrap `gitstore-system` repository is the only permitted datastore-only, non-Git-backed `Repository` record; `renameRepository`/`transferRepository` keep their existing GraphQL input/output shapes but unconditionally return `Unimplemented` — no schema field is removed.
**Scale/Scope**: All existing and future non-bootstrap repositories across all namespaces; repository count scales with namespace count × repositories-per-namespace, not directly with the 5,000,000-product catalogue ceiling, so no new scale ceiling is introduced beyond what spec 048's query-first Repository storage already bounds.
**Replica/Scaling Model**: Admission and resolver writes remain replica-safe via the existing `UpdateRepository` optimistic-concurrency contract (spec 045, `ResourceVersion`-guarded); the new controller reconciler reuses spec 026's queue/backoff/idempotent-reconcile model and spec 046's proven `internal/namespace` instantiation of it — no new coordination primitive.
**Authentication/Authorization**: Reuses the existing `authorizeRepositoryTenant` capability checks (`repository:create`/`repository:push`/`system:admin`) unchanged; the new `updateRepository` mutation is authorized with the existing `"update"` action shape already used elsewhere in this resolver family, and admission enforces the same namespace-`Active`-not-`Terminating` precondition already required by ADR-0003.
**Load/Backpressure Model**: Reuses the existing pre-receive/post-receive per-push budget and the controller-manager's existing per-tick reconcile budget; no new queue, worker pool, or timeout policy is introduced.

## Constitution Check

*GATE: Must pass before Phase 0 research. Re-checked after Phase 1 design.*

### Pre-design gate

| Principle | Result | Plan evidence |
|---|---|---|
| I. Test-First Development | PASS | Failing pre-receive, admission, resolver, and reconciler tests are written before each corresponding implementation change (Phase 2 task ordering), mirroring spec 046's proven test-first sequence. |
| II. API-First Design | PASS | The admitted `Repository` manifest shape (already documented in `docs/repository/repository-spec.md` and ADR-0003) and the `updateRepository` mutation's envelope contract are defined in this spec/plan before any resolver code is written. |
| III. Clear Contracts & Versioning | PASS | `createRepository`/`deleteRepository` keep their public GraphQL signatures; `updateRepository` is additive; `renameRepository`/`transferRepository` keep their existing input/output shapes and only their runtime behavior changes (to `Unimplemented`), which is called out as an explicit, intentional behavior reversal rather than a silent one. New status conditions (`StorageProvisioned`, `Ready`) are additive to the already-defined-but-always-empty `RepositoryStatus.conditions`. |
| IV. Production Observability & Debuggability | PASS | Every admission, commit, and reconciliation step gets structured logging (mirroring the existing Namespace admission/reconciler logging); mutation-to-admission latency is logged for the new synchronous wait, matching spec 046's pattern. |
| V. User Story Driven Development | PASS | Work maps to US1 (Git-backed create/update), US2 (mutation delegation incl. new `updateRepository`), US3 (safe finalizer-based delete), US4 (rename/transfer honesty), US5 (status integrity). |
| VI. Independently Deployable Delivery | PASS | US1–US3 are independently testable per spec; the reconciler (US1's provisioning half) can ship after the admission path without blocking reads, exactly as spec 046 sequenced Namespace's reconciler; old-replica `createRepository` callers remain compatible during rollout since the public mutation name/shape for `createRepository`/`deleteRepository` is unchanged. |
| VII. Simplicity with Proven Scale | JUSTIFIED COMPLEXITY | See Complexity Tracking — this plan closes a previously-`Proposed`, unimplemented ADR (0003) whose Phase 1 scope is inherently larger than a typical incremental spec, though smaller than spec 046's equivalent work because the datastore contract layer (`Generation`/`ResourceVersion`/`Status`/`Finalizers`, `UpdateRepository`) already exists from spec 045 and the finalizer/reconciler *pattern* already exists from spec 046 — this spec is the second, not the first, instantiation of both. |
| VIII. Horizontally Replicable Core Services | PASS | Admission and `UpdateRepository` remain resource-version-guarded across replicas; the reconciler uses the same replica-safe listwatch/cache/queue model already proven for CategoryTaxonomy and Namespace; no process-local correctness state is introduced. |
| IX. Multi-User Authentication, Authorization & Isolation | PASS | Reuses existing `authorizeRepositoryTenant` capability boundaries unchanged; the new `updateRepository` path is authorized before any commit is made; namespace/repository isolation is preserved (a `Repository` manifest can only be authored in its own namespace's `gitstore-system`, never cross-namespace). |
| X. Production Capacity, Backpressure & Load Validation | PASS | No new unbounded queue, scan, or goroutine; admission and reconciliation reuse existing bounded per-push and per-tick budgets; repository count is not on the 5,000,000-product catalogue's critical path. |

**Gate result**: PASS (with one justified, explicitly-scoped complexity exception, smaller in surface area than spec 046's equivalent exception).

### Post-design gate

Phase 1 design preserves the pre-design result:

- the new admitted kind reuses the existing `switch e.parsed.Kind` dispatch shape in `cataloggrpc/server.go` rather than introducing a parallel admission mechanism, exactly as `case "Namespace"` was added in spec 046;
- the new reconciler reuses the existing `Reconciler`/`StatusClient`/`ListWatcher`/`Cache` interfaces unchanged (spec 026/036/039/046) rather than inventing new abstractions;
- the finalizer-drain condition is exactly the existing `HasCatalogResources` check (spec 041) — no new drain-condition framework;
- no new datastore column, migration, or contract-helper file is introduced — `Repository`'s spec-045 fields and contract helpers are reused verbatim, unlike Namespace which needed a new `namespace_contract.go` in spec 046.

**Post-design result**: PASS.

## Project Structure

### Documentation (this feature)

```text
specs/058-repository-git-backed-lifecycle/
├── plan.md
├── research.md
├── data-model.md
├── quickstart.md
├── contracts/
│   ├── repository-admission.md       # manifest path, pre-receive rule, admission outcome contract
│   └── repository-lifecycle.graphqls # createRepository/updateRepository envelope; renameRepository/transferRepository Unimplemented notice
├── checklists/
│   └── requirements.md
└── tasks.md                          # created later by /speckit.tasks
```

### Source Code (repository root)

```text
gitstore-git-service/
└── src/git/hooks/
    ├── validation_handler.rs        # pre-receive: kind==Repository accepted only in <namespace>/gitstore-system
    └── admission_handler.rs         # unchanged path-diff mechanics; Repository manifests flow through the same changed-paths computation

gitstore-api/
└── internal/
    ├── cataloggrpc/
    │   └── server.go                 # new `case "Repository":` in the admission dispatch switch; admitRepository(); structural rejection for immutable-field changes and storageClass downgrade
    ├── datastore/
    │   └── repository_contract.go    # unchanged; reused verbatim (spec 045)
    └── graph/resolver/
        ├── service.go                 # CreateRepository/UpdateRepository/DeleteRepository rewritten to commit-and-wait-for-admission; RenameRepository/TransferRepository rewritten to return Unimplemented; bootstrap-repository-name rejection
        ├── repository.resolvers.go    # UpdateRepository resolver added; status conditions surfaced from the existing Status column
        └── repository_lifecycle_test.go

gitstore-controller-manager/
├── cmd/controller/main.go            # registerRepository(...), mirroring registerNamespace(...) and registerCategoryTaxonomy(...)
└── internal/repository/
    ├── reconciler.go                  # provisions the bare Git repository on git-service, sets StorageProvisioned/Ready, drives finalizer removal
    └── reconciler_test.go

shared/schemas/
└── repository.graphqls               # createRepository/updateRepository envelope inputs; RepositoryStatus.conditions doc comment updated to reflect the new condition-producing writer

tests/integration/
└── repository_lifecycle_test.go       # push-to-admission, mutation-delegation, and finalizer-drain end-to-end coverage
```

**Structure Decision**: Extend the existing three-service pipeline in place — new admitted kind in the existing Rust validation/Go admission dispatch, a new controller-manager package mirroring `internal/namespace/`, and resolver/schema additions. No new service, no new datastore backend, no new external dependency, no new datastore column (unlike spec 046, which needed new Namespace columns; Repository's are already present from spec 045).

## Phase 0: Research Outcomes

Research decisions are recorded in [research.md](research.md):

1. Adopt ADR-0003's Phase 1 scope in full for create/update/delete; treat `renameRepository`/`transferRepository`'s `Unimplemented` behavior as binding per the project owner's explicit choice, reversing today's shipped rename/transfer implementation.
2. `Repository` becomes a sixth admitted kind in the existing `cataloggrpc` dispatch switch; no parallel admission mechanism.
3. No new persisted fields are required — `Repository` already has `Generation`/`ResourceVersion`/`Status`/`DeletionTimestamp`/`Finalizers` and their contract helpers from spec 045; this spec is the first to give them real lifecycle meaning for `Repository`.
4. `createRepository` is rewritten and `updateRepository` is newly added to delegate to `GitWriter.CommitFile` against the owning namespace's `gitstore-system` and synchronously await admission; both use the declarative envelope shape, replacing `createRepository`'s legacy flat input.
5. `renameRepository`/`transferRepository` keep their existing GraphQL shapes but their resolvers are rewritten to unconditionally return `Unimplemented`, deleting their current direct-datastore-write code paths.
6. Deletion reuses the finalizer/`Terminating` machinery spec 046 introduced for Namespace, scoping its only drain condition to the already-existing `HasCatalogResources` check (spec 041) — it does not depend on any other resource's own finalizer machinery.
7. Reconciliation reuses the existing controller-manager reconciler/listwatch/cache/status abstractions verbatim (spec 026/036/039/046); a new `internal/repository` package is added, registered in `cmd/controller/main.go` alongside the existing Namespace and CategoryTaxonomy registrations.
8. Bootstrap repository creation (namespace creation time) is unchanged from spec 041 — no new bootstrap step is introduced by this spec.
9. The "Repository Validation and Admission Matrix" and "Repository Watch Contract" follow-on specs (mirroring spec 047 and GH#174's analogs for Namespace) are explicitly out of scope and sequenced after this spec, matching how those specs were sequenced after spec 046.

All technical unknowns are resolved; no `NEEDS CLARIFICATION` remains.

## Phase 1: Design and Contracts

### Data model

[data-model.md](data-model.md) defines:

- the unchanged `Repository` datastore entity (no new fields; spec 045's fields gain real lifecycle meaning);
- the admission state machine (`AdmissionAccepted` → `StorageProvisioned` → `Ready`; `Terminating` → finalizer removed → hard-deleted);
- the bootstrap-vs-git-backed repository distinction and its invariants;
- the immutable-vs-mutable field matrix for `updateRepository`/manifest re-push (`spec.visibility`, `spec.defaultBranch`, `spec.storageClass` upgrade-only are mutable; `metadata.name`, `metadata.namespace` are immutable).

### Interface contracts

- [contracts/repository-admission.md](contracts/repository-admission.md): the manifest path (`repositories/<name>.md` within a namespace's own `gitstore-system`), the pre-receive repository-restriction rule, and the admission outcome contract (created/updated/rejected, and why).
- [contracts/repository-lifecycle.graphqls](contracts/repository-lifecycle.graphqls): the new `updateRepository` mutation and envelope input shapes for `createRepository`/`updateRepository`; documents that `renameRepository`/`transferRepository` keep their existing SDL shape while their resolver behavior becomes unconditional `Unimplemented`.
- [quickstart.md](quickstart.md): test-first implementation order across all three services, plus manual verification steps (push a manifest, call `createRepository`/`updateRepository`, delete a repository, observe `Terminating`, call `renameRepository`/`transferRepository` and observe `Unimplemented`).

### Implementation sequence

1. Add failing Rust pre-receive tests asserting `Repository` manifests are rejected outside the target namespace's own `gitstore-system`. Implement the rule until it passes.
2. Add failing Go admission tests for the new `Repository` case (create, update of mutable fields, immutable-field-change rejection, storage-class-downgrade rejection, bootstrap-name rejection, `Terminating`-namespace rejection). Implement `admitRepository` until green — reusing `repository_contract.go`'s existing helpers unchanged.
3. Rewrite `Service.CreateRepository`, add `Service.UpdateRepository`, and rewrite `Service.DeleteRepository` to commit-and-wait-for-admission (create/update) and finalizer-mark (delete), with bootstrap-repository-name short-circuit rejection; rewrite `Service.RenameRepository`/`Service.TransferRepository` to unconditionally return `Unimplemented`; add failing resolver tests first.
4. Add the `updateRepository` mutation, `UpdateRepositoryInput`/`UpdateRepositoryPayload`, and envelope-shaped `CreateRepositoryInput` to `shared/schemas/repository.graphqls`; regenerate gqlgen code (never hand-edited).
5. Add the `gitstore-controller-manager/internal/repository` reconciler (provision the bare Git repository on git-service, set `StorageProvisioned`/`Ready`, drive finalizer removal once `HasCatalogResources` is false and storage removal is confirmed), registered in `cmd/controller/main.go`; add failing reconciler tests first.
6. Add end-to-end integration coverage: push-to-admission, mutation-delegation (including `updateRepository`), full create→update→delete→`Terminating`→removed lifecycle, and `renameRepository`/`transferRepository` `Unimplemented` regression coverage.
7. Update `docs/repository/repository-spec.md` (condition vocabulary, version-transitions table, legacy rename/transfer rows) and `docs/ADRs/0003-repository-lifecycle.md`'s status; run targeted tests, `make build`, `make test`, `make pr-ready`.

## Complexity Tracking

| Violation | Why Needed | Simpler Alternative Rejected Because |
|---|---|---|
| New admitted resource kind + new controller-manager reconciler for a single spec, rather than a narrower direct-datastore-write design | Closing `docs/ADRs/0003-repository-lifecycle.md`'s Phase 1 gap requires the same admission-kind-plus-reconciler shape spec 046 already established for Namespace; a narrower alternative (Git as audit trail only) was already evaluated and rejected once for Namespace on consistency grounds, and Repository sits directly below Namespace in the same ownership chain | A narrower alternative would leave Repository as the only tier in `Namespace → Repository → Product/...` still using direct datastore writes, reintroducing exactly the inconsistency spec 046 eliminated for Namespace |
| Reversing `renameRepository`/`transferRepository`'s current working behavior to `Unimplemented` | ADR-0003's Phase 1 recommendation is binding for this spec's scope, and the current shipped behavior contradicts it; shipping git-backed create/update/delete while leaving rename/transfer as direct datastore writes would let a repository's record diverge from its Git manifest with no corresponding commit, breaking the "Git is canonical" invariant this spec establishes | Leaving rename/transfer as-is was rejected — it is not a smaller change, it is a correctness gap that this spec's own admission model would otherwise silently tolerate |
