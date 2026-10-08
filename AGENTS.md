# gitstore Development Guidelines

Auto-generated from all feature plans. Last updated: 2026-03-26

## Active Technologies

Baseline stack, established in early specs (025-035) and reused unchanged by every later spec unless a delta is called out below:
- **Go** 1.25 (`gitstore-api`, `gitstore-controller-manager`; container builders on 1.26.1) — `gqlgen v0.17.90` (GraphQL schema/resolver codegen + `transport.Websocket` subscriptions), `go-playground/validator/v10`, `go.uber.org/zap`, `google/uuid`, `encoding/json`, `github.com/prometheus/client_golang v1.23.2`, `github.com/google/cel-go/cel` (admission rule evaluation), `golang-jwt/v5 v5.3.1`, `golang.org/x/crypto` (bcrypt), `github.com/spf13/viper`.
- **Controller manager** adds: `golang.org/x/time` (queue rate limiting), `github.com/alitto/pond/v2 v2.7.1` (worker pools), `github.com/cenkalti/backoff/v5 v5.0.3` (retry/backoff), `github.com/syndtr/goleveldb v1.0.0` (bounded disk-backed controller projections, work and indexes).
- **Rust** 1.x (`gitstore-git-service`) — `gix 0.84.0` (+ `gix-ref 0.64.0`), `tokio 1.35`, `tonic 0.14`, `tracing 0.1`, `anyhow 1.0`, `async-trait 0.1`, `serde 1.0`, `serde_yaml 0.9`.
- **Storage**: bare Git repositories on local filesystem for Git-backed resources; `datastore.Datastore` abstraction backed by `go-memdb v1.3.5` in development and `gocqlx/v3 v3.0.4` + `gocql` (ScyllaDB 5.x+) in production. Cross-service auth uses a shared `GITSTORE_GRPC_AUTH__HMAC_SECRET`.

Reusable internal interfaces, defined once and instantiated unchanged by later specs (CategoryTaxonomy, Namespace, Repository, Product, etc.) rather than reimplemented:
- `internal/types.Reconciler`/`ReconcileResult`, `internal/status.StatusClient`/`StatusPatch` (`gitstore-controller-manager`, spec 026)
- `internal/listwatch.ListWatcher[T]`/`Watcher[T]`/`Runner[T]`/`WatchEvent[T]` (spec 036)
- `internal/cache.Cache[T]`/`CacheAccessor[T]`/`EventHandler[T]`, `internal/manager.Manager` (spec 026)
- Production controllers use `internal/listwatch.PagedListWatcher[T]`, `internal/checkpoint.DiskStore` and error-returning `internal/cache.LookupFunc[T]`; the full-snapshot/memory adapters remain for compatibility fixtures.
- `internal/graphqlclient.Client` driving `POST /graphql` + `graphql-transport-ws` subscriptions against `gitstore-api`'s `transport.Websocket` (spec 039)
- `gitstore-api/internal/watchjournal` (Scylla CDC-backed, replica-safe durable watch journal) — the only watch mechanism: Namespace (spec 050), Repository (spec 058), Product (spec 055), File, CategoryTaxonomy. Namespaced catalog kinds register a `{kind, table, payload}` source in `scylla/catalog_cdc.go` (`RunCatalogCDC`) instead of adding a runner. Typed and generic watches fail closed without a configured journal; `watchResources` rejects kinds without a source with `UNSUPPORTED_KIND`. The in-memory `eventbus` package has been removed — do not reintroduce process-local fan-out.

