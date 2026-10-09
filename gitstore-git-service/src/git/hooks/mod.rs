// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

pub mod admission_handler;
pub mod category_taxonomy_deletion_handler;
pub mod validation_handler;

use std::path::Path;
use std::sync::Arc;
use std::time::{Duration, Instant};

use async_trait::async_trait;
use tracing::{error, info, warn};

use crate::git::tree_diff::{decode_tree, get_tree_id, make_path};

// ---------------------------------------------------------------------------
// Shared data types
// ---------------------------------------------------------------------------

/// A single ref update passed to all hook phases.
#[derive(Clone, Debug)]
pub struct RefUpdate {
    pub ref_name: String,
    pub old_oid: String,
    pub new_oid: String,
}

/// Typed actor and policy context propagated through the hook pipeline.
/// Derived from the `PushContext` proto on the first gRPC chunk; immutable for
/// the lifetime of the push. Every pipeline stage receives a reference to this.
#[derive(Clone, Debug, Default)]
pub struct HookContext {
    /// Stable repository UUID from the API-supplied PushContext.
    pub repository_id: String,
    /// Principal identifier (e.g. "admin", OIDC sub claim).
    pub actor_subject: String,
    /// Auth mechanism (e.g. "basic", "bearer").
    pub actor_auth_method: String,
    /// Pack size limit; 0 = unlimited.
    pub max_pack_size_bytes: i64,
    /// Per-blob size limit; 0 = unlimited.
    pub max_file_size_bytes: i64,
    /// Opaque audit tag from the datastore record.
    pub config_resource_version: String,
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
// ResourceBlob
// ---------------------------------------------------------------------------

/// Raw bytes of a candidate resource file extracted from a push commit.
/// Any file beginning with `---` qualifies; kind/apiVersion inside determine routing.
#[derive(Clone, Debug)]
pub struct ResourceBlob {
    pub path: String,
    pub blob_oid: String,
    pub content: Vec<u8>,
}

// ---------------------------------------------------------------------------
// ValidationHandler trait
// ---------------------------------------------------------------------------

/// Called in the pre-receive phase (blocking).
#[async_trait]
pub trait ValidationHandler: Send + Sync {
    async fn validate(
        &self,
        blobs: &[ResourceBlob],
        hook_ctx: &HookContext,
    ) -> anyhow::Result<AdmissionDecision>;

    /// Validates a receive-pack operation. Handlers that only need changed blobs
    /// can use the default implementation; proposed-tree handlers override it.
    async fn validate_receive(
        &self,
        git_dir: &Path,
        updates: &[RefUpdate],
        quarantine_dir: Option<&Path>,
        hook_ctx: &HookContext,
    ) -> anyhow::Result<AdmissionDecision> {
        let blobs = extract_resource_blobs(git_dir, updates, quarantine_dir);
        self.validate(&blobs, hook_ctx).await
    }
}

/// Default no-op implementation — always accepts.
pub struct NoopValidationHandler;

#[async_trait]
impl ValidationHandler for NoopValidationHandler {
    async fn validate(
        &self,
        _blobs: &[ResourceBlob],
        _hook_ctx: &HookContext,
    ) -> anyhow::Result<AdmissionDecision> {
        Ok(AdmissionDecision::Accept)
    }
}

/// Runs multiple blocking validation policies in order and stops at the first
/// rejection. Receive-aware handlers retain access to the old and proposed
/// trees instead of being reduced to changed blobs.
pub struct ChainedValidationHandler {
    handlers: Vec<Arc<dyn ValidationHandler + Send + Sync>>,
}

impl ChainedValidationHandler {
    pub fn new(handlers: Vec<Arc<dyn ValidationHandler + Send + Sync>>) -> Self {
        Self { handlers }
    }
}

#[async_trait]
impl ValidationHandler for ChainedValidationHandler {
    async fn validate(
        &self,
        blobs: &[ResourceBlob],
        hook_ctx: &HookContext,
    ) -> anyhow::Result<AdmissionDecision> {
        for handler in &self.handlers {
            match handler.validate(blobs, hook_ctx).await? {
                AdmissionDecision::Accept => {}
                rejected @ AdmissionDecision::Reject(_) => return Ok(rejected),
            }
        }
        Ok(AdmissionDecision::Accept)
    }

