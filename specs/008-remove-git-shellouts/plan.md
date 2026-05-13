# Implementation Plan: Remove Git Binary Shell-Outs and Init Script

**Branch**: `008-remove-git-shellouts` | **Date**: 2026-05-13 | **Spec**: [spec.md](spec.md)  
**Input**: Feature specification from `specs/008-remove-git-shellouts/spec.md`

## Summary

Replace the four `std::process::Command::new("git")` shell-out call sites in `gitstore-git-service/src/http_git_server.rs` with in-process implementations using `gix-packetline`, `gix-pack`, and `gix-protocol` sub-crates. Remove the `git` runtime package from the container image. Remove `scripts/init-demo-catalog.sh` and all documentation references to it. Add a configurable pack-size limit and structured log events at each protocol phase.

## Technical Context

**Language/Version**: Rust edition 2021, MSRV 1.82  
**Primary Dependencies**: `gix 0.83.0`, `gix-packetline` (compatible version), `gix-pack` (compatible version), `gix-protocol` (compatible version), `axum 0.8`, `tokio 1.35`, `tracing 0.1`, `tempfile 3.8` (dev)  
**Storage**: Bare Git repositories on local filesystem (unchanged)  
**Testing**: `cargo test`, `axum-test` or `reqwest` for HTTP integration tests  
**Target Platform**: Linux (musl Alpine container)  
**Performance Goals**: p99 ≤ 2 s for clone/fetch/push on a 10 MB repository (SC-007)  
**Constraints**: No `git` binary in runtime container image; zero `Command::new("git")` in production source  
**Scale/Scope**: Single-service refactor — no new entities, no API surface changes

## Constitution Check

*GATE: Must pass before Phase 0 research. Re-check after Phase 1 design.*

| Principle                         | Status   | Notes                                                                         |
|-----------------------------------|----------|-------------------------------------------------------------------------------|
| I. Test-First                     | **PASS** | Integration tests (HTTP smart protocol) written before implementation tasks   |
| II. API-First                     | **PASS** | No service contract changes; HTTP endpoints keep same paths and Content-Types |
| III. Clear Contracts & Versioning | **PASS** | No breaking protocol changes; this is a drop-in replacement                   |
| IV. Observability                 | **PASS** | FR-012 requires structured log events per protocol phase                      |
| V. User Story Driven              | **PASS** | All work maps to US1–US4 in spec                                              |
| VI. Incremental Delivery          | **PASS** | US1/US2 (clone/push without binary) = P1 MVP; US3/US4 are P2/P3               |
| VII. Simplicity & YAGNI           | **PASS** | No new abstractions beyond `HttpPackServer`; no speculative features          |

## Project Structure

### Documentation (this feature)

```text
specs/008-remove-git-shellouts/
├── plan.md              # This file
├── research.md          # Phase 0 output
├── data-model.md        # Phase 1 output
├── quickstart.md        # Phase 1 output
└── tasks.md             # Phase 2 output (/speckit.tasks)
```

### Source Code (affected paths)

```text
gitstore-git-service/
├── Cargo.toml                          # Add gix-packetline, gix-pack, gix-protocol deps
│                                       # Add config field max_pack_size_bytes
├── src/
│   ├── config.rs                       # Add max_pack_size_bytes to GitConfig
│   ├── http_git_server.rs              # Replace all 4 Command::new("git") call sites
│   │                                   # with HttpPackServer methods
│   └── git/
│       └── pack_server.rs              # NEW: HttpPackServer implementation
│
└── tests/
    └── integration/
        ├── mod.rs                      # Existing gRPC tests (unchanged)
        └── http_smart_protocol.rs      # NEW: HTTP smart protocol integration tests

docker/
└── git-service.Dockerfile              # Remove git from runtime apk add
                                        # Remove stale /etc/gitconfig safe.directory write

scripts/
└── init-demo-catalog.sh                # DELETED

docs/
├── developer-guide.md                  # Remove init-demo-catalog.sh references
├── user-guide.md                       # Remove init-demo-catalog.sh references
└── docker-troubleshooting.md           # Remove init-demo-catalog.sh references (2 occurrences)
```

## Phase 0: Research Findings

All unknowns resolved — see [research.md](research.md).

Key findings:

1. **gix 0.83 has no server-side upload-pack API**: advertisement and stateless-rpc must be hand-assembled from `gix-packetline` (pkt-line framing) + `gix::Repository::references()` (ref enumeration) + `gix-pack` (pack object output). This is well-precedented in the gitoxide ecosystem.

2. **receive-pack atomicity** is already supported: `gix::refs::transaction` with `PreviousValue` guards provides the rollback-on-failure contract required by FR-011.

3. **Pack size limit** is enforced via Axum's `DefaultBodyLimit` middleware on the `POST /{repo}/git-receive-pack` route only. No gix API needed.

