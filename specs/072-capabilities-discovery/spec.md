# Feature Specification: Deployment Discovery and Self Access

**Feature Branch**: `codex/072-capabilities-discovery`

**Created**: 2026-11-20

**Status**: Draft

**Input**: Specify #307 and #472 together in a separate worktree: discover published endpoints, including proxy URLs; discover provider-supported login flows, including device authorization for CLI clients; expose current-user access through self-subject reviews; preserve existing authorization and diagnostics contracts.

## User Scenarios & Testing *(mandatory)*

### User Story 1 - Find Published Services Before Login (Priority: P1)

An Admin, Storefront, CLI, or third-party client starts with a deployment discovery address and learns which published services it can connect to, without knowing internal service ports or reverse-proxy routing.

**Why this priority**: Every subsequent interaction depends on locating the correct externally reachable service.

**Independent Test**: Configure public addresses on different hosts and behind a path-rewriting proxy. An unauthenticated client discovers and connects to each published service using only the returned addresses.

**Acceptance Scenarios**:

1. **Given** a deployment with published Admin and Git interfaces, **When** a client reads discovery before login, **Then** it receives their public addresses, protocol identifiers, and any documented address-template variables without internal upstream addresses.
2. **Given** a proxy that changes the external scheme, host, port, or path prefix, **When** discovery is read, **Then** it returns the configured external addresses unchanged and the client needs no path-guessing rules.
3. **Given** a service is installed but not published, or only provides an unimplemented placeholder, **When** discovery is read, **Then** it is not advertised as a usable endpoint.
4. **Given** an otherwise published service is temporarily degraded, **When** discovery is read, **Then** its publication remains distinguishable from current operational health.

---

### User Story 2 - Choose a Supported Login Flow (Priority: P1)

A browser renders the available password or provider login choices. A CLI selects an enabled device authorization or authorization-code flow using published provider metadata and client registration information.

**Why this priority**: A redirect button alone does not let a non-browser client determine how to authenticate.

**Independent Test**: Exercise password-only, OIDC-only, and mixed deployments, including an OIDC provider that supports device authorization globally but disables it for the configured CLI registration.

**Acceptance Scenarios**:

1. **Given** multiple configured authentication providers, **When** discovery is read, **Then** each client-visible provider has a stable identifier, display name, and its available login flows; bearer-verification support alone does not imply interactive login support.
2. **Given** an OIDC provider and a CLI registration enabled for device authorization, **When** the CLI reads discovery, **Then** it can identify the discovery document, device authorization and token endpoints, public client identifier, requested scopes, and client authentication requirements needed to start that flow.
3. **Given** device authorization is advertised by the provider but not enabled for the registration, **When** the CLI selects a flow, **Then** device authorization is not represented as usable for that registration.
4. **Given** authorization-code login is enabled for a public CLI registration, **When** its flow is advertised, **Then** discovery identifies its proof-key requirement and supported callback constraints without publishing a client secret.
5. **Given** provider metadata is absent, invalid, or beyond its allowed freshness period, **When** clients read discovery, **Then** affected flows are explicitly unavailable or unknown; unrelated providers and endpoint discovery remain usable.
6. **Given** a deployment supports a configured client identity bootstrap method, **When** a client prepares to log in, **Then** it can distinguish a pre-registered client identifier from provider-side dynamic registration or a client-hosted metadata identity, and unsupported methods are not silently attempted.
7. **Given** the authorization server requires a resource indicator, **When** a client prepares its authorization request, **Then** it can identify the configured protected-resource target separately from its client identity and external connection URL.

---

### User Story 3 - Check My Access in a Store Context (Priority: P1)

An authenticated user selects a namespace as the store context. The Admin asks which specific operations that user may perform so it can guard routes and present appropriate controls.

**Why this priority**: Deployment support does not establish that the current user may perform an operation.

**Independent Test**: Use ordinary users with different grants in two namespaces. Compare self-review decisions with the corresponding protected operations and verify that neither user needs permission to review other subjects.

**Acceptance Scenarios**:

