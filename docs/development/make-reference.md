# Make Command Reference

Run commands from the repository root. The [root Makefile](../../Makefile) is
the canonical interface; use its public targets rather than underscore-prefixed
implementation helpers.

```bash
make help                         # Available targets and common variables
make                              # Same as make help; starts no services
```

Selectors belong to a command family: `TARGET=datastore` selects tests, while
`TARGET=proto` selects generation. Supply selectors explicitly for `generate`,
`check`, `bootstrap`, `secret`, `clean` and `capacity`; there is no shared
`TARGET=all` meaning across families. Invoke different families separately rather
than sharing one `TARGET` across several goals.

## Prerequisites

- Native build, test and lint workflows need Git, Make, Go matching
  [go.work](../../go.work), and Rust matching
  [rust-toolchain.toml](../../rust-toolchain.toml), including rustfmt and Clippy.
  They cover the Rust Git service and the Go API, controller manager, OIDC
  bridge, secret-material, observability and CDC modules, not the admin frontend.
- Docker with the Compose plugin is required for container workflows and Compose
  validation. Bootstrap needs `curl` and `jq`; Compose validation also needs `jq`.
- Generation needs the appropriate local tools. Protobuf generation uses Buf and
  local Go/Rust plugins; see the
  [generator installation instructions](../developer-guide.md#protobuf).
- Dependency downloads may require network access. In particular, `make lint`
  installs `staticcheck@latest` before running it; it is not an offline-only check.

## Everyday development

| Command | Behavior and useful options |
| --- | --- |
| `make dev` | Generate/enroll local credentials and run all three native core services in the foreground, with shutdown trapping. Configure required API/Git secrets first. |
| `make git` | Run the native Git service; `GIT_DATA_DIR` defaults to `.gitstore/repos`. |
| `make api` | Run the native API with its `.env` or environment configuration. Generate its separate issuer signing key if absent. |
| `make controller` | Run the native controller using an existing signing record and enrolled UID; `CONTROLLER_CHECKPOINT_DIR` defaults to `.gitstore/checkpoints`. |
| `make compose` | Validate local config, then build/start all core services using shared `CONFIG_FILE=./config/config.toml`; no service `.env` files required. Default datastore is `memdb`. |
| `make scylla` | Start only Scylla services. Default `PROFILE=single`; `PROFILE=cluster` selects three nodes, with `SCYLLA_CLUSTER_SMP=1` per node by default. |
| `make oidc` | Build/start only the optional reference OIDC stack. Required secrets are listed in [the environment example](../../config/oidc/oidc.env.example). |
| `make ps` | Show Compose service status. |
| `make logs SERVICE=api` | Follow logs; omit `SERVICE` for all services. |
| `make stop SERVICE=api` | Stop the selected service; omit `SERVICE` to stop all. |
| `make down` | Stop/remove Compose services and networks, retaining volumes. |

Choose native or container startup; do not run both against the same ports or
storage. The Git service supports exactly one active process per deployment.
Never share a controller checkpoint directory between running processes.

```bash
make compose DETACH=1
make compose DATASTORE=scylla PROFILE=cluster DETACH=1
make compose CONFIG_FILE=./config/config.stage.toml DETACH=1
```

These are alternative startup choices. `DETACH=1` backgrounds Compose startup;
without it startup remains in the foreground. `COMPOSE_BAKE=true` is the default
Compose build setting. Add `IDENTITY=oidc` to `make compose` to include the
reference identity stack, and pass it to lifecycle commands for that stack too.
`SERVICE=scylla` groups all Scylla variants; `SERVICE=oidc` groups the OIDC stack
when used with `IDENTITY=oidc`. `SERVICE` scopes `logs` and `stop`, not `down`.

For separately started native services, start Git/API, then:

```bash
make secret TARGET=signing-key
make enroll-controller-serviceaccount
make controller
```

See [local development and native credential setup](../developer-guide.md#local-development)
for API/Git environment prerequisites, enrollment credentials, and consistent
`CONTROLLER_*` overrides. `make dev` and `make compose` automate their own
credential generation/enrollment. Shipped local credentials are development-only.

## Build, test, lint and PR readiness

| Command | Scope |
| --- | --- |
| `make build` | Build the Rust service and all six Go service/shared modules. |
| `make test` | Default `TARGET=all`: check authorization catalog drift, run shell/contract checks and Rust tests, then Go module tests with race detection and coverage. Includes local capacity-contract regressions, not deployed capacity scenarios. |
| `make test TARGET=datastore` | Backend-neutral/memdb datastore contracts; no external database needed. |
| `make test TARGET=datastore DATASTORE=scylla SCYLLA_TEST_ADDR=127.0.0.1:9042` | Tagged datastore contracts against reachable Scylla; does not start Scylla. |
| `make lint` | Check Rust formatting/Clippy, Go formatting, `go vet` and Staticcheck. Reports formatting problems rather than rewriting source. |
| `make pr-ready` | Run lint, build, the full default test suite, license checks and credential checks before creating a PR. |

`build` and `lint` do not have component `TARGET` selectors. For a small change,
use the existing module-level commands in the
[developer guide](../developer-guide.md#tests-and-pr-readiness) for focused checks.
`pr-ready` always forces the full default tests even if `TARGET` is supplied;
it does **not** include config/Compose validation. Run `make check TARGET=all`
separately when those checks are needed.

The opt-in `make test TARGET=git-integration` and
`make test TARGET=secret-integration` modify disposable resources and require
explicitly owned deployments, not operator environments:

- Git integration requires `GIT_TEST_OWNED_DEPLOYMENT=1`, `GIT_TEST_URI` and
  `GIT_TEST_HMAC_SECRET_FILE`; add `DATASTORE=scylla` and `SCYLLA_TEST_ADDR`
  for the Scylla-backed deletion recovery path.
- Secret integration requires `SECRET_TEST_OWNED_DEPLOYMENT=1`,
  `SECRET_TEST_API_A`, `SECRET_TEST_API_B` and `SECRET_TEST_TOKEN_FILE`.
  See [secret-material development](secret-material.md) for topology and identity
  prerequisites.

## Generation and validation

```bash
make generate TARGET=authz         # Authorization catalogs
make generate TARGET=proto         # Committed Go/Rust protobuf stubs
make generate TARGET=all           # Protobuf, then Go generation (GraphQL + authz)
make check TARGET=all
```

Generation rewrites committed sources; review and include the resulting diff.
The generated authorization catalog check in `make test` and
`make check TARGET=all` rejects drift without rewriting files. For GraphQL-only
work, use [the schema generation workflow](../developer-guide.md#graphql).

| `make check TARGET=…` | What it checks |
| --- | --- |
| `config` | Selected configuration and RBAC policy via `gitctl validate-local-config`. Override `CONFIG_FILE` and `POLICY_FILE` as needed. |
| `compose` | Config validation plus rendered local-profile arguments and read-only/isolated credential mounts; does not start services. |
| `licenses` | Go/Rust/JS license headers, both whole-tree and relative to `DIFF_BASE=origin/main`; the base ref must exist locally. |
| `credentials` | Enrollment-output regressions and credential/log-leakage checks; includes focused Go tests. |
| `all` | All four families above plus the generated authorization catalog drift check. |

Checks can compile/run Go helpers or tests; `check` is not synonymous with a
documentation-only linter.

## Local fixtures, credentials and cleanup

These commands write files or call a live API. Verify paths and endpoints before
running them; keep private keys, tokens and `.env` files out of Git and logs.

| Command family | Selectors, inputs and effects |
| --- | --- |
| `make bootstrap TARGET=all` | Create the Namespace then Repository. Other selectors: `token`, `namespace`, `repository`; Repository-only requires an existing Namespace. Needs a running `API_URL` (default `http://localhost:4000/graphql`). |
| `make secret TARGET=jwt` | Update the API `.env` JWT secret. |
| `make secret TARGET=grpc-hmac` | Write the same newly generated RPC secret to API and Git-service `.env` files; coordinate both services when changing it. |
| `make secret TARGET=signing-key` | Create controller PEM + JSON record by default. Optional `DESTINATION_PATH`, `RECORD_OUTPUT_PATH`, `CONTROLLER_SERVICEACCOUNT_KEY_ID`; custom destinations default to `<DESTINATION_PATH>.json`, and `RECORD_OUTPUT_PATH=` requests PEM only. Existing keys/identical records are preserved; differing records are rejected. |
| `make add-user USERNAME=<name> PASSWORD='<password>'` | Atomically add to `USERS_FILE` (default `config/users.yaml`), rejecting duplicates. Optional `EMAIL`, `DISPLAY_NAME`. Does not grant a role. |
| `make hash-user-password PASSWORD='<password>'` | Print a bcrypt hash for manual user-file maintenance. |
| `make add-role ROLE=<name> ALLOW=<canonical-actions>` | Add an allow-only role in `POLICY_FILE` (default `config/policy.yaml`); `SCOPE=cluster` by default, or `namespace`. No `DENY` or legacy actions. |
| `make assign-role SUBJECT=<name> ROLE=<name>` | Add a typed binding; defaults `SUBJECT_KIND=User`, `ROLE_KIND=ClusterRole`, `SCOPE=cluster`. Namespace scope requires explicit `NAMESPACE`; subject kinds also include `Group`/`ServiceAccount`, role kinds also include `Role`. |
| `make clean TARGET=git-data CONFIRM=1` | Recursively delete only `GIT_DATA_DIR` (default `.gitstore/repos`). |
| `make clean TARGET=controller-checkpoints CONFIRM=1` | Recursively delete only `CONTROLLER_CHECKPOINT_DIR` (default `.gitstore/checkpoints`). |

Bootstrap resource defaults are `NAMESPACE=gitstore-test`, `REPOSITORY=catalog`,
`DEFAULT_BRANCH=main`, `NAMESPACE_DISPLAY_NAME='GitStore Test'` and
`NAMESPACE_TIER=ORGANIZATION`. Resource creation prefers `BOOTSTRAP_TOKEN`, then
the cached token (`BOOTSTRAP_TOKEN_CACHE=.gitstore/bootstrap-token`), otherwise
logs in using `ADMIN_USERNAME=admin` and required `ADMIN_PASSWORD`.
`TARGET=token` always requires `ADMIN_PASSWORD`, prints the token and caches it;
do not capture that output in shared logs.

Stop affected native processes before cleanup and inspect any directory
overrides. Cleanup requires `CONFIRM=1`, rejects unsafe paths and never removes
Docker volumes; it is not a general workspace reset.

## Capacity and fault testing

`make capacity TARGET=<subsystem> PROFILE=<scenario>
MODE=<diagnostic|alpha|production>` and `make chaos` are opt-in, not substitutes
for `pr-ready`. They can generate sustained load, create resources or disrupt
containers. Here `PROFILE` selects a scenario, not the Scylla startup topology.
Use the dedicated [capacity](../../tests/capacity/README.md) and
[chaos](../../tests/chaos/README.md) guides for supported combinations, explicit
ownership/confirmation requirements and evidence options before running them.
