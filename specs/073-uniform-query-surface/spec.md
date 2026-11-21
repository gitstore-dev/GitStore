# Feature Specification: Uniform Query Surface for Connections and Watches

**Feature Branch**: `codex/073-uniform-query-surface`

**Created**: 2026-11-21

**Status**: Draft

**Input**: Specify #471 using the agreed concept: connection arguments are first/after, last/before, namespace for namespaced resources, and query replacing separate selection inputs. Watches use the same query syntax but remain backed exclusively by the durable CDC journal. Connections/search use configured search or recommendation engines where supported, with capability subsets exposed through spec 072. Deliver one breaking cutover; ignore the issue's deprecation-window proposal.

## User Scenarios & Testing *(mandatory)*

### User Story 1 - Select and Page Resources with One Query Language (Priority: P1)

An Admin, CLI, Storefront, or third-party client selects and orders resources using one query string instead of learning a separate filter or selector input for each kind. The selected namespace or trusted publication/parent context remains an authorized store boundary, according to the owning API contract.

**Why this priority**: A consistent selection surface stops new per-kind input variations while retaining predictable, bounded resource access.

**Independent Test**: Against a deployment supporting only existing indexed predicates, query representative namespaced and cluster-scoped connections, compare results with a known dataset, and page forwards/backwards without invoking a search engine.

**Acceptance Scenarios**:

1. **Given** a namespaced connection, **When** a client supplies its namespace, query and forward page arguments, **Then** only authorized matching resources in that namespace are returned with the existing resource/connection representations.
2. **Given** a cluster-scoped connection, **When** the client supplies a query and pagination, **Then** it needs no namespace argument or per-kind filtering input.
3. **Given** a supported exact-label or category-subtree predicate, **When** a client requests equivalent selections, **Then** results retain the owning predicate's meaning, including descendant inclusion and depth boundaries.
4. **Given** a stable supported ordering, **When** the client traverses a result forwards and backwards, **Then** both directions agree on order, page boundaries and membership; invalid direction combinations are rejected.
5. **Given** a cursor issued for another namespace, query or incompatible result revision, **When** it is reused, **Then** the client receives an explicit error rather than a misleading page.

---

### User Story 2 - Use Available Search and Recommendation Capabilities (Priority: P2)

A client discovers which retrieval modes and predicate combinations the deployment supports, then uses the same connection query surface for lexical, semantic, hybrid, similarity or configured recommendation retrieval.

**Why this priority**: The language must support dedicated discovery engines without requiring every deployment to install one or pretending that backend capabilities are interchangeable.

**Independent Test**: Exercise an indexed-datastore-only deployment and controlled search-provider capability fixtures. Verify supported queries, unavailable combinations and result boundaries without requiring every provider to implement every feature.

**Acceptance Scenarios**:

1. **Given** an available indexed lexical search capability, **When** a client submits a supported text query with supported namespace filters, **Then** results come from a bounded authorized plan with documented relevance ordering.
2. **Given** a compatible semantic/hybrid or item-similarity provider, **When** that retrieval mode is requested, **Then** its semantics are preserved; it is not silently converted into keyword matching.
3. **Given** a configured Storefront recommendation surface, **When** an eligible Storefront client supplies the surface's supported context, **Then** serving follows the owning recommendation contract, including its declared context and fallback rules; ranked outputs remain runtime data.
4. **Given** a provider supports text search but cannot combine it with the required namespace restriction or pagination contract, **When** the combination is requested, **Then** it is rejected rather than approximated by an unbounded scan or silently incomplete post-filtering.
5. **Given** a previously available provider is unavailable, **When** a client submits a query, **Then** failure is explicit; another backend is used only if it preserves the same approved semantics and bounds.
6. **Given** the configured search/recommendation provider advertises the required kind/operation capabilities through the same contract that enables advanced query syntax, **When** a client uses the generic search surface, **Then** it receives one Relay-compatible SearchItemConnection whose edges contain SearchItem resource-union nodes selectable through inline fragments, retaining declared scope, authorization and pagination semantics.
7. **Given** that provider or a requested member capability is unavailable, **When** generic search is requested, **Then** discovery and structured rejection reflect that limitation; the server does not emulate it through unbounded per-kind fan-out.

