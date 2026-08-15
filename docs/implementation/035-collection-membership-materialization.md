# Collection Membership Materialization for Paginated Product Access

**Status**: 🟡 Proposed

## 1. Problem statement

`Collection.products` is a Relay connection over a many-to-many, label-selector-defined relationship between `Collection` and `Product`. Today it is resolved by loading the *entire* matching set into memory on every request and then windowing it in application code (`gitstore-api/internal/graph/resolver/collection.resolvers.go:20-55`), rather than pushing the cursor/limit down to the datastore the way `Category.products` and `Namespace.repositories` already do (`category.resolvers.go:22-34`, `repository.resolvers.go:151-165`). This is the last major Relay connection in the catalog surface that still does "materialize the full match set, then paginate" instead of "paginate the datastore directly," and it is also the read path that authorization (spec 022) explicitly forbids implementing naively — 022 requires the *query itself* to be scope-specific, not a page-then-filter operation.

This document designs a replacement access pattern: how `Collection.products` should be stored, queried, paginated, and eventually authorized, given (a) the existing keyset-cursor pagination conventions used everywhere else in the catalog, (b) the ScyllaDB/memdb dual-backend datastore, (c) the absence of any Collection or Product reconciler in `gitstore-controller-manager` today, and (d) the not-yet-implemented but architecturally binding constraints of spec 022 (OPA data authorization).

## 2. Existing implementation and evidence

### 2.1 Read path: materialize-then-paginate

`collectionResolver.Products` (`gitstore-api/internal/graph/resolver/collection.resolvers.go:20-55`) unmarshals `Collection.Spec.Selector` and calls `r.service.ListProductsByLabelSelector(ctx, c.Namespace, selector)` (`service.go:184-186`). Both backends materialize the *entire* matching set before any windowing happens:

- memdb (`gitstore-api/internal/datastore/memdb/backend.go:592-607`): a raw index scan over every product row in the namespace, appending label matches into one slice, **with no sort applied**.
- Scylla (`gitstore-api/internal/datastore/scylla/backend.go:793-816`): pages through `ListProducts` in 500-row batches, evaluating `catalog.MatchesLabels` client-side and appending into one `matched` slice until exhaustion.

`BuildProductConnectionFromSlice` (`gitstore-api/internal/graph/resolver/pagination.go:49-108`) then does the Relay windowing over that already-fully-loaded slice by linear-scanning for the cursor position. Its own doc comment (`pagination.go:48`) states "Products must be pre-sorted by (CreationTimestamp DESC, UID DESC)" — an invariant the memdb caller never actually establishes, since `ListProductsByLabelSelector` there does not sort (finding confirmed directly against `memdb/backend.go:592-607`).

By contrast, `Category.products` (`category.resolvers.go:22-34`) and `Namespace.repositories` (`repository.resolvers.go:151-165`) already push pagination to the datastore via `PageResult[T]`-returning methods (`GetProducts`, `ListRepositoriesByNamespace`). Collection is the outlier.

### 2.2 Cursor mechanics (repo-wide convention, to be reused unchanged)

`keyset_cursor.go`: `EncodeKeysetCursor(createdAt, id)` (lines 23-27) builds `"keyset|<RFC3339Nano>|<id>"`, base64-standard-encoded; `DecodeKeysetCursor` (lines 31-51) reverses this and validates the `"keyset"` tag. The cursor encodes `(CreationTimestamp, UID/ID)`, not an offset. Ordering convention repo-wide is **`CreationTimestamp DESC, UID/ID DESC`** (`memdb/backend.go:18-33` `paginateSlice`, `memdb/backend.go:114-127` `compareKeyset`; ScyllaDB clustering columns in `001_initial_schema.cql`).

### 2.3 Selector shape and evaluation

`catalog.LabelSelector` (`gitstore-api/internal/catalog/collection.go:16-36`) is a full Kubernetes-style selector: `MatchLabels map[string]string` plus `MatchExpressions []LabelSelectorRequirement{Key, Operator ∈ {In,NotIn,Exists,DoesNotExist}, Values}`, AND'd together; an empty/absent selector matches nothing. `catalog.MatchesLabels` (`gitstore-api/internal/catalog/selector.go:8-46`) is the single, unit-tested (`selector_test.go:16-107`) evaluator used by both backends. Product `Labels` is a flat `map[string]string` (`catalog/product.go:25-30`, Scylla column `labels map<text,text>`), scoped by `namespace` in the query, not by key-prefixing.

### 2.4 ScyllaDB schema conventions

`products_by_namespace`, `collection`, and `category_taxonomy` all share `PRIMARY KEY ((namespace), creation_timestamp, uid)` with `CLUSTERING ORDER BY (creation_timestamp DESC, uid DESC)` (`001_initial_schema.cql` lines 1, 39, 112), plus `_by_name`/`_by_uid` lookup tables. No table has a count/aggregate column; no materialized view exists anywhere in the migrations. No secondary index exists for Product/Collection/CategoryTaxonomy (`002_add_initial_indices.cql:1-7` covers only namespaces/repositories/namespace_mappings). All list queries use pure keyset pagination — no `PageState`/`PagingState` usage anywhere in `gitstore-api/internal/` — via `buildPaginatedSelect`'s tuple-inequality WHERE clauses (`scylla/pagination.go:72-121`).

### 2.5 Existing status/resolved precedent

`Collection.status.resolved: ResolvedCollectionDefinition { memberCount: Int! }` is documented as "a cached hint; collection.products is authoritative" (`shared/schemas/collection.graphqls:230,234,251-256`). `CategoryTaxonomy.status.resolved` is the controller-computed analog, with the same nullable-until-first-reconcile semantics (`categorytaxonomy/reconciler.go:182-190`). Both `CollectionStatus` and `CategoryTaxonomyStatus` carry the same `observedGeneration`/`conditions`/`resolved` shape (`shared/schemas/collection.graphqls:215-235`, `shared/schemas/category.graphqls:188-224`) — this machinery already exists on `Collection` and is available for staleness signaling even though nothing currently populates it.

### 2.6 Existence-check convention (spec 041)

`HasRepositories(namespaceID) (bool, error)` and `HasCatalogResources(repoID) (bool, error)` (`datastore.go:187,200`) are explicitly documented as "must be an existence check (LIMIT 1 / equivalent), not a full count." This is the pattern this design reuses for migration-fallback detection (§13), and — per adversarial review — the pattern's limits (it cannot express "fully populated" vs. "partially populated") are a real constraint on how far it can be stretched (§13, §19).

