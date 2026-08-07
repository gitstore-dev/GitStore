# Implementation Plan: Controller Integration Tests + Operations Runbook

**Branch**: `038-controller-integration-tests-runbook` | **Date**: 2026-08-06 | **Spec**: [spec.md](./spec.md)
**Input**: Feature specification from `/specs/038-controller-integration-tests-runbook/spec.md`

**Note**: This template is filled in by the `/speckit.plan` command. See `.specify/templates/plan-template.md` for the execution workflow.

## Summary

Close the gap between the controller manager runtime shipped in specs 025 (runtime), 026 (reconcile handler), and 036 (list-then-watch/checkpointing) and its operational readiness: add integration-level Go tests that drive the real `manager.Manager` + `listwatch.Runner` + `checkpoint.Store` wiring through reconcile/retry/resume, status-conflict, disconnect/reconnect/replay, replay-window-exceeded, and poison-item scenarios end-to-end (as opposed to the existing per-package contract tests, which exercise each component in isolation); then author three markdown runbooks (`docs/runbooks/controller-lag.md`, `controller-replay-window-exceeded.md`, `controller-poisoned-item.md`) that map each failure mode to the Prometheus metrics already defined in `internal/health/metrics.go` and the poison-item HTTP API already exposed in `cmd/controller/main.go`, and verify every signal a runbook references is actually emitted by an existing or new test.

## Technical Context

**Language/Version**: Go 1.25 (`gitstore-controller-manager` module)
**Primary Dependencies**: `github.com/alitto/pond/v2` (worker pool), `github.com/cenkalti/backoff/v5` (retry/backoff), `github.com/prometheus/client_golang` (metrics, already instrumented in `internal/health/metrics.go`), `go.uber.org/zap` (structured logging) — all already present in `go.mod`; no new dependencies
**Storage**: N/A for this feature — tests use the existing in-memory `internal/cache.Cache[T]` and `internal/checkpoint.MemoryStore`/`FilesystemStore` fakes already used by contract tests; no ScyllaDB or filesystem dependency required in CI
**Testing**: Go `testing` package + `github.com/prometheus/client_golang/prometheus/testutil` for metric assertions (both already used in `tests/contract/health_test.go`); new tests live in a new `tests/integration/` package, run via `go test ./...` (same invocation as `make test`)
**Target Platform**: Linux/macOS CI runner and local dev — no platform-specific behavior
**Project Type**: Backend service (Go module) — test/documentation-only feature, no production code changes anticipated beyond what's needed to make a signal observable
**Performance Goals**: Full new integration suite completes in well under the existing `make test` budget; SC-004 requires under 5 minutes for this suite specifically
**Constraints**: Must not depend on a live ScyllaDB instance or external network service (FR-012); must assert on externally observable state only — status, conditions, metrics, logs — not internal struct fields (FR-013)
**Scale/Scope**: One new Go test package (`tests/integration/`) covering 7 scenario groups (FR-001–FR-007) plus 3 new runbook documents (FR-008–FR-010); no new production packages, only whatever minimal instrumentation gaps FR-011's audit uncovers

## Constitution Check

*GATE: Must pass before Phase 0 research. Re-check after Phase 1 design.*

- **I. Test-First Development**: This feature *is* test authorship — every functional requirement (FR-001–FR-007) maps 1:1 to a test group, written before any production-code changes. If the observability audit (FR-011) finds a genuine gap (e.g., a metric label missing), that fix follows red→green per this principle. PASS.
- **II. API-First Design**: No new service-boundary contracts are introduced. The poison-item HTTP API and Prometheus `/metrics` endpoint already exist (spec 025/036); this feature documents and verifies them, it does not change their shape. PASS.
- **III. Clear Contracts & Versioning**: N/A — no public interface changes. PASS.
- **IV. Observability & Debuggability**: Directly advances this principle — FR-011 requires every runbook-referenced signal to be verified as actually emitted, closing a real observability gap. PASS.
- **V. User Story Driven Development**: Spec already organizes work as five independently testable, prioritized user stories (P1–P3). PASS.
- **VI. Incremental Delivery**: P1 (reconcile/retry/resume + status-conflict tests) is independently mergeable and valuable before P2/P3 runbook work begins. PASS.
- **VII. Simplicity & YAGNI**: No new abstractions proposed; tests reuse existing fakes (`cache.New[T]`, `checkpoint.MemoryStore`) and existing metrics. Runbooks are plain markdown, no tooling introduced. PASS.

No violations. Complexity Tracking section is not needed.

## Project Structure

### Documentation (this feature)

```text
specs/038-controller-integration-tests-runbook/
├── plan.md              # This file (/speckit.plan command output)
├── research.md          # Phase 0 output (/speckit.plan command)
├── data-model.md         # Phase 1 output (/speckit.plan command)
├── quickstart.md         # Phase 1 output (/speckit.plan command)
├── contracts/            # Phase 1 output (/speckit.plan command)
└── tasks.md              # Phase 2 output (/speckit.tasks command - NOT created by /speckit.plan)
```

### Source Code (repository root)

```text
gitstore-controller-manager/
├── internal/
│   ├── health/metrics.go       # Existing Prometheus metrics — audited for FR-011, extended only if a gap is found
│   ├── manager/                # Existing dispatch/retry/quarantine runtime under test
│   ├── checkpoint/              # Existing checkpoint Store (memory + filesystem) under test
│   ├── listwatch/               # Existing Runner (bootstrap/resume/expiry) under test
│   └── status/patch.go          # Existing status-conflict primitives (StatusPatch, StatusClient) under test
├── tests/
│   ├── contract/                 # Existing per-package contract tests (unchanged)
│   ├── checkpoint/               # Existing checkpoint store tests (unchanged)
│   └── integration/              # NEW: end-to-end scenario tests wiring Manager + Runner + Store together
│       ├── reconcile_retry_resume_test.go   # FR-001, FR-002, FR-003
│       ├── status_conflict_test.go          # FR-004
│       ├── disconnect_reconnect_test.go     # FR-005, FR-006
│       ├── poison_item_test.go              # FR-007
│       └── observability_test.go            # FR-011 signal-existence assertions
└── go.mod

docs/
└── runbooks/                     # NEW directory
    ├── controller-lag.md                    # FR-008
    ├── controller-replay-window-exceeded.md  # FR-009
    └── controller-poisoned-item.md           # FR-010
```

**Structure Decision**: Single Go module (`gitstore-controller-manager`), consistent with specs 025/026/036. New integration tests live in a new `tests/integration/` package sitting alongside the existing `tests/contract/` and `tests/checkpoint/` packages, following the same `_test` package convention already used there (e.g. `contract_test`). Runbooks are plain markdown under a new `docs/runbooks/` directory, matching how `docs/` already hosts feature/operational documentation (`docs/configuration.md`, `docs/developer-guide.md`) referenced from the controller-manager sections of `docs/developer-guide.md`.

## Complexity Tracking

*No violations — section not applicable.*