---

### User Story 3 - Keep Controllers on Durable Filtered Watches (Priority: P1)

A controller uses the same predicate language for initialization lists and change subscriptions. Its watch remains a durable resource-event stream independent of search/recommendation engines.

**Why this priority**: Query-surface consistency must not weaken reconciliation, recovery, ordering or namespace isolation.

**Independent Test**: Initialize through a supported list predicate, open the corresponding journal watch, mutate matching and nonmatching resources, disconnect/restart a replica and resume. Repeat with the search provider stopped.

**Acceptance Scenarios**:

1. **Given** a predicate supported for both listing and watching, **When** a controller initializes and watches that selection, **Then** predicate meaning agrees while watch delivery retains existing journal ordering, event types and payloads.
2. **Given** an existing label-selector watch case, **When** its equivalent query is used, **Then** admitted changes, selector membership transitions, deletion payload rules and bookmarks match the established watch contract.
3. **Given** a valid durable resume position, **When** a watch reconnects through another API replica, **Then** it follows existing replay/deduplication/bounded-delivery semantics. An expired position still fails explicitly.
4. **Given** a search-engine outage or model/index update, **When** a predicate-only watch is active, **Then** watch execution does not contact that engine or change its ordering/membership because of ranking.
5. **Given** a ranking, semantic, recommendation or sort clause on a watch, **When** the client subscribes, **Then** it receives a structured unsupported-query error before stream execution; the server does not turn the request into a live search result set.
6. **Given** an initial list overlapping writes, **When** the controller continues through its journal-consistent initialization checkpoint, **Then** it does not silently miss admitted changes. A page cursor is never mistaken for a watch resume position.

---

### User Story 4 - Make One Explicit Contract Cutover (Priority: P1)

Maintainers update schemas and supported consumers together so clients use one current contract, with consistent validation and diagnostics from the first release.

**Why this priority**: A deprecation period would retain the parallel selection surfaces this feature is intended to remove.

**Independent Test**: Inspect the generated schema and compile/run all first-party list/watch consumers and fixtures against the replacement contract; exercise old argument names and malformed/unsupported queries.

**Acceptance Scenarios**:

1. **Given** the replacement release, **When** connections and watches are inspected, **Then** the old selection arguments have been removed, with no deprecated aliases or dual-input precedence rules.
2. **Given** controller initialization/reconnect, CLI examples and other owned consumers, **When** their tests execute, **Then** they use the new query argument and retain their existing scope/security and recovery behavior.
3. **Given** malformed syntax, an unknown term, an unsupported combination or an unauthorized predicate, **When** evaluated, **Then** the existing structured diagnostics conventions identify the failure without leaking protected values.
4. **Given** spec 072 discovery, **When** capabilities are reported, **Then** they distinguish connection/search support from journal-watch support and reflect actual registered functionality rather than backend brand names.

### Edge Cases

- Blank query versus malformed query; escaped quotes, Unicode, wildcard literals, repeated terms, explicit grouping and operator precedence must have documented meanings.
- A syntactically valid expression can have no bounded indexed plan. Reject it even when every individual term appears supported.
- Unsupported predicates nested inside OR/NOT must not disappear during normalization or partial execution.
- Namespace arguments and authorization cannot be overridden by terms inside the string; nested relationship queries cannot escape their parent relationship.
- A recent write may be visible in the datastore but not yet in an asynchronous search index. Do not imply authoritative snapshot or watch-checkpoint semantics for an eventually indexed ranked result.
- Equal scores, changed embedding models, changed query meaning, expired result windows, provider outages and permission revocation can invalidate pagination; cursors are not grants.
- A resource can enter or leave a watch predicate on modification. Preserve established selector transition semantics using the durable source facts required by the predicate.
- Relationship predicates may depend on another resource changing. They are not watchable unless those membership changes are durably represented; no search-engine polling to fill the gap.
- A filtered initial list spanning pages needs a safe journal horizon; reading the latest position only after an inconsistent list must not be called lossless initialization.
- Missing watch kind registration remains unsupported; query support must not create a process-local watch source.
- Mixed old/new clients and servers during cutover must fail safely or be drained; retaining old filter/selector aliases is not the remedy.
- Newly available backend features, including ScyllaDB text/vector functionality, must pass the whole namespace/filter/order/pagination contract before advertisement. LIKE filtering alone is not proof of indexed full-text search.
- A heterogeneous search page requires one supported ordering and cursor across all selected resource kinds. Reject unsupported cross-kind ranking/filter combinations rather than concatenate separately paged per-kind results.

