# Quickstart: Product Watch Transport for CategoryTaxonomy Count Reconciliation

This builds directly on spec 039's already-shipped `CategoryTaxonomy` reconciler and spec 040's already-shipped `eventbus`/watch-subscription transport (both the generic `watchResources` path and the dedicated-per-core-kind pattern `watchCategories` established). This feature adds a new dedicated `watchProducts` subscription (mirroring `watchCategories`, per research.md R1) as a new *trigger source* into spec 039's existing reconciler — it does not add a new reconciler or computation.

## 1. `gitstore-api`: add `watchProducts` and publish Product events

```graphql
# shared/schemas/product.graphqls
extend type Subscription {
  watchProducts(namespace: String, selector: LabelSelectorInput, resourceVersion: String): ProductWatchEvent!
}
type ProductWatchEvent {
  type: WatchEventType!
  namespace: String
  name: String!
  resourceVersion: String!
  product: Product
}
```

```go
// gitstore-api/internal/cataloggrpc/server.go
func (s *Server) publishProductEvent(evType eventbus.EventType, p *datastore.Product) {
    if s.eventBus == nil {
        return
    }
    s.eventBus.Publish(eventbus.Event{
        Type: evType, Kind: "Product",
        Namespace: p.Namespace, Name: p.Name,
        ResourceVersion: p.ResourceVersion, Object: p,
    })
}
```

`publishProductEvent` is called from `admitProduct` (create: always; update: only when `categoryRef` changed) and from `deleteResource`'s existing `*datastore.Product` case. A new `subscriptionResolver.WatchProducts`, mirroring `WatchCategories`, subscribes to the same `eventBus` and maps events to `ProductWatchEvent`. See `contracts/product-watch-contract.md` for exact call-site conditions and the resolver mapping.

## 2. `gitstore-controller-manager`: watch Products, enqueue affected CategoryTaxonomies

```go
productClient := graphqlclient.New(cfg.Controller.ApiURI, cfg.Controller.ApiToken)
productListWatcher := listwatch.NewProductListWatcher(productClient)

productCache := cache.New[categorytaxonomy.Product]()
productRunner := &listwatch.Runner[categorytaxonomy.Product]{
    Kind:        "Product",
    ListWatcher: productListWatcher,
    Cache:       productCache,
    Store:       checkpointStore, // same checkpoint.FilesystemStore already used by CategoryTaxonomy's runner
    Enqueue:     mgr.Enqueue,      // note: this Enqueue is Runner[Product]'s own internal replay-dedup hook, not the cross-kind trigger below
    KeyFunc: func(p categorytaxonomy.Product) types.WorkItemKey {
        return types.WorkItemKey{Kind: "Product", Namespace: p.Namespace, Name: p.Name}
    },
    RevisionFunc: func(p categorytaxonomy.Product) string { return p.ResourceVersion },
}

// No mgr.Register for "Product" — Product is watched but never reconciled itself.
// Instead, its cache handlers enqueue the already-registered "CategoryTaxonomy" kind.
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

go func() { _ = productRunner.Run(ctx) }()
```

This is added alongside the existing `registerCategoryTaxonomy` wiring in `cmd/controller/main.go` — the `CategoryTaxonomy` `Runner`/`Reconciler`/`Manager.Register` call from spec 039 is unchanged.

## Running Tests

```bash
cd gitstore-api
go test ./internal/cataloggrpc/...          # unit: publishProductEvent fires/doesn't fire correctly on create/update/delete
go test ./tests/contract/...                # contract: admit a Product, subscribe to watchProducts, assert delivery

cd gitstore-controller-manager
go test ./internal/listwatch/...            # unit: ProductListWatcher against a stub server (List/Watch/expiry)
go test ./internal/categorytaxonomy/...     # unit: Product cache OnAdd/OnUpdate/OnDelete enqueue targeting
go test ./tests/integration/...             # FR-010/FR-011: product create/delete/reassignment convergence; restart-survival
```

## Verifying End-to-End

```bash
make api          # gitstore-api on :4000
make controller   # gitstore-controller-manager on :5001, now also watching Product
```

Create a category with zero products, confirm its `productCount` is `0`, then push a new product referencing that category through the existing git workflow — **without touching the category at all**:

```graphql
query {
  category(by: { namespacePath: { namespace: "gitstore-test", name: "laptops" } }) {
    status { resolved { productCount } }
  }
}
```

Expect `productCount: 1` to appear within one reconciliation cycle, with no push to the category itself. Then push a category reassignment (edit the product's `categoryRef` to a different category) and confirm the original category's count drops back to `0` while the new category's count becomes `1`, and that a third, unrelated category's `productCount`/status are untouched (no status write, verifiable via the existing `docs/runbooks/controller-lag.md` reconcile-count signal for that category's key staying flat).
