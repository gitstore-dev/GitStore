# Feature Specification: Publication-backed Storefront Catalog Reads

**Feature Branch**: `codex/078-storefront-catalog`

**Created**: 2026-11-23

**Status**: Draft

**Input**: Priority3, the first substantive Storefront catalog contract from #284 with #271 mapping and #283 media/body dependencies. Consume076 publication authority and 077 effective context; define public Product/ProductVariant and discovery relationships that let073 finalize its Storefront query contract without waiting for checkout or a complete frontend.

## User Scenarios & Testing *(mandatory)*

### User Story 1 - Browse and Inspect the Published Catalog (Priority: P1)

A buyer lists published products and opens one product with its selected sellable variants and navigation relationships. Private drafts or unrelated targets never appear.

**Why this priority**: This is the first useful public catalog behavior missing from the existing unavailable endpoint.

**Independent Test**: Use prepared snapshot/target/context fixtures to list and look up public resources, advance the private catalog, replace the active publication and probe unauthorized IDs without requiring search or checkout.

**Acceptance Scenarios**:

1. **Given** public snapshot A and newer private B, **When** a buyer lists, looks up or traverses resources, **Then** every result is hydrated from A's approved public representation and never from B.
2. **Given** a Product with explicitly selected Variants, **When** related data resolves, **Then** only eligible selected relationships appear; publication of the parent alone does not publish every sibling Variant.
3. **Given** an expired, suppressed or wrong-target identity, **When** accessed through any supported root/node/relationship path, **Then** the same visibility authority applies without leaking draft existence through errors or counts.

### User Story 2 - Select a Variant Using Stable Public Identity (Priority: P1)

A buyer chooses offered option values and identifies the correct sellable Variant, including no-axis/default-variant and optional-SKU cases.

**Why this priority**: The public schema must provide stable sellable identity before frontend mappings, search results or cart contracts depend on it.

**Independent Test**: Exercise pinned option/value/Variant fixtures, reordered labels, renamed presentation, missing/duplicate/incomplete combinations, absent SKU and unavailable offerings without implementing a cart.

**Acceptance Scenarios**:

1. **Given** valid selected option keys, **When** presentation order/labels or locale changes, **Then** the same published Variant identity remains selectable without SKU-derived identity.
2. **Given** incomplete/ambiguous combinations or unsupported evolving option contracts, **When** a release/public read is prepared, **Then** the explicit eligibility/unsupported policy applies rather than inventing the Cartesian product or another Variant.
3. **Given** optional inventory or SKU, **When** public fields resolve, **Then** absence is represented truthfully and does not imply stock, reservation or universal unavailability.

### User Story 3 - Render Contextual Prices and Public Content (Priority: P2)

A buyer receives price/availability facts, translated presentation, safe body content and usable media with deterministic fallbacks for optional material.

**Why this priority**: A catalog contract must be useful for listing cards and detail views without leaking authoring data or manufacturing commerce facts.

**Independent Test**: Read fixtures with varied currency/locale/eligibility, missing optional media, expired signed URLs, safe/unsafe body references and no-applicable-price cases under pinned effective context.

**Acceptance Scenarios**:

1. **Given** the same effective077 context, **When** product/variant prices and summaries resolve, **Then** currency, eligibility, time validity and no-price behavior agree across nested fields.
2. **Given** published translations/media/body references, **When** rendered, **Then** source revisions and fallbacks follow the pinned snapshot and no credential/private raw source or unresolved interpolation escapes.
3. **Given** a large relationship set or unavailable aggregate capability, **When** a public summary is requested, **Then** it is complete and bounded or explicitly unsupported/unavailable; a first page cannot masquerade as a full price range or stock total.

### User Story 4 - Page Safely and Supply Search-ready Public Representations (Priority: P2)

A client pages through published listings and a Commerce adapter maps the results to its route contract. The same public types/context boundary can later feed073 and dedicated search/recommendation providers.

**Why this priority**: Stable types and pagination break the circular dependency between a nonexistent Storefront schema and the shared query design.

**Independent Test**: Forward/backward page typed listings through two API actors, change context/publication between pages and compile representative Commerce mapping fixtures; no provider deployment or complete UI is required.

