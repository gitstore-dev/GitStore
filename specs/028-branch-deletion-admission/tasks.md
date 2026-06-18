# Tasks: Branch Deletion Admission

**Input**: Design documents from `/specs/028-branch-deletion-admission/`  
**Prerequisites**: plan.md ✅, spec.md ✅, research.md ✅, data-model.md ✅, quickstart.md ✅

**Tests**: Test-First Development (Constitution Principle I - NON-NEGOTIABLE). Tests MUST be written before implementation.

**Organization**: Tasks are grouped by user story to enable independent implementation and testing of each story.

## Format: `[ID] [P?] [Story] Description`

- **[P]**: Can run in parallel (different files, no dependencies)
- **[Story]**: Which user story this task belongs to (US1, US2)
- Include exact file paths in descriptions

## Scope Summary

This feature requires changes to **two files** in the Rust git service only. No Go, proto, or datastore changes are needed.

| File | Change |
|---|---|
| `gitstore-git-service/src/git/hooks/admission_handler.rs` | Add unit tests T019f and T019g |
| `gitstore-git-service/src/grpc/server.rs` | Add `is_delete_only` guard; make quarantine `Option<Quarantine>` |

---

## Phase 1: Setup (Shared Infrastructure)

**Purpose**: No new project structure or dependencies required — this is a focused fix to existing files.

*(No setup tasks needed. Proceed directly to Phase 2.)*

---

## Phase 2: Foundational (Blocking Prerequisites)

**Purpose**: Write the failing unit test (T019f) that drives the US1 implementation. Must be written and verified to **FAIL** before any implementation begins.

**⚠️ CRITICAL**: US1 implementation cannot begin until T001 is written and confirmed to fail.

- [x] T001 Write failing unit test T019f — zero new-OID on matching ref triggers one `AdmitResources` call — in `gitstore-git-service/src/git/hooks/admission_handler.rs` (add after T019e; test must FAIL before proceeding)

**Checkpoint**: `cargo test test_branch_deletion_triggers_admit` must output a test failure before US1 implementation begins.

---

## Phase 3: User Story 1 — Deleting a branch removes its catalog resources (Priority: P1) 🎯 MVP

**Goal**: `git push origin --delete <branch>` succeeds; the git service forwards a zero-new-OID `AdmitResources` call; the Go API removes resources admitted on the deleted branch.

**Independent Test**: `cargo test test_branch_deletion_triggers_admit` (unit) and `go test -run TestAdmission_BranchDeletion ./tests/integration/...` (integration).

### Implementation for User Story 1

- [x] T002 [US1] Add `is_delete_only` detection and make quarantine `Option<Quarantine>` in `gitstore-git-service/src/grpc/server.rs`:
  - After extracting `ref_commands` (current line 813), compute: `let is_delete_only = ref_commands.iter().all(|c| c.new_oid == "0000000000000000000000000000000000000000");`
  - Gate the channel-bridge and `stage_pack_from_reader` block: `let quarantine: Option<Quarantine> = if is_delete_only { None } else { Some(stage_pack_from_reader(...)?) };`
  - Update `quarantine_path` extraction to: `let quarantine_path = quarantine.as_ref().map(|q| q.dir.path().to_path_buf());`
  - Update the pipeline `run` call to pass: `quarantine_path.as_deref()` (was `Some(quarantine_path.as_path())`)
  - Update the `promote_quarantine` call in the `TxnOutcome::Committed` arm to: `if let Some(q) = quarantine { promote_quarantine(&repo, q).map_err(...)? }`
  - Add structured log before the pipeline run when `is_delete_only`: `info!(repo_id = %repo_id, "receive_pack: branch deletion — skipping pack staging")`

- [x] T003 [US1] Verify T019f now passes in `gitstore-git-service/src/git/hooks/admission_handler.rs`: run `cargo test test_branch_deletion_triggers_admit` and confirm green

- [x] T004 [US1] Verify the existing integration test `TestAdmission_BranchDeletion` passes end-to-end without being skipped: run `go test -v -run TestAdmission_BranchDeletion ./tests/integration/...` against a running stack

**Checkpoint**: At this point, `git push origin --delete <branch>` returns exit 0, resources are removed from the datastore, and `TestAdmission_BranchDeletion` is green.

---

## Phase 4: User Story 2 — Branch pattern filtering applies to deletes (Priority: P2)

**Goal**: A branch-delete on a ref that does not match `GITSTORE_ADMISSION_CONTROL__BRANCH_PATTERN` does not trigger admission — identical to the push behaviour.

