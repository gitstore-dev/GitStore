# Feature Specification: Controller Manager Runtime Foundations

**Feature Branch**: `025-controller-manager-runtime`  
**Created**: 2026-06-11  
**Status**: Draft  
**Input**: User description: "Starting from issue #165 we build the infrastructure for controllers. Traverse the dependency graph and build a memory of related specs. This is the first one"

## Clarifications

### Session 2026-06-11

- Q: Should the controller manager live in `gitstore-api` or a separate module? → A: Separate `gitstore-controller-manager` module — independent binary, clean boundary, mirrors k8s pattern.
- Q: Where does the local cache live that reconcilers read from? → A: Cache owned inside `gitstore-controller-manager` — informers populate it per kind, reconcilers read from it, fall back to live API call on cache miss.
- Q: How are poison items recovered? → A: Explicit re-queue via health surface — operator targets specific poison items for retry without restarting the manager.

## User Scenarios & Testing *(mandatory)*

### User Story 1 - Controller Authors Register Reconcilers (Priority: P1)

A platform engineer writing a new controller implements a reconciler for their target resource kind and registers it with the controller manager so that work items are automatically routed to their reconciler, retried on transient failure, and quarantined only when exhausted.

**Why this priority**: The ability to register and invoke a reconciler is the minimum viable unit of the entire controller infrastructure. Nothing else (retry, observability, idempotency) matters until a reconciler can be wired and invoked.

**Independent Test**: Can be fully tested by registering a no-op reconciler for a synthetic resource kind, enqueuing a work item, and confirming the reconciler is invoked exactly once per item under normal conditions.

**Acceptance Scenarios**:

1. **Given** the controller manager is running, **When** a reconciler is registered for resource kind `Product`, **Then** all incoming work items for `Product` are dispatched exclusively to that reconciler.
2. **Given** a reconciler is registered, **When** a work item is enqueued, **Then** the reconciler is invoked with the item's identity and reports success or failure back to the queue.
3. **Given** two reconcilers registered for different kinds, **When** work items arrive for each kind, **Then** each item is dispatched only to the matching reconciler without cross-contamination.

---

### User Story 2 - Operators Observe Retry and Backoff Behavior (Priority: P2)

A platform operator monitors controller throughput and sees that failed work items are retried with increasing delay, that items exceeding the retry limit are quarantined as poison items, and that the queue never stalls due to a single bad item.

**Why this priority**: Without bounded retry and poison-item quarantine, a single bad manifest can stall reconciliation for all resources of the same kind. This is the critical resilience requirement.

**Independent Test**: Can be fully tested by injecting a reconciler that always fails for one specific item, observing successive retries with growing delay, and verifying the item is moved to a dead-letter state after the retry limit is reached while other items continue processing.

**Acceptance Scenarios**:

1. **Given** a reconciler returns a transient failure, **When** the retry budget is not exhausted, **Then** the item is re-enqueued after an increasing delay.
2. **Given** a work item has failed the maximum number of allowed retries, **When** the retry limit is exceeded, **Then** the item is quarantined as a poison item and no longer blocks queue progress.
3. **Given** a poison item exists in the queue, **When** new work items arrive for the same kind, **Then** they are processed normally and not delayed by the quarantined item.
4. **Given** the controller manager is running, **When** an item enters the retry cycle, **Then** an observable signal is emitted recording the retry attempt, delay, and reason.

---

### User Story 3 - Reconcilers Apply Level-Triggered Logic (Priority: P3)

A controller author implements their reconciler to always read current resource state from the cache at reconcile time rather than trusting the event payload, so that replayed or coalesced work items produce the same outcome as first-time deliveries.

