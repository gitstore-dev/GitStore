# Feature Specification: Admin and Storefront Endpoint Split

**Feature Branch**: `070-admin-storefront-split`\
**Created**: 2026-11-04\
**Status**: In progress\
**Input**: User description: "storefront/admin split (#495). Use a worktree rebased against origin/main. Choose the least complex pre-GA implementation and amend ADR-0012; a staged compatibility transition is unnecessary."

**Closure scope (2026-11-06)**: The implemented endpoint/schema/security boundary, local consumer migration, functional cutover/recovery checks and T060–T061 diagnostic/alpha acceptance are complete. Clean `06f10dfe`, including the delayed-create race fix, passed the full default `make pr-ready` and the complete sustained workload: 260,039 HTTP iterations, 4,320 admitted management operations and 2,160 acknowledged/durable-watch-verified native pushes, with zero dropped work or failed management/push operations. Physical API/controller replacements and the unchanged alpha resource/recovery gates passed. Earlier failed and interrupted runs remain nonpasses. This is not production capacity certification: management visibility p95 was 1.170 s against the separate 1 s production target; real publication reads/target binding, adversarial WebSocket pre-decode bounds and external consumer release coordination remain open. The spec therefore remains **In progress**.

## User Scenarios & Testing *(mandatory)*

### User Story 1 - Preserve management access through an explicit Admin endpoint (Priority: P1)

As a merchant, operator or controller, I use an explicit management endpoint to author and observe current resources, while storefront callers cannot discover or invoke those management operations through their endpoint.

**Why this priority**: The split must preserve authoring, reconciliation and access control before exposing a buyer-facing boundary.

**Independent Test**: Exercise existing management queries, mutations and durable watches through Admin with authorized users and controllers; submit the same documents and identifiers to Storefront and verify no management data or effects are returned.

**Acceptance Scenarios**:

1. **Given** an authorized merchant and an unpublished Product, **When** the merchant reads or updates it through Admin, **Then** existing authoring permissions, committed-manifest admission and resource identity/version behavior are preserved.
2. **Given** authorized controller replicas, **When** they resume resource watches and submit status or completion operations through Admin, **Then** existing journal, conflict, identity and finalizer safeguards remain in effect.
3. **Given** a Storefront caller, **When** it requests management fields, mutations, draft relationships or Admin identifiers, **Then** no management data or side effects are produced, including through aliases, fragments, lists, counts and node lookups.
4. **Given** an anonymous caller, **When** it requests Admin introspection, **Then** access is denied; authenticated introspection remains subject to the configured authorization and request limits.

---

### User Story 2 - Deploy unambiguous endpoint routes in one cutover (Priority: P1)

As an operator, I deploy one named Admin listener and one named Storefront listener with fixed endpoint paths, and upgrade matching clients in the same pre-GA release without routing modes, temporary aliases or enablement flags.

**Why this priority**: Predictable paths and one migration eliminate temporary deployment states while allowing management traffic to remain private.

**Independent Test**: Start the two named listeners with default and customized ports, probe their fixed paths and run matching controller, admin UI and operator clients. Repeat deployment and rollback as coordinated version sets.

**Acceptance Scenarios**:

1. **Given** separate Admin and Storefront listeners, **When** clients use `A:/admin/graphql` and `S:/storefront/graphql`, **Then** each selects only its intended schema; `/graphql` and the opposite endpoint's prefixed route return 404 on both listeners.
2. **Given** customized named ports or a reverse proxy, **When** a controller lists resources and opens a watch through its complete Admin URL, **Then** both use the fixed `/admin/graphql` path with unchanged list/watch semantics and no layout-dependent path selection.
3. **Given** a deployment being upgraded, **When** old API replicas are removed from routing and the new API and matching client configuration are activated, **Then** clients use the new paths without an alias or a second migration release.
4. **Given** a failed cutover, **When** the preceding API version, client paths and routing are restored together, **Then** management operations resume without changing authored resources.
5. **Given** zero/equal named ports, a collision with Git HTTP/catalog or the removed generic `api.port` setting, **When** configuration is validated, **Then** startup fails with an actionable diagnostic rather than selecting a routing mode or applying a compatibility mapping.
6. **Given** either playground, **When** the operator switches Admin/Storefront endpoints or edits a URL, **Then** its schema/editor session changes without automatically executing a document, and credentials/custom headers and the previous subscription connection are cleared before any new endpoint request.

