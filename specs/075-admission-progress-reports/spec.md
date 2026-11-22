# Feature Specification: Admission Progress and Durable Reports

**Feature Branch**: `codex/075-admission-progress-reports`

**Created**: 2026-11-21

**Status**: Draft

**Input**: Specify #501: attached Git push admission progress, durable accepted admission execution and AdmissionReport, shared Git/GraphQL diagnostics, authorized report observation/export and rebuildable Git notes. Disconnect ends observation without cancelling accepted work. Preserve current completion behavior, canonical resource contracts and the retained Git source handoff designed under #139/spec 074.

## User Scenarios & Testing *(mandatory)*

### User Story 1 - Recover the Outcome of Accepted Admission Work (Priority: P1)

A developer submits resource manifests through Git or an existing Git-backed write operation. Once the change is committed and its processing obligation is accepted, admission continues independently of that connection. The developer can later determine which revision and ref were processed, what succeeded or failed, and whether execution remains incomplete or uncertain.

**Why this priority**: Progress output is useful only when accepted work and its outcomes survive losing the observer or execution process.

**Independent Test**: Use controlled committed transitions and existing admission fixtures. Interrupt source handoff, work acceptance, execution and report persistence; reconnect through another API replica and verify the submitted revision's eventual outcome without running a second admission engine.

**Acceptance Scenarios**:

1. **Given** a verified committed ref transition, **When** admission work is accepted, **Then** immutable target/input identity and an enumerable recovery obligation exist before source processing is acknowledged.
2. **Given** duplicate callback/source delivery or a retried client request referring to the same accepted command, **When** two API replicas process it, **Then** they recover one logical execution and reject contradictory input for the same identity.
3. **Given** a process stops after ref publication or during execution, **When** retained source/work recovery resumes, **Then** no accepted obligation silently disappears and no terminal success is inferred merely from ref publication or report creation.
4. **Given** a newer commit or repository/ref deletion while older work is pending, **When** recovery runs, **Then** the older submitted revision retains its own truthful outcome and cannot overwrite a newer admitted projection through stale execution.
5. **Given** uncertain external effects or unavailable historical input, **When** automatic recovery cannot safely prove completion, **Then** the report exposes the unresolved state/reason rather than blindly repeating effects or claiming successful admission.

### User Story 2 - Inspect Consistent Durable Reports and Diagnostics (Priority: P1)

A developer or operator inspects findings after the original request has ended. Git progress, GraphQL diagnostics and persisted reports identify the same revision, execution attempt and findings, with explicit limits and omission counts.

**Why this priority**: Durable findings let users correct failures and automate decisions without parsing transient terminal text.

**Independent Test**: Produce successful, rejected, partially admitted, superseded, timed-out and retried outcomes with known findings. Inspect authorized reports and compare the structured findings with the existing GraphQL response projections; a live Git observer is unnecessary.

**Acceptance Scenarios**:

1. **Given** an accepted execution attempt, **When** its report changes, **Then** clients see canonical resource metadata, queued/in-progress/completed state, applicable conclusion, timestamps and bounded summary without conflating controller readiness with admission completion.
2. **Given** the same report is read after reconnect, **When** the caller has current authority, **Then** findings and correlation remain stable; an explicit rerun creates another identifiable attempt and preserves the prior terminal outcome.
3. **Given** more findings than allowed by count or byte limits, **When** results are persisted or displayed, **Then** retained findings are deterministic and totals distinguish recorded, omitted and incomplete findings; truncation cannot turn failure into success.
4. **Given** a pre-receive rejection, **When** diagnostics are returned, **Then** they do not claim that proposed refs were committed or create a successful PostReceive report for them.
5. **Given** a multi-ref or partially successful push, **When** results are inspected, **Then** each accepted ref has its own applicable admission obligations and the push correlation does not report completion until all required children are terminal or explicitly unresolved.

### User Story 3 - Observe Git Push Admission Without Owning Its Lifetime (Priority: P2)

A developer running an ordinary Git push waits for post-receive admission of the submitted changes while progress streams to the terminal. The push normally finishes after admission reaches its outcome, rather than returning after ref publication or a short observation timer. The developer can disconnect or explicitly detach and inspect a report later; accepted work continues independently.

