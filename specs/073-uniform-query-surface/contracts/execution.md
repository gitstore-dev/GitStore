# Execution, Security and Delivery Contract (Proposed)

See [API surface ownership](api-surfaces.md). The Admin overlay is one consumer of this shared design. Recommendation serving is Storefront-only and remains reserved pending its first substantive contract.

## Request pipeline and provider interface

1. Authenticate using existing endpoint/provider chain; resolve the owning API surface and explicit namespace, inherited parent or trusted publication/target boundary. Storefront authorization/representation must come from its published-read owner, never from an Admin resolver. Reject invalid pagination before backend work.
2. Parse bounded language v1; resolve registry types/operators. Authorize sensitive terms, seeds and owning recommendation context. Fail closed inside OR/NOT; no partial supported subset execution.
3. Select an eligible **whole-plan** registration for kind/operation/predicate combination/retrieval/order/scope/pagination. Inject server-owned namespace/visibility policy outside client negation. No eligible plan -> explicit error. A label posting cannot be treated as an arbitrary Boolean index.
4. Validate index readiness, revision and cursor binding; execute finite backend/provider work. Never pass raw query/CQL/JSON from a client to a backend.
5. Hydrate candidate IDs using the owning API's authoritative lookup contracts (management objects for Admin, eligible published representations for Storefront) with <=16 workers/request and <=1,000 point hydrations, separately budgeted from <=64 index subqueries. Recheck current access and scope before returning any node, edge cursor or result-derived metadata. Deleted, scope-changed or now-unauthorized window members invalidate continuation; no silent rank shifting or leakage of which object caused it.

New API-internal `query` package defines backend-neutral value contracts:

| Operation | Input | Output / obligations |
| --- | --- | --- |
| Describe | configured registration/index revision | Safe typed capabilities; no secrets, inferred features or arbitrary config serialization. |
| Compile | typed AST + API surface + authorized scope/publication/context + page/order constraints | Executable complete plan or structured rejection; no provider I/O for parsing alone. |
| Execute | compiled plan + bounded page/window request + deadline | Ordered canonical kind/UID candidates, revision and explicit continuation/window bound. No provider-owned resource envelopes. |
| Health/Revision | registration identity | Ready/index revision and safe availability; stale revision invalidates plans/windows. |

The **same** provider descriptor/compiler/executor handles typed advanced queries and generic search. Qdrant is the first real adapter; Meilisearch, Elasticsearch and Typesense are compatible future targets. None is a generic-search-only service. Native indexed datastore plans and the journal predicate compiler register alongside them, without pretending a search engine is required for baseline queries.

## One heterogeneous Relay search connection

`search` returns exactly one SearchItemConnection with SearchItemEdge.cursor and SearchItemEdge.node: SearchItem, a union of canonical resource objects. No per-kind Search*Connection types or union of connections. Select concrete resource fields through inline fragments on `node`. The existing PageInfo shape is reused.

A positive top-level `kind` scalar/array narrows the registered default set (the Admin overlay maps Product, CategoryTaxonomy and ProductVariant to Product/Category/ProductVariant GraphQL objects; Storefront defines its own published members). Validate the complete selected kind set, required namespace restriction, field semantics and common retrieval/order before execution. Providers must prove a common index/model/scoring policy or an explicit compatible merge algorithm; never compare unrelated scores or concatenate independent per-kind pages. Reject unsupported cross-kind combinations. First Qdrant integration uses a shared compatible vector/index space with kind/namespace payload indexes; other index layouts register only proven subsets. This is the same advanced-query capability registration, not separate infrastructure.

One page bound/window/cursor covers the entire result, not a count per kind. Bind the canonical kind set in the query/cursor/window; deduplicate by kind plus UID. Break equal requested sort/relevance values by canonical kind then UID, consistently across forward/backward traversal. Hydrate through each kind's authoritative lookup/authorization owner; no copied resource envelopes. Kind set/index revision changes invalidate continuation.

## Indexed baseline and known gaps

| Surface | Eligible implementation / gap |
| --- | --- |
| Namespace-scoped ordinary lists | Existing keyset/index paths, guarded by request budgets and exact scope. |
| Category descendants | Existing closure index; preserve includeSelf/maxDepth and relative-depth/name order. Extra terms only with a supported combined plan. |
| Category.products | Existing category-product index, retaining parent UID boundary. |
| Product.productVariants | Add backend-neutral paged parent-reference projection method; existing full slice is not eligible. |
| Collection.products | Intersect owning selector with client predicate through eligible provider/index. Empty owning selector gives no members. Existing full namespace label helper cannot serve this path. Without a compatible provider/owning membership projection, FAILED_PRECONDITION/QUERY_INDEX_NOT_READY, not a scan. This limitation must be visible in capability/release documentation. |
| Label equality/exists | Add namespace/kind/key/value and key-existence query-first postings with bounded shards and canonical sort keys; same-key IN union <=8 values, stable deduplication. |
| Label NOT/absence/arbitrary intersections | Valid watch syntax does not establish indexed list support. Reject unless an adapter registers a complete eligible plan. No iterative namespace scan. |
| Cluster RBAC/actors/accounts | Add query argument and missing last/before support; provider/directory adapters must prove bounded complete pagination. Provider inability is explicit, not in-memory whole-directory filtering. |

