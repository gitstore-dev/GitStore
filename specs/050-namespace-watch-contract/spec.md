# Feature Specification: Namespace Watch Contract: Events and resourceVersion Resume

**Feature Branch**: `050-namespace-watch-contract`
**Created**: 2026-08-19
**Status**: Draft
**Input**: User description: "Namespace Watch Contract: Events and resourceVersion Resume (GH#174). Define namespace watch semantics for controllers and clients, including event model and resourceVersion resume behavior. Scope: define namespace watch event shapes (ADDED, MODIFIED, DELETED); define list-then-watch resume semantics using resourceVersion; define ordering and replay guarantees for namespace streams; provide subscription and query examples for namespace watch consumers. Parent initiative: #170. Blocked by GH#172 (shipped as spec 046). Related: #166 (shipped as spec 040)."

## Clarifications

### Session 2026-08-19

- Q: GH#174's own "Open Questions" section asks about a "Namespace Controller" that watches Namespace, handles deletion, removes all namespaced resources when a namespace enters Terminating, and clears finalizers once everything inside is gone — is that in scope here? → A: No. That assumption is stale. Spec 046 (already merged) implemented the real Namespace reconciler (`gitstore-controller-manager/internal/namespace/reconciler.go`), which clears the foreground-deletion finalizer only once zero repositories remain — it polls and rejects-until-empty, it does not cascade-delete dependents. That design question is already resolved by spec 046 and is explicitly out of scope for this spec, which covers only the watch/event contract for observing Namespace state (including observing `Terminating` and finalizer changes already emitted by that reconciler), not deletion/cascade mechanics.
- Q: Is the watch/event mechanism for Namespace new work, or does something already exist? → A: The underlying event bus, publishing, and resumable-cursor machinery already exist end-to-end (built for spec 040, wired to every Namespace lifecycle transition by spec 046). What does not yet exist is a dedicated, strongly-typed entry point for it — today the only way to reach that machinery for Namespace is the generic, kind-parameterized `watchResources(kind: "Namespace")` subscription. This spec formalizes the underlying contract (event shapes, resume semantics, ordering/delivery guarantees) and closes one verified test-coverage gap, but see the next clarification for a reversal on the entry-point question.
- Q: Should Namespace watch continue to be served only through the generic `watchResources(kind: String)` subscription, or should it get a dedicated, typed entry point? → A: Dedicated — and this is not a novel pattern, it's completing an existing one. The schema already exposes `watchCategories(namespace, selector, resourceVersion) → CategoryWatchEvent` and `watchProducts(namespace, selector, resourceVersion) → ProductWatchEvent` (see `gitstore-api/internal/graph/generated/schema.generated.go`'s `SubscriptionResolver`) as dedicated, strongly-typed subscriptions sitting alongside the generic `watchResources`, each returning an envelope of `{ type, namespace, name, resourceVersion, <kind-named field>: <TypedResource | null> }`. Namespace is the one already-shipped core kind that never got this treatment, and only because spec 046 predates this contract-formalization pass. This spec closes that gap: it adds `watchNamespaces(selector, resourceVersion) → NamespaceWatchEvent` (no `namespace` argument — Namespace is cluster-scoped, unlike Category/Product), following the exact same envelope shape (`{ type, name, resourceVersion, namespace: Namespace | null }`, field named after the kind per the established convention). The generic `watchResources(kind: "Namespace")` path is not removed (no breaking change for any existing caller), but `watchNamespaces` becomes the canonical, documented entry point going forward, consistent with how `watchCategories`/`watchProducts` are already the canonical entry points for their kinds.

### Session 2026-08-30

- Q: Planning found that the existing Namespace event bus and cursors are process-local, are not shared by API replicas, reset on restart, silently close slow subscribers, do not emit periodic idle bookmarks, and do not enforce `namespace.watch`. Should feature 050 remain documentation/typed-schema-only or own the missing production infrastructure? → A: Expand feature 050 to own the replica-safe watch infrastructure. Spec 047 is already shipped and MUST NOT be reopened; the new infrastructure observes its committed Namespace transitions without changing admission, deletion, repository-fence, status, generation, or resourceVersion semantics.

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

