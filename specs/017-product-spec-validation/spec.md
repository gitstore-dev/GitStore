# Feature Specification: Product Spec/Status Validation Semantics and Integration Tests

**Feature Branch**: `017-product-spec-validation`  
**Created**: 2026-06-05  
**Status**: Closed  
**Issues**: #186 (Validation Semantics), #187 (Integration Tests and Documentation)  
**Parent**: #77 (Support Kubernetes-style Product frontmatter)  
**Blocked by**: #184 (CLOSED), #185 (CLOSED)

---

## User Scenarios & Testing *(mandatory)*

### User Story 1 — Author receives precise errors when spec fields are invalid (Priority: P1)

A catalog author pushes a product file whose `spec` block contains invalid values — a title over 200 characters, a media entry missing its `fileRef.name`, or a duplicate option name. They expect the push to be rejected with a clear, field-scoped error message that names exactly what is wrong and where, so they can fix it in one edit.

**Why this priority**: This is the primary value of validation — without it, malformed products are silently accepted and surface as corrupt data at query time.

**Independent Test**: Push a product file with a title exceeding 200 characters and verify the rejection message names `spec.title` and the 200-character constraint.

**Acceptance Scenarios**:

1. **Given** a product file whose `spec.title` is 201 characters, **When** the author pushes the file, **Then** the system rejects it with an error naming `spec.title` and the 200-character limit.
2. **Given** a product file with a `media` entry whose `fileRef` has no `name`, **When** the author pushes the file, **Then** the system rejects it and names `spec.media[N].fileRef.name` as required.
3. **Given** a product file with two `options` entries sharing the same `name`, **When** the author pushes the file, **Then** the system rejects it and identifies the duplicate option name.
4. **Given** a product file where `spec.options[N].name` is absent, **When** the author pushes the file, **Then** the system rejects it and reports the index of the offending entry.
5. **Given** a product file where `spec.categoryRef` is present but `categoryRef.name` is absent, **When** the author pushes the file, **Then** the system rejects it with an error naming `categoryRef.name`.
6. **Given** a product file where `spec` is an empty object (`spec: {}`), **When** the author pushes the file, **Then** the system accepts it — all spec fields are optional.
7. **Given** a product file with multiple spec violations, **When** the author pushes the file, **Then** the system reports all violations together in a single response (not fail-fast).

---

### User Story 2 — Author is blocked from setting system-managed fields (Priority: P1)

A catalog author (or a misconfigured tool) includes a `status` block or a read-only metadata field (`uid`, `resourceVersion`, etc.) in a product file. The system must reject the file before storing anything and explain clearly what is forbidden.

**Why this priority**: Allowing author-written `status` would corrupt the pipeline state the controller writes. Allowing read-only metadata fields would create inconsistencies the system cannot recover from without a manual wipe.

**Independent Test**: Push a product file containing a `status:` key and verify it is rejected before any data is stored, with a message identifying `status` as system-managed.

**Acceptance Scenarios**:

1. **Given** a product file that includes a top-level `status` key with any content, **When** the author pushes the file, **Then** the system rejects it with a message stating `status` is system-managed.
2. **Given** a product file that includes `status: {}` (empty map), **When** the author pushes the file, **Then** the system still rejects it — presence of the key, not its content, triggers the guard.
3. **Given** a product file that includes any read-only metadata field (`uid`, `resourceVersion`, `generation`, `creationTimestamp`, `revision`, `ownerReferences`), **When** the author pushes the file, **Then** the system rejects it and names each forbidden field in the error.

---

### User Story 3 — Operator queries a product and receives accurate pipeline status (Priority: P2)

An operator queries a product via the catalog API and expects the `status` field to reflect the real pipeline state written by the controller — including all conditions and `lastAppliedRevision`. If the controller has not yet processed the product, `status` must be absent, not fabricated or empty.

**Why this priority**: Without this, operators cannot determine whether a product is ready for sale or blocked by a pipeline failure.

