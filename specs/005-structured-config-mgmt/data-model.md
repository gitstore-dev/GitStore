# Data Model: Structured Configuration Management

**Date**: 2026-05-08  
**Branch**: `005-structured-config-mgmt`

---

## Overview

Configuration in both services is modelled as a single typed struct per service. There is no database or persistent storage — the config struct is populated once at startup from layered sources (defaults → config file → env vars) and held in memory for the service lifetime.

---

## gitstore-api Config Struct (Go)

```
Config
├── Api: ApiConfig
│   └── Port: int          [default: 4000, env: GITSTORE_API_PORT]
├── Git: GitConfig
│   ├── GRPC: string       [required, default: localhost:50051, env: GITSTORE_GIT_GRPC]
│   ├── WS: string         [required, default: ws://localhost:8080, env: GITSTORE_GIT_WS]
│   └── HttpURL: string    [required, default: http://localhost:9418, env: GITSTORE_GIT_HTTP_URL]
├── Auth: AuthConfig
│   ├── AdminUsername: string      [required, env: GITSTORE_AUTH_ADMIN_USERNAME]
│   ├── AdminPasswordHash: string  [required, sensitive, env: GITSTORE_AUTH_ADMIN_PASSWORD_HASH]
│   ├── JWTSecret: string          [required, sensitive, env: GITSTORE_AUTH_JWT_SECRET]
│   ├── JWTDuration: string        [default: 24h, env: GITSTORE_AUTH_JWT_DURATION]
│   └── JWTIssuer: string          [default: gitstore, env: GITSTORE_AUTH_JWT_ISSUER]
├── Cache: CacheConfig
│   └── TTL: int           [default: 300, env: GITSTORE_CACHE_TTL]
└── LogLevel: string       [default: info, env: GITSTORE_LOG_LEVEL]
```

### Validation Rules (Go)

| Field                    | Rule                                                   | Error                                 |
|--------------------------|--------------------------------------------------------|---------------------------------------|
| `Git.GRPC`               | `required` — non-empty string (host:port)              | `git.grpc: required`                  |
| `Git.WS`                 | `required` — valid URL (`ws://` or `wss://`)           | `git.ws: required, url`               |
| `Git.HttpURL`            | `required` — valid URL (`http://` or `https://`)       | `git.http_url: required, url`         |
| `Auth.AdminUsername`     | `required` — non-empty string                          | `auth.admin_username: required`       |
| `Auth.AdminPasswordHash` | `required` — non-empty string                          | `auth.admin_password_hash: required`  |
| `Auth.JWTSecret`         | `required`, `min=32` — minimum 32 characters           | `auth.jwt_secret: required, min=32`   |
| `Auth.JWTDuration`       | `omitempty,duration` — valid Go duration string if set | `auth.jwt_duration: invalid duration` |
| `Api.Port`               | `min=1,max=65535`                                      | `api.port: out of range`              |

Empty string is treated as absent for all `required` fields (go-playground `required` tag rejects `""`).

### Sensitive Fields (Go)

Fields marked sensitive are always logged as `<redacted>` (set) or `<unset>` (empty):

- `Auth.AdminPasswordHash`
- `Auth.JWTSecret`

### State Transitions

Configuration is immutable after load. There are no state transitions — the struct is populated once at startup and never mutated.

### Source Precedence

```
hard-coded defaults
  ↓ (override)
config.toml (in working directory, optional)
  ↓ (override)
.env file (in working directory, optional; loaded into process env before config build)
  ↓ (override)
environment variables (highest priority)
```

---

## gitstore-git-service Config Struct (Rust)

