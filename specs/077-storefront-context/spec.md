# Feature Specification: Storefront Context and Contextual Pricing

**Feature Branch**: `codex/077-storefront-context`

**Created**: 2026-11-23

**Updated**: 2026-11-24 — cookie preference handling, provider-owned sessions and context isolation.

**Status**: Draft

**Input**: Priority2, #491: define the typed Storefront operation context for channel, market, locale and currencyCode. Consume spec076's trusted publication authority, pin context/snapshot/pricing time once per operation and provide deterministic eligibility, pricing and cache behavior for spec078 and query spec073.

## User Scenarios & Testing *(mandatory)*

### User Story 1 - Resolve One Authorized Context for the Whole Query (Priority: P1)

A Storefront client selects a permitted market, language and currency and receives one consistent public view throughout the selected query, including fragments, aliases and nested fields.

**Why this priority**: A caller must not receive different publications, currencies or language defaults inside one response.

**Independent Test**: Use trusted target/publication fixtures and concurrent operations with different contexts. Resolve explicit selections, remembered cookie preferences and defaults, with and without a provider-authenticated session, without requiring a full product schema or search provider.

**Acceptance Scenarios**:

1. **Given** an authorized Storefront binding and valid context, **When** a query executes, **Then** all fields use the same resolved target, publication/snapshot, locale, currencyCode and evaluation time.
2. **Given** omitted/null selections and configured defaults, **When** context resolves, **Then** deterministic effective values are returned; blank/unsupported explicit selections never silently fall back.
3. **Given** forged namespace/market/identity information or a context outside the trusted binding, **When** resolution runs, **Then** it fails before protected field execution and discloses no unauthorized target.
4. **Given** explicit selections, valid remembered preferences and configured defaults, **When** an operation is assembled/resolved, **Then** explicit non-null selections take precedence, usable preferences fill only omitted/null selections, and configured defaults fill the remainder; trusted authority constrains every layer.
5. **Given** no cookies or cookie support, **When** an anonymous, mobile or server client supplies the same permitted requested context, **Then** it receives the same effective context without needing browser state.

### User Story 2 - Evaluate Public Prices Without Changing Authored Templates (Priority: P1)

A buyer sees deterministic prices applicable to the selected market/currency and trusted eligibility context. Time-limited offers and missing prices have explicit outcomes.

**Why this priority**: Storefront prices must be commercially meaningful and reproducible rather than raw Admin templates or accidental currency conversions.

**Independent Test**: Evaluate pinned price-template fixtures at controlled times with overlapping priorities, missing trusted bindings and no-applicable-price cases. No checkout implementation is required.

**Acceptance Scenarios**:

1. **Given** several active eligible templates, **When** prices resolve, **Then** the owning deterministic selection/tie policy chooses the same result on both API replicas for the same effective inputs.
2. **Given** no price for the requested currency or unavailable eligibility facts, **When** a price is requested, **Then** the defined no-price/unavailable outcome is returned without implicit FX conversion or invented buyer eligibility.
3. **Given** a price window boundary crossed during one request, **When** nested prices resolve, **Then** the pinned evaluation time prevents inconsistent selections and response validity does not outlive its next relevant boundary.

### User Story 3 - Keep Caches, Cursors and Requests Isolated (Priority: P2)

Buyers in different contexts receive correctly scoped results even when the server batches reads, reuses caches or continues pagination across replicas.

**Why this priority**: Context-aware execution is unsafe if shared caches or cursors can return another buyer's prices or publication.

**Independent Test**: Alternate two namespaces, targets, currencies, locales and entitlement classes through shared cache/batching fixtures; continue pages on another API replica and mutate publication/eligibility state.

**Acceptance Scenarios**:

1. **Given** cached or batched data, **When** a different effective context requests it, **Then** no incompatible result or private entitlement crosses the boundary.
2. **Given** a continuation from publication A, **When** target selection, relevant context, validity or authority changes, **Then** it is rejected explicitly instead of mixing A and B or silently recomputing context.
3. **Given** personalized eligibility or a near-expiry offer, **When** response caching is considered, **Then** cache scope/expiry respects trusted entitlement and earliest relevant time limit; personalized output is not shared publicly.
4. **Given** a preference/session cookie changes between pages, **When** continuation is requested, **Then** changed effective context or current authority invalidates incompatible continuation; cookie rotation alone does not invalidate a cursor if all effective context, eligibility and validity bindings remain compatible.

