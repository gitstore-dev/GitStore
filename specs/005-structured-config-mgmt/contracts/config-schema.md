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

| Key            | Env Var                       | Type   | Default                  | Required | Sensitive | Description                                                    |
|----------------|-------------------------------|--------|--------------------------|----------|-----------|----------------------------------------------------------------|
| `git.grpc.uri` | `GITSTORE_GIT__GRPC__URI`     | string | `dns:///localhost:50051` | **Yes**  | No        | gRPC address of gitstore-git-service (e.g., `dns:///localhost:50051`) |
| `git.ws.uri`   | `GITSTORE_GIT__WS__URI`       | string | `ws://localhost:8080`    | **Yes**  | No        | WebSocket address of gitstore-git-service                      |
| `git.http.uri` | `GITSTORE_GIT__HTTP__URI`     | string | `http://localhost:9418`  | **Yes**  | No        | Smart HTTP address of gitstore-git-service                     |

### Authentication

| Key                          | Env Var                             | Type     | Default    | Required | Sensitive | Description                                                        |
|------------------------------|-------------------------------------|----------|------------|----------|-----------|--------------------------------------------------------------------|
| `auth.admin.username`        | `GITSTORE_AUTH__ADMIN__USERNAME`    | string   | —          | **Yes**  | No        | Admin portal username                                              |
| `auth.admin.password_hash`   | `GITSTORE_AUTH__ADMIN__PASSWORD_HASH` | string | —          | **Yes**  | **Yes**   | bcrypt hash of the admin password                                  |
| `auth.jwt.secret`            | `GITSTORE_AUTH__JWT__SECRET`        | string   | —          | **Yes**  | **Yes**   | Secret key for JWT signing (minimum 32 characters)                 |
| `auth.jwt.duration`          | `GITSTORE_AUTH__JWT__DURATION`      | duration | `24h`      | No       | No        | JWT token validity period (Go duration string, e.g., `12h`, `30m`) |
| `auth.jwt.issuer`            | `GITSTORE_AUTH__JWT__ISSUER`        | string   | `gitstore` | No       | No        | JWT `iss` claim value                                              |

### Cache

| Key         | Env Var              | Type    | Default | Required | Sensitive | Description                    |
|-------------|----------------------|---------|---------|----------|-----------|--------------------------------|
| `cache.ttl` | `GITSTORE_CACHE__TTL` | integer | `300`   | No       | No        | In-memory cache TTL in seconds |

### Logging

| Key         | Env Var              | Type   | Default | Required | Sensitive | Description                                     |
|-------------|----------------------|--------|---------|----------|-----------|-------------------------------------------------|
| `log.level` | `GITSTORE_LOG__LEVEL` | string | `info`  | No       | No        | Log verbosity: `debug`, `info`, `warn`, `error` |

---

## gitstore-git-service Configuration Schema

Config file: `gitstore.toml` (optional, current working directory)  
`.env` file: `.env` (optional, current working directory)  
Env var prefix: `GITSTORE_` (all keys)

### Core

| Key                      | Env Var                              | Type          | Default       | Required | Sensitive | Description                                              |
|--------------------------|--------------------------------------|---------------|---------------|----------|-----------|----------------------------------------------------------|
| `http.port`              | `GITSTORE_HTTP__PORT`                | integer (u16) | `9418`        | No       | No        | Port for smart HTTP git protocol (1–65535)               |
| `ws.port`                | `GITSTORE_WS__PORT`                  | integer (u16) | `8080`        | No       | No        | WebSocket notification port (1–65535)                    |
| `grpc.port`              | `GITSTORE_GRPC__PORT`                | integer (u16) | `50051`       | No       | No        | gRPC server port (1–65535)                               |
| `git.data_dir`           | `GITSTORE_GIT__DATA_DIR`             | string        | `/data/repos` | No       | No        | Path to git repository storage directory                 |
| `log.level`              | `GITSTORE_LOG__LEVEL`                | string        | `info`        | No       | No        | Log verbosity: `trace`, `debug`, `info`, `warn`, `error` |
| `git.repo.max_file_size` | `GITSTORE_GIT__REPO__MAX_FILE_SIZE`  | integer (u64) | `52428800`    | No       | No        | Maximum allowed file size in bytes (default: 50 MB)      |

> **Constraint**: `http.port`, `ws.port`, and `grpc.port` must all be distinct values.

### Hook Phase Toggles (`git-receive-pack`)

All hook toggles default to `false` (disabled). The env var value is a boolean (`true` / `false`).

| Key                                                        | Env Var                                                      | Default | Description                           |
|------------------------------------------------------------|--------------------------------------------------------------|---------|---------------------------------------|
| `hooks.git_receive_pack.pre_receive.enabled`               | `GITSTORE_HOOKS__GIT_RECEIVE_PACK__PRE_RECEIVE__ENABLED`     | `false` | Enable the `pre-receive` hook phase   |
| `hooks.git_receive_pack.update.enabled`                    | `GITSTORE_HOOKS__GIT_RECEIVE_PACK__UPDATE__ENABLED`          | `false` | Enable the `update` hook phase        |
| `hooks.git_receive_pack.post_receive.enabled`              | `GITSTORE_HOOKS__GIT_RECEIVE_PACK__POST_RECEIVE__ENABLED`    | `false` | Enable the `post-receive` hook phase  |
| `hooks.git_receive_pack.proc_receive.enabled`              | `GITSTORE_HOOKS__GIT_RECEIVE_PACK__PROC_RECEIVE__ENABLED`    | `false` | Enable the `proc-receive` hook phase  |
| `hooks.git_receive_pack.post_update.enabled`               | `GITSTORE_HOOKS__GIT_RECEIVE_PACK__POST_UPDATE__ENABLED`     | `false` | Enable the `post-update` hook phase   |

> **Note**: Enabling a hook phase activates the corresponding processing logic during a `git push`. Disabled phases are skipped entirely.

### Admission Control

| Key                                                   | Env Var                                                        | Default       | Required | Description                                                                                                                                                  |
|-------------------------------------------------------|----------------------------------------------------------------|---------------|----------|--------------------------------------------------------------------------------------------------------------------------------------------------------------|
| `admission_control.validating_admission_policy.phase` | `GITSTORE_ADMISSION_CONTROL__VALIDATING_ADMISSION_POLICY__PHASE` | `pre-receive` | No       | Hook phase that runs the validating admission policy. Valid values: `pre-receive`. Only meaningful when `hooks.git_receive_pack.pre_receive.enabled = true`. |

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
