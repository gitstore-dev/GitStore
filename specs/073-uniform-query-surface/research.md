# Research: Uniform Query Surface

**Status**: Planning decisions, 2026-11-21; source `57459dc5`. Discovery dependency inspected at `codex/072-capabilities-discovery` (`51cbf97a`, PR #592).

## Language and ownership

**Decision**: API-owned dependency-free parser/typed AST; colon terms/Boolean predicates with top-level retrieval/order pipelines. Normative details in [query-language.md](contracts/query-language.md).

**Rationale**: One language supports indexed predicates and advanced engines without vendor-native public JSON or per-kind GraphQL inputs. Parsing alone does not prove support.

**Alternatives considered**: Raw vendor queries leak backend contracts; existing CEL is not the requested language. [AIP-160](https://google.aip.dev/160) inspires predicates, but colon terms/pipelines are explicit deviations, not strict compatibility.

## Existing indexed paths

**Decision**: Reuse `datastore.PageParams/PageResult`, namespace-sharded lists, `CategoryAncestorIndex` and `CategoryProductIndex`; authenticate backend positions in query-bound cursors. Implement selected indexed label postings and bounded parent-reference pages; reject unsupported plans.

**Rationale/source**: `scylla/backend.go` product listing merges 64 namespace shards; `category_service.go` uses bounded closure partitions. `keyset_cursor.go` carries only timestamp/ID; closure cursor carries depth/name. `ListProductsByLabelSelector` accumulates namespace pages, so cannot implement indexed query. `Collection.products` requires a compatible provider or owning asynchronous membership projection; absent either, explicit unavailable plan. `Product.productVariants` needs a paged parent-reference index instead of its slice helper.

**Alternatives considered**: Residual namespace scanning/slicing violates scale. Implementing Collection controller status would expand into Proposed ADR 0007; keep that decision separate.

## Watch facts and bootstrap

**Decision**: Initial watch terms cover immutable identity and labels in journal facts. Retain transition projection and existing bootstrap bookmark before paged lists. Add query identity to existing disk checkpoint; no new connection checkpoint field.

**Rationale/source**: `datastore/entities.go` retains current/previous labels, postimage and deletion name/scope, not arbitrary previous spec/status or namespaced deletion UID/version. Derived provider/posting writes therefore need a durable identity ledger and authoritative refresh on replay; journal facts alone cannot identify every deleted UID. `product_watch.go`/`watch.go` own transitions. Controller `listwatcher.go`, category/product/namespace/repository implementations already bootstrap before enumeration; `watchjournal/subscriber.go` captures high water.

**Alternatives considered**: Mutable-field predicates need deliberate durable fact changes. Search polling cannot supply resource events. Empty watch query is match-all; empty Collection selector still means no members.

## Shared providers and first adapter

**Decision**: One provider interface serves advanced typed query and generic union search. First real adapter is Qdrant, gated on configured index/mode readiness. Add bounded typed HTTP fixtures and an owned real integration, versioned scoped ID-only documents and a resumable projector/audit. Other vendors remain adapters to this same model.

**Rationale**: No general execution-provider interface exists in inspected API/shared source. Qdrant's [Query API](https://api.qdrant.tech/api-reference/search/query-points) offers filters/retrieval/limit/offset, not GitStore authorization or stable pagination automatically. Embeddings, sparse vectors and recommendation surfaces must come from configured owning features, never inferred from provider installation.

**Alternatives considered**: Requiring Qdrant for all syntax contradicts datastore/journal subsets; four mandatory integrations add needless scope. Proposed ADR 0019 App grants/installations are not implemented by an ordinary adapter.

## Ranked pagination

**Decision**: Offset-only adapters capture <=1,000 ordered IDs in shared Scylla TTL windows (10 minutes), with hard shared quotas and reauthorization. Qualification sizes quotas at 20,000 global/2,048 principal-scope windows for 25 fresh windows/s across 16 principals at 600-second retention. Native snapshot handles are allowed only with equivalent guarantees.

**Rationale**: API-local caches fail replacement; repeated live offsets change ranking. Elasticsearch [PIT/search_after](https://www.elastic.co/docs/reference/elasticsearch/rest-apis/paginate-search-results) can implement native snapshot continuation. Qdrant offsets, [Meilisearch search](https://www.meilisearch.com/docs/reference/api/search/search-with-post) and live Typesense paging alone cannot.

**Alternatives considered**: Exhaustive counts/rankings or unlimited windows are unnecessary; permission/index changes invalidate instead of silently reranking.

## One Relay search connection

**Decision**: `search(...): SearchItemConnection!` returns edges whose node is `SearchItem = Product | Category | ProductVariant`. One PageInfo/cursor/order covers the selected kind set. Optional positive kind scalar/array narrows registered default kinds; a complete combined plan is required. No per-kind search connection types.

**Rationale**: This follows the canonical node -> edge -> connection convention, verified in shared/schemas/admin/product.graphqls (Product/ProductEdge/ProductConnection), category.graphqls (Category/CategoryEdge/CategoryConnection) and user.graphqls (Actor/ActorEdge/ActorConnection). Thus the SearchItem union owns SearchItemEdge and SearchItemConnection. The union belongs to resource nodes, preserving canonical resource envelopes and standard edge pagination. Shared provider capabilities determine whether a multi-kind query has a coherent order/window; heterogeneous candidates bind canonical kind plus UID and sort ties.

**Alternatives considered**: Union-of-connections and a list of independently paged connections were explicitly rejected by the user. [Typesense multi-search](https://typesense.org/docs/30.2/api/federated-multi-search.html) and [Meilisearch multi-search](https://www.meilisearch.com/docs/reference/api/multi-search/perform-a-multi-search) distinguish independent and merged results; an adapter must prove merged semantics, not concatenate pages. No separate provider contract or federation service is introduced.

## Scylla eligibility

**Decision**: Advertise tested complete plans, not OSS/Cloud/beta labels. Existing structured indexes remain baseline. Do not advertise native FTS until namespace/filter/paging constraints meet the contract.

**Rationale**: [Current FTS constraints](https://docs.scylladb.com/manual/stable/features/fulltext-search.html#query-constraints) pair BM25 filtering/ordering with LIMIT <=1,000 and exclude additional predicates/paging. [Deployment matrix](https://cloud.docs.scylladb.com/stable/vector-search/vector-search-clusters.html#feature-compatibility-matrix) ties availability to versions/index infrastructure. [LIKE/filtering](https://docs.scylladb.com/manual/stable/cql/dml/select.html) is not proof of indexed search. Global top-K followed by namespace filtering is not complete namespace retrieval.

**Alternatives considered**: Inferring hybrid/Relay capability from ANN or LIKE over-advertises support.

## Discovery and errors

**Decision**: Extend spec 072's existing search entries additively; retain `{kind,supportedTerms}`, add operation/combination/limit descriptors and increment discovery minor schema version. No second endpoint. Use `admission.Error.ToGQLError` unchanged.

**Rationale/source**: Spec 072 currently proposes only `{kind,supportedTerms}`; richer fields are new planned work. `admission/error.go` owns code/diagnostics and intentionally omits diagnostics for FORBIDDEN.

**Alternatives considered**: Independent public maps drift; ad hoc query error extensions duplicate the shared contract.

## Cutover and lifecycle

**Decision**: One coordinated schema cutover, drained incompatible recipients, major contract-change record and explicit rollback ordering. No old-input translation. Internal foundations can land without exposing parallel selection arguments.

**Rationale**: User overrides #471's deprecation proposal; replica safety and preserved controller pending work still apply. ADR 0017 remains Accepted; Proposed 0007/0016/0019 retain status. Implementing close-out audits actual core decisions before any status flip/spec closure.

**Alternatives considered**: Old binaries cannot enforce a new capability floor; aliases contradict the chosen contract.

## API ownership and sequencing review

**Decision**: Shared query/planner/provider core, endpoint-qualified exposure. Admin owns management query/watch and owning configuration/diagnostics; recommendation serving belongs in Storefront. Keep the current SDL overlay Admin-specific and the overall cross-API plan provisional until reviewed against the first substantive Storefront contract.

**Rationale/source**: `shared/schemas/storefront/schema.graphqls` is boundary-only with node and ID-only StorefrontProduct; `gitstore-api/internal/storefront/handler.go` reports unavailable. Neither defines published catalog search or recommendation context. Published lookup/visibility and target/revision binding must come from that owner. Shared execution cannot import Admin schemas or hydrate Storefront hits from current drafts.

**Alternatives considered**: An Admin-only feature would place customer recommendation serving incorrectly. Inventing Storefront types, contexts and publication semantics here would create a competing contract. Waiting for that baseline is recommended sequencing; no release date is asserted.
