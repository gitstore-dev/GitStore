---
description: "Task list for In-Process git-receive-pack Hook Pipeline"
---

# Tasks: In-Process git-receive-pack Hook Pipeline

**Input**: Design documents from `/specs/013-receive-pack-hooks/`  
**Prerequisites**: plan.md ✅, spec.md ✅, research.md ✅, data-model.md ✅, contracts/ ✅, quickstart.md ✅

**Tests**: Test-First Development (Constitution Principle I — NON-NEGOTIABLE). Tests MUST be written before implementation and verified to FAIL before implementation begins.

**Organization**: Tasks are grouped by user story to enable independent implementation and testing of each story.

## Format: `[ID] [P?] [Story] Description`

- **[P]**: Can run in parallel (different files, no dependencies on incomplete tasks)
- **[Story]**: Which user story this task belongs to (US1–US4)

---

## Phase 1: Setup (Shared Infrastructure)

**Purpose**: Add the new dependency and create the integration test scaffold. No implementation yet.

- [x] T001 Add `async-trait = "0.1"` to `gitstore-git-service/Cargo.toml` under `[dependencies]`
- [x] T002 Create `gitstore-git-service/tests/integration/` directory; add empty `mod.rs` placeholder so `cargo test` discovers the module
- [x] T003 [P] Create `gitstore-git-service/tests/integration/helpers.rs` — bare-repo fixture helpers: `make_bare_repo(dir) -> PathBuf`, `make_commit(repo_path, msg) -> String` (returns commit OID), `zero_oid() -> &'static str`

**Checkpoint**: `cargo build` succeeds with the new dependency; `cargo test` runs without errors.

---

## Phase 2: Foundational (Blocking Prerequisites)

**Purpose**: Core types and `HookPipeline` struct that ALL user stories depend on. No phase logic yet — just the scaffolding.

**⚠️ CRITICAL**: No user story work can begin until this phase is complete.

### Tests for Foundation (write first, verify FAIL)

- [x] T004 Write unit tests in `gitstore-git-service/src/git/hooks.rs` verifying:
  - `HookDecision::Reject("reason")` carries the reason string
  - `AdmissionDecision::Reject("reason")` carries the reason string
  - `NoopAdmissionHandler::admit()` always returns `Ok(Accept)`
  - `HookRejection` has accessible `phase` and `reason` fields

- [x] T005 Write unit tests in `gitstore-git-service/src/git/hooks.rs` verifying `HookPipeline` toggle enforcement:
  - Pipeline with all toggles disabled runs `run()` and returns all indices as accepted with zero phase invocations (assert no panic, no admission call)
  - Pipeline with only `pre_receive.enabled = true` invokes noop pre-receive and returns accepted

### Implementation

- [x] T006 Replace existing ad-hoc types in `gitstore-git-service/src/git/hooks.rs` with:
  - `pub enum HookDecision { Accept, Reject(String) }`
  - `pub enum AdmissionDecision { Accept, Reject(String) }`
  - `pub struct HookRejection { pub phase: String, pub reason: String }`
  - `#[async_trait] pub trait AdmissionHandler: Send + Sync { async fn admit(...) }`
  - `pub struct NoopAdmissionHandler;` + `impl AdmissionHandler for NoopAdmissionHandler`
  - Add `use async_trait::async_trait;` import

- [x] T007 Add `pub struct HookPipeline` to `gitstore-git-service/src/git/hooks.rs` with fields: `config: GitReceivePackHooks`, `admission_phase: String`, `admission_handler: Arc<dyn AdmissionHandler + Send + Sync>`; add `HookPipeline::new()` constructor; add stub `async fn run()` returning `Ok((0..updates.len()).collect())` (all accepted, no phase logic yet — will be filled in per story)

- [x] T008 Update existing `run_pre_receive`, `run_update_hooks`, `run_post_receive` signatures in `gitstore-git-service/src/git/hooks.rs` to return `HookDecision` / `Vec<(usize, HookDecision)>` respectively, keeping bodies as no-op (always accept) so existing call sites still compile