---

### User Story 3 - Keep incomplete Storefront reads safe and explicit (Priority: P1)

As a storefront integrator, I have a stable endpoint whose availability reflects publication and target-binding readiness, without an extra feature switch or a fallback that exposes drafts.

**Why this priority**: Endpoint separation must be deliverable before the publication lifecycle, without pretending buyer reads are implemented.

**Independent Test**: With publication or trusted target binding absent, call Storefront and verify a stable unavailable response while Admin remains functional. Use controlled publication fixtures to verify the boundary against two targets and differing draft/published values before any real Storefront read is enabled.

**Acceptance Scenarios**:

1. **Given** publication-backed reads or trusted target binding are unavailable, **When** Storefront receives a request, **Then** it returns HTTP 503 with `STOREFRONT_UNAVAILABLE`, no catalog data and no fallback to management reads; Admin remains usable.
2. **Given** publication fixtures with a published Product and a newer draft, **When** a target-bound Storefront read is exercised, **Then** it returns only that target's active published version across direct, node, relationship, list and count paths.
3. **Given** an Admin identifier or another target's resource, **When** it is supplied to a supported Storefront lookup, **Then** the result is not found and reveals no management or cross-target data. A Storefront identifier is likewise not found through Admin.
4. **Given** a publication or target-binding dependency fails after reads become available, **When** the same request is retried, **Then** it fails closed without consulting current management rows or serving another target's cached result.
5. **Given** normal deployment configuration, **When** the API starts, **Then** Storefront routing exists without a separate enablement flag. Availability follows prerequisites, not an operator opt-in step.

---

### User Story 4 - Keep public traffic from overwhelming management work (Priority: P2)

As an operator, I apply endpoint-appropriate access and traffic policies so public request pressure does not expose management operations or create unbounded work.

**Why this priority**: Separate schemas alone do not isolate resource consumption or browser access.

**Independent Test**: Run the declared mixed public/management workload and overload probes against two replicas; verify endpoint-specific limits, origin handling, durable watch resumption and recovery.

**Acceptance Scenarios**:

1. **Given** oversized or over-complex requests to either endpoint, **When** limits are exceeded, **Then** requests receive bounded, structured rejection before expensive data access.
2. **Given** public request pressure and valid controller traffic, **When** Admin operations and watches continue, **Then** their declared latency and recovery objectives hold; Storefront does not inherit controller exemptions.
3. **Given** untrusted origin or buyer-IP headers, **When** Storefront receives them, **Then** configured origin restrictions apply and unverified identity/IP claims cannot bypass throttling.
4. **Given** one API replica is replaced under load, **When** requests and subscriptions reconnect, **Then** surviving replicas preserve endpoint isolation and controllers resume from durable cursors without lost acknowledged changes.

### Edge Cases

- An endpoint's port changes: its prefixed path stays fixed, and clients update only the configured origin/port as needed.
- Both named endpoints choose the same port: reject the conflict rather than select a schema by startup order.
- An Admin mutation document is sent with Storefront credentials, through fragments, aliases, GET, a persisted document or a subscription handshake: no transport may select the Admin boundary.
- Storefront prerequisites are missing at startup or fail later: return unavailable with no draft fallback; do not prevent independent Admin operation.
- A well-formed opposite-endpoint global ID is supplied: return not found without a datastore lookup; preserve existing malformed-ID diagnostics.
- A published resource changes release: its Storefront identity remains stable; target/snapshot/document caches do not leak data or reuse a stale availability response after recovery.
- A client uses the old `/graphql` URL on either listener: return 404 with no redirect, fallback or legacy mode.
- A playground peer URL is not configured, or changes origin: require explicit browser-accessible input and fresh credentials; never infer private/public URLs or forward an Admin token.
- CORS preflight, introspection, playground and subscription paths must obey the selected endpoint and listener boundaries. Storefront subscriptions are not introduced by this feature.
- A replica lacks the new routing contract: exclude it during the initial cutover; do not claim old/new endpoint compatibility.

## Requirements *(mandatory)*

### Functional Requirements

