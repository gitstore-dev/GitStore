# Feature Specification: Controller Watch API and Status Subresource Contract

**Feature Branch**: `040-controller-watch-status-api`  
**Created**: 2026-08-07  
**Status**: Draft  
**Input**: User description: "Controller Watch API and Status Subresource Contract: define a GraphQL Subscription-based watch mechanism (watchProducts, watchCategories, etc. for core kinds; watchResources(kind) for CRDs) and a status-subresource mutation contract (patch/update per kind) so controller-manager reconcilers can list-then-watch resource changes and write status back with resourceVersion-based optimistic concurrency. Unblocks issue #244 (CategoryTaxonomy Controller Reconciliation, spec 039) and issue #165's controller-manager initiative. Covers GitHub issues #131 (Controller Watch API with resourceVersion Resume) and #166 (GraphQL Status Subresource Contract for Controllers, including sub-issues #176 schema, #177 authorization, #178 concurrency/conflict semantics, #179 integration tests)."

## User Scenarios & Testing *(mandatory)*

### User Story 1 - A controller can list-then-watch a resource kind's changes (Priority: P1)

As a controller author, I want to fetch the current state of every resource of a given kind and then receive an ordered stream of subsequent changes, so that my controller can build and maintain an accurate in-memory view without polling the entire catalogue on a timer.

**Why this priority**: This is the foundational capability every reconciler needs before it can do anything else. Spec 039 (CategoryTaxonomy Controller Reconciliation) and every future controller are blocked on this existing at all. Without it, controllers cannot observe change and can only guess when to re-check state.

**Independent Test**: Can be fully tested by starting a watch on a resource kind with no `resourceVersion` cursor (initial connection), confirming a defined "list" response is delivered first, then creating/updating/deleting a resource of that kind through the existing git-push admission pipeline and confirming a corresponding change event arrives on the stream in the order the changes were admitted.

**Acceptance Scenarios**:

1. **Given** a controller with no prior cursor, **When** it opens a watch for a core resource kind (e.g. `CategoryTaxonomy`), **Then** it receives the full current set of resources of that kind followed by a resume cursor (`resourceVersion`) it can persist.
2. **Given** a controller holding a previously issued `resourceVersion` cursor, **When** it opens a watch passing that cursor, **Then** it receives only the changes that occurred after that cursor, in the order they were admitted — not the full current set again.
3. **Given** an open watch connection, **When** a resource of the watched kind is created, updated, or deleted via a git push that is admitted, **Then** a change event describing that resource and the operation type is delivered on the stream.
4. **Given** a controller resumes with a `resourceVersion` cursor that is no longer valid (too old — the server can no longer reconstruct the delta), **When** it opens the watch, **Then** the server returns a distinct, unambiguous signal that the cursor has expired (as opposed to a normal empty-delta response), so the controller knows it must re-list rather than assume it is caught up.
5. **Given** a watch request for a CRD-defined kind (not one of the built-in core kinds), **When** the controller opens a watch using the generic kind-parameterized mechanism, **Then** it receives the same list-then-watch behavior as for a core kind.

---

### User Story 2 - A controller can write status back safely (Priority: P1)

As a controller author, I want to write my reconciler's computed status for a specific resource back to the system, and have that write rejected if someone else changed the resource in the meantime, so that I never silently clobber a newer version of the resource with a decision based on stale data.

**Why this priority**: Equally foundational to User Story 1 — a reconciler that can observe changes but cannot report its findings back provides no value. This is also required by spec 039's `CategoryTaxonomyStatus.resolved` and condition writes.

**Independent Test**: Can be fully tested by fetching a resource's current `resourceVersion`, submitting a status update referencing that version, and confirming the resource's status reflects the update; then submitting a second status update using the same (now-stale) version and confirming it is rejected with a conflict rather than silently applied.

**Acceptance Scenarios**:

1. **Given** a resource with a known current `resourceVersion`, **When** a controller submits a status update naming that exact `resourceVersion`, **Then** the update is applied and the resource's `resourceVersion` advances.
2. **Given** a resource whose `resourceVersion` has changed since a controller last observed it, **When** that controller submits a status update using the stale `resourceVersion`, **Then** the update is rejected with a conflict response, and the resource's status is unchanged.
3. **Given** a status update that supplies only a subset of status fields (e.g. only `conditions`, not `resolved`), **When** the update is applied, **Then** only the supplied fields change; fields the update did not mention retain their previous values.
4. **Given** any attempt (well-formed or malicious) to modify a resource's author-controlled spec fields through the status-update path, **When** the request is submitted, **Then** the request is rejected — the status-update path can only ever change `.status`, never `.spec` or `.metadata` author-controlled fields.
5. **Given** a status update submitted by a caller without controller-level authorization, **When** the request is submitted, **Then** the request is rejected as unauthorized, independent of whether the `resourceVersion` matches.