**Why this priority**: Attached feedback shortens the correction loop while durable execution remains independent of terminal/network behavior.

**Independent Test**: Use a real Git client through the supported Smart HTTP and proxy path with controlled admission delays. Verify incremental output, valid ref status and disconnect behavior using fixture-backed durable reports.

**Acceptance Scenarios**:

1. **Given** negotiated side-band-64k and admission lasting longer than two seconds, **When** a push is accepted, **Then** report/correlation identity and progress reach the client before admission completes, without buffering the whole response.
2. **Given** no sideband negotiation, quiet operation, delete-only updates or mixed ref results, **When** the push runs, **Then** it retains the same wait-for-admission completion policy while wire framing and optional output respect that mode.
3. **Given** a disconnect, explicit detach, slow-client failure or operational transport/authentication limit after durable acceptance, **When** observation stops, **Then** admission continues, locks/resources held only for observation are released and the eventual report remains inspectable; detachment is not admission completion.
4. **Given** upload interruption before acceptance or loss of the final ref-status response, **When** the client reconnects, **Then** it can distinguish uncommitted work from unknown acceptance and verified accepted work; no guessed commit outcome is presented as fact.
5. **Given** committed refs followed by failed or pending admission, **When** the Git response finishes, **Then** ref status remains about actual ref publication and progress identifies the separate admission outcome/report. Ordinary Git success does not certify admission or controller readiness.
6. **Given** healthy admission taking longer than 60 seconds, **When** the client remains attached and configured execution limits permit it, **Then** progress continues and the push waits for admission rather than automatically detaching at an unrelated observation deadline.

### User Story 4 - Keep GraphQL Write Behavior While Sharing Results (Priority: P2)

A client using existing Git-backed GraphQL mutations continues receiving the documented synchronous committed-manifest result and shared diagnostics. It can correlate that response, including an uncertain/disconnected response, with durable admission results.

**Why this priority**: The shared execution design must not force an unrelated asynchronous mutation migration or duplicate current admission rules.

**Independent Test**: Exercise existing successful, warning, rejected, superseded and disconnected GraphQL writes. Compare returned resource/commit identity and diagnostics with the corresponding durable execution without relying on Git sideband.

**Acceptance Scenarios**:

1. **Given** an existing Git-backed mutation, **When** committed-manifest admission completes, **Then** its current success/error behavior and extensions.admission convention remain recognizable, with additive report/attempt correlation and no independent second execution.
2. **Given** GraphQL and Git observations of the same accepted work, **When** findings are projected, **Then** reason/message/level and available file/field/location facts agree, while GraphQL response field paths remain distinct from manifest file/field paths.
3. **Given** a response timeout after durable acceptance, **When** the request ends, **Then** accepted work remains recoverable and the client is not told that committed refs rolled back or that admission completed without evidence.

### User Story 5 - Export and Project Authorized Admission Evidence (Priority: P3)

An operator exports retained admission findings for development tools and can inspect a Git notes projection. These are authorized views of durable reports and never become another source of truth.

**Why this priority**: Tool integrations and offline inspection reuse the same report semantics after core durability and observation are available.

**Independent Test**: Seed retained report attempts without live admission. Export bounded SARIF, rebuild notes from those reports, remove the projection and rebuild again; verify access, revision provenance and non-recursion.

**Acceptance Scenarios**:

1. **Given** authorized retained report attempts, **When** SARIF is exported, **Then** results retain producer/version, original revision/ref, diagnostic rule/severity/location and truncation provenance without leaking unauthorized findings.
2. **Given** missing or stale notes, **When** projection repair runs, **Then** refs/notes/gitstore/admission converges to a deterministic bounded view of retained reports; deleting notes does not delete or change report truth.
3. **Given** an unauthorized read/export, user write to protected admission notes or a projection update, **When** processed, **Then** disclosure/write restrictions apply and internal notes cannot recursively trigger admission or integration business events.

### Edge Cases

