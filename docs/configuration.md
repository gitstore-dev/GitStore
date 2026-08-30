# Configuration Reference

This document is the operator reference for configuring `gitstore-api`, `gitstore-git-service`, and `gitstore-controller-manager`.

---

## Source Precedence

Services load configuration from multiple sources in a fixed order. A higher-priority source overrides a lower-priority one:

```
1. Hard-coded defaults          (lowest priority)
2. Config file                  (optional)
3. .env file                    (optional)
4. Environment variables        (highest priority)
5. CLI value overrides such as `--log-level` (where supported)
```

All three services accept `--config-file <path>`. The flag selects the file
source; values from the environment still override values in that file. An
explicit path is required to exist and be readable. Without the flag, the Go
services retain optional `config.toml` discovery and the Git service retains
optional `gitstore.toml` discovery in the working directory.

## Shared Local Compose Configuration

`make compose` activates the Compose `local` profile and mounts
`config/config.toml` read-only into all three core containers. The API also
receives the development RBAC policy at `config/policy.yaml`. Select another
host-side file explicitly with:

```bash
make compose CONFIG_FILE=./config/config.stage.toml
```

The tracked file contains only development credentials (`admin` / `admin123`),
a long-lived development controller token, and a development HMAC/JWT secret.
Never deploy it to production. Production deployments should mount a reviewed
configuration/policy revision on every replica and inject secrets through the
deployment platform. Configuration is startup-only, so replicas remain
stateless and rolling upgrades can mix binaries with and without the additive
flag while existing environment/default discovery remains supported.

### Sensitive values

Keys marked **Sensitive** are always logged as `<redacted>` (when set) or `<unset>` (when absent), regardless of log level. Production secrets should be supplied externally rather than committed to config files; the tracked local fixture is intentionally development-only.

An empty string (`KEY=`) for a **Required** key is treated identically to an absent key and causes a startup failure listing all failing keys.

---

## gitstore-api

**Config file**: `config.toml` (optional, current working directory)

**Explicit file**: `gitstore-api --config-file /path/to/config.toml` (required when selected)

**`.env` file**: `.env` (optional, current working directory)
**Env var prefix**: `GITSTORE_`

### API Server

| Key             | Env Var                   | Type    | Default | Required | Sensitive | Description                                             |
|-----------------|---------------------------|---------|---------|----------|-----------|---------------------------------------------------------|
| `api.port`      | `GITSTORE_API__PORT`      | integer | `4000`  | No       | No        | HTTP port the GraphQL API server listens on (1–65535)   |
| `api.git_port`  | `GITSTORE_API__GIT_PORT`  | integer | `5000`  | No       | No        | Git Smart HTTP port the API server listens on (1–65535) |
| `api.grpc_port` | `GITSTORE_API__GRPC_PORT` | integer | `6000`  | No       | No        | CatalogService gRPC port called by gitstore-git-service |
| `api.rate_limit_per_second` | `GITSTORE_API__RATE_LIMIT_PER_SECOND` | float | `50` | No | No | Sustained per-client-IP request rate allowed on `/graphql` |
| `api.rate_limit_burst` | `GITSTORE_API__RATE_LIMIT_BURST` | integer | `100` | No | No | Per-client-IP token-bucket burst size on top of `api.rate_limit_per_second` |

### Git Service Connection

| Key            | Env Var                   | Type   | Default                  | Required | Sensitive | Description                          |
|----------------|---------------------------|--------|--------------------------|----------|-----------|--------------------------------------|
| `git.grpc.uri` | `GITSTORE_GIT__GRPC__URI` | string | `dns:///localhost:50051` | Yes      | No        | gRPC address of gitstore-git-service |

### Git Smart HTTP Endpoints

The following endpoints are served on port `api.git_port` (default `5000`):

