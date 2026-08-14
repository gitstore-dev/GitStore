# Feature Specification: Product Watch Transport for CategoryTaxonomy Count Reconciliation

**Feature Branch**: `042-product-category-count`  
**Created**: 2026-08-13  
**Status**: Draft  
**Input**: User description: "Add Product watch transport for CategoryTaxonomy count reconciliation (GH#337). Context: PR #335 shipped a CategoryTaxonomy reconciler (spec 039) that only reacts to CategoryTaxonomy watch events. A Product create, delete, or categoryRef reassignment emits no category event today, so CategoryTaxonomyStatus.resolved.productCount can go stale with no self-healing path. This was deferred from PR #335 review comment. Scope: publish Product admission changes into the existing watch transport, add a Product watch/dependency-trigger path in the controller manager, enqueue every affected CategoryTaxonomy on Product add/delete/categoryRef change (both old and new category on reassignment), preserve existing list-then-watch/resume/authorization semantics, and add contract/integration coverage. Non-goals: replacing CategoryTaxonomy reconciliation itself; polling the full catalogue as a workaround."

## User Scenarios & Testing *(mandatory)*

### User Story 1 - Product count updates when a product is added to a category (Priority: P1)

A catalog author pushes a new product that references an existing category. Without any further action, that category's reported product count reflects the new product.

**Why this priority**: This is the core symptom driving the issue — today a category's resolved product count only self-heals on a change to the category itself, never on a change to a product. Fixing "add" is the smallest slice that proves the fan-out path from Product to CategoryTaxonomy actually works end-to-end.

**Independent Test**: Can be fully tested by creating a category with zero products, pushing one new product referencing that category, and confirming the category's resolved product count becomes 1 without any push or edit to the category itself.

**Acceptance Scenarios**:

1. **Given** a category with a resolved product count of 0, **When** a catalog author pushes a new product referencing that category, **Then** the category's resolved product count becomes 1 without any direct change to the category.
2. **Given** a category already correctly reporting N products, **When** an unrelated product referencing a different category is pushed, **Then** the first category's resolved product count and status remain unchanged (no unnecessary reconciliation).

---

### User Story 2 - Product count updates when a product is removed (Priority: P1)

A catalog author removes (deletes) a product that belonged to a category. The category's reported product count decreases to reflect the removal, without requiring any change to the category itself.

**Why this priority**: Equal in severity to User Story 1 — this is the other half of the staleness gap. An operator relying on product counts to gauge category health cannot trust the number if it never decreases after a deletion.

**Independent Test**: Can be fully tested by creating a category with one product, deleting that product, and confirming the category's resolved product count drops to 0 without touching the category.

**Acceptance Scenarios**:

1. **Given** a category whose resolved product count includes a specific product, **When** that product is deleted, **Then** the category's resolved product count decreases by one to reflect the removal.
2. **Given** a category with zero products after all its products have been deleted, **When** an operator queries the category, **Then** its resolved product count is 0 and no error or stale value is shown.

---

### User Story 3 - Product count updates on category reassignment (Priority: P1)

A catalog author edits a product to move it from one category to another (changes which category it belongs to). Both the category the product left and the category it joined show correct, up-to-date product counts.

**Why this priority**: Reassignment is the scenario most likely to produce a silently wrong pair of numbers (one category overcounts, the other undercounts) if only one side of the move is reconciled. It is equally severe to add/delete because it doubles the blast radius — two categories, not one, can be left wrong.

**Independent Test**: Can be fully tested by creating two categories and one product under the first, moving the product to the second category via a push, and confirming the first category's count decreases by one while the second's increases by one.

**Acceptance Scenarios**:

1. **Given** a product currently counted under category A, **When** the product is reassigned to category B, **Then** category A's resolved product count decreases by one and category B's resolved product count increases by one.
2. **Given** a product reassignment between two categories, **When** the reassignment is processed, **Then** no third, unrelated category is reconciled or has its status touched as a result.

---

### User Story 4 - Convergence survives a controller restart (Priority: P2)

An operator restarts the reconciliation process (planned maintenance or crash recovery) while product changes are in flight or were made just before the restart. After the process resumes, every category affected by product changes made during or shortly before the restart still ends up with a correct product count — no update is silently dropped because of the restart.

**Why this priority**: Restart-safety is a correctness guarantee expected of the existing reconciliation system (per the resume/at-least-once behavior already established for category-driven reconciliation) and must extend to product-driven fan-out; otherwise every deployment or crash becomes a latent staleness window. It is lower priority than US1-3 because it is a robustness property layered on top of behavior that must first exist and work in the steady state.

**Independent Test**: Can be fully tested by pushing a product change, restarting the reconciliation process before that change's effect is confirmed, and then confirming after restart that the affected category's product count still converges correctly, with no manual re-trigger required.

**Acceptance Scenarios**:

