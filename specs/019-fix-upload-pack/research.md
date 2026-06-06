# Research: Fix git clone and git fetch over HTTP (spec#019)

## Root Cause

**Decision**: There are two distinct failure modes in `handle_upload_pack`
(`gitstore-git-service/src/git/pack_server.rs`), and one gap in test coverage.

### Failure mode A — silent empty pack (primary root cause)

`build_pack_for_wants` returns `Ok(Vec::new())` at line 548–550 when `walk_ids`
is empty. When this happens, the `pack_data.chunks(SIDEBAND_CHUNK)` loop at
line 73 produces zero iterations. The response sent to the client becomes:

```
0008NAK\n
0000
```

This is byte-for-byte identical to the "nothing requested" path at lines 60–63,
which is also `NAK + 0000`. A git client receiving NAK followed by a flush with
no pack data closes with an error — the clone/fetch silently produces no objects.

`walk_ids` can be empty despite valid wants in two confirmed scenarios:
1. All wanted commits are older than the oldest `have` commit (the
   `ByCommitTimeCutoff` optimisation introduced by `with_boundary` prunes them
   before the boundary check fires).
2. `rev_walk(...).filter_map(|r| r.ok()...)` silently drops any
   `Err` from the walk iterator — if the first commit resolution fails, every
   subsequent commit is dropped and `walk_ids` ends up empty.

**Fix**: Replace the silent `Ok(Vec::new())` with
`anyhow::bail!("rev_walk produced no objects for non-empty wants")` so the
caller can propagate a sideband channel-3 error to the client rather than an
empty pack.

### Failure mode B — `done` not parsed

`parse_wants_and_haves` only recognises `"want "` and `"have "` prefixes; it
silently skips `"done"`. The return type `(Vec<String>, Vec<String>)` gives the
caller no way to distinguish "client sent wants and is ready for a pack" from
"client sent wants and is still mid-negotiation". In stateless-rpc mode each
POST should end with `done`, but without the flag the server cannot enforce this
invariant.

**Fix**: Change return type to `(Vec<String>, Vec<String>, bool)` where `bool`
is `true` when `done` was parsed. Add a `done_seen` guard in
`handle_upload_pack`: if `wants` is non-empty but `done` was not seen, respond
`NAK + 0000` (still-negotiating) instead of building a pack.

### Gap — annotated tag OIDs not dereferenced

`want_ids` at line 513–516 converts raw hex strings directly into `ObjectId`
values and passes them to `rev_walk`. `rev_walk` traverses commits only; a tag
OID passed as a starting tip is not a commit and is silently unreachable by the
walk. A client that `want`s an annotated tag OID gets an incomplete pack missing
the tag object.

`collect_refs` (line 392–453) already advertises peeled tags
(`refs/tags/v1.0^{}`) so clients can request the underlying commit, but the tag
object itself is a valid `want` target per the protocol. Without dereferencing,
`walk_ids` will be empty for a `want` pointing at an annotated tag, triggering
failure mode A.

**Fix**: Before `rev_walk`, peel each `want_id`: if the object kind is `Tag`,
extract its target commit OID and add the tag object itself to a `tag_objects`
list that is merged into the pack separately via
`count::objects_unthreaded(..., ObjectExpansion::AsIs)`.

---

## Wire Format — Git Protocol v1 Upload-Pack (stateless-rpc)

**Decision**: The correct response for `want + done` (no `have` lines) is:

```
S: 0008NAK\n                           ← bare pkt-line, NOT sideband
S: <pkt-line(0x01 + pack-chunk)> × N   ← channel 1, one pkt-line per chunk
S: 0000                                 ← flush-pkt, terminates sideband stream
```

The current framing in `handle_upload_pack` (lines 66–79) is **correct**:
NAK is a bare pkt-line, sideband uses `0x01` prefix, `0000` terminates.

**Alternatives considered**: Sideband-wrapping NAK (rejected — violates
gitprotocol-pack.txt); sending a second `0000` after NAK before the pack
(rejected — not part of the protocol).

---

## `parse_wants_and_haves` — correctness of current parsing

The current parser correctly handles:
- `want <oid>\n` lines (populates `wants`)
- `have <oid>\n` lines (populates `haves`)
- flush-pkt `0000` (advances `pos += 4`, continues)
- `done\n` (silently skipped — **missing flag**)
- capability strings after `\0` (stripped by `.split('\0').next()` — correct)

No parsing bugs beyond the missing `done` flag.

---

## `build_pack_for_wants` — pack generation correctness

- **`rev_walk` yields commit IDs only** — confirmed. Trees and blobs are NOT
  in `walk_ids`.
- **`ObjectExpansion::TreeContents` expands each commit** to its root tree,
  all sub-trees, and all blobs via `breadthfirst` traversal — confirmed
  correct. The pack is complete for normal branch refs.
- **`with_boundary([])` with empty haves** does a full traversal from
  `want_ids` (fresh clone case) — confirmed correct.
- **Time-cutoff risk**: `with_boundary(have_ids)` switches gix to
  `ByCommitTimeCutoff` sorting when haves are non-empty. Commits older than
  the oldest have are pruned before the boundary check. This can silently
  exclude needed commits in unusual DAG topologies. Out of scope for this
  fix; noted as a known limitation.

---

## Unit test gap

`pack_server.rs` has zero unit tests. The only coverage is the integration
test suite (`tests/integration/git_http_test.go`), which does not isolate
failure modes. Tests needed:
- `parse_wants_and_haves`: want+have+done, want-only+done, done-absent,
  empty body, caps stripping, flush-pkt skipping.
- `build_pack_for_wants`: fresh clone (no haves), incremental fetch (haves
  present), annotated tag want, empty walk error propagation.
- `handle_upload_pack`: empty wants → NAK+flush, valid wants+done → NAK+pack,
  valid wants but no done → NAK+flush.
