# Contract: Product Event Publishing and Category-Enqueue Trigger

This introduces one small, wire-visible schema addition on the `gitstore-api` side (a dedicated `watchProducts` subscription and `ProductWatchEvent` type, mirroring the existing `watchCategories`/`CategoryWatchEvent` — per research.md R1, Product is a core kind and gets the same dedicated-subscription treatment `CategoryTaxonomy` already has, not the generic CRD-oriented `watchResources`/`WatchEvent` path). Everything else is a Go-interface/internal-behavior contract.

## gitstore-api side: GraphQL schema addition

`shared/schemas/product.graphqls` gains a `Subscription` extension and one new type, mirroring `shared/schemas/category.graphqls`'s existing `watchCategories`/`CategoryWatchEvent` exactly:

```graphql
extend type Subscription {
  """
  Same list-then-watch/resourceVersion/expiry semantics as watchCategories
  (spec 040), scoped to Product. Callers obtain the initial list via the
  existing `products` query.
  """
  watchProducts(
    namespace: String
    selector: LabelSelectorInput
    resourceVersion: String
  ): ProductWatchEvent!
}

"""
Product-specific watch event. Carries the same envelope as CategoryWatchEvent
but for Product, so core-kind consumers get full type safety.
"""
type ProductWatchEvent {
  type: WatchEventType!
  namespace: String
  name: String!
  resourceVersion: String!

  """
  Full Product resource for ADDED/MODIFIED. Null for DELETED/BOOKMARK.
  """
  product: Product
}
```

A new `subscriptionResolver.WatchProducts` resolver, mirroring `WatchCategories` (`gitstore-api/internal/graph/resolver/category.resolvers.go`) exactly: subscribes to `r.eventBus.Subscribe("Product", rv)`, maps `WATCH_EXPIRED` the same way, and maps each `eventbus.Event` to a `*model.ProductWatchEvent` (setting `Product` from `ev.Object.(*datastore.Product)` via the existing Product converter used elsewhere in the resolver package, non-nil only for `Added`/`Modified`).

## gitstore-api side: `publishProductEvent`

New private helper in `gitstore-api/internal/cataloggrpc/server.go`, mirroring the existing `publishCategoryTaxonomyEvent`:

```go
func (s *Server) publishProductEvent(evType eventbus.EventType, p *datastore.Product) {
    if s.eventBus == nil {
        return
    }
    s.eventBus.Publish(eventbus.Event{
        Type:            evType,
        Kind:            "Product",
        Namespace:       p.Namespace,
        Name:            p.Name,
        ResourceVersion: p.ResourceVersion,
        Object:          p,
    })
}
```

Call sites and their exact trigger condition:

1. **`admitProduct`, create branch** (after a successful `s.store.CreateProduct`): always call `s.publishProductEvent(eventbus.Added, p)`.
2. **`admitProduct`, update branch** (after a successful update path): call `s.publishProductEvent(eventbus.Modified, existing)` **only when** the incoming `resource.Spec`'s parsed `CategoryRef.Name` differs from `existing`'s previously-stored `CategoryRef.Name` (both may be empty/nil — "no category" to "no category" is not a change). This check runs inside the existing `changedSpecBody` branch (categoryRef lives in `Spec`) — it does not introduce a new no-op path, it narrows an existing one that already writes the record; a spec change that is not a categoryRef change (e.g. price/description) still persists via the existing write but does not call `publishProductEvent`.
3. **`deleteResource`'s existing `*datastore.Product` case** (after a successful `s.store.DeleteProduct`, alongside the pre-existing sibling `*datastore.CategoryTaxonomy` `Publish` call in the same function): call `s.publishProductEvent(eventbus.Deleted, r)` where `r` is the `*datastore.Product` returned by `lookupResourceByIdentity` before deletion (so its last-known `CategoryRef` is available).

No new field is added to `datastore.Product` — `CategoryRef` is parsed from the existing `Spec` JSON exactly as `admitProduct`'s existing logic already does for its own comparisons.

## gitstore-controller-manager side: `ProductListWatcher`

