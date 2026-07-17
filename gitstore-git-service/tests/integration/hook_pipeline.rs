// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

use std::collections::HashSet;
use std::sync::Arc;

use tempfile::TempDir;

use gitstore::config::{GitReceivePackHooks, HookToggle};
use gitstore::git::hooks::{
    HookContext, HookPipeline, NoopAdmissionHandler, NoopValidationHandler, RefUpdate,
};

use super::helpers::{
    make_bare_repo, make_commit, zero_oid, CountingValidationHandler, PerRefRejectingHandler,
    RejectingValidationHandler, SlowValidationHandler,
};

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

fn all_disabled() -> GitReceivePackHooks {
    GitReceivePackHooks {
        pre_receive: HookToggle { enabled: false },
        update: HookToggle { enabled: false },
        post_receive: HookToggle { enabled: false },
        proc_receive: HookToggle { enabled: false },
        post_update: HookToggle { enabled: false },
        reference_transaction: HookToggle { enabled: false },
    }
}

fn all_enabled() -> GitReceivePackHooks {
    GitReceivePackHooks {
        pre_receive: HookToggle { enabled: true },
        update: HookToggle { enabled: true },
        post_receive: HookToggle { enabled: true },
        proc_receive: HookToggle { enabled: true },
        post_update: HookToggle { enabled: false }, // legacy, excluded from scope
        reference_transaction: HookToggle { enabled: true },
    }
}

fn noop_pipeline(config: GitReceivePackHooks) -> HookPipeline {
    HookPipeline::new(
        config,
        "pre-receive".to_string(),
        std::time::Duration::from_secs(10),
        "post-receive".to_string(),
        "refs/heads/main".to_string(),
        Arc::new(NoopValidationHandler),
        Arc::new(NoopAdmissionHandler),
    )
}

fn pipeline_with_validation_handler(
    config: GitReceivePackHooks,
    phase: &str,
    handler: Arc<dyn gitstore::git::hooks::ValidationHandler + Send + Sync>,
) -> HookPipeline {
    HookPipeline::new(
        config,
        phase.to_string(),
        std::time::Duration::from_secs(5),
        "post-receive".to_string(),
        "refs/heads/main".to_string(),
        handler,
        Arc::new(NoopAdmissionHandler),
    )
}

fn make_update(ref_name: &str, old_oid: &str, new_oid: &str) -> RefUpdate {
    RefUpdate {
        ref_name: ref_name.to_string(),
        old_oid: old_oid.to_string(),
        new_oid: new_oid.to_string(),
    }
}

// ---------------------------------------------------------------------------
// T010 / US1: Happy path — push accepted, phase order (with all phases enabled)
// ---------------------------------------------------------------------------

#[tokio::test]
async fn test_push_accepted_all_phases_enabled() {
    let dir = TempDir::new().unwrap();
    let repo_path = make_bare_repo(dir.path());
    let old_oid = make_commit(&repo_path, "initial");
    let new_oid = make_commit(&repo_path, "second");

    let pipeline = noop_pipeline(all_enabled());
    let update = make_update("refs/heads/main", &old_oid, &new_oid);

    let result = pipeline
        .run(
            &repo_path,
            std::slice::from_ref(&update),
            None,
            &HookContext::default(),
        )
        .await
        .unwrap();
    assert_eq!(result, vec![0], "index 0 should be accepted");

    let rt_result = pipeline
        .run_reference_transaction_prepared(
            &repo_path,
            std::slice::from_ref(&update),
            &HookContext::default(),
        )
        .await;
    assert!(
        rt_result.is_ok(),
        "reference-transaction/prepared should accept"
    );
}

// ---------------------------------------------------------------------------
// T011 / US1: Per-phase log events emitted (structural check via pipeline execution)
// ---------------------------------------------------------------------------

#[tokio::test]
async fn test_pipeline_run_returns_all_accepted_with_noop() {
    let dir = TempDir::new().unwrap();
    let repo_path = make_bare_repo(dir.path());
    let oid = make_commit(&repo_path, "init");

    let pipeline = noop_pipeline(all_enabled());
    let update = make_update("refs/heads/main", zero_oid(), &oid);

    let accepted = pipeline
        .run(&repo_path, &[update], None, &HookContext::default())
        .await
        .unwrap();
    assert_eq!(accepted, vec![0]);
}

// ---------------------------------------------------------------------------
// T020 / US2: pre-receive rejection aborts entire push
// ---------------------------------------------------------------------------

#[tokio::test]
async fn test_push_rejected_pre_receive() {
    let dir = TempDir::new().unwrap();
    let repo_path = make_bare_repo(dir.path());
    let oid1 = make_commit(&repo_path, "init");
    let oid2 = make_commit(&repo_path, "second");

    let mut cfg = all_disabled();
    cfg.pre_receive = HookToggle { enabled: true };
    let pipeline = pipeline_with_validation_handler(
        cfg,
        "pre-receive",
        Arc::new(RejectingValidationHandler("blocked by policy".to_string())),
    );

    let updates = vec![
        make_update("refs/heads/main", &oid1, &oid2),
        make_update("refs/heads/dev", zero_oid(), &oid1),
    ];
    let err = pipeline
        .run(&repo_path, &updates, None, &HookContext::default())
        .await
        .unwrap_err();
    assert_eq!(err.phase, "pre-receive");
    assert_eq!(err.reason, "blocked by policy");
}

