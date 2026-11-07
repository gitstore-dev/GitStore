# Feature Specification: CategoryTaxonomy Path Freshness, Git-Backed Mutations, and Descendant Filtering

**Feature Branch**: `057-categorytaxonomy-path-freshness`
**Created**: 2026-08-20
**Updated**: 2026-10-07 (scope expanded: Git-backed create/update/delete mutations; ancestor-path descendant filtering)
**Status**: In progress
**Input**: User description: "CategoryTaxonomy Path Freshness: Deprecate Admission-Time path/depth in Favor of status.resolved (GitHub issue #382). `Category.path`/`Category.depth` go stale after a category is re-parented elsewhere in the tree, while the separate `status.resolved.path`/`status.resolved.depth` (written by the CategoryTaxonomy controller) are kept fresh. Fix the resolver to prefer the fresh fields, with a pre-reconcile fallback, and mark the legacy fields `@deprecated`." Revised 2026-10-07: remove `Category.path`/`Category.depth` outright, with no fallback. Scope expansion: "Add mutations that write to Git to mirror the Repository and Namespace implementations. ADR-0006 mentions path prefix filtering — implement that as well."

## Scope Overview

This feature closes three related CategoryTaxonomy gaps against ADR-0006:

1. **Path freshness** (User Stories 1–2): the stale, admission-time `Category.path`/`Category.depth` fields are removed. `status.resolved.path`/`status.resolved.depth`, maintained by the CategoryTaxonomy controller, are the only hierarchy fields. There is no pre-reconcile fallback.
2. **Git-backed mutations** (User Stories 4–6): `createCategory`, `updateCategory` and a Git-backed `deleteCategory` commit manifests and delegate to admission, mirroring the existing Product, Repository and Namespace mutations. Today no create/update mutation exists, and `deleteCategory` writes the datastore directly, so Git and the datastore diverge.
3. **Descendant filtering** (User Stories 7–8): `categories` gains a subtree filter (`descendantOf`, `includeSelf`, `maxDepth`) backed by an asynchronously maintained ancestor index keyed on `status.resolved.path`. `Category.children` and `Category.parent`, which today always return `[]`/`null`, are served from the same data.

## User Scenarios & Testing *(mandatory)*

### User Story 1 - Reading a category's current position in the tree after a re-parent (Priority: P1)

A catalog administrator (or any GraphQL API consumer — internal UI client or external integrator) re-parents a category by pushing a spec change to its `parentRef`, then later queries that category — or any of its pre-existing descendants elsewhere in the tree. Today the top-level `Category.path`/`Category.depth` fields silently keep showing the old hierarchy position, because they are derived from a value recomputed only for the categories touched by that push. The consumer needs one hierarchy source that reflects reality.

**Why this priority**: This is the core bug reported in issue #382. Keeping a second, stale source alongside the fresh one invites clients to read the wrong one.

**Independent Test**: Re-parent a category via a push, wait for the CategoryTaxonomy controller to reconcile it, then query `status.resolved.path`/`status.resolved.depth` on that category and on a pre-existing descendant, confirming both reflect the new position.

**Acceptance Scenarios**:

1. **Given** a category has been reconciled since its last hierarchy-affecting change, **When** a client queries `status.resolved.path`/`status.resolved.depth`, **Then** the values reflect its current position in the tree.
2. **Given** a category `A` is re-parented in one push, and a pre-existing descendant `B` of `A` was not itself included in that push, **When** a client queries `B`'s `status.resolved.path` before the controller has reconciled `B`, **Then** the value may still reflect `B`'s old position — an expected transient state, signalled by `AncestorPathReady` not yet being `True` for the new generation, not a defect.
3. **Given** the same scenario, **When** the controller's reconcile cascade has reached `B`, **Then** `B`'s `status.resolved.path` reflects its corrected position without `B` being part of any push.

---

### User Story 2 - Removed legacy hierarchy fields (Priority: P2)

A client developer inspecting the schema finds exactly one place to read a category's hierarchy, so new client code cannot be written against a stale field.

**Why this priority**: Prevents the staleness problem from being reintroduced. Removal (rather than deprecation) is acceptable during the pre-1.0 alpha series.

**Independent Test**: Introspect `Category` and confirm it has no `path` or `depth` field; a query selecting either fails schema validation.

**Acceptance Scenarios**:

1. **Given** the GraphQL schema, **When** a client introspects `Category`, **Then** it has no `path` or `depth` field, and `status.resolved.path`/`status.resolved.depth` are documented as the hierarchy fields.
2. **Given** a category that has not yet been reconciled for the first time, **When** a client queries `status.resolved`, **Then** it is `null`. No admission-time value is substituted.

---

### User Story 4 - Creating a category through the API (Priority: P1)

A catalog administrator or an integrator without Git tooling creates a category (root or nested) through the GraphQL API. The result must be indistinguishable from authoring `categories/<name>.md` and pushing it: the manifest lands in the namespace's `gitstore-system` repository with a reviewable commit, and the same admission rules apply.

