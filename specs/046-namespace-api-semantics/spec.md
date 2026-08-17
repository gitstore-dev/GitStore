# Feature Specification: Namespace API Semantics: Spec Writes, Status Updates, Concurrency

**Feature Branch**: `046-namespace-api-semantics`
**Created**: 2026-08-17
**Status**: Draft
**Input**: User description: "Namespace API Semantics: Spec Writes, Status Updates, Concurrency (GH#172). Context: GH#172 is unblocked now that GH#171 (Namespace declarative .spec/.status schema) has shipped. Define namespace API semantics for .spec updates, status-subresource style .status updates, and optimistic concurrency behavior. Scope: define create/update/delete behavior and error contracts, define the status update path and ownership boundaries, define resourceVersion conflict semantics for concurrent writes, and provide API examples for successful and rejected operations. GH#172 blocks GH#174 (Namespace Watch Contract)."

## User Scenarios & Testing *(mandatory)*

### User Story 1 - An owner can update a namespace's mutable configuration and get a deterministic result (Priority: P1)

Today a namespace can only be created or deleted — there is no way to change its configuration (title, repository defaults, push-policy defaults) once created. A namespace owner needs a way to update that mutable configuration and receive a clear, deterministic outcome: either the update succeeds and the namespace reflects the new configuration, or it is rejected with a specific, actionable reason.

**Why this priority**: This is the core capability GH#172 exists to deliver. The declarative schema (GH#171) defined what a namespace's configuration looks like; without an update path, that configuration is write-once, which makes the schema far less useful and blocks everything else in this initiative that assumes namespaces can evolve after creation.

**Independent Test**: Can be fully tested by creating a namespace, updating its title and repository defaults, and confirming the namespace now reflects the new values, its version marker has changed, and its generation counter has advanced — all without needing status-update or watch behavior to exist.

**Acceptance Scenarios**:

1. **Given** an existing namespace, **When** its owner submits an update changing its title, **Then** the update succeeds, the namespace's title reflects the new value, and its version marker and generation counter both advance.
2. **Given** an existing namespace, **When** its owner submits an update that does not change any value (a no-op update), **Then** the update succeeds without error, but the generation counter does not advance since no configuration actually changed.
3. **Given** an existing namespace, **When** its owner submits an update attempting to change its tier, **Then** the update is rejected with a clear, specific reason distinguishing "you cannot change this field" from any other failure, and the namespace's tier remains unchanged.
4. **Given** an update request for a namespace that does not exist, **When** the update is submitted, **Then** it is rejected with a clear "not found" reason, distinguishable from a validation or conflict failure.

---

### User Story 2 - Concurrent updates to the same namespace resolve deterministically, never silently corrupting or losing a change (Priority: P1)

Two callers (e.g., an administrator using the console and an automated process) attempt to update the same namespace's configuration at roughly the same time, each starting from what they believe is the current state. The system must ensure that at most one of these concurrent updates succeeds, and the other receives a clear, actionable conflict response rather than silently overwriting the winner's change or silently being dropped.

**Why this priority**: Equal in importance to User Story 1 — an update capability without concurrency safety is actively dangerous: it would let one caller's change silently clobber another's, which is worse than not having updates at all. This must ship in the same increment as User Story 1, not as a later hardening pass.

**Independent Test**: Can be fully tested by reading a namespace to capture its current version marker, updating it once (which succeeds and advances the version marker), then attempting a second update using the now-stale, previously-captured version marker — the second update must be rejected with a conflict response that includes the namespace's actual current version marker, and the namespace must reflect only the first update's change.

**Acceptance Scenarios**:

1. **Given** a namespace at a known version marker, **When** an update is submitted with that exact version marker as a precondition, **Then** the update succeeds and the namespace's version marker changes to a new value.
2. **Given** a namespace whose version marker has since changed (e.g., due to a prior update), **When** a second update is submitted using the now-stale version marker, **Then** the update is rejected with a conflict response that reports the namespace's actual current version marker, and no part of the second update is applied.
3. **Given** two updates submitted concurrently against the same namespace using the same starting version marker, **When** both are processed, **Then** exactly one succeeds and the other receives a conflict response — never both succeeding, and never both failing when the namespace was actually eligible for one of them to succeed.

---

### User Story 3 - System-computed status can never be set or corrupted through a configuration update (Priority: P2)

A namespace's system-computed status (its observed generation, last-applied revision marker, and conditions) must remain exclusively system-controlled. A caller updating a namespace's configuration must have no way — accidental or deliberate — to directly set or override any status field through that same request.

**Why this priority**: Lower urgency than User Stories 1 and 2 because it is a safety/integrity boundary on top of already-working update behavior, rather than new caller-facing capability, but it must hold from the same release, since allowing status to be caller-writable even briefly would corrupt the owner-supplied-vs-system-computed separation GH#171 just established.

**Independent Test**: Can be fully tested by attempting to submit configuration-update requests that include values for status-shaped fields (conditions, observed generation, last-applied revision) and confirming the namespace's actual status is unaffected by whatever was submitted, using only the configuration-update path — no separate status-write behavior needs to exist yet for this test to pass.

**Acceptance Scenarios**:

1. **Given** a namespace update request that has no means to express a status value, **When** the update is submitted and succeeds, **Then** the namespace's status conditions, observed generation, and last-applied revision are determined solely by the system, not by any value the caller supplied.
2. **Given** a successful configuration update, **When** the namespace's status is inspected afterward, **Then** the status remains internally consistent (e.g., observed generation is never left referring to a superseded generation without a defined, deterministic transition rule).

---

### Edge Cases

- What happens when an update is submitted with no version-marker precondition supplied at all? (Rejected — the precondition is required for every update; there is no unconditional "just overwrite whatever is there" path.)
- What happens when a namespace is deleted while an update to it is in flight? (The update must not succeed against a namespace that no longer exists; it must fail with a "not found" reason, not silently recreate or partially apply.)
- What happens when an update attempts to change multiple fields, some valid and some invalid (e.g., a valid title change bundled with an invalid tier change)? (The entire update is rejected — no partial application of only the valid parts.)
- What happens when an update is retried after a conflict, using the version marker reported in the conflict response? (It must be treated as a fresh update attempt against the now-current state, and can succeed if no further concurrent change has occurred since.)
- What happens when repository defaults or push-policy defaults are updated to remove a previously-set nested value (e.g., clearing a default branch)? (Must be distinguishable from "leave this field unchanged" — the update contract must support explicit clearing, not just setting.)

## Requirements *(mandatory)*

### Functional Requirements

- **FR-001**: The system MUST provide a way for a namespace's mutable configuration (title, repository defaults, push-policy defaults) to be updated after creation.
- **FR-002**: The system MUST require every configuration-update request to include a version-marker precondition, and MUST reject any update request that omits it.
- **FR-003**: The system MUST reject a configuration-update request whose version-marker precondition does not match the namespace's actual current version marker, and MUST report the namespace's actual current version marker in the rejection so the caller can retry.
- **FR-004**: The system MUST NOT apply any part of a configuration-update request once its version-marker precondition has been found stale — rejection MUST be all-or-nothing, never partial.
- **FR-005**: The system MUST reject any configuration-update request that attempts to change the namespace's tier, with a rejection reason that specifically identifies the tier field as immutable, distinguishable from a version-marker conflict or a not-found error.
- **FR-006**: The system MUST advance the namespace's version marker on every successful configuration update, whether or not the generation counter also advances.
- **FR-007**: The system MUST advance the namespace's generation counter only when a successful configuration update actually changes at least one field's value, and MUST NOT advance it for a no-op update that changes nothing.
- **FR-008**: The system MUST resolve concurrent configuration-update requests against the same namespace deterministically: for any set of concurrent requests starting from the same version marker, at most one succeeds, and every other request receives a conflict rejection.
- **FR-009**: The system MUST ensure that a namespace's status fields (conditions, observed generation, last-applied revision) can never be set, overridden, or influenced by any value supplied in a configuration-update request.
- **FR-010**: The system MUST report a specific, distinguishable rejection reason for each of: namespace not found, version-marker conflict, immutable-field-change attempt, and configuration validation failure — a caller must be able to tell these apart programmatically, not just from a human-readable message.
- **FR-011**: The system MUST leave the existing namespace create and delete operations' behavior, inputs, and outputs unchanged.
- **FR-012**: The system MUST allow a caller to explicitly clear a previously-set optional nested configuration value (e.g., a repository default) as part of an update, distinguishably from leaving that value unchanged.

