# Tasks: Structured Configuration Management

**Input**: Design documents from `/specs/005-structured-config-mgmt/`  
**Prerequisites**: plan.md ✅ spec.md ✅ research.md ✅ data-model.md ✅ contracts/ ✅ quickstart.md ✅

**Tests**: Test-First Development (Constitution Principle I — NON-NEGOTIABLE). Tests MUST be written before implementation code, confirmed failing (red), then implementation follows (green).

**Organization**: Tasks are grouped by user story to enable independent implementation and testing of each story. Both `gitstore-api` (Go) and `gitstore-git-service` (Rust) are treated as parallel workstreams within each phase.

## Format: `[ID] [P?] [Story?] Description`

- **[P]**: Can run in parallel (different files, no dependencies on other in-progress tasks)
- **[Story]**: User story this task belongs to (US1–US4)

---

## Phase 1: Setup

**Purpose**: Add new dependencies to both services before any code is written.

- [x] T001 Add Go dependencies to `gitstore-api/go.mod`: `github.com/spf13/viper`, `github.com/joho/godotenv`, `github.com/go-playground/validator/v10`
- [x] T002 [P] Add Rust dependencies to `gitstore-git-service/Cargo.toml`: `config = "0.14"`, `dotenvy = "0.15"`, `validator = "0.18"` with derive feature, `secrecy = "0.10"` with serde feature

---

## Phase 2: Foundational (Blocking Prerequisites)

**Purpose**: Define the typed config struct skeletons with a non-functional stub entry point so that test files compile against the correct types. No loading logic yet.

**⚠️ CRITICAL**: No user story work can begin until this phase is complete.

- [x] T003 [P] Define Go config struct hierarchy (`Config`, `ApiConfig`, `GitConfig`, `AuthConfig`, `CacheConfig`) with `mapstructure` tags and a non-functional `Load() (*Config, error)` stub returning `nil, nil` in `gitstore-api/internal/config/config.go`
- [x] T004 [P] Define Rust config struct hierarchy (`AppConfig`, `HooksConfig`, `GitReceivePackHooks`, `HookToggle`, `AdmissionControlConfig`, `ValidatingAdmissionPolicyConfig`) with `#[derive(Debug, serde::Deserialize)]` and a non-functional `pub fn load_config() -> Result<AppConfig, config::ConfigError>` stub in `gitstore-git-service/src/config.rs`

**Checkpoint**: Struct types exist and entry points compile — test files can now be written against the real API.

---

## Phase 3: User Story 1 — Operator Deploys Service with Complete Configuration (Priority: P1) 🎯 MVP

**Goal**: Both services load all configuration from a single entry point, merge defaults → config file → env vars in correct precedence order, and emit a fully resolved (sensitive-redacted) INFO log at startup.

**Independent Test**: Set all required keys as environment variables, start each service, verify it reaches healthy state and the startup log contains all resolved values with sensitive fields showing `<redacted>`.

### Tests for User Story 1

> **Write these tests FIRST — verify they FAIL before implementing.**

- [x] T005 [P] [US1] Write failing Go tests for layered loading: defaults applied when no source set, env var overrides default, config file value applied when no env var, env var overrides config file — in `gitstore-api/internal/config/config_test.go`
- [x] T006 [P] [US1] Write failing Rust tests for layered loading: same four scenarios — in `gitstore-git-service/src/config.rs` (`#[cfg(test)]` inline module)
- [x] T007 [P] [US1] Write failing Go test: startup log output contains all non-sensitive keys and shows `<redacted>` for `AdminPasswordHash` and `JWTSecret` — in `gitstore-api/internal/config/config_test.go`
- [x] T008 [P] [US1] Write failing Rust test: `AppConfig` debug/display output shows all fields without exposing sensitive values — in `gitstore-git-service/src/config.rs`

### Implementation for User Story 1