**Why this priority**: Lower urgency than User Stories 1–2 because it packages the completed transport and durability behavior for consumers; this story is about making the resulting contract discoverable and reproducible rather than adding another server behavior.

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
- What happens when a Namespace watch subscription sits idle with no actual namespace changes for an extended period? Per the existing `WatchEventType.BOOKMARK` semantics (shared across all kinds), the consumer periodically receives a `BOOKMARK` event carrying only an advanced `resourceVersion` with no resource payload — this lets a long-lived, idle consumer keep its resumable cursor fresh without a corresponding create/update/delete having occurred, and MUST NOT be mistaken for a `MODIFIED` event with no visible change.

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
- **FR-014**: The system MUST expose Namespace watch through a dedicated `watchNamespaces` GraphQL subscription returning a strongly-typed `NamespaceWatchEvent` (fields: `type`, `name`, `resourceVersion`, `namespace: Namespace | null`), mirroring the existing `watchCategories → CategoryWatchEvent` and `watchProducts → ProductWatchEvent` convention — in addition to (not as a replacement for) the existing generic `watchResources(kind: "Namespace")` path. Unlike `watchCategories`/`watchProducts`, `watchNamespaces` MUST NOT take a `namespace` filter argument, since Namespace is cluster-scoped, not itself namespaced.
- **FR-015**: The dedicated `watchNamespaces` subscription MUST satisfy every other requirement in this contract (FR-001 through FR-013) identically to the generic path — the same race-free list-then-watch establishment, event shapes, resume/expiry semantics, ordering, and delivery guarantees — so a consumer gets no weaker a contract by using the typed entry point.
- **FR-016**: The documented contract's consumer-facing examples (FR-011) MUST be written against the dedicated `watchNamespaces` subscription as the primary, recommended form, with the generic `watchResources(kind: "Namespace")` form noted only as the pre-existing alternative.
- **FR-017**: The system MUST deliver a `BOOKMARK` event (carrying only an advanced `resourceVersion`, with `namespace` null) on both the generic and dedicated Namespace watch paths, matching the existing `WatchEventType.BOOKMARK` semantics already defined for every other kind (a periodic cursor refresh on an idle subscription, with no corresponding resource change) — this spec's original event-shape documentation (FR-002 through FR-004) named only `ADDED`/`MODIFIED`/`DELETED` and omitted `BOOKMARK`, which was a documentation gap, not an intentional exclusion.
- **FR-018**: Namespace watch history and cursor allocation MUST be shared across API replicas: a subscription connected to any healthy replica MUST observe Namespace changes admitted by any other replica, and a valid cursor issued by one replica MUST resume through another replica.
- **FR-019**: Namespace watch history MUST survive API process restart and rolling replacement for the configured retention window. A restart MUST NOT silently reset or reuse the cursor namespace; a cursor outside that window or from an incompatible journal epoch MUST produce `WATCH_EXPIRED`.
- **FR-020**: A successful authoritative Namespace create, update, status/finalizer change, or final deletion MUST have a durable change record derived from the same committed datastore mutation. Failed/rejected/conflicting/no-op operations MUST NOT invent successful events. Crash recovery MAY redeliver an event but MUST NOT lose an acknowledged transition.
- **FR-021**: Subscriber backpressure that creates an unrecoverable delivery gap MUST terminate with the same distinguishable `WATCH_EXPIRED` contract as retention expiry, never with an apparently clean end-of-stream that permits stale state to remain trusted.
- **FR-022**: Both `watchNamespaces` and `watchResources(kind: "Namespace")` MUST authorize the caller with the cluster-scoped `namespace.watch` action before registering or replaying a subscription. Denied callers MUST NOT learn Namespace names, cursors, retention state, or event payloads.
- **FR-023**: The infrastructure MUST expose bounded configuration and operational signals for journal retention, CDC/materializer lag, oldest/high-water cursors, replay depth, subscriber count, overflow/expiry reason, bookmark lag, lease/fencing state, and recovery after replica replacement.
- **FR-024**: The durable journal, CDC retention, replay batches, subscriber buffers, polling/backoff, and bookmark production MUST be explicitly bounded. Falling behind any safe bound MUST fail closed with `WATCH_EXPIRED` or watch-unavailable readiness rather than silently skipping records.

### Production Requirements *(mandatory for core-service or load-bearing changes)*

