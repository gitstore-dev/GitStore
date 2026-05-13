---
description: "Task list for migrating gitstore-git-service from git2 to gitoxide"
---

# Tasks: Migrate gitstore-git-service from git2 to gitoxide

**Input**: Design documents from `/specs/007-migrate-gitoxide/`  
**Prerequisites**: plan.md ✅, spec.md ✅, research.md ✅, data-model.md ✅, contracts/ ✅, quickstart.md ✅

**Tests**: Test-First Development (Constitution Principle I — NON-NEGOTIABLE). Tests MUST be written before implementation and verified to fail before the corresponding gix code is added.

**Organization**: Tasks are grouped by user story to enable independent implementation and testing of each story. This migration is a big-bang swap: `git2` is removed entirely in one PR. Task ordering within each phase ensures the codebase compiles at each checkpoint.

## Format: `[ID] [P?] [Story] Description`

- **[P]**: Can run in parallel (different files, no shared dependencies)
- **[Story]**: Which user story this task belongs to ([US1]–[US5], [US2B])
- Exact file paths are included in every task description

---

## Phase 1: Setup (Dependency & Contract)

**Purpose**: Replace the `git2` crate with `gix` in the build manifest and publish the updated gRPC proto contract. These two changes are prerequisites for every downstream task.

- [X] T001 Update `gitstore-git-service/Cargo.toml`: remove `git2 = "0.20.4"`; add `gix = { version = "0.83.0", features = ["max-performance-safe", "tree-editor", "blocking-network-client", "worktree-mutation"] }` and `dashmap = "6"`
- [X] T002 [P] Copy updated gRPC proto from `specs/007-migrate-gitoxide/contracts/gitstore.git.v1.proto` into the service proto directory (confirm path via `find gitstore-git-service -name "*.proto"`) and verify `build.rs` compiles the new `CreateRepository`/`DeleteRepository` RPCs and `repository_id` fields

---

## Phase 2: Foundational (Blocking Prerequisites)

**Purpose**: Core structural changes that ALL user stories depend on. Touches `grpc/server.rs`, `http_git_server.rs`, `lib.rs`, and `main.rs`. No user story implementation can begin until this phase is complete.

**⚠️ CRITICAL**: These tasks reshape the service state and startup sequence. Complete and verify compilation before any user story phase.

