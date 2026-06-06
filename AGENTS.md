# gitstore Development Guidelines

Auto-generated from all feature plans. Last updated: 2026-03-26

## Active Technologies
- Languages: Go 1.25 (`gitstore-api`, `gitstore-controller-manager`) and Rust edition 2021, MSRV 1.82 (`gitstore-git-service`).
- API/Data stack (Go): `gqlgen v0.17.90`, `go-memdb v1.3.5` (dev), `gocqlx/v3 v3.0.4` + `gocql` (ScyllaDB prod), `go-playground/validator/v10`, `go.uber.org/zap`, `google/uuid`, `encoding/json`.
- Git service stack (Rust): `gix 0.84.0` (+ `gix-ref 0.64.0`), `tokio 1.35`, `axum 0.8`, `tonic 0.14`, `tracing 0.1`, `anyhow 1.0`, `async-trait 0.1`, `serde 1.0`, `serde_yaml 0.9`.
- Storage model: bare Git repositories on local filesystem; datastore abstraction with `go-memdb` in development and ScyllaDB 5.x+ in production.
- Product metadata/parsing: `github.com/adrg/frontmatter v0.2.0` and `gopkg.in/yaml.v3` (parser is in-memory via `io.Reader`).
- Go 1.25 (`gitstore-api`) + `gqlgen v0.17.90`, `go-playground/validator/v10 v10.30.3`, `github.com/adrg/frontmatter v0.2.0`, `gopkg.in/yaml.v3`, `go.uber.org/zap`, `shopspring/decimal` (001-product-spec-validation)
- `go-memdb v1.3.5` (dev) / `gocqlx/v3 v3.0.4` + `gocql` (ScyllaDB prod) (001-product-spec-validation)
- Rust edition 2021, MSRV 1.82 (`gitstore-git-service`); Go 1.25 (`gitstore-api`) (018-hook-pipeline-wiring)
- Existing `Datastore` interface (`CreateProduct` / `UpdateProduct`) — no schema changes (018-hook-pipeline-wiring)
- `go-memdb` (dev) / ScyllaDB 5.x+ (prod) via `Datastore` interface (`CreateProduct` / `UpdateProduct`) (018-hook-pipeline-wiring)
- Rust edition 2021, MSRV 1.82 (`gitstore-git-service`) + `gix 0.84.0`, `gix-pack 0.71.0`, `tokio 1.35`, `anyhow 1.0`, `tracing 0.1` (019-fix-upload-pack)
- N/A (reads existing bare git repositories on local filesystem) (019-fix-upload-pack)

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
- 019-fix-upload-pack: Added Rust edition 2021, MSRV 1.82 (`gitstore-git-service`) + `gix 0.84.0`, `gix-pack 0.71.0`, `tokio 1.35`, `anyhow 1.0`, `tracing 0.1`
- 018-hook-pipeline-wiring: Added Rust edition 2021, MSRV 1.82 (`gitstore-git-service`); Go 1.25 (`gitstore-api`)
- 018-hook-pipeline-wiring: Added Rust edition 2021, MSRV 1.82 (`gitstore-git-service`); Go 1.25 (`gitstore-api`)


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