- **PR-001 Replica Safety**: Replace the process-local Namespace watch history with a shared durable journal sourced atomically from committed Namespace datastore changes. Tests MUST use at least two API replicas, admit through one, watch/resume through another, and replace a replica under traffic without a silent gap.
- **PR-002 Multi-User Security**: Add explicit `namespace.watch` authorization to both Namespace watch entry points. Authentication and authorization MUST precede cursor validation, replay, and subscription registration so denial cannot disclose resource or journal state.
- **PR-003 Capacity**: Validate 10 sustained Namespace transitions/second with bursts of 100/second and 1,000 concurrent Namespace subscriptions across two API replicas for 60 minutes. Require event visibility p95 ≤1 second and p99 ≤3 seconds, replay of 10,000 retained events in ≤5 seconds p95, internal errors <0.1%, zero missing acknowledged transitions, CPU <80%, retained-memory growth <10%, and recovery after replacing one replica within 30 seconds.

  A separately labelled early-alpha local-environment qualification MAY enforce
  a provisional visibility p95 ceiling of 2 seconds while retaining p99 ≤3
  seconds and every correctness, error, recovery, CPU, and memory requirement.
  It MUST warn above the 1-second production target and MUST NOT be represented
  as PR-003 production validation. Diagnostic evidence cannot pass either gate.
  Threshold reconsideration requires at least five clean 10-minute repetitions
  with fixed topology and configuration.
- **PR-004 Backpressure**: Journal fetches MUST be bounded to 256 events per batch, per-subscription delivery buffers to 64 events, and retries to capped exponential backoff. Overflow, retention overrun, or journal discontinuity MUST terminate with a typed expiry reason and increment bounded-cardinality metrics.
- **PR-005 Recovery**: Persist CDC progress only after durable journal append. Restart may replay and duplicate the last unsaved CDC record, consistent with at-least-once delivery, but MUST NOT advance progress past an unjournaled record. Journal epoch/retention discontinuity triggers relist through `WATCH_EXPIRED`.
- **PR-006 Rolling Upgrade**: Migration and schema rollout MUST be server-first. During the mixed-version window, Namespace subscriptions are denied fleet-wide; enable the durable backend only after CDC, journal schema, materializer health, and every API replica converge. Rollback restores the deny first and retains the forward migration/CDC schema.
- **PR-007 Spec-047 Isolation**: The shipped spec-047 admission matrix, deletion outcomes, repository fence, generation/resourceVersion rules, and status ownership MUST NOT be changed. Watch infrastructure observes their committed datastore effects and adds no second policy or mutation outcome.

### Key Entities

- **Namespace watch event**: A single change notification for a namespace, carrying an operation type (`ADDED`, `MODIFIED`, or `DELETED`), the namespace's identity, a `resourceVersion` cursor, and — for `ADDED`/`MODIFIED` only — the namespace's full current state (spec, finalizers, status conditions).
- **resourceVersion cursor**: An opaque, per-resource-kind, monotonically meaningful token identifying a point in the Namespace change history; used to resume a watch and to detect a race-free list-then-watch establishment point. Not comparable across resource kinds.
- **Cursor-expired signal**: A distinguishable response returned when a presented `resourceVersion` cursor falls outside the retained change history, signaling that the consumer must discard it and re-establish its view rather than resume.
- **Terminating (observed)**: A namespace state, observable only as a `MODIFIED` event and a finalizer-list value on the namespace's current state, in which the namespace has been marked for deletion but not yet finally removed. Not a distinct event type of its own.
- **Dedicated Namespace subscription (`watchNamespaces`)**: A strongly-typed GraphQL subscription returning `NamespaceWatchEvent` (`type`, `name`, `resourceVersion`, `namespace: Namespace | null`), serving the exact same underlying event stream as the generic `watchResources(kind: "Namespace")` path but without a stringly-typed `kind` parameter or a generic/union response type — the Namespace-kind counterpart to the already-shipped `watchCategories`/`CategoryWatchEvent` and `watchProducts`/`ProductWatchEvent` pair. The canonical, documented entry point for new Namespace watch consumers going forward; the generic path remains available but is no longer the recommended form.
- **BOOKMARK event**: The existing, kind-agnostic `WatchEventType.BOOKMARK` value, delivered with only an advanced `resourceVersion` and no resource payload, used to refresh an idle subscription's resumable cursor. Applies to Namespace watch (both the generic and dedicated paths) identically to every other kind; not new to this spec, but omitted from its initial draft and restored here.

## Success Criteria *(mandatory)*

### Measurable Outcomes

