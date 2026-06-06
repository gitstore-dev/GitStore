# Feature Specification: CategoryTaxonomy Frontmatter and Hierarchy Enforcement

**Feature Branch**: `021-category-taxonomy`  
**Created**: 2026-06-06  
**Status**: Draft  
**Input**: User description: "Support hierarchical CategoryTaxonomy and single-category product constraint (GH#82)"

## User Scenarios & Testing *(mandatory)*

### User Story 1 - Publish a CategoryTaxonomy Document (Priority: P1)

A catalog administrator wants to organize products into a navigable hierarchy by creating `CategoryTaxonomy` resource files in a GitStore repository. They push a file with Kubernetes-style frontmatter (`apiVersion`, `kind`, `metadata`, `spec`) and a markdown body that describes the category. The system validates the document on push and rejects it with a clear error message if required fields are missing or the `kind` is incorrect.

**Why this priority**: Defining valid categories is the foundational prerequisite for all hierarchy navigation, product-category association, and storefront browsing. Nothing downstream works without this.

**Independent Test**: Push a single `CategoryTaxonomy` file to a repository, observe that it is accepted, and verify the category becomes queryable.

**Acceptance Scenarios**:

1. **Given** a repository with no existing categories, **When** a user pushes a file containing a valid `CategoryTaxonomy` document with `apiVersion: catalog.gitstore.dev/v1beta1`, `kind: CategoryTaxonomy`, required `metadata.name`, and a `spec.title`, **Then** the system accepts the push and the category is stored.
2. **Given** a repository, **When** a user pushes a `CategoryTaxonomy` file missing the required `metadata.name` field, **Then** the push is rejected with a descriptive validation error identifying the missing field.
3. **Given** a repository, **When** a user pushes a file with `kind: Category` (wrong kind), **Then** the push is rejected with an error stating the kind is not recognized.
4. **Given** a successfully pushed `CategoryTaxonomy`, **When** a consumer queries it, **Then** the response includes `name`, `title`, and markdown description content.

---

### User Story 2 - Build a Parent-Child Category Hierarchy (Priority: P1)

A catalog administrator wants to nest categories under parent categories (e.g., "Personal Computers" under "Electronics") by setting `spec.parentRef` to reference an existing `CategoryTaxonomy`. The system validates that the referenced parent exists within the same namespace and that no cyclic relationships are introduced.

**Why this priority**: Hierarchical navigation is the core value of `CategoryTaxonomy`; a flat list of categories with no parent linkage cannot support multi-level storefront menus or category drill-down.

**Independent Test**: Push a parent category and a child category that references the parent. Verify the hierarchy is correctly represented in query results.

**Acceptance Scenarios**:

