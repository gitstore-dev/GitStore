# Feature Specification: Repository Resource Contract: Kubernetes-style Declarative Markdown Schema

**Feature Branch**: `045-repository-resource-contract`
**Created**: 2026-08-16
**Status**: Draft
**Input**: User description: "Repository Resource Contract: Kubernetes-style Declarative Markdown Schema (GH#249). Define the declarative .spec/.status schema for the Repository resource, following the same Kubernetes-style contract pattern already established for other catalog resources (CategoryTaxonomy, Collection, Product) and mirroring the parallel Namespace schema work (GH#171), so that Repository gains the same owner-supplied-vs-system-computed separation, identity/versioning contract, and status conditions."

## User Scenarios & Testing *(mandatory)*

### User Story 1 - A caller can read a repository's author-supplied configuration separately from its system-computed state (Priority: P1)

Today a repository is a single flat record: its name, default branch, storage class, storage path, and audit timestamps are all mixed together with no distinction between "what the owner asked for" and "what the system has determined, derived, or computed about it." A caller (an administrator, the controller-manager, or another internal service) needs to read a repository and clearly tell apart the fields the owner controls from the fields the system controls, the same way they already can for CategoryTaxonomy, Collection, and Product, and the same way GH#171 establishes for Namespace.

**Why this priority**: This is the foundational contract this specification exists to deliver. Without it, no future Repository lifecycle spec (validation, watch/resume, reconciliation) has a stable, agreed-upon shape to build against.

**Independent Test**: Can be fully tested by reading an existing repository through the API and confirming the response separates owner-supplied fields (name, default branch) as a distinct group from system-managed identity/versioning fields (unique ID, version marker, generation counter, creation record) and from system-computed/derived fields (storage path, storage class, status conditions), without requiring any write-path or watch-path behavior to exist yet.

**Acceptance Scenarios**:

1. **Given** an existing repository, **When** a caller reads it, **Then** the response exposes the owner-supplied configuration (name, default branch) as a distinct group from the system-managed identity/versioning fields and from the system-computed/derived status.
2. **Given** an existing repository that has never been touched by any status-writing process, **When** a caller reads its status, **Then** the status is present but reflects an initial/default state rather than being absent or causing an error.
3. **Given** two repositories in the same namespace, **When** a caller reads both, **Then** each exposes its own independent version marker and generation counter, and renaming or transferring one does not change the other's identity fields.

---

### User Story 2 - The Repository resource carries the same identity and versioning contract as other catalog resources (Priority: P1)

A controller author or API consumer who already knows how to work with CategoryTaxonomy, Collection, or Product — or with the parallel Namespace schema from GH#171 — needs Repository to expose the same kind of stable identity (a system-generated unique ID that never changes across rename or transfer), a version marker that changes whenever the resource is modified, and a generation counter that changes only when the owner's configuration changes — not when the system updates derived fields like storage path or status.