| Method | Path                                                              | Description                                       |
|--------|-------------------------------------------------------------------|---------------------------------------------------|
| `GET`  | `/{namespace}/{repo}.git/info/refs?service=git-upload-pack`       | Advertise refs for fetch/clone                    |
| `GET`  | `/{namespace}/{repo}.git/info/refs?service=git-receive-pack`      | Advertise refs for push                           |
| `POST` | `/{namespace}/{repo}.git/git-upload-pack`                         | Upload pack (fetch/clone data transfer)           |
| `POST` | `/{namespace}/{repo}.git/git-receive-pack`                        | Receive pack (push data transfer)                 |
| `GET`  | `/health`                                                         | Health probe — returns `{"status":"ok"}`          |

### Authentication

| Key                        | Env Var                               | Type     | Default    | Required | Sensitive | Description                                                 |
|----------------------------|---------------------------------------|----------|------------|----------|-----------|-------------------------------------------------------------|
| `auth.admin.username`      | `GITSTORE_AUTH__ADMIN__USERNAME`      | string   | —          | **Yes**  | No        | Admin portal username                                       |
| `auth.admin.password_hash` | `GITSTORE_AUTH__ADMIN__PASSWORD_HASH` | string   | —          | **Yes**  | **Yes**   | bcrypt hash of the admin password                           |
| `auth.jwt.secret`          | `GITSTORE_AUTH__JWT__SECRET`          | string   | —          | **Yes**  | **Yes**   | JWT signing key (minimum 32 characters)                     |
| `auth.jwt.duration`        | `GITSTORE_AUTH__JWT__DURATION`        | duration | `24h`      | No       | No        | JWT token validity (e.g. `12h`, `30m`)                      |
| `auth.jwt.issuer`          | `GITSTORE_AUTH__JWT__ISSUER`          | string   | `gitstore` | No       | No        | JWT `iss` claim value                                       |
| `auth.jwt.refresh_grace`   | `GITSTORE_AUTH__JWT__REFRESH_GRACE`   | duration | `60s`      | No       | No        | Window after expiry during which `refreshToken` is accepted |

For config files, admin auth keys are nested under `[auth.admin]` (for example, `username = "admin"`) and JWT keys are nested under `[auth.jwt]`.

### Cache

| Key         | Env Var               | Type    | Default | Required | Sensitive | Description                            |
|-------------|-----------------------|---------|---------|----------|-----------|----------------------------------------|
| `cache.ttl` | `GITSTORE_CACHE__TTL` | integer | `300`   | No       | No        | In-memory catalog cache TTL in seconds |

### Logging

| Key          | Env Var                | Type   | Default | Required | Sensitive | Description                            |
|--------------|------------------------|--------|---------|----------|-----------|----------------------------------------|
| `log.level`  | `GITSTORE_LOG__LEVEL`  | string | `info`  | No       | No        | `debug` \| `info` \| `warn` \| `error` |
| `log.format` | `GITSTORE_LOG__FORMAT` | string | `json`  | No       | No        | `json` \| `text`                       |

### Datastore

| Key                                         | Env Var                                                   | Type            | Default          | Required | Sensitive | Description                                    |
|---------------------------------------------|-----------------------------------------------------------|-----------------|------------------|----------|-----------|------------------------------------------------|
| `datastore.backend`                         | `GITSTORE_DATASTORE__BACKEND`                             | string          | `memdb`          | No       | No        | Active datastore backend: `memdb` or `scylla`  |
| `datastore.scylla.hosts`                    | `GITSTORE_DATASTORE__SCYLLA__HOSTS`                       | list of strings | `localhost:9042` | No       | No        | Comma-separated Scylla endpoints (`host:port`) |
| `datastore.scylla.keyspace`                 | `GITSTORE_DATASTORE__SCYLLA__KEYSPACE`                    | string          | `gitstore`       | No       | No        | Scylla keyspace name                           |
| `datastore.scylla.username`                 | `GITSTORE_DATASTORE__SCYLLA__USERNAME`                    | string          | —                | No       | No        | Scylla username (optional)                     |
| `datastore.scylla.password`                 | `GITSTORE_DATASTORE__SCYLLA__PASSWORD`                    | string          | —                | No       | **Yes**   | Scylla password (optional, redacted in logs)   |
| `datastore.scylla.tls`                      | `GITSTORE_DATASTORE__SCYLLA__TLS`                         | boolean         | `false`          | No       | No        | Enable TLS for Scylla connections              |
| `datastore.scylla.disable_shard_aware_port` | `GITSTORE_DATASTORE__SCYLLA__DISABLE_SHARD_AWARE_PORT`    | boolean         | `false`          | No       | No        | Disable shard-aware Scylla port discovery      |

