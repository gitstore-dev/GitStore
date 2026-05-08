# Configuration Schema Contract

**Version**: 1.0.0  
**Date**: 2026-05-08  
**Branch**: `005-structured-config-mgmt`

This document is the authoritative operator-facing contract for all configuration keys accepted by `gitstore-api` and `gitstore-git-service`. It is the source of truth for `docs/` documentation generation.

---

## gitstore-api Configuration Schema

Config file: `config.toml` (optional, current working directory)  
`.env` file: `.env` (optional, current working directory)  
Env var prefix: `GITSTORE_` (all keys)

### API Server

| Key        | Env Var             | Type    | Default | Required | Sensitive | Description                                   |
|------------|---------------------|---------|---------|----------|-----------|-----------------------------------------------|
| `api.port` | `GITSTORE_API_PORT` | integer | `4000`  | No       | No        | HTTP port the API server listens on (1–65535) |

### Git Service Connection

| Key              | Env Var                   | Type   | Default                 | Required | Sensitive | Description                                                    |
|------------------|---------------------------|--------|-------------------------|----------|-----------|----------------------------------------------------------------|
| `git.grpc`       | `GITSTORE_GIT_GRPC`       | string | `localhost:50051`       | **Yes**  | No        | gRPC address of gitstore-git-service (e.g., `localhost:50051`) |
| `git.ws`         | `GITSTORE_GIT_WS`         | string | `ws://localhost:8080`   | **Yes**  | No        | WebSocket address of gitstore-git-service                      |
| `git.http_url`   | `GITSTORE_GIT_HTTP_URL`   | string | `http://localhost:9418` | **Yes**  | No        | Smart HTTP address of gitstore-git-service                     |

### Authentication

| Key                        | Env Var                             | Type     | Default    | Required | Sensitive | Description                                                        |
|----------------------------|-------------------------------------|----------|------------|----------|-----------|--------------------------------------------------------------------|
| `auth.admin_username`      | `GITSTORE_AUTH_ADMIN_USERNAME`      | string   | —          | **Yes**  | No        | Admin portal username                                              |
| `auth.admin_password_hash` | `GITSTORE_AUTH_ADMIN_PASSWORD_HASH` | string   | —          | **Yes**  | **Yes**   | bcrypt hash of the admin password                                  |
| `auth.jwt_secret`          | `GITSTORE_AUTH_JWT_SECRET`          | string   | —          | **Yes**  | **Yes**   | Secret key for JWT signing (minimum 32 characters)                 |
| `auth.jwt_duration`        | `GITSTORE_AUTH_JWT_DURATION`        | duration | `24h`      | No       | No        | JWT token validity period (Go duration string, e.g., `12h`, `30m`) |
| `auth.jwt_issuer`          | `GITSTORE_AUTH_JWT_ISSUER`          | string   | `gitstore` | No       | No        | JWT `iss` claim value                                              |

### Cache

| Key         | Env Var              | Type    | Default | Required | Sensitive | Description                    |
|-------------|----------------------|---------|---------|----------|-----------|--------------------------------|
| `cache.ttl` | `GITSTORE_CACHE_TTL` | integer | `300`   | No       | No        | In-memory cache TTL in seconds |

### Logging

| Key         | Env Var              | Type   | Default | Required | Sensitive | Description                                     |
|-------------|----------------------|--------|---------|----------|-----------|-------------------------------------------------|
| `log_level` | `GITSTORE_LOG_LEVEL` | string | `info`  | No       | No        | Log verbosity: `debug`, `info`, `warn`, `error` |

---

## gitstore-git-service Configuration Schema

Config file: `gitstore.toml` (optional, current working directory)  
`.env` file: `.env` (optional, current working directory)  
Env var prefix: `GITSTORE_` (all keys)

### Core