**Why this priority**: Equal in importance to User Story 1 — this is the specific part of "the contract" that any future concurrency-safe write semantics or resumable watch semantics for Repository would depend on, mirroring why the same guarantee matters for Namespace (GH#171/GH#172/GH#174).

**Independent Test**: Can be fully tested by reading a repository's identity/versioning fields and confirming they follow the same naming, type, and change semantics already documented and implemented for another catalog resource (e.g., CategoryTaxonomy), and matching the shape defined for Namespace by GH#171, without needing any actual write or watch behavior implemented yet.

**Acceptance Scenarios**:

1. **Given** a repository, **When** a caller inspects its identity fields, **Then** it exposes a system-generated unique identifier that is distinct from, and does not change when, the repository is renamed or transferred to a different namespace.
2. **Given** a repository, **When** a caller inspects its versioning fields, **Then** it exposes a version marker and a generation counter using the same field names, types, and semantics as the equivalent fields already defined for CategoryTaxonomy and for Namespace (GH#171).
3. **Given** a repository is renamed, **When** a caller inspects its generation counter afterward, **Then** the counter has advanced, because name is owner-supplied configuration; **Given** the system alone recomputes a derived field like storage path, **When** a caller inspects the generation counter afterward, **Then** it has not advanced from that alone.

---

### User Story 3 - Existing repository consumers continue to work unaffected while the new schema is introduced (Priority: P2)

Today's repository consumers (the admin console, the controller-manager, and any other internal service) already read repositories through the current flat shape. Introducing the new declarative schema must not silently break those consumers or force an uncoordinated simultaneous migration across every consumer the moment this schema lands.

**Why this priority**: Lower urgency than User Stories 1 and 2 because it is a safety/compatibility constraint rather than new capability, but it must be honored by whatever this specification produces, matching the same constraint applied to the parallel Namespace schema work (GH#171).

**Independent Test**: Can be fully tested by exercising every existing repository read/create/rename/transfer/delete operation after this schema is introduced and confirming they behave exactly as before, with no observable change to existing response shapes or error behavior for callers that do not opt into the new schema.

**Acceptance Scenarios**:

1. **Given** an existing caller reading repositories through today's current fields, **When** this specification's schema is introduced, **Then** that caller's existing reads continue to return the same information in the same shape as before.
2. **Given** the current `createRepository`, `renameRepository`, `transferRepository`, and `deleteRepository` operations, **When** this specification's schema is introduced, **Then** those operations continue to succeed and fail under exactly the same conditions as before.

---

### Edge Cases

- What happens to a repository created before this schema existed, once the schema is introduced? (Its status must reflect a well-defined initial state — not an error, not a missing/null status — even though no status was ever written for it under the old shape.)
- What happens when a caller asks for a field that only exists in the new schema (e.g., generation counter) on a repository that predates this schema? (It must still be present with a well-defined initial value; it must never be silently absent.)
- What happens to identity/versioning fields when a repository is transferred to a different namespace? (The unique identifier and version/generation history must be preserved across the transfer — a transfer is not a delete-and-recreate.)
- How does the schema represent a repository that exists but currently has nothing meaningful to report in status (e.g., just created, no controller has touched it, no conditions yet apply)? (An empty/default condition set is valid and distinct from a missing status.)
- What happens to the derived storage path field if it is ever inspected during or immediately after a rename/transfer? (It must remain accurate and consistent with the repository's actual storage location, since storage path is system-derived and not something this schema allows an owner to directly set.)

## Requirements *(mandatory)*

### Functional Requirements

- **FR-001**: The Repository resource contract MUST separate owner-supplied configuration (name, default branch) from system-managed identity/versioning fields and from system-computed/derived status (storage path, storage class, conditions), as three distinguishable groups.
- **FR-002**: The Repository resource contract MUST expose a system-generated unique identifier for each repository that is independent of, and never changes when, the repository is renamed or transferred to a different namespace.
- **FR-003**: The Repository resource contract MUST expose a version marker that changes whenever any part of the repository resource is modified, following the same semantics already used by CategoryTaxonomy's equivalent field and matching the shape defined for Namespace (GH#171).
- **FR-004**: The Repository resource contract MUST expose a generation counter that changes only when the owner-supplied configuration changes (e.g., rename), not when a system-derived field (e.g., storage path) or status changes, following the same semantics already used by CategoryTaxonomy's equivalent field.
- **FR-005**: The Repository resource contract MUST expose a system-computed status that includes a set of named conditions, following the same shape already used by CategoryTaxonomy's and Collection's status conditions.
- **FR-006**: The Repository resource contract MUST define a well-defined initial/default status (including an empty or baseline condition set) for every repository, including repositories that existed before this schema was introduced, so that status is never absent or erroring.
- **FR-007**: The Repository resource contract MUST NOT require or assume the existence of a Repository controller, reconciler, or watch mechanism — those are out of scope for this specification, and the schema must be well-defined on its own before any of them exist.
- **FR-008**: The Repository resource contract MUST NOT change the behavior, inputs, or outputs of the existing repository create, rename, transfer, or delete operations; only the read/representation shape defined by this specification is in scope.
- **FR-009**: Existing callers reading repositories through the fields available before this specification MUST continue to receive the same information in the same shape, unaffected by the introduction of the new schema.
- **FR-010**: The Repository resource contract's owner-supplied configuration group MUST retain exactly the fields already supported today (name, default branch) without adding, removing, or renaming any of them as part of this specification.
- **FR-011**: The Repository resource contract's identity/versioning metadata MUST identify which namespace owns the repository, using the same owning-namespace representation already used by other namespace-scoped catalog resources (CategoryTaxonomy, Collection, Product), rather than inventing a Repository-specific alternative.

### Key Entities

- **Repository resource**: The declarative representation of a repository, now expressed as three distinguishable parts: owner-supplied configuration, system-managed identity/versioning metadata (including its owning namespace), and system-computed/derived status. Replaces the implicit "everything is one flat record" model with the same spec/status split already used by CategoryTaxonomy, Collection, Product, and the parallel Namespace schema (GH#171).
- **Owner-supplied configuration**: The part of the Repository resource that an owner controls directly — name, default branch. Changing either advances the generation counter.
- **System-managed identity/versioning metadata**: The part of the Repository resource the system alone controls — unique identifier, owning namespace reference, version marker, generation counter, creation record. Never set or changed directly by an owner.
- **System-computed/derived status**: The part of the Repository resource that reflects what the system has determined, derived, or observed about the repository — storage path, storage class, and status conditions — expressed using the existing condition shape used elsewhere in the catalog.

## Success Criteria *(mandatory)*

### Measurable Outcomes

- **SC-001**: 100% of existing repositories, including those created before this schema existed, return a well-defined status (never null, missing, or erroring) when read under the new schema.
- **SC-002**: 100% of the identity/versioning field names, types, and change semantics defined for Repository match the equivalent fields already defined for CategoryTaxonomy and for Namespace (GH#171), with zero Repository-specific reinvention of an equivalent concept.
- **SC-003**: 100% of existing repository create, rename, transfer, delete, and read operations continue to succeed or fail under exactly the same conditions as before this schema is introduced, with zero regressions in existing consumer behavior.
- **SC-004**: 100% of repository transfers preserve the repository's unique identifier and version/generation history across the namespace change, with zero instances of an identifier changing or a version/generation counter resetting due to a transfer.
- **SC-005**: A controller or API author unfamiliar with this specific spec, but familiar with the existing CategoryTaxonomy contract or the parallel Namespace schema (GH#171), can correctly predict the shape of the new Repository schema's identity/versioning/status fields without consulting additional documentation.

## Assumptions

- This specification defines the **schema/shape only** — it does not define how writes to the owner-supplied configuration flow into the system-managed metadata or status, does not define validation or admission rules beyond what already exists today, and does not define watch/resume semantics for Repository. Those, if ever needed, are separate future specifications that would build on the schema this one establishes, mirroring how GH#172/#173/#174 build on GH#171 for Namespace.
- The new schema is introduced **additively alongside** the existing repository representation, not as a breaking replacement of it — existing consumers are unaffected until a deliberate future migration spec. This matches the same low-risk default chosen for the parallel Namespace schema work (GH#171).
- Because a repository is a namespace-scoped resource, its system-managed identity/versioning metadata reuses the same owning-namespace representation already defined for other namespace-scoped catalog resources — no new cluster-scoped metadata variant is needed here (unlike Namespace itself in GH#171, which required one because it does not live inside another namespace).
- Storage class remains a system-derived/reserved field, not an owner-configurable spec field, since no existing operation allows a caller to set it today; this specification does not change that.
- No Repository controller or reconciler exists yet and none is introduced by this spec; status conditions, where populated, are written synchronously by the existing create/rename/transfer/delete code paths rather than by an asynchronous reconciliation loop. Asynchronous reconciliation, if ever needed, is a separate future concern.
- The `Namespace` resource's equivalent declarative schema (GH#171) is tracked as a separate, independent specification and is not defined by this one, even though both are sub-issues of the same parent initiative (GH#40) and are intentionally kept consistent with each other.
