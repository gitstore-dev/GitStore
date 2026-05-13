# Tasks: Remove Git Binary Shell-Outs and Init Script

**Input**: Design documents from `specs/008-remove-git-shellouts/`  
**Prerequisites**: plan.md ✅, spec.md ✅, research.md ✅, data-model.md ✅, quickstart.md ✅

**Tests**: Test-First Development (Constitution Principle I — NON-NEGOTIABLE). Tests MUST be written before implementation and verified to FAIL first.

**Organization**: Tasks are grouped by user story to enable independent implementation and testing of each story.

## Format: `[ID] [P?] [Story] Description`

- **[P]**: Can run in parallel (different files, no shared dependencies)
- **[Story]**: Which user story this task belongs to (US1–US4)

---

## Phase 1: Setup (Shared Infrastructure)

**Purpose**: Add new dependencies and configuration fields that all user stories require.

- [x] T001 [P] Add `gix-packetline`, `gix-pack`, and `gix-protocol` sub-crate dependencies to `gitstore-git-service/Cargo.toml` (verify compatible versions with gix 0.83.0 via `cargo tree`)
- [x] T002 [P] Add `max_pack_size_bytes: u64` field (default `52_428_800`) to the `GitConfig` struct in `gitstore-git-service/src/config.rs`; map from env var `GITSTORE_GIT__MAX_PACK_SIZE_BYTES`

**Checkpoint**: `cargo build` compiles cleanly with new deps and config field present.

---

## Phase 2: Foundational (Blocking Prerequisites)

**Purpose**: Stub out `HttpPackServer` and wire it into the module tree so integration tests can import it. Must complete before any user story test or implementation begins.

**⚠️ CRITICAL**: No user story work can begin until this phase is complete.

- [x] T003 Create `HttpPackServer` stub with all four method signatures returning `Err(anyhow::anyhow!("not yet implemented"))` in `gitstore-git-service/src/git/pack_server.rs` (struct fields: `repo_path: PathBuf`, `max_pack_size: u64`; impl block with `new`, `advertise_upload_pack_refs`, `handle_upload_pack`, `advertise_receive_pack_refs`, `handle_receive_pack`)
- [x] T004 Expose `pack_server` module from `gitstore-git-service/src/git/mod.rs` with `pub mod pack_server;`
- [x] T005 Thread `max_pack_size_bytes` from `GitServerState` config into the routes setup in `gitstore-git-service/src/http_git_server.rs` so handlers can construct `HttpPackServer` instances (compile-only change; no behavior change yet)

**Checkpoint**: `cargo build` passes; existing gRPC integration tests still pass (`cargo test --test integration`).

---

## Phase 3: User Story 1 — Git Clone/Fetch Without Binary (Priority: P1) 🎯 MVP

**Goal**: All HTTP smart-protocol clone and fetch operations handled in-process; no `git upload-pack` binary invoked.

**Independent Test**: `cargo test --test integration http_smart_protocol::clone` and `cargo test --test integration http_smart_protocol::fetch` pass against a server with no `git` binary on PATH.

### Tests for User Story 1 ⚠️ WRITE FIRST — verify these FAIL before implementing

- [x] T006 [US1] Write failing integration tests in `gitstore-git-service/tests/integration/http_smart_protocol.rs`: `clone_succeeds_without_git_binary`, `fetch_succeeds_without_git_binary`, `clone_empty_repo_succeeds`, `upload_pack_on_nonexistent_repo_404` — each test spins up an Axum test server via `axum::serve` against a `tempfile::TempDir` bare repo and drives HTTP smart-protocol requests; verify all four tests FAIL (stub returns error)

### Implementation for User Story 1

