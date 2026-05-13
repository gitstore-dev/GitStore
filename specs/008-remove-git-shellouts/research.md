# Research: Remove Git Binary Shell-Outs

**Branch**: `008-remove-git-shellouts` | **Date**: 2026-05-13

## Decision 1: In-Process Protocol Strategy for upload-pack

**Decision**: Hand-roll pkt-line serialization using `gix-packetline` + `gix-pack` sub-crates for advertisement; use `gix-protocol` server state machines where they exist in 0.83.

**Rationale**: gix 0.83 exposes client-side protocol machinery (`gix::protocol`, `gix-transport`) but does not ship a high-level `upload_pack_server()` function. The lower-level sub-crates (`gix-packetline`, `gix-pack`) are usable directly. Advertisement (advertise-refs) is straightforward: enumerate refs via `gix::Repository::references()` and serialize with `gix-packetline`. The stateless-rpc fetch response requires generating a pack from the object database via `gix-pack::data::output`.

**Alternatives considered**:
- Shell out to `git upload-pack`: current approach — eliminated by this feature
- Wait for a future gix server-side API: defers the feature indefinitely; gix 0.83 is already present
- Use `gitoxide`'s `ein` daemon as a subprocess: still a shell-out, defeats the purpose

---

## Decision 2: In-Process Protocol Strategy for receive-pack

**Decision**: Implement receive-pack in-process by: (1) reading the client's pkt-line ref-update commands and pack data from the HTTP body; (2) writing the pack to the object store via `gix-pack`; (3) committing ref updates atomically via `gix::refs::transaction` with `PreviousValue::MustExistAndMatch` / `PreviousValue::MustNotExist` for fast-forward enforcement.

**Rationale**: The codebase already uses `gix::refs::transaction` (in `src/git/repo.rs`) for ref creation. Pack writing via `gix-pack::data::input::bytes_to_entries` + `gix::odb::store::Handle::write_buf` is the canonical path. Atomicity is provided by the existing ref-transaction API: if any ref edit fails, the transaction is rolled back — satisfying FR-011.

**Alternatives considered**:
- Shell out to `git receive-pack`: eliminated
- Use `tempfile` for partial writes then `rename` for atomicity: `gix::refs::transaction` already does this via lock-files; no additional layer needed

---

## Decision 3: Pack Size Limit Enforcement

**Decision**: Enforce the configurable maximum pack size at the Axum HTTP body layer using `axum::extract::DefaultBodyLimit` middleware applied only to the `POST /{repo}/git-receive-pack` route, reading the limit from `GitServerConfig`.

**Rationale**: gix has no built-in pack size gate. Enforcing the limit before any gix call is called prevents memory exhaustion from a large in-memory `Bytes` collection. `DefaultBodyLimit` (Axum 0.8) is the canonical, zero-copy mechanism — it rejects oversized bodies before the handler runs, returns HTTP 413, and streams no data to disk. No custom `AsyncRead` wrapper is needed.

**Alternatives considered**:
- Wrap the `Bytes` reader with a size-counting reader inside the handler: works but fires after Axum has already buffered; does not prevent memory exhaustion
- Add `Content-Length` check in handler: clients may omit the header; not reliable

---

## Decision 4: Dockerfile Runtime git Package Removal

**Decision**: Remove `git` from the `apk add` call in the runtime stage. Also remove the stale `/etc/gitconfig` `[safe]\ndirectory = *` write (that comment was accurate for `git2` but gix does not enforce `safe.directory` and never did).

**Rationale**: `gix::open` does not call the system `git` binary and does not read or enforce `safe.directory`. The comment in the Dockerfile explicitly says it was needed for `libgit2` (the `git2` crate) — Feature 007 already replaced `git2` with `gix`, so the `gitconfig` write is dead code. Removing both simplifies the image.

**Alternatives considered**:
- Keep `/etc/gitconfig` write for safety: it's a no-op for gix; removing it reduces image complexity
- Keep `git` installed for operator debugging: contradicts FR-006 and SC-003; operators can use a debug image if needed

---

## Decision 5: init-demo-catalog.sh Removal Strategy

**Decision**: Delete `scripts/init-demo-catalog.sh` and remove all four doc references (2 in `docs/docker-troubleshooting.md`, 1 in `docs/developer-guide.md`, 1 in `docs/user-guide.md`). No replacement workflow is introduced in this feature.

**Rationale**: The init script shells out to `git` (6 times: `git init`, `git config`, `git add`, `git commit`, `git clone --bare`). With the `git` binary absent from the runtime image, the script is non-functional. The spec explicitly scopes replacement onboarding to a future feature. The doc sections can be simplified to a note that demo data seeding is not yet provided.

**Alternatives considered**:
- Rewrite the script using the gRPC `CommitFile` API: out of scope for this feature; deferred
- Leave the script in-tree with a deprecation notice: misleads contributors; removed cleanly

---

## Decision 6: New gix Sub-Crate Dependencies

**Decision**: Add direct Cargo dependencies on `gix-packetline`, `gix-pack`, and `gix-protocol` at versions compatible with `gix 0.83.0`. Verify version compatibility via `cargo tree` before finalising.

**Rationale**: The top-level `gix` re-export does not surface pkt-line encode/decode or the low-level pack-output builder needed for upload-pack stateless-rpc. Direct sub-crate deps are the standard pattern used across the gix ecosystem.

**Alternatives considered**:
- Enable additional `gix` feature flags that transitively pull in sub-crates: `gix` features control client behaviour, not server; sub-crate versions would still need to be pinned to avoid drift

---

## Decision 7: Integration Test Strategy

**Decision**: Add a new `tests/integration/http_smart_protocol.rs` test module that: (1) spawns a `GitServerState` against a `tempfile::TempDir`; (2) initialises a bare repo using `gix::init_bare`; (3) uses the `git2` crate (dev-dependency only) or shells to the local `git` binary from the *test host* (not the container) to drive clone/fetch/push against the Axum test server via `axum-test` or `reqwest`; (4) verifies no `std::process::Command::new("git")` call is made in production code paths during the test by asserting via a CI grep gate.

**Rationale**: The test host in CI has the `git` binary available (and must). It is the *server container* that must not have it. This distinction is critical: the integration tests verify the server handles the protocol correctly in-process; they do not themselves need to be binary-free. The separate CI grep gate (`rg 'Command::new\("git"\)'`) provides the code-level proof.

**Alternatives considered**:
- Docker-compose test that removes `git` from PATH in the server container: more realistic but slower CI feedback loop; this is better suited for a nightly smoke test, not the primary gate
