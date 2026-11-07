// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

use std::sync::Arc;

use tempfile::TempDir;

use gitstore::git::hooks::{
    HookContext, HookPipeline, NoopAdmissionHandler, NoopValidationHandler, RefUpdate,
};

use super::helpers::{
    make_bare_repo, make_commit, zero_oid, CountingAdmissionHandler, RejectingValidationHandler,
    SlowValidationHandler,
};

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

fn noop_pipeline() -> HookPipeline {
    HookPipeline::new(
        std::time::Duration::from_secs(10),
        Arc::new(NoopValidationHandler),
        Arc::new(NoopAdmissionHandler),
    )
}

fn pipeline_with_validation_handler(
    timeout: std::time::Duration,
    handler: Arc<dyn gitstore::git::hooks::ValidationHandler + Send + Sync>,
) -> HookPipeline {
    HookPipeline::new(timeout, handler, Arc::new(NoopAdmissionHandler))
}

fn make_update(ref_name: &str, old_oid: &str, new_oid: &str) -> RefUpdate {
    RefUpdate {
        ref_name: ref_name.to_string(),
        old_oid: old_oid.to_string(),
        new_oid: new_oid.to_string(),
    }
}

// ---------------------------------------------------------------------------
// Pre-receive schema validation — always on, blocking, fail-closed.
// ---------------------------------------------------------------------------

#[tokio::test]
async fn test_push_accepted_returns_all_indices() {
    let dir = TempDir::new().unwrap();
    let repo_path = make_bare_repo(dir.path());
    let old_oid = make_commit(&repo_path, "initial");
    let new_oid = make_commit(&repo_path, "second");

    let pipeline = noop_pipeline();
    let updates = vec![
        make_update("refs/heads/main", &old_oid, &new_oid),
        make_update("refs/heads/dev", zero_oid(), &old_oid),
    ];

    let result = pipeline
        .run(&repo_path, &updates, None, &HookContext::default())
        .await
        .unwrap();
    assert_eq!(
        result,
        vec![0, 1],
        "all refs are accepted when validation passes"
    );
}

#[tokio::test]
async fn test_push_rejected_by_pre_receive_validation() {
    let dir = TempDir::new().unwrap();
    let repo_path = make_bare_repo(dir.path());
    let oid1 = make_commit(&repo_path, "init");
    let oid2 = make_commit(&repo_path, "second");

    let pipeline = pipeline_with_validation_handler(
        std::time::Duration::from_secs(5),
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

#[tokio::test]
async fn test_pre_receive_timeout_fails_closed() {
    let dir = TempDir::new().unwrap();
    let repo_path = make_bare_repo(dir.path());
    let oid = make_commit(&repo_path, "init");

    let pipeline = pipeline_with_validation_handler(
        std::time::Duration::from_secs(5),
        Arc::new(SlowValidationHandler),
    );

    let update = make_update("refs/heads/main", zero_oid(), &oid);
    let start = std::time::Instant::now();
    let err = pipeline
        .run(&repo_path, &[update], None, &HookContext::default())
        .await
        .unwrap_err();
    let elapsed = start.elapsed();

    assert_eq!(err.phase, "pre-receive");
    assert_eq!(err.reason, "validation service unavailable");
    // Should complete well under 10 seconds (the slow handler sleeps 10s).
    assert!(
        elapsed.as_secs() < 8,
        "pipeline should have timed out at 5s, took {:?}",
        elapsed
    );
}

// ---------------------------------------------------------------------------
// Post-receive admission control — always on, fire-and-forget.
// ---------------------------------------------------------------------------

#[tokio::test]
async fn test_post_receive_always_invokes_admission_handler() {
    let dir = TempDir::new().unwrap();
    let repo_path = make_bare_repo(dir.path());
    let oid = make_commit(&repo_path, "init");

    let (handler, counter) = CountingAdmissionHandler::new();
    let pipeline = HookPipeline::new(
        std::time::Duration::from_secs(10),
        Arc::new(NoopValidationHandler),
        Arc::new(handler),
    );

    let update = make_update("refs/heads/main", zero_oid(), &oid);
    pipeline.run_post_receive(&repo_path, &[update], "repo-1", &HookContext::default());

    // Fire-and-forget: poll briefly for the spawned task to run.
    for _ in 0..50 {
        if counter.load(std::sync::atomic::Ordering::SeqCst) == 1 {
            break;
        }
        tokio::time::sleep(std::time::Duration::from_millis(10)).await;
    }
    assert_eq!(
        counter.load(std::sync::atomic::Ordering::SeqCst),
        1,
        "post-receive must always call the admission handler exactly once"
    );
}
