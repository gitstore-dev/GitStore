# Feature Specification: CategoryTaxonomy Deletion Semantics, OwnerReferences, and Garbage Collection

**Feature Branch**: `052-categorytaxonomy-deletion-semantics`
**Created**: 2026-08-20
**Status**: Draft
**Input**: User description: "CategoryTaxonomy Deletion Semantics, OwnerReferences, and Garbage Collection (GH#243). Define and implement deletion semantics for CategoryTaxonomy resources when dependents (child categories or associated products) still exist. Determine garbage collection strategy via OwnerReferences. In scope: define deletion semantics (block vs cascade), define what happens to orphaned child categories, define what happens to products associated with a deleted category, define the GC strategy via controller reconciliation, integration tests covering deletion with and without dependents. Out of scope: CategoryTaxonomy creation/update validation (GH#82, spec 021), controller manager runtime (GH#165)."

## Clarifications

### Session 2026-08-20

- Q: GH#243 leaves an open design choice between "block delete when dependents exist" and "cascade via OwnerReferences GC." Which does this spec adopt? → A: Block-delete-until-drained (foreground-deletion finalizer + controller-driven drain + synchronous reject-on-precondition), for consistency with the already-shipped Namespace pattern (spec 046, `docs/ADRs/0002-namespace-lifecycle.md`) and Repository's synchronous precondition check (spec 041). No working cascading-GC mechanism exists anywhere in the codebase today to model a first cascade implementation on — `DeleteProduct` does not check for or cascade to `ProductVariant` rows, and no admission path populates `metadata.ownerReferences` to point at an owning resource despite `docs/ADRs/0006-category-taxonomy-lifecycle.md` describing that as already-decided policy. Introducing a first-of-its-kind cascade mechanism here, instead of reusing the pattern already proven for Namespace, would add a second, inconsistent deletion model to the codebase for no documented benefit.
- Q: GH#243's "Blocked by #165" dependency — is it still blocking? → A: No. #165's sub-issues (#180 controller-manager runtime foundations, #181 reconcile handler contract, #182 startup resume, #183 integration tests+runbook) are all closed and merged, and a full reconciler runtime for CategoryTaxonomy already exists and runs today (spec 039, shipped). This spec extends that existing reconciler; it does not wait on or rebuild any controller-manager infrastructure.
- Q: Should assigned products really block category deletion the same way child categories do, or is there a lighter-weight resolution? → A: Reconsidered, per design review. Blocking on **child categories** stays: there is no safe default for a category left pointing at a `spec.parentRef` name that no longer resolves — it would silently break the `ancestorPath`/depth invariants the existing reconciler maintains (spec 039), and no automatic reparent target (grandparent? new root?) can be chosen without a policy decision this spec doesn't own. Blocking on **assigned products** is relaxed to an async decouple instead: a product with no category is already a normal, supported state in this catalog (it mirrors how an absent `spec.parentRef` on a category already means "root category" — a first-class state, not an error), so there is a safe default here that there isn't for children. Under the revised design, deleting a childless category proceeds even with assigned products: the request is accepted, the foreground-deletion finalizer is attached, and the existing reconciler decouples each referencing product by transitioning its `CategoryResolved` status condition to `False` with reason `CategoryDeleted` — reusing the exact unresolved-reference pattern `docs/ADRs/0006-category-taxonomy-lifecycle.md` already specifies for a `parentRef` that points nowhere (`ParentResolved=False`/`ParentNotFound`) — rather than mutating the product's git-authored `spec.categoryRef`, which the controller does not own (Git remains canonical for `.spec`, consistent with every other ADR in this codebase). The category is removed once its **child** count reaches zero; product count no longer gates removal. `docs/ADRs/0006-category-taxonomy-lifecycle.md` is amended alongside this spec to record the split.

## User Scenarios & Testing *(mandatory)*

### User Story 1 - Deleting a category with live children is rejected, not silently applied (Priority: P1)

Today, deleting a `CategoryTaxonomy` file in a git push is admitted unconditionally: `deleteResource` in the admission pipeline hard-deletes the record with no check for other categories whose `spec.parentRef.name` still points at it. An author who deletes a parent category file while its children still exist gets no warning — the parent silently disappears while its children are left pointing at a name that no longer resolves, contradicting `docs/ADRs/0006-category-taxonomy-lifecycle.md`'s documented (but never wired) block-on-dependents rule.

**Why this priority**: This is the core correctness gap the issue exists to close. Without it, the category tree's integrity guarantee is fiction — the ADR describes protection that admission does not actually provide.

