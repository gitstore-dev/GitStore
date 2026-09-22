# Feature Specification: CategoryTaxonomy Path Freshness

**Feature Branch**: `057-categorytaxonomy-path-freshness`
**Created**: 2026-08-20
**Status**: Draft
**Input**: User description: "CategoryTaxonomy Path Freshness: Deprecate Admission-Time path/depth in Favor of status.resolved (GitHub issue #382). `Category.path`/`Category.depth` go stale after a category is re-parented elsewhere in the tree, while the separate `status.resolved.path`/`status.resolved.depth` (written by the CategoryTaxonomy controller) are kept fresh. Fix the resolver to prefer the fresh fields, with a pre-reconcile fallback, and mark the legacy fields `@deprecated`."

## User Scenarios & Testing *(mandatory)*

### User Story 1 - Reading a category's current position in the tree after a re-parent (Priority: P1)

A catalog administrator (or any GraphQL API consumer — internal UI client or external integrator) re-parents a category by pushing a spec change to its `parentRef`, then later queries that category — or any of its pre-existing descendants elsewhere in the tree — via `Category.path`/`Category.depth`. Today, those top-level fields silently keep showing the old, stale hierarchy position because they are derived from a value that is only recomputed for the categories actually touched by that push. The consumer needs the values they read from the most-discoverable fields to reflect reality, not a snapshot frozen at the last time that specific category (or its ancestor chain) was itself pushed.

**Why this priority**: This is the core bug reported in the issue. Consumers query `Category.path`/`Category.depth` first because they are the top-level, most-discoverable fields on the type — serving stale data from the field most people reach for first is the highest-impact problem to fix.

**Independent Test**: Can be fully tested by re-parenting a category (moving it under a different parent) via a push, waiting for the CategoryTaxonomy controller to reconcile it, then querying `Category.path`/`Category.depth` on that category and confirming the returned values match the new hierarchy position rather than the old one.

**Acceptance Scenarios**:

1. **Given** a category has been reconciled at least once by the CategoryTaxonomy controller since its last hierarchy-affecting change, **When** a client queries `Category.path` or `Category.depth` on that category, **Then** the returned value matches the controller-computed `status.resolved.path`/`status.resolved.depth`, even if that differs from what the admission-time computation alone would have produced.
2. **Given** a category `A` is re-parented in one push, and a pre-existing descendant `B` of `A` exists elsewhere in the tree but was not itself included in that push, **When** a client queries `Category.path` on `B` immediately after the push (before the controller has reconciled `B`), **Then** the returned value may still reflect `B`'s old, stale hierarchy position — this is an expected transient state, not a defect.
3. **Given** the same scenario as above, **When** the client queries `Category.path` on `B` again after the CategoryTaxonomy controller's reconcile cascade has reached `B` (i.e., `B`'s `status.resolved` has been updated to reflect `A`'s new position), **Then** the returned value now matches `B`'s corrected hierarchy position — the descendant has self-healed without requiring `B` to be part of any push itself.

---

### User Story 2 - Getting a usable value immediately after a category is first created (Priority: P2)

A client creates a brand-new category via a push and immediately queries it (for example, to render a confirmation UI) before the CategoryTaxonomy controller has had a chance to reconcile it for the first time. The client needs `Category.path`/`Category.depth` to return a sensible, non-null value right away — not null, not an error, and not a wait for asynchronous reconciliation to complete.

**Why this priority**: This preserves existing, relied-upon behavior ("never null immediately after push") while the freshness fix for User Story 1 is introduced. Breaking this would be a regression more disruptive than the staleness bug itself for newly created categories.

**Independent Test**: Can be fully tested by creating a new category via a push and immediately querying `Category.path`/`Category.depth` on it (before any reconcile has occurred), confirming both fields return non-null, best-effort values consistent with the admission-time computation.

**Acceptance Scenarios**:

1. **Given** a category has just been created by a push and has not yet been reconciled by the CategoryTaxonomy controller (its status has no resolved hierarchy data yet), **When** a client queries `Category.path`/`Category.depth`, **Then** both fields return a non-null, best-effort value derived from the same admission-time computation used today, rather than null or an error.
2. **Given** the category above is subsequently reconciled for the first time, **When** a client re-queries `Category.path`/`Category.depth`, **Then** the returned values now come from the controller-computed hierarchy data instead of the admission-time computation, and the previously separate root-vs-nested depth cases resolve identically (a root category's depth is `0` under both computations).

---

### User Story 3 - Discovering the recommended replacement fields via schema introspection (Priority: P3)

A client developer inspecting the GraphQL schema (via a tool, IDE, or introspection query) encounters `Category.path`/`Category.depth` and needs a clear, actionable signal that a fresher, canonical alternative exists, so future client code is written against the right field from the start rather than repeating the same staleness confusion this issue exists to fix.

**Why this priority**: Lower priority than the runtime behavior fix (User Stories 1-2) because it does not change any query result, but it is required by the issue to prevent the staleness problem from being reintroduced by new client code that keeps discovering the legacy fields first.

**Independent Test**: Can be fully tested by running a GraphQL introspection query against `Category.path` and `Category.depth` and confirming both report as deprecated with a reason string naming the specific replacement field.

**Acceptance Scenarios**:

1. **Given** the GraphQL schema for the `Category` type, **When** a client introspects the `path` field, **Then** it is marked deprecated with a reason that names `status.resolved.path` as the replacement.
2. **Given** the GraphQL schema for the `Category` type, **When** a client introspects the `depth` field, **Then** it is marked deprecated with a reason that names `status.resolved.depth` as the replacement.
3. **Given** an existing client query that selects `Category.path`/`Category.depth` without modification, **When** that query is executed after this change ships, **Then** it continues to succeed and return a value for both fields exactly as before (deprecation is advisory only and does not remove, rename, or change the type/nullability of either field).

### Edge Cases

- **`status.resolved` exists but is itself stale relative to a very recent, not-yet-reconciled move**: Because the CategoryTaxonomy controller's reconciliation is asynchronous and propagates level-by-level down an affected subtree (not as a single atomic bulk update), there is a window — bounded by however long the reconcile cascade takes to reach a given descendant — during which `status.resolved.path`/`status.resolved.depth` may not yet reflect the very latest ancestor move. This is expected, transient, self-healing behavior (it resolves on that node's next reconcile pass), not an error condition, and this specification does not require detecting or flagging this window to the client.
- **A category is part of a cycle** (its `parentRef` chain loops back on itself): the controller intentionally freezes `status.resolved.path`/`.depth` at their last-known-good value rather than recomputing a meaningless path through a cycle. This specification's fallback logic is unaffected by this case — it only changes which of the two existing values (`status.resolved` vs. admission-time) is preferred, not what either value contains once chosen.
- **A category has never been reconciled and its admission-time hierarchy data is itself absent or empty** (extremely unlikely, but possible for a malformed or edge-case resource): the field must still return a value, not null or an error, per the "never null" requirement — an empty/self-only path is an acceptable minimal fallback.
- **Root categories** (no parent): both the admission-time computation and the controller computation agree that depth is `0` and path is a single-element array containing the category's own name — there is no divergence to reconcile for this case.

## Requirements *(mandatory)*

### Functional Requirements