**Why this priority**: ADR-0006 names a GraphQL mutation path for CategoryTaxonomy, and every sibling catalog kind (Product) and every infrastructure kind (Namespace, Repository) already has one. Without it, API-only clients cannot build a taxonomy at all.

**Independent Test**: Call `createCategory` for a root category and then for a child naming it as `parentRef`; confirm each call returns the admitted category, `categories/<name>.md` exists at the head of `<namespace>/gitstore-system` with the caller as commit author, and, after reconcile, the child's `status.resolved.path` is `[root, child]`.

**Acceptance Scenarios**:

1. **Given** an `Active` namespace whose `gitstore-system` repository is `Active`, and a caller holding `categoryTaxonomy.create`, **When** the caller invokes `createCategory` with a valid manifest, **Then** exactly one commit adding `categories/<metadata.name>.md` is written to `<namespace>/gitstore-system`, admission runs synchronously, and the payload returns the admitted category with `AdmissionAccepted=True`.
2. **Given** a category with the same name already exists in the namespace (in any repository), **When** `createCategory` is invoked, **Then** the request fails with an "already exists" error and no commit is written.
3. **Given** a manifest whose `spec.parentRef` names a category that does not exist, **When** `createCategory` is invoked, **Then** the category is admitted as a tentative root with `ParentResolved=False`/`ParentNotFound`, exactly as a push would — this is not a mutation error.
4. **Given** a manifest that names itself as parent, names a `Terminating` parent, omits `spec.title`, or otherwise fails pre-receive validation, **When** `createCategory` is invoked, **Then** the request fails with structured diagnostics (see FR-020) and no commit is written.
5. **Given** a caller without `categoryTaxonomy.create` in the target namespace, **When** `createCategory` is invoked, **Then** the request is denied before any Git or datastore work.

---

### User Story 5 - Updating or re-parenting a category through the API (Priority: P1)

An administrator retitles a category, edits its description or media, or moves it under a different parent through the API. The edit must apply to the file the category was actually admitted from — including categories originally pushed to a non-`gitstore-system` repository — and must trigger the same re-parent cascade a push would.

**Why this priority**: Re-parenting is the operation that drives the path-freshness problem in User Story 1; API clients need to perform it with the same guarantees as Git authors.

**Independent Test**: Push `categories/laptops.md` to a non-system repository, then call `updateCategory` changing `spec.parentRef`; confirm the commit lands in that repository at that path, the Markdown body is preserved when `body` is omitted, and descendants' `status.resolved.path` converge to the new position.

**Acceptance Scenarios**:

1. **Given** an admitted category, **When** `updateCategory` is invoked by a caller holding `categoryTaxonomy.update`, **Then** the updated manifest is committed to the category's stored provenance (the repository and path it was admitted from), admission runs synchronously, and the payload returns the newly admitted generation.
2. **Given** `updateCategory` omits `body`, **When** the commit is written, **Then** the existing Markdown body at the stored path is preserved; an explicit `body` replaces it.
3. **Given** the input changes `metadata.name` or `metadata.namespace`, **When** `updateCategory` is invoked, **Then** it is rejected (both are immutable) and no commit is written.
4. **Given** the input changes `spec.parentRef` to a new, existing, non-`Terminating` parent, **When** the update is admitted, **Then** the category keeps its UID, its parent owner reference is replaced, and the controller's existing cascade recomputes `status.resolved` for it and all descendants.
5. **Given** another commit modifying the same file lands between this mutation's commit and its admission, **When** admission runs, **Then** the mutation fails with a superseded error rather than reporting the other writer's content as its own result.
6. **Given** the category is `Terminating`, **When** `updateCategory` is invoked, **Then** it is rejected and no commit is written.

---

### User Story 6 - Deleting a category through Git-backed removal (Priority: P2)

An administrator deletes a leaf category through the API. The manifest file is removed in Git (so history records the removal and a later push cannot silently resurrect it), and the category enters the existing foreground-deletion lifecycle: children block, assigned products are decoupled asynchronously.

**Why this priority**: Today's datastore-only delete leaves the manifest in Git, so the datastore and the source of truth diverge. Fixing it is required for Git to remain canonical, but deletion is less frequent than create/update.

**Independent Test**: Delete a leaf category with assigned products via `deleteCategory`; confirm the file is removed at its stored path in one commit, the payload reports `TERMINATION_STARTED`, products later show `CategoryResolved=False`/`CategoryDeleted`, and the record is eventually removed. Repeat the call and confirm `ALREADY_TERMINATING` with no new commit.

**Acceptance Scenarios**:

