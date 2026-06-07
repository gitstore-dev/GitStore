# Feature Specification: Collection Frontmatter Integration Tests and Documentation

**Feature Branch**: `023-collection-integration-tests`  
**Created**: 2026-06-07  
**Status**: Closed  
**Input**: User description: "#84 and #215"

## User Scenarios & Testing *(mandatory)*

### User Story 1 - Verify Valid Collection Document Is Accepted (Priority: P1)

A catalog maintainer pushes a valid `Collection` frontmatter document into a repository. The system accepts the push, resolves selector membership, and reports a `Ready` status with the correct member count and resolved media reference.

**Why this priority**: This is the core happy-path scenario that proves end-to-end Collection processing works — from document ingestion through selector evaluation to status reporting. All other scenarios depend on this path being trustworthy.

**Independent Test**: Can be fully tested by pushing a well-formed Collection YAML document and verifying the system returns a `Ready` condition with a non-zero member count.

**Acceptance Scenarios**:

1. **Given** a repository with products labeled `gitstore.dev/brand: apple` and `gitstore.dev/product-type: laptop`, **When** a Collection document with `matchLabels: {gitstore.dev/brand: apple}` and a `matchExpressions` entry requiring `gitstore.dev/product-type In [laptop]` is pushed, **Then** the system accepts the push, returns `conditions[Ready].status: "True"`, and `resolved.memberCount` equals the number of matching products.

2. **Given** a Collection document with a valid `media` array referencing an optional file, **When** the collection is pushed and the referenced file does not exist, **Then** the system accepts the push and `resolved.media` is empty for that entry.

---

### User Story 2 - Verify Invalid Collection Document Is Rejected (Priority: P1)

A catalog maintainer pushes a malformed or incomplete `Collection` document. The system rejects the push with a clear, actionable error message identifying the exact validation failure.

**Why this priority**: Rejection behavior gates data quality; without reliable validation, corrupt collection definitions silently enter the catalog. Equally critical to the happy path.

**Independent Test**: Can be fully tested by pushing a document that is missing required fields and observing a failed push with a descriptive error message.

**Acceptance Scenarios**:

1. **Given** a Collection document missing the `spec.title` field, **When** it is pushed, **Then** the push is rejected with an error message indicating that `title` is required.

2. **Given** a document with `kind: Product` instead of `kind: Collection`, **When** it is pushed through the Collection processing path, **Then** the push is rejected with an error indicating kind mismatch.

3. **Given** a Collection document with a malformed `matchExpressions` entry (e.g., unknown `operator` value), **When** it is pushed, **Then** the push is rejected with an error identifying the invalid expression.

4. **Given** a Collection document where `spec.targetRef.kind` is set to a value other than `Product`, **When** it is pushed, **Then** the push is rejected because only `Product` is a supported target kind.

---

### User Story 3 - Validate Selector Semantics and Deterministic Membership (Priority: P2)

A catalog maintainer queries a collection and expects the member list to be deterministic — the same selector always returns the same set of products given the same catalog state.

**Why this priority**: Deterministic resolution is a correctness invariant. Non-deterministic behavior would undermine trust in collection-driven storefronts and is a prerequisite for higher-level features like ranking and pagination.

**Independent Test**: Can be fully tested by running the same collection resolution twice against an unchanged catalog and asserting the member lists are identical in content and order.

**Acceptance Scenarios**:

1. **Given** a catalog with 10 products where 4 match the collection selector, **When** the collection membership is resolved twice in sequence without catalog changes, **Then** both resolutions return the same 4 products in the same order.

2. **Given** a collection with `matchLabels` only, **When** a new product with matching labels is added to the catalog, **Then** the next resolution includes the new product.

3. **Given** a collection with `matchExpressions` using `operator: NotIn`, **When** the collection is resolved, **Then** only products whose label value is not in the exclusion list are included.

---

### User Story 4 - Access Documentation and Examples (Priority: P2)

A developer integrating with the GitStore catalog reads the documentation and can, within a few minutes, produce a valid `Collection` document without referring to source code.

**Why this priority**: Integration tests prove the system works; documentation enables adoption. Without docs, new integrators must reverse-engineer the schema from code or existing files.

**Independent Test**: Can be tested by having a developer unfamiliar with the feature write a Collection document using only the docs, then verifying the document passes validation.

**Acceptance Scenarios**:

1. **Given** the published documentation, **When** a developer follows the documented example, **Then** the resulting document passes all validation rules without modification.

2. **Given** the documentation, **When** a developer looks up error semantics for a specific field, **Then** the documentation clearly states what the field expects and what error is returned when it is wrong.

---

### Edge Cases

