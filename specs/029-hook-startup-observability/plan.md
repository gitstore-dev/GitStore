# Implementation Plan: Hook Phase Startup Observability and Env-Var Validation

**Branch**: `029-hook-startup-observability` | **Date**: 2026-06-18 | **Spec**: [spec.md](spec.md)  
**Input**: Feature specification from `/specs/029-hook-startup-observability/spec.md`

## Summary

Add a structured startup log line in `gitstore-git-service/src/main.rs` that enumerates each
`git_receive_pack` hook phase (enabled/disabled) and the active `admission_phase`, so operators
can confirm their environment variables took effect without making a push. Additionally, add
config-level unit tests in `config.rs` that confirm the double-underscore env-var names
(`GITSTORE_HOOKS__GIT_RECEIVE_PACK__<PHASE>__ENABLED`) round-trip correctly through config-rs.

No new dependencies, no datastore changes, no proto changes. Two files in `gitstore-git-service/`.

## Technical Context

**Language/Version**: Rust 1.x  
**Primary Dependencies**: `tracing 0.1`, `config 0.15.22`, `regex 1` (all already present in `Cargo.toml`)  
**Storage**: N/A  
**Testing**: `cargo test` (unit tests in `src/config.rs`)  
**Target Platform**: Linux server (Docker / bare metal)  
**Project Type**: gRPC server binary  
**Performance Goals**: No performance impact — startup path only  
**Constraints**: No new `[dependencies]` entries; startup log emitted at `info` level  
**Scale/Scope**: Two files changed; one new `info!()` call; two new `#[test]` functions

## Constitution Check

| Principle | Status | Notes |
|-----------|--------|-------|
| I. Test-First | ✅ PASS | US2 tests written before the env-var behavior is verified; US1 test written before the log line is added |
| II. API-First | ✅ N/A | No service boundary changes |
| III. Clear Contracts | ✅ N/A | No public API changes |
| IV. Observability | ✅ CORE GOAL | This feature is the observability improvement |
| V. User Story Driven | ✅ PASS | Two independent user stories with acceptance scenarios |
| VI. Incremental Delivery | ✅ PASS | US1 is independently deliverable; US2 adds test coverage |
| VII. Simplicity | ✅ PASS | No new abstractions; minimal code change |

No violations. No complexity justification required.

## Project Structure

### Documentation (this feature)

```text
specs/029-hook-startup-observability/
├── plan.md              # This file
├── spec.md              # Feature spec
└── tasks.md             # Phase 2 output (/speckit.tasks command — NOT created by /speckit.plan)
```

No `data-model.md`, `contracts/`, or `quickstart.md` needed — this feature involves no new
entities, no external API contracts, and no scenario-level test scripts.

### Source Code (repository root)

```text
gitstore-git-service/
├── src/
│   ├── main.rs          # US1: add hook-phase startup log after validate()
│   └── config.rs        # US2: add env-var round-trip tests for hook toggles
└── Cargo.toml           # no changes needed
```

**Structure Decision**: Single Rust crate (`gitstore-git-service`). No new files; both changes are
additions to existing files.

## Phase 0: Research

No unknowns. All decisions are pre-determined by the existing codebase:

| Decision | Rationale | Alternatives Rejected |
|----------|-----------|----------------------|
| Log in `main.rs` after `validate()` | `cfg` is fully resolved there; `tracing` is already initialised at that point | Logging inside `load_config_from()` happens before logging is initialised, so fields would be lost |
| Structured `info!()` fields (not a formatted string) | Consistent with every other log site in `main.rs`; fields are machine-parseable in JSON mode | `format!()` string would break JSON log consumers |
| Tests in `config.rs` (not `main.rs`) | Config loading is what the round-trip tests exercise; `main.rs` is an async binary entry point — hard to unit-test directly | Separate test binary would add complexity |
| No new dependencies | `tracing` already emits structured fields; `HooksConfig` fields are already public | Adding a log-capture crate (e.g. `tracing-test`) would be overkill for verifying field accessibility |

**Output**: No `research.md` file required — all decisions above are derived directly from the
existing code with zero ambiguity.

## Phase 1: Design

### No data model

This feature introduces no new entities, fields, or state transitions. `HooksConfig` and
`HookToggle` already exist in `config.rs`; no schema changes.

### No external contracts

The startup log is an internal observability artifact consumed by operators via log aggregation
(e.g. Docker/journald). It is not part of the gRPC or HTTP API surface. No contract file needed.

### Startup log structure (US1)

After `info!(grpc_port = cfg.grpc.port, data_dir = %cfg.git.data_dir, "Starting GitStore Server")`
(current line 53–57 of `main.rs`), add a second `info!()` call:

```rust
info!(
    pre_receive  = cfg.hooks.git_receive_pack.pre_receive.enabled,
    update       = cfg.hooks.git_receive_pack.update.enabled,
    post_receive = cfg.hooks.git_receive_pack.post_receive.enabled,
    proc_receive = cfg.hooks.git_receive_pack.proc_receive.enabled,
    post_update  = cfg.hooks.git_receive_pack.post_update.enabled,
    reference_transaction = cfg.hooks.git_receive_pack.reference_transaction.enabled,
    admission_phase = %cfg.admission_control.phase,
    "hook phases"
);
```

Log level: `info`. Field names match the TOML key names exactly, ensuring consistency across log
formats (JSON and text).

### Env-var round-trip test structure (US2)

Two new `#[test]` functions appended to the `#[cfg(test)]` block in `config.rs`, each gated by
`ENV_LOCK` (already defined):

```
test_hook_toggle_env_vars_pre_receive_and_post_receive_round_trip
  - Sets GITSTORE_HOOKS__GIT_RECEIVE_PACK__PRE_RECEIVE__ENABLED=false
  - Sets GITSTORE_HOOKS__GIT_RECEIVE_PACK__POST_RECEIVE__ENABLED=false
  - Asserts both fields are false
  - Asserts update/proc_receive/post_update/reference_transaction remain at defaults

test_hook_toggle_env_var_update_enabled_round_trip
  - Sets GITSTORE_HOOKS__GIT_RECEIVE_PACK__UPDATE__ENABLED=true
  - Asserts update.enabled == true
  - Asserts pre_receive.enabled == true (default unchanged)
```

### Agent context update

No new technologies introduced — no update-agent-context script run needed.