## Requirements *(mandatory)*

### Functional Requirements

- **FR-001**: Connection fields MUST expose only first, after, last, before, query and, where their resources are namespace-scoped, namespace as selection/pagination arguments. Add backward pagination where a supported connection lacks it. No separate filter, selector, sort, ranking or provider-options argument. Single-resource lookups and mutations are outside this argument replacement.
- **FR-002**: Root namespaced connections MUST retain their owning API's authorized namespace boundary (explicit in Admin, or resolved from trusted Storefront publication/target context); cluster-scoped connections MUST not acquire one. Parent-linked connections MUST inherit their existing relationship/scope boundary and may only narrow it. Query expressions MUST not widen or replace those boundaries.
- **FR-003**: Provide one versioned published query language and normative grammar, with AIP-160-inspired expressions and the agreed GitHub/Shopify-style term ergonomics. Define quoting, escaping, typed literals, grouping, implicit AND, explicit Boolean operators, precedence, negation and wildcard meaning before implementation. Document any deviation from AIP-160 rather than claiming strict compatibility.
- **FR-004**: Shared predicates MUST have the same meaning on every operation/backend that advertises them. Existing exact-label, match-expression and category-descendant/include-self/depth behavior MUST have an explicit equivalent query form and parity fixtures. Preserve owning field names/reference semantics rather than add undocumented aliases.
- **FR-005**: Query MUST carry supported ordering and retrieval instructions, including lexical, semantic, hybrid and item-similarity modes where registered. At most one retrieval mode may be selected; reject ambiguous Boolean placement or incompatible ranking/sort combinations. Exact keywords and grammar are contract-design work, not fixed by earlier illustrative examples.
- **FR-006**: Recommendation serving belongs to the Storefront API, not the Admin query surface. Its query instructions MUST refer to the owning configured model/surface and validated context contracts from #29/#256/#257. Authorization and privacy govern referenced seeds/context. This feature MUST not create a parallel recommendation configuration model, accept arbitrary private context, or store computed rankings in Git.
- **FR-007**: Execute connection/search queries only through a bounded plan backed by implemented indexes/projections or compatible configured engines. Choose dedicated search/recommendation execution where available for the requested semantics; datastore-only deployments retain their supported indexed subset. No namespace/table scans, live related-resource aggregates, silent term dropping or semantic downgrade.
- **FR-008**: Capability registration MUST identify support by resource kind, operation, field/operator, retrieval mode, ordering and relevant combinations/limits. Integrate with spec 072's discovery model, not a second discovery API. Backend installation or a provider-wide feature flag alone MUST not imply a supported complete plan.
- **FR-009**: Provider adapters MUST validate namespace isolation, authorized context, index readiness/version, filtering combinations, pagination and ordering before advertising a term/combination. ScyllaDB, Qdrant and Typesense are candidate backends, not interchangeable promises. Feature availability follows verified deployment/version/index support rather than assumed product editions or maturity labels.
- **FR-010**: Reject malformed syntax, unknown/unservable terms, unsupported operators/combinations and over-budget queries explicitly. Reuse existing GraphQL error codes and diagnostic shape (reason, message, level, optional field) with safe query locations/term references defined in the contract. Unsupported capability is not an empty result; provider unavailability is not bad syntax.
- **FR-011**: Authorize resource access, sensitive term use and referenced recommendation context under the selected pluggable AuthN/AuthZ/UserDir contracts. Access predicates are server-owned and cannot be negated by the client. Recheck access when returning cached/ranked pages; no cross-caller result or cursor leakage.
- **FR-012**: Support either forward or backward pagination per request with finite page bounds and stable tie-breaking. Reject incompatible direction/cursor combinations. Preserve the same observable order in both directions; sorting MUST not be reinterpreted when paging backwards.
- **FR-013**: Pagination cursors MUST bind the normalized query, resource kind, namespace/parent boundary, ordering and relevant execution/result revision and authorized context. Reject incompatible/tampered/expired cursors. Normalization MUST preserve literal, grouping and retrieval semantics; cursor opacity remains unchanged.
- **FR-014**: Ranked pagination MUST provide a bounded stable result window or another proven equivalent contract, with explicit expiry and size limits; backward pages traverse that same ranking. Do not claim exhaustive results beyond the window, manufacture total counts or silently repeat live offset searches. Advertise only support that meets this contract across replicas.
- **FR-015**: Typed and generic resource watches MUST replace selector inputs with query while retaining existing kind/namespace and resourceVersion behavior. Generic watches still identify kind separately. Preserve explicitly authorized cross-namespace watching and reject namespace escapes; uniform argument naming does not remove existing authorization checks.
- **FR-016**: Watch execution MUST remain exclusively backed by the durable CDC-derived journal and existing watch infrastructure. Preserve event envelopes/types, selector transition meaning, admitted ordering, bookmarks, payload/deletion rules, replay bounds, resume expiry, missing-kind errors and backpressure. No search/recommendation provider dependency or process-local replacement stream.
- **FR-017**: Watches MUST accept only their registered deterministic predicate subset. Reject ranking/retrieval/recommendation/sort clauses before streaming. Admit relation-dependent predicates only when complete dependency/membership transitions are durably represented and bounded. Search/model changes alone MUST not reinterpret a watch as a ranked result set.
- **FR-018**: Initialization lists and corresponding watches MUST share predicate meaning for the supported intersection. Supply a journal-consistent initialization horizon through existing checkpoint semantics or a specified connection-level checkpoint where missing; pagination cursors remain distinct. Index-lagged ranked results MUST not masquerade as lossless journal initialization.
- **FR-019**: Resume/replacement MUST preserve the established journal cursor contract. A controller maintaining a complete matching set MUST reinitialize appropriately after changing its predicate; do not claim an old predicate's cached membership is complete for a new one. Search/provider capability changes cannot silently alter an existing watch predicate.
- **FR-020**: Deliver one intentional breaking cutover. Remove replaced filter/selector arguments and remove their input types only when no remaining operation owns them. No deprecation windows, compatibility aliases, dual execution paths or silent old-client translation. This explicitly overrides #471's deprecation proposal.
- **FR-021**: Update affected API schemas/resolvers, controller paged list/watch clients and checkpoints where necessary, generated artifacts, tests, examples and documentation as one coherent contract change. Identify independently released consumers and publish the required release ordering. Do not add new envelope/metadata/status/reference variants or use mutation Payload/input-argument conventions for new query surfaces.
- **FR-022**: Mixed-version deployment MUST preserve safety without retaining removed arguments. Specify which replicas/clients can serve the replacement contract; drain incompatible recipients or explicitly reject unsupported operations. A capability validator cannot make an old binary enforce a new floor. Cutover and rollback instructions MUST account for valid cursor/result-window lifetimes and controller initialization.
- **FR-023**: Keep bounded source-owned definitions of fields/terms/operators and share predicate validation/meaning across consumers. Do not maintain independent hand-written per-kind input maps, provider interpretation tables or watch-language dialects that can drift. Generated-source freshness and semantic conformance/parity tests MUST both run in normal checks.
- **FR-024**: Publish safe diagnostics and bounded operational signals for parse/plan rejection, capability selection, index lag, cursor/window expiry, watch replay/backpressure and provider failure/recovery. Logs/telemetry MUST not expose credentials, raw private queries, recommendation subject data or unbounded query-derived labels.
- **FR-025**: Generic search MUST return one Relay-compatible SearchItemConnection with SearchItemEdge entries whose node is a SearchItem union of canonical resource object types. Do not introduce per-kind Search*Connection types or a union of connections. Clients select resource-specific fields through inline fragments on the node union. Reuse the same query language, allowed connection arguments and shared search/recommendation provider capabilities that power advanced typed queries, with no separate infrastructure prerequisite or parallel capability contract. Advertise actual kind/operation/combination support through spec 072; enforce authorization, namespace boundaries, bounded execution and one stable ordering/pagination contract across the requested kinds. Reject unsupported cross-kind plans instead of concatenating independent pages. Datastore-supported typed queries and journal watches remain available without those provider capabilities.
- **FR-026**: This feature MUST support API-specific exposure over shared query/planner/provider infrastructure. Qualify capabilities by API endpoint/surface and operation; do not assume Admin-only contracts or equal subsets across APIs. Admin uses management authorization and resources; Storefront uses its owning published representations, trusted target binding and visibility policy. Never expose Admin types/drafts through Storefront search, share cursor/window authority across surfaces, or expose management journal watches through Storefront by implication. Final Storefront SDL, recommendation context and serving guarantees MUST derive from its first substantive contract.

