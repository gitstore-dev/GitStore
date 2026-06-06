# Tasks: Fix git clone and git fetch over HTTP

**Input**: Design documents from `/specs/019-fix-upload-pack/`
**Prerequisites**: plan.md, spec.md, research.md, data-model.md, quickstart.md

**Tests**: Constitution Principle I — tests MUST be written before implementation (red → green).

**Organization**: Tasks grouped by user story. All changes are in one file:
`gitstore-git-service/src/git/pack_server.rs`.

## Format: `[ID] [P?] [Story] Description`

- **[P]**: Can run in parallel (different logical units, no inter-task dependency)
- **[Story]**: Which user story this task belongs to

---

## Phase 1: Setup

**Purpose**: Confirm the test harness compiles and integration tests are in the red state.

- [X] T001 Rebase `019-fix-upload-pack` onto `main` and confirm `cargo build` passes in `gitstore-git-service/`
- [X] T002 Run `TestGitClone` and `TestGitFetch` in `tests/integration/` against a live stack to confirm they fail (red phase)

**Checkpoint**: Build green, integration tests red.

---

## Phase 2: Foundational — Unit test scaffold

**Purpose**: Add the `#[cfg(test)]` module to `pack_server.rs` with helper functions shared by all unit tests. No assertions yet — just the scaffold that subsequent test tasks fill in.

**⚠️ CRITICAL**: All US1/US2 test tasks depend on this scaffold.

- [X] T003 Add `#[cfg(test)]` module skeleton and `make_test_repo` / `make_commit` helpers in `gitstore-git-service/src/git/pack_server.rs` (helpers create an in-process bare gix repo with real objects for unit tests)

**Checkpoint**: `cargo test --lib git::pack_server` compiles and runs (0 tests, 0 failures).

---

## Phase 3: User Story 1 — git clone succeeds (Priority: P1) 🎯 MVP

**Goal**: `git clone <url>` against a non-empty repository returns a valid working copy.

**Independent Test**: `TestGitClone` in `tests/integration/git_http_test.go` passes.

### Tests for User Story 1

> **Write these first — verify they FAIL before implementing.**

- [X] T004 [P] [US1] Write T050: `parse_wants_and_haves` with `want+done` parses `done_seen=true` and returns the OID in `gitstore-git-service/src/git/pack_server.rs`
- [X] T005 [P] [US1] Write T051: `parse_wants_and_haves` with `done` absent returns `done_seen=false` in `gitstore-git-service/src/git/pack_server.rs`
- [X] T006 [P] [US1] Write T052: `parse_wants_and_haves` with empty body returns `([], [], false)` in `gitstore-git-service/src/git/pack_server.rs`
- [X] T007 [P] [US1] Write T053: `parse_wants_and_haves` strips capability string after `\0` from want OID in `gitstore-git-service/src/git/pack_server.rs`
- [X] T008 [P] [US1] Write T054: `handle_upload_pack` with empty wants returns `NAK+0000` and no pack in `gitstore-git-service/src/git/pack_server.rs`
- [X] T009 [US1] Write T055: `handle_upload_pack` with `wants+done` returns `NAK` followed by sideband-wrapped pack bytes then `0000` in `gitstore-git-service/src/git/pack_server.rs`
- [X] T010 [US1] Write T056: `handle_upload_pack` with wants present but `done` absent returns `NAK+0000` and no pack (still-negotiating guard) in `gitstore-git-service/src/git/pack_server.rs`

**Checkpoint**: `cargo test --lib git::pack_server::tests::t050` through `t056` all FAIL (red).

### Implementation for User Story 1

- [X] T011 [US1] Change `parse_wants_and_haves` return type to `(Vec<String>, Vec<String>, bool)` and add `done` line detection in `gitstore-git-service/src/git/pack_server.rs`
- [X] T012 [US1] Update `handle_upload_pack` to destructure the new return value and add the `done_seen` guard: if `wants.is_empty() || !done_seen` → return `NAK+0000` without building a pack in `gitstore-git-service/src/git/pack_server.rs`
- [X] T013 [US1] Fix all callers of `parse_wants_and_haves` (any other call sites in `pack_server.rs`) to destructure the updated tuple in `gitstore-git-service/src/git/pack_server.rs`

**Checkpoint**: T050–T056 all pass (green). `TestGitClone` passes against a running stack.

---

## Phase 4: User Story 2 — git fetch transfers only missing commits (Priority: P1)

**Goal**: `git fetch` when behind transfers only the missing commits; when up-to-date transfers zero objects.

**Independent Test**: `TestGitFetch` in `tests/integration/git_http_test.go` passes.

### Tests for User Story 2

> **Write these first — verify they FAIL before implementing.**

