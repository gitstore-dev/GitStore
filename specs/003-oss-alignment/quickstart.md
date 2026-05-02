# Quickstart: OSS Alignment Feature

**Feature**: 003-oss-alignment  
**Date**: 2026-05-02

## What This Feature Changes

After this feature lands, the repository structure and workflows change in three ways:

1. **Folder names** — service folders are prefixed: `api → gitstore-api`, `git-server → gitstore-git-service`, `admin-ui → gitstore-admin`.
2. **Compose split** — `docker compose up -d` starts only the core stack. Add `compose.admin.yml` to include the admin add-on.
3. **Integration tests** — `tests/integration/` contains real Go tests for the core stack that run in CI.

---

## Running the Core Stack (Default)

```bash
# Clone
git clone https://github.com/gitstore-dev/gitstore
cd gitstore

# Start core stack only
docker compose up -d

# Verify
docker compose ps
# Expected: gitstore-git-service and gitstore-api only
```

---

## Running the Full Stack Including Admin (Add-On)

```bash
# Start core stack + admin add-on
docker compose -f compose.yml -f compose.admin.yml up -d

# Verify
docker compose -f compose.yml -f compose.admin.yml ps
# Expected: gitstore-git-service, gitstore-api, and gitstore-admin
```

See [docs/admin/quickstart.md](../../docs/admin/quickstart.md) for full admin setup documentation.

---

## Running Integration Tests Locally

```bash
# Start core stack
docker compose up -d

# Wait for services to be healthy (check with docker compose ps)

# Run integration tests
go test -v -timeout 120s ./tests/integration/...

# Cleanup
docker compose down -v
```

---

## Building from Source After Rename

```bash
# Git service (Rust) — now in gitstore-git-service/
cd gitstore-git-service
cargo build --release
cargo test

# GraphQL API (Go) — now in gitstore-api/
cd ../gitstore-api
go mod download
go build -v ./...
go test -v ./...

# Admin UI (Node.js/Astro) — now in gitstore-admin/ — optional
cd ../gitstore-admin
npm install
npm run dev
```