### Production Requirements *(mandatory for core-service or load-bearing changes)*

- **PR-001 Replica Safety**: Verify all affected paths with at least two APIs and two controllers, including cross-replica page/resume use, concurrent changes, replacement and a mixed-version breaking cutover. Queries, windows and checkpoints must survive or fail explicitly after replacement, never rely on unannounced process-local correctness state. Git remains one active process with retained storage and non-overlapping replacement; no Git HA or autoscaling scope.
- **PR-002 Multi-User Security**: Test at least two namespaces, ordinary and privileged principals and pluggable provider combinations, including cross-namespace watch privileges, term authorization, seed/context references, cursor reuse and revocation during pagination/replay. Hidden index hits and rejected query diagnostics must disclose no protected resources or subjects. Apply authoritative access before returning engine-derived resources.
- **PR-003 Capacity**: Proposed unmeasured acceptance envelope: verified retained >=5,000,000 products and >=10,000 categories, two APIs/two controllers/one Git, isolated writable fixtures, 100 structured connection reads/s, 25 supported ranked reads/s and 2,500 predicate watches receiving a 20-change/s owned workload for 30 minutes alongside the existing connected authoring/native-push/watch workload. Structured read p95 <=250 ms/p99 <=1 s; ranked read p95 <=750 ms/p99 <=2 s; admitted matching change visibility p95 <=1 s/p99 <=3 s; unexpected errors <1% outside faults. Preserve stricter existing profile/domain gates. Planning MUST confirm payloads, hardware/resource ceilings and offered-rate attribution before implementation; these are targets, not current capacity claims.
- **PR-004 Backpressure**: Proposed defaults: query <=8 KiB, <=256 lexical tokens, <=128 syntax nodes, <=16 nesting levels, <=64 predicates, <=4 sort keys, <=100 resources/page and <=1,000 retained ranked candidates/window with finite expiry. Provider calls, candidate hydration, cursor/window retention, fan-out, watch buffers/replay and retries MUST have explicit finite budgets. Confirm defaults during planning without weakening resource/index safety. Test five minutes at twice declared offered read/change rates; throttling is explicit and zero leaks/incorrect results are tolerated. Restore steady-state gates within 30 seconds of overload removal.
- **PR-005 Capacity Evidence**: Reuse `make capacity TARGET=api PROFILE=endpoint-split MODE=<diagnostic|alpha|production>` for connected query coexistence, `TARGET=namespace PROFILE=watch` for durable filtered replay/recovery and `TARGET=category PROFILE=hierarchy` for existing subtree/index parity. Extend existing runners/verifiers through documented selectors, not feature-specific public targets; these query probes are proposed work. Receipts MUST identify exact source/config/provider/index versions, actual topology, verified retained dataset, full durations, payload/query distributions, resource ceilings and correctness oracles. Diagnostic or partial runs do not qualify production.
- **PR-006 Fault Recovery**: Reuse explicitly owned `make chaos CHAOS_PROFILE=api-restart`, `controller-restart` and `journal-interruption` cases where applicable, plus a bounded owned search-provider outage fixture specified during planning. Search failure must not disrupt valid journal watches. Journal faults preserve existing explicit availability/expiry/recovery rules. Resume healthy criteria within existing stricter profile deadlines and no later than 60 seconds after dependency readiness; cold reconstruction still follows documented bounded initialization, not a false instant-recovery claim.
- **PR-007 Recovery and Integrity**: Verify no unaccounted matching changes, cross-scope events, silent cursor reset, inconsistent ranking pages or stale unauthorized results through faults/cutover. Preserve shared catalogue data, Git histories, checkpoints and unrelated workloads. Cutover may require a documented cold relist but cannot discard unprocessed admitted work or overlap processes on one checkpoint directory. Search indexes remain derived runtime state and cannot replace journal or Git authority.