1. **Given** an authenticated caller, **When** the caller requests a self-subject access review through the current-user (`me`) surface, **Then** the subject and groups come exclusively from the trusted request identity and configured UserDir; caller-supplied identity or hypothetical groups cannot replace them.
2. **Given** different permissions in two namespaces, **When** the same action is reviewed for each context, **Then** decisions reflect the correct namespace and target restrictions without disclosing inaccessible resource details.
3. **Given** an ordinary user without arbitrary-subject review permission, **When** the user reviews their own access, **Then** the self-review is permitted while arbitrary-subject review retains its existing authorization requirement.
4. **Given** an unsupported review provider, unavailable policy, or indeterminate decision, **When** a self-review is requested, **Then** it returns an explicit unsupported or unavailable result and never fabricates an allow or a definitive policy denial.
5. **Given** permission changes after a self-review, **When** the user attempts the protected operation, **Then** that operation independently enforces current authorization.

---

### User Story 4 - Adapt Features and Diagnose Configuration (Priority: P2)

Clients discover supported search terms and optional extensions. Authorized operators inspect deployment health and redacted configuration to distinguish unsupported functionality, missing setup, denied access, and temporary failure.

**Why this priority**: Clients need consistent feature discovery and actionable failure information without exposing deployment secrets.

**Independent Test**: Vary search registrations, extension enablement, configuration completeness, and operator grants. Verify the advertised capabilities and diagnostic results against each configuration.

**Acceptance Scenarios**:

1. **Given** optional search functionality, **When** discovery is read, **Then** supported query terms are identified per resource kind using the owning search contract's vocabulary.
2. **Given** optional Admin, OIDC bridge, search/recommendation, or hook integrations, **When** discovery is read, **Then** it reports registered client-facing functionality without exposing private topology, provider policy, or secret values.
3. **Given** an authorized operator, **When** configuration and diagnostics are requested, **Then** the result identifies relevant health, deployed version and schema identity, capability availability, and missing configuration using redacted information.
4. **Given** a caller without diagnostic or configuration permission, **When** that information is requested, **Then** access is denied without revealing whether the protected configuration exists.
5. **Given** an existing admission error or successful admission report, **When** the response is returned, **Then** existing diagnostics extensions remain compatible, including forbidden-response redaction.

### Edge Cases

- Forged Host or forwarded headers must not alter advertised endpoints or poison another client's cached discovery document.
- Public endpoints may use different origins or proxy prefixes; discovery must not assume one shared base path.
- A provider may verify tokens without supporting interactive login, have multiple client registrations, or support grants that no configured registration can use.
- A registration endpoint may require an initial access credential unavailable to an unauthenticated client. Advertising the endpoint does not establish open registration or device-flow eligibility.
- A client-hosted metadata URL may be unsupported, unreachable, or rejected by provider policy. Client metadata declarations do not constitute provider approval or authentication of a public client.
- A published endpoint can change behind a proxy while the protected-resource identifier stays stable; the connection URL must not silently replace the token's intended resource.
- Missing metadata fields are unknown unless the applicable discovery standard defines a default; device authorization must never be inferred from OIDC support alone.
- Public OIDC issuer identity must remain canonical even when endpoint traffic passes through a proxy. Publishing a URL does not make an invalid issuer rewrite valid.
- A policy may constrain an existing resource differently from creating a resource in its namespace. Reviews must express both cases without requiring a fabricated resource ID.
- AuthN, AuthZ, and UserDir can come from independent providers. Authentication claims must not silently become authoritative groups or profile types.
- Permission, login, or configuration state may change after discovery. Shared caching must never serve one caller's access decisions to another.
- A mixed-version rollout may have replicas that disagree on supported capabilities. Cache revisions alone cannot make an unsupported operation safe.
- Oversized endpoint/provider registrations, review batches, slow metadata servers, and repeated failures must produce bounded work and explicit failure states.

## Requirements *(mandatory)*

### Functional Requirements