Satisfies the existing `listwatch.ListWatcher[Product]`/`Watcher[Product]` interfaces (spec 036, unchanged — see spec 039's `contracts/reconciler-contract.md` for the interface definitions, reused verbatim here with `T = Product`):

`ProductListWatcher.List`: issues the existing `products(namespace: ..., first: ..., after: ...)` paginated query (same query `categorytaxonomy.NewProductCounter` already issues, reusing its response-shape unmarshaling for `spec.categoryRef.name`) across all pages for all namespaces the controller-manager's service account can see, returning every `Product` (mapped to the new lightweight cache entity — `UID`, `Namespace`, `Name`, `ResourceVersion`, `CategoryRefName`) and the highest observed `resourceVersion` as the list-time cursor. Mirrors `CategoryTaxonomyListWatcher.List`'s pagination-loop structure exactly.

`ProductListWatcher.Watch`: opens the new `watchProducts(resourceVersion: $rv)` subscription and maps each delivered `ProductWatchEvent`'s strongly-typed `product.spec.categoryRef.name` field to a `Product` cache entity directly (no JSON-unmarshal-into-untyped-map step, unlike the generic `watchResources` path — see research.md R4), or maps a `DELETED` event (whose `product` is null per the schema) using the event's `namespace`/`name` alone (the enqueue glue, not the watcher, is responsible for looking up the last-known `CategoryRefName` from its own cache before the delete removes it — see below). Maps a `WATCH_EXPIRED`-extension error to `listwatch.ErrWatchExpired`, identically to `CategoryTaxonomyListWatcher.Watch`.

## gitstore-controller-manager side: Product-cache-to-CategoryTaxonomy-enqueue glue

New cache event handlers, registered in `cmd/controller/main.go` alongside the existing `CategoryTaxonomy` registration, following the exact shape of the existing `enqueueParent`/`catCache.AddEventHandler` block already used for category→parent-category propagation:

```go
enqueueCategory := func(namespace, categoryName string) {
    if categoryName != "" {
        _ = mgr.Enqueue(types.WorkItemKey{Kind: "CategoryTaxonomy", Namespace: namespace, Name: categoryName})
    }
}
productCache.AddEventHandler(cache.EventHandler[categorytaxonomy.Product]{
    OnAdd: func(_ types.WorkItemKey, p categorytaxonomy.Product) {
        enqueueCategory(p.Namespace, p.CategoryRefName)
    },
    OnUpdate: func(_ types.WorkItemKey, old, current categorytaxonomy.Product) {
        if old.CategoryRefName != current.CategoryRefName {
            enqueueCategory(old.Namespace, old.CategoryRefName)
            enqueueCategory(current.Namespace, current.CategoryRefName)
        }
    },
    OnDelete: func(_ types.WorkItemKey, p categorytaxonomy.Product) {
        enqueueCategory(p.Namespace, p.CategoryRefName)
    },
})
```

`enqueueCategory` targets `Kind: "CategoryTaxonomy"` — the already-registered kind from spec 039 — never `"Product"`, since no `Reconciler` is registered for `"Product"` in this feature (research.md R1). `mgr.Enqueue`'s existing `ErrKindNotRegistered` path is therefore never hit by this glue under normal operation (it would only be hit if `CategoryTaxonomy` registration itself failed at startup, an existing pre-condition failure mode unrelated to this feature).

## Test contract obligations (traces to FR-010/FR-011)

- A contract test on the `gitstore-api` side asserting `publishProductEvent` fires with the right `Kind`/`Type`/payload for create, for an update that changes `categoryRef`, for an update that does *not* change `categoryRef` (asserting it does NOT fire), and for delete.
- A contract test on the `gitstore-api` side for the new `WatchProducts` resolver: subscribing and receiving a correctly-shaped `ProductWatchEvent` for each event type, and a `WATCH_EXPIRED`-extension error on an expired cursor — mirroring the existing `WatchCategories` coverage.
- A contract test for `ProductListWatcher.List`/`Watch` following `listwatch_bootstrap_test.go`'s existing pattern.
- An integration test asserting: product create → target category's `productCount` increments; product delete → target category's `productCount` decrements; product categoryRef reassignment → old category decrements, new category increments, and a third unrelated category is untouched (no reconcile, no status write) — satisfying FR-004/FR-010.
- An integration test extending `reconcile_retry_resume_test.go`'s existing restart-survival pattern to a Product-driven trigger, satisfying FR-006/FR-011.
