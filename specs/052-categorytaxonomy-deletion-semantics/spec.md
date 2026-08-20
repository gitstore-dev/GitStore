# Feature Specification: CategoryTaxonomy Deletion Semantics, OwnerReferences, and Garbage Collection

**Feature Branch**: `052-categorytaxonomy-deletion-semantics`
**Created**: 2026-08-20
**Status**: Draft
**Input**: User description: "CategoryTaxonomy Deletion Semantics, OwnerReferences, and Garbage Collection (GH#243). Define and implement deletion semantics for CategoryTaxonomy resources when dependents (child categories or associated products) still exist. Determine garbage collection strategy via OwnerReferences. In scope: define deletion semantics (block vs cascade), define what happens to orphaned child categories, define what happens to products associated with a deleted category, define the GC strategy via controller reconciliation, integration tests covering deletion with and without dependents. Out of scope: CategoryTaxonomy creation/update validation (GH#82, spec 021), controller manager runtime (GH#165)."

## Clarifications

### Session 2026-08-20

- Q: GH#243 leaves an open design choice between "block delete when dependents exist" and "cascade via OwnerReferences GC." Which does this spec adopt? → A: Block-delete-until-drained (foreground-deletion finalizer + controller-driven drain + synchronous reject-on-precondition), for consistency with the already-shipped Namespace pattern (spec 046, `docs/ADRs/0002-namespace-lifecycle.md`) and Repository's synchronous precondition check (spec 041). No working cascading-GC mechanism exists anywhere in the codebase today to model a first cascade implementation on — `DeleteProduct` does not check for or cascade to `ProductVariant` rows, and no admission path populates `metadata.ownerReferences` to point at an owning resource despite `docs/ADRs/0006-category-taxonomy-lifecycle.md` describing that as already-decided policy. Introducing a first-of-its-kind cascade mechanism here, instead of reusing the pattern already proven for Namespace, would add a second, inconsistent deletion model to the codebase for no documented benefit.
- Q: GH#243's "Blocked by #165" dependency — is it still blocking? → A: No. #165's sub-issues (#180 controller-manager runtime foundations, #181 reconcile handler contract, #182 startup resume, #183 integration tests+runbook) are all closed and merged, and a full reconciler runtime for CategoryTaxonomy already exists and runs today (spec 039, shipped). This spec extends that existing reconciler; it does not wait on or rebuild any controller-manager infrastructure.

## User Scenarios & Testing *(mandatory)*

### User Story 1 - Deleting a category with live children is rejected, not silently applied (Priority: P1)

Today, deleting a `CategoryTaxonomy` file in a git push is admitted unconditionally: `deleteResource` in the admission pipeline hard-deletes the record with no check for other categories whose `spec.parentRef.name` still points at it. An author who deletes a parent category file while its children still exist gets no warning — the parent silently disappears while its children are left pointing at a name that no longer resolves, contradicting `docs/ADRs/0006-category-taxonomy-lifecycle.md`'s documented (but never wired) block-on-dependents rule.

**Why this priority**: This is the core correctness gap the issue exists to close. Without it, the category tree's integrity guarantee is fiction — the ADR describes protection that admission does not actually provide.

**Independent Test**: Can be fully tested by pushing a deletion of a category that has at least one other category referencing it via `spec.parentRef.name`, and confirming the delete is rejected and the parent record still exists afterward, with the child's own state unchanged — all without needing the GraphQL delete path or controller drain behavior to exist yet.

**Acceptance Scenarios**:

