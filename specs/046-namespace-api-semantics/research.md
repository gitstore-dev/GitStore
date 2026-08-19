# Research: Namespace API Semantics: Spec Writes, Status Updates, Concurrency

## 1. Canonical write path: Git vs. direct datastore write

**Decision**: Git is canonical for every non-bootstrap namespace, per `docs/ADRs/0002-namespace-lifecycle.md`, adopted in full.

**Rationale**: The recorded Clarification on spec 044 ("Git is canonical; Namespace GraphQL mutations delegate to Git under GH#170") is binding, and ADR-0002 is the concrete design behind it. The project owner was shown a narrower alternative (Git as an audit trail only, synchronous datastore write in the same call, no new admission kind or controller) and explicitly chose full ADR-0002 adoption instead, to keep Namespace consistent with every other catalog resource's reviewable/auditable/rollback-capable contract.

**Alternatives considered**:
- *Narrower git-delegation* (`GitWriter.CommitFile` as audit trail, synchronous datastore write in the same request, no new admission kind/controller/finalizer). Rejected by the project owner — smaller and shippable sooner, but leaves Namespace's write-path inconsistent with the documented architecture and does not close ADR-0002.
- *Revert to direct-API-mutation, no Git involvement* (this spec's original draft). Rejected — contradicts the recorded Clarification and ADR-0002's classification of Namespace as git-backed in `docs/resource-storage/git-backed.md`.

## 2. Admission mechanism for the new `Namespace` kind

**Decision**: Reuse the existing `cataloggrpc` admission dispatch (`switch e.parsed.Kind { case "Product": ...; case "CategoryTaxonomy": ... }` in `gitstore-api/internal/cataloggrpc/server.go`), adding a fifth case for `"Namespace"`.

**Rationale**: Product, ProductVariant, CategoryTaxonomy, and Collection already share this exact dispatch shape and the same diff-aware, changed-paths-driven admission flow (spec 034). Adding a fifth case is additive and does not change the shape of the existing mechanism. The alternative — a parallel, Namespace-specific admission code path — would duplicate the pre-receive → post-receive → hydration flow for no benefit.

**Alternatives considered**:
- *Separate Namespace-only gRPC admission endpoint*. Rejected — duplicates validated, working machinery (changed-paths diff, revision tracking, error mapping) for a single kind.

## 3. Repository restriction for Namespace manifests

**Decision**: Pre-receive rejects any `Namespace`-kind manifest pushed to a repository other than `gitstore-system/gitstore-system`, before admission is evaluated.

**Rationale**: ADR-0002 §"Validation and admission rules" states this explicitly ("Accept only in `gitstore-system/gitstore-system`"). This must be a pre-receive (not post-receive) check so a rejected push never reaches admission or touches the datastore, consistent with the existing pre-receive/post-receive split already used for other kinds' structural checks.

**Alternatives considered**:
- *Post-receive rejection* (accept the push, reject at admission time). Rejected — allows an invalid commit to land in an arbitrary repository's history, which the pre-receive/post-receive split elsewhere in this codebase is specifically designed to avoid.

## 4. Persisted versioning/status fields on `Namespace`

**Decision**: Add `Generation int64`, `ResourceVersion string`, `Status json.RawMessage`, `DeletionTimestamp *time.Time`, `Finalizers []string` to `datastore.Namespace`, plus a `namespace_contract.go` file mirroring `repository_contract.go`'s `NormalizeRepositoryContract`/`AdvanceRepositorySpecVersion`/`AdvanceRepositorySystemVersion` shape exactly (renamed to the `Namespace` equivalents).

**Rationale**: Spec 044 deliberately left `Namespace`'s `resourceVersion`/`generation` as read-time-fabricated constants ("1"/1) because GH#171 was schema-only. Spec 045 already established the concrete pattern for adding *real* persisted versioning/status to a previously-flat, non-git-backed entity (`Repository`). Reusing that exact pattern for `Namespace` is the smallest, most consistent change; inventing a different persistence shape for the same concept would be needless divergence (Principle VII).

**Alternatives considered**:
- *Derive resourceVersion from `UpdatedAt`*. Rejected — `UpdatedAt` is a timestamp, not an opaque monotonic counter; it cannot support the `IF resource_version=?` LWT precondition pattern already proven for `Repository` in Scylla.

## 5. Optimistic concurrency at the datastore layer

**Decision**: Add `UpdateNamespace(ctx, ns *Namespace, expectedResourceVersion string) error` to the `Datastore` interface, copying `UpdateRepository`'s exact contract: memdb does a transactional check-then-insert returning `datastore.ErrConflict` on mismatch; Scylla uses `UPDATE ... IF resource_version=?` and returns `datastore.ErrConflict` when `applied == false`.

**Rationale**: This is purely an internal admission-time concern now (the caller-facing GraphQL precondition originally drafted for this spec is no longer exposed directly, since callers go through Git) — but admission itself still needs to apply updates safely under concurrent pushes, and the existing `UpdateRepository` pattern already solves exactly this problem for another entity.

**Alternatives considered**:
- *Last-write-wins with no precondition*. Rejected — admission of two near-simultaneous manifest updates could silently drop one's effect on `Generation`/`ResourceVersion` bookkeeping.

## 6. Finalizer/`Terminating` scope

**Decision**: Namespace deletion sets `DeletionTimestamp` + a `gitstore.dev/foreground-deletion` finalizer (naming taken verbatim from ADR-0003, applied here to Namespace for the first time) and enters `Terminating`. The controller removes the finalizer once `HasRepositories(namespaceID)` (spec 041, already implemented) returns `false`, then the record is hard-deleted.

**Rationale**: This is the smallest correct scope: it reuses an existing, already-shipped existence check as the sole drain condition, rather than waiting on Repository's own ADR-0003 finalizer machinery (which remains unimplemented and is not this spec's dependency).

