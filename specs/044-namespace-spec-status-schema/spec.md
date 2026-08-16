# Feature Specification: Namespace Resource Contract: Declarative .spec/.status Schema

**Feature Branch**: `044-namespace-spec-status-schema`  
**Created**: 2026-08-16  
**Status**: Closed  
**Input**: User description: "Namespace Resource Contract: Declarative .spec/.status Schema (GH#171). Context: this is a sub-issue of GH#170 (Declarative Namespace Resource Contract), itself part of the Kubernetes-style Catalog Frontmatter initiative (GH#40). GH#171 is currently unblocked and blocks both GH#172 (Namespace API Semantics: spec writes, status updates, concurrency) and GH#173 (Namespace Validation and Admission Matrix), which in turn blocks GH#174 (Namespace Watch Contract). Define the declarative .spec/.status schema for the Namespace resource, following the same Kubernetes-style contract pattern already established for other catalog resources (CategoryTaxonomy, Collection, Product) in this repo, so that GH#172/#173/#174 can build on a stable schema."

## Clarifications

### Session 2026-08-16

- Q: What is the canonical write path for Namespace create, update, and delete, and how should GraphQL mutations participate? → A: Git is canonical; Namespace GraphQL mutations delegate to Git under GH#170.
- Q: How must the new Namespace schema handle the constitution's public-interface versioning rule? → A: Add the declarative fields alongside deprecated flat fields; remove the flat fields only in a future major GraphQL API release.
- Q: Which top-level resource shape should Namespace use? → A: Use the standard `apiVersion`, `kind`, `metadata`, `spec`, and `status` envelope; map the human identifier to `metadata.name` and omit `metadata.namespace`.
- Q: Which fields and defaults should Namespace status expose? → A: Use `observedGeneration`, `lastAppliedRevision`, and `conditions` with defaults `0`, null/empty, and `[]`; do not add a Namespace-specific `resolved` field.
- Q: Which metadata fields should Namespace expose? → A: Use a dedicated `NamespaceMetadata` type with the shared metadata fields, including labels, annotations, owner references, and finalizers, while omitting `metadata.namespace`.
- Q: What is the canonical Namespace spec field for its human-friendly label? → A: Use `spec.title`.
- Q: Should Namespace define repository and push-policy defaults as part of this schema? → A: Yes; include `repositoryDefaults` and `pushPolicyDefaults` in `NamespaceSpec`.

## User Scenarios & Testing *(mandatory)*

### User Story 1 - A caller can read a namespace's author-supplied configuration separately from its system-computed state (Priority: P1)

Today a namespace is a single flat record: identifier, title (formerly referred to as "display name"), tier, and audit timestamps are all mixed together with no distinction between "what the owner asked for" and "what the system has determined or computed about it." A caller (an administrator, the controller-manager, or another internal service) needs to read a namespace and clearly tell apart the fields the owner controls from the fields the system controls, the same way they already can for CategoryTaxonomy, Collection, and Product.

**Why this priority**: This is the foundational contract every other piece of Namespace lifecycle work in this initiative depends on. GH#172 (spec writes, status updates, concurrency), GH#173 (validation/admission matrix), and GH#174 (watch/resume) all need a stable, agreed-upon shape for "the Namespace resource" before they can define behavior against it. Without this, each of those specs would have to invent or guess at the shape themselves.

**Independent Test**: Can be fully tested by reading an existing namespace through the API and confirming the response uses the standard resource envelope, exposes the human identifier and shared metadata through `metadata`, separates `spec.title` and `spec.tier` from system-managed metadata, and exposes system-computed status conditions, without requiring any write-path or watch-path behavior to exist yet.

**Acceptance Scenarios**:

1. **Given** an existing namespace, **When** a caller reads it, **Then** the response uses `apiVersion`, `kind`, `metadata`, `spec`, and `status`, with the identifier plus shared metadata fields under `metadata`, title and tier under `spec`, and system-computed observations under `status`.
2. **Given** an existing namespace that has never been touched by any status-writing process, **When** a caller reads its status, **Then** `observedGeneration` is `0`, `lastAppliedRevision` is null, and `conditions` is an empty list rather than status being absent or causing an error.
3. **Given** two different namespaces, **When** a caller reads both, **Then** each exposes its own per-resource version marker and generation counter; both may initially be `"1"`/`1` without implying shared identity or cross-resource coupling.
4. **Given** a namespace that supplies repository or push-policy defaults, **When** a caller reads its spec, **Then** those defaults are represented as structured `repositoryDefaults` and `pushPolicyDefaults` fields rather than an untyped map.

