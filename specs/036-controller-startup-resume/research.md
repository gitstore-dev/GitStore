# Research: Controller Startup Resume (spec 036)

## 1. ListWatcher Transport Abstraction

**Decision**: Define a minimal generic pair of interfaces — `ListWatcher[T]` (constructs a `Watcher[T]` and performs the initial `List`) and `Watcher[T]` (an open watch stream: `Events() <-chan WatchEvent[T]`, `Err() error`, `Stop()`) — in a new `internal/listwatch` package. No concrete GraphQL/gRPC transport is implemented in this spec.

**Rationale**: The spec's own Assumptions section states the concrete transport (GraphQL subscription vs. dedicated watch endpoint) "is not specified here," and research confirms this is accurate: `gitstore-api`'s GraphQL schema (`shared/schemas/*.graphqls`) has no `type Subscription` anywhere, and `gitstore-controller-manager` has zero existing GraphQL client code — `Config.Controller.ApiURI` is read into config but never dialed. Building a concrete transport would require schema work on the `gitstore-api` side that is out of scope for this spec (not listed in Dependencies) and would risk the plan quietly expanding beyond issue #182. The interface-first approach lets `internal/listwatch` and its `Runner[T]` be fully tested against an in-memory stub `ListWatcher[T]` (mirroring how spec 026 tested `Reconciler` against `stubReconciler`), while the concrete GraphQL-subscription-backed implementation is deferred to whichever spec first needs a real `T` (e.g. issue #244, CategoryTaxonomy Controller Reconciliation) or a dedicated "watch transport" spec.

**Design**:
```go
// internal/listwatch/types.go
type EventType int
const (
    Added EventType = iota
    Modified
    Deleted
    Bookmark
)

type WatchEvent[T any] struct {
    Type            EventType
    Object          T       // zero value for Bookmark
    ResourceVersion string
}

type ListResponse[T any] struct {
    Items           []T
    ResourceVersion string
}

var ErrWatchExpired = errors.New("watch cursor expired: event log compacted")

// internal/listwatch/listwatcher.go
type Watcher[T any] interface {
    Events() <-chan WatchEvent[T]
    Err() error   // non-nil (possibly ErrWatchExpired) after Events() closes
    Stop()
}

type ListWatcher[T any] interface {
    List(ctx context.Context) (ListResponse[T], error)
    Watch(ctx context.Context, resourceVersion string) (Watcher[T], error)
}
```

**Alternatives considered**: A single `Sync(ctx, resourceVersion string) (<-chan WatchEvent[T], error)` method that transparently does list-then-watch internally — rejected because US1's independent test explicitly requires listing and watching to be separately observable/testable steps ("stub API that serves a static list response... no watch stream... required"), and FR-007 requires the caller (the `Runner`) to skip `List` entirely on resume, which is only expressible if `List` and `Watch` are separate calls.

---

## 2. Runner Orchestration Loop & Single-Active-Loop Guarantee (FR-012)

**Decision**: One `Runner[T]` per registered kind, running its entire lifecycle (bootstrap-or-resume → watch loop → reconnect/expiry recovery) on a single dedicated goroutine, owned by `main.go` the same way `Manager.Start` owns one dispatch goroutine per kind.

**Rationale**: FR-012 requires "at most one active list-or-watch loop at a time" per kind and coalesced expiry recoveries. A single-goroutine-per-kind design satisfies this by construction — there is no second goroutine that could concurrently open a competing `Watch` or trigger a second re-list, so no mutex, singleflight, or leader-election-style primitive is needed. This mirrors the existing `runDispatchLoop` pattern (`internal/manager/manager.go`) where one goroutine per kind already owns dequeue+dispatch. Concurrent *multi-instance* operation (two controller-manager processes) is explicitly out of scope per the spec's Assumptions ("leader election... out of scope").

**Design**: `Runner[T].Run(ctx context.Context) error` is a blocking call, spawned once per kind from `main.go` in its own goroutine (parallel to, not part of, `Manager.Start`). Internally it is a single `for` loop: bootstrap-or-resume once, then loop on `select { case ev := <-watcher.Events(): ...; case <-ctx.Done(): flush and return }`; on channel close, check `watcher.Err()` to decide between `ErrWatchExpired` (→ re-list) and any other error / nil (→ reconnect with backoff, same `resourceVersion` cursor).