- [x] T007 [US1] Implement `advertise_upload_pack_refs(&self) -> Result<Vec<u8>>` in `gitstore-git-service/src/git/pack_server.rs`: open repo with `gix::open`, enumerate refs with `repository.references()`, serialize with `gix-packetline` pkt-line framing, prefix with `001e# service=git-upload-pack\n0000`; emit `tracing::info!` span with fields `repo`, `operation = "upload-pack-advertise"`, `duration_ms`, `outcome`
- [x] T008 [US1] Implement `handle_upload_pack(&self, body: &[u8]) -> Result<Vec<u8>>` in `gitstore-git-service/src/git/pack_server.rs`: parse stateless-rpc request body via `gix-packetline`, run pack-negotiation using `gix-pack` object output, return pack stream; emit `tracing::info!` span with fields `repo`, `operation = "upload-pack-rpc"`, `duration_ms`, `outcome`
- [x] T009 [US1] Replace `git upload-pack --advertise-refs` shell-out in the `"git-upload-pack"` branch of `info_refs` in `gitstore-git-service/src/http_git_server.rs` with a call to `HttpPackServer::new(repo_path, max_pack_size).advertise_upload_pack_refs()`
- [x] T010 [US1] Replace `git upload-pack --stateless-rpc` shell-out in the `upload_pack` handler in `gitstore-git-service/src/http_git_server.rs` with a call to `HttpPackServer::new(repo_path, max_pack_size).handle_upload_pack(&body_bytes)`

**Checkpoint**: All four US1 tests pass. `grep -n 'Command::new("git")' gitstore-git-service/src/http_git_server.rs` shows at most 2 remaining matches (the receive-pack ones).

---

## Phase 4: User Story 2 — Git Push Without Binary (Priority: P1)

**Goal**: All HTTP smart-protocol push operations handled in-process with atomic rollback on failure; pack-size limit enforced before any write; no `git receive-pack` binary invoked.

**Independent Test**: `cargo test --test integration http_smart_protocol::push` and `cargo test --test integration http_smart_protocol::receive` pass against a server with no `git` binary on PATH.

### Tests for User Story 2 ⚠️ WRITE FIRST — verify these FAIL before implementing

- [x] T011 [US2] Add failing integration tests to `gitstore-git-service/tests/integration/http_smart_protocol.rs`: `push_succeeds_without_git_binary`, `push_rejection_is_human_readable` (non-fast-forward push returns readable error text), `push_over_size_limit_rejected_413` (body > `max_pack_size_bytes` returns HTTP 413), `partial_write_rolls_back_atomically` (simulated mid-pack abort leaves repository unchanged); verify all four FAIL

### Implementation for User Story 2

- [x] T012 [US2] Implement `advertise_receive_pack_refs(&self) -> Result<Vec<u8>>` in `gitstore-git-service/src/git/pack_server.rs`: open repo with `gix::open`, enumerate refs, serialize with pkt-line framing, prefix with `001f# service=git-receive-pack\n0000`; emit `tracing::info!` span with `repo`, `operation = "receive-pack-advertise"`, `duration_ms`, `outcome`
- [x] T013 [US2] Implement `handle_receive_pack(&self, body: &[u8]) -> Result<Vec<u8>>` in `gitstore-git-service/src/git/pack_server.rs`: parse pkt-line ref-update commands from `body`, write pack objects to the object store via `gix-pack`, commit all ref updates atomically via `gix::refs::transaction` with `PreviousValue` guards (fast-forward check); on any failure roll back the transaction and return a pkt-line error-band response; emit `tracing::info!` span with `repo`, `operation = "receive-pack-rpc"`, `duration_ms`, `pack_size_bytes`, `outcome`
- [x] T014 [US2] Add `axum::extract::DefaultBodyLimit::max(state.config.max_pack_size_bytes as usize)` layer to only the `POST /{repo}/git-receive-pack` route in `gitstore-git-service/src/http_git_server.rs` so oversized pushes are rejected with HTTP 413 before the handler runs
- [x] T015 [US2] Replace `git receive-pack --advertise-refs` shell-out in the `"git-receive-pack"` branch of `info_refs` in `gitstore-git-service/src/http_git_server.rs` with a call to `HttpPackServer::new(repo_path, max_pack_size).advertise_receive_pack_refs()`
- [x] T016 [US2] Replace `git receive-pack --stateless-rpc` shell-out in the `receive_pack` handler in `gitstore-git-service/src/http_git_server.rs` with a call to `HttpPackServer::new(repo_path, max_pack_size).handle_receive_pack(&body_bytes)`; propagate human-readable error text from `GitError::ValidationFailed` unchanged

