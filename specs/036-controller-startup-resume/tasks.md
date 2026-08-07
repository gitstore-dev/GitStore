# Tasks: Controller Startup Resume — List-Then-Watch and resourceVersion Checkpointing

**Input**: Design documents from `/specs/036-controller-startup-resume/`
**Branch**: `036-controller-startup-resume`
**Prerequisites**: plan.md ✅, spec.md ✅, research.md ✅, data-model.md ✅, contracts/ ✅, quickstart.md ✅

**Tests**: Test-First Development (Constitution Principle I — NON-NEGOTIABLE). Tests MUST be written before implementation and verified to fail before proceeding.

**Scope notes**:
- No concrete `ListWatcher[T]` transport implementation ships in this spec — `gitstore-api`'s GraphQL schema has no `Subscription` type today, and this is explicitly greenfield per the spec's own Assumptions (see research.md §1). All tests use a hand-written `stubListWatcher[T]` test double. Production wiring of a real kind's `Runner[T]` is deferred to the spec that introduces the first concrete kind (e.g. issue #244).
- No changes to `Reconciler`, `ReconcileResult`, or `syncChecker` from specs 025/026 — this spec is purely a producer of `WorkItemKey`s into the existing `Manager`/`Queue`.
- No changes to `internal/api/poison.go` or the `/health` JSON surface — checkpoint health is Prometheus-only per the spec's clarification.

## Format: `[ID] [P?] [Story] Description`

- **[P]**: Can run in parallel (different files, no dependencies on incomplete tasks)
- **[Story]**: User story label (US1–US4)

---

## Phase 1: Setup

**Purpose**: Scaffold the two new packages. No new external dependencies.

- [X] T001 Create `gitstore-controller-manager/internal/checkpoint/` package with empty `checkpoint.go`, `filesystem.go`, `memory.go` files (package declaration only)
- [X] T002 [P] Create `gitstore-controller-manager/internal/listwatch/` package with empty `types.go`, `listwatcher.go`, `runner.go` files (package declaration only)
- [X] T003 [P] Create `gitstore-controller-manager/tests/checkpoint/` directory (for filesystem/memory store tests)
- [X] T004 Verify `go build ./...` and `go test ./...` still pass after scaffolding (no behavior change)

---

## Phase 2: Foundational (Blocking Prerequisites)

**Purpose**: Shared types, interfaces, and config/metrics declarations every user story depends on.

**⚠️ CRITICAL**: No user story work can begin until this phase is complete and `go build ./...` passes.

- [X] T005 Define `Record{Kind, ResourceVersion string; WrittenAt time.Time}` struct and `Store interface{ Load(ctx, kind string) (Record, error); Save(ctx, Record) error }` in `gitstore-controller-manager/internal/checkpoint/checkpoint.go` per contracts/checkpoint-store-api.md
- [X] T006 [P] Implement `MemoryStore` (mutex-guarded `map[string]Record`) satisfying `Store` in `gitstore-controller-manager/internal/checkpoint/memory.go`, mirroring the style of `internal/cache.Cache`
- [X] T007 [P] Define `EventType` enum (`Added`, `Modified`, `Deleted`, `Bookmark`), `WatchEvent[T]{Type EventType; Object T; ResourceVersion string}`, `ListResponse[T]{Items []T; ResourceVersion string}`, and `var ErrWatchExpired = errors.New(...)` in `gitstore-controller-manager/internal/listwatch/types.go` per data-model.md
- [X] T008 [P] Define `Watcher[T]{Events() <-chan WatchEvent[T]; Err() error; Stop()}` and `ListWatcher[T]{List(ctx) (ListResponse[T], error); Watch(ctx, resourceVersion string) (Watcher[T], error)}` interfaces in `gitstore-controller-manager/internal/listwatch/listwatcher.go` per contracts/listwatcher-interface.md
- [X] T009 Add `CheckpointDir string` (`mapstructure:"checkpoint_dir"`, default `.gitstore/checkpoints`), `CheckpointFlushIntervalEvents int` (`mapstructure:"checkpoint_flush_interval_events"`, default `100`), and `MaxWatchBackoffStr string`/`MaxWatchBackoff time.Duration` (`mapstructure:"max_watch_backoff"`/`"-"`, default `"30s"`) fields to `ControllerConfig` in `gitstore-controller-manager/internal/config/config.go`; parse `MaxWatchBackoffStr` into `MaxWatchBackoff` inside `validate()` following the existing `DefaultStallThresholdStr` idiom
- [X] T010 [P] Add `TestLoad_Defaults` and `TestLoad_EnvOverrides` assertions for the three new config fields to `gitstore-controller-manager/internal/config/config_test.go`, using the existing `setenv(t, pairs...)` helper
- [X] T011 [P] Declare `CheckpointLastWriteTimestamp` (`GaugeVec`, `gitstore_controller_checkpoint_last_write_timestamp_seconds`), `CheckpointWriteFailuresTotal` (`CounterVec`, `gitstore_controller_checkpoint_write_failures_total`), and `CheckpointReplayBacklog` (`GaugeVec`, `gitstore_controller_checkpoint_replay_backlog`) — each labeled `["kind"]` — in `gitstore-controller-manager/internal/health/metrics.go`, following the existing `promauto.New*Vec` pattern
- [X] T012 Define the `Runner[T]` struct (fields: `Kind string`, `ListWatcher ListWatcher[T]`, `Cache *cache.Cache[T]`, `Store checkpoint.Store`, `Enqueue func(types.WorkItemKey) error`, `KeyFunc func(T) types.WorkItemKey`, `RevisionFunc func(T) string`, `FlushIntervalEvents int`, `MaxBackoff time.Duration`, `Log *zap.Logger`, plus an unexported `currentRV string` field) in `gitstore-controller-manager/internal/listwatch/runner.go` — type only, no `Run` method body yet

