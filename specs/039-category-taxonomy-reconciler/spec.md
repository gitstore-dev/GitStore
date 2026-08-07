# Feature Specification: CategoryTaxonomy Controller Reconciliation

**Feature Branch**: `039-category-taxonomy-reconciler`  
**Created**: 2026-08-07  
**Status**: Draft  
**Input**: User description: "CategoryTaxonomy Controller Reconciliation: implement the controller reconciliation loop for CategoryTaxonomy resources per GitHub issue #244. Compute and write CategoryTaxonomyStatus.resolved (hierarchy depth, materialized ancestorPath e.g. electronics/computers/laptops, child count, product count). Set ParentResolved, Acyclic, and Ready status conditions after each reconcile. Surface File reference existence as a status condition for optional:false media entries (FR-009 controller portion, related #165) without rejecting the push. Include integration tests covering a depth-3 hierarchy, cycle detection via status, and the file-ref condition, plus runbook/observability notes for reconciliation lag. Out of scope: CategoryTaxonomy push-time validation (covered by spec 021/#204-206) and deletion/GC semantics (covered by #243). Uses the reconcile handler interface from #181 (spec 026) and the controller manager runtime from #180 (spec 025). Resolves the AncestorPath staleness-on-parent-move/delete edge case noted from prior work."

## User Scenarios & Testing *(mandatory)*

### User Story 1 - Category hierarchy status stays accurate after any change (Priority: P1)

As a catalog author, when I push a change that creates, moves, or removes a `CategoryTaxonomy` node, I want the system to recompute and expose accurate hierarchy information (depth, full ancestry path, child count, product count) for that node and every descendant, without me needing to re-push files I didn't touch.

**Why this priority**: This is the core value of the feature — the materialized hierarchy view (`ancestorPath`, `depth`) is what every downstream consumer (storefront navigation, breadcrumb rendering, admin UI) relies on. Stale hierarchy data silently breaks navigation and is invisible until a customer or admin notices misplaced categories.

**Independent Test**: Can be fully tested by pushing a 3-level-deep category hierarchy (e.g. `electronics` → `computers` → `laptops`), verifying each node's status shows the correct `depth` and `ancestorPath`, then reparenting `computers` under a different root and confirming `laptops`'s status (a node not touched by the reparenting push) updates to reflect the new path — without requiring a follow-up push that touches `laptops` directly.

**Acceptance Scenarios**:

1. **Given** a newly admitted 3-level category hierarchy (`electronics/computers/laptops`), **When** the controller reconciles each node, **Then** each node's `status.resolved.depth` and `status.resolved.ancestorPath` match its position in the hierarchy (root depth 0, path equal to its own name; leaf depth 2, path `electronics/computers/laptops`).
2. **Given** an existing category `computers` with children `laptops` and `desktops`, **When** a push changes `computers`'s `parentRef` to point at a different parent category, **Then** the controller updates `computers`'s own `ancestorPath`/`depth` and also updates `laptops` and `desktops` (and any of their descendants) to reflect the new ancestry, even though those descendant files were not part of the triggering push.
3. **Given** a category with two children and three products assigned to it, **When** the controller reconciles that category, **Then** `status.resolved.childCount` equals 2 and `status.resolved.productCount` equals 3.
4. **Given** a category is deleted, **When** the controller reconciles its former children, **Then** their `ancestorPath` and `depth` are recomputed relative to their next-available ancestor (or promoted to root if the deleted category had no parent).

---

### User Story 2 - Parent-resolution and cycle conditions are trustworthy (Priority: P2)

As a catalog author or an engineer debugging a broken storefront category tree, I want the `ParentResolved`, `Acyclic`, and `Ready` status conditions on a `CategoryTaxonomy` to accurately reflect whether its parent reference is valid and whether it participates in a cycle, so that I can diagnose hierarchy problems from status alone without inspecting raw data.

**Why this priority**: Conditions are the observable contract consumers and operators use to detect problems. Without trustworthy conditions, the only way to detect a broken hierarchy is a full manual audit.

**Independent Test**: Can be fully tested by pushing a category whose `parentRef` names a category that doesn't exist, and confirming `ParentResolved=False` is set with `Ready=False`; separately, by constructing a parent/child cycle across two pushes and confirming both participants report `Acyclic=False`.

**Acceptance Scenarios**:

