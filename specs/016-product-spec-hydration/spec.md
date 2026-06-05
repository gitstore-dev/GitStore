# Feature Specification: Product Spec and Status Hydration

**Feature Branch**: `016-product-spec-hydration`
**Created**: 2026-06-04
**Status**: Closed
**Input**: User description: "GH#186 and #235"
**Related**: GH#186 (spec/status field validation semantics), GH#235 (GraphQL hydration + pagination), GH#77 (parent initiative), GH#40 (Kubernetes-style Catalog Frontmatter initiative), GH#79 (File frontmatter — media URL resolution), GH#82 (CategoryTaxonomy — categoryRef + category path resolution), GH#83 (ProductVariant — pricing/inventory/variant resolution), spec#014 (Product Resource Contract), spec#015 (Product Parser)

## Clarifications

### Session 2026-06-04

- Q: Which fields of `status.resolved` are in scope for this feature vs. GH#40-deferred? → A: Pass-through hydration of all stored `status.resolved` sub-fields from the blob is in scope. Cross-resource reference *resolution* is deferred to specific GH#40 sub-issues: `category` path computation → GH#82 (CategoryTaxonomy); `priceRange`/`totalInventory`/`variantSummary`/`defaultVariantRef` → GH#83 (ProductVariant); `media` URL resolution → GH#79 (File frontmatter). `spec.categoryRef` is likewise a pass-through stored reference; its resolver is GH#82's concern. This feature surfaces whatever the pipeline has written to the stored blob, no more.

## User Scenarios & Testing *(mandatory)*

### User Story 1 — Query a Product and Receive Full Spec Data (Priority: P1)

A developer or storefront client sends a GraphQL query for a single product or a list of products. The response includes all spec fields that were present in the product's git file: title, tags, media references, options, and category reference. Currently the API returns an empty spec for every product regardless of what was pushed.

**Why this priority**: Spec data (title, tags, options) is the primary content of a product record. Returning empty spec data is a correctness failure that silently breaks every consumer of the catalogue API. Nothing downstream (storefronts, search indexers, catalogue UIs) can function correctly until this is fixed.

**Independent Test**: Push a product file with a populated spec (title, two tags, two options), then query it via `product(id)`. The response must contain matching title, tags, and options. Can be verified with a single round-trip integration test.

**Acceptance Scenarios**:

1. **Given** a product stored with `spec.title`, `spec.tags`, `spec.options`, and `spec.categoryRef`, **When** a client queries that product, **Then** all spec fields in the response match the values from the original git file.
2. **Given** a product stored with a minimal spec (`spec: {}`), **When** a client queries that product, **Then** `spec.title` is null, `spec.tags` is an empty list, `spec.media` is an empty list, and `spec.options` is an empty list — no error is returned.
3. **Given** a list of products in a namespace, **When** a client queries `products(namespace)`, **Then** every product in the result has its spec fully populated, not empty.

---

### User Story 2 — Query a Product's System Status (Priority: P1)

An operator or CI pipeline queries a product's status to check whether the ingest pipeline has successfully processed it — inspecting condition types (e.g. `Ready`), condition status (`TRUE`/`FALSE`/`UNKNOWN`), and any resolved definition (computed media). Currently the API never returns status data, even for products that have been successfully ingested.

**Why this priority**: Status is the primary observability signal for the ingest pipeline. Without it, operators cannot determine whether a push succeeded, a media reference resolved, or an error occurred post-admission.

**Independent Test**: Ingest a product through the pipeline, then query its status. The response must include at least one condition entry. Independently testable against the running pipeline.

**Acceptance Scenarios**:

1. **Given** a product that has been successfully ingested, **When** a client queries its status conditions, **Then** the response includes at least one condition with a valid `type` and `status`.
2. **Given** a product with no status recorded (newly admitted, not yet reconciled), **When** a client queries its status, **Then** `status` is null — no error is returned and the spec fields are still returned correctly.
3. **Given** a product whose status includes a `resolved` definition, **When** a client queries `status.resolved`, **Then** the resolved media entries are present in the response.

---

### User Story 3 — Paginate Through Products Using Cursors (Priority: P1)