1. **Given** a category with no child categories, **When** `deleteCategory` is invoked by a caller holding `categoryTaxonomy.delete`, **Then** the manifest is removed at the stored provenance in one commit, admission marks the category `Terminating`, and the payload is `{ category, outcome: TERMINATION_STARTED }`.
2. **Given** a category with at least one child category (a `blockOwnerDeletion: true` dependent), **When** `deleteCategory` is invoked, **Then** it fails with "child categories present" and no commit is written.
3. **Given** a category that is already `Terminating`, **When** `deleteCategory` is invoked, **Then** the payload is `{ category, outcome: ALREADY_TERMINATING }` and no commit is written.
4. **Given** a category with assigned products, **When** it is deleted, **Then** product assignments never block the request; products are decoupled asynchronously exactly as for a Git-push deletion.

---

### User Story 7 - Listing a category subtree (Priority: P2)

A storefront or admin UI renders a category navigation tree, a breadcrumb-scoped listing, or a "everything under Electronics" page. It needs the categories beneath a given category — optionally including the category itself and optionally limited to N levels — in one paginated query, without walking the tree one `children` call at a time.

**Why this priority**: ADR-0006 specifies `listCategoryTaxonomies` as "filterable by ancestorPath prefix", and prior planning assumed it existed; nothing implements it. It is the read-side counterpart to the hierarchy maintained by User Stories 1 and 5.

**Independent Test**: Build `electronics → computers → laptops` and `electronics → computers-refurb`; query `categories(namespace, filter: { descendantOf: "computers" })` and confirm only `laptops` is returned (not `computers-refurb`); add `includeSelf: true` and confirm `computers` is also returned; query `descendantOf: "electronics", maxDepth: 1` and confirm only `computers` and `computers-refurb` are returned.

**Acceptance Scenarios**:

1. **Given** a namespace taxonomy, **When** a client queries `categories` with `filter: { descendantOf: X }`, **Then** the result contains every category whose current hierarchy path contains `X` as a proper ancestor, and nothing else.
2. **Given** `includeSelf: true`, **When** the query runs, **Then** `X` itself is included as the first result (relative depth 0).
3. **Given** `maxDepth: N` (N ≥ 1), **When** the query runs, **Then** only descendants at most N levels below `X` are returned.
4. **Given** matching is by whole path segment, **When** `descendantOf: "computers"` is queried, **Then** a sibling named `computers-refurb` and its descendants are never returned.
5. **Given** `X` does not exist in the namespace, **When** the query runs, **Then** an empty connection is returned (not an error).
6. **Given** a filtered result larger than one page, **When** the client follows `after` cursors, **Then** every matching category is returned exactly once in a stable order (ascending relative depth, then name), with the same page-size limits as the unfiltered query.
7. **Given** no `filter` argument, **When** `categories` is queried, **Then** behavior, ordering and cursors are unchanged from today.

---

### User Story 8 - Navigating parent and children on a category (Priority: P3)

A client already holding a `Category` traverses `parent` and `children` to render breadcrumbs and one-level menus. Today `parent` always returns `null` and `children` always returns `[]`, which is silently wrong.

**Why this priority**: Correctness fix for fields that already exist in the schema; cheap once User Story 7's index exists.

**Independent Test**: For `electronics → computers → laptops`, query `computers { parent { name } children { name } }` and confirm `electronics` and `[laptops]`.

**Acceptance Scenarios**:

1. **Given** a category with `ParentResolved=True`, **When** `parent` is selected, **Then** the resolved parent category is returned; for a root or an unresolved parent, `null`.
2. **Given** a category with direct children, **When** `children` is selected, **Then** exactly its direct children (relative depth 1 in the ancestor index) are returned, ordered by name, bounded by the same per-request limit as a single list page.

### Edge Cases

- **`status.resolved` exists but is itself stale relative to a very recent, not-yet-reconciled move**: Because the CategoryTaxonomy controller's reconciliation is asynchronous and propagates level-by-level down an affected subtree (not as a single atomic bulk update), there is a window — bounded by however long the reconcile cascade takes to reach a given descendant — during which `status.resolved.path`/`status.resolved.depth` may not yet reflect the very latest ancestor move. This is expected, transient, self-healing behavior (it resolves on that node's next reconcile pass), not an error condition, and this specification does not require detecting or flagging this window to the client. The same window applies to descendant-filter results and `children`.
- **A category is part of a cycle** (its `parentRef` chain loops back on itself): the controller intentionally freezes `status.resolved.path`/`.depth` at their last-known-good value rather than recomputing a meaningless path through a cycle. The ancestor index therefore also keeps the category at its last-known-good position until the cycle is fixed.
- **A category has not yet been reconciled for the first time**: `status.resolved` is `null`, and the category does not appear in descendant-filter results or in its parent's `children` until the controller's first reconcile writes `status.resolved`. No admission-time value is substituted.
- **Root categories** (no parent): `status.resolved.depth` is `0` and `status.resolved.path` is a single-element array containing the category's own name.
- **Mutation commit succeeds but post-receive admission denies the manifest** (for example, a cross-resource check that only runs after commit): the ref has already moved; the category keeps serving its last accepted generation, `AdmissionAccepted=False` with reason `AdmissionReportFailed` is recorded, and the mutation returns an error carrying the diagnostics — never the stale previous generation as a success.
- **Mutation and a Git push race on the same file**: whichever commit is at the ref head when admission runs wins; the losing mutation reports superseded (User Story 5, scenario 5).
- **Category originally pushed to a non-system repository**: `updateCategory`/`deleteCategory` follow stored provenance. `createCategory` never writes outside `gitstore-system`. A later push of the same name to a different repository is rejected by the existing name-uniqueness rule.
- **Missing provenance** (a category admitted before provenance was recorded): `updateCategory`/`deleteCategory` fail with a clear "provenance unavailable" error rather than guessing a path.
- **Very deep trees**: the controller already caps ancestor walks at depth 128; `maxDepth` is accepted up to that same bound and rejected above it.
- **Terminating categories in subtree results**: a `Terminating` category remains listed (with its `Terminating` condition) until its record is removed, consistent with the unfiltered list.

