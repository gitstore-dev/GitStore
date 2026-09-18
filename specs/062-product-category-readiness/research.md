# Research: Product Category and Readiness Reconciliation

## R1: Does anything already write `CategoryResolved=True` (the positive/resolve path)?

**Decision**: No. The only existing writer of `catalog.ConditionCategoryResolved`
is `Service.DecoupleCategoryProducts` (`gitstore-api/internal/graph/resolver/service.go:540`),
which sets `CategoryResolved=False`/`CategoryDeleted` on Products whose
category is being force-deleted (spec 055's decoupling flow). No admission or
resolver path ever sets `CategoryResolved=True`. This feature is the first
writer of the positive path, exactly matching ADR-0004's assignment of
`categoryRef` resolution to the controller (not admission).

**Rationale**: Confirmed by exhaustive grep for `ConditionCategoryResolved`/`CategoryFound`/`CategoryNotFound`
across `gitstore-api`; only the one call site exists, and it is deletion-only.

**Alternatives considered**: None — this was a fact-finding step, not a choice.

## R2: `UpdateProductStatusInput` cannot express what this feature needs

**Decision**: Extend the existing `updateProductStatus` mutation contract
rather than adding a new mutation. Add three fields to `UpdateProductStatusInput`:
`observedGeneration: Int`, `lastAppliedRevision: String`, and
`resolved: ResolvedProductStatusInput` (a new input type carrying only
`category: ResolvedCategoryRefInput { name: String!, uid: ID! }`). Add
`uid: ID` to the existing `ResolvedCategoryDefinition` output type.

**Rationale**: `UpdateProductStatusInput` today only has
`name, namespace, resourceVersion, conditions` (plus a dead `removeOwnerID`
field removed by this feature — see R7) — no
`observedGeneration`/`lastAppliedRevision`/`resolved`, unlike every sibling
per-kind status mutation (`updateRepositoryStatus`, `updateCategoryStatus`,
`updateNamespaceStatus`), which all carry the full partial-merge shape. This
is a pre-existing contract gap, not a design choice this feature should
route around. Extending it additively (all new fields optional/nullable)
keeps `Independently Deployable Delivery`: an old controller replica that
never sends `resolved`/`observedGeneration` still works against the new API
schema unchanged, and a new controller talking to an old API replica during
a rolling upgrade simply has its new fields ignored/rejected by the old
resolver until that replica rolls too — no simultaneous-fleet-restart
requirement.

**Alternatives considered**:
- *New dedicated mutation (e.g. `updateProductCategoryResolution`)* — rejected.
  Splits Product's status writeback across two mutations for no reason;
  every other kind uses exactly one status mutation, and `conditions` (which
  this feature also needs) already lives on `updateProductStatus`.
- *Route through the generic `updateResourceStatus` mutation* — rejected. That
  mutation JSON-boxes `resolved`, which defeats FR-016's requirement that the
  resolved reference's `uid` be the same typed, opaque value `CategoryTaxonomy.id`
  already is; the per-kind mutation gets a real `ID` scalar instead of an
  untyped JSON string.

## R3: Reverse fan-out (CategoryTaxonomy → affected Products) must be bounded

**Decision**: Add a small, controller-local index —
`internal/categorytaxonomy/product_index.go` — mapping
`(namespace, categoryName) → set of Product WorkItemKeys`, maintained
incrementally by the *existing* Product-cache event handler wiring in
`registerProductWatch` (the same `OnAdd`/`OnUpdate`/`OnDelete` callbacks that
already exist to drive the spec-042 Product→CategoryTaxonomy count fan-out).
The CategoryTaxonomy-cache event handler added for this feature does an
O(1) map lookup by `(namespace, name)` against this index (and, for a rename,
also the old name) to decide which Products to re-enqueue — never a full
`productCache.List()` scan.

**Rationale**: `internal/cache.CacheAccessor[T].List()` exists and is the
established pattern for full-population scans (e.g. CategoryTaxonomy's own
parent-hierarchy computation, spec 039) — but that scans the *category*
cache, which is small. Scanning the *Product* cache (up to 5,000,000 entries
per the constitution's Production Capacity Envelope) synchronously inside a
cache-mutation callback on every CategoryTaxonomy create/rename would
violate PR-003 ("bounded, indexed lookup per Product") and PR-004 (a burst of
category creates must not starve unrelated reconciliation). An incrementally
maintained index turns this into the same O(matching Products) cost the
forward direction already pays.

**Alternatives considered**:
- *Full `productCache.List()` scan per CategoryTaxonomy event* — rejected
  per PR-003/PR-004 above.
- *Re-list Products from the API on demand (`NewProductCounter`'s
  pagination pattern)* — rejected. That pattern exists for a bounded,
  infrequent count computation (spec 042), not a per-event lookup on the
  reconciliation hot path; it would add API round-trips proportional to
  catalog size instead of an in-process map read.

## R4: Ready condition composition

**Decision**: `Ready=True` iff `AdmissionAccepted=True` and
`CategoryResolved=True` (FR-004). `MediaResolved` is not part of the
composition (FR-013 — deferred to Phase 2/GH#244, matching ADR-0004's own
scoping).

**Rationale**: `AdmissionAccepted=True` is a precondition of the controller
ever observing the Product in a reconcilable (non-pending-admission) state,
so checking it here is defensive, not redundant — it also guards the edge
case (documented in Edge Cases) where a Product's namespace/repository later
becomes non-`Active`; this feature does not change that behavior, it simply
does not claim `Ready=True` in front of it.

**Alternatives considered**: Deriving `Ready` purely from `CategoryResolved`
(dropping the `AdmissionAccepted` check) — rejected as a needless narrowing
of the existing Ready contract other kinds already follow (Repository's
`Ready` composition, `internal/repository/reconciler.go`).

## R5: Condition/patch mechanics reuse existing prior art unchanged

**Decision**: Reuse `internal/status.StatusPatch`/`Condition`/`IsNoOp` as-is;
add a `NewGraphQLProductStatusClient` in `internal/status`
(`graphql_product_status_client.go`) mirroring
`graphql_repository_status_client.go`/`graphql_namespace_status_client.go`
exactly (same `toUpdateCategoryStatusInput`-shaped input builder, same
`RESOURCE_VERSION_CONFLICT`/`NOT_FOUND` error-extension mapping to
`types.ErrConflict`/`types.ErrNotFound`). Reuse the existing
`preserveTransitionTime`-style condition-merge helper pattern from
`internal/repository/reconciler.go`/`internal/categorytaxonomy/reconciler.go`
for FR-007 (don't flap `lastTransitionTime` on a no-op re-confirmation).

**Rationale**: This is the fourth instantiation of the same
StatusPatch/StatusClient/condition-merge pattern (after Repository,
CategoryTaxonomy, Namespace) — no new abstraction, per Simplicity.

**Alternatives considered**: None — this is prior-art reuse, not a design
choice with real alternatives.

## R6: Retry/backoff for an unresolved category

**Decision**: Reuse the existing bounded `types.ResultAfter(delay)` requeue
mechanism already used by every other reconciler in this codebase (e.g.
`rateLimitRequeueDelay`/`conflictRequeueDelay` in `internal/repository/reconciler.go`)
for the FR-011 indefinite-but-bounded-interval retry when `CategoryResolved=False`/`CategoryNotFound`.
No new backoff/scheduling primitive.

**Rationale**: Matches the Clarifications session decision (indefinite,
bounded-interval requeue) and existing codebase convention exactly.

**Alternatives considered**: A dedicated exponential-backoff timer per
unresolved Product — rejected as unnecessary; the watch-driven re-enqueue
(R3) already converges immediately once the category exists, so the
interval-based retry is only a fallback for cases the watch might miss
(e.g. missed/replayed events), not the primary convergence path.

## R7: The controller's resolved-category write must also (re-)establish the owner reference — reuse admission's mechanism, don't reinvent it

**Decision**: `resolvedCategoryOwnerReferences` (`gitstore-api/internal/cataloggrpc/server.go:1877`)
already resolves `spec.categoryRef` into a non-blocking `CategoryTaxonomy`
`OwnerReference` — but only at Product admission (push/mutation) time.
A Product pushed before its category exists gets `OwnerReferences: []`
and nothing ever re-runs that resolution later; only a new Product push
would. For User Story 2 (category created after the Product), the
controller's `updateProductStatus` write is therefore the only path that
can establish that owner reference for an already-admitted Product. Rather
than adding a separate imperative add/remove mechanism, `resolved.category`
(R2) drives it declaratively: the `UpdateProductStatus` resolver
synchronizes the Product's `CategoryTaxonomy` owner reference to match
whatever `resolved.category` says on every call — present and matching if
set, absent if null — mirroring exactly what admission already computes for
a freshly-pushed Product. `UpdateProductStatusInput.removeOwnerID` (dead:
zero callers anywhere in `gitstore-controller-manager`, `tests/integration`,
or `gitstore-api`'s own tests, and single-purpose — its only non-empty
branch matched a `CategoryTaxonomy`-kind reference) is removed as part of
this change; it is fully subsumed by the declarative sync.

**Rationale**: This closes a gap the original R1–R6 decisions missed —
writing `CategoryResolved=True`/`resolved.category` without also
establishing the owner reference would leave `DecoupleCategoryProducts`
(the already-implemented, already-wired User-Story-3 mechanism — see
`updateCategoryStatus(decoupleProducts: true)`) blind to that Product: its
owner-reference reverse index would have no entry for it, so a later
category deletion would silently fail to flip that Product's
`CategoryResolved` back to `False`. Reusing the declarative, resolved-state
approach (rather than an imperative add-owner-reference RPC) also means User
Story 3 requires **no new controller code at all** — deletion decoupling
already exists and already works once the owner reference is present.

**Alternatives considered**:
- *A dedicated `addOwnerID`-style imperative field, mirroring the removed
  `removeOwnerID`* — rejected. Two imperative, single-purpose fields
  (add/remove) duplicate what a single declarative `resolved.category` field
  already expresses; the resolver can derive "add", "update", or "remove"
  from a single before/after comparison.
- *Have the controller call `resolvedCategoryOwnerReferences`'s logic itself
  and write raw `OwnerReferences` JSON directly* — rejected. That function
  is `gitstore-api`-internal (unexported, datastore-adjacent); duplicating
  its owner-reference shape in the controller would create two independent
  implementations of the same synthesis that could drift.

## R8: One uniform resolution algorithm — no "admission already resolved it" fast path

**Decision**: The reconciler resolves `spec.categoryRef` against its own
CategoryTaxonomy cache on *every* reconcile, unconditionally — the same code
path whether the Product was just admitted with an already-existing
category (User Story 1), is still waiting on one (User Story 2), or is being
re-enqueued after its category was deleted (User Story 3). It never reads
`Product.OwnerReferences` to short-circuit this decision.

**Rationale**: The controller already needs read access to its own
CategoryTaxonomy cache for R3 (watch-driven re-enqueue) and R6 (bounded
retry), so resolving against it is not extra machinery — it is the same
lookup either way. Branching on "did admission already write an owner
reference" would require extending the lightweight `categorytaxonomy.Product`
cache entity (today: `UID, Namespace, Name, ResourceVersion, Finalizers,
DeletionTimestamp, CategoryRefName` — no owner-reference data) purely to
support a fast path that saves nothing (the cache lookup it would skip is
O(1)), while adding a second source of truth that could disagree with the
controller's own resolution during a race at push time.

**Alternatives considered**: Have the reconciler check
`Product.OwnerReferences` first and only fall back to a CategoryTaxonomy
cache lookup when absent — rejected per Simplicity: no measured benefit,
extends the Product cache entity for no reason, and introduces a
theoretical disagreement between two resolution mechanisms that the uniform
approach avoids by construction.