### User Story 4 - Understand Effective Context and Invalid Requests (Priority: P2)

A client can discover which effective defaults were applied and understand a rejected selection without learning private configuration or policy.

**Why this priority**: Explicit context is useful only if clients can inspect its safe result and recover from unsupported choices predictably.

**Independent Test**: Exercise literal/variable context arguments, missing defaults, invalid combinations and unsupported operation locations; inspect response extensions/errors through existing transport conventions.

**Acceptance Scenarios**:

1. **Given** valid literal or variable arguments, **When** context resolves, **Then** a documented safe extension reports effective selections and relevant public context/publication identity.
2. **Given** context on a mutation, subscription, field or nested override, **When** validated, **Then** unsupported usage fails rather than partially applying another context.
3. **Given** an invalid/forbidden selection, **When** diagnostics are rendered, **Then** existing structured conventions identify safe offending fields without issuer internals, entitlements or raw policy expressions.
4. **Given** a malformed, expired, obsolete, wrong-store or incompatible preference cookie, **When** preferences are resolved, **Then** the unusable preference set is discarded, its owning adapter clears/replaces it where supported and valid defaults are used without overriding explicit selections. An invalid explicit directive selection still fails, even if a cookie contains a valid alternative.
5. **Given** a missing, expired, revoked or tampered authentication cookie, **When** session identity is required, **Then** the configured authentication provider applies its own validation/error/anonymous-access policy; preference fallback does not authenticate the caller or conceal an authentication failure.

### Edge Cases

- Channel/market is public in one store but not another; target names and namespace identities are recreated.
- Missing defaults, null versus blank arguments, unsupported language tags/currencies and conflicting locale fallback are supplied through variables and fragments.
- Market is confused with geographic region; EU membership is treated as currency or residency evidence.
- Anonymous browse lacks destination/customer data required by an eligibility expression; a client spoofs groups or geographic data.
- Equal-priority templates, price-window boundaries, missing prices and mixed currencies would make a product range misleading.
- Publication withdrawal or current suppression occurs during cache/continuation reuse; pinned content is not continuing authority.
- A cookie has valid syntax but refers to a retired market, removed currency combination, another store or obsolete preference format; signed preferences remain preferences, not authorization proof.
- An explicit market conflicts with remembered currency/locale choices; rejecting the lower-precedence preference set cannot replace the explicit market or invent a compatible configured default.
- Invalid preferences and an invalid session arrive together; clearing a preference cannot clear, validate or silently downgrade the session.
- Two concurrent requests/tabs use different cookies, or a session identifier rotates without changing trusted identity; effective state remains immutable within each operation.
- A changed cookie would silently switch a ranked continuation's context; the request must reject incompatibility rather than rebind the cursor to a new view.

## Requirements *(mandatory)*

### Functional Requirements

