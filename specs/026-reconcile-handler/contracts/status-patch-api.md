# Contract: StatusPatch API

**Package**: `github.com/gitstore-dev/gitstore/controller-manager/internal/status`  (new)  
**Stability**: Stable (used by all reconcilers that write status)

## StatusPatch Struct

Fields match the common status shape shared by all core GitStore resource kinds (issue #40, `shared/schemas/`). `resolved` is kind-specific and excluded from the generic patch.

```go
// StatusPatch is a partial-merge update applied to a resource's .status sub-resource.
// Only non-nil pointer/slice fields are written; all other status fields are left unchanged.
// ResourceVersion is always required for optimistic-concurrency conflict detection.
type StatusPatch struct {
    ResourceVersion     string       // required; value of metadata.resourceVersion observed at dispatch
    ObservedGeneration  *int64       // MUST be set on successful reconcile (FR-008); maps to status.observedGeneration
    LastAppliedRevision *string      // git revision, e.g. "main@sha1:a1b2c3d"; nil = leave unchanged
    Conditions          []*Condition // full replacement of the conditions slice; nil = leave unchanged
    // resolved is NOT part of the generic patch — it is kind-specific
}

// Condition mirrors the common condition shape from the GraphQL schema (issue #40).
type Condition struct {
    Type               string
    Status             string    // "True", "False", or "Unknown"
    ObservedGeneration int64
    LastTransitionTime time.Time
    Reason             string
    Message            string
}

// IsNoOp returns true if every non-nil field in p already equals its observed value.
// Callers MUST skip the API call when IsNoOp returns true (FR-007, SC-004).
func (p *StatusPatch) IsNoOp(current ResourceStatus) bool
```

## StatusClient Interface

```go
// StatusClient is the interface reconcilers use to write status.
// Injected into reconciler constructors — not passed to Reconcile().
type StatusClient interface {
    // Apply writes the patch. Returns ErrConflict if the resource's
    // resourceVersion no longer matches, nil on success.
    Apply(ctx context.Context, key types.WorkItemKey, patch *StatusPatch) error
}
```

## Errors

| Error | Meaning | Reconciler action |
|-------|---------|-------------------|
| `types.ErrConflict` | `resourceVersion` mismatch — resource updated concurrently | Return `ResultTransient(err)` |
| other `error` | Network/API error | Return `ResultTransient(err)` |
| `nil` | Success | Return `ResultOK()` |

## Partial-Merge Semantics

- Only fields with non-nil pointer values are included in the request body sent to `gitstore-api`.
- All other `.status` sub-fields are left unchanged — multiple reconcilers can own distinct status sub-fields without coupling.
- Full-replace semantics are NOT used.

## Optimistic Concurrency

- `ResourceVersion` is always included in the request.
- If the stored `resourceVersion` on the API side does not match, the API returns a conflict response.
- The `StatusClient` implementation wraps this into `types.ErrConflict`.
- The reconciler propagates this as `TransientFailure`, causing the item to be retried with fresh state from the (re-populated) cache.

## Idempotent Write Suppression (FR-007)

```go
patch := &StatusPatch{
    ResourceVersion:    obj.Meta.ResourceVersion,
    Ready:              ptr(true),
    ObservedGeneration: &obj.Meta.Generation,
}
if patch.IsNoOp(obj.Status) {
    return types.ResultOK() // skip — zero API calls under steady state (SC-004)
}
if err := statusClient.Apply(ctx, key, patch); err != nil { ... }
```

## ObservedGeneration Rule (FR-008)

On every successful reconcile, `ObservedGeneration` MUST be set to the value of `metadata.generation` observed at the start of the reconcile cycle. This prevents the watch event from re-enqueuing the resource after a status-only write (feedback-loop suppression).

## ResourceStatus (observer-side type)

```go
// ResourceStatus holds the current observed status values read from the cache.
// Used by IsNoOp for comparison. Mirrors the common status shape from issue #40.
type ResourceStatus struct {
    ObservedGeneration  int64
    LastAppliedRevision string
    Conditions          []*Condition
    // resolved is NOT included — kind-specific, out of scope for generic patch
}
```
