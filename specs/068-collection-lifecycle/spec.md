# Feature Specification: Collection Lifecycle and Materialized Membership

**Feature Branch**: `068-collection-lifecycle`

**Created**: 2026-10-09

**Status**: Draft

**Input**: User description: "Namespace, Repository, Product, CategoryTaxonomy all implement the full lifecycle with each implementation filling in implementation gaps that existed for previous Resource types. Each has Git push -> validate -> admit; GraphQL mutations that delegate to git -> validate -> admit; controller reconciliation; owner references and deletion safety via finalizers. It is now time for Collections. In scope is #359 and #468. Design doc 035 also deferred some decisions. The ADRs now provide some guidance."

## User Scenarios & Testing *(mandatory)*

### User Story 1 - Authors manage Collections through one lifecycle (Priority: P1)

A catalog author creates, updates, or requests deletion of a Collection through Git or GraphQL. Both routes produce the same validated, admitted, auditable desired state rather than maintaining separate sources of truth.

**Why this priority**: Membership and lifecycle automation cannot be trusted if authoring routes disagree about the accepted Collection.

**Independent Test**: Submit equivalent valid and invalid manifests through both routes, including a Collection originally authored outside the system repository, and compare identity, revision, diagnostics, and admitted state.

**Acceptance Scenarios**:

1. **Given** an active namespace and repository, **When** an authorized author creates a valid Collection through either route, **Then** it has one new identity, repository ownership, admitted revision, and independently observable reconciliation state.
2. **Given** a Collection authored in a non-system repository, **When** it is updated through GraphQL, **Then** the change updates its original repository and source path; an omitted description preserves its existing Markdown body.
3. **Given** invalid selector syntax, an inactive owner, or an attempt to author system-managed state, **When** either route validates the request, **Then** it rejects the change before authoring where that check is available before commit and reports equivalent diagnostics.
4. **Given** a committed change whose admission fails, **When** the author inspects the outcome, **Then** the failure identifies the committed revision, retains the last accepted state where one exists, and never presents that older state as successful application of the new change.
5. **Given** concurrent changes to one Collection, **When** one supersedes the other before admission, **Then** the losing request reports a conflict rather than returning the other author's result as its own.
6. **Given** a path-only move, metadata-only edit, or byte-identical retry, **When** it is admitted, **Then** identity and generation follow the existing resource rules without spurious desired-state changes.

---

### User Story 2 - Merchandisers receive converged membership and counts (Priority: P1)

A merchandiser defines Collection membership by Product labels and can distinguish a valid empty Collection from one whose membership is pending, stale, or failed. Membership and its count update in the background rather than being recomputed whenever a reader opens the Collection.

**Why this priority**: This delivers #468 and removes expensive membership work from routine reads without hiding asynchronous behavior.

**Independent Test**: Create Collections covering every supported selector operator, change matching and non-matching Products, and compare completed evaluations with independently calculated expected membership and counts.

**Acceptance Scenarios**:

1. **Given** an absent or empty selector, **When** the Collection completes its first evaluation, **Then** it has zero members, `memberCount=0`, and `MembersResolved=True`; it is not interpreted as matching the whole namespace.
2. **Given** a valid nonempty selector, **When** matching Products are admitted, relabeled, or finally deleted, **Then** affected Collections converge to the exact matching set and count without another Collection edit.
3. **Given** a Product label change that leaves one Collection and enters another, **When** evaluation completes, **Then** both Collections are corrected, while unrelated Collections and Product-owned status are unchanged.
4. **Given** a selector edit or an unavailable evaluation dependency, **When** a reader inspects the Collection, **Then** it sees pending or failed freshness explicitly; an older completed result is never described as current.
5. **Given** a controller restart, duplicate event, or stale concurrent writer, **When** work resumes, **Then** membership and count converge without double counting or regression to an older completed evaluation.

---

### User Story 3 - Readers page a stable, authorized membership result (Priority: P1)

An authorized management client starts a products traversal from the latest completed Collection evaluation, sees when that evaluation was completed and what state it covers, and continues through a stable ordered membership set.

**Why this priority**: A fast count alone does not fix today's repeated selector scans or inconsistent membership across pages.

