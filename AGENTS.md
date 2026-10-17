# gitstore Development Guidelines

Auto-generated from all feature plans. Last updated: 2026-03-26

## Active Technologies

- Spec 064 planning uses the current Go 1.26.0 API/controller modules and existing Rust stack; no new third-party dependencies. It plans bounded v1beta1 RBAC snapshots and direct replacement of the undeployed ServiceAccount baseline with cluster-scoped storage; these changes are not implemented yet.

Baseline stack, established in early specs (025-035) and reused unchanged by every later spec unless a delta is called out below:
- **Go** 1.26.0 (`gitstore-api`, `gitstore-controller-manager`; container builders on 1.26.1) — `gqlgen v0.17.90` (GraphQL schema/resolver codegen + `transport.Websocket` subscriptions), `go-playground/validator/v10`, `go.uber.org/zap`, `google/uuid`, `encoding/json`, `github.com/prometheus/client_golang v1.23.2`, `github.com/google/cel-go/cel` (admission rule evaluation), `golang-jwt/v5 v5.3.1`, `golang.org/x/crypto` (bcrypt), `github.com/spf13/viper`.
- **Controller manager** adds: `golang.org/x/time` (queue rate limiting), `github.com/alitto/pond/v2 v2.7.1` (worker pools), `github.com/cenkalti/backoff/v5 v5.0.3` (retry/backoff), `github.com/syndtr/goleveldb v1.0.0` (bounded disk-backed controller projections, work and indexes).
- **Rust** ≥1.88 (`gitstore-git-service`) — `gix 0.89.0` (+ `gix-ref 0.69.0`, `gix-pack 0.76.0`), `tokio 1.35`, `tonic 0.14`, `tracing 0.1`, `anyhow 1.0`, `async-trait 0.1`, `serde 1.0`; signed RPC uses `hmac 0.13`, `sha2 0.11` and `getrandom 0.4`.
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

Deltas not covered above: 028 (branch-deletion admission, no new Go deps), 029 (`config 0.15.22`, `regex 1`, already in Rust `Cargo.toml`), 033 (`cmd/gitctl` replaces `cmd/hashpw`), 035 (`github.com/gin-gonic/gin`, `go-grpc-prometheus`; push-policy fields on `datastore.Repository`), 048 (Scylla query-specific denormalized tables, `go-memdb` as dev/contract-test backend). 057 (no new deps; Scylla projection `category_ancestor_index` in migration `010`, maintained by `UpdateCategoryTaxonomyStatus`/final removal and repaired via `gitctl scylla-projection-audit|repair`; shared `internal/admission` diagnostics/`RejectedError` and `internal/admissionreport` response extension).

## Commands