| Key             | Env Var                  | Type          | Default       | Required | Sensitive | Description                                              |
|-----------------|--------------------------|---------------|---------------|----------|-----------|----------------------------------------------------------|
| `http_port`     | `GITSTORE_HTTP_PORT`     | integer (u16) | `9418`        | No       | No        | Port for smart HTTP git protocol (1–65535)               |
| `ws_port`       | `GITSTORE_WS_PORT`       | integer (u16) | `8080`        | No       | No        | WebSocket notification port (1–65535)                    |
| `grpc_port`     | `GITSTORE_GRPC_PORT`     | integer (u16) | `50051`       | No       | No        | gRPC server port (1–65535)                               |
| `data_dir`      | `GITSTORE_DATA_DIR`      | string        | `/data/repos` | No       | No        | Path to git repository storage directory                 |
| `log_level`     | `GITSTORE_LOG_LEVEL`     | string        | `info`        | No       | No        | Log verbosity: `trace`, `debug`, `info`, `warn`, `error` |
| `max_file_size` | `GITSTORE_MAX_FILE_SIZE` | integer (u64) | `52428800`    | No       | No        | Maximum allowed file size in bytes (default: 50 MB)      |

> **Constraint**: `http_port`, `ws_port`, and `grpc_port` must all be distinct values.

### Hook Phase Toggles (`git-receive-pack`)

All hook toggles default to `false` (disabled). The env var value is a boolean (`true` / `false`).

| Key                                                        | Env Var                                                      | Default | Description                           |
|------------------------------------------------------------|--------------------------------------------------------------|---------|---------------------------------------|
| `hooks.git_receive_pack.pre_receive.enabled`               | `GITSTORE_HOOKS_GIT_RECEIVE_PACK_PRE_RECEIVE_ENABLED`        | `false` | Enable the `pre-receive` hook phase   |
| `hooks.git_receive_pack.update.enabled`                    | `GITSTORE_HOOKS_GIT_RECEIVE_PACK_UPDATE_ENABLED`             | `false` | Enable the `update` hook phase        |
| `hooks.git_receive_pack.post_receive.enabled`              | `GITSTORE_HOOKS_GIT_RECEIVE_PACK_POST_RECEIVE_ENABLED`       | `false` | Enable the `post-receive` hook phase  |
| `hooks.git_receive_pack.proc_receive.enabled`              | `GITSTORE_HOOKS_GIT_RECEIVE_PACK_PROC_RECEIVE_ENABLED`       | `false` | Enable the `proc-receive` hook phase  |
| `hooks.git_receive_pack.post_update.enabled`               | `GITSTORE_HOOKS_GIT_RECEIVE_PACK_POST_UPDATE_ENABLED`        | `false` | Enable the `post-update` hook phase   |

> **Note**: Enabling a hook phase activates the corresponding processing logic during a `git push`. Disabled phases are skipped entirely.

### Admission Control

| Key                                                   | Env Var                                                        | Default       | Required | Description                                                                                                                                                  |
|-------------------------------------------------------|----------------------------------------------------------------|---------------|----------|--------------------------------------------------------------------------------------------------------------------------------------------------------------|
| `admission_control.validating_admission_policy.phase` | `GITSTORE_ADMISSION_CONTROL_VALIDATING_ADMISSION_POLICY_PHASE` | `pre-receive` | No       | Hook phase that runs the validating admission policy. Valid values: `pre-receive`. Only meaningful when `hooks.git_receive_pack.pre_receive.enabled = true`. |

---

## Source Precedence (Both Services)

```
1. Hard-coded defaults (lowest priority)
2. Config file (config.toml / gitstore.toml) — optional
3. .env file — optional; values loaded into process env before config build
4. Environment variables (highest priority)
   [gitstore-git-service only: CLI flags --config-file, --log-level override all above]
```

---

## Sensitive Value Handling

Keys marked **Sensitive: Yes** are always logged as `<redacted>` (when set) or `<unset>` (when absent) in all log output. The actual value is never written to logs regardless of log level.

An empty string (`KEY=`) for any **Required** key is treated identically to an absent key and causes startup failure.

---

## Stability

The system is in alpha. Breaking changes to env var names are expected and do not require a migration guide. Once the system reaches beta, the env var names defined in this contract will be treated as stable.

## Schema Evolution

- Adding a new optional key with a default: non-breaking, increment PATCH
- Adding a new required key: breaking for existing deployments, increment MAJOR
- Renaming or removing a key: breaking, increment MAJOR
