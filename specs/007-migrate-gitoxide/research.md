# Research: Migrate gitstore-git-service from git2 to gitoxide

**Feature**: 007-migrate-gitoxide  
**Date**: 2026-05-12  
**Status**: Complete — all NEEDS CLARIFICATION resolved

---

## 1. gitoxide (gix) Current State

**Decision**: Use `gix = "0.83.0"` (released 2026-04-27, MSRV 1.82).  
**Rationale**: Latest stable release. All operations needed by `gitstore-git-service` are covered by stable APIs, with one known gap (index mutation) handled via the `tree-editor` pattern. MSRV 1.82 is already satisfied since `gitstore-git-service` uses Rust edition 2021 and the workspace targets current stable.  
**Alternatives considered**: `gitoxide 0.70.x` (older stable) — rejected because `tree-editor` and `commit_as` API stabilisation were added more recently; pinning to an older version would require more shims.

---

## 2. Thread Safety

**Decision**: Use `gix::ThreadSafeRepository` as the cross-thread token; call `.to_thread_local()` inside each `spawn_blocking` closure.  
**Rationale**: `gix::Repository` is `Send` but not `Sync` (holds `RefCell` buffers). `ThreadSafeRepository` is `Send + Sync` and is created via `repo.into_sync()`. The existing `spawn_blocking` pattern in `grpc/server.rs` is preserved exactly — only the repository type changes.  
**Alternatives considered**: Wrapping in `Arc<Mutex<>>` — rejected as unnecessary; `ThreadSafeRepository` is the canonical gix pattern and avoids lock contention.

---

## 3. Complete git2 → gix API Mapping

### git/repo.rs

| git2                                                                                                         | gix equivalent                                                                                                                    |
|--------------------------------------------------------------------------------------------------------------|-----------------------------------------------------------------------------------------------------------------------------------|
| `Repository::open(path)`                                                                                     | `gix::open(path)?`                                                                                                                |
| `RepositoryInitOptions` + `.bare(true)` + `.mkdir(true)` + `.initial_head("main")` + `Repository::init_opts` | `gix::init_bare(path)?` — always creates dir; `main` enforced via in-memory config override or post-init `edit_reference` on HEAD |
| `repo.head()?.peel_to_commit()`                                                                              | `repo.head_commit()?`                                                                                                             |
| `repo.tag_foreach(\|oid, name\| {...})`                                                                      | `repo.references()?.tags()?.filter_map(\|r\| r.ok())` — each item is `gix::Reference`; `r.name().shorten()` strips `refs/tags/`   |
| `repo.find_reference(name)?.peel_to_tag().is_ok()`                                                           | Same call; `peel_to_tag()` takes `&mut self` in gix                                                                               |
| `repo.find_reference(name)?.peel_to_commit()?.id().to_string()`                                              | Same call; `id()` returns `gix::Id` implementing `Display`                                                                        |

### git/events.rs

Same patterns as repo.rs. Test helper `Repository::init_bare(path)` → `gix::init_bare(path)?`.

### grpc/server.rs

| git2                                                                                        | gix equivalent                                                                                                                                                                |
|---------------------------------------------------------------------------------------------|-------------------------------------------------------------------------------------------------------------------------------------------------------------------------------|
| `repo.revparse_single(ref_str)?.peel_to_commit()`                                           | `repo.rev_parse_single(ref_str.as_bytes())?.object()?.try_into_commit()?` — needs `"revision"` feature (in `default`)                                                         |
| `commit.tree()?.walk(PreOrder, \|dir, entry\| {...})` with `TreeWalkResult::Skip/Ok`        | Custom `Visit` impl via `tree.traverse().breadthfirst(delegate)?`; `ControlFlow::Continue(false)` from `visit_tree()` = skip subtree; `ControlFlow::Continue(true)` = descend |
| `git2::Repository::clone(url, path)` (clone bare to working dir)                            | **Replaced entirely**: use `tree-editor` directly on the bare repo — no working-dir clone needed (see below)                                                                  |
| `index.add_path(path)` / `index.remove_path(path)` / `index.write()` / `index.write_tree()` | **Not available in gix high-level API** — eliminated by `tree-editor` approach                                                                                                |
| `repo.find_tree(oid)`                                                                       | `repo.find_object(oid)?.try_into_tree()?` or `repo.find_tree(oid)?`                                                                                                           |
| `git2::Signature::now(name, email)`                                                         | Construct `gix_actor::Signature { name: name.into(), email: email.into(), time: gix_date::Time::now_local_or_utc() }`                                                         |
| `repo.commit(Some("HEAD"), &sig, &sig, msg, &tree, &parents)`                               | `repo.commit_as(&committer, &author, "HEAD", msg, tree_id, parent_ids)?`                                                                                                      |
| `repo.reference("refs/heads/main", oid, false, msg)`                                        | `repo.reference("refs/heads/main", oid, PreviousValue::MustNotExist, msg)?`                                                                                                   |
| `repo.find_remote("origin")?.push(...)`                                                     | `repo.find_remote("origin")?.connect(Push)?.prepare_push(...)?.send(...)?` — needs `"blocking-network-client"`                                                                |
| `repo.tag(name, &obj, &sig, msg, false)`                                                    | `repo.tag(name, target_id, Kind::Commit, Some(sig_ref), msg, PreviousValue::MustNotExist)?`                                                                                   |
| `repo.find_reference(name)?.peel_to_tag()?.message()`                                       | `repo.find_reference(name)?.peel_to_tag()?.decode()?.message.to_string()`                                                                                                     |
| `repo.reference("refs/heads/main", oid, false, msg)` (initial branch after init)            | `repo.edit_reference(RefEdit { ... Target::Symbolic("refs/heads/main") ... })?`                                                                                               |
| Tests: `git2::Repository::init_bare`, `repo.blob()`, `repo.treebuilder()` etc.              | `gix::init_bare()`, `repo.write_blob()`, `repo.edit_tree(parent_tree_id)?.upsert(...)?.write()?`                                                                              |