## Requirements *(mandatory)*

### Functional Requirements

#### Path freshness

- **FR-001**: The GraphQL `Category` type MUST NOT expose top-level `path` or `depth` fields. `status.resolved.path` (root-to-self names) and `status.resolved.depth` (root = 0) are the only client-visible hierarchy fields.
- **FR-002**: When a category has not yet been reconciled, `status.resolved` MUST be `null`. The system MUST NOT substitute the admission-time ancestor path, or any other fallback, in any client-visible field, filter or navigation result.
- **FR-003**: A transient mismatch between a recently moved ancestor and a not-yet-reconciled descendant's `status.resolved` is expected, self-healing behavior, not an error. `AncestorPathReady` remains the documented convergence signal.
- **FR-004**: The admission-time ancestor path MAY remain an internal datastore value for admission-time cycle detection, but MUST NOT be read by any GraphQL resolver. This feature does not otherwise change its computation.
- **FR-005**: This feature MUST NOT alter how the CategoryTaxonomy controller computes or propagates `status.resolved.path`/`status.resolved.depth` to descendants.
- **FR-006**: The `status.resolved` field descriptions and the API reference MUST state that hierarchy data is asynchronous: `null` until first reconcile, and eventually consistent after an ancestor move. The removal of `Category.path`/`Category.depth` MUST be called out in release notes as a breaking change.

#### Git-backed mutations