1. **Given** a `CategoryTaxonomy` document with `kind: CategoryTaxonomy` and `name: electronics` already stored, **When** a user pushes a new `CategoryTaxonomy` with `spec.parentRef.name: electronics`, **Then** the push is accepted and the child category is linked to its parent.
2. **Given** a category `A` that is a parent of `B`, **When** a user pushes an update to `A` setting `spec.parentRef.name: B` (creating a cross-push cycle), **Then** the push is accepted and the controller sets the `Acyclic` status condition to `False` (cross-push cycle detection is deferred to the controller reconciliation loop, GH#244).
3. **Given** a `CategoryTaxonomy` with `spec.parentRef.name: nonexistent`, **When** a user pushes this document, **Then** the push is accepted and the category is stored with `ParentResolved: False` status condition indicating the referenced parent does not exist.
4. **Given** a category `A` with no parent, **When** a user pushes `CategoryTaxonomy` document with `spec.parentRef.name: A`, **Then** the child is accepted and its resolved path reflects the ancestry.

---

### User Story 3 - Enforce Single-Category Product Constraint (Priority: P2)

A catalog administrator wants assurance that every product in the catalog belongs to exactly one category. The system enforces this constraint during product pushes: a product without a category reference is accepted if the catalog allows uncategorized products (per configuration), but a product referencing more than one category is always rejected.

**Why this priority**: The single-category constraint is a stated business rule (a product belongs to exactly one category). It must be enforced at write time to prevent catalog data corruption, but it is secondary to the category definition stories because it requires categories to exist first.

**Independent Test**: Push a product referencing two categories and verify rejection. Push a product referencing one category and verify acceptance.

**Acceptance Scenarios**:

1. **Given** two existing `CategoryTaxonomy` records, **When** a user pushes a `Product` document referencing both categories, **Then** the push is rejected with an error stating a product may belong to only one category.
2. **Given** an existing `CategoryTaxonomy`, **When** a user pushes a `Product` document referencing exactly that one category, **Then** the push is accepted.
3. **Given** a repository, **When** a user pushes a `Product` document with no category reference, **Then** the push is accepted (uncategorized products are valid).

---

### User Story 4 - Attach Media to a Category (Priority: P3)

A catalog administrator wants to attach visual assets (e.g., hero images) to a category for use in storefronts by listing file references in `spec.media`. The system validates that each media entry is a valid `File` reference within the same namespace.

**Why this priority**: Media attachment enhances the category presentation but does not block category hierarchy or product association. It is additive and deferrable.

**Independent Test**: Push a `CategoryTaxonomy` with a `spec.media` entry referencing an existing `File` resource, and verify the media is reflected in query results.

**Acceptance Scenarios**:

1. **Given** an existing `File` resource named `category-hero`, **When** a user pushes a `CategoryTaxonomy` with `spec.media[0].name: category-hero` and `spec.media[0].kind: File`, **Then** the push is accepted and the media reference is stored.
2. **Given** no `File` resource named `missing-image`, **When** a user pushes a `CategoryTaxonomy` with a media entry referencing `missing-image` and `optional: false`, **Then** the push is accepted and the controller sets a status condition indicating the required file is not found (reconciliation phase, not push-time).
3. **Given** a media entry with `optional: true` referencing a non-existent file, **When** a user pushes the `CategoryTaxonomy`, **Then** the push is accepted and the unresolved optional media is silently skipped or flagged in status.

---

### Edge Cases

- What happens when a `CategoryTaxonomy` document's `metadata.name` contains characters illegal in a resource identifier (spaces, slashes)?
- What happens when a `parentRef` points to a `CategoryTaxonomy` in a different namespace?
- *(Deferred — GH#243)* Category deletion with dependents (child categories or associated products) is out of scope for this spec. Deletion semantics, OwnerReferences, and garbage collection strategy are tracked in GH#243 and addressed in a follow-on spec.
- How does the system handle a push containing both a new parent and a new child category referencing it in a single commit?
- What happens when two `CategoryTaxonomy` files in the same commit create a mutual parent cycle?
- What happens when a category hierarchy grows beyond a reasonable nesting depth?

## Requirements *(mandatory)*

### Functional Requirements

- **FR-001**: The system MUST accept `CategoryTaxonomy` documents with `apiVersion: catalog.gitstore.dev/v1beta1` and `kind: CategoryTaxonomy`.
- **FR-002**: The system MUST reject `CategoryTaxonomy` documents that are missing `metadata.name` or `spec.title`, with a descriptive error message identifying the missing field.
- **FR-003**: The system MUST reject any resource file where `kind` is not a recognized catalog resource type, providing an error message.
- **FR-004**: The system MUST support an optional `spec.parentRef` field that references an existing `CategoryTaxonomy` within the same namespace.
- **FR-005**: When a `CategoryTaxonomy` with `spec.parentRef` is admitted, the system MUST set the `ParentResolved` status condition to `False` if the referenced category does not exist in the stored index and is not being created in the same push. This check is performed in the **AdmitResources post-receive handler** (fire-and-forget; DB lookup sets a status condition, does not reject the push synchronously). The pre-receive `ValidateResources` phase performs schema-only validation — no DB lookups.
- **FR-006**: The system MUST detect cyclic category ancestry and surface it as a status condition (`Acyclic: False`). For intra-push cycles (parent and child in the same commit), detection is performed in **AdmitResources** by graph-walking the blobs of the current push. For cross-push cycles (parent already stored), detection is deferred to the **controller reconciliation loop** (GH#244) as it requires potentially expensive multi-hop DB traversal. Direct self-parenting (`parentRef.name == metadata.name`) is rejected in the `ValidateResources` pre-receive phase as it requires no DB lookup.
- **FR-007**: The system MUST treat the markdown body of a `CategoryTaxonomy` document as the human-readable description of the category.
- **FR-008**: The system MUST support an optional `spec.media` field containing zero or more file references, each identifying a named `File` resource.
- **FR-009**: *(Controller portion deferred — follow-on spec, blocked on GH#40)* For a media entry with `optional: false`, the system MUST surface a status condition on the `CategoryTaxonomy` when the referenced `File` resource does not exist in the same namespace. This check is performed by the controller during reconciliation (GH#165); the push itself is not rejected on missing File references. The status condition definition is specified here for contract stability; the reconciler implementation is deferred.
- **FR-010**: The system MUST accept a media entry with `optional: true` even if the referenced `File` resource does not exist.
- **FR-011**: The system MUST reject a `Product` push that references more than one `CategoryTaxonomy`, with a message indicating only one category is allowed.
- **FR-012**: The system MUST accept a `Product` push that references exactly one `CategoryTaxonomy` or no category at all.
- **FR-013**: *(Deferred — follow-on spec, blocked on GH#40)* The system MUST expose resolved category metadata in the `CategoryTaxonomyStatus`, including hierarchy depth, ancestry path, and child/product counts. This data is computed asynchronously by the controller reconciliation loop. It is out of scope for this spec; the data model and status fields are defined here for contract stability, but the reconciler implementation belongs in a subsequent spec that depends on GH#40.
- **FR-014**: `metadata.labels` MUST be accepted on `CategoryTaxonomy` documents and stored for future label-selector queries.

### Key Entities

- **CategoryTaxonomy**: A named catalog resource representing a single node in the product category hierarchy. Carries a display title, optional parent reference, optional media attachments, and a markdown description body. Identified by `metadata.name` within a namespace.
- **CategoryTaxonomySpec**: The desired-state fields of a `CategoryTaxonomy`: `parentRef`, `title`, `media`.
- **CategoryTaxonomyStatus**: Machine-computed state including resolved hierarchy depth, ancestor path (materialized path string, e.g. `root/parent/child`), child count, product count, last-applied revision, and a set of conditions (`ParentResolved`, `Acyclic`, `Ready`).
- **MediaDefinition**: A named reference to a `File` resource within the same namespace, with an `optional` flag controlling whether absence causes a validation failure.
- **ObjectReference**: A structured pointer to another catalog resource, carrying `apiVersion`, `kind`, `name`, and `namespace`.

## Success Criteria *(mandatory)*

### Measurable Outcomes

- **SC-001**: 100% of `CategoryTaxonomy` documents with all required fields are accepted on push without errors.
- **SC-002**: 100% of `CategoryTaxonomy` documents missing required fields are rejected at push time with a message that identifies the specific missing or invalid field.
- **SC-003**: Direct self-parenting (`parentRef.name == metadata.name`) is rejected at pre-receive time in 100% of cases. Intra-push mutual cycles (parent and child in the same push) are surfaced as `Acyclic: False` status conditions via the admission handler. Cross-push cycles are detected by the controller (GH#244, out of scope for this spec).
- **SC-004**: A product referencing more than one category is rejected in 100% of cases; single-category and uncategorized products are always accepted.
- **SC-005**: A three-level category hierarchy (root → parent → child) can be created across up to three separate pushes and queried with correct ancestry data after the controller has reconciled the affected categories (eventual consistency; not required synchronously at push time).
- **SC-006**: All push-time validation errors are surfaced to the committing user with a plain-language message in under 5 seconds.

## Assumptions

- The `File` resource kind is an already-supported catalog type; this spec adds only a reference to it from `CategoryTaxonomy`, not its own definition.
- The product's category reference field is `spec.categoryRef` (single `ObjectReference`), consistent with Kubernetes-style `*Ref` naming already established for `productRef` in `ProductVariant`.
- Parent validation at push time uses the repository state at the tip of the target branch; in-commit co-creation of parent and child in the same push is considered in-scope and must succeed if no cycle is introduced.
- A `parentRef` without a `namespace` field defaults to the same namespace as the child category.
- Category deletion is out of scope for this spec; orphaned children remain valid and their status reflects an unresolved parent condition.
- Maximum hierarchy depth is not enforced by this spec; practical limits may be imposed in a future spec.
- The category hierarchy is stored using a **materialized path** model: each `CategoryTaxonomy` row carries a `ancestorPath` string (slash-separated `metadata.name` segments from root to self, e.g. `electronics/computers/laptops`) alongside the `parentRef` adjacency pointer. This is the ScyllaDB-compatible equivalent of Postgres `ltree` and supports prefix-range ancestor queries in both `go-memdb` (dev) and ScyllaDB (prod) without recursive traversal.
- The `status` fields are read-only and written by the system after admission; users MUST NOT include them in committed files (or they will be stripped/overwritten).

## Clarifications

### Session 2026-06-06

- Q: Which phase should handle parentRef existence checks (FR-005) and required File reference checks (FR-009)? → A: Both deferred to AdmitResources (post-receive, fire-and-forget) as status conditions; File reference checks further deferred to controller reconciliation (GH#165). No DB lookups in pre-receive. Direct self-parenting is the only cycle check done pre-receive (no DB needed).
- Q: Should cycle detection be synchronous (pre-receive) or asynchronous? → A: Direct self-reference caught pre-receive (no DB). Intra-push mutual cycles detected in AdmitResources by graph-walking the current push blobs. Cross-push cycles deferred to controller reconciliation loop (GH#244) — too expensive for push path.
- Q: Should FR-013 status computation (depth, ancestry path, child/product counts) be synchronous (post-receive) or asynchronous (controller loop)? → A: Controller reconciliation loop (async, eventual consistency); not required at push time.
- Q: How should the category hierarchy be stored in ScyllaDB (no native ltree)? → A: Materialized path — store full ancestor path string (e.g. `root/parent/child`) alongside the `parentRef` adjacency pointer; ScyllaDB-compatible equivalent of Postgres `ltree`.
- Q: Should this spec include controller reconciliation tasks (FR-013, FR-009 controller portion) or scope only the push pipeline? → A: Push pipeline only; controller work is a follow-on spec blocked on GH#40. FR-013 and the controller portion of FR-009 are deferred; data model and status field contracts are defined here for stability.
- Q: How should the category deletion edge case be handled in this spec? → A: Explicitly deferred; a new GitHub issue must be created to track deletion semantics, OwnerReferences, and GC strategy. The edge case bullet is updated to record this decision.

## Dependencies

- Spec #014 (product-frontmatter), #015 (product-parser), #016 (product-spec-hydration), #017 (product-spec-validation) — establish the frontmatter parsing and admission pipeline that this spec extends with a new resource kind.
- Spec #018 (hook-pipeline-wiring) — provides the pre-receive/post-receive hook framework that this spec's validation and admission handlers plug into.
- GH#40 — parent initiative defining the shared `ObjectMeta`, `ObjectReference`, `LabelSelector`, and `Condition` contracts that `CategoryTaxonomy` adopts. Controller reconciliation work (FR-009 controller portion, FR-013) is blocked on GH#40 and its descendants; this spec delivers only the push pipeline.
- GH#82 — the GitHub issue this spec implements.
- GH#204, #205, #206, #207 — sub-tasks of GH#82 that will map to the plan tasks generated from this spec.
- GH#243 — follow-on: CategoryTaxonomy deletion semantics, OwnerReferences, and garbage collection (deferred from this spec; blocked on GH#165).
- GH#244 — follow-on: CategoryTaxonomy controller reconciliation — status computation (FR-013) and File reference conditions (FR-009 controller portion); blocked on GH#40 and GH#165 sub-tasks (#180, #181, #182).
