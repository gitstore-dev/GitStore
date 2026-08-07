# Contract: Runner[T] — List-Then-Watch Orchestration

**Package**: `github.com/gitstore-dev/gitstore/controller-manager/internal/listwatch`
**Stability**: New in this spec. `Runner[T]` is constructed and started by `cmd/controller/main.go`, one instance per registered kind, alongside (not inside) `Manager.Register`/`Manager.Start`.

## Interface

```go
type Runner[T any] struct {
    Kind         string
    ListWatcher  ListWatcher[T]
    Cache        *cache.Cache[T]           // mutable — Runner is the sole writer (Set/Delete/MarkSynced)
    Store        checkpoint.Store
    Enqueue      func(types.WorkItemKey) error
    KeyFunc      func(T) types.WorkItemKey
    RevisionFunc func(T) string

    FlushIntervalEvents int
    MaxBackoff           time.Duration

    Log *zap.Logger
}

// Run blocks until ctx is cancelled or an unrecoverable error occurs.
// Exactly one Run call MUST be active per Kind at a time (FR-012) — the
// caller (main.go) is responsible for not starting a second Runner for the
// same kind; Runner itself does not defend against this.
func (r *Runner[T]) Run(ctx context.Context) error
```

## Dispatch-Side Obligations (mirrors spec 026's Reconciler dispatch contract)

1. `Run` MUST NOT call `Cache.MarkSynced()` until the bootstrap `List` (or a resume/re-list) has fully populated the cache via `Cache.Set` for every returned item (FR-002).
2. `Run` MUST NOT call `Enqueue` for any key before `Cache.MarkSynced()` has been called for that kind (FR-002) — this is the producer-side half of the same gate `manager.runDispatchLoop` already enforces on the consumer side via `syncChecker`.
3. A resource returned by the bootstrap `List` and also delivered as an `Added` `WatchEvent` for the same key during the list-to-watch transition window MUST be enqueued exactly once, not twice (FR-003, SC-008). Mechanism: `Run` retains the set of keys enqueued during bootstrap until the first watch event with a `ResourceVersion` strictly newer than the list's `ResourceVersion` is observed for that key; an `Added` event whose `ResourceVersion` matches (or is not newer than) the list snapshot is treated as a duplicate and only updates the cache (`Cache.Set`), without a second `Enqueue`.
4. `Bookmark` events MUST update `currentRV` and, at the next flush boundary, the persisted checkpoint — MUST NOT call `Enqueue` (FR-010).
5. `Deleted` events MUST call `Cache.Delete` before `Enqueue`, so a reconciler that reads the cache after dequeue observes the deletion (mirrors spec 026's `CacheAccessor.Get` returning `(zero, false)` → `TerminalFailure` convention).
6. A resource deleted between `List` completing and `Watch` opening MUST NOT be enqueued from the list snapshot once the corresponding `Deleted` event arrives — `Run` MUST apply `Cache.Delete` and skip/cancel the pending enqueue for that key if it has not yet been dispatched (spec Edge Cases). In practice: the `Deleted` event's `Cache.Delete` + `Enqueue` still fires (the reconciler will observe `(zero, false)` from its cache read and return `TerminalFailure`/no-op) — `Run` does not need special-case suppression here because the reconcile contract already handles "key present in queue, absent in cache" correctly.

## Checkpoint Obligations

1. Exactly one `Store.Load` call per `Run` invocation, at entry, to decide bootstrap vs. resume (FR-007, FR-008).
2. `Store.Save` at every `FlushIntervalEvents`-th processed event and once more on `ctx.Done()` (best-effort — a failed final flush on shutdown is logged, not retried indefinitely, since the process is exiting) (FR-005).
3. On `Store.Save` failure at a flush boundary, `Run` MUST retry with backoff and MUST NOT consume the next `WatchEvent` from `Watcher.Events()` until a `Save` succeeds (backpressure — FR-005, SC-004). `Run` MUST increment `health.CheckpointWriteFailuresTotal{kind}` on every failed attempt and MUST NOT halt the process or the loop entirely — only pause event consumption (FR-013 edge case: "continues processing events without halting").
4. On successful `Save`, `Run` MUST set `health.CheckpointLastWriteTimestamp{kind}` to the current Unix timestamp.

## Reconnect / Expiry Obligations

1. A transient `Watcher` close (`Err()` not `ErrWatchExpired`, including a `nil` `Err()` from a clean `Stop()` during shutdown) triggers a reconnect at the in-memory `currentRV` — MUST NOT call `Store.Load` (FR-011, research.md §5).
2. Reconnect backoff MUST be exponential, capped at `MaxBackoff`, and MUST be retried indefinitely (not bounded by an attempt count) until `ctx` is cancelled — connection retries are architecturally distinct from the bounded-attempt `internal/retry` package used for per-item reconcile retries (research.md §3 in plan.md's Technical Context; a connection that never recovers should not "quarantine," it should keep trying).
3. An `ErrWatchExpired` close triggers: discard in-memory `currentRV` and any pending flush state → re-list (retried with backoff per FR-014's pattern, reusing the bootstrap retry path) → for each re-listed item, `Cache.Set` always; `Enqueue` only if `RevisionFunc(item)` differs from the previously cached object's revision (or the key was absent) → `Store.Save` the new list's `ResourceVersion` → resume `Watch` at the new cursor (FR-009, US3 AC2).
4. Repeated expiry (the re-list's own subsequent `Watch` immediately expiring again) MUST NOT lose work items — each re-list cycle is independently correct via the same diff-and-enqueue logic (US3 AC3); `Run`'s single-loop structure means repeated expiry simply repeats step 3 with no additional state to reset.

## Test Fixtures Expected

- `stubListWatcher[T]` — in-memory `ListWatcher[T]` implementation for tests, configurable to: return a fixed `ListResponse[T]`, fail N times before succeeding (to test FR-014 retry), and produce a scripted sequence of `WatchEvent[T]` (including `Bookmark` and a forced `ErrWatchExpired` close) via a test-controlled channel.
- Mirrors the existing test-double convention in `tests/contract/` (e.g. `stubReconciler`, `stateReadingReconciler` from spec 026) — no mocking framework, hand-written structs implementing the interface directly.