**Independent Test**: Traverse forward and backward while Product labels and the Collection selector change, sending subsequent requests to another API replica. Compare the traversal with its initial evaluation.

**Acceptance Scenarios**:

1. **Given** a completed evaluation, **When** a traversal starts, **Then** it selects that evaluation, exposes its freshness, and serves a bounded page without evaluating the entire namespace.
2. **Given** an existing traversal, **When** new Products arrive or labels or the selector change, **Then** subsequent pages remain bound to the original membership and order, with no duplicates, omissions, or silently substituted evaluation.
3. **Given** a Collection with no completed evaluation, **When** products are requested, **Then** the response explicitly reports membership unavailable or pending, not a fabricated empty connection or a synchronous full-scan fallback.
4. **Given** an expired, forged, cross-Collection, cross-namespace, or unauthorized cursor, **When** it is submitted, **Then** it yields an explicit safe error and no protected data.
5. **Given** a product in a pinned traversal is permanently removed or authorization no longer permits continuing that traversal, **When** continuation cannot safely return the pinned result, **Then** the traversal is explicitly invalidated and requires restart rather than silently returning an incomplete set or disclosing inaccessible data.

---

### User Story 4 - Operators delete Collections safely without deleting Products (Priority: P1)

An operator requests Collection deletion and observes termination while Collection-owned derived state is cleaned up. Products remain independently owned resources and are never deleted or relabeled to remove a Collection.

**Why this priority**: Materialized membership introduces cleanup responsibilities absent from ADR 0007's original immediate-deletion proposal.

**Independent Test**: Delete a populated Collection through each authoring route, interrupt cleanup, repeat deletion, and verify owner protection, eventual cleanup, and unchanged Products.

**Acceptance Scenarios**:

1. **Given** a Collection with members, **When** deletion is accepted, **Then** its manifest is removed, its deletion timestamp and finalizers become visible, and cleanup proceeds asynchronously.
2. **Given** a terminating Collection, **When** it is updated, recreated under the same identity, or evaluated by a stale worker, **Then** no new desired state or membership is installed and termination is not reversed.
3. **Given** interrupted cleanup or a repeated delete request, **When** another controller resumes, **Then** it continues the same operation and permanently removes the Collection only after its required cleanup is complete.
4. **Given** a repository containing an active or terminating Collection, **When** owner deletion is attempted, **Then** the existing repository-dependent safety rules continue to apply until the Collection is finally removed.
5. **Given** a finally deleted Collection, **When** its name is reused, **Then** it receives a new identity and cannot inherit old membership, status, cleanup work, or cursors.

---

### User Story 5 - Consumers securely observe and resume Collection changes (Priority: P1)

Controllers and authorized clients can list Collections and resume change observation across replicas, including admission, reconciliation, termination, and final deletion.

**Why this priority**: Reliable background membership and deletion cannot depend on a particular API process remaining alive.

**Independent Test**: Bootstrap a list and change stream under concurrent writes, reconnect through another replica, and verify all acknowledged transitions against their durable cursors.

**Acceptance Scenarios**:

1. **Given** writes concurrent with initial listing, **When** a consumer completes bootstrap and drains changes, **Then** it obtains complete current state with no list-to-watch gap.
2. **Given** a retained cursor, **When** a consumer resumes after API or controller replacement, **Then** it receives the subsequent ordered transitions or an explicit recovery response if continuity is no longer available.
3. **Given** status and deletion progress, **When** clients read or observe the Collection, **Then** ownership, generation, admitted revision, finalizers, deletion timestamp, and freshness agree across lookup, node, list, and change surfaces.
4. **Given** a slow consumer, **When** its bounded delivery budget is exceeded, **Then** it receives an explicit resumable or restart-required outcome without blocking healthy consumers.

---

### User Story 6 - Teams receive least-privilege Collection access (Priority: P1)

Human authors, service accounts, and controllers use configured identity and authorization providers. Collection membership is private management data, not a new storefront publication mechanism.

**Why this priority**: Membership, counts, and resumable cursors can reveal protected Products even when ordinary Product lookups are denied.