### Key Entities *(include if feature involves data)*

- **Query Expression**: Versioned client selection/retrieval/order intent with defined grammar and typed term meaning, shared across operations where supported.
- **Capability Registration**: Kind/operation-specific supported terms, operators, combinations, limits and availability within spec 072 discovery.
- **Executable Query Plan**: Validated authorized bounded use of available indexes/projections/providers; no guarantee follows merely from parsing successfully.
- **Connection Page / Ranked Window**: Existing resource connection representation with stable order, opaque continuation and bounded result identity/expiry where ranking requires it.
- **Search Connection / Search Item**: One Relay-compatible connection of edges and PageInfo, with canonical resource objects represented by a SearchItem node union. The same registered provider capabilities as advanced query syntax govern supported kinds and cross-kind plans.
- **Journal Watch Predicate**: Deterministic registered predicate over durable watch facts; never a ranking or live recommendation subscription.
- **Initialization Horizon**: Journal-consistent checkpoint connecting an initialization list to resumed event processing, distinct from page cursors.
- **Cutover Contract**: One replacement interface and coordinated producer/consumer release requirements, with explicit incompatible-client behavior and rollback safety.

## Success Criteria *(mandatory)*

### Measurable Outcomes

- **SC-001**: Every in-scope connection exposes exactly the allowed pagination/query/namespace argument family, and every replaced watch selector uses query. Schema conformance tests detect any reintroduced parallel filter/selector argument or mutation-style query wrapper.
- **SC-002**: All existing label/match-expression and category subtree boundary fixtures have equivalent supported expressions and identical authorized results; forward/backward traversal produces no duplicate or missing resource within the declared stable result contract.
- **SC-003**: Every advertised provider/operation capability passes a whole-plan fixture, including namespace/filter/pagination constraints. Every deliberately unsupported combination fails explicitly with zero scan fallback, ignored predicates or semantic downgrade.
- **SC-004**: In all supported list/watch predicate fixtures, controllers initialize and resume with the established membership/event semantics across either API replica, with zero unaccounted admitted changes and explicit expiry failures.
- **SC-005**: Predicate watches continue correctly through a search-provider outage and reject all ranking/recommendation/sort-only requests before execution. No watch test relies on search polling or a ranked-result event envelope.
- **SC-006**: Security fixtures yield zero cross-namespace/subject/cursor leakage, unauthorized term use or stale cached grants, including revocation and provider/replica replacement.
- **SC-007**: The declared 30-minute healthy workload meets all latency/error/resource/domain gates; doubled-load and fault tests retain correctness and recover within the declared bounds. Evidence distinguishes offered load from completed work and excludes silently dropped work from success.
- **SC-008**: All inventoried first-party consumers/examples/generated artifacts pass against the new schema, and removed arguments fail as expected. The release contains one contract with no deprecation window or compatibility selection path.
- **SC-009**: Normal contributor checks fail on deliberate mutations that alter a shared predicate's meaning, skip capability/authorization validation, mismatch query/cursor binding or route a watch to search. Syntax/schema-generation checks alone are insufficient.
- **SC-010**: With the required capabilities advertised by the shared search/recommendation provider contract, contract fixtures select each advertised SearchItem resource-union member through edge-node inline fragments and verify one SearchItemConnection's authorization, scope and forward/backward pagination across supported kinds. Without those provider capabilities, datastore-supported typed queries and journal watches remain usable and unsupported generic search fails explicitly without unbounded fan-out.
- **SC-011**: Cross-API fixtures prove endpoint-qualified discovery and plan rejection, no Storefront access to Admin-only types/fields/drafts or management watches, no cross-surface cursor/window reuse, and recommendation serving enabled only on the eligible Storefront surface. Validate those fixtures against the first substantive Storefront contract before treating cross-API implementation planning as final.