- [x] T009 Update call sites in `gitstore-git-service/src/git/pack_server.rs` and `gitstore-git-service/src/grpc/server.rs` to handle the new return types without changing behaviour — ensure `cargo test` still passes after this change

**Checkpoint**: `cargo test` passes. All foundation unit tests pass (T004, T005). Hook types exist and compile.

---

## Phase 3: User Story 1 — Push Accepted Through Enabled Hook Phases (Priority: P1) 🎯 MVP

**Goal**: A push with all hook phases enabled executes pre-receive → proc-receive → update → reference-transaction/prepared → committed → post-receive in order, refs are updated, client receives success.

**Independent Test**: `cargo test` with integration test `test_push_accepted_all_phases_enabled` passes; log output contains one event per phase in the correct order.

### Tests for User Story 1 (write first, verify FAIL)

- [x] T010 [US1] Write integration test `test_push_accepted_all_phases_enabled` in `gitstore-git-service/tests/integration/hook_pipeline_test.rs`:
  - Create a bare repo with one commit using helpers from T003
  - Construct a `HookPipeline` with all phases enabled and `NoopAdmissionHandler`
  - Build a `RefUpdate` advancing `refs/heads/main` to a new commit OID
  - Call `pipeline.run(git_dir, &[update])` and assert `Ok(vec![0])` (index 0 accepted)
  - Assert `pipeline.run_reference_transaction_prepared(git_dir, &[update])` returns `Ok(())`
  - Verify test FAILS before implementation

- [x] T011 [US1] Write unit test in `gitstore-git-service/src/git/hooks.rs` asserting per-phase structured log events are emitted (use `tracing_test` or capture span fields manually): pre-receive log contains `phase="pre-receive"`, `outcome="accepted"`, `duration_ms` field present

### Implementation for User Story 1

- [x] T012 [US1] Implement `HookPipeline::run()` in `gitstore-git-service/src/git/hooks.rs`:
  - Check `config.pre_receive.enabled`; if enabled call `run_pre_receive` and call admission handler if `admission_phase == "pre-receive"` (with `tokio::time::timeout(5s, ...)`); on `Reject` return `Err(HookRejection { phase: "pre-receive", reason })`
  - Check `config.proc_receive.enabled`; if enabled call `run_proc_receive` stub + admission routing for `"proc-receive"`; on `Reject` return `Err(HookRejection)`
  - For each ref update: check `config.update.enabled`; call `run_update` + admission routing for `"update"`; collect accepted indices (per-ref semantics — rejections mark that ref ng, others continue)
  - Emit structured `tracing::info!` / `tracing::warn!` per phase with `phase`, `duration_ms`, `outcome` fields (and `ref_name` for update, `reason` for reject)
  - Return `Ok(accepted_indices)`

- [x] T013 [US1] Add `pub fn run_proc_receive(git_dir: &Path, updates: &[RefUpdate]) -> HookDecision` stub (always `Accept`) in `gitstore-git-service/src/git/hooks.rs`

- [x] T014 [US1] Implement `HookPipeline::run_reference_transaction_prepared()` in `gitstore-git-service/src/git/hooks.rs`:
  - Check `config` for reference-transaction toggle (add `reference_transaction: HookToggle` field to `GitReceivePackHooks` in `gitstore-git-service/src/config.rs` with default `enabled = false`)
  - If enabled: call admission handler if `admission_phase == "reference-transaction/prepared"` (with 5s timeout); on `Reject` return `Err(HookRejection { phase: "reference-transaction/prepared", reason })`
  - Emit structured log event; return `Ok(())`

- [x] T015 [US1] Add `pub fn run_reference_transaction_committed()` and `pub fn run_reference_transaction_aborted()` stubs in `gitstore-git-service/src/git/hooks.rs` — both emit a structured `tracing::info!` event and return `()`

- [x] T016 [US1] Implement `HookPipeline::run_post_receive()` in `gitstore-git-service/src/git/hooks.rs`:
  - Check `config.post_receive.enabled`; if enabled call `run_post_receive` stub
  - Catch any error with `if let Err(e) = ...`; log at `tracing::error!` with `phase="post-receive"` and `reason`; never propagate