**Alternatives considered**:
- *Wait for every owned Repository to itself reach `Terminating` and clear its own finalizer* (ADR-0003's eventual model, referenced in ADR-0003 §"Namespace entering Terminating while repository exists"). Rejected for this spec — spec 041 already rejects namespace deletion outright while any repository exists, so a namespace can only begin `Terminating` when it is already provably empty; there is nothing left to wait on beyond confirming that fact, so the richer ordering ADR-0003 describes is not yet needed.

## 7. Reconciliation

**Decision**: Add `gitstore-controller-manager/internal/namespace`, a new reconciler package mirroring `internal/categorytaxonomy` exactly in shape (a `Reconciler` implementing `types.Reconciler`, registered via a `registerNamespace(...)` function in `cmd/controller/main.go` alongside the existing `registerCategoryTaxonomy(...)`). It provisions each admitted namespace's own per-namespace `gitstore-system` repository (reusing `ProvisionSystemRepository`'s existing logic, now controller-invoked instead of API-invoked-at-create-time for non-bootstrap namespaces), sets `SystemRepoReady`/`Ready`, and drives finalizer removal for `Terminating` namespaces.

**Rationale**: The controller-manager's `Reconciler`/`StatusClient`/`ListWatcher[T]`/`Cache[T]` abstractions (spec 026/036/039) are kind-agnostic by design; CategoryTaxonomy is the only existing concrete instance. Adding `Namespace` as a second instance is exactly the extensibility these abstractions exist for.

**Alternatives considered**:
- *Provision the per-namespace system repository synchronously inside the admission handler itself, no controller*. Rejected — this is exactly the synchronous-everything pattern spec 026's controller-manager architecture was introduced to move away from; it would also make admission's request path depend on git-service repository-creation latency.

## 8. Bootstrap namespace creation

**Decision**: API startup ensures `gitstore-system` and `default` exist directly in the datastore (idempotent — a no-op if they already exist) and provisions each one's own `gitstore-system` repository, before the API begins serving admission traffic.

**Rationale**: ADR-0002's bootstrap path is exactly this; it is what makes `gitstore-system/gitstore-system` available as an authoring target before any namespace manifest can be pushed, resolving the chicken-and-egg problem ADR-0002 itself identifies.

**Alternatives considered**:
- *Lazy bootstrap on first namespace-related request*. Rejected — startup-time bootstrap is simpler to reason about and test, and ADR-0002 explicitly describes it as a startup step, not a request-time path.

## 9. Consistency with spec 047 (GH#173)

**Decision**: Spec 047 is revised in this same round to remove its now-stale "no Terminating state exists" assumption and align its validation/admission matrix with this spec's lifecycle (structural pre-receive rules vs. platform-wide admission policy rules, per ADR-0002's own phase table), while this spec (046) retains sole ownership of the lifecycle *behavior* (bootstrap, mutation delegation, finalizer/Terminating state machine, reconciliation).

**Rationale**: Avoiding duplicate ownership of the same rules across two specs; 046 owns "what happens," 047 owns "the full validation rule matrix and condition-outcome documentation."

## 10. No `NEEDS CLARIFICATION` remains

All prior unknowns were resolved through the two rounds of user decisions recorded in spec 046's Clarifications section and this document.
