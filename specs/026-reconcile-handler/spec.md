# Feature Specification: Reconcile Handler Contract for Core and CRD Kinds

**Feature Branch**: `026-reconcile-handler`  
**Created**: 2026-06-12  
**Status**: Closed  
**Input**: User description: "next spec" (initiative #165 sub-issue #181 — Reconcile Handler Contract for Core + CRD Kinds)

## Clarifications

### Session 2026-06-12

- Q: What conflict-detection mechanism should the status-update patch use? → A: Optimistic concurrency — the status patch includes the current `resourceVersion`; the API rejects the patch if the token doesn't match, and the reconciler propagates the conflict as a `TransientFailure`.
- Q: When should a reconciler choose `TerminalFailure` vs `TransientFailure`? → A: `TerminalFailure` is for unrecoverable resource-level errors (invalid spec, unresolvable reference — cases where no retry would help); all transient or infrastructure errors (API timeout, conflict, cache miss) use `TransientFailure`.
- Q: Which generation field is canonical for feedback-loop suppression (FR-008)? → A: `metadata.generation`, incremented by the API on every spec write; reconcilers write `status.observedGeneration` to match it after a successful reconcile, and status-only updates that do not change `metadata.generation` MUST NOT re-enqueue the resource.
- Q: How should reconciler panics be surfaced to operators? → A: Structured log entry at ERROR level (including kind, namespace, name, and stack trace) plus the existing `gitstore_controller_reconcile_total{result="transient_failure"}` metric counter incremented — no separate panic counter.
- Q: Should `StatusPatch` use partial merge or full replace semantics? → A: Partial merge — only fields explicitly included in the patch are written; other status fields are left unchanged, allowing multiple reconcilers to own distinct status sub-fields without coupling.

## User Scenarios & Testing *(mandatory)*

### User Story 1 - Controller Author Implements a Core-Kind Reconciler (Priority: P1)

A platform engineer writing the first real reconciler for a built-in GitStore resource (e.g. `CategoryTaxonomy`) implements the reconciler interface, reads current resource state from the informer cache, performs the required side-effect (computing materialized ancestors, resolving cycles), and returns a result that tells the manager whether to succeed, retry, or re-queue after a delay.

**Why this priority**: The reconciler interface contract is the primary extension point for all controller work. Until the interface is stable and tested, no domain logic can be safely written against it. All other user stories depend on at least one working reconciler.

**Independent Test**: Can be fully tested by writing a `CategoryTaxonomy` reconciler stub that reads state from the cache, simulates a side-effect, and asserts that each return result (success, transient failure, re-queue with delay) is handled correctly by the controller manager from spec 025 — without requiring a live API or persistence layer.

**Acceptance Scenarios**:

1. **Given** a core-kind reconciler is implemented and registered, **When** a work item for that kind arrives, **Then** the reconciler is invoked with the item's identity and has access to the current resource state from the informer cache.
2. **Given** the reconciler returns a success result, **When** the dispatch completes, **Then** the work item is removed from the queue and the last-success timestamp is updated.
3. **Given** the reconciler returns a transient failure, **When** the result is processed, **Then** the item enters the retry cycle defined in spec 025 with the reconciler's optionally supplied backoff hint.
4. **Given** the reconciler returns a re-queue-after result with a specific duration, **When** that duration elapses, **Then** the item is re-enqueued exactly once regardless of how many other events arrived for the same resource in the interim.
5. **Given** the reconciler detects an unrecoverable resource-level error (e.g. an unresolvable reference or a logically invalid spec) and returns `TerminalFailure`, **When** the result is processed, **Then** the item is quarantined immediately without consuming any retry budget, and the failure reason is recorded on the poison item.

---

### User Story 2 - Controller Author Registers a CRD-Kind Reconciler (Priority: P2)

A platform engineer extending GitStore with a custom resource kind (defined via a CRD) registers a reconciler for that kind at startup, following the same interface contract used by core kinds. The manager routes work items for that CRD kind to the registered reconciler without requiring changes to the controller manager binary.

**Why this priority**: CRD kinds are the primary extensibility mechanism of the GitStore platform. A uniform contract shared by core and CRD reconcilers eliminates divergent code paths and lets the runtime remain kind-agnostic. This is the second most critical requirement after P1.

**Independent Test**: Can be fully tested by defining a synthetic CRD kind at startup, registering a reconciler for it using the same interface as core kinds, enqueueing a work item, and asserting the reconciler is invoked and returns a correct result — with no core-kind code paths involved.

**Acceptance Scenarios**:

1. **Given** a CRD kind `BackfillJob` is defined and a reconciler is registered for it, **When** a work item for `BackfillJob` arrives, **Then** it is dispatched to the CRD reconciler using the same dispatch path as core kinds.
2. **Given** the same reconciler interface is implemented for both a core kind and a CRD kind, **When** both are registered and work items arrive, **Then** neither requires kind-specific handling in the controller manager.
3. **Given** a CRD reconciler is not yet registered but a work item for that kind arrives, **When** the manager processes the item, **Then** it emits a "kind not registered" signal and does not panic or corrupt queue state.

---

### User Story 3 - Reconciler Reads Status and Writes Back a Status Update (Priority: P3)

A controller author's reconciler reads the current `.status` of a resource from the cache, computes new status fields (e.g. `Ready`, `AncestorPath`, `ObservedGeneration`), and applies a status patch so that API consumers can observe the resource's reconciled state.

**Why this priority**: Status writeback is the primary observable output of reconciliation. Without it, controllers are invisible to users and other controllers. It is however a higher-level concern than the basic dispatch contract and can be tested independently.

**Independent Test**: Can be fully tested by writing a reconciler that reads `.status.ready = false`, computes `.status.ready = true`, and asserts the status-update call is issued exactly once — independently of the queue or retry machinery.

**Acceptance Scenarios**:

1. **Given** a reconciler reads a resource with `.status.ready = false` from the cache, **When** reconciliation completes successfully, **Then** the reconciler applies a status patch setting `.status.ready = true` and `.status.observedGeneration` to the value of `metadata.generation` observed at reconcile time.
2. **Given** the reconciler applies a status patch with the `resourceVersion` it observed, **When** the resource has been concurrently updated and the stored `resourceVersion` no longer matches, **Then** the API rejects the patch and the reconciler receives a conflict error, which it returns as a `TransientFailure` so the item is retried with the refreshed state.
3. **Given** a reconciler completes with no change to the desired state, **When** the current status already reflects the desired state, **Then** the reconciler MUST NOT issue a no-op status patch (idempotent short-circuit).

---

### User Story 4 - Operator Observes Per-Kind Reconciler Registration at Startup (Priority: P4)

A platform operator starting the controller manager can query which reconcilers are registered and for which kinds, so they can verify that all expected controllers are active before the manager begins processing work items.

**Why this priority**: Startup visibility is an operational hygiene requirement. Missing reconciler registrations are a silent misconfiguration risk in production; surfacing them at startup reduces incident mean-time-to-detect.

**Independent Test**: Can be fully tested by starting the controller manager with a fixed set of registered reconcilers and querying the health surface (from spec 025) immediately after startup, asserting that all registered kinds are listed and no unregistered kinds appear.

**Acceptance Scenarios**:

1. **Given** the controller manager starts with reconcilers registered for `CategoryTaxonomy` and `Collection`, **When** the health surface is queried, **Then** both kinds are listed with a `registered` status.
2. **Given** a reconciler registration fails at startup (e.g. duplicate kind), **When** the manager starts, **Then** it rejects the duplicate registration with a clear error and halts startup rather than running with an ambiguous configuration.
3. **Given** the manager is running and a new kind is registered at runtime (hot-registration), **When** the health surface is queried, **Then** the new kind appears immediately without restarting the manager.

---

### Edge Cases

- What happens when a reconciler panics during execution rather than returning an error? → The goroutine is recovered, a structured ERROR log with stack trace is emitted, the existing reconcile-failure metric is incremented, and the item enters the normal `TransientFailure` retry cycle (FR-004).
- How does the system behave when the informer cache has not yet synced for a kind and the reconciler attempts a read?
- What is the correct behavior when a reconciler reads a resource from the cache but finds it absent (the resource was deleted between enqueue and dispatch)?
- How are circular reconcile loops prevented — a reconciler writing status triggers an event that re-enqueues the same item?
- What happens when a CRD schema is removed while its reconciler is registered and work items are in-flight?
- How does a status-patch conflict interact with the retry budget — does a conflict count as a retry attempt?

## Requirements *(mandatory)*

### Functional Requirements

- **FR-001**: The system MUST define a single reconciler interface implemented identically by both core-kind and CRD-kind reconcilers; the controller manager MUST NOT have kind-specific dispatch branches.
- **FR-002**: The reconciler interface MUST accept the work item identity (kind, namespace, name) and a read-only cache accessor for that kind; it MUST NOT receive the original event payload.
- **FR-003**: The reconciler MUST be able to return one of four result states: `Success`, `TransientFailure` (retry — used for all transient or infrastructure errors such as API timeouts, conflicts, or cache misses), `TerminalFailure` (quarantine immediately — used only for unrecoverable resource-level errors where no retry would help, such as an invalid spec or an unresolvable reference), and `RequeueAfter(duration)` (re-enqueue after a specified delay).
- **FR-004**: The system MUST treat a reconciler panic as a `TransientFailure` — it MUST recover the goroutine, emit a structured ERROR log entry containing the kind, namespace, name, and full stack trace, increment the existing `gitstore_controller_reconcile_total{result="transient_failure"}` metric, record the panic message as the failure reason on the retry record, and apply the normal retry policy without terminating the manager.
- **FR-005**: The system MUST allow reconcilers to read current resource state from the per-kind informer cache; if the resource is absent from the cache, the reconciler receives a `NotFound` indicator and MUST treat deletion as a terminal condition requiring no retry.
- **FR-006**: The system MUST provide a status-update mechanism that reconcilers use to write computed `.status` fields back to the resource; the patch MUST include the `resourceVersion` observed at reconcile time and the API MUST reject patches whose `resourceVersion` does not match the current stored version; a rejected patch MUST be returned to the reconciler as a conflict error to propagate as a `TransientFailure`.
- **FR-007**: The system MUST suppress a status patch when every field explicitly included in the patch is identical to its current observed value (idempotent write suppression using partial-merge semantics); zero redundant write calls under steady-state operation.
- **FR-008**: The system MUST prevent reconcile feedback loops: a status-only update MUST NOT re-enqueue the same resource unless `metadata.generation` has changed since the last reconcile; the API MUST increment `metadata.generation` on every spec write and MUST NOT increment it on status-only writes; reconcilers MUST write `status.observedGeneration` equal to the `metadata.generation` observed at reconcile time upon successful completion.
- **FR-009**: The system MUST allow reconcilers to be registered for CRD kinds using the same registration API as core kinds; the kind name is the sole discriminator.
- **FR-010**: The system MUST reject duplicate reconciler registrations for the same kind and halt startup with a descriptive error identifying the conflicting registration.
- **FR-011**: The system MUST expose the list of registered kinds and their registration status via the health surface introduced in spec 025.
- **FR-012**: The system MUST support hot-registration of reconcilers after startup; newly registered kinds appear in the health surface immediately and begin receiving work items.
- **FR-013**: When the informer cache for a kind has not yet completed its initial sync, the system MUST hold dispatch for that kind until the cache reports synced; it MUST NOT invoke reconcilers against a partially-populated cache.

### Key Entities

- **ReconcilerInterface**: The contract a reconciler must satisfy — accepts a work item identity and a cache accessor, returns a `ReconcileResult`.
- **ReconcileResult**: The outcome of a reconcile invocation; one of `Success`, `TransientFailure`, `TerminalFailure`, or `RequeueAfter(duration)`.
- **CacheAccessor**: A read-only view of the per-kind informer cache exposed to a reconciler; returns the current resource state or a `NotFound` indicator.
- **StatusPatch**: A partial-merge update applied to a resource's `.status` sub-resource; only fields explicitly included in the patch are written, leaving all other status fields unchanged. The patch must include the current `resourceVersion` for optimistic-concurrency conflict detection.
- **ReconcilerRegistry**: The component that holds the mapping of kind → reconciler; consulted by the dispatch loop before invoking any handler.
- **CoreKind**: A built-in GitStore resource kind (`CategoryTaxonomy`, `Collection`, `Product`, `Namespace`, `Repository`) whose reconciler is bundled with the controller manager binary.
- **CRDKind**: A custom resource kind defined via a CRD manifest; its reconciler follows the same interface as core kinds and is registered at startup or at runtime.

## Success Criteria *(mandatory)*

### Measurable Outcomes

- **SC-001**: Every reconciler invocation completes in under 500 milliseconds for a resource whose desired and observed states are already aligned (no-op reconcile path).
- **SC-002**: A reconciler panic is recovered within one dispatch cycle and the item is re-queued without restarting the controller manager; zero manager crashes attributable to reconciler code.
- **SC-003**: A `TerminalFailure` result quarantines the work item in the same dispatch cycle it is returned, with no additional retry attempts consuming the retry budget.
- **SC-004**: Status patches for resources whose status is already up to date are suppressed in 100% of cases — zero redundant write calls observed under steady-state operation.
- **SC-005**: Hot-registration of a new CRD kind makes the kind dispatchable within 1 second of registration without restarting the manager.
- **SC-006**: At startup, all reconciler registration errors are reported before any work item is dispatched; the manager never enters a partially-initialized dispatch state.
- **SC-007**: Reconcilers for CRD kinds achieve the same dispatch latency, retry behavior, and health-surface coverage as reconcilers for core kinds — no measurable operational difference between the two.

## Assumptions

- The informer cache interface and `HasSynced()` semantics are as implemented in spec 025; this spec extends that contract without modifying the cache internals.
- Status sub-resource updates are directed at `gitstore-api`; this spec defines what the reconciler calls and what is returned, not the underlying network transport.
- CRD schema validation and versioning are handled by initiative #164; this spec assumes a CRD kind name is already known at reconciler registration time and does not cover schema parsing or version migration.
- The Watch event stream (issue #131) is responsible for populating the informer cache and enqueuing work items; this spec covers only what happens once an item is dispatched to a registered reconciler.
- Reconcile feedback-loop prevention (FR-008) relies on `metadata.generation` (incremented by the API on every spec write, never on status writes) and `status.observedGeneration` (written by the reconciler after a successful reconcile). All GitStore resource kinds managed by a controller MUST carry both fields.
- Hot-registration is additive only; de-registering a live reconciler while it has in-flight work items is out of scope for this spec.

## Dependencies

- **Spec 025** (`025-controller-manager-runtime`): Provides the work queue, worker pool, retry engine, informer cache interface, and health surface. This spec extends those foundations. ✅ Merged 2026-06-12.
- **Issue #181** (this feature): Reconcile Handler Contract for Core + CRD Kinds. Sub-issue of initiative #165.
- **Issue #149** (upstream): Dynamic GraphQL Schema Synthesis — required for CRD kinds to have queryable schema at runtime. CRD reconciler registration is limited to statically-known kind names until #149 lands.
- **Issue #164** (upstream): Hub-and-Spoke CRD Versioning — defines the CRD manifest format and version resolution semantics.
- **Issue #244** (downstream): CategoryTaxonomy Controller Reconciliation: Status Computation and File Reference Conditions — the first concrete implementation of the reconciler interface defined by this spec.

### Sub-issues of #165 (updated status)

| #    | Title                                                                           | Blocked by   | Status       |
|------|---------------------------------------------------------------------------------|--------------|--------------|
| #180 | Controller Manager Runtime: Queueing, Workers, Retry/Backoff, Idempotency      | —            | ✅ Closed    |
| #181 | Reconcile Handler Contract for Core + CRD Kinds                                | #180         | 🔵 This spec |
| #182 | Controller Startup Resume: List-Then-Watch and resourceVersion Checkpointing   | #180         | ⬜ Open      |
| #183 | Controller Integration Tests + Operations Runbook                               | #181, #182   | ⬜ Open      |
