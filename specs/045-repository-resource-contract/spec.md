# Feature Specification: Repository Resource Contract: Kubernetes-style Declarative Markdown Schema

**Feature Branch**: `045-repository-resource-contract`

**Created**: 2026-08-16

**Status**: Closed

**Input**: User description: "Repository Resource Contract: Kubernetes-style Declarative Markdown Schema (GH#249). Define the declarative .spec/.status schema for the Repository resource, following the same Kubernetes-style contract pattern already established for other catalog resources (CategoryTaxonomy, Collection, Product) and mirroring the parallel Namespace schema work (GH#171), so that Repository gains the same owner-supplied-vs-system-computed separation, identity/versioning contract, and status conditions."

## User Scenarios & Testing *(mandatory)*

### User Story 1 - A caller can read declarative Repository state without breaking existing flat reads (Priority: P1)

Today a repository is a single flat record: its name, default branch, push-policy limits, storage class, storage path, and audit timestamps are mixed together with no declarative resource envelope or distinction between desired and observed state. A caller (an administrator, the controller-manager, or another internal service) needs the same `apiVersion` / `kind` / `metadata` / `spec` / `status` contract established for catalog resources and Namespace, while existing flat consumers migrate through explicit deprecations.

**Why this priority**: This is the foundational contract this specification exists to deliver. Without it, no future Repository lifecycle spec (validation, watch/resume, reconciliation) has a stable, agreed-upon shape to build against.

**Independent Test**: After the shared contract foundation is present, can be fully tested by reading an existing repository by ID and namespace/name and by listing repositories. Every path returns the same constant `apiVersion`/`kind`, metadata/spec/status envelope, projected configuration, and non-null observed/resolved status. The pre-contract flat fields remain selectable with their original values, schema introspection marks every duplicate field deprecated, and Relay `id` remains non-deprecated.

**Acceptance Scenarios**:

1. **Given** an existing repository, **When** a caller reads it, **Then** the response exposes `apiVersion` and `kind`, author-controlled `metadata.name`, the declarative configuration projection (persisted default branch and limits plus reserved defaults), system-managed identity/versioning fields, and system-computed/derived status as distinguishable parts of one envelope.
2. **Given** an existing repository that has never been touched by any status-writing process, **When** a caller reads its status, **Then** the status is present but reflects an initial/default state rather than being absent or causing an error.
3. **Given** two repositories in the same namespace, **When** a caller reads both, **Then** each exposes its own independent version marker and generation counter, and renaming or transferring one does not change the other's identity fields.
4. **Given** a caller still selects the pre-contract flat Repository fields, **When** the schema is introspected, **Then** each duplicate field is marked deprecated with guidance to its declarative replacement.
5. **Given** a caller reads both the declarative and pre-contract fields, **When** the repository is returned by any read path, **Then** both projections contain equivalent values and Relay `id` is not deprecated.

---

### User Story 2 - The Repository resource carries the same identity and versioning contract as other catalog resources (Priority: P1)

A controller author or API consumer who already knows how to work with CategoryTaxonomy, Collection, or Product — or with the parallel Namespace schema from GH#171 — needs Repository to expose the same kind of stable identity (a system-generated unique ID that never changes across rename or transfer), a version marker that changes whenever the resource is modified, and a generation counter that changes only when the owner's configuration changes — not when the system updates derived fields like storage path or status.