### http_git_server.rs

| git2                                         | gix equivalent                                                                                            |
|----------------------------------------------|-----------------------------------------------------------------------------------------------------------|
| `Repository::open(path)` (existence check)   | `gix::open(path)?`                                                                                        |
| `repository.head()?.shorthand()`             | `repo.head_name()?.map(\|n\| n.shorten().to_string())`                                                    |
| `repository.tag_names(None)` → `Vec<String>` | `repo.references()?.tags()?.filter_map(\|r\| r.ok()).map(\|r\| r.name().shorten().to_string()).collect()` |

---

## 4. Key Architectural Decision: Eliminate Clone-to-Workdir Pattern

**Decision**: Replace the `commit_file` / `delete_file` clone-to-tmpdir pattern with direct in-memory tree editing on the bare repository using gix's `tree-editor`.  
**Rationale**: The current approach (clone bare → working dir → stage → commit → push back) exists because git2 requires a working directory for index operations. gix's `tree-editor` works directly on the object database, making the round-trip unnecessary. This is simpler, faster, and avoids the `blocking-network-client` requirement for the local push-back step.  
**Pattern**:
```
1. Open bare repo
2. Resolve HEAD to current commit (or handle empty repo)
3. write_blob(content) → blob_oid
4. edit_tree(current_tree_id)?.upsert("path", EntryKind::Blob, blob_oid)?.write()? → new_tree_id
5. commit_as(&sig, &sig, "HEAD", message, new_tree_id, [parent_commit_id])? → commit_oid
```
For `delete_file`: step 3 is skipped; step 4 uses `.remove("path")?` instead of `.upsert()`.  
**Alternatives considered**: Keeping the clone-to-workdir pattern via gix's `prepare_clone` — rejected because it adds the `blocking-network-client` feature dependency for a purely local operation and is more complex.

---

## 5. Cargo.toml Changes

**Decision**: Replace `git2 = "0.20.4"` with the following:

```toml
[dependencies]
gix = { version = "0.83.0", features = [
    "max-performance-safe",
    "tree-editor",
    "blocking-network-client",
    "worktree-mutation",
] }
dashmap = "6"   # concurrent HashMap for per-repository lock map (multi-repo state)
```

`max-performance-safe` pulls in `parallel` (making `Repository` `Send`) plus pack caches. `tree-editor` enables the in-memory tree mutation API. `blocking-network-client` is needed only for the HTTP server's repository existence check via `gix::open` (which is already covered by default features) and for any remote push operations — if the clone-to-workdir pattern is eliminated, `blocking-network-client` may be dropped in a follow-up.

`gix` re-exports the needed sub-crate types via `gix::objs` (= `gix-object` types), `gix::actor` (= `gix-actor::Signature`), and `gix::refs` (= `gix-ref` types). Direct sub-crate dependencies are not required.

**Rationale**: Minimise the dependency footprint; use the umbrella `gix` crate's re-exports rather than listing individual sub-crates.  
**Alternatives considered**: Adding `gix-actor`, `gix-date`, `gix-ref`, `gix-traverse` as direct dependencies — rejected in favour of re-exports unless a type is not re-exported by `gix`.

---

## 6. Known Gaps and Required Shims

| Gap                                                                            | Shim approach                                                                            | Tracking                                                      |
|--------------------------------------------------------------------------------|------------------------------------------------------------------------------------------|---------------------------------------------------------------|
| `gix::init_bare` does not accept `initial_head` option — reads from git config | Post-init HEAD `edit_reference` call to force `refs/heads/main` regardless of env config | Document in code; remove once gix adds `init_opts` equivalent |
| `gix::Repository::set_head()` has no high-level equivalent                     | Low-level `repo.edit_reference()` with `Target::Symbolic`                                | Acceptable; same semantics                                    |
| Direct index mutation (`add_path`/`remove_path`) not in gix high-level API     | Eliminated by tree-editor approach; no shim needed                                       | —                                                             |

---

## 7. Behavioural Differences to Document

- `peel_to_tag()` and `peel_to_commit()` take `&mut self` in gix (not `&self` as in git2). Code that calls these on shared references must be adjusted.
- `tag.message` in gix requires `tag.decode()?.message` — it is `&BStr`, not `Option<&str>`. `lightweight_tag.message` returns an empty `BStr` after peeling (not `None`).
- `gix::FullNameRef::shorten()` automatically strips `refs/tags/`, `refs/heads/`, `refs/remotes/origin/` — the manual `strip_prefix("refs/tags/")` calls can be removed.
- `rev_parse_single` returns `gix::Id` (not an object); calling `.object()?` materialises it. The two-step replaces git2's one-step `revparse_single().peel_to_commit()`.
- Tree traversal `ControlFlow::Continue(false)` from `visit_tree()` replaces `TreeWalkResult::Skip`; the semantics are identical.

---

## 8. Constitution Compliance Check

All decisions comply with the GitStore Constitution:

- **Test-First (I)**: Existing tests updated before implementation; new behaviour validated by tests written before code.
- **Observability (IV)**: FR-014 requires all `git2`-specific log/trace signals to be updated to `gitoxide` equivalents; no signal dropped.
- **Simplicity (VII)**: tree-editor eliminates the clone-to-workdir complexity; no new abstractions introduced.
- **API-First (II)**: No gRPC or HTTP contract changes; migration is purely internal.
