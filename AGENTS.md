# gitstore Development Guidelines

Auto-generated from all feature plans. Last updated: 2026-03-26

## Active Technologies
- Go 1.25 (`gitstore-api`), Rust edition 2021 (`gitstore-git-service`) (005-structured-config-mgmt)
- N/A — configuration is in-memory after startup load (005-structured-config-mgmt)
- `go-memdb` (in-memory backend) / ScyllaDB 5.x+ (production backend) (006-api-datastore-abstraction)
- Rust edition 2021, MSRV 1.82 (required by gix 0.83.0) + `gix 0.83.0` (replaces `git2 0.20.4`), `tokio 1.35`, `axum 0.8`, `tonic 0.14`, `tracing 0.1`, `anyhow 1.0` (007-migrate-gitoxide)
- Bare Git repositories on local filesystem (unchanged) (007-migrate-gitoxide)
- Rust edition 2021, MSRV 1.82 + `gix 0.83.0`, `gix-packetline` (compatible version), `gix-pack` (compatible version), `gix-protocol` (compatible version), `axum 0.8`, `tokio 1.35`, `tracing 0.1`, `tempfile 3.8` (dev) (008-remove-git-shellouts)
- Go 1.25 (`gitstore-api`) + `gqlgen v0.17.90`, `go-memdb v1.3.5`, `gocqlx/v3 v3.0.4` (ScyllaDB), `go-playground/validator/v10`, `go.uber.org/zap`, `google/uuid` (009-api-namespaces)
- `go-memdb` (development / in-memory backend) / ScyllaDB 5.x+ (production backend) — via the `datastore.Datastore` interface from feature 006 (009-api-namespaces)
- Go 1.25 (`gitstore-api`) · Rust edition 2021, MSRV 1.82 (`gitstore-git-service`) + `gqlgen v0.17.90`, `go-memdb v1.3.5`, `gocqlx/v3 v3.0.4`, `google/uuid v1.6.0` (Go) · `gix 0.83.0`, `tonic 0.14.6`, `prost 0.14.3` (Rust) (010-repo-storage-identity)
- `go-memdb` (development) · ScyllaDB 5.x+ (production) — via `datastore.Datastore` interface (feature #006) (010-repo-storage-identity)
- Rust edition 2021, MSRV 1.82; actual gix version is `0.84.0` (Cargo.lock canonical) + `gix 0.84.0`, `gix-ref 0.64.0` (two-phase transaction API), `tokio 1.35` (full features), `tonic 0.14`, `tracing 0.1`, `anyhow 1.0`, `async-trait 0.1` (to add) (013-receive-pack-hooks)
- Go 1.25 (`gitstore-api`), Rust edition 2021 MSRV 1.82 (`gitstore-git-service`) + `github.com/adrg/frontmatter v0.2.0`, `go-playground/validator/v10 v10.30.3`, `gqlgen v0.17.90` (Go) · `gix 0.84.0`, `serde 1.0`, `serde_yaml 0.9` (to add) (Rust) (014-product-frontmatter)
- ScyllaDB 5.x+ (production) / `go-memdb v1.3.5` (development) — via `datastore.Datastore` interface (014-product-frontmatter)

- (001-git-backed-ecommerce)

## Commands

### Workspace
- `make help` — list root commands and common variables.
- `make git` — run `gitstore-git-service` locally in the foreground using `GIT_DATA_DIR` (default: `.gitstore/repos`).
- `make api` — run `gitstore-api` locally in the foreground. Requires `gitstore-api/.env` or shell env for required auth secrets.
- `make dev` — run the native git service and API together in the foreground with shutdown trapping.
- `make compose` — run the core Docker Compose stack (API + git service) in the foreground.
- `DETACH=1 make compose` — run the core Docker Compose stack in the background.
- `make scylla` — run only local Scylla services from `compose.yml` + `compose.scylla.yml`.
- `make compose-scylla` — run the full core stack with Scylla from `compose.yml` + `compose.scylla.yml`.
- `DETACH=1 make scylla` and `DETACH=1 make compose-scylla` — run those compose targets in the background.
- `make ps`, `make logs`, `make stop`, `make down` — compose lifecycle helpers. Use `SERVICE=<name>` with `logs` or `stop` to scope the command.
- `make bootstrap-token ADMIN_PASSWORD=<password>` — authenticate against GraphQL and print/cache a bootstrap bearer token.
- `make bootstrap ADMIN_PASSWORD=<password>` — create the default namespace and repository through the running API.
- `make bootstrap-namespace` / `make bootstrap-repository` — create only one bootstrap resource. `bootstrap-repository` requires the namespace to exist.
- `make git-clean-data CONFIRM=1` — remove the native local git-service repository data directory only; does not remove Docker volumes.
- `make build`, `make test`, `make lint`, `make license-check`, `make pr-ready` — aggregate development and PR readiness checks.
- `make admin-compose`, `make admin-stop`, `make admin-down`, `make admin-logs` — optional admin compose wrappers.

Common bootstrap variables:
- `API_URL ?= http://localhost:4000/graphql`
- `ADMIN_USERNAME ?= admin`
- `ADMIN_PASSWORD` is required unless `BOOTSTRAP_TOKEN` is provided or a cached bootstrap token exists.
- `BOOTSTRAP_TOKEN` overrides login/cached-token lookup.
- `NAMESPACE ?= gitstore`
- `NAMESPACE_DISPLAY_NAME ?= GitStore`
- `NAMESPACE_TIER ?= USER`
- `REPOSITORY ?= catalog`
- `DEFAULT_BRANCH ?= main`

## Code Style

: Follow standard conventions

## Recent Changes
- 014-product-frontmatter: Added Go 1.25 (`gitstore-api`), Rust edition 2021 MSRV 1.82 (`gitstore-git-service`) + `github.com/adrg/frontmatter v0.2.0`, `go-playground/validator/v10 v10.30.3`, `gqlgen v0.17.90` (Go) · `gix 0.84.0`, `serde 1.0`, `serde_yaml 0.9` (to add) (Rust)
- 013-receive-pack-hooks: Added Rust edition 2021, MSRV 1.82; actual gix version is `0.84.0` (Cargo.lock canonical) + `gix 0.84.0`, `gix-ref 0.64.0` (two-phase transaction API), `tokio 1.35` (full features), `tonic 0.14`, `tracing 0.1`, `anyhow 1.0`, `async-trait 0.1` (to add)
- 012-smart-http-api: Go 1.25 (`gitstore-api`) + `net/http` second server on port 5000 · Rust edition 2021, MSRV 1.82 (`gitstore-git-service`) — adds `ReceivePack` (client-streaming), `UploadPack` (server-streaming), `InfoRefs` gRPC RPCs; removes `axum`/HTTP server, `tokio_tungstenite`/`tungstenite` WebSocket deps; removes `gorilla/websocket` and `internal/websocket` from `gitstore-api`


<!-- MANUAL ADDITIONS START -->
## Development Guidelines

- The root `Makefile` is the canonical command interface for this repository. Future repo-level commands must be added to the root `Makefile` and documented in this file.

- Before creating a PR run:

  ```bash
  make pr-ready
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