## 3. Relevant authorization constraints from docs/implementation/022-*

022 (`Status: 🟡 Proposed (architecture only)`) is not implemented — the only live `AuthZProvider` is `rbac-local` (`gitstore-api/internal/auth/provider/rbaclocal/provider.go:52-92`), a pure action-string check that explicitly discards the resource argument and has no per-item data scoping. Today, `Query.products`, `Query.product`, `Category.products`, and `Collection.products` all call the datastore directly with **zero** authorization check and no per-item filtering (`product.resolvers.go:21-52`, `category.resolvers.go:22-35`, `collection.resolvers.go:20-49`) — matching 022 §3's own description of the gap verbatim.

022's binding requirements for any future connection design:

1. **No post-hoc filtering, ever.** "For Relay connections, authorization changes the physical query plan before pagination... GitStore never fetches a page and removes unauthorized nodes afterward, and it never relies on CQL `ALLOW FILTERING` for authorization" (022 lines 22-26). §12.2 restates this as a prohibition: fetching a page and deleting unpublished edges afterward "creates short pages, misleading `hasNextPage`, inconsistent counts, and data-dependent over-fetching" (022 lines 625-632).
2. **Scope-specific projections, not per-item authz calls.** Filtering is by visibility *class* (PUBLIC vs MANAGEMENT), selected as a whole pre-materialized projection before pagination (022 §7.1-7.2, §12.1 lines 592-605) — not a residual per-row "can this user see item N" filter.
3. **Ordered pipeline** (022 §12.2 lines 613-618): select the scope-specific projection → apply its cursor → read `limit+1` rows → build edges/pageInfo → compute `totalCount` from the same scope-specific dataset or a matching maintained counter.
4. **No enumeration via any channel.** §13 (lines 634-647): direct lookup of an unpublished resource returns `NOT_FOUND`, identical to absent; list results show ineligible resources as simply absent, no per-edge errors; "the API must not reveal whether a Product or ProductVariant exists but is outside the caller's view through messages, timing classes, **counts**, or global-node behavior" (emphasis load-bearing for §12 of this document). Cursors must bind visibility so a cursor can't be replayed under a different scope to probe existence (§12.2 lines 621-623).

There is no existing "visible namespace/authorized-ID set" precomputation to build on (`AuthorizedNamespaceForDeletion`, `middleware/security/graphql.go:39-42`, is a single-resource cache for one mutation flow, not reusable for a WHERE-clause push-down).

## 4. Control-plane and asynchronous reconciliation model

`gitstore-controller-manager` has exactly one reconciled kind today: `CategoryTaxonomy`, wired via `internal/categorytaxonomy/reconciler.go:87-180` and registered in `cmd/controller/main.go:130-139`. Its `Reconcile` computes `ResolvedCategoryTaxonomy{Depth, Path, ChildCount, ProductCount}`, marshals it into `status.StatusPatch.Resolved json.RawMessage` (`status/patch.go:41-54`), skips no-op writes (`patch.IsNoOp`, lines 59-80), and calls `StatusClient.Apply` treating conflicts as retryable (lines 171-173).

Spec 042 added a **cache without a reconciler** for Product: `internal/categorytaxonomy/products.go:44-116` defines a Product cache entity and an `EventHandler[Product]` whose only effect is to re-enqueue **CategoryTaxonomy** work items — never a Product work item. `cmd/controller/main.go:170-212`'s `registerProductWatch` deliberately never calls `mgr.Register(manager.ReconcilerRegistration{Kind: "Product", ...})` (contrast with `registerCategoryTaxonomy` at line 130-139, which does). The comment at lines 191-196 states outright that "Product has no registered Reconciler/work queue of its own."

There is **no Collection reconciler, no `internal/collection` package, and no precedent anywhere in this codebase for a controller-maintained many-to-many derived index** (the only cross-kind linkage, CategoryTaxonomy↔Product, is one-to-many and computed by client-side pagination-and-filter, not a stored index). The eventbus itself is kind-agnostic (`eventbus.go` keys everything by an arbitrary string `Kind`, no enum) — `Kind: "Product"` already flows end-to-end (spec 042 shipped it) and `Kind: "Collection"` would work with zero eventbus/schema changes if a future controller needed it — but no controller consumes it today.

This matters directly for the design decision in §8: any candidate requiring a new controller is asking this codebase to cross a line it has so far deliberately avoided (spec 042 added the cache half of the CategoryTaxonomy pattern for Product and explicitly stopped short of the reconciler half).

## 5. Access-pattern inventory

| Access pattern | Current implementation | Cursor/order |
|---|---|---|
| `Collection.products` (forward/backward page) | Full namespace scan + in-memory selector match + linear-scan windowing (`collection.resolvers.go:20-55`) | `(CreationTimestamp DESC, UID DESC)`, unenforced on memdb |
| `Collection.status.resolved.memberCount` | Not populated anywhere today; doc-commented as a "cached hint" | N/A |
| `Category.products` | Datastore-paginated `GetProducts` (`category.resolvers.go:22-34`) | Same convention, correctly pushed down |
| `Namespace.repositories` | Datastore-paginated `ListRepositoriesByNamespace` (`repository.resolvers.go:151-165`) | Same convention |
| Reverse lookup: "which collections contain product X" | Does not exist | — |
| Existence check: "has this collection been indexed yet" | Does not exist (needed for migration, §13) | — |

## 6. Candidate designs

Four candidates were evaluated; the full decision matrix is in §7.

**A — Materialized `collection_membership` table, populated synchronously in the admission path.** A `(namespace, collection_uid)`-partitioned forward table plus a `(namespace, product_uid)`-partitioned reverse table, both clustered `(creation_timestamp DESC, uid DESC)` exactly like `products_by_namespace`. Populated inline inside `CreateProduct`/`UpdateProduct`/`DeleteProduct` (diff old vs. new label match against all Collections in the namespace via the reverse table) and `CreateCollection`/`UpdateCollection` (full re-scan and partition replace). Strengths: one query per page at read time, zero controller involvement, reuses every existing cursor/ordering convention, bounded staleness window (one write). Weaknesses: unbounded single-partition growth for a broad-selector collection with no mitigation designed; cross-table (forward/reverse) writes are not atomic, so a crash mid-write can desync them; needs a second, visibility-scoped table to satisfy 022 once it ships; a full-partition-replace on Collection selector edit is not atomic at Scylla batch-size scale.