A developer builds a paginated product listing. They request the first page, then use the returned `endCursor` to request the next page. Currently the second (and all subsequent) page requests return the same first page of results.

**Why this priority**: Broken pagination means any namespace with more than one page of products cannot be fully traversed. This is a data-correctness bug in production that affects every deployment with a non-trivial catalogue.

**Independent Test**: Create 25 products in a namespace, fetch three pages of 10 using forward cursors. Verify each page contains distinct products and all three together equal all 25 products.

**Acceptance Scenarios**:

1. **Given** a namespace with 25 products, **When** a client fetches page 1 (10 items), then page 2 using `endCursor`, then page 3 using `endCursor`, **Then** each page returns 10, 10, and 5 distinct products respectively.
2. **Given** a namespace with 5 products, **When** a client fetches the first page with a page size of 10, **Then** all 5 are returned and `pageInfo.hasNextPage` is false.
3. **Given** a forward-paginated result, **When** a client uses the final page's cursor as the `after` argument, **Then** an empty connection is returned (not the first page again).
4. **Given** a backward-paginated query using `last`/`before`, **When** a client navigates backward, **Then** pages advance in the correct direction and contain distinct, non-repeating products.

---

### User Story 4 — Spec Field Validation at Ingest (Priority: P2)

An operator ingests a product via the pre-receive hook. The ingest pipeline enforces that stored spec fields satisfy their documented constraints before writing to the datastore — title length, option name uniqueness, and media reference completeness. Violations are rejected with field-specific errors.