---

### User Story 2 - The Namespace resource carries the same identity and versioning contract as other catalog resources (Priority: P1)

A controller author or API consumer who already knows how to work with CategoryTaxonomy, Collection, or Product needs Namespace to expose the same stable identity and versioning field contract: an immutable system-generated UID, an opaque resource version, and a generation counter whose advancement semantics are defined for GH#172. This consistency lets downstream concurrency and watch specifications avoid inventing a second Namespace-specific identity model.

**Why this priority**: Equal in importance to User Story 1 — this is the specific part of "the contract" that unblocks concurrency-safe writes (GH#172) and resumable watch (GH#174). A schema that exposes owner-supplied vs. system-computed fields but lacks a consistent version/generation contract would still leave those two downstream specs blocked.

**Independent Test**: Can be fully tested by reading a namespace's identity/versioning fields and confirming their names, GraphQL types, nullability, initial values, and documented future advancement semantics match CategoryTaxonomy, without requiring write or watch behavior.

**Acceptance Scenarios**:

1. **Given** a namespace, **When** a caller inspects its identity fields, **Then** it exposes a system-generated unique identifier that is distinct from, and does not change when, its human-readable identifier is used for lookups.
2. **Given** a namespace, **When** a caller inspects its versioning fields, **Then** it exposes `resourceVersion: "1"` and `generation: 1` using the same field names and types as CategoryTaxonomy.
3. **Given** the schema defined by this specification, **When** it is compared against the existing CategoryTaxonomy and Collection schemas, **Then** the shape and naming of the shared identity/versioning/status-condition fields match, with no Namespace-specific renaming or reinvention of equivalent concepts.

---

### User Story 3 - Alpha consumers migrate to the preferred declarative Namespace fields (Priority: P3)

Today's namespace consumers (the admin console, the controller-manager, and other internal services) read namespaces through the current flat shape. The declarative fields become the preferred contract immediately, while the existing flat GraphQL fields remain temporarily available and deprecated so consumers can migrate without blocking delivery of User Stories 1 and 2.

**Why this priority**: Consumer migration is lower priority than establishing the correct long-term resource contract. The constitution requires deprecation before removal, so migration can proceed after the additive schema lands; removal is reserved for a future major GraphQL API release.

**Independent Test**: Can be tested by updating affected in-repository Namespace consumers and contract tests to select the declarative fields while schema introspection confirms the previous flat fields remain present and carry deprecation metadata.

**Acceptance Scenarios**:

1. **Given** an existing caller using the flat Namespace fields, **When** the declarative schema is introduced, **Then** the caller continues to work while receiving GraphQL deprecation metadata directing it to the preferred fields.
2. **Given** an in-repository caller is migrated, **When** its GraphQL selections are reviewed, **Then** it uses `metadata`, `spec`, and `status` rather than deprecated flat fields.
3. **Given** a future release removes the deprecated flat fields, **When** that removal is planned, **Then** it is delivered only as part of a major GraphQL API version change.

---

### Edge Cases

- What happens to a namespace created before this schema existed, once the schema is introduced? (Its status must reflect a well-defined initial state — not an error, not a missing/null status — even though no status was ever written for it under the old shape.)
- What happens when a caller asks for a field that only exists in the new schema (e.g., generation counter) on a namespace that predates this schema? (It must still be present with a well-defined initial value; it must never be silently absent.)
- What happens when status has not yet observed the current spec generation? (`status.observedGeneration` remains lower than `metadata.generation`, making the lag observable without duplicating spec fields into status.)
- How does the schema represent a namespace that exists but currently has nothing meaningful to report in status (e.g., just created, no controller has touched it, no conditions yet apply)? (`observedGeneration: 0`, `lastAppliedRevision: null`, and `conditions: []` are valid and distinct from missing status.)
- What happens when `repositoryDefaults` or `pushPolicyDefaults` is omitted or only partially specified? (Each group and each nested override is optional; unresolved values come from trusted operator defaults or Repository-level resolution defined under GH#249.)
- What happens when a Namespace default attempts to relax an administrator-controlled global ceiling or disable required validation/admission? (The schema can represent the request, but GH#173 validation/admission MUST reject it; Namespace policy may tighten but not weaken operator safety boundaries.)

## Requirements *(mandatory)*

### Functional Requirements

- **FR-001**: The Namespace resource contract MUST use the standard `apiVersion`, `kind`, `metadata`, `spec`, and `status` top-level envelope used by existing Git-backed catalog resources.
- **FR-002**: The Namespace resource contract MUST expose a system-generated unique identifier for each namespace that is independent of, and never changes when, the namespace's human-readable identifier is used.
- **FR-003**: The Namespace resource contract MUST expose `metadata.resourceVersion` as the same non-null opaque numeric-string type used by CategoryTaxonomy. Existing rows MUST initially return `"1"`; advancement on successful resource changes is defined by GH#172.
- **FR-004**: The Namespace resource contract MUST expose `metadata.generation` using the same integer type and declared semantics as CategoryTaxonomy. Existing rows MUST initially return `1`; advancement only for owner-controlled changes is defined by GH#172.
- **FR-005**: The Namespace resource contract MUST expose `status.observedGeneration`, `status.lastAppliedRevision`, and `status.conditions`; the detailed shared condition contract is defined by FR-015. Namespace MUST NOT add a kind-specific `status.resolved` field.
- **FR-006**: Every namespace, including one created before this schema, MUST return a non-null status whose initial/default values are `observedGeneration: 0`, `lastAppliedRevision: null`, and `conditions: []`.
- **FR-007**: The Namespace resource contract MUST NOT require or assume the existence of a Namespace controller, reconciler, or watch mechanism — those are out of scope for this specification (tracked separately in GH#174) and the schema must be well-defined on its own before any of them exist.
- **FR-008**: This schema-only specification MUST preserve the existing create/delete mutation behavior and storage path while changing their returned Namespace representation additively. Git-delegating write behavior is a downstream requirement of GH#172.
- **FR-009**: The Namespace resource contract MUST retain the previous flat GraphQL output fields as deprecated compatibility fields mapped from the same datastore row. In-repository consumers MUST migrate to the declarative fields, and removal MUST occur only in a future major GraphQL API release.
- **FR-010**: The Namespace resource contract MUST represent the existing human-readable identifier as `metadata.name`, MUST omit `metadata.namespace`, and MUST expose `spec.title`, `spec.tier`, `spec.repositoryDefaults`, and `spec.pushPolicyDefaults`. `title` is optional, `tier` is required, and both default groups are optional.
- **FR-011**: Namespace MUST use a dedicated `NamespaceMetadata` type that is field-equivalent to the shared `ObjectMeta` contract for `name`, `labels`, `annotations`, `uid`, `resourceVersion`, `generation`, `creationTimestamp`, `revision`, `ownerReferences`, and `finalizers`, while intentionally omitting `metadata.namespace`.
- **FR-012**: Namespace manifests MUST use `apiVersion: gitstore.dev/v1beta1` and `kind: Namespace`.
- **FR-013**: `repositoryDefaults` MUST be a typed partial-default object supporting `visibility` and `defaultBranch`. `pushPolicyDefaults` MUST be a typed partial-default object supporting `maxPackSizeBytes`, `maxFileSizeBytes`, per-hook `enabled` settings for `preReceive`, `update`, `postReceive`, `procReceive`, `postUpdate`, and `referenceTransaction`, plus `schemaValidation.phase`, `schemaValidation.timeoutSeconds`, `admissionControl.phase`, and `admissionControl.branchPattern`.
- **FR-014**: The contract MUST classify Namespace repository and push-policy defaults as author-controlled desired state. Their persistence, generation advancement, validation, merge precedence, effective-policy resolution, and enforcement are defined by GH#172, GH#173, and GH#249.
- **FR-015**: Each Namespace condition MUST use the shared `type`, `status`, `observedGeneration`, `lastTransitionTime`, `reason`, and `message` fields. The contract documentation MUST define `Ready`, `AdmissionAccepted`, and `DeletionBlocked` as the initial Namespace condition vocabulary; the shared string-valued condition type remains open for future additions, and no condition is required in the initial empty set.
- **FR-016**: The contract and documentation MUST classify `apiVersion`, `kind`, `metadata.name`, `metadata.uid`, and `metadata.creationTimestamp` as immutable; labels, annotations, and `spec` fields as author-controlled; and all remaining metadata/status fields as system-managed. Enforcement is defined by GH#172 and GH#173.
- **FR-017**: Author-authored Namespace manifests MUST omit system-managed metadata values and `status`; API reads MUST return the fully hydrated resource including those fields.

### Key Entities

- **Namespace resource**: The declarative representation of a namespace using the standard `apiVersion`, `kind`, `metadata`, `spec`, and `status` envelope. It replaces the implicit flat-record model with the same resource contract used by CategoryTaxonomy, Collection, and Product.
- **Owner-supplied configuration**: The author-controlled Namespace fields: `metadata.name`, `metadata.labels`, `metadata.annotations`, `spec.title`, `spec.tier`, `spec.repositoryDefaults`, and `spec.pushPolicyDefaults`. Changing any of these advances the generation counter.
- **System-managed identity/versioning metadata**: The shared `ObjectMeta` fields controlled by the system: `uid`, `resourceVersion`, `generation`, `creationTimestamp`, `revision`, `ownerReferences`, and `finalizers`. Namespace omits only the owning `namespace` field.
- **System-computed status**: The system-owned `observedGeneration`, `lastAppliedRevision`, and shared condition set. Namespace has no kind-specific `resolved` payload.

## Success Criteria *(mandatory)*

### Measurable Outcomes

- **SC-001**: 100% of existing namespaces, including those created before this schema existed, return non-null status with `observedGeneration: 0`, `lastAppliedRevision: null`, and `conditions: []` when no status has previously been written.
- **SC-002**: Automated schema/data-model contract tests verify that every shared Namespace identity/versioning/status field has the same name, GraphQL type, nullability, and initial-value convention as the CategoryTaxonomy equivalent, except for the intentional omission of `metadata.namespace` and `status.resolved`.
- **SC-003**: 100% of affected in-repository Namespace consumers and contract tests select the declarative fields, while schema introspection confirms every previous flat output field remains available with a deprecation reason.
- **SC-005**: The Namespace schema exposes typed fields for 100% of the repository-default and push-policy-default settings shown in GH#170's canonical Namespace manifest, with no untyped policy map.
- **SC-006**: The implementation documentation at `docs/namespace/namespace-spec.md` includes a field mutability/ownership matrix and copy-pasteable Namespace create/update manifests that pass an automated documentation-contract validation test.

## Assumptions

- This specification defines the **schema/shape only** — GH#172 defines how Git-driven writes to the owner-supplied configuration flow into system-managed metadata and status, including GraphQL mutation delegation required by existing tooling under GH#170. Validation/admission rules (GH#173) and watch/resume semantics (GH#174) remain separate specifications that build on this schema.
- GitStore is Alpha software, but the constitution's public-interface versioning rule still applies: the declarative fields are additive, the flat fields are deprecated, and removal is deferred to a future major GraphQL API release.
- Because a namespace is the top-level tenant boundary and does not itself live "inside" another namespace, its system-managed identity/versioning metadata is scoped accordingly (no owning-namespace reference), distinguishing it from the metadata shape used by namespace-scoped resources like CategoryTaxonomy, Collection, and Product.
- No Namespace controller or reconciler exists yet and none is introduced by this spec; this specification defines only the initial/default status shape, while GH#172 owns status-writing behavior for Git-driven Namespace changes.
- The `Repository` resource's equivalent declarative schema, policy-override merge behavior, effective-policy resolution, and Git-delegating GraphQL mutations are tracked separately under GH#249 and are not defined by this specification, even though Namespace and Repository retain GraphQL mutation entry points for dependent tooling.
