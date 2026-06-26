# Quickstart: Auth Phase 3 — Session Lifecycle (Local Dev)

**Branch**: `032-auth-phase-3` | **Date**: 2026-06-26

---

## Prerequisites

Phase 3 builds on top of Phase 1. Confirm your environment is healthy before starting:

```bash
make pr-ready      # must pass: all tests green, lint clean
make dev           # API + git-service running
```

If `make dev` is not running, start a fresh environment:

```bash
make gen-admin-password ADMIN_PASSWORD=secret
make dev
```

---

## New Configuration

One new optional env var is added in Phase 3. Add it to `gitstore-api/.env` if you want a non-default grace window:

```bash
# How long after JWT expiry refreshToken is still accepted (default: 60s)
GITSTORE_AUTH__JWT__REFRESH_GRACE=60s
```

All other variables (`GITSTORE_AUTH__ADMIN__*`, `GITSTORE_AUTH__JWT__SECRET`, etc.) are unchanged.

---

## Smoke-testing the Session Lifecycle

### 1. Login

```bash
curl -s -X POST http://localhost:4000/graphql \
  -H "Content-Type: application/json" \
  -d '{"query":"mutation { login(input:{username:\"admin\",password:\"secret\"}) { session { token expiresAt user { username isAdmin } } } }"}'
```

Expected: `session.user.isAdmin` is `true`, `session.user.username` is `"admin"` (not hardcoded — derived from principal).

Save the token:

```bash
TOKEN=$(curl -s ... | jq -r '.data.login.session.token')
```

### 2. Use the token

```bash
curl -s -X POST http://localhost:4000/graphql \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"query":"query { namespaces { edges { node { identifier } } } }"}'
```

### 3. Refresh the token

```bash
NEW_SESSION=$(curl -s -X POST http://localhost:4000/graphql \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"query":"mutation { refreshToken(input:{}) { session { token expiresAt } } }"}')

echo $NEW_SESSION | jq .

NEW_TOKEN=$(echo $NEW_SESSION | jq -r '.data.refreshToken.session.token')
```

### 4. Verify old token is rejected

```bash
curl -s -X POST http://localhost:4000/graphql \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"query":"query { namespaces { edges { node { identifier } } } }"}'
```

Expected: `errors[0].message` contains `"token has been revoked"`.

### 5. Logout

```bash
curl -s -X POST http://localhost:4000/graphql \
  -H "Authorization: Bearer $NEW_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"query":"mutation { logout(input:{}) { success } }"}'
```

Expected: `{ "data": { "logout": { "success": true } } }`.

### 6. Verify revoked token is rejected

```bash
curl -s -X POST http://localhost:4000/graphql \
  -H "Authorization: Bearer $NEW_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"query":"query { namespaces { edges { node { identifier } } } }"}'
```

Expected: `errors[0].message` contains `"token has been revoked"`.

---

## Running Tests

```bash
# Unit tests only (fast, no running services needed)
cd gitstore-api && go test ./tests/unit/... -v

# Full test suite
make test

# PR readiness gate
make pr-ready
```

---

## Troubleshooting

| Symptom | Cause | Fix |
|---------|-------|-----|
| `logout` returns `"not implemented"` | Still on old code | Confirm you are on branch `032-auth-phase-3` and rebuilt |
| `refreshToken` returns `"token too old to refresh"` | Token expired beyond grace window | Log in again; set a longer `GITSTORE_AUTH__JWT__REFRESH_GRACE` for testing |
| `user.isAdmin` is `false` after login | `Roles` not set on principal | Confirm `static-admin` provider is in `GITSTORE_AUTH__AUTHN__CHAIN` |
| Old token accepted after `logout` | Server restarted, blacklist lost | Expected behaviour for in-memory blacklist; persistent blacklist is a future phase |
