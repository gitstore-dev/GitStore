# Implementation Plan: Controller Manager Runtime Foundations

**Branch**: `025-controller-manager-runtime` | **Date**: 2026-06-11 | **Spec**: [spec.md](spec.md)  
**Input**: Feature specification from `specs/025-controller-manager-runtime/spec.md`

## Summary

Define and implement the controller manager runtime in the `gitstore-controller-manager` module: a rate-limited, deduplicated work queue; per-kind bounded worker pools; exponential backoff with poison-item quarantine and operator-triggered re-queue; an informer cache that enforces level-triggered reconciliation; and a Prometheus-backed health surface with stall detection. This is the runtime foundation that all subsequent controller specs (#181, #182, #183) build on.

## Technical Context

**Language/Version**: Go 1.25 (`gitstore-controller-manager`)  
**Primary Dependencies**: `golang.org/x/time` (queue rate limiting), `github.com/alitto/pond/v2` (worker pools), `github.com/cenkalti/backoff/v5` (retry/backoff), `github.com/prometheus/client_golang v1.23.2` (health metrics), `net/http` stdlib (health/poison API)  
**Storage**: In-memory only (sync.RWMutex maps) — no persistence in this spec  
**Testing**: `go test ./...`; constitution requires contract tests (failing first) + integration tests  
**Target Platform**: Linux server (same as `gitstore-api`)  
**Project Type**: Internal service binary (separate module from `gitstore-api`)  
**Performance Goals**: Work item dispatched within 100ms under non-saturated queue (SC-001); health surface reflects queue state within 5s (SC-004)  
**Constraints**: No `k8s.io/*` dependencies; no persistence layer in this spec; multi-node distribution out of scope  
**Scale/Scope**: Single-process; handles all registered resource kinds for the controller manager

## Constitution Check

*GATE: Must pass before implementation begins. Re-check after Phase 1 design.*

| Principle | Status | Notes |
|---|---|---|
| I. Test-First | PASS | Contract tests and integration tests written before implementation code. All user stories have independently testable acceptance criteria. |
| II. API-First | PASS | Go interfaces (`Reconciler`, `Queue`, `QuarantineStore`, `InformerCache`) and HTTP contract (`contracts/http-api.md`) defined in this plan before implementation. |
| III. Clear Contracts & Versioning | PASS | HTTP API versioned at `/controller/v1`; Go interfaces are the internal contract; breaking changes follow semantic versioning. |
| IV. Observability | PASS | Structured logging required for all reconcile events, retries, quarantines, and re-queues. Prometheus metrics defined. |
| V. User Story Driven | PASS | All tasks tagged US1–US4; P1 (reconciler registration + dispatch) is the MVP. |
| VI. Incremental Delivery | PASS | P1 delivers a working queue + reconciler dispatch. P2 adds retry/quarantine. P3 adds level-triggered cache. P4 adds health surface. Each deploys independently. |
| VII. Simplicity | PASS | 4 new dependencies, all small and focused. No persistence, no distributed coordination, no speculative features. New module justified: separate binary mirrors k8s pattern, enables independent deployment. |

## Complexity Tracking

| Justification | Why Needed | Simpler Alternative Rejected Because |
|---|---|---|
| 4th Go module (`gitstore-controller-manager`) | Independent binary lifecycle; mirrors k8s controller-manager separation | Embedding in `gitstore-api` couples API server and controller manager deployments; conflicts with confirmed architectural decision |
| `pond/v2` worker pool library | Per-kind bounded concurrency with live resize (FR-010); exports metrics directly usable for health surface | `errgroup` panics on `SetLimit` while goroutines run; hand-rolling pool + metrics is ~300 lines with no upside |

## Project Structure

### Documentation (this feature)

```text
specs/025-controller-manager-runtime/
├── plan.md              ← this file
├── research.md          ← Phase 0 output
├── data-model.md        ← Phase 1 output
├── quickstart.md        ← Phase 1 output
├── contracts/
│   └── http-api.md      ← Phase 1 output
└── tasks.md             ← Phase 2 output (/speckit.tasks)
```

### Source Code

```text
gitstore-controller-manager/
├── go.mod                          ← add 4 new dependencies
├── cmd/
│   └── controller/
│       └── main.go                 ← wire manager, register reconcilers, start HTTP + manager
├── internal/
│   ├── manager/
│   │   └── manager.go              ← Manager struct: Register, Start, Stop, Cache
│   ├── queue/
│   │   ├── queue.go                ← typed dedup queue (dirty/processing/queue sets)
│   │   └── queue_test.go
│   ├── worker/
│   │   ├── pool.go                 ← per-kind pond/v2 worker pool wrapper
│   │   └── pool_test.go
│   ├── retry/
│   │   ├── retry.go                ← cenkalti/backoff retry loop + RetryRecord
│   │   ├── quarantine.go           ← QuarantineStore (sync.RWMutex map)
│   │   └── retry_test.go
│   ├── cache/
│   │   ├── cache.go                ← InformerCache[T] generic implementation
│   │   └── cache_test.go
│   ├── health/
│   │   ├── metrics.go              ← Prometheus GaugeVec/CounterVec registration
│   │   ├── stall.go                ← background stall detector goroutine
│   │   └── handler.go             ← /health JSON handler
│   └── api/
│       └── poison.go               ← /controller/v1/poison/* HTTP handlers
└── tests/
    ├── contract/
    │   └── reconciler_contract_test.go   ← Reconciler interface contract tests (RED first)
    └── integration/
        └── manager_integration_test.go   ← Full manager lifecycle tests (RED first)
```

## Implementation Phases

### Phase 0 — Research ✅

All NEEDS CLARIFICATION items resolved. See [research.md](research.md).

**Resolved decisions:**
- Work queue: hand-rolled generic + `golang.org/x/time/rate`
- Worker pool: `github.com/alitto/pond/v2`
- Retry engine: `github.com/cenkalti/backoff/v5`
- Informer cache: stdlib `sync.RWMutex` map
- Health metrics: `github.com/prometheus/client_golang`
- Re-queue API: `net/http`
- Module placement: `gitstore-controller-manager` (separate binary)
- Cache ownership: inside `gitstore-controller-manager`
- Poison recovery: explicit operator re-queue via HTTP API

### Phase 1 — Design & Contracts ✅

All Phase 1 artifacts complete:
- [data-model.md](data-model.md) — entities, state transitions, Go interfaces
- [contracts/http-api.md](contracts/http-api.md) — HTTP API contract
- [quickstart.md](quickstart.md) — developer guide

### Phase 2 — Tasks

Produced by `/speckit.tasks`. Task breakdown will cover:

**US1 — Reconciler Registration and Dispatch (P1, MVP)**
1. Write failing contract test: `Reconciler` interface is callable with `WorkItemKey`
2. Write failing integration test: register reconciler, enqueue item, assert invoked once
3. Implement `internal/queue` — typed dedup queue with dirty/processing/queue sets
4. Implement `internal/worker` — per-kind pond/v2 pool wrapper
5. Implement `internal/manager` — `Register()` and dispatch loop
6. Wire `cmd/controller/main.go`
7. Verify all US1 tests pass

**US2 — Retry / Backoff / Poison Quarantine (P2)**
1. Write failing contract test: `RetryRecord` and `QuarantineStore` contracts
2. Write failing integration test: handler always fails → observe quarantine after MaxAttempts
3. Implement `internal/retry` — cenkalti/backoff loop + RetryRecord
4. Implement `internal/retry/quarantine.go` — QuarantineStore
5. Implement `internal/api/poison.go` — list + requeue HTTP handlers
6. Wire health surface poison count into Prometheus (internal/health/metrics.go)
7. Verify all US2 tests pass

**US3 — Level-Triggered Informer Cache (P3)**
1. Write failing contract test: `InformerCache[T]` interface contract
2. Write failing integration test: cache miss triggers live call; cache hit does not
3. Implement `internal/cache` — generic `sync.RWMutex` map with `HasSynced()`
4. Add event handler callback support to cache
5. Verify all US3 tests pass

**US4 — Health Surface (P4)**
1. Write failing integration test: start manager, query `/health`, assert per-kind fields present
2. Implement `internal/health/metrics.go` — Prometheus gauge registrations
3. Implement `internal/health/stall.go` — background stall detector
4. Implement `internal/health/handler.go` — `/health` JSON endpoint
5. Verify all US4 tests pass

**Cross-cutting**
- Structured logging (go.uber.org/zap) for all reconcile, retry, quarantine, and re-queue events
- Add `make controller` target to root Makefile
- Update `AGENTS.md` with new module and commands
