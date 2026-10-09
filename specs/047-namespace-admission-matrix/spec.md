# Feature Specification: Namespace Validation and Admission Matrix

**Feature Branch**: `047-namespace-admission-matrix`  
**Created**: 2026-08-17  
**Status**: Draft  
**Input**: User description: "Namespace Validation and Admission Matrix (GH#173). Context: unblocked now that GH#171 shipped. Document namespace validation and admission boundaries for create, update, and delete operations. Scope: separate structural and schema validation from policy admission checks, define an operation-by-operation validation matrix, define deletion safety rules (protected, non-empty, policy-blocked namespaces), and document failure examples and expected status condition outcomes."

## Clarifications

### Session 2026-08-17

- Q: Spec 046 (GH#172) adopted `docs/ADRs/0002-namespace-lifecycle.md` in full, which introduces a real `Terminating`/finalizer lifecycle state — directly contradicting this spec's original "no Terminating state exists" assumption (old FR-012). How should this spec reconcile? → A: Drop that assumption entirely. This spec now documents the validation rule catalogue *against* the lifecycle spec 046 owns, rather than assuming that lifecycle doesn't exist. Ownership split: spec 046 owns lifecycle *behavior* (bootstrap namespaces, mutation-to-Git delegation, the finalizer/`Terminating` state machine, reconciliation); this spec owns the full *validation rule matrix* (which phase — pre-receive structural, admission policy, or controller-readiness — evaluates which rule) and the deletion-reason taxonomy.
- Q: The original "protected" and "policy-blocked" deletion reasons were invented ahead of ADR-0002 and don't map cleanly onto it. How should they be redefined? → A: "Protected" is redefined to mean exactly the two bootstrap namespaces (`gitstore-system`, `default`), matching spec 046 FR-011 — this spec does not introduce a separate protected-namespace designation mechanism. "Policy-blocked" is replaced with "already terminating": a deletion request against a namespace already in the `Terminating` state (spec 046 FR-014) is reported as a distinguishable outcome rather than invented as a new condition-based blocking mechanism with no grounding in the adopted ADR.

### Session 2026-08-29

- Q: Is immutable-field validation a third request-time phase? → A: No. Namespace admission has two request-time phases: structural/pre-receive and stateful policy admission. Immutable-change validation runs within the structural/pre-receive phase but has its own machine-readable reason category, so callers can distinguish it from ordinary shape validation and from policy rejection.
- Q: Which immutable Namespace change is detectable? → A: `metadata.name` is immutable when the old and proposed manifests occupy the same repository path. A simultaneous path-and-name change has no stable manifest identity and is treated as a new Namespace declaration; manifest deletion remains governed by spec 046 and is not reinterpreted as rename.

## User Scenarios & Testing *(mandatory)*

### User Story 1 - Structural mistakes and policy violations are reported as clearly different kinds of failure (Priority: P1)

When a caller submits a namespace create or update request that is wrong in some way, they need to know *what kind* of wrong it is: did they submit something structurally malformed (bad identifier format, a reserved name, an attempt to change an immutable field), or did they submit something structurally valid that a policy rule still disallows? Today these two kinds of rejection are not consistently distinguished, which makes it hard for a caller (or an automated retry) to know whether fixing the request's shape would help at all.

**Why this priority**: This is the foundational distinction GH#173 exists to establish. Every other requirement in this spec — the operation-by-operation matrix, the deletion safety rules, the condition outcomes — depends on structural validation and policy admission being two clearly separated phases with clearly separated failure reasons.

**Independent Test**: Can be fully tested by submitting a request that fails structural validation (e.g., a malformed identifier) and confirming it is rejected with a structural-validation reason without ever reaching a policy check, then separately submitting a structurally valid request that a policy rule rejects, and confirming that rejection carries a policy-admission reason distinguishable from the first.

**Acceptance Scenarios**:

1. **Given** a namespace create or update request with a structurally invalid value (e.g., a malformed identifier, or a reserved identifier), **When** it is submitted, **Then** it is rejected with a structural-validation reason, and no policy admission check is evaluated for that request.
2. **Given** a namespace update request that is structurally valid but attempts to change an immutable field, **When** it is submitted, **Then** it is rejected with a reason distinguishable from both a structural-validation failure and a policy-admission failure.
3. **Given** a namespace create or update request that is structurally valid, **When** a policy admission rule rejects it, **Then** the rejection is reported with a policy-admission reason, distinguishable from a structural-validation failure.

---

### User Story 2 - Namespace deletion is blocked for clearly distinguishable reasons, and every applicable reason is surfaced (Priority: P1)

An operator attempting to delete a namespace needs to know exactly why deletion is blocked when it is: because the namespace still owns repositories (the existing rule), because the namespace is one of the two bootstrap namespaces (which can never be deleted), or because the namespace is already in the process of being deleted. If more than one of the first two reasons applies at once, the operator needs to see both, not just whichever one happened to be checked first — otherwise they might resolve one blocker, retry, and be surprised by a second one they were never told about.

**Why this priority**: Equal in importance to User Story 1 — this is the specific, concrete deletion-safety matrix GH#173 exists to define, building directly on the non-empty rule already shipped in spec 041 and the bootstrap-namespace/`Terminating` lifecycle spec 046 introduces.

**Independent Test**: Can be fully tested by attempting to delete a namespace under each condition independently (non-empty; bootstrap; already-terminating) and confirming each produces its own distinguishable outcome, then attempting to delete a namespace that is both non-empty and a bootstrap namespace and confirming both reasons are reported together.

**Acceptance Scenarios**:

1. **Given** a namespace with at least one repository, **When** deletion is attempted, **Then** it is rejected with the existing non-empty reason (unchanged from spec 041).
2. **Given** one of the two bootstrap namespaces, **When** deletion is attempted, **Then** it is rejected with a bootstrap-namespace reason, regardless of whether the namespace is empty.
3. **Given** a namespace already in the `Terminating` state, **When** a second deletion is attempted, **Then** the caller is told the namespace is already being deleted, distinguishable from a rejection — this is not a new, independently-tracked deletion attempt (spec 046 FR-014).
4. **Given** a namespace that is both non-empty and a bootstrap namespace, **When** deletion is attempted, **Then** the rejection reports both the non-empty reason and the bootstrap-namespace reason together, not just one of them.
5. **Given** an eligible, non-bootstrap, empty namespace, **When** deletion is attempted, **Then** it succeeds and the namespace transitions to `Terminating` (spec 046).

---

### User Story 3 - A caller can tell, after the fact, why an admission decision was made (Priority: P2)

An administrator investigating a namespace's history needs to be able to inspect, from the namespace's own status, whether its current configuration passed policy admission — without having to have been present at request time to read the original error response.

**Why this priority**: Lower urgency than User Stories 1 and 2 because it's an observability/inspectability improvement on top of already-correct admission behavior, rather than a new admission rule, but it directly fulfills GH#173's "document... expected status condition outcomes" scope item.

**Independent Test**: Can be fully tested by creating or updating a namespace successfully and confirming its status exposes a condition reflecting that its current configuration passed admission, using only read access to the namespace's status — no new write behavior needs to exist for this test to pass.

**Acceptance Scenarios**:

1. **Given** a namespace whose most recent create or update was accepted, **When** its status is read, **Then** it exposes a condition reflecting that its current configuration passed admission, referencing the generation it was evaluated against.
2. **Given** a namespace that is already `Terminating`, **When** its status is read, **Then** that state is present and distinguishable from the admission-acceptance condition in this User Story's first scenario.

---

### Edge Cases

- What happens when a request fails both structural validation and would also have failed a policy check? (Only the structural-validation reason is reported — policy checks are never evaluated once structural validation has already failed, per User Story 1.)
- What happens when a namespace's bootstrap status or `Terminating` state changes after it was already created (i.e., these are not only creation-time checks)? (Both are evaluated at delete time regardless of when the namespace was created, including namespaces that predate this specification.)
- What happens when a `Namespace` manifest update (spec 046) targets a namespace that is already `Terminating`? (Rejected — a namespace being deleted cannot also be updated; see FR-013.)
- What happens when a caller checks whether a namespace can be deleted without actually attempting deletion? (Out of scope for this spec — no dry-run/preview capability is introduced here; a caller learns the applicable reasons only by attempting the operation.)
- What happens to resource-creation requests for repositories, products, categories, or collections that reference a namespace that does not exist, or a namespace that is in the process of being deleted? (Explicitly out of scope for this spec — see Assumptions. This spec covers only Namespace's own create/update/delete admission, not admission checks performed on other resource kinds.)

## Requirements *(mandatory)*

### Functional Requirements

- **FR-001**: The system MUST evaluate structural/schema validation for every namespace create and update request before evaluating any policy-based admission rule, and MUST NOT evaluate a policy-based admission rule once structural/schema validation has failed.
- **FR-002**: The system MUST report a structural/schema validation failure using a reason distinguishable from a policy-admission rejection reason.
- **FR-003**: The system MUST report an attempt to change `metadata.name` between old and proposed Namespace manifests at the same repository path using an immutable-field reason distinguishable from both an ordinary structural-validation failure and a policy-admission rejection.
- **FR-004**: The system MUST continue to reject deletion of a namespace that owns one or more repositories, unchanged from the existing rule (spec 041), and MUST report this using a reason distinguishable from the other deletion-outcome reasons defined by this spec.
- **FR-005**: The system MUST reject deletion of either bootstrap namespace (`gitstore-system`, `default`), regardless of whether it is empty, and MUST report this using a reason distinguishable from the other deletion-outcome reasons. (Behaviorally owned by spec 046 FR-011; this spec documents it as part of the deletion-safety matrix.)
- **FR-006**: The system MUST report a deletion request against a namespace already in the `Terminating` state as a distinguishable "already being deleted" outcome, not as a new, independently-tracked deletion attempt. (Behaviorally owned by spec 046 FR-014; this spec documents its place in the matrix.)
- **FR-007**: When more than one of the non-empty and bootstrap-namespace reasons applies to the same deletion attempt, the system MUST report both, not only the first one evaluated.
- **FR-008**: The system MUST expose, via a status condition, whether a namespace's current configuration most recently passed admission, including the generation it was evaluated against (the `AdmissionAccepted` condition defined by spec 046).
- **FR-009**: The system MUST expose a namespace's `Terminating` state, when present, as distinguishable from the admission-acceptance condition in FR-008.
- **FR-010**: The system MUST leave existing structural validations already enforced at namespace creation (identifier format, reserved-identifier blocklist) unchanged in behavior.
- **FR-011**: The system MUST NOT introduce any admission check that evaluates or blocks operations on resource kinds other than Namespace itself.
- **FR-012**: The system MUST evaluate, as a policy-admission rule, whether a `Namespace` manifest's `spec.tier` attempts to demote an existing namespace's tier (spec 046 FR-006), reporting this with a reason distinguishable from other admission failures.
- **FR-013**: The system MUST reject a `Namespace` manifest update (create/update per spec 046) targeting a namespace that is currently `Terminating`, using a reason distinguishable from a not-found error.

### Key Entities

- **Structural/schema validation (pre-receive)**: The first admission phase — checks a request's shape and values in isolation (format, same-path `metadata.name` immutability, reserved identifiers, repository-restriction per spec 046) without regard to any other namespace's or the system's current state. Immutable changes receive their own reason category within this phase. Structural failure always short-circuits policy admission.
- **Policy admission**: The second admission phase — checks a structurally valid request against broader rules that may depend on system state (name uniqueness, tier-demotion rejection, `Terminating`-target rejection). Only evaluated once structural/schema validation has passed.
- **Controller-readiness phase**: The asynchronous third phase (spec 046's reconciler) — sets `SystemRepoReady`/`Ready` once admission has accepted a namespace's spec. Not a request-time validation phase; documented here for completeness of the matrix.
- **Deletion-outcome reason**: One of three distinguishable outcomes for a namespace deletion request — non-empty (owns repositories, spec 041; rejection), bootstrap namespace (deletion always disallowed; rejection), or already-terminating (deletion already in progress; not a rejection, a redundant-request signal).
- **Admission-acceptance condition**: The `AdmissionAccepted` status condition (spec 046) recording whether a namespace's current configuration, as of a specific generation, passed admission.

## Success Criteria *(mandatory)*

### Measurable Outcomes

- **SC-001**: 100% of structurally invalid create/update requests are rejected with a structural-validation reason, with zero instances of a policy-admission reason being reported instead.
- **SC-002**: 100% of deletion attempts against a bootstrap namespace are rejected, regardless of emptiness, with zero exceptions.
- **SC-003**: 100% of deletion attempts against a namespace already `Terminating` are reported as an already-being-deleted outcome rather than starting a second, independent deletion attempt.
- **SC-004**: 100% of deletion attempts against a namespace that is both non-empty and a bootstrap namespace report both reasons together, with zero instances of a caller being told about only one when both applied.
- **SC-005**: 100% of namespaces with an accepted current configuration expose a status condition reflecting that acceptance and the generation it applies to, readable without needing the original request/response.
- **SC-006**: Zero regressions to the existing non-empty deletion rule (spec 041) or to existing creation-time structural validations.

## Assumptions

- Admission checks that evaluate *other* resource kinds' requests against namespace existence or lifecycle state (GH#173's "Namespace exists admission plugin" open question — e.g., rejecting a repository, product, category, or collection create because its referenced namespace does not exist) remain explicitly **out of scope** for this specification. That is a cross-cutting concern spanning every namespaced resource kind, not specific to Namespace's own lifecycle operations, and is large enough to warrant its own future specification.
- This spec's original assumption that no "terminating" namespace lifecycle state exists is **retracted** (see Clarifications). Spec 046 (GH#172), after adopting `docs/ADRs/0002-namespace-lifecycle.md` in full, introduces the real `Terminating`/finalizer state machine. This spec documents the validation rules that interact with that lifecycle (e.g., FR-013) rather than assuming it away; it does not re-implement or duplicate the lifecycle behavior itself, which spec 046 owns.
- "Protected" is redefined to mean exactly the two bootstrap namespaces (`gitstore-system`, `default`, per spec 046 FR-011), not the pre-existing `reservedIdentifiers` creation-time blocklist (which remains an unrelated, unchanged structural check per FR-010). This spec does not introduce a mechanism for designating additional namespaces as protected.
- "Policy-blocked" (an independent, condition-based deletion-blocking mechanism with no grounding in ADR-0002) is **dropped**. The third deletion outcome is "already terminating" (spec 046 FR-014), which is not a caller-configurable policy but a direct consequence of the lifecycle state machine.
- No dry-run or "can I delete this" preview capability is introduced; callers learn applicable deletion outcomes only by attempting the operation, consistent with how every other lifecycle check in this codebase currently works.
- This spec builds directly on GH#171's schema and GH#172's (spec 046) lifecycle, admission, and concurrency semantics; it documents the validation rule matrix against that lifecycle rather than redefining it.
