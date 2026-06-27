# Tasks: Admission Path Cleanup — `changed_paths` Population and Legacy Fallback Removal

**Input**: Design documents from `/specs/034-admission-path-cleanup/`
**Prerequisites**: plan.md ✓, spec.md ✓, research.md ✓, data-model.md ✓, contracts/ ✓, quickstart.md ✓

**Tests**: Test-First Development (Constitution Principle I — NON-NEGOTIABLE). Tests MUST be written before implementation.

**Organization**: Tasks are grouped by user story. Phase 3 (US1, Rust) and Phase 4 (US2+US3, Go) are independently deployable. Phase 4 MUST NOT be merged before Phase 3 is deployed and verified in production.

## Format: `[ID] [P?] [Story] Description`

- **[P]**: Can run in parallel (different files, no dependencies on incomplete tasks)
- **[Story]**: Which user story this task belongs to (US1, US2, US3)
- Include exact file paths in descriptions

---

## Phase 1: Setup

**Purpose**: No new files need to be created; all work modifies existing files. This phase confirms the repo is in a clean state for implementation.

- [x] T001 Verify `cargo test --verbose` passes in `gitstore-git-service/` on the current branch (baseline green)
- [x] T002 Verify `go test ./internal/cataloggrpc/...` passes in `gitstore-api/` on the current branch (baseline green)

**Checkpoint**: Both test suites are green. Implementation can begin.

---

## Phase 2: Foundational (Blocking Prerequisites)

**Purpose**: Extend the `AdmissionHandler` trait with `git_dir: &Path`. All US1 implementation depends on this trait change. All US2/US3 work depends on US1 shipping.

**⚠️ CRITICAL**: No user story work can begin until this phase is complete and `cargo test --verbose` passes.