Storage/schema notes:
- Most resources (Repository since spec 045, reused by 058; Namespace via spec 046) carry `Generation`, `ResourceVersion`, `Status json.RawMessage`, `DeletionTimestamp`, `Finalizers`, and an optimistic-concurrency `Update*(ctx, r, expectedResourceVersion)` method — later specs reuse these fields/methods rather than adding schema.
- Namespace/Repository/CategoryTaxonomy/Product existence and ownership checks reuse existing indexed `RepositoryID`/`NamespaceID` fields and the `status` JSON blob column; no migrations required (specs 039-042, 062).
- Scylla schema is a per-resource baseline (`scylla/migrations/001_infra.cql` … `009_service_account.cql`). Tables follow `<plural>_by_<key>`; watched kinds enable full-preimage/postimage CDC (14-day TTL) on their authoritative table and share the `resource_watch_clock`/`resource_watch_events` journal, consumed via `github.com/scylladb/scylla-cdc-go v1.2.1`; memdb implements the same journal contract in-process. New schema changes are incremental files after the baseline — never edit an applied migration.
- Spec 063 adds a repository-local `shared/secretmaterial` Go module (no new third-party dependency); provider-owned secret records are bounded in-process memory only, no persisted secret cache.
- Controller state uses `<Kind>.disk-v2` directories on existing per-replica checkpoint volumes. Legacy JSON checkpoints are retained but not loaded; the first upgrade streams a cold list. Never overlap processes on one checkpoint directory. Pending work, retry deadlines, quarantine, relation counts and bounded fan-out cursors are durable; lookups must preserve I/O errors rather than returning false absence.

Deltas not covered above: 028 (branch-deletion admission, no new Go deps), 029 (`config 0.15.22`, `regex 1`, already in Rust `Cargo.toml`), 033 (`cmd/gitctl` replaces `cmd/hashpw`), 035 (`github.com/gin-gonic/gin`, `go-grpc-prometheus`; push-policy fields on `datastore.Repository`), 048 (Scylla query-specific denormalized tables, `go-memdb` as dev/contract-test backend).

## Commands