**Independent Test**: `cargo test test_branch_deletion_non_matching_ref_no_call` (unit) — confirms no `AdmitResources` call for a non-matching ref with zero new-OID.

### Tests for User Story 2 ⚠️

> **NOTE: Write this test FIRST — it should PASS immediately (no code change required for US2).**
> Passing immediately is expected: the existing `ref_name != branch_pattern` filter already handles zero new-OID. Writing the test documents and locks in the acceptance scenario.

- [x] T005 [US2] Write unit test T019g — zero new-OID on a **non-matching** ref produces no `AdmitResources` call — in `gitstore-git-service/src/git/hooks/admission_handler.rs` (add after T019f):
  - `ref_name = "refs/heads/experiment/foo"`, `branch_pattern = "refs/heads/main"`, `new_oid = "0".repeat(40)`
  - Assert `count == 0` after 50ms sleep

- [x] T006 [US2] Verify T019g passes: run `cargo test test_branch_deletion_non_matching_ref_no_call` and confirm green (no implementation change needed)

**Checkpoint**: Both admission handler test cases (T019f and T019g) pass; branch pattern filtering is verified for the deletion path.

---

## Phase 5: Polish & Cross-Cutting Concerns

**Purpose**: Full test suite validation and documentation update.

- [x] T007 Run the full Rust test suite to confirm no regressions: `cd gitstore-git-service && cargo test`
- [x] T008 [P] Run the full project test suite: `make test`
- [x] T009 [P] Update `docs/products/push-validation.md` to document that branch deletion triggers admission and removes resources admitted on the deleted branch

---

## Dependencies & Execution Order

### Phase Dependencies

- **Foundational (Phase 2)**: No dependencies — start immediately
- **US1 (Phase 3)**: Depends on T001 (failing test written) — **BLOCKS** T002
- **US2 (Phase 4)**: Independent of US1 — can start after Foundational, or in parallel with US1 once T002 is complete
- **Polish (Phase 5)**: Depends on US1 and US2 completion

### User Story Dependencies

- **User Story 1 (P1)**: Depends only on T001 (the failing unit test)
- **User Story 2 (P2)**: No dependency on US1 — can proceed independently after Phase 2

### Within Each User Story

- T001 MUST be written and FAILING before T002 begins (test-first)
- T002 (implementation) → T003 (verify unit test passes) → T004 (verify integration test)
- T005 (write T019g) can run in parallel with T002 and T003 since it targets a different method in the same file — but coordinate to avoid merge conflicts in `admission_handler.rs`

### Parallel Opportunities

- T005 can begin as soon as T001 is committed (different test function in the same file; serialise commits)
- T007, T008, T009 can all run concurrently once Phase 4 is complete

---

## Parallel Example: Writing both unit tests

```bash
# T001 — written first, must FAIL:
cargo test test_branch_deletion_triggers_admit
# → FAIL (expected: guard not yet added)

# T005 — written after T001, should PASS immediately:
cargo test test_branch_deletion_non_matching_ref_no_call
# → PASS (existing ref_name check handles this)

# After T002 (guard added):
cargo test test_branch_deletion_triggers_admit
# → PASS

# Full admission handler suite:
cargo test admission_handler
# → all 7 tests pass (T019a–T019g)
```

---

## Implementation Strategy

### MVP First (User Story 1 Only)

1. Complete Phase 2: Write T001 (failing test)
2. Complete Phase 3: Implement guard (T002) → verify unit test (T003) → verify integration test (T004)
3. **STOP and VALIDATE**: `TestAdmission_BranchDeletion` passes end-to-end
4. Ship — US1 alone delivers the core correctness fix

### Incremental Delivery

1. Foundation → US1 delivered → branch deletion works end-to-end (**MVP**)
2. Add US2 test → confirms pattern filtering is symmetrically verified (**documented guarantee**)
3. Polish → full suite green, docs updated (**done**)

---

## Notes

- [P] tasks operate on different files or are independent shell commands with no ordering constraint
- [US1]/[US2] labels map to user stories in spec.md for traceability
- T001 must FAIL before T002; T005 should PASS without any code change (it documents existing behaviour)
- No Go, proto, or datastore changes in this spec — all changes are in `gitstore-git-service/`
- The integration test `TestAdmission_BranchDeletion` is already written; T004 is purely a verification step
- Commit after T001 (failing test), then again after T002 (guard + passing test)
