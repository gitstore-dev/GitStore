# Tasks: Hook Phase Startup Observability and Env-Var Validation

**Input**: Design documents from `/specs/029-hook-startup-observability/`  
**Prerequisites**: plan.md ✅, spec.md ✅

**Tests**: Test-First Development (Constitution Principle I - NON-NEGOTIABLE). Tests MUST be written before implementation.

**Organization**: Tasks are grouped by user story to enable independent implementation and testing of each story.

## Format: `[ID] [P?] [Story] Description`

- **[P]**: Can run in parallel (different files, no dependencies)
- **[Story]**: Which user story this task belongs to (US1, US2)
- Include exact file paths in descriptions

## Scope Summary

This feature requires changes to **two files** in the Rust git service only. No Go, proto, or datastore changes are needed.

| File | Change |
|------|--------|
| `gitstore-git-service/src/main.rs` | US1: add startup `info!()` log enumerating hook phases and admission_phase |
| `gitstore-git-service/src/config.rs` | US2: add two env-var round-trip unit tests for hook toggles |

---

## Phase 1: Setup (Shared Infrastructure)

**Purpose**: No new project structure or dependencies required — this is a focused addition to two existing files.

*(No setup tasks needed. Proceed directly to Phase 2.)*

---

## Phase 2: Foundational (Blocking Prerequisites)

**Purpose**: Confirm the existing hook-phase and config infrastructure is testable before adding to it.

**⚠️ CRITICAL**: Both user stories depend on the existing `HooksConfig` struct and `load_config_from` function being accessible. Verify the codebase compiles cleanly before starting.

- [x] T001 Verify `cd gitstore-git-service && cargo build` succeeds with zero errors before any changes

**Checkpoint**: Clean build confirmed — US1 and US2 can proceed in parallel.

---

## Phase 3: User Story 1 — Startup log enumerates enabled hook phases (Priority: P1) 🎯 MVP

**Goal**: Emit one structured `info!()` log line at startup that lists every `git_receive_pack` phase toggle and the `admission_phase`, so operators can confirm environment variables took effect without making a push.

**Independent Test**: Start the binary with a custom `GITSTORE_HOOKS__GIT_RECEIVE_PACK__UPDATE__ENABLED=true`; observe the JSON log line contains `"update":true` and `"admission_phase":"post-receive"`.

### Tests for User Story 1 ⚠️

> **NOTE: Write this test FIRST — it should FAIL (no log line exists yet) before T003 is implemented.**

- [x] T002 [US1] Write a failing unit test in `gitstore-git-service/src/main.rs` (or a separate `tests/startup_log_test.rs`) that verifies `HooksConfig` fields are readable and would appear in a log — confirm it compiles but the corresponding log line does not yet exist (test can simply assert the field names exist on the struct to pin the API surface)

### Implementation for User Story 1

- [x] T003 [US1] Add a structured `info!()` call in `gitstore-git-service/src/main.rs` immediately after the existing "Starting GitStore Server" log (line ~56), emitting:
  - `pre_receive = cfg.hooks.git_receive_pack.pre_receive.enabled`
  - `update = cfg.hooks.git_receive_pack.update.enabled`
  - `post_receive = cfg.hooks.git_receive_pack.post_receive.enabled`
  - `proc_receive = cfg.hooks.git_receive_pack.proc_receive.enabled`
  - `post_update = cfg.hooks.git_receive_pack.post_update.enabled`
  - `reference_transaction = cfg.hooks.git_receive_pack.reference_transaction.enabled`
  - `admission_phase = %cfg.admission_control.phase`
  - message: `"hook phases"`

- [x] T004 [US1] Run `cd gitstore-git-service && cargo build` and confirm T003 compiles without warnings

**Checkpoint**: Binary built successfully; the "hook phases" log line is present. A manual run (`GITSTORE_LOG__FORMAT=json cargo run -- --config-file /dev/null 2>&1 | head -5`) will show the fields without making any push.

---

## Phase 4: User Story 2 — Env-var names for hook toggles round-trip correctly (Priority: P2)

**Goal**: Two unit tests in `config.rs` confirm that the double-underscore env-var names (`GITSTORE_HOOKS__GIT_RECEIVE_PACK__<PHASE>__ENABLED`) deserialise correctly through config-rs, preventing silent regressions if struct field names or the separator convention change.

**Independent Test**: `cargo test test_hook_toggle_env_vars` — both new tests must be green.

### Tests for User Story 2 ⚠️

> **NOTE: Write these tests FIRST — they should FAIL (config-rs may or may not support the nested key yet) before confirming. If they pass immediately, document that the round-trip already worked and the tests serve as a lock.**

