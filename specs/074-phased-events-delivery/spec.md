# Feature Specification: Phased Events, Audit and Reliable Delivery

**Feature Branch**: `codex/074-phased-events-delivery`

**Created**: 2026-11-21

**Status**: Draft

**Input**: Specify #502 and its relationship with #139: independently phased operational Events, trusted AuditLog evidence, immutable OutboxEvent materialization and signed WebhookDelivery; durable Git notifications remain a separate source contract feeding recoverable processing and optional integrations. Preserve existing resource/watch/identity conventions, explicit failure and retention policies, bounded work and singleton Git.

## User Scenarios & Testing *(mandatory)*

### User Story 1 - Explain Activity Without Controlling Correctness (Priority: P1)

An operator or namespace administrator uses CLI/Admin diagnostics to understand why a resource failed to synchronize or reconcile. Repeated observations can form one bounded series. Resource processing remains correct when diagnostics are unavailable.

**Why this priority**: Useful operational explanations must not become an unreliable work queue or a hidden prerequisite for commerce operations.

**Independent Test**: Record authorized observations against known resource incarnations, list/watch them, retry submissions and advance a controlled clock through aggregation/expiry. Disable recording while reconciling the same resources.

**Acceptance Scenarios**:

1. **Given** an authenticated producer authorized for a resource scope, **When** it records an observation, **Then** CLI/Admin can read its bounded Markdown explanation and trusted producer/resource/report correlation without parsing the body for machine meaning.
2. **Given** one accepted observation whose response was lost, **When** concurrent retries reuse its idempotency identity, **Then** exactly one accepted observation contributes to the series count; reuse with conflicting content fails explicitly.
3. **Given** repeated equivalent observations and then a materially different explanation or new resource UID, **When** they are recorded, **Then** only equivalent observations aggregate; different explanations/incarnations remain distinct.
4. **Given** an expired series racing a new accepted observation, **When** expiry executes, **Then** a refreshed series is not deleted; retained readers/watchers receive the declared creation/modification/deletion behavior.
5. **Given** recording is disabled, throttled or unavailable, **When** an otherwise valid primary operation or reconciliation executes, **Then** its correctness does not depend on recording success and diagnostic loss is observable.

---

### User Story 2 - Inspect Trustworthy Operation Evidence (Priority: P1)

A security operator reviews who attempted an operation, which stage was reached and what outcome is known, using independently retained restricted audit evidence.

**Why this priority**: Informational diagnostic summaries cannot establish actor identity, committed outcomes or a durable security record.

**Independent Test**: Exercise authentication failures, authorization denials and successful/failed operations with different principals. Inject audit persistence failures at each required durability boundary, then inspect restricted query/export results.

**Acceptance Scenarios**:

1. **Given** a request and trusted authentication/service context, **When** authentication, authorization or operation outcomes are captured, **Then** correlated immutable records distinguish verified actor, claimed identity where allowed, initiating actor, authenticated service and stage-specific outcome without accepting spoofed identity.
2. **Given** an operation requiring durable pre-operation evidence, **When** that evidence cannot be accepted, **Then** the operation does not begin its protected side effect; the failure follows a declared policy rather than silently dropping evidence.
3. **Given** an operation that may already have committed, **When** outcome persistence or its response fails, **Then** recovery reports a truthful pending/uncertain outcome until verified; no record falsely claims rejection, rollback or completion.
4. **Given** an anonymous failure, denied target or deleted resource, **When** audited, **Then** evidence uses only verified safe context and does not invent a subject, namespace, resource UID or authorized resource existence.
5. **Given** a caller with ordinary Event access but no audit permission, **When** it queries, watches or exports audit data, **Then** access is denied without leaking evidence or target existence.

---

### User Story 3 - Recover Git Processing Across Disconnects and Replacement (Priority: P1)

The API receives Git changes through #139 and durably accepts the work needed to converge admitted resource state. Integrations that require occurrences can distinguish a replayable ref transition from a current-state resynchronization.

**Why this priority**: A disconnected stream or detached callback cannot be the only record of accepted Git work.

**Independent Test**: Change refs through native push and every supported API-originated write path. Cut the stream and stop processes before/after ref publication and consumer persistence; restart against retained storage, replay and compare accepted ref facts and durable work.

**Acceptance Scenarios**:

1. **Given** a committed branch/tag/custom-ref transition through a supported write path, **When** notification is consumed, **Then** its repository incarnation, ref, actual old/new object IDs, stable mutation/source identity and committed classification are recoverable independently of live observers.
2. **Given** refs change A -> B -> A while the API is disconnected within retention, **When** it resumes, **Then** occurrence-sensitive consumers can recover both committed transitions rather than receiving only the final A snapshot.
3. **Given** a crash between ref publication and notification completion, or partial multi-ref publication, **When** the singleton Git process recovers, **Then** committed versus uncommitted outcomes are verified before normal writes resume; no committed required work silently disappears or uncommitted transition is reported as committed.
4. **Given** duplicate delivery or a lost consumer acknowledgement, **When** either API replica recovers, **Then** durable work is accepted idempotently and source progress never skips unpersisted work.
5. **Given** an expired position or source epoch discontinuity, **When** reconnecting, **Then** the consumer receives an explicit gap: reconciliation may restore current state, but integrations do not claim missing historical occurrences were reconstructed.
6. **Given** a rejected push or newly created tag, **When** classified, **Then** rejection is an attempt/admission/audit outcome rather than a committed ref change, and tag creation does not itself assert a published release.

---

### User Story 4 - Preserve Integration Facts Before Advancing Sources (Priority: P2)

An integration subscription receives deliberately selected facts about committed resource or Git transitions, with enough retained information to survive source cleanup and interrupted routing.

**Why this priority**: Reliable delivery requires immutable historical facts and durable routing intent; it cannot be built on short-lived diagnostics or today's resource snapshot.

**Independent Test**: Use controlled durable resource/Git source fixtures and two materializer workers. Interrupt before/after fact creation, partial subscriber routing and checkpoint persistence; remove source resources and compact retained source history after materialization.

**Acceptance Scenarios**:

1. **Given** an eligible source transition, **When** it is materialized repeatedly, **Then** one logical immutable fact is recoverable under its complete source identity and separately identifiable delivery work exists for each eligible subscription.
2. **Given** interruption halfway through routing, **When** another authorized owner resumes, **Then** it completes the pinned routing decision without silently skipping destinations or applying a later configuration to earlier work.
3. **Given** an eligible deletion or condition transition, **When** its payload is formed, **Then** retained source facts establish the published meaning; a new same-name resource or current status cannot be substituted for historical data.
4. **Given** a retained immutable fact and pending delivery, **When** the original resource, diagnostic Event or source replay range expires, **Then** delivery still has its required payload/configuration under its own retention policy.
5. **Given** a missing source interval or unavailable required source facts, **When** processing continues, **Then** affected work is explicitly degraded/unsupported; a fresh list does not masquerade as complete occurrence history.

---

### User Story 5 - Deliver and Replay Without Losing Outcomes (Priority: P2)

An authorized subscriber receives signed integration notifications. Operators can inspect retries/dead letters and request an audited replay without retargeting or rewriting the original event.

**Why this priority**: External delivery must recover independently of primary resource processing and tolerate duplicate requests honestly.

**Independent Test**: Seed accepted immutable outbox facts and subscriptions, then use a controllable receiver for success, timeout, throttling, permanent rejection and lost responses. Exercise both providers, replace API replicas, lose provider acceptance responses, expire deduplication windows, change/disable subscriptions and exercise authorized replay.

**Acceptance Scenarios**:

1. **Given** one eligible delivery, **When** it is sent, **Then** the receiver sees the approved GitStore envelope without a provider wrapper, verifies the signature over actual transmitted bytes and identifies the stable CloudEvent across retries; GitStore maps provider receipts to logical delivery identity.
2. **Given** the receiver accepted a request but its response was lost, **When** the provider retries after API replacement, **Then** identity remains stable and the contract permits duplicate receipt without promising exactly-once effects.
3. **Given** a stale API reconciler, slow provider/destination or revoked credential, **When** processing resumes, **Then** stale state writes are rejected, new unauthorized handoffs stop, external disable uncertainty is visible and unrelated resource operations continue within bounded work.
4. **Given** exhausted retries, **When** the provider reports terminal failure, **Then** GitStore records confirmed terminal evidence; authorized replay retains original event identity and unambiguous replay provenance.
5. **Given** destination edits, subscription deletion/recreation or expired payload, **When** old work is retried/replayed, **Then** it is not silently retargeted and unavailable historical material produces an explicit error.
6. **Given** provider acceptance succeeded but its response was lost, **When** another API replica recovers, **Then** it reconciles the same submission identity; unresolved ambiguity after the provider deduplication horizon remains explicit and does not trigger blind resubmission.
7. **Given** a dev-only durability guarantee or unsupported production policy, **When** production activation is attempted, **Then** readiness rejects the configuration rather than claiming distributed delivery guarantees.