**Independent Test**: Exercise all Collection operations as reader, author, controller, and unauthorized identities across two namespaces and differently authorized repositories.

**Acceptance Scenarios**:

1. **Given** an identity lacking access to a namespace or repository, **When** it attempts any Collection read, mutation, membership traversal, count, or observation, **Then** no protected payload, existence, count, or cursor is disclosed.
2. **Given** an author without controller permissions, **When** it submits status, ownership, finalizer, or deletion-completion changes, **Then** those changes are denied.
3. **Given** a Collection controller identity, **When** it reconciles, **Then** it can update only its authorized Collection-owned state and cannot modify Product or CategoryTaxonomy status.
4. **Given** a caller whose Product access does not cover the full Collection membership, **When** it requests members or the cached count, **Then** the affected field fails closed unless an authorized-scope result has already been supported and evaluated; pagination never removes unauthorized rows after selecting a page.

---

### User Story 7 - Operators can rebuild and scale membership with evidence (Priority: P2)

Operators can deploy the feature over existing Collections, recover derived membership after failure, and assess a documented selector design against representative workloads rather than accepting an unmeasured historical recommendation.

**Why this priority**: #359 explicitly requires revisiting access patterns and comparing alternatives before selecting the query and migration design.

**Independent Test**: Build and exercise the Collection controller alongside the existing Product controller, collect the design-035 evidence, compare alternatives on the same corpus, then demonstrate the selected design's backfill, repair, rolling deployment, and production acceptance.

**Acceptance Scenarios**:

1. **Given** existing Products and Collections, **When** the feature is enabled, **Then** they become evaluated without repushing their manifests; incomplete backfill is visibly distinct from zero membership.
2. **Given** baseline and candidate measurements and the shipped Category products projection from #480, **When** a selector design is selected, **Then** the decision records all required comparisons, reusable behavior, selector-specific gaps, ownership, operational costs, rejected alternatives, and migration/rollback behavior before production storage changes are implemented.
3. **Given** interrupted evaluation, missing derived entries, or lost controller-local state, **When** recovery runs, **Then** it reconstructs correct membership and counts from accepted resources without making authors repair manifests.
4. **Given** sustained load and a hot selector, **When** the system reaches a work limit, **Then** it exposes lag or overload, preserves accepted work, and continues processing unrelated namespaces within the declared objectives.

### Edge Cases

- `NotIn` and `DoesNotExist` selectors may match Products lacking a key; their existing semantics must survive optimization, including selectors with no positive anchor.
- Combined `matchLabels` and expressions use logical AND. Contradictory but valid requirements yield zero members, not an evaluation error.
- A Product and Collection changed in the same accepted push converge to the final admitted state regardless of notification order.
- Product metadata-label edits change membership even when Product desired-state generation does not change. Collection generation alone cannot describe membership freshness.
- During a membership rebuild, a complete old evaluation may remain readable as explicitly stale; partially rebuilt results are never presented as complete.
- Product termination does not itself delete the Product. Management membership continues to use admitted matching Products until final removal; retirement and readiness do not introduce an implicit membership filter.
- A source file disappearing does not erase a terminating Collection before cleanup. Missing provenance causes an explicit authoring failure rather than a guessed repository or path.
- Collection deletion invalidates its traversals. Product deletion may invalidate an affected traversal as specified in Story 3; label edits alone do not.
- Authorization revocation takes effect on continuation even for a previously valid cursor. Snapshot stability never overrides current authorization.
- A syntactically invalid legacy selector or unavailable input source leaves an explicit failed or pending condition, never a success-shaped zero count.
- Routine Product changes affecting many Collections, broad Collections containing the entire namespace, and interrupted cleanup must not require unbounded per-request work or process memory.

## Requirements *(mandatory)*

### Functional Requirements

#### Authored lifecycle and parity