**Independent Test**: Can be fully tested by pushing a deletion of a category that has at least one other category referencing it via `spec.parentRef.name`, and confirming the delete is rejected and the parent record still exists afterward, with the child's own state unchanged — all without needing the GraphQL delete path or controller drain behavior to exist yet.

**Acceptance Scenarios**:

1. **Given** a category with at least one other category whose `spec.parentRef.name` points at it, **When** a git push deletes the parent's manifest file, **Then** the deletion is rejected, the parent record is unchanged in the datastore, and the rejection is distinguishable from a successful delete.
2. **Given** a category with no other category referencing it as a parent, **When** a git push deletes its manifest file, **Then** the deletion proceeds regardless of assigned-product count (per User Story 2, products never block deletion).
3. **Given** a category whose only child was itself deleted or re-parented away in an earlier, already-admitted push, **When** the (now childless) category's manifest is deleted, **Then** the deletion proceeds.

---

### User Story 2 - Deleting a category with assigned products proceeds, and those products become Uncategorized (Priority: P1)

The same admission gap exists for products today: `deleteResource` performs no check at all for `Product` records whose `spec.categoryRef.name` points at the category being deleted, so a category with live product assignments can vanish, leaving those products pointing at a category name that no longer resolves, with no observable signal that anything changed. The fix is not to block on products the same way as children (see Clarifications): a product with no category is already a normal, supported state in this catalog, so deletion of a childless category is allowed to proceed even with assigned products, and each affected product's own status is updated to reflect that its category reference no longer resolves.

**Why this priority**: Equal in importance to User Story 1 — the underlying correctness gap (a dependent silently left pointing at nothing) is real for both dependent types, even though the two types resolve differently. Shipping the children fix without also fixing the currently-silent product case leaves products in exactly the same "points at nothing, no one told me" state this issue exists to close.

**Independent Test**: Can be fully tested by pushing a deletion of a childless category that at least one `Product` still references via `spec.categoryRef.name`, confirming the deletion proceeds (unlike User Story 1), and confirming the referencing product's own status subsequently reports its category reference as unresolved rather than continuing to claim a category that no longer exists.

**Acceptance Scenarios**:

1. **Given** a childless category with at least one `Product` whose `spec.categoryRef.name` points at it, **When** a git push deletes the category's manifest file, **Then** the deletion proceeds (the finalizer/drain flow of User Story 4 applies), not rejected.
2. **Given** a category deletion has proceeded per Scenario 1, **When** the controller next reconciles each product that referenced the deleted category, **Then** that product's `CategoryResolved` condition transitions to `False` with reason `CategoryDeleted`, without any change to the product's own git-authored `spec.categoryRef` field.
3. **Given** a product whose `CategoryResolved` condition is `False`/`CategoryDeleted`, **When** an author later pushes an update to that product's manifest with a different, valid `spec.categoryRef` (or removes the field entirely), **Then** the product's category resolution is re-evaluated exactly as it would be for any other categoryRef change, with no special-casing left over from the deletion.

---

### User Story 3 - The same deletion safety applies to the GraphQL API, not only git push (Priority: P1)

An operator or integration using the GraphQL API needs a working way to request category deletion without constructing a git commit by hand, and that path must enforce the exact same rules as the git-push path — not a separate, weaker, or entirely absent one. Today, the GraphQL delete mutation is a stub that unconditionally returns an error and performs no checks, no git delegation, and no state change of any kind.

**Why this priority**: Equal priority to User Stories 1 and 2 — a safety rule that only one of two entry points enforces is not a safety rule. GraphQL callers are a first-class way this system is used, per the delegation pattern already implemented for `createNamespace`/`updateNamespace` (spec 046) and other resource kinds' create/update mutations.

**Independent Test**: Can be fully tested by calling the GraphQL category-deletion mutation against a category with live children and confirming it is rejected with the same reason as the git-push path; calling it against a childless category with assigned products and confirming deletion proceeds (per User Story 2, not rejected); and calling it against a fully dependent-free category and confirming deletion proceeds end-to-end — none of which the current stub can do.

**Acceptance Scenarios**:

1. **Given** a category with live children, **When** the GraphQL deletion mutation is called, **Then** it is rejected identifying children as the blocking reason, and the category is unchanged.
2. **Given** a childless category, whether or not it has assigned products, **When** the GraphQL deletion mutation is called, **Then** the category is marked as being deleted (deletion marker plus foreground-deletion finalizer) and eventually removed, mirroring the Namespace deletion flow (spec 046) — assigned products never block this path, per User Story 2.
3. **Given** a category already marked as being deleted, **When** the GraphQL deletion mutation is called again for the same category, **Then** it is treated as redundant with the existing in-progress deletion, not as a second, independent deletion attempt.