**Independent Test**: Ingest a product, write a controller status directly to the datastore, and query the product via the API to verify all condition fields are present and correctly represented.

**Acceptance Scenarios**:

1. **Given** a product whose controller has set `status.conditions` with `Ready=True` and all six condition types, **When** the operator queries the product, **Then** the response includes all conditions with correct statuses and a non-empty `lastAppliedRevision`.
2. **Given** a product that has never been processed by the controller, **When** the operator queries the product, **Then** the `status` field is absent from the response (not an empty object).
3. **Given** a product whose status blob was written with Kubernetes-style casing (`"Ready"`, `"True"`), **When** the operator queries the product, **Then** the API returns all conditions correctly — none are dropped due to casing mismatch.
4. **Given** a product whose `status.resolved.priceRange` contains decimal monetary values, **When** the operator queries the product, **Then** the values are returned without precision loss.

---

### User Story 4 — Developer validates the full lifecycle via documented examples (Priority: P3)

A developer or operator can run documented examples through the full product lifecycle (parse → validate → store → query → status write → query again) and have each step produce the stated outcome, both for the happy path and the most common rejection cases.

**Why this priority**: Documentation and integration tests are the contract between the catalog system and its consumers. Without them, regressions go undetected.

**Independent Test**: Run the integration test suite against a live catalog service and confirm every documented scenario passes without skips.

**Acceptance Scenarios**:

1. **Given** the integration test suite is run against a running catalog service, **When** all tests execute, **Then** every test passes with zero failures and zero skips.
2. **Given** the documentation includes a complete valid product file example, **When** a developer copies and pushes it verbatim, **Then** the system accepts it.
3. **Given** the documentation includes rejection examples (e.g. `status` present, title too long, missing `fileRef.name`), **When** a developer follows those examples, **Then** the system returns the exact documented error message.

---

### Edge Cases

- What happens when `spec.options` is an empty list vs. absent? Both must be accepted (all spec fields optional).
- What happens when a `media` entry has `optional: true` but no `fileRef.name`? Must still be rejected — `optional` does not waive the `name` requirement.
- What happens when `status: {}` (empty map) is present? Must be rejected — the key's presence is sufficient.
- What happens when a label key prefix exceeds 253 characters? Must be rejected, naming the prefix and the limit.
- What happens when `spec.categoryRef` is set but `categoryRef.name` is absent? Must be rejected.
- What happens when `resolved.priceRange` uses a zero-decimal currency (e.g. JPY)? Must round-trip without truncation or rounding.
- What happens when the status blob has a condition with an unrecognised `type`? The system must not crash; the condition should be passed through or logged as a warning.

---

## Requirements *(mandatory)*

### Functional Requirements

