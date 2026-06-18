# Quickstart: Controller Manager Runtime

**Module**: `gitstore-controller-manager`  
**Date**: 2026-06-11

---

## Prerequisites

- Go 1.25+
- `gitstore-controller-manager` module initialised (already exists at `gitstore-controller-manager/`)
- A running `gitstore-api` (for the informer watch source — wired in spec #182)

---

## Running the Skeleton

```bash
cd gitstore-controller-manager
go run ./cmd/controller
# Output: gitstore-controller-manager skeleton
```

After this spec is implemented:

```bash
make api          # start gitstore-api (Watch event source)
make controller   # start gitstore-controller-manager (add this target to root Makefile)
```

---

## Registering a Reconciler (Developer Guide)

```go
// 1. Implement the Reconciler interface
type ProductReconciler struct {
    cache controller.InformerCache[*catalog.Product]
}

func (r *ProductReconciler) Reconcile(ctx context.Context, key controller.WorkItemKey) (controller.Result, error) {
    obj, ok := r.cache.Get(key)
    if !ok {
        // Resource deleted — nothing to reconcile
        return controller.Result{}, nil
    }
    // Drive obj toward desired state...
    return controller.Result{}, nil
}

// 2. Register with the manager
mgr := controller.NewManager(cfg)
mgr.Register(controller.ReconcilerRegistration{
    Kind:            "Product",
    Reconciler:      &ProductReconciler{cache: mgr.Cache("Product")},
    MaxAttempts:     5,
    InitialInterval: 500 * time.Millisecond,
    MaxInterval:     30 * time.Second,
    Multiplier:      2.0,
    StallThreshold:  5 * time.Minute,
    WorkerCount:     4,
})

// 3. Start
if err := mgr.Start(ctx); err != nil {
    log.Fatal(err)
}
```

---

## Observing Health

```bash
# Human-readable health summary
curl http://localhost:5001/health | jq .

# Prometheus metrics
curl http://localhost:5001/metrics

# List poison items for Product kind
curl http://localhost:5001/controller/v1/poison/Product | jq .

# Re-queue a specific poison item
curl -X POST http://localhost:5001/controller/v1/poison/gitstore-test/Product/widget-pro/requeue
```

---

## Configuration

The controller manager reads configuration from environment variables (consistent with `gitstore-api` pattern via `viper`/structured config):

| Variable | Default | Description |
|---|---|---|
| `GITSTORE_CONTROLLER__PORT` | `5001` | Health/metrics HTTP listen port |
| `GITSTORE_CONTROLLER__API_URI` | `http://localhost:4000/graphql` | `gitstore-api` GraphQL URI (for informer source) |
| `GITSTORE_CONTROLLER__DEFAULT_MAX_ATTEMPTS` | `5` | Global default retry limit |
| `GITSTORE_CONTROLLER__DEFAULT_STALL_THRESHOLD` | `5m` | Global default stall threshold |

Per-kind overrides are set programmatically via `ReconcilerRegistration` fields.

---

## Testing a Reconciler

```bash
# Run unit tests (no external dependencies)
cd gitstore-controller-manager
go test ./...

# Run integration tests (requires gitstore-api)
GITSTORE_CONTROLLER__API_ADDR=localhost:4000 go test ./tests/integration/...
```
