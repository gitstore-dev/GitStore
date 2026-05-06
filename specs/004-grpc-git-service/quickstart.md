# Quickstart: Decouple API from Git Storage via gRPC Git Service

**Feature**: 004-grpc-git-service
**Date**: 2026-05-06

## Prerequisites

- Docker + Docker Compose
- Rust toolchain (rustup, cargo) — for git-service development
- Go 1.25+ — for API development
- buf CLI: `brew install bufbuild/buf/buf` or `go install github.com/bufbuild/buf/cmd/buf@latest`
- grpcurl (optional, for manual contract testing): `brew install grpcurl`

---

## 1. Set Up Proto Toolchain

```bash
# From repo root — verify buf is installed
buf --version

# Lint proto files (once proto/ is created)
buf lint proto/

# Check for breaking changes against main
buf breaking proto/ --against '.git#branch=main'

# Generate Rust stubs into gitstore-git-service/gen/
buf generate proto/ --template proto/buf.gen.rust.yaml

# Generate Go stubs into gitstore-api/gen/
buf generate proto/ --template proto/buf.gen.go.yaml
```

---

## 2. Run git-service with gRPC Enabled

```bash
cd gitstore-git-service

# Build (includes tonic codegen via build.rs)
cargo build

# Run with gRPC port exposed (default: 50051)
GITSTORE_GRPC_PORT=50051 \
GITSTORE_DATA_DIR=/data/repos \
cargo run
```

git-service now listens on three ports:
- `9418` — git protocol (push/fetch)
- `8080` — websocket notifications
- `50051` — gRPC contract (new)

---

## 3. Run API Against gRPC git-service

```bash
cd gitstore-api

# Build (go-git dep removed, gRPC client added)
go build ./...

# Run — note: no GITSTORE_GIT_REPO local path, use gRPC address instead
GITSTORE_GIT_GRPC=git-service:50051 \
GITSTORE_GIT_WS=ws://git-service:8080 \
GITSTORE_API_PORT=4000 \
go run ./cmd/server
```

The API no longer requires `GITSTORE_GIT_REPO` (local path) or a shared volume mount.

---

## 4. Run with Docker Compose

```bash
# From repo root — volume mount on API is removed
docker compose up

# Verify API has no volume to git repos
docker inspect gitstore-api | jq '.[].Mounts'
# Expected: [] (empty)

# Verify gRPC port on git-service
docker inspect gitstore-git-service | jq '.[].NetworkSettings.Ports'
# Expected includes: "50051/tcp"
```

---

## 5. Manual Contract Test with grpcurl

```bash
# List available services (requires tonic-reflection enabled in dev builds)
grpcurl -plaintext localhost:50051 list

# List files at a tag
grpcurl -plaintext -d '{"ref": "v1.0.0", "path_prefix": "products/", "recursive": true}' \
  localhost:50051 gitstore.git.v1.GitService/ListFiles

# Get a single file
grpcurl -plaintext -d '{"path": "products/apparel/shirt-001.md", "ref": "v1.0.0"}' \
  localhost:50051 gitstore.git.v1.GitService/GetFile

# Get latest tag
grpcurl -plaintext -d '{"prefix": "v"}' \
  localhost:50051 gitstore.git.v1.GitService/GetLatestTag
```

---

## 6. Run Integration Tests

```bash
# Unit/contract tests (in-process, no Docker required)
cd gitstore-api
go test ./internal/gitclient/... -v

# Cross-language integration tests (requires Docker for testcontainers-go)
go test ./tests/integration/... -v -tags integration
```

```bash
# Rust unit tests
cd gitstore-git-service
cargo test
```

---

## 7. Check Prometheus Metrics

```bash
# git-service: per-RPC server metrics
curl http://localhost:9090/metrics | grep grpc_server

# API: per-RPC client metrics
curl http://localhost:4000/metrics | grep grpc_client
```

Expected metric families:
- `grpc_server_handled_total{grpc_method="GetFile", grpc_code="OK"}`
- `grpc_server_handling_seconds_bucket{grpc_method="ListFiles"}`
- `grpc_client_handled_total{grpc_method="CommitFile", grpc_code="OK"}`
- `grpc_client_handling_seconds_bucket{grpc_method="CommitFile"}`

---

## Environment Variables Reference

| Variable             | Service     | Description                            | Default                 |
|----------------------|-------------|----------------------------------------|-------------------------|
| `GITSTORE_GRPC_PORT` | git-service | gRPC server listen port                | `50051`                 |
| `GITSTORE_GIT_GRPC`  | API         | git-service gRPC address (`host:port`) | —                       |
| `GITSTORE_GIT_WS`    | API         | git-service websocket URL              | `ws://git-service:8080` |
| `GITSTORE_GIT_REPO`  | API         | **Removed** — no longer used           | —                       |

---

## Troubleshooting

**`connection refused` on port 50051**
git-service was not built with the gRPC feature flag. Check `cargo build` output for tonic codegen errors in `build.rs`.

**API fails to start with `GITSTORE_GIT_GRPC not set`**
Set `GITSTORE_GIT_GRPC=<host>:50051`. The old `GITSTORE_GIT_REPO` env var is no longer read.

**`buf generate` fails with `protoc not found`**
buf manages its own protoc binary via `buf.yaml`. Run `buf mod update` to download. Do not install a separate `protoc`.

**Integration tests fail with `Cannot connect to Docker`**
`testcontainers-go` requires Docker daemon. Run `docker ps` to verify Docker is running.