4. **Dockerfile**: `git` package removal also requires dropping the stale `[safe]\n\tdirectory = *` gitconfig write — that was for `libgit2` (Feature 007 already removed `git2`).

5. **CI grep gate**: `grep -rn 'Command::new("git")' gitstore-git-service/src/` run as a CI step is sufficient to enforce FR-010 automatically.

## Phase 1: Design

### HttpPackServer — Implementation Approach

`src/git/pack_server.rs` exposes:

```rust
pub struct HttpPackServer { repo_path: PathBuf, max_pack_size: u64 }

impl HttpPackServer {
    pub fn new(repo_path: PathBuf, max_pack_size: u64) -> Self;

    // Replaces: git upload-pack --advertise-refs
    pub fn advertise_upload_pack_refs(&self) -> Result<Vec<u8>>;

    // Replaces: git upload-pack --stateless-rpc
    pub fn handle_upload_pack(&self, body: &[u8]) -> Result<Vec<u8>>;

    // Replaces: git receive-pack --advertise-refs
    pub fn advertise_receive_pack_refs(&self) -> Result<Vec<u8>>;

    // Replaces: git receive-pack --stateless-rpc
    // Atomically writes pack + updates refs; rolls back on any failure
    pub fn handle_receive_pack(&self, body: &[u8]) -> Result<Vec<u8>>;
}
```

Each method:
- Opens the repo with `gix::open(&self.repo_path)`
- Emits a `tracing::info!` span with `repo`, `operation`, `duration_ms`, `outcome` (and `pack_size_bytes` for receive)
- Returns a `Result<Vec<u8>>` ready to be written as the HTTP response body

### `http_git_server.rs` Changes

- `info_refs` handler: replace both `Command::new("git")` branches with `HttpPackServer::advertise_upload_pack_refs()` / `advertise_receive_pack_refs()`
- `upload_pack` handler: replace `Command::new("git")` with `HttpPackServer::handle_upload_pack(&body_bytes)`
- `receive_pack` handler: replace `Command::new("git")` with `HttpPackServer::handle_receive_pack(&body_bytes)`; pack-size rejection (HTTP 413) is handled by Axum middleware before the handler runs
- Add `DefaultBodyLimit::max(state.config.max_pack_size)` to the receive-pack route

### Config Extension

Add to the existing `GitConfig` struct (in `src/config.rs`):

```rust
pub max_pack_size_bytes: u64,  // default: 52_428_800
```

Environment variable: `GITSTORE_GIT__MAX_PACK_SIZE_BYTES`

### Integration Tests (`tests/integration/http_smart_protocol.rs`)

Test cases (written first — red before green):

| Test                                  | Verifies             |
|---------------------------------------|----------------------|
| `clone_succeeds_without_git_binary`   | US1 / SC-001, SC-002 |
| `fetch_succeeds_without_git_binary`   | US1 / SC-002         |
| `push_succeeds_without_git_binary`    | US2 / SC-002         |
| `push_rejection_is_human_readable`    | FR-005, SC-005       |
| `push_over_size_limit_rejected_413`   | FR-013, SC-008       |
| `partial_write_rolls_back_atomically` | FR-011               |
| `upload_pack_on_nonexistent_repo_404` | Edge case from spec  |
| `clone_empty_repo_succeeds`           | Edge case from spec  |

### Dockerfile Changes

```diff
-RUN apk add --no-cache \
-    git \
-    ca-certificates \
-    libgcc
+RUN apk add --no-cache \
+    ca-certificates \
+    libgcc

-RUN printf '[safe]\n\tdirectory = *\n' > /etc/gitconfig && \
-    mkdir -p /data/repos
+RUN mkdir -p /data/repos
```

### Documentation Changes

- `docs/developer-guide.md`: remove the `./scripts/init-demo-catalog.sh` code block and surrounding instructions; replace with a one-line note that demo data seeding is provided via a future feature
- `docs/user-guide.md`: same treatment for the single reference at line 40
- `docs/docker-troubleshooting.md`: remove both references (lines 9 and 129) and any surrounding context that only makes sense with the script present
- `scripts/init-demo-catalog.sh`: deleted

### CI Gate

Add to the CI pipeline (after `cargo build`):

```bash
# FR-010: zero git shell-outs in production source
if grep -rn 'Command::new("git")' gitstore-git-service/src/; then
  echo "ERROR: git shell-out found in production source"
  exit 1
fi

# SC-006: no references to init-demo-catalog
if grep -rn 'init-demo-catalog' .; then
  echo "ERROR: init-demo-catalog reference found"
  exit 1
fi
```

## Complexity Tracking

No constitution violations. `HttpPackServer` is the only new abstraction — it is a direct replacement for four call sites and does not introduce layering beyond what the spec requires.
