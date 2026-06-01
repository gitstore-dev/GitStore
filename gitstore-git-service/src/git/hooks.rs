// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

use std::path::Path;
use std::sync::Arc;
use std::time::{Duration, Instant};

use async_trait::async_trait;
use tracing::{error, info, warn};

use crate::config::GitReceivePackHooks;

// ---------------------------------------------------------------------------
// Shared data type
// ---------------------------------------------------------------------------

/// A single ref update passed to all hook phases.
#[derive(Clone, Debug)]
pub struct RefUpdate {
    pub ref_name: String,
    pub old_oid: String,
    pub new_oid: String,
}

// ---------------------------------------------------------------------------
// Decision types
// ---------------------------------------------------------------------------

/// Outcome of a hook phase execution.
#[derive(Debug)]
pub enum HookDecision {
    Accept,
    Reject(String),
}

/// Outcome returned by an `AdmissionHandler`.
#[derive(Debug)]
pub enum AdmissionDecision {
    Accept,
    Reject(String),
}

/// Carries the phase name and reason when a pipeline phase rejects a push.
#[derive(Debug)]
pub struct HookRejection {
    pub phase: String,
    pub reason: String,
}

impl std::fmt::Display for HookRejection {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        write!(f, "hook rejected by {}: {}", self.phase, self.reason)
    }
}

impl std::error::Error for HookRejection {}

// ---------------------------------------------------------------------------
// AdmissionHandler trait
// ---------------------------------------------------------------------------

/// Integration point for future admission (#105) and validation (#106) services.
/// Implemented by any type that participates in push admission decisions.
///
/// Called at the phase configured by
/// `GITSTORE_ADMISSION_CONTROL_VALIDATING_ADMISSION_POLICY_PHASE`.
#[async_trait]
pub trait AdmissionHandler: Send + Sync {
    /// Return `Accept` to allow the push/ref to proceed, or `Reject(reason)` to block it.
    /// Errors are treated as `Reject("admission handler error")`.
    async fn admit(&self, phase: &str, updates: &[RefUpdate]) -> anyhow::Result<AdmissionDecision>;
}

/// Default no-op implementation — always accepts.
pub struct NoopAdmissionHandler;

#[async_trait]
impl AdmissionHandler for NoopAdmissionHandler {
    async fn admit(
        &self,
        _phase: &str,
        _updates: &[RefUpdate],
    ) -> anyhow::Result<AdmissionDecision> {
        Ok(AdmissionDecision::Accept)
    }
}

// ---------------------------------------------------------------------------
// HookPipeline
// ---------------------------------------------------------------------------

const ADMISSION_TIMEOUT: Duration = Duration::from_secs(5);

/// Orchestrates the in-process hook execution pipeline for a single push event.
pub struct HookPipeline {
    pub config: GitReceivePackHooks,
    pub admission_phase: String,
    pub admission_handler: Arc<dyn AdmissionHandler + Send + Sync>,
}

impl HookPipeline {
    pub fn new(
        config: GitReceivePackHooks,
        admission_phase: String,
        handler: Arc<dyn AdmissionHandler + Send + Sync>,
    ) -> Self {
        Self {
            config,
            admission_phase,
            admission_handler: handler,
        }
    }