- **SC-001**: A consumer can go from "no prior state" to "holding an accurate, current view of every namespace and continuing to receive every subsequent change live" with zero missed changes and zero polling, verified for the Namespace resource kind specifically.
- **SC-002**: A consumer that persists its last-observed `resourceVersion`, stops observing, and later resumes from that cursor observes exactly the changes it missed — no gaps and no redelivery of unchanged namespaces — 100% of the time the cursor is still within the retained history.
- **SC-003**: 100% of resume attempts using a cursor outside the retained history receive the distinguishable "cursor expired" signal rather than a silently incomplete or empty result.
- **SC-004**: 100% of namespace transitions into or out of `Terminating`, and 100% of finalizer-list changes, are observable as a `MODIFIED` event — zero instances of such a transition being silently dropped or misrepresented as a `DELETED` event.
- **SC-005**: A reader unfamiliar with the server implementation can, using only the documented contract and its example payloads/calls, correctly describe the exact sequence needed to establish and resume a Namespace watch, and correctly identify which fields are absent from the payload (e.g., no `deletionTimestamp`-equivalent field) — verified by review against the documented content.
- **SC-006**: Automated test coverage exists that exercises the full `ADDED`/`MODIFIED`/`DELETED` sequence, a valid-cursor resume, and an expired-cursor resume for the Namespace resource kind specifically, and passes.
- **SC-007**: A consumer can observe every Namespace watch event through the dedicated, strongly-typed `watchNamespaces` subscription without ever needing to pass a `kind` string parameter or discriminate a generic/union payload by type — verified by the documented schema and its examples.
- **SC-008**: 100% of `BOOKMARK` events delivered on an idle Namespace watch carry an advanced `resourceVersion` and no resource payload, and are never misrepresented as a `MODIFIED` event — verified for both the generic and dedicated Namespace watch paths.
- **SC-009**: In a two-replica test, 100% of Namespace transitions admitted through either replica are observed in one ordered stream through either replica, including after reconnecting with a cursor issued by the other replica.
- **SC-010**: During a rolling replacement under sustained transitions, zero acknowledged Namespace transitions are missing; duplicate delivery is accepted and measured, and the replacement replica resumes within 30 seconds.
- **SC-011**: A subscriber forced beyond its buffer or retained-history bound receives `WATCH_EXPIRED` and successfully re-establishes state; zero overflow cases end as an apparently successful stream.
- **SC-012**: Unauthorized callers receive `FORBIDDEN` before any cursor validation or replay and observe zero Namespace identities or journal metadata through either watch entry point.
- **SC-013**: The 60-minute production envelope in PR-003 meets every latency, error, correctness, CPU, memory, replay, and recovery threshold.

## Assumptions

- This spec reuses the existing GraphQL WebSocket transport and the already-wired Namespace lifecycle publication points, but it owns the infrastructure required to replace process-local Namespace watch history with a durable, replica-safe stream. It introduces the additive `watchNamespaces`/`NamespaceWatchEvent` schema and durable journal/CDC support; it does not introduce a new Namespace mutation.
- Ordering is guaranteed only within the Namespace stream itself (per-kind ordering, matching the equivalent assumption already recorded for other resource kinds); no cross-kind ordering guarantee is implied or required.
- Delivery is at-least-once, not exactly-once, consistent with the existing reconciliation model elsewhere in this system (reconciliation is expected to be level-triggered and idempotent); this spec does not change that guarantee for Namespace.
- `Terminating` is observed purely via the presence of the foreground-deletion finalizer on a namespace's current state, delivered as an ordinary `MODIFIED` event — this spec does not introduce a dedicated `deletionTimestamp`-equivalent field or a distinct "entering Terminating" event type. Adding such a field, if ever needed, is explicitly deferred to a future spec rather than decided here.
- Per user design feedback, this spec reverses its original position that a generic-only entry point was sufficient. `watchNamespaces` (FR-014–FR-016) is not a new pattern — it completes the existing `watch<Kind>` convention already shipped for `watchCategories`/`watchProducts` — applied to the one already-shipped core kind that was missing it. This is scoped as an additive change (the generic `watchResources` path is not removed or deprecated) and covers Namespace only; retrofitting is not needed for CategoryTaxonomy/Product since they already have their dedicated subscriptions — any other core kind that ships in the future should get its `watch<Kind>` subscription as part of its own spec, not bundled here.
- The `BOOKMARK` enum value and one-time bootstrap bookmark already exist, but periodic durable idle-bookmark production does not. Feature 050 supplies that missing producer for the durable Namespace journal without changing the shared enum.
- Spec 047 is a shipped dependency and is not reopened. Its Namespace admission, deletion, repository-fence, authored-state, resourceVersion, and status contracts are inputs to event classification and tests, not redesign targets.
- Cascade-delete or auto-drain behavior for a namespace's dependent resources during `Terminating` is out of scope for this spec; that design question was already decided (reject-until-empty, not auto-drain) by spec 046 and is not reopened here.
- "Documented" in this spec's acceptance criteria means the contract, its guarantees, and its example payloads/calls are captured in a form a future consumer can rely on without reading server source code; it does not require any particular documentation tool or file format, which is a planning-phase decision.