### Workspace
- `make help` — list root commands and common variables.
- `make git` — run `gitstore-git-service` locally in the foreground using `GIT_DATA_DIR` (default: `.gitstore/repos`).
- `make api` — run `gitstore-api` locally in the foreground. Requires `gitstore-api/.env` or shell env for required auth secrets.
- `make controller` — run `gitstore-controller-manager` locally in the foreground on port 5001. Requires `GITSTORE_CONTROLLER__API_URI` pointing at a running API (default: `http://localhost:4000/graphql`).
- `make dev` — run the native git service and API together in the foreground with shutdown trapping.
- `make compose` — run all three core services with the Docker Compose `local` profile and shared `CONFIG_FILE` (default: `./config/config.toml`) in the foreground using the default in-memory datastore; no service `.env` files are required.
- `make compose DATASTORE=scylla` — run the full core stack with single-node Scylla from `compose.yml` + `compose.scylla.yml`.
- `make compose DATASTORE=scylla PROFILE=cluster` — run the full core stack with the three-node Scylla cluster from `compose.yml` + `compose.scylla.cluster.yml`.
- `make check TARGET=<all|config|compose|licenses|credentials>` — run the selected validation family. `compose` validates local-profile arguments and read-only mounts; `config` validates the selected config and RBAC policy; `all` runs every check family.
- `DETACH=1 make compose` — run the core Docker Compose stack in the background.
- `make scylla` — run only local single-node Scylla services from `compose.yml` + `compose.scylla.yml` (CI-friendly, `--smp=1`, replication factor 1).
- `make scylla PROFILE=cluster` — run only local three-node Scylla services from `compose.yml` + `compose.scylla.cluster.yml` (replication factor 3; tune per-node shards with `SCYLLA_CLUSTER_SMP`, default `1` for Docker Desktop compatibility).
- `DETACH=1 make scylla` and `DETACH=1 make compose DATASTORE=scylla` — run those compose targets in the background.
- `make ps`, `make logs`, `make stop`, `make down` — compose lifecycle helpers. Use `SERVICE=<name>` with `logs` or `stop` to scope the command; `SERVICE=scylla` includes both the single-node service and the three-node cluster services.
- `make add-user USERNAME=<user> PASSWORD=<password> [EMAIL=<email>] [DISPLAY_NAME=<name>] [USERS_FILE=<path>]` — add a local human identity to `users.yaml` using an atomic YAML-aware write; fails if the username already exists and reminds you to add its `policy.yaml` role binding.
- `make hash-user-password PASSWORD=<password>` — print a bcrypt hash for manual `users.yaml` maintenance.
- `make add-role ROLE=<role> ALLOW=<actions> [DENY=<actions>] [POLICY_FILE=<path>]` — add an RBAC role through an atomic YAML-aware write; comma-separate multiple actions.
- `make assign-role SUBJECT=<subject> ROLE=<role> [POLICY_FILE=<path>]` — idempotently bind an existing role to any authentication subject.
- `make secret TARGET=<jwt|grpc-hmac|signing-key> [DESTINATION_PATH=<path>]` — generate local authentication material. `jwt` updates `gitstore-api/.env`; `grpc-hmac` writes the same shared secret to both service `.env` files.
- `make bootstrap TARGET=<all|token|namespace|repository> [ADMIN_PASSWORD=<password>]` — authenticate/cache a token or create selected bootstrap resources. `repository` requires the namespace to exist.
- API schema preparation can run independently through `gitctl migrate --hosts <host:port,...> --keyspace <existing-keyspace> --timeout 5m`, including in an init container. Set `GITSTORE_API__DATASTORE__SCYLLA__AUTO_MIGRATE=false` on API replicas to require read-only migration-history validation at startup; the compatible default remains `true`. Data transfers are not part of this command.
- `make clean TARGET=<git-data|controller-checkpoints> CONFIRM=1` — remove only the selected local runtime state; never removes Docker volumes.
- `make build`, `make test`, `make lint`, `make check TARGET=all`, `make pr-ready` — aggregate development and PR readiness checks.
- `make test` defaults to the existing Rust/Go/shared-module suites, including local capacity regressions. Select existing specialized suites with `TARGET=datastore` (optionally `DATASTORE=scylla`) or `TARGET=secret-integration`. `make pr-ready` always keeps the full default test suite; capacity scenarios remain exclusively under `make capacity`.
- The Go build/test/lint aggregates include `shared/secretmaterial`; its dependency-free module also builds and tests independently with `GOWORK=off`.
- `make test TARGET=datastore` — run backend-neutral datastore contracts without an external Scylla instance.
- `make test TARGET=secret-integration SECRET_TEST_OWNED_DEPLOYMENT=1 SECRET_TEST_API_A=<base-url> SECRET_TEST_API_B=<base-url> SECRET_TEST_TOKEN_FILE=<path>` — run real controller bootstrap, isolated provider outage, enrolled-key overlap/retirement and process replacement against an isolated two-API deployment sharing Scylla. Both APIs must clamp ServiceAccount tokens to 60s. The existing `controllers/gitstore-controller-manager` identity must have controller RBAC permissions; optional `SECRET_TEST_NAMESPACE`/`SECRET_TEST_SERVICEACCOUNT` select another test identity. Only test-generated keys are enrolled/removed; controllers, private records and checkpoints are test-owned. Never point this at an operator deployment.
- `REPOSITORY_CAPACITY_SECRET_SCENARIO=1 make capacity TARGET=repository PROFILE=lifecycle MODE=<mode>` runs the connected File/fault/resource scenario only with `CHAOS_CONFIRM=1`, provisioned owned fixtures and an acknowledged Product manifest. The finalizer hashes/scans all closed artifacts; the Repository-only profile and local fixtures cannot substitute for a full production run.
- `make test TARGET=datastore DATASTORE=scylla SCYLLA_TEST_ADDR=<host:port>` — run tagged Scylla datastore contracts.
- `make capacity TARGET=<api|namespace|repository|scylla> PROFILE=<scenario> MODE=<diagnostic|alpha|production>` — the only public capacity interface. Valid target/profile pairs are `api/readiness`, `namespace/admission`, `namespace/validation`, `namespace/watch`, `namespace/recovery`, `repository/lifecycle`, and `scylla/soak`. Repository lifecycle is the real two-API/two-controller Git-admission, durable-watch, load, overflow, rolling-replacement, and recovery gate. Diagnostic evidence cannot pass a gate; alpha watch evidence enforces visibility p95 ≤2s and warns above the unchanged 1s production target; production enforces p95 ≤1s and p99 ≤3s. Set `CAPACITY_OBSERVABILITY=prometheus` and `CAPACITY_PROMETHEUS_TARGETS=<host:port,...>` to have the dispatcher manage an isolated scraper and export current-run API phase queries into the evidence bundle.
- The secret scenario exports `REPOSITORY_CAPACITY_SECRET_DATASET_MANIFEST` (absolute acknowledged Product JSONL path), `REPOSITORY_CAPACITY_SECRET_DATASET_NAMESPACE` (defaults to `REPOSITORY_CAPACITY_NAMESPACE`) and `REPOSITORY_CAPACITY_SECRET_DATASET_PAGE_SIZE` (default/maximum 250). It also requires `REPOSITORY_CAPACITY_SECRET_OWNED_DEPLOYMENT=1` and a private provisioned `REPOSITORY_CAPACITY_SECRET_FIXTURE_DIR` matching `CAPACITY_RUN_ID`; see `tests/capacity/README.md` for the manifest format, ownership and evidence limits.
- `make compose IDENTITY=oidc` — run the core stack together with the optional reference OIDC provider (Hydra + Kratos + `gitstore-oidc-bridge`), with the api's `oidc-jwt` provider auto-wired at the reference issuer. `make oidc` runs only the OIDC services. Lifecycle via the generic `make ps`/`make logs`/`make stop`/`make down` (pass `IDENTITY=oidc` to include the OIDC overlay; `SERVICE=oidc` then covers the whole stack). Requires OIDC secrets in the environment or an `.env` file (see `config/oidc/oidc.env.example` and `specs/059-optional-oidc-provider/quickstart.md`).