- **FR-001**: Provide one coherent deployment discovery model shared by Admin, Storefront, CLI, and third-party clients. Its public projection MUST be readable before login; protected identity, permissions, configuration, and operational details MUST require the appropriate access.
- **FR-002**: Public discovery MUST be bootstrappable without knowing a GraphQL endpoint. Exact transport, URL, and field names MUST be defined during planning, with equivalent meaning for any additional projection.
- **FR-003**: Advertise each deliberately published client interface with a stable identifier, protocol, and complete external URL or explicit template with defined variable encoding. Preserve configured proxy paths and support different origins. Internal gRPC, metrics, datastore, and other private endpoints MUST remain undisclosed unless separately authorized publication is in scope.
- **FR-004**: Derive publication from validated operator configuration and registered functionality, not guessed listener addresses or untrusted request headers. Reject ambiguous, malformed, duplicate-identifier, and credential-bearing publication entries. Unpublished services and unimplemented placeholders MUST NOT appear as usable endpoints.
- **FR-005**: Distinguish endpoint publication, feature support, current health, and caller permission. A discovery entry MUST NOT imply authorization or guarantee momentary availability.
- **FR-006**: Describe client-visible authentication providers independently, including stable identity, display information, and supported login mechanisms. Retain AuthN chain semantics; do not advertise an interactive mechanism merely because a provider accepts bearer credentials.
- **FR-007**: For OIDC providers, expose or reference validated public discovery metadata, including the canonical issuer and the authorization, device authorization, and token endpoints where supported. Retain standard semantics for grant types, response types, scopes, token endpoint authentication methods, and proof-key methods rather than inventing a competing provider protocol.
- **FR-008**: Distinguish provider-advertised support from flows enabled for a client. Describe how the client obtains or presents its identity rather than assuming every deployment publishes one pre-registered client identifier. Include an existing public client identifier or the supported identity bootstrap method, intended client category, required scopes, client authentication requirements, and callback constraints where applicable. An enabled flow MUST be compatible with provider support, effective client metadata, deployment configuration, and GitStore's accepted token contract, including audience; unknown support MUST NOT be advertised as enabled.
- **FR-009**: Device authorization discovery MUST identify the device authorization endpoint, token endpoint, and device-code grant support. Public CLI clients MUST NOT need a distributed secret. Authorization-code flows for public CLI clients MUST require an applicable proof-key mechanism.
- **FR-010**: Metadata fetching MUST be limited to operator-approved providers and destinations, including redirect validation, with bounded response sizes, deadlines, refresh concurrency, and retries. Report freshness/availability so clients can distinguish absent support from failed discovery. Refresh failures MUST NOT indefinitely preserve enabled flows based on expired metadata.
- **FR-011**: Expose registered optional capabilities and supported search terms per resource kind. Search and extension owners MUST register their vocabulary and availability in the shared model; do not imply implementation of the underlying optional feature.
- **FR-012**: Version the public contract and identify the exact public representation with a cache revision or validator. Define finite freshness and invalidation semantics. Identical effective public representations MUST have identical validators across replicas; protected responses MUST NOT enter shared public caches.
- **FR-013**: Provide a current-user (`me`) surface with self-subject access review semantics. Self-review MUST derive its principal from the trusted request, reject identity overrides, require no arbitrary-subject review grant, and reject unauthenticated calls to this authenticated surface.
- **FR-014**: Reuse the existing `accessReview` / `SubjectAccessReview` action vocabulary, decision meaning, policy evidence, and authoritative evaluation behavior where supported. Preserve existing arbitrary-subject review authorization and hypothetical-review behavior. Self-review MUST NOT introduce a second permission engine or weaken the existing contract.
- **FR-015**: Support bounded selector-only namespace-context and resource checks, including creation permission where determinable without a proposed mutation body. Keep tier, visibility and other operation-specific proposal fields out of generic selectors. When missing facts could affect the decision, report explicit insufficient context rather than fabricate defaults or a definitive allow/deny. Cover only implemented owning workflows; reviews do not promise authorization for an unspecified future payload.
- **FR-016**: Retain the independence of AuthN, AuthZ, and UserDir. Groups and profile type MUST follow the UserDir contract. Providers lacking self-review or diagnostic capabilities MUST return explicit unsupported states without silently falling back to another authorization provider.
- **FR-017**: Self-review decisions MUST identify the evaluated scope and freshness/policy evidence when available, reveal no unauthorized resource details, and remain advisory to client presentation. Each protected operation MUST independently enforce authorization.
- **FR-018**: Authorized diagnostics MUST cover relevant API health, deployment version, schema identity needed for client compatibility/code generation, enabled functionality, and degraded or unavailable states. Configuration inspection MUST distinguish missing configuration from forbidden access and reveal only safe values or presence indicators for secrets.
- **FR-019**: Preserve existing `errors[].extensions` diagnostics and response-level `extensions.admission` contracts. Retain diagnostic reason, message, severity, optional file/field, and admission phase/commit semantics where applicable, including forbidden-error redaction. New authentication, unsupported, or unavailable conditions MUST fit the established error model; do not replace it with an unrelated error envelope.
- **FR-020**: Public discovery MUST contain no credentials, secret material, private issuer administration details, authorization policies, user/group membership, or private upstream addresses. Public issuer and login protocol metadata needed by clients are intentional disclosures.
- **FR-021**: Mixed-version deployments MUST advertise only capabilities safe across all replicas eligible to receive the advertised operation, or return an explicit incompatibility/unavailable state. Planning MUST define the rollout contract and its source of truth; process-local optimistic advertisements or validators alone are insufficient. Older clients MUST tolerate additive fields and unknown optional capability identifiers.
- **FR-022**: Document discovery bootstrap, published-address configuration, provider/client flow prerequisites, self-review semantics, caching, redaction, and rollout/rollback behavior in the relevant operator and client documentation when implementing the feature. Admin remains an optional API consumer and MUST NOT connect directly to the native Git service.
- **FR-023**: Client identity bootstrap MUST distinguish pre-registration, provider-side dynamic registration, and client-hosted metadata identities when supported. Publish only verified support and non-secret prerequisites; the presence of a registration endpoint or metadata document MUST NOT imply registration permission, accepted grants, or a working device flow. Planning MUST compare these alternatives and define explicit selection/fallback behavior before committing to response fields. This requirement does not mandate implementing every alternative or performing registrations inside GitStore.
- **FR-024**: Where supported and required by the provider, identify the protected-resource target needed for token acquisition independently of client identity and published connection addresses. Resource selection MUST preserve the API's configured token audience validation; a resource indicator is not a mechanism for obtaining a client identifier.
- **FR-025**: New top-level GraphQL objects MUST have the standard apiVersion/kind/metadata envelope. Their body MUST use spec/status when defined, with no parallel domain fields; otherwise direct body fields are permitted. New status contracts MUST follow #40 conditions/observedGeneration/lastAppliedRevision/typed resolved conventions where applicable; never invent reconciliation generations, stored metadata or applied commits for computed observations. Transient reviews MUST validate versioned request envelopes before effects, reuse existing decision evidence within resolved, and preserve the existing accessReview contract. Nested helpers and OAuth protocol metadata retain their appropriate shapes. Reuse owning types, context builders and capability definitions. The same feature change MUST include envelope/placement/status, no-effect invalid-input, generated-source and operation-parity checks in the normal contributor/CI path.

