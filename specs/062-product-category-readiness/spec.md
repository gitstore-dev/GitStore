# Feature Specification: Product Category and Readiness Reconciliation

**Feature Branch**: `062-product-category-readiness`
**Created**: 2026-09-18
**Status**: Draft
**Input**: User description: "Product category and readiness reconciliation: the Product controller (gitstore-controller-manager/internal/product) currently only handles foreground-deletion completion. Per ADR-0004 (docs/ADRs/0004-product-lifecycle.md), the controller must also resolve spec.categoryRef against the existing CategoryTaxonomy datastore/cache and write CategoryResolved and Ready conditions onto Product status via the updateProductStatus GraphQL mutation. MediaResolved is explicitly out of scope (deferred to Phase 2 / GH#244 per the ADR). CategoryResolved=False with reason CategoryNotFound is non-blocking (push accepted, controller retries). Must reuse the existing Product list-watch cache and CategoryTaxonomy cache already wired in gitstore-controller-manager/cmd/controller/main.go, mirroring the existing pattern used for Repository's status.resolved writeback and CategoryTaxonomy's own parent-resolution reconciler as prior art. Must preserve spec 055's constraint that Terminating stays derived (never independently written) and that the existing Product-to-CategoryTaxonomy count fan-out (spec 042) is unaffected."

## Clarifications

### Session 2026-09-18

- Q: Should the controller re-enqueue a Product whose categoryRef previously failed to resolve when the matching CategoryTaxonomy is later created (or renamed into a match)? → A: Yes, watch-driven — CategoryTaxonomy create/rename events trigger re-enqueue of Products with a matching unresolved categoryRef.
- Q: When CategoryResolved=False/CategoryNotFound, how long should the controller keep retrying? → A: Indefinite, bounded-interval requeue — the author can fix the categoryRef in git at any time and it will still resolve.
- Q: Is CrossNamespaceRef reachable, given admission already rejects cross-namespace categoryRef at push/mutation time? → A: Drop it from this feature — only CategoryResolved=False/CategoryNotFound is implemented; a defensive cross-namespace terminal condition is out of scope until a real reachable path exists.

## User Scenarios & Testing *(mandatory)*

### User Story 1 - Product becomes Ready once its category resolves (Priority: P1)

An author pushes a Product manifest whose `spec.categoryRef` points at a CategoryTaxonomy that already exists in the same namespace. After admission accepts the push, the controller resolves the reference and the Product's status converges to `CategoryResolved=True` and `Ready=True` without any further author action.

**Why this priority**: This is the core value of the feature — closing the gap between "admitted" and "actually usable," matching the behavior already documented for Product in ADR-0004 and delivered for other resources (Repository, CategoryTaxonomy).

**Independent Test**: Push a Product referencing an existing CategoryTaxonomy, then poll/subscribe to its status until `Ready=True`; verify no other resource's status changed.

**Acceptance Scenarios**:

1. **Given** a namespace with an existing CategoryTaxonomy "laptops", **When** a Product is admitted with `spec.categoryRef: {kind: CategoryTaxonomy, name: laptops}`, **Then** the controller sets `CategoryResolved=True` (reason `CategoryFound`) and `Ready=True` on the Product's status, preserving its `AdmissionAccepted` condition and other existing status fields, and records a resolved category reference (name "laptops" plus "laptops"'s own opaque identifier) on the Product's status.
2. **Given** a Product whose category already resolved and is `Ready=True`, **When** the Product's `spec` is updated without changing `categoryRef`, **Then** the controller re-confirms `CategoryResolved=True`/`Ready=True` against the new `resourceVersion` without flapping the condition's `lastTransitionTime`, and the resolved category reference is unchanged.

---

### User Story 2 - Product waits for a category that doesn't exist yet, then converges (Priority: P2)

An author pushes a Product before its target CategoryTaxonomy exists (e.g., both are being introduced in the same rollout, category last). The push is accepted; the Product is temporarily not `Ready`. Once the CategoryTaxonomy is created, the Product converges to `Ready=True` without the author touching the Product again.

**Why this priority**: Matches ADR-0004's explicit non-blocking design for `CategoryResolved=False/CategoryNotFound` and is a common ordering in bulk catalog onboarding.

**Independent Test**: Push a Product referencing a not-yet-created CategoryTaxonomy, verify `CategoryResolved=False`/`Ready=False` with reason `CategoryNotFound`, then create the CategoryTaxonomy and verify the Product converges to `Ready=True` within the reconciliation window without a new Product push.

**Acceptance Scenarios**:

