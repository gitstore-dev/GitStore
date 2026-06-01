# Implementation Plan: In-Process git-receive-pack Hook Pipeline

**Branch**: `013-receive-pack-hooks` | **Date**: 2026-06-01 | **Spec**: [spec.md](spec.md)  
**Input**: Feature specification from `specs/013-receive-pack-hooks/spec.md`

## Summary

Implement a fully in-process hook execution pipeline for `git-receive-pack` in `gitstore-git-service` (Rust). The pipeline executes enabled hook phases — pre-receive, proc-receive, update (per-ref), reference-transaction (three-state: prepared/committed/aborted), and post-receive — in the correct order, enforcing all-or-nothing semantics for pre-receive and per-ref semantics for update, while providing a typed `AdmissionHandler` trait as the integration point for future admission (#105) and validation (#106) services. All hook phases default to disabled; the pipeline emits structured per-phase log events and enforces a 5-second timeout on admission callouts.

## Technical Context

**Language/Version**: Rust edition 2021, MSRV 1.82; actual gix version is `0.84.0` (Cargo.lock canonical)  
**Primary Dependencies**: `gix 0.84.0`, `gix-ref 0.64.0` (two-phase transaction API), `tokio 1.35` (full features), `tonic 0.14`, `tracing 0.1`, `anyhow 1.0`, `async-trait 0.1` (to add)  
**Storage**: Bare Git repositories on local filesystem (unchanged)  
**Testing**: `cargo test` (unit + integration); integration tests use `tempfile` for bare repo fixtures  
**Target Platform**: Linux server (Docker) + macOS dev  
**Project Type**: gRPC service library + binary  
**Performance Goals**: ≤ 50 ms pipeline overhead for no-op phases (SC-005); 5 s admission timeout (FR-009a)  
**Constraints**: No external `git` binary invocation (FR-011); all phase logic in-process  
**Scale/Scope**: Single push at a time per repository (per-repo RwLock already in place)

## Constitution Check

*GATE: Must pass before Phase 0 research. Re-check after Phase 1 design.*

| Principle                         | Status | Notes                                                                                                                    |
|-----------------------------------|--------|--------------------------------------------------------------------------------------------------------------------------|
| I. Test-First                     | ✅ PASS | Tasks will write failing tests before implementation for each phase function and the pipeline orchestrator               |
| II. API-First                     | ✅ PASS | `AdmissionHandler` trait and `HookPipeline` public API defined in `contracts/hook-pipeline.md` before any implementation |
| III. Clear Contracts & Versioning | ✅ PASS | Contract defined; alpha — no semver stability promise needed                                                             |
| IV. Observability                 | ✅ PASS | FR-013 mandates structured per-phase log events; `PhaseLog` contract defined                                             |
| V. User Story Driven              | ✅ PASS | All work maps to US1–US4 in spec; tasks will carry [US1]/[US2] labels                                                    |
| VI. Incremental Delivery          | ✅ PASS | P1 stories (accept/reject) deliverable independently of P2 (config toggles, admission routing)                           |
| VII. Simplicity                   | ✅ PASS | No new config keys; `NoopAdmissionHandler` default; `async-trait` only new dep                                           |

**Post-Design Re-check** (after Phase 1):

| Item                                          | Result                                                                                                                                                                         |
|-----------------------------------------------|--------------------------------------------------------------------------------------------------------------------------------------------------------------------------------|
| `HookPipeline` struct — justified complexity? | Yes: encapsulates phase ordering, toggle enforcement, admission routing, timeouts in one place; alternative (scattered if-guards at call sites) is harder to test and maintain |
| Two-phase gix transaction — added complexity? | Low: replaces one `edit_references()` call with `prepare()` + `commit()` on `repo.refs`; no new abstractions                                                                   |
| `async-trait` dependency                      | Minimal: single dep, established in ecosystem; no speculative use                                                                                                              |

No constitution violations.

## Project Structure

### Documentation (this feature)

```text
specs/013-receive-pack-hooks/
├── plan.md              # This file
├── research.md          # Phase 0 output
├── data-model.md        # Phase 1 output
├── quickstart.md        # Phase 1 output
├── contracts/
│   └── hook-pipeline.md # Phase 1 output
└── tasks.md             # Phase 2 output (/speckit.tasks command)
```

### Source Code Changes

```text
gitstore-git-service/
├── Cargo.toml                         # add async-trait = "0.1"
└── src/
    ├── git/
    │   ├── hooks.rs                   # REPLACE: HookDecision, AdmissionDecision,
    │   │                              #   AdmissionHandler trait, NoopAdmissionHandler,
    │   │                              #   HookPipeline struct, all phase functions
    │   ├── pack_server.rs             # MODIFY: wire HookPipeline into handle_receive_pack;
    │   │                              #   replace edit_references() with two-phase txn
    │   └── mod.rs                     # no changes expected
    └── grpc/
        └── server.rs                  # MODIFY: wire HookPipeline into receive_pack RPC;
                                       #   replace edit_references() with two-phase txn;
                                       #   add async admission call with timeout

tests/                                 # NEW directory at crate root (or gitstore-git-service/tests/)
└── integration/
    ├── hook_pipeline_test.rs          # integration tests (US1–US4)
    └── helpers.rs                     # shared bare-repo fixture helpers
```

**Structure Decision**: Single Rust crate (`gitstore-git-service`). All changes are within the existing crate. Integration tests live in `tests/` at the crate root (Rust convention for integration tests outside `src/`). Unit tests remain co-located in `hooks.rs`.

## Complexity Tracking

No constitution violations requiring justification.

---

## Phase 0: Research Complete

All unknowns resolved. See [research.md](research.md).

Key findings:
1. `gix 0.84.0` exposes `repo.refs.transaction().prepare().commit()` — two-phase ref transaction available natively via `gix_ref::file::Store`.
2. `async-trait = "0.1"` needed for dyn-compatible async `AdmissionHandler` trait.
3. `tokio::time::timeout(Duration::from_secs(5), ...)` — standard pattern, tokio 1.35 full features already present.
4. `receive_pack` gRPC handler is a real `async fn` — timeout wraps the admission `.await` directly.
5. Config toggles and `AdmissionControlConfig` already exist in `config.rs` — no new config keys needed.

---

## Phase 1: Design & Contracts Complete

### Data Model

See [data-model.md](data-model.md).

New types:
- `HookDecision` (enum: Accept | Reject(String))
- `AdmissionDecision` (enum: Accept | Reject(String))
- `AdmissionHandler` (async trait, dyn-compatible)
- `NoopAdmissionHandler` (default no-op implementation)
- `HookPipeline` (struct: config + admission_phase + admission_handler)
- `HookRejection` (struct: phase + reason — error type for pipeline abort)

Modified types:
- `RefUpdate` — existing, no changes
- `HooksConfig` / `GitReceivePackHooks` / `HookToggle` — existing, no changes

### Phase Execution Contract

```
pre-receive (once/push, all-or-nothing)
  └─► [admission check if phase == admission_phase]
  └─► HookDecision::Reject → HookRejection → abort entire push

proc-receive (once/push, all-or-nothing)
  └─► [admission check if phase == admission_phase]
  └─► HookDecision::Reject → HookRejection → abort entire push

update (once/ref, per-ref semantics)
  └─► [admission check if phase == admission_phase]
  └─► HookDecision::Reject for ref N → mark N as ng; continue other refs

reference-transaction/prepared  (once/push, veto allowed — via gix_ref two-phase txn)
  └─► [admission check if phase == admission_phase]
  └─► HookDecision::Reject → txn.rollback() → HookRejection

[promote quarantine + txn.commit()]

reference-transaction/committed (once/push, observation only)

post-receive (once/push, fire-and-forget)
  └─► errors logged at ERROR level, never returned to caller
```

### Interfaces (contracts)

See [contracts/hook-pipeline.md](contracts/hook-pipeline.md).

Public surface:
- `AdmissionHandler` trait — stable integration point for #105/#106
- `HookPipeline::new()` + `HookPipeline::run()` — pipeline entry point
- `HookPipeline::run_reference_transaction_prepared/committed/aborted()` — ref-transaction callbacks
- `HookPipeline::run_post_receive()` — fire-and-forget post-push notification

### Quickstart

See [quickstart.md](quickstart.md).

---

## Implementation Sequence (input to /speckit.tasks)

The following ordered phases feed directly into task generation:

### Setup
1. Add `async-trait = "0.1"` to `gitstore-git-service/Cargo.toml`
2. Create `gitstore-git-service/tests/integration/` directory with placeholder `helpers.rs`

### Foundational — Core Types (blocks all user stories)
3. [US1/US2] Write failing unit tests for `HookDecision`, `AdmissionDecision`, `AdmissionHandler` trait, `NoopAdmissionHandler`
4. [US1/US2] Implement `HookDecision`, `AdmissionDecision`, `AdmissionHandler`, `NoopAdmissionHandler`, `HookRejection` in `hooks.rs`
5. [US1/US2] Write failing unit tests for `HookPipeline::new()` and toggle enforcement (disabled phase = no-op)
6. [US1/US2] Implement `HookPipeline` struct with config wiring

### User Story 1 — Happy Path Push
7. [US1] Write failing integration test: single-ref push accepted, phase order verified in logs
8. [US1] Implement `HookPipeline::run()` — pre-receive → proc-receive → update → reference-transaction/prepared; write phase log events
9. [US1] Modify `pack_server.rs` and `grpc/server.rs` to drive two-phase gix transaction via `repo.refs.transaction().prepare()/commit()`
10. [US1] Wire `HookPipeline` into `handle_receive_pack` and `receive_pack` RPC
11. [US1] Implement `run_reference_transaction_committed()` and `run_post_receive()` (fire-and-forget)
12. [US1] Verify integration test passes

### User Story 2 — Push Rejection
13. [US2] Write failing integration tests: pre-receive rejection (all refs blocked), update rejection (one ref blocked), proc-receive rejection
14. [US2] Implement rejection propagation: pre-receive/proc-receive abort full push; update marks single ref ng
15. [US2] Wire rejection reason into `report-status` ng lines for client visibility
16. [US2] Implement `run_reference_transaction_prepared()` with veto (rollback path)
17. [US2] Verify rejection integration tests pass

### User Story 3 — Config Toggle Enforcement
18. [US3] Write failing integration tests: each phase disabled individually; all disabled
19. [US3] Verify toggle checks short-circuit each phase in `HookPipeline::run()`
20. [US3] Verify US3 integration tests pass

### User Story 4 — Admission Routing
21. [US4] Write failing integration tests: stub handler accept/reject/timeout wired to configured phase
22. [US4] Implement `tokio::time::timeout(5s, handler.admit(...))` in `HookPipeline` async phase calls
23. [US4] Implement admission routing: only call handler when `phase == admission_phase`
24. [US4] Verify US4 integration tests pass

### Polish
25. Verify all `cargo test` pass including config tests
26. Verify `make pr-ready` passes (lint, licence-check, build)