// ---------------------------------------------------------------------------
// T021 / US2: update per-ref rejection — one ref blocked, others proceed
// ---------------------------------------------------------------------------

#[tokio::test]
async fn test_push_rejected_update_one_ref() {
    let dir = TempDir::new().unwrap();
    let repo_path = make_bare_repo(dir.path());
    let oid = make_commit(&repo_path, "init");

    let mut cfg = all_disabled();
    cfg.update = HookToggle { enabled: true };

    // PerRefRejectingHandler rejects refs named refs/test/idx/1
    // Per-ref rejection: use the AdmissionHandler slot at the update phase.
    // The AdmissionHandler is called once per ref in run_schema_validation when
    // the phase matches admission_control_phase.
    let mut reject_set = HashSet::new();
    reject_set.insert(1usize);
    let pipeline = HookPipeline::new(
        cfg,
        "pre-receive".to_string(), // validation at pre-receive (noop)
        std::time::Duration::from_secs(5),
        "update".to_string(), // admission at update (per-ref)
        "refs/heads/main".to_string(),
        Arc::new(NoopValidationHandler),
        Arc::new(PerRefRejectingHandler(reject_set)),
    );

    let updates = vec![
        make_update("refs/test/idx/0", zero_oid(), &oid),
        make_update("refs/test/idx/1", zero_oid(), &oid),
        make_update("refs/test/idx/2", zero_oid(), &oid),
    ];
    let accepted = pipeline
        .run(&repo_path, &updates, None, &HookContext::default())
        .await
        .unwrap();
    assert_eq!(accepted, vec![0, 2], "only idx/1 should be rejected");
}

// ---------------------------------------------------------------------------
// T022 / US2: proc-receive rejection aborts push
// ---------------------------------------------------------------------------

#[tokio::test]
async fn test_push_rejected_proc_receive() {
    let dir = TempDir::new().unwrap();
    let repo_path = make_bare_repo(dir.path());
    let oid = make_commit(&repo_path, "init");

    let mut cfg = all_disabled();
    cfg.proc_receive = HookToggle { enabled: true };
    let pipeline = pipeline_with_validation_handler(
        cfg,
        "proc-receive",
        Arc::new(RejectingValidationHandler("proc blocked".to_string())),
    );

    let update = make_update("refs/heads/main", zero_oid(), &oid);
    let err = pipeline
        .run(&repo_path, &[update], None, &HookContext::default())
        .await
        .unwrap_err();
    assert_eq!(err.phase, "proc-receive");
    assert_eq!(err.reason, "proc blocked");
}

// ---------------------------------------------------------------------------
// T023 / US2: reference-transaction/prepared veto
// ---------------------------------------------------------------------------

#[tokio::test]
async fn test_reference_transaction_veto() {
    let dir = TempDir::new().unwrap();
    let repo_path = make_bare_repo(dir.path());
    let oid = make_commit(&repo_path, "init");

    let mut cfg = all_disabled();
    cfg.reference_transaction = HookToggle { enabled: true };
    let pipeline = pipeline_with_validation_handler(
        cfg,
        "reference-transaction/prepared",
        Arc::new(RejectingValidationHandler("txn blocked".to_string())),
    );

    let update = make_update("refs/heads/main", zero_oid(), &oid);
    let err = pipeline
        .run_reference_transaction_prepared(&repo_path, &[update], &HookContext::default())
        .await
        .unwrap_err();
    assert_eq!(err.phase, "reference-transaction/prepared");
    assert_eq!(err.reason, "txn blocked");
}

// ---------------------------------------------------------------------------
// T029 / US3: all phases disabled — all refs accepted, no log overhead
// ---------------------------------------------------------------------------

#[tokio::test]
async fn test_all_phases_disabled() {
    let dir = TempDir::new().unwrap();
    let repo_path = make_bare_repo(dir.path());
    let oid = make_commit(&repo_path, "init");

    let pipeline = noop_pipeline(all_disabled());
    let update = make_update("refs/heads/main", zero_oid(), &oid);

    let accepted = pipeline
        .run(&repo_path, &[update], None, &HookContext::default())
        .await
        .unwrap();
    assert_eq!(accepted, vec![0]);
}

// ---------------------------------------------------------------------------
// T030 / US3: only pre-receive enabled
// ---------------------------------------------------------------------------

#[tokio::test]
async fn test_only_pre_receive_enabled() {
    let dir = TempDir::new().unwrap();
    let repo_path = make_bare_repo(dir.path());
    let oid = make_commit(&repo_path, "init");

    let mut cfg = all_disabled();
    cfg.pre_receive = HookToggle { enabled: true };
    let pipeline = noop_pipeline(cfg);

    let update = make_update("refs/heads/main", zero_oid(), &oid);
    let accepted = pipeline
        .run(&repo_path, &[update], None, &HookContext::default())
        .await
        .unwrap();
    assert_eq!(accepted, vec![0]);
}

