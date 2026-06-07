# Feature Specification: Collection Resource Contract with Label Selectors

**Feature Branch**: `022-collection-resource-contract`
**Created**: 2026-06-07
**Status**: Closed
**Input**: User description: "Collection Resource Contract with Label Selectors"

## Clarifications

### Session 2026-06-07

- Q: Should resolved product references be stored inline in `status.resolved`? → A: No. `status.resolved` stores only `memberCount` (like product/category status counts). Resolved products are exposed via a paginated `collection.products(first, last, ...)` GraphQL connection, not stored in status.
- Q: When membership changes mid-pagination of `collection.products`, how should the cursor behave? → A: Snapshot-at-query-time. The cursor is point-in-time; pages reflect membership as evaluated when the first page was fetched, preventing skips or ghost products across pages.
- Q: When `spec.selector` is omitted entirely, what does the Collection match? → A: No products. An empty or absent selector yields zero membership; an operator must provide a non-empty selector for any products to be included.
- Q: When `memberCount` diverges from the live `collection.products` count, which is authoritative? → A: `collection.products` is the authoritative live source. `memberCount` in `status.resolved` is a cached hint that may lag until the next reconciliation cycle.
- Q: When a Collection is deleted, what happens to its member products? → A: Products are unaffected. They exist independently and remain in the catalog; a Collection does not own its members.
- Q: Does `collection.products` require separate access control beyond reading the Collection? → A: No. `collection.products` inherits the same namespace-scoped access as the Collection itself; no additional per-field ACL is applied.

## User Scenarios & Testing *(mandatory)*

### User Story 1 — Define a Collection via git push (Priority: P1)

A store operator authors a `Collection` Markdown file with a `kind: Collection` frontmatter envelope, commits it to the catalog repository, and pushes. The system validates the document and admits it as a `Collection` resource, making it queryable by name.

**Why this priority**: This is the foundational capability — no other collection behaviour is meaningful unless a Collection can be authored and persisted.

**Independent Test**: Push a single valid `Collection` document and verify it appears in the `collections` GraphQL query with correct `metadata`, `spec.title`, and an empty or absent `spec.selector`.

**Acceptance Scenarios**:

1. **Given** a valid `Collection` frontmatter document with `kind: Collection`, `metadata.name`, and `spec.title`, **When** it is pushed to the catalog repository, **Then** the system admits the resource and it is retrievable by name with all authored fields preserved.
2. **Given** a document whose `kind` is `Collection` but `spec.title` is missing, **When** it is pushed, **Then** the push is rejected with a descriptive validation error referencing the missing field.
3. **Given** a document with an unrecognised `kind`, **When** it is pushed alongside a valid Collection, **Then** only the unrecognised document is rejected; the valid Collection is admitted.

---

### User Story 2 — Query a Collection and its members (Priority: P1)

A storefront developer queries the GraphQL API for a `Collection` by name. The response includes the collection metadata, spec (title, selector, media), status with a `memberCount`, and a paginated `products` connection for traversing matched products.

**Why this priority**: Without queryability the authored resource has no consumer-facing value.

**Independent Test**: Query `collection(by: {namespacePath: {namespace: "...", name: "..."}})` and assert that `spec.title`, `spec.selector`, `status.resolved.memberCount`, and the `products(first: 10)` connection edges are present and correct.

**Acceptance Scenarios**:

1. **Given** a Collection whose selector matches two products in the namespace, **When** queried via GraphQL, **Then** `status.resolved.memberCount` equals 2 and `collection.products(first: 10)` returns both products.
2. **Given** a Collection whose selector matches no products, **When** queried, **Then** `status.resolved.memberCount` is 0 and `collection.products(first: 10).edges` is empty.
3. **Given** a Collection with `spec.media` entries, **When** queried, **Then** `spec.media` is returned with each `fileRef.name`, `fileRef.kind`, and `fileRef.optional` value intact.

---

### User Story 3 — Selector-driven membership (Priority: P2)

An operator uses `spec.selector.matchLabels` and `spec.selector.matchExpressions` to declare which products belong to a collection. Products labelled appropriately are included; unlabelled products are excluded.

**Why this priority**: Label-selector membership is the core differentiator of this resource type over a manually curated list, but the P1 stories already deliver a usable collection without it.

**Independent Test**: Create a Collection with `matchLabels: {gitstore.dev/brand: apple}`, push products with and without that label, then verify only labelled products appear via `collection.products(first: 10)` and `memberCount` equals the matching count.

**Acceptance Scenarios**:

1. **Given** a `matchLabels` selector and products with matching labels in the same namespace, **When** the Collection is reconciled, **Then** only products whose labels satisfy every key-value pair are included.
2. **Given** a `matchExpressions` entry with operator `In` and a set of values, **When** the Collection is reconciled, **Then** products whose label value appears in the set are included; others are excluded.
3. **Given** a `matchExpressions` entry with operator `NotIn`, **When** reconciled, **Then** products whose label value appears in the set are excluded.
4. **Given** both `matchLabels` and `matchExpressions` present, **When** reconciled, **Then** a product must satisfy all constraints to be included (logical AND).

---

### User Story 4 — Update and re-validate a Collection (Priority: P2)

An operator edits the Collection document (changes title, selector, or media), pushes the update, and the system re-validates and re-resolves members.

**Why this priority**: Collections will be updated frequently as merchandising strategy changes.

**Independent Test**: Push an updated Collection with a narrower selector and verify `status.resolved.memberCount` decreases accordingly and `collection.products(first: 10)` returns only the narrowed set.

**Acceptance Scenarios**:

1. **Given** an existing Collection, **When** the operator narrows the selector and pushes, **Then** `memberCount` decreases and `collection.products(first: ...)` reflects only the products satisfying the new selector.
2. **Given** an existing Collection, **When** the operator changes `spec.title` and pushes, **Then** the updated title is returned in GraphQL without affecting resolved members.

---

### Edge Cases

- What happens when `spec.selector.targetRef` references a kind other than `Product`?
- Deleting a Collection has no effect on its member products — products are independent resources not owned by the Collection.
- How does the system behave when a `matchExpressions` entry has an empty `values` list with operator `In`?
- What happens when the same product matches multiple Collections — is it included in all of them?
- How does resolution behave when a Collection and several of its matched products are created in the same push?
- What is returned for `status.resolved` and `collection.products` before any reconciliation has occurred?

## Requirements *(mandatory)*

### Functional Requirements

- **FR-001**: System MUST accept a `Collection` Markdown document with valid `apiVersion: catalog.gitstore.dev/v1beta1`, `kind: Collection`, `metadata.name`, and `spec.title` via git push.
- **FR-002**: System MUST reject a `Collection` document missing `spec.title` at push time with a descriptive error message.
- **FR-003**: System MUST reject a `Collection` document with an invalid `apiVersion` or mismatched `kind`.
- **FR-004**: System MUST persist `spec.selector.matchLabels` and `spec.selector.matchExpressions` without modification.
- **FR-005**: System MUST resolve collection membership by evaluating `spec.selector` against all products in the same namespace and store the resolved `memberCount` in `status.resolved`. A Collection with no `spec.selector` (omitted or empty) MUST resolve to zero members.
- **FR-006**: System MUST support `matchExpressions` operators `In`, `NotIn`, `Exists`, and `DoesNotExist`.
- **FR-007**: When both `matchLabels` and `matchExpressions` are present, System MUST apply them as a logical AND; a product must satisfy all constraints to be a member.
- **FR-008**: System MUST expose a `collection(by: ...)` GraphQL query supporting lookup by namespace + name and by globally unique ID.
- **FR-009**: System MUST expose a paginated `collections` GraphQL listing scoped to a namespace, using cursor-based pagination.
- **FR-010**: System MUST persist `spec.media` entries and return them in GraphQL responses with `fileRef.name`, `fileRef.kind`, and `fileRef.optional`.
- **FR-011**: System MUST expose a paginated `collection.products(first, last, after, before)` GraphQL connection that returns the products matched by the Collection's selector. The connection MUST use snapshot-at-query-time cursor semantics: all pages in a single traversal reflect membership as evaluated when the first page was requested. `collection.products` is the authoritative live source for membership; `status.resolved.memberCount` is a cached hint that may lag until the next reconciliation cycle.
- **FR-012**: System MUST write `status.conditions` entries for `SelectorAccepted`, `MembersResolved`, and `Ready` on each reconciliation.
- **FR-013**: `spec.selector.targetRef`, when present, MUST only accept `kind: Product`; other target kinds MUST be rejected at push time.

### Key Entities

- **Collection**: A named, namespace-scoped catalog resource that groups products via a declarative label selector. Carries `metadata`, `spec` (title, selector, media), and `status` (conditions, memberCount).
- **LabelSelector**: A selector composed of `matchLabels` (exact key-value pairs) and `matchExpressions` (set-based expressions), evaluated against product labels to determine membership.
- **LabelSelectorRequirement**: A single expression with `key`, `operator` (`In`, `NotIn`, `Exists`, `DoesNotExist`), and `values`.
- **ResolvedCollectionDefinition**: The computed status snapshot containing `memberCount` (a cached hint, may lag reconciliation) and resolved `media`. Matched products are not stored here; they are queried live via the `collection.products` connection, which is the authoritative membership source.
- **CollectionStatus**: The full status envelope including `conditions`, `observedGeneration`, `lastAppliedRevision`, and `resolved`.

## Success Criteria *(mandatory)*

### Measurable Outcomes

- **SC-001**: A valid Collection document pushed to the catalog repository is admitted and queryable within the same request-response cycle as the push acknowledgement.
- **SC-002**: `collection.products(first: 20)` returns correct results for a namespace containing up to 10,000 products within 2 seconds.
- **SC-003**: All four `matchExpressions` operators (`In`, `NotIn`, `Exists`, `DoesNotExist`) are verified by automated tests covering both matching and non-matching cases.
- **SC-004**: Invalid Collection documents (missing title, wrong kind, unsupported target) are rejected at push time in 100% of cases with a human-readable error message.
- **SC-005**: The `collections` GraphQL listing returns consistent, non-duplicated results across all pages for a namespace with at least 50 collections.

## Assumptions

- Label keys and values follow the same conventions as product labels already in use (`gitstore.dev/` prefix for system keys).
- `targetRef` in `CollectionSpec` defaults to `kind: Product`; other target kinds are out of scope.
- Resolution is computed during the reconciliation cycle after a push; near-realtime staleness is acceptable.
- `matchExpressions` with operator `Exists` or `DoesNotExist` ignore the `values` field.
- An absent or empty `spec.selector` yields zero membership; it is not treated as a universal selector.
- Collections are namespace-scoped; cross-namespace selectors are out of scope.
- `collection.products` inherits the same access control as the Collection resource itself; no separate per-field authorization is applied.
- `collection.products` evaluates the selector at query time using snapshot-at-query-time cursor semantics; membership changes between pages of a single traversal are not reflected mid-pagination.

## Dependencies

- GH#84: Parent Collection initiative (selector schema and overall design)
- GH#40: ObjectMeta, Condition, and ObjectReference contracts (shared envelope types)
- Spec 021 (`021-category-taxonomy`): Established the Kubernetes-style resource envelope pattern this spec follows
