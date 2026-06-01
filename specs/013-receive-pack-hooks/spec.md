# Feature Specification: In-Process git-receive-pack Hook Pipeline

**Feature Branch**: `013-receive-pack-hooks`  
**Created**: 2026-06-01  
**Status**: Closed  
**Input**: User description: "Implement GH#110 — Implement in-process git-receive-pack hook pipeline"

## Clarifications

### Session 2026-06-01

- Q: Should `update` hook rejection abort the entire push or only the rejected ref? → A: Per-ref for `update` (only the rejected ref is blocked); all-or-nothing only for `pre-receive`.
- Q: Should post-receive / post-update failures surface to the Git client or be fire-and-forget? → A: Fire-and-forget — failures logged at ERROR level, never returned to the client (matches standard Git semantics).
- Q: Does reference-transaction support veto (push abort) or is it observation-only? → A: Full three-state (prepared/committed/aborted); veto allowed in `prepared` state only.
- Q: What observability signals should the hook pipeline emit? → A: Structured log entry per phase (phase name, ref, duration ms, accept/reject outcome).
- Q: What timeout applies to admission service calls, and what happens on timeout? → A: Fixed 5-second timeout per call; timeout is treated as rejection (fail-closed).

## User Scenarios & Testing *(mandatory)*

### User Story 1 - Push Accepted Through Enabled Hook Phases (Priority: P1)

A developer pushes commits to a repository hosted on GitStore. The push passes through all enabled hook phases (pre-receive, update, proc-receive, reference-transaction, post-receive) in the correct order before the refs are updated. The developer receives confirmation that the push succeeded.

