# Tasks: OSS Alignment — Service Naming, Docs, CI, and Compose Separation

**Input**: Design documents from `/specs/003-oss-alignment/`  
**Prerequisites**: plan.md ✓, spec.md ✓, research.md ✓, data-model.md ✓, contracts/ ✓, quickstart.md ✓  
**Tracking**: GH#42 (future `release-please` / tag-only docker push)

**Tests**: Constitution Principle I — integration tests written before CI wiring is considered complete.

**Organization**: Tasks grouped by user story. P1 stories (rename, compose split, integration tests) are the MVP and can be validated independently before P2 work begins.

## Format: `[ID] [P?] [Story] Description`

- **[P]**: Can run in parallel (different files, no dependencies on incomplete siblings)
- **[Story]**: User story label (US1–US5)

---

## Phase 1: Setup (Shared Infrastructure)

**Purpose**: Structural rename of all three service folders and update of every reference. This is the foundation on which all other phases depend — nothing else can proceed until the old folder names are gone.

- [X] T001 Rename `api/` → `gitstore-api/` using `git mv api gitstore-api` at repository root
- [X] T002 Rename `git-server/` → `gitstore-git-service/` using `git mv git-server gitstore-git-service` at repository root
- [X] T003 Rename `admin-ui/` → `gitstore-admin/` using `git mv admin-ui gitstore-admin` at repository root
- [X] T004 [P] Update `docker/api.Dockerfile`: change `COPY api/go.mod api/go.sum` → `COPY gitstore-api/go.mod gitstore-api/go.sum` and `COPY api/ ./` → `COPY gitstore-api/ ./` in `docker/api.Dockerfile`
- [X] T005 [P] Update `docker/git-service.Dockerfile`: change `COPY git-server/Cargo.toml git-server/Cargo.lock*` → `COPY gitstore-git-service/Cargo.toml gitstore-git-service/Cargo.lock*` and `COPY git-server/src ./src` → `COPY gitstore-git-service/src ./src` in `docker/git-service.Dockerfile`
- [X] T006 [P] Update `docker/admin.Dockerfile`: change `COPY admin-ui/package.json admin-ui/package-lock.json*` → `COPY gitstore-admin/package.json gitstore-admin/package-lock.json*` and `COPY admin-ui/ ./` → `COPY gitstore-admin/ ./` in `docker/admin.Dockerfile`
- [X] T007 [P] Update `compose.yml`: change build `context/dockerfile` paths for `git-service` and `api` services to reference `gitstore-git-service/` and `gitstore-api/` source folders respectively; leave service names (`git-service`, `api`, `admin`) as-is (Docker Compose prepends `gitstore-` via project name to produce container names)
- [X] T008 [P] Update `.github/workflows/ci.yml`: change `working-directory: ./git-server` → `./gitstore-git-service`, `working-directory: ./api` → `./gitstore-api`; update Cargo cache paths from `git-server/target/` → `gitstore-git-service/target/` and `**/Cargo.lock` cache key; update Go cache from `api/go.sum` → `gitstore-api/go.sum`; update coverage upload paths
- [X] T009 [P] Update `.github/workflows/cd.yml`: change build `file:` paths for all three image jobs to use new folder names (`gitstore-api/`, `gitstore-git-service/`, `gitstore-admin/`); remove `continue-on-error: true` from `build-admin-image` job (admin is now a fully supported service); do NOT add path filters to any CD job — all images always build together for release consistency (GH#42)
- [X] T010 [P] Grep entire repository for remaining references to old folder names and fix any found in scripts, config files, or tooling: `grep -r '\bapi/\|git-server/\|admin-ui/' --include="*.yml" --include="*.yaml" --include="*.toml" --include="*.sh" --include="*.json" --include="*.md" .` (exclude `specs/` and `.git/`)
- [X] T011 Verify all services build successfully after rename: `cd gitstore-git-service && cargo build --verbose`, `cd ../gitstore-api && go build -v ./...`, `cd ../gitstore-admin && npm ci && npm run build`

**Checkpoint**: All three service folders renamed, all cross-references updated, builds pass. Git history preserved (`git log --follow gitstore-api/` works).

---

## Phase 2: Foundational (Blocking Prerequisites)

**Purpose**: Establish the integration test infrastructure and the `go.mod` module for `tests/integration/` before writing any test logic. This gates Phase 3 (integration tests) and Phase 4 (CI wiring).

**⚠️ CRITICAL**: No user story work in Phase 3+ can begin until this phase is complete.

- [X] T012 Create `tests/integration/` directory and initialize a Go module: `go mod init github.com/gitstore-dev/gitstore/tests/integration` in `tests/integration/`; add `go.sum` (empty initially); create `tests/integration/main_test.go` with `package integration` declaration and `TestMain` that reads `GIT_SERVER_URL`, `API_URL`, `GIT_SERVER_WS_URL` env vars (defaulting to localhost) and calls `m.Run()`
- [X] T013 Move `tests/e2e/request_tracing.spec.ts` and `tests/e2e/docker-test.sh` into `gitstore-admin/tests/e2e/` (create the directory); update any import paths or env var references as needed; this test belongs to the admin layer, not the core stack

**Checkpoint**: `tests/integration/` module exists, compiles cleanly. Admin E2E test lives inside `gitstore-admin/`.

---

## Phase 3: User Story 1 — Repository Structure Is Immediately Legible (Priority: P1) 🎯 MVP

**Goal**: A contributor cloning the repo sees `gitstore-api/`, `gitstore-git-service/`, `gitstore-admin/` and can immediately tell which are services, which is core, and which is add-on — without reading any documentation.

**Independent Test**: `ls` the repository root; verify no old-named folders exist (`api/`, `git-server/`, `admin-ui/`); verify the three new folders exist; verify `git log --follow gitstore-api/` shows history.

> **NOTE**: This phase's implementation is already delivered by Phase 1. The tasks below are verification and commit tasks.

- [X] T014 [US1] Verify `git log --follow gitstore-api/cmd/server/main.go` shows pre-rename history (confirms `git mv` preserved history); repeat for a file in `gitstore-git-service/` and `gitstore-admin/`
- [X] T015 [US1] Run `grep -r '\bapi/\|git-server/\|admin-ui/' . --include="*.go" --include="*.rs" --include="*.ts" --include="*.md" --include="*.yml" --exclude-dir=specs --exclude-dir=.git` and confirm zero matches; fix any that remain
- [X] T016 [US1] Commit the structural rename as a single git commit with message: `refactor: rename service folders to gitstore-* prefix (OSS alignment)`

**Checkpoint**: Repository root is clean. `ls` output instantly communicates structure. US1 independently testable.

---

## Phase 4: User Story 2 — Core Stack Runs Without Admin Add-On (Priority: P1)

**Goal**: `docker compose up -d` starts exactly `gitstore-git-service` and `gitstore-api`; no admin container. An operator using `compose.admin.yml` override gets all three.

**Independent Test**: Run `docker compose up -d && docker compose ps` — confirm exactly two application containers; run `docker compose -f compose.yml -f compose.admin.yml up -d && docker compose -f compose.yml -f compose.admin.yml ps` — confirm three containers.

- [X] T017 [US2] Remove the `admin` service block from `compose.yml`; ensure remaining `git-service` and `api` service definitions, the shared `gitstore-network`, and the `git-data` volume remain intact in `compose.yml`
- [X] T018 [US2] Create `compose.admin.yml` as a Docker Compose override file: define only the `admin` service (image, build context pointing to `gitstore-admin/`, ports `3000:3000`, `depends_on: api`, environment vars, healthcheck, `networks: - gitstore-network`); do NOT redefine `gitstore-network` or `git-data` volume
- [X] T019 [US2] Verify `docker compose config` (base only) renders without errors and contains no admin service; verify `docker compose -f compose.yml -f compose.admin.yml config` renders with the admin service merged in
- [X] T020 [US2] Commit: `chore: split compose.yml into core stack + compose.admin.yml override`

**Checkpoint**: `docker compose up -d` → 2 services. `docker compose -f compose.yml -f compose.admin.yml up -d` → 3 services. US2 independently testable.

---

## Phase 5: User Story 3 — Integration Tests Validate the Core Stack in CI (Priority: P1)

**Goal**: Real Go integration tests in `tests/integration/` replace the TODO stub in CI. Tests exercise the `gitstore-api` ↔ `gitstore-git-service` boundary and pass in a clean CI environment.

**Independent Test**: Run `docker compose up -d && go test -v -timeout 120s ./tests/integration/... && docker compose down -v`; all 5 contract assertions (C-001–C-005) pass.

> **Constitution Principle I**: Write tests first, verify they fail, then fix until green.

### Integration Tests (write first — verify FAIL before proceeding)

- [X] T021 [P] [US3] Write `tests/integration/health_test.go`: implement `TestHealthEndpoints` covering contract C-001 (HTTP GET both `/health` endpoints, assert 200 + `{"status":"healthy"}`); run against live stack — test should PASS if services are up, or be skipped with `t.Skip` when services are unreachable (test is environment-gated)
- [X] T022 [P] [US3] Write `tests/integration/websocket_test.go`: implement `TestValidPushEmitsWebSocketNotification` covering contract C-002 (connect WS client, push valid commit, assert notification within 5s with `repository`, `ref`, `commit_sha` fields); run — should FAIL initially if git push client not yet wired
- [X] T023 [P] [US3] Write `tests/integration/catalog_test.go`: implement `TestTagPushPublishesToGraphQL` covering contract C-003 (push release tag, poll GraphQL up to 10s, assert product SKU appears) and `TestInvalidPushIsRejected` covering C-004 (push bad front-matter, assert non-zero exit + validation message, assert SKU absent from GraphQL)
- [X] T024 [P] [US3] Write `tests/integration/websocket_health_test.go`: implement `TestWebSocketHealthEndpoint` covering contract C-005 (HTTP GET `/websocket/health`, assert 200, assert `active_connections` is a non-negative integer)

### Implementation — Git Push Client Helper

- [X] T025 [US3] Implement `tests/integration/githelper_test.go`: a test helper that uses `os/exec` to run `git` CLI commands against `GIT_SERVER_GIT_URL` (initializes a temp bare repo, commits a valid product markdown file, pushes to the service); reused by T022, T023
- [X] T026 [US3] Wire T021–T024 to use the helper from T025; run the full test suite locally against `docker compose up -d`; fix failures until all tests pass
- [X] T027 [US3] Update `.github/workflows/ci.yml` `integration-test` job: replace the `echo "Integration tests placeholder"` step with `go test -v -timeout 120s ./tests/integration/...`; ensure the job still uses `docker compose up -d --build` (core stack only, no `compose.admin.yml`)
- [X] T028 [US3] Commit: `test: implement core stack integration tests (replaces TODO stubs)`

**Checkpoint**: CI `integration-test` job runs real tests and passes on a clean environment. US3 independently verifiable.

---

## Phase 6: User Story 4 — Admin CI Is Path-Filtered; Core CI Always Runs (Priority: P2)

**Goal**: A new `admin-test` CI job runs only when files in `gitstore-admin/` change. Core CI jobs (`rust-test`, `go-test`, `integration-test`) have no path filter and always run as required status checks.

**Independent Test**: Open a PR touching only `gitstore-admin/package.json`; confirm `admin-test` appears in CI run and core jobs also run. Open a PR touching only `gitstore-api/`; confirm `admin-test` does NOT appear.

- [X] T029 [US4] Add a new `admin-test` job to `.github/workflows/ci.yml` with:
  - `name: Admin Tests`
  - `paths: ['gitstore-admin/**']` under `on.pull_request`
  - `defaults.run.working-directory: ./gitstore-admin`
  - Steps: `actions/checkout`, `actions/setup-node` (LTS), `npm ci`, `npm run build`, `npx playwright install --with-deps`, `npm run test:e2e` (or equivalent Playwright command)
  - This job is NOT added to `build-status` needs (it is informational, not a required check)
- [X] T030 [US4] Verify existing core CI jobs (`rust-test`, `go-test`, `integration-test`, `security-scan`, `build-status`) have NO `paths:` filter — confirm by reading `.github/workflows/ci.yml`; remove any accidentally added path filters
- [X] T031 [US4] Update `build-status` job in `ci.yml` to remain gated on `[rust-test, go-test, integration-test, security-scan]` only — `admin-test` must NOT be in `needs`
- [X] T032 [US4] Commit: `ci: add path-filtered admin-test job; core CI always runs unconditionally`

**Checkpoint**: CI path filtering correct. Core jobs always gatekeep merge. Admin job runs only when its files change. US4 independently testable via PR inspection.

---

## Phase 7: User Story 5 — Documentation Reflects Core/Add-On Separation (Priority: P2)

**Goal**: Core docs contain no admin references. `docs/admin/` is a self-contained guide covering add-on setup, architecture, and `compose.admin.yml`. Core architecture diagrams show only the two core services.

**Independent Test**: `grep -r "gitstore-admin\|admin-ui\|Admin UI" docs/ --include="*.md" --exclude-dir=admin` returns zero matches (excluding the admin subdirectory and "see also" pointers).

- [X] T033 [P] [US5] Update `README.md`:
  - Architecture diagram: remove `AdminUI` node and its two edges (`GraphQL API -- GraphQL --> AdminUI`, no other changes)
  - Components section: remove the **Admin UI** bullet
  - Quick Start expected output block: remove `gitstore-admin` row
  - Build-from-source section: remove the "Admin UI (Astro/React)" subsection entirely
  - Add a brief callout after Components: `> **Admin add-on**: For the optional web UI, see [docs/admin/](docs/admin/).`
  - Update `cd api` → `cd gitstore-api` and `cd git-server` → `cd gitstore-git-service` in build instructions
- [X] T034 [P] [US5] Update `docs/architecture.md`:
  - Implementation Baseline section: update folder paths (`api/` → `gitstore-api/`, `git-server/` → `gitstore-git-service/`); remove `admin-ui/` line; add note: "Admin add-on: see `docs/admin/architecture.md`"
  - Proposal 1 diagram: remove the Admin UI subgraph node (if present); leave all core service nodes
  - Proposal 2 diagram: same — remove Admin UI node if present
- [X] T035 [P] [US5] Update `docs/developer-guide.md`:
  - Expected output block: remove `gitstore-admin` row (core stack only)
  - All `cd api/` → `cd gitstore-api/`, all `cd git-server/` → `cd gitstore-git-service/`
  - Replace the "Admin UI" build-from-source section with: `> For the Admin add-on, see [docs/admin/quickstart.md](admin/quickstart.md).`
- [X] T036 [P] [US5] Update `docs/user-guide.md`:
  - Replace "Using the Admin" section content with: `> For Admin UI setup and usage, see [docs/admin/quickstart.md](admin/quickstart.md).`
  - Remove any other inline admin-UI instructions from the user guide body
- [X] T037 [US5] Create `docs/admin/overview.md`: service overview (what `gitstore-admin` is, what it adds to the core stack, prerequisites: Node.js 18+, running core stack); include a "when to use" section explaining it is optional
- [X] T038 [US5] Create `docs/admin/architecture.md`: architecture diagram (Mermaid) showing `gitstore-admin` → `gitstore-api` → `gitstore-git-service` with the full request flow; include the diagram in the existing "two proposals" context noting admin attaches to the API layer
- [X] T039 [US5] Create `docs/admin/quickstart.md`: step-by-step setup using `docker compose -f compose.yml -f compose.admin.yml up -d`; access at `http://localhost:3000`; how to create products and publish from the UI; troubleshooting (merge conflicts, stale data, validation errors) — migrate relevant content from `docs/user-guide.md` "Using the Admin" and `docs/user-guide.md` troubleshooting sections
- [X] T040 [US5] Remove (or replace with a redirect pointer) `docs/admin.md` stub (1-line file): `echo "See [docs/admin/overview.md](admin/overview.md) for the Admin add-on documentation." > docs/admin.md` or delete and add redirect — ensure no broken links in the repo
- [X] T041 [US5] Verify documentation audit: run `grep -r "admin-ui\|Admin UI\|gitstore-admin\|cd admin" docs/ --include="*.md" --exclude-dir=admin` — confirm zero matches (excluding pointer links); fix any remaining references
- [X] T042 [US5] Commit: `docs: separate admin docs into docs/admin/; remove admin from core docs`

**Checkpoint**: Core docs are admin-free. `docs/admin/` is self-contained. US5 independently testable.

---

## Phase 8: Polish & Cross-Cutting Concerns

**Purpose**: Final verification, cross-reference audit, and post-rename cleanup.

- [X] T043 [P] Update `AGENTS.md` (the canonical file — `CLAUDE.md` is a symlink to it): update any remaining `git-server/`, `api/`, `admin-ui/` folder references in the GitOps section to the new names
- [X] T044 [P] Update `docs/storefront.md` and `docs/api-reference.md` if they contain old folder references: run `grep -n "git-server\|/api/\|admin-ui" docs/storefront.md docs/api-reference.md` and fix
- [X] T045 [P] Verify `scripts/init-demo-catalog.sh` contains no hardcoded old folder names; update if found
- [X] T046 Run the full quickstart validation from `specs/003-oss-alignment/quickstart.md`: `docker compose up -d`, clone catalog, push product, push tag, query GraphQL — confirm end-to-end flow works with the renamed stack
- [X] T047 Run `docker compose -f compose.yml -f compose.admin.yml up -d` and confirm admin UI is accessible at `http://localhost:3000`; run `docker compose -f compose.yml -f compose.admin.yml down -v`
- [X] T048 Final grep audit: `grep -r '\bgit-server\b\|\badmin-ui\b\|working-directory: ./api\b' . --include="*.yml" --include="*.yaml" --include="*.md" --include="*.sh" --include="*.toml" --exclude-dir=specs --exclude-dir=.git` — must return zero matches; fix any found
- [X] T049 Commit all polish changes: `chore: post-rename reference cleanup and final audit`

---

## Dependencies & Execution Order

### Phase Dependencies

- **Phase 1 (Setup)**: No dependencies — start immediately
- **Phase 2 (Foundational)**: Depends on Phase 1 (renamed folders must exist before moving E2E tests)
- **Phase 3 (US1 — Structure)**: Depends on Phase 1 completion; verification tasks only
- **Phase 4 (US2 — Compose split)**: Depends on Phase 1 (needs new folder names in compose)
- **Phase 5 (US3 — Integration tests)**: Depends on Phase 2 (needs `tests/integration/` module)
- **Phase 6 (US4 — CI path filter)**: Depends on Phase 5 (admin-test job needs moved E2E tests to reference)
- **Phase 7 (US5 — Docs)**: Depends on Phase 1 (new folder names in docs), otherwise independent
- **Phase 8 (Polish)**: Depends on all phases complete

### User Story Dependencies

```
Phase 1 (rename) ──────────────────────────────────┐
Phase 2 (test infra) ───────────────────────────┐  │
                                                  │  │
US1 (structure legible): Phase 1 ─────────────── ✓  │
US2 (compose split):     Phase 1 ─────────────── ✓  │
US3 (integration tests): Phase 2 ──────────────── ✓ │
US4 (CI path filter):    Phase 5 ─────────────── ✓  │
US5 (docs):              Phase 1 (for folder names) ─┘
```

### P1 stories are the MVP — US1, US2, US3 can be validated and merged independently

### Parallel Opportunities

- Within Phase 1: T004, T005, T006 (three Dockerfiles), T007, T008, T009, T010 all touch different files — run in parallel after T001–T003 complete
- Within Phase 5: T021, T022, T023, T024 (four test files) can be written in parallel
- Within Phase 7: T033, T034, T035, T036 all touch different doc files — run in parallel

---

## Parallel Example: Phase 1

```
# After T001, T002, T003 complete:
Task T004: update docker/api.Dockerfile
Task T005: update docker/git-service.Dockerfile
Task T006: update docker/admin.Dockerfile
Task T007: update compose.yml
Task T008: update .github/workflows/ci.yml
Task T009: update .github/workflows/cd.yml
Task T010: grep audit for stragglers
# All six run in parallel — different files
```

## Parallel Example: Phase 5 (Test First)

```
# After Phase 2 complete:
Task T021: write tests/integration/health_test.go
Task T022: write tests/integration/websocket_test.go
Task T023: write tests/integration/catalog_test.go
Task T024: write tests/integration/websocket_health_test.go
# All four test files written in parallel — then T025 helper, then T026 wire-up
```

---

## Implementation Strategy

### MVP First (P1 Stories — US1, US2, US3)

1. Complete Phase 1: Rename + reference updates
2. Complete Phase 2: Test infrastructure
3. Complete Phase 3: Verify structure legibility (US1)
4. Complete Phase 4: Compose split (US2)
5. Complete Phase 5: Integration tests pass in CI (US3)
6. **STOP and VALIDATE**: Three P1 stories independently testable; merge to main

### Incremental Delivery

7. Add Phase 6: CI path-filter for admin (US4)
8. Add Phase 7: Documentation audit + `docs/admin/` (US5)
9. Add Phase 8: Polish + final audit

### Notes

- Each phase produces a standalone, committable increment
- `AGENTS.md` is the canonical dev-guidelines file; `CLAUDE.md` is a symlink — only edit `AGENTS.md`
- GH#42 tracks the `release-please` work that will change when docker push occurs (tag-only); do NOT implement that here
- Docker Compose service names (`git-service`, `api`, `admin`) are intentionally left unchanged; `container_name:` in compose already sets them to `gitstore-git-service`, `gitstore-api`, `gitstore-admin`