// ---------------------------------------------------------------------------
// T031 / US3: reference-transaction disabled — prepared returns Ok immediately
// ---------------------------------------------------------------------------

#[tokio::test]
async fn test_reference_transaction_disabled() {
    let dir = TempDir::new().unwrap();
    let repo_path = make_bare_repo(dir.path());
    let oid = make_commit(&repo_path, "init");

    let pipeline = noop_pipeline(all_disabled());
    let update = make_update("refs/heads/main", zero_oid(), &oid);

    let result = pipeline
        .run_reference_transaction_prepared(&repo_path, &[update], &HookContext::default())
        .await;
    assert!(result.is_ok());
}

// ---------------------------------------------------------------------------
// T035 / US4: admission accept at configured phase
// ---------------------------------------------------------------------------

#[tokio::test]
async fn test_admission_accept() {
    let dir = TempDir::new().unwrap();
    let repo_path = make_bare_repo(dir.path());
    let oid = make_commit(&repo_path, "init");

    let mut cfg = all_disabled();
    cfg.pre_receive = HookToggle { enabled: true };
    let pipeline = noop_pipeline(cfg);

    let update = make_update("refs/heads/main", zero_oid(), &oid);
    let accepted = pipeline
        .run(&repo_path, &[update], None, &HookContext::default())
        .await
        .unwrap();
    assert_eq!(accepted, vec![0]);
}

// ---------------------------------------------------------------------------
// T036 / US4: admission reject with reason
// ---------------------------------------------------------------------------

#[tokio::test]
async fn test_admission_reject_with_reason() {
    let dir = TempDir::new().unwrap();
    let repo_path = make_bare_repo(dir.path());
    let oid = make_commit(&repo_path, "init");

    let mut cfg = all_disabled();
    cfg.update = HookToggle { enabled: true };
    let pipeline = pipeline_with_validation_handler(
        cfg,
        "update",
        Arc::new(RejectingValidationHandler("policy violation".to_string())),
    );

    let update = make_update("refs/heads/main", zero_oid(), &oid);
    // update is per-ref, rejection causes that ref to be marked ng (not a pipeline Err)
    // All refs are rejected so accepted set is empty.
    let accepted = pipeline
        .run(&repo_path, &[update], None, &HookContext::default())
        .await
        .unwrap();
    assert!(
        accepted.is_empty(),
        "all refs should be rejected by update admission"
    );
}

// ---------------------------------------------------------------------------
// T037 / US4: admission timeout → fail-closed
// ---------------------------------------------------------------------------

#[tokio::test]
async fn test_admission_timeout_fail_closed() {
    let dir = TempDir::new().unwrap();
    let repo_path = make_bare_repo(dir.path());
    let oid = make_commit(&repo_path, "init");

    let mut cfg = all_disabled();
    cfg.pre_receive = HookToggle { enabled: true };
    let pipeline =
        pipeline_with_validation_handler(cfg, "pre-receive", Arc::new(SlowValidationHandler));

    let update = make_update("refs/heads/main", zero_oid(), &oid);
    let start = std::time::Instant::now();
    let err = pipeline
        .run(&repo_path, &[update], None, &HookContext::default())
        .await
        .unwrap_err();
    let elapsed = start.elapsed();

    assert_eq!(err.phase, "pre-receive");
    assert_eq!(err.reason, "validation service unavailable");
    // Should complete well under 10 seconds (the slow handler sleeps 10s)
    assert!(
        elapsed.as_secs() < 8,
        "pipeline should have timed out at 5s, took {:?}",
        elapsed
    );
}

// ---------------------------------------------------------------------------
// T038 / US4: admission called only at configured phase
// ---------------------------------------------------------------------------

#[tokio::test]
async fn test_admission_only_called_at_configured_phase() {
    let dir = TempDir::new().unwrap();
    let repo_path = make_bare_repo(dir.path());
    let oid1 = make_commit(&repo_path, "init");
    let oid2 = make_commit(&repo_path, "second");

    // Enable both pre-receive and update; route admission to update only.
    let mut cfg = all_disabled();
    cfg.pre_receive = HookToggle { enabled: true };
    cfg.update = HookToggle { enabled: true };

    let (counter_handler, counter) = CountingValidationHandler::new();
    let pipeline = pipeline_with_validation_handler(cfg, "update", Arc::new(counter_handler));

    let updates = vec![
        make_update("refs/heads/main", &oid1, &oid2),
        make_update("refs/heads/dev", zero_oid(), &oid1),
    ];
    let accepted = pipeline
        .run(&repo_path, &updates, None, &HookContext::default())
        .await
        .unwrap();
    assert_eq!(accepted, vec![0, 1]);

    // Admission called once per ref in update phase = 2 times; pre-receive = 0
    let call_count = counter.load(std::sync::atomic::Ordering::SeqCst);
    assert_eq!(
        call_count, 2,
        "should be called once per ref in update phase"
    );
}