```
AppConfig
├── http_port: u16         [default: 9418, env: GITSTORE_HTTP_PORT]
├── ws_port: u16           [default: 8080, env: GITSTORE_WS_PORT]
├── grpc_port: u16         [default: 50051, env: GITSTORE_GRPC_PORT]
├── data_dir: String       [default: /data/repos, env: GITSTORE_DATA_DIR]
├── log_level: String      [default: info, env: GITSTORE_LOG_LEVEL]
├── max_file_size: u64     [default: 52428800 (50MB), env: GITSTORE_MAX_FILE_SIZE]
├── hooks: HooksConfig
│   └── git_receive_pack: GitReceivePackHooks
│       ├── pre_receive: HookToggle   [default: disabled, env: GITSTORE_HOOKS_GIT_RECEIVE_PACK_PRE_RECEIVE_ENABLED]
│       ├── update: HookToggle        [default: disabled, env: GITSTORE_HOOKS_GIT_RECEIVE_PACK_UPDATE_ENABLED]
│       ├── post_receive: HookToggle  [default: disabled, env: GITSTORE_HOOKS_GIT_RECEIVE_PACK_POST_RECEIVE_ENABLED]
│       ├── proc_receive: HookToggle  [default: disabled, env: GITSTORE_HOOKS_GIT_RECEIVE_PACK_PROC_RECEIVE_ENABLED]
│       └── post_update: HookToggle   [default: disabled, env: GITSTORE_HOOKS_GIT_RECEIVE_PACK_POST_UPDATE_ENABLED]
└── admission_control: AdmissionControlConfig
    └── validating_admission_policy: ValidatingAdmissionPolicyConfig
        └── phase: String  [default: pre-receive, env: GITSTORE_ADMISSION_CONTROL_VALIDATING_ADMISSION_POLICY_PHASE]
                           [only meaningful when hooks.git_receive_pack.pre_receive.enabled = true]
```

`HookToggle` is a single-field struct `{ enabled: bool }`.  
`AdmissionControlConfig.validating_admission_policy.phase` accepts: `pre-receive` (only valid value in this iteration).

### Validation Rules (Rust)

| Field                                                 | Rule                                                                                                      |
|-------------------------------------------------------|-----------------------------------------------------------------------------------------------------------|
| `http_port`                                           | `range(min = 1, max = 65535)`                                                                             |
| `ws_port`                                             | `range(min = 1, max = 65535)`                                                                             |
| `grpc_port`                                           | `range(min = 1, max = 65535)`                                                                             |
| All three ports                                       | `custom` — `http_port`, `ws_port`, and `grpc_port` must be distinct                                       |
| `data_dir`                                            | `length(min = 1)` — must not be empty                                                                     |
| `log_level`                                           | `custom` — must be one of: `trace`, `debug`, `info`, `warn`, `error`                                      |
| `max_file_size`                                       | `range(min = 1)` — must be positive                                                                       |
| `admission_control.validating_admission_policy.phase` | `custom` — must be `pre-receive`; only validated when `hooks.git_receive_pack.pre_receive.enabled = true` |

### Sensitive Fields (Rust)

No currently defined fields are sensitive. Future auth/token fields will use `secrecy::SecretString`.

### Source Precedence

```
hard-coded defaults (in ConfigBuilder)
  ↓ (override)
gitstore.toml (in working directory, optional)
  ↓ (override)
.env file (loaded into process env via dotenvy before config build)
  ↓ (override)
environment variables (GITSTORE_ prefix, separator _)
  ↓ (override, highest priority)
CLI flags (--config-file, --log-level only)
```

---

## Key Entity Summary

| Entity                            | Service                     | Description                                                                               |
|-----------------------------------|-----------------------------|-------------------------------------------------------------------------------------------|
| `Config`                          | gitstore-api (Go)           | Top-level config struct; the single result of `config.Load()`                             |
| `ServerConfig`                    | gitstore-api                | HTTP server settings                                                                      |
| `GitConfig`                       | gitstore-api                | gRPC, WebSocket, and HTTP addresses for the git service                                   |
| `AuthConfig`                      | gitstore-api                | Admin credentials and JWT settings; contains sensitive fields                             |
| `CacheConfig`                     | gitstore-api                | In-memory cache settings                                                                  |
| `AppConfig`                       | gitstore-git-service (Rust) | Top-level config struct; the single result of `load_config()`                             |
| `HooksConfig`                     | gitstore-git-service        | Groups all hook phase toggles                                                             |
| `GitReceivePackHooks`             | gitstore-git-service        | Enabled/disabled toggles for each `git-receive-pack` hook phase                           |
| `HookToggle`                      | gitstore-git-service        | Single-field struct `{ enabled: bool }` for one hook phase                                |
| `AdmissionControlConfig`          | gitstore-git-service        | Admission control settings; currently contains only `validating_admission_policy`         |
| `ValidatingAdmissionPolicyConfig` | gitstore-git-service        | Validating admission policy settings; `phase` determines which hook phase runs the policy |
| `Config Source`                   | Both                        | One of: defaults, config file, `.env`, environment variables                              |
| `Config Entry Point`              | Both                        | The single `config.Load()` / `load_config()` function called once in `main`               |