1. **Given** a Product referencing a CategoryTaxonomy name that does not exist, **When** the controller reconciles it, **Then** status shows `CategoryResolved=False` with reason `CategoryNotFound`, `Ready=False`, and the controller schedules a bounded retry rather than failing terminally.
2. **Given** a Product stuck at `CategoryResolved=False`/`CategoryNotFound`, **When** a CategoryTaxonomy matching its `categoryRef` is subsequently created (or an existing CategoryTaxonomy is renamed into a match) in the same namespace, **Then** the controller is re-enqueued for that Product and converges it to `CategoryResolved=True`/`Ready=True` without waiting for its own retry timer.

---

### User Story 3 - A previously resolved category disappears (Priority: P3)

A CategoryTaxonomy referenced by an already-`Ready` Product is deleted. The Product's status reflects that its reference is no longer resolvable, without the Product itself being deleted or blocking the category's own deletion beyond existing rules.

**Why this priority**: Correctness/edge-case coverage; lower priority than the two primary convergence paths because category deletion while referenced is a less common operational event.

**Independent Test**: Resolve a Product against a category, delete that category (assuming deletion is otherwise permitted), and verify the Product's status flips to `CategoryResolved=False`/`Ready=False` with a reason indicating the reference no longer resolves, while the category deletion itself completes per its own lifecycle rules.

**Acceptance Scenarios**:

1. **Given** a `Ready=True` Product referencing CategoryTaxonomy "laptops", **When** "laptops" is deleted, **Then** the Product's status converges to `CategoryResolved=False` (reason `CategoryNotFound`) and `Ready=False`, and the controller continues its indefinite bounded-interval retry in case the category reappears.

---

### Edge Cases

- A Product is deleted (enters `Terminating`) while its category resolution is still pending: the controller MUST NOT attempt to write `CategoryResolved`/`Ready` status once foreground deletion has begun for that Product; deletion completion takes precedence and `Terminating` remains derived, never independently written (per spec 055).
- Two controller replicas race to reconcile the same Product after a CategoryTaxonomy create event: `updateProductStatus` optimistic concurrency (`resourceVersion`) MUST cause the losing replica's write to be rejected and retried against fresh state, not silently overwrite the winner's write.
- A Product's `spec.categoryRef` is changed from one existing category to another: the controller MUST re-resolve against the new reference and update `CategoryResolved`/`Ready` accordingly; stale resolution state from the old reference MUST NOT persist.
- A namespace or repository backing a Product becomes non-`Active` after the Product was already `Ready`: this feature does not add new behavior for that case; existing Product/Repository/Namespace lifecycle rules apply unchanged.
- `MediaResolved` is out of scope for this feature; a Product's `Ready` computation in this feature depends only on `AdmissionAccepted` (already true by the time the controller observes the Product) and `CategoryResolved`.
- A Product with no `spec.categoryRef` at all (the field is nullable — an uncategorized Product is valid): the controller MUST set `CategoryResolved=True` (reason `NoCategoryReference`) rather than `CategoryNotFound`, so such a Product can still reach `Ready=True`. This is distinct from a `categoryRef` that is set but does not resolve, which remains `CategoryResolved=False`/`CategoryNotFound` per FR-003.

## Requirements *(mandatory)*

### Functional Requirements

