# Implementation Plan: Pre-Receive Validation End-to-End

**Branch**: `020-pre-receive-validation-e2e` | **Date**: 2026-06-06 | **Spec**: [spec.md](spec.md)

## Summary

Wire the remaining CI gap so that the product-lifecycle integration tests pass reliably against
both the memdb and ScyllaDB backends. All changes land in a single file:
`.github/workflows/ci-integration.yml`.

**Root cause**: The existing `integration-test` job has no readiness check for port 6000 (the
`CatalogService` gRPC endpoint in `gitstore-api`). When the git service's `SchemaValidationHandler`
fires its first `ValidateResources` RPC during a test push and port 6000 is not yet accepting
connections, the RPC fails: valid pushes are falsely rejected (fail-closed) and the
`AdmitResources` fire-and-forget call never lands, so products are never stored.

**Fix surface**: Two CI workflow changes:
1. Add `until nc -z localhost 6000; do sleep 1; done` after the existing port-50051 readiness check.
2. Add a new `integration-test-scylla` job that replays the same suite using the
   `compose.scylla.yml` overlay.

No Rust, Go, or test code changes are required — both handlers already use `connect_lazy()`.

## Technical Context

**Language/Version**: GitHub Actions YAML + Go 1.25 (test runner)
**Primary Dependencies**: Docker Compose, `compose.scylla.yml` (already committed), ScyllaDB 5.4
**Storage**: memdb (default, no infra) and ScyllaDB 5.4 (via Docker overlay)
**Testing**: `go test ./...` inside `tests/integration/`
**Target Platform**: ubuntu-latest CI runner
**Project Type**: CI workflow fix
**Performance Goals**: Full integration suite completes in under 3 minutes
**Constraints**: No changes to service code, proto contracts, or test logic
**Scale/Scope**: 10 integration test cases (6 `TestProductLifecycle_*` + 4 `TestDocumentationExamples_*`)

## Constitution Check

| Principle | Status | Notes |
|-----------|--------|-------|
| I. Test-First | ✅ Pass | Tests already exist and are failing (red); this fix makes them green |
| II. API-First | ✅ Pass | No API contract changes |
| III. Clear Contracts | ✅ Pass | No versioning impact |
| IV. Observability | ✅ Pass | Existing structured logging unchanged |
| V. User Story Driven | ✅ Pass | All 3 user stories map to existing test cases |
| VI. Incremental Delivery | ✅ Pass | P1 (rejection gate) and P1 (admission) fixed together; P2 (docs examples) follows from the same fix |
| VII. Simplicity | ✅ Pass | Single-file change; no abstractions introduced |

## Project Structure

### Documentation (this feature)

```text
specs/020-pre-receive-validation-e2e/
├── plan.md              ← this file
├── research.md          ← R-001 through R-005 (Phase 0)
├── data-model.md        ← no new entities; context only (Phase 1)
├── quickstart.md        ← local run guide for both backends (Phase 1)
├── checklists/
│   └── requirements.md  ← spec quality checklist
└── tasks.md             ← Phase 2 output (/speckit.tasks)
```

### Source Code (repository root)

```text
.github/
└── workflows/
    └── ci-integration.yml    ← only file changed
```

No changes to:
- `gitstore-git-service/` (Rust)
- `gitstore-api/` (Go)
- `tests/integration/` (Go integration tests)
- `compose.yml`, `compose.scylla.yml`

## Phase 0: Research Summary

See [research.md](research.md) for full findings.

| ID | Decision |
|----|---------|
| R-001 | Add port-6000 readiness check to `integration-test` job — root cause confirmed |
| R-002 | Add `integration-test-scylla` job using `compose.scylla.yml` overlay |
| R-003 | No Rust changes needed — both handlers already use `connect_lazy()` |
| R-004 | No Go changes needed — datastore factory and RPC implementations are correct |
| R-005 | No test code changes needed — suite is backend-agnostic |

## Phase 1: Design

### integration-test job changes (memdb)