- **FR-001**: When returning `Category.path` and `Category.depth` for a CategoryTaxonomy resource, the system MUST prefer the value from that resource's `status.resolved.path`/`status.resolved.depth` whenever `status.resolved` is present (i.e., the resource has been reconciled by the CategoryTaxonomy controller at least once since being created).
- **FR-002**: When a CategoryTaxonomy resource's `status.resolved` is not yet present (i.e., it has not yet been reconciled for the first time), the system MUST fall back to the existing admission-time-derived computation for `Category.path`/`Category.depth`, so that both fields always return a non-null, best-effort value immediately after a push — never null and never an error.
- **FR-003**: The fallback precedence in FR-001/FR-002 MUST apply identically and uniformly to both `Category.path` and `Category.depth` — no distinct special-case handling is required for `depth` versus `path`, since both computations already agree that a root category's depth is `0`.
- **FR-004**: The system MUST treat a transient mismatch between a very-recently-changed ancestor's position and a not-yet-reconciled descendant's `status.resolved.path`/`.depth` as expected, self-healing behavior rather than an error — no error, warning, or blocking behavior is required while this window exists, and the value in this window is still governed by FR-001 (once `status.resolved` exists, it is preferred even if not yet fully caught up).
- **FR-005**: The GraphQL schema MUST mark `Category.path` and `Category.depth` as deprecated, each with a reason that explicitly names its canonical replacement field (`status.resolved.path` and `status.resolved.depth`, respectively), following this codebase's established deprecation-reason phrasing convention for prior deprecated fields.
- **FR-006**: Marking `Category.path`/`Category.depth` as deprecated MUST NOT remove, rename, or change the type or nullability of either field — both fields remain fully queryable and behave exactly as before for any existing client query that selects them, preserving backward compatibility.
- **FR-007**: This specification MUST NOT alter the underlying admission-time hierarchy computation itself, the datastore value it is derived from, or that value's use for internal cycle-detection purposes — only the GraphQL-facing precedence used to answer `Category.path`/`Category.depth` queries changes.
- **FR-008**: This specification MUST NOT alter how the CategoryTaxonomy controller computes or propagates `status.resolved.path`/`status.resolved.depth` to descendants — that mechanism is assumed to already exist and function as designed; this feature only changes which of the two existing values the read path prefers.
- **FR-009**: This specification MUST NOT alter any hierarchy-prefix-based filtering behavior used elsewhere for listing or querying categories by ancestor path.
- **FR-010**: The documentation accompanying the schema change (field descriptions and/or accompanying developer-facing notes) MUST describe the two-phase fallback behavior (pre-reconcile best-effort value vs. post-reconcile authoritative value) clearly enough that a client developer encountering the difference understands it as intentional, not a bug.

### Production Requirements *(mandatory for core-service or load-bearing changes)*