- **FR-026**: Derive scope from canonical action and id/namespacePath/inNamespace selectors. A batch MUST contain 1–32 checks resolving to one cluster scope or one authoritative Namespace incarnation; reject mixed/indeterminate scope wholesale. No batch scope/cluster Boolean, resource name shortcut, create branch or per-kind selector union. Permit omitted target only for supported cluster operations needing no individual resource. Reuse owning lookup/context semantics; insufficient proposed-operation context remains explicit.
- **FR-027**: Each published endpoint MUST describe accepted credential schemes and protocol-specific delivery mechanisms, linked to shared provider/login profiles and compatible token requirements. Credential acquisition flows and endpoint credential usage MUST remain distinct and MUST NOT be duplicated per endpoint. Publish only transport/provider combinations validated against actual handlers, including negative tests for unsupported Basic-token conversions and incompatible audiences.

### Production Requirements *(mandatory for core-service or load-bearing changes)*

- **PR-001 Replica Safety**: Validate public discovery, metadata refresh, and self-review with at least two API replicas and a mixed-version/configuration rolling replacement. Equal public representations produce equal validators; a route must never advertise unsupported cross-replica behavior. Maintain two controller replicas for connected reconciliation evidence. Git remains exactly one active process with retained storage and non-overlapping replacement.
- **PR-002 Multi-User Security**: Validate unauthenticated discovery, authenticated self-review, arbitrary-subject review authorization, UserDir-derived identity, two-namespace isolation, protected configuration, and redaction across pluggable provider combinations. Audit or operational records MUST follow existing conventions and contain no credentials or secret payloads.
- **PR-003 Capacity**: Proposed production acceptance envelope: a verified retained baseline of at least 5,000,000 products and 10,000 categories; two APIs/two controllers/one Git process; up to 16 visible providers, 16 published endpoints, 32 client registrations, and 32 checks per review request; public documents capped at 256 KiB. Sustain 100 discovery reads/s and 20 authenticated review requests/s for 30 minutes alongside the existing connected authoring/watch/push workload. Require discovery p95 <=250 ms/p99 <=1 s, review p95 <=1 s/p99 <=2 s, and unexpected errors <1% outside injected faults. Planning MUST confirm these proposed budgets against the reusable harness before implementation and preserve existing stricter gates.
- **PR-004 Backpressure**: Reject over-limit registrations and requests explicitly. Bound metadata caches by configured providers and registrations, not arbitrary client input. Bound network workers, request concurrency, review work, retries, and queue depth; use finite deadlines. During five minutes at twice the stated discovery/review rates, excess work MUST receive bounded throttled/unavailable responses, with no cross-user leakage, false allows, or unbounded resource growth.
- **PR-005 Capacity Evidence**: Extend the existing `make capacity TARGET=api PROFILE=endpoint-split MODE=<diagnostic|alpha|production>` family for discovery/self-review coverage, preserving its connected oracle; these new probes are proposed work, not current harness functionality. Evidence MUST identify tested source, configuration, actual process topology, retained dataset verification, full load window, latency/error/resource ceilings, and a verifier for publication accuracy, grant selection, scope isolation, policy-decision parity, redaction, and rolling compatibility. Diagnostics do not certify production readiness.
- **PR-006 Fault Recovery**: Reuse `make chaos CHAOS_PROFILE=api-restart` and `make chaos CHAOS_PROFILE=identity-restart` against explicitly owned targets during the connected load window. Unrelated discovery must remain available during provider failure; affected flows/reviews must report unavailable without false support or grants. Restore steady-state success/latency within 60 seconds of dependency readiness or replacement readiness. Apply the existing endpoint-split 30-second overload recovery allowance, including its final ten-second stable window. Planning MUST specify a bounded metadata-timeout scenario using the same owned-test conventions.
- **PR-007 Recovery**: Failed metadata refresh or partial configuration reload MUST NOT publish mixed or secret-bearing documents, alter authorization policy, or corrupt caller isolation. After recovery, publish a coherent validated representation, invalidate obsolete cache validators, and re-evaluate access using current authority. Preserve all retained datasets and unrelated deployments during evidence gathering.