1. **Given** a product change that would affect a category's product count, **When** the reconciliation process restarts before processing that change, **Then** the affected category's product count still converges correctly after restart without operator intervention.
2. **Given** a restart during a burst of product changes affecting several categories, **When** the process resumes, **Then** every affected category converges and none is left permanently stale.

---

### Edge Cases

- What happens when a product references a category that does not exist (dangling reference)? The system must not fail the affected-category fan-out; the non-existent category has nothing to reconcile and no error should be raised to the catalog author for this case alone (existing category-resolution behavior from spec 039 governs how a category record itself reports its own validity).
- What happens when the same product is created, then immediately deleted, before the category is ever reconciled? The category's final product count must reflect the net effect (product absent), not a stale intermediate state.
- What happens under a high-volume burst of product changes all affecting the same category (e.g., a bulk import)? The category must converge to the correct final count without requiring one reconciliation per individual product change — redundant enqueues for the same category within a short window should collapse rather than each be processed as a separate cycle.
- What happens when a product change and a direct category change happen close together in time? Both change sources must independently be able to trigger a correct reconciliation, and neither should overwrite the other with stale data.
- What happens if the affected category was deleted before the product-driven reconciliation for it runs? The reconciliation attempt must resolve as a no-op rather than an error or retry loop.

## Requirements *(mandatory)*

### Functional Requirements

- **FR-001**: The system MUST notify the reconciliation process whenever a product is created, so that any category the product references can be re-evaluated for its resolved product count.
- **FR-002**: The system MUST notify the reconciliation process whenever a product is deleted, so that any category the product previously referenced can be re-evaluated for its resolved product count.
- **FR-003**: The system MUST notify the reconciliation process whenever a product's category reference changes from one category to another, identifying both the previously-referenced category and the newly-referenced category.
- **FR-004**: The system MUST re-evaluate only the category (or categories) actually affected by a given product change — an unrelated category MUST NOT be reconciled or have its status touched as a side effect of a product change that does not involve it.
- **FR-005**: The system MUST NOT alter the category-driven reconciliation behavior already established for the CategoryTaxonomy resource (spec 039) — product-driven reconciliation is an additional trigger path into the same reconciliation outcome, not a replacement or parallel computation of category status.
- **FR-006**: The product-driven notification path MUST support resuming from where it left off after a process restart, consistent with the existing resume behavior already established for category-driven reconciliation, so that no product change made shortly before a restart is silently lost.
- **FR-007**: The product-driven notification path MUST deliver at-least-once: a product change may result in more than one reconciliation attempt for the affected category, but MUST NOT result in zero attempts.
- **FR-008**: The system MUST NOT require scanning or polling the entire product catalogue to detect which categories are affected by a product change; affected categories MUST be identified directly from the product change itself (its category reference and, on reassignment, its prior category reference).
- **FR-009**: Access to the product-driven notification path MUST be restricted to the same trusted, authenticated actor already permitted to consume category-driven reconciliation notifications — no new, broader access is introduced.
- **FR-010**: The system MUST provide automated test coverage demonstrating that product create, product delete, and product category-reassignment each correctly update the affected category's resolved product count, and that an unrelated category's product count and status are left untouched by each of those operations.
- **FR-011**: The system MUST provide automated test coverage demonstrating that a reconciliation-process restart does not cause a product change's effect on a category's resolved product count to be permanently lost.

### Key Entities

- **Product**: A catalog item that references at most one category via a category reference. Its creation, deletion, and category-reference changes are the triggering events for this feature. Not otherwise modified by this feature.
- **CategoryTaxonomy**: An existing catalog category resource whose status already includes a resolved product count (established by spec 039). This feature adds a new way for that resolved count to be kept correct — via product-side changes — without altering how the count itself is computed or how any other part of category status is derived.
- **Affected-category notification**: A record of "this category's resolved data may now be out of date" raised in response to a product change, carrying enough information (the category identifier or identifiers involved) to know what to re-evaluate. On product creation or deletion this is a single category; on reassignment it is the pair of categories (previous and new).

## Success Criteria *(mandatory)*

### Measurable Outcomes

- **SC-001**: After a product referencing a category is created, that category's resolved product count reflects the addition within one reconciliation cycle, with no push or edit to the category itself required.
- **SC-002**: After a product is deleted, its former category's resolved product count reflects the removal within one reconciliation cycle, with no push or edit to the category itself required.
- **SC-003**: After a product is reassigned from one category to another, both the losing and gaining category's resolved product counts are correct within one reconciliation cycle, and no third category is reconciled as a result.
- **SC-004**: 100% of product changes that affect a category's resolved product count are eventually reflected in that category's status, including when the reconciliation process restarts partway through processing.
- **SC-005**: Under steady state (no product or category changes), no category incurs additional reconciliation activity solely due to the existence of this feature — product-driven reconciliation only fires in response to an actual, relevant product change.
- **SC-006**: A burst of many product changes affecting the same category converges to the correct final count without a proportional one-reconciliation-per-change cost — redundant re-evaluations of the same category collapse rather than queueing indefinitely.