### Feature rollout gates

| Key | Env Var | Type | Default | Required | Sensitive | Description |
|---|---|---|---|---|---|---|
| `features.namespace_repository_fence` | `GITSTORE_FEATURES__NAMESPACE_REPOSITORY_FENCE` | string | `auto` | No | No | `auto`, `disabled`, or `enabled`. `auto` enables memdb and disables Scylla; enable Scylla only after migration 005 and full API-fleet convergence. |

See [Namespace admission operations](runbooks/namespace-admission.md) for the
mandatory mixed-version ingress/AuthZ deny and rollback procedure.

### Scylla projection operations

`gitctl` reads the same Scylla environment variables for offline projection
audit and repair. The password is read only from
`GITSTORE_DATASTORE__SCYLLA__PASSWORD`; do not pass it as a CLI argument.

```bash
cd gitstore-api
go run ./cmd/gitctl scylla-projection-audit
go run ./cmd/gitctl scylla-projection-repair --dry-run
go run ./cmd/gitctl scylla-projection-repair --confirm
```

Optional command flags override non-secret connection settings:
`--hosts`, `--keyspace`, `--username`, `--tls`, and
`--disable-shard-aware-port`. Repair requires either `--dry-run` or explicit
`--confirm`; conditional misses and post-repair findings return an error.

Operational invariants:

- partition hard ceiling: 100 MiB;
- hot-partition target: 10 MiB;
- `gc_grace_seconds`: 10 days;
- completed anti-entropy repair interval: at most 7 days;
- no TWCS on tables with updates or explicit deletes.

Datastore metrics use bounded labels only:

- `gitstore_datastore_projection_write_failures_total`
  (`operation`, `backend`, `resource_kind`, `projection`);
- `gitstore_datastore_compensation_attempts_total` and
  `gitstore_datastore_compensation_failures_total` (same bounded labels);
- `gitstore_datastore_projection_findings_total` (adds `finding_type`);
- `gitstore_datastore_operation_duration_seconds` (`operation`, `backend`).

Alert on every compensation failure, any partition above 100 MiB, repair older
than seven days, and sustained growth in projection findings or repair backlog.
Resource UIDs and names belong in structured logs, never metric labels. See
[`scylla-projection-repair.md`](runbooks/scylla-projection-repair.md).

### Example `config.toml`

```toml
[api]
port = 4000
git_port = 5000
grpc_port = 6000

[git.grpc]
uri = "dns:///localhost:50051"

[auth.jwt]
duration = "24h"
issuer = "gitstore"
refresh_grace = "60s"

[log]
level = "debug"
format = "json"

[cache]
ttl = 300

[datastore]
backend = "memdb"

[datastore.scylla]
hosts = ["localhost:9042"]
keyspace = "gitstore"
tls = false
```

Secrets (`auth.admin.password_hash`, `auth.jwt.secret`) must remain in environment variables or `.env`, never in `config.toml`.

## gitstore-git-service

**Config file**: `gitstore.toml` (optional, current working directory)  
**`.env` file**: `.env` (optional, current working directory)  
**Env var prefix**: `GITSTORE_`

### Core