- [x] T009 [P] [US1] Implement Go `config.Load()` in `gitstore-api/internal/config/config.go`: `godotenv.Load()` → viper defaults → `v.SetConfigName("config")`, `v.SetConfigType("toml")`, `v.AddConfigPath(".")`, `v.ReadInConfig()` (allow `ConfigFileNotFoundError`) → `v.SetEnvPrefix("GITSTORE")`, `v.SetEnvKeyReplacer(strings.NewReplacer(".", "_"))`, `v.AutomaticEnv()` → `v.UnmarshalExact(&cfg)`
- [x] T010 [P] [US1] Implement Rust `load_config()` in `gitstore-git-service/src/config.rs`: `Config::builder()` with all defaults → `File::with_name("gitstore").required(false)` → `Environment::with_prefix("GITSTORE").separator("_").try_parsing(true)` → `cfg.try_deserialize::<AppConfig>()`; add implementation note for nested hook/admission keys: load from TOML tables in `gitstore.toml`; env var overrides for nested keys require `__` separator (document as known limitation)
- [x] T011 [US1] Implement startup INFO log with sensitive-field redaction in `gitstore-api/internal/config/config.go`: implement `zap.ObjectMarshaler` on `Config`; `redact(s string) string` returns `"<redacted>"` if set, `"<unset>"` if empty; log after successful load
- [x] T012 [US1] Implement startup INFO log for Rust `AppConfig` in `gitstore-git-service/src/config.rs`: implement `std::fmt::Display` on `AppConfig` printing all fields; wrap future secret fields in `secrecy::SecretString`; call `tracing::info!` after successful load
- [x] T013 [US1] Update `gitstore-api/cmd/server/main.go`: call `config.Load()` as first action in `main()`; remove `getEnv`/`getEnvInt` helpers; pass `*Config` to all downstream constructors by dependency injection
- [x] T014 [US1] Update `gitstore-api/internal/auth/session.go`: replace all `os.Getenv` calls (`JWT_SECRET`, `JWT_DURATION`, `JWT_ISSUER`) with fields from injected `*AuthConfig`
- [x] T015 [US1] Update `gitstore-api/internal/middleware/auth.go`: replace `os.Getenv` calls (`ADMIN_USERNAME`, `ADMIN_PASSWORD_HASH`) with fields from injected `*AuthConfig`
- [x] T016 [US1] Update `gitstore-api/internal/logger/logger.go`: replace `os.Getenv("GITSTORE_LOG_LEVEL")` with `logLevel string` parameter received from `Config.LogLevel`
- [x] T017 [US1] Update `gitstore-api/internal/gitclient/grpc_client.go`: replace `os.Getenv("GITSTORE_GIT_GRPC")` with `grpcAddr string` parameter received from `Config.Git.GRPC`; remove duplicate env read
- [x] T018 [US1] Update `gitstore-git-service/src/main.rs`: call `load_config()` after `dotenvy::dotenv().ok()`; remove all `env::var` calls; slim `clap` `Args` struct to `--config-file: Option<String>` and `--log-level: Option<String>` only; apply CLI overrides as `set_override` calls in config builder

**Checkpoint**: Both services start successfully with full config set via env vars and emit a correctly redacted startup log. No `os.Getenv` / `env::var` call sites remain outside the config entry points.

---

## Phase 4: User Story 2 — Operator Receives Actionable Startup Error (Priority: P1)

**Goal**: Both services fail fast at startup with a single error output listing every missing, empty, or invalid configuration key when required config is absent or malformed.

**Independent Test**: Start each service with required keys unset; verify it exits non-zero and the output names every failing key in one message.

### Tests for User Story 2

> **Write these tests FIRST — verify they FAIL before implementing.**

- [x] T019 [P] [US2] Write failing Go tests for validation: missing required key exits with error naming that key; empty string for required key treated as absent; invalid port (>65535) named in error; multiple failures reported together in single error string — in `gitstore-api/internal/config/config_test.go`
- [x] T020 [P] [US2] Write failing Rust tests for validation: port out of range named; `data_dir` empty fails; port uniqueness constraint violated; all errors collected into one `ConfigErrors` — in `gitstore-git-service/src/config.rs`
- [x] T021 [P] [US2] Write failing Go test: unknown key in `config.toml` produces a log warning and does not abort startup — in `gitstore-api/internal/config/config_test.go`
- [x] T022 [P] [US2] Write failing Rust test: unknown key in `gitstore.toml` produces a `tracing::warn!` and does not abort startup — in `gitstore-git-service/src/config.rs`

### Implementation for User Story 2

- [x] T023 [P] [US2] Add `validate` struct tags to Go `Config` hierarchy fields (`required`, `min`, `max`, `url`, `omitempty,duration`) and implement `validateConfig(cfg *Config) error` using `go-playground/validator/v10` that collects all `ValidationErrors` into a single formatted string — in `gitstore-api/internal/config/config.go`
- [x] T024 [P] [US2] Implement `AppConfig::validate(&self) -> Result<(), ConfigErrors>` in `gitstore-git-service/src/config.rs`: check each field rule, collect all failures into `Vec<String>`, return all at once; implement `ConfigErrors` with `Display` joining all messages
- [x] T025 [US2] Handle unknown keys (FR-010) in Go `config.Load()`: after `UnmarshalExact`, if error contains unknown field names extract and log each as `zap.Warn("unknown configuration key", ...)` then continue — in `gitstore-api/internal/config/config.go`
- [x] T026 [US2] Handle unknown keys (FR-010) in Rust `load_config()`: after successful build, check for unrecognised keys and emit `tracing::warn!` for each; continue normally — in `gitstore-git-service/src/config.rs`