**Checkpoint**: `go build ./...` must pass before proceeding.

---

## Phase 3: User Story 1 — Controller Boots and Reconciles All Existing Resources (Priority: P1) 🎯 MVP

**Goal**: On first start, the controller lists every resource for a kind, populates the cache, marks it synced, enqueues a work item for every listed resource, then opens a watch stream at the list's resourceVersion with no duplicate enqueue across the transition.

**Independent Test**: Pre-create five resources via a stub `ListWatcher[T]` that serves a static list response; start `Runner.Run` against it with no watch stream required; assert a work item is enqueued for each of the five resources within one bootstrap cycle.

### Tests for User Story 1 (write first, verify they FAIL before T017)

- [X] T013 [P] [US1] Create `gitstore-controller-manager/tests/contract/listwatch_bootstrap_test.go` with a hand-written `stubListWatcher[T]` test double (configurable `List` response/error/failure-count and a test-controlled `Watch` channel, mirroring the `stubReconciler` convention from spec 026) and `TestRunner_Bootstrap_ListsAndEnqueuesAll`: 50 stub items, run `Runner.Run` with a `checkpoint.MemoryStore`, assert 50 `Enqueue` calls and `cache.HasSynced() == true`
- [X] T014 [P] [US1] Add `TestRunner_Bootstrap_NoDispatchBeforeSynced` to the same file: `stubListWatcher.List` fails until told to succeed; assert zero `Enqueue` calls and `HasSynced() == false` while `List` is still failing
- [X] T015 [P] [US1] Add `TestRunner_Bootstrap_NoDuplicateAcrossListWatchTransition` to the same file: `List` returns an item with key K at `ResourceVersion="5"`; the stub `Watch` stream immediately delivers an `Added` event for K at `ResourceVersion="5"` (same as the list snapshot); assert K is enqueued exactly once
- [X] T016 [P] [US1] Add `TestRunner_Bootstrap_ListFailure_RetriesWithBackoff_NeverMarksSynced` to the same file: `List` fails 3 times then succeeds; assert `MarkSynced`/`Enqueue`/`Watch` only happen after the successful call, never during the 3 failing attempts

### Implementation for User Story 1

- [X] T017 [US1] Implement the bootstrap branch of `Runner.Run(ctx)` in `gitstore-controller-manager/internal/listwatch/runner.go`: retry `ListWatcher.List` with unbounded exponential backoff (`cenkalti/backoff/v5`, same style as `internal/retry/retry.go` but no `MaxTries` — a list failure must never give up per FR-014) until success; `Cache.Set` every returned item; call `Cache.MarkSynced()`; `Enqueue(KeyFunc(item))` for every item; retain the enqueued-key set for the transition de-dup in T018
- [X] T018 [US1] Implement list-to-watch transition de-dup in `Runner.Run`: after bootstrap, open `Watch(ctx, list.ResourceVersion)`; for a key whose first watch event carries a `ResourceVersion` that is not newer than the list's own `ResourceVersion`, only `Cache.Set` (suppress the duplicate `Enqueue`); once a key's event carries a strictly newer `ResourceVersion`, resume normal per-event enqueue behavior
- [X] T019 [US1] Implement per-event handling inside the watch loop in `Runner.Run`: `Added`/`Modified` → `Cache.Set` + `Enqueue`; `Deleted` → `Cache.Delete` + `Enqueue`; `Bookmark` → update `currentRV` only, no `Enqueue` (FR-010) — flush/backpressure/reconnect logic is added in US2 (T028–T029); this task only wires the event-type switch and `currentRV` tracking

