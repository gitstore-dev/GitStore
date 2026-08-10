# Quickstart: CategoryTaxonomy Reconciler

This builds directly on `specs/026-reconcile-handler/quickstart.md` (the generic reconciler pattern) and `specs/040-controller-watch-status-api/quickstart.md` (the client-adapter shape spec 040 defined but left as pseudocode). This spec implements the adapters for real and adds the `CategoryTaxonomy`-specific reconciler on top.

## 1. Wire the GraphQL client

```go
// gitstore-controller-manager/internal/graphqlclient/client.go
client := graphqlclient.New(cfg.Controller.ApiURI, bearerToken)
```

`bearerToken` is obtained once at startup via the existing `gitctl`/`staticadmin` session-issuance path (spec 040 quickstart step 5) and refreshed per that provider's existing refresh-token flow.

## 2. Wire the concrete ListWatcher and StatusClient

```go
listWatcher := listwatch.NewCategoryTaxonomyListWatcher(client)
statusClient := status.NewGraphQLStatusClient(client)
```

## 3. Register the Runner and Reconciler exactly as specs 026/036 already document

```go
catCache := cache.New[categorytaxonomy.CategoryTaxonomy]()
reconciler := categorytaxonomy.NewReconciler(cache.AsReadOnly(catCache), statusClient)

runner := &listwatch.Runner[categorytaxonomy.CategoryTaxonomy]{
    Kind:        "CategoryTaxonomy",
    ListWatcher: listWatcher,
    Cache:       catCache,
    Store:       checkpointStore, // already constructed in cmd/controller/main.go
    Enqueue:     mgr.Enqueue,
    KeyFunc:     func(c categorytaxonomy.CategoryTaxonomy) types.WorkItemKey {
        return types.WorkItemKey{Kind: "CategoryTaxonomy", Namespace: c.Namespace, Name: c.Name}
    },
    RevisionFunc: func(c categorytaxonomy.CategoryTaxonomy) string { return c.ResourceVersion },
}

if err := mgr.Register(manager.ReconcilerRegistration{
    Kind:       "CategoryTaxonomy",
    Reconciler: reconciler,
    Cache:      catCache,
    OnSuccess:  runner.MarkCompleted,
}); err != nil {
    logger.Fatal("failed to register CategoryTaxonomy reconciler", zap.Error(err))
}

go func() { _ = runner.Run(ctx) }()
if err := mgr.Start(ctx); err != nil {
    logger.Fatal("manager exited", zap.Error(err))
}
```

## Running Tests

```bash
cd gitstore-controller-manager
go test ./internal/graphqlclient/...       # unit: query/mutation/subscription framing
go test ./internal/listwatch/...            # unit: CategoryTaxonomyListWatcher against a stub server
go test ./internal/status/...               # unit: graphqlStatusClient against a stub server
go test ./internal/categorytaxonomy/...     # unit: hierarchy computation, cycle detection, conditions, no-op suppression
go test ./tests/integration/...             # FR-015: depth-3 hierarchy, cycle scenario, file-ref condition, against a real gitstore-api
```

## Verifying End-to-End

```bash
make api          # gitstore-api on :4000
make controller   # gitstore-controller-manager on :5001, now with CategoryTaxonomy registered
curl http://localhost:5001/health | jq '.kinds.CategoryTaxonomy'
# { "activeWorkers": 0, "queueDepth": 0, "poisonItems": 0, "stalled": false, "registered": true }
```

Push a 3-level category hierarchy through the existing git workflow, then query:

```graphql
query {
  category(by: { namespacePath: { namespace: "gitstore-test", name: "laptops" } }) {
    status {
      resolved {
        depth
        path
        childCount
        productCount
      }
      conditions { type status reason }
    }
  }
}
```

Expect `depth: 2`, `path: ["electronics", "computers", "laptops"]`, and `ParentResolved`/`Acyclic`/`Ready` all `True`.