## Assumptions and Scope

- The agreed user instructions supersede [#471](https://github.com/gitstore-dev/GitStore/issues/471) where it proposes deprecation windows or requires Qdrant before any query surface can land. Indexed datastore and journal subsets may ship independently; richer retrieval is available only when its compatible provider/owning feature exists. Uniform syntax does not promise every clause on every operation.
- Spec 072 / [#592](https://github.com/gitstore-dev/GitStore/pull/592) owns the deployment-discovery model. This feature defines and registers search/watch capability subsets there; it does not duplicate that API or assume the discovery implementation is already shipped.
- [#29](https://github.com/gitstore-dev/GitStore/issues/29), [#256](https://github.com/gitstore-dev/GitStore/issues/256) and [#257](https://github.com/gitstore-dev/GitStore/issues/257) retain recommendation configuration, serving context, events and reconciliation scope. This spec integrates their supported retrieval surface, not a new recommendation platform or all provider integrations at once.
- The first substantive Storefront contract is the recommended sequencing baseline for completing this design; its current boundary-only schema is insufficient. Recommendation serving is Storefront-owned; Admin management/configuration/diagnostics remain with their owning features. See [API surface ownership](contracts/api-surfaces.md).
- Exact EBNF, term spellings, backend choice for the first rich-retrieval adapter, window implementation, checkpoint field and resource budgets are planning decisions constrained above. Earlier conversation examples are illustrative, not a committed grammar or working endpoint claim.
- Generic search returns one SearchItemConnection with SearchItem resource-union nodes using the same capabilities contract and configured search/recommendation server that powers advanced query syntax. Qdrant, Meilisearch, Elasticsearch and Typesense are candidate providers within that shared model, not separate generic-search infrastructure or promises of identical features. Provider capabilities determine supported resource-kind combinations and cross-kind ordering; additional per-kind search connections are excluded.
- Existing durable watch behavior and canonical current schemas take precedence over outdated historical spec examples. This feature changes selection arguments, not resource/status envelopes, event schemas, cursor purpose or journal storage authority. It adds no live ranked-result subscription.
- Protocol names, surface arguments and Make selectors in this specification are user/repository contract requirements, not prescribed implementation libraries. This invocation produces only requirements/checklist artifacts; no runtime changes, provider deployment, live faults or production qualification.

## Existing Evidence and Design References

- [Current connection/watch schema roots](../../shared/schemas/admin/schema.graphqls), [Product connections/watches](../../shared/schemas/admin/product.graphqls) and [Category filter contract](../../shared/schemas/admin/category.graphqls): owning argument/event contracts to inventory, not proof of the new query implementation.
- [Durable watch materialization](../../gitstore-api/internal/watchjournal/materializer.go) and [subscription path](../../gitstore-api/internal/watchjournal/subscriber.go): preserve journal facts, selector transitions and bounded replay rather than create another source.
- [Controller list/watch](../../gitstore-controller-manager/internal/listwatch) and [capacity catalogue](../../tests/capacity/README.md): reuse existing consumer/checkpoint/evidence families.
- [AIP-160](https://google.aip.dev/160), [Qdrant query API](https://api.qdrant.tech/api-reference/search/query-points), [Typesense search](https://typesense.org/docs/29.0/api/search.html) and [vector search](https://typesense.org/docs/29.0/api/vector-search.html): grammar inspiration and provider behavior to validate, not pass-through public syntax.
- [ScyllaDB SELECT/filtering](https://docs.scylladb.com/manual/stable/cql/dml/select.html), [full-text constraints](https://docs.scylladb.com/manual/stable/features/fulltext-search.html#query-constraints) and [Cloud feature matrix](https://cloud.docs.scylladb.com/stable/vector-search/vector-search-clusters.html#feature-compatibility-matrix): verify actual filtering, paging, isolation and version support; do not infer an eligible complete query plan from text/vector feature availability alone.
