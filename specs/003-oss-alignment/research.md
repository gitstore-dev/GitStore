# Research: OSS Alignment

**Feature**: 003-oss-alignment  
**Date**: 2026-05-02  
**Status**: Complete — all unknowns resolved

---

## R-001: Folder Rename Strategy

**Decision**: Use `git mv` for each service folder rename in a single commit per rename, preserving history.

**Rationale**: `git mv` preserves file history in `git log --follow`. Three renames (`api → gitstore-api`, `git-server → gitstore-git-service`, `admin-ui → gitstore-admin`) are independent and can be batched into a single structural commit without loss of history traceability.

**Alternatives considered**:
- Delete + re-create (loses history — rejected)
- In-place rename via filesystem `mv` (same outcome as `git mv`, but less explicit about git tracking — no advantage)

---

## R-002: Integration Test Location and Language

**Decision**: New integration tests live at the repository root under `tests/integration/` and are written in Go.

**Rationale**:
- The GraphQL API (`gitstore-api`) is Go, and its `net/http` and WebSocket client packages are sufficient to exercise cross-service interactions without browser automation.
- Placing them under a root-level `tests/integration/` directory (separate from `tests/e2e/`) makes the purpose clear and mirrors the existing `tests/e2e/` pattern.
- The existing `tests/e2e/request_tracing.spec.ts` (Playwright) tests flows that originate from the admin UI — it belongs to the admin layer, not the core integration suite.
- Go integration tests can run in CI using the core stack (`compose.yml`) only; `gitstore-admin` is not needed.

**What the integration tests must cover** (minimum viable set):
1. Push a valid catalogue commit to `gitstore-git-service` → verify WebSocket notification is broadcast.
2. Push a release tag to `gitstore-git-service` → poll `gitstore-api` GraphQL and verify catalogue data reflects the tag.
3. Push an invalid commit (bad front-matter) → verify push is rejected with a structured error.
4. Query `gitstore-api` GraphQL `/health` → verify both service health checks report healthy.

**Alternatives considered**:
- Shell scripts (`docker-test.sh` pattern already in `tests/e2e/`) — lack structured reporting and type safety; not suitable as required CI status checks.
- Rust integration tests — `gitstore-git-service` Rust tests can cover internal logic; cross-service boundary tests need a client perspective, which Go provides naturally.

---

## R-003: CI Path-Filter Strategy for Admin

**Decision**: Core CI jobs (`rust-test`, `go-test`, `integration-test`, `security-scan`, `build-status`) carry NO `paths` filter and run on every PR. The admin-specific job (`admin-test`) carries `paths: ['gitstore-admin/**']` and only runs when admin files change.

**Rationale**:
- Core jobs are required status checks for branch protection. GitHub Actions path filters would skip them on admin-only PRs, breaking the branch protection guarantee. Removing path filters from core jobs ensures they always run and always gate merge.
- Admin jobs are not required status checks; they are informational. Path-filtering them prevents unnecessary build minutes on PRs that never touch admin files.
- This is the standard GitHub Actions pattern for mono-repos with optional components.

**Alternatives considered**:
- Path-filter core jobs too (skip on admin-only PRs) — rejected because FR-011 explicitly requires core CI to always run as a branch protection requirement.
- Separate workflow files per service — valid but increases workflow maintenance surface area without adding value for a small number of services.

**Applied to CD as well**: `build-admin-image` job in `cd.yml` should also use a path filter so admin images are only rebuilt and pushed when admin files change.

---

## R-004: compose.admin.yml Override Pattern

**Decision**: `compose.admin.yml` is a Docker Compose override file, not a standalone file. The full-stack command is `docker compose -f compose.yml -f compose.admin.yml up -d`.

**Rationale**: Standard Docker Compose overlay pattern. The admin service depends on `gitstore-api` which is already defined in the base `compose.yml`. The override file can reference the existing `gitstore-network` network without redefining it. Operators who only want the core stack use `docker compose up -d`; those who want the admin add-on use the two-file form.

