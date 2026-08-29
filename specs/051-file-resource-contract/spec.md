# Feature Specification: File Resource Contract — Kubernetes-style Frontmatter Schema

**Feature Branch**: `051-file-resource-contract`

**Created**: 2026-08-19

**Status**: Closed

**Input**: User description: "Support Kubernetes-style `File` frontmatter as the technical media primitive for GitStore (GH#79). Define the declarative `.spec`/`.status` schema contract for a brand-new `File` resource: top-level `apiVersion`/`kind`/`metadata`/`spec`/`status` envelope; `FileSpec` fields `contentType`, `source` (`FileSourceDefinition`), `type`, `processing.image.variants`; a lifecycle `status` indicator (`Uploaded`, `Processing`, `Ready`, `Failed`); the Markdown body used as alt text; validation that documents self-identify as `kind: File`. File-only — `MediaAsset` is an explicitly out-of-scope follow-on resource."

**Related**: GH#79 (task), GH#40 (parent initiative — Kubernetes-style frontmatter), ADR-0008 (File lifecycle), ADR-0001 (SecretRef reference contract)

## Clarifications

### Session 2026-08-21

- Q: The original description for this feature (and an earlier draft of GH#79) called for a `status.phase` lifecycle indicator (`Uploaded`/`Processing`/`Ready`/`Failed`) alongside `status.conditions`. Should `File` have a `status.phase` field at all? → A: No — removed entirely, not merely fixed. `status.phase` is the exact field Kubernetes itself deprecated on `Pod` for being a lossy, non-comprehensive rollup that could disagree with the condition set it was meant to summarize. An earlier revision of this spec tried to fix that by making `phase` a strictly-computed, never-independent rollup of `conditions` — but on further review, a second, always-in-sync field that says nothing `conditions` doesn't already say is redundant complexity with no consumer benefit, not a smaller version of the problem. GH#79's own Acceptance Criteria never required `phase` in the first place ("`status` is modelled with `resolved` and `conditions`" — no `phase`); only its Scope section did, and that section has been corrected to match. `File`'s lifecycle state is expressed exclusively through `status.conditions` (FR-013), matching every other catalog resource kind in this codebase, none of which has ever had a `phase` field.

## User Scenarios & Testing *(mandatory)*

### User Story 1 - Author a File Manifest Document (Priority: P1)

A technical author (a merchandiser, an integration, or a script acting on their behalf) creates a new `File` manifest by writing a Markdown file with Kubernetes-style YAML frontmatter that points at a binary asset already stored in Git LFS or object storage, and pushes it to a repository. The file does not exist in GitStore today in any form — no schema, no admission handling, no read path — so this is the first time a document can declare `kind: File` and be recognized rather than silently ignored or treated as unstructured content.

**Why this priority**: This is the foundational contract this specification exists to deliver. Every other resource that references a file (`Product`, `ProductVariant`, `CategoryTaxonomy`, `Collection`, and their `spec.media[*].fileRef` fields) already assumes a `File` resource exists; until this contract is defined, those references resolve to nothing.

**Independent Test**: A correctly structured `File` document can be parsed from YAML frontmatter and all schema fields extracted without error, using a single well-formed fixture file such as the worked example in GH#79. A document that identifies as a different `kind`, or omits required fields, is rejected with a descriptive error.

**Acceptance Scenarios**:

1. **Given** a Markdown file with valid `apiVersion: storage.gitstore.dev/v1beta1`, `kind: File`, complete `metadata`, and `spec` fields, **When** the system processes the file, **Then** all resource fields are successfully extracted and no validation errors are raised.
2. **Given** a file document missing the `kind` field, **When** the system processes the file, **Then** a descriptive error is returned indicating the missing required field.
3. **Given** a file document with `kind: Product` (or any kind other than `File`), **When** the system processes the file, **Then** the file is rejected with an error stating the kind must be `File`.
4. **Given** a file document with no `metadata.name`, **When** the system processes the file, **Then** the file is rejected with an error indicating the name is required.
5. **Given** a file document with a non-empty Markdown body, **When** the system reads the resource, **Then** the Markdown body is exposed as the file's alt text; **Given** a file document with an empty or omitted body, **When** the system reads the resource, **Then** alt text is an empty string rather than an error.

---

### User Story 2 - Use FileSpec to Declare Source, Content Type, and Processing Hints (Priority: P1)

A technical author fills in `spec.contentType`, `spec.type`, `spec.source` (a pointer to where the binary payload actually lives — Git, Git LFS, or object storage — plus its checksum), and optional `spec.processing.image.variants` to describe what rendition work should eventually happen against the source asset.

**Why this priority**: `spec` is the author-controlled portion of the resource and the part every consumer of a `File` record depends on. Without a defined `spec` contract, no downstream resource can reliably point at a `File` by name and expect a stable shape back.

**Independent Test**: A file spec with all supported fields (`contentType`, `type`, `source.type`/`source.uri`/`source.checksum`/`source.credentialsRef`, `processing.image.variants[].name`) round-trips correctly — values written in are values read out. A spec missing a required field, or using an unrecognized `source.type`, is rejected.

**Acceptance Scenarios**:

1. **Given** a file spec with `contentType`, `type`, a `source` with `type: git|lfs|s3|gcs` and a non-empty `uri`, an optional `checksum` (`algorithm` + `value`), an optional `credentialsRef`, and `processing.image.variants` with at least one entry, **When** the system parses the document, **Then** all fields are correctly modelled and accessible.
2. **Given** a file spec with only `contentType` and `source` (no `type` or `processing`), **When** the system parses the document, **Then** the optional fields default to empty/nil without error.
3. **Given** a file spec whose `source.type` is not one of the known values (`git`, `lfs`, `s3`, `gcs`), **When** the system processes the file, **Then** a validation error is returned naming the invalid `source.type` value.
4. **Given** a file spec whose `source.uri` is empty or missing, **When** the system processes the file, **Then** a validation error is returned for the missing `source.uri`.
5. **Given** a file spec with a `processing.image.variants` entry missing the required `name` field, **When** the system processes the file, **Then** a validation error is returned for the missing `processing.image.variants[].name`.
6. **Given** a file spec whose `source.credentialsRef` names a namespace different from the `File` document's own namespace, **When** the system processes the file, **Then** the file is rejected (cross-namespace credential references are not permitted).
7. **Given** an existing, previously admitted `File` document, **When** an update attempts to change `spec.contentType`, **Then** the update is rejected because `contentType` is immutable after first successful admission.

---

### User Story 3 - Read System-Populated FileStatus Conditions (Priority: P2)

An API consumer, operator, or another resource's read path inspects the `status.conditions` list of a `File` document to determine whether the file's binary payload has been admitted, verified, processed, and is ready to reference.

**Why this priority**: Status fields are machine-written; their schema must be documented so consumers can rely on the same structured condition model already used by other catalog resources, without inspecting raw file content or reverse-engineering a `File`-specific status shape.

**Independent Test**: A `FileStatus` object with all documented fields (`observedGeneration`, `lastAppliedRevision`, `conditions`, `resolved`) can be serialized and deserialized without data loss. Each condition type in the fixed set (`AdmissionAccepted`, `SourceResolved`, `ProcessingComplete`, `Ready`, `Terminating`) is representable in the model.

**Acceptance Scenarios**:

1. **Given** a file status containing `observedGeneration`, `lastAppliedRevision`, `conditions`, and a `resolved` block, **When** the system reads the status, **Then** all fields are accessible with correct types.
2. **Given** a file status with a `conditions` entry where `status` is not one of `True`, `False`, or `Unknown`, **When** the system validates the document, **Then** a validation error is raised for the invalid condition status value.
3. **Given** a file status with a `conditions` entry whose `type` is not one of the fixed enumeration (`AdmissionAccepted`, `SourceResolved`, `ProcessingComplete`, `Ready`, `Terminating`), **When** the system validates the document, **Then** a validation error is raised for the unrecognized condition type.
4. **Given** a newly admitted `File` document with no controller-driven processing yet performed, **When** the system reads the file, **Then** `status.conditions` contains `AdmissionAccepted=True` and `Ready=True`, with `SourceResolved`/`ProcessingComplete` absent (not yet populated, per Initial Condition Vocabulary) rather than the read erroring.
5. **Given** an author-pushed file document that itself contains a `status` block or a read-only `metadata` field (e.g. `uid`, `resourceVersion`), **When** the system processes the push, **Then** the push is rejected because those fields are system-managed and must not be author-supplied.

---

### Edge Cases

- What happens when a file document's `spec.source.type` is not one of the known values (`git`, `lfs`, `s3`, `gcs`)? (Rejected with a descriptive error naming the invalid value.)
- What happens when `spec.contentType` is empty or missing? (Rejected — `contentType` is required.)
- What happens when a file document contains an author-supplied `status` block or a read-only `metadata` field? (Rejected at admission validation; these fields are system-managed and stored in the datastore only.)
- What happens when the Markdown body is empty or omitted? (Alt text defaults to an empty string; this is not an error.)
- What happens when `spec.processing.image.variants` contains an entry with no `name`? (Rejected with a validation error for the missing `processing.image.variants[].name`.)
- What happens when an update attempts to change `spec.contentType` after the file's first successful admission? (Rejected — `contentType` is immutable after first admission, per ADR-0008; a content-type change implies a new file, not an in-place edit.)
- What happens when `spec.source.credentialsRef` points at a different namespace than the `File` document itself? (Rejected — cross-namespace secret references are not permitted, per ADR-0001.)
- What happens when `metadata.namespace` is omitted from the file? (Inherited from the repository/push context, consistent with other catalog resources.)
- What happens when a document identifies as any `kind` other than `File` (including the separate, out-of-scope `MediaAsset` kind)? (Rejected as the wrong kind; `MediaAsset` is a distinct resource with its own future contract, not an alias for `File`.)
- What happens when `metadata.name` collides with an existing `File` in the same namespace? (Rejected — `metadata.name` MUST be unique within its namespace, matching the identity rule already used by other catalog resources.)

## Requirements *(mandatory)*

### Functional Requirements

- **FR-001**: The system MUST accept `File` documents with top-level fields `apiVersion`, `kind`, `metadata`, and `spec`. The `status` field and read-only `metadata` fields are system-managed and stored in the datastore (not in git).
- **FR-002**: The system MUST reject any document where `kind` is not exactly `File`.
- **FR-003**: The canonical `apiVersion` for `File` resources MUST be `storage.gitstore.dev/v1beta1` — a distinct API group from `catalog.gitstore.dev/v1beta1` used by `Product`, `ProductVariant`, `CategoryTaxonomy`, and `Collection` — consistent with `File`'s classification as a Media Manifest resource (`docs/resource-storage/git-backed.md`) rather than a catalog/merchandising resource.
- **FR-004**: The system MUST reject any `File` document where `metadata.name` is absent or empty. Within a namespace, `metadata.name` MUST be unique — two `File` documents with the same name in the same namespace are not permitted. `metadata.namespace` is optional in the pushed file; when absent it is inherited from the repository/push context.
- **FR-005**: `metadata.name` and `metadata.namespace` MUST be immutable once a `File` resource is created (per ADR-0008); no operation in scope of this specification changes them in place.
- **FR-006**: The `spec` field MUST support `contentType` (string, required, non-empty) and `type` (string, optional free-form classification, e.g. `gitstore.dev/media`; not constrained to an enumeration in this phase).
- **FR-007**: The `spec` field MUST support `source`, a `FileSourceDefinition` with: `type` (required; one of the known values `git`, `lfs`, `s3`, `gcs`), `uri` (required, non-empty pointer to the binary payload's location), `checksum` (optional; `algorithm` + `value`), and `credentialsRef` (optional; uses the shared `SecretRef` contract — `kind: SecretRef`, `name`, optional `key`, optional `namespace` — with cross-namespace resolution rejected, per ADR-0001).
- **FR-008**: The `spec` field MUST support `processing.image.variants`, a list of image-variant requests. Each entry MUST have a required `name`; additional resize/format hints are reserved for future extension and are not required by this specification.
- **FR-009**: `spec.contentType` MUST be immutable after the `File` resource's first successful admission; changing content type requires creating a new `File`, not an in-place edit.
- **FR-010**: The Markdown body of a `File` document MUST be used as the resource's alt text. An empty or omitted body MUST yield an empty-string alt text, never an error.
- **FR-011**: `File` documents MUST be validated through the same admission pathway already used for other catalog resources (`Product`, `CategoryTaxonomy`, `Collection`) — self-identification as `kind: File`, required-field presence, and rejection of author-supplied `status`/read-only `metadata` fields are validated consistently with those existing resources. The detailed implementation of a `File`-specific admission policy (e.g. checksum verification, source accessibility, domain constraints beyond structural presence) is deferred to a follow-on specification, mirroring how Product's frontmatter contract (specs/014-product-frontmatter) deferred its own validation semantics and domain constraints.
- **FR-012**: The `status` field MUST support the following sub-fields: `observedGeneration`, `lastAppliedRevision`, `conditions`, and `resolved`. This field is stored in the datastore and merged with git content at read time; it is never persisted in the git repository.
- **FR-013**: The `conditions` list MUST use only `True`, `False`, or `Unknown` as valid values for `conditions[].status`. The `conditions[].type` field MUST be one of the fixed enumeration for v1beta1: `AdmissionAccepted`, `SourceResolved`, `ProcessingComplete`, `Ready`, `Terminating`. No custom or additional condition types are permitted in this version.
- **FR-014**: The `status.resolved` block MUST support a `resolvedVariants` placeholder for controller-computed rendition output; this specification defines its presence in the schema only — populating it is Phase 2 controller work and out of scope here.
- **FR-015**: `metadata` MUST support author-supplied fields: `name`, `generateName`, `namespace`, `labels` (string map), and `annotations` (string map). The read-only fields `uid`, `resourceVersion`, `generation`, `creationTimestamp`, and `revision` are system-managed and stored in the datastore; they are merged into the resource view at read time and MUST NOT appear in author-pushed git files.
- **FR-016**: `metadata.ownerReferences` MUST support: `apiVersion`, `kind`, `name`, and `uid`, and MUST be system-managed (never author-writable).
- **FR-017**: The system MUST reject any author-pushed file that contains `status` or read-only `metadata` fields (e.g. `uid`, `resourceVersion`) via admission validation. These fields are system-managed and stored in the datastore only.
- **FR-018**: The system MUST document the full schema with a complete worked example matching the example in GH#79.
- **FR-019**: The system MUST NOT introduce a `status.phase` field, or any equivalently-named single-enum lifecycle field, on `File`. Lifecycle state MUST be expressed exclusively through `status.conditions` (FR-013) — consistent with GH#79's Acceptance Criteria (which specifies `status` is modelled with `resolved` and `conditions`, never `phase`) and with every other catalog resource kind in this codebase, none of which has a `phase` field. This avoids reintroducing the pattern Kubernetes deprecated for `Pod.status.phase` (a second, lossy summary field capable of disagreeing with the condition set it purports to summarize).
- **FR-020**: This specification MUST NOT define, modify, or expand the schema, lifecycle, or admission behaviour of `MediaAsset`, `spec.media[*].fileRef` fields on other resources, binary payload upload (`requestFileUpload`), checksum verification, processing-pipeline execution, `fileRef` back-reference existence validation, or object-storage payload cleanup (`purgeFilePayload`) — all of these remain out of scope, deferred to their own follow-on specifications per ADR-0008's Phase 2 boundary.

### Key Entities

- **File**: Top-level media-manifest resource. Identified by `kind: File`. Author-supplied (stored in git): `apiVersion`, `kind`, writable `metadata`, `spec`, and the Markdown body (used as alt text). System-managed (stored in datastore): `status` and read-only `metadata` fields. The full resource view is a merge of both at read time. `File` is Git-backed for the manifest only; the binary payload lives in Git LFS or object storage and is never stored in the git tree.
- **FileSpec**: The declarative description of a file manifest — content type, a free-form type classification, the source pointer, and optional image-processing hints. `contentType` is immutable after first admission.
- **FileSourceDefinition**: A pointer to the binary payload's actual location: `type` (`git`/`lfs`/`s3`/`gcs`), `uri`, optional `checksum` (`algorithm` + `value`), and optional `credentialsRef` (a `SecretRef`, same-namespace only).
- **FileProcessingDefinition**: Optional processing hints, currently scoped to `image.variants` — a list of requested renditions, each identified minimally by a required `name`.
- **FileStatus**: System-written state stored in the datastore (not in git). Contains reconciliation `conditions` (the sole lifecycle-state signal — no `phase` field, per FR-019), observed generation, last applied revision, and a `resolved` block reserved for controller-computed rendition output. Merged into the resource view at read time.
- **ObjectMeta**: Common metadata carried by all catalogue resources (shared with `Product`, `ProductVariant`, `CategoryTaxonomy`, `Collection`, `Repository`). Includes identity fields (`name`, `namespace`, `uid`), classification (`labels`, `annotations`), ownership (`ownerReferences`), and read-only system fields (`resourceVersion`, `generation`, `creationTimestamp`, `revision`). `name` is unique within its `namespace`.
- **SecretRef**: The shared reference contract (ADR-0001) used by `spec.source.credentialsRef` to point at object-storage credentials managed outside GitStore's Git-backed resource store; resolution is same-namespace only in v1.
- **Condition**: The shared status-signal shape (`type`, `status` True/False/Unknown, `reason`, `message`, `observedGeneration`, `lastTransitionTime`) already used by `Product`, `CategoryTaxonomy`, and `Repository`. For `File` v1beta1 the valid `type` values are a fixed enumeration: `AdmissionAccepted`, `SourceResolved`, `ProcessingComplete`, `Ready`, `Terminating`.
- **fileRef consumers (out of scope, motivating context only)**: `Product`, `ProductVariant`, `CategoryTaxonomy`, and `Collection` already define `spec.media[*].fileRef` (an `ObjectReference` naming a `File` by `kind`/`name`, with an `optional` flag) that resolves to nothing today because `File` does not exist. This specification's contract is what those references will resolve against once implemented; this specification does not modify those other resources.

### Initial Condition Vocabulary

- This feature defines the fixed condition-type enumeration for `File.status.conditions`, per ADR-0008: `AdmissionAccepted`, `SourceResolved`, `ProcessingComplete`, `Ready`, `Terminating`.
- Of these, only `AdmissionAccepted` is set by the admission path described in this contract (structural validation and datastore persistence succeed). `SourceResolved` (checksum verification against `spec.source.checksum`) and `ProcessingComplete` (all requested `processing.image.variants` generated) are Phase 2 controller-owned conditions and are not populated by this feature. `Ready` is set optimistically `True` immediately after `AdmissionAccepted=True` in this phase, per ADR-0008, pending future re-evaluation once `SourceResolved`/`ProcessingComplete` are implemented. `Terminating` is out of scope of this feature entirely — no delete/finalizer flow is defined here.
- There is no `status.phase` or other single-enum lifecycle summary field (FR-019) — `conditions` is the only lifecycle-state signal for `File`, matching every other catalog resource kind.
- Any future condition-producing feature MUST reuse this fixed enumeration and the shared `Condition` shape without introducing `File`-specific duplicates.

## Success Criteria *(mandatory)*

### Measurable Outcomes

- **SC-001**: Every `File` document that conforms to the documented schema is accepted without errors.
- **SC-002**: Every `File` document that violates a required constraint (wrong `kind`, missing `contentType`, missing/invalid `source.type`, missing `source.uri`, missing `processing.image.variants[].name`, author-supplied `status`/read-only fields, cross-namespace `credentialsRef`) is rejected with a specific, actionable error message.
- **SC-003**: All `FileSpec` fields documented in GH#79 (`contentType`, `type`, `source`, `processing.image.variants`) round-trip correctly — a document written with all supported fields produces identical output when read back.
- **SC-004**: All `FileStatus` fields (`observedGeneration`, `lastAppliedRevision`, `conditions`, `resolved`) are representable in the model without data loss, and every `File` resource returns a well-defined, non-null status the first time it is read after admission.
- **SC-005**: A complete worked example document (matching the example in GH#79) is included in project documentation and passes schema validation without modification.
- **SC-006**: 100% of `File` `status.conditions` entries use exclusively the fixed five-value type enumeration and the shared `True`/`False`/`Unknown` status vocabulary, with zero `File`-specific condition-type reinvention.
- **SC-007**: The Markdown body is retrievable as alt text for 100% of `File` documents, including those with an empty body (returned as an empty string, never an error).
- **SC-008**: The schema specification is approved and stable before dependent follow-on work begins — a `File` admission policy implementation, controller/reconciler work for source resolution and processing, and `fileRef` resolution in `Product`/`ProductVariant`/`CategoryTaxonomy`/`Collection`.

## Assumptions

- **`status.phase` does not exist, by design (removed, not merely fixed)**: two earlier drafts of this specification each tried a different way to keep a `phase` field — first as an independently-set value deliberately allowed to disagree with the `Ready` condition, then as a strictly-computed rollup that could never disagree with `conditions`. Per design review, both were rejected in favor of dropping the field entirely (FR-019): a rollup that only ever restates what `conditions` already says is redundant surface area with no consumer benefit, not a smaller version of the k8s `Pod.status.phase` problem. GH#79's own Acceptance Criteria never required `phase` — only its Scope section did, and that section has been corrected to match this decision. `conditions` is the sole lifecycle-state signal for `File`.
- **`spec.type` is a free-form classification string** (e.g. `gitstore.dev/media`) in this phase, not constrained to an enumeration. A future phase may introduce a closed set of allowed values once more `type` use cases exist.
- **`processing.image.variants` sub-schema is intentionally minimal.** GH#79 only mandates the field's existence and that each entry be identifiable (`name`); this specification does not define resize/format/quality sub-fields. Those are reserved for a future specification once processing behaviour (Phase 2) is designed.
- **`apiVersion` is fixed at `storage.gitstore.dev/v1beta1`** for this release, a distinct API group from `catalog.gitstore.dev/v1beta1`, matching `File`'s classification as a Media Manifest resource per `docs/resource-storage/git-backed.md`; versioning strategy for future API versions is out of scope.
- **The binary payload itself, checksum verification, and processing-pipeline execution are explicitly out of scope.** Per ADR-0008, these are Phase 2 controller responsibilities. This specification defines only the manifest schema and the pointer contract (`spec.source`) the controller will eventually act on.
- **`MediaAsset` is a separate, out-of-scope resource.** This specification does not define, reference as a dependency, or otherwise couple `File`'s schema to `MediaAsset`'s eventual schema, per GH#79's explicit framing and ADR-0008's "Relationship to MediaAsset" section.
- **`spec.media[*].fileRef` on other resources is motivating context, not in-scope work.** `Product`, `ProductVariant`, `CategoryTaxonomy`, and `Collection` already declare `fileRef` fields that will resolve against `File` records defined by this contract; this specification does not modify those resources' specs, reconcilers, or admission policies.
- **Full CRUD mutation implementation and the `File`-specific admission policy's implementation are out of scope.** This specification defines the schema/data contract that future `createFile`/`updateFile`/`deleteFile`/`getFile`/`listFiles` operations and a `File` admission policy will implement against — mirroring how specs/014-product-frontmatter defined `Product`'s schema separately from its later validation-semantics and domain-constraints follow-on work.
- **Cross-namespace references are rejected in v1.** Both `spec.source.credentialsRef` (per ADR-0001's `SecretRef` contract) and any future `fileRef` resolution are same-namespace only; this specification does not introduce a cross-namespace resolution mechanism.
- **`metadata.name` is unique within namespace and immutable, matching the identity rule already used by `Product`, `ProductVariant`, `CategoryTaxonomy`, and `Collection`.** No Git rename/move semantics beyond what those existing resources already define are introduced here.
- **File location convention**: when a repository path is not otherwise specified, a `File` document lives at `files/<metadata.name>.md`, per ADR-0008 — the same convention family already used for other catalog resource kinds, applied consistently rather than inventing a `File`-specific layout.