### Edge Cases

- A diagnostic Event's type, an admission finding's severity, a report's conclusion and a watch transition describe different things and cannot share an ambiguous enum.
- A producer retries after aggregation, expiry, failover or namespace recreation; counts, scope and idempotency cannot follow a reused name into another resource incarnation.
- A producer timestamp is wrong or malicious; server acceptance time bounds retention and cannot be extended by untrusted clocks.
- A status-only aggregation changes resourceVersion without changing desired generation; no fabricated applied revision or readiness condition is required for observations.
- Expired Event data may remain in CDC/journal postimages unless privacy/retention is coordinated; datastore TTL alone is not a verified watch deletion mechanism.
- A filter reveals a hidden resource, producer or audit target through counts, cursors, bookmarks or error detail; authorization must cover derived metadata as well as returned payloads.
- Source sequence numbers repeat after epoch reset; repository/subscription names are reused; event identity must bind incarnation and source.
- Native push, guarded/legacy commit, delete and tag RPCs have different entry paths; a push-only callback cannot represent all ref mutations.
- Git commits but the response, notification completion or API work acknowledgement is lost. Post-commit uncertainty is not rejection; storage exhaustion cannot erase committed required work.
- A multi-ref write partially publishes before process loss. The outcome must be verified per actual ref; a transaction-group identifier does not imply an unproven all-or-nothing guarantee.
- A historical deletion lacks UID/preimage, or a condition update lacks prior evidence. Never fetch a same-name replacement to fabricate the missing fact.
- A source checkpoint advances but routing is only partly persisted, a subscription changes mid-route, or a stale owner keeps running after lease expiry.
- A receiver accepts while the sender crashes; fencing cannot recall an HTTP request already sent. A 2xx receipt does not establish business completion.
- Bookkeeping creates more bookkeeping events. Exclude recursive sources by default and bound any explicitly enabled diagnostic alerts or audit-of-audit policy.
- Namespace teardown, credential revocation, retention reduction and operator replay can conflict; teardown must not silently move old private work to a new scope.

## Requirements *(mandatory)*

### Functional Requirements