- [X] T003 Update `GitServiceImpl` struct in `gitstore-git-service/src/grpc/server.rs`: replace `repo_path: Arc<PathBuf>` + `write_lock: Arc<RwLock<()>>` with `data_root: Arc<PathBuf>` + `repo_locks: Arc<DashMap<String, Arc<RwLock<()>>>>` (per data-model.md §Service State Changes)
- [X] T004 [P] Update `GitServerState` struct in `gitstore-git-service/src/http_git_server.rs`: rename field `repo_path: PathBuf` → `data_root: PathBuf`
- [X] T005 Update startup in `gitstore-git-service/src/lib.rs` and `gitstore-git-service/src/main.rs`: change `GitServiceImpl::new` to accept `data_root: PathBuf` only; remove `init_or_open_repository` call and `catalog.git` provisioning; ensure data directory is created if absent (FR-019)
- [X] T006 [P] Implement `validate_repository_name(name: &str) -> Result<(), tonic::Status>` in `gitstore-git-service/src/grpc/server.rs`: return `INVALID_ARGUMENT` for empty names, names containing `/`, `\`, or `..` components (FR-020)
- [X] T007 [P] Add per-repository lock helper `get_or_insert_lock(repo_locks, id) -> Arc<RwLock<()>>` in `gitstore-git-service/src/grpc/server.rs`: inserts a new `Arc<RwLock<()>>` if absent, returns the existing one if present (DashMap entry API)

**Checkpoint**: `cargo check` must pass after this phase with stub/placeholder implementations for unimplemented RPCs.

---

## Phase 3: User Story 1 — Build and Deploy Without Native Git Library (Priority: P1) 🎯 MVP

**Goal**: `gitstore-git-service` builds and runs with zero libgit2 dependency. Removes all `git2` imports from every source file and replaces the core repository-open and initialisation APIs with `gix` equivalents.

**Independent Test**: `grep -c '^name = "git2"' Cargo.lock` returns `0`; `cargo build` succeeds in a clean environment with no libgit2 installed.

### Tests for User Story 1

> **Write these first — they MUST fail before the gix implementation is added**

- [X] T008 [P] [US1] Update test helpers in `gitstore-git-service/src/git/repo.rs`: replace `git2::Repository::init_bare` → `gix::init_bare`, `git2::Signature::now` → `gix_actor::Signature`; confirm tests fail before implementation
- [X] T009 [P] [US1] Update test helpers in `gitstore-git-service/src/git/events.rs`: replace `git2::Repository::init_bare` → `gix::init_bare`; confirm tests fail before implementation

### Implementation for User Story 1

- [X] T010 [P] [US1] Migrate `init_or_open_repository` in `gitstore-git-service/src/git/repo.rs`: `Repository::open` → `gix::open`; `RepositoryInitOptions` + `Repository::init_opts` → `gix::init_bare` + post-init `edit_reference` to force `HEAD → refs/heads/main` (shim; see research.md §6)
- [X] T011 [P] [US1] Remove `use git2::Repository` import from `gitstore-git-service/src/git/events.rs`; update `should_notify_tag_creation` and `create_release_event` parameter type from `&git2::Repository` to `&gix::Repository`
- [X] T012 [US1] Remove all `git2` imports from `gitstore-git-service/src/grpc/server.rs`; add `gix`, `gix::actor`, `gix::object::tree::EntryKind`, `dashmap::DashMap` imports; add `use std::ops::ControlFlow`
- [X] T013 [US1] Remove all `git2` imports from `gitstore-git-service/src/http_git_server.rs`; add `gix` import
- [X] T014 [US1] Verify build: run `cargo build` and confirm it succeeds; run `grep -c '^name = "git2"' ../Cargo.lock` and confirm output is `0` (SC-001, SC-004)

**Checkpoint**: `cargo build` succeeds with no libgit2 linkage. User Story 1 acceptance criteria met.

---

## Phase 4: User Story 2 — Create and Delete Repositories at Runtime (Priority: P1)

**Goal**: Callers can provision a new named bare repository and remove it without restarting the service. No default repository is created at startup.

**Independent Test**: Create a repository via gRPC, write a file, list a ref, then delete it — all succeed; subsequent operations on the deleted name return NOT_FOUND.

### Tests for User Story 2

> **Write these first — they MUST fail before the RPC implementation is added**

- [X] T015 [P] [US2] Write unit test `test_create_repository_succeeds` in `gitstore-git-service/src/grpc/server.rs`: call `CreateRepository`, assert the path `<data_root>/<id>.git` exists on disk and the RPC returns OK
- [X] T016 [P] [US2] Write unit test `test_create_repository_already_exists` in `gitstore-git-service/src/grpc/server.rs`: create repo twice, assert second call returns `ALREADY_EXISTS`
- [X] T017 [P] [US2] Write unit tests `test_delete_repository_succeeds` and `test_delete_repository_not_found` in `gitstore-git-service/src/grpc/server.rs`: verify path removal and NOT_FOUND on missing repo

### Implementation for User Story 2

- [X] T018 [P] [US2] Implement `create_repository(path: &Path) -> anyhow::Result<()>` in `gitstore-git-service/src/git/repo.rs`: call `gix::init_bare(path)?` then `edit_reference` to set `HEAD → refs/heads/main`
- [X] T019 [P] [US2] Implement `delete_repository(path: &Path) -> anyhow::Result<()>` in `gitstore-git-service/src/git/repo.rs`: call `std::fs::remove_dir_all(path)?`
- [X] T020 [US2] Implement `create_repository` RPC handler in `gitstore-git-service/src/grpc/server.rs`: validate name (call `validate_repository_name`), resolve path as `data_root/<id>.git`, return `ALREADY_EXISTS` if path exists, call `git::repo::create_repository`, insert entry into `repo_locks` (FR-016)
- [X] T021 [US2] Implement `delete_repository` RPC handler in `gitstore-git-service/src/grpc/server.rs`: validate name, acquire write lock from `repo_locks`, call `git::repo::delete_repository`, remove from `repo_locks`, return `NOT_FOUND` if path absent (FR-017)

**Checkpoint**: `cargo test grpc::server::test_create_repository` and `test_delete_repository` pass. Creating and deleting repos works without service restart.

---

## Phase 5: User Story 2B — Operate on a Named Repository (Priority: P1)

**Goal**: Every existing RPC (GetFile, GetFileStream, ListFiles, CommitFile, DeleteFile, CreateTag, ListTags, GetLatestTag) accepts `repository_id`, routes to the correct repository, and returns NOT_FOUND for unknown names. HTTP smart protocol routes validate and resolve the repo name from `data_root`.

**Independent Test**: Create repos `repo-a` and `repo-b`, write a file to `repo-a`, verify `ListFiles` on `repo-b` does not include it; operations with unknown IDs return NOT_FOUND.

### Tests for User Story 2B

> **Write these first — they MUST fail before routing logic is added**

- [X] T022 [P] [US2B] Write unit test `test_operation_on_unknown_repo_returns_not_found` in `gitstore-git-service/src/grpc/server.rs`: call `GetFile` with an unregistered `repository_id`, assert NOT_FOUND (SC-009)
- [X] T023 [P] [US2B] Write unit test `test_invalid_repo_name_rejected` in `gitstore-git-service/src/grpc/server.rs`: test names `""`, `"../etc"`, `"a/b"`, `"a\\b"` each return INVALID_ARGUMENT on any RPC (FR-020)
- [X] T024 [P] [US2B] Write unit test `test_concurrent_repos_are_isolated` in `gitstore-git-service/src/grpc/server.rs`: spawn concurrent write tasks to `repo-a` and `repo-b`, assert each completes independently without blocking the other (FR-021, SC-010)

### Implementation for User Story 2B

- [X] T025 [US2B] Add `resolve_repo_path(data_root, id) -> Result<PathBuf, Status>` helper in `gitstore-git-service/src/grpc/server.rs`: call `validate_repository_name`, build path, return NOT_FOUND if path does not exist
- [X] T026 [P] [US2B] Add `repository_id` routing to `get_file` and `get_file_stream` RPC handlers in `gitstore-git-service/src/grpc/server.rs`: extract `repository_id`, call `resolve_repo_path`, acquire read lock, call `gix::open`
- [X] T027 [P] [US2B] Add `repository_id` routing to `list_files` RPC handler in `gitstore-git-service/src/grpc/server.rs`
- [X] T028 [P] [US2B] Add `repository_id` routing to `commit_file` and `delete_file` RPC handlers in `gitstore-git-service/src/grpc/server.rs` (acquire write lock)
- [X] T029 [P] [US2B] Add `repository_id` routing to `create_tag`, `list_tags`, and `get_latest_tag` RPC handlers in `gitstore-git-service/src/grpc/server.rs` (acquire appropriate lock per operation type)
- [X] T030 [US2B] Update HTTP smart protocol handlers in `gitstore-git-service/src/http_git_server.rs`: extract `{repo}` path segment, call `validate_repository_name`, resolve path as `data_root/{repo}.git`, return HTTP 404 if path does not exist (mirrors gRPC NOT_FOUND logic)

**Checkpoint**: All RPCs correctly route to named repositories. Unknown names return NOT_FOUND. HTTP endpoints validate and resolve repo names.

---

## Phase 6: User Story 3 — Discover and List References (Priority: P1)

**Goal**: `list_tags`, `get_head_commit`, `resolve_ref_to_commit`, and `list_tree_files` all produce results equivalent to the pre-migration baseline using gix APIs. Existing ref-listing tests pass unchanged.

**Independent Test**: Run existing ref-discovery and ref-listing tests (`cargo test git::repo`); all pass with equivalent results.

### Tests for User Story 3

> **Update existing tests to use gix APIs — verify they FAIL before the gix implementation is added**

- [X] T031 [US3] Update ref-listing unit tests in `gitstore-git-service/src/git/repo.rs`: ensure `list_tags` and `get_head_commit` tests use `gix::init_bare` test setup (already done in T008; verify tests still fail with unimplemented gix body)

### Implementation for User Story 3

- [X] T032 [P] [US3] Migrate `get_head_commit` in `gitstore-git-service/src/git/repo.rs`: `repo.head()?.peel_to_commit()` → `repo.head_commit()?`
- [X] T033 [P] [US3] Migrate `list_tags` in `gitstore-git-service/src/git/repo.rs`: `repo.tag_foreach(cb)` → `repo.references()?.tags()?.filter_map(|r| r.ok())` + `r.name().shorten()` to strip `refs/tags/` prefix (research.md §7)
- [X] T034 [P] [US3] Migrate `resolve_ref_to_commit` in `gitstore-git-service/src/grpc/server.rs`: `repo.revparse_single(ref_str)?.peel_to_commit()` → `repo.rev_parse_single(ref_str.as_bytes())?.object()?.try_into_commit()?`
- [X] T035 [US3] Migrate `list_tree_files` in `gitstore-git-service/src/grpc/server.rs`: replace `tree.walk(PreOrder, closure)` with a custom `Visit` impl using `tree.traverse().breadthfirst(delegate)?`; map `TreeWalkResult::Skip` → `ControlFlow::Continue(false)` from `visit_tree()` and `TreeWalkResult::Ok` → `ControlFlow::Continue(true)` (research.md §3, data-model.md §Tree Traversal)

**Checkpoint**: `cargo test git::repo` passes. Ref listing and tree traversal produce correct results.

---

## Phase 7: User Story 4 — Inspect Commits and Tags for Event Handling (Priority: P2)

**Goal**: Tag event handling (`should_notify_tag_creation`, `create_release_event`), tag/commit inspection (`is_release_tag`, `get_tag_commit`), and write operations (`commit_file`, `delete_file`, `create_tag`) all work correctly using gix APIs. Existing tag/commit tests pass unchanged.

**Independent Test**: Run existing tag-event and commit-inspection tests (`cargo test git::events`); all pass with equivalent results.

### Tests for User Story 4

> **Update existing tests to use gix APIs — verify they FAIL before implementation**

- [X] T036 [US4] Update commit/tag unit tests in `gitstore-git-service/src/git/events.rs`: replace `git2::Repository` setup with `gix::init_bare` + gix write helpers; verify tests fail before implementation
- [X] T037 [P] [US4] Update `is_release_tag` and `get_tag_commit` unit tests in `gitstore-git-service/src/git/repo.rs`: replace `git2::Repository` tag-creation helpers with gix equivalents; verify tests fail before implementation

### Implementation for User Story 4

- [X] T038 [P] [US4] Migrate `is_release_tag` in `gitstore-git-service/src/git/repo.rs`: `repo.find_reference(name)?.peel_to_tag().is_ok()` — same call; note `peel_to_tag()` now takes `&mut self` in gix (research.md §7)
- [X] T039 [P] [US4] Migrate `get_tag_commit` in `gitstore-git-service/src/git/repo.rs`: `repo.find_reference(name)?.peel_to_commit()?.id().to_string()` — same call; `id()` returns `gix::Id` implementing `Display`
- [X] T040 [P] [US4] Migrate `should_notify_tag_creation` and `create_release_event` in `gitstore-git-service/src/git/events.rs`: update `&git2::Repository` parameter to `&gix::Repository`; verify all callers in `grpc/server.rs` updated accordingly
- [X] T041 [US4] Migrate `commit_file` in `gitstore-git-service/src/grpc/server.rs`: replace clone-to-tmpdir pattern with tree-editor — `repo.write_blob(content)?` → `repo.edit_tree(head_commit.tree_id()?.detach())?.upsert("path", EntryKind::Blob, blob_oid.detach())?.write()?` → `repo.commit_as(&sig, &sig, "HEAD", msg, new_tree_id, [parent_id])?` (research.md §4, quickstart.md §Commit File)
- [X] T042 [US4] Migrate `delete_file` in `gitstore-git-service/src/grpc/server.rs`: same tree-editor pattern as T041 but using `.remove("path")?` instead of `.upsert()`
- [X] T043 [P] [US4] Migrate `create_tag` in `gitstore-git-service/src/grpc/server.rs`: `repo.revparse_single` → `repo.rev_parse_single`; `git2::Signature::now` → `gix_actor::Signature { name, email, time: gix_date::Time::now_local_or_utc() }`; `repo.tag(name, &obj, &sig, msg, false)` → `repo.tag(name, target_id, Kind::Commit, Some(sig_ref), msg, PreviousValue::MustNotExist)?` (research.md §3)
- [X] T044 [P] [US4] Update `get_tag_message` helper in `gitstore-git-service/src/grpc/server.rs`: `reference.peel_to_tag()?.message()` → `reference.peel_to_tag()?.decode()?.message.to_string()` (research.md §7)
- [X] T045 [US4] Update `list_tags` and `get_latest_tag` handlers in `gitstore-git-service/src/grpc/server.rs` to use updated `get_tag_message` helper and gix ref iteration

**Checkpoint**: `cargo test git::events` and `cargo test grpc::server` pass. Commit/tag operations produce correct objects.

---

## Phase 8: User Story 5 — Preserve Smart HTTP and WebSocket Behaviour (Priority: P1)

**Goal**: All HTTP smart protocol handlers (`info_refs`, `receive_pack`, `check_repository`) use `gix::open` instead of `git2::Repository::open`. Head name and tag-name lookups use gix equivalents. Transport-level behaviour is unchanged.

**Independent Test**: Run `git clone` and `git push` against the migrated service; both complete successfully with correct data transferred (SC-003).

### Tests for User Story 5

> **Update existing HTTP handler tests — verify they FAIL before gix implementation**

- [X] T046 [US5] Update HTTP handler unit tests in `gitstore-git-service/src/http_git_server.rs`: replace `git2::Repository` setup helpers with `gix::init_bare`; verify tests fail before implementation

### Implementation for User Story 5

- [X] T047 [P] [US5] Migrate `info_refs` handler in `gitstore-git-service/src/http_git_server.rs`: `Repository::open(&repo_path)` → `gix::open(&repo_path)?`; `repository.head()?.shorthand()` → `repo.head_name()?.map(|n| n.shorten().to_string())`
- [X] T048 [P] [US5] Migrate `receive_pack` handler in `gitstore-git-service/src/http_git_server.rs`: `Repository::open(&repo_path)` existence check → `gix::open(&repo_path)?`
- [X] T049 [P] [US5] Migrate `check_repository` function in `gitstore-git-service/src/http_git_server.rs` (readiness handler): `Repository::open` → `gix::open`
- [X] T050 [US5] Migrate tag-names collection in `gitstore-git-service/src/http_git_server.rs`: `repository.tag_names(None)` → `repo.references()?.tags()?.filter_map(|r| r.ok()).map(|r| r.name().shorten().to_string()).collect::<Vec<_>>()` (research.md §3)

**Checkpoint**: HTTP smart protocol works end-to-end. `git clone` and `git push` succeed against the migrated service.

---

## Phase 9: Polish & Cross-Cutting Concerns

**Purpose**: Observability updates, integration testing, documentation, and pre-PR validation across all user stories.

- [X] T051 Write integration smoke test `test_create_clone_push_delete` in `gitstore-git-service/tests/integration/mod.rs`: full repository lifecycle via HTTP (clone, push) and gRPC (create, delete); verify all steps succeed without service restart (SC-008)
- [X] T052 [P] Update all `tracing` spans and log statements in `gitstore-git-service/src/` that reference `git2` error types (e.g. `git2::Error` display) to equivalent gix error display strings; verify no observability signal is silently dropped (FR-014)
- [X] T053 [P] Add code comment in `gitstore-git-service/src/git/repo.rs` on the post-init `edit_reference` shim for HEAD: document the gix gap (`gix::init_bare` does not accept `initial_head` option), reference research.md §6, add tracking note for removal once gix adds `init_opts` equivalent (FR-015)
- [X] T054 [P] Update `docs/` to document the gitoxide migration outcome, multi-repository hosting model (`data_root`, `repository_id` routing), and the removal of `catalog.git` auto-provisioning
- [X] T055 Run pre-PR checks from `CLAUDE.md` GitOps section: `cargo fmt --all -- --check`, `cargo clippy --all-targets --all-features -- -D warnings`, `cargo build --verbose`, `cargo test --verbose`; fix any failures before marking complete

---

## Dependencies & Execution Order

### Phase Dependencies

- **Setup (Phase 1)**: No dependencies — start immediately
- **Foundational (Phase 2)**: Depends on Phase 1 (gix must be in Cargo.toml for imports to resolve) — **BLOCKS all user stories**
- **US1 (Phase 3)**: Depends on Phase 2 — can begin as soon as state structs compile
- **US2 (Phase 4)**: Depends on Phase 2 — can begin after Foundational; independent of US1 (different functions in repo.rs and server.rs)
- **US2B (Phase 5)**: Depends on Phase 2; benefits from US2 being complete but routing logic is independent
- **US3 (Phase 6)**: Depends on Phase 2; independent of US2/US2B (different functions)
- **US4 (Phase 7)**: Depends on Phase 2; benefits from US3 (uses `resolve_ref_to_commit`) but tree-editor work is independent
- **US5 (Phase 8)**: Depends on Phase 2; independent of all gRPC phases (different file)
- **Polish (Phase 9)**: Depends on all user story phases complete

### User Story Dependencies

- **US1 (P1)**: After Foundational — no story dependencies
- **US2 (P1)**: After Foundational — no story dependencies
- **US2B (P1)**: After Foundational — depends on US2 (Create/Delete must exist before routing can be tested end-to-end)
- **US3 (P1)**: After Foundational — no story dependencies (different functions)
- **US4 (P2)**: After US3 (tree-editor commit uses `resolve_ref_to_commit`; tag listing uses ref iteration)
- **US5 (P1)**: After Foundational — no story dependencies (isolated file)

### Within Each User Story

1. Tests MUST be written and verified to FAIL before implementation
2. `git/repo.rs` functions before `grpc/server.rs` handlers that call them
3. State struct helpers (Phase 2) before per-RPC routing (Phase 5)
4. Each user story phase independently compilable after completion

---

## Parallel Execution Examples

### Phase 2 (Foundational) — run together

```bash
# T003: server.rs state struct update
# T004: http_git_server.rs state struct update   [P]
# T005: lib.rs + main.rs startup sequence         (sequential — depends on T003)
# T006: validate_repository_name helper          [P]
# T007: get_or_insert_lock helper                [P]
```

### Phase 3 (US1 — Build) — T008 and T009 can run in parallel

```bash
# T008: Update test helpers in git/repo.rs        [P]
# T009: Update test helpers in git/events.rs      [P]
# T010: Migrate init_or_open_repository (repo.rs) [P] — after tests fail
# T011: Remove git2 from events.rs               [P] — after tests fail
# T012: Remove git2 from server.rs               (after T010, T011)
# T013: Remove git2 from http_git_server.rs      [P]
# T014: Build verification                        (after T012, T013)
```

### Phase 4 (US2) — tests can run in parallel; impl follows

```bash
# T015: test_create_repository_* tests           [P]
# T016: test_delete_repository_* tests           [P]
# T017: test_concurrent_repos_are_isolated       [P]
# T018: create_repository in git/repo.rs         [P] — after tests fail
# T019: delete_repository in git/repo.rs         [P] — after tests fail
# T020: CreateRepository RPC handler             (after T018)
# T021: DeleteRepository RPC handler             (after T019)
```

### Phase 7 (US4) — parallel within story

```bash
# T036: Update events.rs tests                   [P]
# T037: Update repo.rs tag tests                 [P]
# T038: Migrate is_release_tag                   [P]
# T039: Migrate get_tag_commit                   [P]
# T040: Migrate events.rs                        [P]
# T041: Migrate commit_file                       (sequential — tree-editor pattern)
# T042: Migrate delete_file                       (sequential — follows T041 pattern)
# T043: Migrate create_tag                       [P]
# T044: Update get_tag_message helper            [P]
# T045: Update list_tags / get_latest_tag         (after T044)
```

---

## Implementation Strategy

### MVP First (US1 + US2 Only)

1. Complete Phase 1: Setup
2. Complete Phase 2: Foundational (**critical — blocks all stories**)
3. Complete Phase 3: US1 — build succeeds, no libgit2
4. Complete Phase 4: US2 — create/delete repos
5. **STOP and VALIDATE**: `cargo test grpc::server::test_create_repository` and `test_delete_repository` pass; `cargo build` produces a binary with no libgit2

### Full Delivery (all stories)

1. Setup + Foundational → foundation ready
2. US1 → build clean, no git2
3. US2 → create/delete repos via gRPC
4. US2B → all RPCs routed by `repository_id`
5. US3 → ref listing / tree traversal correct
6. US4 → commit/tag inspection and write ops correct
7. US5 → HTTP smart protocol preserved
8. Polish → integration tests, observability, docs, pre-PR checks

### Parallel Team Strategy

With multiple developers (after Foundational is complete):

- **Dev A**: US1 (git/repo.rs base migration) + US3 (ref listing functions)
- **Dev B**: US2 (create/delete RPCs) + US2B (routing in server.rs)
- **Dev C**: US5 (http_git_server.rs migration) independently
- **Dev D**: US4 (events.rs + tree-editor commits/tags) after US3 completes

---

## Notes

- All `[P]` tasks touch different files or independent functions; verify no two parallel tasks write the same file before parallelising
- `[Story]` label maps each task to a user story for traceability and independent testing
- The `grpc/server.rs` file is touched by US2, US2B, US3, US4 — coordinate sequencing within this file carefully
- Test-first is non-negotiable (Constitution I): every test task must be completed and verified to fail before its corresponding implementation task
- Commit after each phase checkpoint to enable bisect if a regression is introduced
- Shim tasks (T010, T053) must be documented in code per FR-015; open a tracking issue for the `gix::init_opts` gap