**Acceptance Scenarios**:

1. **Given** a bounded published listing, **When** forward/backward pagination is used, **Then** stable ordering and context-bound cursors preserve one coherent public view or reject changed/expired continuation explicitly.
2. **Given** unsupported query terms or backend capabilities, **When** requested, **Then** the initial supported subset is reported/rejected explicitly without scans, silent filtering or private fallbacks.
3. **Given** a frontend's sort/reverse/filter inputs, **When** its adapter maps them, **Then** core selection stays within the shared query contract rather than duplicating Commerce-specific GraphQL arguments.

### Edge Cases

- A stable public UID is retained across publications but unavailable on the caller's target; handle/name reuse is not authority.
- A selected Variant's parent or media dependency is absent; publishing a Product does not include all Variants automatically.
- Options from issue492 evolve while labels translate; an absent SKU under issue500 cannot become the fallback identity.
- A price range would mix currencies, ignore eligibility or summarize only the loaded Variant page.
- Missing inventory is mistaken for out-of-stock/in-stock; availability is mistaken for a reservation or checkout guarantee.
- A publication/context changes between pages; suppression happens after a search candidate is indexed.
- Body serialization, locale fallback or URL signing versions change; raw Admin markup/status/secret references must not become a fallback.

## Requirements *(mandatory)*

### Functional Requirements

