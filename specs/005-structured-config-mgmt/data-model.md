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
│   ├── Grpc: GitEndpointConfig
│   │   └── Uri: string    [required, default: dns:///localhost:50051, env: GITSTORE_GIT__GRPC__URI]
│   ├── Ws: GitEndpointConfig
│   │   └── Uri: string    [required, default: ws://localhost:8080, env: GITSTORE_GIT__WS__URI]
│   └── Http: GitEndpointConfig
│       └── Uri: string    [required, default: http://localhost:9418, env: GITSTORE_GIT__HTTP__URI]
├── Auth: AuthConfig
│   ├── Admin: UserConfig
│   │   ├── Username: string       [required, env: GITSTORE_AUTH_ADMIN_USERNAME]
│   │   └── Password: string       [required, sensitive, env: GITSTORE_AUTH_ADMIN_PASSWORD_HASH]
│   └── JWT: JWTConfig
│       ├── Secret: string         [required, sensitive, env: GITSTORE_AUTH_JWT_SECRET]
│       ├── Duration: string       [default: 24h, env: GITSTORE_AUTH_JWT_DURATION]
│       └── Issuer: string         [default: gitstore, env: GITSTORE_AUTH_JWT_ISSUER]
├── Cache: CacheConfig
│   └── TTL: int           [default: 300, env: GITSTORE_CACHE_TTL]
└── LogLevel: string       [default: info, env: GITSTORE_LOG_LEVEL]
```

### Validation Rules (Go)

| Field                    | Rule                                                   | Error                                 |
|--------------------------|--------------------------------------------------------|---------------------------------------|
| `Git.Grpc.Uri`           | `required` — non-empty string (host:port)              | `git.grpc.uri: required`              |
| `Git.Ws.Uri`             | `required` — valid URL (`ws://` or `wss://`)           | `git.ws.uri: required, url`           |
| `Git.Http.Uri`           | `required` — valid URL (`http://` or `https://`)       | `git.http.uri: required, url`         |
| `Auth.Admin.Username`    | `required` — non-empty string                          | `auth.admin.username: required`       |
| `Auth.Admin.Password`    | `required` — non-empty string                          | `auth.admin.password_hash: required`  |
| `Auth.JWT.Secret`        | `required`, `min=32` — minimum 32 characters           | `auth.jwt.secret: required, min=32`   |
| `Auth.JWT.Duration`      | `omitempty,duration` — valid Go duration string if set | `auth.jwt.duration: invalid duration` |
| `Api.Port`               | `min=1,max=65535`                                      | `api.port: out of range`              |

Empty string is treated as absent for all `required` fields (go-playground `required` tag rejects `""`).

### Sensitive Fields (Go)

Fields marked sensitive are always logged as `<redacted>` (set) or `<unset>` (empty):

- `Auth.Admin.Password`
- `Auth.JWT.Secret`

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
├── http: PortConfig
│   └── port: u16         [default: 9418, env: GITSTORE_HTTP__PORT]
├── ws: PortConfig
│   └── port: u16         [default: 8080, env: GITSTORE_WS__PORT]
├── grpc: PortConfig
│   └── port: u16         [default: 50051, env: GITSTORE_GRPC__PORT]
├── git: GitConfig
│   ├── data_dir: String   [default: /data/repos, env: GITSTORE_GIT__DATA_DIR]
│   └── repo: RepoConfig
│       └── max_file_size: u64 [default: 52428800 (50MB), env: GITSTORE_GIT__REPO__MAX_FILE_SIZE]
├── log: LogConfig
│   └── level: String      [default: info, env: GITSTORE_LOG__LEVEL]
├── hooks: HooksConfig
│   └── git_receive_pack: GitReceivePackHooks
│       ├── pre_receive: HookToggle   [default: disabled, env: GITSTORE_HOOKS__GIT_RECEIVE_PACK__PRE_RECEIVE__ENABLED]
│       ├── update: HookToggle        [default: disabled, env: GITSTORE_HOOKS__GIT_RECEIVE_PACK__UPDATE__ENABLED]
│       ├── post_receive: HookToggle  [default: disabled, env: GITSTORE_HOOKS__GIT_RECEIVE_PACK__POST_RECEIVE__ENABLED]
│       ├── proc_receive: HookToggle  [default: disabled, env: GITSTORE_HOOKS__GIT_RECEIVE_PACK__PROC_RECEIVE__ENABLED]
│       └── post_update: HookToggle   [default: disabled, env: GITSTORE_HOOKS__GIT_RECEIVE_PACK__POST_UPDATE__ENABLED]
└── admission_control: AdmissionControlConfig
  └── validating_admission_policy: ValidatingAdmissionPolicyConfig
    └── phase: String  [default: pre-receive, env: GITSTORE_ADMISSION_CONTROL__VALIDATING_ADMISSION_POLICY__PHASE]
               [only meaningful when hooks.git_receive_pack.pre_receive.enabled = true]
```

`HookToggle` is a single-field struct `{ enabled: bool }`.  
`AdmissionControlConfig.validating_admission_policy.phase` accepts: `pre-receive` (only valid value in this iteration).

### Validation Rules (Rust)

| Field                                                 | Rule                                                                                                      |
|-------------------------------------------------------|-----------------------------------------------------------------------------------------------------------|
| `http.port`                                           | `range(min = 1, max = 65535)`                                                                             |
| `ws.port`                                             | `range(min = 1, max = 65535)`                                                                             |
| `grpc.port`                                           | `range(min = 1, max = 65535)`                                                                             |
| All three ports                                       | `custom` — `http.port`, `ws.port`, and `grpc.port` must be distinct                                       |
| `git.data_dir`                                        | `length(min = 1)` — must not be empty                                                                     |
| `log.level`                                           | `custom` — must be one of: `trace`, `debug`, `info`, `warn`, `error`                                      |
| `git.repo.max_file_size`                              | `range(min = 1)` — must be positive                                                                       |
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
