# Contract: ListWatcher / Watcher Transport Abstraction

**Package**: `github.com/gitstore-dev/gitstore/controller-manager/internal/listwatch`
**Stability**: New in this spec. No concrete production implementation ships here — see research.md §1. Implementors targeting a real `T` (e.g. `CategoryTaxonomy` for issue #244) provide their own type satisfying these interfaces.

## Interfaces

```go
type EventType int
const (
    Added EventType = iota
    Modified
    Deleted
    Bookmark
)

type WatchEvent[T any] struct {
    Type            EventType
    Object          T       // zero value when Type == Bookmark; MUST NOT be read
    ResourceVersion string  // always populated
}

type ListResponse[T any] struct {
    Items           []T
    ResourceVersion string  // cursor a subsequent Watch call resumes from with no gap, no dup
}

var ErrWatchExpired = errors.New("watch cursor expired: event log compacted")

type Watcher[T any] interface {
    // Events delivers watch notifications in resourceVersion order per-key
    // (cross-key interleaving is permitted). The channel is closed when the
    // watch ends (error, expiry, or Stop() called).
    Events() <-chan WatchEvent[T]

    // Err returns the reason Events() closed. Valid only after the channel is
    // closed (reading it earlier is undefined). errors.Is(Err(), ErrWatchExpired)
    // signals a compacted cursor; any other non-nil value (or nil, e.g. on a
    // clean Stop()) signals a transient/ordinary close.
    Err() error

    // Stop ends the watch. Safe to call multiple times. MUST cause Events()
    // to close (if not already closed) so callers blocked on it unblock.
    Stop()
}

type ListWatcher[T any] interface {
    // List returns a full snapshot. Implementations do not retry internally —
    // the caller (Runner) retries with exponential backoff on error (FR-014).
    List(ctx context.Context) (ListResponse[T], error)

    // Watch opens a stream starting after resourceVersion. An empty
    // resourceVersion means "from the beginning" (only used by the bootstrap
    // path immediately after a successful List, using that List's own
    // ResourceVersion — never actually empty in this spec's usage).
    Watch(ctx context.Context, resourceVersion string) (Watcher[T], error)
}
```

## Implementor Obligations

1. `List` MUST return a `ResourceVersion` such that `Watch(ctx, thatVersion)` delivers every change that occurred after the list snapshot was taken, with no gap (FR-003).
2. `Watch` MUST deliver events for a single resource key in `ResourceVersion` order. Interleaving across different keys is permitted (spec Edge Cases — ordering guarantees).
3. `Watch` MUST emit `Bookmark` events (if the underlying transport supports them) carrying only `ResourceVersion` — `Object` MUST be the zero value and callers MUST NOT dereference it.
4. When the requested `resourceVersion` has been compacted out of the transport's retained history, `Watch` (or the subsequently-closed `Watcher.Err()`) MUST return/expose an error satisfying `errors.Is(err, ErrWatchExpired)` — not a generic error — so `Runner` can distinguish "re-list required" from "just reconnect."
5. `Stop()` MUST be idempotent and MUST NOT block indefinitely.

## Caller Obligations (the `Runner`)

1. MUST NOT call `Watch` before either `List` (bootstrap) or `Store.Load` (resume) has produced a starting `resourceVersion`.
2. MUST retry `List` with exponential backoff on error, and MUST NOT call `Watch`, `Cache.MarkSynced`, or enqueue any work item until `List` succeeds (FR-014).
3. On `Watcher.Err()` satisfying `errors.Is(err, ErrWatchExpired)`, MUST discard the current checkpoint for that kind and re-list (FR-009) rather than reconnecting at the same cursor.
4. On any other `Watcher.Err()` (or nil, e.g. context cancellation), MUST reconnect via `Watch` using the in-memory cursor with exponential backoff capped at the configured maximum (FR-011) — MUST NOT re-list.
5. MUST call `Stop()` on the current `Watcher` before opening a replacement one, and on `ctx.Done()`.
