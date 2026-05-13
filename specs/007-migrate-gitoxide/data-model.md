# Data Model: Migrate gitstore-git-service from git2 to gitoxide

**Feature**: 007-migrate-gitoxide  
**Date**: 2026-05-12

This feature has two concerns: replacing `git2` with `gix`, and introducing multi-repository hosting. On the data side, the only persistent entity added is the **Repository** — a named bare Git repository managed by the service on disk. No database or external store is introduced; repositories live as filesystem directories under the configured data root. The section below covers both the new domain entity and the internal type-system changes from the `git2` → `gix` migration.

---

## Domain Entity: Repository

| Attribute | Description                                                                                                                            |
|-----------|----------------------------------------------------------------------------------------------------------------------------------------|
| `id`      | Caller-supplied unique name (e.g. `"@hash(catalog)"`, `"@hash(org/repo)"`). Used as the filesystem directory name under the data root. |
| `path`    | Absolute filesystem path: `<data_root>/<id>.git`                                                                                       |
| `state`   | Implicit from filesystem: exists → available; absent → not found                                                                       |

**Lifecycle**:
- `CreateRepository` → allocates directory, runs `gix::init_bare`, sets HEAD to `refs/heads/main`
- `DeleteRepository` → removes directory tree atomically
- All other operations → resolve `id` to path, fail-fast with NOT_FOUND if path absent

**Name validation rules** (FR-020):
- Must be non-empty
- Must not contain `/`, `\`, or `..` path components
- Must not be an absolute path

**Concurrency model**: Each repository has its own `RwLock`. The lock map is stored in the service state (`HashMap<String, Arc<RwLock<()>>>`). Reads acquire a read lock; writes acquire a write lock. Locks for different repositories are independent (FR-021).

---

## Internal Type Mapping (git2 → gix)

These are not persisted entities — they are in-memory representations of Git objects used within the service boundary. The mapping is provided to guide implementation.

### Repository Handle

| Concept                  | git2 type                                    | gix type                    | Notes                                                                                       |
|--------------------------|----------------------------------------------|-----------------------------|---------------------------------------------------------------------------------------------|
| Thread-local repo handle | `git2::Repository`                           | `gix::Repository`           | Not `Sync`; obtained from `ThreadSafeRepository::to_thread_local()` inside `spawn_blocking` |
| Cross-thread repo token  | _(not needed; `git2::Repository` is `Send`)_ | `gix::ThreadSafeRepository` | Created once via `gix::open(path)?.into_sync()`; stored in service state                    |

### Git Object Types

| Concept                      | git2 type                      | gix type                                                             |
|------------------------------|--------------------------------|----------------------------------------------------------------------|
| Object ID / SHA              | `git2::Oid`                    | `gix::ObjectId` / `gix::Id<'_>`                                      |
| Commit object                | `git2::Commit<'_>`             | `gix::Commit<'_>`                                                    |
| Tree object                  | `git2::Tree<'_>`               | `gix::Tree<'_>`                                                      |
| Blob object                  | _(bytes via `blob.content()`)_ | `gix::Object<'_>` → `.data`                                          |
| Tag object                   | `git2::Tag<'_>`                | `gix::Tag<'_>` → decoded via `.decode()?` → `gix_object::TagRef<'_>` |
| Reference                    | `git2::Reference<'_>`          | `gix::Reference<'_>`                                                 |
| Signature (author/committer) | `git2::Signature<'_>`          | `gix::actor::Signature` (owned)                                      |
| Tree entry mode              | `git2::FileMode`               | `gix::object::tree::EntryKind`                                       |

### Tree Traversal

| Concept              | git2                                       | gix                                                |
|----------------------|--------------------------------------------|----------------------------------------------------|
| Walk callback result | `git2::TreeWalkResult::Ok`                 | `ControlFlow::Continue(true)`                      |
| Skip subtree         | `git2::TreeWalkResult::Skip`               | `ControlFlow::Continue(false)` from `visit_tree()` |
| Abort traversal      | _(return false)_                           | `ControlFlow::Break(())`                           |
| Visitor trait        | closure `\|dir: &str, entry: &TreeEntry\|` | `impl gix_traverse::tree::Visit`                   |

### Reference Transactions

| Concept                      | git2                                    | gix                                            |
|------------------------------|-----------------------------------------|------------------------------------------------|
| Create ref (must not exist)  | `repo.reference(name, oid, false, msg)` | `PreviousValue::MustNotExist`                  |
| Update ref (any prior value) | `repo.reference(name, oid, true, msg)`  | `PreviousValue::Any`                           |
| Symbolic ref (e.g. HEAD)     | `repo.set_head(target)`                 | `edit_reference` with `Target::Symbolic(name)` |

---

## Service State Changes

### Current state in `GitServiceImpl` (grpc/server.rs)

```
GitServiceImpl {
    repo_path: Arc<PathBuf>,   // single hardcoded catalog.git path
    write_lock: Arc<RwLock<()>>,
}
```

### Post-migration state

```
GitServiceImpl {
    data_root: Arc<PathBuf>,
    repo_locks: Arc<DashMap<String, Arc<RwLock<()>>>>,
    // DashMap (or equivalent) for per-repo write locks; read operations acquire read lock
}
```

**Rationale**: The single `repo_path` + single `write_lock` is replaced by a `data_root` (the directory under which all repos live as `<name>.git` subdirectories) and a concurrent map of per-repository locks. Callers supply `repository_id` on each request; the handler resolves the path as `data_root/<repository_id>.git` and acquires the appropriate lock. `gix::open(path)` is called per-request rather than once at startup — this is intentional for multi-repo hosting where the set of open repositories is unbounded. If per-request open cost proves significant, a `ThreadSafeRepository` cache can be added in a follow-up without any API changes.

### Current state in `GitServerState` (http_git_server.rs)

```
GitServerState {
    repo_path: PathBuf,         // single hardcoded path to data dir
    broadcaster: Arc<RwLock<Broadcaster>>,
    start_time: Instant,
}
```

### Post-migration state

```
GitServerState {
    data_root: PathBuf,         // directory containing all repos
    broadcaster: Arc<RwLock<Broadcaster>>,
    start_time: Instant,
}
```

HTTP smart protocol routes (`/{repo}/info/refs`, `/{repo}/git-upload-pack`, `/{repo}/git-receive-pack`) already accept a `{repo}` path segment. After migration the handler resolves `data_root/{repo}.git` (validating the name with the same rules as FR-020) and returns 404 if the path does not exist. No new routes are needed.

---

## No Schema or Protocol Changes

- The gRPC proto contract (`gitstore.git.v1`) is unchanged.
- The HTTP smart protocol endpoints (`/info/refs`, `/git-upload-pack`, `/git-receive-pack`) continue to shell out to the `git` binary — no change to transport logic.
- WebSocket broadcast format (`GitEvent` JSON) is unchanged.
- All public error types and gRPC `Status` codes returned by handlers are unchanged.
