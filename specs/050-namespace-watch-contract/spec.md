# Feature Specification: Namespace Watch Contract: Events and resourceVersion Resume

**Feature Branch**: `050-namespace-watch-contract`
**Created**: 2026-08-19
**Status**: Draft
**Input**: User description: "Namespace Watch Contract: Events and resourceVersion Resume (GH#174). Define namespace watch semantics for controllers and clients, including event model and resourceVersion resume behavior. Scope: define namespace watch event shapes (ADDED, MODIFIED, DELETED); define list-then-watch resume semantics using resourceVersion; define ordering and replay guarantees for namespace streams; provide subscription and query examples for namespace watch consumers. Parent initiative: #170. Blocked by GH#172 (shipped as spec 046). Related: #166 (shipped as spec 040)."

## Clarifications

### Session 2026-08-19

- Q: GH#174's own "Open Questions" section asks about a "Namespace Controller" that watches Namespace, handles deletion, removes all namespaced resources when a namespace enters Terminating, and clears finalizers once everything inside is gone — is that in scope here? → A: No. That assumption is stale. Spec 046 (already merged) implemented the real Namespace reconciler (`gitstore-controller-manager/internal/namespace/reconciler.go`), which clears the foreground-deletion finalizer only once zero repositories remain — it polls and rejects-until-empty, it does not cascade-delete dependents. That design question is already resolved by spec 046 and is explicitly out of scope for this spec, which covers only the watch/event contract for observing Namespace state (including observing `Terminating` and finalizer changes already emitted by that reconciler), not deletion/cascade mechanics.
- Q: Is the watch/event mechanism for Namespace new work, or does something already exist? → A: It already exists end-to-end. As a byproduct of shipping spec 040 (generic `watchResources(kind:)` mechanism) and spec 046 (Namespace lifecycle, finalizers, status conditions), the full chain is already implemented and wired: the API publishes Added/Modified/Deleted events for Namespace create/update/delete/status-write/finalizer-clear operations onto a per-kind, bounded, resumable event bus; a generic `watchResources` subscription resolver serves those events with a Namespace-specific bootstrap-cursor protocol for race-free list-then-watch establishment; and a reference consumer (`NamespaceListWatcher` in `gitstore-controller-manager`) already implements List-then-Watch against it, including expired-cursor handling. This spec formalizes and documents that real, existing contract — and closes one verified test-coverage gap — rather than designing a new mechanism.

## User Scenarios & Testing *(mandatory)*

### User Story 1 - A controller or client establishes an accurate, current view of every Namespace and keeps it live (Priority: P1)

A controller (or any GraphQL client) that needs to react to Namespace changes — creation, spec updates, status/condition changes, entering `Terminating`, or final removal — must be able to fetch the complete current set of namespaces once, then receive every subsequent change as an ordered stream, without missing a change that happens in the gap between the initial fetch and the stream starting.

**Why this priority**: This is the foundational capability the rest of the contract depends on. Without a race-free list-then-watch establishment, a consumer's in-memory view of Namespace state can silently diverge from reality, and every downstream reconciliation decision becomes unreliable.

**Independent Test**: Can be fully tested by starting a watch with no prior cursor, confirming a resumable starting point is delivered before (or atomically with) the initial full list, then creating, updating, and deleting a namespace and confirming each change arrives on the stream exactly once, in the order it was admitted.

**Acceptance Scenarios**:

1. **Given** a consumer with no prior cursor, **When** it begins observing Namespace state for the first time, **Then** it receives a resumable starting point together with the complete current set of namespaces, with no gap in which a change could occur unobserved between the two.
2. **Given** an open watch on Namespace, **When** a namespace is created, **Then** an `ADDED` event carrying the namespace's full current state (including its finalizers and status conditions) is delivered.
3. **Given** an open watch on Namespace, **When** an existing namespace's spec, status, or finalizer list changes (including entering or leaving `Terminating`), **Then** a `MODIFIED` event carrying the namespace's full current state at that point is delivered.
4. **Given** an open watch on Namespace, **When** a namespace's record is finally and permanently removed (the foreground-deletion finalizer is cleared and the record deleted), **Then** a `DELETED` event carrying the namespace's identity and last-known `resourceVersion` (not necessarily its full prior state) is delivered.
5. **Given** a consumer holding a previously issued `resourceVersion` cursor, **When** it resumes observing from that cursor, **Then** it receives exactly the changes that occurred after that cursor, in the order they were admitted, with no redelivery of unchanged namespaces and no gap.