- [x] T017 [US1] Replace `repo.edit_references()` in `gitstore-git-service/src/git/pack_server.rs` with the two-phase gix transaction:
  - Build `ref_edits` as before
  - Call `let txn = repo.refs.transaction().prepare(ref_edits, lock_fail_mode, packed_lock_fail)?;`
  - Call `pipeline.run_reference_transaction_prepared(git_dir, accepted_updates).await?` (or blocking equivalent in this sync context — see Decision 7 in research.md: use a `NoopAdmissionHandler` here, admission only in gRPC path)
  - On Ok: call `txn.commit(committer)?` then `run_reference_transaction_committed()`
  - On Err: call `txn.rollback()` (or drop) then `run_reference_transaction_aborted()`; propagate error

- [x] T018 [US1] Replace `repo.edit_references()` in `gitstore-git-service/src/grpc/server.rs::receive_pack` with the two-phase gix transaction using the same pattern as T017; wire `HookPipeline` (constructed from `AppConfig` passed as `Arc<AppConfig>` into `GitServiceImpl`) into the `receive_pack` RPC flow:
  - Add `hook_pipeline: Arc<HookPipeline>` field to `GitServiceImpl`; update `GitServiceImpl::new()` to accept and store it
  - In `receive_pack`: call `pipeline.run(git_dir, &ref_updates).await` to get accepted indices; build ref edits from accepted indices only; run two-phase transaction; call `pipeline.run_post_receive()`

- [x] T019 [US1] Update `gitstore-git-service/src/main.rs` to construct `HookPipeline` from loaded `AppConfig` and pass it into `GitServiceImpl::new()`

**Checkpoint**: `cargo test` passes including T010 and T011. A real `git push` via `make dev` succeeds with per-phase log events visible.

---

## Phase 4: User Story 2 — Push Rejected by a Hook Phase (Priority: P1)

**Goal**: A push that violates a pre-receive or update policy is rejected — pre-receive blocks all refs (all-or-nothing); update blocks only the rejected ref (per-ref). Client sees rejection reason.

**Independent Test**: `cargo test` with integration tests `test_push_rejected_pre_receive`, `test_push_rejected_update_one_ref`, `test_push_rejected_proc_receive` all pass; rejected refs appear in report-status `ng` lines with the reason.

### Tests for User Story 2 (write first, verify FAIL)

- [x] T020 [US2] Write integration test `test_push_rejected_pre_receive` in `gitstore-git-service/tests/integration/hook_pipeline_test.rs`:
  - Create a `RejectingAdmissionHandler("blocked by policy")` test double that always returns `Reject`
  - Construct `HookPipeline` with `pre_receive.enabled = true`, `admission_phase = "pre-receive"`, wired to `RejectingAdmissionHandler`
  - Call `pipeline.run(git_dir, &[update1, update2])` and assert `Err(HookRejection { phase: "pre-receive", .. })`
  - Verify test FAILS before implementation

- [x] T021 [US2] Write integration test `test_push_rejected_update_one_ref` in `gitstore-git-service/tests/integration/hook_pipeline_test.rs`:
  - Construct `HookPipeline` with `update.enabled = true`; use `PerRefRejectingHandler` that rejects ref index 1 only
  - Call `pipeline.run(git_dir, &[update0, update1, update2])` and assert `Ok(vec![0, 2])` (index 1 rejected, others accepted)
  - Verify test FAILS before implementation

- [x] T022 [P] [US2] Write integration test `test_push_rejected_proc_receive` in `gitstore-git-service/tests/integration/hook_pipeline_test.rs`: proc-receive rejection returns `Err(HookRejection { phase: "proc-receive", .. })`

- [x] T023 [P] [US2] Write integration test `test_reference_transaction_veto` in `gitstore-git-service/tests/integration/hook_pipeline_test.rs`:
  - `run_reference_transaction_prepared()` with rejecting handler returns `Err(HookRejection { phase: "reference-transaction/prepared", .. })`

### Implementation for User Story 2

