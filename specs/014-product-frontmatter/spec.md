# Feature Specification: Product Resource Contract — Kubernetes-style Frontmatter Schema

**Feature Branch**: `014-product-frontmatter`  
**Created**: 2026-06-01  
**Status**: Closed  
**Input**: User description: "Define Product Resource Contract: Kubernetes-style Frontmatter Schema"  
**Related**: GH#184 (task), GH#77 (initiative), GH#40 (parent initiative)

## Clarifications

### Session 2026-06-01

- Q: How should the system handle existing product files that use the old frontmatter format? → A: Reject legacy files with a descriptive error; migration is out of scope for alpha software.
- Q: What is the uniqueness scope for `metadata.name`? → A: Unique within namespace (Kubernetes convention).
- Q: How should the system handle author-supplied `status` blocks in pushed files? → A: Reject at the admission validation layer (pre-receive). Note: `status` and read-only fields are never stored in git — git holds only author-supplied frontmatter and markdown body. The system hydrates the full resource view (including `status` and read-only `metadata` fields) from the datastore (ScyllaDB/memdb) at read time. Git history remains clean.
- Q: Are `conditions[].type` values a fixed enumeration or open extension? → A: Fixed enumeration for v1beta1 — only the six documented condition types are valid (`Published`, `AdmissionAccepted`, `CategoryResolved`, `OptionsAccepted`, `VariantsResolved`, `Ready`).
- Q: What is the cardinality of `spec.categoryRef`? → A: Single reference — one category per product (consistent with GH#82 single-category product constraint).

## User Scenarios & Testing *(mandatory)*

### User Story 1 — Author a Product Catalogue File (Priority: P1)

A merchant author creates a new product by writing a Markdown file with Kubernetes-style YAML frontmatter. They populate `apiVersion`, `kind`, `metadata`, `spec`, and `status` fields and push the file to the repository.

**Why this priority**: This is the core value of the feature — defining the canonical shape every product document must conform to. All downstream parsing, validation, and integration work depends on this contract being stable.

**Independent Test**: A correctly structured product file can be parsed from YAML frontmatter and all schema fields extracted without error. This can be validated with a single well-formed fixture file.

**Acceptance Scenarios**:

1. **Given** a Markdown file with valid `apiVersion: catalog.gitstore.dev/v1beta1`, `kind: Product`, complete `metadata`, and `spec` fields, **When** the system processes the file, **Then** all resource fields are successfully extracted and no validation errors are raised.
2. **Given** a product file missing the `kind` field, **When** the system processes the file, **Then** a descriptive error is returned indicating the missing required field.
3. **Given** a product file with `kind: Category` (wrong kind), **When** the system processes the file, **Then** the file is rejected with an error stating the kind must be `Product`.
4. **Given** a product file with no `metadata.name`, **When** the system processes the file, **Then** the file is rejected with an error indicating the name is required.

---

### User Story 2 — Use ProductSpec to Declare Product Attributes (Priority: P1)

A merchant author fills in `spec.title`, `spec.categoryRef`, `spec.tags`, `spec.media`, and `spec.options` to describe the product's display attributes, taxonomy placement, and variant dimensions.

**Why this priority**: `spec` is the author-controlled portion of the resource. Without a defined spec contract, downstream consumers cannot rely on any product attributes.

**Independent Test**: A product spec with all supported fields (`title`, `categoryRef`, `tags`, `media`, `options`) round-trips correctly — values written in are values read out.

**Acceptance Scenarios**:

1. **Given** a product spec with `title`, a valid `categoryRef`, a list of `tags`, `media` entries with `fileRef`, and `options` with name/title/values, **When** the system parses the document, **Then** all fields are correctly modelled and accessible.
2. **Given** a product spec with only `title` and `categoryRef` (all other fields omitted), **When** the system parses the document, **Then** optional fields default to empty/nil without error.
3. **Given** a product spec with an `options` entry missing the required `name` field, **When** the system processes the file, **Then** a validation error is returned for the missing `options.name`.

---

### User Story 3 — Read System-Populated ProductStatus (Priority: P2)

An API consumer or operator reads the `status` block of a product document to check readiness conditions, resolved category information, price range, and variant summary.

**Why this priority**: Status fields are machine-written; their schema must be documented so consumers can rely on stable field names and condition types without inspecting raw file content.

**Independent Test**: A `ProductStatus` object with all documented fields can be serialized and deserialized without data loss. Each condition type in the example set (`Published`, `AdmissionAccepted`, `CategoryResolved`, `OptionsAccepted`, `VariantsResolved`, `Ready`) is representable in the model.

**Acceptance Scenarios**:

1. **Given** a product status containing `observedGeneration`, `lastAppliedRevision`, `conditions`, and a fully populated `resolved` block, **When** the system reads the status, **Then** all fields are accessible with correct types.
2. **Given** a product status with a `conditions` entry where `status` is not one of `True`, `False`, or `Unknown`, **When** the system validates the document, **Then** a validation error is raised for the invalid condition status value.
3. **Given** a newly created product with no status yet written, **When** the system reads the product, **Then** `status` is treated as empty/nil without error.

---

### Edge Cases

- What happens when `metadata.labels` contains a key or value that exceeds allowed length?
- How does the system handle a `spec.options` list with duplicate `name` values?
- What happens if `spec.categoryRef.kind` is set to a value other than `CategoryTaxonomy`?
- How does the system behave when `status.resolved.priceRange` contains an invalid decimal string (e.g. `"not-a-number"`)?
- What happens when `metadata.namespace` is omitted — is it inherited from context or rejected?
- What happens when a product file uses the old (pre-Kubernetes-style) frontmatter format — it must be rejected with a clear error (no compatibility shim; migration is out of scope for alpha).

## Requirements *(mandatory)*

### Functional Requirements

- **FR-001**: The system MUST accept product documents with top-level fields `apiVersion`, `kind`, `metadata`, and `spec`. The `status` field and read-only `metadata` fields are system-managed and stored in the datastore (not in git).
- **FR-002**: The system MUST reject any product document where `kind` is not exactly `Product`.
- **FR-003**: The system MUST reject any product document where `metadata.name` is absent or empty. Within a namespace, `metadata.name` MUST be unique — two product documents with the same name in the same namespace are not permitted.
- **FR-004**: The `spec` field MUST support the following sub-fields: `title` (string), `categoryRef` (ObjectReference — exactly one category per product), `tags` (list of strings), `media` (list of MediaDefinition), and `options` (list of ProductOptionDefinition).
- **FR-005**: Each `options` entry MUST have a `name` field; `title` and `values` are optional.
- **FR-006**: Each `media` entry MUST contain a `fileRef` with at least `name` and `kind`; `optional` defaults to `false`.
- **FR-007**: The `status` field MUST support the following sub-fields: `observedGeneration`, `lastAppliedRevision`, `conditions`, and `resolved`. This field is stored in the datastore and merged with git content at read time; it is never persisted in the git repository.
- **FR-008**: The `conditions` list MUST use only `True`, `False`, or `Unknown` as valid values for `conditions[].status`. The `conditions[].type` field MUST be one of the fixed enumeration for v1beta1: `Published`, `AdmissionAccepted`, `CategoryResolved`, `OptionsAccepted`, `VariantsResolved`, `Ready`. No custom or additional condition types are permitted in this version.
- **FR-009**: The `status.resolved` block MUST support: `category` (name + path), `priceRange` (per currency: min/max decimals), `totalInventory` (integer), `variantSummary` (total/ready/unavailable counts), `defaultVariantRef` (ObjectReference), and `media` (list of resolved file definitions with name/url/contentType).
- **FR-010**: `metadata` MUST support author-supplied fields: `name`, `generateName`, `namespace`, `labels` (string map), and `annotations` (string map). The read-only fields `uid`, `resourceVersion`, `generation`, `creationTimestamp`, and `revision` are system-managed and stored in the datastore; they are merged into the resource view at read time and MUST NOT appear in author-pushed git files.
- **FR-011**: `metadata.ownerReferences` MUST support: `apiVersion`, `kind`, `name`, and `uid`.
- **FR-012**: The canonical `apiVersion` for all product resources MUST be `catalog.gitstore.dev/v1beta1`.
- **FR-013**: The system MUST document the full schema with a complete worked example (as found in GH#77).
- **FR-014**: The system MUST reject any product document that does not conform to the Kubernetes-style schema (i.e., missing `apiVersion` or using a legacy frontmatter format) with a descriptive error message. Migration tooling is explicitly out of scope (alpha software).
- **FR-015**: The system MUST reject any author-pushed file that contains `status` or read-only `metadata` fields (e.g., `uid`, `resourceVersion`) via admission validation. These fields are system-managed and stored in the datastore only.

### Key Entities

- **Product**: Top-level catalogue resource. Identified by `kind: Product`. Author-supplied (stored in git): `apiVersion`, `kind`, writable `metadata`, `spec`, and markdown body. System-managed (stored in datastore): `status` and read-only `metadata` fields. The full resource view is a merge of both at read time.
- **ProductSpec**: The declarative description of a product — title, category placement, tags, media references, and option dimensions.
- **ProductStatus**: System-written state stored in the datastore (not in git). Contains reconciliation conditions, resolved references, and aggregate metrics (price range, inventory). Merged into the resource view at read time.
- **ObjectMeta**: Common metadata carried by all catalogue resources. Includes identity fields (`name`, `namespace`, `uid`), classification (`labels`, `annotations`), ownership (`ownerReferences`), and read-only system fields (`resourceVersion`, `generation`, `creationTimestamp`, `revision`). `name` is unique within its `namespace`.
- **ObjectReference**: A pointer to another catalogue resource, identified by `apiVersion`, `kind`, `name`, and optionally `namespace`, `uid`, `resourceVersion`, and `fieldPath`. `spec.categoryRef` is a singular `ObjectReference` — a product belongs to exactly one category.
- **MediaDefinition**: A product media slot referencing a `File` resource by name and kind, with an `optional` flag.
- **ProductOptionDefinition**: A variant dimension (e.g. Colour, Size) with a `name`, optional `title`, and list of `values`.
- **Condition**: A named status signal with `type`, `status` (True/False/Unknown), `reason`, `message`, `observedGeneration`, and `lastTransitionTime`. For v1beta1 the valid `type` values are a fixed enumeration: `Published`, `AdmissionAccepted`, `CategoryResolved`, `OptionsAccepted`, `VariantsResolved`, `Ready`.
- **ResolvedProductDefinition**: System-computed aggregates: resolved category path, price range per currency, total inventory, variant summary, default variant reference, and resolved media URLs.

## Success Criteria *(mandatory)*

### Measurable Outcomes

- **SC-001**: Every product catalogue document that conforms to the documented schema is accepted without errors.
- **SC-002**: Every product document that violates a required constraint (missing `kind`, missing `metadata.name`, invalid condition status) is rejected with a specific, actionable error message.
- **SC-003**: All `ProductSpec` fields documented in GH#77 round-trip correctly — a document written with all supported fields produces identical output when read back.
- **SC-004**: All `ProductStatus` fields documented in GH#77 are representable in the model without data loss.
- **SC-005**: A complete worked example document (matching the example in GH#77) is included in project documentation and passes schema validation without modification.
- **SC-006**: The schema specification is approved and stable before dependent tasks GH#185 (validation semantics) and GH#186 (domain constraints) begin implementation.

## Assumptions

- `apiVersion` is fixed at `catalog.gitstore.dev/v1beta1` for this release; versioning strategy for future versions is out of scope.
- `status` fields and read-only `metadata` fields are never stored in git. Git holds only author-supplied frontmatter (`apiVersion`, `kind`, writable `metadata`, `spec`) and the markdown body. The system writes `status` and read-only fields to the datastore (ScyllaDB in production, memdb in development) and merges them into the full resource view at read time.
- `metadata.namespace` is optional in the file; when absent, it is resolved from the repository/push context.
- `spec.options[].values` may be empty for open-ended option dimensions; this is acceptable per the schema.
- Validation of `metadata.labels` key/value length follows Kubernetes label conventions (63-character max per key segment, 253-character max for prefix).
- `spec.categoryRef` references a `CategoryTaxonomy` resource; cross-resource existence validation is deferred to the admission phase (GH#105, GH#106).
- Decimal values in `priceRange` (min/max) are represented as strings to avoid floating-point precision issues.
