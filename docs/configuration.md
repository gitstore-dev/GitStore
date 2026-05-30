# Configuration Reference

This document is the complete operator reference for configuring `gitstore-api` and `gitstore-git-service`.

---

## Source Precedence

Both services load configuration from multiple sources in a fixed order. A higher-priority source overrides a lower-priority one:

```
1. Hard-coded defaults          (lowest priority)
2. Config file                  (optional)
3. .env file                    (optional)
4. Environment variables        (highest priority)
   [gitstore-git-service only]
5. CLI flags --config-file, --log-level  (override everything)
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

| Key        | Env Var              | Type    | Default | Required | Sensitive | Description                                   |
|------------|----------------------|---------|---------|----------|-----------|-----------------------------------------------|
| `api.port` | `GITSTORE_API__PORT` | integer | `4000`  | No       | No        | HTTP port the API server listens on (1–65535) |

### Git Service Connection

| Key                | Env Var                           | Type   | Default                  | Required | Sensitive | Description                                |
|--------------------|-----------------------------------|--------|--------------------------|----------|-----------|--------------------------------------------|
| `git.grpc.uri`     | `GITSTORE_GIT__GRPC__URI`        | string | `dns:///localhost:50051` | Yes      | No        | gRPC address of gitstore-git-service       |
| `git.ws.uri`       | `GITSTORE_GIT__WS__URI`          | string | `ws://localhost:8080`    | Yes      | No        | WebSocket address of gitstore-git-service  |
| `git.http.uri`     | `GITSTORE_GIT__HTTP__URI`        | string | `http://localhost:9418`  | Yes      | No        | Smart HTTP address of gitstore-git-service |

### Authentication

| Key                          | Env Var                               | Type     | Default    | Required | Sensitive | Description                             |
|------------------------------|---------------------------------------|----------|------------|----------|-----------|-----------------------------------------|
| `auth.admin.username`        | `GITSTORE_AUTH__ADMIN__USERNAME`      | string   | —          | **Yes**  | No        | Admin portal username                   |
| `auth.admin.password_hash`   | `GITSTORE_AUTH__ADMIN__PASSWORD_HASH` | string   | —          | **Yes**  | **Yes**   | bcrypt hash of the admin password       |
| `auth.jwt.secret`            | `GITSTORE_AUTH__JWT__SECRET`          | string   | —          | **Yes**  | **Yes**   | JWT signing key (minimum 32 characters) |
| `auth.jwt.duration`          | `GITSTORE_AUTH__JWT__DURATION`        | duration | `24h`      | No       | No        | JWT token validity (e.g. `12h`, `30m`)  |
| `auth.jwt.issuer`            | `GITSTORE_AUTH__JWT__ISSUER`          | string   | `gitstore` | No       | No        | JWT `iss` claim value                   |

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

[git.grpc]
uri = "dns:///localhost:50051"

[git.ws]
uri = "ws://localhost:8080"

[git.http]
uri = "http://localhost:9418"

[auth.jwt]
duration = "24h"
issuer = "gitstore"

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

**Config file**: `gitstore.toml` (optional, current working directory)  
**`.env` file**: `.env` (optional, current working directory)  
**Env var prefix**: `GITSTORE_`

### Core

| Key                      | Env Var                              | Type   | Default       | Required | Sensitive | Description                                       |
|--------------------------|--------------------------------------|--------|---------------|----------|-----------|---------------------------------------------------|
| `http.port`              | `GITSTORE_HTTP__PORT`                | u16    | `9418`        | No       | No        | Smart HTTP git server port (1–65535)              |
| `ws.port`                | `GITSTORE_WS__PORT`                  | u16    | `8080`        | No       | No        | WebSocket notification port (1–65535)             |
| `grpc.port`              | `GITSTORE_GRPC__PORT`                | u16    | `50051`       | No       | No        | gRPC server port (1–65535)                        |
| `git.data_dir`           | `GITSTORE_GIT__DATA_DIR`             | string | `/data/repos` | No       | No        | Repository storage directory                      |
| `log.level`              | `GITSTORE_LOG__LEVEL`                | string | `info`        | No       | No        | `trace` \| `debug` \| `info` \| `warn` \| `error` |
| `log.format`             | `GITSTORE_LOG__FORMAT`               | string | `json`        | No       | No        | `json` \| `text`                                  |
| `git.repo.max_file_size` | `GITSTORE_GIT__REPO__MAX_FILE_SIZE`  | u64    | `52428800`    | No       | No        | Max size per repo in bytes (default: 50 MB)       |

> **Constraint**: `http.port`, `ws.port`, and `grpc.port` must all be distinct values.

### Hook Phase Toggles

All hook toggles default to `false`. Nested keys must be set via `gitstore.toml` — env var overrides for nested keys require `__` (double-underscore) as the separator.

| Config Key                                    | Default | Description                          |
|-----------------------------------------------|---------|--------------------------------------|
| `hooks.git_receive_pack.pre_receive.enabled`  | `false` | Enable the `pre-receive` hook phase  |
| `hooks.git_receive_pack.update.enabled`       | `false` | Enable the `update` hook phase       |
| `hooks.git_receive_pack.post_receive.enabled` | `false` | Enable the `post-receive` hook phase |
| `hooks.git_receive_pack.proc_receive.enabled` | `false` | Enable the `proc-receive` hook phase |
| `hooks.git_receive_pack.post_update.enabled`  | `false` | Enable the `post-update` hook phase  |

### Admission Control

| Config Key                                            | Default       | Required | Description                                                                                            |
|-------------------------------------------------------|---------------|----------|--------------------------------------------------------------------------------------------------------|
| `admission_control.validating_admission_policy.phase` | `pre-receive` | No       | Hook phase running the validating admission policy. Only meaningful when `pre_receive.enabled = true`. |

### CLI Flags

| Flag                   | Type   | Description                                           |
|------------------------|--------|-------------------------------------------------------|
| `--config-file <path>` | string | Load config from this path instead of `gitstore.toml` |
| `--log-level <level>`  | string | Override log level (highest priority)                 |

### Example `gitstore.toml`

```toml
[http]
port = 9418

[ws]
port = 8080

[grpc]
port = 50051

[git]
data_dir = "/data/repos"

[git.repo]
max_file_size = 52428800

[log]
level = "info"
format = "json"

[hooks.git_receive_pack]
pre_receive  = { enabled = false }
update       = { enabled = false }
post_receive = { enabled = false }
proc_receive = { enabled = false }
post_update  = { enabled = false }

[admission_control.validating_admission_policy]
phase = "pre-receive"
```

---

## Local Development with `.env`

Both services automatically load a `.env` file from the current working directory at startup. Shell environment variables always override `.env` values.

Copy the example file and fill in the required values:

```bash
# gitstore-api
cp gitstore-api/.env.example gitstore-api/.env

# gitstore-git-service
cp gitstore-git-service/.env.example gitstore-git-service/.env
```

See `.env.example` in each service directory for the full list of supported variables with their types, defaults, and required/optional status.