- [x] T003 Update `AdmissionHandler::admit` trait in `gitstore-git-service/src/git/hooks/mod.rs` to add `git_dir: &std::path::Path` as the fourth parameter (after `repository_id: &str`)
- [x] T004 Update `NoopAdmissionHandler::admit` in `gitstore-git-service/src/git/hooks/mod.rs` to accept and ignore the new `git_dir` parameter
- [x] T005 Update `HookPipeline::run_post_receive` in `gitstore-git-service/src/git/hooks/mod.rs` to pass `git_dir` to `handler.admit(...)` (it already receives `git_dir: &Path`)
- [x] T006 Update `HookPipeline::run_schema_validation` in `gitstore-git-service/src/git/hooks/mod.rs` to pass `git_dir` to `self.admission_handler.admit(...)` in the blocking admission path (the `phase == self.admission_control_phase && phase != "post-receive"` branch)
- [x] T007 Update all existing unit test call sites of `handler.admit(...)` / `h.admit(...)` in `gitstore-git-service/src/git/hooks/admission_handler.rs` and `gitstore-git-service/src/git/hooks/mod.rs` to pass an appropriate `git_dir` (use `std::path::Path::new("")` for tests that don't need a real repo)

**Checkpoint**: `cargo test --verbose` passes. All existing T019* and pipeline tests compile and pass with the new trait signature.

---

## Phase 3: User Story 1 — Admission cost scales with push size (Priority: P1) 🎯 MVP

**Goal**: The Rust git service computes the changed file paths for every push and sends them in `AdmitResourcesRequest.changed_paths`. After this phase, the Go API's `changedPaths` fast-path in `loadParsedEntries` activates for all pushes.

**Independent Test**: Run `cargo test admission_handler` in `gitstore-git-service/`. The new T020* tests pass. Do an end-to-end push and confirm in API logs that only the changed file is read.

### Tests for User Story 1

> **Write these tests FIRST. Verify they FAIL before writing any implementation.**

- [x] T008 [P] [US1] Add `TestChangedPathsSingleFileUpdate` to `gitstore-git-service/src/git/hooks/admission_handler.rs` tests: create a bare in-process repo using `make_repo_with_files` pattern (see `mod.rs` tests), make a second commit changing one of the files, call `admit` with real non-zero `old_oid`/`new_oid` and a real `git_dir`, and assert the captured `AdmitResourcesRequest.changed_paths` contains exactly that one file path
- [x] T009 [P] [US1] Add `TestChangedPathsNewBranch` to `gitstore-git-service/src/git/hooks/admission_handler.rs` tests: use a fresh repo with one commit, call `admit` with `old_oid = "0".repeat(40)` and a real `git_dir`, assert `changed_paths` contains all file paths in the commit (full tree)
- [x] T010 [P] [US1] Add `TestChangedPathsBranchDeletion` to `gitstore-git-service/src/git/hooks/admission_handler.rs` tests: call `admit` with `new_oid = "0".repeat(40)` and a real `git_dir`, assert `changed_paths` contains all file paths from the `old_oid` tree
- [x] T011 [P] [US1] Add `TestChangedPathsGixOpenFailure` to `gitstore-git-service/src/git/hooks/admission_handler.rs` tests: pass a non-existent `git_dir`, assert `changed_paths` in the sent request is `vec![]` (fallback) and `admit` still returns `AdmissionDecision::Accept`
- [x] T012 [P] [US1] Add `TestChangedPathsEmptyGitDir` to `gitstore-git-service/src/git/hooks/admission_handler.rs` tests: pass `git_dir = Path::new("")`, assert `changed_paths` is `vec![]` and the call still completes

### Implementation for User Story 1

- [x] T013 [US1] Add private helper `compute_changed_paths(git_dir: &std::path::Path, old_oid: &str, new_oid: &str) -> Vec<String>` to `gitstore-git-service/src/git/hooks/admission_handler.rs`; implement: empty or non-existent `git_dir` → return `vec![]`; gix open failure → log `error!`, return `vec![]`; all-zeros `old_oid` → collect all file paths from `new_oid` tree; all-zeros `new_oid` → collect all file paths from `old_oid` tree; both non-zero → collect only differing paths between trees. Reuse the tree-walking pattern from `collect_changed_blobs_from_trees` and `collect_blobs_from_tree` in `mod.rs` but collect `String` paths only, no blob content.
- [x] T014 [US1] Update `AdmissionControlHandler::admit` in `gitstore-git-service/src/git/hooks/admission_handler.rs` to call `compute_changed_paths(git_dir, &update.old_oid, &update.new_oid)` and assign the result to `changed_paths` in the `AdmitResourcesRequest` (replacing `changed_paths: vec![]`)

**Checkpoint**: `cargo test --verbose` passes. All T008–T012 tests now pass (green). The `changed_paths` field is populated on every `AdmitResourcesRequest`.

---

## Phase 4: User Story 2 + User Story 3 — Legacy path removal and double-lookup fix (Priority: P2/P3)

**⚠️ DEPLOYMENT CONSTRAINT**: Tasks in this phase MUST NOT be merged to `main` before Phase 3 (US1) is deployed and verified in production. Developing in parallel is fine; merging is gated.

**Goal**: Remove the `OldCommitSha == ""` legacy fallback from the Go API, and eliminate the double `GetXByName` DB call and double `operationForEntry` per CategoryTaxonomy.

**Independent Test**: Run `go test ./internal/cataloggrpc/...` in `gitstore-api/`. All existing tests pass. The fallback warning log no longer exists in the codebase. A test asserting `old_commit_sha == ""` triggers the fallback MUST NOT exist.

### Tests for User Story 2

> **Write these tests FIRST. Verify they FAIL before writing any implementation.**

- [x] T015 [P] [US2] Add `TestAdmitResourcesLegacyPathAbsent` to `gitstore-api/internal/cataloggrpc/server_test.go`: send an `AdmitResourcesRequest` with `old_commit_sha = ""` and verify the handler returns an empty response without calling `loadParsedEntries` (the legacy path must not exist); this test should fail until T021 removes the branch
- [x] T016 [P] [US2] Add `TestAdmitResourcesChangedPathsFastPath` to `gitstore-api/internal/cataloggrpc/server_test.go`: send an `AdmitResourcesRequest` with non-empty `old_commit_sha`, non-empty `new_commit_sha`, and `changed_paths = ["products/widget.md"]`; use a mock `GitReader` that records `ReadFile` calls; assert `ReadFile` is called exactly once for `products/widget.md` and not for any other path

### Tests for User Story 3

> **Write these tests FIRST. Verify they FAIL before writing any implementation.**

- [x] T017 [P] [US3] Add `TestOperationForEntryReturnsCachedObject` to `gitstore-api/internal/cataloggrpc/server_test.go`: call `operationForEntry` with an `explicitOps` map that contains the entry; assert the returned `existing` object matches what was in the map and `store.GetProductByName` (mock) is NOT called
- [x] T018 [P] [US3] Add `TestAdmitProductSingleDBRead` to `gitstore-api/internal/cataloggrpc/server_test.go`: wire a full `admitProduct` call with `explicitOps` pre-populated; assert the mock datastore's `GetProductByName` is called exactly once (from `operationForEntry`), not twice

### Implementation for User Story 2

- [x] T019 [US2] Remove the `if req.GetOldCommitSha() == ""` legacy fallback branch (lines 380–388) from `gitstore-api/internal/cataloggrpc/server.go` including its warning log; the diff-aware path becomes the only path through `AdmitResources`

### Implementation for User Story 3

- [x] T020 [US3] Refactor `operationForEntry` in `gitstore-api/internal/cataloggrpc/server.go` to return `(admission.Operation, any, bool)` where the `any` is the looked-up existing object (or `nil` for create); the DB lookup result is returned to the caller rather than discarded
- [x] T021 [P] [US3] Update `admitProduct` in `gitstore-api/internal/cataloggrpc/server.go` to accept `existing any` as a parameter (replacing the internal `store.GetProductByName` call); the caller (`admitParsedEntries`) passes the object returned by `operationForEntry`
- [x] T022 [P] [US3] Update `admitCollection` in `gitstore-api/internal/cataloggrpc/server.go` to accept `existing any` as a parameter, removing the internal `store.GetCollectionByName` call
- [x] T023 [P] [US3] Update `admitProductVariant` in `gitstore-api/internal/cataloggrpc/server.go` to accept `existing any` as a parameter, removing the internal `store.GetProductVariantByName` call
- [x] T024 [P] [US3] Update `admitCategoryTaxonomyWithContext` in `gitstore-api/internal/cataloggrpc/server.go` to accept `existing any` as a parameter, removing the internal `store.GetCategoryTaxonomyByName` call
- [x] T025 [US3] Update `admitParsedEntries` in `gitstore-api/internal/cataloggrpc/server.go` to: (a) build `catPushSet` by reading `op` directly from `explicitOps[e.identity.key()]` (no `operationForEntry` call); (b) pass the `existing` object returned by `operationForEntry` into each `admitProduct`/`admitCollection`/`admitProductVariant`/`admitCategoryTaxonomyWithContext` call

**Checkpoint**: `go test ./internal/cataloggrpc/...` passes. T015–T018 pass. The string `"admit_resources: old commit absent"` does not appear anywhere in `gitstore-api/`. `GetXByName` is called once per resource per admission cycle.

---

## Phase 5: Polish

- [x] T026 [P] Update `gitstore-git-service/src/git/hooks/admission_handler.rs` tests T019a–T019i to pass a real `git_dir` where the mock server is exercised (or `Path::new("")` where it isn't), confirming all existing admission handler tests remain green with the new trait signature
- [ ] T027 [P] Verify `quickstart.md` end-to-end steps in a running local stack (`make dev`): push a single-file change to a repo with multiple tracked resources; confirm API logs show only that file being read and the fallback warning is absent
- [x] T028 Update `CLAUDE.md` / `AGENTS.md` Recent Changes section to record `034-admission-path-cleanup`: `changed_paths` populated in Rust admission handler; legacy `OldCommitSha==""` path removed from Go API

---

## Dependencies & Execution Order

### Phase Dependencies

```
Phase 1 (Setup)
    └─► Phase 2 (Foundational — trait extension)
            └─► Phase 3 (US1 — Rust changed_paths)  ←── MVP deliverable
                    └─► Phase 4 (US2+US3 — Go cleanup)  ←── DEPLOYMENT GATED on Phase 3
                            └─► Phase 5 (Polish)
```

### User Story Dependencies

- **US1 (P1)**: Depends on Phase 2 (trait extension complete). No dependency on US2/US3.
- **US2 (P2)**: Depends on US1 DEPLOYED in production (not just merged). Can be developed in parallel.
- **US3 (P3)**: Depends on Phase 2. Can be developed in parallel with US1 (different files). Must be merged with US2 in the same PR or after US2 is merged.

### Parallel Opportunities

Within Phase 3 (US1): T008–T012 (tests) can all be written in parallel. T013 and T014 are sequential (helper before caller).

Within Phase 4: T015–T018 (tests) can be written in parallel. T020–T024 can be implemented in parallel (different admit functions). T025 depends on T020–T024 completing.

---

## Parallel Example: User Story 1 (Rust)

```
# Write all tests concurrently:
T008: TestChangedPathsSingleFileUpdate → admission_handler.rs
T009: TestChangedPathsNewBranch       → admission_handler.rs
T010: TestChangedPathsBranchDeletion  → admission_handler.rs
T011: TestChangedPathsGixOpenFailure  → admission_handler.rs
T012: TestChangedPathsEmptyGitDir     → admission_handler.rs

# Then implement sequentially:
T013: compute_changed_paths helper → admission_handler.rs
T014: wire into AdmissionControlHandler::admit → admission_handler.rs
```

## Parallel Example: User Story 3 (Go admit functions)

```
# After T020 (operationForEntry signature change):
T021: admitProduct           → server.go
T022: admitCollection        → server.go
T023: admitProductVariant    → server.go
T024: admitCategoryTaxonomy  → server.go
# All touch different functions in the same file — serialize or coordinate carefully
# T025 depends on T021–T024 all completing
```

---

## Implementation Strategy

### MVP First (US1 Only)

1. Complete Phase 1: baseline green
2. Complete Phase 2: trait extension (`cargo test` passes)
3. Complete Phase 3: `compute_changed_paths` + wire in handler
4. **STOP and VALIDATE**: `cargo test --verbose` green; push a real branch and confirm `changed_paths` is populated in API logs
5. Ship Phase 3 to production and monitor for absence of fallback warning

### Incremental Delivery

1. Setup + Foundational → trait compiles
2. US1 (Phase 3) → `changed_paths` populated, fast-path live → **Deploy**
3. Verify: no `"admit_resources: old commit absent"` warning for 10+ pushes in production
4. US2 (Phase 4, T019) → legacy branch removed → merge only after step 3
5. US3 (Phase 4, T020–T025) → double-lookup eliminated → merge with US2

---

## Notes

- [P] tasks can run in parallel (different functions/files, no shared state)
- US1 and US2/US3 are split across two services — the Rust and Go changes can be developed concurrently but the Go changes must not be deployed first
- The deployment ordering constraint is explicit in `contracts/admission-changed-paths.md`
- After Phase 3 ships, the absence of `"admit_resources: old commit absent"` in production logs for 10+ pushes is the green light for Phase 4
- T028 (AGENTS.md update) must be done against `AGENTS.md`, not `CLAUDE.md` (which is a symlink)