    /// Run the pre-receive → proc-receive → update phases.
    ///
    /// Returns `Ok(accepted_indices)` where each entry is an index into `updates`
    /// that was accepted by the update phase.
    /// Returns `Err(HookRejection)` if pre-receive or proc-receive rejects the push.
    pub async fn run(
        &self,
        git_dir: &Path,
        updates: &[RefUpdate],
    ) -> Result<Vec<usize>, HookRejection> {
        // --- pre-receive (once per push, all-or-nothing) ---
        if self.config.pre_receive.enabled {
            let decision = self
                .run_phase_with_admission("pre-receive", git_dir, updates, || {
                    run_pre_receive(git_dir, updates)
                })
                .await;
            if let HookDecision::Reject(reason) = decision {
                let reason = non_empty(reason, "rejected by pre-receive");
                warn!(
                    phase = "pre-receive",
                    outcome = "rejected",
                    reason = %reason,
                    "hook_phase_complete"
                );
                return Err(HookRejection {
                    phase: "pre-receive".to_string(),
                    reason,
                });
            }
            log_phase("pre-receive", None, "accepted", None);
        }

        // --- proc-receive (once per push, all-or-nothing) ---
        if self.config.proc_receive.enabled {
            let decision = self
                .run_phase_with_admission("proc-receive", git_dir, updates, || {
                    run_proc_receive(git_dir, updates)
                })
                .await;
            if let HookDecision::Reject(reason) = decision {
                let reason = non_empty(reason, "rejected by proc-receive");
                warn!(
                    phase = "proc-receive",
                    outcome = "rejected",
                    reason = %reason,
                    "hook_phase_complete"
                );
                return Err(HookRejection {
                    phase: "proc-receive".to_string(),
                    reason,
                });
            }
            log_phase("proc-receive", None, "accepted", None);
        }

        // --- update (once per ref, per-ref semantics) ---
        let mut accepted = Vec::new();
        for (i, update) in updates.iter().enumerate() {
            if !self.config.update.enabled {
                accepted.push(i);
                continue;
            }
            let single = std::slice::from_ref(update);
            let decision = self
                .run_phase_with_admission("update", git_dir, single, || run_update(git_dir, update))
                .await;
            match decision {
                HookDecision::Accept => {
                    log_phase("update", Some(&update.ref_name), "accepted", None);
                    accepted.push(i);
                }
                HookDecision::Reject(reason) => {
                    let reason = non_empty(reason, "rejected by update");
                    warn!(
                        phase = "update",
                        ref_name = %update.ref_name,
                        outcome = "rejected",
                        reason = %reason,
                        "hook_phase_complete"
                    );
                    // per-ref: continue processing remaining refs
                }
            }
        }

        Ok(accepted)
    }

    /// Called after ref lock files are acquired (gix two-phase transaction prepare step).
    /// Returns `Ok(())` to allow the commit, or `Err(HookRejection)` to trigger rollback.
    pub async fn run_reference_transaction_prepared(
        &self,
        git_dir: &Path,
        updates: &[RefUpdate],
    ) -> Result<(), HookRejection> {
        if !self.config.reference_transaction.enabled {
            return Ok(());
        }
        let start = Instant::now();
        let decision = self
            .run_phase_with_admission("reference-transaction/prepared", git_dir, updates, || {
                HookDecision::Accept
            })
            .await;
        let duration_ms = start.elapsed().as_millis() as u64;
        match decision {
            HookDecision::Accept => {
                info!(
                    phase = "reference-transaction/prepared",
                    duration_ms,
                    outcome = "accepted",
                    "hook_phase_complete"
                );
                Ok(())
            }
            HookDecision::Reject(reason) => {
                let reason = non_empty(reason, "rejected by reference-transaction");
                warn!(
                    phase = "reference-transaction/prepared",
                    duration_ms,
                    outcome = "rejected",
                    reason = %reason,
                    "hook_phase_complete"
                );
                Err(HookRejection {
                    phase: "reference-transaction/prepared".to_string(),
                    reason,
                })
            }
        }
    }

    /// Called after refs are committed. Observation only — cannot fail.
    pub fn run_reference_transaction_committed(&self, _git_dir: &Path, _updates: &[RefUpdate]) {
        if self.config.reference_transaction.enabled {
            info!(
                phase = "reference-transaction/committed",
                outcome = "accepted",
                "hook_phase_complete"
            );
        }
    }

    /// Called on rollback. Observation only — cannot fail.
    pub fn run_reference_transaction_aborted(&self, _git_dir: &Path, _updates: &[RefUpdate]) {
        if self.config.reference_transaction.enabled {
            info!(
                phase = "reference-transaction/aborted",
                outcome = "aborted",
                "hook_phase_complete"
            );
        }
    }

    /// Called after refs are committed. Errors are logged at ERROR level and never propagated.
    pub fn run_post_receive(&self, git_dir: &Path, updates: &[RefUpdate]) {
        if !self.config.post_receive.enabled {
            return;
        }
        let start = Instant::now();
        run_post_receive(git_dir, updates);
        let duration_ms = start.elapsed().as_millis() as u64;
        info!(
            phase = "post-receive",
            duration_ms,
            outcome = "accepted",
            "hook_phase_complete"
        );
    }

    // -- internal helpers --