**Checkpoint**: `go test ./tests/contract/ -run TestRunner_Bootstrap` passes. User Story 1 is independently functional and testable.

---

## Phase 4: User Story 2 — Controller Resumes After Restart Without Re-Processing Unchanged Resources (Priority: P1)

**Goal**: On restart with a valid checkpoint, skip the list phase and resume the watch stream at the checkpointed resourceVersion. Persist the in-memory checkpoint every N events and on clean shutdown, atomically and per-kind. Backpressure event consumption when persistence fails so the replay window stays bounded. Same-process transient reconnects resume from the in-memory cursor, not the persisted one.

**Independent Test**: Start `Runner.Run`, process events up to a checkpoint value, stop it, restart with the checkpoint file in place, and assert only events after the checkpoint are replayed with no re-list.

### Tests for User Story 2 (write first, verify they FAIL before T026)

- [X] T020 [P] [US2] Create `gitstore-controller-manager/tests/checkpoint/filesystem_test.go` with `TestFilesystemStore_SaveThenLoad_RoundTrips`, `TestFilesystemStore_AtomicWrite_NoPartialFileOnCrash` (assert the temp file is never observable at the final path), `TestFilesystemStore_CorruptFile_ReturnsError`, `TestFilesystemStore_MissingFile_ReturnsError`, and `TestFilesystemStore_OneFilePerKind_Isolated` (writing kind A's checkpoint never touches kind B's file)
- [X] T021 [P] [US2] Create `gitstore-controller-manager/tests/checkpoint/memory_test.go` with `TestMemoryStore_SaveThenLoad`
- [X] T022 [P] [US2] Create `gitstore-controller-manager/tests/contract/listwatch_resume_test.go` with `TestRunner_Resume_SkipsListPhase` and `TestRunner_Resume_WatchesFromCheckpointedVersion`: pre-populate a `checkpoint.MemoryStore` with `Record{Kind: "X", ResourceVersion: "500"}`; run `Runner.Run`; assert `stubListWatcher.List` is never called and `Watch` is called with `resourceVersion == "500"`
- [X] T023 [P] [US2] Add `TestRunner_Flush_PersistsAfterNEvents` and `TestRunner_Flush_PersistsOnCleanShutdown` to the same file: configure `FlushIntervalEvents = 3`; feed 3 stub watch events; assert `Store.Save` is called once with the third event's `ResourceVersion`; cancel `ctx` after one more event and assert a final `Save` occurs with that event's version
- [X] T024 [P] [US2] Add `TestRunner_Backpressure_PausesOnWriteFailure_ResumesOnSuccess` and `TestRunner_Backpressure_BoundedReplayWindow` to the same file: use a `Store` stub whose `Save` fails N times then succeeds at the flush boundary; assert no further events are drained from the stub `Watch` channel while `Save` is failing, `CheckpointWriteFailuresTotal` increments per failed attempt, and at most `FlushIntervalEvents` events are ever unpersisted at once even across repeated failures
- [X] T025 [P] [US2] Add `TestRunner_TransientReconnect_UsesInMemoryCheckpoint_NotPersisted` and `TestRunner_TransientReconnect_BackoffCappedAtMax` to the same file: close the stub `Watcher` with a non-`ErrWatchExpired`, non-nil `Err()` after `currentRV` has advanced past the last persisted value; assert the next `Watch(ctx, resourceVersion)` call receives `currentRV` (not the stale persisted value) and that successive reconnect attempts back off exponentially, capped at `MaxBackoff`

### Implementation for User Story 2