- **FR-010**: The system MUST provide `createCategory(input: CreateCategoryInput!): CreateCategoryPayload!` and `updateCategory(input: UpdateCategoryInput!): UpdateCategoryPayload!`. Inputs MUST follow the existing catalog input shape: `apiVersion` (default `catalog.gitstore.dev/v1beta1`), `kind` (default `CategoryTaxonomy`), `metadata: ObjectMetaInput!`, `spec: CategorySpecInput!` (`title`, `parentRef`, `media` mirroring `CategorySpec`), and optional `body`. Payloads MUST carry `category: Category`. No `clientMutationId` is added.
- **FR-011**: `createCategory` MUST render the manifest as Markdown with YAML frontmatter identical in shape to a Git-authored file, commit it to `categories/<metadata.name>.md` in `<metadata.namespace>/gitstore-system` with commit message `Create CategoryTaxonomy <name>` and the authenticated caller as commit author, then admit that commit synchronously. It MUST NOT write the datastore directly.
- **FR-012**: `updateCategory` MUST commit to the category's stored provenance (admitted repository and path), preserve the existing Markdown body when `body` is omitted, use commit message `Update CategoryTaxonomy <name>`, and admit synchronously. It MUST fail without committing when the category does not exist, has no recorded provenance, is `Terminating`, or when the input changes `metadata.name`/`metadata.namespace`.
- **FR-013**: `deleteCategory` MUST become Git-backed: after pre-receive checks pass, it removes the manifest at stored provenance in one commit (message `Delete CategoryTaxonomy <name>`) and admits the deletion, which applies the existing foreground-deletion lifecycle (finalizer, `Terminating`, child blocking, asynchronous product decoupling). It MUST NOT transition the datastore record without a corresponding Git removal.
- **FR-014**: `DeleteCategoryPayload` MUST become `{ category: Category, outcome: ResourceDeletionOutcome! }`, mirroring the other resource deletions. `outcome` is `TERMINATION_STARTED` when this request started termination and `ALREADY_TERMINATING` when the category was already terminating (no commit written). The legacy `deletedCategoryId` and `orphanedProductIds` fields are removed (`orphanedProductIds` was never populated; product decoupling is asynchronous and observable on each product's `CategoryResolved` condition).
- **FR-014a**: The system MUST provide a controller-only `completeCategoryDeletion(input: CompleteCategoryDeletionInput!): CompleteCategoryDeletionPayload!`, with input `{ namespace, name, resourceVersion }` and payload `{ id }`, mirroring `completeNamespaceDeletion`, `completeRepositoryDeletion` and `completeProductDeletion`.
  - It permanently removes a `Terminating` category, its finalizer and its ancestor-index entries, but only once no blocking children remain and product decoupling is complete.
  - It is authorized as `categoryTaxonomy.purge`, the ADR-0010 verb for final removal, used by every `complete*Deletion` mutation.
  - It fails with `CONFLICT` on a resource-version mismatch, and with `FAILED_PRECONDITION` when the category is not terminating (`CATEGORY_NOT_TERMINATING`), still has children (`CHILD_CATEGORIES_PRESENT`) or still has products to decouple (`PRODUCT_DECOUPLING_INCOMPLETE`).
  - The `completeDeletion` flag on `UpdateCategoryStatusInput` MUST keep working, marked `@deprecated` with a reason naming `completeCategoryDeletion`, for one release so API and controller can upgrade independently. The CategoryTaxonomy controller MUST switch to `completeCategoryDeletion` in the same release, and the flag is removed in the following release.
- **FR-015**: Committed-manifest admission MUST support the CategoryTaxonomy delete operation (today only Product deletion is supported) and create/update operation validation (create→"already exists", update→"not found") for CategoryTaxonomy.
- **FR-016**: Because API-originated commits bypass the Git push hooks, every check that a push enforces before the ref moves MUST be enforced by the mutation before committing: envelope and schema validation; self-parent rejection; `Terminating` parent rejection; cross-namespace `parentRef` rejection; `media[*].fileRef.name` presence; and, for delete, the `blockOwnerDeletion: true` (children) check. A mutation-path category MUST NOT be admissible in a state a push would have rejected.
- **FR-017**: Post-receive admission runs asynchronously for a Git push (a push may carry thousands of objects and must not be held open for them) but synchronously inside a mutation (exactly one object, bounded by the request deadline). Mutations MUST therefore never batch multiple manifests into one commit. CategoryTaxonomy admission MUST therefore return its decision (accepted, denied with diagnostics, or failed) to its caller instead of only logging it. A mutation MUST NOT return success unless the record it returns is the generation produced by its own commit (verified against that commit's identity), and MUST NOT return a previous generation when admission denied or failed. Because both paths share one admission decision, a denial on the asynchronous push path MUST also be recorded on the category (`AdmissionAccepted=False`/`AdmissionReportFailed`, last accepted generation retained) rather than only logged.
- **FR-018**: If another commit to the same file reaches the ref head before admission and its content differs, the mutation MUST fail with a superseded error and MUST NOT report the other writer's result as its own.
- **FR-019**: Re-submitting a create/update whose rendered manifest is byte-identical to the admitted one MUST NOT produce a new generation or re-trigger the descendant cascade.
- **FR-020 (Error envelope and diagnostics)**: Every Git-backed mutation error MUST use one kind-neutral `extensions` envelope of at most four keys, each with a fixed rule for when it appears. Admission errors follow the two-lane contract of ADR-0015 §4.
  - **Keys**:
    - `code`: always present. One of the kind-neutral codes `ADMISSION_REJECTED`, `ALREADY_EXISTS`, `NOT_FOUND`, `CONFLICT`, `FAILED_PRECONDITION`, `BAD_USER_INPUT`, `FORBIDDEN`.
    - `diagnostics`: present whenever the error has detail, with one entry per problem.
    - `phase`: present only when `code = ADMISSION_REJECTED`. `PRE_RECEIVE` means no commit was written and no record changed. `POST_RECEIVE` means the ref moved; the read model keeps serving the last accepted generation and records `AdmissionAccepted=False` with reason `AdmissionReportFailed`.
    - `commit`: present only when `phase = POST_RECEIVE`.

    No other keys are allowed, with no exceptions. The status-write and deletion-completion code `RESOURCE_VERSION_CONFLICT` and its `resourceVersion` key fold into `code: CONFLICT` with `diagnostics[].reason = RESOURCE_VERSION_CONFLICT`; the current resource version moves into the diagnostic message. Controllers MUST accept `CONFLICT` before the API stops emitting the old code.
  - **Diagnostic entry**: `{ reason, message, level, file?, field? }`.
    - `reason`: required machine-readable SCREAMING_SNAKE value.
    - `message`: human-readable text.
    - `level`: `FAILURE | WARNING | NOTICE`.
    - `file`: the manifest path, e.g. `categories/laptops.md`.
    - `field`: the field path, e.g. `spec.title`.

    Optional `title`, `startLine`, `startColumn`, `endLine` and `endColumn` MAY be added later without breaking clients. Without `reason`, the shape is a subset of the `AdmissionReport` annotation fields, so a later `AdmissionReport` resource can carry the same entries. Building that resource is out of scope.
  - **Code selection by cause**:
    - Validation or policy outcomes → `ADMISSION_REJECTED`.
    - The name is taken → `ALREADY_EXISTS`.
    - The target is missing → `NOT_FOUND`.
    - Superseded by a concurrent write → `CONFLICT`.
    - Lifecycle or state preconditions (terminating, blocking dependents, missing provenance) → `FAILED_PRECONDITION`.
  - **Message**: for `ADMISSION_REJECTED`, the error `message` MUST be the same flattened `file: message; …` text a push rejection carries for the same manifest.
  - **Warnings on success**: a successful mutation whose admission produced `WARNING`/`NOTICE` entries MUST return them in the response's top-level `extensions.admission`, as a list of `{ path, commit, diagnostics }` objects. `path` is the mutation's response path (alias-aware, so a document with several mutations attributes each entry correctly). `extensions.admission` is omitted when there is nothing to report, MUST coexist with other top-level extension keys (such as operation cost reporting), and MUST NOT appear in `data`.
  - **Namespace alignment**: Namespace mutation errors MUST adopt the same envelope and codes:
    - `NAMESPACE_STRUCTURAL_VALIDATION_FAILED`, `NAMESPACE_IMMUTABLE_FIELD` and `NAMESPACE_POLICY_REJECTED` become `ADMISSION_REJECTED`, `ALREADY_EXISTS`, `NOT_FOUND` or `FAILED_PRECONDITION`, according to their reason.
    - `NAMESPACE_CONFLICT` becomes `CONFLICT`.
    - `NAMESPACE_DELETION_BLOCKED` becomes `FAILED_PRECONDITION`, with one diagnostic per blocker.
    - The former `phase: STRUCTURAL|POLICY`, `reason` and `reasons` keys are removed. Their information is in `code` and `diagnostics[].reason`, and existing reason values are kept unchanged.

    This is a breaking change to Namespace error consumers and MUST be called out in release notes.
  - The codes, the envelope rules, the reason values and `extensions.admission` MUST be documented in the API reference, since `extensions` are not visible through schema introspection.
- **FR-021 (AuthZ)**: `createCategory`, `updateCategory` and `deleteCategory` MUST be authorized as `categoryTaxonomy.create`, `categoryTaxonomy.update` and `categoryTaxonomy.delete` respectively (ADR-0010 vocabulary), scoped to the target namespace with the resource name and repository available to the provider, and checked before any Git or datastore work. Update/delete authorization MUST use the stored resource (not caller-supplied fields) for scope. All three MUST require an authenticated principal. `deleteCategory`'s existing `category.delete` check is replaced by `categoryTaxonomy.delete`. No new legacy `category.*` action strings are introduced.
- **FR-022**: Updates MUST preserve the system-managed owner annotation and reject attempts to change it, consistent with Product and Repository updates.

#### Descendant filtering and navigation

- **FR-023**: `categories` MUST accept an optional `filter: CategoryFilterInput` with `descendantOf: String!` (category name in the queried namespace), `includeSelf: Boolean = false`, and `maxDepth: Int` (relative depth, 1–128; omitted means unbounded). When `filter` is omitted, behavior MUST be unchanged.
- **FR-024**: Descendant matching MUST be by whole hierarchy segment using each category's `status.resolved.path`. Categories without `status.resolved` are not matched (FR-002). String-prefix matching that would match a sibling such as `computers-refurb` for `computers` MUST NOT occur.
- **FR-025**: Filtered results MUST be ordered by ascending relative depth, then name, with opaque cursors stable across pages and the same page-size limits as the unfiltered query. `descendantOf` naming a non-existent category returns an empty connection.
- **FR-026**: Filtered queries MUST be served from a materialized ancestor index (one entry per ancestor–descendant pair, keyed by namespace and ancestor) so that a subtree query reads a bounded, paginated slice for that ancestor only — never a namespace-wide scan, in-memory filter, or unbounded cross-partition aggregate.
- **FR-027**: The ancestor index MUST be maintained by the API in the same write paths that change a category's effective hierarchy: controller status writes that set or change `status.resolved.path`, and final record removal. Admission does not write index entries. Each such write updates only that category's own entries (O(depth)); a subtree re-parent converges through the existing level-by-level controller cascade, with no new fan-out mechanism.
- **FR-028**: The ancestor index MUST converge to the authoritative records: a partially applied index write MUST be retried or repaired so that it never permanently diverges from the category's effective hierarchy, and it MUST be rebuildable from authoritative category records.
- **FR-029**: Existing categories MUST appear in filtered results after upgrade without requiring a push or a controller-side status change (a backfill from authoritative records), and the index schema MUST be added as a new incremental migration.
- **FR-030**: `Category.children` MUST return direct children from the ancestor index (relative depth 1), ordered by name, bounded per request. `Category.parent` MUST return the resolved parent when `ParentResolved=True`, otherwise `null`.
- **FR-031**: Filtered `categories` queries MUST be authorized exactly as the unfiltered `categories` query in that namespace (`categoryTaxonomy.list`); the filter adds no new permission.
- **FR-032**: `Category.products` remains unchanged by this feature; serving "products in this subtree" is out of scope (see Assumptions).

### Production Requirements *(mandatory for core-service or load-bearing changes)*

- **PR-001 Replica Safety**:
  - Hierarchy reads come only from each record's own `status.resolved`, so behavior is identical across replicas.
  - Mutations hold no replica-local state: correctness across concurrent replicas rests on the Git service's per-repository serialized ref update plus the post-receive head/superseded check (FR-017/FR-018). Two replicas mutating the same category concurrently produce at most one successful result per committed content; the other reports superseded.
  - Ancestor-index writes are idempotent and keyed by (namespace, ancestor, descendant), so concurrent or repeated writes from different replicas for the same category converge to its latest effective hierarchy (the write carrying the newer resource version wins).
  - **Rolling upgrade**: during rollout, old replicas do not serve the new mutations or filter and do not maintain the index. Status writes accepted by old replicas MUST be caught up by the backfill/repair of FR-028/FR-029 after rollout; filtered results may be incomplete only until that completes. New replicas MUST tolerate records written by old replicas (no index entries yet). `deleteCategory`'s payload change is a breaking change for clients selecting the removed fields and MUST be called out in release notes.
  - The Git service remains a singleton; this feature adds no Git replication or HA assumption.
- **PR-002 Multi-User Security**:
  - The mutations are checked against ADR-0010 actions through the pluggable authorization provider (FR-021), before any side effect.
  - Commit authorship is the authenticated subject.
  - Update/delete scope comes from the stored resource, so a caller cannot redirect a write to another namespace or repository through input fields.
  - The descendant filter cannot widen visibility: it returns only categories in a namespace the caller can already list.
  - Diagnostics MUST NOT disclose resources outside the caller's namespace (for example, a cross-namespace `parentRef` rejection does not reveal whether the target exists).
- **PR-003 Capacity**:
  - Removing `Category.path`/`Category.depth` removes per-resource work.
  - Each mutation performs one commit, one synchronous admission, and a bounded number of point reads.
  - Each index write is O(depth ≤ 128) entries for one category.
  - A filtered list reads one ancestor's slice for the requested page.
  - A re-parent of a subtree of size N at depth d causes O(N·d) index entry changes in total, spread across the existing cascade's per-category writes rather than issued in one request.
  - Capacity validation MUST cover:
    - sustained `createCategory`/`updateCategory` load concurrent with Git pushes to the same `gitstore-system` repository;
    - a re-parent of a large subtree (at least 10,000 descendants) with filtered-list p95 latency within the existing list query budget throughout the cascade.
- **PR-004 Backpressure**:
  - Mutations are bounded by the request context deadline and the Git service's existing per-repository write serialization; no new queue is introduced.
  - Index maintenance rides existing status writes and the existing controller work queue, so cascade pressure is governed by the controller's existing rate limiting and bounded fan-out.
  - Backfill MUST be paged and rate-bounded, never a single unbounded scan.
- **PR-005 Recovery**:
  - A crash between commit and admission leaves a committed manifest that the existing post-receive and admission recovery path admits on retry; the mutation's caller sees an error and may safely retry (FR-019 makes identical retries no-ops).
  - A crash during an index write is repaired per FR-028.
  - The controller cascade's existing checkpointed recovery continues to drive descendant convergence after restarts or replica handoff.
  - A failed post-receive admission is recorded on the resource (FR-020) and is fixed by a later corrected write, exactly like a push.

### Key Entities

- **CategoryTaxonomy (Category)**: A hierarchical classification resource. Exposes its hierarchy only through `status.resolved` (`path`: ancestor names from root to self; `depth`: root = 0), computed and kept fresh by the CategoryTaxonomy controller and `null` until first reconcile. Records its admission provenance (repository and path) for Git-backed updates and deletes.
- **Resolved Hierarchy Status**: The controller-maintained, per-category record of `path`/`depth` (plus other reconciled hierarchy metadata), updated asynchronously and propagated to descendants over one or more reconcile passes after any ancestor move. Present only once a category has been reconciled at least once; absent (null) before that point.
- **Category Ancestor Index**: A derived, rebuildable projection with one entry per (namespace, ancestor, descendant) carrying the descendant's relative depth, maintained from each category's effective hierarchy. Never authoritative; serves subtree queries and `children`.
- **Category Manifest**: The Git-authored Markdown file with YAML frontmatter (`categories/<name>.md` by convention) that is the desired state for a category, whether authored by push or by mutation.
- **Admission Diagnostic**: A structured `{reason, message, level, file?, field?}` entry describing why a manifest was rejected, returned by mutations and shaped for later carriage in an `AdmissionReport`.

## Success Criteria *(mandatory)*

### Measurable Outcomes

- **SC-001**: After a category is re-parented and the CategoryTaxonomy controller completes reconciling it, 100% of subsequent `status.resolved.path`/`status.resolved.depth` reads for that category return its current position.
- **SC-002**: For a category re-parented in one push or mutation, a pre-existing descendant that was not part of that change shows its corrected `status.resolved.path`, and appears under its new ancestors in filtered `categories` results, within the window it takes the existing reconcile cascade to reach that descendant.
- **SC-003**: Zero client-visible fields, filters or navigation results are derived from the admission-time ancestor path.
- **SC-004**: 100% of existing `categories` queries without `filter` continue to execute with unchanged results.
- **SC-005**: Introspection of `Category` shows no `path` or `depth` field.
- **SC-006**: For every manifest in a shared validation fixture set, `createCategory`/`updateCategory` and a Git push of the same manifest produce the same accept/reject decision and the same diagnostic messages; zero manifests are admissible via mutation but rejected via push.
- **SC-007**: Under concurrent mutation and push load on the same `gitstore-system` repository across two API replicas, zero mutations report success with a record that does not correspond to their own commit.
- **SC-008**: After every API-originated category delete, the manifest no longer exists at the head of its repository; zero category records enter `Terminating` through the API without a corresponding Git removal.
- **SC-009**: For a 10,000-descendant subtree, a filtered `categories` page returns within the existing list-query latency budget, and after any re-parent completes its cascade, the filtered result set equals the set computed by walking `status.resolved.path` for every category (zero missing, zero extra).
- **SC-010**: After upgrade, 100% of pre-existing categories appear correctly in filtered results without any push.

## Assumptions

- **Descendant fan-out mechanism already exists**: The CategoryTaxonomy controller's mechanism for propagating a hierarchy change down to pre-existing descendants already exists and functions correctly. This feature depends on that mechanism but does not build, modify, or extend it; ancestor-index convergence rides on it.
- **No fallback**: Clients rendering a just-created category tolerate `status.resolved` being `null` until first reconcile; no admission-time substitute is provided.
- **Pre-1.0 field removal**: Removing `Category.path`/`Category.depth` without a deprecation cycle is acceptable during the alpha series.
- **Target repository**: `createCategory` writes only to the namespace's `gitstore-system` repository (as `createProduct` does); writing new categories to arbitrary repositories is out of scope. Categories pushed to other repositories remain fully manageable via `updateCategory`/`deleteCategory` through stored provenance.
- **Mutation naming**: Mutations use the `Category` GraphQL type name (`createCategory`, `updateCategory`, `deleteCategory`), matching the existing `deleteCategory`, `updateCategoryStatus` and `watchCategories` fields, rather than ADR-0006's illustrative `createCategoryTaxonomy`. ADR-0006's mutation table is updated accordingly.
- **No resource-version precondition on create/update inputs**: Consistent with Product, Repository and Namespace, concurrency is governed by the ref-head/superseded check (FR-018) rather than an `expectedResourceVersion` input. Adding preconditions across all Git-backed kinds is separate work.
- **`descendantOf` by name, not path**: Category names are unique within a namespace, so a name identifies a subtree without the client knowing (or racing on) its current full path.
- **Product subtree listing**: `Category.products` ("includes subcategory products") is not addressed here; it would need either a product-side ancestor projection or an asynchronous materialization per ADR 0017, and is tracked separately.
- **Same admission-gap fix for Product**: Product's admission has the same "denial reported as success" gap as FR-017 addresses for CategoryTaxonomy. Fixing it for Product is out of scope but SHOULD reuse the same mechanism.
- **AdmissionReport**: ADR-0015's `AdmissionReport` resource, Git-notes projection and next-push echo are not implemented here; FR-020 only fixes the diagnostic entry shape so they can adopt it unchanged.
- **Pre-1.0 breaking payload change**: Replacing `DeleteCategoryPayload`'s fields (FR-014) is likewise acceptable during the alpha series without a deprecation cycle; `orphanedProductIds` has never carried data.

## Dependencies

- Depends on the CategoryTaxonomy controller's existing hierarchy-resolution and status-writeback mechanism, which computes and maintains `status.resolved.path`/`status.resolved.depth` and propagates changes to descendants after an ancestor move.
- Depends on the existing `status.resolved` data shape already defined for CategoryTaxonomy resources.
- Depends on the existing Git writer (`CommitFile`/`DeleteFile`) and committed-manifest admission used by the Product, Repository and Namespace mutations.
- Depends on ADR-0010's authorization action vocabulary (`categoryTaxonomy.*`), aligned with the canonical-vocabulary work in spec 064.
- Depends on ADR-0006 (CategoryTaxonomy lifecycle) and ADR-0015 §4 (two-lane admission diagnostics); ADR-0006 is amended for mutation naming, the Git-backed delete and the ancestor-index design.
- Related to this codebase's established schema-evolution principle of preferring additive changes and deprecation-before-removal for GraphQL-facing changes (FR-001 and FR-014 are the documented pre-1.0 exceptions).
