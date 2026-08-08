# Quickstart: Watch + Status Client for a Reconciler (spec 040)

This extends the spec 026 quickstart (`specs/026-reconcile-handler/quickstart.md`) with the two previously-missing pieces: a concrete `ListWatcher[T]` and a concrete `StatusClient`, both backed by the `gitstore-api` GraphQL server introduced in this spec.

## 1. Wire a concrete ListWatcher[T] for CategoryTaxonomy

```go
// gitstore-controller-manager/internal/listwatch/graphql_listwatcher.go

package listwatch

type CategoryTaxonomyListWatcher struct {
    client *graphqlclient.Client // thin wrapper around gqlgenclient / websocket dialer
}

func (lw *CategoryTaxonomyListWatcher) List(ctx context.Context) (ListResponse[CategoryTaxonomy], error) {
    // Query { categories { edges { node { ... } } } }, extract resourceVersion
    // from the highest metadata.resourceVersion observed, or a dedicated
    // list-time cursor if the query exposes one.
}

func (lw *CategoryTaxonomyListWatcher) Watch(ctx context.Context, resourceVersion string) (Watcher[CategoryTaxonomy], error) {
    // Open a subscription: subscription { watchCategories(resourceVersion: $rv) { ... } }
    // On a WATCH_EXPIRED extension error, return an error satisfying
    // errors.Is(err, listwatch.ErrWatchExpired).
}
```

## 2. Register the Runner exactly as spec 036 already documents

No change from `specs/036-controller-startup-resume/quickstart.md` — `Runner[CategoryTaxonomy]` is constructed with this `ListWatcher` and the existing `checkpoint.FilesystemStore` already wired in `cmd/controller/main.go`.

## 3. Wire a concrete StatusClient

```go
// gitstore-controller-manager/internal/status/graphql_status_client.go

package status

type graphqlStatusClient struct {
    client *graphqlclient.Client
}

func (c *graphqlStatusClient) Apply(ctx context.Context, key types.WorkItemKey, patch *StatusPatch) error {
    // mutation { updateCategoryStatus(input: {
    //   name: $key.Name, namespace: $key.Namespace,
    //   resourceVersion: $patch.ResourceVersion,
    //   observedGeneration: $patch.ObservedGeneration,
    //   lastAppliedRevision: $patch.LastAppliedRevision,
    //   conditions: $patch.Conditions,
    //   resolved: <unmarshal patch.Resolved into ResolvedCategoryTaxonomyInput>,
    // }) { category { metadata { resourceVersion } } conflict { currentResourceVersion } } }
    //
    // if response.conflict != nil { return types.ErrConflict }
    // if GraphQL error extensions.code == "NOT_FOUND" { return types.ErrNotFound }
    // if GraphQL error extensions.code == "FORBIDDEN" { return fmt.Errorf("status write forbidden: %w", err) }
}
```

## 4. A CategoryTaxonomy reconciler supplying `Resolved`

```go
type resolvedCategoryTaxonomy struct {
    Depth        int8   `json:"depth"`
    AncestorPath string `json:"ancestorPath"`
    ChildCount   int64  `json:"childCount"`
    ProductCount int64  `json:"productCount"`
}

func (r *CategoryTaxonomyReconciler) Reconcile(ctx context.Context, key types.WorkItemKey) types.ReconcileResult {
    obj, ok := r.cache.Get(key)
    if !ok {
        return types.ResultTerminal(errors.New("resource deleted"))
    }

    resolved, conditions := r.computeHierarchy(obj) // depth, ancestorPath, childCount, productCount + ParentResolved/Acyclic/Ready
    resolvedJSON, err := json.Marshal(resolved)
    if err != nil {
        return types.ResultTerminal(err)
    }

    patch := &status.StatusPatch{
        ResourceVersion:    obj.ResourceVersion,
        ObservedGeneration: &obj.Generation,
        Conditions:         conditions,
        Resolved:           resolvedJSON,
    }
    if patch.IsNoOp(obj.Status) {
        return types.ResultOK()
    }
    if err := r.client.Apply(ctx, key, patch); err != nil {
        if errors.Is(err, types.ErrConflict) {
            return types.ResultTransient(err)
        }
        return types.ResultTransient(err)
    }
    return types.ResultOK()
}
```

## 5. Registering a controller identity for status writes

The controller-manager authenticates to `gitstore-api` as an ordinary bearer-JWT principal:

```bash
# One-time: mint a long-lived session for the controller-manager identity
# using the existing gitctl/staticadmin session-issuance path.
gitctl gen-jwt-secret   # if not already generated for this deployment
```

Add a `controller` role to `policy.yaml` (rbac-local) granting the new status-write action:

```yaml
roles:
  controller:
    allow:
      - "category.status.write"
role_bindings:
  controller: [controller-manager]
```

The controller-manager includes the issued bearer token as `Authorization: Bearer <token>` on both the GraphQL subscription (watch) and mutation (status write) connections — no new authentication mechanism, reusing the existing chain.

## Running Tests

```bash
# gitstore-api: contract tests for the new Subscription/Mutation fields
cd gitstore-api && go test ./tests/contract/...

# gitstore-controller-manager: integration test exercising the full
# list-then-watch + status-writeback loop against a real gitstore-api
# test instance
cd gitstore-controller-manager && go test ./tests/integration/...
```
