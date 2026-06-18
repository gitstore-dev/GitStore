# gitstore Development Guidelines

Auto-generated from all feature plans. Last updated: 2026-03-26

## Active Technologies
- API/Data stack (Go): `gqlgen v0.17.90`, `go-memdb v1.3.5` (dev), `gocqlx/v3 v3.0.4` + `gocql` (ScyllaDB prod), `go-playground/validator/v10`, `go.uber.org/zap`, `google/uuid`, `encoding/json`.
- Git service stack (Rust): `gix 0.84.0` (+ `gix-ref 0.64.0`), `tokio 1.35`, `axum 0.8`, `tonic 0.14`, `tracing 0.1`, `anyhow 1.0`, `async-trait 0.1`, `serde 1.0`, `serde_yaml 0.9`.
- Storage model: bare Git repositories on local filesystem; datastore abstraction with `go-memdb` in development and ScyllaDB 5.x+ in production.
- Controller manager stack (Go): `golang.org/x/time` (queue rate limiting), `github.com/alitto/pond/v2 v2.7.1` (worker pools), `github.com/cenkalti/backoff/v5 v5.0.3` (retry/backoff), `github.com/prometheus/client_golang v1.23.2` (health metrics), `net/http` stdlib (health/poison API), `go.uber.org/zap`, `github.com/spf13/viper` (025-controller-manager-runtime)
- Go 1.25 + `go.uber.org/zap`, `github.com/cenkalti/backoff/v5 v5.0.3`, `github.com/prometheus/client_golang v1.23.2`, `github.com/alitto/pond/v2 v2.7.1`, `runtime/debug` (stdlib — for stack traces) (026-reconcile-handler)
- In-memory only (`sync.RWMutex` maps) — no persistence added in this spec (026-reconcile-handler)
- Go 1.25 (gitstore-api) + `go.uber.org/zap`, `github.com/google/cel-go/cel`, `github.com/go-playground/validator/v10`, `encoding/json` (027-admission-contracts)
- None (in-process Go types only; no new datastore tables) (027-admission-contracts)
- Rust 1.x (`gitstore-git-service`); Go 1.25 (`gitstore-api`) + `gix 0.84.0`, `tonic 0.14`, `tokio 1.35` (Rust); no new Go deps (028-branch-deletion-admission)
- No datastore changes (028-branch-deletion-admission)

## Commands

### Workspace
- `make help` — list root commands and common variables.
- `make git` — run `gitstore-git-service` locally in the foreground using `GIT_DATA_DIR` (default: `.gitstore/repos`).
- `make api` — run `gitstore-api` locally in the foreground. Requires `gitstore-api/.env` or shell env for required auth secrets.
- `make controller` — run `gitstore-controller-manager` locally in the foreground on port 5001. Requires `GITSTORE_CONTROLLER__API_URI` pointing at a running API (default: `http://localhost:4000/graphql`).
- `make dev` — run the native git service and API together in the foreground with shutdown trapping.
- `make compose` — run the core Docker Compose stack (API + git service) in the foreground.
- `DETACH=1 make compose` — run the core Docker Compose stack in the background.
- `make scylla` — run only local Scylla services from `compose.yml` + `compose.scylla.yml`.
- `make compose-scylla` — run the full core stack with Scylla from `compose.yml` + `compose.scylla.yml`.
- `DETACH=1 make scylla` and `DETACH=1 make compose-scylla` — run those compose targets in the background.
- `make ps`, `make logs`, `make stop`, `make down` — compose lifecycle helpers. Use `SERVICE=<name>` with `logs` or `stop` to scope the command.
- `make gen-admin-password ADMIN_PASSWORD=<password>` — generate a bcrypt hash for the given password and write `GITSTORE_AUTH__ADMIN__PASSWORD_HASH` to `gitstore-api/.env` (creates the file if absent, updates the key if present). Run this once when setting up a fresh environment or changing the admin password.
- `make bootstrap-token ADMIN_PASSWORD=<password>` — authenticate against GraphQL and print/cache a bootstrap bearer token. Prints a remediation hint if the password is wrong.
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
- `NAMESPACE ?= gitstore-test`
- `NAMESPACE_DISPLAY_NAME ?= GitStore Test`
- `NAMESPACE_TIER ?= USER`
- `REPOSITORY ?= catalog`
- `DEFAULT_BRANCH ?= main`

## Code Style

: Follow standard conventions

## Recent Changes
- 028-branch-deletion-admission: Added Rust 1.x (`gitstore-git-service`); Go 1.25 (`gitstore-api`) + `gix 0.84.0`, `tonic 0.14`, `tokio 1.35` (Rust); no new Go deps
- 027-admission-contracts: Added Go 1.25 (gitstore-api) + `go.uber.org/zap`, `github.com/google/cel-go/cel`, `github.com/go-playground/validator/v10`, `encoding/json`
- 026-reconcile-handler: Added Go 1.25 + `go.uber.org/zap`, `github.com/cenkalti/backoff/v5 v5.0.3`, `github.com/prometheus/client_golang v1.23.2`, `github.com/alitto/pond/v2 v2.7.1`, `runtime/debug` (stdlib — for stack traces)


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
at specs/024-product-variant/plan.md
<!-- SPECKIT END -->