---

### User Story 4 - A category mid-deletion is visibly distinguishable, and its removal only happens once it is actually safe (Priority: P2)

Once a category passes its dependent checks and begins deleting, the system must never destroy its record before every removal condition is satisfied, and must expose that in-progress state so a reader (human or controller) can tell a normal category apart from one that is being torn down.

**Why this priority**: Lower urgency than User Stories 1-3 because it depends on those first passing (there is nothing to make visible until deletion is actually gated), but it is what makes the drain-then-remove sequence observable and trustworthy rather than an internal implementation detail no one can verify.

**Independent Test**: Can be fully tested by deleting an eligible category through the GraphQL mutation and observing its status expose an in-progress-deletion condition before the record disappears, using only reads against the existing status/condition surface.

**Acceptance Scenarios**:

1. **Given** a category that has passed its dependent checks and begun deletion, **When** its status is read before removal completes, **Then** it exposes a `Terminating=True` condition distinguishing it from an active category.
2. **Given** a category mid-deletion whose child count is still zero, **When** the controller next reconciles it, **Then** the category's record is finally removed and no further reconciliation of it occurs, regardless of its product count.
3. **Given** a category that somehow re-acquires a child between the initial check and final removal (e.g., a new child is created pointing at it before its drain completes), **When** the controller reconciles it, **Then** removal is withheld until that child is gone again — final removal never happens while any child still exists. (Products re-acquired during this window do not withhold removal — see User Story 2 and the Edge Cases below.)

---

### Edge Cases

