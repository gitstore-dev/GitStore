# gitstore Development Guidelines

Auto-generated from all feature plans. Last updated: 2026-03-26

## Active Technologies
- Go 1.25 (`gitstore-api`), Rust edition 2021 (`gitstore-git-service`) (005-structured-config-mgmt)
- N/A — configuration is in-memory after startup load (005-structured-config-mgmt)
- `go-memdb` (in-memory backend) / ScyllaDB 5.x+ (production backend) (006-api-datastore-abstraction)
- Rust edition 2021, MSRV 1.82 (required by gix 0.83.0) + `gix 0.83.0` (replaces `git2 0.20.4`), `tokio 1.35`, `axum 0.8`, `tonic 0.14`, `tracing 0.1`, `anyhow 1.0` (007-migrate-gitoxide)
- Bare Git repositories on local filesystem (unchanged) (007-migrate-gitoxide)

- (001-git-backed-ecommerce)

## Commands

### Workspace

## Code Style

: Follow standard conventions

## Recent Changes
- 007-migrate-gitoxide: Added Rust edition 2021, MSRV 1.82 (required by gix 0.83.0) + `gix 0.83.0` (replaces `git2 0.20.4`), `tokio 1.35`, `axum 0.8`, `tonic 0.14`, `tracing 0.1`, `anyhow 1.0`
- 006-api-datastore-abstraction: Added Go 1.25 (`gitstore-api`)
- 005-structured-config-mgmt: Added Go 1.25 (`gitstore-api`), Rust edition 2021 (`gitstore-git-service`)


<!-- MANUAL ADDITIONS START -->
## GitOps

- Before creating a PR do the following checks:

  ```bash
  # Check formatting and clippy for Rust
  cd gitstore-git-service
  cargo fmt --all -- --check
  cargo clippy --all-targets --all-features -- -D warnings
  cargo build --verbose
  cargo test --verbose

  # Check formatting and linting for Go
  cd ../gitstore-api
  if [ "$(gofmt -s -l . | wc -l)" -gt 0 ]; then
      echo "The following files need formatting:"
      gofmt -s -l .
      exit 1
  fi
  go vet ./...
  # Setup $GOPATH/bin in PATH if not already
  go install honnef.co/go/tools/cmd/staticcheck@latest
  staticcheck ./...
  go build -v ./...
  go test -v -race -coverprofile=coverage.txt -covermode=atomic ./...

  # Check Go license headers (all files + branch diff)
  cd ..
  ./scripts/check-go-license-headers.sh --all
  ./scripts/check-go-license-headers.sh --diff-base origin/main

  # Check Rust license headers (all files + branch diff)
  ./scripts/check-rust-license-headers.sh --all
  ./scripts/check-rust-license-headers.sh --diff-base origin/main

  # Check TypeScript/JavaScript license headers (all files + branch diff)
  ./scripts/check-js-license-headers.sh --all
  ./scripts/check-js-license-headers.sh --diff-base origin/main
  ```

- Install git hooks once per clone so staged Go/Rust/TS/JS files are checked automatically:

  ```bash
  ./scripts/install-git-hooks.sh
  ```

- Use Conventional Commits
- After implementing a feature update the documentation in [`docs/`](docs/).

## Tool Usage

- Prefer editor-based tools for file operations (read/edit/create/move) and reserve terminal commands primarily for build, lint, and test workflows.
<!-- MANUAL ADDITIONS END -->

<!-- SPECKIT START -->
For additional context about technologies to be used, project structure,
shell commands, and other important information, read the current plan
<!-- SPECKIT END -->