- [x] T024 [US2] Wire `RejectingAdmissionHandler` test double into `gitstore-git-service/tests/integration/helpers.rs` (or inline in test file): a struct implementing `AdmissionHandler` that returns `Reject(reason)` for all calls; also add `PerRefRejectingHandler(HashSet<usize>)` that rejects specific ref indices

- [x] T025 [US2] Verify `HookPipeline::run()` pre-receive rejection path returns `Err(HookRejection)` carrying the exact reason string from the admission handler (should already work from T012; confirm with T020 passing)

- [x] T026 [US2] Verify `HookPipeline::run()` update per-ref rejection correctly excludes only the rejected ref index from the accepted set and continues processing remaining refs (confirm with T021 passing)

- [x] T027 [US2] Update `report-status` building in `gitstore-git-service/src/grpc/server.rs::receive_pack` and `gitstore-git-service/src/git/pack_server.rs::handle_receive_pack` to:
  - On `Err(HookRejection)` from `pipeline.run()`: emit `ng <all-refs> <reason>` for all requested refs (pre-receive / proc-receive rejection blocks entire push)
  - On `Ok(accepted_indices)`: emit `ng <ref> <reason>` for rejected refs using the rejection reason captured during the update phase iteration; emit `ok <ref>` for accepted refs

- [x] T028 [US2] Verify `HookPipeline::run_reference_transaction_prepared()` rejection causes `txn.rollback()` to be called (lock files dropped) and no refs are written — confirm via T023

**Checkpoint**: `cargo test` passes including T020–T023. Rejection reasons appear verbatim in client `git push` output.

---

## Phase 5: User Story 3 — Selectively Disable Hook Phases via Configuration (Priority: P2)

**Goal**: Each hook phase can be independently disabled via config toggle; disabled phases are skipped with zero overhead and zero log output.

**Independent Test**: `cargo test` with integration tests `test_all_phases_disabled`, `test_single_phase_enabled` pass; log capture confirms no hook events emitted for disabled phases.

### Tests for User Story 3 (write first, verify FAIL)

- [x] T029 [US3] Write integration test `test_all_phases_disabled` in `gitstore-git-service/tests/integration/hook_pipeline_test.rs`:
  - Construct `HookPipeline` with all toggles `enabled = false`
  - Assert `pipeline.run(git_dir, &[update])` returns `Ok(vec![0])` and no `hook_phase_complete` log events are emitted
  - Verify test FAILS before T030 if phase logging is unconditional

- [x] T030 [P] [US3] Write integration test `test_only_pre_receive_enabled` in `gitstore-git-service/tests/integration/hook_pipeline_test.rs`:
  - Enable only `pre_receive`; assert noop pre-receive runs (one log event) and update/proc-receive/post-receive emit no log events

- [x] T031 [P] [US3] Write integration test `test_reference_transaction_disabled` in `gitstore-git-service/tests/integration/hook_pipeline_test.rs`:
  - `reference_transaction.enabled = false`; assert `run_reference_transaction_prepared()` returns `Ok(())` immediately with no log event

### Implementation for User Story 3

- [x] T032 [US3] Audit `HookPipeline::run()` and `run_reference_transaction_prepared()` in `gitstore-git-service/src/git/hooks.rs` — confirm each phase arm is guarded by `if self.config.<phase>.enabled { ... }` and emits no log event when disabled; fix any missing guards (should largely be in place from Phase 3 implementation)

- [x] T033 [US3] Add `reference_transaction: HookToggle` to `GitReceivePackHooks` in `gitstore-git-service/src/config.rs` with default `{ enabled = false }` in `default_toml()` (may already exist from T014 — verify and add if missing)

- [x] T034 [US3] Verify existing config tests in `gitstore-git-service/src/config.rs` still pass after adding the new toggle; add a test asserting `reference_transaction.enabled` defaults to `false`

**Checkpoint**: `cargo test` passes including T029–T031. Toggling any single phase on/off has no effect on other phases.

---

## Phase 6: User Story 4 — Hook Pipeline Provides Routing Points for Admission Services (Priority: P2)