- **FR-001**: Define one non-repeatable executable Storefront @context directive on QUERY with channel, market, currencyCode and locale selections, following issue491. Support literals/variables, fragments and aliases through one operation context. Mutation/subscription/field/subtree overrides are excluded initially.
- **FR-002**: Resolve context exactly once before protected field execution using spec076's trusted binding and publication-read authority. A client selection can only narrow/select within permitted targets and cannot establish namespace/store/customer authority.
- **FR-003**: Resolve each selection by precedence: explicit non-null operation selection, then usable remembered preference, then configured Storefront defaults for the permitted target/context. Null/omitted selections leave room for the lower layers; explicit blank, unsupported or forbidden selections and incompatible explicit combinations fail rather than fall back. Validate/normalize identifiers, locales, currencies and the completed combination through one owning context policy; absent/incompatible required defaults fail explicitly. Authority is always a constraint, never another preference layer.
- **FR-004**: Keep commercial Market identity distinct from geographic region, served-country rules, destination evidence and customer identity. Context selection is not proof of location, tax entitlement, group membership or negotiated-price eligibility.
- **FR-005**: Pin effective namespace/target, active publication/snapshot/public representation revision, locale/fallback policy, currencyCode and pricing evaluation time for the operation. Parallel fields cannot mutate or independently replace that state.
- **FR-006**: Enforce current publication activation/expiry, suppression and caller eligibility on every public access path and continuation. Never hydrate unavailable published content from private Admin rows or retain authorization solely because a snapshot is immutable.
- **FR-007**: Publish a safe effective-context response extension through existing transport conventions, with resolved defaults and permitted publication/context identity. The extension cannot expose secret configuration, raw tokens, issuer internals, customer groups or private eligibility evidence.
- **FR-008**: Define typed eligibility bindings, trusted input sources, supported expressions, missing-value semantics and finite evaluation limits before enabling them. Unknown facts cannot authorize a price; expressions requiring unavailable facts yield the owning explicit non-applicable/unavailable outcome. Reject unsupported bindings rather than interpret them loosely.
- **FR-009**: Preserve authored price templates in publication snapshots; expose evaluated prices through explicit Storefront types owned by078. Define active windows, priority, deterministic tie handling, eligibility and no-applicable-price behavior consistently with the owning ProductVariant pricing contract.
- **FR-010**: Currency selection does not perform FX conversion. A contextual result identifies its currencyCode and valid observation window; mixed currencies cannot be summed/compared into one advertised range without a separately approved domain contract.
- **FR-011**: Keep quantity, cart contents and transaction/destination inputs in their owning domain operations. Do not add unrestricted priceSet/PricingTable selectors, region overrides, customer-group or identity overrides to the operation directive.
- **FR-012**: Define response/cache validity against the earliest relevant publication, price-window, authorization/entitlement and suppression boundary. Pinning request time prevents intra-operation drift but cannot justify serving expired cached results indefinitely.
- **FR-013**: Include effective target/publication/representation, locale, currencyCode, trusted identity/entitlement dimensions that affect the result and relevant policy/time validity in cache/batching/result-window isolation. Raw Cookie headers, preference serialization and session tokens are not semantic cache keys and never appear in cache/debug telemetry. Personalized results must not enter a public shared cache; disable unsafe reuse when all relevant dimensions cannot be represented. Request-local batching cannot merge incompatible contexts.
- **FR-014**: Supply073/078 canonical context identity and compatibility checks for cursors/ranked windows. Publication/context/authority changes invalidate incompatible continuation explicitly; no silent context substitution or cross-endpoint cursor reuse. Cookie changes/fallback are resolved for a later operation before continuation validation, never used to reinterpret an existing cursor. Transport-only cookie changes may retain compatibility when the effective context, current authority and validity bindings are unchanged.
- **FR-015**: Reuse pluggable AuthN/AuthZ/UserDir and current core read/list permissions. Public context arguments do not weaken access policy. Reuse existing GraphQL code/diagnostics/error shapes; avoid a competing error envelope or authorization verb family.
- **FR-016**: Reuse spec072's endpoint-qualified discovery for implemented context/price capabilities when available; no parallel capability API. Unknown context or unsupported combinations remain explicit even before discovery implementation ships.
- **FR-017**: Keep trusted binding/activation authority in076 and public product/price/media representations in078. This spec owns resolution/evaluation/isolation semantics, not a second publication system, locale source of truth or checkout engine.
- **FR-018**: Integrate ADR0012/0014, issue491 and existing implemented Translation fallback/snapshot ownership consistently. Planning must identify required amendments and stale issue/document links without implying Proposed ADRs or optional providers are implemented.
- **FR-019**: Cookies are optional transport inputs, not a required Storefront protocol or independent context authority. A Storefront application/server adapter may read remembered locale, currencyCode and preferred market/channel choices, validate them against the owning context policy and translate the resolved selections into ordinary requested operation context. Preserve explicit-versus-preference provenance through precedence/validation; blindly promoting unvalidated cookies into explicit directive arguments is not compliant. Do not require ambient cookie parsing in GraphQL resolvers or create a second cookie-specific context dialect. Cookie-free clients remain supported.
- **FR-020**: Treat preference cookies as untrusted hints even when signed. Missing/deleted cookies mean no remembered preference. Malformed/oversized, expired, obsolete-format, wrong-store, unsupported, unauthorized or incompatible preference sets are unusable: discard the lower-precedence preference set as a unit, resolve the remainder from explicit selections plus valid configured defaults, and let the owning adapter clear/replace only its own preference cookie where supported. Never select an arbitrary replacement market/currency, change an explicit selection or turn missing/invalid required defaults into success. Explicitly invalid operation input remains an error. Preference evaluation must remain bounded and must not expose raw cookie values in diagnostics/extensions/logs.
- **FR-021**: Session authentication remains owned by the configured AuthN/session integration and existing AuthZ/UserDir contracts. Validate session credentials before using trusted buyer facts; a cookie field, signature or preferred market is not proof of customer identity, group, geographic location, entitlement or publication access. Invalid/expired/revoked/tampered credentials follow the provider's configured authentication/error/anonymous-access policy and are never handled as invalid preferences. Spec077 neither issues sessions nor introduces a separate session store, cookie authentication adapter or implicit anonymous downgrade. Direct cookie authentication exists only when the configured provider/integration supports it.
- **FR-022**: Keep cart identifiers/contents and consent/behavior identifiers in their owning domain contracts; they are not operation-wide pricing inputs or eligibility grants merely because they arrive in cookies. Resolve and pin validated preference and authenticated-session contributions once per operation. Later preference/session changes apply only to a new operation under current authority and existing cache/cursor checks; effective-context extensions disclose resolved selections, not raw cookies, session secrets or private entitlement evidence.