**Why this priority**: Level-triggered reconciliation is the safety guarantee that makes retry and replay safe. It means a reconciler never acts on stale event data — it always observes what is true now. This is required for safe reconnect/resume semantics (issue #131).

**Independent Test**: Can be fully tested by simulating a crash mid-reconcile, restarting the controller manager, and confirming the replayed item drives the resource to its correct desired state without duplicate side effects — proving the reconciler read live state rather than relying on the original event payload.

**Acceptance Scenarios**:

1. **Given** a work item is re-delivered after a crash, **When** the reconciler runs, **Then** it reads current resource state from the cache and not from the original event payload.
2. **Given** multiple events for the same resource are coalesced into one work item, **When** the reconciler runs, **Then** it produces the same outcome as if it had processed each event individually.
3. **Given** a resource has been updated between enqueue time and dispatch time, **When** the reconciler runs, **Then** it acts on the latest observed state and reaches the correct desired state.

---

### User Story 4 - Operators Inspect Controller Manager Health (Priority: P4)

A platform operator queries the controller manager's health surface and can see how many workers are active per kind, how many items are queued, and whether any worker has stalled beyond the expected processing window.

**Why this priority**: Operational visibility is a hygiene requirement for production deployments but does not block early feature development.

**Independent Test**: Can be fully tested by starting the controller manager, enqueuing a batch of work items, and querying the health surface to confirm per-kind queue depth, active workers, and last-processed timestamp are all visible.

**Acceptance Scenarios**:

1. **Given** the controller manager is running with workers, **When** the health surface is queried, **Then** the response includes per-kind worker count, queue depth, and last-successful-reconcile timestamp.
2. **Given** a worker has not completed a reconcile within the expected window, **When** the health surface is queried, **Then** the stalled worker is flagged distinctly from healthy workers.
3. **Given** all queues are empty and all workers are idle, **When** the health surface is queried, **Then** the response reflects a fully idle, healthy state.

---

### Edge Cases

- What happens when a work item is enqueued for a kind that has no registered reconciler?
- How does the system handle a reconciler that panics or crashes mid-execution rather than returning an error?
- What happens when the work queue reaches its capacity limit before items are consumed?
- How does the system behave when the same item is enqueued multiple times before its first dispatch (deduplication)?
- What happens when a work item's resource is deleted between enqueue time and dispatch time?
- How does the manager recover if it crashes with items in-flight (neither committed nor quarantined)?

## Requirements *(mandatory)*

### Functional Requirements

- **FR-001**: The system MUST route work items to the reconciler registered for the matching resource kind.
- **FR-002**: The system MUST retry a failed work item at least once before quarantining it, applying an increasing delay between attempts.
- **FR-003**: The system MUST quarantine a work item as a poison item after it exceeds the configured maximum retry count, without blocking the processing of other items.
- **FR-003a**: The health surface MUST expose quarantined poison items per kind and allow an operator to re-queue a specific poison item for a fresh retry attempt without restarting the controller manager.
- **FR-004**: The system MUST emit an observable signal for every retry attempt, including the attempt number, delay applied, and failure reason.
- **FR-005**: The system MUST enforce level-triggered reconciliation: reconcilers MUST read current resource state from the informer cache at dispatch time, not from the original event payload; a live API call is made only on a cache miss.
- **FR-006**: The system MUST expose a health surface showing per-kind worker count, queue depth, quarantined poison item count, and last-successful-reconcile timestamp.
- **FR-007**: The system MUST flag workers that have not completed a reconcile within a configurable stall threshold.
- **FR-008**: The system MUST deduplicate work items so that multiple enqueue calls for the same item identity within the same cycle result in a single dispatch.
- **FR-009**: The system MUST reject enqueue requests for resource kinds with no registered reconciler and emit a signal indicating the missing registration.
- **FR-010**: The system MUST allow the maximum retry count and backoff parameters to be configured per kind without restarting the manager.

### Key Entities

- **Work Item**: A unit of reconciliation work identified by resource kind, namespace, and name; carries the resource version at enqueue time and retry metadata.
- **Reconciler**: A registered implementation for a specific resource kind; reads current state from the informer cache, drives the resource toward desired state, and returns a success, transient failure, or terminal failure result.
- **Informer**: A per-kind component that maintains a local in-memory cache of resource state, populated from the Watch stream; the authoritative read source for reconcilers inside `gitstore-controller-manager`.
- **Controller**: The composed unit of a reconciler wired to an informer cache, a work queue, and a worker pool for a specific resource kind; managed by the controller manager.
- **Worker Pool**: The set of concurrent workers allocated to a resource kind; bounded in size and supervised by the manager.
- **Retry Record**: A tracked history of attempts for a work item, including timestamps, delays, and failure reasons.
- **Poison Item**: A work item that has exceeded its retry budget; moved to a quarantine state and excluded from normal queue processing.
- **Health Surface**: A queryable interface exposing runtime metrics for each registered kind and its associated workers.

## Success Criteria *(mandatory)*

### Measurable Outcomes

- **SC-001**: A work item for a registered kind is dispatched to its reconciler within 100 milliseconds of being enqueued under a non-saturated queue.
- **SC-002**: A poison item (retry-exhausted) never delays processing of any other item for the same or different kind.
- **SC-003**: The controller manager continues processing all non-affected kinds when one kind's reconciler is continuously failing.
- **SC-004**: The health surface reflects the actual queue state within 5 seconds of a change in worker count or queue depth.
- **SC-005**: After a controller manager restart, all in-progress and pending items are replayed and reach a terminal state (success or poison) without manual intervention.
- **SC-006**: Duplicate enqueue calls for the same item within one processing cycle result in exactly one reconciler invocation.

## Assumptions

- The Watch API (issue #131) provides the source of work items for controllers; this spec covers only what happens after an item enters the manager's queue.
- The controller manager runs as a separate `gitstore-controller-manager` module/binary, independent from `gitstore-api`; multi-node distribution is out of scope for this spec.
- Retry configuration (max attempts, backoff multiplier, backoff ceiling) has sensible defaults that cover most reconcile scenarios; per-kind override is the extension point.
- Poison items are logged and observable via the health surface; an operator explicitly re-queues specific items for retry through the health surface without restarting the manager. Poison items are not retried automatically.
- Reconcilers are responsible for reading current state from the cache at dispatch time (level-triggered); the controller manager does not verify this at runtime.

## Dependencies

- **Issue #165** (parent initiative): Controller Manager Reconciliation Loop for Core Resources and CRDs. This spec implements the first sub-issue in its plan.
- **Issue #180** (this feature): Controller Manager Runtime — Queueing, Workers, Retry/Backoff, Idempotency. Must be complete before #181, #182, or #183 can proceed.
- **Issue #131** (upstream initiative): Watch API with `resourceVersion` resume — provides the event stream that produces work items for the controller manager.
- **Issue #139** (upstream initiative): gRPC GitEvent Notification Stream — upstream trigger that feeds the Watch API.

### Sub-issues of #165 (implementation plan)

| # | Title | Blocked by |
|---|-------|-----------|
| #180 | Controller Manager Runtime: Queueing, Workers, Retry/Backoff, Idempotency | — |
| #181 | Reconcile Handler Contract for Core + CRD Kinds | #180 |
| #182 | Controller Startup Resume: List-Then-Watch and resourceVersion Checkpointing | #180 |
| #183 | Controller Integration Tests + Operations Runbook | #181, #182 |

**This spec (#180) also blocks #244** — CategoryTaxonomy Controller Reconciliation: Status Computation and File Reference Conditions.