**Checkpoint**: All US2 tests pass. `grep -rn 'Command::new("git")' gitstore-git-service/src/` returns zero matches (FR-010).

---

## Phase 5: User Story 3 — Slimmer Container Image (Priority: P2)

**Goal**: The git-service Docker image builds and runs without the `git` binary installed.

**Independent Test**: `docker build -f docker/git-service.Dockerfile -t gitstore-git-service:local . && docker run --rm gitstore-git-service:local which git 2>&1 || echo "absent"` — must print `absent`.

### Implementation for User Story 3

- [x] T017 [US3] Remove `git \` from the `apk add --no-cache` call in the runtime stage of `docker/git-service.Dockerfile`; also remove the stale `RUN printf '[safe]\n\tdirectory = *\n' > /etc/gitconfig` line (was required by `libgit2`/`git2` crate — Feature 007 removed that dependency; gix does not enforce `safe.directory`); update the inline comment that referenced libgit2

**Checkpoint**: `docker build` succeeds; `docker run --rm gitstore-git-service:local which git` exits non-zero; `docker run --rm gitstore-git-service:local /app/git-service --help` or health-check confirms binary starts.

---

## Phase 6: User Story 4 — Remove Demo Init Script and Docs (Priority: P3)

**Goal**: `scripts/init-demo-catalog.sh` is deleted and no documentation file references it.

**Independent Test**: `grep -rn 'init-demo-catalog' .` from repo root returns zero matches.

### Implementation for User Story 4

- [x] T018 [P] [US4] Delete `scripts/init-demo-catalog.sh` from the repository
- [x] T019 [P] [US4] Remove the `./scripts/init-demo-catalog.sh` code block and all surrounding instructions from `docs/developer-guide.md`; replace with a one-line note: "Demo data seeding will be provided in a future feature."
- [x] T020 [P] [US4] Remove the `./scripts/init-demo-catalog.sh` instruction and surrounding context from `docs/user-guide.md` (line 40 area); replace with equivalent one-line note
- [x] T021 [P] [US4] Remove both occurrences of `./scripts/init-demo-catalog.sh` and their surrounding context from `docs/docker-troubleshooting.md` (lines 9 and 129 area); replace with equivalent one-line note per location

**Checkpoint**: `grep -rn 'init-demo-catalog' .` from repo root returns zero matches (SC-006).

---

## Phase 7: Polish & Cross-Cutting Concerns

**Purpose**: CI gates, formatting, license headers, and end-to-end validation.

- [x] T022 Add two CI grep gates to the existing CI pipeline configuration: (1) `grep -rn 'Command::new("git")' gitstore-git-service/src/` fails the build if any match found; (2) `grep -rn 'init-demo-catalog' .` fails the build if any match found
- [x] T023 [P] Run `cargo fmt --all -- --check` and `cargo clippy --all-targets --all-features -- -D warnings` in `gitstore-git-service/`; fix any issues reported
- [x] T024 [P] Run `./scripts/check-rust-license-headers.sh --diff-base origin/main` from repo root; add SPDX/copyright headers to any new `.rs` files (`pack_server.rs`, `http_smart_protocol.rs`) that are missing them
- [x] T025 Run the container image validation steps from `specs/008-remove-git-shellouts/quickstart.md`: build the image, confirm `git` binary absent, confirm service starts and `/health` returns `{"status":"healthy"}`
- [x] T026 Run `cargo test --verbose` for the full test suite and confirm all existing gRPC integration tests and new HTTP smart-protocol tests pass; confirm `cargo test` exit code is 0

---

## Dependencies & Execution Order

### Phase Dependencies

- **Setup (Phase 1)**: No dependencies — T001 and T002 can start immediately in parallel
- **Foundational (Phase 2)**: Depends on Phase 1 completion — T003 requires T001 (new crate deps); T004 requires T003; T005 requires T002 and T004
- **US1 (Phase 3)**: Depends on Phase 2 — T006 (tests) first, then T007–T010
- **US2 (Phase 4)**: Depends on Phase 2 — T011 (tests) first, then T012–T016; can run in parallel with US1 if separately staffed
- **US3 (Phase 5)**: Depends on Phase 2 only — independent of US1/US2, can run in parallel
- **US4 (Phase 6)**: No code dependencies — can start any time after Phase 1; T018–T021 all parallel
- **Polish (Phase 7)**: Depends on all desired user story phases being complete

### User Story Dependencies

- **US1 (P1)**: After Foundational — no dependency on US2/US3/US4
- **US2 (P1)**: After Foundational — no dependency on US1/US3/US4 (both P1 stories are independent)
- **US3 (P2)**: After Foundational — independent of US1/US2/US4
- **US4 (P3)**: After Phase 1 only — fully independent of all other stories

### Within Each User Story

1. Tests MUST be written and verified FAIL before any implementation task begins
2. `HttpPackServer` method implementation before handler wiring (`http_git_server.rs` changes)
3. Pack-size middleware (T014) before receive-pack handler replacement (T015, T016)

### Parallel Opportunities

- T001 ∥ T002 (different files, Phase 1)
- T007 and T008 can be developed in parallel on separate branches (different method bodies in same file — sequential if solo)
- T012 and T013 same as above
- T018 ∥ T019 ∥ T020 ∥ T021 (all different files, Phase 6)
- T023 ∥ T024 (different tools, Phase 7)

---

## Parallel Example: User Story 1

```bash
# Step 1 — write tests first, confirm they fail:
cargo test --test integration http_smart_protocol::clone_succeeds_without_git_binary
# Expected: FAILED (stub returns error)