- Add `CAPACITY_PROMETHEUS_CONTROLLER_TARGETS=<host:port,...>` to an enabled capacity scraper to retain per-kind controller recovery, reconciliation, stall, queue, credential and process-identity time series. Secret capacity waits for list/watch recovery before baseline collection and requires both controllers' `ready` health field before load and acceptance.

Common bootstrap variables:
- `CONFIG_FILE ?= ./config/config.toml` (override with an explicit file such as `./config/config.stage.toml`)
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
- 063-implement-secret-adrs: Added Go 1.25 module baseline (API and controller); existing Rust Git service; current Go container builders use 1.26.1. + Existing gqlgen, validator, zap, Prometheus client, JWT/crypto and standard library; new repository-local `shared/secretmaterial` Go module, no new third-party dependency.
- 062-product-category-readiness: Added Go 1.25 (`gitstore-api`, `gitstore-controller-manager`) + `github.com/99designs/gqlgen v0.17.90` (schema/resolver codegen), existing `internal/graphqlclient.Client`, existing `internal/status.StatusPatch`/`StatusClient` (`gitstore-controller-manager`), existing `internal/cache.Cache[T]`/`CacheAccessor[T]` (spec 026), existing `internal/listwatch.Runner[T]` (spec 036/042), existing `internal/manager.Manager` reconciler registration (spec 026); no new external dependency in either service.
- 055-product-deletion-safety: Added Go 1.25 (`gitstore-api`, `gitstore-controller-manager`); Rust 1.x (`gitstore-git-service`) + Existing gqlgen v0.17.90, gocqlx/gocql, go-memdb, Git writer/catalog gRPC, `internal/watchjournal`, Scylla CDC, Prometheus, zap, controller ListWatcher/Runner/cache/status interfaces; no new dependency


<!-- MANUAL ADDITIONS START -->
## Development Guidelines

- The root `Makefile` is the canonical command interface for this repository. Future repo-level commands must be added to the root `Makefile` and documented in this file.
- Extend existing command families using selector flags (`TARGET`, `PROFILE`, `DATASTORE`, etc.) rather than adding feature-specific public Make targets. Reuse existing Compose files with parameterized configuration and mounts instead of adding feature-specific overlays.
- Reuse existing component, helper, test and runner files before introducing additional Go, shell, JSON, Rust or Compose files.

- Before creating a PR run:

  ```bash
  make pr-ready
  ```