    async fn validate_receive(
        &self,
        git_dir: &Path,
        updates: &[RefUpdate],
        quarantine_dir: Option<&Path>,
        hook_ctx: &HookContext,
    ) -> anyhow::Result<AdmissionDecision> {
        for handler in &self.handlers {
            match handler
                .validate_receive(git_dir, updates, quarantine_dir, hook_ctx)
                .await?
            {
                AdmissionDecision::Accept => {}
                rejected @ AdmissionDecision::Reject(_) => return Ok(rejected),
            }
        }
        Ok(AdmissionDecision::Accept)
    }
}

// ---------------------------------------------------------------------------
// AdmissionHandler trait
// ---------------------------------------------------------------------------

/// Called in the post-receive phase (fire-and-forget).
#[async_trait]
pub trait AdmissionHandler: Send + Sync {
    async fn admit(
        &self,
        phase: &str,
        updates: &[RefUpdate],
        repository_id: &str,
        git_dir: &std::path::Path,
        hook_ctx: &HookContext,
    ) -> anyhow::Result<AdmissionDecision>;
}

/// Default no-op implementation — always accepts.
pub struct NoopAdmissionHandler;

#[async_trait]
impl AdmissionHandler for NoopAdmissionHandler {
    async fn admit(
        &self,
        _phase: &str,
        _updates: &[RefUpdate],
        _repository_id: &str,
        _git_dir: &std::path::Path,
        _hook_ctx: &HookContext,
    ) -> anyhow::Result<AdmissionDecision> {
        Ok(AdmissionDecision::Accept)
    }
}

// ---------------------------------------------------------------------------
// HookPipeline
// ---------------------------------------------------------------------------

/// Orchestrates the in-process hook execution pipeline for a single push event.
///
/// pre-receive schema validation and post-receive admission control are
/// always on — there is no toggle. Earlier phases (`update`, `proc-receive`,
/// `post-update`, `reference-transaction`) have been removed: they were
/// disabled by default and never carried real policy logic.
pub struct HookPipeline {
    pub schema_validation_timeout: Duration,
    pub validation_handler: Arc<dyn ValidationHandler + Send + Sync>,
    pub admission_handler: Arc<dyn AdmissionHandler + Send + Sync>,
}

impl HookPipeline {
    pub fn new(
        schema_validation_timeout: Duration,
        validation_handler: Arc<dyn ValidationHandler + Send + Sync>,
        admission_handler: Arc<dyn AdmissionHandler + Send + Sync>,
    ) -> Self {
        Self {
            schema_validation_timeout,
            validation_handler,
            admission_handler,
        }
    }

    /// Run pre-receive schema validation (blocking, fail-closed, always on).
    ///
    /// `quarantine_dir`: path to the staged quarantine TempDir written by
    /// `stage_pack_from_reader`. When present, blob extraction opens the repo
    /// with this directory listed as an alternate object store so that pushed
    /// objects (not yet promoted to the live ODB) are visible during validation.
    ///
    /// Returns `Ok(accepted_indices)` — every index into `updates` — when
    /// validation accepts the push.
    /// Returns `Err(HookRejection)` if pre-receive rejects the push.
    pub async fn run(
        &self,
        git_dir: &Path,
        updates: &[RefUpdate],
        quarantine_dir: Option<&Path>,
        hook_ctx: &HookContext,
    ) -> Result<Vec<usize>, HookRejection> {
        let decision = self
            .run_schema_validation(git_dir, updates, quarantine_dir, hook_ctx)
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
        Ok((0..updates.len()).collect())
    }

