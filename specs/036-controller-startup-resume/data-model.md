# Data Model: Controller Startup Resume (spec 036)

## Entities

### Checkpoint (`Record`)

**Package**: `github.com/gitstore-dev/gitstore/controller-manager/internal/checkpoint`

**Description**: A persisted record binding a resource kind to the `resourceVersion` of the last event (or list completion) successfully processed. Spec's "Checkpoint" entity.

```go
type Record struct {
    Kind            string
    ResourceVersion string
    WrittenAt       time.Time
}
```

**Invariants**:
- `Kind` MUST be non-empty and MUST match the file/map key it is stored under.
- `ResourceVersion` is an opaque string — never parsed numerically or compared with `<`/`>` (per spec Assumptions); only equality and "cursor to resume from" semantics apply.
- `WrittenAt` is informational (used to derive checkpoint age for FR-013/US4); it is NOT used for correctness decisions.

---

### CheckpointStore

**Package**: `internal/checkpoint`

**Description**: The storage abstraction responsible for reading and writing `Record`s. Writes are atomic. Two implementations: filesystem (production) and in-memory (tests).

```go
type Store interface {
    Load(ctx context.Context, kind string) (Record, error)
    Save(ctx context.Context, rec Record) error
}
```

| Method | Behaviour |
|--------|-----------|
| `Load` | Returns a non-nil error if the record for `kind` is missing, unreadable, or corrupt. Callers (the `Runner`) treat every error identically — fall back to bootstrap (FR-008). No distinct error types are exposed for "missing" vs. "corrupt"; the spec does not require differentiated handling. |
| `Save` | MUST be atomic: the record for `kind` is either fully written and readable, or the previous record is unchanged — never partially written (FR-006). |

**Implementations**:

| Type | Storage | Isolation |
|------|---------|-----------|
| `FilesystemStore{Dir string}` | One JSON file per kind: `<Dir>/<kind>.checkpoint.json`. `Save` writes to a temp file in `Dir` then `os.Rename`s into place. | A write or corrupted file for kind A never touches kind B's file (per clarification: one file per kind). |
| `MemoryStore` | `map[string]Record` guarded by `sync.RWMutex`. | Per-process; used only in tests. |

**Relationships**: One `Store` instance is shared across all kinds (both implementations key internally by `kind`); each `Runner[T]` calls `Load`/`Save` passing its own kind name.

---

### ListResponse[T]

**Package**: `internal/listwatch`

**Description**: The result of a full list request to the API for a given kind: a snapshot of all current resources and the `resourceVersion` at the time of the snapshot.

```go
type ListResponse[T any] struct {
    Items           []T
    ResourceVersion string
}
```

**Invariants**: `ResourceVersion` MUST be the cursor value that a subsequent `Watch` call at this version will resume from with no gap and no duplicate relative to the moment the list snapshot was taken (FR-003).

---

### WatchEvent[T] / EventType

**Package**: `internal/listwatch`

**Description**: A streaming change notification delivered after a list.

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
    Object          T       // zero value when Type == Bookmark
    ResourceVersion string
}
```

**Invariants**:
- Every variant carries `ResourceVersion`; `Bookmark` carries only the version (`Object` is the zero value and MUST NOT be interpreted).
- Events for a single resource (same key) are delivered in `ResourceVersion` order; events for different resources may interleave (spec Edge Cases).
- `Bookmark` events update the checkpoint cursor but MUST NOT cause an enqueue (FR-010).

---

### ListWatcher[T] / Watcher[T]

**Package**: `internal/listwatch`

**Description**: The transport abstraction the `Runner` depends on. No concrete implementation ships in this spec (see research.md §1) — production wiring is deferred to the spec that introduces the first real `T` (e.g. issue #244).

```go
type Watcher[T any] interface {
    Events() <-chan WatchEvent[T]
    Err() error   // valid only after Events() is closed; may be ErrWatchExpired
    Stop()
}