- **FR-001**: Implement a Storefront-local catalog read/representation contract over076's immutable published authority and077's effective context. Preserve separate schemas/listeners and return explicit unavailable when prerequisites are absent; no Admin current-row or Git-on-public-request fallback.
- **FR-002**: Define initial public Product, ProductVariant, Collection, Category and media/body relationships, lookup/global identity behavior and omission/null/error semantics. Expose only reviewed public fields, not wholesale Admin objects or management watches/status.
- **FR-003**: Reuse canonical identity, reference, resource envelope/status concepts where exposed, without duplicating metadata contracts. Distinguish public projection/value types from independently stored resources; a public representation cannot authorize access through a management Node ID or import private ownership/configuration.
- **FR-004**: Apply publication membership, current suppression, trusted target and request eligibility consistently to root lookups, node access, nested relationships, connections, cursors and derived metadata. A resource retained in an older snapshot is not automatically currently public.
- **FR-005**: Connections use Relay-compatible edges/pageInfo and first/after, last/before and query selection conventions. Scope is inherited from trusted Storefront/parent context; any namespace argument allowed by the owning baseline is only validated narrowing, never target authority. Do not introduce filter/sort/reverse/provider argument families.
- **FR-006**: Initial typed reads may support blank query and explicitly implemented indexed subsets before richer073 execution exists. Unsupported terms/combinations fail existing structured diagnostics. Basic catalog reads must not require a rich search server, and cannot emulate missing query support with full-catalog scans.
- **FR-007**: Define stable published resource/Variant identity independently of mutable labels, option presentation and SKU. Review the public implications of issues492/500 before freezing concrete fields; preserve supported current contracts or explicitly version the approved change rather than silently assume those proposals shipped.
- **FR-008**: Define selected-option/value identity, valid offered combinations, default/no-axis Variant behavior and incomplete/duplicate combination handling for the initial supported model. Reordering/translation cannot change semantic selection; do not materialize all possible combinations implicitly.
- **FR-009**: SKU is not universal sellable identity. Optionality and capability-scoped requirements follow the owning Variant/release contract. Unsupported inventory/fulfillment/vertical capabilities cannot be inferred from Product/Variant existence.
- **FR-010**: Expose contextual prices and no-applicable-price outcomes through explicit public types using077's currencyCode/evaluation/eligibility policy. Preserve authored templates privately/in snapshots and avoid implicit FX conversion or customer-controlled entitlement overrides.
- **FR-011**: Public availability must distinguish declared known availability, unavailable and unsupported/unknown/untracked observation. It is not stock reservation, checkout acceptance or proof of inventory implementation. Specify freshness and applicable optional-capability behavior before advertising it.
- **FR-012**: Price ranges, counts, availability summaries and relationship-derived fields require complete bounded public projection inputs/materialized summaries under the owning aggregation rules. Never query live related-resource aggregates in GraphQL resolvers, use a first Variant page as a total or expose private candidates through counts.
- **FR-013**: Consume implemented Translation071 and076 snapshot selection with077 locale/fallback policy. Freeze public content/definition translations for that release; distinguish missing required localized dependencies from permitted optional fallback and never localize stable identity keys.
- **FR-014**: Define the safe initial body contract aligned with ADR0013: pinned public serialization/resolution, supported node/reference kinds, sanitization and unavailable behavior. Required unresolved/private references fail preparation; raw authoring source/HTML/interpolation is not a public fallback. A minimal supported subset must be explicit rather than imply the whole future content engine.
- **FR-015**: Resolve File/media dependencies behind backend-owned publication boundaries in line with issue283. Public/signed URL semantics, alt/content-type/variant metadata, expiry and optional fallback must be deterministic and authorization-safe; provider credentials and private source paths never enter public output.
- **FR-016**: Pin public result representation, publication, context and ordering into cursor/cache identity. Reject incompatible context/snapshot/authority or expired continuation explicitly; do not mix releases or share personalized output publicly. Preserve the same logical order when paging backwards.
- **FR-017**: Supply073 a versioned public kind/field/reference/representation registry boundary and safe provider-document inputs. Search candidates must hydrate from this public authority and be revalidated for visibility, including snippets/facets/counts; never from Admin drafts.
- **FR-018**: Document the mapping needed by issues271/284 for product detail/listing, selected Variants, tags/navigation/media/SEO fallback and contextual summaries. Frontend route parameters map into core query semantics; do not copy outdated sort/reverse/filter arguments from issue284 into the canonical schema.
- **FR-019**: Use core read/list actions and existing pluggable AuthN/AuthZ/UserDir, errors/diagnostics and endpoint-qualified072 capabilities. No new authorization verb family or parallel discovery/error surface. Anonymous access still requires076's trusted target binding.
- **FR-020**: Keep public catalog reads independent of cart/checkout/customer mutation implementation, the Vercel UI scaffold and recommendation ingestion/model execution. No recommendation result is faked merely to satisfy a Commerce adapter helper; unsupported optional functions are explicit.
- **FR-021**: Define schema/public serializer version compatibility, safe activation/rollback and focused drift checks across076/077/073. Typed Storefront naming and admissible SearchItem members are resolved here before073's Storefront schema is finalized; generic SearchItemConnection implementation remains073's owner.
- **FR-022**: Document which initial fields/relationships are implemented versus unavailable/provisional. Plan required ADR0012/0013/0014 amendments/acceptance assessments and relevant docs/issues together; a compiled schema or fixture-only reader is not published production-read support.

### Production Requirements *(mandatory for core-service or load-bearing changes)*

- **PR-001 Replica Safety**: Focused implementation tests cover two API actors, publication/context replacement, cursor/cache behavior and resolver isolation. Production qualification adds actual concurrent replicas/mixed-version rollout and controller coexistence. Git remains singleton-only with retained non-overlapping replacement where exercised.
- **PR-002 Multi-User Security**: Two namespaces/targets and ordinary/anonymous/service identities test private Node IDs, nested relationships, hidden counts, signed media, suppression and stale continuation under pluggable providers. Zero draft/entitlement/credential leakage.
- **PR-003 Capacity**: Proposed production qualification: >=5M products/10K categories, 30 minutes at100 public typed reads/s with declared Product/Variant cardinality and content/media payloads plus sustained private authoring. Typed reads p95/p99 <=250 ms/1 s and unexpected errors <1%; measure real resources and retain stricter existing goals. These are unmeasured targets.
- **PR-004 Backpressure**: Planning assigns finite page, relationship/Variant candidate, projection, body/media, batch/hydration, cache, retry and execution budgets. Oversize/unsupported aggregates fail explicitly; one product's cardinality cannot create unbounded memory/related scans or starve other public reads.
- **PR-005 Capacity Evidence**: Reuse make capacity TARGET=api PROFILE=endpoint-split, TARGET=namespace PROFILE=watch and TARGET=repository PROFILE=lifecycle for public/private coexistence. New public projection probes are proposed. Focused changed-risk tests/full make pr-ready establish implementation readiness; deployed capacity/soak is separate and can be deferred for alpha with honest labels.
- **PR-006 Fault Recovery**: Focused fixtures stop publication/media/context dependencies and change snapshots during pagination. Production qualification adds owned API/controller restart and derived-index interruption where applicable; healthy reads resume <=60 s after dependency readiness without private fallback, reseeding or losing accepted publication work.
- **PR-007 Recovery and Integrity**: Verify byte/count limits, current takedown and snapshot/cursor version checks in every failure/rollout path. Production claims require complete declared healthy/overload/recovery evidence; fixture-only public types do not certify deployed catalog access.