| Key                            | Env Var                                   | Type   | Default                 | Required | Sensitive | Description                                       |
|--------------------------------|-------------------------------------------|--------|-------------------------|----------|-----------|---------------------------------------------------|
| `grpc.port`                    | `GITSTORE_GRPC__PORT`                     | u16    | `50051`                 | No       | No        | GitService gRPC server port                       |
| `git.data_dir`                 | `GITSTORE_GIT__DATA_DIR`                  | string | `/data/repos`           | No       | No        | Bare repository storage directory                 |
| `git.repo.max_file_size`       | `GITSTORE_GIT__REPO__MAX_FILE_SIZE`       | u64    | `52428800`              | No       | No        | Max file size in bytes                            |
| `git.repo.max_pack_size_bytes` | `GITSTORE_GIT__REPO__MAX_PACK_SIZE_BYTES` | u64    | `52428800`              | No       | No        | Max pack size in bytes                            |
| `catalog_service.uri`          | `GITSTORE_CATALOG_SERVICE__URI`           | string | `http://localhost:6000` | No       | No        | gitstore-api CatalogService gRPC endpoint         |
| `log.level`                    | `GITSTORE_LOG__LEVEL`                     | string | `info`                  | No       | No        | `trace` \| `debug` \| `info` \| `warn` \| `error` |
| `log.format`                   | `GITSTORE_LOG__FORMAT`                    | string | `json`                  | No       | No        | `json` \| `text`                                  |

### Hook Phase Toggles

Nested hook keys may be set in `gitstore.toml`. Environment variable overrides use `__` (double-underscore) as the separator.

| Config Key                                             | Default | Description                                   |
|--------------------------------------------------------|---------|-----------------------------------------------|
| `hooks.git_receive_pack.pre_receive.enabled`           | `true`  | Enable the `pre-receive` hook phase           |
| `hooks.git_receive_pack.update.enabled`                | `false` | Enable the `update` hook phase                |
| `hooks.git_receive_pack.post_receive.enabled`          | `true`  | Enable the `post-receive` hook phase          |
| `hooks.git_receive_pack.proc_receive.enabled`          | `false` | Enable the `proc-receive` hook phase          |
| `hooks.git_receive_pack.post_update.enabled`           | `false` | Enable the `post-update` hook phase           |
| `hooks.git_receive_pack.reference_transaction.enabled` | `false` | Enable the `reference-transaction` hook phase |

### Validation and Admission

| Config Key                         | Env Var                                      | Default           | Description                                 |
|------------------------------------|----------------------------------------------|-------------------|---------------------------------------------|
| `schema_validation.phase`          | `GITSTORE_SCHEMA_VALIDATION__PHASE`          | `pre-receive`     | Hook phase for blocking schema validation   |
| `schema_validation.timeout_secs`   | `GITSTORE_SCHEMA_VALIDATION__TIMEOUT_SECS`   | `10`              | CatalogService validation timeout           |
| `admission_control.phase`          | `GITSTORE_ADMISSION_CONTROL__PHASE`          | `post-receive`    | Hook phase for admission notification       |
| `admission_control.branch_pattern` | `GITSTORE_ADMISSION_CONTROL__BRANCH_PATTERN` | `refs/heads/main` | Ref pattern admitted into catalogue storage |

### CLI Flags

| Flag                   | Type   | Description                                           |
|------------------------|--------|-------------------------------------------------------|
| `--config-file <path>` | string | Load config from this path instead of `gitstore.toml` |
| `--log-level <level>`  | string | Override log level (highest priority)                 |

### Example `gitstore.toml`

```toml
[grpc]
port = 50051

[git]
data_dir = "/data/repos"

[git.repo]
max_file_size = 52428800
max_pack_size_bytes = 52428800

[log]
level = "info"
format = "json"

[hooks.git_receive_pack]
pre_receive  = { enabled = true }
update       = { enabled = false }
post_receive = { enabled = true }
proc_receive = { enabled = false }
post_update  = { enabled = false }
reference_transaction = { enabled = false }

[schema_validation]
phase = "pre-receive"
timeout_secs = 10

[admission_control]
phase = "post-receive"
branch_pattern = "refs/heads/main"

[catalog_service]
uri = "http://localhost:6000"
```

---

## gitstore-controller-manager

**Config file**: `config.toml` (optional, current working directory)

**Explicit file**: `gitstore-controller-manager --config-file /path/to/config.toml` (required when selected)

**`.env` file**: `.env` (optional, current working directory)
**Env var prefix**: `GITSTORE_`

