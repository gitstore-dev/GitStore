# Quickstart: Testing the Hook Pipeline

**Branch**: `013-receive-pack-hooks` | **Date**: 2026-06-01

This guide shows how to exercise the hook pipeline locally once the feature is implemented.

## Prerequisites

- gitstore-git-service built and running (`make git` or `make dev`)
- A repository exists (see `make bootstrap`)
- A Git client with the smart HTTP URL for the repository

## Enable a Hook Phase

By default, all hook phases are disabled. To enable, set via `gitstore.toml` or environment variables.

**Enable pre-receive and post-receive via env (double-underscore separator):**

```bash
GITSTORE_HOOKS__GIT_RECEIVE_PACK__PRE_RECEIVE__ENABLED=true \
GITSTORE_HOOKS__GIT_RECEIVE_PACK__POST_RECEIVE__ENABLED=true \
make git
```

Or in `gitstore.toml`:

```toml
[hooks.git_receive_pack]
pre_receive  = { enabled = true }
update       = { enabled = true }
post_receive = { enabled = true }
proc_receive = { enabled = false }
post_update  = { enabled = false }
```

## Test a Push

```bash
# Clone the repository
git clone http://localhost:5000/gitstore/catalog.git

# Make a commit
cd catalog
echo "test" >> README.md
git add README.md
git commit -m "test: hook pipeline"

# Push (should succeed with all phases accepting)
git push origin main
```

Expected output when all phases accept:
```
Enumerating objects: 5, done.
Counting objects: 100% (5/5), done.
Writing objects: 100% (3/3), 300 bytes | 300.00 KiB/s, done.
To http://localhost:5000/gitstore/catalog.git
   abc1234..def5678  main -> main
```

## Observe Hook Phase Logs

With `log.format = "json"` (default), each phase produces a structured log line:

```json lines
{"level":"INFO","target":"gitstore_git_service","hook_phase_complete":{"phase":"pre-receive","duration_ms":1,"outcome":"accepted"}}
{"level":"INFO","target":"gitstore_git_service","hook_phase_complete":{"phase":"update","ref_name":"refs/heads/main","duration_ms":0,"outcome":"accepted"}}
{"level":"INFO","target":"gitstore_git_service","hook_phase_complete":{"phase":"post-receive","duration_ms":0,"outcome":"accepted"}}
```

## Test a Rejection

The default `NoopAdmissionHandler` always accepts. To exercise rejection, wire a test handler in integration tests (see `tests/integration/` after implementation). The integration test suite covers:

- `test_push_accepted_all_phases_enabled` — happy path, phase order verified
- `test_push_rejected_pre_receive` — pre-receive rejects, zero refs updated
- `test_push_rejected_update_one_ref` — update rejects one ref, others proceed
- `test_push_all_phases_disabled` — no hook overhead
- `test_admission_routing_phase` — admission handler wired to configured phase

## Wiring a Custom Admission Handler

For future use by #105 and #106, implement the `AdmissionHandler` trait:

```rust
use gitstore_git_service::git::hooks::{AdmissionDecision, AdmissionHandler, RefUpdate};
use async_trait::async_trait;

pub struct MyAdmissionService { /* gRPC channel etc. */ }

#[async_trait]
impl AdmissionHandler for MyAdmissionService {
    async fn admit(&self, phase: &str, updates: &[RefUpdate]) -> anyhow::Result<AdmissionDecision> {
        // call your service here; 5-second timeout enforced by the pipeline
        Ok(AdmissionDecision::Accept)
    }
}
```

Pass it when constructing `HookPipeline`:

```rust
let pipeline = HookPipeline::new(
    config.hooks.git_receive_pack.clone(),
    config.admission_control.validating_admission_policy.phase.clone(),
    Arc::new(MyAdmissionService::new(channel)),
);
```