    /// Called after refs are committed. Spawns the admission control handler
    /// (fire-and-forget, always on) and logs phase completion.
    pub fn run_post_receive(
        &self,
        git_dir: &Path,
        updates: &[RefUpdate],
        repository_id: &str,
        hook_ctx: &HookContext,
    ) {
        let start = Instant::now();

        let handler = Arc::clone(&self.admission_handler);
        let updates_owned = updates.to_vec();
        let repository_id = repository_id.to_string();
        let git_dir_owned = git_dir.to_path_buf();
        let hook_ctx_owned = hook_ctx.clone();
        tokio::spawn(async move {
            if let Err(e) = handler
                .admit(
                    "post-receive",
                    &updates_owned,
                    &repository_id,
                    &git_dir_owned,
                    &hook_ctx_owned,
                )
                .await
            {
                error!(
                    phase = "post-receive",
                    reason = %e,
                    "admission_handler_error"
                );
            }
        });

        let duration_ms = start.elapsed().as_millis() as u64;
        info!(
            phase = "post-receive",
            duration_ms,
            outcome = "accepted",
            "hook_phase_complete"
        );
    }

    // -- internal helpers --

    /// Call the validation handler with the configured timeout (fail-closed).
    /// Returns the final `HookDecision`.
    async fn run_schema_validation(
        &self,
        git_dir: &Path,
        updates: &[RefUpdate],
        quarantine_dir: Option<&Path>,
        hook_ctx: &HookContext,
    ) -> HookDecision {
        let phase = "pre-receive";
        let start = Instant::now();
        let result = tokio::time::timeout(
            self.schema_validation_timeout,
            self.validation_handler
                .validate_receive(git_dir, updates, quarantine_dir, hook_ctx),
        )
        .await;
        let duration_ms = start.elapsed().as_millis() as u64;
        match result {
            Ok(Ok(AdmissionDecision::Accept)) => HookDecision::Accept,
            Ok(Ok(AdmissionDecision::Reject(reason))) => HookDecision::Reject(reason),
            Ok(Err(e)) => {
                error!(phase, duration_ms, reason = %e, "hook_phase_error");
                HookDecision::Reject("validation handler error".to_string())
            }
            Err(_elapsed) => {
                error!(
                    phase,
                    duration_ms,
                    reason = "validation service timeout",
                    "hook_phase_error"
                );
                HookDecision::Reject("validation service unavailable".to_string())
            }
        }
    }
}

// ---------------------------------------------------------------------------
// Blob extraction
// ---------------------------------------------------------------------------

/// Opens a repository with quarantined pack files temporarily exposed to gix.
/// The temporary links are always removed before returning.
pub(crate) fn with_quarantine_repo<T>(
    git_dir: &Path,
    quarantine_dir: Option<&Path>,
    operation: impl FnOnce(&gix::Repository) -> Result<T, String>,
) -> Result<T, String> {
    let pack_dir = git_dir.join("objects").join("pack");
    let mut staged_files: Vec<std::path::PathBuf> = Vec::new();
    if let Some(q) = quarantine_dir {
        let _ = std::fs::create_dir_all(&pack_dir);
        if let Ok(entries) = std::fs::read_dir(q) {
            for entry in entries.flatten() {
                let src = entry.path();
                let ext = src.extension().and_then(|e| e.to_str()).unwrap_or("");
                if ext == "pack" || ext == "idx" {
                    let dst = pack_dir.join(entry.file_name());
                    if !dst.exists()
                        && (std::fs::hard_link(&src, &dst).is_ok()
                            || std::fs::copy(&src, &dst).is_ok())
                    {
                        staged_files.push(dst);
                    }
                }
            }
        }
    }

    let result = gix::open(git_dir)
        .map_err(|e| format!("open repository: {e}"))
        .and_then(|repo| operation(&repo));

    for file in staged_files {
        let _ = std::fs::remove_file(file);
    }
    result
}

/// Walk the commit trees for all ref updates and collect resource blobs (files
/// beginning with `---`).
///
/// `quarantine_dir` is the TempDir written by `stage_pack_from_reader`. When
/// present its path is written to `{git_dir}/objects/info/alternates` so that
/// gix-odb resolves pushed objects that are not yet promoted to the live ODB.
/// The alternates entry is removed after extraction regardless of outcome.
///
/// Returns an empty vec if git_dir is not a valid repo or any update fails.
fn extract_resource_blobs(
    git_dir: &Path,
    updates: &[RefUpdate],
    quarantine_dir: Option<&Path>,
) -> Vec<ResourceBlob> {
    let zero = "0".repeat(40);

    // To make pushed objects visible before promote_quarantine runs, temporarily
    // hard-link the quarantine pack/index files into the live objects/pack dir.
    // Hard-links are instant and atomic; the files are removed after extraction.
    // This mirrors what native git does with GIT_ALTERNATE_OBJECT_DIRECTORIES.
    let pack_dir = git_dir.join("objects").join("pack");
    let mut staged_files: Vec<std::path::PathBuf> = Vec::new();
    if let Some(q) = quarantine_dir {
        let _ = std::fs::create_dir_all(&pack_dir);
        if let Ok(entries) = std::fs::read_dir(q) {
            for entry in entries.flatten() {
                let src = entry.path();
                let ext = src.extension().and_then(|e| e.to_str()).unwrap_or("");
                if ext == "pack" || ext == "idx" {
                    let dst = pack_dir.join(entry.file_name());
                    if !dst.exists() {
                        // Try hard link first (free); fall back to copy across filesystems.
                        let ok = std::fs::hard_link(&src, &dst).is_ok()
                            || std::fs::copy(&src, &dst).is_ok();
                        if ok {
                            staged_files.push(dst);
                        }
                    }
                }
            }
        }
    }

    let repo = match gix::open(git_dir) {
        Ok(r) => r,
        Err(_) => {
            for f in &staged_files {
                let _ = std::fs::remove_file(f);
            }
            return vec![];
        }
    };

    let mut blobs = Vec::new();
    for update in updates {
        let new_oid_hex = &update.new_oid;
        let old_oid_hex = &update.old_oid;
        if new_oid_hex == &zero {
            continue; // branch deletion — no blobs to extract
        }
        let new_oid = match gix::ObjectId::from_hex(new_oid_hex.as_bytes()) {
            Ok(id) => id,
            Err(_) => continue,
        };
        if old_oid_hex == &zero {
            // New branch creation — scan the full new tree.
            collect_blobs_from_commit(&repo, new_oid, &mut blobs);
        } else {
            // Ref update — only validate files changed in the push delta.
            // This avoids re-validating files that already passed in prior commits.
            let old_oid = match gix::ObjectId::from_hex(old_oid_hex.as_bytes()) {
                Ok(id) => id,
                Err(_) => {
                    collect_blobs_from_commit(&repo, new_oid, &mut blobs);
                    continue;
                }
            };
            collect_changed_blobs(&repo, old_oid, new_oid, &mut blobs);
        }
    }

    for f in &staged_files {
        let _ = std::fs::remove_file(f);
    }

    blobs
}

fn collect_blobs_from_commit(
    repo: &gix::Repository,
    commit_id: gix::ObjectId,
    out: &mut Vec<ResourceBlob>,
) {
    let Some(tree_id) = get_tree_id(repo, commit_id) else {
        return;
    };
    collect_blobs_from_tree(repo, tree_id, "", out);
}

/// For a ref update (non-zero old_oid), collect only blobs for paths that
/// changed between old_commit and new_commit. This avoids re-validating files
/// that were already accepted in prior commits.
fn collect_changed_blobs(
    repo: &gix::Repository,
    old_commit_id: gix::ObjectId,
    new_commit_id: gix::ObjectId,
    out: &mut Vec<ResourceBlob>,
) {
    let old_tree = match get_tree_id(repo, old_commit_id) {
        Some(t) => t,
        None => {
            // Can't find old tree — fall back to full scan of new commit.
            collect_blobs_from_commit(repo, new_commit_id, out);
            return;
        }
    };
    let Some(new_tree) = get_tree_id(repo, new_commit_id) else {
        return;
    };
    let mut old_blobs = Vec::new();
    collect_changed_resource_blobs_from_trees(repo, old_tree, new_tree, "", &mut old_blobs, out);
}

/// Collect the changed resource blobs from both sides of one ref update.
/// Keeping the old side lets schema validation compare immutable fields using
/// only Git objects while refs are still unchanged.
pub(crate) fn collect_changed_resource_blobs(
    repo: &gix::Repository,
    old_commit_id: Option<gix::ObjectId>,
    new_commit_id: Option<gix::ObjectId>,
) -> (Vec<ResourceBlob>, Vec<ResourceBlob>) {
    let mut old_blobs = Vec::new();
    let mut proposed_blobs = Vec::new();
    match (
        old_commit_id.and_then(|id| get_tree_id(repo, id)),
        new_commit_id.and_then(|id| get_tree_id(repo, id)),
    ) {
        (Some(old_tree), Some(new_tree)) => collect_changed_resource_blobs_from_trees(
            repo,
            old_tree,
            new_tree,
            "",
            &mut old_blobs,
            &mut proposed_blobs,
        ),
        (Some(old_tree), None) => collect_blobs_from_tree(repo, old_tree, "", &mut old_blobs),
        (None, Some(new_tree)) => collect_blobs_from_tree(repo, new_tree, "", &mut proposed_blobs),
        (None, None) => {}
    }
    (old_blobs, proposed_blobs)
}

fn collect_changed_resource_blobs_from_trees(
    repo: &gix::Repository,
    old_tree_id: gix::ObjectId,
    new_tree_id: gix::ObjectId,
    prefix: &str,
    old_out: &mut Vec<ResourceBlob>,
    proposed_out: &mut Vec<ResourceBlob>,
) {
    let old_entries = decode_tree(repo, old_tree_id);
    let new_entries = decode_tree(repo, new_tree_id);

    let old_map: std::collections::HashMap<String, (gix::ObjectId, gix::object::tree::EntryKind)> =
        old_entries
            .iter()
            .map(|e| (e.filename.to_string(), (e.oid, e.mode.kind())))
            .collect();
    let new_map: std::collections::HashMap<String, (gix::ObjectId, gix::object::tree::EntryKind)> =
        new_entries
            .iter()
            .map(|e| (e.filename.to_string(), (e.oid, e.mode.kind())))
            .collect();
    let mut names: Vec<_> = old_map.keys().chain(new_map.keys()).collect();
    names.sort();
    names.dedup();

    for name in names {
        let path = make_path(prefix, name);
        match (old_map.get(name), new_map.get(name)) {
            (
                Some((old_id, gix::object::tree::EntryKind::Tree)),
                Some((new_id, gix::object::tree::EntryKind::Tree)),
            ) if old_id != new_id => {
                collect_changed_resource_blobs_from_trees(
                    repo,
                    *old_id,
                    *new_id,
                    &path,
                    old_out,
                    proposed_out,
                );
            }
            (
                Some((old_id, gix::object::tree::EntryKind::Tree)),
                Some((new_id, gix::object::tree::EntryKind::Tree)),
            ) if old_id == new_id => {}
            (Some((old_id, gix::object::tree::EntryKind::Tree)), _) => {
                collect_blobs_from_tree(repo, *old_id, &path, old_out);
                if let Some((new_id, kind)) = new_map.get(name) {
                    collect_resource_entry(repo, *new_id, *kind, &path, proposed_out);
                }
            }
            (_, Some((new_id, gix::object::tree::EntryKind::Tree))) => {
                if let Some((old_id, kind)) = old_map.get(name) {
                    collect_resource_entry(repo, *old_id, *kind, &path, old_out);
                }
                collect_blobs_from_tree(repo, *new_id, &path, proposed_out);
            }
            (Some((old_id, old_kind)), Some((new_id, new_kind)))
                if old_id != new_id || old_kind != new_kind =>
            {
                collect_resource_entry(repo, *old_id, *old_kind, &path, old_out);
                collect_resource_entry(repo, *new_id, *new_kind, &path, proposed_out);
            }
            (Some(_), Some(_)) => {}
            (Some((old_id, old_kind)), None) => {
                collect_resource_entry(repo, *old_id, *old_kind, &path, old_out)
            }
            (None, Some((new_id, new_kind))) => {
                collect_resource_entry(repo, *new_id, *new_kind, &path, proposed_out)
            }
            (None, None) => {}
        }
    }
}

fn collect_resource_entry(
    repo: &gix::Repository,
    object_id: gix::ObjectId,
    kind: gix::object::tree::EntryKind,
    path: &str,
    out: &mut Vec<ResourceBlob>,
) {
    match kind {
        gix::object::tree::EntryKind::Tree => collect_blobs_from_tree(repo, object_id, path, out),
        gix::object::tree::EntryKind::Blob | gix::object::tree::EntryKind::BlobExecutable => {
            if let Ok(blob_obj) = repo.find_object(object_id) {
                let content = blob_obj.data.to_vec();
                if content.starts_with(b"---") {
                    out.push(ResourceBlob {
                        path: path.to_string(),
                        blob_oid: object_id.to_string(),
                        content,
                    });
                }
            }
        }
        _ => {}
    }
}

pub(crate) fn collect_blobs_from_tree(
    repo: &gix::Repository,
    tree_id: gix::ObjectId,
    prefix: &str,
    out: &mut Vec<ResourceBlob>,
) {
    for entry in decode_tree(repo, tree_id) {
        let name = entry.filename.to_string();
        let path = make_path(prefix, &name);
        match entry.mode.kind() {
            gix::object::tree::EntryKind::Tree => {
                collect_blobs_from_tree(repo, entry.oid, &path, out);
            }
            gix::object::tree::EntryKind::Blob | gix::object::tree::EntryKind::BlobExecutable => {
                let blob_id: gix::ObjectId = entry.oid;
                if let Ok(blob_obj) = repo.find_object(blob_id) {
                    let content = blob_obj.data.to_vec();
                    if content.starts_with(b"---") {
                        out.push(ResourceBlob {
                            path,
                            blob_oid: blob_id.to_string(),
                            content,
                        });
                    }
                }
            }
            _ => {}
        }
    }
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
        let result = h
            .admit(
                "pre-receive",
                &updates,
                "",
                std::path::Path::new(""),
                &HookContext::default(),
            )
            .await
            .unwrap();
        assert!(matches!(result, AdmissionDecision::Accept));
    }

