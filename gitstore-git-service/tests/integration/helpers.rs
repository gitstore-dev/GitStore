// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

use std::path::{Path, PathBuf};
use std::sync::atomic::{AtomicUsize, Ordering};
use std::sync::Arc;
use std::time::Duration;

use async_trait::async_trait;
use gitstore::git::hooks::{
    AdmissionDecision, AdmissionHandler, HookContext, RefUpdate, ResourceBlob, ValidationHandler,
};

/// Create a bare git repository at `dir` and return its path.
pub fn make_bare_repo(dir: &Path) -> PathBuf {
    let repo_path = dir.join("repo.git");
    gix::init_bare(&repo_path).expect("init_bare");
    repo_path
}

/// Commit a single file into a bare repo, creating `refs/heads/main` if needed.
/// Returns the new commit OID as a hex string.
pub fn make_commit(repo_path: &Path, msg: &str) -> String {
    let repo = gix::open(repo_path).expect("open repo");

    let sig = gix::actor::Signature {
        name: "test".into(),
        email: "test@example.com".into(),
        time: gix::date::Time::now_local_or_utc(),
    };

    let blob_oid: gix::ObjectId = repo
        .write_blob(msg.as_bytes())
        .expect("write_blob")
        .detach();

    let parent = repo.head_commit().ok().map(|c| c.id().detach());
    let parent_tree_id = parent
        .and_then(|id| repo.find_object(id).ok())
        .and_then(|obj| obj.try_into_commit().ok())
        .and_then(|c| c.tree_id().ok())
        .map(|id| id.detach())
        .unwrap_or_else(|| gix::ObjectId::empty_tree(gix::hash::Kind::Sha1));

    let new_tree = repo
        .edit_tree(parent_tree_id)
        .expect("edit_tree")
        .upsert("file.txt", gix::object::tree::EntryKind::Blob, blob_oid)
        .expect("upsert")
        .write()
        .expect("write tree")
        .detach();

    let parents: Vec<gix::ObjectId> = parent.into_iter().collect();

    let mut time_buf = gix::date::parse::TimeBuf::default();
    let sig_ref = sig.to_ref(&mut time_buf);

    let commit_id = repo
        .commit_as(sig_ref, sig_ref, "HEAD", msg, new_tree, parents)
        .expect("commit_as")
        .detach();

    // Ensure refs/heads/main exists
    use gix::refs::transaction::{Change, LogChange, PreviousValue, RefEdit, RefLog};
    use gix::refs::Target;
    repo.edit_reference(RefEdit {
        change: Change::Update {
            log: LogChange {
                mode: RefLog::AndReference,
                force_create_reflog: false,
                message: "update".into(),
            },
            expected: PreviousValue::Any,
            new: Target::Symbolic("refs/heads/main".try_into().unwrap()),
        },
        name: "HEAD".try_into().unwrap(),
        deref: false,
    })
    .ok();
    repo.edit_reference(RefEdit {
        change: Change::Update {
            log: LogChange {
                mode: RefLog::AndReference,
                force_create_reflog: false,
                message: "commit".into(),
            },
            expected: PreviousValue::Any,
            new: Target::Object(commit_id),
        },
        name: "refs/heads/main".try_into().unwrap(),
        deref: false,
    })
    .expect("update main ref");

    commit_id.to_string()
}

pub fn zero_oid() -> &'static str {
    "0000000000000000000000000000000000000000"
}

// ---------------------------------------------------------------------------
// Test doubles for AdmissionHandler
// ---------------------------------------------------------------------------

/// Always rejects with the configured reason.
pub struct RejectingAdmissionHandler(pub String);

#[async_trait]
impl AdmissionHandler for RejectingAdmissionHandler {
    async fn admit(
        &self,
        _phase: &str,
        _updates: &[RefUpdate],
        _repository_id: &str,
        _git_dir: &std::path::Path,
        _hook_ctx: &HookContext,
    ) -> anyhow::Result<AdmissionDecision> {
        Ok(AdmissionDecision::Reject(self.0.clone()))
    }
}

/// Counts how many times admit() is called.
pub struct CountingAdmissionHandler(pub Arc<AtomicUsize>);

impl CountingAdmissionHandler {
    pub fn new() -> (Self, Arc<AtomicUsize>) {
        let counter = Arc::new(AtomicUsize::new(0));
        (Self(Arc::clone(&counter)), counter)
    }
}

#[async_trait]
impl AdmissionHandler for CountingAdmissionHandler {
    async fn admit(
        &self,
        _phase: &str,
        _updates: &[RefUpdate],
        _repository_id: &str,
        _git_dir: &std::path::Path,
        _hook_ctx: &HookContext,
    ) -> anyhow::Result<AdmissionDecision> {
        self.0.fetch_add(1, Ordering::SeqCst);
        Ok(AdmissionDecision::Accept)
    }
}

// ---------------------------------------------------------------------------
// ValidationHandler test doubles (mirror of the AdmissionHandler variants above)
// ---------------------------------------------------------------------------

/// Always rejects validation with the configured reason.
pub struct RejectingValidationHandler(pub String);

#[async_trait]
impl ValidationHandler for RejectingValidationHandler {
    async fn validate(
        &self,
        _blobs: &[ResourceBlob],
        _hook_ctx: &HookContext,
    ) -> anyhow::Result<AdmissionDecision> {
        Ok(AdmissionDecision::Reject(self.0.clone()))
    }
}

/// Sleeps longer than the pipeline timeout to trigger fail-closed.
pub struct SlowValidationHandler;

#[async_trait]
impl ValidationHandler for SlowValidationHandler {
    async fn validate(
        &self,
        _blobs: &[ResourceBlob],
        _hook_ctx: &HookContext,
    ) -> anyhow::Result<AdmissionDecision> {
        tokio::time::sleep(Duration::from_secs(10)).await;
        Ok(AdmissionDecision::Accept)
    }
}