- [X] T014 [P] [US2] Write T057: `build_pack_for_wants` with a fresh clone (no haves) returns a non-empty pack containing the commit, its tree, and blobs in `gitstore-git-service/src/git/pack_server.rs`
- [X] T015 [P] [US2] Write T058: `build_pack_for_wants` with `haves=[tip]` (client already has everything) returns `Err` (the new bail path) rather than a silent empty pack in `gitstore-git-service/src/git/pack_server.rs`
- [X] T016 [P] [US2] Write T059: `build_pack_for_wants` with an annotated tag OID in wants includes both the tag object and its target commit in the returned pack in `gitstore-git-service/src/git/pack_server.rs`

**Checkpoint**: T057–T059 FAIL (red).

### Implementation for User Story 2

- [X] T017 [US2] Replace the silent `Ok(Vec::new())` at `walk_ids.is_empty()` with `anyhow::bail!("upload-pack: rev_walk produced no objects for {} want(s)", want_ids.len())` in `gitstore-git-service/src/git/pack_server.rs`
- [X] T018 [US2] Add annotated tag dereferencing in `build_pack_for_wants`: before `rev_walk`, peel each `want_id` — if the object is a `Tag`, push the tag OID to `extra_objects` and walk from its target commit; merge `extra_objects` into the count pipeline with `ObjectExpansion::AsIs` in `gitstore-git-service/src/git/pack_server.rs`

**Checkpoint**: T057–T059 all pass. `TestGitFetch` passes. `TestGitPush` still passes (push path unchanged).

---

## Phase 5: User Story 3 — push latency unaffected (Priority: P2)

**Goal**: `git push` continues to work without regression after the upload-pack changes.

**Independent Test**: `TestGitPush` in `tests/integration/git_http_test.go` and all existing hook pipeline integration tests pass.

- [X] T019 [US3] Run `cargo test --lib` — all 78+ unit tests pass in `gitstore-git-service/`
- [X] T020 [US3] Run `TestGitPush` and all product lifecycle integration tests to confirm zero regressions in `tests/integration/`

**Checkpoint**: All existing tests pass. No receive-pack path was modified.

---

## Phase 6: Polish & Cross-Cutting Concerns

- [X] T021 [P] Run `cargo fmt --all -- --check` and `cargo clippy --all-targets --all-features -- -D warnings` in `gitstore-git-service/`; fix any new issues introduced by this PR
- [X] T022 Run `make pr-ready` from repo root to confirm all checks pass locally
- [X] T023 [P] Update `specs/019-fix-upload-pack/spec.md` status from `Draft` to `Closed`

---

## Dependencies & Execution Order

### Phase Dependencies

- **Phase 1 (Setup)**: No dependencies — start immediately
- **Phase 2 (Foundational)**: Depends on Phase 1 — scaffold must compile before story tests
- **Phase 3 (US1 clone)**: Depends on Phase 2 — uses `make_test_repo` helper from T003
- **Phase 4 (US2 fetch)**: Depends on Phase 2; can start in parallel with Phase 3 if desired (different test functions, same file)
- **Phase 5 (US3 regression)**: Depends on Phases 3 and 4 completing
- **Phase 6 (Polish)**: Depends on Phase 5

### User Story Dependencies

- **US1** and **US2** share `pack_server.rs` but touch different functions — they can proceed in parallel if care is taken to avoid merge conflicts (T004–T013 vs T014–T018)
- **US3** is a validation phase, no implementation

### Within Each User Story

1. Write tests first → confirm red
2. Implement → confirm green
3. Run integration test → confirm end-to-end

### Parallel Opportunities

- T004–T008 (US1 test writing) can all run in parallel — different test functions
- T014–T016 (US2 test writing) can all run in parallel — different test functions
- T019 and T020 can run in parallel (unit vs integration)
- T021 and T023 can run in parallel

---

## Parallel Example: User Story 1

```bash
# Write all parse_wants_and_haves tests in parallel (same test module, different functions):
T004: test_parse_wants_and_haves_done_seen
T005: test_parse_wants_and_haves_done_absent
T006: test_parse_wants_and_haves_empty_body
T007: test_parse_wants_and_haves_caps_stripped
```

---

## Implementation Strategy

### MVP First (User Stories 1 + 2 only)

1. Phase 1: Confirm red integration tests
2. Phase 2: Add test helpers scaffold
3. Phase 3: Fix `parse_wants_and_haves` + `handle_upload_pack` → `TestGitClone` passes
4. Phase 4: Fix `build_pack_for_wants` → `TestGitFetch` passes
5. **STOP and VALIDATE**: Run full integration suite
6. Phase 6: Polish + mark spec Closed

### Incremental Delivery

- After Phase 3: `git clone` works — authors can start catalog workflows
- After Phase 4: `git fetch`/`git pull` works — incremental sync works
- US3 is validation only — no new delivery risk

---

## Notes

- All changes are confined to `gitstore-git-service/src/git/pack_server.rs`
- No proto, Go, config, or compose changes required
- `cargo fmt --all` must be run before pushing (CI runs `cargo fmt --check` separately from clippy)
- The `done_seen` guard (T012) is the single most impactful change — it gates pack generation on the client having committed to receiving a pack
