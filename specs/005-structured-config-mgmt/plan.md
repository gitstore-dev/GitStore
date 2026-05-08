# Implementation Plan: Structured Configuration Management

**Branch**: `005-structured-config-mgmt` | **Date**: 2026-05-08 | **Spec**: [spec.md](spec.md)  
**Input**: Feature specification from `/specs/005-structured-config-mgmt/spec.md`

## Summary

Replace all scattered `os.Getenv` / `env::var` call sites in `gitstore-api` (Go) and `gitstore-git-service` (Rust) with a single structured, validated configuration entry point per service. 
Each service will use an idiomatic configuration library for its ecosystem — `spf13/viper` for Go and `config-rs` for Rust — with layered loading (defaults → config file → environment variables), `.env` 
file support via `joho/godotenv` (Go) and `dotenvy` (Rust), fail-fast all-errors-at-once validation, and INFO-level startup logging with permanent redaction of sensitive fields.

## Technical Context

**Language/Version**: Go 1.25 (`gitstore-api`), Rust edition 2021 (`gitstore-git-service`)  
**Primary Dependencies**:
- Go: `github.com/spf13/viper` v1.20 (TOML via `v.SetConfigType("toml")`), `github.com/joho/godotenv`, `github.com/go-playground/validator/v10`
- Rust: `config` 0.14, `dotenvy` 0.15, `validator` 0.18, `secrecy` 0.10

**Storage**: N/A — configuration is in-memory after startup load  
**Testing**: `go test` with `testify` (Go); `cargo test` (Rust)  
**Target Platform**: Linux server (containerised deployment)  
**Project Type**: Web service / gRPC API (Go), gRPC service (Rust)  
**Performance Goals**: Config load completes before first request; not on critical latency path  
**Constraints**:
- Config file MUST remain optional — containers rely solely on env vars
- Existing env var key names (`GITSTORE_API_PORT`, `GITSTORE_GIT_GRPC`, etc.) MUST be preserved for backward compatibility
- No secrets (`JWT_SECRET`, `ADMIN_PASSWORD_HASH`, etc.) are ever written to config files
- Empty string for a required key is treated as absent (fails validation)

**Scale/Scope**: ~11 config keys in `gitstore-api`, ~5 in `gitstore-git-service`; schema designed to accommodate ~30 keys as surface grows

## Constitution Check

*GATE: Must pass before Phase 0 research. Re-check after Phase 1 design.*

| Principle                         | Status   | Notes                                                                                                                                            |
|-----------------------------------|----------|--------------------------------------------------------------------------------------------------------------------------------------------------|
| I. Test-First Development         | **PASS** | Config tests (defaults, overrides, validation errors, missing required) must be written and confirmed failing before any implementation begins   |
| II. API-First Design              | **PASS** | No new external API surface. Config schema is the internal contract — documented in `contracts/` before implementation                           |
| III. Clear Contracts & Versioning | **PASS** | Env var names are a stable operator-facing contract; documented in `contracts/config-schema.md` and `docs/`                                      |
| IV. Observability                 | **PASS** | FR-013 mandates INFO-level startup config log with sensitive value redaction; aligns with constitution structured logging requirement            |
| V. User Story Driven              | **PASS** | 4 user stories (P1/P1/P2/P3) with independent acceptance criteria                                                                                |
| VI. Incremental Delivery          | **PASS** | P1 (core loading + validation) → P2 (.env support) → P3 (documentation); each delivers standalone value                                          |
| VII. Simplicity                   | **PASS** | One library per ecosystem; no new abstractions beyond a single `config` package per service; no premature RBAC, remote config, or dynamic reload |

## Project Structure

### Documentation (this feature)

```text
specs/005-structured-config-mgmt/
├── plan.md              # This file (/speckit.plan command output)
├── research.md          # Phase 0 output (/speckit.plan command)
├── data-model.md        # Phase 1 output (/speckit.plan command)
├── quickstart.md        # Phase 1 output (/speckit.plan command)
├── contracts/           # Phase 1 output (/speckit.plan command)
│   └── config-schema.md
└── tasks.md             # Phase 2 output (/speckit.tasks command)
```

### Source Code (repository root)

```text
gitstore-api/
├── internal/
│   └── config/
│       ├── config.go          # Config struct + Load() entry point
│       └── config_test.go     # Unit tests (written first)
├── cmd/
│   └── server/
│       └── main.go            # Updated: remove getEnv/getEnvInt helpers; call config.Load()
├── internal/
│   ├── auth/
│   │   └── session.go         # Updated: receive config, remove os.Getenv
│   ├── middleware/
│   │   └── auth.go            # Updated: receive config, remove os.Getenv
│   ├── logger/
│   │   └── logger.go          # Updated: receive log level from config
│   └── gitclient/
│       └── grpc_client.go     # Updated: receive gRPC address from config
└── go.mod                     # Add viper, godotenv, validator dependencies

gitstore-git-service/
├── src/
│   ├── config.rs              # AppConfig struct + load_config() entry point
│   └── main.rs                # Updated: call load_config(); remove env::var calls; slim clap
└── Cargo.toml                 # Add config, dotenvy, validator, secrecy dependencies
```

**Structure Decision**: Each service gets a single new source file (`config.go` / `config.rs`) containing the config struct and load function. No additional packages or modules are introduced. Dependency injection via function arguments replaces all ambient `os.Getenv` / `env::var` calls.

## Complexity Tracking

No constitution violations. Both library choices (`spf13/viper`, `config-rs`) are idiomatic for their respective ecosystems and introduce no novel abstractions.
