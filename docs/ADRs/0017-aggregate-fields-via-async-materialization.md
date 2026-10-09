# ADR 0017: Aggregate Fields via Async Controller Materialization

**Status**: Accepted (2026-10-03)

**Date**: 2026-10-03

**Audience**: GitStore API and controller-manager authors; anyone adding a
count, sum, or other cross-partition aggregate field to a resource's
GraphQL schema or status contract.

## Context

Several resource contracts need an aggregate derived from a *set* of related
resources, not from the resource's own row: a product count per category, a
member count per collection, a variant-readiness summary per product,
aggregate inventory per product. These numbers look like a cheap `COUNT`/`SUM`
GraphQL field, and it is tempting to resolve them by querying the datastore
directly inside the GraphQL resolver, the same way a single-row field is
resolved.

ScyllaDB (GitStore's production datastore; `go-memdb` is dev/contract-test
only) cannot do this cheaply. It has no index-maintained row count — `COUNT`
and `SUM` are full scans — and GitStore's list tables are deliberately
partitioned by time bucket (`YYYY-MM`) and/or namespace specifically to keep
individual partitions bounded in size. A "logical collection" (e.g. "every
Product in this namespace") is not one partition; it is an open-ended,
ever-growing set of partitions. There is no secondary index or materialized
view anywhere in the Scylla migrations (`gitstore-api/internal/datastore/scylla/migrations/`)
that would make a cross-partition aggregate cheap, and adding a CQL counter
column does not fix this safely — counters are a non-idempotent, op-based
CRDT that cannot batch atomically with the entity write, so retried writes
(already a normal failure mode on this codebase's `backoff/v5`-driven
admission paths) silently double-count, and a crash between the two writes
leaves the counter and the real rows permanently diverged with no
reconciliation.

This is the same reasoning that removed the generic Relay `totalCount` field
from every connection type in `shared/schemas/*.graphqls` (2026-10): it is not
part of the Relay Connection spec, had no demonstrated consumer, and the
production (ScyllaDB) backend could only ever return a `-1` "unknown"
sentinel for it. That removal eliminated the *generic*, every-connection case.
This ADR covers the *specific*, named-field case — `CategoryTaxonomy.status.resolved.productCount`,
`Collection.status.resolved.memberCount`, `Product.status.resolved.variantSummary`,
`Product.status.resolved.totalInventory`, and future fields shaped like them
— where a product requirement for a real aggregate exists and dropping the
field is not the answer.

GitStore has already built and shipped a working pattern for exactly this,
for one resource pair, but it has never been written down as a reusable rule.
A future implementer (human or agent) asked to add a new aggregate has
nothing steering them away from a live Scylla scan and nothing pointing them
at the pattern to copy.

## Decision

Aggregate fields over a set of related resources MUST be computed
asynchronously by a `gitstore-controller-manager` reconciler, written into the
owning resource's `status.resolved` blob via the existing `StatusClient` patch
mechanism, and documented in the GraphQL schema as a non-authoritative cached
value. They MUST NOT be computed by a `gitstore-api` GraphQL resolver querying
the datastore live on the read path.

### Reference implementation

`CategoryTaxonomy.status.resolved.productCount` (specs 042, 062) is the
worked example every new aggregate should copy:

1. **Watch the children, don't poll.** The controller-manager keeps an
   in-memory `cache.Cache[Product]` populated by a `ListWatcher` against the
   durable `watchProducts` subscription (spec 040/055's watchjournal/CDC
   stack) — not a periodic re-scan.
2. **Invert the relationship to find the affected parent(s).** A child
   add/delete/relevant-field-change event enqueues the specific parent(s) it
   affects (`categorytaxonomy/products.go`'s `NewProductCategoryEnqueueHandler`),
   using the normal bounded work-queue (`alitto/pond` + `backoff/v5`), not a
   synchronous recomputation inline with the write.
3. **Recompute off the hot path.** When the parent's reconciler fires, it
   recomputes the aggregate by paginating the child connection and
   reducing client-side (`NewProductCounter`). A full-connection walk is
   acceptable here specifically because it happens debounced, rate-limited,
   and off the request path — the same operation that is unacceptable inside
   a GraphQL resolver is fine inside a reconciler.
4. **Store it as an ordinary field.** The result is written to the parent's
   `status.resolved` JSON blob through the existing `UpdateProductStatus`-style
   status-patch call. After that it is just a stored column, cheap to read on
   every direct fetch — no live aggregate query ever runs on the read path.
5. **Document it as a cache, not a source of truth.** The schema's doc
   comment must say so explicitly, matching `Collection.status.resolved.memberCount`'s
   existing comment ("cached hint; `collection.products` is authoritative").
   Staleness is bounded by the same `observedGeneration`/`conditions`
   machinery already used for every other resolved status field. Callers that
   need certainty walk the real paginated connection.

### The fan-out test: is the relationship FK-style or selector-style?

Before building a new aggregate, classify the parent↔child relationship:

- **FK-style** (`categoryRef.name`, `productRef`): the child's own fields name
  the exact parent(s) it affects. Inverting "child changed → enqueue parent"
  is O(1). Copy the Category pattern above directly. `Product.status.resolved.variantSummary`
  and `Product.status.resolved.totalInventory` (ProductVariant → Product via
  `productRef`) are FK-style and unimplemented today — building them is
  "repeat the Category pattern with `ProductVariant` as the child," nothing
  novel.
- **Selector-style** (`Collection.spec.selector` label match): a child's
  change could affect an unbounded, unknown set of parents, and finding out
  which ones requires either scanning every parent's selector against the
  changed child or maintaining an inverted index. This is a harder, open
  design problem *independent of the counting mechanism* — see
  `docs/implementation/035-collection-membership-materialization.md`, which
  defers exactly this question pending cardinality/fan-out evidence.
  `Collection.status.resolved.memberCount` stays schema-only-unpopulated
  until that question is answered; do not attempt to populate it by bolting
  a live count onto the current full-scan `Collection.products` read path.

### What this does not change

- Direct, single-resource field reads (status, spec, metadata) continue to
  resolve straight from the datastore row — this ADR only governs aggregates
  over a *set* of other resources.
- `HasRepositories`/`HasCatalogResources`-style existence checks (spec 041,
  `datastore.go`) are a different, already-correct pattern: a `LIMIT 1`
  existence probe is O(1) regardless of partition count and is not subject to
  this ADR's reasoning. Do not generalize an existence check into a count by
  removing its `LIMIT`.

## Consequences

Positive:

- New aggregate fields have a concrete template instead of re-deriving the
  tradeoffs (and re-discovering the Scylla counter hazards) from scratch each
  time.
- Aggregates stay cheap to read (ordinary status field) at the cost of
  eventual consistency, which is already the accepted model for every other
  resolved status field in GitStore.
- The fan-out test gives implementers an upfront answer to "is this a quick
  copy-paste or a design problem," before they start coding.

Negative:

- Aggregates lag writes by one reconcile cycle; they are not suitable for any
  requirement that needs a strongly consistent count (e.g. hard inventory
  oversell prevention must not read `totalInventory` as authoritative).
- Every new aggregate requires its own ListWatcher/cache/enqueue wiring in
  `gitstore-controller-manager`, not just a schema field — more upfront
  implementation cost than a naive resolver query, in exchange for not
  breaking under production data volume.
- Selector-style relationships (Collection) are not solved by this ADR; it
  only names the problem and points at where the real decision has to happen.

## Alternatives Considered

### Live aggregate query in the GraphQL resolver

Rejected. This is the trap this ADR exists to close off — it works at demo
scale and degrades into a full historical-partition scan in production,
exactly like the removed `totalCount` field did.

### ScyllaDB counter column, updated synchronously at write time

Rejected as the general mechanism. Non-idempotent under retried writes,
cannot batch atomically with the entity row, and drifts under exactly the
failure modes GitStore's admission/reconcile paths already have to tolerate
(partial failures, retries, multi-replica races). Not ruled out forever for a
narrow, carefully-owned case, but no current aggregate requirement justifies
taking on that operational risk.

### Materialized view / secondary index in ScyllaDB

Rejected for the same reason it was rejected in spec 048's query design and
035's membership-materialization research: no materialized view exists
anywhere in the current migrations, and introducing one duplicates storage
and adds its own consistency/repair burden without solving the selector
fan-out problem for Collection-style relationships anyway.

## Open Questions

- Should the reconciler recompute by walking the full child connection every
  time (as `NewProductCounter` does today), or incrementally adjust a stored
  value from the watch event's delta once a resource's cardinality makes a
  full walk per reconcile too expensive?
- When `docs/implementation/035` resolves the Collection selector/fan-out
  question, does `memberCount` reuse this ADR's materialization step as-is,
  or does the chosen membership mechanism make the count fall out some other
  way?