1. **Given** a category with at least one other category whose `spec.parentRef.name` points at it, **When** a git push deletes the parent's manifest file, **Then** the deletion is rejected, the parent record is unchanged in the datastore, and the rejection is distinguishable from a successful delete.
2. **Given** a category with no other category referencing it as a parent, **When** a git push deletes its manifest file, **Then** the deletion proceeds (subject to User Story 2's product check).
3. **Given** a category whose only child was itself deleted or re-parented away in an earlier, already-admitted push, **When** the (now childless) category's manifest is deleted, **Then** the deletion proceeds.

---

### User Story 2 - Deleting a category with assigned products is rejected, not silently applied (Priority: P1)

The same admission gap applies to products: `deleteResource` performs no check for `Product` records whose `spec.categoryRef.name` points at the category being deleted. A category with live product assignments can vanish, leaving those products pointing at a category name that no longer resolves.

**Why this priority**: Equal in importance to User Story 1 — both dependent types (children, products) are named explicitly in the governing ADR's block-on-dependents rule, and both are currently unenforced. Shipping one without the other leaves half the correctness gap open.

**Independent Test**: Can be fully tested by pushing a deletion of a category that at least one `Product` still references via `spec.categoryRef.name`, and confirming the delete is rejected and the category record still exists afterward.

**Acceptance Scenarios**:

1. **Given** a category with at least one `Product` whose `spec.categoryRef.name` points at it, **When** a git push deletes the category's manifest file, **Then** the deletion is rejected and the category record is unchanged.
2. **Given** a category with no products referencing it, **When** its manifest is deleted (and User Story 1's child check also passes), **Then** the deletion proceeds.
3. **Given** a category previously rejected for deletion due to assigned products, **When** every referencing product is later re-assigned to a different category or deleted, and the category's manifest deletion is pushed again, **Then** the deletion now proceeds.

---

### User Story 3 - The same deletion safety applies to the GraphQL API, not only git push (Priority: P1)

An operator or integration using the GraphQL API needs a working way to request category deletion without constructing a git commit by hand, and that path must enforce the exact same dependent checks as the git-push path — not a separate, weaker, or entirely absent one. Today, the GraphQL delete mutation is a stub that unconditionally returns an error and performs no checks, no git delegation, and no state change of any kind.

**Why this priority**: Equal priority to User Stories 1 and 2 — a safety rule that only one of two entry points enforces is not a safety rule. GraphQL callers are a first-class way this system is used, per the delegation pattern already implemented for `createNamespace`/`updateNamespace` (spec 046) and other resource kinds' create/update mutations.

**Independent Test**: Can be fully tested by calling the GraphQL category-deletion mutation against a category with live children or assigned products and confirming it is rejected with the same reasons as the git-push path, then calling it against an eligible (dependent-free) category and confirming deletion actually proceeds end-to-end — something the current stub can never do.

**Acceptance Scenarios**:

1. **Given** a category with live children or assigned products, **When** the GraphQL deletion mutation is called, **Then** it is rejected with a reason identifying which dependent type blocked it, and the category is unchanged.
2. **Given** a dependent-free, eligible category, **When** the GraphQL deletion mutation is called, **Then** the category is marked as being deleted (deletion marker plus foreground-deletion finalizer) and eventually removed, mirroring the Namespace deletion flow (spec 046).
3. **Given** a category already marked as being deleted, **When** the GraphQL deletion mutation is called again for the same category, **Then** it is treated as redundant with the existing in-progress deletion, not as a second, independent deletion attempt.

---

### User Story 4 - A category mid-deletion is visibly distinguishable, and its removal only happens once it is actually safe (Priority: P2)

Once a category passes its dependent checks and begins deleting, the system must never destroy its record before every removal condition is satisfied, and must expose that in-progress state so a reader (human or controller) can tell a normal category apart from one that is being torn down.

**Why this priority**: Lower urgency than User Stories 1-3 because it depends on those first passing (there is nothing to make visible until deletion is actually gated), but it is what makes the drain-then-remove sequence observable and trustworthy rather than an internal implementation detail no one can verify.

**Independent Test**: Can be fully tested by deleting an eligible category through the GraphQL mutation and observing its status expose an in-progress-deletion condition before the record disappears, using only reads against the existing status/condition surface.

**Acceptance Scenarios**:

1. **Given** a category that has passed its dependent checks and begun deletion, **When** its status is read before removal completes, **Then** it exposes a `Terminating=True` condition distinguishing it from an active category.
2. **Given** a category mid-deletion whose dependent counts are both still zero, **When** the controller next reconciles it, **Then** the category's record is finally removed and no further reconciliation of it occurs.
3. **Given** a category that somehow re-acquires a dependent between the initial check and final removal (e.g., a new child is created pointing at it before its drain completes), **When** the controller reconciles it, **Then** removal is withheld until that dependent is gone again — final removal never happens while any dependent still exists.

---

### Edge Cases

- What happens to a child category while its parent is `Terminating`? Nothing special — under the block-until-drained model, a parent's deletion is rejected/withheld for as long as any child exists, so a child is never actually left pointing at a parent that has been removed. No orphaned-child status condition is needed; the existing `ParentResolved` condition already correctly reports `True` for as long as the parent record still exists, which it always does while any child remains.
- What happens to a product assigned to a category while that category is `Terminating`? Nothing special, for the same reason: deletion is withheld for as long as any product still references the category, so a product is never left pointing at a category name that has vanished. The existing `CategoryResolved` condition (Product's own status) continues to report accurately.
- What happens if a category delete request race with a concurrent child-create or product-assignment targeting the same category? The dependent check is re-evaluated by the controller on every reconcile of the terminating category (User Story 4, Scenario 3), so a dependent created after the initial synchronous check but before final removal still blocks removal — there is no window in which removal can occur while a dependent exists.
- What happens to a category's own `ancestorPath`/hierarchy fields (depth, path, childCount, productCount) while it is `Terminating`? They continue to be computed exactly as before by the existing reconciler (spec 039); `Terminating` is an additional condition layered on top, not a replacement for the existing resolved-status computation.
- What happens on a redundant delete request (git push deleting an already-deleted-in-flight category's file again, or a second GraphQL deletion call)? Treated as a no-op against the existing in-progress deletion — it does not restart, duplicate, or advance the deletion beyond where it already was.
- What happens to `metadata.ownerReferences` on a `CategoryTaxonomy` record? It remains out of scope for this spec to populate or act on, since it is not needed for the block-until-drained model this spec adopts (see Assumptions) and no other part of the codebase currently populates or consumes it for GC purposes either.

## Requirements *(mandatory)*

### Functional Requirements

- **FR-001**: The system MUST reject a git-push deletion of a `CategoryTaxonomy` manifest when at least one other `CategoryTaxonomy` record's `spec.parentRef.name` points at the category being deleted, leaving the deleted category's record unchanged.
- **FR-002**: The system MUST reject a git-push deletion of a `CategoryTaxonomy` manifest when at least one `Product` record's `spec.categoryRef.name` points at the category being deleted, leaving the deleted category's record unchanged.
- **FR-003**: The system MUST provide a working GraphQL category-deletion mutation that performs the same two dependent checks (FR-001, FR-002) synchronously and rejects the request, identifying which dependent type blocked it, when either check fails.
- **FR-004**: The system MUST, upon an eligible GraphQL deletion request (both dependent checks pass), mark the category as being deleted using a deletion marker plus the existing foreground-deletion finalizer mechanism already used for Namespace (spec 046), before removing anything else.
- **FR-005**: The system MUST make a category marked as being deleted visibly distinguishable from an active category when its status is read, via a `Terminating` condition using the existing `ConditionTerminating` condition-type vocabulary.
- **FR-006**: The system MUST NOT hard-delete a `CategoryTaxonomy` record marked as being deleted until both dependent counts (children referencing it via `spec.parentRef.name`, products referencing it via `spec.categoryRef.name`) are zero at the time of removal.
- **FR-007**: The system MUST re-evaluate both dependent counts on every reconciliation of a category marked as being deleted, not only at the time the deletion request was first accepted, so a dependent created after acceptance still withholds final removal.
- **FR-008**: The system MUST treat a deletion request (via either git push or the GraphQL mutation) against a category already marked as being deleted as redundant with its existing in-progress deletion, not as a new, independently-tracked deletion attempt.
- **FR-009**: The system MUST leave a category's existing hierarchy-derived status fields (depth, path, child count, product count) computed exactly as they are today (spec 039) while the category is marked as being deleted, layering the `Terminating` condition on top rather than replacing that computation.
- **FR-010**: The system MUST NOT introduce a cascading, OwnerReferences-driven, or otherwise automatic deletion of a category's children or associated products as a result of deleting that category — dependents are never auto-removed as a side effect of this feature.

### Key Entities

- **Dependent child category**: Another `CategoryTaxonomy` record in the same namespace whose `spec.parentRef.name` equals the category being considered for deletion. Its existence unconditionally blocks that category's deletion.
- **Dependent product**: A `Product` record whose `spec.categoryRef.name` equals the category being considered for deletion. Its existence unconditionally blocks that category's deletion.
- **In-progress deletion (Terminating)**: The state a category enters once an eligible deletion request has been accepted and the foreground-deletion finalizer attached, prior to its record being finally removed. Distinguishable from an active category via a `Terminating` condition.
- **CategoryTaxonomy status conditions**: System-computed observations already including `ParentResolved`, `Acyclic`, `AncestorPathReady`/hierarchy resolution, and `Ready` (spec 039); this spec adds `Terminating` to that set.

### Status condition matrix

| Condition       | Source of truth                                  | Set by                 | Read semantics                                                                             |
|-----------------|---------------------------------------------------|-------------------------|---------------------------------------------------------------------------------------------|
| `ParentResolved`| datastore `Status.conditions` (spec 039)          | existing reconciler     | unchanged by this spec                                                                      |
| `Acyclic`       | datastore `Status.conditions` (spec 039)          | existing reconciler     | unchanged by this spec                                                                      |
| `Ready`         | datastore `Status.conditions` (spec 039)          | existing reconciler     | unchanged by this spec                                                                      |
| `Terminating`   | deletion marker + foreground-deletion finalizer   | system deletion flow    | `True` while the category is marked as being deleted and at least one dependent-check-and-drain cycle has not yet completed; absent otherwise |

## Success Criteria *(mandatory)*

### Measurable Outcomes

- **SC-001**: 100% of category deletions (via git push or the GraphQL mutation) attempted against a category with at least one live child are rejected, with zero instances of such a category's record being removed.
- **SC-002**: 100% of category deletions attempted against a category with at least one assigned product are rejected, with zero instances of such a category's record being removed.
- **SC-003**: 100% of eligible category deletions pass through a visibly distinguishable in-progress (`Terminating`) state before the record is removed, with zero instances of a category record disappearing without first being observable in that state.
- **SC-004**: 100% of category deletion requests against a category already marked as being deleted are treated as redundant, with zero instances of a second, independently-tracked deletion attempt being created.
- **SC-005**: Zero instances, across all deletion outcomes, of a child category or product being left referencing a category name that no longer resolves to any existing record.

## Assumptions

- **Design choice (block, not cascade)**: This spec resolves GH#243's open "block vs cascade" question in favor of block-delete-until-drained, matching the already-shipped Namespace pattern (spec 046) and Repository's synchronous precondition (spec 041), rather than a cascading OwnerReferences-based GC delete. See the Clarifications section for the full reasoning; the deciding factors are architectural consistency with the only working precedent in the codebase, and the absence of any existing cascade-GC mechanism to build a first implementation on.
- **`metadata.ownerReferences` remains unpopulated for `CategoryTaxonomy`**: `docs/ADRs/0006-category-taxonomy-lifecycle.md` describes `ownerReferences` being written to point at the owning repository, but no admission code path implements this today for any resource kind. Since this spec's chosen design (block-until-drained) does not require `ownerReferences` for either the dependent checks or the drain logic, populating it is out of scope here and left as a pre-existing, separately trackable gap rather than being silently folded into this spec's scope.
- **Extends, not replaces, the existing CategoryTaxonomy reconciler**: The spec 039 controller reconciler (`gitstore-controller-manager/internal/categorytaxonomy`) already computes child count and product count as part of its existing hierarchy resolution. This spec's drain logic is additive to that reconciler's existing responsibilities, not a parallel or competing reconciliation loop.
- **`#165` dependency is resolved**: GH#243's "Blocked by #165" is treated as no longer applicable; the controller-manager runtime #165 was tracking is shipped and already running the CategoryTaxonomy reconciler this spec extends.
- **Orphan status conditions are unnecessary by construction**: Because deletion is withheld for as long as any dependent exists, GH#243's acceptance criterion asking what happens to "orphaned child categories" and "products associated with a deleted category" is satisfied by the fact that neither can ever become orphaned under this design — there is no intermediate state in which a dependent points at a removed category. This is recorded as a deliberate design outcome, not an unanswered question.
- **Scope boundary with GH#82/spec 021 and GH#165**: Consistent with GH#243, this spec does not revisit `CategoryTaxonomy` creation/update validation or controller-manager runtime foundations; it only adds deletion-safety behavior on top of both.