---

### User Story 3 - Operators can diagnose watch and status-write problems from documented signals (Priority: P2)

As an operator running the controller-manager fleet, when a controller's watch connection drops, its cursor expires, or its status writes start conflicting or failing, I want documented signals and guidance so I can tell whether the problem is transient, whether the controller is falling behind, and what to do about it.

**Why this priority**: Necessary for production operation but not for the mechanism to function in a demo or test environment — it builds on User Stories 1 and 2 rather than gating them.

**Independent Test**: Can be fully tested by deliberately expiring a watch cursor and a status-write conflict in a test environment, and confirming the documented signals (whatever form they take — logs, metrics, error codes) are observable and match the documentation.

**Acceptance Scenarios**:

1. **Given** a watch connection that disconnects and reconnects, **When** an operator inspects the documented signals, **Then** they can distinguish a normal transient reconnect from a cursor that expired and required a re-list.
2. **Given** a burst of status-write conflicts for a specific resource kind, **When** an operator inspects the documented signals, **Then** they can identify which kind is experiencing conflicts and get guidance on likely causes (e.g. two controllers writing the same status field, or a controller operating on stale cache data).

---

### Edge Cases

- What happens when a controller opens a watch for a kind that has zero resources? The initial list response is empty and the stream then delivers only future changes — this is not treated as an error.
- What happens when a status update is submitted for a resource that no longer exists (deleted between the controller's last observation and its write attempt)? The write is rejected with a not-found response, distinct from a version conflict.
- What happens when two independent controllers each own a disjoint subset of a resource's status fields (e.g. one owns `conditions`, another owns a kind-specific `resolved` sub-field) and both submit updates around the same time? Each update only touches the fields it supplied (per partial-merge semantics), so both can succeed without conflicting, provided neither's `resourceVersion` precondition is violated by the other's write. A conflict is only raised when the precondition itself is violated (the resource changed since the caller last observed it), not merely because two different fields were touched.
- What happens when a watch stream is held open longer than the server's retention window for undelivered events? This is the same "cursor expired" condition as an old resume cursor and must be signaled the same way.
- What happens when a namespace- or selector-based filter is applied to a watch and a resource that matches the filter is updated to no longer match it (or vice versa)? The transition itself (matching → non-matching, or the reverse) must be delivered as a change event so the controller's view stays consistent, not silently dropped.

## Requirements *(mandatory)*

### Functional Requirements

- **FR-001**: The system MUST provide a mechanism for a controller to fetch the complete current set of resources of a given kind (the "list" step).
- **FR-002**: The system MUST provide a mechanism for a controller to receive an ordered stream of subsequent create/update/delete changes for a given resource kind (the "watch" step), continuing from a cursor obtained from the list step or a prior watch session.
- **FR-003**: The system MUST expose a stable, opaque, per-change-event cursor value (`resourceVersion`) that a controller can persist and later present to resume a watch from that point, without redelivering changes already observed.
- **FR-004**: The system MUST provide a distinguishable signal when a controller presents a cursor that is no longer valid for resumption, so the controller can react by re-listing rather than assuming a normal (possibly empty) resume.
- **FR-005**: The system MUST support this list-then-watch mechanism for every core resource kind that a controller may need to reconcile (at minimum: `CategoryTaxonomy`, with the mechanism designed to extend to `Product`, `ProductVariant`, and `Collection` without a redesign).
- **FR-006**: The system MUST provide a generically kind-parameterized variant of the watch mechanism usable for CRD-defined kinds that are not one of the built-in core kinds, without requiring a schema change per new CRD kind.
- **FR-007**: The system MUST support server-side filtering of a watch by namespace, and MUST support filtering by label selector, so a controller only receives events relevant to its scope.
- **FR-008**: The system MUST provide a mechanism for a controller to submit a partial update to a resource's `.status` sub-resource, where only explicitly supplied fields are changed and all other existing status fields are left unchanged.
- **FR-009**: The system MUST require every status-update request to include the `resourceVersion` the caller last observed, and MUST reject the request with a conflict response if the resource's current `resourceVersion` does not match.
- **FR-010**: The system MUST reject any status-update request that attempts to modify fields outside the `.status` sub-resource (i.e. `.spec` or author-controlled `.metadata` fields), regardless of whether the `resourceVersion` precondition is satisfied.
- **FR-011**: The system MUST authorize status-update requests separately from ordinary read/write access, such that only callers with controller-level authorization can perform a status update, independent of the `resourceVersion` outcome.
- **FR-012**: The system MUST return a distinct response when a status update targets a resource that no longer exists, distinguishable from a `resourceVersion` conflict on an existing resource.
- **FR-013**: The system MUST deliver a change event for a watched resource whenever it transitions into or out of matching an active namespace/selector filter, not only while it continuously matches.
- **FR-014**: The system MUST document, for operators, how to distinguish a transient watch reconnect from an expired-cursor condition requiring re-list, and how to interpret a sustained rate of status-write conflicts for a given kind.
- **FR-015**: Integration tests MUST cover: initial list-then-watch for a core kind with zero and non-zero existing resources; resume from a valid cursor; the expired-cursor signal and required re-list; a successful status update; a rejected status update due to `resourceVersion` conflict; a rejected status update attempting to alter spec fields; and a rejected status update from an unauthorized caller.

### Key Entities

- **WatchEvent**: A single change notification for a resource — carries the operation type (added/modified/deleted), the resource's current state (or identity, for deletions), and the `resourceVersion` cursor value at that point in the stream.
- **resourceVersion**: An opaque, monotonically meaningful (but not necessarily numerically ordered — treated as an opaque token) cursor value that identifies a point in a resource kind's change history, used both for watch resumption and for optimistic-concurrency preconditions on status writes.
- **StatusUpdateRequest**: A caller-submitted partial update targeting a single resource's `.status` sub-resource, carrying the fields to change and the `resourceVersion` precondition.
- **LabelSelector**: An existing filtering construct (already defined for `Collection` membership) reused here to scope which resources a watch subscription receives events for.
- **Core resource kind**: One of the built-in, schema-defined kinds (`CategoryTaxonomy`, `Product`, `ProductVariant`, `Collection`) with a dedicated watch entry point.
- **CRD-defined kind**: A kind not built into the core schema, addressed through the generic kind-parameterized watch and status-update mechanism.

## Success Criteria *(mandatory)*

### Measurable Outcomes

- **SC-001**: A controller can go from "cold start, no prior state" to "holding an accurate, current view of every resource of a kind and continuing to receive live changes" without any polling loop and without missing a change that occurred during the transition from list to watch.
- **SC-002**: A controller that persists its last-seen `resourceVersion`, restarts, and resumes a watch from that cursor observes exactly the changes it missed while it was down — no gaps, no full re-delivery of unchanged resources.
- **SC-003**: 100% of status-write attempts that use a stale `resourceVersion` are rejected rather than silently overwriting newer data; 0% of status-write attempts can alter a resource's spec fields regardless of caller intent.
- **SC-004**: An operator, using only the documented signals, can correctly classify a real watch disruption as "transient — will self-heal" versus "cursor expired — controller must re-list" without inspecting source code.
- **SC-005**: The same watch and status-update mechanism works for a newly introduced CRD kind without any schema or server-code change beyond registering the new kind — validated by exercising the mechanism against at least one CRD-style kind in integration tests.

## Assumptions

- This spec defines the contract and its server-side implementation for at least one core kind (`CategoryTaxonomy`, to directly unblock spec 039 / issue #244); extending live event production to every other core kind (`Product`, `ProductVariant`, `Collection`) is designed for but may be delivered incrementally in follow-on work, since only `CategoryTaxonomy` has an immediate controller consumer today.
- "Ordered" delivery within a single resource kind's stream means changes are delivered in the order they were admitted for that kind; no cross-kind ordering guarantee is implied or required.
- Delivery semantics are at-least-once, consistent with the existing controller-manager reconciler design (reconciliation is level-triggered and idempotent per spec 026), so a controller observing the same change twice must not corrupt its state.
- The underlying transport mechanism (e.g. GraphQL `Subscription` type, a dedicated streaming endpoint, or server-side polling presented as a stream) is an implementation decision made during planning; this spec defines only the observable contract (list-then-watch semantics, cursor behavior, filtering, status-update semantics) that any transport choice must satisfy.
- Authorization for controller-level status writes builds on whatever identity/authorization mechanism already distinguishes API callers (existing auth phases); this spec does not redesign authentication, only adds a controller-specific authorization check on the status-update path.
- This spec does not redesign or replace any existing spec-mutation pathway (git push remains the only way to change `.spec`); it only adds the previously-missing status-write and watch paths.