- The same commit returns to the same ref after another transition; repository/ref names are recreated; a producer version changes; an automatic retry is confused with a requested rerun.
- A ref is deleted and has no new commit, a push changes several refs, or ref publication succeeds only for a subset. A proposed commit is not evidence of acceptance.
- Admission succeeds for some manifests and rejects others; warnings or truncation do not hide a failed entry. Deletion findings still identify their captured resource incarnation.
- Callback execution and retained-source replay race with each other or with an existing synchronous GraphQL write.
- A stale execution resumes after ownership loss, a newer admitted revision or revoked authorization. Reusing an old principal snapshot cannot grant indefinite authority.
- Report persistence fails after a semantic effect; completion is uncertain until the owning effect can be verified. Reports are not themselves a work queue or proof of atomic external effects.
- A live observer misses intermediate progress, reconnects after report expiry, or receives only some report IDs in a multi-ref push. Human output is not a resumable machine event log.
- A finding contains secret material, a control character, an oversized source excerpt or a path that should not be disclosed to that observer.
- Producer locations are absent or invalid; multi-byte text makes byte offsets different from displayed columns. Missing locations are not invented.
- An export or notes rebuild races with report completion/expiry; protected historical content outlives ordinary report visibility in source history or backups.

## Requirements *(mandatory)*

### Functional Requirements

