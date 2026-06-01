# Contract: Hook Pipeline Public Interface

**Service**: `gitstore-git-service` (Rust)  
**Feature**: `013-receive-pack-hooks`  
**Date**: 2026-06-01

## AdmissionHandler Trait Contract

This trait is the stable integration point for future admission (#105) and validation (#106) services. Any type that implements this trait can be injected into the hook pipeline.

```rust
/// Implemented by any service that participates in push admission.
/// Called at the phase assigned by GITSTORE_ADMISSION_CONTROL_VALIDATING_ADMISSION_POLICY_PHASE.
#[async_trait]
pub trait AdmissionHandler: Send + Sync {
    /// Called once per push for per-push phases (pre-receive, proc-receive, post-receive).
    /// Called once per ref for per-ref phases (update).
    ///
    /// Returns:
    ///   Ok(Accept)          — push/ref proceeds
    ///   Ok(Reject(reason))  — push/ref rejected; reason shown to Git client verbatim
    ///   Err(_)              — treated as Reject("admission handler error")
    async fn admit(
        &self,
        phase: &str,           // canonical phase name (see Phase Names below)
        updates: &[RefUpdate], // all updates for per-push phases; single update for per-ref phases
    ) -> anyhow::Result<AdmissionDecision>;
}
```

### Phase Names (canonical strings)

| Phase                              | Cardinality | Called when…                           |
|------------------------------------|-------------|----------------------------------------|
| `"pre-receive"`                    | once/push   | Before any ref is updated              |
| `"proc-receive"`                   | once/push   | After pre-receive, before update       |
| `"update"`                         | once/ref    | For each ref in the push independently |
| `"reference-transaction/prepared"` | once/push   | Ref locks acquired, not yet committed  |
| `"post-receive"`                   | once/push   | After refs committed (fire-and-forget) |

### AdmissionDecision

```rust
pub enum AdmissionDecision {
    Accept,
    Reject(String), // non-empty reason shown verbatim to the Git client
}
```

### Timeout Contract

The pipeline enforces a **5-second hard timeout** on every `admit()` call. An elapsed timeout is treated as `Reject("admission service timeout")`. Implementations MUST NOT assume they will receive a cancellation signal — the future is simply dropped.

### NoopAdmissionHandler (default)

```rust
pub struct NoopAdmissionHandler;

#[async_trait]
impl AdmissionHandler for NoopAdmissionHandler {
    async fn admit(&self, _phase: &str, _updates: &[RefUpdate]) -> anyhow::Result<AdmissionDecision> {
        Ok(AdmissionDecision::Accept)
    }
}
```

This is the default when no admission service is wired up.

---

## HookPipeline Public API

```rust
pub struct HookPipeline {
    pub config: HooksConfig,
    pub admission_phase: String,
    pub admission_handler: Arc<dyn AdmissionHandler + Send + Sync>,
}

impl HookPipeline {
    pub fn new(config: HooksConfig, admission_phase: String, handler: Arc<dyn AdmissionHandler + Send + Sync>) -> Self;

    /// Run the full pipeline for a push event.
    /// Returns Ok(Vec<usize>) — indices of accepted ref updates.
    /// Returns Err(HookRejection) — push aborted; rejection reason for client.
    pub async fn run(
        &self,
        git_dir: &Path,
        updates: &[RefUpdate],
        // reference-transaction is driven separately via the prepare/commit/rollback callbacks
    ) -> Result<Vec<usize>, HookRejection>;

    /// Called after pack is promoted and ref edits are prepared (locks held).
    /// Returns Ok(()) to allow commit, Err(HookRejection) to rollback.
    pub async fn run_reference_transaction_prepared(
        &self,
        git_dir: &Path,
        updates: &[RefUpdate],
    ) -> Result<(), HookRejection>;

    /// Called after commit completes (observation only, cannot fail).
    pub fn run_reference_transaction_committed(&self, git_dir: &Path, updates: &[RefUpdate]);

    /// Called on rollback (observation only, cannot fail).
    pub fn run_reference_transaction_aborted(&self, git_dir: &Path, updates: &[RefUpdate]);

    /// Called after refs committed. Errors logged at ERROR, never propagated.
    pub fn run_post_receive(&self, git_dir: &Path, updates: &[RefUpdate]);
}

pub struct HookRejection {
    pub phase: String,
    pub reason: String,
}
```

---

## Structured Log Contract

Every phase execution MUST produce a `tracing` event with these fields:

```
INFO  hook_phase_complete  phase="pre-receive"  duration_ms=2  outcome="accepted"
WARN  hook_phase_complete  phase="update"  ref_name="refs/heads/main"  duration_ms=1  outcome="rejected"  reason="non-fast-forward"
ERROR hook_phase_error     phase="post-receive"  duration_ms=5001  reason="admission service timeout"
```

Log level rules:
- `INFO` — phase executed and accepted
- `WARN` — phase executed and rejected (pre-receive, update, proc-receive, reference-transaction/prepared)
- `ERROR` — post-receive / post-update failure (fire-and-forget error)

---

## Backward Compatibility

All hook phases default to `enabled = false` in `gitstore.toml`. No existing push behaviour changes unless an operator explicitly enables a phase. This contract makes no stability promises (alpha software).