**Checkpoint**: Both services exit non-zero with all validation errors listed when misconfigured. Unknown keys produce a startup warning. No silent misconfiguration possible.

---

## Phase 5: User Story 3 — Developer Uses .env File for Local Development (Priority: P2)

**Goal**: Both services load configuration from a `.env` file in the working directory at startup, without requiring shell variable exports. Absent `.env` file is not an error. Shell env vars override `.env` values.

**Independent Test**: Create `.env` with all required keys, unset all shell vars, start each service — verify successful startup.

### Tests for User Story 3

> **Write these tests FIRST — verify they FAIL before implementing.**

- [x] T027 [P] [US3] Write failing Go tests for `.env` loading: service starts with only `.env` present (no shell vars); shell var overrides `.env` value for same key; absent `.env` is no-op — in `gitstore-api/internal/config/config_test.go`
- [x] T028 [P] [US3] Write failing Rust tests for `.env` loading: same three scenarios — in `gitstore-git-service/src/config.rs`

### Implementation for User Story 3

- [x] T029 [P] [US3] Add `godotenv.Load()` as the first call inside `config.Load()` before any viper setup in `gitstore-api/internal/config/config.go` (silent no-op if `.env` absent via `_ = godotenv.Load()`)
- [x] T030 [P] [US3] Add `dotenvy::dotenv().ok()` as the very first line of `main()` in `gitstore-git-service/src/main.rs`, before `Cli::parse()` and `load_config()`
- [x] T031 [P] [US3] Create `gitstore-api/.env.example` listing all supported env vars with types, defaults, and required/optional status based on `contracts/config-schema.md`
- [x] T032 [P] [US3] Create `gitstore-git-service/.env.example` listing all supported env vars with types, defaults, and required/optional status based on `contracts/config-schema.md`

**Checkpoint**: Developer can clone the repo, copy `.env.example` to `.env`, fill in secrets, and start either service without any shell configuration.

---

## Phase 6: User Story 4 — Configuration Keys Discoverable via Documentation (Priority: P3)

**Goal**: All configuration keys for both services documented in `docs/configuration.md` — name, type, default, required/optional, description — with no need to read source code.

**Independent Test**: Read `docs/configuration.md` alone and locate every key in `contracts/config-schema.md` with its type, default, and required status.

- [x] T033 [US4] Write `docs/configuration.md` with full operator reference for both services derived from `specs/005-structured-config-mgmt/contracts/config-schema.md`: all sections (Core, Git Service Connection, Authentication, Cache, Logging, Hook Phase Toggles, Admission Control), source precedence diagram, sensitive value handling note, `.env.example` usage, and config file examples in TOML
- [x] T034 [P] [US4] Cross-check `docs/configuration.md` against `specs/005-structured-config-mgmt/contracts/config-schema.md`: verify every key row is present, defaults match, required/optional status matches, sensitive flags match

**Checkpoint**: `docs/configuration.md` is the single source operators need to configure both services.

---

## Phase 7: Polish & Cross-Cutting Concerns

**Purpose**: Quality gates, license headers, and end-to-end validation before the feature is merged.

- [x] T035 [P] Run Go quality checks in `gitstore-api/`: `go fmt ./...`, `go vet ./...`, `staticcheck ./...`, `go test -v -race -coverprofile=coverage.txt ./internal/config/...`
- [x] T036 [P] Run Rust quality checks in `gitstore-git-service/`: `cargo fmt --all -- --check`, `cargo clippy --all-targets --all-features -- -D warnings`, `cargo test --verbose`
- [x] T037 [P] Check Go license headers: `./scripts/check-go-license-headers.sh --all` and `./scripts/check-go-license-headers.sh --diff-base origin/main`
- [x] T038 [P] Check Rust license headers: `./scripts/check-rust-license-headers.sh --all` and `./scripts/check-rust-license-headers.sh --diff-base origin/main`
- [x] T039 Validate `specs/005-structured-config-mgmt/quickstart.md` end-to-end: follow each scenario against running services and confirm startup log output, error output, and `.env` behaviour match the documented examples

---

## Dependencies & Execution Order

### Phase Dependencies

- **Setup (Phase 1)**: No dependencies — start immediately; T001 and T002 are parallel
- **Foundational (Phase 2)**: Depends on Phase 1 — T003 and T004 are parallel; BLOCKS all user story phases
- **US1 (Phase 3)**: Depends on Phase 2 — tests (T005–T008) written first; implementation (T009–T018) follows after tests confirmed failing
- **US2 (Phase 4)**: Depends on Phase 3 implementation complete (validation is called inside `Load()`/`load_config()`)
- **US3 (Phase 5)**: Depends on Phase 3 — `.env` loading hooks into the existing `Load()` call
- **US4 (Phase 6)**: Depends on all prior phases — documents the final schema
- **Polish (Phase 7)**: Depends on all user story phases complete

