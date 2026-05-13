# Developer Quickstart: 007-migrate-gitoxide

**Feature**: Migrate gitstore-git-service from git2 to gitoxide  
**Branch**: `007-migrate-gitoxide`

---

## Prerequisites

- Rust toolchain (stable, edition 2021; MSRV 1.82 — required by `gix 0.83`)
- No libgit2 or pkg-config needed after migration

Verify your toolchain:

```bash
rustc --version   # must be ≥ 1.82
cargo --version
```

---

## Build

```bash
cd gitstore-git-service
cargo build
```

After migration, the build **must not require libgit2** to be installed. If you see a build error referencing `libgit2` or `pkg-config`, the migration is incomplete — check that `git2` has been fully removed from `Cargo.toml` and `Cargo.lock`.

---

## Run Tests

```bash
cd gitstore-git-service

# Unit + integration tests (all)
cargo test

# Single module
cargo test git::repo
cargo test grpc::server

# With output
cargo test -- --nocapture
```

All tests that previously used `git2` types directly (e.g. `git2::Repository::init_bare`, `git2::Signature::now`) will be updated to use `gix` equivalents. The observable assertions (commit SHAs, ref names, file contents, gRPC status codes) remain identical.

---

## Verify No libgit2 Dependency

```bash
# Confirm git2 is absent from the lock file
grep -c "^name = \"git2\"" gitstore-git-service/../Cargo.lock
# Expected: 0

# Build in a Docker container without libgit2 (optional, mirrors CI)
docker run --rm -v "$(pwd)":/src -w /src/gitstore-git-service \
  rust:1.82-slim cargo build 2>&1 | grep -v "^$"
```

---

## Key Migration Notes for Developers

### No Default Repository at Startup

The service no longer creates `catalog.git` (or any repository) at boot. The data directory is prepared, but all repositories are provisioned via `CreateRepository` RPC. The startup `init_or_open_repository` call in `main.rs` is removed.

### Multi-Repository Routing

Every gRPC handler now:
1. Reads `repository_id` from the request
2. Validates the name (non-empty, no `/`, `\`, `..`)
3. Resolves the path: `data_root/<repository_id>.git`
4. Returns `Status::NOT_FOUND` immediately if the path does not exist
5. Acquires the per-repository lock from `repo_locks` map
6. Calls `gix::open(path)?` to get a thread-local `Repository`

### Repository Handle Pattern

`gix::Repository` is opened per-request (not cached at startup) to support an unbounded number of repositories. If open cost becomes measurable it can be cached behind a `ThreadSafeRepository` map in a follow-up.

```rust
// Per-request (inside spawn_blocking)
let repo: gix::Repository = gix::open(&repo_path)?;
```

### Commit File / Delete File

The old pattern (clone bare repo to tmpdir → stage → commit → push back) is replaced by direct tree editing:

```rust
// Write blob
let blob_oid = repo.write_blob(content)?;

// Edit tree
let new_tree = repo
    .edit_tree(head_commit.tree_id()?.detach())?
    .upsert("path/to/file", gix::object::tree::EntryKind::Blob, blob_oid.detach())?
    .write()?;

// Commit
let commit_id = repo.commit_as(
    &sig, &sig,
    "HEAD",
    message,
    new_tree.detach(),
    [head_commit.id],
)?;
```

### Tag Message Access

gix requires an extra `.decode()` call to access tag metadata:

```rust
// git2: tag.message()  →  Option<&str>
// gix:
let tag = reference.peel_to_tag()?;
let decoded = tag.decode()?;
let message = decoded.message.to_string();
```

### Reference Name Shortening

Replace manual `strip_prefix("refs/tags/")` calls:

```rust
// Before: name_str.strip_prefix("refs/tags/")
// After:
let short_name: &gix::bstr::BStr = reference.name().shorten();
```

---

## CI Check (pre-PR)

```bash
cd gitstore-git-service
cargo fmt --all -- --check
cargo clippy --all-targets --all-features -- -D warnings
cargo build --verbose
cargo test --verbose
```