### Production Requirements *(mandatory for core-service or load-bearing changes)*

- **PR-001 Replica Safety**: Focused tests cover concurrent operations and two API actors with differing defaults/policy versions, cursor reuse and publication replacement. Production qualification adds actual mixed-version replicas/rolling behavior; affected controllers preserve their source authority and Git remains singleton/non-overlapping.
- **PR-002 Multi-User Security**: Use two namespaces/targets and anonymous, ordinary and service identities across pluggable providers. Test forged context/destination/groups, wrong-store/tampered/stale preferences, provider-rejected sessions, current revocation, cookie rotation, personalized caching and safe diagnostics with zero cross-context/private disclosure. Preference fallback cannot bypass provider authentication or current target authority.
- **PR-003 Capacity**: Proposed production qualification: >=5M products/10K categories, 30 minutes at100 contextual reads/s with at least four locale/currency/market combinations and concurrent private authoring. Contextual read p95/p99 <=250 ms/1 s and unexpected errors <1%; record template/expression counts and actual hardware. These are unmeasured goals, not claims of implementation capacity.
- **PR-004 Backpressure**: Planning assigns finite context/preference-cookie bytes/count/parse cost, template/expression count/cost, reference resolution, cache cardinality, batching, retries and deadlines. Invalid or excessive preference hints follow bounded discard/default behavior; transport-level header limits and provider credential limits retain their owning error semantics. Reject unsupported/over-budget explicit contexts before expensive work; one expensive eligibility expression cannot monopolize public reads. No per-field unbounded price scans.
- **PR-005 Capacity Evidence**: Reuse make capacity TARGET=api PROFILE=endpoint-split and repository/lifecycle coexistence through existing modes. Add proposed context/pricing probes to existing families. Focused correctness plus full make pr-ready establishes implementation/PR readiness; production capacity/soak remains separately labeled and required before production claims.
- **PR-006 Fault Recovery**: Focused fixtures interrupt context/default/publication changes, cache fill and price evaluation. Production qualification uses owned API/controller restart profiles where applicable; restore healthy reads <=60 s after dependency readiness, with no stale cross-context output or data reseeding.
- **PR-007 Recovery and Integrity**: Verify controlled time boundaries, missing trusted evidence and concurrency produce the same declared results across replicas, with zero invented eligibility or currency conversion. Record deferred deployed upgrade/fault/load evidence honestly; do not require a full production rehearsal for every alpha slice.

### Key Entities *(include if feature involves data)*

