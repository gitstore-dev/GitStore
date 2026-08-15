# Data Model: Product Watch Transport for CategoryTaxonomy Count Reconciliation

No new datastore tables or columns. This feature adds one new GraphQL type + one new dedicated `Subscription` field on the `gitstore-api` side (mirroring `CategoryWatchEvent`/`watchCategories`, per research.md R1), one new Go type internal to `gitstore-controller-manager` (a Product cache entity), and one new client-side watcher implementation. It writes nothing new to `CategoryTaxonomy.status.resolved` — `ProductCount`'s computation, defined by spec 039, is unchanged; this feature only adds a new reason for that existing computation to re-run.

## gitstore-api-Side Types

### `eventbus.Event` (existing, spec 040 — reused, not modified)

No field changes. This feature populates the existing fields with Product-specific values at two new `publishProductEvent` call sites:

| Field             | Value for a Product event                                                                                     |
|-------------------|------------------------------------------------------------------------------------------------------------------|
| `Type`            | `eventbus.Added` (create), `eventbus.Modified` (categoryRef change), `eventbus.Deleted` (delete)                |
| `Kind`            | `"Product"`                                                                                                       |
| `Namespace`       | The product's `Namespace`                                                                                        |
| `Name`            | The product's `Name`                                                                                             |
| `ResourceVersion` | The product's `ResourceVersion` at time of publish                                                               |
| `Object`          | The `*datastore.Product`, mapped by the new `WatchProducts` resolver into the strongly-typed `ProductWatchEvent.product` field below (not JSON-boxed — see research.md R4) |

### `ProductWatchEvent` (new GraphQL type, `shared/schemas/product.graphqls`)

Mirrors `CategoryWatchEvent` field-for-field:

| Field             | Type              | Notes                                                                 |
|-------------------|-------------------|------------------------------------------------------------------------|
| `type`            | `WatchEventType!` | Existing enum (spec 040), unchanged                                   |
| `namespace`       | `String`          |                                                                          |
| `name`            | `String!`         |                                                                          |
| `resourceVersion` | `String!`         |                                                                          |
| `product`         | `Product`         | Full existing `Product` type. Null for `DELETED`/`BOOKMARK`, matching `CategoryWatchEvent.category`'s convention |

### `watchProducts` (new `Subscription` field, `shared/schemas/product.graphqls`)

`watchProducts(namespace: String, selector: LabelSelectorInput, resourceVersion: String): ProductWatchEvent!` — same list-then-watch/resourceVersion/expiry semantics as `watchCategories`, backed by the same `eventbus.Bus.Subscribe("Product", rv)` call via a new `subscriptionResolver.WatchProducts` resolver mirroring `WatchCategories`.

## gitstore-controller-manager-Side Types (new)

### `Product` (cache entity, new — `internal/categorytaxonomy` package, alongside the existing `CategoryTaxonomy` type)

The type held in a new `cache.Cache[Product]`, populated by a new `Runner[Product]`'s list-then-watch loop against the existing `products` query and the new `watchProducts` subscription.

| Field             | Type     | Notes                                                                                                   |
|-------------------|----------|-----------------------------------------------------------------------------------------------------------|
| `UID`             | `string` |                                                                                                             |
| `Namespace`       | `string` |                                                                                                             |
| `Name`            | `string` |                                                                                                             |
| `ResourceVersion` | `string` |                                                                                                             |
| `CategoryRefName` | `string` | Empty when the product has no category reference. Mirrors `spec.categoryRef.name`. The only field this feature's enqueue logic reads. |

Deliberately excludes `Generation`, `Status`, `Spec`, `Body`, and every other Product field — none is needed to decide which `CategoryTaxonomy` to enqueue (see research.md R2).

### `ProductListWatcher` (new, `internal/listwatch`)

Satisfies the existing `listwatch.ListWatcher[Product]`/`Watcher[Product]` interfaces (spec 036), following the identical shape of the existing `CategoryTaxonomyListWatcher` — see contracts/product-watch-contract.md.

## Relationships

- `Product.CategoryRefName` is the same relationship already modeled by `datastore.Product`'s `spec.categoryRef.name` on the `gitstore-api` side and already read client-side by the existing `categorytaxonomy.NewProductCounter` — this feature does not introduce a new relationship, only a second consumer of the existing one, used to decide *which* `CategoryTaxonomy` `WorkItemKey` to enqueue rather than to count anything itself.
- A Product's cache-level `OnAdd`/`OnDelete` event maps to exactly one affected `CategoryTaxonomy` `WorkItemKey` (`CategoryRefName`, when non-empty); an `OnUpdate` event maps to at most two (`old.CategoryRefName` and `current.CategoryRefName`, each independently, when non-empty and when changed) — see research.md R2/R3 for the exact diffing rule.
- The actual `ProductCount` value for an enqueued `CategoryTaxonomy` is still computed exactly as spec 039 defines it (via `ProductCounter`, itself unchanged) at `Reconcile` time — this feature never computes or caches a count itself; it only decides when to ask spec 039's existing computation to re-run.

## Validation Rules (from Functional Requirements)

- An enqueue is issued only for a category actually named by a product's current or previous `categoryRef` — never for every `CategoryTaxonomy` in the namespace and never for a `categoryRef` value that did not change on update (FR-004).
- A product with no `categoryRef` (empty `CategoryRefName`) produces no `CategoryTaxonomy` enqueue on add or delete, and an update transitioning to/from no-category-ref enqueues only the one non-empty side (FR-001/FR-002/FR-003).
- A dangling `categoryRef` (names a category that does not exist in the cache) still results in an enqueue attempt; `Manager.Enqueue` returning `types.ErrKindNotRegistered` cannot occur here since `CategoryTaxonomy` is always the registered kind, and `Reconcile` on a genuinely non-existent name is a pre-existing, already-handled case in spec 039's reconciler (returns a terminal not-found result) — this feature does not need new dangling-reference handling (Edge Cases).