- **FR-001**: Preserve five distinct contracts: Git source notifications (#139), operational Event (#503), AuditLog (#504), OutboxEvent (#505) and WebhookDelivery/EventSubscription (#506). No shared generic queue or identical retention/failure policy substitutes for them.
- **FR-002**: Keep #502 an umbrella with independently reviewable phases. Operational Events and AuditLog can ship independently; resource-derived outbox materialization need not wait for Git notifications; webhook delivery depends on durable outbox; Git-derived occurrence integrations require the #139 durability boundary. State prerequisites and evidence per slice, not initiative-wide completion by declaration.
- **FR-003**: Reuse canonical apiVersion/kind/metadata and applicable spec/status contracts. Reuse ObjectMeta/ObjectMetaInput and compatible UID-bound reference types; no parallel runtime metadata/reference schema. Mutable observations/progress follow the shared Condition/status.resolved convention where applicable; immutable audit records may omit status. Do not fabricate desired generations, applied Git revisions or ready conditions for records lacking that meaning.
- **FR-004**: Preserve bounded Markdown body representation with structured machine fields carrying classification/correlation. Runtime records are datastore-owned unless their owning contract explicitly declares Git-backed configuration. EventSubscription remains desired configuration; sharing Markdown/frontmatter does not make runtime evidence Git-authored.
- **FR-005**: Accept Event observations only from authenticated principals authorized for scope and reporting identity. Bind logical producer/instance provenance to trusted configuration/context; clients cannot impersonate another producer. Put cluster-component observations in a protected configured operational namespace, never a caller-selected tenant fallback.
- **FR-006**: Event spec/body MUST be immutable after acceptance; system-owned series/expiry can change. Distinguish Normal/Warning Event classification from existing admission notice/warning/failure and watch ADDED/MODIFIED/DELETED/BOOKMARK semantics. Reference #501 reports rather than duplicate detailed findings.
- **FR-007**: Define producer-scoped idempotency and a bounded retry/deduplication window. Equivalent concurrent/replayed submissions count once; conflicting reuse fails. Aggregation binds namespace incarnation, regarding UID, logical producer, reason/action/type, correlation and explanation semantics. Instance treatment is explicit. Different incarnations or material explanations cannot merge.
- **FR-008**: Event recording MUST remain best-effort relative to primary operations, with bounded buffering/retries and explicit overload/drop signals. Acknowledged observations follow the documented persistence/watch guarantee; recorder failure cannot block authoritative admission/reconciliation or become a reliable notification queue.
- **FR-009**: Provide cluster-bounded default/min/max diagnostic retention and constrained authorized namespace overrides. Proposed starting default is one hour, prospectively applied. Server acceptance time governs expiry; any refresh policy has a finite maximum series lifetime. Policy precedence, namespace teardown and deduplication retention are explicit and cannot delete required delivery/audit work.
- **FR-010**: Expiry MUST produce truthful explicit deletion/list/watch behavior with UID/version/deadline revalidation against concurrent refresh and replacement. Reuse existing CDC/watchjournal registration and its replay/bookmark/gap/backpressure semantics; do not assume ordinary cell TTL creates deletion events. Coordinate retained postimages with declared privacy lifetime and bounded cleanup lag.
- **FR-011**: AuditLog records MUST be trusted, immutable stage/outcome evidence with correlated request/operation identities. Distinguish authenticated service, verified initiator, claimed identity and canonical security action; author/committer text alone is not authenticated identity. Anonymous/denied/unknown-target cases use safe optional facts, not fabricated resources.
- **FR-012**: Classify audit coverage by explicit durability/failure policy: required acceptance before protected effects or bounded asynchronous capture with visible loss accounting. Required coverage must not silently degrade to best effort. Define bootstrap/emergency behavior and recursion limits. Pre-operation intent is not completion; uncertain committed outcomes require recoverable verification, not invented rollback or success.
- **FR-013**: Audit access/export and any optional audit watch MUST have independent permissions, retention and bounded access paths. Ordinary diagnostic access cannot grant audit access. Redact secrets and unnecessary before/after snapshots at source; append-only does not claim tamper-proof archival or legal compliance. Exported-copy/expiry responsibilities are explicit.
- **FR-014**: #139 MUST supply a retained, bounded, replayable committed-ref source over the existing signed service boundary. Record repository incarnation, stable mutation identity, ref, actual old/new object IDs, source identity/epoch/position, committed classification and safe trusted correlation. Cover every supported native/API-originated ref-write path; no WebSocket control-plane dependency.
- **FR-015**: Accepted required Git transitions MUST survive stream/API disconnection and singleton process replacement within the declared retention/recovery window. Recover and verify publication-to-notification uncertainty, including partial multi-ref writes, before normal writes resume. Never assume atomic Git-plus-database/log commits or reconstruct A -> B -> A occurrence history from the final ref snapshot. Exact storage/commit protocol is planning work requiring crash-boundary proof.
- **FR-016**: API consumers MUST durably accept required work before advancing Git-source consumption. Replays/lost acknowledgements/concurrent API replicas cannot create duplicate logical work or skip unpersisted obligations. Keep source acceptance, admission execution and downstream delivery progress independent; reuse #501's owning work/report recovery contract rather than turn a report into a queue.
- **FR-017**: Separate committed Git transitions from rejected attempts, symbolic/default-branch configuration changes and domain publication outcomes. A tag change does not assert release publication; a post-commit processing failure does not retroactively reject Git. Record rejected attempts through owning audit/report contracts and optional diagnostics; additional source variants require explicit semantics.
- **FR-018**: Git-source gaps, epoch changes and unsupported versions MUST be explicit. Reconcile authoritative refs to restore current state when permitted, but preserve a durable declaration of missing historical coverage for occurrence-sensitive integrations. Define continuity limits after repository deletion/recreation, external mutation and storage restoration; never silently reset a cursor.
- **FR-019**: Reuse the existing durable resource journal as the source for committed resource transitions. #139 remains a separate Git source adapter; neither raw Git notifications nor diagnostic Event resources replace resource-watch semantics. Source-specific cursors remain typed/opaque and are not interchangeable with entity versions or each other.
- **FR-020**: OutboxEvent MUST preserve one immutable, versioned, deliberately selected safe integration fact per supported semantic source transition. Identity includes complete source provenance/epoch/position and stable extraction semantics; repeated materialization preserves identity. CloudEvents is a wire projection, not a replacement internal resource envelope.
- **FR-021**: Persist required immutable facts and recoverable routing intent before source/materialization progress advances. Partial writes and interrupted subscriber fan-out require an explicit recoverable state machine; no assumed cross-partition transaction or process-local fallback. Separate source acceptance, fact materialization, routing and delivery outcomes.
- **FR-022**: Capture enough authoritative historical evidence for each advertised transition/deletion/condition payload before discarding source context. If existing journal images are insufficient, extend internal safe facts or restrict the advertised contract; do not fetch current state to reconstruct a deleted incarnation or previous condition. Public payload-free deletion watches need not expose additional private facts.
- **FR-023**: Subscription matching MUST use an explicit pinned activation/configuration identity and bounded recoverable routing. New subscriptions do not silently receive historical facts; update/delete/disable/recreation semantics cannot retarget accepted work. Enforce source/payload/export authority and recheck revocation without using a broad worker identity to bypass subscriber permissions.
- **FR-024**: Retain required payload and configuration provenance independently of source cleanup and diagnostic expiry for the declared delivery/replay lifetime. Missing source ranges or expired replay material produce explicit degraded/error outcomes. A fresh resource list cannot repair missing occurrences. Gap signaling itself must have a recoverable path independent of the missing interval.
- **FR-025**: Delivery MUST use a provider-neutral interface with a development implementation and Svix for production. The API owns asynchronous durable handoff/status reconciliation; the provider owns accepted-message queueing, HTTP delivery, signing, retry and redelivery. Preserve the approved GitStore CloudEvents envelope as the outgoing body without a provider wrapper. Do not expose webhook worker GraphQL commands or add a controller-manager sender/transport.
- **FR-026**: Persist enumerable delivery/run intents, stable submission identity and immutable provider/destination/policy bindings before remote calls. Across API replicas, reconcile ambiguous acceptance within proven provider idempotency/lookup guarantees; retain explicit uncertainty beyond those horizons rather than blindly resubmit. Persist authenticated outcome evidence with guarded idempotent projection. Provider acceptance is not receiver receipt; duplicate and out-of-order requests are permitted, with no exactly-once receiver-effect promise.
- **FR-027**: Preserve approved CloudEvents fields, safe data and semantic event identity across providers/retries/replay; verify Standard Webhooks-compatible signatures over actual transmitted bytes with refreshed timestamps and bounded replay tolerance. Provider transport IDs remain distinct from GitStore resource/event IDs. Pin authorized destination/key bindings and qualify provider signing, rotation and egress/TLS/DNS/redirect behavior. Stop new submissions on disable/revocation and expose external stop propagation uncertainty; do not promise recall or per-attempt GitStore authorization inside Svix.
- **FR-028**: Resolve versioned operator delivery profiles against explicit provider capabilities and finite retry/age/concurrency/rate/response/retention limits; reject unsupported requirements before activation. The dev implementation has bounded local queues/work and explicit process-local durability limits and is disallowed in production. Svix owns production retries; GitStore must not maintain a competing HTTP retry scheduler. A failing provider/receiver cannot block unrelated primary operations. Quotas/backpressure preserve accepted obligations.
- **FR-029**: Manual replay MUST be independently authorized/audited, retain original event identity and record separate replay provenance with an explicit receiver-deduplication policy. Expired payload/configuration cannot be synthesized, silently skipped or redirected to a recreated subscription.
- **FR-030**: Expose bounded indexed list/show and useful watch/status/export/replay operations under owning permissions and existing error conventions. Reuse canonical Relay item/edge/connection naming, descriptive query arguments and mutation-only input/Payload conventions. Follow the agreed connection pagination/namespace/query direction when integrating spec 073; do not assume its draft grammar has shipped or add competing per-kind filter contracts. No fetch-all sorting, live related-resource aggregates or exact total counts without a separately supported contract.
- **FR-031**: Keep internal diagnostics/audits/work delivery on authorized management/service surfaces. Storefront receives only separately defined curated business projections. #498 identity verification/recovery and #257/#285 behavior ingestion retain their own authority/privacy contracts; no secrets or arbitrary recipient/workflow selection in generic Event/watch/outbox paths.
- **FR-032**: Exclude audit/outbox/delivery bookkeeping from recursive default sources. Explicit best-effort diagnostic alerts do not acquire stronger source guarantees through an outbox. Publish bounded ingestion/loss, persistence, source lag/gaps, routing/backlog, lease/retry/terminal, expiry/cleanup and recovery signals without raw bodies, tokens or high-cardinality caller/resource labels.
- **FR-033**: Reconcile design 039's single delivery-cursor and sequence-only event identity with separate progress and complete epoch-aware identities. Update owning schemas/docs/source inventories together, distinguish proposed from implemented contracts, and add normal schema/generated/semantic checks to prevent drift. Issue completion requires its actual implementation/evidence; this umbrella specification does not close #139 or children.

### Production Requirements *(mandatory for core-service or load-bearing changes)*

- **PR-001 Replica Safety**: Verify affected paths with at least two APIs/two controllers, concurrent recording/materialization/delivery, duplicate ingestion, stale ownership, process replacement and supported old/new overlap. Retain exactly one active Git process and its repository/source storage; replacement must not overlap. Document unsupported upgrade/gap states rather than infer HA or sharding.
- **PR-002 Multi-User Security**: Exercise at least two namespaces and ordinary/admin/service/external-producer identities under pluggable AuthN/AuthZ/UserDir. Verify producer spoofing, resource/namespace/subscription UID reuse, denied/anonymous audit context, export/replay/revocation and sink authorization. Zero leaked protected facts, tokens or private postimages; use cluster-scoped ServiceAccount subjects `system:serviceaccount:<name>` with scope granted by bindings.
- **PR-003 Capacity**: Proposed, unmeasured per-phase acceptance targets: retained >=5M products/10K categories; 30 minutes of 500 offered diagnostic observations/s with 25% producer-idempotent retries and a declared distinct-series distribution, 100 audited operations/s, 100 eligible resource transitions/s, 100 delivery attempts/s across >=20 subscriptions and 10 committed Git ref mutations/s across native/API paths. Run independent phase qualifications and combined coexistence with separately attributed sustained authoring/native-push/reconciliation traffic. Record payloads (observations <=4 KiB, facts <=64 KiB), hardware, storage/retention budget and actual topology in planning. Event/audit acceptance p95 <=250 ms/p99 <=1 s; eligible-source-to-durable-fact p95 <=1 s/p99 <=3 s; healthy ready-delivery receipt p95 <=2 s/p99 <=5 s; unexpected healthy errors <1%. Preserve stricter existing domain/profile gates; targets are not measured capacity claims.
- **PR-004 Backpressure**: Bound bytes, cardinality, history/time ranges, workers/queues, deduplication/aggregation windows, source batches, routing/filter cost, lease lifetimes, destination concurrency/deadlines, retries and cleanup partitions before implementation. Proposed diagnostic producer buffer <=1,000 observations, <=3 attempts within 30 s; integration payload <=64 KiB; pages <=100 records. Test five minutes at twice each declared offered rate with explicit throttle/drop policy: diagnostic loss is accounted, required accepted work is never silently dropped. Restore healthy acceptance gates <=30 s after overload removal when dependencies are healthy; backlog drain has a separately measured finite bound.
- **PR-005 Capacity Evidence**: Extend existing `make capacity TARGET=api PROFILE=endpoint-split MODE=<diagnostic|alpha|production>`, `TARGET=namespace PROFILE=watch` and `TARGET=repository PROFILE=lifecycle` for management coexistence, retained watch recovery and native/API Git processing respectively. New event/audit/outbox/receiver probes are proposed additions to existing runners, not currently available selectors/flags. Receipts identify source/config/schema versions, verified retained dataset, actual resources/topology, complete duration, offered/completed/throttled/dropped work and domain-correctness oracles. Diagnostic or partial runs do not qualify production.
- **PR-006 Fault Recovery**: Reuse owned `make chaos CHAOS_PROFILE=api-restart`, `controller-restart`, `journal-interruption` where applicable; plan bounded receiver/storage failure and non-overlapping Git replacement fixtures through existing harness families. Injector/process restoration alone is not correctness evidence. After dependencies are ready, resume healthy processing <=60 s or an existing stricter deadline; interrupted work/backlog must meet its separately declared recovery bound. Source-expiry tests must demonstrate explicit gaps rather than fake recovery.
- **PR-007 Recovery and Integrity**: Verify every acceptance boundary with crashes before/after persistence/publication/checkpoint/outcome, lost responses, partial routing, epoch changes and concurrent expiry/refresh. Zero skipped accepted required facts, false success/rejection, duplicate logical observations/facts, cross-scope delivery or unreported gaps within supported windows. Preserve Git history/source storage, admitted pending work, per-replica checkpoints, retained catalogue and unrelated evidence. Tests cannot reseed or erase outstanding work to claim success.

### Key Entities *(include if feature involves data)*

- **Operational Event / Observation / Series**: Bounded explanatory record, unique producer observation and optional aggregation of equivalent observations; regarding UID, trusted producer, classification/correlation and Markdown explanation.
- **Audit Record / Coverage Policy**: Immutable trusted stage/outcome evidence and the explicit durability, retention/access and failure policy governing what can be claimed.
- **Git Source Fact / Mutation Identity**: Actual committed ref transition tied to repository incarnation and recoverable source continuity, independent of observers and resource admission outcome.
- **Source Position / Accepted Work**: Distinct durable progress and required processing obligation; source receipt alone is not admission or delivery completion.
- **Outbox Fact / Routing Intent**: Immutable selected historical payload and recoverable decisions about eligible subscription/configuration identities.
- **EventSubscription / Activation Revision**: Authorized desired routing/destination policy with stable incarnation and configuration provenance.
- **Delivery Provider / Handoff / Replay**: Dev and Svix adapters transport the approved envelope. One logical event-to-subscription delivery has durable submission/acceptance identity, guarded normalized outcome and bounded replay provenance, independent of provider transport IDs.
- **Retention / Continuity Policy**: Independent diagnostic, CDC, journal, audit, Git source, outbox/configuration and attempt/replay lifetimes plus declared loss/gap behavior.

## Success Criteria *(mandatory)*

### Measurable Outcomes

- **SC-001**: In every diagnostic outage/disable/overload fixture, otherwise valid resource processing converges without depending on diagnostic recording; all deliberately dropped observations are accounted under the declared policy.
- **SC-002**: Concurrent retry/aggregation/expiry fixtures produce exactly the expected accepted-observation counts, zero merges across material explanation/resource incarnations and zero deletion of refreshed series. Retained consumers observe the declared transitions and explicit replay expiry.
- **SC-003**: Every covered authentication/authorization/operation stage yields truthful authorized evidence or its declared persistence-failure outcome. Tests find zero spoofed verified actors, secret-bearing records or completion claims derived only from intent.
- **SC-004**: Every supported native/API Git write path passes disconnect/crash/restart tests with zero skipped required committed transitions inside retention. A -> B -> A preserves occurrence history; source gaps and uncertain/partial publication are explicit.
- **SC-005**: Repeated source delivery, concurrent/stale workers and interrupted routing create one logical fact and the expected recoverable subscriber work, with zero checkpoint advancement over missing obligations.
- **SC-006**: Pending integration delivery remains possible after source/resource cleanup within the declared retained-payload window; missing history or expired replay yields an explicit failure in every corresponding fixture.
- **SC-007**: Receivers verify exact payload/signature/identity across retries; lost responses may duplicate receipt without duplicating logical event identity. Both adapters preserve the approved envelope; production handoff/outcome/replay survive API replacement, including ambiguous provider acceptance, with zero silent retargeting or fabricated outcomes. Dev restart limitations are explicit.
- **SC-008**: Two-namespace, multi-principal, revocation and name-reuse fixtures reveal zero unauthorized records, payloads, derived metadata or deliveries across either API replica.
- **SC-009**: Each claimed phase and combined coexistence meet the full declared healthy workload/latency/error/resource gates; twice-load/fault runs preserve accepted-work correctness and recover within the declared bounds. Offered work and drops remain visible.
- **SC-010**: Contract/semantic checks reject deliberate drift in resource metadata/status/reference reuse, source identity, progress ordering, query conventions and error/authorization behavior. Each phase's docs and implementation claims agree; unbuilt phases remain explicitly unimplemented.

## Assumptions, Ownership and Scope

| Owner | Responsibility | Dependency boundary |
| --- | --- | --- |
| #139 | Git-service notification source/transport, retention and restart/reconnect guarantees. | Occurrence-sensitive Git adapters require durable source coverage; other #502 phases do not. |
| #503 | Operational Event recording, aggregation, bounded management access/watch and diagnostic retention. | Independent of AuditLog/outbox/webhooks; never a control-plane trigger. |
| #504 | Audit capture, immutable evidence, coverage/failure policy and restricted access/export. | Independent diagnostic retention; required audit acceptance may gate its declared protected operations. |
| #505 | Immutable integration facts, recoverable source/routing progress and gap semantics. | Resource source uses existing journal; Git source adds #139; no HTTP completion claim. |
| #506 | Subscription/delivery lifecycle, provider interface/dev/Svix adapters, wire security, normalized outcomes/replay. | Requires recoverable #505 facts; does not own primary admission/reconciliation. |

- This is one umbrella specification, not five new issue owners or a mandate for one implementation PR. Implementation planning must preserve independently testable slices and explicitly reconcile changes in the owning issues/designs.
- #501 owns AdmissionReport/admission diagnostics and accepted admission-work recovery. #498 owns identity notification ingress/delivery. #257/#285 own behavioral recommendation events; #282/#277 own curated cache revalidation consumers. Sharing infrastructure does not transfer authority or permit token/payload dumping.
- Current source baseline is `f8bd3bca` from refreshed origin/main. The Git service has signed write RPCs and post-publication detached admission, but no GitEvent notification RPC. Current resource watches use the durable CDC journal. Inventory/proposed design entries do not establish implemented Event/AuditLog/outbox/webhook contracts.
- Proposed operational default TTL is one hour; exact bounds, series lifetime, audit/source/outbox retention, wire fields, signing/key modes, storage protocol and budgets are planning decisions constrained by the acceptance guarantees. No unseen compliance requirement or general external broker/database is prescribed. Delivery is explicitly provider-based: dev locally, Svix in production; the approved outgoing envelope remains owned by GitStore.
- The Git commit/recovery protocol must prove required outcomes and scope unsupported external writers/storage restores explicitly. Reusing existing write receipts is an option to investigate, not evidence that every mutation already has durable notification receipts. No new generic event bus, full event sourcing, Git HA/replication/sharding/autoscaling or unconditional exactly-once promise.
- Spec 072 owns deployment capability discovery; spec 073 is a proposed shared query contract, not shipped grammar. Integrate through their owning conventions when available without blocking independent phase design on Storefront or copying Admin resources into public APIs.
- Use current canonical envelope/status/identity/action definitions; issue examples and design 039 are proposed and may require amendments. ADRs 0015/0018 remain Proposed until their core decisions are implemented; implementation plans must include applicable lifecycle/index updates rather than accept them through specification alone.
- This invocation creates specification/checklist artifacts only. No runtime schema, RPC, deployment, migration, live fault, production qualification or existing GitHub issue state changes.

## Existing Evidence and References

- [#502](https://github.com/gitstore-dev/GitStore/issues/502), [#139](https://github.com/gitstore-dev/GitStore/issues/139), [#503](https://github.com/gitstore-dev/GitStore/issues/503), [#504](https://github.com/gitstore-dev/GitStore/issues/504), [#505](https://github.com/gitstore-dev/GitStore/issues/505), [#506](https://github.com/gitstore-dev/GitStore/issues/506): existing owners and acceptance boundaries.
- [Git RPC contract](../../shared/proto/gitstore/git/v1/git_service.proto), [Git write paths](../../gitstore-git-service/src/grpc/server.rs), [post-receive dispatch](../../gitstore-git-service/src/git/hooks/mod.rs): current mutation/publication boundary to verify, not an implemented notification stream.
- [Watch journal](../../gitstore-api/internal/watchjournal/journal.go), [durable record facts](../../gitstore-api/internal/datastore/entities.go), [catalog CDC](../../gitstore-api/internal/datastore/scylla/catalog_cdc.go): existing source/replay authority and retained-image constraints.
- [Design 039](../../docs/implementation/039-resource-lifecycle-hooks.md), [ADR 0015](../../docs/ADRs/0015-resource-lifecycle-hooks.md), [ADR 0018](../../docs/ADRs/0018-controller-ownership-concurrency-and-fencing.md): proposed lifecycle/delivery/ownership decisions requiring explicit reconciliation.
- [Datastore-only resource inventory](../../docs/storage-groups/datastore-only.md), [capacity catalogue](../../tests/capacity/README.md), [owned chaos fixtures](../../tests/chaos/README.md): representation/command owners, not proof the proposed phases are implemented.