- **FR-001**: Git MUST remain the canonical authored source of Collection desired state. Create, update, and delete MUST be supported through both Git push and GraphQL with shared validation, admission, ownership, and lifecycle outcomes.
- **FR-002**: GraphQL creation MUST use the namespace's `gitstore-system` repository and conventional `collections/<name>.md` path. Update and delete MUST use admitted provenance, including non-system repositories and moved files. Mutation inputs MUST NOT introduce a repository or source-path selector.
- **FR-003**: Authors MUST be able to manage title, selector, supported Product-only target declaration, media declarations, metadata labels/annotations, and Markdown body. Existing valid manifests and selector semantics MUST remain supported. Omitted update bodies MUST be preserved.
- **FR-004**: Both entry points MUST reject invalid envelopes, invalid selectors, unsupported target kinds, cross-namespace targeting, inactive or terminating owners, protected ownership changes, and authored system-managed fields. Preconditions enforceable before commit MUST be checked before the ref moves.
- **FR-005**: Mutable updates and path moves MUST preserve identity. Desired-state or body changes advance generation; metadata-only edits and path moves do not. Effective admitted changes advance resource version; no-op retries MUST NOT create artificial generations. In-place name/namespace changes MUST be rejected; delete-and-recreate allocates a new identity.
- **FR-006**: A mutation MUST report success only for its own admitted revision, not an older accepted state or a concurrent writer's state. It MUST distinguish rejection before commit, rejection after commit, supersession, missing provenance, and time-bounded failure using the existing shared diagnostics convention. Push admission failures MUST remain observable after the push completes.

#### Membership, freshness, and traversal

- **FR-007**: Membership MUST be derived from the Collection selector over admitted Products in the same namespace, including across repositories when authorized. Collections MUST NOT gain author-managed member lists or direct add/remove-member mutations. Product variants are not direct members.
- **FR-008**: Selector evaluation MUST preserve equality, `In`, `NotIn`, `Exists`, `DoesNotExist`, missing-key behavior, AND composition, and the absent/empty-selector-means-zero rule. Omitted `targetRef` implies Product; no category-target or arbitrary-resource interpretation is added.
- **FR-009**: One logical Collection controller owner MUST derive membership and cached `status.resolved.memberCount` asynchronously. Routine member and count reads MUST NOT run a live selector scan or aggregate. Derived membership MUST NOT be stored as an unbounded member list in a manifest or status.
- **FR-010**: A completed evaluation MUST identify the Collection identity and generation it evaluates, the Product observation boundary it covers, and its completion time. The published member set and count MUST refer to that same complete evaluation. Collection generation alone MUST NOT be presented as proof of freshness after Product changes.
- **FR-011**: `MembersResolved=True` MUST mean a complete successful evaluation for the reported inputs, including a legitimate zero result. `Ready` MUST additionally require successful admission and a non-terminating, usable lifecycle state. Pending work, detected lag, evaluation failure, and termination MUST be distinguishable; `status.resolved` remains absent until the first successful evaluation.
- **FR-012**: Collection selector changes and relevant Product creation, label changes, and final deletion MUST enqueue every affected Collection, including old and new matches. Reconciliation MUST tolerate repeated, reordered, or replayed notifications and converge from current accepted state. Unrelated Product status updates MUST NOT cause namespace-wide reevaluation.
- **FR-013**: New traversals MUST use the latest complete evaluation available at traversal start and explicitly expose its freshness. After selector changes, the last completed evaluation MAY be served only as explicitly stale with its old evaluated generation. No completed evaluation MUST yield an explicit pending/unavailable result, not an empty-success or live-scan fallback.
- **FR-014**: Each traversal MUST pin membership and ordering to one evaluation, support forward and backward pages of at most 250 members, and remain usable through another healthy API replica. Label and selector changes MUST NOT change that traversal's member set. The ordering MUST preserve the existing creation-time and identity tie-break convention.
- **FR-015**: Traversal cursors MUST bind Collection identity, namespace, evaluation, ordering position, and authorized scope. Retained traversals MUST remain valid for at least 15 minutes absent deletion, authorization change, or explicitly reported data unavailability. Invalid or expired continuations MUST fail explicitly rather than restarting silently.
- **FR-016**: A permanently deleted member that cannot safely be returned from a pinned traversal MUST cause explicit traversal invalidation rather than silent omission. Product field values are not promised to be historical snapshots; the stable contract covers membership and order. Current authorization MUST be enforced for every page and count.