    /// Run `phase_fn` then, if this is the configured admission phase, call the handler
    /// with a 5-second timeout (fail-closed). Returns the final `HookDecision`.
    async fn run_phase_with_admission<F>(
        &self,
        phase: &str,
        _git_dir: &Path,
        updates: &[RefUpdate],
        phase_fn: F,
    ) -> HookDecision
    where
        F: FnOnce() -> HookDecision,
    {
        let start = Instant::now();
        let decision = phase_fn();
        if let HookDecision::Reject(_) = &decision {
            return decision;
        }
        // Only invoke the admission handler at the configured phase.
        if phase == self.admission_phase {
            let result = tokio::time::timeout(
                ADMISSION_TIMEOUT,
                self.admission_handler.admit(phase, updates),
            )
            .await;
            let duration_ms = start.elapsed().as_millis() as u64;
            match result {
                Ok(Ok(AdmissionDecision::Accept)) => {}
                Ok(Ok(AdmissionDecision::Reject(reason))) => {
                    return HookDecision::Reject(reason);
                }
                Ok(Err(e)) => {
                    error!(
                        phase,
                        duration_ms,
                        reason = %e,
                        "hook_phase_error"
                    );
                    return HookDecision::Reject("admission handler error".to_string());
                }
                Err(_elapsed) => {
                    error!(
                        phase,
                        duration_ms,
                        reason = "admission service timeout",
                        "hook_phase_error"
                    );
                    return HookDecision::Reject("admission service timeout".to_string());
                }
            }
        }
        decision
    }
}

// ---------------------------------------------------------------------------
// Phase functions (stubs — replaced by real logic in future features)
// ---------------------------------------------------------------------------

fn run_pre_receive(_git_dir: &Path, _updates: &[RefUpdate]) -> HookDecision {
    HookDecision::Accept
}

fn run_proc_receive(_git_dir: &Path, _updates: &[RefUpdate]) -> HookDecision {
    HookDecision::Accept
}

fn run_update(_git_dir: &Path, _update: &RefUpdate) -> HookDecision {
    HookDecision::Accept
}

fn run_post_receive(_git_dir: &Path, _updates: &[RefUpdate]) {
    // fire-and-forget: future features will fan out events here
}

// ---------------------------------------------------------------------------
// Legacy helpers (kept for tag utilities used elsewhere)
// ---------------------------------------------------------------------------

/// Parse pre-receive hook input (from stdin) — kept for compatibility.
/// Format: <old-oid> <new-oid> <ref-name>
pub fn parse_pre_receive_input(input: &str) -> anyhow::Result<Vec<RefUpdate>> {
    let mut updates = Vec::new();
    for line in input.lines() {
        let parts: Vec<&str> = line.split_whitespace().collect();
        if parts.len() != 3 {
            tracing::warn!(line = line, "Invalid pre-receive input line");
            continue;
        }
        updates.push(RefUpdate {
            old_oid: parts[0].to_string(),
            new_oid: parts[1].to_string(),
            ref_name: parts[2].to_string(),
        });
    }
    Ok(updates)
}

pub fn is_tag_update(ref_name: &str) -> bool {
    ref_name.starts_with("refs/tags/")
}

pub fn get_tag_name(ref_name: &str) -> Option<String> {
    ref_name.strip_prefix("refs/tags/").map(|s| s.to_string())
}

// ---------------------------------------------------------------------------
// Private helpers
// ---------------------------------------------------------------------------

fn non_empty(s: String, fallback: &str) -> String {
    if s.is_empty() {
        fallback.to_string()
    } else {
        s
    }
}

fn log_phase(phase: &str, ref_name: Option<&str>, outcome: &str, reason: Option<&str>) {
    match (ref_name, reason) {
        (Some(r), Some(reason)) => {
            info!(phase, ref_name = r, outcome, reason, "hook_phase_complete")
        }
        (Some(r), None) => info!(phase, ref_name = r, outcome, "hook_phase_complete"),
        (None, Some(reason)) => info!(phase, outcome, reason, "hook_phase_complete"),
        (None, None) => info!(phase, outcome, "hook_phase_complete"),
    }
}

// ---------------------------------------------------------------------------
// Unit tests
// ---------------------------------------------------------------------------

#[cfg(test)]
mod tests {
    use super::*;
    use std::sync::Arc;