### Workspace
- `make generate TARGET=<authz|proto|all>` — regenerate committed authorization catalogs, Go/Rust protobuf stubs using local generators, or all generated sources. `make test` and `make check TARGET=all` reject catalog drift without rewriting files.
- `make help` — list root commands and common variables.
- `make git` — run `gitstore-git-service` locally in the foreground using `GIT_DATA_DIR` (default: `.gitstore/repos`).
- `make api` — run `gitstore-api` locally in the foreground. Requires `gitstore-api/.env` or shell env for required auth secrets.
- `make controller` — run `gitstore-controller-manager` locally in the foreground on port 5001 using an existing signing JSON record and enrolled UID. First run `make secret TARGET=signing-key` and `make enroll-controller-serviceaccount`. Requires `GITSTORE_CONTROLLER__API_URI` pointing at a running API (default: `http://localhost:4000/graphql`).
- `make enroll-controller-serviceaccount` — enroll the existing controller public key with `API_URL` using local `DEV_ADMIN_USERNAME`/`DEV_ADMIN_PASSWORD`, and save the assigned UID to `CONTROLLER_SERVICEACCOUNT_IDENTITY_FILE`. Does not generate private material or JSON. Keep `CONTROLLER_*` overrides consistent with generation/startup; no namespace is accepted.
- `make dev` — generate/enroll local credentials and run all three native core services in the foreground with shutdown trapping.
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
- `make add-role ROLE=<role> ALLOW=<canonical-actions> [SCOPE=cluster|namespace] [POLICY_FILE=<path>]` — atomically add a v1beta1 ClusterRole (default) or Role. DENY and legacy actions are rejected.
- `make assign-role SUBJECT=<name> ROLE=<role> [SUBJECT_KIND=User|Group|ServiceAccount] [ROLE_KIND=ClusterRole|Role] [SCOPE=cluster|namespace] [NAMESPACE=<explicit-binding-namespace>] [POLICY_FILE=<path>]` — idempotently add a typed binding; namespace scope requires an explicit NAMESPACE.
- `make secret TARGET=<jwt|grpc-hmac|signing-key> [DESTINATION_PATH=<path>] [RECORD_OUTPUT_PATH=<path>]` — generate local authentication material. `jwt` updates `gitstore-api/.env`; `grpc-hmac` writes the same shared secret to both service `.env` files. `signing-key` defaults to the native controller PEM plus JSON record using `CONTROLLER_SERVICEACCOUNT_KEY_ID`; custom destinations default to `<DESTINATION_PATH>.json`. Set `RECORD_OUTPUT_PATH=` for PEM only. Existing keys/identical records are preserved; differing records are rejected.
- `make bootstrap TARGET=<all|token|namespace|repository> [ADMIN_PASSWORD=<password>]` — authenticate/cache a token or create selected bootstrap resources. `repository` requires the namespace to exist.
- API schema preparation can run independently through `gitctl migrate --hosts <host:port,...> --keyspace <existing-keyspace> --timeout 5m`, including in an init container. Set `GITSTORE_API__DATASTORE__SCYLLA__AUTO_MIGRATE=false` on API replicas to require read-only migration-history validation at startup; the compatible default remains `true`. Data transfers are not part of this command.
- `make clean TARGET=<git-data|controller-checkpoints> CONFIRM=1` — remove only the selected local runtime state; never removes Docker volumes.
- `make build`, `make test`, `make lint`, `make check TARGET=all`, `make pr-ready` — aggregate development and PR readiness checks.
- `make test` defaults to the existing Rust/Go/shared-module suites, including local capacity regressions. Select existing specialized suites with `TARGET=datastore` (optionally `DATASTORE=scylla`) or `TARGET=secret-integration`. `make pr-ready` always keeps the full default test suite; capacity scenarios remain exclusively under `make capacity`.
- The Go build/test/lint aggregates include `shared/secretmaterial`; its dependency-free module also builds and tests independently with `GOWORK=off`.
- API and controller-manager telemetry share `shared/observability`: configuration validation, bounded OTLP trace/log export, Zap bridge, Prometheus SDK reader and trace-context HTTP helpers. Both use fixed service identities; controller spans cover each reconciliation attempt, HTTP request and WebSocket dial, not entire watch lifetimes. Keep native Prometheus metrics unchanged. The module participates in existing Go build/test/lint families and standalone `GOWORK=off` CI checks.
- The Go build/test/lint aggregates also include `shared/scylla-cdc-go`, a production-source copy of the existing v1.2.1 dependency with a minimal reader-shutdown patch. Repository-local library modules belong under `shared/`; top-level Go modules are standalone services or tooling. The API uses the same local replacement in native and container builds; preserve its Apache-2.0 license and provenance in `NOTICE.md`.
- `make test TARGET=datastore` — run backend-neutral datastore contracts without an external Scylla instance.
- `make test TARGET=git-integration GIT_TEST_OWNED_DEPLOYMENT=1 GIT_TEST_URI=<host:port> GIT_TEST_HMAC_SECRET_FILE=<private-file>` — run race-enabled signed native Git deletion-response recovery against an explicitly owned singleton. Creates/removes only disposable UUID repositories; requires the existing RPC signing material, never an operator deployment. The response-loss interceptor discards a successful native reply before the caller sees it; it is not a network outage, server replacement, API/datastore saga or sustained-load acceptance.
- Add `DATASTORE=scylla SCYLLA_TEST_ADDR=<host:port>` to the native Git suite to exercise Repository/Namespace finalizer sagas through two independent in-process API services and Scylla sessions, with actual signed Git removal. Uses an isolated test-owned keyspace and random storage UUIDs. Covers lost Git replies, partial projection cleanup, pre-authoritative failure, lost authoritative replies and stale-UID same-name recreation. The API-side Git registry is the existing allow-all test provider; this is not deployed HTTP/RBAC, a physical network fault or sustained-load acceptance.
- `make chaos CHAOS_PROFILE=api-invalid-policy-reload CHAOS_TARGET=<container> CHAOS_COMPOSE_PROJECT=<owned-project> CHAOS_POLICY_DIR=<absolute-private-directory> CHAOS_CONFIRM=1` — reject malformed policy on one explicitly owned API and restore the original file and active snapshot. Requires a dedicated read-only directory bind and matching `owned-policy.json`; see `tests/chaos/README.md`. Set `CAPACITY_CHAOS_PROFILE=api-invalid-policy-reload` and `CAPACITY_CHAOS_POLICY_DIR` with the existing capacity fault selectors to run inside a declared k6 or Go Repository lifecycle load window. Other Go capacity profiles and overlapping the full secret scenario's managed faults are rejected.
- `make capacity TARGET=repository PROFILE=lifecycle MODE=diagnostic REPOSITORY_CAPACITY_WORKLOAD=<repository|file|combined> REPOSITORY_CAPACITY_SKIP_REPLACEMENT=1` — compare controlled offered loads on a provisioned owned deployment with equally seeded Repository/File pools. Requires the existing private run-matched fixture, endpoint/token/Git settings. No full secret scenario, replay, scheduled faults or alpha/production acceptance.
- Add `REPOSITORY_CAPACITY_TRACE_CORRELATION=1` to File/combined comparisons or the full secret scenario to attach a fresh sampled W3C parent to each File push and export its `traceID` in bounded timelines. Default is off; Repository-only selections are rejected. Headers remain command-scoped alongside bearer authentication, with no new module/dependency. This correlates instrumented Git HTTP/admission/RPC spans, not client queue/provider work or shared projection batches; verify actual trace/log/exemplar joins against the deployed backends.
- Add `REPOSITORY_CAPACITY_CLIENT_TIMING=1` to diagnostic File/combined comparisons for bounded command wall/CPU and in-memory Trace2 startup/HTTP-helper timestamps in the existing File timelines. Default is off; full scenarios, Repository-only and alpha/production runs reject it. Raw trace events, argv, paths and configuration are never exported. This adds measurement overhead, not a new acceptance mode or a change to workers, checks or deadlines.
- `make capacity TARGET=repository PROFILE=lifecycle MODE=diagnostic CAPACITY_STACK_ACTION=<up|resume|wait|token|verify-owned|down|config>` — manage an explicitly named owned two-API/two-controller/singleton-Git/three-Scylla diagnostic deployment through the existing capacity stack helper. `up` refuses existing project containers/volumes; `resume` only completes owned secret startup interrupted before private fixture provisioning; `down` stops only verified owned services and retains volumes.
- Add `REPOSITORY_CAPACITY_VERIFY_STORAGE=1 REPOSITORY_CAPACITY_PROJECT=<owned-project>` to Repository lifecycle diagnostics for read-only UID-bound Git directory/tombstone checks through the owned singleton container. Alpha/production default this on and reject disabled inspection. Inspection preserves all files, receipts, tombstones and volumes; it does not replace the remaining deletion/fault/capacity gates.
- Add `REPOSITORY_CAPACITY_VERIFY_FINALIZERS=1 CAPACITY_SCYLLA_KEYSPACE=<owned-keyspace>` alongside physical-storage verification to exercise all five catalog kinds as simulated terminating blockers, plus real API/controller-initiated Namespace, system Repository and ordinary Repository termination held by an unrelated finalizer. Alpha/production require this check and default it on. The test-only fixture uses bounded exact-UID/resource-version CQL CAS against newly created probe records and verifies both owned APIs use the selected keyspace. Catalog simulations are restored before real Git removal; parent probes add/remove only their own finalizer without changing deletion intent, timestamp or generation. Both APIs must preserve held storage and reject completion; terminating ordinary Repositories must continue blocking Namespace deletion. These probes do not establish real catalog-child deletion initiation or production acceptance.
- `make capacity TARGET=api PROFILE=authorization MODE=<diagnostic|alpha|production>` — sample bounded per-case authorization latency at the maximum policy dimensions and concurrency 1/8/32, then exercise concurrent valid/rejected policy reloads for 30m with 32 decision workers. Export the evaluator call-graph dependency audit, latency histograms and post-GC reload heap evidence. `AUTHZ_CAPACITY_WARMUP`, `AUTHZ_CAPACITY_DURATION` and `AUTHZ_CAPACITY_RELOAD_DURATION` may shorten diagnostic exploration only. Alpha/production require clean unchanged source, matching runtime manifests, per-case p99 <1ms, complete durations and post-GC heap ≤110%; this evaluator-only gate does not replace deployed readiness, replica semantics or lifecycle gates.
- `make test TARGET=secret-integration SECRET_TEST_OWNED_DEPLOYMENT=1 SECRET_TEST_API_A=<base-url> SECRET_TEST_API_B=<base-url> SECRET_TEST_TOKEN_FILE=<path>` — run real controller bootstrap, isolated provider outage, enrolled-key overlap/retirement and process replacement against an isolated two-API deployment sharing Scylla. Both APIs must clamp ServiceAccount tokens to 60s. The existing cluster-scoped `gitstore-controller-manager` identity must have controller RBAC permissions; optional `SECRET_TEST_SERVICEACCOUNT` selects another test identity; SECRET_TEST_NAMESPACE is rejected. Only test-generated keys are enrolled/removed; controllers, private records and checkpoints are test-owned. Never point this at an operator deployment.
- Add `SECRET_TEST_REINCARNATION_API=<disposable-issuer-base-url>` and `SECRET_TEST_REINCARNATION_SERVICEACCOUNT=<initially-absent-test-name>` together to that suite to verify still-unexpired tokens fail after same-name/same-key recreation on both unchanged target APIs. The disposable issuer shares their datastore/signing material and alone grants token issuance for the test name; keep that name unbound on the targets. The test creates/deletes only this new identity, never the enrolled controller. Without these flags the suite explicitly reports that this access-token case was not exercised.
- `REPOSITORY_CAPACITY_SECRET_SCENARIO=1 make capacity TARGET=repository PROFILE=lifecycle MODE=<mode>` runs the connected File/fault/resource scenario only with `CHAOS_CONFIRM=1`, provisioned owned fixtures and an acknowledged Product manifest. The finalizer hashes/scans all closed artifacts; the Repository-only profile and local fixtures cannot substitute for a full production run.
- `make test TARGET=datastore DATASTORE=scylla SCYLLA_TEST_ADDR=<host:port>` — run tagged Scylla datastore contracts.
- `make capacity TARGET=<api|namespace|repository|category|scylla> PROFILE=<scenario> MODE=<diagnostic|alpha|production>` — the only public capacity interface. Valid target/profile pairs are `api/readiness`, `namespace/admission`, `namespace/validation`, `namespace/watch`, `namespace/recovery`, `repository/lifecycle`, `category/hierarchy` (spec 057: 10,000-descendant subtree, mutations plus filtered pages, re-parent cascade, SC-007/SC-009 verifier; needs a companion Git push driver, see `tests/capacity/README.md`), and `scylla/soak`. Repository lifecycle is the real two-API/two-controller Git-admission, durable-watch, load, overflow, rolling-replacement, and recovery gate. Diagnostic evidence cannot pass a gate; alpha watch evidence enforces visibility p95 ≤2s and warns above the unchanged 1s production target; production enforces p95 ≤1s and p99 ≤3s. Set `CAPACITY_OBSERVABILITY=prometheus` and `CAPACITY_PROMETHEUS_TARGETS=<host:port,...>` to have the dispatcher manage an isolated scraper and export current-run API phase queries into the evidence bundle.
- The secret scenario exports `REPOSITORY_CAPACITY_SECRET_DATASET_MANIFEST` (absolute acknowledged Product JSONL path), `REPOSITORY_CAPACITY_SECRET_DATASET_NAMESPACE` (defaults to `REPOSITORY_CAPACITY_NAMESPACE`) and `REPOSITORY_CAPACITY_SECRET_DATASET_PAGE_SIZE` (default/maximum 250). It also requires `REPOSITORY_CAPACITY_SECRET_OWNED_DEPLOYMENT=1` and a private provisioned `REPOSITORY_CAPACITY_SECRET_FIXTURE_DIR` matching `CAPACITY_RUN_ID`; see `tests/capacity/README.md` for the manifest format, ownership and evidence limits.
- `make compose IDENTITY=oidc` — run the core stack together with the optional reference OIDC provider (Hydra + Kratos + `gitstore-oidc-bridge`), with the api's `oidc-jwt` provider auto-wired at the reference issuer. `make oidc` runs only the OIDC services. Lifecycle via the generic `make ps`/`make logs`/`make stop`/`make down` (pass `IDENTITY=oidc` to include the OIDC overlay; `SERVICE=oidc` then covers the whole stack). Requires OIDC secrets in the environment or an `.env` file (see `config/oidc/oidc.env.example` and `specs/059-optional-oidc-provider/quickstart.md`).