#### Ownership and deletion safety

- **FR-017**: Admission MUST establish system-managed repository owner references and preserve repository/namespace deletion protection for active and terminating Collections. Selector membership MUST NOT establish a blocking owner relationship on Products.
- **FR-018**: Accepted deletion MUST remove the source manifest and start visible, finalizer-governed termination. The result MUST distinguish termination started from already terminating and return the current lifecycle envelope; it MUST NOT claim immediate permanent deletion.
- **FR-019**: The Collection owner MUST clean up its derived membership, counts, evaluation state, and traversal accessibility before final removal. Cleanup MUST be bounded, retryable, and safe under concurrent replicas; it MUST NOT delete, relabel, or change ownership of Products.
- **FR-020**: Final removal MUST require a fresh authorized, concurrency-safe check of termination, required cleanup, and any blocking dependents recognized by the existing owner-reference contract. Unrelated finalizers MUST be preserved. Stale evaluation or cleanup work MUST NOT resurrect a removed Collection or affect a new identity reusing its name.
- **FR-021**: New author updates MUST be rejected while terminating, repeated deletion MUST resume the existing outcome, and name reuse MUST remain unavailable until final removal.

#### Observation and security

- **FR-022**: Lookup, global-node lookup, namespaced listing, typed Collection observation, and generic resource observation MUST consistently expose full lifecycle metadata and status. Collection observation MUST use the existing durable resource-watch contract, with race-free bootstrap, ordered resumable changes, bookmarks, and explicit expiry/unavailability outcomes.
- **FR-023**: Every acknowledged effective admission, status/evaluation change, termination/finalizer transition, and final removal MUST be durably observable across API replicas. Failed and no-op operations MUST NOT be represented as successful changes. Product input observation MUST likewise survive controller replacement without permanently missing membership changes.
- **FR-024**: Every Collection entry point, including nested membership/counts, cursors, status writes, and final removal, MUST authenticate and authorize through the configured pluggable providers before protected information or side effects are exposed. Read, author, watch, status, and final-removal authority MUST be distinct and auditable.
- **FR-025**: Collection reads and membership MUST remain private management/catalog surfaces. A caller lacking access to the complete membership scope MUST NOT receive an unscoped count or a page filtered after pagination; unsupported partial scopes MUST fail closed. This feature MUST NOT add storefront endpoints or public visibility rules.
- **FR-026**: Collection replicas MUST preserve status fields and condition types they do not own and reject stale writes at the write boundary. Product and CategoryTaxonomy controllers retain their existing ownership; consuming their events grants no authority to mutate their status.

#### Evidence-led design and deployment

- **FR-027**: #359 MUST include the comparison, documented selection, and implementation of the selected redesign within this feature. Selection MUST follow evidence from a working Collection controller alongside the existing Product controller, not assume design 035's historical recommendation is approved.
- **FR-028**: Before production storage redesign, the decision MUST compare the live-scan baseline, inverted/secondary label indexing, controller-maintained membership projections, and external search on the same dataset and objectives. It MUST address positive and negative selectors, per-namespace cardinality, matches per Collection, hot keys, Product mutation rate, affected Collections per mutation, read/count frequency, latency, freshness, resource use, write amplification, storage, tombstones, and repair/backfill cost.
- **FR-029**: The decision MUST document the selected query-first access patterns, a single derivation owner, migration and rollback, bounded partitions/work, authorization scope, stable traversal behavior, readiness, drift detection, and repair ownership. Alternatives incompatible with asynchronous Collection ownership MUST be explicitly rejected rather than silently retained as concurrent writers.
- **FR-030**: Existing Collections MUST backfill without repush. Partially built evaluations MUST remain unavailable as complete results; drift MUST be detectable and repairable from accepted resources. Process replacement or loss of local controller state MUST not lose accepted membership work permanently.
- **FR-031**: Independently deployed API and controller versions MUST have a documented compatibility window and rollout order. Unsupported mixed-version writes MUST be prevented or explicitly rejected before they can bypass finalizers or publish inconsistent evaluations. Rollback MUST not silently restore unsafe immediate deletion or live-scan fallback.
- **FR-032**: Implementation documentation MUST update the Collection reference, operational recovery/capacity guidance, design 035's decision status, and the ADR index. ADR 0007's conflicting live-scan, query-owned-only snapshot, mutation-routing, and immediate-deletion statements MUST be explicitly reconciled in an amendment or superseding decision before implementation is described as compliant. Acceptance status changes belong in the implementing delivery, not this specification-only change.
- **FR-033**: The #359 comparison MUST assess the shipped Category products projection from #480 before introducing parallel projection, pagination, readiness, or repair mechanisms. It MUST identify which existing behavior can be reused and which must change for selector-driven many-to-many membership, negative predicates, hot-label fan-out, and pinned evaluations. Category subtree behavior MUST remain unchanged; its successful delivery MUST NOT be treated as proof of Collection selector scalability or stable snapshots.

