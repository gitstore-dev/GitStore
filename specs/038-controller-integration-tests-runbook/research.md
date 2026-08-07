# Research: Controller Integration Tests + Operations Runbook

**Feature**: [spec.md](./spec.md) | **Plan**: [plan.md](./plan.md)

No `[NEEDS CLARIFICATION]` markers remain in the spec (see `checklists/requirements.md`), so this phase focuses on confirming the existing runtime surface is sufficient to write the integration tests and runbooks without new production abstractions.

## Decision: Test strategy — real components wired together, no new fakes

**Decision**: Integration tests construct a real `manager.Manager`, a real `listwatch.Runner[T]`, and a real `checkpoint.MemoryStore` (or `FilesystemStore` pointed at `t.TempDir()`), wired together exactly as `cmd/controller/main.go` wires them in production, using an in-process fake `ListWatcher[T]` to simulate the remote API (list/watch/error injection). No new test-double abstractions are introduced beyond what the existing `tests/contract/listwatch_*_test.go` files already use (`fakeListWatcher`, `failingStore`).

**Rationale**: Spec FR-013 requires assertions on externally observable outcomes rather than internal state, and FR-012 forbids a live ScyllaDB or network dependency. The existing contract tests already validate each component (`Manager`, `Runner`, `Store`) in isolation with fakes at that same boundary; wiring the *real* instances together (rather than re-fake at a higher boundary) is what actually proves the pieces integrate correctly — that is the gap this spec exists to close. Reusing the same `ListWatcher[T]` fake pattern from `tests/contract/listwatch_resume_test.go` avoids inventing a second simulation harness.

**Alternatives considered**:
- *End-to-end against the real `gitstore-api` GraphQL server*: rejected — violates FR-012 (no external network/ScyllaDB dependency in CI) and would blur the line with `gitstore-api`'s own test suite; the controller-manager's job is to react correctly to a list/watch stream regardless of what backend produced it.
- *Testing only at the `Manager` boundary, mocking `Runner`*: rejected — the disconnect/reconnect/replay-window user story (US3) is specifically about `Runner` behavior; mocking it away would make FR-005/FR-006 untestable at the integration level, leaving only unit coverage that already exists in `tests/contract/listwatch_resume_test.go`.

## Decision: Status-conflict simulation

**Decision**: Simulate concurrent status writes using two calls to a fake `status.StatusClient.Apply` implementation, one issued with a stale `StatusPatch.ResourceVersion` captured before the other's write commits. The fake returns `types.ErrConflict` (already defined in `internal/types/types.go:82`) for the stale call, matching the existing contract in `tests/contract/status_patch_test.go::TestStatusClient_Conflict_ReturnsErrConflict`.

**Rationale**: `internal/status/patch.go` already defines `StatusPatch`, `StatusClient`, and conflict detection is already contracted to return `types.ErrConflict`. No new conflict-detection logic is needed — this integration test proves a `Manager`-driven reconciler that receives `ErrConflict` from its `StatusClient` correctly re-fetches and retries rather than treating the conflict as a terminal or silently-dropped failure (User Story 2, FR-004). This is currently untested above the unit level: `status_patch_test.go` proves the client returns the error; nothing yet proves a reconciler-under-Manager reacts to it correctly.

**Alternatives considered**: Simulating conflict via two real goroutines racing against a shared in-memory store — rejected as flaky/non-deterministic for CI; a fake that deterministically returns `ErrConflict` on the second call achieves the same assertion without timing dependence.

## Decision: Poison-item and replay-window scenarios reuse existing fixtures

**Decision**: Poison-item tests (FR-007) build on the same reconciler-that-always-fails pattern already used in `tests/contract/retry_quarantine_test.go::TestManager_QuarantinesAfterMaxAttempts`, but assert through the full stack: `Manager.AllPoisonItems()`, the `health.PoisonItemsTotal` gauge, and (per FR-011) that the poison-item HTTP handlers registered in `cmd/controller/main.go` (`GET /controller/v1/poison/{kind}`, `POST /controller/v1/poison/{namespace}/{kind}/{name}/requeue`) reflect the same state. Replay-window-exceeded tests (FR-006) reuse the `ErrWatchExpired` sentinel from `internal/listwatch/types.go:47` and the existing expiry-recovery unit coverage in `tests/contract/listwatch_expiry_test.go`, extended to run through a real `Manager` dispatch loop rather than asserting on `Runner` internals alone.

**Rationale**: These sentinels, gauges, and HTTP routes already exist and are unit-tested in isolation; this spec's job is to prove they compose correctly end-to-end and that a runbook author can rely on them, not to invent new mechanisms.

**Alternatives considered**: Adding a new "poison reason" taxonomy or structured error codes — rejected as out of scope; FR-007 only requires that a poisoned item is "surfaced as a terminal failure," which the existing `retry.PoisonItem{Key, Attempts, LastError}` shape already satisfies.

## Decision: Observability audit method (FR-011)

**Decision**: For each of the three runbooks, write one `observability_test.go` case per referenced signal that asserts the signal's presence and correctness using `prometheus/client_golang/prometheus/testutil` (already imported in `tests/contract/health_test.go`), following the same pattern as `TestHealth_CheckpointLastWriteTimestamp_UpdatesOnSave`. If a referenced signal turns out not to exist or not to update as expected, the runbook language is corrected to describe what is actually emitted (do not invent a new metric unless the runbook is otherwise impossible to write truthfully) — in which case, per Constitution Principle I, a failing test is written first, then the minimal instrumentation is added.

**Rationale**: `internal/health/metrics.go` already defines `QueueDepth`, `ActiveWorkers`, `PoisonItemsTotal`, `StalledWorkers`, `ReconcileTotal`, `CheckpointLastWriteTimestamp`, `CheckpointWriteFailuresTotal`, and `CheckpointReplayBacklog` — a full pre-existing signal set. Initial inspection (this research phase) found no obviously missing signal for the three runbook topics (lag → `QueueDepth`/`StalledWorkers`/`ActiveWorkers`; replay-window-exceeded → `CheckpointLastWriteTimestamp`/`CheckpointReplayBacklog`; poisoned item → `PoisonItemsTotal` + poison HTTP API), so no new metric is anticipated, but the audit is executed as tests rather than assumed.

**Alternatives considered**: Manual/documentation-only verification (reading the code and asserting by inspection) — rejected because SC-003 requires every runbook-referenced signal to be "confirmed present and accurate by at least one automated test," not by inspection alone.

## Decision: Runbook format and location

**Decision**: Runbooks are plain markdown files under a new `docs/runbooks/` directory (`controller-lag.md`, `controller-replay-window-exceeded.md`, `controller-poisoned-item.md`), each following a consistent structure: Symptom → Diagnostic Steps (with exact metric names / PromQL-style queries / API calls) → Recovery Actions → Verification. Each is linked from `docs/developer-guide.md`'s existing controller-manager section (which already links out to per-spec quickstarts, e.g. line 386-388).

**Rationale**: No runbook precedent exists yet in this repo (`docs/` has no `runbooks/` or `operations/` subdirectory); `docs/developer-guide.md` is the established place for controller-manager operational documentation, so linking from there keeps discovery consistent with how spec quickstarts are already surfaced. Per project guideline ("After implementing a feature update the documentation in `docs/`"), this is the correct location rather than nesting docs solely under `specs/038-.../`.

**Alternatives considered**: Placing runbooks inside `specs/038-controller-integration-tests-runbook/` only — rejected because runbooks are living operational documentation an on-call engineer needs to find without knowing which spec introduced them; `docs/` is the durable, discoverable location, while `specs/` remains the historical record of how the feature was planned.
