# Quickstart: Move Git Smart HTTP Server into gitstore-api

**Branch**: `012-smart-http-api` | **Date**: 2026-05-30

## Prerequisites

- GH#65 merged: `gitstore-git-service` exposes a gRPC server on port 50051
- GH#70 merged: `(namespace, repo-name) → repo_id` lookup available via `datastore.Datastore`
- GH#100 merged: repository storage identity fanout path strategy in place
- GH#39 merged: namespace support available in `gitstore-api`
- Docker Compose or local Go/Rust toolchain available

## Running the Stack

```bash
# Start the full stack (API + git service)
make dev

# Or with Docker Compose
make compose

# With ScyllaDB
make compose-scylla
```

After startup:
- GraphQL/REST API: `http://localhost:4000`
- Git smart HTTP: `http://localhost:5000`
- gRPC (git service internal): `localhost:50051`

## Verifying the Feature

### 1. Bootstrap a namespace and repository

```bash
make bootstrap ADMIN_PASSWORD=<password>
# Creates namespace "gitstore" and repository "catalog" by default
```

### 2. Clone over smart HTTP

```bash
git clone http://localhost:5000/gitstore/catalog
cd catalog
```

### 3. Push over smart HTTP

```bash
echo "# Hello" > README.md
git add README.md
git commit -m "chore: initial commit"
git push origin main
```

### 4. Fetch over smart HTTP

```bash
git fetch origin
```

### 5. Verify no WebSocket listener

```bash
# git service should have no open port except gRPC (50051)
ss -tlnp | grep -E '8080|9418'  # should return nothing
```

### 6. Verify gitstore-api has no WebSocket references

```bash
# No websocket package should be present
grep -r "websocket" gitstore-api/internal/ # should return nothing
grep "gorilla/websocket" gitstore-api/go.mod # should return nothing
```

## Environment Variables

### gitstore-api

| Variable                  | Default                  | Description                          |
|---------------------------|--------------------------|--------------------------------------|
| `GITSTORE_API__PORT`      | `4000`                   | GraphQL/REST API port                |
| `GITSTORE_API__GIT_PORT`  | `5000`                   | Git smart HTTP port                  |
| `GITSTORE_GIT__GRPC__URI` | `dns:///localhost:50051` | gRPC address of gitstore-git-service |

**Removed variables** (no longer accepted):
- `GITSTORE_GIT__WS__URI`
- `GITSTORE_GIT__HTTP__URI`

### gitstore-git-service

| Variable                 | Default           | Description          |
|--------------------------|-------------------|----------------------|
| `GITSTORE_GRPC__PORT`    | `50051`           | gRPC server port     |
| `GITSTORE_GIT__DATA_DIR` | `.gitstore/repos` | Repository data root |

**Removed variables** (no longer accepted):
- `GITSTORE_HTTP__PORT`
- `GITSTORE_WS__PORT`

## Large Push Test

To verify bounded memory on large pushes:

```bash
# Generate a repository with ~1 GB of history (requires git-filter-repo or similar)
# Then push and observe memory usage
git push http://localhost:5000/gitstore/large-repo main
# Peak RSS of gitstore-git-service should not reach the full pack size
```

## Running Tests

```bash
# Unit + integration tests
make test

# PR readiness check (includes lint, license, build)
make pr-ready
```

## Compose Health Check

Both ports report health within 30 seconds of startup:

```bash
curl http://localhost:4000/health   # {"status":"ok"}
curl http://localhost:5000/health   # {"status":"ok"}
```
