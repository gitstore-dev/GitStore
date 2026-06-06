# Data Model: Fix git clone and git fetch over HTTP (spec#019)

This feature is a focused bug fix within a single Rust source file
(`gitstore-git-service/src/git/pack_server.rs`). There are no new entities,
no new database tables, and no new proto messages. The changes are:

---

## Modified: `parse_wants_and_haves`

**File**: `gitstore-git-service/src/git/pack_server.rs`

```rust
// Before
pub fn parse_wants_and_haves(body: &[u8]) -> (Vec<String>, Vec<String>)

// After
pub fn parse_wants_and_haves(body: &[u8]) -> (Vec<String>, Vec<String>, bool)
//                                                                         ^^^
//                                                       done_seen: true when
//                                                       "done\n" pkt-line parsed
```

The third element is `true` when the client sent `done`, signalling the end of
want/have negotiation and that the server must send a pack.

---

## Modified: `handle_upload_pack`

**File**: `gitstore-git-service/src/git/pack_server.rs`

Three-case logic (was two-case):

| Condition | Response |
|-----------|----------|
| `wants.is_empty()` | `NAK + 0000` (nothing requested — unchanged) |
| `!wants.is_empty() && !done_seen` | `NAK + 0000` (negotiation still in progress) |
| `!wants.is_empty() && done_seen` | `NAK + sideband-pack + 0000` (send pack) |

---

## Modified: `build_pack_for_wants`

**File**: `gitstore-git-service/src/git/pack_server.rs`

Two changes:

**1. Annotated tag dereferencing** — before `rev_walk`, peel each `want_id`:

```rust
// Conceptual shape (not verbatim — see implementation)
let mut commit_tips: Vec<ObjectId> = Vec::new();
let mut extra_objects: Vec<ObjectId> = Vec::new();

for &oid in &want_ids {
    match repo.find_object(oid)?.kind {
        Kind::Tag => {
            // Include the tag object itself and walk from its target commit.
            extra_objects.push(oid);
            commit_tips.push(peel_to_commit(repo, oid)?);
        }
        _ => commit_tips.push(oid),
    }
}
// rev_walk uses commit_tips; extra_objects are counted with AsIs expansion.
```

**2. Error on empty walk** — replace silent `Ok(Vec::new())`:

```rust
// Before
if walk_ids.is_empty() {
    return Ok(Vec::new());
}

// After
if walk_ids.is_empty() {
    anyhow::bail!("upload-pack: rev_walk produced no objects for {} want(s); \
                   repository may be corrupt or wants are not reachable", want_ids.len());
}
```

This surfaces the failure through the gRPC error path, which sends a channel-3
sideband error to the git client instead of a silent empty pack.

---

## New: Unit tests for `pack_server.rs`

**File**: `gitstore-git-service/src/git/pack_server.rs` (new `#[cfg(test)]` module)

| Test ID | Scenario |
|---------|----------|
| T050 | `parse_wants_and_haves`: want+done, caps stripped |
| T051 | `parse_wants_and_haves`: want+have+done |
| T052 | `parse_wants_and_haves`: done absent → `done_seen=false` |
| T053 | `parse_wants_and_haves`: empty body |
| T054 | `handle_upload_pack`: empty wants → NAK+flush |
| T055 | `handle_upload_pack`: wants+done → NAK+sideband pack |
| T056 | `handle_upload_pack`: wants but no done → NAK+flush |
| T057 | `build_pack_for_wants`: fresh clone (no haves) — pack non-empty |
| T058 | `build_pack_for_wants`: incremental fetch (haves=tip) — empty pack |
| T059 | `build_pack_for_wants`: non-empty wants with unreachable OID → error |