**Alternatives considered**: A supervisor + worker-goroutine split (like spec 026's deferred `HotRegister` channel design) — rejected as premature; spec 026 itself deferred that complexity because there was no concrete need, and the same reasoning applies here: kinds are statically registered before `Start()`, there is no hot-add-a-kind requirement in this spec.

---

## 3. Checkpoint Backpressure Mechanism (FR-005, SC-004)

**Decision**: The `Runner`'s event-processing loop does not `case ev := <-watcher.Events()` again until the most recent flush attempt (at the configured interval boundary) has succeeded. A failed `Store.Save` retries in a tight backoff loop *before* the loop proceeds to read the next event.

**Rationale**: This was clarified explicitly in the spec (see spec.md Clarifications, session 2026-08-06): SC-004's "replay window is bounded and never unbounded" is only true if the controller stops advancing its position once persistence can no longer keep up. Because `Watcher[T]` is a channel, "pausing dispatch" is naturally expressed as "don't drain the channel" — the transport implementation (whatever it turns out to be) is responsible for its own internal buffering/backpressure once the controller stops calling `Events()`; the `Runner` itself does no polling or busy-waiting beyond the retry-backoff sleep between `Save` attempts.

**Design**: Pseudocode inside the watch loop:
```go
eventsSinceFlush++
if eventsSinceFlush >= flushInterval {
    for {
        if err := store.Save(ctx, record); err != nil {
            health.CheckpointWriteFailuresTotal.WithLabelValues(kind).Inc()
            log.Warn("checkpoint write failed; pausing watch consumption", zap.Error(err))
            select {
            case <-ctx.Done():
                return ctx.Err()
            case <-time.After(backoff.NextBackOff()):
                continue
            }
        }
        health.CheckpointLastWriteTimestamp.WithLabelValues(kind).Set(float64(time.Now().Unix()))
        eventsSinceFlush = 0
        break
    }
}
```

**Alternatives considered**: Best-effort (advance in-memory checkpoint regardless of write success, accept unbounded replay on crash) — this was presented as an explicit option in clarification and rejected by the user specifically because it contradicts SC-004 as written. Circuit-breaker (halt entirely after M consecutive failures) — also presented and rejected in favor of the simpler always-retry backpressure model; nothing in the accepted answer calls for a hard stop, and a hard stop would conflict with FR-013's requirement that "the controller continues processing events without halting" (edge case: a single write failure increments the counter but does not halt — sustained failure delays, it does not stop, consumption).

---

## 4. Filesystem CheckpointStore — One File Per Kind, Atomic Write

**Decision**: `FilesystemStore{Dir string}`; `Save(ctx, Record)` writes JSON to `os.CreateTemp(dir, "<kind>.checkpoint.*.tmp")`, `fsync`s, then `os.Rename` to `<dir>/<kind>.checkpoint.json`. `Load(ctx, kind)` reads and unmarshals `<dir>/<kind>.checkpoint.json`; any error (not-exist, permission, malformed JSON) is returned as-is — the `Runner` treats every `Load` error identically per FR-008 ("missing, unreadable, or corrupt").

**Rationale**: Confirmed via clarification (spec.md Clarifications) that each kind gets its own file rather than one combined file — this directly satisfies FR-012's per-kind independence (a write or corruption for one kind's file structurally cannot affect another kind's file) and keeps `Save`/`Load` trivially generic over `T` (the store itself is untyped — it persists `Record{Kind, ResourceVersion, WrittenAt}`, not `T`). `os.Rename` within the same directory is atomic on POSIX filesystems (the standard write-temp-then-rename idiom already implied by FR-006 and the edge cases section), avoiding any need for file locking. `CheckpointDir` is a single new config field (`ControllerConfig.CheckpointDir`), consistent with the spec's Assumption that "no new top-level environment variables are introduced" — it lives under the existing `GITSTORE_CONTROLLER__` prefix.

**Alternatives considered**: A single combined JSON file (map of kind → Record) — presented as an explicit option in clarification and rejected in favor of one-file-per-kind, specifically because a single file means every write (regardless of which kind changed) rewrites the whole file, and a single corrupt read affects every registered kind rather than just one.

---

## 5. Reconnect vs. Restart — Which Checkpoint Value To Resume From (FR-011)

**Decision**: A transient watch-stream disconnect within the same controller-manager process (network blip, server restart on the API side) reconnects using the `Runner`'s in-memory `currentRV` field — updated on every event per FR-004 — never the last value written to `CheckpointStore`. Only an actual process restart (a new `Runner` constructed from scratch, calling `Store.Load` at `Run()` entry) resumes from the persisted checkpoint.

**Rationale**: This was the second explicit clarification in the session. The spec's original wording ("resume from the last persisted checkpoint") was ambiguous about same-process reconnects, and since `currentRV` is updated synchronously on every watch event (FR-004) while `Store.Save` only happens every N events (FR-005), the in-memory value is always >= the persisted value. Resuming a same-process reconnect from the (older) persisted value would deterministically replay up to N already-handled events on every transient network blip — a correctness regression relative to just holding the in-memory cursor, which requires no additional state.

**Design**: `Runner` holds `currentRV string` (unexported field, updated under no additional lock — the watch loop is single-goroutine so no synchronization is needed). The reconnect path calls `listwatcher.Watch(ctx, currentRV)`, not `store.Load(ctx, kind)`. `store.Load` is called exactly once, at `Run()` entry, before the loop starts.

---

## 6. Expiry Recovery and FR-012 Coalescing

**Decision**: No explicit coalescing primitive (mutex, singleflight, dedup-token) is introduced for concurrent expiry recoveries. The single-goroutine-per-kind `Runner` design (research.md §2) makes concurrent expiry recovery for the same kind structurally impossible — there is only ever one call stack that could observe `ErrWatchExpired` and only one place (the top of the `Runner.Run` loop) that triggers a re-list.

**Rationale**: FR-012's requirement ("concurrent expiry recoveries for the same kind MUST be coalesced into a single re-list") reads as a safety requirement against a multi-goroutine design where two paths could each detect expiry and race to re-list. Since this plan's `Runner` never has more than one goroutine per kind, the requirement is satisfied trivially rather than through an explicit coalescing mechanism — consistent with Principle VII (Simplicity/YAGNI): don't add a mutex to solve a race that the chosen architecture doesn't have.

**Alternatives considered**: A supervisor goroutine fanning out per-kind watch loops with a shared "recovery in progress" flag per kind — rejected, this is the multi-goroutine design the single-goroutine `Runner` was chosen specifically to avoid.

---

## 7. ReplayBacklog Metric — Reuse Queue Depth, Don't Add New Bookkeeping

**Decision**: `gitstore_controller_checkpoint_replay_backlog{kind}` is set from the same per-kind queue-depth value already computed by `Manager.KindStats()` (`ks.q.Len()`), not from a new independent counter inside `Runner`.

**Rationale**: The spec's Key Entities section defines `ReplayBacklog` as "the count of events received on the watch stream that have been enqueued as work items but not yet dispatched" — this is definitionally the work queue's depth for that kind. `Manager` already tracks and exposes this exact quantity via `QueueDepth` (`internal/health/metrics.go`) for the *general* per-kind queue-depth metric; introducing a second, checkpoint-specific counter that tracks the same underlying number would be duplicate bookkeeping with a real risk of drifting out of sync (e.g. if the `Runner` incremented its own counter but the actual `Manager.Enqueue` call failed). The `Runner` reads `Manager.KindStats()[kind].QueueDepth` (or an equivalent narrow accessor) once per checkpoint metric update and republishes it under the new checkpoint-specific metric name, so operators distinguishing "checkpoint backlog" from "general queue depth" get a stable value without a second source of truth. If the two metrics need to diverge in the future (e.g. only counting items enqueued *during* a replay burst, not steady-state traffic), that is a follow-up refinement, not a blocker for this spec's SC-006.

**Alternatives considered**: A dedicated atomic counter incremented in the `Runner`'s enqueue path and decremented via a callback from `Manager.dispatch` — rejected as unjustified complexity (a second counter tracking a quantity `Manager` already tracks) given Principle VII, unless/until the metrics are shown to need different semantics.

---

## 8. Config Additions — Flush Interval as Event Count, Not Duration

**Decision**: `ControllerConfig.CheckpointFlushIntervalEvents int` (`mapstructure:"checkpoint_flush_interval_events"`, default `100`), distinct in kind from the existing `DefaultStallThresholdStr`/`MaxWatchBackoffStr` duration-string idiom.

**Rationale**: FR-005 specifies the flush trigger as "every configurable number of events (default: 100)" — an event count, not a time interval. Reusing the duration-string-plus-parsed-field idiom (`Str string` + parsed `time.Duration`) from `DefaultStallThresholdStr` would be a type mismatch; a plain `int` with a `viper` default is the correct fit and needs no post-`Unmarshal` parsing step (unlike durations, ints unmarshal directly). `MaxWatchBackoffStr`/`MaxWatchBackoff time.Duration`, by contrast, *is* duration-shaped (FR-011's "maximum backoff interval MUST be configurable") and does follow the existing idiom exactly, parsed in `validate()` the same way `DefaultStallThresholdStr` is today.

**Alternatives considered**: Time-based flush interval (flush every N seconds instead of every N events) — rejected, contradicts the spec's explicit wording and its SC-004 bound, which is defined in terms of event count, not wall-clock time.