- **FR-001**: Separate ref publication, durable admission acceptance, execution/report completion and controller readiness. Every surface MUST identify which outcome is known; no post-publication admission failure may fabricate ref rejection or rollback.
- **FR-002**: Establish one accepted admission command boundary shared by existing post-receive callbacks, retained committed-transition ingestion and applicable Git-backed GraphQL writes. Preserve existing CommittedManifestAdmitter semantics and producer-owned validation; no parallel engine or duplicate semantic application.
- **FR-003**: Recover the ref-commit-to-work boundary through #139's retained committed-transition source/handoff designed in spec 074. Bind immutable input and enumerable accepted work before advancing the relevant consumer checkpoint. Callback and source replay use the same command identity. Until this prerequisite is implemented and verified, do not advertise restart-safe post-commit execution; never substitute a detached task or current-ref polling.
- **FR-004**: Execution identity MUST bind repository UID/incarnation, verified mutation/ref-transition identity, submitted revision or deletion target, admission phase, producer identity/version and processing scope. Different transitions, phases, manifest subsets or producer versions cannot alias merely because a commit SHA matches. A producer-neutral execution/attempt correlation is separate from public resource naming.
- **FR-005**: Distinguish idempotent delivery/resumption of one accepted attempt from a new execution attempt or explicit rerun. Preserve completed attempts and their findings; allocate new attempt identity/linkage rather than overwrite terminal history. Reject same-key/different-input requests. Bound automatic retries and expose unresolved effects for verified recovery rather than promising arbitrary exactly-once effects.
- **FR-006**: Preserve original revision/input and security provenance for recovery independently of current ref position. Prevent stale workers/older commits from overwriting newer admitted state using existing owned concurrency semantics. Explicitly classify superseded, skipped and uncertain work; never process current content while labeling it as an older revision.
- **FR-007**: AdmissionReport MUST be a datastore-only resource with canonical apiVersion/kind/ObjectMeta/spec and shared status/Condition/resolved conventions. Immutable spec identifies the execution attempt and producer/version; status records observed results and progress. Public targets omit mutationId; native publication mutation identity remains internal recovery evidence, while batch/execution/attempt/revision provide client correlation. No second metadata contract, Git-authored report authority or fabricated controller-ready condition.
- **FR-008**: Provide queued, in-progress and completed execution states and success, failure, neutral, skipped and timed-out conclusions. Conclusion is absent until established; uncertainty remains explicitly nonterminal/degraded until safely resolved. Record accepted/started/completed times, bounded summary and observed freshness. Stable completed attempt results cannot regress on delayed updates.
- **FR-009**: Represent built-in admission as an explicit versioned producer without requiring the unimplemented lifecycle-hook platform. Correlate a bounded sealed set of per-ref/producer obligations for each accepted push; child outcomes are independent and aggregate counts/completion are materialized asynchronously, not live resolver scans. Ref deletion and partial ref publication have explicit applicable/no-work semantics.
- **FR-010**: Reuse the canonical admission Diagnostic/RejectedError and extensions.admission contracts. Preserve reason, message, level, file and field; add optional source-region information in their shared owner where justified. Keep GraphQL response path separate from resource field path and source file location. Do not create competing error extensions or severity vocabularies.
- **FR-011**: Persist the same post-receive findings projected to authorized Git/GraphQL observers, with report/attempt/commit correlation. Pre-receive rejections remain rejection diagnostics with truthful proposed-ref context; they do not imply an accepted PostReceive report. Report findings are sanitized before persistence and every subsequent projection.
- **FR-012**: Bound report findings by both count and total encoded size. Proposed initial ceilings are 1,000 findings/attempt, 4 KiB/message and 1 MiB retained findings/attempt; retain deterministic findings plus total/omitted/incomplete accounting. Larger results use bounded report reads rather than an unbounded status response. Preserve AdmissionDiagnosticConnection over diagnostic values without requiring a global Node identity or another resource envelope. Counts/conclusion must account for all known failures even when messages are omitted.
- **FR-013**: Offer authorized report lookup, namespace-scoped bounded Relay connections and durable typed/generic watch through the existing resource journal. Reuse ObjectMeta, canonical references, mutation-only input/Payload conventions and spec 073's first/after, last/before, namespace and query direction where applicable. No competing filter dialect, process-local watch or mandatory dependency on advanced search capabilities. Reconnect, expiry and unsupported capabilities are explicit.
- **FR-014**: Enforce current pluggable AuthN/AuthZ and repository/namespace disclosure rules for execution submission, report lookup/list/watch, retries/reruns, export and notes. Requesting another report attempt uses admissionReport.create plus the original target's required authority; SARIF export reuses admissionReport.read and current diagnostic disclosure checks. Do not add rerun/export authorization verbs or a separate export permission. Typed commands do not permit callers to author report results. Bind signed cross-service commands/streams to receiver, method, target and allowed lifetime; observer/report IDs do not grant authority. Recovery retains provenance but cannot bypass revocation or disclosure constraints.
- **FR-015**: Preserve and validate receive-pack capability negotiation; advertise only implemented modes. Implement valid sideband report-status/progress/error framing and final flush behavior, quiet behavior and a truthful non-sideband fallback. Sideband negotiation changes transport encoding, not admission acceptance or failure semantics; protocol-v2 fetch is independent.
- **FR-016**: Stream typed bounded progress/diagnostics/ref-status through the existing signed Git-service RPC and Smart HTTP path, flushing incrementally through supported proxies. Human channel-2 text is a projection, never the stable machine-consumption contract. Version inter-service changes and preserve supported old/new behavior during rollout.
- **FR-017**: After verified acceptance, pushes MUST wait for their required post-receive admission outcomes by default, streaming progress when negotiated. Do not finish normally after ref publication or impose a separate short default observation timer. Quiet and non-sideband clients retain waiting semantics without unsupported output. Report/batch correlation is available as soon as durable acceptance is known. Explicit detach and operational transport/authentication limits end observation only. Align finite queue/execution/retry/transport budgets so healthy supported admission can complete while attached; timeout or unresolved execution is reported explicitly, never as successful completion. Do not assume arbitrary Git clients understand a new custom option.
- **FR-018**: Disconnect/explicit detach/transport failure/quiet/slow-observer behavior MUST NOT cancel accepted work or hold publication locks. Bound observer count, buffer, output and heartbeat cost; coalesce live progress and detach failed slow observers without losing durable execution/results. Exhausting optional output suppresses further optional output, not the admission wait. Quiet suppresses optional progress, not waiting or mandatory protocol/error correctness or later report access. Expired authentication ends or reauthorizes observation according to its transport contract.
- **FR-019**: Keep transport acceptance observable separately from admission completion: interrupted upload, verified rejected/uncommitted refs, accepted refs and uncertain final acknowledgement are different outcomes. Git final ref status reflects publication even when post-receive admission fails or remains pending. A separate CLI admission wait may use report conclusion for its own outcome; unrelated Ready status cannot satisfy the submitted revision.
- **FR-020**: Keep existing GraphQL committed-manifest synchronous completion and error behavior while executing through the shared accepted-work/result boundary. Add report/attempt correlation compatibly to existing diagnostics, including inspectable clean-success outcomes where applicable; do not force all mutations into an asynchronous job/Payload redesign. Request cancellation after acceptance ends that observer, not durable recovery.
- **FR-021**: Cap human remote diagnostics at 50 lines per push observation, with an omitted count/report reference when that output mode permits. Independently bound progress/heartbeat and total output; escape unsafe text and redact protected material. Structured findings and live progress loss have separate accounting.
- **FR-022**: Retain terminal reports/attempt evidence for a proposed default 90 days after completion; never expire required pending work/recovery evidence solely by age. Expiry/cleanup, source/CDC/backup copies and projection retention are independently documented; no claim of physical erasure from hiding a report. A report/replay request beyond retention yields explicit expiry rather than recreated historical findings.
- **FR-023**: Provide bounded authorized SARIF export using the canonical report findings and owning error conventions. Preserve producer/version, exact submitted revision/ref, stable rule/severity mapping, validated source regions and truncation provenance. Define export snapshot/continuation and size limits before enabling; no public unauthenticated artifact URLs or unbounded exports.
- **FR-024**: Implement refs/notes/gitstore/admission as a deterministic rebuildable bounded projection from authorized retained reports. Preserve per-ref/producer/attempt distinction within commit notes; do not use notes as work/recovery authority. Protect internal note writes, retain repository authorization on fetch, serialize competing projection writes and prevent recursive admission or default business-event extraction. Shared repository notes may contain only sanitized summary/reference facts safe for every authorized repository reader; narrower report-only findings remain behind report authorization and cannot be copied into broadly fetchable notes.
- **FR-025**: Keep API/controller communication on existing GraphQL HTTP/WebSocket where controller projection work is appropriate; do not add a second controller transport or expose admission execution-worker commands merely to move API-owned work. Evolve existing API/Git signed RPCs only for their owned Git/source/streaming responsibilities.
- **FR-026**: Publish bounded observability for source handoff/uncertainty, queue age, execution/attempt outcome, report persistence/truncation, observer detach/output, watch/export/notes lag and cleanup. Logs/metrics cannot contain raw manifests, diagnostics bodies, tokens or high-cardinality identities. Operational Event/AuditLog/outbox resources retain their own guarantees and are not admission correctness dependencies.
- **FR-027**: Deliver independently testable durability/report, shared-diagnostics/GraphQL, Git-observation and export/notes slices. Update design 039's identity/status/bounds/retention proposals and owning schema/protocol documentation together; include drift checks. Implementing PRs must explicitly assess ADR 0015/0018 core-decision status and index changes, without accepting the whole lifecycle platform by association.

