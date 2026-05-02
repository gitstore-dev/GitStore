# Implementation Plan: OSS Alignment — Service Naming, Docs, CI, and Compose Separation

**Branch**: `003-oss-alignment` | **Date**: 2026-05-02 | **Spec**: [spec.md](spec.md)  
**Input**: Feature specification from `/specs/003-oss-alignment/spec.md`

## Summary

Rename the three service source folders to use the `gitstore-` prefix (`api → gitstore-api`, `git-server → gitstore-git-service`, `admin-ui → gitstore-admin`), making the core stack (`gitstore-api` + `gitstore-git-service`) structurally distinct from the optional add-on (`gitstore-admin`). Ripple the renames through all CI workflows, Dockerfiles, compose files, scripts, and documentation. Separate the compose file so the default `compose.yml` starts only the core stack and `compose.admin.yml` is an override for the add-on. Implement real integration tests (replacing TODO stubs) for the core stack. Audit core documentation to remove all admin references and create a dedicated `docs/admin/` section. Apply CI path-filtering exclusively to admin jobs; core CI jobs remain unconditional required status checks.

## Technical Context

**Language/Version**: Rust 1.75+ (`gitstore-git-service`), Go 1.21+ (`gitstore-api`), Node.js 18+ / Astro (`gitstore-admin`)  
**Primary Dependencies**: cargo (Rust toolchain), gqlgen (Go GraphQL), Playwright (admin E2E), Docker Compose v2  
**Storage**: Git bare repositories on disk (canonical), optional Redis/KV for read layer  
**Testing**: `cargo test` (Rust unit), `go test` (Go unit + integration), Playwright (admin E2E)  
**Target Platform**: Linux (CI/CD), macOS (local dev), Docker containers  
**Project Type**: Multi-service OSS platform (Rust service + Go service + optional TypeScript UI)  
**Performance Goals**: No change — this is a structural/OSS hygiene feature  
**Constraints**: All existing service behaviour and external-facing interfaces must remain unchanged after rename  
**Scale/Scope**: Repository-level restructuring; three service folders, two CI workflow files, three Dockerfiles, one compose file, integration test suite, documentation

## Constitution Check

*GATE: Must pass before Phase 0 research. Re-check after Phase 1 design.*

| Principle                         | Status | Notes                                                                                                                                                                   |
|-----------------------------------|--------|-------------------------------------------------------------------------------------------------------------------------------------------------------------------------|
| I. Test-First                     | PASS   | Integration tests are written before the compose/CI wiring is considered "done"; test stubs in CI replaced with real test code. Admin E2E moved alongside admin source. |
| II. API-First                     | PASS   | No service interfaces change; contracts are unchanged.                                                                                                                  |
| III. Clear Contracts & Versioning | PASS   | No version bumps required; structural rename only. Dockerfiles and compose images updated to new names.                                                                 |
| IV. Observability                 | PASS   | No regression in logging/tracing; `request_tracing.spec.ts` moved to `gitstore-admin/` where it belongs.                                                                |
| V. User Story Driven              | PASS   | All work maps to US1–US5 in the spec.                                                                                                                                   |
| VI. Incremental Delivery          | PASS   | P1 stories (rename, compose split, integration tests) are independently deployable before P2 (CI path-filter, docs).                                                    |
| VII. Simplicity & YAGNI           | PASS   | No new abstractions; only moving/renaming existing artefacts and filling in TODO stubs.                                                                                 |

**Post-design re-check**: No violations introduced in Phase 1 design. Complexity tracking section not required.

## Project Structure

### Documentation (this feature)

```text
specs/003-oss-alignment/
├── plan.md              # This file
├── research.md          # Phase 0 output
├── data-model.md        # Phase 1 output
├── quickstart.md        # Phase 1 output
├── contracts/           # Phase 1 output
│   └── integration-test-contract.md
└── tasks.md             # Phase 2 output (/speckit.tasks — NOT created here)
```

### Source Code (repository root — before and after)

```text
# BEFORE (current state)
api/                    ← Go GraphQL API
git-server/             ← Rust Git service
admin-ui/               ← Astro/React admin
tests/
  e2e/
    request_tracing.spec.ts
    docker-test.sh
compose.yml             ← all three services
.github/workflows/
  ci.yml                ← no path filters; integration tests are TODO stubs
  cd.yml                ← all three images built unconditionally
docker/
  api.Dockerfile
  git-service.Dockerfile
  admin.Dockerfile
docs/
  architecture.md       ← admin-ui in Implementation Baseline + diagrams
  developer-guide.md    ← admin-ui folder refs, gitstore-admin in expected output
  user-guide.md         ← "Using the Admin" section
  admin.md              ← 1-line stub

# AFTER (target state)
gitstore-api/           ← Go GraphQL API (renamed)
gitstore-git-service/   ← Rust Git service (renamed)
gitstore-admin/         ← Astro/React admin (renamed)
  tests/
    e2e/
      request_tracing.spec.ts   ← moved from tests/e2e/
      docker-test.sh            ← moved from tests/e2e/
tests/
  integration/
    api_git_integration_test.go ← NEW: core stack integration tests
compose.yml             ← core stack only (gitstore-api + gitstore-git-service)
compose.admin.yml       ← override: adds gitstore-admin service
.github/workflows/
  ci.yml                ← core jobs unconditional; admin-test job path-filtered
  cd.yml                ← admin image build path-filtered
docker/
  api.Dockerfile        ← COPY paths updated to gitstore-api/
  git-service.Dockerfile ← COPY paths updated to gitstore-git-service/
  admin.Dockerfile      ← COPY paths updated to gitstore-admin/
docs/
  architecture.md       ← Implementation Baseline updated; admin removed from diagrams
  developer-guide.md    ← folder refs updated; admin section → "see docs/admin/"
  user-guide.md         ← "Using the Admin" section removed → pointer to docs/admin/
  admin/
    overview.md         ← NEW: admin service overview
    architecture.md     ← NEW: architecture diagram including gitstore-admin
    quickstart.md       ← NEW: compose.admin.yml setup guide
```