# Step 2 — implement both upload-pack methods in parallel (separate branches):
# Developer A: advertise_upload_pack_refs() → T007
# Developer B: handle_upload_pack()         → T008

# Step 3 — wire into handlers after both methods pass their tests:
# T009 (info_refs upload-pack branch)
# T010 (upload_pack handler)
```

## Parallel Example: User Story 4

```bash
# All four tasks in parallel (all different files):
# T018: git rm scripts/init-demo-catalog.sh
# T019: edit docs/developer-guide.md
# T020: edit docs/user-guide.md
# T021: edit docs/docker-troubleshooting.md
```

---

## Implementation Strategy

### MVP First (User Stories 1 + 2 — both P1)

1. Complete Phase 1: Setup (T001, T002)
2. Complete Phase 2: Foundational (T003–T005)
3. Complete Phase 3: US1 clone/fetch (T006–T010)
4. Complete Phase 4: US2 push (T011–T016)
5. **STOP and VALIDATE**: Run full test suite; verify zero `Command::new("git")` matches
6. Ship P1 increment

### Incremental Delivery

1. Setup + Foundational → compile gate
2. US1 → clone/fetch work without binary (MVP for read-only clients)
3. US2 → push works without binary (full read-write MVP)
4. US3 → Dockerfile slimmed; image size drops ~30 MB
5. US4 → docs cleaned; no stale references remain
6. Polish → CI gates locked in; image validation confirmed

---

## Notes

- `[P]` tasks touch different files and have no outstanding dependencies — safe to run in parallel
- Constitution Principle I is enforced: every implementation phase is preceded by a test task that must fail first
- US1 and US2 are both P1 — order is suggested (fetch before push) but they can be done in parallel by two developers
- US4 (doc removal) has zero code dependencies and can be merged as a standalone PR at any time
- After T016, run `grep -rn 'Command::new("git")' gitstore-git-service/src/` — must return empty
- After T021, run `grep -rn 'init-demo-catalog' .` from repo root — must return empty