---

### User Story 2 - A consumer can tell the difference between "caught up" and "must start over" (Priority: P1)

A controller that persisted a `resourceVersion` cursor before restarting needs to resume from exactly where it left off. If too much time has passed and the system can no longer reconstruct the missed changes from that cursor, the consumer needs an unambiguous signal that it must discard its cursor and re-establish its view from scratch, rather than silently believing it is caught up when it is not.

**Why this priority**: Equal in importance to User Story 1 — a watch mechanism that cannot distinguish "resumed successfully" from "resumed into a gap" is unsafe to build any reconciliation logic on top of, since a reconciler could operate on a stale view indefinitely without knowing it.

**Independent Test**: Can be fully tested by presenting a cursor that is known to be older than the retained change history and confirming a distinct, unambiguous "cursor expired" signal is returned — as opposed to an empty or normal resumed stream — and that presenting a still-valid cursor never produces this signal.

**Acceptance Scenarios**:

1. **Given** a `resourceVersion` cursor that is still within the retained change history, **When** a consumer resumes a watch using it, **Then** the resume succeeds and delivers only the changes after that cursor.
2. **Given** a `resourceVersion` cursor that is no longer within the retained change history, **When** a consumer resumes a watch using it, **Then** the consumer receives a distinguishable "cursor expired" signal, not a normal (possibly empty) resumed stream.
3. **Given** a consumer that receives the "cursor expired" signal, **When** it reacts by re-establishing its view from scratch (per User Story 1), **Then** it ends up with an accurate current view with no reliance on the expired cursor.

---

### User Story 3 - A documentation reader can implement a correct Namespace watch consumer without reading server source code (Priority: P2)

Someone building a new controller or integration against Namespace watch needs enough documented information — event shapes with concrete example payloads, the list-then-watch establishment sequence, and the resume/expiry contract — to implement a correct consumer without having to read and reverse-engineer the server implementation.

**Why this priority**: Lower urgency than User Stories 1–2 because the underlying behavior already exists and already works; this user story is about making that behavior discoverable and reproducible for the next consumer, rather than about changing behavior.

**Independent Test**: Can be fully tested by having someone unfamiliar with the implementation follow only the documented contract and example payloads/examples to build a minimal consumer that correctly lists, watches, resumes, and reacts to an expired cursor for Namespace.

**Acceptance Scenarios**:

1. **Given** the documented event contract, **When** a reader inspects it, **Then** they can identify, for each of `ADDED`, `MODIFIED`, and `DELETED`, exactly which fields are present and which are absent, illustrated with concrete example payloads.
2. **Given** the documented list-then-watch and resume contract, **When** a reader inspects it, **Then** they can identify the exact sequence of steps required to establish a race-free initial view and to resume after a restart, illustrated with example subscription and query calls.
3. **Given** the documented ordering and delivery guarantees, **When** a reader inspects it, **Then** they can correctly state whether the mechanism guarantees exactly-once or at-least-once delivery, and what ordering is (and is not) guaranteed.

---

### Edge Cases

- What happens when a watch is opened for Namespace and zero namespaces currently exist (a fresh environment, before bootstrap namespaces are provisioned)? The initial view is empty and the stream then delivers only future changes; this is not treated as an error, and the two bootstrap namespaces (`gitstore-system`, `default`) — created directly at API startup with no admitted Git commit — still each produce their own lifecycle of watch events once they are created.
- What happens when a namespace enters `Terminating` (its foreground-deletion finalizer is attached) and then, later, the finalizer is cleared and the record is removed? These are two distinct, separately observable events: a `MODIFIED` event when the finalizer/Terminating state is attached, and a final `DELETED` event only once the record is actually removed — never a single event conflating both transitions.
- What happens when a resumed watch's cursor points to a namespace that has since been fully removed? The consumer still receives the `DELETED` event for that namespace (if it falls after the resume cursor) — removal from the resource's own history is itself a delivered change, not a silent omission.
- What happens when a consumer observes the same change more than once (e.g., after a reconnect near a delivery boundary)? Consumers must treat delivery as at-least-once and idempotent per resource, consistent with the existing reconciliation model — observing the same `MODIFIED` event twice must not corrupt a consumer's state.
- What happens when a consumer expects a `deletionTimestamp`-style field to detect `Terminating`? No such field is present in the documented Namespace watch payload today; `Terminating` must be derived from the presence of the foreground-deletion finalizer in the namespace's finalizer list. This is called out explicitly rather than left for a consumer to discover by trial and error.