**Goal**: A configurable admission handler is called at the phase specified by `GITSTORE_ADMISSION_CONTROL_VALIDATING_ADMISSION_POLICY_PHASE`; its accept/reject decision controls the push outcome; calls time out in 5 seconds (fail-closed).

**Independent Test**: `cargo test` with integration tests `test_admission_accept`, `test_admission_reject`, `test_admission_timeout_fail_closed` all pass.

### Tests for User Story 4 (write first, verify FAIL)

- [x] T035 [US4] Write integration test `test_admission_accept` in `gitstore-git-service/tests/integration/hook_pipeline_test.rs`:
  - Wire `NoopAdmissionHandler` to `admission_phase = "pre-receive"`, enable pre-receive
  - Assert push accepted

- [x] T036 [US4] Write integration test `test_admission_reject_with_reason` in `gitstore-git-service/tests/integration/hook_pipeline_test.rs`:
  - Wire `RejectingAdmissionHandler("policy violation")` to `admission_phase = "update"`, enable update
  - Assert `pipeline.run()` returns `Ok` but with empty accepted indices (all refs rejected via update) and rejection reason `"policy violation"` captured

- [x] T037 [US4] Write integration test `test_admission_timeout_fail_closed` in `gitstore-git-service/tests/integration/hook_pipeline_test.rs`:
  - Create `SlowAdmissionHandler` that sleeps 10 seconds before returning
  - Wire to `admission_phase = "pre-receive"`, enable pre-receive
  - Assert `pipeline.run()` returns `Err(HookRejection { reason: "admission service timeout", .. })` within ~6 seconds
  - Verify test FAILS before T038

- [x] T038 [P] [US4] Write integration test `test_admission_only_called_at_configured_phase` in `gitstore-git-service/tests/integration/hook_pipeline_test.rs`:
  - Wire counting handler to `admission_phase = "update"`, enable pre-receive + update
  - Assert handler is called exactly N times (once per ref) for update phase, and zero times for pre-receive phase

### Implementation for User Story 4

- [x] T039 [US4] Add `SlowAdmissionHandler` test double to `gitstore-git-service/tests/integration/helpers.rs`: implements `AdmissionHandler`, sleeps `tokio::time::sleep(Duration::from_secs(10)).await` before returning `Ok(Accept)`

- [x] T040 [US4] Verify `HookPipeline::run()` wraps every admission `handler.admit()` call with `tokio::time::timeout(Duration::from_secs(5), ...)` and maps `Err(Elapsed)` to `HookRejection { reason: "admission service timeout".to_string(), .. }` — confirm via T037 passing

- [x] T041 [US4] Verify admission routing guard `if phase_name == self.admission_phase { ... }` is correctly placed at each phase call site in `gitstore-git-service/src/git/hooks.rs` — handler must NOT be called at phases other than the configured one; confirm via T038 passing

- [x] T042 [US4] Add `CountingAdmissionHandler` test double to `gitstore-git-service/tests/integration/helpers.rs`: wraps an `Arc<AtomicUsize>` call counter, always returns `Accept`

**Checkpoint**: `cargo test` passes including T035–T038. Admission handler is called exactly at the configured phase with the 5-second timeout enforced.

---

## Phase 7: Polish & Cross-Cutting Concerns

**Purpose**: Final validation, documentation, PR readiness.

- [x] T043 [P] Run `cargo clippy -- -D warnings` in `gitstore-git-service/` and fix any lint warnings introduced by this feature
- [x] T044 [P] Run `cargo test` for the full `gitstore-git-service` crate and confirm all pre-existing tests still pass (no regressions in config, gRPC, pack_server tests)
- [x] T045 Update `docs/` per CLAUDE.md guideline: add a section describing the hook pipeline, config toggles, and admission handler integration point
- [x] T046 Run `make pr-ready` from repo root and fix any failures (lint, license-check, build across all modules)
- [x] T047 Walk through `specs/013-receive-pack-hooks/quickstart.md` steps against a running `make dev` instance to verify the happy path, phase log events, and toggle behaviour

**Checkpoint**: `make pr-ready` passes. quickstart.md validated end-to-end.

