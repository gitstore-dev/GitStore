# Implementation Plan: Controller Startup Resume — List-Then-Watch and resourceVersion Checkpointing

**Branch**: `036-controller-startup-resume` | **Date**: 2026-08-06 | **Spec**: [spec.md](spec.md)
**Input**: Feature specification from `/specs/036-controller-startup-resume/spec.md`

## Summary

Extend `gitstore-controller-manager` with a list-then-watch bootstrap sequence and `resourceVersion` checkpointing so the controller can populate its informer cache on first start, resume a watch stream from a persisted checkpoint after a restart, recover from compacted watch cursors by re-listing, and expose checkpoint health via Prometheus metrics. The primary new components are a generic `ListWatcher[T]` abstraction (the first "talk to the API" interface for list/watch, greenfield per spec's own Assumptions), a per-kind `Runner[T]` orchestration loop that owns the mutable `*cache.Cache[T]` and drives `Manager.Enqueue`, and a pluggable `CheckpointStore` (filesystem: one atomically-written file per kind; in-memory for tests). No changes to the `Reconciler`/`ReconcileResult` contract from spec 026 — this spec is purely a producer of `WorkItemKey`s into the existing queue.

## Technical Context

**Language/Version**: Go 1.25
**Primary Dependencies**: `go.uber.org/zap`, `github.com/cenkalti/backoff/v5 v5.0.3` (unbounded reconnect backoff, distinct usage from the bounded-attempt `internal/retry` package), `github.com/prometheus/client_golang v1.23.2`, `github.com/spf13/viper v1.21.0` — no new external dependencies
**Storage**: Filesystem checkpoint files (new — first persistence introduced in this module; one JSON file per registered kind, atomic write-temp-then-rename) + in-memory backend for tests
**Testing**: `go test ./...`; contract tests in `tests/contract/`, unit tests co-located in `internal/`
**Target Platform**: Linux server (controller manager binary)
**Project Type**: Service (internal Go packages consumed by `cmd/controller/main.go`)
**Performance Goals**: Resume begins dispatching within 5s with no re-list (SC-001); cold bootstrap of 10,000 resources completes within 60s (SC-002); checkpoint writes complete in <50ms under normal load (SC-005); expiry recovery resumes within 30s (SC-003); reconnect completes within the configured backoff window, default max 30s (SC-007)
**Constraints**: Replay window never exceeds the configured flush interval (default 100 events), even under sustained checkpoint-write failures — enforced via backpressure, not best-effort (SC-004); zero duplicate work items enqueued across a single list-to-watch transition (SC-008)
**Scale/Scope**: One controller-manager process; one `Runner[T]` goroutine per registered kind; O(10,000) resources per kind at bootstrap

## Constitution Check

*GATE: Must pass before Phase 0 research. Re-check after Phase 1 design.*

| Principle | Status | Notes |
|-----------|--------|-------|
| I. Test-First | ✅ | Contract tests written before implementation for `checkpoint`, `listwatch` packages. Existing `tests/contract/health_test.go` extended with checkpoint-metric assertions |
| II. API-First | ✅ | `contracts/` defines `CheckpointStore`, `ListWatcher[T]`/`Watcher[T]`, and the `Runner` obligations contract before implementation |
| III. Clear Contracts | ✅ | New interfaces are additive; no breaking change to `Reconciler`, `ReconcileResult`, or `syncChecker` from specs 025/026 (per this spec's own Assumptions) |
| IV. Observability | ✅ | Structured logging on every list/watch/checkpoint transition; three new Prometheus metrics (FR-013) follow the existing `gitstore_controller_<noun>{kind}` naming convention |
| V. User Story Driven | ✅ | P1=US1 (bootstrap list) + US2 (resume/checkpoint), P2=US3 (expiry recovery), P3=US4 (observability). Tasks labelled US1–US4 |
| VI. Incremental Delivery | ✅ | US1+US2 (P1) deliver a working bootstrap+resume cycle without US3/US4. US3 and US4 are additive |
| VII. Simplicity/YAGNI | ✅ | No new external dependencies. Single-goroutine-per-kind `Runner` design satisfies FR-012 (one active list-or-watch loop per kind) by construction — no mutex/singleflight primitive needed. `ReplayBacklog` metric aliases the existing per-kind queue depth rather than introducing separate bookkeeping (see research.md §7) |

**Post-design re-check**: No violations. `internal/checkpoint` and `internal/listwatch` are new packages justified by the `CheckpointStore` and `ListWatcher[T]`/`Runner[T]` entities (FR-001–FR-015). No fourth service introduced — all additions are additive to the existing `gitstore-controller-manager` module.

## Project Structure

### Documentation (this feature)

```text
specs/036-controller-startup-resume/
├── plan.md              ✅ (this file)
├── research.md          ✅ Phase 0 output
├── data-model.md         ✅ Phase 1 output
├── quickstart.md         ✅ Phase 1 output
├── contracts/
│   ├── checkpoint-store-api.md     ✅ Phase 1 output
│   ├── listwatcher-interface.md    ✅ Phase 1 output
│   └── runner-contract.md          ✅ Phase 1 output
└── tasks.md             ⬜ Phase 2 output (/speckit.tasks command)
```

### Source Code

```text
gitstore-controller-manager/
├── internal/
│   ├── checkpoint/
│   │   ├── checkpoint.go             NEW: Record type, Store interface
│   │   ├── filesystem.go             NEW: FilesystemStore — one file per kind, atomic write-temp-then-rename
│   │   └── memory.go                 NEW: MemoryStore — mutex-guarded map, for tests
│   ├── listwatch/
│   │   ├── types.go                  NEW: ListResponse[T], WatchEvent[T], EventType, ErrWatchExpired
│   │   ├── listwatcher.go            NEW: ListWatcher[T], Watcher[T] interfaces
│   │   └── runner.go                 NEW: Runner[T] — bootstrap list, resume, checkpoint flush/backpressure,
│   │                                       reconnect, expiry-recovery re-list, cache Set/Delete/MarkSynced
│   ├── config/
│   │   └── config.go                 MODIFY: add CheckpointDir, CheckpointFlushIntervalEvents,
│   │                                       MaxWatchBackoffStr/MaxWatchBackoff
│   ├── health/
│   │   └── metrics.go                MODIFY: add CheckpointLastWriteTimestamp, CheckpointReplayBacklog,
│   │                                       CheckpointWriteFailuresTotal
│   ├── cache/, manager/, queue/,
│   │   worker/, retry/, status/,
│   │   types/, api/                  unchanged
├── tests/
│   ├── checkpoint/
│   │   ├── filesystem_test.go        NEW: atomic write, corrupt/missing fallback, one-file-per-kind isolation
│   │   └── memory_test.go            NEW
│   └── contract/
│       ├── listwatch_bootstrap_test.go     NEW: US1 — full list, MarkSynced, enqueue-all, no-dup at transition
│       ├── listwatch_resume_test.go        NEW: US2 — checkpoint resume, flush interval, backpressure
│       ├── listwatch_expiry_test.go        NEW: US3 — ErrWatchExpired → re-list → diff-enqueue
│       └── health_test.go                  MODIFY: add checkpoint metric assertions
└── cmd/
    └── controller/
        └── main.go                   MODIFY: construct checkpoint.FilesystemStore + listwatch.Runner[T]
                                             per kind before mgr.Start(ctx)
```

**Structure Decision**: Single project (`gitstore-controller-manager`). All new code is additive within two new internal packages (`checkpoint/`, `listwatch/`) plus small extensions to `config/` and `health/`. No new external dependencies, no new service/module.

## Complexity Tracking

No constitution violations. No complexity justifications required.

## Implementation Phases

### Phase 1 — Checkpoint Store (US2 foundation — P1)

Goal: Pluggable, atomic, per-kind checkpoint persistence.

**Files**: `internal/checkpoint/checkpoint.go` (new), `internal/checkpoint/filesystem.go` (new), `internal/checkpoint/memory.go` (new), `internal/config/config.go`

**Key changes**:
1. `Record{Kind, ResourceVersion string; WrittenAt time.Time}` and `Store` interface (`Load`, `Save`) in `checkpoint.go`.
2. `FilesystemStore{Dir string}` — `Save` writes to `os.CreateTemp(dir, kind+".tmp-*")`, `json.Marshal`, `os.Rename` into `<kind>.checkpoint.json`. `Load` returns a non-nil error for missing, unreadable, or corrupt (JSON-unmarshal-failure) files — callers treat *any* `Load` error identically (fall back to list-then-watch), matching FR-008.
3. `MemoryStore` — mutex-guarded `map[string]Record`, for tests (mirrors `internal/cache.Cache` style).
4. `ControllerConfig.CheckpointDir string` (default `.gitstore/checkpoints`), env `GITSTORE_CONTROLLER__CHECKPOINT_DIR`.

**Test-first**: `TestFilesystemStore_SaveThenLoad_RoundTrips`, `TestFilesystemStore_AtomicWrite_NoPartialFileOnCrash` (verify temp file never observed at final path), `TestFilesystemStore_CorruptFile_ReturnsError`, `TestFilesystemStore_MissingFile_ReturnsError`, `TestFilesystemStore_OneFilePerKind_Isolated` (writing kind A never touches kind B's file), `TestMemoryStore_SaveThenLoad`.

### Phase 2 — ListWatcher Abstraction & Bootstrap Loop (US1 — P1)

Goal: Define the list/watch transport abstraction; implement the cold-start bootstrap path.

**Files**: `internal/listwatch/types.go` (new), `internal/listwatch/listwatcher.go` (new), `internal/listwatch/runner.go` (new)

**Key changes**:
1. `EventType` enum (`Added`, `Modified`, `Deleted`, `Bookmark`), `WatchEvent[T]{Type EventType; Object T; ResourceVersion string}`, `ListResponse[T]{Items []T; ResourceVersion string}`.
2. `Watcher[T]{Events() <-chan WatchEvent[T]; Err() error; Stop()}` and `ListWatcher[T]{List(ctx) (ListResponse[T], error); Watch(ctx, resourceVersion string) (Watcher[T], error)}` interfaces. `ErrWatchExpired` sentinel for compacted cursors (FR-009).
3. `Runner[T]{KeyFunc func(T) types.WorkItemKey; RevisionFunc func(T) string; ListWatcher ListWatcher[T]; Cache *cache.Cache[T]; Enqueue func(types.WorkItemKey) error; ...}`.
4. Bootstrap path: retry `List` with exponential backoff on failure (FR-014, no watch/MarkSynced/enqueue until success) → `cache.Set` every item → `cache.MarkSynced()` → enqueue a work item for every listed resource (FR-001, FR-002) → open `Watch` at the list response's `resourceVersion` (FR-003) → for the brief window between list completion and watch open, dedupe against the just-listed set so an early watch `Added` for an already-listed key is not double-enqueued (SC-008).

**Test-first**: `TestRunner_Bootstrap_ListsAndEnqueuesAll`, `TestRunner_Bootstrap_NoDispatchBeforeSynced`, `TestRunner_Bootstrap_NoDuplicateAcrossListWatchTransition`, `TestRunner_Bootstrap_ListFailure_RetriesWithBackoff_NeverMarksSynced`.

### Phase 3 — Resume, Flush Interval & Backpressure (US2 — P1)

Goal: Skip the list phase on a valid checkpoint; persist the in-memory cursor on a schedule; backpressure on write failure.

**Files**: `internal/listwatch/runner.go`, `internal/config/config.go`

**Key changes**:
1. On `Runner.Run(ctx)` start: `checkpoint.Store.Load(kind)`. Error (missing/corrupt/unreadable) → bootstrap path (Phase 2). Success → skip `List`, open `Watch` directly at the checkpointed `resourceVersion` (FR-007).
2. Every watch event updates an in-memory `currentRV` (FR-004) regardless of type (including `Bookmark`, without enqueuing — FR-010).
3. `eventsSinceFlush` counter; at `CheckpointFlushIntervalEvents` (default 100, `mapstructure:"checkpoint_flush_interval_events"`, env `GITSTORE_CONTROLLER__CHECKPOINT_FLUSH_INTERVAL_EVENTS`) and on clean shutdown (`ctx.Done()`), call `flushWithBackoff`.
4. `flushWithBackoff`: loop calling `Store.Save`; on error, increment `health.CheckpointWriteFailuresTotal`, log, backoff-sleep, retry — the event-processing loop does not read the next event from `Watcher.Events()` until a save succeeds, so replay never exceeds the flush interval even under a sustained outage (FR-005, SC-004). On success, update `health.CheckpointLastWriteTimestamp` and reset the counter.
5. `MaxWatchBackoffStr`/`MaxWatchBackoff time.Duration` config (default `30s`, same duration-string idiom as `DefaultStallThresholdStr`), env `GITSTORE_CONTROLLER__MAX_WATCH_BACKOFF`.
6. Transient watch-stream disconnects (network error, not `ErrWatchExpired`) reconnect using `currentRV` (in-memory), never the last-persisted value, with unbounded exponential backoff capped at `MaxWatchBackoff` (FR-011).

**Test-first**: `TestRunner_Resume_SkipsListPhase`, `TestRunner_Resume_WatchesFromCheckpointedVersion`, `TestRunner_Flush_PersistsAfterNEvents`, `TestRunner_Flush_PersistsOnCleanShutdown`, `TestRunner_Backpressure_PausesOnWriteFailure_ResumesOnSuccess`, `TestRunner_Backpressure_BoundedReplayWindow`, `TestRunner_TransientReconnect_UsesInMemoryCheckpoint_NotPersisted`, `TestRunner_TransientReconnect_BackoffCappedAtMax`.

### Phase 4 — Expiry Recovery (US3 — P2)

Goal: Detect a compacted watch cursor, re-list, enqueue only changed resources, resume watching.

**Files**: `internal/listwatch/runner.go`

**Key changes**:
1. `Watcher.Err()` returning `ErrWatchExpired` (or an `errors.Is` match) after the events channel closes triggers: discard the in-memory/persisted checkpoint for the kind, run `List` again (retried with backoff per FR-014's pattern), write a fresh checkpoint from the new list's `resourceVersion` (FR-009).
2. Re-list diff: for each item in the new `ListResponse`, compare `RevisionFunc(item)` against the cached object's revision via `cache.Get` — enqueue only if absent or the revision differs (US3 AC2); always `cache.Set` regardless, to keep the cache accurate.
3. Single-goroutine `Runner.Run` loop means expiry recovery is inherently serialized per kind — no explicit coalescing primitive is added (FR-012 satisfied by construction; see research.md §6).

**Test-first**: `TestRunner_ExpiryRecovery_DiscardsCheckpointAndRelists`, `TestRunner_ExpiryRecovery_EnqueuesOnlyChangedResources`, `TestRunner_ExpiryRecovery_RepeatedExpiry_NoLostWorkItems`.

### Phase 5 — Checkpoint Observability (US4 — P3)

Goal: Expose checkpoint age, replay backlog, and write-failure count via the existing Prometheus surface.

**Files**: `internal/health/metrics.go`, `internal/listwatch/runner.go`

**Key changes**:
1. `CheckpointLastWriteTimestamp = promauto.NewGaugeVec({Name: "gitstore_controller_checkpoint_last_write_timestamp_seconds"}, []string{"kind"})` — set to `float64(time.Now().Unix())` on every successful `Save` (operators compute age via `time() - metric`, standard Prometheus idiom).
2. `CheckpointWriteFailuresTotal = promauto.NewCounterVec({Name: "gitstore_controller_checkpoint_write_failures_total"}, []string{"kind"})` — incremented once per failed `Save` attempt inside `flushWithBackoff`.
3. `CheckpointReplayBacklog = promauto.NewGaugeVec({Name: "gitstore_controller_checkpoint_replay_backlog"}, []string{"kind"})` — set from the same per-kind queue-depth value `Manager.KindStats()` already computes; this is a deliberate simplification (see research.md §7), not a second bookkeeping mechanism.
4. No changes to `internal/api/poison.go` or `internal/health/handler.go`'s `/health` JSON surface — Prometheus-only, per the spec's clarification.

**Test-first**: `TestHealth_CheckpointLastWriteTimestamp_UpdatesOnSave`, `TestHealth_CheckpointWriteFailuresTotal_IncrementsOnFailure`, `TestHealth_CheckpointReplayBacklog_TracksQueueDepth`.