type ListWatcher[T any] interface {
    List(ctx context.Context) (ListResponse[T], error)
    Watch(ctx context.Context, resourceVersion string) (Watcher[T], error)
}

var ErrWatchExpired = errors.New("watch cursor expired: event log compacted")
```

**Contract**:
- `List` errors are retried by the caller with exponential backoff (FR-014) — `ListWatcher` implementations do not retry internally.
- `Watch` returning a `Watcher` whose `Events()` channel closes with `Err() == ErrWatchExpired` (checked via `errors.Is`) signals a compacted cursor (FR-009); any other non-nil `Err()` (or context cancellation) signals a transient disconnect (FR-011).
- `Stop()` MUST be safe to call multiple times and MUST cause `Events()` to close if not already closed.

---

### Runner[T] (orchestration entity — the spec's "controller manager" bootstrap/resume actor)

**Package**: `internal/listwatch`

**Description**: Per-kind orchestration loop. Owns the *mutable* `*cache.Cache[T]` (distinct from the read-only `CacheAccessor[T]` reconcilers receive per spec 026) and drives `Manager.Enqueue`. Exactly one `Runner[T]` instance runs per registered kind, on a single dedicated goroutine (FR-012, research.md §2/§6).

```go
type Runner[T any] struct {
    Kind            string
    ListWatcher     ListWatcher[T]
    Cache           *cache.Cache[T]
    Store           checkpoint.Store
    Enqueue         func(types.WorkItemKey) error
    KeyFunc         func(T) types.WorkItemKey
    RevisionFunc    func(T) string   // extracts resourceVersion from an object, for expiry-recovery diffing

    FlushIntervalEvents int            // default 100
    MaxBackoff           time.Duration // default 30s

    Log *zap.Logger
    // currentRV (unexported) — in-memory watch cursor, source of truth for
    // same-process reconnects (FR-011); distinct from the last value persisted
    // to Store, which may lag by up to FlushIntervalEvents-1 events.
}

func (r *Runner[T]) Run(ctx context.Context) error
```

**State machine**:

```
Run(ctx) entry
  │
  ├─ Store.Load(kind) succeeds ──────────────► resume: skip to WATCH at loaded ResourceVersion
  │
  └─ Store.Load(kind) fails (missing/corrupt) ─► BOOTSTRAP
                                                    │
                                                    ▼
BOOTSTRAP: retry List with backoff until success (FR-014; never MarkSynced/enqueue/watch before success)
  → cache.Set every item → cache.MarkSynced() → Enqueue every listed key (FR-001, FR-002)
  → Store.Save(ResourceVersion from list)
  → currentRV = list.ResourceVersion
  → fall through to WATCH

WATCH(currentRV): open Watch(ctx, currentRV)
  loop: select on Events()
    Added/Modified   → cache.Set    → Enqueue(key)   → currentRV = event.ResourceVersion
    Deleted          → cache.Delete → Enqueue(key)   → currentRV = event.ResourceVersion
    Bookmark         →                (no enqueue)   → currentRV = event.ResourceVersion
    (every branch)   → eventsSinceFlush++; if >= FlushIntervalEvents: flushWithBackoff (blocks further
                        event consumption until Store.Save succeeds — FR-005, SC-004)
    ctx.Done()       → best-effort final flush → return
  Events() channel closes:
    Err() is ErrWatchExpired → discard checkpoint → BOOTSTRAP-style re-list, but only enqueue items whose
                                 RevisionFunc differs from the cached value (FR-009, US3 AC2) → WATCH(new RV)
    Err() is anything else / nil (incl. ctx cancellation) → reconnect: WATCH(currentRV) with exponential
                                 backoff capped at MaxBackoff (FR-011) — uses in-memory currentRV, NOT
                                 Store.Load (research.md §5)
