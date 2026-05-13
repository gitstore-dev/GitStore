# Quickstart: Feature 008 — Remove Git Binary Shell-Outs

**Branch**: `008-remove-git-shellouts` | **Date**: 2026-05-13

## Prerequisites

- Rust toolchain (MSRV 1.82 per CLAUDE.md)
- Docker + Docker Compose (for container image validation)
- `git` binary on the **developer's machine** (needed to drive integration tests from the test host — the server image must not have it)

## Build & Test

```bash
cd gitstore-git-service

# Check formatting and clippy
cargo fmt --all -- --check
cargo clippy --all-targets --all-features -- -D warnings

# Build
cargo build --verbose

# Run all tests (unit + integration)
cargo test --verbose
```

## Verify No Shell-Outs Remain

```bash
# Must return zero matches in production source
grep -rn 'Command::new("git")' gitstore-git-service/src/
```

## Build and Validate the Container Image

```bash
# Build the git-service image
docker build -f docker/git-service.Dockerfile -t gitstore-git-service:local .

# Confirm git binary is absent
docker run --rm gitstore-git-service:local which git 2>&1 || echo "git binary absent — OK"

# Confirm service starts cleanly
docker run --rm -e GITSTORE_GIT__DATA_DIR=/tmp/repos \
  gitstore-git-service:local /app/git-service &
sleep 2
curl -sf http://localhost:9418/health | grep healthy
```

## Run HTTP Smart Protocol Integration Test

The integration test spins up the Axum server against a temp directory and uses the local `git` binary (on the test host) to drive clone/fetch/push. It verifies the server handles all operations in-process.

```bash
cargo test --test integration http_smart_protocol -- --nocapture
```

## Tune the Pack Size Limit

Set via environment variable (default 50 MB):

```bash
export GITSTORE_GIT__MAX_PACK_SIZE_BYTES=104857600  # 100 MB
```

Or via config file:

```toml
[git]
max_pack_size_bytes = 104857600
```
