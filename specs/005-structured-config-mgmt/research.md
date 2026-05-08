# Research: Structured Configuration Management

**Date**: 2026-05-08  
**Branch**: `005-structured-config-mgmt`

---

## Decision 1: Go Configuration Library

**Decision**: Use `github.com/spf13/viper` v1.20 with `github.com/joho/godotenv` for `.env` loading and `github.com/go-playground/validator/v10` for validation.

**Rationale**: Viper is the de-facto standard for layered config in Go services. It supports defaults, config file, and env var sources natively with a well-understood precedence model. The `go-viper/mapstructure/v2` module — already an indirect dependency in `gitstore-api/go.mod` — is the correct mapstructure version for viper 1.19+. Viper does not natively read `.env` files; `joho/godotenv` fills that gap and aligns with the standard Go ecosystem approach. `go-playground/validator` provides declarative, all-errors-at-once struct validation with `validate` struct tags.

**Alternatives considered**:
- `github.com/kelseyhightower/envconfig` — simpler but env-vars only; no config file support, no layered loading
- `github.com/caarlos0/env` — similar limitation; good for 12-factor but lacks file support
- Custom hand-rolled config — adequate at current scale (11 keys) but grows brittle as surface expands; ruled out per Simplicity principle

**Key implementation notes**:
- All env vars now share the `GITSTORE_` prefix; `AutomaticEnv` with `SetEnvPrefix("GITSTORE")` and `SetEnvKeyReplacer(strings.NewReplacer(".", "_"))` handles all keys uniformly — no explicit `BindEnv` calls needed
- Key mapping examples: `GITSTORE_API_PORT` → `api.port`, `GITSTORE_GIT_GRPC` → `git.grpc`, `GITSTORE_GIT_HTTP_URL` → `git.http_url`, `GITSTORE_AUTH_JWT_SECRET` → `auth.jwt_secret`
- `v.SetConfigType("toml")` must be set explicitly since viper defaults to YAML; `ReadInConfig()` returns `viper.ConfigFileNotFoundError` when no file is found — this must be allowed (config file is optional)
- `validate:"required"` from go-playground rejects both absent keys and empty-string values, satisfying the clarified empty-string-as-absent requirement
- `UnmarshalExact` (viper 1.19+) catches unknown config file keys and surfaces them as errors; pair with FR-010 warning behaviour

---

## Decision 2: Rust Configuration Library

**Decision**: Use `config` crate 0.14 with `dotenvy` 0.15 for `.env` loading; `validator` 0.18 for declarative validation; `secrecy` 0.10 for sensitive field types.

**Rationale**: `config-rs` (crate name: `config`) is the most mature layered config library for Rust and maps directly onto the 3-layer model (defaults → file → env). The `ConfigBuilder` pattern introduced in 0.14 provides a clean, type-safe builder API. `dotenvy` is the actively maintained fork of the abandoned `dotenv` crate; calling `dotenvy::dotenv().ok()` before config build is the standard Rust pattern for `.env` support. `secrecy` provides a `SecretString` type with a `[REDACTED]` `Debug` impl, avoiding accidental log exposure of secrets.

**Alternatives considered**:
- `figment` — excellent ergonomics but less battle-tested for env-var-heavy container deployments; `config-rs` has a larger ecosystem
- `envy` — env-vars only (no file support, no defaults layering)
- Continuing with `clap` for all config — `clap` is designed for CLI arg parsing, not layered config management; mixing env var overrides into clap's `default_value` mechanism is unmaintainable

**Key implementation notes**:
- `Environment::with_prefix("GITSTORE").separator("_").try_parsing(true)` correctly maps `GITSTORE_GIT_PORT` → field `git_port: u16` — matches all existing env var names
- `dotenvy::dotenv().ok()` must be called before `Config::builder()` so env vars are populated in the process environment before config-rs reads them
- `config-rs` `try_deserialize` fails on the first type mismatch — the custom `AppConfig::validate()` method with `Vec<String>` error collection provides the all-errors-at-once behaviour for semantic validation
- `secrecy::SecretString` implements `serde::Deserialize` (with `features = ["serde"]`) — use for `auth_token` and any future secret fields
- The existing `clap` `Args` struct should be slimmed to CLI-only flags (`--config-file`, `--log-level`); all other config moves to `AppConfig`. Per-field clap overrides are removed
- `File::with_name("gitstore").required(false)` — looks for `gitstore.toml`, `gitstore.yaml`, etc. in the working directory; absent file is not an error

---

## Decision 3: Config File Format

**Decision**: TOML for both services — `config.toml` for `gitstore-api`, `gitstore.toml` for `gitstore-git-service`.

**Rationale**: A single config format across both services reduces operator cognitive load. Users only need to learn one syntax, one set of quoting rules, and one nesting model. TOML is already the default for `gitstore-git-service` (config-rs uses it natively); viper supports TOML equally well via `v.SetConfigType("toml")`. Consistency outweighs the marginal familiarity advantage YAML has in Go tooling.

**Alternatives considered**: YAML for both — works in viper and config-rs but requires the `serde_yaml` feature in Rust and adds a dependency; YAML for Go / TOML for Rust — rejected in favour of consistency.

---

## Decision 4: Env Var Naming for Auth Keys (Go)