- **Requested Context**: Client channel/market/locale/currencyCode selections, distinct from authority.
- **Remembered Preference Set**: Optional store-scoped locale/currency/market/channel hints supplied by a transport/application adapter, retaining lower-precedence provenance and a version/expiry contract without becoming a new stored resource.
- **Authenticated Session Contribution**: Buyer identity/entitlement facts accepted by the configured provider/integration; raw credentials are not effective-context fields.
- **Effective Context**: Validated/defaulted operation state, pinned publication and pricing time plus safe response identity.
- **Eligibility Environment**: Typed trusted facts and missing-value semantics used by bounded evaluation.
- **Contextual Price / Validity**: Evaluated public price and applicable currency/time limits, separate from authored templates.
- **Context Identity**: Canonical compatibility dimensions for caches, batching and continuation.

## Success Criteria *(mandatory)*

### Measurable Outcomes

- **SC-001**: Every multi-field/fragment/concurrency fixture resolves exactly one coherent effective context per operation.
- **SC-002**: All invalid/default/missing-value cases either resolve their documented values or fail explicitly; unsupported explicit selections never silently fall back.
- **SC-003**: Controlled price/eligibility/window fixtures return deterministic results across two API actors with zero invented eligibility or implicit currency conversion.
- **SC-004**: Two-namespace/market/currency/locale/entitlement cache and cursor fixtures disclose zero incompatible or private results.
- **SC-005**: Expiry/revocation/publication-switch tests reject stale continuation or caching in every affected case while preserving coherent request-time evaluation.
- **SC-006**: Effective-context extensions and structured errors expose required client facts without secrets or private eligibility evidence in all disclosure fixtures.
- **SC-007**: Production claims require the complete declared latency/error/resource/recovery envelope; implementation reports distinguish focused passing checks from deferred qualification.
- **SC-008**:078/073 integration fixtures consume the same canonical context identity rather than invent parallel market/currency/snapshot rules.
- **SC-009**: All explicit/preference/default fixtures obey documented precedence, discard invalid/stale preference sets without replacing explicit selections, and produce explicit errors when no valid required defaults exist. Cookie-free clients resolve the same permitted context.
- **SC-010**: Provider-rejected session and two-store cookie fixtures yield zero forged identity/entitlement or cross-store disclosure; preference clearing never masks provider policy. Cursor/cache tests distinguish changed effective context from transport-only cookie rotation and never silently rebind an existing result window.

## Assumptions, Dependencies and Scope

- Baseline origin/main86b1c15e includes Translation071, but no working publication-backed Storefront reader. [#491](https://github.com/gitstore-dev/GitStore/issues/491) describes future behavior; stale references to a merely proposed endpoint or moved docs are not current implementation evidence.
-076 owns trusted binding/publication authority;078 owns the initial public catalog schema;073 owns shared query planning and result-window/cursor mechanics. Use feature IDs across separate branches, not broken relative links to unavailable sibling directories.
- Initial configuration may bind one target/default locale/currency; supporting several requires explicit trusted authority and valid combinations. No Market resource is assumed implemented merely because market is an argument.
- Cookie integration normally belongs to the Storefront application/server adapter: browser preferences become validated requested context, while authentication supplies provider-validated principal facts. Adapter and API validation must share the owning policy and preserve preference provenance; no duplicated market/eligibility rules or implicit resolver-wide cookie state. Detailed cookie names, storage format/version, scoped clearing and provider transport protections are planning/integration work, not a new session subsystem.
- Preference cookies are optional and never required for anonymous browsing or non-browser clients. Cart/consent/session cookies cannot be cleared as a side effect of preference fallback. Invalid explicit selections fail even when cookies/defaults offer valid alternatives; invalid preferences may use defaults only within the trusted binding.
- Quantity/cart/checkout/tax/shipping/FX implementation and behavioral recommendation ingestion are out of scope. Optional inventory is not implied by ProductVariant. Later recommendation serving may consume approved context under its own privacy/consent contract; it cannot add arbitrary trusted facts here.
- Concrete directive/extension SDL, typed binding names, canonical fingerprints, pricing tie policy details and budgets are planning deliverables constrained by these requirements. Use supported subsets with explicit rejection instead of claiming full runtime CEL/environment support from expression syntax alone.
- This workflow writes one requirements spec/checklist only; no runtime/schema modifications, issue edits, ADR status changes, provider deployment or production qualification. Planned implementing changes must assess applicable ADR core acceptance under repository lifecycle rules.
