# Quickstart: Fix git clone and git fetch over HTTP (spec#019)

## What this feature fixes

`git clone` and `git fetch` over HTTP fail because `handle_upload_pack` in
`gitstore-git-service` sends `NAK + 0000` (no pack) instead of `NAK + pack`
when the client sends `want + done` with no `have` lines.

Two root causes:
1. `parse_wants_and_haves` does not parse `done` — the caller cannot tell
   whether the client has committed to receiving a pack.
2. `build_pack_for_wants` silently returns an empty byte slice when `rev_walk`
   yields no commits, producing an indistinguishable-from-nothing response.

## Files changed

| File | Change |
|------|--------|
| `gitstore-git-service/src/git/pack_server.rs` | Three targeted edits + new unit tests |

No proto changes, no Go changes, no config changes.

## The three edits

### 1. `parse_wants_and_haves` — add `done` flag

```rust
// Signature change
pub fn parse_wants_and_haves(body: &[u8]) -> (Vec<String>, Vec<String>, bool)

// Inside the loop, add:
} else if s == "done" {
    done_seen = true;
}
// Return: (wants, haves, done_seen)
```

### 2. `handle_upload_pack` — guard pack generation on `done`

```rust
let (wants, haves, done_seen) = parse_wants_and_haves(body);

if wants.is_empty() || !done_seen {
    write_pkt_line(&mut response, b"NAK\n")?;
    response.extend_from_slice(b"0000");
    return Ok(response);
}
// ... build and send pack
```

### 3. `build_pack_for_wants` — error instead of silent empty pack

```rust
// Replace:
if walk_ids.is_empty() {
    return Ok(Vec::new());
}

// With:
if walk_ids.is_empty() {
    anyhow::bail!("upload-pack: rev_walk produced no objects for {} want(s)", want_ids.len());
}
```

## Verifying the fix

```bash
# Start the dev stack
make dev

# Bootstrap a namespace and repo (if not already done)
make bootstrap ADMIN_PASSWORD=<password>

# Clone — should succeed after this fix
git clone http://localhost:5000/gitstore/catalog.git /tmp/test-clone
cd /tmp/test-clone && git log --oneline

# Push a new commit from a second clone, then fetch
git clone http://localhost:5000/gitstore/catalog.git /tmp/test-clone-2
# ... make a commit in /tmp/test-clone-2 and push ...
cd /tmp/test-clone && git fetch && git log --oneline origin/main
```

## Running the tests

```bash
cd gitstore-git-service

# Unit tests covering the three fixes
cargo test --lib git::pack_server

# All unit tests
cargo test --lib

# Integration tests (requires running stack)
cd ../tests/integration
NAMESPACE=gitstore REPOSITORY=catalog \
  go test -v -tags=integration -run TestGitClone -run TestGitFetch ./...
```

## Constitution compliance

- **Test-First (I)**: T050–T059 unit tests written first, verified to fail, then
  implementation follows.
- **Simplicity (VII)**: Three targeted edits in one file. No new abstractions,
  no new types, no new dependencies.
- **Observability (IV)**: Existing `emit_span("upload-pack-rpc", ...)` already
  logs outcome; the error path now propagates a clear message instead of silently
  succeeding with zero bytes.