**B — Collection-membership projection derived asynchronously by future Product and Collection controllers.** Follows the CategoryTaxonomy reconciler pattern exactly, but requires **two new reconcilers that do not exist today**, one of which (Product) directly contradicts the deliberate no-reconciler decision spec 042 just shipped (§4). It also escalates `status.resolved` from a documented "cached hint" to a load-bearing pagination source (or else creates a visible count/list disagreement if it doesn't), has no rebuild/drift-detection mechanism and none to borrow from elsewhere in the repo, doesn't satisfy 022 without duplicating every table per visibility scope, and has the same unmitigated wide-partition risk as A. Its pre-controller behavior is worse than A's: an empty projection is indistinguishable from an unmaterialized one, requiring yet another marker field to disambiguate.

**C — Inverted label-index tables queried at read time.** Per-`(namespace, label_key, label_value)` and per-`(namespace, label_key)` tables enabling equality/`In`/`Exists` lookups without a full scan. Rejected primarily because `NotIn`/`DoesNotExist`-only selectors (permitted by `catalog.LabelSelectorRequirement.Operator` today, `catalog/collection.go:26-36`) have no anchor set and degrade to exactly today's full scan — the design's core promise ("avoid full scans") doesn't hold for a selector shape the schema explicitly allows. Multi-term intersection still requires materializing the full candidate set before Relay windowing — the same anti-pattern being fixed, just over a smaller candidate universe. Write amplification is up to 2K writes per K-labeled product with no controller to host the diffing, and popular label values create the same wide-partition risk as A/B, worse because it fragments by value while still concentrating popular ones.

**D — Hybrid: materialized membership plus authorization filtering at read time (over-read-and-fill).** Keeps one shared (non-visibility-scoped) `collection_membership` table and defers authorization to a bounded-rounds over-read-and-fill loop at query time, explicitly documented as unable to fully honor 022's "never short pages" guarantee (a page can hit `maxRounds` and return short-but-not-exhausted, itself a soft leak signal). Requires a Collection controller that does not exist (same gap as B for the write side), cannot ship before that controller exists (no dual-source migration story is given), and has the highest write cost of all four candidates combined with a deliberately weaker authorization guarantee than A or B's "true" scope-specific-table approach.

See §7 for the full side-by-side comparison; the summary judgment is that **B and D require new controllers this codebase has deliberately avoided building, and C fails to eliminate the scan it exists to eliminate for a selector shape the schema already permits** — leaving A as the only candidate that is both a genuine improvement over the status quo and buildable without new control-plane scope.

## 7. Decision matrix

| Criterion | A: Materialized (write-time) | B: Controller-derived (async) | C: Inverted label index | D: Hybrid materialized + runtime authz |
|---|---|---|---|---|
| Query count / read amplification | Good — 1 query/page | Good — 1 query/page (once populated) | Fair — N+ queries, still materializes full intersection for multi-term/negative selectors | Fair — 1+ queries/round, multiple rounds under low authz density |
| Partition/hotspot risk | Poor — unbounded partition per broad collection, no mitigation | Poor — same, `collection_uid` partition | Poor — value-partitioned, popular label values hot | Poor — same `collection_uid` partition, explicitly flagged unmitigated |
| Pagination correctness | Good — standard keyset, no offset (see §11 for a bulk-rewrite caveat) | Good — keyset, but membership-not-current | Fair — intersection breaks true cursor semantics for multi-term | Good for membership; short-page risk under authz filtering |
| Cursor stability | Good — reuses `EncodeKeysetCursor`/`DecodeKeysetCursor` unchanged | Good — same | Good | Good |
| Write amplification | Fair — bounded by collections/products per namespace | Fair — similar, but async so latency hidden from writer | Poor — 2K writes per product, diff-on-write required | Poor — same as A plus authz has no write cost but membership same |
| Staleness behavior | Good — synchronous, single-request window (but see §13 for a backfill-window caveat) | Poor — queue/backoff-dependent, unbounded in principle | Fair — synchronous but no controller to host diffing, so inline too | Poor — controller-reconcile-dependent |
| Rebuild/backfill complexity | Fair — derivable, needs one-shot job, no drift detection built in | Poor — no rebuild precedent, no drift detection | Fair — full scan re-derivation, same gap | Fair — `resource_version` per row aids diffing, but still needs new job |
| Authorization correctness/leak risk | Fair — needs a second visibility-scoped table to satisfy 022; see §12 for gaps in even that plan | Poor — violates 022's "no post-hoc filter" outright as designed | Poor — same duplication problem, worse (index itself leaks) | Fair — explicitly documents it deviates from 022's "never short pages" guarantee |
| Namespace isolation | Good — partition includes namespace | Fair — keyed by UID only, no partition-level guard | Good — partition includes namespace | Fair — UID-partitioned, namespace is a defense-in-depth column only |
| memdb compatibility | Good — straightforward map mirror | Good | Fair — doubles index maintenance code paths | Good |
| Controller-manager fit | Good — zero controller involvement | Poor — needs 2 new reconcilers, contradicts spec 042's Product-has-no-reconciler precedent | Good — zero controller involvement | Poor — needs a new Collection controller |
| Behavior before any controller/backfill exists | Fair — universal fallback holds on deploy day, but the backfill *transition itself* has a silent under-count window (§13) | Poor — table is empty/absent, ambiguous empty-vs-unmaterialized | Good — doesn't depend on one | Poor — cannot ship without the missing controller |
| Migration complexity | Fair — 2 tables, admission hook, backfill | Poor — 2 tables, 2 controllers, dual-path fallback, visibility duplication | Poor — 2 tables, mutation-path diffing, backfill, visibility duplication | Poor — most moving parts of all four |

## 8. Recommended architecture

**Adopt Candidate A — a `collection_membership` table pair, populated synchronously in `gitstore-api`'s existing admission/mutation path — as the permanent design, not an interim stopgap.**

B and D are rejected outright: both require standing up controllers this codebase deliberately does not have. Spec 042 is direct precedent — it added a Product *cache* but explicitly declined to register a Product *reconciler* (`cmd/controller/main.go:191-196`), and a Collection controller has even less precedent (no `internal/collection` package exists at all, no many-to-many derived index has ever been built here). C is rejected because it does not eliminate full-namespace scans for negative-only selectors, which the schema explicitly permits, and its write cost (2K writes per product) with no controller to host the diff is strictly worse than A under the same "no controller" constraint.

A wins because it needs zero new controller-manager work, reuses every existing convention (`(CreationTimestamp DESC, UID DESC)` clustering, `EncodeKeysetCursor`, `PageResult[T]`), and turns an O(namespace) scan-per-request into O(1) partition reads at the one-time cost of write-path hooks in code that already computes derived fields (spec 034's admission functions).

This recommendation is adopted **with amendments** relative to the original Candidate A writeup, required by the three adversarial verification passes:

- **§11** amends the bulk `ReplaceCollectionMembership` contract to fix a real cursor-instability bug found in verification.
- **§12** amends `memberCount` and the migration-fallback path to close a real information-leak the auth-leak verifier constructed concretely.
- **§13** amends the backfill/bootstrap story to name and bound (rather than silently absorb) the under-count window the staleness verifier found.

None of these amendments changes the overall recommendation; they close gaps in how it must be built.

## 9. ScyllaDB data model

```cql
CREATE TABLE IF NOT EXISTS collection_membership_by_collection (
    namespace           text,
    collection_uid       uuid,
    product_created_at   timestamp,   -- the PRODUCT's own immutable CreationTimestamp, NOT a rewrite/index timestamp
    product_uid           uuid,
    product_name          text,
    resource_version       text,       -- product's resource_version at last evaluation, for staleness detection
    PRIMARY KEY ((namespace, collection_uid), product_created_at, product_uid)
) WITH CLUSTERING ORDER BY (product_created_at DESC, product_uid DESC);

CREATE TABLE IF NOT EXISTS collection_membership_by_product (
    namespace       text,
    product_uid     uuid,
    collection_uid   uuid,
    PRIMARY KEY ((namespace, product_uid), collection_uid)
);
```

Design notes:

- Partition key includes `namespace` on both tables (unlike the original candidate writeups' UID-only reverse-index shape), giving the same structural namespace isolation every other table in `001_initial_schema.cql` has, and giving `HasCollectionMembership` and any future public-scope variant a namespace-scoped partition to query.
- `product_created_at` **must** be the product's own `CreationTimestamp`, never a write-time/rewrite timestamp — this is the fix for the pagination bug in §11. It is denormalized from the product row at every write (initial insert, incremental upsert, and bulk rewrite alike) rather than being generated fresh.
- `resource_version` on the membership row is the product's `resource_version` at the time this membership row was last written — the basis for backfill-completeness / drift detection, not a version of the membership row itself.
- A visibility-scoped twin table, `collection_membership_public_by_collection` (same shape, populated only for publicly visible products), is a **named, reserved-but-not-yet-built** part of this schema — see §12 for why it must exist before 022 ships, not after.

## 10. Example CQL queries

Forward page (`first`/`after`):
```cql
SELECT product_uid, product_name, product_created_at, resource_version
FROM collection_membership_by_collection
WHERE namespace = ? AND collection_uid = ?
  AND (product_created_at, product_uid) < (?, ?)
ORDER BY product_created_at DESC, product_uid DESC
LIMIT 21;  -- first + 1, to compute hasNextPage
```

Backward page (`last`/`before`), reversing the inequality and re-reversing the result in application code, per `buildPaginatedSelect`'s existing convention (`scylla/pagination.go:92-104`):
```cql
SELECT product_uid, product_name, product_created_at, resource_version
FROM collection_membership_by_collection
WHERE namespace = ? AND collection_uid = ?
  AND (product_created_at, product_uid) > (?, ?)
ORDER BY product_created_at ASC, product_uid ASC
LIMIT 21;
```

Existence check (migration-fallback signal, mirrors `HasRepositories`/`HasCatalogResources`):
```cql
SELECT product_uid FROM collection_membership_by_collection
WHERE namespace = ? AND collection_uid = ?
LIMIT 1;
```

Reverse lookup (used by the Product-triggered incremental writer to find current memberships before diffing):
```cql
SELECT collection_uid FROM collection_membership_by_product
WHERE namespace = ? AND product_uid = ?;
```

Incremental write (single product, single collection, on label-driven match-state flip):
```cql
INSERT INTO collection_membership_by_collection
  (namespace, collection_uid, product_created_at, product_uid, product_name, resource_version)
VALUES (?, ?, ?, ?, ?, ?);

INSERT INTO collection_membership_by_product (namespace, product_uid, collection_uid)
VALUES (?, ?, ?);
```
(and the corresponding `DELETE ... WHERE namespace = ? AND collection_uid = ? AND product_created_at = ? AND product_uid = ?` / reverse-table delete for products that no longer match.)

Bulk rewrite (Collection selector change) is deliberately **not** expressed as a single statement — see §11 for why a naive `DELETE partition` + `INSERT ...` loop is unsafe, and for the diff-based rewrite this design requires instead.

## 11. Pagination and cursor semantics

Cursor format, ordering, and windowing are unchanged from every other Relay connection in this codebase: `EncodeKeysetCursor(product_created_at, product_uid)` / `DecodeKeysetCursor` (`keyset_cursor.go:23-51`), `(CreationTimestamp DESC, UID DESC)` ordering, `limit+1` read to compute `hasNextPage`, fed into the existing generic `PageResult[T]`-based connection builder (the one `Category`/`Namespace` already use) rather than `BuildProductConnectionFromSlice`, which is retired for this path.

### 11.1 The bulk-rewrite cursor-instability bug (confirmed by adversarial review) and its fix

The original Candidate A contract specified `ReplaceCollectionMembership(ctx, namespace, collectionUID, productUIDs []uuid.UUID) error` for the "Collection selector changed" trigger, described only as a "full rewrite." Adversarial verification constructed a concrete failure: because this signature carries **no timestamps**, an implementation has no way to populate the clustering column `product_created_at` for surviving members except either (a) an uncosted extra per-product lookup of the real `CreationTimestamp` before every rewrite (not costed anywhere in the write-amplification analysis), or (b) stamping a fresh rewrite-time timestamp — the natural default for a delete-partition-then-reinsert loop.

Under (b), the trace is: a client fetches page 1 of a 3-member collection (cursor = last item's real `CreationTimestamp`), an *unrelated, membership-preserving* Collection selector edit fires a full rewrite that re-stamps all rows with `now()`, and the client's page-2 request — using its now-stale cursor — finds that every surviving row's `product_created_at` is *greater* than the cursor (since `now()` is newer than the original timestamps), so the `< (?, ?)` predicate matches zero rows. The client receives `hasNextPage: false` and silently believes it has seen the whole collection, when in fact the untouched remaining members were dropped with no error, no short-page signal, and nothing distinguishing this from "the collection genuinely has 3 members."

**Fix, adopted into this design's contract:**

1. `product_created_at` in `collection_membership_by_collection` is always the **product's own immutable `CreationTimestamp`**, fetched from the authoritative product row (or already in hand, for the incremental path) — never a rewrite-time value. This is stated explicitly in the schema comment (§9) precisely to prevent an implementation from defaulting to `now()`.
2. The Collection-selector-change trigger is specified as a **diff**, not a blind delete+reinsert: compute the new matching product-UID set, compare it against the current partition contents (read via the same `collection_membership_by_collection` partition, or via `collection_membership_by_product` for the reverse direction), and issue `INSERT`s only for newly-matching products (with their real `CreationTimestamp`) and `DELETE`s only for no-longer-matching products. Unaffected members are never touched, so their clustering key — and therefore any in-flight client's cursor — is never perturbed.
3. This mirrors what the incremental `Upsert`/`Remove` path already does correctly (it diffs a single product against its current collection set using a real `CreationTimestamp` already in hand) — the fix simply extends the same diff discipline to the bulk path instead of treating "bulk" as license for a cheaper-looking blind rewrite.

This closes the bug at the contract level; §16 reflects the corrected method signature.

### 11.2 Residual, accepted keyset properties (inherited, not new)

- A product added to a collection with a `CreationTimestamp` *older* than an in-flight client's last-consumed cursor position will not retroactively appear on an earlier page — standard keyset behavior, unchanged from every other paginated connection in this codebase, and not a new weakness introduced here.
- A product removed from a collection between page fetches simply disappears with no dangling reference and no error — the standard, accepted "phantom/missing edge" tradeoff every keyset design in this codebase already carries.

## 12. Authorization and information-leak analysis

022 is unimplemented today, so this feature ships with **zero enforcement**, identical to current behavior for `Collection.products`, `Category.products`, and `Query.products` (§3). The contract below is written so that turning on 022 later requires no new schema migration for the connection itself — but adversarial review found that the original writeup's claim of "no additional migration" did not actually hold end-to-end, and this section adopts the fixes required to make it true.

### 12.1 Where authorization is enforced (query-time projection selection, never post-hoc)

`ListCollectionMembers(ctx, namespace, collectionUID, visibility, page)` resolves `visibility` from `auth.PrincipalFromContext(ctx)` (`auth/context.go:18-21`) and selects between `collection_membership_by_collection` (management scope) and `collection_membership_public_by_collection` (public scope) **before** building the paginated CQL query — exactly 022 §12.1-12.2's mandate, and exactly what rules out post-fetch filtering or the over-read/fill approach evaluated (and rejected) as Candidate D.

### 12.2 Finding 1 (confirmed): unscoped `memberCount` is a label-predicate oracle — closed by design amendment

Adversarial review constructed a concrete attack: an ordinary PUBLIC-scoped user creates a Collection with `matchLabels: {"internal-sku": "PROJECT-CHIMERA-v2"}`, queries `products(first:10)` (empty, correctly, since no public product matches) alongside `status.resolved.memberCount`. If `memberCount` is sourced from the single, unscoped `collection_membership_by_collection` table — as the original recommendation described it ("sourced from the membership table's row count going forward instead of a live scan," with no per-scope variant) — the mismatch (`products: []` but `memberCount: 1`) proves a hidden product exists with that exact label value. Because selectors support `In`/`NotIn`/`Exists`/`DoesNotExist`, this generalizes into a binary-search oracle over the entire label space of products the caller cannot see — precisely the "counts... reveal whether a resource exists" leak 022 §13 prohibits by name.

**Design amendment, adopted:** `memberCount` is **not** a single collection-wide value once 022 ships. It must be computed from the same scope-specific table `ListCollectionMembers` selects for the requesting principal — i.e., `memberCount` is a per-request/per-scope derived value (or, if it must remain a stored field on `Collection.status.resolved`, it is split into a scope-specific pair, e.g. an internal `memberCount` used for management-scope reads and a separately maintained public-scope count for public reads). This is a genuine scope expansion of the original "reserve `visibility` on `ListCollectionMembers` now, no future migration needed" claim: **the visibility reservation must extend to the count path from day one**, or the leak goes live silently the moment 022 turns on scoping with no further code change. This document treats that extension as part of the baseline contract (§16), not as follow-up work.

### 12.3 Finding 2 (confirmed): the migration fallback is a full, ungated bypass of 022

The fallback path for a not-yet-backfilled collection is "today's live `ListProductsByLabelSelector` scan" (§13), which per §3 has **zero authorization applied** — it is precisely the ungated full-namespace scan 022 exists to replace. As originally written, nothing prevented 022 from being enabled before backfill completes, or before a large-namespace backfill job reaches every collection. **Design amendment, adopted:** enabling 022's enforcement in `ListCollectionMembers` and completing the Collection-membership backfill are treated as a single, sequenced rollout gate, not two independent workstreams — 022 (whenever it ships) must not be turned on for the Collection-membership read path until the backfill job's completion is verifiable (§13 defines the completeness signal this requires). Until 022 ships, the fallback's lack of authorization is unchanged from current production behavior, so this is a rollout-ordering constraint, not a new regression, but it is written down explicitly here so it is not lost when 022 is scheduled.

### 12.4 Finding 3 (plausible, flagged as an open question, not fully closed here)

If Product visibility/publish state is a field independent of `Labels` (near-certain under 022's design, which treats "published vs. unpublished" as orthogonal to label-based selector matching), then a Product transitioning public→private via a mutation that does not touch `Labels` will not fire the label-diff trigger that maintains `collection_membership_public_by_collection`. Two bad outcomes follow: either the stale public-index row keeps serving the now-private product's full node data through the Collection path (a direct §13 violation, and inconsistent with `product(id:)` correctly returning `NOT_FOUND` for the same product), or a defensive per-row re-check at read time drops the stale entry and reproduces the exact short-page-vs-`memberCount` mismatch Finding 1 already demonstrates.

This is **not fully resolved by this design** — it is listed as an explicit unresolved question in §19 with a required action: whenever 022 defines the concrete visibility/publish-state field, the Collection-membership write triggers in §16 must be extended to include "Product visibility transition" as a third trigger (alongside label change and selector change), symmetrically maintaining the public-scoped table. This document commits to that extension being mandatory before 022 enforcement is turned on for this path, per §12.3's sequencing gate.

### 12.5 Axes checked and found sound

- **Cursor forgery/replay**: `DecodeKeysetCursor` validates only the `"keyset"` tag, timestamp parseability, and id — it does not check row existence, so a forged cursor for a real-but-hidden product's `(timestamp, uid)` is indistinguishable from one for a non-existent tuple; no observable leak beyond Findings 1/3 above. (The cursor format not being visibility-bound, per 022 §12.2's stricter requirement, is a spec-compliance gap worth closing defensively but was not turned into an independent exploit.)
- **`hasNextPage`/short pages on the primary connection**: safe, provided the twin scope-specific tables are implemented and kept in sync as required by §16/§12.4 — pagination runs entirely inside one consistent, correctly-scoped table with no post-hoc filtering.
- **Error messages**: `ErrNotFound` is reused undifferentiated for missing collection/product, matching 022 §13's mandate; no new differentiation introduced.
- **Ordering**: uniform `(CreationTimestamp DESC, UID DESC)` regardless of which table is queried; no additional leakage beyond the inherent, unavoidable property of any filtered keyset list.

## 13. Consistency, staleness, rebuild, and backfill behavior

### 13.1 Authoritative vs. derived data

`Product.Labels` and `Collection.Spec.Selector` remain authoritative — unchanged, still the source data. `collection_membership_by_collection`/`collection_membership_by_product` (and their future public-scoped twins) are a fully rebuildable derived index: any drift can be repaired by re-running `catalog.MatchesLabels` (`catalog/selector.go:8-46`) against the authoritative tables. `Collection.status.resolved.memberCount`'s doc comment — "a cached hint; collection.products is authoritative" — is preserved, except "authoritative" now means "authoritative derived index," not "recomputed live every request," and (per §12.2) is scope-specific rather than a single value.

### 13.2 Steady-state consistency

Writes are synchronous, inside the same request as the triggering `CreateProduct`/`UpdateProduct`/`DeleteProduct`/`CreateCollection`/`UpdateCollection` call. No cross-partition transaction exists between the forward and reverse tables (ScyllaDB gives no such primitive), so a crash mid-write can desync them; this is an accepted, bounded risk (repaired by the same rebuild mechanism as backfill, §13.4), not eliminated.

### 13.3 The backfill/bootstrap gap (confirmed by adversarial review) — named and bounded, not hand-waved

Adversarial review traced a concrete, previously-unnamed failure mode. On the day this design ships, every Collection/Product row that existed **before** deploy has zero membership rows, because admission hooks only fire on new writes. `HasCollectionMembership(collectionUID)` returning `false` correctly triggers a fallback to the live `ListProductsByLabelSelector` scan — this holds and is correct on literal deploy day, before any backfill job runs.

The gap is in the backfill job's transition window, not its absence. Since ScyllaDB gives no cross-partition, arbitrary-size atomic transaction (confirmed: no materialized view, no aggregate column anywhere in `001_initial_schema.cql`), a backfill job populating a collection with, say, 500 members necessarily writes those rows incrementally. Mid-backfill, for a collection currently at 1-of-500 written rows: `HasCollectionMembership(X)` returns **true** (a `LIMIT 1` existence check needs only one row), so the resolver switches off the live-scan fallback and reads the partially-populated table directly — returning a fully-formed, error-free 1-edge connection with `hasNextPage: false`. This is a **silent under-count, structurally indistinguishable from a genuine 1-member collection**, and it reproduces — inside Candidate A's own migration mechanism, narrowed from "permanent" to "a race window" — exactly the failure mode this design's own decision matrix used to reject Candidate B ("empty-vs-unmaterialized ambiguity"). `HasCollectionMembership`/`HasRepositories`/`HasCatalogResources`'s `LIMIT 1` semantics (designed for spec 041's precondition-check use case) cannot express "fully populated" vs. "partially populated" — repurposing an existence check as a migration-completeness signal is a category error, not a minor gap.

**Design amendments, adopted, to bound this window instead of leaving it open-ended:**

1. **A per-collection backfill-completion marker is required**, not just a job that "populates all existing collections." Concretely: reuse the existing `CollectionStatus.observedGeneration`/`conditions` fields already present on the schema (`shared/schemas/collection.graphqls:215-235`, same shape as `CategoryTaxonomyStatus`) to record a condition such as `MembershipBackfilled: true` written atomically as the *last* step of that collection's backfill (after all rows are written), analogous to how `categorytaxonomy.Reconciler` only reports `Resolved` once its computation is complete. `HasCollectionMembership` continues to answer "is there at least one row" for the cheap common case, but the resolver's decision to trust the materialized table for a *given* collection is gated on this condition being present, not merely on row existence.
2. Until that condition is set for a collection, `ListCollectionMembers` **must** continue to use the live-scan fallback for that specific collection, even if `HasCollectionMembership` would return `true` — closing the exact race window traced above.
3. The backfill job itself (new, does not exist today, and this document does not claim otherwise) is scoped as: for each Collection lacking `MembershipBackfilled`, diff-write its full membership set (using the same diff discipline as §11's bulk-rewrite fix, not a blind delete+reinsert), then set the condition. This gives an automatable, testable completion signal instead of relying on operator judgment to know when the fallback branch can be safely deleted.
4. `Collection.status.resolved.memberCount` during the backfill window reports whatever the (possibly partial) materialized table currently holds **only if** the fallback-gating condition from (2) is respected — i.e., during backfill, `memberCount` is sourced from the live scan just like `products` is, so the two never visibly disagree mid-migration. This directly addresses the adversarial finding that the original design's `memberCount` would "corroborate rather than flag the wrong answer" during the race.

### 13.4 Rebuild and drift detection going forward

Post-backfill, drift detection is not fully automatic — this document does not claim a checksum/reconciliation job exists. It commits to the same `resource_version`-per-row mechanism (§9) being sufficient to support a future diff-based repair job (compare each membership row's stored `resource_version` against the current product's `resource_version`; a mismatch means the row is stale and must be re-evaluated), but building that job is out of scope for this design and is listed as an open question in §19, not silently assumed solved.

## 14. Memdb compatibility

Both tables mirror straightforwardly onto `go-memdb`: `map[uuid.UUID][]membershipRow` keyed by `(namespace, collectionUID)` for the forward index, `map[uuid.UUID][]uuid.UUID` keyed by `(namespace, productUID)` for the reverse index, sorted via the existing `paginateSlice`/`compareKeyset` helpers (`memdb/backend.go:18-33,114-127`) rather than CQL clustering order. This also **fixes** the previously-unenforced sort defect noted in §2.1: `ListCollectionMembers`'s memdb implementation must sort by `(CreationTimestamp DESC, UID DESC)` before returning, unlike today's `ListProductsByLabelSelector`, which does not. No structural blocker exists for the write-time diff logic either — it is backend-agnostic Go code (label evaluation via `catalog.MatchesLabels`) sitting above both `Datastore` implementations.

## 15. Controller-manager integration and future controller responsibilities

**Zero Product/Collection controllers are introduced by this design.** Everything — admission-time writes, reads, backfill — lives inside `gitstore-api`. `gitstore-controller-manager` is untouched: no new `Reconciler`, no new `ListWatcher`, no new eventbus consumer, no interaction with the Product-cache-without-a-reconciler pattern from spec 042 (`categorytaxonomy/products.go:44-116`).

**Ownership going forward:** `collection_membership_by_collection`/`collection_membership_by_product` (and their public-scope twins) are owned permanently by `gitstore-api`'s datastore layer, by design, to preserve single-request write consistency (§13.2). If a future Collection or Product controller is justified for unrelated reasons (e.g., enriching `status.resolved` with additional computed fields the way `CategoryTaxonomy`'s controller does for `Depth`/`Path`/`ChildCount`), it may **read** the membership table, but must never become a second writer to it — a second writer would reintroduce exactly the eventual-consistency/staleness profile this design exists to avoid (§8's rejection of Candidates B and D). This constraint should be written into any future controller's design doc as a hard dependency on this one.

## 16. API and datastore contract changes

New `Datastore` interface methods (`gitstore-api/internal/datastore/datastore.go`), alongside existing Product/Collection methods:

- `ListCollectionMembers(ctx context.Context, namespace string, collectionUID uuid.UUID, visibility Visibility, page PageParams) (PageResult[*Product], error)` — primary read; replaces `ListProductsByLabelSelector` as the `Collection.Products` data source. `visibility` selects between the management and public-scoped tables (§12.1); required from day one, not deferred.
- `CollectionMemberCount(ctx context.Context, namespace string, collectionUID uuid.UUID, visibility Visibility) (int, error)` — **new relative to the original recommendation**, added directly in response to §12.2's Finding 1: a scope-aware count, so `memberCount` can never be sourced from an unscoped table.
- `ReplaceCollectionMembership(ctx context.Context, namespace string, collectionUID uuid.UUID, products []ProductRef) error` — Collection-selector-change trigger. **Signature changed from the original `productUIDs []uuid.UUID`** to carry each product's real `CreationTimestamp` (`ProductRef{UID, CreationTimestamp}`) and is specified as a diff against current partition contents, not a blind delete+reinsert — the §11.1 fix.
- `UpsertCollectionMembership` / `RemoveCollectionMembership(ctx, namespace, collectionUID, productUID uuid.UUID) error` — incremental, Product-label-change trigger, diffed against `collection_membership_by_product`.
- `HasCollectionMembership(ctx context.Context, namespace string, collectionUID uuid.UUID) (bool, error)` — cheap existence check, same "LIMIT 1, not a count" convention as `HasRepositories`/`HasCatalogResources`. Per §13.3, this is **not** sufficient on its own to gate the fallback-to-live-scan decision; it must be combined with the `MembershipBackfilled` condition check.

**Types:** reuse existing `PageParams`/`PageResult[T]`, `EncodeKeysetCursor`/`DecodeKeysetCursor` unchanged — no new cursor format.

**Ordering guarantee:** `(CreationTimestamp DESC, UID DESC)`, enforced by both backends, fixing memdb's currently-unenforced sort (§2.1, §14).

**Error behavior:** `ErrNotFound` for missing collection/product, `ErrConflict` for optimistic-concurrency membership writes (mirroring `ApplyCategoryTaxonomyStatusPatch`'s `ResourceVersion` check pattern, `datastore.go:83-115`).

**Resolver change:** `collectionResolver.Products` (`collection.resolvers.go:20-55`) drops the `spec.Selector`/`ListProductsByLabelSelector` call and calls `r.service.ListCollectionMembers`, feeding the returned `PageResult[*Product]` into the existing generic connection builder Category/Namespace already use, retiring `BuildProductConnectionFromSlice` for this path. `Collection.status.resolved.memberCount` resolves via `CollectionMemberCount`, scope-aware and backfill-window-aware per §13.3(4).

## 17. Migration and rollout plan

1. Ship the two Scylla tables (`collection_membership_by_collection`, `collection_membership_by_product`) plus the memdb equivalents, and the admission-path write hooks (incremental first, since it's the lower-risk path — bulk rewrite ships with the §11.1 diff logic from the start, never as a naive delete+reinsert).
2. Ship `ListCollectionMembers`/`CollectionMemberCount` behind the `HasCollectionMembership` + `MembershipBackfilled`-condition gate described in §13.3; until a given collection's condition is set, both continue to use today's live scan, so behavior is unchanged for every not-yet-backfilled collection.
3. Build and run the backfill job (diff-based per-collection rewrite, condition set only on completion, per §13.3(3)) as a one-shot operational task; monitor `MembershipBackfilled` coverage across all namespaces before proceeding.
4. Once all collections report `MembershipBackfilled`, delete the fallback branch from the resolver — do not keep it as a permanent dual-source-of-truth.
5. **Do not enable 022 enforcement for this path** (i.e., do not wire the `visibility` parameter to a real authz decision, and do not populate the public-scoped twin tables) until both (a) 022 itself ships, and (b) the visibility-transition trigger required by §12.4 is implemented — these are sequenced, not independent, per §12.3.

## 18. Test strategy

- **Unit**: `catalog.MatchesLabels` is already covered (`selector_test.go:16-107`); extend coverage to the diff functions (label-change diff, selector-change diff) with property-style tests asserting that unaffected members' `CreationTimestamp` is never rewritten across a bulk `ReplaceCollectionMembership` call (direct regression test for §11.1).
- **Pagination correctness**: a test that pages a collection, performs a membership-preserving Collection selector edit mid-pagination (adding a redundant matchExpression that changes nothing), and asserts the next page still returns all originally-untouched members — this is the exact regression the adversarial pagination-correctness finding demonstrated as broken under the original signature.
- **Leak regression**: a test asserting `CollectionMemberCount` under PUBLIC scope never exceeds the count of edges a PUBLIC-scoped `products` query would return for the same collection and selector, across `matchLabels`/`In`/`NotIn`/`Exists`/`DoesNotExist` selector shapes — direct regression for §12.2's Finding 1.
- **Backfill-window regression**: a test that starts a fake partial backfill (writes 1-of-N rows, does not set `MembershipBackfilled`), and asserts `ListCollectionMembers`/`CollectionMemberCount` still return the live-scan result, not the partial materialized result — direct regression for §13.3.
- **Dual-backend parity**: run the full `Collection.products` connection test suite against both memdb and Scylla backends, asserting identical ordering, cursor behavior, and count semantics — extending the existing pattern implied by the dual-implementation maintenance burden noted throughout the investigation.
- **Cross-table consistency**: a test/tool that walks `collection_membership_by_collection` and `collection_membership_by_product` for a namespace and asserts they agree (every forward row has a matching reverse row and vice versa) — a minimal drift detector, given none exists elsewhere in the repo to reuse (§13.4).

## 19. Risks and unresolved questions

1. **Wide-partition/hotspot risk is unmitigated.** A single collection with a very broad selector (e.g., `Exists` on a common label) accumulates an unbounded number of clustering rows in one `(namespace, collection_uid)` partition. No sharding/bucketing scheme is designed here; mitigating this would require a synthetic bucket suffix on the partition key, which complicates cursor encoding and is deferred as a follow-up if any collection is observed approaching Scylla's practical per-partition row-count guidance.
2. **Cross-table atomicity is not guaranteed.** `collection_membership_by_collection` and `collection_membership_by_product` are updated in the same logical operation but not atomically (no Scylla multi-partition transaction); a crash mid-write can desync them, recoverable only via the (not-yet-built) drift-detection/repair job in §13.4.
3. **Visibility-transition trigger is a named gap, not a closed one (§12.4).** This document specifies that a Product's visibility/publish-state change must become a third membership-write trigger before 022 enforcement is turned on, but the concrete field and mechanics depend on 022's own not-yet-finalized design. Shipping this design's write-time hooks without that third trigger, then later enabling 022 without adding it, will reproduce Finding 3's leak exactly as described.
4. **The backfill job and its completion signal do not exist yet.** §13.3 specifies the shape they must take (diff-based rewrite, `MembershipBackfilled` condition gating) but this document does not claim they are built; rollout (§17) is explicitly sequenced to prevent the ambiguity window from being silently absorbed.
5. **Cursor format is not visibility-bound**, contrary to 022 §12.2's stricter requirement that a cursor encode enough scope information to prevent replay-across-scope probing. This was checked by adversarial review and not turned into an independent exploit given the other mitigations in §12, but it remains a spec-compliance gap relative to 022's letter, not just its spirit, and should be revisited when 022's cursor requirements are finalized.
6. **`resource_version`-based drift detection (§13.4) is specified but not implemented.** No automated job exists today to detect or repair post-backfill drift; this is accepted as follow-up work, not a blocking gap for initial rollout, but should not be indefinitely deferred given the cross-table atomicity risk in item 2.
7. **Scope of `CollectionMemberCount` beyond PUBLIC/MANAGEMENT.** If 022 eventually introduces finer-grained scopes than a binary PUBLIC/MANAGEMENT split, the two-table (management + public) design in §9/§12.1 would need to generalize; this document does not attempt to anticipate that and treats it as an explicit non-goal for this iteration.

## 20. Phased implementation plan

**Phase 1 — Datastore foundation (no behavior change).**
Add the two Scylla tables and memdb equivalents; add `Datastore` interface methods (`ListCollectionMembers`, `CollectionMemberCount`, `ReplaceCollectionMembership` with the §11.1 diff contract, `UpsertCollectionMembership`, `RemoveCollectionMembership`, `HasCollectionMembership`); implement admission-path write hooks for both backends. `Collection.products` and `status.resolved.memberCount` continue to use today's live-scan path — nothing reads the new tables yet.

**Phase 2 — Read path cutover, gated by backfill condition.**
Change `collectionResolver.Products` to call `ListCollectionMembers` and `CollectionMemberCount`, both gated per collection on the `MembershipBackfilled` condition (§13.3). Add the fallback-branch test (§18) proving live-scan behavior is unchanged for any not-yet-gated collection. No visibility/authz parameter is wired to a real decision yet — `visibility` defaults to "management" (today's ungated behavior) for every caller, matching current production behavior exactly.

**Phase 3 — Backfill.**
Build and run the one-shot backfill job (diff-based per-collection rewrite, condition set only on full completion). Monitor coverage; do not delete the fallback branch until 100% of collections across all namespaces report `MembershipBackfilled`.

**Phase 4 — Fallback removal.**
Delete the live-scan fallback branch and `BuildProductConnectionFromSlice`'s usage for this path entirely. `ListCollectionMembers`/`CollectionMemberCount` become the sole source of truth for `Collection.products`/`memberCount`.

**Phase 5 — 022 integration (sequenced with 022's own rollout, not before).**
Implement the public-scoped twin tables (`collection_membership_public_by_collection`, `collection_membership_public_by_product`), wire the visibility-transition trigger (§12.4/item 3 in §19), wire `visibility` to a real authz decision derived from `auth.PrincipalFromContext`, and only then enable enforcement — per §12.3's explicit sequencing requirement that this phase must not ship ahead of both 022 itself and the visibility-transition trigger.

Relevant files referenced throughout: `gitstore-api/internal/graph/resolver/collection.resolvers.go:20-55`, `gitstore-api/internal/graph/resolver/pagination.go:14-266`, `gitstore-api/internal/datastore/memdb/backend.go:18-33,114-127,592-607,686,771`, `gitstore-api/internal/datastore/scylla/backend.go:793-816,1140-1159`, `gitstore-api/internal/datastore/scylla/migrations/001_initial_schema.cql`, `gitstore-api/internal/datastore/scylla/pagination.go:72-121`, `gitstore-api/internal/catalog/collection.go:16-36`, `gitstore-api/internal/catalog/selector.go:8-46`, `gitstore-api/internal/catalog/selector_test.go:16-107`, `gitstore-api/internal/datastore/datastore.go:83-127,134-212`, `gitstore-api/internal/middleware/security/graphql.go:39-42,46-122`, `gitstore-api/internal/auth/provider/rbaclocal/provider.go:52-92`, `gitstore-api/internal/eventbus/eventbus.go`, `gitstore-controller-manager/internal/categorytaxonomy/reconciler.go:87-190`, `gitstore-controller-manager/internal/categorytaxonomy/products.go:44-116`, `gitstore-controller-manager/cmd/controller/main.go:130-139,170-212`, `gitstore-controller-manager/internal/status/patch.go`, `shared/schemas/collection.graphqls:156-256`, `shared/schemas/category.graphqls:161-224`, `docs/implementation/022-opa-data-authorization.md` (§2-3, §7, §12-13).