- What happens to a child category while its parent is `Terminating`? Nothing special — under the block-until-drained model, a parent's deletion is rejected/withheld for as long as any child exists, so a child is never actually left pointing at a parent that has been removed. No orphaned-child status condition is needed; the existing `ParentResolved` condition already correctly reports `True` for as long as the parent record still exists, which it always does while any child remains.
- What happens to a product assigned to a category while that category is `Terminating` (draining for other reasons, e.g. a lingering child) or being finally removed? It is decoupled, not orphaned-and-ignored: the controller sets that product's `CategoryResolved` condition to `False`/`CategoryDeleted` as part of the same reconcile pass that processes the category's deletion, so there is no window in which a product's status silently continues to claim a category that is gone.
- Can a new product be created, or an existing product updated, to reference a category that is already `Terminating`? No — admission rejects a `categoryRef` pointing at a category already marked `Terminating`, the same way admission already rejects new resources being created under a `Terminating` Namespace (spec 046) — this avoids a race where a brand-new reference is created against a category that is about to disappear, only to immediately need decoupling.
- What happens if a category delete request races with a concurrent child-create targeting the same category? The child-dependent check is re-evaluated by the controller on every reconcile of the terminating category (User Story 4, Scenario 3), so a child created after the initial synchronous check but before final removal still blocks removal — there is no window in which removal can occur while a child still exists. (No equivalent race exists for products, since they no longer gate removal.)
- What happens to a category's own `ancestorPath`/hierarchy fields (depth, path, childCount, productCount) while it is `Terminating`? They continue to be computed exactly as before by the existing reconciler (spec 039); `Terminating` is an additional condition layered on top, not a replacement for the existing resolved-status computation. `productCount` in particular continues to reflect however many products still reference the category at each point in time, including as that count drops to zero once decoupling completes.
- What happens on a redundant delete request (git push deleting an already-deleted-in-flight category's file again, or a second GraphQL deletion call)? Treated as a no-op against the existing in-progress deletion — it does not restart, duplicate, or advance the deletion beyond where it already was.
- What happens to `metadata.ownerReferences` on a `CategoryTaxonomy` record? It remains out of scope for this spec to populate or act on, since neither the children-block nor the products-decouple mechanism this spec adopts requires it (see Assumptions), and no other part of the codebase currently populates or consumes it for GC purposes either.

## Requirements *(mandatory)*

### Functional Requirements

- **FR-001**: The system MUST reject a git-push deletion of a `CategoryTaxonomy` manifest when at least one other `CategoryTaxonomy` record's `spec.parentRef.name` points at the category being deleted, leaving the deleted category's record unchanged.
- **FR-002**: The system MUST NOT reject a git-push deletion of a `CategoryTaxonomy` manifest solely because at least one `Product` record's `spec.categoryRef.name` points at the category being deleted — assigned products MUST NOT block deletion, only child categories (FR-001) do.
- **FR-003**: The system MUST provide a working GraphQL category-deletion mutation that performs the same child-dependent check (FR-001) synchronously and rejects the request, identifying children as the blocking reason, when that check fails; the mutation MUST NOT reject solely for assigned products, per FR-002.
- **FR-004**: The system MUST, upon an eligible deletion request (the child check passes, regardless of assigned-product count) via either git push or the GraphQL mutation, mark the category as being deleted using a deletion marker plus the existing foreground-deletion finalizer mechanism already used for Namespace (spec 046), before removing anything else.
- **FR-005**: The system MUST make a category marked as being deleted visibly distinguishable from an active category when its status is read, via a `Terminating` condition using the existing `ConditionTerminating` condition-type vocabulary.
- **FR-006**: The system MUST NOT hard-delete a `CategoryTaxonomy` record marked as being deleted until its child count (via `spec.parentRef.name`) is zero at the time of removal; the product count is not part of this gate.
- **FR-007**: The system MUST re-evaluate the child count on every reconciliation of a category marked as being deleted, not only at the time the deletion request was first accepted, so a child created after acceptance still withholds final removal.
- **FR-008**: The system MUST treat a deletion request (via either git push or the GraphQL mutation) against a category already marked as being deleted as redundant with its existing in-progress deletion, not as a new, independently-tracked deletion attempt.
- **FR-009**: The system MUST leave a category's existing hierarchy-derived status fields (depth, path, child count, product count) computed exactly as they are today (spec 039) while the category is marked as being deleted, layering the `Terminating` condition on top rather than replacing that computation.
- **FR-010**: The system MUST NOT introduce a cascading, OwnerReferences-driven, or otherwise automatic deletion of a category's children as a result of deleting that category — children are never auto-removed as a side effect of this feature. (Products are not auto-removed either; they are decoupled per FR-011, not deleted.)
- **FR-011**: For every `Product` whose `spec.categoryRef.name` points at a category marked as being deleted, the system MUST, as part of reconciling that category, transition the product's `CategoryResolved` status condition to `False` with reason `CategoryDeleted` — reusing the existing unresolved-reference pattern already defined for a `CategoryTaxonomy`'s own unresolved `spec.parentRef` (`ParentResolved=False`/`ParentNotFound`, `docs/ADRs/0006-category-taxonomy-lifecycle.md`) — without modifying the product's git-authored `spec.categoryRef` field, which remains under Git's ownership.
- **FR-012**: The system MUST reject, at admission, a `Product` create or update whose `spec.categoryRef.name` points at a `CategoryTaxonomy` already marked as being deleted (`Terminating`), mirroring the existing rejection of new resources targeting a `Terminating` Namespace (spec 046) — this prevents a newly created reference from immediately requiring decoupling.

### Key Entities

- **Dependent child category**: Another `CategoryTaxonomy` record in the same namespace whose `spec.parentRef.name` equals the category being considered for deletion. Its existence unconditionally blocks that category's deletion.
- **Dependent product**: A `Product` record whose `spec.categoryRef.name` equals the category being considered for deletion. Its existence does not block deletion; instead, it is decoupled (its `CategoryResolved` condition is set to `False`/`CategoryDeleted`) as part of the category's deletion reconcile.
- **In-progress deletion (Terminating)**: The state a category enters once an eligible deletion request (child count zero, regardless of product count) has been accepted and the foreground-deletion finalizer attached, prior to its record being finally removed. Distinguishable from an active category via a `Terminating` condition.
- **CategoryTaxonomy status conditions**: System-computed observations already including `ParentResolved`, `Acyclic`, `AncestorPathReady`/hierarchy resolution, and `Ready` (spec 039); this spec adds `Terminating` to that set.
- **Product `CategoryResolved` condition**: An existing Product status condition (referenced by `docs/ADRs/0006-category-taxonomy-lifecycle.md` alongside `ParentResolved`) that this spec repurposes: previously only ever observed transitioning to `False`/`CategoryNotFound` for a `categoryRef` that never resolved, it now also transitions to `False`/`CategoryDeleted` when its target category is deleted out from under it — a distinct, diagnosable reason for a state a caller could otherwise confuse with a simple typo.

### Status condition matrix

| Condition                       | Source of truth                                  | Set by                 | Read semantics                                                                             |
|----------------------------------|---------------------------------------------------|-------------------------|---------------------------------------------------------------------------------------------|
| `ParentResolved` (CategoryTaxonomy) | datastore `Status.conditions` (spec 039)      | existing reconciler     | unchanged by this spec                                                                      |
| `Acyclic` (CategoryTaxonomy)     | datastore `Status.conditions` (spec 039)          | existing reconciler     | unchanged by this spec                                                                      |
| `Ready` (CategoryTaxonomy)       | datastore `Status.conditions` (spec 039)          | existing reconciler     | unchanged by this spec                                                                      |
| `Terminating` (CategoryTaxonomy) | deletion marker + foreground-deletion finalizer  | system deletion flow    | `True` while the category is marked as being deleted and its child count has not yet reached zero at removal time; absent otherwise. Product count does not affect this condition. |
| `CategoryResolved` (Product)     | datastore `Status.conditions`                    | this spec's reconcile of the deleted category | `False`/`CategoryDeleted` once the referenced category is deleted; unaffected for products referencing any category that is not being deleted |

## Success Criteria *(mandatory)*

### Measurable Outcomes

- **SC-001**: 100% of category deletions (via git push or the GraphQL mutation) attempted against a category with at least one live child are rejected, with zero instances of such a category's record being removed.
- **SC-002**: 100% of category deletions attempted against a childless category proceed regardless of assigned-product count, with zero instances of such a deletion being rejected on account of assigned products.
- **SC-003**: 100% of eligible category deletions pass through a visibly distinguishable in-progress (`Terminating`) state before the record is removed, with zero instances of a category record disappearing without first being observable in that state.
- **SC-004**: 100% of category deletion requests against a category already marked as being deleted are treated as redundant, with zero instances of a second, independently-tracked deletion attempt being created.
- **SC-005**: Zero instances, across all deletion outcomes, of a child category being left referencing a parent category name that no longer resolves to any existing record; and, separately, zero instances of a product continuing to report a resolved category after that category has been deleted (its `CategoryResolved` condition must transition to `False`/`CategoryDeleted` instead).

## Assumptions

- **Design choice (hybrid: block on children, decouple on products)**: This spec resolves GH#243's open "block vs cascade" question with a split answer rather than picking one for both dependent types. Children: block-delete-until-drained (foreground-deletion finalizer + controller-driven drain + synchronous reject-on-precondition), matching the already-shipped Namespace pattern (spec 046) and Repository's synchronous precondition (spec 041) — there is no safe default for an orphaned child, so consistency with the only working precedent in the codebase governs. Products: async decouple via status, not block and not cascade-delete — a product with no category is already a normal, first-class state in this catalog (mirroring how an absent `parentRef` already means "root category" for a `CategoryTaxonomy`), so there is a safe default here that doesn't exist for children, and reusing the existing unresolved-reference (`*Resolved=False`) pattern needs no new mechanism. See the Clarifications section for the full reasoning behind this revision from the original all-block draft.
- **`metadata.ownerReferences` remains unpopulated for `CategoryTaxonomy`**: `docs/ADRs/0006-category-taxonomy-lifecycle.md` describes `ownerReferences` being written to point at the owning repository, but no admission code path implements this today for any resource kind. Neither this spec's children-block mechanism nor its products-decouple mechanism requires `ownerReferences`, so populating it remains out of scope here and is left as a pre-existing, separately trackable gap rather than being silently folded into this spec's scope.
- **Extends, not replaces, the existing CategoryTaxonomy reconciler**: The spec 039 controller reconciler (`gitstore-controller-manager/internal/categorytaxonomy`) already computes child count and product count as part of its existing hierarchy resolution. This spec's drain logic (children) and decouple logic (products) are both additive to that reconciler's existing responsibilities, not a parallel or competing reconciliation loop.
- **`#165` dependency is resolved**: GH#243's "Blocked by #165" is treated as no longer applicable; the controller-manager runtime #165 was tracking is shipped and already running the CategoryTaxonomy reconciler this spec extends.
- **Orphaned child categories are unnecessary by construction; orphaned products are not — they're decoupled, not left silent**: Because deletion is withheld for as long as any child exists, GH#243's question of what happens to "orphaned child categories" is satisfied by the fact that a child can never actually become orphaned under this design. The parallel question for "products associated with a deleted category" is answered differently and deliberately: such a product is allowed to exist in an unresolved-category state (that's the whole point of not blocking on it), but it is never left silent about it — `CategoryResolved=False`/`CategoryDeleted` makes the state observable, which is what distinguishes "intentionally decoupled" from the "silently pointing at nothing" gap this issue exists to close.
- **Scope boundary with GH#82/spec 021 and GH#165**: Consistent with GH#243, this spec does not revisit `CategoryTaxonomy` creation/update validation or controller-manager runtime foundations; it only adds deletion-safety (children) and deletion-decoupling (products) behavior on top of both.