- **PR-001 Replica Safety**: This feature changes only a read-path computation over data each `gitstore-api` replica already loads per-request (the resource's own `status` blob and admission-time hierarchy value); it introduces no new shared or replica-local state, so behavior is identical across any number of replicas, during rolling upgrades, and across process replacement — a replica serving an old binary and one serving a new binary may disagree on which of the two existing values is preferred for a given request, but neither can return an incorrect or inconsistent value for the data it holds.
- **PR-002 Multi-User Security**: No new field, argument, or capability is added — `Category.path`/`Category.depth` remain governed by whatever read authorization already applies to querying a `Category`. No new authorization check is required or introduced by this feature.
- **PR-003 Capacity**: The added computation is a constant-time nil-check and field copy per resource per query (checking whether `status.resolved` is present before falling back to the existing computation); it adds no additional datastore reads, no additional payload size, and no measurable latency at any dataset size or concurrency level already supported.
- **PR-004 Backpressure**: Not applicable — this feature introduces no new queue, worker, retry, or timeout; it changes only which in-memory value an already-executing read resolver returns.
- **PR-005 Recovery**: This feature introduces no new failure mode. The pre-existing, asynchronous, level-by-level reconcile cascade already recovers a descendant's stale hierarchy data after a partial failure or restart of the CategoryTaxonomy controller; this feature only changes the read path to surface that recovered data once it exists, and continues returning the existing best-effort fallback value until it does.

### Key Entities

- **CategoryTaxonomy (Category)**: A hierarchical classification resource. Exposes `path` (ordered list of ancestor names from root to self) and `depth` (position in the tree, root = 0) as top-level, always-populated fields, and a separate `status.resolved` block containing the same two values as computed and kept fresh by the CategoryTaxonomy controller. This feature changes which of these two sources of hierarchy data the top-level fields draw from, without changing either source's own computation.
- **Resolved Hierarchy Status**: The controller-maintained, per-category record of `path`/`depth` (plus other reconciled hierarchy metadata), updated asynchronously and propagated to descendants over one or more reconcile passes after any ancestor move. Present only once a category has been reconciled at least once; absent (null) before that point.

## Success Criteria *(mandatory)*

### Measurable Outcomes

- **SC-001**: After a category is re-parented and the CategoryTaxonomy controller completes reconciling it, 100% of subsequent `Category.path`/`Category.depth` queries against that category return its correct, current hierarchy position — not its pre-move position.
- **SC-002**: For a category re-parented in one push, a pre-existing descendant elsewhere in the tree that was not part of that push shows its corrected `Category.path` within the same bounded window it takes the CategoryTaxonomy controller's existing reconcile cascade to reach that descendant, with no additional client-visible delay introduced by this feature.
- **SC-003**: 100% of `Category.path`/`Category.depth` queries issued immediately after a category's first-ever push (before any reconcile has occurred) return a non-null value; zero return null or an error.
- **SC-004**: 100% of existing client queries that select `Category.path`/`Category.depth` continue to execute successfully and return a value after this change ships, with zero required client-side changes.
- **SC-005**: A developer introspecting the GraphQL schema can, within a single introspection query, discover that `Category.path`/`Category.depth` are deprecated and identify the exact replacement field to use instead, without consulting external documentation.

## Assumptions

- **Deprecation reason phrasing**: This codebase has an established convention for `@deprecated` reason text on prior deprecated fields (e.g., pointing callers at `metadata.name` or `status.resolved.storageClass` with a note that removal requires a future major API release). This specification assumes `Category.path`/`Category.depth`'s deprecation reasons follow that same established phrasing pattern, naming `status.resolved.path`/`status.resolved.depth` as the respective replacements, rather than inventing a new phrasing style.
- **Descendant fan-out mechanism already exists**: This specification assumes the CategoryTaxonomy controller's mechanism for propagating a hierarchy change down to pre-existing descendants (independently of any specific push) already exists and functions correctly, per prior work on the CategoryTaxonomy reconciler. This specification depends on that mechanism but does not build, modify, or extend it — the acceptance scenario in User Story 1 (descendant self-heals) is a verification of the combined effect of this feature's read-path fix together with that pre-existing mechanism, not a new capability being introduced by this feature.
- **"Never null" pre-reconcile fallback scope**: This specification assumes the pre-reconcile fallback value only needs to match today's existing admission-time computation's behavior (including its own pre-existing edge cases, such as an unresolved parent reference) — it does not introduce new guarantees about the fallback's accuracy beyond what already exists today; it only changes when that fallback is used (i.e., only before first reconcile, rather than always).
- **No new error or warning surface**: This specification assumes clients do not need a machine-readable signal distinguishing "value came from status.resolved" versus "value came from the pre-reconcile fallback" — the two-phase behavior is documented for developer understanding (FR-010) but is not exposed as a queryable field or flag, since the issue's scope is limited to fixing which value is returned, not adding new introspectable state.

## Dependencies

- Depends on the CategoryTaxonomy controller's existing hierarchy-resolution and status-writeback mechanism, which computes and maintains `status.resolved.path`/`status.resolved.depth` and propagates changes to descendants after an ancestor move.
- Depends on the existing `status.resolved` data shape already defined for CategoryTaxonomy resources (the `path`/`depth` fields within it).
- Related to this codebase's established schema-evolution principle of preferring additive changes and deprecation-before-removal for GraphQL-facing changes.