**Decision**: Rename all auth env vars to carry the `GITSTORE_AUTH_` prefix (`GITSTORE_AUTH_ADMIN_USERNAME`, `GITSTORE_AUTH_ADMIN_PASSWORD_HASH`, `GITSTORE_AUTH_JWT_SECRET`, `GITSTORE_AUTH_JWT_DURATION`, `GITSTORE_AUTH_JWT_ISSUER`).

**Rationale**: The system is in alpha; breaking changes are expected. Consistent `GITSTORE_` prefix across all keys eliminates the need for explicit `BindEnv` calls — `AutomaticEnv` with `SetEnvPrefix("GITSTORE")` handles every key uniformly, reducing configuration code and operator confusion. The `auth.` viper key subtree maps directly to `GITSTORE_AUTH_*` env vars via the standard key replacer.

**Alternatives considered**: Retain unprefixed names (`JWT_SECRET` etc.) with explicit `BindEnv` — eliminated; no backward compatibility obligation in alpha.

---

## Decision 5: clap Handling in Rust Service

**Decision**: Retain `clap` for `--config-file` and `--log-level` CLI overrides only. Remove all per-field clap args (`--port`, `--ws-port`, `--grpc-port`, `--data-dir`). Apply CLI overrides as `set_override` calls after env var sources in the config-rs builder.

**Rationale**: `clap` is the correct tool for parsing CLI invocation arguments but the wrong tool for managing multi-source layered configuration. Removing per-field clap args eliminates the current dual-definition problem (default in clap + env override in main) while preserving operator ability to override config file path and log level from the command line.

---

## Current Config Key Inventory

### gitstore-api (Go) — 11 keys

| Env Var Key                         | Default                 | Required | Sensitive |
|-------------------------------------|-------------------------|----------|-----------|
| `GITSTORE_API_PORT`                 | `4000`                  | No       | No        |
| `GITSTORE_GIT_WS`                   | `ws://localhost:8080`   | Yes      | No        |
| `GITSTORE_GIT_GRPC`                 | `localhost:50051`       | Yes      | No        |
| `GITSTORE_GIT_HTTP_URL`             | `http://localhost:9418` | Yes      | No        |
| `GITSTORE_CACHE_TTL`                | `300`                   | No       | No        |
| `GITSTORE_LOG_LEVEL`                | `info`                  | No       | No        |
| `GITSTORE_AUTH_ADMIN_USERNAME`      | `""`                    | Yes      | No        |
| `GITSTORE_AUTH_ADMIN_PASSWORD_HASH` | `""`                    | Yes      | Yes       |
| `GITSTORE_AUTH_JWT_SECRET`          | `""`                    | Yes      | Yes       |
| `GITSTORE_AUTH_JWT_DURATION`        | `24h`                   | No       | No        |
| `GITSTORE_AUTH_JWT_ISSUER`          | `gitstore`              | No       | No        |

*Note: `GITSTORE_GIT_GRPC` is read in two files (main.go and grpc_client.go) — this duplicate call-site is eliminated by the single config entry point.*

### gitstore-git-service (Rust) — 5 keys

| Env Var Key                                                    | Default       | Required | Sensitive |
|----------------------------------------------------------------|---------------|----------|-----------|
| `GITSTORE_HTTP_PORT`                                           | `9418`        | No       | No        |
| `GITSTORE_WS_PORT`                                             | `8080`        | No       | No        |
| `GITSTORE_GRPC_PORT`                                           | `50051`       | No       | No        |
| `GITSTORE_DATA_DIR`                                            | `/data/repos` | No       | No        |
| `GITSTORE_LOG_LEVEL`                                           | `info`        | No       | No        |
| `GITSTORE_MAX_FILE_SIZE`                                       | `52428800`    | No       | No        |
| `GITSTORE_HOOKS_GIT_RECEIVE_PACK_PRE_RECEIVE_ENABLED`          | `false`       | No       | No        |
| `GITSTORE_HOOKS_GIT_RECEIVE_PACK_UPDATE_ENABLED`               | `false`       | No       | No        |
| `GITSTORE_HOOKS_GIT_RECEIVE_PACK_POST_RECEIVE_ENABLED`         | `false`       | No       | No        |
| `GITSTORE_HOOKS_GIT_RECEIVE_PACK_PROC_RECEIVE_ENABLED`         | `false`       | No       | No        |
| `GITSTORE_HOOKS_GIT_RECEIVE_PACK_POST_UPDATE_ENABLED`          | `false`       | No       | No        |
| `GITSTORE_ADMISSION_CONTROL_VALIDATING_ADMISSION_POLICY_PHASE` | `pre-receive` | No       | No        |

*Note: All current Rust config keys have defaults; no required-without-default keys exist. Future keys (auth tokens, KV layer) will introduce required sensitive fields.*

> **Implementation note — env var separator for nested structs**: The deeply nested hook and admission control keys (e.g., `GITSTORE_HOOKS_GIT_RECEIVE_PACK_PRE_RECEIVE_ENABLED`) contain underscores in both the struct path separators and the field names themselves. Using `separator("_")` in config-rs would misparse these. The implementation MUST switch to `separator("__")` (double underscore) for struct-level nesting while using single underscores within field names — OR use the config file for nested values and env vars only for top-level keys. The chosen approach must be documented in the implementation task for `gitstore-git-service`.