### Production Requirements *(mandatory for core-service or load-bearing changes)*

- **PR-001 Replica Safety**: Verify work acceptance/execution, report/watch and any notes-projection ownership with at least two APIs/two controllers, concurrent duplicates, stale ownership, process replacement and supported old/new overlap. Git remains exactly one active process on retained storage; replacement must not overlap. Preserve pending obligations and reject unsupported source/RPC versions before activation.
- **PR-002 Multi-User Security**: Exercise at least two namespaces and ordinary/admin/service principals under pluggable AuthN/AuthZ. Cover cross-repository guessing, revoked/expired observers, source replay after deletion/recreation, safe diagnostics, retry/export and notes-read isolation. Zero unauthorized findings, identities, content or write effects; actor provenance and recovery authority remain distinct.
- **PR-003 Capacity**: Proposed unmeasured qualification: retained at least 5M products/10K categories, 100,000 report attempts, 30 minutes at 10 committed ref transitions/s with a declared bounded 10-manifest normal mix, 100 simultaneous observers and separate 5,000-manifest push probes. Include native pushes and GraphQL authoring with reconciliation coexistence. Healthy report lookup/list and persisted progress visibility p95 <=250 ms/p99 <=1 s and <=1 s/3 s respectively; unexpected errors <1%. Attached clients receive an initial accepted-work/report indication within two seconds of durable acceptance. Admission duration depends on declared work/producer bounds rather than a universal completion promise; preserve stricter existing domain gates.
- **PR-004 Backpressure**: Planning MUST assign finite queue/storage budgets, per-repository/producer work, retries/deadlines, observer buffers/rate/total output, findings, export/rebuild pages and retention. Proposed observer ceiling 64 KiB pending output and 256 KiB total/push, at most one heartbeat/5 s and ten progress updates/s with coalescing. Suppress optional output at its total-byte ceiling while continuing the wait; detach on actual slow-client/transport failure or explicit request, with accepted work preserved. Operational limits must cover the supported end-to-end admission budget, rather than add a short default observation cutoff. At twice offered load for five minutes, throttle/reject before acceptance or retain accepted work without loss. Slow observers and admission callbacks must not hold publication locks or starve unrelated operations.
- **PR-005 Capacity Evidence**: Extend existing make capacity TARGET=api PROFILE=endpoint-split, TARGET=namespace PROFILE=watch and TARGET=repository PROFILE=lifecycle under existing MODE selectors for report/coexistence, journal replay and sustained push/reconciliation respectively. Proposed streaming/report probes are not shipped flags. Receipts identify actual topology/dataset, payload/retention budgets, complete offered/completed/throttled work, supported proxy/version configuration and revision-correlated correctness. Normal full make pr-ready remains required for implementation PRs; partial/diagnostic runs do not certify production.
- **PR-006 Fault Recovery**: Reuse owned make chaos CHAOS_PROFILE=api-restart, controller-restart and journal-interruption where applicable, with bounded observer/proxy failure and non-overlapping Git replacement fixtures. Resume healthy recovery <=60 s after dependencies are ready, preserving pending work and independently measuring backlog drain. For a controlled five-minute admission outage at the declared 100-manifest/s normal mix, bound injected backlog to30,000 manifest units and prove >=200 units/s recovery against100/s offers, draining <=300 s after readiness. Execution timeouts remain bounded; uncertainty/retention gaps are explicit.
- **PR-007 Recovery and Integrity**: Crash-test before/after ref publication, source/work acceptance, each semantic effect, report update and observer final response. Verify zero skipped required accepted units, duplicate semantic applications, false ref/admission outcomes, overwritten prior attempts or cross-scope results within supported horizons. Preserve Git history, original admitted inputs, per-replica checkpoint state and unrelated evidence. Current-state relist, erased work or reseeding cannot establish historical recovery.