- What happens when a Collection selector matches zero products? The system accepts the document, sets `resolved.memberCount` to 0, and reports `MembersResolved: "True"` with a `reason: NoProductsMatched` message.
- What happens when the same product matches multiple collections? Each collection resolves independently; a product may appear in many collections without conflict.
- How does the system handle a Collection document with an empty `metadata.name`? The push is rejected with a validation error before selector evaluation occurs.
- What happens when a Collection document's `media` array references a non-optional file that does not exist? The push is accepted but `conditions[Ready]` remains `"False"` with `reason: MediaNotResolved`.
- What happens when a push contains multiple Collection documents in the same commit? Each document is validated independently; if any document is invalid the entire push is atomically rejected (consistent with the pre-receive hook model used for other resource kinds).

## Requirements *(mandatory)*

### Functional Requirements

- **FR-001**: The test suite MUST verify that a valid Collection document is accepted end-to-end, including status condition reporting (`Ready`, `SelectorAccepted`, `MembersResolved`).
- **FR-002**: The test suite MUST verify that a Collection document missing any required field (`apiVersion`, `kind`, `metadata.name`, `metadata.namespace`, `spec.title`) is rejected with a descriptive error.
- **FR-003**: The test suite MUST verify that `kind: Collection` is enforced and any other kind value causes rejection.
- **FR-004**: The test suite MUST verify that `spec.targetRef.kind` accepts only `Product`; other values must be rejected.
- **FR-005**: The test suite MUST verify all `LabelSelector` operator variants: `In`, `NotIn`, `Exists`, `DoesNotExist`, and plain `matchLabels`.
- **FR-006**: The test suite MUST verify deterministic membership: repeated resolution of the same selector against an unchanged catalog yields identical results.
- **FR-007**: The test suite MUST verify that adding a product with matching labels causes it to appear in the next collection resolution.
- **FR-008**: The test suite MUST verify that a Collection with an optional `media` reference is accepted when the referenced file is absent.
- **FR-009**: The test suite MUST cover the zero-member scenario and assert `resolved.memberCount: 0`.
- **FR-010**: Documentation MUST include a complete, copy-pasteable example Collection document that passes all validation rules.
- **FR-011**: Documentation MUST describe all validation error messages and the conditions under which they are returned.
- **FR-012**: Documentation MUST describe all `CollectionStatus` fields: `conditions`, `lastAppliedRevision`, `observedGeneration`, and `resolved`.

### Key Entities

- **Collection**: A catalog resource identified by `apiVersion: catalog.gitstore.dev/v1beta1` and `kind: Collection`. Carries a `spec` with `title`, `media`, and `selector`, and a `status` block populated by the system after resolution.
- **LabelSelector**: A selector composed of `matchLabels` (exact key-value pairs) and `matchExpressions` (key-operator-values tuples). Determines which products belong to a collection.
- **CollectionStatus**: System-managed fields written after resolution: `conditions` (Ready, SelectorAccepted, MembersResolved), `resolved.memberCount`, `resolved.media`, `lastAppliedRevision`, `observedGeneration`.
- **MediaDefinition**: A reference to a `File` resource with an optional flag. Resolved into a URL-bearing entry in `resolved.media` within status.

## Success Criteria *(mandatory)*

### Measurable Outcomes

- **SC-001**: All integration test cases pass with 100% success rate against a live catalog stack before the feature is merged.
- **SC-002**: Each of the five LabelSelector operator variants (`In`, `NotIn`, `Exists`, `DoesNotExist`, `matchLabels`) is exercised by at least one test case.
- **SC-003**: A developer new to the feature can produce a valid Collection document within 5 minutes using only the published documentation.
- **SC-004**: Documentation covers 100% of publicly visible `CollectionSpec` and `CollectionStatus` fields with descriptions and at least one example value each.
- **SC-005**: Repeated resolution of a fixed collection against an unchanged catalog returns identical membership lists across at least 10 consecutive runs with zero non-determinism failures.

## Assumptions

- A push containing multiple Collection documents is atomically rejected if any one document is invalid, consistent with the pre-receive hook model used for other resource kinds.
- Integration tests will run against the same Docker Compose infrastructure introduced in spec `020-pre-receive-validation-e2e` (ScyllaDB 5.4 + full API stack).
- `LabelSelector` validation rules (field constraints, allowed operators) are finalized per closed issues #213 and #214; this spec does not reopen those decisions.
- `spec.selector` is optional in the Collection document; when absent, the collection resolves to zero members.
- `spec.targetRef` is optional in the document; when omitted, `kind: Product` is the implied default. Only `Product` is a valid explicit value.
- Integration tests MUST run against both the memdb backend (default, no infra required) and ScyllaDB backend; both must pass before the feature is merged.
- Documentation will live in the existing `docs/` directory structure following conventions established by prior features.