- Install git hooks once per clone so staged Go/Rust/TS/JS files are checked automatically:

  ```bash
  ./scripts/install-git-hooks.sh
  ```

- Use Conventional Commits. PR titles are CI-enforced Conventional Commits (`.github/workflows/pr-title-lint.yml`) since squash-merge makes the PR title the commit message Release Please parses.
- Releases are automated via Release Please (`.github/workflows/release-please.yml`); see [Release Process](docs/runbooks/release-process.md) for versioning scheme, graduation between alpha/beta/stable, and troubleshooting.
- `.github/workflows/cd.yml` publishes only the core stack (`api`, `controller-manager`, `git-service`). A new optional service's image (following `admin`, `oidc-bridge`) belongs in `.github/workflows/cd-optional.yml`, not `cd.yml` — an optional service failing to build must never block or gate the core release.
- After implementing a feature update the documentation in [`docs/`](docs/).
- `gitstore-dev/quickstart` hand-copies several things from this repo rather than sharing them, so it silently drifts when any of the following change here: `compose.yml` (base services, ports, dependencies, health checks, networks, volumes, primary `ghcr.io` images), `compose.local.yml`/`compose.scylla.yml`/`compose.scylla.cluster.yml`/`compose.oidc.yml` (overlay wiring, env vars, images), the `config.toml` schema (section/key names, defaults), `users.yaml`/`policy.yaml` formats, the OIDC reference stack (`config/oidc/hydra/config.yaml`, `config/oidc/kratos/kratos.yml`, `identity.schema.json`), or the `ghcr.io` image list/tagging convention. Flag any PR touching those for a follow-up update on the quickstart side.
- For `gitstore-api` and `gitstore-controller-manager`, plans and tests must
  cover concurrent-replica correctness and rolling upgrades. These are
  requirements to verify per path, not blanket claims of implemented HA.
  All core-service changes must cover pluggable AuthN/AuthZ, bounded work and
  sustained Git push or reconciliation load where applicable.
- **Git service is stateful and singleton-only: exactly one active process per
  deployment.** Repository sharding and placement-aware routing are not
  implemented. Neither separate repository volumes nor a shared volume makes
  multiple Git instances supported. Do not add Git replication/autoscaling/HA
  requirements to specs, plans, tasks or capacity gates unless that work is
  explicitly approved feature scope. Use retained storage and non-overlapping
  Git replacement; do not claim zero-downtime Git failover. See constitution
  Principle VIII and `docs/architecture/README.md`.
- Never resolve a count/sum/other aggregate over a set of related resources
  (e.g. products in a category, variants for a product, inventory totals) with
  a live datastore query in a `gitstore-api` GraphQL resolver — ScyllaDB has no
  cheap cross-partition aggregate, by design. Materialize it asynchronously via
  a `gitstore-controller-manager` reconciler into `status.resolved`, per
  [ADR 0017](docs/ADRs/0017-aggregate-fields-via-async-materialization.md).

## Tool Usage

- Prefer editor-based tools for file operations (read/edit/create/move) and reserve terminal commands primarily for build, lint, and test workflows.
<!-- MANUAL ADDITIONS END -->

<!-- SPECKIT START -->
For additional context about technologies to be used, project structure,
shell commands, and other important information, read the current plan
at specs/063-implement-secret-adrs/plan.md
<!-- SPECKIT END -->

## graphify

This project has a knowledge graph at graphify-out/ with god nodes, community structure, and cross-file relationships.

Rules:
- For codebase questions, first run `graphify query "<question>"` when graphify-out/graph.json exists. Use `graphify path "<A>" "<B>"` for relationships and `graphify explain "<concept>"` for focused concepts. These return a scoped subgraph, usually much smaller than GRAPH_REPORT.md or raw grep output.
- If graphify-out/wiki/index.md exists, use it for broad navigation instead of raw source browsing.
- Read graphify-out/GRAPH_REPORT.md only for broad architecture review or when query/path/explain do not surface enough context.
- After modifying code, run `graphify update .` to keep the graph current (AST-only, no API cost).
