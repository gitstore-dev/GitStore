# Workspace commands, configuration and helper instructions

These instructions also apply when changing the root `Makefile`, Compose
files, container wiring or configuration. Use the root Makefile interface,
existing selector families and existing helpers. For capacity/chaos helpers
also read `../tests/capacity/AGENTS.md`; detailed load and profiling commands
belong there.

## Local startup

- `make git` runs the Git service in the foreground with `GIT_DATA_DIR`
  (default `.gitstore/repos`).
- `make api` runs the API in the foreground; required auth secrets come from
  `gitstore-api/.env` or the environment.
- `make controller` runs the controller in the foreground on port 5001 using
  an existing signing JSON record and enrolled UID. First run
  `make secret TARGET=signing-key` and
  `make enroll-controller-serviceaccount`. Set
  `GITSTORE_CONTROLLER__API_URI` to the running API
  (default `http://localhost:4000/graphql`).
- `make dev` generates/enrolls local credentials and runs all three native
  core services in the foreground with shutdown trapping.

## Compose and configuration

- `make compose` runs the three core services using the `local` profile and
  shared `CONFIG_FILE` (default `./config/config.toml`), in the foreground
  with memdb. Service `.env` files are not required.
- Add `DATASTORE=scylla` for the single-node stack from `compose.yml` and
  `compose.scylla.yml`; add `PROFILE=cluster` for three nodes using
  `compose.scylla.cluster.yml`.
- `make scylla` runs only single-node Scylla (CI-friendly, `--smp=1`, RF1).
  `make scylla PROFILE=cluster` runs three nodes at RF3; per-node
  `SCYLLA_CLUSTER_SMP` defaults to `1` for Docker Desktop.
- `SCYLLA_TABLETS_PER_SHARD_GOAL=<positive-integer>` selects the shared
  native tablet budget (default `100`). Measure achieved allocation rather
  than claiming the requested value is an achieved CDC stream count.
- `DETACH=1` backgrounds the Compose or Scylla targets.
- `make ps`, `make logs`, `make stop`, `make down` provide lifecycle actions.
  `SERVICE=<name>` scopes logs/stop; `SERVICE=scylla` includes single-node
  and cluster services.
- `make check TARGET=<all|config|compose|licenses|credentials>` selects checks.
  Compose checks local-profile arguments and read-only mounts; config checks
  the selected configuration and RBAC policy.
- `make compose IDENTITY=oidc` adds Hydra, Kratos and `gitstore-oidc-bridge`,
  auto-wiring the API's `oidc-jwt` issuer. `make oidc` runs only OIDC services.
  Pass `IDENTITY=oidc` for generic lifecycle actions; `SERVICE=oidc` covers
  the entire OIDC stack. Secrets come from environment or `.env`; see
  `../config/oidc/oidc.env.example` and the OIDC feature quickstart.
- Preserve compatibility with `gitstore-dev/quickstart`, which hand-copies
  Compose base/overlay wiring, ports, dependencies, health checks, networks,
  volumes, image names/tags, config schema/defaults, user/policy formats and
  OIDC reference configuration. Flag affected PRs for a quickstart follow-up.

## Identity and bootstrap

- `make enroll-controller-serviceaccount` enrolls the existing public key at
  `API_URL` using local `DEV_ADMIN_USERNAME`/`DEV_ADMIN_PASSWORD` and saves
  the UID to `CONTROLLER_SERVICEACCOUNT_IDENTITY_FILE`. It does not generate
  private material or JSON. Keep `CONTROLLER_*` overrides consistent across
  generation/startup; no namespace is accepted.
- `make add-user USERNAME=<user> PASSWORD=<password> [EMAIL=<email>] [DISPLAY_NAME=<name>] [USERS_FILE=<path>]`
  performs an atomic YAML-aware add, rejects existing usernames and reminds
  the caller to bind a role in `policy.yaml`.
- `make hash-user-password PASSWORD=<password>` prints a bcrypt hash for
  manual `users.yaml` maintenance.
- `make add-role ROLE=<role> ALLOW=<canonical-actions> [SCOPE=cluster|namespace] [POLICY_FILE=<path>]`
  atomically adds a v1beta1 ClusterRole (default) or Role. Reject DENY and
  legacy actions.
- `make assign-role SUBJECT=<name> ROLE=<role> [SUBJECT_KIND=User|Group|ServiceAccount] [ROLE_KIND=ClusterRole|Role] [SCOPE=cluster|namespace] [NAMESPACE=<explicit-binding-namespace>] [POLICY_FILE=<path>]`
  idempotently adds a typed binding. Namespace scope requires `NAMESPACE`.
- `make secret TARGET=<jwt|grpc-hmac|signing-key> [DESTINATION_PATH=<path>] [RECORD_OUTPUT_PATH=<path>]`
  generates local authentication material. JWT updates the API `.env`;
  grpc-hmac writes the same secret to both services. Signing-key defaults
  to the controller PEM plus JSON with `CONTROLLER_SERVICEACCOUNT_KEY_ID`;
  custom destinations default to `<DESTINATION_PATH>.json`.
  `RECORD_OUTPUT_PATH=` means PEM only. Preserve existing keys and identical
  records; reject differing records.
- `make bootstrap TARGET=<all|token|namespace|repository> [ADMIN_PASSWORD=<password>]`
  authenticates/caches a token or creates bootstrap resources. Repository
  bootstrap requires the Namespace to exist.
- Common defaults: `CONFIG_FILE=./config/config.toml`,
  `API_URL=http://localhost:4000/graphql`, `ADMIN_USERNAME=admin`,
  `NAMESPACE=gitstore-test`, `NAMESPACE_DISPLAY_NAME=GitStore Test`,
  `NAMESPACE_TIER=ORGANIZATION`, `REPOSITORY=catalog`, `DEFAULT_BRANCH=main`.
  `ADMIN_PASSWORD` is required unless `BOOTSTRAP_TOKEN` or a cached token is
  available. `BOOTSTRAP_TOKEN` overrides login/cache lookup.

## Schema preparation and cleanup

- `gitctl migrate --hosts <host:port,...> --keyspace <existing-keyspace> --timeout 5m`
  prepares schema independently, including from init containers.
  `GITSTORE_API__DATASTORE__SCYLLA__AUTO_MIGRATE=false` makes API startup
  validate migration history read-only; the compatible default is `true`.
  This command does not transfer data.
- `make clean TARGET=<git-data|controller-checkpoints> CONFIRM=1` removes
  only selected local runtime state, never Docker volumes.

## Infrastructure provisioning

- `make bootstrap TARGET=infrastructure PROFILE=digitalocean ACTION=<init|plan|show|apply|output>`
  operates `deploy/digitalocean`. The default action is `plan`; `apply` consumes
  the saved plan and does not prompt. It is never called by `check` or `plan`.
- `init` requires `INFRA_BACKEND_CONFIG`; `plan` requires `INFRA_VAR_FILE`.
  Supply absolute paths to private files. Keep credentials in environment
  variables, not command arguments, cloud-init or versioned configuration.
- `make check TARGET=infrastructure PROFILE=digitalocean` runs formatting,
  backend-disabled initialization, validation and mocked topology tests. It is
  opt-in because it requires OpenTofu and the pinned provider download. Set
  `TOFU_BIN` to override the executable.
- Provisioning installs Docker and prepares storage, not application containers.
  Read `../docs/runbooks/digitalocean-provisioning.md` for runtime handoff,
  networking, state protection and non-overlapping recovery requirements.