### Key Entities *(include if feature involves data)*

- **Public Catalog Representation**: Versioned allowlisted resource projection from a pinned active snapshot, distinct from a mutable management object.
- **Published Variant / Selection**: Stable sellable identity and explicitly offered option/value combination, independent of SKU and translation.
- **Contextual Commerce Facts**: Price/availability values with explicit currency, eligibility, validity and support semantics.
- **Public Media/Body/Localization**: Pinned safely resolved presentation with controlled runtime URL validity and approved fallback.
- **Public Connection/Continuation**: Bounded ordered view bound to target/publication/context/representation and current disclosure.

## Success Criteria *(mandatory)*

### Measurable Outcomes

- **SC-001**: All A-public/B-draft lookup/list/relationship fixtures return only A's permitted projection, with zero draft hydration.
- **SC-002**: Every wrong-target/Node/relationship/metadata probe respects the same visibility rules and exposes zero unauthorized data.
- **SC-003**: Variant selection fixtures preserve stable identity across reordered/translated presentation and absent SKU without invented combinations or inventory claims.
- **SC-004**: Price/availability/content/media fixtures return deterministic valid facts or explicit unavailable/fallback outcomes; no mixed currencies, partial-page totals or private source leakage.
- **SC-005**: Forward/backward/context-change/publication-change fixtures produce stable pages or explicit invalidation, with zero cross-snapshot/caller cache reuse.
- **SC-006**: Unsupported query/provider features fail explicitly while supported basic published reads remain usable; no full-catalog scans or fake recommendation results.
- **SC-007**: Production claims require the declared latency/error/resource/recovery envelope; all implementation reports distinguish focused passing tests from deferred deployed qualification.
- **SC-008**: Contract fixtures supply073's concrete public result/field/context boundary and compile representative Commerce mappings without a second filter syntax or Admin type import.

## Assumptions, Dependencies and Scope

- Baseline origin/main86b1c15e includes Translation071; current Storefront schema is boundary-only and production handler remains unavailable. This spec creates requirements, not proof of public catalog runtime.
-076 owns immutable publication/target authority;077 owns effective context/pricing evaluation;078 owns public result semantics;073 owns shared query/provider execution and generic search. Feature-number references intentionally span isolated branches without broken local sibling links.
- [#284](https://github.com/gitstore-dev/GitStore/issues/284), [#271](https://github.com/gitstore-dev/GitStore/issues/271) and [#283](https://github.com/gitstore-dev/GitStore/issues/283) supply the initial read/adapter/media scope. [ADR0013](../../docs/ADRs/0013-markdown-body-intermediate-representation.md) owns body IR intent. Their proposed examples do not establish current implementation or override user-approved query conventions.
- Issues492/500 may change the future option/SKU contract. Planning must reconcile their public compatibility explicitly without pulling complete custom attributes, inventory, App capabilities or specialized commerce workflows into this first reader.
- Full Vercel Commerce UI, carts, checkout, payment, order/customer APIs, CRD host execution and recommendation serving/event collection are excluded. A read-only catalog can unlock073 before those features exist.
- Detailed SDL, serializer schemas, query subset registry, projection/storage protocols, aggregation inputs and concrete budgets belong in speckit-plan. This workflow changes no canonical code/schema/ADR status/issues and performs no live fault or production qualification.