Add one step after "Wait for git-service gRPC to be ready":

```yaml
- name: Wait for CatalogService gRPC to be ready
  run: timeout 30 sh -c 'until nc -z localhost 6000; do sleep 1; done'
```

This is the minimal change to unblock all 10 failing tests.

### integration-test-scylla job (new)

A new parallel job in `ci-integration.yml`. It differs from `integration-test` only in:
1. Uses `docker compose -f compose.yml -f compose.scylla.yml` instead of `docker compose`
2. Adds a step to wait for ScyllaDB to be healthy (port 9042) before the existing health checks
3. Uses the same bootstrap, seed, and test steps verbatim

```yaml
integration-test-scylla:
  name: Integration Tests (ScyllaDB)
  runs-on: ubuntu-latest
  permissions:
    contents: read

  steps:
  - uses: actions/checkout@v6
  - name: Set up Go
    uses: actions/setup-go@v6
    with:
      go-version: '1.25.x'
      cache-dependency-path: tests/integration/go.sum
  - name: Set up Docker Compose
    uses: docker/setup-compose-action@v2
  - name: Build and start core stack with ScyllaDB
    run: docker compose -f compose.yml -f compose.scylla.yml up -d --build
    env:
      GITSTORE_GIT__DATA_DIR: ./data/repos
      GITSTORE_AUTH__ADMIN__USERNAME: admin
      GITSTORE_AUTH__ADMIN__PASSWORD_HASH: "$2a$10$awdOeTC5BhJGSasW9EdvO.4wnP7SbC96ycmbPx5dxIxEdbHye6eOy"
      GITSTORE_AUTH__JWT__SECRET: ci-test-jwt-secret-minimum-32-characters-long
  - name: Wait for ScyllaDB to be healthy
    run: timeout 120 sh -c 'until nc -z localhost 9042; do sleep 2; done'
  - name: Wait for services to be healthy
    run: |
      timeout 90 sh -c 'until curl -sf http://localhost:4000/health; do sleep 2; done'
      timeout 60 sh -c 'until curl -sf http://localhost:5000/health; do sleep 2; done'
      docker compose -f compose.yml -f compose.scylla.yml ps
  - name: Wait for git-service gRPC to be ready
    run: timeout 30 sh -c 'until nc -z localhost 50051; do sleep 1; done'
  - name: Wait for CatalogService gRPC to be ready
    run: timeout 30 sh -c 'until nc -z localhost 6000; do sleep 1; done'
  - name: Bootstrap namespace and repository
    run: make bootstrap ADMIN_PASSWORD=admin123 NAMESPACE=gitci NAMESPACE_DISPLAY_NAME=CI NAMESPACE_TIER=USER REPOSITORY=catalog
  - name: Seed repository with initial commit
    run: |
      git config --global user.email "ci@gitstore.dev"
      git config --global user.name "CI"
      git config --global init.defaultBranch main
      tmpdir=$(mktemp -d)
      git clone http://localhost:5000/gitci/catalog.git "$tmpdir"
      echo "# CI seed" > "$tmpdir/README.md"
      git -C "$tmpdir" add README.md
      git -C "$tmpdir" commit -m "ci: seed initial commit"
      git -C "$tmpdir" push origin main
  - name: Run integration tests (ScyllaDB backend)
    working-directory: ./tests/integration
    env:
      NAMESPACE: gitci
      REPOSITORY: catalog
    run: go test -v -timeout 120s ./...
  - name: Collect logs on failure
    if: failure()
    run: docker compose -f compose.yml -f compose.scylla.yml logs
  - name: Cleanup
    if: always()
    run: docker compose -f compose.yml -f compose.scylla.yml down -v
```

### Trigger paths

The ScyllaDB job must trigger on the same file paths as the existing jobs. No change to the
`on:` trigger section is needed — the existing path filters already cover `compose.yml`,
`gitstore-api/**`, and `gitstore-git-service/**`.

## Complexity Tracking

No constitution violations. No complexity justification needed.
