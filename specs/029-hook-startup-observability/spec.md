# Feature Specification: Hook Phase Startup Observability and Env-Var Validation

**Feature Branch**: `029-hook-startup-observability`  
**Created**: 2026-06-18  
**Status**: Closed  
**Input**: Config-Driven git-receive-pack Hook Orchestration and Admission Phase Routing (issue #107)

## User Scenarios & Testing *(mandatory)*

### User Story 1 — Startup log enumerates enabled hook phases (Priority: P1)

As an operator deploying the git service, I want a startup log line that tells me which
git-receive-pack hook phases are enabled and what the active admission phase is, so that I
can confirm my environment variables took effect without attaching a debugger.

**Why this priority**: Misconfigurations (e.g. admission phase accidentally disabled, wrong phase
chosen) are currently invisible until a push is made. A startup log reduces operational blind spots
immediately at boot time.

**Independent Test**: Start the git service binary with a known set of `GITSTORE_HOOKS__*` variables
and assert that the first emitted JSON log contains a `hook_phases` field (or equivalent) listing
every phase and its enabled state plus `admission_phase`.

**Acceptance Scenarios**:

1. **Given** the service starts with default configuration, **When** the first log line is emitted,
   **Then** it contains the enabled/disabled status of each `git_receive_pack` hook phase
   (`pre_receive`, `update`, `post_receive`, `proc_receive`, `post_update`, `reference_transaction`)
   and the configured `admission_phase` value.

2. **Given** `GITSTORE_HOOKS__GIT_RECEIVE_PACK__UPDATE__ENABLED=true` is set before startup,
   **When** the service starts, **Then** the startup log shows `update` as enabled.

3. **Given** an operator tails the service log, **When** no push has yet been received,
   **Then** the startup log is sufficient to confirm that the desired hook phases are active.

---

### User Story 2 — Env-var names for hook toggles round-trip correctly (Priority: P2)

As a developer writing deployment configuration, I want confirmation that
`GITSTORE_HOOKS__GIT_RECEIVE_PACK__<PHASE>__ENABLED` is the correct environment variable name
for each phase toggle, so that I can set these variables in Docker Compose / Kubernetes without
guessing whether the separator is `_` or `__`.

**Why this priority**: The double-underscore separator convention is subtle. A misplaced single
underscore silently falls back to the compiled-in default. A config-level test pins the exact
env-var names and prevents silent regressions if config-rs or the struct field names change.

**Independent Test**: Set `GITSTORE_HOOKS__GIT_RECEIVE_PACK__PRE_RECEIVE__ENABLED=false` and
`GITSTORE_HOOKS__GIT_RECEIVE_PACK__POST_RECEIVE__ENABLED=false` in the test environment and call
`load_config_from(None)`. Assert the loaded values match what was set.

**Acceptance Scenarios**:

1. **Given** `GITSTORE_HOOKS__GIT_RECEIVE_PACK__PRE_RECEIVE__ENABLED=false`, **When** the config
   is loaded, **Then** `hooks.git_receive_pack.pre_receive.enabled` equals `false`.

2. **Given** `GITSTORE_HOOKS__GIT_RECEIVE_PACK__POST_RECEIVE__ENABLED=false`, **When** the config
   is loaded, **Then** `hooks.git_receive_pack.post_receive.enabled` equals `false`.

3. **Given** no env vars are set, **When** the config is loaded, **Then** the default values
   defined in `default_toml()` are preserved (`pre_receive` enabled, `post_receive` enabled,
   all others disabled).

4. **Given** `GITSTORE_HOOKS__GIT_RECEIVE_PACK__UPDATE__ENABLED=true`, **When** the config is
   loaded, **Then** `hooks.git_receive_pack.update.enabled` equals `true`.

---

### Edge Cases

- What happens when both `pre_receive` and `post_receive` are disabled? The startup log must still
  emit the full phase table; no panic or silent omission.
- What happens when an env var uses a single underscore (e.g. `GITSTORE_HOOKS_GIT_RECEIVE_PACK_PRE_RECEIVE_ENABLED`)?
  The value must be silently ignored; the default must apply.

## Requirements *(mandatory)*

### Functional Requirements

- **FR-001**: At service startup, the system MUST emit a structured log entry that includes the
  enabled/disabled state of every `git_receive_pack` hook phase.
- **FR-002**: The startup log MUST include the configured `admission_control.phase` value
  alongside the hook phase table.
- **FR-003**: The startup log entry MUST be emitted at the `info` level so it appears under the
  default log level.
- **FR-004**: The env-var name for each hook toggle MUST follow the pattern
  `GITSTORE_HOOKS__GIT_RECEIVE_PACK__<PHASE_UPPER>__ENABLED` using double-underscore separators.
- **FR-005**: Config loading MUST correctly deserialise a `bool` value set via the env-var names
  defined in FR-004 for every `git_receive_pack` hook phase.
- **FR-006**: Existing phase-conflict and branch-pattern validation MUST continue to pass without
  modification.

## Success Criteria *(mandatory)*

### Measurable Outcomes

- **SC-001**: After starting the service, a single grep on the log output (`hook_phases` or
  equivalent field name) returns exactly one match containing all six phase names.
- **SC-002**: All config unit tests in `gitstore-git-service/src/config.rs` pass, including two
  new env-var round-trip tests (one per hook phase pair tested).
- **SC-003**: The full Rust test suite (`cargo test`) passes with zero regressions.
- **SC-004**: Operator can confirm hook configuration without any push to the repository — log
  evidence alone is sufficient.

## Assumptions

- The startup log is emitted from `main.rs` immediately after `AppConfig::validate()` succeeds,
  using the existing `tracing` infrastructure already in place.
- No new dependencies are required; `tracing` is already a direct dependency.
- The log format (JSON or text) is controlled by `GITSTORE_LOG__FORMAT`, so the structured field
  names must be stable regardless of format.
- Phase names in the log match the TOML key names (`pre_receive`, `update`, `post_receive`,
  `proc_receive`, `post_update`, `reference_transaction`) for consistency with the configuration
  surface.
