# Implementation Plan: Migrate gitstore-git-service from git2 to gitoxide

**Branch**: `007-migrate-gitoxide` | **Date**: 2026-05-12 | **Spec**: [spec.md](spec.md)  
**Input**: Feature specification from `specs/007-migrate-gitoxide/spec.md`

## Summary

Replace all `git2` (libgit2-backed) usage in `gitstore-git-service` with `gix 0.83.0` (pure-Rust `gitoxide`), and simultaneously introduce multi-repository hosting as the architectural foundation. The service no longer boots with a hardcoded `catalog.git` repository; instead every operation is scoped to a caller-supplied `repository_id`, and `CreateRepository`/`DeleteRepository` RPCs manage the repository lifecycle. The gRPC proto is extended additively (new RPCs, `repository_id` field on all requests). HTTP smart protocol routes already accept a repo path segment and are updated to resolve against the data root. Delivered as a single big-bang swap in one PR.

## Technical Context

**Language/Version**: Rust edition 2021, MSRV 1.82 (required by gix 0.83.0)  
**Primary Dependencies**: `gix 0.83.0` (replaces `git2 0.20.4`), `tokio 1.35`, `axum 0.8`, `tonic 0.14`, `tracing 0.1`, `anyhow 1.0`  
**Storage**: Bare Git repositories on local filesystem (unchanged)  
**Testing**: `cargo test` (unit tests in each module + integration test binary)  
**Target Platform**: Linux server (unchanged)  
**Project Type**: gRPC + HTTP service (Rust binary)  
**Performance Goals**: Core operations within ±10% of pre-migration baseline (SC-007)  
**Constraints**: No libgit2 in build or runtime environment post-migration (SC-001, SC-004)  
**Scale/Scope**: Single-service migration + multi-repo hosting foundation; gRPC proto extended additively

## Constitution Check

| Principle                | Status | Notes                                                                                                      |
|--------------------------|--------|------------------------------------------------------------------------------------------------------------|
| I. Test-First            | ✅ Pass | Existing tests updated to fail under git2 patterns before gix implementation; new shim tests written first |
| II. API-First            | ✅ Pass | gRPC proto extended in `contracts/gitstore.git.v1.proto` before implementation; additive only              |
| III. Clear Contracts     | ✅ Pass | New RPCs and `repository_id` field defined in proto before any handler code                                |
| IV. Observability        | ✅ Pass | FR-014: all git2-specific log/trace signals updated to gix equivalents                                     |
| V. User Story Driven     | ✅ Pass | All tasks map to user stories US1–US5                                                                      |
| VI. Incremental Delivery | ✅ Pass | P1 stories (build, open/init, refs, transport) unblock all P2 stories                                      |
| VII. Simplicity          | ✅ Pass | tree-editor eliminates clone-to-workdir complexity; no new abstractions                                    |

**Gate result**: PASS — proceed to Phase 1.

## Project Structure

### Documentation (this feature)

```text
specs/007-migrate-gitoxide/
├── plan.md              ← this file
├── research.md          ← Phase 0 output (git2→gix mapping, decisions)
├── data-model.md        ← Phase 1 output (domain entity + type mapping)
├── quickstart.md        ← Phase 1 output (build, test, dev notes)
├── contracts/
│   └── gitstore.git.v1.proto   ← Phase 1 output (updated gRPC contract)
└── tasks.md             ← Phase 2 output (/speckit.tasks command)
```

### Source Code