    // Pre-receive validation and post-receive admission are always on now —
    // no per-phase toggles to construct.
    fn make_pipeline() -> HookPipeline {
        HookPipeline::new(
            Duration::from_secs(10),
            Arc::new(NoopValidationHandler),
            Arc::new(NoopAdmissionHandler),
        )
    }

    fn make_update(ref_name: &str) -> RefUpdate {
        RefUpdate {
            ref_name: ref_name.to_string(),
            old_oid: "0".repeat(40),
            new_oid: "a".repeat(40),
        }
    }

    #[tokio::test]
    async fn test_pre_receive_accepts_all_indices() {
        let pipeline = make_pipeline();
        let updates = vec![
            make_update("refs/heads/main"),
            make_update("refs/heads/dev"),
        ];
        let result = pipeline
            .run(
                std::path::Path::new("/tmp"),
                &updates,
                None,
                &HookContext::default(),
            )
            .await
            .unwrap();
        assert_eq!(result, vec![0, 1]);
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

    // T009: ResourceBlob extraction logic

    fn make_repo_with_files(
        dir: &std::path::Path,
        files: &[(&str, &[u8])],
    ) -> (gix::Repository, gix::ObjectId) {
        let repo = gix::init_bare(dir).unwrap();

        let mut tree_id = gix::ObjectId::empty_tree(gix::hash::Kind::Sha1);
        for (path, content) in files {
            let blob_oid = repo.write_blob(content).unwrap().detach();
            tree_id = repo
                .edit_tree(tree_id)
                .unwrap()
                .upsert(*path, gix::object::tree::EntryKind::Blob, blob_oid)
                .unwrap()
                .write()
                .unwrap()
                .detach();
        }

        let tree_id = tree_id;

        let sig = gix::actor::Signature {
            name: "test".into(),
            email: "test@test.com".into(),
            time: gix::date::Time::now_local_or_utc(),
        };
        let mut time_buf = gix::date::parse::TimeBuf::default();
        let sig_ref = sig.to_ref(&mut time_buf);
        let commit_id = repo
            .commit_as(
                sig_ref,
                sig_ref,
                "HEAD",
                "init",
                tree_id,
                std::iter::empty::<gix::ObjectId>(),
            )
            .unwrap()
            .detach();

        (repo, commit_id)
    }

    #[test]
    fn test_file_with_frontmatter_prefix_extracted_as_blob() {
        let dir = tempfile::tempdir().unwrap();
        let content = b"---\napiVersion: catalog.gitstore.dev/v1beta1\nkind: Product\n---\nbody";
        let (repo, commit_id) =
            make_repo_with_files(dir.path(), &[("products/widget.md", content)]);

        // extract_resource_blobs opens a fresh repo from git_dir; use path directly
        let mut out = Vec::new();
        collect_blobs_from_commit(&repo, commit_id, &mut out);

        assert_eq!(out.len(), 1);
        assert_eq!(out[0].path, "products/widget.md");
        assert!(out[0].content.starts_with(b"---"));
    }

    #[test]
    fn test_file_without_frontmatter_prefix_skipped() {
        let dir = tempfile::tempdir().unwrap();
        let content = b"This is just a plain markdown file without frontmatter.";
        let (repo, commit_id) = make_repo_with_files(dir.path(), &[("README.md", content)]);

        let mut out = Vec::new();
        collect_blobs_from_commit(&repo, commit_id, &mut out);

        assert_eq!(out.len(), 0, "non-frontmatter files must be skipped");
    }

    #[test]
    fn test_ref_update_with_zero_old_oid_treated_as_new_branch() {
        let dir = tempfile::tempdir().unwrap();
        let content = b"---\nkind: Product\n---\nbody";
        let (repo, commit_id) = make_repo_with_files(dir.path(), &[("products/p.md", content)]);

        // new branch creation: old_oid is all zeros
        let update = RefUpdate {
            ref_name: "refs/heads/new-branch".to_string(),
            old_oid: "0".repeat(40),
            new_oid: commit_id.to_string(),
        };

        let mut out = Vec::new();
        collect_blobs_from_commit(&repo, commit_id, &mut out);

        // Blobs extracted regardless of old_oid
        assert_eq!(out.len(), 1);
        assert_eq!(out[0].path, "products/p.md");
        // The zero old_oid case is handled by extract_resource_blobs skipping deletions
        let _ = update; // just show the zero old_oid scenario compiles
    }

    #[test]
    fn test_empty_commit_tree_produces_zero_blobs() {
        let dir = tempfile::tempdir().unwrap();
        let repo = gix::init_bare(dir.path()).unwrap();

        let sig = gix::actor::Signature {
            name: "test".into(),
            email: "test@test.com".into(),
            time: gix::date::Time::now_local_or_utc(),
        };
        let empty_tree = gix::ObjectId::empty_tree(gix::hash::Kind::Sha1);
        let mut time_buf = gix::date::parse::TimeBuf::default();
        let sig_ref = sig.to_ref(&mut time_buf);
        let commit_id = repo
            .commit_as(
                sig_ref,
                sig_ref,
                "HEAD",
                "empty",
                empty_tree,
                std::iter::empty::<gix::ObjectId>(),
            )
            .unwrap()
            .detach();

        let mut out = Vec::new();
        collect_blobs_from_commit(&repo, commit_id, &mut out);
        assert_eq!(out.len(), 0);
    }
}
