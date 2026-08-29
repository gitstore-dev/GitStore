# Research: Repository Git-Backed Lifecycle, Admission, and Reconciler

## 1. Canonical write path: Git vs. direct datastore write

**Decision**: Git is canonical for every non-bootstrap repository, per `docs/ADRs/0003-repository-lifecycle.md`'s Phase 1 scope, adopted in full for create, update, and delete.

**Rationale**: `Repository` sits directly below `Namespace` in the ownership chain (`Namespace → Repository → Product/ProductVariant/CategoryTaxonomy/Collection/File`), and spec 046 already made this exact decision for Namespace, for exactly the reason ADR-0003 itself gives: "No git-backed resource can exist without a containing repository. Repository lifecycle therefore gates every other catalog ADR." Leaving Repository as a direct-datastore-write special case, now that Namespace above it and every catalog resource below it are git-backed, would be the one remaining structural inconsistency in the ownership chain.

**Alternatives considered**:
- *Git as an audit trail only* (commit a manifest for review purposes, but keep the synchronous datastore write in the same request, no new admission kind or controller). Rejected for the same reason the project owner rejected it for Namespace in spec 046: it is smaller and shippable sooner but does not close ADR-0003 and leaves Repository's contract inconsistent with the resources immediately above and below it.
- *Leave Repository as direct-datastore-write indefinitely, only fix rename/transfer's `Unimplemented` behavior*. Rejected — this would fix the ADR-0003 rename/transfer contradiction while leaving the larger, more consequential create/update/delete contradiction (ADR-0003 §"Git write path", §"GraphQL mutation delegation") unresolved.

## 2. Admission mechanism for the new `Repository` kind

**Decision**: Reuse the existing `cataloggrpc` admission dispatch (`switch e.parsed.Kind { case "Product": ...; case "Namespace": ... }` in `gitstore-api/internal/cataloggrpc/server.go`), adding a sixth case for `"Repository"`.

**Rationale**: Product, ProductVariant, CategoryTaxonomy, Collection, and Namespace already share this exact dispatch shape and the same diff-aware, changed-paths-driven admission flow (spec 034, extended for Namespace in spec 046). Adding a sixth case is additive and does not change the shape of the existing mechanism.

**Alternatives considered**:
- *Separate Repository-only gRPC admission endpoint*. Rejected — duplicates validated, working machinery (changed-paths diff, revision tracking, error mapping) for a single kind, for no benefit over the pattern already proven twice (CategoryTaxonomy, Namespace).

## 3. Repository restriction for `Repository` manifests