### Key Entities *(include if feature involves data)*

- **Deployment Discovery**: Versioned public description of published services, login options, optional capabilities, cache identity, and metadata freshness.
- **Published Endpoint**: Stable service/protocol identity with an externally reachable address or an explicit address template; independent of internal listener placement and live health.
- **Authentication Provider Description**: Public provider identity, display information, standard discovery metadata, and availability/freshness information.
- **Client Login Registration**: Public client identity or supported identity bootstrap method and its deployment-enabled flows, scopes, authentication requirements, callback constraints, and token compatibility prerequisites.
- **Protected Resource Target**: Configured identity of the service for which a token is requested, distinct from the OAuth client's identity and potentially distinct from the external connection URL.
- **Self-Subject Access Review**: Read-only evaluation of the trusted caller's requested operation in scope derived from its action/target, reusing existing decision evidence; all checks in a batch share one resolved authorization scope.
- **Registered Capability**: Client-facing optional feature or kind-specific search vocabulary with explicit support and availability semantics.
- **Protected Diagnostic View**: Authorized, redacted operational/configuration information using the established diagnostics model.

## Success Criteria *(mandatory)*

### Measurable Outcomes

- **SC-001**: In every direct-host, separate-origin, and proxy-prefix acceptance case, an unauthenticated client locates all deliberately published supported services without guessed paths; zero private or placeholder endpoints are advertised as usable.
- **SC-002**: Browser and CLI clients select a valid enabled login flow in every password-only, OIDC-only, mixed-provider, device-enabled, and device-disabled case for each selected identity bootstrap method; no case mistakes provider-wide grant support or a client declaration for client eligibility. Cases requiring resource selection use the intended protected-resource target without confusing it with a client identifier.
- **SC-003**: Every definitive self-review matches the corresponding operation at the same complete policy/identity/context state; proposal-dependent selector-only cases return explicit context-required instead of an invented decision. Cross-namespace access and identity overrides succeed zero times.
- **SC-004**: Every unauthorized diagnostic/configuration case discloses zero protected configuration facts or secret values; existing diagnostic/admission contract fixtures remain compatible.
- **SC-005**: Across both API replicas and rolling replacement cases, clients receive coherent discovery revisions and zero advertised operations unsupported by an eligible serving replica; protected cache entries never cross caller boundaries.
- **SC-006**: During the declared 30-minute production workload, at least 95% of discovery reads complete within 250 ms and 95% of self-review requests within one second; the p99/error/resource and connected-workload gates in PR-003 through PR-005 also pass.
- **SC-007**: Overload and fault cases produce zero false grants, leaks, or unbounded work; steady-state behavior resumes within the declared 30-second overload and 60-second dependency-recovery limits.
- **SC-008**: Every new operation/input family is covered by contract-convention tests. Deliberate mutations removing a top-level envelope field or required review default, mixing direct domain fields with spec/status, departing from the applicable #40 status shape, accepting invalid discriminators, or changing shared authorization-context mappings cause the relevant normal checks to fail before merge. Invalid-input fixtures produce zero review-provider/store effects. No separate cleanup PR is required to normalize this feature's initial contracts.
- **SC-009**: Every advertised endpoint credential binding passes its declared transport/provider acceptance fixture and incompatible scheme/audience fixtures are rejected. Clients can distinguish credential acquisition from use on Git HTTP, GraphQL HTTP and GraphQL WebSocket without assuming a token works on every endpoint.