**What `compose.admin.yml` must contain**:
- The `admin` service definition (build, image, ports, environment, depends_on, health check, network).
- No redefinition of networks, volumes, or core services.

**Alternatives considered**:
- Standalone `compose.admin.yml` (self-contained) — requires duplicating network/volume definitions and core service health deps; fragile and error-prone.
- Docker profiles (`--profile admin`) — cleaner UX, but requires Docker Compose 1.28+ and changes the mental model; override files are universally understood and compatible.

---

## R-005: Documentation Structure for Admin

**Decision**: Create `docs/admin/` directory. Move/expand `docs/admin.md` (currently 1 line) into `docs/admin/index.md` (or `docs/admin/overview.md`). Core docs (`README.md`, `docs/developer-guide.md`, `docs/user-guide.md`, `docs/architecture.md`) get admin references stripped and replaced with a single "see also: docs/admin/" pointer.

**Rationale**:
- A dedicated `docs/admin/` directory scales naturally (can hold multiple pages: overview, architecture, API reference for the admin surface, troubleshooting).
- Keeping it under `docs/` keeps the repo layout simple; no new top-level directories needed.

**Admin documentation must include**:
1. `docs/admin/overview.md` — service overview, prerequisites, what it adds to the core stack.
2. `docs/admin/architecture.md` — architecture diagram showing `gitstore-admin` ↔ `gitstore-api` ↔ `gitstore-git-service`.
3. `docs/admin/quickstart.md` — setup using `compose.admin.yml`, how to access the admin UI, how to publish from the UI.

**Alternatives considered**:
- Single flat `docs/admin.md` — simpler but doesn't scale as admin docs grow; also the file already exists and is empty, so creating a directory is a clean upgrade.
- Separate documentation site — unnecessary complexity for the scope of this feature.

---

## R-006: References to Update Across Repository

**Current old names → new names mapping**:

| Old name                                | New name                                        | Where it appears                                                                                                                                                                                                            |
|-----------------------------------------|-------------------------------------------------|-----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------|
| `api/`                                  | `gitstore-api/`                                 | CI `working-directory`, CD `file:` paths, Dockerfiles `COPY`, compose `context/dockerfile`, scripts, docs                                                                                                                   |
| `git-server/`                           | `gitstore-git-service/`                         | CI `working-directory`, CD `file:` paths, Dockerfiles `COPY`, compose `context/dockerfile`, scripts, docs, CI cache key paths                                                                                               |
| `admin-ui/`                             | `gitstore-admin/`                               | CD `file:` paths, Dockerfiles `COPY`, compose `context/dockerfile`, E2E test env vars, docs                                                                                                                                 |
| `git-service` (service name in compose) | Keep as `git-service` OR rename to match folder | Compose service name is separate from folder name; keep `git-service` for container naming consistency, or rename to `gitstore-git-service` for clarity — **decision: rename service names in compose to match image slug** |

**Files requiring updates** (comprehensive):
- `.github/workflows/ci.yml` — working-directory paths, cache paths
- `.github/workflows/cd.yml` — build context paths, image name env vars
- `docker/git-service.Dockerfile` — COPY source paths
- `docker/api.Dockerfile` — COPY source paths
- `docker/admin.Dockerfile` — COPY source paths
- `compose.yml` — build context/dockerfile paths, service names
- `README.md` — folder references, architecture diagram, component list, build-from-source section
- `docs/developer-guide.md` — working-directory instructions, expected output, all folder references
- `docs/user-guide.md` — "Using the Admin" section (move to admin docs; leave a pointer)
- `docs/architecture.md` — "Implementation Baseline" section folder references
- `AGENTS.md` — any folder references
- `scripts/` — any hardcoded folder references
- `tests/e2e/request_tracing.spec.ts` — env var names (ADMIN_UI_URL) — move this file to `gitstore-admin/tests/` since it's admin-specific