**Decision**: Pre-receive rejects any `Repository`-kind manifest pushed to a repository other than the target namespace's own `gitstore-system`, before admission is evaluated. Unlike Namespace (which has exactly one valid authoring target, `gitstore-system/gitstore-system`), the valid authoring target for a `Repository` manifest is namespace-scoped: `<manifest's own metadata.namespace>/gitstore-system`.

**Rationale**: ADR-0003 §"Lifecycle rules" states this explicitly: "Pre-receive validates manifest are pushed to `gitstore-system` repository only within the current namespace." This must be a pre-receive (not post-receive) check, consistent with the existing pre-receive/post-receive split used for Namespace's own repository-restriction rule and every other kind's structural checks — a rejected push must never reach admission or touch the datastore.

**Alternatives considered**:
- *Post-receive rejection* (accept the push, reject at admission time). Rejected for the same reason it was rejected for Namespace in spec 046 — it allows an invalid commit to land in a repository's history.
- *A single global authoring target, mirroring Namespace's `gitstore-system/gitstore-system`*. Rejected — Repository is namespace-scoped by definition (`metadata.namespace` is a required, immutable field), and ADR-0003 explicitly scopes the authoring target per-namespace, not globally.

## 4. Persisted versioning/status fields on `Repository`

**Decision**: No new persisted fields are required. `datastore.Repository` already carries `Generation int64`, `ResourceVersion string`, `Status json.RawMessage`, `DeletionTimestamp *time.Time`, and `Finalizers []string` (added by spec 045, "Repository Resource Contract"), and `gitstore-api/internal/datastore/repository_contract.go` already defines `NormalizeRepositoryContract`/`AdvanceRepositorySpecVersion`/`AdvanceRepositorySystemVersion`. This spec reuses all of them unchanged and is the first to give them real lifecycle meaning (real conditions, a real finalizer, a real deletion marker) rather than the deterministic-placeholder values spec 045 documented (`docs/repository/repository-spec.md`: "This feature has no Repository controller, reconciler, status mutation, or condition-producing writer").

**Rationale**: Spec 045 deliberately built exactly the persistence shape this spec now needs, anticipating "a future writer must define its condition types, ownership, transition rules, reasons, and observed-generation behavior before emitting conditions." This spec is that future writer. Reusing the existing fields and helpers is the smallest, most consistent change; inventing a parallel shape (as spec 046 had to for Namespace, which had no equivalent prior art) would be needless divergence.

**Alternatives considered**:
- *Add a separate `RepositoryLifecycle` sub-structure instead of extending the existing `Status`/`Finalizers`/`DeletionTimestamp` fields*. Rejected — the existing fields are already general-purpose and already used by `UpdateRepository`'s optimistic-concurrency contract; a parallel structure would duplicate state with no benefit.

## 5. Optimistic concurrency at the datastore layer

**Decision**: Reuse `UpdateRepository(ctx, r *Repository, expectedResourceVersion string) error` (already implemented for memdb and, per spec 048, ScyllaDB) unchanged for admission-time writes.

**Rationale**: This method already exists and already implements the same check-then-insert / `IF resource_version=?` LWT contract spec 046 had to newly build for Namespace. Admission needs exactly this to apply create/update writes safely under concurrent pushes.

**Alternatives considered**:
- *Last-write-wins with no precondition*. Rejected for the same reason it was rejected for Namespace — admission of two near-simultaneous manifest updates could silently drop one's effect on `Generation`/`ResourceVersion` bookkeeping.

## 6. Finalizer/`Terminating` scope

**Decision**: Repository deletion sets `DeletionTimestamp` + the existing `gitstore.dev/foreground-deletion` finalizer constant (already defined in `gitstore-controller-manager/internal/namespace` as `ForegroundDeletionFinalizer`, and named identically in ADR-0003) and enters `Terminating`. The controller removes the finalizer once `HasCatalogResources(repositoryID)` (spec 041, already implemented) returns `false` **and** the bare Git repository has been confirmed removed from the git-service filesystem, then the record is hard-deleted.

**Rationale**: This is the smallest correct scope: it reuses an existing, already-shipped existence check (`HasCatalogResources`) as the sole *catalog-resource* drain condition, exactly mirroring how spec 046 scoped Namespace's drain condition to the existing `HasRepositories` check rather than waiting on a richer, not-yet-built mechanism. The additional "confirmed storage removal" condition is specific to Repository (Namespace's drain condition had no analogous storage-removal step, since a namespace's own record removal does not require deleting a filesystem artifact) and is required directly by ADR-0003 §"Repository in Terminating state but git-service unreachable".

**Alternatives considered**:
- *Hard-delete the record as soon as `HasCatalogResources` is false, without waiting for confirmed storage removal*. Rejected — ADR-0003 is explicit that "The controller must not remove the finalizer until storage is confirmed absent," and doing otherwise could leave an orphaned bare Git repository on disk with no corresponding record to ever clean it up.

## 7. Reconciliation

**Decision**: Add `gitstore-controller-manager/internal/repository`, a new reconciler package mirroring `internal/namespace` exactly in shape (a `Reconciler` implementing `types.Reconciler`, registered via a `registerRepository(...)` function in `cmd/controller/main.go` alongside the existing `registerNamespace(...)` and `registerCategoryTaxonomy(...)`). It provisions each admitted repository's bare Git repository on the git-service filesystem, sets `StorageProvisioned`/`Ready`, and drives finalizer removal for `Terminating` repositories once both drain conditions (§6) clear.

**Rationale**: This is the controller-manager's third concrete `Reconciler`/`StatusClient`/`ListWatcher[T]`/`Cache[T]` instantiation (after CategoryTaxonomy, spec 039, and Namespace, spec 046), and the pattern is now proven twice over. Adding `Repository` as a third instance is exactly the extensibility these abstractions exist for, and the closest structural analog (Namespace, which also provisions a piece of Git-service-managed storage and also drives a foreground-deletion finalizer) makes the mirror direct rather than approximate.

**Alternatives considered**:
- *Provision the bare Git repository synchronously inside the admission handler itself, no controller*. Rejected for the same reason spec 046 rejected the equivalent option for Namespace's system-repository provisioning — it reintroduces the synchronous-everything pattern the controller-manager architecture (spec 026) exists to move away from, and makes admission's request path depend on git-service repository-creation latency.

## 8. `renameRepository`/`transferRepository` disposition

**Decision**: Both mutations keep their existing GraphQL input/output shapes (`RenameRepositoryInput`/`RenameRepositoryPayload`, `TransferRepositoryInput`/`TransferRepositoryPayload`) but their resolvers are rewritten to unconditionally return an `Unimplemented`-classed error and perform no datastore or Git mutation whatsoever, deleting the current working implementation in `Service.RenameRepository`/`Service.TransferRepository`.

**Rationale**: ADR-0003's own §"GraphQL mutation delegation" table states this outcome directly: "**Not supported in Phase 1.** Returns `Unimplemented`." The current shipped code contradicts this by performing real, unreviewable datastore-only renames/transfers — behavior that would silently diverge a repository's record from its Git manifest once create/update/delete become git-backed, breaking the "Git is canonical" invariant this spec establishes for every other mutation. Keeping the schema shape (rather than removing the fields) preserves Principle III (no breaking schema removal) while still delivering the correctness-critical behavior change.

**Alternatives considered**:
- *Remove `renameRepository`/`transferRepository` from the schema entirely*. Rejected — this is a breaking schema change with no compensating benefit over returning `Unimplemented`, and ADR-0003 itself frames this as a temporary Phase 1 posture ("Planned for Phase 2 with a dedicated transfer operation"), implying the fields should remain for future implementation.
- *Leave the current working rename/transfer implementation in place, deferring only the `Unimplemented` correction*. Rejected — this was the status quo this spec exists to fix; leaving it in place while making create/update/delete git-backed would make the inconsistency worse, not better, since three of five Repository mutations would be git-backed and two would silently bypass Git entirely.

## 9. `updateRepository` as a new mutation

**Decision**: Add `updateRepository` as a new GraphQL mutation, using the same declarative envelope shape as the also-newly-enveloped `createRepository`.

**Rationale**: `updateRepository` does not exist today (`shared/schemas/repository.graphqls` defines only `createRepository`, `renameRepository`, `transferRepository`, `deleteRepository`), but the admission mechanism this spec introduces for git-backed `Repository` creation inherently must also define update semantics for a re-pushed manifest — the same `case "Repository":` dispatch handles both `admission.OperationCreate` and `admission.OperationUpdate` via the existing `operationForEntry` helper, exactly as `admitNamespace` already does for both operations in one function. ADR-0003 §"Update" and §"GraphQL mutation delegation" already describe this mutation's intended behavior ("the API commits an updated manifest ... Admission validates the delta and updates the datastore record"), so adding it now closes a gap ADR-0003 already anticipated rather than introducing new scope.

**Alternatives considered**:
- *Support update only via direct `git push`, with no GraphQL mutation*. Rejected — this would make Repository's mutation surface inconsistent with Namespace's (spec 046 added both `createNamespace` and `updateNamespace`), and would force every API caller wanting to update a repository's mutable fields to become a Git client, which is exactly what spec 046's User Story 2 established should not be required.

## 10. Consistency with future specs (Repository Validation and Admission Matrix, Repository Watch Contract)

**Decision**: This spec owns only the lifecycle *behavior* (bootstrap-repository-name rejection, per-namespace repository restriction, mutation delegation, finalizer/`Terminating` state machine, reconciliation, rename/transfer disposition). A future "Repository Validation and Admission Matrix" spec (mirroring spec 047 for Namespace) owns the full structural-vs-policy validation rule catalogue and condition-outcome documentation; a future "Repository Watch Contract" spec (mirroring GH#174's `watchNamespaces` pattern) owns watch/subscription semantics. Both are explicitly out of scope here and depend on this spec landing first.

**Rationale**: This is the same ownership split spec 046 recorded relative to spec 047 for Namespace — avoiding duplicate ownership of the same rules across specs while keeping this spec's scope bounded to what ADR-0003's Phase 1 already fully specifies.

## 11. No `NEEDS CLARIFICATION` remains

All prior unknowns were resolved by direct precedent (spec 045's existing `Repository` contract fields, spec 046's proven Namespace lifecycle pattern) and by ADR-0003's own explicit Phase 1 recommendations, which this spec adopts rather than re-derives.
