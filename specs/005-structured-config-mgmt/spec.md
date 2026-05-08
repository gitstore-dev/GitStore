# Feature Specification: Structured Configuration Management

**Feature Branch**: `005-structured-config-mgmt`  
**Created**: 2026-05-08  
**Status**: Closed  
**GitHub Issue**: [#116](https://github.com/gitstore-dev/GitStore/issues/116)

## Overview

Both `gitstore-api` (Go) and `gitstore-git-service` (Rust) currently configure themselves by reading environment variables at scattered call sites throughout the codebase. There is no schema, no validation, and no file-based override mechanism. As the configuration surface grows — covering gRPC endpoints, hook phase toggles, admission control, and KV layer options — the lack of structure makes it increasingly difficult to onboard operators, diagnose misconfiguration errors, and maintain consistency across environments.

This feature replaces scattered configuration lookups in both services with a single structured, validated configuration entry point that supports layered loading, actionable startup errors, and local development via `.env` files.

## Clarifications

### Session 2026-05-08

- Q: When a required key is present but set to an empty string, how should the service treat it? → A: Empty string is treated as absent; required keys with empty values fail startup validation.
- Q: When the config file or `.env` contains a key not in the schema, how should the service respond? → A: Log a warning listing unknown keys and continue normally.
- Q: How should sensitive configuration values be handled when logging resolved config at startup? → A: Always redact; log only whether the key is set or unset (e.g., `TOKEN=<redacted>`).
- Q: When no config file path is explicitly provided, how does a service locate its config file? → A: Look for a fixed conventional filename in the current working directory only; no multi-location search.
- Q: Should services log resolved configuration values at startup? → A: Log all resolved configuration at INFO level on every startup; sensitive values are always redacted.

## User Scenarios & Testing *(mandatory)*

### User Story 1 - Operator Deploys Service with Complete Configuration (Priority: P1)

An operator deploying `gitstore-api` or `gitstore-git-service` to a new environment sets required configuration values and starts the service. The service starts successfully with all values loaded from the expected sources.

**Why this priority**: This is the core happy path. Every deployment depends on correct configuration loading. Without it, no downstream behaviour is testable.

**Independent Test**: Can be fully tested by setting all required configuration keys as environment variables, starting the service, and verifying it reaches a healthy ready state with correct values applied.

**Acceptance Scenarios**:

1. **Given** all required configuration keys are set as environment variables, **When** the service starts, **Then** it initialises successfully using those values and serves requests normally.
2. **Given** a config file with some values and environment variables with other values, **When** the service starts, **Then** environment variable values take precedence over config file values for the same keys.
3. **Given** a config file with some values and no environment variables set, **When** the service starts, **Then** config file values are applied for those keys.
4. **Given** no config file and no environment variables, **When** the service starts, **Then** keys with defined defaults use those defaults.

---

### User Story 2 - Operator Receives Actionable Startup Error for Missing Configuration (Priority: P1)

An operator starts a service with one or more required configuration keys missing. Instead of a cryptic panic or silent misconfiguration, the service immediately reports all missing and invalid values in a single human-readable error and exits.

**Why this priority**: Fast-fail with clear errors is critical for operator experience. Silent misconfiguration causes hard-to-diagnose runtime failures; this prevents them entirely.

**Independent Test**: Can be fully tested by starting the service with required keys unset and verifying the output lists every missing key with a description of what is expected.

**Acceptance Scenarios**:

1. **Given** one or more required configuration keys are absent, **When** the service starts, **Then** it exits immediately with a non-zero code listing every missing key.
2. **Given** a required key is present but holds an invalid value (e.g., non-numeric port), **When** the service starts, **Then** it exits with an error identifying the key and describing why the value is invalid.
3. **Given** multiple missing and invalid keys, **When** the service starts, **Then** all issues are reported together in a single error output, not one at a time.

---

### User Story 3 - Developer Uses .env File for Local Development (Priority: P2)

A developer working locally creates a `.env` file at the project root instead of exporting shell environment variables. The service reads this file at startup without requiring any shell configuration.

**Why this priority**: Reduces onboarding friction for contributors and eliminates environment pollution. Local development ergonomics matter for velocity but do not block deployments.

**Independent Test**: Can be fully tested by creating a `.env` file with required keys, starting the service without any exported shell variables, and confirming the service starts successfully.

**Acceptance Scenarios**:

1. **Given** a `.env` file with valid key-value pairs is present, **When** the service starts, **Then** it loads those values as if they were environment variables.
2. **Given** both a `.env` file and actual shell environment variables define the same key, **When** the service starts, **Then** the shell environment variable takes precedence.
3. **Given** no `.env` file is present, **When** the service starts, **Then** it proceeds normally without error (the file is optional).

---

### User Story 4 - Developer Discovers All Configuration Keys via Documentation (Priority: P3)

A developer or operator wants to understand all configuration keys a service accepts, their types, allowed values, defaults, and whether they are required or optional.

**Why this priority**: Documentation enables self-service onboarding and reduces support burden. Valuable, but the service functions without it.

**Independent Test**: Can be fully tested by checking that `docs/` contains an up-to-date reference for each service listing every supported key with type, default, and description.

**Acceptance Scenarios**:

1. **Given** the documentation in `docs/`, **When** an operator reads it, **Then** they can identify every configuration key accepted by each service.
2. **Given** the documentation, **When** an operator reads it, **Then** they can determine which keys are required vs. optional, and what the default value is for each optional key.
3. **Given** a code change that adds, renames, or removes a configuration key, **When** documentation is updated, **Then** the docs accurately reflect the change.

---

### Edge Cases

- What happens when a `.env` file exists but contains a syntax error (e.g., unquoted value with spaces)?
- A required key set to an empty string (`KEY=`) is treated as absent and fails validation with the same error as a missing key.
- When no config file path is provided, the service looks for a fixed conventional filename in the current working directory only. If the file is absent, the service continues without it (file is optional). If an explicit path is provided but the file does not exist, the service fails startup with a clear error.
- Unknown keys in the config file or `.env` file produce a startup warning listing each unrecognised key; the service continues normally.
- What happens if a numeric configuration value exceeds the valid range (e.g., port number > 65535)?

## Requirements *(mandatory)*

### Functional Requirements

- **FR-001**: Each service MUST load all its configuration from a single structured entry point invoked once at startup, before any service logic executes.
- **FR-002**: Configuration MUST be loaded in precedence order: hard-coded defaults → config file → environment variables, with environment variables always taking highest precedence.
- **FR-003**: Each service MUST validate all required configuration keys at startup and fail fast with a non-zero exit code if any required key is absent, set to an empty string, or holds an invalid value. An empty string for a required key is treated as absent.
- **FR-004**: The startup error for missing or invalid configuration MUST list every failing key in a single output, with a human-readable description of the issue for each key.
- **FR-005**: Each service MUST support loading configuration from a `.env` file for local development without requiring shell variable exports; the `.env` file MUST be optional (its absence is not an error).
- **FR-006**: All existing scattered environment-variable lookups in both services MUST be removed and replaced with references to the single structured configuration entry point.
- **FR-007**: Configuration values MUST be typed (e.g., integer ports, boolean toggles, string addresses) and validated against their declared types at startup.
- **FR-008**: All configuration keys supported by each service MUST be documented in `docs/` with their name, type, default value (if any), whether they are required, and a brief description.
- **FR-009**: Configuration tests MUST cover: default values applied when keys are absent, environment variable values override defaults and config file values, invalid values produce descriptive errors, and missing required fields cause startup failure.
- **FR-010**: When the config file or `.env` file contains keys not recognised by the service schema, the service MUST emit a warning at startup identifying each unknown key and then continue initialising normally.
- **FR-011**: Configuration keys declared as sensitive (e.g., tokens, passwords, private keys) MUST have their values permanently redacted in all log output; logs MUST indicate only whether such a key is set or unset (e.g., `TOKEN=<redacted>`).
- **FR-012**: Each service MUST look for its config file using a fixed conventional filename in the current working directory. If no config file is found at that location, the service MUST continue normally. If an explicit config file path is provided and that file does not exist, the service MUST fail startup with a clear error identifying the missing path.
- **FR-013**: Each service MUST log its fully resolved configuration at INFO level during startup. All keys declared as sensitive MUST be redacted per FR-011; all other resolved values MUST be included.

### Key Entities

- **Configuration Schema**: The full set of typed, named keys a service accepts, including type, required/optional status, default value, and validation rules.
- **Config Source**: An origin of configuration values — one of: hard-coded defaults, config file, or environment variables (including `.env` file). Sources are applied in precedence order.
- **Configuration Entry Point**: The single location in each service where all config sources are merged, validated, and exposed to the rest of the service as a typed struct.

## Success Criteria *(mandatory)*

### Measurable Outcomes

- **SC-001**: A service started with missing required configuration produces an error message that names every missing key within the first output lines of startup — verified by test.
- **SC-002**: Zero `os.Getenv` / `env::var` call sites remain outside the configuration entry point in either service after this feature is complete — verified by code search.
- **SC-003**: A developer can configure a service for local development by creating a single `.env` file without exporting any shell variables — verified by running the service with only a `.env` file present.
- **SC-004**: All configuration keys for both services are discoverable from `docs/` alone, with no need to read service source code — verified by documentation review.
- **SC-005**: Configuration tests pass for default values, env overrides, malformed inputs, and missing required fields across both services — verified by test suite results.
- **SC-006**: An operator can determine the full resolved configuration of a running service instance from its startup log output alone, without accessing env files or config files directly — verified by log review.

## Assumptions

- Config file format will follow the convention already established or most natural for each service's ecosystem (e.g., TOML or YAML for Rust, YAML or TOML for Go); the exact format is a planning-phase decision.
- Both the `.env` file and the config file are looked up from the current working directory using fixed conventional filenames. No multi-location search is performed. If an explicit path to a config file is provided and the file is absent, startup fails with a clear error; if no path is provided and no file is found, the service continues normally.
- Configuration keys for near-term features (gRPC endpoints, hook phases, admission control, KV layer) are included in the schema design, even if those features are not yet fully implemented, to prevent future schema churn.
- Secret values (tokens, passwords) are accepted from environment variables only; no secrets are written to config files in version control. Sensitive keys are always redacted in log output regardless of log level (see FR-011).

## Dependencies

- **GH#117**: gitstore-api (Go) structured configuration implementation (subtask).
- **GH#118**: gitstore-git-service (Rust) structured configuration implementation (subtask).