Label/provider projections consume durable journal events with a durable kind/namespace/name identity ledger, deletion tombstones and durable checkpoints; no new process-local watch stream or public event changes. Namespaced journal deletions lack UID/version. Reconcile every replay identity through an authoritative point lookup: use current resource UID/version if present, otherwise delete the ledger's prior UID/posting keys. On recreation remove prior UID keys before upserting current identity. Lookup I/O errors never mean absence. This also prevents pre-bootstrap events from reverting a newer initial-list image. Reuse lease/CAS patterns for a named fenced projection owner, separate from journal-source ownership; serialize identity work and make external writes repeatable. Persist projection/ledger completion before source checkpoint; test crash boundaries and stale-owner writes. Expired replay forces rebuild/unready status. First build: bookmark-before-list, bounded pages plus replay, audit retained count/version coverage before ready. Non-journal kinds require a proven owning feed before registration. No user-request-triggered full rebuild. Full Collection membership/status work stays with its owning feature/ADR.

## Pagination

Default first=50 if neither direction supplied. Explicit counts must be 1–100. Reject first+last, after+before, after+last and before+first, including zero/negative counts. `after` alone implies first=50; `before` alone implies last=50. Backward traversal returns the same logical order as forward traversal. Registry supplies stable default order; append UID tie-breaker when needed. Category subtree retains depth/name/UID semantics.

Authenticate a versioned cursor envelope with a dedicated shared key derived/resolved through existing secret-material conventions; do not reuse authentication signing credentials or publish the key. Key ID supports rotation; keep verification keys for <=10-minute cursor lifetime, then reject. Bind language/registry version, normalized query, kind, namespace/parent UID, API surface, trusted publication target/revision where applicable, principal/auth-context fingerprint, index/model/scoring revision and backend position or window ordinal. Authorization is still reevaluated, including when no provider policy-revision token exists. Any mismatch fails before backend continuation. Reject legacy unbound keyset/closure cursors at cutover. Journal resourceVersion encoding is unchanged.

Authoritative keyset lists promise stable ordering, not a snapshot under concurrent writes. Ranked requests capture one complete bounded window (<=1,000 candidates), absolute TTL 10 minutes. For >1,000 total matches, the contract is traversal of the declared top-1,000 window only; never claim exhaustive totalCount. Store ordered IDs/captured authoritative versions/tie values in shared Scylla, atomically mark ready and allocate shared quota slots first. Current version mismatch invalidates the window; do not silently mix old membership with changed content. Do not serialize private query/context into cursor or window records. Context needed beyond initial execution is represented by a digest/owning reference; reranking requires a new request/window. If complete candidate capture cannot fit deadline/budgets, fail explicitly. Native provider snapshots may substitute only with equivalent two-replica/replacement/backward tests and finite lifetime.

## Journal watches and controller recovery

Subscription arguments retain kind (generic), existing namespace semantics and resourceVersion, replacing selector with query. Namespace omission requires existing fleet-wide authorization. Unsupported kinds remain unsupported. Compile only current/previous labels and immutable identity available in existing journal records. Do not infer arbitrary mutable predicates from postimage alone.

Reuse projector transitions: false->true ADDED; true->true MODIFIED; true->false payload-free DELETED; false->false omitted. Preserve original deletion payload rules, ordering, bookmarks, expiry, replay bounds, backpressure and event/resource cursor distinction. BOOKMARK bypasses query filtering. No pipeline directives or provider calls, including during provider outage/model refresh.

Reuse `bootstrapCursor` and existing per-kind sentinels: capture journal BOOKMARK **before** listing, persist/use that horizon, page indexed initialization, then replay. Do not read high water only after listing. Initial lists cannot use an eventually indexed ranked provider as authoritative controller initialization. Reject a configured controller predicate without eligible list/watch intersection. Tests must cover index-lag and read-consistency races; if existing list/index visibility cannot meet initialization invariants, keep that combination unavailable until fixed.

Checkpoint identity binds query/language/scope/parent; changing it forces controlled cold relist while preserving admitted pending work/retries/quarantine. Disk-v2 upgrade rules and no-overlapping-writers stay unchanged. No new connection watermark or second watch cursor. Existing watch error codes/reasons remain owning contracts rather than being converted to query cursor errors.

## Errors and observability

Use `admission.Error.ToGQLError`: extensions contains code and diagnostics (reason/message/level, optional field/file); no new query-specific extension object. BAD_USER_INPUT covers malformed/unsupported/unbounded plans and bad cursors; FAILED_PRECONDITION covers unavailable provider/index/window/revision. FORBIDDEN remains sanitized without diagnostics. Throttling uses the existing transport's overload behavior; do not mislabel throttling as empty results. See grammar for exact query reason names.

Record bounded metrics for phase/outcome, registered kind/operation/mode, provider ID from bounded config, index lag, window count/expiry, candidate/subquery budgets and watch replay/backpressure. No raw queries, term values, caller IDs, window IDs, recommendation context or arbitrary labels in telemetry. Health describes the affected capability; provider outage does not disable journal readiness.

## Cutover and rollback

After the first substantive Storefront contract establishes its public types/visibility/context, finalize its endpoint-specific schema/fixtures. Inventory and update all first-party documents/generated clients, controller GraphQL strings, capacity fixtures, examples and independently released CLI/Admin consumers before activation. Keep CollectionSpec.selector and other resource configuration untouched; remove input definitions only after proving no remaining owner.

Internal preparatory releases may leave the old public schema until cutover, but the replacement binary exposes only the new schema. Provision forward storage migrations/indexes first, validate readiness, pause/drain incompatible clients and API recipients, deploy new APIs, then activate new clients/controllers. Two new API/controller replicas must pass recovery; do not imply old binaries enforce a new floor. Preserve Git storage/process and controller queues. Add the major compatibility change under repository release policy; no manual tag/publication in this planning task.

Rollback deactivates replacement clients, drains new recipients, invalidates new cursors/windows and restores a matched old API/client set. Retain forward tables and checkpoints; no destructive down migration. Reinitialization is explicit; replay expiry never silently resets. Pending admitted controller work cannot be thrown away to make rollback appear successful.
