# Feature Specification: Controller Integration Tests + Operations Runbook

**Feature Branch**: `038-controller-integration-tests-runbook`  
**Created**: 2026-08-06  
**Status**: Draft  
**Input**: User description: "Controller Integration Tests + Operations Runbook (Resume, Conflicts, Poison Retries) — GitHub issue #183, parent initiative #165. Add integration coverage and operational runbooks for controller manager behavior in normal and failure scenarios: integration tests for successful reconcile, retry, resume, and status conflict handling; tests for disconnect/reconnect and replay recovery behavior; runbooks for controller lag, replay-window exceeded, and poisoned queue item handling; validate observability hooks and metrics needed for operations. Depends on spec 025 (controller manager runtime), spec 026 (reconcile handler), and spec 036 (controller startup resume / list-then-watch / resourceVersion checkpointing), all merged."

## User Scenarios & Testing *(mandatory)*

### User Story 1 - Verify reconcile, retry, and resume behavior end-to-end (Priority: P1)

An operator or maintainer needs confidence that the controller manager correctly reconciles resources under normal conditions, retries transient failures, and resumes cleanly from a checkpoint after a restart — without relying on manual, ad-hoc verification in a live environment.

**Why this priority**: This is the foundation of trust in the controller manager runtime shipped across specs 025, 026, and 036. Without automated coverage of the core reconcile/retry/resume path, every change to the runtime risks silent regressions that only surface in production.

**Independent Test**: Can be fully tested by running an integration test suite against a controller manager instance with a fake/in-memory API backend, driving it through create → reconcile-success, create → transient-failure → retry-success, and stop → restart → resume-from-checkpoint sequences, and asserting on final resource status and reconcile counts.

**Acceptance Scenarios**:

1. **Given** a newly created resource is queued, **When** the controller reconciles it successfully on the first attempt, **Then** the resource status reflects the successful reconciliation and no retry is recorded.
2. **Given** a resource reconcile attempt fails with a transient error, **When** the controller retries per its backoff policy, **Then** the resource eventually reaches a successful status and the retry count matches the number of failed attempts.
3. **Given** a controller manager process is stopped mid-run with unreconciled items still queued, **When** it restarts, **Then** it resumes from its last persisted checkpoint and reconciles all items that were pending or changed since that checkpoint, without reconciling already-completed items redundantly.

---

### User Story 2 - Verify status-conflict handling (Priority: P1)

A maintainer needs assurance that when two writers (e.g., a stale controller replica and a fresher one, or a controller and a direct API update) attempt to update the same resource's status concurrently, the system detects the conflict and resolves it safely rather than silently overwriting newer data with stale data.

