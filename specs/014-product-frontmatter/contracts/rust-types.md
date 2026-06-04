# Contract: Rust Admission Delegation — Git Service Boundary

**Crate**: `gitstore-server` (`gitstore-git-service`)  
**Feature**: 014-product-frontmatter  
**Date**: 2026-06-01

## Architecture

The `gitstore-git-service` is a **dumb git transport layer** — no Markdown knowledge, no business logic. All validation and admission control lives in `gitstore-api` (Go). The git service makes two independent callouts at configurable hook phases.

---

## No New Rust Code in This Feature

Feature 014 requires no changes to `gitstore-git-service`. The Rust changes described below are **deferred to GH#105/106** where the concrete `AdmissionHandler` implementations are wired. They are documented here so those issues can scope them correctly.

---

## Two Independent Callout Phases

| Callout           | Phase          | Env var                             | Default        | Blocking?            | Concern                                                |
|-------------------|----------------|-------------------------------------|----------------|----------------------|--------------------------------------------------------|
| Schema validation | `pre-receive`  | `GITSTORE_SCHEMA_VALIDATION__PHASE` | `pre-receive`  | Yes — rejects push   | Structural schema check (kind, name, forbidden fields) |
| Admission control | `post-receive` | `GITSTORE_ADMISSION_CONTROL__PHASE` | `post-receive` | No — fire-and-forget | Mutating + validating admission, datastore hydration   |

---

## Rust Config Changes (deferred to GH#105/106)

### Remove `ValidatingAdmissionPolicyConfig`

`validating_admission_policy` is superseded. The Go API owns the distinction between schema validation and admission control.

```rust
// REMOVE from AdmissionControlConfig:
pub struct ValidatingAdmissionPolicyConfig {
    pub phase: String,
}
```

### New `SchemaValidationConfig`

```rust
#[derive(Debug, serde::Deserialize)]
pub struct SchemaValidationConfig {
    pub phase: String,   // default: "pre-receive"
}
```

TOML default:
```toml
[schema_validation]
phase = "pre-receive"
```

Env var override: `GITSTORE_SCHEMA_VALIDATION__PHASE=pre-receive`

### Updated `AdmissionControlConfig`

```rust
#[derive(Debug, serde::Deserialize)]
pub struct AdmissionControlConfig {
    pub phase: String,   // default: "post-receive"
                         // replaces validating_admission_policy.phase
}
```

TOML default:
```toml
[admission_control]
phase = "post-receive"
```

Env var override: `GITSTORE_ADMISSION_CONTROL__PHASE=post-receive`

### Updated `AppConfig`

```rust
pub struct AppConfig {
    pub grpc: PortConfig,
    pub git: GitConfig,
    pub log: LogConfig,
    pub hooks: HooksConfig,
    pub schema_validation: SchemaValidationConfig,    // NEW
    pub admission_control: AdmissionControlConfig,    // CHANGED (phase field only)
}
```

---

## HookPipeline Changes (deferred to GH#105/106)

The current `HookPipeline` has one `admission_phase` + `admission_handler`. It needs two independent handler slots:

```rust
pub struct HookPipeline {
    pub config: GitReceivePackHooks,
    // Schema validation — blocking, called at schema_validation.phase
    pub schema_validation_phase: String,
    pub schema_validation_handler: Arc<dyn AdmissionHandler + Send + Sync>,
    // Admission control — fire-and-forget, spawned at admission_control.phase
    pub admission_control_phase: String,
    pub admission_control_handler: Arc<dyn AdmissionHandler + Send + Sync>,
}
```

`NoopAdmissionHandler` is the default for both until GH#105/106 wire the concrete implementations.

The `run_post_receive` method spawns the admission control callout as an async task (non-blocking):

```rust
pub fn run_post_receive(&self, git_dir: &Path, updates: &[RefUpdate]) {
    if !self.config.post_receive.enabled { return; }
    // Fire-and-forget admission control
    let handler = Arc::clone(&self.admission_control_handler);
    let updates = updates.to_vec();
    tokio::spawn(async move {
        match handler.admit("post-receive", &updates).await {
            Ok(AdmissionDecision::Accept) => {}
            Ok(AdmissionDecision::Reject(reason)) => {
                tracing::error!(reason, "admission_control_rejected_post_receive");
            }
            Err(e) => {
                tracing::error!(error = %e, "admission_control_error_post_receive");
            }
        }
    });
}
```

---

## Call Flow

```
git push
  └─► gitstore-git-service: HookPipeline.run()
          │
          ├─► pre-receive phase (blocking)
          │       └─► schema_validation_handler.admit("pre-receive", updates)
          │               └─► gRPC callout to gitstore-api (GH#105)
          │                       └─► API reads blobs, parses frontmatter
          │                               Accept → push proceeds
          │                               Reject → push aborted with error message
          │
          └─► post-receive phase (fire-and-forget)
                  └─► tokio::spawn(admission_control_handler.admit("post-receive", updates))
                          └─► gRPC callout to gitstore-api (GH#106)
                                  └─► API: assign uid, write status, hydrate datastore
                                      (errors logged, push already accepted)
```

---

## What gitstore-api Implements (Go — see `contracts/go-types.md`)

**Schema validation handler** (called at `pre-receive`):
1. For each changed `.md` file in the incoming commit, reads the blob via `gitclient.ReadFile`.
2. Parses YAML frontmatter using `github.com/adrg/frontmatter`.
3. Validates against schema rules (required fields, forbidden fields, kind check, legacy format rejection).
4. Returns `Reject` with a descriptive message on first failure, or `Accept` if all pass.

**Admission control handler** (called at `post-receive`):
1. Reads the accepted commit's `.md` blobs.
2. Assigns system-managed metadata (`uid`, `resourceVersion`, `generation`, `creationTimestamp`, `revision`).
3. Writes initial `ProductStatus` with `AdmissionAccepted: True`.
4. Stores the fully hydrated record in the datastore.