Shared metadata and query-return rules are part of FR-025/SC-008: reuse ObjectMeta/ObjectMetaInput without parallel variants; new query fields return objects, lists or established connections rather than Payload types. Computed metadata may be absent when no authoritative source exists; do not fabricate persisted metadata or change shared field semantics. Preserve existing legacy query contracts for compatibility.

## Assumptions and Scope

- This is one feature spanning [#307](https://github.com/gitstore-dev/GitStore/issues/307) and [#472](https://github.com/gitstore-dev/GitStore/issues/472). It defines backend discovery and self-access behavior; adapting Admin, Storefront, or CLI screens/commands can ship independently using the contract.
- `me` and self-subject review are the intended user-facing semantics. Exact schema, transport, batched-check representation, publication config keys, cache TTLs, and mixed-version coordination are planning decisions constrained by the requirements above. Earlier conversation JSON examples are illustrative, not approved wire contracts.
- The public issuer and selected discovery fields are necessary protocol information. This refines #472's restriction on issuer internals while retaining its prohibition on secrets, administrative internals, and policy disclosure.
- Device authorization execution and any selected registration exchange belong to the provider and client. This feature discovers usable flows and identity bootstrap options; GitStore does not implement an authorization server, automatically register clients, distribute confidential credentials, or become an authentication proxy. Public endpoint discovery can describe an existing proxy.
- Optional feature registration does not implement Storefront publication, search syntax, recommendations, hooks, or new commerce operations. Capability vocabulary follows the owning contracts, including #471 for search.
- Permission results are scoped checks, not a complete permission enumeration or grants that reserve future authority. Existing arbitrary-subject and hypothetical reviews retain their behavior.
- The numeric capacity envelope is a proposed acceptance baseline for planning, not a measured capability claim. No deployed load or chaos execution is part of this specification-only change.

### Client Identity Alternatives for Planning

The response shape must leave room to compare these methods; none is selected merely by listing it here.

| Mechanism | Role in this feature | Evaluation required during planning |
| --- | --- | --- |
| Pre-registered client identifier | Baseline for providers requiring advance registration. | Distribution, callbacks, public-client requirements, and enabled grants. |
| [RFC 7591](https://www.rfc-editor.org/rfc/rfc7591.html) | A registration exchange obtains a provider-issued client identifier and accepted metadata. This is registration rather than lookup of an existing identifier. | Provider support, open versus protected registration, prerequisite credentials, persistence/reuse, abuse bounds, and grant approval. GitStore discovery must not expose registration credentials. |
| [Client ID Metadata Document, requested revision -00](https://datatracker.ietf.org/doc/html/draft-ietf-oauth-client-id-metadata-document-00) | The client supplies an HTTPS URL as its identifier; the authorization server retrieves the client's metadata. This reverses who discovers whose metadata. | Provider support, hosting/trust, bounded retrieval and caching, callback validation, device-flow compatibility, and fallback. A client description alone does not prove provider acceptance. |
| [RFC 8707](https://www.rfc-editor.org/rfc/rfc8707.html) | A complementary resource indicator identifies the protected service for token acquisition; it does not assign or discover client identifiers. | Provider support and mapping between the configured resource identifier and accepted token audience, including deployments with rewritten public URLs. |

As checked on 2026-11-20, the client metadata document is an active Internet-Draft with [revision -03](https://datatracker.ietf.org/doc/html/draft-ietf-oauth-client-id-metadata-document-03) dated 2026-11-02. Evaluate the requested -00 and the current revision, then pin any adopted revision and record compatibility differences. Treat support advertisement such as `client_id_metadata_document_supported` as provider capability information, not approval of a particular client or grant.

## Existing Contract Evidence

Inspected on the worktree's `origin/main` baseline when drafting; these references describe existing building blocks rather than new endpoint claims:

- [Access review resolver](../../gitstore-api/internal/admin/graph/resolver/access_review.go) and [contract tests](../../gitstore-api/internal/admin/graph/resolver/access_review_test.go): `SubjectAccessReview`, canonical actions, resource IDs, review authorization, hypothetical inputs, and revision/digest evidence. The present resource-ID input does not by itself settle namespace-only creation checks or self-review permissions.
- [Authentication provider types](../../gitstore-api/internal/auth/types.go), [registry diagnostics](../../gitstore-api/internal/auth/registry.go), and [OIDC verifier](../../gitstore-api/internal/auth/provider/oidcjwt/provider.go): independent provider planes, internal capability flags, and existing OIDC discovery/token verification. Interactive login publication and client registration metadata require specification and implementation work.
- [Error diagnostics](../../gitstore-api/internal/admission/error.go) and [admission response extension](../../gitstore-api/internal/admissionreport/extension.go): established extension shapes and forbidden-error redaction.
- [Endpoint configuration](../../gitstore-api/internal/config/endpoints.go), [GraphQL routing](../../gitstore-api/internal/app/graphql.go), and [Storefront placeholder](../../gitstore-api/internal/storefront/handler.go): internal endpoint policies/routes and unavailable Storefront behavior are not a public deployment address registry.
- [Capacity interface and evidence requirements](../../tests/capacity/README.md): existing endpoint-split family and connected workload to extend during implementation.
- [OIDC Discovery](https://openid.net/specs/openid-connect-discovery-1_0.html), [OAuth authorization server metadata](https://www.rfc-editor.org/rfc/rfc8414.html), and [device authorization discovery](https://www.rfc-editor.org/rfc/rfc8628.html#section-4): normative metadata semantics, including device endpoint and grant advertisement.