**Why this priority**: Status-write conflicts are a correctness-critical failure mode for any reconciler; an undetected conflict can cause data loss or an inconsistent status that downstream consumers rely on. This must be verified before the runtime is trusted with higher-stakes controllers (e.g., CategoryTaxonomy in #244).

**Independent Test**: Can be fully tested by simulating two concurrent status updates to the same resource (one with a stale resourceVersion) and asserting the stale write is rejected and the controller retries reconciliation using the latest resource state.

**Acceptance Scenarios**:

1. **Given** two status updates are attempted for the same resource where one carries a stale resourceVersion, **When** the stale update is submitted, **Then** it is rejected and the resource's status reflects only the update made against the current resourceVersion.
2. **Given** a status write conflict is rejected, **When** the controller detects the conflict, **Then** it re-fetches the current resource state and re-attempts reconciliation rather than failing permanently.

---

### User Story 3 - Verify disconnect, reconnect, and replay-recovery behavior (Priority: P2)

An operator needs confidence that when the controller's watch connection to the API is dropped (network blip, API restart) and later reconnects, the controller correctly replays any missed changes and does not miss or duplicate reconciliation of resources changed during the outage.

**Why this priority**: Spec 036 introduced list-then-watch bootstrap and resourceVersion checkpointing specifically to handle reconnects; this story is the integration-level proof that the mechanism works under a real disconnect/reconnect cycle, not just at the unit level.

**Independent Test**: Can be fully tested by establishing a watch, forcibly disconnecting it, mutating resources while disconnected, reconnecting, and asserting all mutations made during the outage are eventually reconciled exactly once.

**Acceptance Scenarios**:

1. **Given** an active watch connection is interrupted, **When** the controller detects the disconnect, **Then** it attempts to reconnect using a backoff policy and logs/exposes the disconnect event.
2. **Given** resources were created, updated, or deleted while the watch was disconnected, **When** the watch reconnects and resumes from its last checkpoint, **Then** every changed resource is reconciled exactly once with no gaps and no duplicate reconciliation of unchanged resources.
3. **Given** the reconnect attempt finds that the last checkpoint is outside the API's replay window (expired cursor / "410 Gone"-equivalent), **When** replay is no longer possible, **Then** the controller falls back to a full list-then-watch bootstrap rather than failing permanently.

---

### User Story 4 - Diagnose and recover from operational failure modes using runbooks (Priority: P2)

An on-call engineer, when paged for controller lag, an exhausted replay window, or a queue item that repeatedly fails reconciliation ("poisoned" item), needs a documented, step-by-step procedure to diagnose the condition using available metrics/logs and take a safe recovery action — without needing to read controller source code under time pressure.

**Why this priority**: Automated tests validate the system behaves correctly; runbooks ensure humans can respond correctly when the system's own recovery mechanisms are insufficient or when the failure needs a manual decision (e.g., whether to skip a poisoned item). This is required before the controller manager can be considered production-operable.

**Independent Test**: Can be fully tested by having someone unfamiliar with the controller internals follow each runbook against a deliberately induced version of that failure (lag, expired checkpoint, poisoned item) in a test environment and successfully diagnose and resolve it using only the runbook and exposed metrics/logs.

**Acceptance Scenarios**:

1. **Given** a controller's reconcile queue is growing and falling behind incoming changes, **When** an engineer follows the controller-lag runbook, **Then** they can identify the lag using exposed metrics and determine whether it is caused by slow reconciles, insufficient worker concurrency, or an upstream stall.
2. **Given** a controller's checkpoint has aged past the API's replay window, **When** an engineer follows the replay-window-exceeded runbook, **Then** they can confirm the fallback-to-full-list behavior occurred (or trigger it manually) and verify the controller has caught up.
3. **Given** a specific queue item fails reconciliation repeatedly beyond the retry limit, **When** an engineer follows the poisoned-item runbook, **Then** they can identify the offending resource, inspect the failure reason from logs/metrics, and choose to either fix the underlying data and requeue it or explicitly quarantine/skip it.

---

### User Story 5 - Validate observability signals used by the above scenarios (Priority: P3)

A maintainer needs confirmation that the metrics, logs, and status conditions the runbooks and tests depend on (queue depth, reconcile duration/error counts, checkpoint age, retry counts, disconnect/reconnect events) are actually emitted and accurate, so that the runbooks are trustworthy rather than aspirational.

**Why this priority**: The runbooks in User Story 4 are only useful if the underlying signals they reference truly exist and reflect reality. This is lower priority than the behavioral tests because it validates instrumentation rather than correctness, but it is required to close the loop on "operations-ready."

**Independent Test**: Can be fully tested by inducing each failure/success scenario from User Stories 1–4 and asserting the expected metric or log line is emitted with the correct value/label at the expected time.

**Acceptance Scenarios**:

1. **Given** a reconcile attempt succeeds or fails, **When** the reconcile completes, **Then** a corresponding metric (count and duration, labeled by outcome and resource kind) is recorded.
2. **Given** a controller's checkpoint is persisted, **When** time passes without a new checkpoint, **Then** a checkpoint-age metric reflects the elapsed time per resource kind, matching what the replay-window-exceeded runbook instructs engineers to check.
3. **Given** a queue item is retried or exceeds the retry limit, **When** that occurs, **Then** the retry count and terminal-failure ("poisoned") state are observable via metrics or logs as referenced by the poisoned-item runbook.

### Edge Cases

- What happens when a reconcile succeeds but the subsequent status write fails (e.g., due to a conflict) — is the reconcile considered complete, retried, or does it re-run the reconcile logic from scratch?
- How does the system handle a resource that is deleted while its reconcile is in flight?
- How does the system handle a burst of many resources changing simultaneously right after reconnect (replay flood) — does it process them without overwhelming workers?
- What happens if the operations runbook's suggested recovery action (e.g., requeue) is taken while the underlying root cause is still present — does the system avoid an infinite retry storm?
- How does the system distinguish a genuinely poisoned item (bad data, permanent error) from one that is failing due to a longer transient outage (e.g., a dependent service down for several minutes)?

## Requirements *(mandatory)*

### Functional Requirements

- **FR-001**: The test suite MUST cover successful reconciliation of a resource on first attempt, verifying final status reflects success.
- **FR-002**: The test suite MUST cover reconciliation that fails transiently and succeeds after retry, verifying retry counts and eventual success status.
- **FR-003**: The test suite MUST cover controller restart and resume-from-checkpoint, verifying no pending change is lost and no already-completed reconciliation is unnecessarily repeated.
- **FR-004**: The test suite MUST cover concurrent status-update conflicts, verifying stale writes are rejected and the controller re-reconciles against the current resource state.
- **FR-005**: The test suite MUST cover watch disconnect and reconnect, verifying changes made during the disconnect window are reconciled exactly once after reconnect.
- **FR-006**: The test suite MUST cover the replay-window-exceeded case, verifying the controller falls back to a full list-then-watch bootstrap when its checkpoint can no longer be replayed.
- **FR-007**: The test suite MUST cover a queue item that repeatedly fails reconciliation past the configured retry limit ("poisoned" item), verifying it is surfaced as a terminal failure rather than retried indefinitely.
- **FR-008**: A runbook MUST be authored documenting how to diagnose and recover from controller lag (queue depth growing / reconcile falling behind), including which metrics/logs to check and what remediation steps are available.
- **FR-009**: A runbook MUST be authored documenting how to diagnose and recover from a replay-window-exceeded condition, including how to confirm the automatic fallback occurred and how to verify recovery.
- **FR-010**: A runbook MUST be authored documenting how to diagnose and handle a poisoned queue item, including how to identify the offending resource, inspect the failure reason, and choose between fix-and-requeue or quarantine/skip.
- **FR-011**: Each runbook MUST reference the specific metrics, log fields, or status conditions an engineer should inspect, and those signals MUST be validated (via test or manual check) to actually be emitted by the controller manager runtime.
- **FR-012**: The test suite MUST be runnable in CI without dependency on a live ScyllaDB or external network service, using the in-memory/fake datastore already established by prior controller manager specs.
- **FR-013**: Test scenarios MUST assert on externally observable outcomes (resource status, conditions, metrics, logs) rather than internal implementation state, so tests remain valid across internal refactors.

### Key Entities

- **Reconcile Outcome**: The result of a single reconciliation attempt for a resource — success, transient failure (retryable), or terminal failure (poisoned) — including attempt count and error detail.
- **Checkpoint**: The persisted resourceVersion (or equivalent cursor) per resource kind marking the controller's last-observed position in the change stream; used to resume after restart or reconnect.
- **Status Condition**: A structured, resource-level status entry (e.g., `Ready`, `ParentResolved`) written by a controller after reconciliation, subject to conflict detection on concurrent writes.
- **Runbook**: A documented procedure mapping an observable operational symptom to diagnostic steps (which signals to check) and recovery actions, intended for on-call use without prior controller-internals knowledge.
- **Observability Signal**: A metric, log line, or status condition emitted by the controller manager that a runbook or test relies on to detect or confirm a given state (e.g., checkpoint age, queue depth, retry count).

## Success Criteria *(mandatory)*

### Measurable Outcomes

- **SC-001**: 100% of the core reconcile scenarios (first-attempt success, retry-then-success, restart-and-resume, status conflict, disconnect/reconnect, replay-window-exceeded, poisoned item) have automated integration test coverage that passes reliably in CI.
- **SC-002**: An engineer unfamiliar with the controller manager's internals can follow each of the three runbooks (lag, replay-window-exceeded, poisoned item) to correctly diagnose an induced instance of that failure using only exposed metrics/logs, without reading source code.
- **SC-003**: Every signal referenced by a runbook is confirmed present and accurate by at least one automated test or documented manual verification step, with zero "aspirational" (unverified) references remaining.
- **SC-004**: The full integration test suite for these scenarios completes in under 5 minutes in CI, enabling it to run on every pull request touching the controller manager.
- **SC-005**: Zero regressions are introduced to the reconcile/retry/resume behavior of specs 025, 026, and 036 as measured by this suite passing against the current merged state of those specs.