```text
gitstore-git-service/
├── Cargo.toml                         ← remove git2; add gix with features
├── src/
│   ├── git/
│   │   ├── repo.rs                    ← replace all git2 calls with gix;
│   │   │                                add create_repository / delete_repository fns
│   │   ├── events.rs                  ← replace Repository import; fix tests
│   │   ├── hooks.rs                   ← no git2 usage; unchanged
│   │   └── metrics.rs                 ← no git2 usage; unchanged
│   ├── grpc/
│   │   └── server.rs                  ← major rewrite: multi-repo state
│   │                                     (data_root + per-repo lock map),
│   │                                     new CreateRepository / DeleteRepository RPCs,
│   │                                     repository_id routing on all existing RPCs,
│   │                                     tree-editor pattern for commits
│   ├── http_git_server.rs             ← replace git2 open/head/tag_names;
│   │                                     repo name validated + resolved from data_root
│   └── lib.rs / main.rs               ← update startup: data_root only, no catalog.git init
└── tests/
    └── integration/
        └── mod.rs                     ← add integration smoke tests (create, clone, push, delete)
```

**Structure Decision**: Single-project Rust binary. No new source files required beyond the existing layout. The most significant change is in `grpc/server.rs` (multi-repo state, new RPCs, `repository_id` routing) and `main.rs` (removal of `catalog.git` init).

## Phase 0: Research (Complete)

All NEEDS CLARIFICATION items resolved. See `research.md` for full details.

Key decisions:
1. **gix 0.83.0** — latest stable, MSRV 1.82, all required APIs stable
2. **Per-request `gix::open`** — multi-repo hosting means no single startup-time `ThreadSafeRepository`; open per-request, cache in follow-up if needed
3. **tree-editor replaces clone-to-workdir** — `commit_file`/`delete_file` operate directly on bare repo object DB
4. **Per-repository `RwLock`** — stored in a concurrent map keyed by `repository_id`; locks for different repos are independent
5. **Big-bang swap** — `git2` removed entirely in one PR; no hybrid state
6. **No default repository at startup** — `catalog.git` init removed from `main.rs`; no backwards compatibility needed

## Phase 1: Design (Complete)

Artefacts: `research.md`, `data-model.md`, `quickstart.md`

### Implementation Approach by File

#### Cargo.toml
- Remove: `git2 = "0.20.4"`
- Add: `gix = { version = "0.83.0", features = ["max-performance-safe", "tree-editor", "blocking-network-client", "worktree-mutation"] }`
- No other dependency changes required

#### src/git/repo.rs
Five functions, all `git2`-free after migration:

| Function                  | git2 → gix change                                                                                                                  |
|---------------------------|------------------------------------------------------------------------------------------------------------------------------------|
| `init_or_open_repository` | `Repository::open` → `gix::open`; `RepositoryInitOptions` + `Repository::init_opts` → `gix::init_bare` + `edit_reference` for HEAD |
| `get_head_commit`         | `repo.head()?.peel_to_commit()` → `repo.head_commit()?`                                                                            |
| `list_tags`               | `repo.tag_foreach(cb)` → `repo.references()?.tags()?.filter_map(…)` + `r.name().shorten()`                                         |
| `is_release_tag`          | `repo.find_reference(name)?.peel_to_tag().is_ok()` — same call; `&mut self` receiver                                               |
| `get_tag_commit`          | `repo.find_reference(name)?.peel_to_commit()?.id().to_string()` — same call                                                        |

Function signatures are **unchanged** — callers are unaffected. Return type changes from `git2::Repository` to `gix::Repository`; callers in `grpc/server.rs` must be updated to `.into_sync()` after open.

#### src/git/events.rs
- Remove `use git2::Repository` import
- `should_notify_tag_creation` and `create_release_event`: `&Repository` parameter changes to `&gix::Repository` (or eliminated if `grpc/server.rs` is restructured to pass only needed data)
- Test helper `Repository::init_bare` → `gix::init_bare`

#### src/grpc/server.rs
Most significant file. Key changes:

1. **State struct**: `repo_path: Arc<PathBuf>` + single `write_lock` → `data_root: Arc<PathBuf>` + `repo_locks: Arc<DashMap<String, Arc<RwLock<()>>>>`
2. **New RPCs — `create_repository`**: Validate name (FR-020), resolve path as `data_root/<id>.git`, call `git::repo::create_repository(path)`, insert entry into `repo_locks`. Return ALREADY_EXISTS if path exists.
3. **New RPCs — `delete_repository`**: Validate name, acquire write lock, `std::fs::remove_dir_all(path)`, remove from `repo_locks`. Return NOT_FOUND if path absent.
4. **All existing RPCs**: Extract `repository_id` from request, validate, resolve path, acquire appropriate lock (read for reads, write for writes), then proceed with gix operations.
5. **`resolve_ref_to_commit`**: `git2::Repository` → `gix::Repository`; `repo.revparse_single` → `repo.rev_parse_single(…)?.object()?.try_into_commit()?`
6. **`list_tree_files`**: Replace `tree.walk(PreOrder, cb)` with a `Visit` impl; `TreeWalkResult::Skip/Ok` → `ControlFlow::Continue(false/true)`
7. **`commit_file`**: Eliminate `git2::Repository::clone` + index staging; replace with: `write_blob` → `edit_tree(head_tree)?.upsert(path, Blob, blob_oid)?.write()` → `commit_as`
8. **`delete_file`**: Same pattern using `edit_tree(…)?.remove(path)?.write()`
9. **`create_tag`**: `repo.revparse_single` → `rev_parse_single`; `git2::Signature::now` → `gix::actor::Signature`
10. **`list_tags` / `get_latest_tag`**: Update `get_tag_message` to use `peel_to_tag()?.decode()?.message`
11. **Test helpers**: Rewrite using `gix::init_bare`, `write_blob`, `edit_tree`, `commit_as`, `tag`, `edit_reference`

#### src/http_git_server.rs
Three `git2` usages:

1. `Repository::open(&repo_path)` in `info_refs` (dumb fallback) → `gix::open(&repo_path)?`
2. `Repository::open(&repo_path)` in `receive_pack` (existence check) → `gix::open(&repo_path)?`
3. `repository.head()?.shorthand()` → `repo.head_name()?.map(|n| n.shorten().to_string())`
4. `repository.tag_names(None)` → `repo.references()?.tags()?.filter_map(|r| r.ok()).map(|r| r.name().shorten().to_string()).collect()`

The `check_repository` function in the readiness handler uses `Repository::open` → `gix::open`.

#### src/lib.rs / src/main.rs
Update startup sequence: remove `catalog.git` initialisation and `init_or_open_repository` call entirely. `GitServiceImpl::new` now takes only `data_root: PathBuf`. `GitServerState` receives `data_root` instead of `repo_path`. The data directory is still created if absent; no repository is provisioned inside it.

### Observability Updates (FR-014)
Any `tracing` calls that reference `git2` error types (e.g. `git2::Error`) in format strings must be updated to use the equivalent `gix` error display. Structured field names (`path`, `tag`, `commit`) are unchanged.

### Shims Required
- **Initial HEAD for bare repos**: Post-`gix::init_bare` `edit_reference` call to force `HEAD → refs/heads/main`. Documented in code with a comment and a tracking note pointing to this plan.

### Tests
- All existing unit tests in `repo.rs`, `events.rs`, `grpc/server.rs` are updated to use gix APIs. Assertions on SHA strings, ref names, file content, and gRPC status codes are **unchanged**.
- New unit tests for multi-repo behaviour:
  - `test_create_repository_succeeds`
  - `test_create_repository_already_exists`
  - `test_delete_repository_succeeds`
  - `test_delete_repository_not_found`
  - `test_operation_on_unknown_repo_returns_not_found`
  - `test_concurrent_repos_are_isolated`
  - `test_invalid_repo_name_rejected` (path traversal, empty, absolute)
- New integration smoke tests in `tests/integration/mod.rs`:
  - `test_create_clone_push_delete`: full lifecycle via HTTP and gRPC
- All tests written before implementation (Test-First, Constitution I).

## Complexity Tracking

No constitution violations to justify. The tree-editor approach reduces complexity relative to the prior clone-to-workdir pattern.