- [x] T005 [US2] Add `test_hook_toggle_env_vars_pre_receive_and_post_receive_round_trip` to the `#[cfg(test)]` block in `gitstore-git-service/src/config.rs`:
  - Acquire `ENV_LOCK`
  - Call `clear_env()`
  - Add `GITSTORE_HOOKS__GIT_RECEIVE_PACK__PRE_RECEIVE__ENABLED` and `GITSTORE_HOOKS__GIT_RECEIVE_PACK__POST_RECEIVE__ENABLED` to the `clear_env()` key list (if not already present)
  - Set `GITSTORE_HOOKS__GIT_RECEIVE_PACK__PRE_RECEIVE__ENABLED=false`
  - Set `GITSTORE_HOOKS__GIT_RECEIVE_PACK__POST_RECEIVE__ENABLED=false`
  - Call `load_config_from(None)` and assert `hooks.git_receive_pack.pre_receive.enabled == false`
  - Assert `hooks.git_receive_pack.post_receive.enabled == false`
  - Assert `hooks.git_receive_pack.update.enabled == false` (default)
  - Call `clear_env()`

- [x] T006 [US2] Add `test_hook_toggle_env_var_update_enabled_round_trip` to the same `#[cfg(test)]` block in `gitstore-git-service/src/config.rs`:
  - Acquire `ENV_LOCK`
  - Call `clear_env()`
  - Add `GITSTORE_HOOKS__GIT_RECEIVE_PACK__UPDATE__ENABLED` to the `clear_env()` key list (if not already present)
  - Set `GITSTORE_HOOKS__GIT_RECEIVE_PACK__UPDATE__ENABLED=true`
  - Call `load_config_from(None)` and assert `hooks.git_receive_pack.update.enabled == true`
  - Assert `hooks.git_receive_pack.pre_receive.enabled == true` (default unchanged)
  - Call `clear_env()`

- [x] T007 [US2] Run `cd gitstore-git-service && cargo test test_hook_toggle_env_vars` and confirm both tests pass (if they fail, the env-var mapping is broken — fix the separator or struct field names in `config.rs` to match)

**Checkpoint**: Both round-trip tests are green. The env-var names are pinned and any future struct rename will produce a test failure rather than a silent config regression.

---

## Phase 5: Polish & Cross-Cutting Concerns

**Purpose**: Full test suite validation and documentation update.

- [x] T008 [P] Run the full Rust test suite to confirm no regressions: `cd gitstore-git-service && cargo test`
- [x] T009 [P] Update `docs/products/` or the relevant observability doc to mention the "hook phases" startup log line and the env-var naming convention

---

## Dependencies & Execution Order

### Phase Dependencies

- **Foundational (Phase 2)**: No dependencies — run immediately
- **US1 (Phase 3)**: Depends on T001 (clean build) — T002 and T003 are sequential within US1
- **US2 (Phase 4)**: Independent of US1 — can start after T001
- **Polish (Phase 5)**: Depends on US1 and US2 completion

### User Story Dependencies

- **User Story 1 (P1)**: Only depends on T001
- **User Story 2 (P2)**: Only depends on T001; fully independent of US1

### Within Each User Story

- T002 (failing test) → T003 (implementation) → T004 (verify build): sequential
- T005 → T006 (can write in parallel, same file — coordinate commits) → T007 (verify): sequential per test

### Parallel Opportunities

- US1 (Phase 3) and US2 (Phase 4) can run concurrently once T001 is done
- T008 and T009 can run concurrently in Phase 5

---

## Parallel Example

```bash
# After T001 (clean build), run US1 and US2 in parallel:

# Terminal A — US1:
# T002: add struct-accessibility test, confirm compile
# T003: add info!() log line to main.rs
# T004: cargo build

# Terminal B — US2:
# T005: add pre_receive/post_receive round-trip test to config.rs
# T006: add update round-trip test to config.rs
# T007: cargo test test_hook_toggle_env_vars

# Once both complete:
# T008: cargo test  (full suite)
# T009: update docs
```

---

## Implementation Strategy

### MVP First (User Story 1 Only)

1. Complete T001 (clean build)
2. Complete T002–T004 (startup log)
3. **STOP and VALIDATE**: `cargo build` green; manual run shows "hook phases" log line
4. Ship — US1 alone delivers the operator visibility improvement

### Incremental Delivery

1. T001 → US1 (T002–T004) → operator can confirm hook config at startup (**MVP**)
2. US2 (T005–T007) → env-var names are pinned by tests (**documented guarantee**)
3. T008–T009 → full suite green, docs updated (**done**)

---

## Notes

- [P] tasks operate on different files or are independent shell commands with no ordering constraint
- [US1]/[US2] labels map to user stories in spec.md for traceability
- T002 is a lightweight struct-accessibility check (not a log-capture test) — it verifies the API surface the log line depends on, not the log output itself, to avoid pulling in `tracing-test` as a dev-dependency
- Both US1 and US2 touch different files (`main.rs` vs `config.rs`) — they can be developed in parallel after T001
- No Go, proto, or datastore changes in this spec — all changes are in `gitstore-git-service/`