### Key Entities *(include if feature involves data)*

- **Accepted Admission Command**: Immutable verified target/input/security provenance and durable processing obligation shared by supported entry paths; distinct from a report or observer.
- **Execution and Attempt**: Logical unit bound to committed transition, phase, producer/version and processing scope; identifiable bounded attempts preserve prior terminal outcomes and distinguish resumption from rerun.
- **AdmissionReport**: Canonically enveloped datastore-only attempt evidence with observed progress, conclusion, bounded findings, timestamps and omission/uncertainty accounting.
- **Push/Batch Correlation**: Links proposed/accepted per-ref outcomes and the sealed required admission obligations without creating a second public job-resource authority.
- **Diagnostic**: Shared reason/message/level and optional file/field/location facts, projected consistently into existing errors/extensions, reports, human progress and export.
- **Observer Session**: Authorized finite-lived view of accepted execution; its lifetime, output loss or cancellation does not determine execution durability.
- **SARIF Export / Notes Projection**: Bounded authorized derived views with report/revision provenance, independent retention and rebuildability; neither is canonical execution state.

## Success Criteria *(mandatory)*

### Measurable Outcomes

- **SC-001**: Every supported acceptance/crash/restart fixture preserves all required accepted work and produces either verified completion or explicit unresolved coverage; no connection loss silently discards work.
- **SC-002**: Concurrent duplicates, repeated commits, ref/name recreation and retry/rerun fixtures yield the expected logical executions and distinct attempts, with zero overwritten terminal evidence or duplicate semantic applications.
- **SC-003**: Git and GraphQL findings match the retained report's reason, severity, target revision and available location facts in every fixture; clean success, failure, truncation, partial batch and uncertainty remain distinguishable.
- **SC-004**: On the supported proxy path, attached clients receive accepted-work identity within two seconds and progress before controlled admission completes; healthy execution exceeding 60 seconds remains attached until its admission outcome. Non-negotiated and quiet pushes retain the same wait semantics, and delete-only/multi-ref cases produce valid truthful outcomes.
- **SC-005**: Slow/disconnected/deadline-expired observers cause zero cancelled accepted work or observation-held publication locks; retained outcomes remain inspectable through another replica.
- **SC-006**: Two-namespace/multi-principal and revoked-observer fixtures disclose zero unauthorized reports, findings, exports or notes and permit zero unauthorized rerun/application effects.
- **SC-007**: All report count/byte, live output, pagination, export/rebuild and retention-boundary fixtures enforce declared limits and truthful omitted/expired/uncertain outcomes without converting failure into success.
- **SC-008**: SARIF and rebuilt notes reproduce authorized retained report/revision/producer facts deterministically, and projection repair/user-write rejection produces zero recursive admission or false canonical state changes.
- **SC-009**: Full declared healthy, twice-load and fault/recovery workloads meet the specified latency, bounded-resource and recovery/drain gates with concurrent service replicas and one active Git writer; offered work and throttle/loss accounting remain visible.
- **SC-010**: Compatibility/drift fixtures detect changed envelope/status/diagnostic identity, checkpoint-before-acceptance, duplicate engines, stale completion/readiness and invalid transport framing. Independently delivered phases retain accurate documentation and readiness claims.