### Production Requirements *(mandatory for core-service or load-bearing changes)*

- **PR-001 Replica Safety**: Acceptance MUST use at least two API and two controller replicas with concurrent writes, stale writers, replay, rolling replacement, and independent process-local state. Exactly one Git service may be active; its replacement retains repository storage and never overlaps the old process. No Git HA or zero-downtime failover claim is in scope.
- **PR-002 Multi-User Security**: Reader, author, controller, and unauthorized identities MUST be exercised concurrently across at least two namespaces and differently authorized repositories, including count disclosure, node lookup, cursor replay, authorization revocation, and controller-only writes. Every denied operation must leave protected state unchanged and expose no protected payload.
- **PR-003 Capacity**: The proposed production acceptance floor is 5,000,000 admitted Products and 10,000 Collections, with a stress namespace containing the full Product population plus a separate isolation namespace. Use 20 labels per Product, selectors of 1-8 terms spanning every operator and negative-only selectors, one Collection matching all 5,000,000 Products, and a hot label affecting 1,000 Collections. Record p50/p95/p99/max membership cardinality and total derived membership volume. After warmup, sustain 60 minutes of 10 Git pushes/second with 10 Product changes per push, 10 Collection mutations/second, 100 membership page requests/second from at least 32 concurrent clients, and 1,000 Collection observers. Push traffic includes create, label-update, and final-deletion cases; Collection traffic includes selector edits and deletion. At minute 30, double write traffic for 60 seconds. Offered, accepted, rejected, late, and retried operations MUST be recorded separately.
- **PR-004 Backpressure**: Admission, observation, evaluation, fan-out, snapshot retention, cleanup, retries, and repair MUST have explicit finite concurrency, batch, time, and storage limits in the plan. Overload MUST be reported, not hidden through dropped scheduling or fabricated success. At the declared offered load, unexpected errors MUST stay below 0.1%, excluding intentional invalid/auth-denied probes; healthy namespace traffic MUST retain its page and watch objectives during the hot-label burst. Per-process warmed memory growth MUST be below 10% over the soak, CPU p95 below 80% of allocated capacity, and no process may exhaust its declared limits.
- **PR-005 Capacity Evidence**: Extend the existing root `make capacity` family with the planned `TARGET=collection PROFILE=lifecycle MODE=<diagnostic|alpha|production>` pair, reusing existing dataset, topology, load, observation, and fault collectors. This pair does not exist yet and is an implementation deliverable. Evidence MUST include the #359 comparisons, acknowledged dataset identity/cardinality, actual topology, offered load, all threshold results, and an independent selector/membership/count verifier. Diagnostic or reduced-scale evidence MUST NOT certify production readiness.
- **PR-006 Fault Recovery**: Reuse `make chaos CHAOS_PROFILE=api-restart`, `controller-restart`, and `api-pause-5s` against one explicit owned target with `CHAOS_CONFIRM=1`; include interruption during evaluation publication and finalization. Healthy replicas MUST preserve safe reads and authorization. Following restoration, warm observation and reconciliation readiness MUST recover within 30 seconds, and accepted pending work MUST converge within 5 minutes. Cold rebuild over the declared dataset MUST complete within 30 minutes with explicit pending state; injection success alone is not recovery evidence.
- **PR-007 Freshness and Integrity**: At the declared workload, member pages MUST complete at p95 <=150 ms and p99 <=500 ms; newly completed membership/count visibility after relevant accepted changes MUST be p95 <=5 seconds and p99 <=15 seconds. Durable transition visibility MUST be p95 <=1 second and p99 <=3 seconds, and replay of 10,000 retained transitions p95 <=5 seconds. The write burst backlog MUST drain within 5 minutes after baseline load resumes. Eligible deletion cleanup, including the broadest Collection, MUST finish within 5 minutes after dependencies are healthy. Rebuild and fault windows MUST be reported separately rather than hidden in steady-state percentiles.