### Key Entities

- **Namespace configuration update**: A request to change a namespace's owner-supplied configuration (title, repository defaults, push-policy defaults), guarded by a required version-marker precondition. Succeeds or fails as a whole; never partially applied.
- **Version-marker conflict**: The outcome when an update's precondition does not match the namespace's actual current version marker. Carries the namespace's actual current version marker so the caller can retry deliberately.
- **Namespace status**: The system-computed part of a namespace (conditions, observed generation, last-applied revision), established by GH#171 and reaffirmed here as exclusively system-writable — no configuration-update request can set or affect it.

## Success Criteria *(mandatory)*

### Measurable Outcomes

- **SC-001**: 100% of configuration updates submitted with a version marker matching the namespace's actual current value succeed and correctly advance the version marker.
- **SC-002**: 100% of configuration updates submitted with a stale version marker are rejected with a conflict response containing the namespace's actual current version marker, with zero instances of a stale update partially or fully overwriting a newer change.
- **SC-003**: 100% of update attempts to change a namespace's tier are rejected with a distinguishable immutable-field reason, with zero instances of a tier value changing through this path.
- **SC-004**: Under concurrent update load against the same namespace, exactly one request succeeds per eligible round and 100% of the others receive a conflict response — zero instances of both succeeding or both being silently dropped.
- **SC-005**: 100% of successful configuration updates leave the namespace's status fields exactly as the system determines them, with zero instances of a caller-supplied value appearing in status.
- **SC-006**: 100% of existing namespace create and delete requests continue to succeed or fail under exactly the same conditions as before this specification, with zero regressions.

## Assumptions

- This specification does not introduce a caller-facing status-update mutation for Namespace. Unlike CategoryTaxonomy (which has an external controller that writes status via a dedicated mutation), no Namespace controller or reconciler exists yet — per GH#171's assumptions — so namespace status continues to be written synchronously and exclusively by the same internal code path that already handles create, delete, and (per this spec) update. If a future spec introduces a Namespace controller, that spec is where a dedicated status-update mutation would be added; this spec only guarantees the *boundary* (status is never caller-writable) referenced in GH#172's "status update path and ownership boundaries" scope item.
- Tier is treated as immutable after creation. This is a deliberate, informed default: tier determines ownership and repository capability semantics (per the existing `NamespaceTier` contract), and changing it after repositories may already exist under a namespace has significant, currently-undefined downstream consequences that are out of scope for this spec. If tier mutability is ever needed, it should be its own specification.
- The identifier (`metadata.name`) remains immutable after creation, consistent with every other catalog resource's naming convention in this codebase (renaming a namespace is not modeled as a configuration update). This spec does not introduce namespace renaming.
- Concrete request/response examples for successful and rejected operations (an explicit acceptance-criteria item in GH#172) are a planning/documentation deliverable, not a specification-level requirement, and belong in this feature's plan and quickstart artifacts rather than in this spec.
- Namespace deletion's existing precondition (rejecting deletion when repositories exist, per spec 041) is unaffected by and unrelated to the update semantics defined here.