---

## Dependencies & Execution Order

### Phase Dependencies

- **Setup (Phase 1)**: No dependencies — start immediately
- **Foundational (Phase 2)**: Depends on Phase 1 — **BLOCKS all user stories**
- **US1 (Phase 3)**: Depends on Phase 2 — first P1 story, enables MVP
- **US2 (Phase 4)**: Depends on Phase 2 — second P1 story; depends on US1 types being present (HookRejection, run_reference_transaction_prepared) but independently testable
- **US3 (Phase 5)**: Depends on Phase 2 — can start after Phase 2 in parallel with US1/US2; toggle enforcement is a property of the existing pipeline structure
- **US4 (Phase 6)**: Depends on Phase 3 (admission handler wiring lives in HookPipeline::run() from T012) — must follow US1
- **Polish (Phase 7)**: Depends on all desired user stories complete

### User Story Dependencies

- **US1 (P1)**: No story dependencies — enables the pipeline scaffold
- **US2 (P1)**: Requires US1 types (`HookRejection`, `run_reference_transaction_prepared`) — tightly related, treat as sequential with US1
- **US3 (P2)**: No story dependencies beyond Foundational — toggle enforcement is a guard on the existing pipeline; can develop in parallel with US1/US2
- **US4 (P2)**: Requires US1 (admission call sites exist in `HookPipeline::run()`) — must follow US1

### Within Each User Story

1. Write tests → verify they FAIL
2. Implement → verify tests PASS
3. Fix any regressions in existing tests
4. Checkpoint before moving to next phase

### Parallel Opportunities

- T002 + T003 (Phase 1) — parallel: different files
- T004 + T005 (Phase 2 tests) — parallel: same file but non-conflicting test functions
- T020 + T021 + T022 + T023 (Phase 4 tests) — all parallel: non-conflicting test functions in integration test file
- T029 + T030 + T031 (Phase 5 tests) — parallel: non-conflicting test functions
- T035 + T036 + T037 + T038 (Phase 6 tests) — parallel: non-conflicting test functions
- T043 + T044 (Phase 7) — parallel: different commands, read-only

---

## Parallel Example: User Story 2 Tests

```bash
# All US2 tests can be written in parallel (different test functions, same file):
Task T020: "Write test_push_rejected_pre_receive"
Task T021: "Write test_push_rejected_update_one_ref"
Task T022: "Write test_push_rejected_proc_receive"
Task T023: "Write test_reference_transaction_veto"
```

---

## Implementation Strategy

### MVP First (User Stories 1 + 2 — both P1)

1. Complete Phase 1: Setup
2. Complete Phase 2: Foundational — **BLOCKS everything**
3. Complete Phase 3: US1 (happy path push, phase ordering, structured logs)
4. Complete Phase 4: US2 (rejection, per-ref semantics, client-visible reason)
5. **STOP and VALIDATE**: `make dev` + `git push` with rejecting handler; verify log output and client error message
6. Deploy/demo if ready — this is the complete P1 hook pipeline

### Full Delivery (all four stories)

1. Phases 1–4 (MVP above)
2. Phase 5: US3 (config toggle enforcement)
3. Phase 6: US4 (admission routing + timeout)
4. Phase 7: Polish

### Parallel Team Strategy

With two developers after Phase 2 completes:
- **Developer A**: US1 (Phase 3) → US2 (Phase 4) — critical path P1
- **Developer B**: US3 (Phase 5) — config toggles, fully independent of US1/US2 implementation details

---

## Notes

- `[P]` tasks = different files or non-conflicting test functions — safe to run in parallel
- Each user story is independently completable and testable via its integration tests
- Constitution Principle I: every test task MUST be verified to FAIL before the corresponding implementation task begins
- The `reference_transaction` toggle (T014, T033) may already exist from Phase 3 implementation — check before adding to avoid duplication
- `GitServiceImpl` constructor change (T018, T019) touches `main.rs` and test fixture helpers — coordinate to avoid conflict if working in parallel
- Commit after each checkpoint using Conventional Commits (`feat:`, `test:`, `refactor:`)