**Spec field validation — author-facing (#186)**

- **FR-001**: The system MUST reject a product file where `spec.title` exceeds 200 characters, reporting the field name and the limit.
- **FR-002**: The system MUST reject a product file where any `spec.media[N].fileRef` entry is missing `name` or `kind`, reporting the index and field.
- **FR-003**: The system MUST reject a product file where `spec.options` contains duplicate `name` values, reporting the duplicate.
- **FR-004**: The system MUST reject a product file where any `spec.options[N].name` is absent, reporting the index.
- **FR-005**: The system MUST reject a product file where `spec.categoryRef` is present but `categoryRef.name` is absent.
- **FR-006**: The system MUST accept a product file where `spec` is an empty object — no spec field is mandatory.
- **FR-007**: The system MUST reject a product file containing a top-level `status` key, regardless of its value, with a message stating it is system-managed.
- **FR-008**: The system MUST reject a product file containing any read-only metadata field (`uid`, `resourceVersion`, `generation`, `creationTimestamp`, `revision`, `ownerReferences`), naming each offending field.
- **FR-009**: When a product file contains multiple violations, all violations MUST be reported together in a single rejection response.

**Status hydration contract — operator-facing (#186)**

- **FR-010**: The catalog API MUST return the full `ProductStatus` as written by the controller, including all conditions, `observedGeneration`, `lastAppliedRevision`, and the `resolved` sub-object when present.
- **FR-011**: The catalog API MUST return `status` as absent for products that have never been processed by the controller.
- **FR-012**: Condition type and status values written in Kubernetes TitleCase (`"Ready"`, `"True"`) MUST be normalised to the API's representation at read time — no condition may be silently dropped due to casing mismatch.
- **FR-013**: Monetary values in `resolved.priceRange` (`min`, `max`) MUST survive a serialisation round-trip without precision loss for any currency.

**Integration tests and documentation (#187)**

- **FR-014**: An integration test suite MUST cover the full product lifecycle: parse valid file → accept; parse invalid file → reject with correct error message; store product → query product → verify spec and status fields match what was stored.
- **FR-015**: The integration test suite MUST contain zero `t.Skip` / `t.Skipf` calls.
- **FR-016**: Documentation MUST include at least one complete valid product file example and at least one rejection example for each of: `status` present, title too long, missing `fileRef.name`.
- **FR-017**: Each documentation example file MUST be parseable by the actual parser without modification (examples are tested, not illustrative-only).

### Key Entities

- **ProductResource**: Top-level author-writable envelope (`apiVersion`, `kind`, `metadata`, `spec`). The `status` key is strictly forbidden from this type.
- **ProductSpec**: Author-controlled declaration: `title`, `categoryRef`, `tags`, `media`, `options`. All fields optional individually; constraints apply when present.
- **ProductStatus**: System-written pipeline state: `conditions`, `observedGeneration`, `lastAppliedRevision`, `resolved`. Never accepted from authors; hydrated by the catalog API from the datastore at read time.
- **Condition**: A named pipeline signal on a product with `type`, `status` (True/False/Unknown), `observedGeneration`, `lastTransitionTime`, `reason`, `message`.
- **ResolvedProductDefinition**: System-computed aggregates attached to `ProductStatus` — resolved category path, price range, inventory totals, variant summary, resolved media URLs.
- **MediaDefinition**: A product media slot referencing a File resource; `fileRef.name` and `fileRef.kind` are both required when the entry is present.
- **ProductOptionDefinition**: A variant dimension (e.g. Colour, Size) with a unique, required `name`, optional display `title`, and `values` list.

---

## Success Criteria *(mandatory)*

### Measurable Outcomes

- **SC-001**: Every category of invalid product file (FR-001 through FR-008) produces a rejection error that names the specific field and violated constraint — verified for each case individually.
- **SC-002**: A product whose status was written by the controller is returned by the catalog API with all condition fields intact — zero conditions dropped on any well-formed status blob.
- **SC-003**: The integration test suite passes in full (zero failures, zero skips) against a running catalog service for every scenario in FR-014.
- **SC-004**: Documentation examples can be copied verbatim and either accepted or rejected by the parser exactly as the documentation states.
- **SC-005**: Monetary values in `priceRange` survive a serialisation round-trip with no change in value, verified for at least three currency types including one zero-decimal currency.

---

## Assumptions

- `catalog.gitstore.dev/v1beta1` is the stable API version for this work; no version migration is in scope.
- The controller is the sole writer of `ProductStatus`; the catalog API is read-only with respect to status.
- Decimal monetary precision follows the existing `shopspring/decimal` implementation already in use.
- Integration tests target a Scylla-backed environment (the `-tags scylla` build constraint is required).
- PR #236 (`016-product-spec-hydration`) must be merged before the #187 integration tests are written, as the status hydration fix it contains is a prerequisite for FR-010–FR-013 and FR-014.

---

## Dependencies

- **Blocked by**: #184 (schema contract — CLOSED), #185 (parser/kind validation — CLOSED)
- **Depends on**: PR #236 merge for FR-010–FR-013 and the full-lifecycle integration tests in FR-014
- **Parent**: #77 (Kubernetes-style Product frontmatter initiative)
- **Blocks**: #187 (Integration Tests and Documentation) — blocked by this spec
