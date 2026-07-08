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
5. CLI flags --config-file, --log-level  (gitstore-git-service only; override everything)
```

### Sensitive values

Keys marked **Sensitive** are always logged as `<redacted>` (when set) or `<unset>` (when absent), regardless of log level. Sensitive values must never be placed in config files — set them via environment variables or `.env` only.

An empty string (`KEY=`) for a **Required** key is treated identically to an absent key and causes a startup failure listing all failing keys.

---

## gitstore-api

**Config file**: `config.toml` (optional, current working directory)

**`.env` file**: `.env` (optional, current working directory)
**Env var prefix**: `GITSTORE_`

### API Server

| Key             | Env Var                   | Type    | Default | Required | Sensitive | Description                                             |
|-----------------|---------------------------|---------|---------|----------|-----------|---------------------------------------------------------|
| `api.port`      | `GITSTORE_API__PORT`      | integer | `4000`  | No       | No        | HTTP port the GraphQL API server listens on (1–65535)   |
| `api.git_port`  | `GITSTORE_API__GIT_PORT`  | integer | `5000`  | No       | No        | Git Smart HTTP port the API server listens on (1–65535) |
| `api.grpc_port` | `GITSTORE_API__GRPC_PORT` | integer | `6000`  | No       | No        | CatalogService gRPC port called by gitstore-git-service |

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

| Key                       | Env Var                             | Type   | Default                 | Required | Sensitive | Description                                       |
|---------------------------|-------------------------------------|--------|-------------------------|----------|-----------|---------------------------------------------------|
| `grpc.port`               | `GITSTORE_GRPC__PORT`               | u16    | `50051`                 | No       | No        | GitService gRPC server port                       |
| `git.data_dir`            | `GITSTORE_GIT__DATA_DIR`            | string | `/data/repos`           | No       | No        | Bare repository storage directory                 |
| `git.max_pack_size_bytes` | `GITSTORE_GIT__MAX_PACK_SIZE_BYTES` | u64    | `52428800`              | No       | No        | Max pack size in bytes                            |
| `git.repo.max_file_size`  | `GITSTORE_GIT__REPO__MAX_FILE_SIZE` | u64    | `52428800`              | No       | No        | Max file size in bytes                            |
| `catalog_service.uri`     | `GITSTORE_CATALOG_SERVICE__URI`     | string | `http://localhost:6000` | No       | No        | gitstore-api CatalogService gRPC endpoint         |
| `log.level`               | `GITSTORE_LOG__LEVEL`               | string | `info`                  | No       | No        | `trace` \| `debug` \| `info` \| `warn` \| `error` |
| `log.format`              | `GITSTORE_LOG__FORMAT`              | string | `json`                  | No       | No        | `json` \| `text`                                  |

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
max_pack_size_bytes = 52428800

[git.repo]
max_file_size = 52428800

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

**`.env` file**: `.env` (optional, current working directory)
**Env var prefix**: `GITSTORE_`

| Key                                  | Env Var                                        | Type     | Default                         | Required | Sensitive | Description                                                 |
|--------------------------------------|------------------------------------------------|----------|---------------------------------|----------|-----------|-------------------------------------------------------------|
| `controller.port`                    | `GITSTORE_CONTROLLER__PORT`                    | integer  | `5001`                          | No       | No        | HTTP port for `/health`, `/metrics`, and `/controller/v1/*` |
| `controller.api_uri`                 | `GITSTORE_CONTROLLER__API_URI`                 | string   | `http://localhost:4000/graphql` | No       | No        | GraphQL API URI used by reconcilers                         |
| `controller.default_max_attempts`    | `GITSTORE_CONTROLLER__DEFAULT_MAX_ATTEMPTS`    | integer  | `5`                             | No       | No        | Retry limit before quarantine                               |
| `controller.default_stall_threshold` | `GITSTORE_CONTROLLER__DEFAULT_STALL_THRESHOLD` | duration | `5m`                            | No       | No        | Worker stall threshold                                      |
| `log.level`                          | `GITSTORE_LOG__LEVEL`                          | string   | `info`                          | No       | No        | `debug` \| `info` \| `warn` \| `error`                      |
| `log.format`                         | `GITSTORE_LOG__FORMAT`                         | string   | `json`                          | No       | No        | `json` \| `text`                                            |

Example:

```toml
[controller]
port = 5001
api_uri = "http://localhost:4000/graphql"
default_max_attempts = 5
default_stall_threshold = "5m"

[log]
level = "info"
format = "json"
```

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