1. **Given** a category whose `spec.parentRef.name` does not match any existing `CategoryTaxonomy` in the same namespace, **When** the controller reconciles it, **Then** `ParentResolved=False` and `Ready=False`, with a human-readable `reason`/`message` identifying the missing parent.
2. **Given** a previously unresolved parent reference that is now satisfied by a category admitted in a later push, **When** the controller reconciles the child again, **Then** `ParentResolved` transitions to `True` and `Ready` reflects the combination of all conditions being satisfied.
3. **Given** two categories `A` and `B` where `A.parentRef = B` and `B.parentRef = A` (a direct cycle), **When** the controller reconciles both, **Then** both report `Acyclic=False` and `Ready=False`, and neither node's `ancestorPath`/`depth` is computed from the cyclic chain (existing values are left unchanged or a defined sentinel is used).
4. **Given** a cycle is broken by a subsequent push (e.g. `A`'s `parentRef` is removed), **When** the controller reconciles the formerly cyclic nodes, **Then** `Acyclic` transitions to `True` and hierarchy fields are recomputed normally.

---

### User Story 3 - Missing required media is visible without blocking publication (Priority: P3)

As a catalog author, when I reference a `File` resource from a `CategoryTaxonomy`'s media list with `optional: false` and that file doesn't exist (yet, or due to a typo), I want the system to accept my push and flag the problem in status, rather than rejecting the entire push, so that I can fix the category description or add the missing file independently.

**Why this priority**: Lower priority than hierarchy correctness and cycle detection because it affects a single node's status rather than the shape of the whole tree, and the underlying `File` resource type is not yet fully implemented elsewhere in the system — this story's scope is limited to what the reconciler can determine today.

**Independent Test**: Can be fully tested by pushing a `CategoryTaxonomy` with a media entry that has `optional: false` and a `fileRef.name` that does not resolve, and confirming the push succeeds while the status carries a condition describing the unresolved required reference.

**Acceptance Scenarios**:

1. **Given** a `CategoryTaxonomy` with a media entry `{fileRef: {name: "missing-image"}, optional: false}`, **When** the push is admitted and the controller reconciles the resource, **Then** the push is not rejected and the resource's status carries a condition indicating the required file reference could not be confirmed.
2. **Given** a `CategoryTaxonomy` with a media entry `{fileRef: {name: "missing-image"}, optional: true}`, **When** the controller reconciles the resource, **Then** no failing condition is raised for that entry.
3. **Given** the `File` existence check cannot be performed because the referenced resource type is not yet queryable in the system, **When** the controller reconciles a resource with an `optional: false` media entry, **Then** the condition it sets communicates "unresolved" rather than falsely asserting the file is confirmed present.

---

### Edge Cases

- What happens when a category's `parentRef` points at itself (`metadata.name == spec.parentRef.name`)? This is caught as a single-node cycle and reported the same way as a multi-node cycle (`Acyclic=False`).
- What happens when a very deep hierarchy is created (e.g. 20+ levels)? Depth and ancestorPath must still compute correctly; no fixed depth ceiling is assumed by this feature (any limit is a separate concern).
- What happens when a reconcile is triggered for a category whose parent is currently mid-reconciliation (parent's own `ancestorPath` isn't updated yet)? The child's reconcile must either wait/requeue or produce a transient (retryable) result rather than writing an incorrect path — descendants are only recomputed after their ancestor's path is settled.
- What happens when the same category is reconciled twice with no observed changes (steady state)? No redundant status write should occur.
- What happens when a category has zero children and zero products? `childCount` and `productCount` are both `0`, not omitted.
- What happens when a status write conflicts because the resource changed concurrently (optimistic concurrency failure)? The reconciler retries with fresh state rather than overwriting the newer version.

## Requirements *(mandatory)*

### Functional Requirements

- **FR-001**: The system MUST compute, for every `CategoryTaxonomy` resource, a hierarchy depth (root = 0, incrementing by 1 per level) and write it to `status.resolved.depth`.
- **FR-002**: The system MUST compute, for every `CategoryTaxonomy` resource, a materialized ancestor path expressed as a slash-separated sequence of ancestor names from root to the resource itself (e.g. `electronics/computers/laptops`), and write it to `status.resolved.ancestorPath`.
- **FR-003**: The system MUST compute the direct child count for every `CategoryTaxonomy` resource and write it to `status.resolved.childCount`.
- **FR-004**: The system MUST compute the count of products currently assigned to a category and write it to `status.resolved.productCount`.
- **FR-005**: When a category's ancestry changes (its own `parentRef` changes, or an ancestor is deleted or reparented), the system MUST recompute `ancestorPath` and `depth` for that category and for all of its transitive descendants, even when those descendants were not part of the triggering push.
- **FR-006**: The system MUST set a `ParentResolved` condition to `True` when `spec.parentRef` is absent or refers to an existing `CategoryTaxonomy` in the same namespace, and to `False` (with a reason/message identifying the problem) when `spec.parentRef` refers to a name that does not resolve.
- **FR-007**: The system MUST set an `Acyclic` condition to `False` for every `CategoryTaxonomy` that participates in a parent-reference cycle (including a category referencing itself), and to `True` otherwise.
- **FR-008**: The system MUST NOT compute or update `ancestorPath`/`depth` for a category currently participating in a detected cycle; existing values are left as previously computed until the cycle is resolved.
- **FR-009**: The system MUST set a `Ready` condition that is `True` only when all other applicable conditions (`ParentResolved`, `Acyclic`, and any required-file-reference conditions) are `True`, and `False` otherwise.
- **FR-010**: For each media entry in `spec.media` with `optional: false`, the system MUST set a status condition indicating whether the referenced resource could be confirmed to exist; the push itself MUST NOT be rejected based on this check.
- **FR-011**: For each media entry in `spec.media` with `optional: true`, the system MUST NOT raise a failing condition solely because the referenced resource could not be confirmed to exist.
- **FR-012**: The system MUST set `status.observedGeneration` to the `metadata.generation` value observed at the start of each successful reconcile, so that a status-only write does not trigger a redundant re-reconcile.
- **FR-013**: The system MUST skip writing a status update when the computed status is identical to the currently observed status (no-op suppression), to avoid unnecessary write traffic under steady state.
- **FR-014**: The system MUST retry reconciliation (without data loss or partial writes) when a status write is rejected due to a concurrent modification of the target resource.
- **FR-015**: Integration tests MUST cover: a depth-3 hierarchy with correct `depth`/`ancestorPath`/`childCount`/`productCount` at every level; a cycle scenario where `Acyclic=False` is observable in status for all cycle participants; and the required-file-reference condition scenario for both `optional: false` and `optional: true` media entries.
- **FR-016**: Operational documentation MUST describe how to detect and diagnose reconciliation lag for `CategoryTaxonomy` resources (e.g. what signals indicate the controller has fallen behind, and what remediation steps an operator should take).

### Key Entities

- **CategoryTaxonomy**: A named catalog resource representing one node in the product category hierarchy. Has an author-controlled `spec` (title, optional `parentRef`, optional media list) and a controller-owned `status`.
- **CategoryTaxonomyStatus.resolved**: The controller-computed hierarchy aggregate for a category — `depth`, `ancestorPath`, `childCount`, `productCount`. Never written by the push pipeline; only by the controller.
- **Condition**: A named, typed status signal (`ParentResolved`, `Acyclic`, `Ready`, and a required-file-reference condition) with a status of `True`/`False`/`Unknown`, an observed generation, a last-transition timestamp, and an optional human-readable reason/message.
- **ParentRef**: A reference from a `CategoryTaxonomy` to another `CategoryTaxonomy` in the same namespace, forming the adjacency pointer that the materialized `ancestorPath` is derived from.
- **MediaDefinition**: A named reference to a `File` resource with an `optional` flag; only entries with `optional: false` participate in the required-file-reference condition.

## Success Criteria *(mandatory)*

### Measurable Outcomes

- **SC-001**: After any push that changes a category's position in the hierarchy, every affected node (the changed node and all its transitive descendants) shows correct `depth` and `ancestorPath` in status within one reconciliation cycle, without requiring a manual re-push of unaffected descendant files.
- **SC-002**: 100% of categories participating in a parent-reference cycle are observable as `Acyclic=False` in status, and no cyclic category's `ancestorPath`/`depth` reflects a computation that walked through the cycle.
- **SC-003**: A catalog author referencing a missing required file in a category's media list has their push accepted, and can see the specific problem by inspecting that category's status conditions — no push is ever rejected solely due to an unresolved `optional: false` file reference.
- **SC-004**: Under steady state (no hierarchy or product-count changes), reconciling an already-correct category produces zero additional status write calls.
- **SC-005**: An operator following the reconciliation-lag runbook can determine, from documented signals alone, whether the `CategoryTaxonomy` controller is keeping up with incoming changes, without reading controller source code.

## Assumptions

- The `File` resource kind (GitHub issue #79) is not yet implemented as a queryable datastore entity at the time of this feature. The required-file-reference condition (User Story 3 / FR-010, FR-011) is scoped to what the reconciler can determine with the datastore access available today; if no reliable existence check is possible, the condition communicates an "unresolved/unknown" state rather than a false positive or false negative. Full semantic verification against a real `File` record is deferred to when issue #79 lands (tracked in the ADR 0008 file-lifecycle reference-checking model).
- "Products assigned to a category" for `productCount` (FR-004) means products whose resolved category reference (per spec 021/first-party category resolution) names this `CategoryTaxonomy`.
- Reconciliation is level-triggered: the controller reads current resource state from its cache at dispatch time rather than acting on a specific historical event payload, consistent with the existing reconcile handler contract (spec 026).
- Namespace scoping applies throughout: `parentRef` and `fileRef` resolution only considers resources within the same namespace as the referencing `CategoryTaxonomy`.
