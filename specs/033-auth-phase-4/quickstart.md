# Quickstart: Phase 4 gRPC HMAC Auth — Local Setup

**Branch**: `033-auth-phase-4` | **Date**: 2026-06-26

---

## Prerequisites

- Phase 1 (`031-pluggable-authn-authz`) and Phase 3 (`032-auth-phase-3`) merged
- `gitstore-api/.env` exists with `GITSTORE_AUTH__ADMIN__PASSWORD_HASH` and `GITSTORE_AUTH__JWT__SECRET`
- `make` and Go 1.25 toolchain available

---

## One-time setup

### 1. Generate the HMAC secret

```bash
make gen-hmac-secret
```

This generates a random secret and appends `GITSTORE_AUTH__GRPC__HMAC_SECRET=<value>` to
`gitstore-api/.env`. You must also add the same secret to the git service config (see step 2).

### 2. Add the HMAC secret to the git service env

```bash
# The git service reads its env from the shell or a gitstore.toml file.
# For local dev, export it in your shell or add to a .env file:
echo "GITSTORE_AUTH__GRPC__HMAC_SECRET=$(grep GITSTORE_AUTH__GRPC__HMAC_SECRET gitstore-api/.env | cut -d= -f2)" >> gitstore-git-service/.env
```

Or, set it in your shell session:
```bash
export GITSTORE_AUTH__GRPC__HMAC_SECRET=$(grep GITSTORE_AUTH__GRPC__HMAC_SECRET gitstore-api/.env | cut -d= -f2-)
```

### 3. Start the stack

```bash
make dev
```

Both services start. The git service logs:
```
{"level":"INFO","msg":"gRPC HMAC auth active","rotation_window_open":false}
```

---

## Verify the HMAC guard

### Confirm the happy path (API can reach git service)

```bash
make bootstrap ADMIN_PASSWORD=<your-password>
```

A successful bootstrap (no `unauthenticated` errors) confirms the API is sending the correct token.

### Confirm the guard rejects unauthorized callers

Use `grpcurl` (or `evans`) to send a call with no token:

```bash
grpcurl -plaintext localhost:50051 gitstore.git.v1.GitService/ListFiles
# Expected: ERROR: Code: Unauthenticated
#           Message: missing inter-service token
```

With a wrong token:
```bash
grpcurl -plaintext -H 'authorization: Bearer wrong-secret' \
  localhost:50051 gitstore.git.v1.GitService/ListFiles
# Expected: ERROR: Code: Unauthenticated
#           Message: invalid inter-service token
```

---

## Generating secrets (reference)

| Need | Command |
|------|---------|
| New bcrypt password hash | `make gen-admin-password ADMIN_PASSWORD=<pw>` |
| New JWT secret | `make gen-jwt-secret` |
| New gRPC HMAC secret | `make gen-hmac-secret` |

All `gen-*` targets write `KEY=VALUE` lines suitable for appending to `.env` with `>>`.

---

## Rotating the HMAC secret

1. Generate a new secret: `make gen-hmac-secret`
2. On the git service, add `GITSTORE_AUTH__GRPC__HMAC_SECRET_PREVIOUS=<old_value>` and update
   `GITSTORE_AUTH__GRPC__HMAC_SECRET=<new_value>`. Restart the git service.
3. Update `GITSTORE_AUTH__GRPC__HMAC_SECRET=<new_value>` in `gitstore-api/.env`. Restart the API.
4. Confirm `make bootstrap` succeeds.
5. Remove `GITSTORE_AUTH__GRPC__HMAC_SECRET_PREVIOUS` from git service config. Restart.

---

## Troubleshooting

| Symptom | Likely cause | Fix |
|---------|-------------|-----|
| API fails to start with `GITSTORE_AUTH__GRPC__HMAC_SECRET missing` | Key not in `.env` | Run `make gen-hmac-secret` |
| Git service fails to start with `auth.grpc.hmac_secret must not be empty` | Secret not exported | Export `GITSTORE_AUTH__GRPC__HMAC_SECRET` in shell |
| `make bootstrap` fails with `unauthenticated` on git ops | Secrets mismatched | Verify both `.env` files have the same value |
| Git service starts but logs `rotation_window_open: true` unexpectedly | `GITSTORE_AUTH__GRPC__HMAC_SECRET_PREVIOUS` still set | Remove the old key from git service env |