- Add `CAPACITY_PROMETHEUS_CONTROLLER_TARGETS=<host:port,...>` to an enabled capacity scraper to retain per-kind controller recovery, reconciliation, stall, queue, credential and process-identity time series. Secret capacity waits for list/watch recovery before baseline collection and requires both controllers' `ready` health field before load and acceptance.
- Add `CAPACITY_PROMETHEUS_SCYLLA_TARGETS=<host:9180,...>` to retain separate server CAS, contention/timeouts, per-shard utilization/scheduling and transport histories. Every configured node must supply finite CAS-count/utilization history and an up sample; missing history fails export. Raw values keep server-native units and are not individual request execution times.
- `CAPACITY_OBSERVABILITY=full make capacity TARGET=repository PROFILE=lifecycle MODE=diagnostic ...` reuses `compose.capacity.yml` for local Prometheus/OTLP/Tempo/Loki, bounded trace/phase-log exports and retained histories. Core telemetry uses the existing typed `observability.otlp.uri` setting; configure capacity processes with `CAPACITY_OTLP_URI`, `CAPACITY_TRACE_SAMPLE_RATIO`, `CAPACITY_API_PROFILE_URI` and `CAPACITY_GIT_METRICS_URI` before controlled replacement. Profiling/metrics values are full HTTP URIs (for example `http://127.0.0.1:6060` and `http://0.0.0.0:50052`). Add `CAPACITY_PROMETHEUS_GIT_TARGETS=capacity-git-service:50052` for native Git metrics; `CAPACITY_OBSERVABILITY_KEEP=1` retains running collectors for later queries. OTLP/HTTP metrics share collector port 4318 and are scraped by Prometheus on internal port 8889 under `gitstore-otlp-metrics-capacity`; existing direct application scrapes and collector self-telemetry on port 8888 remain unchanged.
- `CAPACITY_PROFILING=go CAPACITY_PROFILE_SECONDS=30 make capacity TARGET=repository PROFILE=lifecycle MODE=diagnostic ...` captures bounded CPU/heap/allocation/block/mutex/goroutine/execution profiles on both owned APIs during actual offered load. Requires the configured loopback profiler (default capture port 6060); never uses public profile ports. Profiled runs are diagnostics, never acceptance evidence; binary profiles are private and may contain runtime data. Native perf must be probed without privilege escalation.
- Add `CAPACITY_PROFILE_NATIVE_SAMPLES=16` to that owned Go diagnostic to sample at most sixteen File conditional exchanges per API execution capture (default zero, maximum 64). Only sampled File conditional exchanges consume the explicit loopback `sample_limit` budget; each retains at most four native trace IDs with existing retries unchanged. Their `scylla.server_trace` events identify exact native sessions; admission never reads trace tables. Native tracing adds overhead and cannot supply unprofiled acceptance.

