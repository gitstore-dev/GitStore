# Contract: CategoryTaxonomy Reconciler and Client Adapters

This is a Go-interface contract (internal to `gitstore-controller-manager`), not a wire/schema contract — the wire contract this spec consumes was already defined and shipped by spec 040 (`contracts/watch-api.graphql`, `contracts/status-api.graphql`).

## Reconciler

Satisfies the existing `types.Reconciler` interface (spec 026, `gitstore-controller-manager/internal/types/types.go` — unchanged):

```go
type Reconciler interface {
    Reconcile(ctx context.Context, req WorkItemKey) ReconcileResult
}
```

`CategoryTaxonomyReconciler.Reconcile`:
1. Reads the current object from its `cache.CacheAccessor[CategoryTaxonomy]` (read-only view). Missing → `types.ResultTerminal` (resource deleted, per the existing spec 026 quickstart pattern).
2. Runs cycle detection (research.md R3) over the full cached population for the resource's namespace.
3. If not a cycle participant: computes `Depth`/`Path` by walking `ParentRefName` through the cache to the root (research.md R2); if a cycle participant: leaves `Path`/`Depth` at their last-observed values (FR-008).
4. Computes `ChildCount` (linear scan of the cache for entries whose `ParentRefName` equals this resource's name) and `ProductCount` (research.md R4 — via the client's product-list call, filtered client-side by `categoryRef`).
5. Computes `ParentResolved`/`Acyclic`/`Ready` conditions (FR-006, FR-007, FR-009) and the required-file-reference condition per media entry (FR-010, FR-011, research.md R5 — always `Unknown`).
6. Builds a `status.StatusPatch` (`ObservedGeneration` set to the generation observed at step 1, per FR-012; `Resolved` marshaled from the computed `ResolvedCategoryTaxonomy`; `Conditions` set to the full computed slice).
7. If `patch.IsNoOp(current.Status)`: returns `types.ResultOK()` without writing (FR-013).
8. Otherwise calls `client.Apply(ctx, key, patch)`. On `types.ErrConflict`: returns `types.ResultTransient(err)` (FR-014, retried with fresh cache state on next dispatch). On success: if this reconcile changed `Path`/`Depth` relative to what was previously observed, re-enqueues every direct child found in the cache (research.md R2) so FR-005's descendant-propagation requirement is satisfied, then returns `types.ResultOK()`.

## CategoryTaxonomyListWatcher

Satisfies the existing `listwatch.ListWatcher[T]`/`Watcher[T]` interfaces (spec 036, `gitstore-controller-manager/internal/listwatch/listwatcher.go` — unchanged):

```go
type ListWatcher[T any] interface {
    List(ctx context.Context) (ListResponse[T], error)
    Watch(ctx context.Context, resourceVersion string) (Watcher[T], error)
}

type Watcher[T any] interface {
    Events() <-chan WatchEvent[T]
    Err() error
    Stop()
}
```

`CategoryTaxonomyListWatcher.List`: issues the existing `categories(namespace: ...)` paginated query (via the new `graphqlclient` package, POST transport) across all pages, returning every `CategoryTaxonomy` in scope and the highest observed `resourceVersion` as the list-time cursor.

`CategoryTaxonomyListWatcher.Watch`: opens a `graphql-transport-ws` connection and sends a `subscription { watchCategories(resourceVersion: $rv) { ... } }` per spec 040's `contracts/watch-api.graphql`. Maps the server's `WATCH_EXPIRED`-extension GraphQL error to an error satisfying `errors.Is(err, listwatch.ErrWatchExpired)`. Maps `ADDED`/`MODIFIED`/`DELETED` `CategoryWatchEvent`s to `listwatch.WatchEvent[CategoryTaxonomy]{Type: ..., Object: ..., ResourceVersion: ...}`.

## graphqlStatusClient

Satisfies the existing `status.StatusClient` interface (spec 026, extended by spec 040 R8 — `Apply`'s signature itself is unchanged):

```go
type StatusClient interface {
    Apply(ctx context.Context, key types.WorkItemKey, patch *StatusPatch) error
}
```

`graphqlStatusClient.Apply`: issues `mutation { updateCategoryStatus(input: {...}) { category { metadata { resourceVersion } } conflict { currentResourceVersion } } }` per spec 040's `contracts/status-api.graphql`. `patch.Resolved` (JSON bytes) is unmarshaled into the `resolved: ResolvedCategoryTaxonomyInput` argument. A non-null `conflict` in the response maps to `types.ErrConflict`. A `NOT_FOUND`-extension GraphQL error maps to `types.ErrNotFound`. A `FORBIDDEN`-extension error is returned wrapped as an ordinary error (not a sentinel — an authorization failure is not something a reconciler should blindly retry indefinitely; it surfaces as a `TerminalFailure` at the call site).

## graphqlclient (new minimal client package)

```go
type Client struct { /* base URL, bearer token, http.Client, dialer */ }

func (c *Client) Query(ctx context.Context, query string, vars map[string]any, out any) error
func (c *Client) Mutate(ctx context.Context, mutation string, vars map[string]any, out any) error
func (c *Client) Subscribe(ctx context.Context, subscription string, vars map[string]any) (Subscription, error)

// Subscription mirrors the existing listwatch.Watcher[T] shape (spec 036) so
// callers can wrap it without inventing a second "open stream" pattern.
type Subscription interface {
    Next() <-chan json.RawMessage
    Err() error // valid only after Next()'s channel closes
    Stop()
}
```

`Query`/`Mutate` issue a single `POST /graphql` request (standard GraphQL-over-HTTP request/response shape, `Authorization: Bearer <token>` header) and decode `data`/`errors` into `out`, surfacing any GraphQL error's `extensions.code` for the caller to inspect. `Subscribe` dials `/graphql` as a WebSocket with subprotocol `graphql-transport-ws`, performs the `connection_init`/`connection_ack` handshake, and sends a `subscribe` message; the returned `Subscription` streams `next` message payloads until the caller calls `Stop()` or the server closes the stream with a GraphQL error, at which point `Next()`'s channel closes and `Err()` reports the reason (mirroring `listwatch.Watcher[T]`'s existing `Events()`/`Err()`/`Stop()` shape exactly, so `CategoryTaxonomyListWatcher.Watch` is a thin adapter, not a reimplementation).