### Key Entities

- **Collection**: Namespace-scoped, Git-authored merchandising definition with title, Product selector, optional supported target declaration, media declarations, description, immutable identity, provenance, and system lifecycle state.
- **Selector**: Existing label requirements combined with AND; absent or empty requirements select no Products.
- **Membership evaluation**: Complete derived member set and count for one Collection identity/generation and a stated Product observation boundary, with completion time and freshness.
- **Membership traversal**: Time-bounded, authorized view of one completed evaluation with stable membership and ordering, independent of API process.
- **Collection lifecycle status**: Admission, evaluation readiness/failure, observed inputs, termination, and cleanup progress; authored desired state and controller status remain separate.
- **Repository owner relationship**: System-managed dependency preserving owner deletion safety until Collection final removal; distinct from non-owning selector membership.
- **Durable Collection change**: Resumable admitted or lifecycle transition supporting client observation and controller recovery.

## Success Criteria *(mandatory)*

### Measurable Outcomes

- **SC-001 Authoring parity**: Every case in the valid/invalid authoring matrix produces equivalent accepted intent or equivalent rejection across both authoring routes; zero mutations report a previous or competing revision as their own success. Covers FR-001-FR-006.
- **SC-002 Membership correctness**: For every operator, empty selector, label transition, selector edit, deletion, replay, and repair case, the completed member set exactly matches an independent expected set and the cached count equals its cardinality. No incomplete result is presented as complete. Covers FR-007-FR-012, FR-030.
- **SC-003 Traversal stability**: Every retained authorized forward/backward traversal returns its pinned members exactly once in the documented order despite label edits and replica changes, or explicitly invalidates under the documented deletion/authorization/expiry conditions; zero silent snapshot switches or omissions occur. Covers FR-013-FR-016.
- **SC-004 Deletion safety**: Every eligible deletion exposes termination before removal and completes within 5 minutes under the declared healthy-dependency envelope; zero Products are changed or removed as a side effect, zero protected owners are removed prematurely, and zero stale workers resurrect old identities. Covers FR-017-FR-021.
- **SC-005 Isolation**: The complete multi-identity access matrix discloses zero unauthorized member identities, counts, resource existence, or cursors, and makes zero unauthorized status, finalizer, or desired-state changes. Covers FR-024-FR-026.
- **SC-006 Observation continuity**: Across concurrent service instances, rolling replacements, and reconnects, 100% of acknowledged effective Collection transitions are observable or recoverable through explicit resynchronization; stale writes never regress state. Covers FR-022-FR-023, FR-026, FR-031.
- **SC-007 Sustained user experience**: Across the full 60-minute production workload, page latency is p95 <=150 ms/p99 <=500 ms, membership freshness is p95 <=5 seconds/p99 <=15 seconds, and unexpected errors remain below 0.1%. Observed transition visibility is p95 <=1 second/p99 <=3 seconds, with 10,000-change replay p95 <=5 seconds.
- **SC-008 Bounded recovery**: Warm readiness returns within 30 seconds after a fault is removed; accepted backlog and burst work converge within 5 minutes; cold reconstruction completes within 30 minutes; exact membership and counts are restored without author intervention.
- **SC-009 Stable resource use**: At the declared dataset and workload, warmed process memory grows by less than 10%, CPU p95 remains below 80% of allocation, and no accepted work is lost through queue overflow or exhausted retention. Hot-label traffic does not violate healthy-namespace page/observation objectives.
- **SC-010 Evidence and rollout**: All four design alternatives have comparable evidence or explicit measured/contractual disqualification before selection, including a documented reuse/gap assessment of the shipped Category products behavior; the selected design passes the membership, backfill, repair, compatibility, and rollback acceptance matrix. Existing Collections require zero manifest resubmissions. Covers FR-027-FR-033.