## Assumptions, Dependencies and Scope

- This specification owns [#501](https://github.com/gitstore-dev/GitStore/issues/501). It does not implement or close it. Source baseline is f8bd3bca from origin/main; current request-scoped admissionreport and detached post-receive dispatch are not durable-report/recovery implementations.
- [Spec 074, draft PR #602](https://github.com/gitstore-dev/GitStore/pull/602) proposes the #139 retained Git source and source-to-accepted-work handoff. That contract is a planning dependency for crash-safe post-commit acceptance, not a shipped prerequisite already satisfied. Spec 075 owns accepted admission execution/reporting; the two specifications must converge on one command identity and checkpoint boundary. Event/AuditLog/Svix delivery phases are not required to implement reports or attached observation.
- Representation, source of truth and projection remain separate: reports use the GitStore envelope but are datastore-only; Git notes are derived. Shared ObjectMeta, status/Condition/resolved, references, diagnostics, authorization and journal behavior remain canonical owners. Detailed SDL/RPC/storage design belongs in planning.
- Report identity and retry history deliberately amend design 039's commit/ref/hook key and annotation replacement. Built-in producer/version and phase support ship without requiring AdmissionHookConfiguration or the full hook platform. Existing phases retain their defined meanings; GraphQL entry transport alone does not relabel post-commit work as pre-commit admission.
- Proposed 90-day terminal retention replaces design 039's reachability-based early expiration with one bounded completion-time policy, avoiding mandatory historical ancestry scans. Operators may configure a supported finite retention policy; pending-work evidence cannot be purged to meet it. Proposed aggregate/message findings limits replace the original potentially 64-MiB report allowance. These defaults require sizing and implementation evidence in planning, not a claim of measured capacity or regulatory compliance.
- Nonterminal work may be internally paused/degraded; absent conclusion is not success, neutral or skipped. Cancellation policy and supported effect recovery must be explicit before enabling an affected producer. No generic arbitrary-effect exactly-once promise or unconditional cross-service transaction is introduced.
- Stock Git ref status reports transport/publication, while an admission-aware CLI may independently fail an explicit admission wait. Human output/report links are not authorization tokens. Explicit wait/detach negotiation must use a supported declared mechanism; sideband alone does not request a particular admission policy.
- #239 retains protocol/fallback ownership; #240 fetch optimizations, #104 SSH implementation, controller readiness waits, webhook delivery, Git HA/replication/sharding and the complete lifecycle-hook platform are out of scope. DEP-016 closes only after real client/proxy streaming evidence, not report persistence or protobuf compilation alone.
- Planning must include normal schema/protocol/diagnostic drift checks, independently deployable rollout/rollback, operator documentation and applicable ADR 0015/0018 lifecycle/index work based on their actually implemented core decisions. ADRs remain Proposed through specification alone.