**Structure Decision**: Multi-service repository layout. The three renamed service folders are the only structural change at source level. Tests are split: Go integration tests under root `tests/integration/` (core stack, CI-required), admin E2E tests moved inside `gitstore-admin/tests/e2e/` (admin-scoped, path-filtered). Compose files split into base + override. Documentation split into core docs (no admin) + `docs/admin/` (admin-only).

## Phase 0 — Research Summary

See [research.md](research.md) for full findings. Key decisions:

- **Folder rename**: `git mv` in a single structural commit, preserving history (`--follow` compatible).
- **Integration tests**: Go tests in `tests/integration/`, cover four core interaction scenarios (valid push → WebSocket, tag push → GraphQL data, invalid push → rejection, health checks).
- **CI path filtering**: Core jobs have NO `paths` filter (always run, required status checks). Only `admin-test` CI job and `build-admin-image` CD job carry `paths: ['gitstore-admin/**']`.
- **compose.admin.yml**: Docker Compose override file (two-file invocation). Does not redefine networks, volumes, or core services.
- **Admin docs**: `docs/admin/` directory with `overview.md`, `architecture.md`, `quickstart.md`.

## Phase 1 — Design & Contracts

### Data Model

This feature has no data model changes — no new entities, schemas, or database tables. The only "data" artefact is the integration test contract (what the tests verify at the service boundary). See [data-model.md](data-model.md) for the test boundary definitions.

### Interface Contracts

See [contracts/integration-test-contract.md](contracts/integration-test-contract.md) for the contract specification governing what the core-stack integration tests must verify.

No changes to the GraphQL schema, gRPC interfaces, or WebSocket protocol are introduced by this feature.

### CI/CD Contract Changes

| Job                      | Trigger change                                                        |
|--------------------------|-----------------------------------------------------------------------|
| `rust-test`              | No change — always runs                                               |
| `go-test`                | No change — always runs                                               |
| `integration-test`       | Test placeholder replaced with real `go test ./tests/integration/...` |
| `security-scan`          | No change                                                             |
| `build-status`           | No change                                                             |
| `admin-test` *(new job)* | Path-filtered to `gitstore-admin/**`                                  |
| `build-admin-image`      | Add path filter `gitstore-admin/**` to CD workflow                    |

### Compose Contract Changes

| File                | Contents                                                                        |
|---------------------|---------------------------------------------------------------------------------|
| `compose.yml`       | `git-service` + `api` services, shared network + volume. Admin service removed. |
| `compose.admin.yml` | `admin` service only. References shared network from base file.                 |

### Working-directory updates (CI)

| Job                  | Old path                    | New path                              |
|----------------------|-----------------------------|---------------------------------------|
| `rust-test`          | `./git-server`              | `./gitstore-git-service`              |
| `go-test`            | `./api`                     | `./gitstore-api`                      |
| Cargo cache key      | `git-server/target/`        | `gitstore-git-service/target/`        |
| Go cache             | `api/go.sum`                | `gitstore-api/go.sum`                 |
| Rust coverage upload | `./git-server/coverage/...` | `./gitstore-git-service/coverage/...` |
| Go coverage upload   | `./api/coverage.txt`        | `./gitstore-api/coverage.txt`         |

### Dockerfile COPY path updates

| Dockerfile               | Old source path | New source path         |
|--------------------------|-----------------|-------------------------|
| `git-service.Dockerfile` | `git-server/`   | `gitstore-git-service/` |
| `api.Dockerfile`         | `api/`          | `gitstore-api/`         |
| `admin.Dockerfile`       | `admin-ui/`     | `gitstore-admin/`       |

### Documentation change map

| File                      | Change                                                                                                                                                                                                                                               |
|---------------------------|------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------|
| `README.md`               | Architecture diagram: remove AdminUI node and its edges. Components list: remove Admin UI bullet. Build-from-source: remove Admin UI section. Quick Start expected output: remove `gitstore-admin` row. Add "Admin add-on: see docs/admin/" callout. |
| `docs/architecture.md`    | Implementation Baseline section: update folder names (`api/` → `gitstore-api/`, `git-server/` → `gitstore-git-service/`, remove `admin-ui/`). Proposal diagrams: remove Admin UI node from both Proposal 1 and Proposal 2 diagrams.                  |
| `docs/developer-guide.md` | Expected output block: remove `gitstore-admin` row. All `cd api/`, `cd git-server/` commands → updated paths. Admin UI section → single pointer to `docs/admin/`.                                                                                    |
| `docs/user-guide.md`      | "Using the Admin" section → replace content with pointer to `docs/admin/quickstart.md`.                                                                                                                                                              |
| `docs/admin/`             | Create new directory with `overview.md`, `architecture.md`, `quickstart.md`.                                                                                                                                                                         |