```

**Invariants**:
- No `Enqueue` call happens before `Cache.MarkSynced()` for that kind (FR-002, mirrors the existing `syncChecker` gate in `manager.runDispatchLoop` — this is the producer-side half of the same guarantee).
- A resource present in both the bootstrap `List` response and an early `Watch` `Added` event for the same key is enqueued exactly once across the transition (SC-008) — the `Runner` tracks the set of keys enqueued during bootstrap and suppresses a duplicate `Added` enqueue for a key it just listed, until the first watch event for that key that is *not* a duplicate of the list snapshot arrives (see contracts/runner-contract.md for the precise suppression window).
- `Runner` never enqueues from a `Bookmark` event.
- `Runner` never resumes a same-process reconnect from `Store.Load` — only from `currentRV`.

---

## Config Additions (`internal/config.ControllerConfig`)

| Field | Type | mapstructure key | Env var | Default | Notes |
|-------|------|-------------------|---------|---------|-------|
| `CheckpointDir` | `string` | `checkpoint_dir` | `GITSTORE_CONTROLLER__CHECKPOINT_DIR` | `.gitstore/checkpoints` | Directory for `FilesystemStore`; created with `os.MkdirAll` at startup if absent |
| `CheckpointFlushIntervalEvents` | `int` | `checkpoint_flush_interval_events` | `GITSTORE_CONTROLLER__CHECKPOINT_FLUSH_INTERVAL_EVENTS` | `100` | Event count, not a duration (research.md §8) |
| `MaxWatchBackoffStr` / `MaxWatchBackoff` | `string` / `time.Duration` | `max_watch_backoff` / `-` | `GITSTORE_CONTROLLER__MAX_WATCH_BACKOFF` | `30s` | Parsed in `validate()`, same idiom as `DefaultStallThresholdStr` |

No new env var prefix — all three live under the existing `GITSTORE_CONTROLLER__` namespace, per the spec's Assumptions.

## Prometheus Metrics Additions (`internal/health/metrics.go`)

| Metric | Type | Labels | Set/incremented when |
|--------|------|--------|----------------------|
| `gitstore_controller_checkpoint_last_write_timestamp_seconds` | Gauge | `kind` | Every successful `Store.Save` — set to `time.Now().Unix()` |
| `gitstore_controller_checkpoint_write_failures_total` | Counter | `kind` | Every failed `Store.Save` attempt inside `flushWithBackoff` |
| `gitstore_controller_checkpoint_replay_backlog` | Gauge | `kind` | Mirrors the kind's existing queue-depth value from `Manager.KindStats()` (research.md §7) — not independently tracked |

No changes to `internal/health/handler.go`'s `/health` JSON response or `internal/api/poison.go` — Prometheus-only exposure per the spec's clarification.

## Relationships Diagram

```
cmd/controller/main.go
  ├─ mgr := manager.New()...            (spec 025/026, unchanged)
  ├─ store := checkpoint.NewFilesystemStore(cfg.Controller.CheckpointDir)
  └─ for each kind:
       c := cache.New[T]()
       mgr.Register(manager.ReconcilerRegistration{Kind: kind, Cache: c, Reconciler: ...})
       runner := &listwatch.Runner[T]{
           Kind: kind, Cache: c, Store: store,
           Enqueue: func(k types.WorkItemKey) error { return mgr.Enqueue(k) },
           ListWatcher: <transport impl — out of scope for this spec>,
           FlushIntervalEvents: cfg.Controller.CheckpointFlushIntervalEvents,
           MaxBackoff: cfg.Controller.MaxWatchBackoff,
       }
       go runner.Run(ctx)   // parallel to, not part of, mgr.Start(ctx)
  mgr.Start(ctx)                          (blocks; dispatch loops gated on c.HasSynced())

Runner[T]
  ├─ owns *cache.Cache[T]    (mutable: Set/Delete/MarkSynced — Runner is the sole writer)
  ├─ owns checkpoint.Store   (Load once at Run() entry; Save every FlushIntervalEvents events + on shutdown)
  ├─ owns currentRV string   (in-memory cursor; source of truth for reconnects)
  └─ calls Enqueue(WorkItemKey)  →  manager.Queue (existing, unchanged)
```