- **FR-001**: The controller MUST resolve each active (non-`Terminating`) Product's `spec.categoryRef` against CategoryTaxonomy resources in the same namespace.
- **FR-002**: When `spec.categoryRef` resolves to an existing CategoryTaxonomy in the same namespace, the controller MUST set `CategoryResolved=True` with reason `CategoryFound`.
- **FR-003**: When `spec.categoryRef` does not resolve to any existing CategoryTaxonomy in the same namespace, the controller MUST set `CategoryResolved=False` with reason `CategoryNotFound`, and MUST treat this as non-blocking and retryable rather than a terminal failure.
- **FR-004**: The controller MUST set `Ready=True` on a Product if and only if `AdmissionAccepted=True` and `CategoryResolved=True`; otherwise `Ready=False`.
- **FR-005**: The controller MUST write `CategoryResolved` and `Ready` conditions via the `updateProductStatus` mutation, using the Product's current `resourceVersion` for optimistic concurrency, and MUST NOT overwrite unrelated status fields (e.g. `AdmissionAccepted`, `Terminating`) written by admission or by deletion completion.
- **FR-006**: On an optimistic-concurrency conflict, the controller MUST re-read the current Product state and retry reconciliation rather than treating the conflict as a permanent failure.
- **FR-007**: The controller MUST preserve an existing condition's `lastTransitionTime` when re-confirming the same condition type and status, and MUST update it only when the condition's status actually changes.
- **FR-008**: The controller MUST NOT write `CategoryResolved`/`Ready` status for a Product once that Product has a deletion timestamp and the foreground-deletion finalizer set; deletion-path reconciliation takes precedence and is unchanged by this feature.
- **FR-009**: When a CategoryTaxonomy is created, renamed, or otherwise changed such that it newly matches one or more previously-unresolved Products' `spec.categoryRef` in the same namespace, the controller MUST re-enqueue those Products for reconciliation without waiting for their own retry interval.
- **FR-010**: When a Product whose category previously resolved has its category become unresolvable (e.g., the referenced CategoryTaxonomy is deleted), the controller MUST transition that Product back to `CategoryResolved=False`/`Ready=False` with reason `CategoryNotFound`.
- **FR-011**: A Product with an unresolved category MUST be retried indefinitely at a bounded interval; the controller MUST NOT give up permanently or require manual intervention to resume resolution attempts.
- **FR-012**: This feature MUST NOT change the existing Product-to-CategoryTaxonomy count fan-out behavior (spec 042): CategoryTaxonomy product counts must remain correct and unaffected by the new `CategoryResolved`/`Ready` reconciliation path.
- **FR-013**: This feature MUST NOT implement `MediaResolved` or any `spec.media[*].fileRef` resolution; that remains deferred per ADR-0004/GH#244.
- **FR-014**: This feature MUST NOT implement a `CrossNamespaceRef` condition/reason; cross-namespace `categoryRef` values remain rejected at admission time only (spec 055), and this feature does not add a controller-side defensive check for that case.
- **FR-015**: When `CategoryResolved=True`, the controller MUST populate a resolved CategoryTaxonomy reference (the category's name and its opaque Relay identifier) on the Product's system-owned status, distinct from and without mutating the author-supplied `spec.categoryRef`. This resolved reference MUST be cleared (absent) whenever `CategoryResolved=False`.
- **FR-016**: The opaque identifier in the resolved category reference MUST be the same Relay-encoded value the API already returns as a CategoryTaxonomy's own `id`, not the CategoryTaxonomy's raw internal identifier.

### Production Requirements *(mandatory for core-service or load-bearing changes)*

- **PR-001 Replica Safety**: With two or more controller replicas running concurrently, exactly one replica's `CategoryResolved`/`Ready` write for a given Product reconciliation attempt succeeds; the other observes a version conflict and retries against fresh state. A replica replacement mid-reconciliation must not leave a Product stuck at stale status — the replacement replica converges it on its next reconcile.
- **PR-002 Multi-User Security**: Status writes continue to use the existing controller-only `updateProductStatus` authorization path; no new author-facing mutation or read surface is introduced, and no additional data becomes visible to non-controller callers.
- **PR-003 Capacity**: Category resolution and status writeback must scale to the same catalog size and concurrency envelope already declared for Product (spec 055's five-million-product catalog scale) without introducing an additional full-catalog scan per reconciliation — resolution must be a bounded, indexed lookup per Product.
- **PR-004 Backpressure**: A burst of CategoryTaxonomy creates that re-enqueues a large number of previously-unresolved Products must be bounded by the existing reconciliation queue/worker limits; it must not starve unrelated Namespace/Repository/CategoryTaxonomy reconciliation work.
- **PR-005 Recovery**: After a controller restart or checkpoint replay, Products left at `CategoryResolved=False` must resume retrying without requiring a new Product push or manual re-enqueue.

### Key Entities

- **Product status conditions**: The `CategoryResolved` and `Ready` condition types added to a Product's existing status condition set (alongside `AdmissionAccepted`, and the derived `Terminating`). Each condition carries a status (`True`/`False`), reason, message, and `lastTransitionTime`, and is merged with — not replacing — unrelated conditions already present.
- **CategoryTaxonomy reference match**: The relationship, keyed by namespace + category name, used both to resolve a Product's `spec.categoryRef` and to determine which previously-unresolved Products to re-enqueue when a CategoryTaxonomy changes.
- **Resolved category reference**: A system-owned pointer (category name + opaque Relay identifier) recorded on the Product once its category resolves, distinct from the author-supplied `spec.categoryRef`. Mirrors the existing resolved-reference pattern already used elsewhere (e.g. a resolved ProductVariant's pointer back to its Product).

## Success Criteria *(mandatory)*

### Measurable Outcomes

- **SC-001**: A Product pushed with a `categoryRef` to an already-existing CategoryTaxonomy in the same namespace reaches `Ready=True` within the same reconciliation latency window already achieved for Repository storage-provisioning readiness, without any additional author action.
- **SC-002**: A Product pushed before its target category exists reaches `Ready=True` within one reconciliation cycle after the category is created, with zero additional Product pushes required.
- **SC-003**: 100% of Product `CategoryResolved`/`Ready` reconciliations leave unrelated status fields (`AdmissionAccepted`, `Terminating`, `resourceVersion` history) and unrelated CategoryTaxonomy product counts unchanged.
- **SC-004**: Under two concurrent controller replicas reconciling the same Product repeatedly, 100% of resulting status writes converge to a single consistent final state with no lost or duplicated condition transitions.
- **SC-005**: Deleting a Product whose category resolution is still pending completes deletion without ever surfacing a `CategoryResolved`/`Ready` write racing the deletion completion.