- **FR-001**: The system MUST expose distinct Admin and Storefront schema contracts. Storefront MUST NOT include management mutations, watches, resource status, finalizers, generation/resource-version metadata, or draft-only fields. Reverse-proxy aliases alone MUST NOT count as schema isolation. (Stories 1, 3)
- **FR-002**: Admin MUST preserve current control-plane query, mutation, authorization and durable subscription behavior, including Git-backed authoring, shared admission diagnostics and identity/version-bound deletion completion. (Story 1)
- **FR-003**: Admin and Storefront MUST have separate listeners, serving only `/admin/graphql` and `/storefront/graphql` respectively. `/graphql` and the opposite endpoint's prefixed path MUST return 404 on each. Their playground paths MUST be `/admin/playground` and `/storefront/playground`. Paths MUST remain fixed when ports/origins change. (Story 2)
- **FR-004**: Configuration MUST use `api.admin_port` and `api.storefront_port`, consistent with `api.git_port` and `api.grpc_port`. Remove generic `api.port` in this implementing release. Zero/equal ports, collisions and legacy generic-port configuration MUST fail validation explicitly; no shared mode or compatibility mapping is provided. Root operational endpoints MUST belong to the Admin listener. (Story 2)
- **FR-005**: The implementing release MUST migrate controller, admin UI, operator CLI, Makefile defaults, Compose examples and capacity/tooling callers together. It MUST NOT introduce a legacy alias, redirect, deprecation layer, automatic old-path fallback or migration-mode configuration. Coordinated initial cutover and rollback MUST be documented; pre-split/post-split mixed routing is unsupported. (Story 2)
- **FR-006**: Storefront MUST have its normal route without a separate enablement flag. Until publication-backed reads and trusted target binding are available, requests MUST return HTTP 503 with a stable `STOREFRONT_UNAVAILABLE` diagnostic and no catalog data. Missing prerequisites MUST NOT disable otherwise-ready Admin operations. (Story 3)
- **FR-007**: Any Storefront catalog reads delivered or subsequently connected to this boundary MUST read only the selected target's active publication snapshot. Direct lookup, nodes, relationships, lists, counts, fragments and supported search paths MUST enforce the same boundary; there MUST be no fallback to current management data, live aggregate queries or post-pagination visibility filtering. Unsupported read operations MUST remain unavailable. (Stories 1, 3)
- **FR-008**: Global IDs MUST distinguish Admin and Storefront resource kinds. Cross-endpoint IDs MUST produce the existing `NOT_FOUND` result. Storefront IDs MUST identify a stable resource rather than a particular release, and all lookup/cache scopes MUST include trusted target context where applicable. (Story 3)
- **FR-009**: Both endpoints MUST use existing pluggable authentication, authorization and UserDir contracts. The endpoint determines management versus public access; credentials alone MUST NOT switch the data plane. Storefront/buyer credentials MUST NOT gain Admin permissions through their presence on Storefront, and an Admin credential MUST NOT grant access to drafts through Storefront. Existing policy checks remain authoritative. (Stories 1, 3)
- **FR-010**: Admin introspection MUST require authentication. Storefront introspection MAY expose only its public schema when its endpoint is available; it MUST NOT expose the Admin schema. Storefront MUST NOT accept management subscriptions; Admin durable subscriptions MUST preserve authorization and replay semantics. (Stories 1, 4)
- **FR-011**: Both endpoints MUST enforce bounded structural and operation-cost limits. The operation-cost ceiling defaults to 1,000 for each. Admin MUST implement its principal cost budget with the authenticated controller exemption; Storefront has no buyer-read principal cost bucket and no controller exemption. Per-IP limits and CORS MUST be independently configurable per endpoint. Empty origin lists MUST deny cross-origin browser access, and wildcard origins with credentials MUST fail validation. (Story 4)
- **FR-012**: Claimed buyer-IP headers MUST be ignored unless authenticated by a supported, trusted Storefront access-token contract. Until that contract exists, rate limiting MUST use the configured trusted-proxy client-IP behavior. No new token provider or anonymous target-selection mechanism is introduced here. (Story 4)
- **FR-013**: Request execution and cache reuse MUST remain endpoint-specific. A persisted document, response or identifier accepted for one endpoint MUST NOT bypass schema, authorization or target checks on the other. Future cacheable Storefront reads MUST be scoped by target, active snapshot and document; this feature does not introduce shared caching of Admin responses. (Stories 1, 3)
- **FR-014**: Operator documentation and generated consumer contracts MUST describe the final paths, matching-version cutover, per-endpoint security/limits and Storefront unavailability behavior. Planning and implementation tasks MUST include the ADR-0012 acceptance-status change and dependent architecture-document alignment in the implementing PR; specification authoring alone MUST leave the ADR Proposed. (Stories 1–4)
- **FR-015**: Playgrounds MUST offer an Admin/Storefront endpoint switcher with explicit editable browser-accessible URLs and separate schema/editor sessions. Switching endpoint or URL MUST clear credentials/custom headers, close the previous subscription connection and avoid automatic document execution. Missing peer URLs MUST require explicit input rather than guess private or proxy addresses. Switching MUST NOT bypass CORS, authentication or Storefront unavailability. (Story 2)