**Why this priority**: This is the foundational happy-path scenario. Without a working hook pipeline that accepts valid pushes, none of the dependent admission or validation features (#105, #106) can function. It is the minimal viable behaviour.

**Independent Test**: Can be fully tested by running `git push` against a repository with all hook phases enabled and verifying that refs are updated and the client sees a success response.

**Acceptance Scenarios**:

1. **Given** a repository with all hook phases enabled, **When** a developer pushes a new commit, **Then** each enabled phase executes in order (pre-receive → update → proc-receive → reference-transaction → post-receive) and the push succeeds with the refs updated.
2. **Given** a repository where only `pre-receive` and `post-receive` phases are enabled, **When** a developer pushes, **Then** only those two phases run and the push still succeeds.
3. **Given** a successful push, **When** post-receive runs, **Then** the hook receives the full list of updated refs as input.

---

### User Story 2 - Push Rejected by a Hook Phase (Priority: P1)

A developer attempts to push commits that violate a policy enforced in the pre-receive or update phase. The push is rejected atomically — no refs are updated — and the developer sees a clear, actionable error message explaining why the push was denied.

**Why this priority**: Rejection with a human-readable reason is the core value proposition of the hook pipeline. Without atomic rejection, downstream admission control (#105, #106) cannot safely block invalid pushes.

**Independent Test**: Can be fully tested by enabling the pre-receive phase with a rejection rule and verifying that `git push` returns a non-zero exit code with the error text visible in the client output, and that the remote refs remain unchanged.

**Acceptance Scenarios**:

1. **Given** the pre-receive phase is enabled and configured to reject, **When** a developer pushes, **Then** the push is aborted, no refs are updated, and the client receives a descriptive rejection message.
2. **Given** the update phase rejects one ref in a multi-ref push, **When** a developer pushes multiple branches simultaneously, **Then** only the rejected ref is blocked (best-effort atomicity) and accepted refs are updated.
3. **Given** the proc-receive phase rejects a push, **When** a developer pushes, **Then** refs are not updated and the client sees the rejection reason.

---

### User Story 3 - Selectively Disable Hook Phases via Configuration (Priority: P2)

An operator configures GitStore to skip specific hook phases (e.g., disable `proc-receive` and `post-update`) for a deployment. Pushes complete without executing the disabled phases, and the system behaves identically to a deployment where those phases were never implemented.

**Why this priority**: Operators need granular control to roll out hook phases incrementally or disable them for performance-sensitive paths without modifying code. This is required to preserve backward compatibility with existing deployments.

**Independent Test**: Can be fully tested by toggling each phase toggle independently, pushing to the repository, and verifying through observable behaviour (logs, response timing, ref state) that only the enabled phases ran.

**Acceptance Scenarios**:

1. **Given** `GITSTORE_HOOKS_GIT_RECEIVE_PACK_PRE_RECEIVE_ENABLED=false`, **When** a developer pushes, **Then** the pre-receive phase is skipped and the push completes without any pre-receive logic running.
2. **Given** all hook phases disabled, **When** a developer pushes, **Then** the push succeeds immediately with no hook overhead.
3. **Given** `GITSTORE_HOOKS_GIT_RECEIVE_PACK_PROC_RECEIVE_ENABLED=true` and all others disabled, **When** a developer pushes, **Then** only proc-receive executes.

---

### User Story 4 - Hook Pipeline Provides Routing Points for Admission Services (Priority: P2)

A GitStore operator has configured an external validation or admission policy service (future: #105, #106). The hook pipeline calls out to that service at the appropriate phase and forwards the push context (ref name, old OID, new OID). The service's accept/reject decision is propagated back to the Git client.

**Why this priority**: This is the extensibility contract that makes the entire hook system valuable beyond simple logging. It enables #105 and #106 without requiring further changes to the push flow.

**Independent Test**: Can be fully tested by wiring a stub admission service and verifying that the stub's decision (accept or reject) controls the push outcome.

**Acceptance Scenarios**:

1. **Given** an admission phase is configured and the stub service returns "accept", **When** a developer pushes, **Then** the push succeeds.
2. **Given** an admission phase is configured and the stub service returns "reject" with a reason, **When** a developer pushes, **Then** the push is rejected and the client sees the reason from the service.
3. **Given** the admission service is unreachable, **When** a developer pushes, **Then** the push is rejected with a service-unavailable error (fail-closed by default).

---

### Edge Cases

- What happens when a hook phase times out mid-execution? The push is rejected and the client receives a timeout error; no partial ref update occurs.
- What happens when a push updates 50+ refs simultaneously? All refs pass through each hook phase; performance degrades gracefully rather than failing.
- What happens when the same ref is pushed twice concurrently? Hook phases execute for each push independently; ref-transaction semantics prevent double-update races.
- What happens when a post-receive hook fails? The push is already committed (refs updated); the failure is logged but does not roll back the push (post-* hooks are best-effort).
- What happens when a push contains a branch deletion (new OID = zero)? Hook phases receive the zero OID as the new value and must treat it as a deletion event.
- What happens when no refs are included in the push? The hook pipeline is not invoked; the client receives an empty-update response.

## Requirements *(mandatory)*

### Functional Requirements

- **FR-001**: The system MUST execute enabled hook phases in the following order for each push: pre-receive → proc-receive → update (per ref) → reference-transaction → post-receive.
- **FR-002**: The system MUST skip any hook phase whose configuration toggle is set to disabled, without error or observable side effect.
- **FR-003a**: The system MUST abort the push and leave all refs unchanged if the pre-receive phase returns a rejection (all-or-nothing semantics).
- **FR-003b**: The system MUST block only the rejected ref if the update phase returns a rejection for that ref; other refs in the same push MUST proceed normally (per-ref semantics).
- **FR-004**: The system MUST surface the rejection reason from a hook phase as a human-readable message visible to the Git client.
- **FR-005**: The system MUST execute the update phase once per ref being pushed (each ref is an independent invocation).
- **FR-006**: The system MUST execute pre-receive and post-receive phases exactly once per push, regardless of how many refs are being updated.
- **FR-007**: The system MUST execute proc-receive in the appropriate position in the pipeline when enabled, with the ability to rewrite ref targets.
- **FR-008**: The system MUST invoke the reference-transaction phase in three states per push: `prepared` (ref locks acquired — veto allowed, push aborted if rejected), `committed` (refs written — observation only), and `aborted` (rollback — observation only). Rejection is only honoured in the `prepared` state.
- **FR-009**: The system MUST provide hook invocation points that accept a routing contract so that future admission and validation services (#105, #106) can be integrated without modifying the pipeline ordering.
- **FR-009a**: Each admission phase call MUST time out after 5 seconds; a timeout MUST be treated as a rejection (fail-closed). No client push may be stalled indefinitely by an unresponsive admission service.
- **FR-010**: The system MUST respect the `GITSTORE_ADMISSION_CONTROL_VALIDATING_ADMISSION_POLICY_PHASE` configuration to determine which phase admission policy evaluation is routed to.
- **FR-011**: The system MUST NOT invoke any external `git` binary when executing hook phases; all phase logic MUST run in-process.
- **FR-012**: Post-receive and post-update failures MUST be recorded as structured ERROR-level log entries but MUST NOT cause the push to be reported as failed to the client (fire-and-forget semantics matching standard Git behavior).
- **FR-013**: The system MUST emit a structured log entry for each hook phase execution containing at minimum: phase name, ref name (where applicable), duration in milliseconds, and outcome (accepted/rejected with reason).

### Key Entities

- **Push Event**: A single `git push` operation; carries one or more ref updates (each with ref name, old OID, new OID) and the pack data.
- **Ref Update**: A single ref being created, updated, or deleted as part of a push event; the unit of input to the `update` phase.
- **Hook Phase**: A named, ordered stage in the push pipeline (pre-receive, proc-receive, update, reference-transaction, post-receive); each has an enabled/disabled toggle.
- **Hook Decision**: The outcome of a hook phase execution — either "accept" (push proceeds) or "reject" (push aborted with reason).
- **Admission Phase Assignment**: The configuration value that maps the admission policy evaluation to a specific hook phase.

## Success Criteria *(mandatory)*

### Measurable Outcomes

- **SC-001**: 100% of pushes through an enabled hook pipeline execute phases in the documented order, verifiable by integration test assertions on phase execution sequence.
- **SC-002**: A push rejected by any pre-receive or update phase results in zero ref updates on the server side in all tested scenarios.
- **SC-003**: Rejection messages from hook phases appear verbatim in the `git push` client output for 100% of rejection test cases.
- **SC-004**: Disabling a hook phase via configuration results in that phase never executing, confirmed across all integration test runs.
- **SC-005**: The hook pipeline adds no more than 50 ms of latency to a push that exercises all enabled phases with no-op phase logic (measured in integration tests without network round-trips to external services).
- **SC-006**: Integration tests cover at minimum: single-ref accepted push, single-ref rejected push (pre-receive), single-ref rejected push (update), multi-ref push with one rejection, all-phases-disabled push, and admission routing phase confirmation.

## Assumptions

- The in-process hook pipeline replaces only the hook execution mechanism; the Git pack protocol, ref negotiation, and object storage remain as implemented in feature #009 (remove git shell-outs).
- Admission and validation logic bodies (the actual policies) are out of scope; this feature provides the invocation points only (#105 and #106 implement the logic).
- The fail-closed behaviour for unreachable admission services (FR-009) is the safe default; operators who require fail-open must configure it explicitly (tracked separately).
- Phase configuration toggles already exist in the codebase (from feature #005); this feature preserves their semantics without introducing new configuration keys.
- The `post-update` (legacy) phase is explicitly excluded from scope per the issue.
- `push-to-checkout` is in scope as a phase invocation point but its checkout logic is minimal (update the working tree on non-bare repos); GitStore uses bare repositories so this phase is a no-op by default.

## Dependencies

- **Requires**: Feature #009 (remove git shell-outs) — the in-process push flow must be in place before hooks can be injected.
- **Supports**: Feature #105 (validation logic), Feature #106 (admission policy evaluation), Feature #107.
- **Related**: Issue #139 (release-tag notification, excluded from this scope).