- [X] T026 [US2] Implement `FilesystemStore{Dir string}` in `gitstore-controller-manager/internal/checkpoint/filesystem.go`: `NewFilesystemStore(dir) (*FilesystemStore, error)` (`os.MkdirAll(dir, 0o755)`); `Save` marshals `Record` to JSON, writes via `os.CreateTemp(dir, "<kind>.checkpoint.*.tmp")`, `Sync`s, `Close`s, then `os.Rename`s to `<dir>/<kind>.checkpoint.json`; `Load` does `os.ReadFile` + `json.Unmarshal` on that path, returning any error (not-exist, permission, malformed JSON) as-is
- [X] T027 [US2] Implement the resume entry path in `Runner.Run` (`gitstore-controller-manager/internal/listwatch/runner.go`): call `Store.Load(ctx, Kind)` exactly once at the top of `Run`; on success, set `currentRV` from the loaded `Record.ResourceVersion` and skip directly to `Watch(ctx, currentRV)`, bypassing the T017 bootstrap branch entirely; on error (any kind), fall through to bootstrap
- [X] T028 [US2] Implement `flushWithBackoff` in `Runner.Run`: an `eventsSinceFlush` counter incremented on every processed watch event (including `Bookmark`); at `FlushIntervalEvents` and once more on `ctx.Done()`, loop calling `Store.Save(ctx, Record{Kind, currentRV, time.Now()})`; on error, `health.CheckpointWriteFailuresTotal.WithLabelValues(Kind).Inc()`, log at WARN, and backoff-sleep before retrying — the watch loop's `select` MUST NOT read `Watcher.Events()` again until this call returns successfully (FR-005, SC-004); on success, `health.CheckpointLastWriteTimestamp.WithLabelValues(Kind).Set(float64(time.Now().Unix()))` and reset the counter
- [X] T029 [US2] Implement transient-reconnect handling in `Runner.Run`: when `Watcher.Events()` closes and `Watcher.Err()` does not satisfy `errors.Is(err, listwatch.ErrWatchExpired)` (including a `nil` `Err()`, e.g. clean `ctx` cancellation), reopen `Watch(ctx, currentRV)` — never call `Store.Load` here — with unbounded exponential backoff (`cenkalti/backoff/v5`) between attempts, capped at `MaxBackoff`
- [X] T030 [US2] Wire `cfg.Controller.CheckpointDir`, `cfg.Controller.CheckpointFlushIntervalEvents`, and `cfg.Controller.MaxWatchBackoff` through to `Runner` construction points by adding a documented example/comment block in `gitstore-controller-manager/cmd/controller/main.go` (no real kind is registered by this spec — see quickstart.md for the full per-kind wiring pattern a future kind-owning spec follows)

**Checkpoint**: `go test ./tests/checkpoint/...` and `go test ./tests/contract/ -run 'TestRunner_(Resume|Flush|Backpressure|TransientReconnect)'` pass. User Stories 1 AND 2 both work independently.

---

## Phase 5: User Story 3 — Controller Handles Expired Watch Cursor and Reconnects Gracefully (Priority: P2)

**Goal**: When the watch cursor is compacted, discard the stale checkpoint, re-list, enqueue only resources that changed since the last checkpoint, write a fresh checkpoint, and resume watching — repeatedly, without losing work items.

**Independent Test**: A stub `ListWatcher` rejects the first `Watch` as expired, then serves a valid list and stream on the next attempt; assert the controller re-lists, updates the checkpoint, and resumes reconciliation without crashing or requiring a restart.

### Tests for User Story 3 (write first, verify they FAIL before T034)

- [X] T031 [P] [US3] Create `gitstore-controller-manager/tests/contract/listwatch_expiry_test.go` with `TestRunner_ExpiryRecovery_DiscardsCheckpointAndRelists`: stub `Watcher.Err()` returns `listwatch.ErrWatchExpired`; assert a fresh `List` call occurs and the stale in-memory cursor is not reused for the reconnect
- [X] T032 [P] [US3] Add `TestRunner_ExpiryRecovery_EnqueuesOnlyChangedResources` to the same file: the re-list response contains one item whose `RevisionFunc` value is unchanged from what's already cached and one whose value differs; assert only the changed item is enqueued, while both are `Cache.Set`
- [X] T033 [P] [US3] Add `TestRunner_ExpiryRecovery_RepeatedExpiry_NoLostWorkItems` to the same file: script two consecutive `ErrWatchExpired` closes with different changed resources each time; assert every distinct changed resource across both recovery cycles is eventually enqueued exactly once, with a bounded test timeout (no deadlock)

### Implementation for User Story 3