### Production Requirements *(mandatory for core-service or load-bearing changes)*

These are proposed acceptance thresholds, not claims of measured production capacity. The production dataset is the preserved catalog baseline with at least five million Products, 10,000 Categories, two isolated namespaces/targets and a deliberately differing draft/published fixture cohort. Real publication-dependent acceptance remains blocked until authoritative projections and target binding exist; fixtures prove only boundary behavior.

- **PR-001 Replica Safety**: Verify two concurrent API replicas and two controller replicas against the same production datastore and durable journal. After the coordinated initial cutover, rolling replacement within the new routing contract MUST preserve management writes, cursor resumption and endpoint isolation. Do not simulate compatibility by routing old-path requests to a new alias. Git remains exactly one active process with retained storage and non-overlapping replacement; no Git availability or replication claim is added.
- **PR-002 Multi-User Security**: Verify static-user, OIDC, ServiceAccount and anonymous principal cases through supported configured providers, including denied, expired and malformed credentials. Across both replicas, all cross-endpoint, cross-namespace and cross-target probes MUST return no unauthorized data or side effects. Logs and diagnostics MUST identify endpoint and failure class without secrets or draft contents.
- **PR-003 Capacity**: On the declared dataset, sustain 100 offered public requests/second from 100 concurrent clients plus 10 Admin operations/second from 20 clients and 20 active Admin watches for 30 minutes. Public requests cover bounded pages of 100 items and direct/node/relationship reads where implemented; unavailable Storefront responses are measured separately and cannot count as successful catalog reads. Public successful reads MUST have p95 ≤500 ms and p99 ≤1 s; Admin operations p95 ≤2 s; unexpected errors <0.1% on each endpoint. Add one acknowledged bounded manifest push/second to the existing repository workload, with affected management state observable within 60 s. Record achieved throughput, payload sizes, skew and per-process resources, not only offered load.
- **PR-004 Backpressure**: At twice the declared public offered rate for five minutes, structural limits, finite request deadlines and configured finite concurrency/queue budgets MUST prevent unbounded growth. Rejected, timed-out and dropped work MUST be counted separately. Admin retains its PR-003 latency objective; valid baseline traffic MUST recover within 30 s after overload ends. Cancellation MUST release request capacity. Planning MUST name concrete budgets using existing limiting mechanisms rather than add a parallel scheduler.
- **PR-005 Capacity Evidence**: Reuse `make capacity TARGET=api PROFILE=readiness MODE=production` for existing smoke gates and `make capacity TARGET=repository PROFILE=lifecycle MODE=production` for connected pushes, admission and reconciliation. Extend the existing API target family with an endpoint-split profile if needed; the endpoint-split selector is registered in the dispatcher with separate boundary, fixture and published-catalog outcomes. The domain verifier MUST prove actual dataset size/topology, expected endpoint/schema responses, zero unauthorized disclosures, acknowledged push visibility and gap-free durable watch recovery. Preserve existing runner thresholds; readiness-only or unavailable-response load cannot certify Storefront read capacity.
- **PR-006 Fault Recovery**: Reuse `make chaos CHAOS_PROFILE=api-restart` against one explicitly owned API container under the declared mixed workload. The surviving replica MUST maintain isolation; the replaced replica MUST become ready within 30 s and controller watches MUST resume within 60 s without losing acknowledged changes. Exercise publication/target dependency loss through controlled tests, requiring unavailable responses throughout the fault and restored eligible reads within 30 s of dependency readiness; unavailable prerequisites MUST never authorize Admin fallback. Faults against unowned deployments are prohibited.
- **PR-007 Data Integrity**: Endpoint cutover, cancellation, restart and rollback MUST not change authored manifests, admitted resource identity/version, publication selection or finalizers except through authorized operations. Reuse asynchronous `status.resolved` aggregates and existing datastore/journal contracts. Every acknowledged test mutation MUST remain observable after recovery; replay may repeat notifications but MUST not duplicate state changes.