## Clarifications

### Session 2026-10-09

- New member traversals use the latest completed evaluation, with explicit freshness and stable subsequent pages. They do not wait for every Product change acknowledged before traversal start.
- #359 includes comparing alternatives, documenting the selection, and implementing the selected redesign in this feature. The comparison is an evidence gate, not permission to adopt design 035's historical recommendation without measurement.
- The user identified #480's shipped Category products design as related precedent, with the caveat that Collection membership is selector-based. It is an explicit reuse/comparison input, not a predetermined Collection design.

## Assumptions, Dependencies, and Scope Boundaries

- **Sources**: [#359](https://github.com/gitstore-dev/GitStore/issues/359), [#468](https://github.com/gitstore-dev/GitStore/issues/468), [#480](https://github.com/gitstore-dev/GitStore/pull/480), [design 035](../../docs/implementation/035-collection-membership-materialization.md), [ADR 0007](../../docs/ADRs/0007-collection-lifecycle.md), [ADR 0017](../../docs/ADRs/0017-aggregate-fields-via-async-materialization.md), [ADR 0018](../../docs/ADRs/0018-controller-ownership-concurrency-and-fencing.md), [Product lifecycle](../055-product-lifecycle/spec.md), and [CategoryTaxonomy lifecycle refinements](../057-categorytaxonomy-path-freshness/spec.md).
- Existing Collection frontmatter, selector validation, admitted records, and read surfaces are the baseline, not new resource formats. Current code still evaluates membership on reads and lacks the Collection mutation/controller behavior requested here; documentation describing that behavior as already implemented is not evidence.
- #480 is already merged into this feature's starting revision. Its Category products projection provides bounded reads, resource-bound cursors, durable readiness, Product-list authorization, and repair/backfill precedent. Its resolved ancestor relationships identify a bounded set of affected categories per Product; Collection selectors must discover potentially many affected Collections, including negative matches. Category keyset cursors do not themselves pin a completed membership evaluation. Its rollout requires draining old API writers and confirmed repair before enabling reads, and its production capacity acceptance remains tracked separately in #451; none of these facts proves the new Collection objectives.
- ADR 0017's asynchronous aggregate rule is accepted. ADRs 0007 and 0018 remain proposed. This feature adopts Collection ownership and conditional/idempotent concurrency requirements without implying implementation of a general lease/fencing system or accepting unrelated parts of proposed ADRs.
- The confirmed materialized-membership contract replaces ADR 0007's live-query-only membership model for this feature. Derived membership is neither author-managed nor an unbounded status member list. Physical storage, indexes, cursor representation, and migration design remain planning decisions gated by #359.
- Preserve design 035's evidence-before-selection ordering: first establish working controller behavior and bounded diagnostic measurements, then compare and document candidates, then implement the selected production redesign. A diagnostic controller slice is not production certification and does not authorize live aggregate reads.
- Product-only `targetRef` follows the existing Collection reference and validation contract. ADR 0007's example suggesting a CategoryTaxonomy target is not adopted. Retirement, readiness, and publication do not silently alter label-based management membership.
- File media resolution remains deferred, consistent with ADR 0007's Phase 2 boundary. Preserve media declarations and existing validation, but do not add File lifecycle work or make unimplemented media resolution a new readiness prerequisite.
- Publication/release workflows, storefront schemas, explicit/manual membership, arbitrary-resource membership, a general authorization-provider redesign, controller-wide lease infrastructure, and Git replication/sharding are outside this feature.
- The production envelope and deadlines above are proposed acceptance requirements, not measured capabilities. Research must report any infeasible objective for explicit scope revision; it must not silently lower thresholds, omit broad/negative selectors, or substitute smaller fixtures for production evidence.
- Planning must include directly related documentation and ADR lifecycle updates as explicit tasks. No ADR is marked Accepted and no issue is claimed closed by this specification alone.
