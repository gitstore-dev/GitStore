# Integration Test Contract: Core Stack

**Feature**: 003-oss-alignment  
**Date**: 2026-05-02  
**Scope**: `gitstore-api` ↔ `gitstore-git-service` interaction boundary  
**Test location**: `tests/integration/`  
**Language**: Go  
**CI job**: `integration-test` in `.github/workflows/ci.yml`

## Purpose

This contract defines the observable behaviours that the core-stack integration tests MUST verify. It replaces the `# TODO: Add integration test commands when implemented` placeholder in CI. Tests are written first (red), then implementation confirms they pass (green).

## Environment Prerequisites

- `compose.yml` brings up `gitstore-git-service` and `gitstore-api` (core stack only).
- `gitstore-admin` is NOT required; `compose.admin.yml` is NOT used in CI.
- Tests use environment variables for service URLs (defaulting to localhost ports).

| Variable             | Default                                         |
|----------------------|-------------------------------------------------|
| `GIT_SERVER_URL`     | `http://localhost:9418`                         |
| `GIT_SERVER_GIT_URL` | `git://localhost:9418` (or HTTP smart protocol) |
| `GIT_SERVER_WS_URL`  | `ws://localhost:8080`                           |
| `API_URL`            | `http://localhost:4000`                         |

## Contract Assertions

### C-001: Health — Both Services Report Healthy

```
GET {GIT_SERVER_URL}/health
→ 200 OK
→ body.status == "healthy"

GET {API_URL}/health
→ 200 OK
→ body.status == "healthy"
```

**Must pass before**: all other contract assertions are attempted.

---

### C-002: Valid Push → WebSocket Notification Emitted

```
SETUP: Connect WebSocket client to {GIT_SERVER_WS_URL}
ACTION: Push a commit containing a valid product markdown file to gitstore-git-service
ASSERT: WebSocket message received within 5 seconds
ASSERT: message.repository is non-empty string
ASSERT: message.ref == "refs/heads/main"
ASSERT: message.commit_sha matches ^[0-9a-f]{40}$
```

---

### C-003: Release Tag Push → Product Visible in GraphQL

```
SETUP: Valid product commit already pushed (from C-002 setup or fresh)
ACTION: Push annotated tag "v0.0.1-inttest" to gitstore-git-service
ASSERT (with retry, max 10s):
  GraphQL query { products(first: 10) { edges { node { sku } } } }
  → response contains a product node with sku matching the pushed product
```

---

### C-004: Invalid Push Rejected with Structured Error

```
ACTION: Push a commit where a product front-matter field "price" has value "not-a-number"
ASSERT: Push exit code != 0
ASSERT: Push rejection message (stderr/stdout) contains the string "price"
        OR contains "validation" (case-insensitive)
ASSERT: Subsequent GraphQL query does NOT return a product with the test SKU
```

---

### C-005: WebSocket Health Endpoint Reports Connection Count

```
GET {GIT_SERVER_URL}/websocket/health
→ 200 OK
→ body.status is non-empty
→ body.active_connections is a non-negative integer
```

## What This Contract Does NOT Cover

- Admin UI interactions (covered by `gitstore-admin/tests/e2e/`)
- Internal Rust unit logic (covered by `gitstore-git-service` cargo tests)
- Internal Go unit logic (covered by `gitstore-api` go test)
- Performance benchmarks
- Authentication/authorisation flows (deferred per Constitution Principle VII)

## CI Integration

The `integration-test` job in `ci.yml` MUST:

1. Start only the core stack: `docker compose up -d --build`  
2. Wait for both health checks to pass (poll `/health` endpoints, timeout 60s)  
3. Run: `go test -v -timeout 120s ./tests/integration/...`  
4. Always run clean-up: `docker compose down -v`

The job MUST NOT reference `compose.admin.yml` or start `gitstore-admin`.