### Key Entities *(include if feature involves data)*

- **Endpoint contract**: Admin management or Storefront public audience, exposed fields, allowed operations, authorization scope and traffic policy.
- **Named listener**: Admin or Storefront port with one invariant prefixed path; no shared/dedicated mode.
- **Publication target and active snapshot**: Trusted context selecting the immutable published view; never interchangeable with the current management view.
- **Endpoint-specific resource identity**: Stable resource identity whose kind distinguishes Admin from Storefront without embedding a release.
- **Availability state**: Whether Storefront publication/target prerequisites are ready; separate from Admin availability and never a visibility permission.

## Success Criteria *(mandatory)*

### Measurable Outcomes

- **SC-001**: All supported management journeys—read, author, status update, completion and durable observation—pass through Admin for authorized actors; 100% of the negative boundary matrix produces no unauthorized data or effects on either replica.
- **SC-002**: Default/customized named ports preserve the two fixed endpoint paths, with 404 on old/opposite paths and rejection of all tested collisions/legacy generic-port settings. Matching clients complete their journeys after one release cutover/rollback rehearsal, with zero routing-mode switches or aliases. Playground switch tests produce zero cross-endpoint credential transfers or automatic executions.
- **SC-003**: Every missing-prerequisite Storefront case returns the documented unavailable result while authorized management journeys continue. Every available publication fixture yields the exact expected target/version across all supported access paths, with zero drafts or other-target results. Fixture evidence is labeled separately from real published-catalog acceptance.
- **SC-004**: During the 30-minute workload on the declared dataset, successful public reads, management operations and acknowledged-change visibility meet PR-003's latency, error and 60-second convergence thresholds; achieved throughput and unavailable/rejected work are independently reported.
- **SC-005**: During the five-minute overload and single-replica replacement exercises, unauthorized disclosures remain zero, work remains within declared budgets, baseline service recovers within 30 seconds and durable observers resume within 60 seconds with all acknowledged changes present.
- **SC-006**: Endpoint paths, authentication behavior, origin rules and isolation results agree across both replicas before and after replacement for every tested provider configuration; no outcome depends on which replica receives the request.

## Scope, Dependencies & Assumptions

- Source issue: [#495](https://github.com/gitstore-dev/GitStore/issues/495). Governing decision: [ADR-0012](../../docs/ADRs/0012-admin-storefront-graphql-endpoints.md), amended with the user's 2026-11-04 single-cutover and fixed named-listener direction. Earlier staged migration, shared/dedicated routing combinations and disabled-by-default flag are superseded proposals.
- This feature delivers endpoint/schema separation and safe unavailability. It does not implement the publication lifecycle, storefront access tokens, anonymous target selection, buyer identity, cart/checkout/order flows, new search indexing or Storefront subscriptions. Real catalog reads cannot be declared ready until the relevant dependencies supply reviewed contracts and authoritative data; this feature must not create stand-in production data paths to appear complete.
- [#512](https://github.com/gitstore-dev/GitStore/issues/512) remains the independent runtime-role/deployment and advertised-endpoint follow-up. Separate API processes, `--all`, role-selection commands and public URL discovery are outside this feature. [#472](https://github.com/gitstore-dev/GitStore/issues/472) owns deployment capabilities discovery and [#307](https://github.com/gitstore-dev/GitStore/issues/307) owns viewer permissions/diagnostics.
- GitStore is pre-GA. One coordinated breaking cutover is accepted; matching API/client versions are required across it. Concurrent replicas and rolling recovery after that cutover remain required. No second release is reserved solely for compatibility cleanup.
- The publication fixture cohort includes two targets, published/unpublished resources, a newer draft of a published resource and a release change. Production verification must use real authoritative projections; fixtures do not certify missing publication behavior.
- Planning must reuse existing schema-generation, auth, admission, datastore, durable journal, transport, limiting and owned-load components. Technology choices, package layout, exact bounded-work budgets and execution tasks belong in the plan rather than this user-facing requirements contract.