### User Story Dependencies

- **US1 (P1)**: Can start after Foundational — no dependency on other stories
- **US2 (P1)**: Depends on US1 structs and entry points existing (adds validation to the same functions)
- **US3 (P2)**: Depends on US1 — adds `.env` loading as a single call at the top of the existing entry points
- **US4 (P3)**: Depends on US1–US3 — documents the final, stable schema

### Within Each User Story

1. Write tests → confirm they FAIL (red)
2. Implement → confirm tests PASS (green)
3. Refactor if needed → confirm tests still pass
4. Go tasks and Rust tasks within the same phase are parallel

### Parallel Opportunities

- T001 ‖ T002 (Phase 1)
- T003 ‖ T004 (Phase 2)
- T005 ‖ T006 ‖ T007 ‖ T008 (US1 tests)
- T009 ‖ T010 (US1 loading implementation, Go vs Rust)
- T011 ‖ T012 (US1 startup log, Go vs Rust)
- T013 → T014 ‖ T015 ‖ T016 ‖ T017 (US1 call-site cleanup, all after main.go is wired)
- T019 ‖ T020 ‖ T021 ‖ T022 (US2 tests)
- T023 ‖ T024 (US2 validation implementation, Go vs Rust)
- T027 ‖ T028 (US3 tests)
- T029 ‖ T030 ‖ T031 ‖ T032 (US3 implementation + examples)
- T035 ‖ T036 ‖ T037 ‖ T038 (Polish checks)

---

## Parallel Example: User Story 1

```bash
# Step 1 — write all US1 tests together (all parallel):
T005: Go layered-loading tests        → gitstore-api/internal/config/config_test.go
T006: Rust layered-loading tests      → gitstore-git-service/src/config.rs
T007: Go startup log test             → gitstore-api/internal/config/config_test.go
T008: Rust startup log test           → gitstore-git-service/src/config.rs

# Step 2 — confirm all FAIL, then implement in parallel:
T009: Go config.Load() implementation → gitstore-api/internal/config/config.go
T010: Rust load_config() impl         → gitstore-git-service/src/config.rs

# Step 3 — startup log (Go and Rust parallel):
T011: Go MarshalLogObject + redact()  → gitstore-api/internal/config/config.go
T012: Rust Display + tracing::info!   → gitstore-git-service/src/config.rs

# Step 4 — call-site cleanup (Go files parallel after T013):
T014 ‖ T015 ‖ T016 ‖ T017: Update each internal Go package
T018: Slim Rust main.rs               → gitstore-git-service/src/main.rs
```

---

## Implementation Strategy

### MVP First (User Story 1 + User Story 2)

Both P1 stories must ship together — a service that loads config but does not validate it is not safe to deploy.

1. Complete Phase 1: Setup
2. Complete Phase 2: Foundational — structs + stubs
3. Complete Phase 3: US1 — full loading + startup log + DI wiring
4. Complete Phase 4: US2 — fail-fast validation + unknown key warnings
5. **STOP and VALIDATE**: run `go test -race ./internal/config/...` and `cargo test`; start each service with valid config (happy path) and with missing config (error path); verify outputs match `quickstart.md`
6. Deploy / demo MVP

### Incremental Delivery

1. Setup + Foundational → types and stubs compile
2. US1 → both services load config and start; startup log visible
3. US2 → misconfigured deployments fail loudly with actionable errors
4. US3 → local development works with `.env` files only
5. US4 → operators self-serve from `docs/configuration.md`

---

## Notes

- `[P]` tasks operate on different files with no shared state — safe to run concurrently
- Go and Rust tasks within the same phase are always parallel workstreams
- Constitution Principle I is NON-NEGOTIABLE: no implementation task may begin until its test task is complete and the test is confirmed **failing**
- The nested Rust hook/admission keys (`GITSTORE_HOOKS_*`, `GITSTORE_ADMISSION_CONTROL_*`) conflict with config-rs `separator("_")` — T010 must resolve this: load nested values from `gitstore.toml` TOML tables; document env var override behaviour for nested keys in the implementation
- Commit after each logical group (per-story or per-task where appropriate)
- Verify all `os.Getenv` and `env::var` call sites are eliminated before closing the feature: `grep -r "os.Getenv" gitstore-api/` and `grep -r "env::var\|std::env::var" gitstore-git-service/src/` should return empty results (excluding `internal/config/`)