- [X] T034 [US3] Implement the expiry-recovery branch in `Runner.Run` (`gitstore-controller-manager/internal/listwatch/runner.go`): when `Watcher.Err()` satisfies `errors.Is(err, listwatch.ErrWatchExpired)`, discard `currentRV` and any pending flush state, then re-run the T017 bootstrap-retry `List` logic (extract a shared helper if not already factored out), replacing "enqueue every item" with "enqueue only if `RevisionFunc(item)` differs from the cached object's revision or the key is absent from the cache" — always `Cache.Set` regardless of the enqueue decision
- [X] T035 [US3] After a successful expiry re-list, call `Store.Save` with the new list's `ResourceVersion` before reopening `Watch` at the new cursor (FR-009), reusing the T028 `flushWithBackoff` helper for the write

**Note**: T034/T035 were implemented as part of `watchLoop`/`recoverFromExpiry` in Phase 4 (the watch loop's close-handling branches for expired vs. transient closes were designed together) — verified independently correct by the US3 test suite above, all passing.

**Checkpoint**: `go test ./tests/contract/ -run TestRunner_ExpiryRecovery` passes. All three of US1/US2/US3 are independently functional.

---

## Phase 6: User Story 4 — Operator Monitors Checkpoint Age and Replay Backlog (Priority: P3)

**Goal**: Checkpoint age, replay backlog, and write-failure count are all observable via the existing Prometheus metrics surface.

**Independent Test**: Start the controller, inject a delay in checkpoint writes, and assert the metrics surface reports a non-zero checkpoint age within one metrics scrape interval.

### Tests for User Story 4 (write first, verify they FAIL before T038)

- [X] T036 [P] [US4] Add `TestHealth_CheckpointLastWriteTimestamp_UpdatesOnSave` and `TestHealth_CheckpointWriteFailuresTotal_IncrementsOnFailure` to `gitstore-controller-manager/tests/contract/health_test.go`: drive a `Runner` through one successful and one failing flush (reusing the T024 failing-`Store` stub) and assert both metrics via direct `.Write`/collector inspection, matching this file's existing assertion style
- [X] T037 [P] [US4] Add `TestHealth_CheckpointReplayBacklog_TracksQueueDepth` to the same file: enqueue several items via `Manager.Enqueue` for a registered kind, call `Manager.KindStats()`, and assert `CheckpointReplayBacklog{kind}` equals `KindStats()[kind].QueueDepth`

### Implementation for User Story 4

- [X] T038 [US4] Set `health.CheckpointReplayBacklog.WithLabelValues(kind).Set(float64(depth))` inside the existing per-kind loop in `Manager.KindStats()` (`gitstore-controller-manager/internal/manager/manager.go`), alongside the existing `QueueDepth`/`ActiveWorkers`/`PoisonItemsTotal` sets — reuses the same `depth` value already computed there; no second bookkeeping mechanism (research.md §7)

**Checkpoint**: `go test ./tests/contract/ -run TestHealth_Checkpoint` passes. All four user stories independently functional. Full `go test ./...` passes.

---

## Phase 7: Polish & Cross-Cutting Concerns

- [X] T039 [P] Construct a shared `checkpoint.FilesystemStore` from `cfg.Controller.CheckpointDir` in `gitstore-controller-manager/cmd/controller/main.go`, fatal-logging on construction error (same pattern as `config.Load`/`manager.InitLogger`); no per-kind `Runner` is registered here since no concrete kind/`ListWatcher` implementation exists yet (deferred — see Scope notes above)
- [X] T040 [P] Run `make pr-ready` from the repo root; fix any lint, license-header, or test failures before marking the branch ready for review
- [X] T041 [P] Verify every code snippet in `specs/036-controller-startup-resume/quickstart.md` against the final signatures in `internal/checkpoint/{checkpoint,filesystem,memory}.go` and `internal/listwatch/{types,listwatcher,runner}.go`; update any snippet that no longer matches
- [X] T042 Update `docs/` (per repo guideline: "After implementing a feature update the documentation in `docs/`") to document the three new `GITSTORE_CONTROLLER__CHECKPOINT_DIR` / `GITSTORE_CONTROLLER__CHECKPOINT_FLUSH_INTERVAL_EVENTS` / `GITSTORE_CONTROLLER__MAX_WATCH_BACKOFF` env vars and the three new Prometheus metrics

---

## Dependencies & Execution Order

### Phase Dependencies

- **Phase 1 (Setup)**: No dependencies — start immediately
- **Phase 2 (Foundational)**: Depends on Phase 1 — **blocks all user stories**
- **Phase 3 (US1)**: Depends on Phase 2
- **Phase 4 (US2)**: Depends on Phase 2; extends the same `Runner.Run` method US1 started (T017–T019) but is independently testable via its own resume/flush/backpressure/reconnect test file
- **Phase 5 (US3)**: Depends on Phase 2 and on T017's extracted bootstrap-retry `List` helper (US1) being reusable; independently testable via its own expiry test file
- **Phase 6 (US4)**: Depends on Phase 2 (metric declarations, T011) and reuses the T024 failing-`Store` stub from US2 for one test, but the implementation task (T038) only touches `Manager.KindStats()` — no dependency on US1/US2/US3 implementation being complete
- **Phase 7 (Polish)**: Depends on Phases 3–6

### User Story Dependencies

- **US1 (P1)**: Depends only on Phase 2 — no other story dependencies. Fully independently testable with a stub `ListWatcher` and no checkpoint file (per spec's own Independent Test).
- **US2 (P1)**: Depends on Phase 2 only at the test level (uses its own `MemoryStore`/`FilesystemStore` and stub `ListWatcher`); shares the `Runner.Run` method body with US1 but adds distinct branches (resume entry, flush, reconnect) that are additive, not overlapping, with US1's bootstrap branch.
- **US3 (P2)**: Depends on Phase 2 and reuses US1's list-retry logic (extracted as a shared helper in T034) — test-level independence is preserved via its own `stubListWatcher` scripted to expire.
- **US4 (P3)**: Depends on Phase 2 (metric declarations) and, for one test only, the US2 failing-`Store` stub pattern — implementation is a one-line addition to existing `Manager.KindStats()`, fully independent of US1/US3.

### Parallel Opportunities

Within Phase 2: T006–T008, T010–T011 can run in parallel (distinct files).
Within Phase 3: T013–T016 (all tests) can run in parallel; T017–T019 are sequential (same method, same file).
Within Phase 4: T020–T025 (all tests) can run in parallel; T026 can run in parallel with T020–T025; T027–T029 are sequential (same method).
Within Phase 5: T031–T033 (all tests) can run in parallel; T034–T035 are sequential.
Within Phase 6: T036–T037 (all tests) can run in parallel.
Phases 3, 5, and 6 can be worked on in parallel by different developers after Phase 2 completes, provided Phase 4's `Runner.Run` scaffolding (T017) lands first as the shared method body they all extend.

---

## Parallel Example: User Story 1

```bash
# Write all US1 tests concurrently (same file, different test functions):
Task: T013 — stubListWatcher + TestRunner_Bootstrap_ListsAndEnqueuesAll
Task: T014 — TestRunner_Bootstrap_NoDispatchBeforeSynced
Task: T015 — TestRunner_Bootstrap_NoDuplicateAcrossListWatchTransition
Task: T016 — TestRunner_Bootstrap_ListFailure_RetriesWithBackoff_NeverMarksSynced

# After tests fail, implement in order (same method body — sequential):
Task: T017 — bootstrap List-retry + Set/MarkSynced/Enqueue
Task: T018 — list-to-watch transition de-dup
Task: T019 — per-event type switch (Added/Modified/Deleted/Bookmark)
```

---

## Implementation Strategy

### MVP (User Story 1 only)

1. Phase 1: Setup (T001–T004)
2. Phase 2: Foundational (T005–T012) — `go build ./...` must pass
3. Phase 3: US1 tests (T013–T016, all failing) then implementation (T017–T019)
4. **STOP**: `go test ./tests/contract/ -run TestRunner_Bootstrap` passes; manually verify with a synthetic 5-resource stub

### Incremental Delivery

1. Phase 2 → Phase 3 (US1): cold-start bootstrap, no persistence — **deploy-ready MVP** (US1 alone delivers correct behavior for a controller that never restarts)
2. Phase 4 (US2): checkpoint persistence, resume, backpressure, transient reconnect — completes the restart story
3. Phase 5 (US3): expiry recovery — additive, no changes to US1/US2 code paths beyond reusing the bootstrap-retry helper
4. Phase 6 (US4): one new metric wired into existing `KindStats()` — additive, one-line implementation
5. Phase 7: shared `FilesystemStore` construction in `main.go` + polish + `make pr-ready`
