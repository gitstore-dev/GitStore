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

| Key        | Env Var             | Type    | Default | Required | Sensitive | Description                                   |
|------------|---------------------|---------|---------|----------|-----------|-----------------------------------------------|
| `api.port` | `GITSTORE_API_PORT` | integer | `4000`  | No       | No        | HTTP port the API server listens on (1–65535) |

### Git Service Connection

| Key            | Env Var                 | Type   | Default                 | Required | Sensitive | Description                                |
|----------------|-------------------------|--------|-------------------------|----------|-----------|--------------------------------------------|
| `git.grpc`     | `GITSTORE_GIT_GRPC`     | string | `localhost:50051`       | Yes      | No        | gRPC address of gitstore-git-service       |
| `git.ws`       | `GITSTORE_GIT_WS`       | string | `ws://localhost:8080`   | Yes      | No        | WebSocket address of gitstore-git-service  |
| `git.http_url` | `GITSTORE_GIT_HTTP_URL` | string | `http://localhost:9418` | Yes      | No        | Smart HTTP address of gitstore-git-service |

### Authentication

| Key                        | Env Var                             | Type     | Default    | Required | Sensitive | Description                             |
|----------------------------|-------------------------------------|----------|------------|----------|-----------|-----------------------------------------|
| `auth.admin_username`      | `GITSTORE_AUTH_ADMIN_USERNAME`      | string   | —          | **Yes**  | No        | Admin portal username                   |
| `auth.admin_password_hash` | `GITSTORE_AUTH_ADMIN_PASSWORD_HASH` | string   | —          | **Yes**  | **Yes**   | bcrypt hash of the admin password       |
| `auth.jwt_secret`          | `GITSTORE_AUTH_JWT_SECRET`          | string   | —          | **Yes**  | **Yes**   | JWT signing key (minimum 32 characters) |
| `auth.jwt_duration`        | `GITSTORE_AUTH_JWT_DURATION`        | duration | `24h`      | No       | No        | JWT token validity (e.g. `12h`, `30m`)  |
| `auth.jwt_issuer`          | `GITSTORE_AUTH_JWT_ISSUER`          | string   | `gitstore` | No       | No        | JWT `iss` claim value                   |

### Cache

| Key         | Env Var              | Type    | Default | Required | Sensitive | Description                            |
|-------------|----------------------|---------|---------|----------|-----------|----------------------------------------|
| `cache.ttl` | `GITSTORE_CACHE_TTL` | integer | `300`   | No       | No        | In-memory catalog cache TTL in seconds |

### Logging

| Key         | Env Var              | Type   | Default | Required | Sensitive | Description                            |
|-------------|----------------------|--------|---------|----------|-----------|----------------------------------------|
| `log_level` | `GITSTORE_LOG_LEVEL` | string | `info`  | No       | No        | `debug` \| `info` \| `warn` \| `error` |

### Example `config.toml`

```toml
[api]
port = 4000

[git]
ws       = "ws://localhost:8080"
http_url = "http://localhost:9418"

[cache]
ttl = 300

log_level = "debug"
```

Secrets (`admin_password_hash`, `jwt_secret`) must remain in environment variables or `.env`, never in `config.toml`.

---

## gitstore-git-service

**Config file**: `gitstore.toml` (optional, current working directory)  
**`.env` file**: `.env` (optional, current working directory)  
**Env var prefix**: `GITSTORE_`

### Core

| Key             | Env Var                  | Type   | Default       | Required | Sensitive | Description                                       |
|-----------------|--------------------------|--------|---------------|----------|-----------|---------------------------------------------------|
| `http_port`     | `GITSTORE_HTTP_PORT`     | u16    | `9418`        | No       | No        | Smart HTTP git server port (1–65535)              |
| `ws_port`       | `GITSTORE_WS_PORT`       | u16    | `8080`        | No       | No        | WebSocket notification port (1–65535)             |
| `grpc_port`     | `GITSTORE_GRPC_PORT`     | u16    | `50051`       | No       | No        | gRPC server port (1–65535)                        |
| `data_dir`      | `GITSTORE_DATA_DIR`      | string | `/data/repos` | No       | No        | Repository storage directory                      |
| `log_level`     | `GITSTORE_LOG_LEVEL`     | string | `info`        | No       | No        | `trace` \| `debug` \| `info` \| `warn` \| `error` |
| `max_file_size` | `GITSTORE_MAX_FILE_SIZE` | u64    | `52428800`    | No       | No        | Max upload size in bytes (default: 50 MB)         |

> **Constraint**: `http_port`, `ws_port`, and `grpc_port` must all be distinct values.

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
http_port    = 9418
ws_port      = 8080
grpc_port    = 50051
data_dir     = "/data/repos"
log_level    = "info"
max_file_size = 52428800

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