| Key                                  | Env Var                                        | Type     | Default                         | Required | Sensitive | Description                                                 |
|--------------------------------------|------------------------------------------------|----------|---------------------------------|----------|-----------|-------------------------------------------------------------|
| `controller.port`                    | `GITSTORE_CONTROLLER__PORT`                    | integer  | `5001`                          | No       | No        | HTTP port for `/health`, `/metrics`, and `/controller/v1/*` |
| `controller.api_uri`                 | `GITSTORE_CONTROLLER__API_URI`                 | string   | `http://localhost:4000/graphql` | No       | No        | GraphQL API URI used by reconcilers                         |
| `controller.api_token`               | `GITSTORE_CONTROLLER__API_TOKEN`               | string   | (empty)                         | No       | Yes       | Bearer token presented to `gitstore-api` on every GraphQL query/mutation/subscription |
| `controller.default_max_attempts`    | `GITSTORE_CONTROLLER__DEFAULT_MAX_ATTEMPTS`    | integer  | `5`                             | No       | No        | Retry limit before quarantine                               |
| `controller.default_stall_threshold` | `GITSTORE_CONTROLLER__DEFAULT_STALL_THRESHOLD` | duration | `5m`                            | No       | No        | Worker stall threshold                                      |
| `controller.checkpoint_dir`          | `GITSTORE_CONTROLLER__CHECKPOINT_DIR`          | string   | `.gitstore/checkpoints`         | No       | No        | Directory for the filesystem checkpoint store (one file per kind) |
| `controller.checkpoint_flush_interval_events` | `GITSTORE_CONTROLLER__CHECKPOINT_FLUSH_INTERVAL_EVENTS` | integer | `100` | No | No | Watch events between checkpoint persists |
| `controller.max_watch_backoff`       | `GITSTORE_CONTROLLER__MAX_WATCH_BACKOFF`       | duration | `30s`                           | No       | No        | Cap on exponential backoff between watch-stream reconnect attempts |
| `log.level`                          | `GITSTORE_LOG__LEVEL`                          | string   | `info`                          | No       | No        | `debug` \| `info` \| `warn` \| `error`                      |
| `log.format`                         | `GITSTORE_LOG__FORMAT`                         | string   | `json`                          | No       | No        | `json` \| `text`                                            |

Example:

```toml
[controller]
port = 5001
api_uri = "http://localhost:4000/graphql"
default_max_attempts = 5
default_stall_threshold = "5m"
checkpoint_dir = ".gitstore/checkpoints"
checkpoint_flush_interval_events = 100
max_watch_backoff = "30s"

[log]
level = "info"
format = "json"
```

List-then-watch bootstrap, restart resume, and expired-watch-cursor recovery for registered
resource kinds (spec 036) persist a per-kind restart checkpoint under `checkpoint_dir`. Each
checkpoint contains the `resourceVersion`, cache snapshot, and deletion replay keys needed to
restore volatile controller state without losing queued reconciliation work.
Checkpoint health — last successful write time, replay backlog, and write-failure count — is
exposed on the existing `/metrics` endpoint as `gitstore_controller_checkpoint_last_write_timestamp_seconds`,
`gitstore_controller_checkpoint_replay_backlog`, and `gitstore_controller_checkpoint_write_failures_total`
(all labeled by `kind`).

---

## Local Development with `.env`

All Go services automatically load a `.env` file from the current working directory at startup. The Git service loads `.env` in its binary entrypoint before resolving layered configuration. Shell environment variables always override `.env` values.

For the shared gRPC HMAC secret, `make gen-hmac-secret` writes the same `GITSTORE_AUTH__GRPC__HMAC_SECRET` value to both `gitstore-api/.env` and `gitstore-git-service/.env` so local API and git-service runs stay in sync.

Copy the example file and fill in the required values:

```bash
# gitstore-api
cp gitstore-api/.env.example gitstore-api/.env

# gitstore-git-service
cp gitstore-git-service/.env.example gitstore-git-service/.env

# gitstore-controller-manager
cp gitstore-controller-manager/.env.example gitstore-controller-manager/.env
```

See `.env.example` in each service directory for the full list of supported variables with their types, defaults, and required/optional status.