## Requirements *(mandatory)*

### Functional Requirements

- **FR-001**: The system MUST allow a consumer to establish an initial, race-free view of the complete current set of namespaces together with a resumable cursor, such that no change occurring between establishing the cursor and completing the initial listing can be missed.
- **FR-002**: The system MUST deliver an `ADDED` event, carrying the namespace's full current state (including its finalizer list and status conditions), whenever a new namespace is admitted.
- **FR-003**: The system MUST deliver a `MODIFIED` event, carrying the namespace's full current state at that point, whenever an existing namespace's spec, status, or finalizer list changes — including, without a separate event class, the transition into or out of a `Terminating` (foreground-deletion) state.
- **FR-004**: The system MUST deliver a `DELETED` event, carrying at minimum the namespace's identity and last-known `resourceVersion` (not necessarily its full prior state), only once a namespace's record is finally and permanently removed — never for entering `Terminating`, which MUST instead be represented as a `MODIFIED` event per FR-003.
- **FR-005**: The system MUST provide a stable, opaque `resourceVersion` cursor value with every delivered event, usable by a consumer to resume a watch from exactly that point without redelivering already-observed changes.
- **FR-006**: The system MUST deliver, to a consumer resuming from a valid `resourceVersion` cursor, exactly the changes that occurred after that cursor, in the order they were admitted for Namespace — and no others.
- **FR-007**: The system MUST provide a distinguishable "cursor expired" signal, distinct from a normal (possibly empty) resumed stream, when a consumer presents a `resourceVersion` cursor that is no longer within the retained change history, so the consumer knows it must re-establish its view rather than assume it is caught up.
- **FR-008**: The system MUST guarantee that changes within the Namespace stream are delivered in the order they were admitted, and MUST NOT guarantee any ordering relative to any other resource kind's stream.
- **FR-009**: The system MUST treat delivery as at-least-once; a consumer observing the same event more than once MUST be able to do so without the documented contract requiring exactly-once delivery.
- **FR-010**: The documented contract MUST state precisely which fields are present in each event's payload for Namespace today, including that no `deletionTimestamp`-equivalent field is present and that `Terminating` must be derived from the finalizer list.
- **FR-011**: The documented contract MUST provide concrete, consumer-facing examples covering: the initial list step, establishing a resumable cursor before or atomically with that list step, a resumed watch call using a previously obtained cursor, and the distinguishable response when that cursor has expired.
- **FR-012**: The documented contract MUST explicitly state, as non-goals, that this contract does not define or change: cascade-delete/auto-drain behavior for dependent resources of a namespace (already resolved by spec 046 as reject-until-empty, not auto-drain), any new namespace mutation or admission semantics (spec 046's domain), or the addition of any new field (such as a `deletionTimestamp`-equivalent) to the Namespace watch payload.
- **FR-013**: Automated tests MUST demonstrate, specifically for the Namespace resource kind (not only by analogy to another resource kind), that: an `ADDED`/`MODIFIED`/`DELETED` sequence is delivered in order; a resume from a valid cursor delivers only the missed changes; and a resume from an invalid/expired cursor produces the distinguishable "cursor expired" signal rather than a normal response.

### Production Requirements *(mandatory for core-service or load-bearing changes)*

- **PR-001 Replica Safety**: No new production behavior is introduced; the underlying event bus, subscription resolver, and resumable-cursor mechanism already run today across replicas as part of the existing spec 040/046 delivery. This spec adds only documentation of the existing contract and Namespace-kind-specific test coverage (FR-013); it does not change how multiple API replicas or controller-manager instances observe or resume Namespace watches.
- **PR-002 Multi-User Security**: Unchanged. Authorization for Namespace watch subscriptions continues to rely on the existing caller-authorization model already enforced for the generic `watchResources` mechanism; this spec neither loosens nor tightens it.
- **PR-003 Capacity**: Unchanged. The per-kind bounded retention window that determines when a `resourceVersion` cursor expires (FR-007) already exists and is not resized or redesigned by this spec; documenting its observable behavior (SC-003) does not itself require any capacity change.
- **PR-004 Backpressure**: Unchanged. Existing buffered delivery and slow-subscriber handling on the event bus are not modified by this spec.
- **PR-005 Recovery**: Unchanged in mechanism; clarified in documentation only. A consumer's recovery path after a restart or expired cursor (re-establish the initial view per FR-001/FR-007) is the existing, already-implemented behavior; this spec's contribution is making that recovery path documented and verified by test (FR-013, SC-006), not altering it.

### Key Entities

- **Namespace watch event**: A single change notification for a namespace, carrying an operation type (`ADDED`, `MODIFIED`, or `DELETED`), the namespace's identity, a `resourceVersion` cursor, and — for `ADDED`/`MODIFIED` only — the namespace's full current state (spec, finalizers, status conditions).
- **resourceVersion cursor**: An opaque, per-resource-kind, monotonically meaningful token identifying a point in the Namespace change history; used to resume a watch and to detect a race-free list-then-watch establishment point. Not comparable across resource kinds.
- **Cursor-expired signal**: A distinguishable response returned when a presented `resourceVersion` cursor falls outside the retained change history, signaling that the consumer must discard it and re-establish its view rather than resume.
- **Terminating (observed)**: A namespace state, observable only as a `MODIFIED` event and a finalizer-list value on the namespace's current state, in which the namespace has been marked for deletion but not yet finally removed. Not a distinct event type of its own.

## Success Criteria *(mandatory)*

### Measurable Outcomes

- **SC-001**: A consumer can go from "no prior state" to "holding an accurate, current view of every namespace and continuing to receive every subsequent change live" with zero missed changes and zero polling, verified for the Namespace resource kind specifically.
- **SC-002**: A consumer that persists its last-observed `resourceVersion`, stops observing, and later resumes from that cursor observes exactly the changes it missed — no gaps and no redelivery of unchanged namespaces — 100% of the time the cursor is still within the retained history.
- **SC-003**: 100% of resume attempts using a cursor outside the retained history receive the distinguishable "cursor expired" signal rather than a silently incomplete or empty result.
- **SC-004**: 100% of namespace transitions into or out of `Terminating`, and 100% of finalizer-list changes, are observable as a `MODIFIED` event — zero instances of such a transition being silently dropped or misrepresented as a `DELETED` event.
- **SC-005**: A reader unfamiliar with the server implementation can, using only the documented contract and its example payloads/calls, correctly describe the exact sequence needed to establish and resume a Namespace watch, and correctly identify which fields are absent from the payload (e.g., no `deletionTimestamp`-equivalent field) — verified by review against the documented content.
- **SC-006**: Automated test coverage exists that exercises the full `ADDED`/`MODIFIED`/`DELETED` sequence, a valid-cursor resume, and an expired-cursor resume for the Namespace resource kind specifically, and passes.

## Assumptions

- This spec formalizes and verifies an already-implemented mechanism rather than designing a new one: the underlying event-publishing, subscription, and resumable-cursor mechanism already exists as a generic, kind-parameterized capability (built for spec 040) and is already wired up for every Namespace lifecycle transition (built/extended by spec 046). No new transport, schema field, or mutation is introduced by this spec.
- Ordering is guaranteed only within the Namespace stream itself (per-kind ordering, matching the equivalent assumption already recorded for other resource kinds); no cross-kind ordering guarantee is implied or required.
- Delivery is at-least-once, not exactly-once, consistent with the existing reconciliation model elsewhere in this system (reconciliation is expected to be level-triggered and idempotent); this spec does not change that guarantee for Namespace.
- `Terminating` is observed purely via the presence of the foreground-deletion finalizer on a namespace's current state, delivered as an ordinary `MODIFIED` event — this spec does not introduce a dedicated `deletionTimestamp`-equivalent field or a distinct "entering Terminating" event type. Adding such a field, if ever needed, is explicitly deferred to a future spec rather than decided here.
- There is exactly one watch entry point for Namespace today — a generic, kind-parameterized mechanism shared with other resource kinds — and this spec does not call for a dedicated, Namespace-only entry point; introducing one, if ever needed, is out of scope here.
- Cascade-delete or auto-drain behavior for a namespace's dependent resources during `Terminating` is out of scope for this spec; that design question was already decided (reject-until-empty, not auto-drain) by spec 046 and is not reopened here.
- "Documented" in this spec's acceptance criteria means the contract, its guarantees, and its example payloads/calls are captured in a form a future consumer can rely on without reading server source code; it does not require any particular documentation tool or file format, which is a planning-phase decision.