Common bootstrap variables:
- `CONFIG_FILE ?= ./config/config.toml` (override with an explicit file such as `./config/config.stage.toml`)
- `API_URL ?= http://localhost:4000/graphql`
- `ADMIN_USERNAME ?= admin`
- `ADMIN_PASSWORD` is required unless `BOOTSTRAP_TOKEN` is provided or a cached bootstrap token exists.
- `BOOTSTRAP_TOKEN` overrides login/cached-token lookup.
- `NAMESPACE ?= gitstore-test`
- `NAMESPACE_DISPLAY_NAME ?= GitStore Test`
- `NAMESPACE_TIER ?= ORGANIZATION`
- `REPOSITORY ?= catalog`
- `DEFAULT_BRANCH ?= main`

## Code Style

: Follow standard conventions

## Recent Changes
- 064-authz-action-vocabulary: Planned canonical actions, allow-only v1beta1 RBAC, name-only ServiceAccounts, signed identity migration and replica/load validation. No implementation or new third-party dependency in the planning change.
- 057-categorytaxonomy-path-freshness: Added Go 1.25 (`gitstore-api`, `gitstore-controller-manager`); Rust 1.x (`gitstore-git-service`, golden-fixture parity test only) + Existing only: gqlgen v0.17.90, gocqlx/gocql, go-memdb, `internal/gitclient` Git writer gRPC, `internal/admission`, `cataloggrpc`, zap, Prometheus. No new third-party dependency.
- 063-implement-secret-adrs: Added Go 1.25 module baseline (API and controller); existing Rust Git service; current Go container builders use 1.26.1. + Existing gqlgen, validator, zap, Prometheus client, JWT/crypto and standard library; new repository-local `shared/secretmaterial` Go module, no new third-party dependency.


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
- ADRs under `docs/ADRs/` follow the status lifecycle in
  [`docs/ADRs/README.md`](docs/ADRs/README.md): a PR that implements an ADR's
  decision flips its `**Status**` line from `Proposed` to `Accepted
  (YYYY-MM-DD)` in that same PR, the same way a spec's Status flips to Closed.
  `/speckit.plan` and `/speckit.tasks` must include that flip as an explicit
  step whenever the plan/tasks implement an ADR's core decision. Superseding
  an ADR updates both the superseded ADR's status line and the
  `docs/ADRs/README.md` index in the same PR.

## Tool Usage

- Prefer editor-based tools for file operations (read/edit/create/move) and reserve terminal commands primarily for build, lint, and test workflows.
<!-- MANUAL ADDITIONS END -->

<!-- SPECKIT START -->
For additional context about technologies to be used, project structure,
shell commands, and other important information, read the current plan
at specs/064-authz-action-vocabulary/plan.md
<!-- SPECKIT END -->

## graphify

This project has a knowledge graph at graphify-out/ with god nodes, community structure, and cross-file relationships.

Rules:
- For codebase questions, first run `graphify query "<question>"` when graphify-out/graph.json exists. Use `graphify path "<A>" "<B>"` for relationships and `graphify explain "<concept>"` for focused concepts. These return a scoped subgraph, usually much smaller than GRAPH_REPORT.md or raw grep output.
- If graphify-out/wiki/index.md exists, use it for broad navigation instead of raw source browsing.
- Read graphify-out/GRAPH_REPORT.md only for broad architecture review or when query/path/explain do not surface enough context.
- After modifying code, run `graphify update .` to keep the graph current (AST-only, no API cost).