**Why this priority**: The parser (spec#015) enforces admission-time constraints from the git file. This story closes the remaining gap between what the parser accepts and what the datastore contract requires.

**Independent Test**: Submit a product whose spec passes the parser but violates a post-ingest constraint. The pipeline must reject the write with a specific error identifying the field.

**Acceptance Scenarios**:

1. **Given** a `spec.title` that exceeds 200 characters, **When** the ingest pipeline attempts to write the product, **Then** the write is rejected with an error identifying `spec.title` and the length constraint.
2. **Given** a `spec.options` list with duplicate `name` values, **When** the ingest pipeline processes the product, **Then** the write is rejected with an error identifying the duplicate option name.
3. **Given** a spec where all fields satisfy their constraints, **When** the ingest pipeline processes the product, **Then** the product is written to the datastore without a validation error.

---

### Edge Cases

- What happens when a product's stored spec data is malformed and cannot be deserialised? The API must not panic; return a null spec and a resolvable error signal rather than crashing.
- What happens when `status` contains a condition type that is no longer recognised by the schema? The condition is returned as-is without error — forward-compatibility is required.
- What happens when a cursor encodes a position for a product that has since been deleted? The next page continues from the next available product after that position — no error and no repeated results.
- What happens when `first` and `last` are both provided in the same query? The system must return a user-facing error; mixing forward and backward pagination is forbidden.
- What happens when a cursor is used against a different namespace than it was issued for? The cursor must be treated as invalid and return an error.
- What happens when `spec.media` contains a `fileRef` that has not yet been resolved by the pipeline? The spec entry is returned verbatim; `status.resolved` is absent or null for that entry.

## Requirements *(mandatory)*

### Functional Requirements

- **FR-001**: The product GraphQL response MUST populate all spec fields from the stored spec blob: `title`, `categoryRef`, `tags`, `media`, and `options`. An absent or empty stored spec MUST produce an empty (not null) `ProductSpec` with empty lists for all collection fields.
- **FR-002**: The product GraphQL response MUST include the `status` field when a non-null status blob is present in the datastore. When no status has been recorded, `status` MUST be null.
- **FR-003**: `status.conditions` MUST be deserialised and returned as a typed list; each condition MUST include `type`, `status`, `lastTransitionTime`, and optional `reason`/`message` fields.
- **FR-004**: `status.resolved` MUST be included in the response when the status blob contains a resolved definition; it MUST be null otherwise. All sub-fields present in the stored blob (`media`, `category`, `priceRange`, `totalInventory`, `variantSummary`, `defaultVariantRef`) MUST be passed through verbatim from the stored blob. Computing those values (media URL resolution → GH#79, category path traversal → GH#82, pricing/variant aggregation → GH#83) is out of scope for this feature.
- **FR-005**: The `products` query MUST support cursor-based forward pagination (`first`/`after`) and backward pagination (`last`/`before`). Successive pages MUST return distinct, non-overlapping results.
- **FR-006**: Cursor values MUST be opaque to callers. A cursor received from one response MUST be valid as input for the subsequent page of the same query.
- **FR-007**: `pageInfo.hasNextPage` MUST be `true` if and only if more items exist after the current page. `pageInfo.hasPreviousPage` MUST be `true` if and only if more items exist before the current page.
- **FR-008**: A query that supplies both `first`/`after` and `last`/`before` simultaneously MUST be rejected with a user-facing error.
- **FR-009**: The ingest pipeline MUST validate `spec.title` length (≤200 characters), `spec.options` name uniqueness, and `spec.media[].fileRef` presence before writing to the datastore. Violations MUST produce errors identifying the specific field and violated constraint.
- **FR-010**: `status.observedGeneration` MUST equal `metadata.generation` at the time the status was last written by the pipeline.
- **FR-011**: The datastore-to-GraphQL converter MUST be the single authoritative mapping point for all product fields. Resolvers MUST NOT perform additional field mapping outside the converter.

### Key Entities

- **ProductSpec** (stored): JSON blob in the datastore representing the author-supplied spec fields from the product's git file. The authoritative source for all `spec.*` GraphQL fields.
- **ProductStatus** (stored): JSON blob written by the ingest pipeline after admission. Contains `observedGeneration`, `lastAppliedRevision`, `conditions`, and optionally `resolved`. Never authored by git file contributors.
- **ProductCondition**: A single status condition entry with `type`, `status` (`TRUE`/`FALSE`/`UNKNOWN`), `lastTransitionTime`, and optional `reason`/`message`.
- **ResolvedProductDefinition**: The computed post-ingest view containing resolved `media` entries that map `fileRef` names to storage-backed resources.
- **PageCursor**: An opaque token encoding the keyset position `(createdAt, id)` for the last item on a page. Passed verbatim to continue pagination.

## Success Criteria *(mandatory)*

### Measurable Outcomes

- **SC-001**: Every product returned by the API contains a fully populated `spec` matching the values stored at ingest — zero products with empty spec when spec data exists in the datastore.
- **SC-002**: Every product with a recorded status returns non-null `status.conditions` with at least one entry — zero products silently omitting pipeline status.
- **SC-003**: A 25-product namespace paginated at 10 per page with forward cursors yields three distinct, non-overlapping pages totalling all 25 products — zero repeated results across pages.
- **SC-004**: All documented spec field constraints (title length, option name uniqueness, media fileRef presence) are enforced at ingest with zero silent violations written to the datastore.
- **SC-005**: No existing passing test is broken by these changes — zero regressions across the full test suite.

## Assumptions

- `Spec` and `Status` are stored as JSON blobs in the datastore; deserialization into typed GraphQL model structs happens in the API layer, not the datastore layer.
- The ingest pipeline writes `ProductStatus` asynchronously after admission; a product may legitimately exist in the datastore with a null status if reconciliation has not yet run.
- Keyset pagination for products uses `(createdAt, id)` as the sort key, consistent with the pattern already used for other entity types in the same codebase.
- The `products` table schema in production is partitioned by `namespace`; the pagination helper will be adapted accordingly.
- The memdb backend already handles pagination correctly in-memory; changes are primarily scoped to the Scylla backend.
- `status.resolved` pass-through hydration is in scope. Computing the values within `resolved` is out of scope: media URL resolution (GH#79), category path traversal (GH#82), and pricing/variant aggregation (GH#83) are each owned by dedicated GH#40 sub-issues and wired in by their respective GraphQL resolvers.
- `spec.categoryRef` is a pass-through stored reference; the GraphQL resolver that fetches the referenced Category resource is a GH#82 concern, not this feature's.
- Mixing `first`/`after` with `last`/`before` in the same query is forbidden per the Relay Connection Specification.
