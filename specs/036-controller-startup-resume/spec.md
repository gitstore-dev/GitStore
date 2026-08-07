# Feature Specification: Controller Startup Resume — List-Then-Watch and resourceVersion Checkpointing

**Feature Branch**: `036-controller-startup-resume`  
**Created**: 2026-07-17  
**Status**: Draft  
**Input**: User description: "Controller Startup Resume: List-Then-Watch and resourceVersion Checkpointing" (initiative #165 sub-issue #182)

## Clarifications

### Session 2026-08-06

- Q: SC-004 claims the replay window is "bounded and never unbounded," but FR-013 only counts checkpoint write failures without specifying what happens if writes keep failing (e.g. storage outage) for longer than the flush interval. What should happen when checkpoint writes fail repeatedly beyond the configured flush interval? → A: Backpressure until write succeeds — pause enqueuing/dispatching further watch events once a checkpoint write fails, resume only after a write succeeds.
- Q: FR-011 and the edge cases say a watch reconnect after a transient network error resumes "from the last persisted checkpoint." Should a same-process transient reconnect resume from the in-memory checkpoint or the persisted one? → A: In-memory checkpoint — only an actual process restart resumes from the persisted checkpoint; a same-process reconnect resumes from the freshest in-memory value.
- Q: Checkpoint age, replay backlog, and write-failure count should be exposed via which surface — Prometheus metrics, the health/poison HTTP API, or both? → A: Prometheus metrics only, via the existing `prometheus/client_golang` surface from spec 025; no new HTTP fields are added.
- Q: Should each registered kind get its own checkpoint file, or should all kinds share one combined file? → A: One file per kind, each with independent atomic write-temp-then-rename semantics.

## User Scenarios & Testing *(mandatory)*

### User Story 1 — Controller Boots and Reconciles All Existing Resources (Priority: P1)

When the controller manager starts for the first time against a live API, it lists every resource for each registered kind, populates the informer cache from that snapshot, marks the cache synced, and enqueues a reconcile work item for every resource seen during the list — ensuring no pre-existing resource is silently skipped.

**Why this priority**: An unreliable bootstrap sequence directly undermines the entire controller model. Resources that exist before the controller starts will accumulate silent drift if they are never reconciled. This is the most fundamental correctness requirement of the feature.

**Independent Test**: Can be fully tested by pre-creating five resources via the API, starting the controller manager against a stub API that serves a static list response, and asserting that a work item is enqueued for each of the five resources within one bootstrap cycle — with no watch stream or checkpoint file required.

**Acceptance Scenarios**:

1. **Given** fifty resources of kind `CategoryTaxonomy` exist before the controller starts, **When** the controller starts and completes its initial list, **Then** a reconcile work item is enqueued for each of the fifty resources.
2. **Given** the initial list completes successfully, **When** the cache is populated from the list response, **Then** `HasSynced()` returns `true` and dispatch begins; no reconcile is dispatched before `HasSynced()` is `true` for that kind.
3. **Given** a resource is created in the API after the list request is issued but before the watch stream starts, **When** the watch stream delivers the creation event, **Then** a work item is enqueued exactly once (no duplicate with the list result).

---

### User Story 2 — Controller Resumes After Restart Without Re-Processing Unchanged Resources (Priority: P1)

When the controller manager restarts, it reads a persisted `resourceVersion` checkpoint to resume the watch stream at the point it left off — replaying only the changes that occurred while it was down, rather than re-listing and re-reconciling every resource.

**Why this priority**: Without checkpointing, every restart triggers a full reconcile sweep that scales with the total resource count, not the change volume. On large installations this creates a burst of unnecessary work. Correctness on restart is as important as correctness on first boot.

**Independent Test**: Can be fully tested by starting the controller, processing events up to a checkpoint value, stopping the controller, restarting it with the checkpoint in place, and asserting that only events after the checkpoint are replayed — with no events before that version reprocessed.

**Acceptance Scenarios**:

1. **Given** the controller has checkpointed `resourceVersion=500`, **When** it restarts, **Then** the watch stream opens at `resourceVersion=500` and only events with version > 500 are delivered; no events before that version are replayed.
2. **Given** a valid checkpoint exists, **When** the controller starts, **Then** the initial list phase is skipped and the controller moves directly to the watch phase using the checkpointed version.
3. **Given** the checkpoint file is missing or corrupt, **When** the controller starts, **Then** it falls back to a full list-and-watch cycle as in User Story 1, discards the corrupt checkpoint, and writes a new valid checkpoint after the list completes.
4. **Given** the checkpoint is written after every N events (configurable), **When** the controller crashes between checkpoints, **Then** at most N events are re-processed on restart (bounded replay window).

---

### User Story 3 — Controller Handles Expired Watch Cursor and Reconnects Gracefully (Priority: P2)

When the API signals that the requested watch cursor is no longer available because the event log has been compacted, the controller manager detects this condition, falls back to a full re-list, writes a new checkpoint, and resumes watching — without requiring a manual restart or operator intervention.

**Why this priority**: Long-running controllers will inevitably encounter compacted event histories. An unhandled expiry would halt reconciliation silently until a human intervenes. Automatic recovery is required for unattended production operation.

**Independent Test**: Can be fully tested by wiring a stub API that rejects the first watch request as expired, then returns a valid list and event stream on the next attempt; asserting the controller re-lists, updates the checkpoint, and resumes reconciliation without crashing or requiring a restart.

**Acceptance Scenarios**:

1. **Given** the controller sends a watch request with a `resourceVersion` that has been compacted, **When** the API rejects it as expired, **Then** the controller discards the stale checkpoint, performs a full re-list, writes a new checkpoint, and resumes watching.
2. **Given** a full re-list is triggered by an expiry recovery, **When** the list completes, **Then** only resources whose state has changed since the last checkpoint are enqueued for reconciliation.
3. **Given** a watch cursor expiry occurs repeatedly (aggressive API compaction), **When** the controller falls back to list-then-watch each time, **Then** reconciliation remains correct and no work items are permanently lost.

---

### User Story 4 — Operator Monitors Checkpoint Age and Replay Backlog (Priority: P3)

A platform operator can observe checkpoint health — how recent the last successful checkpoint write was and how many events remain in the current replay backlog — through the existing Prometheus metrics surface, so that stale checkpoints or growing backlogs are detectable before they cause operational problems.

**Why this priority**: Checkpoint staleness is an invisible operational risk. Without metrics, an operator cannot distinguish a healthy controller from one that has been unable to persist checkpoints for hours.

**Independent Test**: Can be fully tested by starting the controller, injecting a delay in checkpoint writes, and asserting that the Prometheus metrics surface reports a non-zero checkpoint age within one metrics scrape interval — without requiring a full API stack.

**Acceptance Scenarios**:

1. **Given** the controller has written a checkpoint, **When** the Prometheus metrics surface is queried, **Then** it reports the age of the last successful checkpoint write for each registered kind.
2. **Given** the controller is processing a replay burst after a restart, **When** the Prometheus metrics surface is queried, **Then** it reports the number of events remaining in the replay backlog.
3. **Given** a checkpoint write fails (e.g. storage unavailable), **When** the Prometheus metrics surface is queried, **Then** a checkpoint write failure counter has been incremented; the controller applies backpressure (pausing further dispatch for the affected kind) rather than halting entirely, and resumes once a write succeeds.

---

### Edge Cases

- What happens when the API is unavailable during the initial list? The controller must retry the list with exponential backoff and not start the watch or mark the cache synced until the list succeeds.
- What happens when the watch stream closes unexpectedly (network error, server restart)? The controller must detect the disconnection, re-open the watch from the current in-memory checkpoint (or the last persisted checkpoint if the controller process itself restarted), and apply backoff between reconnect attempts.
- What happens when two controller instances start concurrently and both attempt to write a checkpoint for the same kind? Each kind's checkpoint lives in its own file; the checkpoint store must use atomic writes (write-to-temp, rename) so a partial write is never read as valid, and concurrent-write semantics for that single file are defined per backend.
- What happens when a checkpoint write fails repeatedly (e.g. storage unavailable) beyond the configured flush interval? The controller must apply backpressure — pausing further watch-event dispatch for the affected kind — until a write succeeds, so the replay window never grows unbounded; the write-failure counter increments on each failed attempt.
- What happens on a transient watch-stream reconnect within the same controller process (no restart)? The controller resumes from the current in-memory checkpoint, not the last value written to disk, so events already processed in this run are never replayed; only a process restart falls back to the persisted checkpoint.
- What happens when a bookmark event arrives on the watch stream? Bookmark events carry a `resourceVersion` and must update the checkpoint without enqueuing a work item.
- What happens when a delete event arrives for a resource during the list phase (before the watch stream starts)? The delete must be detected at the list-to-watch transition and the deleted resource must not be enqueued for reconciliation.
- What ordering guarantees does the watch stream provide? Events for a single resource must be processed in `resourceVersion` order; events for different resources may interleave.
- What happens if the controller is killed mid-checkpoint-write? Atomic write (write-to-temp, rename) ensures a partially-written checkpoint is never read.

## Requirements *(mandatory)*

### Functional Requirements

- **FR-001**: On first start (no checkpoint), the controller MUST perform a full list of all resources for each registered kind and populate the informer cache before opening the watch stream.
- **FR-002**: After the initial list completes, the controller MUST mark the kind's cache as synced (`MarkSynced`) and enqueue a work item for every resource returned in the list; no work item MUST be dispatched before `HasSynced` is `true` for that kind.
- **FR-003**: The transition from list phase to watch phase MUST use the `resourceVersion` returned by the list response as the starting cursor for the watch stream, so no events between list completion and watch-stream open are lost.
- **FR-004**: On every watch event, the controller MUST update the in-memory checkpoint for the affected kind to the event's `resourceVersion`.
- **FR-005**: The controller MUST persist the in-memory checkpoint to durable storage after every configurable number of events (default: 100) and after each clean shutdown. If a checkpoint write fails, the controller MUST apply backpressure — pausing further watch-event dispatch for the affected kind — until a subsequent write succeeds, so the replay window never exceeds the configured flush interval even during a sustained write outage.
- **FR-006**: Checkpoint writes MUST be atomic: written to a temporary location and renamed into place so a crash during write never leaves a partially-written checkpoint readable.
- **FR-007**: On restart with a valid checkpoint, the controller MUST skip the list phase and open the watch stream at the checkpointed `resourceVersion` directly.
- **FR-008**: On restart with a missing, unreadable, or corrupt checkpoint, the controller MUST fall back to a full list-then-watch cycle and write a new valid checkpoint after the list completes.
- **FR-009**: When the API signals that the requested watch cursor is expired (event log compacted), the controller MUST discard the stale checkpoint for the affected kind, perform a full re-list, write a new checkpoint, and resume watching — without requiring a restart.
- **FR-010**: Bookmark events on the watch stream MUST update the in-memory (and, at flush intervals, the persisted) checkpoint `resourceVersion` without enqueuing a work item.
- **FR-011**: Watch stream reconnections after a transient network error (same process, no restart) MUST resume from the current in-memory checkpoint — not the last persisted value — so events already processed in this run are never replayed; reconnects MUST apply exponential backoff between attempts, and the maximum backoff interval MUST be configurable. Only a full process restart resumes from the last *persisted* checkpoint (per FR-007).
- **FR-012**: A single registered kind MUST have at most one active list-or-watch loop at a time; concurrent expiry recoveries for the same kind MUST be coalesced into a single re-list.
- **FR-013**: The controller MUST expose per-kind checkpoint health via the existing Prometheus metrics surface (`prometheus/client_golang`, per spec 025) only: last successful checkpoint write time, current replay backlog size, and a total checkpoint write failure counter. No new fields are added to the health/poison HTTP API.
- **FR-014**: When the initial list request fails, the controller MUST retry with exponential backoff and MUST NOT start the watch stream, mark the cache synced, or enqueue any work items until the list succeeds.
- **FR-015**: The checkpoint storage backend MUST be swappable at construction time; a filesystem backend (for production) and an in-memory backend (for testing) MUST be provided. The filesystem backend MUST store each registered kind's checkpoint in its own file, independently subject to the atomic write-temp-then-rename rule in FR-006, so a write or corruption affecting one kind's file never impacts another kind's checkpoint.

### Key Entities

- **Checkpoint**: A persisted record binding a resource kind to the `resourceVersion` of the last event (or list completion) successfully processed by the controller. Used to resume a watch stream after a restart without replaying the full history.
- **CheckpointStore**: The storage abstraction responsible for reading and writing checkpoints. Writes are atomic. The filesystem implementation stores one checkpoint file per registered kind (independent atomic write-temp-then-rename per file); an in-memory backend is also provided for testing.
- **ListResponse**: The result of a full list request to the API for a given kind: a snapshot of all current resources and the `resourceVersion` at the time of the snapshot.
- **WatchEvent**: A streaming change notification delivered by the API after a list. One of `ADDED`, `MODIFIED`, `DELETED`, or `BOOKMARK`. Each event carries a `resourceVersion`; `BOOKMARK` carries only the version.
- **ReplayBacklog**: The count of events received on the watch stream that have been enqueued as work items but not yet dispatched. Used to measure recovery progress after a restart.

## Success Criteria *(mandatory)*

### Measurable Outcomes

- **SC-001**: A controller restarting with a valid checkpoint begins dispatching reconcile work within 5 seconds of startup, with no full re-list performed.
- **SC-002**: A controller starting cold (no checkpoint) with 10,000 pre-existing resources completes its initial list, populates the cache, and enqueues all work items within 60 seconds.
- **SC-003**: After a watch cursor expiry recovery, the controller resumes normal reconciliation within 30 seconds of the error; no manual intervention is required.
- **SC-004**: At most N events are re-processed after an unclean restart, where N is the configurable checkpoint flush interval (default: 100); the replay window is bounded and never unbounded, including during sustained checkpoint-write failures, because watch-event dispatch backpressures until a write succeeds.
- **SC-005**: Checkpoint writes complete in under 50 milliseconds under normal load; a write failure is surfaced in the Prometheus metrics surface within one scrape interval (default: 15 seconds).
- **SC-006**: The Prometheus metrics surface reports accurate checkpoint age and replay backlog within 5 seconds of any state change.
- **SC-007**: A watch stream reconnection after a transient network error completes within the configured backoff window (max: 30 seconds default) and resumes processing without operator action.
- **SC-008**: Zero duplicate work items are enqueued for a given resource during a single list-to-watch transition, even if the resource appears in both the list response and an early watch event.

## Assumptions

- The API supports standard list-and-watch semantics: list returns a resource snapshot with a `resourceVersion`, watch accepts a `resourceVersion` cursor, and the API signals when the cursor is no longer available due to compaction.
- `resourceVersion` values are opaque strings that the controller must not interpret numerically; they are used only as cursors and compared for equality.
- The informer cache interface (`HasSynced`, `MarkSynced`, `SyncedCh`, `Set`, `Delete`, `List`) is as implemented in spec 025 and is not modified by this spec.
- The checkpoint flush interval (default: 100 events) and maximum reconnect backoff (default: 30 seconds) are configurable via the existing `viper`-based config system; no new config file formats are introduced.
- The filesystem checkpoint store uses a directory configurable via the existing controller config; no new top-level environment variables are introduced.
- Concurrent multi-instance operation (leader election) is out of scope; only single-active-instance operation is covered.
- Bookmark event support requires the API to emit bookmark events on the watch stream; this spec defines the controller-side handling and assumes the API already produces them.
- The watch stream transport is consumed via an interface abstraction; the concrete transport (GraphQL subscription or dedicated watch endpoint) is not specified here.

## Dependencies

- **Spec 025** (`025-controller-manager-runtime`): Provides the informer cache (`HasSynced`, `MarkSynced`, `SyncedCh`), work queue, worker pool, and health surface. This spec extends the bootstrap path without modifying core dispatch logic. ✅ Merged.
- **Spec 026** (`026-reconcile-handler`): Defines the reconciler interface and dispatch contract that consumes work items produced by this spec's list-and-watch loop. ✅ Merged.
- **Issue #182** (this feature): Controller Startup Resume — sub-issue of initiative #165.
- **Issue #183** (downstream): Controller Integration Tests + Operations Runbook — blocked by this spec. Startup and resume paths must be defined before integration tests can exercise them.
- **Issue #244** (downstream): CategoryTaxonomy Controller Reconciliation — depends on the cache being reliably populated by the startup sequence defined here.

### Sub-issues of #165 (updated status)

| #    | Title                                                                         | Status             |
|------|-------------------------------------------------------------------------------|--------------------|
| #180 | Controller Manager Runtime: Queueing, Workers, Retry/Backoff, Idempotency    | ✅ Closed          |
| #181 | Reconcile Handler Contract for Core + CRD Kinds                              | ✅ Closed          |
| #182 | Controller Startup Resume: List-Then-Watch and resourceVersion Checkpointing | 🔵 This spec       |
| #183 | Controller Integration Tests + Operations Runbook                             | ⬜ Open (blocked)  |
| #244 | CategoryTaxonomy Controller Reconciliation                                    | ⬜ Open (blocked)  |