    // T004: HookDecision, AdmissionDecision, NoopAdmissionHandler, HookRejection
    #[test]
    fn test_hook_decision_reject_carries_reason() {
        let d = HookDecision::Reject("test reason".to_string());
        if let HookDecision::Reject(r) = d {
            assert_eq!(r, "test reason");
        } else {
            panic!("expected Reject");
        }
    }

    #[test]
    fn test_admission_decision_reject_carries_reason() {
        let d = AdmissionDecision::Reject("admission reason".to_string());
        if let AdmissionDecision::Reject(r) = d {
            assert_eq!(r, "admission reason");
        } else {
            panic!("expected Reject");
        }
    }

    #[test]
    fn test_hook_rejection_fields() {
        let r = HookRejection {
            phase: "pre-receive".to_string(),
            reason: "blocked".to_string(),
        };
        assert_eq!(r.phase, "pre-receive");
        assert_eq!(r.reason, "blocked");
    }

    #[tokio::test]
    async fn test_noop_admission_handler_always_accepts() {
        let h = NoopAdmissionHandler;
        let updates = vec![RefUpdate {
            ref_name: "refs/heads/main".to_string(),
            old_oid: "0".repeat(40),
            new_oid: "a".repeat(40),
        }];
        let result = h.admit("pre-receive", &updates).await.unwrap();
        assert!(matches!(result, AdmissionDecision::Accept));
    }

    // T005: HookPipeline toggle enforcement
    fn make_disabled_config() -> GitReceivePackHooks {
        use crate::config::HookToggle;
        GitReceivePackHooks {
            pre_receive: HookToggle { enabled: false },
            update: HookToggle { enabled: false },
            post_receive: HookToggle { enabled: false },
            proc_receive: HookToggle { enabled: false },
            post_update: HookToggle { enabled: false },
            reference_transaction: HookToggle { enabled: false },
        }
    }

    fn make_update(ref_name: &str) -> RefUpdate {
        RefUpdate {
            ref_name: ref_name.to_string(),
            old_oid: "0".repeat(40),
            new_oid: "a".repeat(40),
        }
    }

    #[tokio::test]
    async fn test_all_disabled_accepts_all_indices() {
        let pipeline = HookPipeline::new(
            make_disabled_config(),
            "pre-receive".to_string(),
            Arc::new(NoopAdmissionHandler),
        );
        let updates = vec![
            make_update("refs/heads/main"),
            make_update("refs/heads/dev"),
        ];
        let result = pipeline
            .run(std::path::Path::new("/tmp"), &updates)
            .await
            .unwrap();
        assert_eq!(result, vec![0, 1]);
    }

    #[tokio::test]
    async fn test_pre_receive_only_enabled_accepts() {
        use crate::config::HookToggle;
        let mut cfg = make_disabled_config();
        cfg.pre_receive = HookToggle { enabled: true };
        let pipeline = HookPipeline::new(
            cfg,
            "pre-receive".to_string(),
            Arc::new(NoopAdmissionHandler),
        );
        let updates = vec![make_update("refs/heads/main")];
        let result = pipeline
            .run(std::path::Path::new("/tmp"), &updates)
            .await
            .unwrap();
        assert_eq!(result, vec![0]);
    }

    #[test]
    fn test_parse_pre_receive_input() {
        let input = "abc123 def456 refs/heads/main\n000000 fed789 refs/tags/v1.0.0";
        let updates = parse_pre_receive_input(input).unwrap();
        assert_eq!(updates.len(), 2);
        assert_eq!(updates[0].old_oid, "abc123");
        assert_eq!(updates[0].new_oid, "def456");
        assert_eq!(updates[0].ref_name, "refs/heads/main");
        assert_eq!(updates[1].ref_name, "refs/tags/v1.0.0");
    }

    #[test]
    fn test_parse_invalid_input() {
        let input = "invalid line\nabc def\n";
        let updates = parse_pre_receive_input(input).unwrap();
        assert_eq!(updates.len(), 0);
    }

    #[test]
    fn test_is_tag_update() {
        assert!(is_tag_update("refs/tags/v1.0.0"));
        assert!(!is_tag_update("refs/heads/main"));
    }

    #[test]
    fn test_get_tag_name() {
        assert_eq!(get_tag_name("refs/tags/v1.0.0"), Some("v1.0.0".to_string()));
        assert_eq!(get_tag_name("refs/heads/main"), None);
    }
}