**Why this priority**: Equal in importance to User Story 1 — this is the specific part of "the contract" that any future concurrency-safe write semantics or resumable watch semantics for Repository would depend on, mirroring why the same guarantee matters for Namespace (GH#171/GH#172/GH#174).

**Independent Test**: After the shared contract foundation is present, can be fully tested independently of User Story 1 by creating, renaming, and transferring a repository through existing mutations. The test verifies stable Relay ID/UID, canonical initial counters, the required counter transitions, preserved status, and unchanged success/error behavior.

**Acceptance Scenarios**:

1. **Given** a repository, **When** a caller inspects its identity fields, **Then** it exposes a system-generated unique identifier that is distinct from, and does not change when, the repository is renamed or transferred to a different namespace.
2. **Given** a repository, **When** a caller inspects its versioning fields, **Then** it exposes a version marker and a generation counter using the same field names, types, and semantics as the equivalent fields already defined for CategoryTaxonomy and for Namespace (GH#171).
3. **Given** a repository is renamed, **When** a caller inspects its generation counter afterward, **Then** the counter has advanced because `metadata.name` is author-controlled; **Given** the system alone recomputes a derived field like storage path, **When** a caller inspects the generation counter afterward, **Then** it has not advanced from that alone.
4. **Given** the current `createRepository`, `renameRepository`, `transferRepository`, and `deleteRepository` operations, **When** the declarative contract is introduced, **Then** those operations continue to succeed and fail under exactly the same conditions as before.

---

### Edge Cases

- What happens to a repository created before this schema existed, once the schema is introduced? (Its status must reflect a well-defined initial state — not an error, not a missing/null status — even though no status was ever written for it under the old shape.)
- What happens when a caller asks for a field that only exists in the new schema (e.g., generation counter) on a repository that predates this schema? (It must still be present with a well-defined initial value; it must never be silently absent.)
- What happens to identity/versioning fields when a repository is transferred to a different namespace? (The unique identifier and version/generation history must be preserved across the transfer — a transfer is not a delete-and-recreate.)
- How does the schema represent a repository that exists but currently has nothing meaningful to report in status (e.g., just created, no controller has touched it, no conditions yet apply)? (An empty/default condition set is valid and distinct from a missing status.)
- What happens to the derived storage path field if it is ever inspected during or immediately after a rename/transfer? (It must remain accurate and consistent with the repository's actual storage location, since storage path is system-derived and not something this schema allows an owner to directly set.)

## Requirements *(mandatory)*

### Functional Requirements

- **FR-001**: The Repository resource contract MUST use the Kubernetes-style `apiVersion` / `kind` / `metadata` / `spec` / `status` envelope. The author-controlled name MUST be `metadata.name`; desired configuration MUST be under `spec`; system-computed/derived values MUST be under `status`.
- **FR-002**: The Repository resource contract MUST expose a system-generated unique identifier for each repository that is independent of, and never changes when, the repository is renamed or transferred to a different namespace.
- **FR-003**: The Repository resource contract MUST expose a version marker that changes for every modification performed by the existing Repository lifecycle operations covered by this feature. Create starts at `"1"`; rename increments it; transfer increments it without resetting identity or history. The shared system-transition helper MUST also increment resourceVersion without incrementing generation so future status/system writers have one tested transition path.
- **FR-004**: The Repository resource contract MUST expose a generation counter that starts at `1` and changes only when currently supported author-controlled metadata changes. In this feature, rename of `metadata.name` increments generation, while transfer and system/status transitions do not. Future write APIs for `defaultBranch`, `visibility`, or push policy MUST use the same spec-transition helper and increment generation.
- **FR-005**: The Repository resource contract MUST expose a non-null system-computed status containing `observedGeneration`, `lastAppliedRevision`, conditions using the shared `Condition` shape, and a non-null resolved group for storage path and storage class.
- **FR-006**: The Repository resource contract MUST define a well-defined initial/default status (including an empty or baseline condition set) for every repository, including repositories that existed before this schema was introduced, so that status is never absent or erroring.
- **FR-007**: The Repository resource contract MUST NOT require or assume the existence of a Repository controller, reconciler, or watch mechanism — those are out of scope for this specification, and the schema must be well-defined on its own before any of them exist.
- **FR-008**: The Repository resource contract MUST NOT change the behavior, inputs, or outputs of the existing repository create, rename, transfer, or delete operations; only the read/representation shape defined by this specification is in scope.
- **FR-009**: Existing callers reading repositories through the fields available before this specification MUST continue to receive the same information in the same shape. Duplicate legacy output fields MUST be marked deprecated with guidance to the declarative replacement and MUST NOT be removed before a future major GraphQL API release.
- **FR-010**: `RepositorySpec` MUST define `defaultBranch`, `visibility`, and a non-null Repository push-policy object using the same policy vocabulary as Namespace defaults from GH#171 / PR #345. `defaultBranch` and maximum pack/file sizes project existing persisted configuration. Because this feature adds no new Repository update input or persistence fields, visibility MUST project `PRIVATE` and receive-pack hook, schema-validation, and admission-control override groups MUST project null until a future write/persistence feature is introduced. Maximum pack/file sizes MUST be non-null signed 64-bit values because explicit zero is the existing unlimited sentinel.
- **FR-011**: The Repository resource contract's identity/versioning metadata MUST identify which namespace owns the repository, using the same owning-namespace representation already used by other namespace-scoped catalog resources (CategoryTaxonomy, Collection, Product), rather than inventing a Repository-specific alternative.
- **FR-012**: `apiVersion` MUST be the non-null constant `gitstore.dev/v1beta1`, and `kind` MUST be the non-null constant `Repository`.
- **FR-013**: Existing `name`, `namespace`, `defaultBranch`, `storageClass`, `storagePath`, `createdAt`, `createdBy`, `updatedAt`, and `updatedBy` GraphQL output fields MUST remain available with explicit `@deprecated` reasons. Relay `id` MUST remain non-deprecated.
- **FR-014**: Existing `MaxPackSizeBytes` and `MaxFileSizeBytes` repository values MUST project through `spec.pushPolicy`. All repositories MUST project `PRIVATE` visibility and null extended override groups until their persistence semantics are implemented.

### Key Entities

- **Repository resource**: The declarative representation of a repository expressed as the standard resource envelope: contract identity (`apiVersion`, `kind`), mixed author/system metadata, a declarative spec projection, and system-computed status. It replaces the implicit "everything is one flat record" model while preserving deprecated flat projections during migration.
- **Declarative metadata and specification**: `metadata.name` is currently author-controlled. `spec.defaultBranch` and maximum-size policy fields project existing persisted configuration, while visibility and extended policy groups are reserved deterministic defaults in this feature. Rename advances generation; future write APIs for any spec field must use the shared spec-transition helper.
- **System-managed identity/versioning metadata**: The part of the Repository resource the system alone controls — unique identifier, owning namespace reference, version marker, generation counter, creation record. Never set or changed directly by an owner.
- **System-computed/derived status**: The part of the Repository resource that reflects what the system has determined, derived, or observed — observed generation, last applied revision, conditions, and resolved storage path/class.

### Initial Condition Vocabulary

- This feature introduces no condition-producing controller, reconciler, mutation, or background writer.
- `status.conditions` therefore defaults to an empty list and no Repository-specific condition `type` values are emitted by this feature.
- Any future condition-producing feature MUST define its condition types, owning writer, transition rules, reasons, and observed-generation behavior before emitting them, while continuing to use the shared `Condition` GraphQL shape.

## Success Criteria *(mandatory)*

### Measurable Outcomes

- **SC-001**: 100% of existing repositories, including those created before this schema existed, return a well-defined status (never null, missing, or erroring) when read under the new schema.
- **SC-002**: 100% of the identity/versioning field names, types, and change semantics defined for Repository match the equivalent fields already defined for CategoryTaxonomy and for Namespace (GH#171), with zero Repository-specific reinvention of an equivalent concept.
- **SC-003**: 100% of existing repository create, rename, transfer, delete, and read operations continue to succeed or fail under exactly the same conditions as before this schema is introduced, with zero regressions in existing consumer behavior.
- **SC-004**: 100% of repository transfers preserve the repository's unique identifier and version/generation history across the namespace change, with zero instances of an identifier changing or a version/generation counter resetting due to a transfer.
- **SC-005**: GraphQL schema introspection confirms that 100% of Repository identity/versioning metadata fields use the same names and GraphQL types as the shared `ObjectMeta` contract and that Repository status reuses the shared `Condition` type, with no Repository-specific duplicate metadata or condition type.
- **SC-006**: 100% of duplicate legacy Repository output fields are marked deprecated with a replacement or legacy-audit explanation, while Relay `id` remains non-deprecated.
- **SC-007**: 100% of existing maximum pack/file size values are visible through the non-null `spec.pushPolicy`, including explicit zero values.

## Assumptions

- This specification defines the **schema/read projection and version-field maintenance on existing synchronous operations**. It does not introduce Git-driven declarative writes, validation/admission rules for the new configuration fields, a status mutation API, or watch/resume semantics. Those remain separate future work, mirroring how GH#172/#173/#174 build on GH#171 for Namespace.
- The new schema is introduced **additively alongside** the existing repository representation, not as a breaking replacement of it — existing consumers are unaffected until a deliberate future migration spec. This matches the same low-risk default chosen for the parallel Namespace schema work (GH#171).
- Shared `Long`, `RepositoryVisibility`, receive-pack hook, schema-validation, and admission-control GraphQL types follow PR #345. This feature reuses those common types rather than defining Repository-specific duplicates.
- PR #345 is an implementation prerequisite pinned for planning purposes at head commit `fefadbea951959c42a982d5e0d7824dbf175209c`. Implementation verifies that revision or a merged descendant containing the same shared definitions; it does not merge or rebase another branch as a task side effect.
- Because a repository is a namespace-scoped resource, its system-managed identity/versioning metadata reuses the same owning-namespace representation already defined for other namespace-scoped catalog resources — no new cluster-scoped metadata variant is needed here (unlike Namespace itself in GH#171, which required one because it does not live inside another namespace).
- Storage class remains a system-derived/reserved field under `status.resolved`, not an owner-configurable spec field, since no existing operation allows a caller to set it today.
- No Repository controller or reconciler exists yet and none is introduced by this spec. Existing operations initialize or preserve the default status projection; no condition-producing reconciliation logic is added.
- `visibility` and the extended hook/schema/admission override groups are reserved read-contract fields in this feature. They expose deterministic defaults only and are not described as currently writable owner input.
- The `Namespace` resource's equivalent declarative schema (GH#171) is tracked as a separate, independent specification and is not defined by this one, even though both are sub-issues of the same parent initiative (GH#40) and are intentionally kept consistent with each other.
