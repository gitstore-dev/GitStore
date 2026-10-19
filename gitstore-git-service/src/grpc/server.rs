// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

// gRPC service implementation for the GitService contract (gitstore.git.v1).

#![allow(clippy::result_large_err)]

use dashmap::DashMap;
use std::collections::VecDeque;
use std::fs::{File, OpenOptions};
use std::io;
use std::path::{Path, PathBuf};
use std::sync::atomic::{AtomicBool, Ordering};
use std::sync::Arc;
use tokio::sync::{oneshot, Mutex, Notify, OwnedSemaphorePermit, RwLock, Semaphore};
use tokio_stream::Stream;
use tonic::{Request, Response, Status};

use super::metrics::{start_file_read_phase, FileReadPhase};
use crate::auth::interceptor::RPCVerifier;
use crate::git::hooks::{HookPipeline, NoopAdmissionHandler, NoopValidationHandler, RefUpdate};
use crate::git::repo::{create_repository, delete_repository, fanout_path, list_tags};
use tracing::{debug, error, info};
use tracing_opentelemetry::OpenTelemetrySpanExt;

pub mod proto {
    include!(concat!(
        env!("CARGO_MANIFEST_DIR"),
        "/gen/gitstore/git/v1/gitstore.git.v1.rs"
    ));
}

pub mod catalog_proto {
    include!(concat!(
        env!("CARGO_MANIFEST_DIR"),
        "/gen/gitstore/catalog/v1/gitstore.catalog.v1.rs"
    ));
}

use proto::git_service_server::GitService;
use proto::*;

use crate::git::hooks::HookContext;

const COMMIT_FILE_MAX_ATTEMPTS: usize = 32;
const COMMIT_FILE_LOCK_TIMEOUT: std::time::Duration = std::time::Duration::from_secs(30);
const COMMIT_FILE_COHORT_LIMIT: usize = 32;
const COMMIT_FILE_PENDING_LIMIT: usize = 64;
const DELETE_RECEIPT_PREFIX: &str = "refs/gitstore/deletions/";
const DELETE_RECEIPT_HISTORY_LIMIT: usize = 4096;

/// Dropping the response consumer interrupts its background pack reads.
#[derive(Debug)]
pub struct CancellableUploadPackStream {
    inner: tokio_stream::wrappers::ReceiverStream<Result<UploadPackResponse, Status>>,
    interrupt: Arc<AtomicBool>,
}

impl Stream for CancellableUploadPackStream {
    type Item = Result<UploadPackResponse, Status>;

    fn poll_next(
        self: std::pin::Pin<&mut Self>,
        cx: &mut std::task::Context<'_>,
    ) -> std::task::Poll<Option<Self::Item>> {
        std::pin::Pin::new(&mut self.get_mut().inner).poll_next(cx)
    }
}

impl Drop for CancellableUploadPackStream {
    fn drop(&mut self) {
        self.interrupt.store(true, Ordering::Release);
    }
}

fn attach_file_read_report<T>(
    response: &mut Response<T>,
    durations: [std::time::Duration; 10],
    history_commits: usize,
) -> Result<(), Status> {
    let headers = [
        "x-gitstore-file-read-authentication-ns",
        "x-gitstore-file-read-repository-lock-ns",
        "x-gitstore-file-read-blocking-queue-ns",
        "x-gitstore-file-read-payload-ns",
        "x-gitstore-file-read-history-commit-ns",
        "x-gitstore-file-read-history-tree-ns",
        "x-gitstore-file-read-repository-open-ns",
        "x-gitstore-file-read-revision-ns",
        "x-gitstore-file-read-target-tree-ns",
        "x-gitstore-file-read-blob-ns",
    ];
    for (key, duration) in headers.into_iter().zip(durations) {
        response.metadata_mut().insert(
            key,
            duration
                .as_nanos()
                .to_string()
                .parse()
                .map_err(|_| Status::internal("failed to encode File read phase duration"))?,
        );
    }
    response.metadata_mut().insert(
        "x-gitstore-file-read-history-commits",
        history_commits
            .to_string()
            .parse()
            .map_err(|_| Status::internal("failed to encode File history work"))?,
    );
    Ok(())
}

/// T047: Convert a proto PushContext into a HookContext for the pipeline.
impl From<&PushContext> for HookContext {
    fn from(ctx: &PushContext) -> Self {
        let actor = ctx
            .authorization
            .as_ref()
            .and_then(|authz| authz.actor.as_ref());
        HookContext {
            repository_id: ctx.repository_id.clone(),
            actor_subject: actor.map(|a| a.subject.clone()).unwrap_or_default(),
            actor_auth_method: actor.map(|a| a.auth_method.clone()).unwrap_or_default(),
            actor_groups: actor.map(|a| a.groups.clone()).unwrap_or_default(),
            actor_service_account_uid: actor
                .map(|a| a.service_account_uid.clone())
                .unwrap_or_default(),
            // Correlation identity is generated inside the authenticated Git
            // receive boundary, never copied from author-supplied metadata.
            receive_id: {
                static NEXT: std::sync::atomic::AtomicU64 = std::sync::atomic::AtomicU64::new(0);
                format!(
                    "{:x}-{:x}-{:x}",
                    std::process::id(),
                    std::time::SystemTime::now()
                        .duration_since(std::time::UNIX_EPOCH)
                        .unwrap_or_default()
                        .as_nanos(),
                    NEXT.fetch_add(1, std::sync::atomic::Ordering::Relaxed)
                )
            },
            max_pack_size_bytes: ctx
                .policy
                .as_ref()
                .map(|p| p.max_pack_size_bytes)
                .unwrap_or(0),
            max_file_size_bytes: ctx
                .policy
                .as_ref()
                .map(|p| p.max_file_size_bytes)
                .unwrap_or(0),
            config_resource_version: ctx.config_resource_version.clone(),
            resource_deletion_validation_complete: Default::default(),
        }
    }
}

/// Shared static platform push-size ceiling (see config::PushLimitsConfig).
/// The effective per-push limit is `min(repository policy, ceiling)`,
/// treating a 0/absent repository policy as the ceiling itself.
#[derive(Clone, Copy, Debug)]
pub struct PushLimits {
    pub max_pack_size: u64,
    pub max_file_size: u64,
}

impl Default for PushLimits {
    fn default() -> Self {
        // Mirrors config::default_toml's push_limits defaults.
        Self {
            max_pack_size: 512 * 1024 * 1024,
            max_file_size: 100 * 1024 * 1024,
        }
    }
}

impl PushLimits {
    /// Resolves the effective limit for a single push: the repository's
    /// configured policy value clamped to the ceiling, or the ceiling
    /// itself when the repository policy is 0/absent (there is no more
    /// "0 = unlimited").
    fn effective_pack_limit(&self, repository_policy: i64) -> i64 {
        effective_limit(repository_policy, self.max_pack_size)
    }

    fn effective_file_limit(&self, repository_policy: i64) -> i64 {
        effective_limit(repository_policy, self.max_file_size)
    }
}

/// `repository_policy > 0` -> `min(repository_policy, ceiling)`; otherwise
/// the ceiling itself. The ceiling is always positive (config validation
/// enforces this), so the result is always a positive, enforceable limit.
fn effective_limit(repository_policy: i64, ceiling: u64) -> i64 {
    let ceiling = ceiling.min(i64::MAX as u64) as i64;
    if repository_policy > 0 {
        repository_policy.min(ceiling)
    } else {
        ceiling
    }
}

#[derive(Default)]
pub struct RepositoryLocks {
    operation: Arc<RwLock<()>>,
    snapshot: Arc<RwLock<()>>,
    commits: Arc<CommitFileQueue>,
}

struct CommitFileEntry {
    request: CommitFileRequest,
    span: tracing::Span,
    result: oneshot::Sender<Result<CommitFileResponse, Status>>,
    _permit: OwnedSemaphorePermit,
}

#[derive(Default)]
struct CommitFilePending {
    entries: VecDeque<CommitFileEntry>,
    running: bool,
}

#[derive(Default)]
struct CommitFileQueue {
    pending: Mutex<CommitFilePending>,
    changed: Notify,
}

struct CommitFileCancellation(Arc<CommitFileQueue>);

impl Drop for CommitFileCancellation {
    fn drop(&mut self) {
        self.0.changed.notify_one();
    }
}

pub struct GitServiceImpl {
    rpc_auth: RPCVerifier,
    pub data_root: Arc<PathBuf>,
    pub repo_locks: Arc<DashMap<String, RepositoryLocks>>,
    pub hook_pipeline: Arc<HookPipeline>,
    pub push_limits: PushLimits,
    commit_slots: Arc<Semaphore>,
}

enum ReceivePackRepositoryGuard {
    Shared {
        _guard: tokio::sync::OwnedRwLockReadGuard<()>,
    },
    Exclusive {
        _guard: tokio::sync::OwnedRwLockWriteGuard<()>,
    },
}

impl GitServiceImpl {
    pub fn new(data_root: PathBuf, rpc_auth: RPCVerifier) -> Self {
        Self::with_pipeline(
            data_root,
            Arc::new(HookPipeline::new(
                std::time::Duration::from_secs(10),
                Arc::new(NoopValidationHandler),
                Arc::new(NoopAdmissionHandler),
            )),
            PushLimits::default(),
            rpc_auth,
        )
    }

    pub fn with_pipeline(
        data_root: PathBuf,
        hook_pipeline: Arc<HookPipeline>,
        push_limits: PushLimits,
        rpc_auth: RPCVerifier,
    ) -> Self {
        Self {
            rpc_auth,
            data_root: Arc::new(data_root),
            repo_locks: Arc::new(DashMap::new()),
            hook_pipeline,
            push_limits,
            commit_slots: Arc::new(Semaphore::new(COMMIT_FILE_PENDING_LIMIT)),
        }
    }
}

// --- helpers -----------------------------------------------------------------

/// Resolve repository_id to its fanout path; returns NOT_FOUND if absent.
fn resolve_repo_path(data_root: &Path, id: &str) -> Result<PathBuf, Status> {
    let path = fanout_path(data_root, id)?;
    if !path.exists() {
        return Err(Status::not_found(format!("repository '{}' not found", id)));
    }
    Ok(path)
}

// File-read NOT_FOUND proves path absence, not an unavailable repository or ref.
fn file_read_target_error(error: Status) -> Status {
    if error.code() == tonic::Code::NotFound {
        Status::failed_precondition(error.message())
    } else {
        error
    }
}

/// Get or insert a per-repository lock.
fn get_or_insert_lock(repo_locks: &DashMap<String, RepositoryLocks>, id: &str) -> Arc<RwLock<()>> {
    repo_locks
        .entry(id.to_string())
        .or_default()
        .operation
        .clone()
}

fn get_or_insert_snapshot_lock(
    repo_locks: &DashMap<String, RepositoryLocks>,
    id: &str,
) -> Arc<RwLock<()>> {
    repo_locks
        .entry(id.to_string())
        .or_default()
        .snapshot
        .clone()
}

async fn acquire_commit_guard<T>(
    queue: &CommitFileQueue,
    acquire: impl std::future::Future<Output = T>,
) -> Option<T> {
    tokio::pin!(acquire);
    loop {
        let changed = queue.changed.notified();
        {
            let mut pending = queue.pending.lock().await;
            pending.entries.retain(|entry| !entry.result.is_closed());
            if pending.entries.is_empty() {
                pending.running = false;
                return None;
            }
        }
        tokio::select! {
            guard = &mut acquire => return Some(guard),
            _ = changed => {}
        }
    }
}

async fn run_commit_file_queue(
    repo_path: PathBuf,
    operation: Arc<RwLock<()>>,
    snapshot: Arc<RwLock<()>>,
    queue: Arc<CommitFileQueue>,
) {
    loop {
        let Some(operation_guard) =
            acquire_commit_guard(&queue, operation.clone().read_owned()).await
        else {
            return;
        };
        let Some(snapshot_guard) =
            acquire_commit_guard(&queue, snapshot.clone().write_owned()).await
        else {
            return;
        };
        let entries = {
            let mut pending = queue.pending.lock().await;
            pending.entries.retain(|entry| !entry.result.is_closed());
            let count = pending.entries.len().min(COMMIT_FILE_COHORT_LIMIT);
            pending.entries.drain(..count).collect::<Vec<_>>()
        };
        if entries.is_empty() {
            continue;
        }
        let path = repo_path.clone();
        let publication = crate::spawn_blocking(move || {
            let _operation_guard = operation_guard;
            let _snapshot_guard = snapshot_guard;
            let results = publish_commit_file_cohort(&path, &entries);
            match results {
                Ok(results) => {
                    for (entry, result) in entries.into_iter().zip(results) {
                        let _ = entry.result.send(result);
                    }
                }
                Err(error) => {
                    for entry in entries {
                        let _ = entry.result.send(Err(error.clone()));
                    }
                }
            }
        });
        tokio::pin!(publication);
        let result = loop {
            tokio::select! {
                result = &mut publication => break result,
                _ = queue.changed.notified() => {
                    queue.pending.lock().await.entries.retain(|entry| !entry.result.is_closed());
                }
            }
        };
        if let Err(error) = result {
            error!(%error, "CommitFile cohort worker failed");
            let mut pending = queue.pending.lock().await;
            for entry in pending.entries.drain(..) {
                let _ = entry
                    .result
                    .send(Err(Status::internal("CommitFile cohort worker failed")));
            }
            pending.running = false;
            return;
        }
    }
}

fn publish_commit_file_ref(
    repo: &gix::Repository,
    expected: Option<gix::ObjectId>,
    commit: gix::ObjectId,
    signature: &gix::actor::Signature,
    message: &str,
) -> Result<(), Status> {
    use gix::refs::transaction::{Change, LogChange, PreviousValue, RefEdit, RefLog};
    let expected = match expected {
        Some(id) => PreviousValue::MustExistAndMatch(gix::refs::Target::Object(id)),
        None => PreviousValue::MustNotExist,
    };
    let edit = RefEdit {
        change: Change::Update {
            log: LogChange {
                mode: RefLog::AndReference,
                force_create_reflog: false,
                message: format!("commit: {}", message.lines().next().unwrap_or("")).into(),
            },
            expected,
            new: gix::refs::Target::Object(commit),
        },
        name: "HEAD"
            .try_into()
            .map_err(|error: gix::refs::name::Error| Status::internal(error.to_string()))?,
        deref: true,
    };
    let fail =
        gix::lock::acquire::Fail::AfterDurationWithBackoff(std::time::Duration::from_secs(1));
    let transaction = repo
        .refs
        .transaction()
        .prepare([edit], fail, fail)
        .map_err(|error| {
            if is_reference_content_conflict(&error) {
                Status::aborted(format!("CommitFile reference changed: {error}"))
            } else {
                Status::internal(format!("prepare CommitFile reference: {error}"))
            }
        })?;
    let mut time = gix::date::parse::TimeBuf::default();
    transaction
        .commit(Some(signature.to_ref(&mut time)))
        .map_err(|error| Status::internal(format!("publish CommitFile reference: {error}")))?;
    Ok(())
}

fn publish_commit_file_cohort(
    repo_path: &Path,
    entries: &[CommitFileEntry],
) -> Result<Vec<Result<CommitFileResponse, Status>>, Status> {
    if entries.is_empty() || entries.len() > COMMIT_FILE_COHORT_LIMIT {
        return Err(Status::internal("invalid CommitFile cohort size"));
    }
    let marker_started = std::time::Instant::now();
    let _repository_guard = acquire_repository_commit_lock(
        &repo_path.join("gitstore-commit.lock"),
        COMMIT_FILE_LOCK_TIMEOUT,
    )
    .map_err(|error| {
        Status::resource_exhausted(format!("acquire repository commit lock: {error}"))
    })?;
    debug!(
        marker_lock_wait_seconds = marker_started.elapsed().as_secs_f64(),
        "CommitFile repository marker lock acquired"
    );
    let objects = gix::open(repo_path)
        .map_err(|error| Status::internal(format!("failed to open repo: {error}")))?;
    let prepared = entries
        .iter()
        .map(|entry| {
            validate_file_path(&entry.request.path)?;
            let signature = gix::actor::Signature {
                name: if entry.request.author_name.is_empty() {
                    "GitStore"
                } else {
                    &entry.request.author_name
                }
                .into(),
                email: if entry.request.author_email.is_empty() {
                    "gitstore@localhost"
                } else {
                    &entry.request.author_email
                }
                .into(),
                time: gix::date::Time::now_local_or_utc(),
            };
            let blob = objects
                .write_blob(&entry.request.content)
                .map_err(|error| Status::internal(format!("write_blob: {error}")))?
                .detach();
            Ok((blob, signature))
        })
        .collect::<Vec<Result<_, Status>>>();
    for attempt in 0..COMMIT_FILE_MAX_ATTEMPTS {
        let repo = gix::open(repo_path)
            .map_err(|error| Status::internal(format!("failed to reopen repo: {error}")))?;
        let head =
            if repo
                .head()
                .map_err(|error| Status::internal(format!("read CommitFile HEAD: {error}")))?
                .is_unborn()
            {
                None
            } else {
                Some(repo.head_commit().map_err(|error| {
                    Status::internal(format!("read CommitFile parent: {error}"))
                })?)
            };
        let base = head.as_ref().map(|head| head.id().detach());
        let mut parent = base;
        let mut tree = match head {
            Some(head) => head
                .tree_id()
                .map_err(|error| Status::internal(error.to_string()))?
                .detach(),
            None => gix::ObjectId::empty_tree(gix::hash::Kind::Sha1),
        };
        let mut last = None;
        let mut results = Vec::with_capacity(entries.len());
        let mut commits = Vec::with_capacity(entries.len());
        for (index, (entry, prepared)) in entries.iter().zip(&prepared).enumerate() {
            let result = (|| {
                let (blob, signature) = prepared.as_ref().map_err(Clone::clone)?;
                let next_tree = repo
                    .edit_tree(tree)
                    .map_err(|error| Status::internal(format!("edit_tree: {error}")))?
                    .upsert(
                        entry.request.path.as_str(),
                        gix::object::tree::EntryKind::Blob,
                        *blob,
                    )
                    .map_err(|error| Status::internal(format!("upsert: {error}")))?
                    .write()
                    .map_err(|error| Status::internal(format!("tree write: {error}")))?
                    .detach();
                let id = repo
                    .write_object(&gix::objs::Commit {
                        tree: next_tree,
                        parents: parent.into_iter().collect(),
                        author: signature.clone(),
                        committer: signature.clone(),
                        encoding: None,
                        message: entry.request.commit_message.as_str().into(),
                        extra_headers: vec![],
                    })
                    .map_err(|error| Status::internal(format!("write commit: {error}")))?
                    .detach();
                let previous = parent;
                parent = Some(id);
                tree = next_tree;
                last = Some((index, signature));
                Ok((id, previous))
            })();
            match result {
                Ok((id, parent)) => {
                    commits.push((index, id, parent));
                    results.push(Ok(CommitFileResponse {
                        commit_sha: id.to_string(),
                        branch: "main".into(),
                    }));
                }
                Err(error) => results.push(Err(error)),
            }
        }
        let Some((last_index, signature)) = last else {
            return Ok(results);
        };
        let tip = parent.expect("successful cohort has a tip");
        // The entire immutable chain is published against its original base,
        // not against an intermediate commit that has never been HEAD.
        match publish_commit_file_ref(
            &repo,
            base,
            tip,
            signature,
            &entries[last_index].request.commit_message,
        ) {
            Ok(()) => {
                let committed = commits.len();
                let tip_parent = commits.last().expect("successful cohort").2;
                info!(
                    parent: &entries[last_index].span,
                    git_commit_oid = %tip,
                    git_parent_oid = tip_parent.map(|id| id.to_string()).unwrap_or_default(),
                    git_publish_base_oid = base.map(|id| id.to_string()).unwrap_or_default(),
                    git_batch_commits = committed,
                    "git_ref_published"
                );
                for (index, id, parent) in commits {
                    let span = &entries[index].span;
                    span.set_attribute("git.write.commit_oid", id.to_string());
                    if let Some(parent) = parent {
                        span.set_attribute("git.write.parent_oid", parent.to_string());
                    }
                    span.set_attribute("git.write.batch_size", committed as i64);
                }
                return Ok(results);
            }
            Err(error)
                if error.code() == tonic::Code::Aborted
                    && attempt + 1 < COMMIT_FILE_MAX_ATTEMPTS =>
            {
                debug!(
                    retry_attempt = attempt + 1,
                    "CommitFile cohort reference conflict; rebuilding"
                );
                std::thread::sleep(std::time::Duration::from_millis(1_u64 << attempt.min(4)));
            }
            Err(error) => return Err(error),
        }
    }
    unreachable!("bounded commit loop returns on its last attempt")
}

fn ensure_repository_not_deleted(repo_path: &Path) -> Result<(), Status> {
    match std::fs::symlink_metadata(repo_path.with_extension("git.deleted")) {
        Ok(_) => Err(Status::failed_precondition(
            "repository UID has been permanently deleted",
        )),
        Err(e) if e.kind() == io::ErrorKind::NotFound => Ok(()),
        Err(e) => Err(Status::internal(format!("read repository tombstone: {e}"))),
    }
}

fn persist_repository_tombstone(data_root: &Path, repo_path: &Path) -> io::Result<()> {
    let parent = repo_path.parent().expect("repository fanout parent");
    std::fs::create_dir_all(parent)?;
    // The zero-byte marker lives beside, never inside, removable Git storage.
    // Sync it and the fanout directory chain before any destructive operation.
    OpenOptions::new()
        .write(true)
        .create(true)
        .truncate(false)
        .open(repo_path.with_extension("git.deleted"))?
        .sync_all()?;
    for directory in parent.ancestors() {
        File::open(directory)?.sync_all()?;
        if directory == data_root {
            break;
        }
    }
    Ok(())
}

/// Acquire an advisory lock whose ownership is tied to the open file
/// descriptor. The file itself is intentionally persistent: the operating
/// system releases the lock if a service process exits or is killed, so a
/// leftover path never wedges later writers.
fn acquire_repository_commit_lock(path: &Path, timeout: std::time::Duration) -> io::Result<File> {
    let file = OpenOptions::new()
        .read(true)
        .write(true)
        .create(true)
        .truncate(false)
        .open(path)?;
    let deadline = std::time::Instant::now() + timeout;
    let mut backoff = std::time::Duration::from_millis(1);
    loop {
        match file.try_lock() {
            Ok(()) => return Ok(file),
            Err(std::fs::TryLockError::WouldBlock) => {
                if std::time::Instant::now() >= deadline {
                    return Err(io::Error::new(
                        io::ErrorKind::TimedOut,
                        "timed out acquiring repository commit lock",
                    ));
                }
                std::thread::sleep(backoff);
                backoff = std::cmp::min(backoff * 2, std::time::Duration::from_millis(100));
            }
            Err(std::fs::TryLockError::Error(error)) => return Err(error),
        }
    }
}

fn is_reference_content_conflict(error: &gix::Error) -> bool {
    use gix::refs::file::transaction::prepare::{MustNotExist, ReferenceOutOfDate};
    error.downcast_any_ref::<ReferenceOutOfDate>().is_some()
        || error.downcast_any_ref::<MustNotExist>().is_some()
}

fn publish_delete_ref(
    repo: &gix::Repository,
    ref_name: &str,
    expected: gix::ObjectId,
    commit_id: gix::ObjectId,
    receipt_ref: Option<&str>,
) -> Result<(), Status> {
    use gix::refs::transaction::{Change, LogChange, PreviousValue, RefEdit, RefLog};
    let mut edits = Vec::with_capacity(2);
    if let Some(receipt_ref) = receipt_ref {
        edits.push(RefEdit {
            change: Change::Update {
                log: LogChange::default(),
                expected: PreviousValue::MustExistAndMatch(gix::refs::Target::Object(commit_id)),
                new: gix::refs::Target::Object(commit_id),
            },
            name: receipt_ref
                .try_into()
                .map_err(|e: gix::refs::name::Error| Status::internal(e.to_string()))?,
            deref: false,
        });
    }
    edits.push(RefEdit {
        change: Change::Update {
            log: LogChange {
                mode: RefLog::AndReference,
                force_create_reflog: false,
                message: "delete file".into(),
            },
            expected: PreviousValue::MustExistAndMatch(gix::refs::Target::Object(expected)),
            new: gix::refs::Target::Object(commit_id),
        },
        name: ref_name.try_into().map_err(|e: gix::refs::name::Error| {
            Status::invalid_argument(format!("invalid ref_name: {e}"))
        })?,
        deref: false,
    });
    let fail =
        gix::lock::acquire::Fail::AfterDurationWithBackoff(std::time::Duration::from_secs(1));
    let transaction = repo
        .refs
        .transaction()
        .prepare(edits, fail, fail)
        .map_err(|e| {
            if is_reference_content_conflict(&e) || e.is_not_found() {
                if receipt_ref.is_some() {
                    Status::failed_precondition(format!("prepared deletion was not published: {e}"))
                } else {
                    Status::aborted(format!("delete precondition changed: {e}"))
                }
            } else {
                Status::internal(format!("publish deletion: {e}"))
            }
        })?;
    let signature = gix::actor::Signature {
        name: "GitStore".into(),
        email: "gitstore@localhost".into(),
        time: gix::date::Time::now_local_or_utc(),
    };
    let mut time_buf = gix::date::parse::TimeBuf::default();
    if let Some(receipt_ref) = receipt_ref {
        // Consume permission to resume before publishing. A later force-reset to
        // the original tip must not look like a never-published prepared receipt.
        let prepared_ref = format!("{receipt_ref}-prepared");
        let prepared = repo
            .find_reference(&prepared_ref)
            .map_err(|_| Status::failed_precondition("deletion receipt is not resumable"))?;
        if prepared.target() != gix::refs::TargetRef::Object(commit_id.as_ref()) {
            return Err(Status::failed_precondition(
                "prepared deletion marker does not match receipt",
            ));
        }
        prepared
            .delete()
            .map_err(|e| Status::internal(format!("consume prepared deletion marker: {e}")))?;
        let parent = repo
            .path()
            .join(receipt_ref)
            .parent()
            .expect("receipt parent")
            .to_path_buf();
        File::open(parent)
            .and_then(|directory| directory.sync_all())
            .map_err(|e| Status::internal(format!("sync consumed deletion marker: {e}")))?;
        let packed_refs = repo.path().join("packed-refs");
        if packed_refs.exists() {
            sync_git_file(repo.path(), &packed_refs)?;
        }
    }
    transaction
        .commit(Some(signature.to_ref(&mut time_buf)))
        .map_err(|e| Status::internal(format!("publish deletion: {e}")))?;
    Ok(())
}

fn delete_receipt_ref(
    repo: &gix::Repository,
    req: &DeleteFileRequest,
    expected: gix::ObjectId,
) -> Result<String, Status> {
    let mut key = Vec::new();
    for part in [
        &req.repository_id,
        req.ref_name.as_ref().expect("guarded request"),
        &req.path,
        &expected.to_string(),
    ] {
        key.extend_from_slice(&(part.len() as u64).to_be_bytes());
        key.extend_from_slice(part.as_bytes());
    }
    let digest = repo
        .write_blob(key)
        .map_err(|e| Status::internal(format!("hash deletion key: {e}")))?;
    Ok(format!("{DELETE_RECEIPT_PREFIX}{digest}"))
}

fn sync_git_file(repo_path: &Path, path: &Path) -> Result<(), Status> {
    File::open(path)
        .and_then(|file| file.sync_all())
        .map_err(|e| Status::internal(format!("sync deletion evidence: {e}")))?;
    for directory in path.parent().expect("Git file parent").ancestors() {
        File::open(directory)
            .and_then(|file| file.sync_all())
            .map_err(|e| Status::internal(format!("sync deletion evidence directory: {e}")))?;
        if directory == repo_path {
            break;
        }
    }
    Ok(())
}

fn prepare_delete_receipt(
    repo: &gix::Repository,
    receipt_ref: &str,
    commit_id: gix::ObjectId,
    path: &str,
) -> Result<(), Status> {
    let commit = repo
        .find_object(commit_id)
        .map_err(|e| Status::internal(e.to_string()))?
        .try_into_commit()
        .map_err(|e| Status::internal(e.to_string()))?;
    let mut objects = vec![
        commit_id,
        commit
            .tree_id()
            .map_err(|e| Status::internal(e.to_string()))?
            .detach(),
    ];
    let mut tree = commit.tree().map_err(|e| Status::internal(e.to_string()))?;
    for component in path
        .split('/')
        .take(path.split('/').count().saturating_sub(1))
    {
        let Some(entry) = tree.find_entry(component) else {
            break;
        };
        if !entry.mode().is_tree() {
            break;
        }
        let id = entry.oid().to_owned();
        objects.push(id);
        tree = repo
            .find_object(id)
            .map_err(|e| Status::internal(e.to_string()))?
            .try_into_tree()
            .map_err(|e| Status::internal(e.to_string()))?;
    }
    for id in objects {
        let hex = id.to_string();
        let file = repo.path().join("objects").join(&hex[..2]).join(&hex[2..]);
        if file.exists() {
            sync_git_file(repo.path(), &file)?;
        }
    }
    // Stage and sync the immutable result BEFORE publishing the branch. A loose-ref
    // transaction can partially commit on crash; ancestry distinguishes this
    // prepared receipt from proof that the deletion actually reached the branch.
    repo.reference(
        receipt_ref,
        commit_id,
        gix::refs::transaction::PreviousValue::ExistingMustMatch(gix::refs::Target::Object(
            commit_id,
        )),
        "prepare guarded deletion",
    )
    .map_err(|e| Status::internal(format!("prepare deletion receipt: {e}")))?;
    let receipt_path = repo.path().join(receipt_ref);
    sync_git_file(repo.path(), &receipt_path)?;
    let prepared_ref = format!("{receipt_ref}-prepared");
    repo.reference(
        prepared_ref.as_str(),
        commit_id,
        gix::refs::transaction::PreviousValue::MustNotExist,
        "allow prepared deletion recovery",
    )
    .map_err(|e| Status::internal(format!("prepare deletion recovery marker: {e}")))?;
    sync_git_file(repo.path(), &repo.path().join(prepared_ref))?;
    Ok(())
}

fn resume_prepared_delete_receipt(
    repo: &gix::Repository,
    receipt_ref: &str,
    ref_name: &str,
    expected: gix::ObjectId,
    path: &str,
) -> Result<gix::ObjectId, Status> {
    let invalid = |e| Status::failed_precondition(format!("invalid prepared deletion: {e}"));
    let receipt = repo
        .find_reference(receipt_ref)
        .map_err(|e| invalid(e.to_string()))?;
    let deleted = receipt
        .target()
        .try_id()
        .map(ToOwned::to_owned)
        .ok_or_else(|| invalid("receipt must target a commit".into()))?;
    let prepared = repo
        .find_reference(&format!("{receipt_ref}-prepared"))
        .map_err(|_| invalid("receipt was published or is not safely resumable".into()))?;
    if prepared.target() != gix::refs::TargetRef::Object(deleted.as_ref()) {
        return Err(invalid("recovery marker differs from receipt".into()));
    }
    let commit = repo
        .find_object(deleted)
        .map_err(|e| invalid(e.to_string()))?
        .try_into_commit()
        .map_err(|e| invalid(e.to_string()))?;
    let mut parents = commit.parent_ids();
    if parents.next().map(|id| id.detach()) != Some(expected) || parents.next().is_some() {
        return Err(invalid(
            "receipt parent differs from expected commit".into(),
        ));
    }
    let original = repo
        .find_object(expected)
        .map_err(|e| invalid(e.to_string()))?
        .try_into_commit()
        .map_err(|e| invalid(e.to_string()))?;
    let original_tree = original
        .tree_id()
        .map_err(|e| invalid(e.to_string()))?
        .detach();
    find_blob_in_tree(repo, original_tree, path).map_err(|e| invalid(e.to_string()))?;
    let expected_tree = repo
        .edit_tree(original_tree)
        .map_err(|e| invalid(e.to_string()))?
        .remove(path)
        .map_err(|e| invalid(e.to_string()))?
        .write()
        .map_err(|e| invalid(e.to_string()))?
        .detach();
    if commit
        .tree_id()
        .map_err(|e| invalid(e.to_string()))?
        .detach()
        != expected_tree
    {
        return Err(invalid(
            "receipt tree is not exactly the requested file removal".into(),
        ));
    }
    publish_delete_ref(repo, ref_name, expected, deleted, Some(receipt_ref))?;
    sync_git_file(repo.path(), &repo.path().join(ref_name))?;
    Ok(deleted)
}

fn completed_delete_receipt(
    repo: &gix::Repository,
    receipt_ref: &str,
    tip: gix::ObjectId,
    path: &str,
) -> Result<gix::ObjectId, Status> {
    let receipt = repo
        .find_reference(receipt_ref)
        .map_err(|_| Status::aborted("no guarded deletion receipt for this request"))?;
    let deleted = receipt
        .target()
        .try_id()
        .map(ToOwned::to_owned)
        .ok_or_else(|| Status::failed_precondition("invalid guarded deletion receipt"))?;
    let mut tree = repo
        .find_object(tip)
        .map_err(|e| Status::internal(e.to_string()))?
        .try_into_commit()
        .map_err(|e| Status::internal(e.to_string()))?
        .tree()
        .map_err(|e| Status::internal(e.to_string()))?;
    let mut components = path.split('/').peekable();
    while let Some(component) = components.next() {
        let Some(entry) = tree.find_entry(component) else {
            break;
        };
        if components.peek().is_none() || !entry.mode().is_tree() {
            return Err(Status::failed_precondition(
                "deleted path has been restored or replaced",
            ));
        }
        let id = entry.oid().to_owned();
        tree = repo
            .find_object(id)
            .map_err(|e| Status::internal(e.to_string()))?
            .try_into_tree()
            .map_err(|e| Status::internal(e.to_string()))?;
    }
    let walk = repo
        .rev_walk([tip])
        .all()
        .map_err(|e| Status::internal(e.to_string()))?;
    for item in walk.take(DELETE_RECEIPT_HISTORY_LIMIT) {
        if item.map_err(|e| Status::internal(e.to_string()))?.id == deleted {
            return Ok(deleted);
        }
    }
    Err(Status::failed_precondition(
        "deletion receipt is not in bounded branch history",
    ))
}

#[derive(Debug, Default)]
struct FileHistoryProofWork {
    commits: usize,
    commit_reads: std::time::Duration,
    tree_reads: std::time::Duration,
}

fn verify_unchanged_file_history(
    repo: &gix::Repository,
    mut tip: gix::ObjectId,
    ancestor: gix::ObjectId,
    path: &str,
    blob: gix::ObjectId,
) -> Result<FileHistoryProofWork, Status> {
    let started = std::time::Instant::now();
    let mut work = FileHistoryProofWork::default();
    for _ in 0..=DELETE_RECEIPT_HISTORY_LIMIT {
        if started.elapsed() >= std::time::Duration::from_secs(1) {
            return Err(Status::resource_exhausted(
                "file history proof time budget exhausted",
            ));
        }
        let commit_started = std::time::Instant::now();
        let commit = repo
            .find_object(tip)
            .map_err(|e| Status::internal(e.to_string()))?
            .try_into_commit()
            .map_err(|e| Status::internal(e.to_string()))?;
        let tree = commit
            .tree_id()
            .map_err(|e| Status::internal(e.to_string()))?
            .detach();
        work.commits += 1;
        work.commit_reads += commit_started.elapsed();
        let tree_started = std::time::Instant::now();
        match find_blob_in_tree(repo, tree, path) {
            Ok(id) if id == blob => {}
            Ok(_) => return Err(Status::aborted("file changed since requested commit")),
            Err(error)
                if matches!(
                    error.code(),
                    tonic::Code::NotFound | tonic::Code::FailedPrecondition
                ) =>
            {
                return Err(Status::aborted(
                    "file removed or replaced since requested commit",
                ));
            }
            Err(error) => return Err(error),
        }
        work.tree_reads += tree_started.elapsed();
        if tip == ancestor {
            return Ok(work);
        }
        let mut parents = commit.parent_ids();
        let Some(parent) = parents.next() else {
            return Err(Status::aborted("requested commit is not in file history"));
        };
        if parents.next().is_some() {
            return Err(Status::aborted("file history proof does not cross merges"));
        }
        tip = parent.detach();
    }
    Err(Status::aborted(
        "file history proof commit budget exhausted",
    ))
}

/// Reject paths that could escape the repository working directory.
fn validate_file_path(path: &str) -> Result<(), Status> {
    if std::path::Path::new(path).is_absolute() {
        return Err(Status::invalid_argument(format!(
            "path '{}' must be relative",
            path
        )));
    }
    if path.split('/').any(|c| c == "..") {
        return Err(Status::invalid_argument(format!(
            "path '{}' must not contain '..'",
            path
        )));
    }
    Ok(())
}

// validate_request_authorization enforces the API's signed-in, approved
// authorization envelope. HMAC authenticates the API transport; this check
// binds that authenticated caller's declared decision to this specific RPC and
// repository without attempting to duplicate pluggable AuthZ policy in Rust.
fn validate_request_authorization(
    authorization: Option<&RequestAuthorization>,
    repository_id: &str,
    allowed_actions: &[&str],
) -> Result<(), Status> {
    let authorization = authorization
        .ok_or_else(|| Status::invalid_argument("request authorization is required"))?;
    let actor = authorization
        .actor
        .as_ref()
        .ok_or_else(|| Status::invalid_argument("request authorization actor is required"))?;
    let valid_text = |value: &str| {
        !value.is_empty() && value.len() <= 256 && !value.chars().any(char::is_control)
    };
    if !valid_text(&actor.subject)
        || !valid_text(&actor.auth_method)
        || actor.groups.len() > 256
        || actor.groups.iter().any(|group| !valid_text(group))
        || actor.service_account_uid.len() > 256
    {
        return Err(Status::invalid_argument(
            "request authorization actor is malformed",
        ));
    }
    let service_account = actor.subject.strip_prefix("system:serviceaccount:");
    let valid_identity = if actor.subject == "system:api" {
        actor.auth_method == "internal" && actor.service_account_uid.is_empty()
    } else if let Some(name) = service_account {
        valid_text(name)
            && !name.contains([':', '/'])
            && actor.auth_method == "serviceaccount-jwt"
            && !actor.service_account_uid.is_empty()
    } else if actor.subject == "anon" {
        actor.auth_method == "none" && actor.service_account_uid.is_empty()
    } else {
        !actor.subject.starts_with("system:")
            && !actor.subject.starts_with("serviceaccount:")
            && actor.service_account_uid.is_empty()
            && !matches!(
                actor.auth_method.as_str(),
                "internal" | "none" | "serviceaccount-jwt" | "serviceaccount-assertion"
            )
    };
    if !valid_identity {
        return Err(Status::permission_denied(
            "request authorization actor identity is invalid",
        ));
    }
    let definition = crate::auth::actions_generated::lookup_action(&authorization.action)
        .ok_or_else(|| Status::permission_denied("unregistered authorization action"))?;
    if definition.kind != "repository" || definition.reserved {
        return Err(Status::permission_denied(
            "authorization action is not a repository operation",
        ));
    }
    if authorization.resource_kind != "repository" || authorization.repository_id != repository_id {
        return Err(Status::permission_denied(
            "request authorization resource does not match repository",
        ));
    }
    if !allowed_actions
        .iter()
        .any(|action| *action == authorization.action)
    {
        return Err(Status::permission_denied(
            "request authorization action is not valid for this RPC",
        ));
    }
    if actor.auth_method == "none"
        && (actor.subject != "anon" || authorization.action != "repository.contents.read")
    {
        return Err(Status::permission_denied(
            "anonymous actors may only use repository.contents.read",
        ));
    }
    debug!(repo_id = %repository_id, subject = %actor.subject, action = %authorization.action, "request authorization accepted");
    Ok(())
}

/// Resolve a ref to a gix commit (returned as ObjectId so it is not bound to repo lifetime).
/// Annotated tags are peeled to their target commit before conversion.
/// Full object IDs do not require revision or prefix disambiguation.
fn resolve_ref_to_commit_id(
    repo: &gix::Repository,
    ref_str: &str,
) -> Result<gix::ObjectId, Status> {
    if ref_str.starts_with("refs/") {
        let mut reference = repo
            .try_find_reference(ref_str)
            .map_err(|e| Status::internal(format!("read ref '{ref_str}': {e}")))?
            .ok_or_else(|| Status::not_found(format!("ref '{ref_str}' not found")))?;
        let commit = reference
            .peel_to_commit()
            .map_err(|e| Status::internal(format!("peel ref '{ref_str}': {e}")))?;
        return Ok(commit.id().detach());
    }
    let object = match gix::ObjectId::from_hex(ref_str.as_bytes()) {
        Ok(id) if id.kind() == repo.object_hash() => repo
            .try_find_object(id)
            .map_err(|e| Status::internal(e.to_string()))?
            .ok_or_else(|| Status::not_found(format!("ref '{}' not found", ref_str)))?,
        _ => repo
            .rev_parse_single(ref_str.as_bytes())
            .map_err(|e| Status::not_found(format!("ref '{}' not found: {}", ref_str, e)))?
            .object()
            .map_err(|e| Status::internal(e.to_string()))?,
    };
    let commit = object
        .peel_tags_to_end()
        .map_err(|e| Status::internal(e.to_string()))?
        .try_into_commit()
        .map_err(|_| Status::internal(format!("ref '{}' is not a commit", ref_str)))?;
    Ok(commit.id().detach())
}

/// Walk the tree rooted at `commit` and collect blobs under `prefix`.
fn list_tree_files_gix(
    repo: &gix::Repository,
    commit_id: gix::ObjectId,
    prefix: &str,
    recursive: bool,
) -> Result<Vec<FileEntry>, Status> {
    let commit = repo
        .find_object(commit_id)
        .map_err(|e| Status::internal(e.to_string()))?
        .try_into_commit()
        .map_err(|_| Status::internal("not a commit"))?;

    let tree_id = commit
        .tree_id()
        .map_err(|e| Status::internal(e.to_string()))?
        .detach();

    let mut files: Vec<FileEntry> = Vec::new();
    collect_tree_entries(repo, tree_id, "", prefix, recursive, &mut files)?;
    Ok(files)
}

fn collect_tree_entries(
    repo: &gix::Repository,
    tree_id: gix::ObjectId,
    current_dir: &str,
    prefix: &str,
    recursive: bool,
    files: &mut Vec<FileEntry>,
) -> Result<(), Status> {
    let tree = repo
        .find_object(tree_id)
        .map_err(|e| Status::internal(e.to_string()))?
        .try_into_tree()
        .map_err(|_| Status::internal("not a tree"))?;

    let decoded = tree.decode().map_err(|e| Status::internal(e.to_string()))?;

    for entry in &decoded.entries {
        let name = entry.filename.to_string();
        let full_path = if current_dir.is_empty() {
            name.clone()
        } else {
            format!("{}{}", current_dir, name)
        };

        match entry.mode.kind() {
            gix::object::tree::EntryKind::Tree => {
                if recursive {
                    let subdir = format!("{}/", full_path);
                    collect_tree_entries(
                        repo,
                        entry.oid.into(),
                        &subdir,
                        prefix,
                        recursive,
                        files,
                    )?;
                } else {
                    // non-recursive: don't descend unless this dir is within prefix
                    let dir_path = format!("{}/", full_path);
                    if prefix.is_empty()
                        || dir_path.starts_with(prefix)
                        || prefix.starts_with(&dir_path)
                    {
                        let subdir = format!("{}/", full_path);
                        collect_tree_entries(
                            repo,
                            entry.oid.into(),
                            &subdir,
                            prefix,
                            false,
                            files,
                        )?;
                    }
                }
            }
            gix::object::tree::EntryKind::Blob | gix::object::tree::EntryKind::BlobExecutable
                if prefix.is_empty() || full_path.starts_with(prefix) =>
            {
                if !recursive {
                    let suffix = full_path.strip_prefix(prefix).unwrap_or(&full_path);
                    if suffix.contains('/') {
                        continue;
                    }
                }
                files.push(FileEntry {
                    path: full_path,
                    size_bytes: 0,
                    blob_sha: entry.oid.to_string(),
                });
            }
            _ => {}
        }
    }
    Ok(())
}

// --- RPC implementations -----------------------------------------------------

#[tonic::async_trait]
impl GitService for GitServiceImpl {
    async fn create_repository(
        &self,
        request: Request<CreateRepositoryRequest>,
    ) -> Result<Response<CreateRepositoryResponse>, Status> {
        self.rpc_auth
            .verify(
                request.metadata(),
                "/gitstore.git.v1.GitService/CreateRepository",
                request.get_ref(),
            )
            .await?;
        let req = request.into_inner();
        validate_request_authorization(
            req.authorization.as_ref(),
            &req.repository_id,
            &["repository.create"],
        )?;
        let repo_path = fanout_path(&self.data_root, &req.repository_id)?;
        let lock = get_or_insert_lock(&self.repo_locks, &req.repository_id);
        // A duplicate check publishes nothing. Exclude lifecycle writers, not
        // immutable readers; absent storage is rechecked under both write gates.
        let existing_guard = Arc::clone(&lock).read_owned().await;
        let (repo_path, req) = crate::spawn_blocking(move || {
            let _guard = existing_guard;
            ensure_repository_not_deleted(&repo_path)?;
            if repo_path
                .try_exists()
                .map_err(|e| Status::internal(format!("inspect repository: {e}")))?
            {
                return Err(Status::already_exists(format!(
                    "repository '{}' already exists",
                    req.repository_id
                )));
            }
            Ok((repo_path, req))
        })
        .await
        .map_err(|e| Status::internal(format!("task join error: {e}")))??;
        let guard = lock.write_owned().await;
        let snapshot = get_or_insert_snapshot_lock(&self.repo_locks, &req.repository_id);
        let snapshot_guard = snapshot.write_owned().await;

        crate::spawn_blocking(move || {
            let _guard = guard;
            let _snapshot_guard = snapshot_guard;
            ensure_repository_not_deleted(&repo_path)?;
            if repo_path.exists() {
                return Err(Status::already_exists(format!(
                    "repository '{}' already exists",
                    req.repository_id
                )));
            }

            // Ensure two-level fanout directory exists before initialising the repo
            if let Some(parent) = repo_path.parent() {
                std::fs::create_dir_all(parent).map_err(|e| {
                    Status::internal(format!("failed to create fanout directories: {}", e))
                })?;
            }

            create_repository(&repo_path)
                .map_err(|e| Status::internal(format!("failed to create repository: {}", e)))?;

            let storage_path = repo_path.to_string_lossy().into_owned();
            info!(
                repo_id = %req.repository_id,
                storage_path = %storage_path,
                "created repository"
            );

            Ok(Response::new(CreateRepositoryResponse {
                repository_id: req.repository_id,
                storage_path,
            }))
        })
        .await
        .map_err(|e| Status::internal(format!("task join error: {e}")))?
    }

    async fn delete_repository(
        &self,
        request: Request<DeleteRepositoryRequest>,
    ) -> Result<Response<DeleteRepositoryResponse>, Status> {
        self.rpc_auth
            .verify(
                request.metadata(),
                "/gitstore.git.v1.GitService/DeleteRepository",
                request.get_ref(),
            )
            .await?;
        let req = request.into_inner();
        validate_request_authorization(
            req.authorization.as_ref(),
            &req.repository_id,
            &["repository.delete"],
        )?;
        let repo_path = fanout_path(&self.data_root, &req.repository_id)?;

        let lock = get_or_insert_lock(&self.repo_locks, &req.repository_id);
        let guard = lock.write_owned().await;
        let snapshot = get_or_insert_snapshot_lock(&self.repo_locks, &req.repository_id);
        let snapshot_guard = snapshot.write_owned().await;
        let data_root = Arc::clone(&self.data_root);

        crate::spawn_blocking(move || {
            let _guard = guard;
            let _snapshot_guard = snapshot_guard;
            persist_repository_tombstone(&data_root, &repo_path)
                .map_err(|e| Status::internal(format!("persist repository tombstone: {e}")))?;
            info!(
                repo_id = %req.repository_id,
                storage_path = %repo_path.display(),
                "deleting repository"
            );

            if let Err(e) = delete_repository(&repo_path) {
                if e.downcast_ref::<io::Error>()
                    .is_none_or(|e| e.kind() != io::ErrorKind::NotFound)
                {
                    return Err(Status::internal(format!(
                        "failed to delete repository: {e}"
                    )));
                }
            }
            File::open(repo_path.parent().expect("repository fanout parent"))
                .and_then(|parent| parent.sync_all())
                .map_err(|e| Status::internal(format!("sync repository removal: {e}")))?;

            // Keep the lock entry: queued operations may still hold this Arc.
            Ok(Response::new(DeleteRepositoryResponse {
                repository_id: req.repository_id,
            }))
        })
        .await
        .map_err(|e| Status::internal(format!("task join error: {e}")))?
    }

    async fn get_file(
        &self,
        request: Request<GetFileRequest>,
    ) -> Result<Response<GetFileResponse>, Status> {
        let authentication_started = std::time::Instant::now();
        let authentication_timer = start_file_read_phase(FileReadPhase::Authentication);
        self.rpc_auth
            .verify(
                request.metadata(),
                "/gitstore.git.v1.GitService/GetFile",
                request.get_ref(),
            )
            .await?;
        drop(authentication_timer);
        let authentication_elapsed = authentication_started.elapsed();
        let req = request.into_inner();
        validate_request_authorization(
            req.authorization.as_ref(),
            &req.repository_id,
            &["repository.contents.read"],
        )?;
        let receipt_ancestor = if req.allow_ancestor_receipt {
            let ancestor = req.unchanged_since_commit.as_deref().unwrap_or("");
            if !req.r#ref.starts_with("refs/") || ancestor.len() != 40 || req.path.is_empty() {
                return Err(Status::invalid_argument(
                    "ancestor receipt requires a named ref, full ancestor commit ID and path",
                ));
            }
            validate_file_path(&req.path)?;
            Some(gix::ObjectId::from_hex(ancestor.as_bytes()).map_err(|_| {
                Status::invalid_argument("ancestor receipt requires a full ancestor commit ID")
            })?)
        } else {
            None
        };
        let repo_path = resolve_repo_path(&self.data_root, &req.repository_id)
            .map_err(file_read_target_error)?;
        let lock = get_or_insert_snapshot_lock(&self.repo_locks, &req.repository_id);
        let lock_started = std::time::Instant::now();
        let lock_timer = start_file_read_phase(FileReadPhase::RepositoryLock);
        let guard = lock.read_owned().await;
        drop(lock_timer);
        let lock_elapsed = lock_started.elapsed();

        let queue_started = std::time::Instant::now();
        let queue_timer = start_file_read_phase(FileReadPhase::BlockingQueue);
        let payload_span = tracing::Span::current();
        crate::spawn_blocking(move || {
            let _guard = guard;
            let queue_elapsed = queue_started.elapsed();
            queue_timer.observe_duration();
            let payload_started = std::time::Instant::now();
            let _payload_timer =
                super::metrics::start_file_read_phase_in_span(FileReadPhase::Payload, payload_span);
            let open_started = std::time::Instant::now();
            let repo = gix::open(&repo_path)
                .map_err(|e| Status::internal(format!("failed to open repo: {}", e)))?;
            let open_elapsed = open_started.elapsed();

            let ref_str = if req.r#ref.is_empty() {
                "HEAD".to_string()
            } else {
                req.r#ref.clone()
            };
            let revision_started = std::time::Instant::now();
            let commit_id = resolve_ref_to_commit_id(&repo, &ref_str).map_err(|error| {
                if req.unchanged_since_commit.is_some()
                    && error.code() == tonic::Code::NotFound
                    && (ref_str.starts_with("refs/") || ref_str == "HEAD")
                {
                    Status::aborted("unchanged file proof ref no longer exists")
                } else {
                    file_read_target_error(error)
                }
            })?;
            if receipt_ancestor == Some(commit_id) {
                let mut response = Response::new(GetFileResponse {
                    path: req.path,
                    unchanged_since_commit: req.unchanged_since_commit.unwrap_or_default(),
                    ref_commit_sha: commit_id.to_string(),
                    at_ancestor: true,
                    ..GetFileResponse::default()
                });
                attach_file_read_report(
                    &mut response,
                    [
                        authentication_elapsed,
                        lock_elapsed,
                        queue_elapsed,
                        payload_started.elapsed(),
                        std::time::Duration::ZERO,
                        std::time::Duration::ZERO,
                        open_elapsed,
                        revision_started.elapsed(),
                        std::time::Duration::ZERO,
                        std::time::Duration::ZERO,
                    ],
                    1,
                )?;
                return Ok(response);
            }
            let commit = repo
                .find_object(commit_id)
                .map_err(|e| Status::internal(e.to_string()))?
                .try_into_commit()
                .map_err(|_| Status::internal("not a commit"))?;

            let tree_id = commit
                .tree_id()
                .map_err(|e| Status::internal(e.to_string()))?
                .detach();
            let revision_elapsed = revision_started.elapsed();

            // Navigate to the file entry
            let target_tree_started = std::time::Instant::now();
            let entry_oid = find_blob_in_tree(&repo, tree_id, &req.path)?;
            let target_tree_elapsed = target_tree_started.elapsed();

            let history_work = if let Some(ancestor) = &req.unchanged_since_commit {
                let ancestor = gix::ObjectId::from_hex(ancestor.as_bytes()).map_err(|_| {
                    Status::invalid_argument("unchanged_since_commit must be a full object ID")
                })?;
                if commit_id == ancestor {
                    FileHistoryProofWork {
                        commits: 1,
                        ..FileHistoryProofWork::default()
                    }
                } else {
                    verify_unchanged_file_history(&repo, commit_id, ancestor, &req.path, entry_oid)?
                }
            } else {
                FileHistoryProofWork::default()
            };

            let blob_started = std::time::Instant::now();
            let blob = repo
                .find_object(entry_oid)
                .map_err(|e| Status::internal(e.to_string()))?;
            let content = blob.data.clone();
            let size_bytes = content.len() as u64;
            let blob_sha = entry_oid.to_string();
            let blob_elapsed = blob_started.elapsed();

            let mut response = Response::new(GetFileResponse {
                path: req.path,
                content,
                blob_sha,
                size_bytes,
                unchanged_since_commit: req.unchanged_since_commit.unwrap_or_default(),
                ref_commit_sha: commit_id.to_string(),
                at_ancestor: false,
            });
            attach_file_read_report(
                &mut response,
                [
                    authentication_elapsed,
                    lock_elapsed,
                    queue_elapsed,
                    payload_started.elapsed(),
                    history_work.commit_reads,
                    history_work.tree_reads,
                    open_elapsed,
                    revision_elapsed,
                    target_tree_elapsed,
                    blob_elapsed,
                ],
                history_work.commits,
            )?;
            Ok(response)
        })
        .await
        .map_err(|e| Status::internal(format!("task join error: {}", e)))?
    }

    async fn get_files(
        &self,
        request: Request<GetFilesRequest>,
    ) -> Result<Response<GetFilesResponse>, Status> {
        let started = std::time::Instant::now();
        let timer = start_file_read_phase(FileReadPhase::Authentication);
        self.rpc_auth
            .verify(
                request.metadata(),
                "/gitstore.git.v1.GitService/GetFiles",
                request.get_ref(),
            )
            .await?;
        drop(timer);
        let authentication = started.elapsed();
        let req = request.into_inner();
        validate_request_authorization(
            req.authorization.as_ref(),
            &req.repository_id,
            &["repository.contents.read"],
        )?;
        if req.paths.is_empty() || req.paths.len() > 4 {
            return Err(Status::invalid_argument(
                "GetFiles requires one to four paths",
            ));
        }
        for (i, path) in req.paths.iter().enumerate() {
            if path.is_empty() || req.paths[..i].contains(path) {
                return Err(Status::invalid_argument(
                    "GetFiles paths must be nonempty and distinct",
                ));
            }
        }
        let commit_id = gix::ObjectId::from_hex(req.commit_sha.as_bytes()).map_err(|_| {
            Status::invalid_argument("GetFiles requires a full immutable commit ID")
        })?;
        let secondary_commit_id = req
            .secondary_commit_sha
            .as_ref()
            .map(|value| {
                gix::ObjectId::from_hex(value.as_bytes()).map_err(|_| {
                    Status::invalid_argument("GetFiles requires a full second immutable commit ID")
                })
            })
            .transpose()?;
        if let Some(ref_name) = &req.current_ref {
            if secondary_commit_id.is_none() || !ref_name.starts_with("refs/") {
                return Err(Status::invalid_argument(
                    "GetFiles current ref requires a full ref and secondary commit",
                ));
            }
        }
        let repo_path = resolve_repo_path(&self.data_root, &req.repository_id)
            .map_err(file_read_target_error)?;
        let lock = get_or_insert_lock(&self.repo_locks, &req.repository_id);
        let started = std::time::Instant::now();
        let timer = start_file_read_phase(FileReadPhase::RepositoryLock);
        let guard = lock.read_owned().await;
        drop(timer);
        let repository_lock = started.elapsed();
        let queued = std::time::Instant::now();
        let queue_timer = start_file_read_phase(FileReadPhase::BlockingQueue);
        let payload_span = tracing::Span::current();
        crate::spawn_blocking(move || {
            let _guard = guard;
            let blocking_queue = queued.elapsed();
            queue_timer.observe_duration();
            let payload_started = std::time::Instant::now();
            let _payload_timer =
                super::metrics::start_file_read_phase_in_span(FileReadPhase::Payload, payload_span);
            let opened = std::time::Instant::now();
            let repo = gix::open(&repo_path)
                .map_err(|e| Status::internal(format!("failed to open repo: {e}")))?;
            let repository_open = opened.elapsed();
            let mut revision = std::time::Duration::ZERO;
            let mut target_tree = std::time::Duration::ZERO;
            let mut blob_read = std::time::Duration::ZERO;
            let mut result = GetFilesResponse {
                files: Vec::with_capacity(req.paths.len()),
                commit_sha: commit_id.to_string(),
                secondary_files: Vec::new(),
                secondary_commit_sha: secondary_commit_id
                    .map(|id| id.to_string())
                    .unwrap_or_default(),
                current_ref_commit_sha: String::new(),
            };
            for (secondary, snapshot_id) in [(false, Some(commit_id)), (true, secondary_commit_id)]
            {
                let Some(snapshot_id) = snapshot_id else {
                    continue;
                };
                let resolving = std::time::Instant::now();
                let commit = repo
                    .try_find_object(snapshot_id)
                    .map_err(|e| Status::internal(format!("GetFiles commit read failed: {e}")))?
                    .ok_or_else(|| Status::failed_precondition("GetFiles commit unavailable"))?
                    .try_into_commit()
                    .map_err(|_| Status::failed_precondition("GetFiles target is not a commit"))?;
                let tree_id = commit
                    .tree_id()
                    .map_err(|e| Status::internal(e.to_string()))?
                    .detach();
                revision += resolving.elapsed();
                for path in &req.paths {
                    let started = std::time::Instant::now();
                    let entry = find_blob_in_tree(&repo, tree_id, path);
                    target_tree += started.elapsed();
                    let file = match entry {
                        Ok(oid) => {
                            let started = std::time::Instant::now();
                            let blob = repo
                                .find_object(oid)
                                .map_err(|e| Status::internal(e.to_string()))?;
                            if blob.data.len() > 4 * 1024 * 1024 {
                                return Err(Status::resource_exhausted(
                                    "GetFiles blob exceeds 4 MiB",
                                ));
                            }
                            let file = FileSnapshot {
                                path: path.clone(),
                                found: true,
                                content: blob.data.clone(),
                                blob_sha: oid.to_string(),
                                size_bytes: blob.data.len() as u64,
                            };
                            blob_read += started.elapsed();
                            file
                        }
                        Err(error) if error.code() == tonic::Code::NotFound => FileSnapshot {
                            path: path.clone(),
                            found: false,
                            content: Vec::new(),
                            blob_sha: String::new(),
                            size_bytes: 0,
                        },
                        Err(error) => return Err(error),
                    };
                    if secondary {
                        result.secondary_files.push(file);
                    } else {
                        result.files.push(file);
                    }
                    if prost::Message::encoded_len(&result) > 16 * 1024 * 1024 {
                        return Err(Status::resource_exhausted(
                            "GetFiles response exceeds 16 MiB",
                        ));
                    }
                }
            }
            if let Some(ref_name) = &req.current_ref {
                let resolving = std::time::Instant::now();
                let current = resolve_ref_to_commit_id(&repo, ref_name).map_err(|error| {
                    if error.code() == tonic::Code::NotFound {
                        Status::aborted("GetFiles current ref was deleted")
                    } else {
                        error
                    }
                })?;
                revision += resolving.elapsed();
                if Some(current) != secondary_commit_id {
                    return Err(Status::aborted("GetFiles current ref changed"));
                }
                result.current_ref_commit_sha = current.to_string();
                if prost::Message::encoded_len(&result) > 16 * 1024 * 1024 {
                    return Err(Status::resource_exhausted(
                        "GetFiles response exceeds 16 MiB",
                    ));
                }
            }
            let mut response = Response::new(result);
            attach_file_read_report(
                &mut response,
                [
                    authentication,
                    repository_lock,
                    blocking_queue,
                    payload_started.elapsed(),
                    std::time::Duration::ZERO,
                    std::time::Duration::ZERO,
                    repository_open,
                    revision,
                    target_tree,
                    blob_read,
                ],
                0,
            )?;
            Ok(response)
        })
        .await
        .map_err(|e| Status::internal(format!("task join error: {e}")))?
    }

    type GetFileStreamStream =
        tokio_stream::wrappers::ReceiverStream<Result<GetFileStreamResponse, Status>>;

    async fn get_file_stream(
        &self,
        request: Request<GetFileStreamRequest>,
    ) -> Result<Response<Self::GetFileStreamStream>, Status> {
        self.rpc_auth
            .verify(
                request.metadata(),
                "/gitstore.git.v1.GitService/GetFileStream",
                request.get_ref(),
            )
            .await?;
        let req = request.into_inner();
        validate_request_authorization(
            req.authorization.as_ref(),
            &req.repository_id,
            &["repository.contents.read"],
        )?;
        let repo_path = resolve_repo_path(&self.data_root, &req.repository_id)
            .map_err(file_read_target_error)?;
        let lock = get_or_insert_lock(&self.repo_locks, &req.repository_id);
        let _guard = lock.read().await;

        let (tx, rx) = tokio::sync::mpsc::channel(16);

        crate::spawn_blocking(move || {
            let send = |chunk: Result<GetFileStreamResponse, Status>| {
                let _ = tx.blocking_send(chunk);
            };

            let repo = match gix::open(&repo_path) {
                Ok(r) => r,
                Err(e) => {
                    send(Err(Status::internal(format!("failed to open repo: {}", e))));
                    return;
                }
            };

            let ref_str = if req.r#ref.is_empty() {
                "HEAD".to_string()
            } else {
                req.r#ref.clone()
            };

            let commit_id = match resolve_ref_to_commit_id(&repo, &ref_str) {
                Ok(id) => id,
                Err(e) => {
                    send(Err(file_read_target_error(e)));
                    return;
                }
            };

            let obj = match repo.find_object(commit_id) {
                Ok(o) => o,
                Err(e) => {
                    send(Err(Status::internal(e.to_string())));
                    return;
                }
            };
            let commit = match obj.try_into_commit() {
                Ok(c) => c,
                Err(_) => {
                    send(Err(Status::internal("not a commit")));
                    return;
                }
            };

            let tree_id = match commit.tree_id() {
                Ok(id) => id.detach(),
                Err(e) => {
                    send(Err(Status::internal(e.to_string())));
                    return;
                }
            };

            let entry_oid = match find_blob_in_tree(&repo, tree_id, &req.path) {
                Ok(oid) => oid,
                Err(e) => {
                    send(Err(e));
                    return;
                }
            };

            let blob = match repo.find_object(entry_oid) {
                Ok(b) => b,
                Err(e) => {
                    send(Err(Status::internal(e.to_string())));
                    return;
                }
            };

            const CHUNK: usize = 256 * 1024;
            let content = blob.data.clone();
            let chunks: Vec<&[u8]> = content.chunks(CHUNK).collect();
            let last = chunks.len().saturating_sub(1);
            for (i, chunk) in chunks.into_iter().enumerate() {
                send(Ok(GetFileStreamResponse {
                    data: chunk.to_vec(),
                    chunk_index: i as u32,
                    is_last: i == last,
                }));
            }
        });

        Ok(Response::new(tokio_stream::wrappers::ReceiverStream::new(
            rx,
        )))
    }

    async fn list_files(
        &self,
        request: Request<ListFilesRequest>,
    ) -> Result<Response<ListFilesResponse>, Status> {
        self.rpc_auth
            .verify(
                request.metadata(),
                "/gitstore.git.v1.GitService/ListFiles",
                request.get_ref(),
            )
            .await?;
        let req = request.into_inner();
        validate_request_authorization(
            req.authorization.as_ref(),
            &req.repository_id,
            &["repository.contents.read"],
        )?;
        let repo_path = resolve_repo_path(&self.data_root, &req.repository_id)?;
        let lock = get_or_insert_lock(&self.repo_locks, &req.repository_id);
        let _guard = lock.read().await;

        crate::spawn_blocking(move || {
            let repo = gix::open(&repo_path)
                .map_err(|e| Status::internal(format!("failed to open repo: {}", e)))?;

            let ref_str = if req.r#ref.is_empty() {
                "HEAD".to_string()
            } else {
                req.r#ref.clone()
            };
            let commit_id = resolve_ref_to_commit_id(&repo, &ref_str)?;
            let ref_commit_sha = commit_id.to_string();

            let files = list_tree_files_gix(&repo, commit_id, &req.path_prefix, req.recursive)?;

            Ok(Response::new(ListFilesResponse {
                files,
                ref_commit_sha,
            }))
        })
        .await
        .map_err(|e| Status::internal(format!("task join error: {}", e)))?
    }

    async fn commit_file(
        &self,
        request: Request<CommitFileRequest>,
    ) -> Result<Response<CommitFileResponse>, Status> {
        self.rpc_auth
            .verify(
                request.metadata(),
                "/gitstore.git.v1.GitService/CommitFile",
                request.get_ref(),
            )
            .await?;
        let req = request.into_inner();
        validate_request_authorization(
            req.authorization.as_ref(),
            &req.repository_id,
            &["repository.contents.write"],
        )?;
        validate_file_path(&req.path)?;
        let repo_path = resolve_repo_path(&self.data_root, &req.repository_id)?;
        let lock = get_or_insert_lock(&self.repo_locks, &req.repository_id);
        let snapshot = get_or_insert_snapshot_lock(&self.repo_locks, &req.repository_id);
        let queue = self
            .repo_locks
            .entry(req.repository_id.clone())
            .or_default()
            .commits
            .clone();
        let permit = self
            .commit_slots
            .clone()
            .try_acquire_owned()
            .map_err(|_| Status::resource_exhausted("CommitFile pending capacity exhausted"))?;
        let (result, completed) = oneshot::channel();
        let _cancellation = CommitFileCancellation(queue.clone());
        let start = {
            let mut pending = queue.pending.lock().await;
            pending.entries.push_back(CommitFileEntry {
                request: req,
                span: tracing::Span::current(),
                result,
                _permit: permit,
            });
            let start = !pending.running;
            pending.running = true;
            start
        };
        if start {
            tokio::spawn(run_commit_file_queue(repo_path, lock, snapshot, queue));
        }
        completed
            .await
            .map_err(|_| Status::internal("CommitFile worker ended without a result"))?
            .map(Response::new)
    }

    async fn delete_file(
        &self,
        request: Request<DeleteFileRequest>,
    ) -> Result<Response<DeleteFileResponse>, Status> {
        self.rpc_auth
            .verify(
                request.metadata(),
                "/gitstore.git.v1.GitService/DeleteFile",
                request.get_ref(),
            )
            .await?;
        let req = request.into_inner();
        validate_request_authorization(
            req.authorization.as_ref(),
            &req.repository_id,
            &["repository.contents.write"],
        )?;
        let precondition = match (&req.ref_name, &req.expected_commit_sha) {
            (None, None) => None,
            (Some(ref_name), Some(expected)) if !ref_name.is_empty() && !expected.is_empty() => {
                if !ref_name.starts_with("refs/heads/") {
                    return Err(Status::invalid_argument(
                        "ref_name must be a fully qualified branch",
                    ));
                }
                let _: gix::refs::FullName =
                    ref_name
                        .as_str()
                        .try_into()
                        .map_err(|e: gix::refs::name::Error| {
                            Status::invalid_argument(format!("invalid ref_name: {e}"))
                        })?;
                let expected = gix::ObjectId::from_hex(expected.as_bytes()).map_err(|e| {
                    Status::invalid_argument(format!("invalid expected_commit_sha: {e}"))
                })?;
                Some(expected)
            }
            _ => {
                return Err(Status::invalid_argument(
                    "ref_name and expected_commit_sha must both be supplied and nonempty",
                ))
            }
        };
        let repo_path = fanout_path(&self.data_root, &req.repository_id)?;
        let lock = get_or_insert_lock(&self.repo_locks, &req.repository_id);
        let guard = lock.write_owned().await;
        let snapshot = get_or_insert_snapshot_lock(&self.repo_locks, &req.repository_id);
        let snapshot_guard = snapshot.write_owned().await;

        crate::spawn_blocking(move || {
            let _guard = guard;
            let _snapshot_guard = snapshot_guard;
            validate_file_path(&req.path)?;
            if precondition.is_some() {
                ensure_repository_not_deleted(&repo_path)?;
            }
            if !repo_path.exists() {
                return Err(Status::not_found(format!(
                    "repository '{}' not found",
                    req.repository_id
                )));
            }

            let repo = gix::open(&repo_path)
                .map_err(|e| Status::internal(format!("failed to open repo: {}", e)))?;

            let receipt_ref = precondition
                .map(|expected| delete_receipt_ref(&repo, &req, expected))
                .transpose()?;
            let head_commit = if let Some(expected) = precondition {
                let ref_name = req.ref_name.as_deref().expect("validated precondition");
                let has_receipt = repo
                    .try_find_reference(receipt_ref.as_deref().expect("guarded receipt"))
                    .map_err(|e| Status::internal(format!("read deletion receipt: {e}")))?
                    .is_some();
                let conflict = |message: String| {
                    if has_receipt {
                        Status::failed_precondition(message)
                    } else {
                        Status::aborted(message)
                    }
                };
                let reference = repo
                    .find_reference(ref_name)
                    .map_err(|e| conflict(format!("delete ref unavailable: {e}")))?;
                let current = reference
                    .target()
                    .try_id()
                    .map(ToOwned::to_owned)
                    .ok_or_else(|| conflict("delete ref must remain a direct commit ref".into()))?;
                if current != expected {
                    let deleted = completed_delete_receipt(
                        &repo,
                        receipt_ref.as_deref().expect("guarded receipt"),
                        current,
                        &req.path,
                    )?;
                    return Ok(Response::new(DeleteFileResponse {
                        commit_sha: deleted.to_string(),
                    }));
                }
                if has_receipt {
                    let deleted = resume_prepared_delete_receipt(
                        &repo,
                        receipt_ref.as_deref().expect("guarded receipt"),
                        ref_name,
                        expected,
                        &req.path,
                    )?;
                    return Ok(Response::new(DeleteFileResponse {
                        commit_sha: deleted.to_string(),
                    }));
                }
                repo.find_object(expected)
                    .map_err(|e| Status::internal(format!("expected commit: {e}")))?
                    .try_into_commit()
                    .map_err(|_| {
                        Status::invalid_argument("expected_commit_sha must identify a commit")
                    })?
            } else {
                repo.head_commit()
                    .map_err(|e| Status::internal(format!("head commit: {}", e)))?
            };

            // Check the file exists in the tree first
            let tree_id = head_commit
                .tree_id()
                .map_err(|e| Status::internal(e.to_string()))?
                .detach();

            find_blob_in_tree(&repo, tree_id, &req.path).map_err(|e| {
                if precondition.is_some() && e.code() == tonic::Code::NotFound {
                    Status::aborted("path is absent without a completed guarded deletion receipt")
                } else {
                    e
                }
            })?;

            let author_name = if req.author_name.is_empty() {
                "GitStore"
            } else {
                &req.author_name
            };
            let author_email = if req.author_email.is_empty() {
                "gitstore@localhost"
            } else {
                &req.author_email
            };

            let sig = gix::actor::Signature {
                name: author_name.into(),
                email: author_email.into(),
                time: gix::date::Time::now_local_or_utc(),
            };

            let new_tree = repo
                .edit_tree(tree_id)
                .map_err(|e| Status::internal(format!("edit_tree: {}", e)))?
                .remove(req.path.as_str())
                .map_err(|e| Status::internal(format!("remove: {}", e)))?
                .write()
                .map_err(|e| Status::internal(format!("tree write: {}", e)))?;

            let parent_id = head_commit.id().detach();
            let mut time_buf = gix::date::parse::TimeBuf::default();
            let sig_ref = sig.to_ref(&mut time_buf);
            let commit_id = if let Some(expected) = precondition {
                // Write immutable objects first; only the conditional ref transaction
                // publishes them, including when a push races after the initial check.
                let commit = gix::objs::Commit {
                    tree: new_tree.detach(),
                    parents: vec![parent_id].into(),
                    author: sig.clone(),
                    committer: sig.clone(),
                    encoding: None,
                    message: req.commit_message.as_str().into(),
                    extra_headers: vec![],
                };
                let id = repo
                    .write_object(&commit)
                    .map_err(|e| Status::internal(format!("write deletion commit: {e}")))?
                    .detach();
                let receipt_ref = receipt_ref.as_deref().expect("guarded receipt");
                prepare_delete_receipt(&repo, receipt_ref, id, &req.path)?;
                publish_delete_ref(
                    &repo,
                    req.ref_name.as_deref().expect("validated precondition"),
                    expected,
                    id,
                    Some(receipt_ref),
                )?;
                sync_git_file(
                    repo.path(),
                    &repo
                        .path()
                        .join(req.ref_name.as_deref().expect("guarded ref")),
                )?;
                id
            } else {
                repo.commit_as(
                    sig_ref,
                    sig_ref,
                    "HEAD",
                    &req.commit_message,
                    new_tree.detach(),
                    std::iter::once(parent_id),
                )
                .map_err(|e| Status::internal(format!("commit: {}", e)))?
                .detach()
            };

            Ok(Response::new(DeleteFileResponse {
                commit_sha: commit_id.to_string(),
            }))
        })
        .await
        .map_err(|e| Status::internal(format!("task join error: {}", e)))?
    }

    async fn create_tag(
        &self,
        request: Request<CreateTagRequest>,
    ) -> Result<Response<CreateTagResponse>, Status> {
        self.rpc_auth
            .verify(
                request.metadata(),
                "/gitstore.git.v1.GitService/CreateTag",
                request.get_ref(),
            )
            .await?;
        let req = request.into_inner();
        validate_request_authorization(
            req.authorization.as_ref(),
            &req.repository_id,
            &["repository.contents.write"],
        )?;
        let repo_path = resolve_repo_path(&self.data_root, &req.repository_id)?;
        let lock = get_or_insert_lock(&self.repo_locks, &req.repository_id);
        let guard = lock.write_owned().await;
        let snapshot = get_or_insert_snapshot_lock(&self.repo_locks, &req.repository_id);
        let snapshot_guard = snapshot.write_owned().await;

        crate::spawn_blocking(move || {
            let _guard = guard;
            let _snapshot_guard = snapshot_guard;
            let repo = gix::open(&repo_path)
                .map_err(|e| Status::internal(format!("failed to open repo: {}", e)))?;

            let target_id = if req.target_commit_sha.is_empty() {
                repo.rev_parse_single(b"HEAD".as_ref())
                    .map_err(|e| Status::not_found(format!("HEAD not found: {}", e)))?
                    .detach()
            } else {
                repo.rev_parse_single(req.target_commit_sha.as_bytes())
                    .map_err(|e| {
                        Status::not_found(format!(
                            "target '{}' not found: {}",
                            req.target_commit_sha, e
                        ))
                    })?
                    .detach()
            };

            // Check for existing tag
            let ref_name = format!("refs/tags/{}", req.tag_name);
            if repo.find_reference(&ref_name).is_ok() {
                return Err(Status::already_exists(format!(
                    "tag '{}' already exists",
                    req.tag_name
                )));
            }

            let sig = gix::actor::Signature {
                name: "GitStore".into(),
                email: "gitstore@localhost".into(),
                time: gix::date::Time::now_local_or_utc(),
            };
            let mut time_buf = gix::date::parse::TimeBuf::default();
            let sig_ref = sig.to_ref(&mut time_buf);

            let tag_ref = repo
                .tag(
                    &req.tag_name,
                    target_id,
                    gix::object::Kind::Commit,
                    Some(sig_ref),
                    &req.message,
                    gix::refs::transaction::PreviousValue::MustNotExist,
                )
                .map_err(|e| Status::internal(format!("tag: {}", e)))?;

            let tag_sha = tag_ref.id().to_string();
            Ok(Response::new(CreateTagResponse {
                tag_name: req.tag_name,
                tag_sha,
            }))
        })
        .await
        .map_err(|e| Status::internal(format!("task join error: {}", e)))?
    }

    async fn list_tags(
        &self,
        request: Request<ListTagsRequest>,
    ) -> Result<Response<ListTagsResponse>, Status> {
        self.rpc_auth
            .verify(
                request.metadata(),
                "/gitstore.git.v1.GitService/ListTags",
                request.get_ref(),
            )
            .await?;
        let req = request.into_inner();
        validate_request_authorization(
            req.authorization.as_ref(),
            &req.repository_id,
            &["repository.contents.read"],
        )?;
        let repo_path = resolve_repo_path(&self.data_root, &req.repository_id)?;
        let lock = get_or_insert_lock(&self.repo_locks, &req.repository_id);
        let _guard = lock.read().await;

        crate::spawn_blocking(move || {
            let repo = gix::open(&repo_path)
                .map_err(|e| Status::internal(format!("failed to open repo: {}", e)))?;

            let all_tags = list_tags(&repo)
                .map_err(|e| Status::internal(format!("failed to list tags: {}", e)))?;

            let tags: Vec<TagEntry> = all_tags
                .into_iter()
                .filter(|t| req.prefix.is_empty() || t.starts_with(&req.prefix))
                .filter_map(|name| {
                    let commit_sha = crate::git::repo::get_tag_commit(&repo, &name).ok()?;
                    let message = get_tag_message(&repo, &name).unwrap_or_default();
                    Some(TagEntry {
                        name,
                        commit_sha,
                        message,
                    })
                })
                .collect();

            Ok(Response::new(ListTagsResponse { tags }))
        })
        .await
        .map_err(|e| Status::internal(format!("task join error: {}", e)))?
    }

    async fn info_refs(
        &self,
        request: Request<InfoRefsRequest>,
    ) -> Result<Response<InfoRefsResponse>, Status> {
        self.rpc_auth
            .verify(
                request.metadata(),
                "/gitstore.git.v1.GitService/InfoRefs",
                request.get_ref(),
            )
            .await?;
        let req = request.into_inner();
        let expected_action = match Service::try_from(req.service).unwrap_or(Service::Unspecified) {
            Service::GitUploadPack => &["repository.contents.read"][..],
            Service::GitReceivePack => &["repository.contents.write"][..],
            Service::Unspecified => return Err(Status::invalid_argument("unknown service")),
        };
        validate_request_authorization(
            req.authorization.as_ref(),
            &req.repository_id,
            expected_action,
        )?;
        let repo_path = resolve_repo_path(&self.data_root, &req.repository_id)?;
        let lock = get_or_insert_snapshot_lock(&self.repo_locks, &req.repository_id);
        let rpc_span = tracing::Span::current();
        let snapshot_started = std::time::Instant::now();
        let guard = lock.read_owned().await;
        rpc_span.set_attribute(
            "git.advertisement.snapshot_acquisition_ms",
            snapshot_started.elapsed().as_secs_f64() * 1000.0,
        );
        let service_enum = req.service;

        let queued_at = std::time::Instant::now();
        let advertisement = crate::spawn_blocking(move || {
            let _guard = guard;
            rpc_span.set_attribute(
                "git.advertisement.blocking_queue_ms",
                queued_at.elapsed().as_secs_f64() * 1000.0,
            );
            let execution_started = std::time::Instant::now();
            let pack_server = crate::git::pack_server::HttpPackServer::new(repo_path, 0);
            let svc = proto::Service::try_from(service_enum).unwrap_or(proto::Service::Unspecified);
            let result = match svc {
                proto::Service::GitUploadPack => pack_server
                    .advertise_upload_pack_refs()
                    .map_err(|e| Status::internal(format!("upload-pack advertise: {}", e))),
                proto::Service::GitReceivePack => pack_server
                    .advertise_receive_pack_refs()
                    .map_err(|e| Status::internal(format!("receive-pack advertise: {}", e))),
                _ => Err(Status::invalid_argument("unknown service")),
            };
            rpc_span.set_attribute(
                "git.advertisement.execution_ms",
                execution_started.elapsed().as_secs_f64() * 1000.0,
            );
            result
        })
        .await
        .map_err(|e| Status::internal(format!("task join: {}", e)))??;

        info!(
            repo_id = %req.repository_id,
            service = service_enum,
            "info_refs complete"
        );

        Ok(Response::new(InfoRefsResponse {
            advertisement,
            service: service_enum,
        }))
    }

    async fn receive_pack(
        &self,
        request: Request<tonic::Streaming<ReceivePackRequest>>,
    ) -> Result<Response<ReceivePackResponse>, Status> {
        let metadata = request.metadata().clone();
        let mut stream = request.into_inner();

        // First chunk carries repo_id + ref_commands + optional initial pack_data
        let first = stream
            .message()
            .await
            .map_err(|e| Status::internal(format!("recv first chunk: {}", e)))?
            .ok_or_else(|| Status::invalid_argument("empty stream"))?;

        self.rpc_auth
            .verify(&metadata, "/gitstore.git.v1.GitService/ReceivePack", &first)
            .await?;
        let repo_id = first.repository_id.clone();

        // T040: Validate push_context presence and repo_id consistency (FR-011).
        let push_ctx = first.push_context.as_ref().ok_or_else(|| {
            Status::invalid_argument("push_context is required on the first chunk")
        })?;
        if push_ctx.repository_id != repo_id {
            return Err(Status::invalid_argument(format!(
                "push_context.repository_id ({}) does not match chunk.repository_id ({})",
                push_ctx.repository_id, repo_id
            )));
        }
        validate_request_authorization(
            push_ctx.authorization.as_ref(),
            &repo_id,
            &["repository.contents.write"],
        )?;
        record_push_ref_snapshot(&first.ref_commands);

        // T041: Extract pack size limit before any pack I/O. Effective limit
        // = min(repository policy, ceiling); a 0/absent repository policy
        // means the ceiling itself (no more "0 = unlimited").
        let max_pack_size_bytes: i64 = self.push_limits.effective_pack_limit(
            push_ctx
                .policy
                .as_ref()
                .map(|p| p.max_pack_size_bytes)
                .unwrap_or(0),
        );

        // T051: Build HookContext from validated PushContext.
        let hook_ctx = crate::git::hooks::HookContext::from(push_ctx);

        let repo_path = resolve_repo_path(&self.data_root, &repo_id)?;
        let lock = get_or_insert_lock(&self.repo_locks, &repo_id);
        // Git may send a flush-only request after rejecting every local update.
        let guard = if first.ref_commands.is_empty() {
            ReceivePackRepositoryGuard::Shared {
                _guard: lock.read_owned().await,
            }
        } else {
            ReceivePackRepositoryGuard::Exclusive {
                _guard: lock.write_owned().await,
            }
        };

        info!(repo_id = %repo_id, "receive_pack: stream start");

        let guard = if first.shallow_oids.is_empty() {
            guard
        } else {
            let shallow_oids = first.shallow_oids.clone();
            let repo_path_shallow = repo_path.clone();
            crate::spawn_blocking(move || {
                if shallow_oids.len() > 4096 {
                    return Err(Status::resource_exhausted(
                        "shallow boundaries exceed 4096 commits",
                    ));
                }
                let repo = gix::open(&repo_path_shallow)
                    .map_err(|e| Status::internal(format!("open repo for shallow check: {e}")))?;
                match std::fs::metadata(repo.git_dir().join("shallow")) {
                    Ok(metadata) if metadata.len() > 0 => {
                        return Err(Status::failed_precondition(
                            "shallow server repositories are unsupported",
                        ));
                    }
                    Ok(_) => {}
                    Err(e) if e.kind() == io::ErrorKind::NotFound => {}
                    Err(e) => {
                        return Err(Status::internal(format!("read shallow boundary: {e}")));
                    }
                }
                let mut seen = std::collections::HashSet::new();
                for value in shallow_oids {
                    if value.len() != 40
                        || value
                            .bytes()
                            .any(|b| !b.is_ascii_digit() && !(b'a'..=b'f').contains(&b))
                    {
                        return Err(Status::invalid_argument(
                            "shallow commit must be 40 lowercase hex characters",
                        ));
                    }
                    let oid = gix::ObjectId::from_hex(value.as_bytes()).map_err(|e| {
                        Status::invalid_argument(format!("parse shallow commit: {e}"))
                    })?;
                    if oid.is_null() || !seen.insert(oid) {
                        return Err(Status::invalid_argument(
                            "shallow commits must be distinct and nonzero",
                        ));
                    }
                    repo.try_find_object(oid)
                        .map_err(|e| Status::internal(format!("read shallow commit: {e}")))?
                        .ok_or_else(|| {
                            Status::failed_precondition(
                                "client shallow boundary is absent from the live repository",
                            )
                        })?
                        .try_into_commit()
                        .map_err(|_| Status::invalid_argument("shallow boundary is not a commit"))?
                        .decode()
                        .map_err(|e| Status::internal(format!("decode shallow commit: {e}")))?;
                }
                Ok::<_, Status>(guard)
            })
            .await
            .map_err(|e| Status::internal(format!("shallow check join: {e}")))??
        };

        let ref_commands = first.ref_commands.clone();
        if ref_commands.iter().any(|command| {
            command.ref_name == "refs/gitstore" || command.ref_name.starts_with("refs/gitstore/")
        }) {
            let refs: Vec<&str> = ref_commands
                .iter()
                .map(|command| command.ref_name.as_str())
                .collect();
            return Ok(Response::new(ReceivePackResponse {
                report_status: build_rejection_status(
                    &refs,
                    "private GitStore operation refs cannot be pushed",
                ),
            }));
        }
        let initial_pack = first.pack_data.clone();
        let is_last = first.is_last;

        // Branch-delete pushes carry no objects; skip pack staging entirely.
        let is_delete_only = ref_commands
            .iter()
            .all(|c| c.new_oid == "0000000000000000000000000000000000000000");

        let quarantine: Option<crate::git::pack_server::Quarantine> = if is_delete_only {
            info!(repo_id = %repo_id, "receive_pack: branch deletion — skipping pack staging");
            None
        } else {
            // T041: Check initial pack bytes against limit before sending to channel.
            if max_pack_size_bytes > 0 && initial_pack.len() as i64 > max_pack_size_bytes {
                return Err(Status::resource_exhausted(format!(
                    "pack size exceeds limit of {} bytes",
                    max_pack_size_bytes
                )));
            }

            // Channel bridge: tokio async stream → sync Read
            let (tx, rx) = std::sync::mpsc::sync_channel::<Vec<u8>>(16);

            if !initial_pack.is_empty() {
                tx.send(initial_pack)
                    .map_err(|_| Status::internal("channel send failed"))?;
            }

            if !is_last {
                let tx2 = tx.clone();
                tokio::spawn(async move {
                    let mut idx = 1u32;
                    let mut total_bytes: i64 = 0;
                    while let Ok(Some(chunk)) = stream.message().await {
                        let bytes = chunk.pack_data.len();
                        total_bytes += bytes as i64;
                        if max_pack_size_bytes > 0 && total_bytes > max_pack_size_bytes {
                            // Send the sentinel string so LimitedReader's map_err closure
                            // can surface RESOURCE_EXHAUSTED instead of INTERNAL.
                            let _ = tx2.send(b"pack size limit exceeded (streaming)".to_vec());
                            break;
                        }
                        if !chunk.pack_data.is_empty() && tx2.send(chunk.pack_data).is_err() {
                            break;
                        }
                        info!(chunk_index = idx, bytes, "receive_pack: chunk received");
                        idx += 1;
                        if chunk.is_last {
                            break;
                        }
                    }
                    drop(tx2);
                });
            }
            drop(tx);

            // Open repo now to obtain ODB handle for thin pack resolution.
            let repo_for_odb =
                gix::open(&repo_path).map_err(|e| Status::internal(format!("open repo: {}", e)))?;
            let odb = (*repo_for_odb.objects).clone();

            // T041: Use a LimitedReader that enforces max_pack_size_bytes.
            let q = crate::spawn_blocking(move || {
                let reader = crate::git::pack_server::ChannelReader::new(rx);
                if max_pack_size_bytes > 0 {
                    let limited = crate::git::pack_server::LimitedReader::new(
                        reader,
                        max_pack_size_bytes as u64,
                    );
                    crate::git::pack_server::stage_pack_from_reader(limited, Some(odb))
                } else {
                    crate::git::pack_server::stage_pack_from_reader(reader, Some(odb))
                }
            })
            .await
            .map_err(|e| Status::internal(format!("spawn_blocking join: {}", e)))?
            .map_err(|e| {
                let msg = e.to_string();
                if msg.contains("pack size limit exceeded") {
                    Status::resource_exhausted(msg)
                } else {
                    Status::internal(format!("stage pack: {}", e))
                }
            })?;

            info!(repo_id = %repo_id, "receive_pack: pack staged in quarantine");
            Some(q)
        };

        // T042: Enforce blob size limit on quarantined objects before any ref
        // update. Effective limit = min(repository policy, ceiling); a
        // 0/absent repository policy means the ceiling itself.
        let max_file_size_bytes: i64 = self.push_limits.effective_file_limit(
            first
                .push_context
                .as_ref()
                .and_then(|ctx| ctx.policy.as_ref())
                .map(|p| p.max_file_size_bytes)
                .unwrap_or(0),
        );
        if max_file_size_bytes > 0 {
            if let Some(ref q) = quarantine {
                let pack_path = q.pack_path.clone();
                let index_path = q.index_path.clone();
                let limit = max_file_size_bytes as u64;
                crate::spawn_blocking(move || {
                    crate::git::pack_server::check_blob_sizes_in_quarantine_paths(
                        &pack_path,
                        &index_path,
                        limit,
                    )
                })
                .await
                .map_err(|e| Status::internal(format!("blob check join: {}", e)))?
                .map_err(|e| Status::resource_exhausted(e.to_string()))?;
            }
        }

        // Build ref_updates from ref_commands
        let ref_updates: Vec<RefUpdate> = ref_commands
            .iter()
            .map(|c| RefUpdate {
                ref_name: c.ref_name.clone(),
                old_oid: c.old_oid.clone(),
                new_oid: c.new_oid.clone(),
            })
            .collect();

        // Non-fast-forward validation in blocking context (gix::Repository is !Send).
        let ref_updates_clone = ref_updates.clone();
        let repo_path_nff = repo_path.clone();
        let rpc_span = tracing::Span::current();
        let (nff_rejected, pipeline_updates) = crate::spawn_blocking(move || {
            let repo = gix::open(&repo_path_nff)
                .map_err(|e| Status::internal(format!("open repo for nff: {}", e)))?;
            let mut rejected = Vec::new();
            let mut valid = Vec::new();
            for (index, update) in ref_updates_clone.iter().enumerate() {
                let is_non_ff = validate_ref_command(
                    &repo,
                    &update.ref_name,
                    &update.old_oid,
                    (index == 0).then_some(&rpc_span),
                )
                .map_err(|e| Status::internal(e.to_string()))?;
                if is_non_ff {
                    rejected.push(update.ref_name.clone());
                } else {
                    valid.push(update.clone());
                }
            }
            Ok::<_, Status>((rejected, valid))
        })
        .await
        .map_err(|e| Status::internal(format!("nff join: {}", e)))??;

        // Run hook pipeline async (pre-receive → proc-receive → update).
        // Pass quarantine dir so blob extraction can see pushed objects before
        // promote_quarantine runs.
        let pipeline = Arc::clone(&self.hook_pipeline);
        let repo_path_pipeline = repo_path.clone();
        let quarantine_path = quarantine.as_ref().map(|q| q.dir.path().to_path_buf());
        let accepted_indices = match pipeline
            .run(
                &repo_path_pipeline,
                &pipeline_updates,
                quarantine_path.as_deref(),
                &hook_ctx,
            )
            .await
        {
            Ok(indices) => indices,
            Err(rejection) => {
                let all_refs: Vec<&str> = ref_updates.iter().map(|u| u.ref_name.as_str()).collect();
                let report_status = build_rejection_status(&all_refs, &rejection.reason);
                return Ok(Response::new(ReceivePackResponse { report_status }));
            }
        };

        let accepted_updates: Vec<RefUpdate> = accepted_indices
            .iter()
            .map(|i| pipeline_updates[*i].clone())
            .collect();

        // Build ref_edits, prepare the gix transaction (acquires lock files), then
        // commit. Runs in spawn_blocking because gix::Repository is !Send.
        let pipeline_updates_for_txn = pipeline_updates.clone();
        let accepted_indices_clone = accepted_indices.clone();
        let accepted_updates_for_callbacks = accepted_updates.clone();
        let repo_path_commit = repo_path.clone();
        let hook_ctx_post = hook_ctx.clone();
        let repo_id_commit = repo_id.clone();
        // Snapshot readers exclude publication and deletion, not pack staging
        // or validation. The operation guard still serializes complete pushes.
        let snapshot_guard = if accepted_indices.is_empty() {
            None
        } else {
            Some(
                get_or_insert_snapshot_lock(&self.repo_locks, &repo_id)
                    .write_owned()
                    .await,
            )
        };
        crate::spawn_blocking(move || {
            let _guard = guard;
            use gix::refs::transaction::{Change, LogChange, PreviousValue, RefEdit, RefLog};

            if accepted_indices_clone.is_empty() {
                return Ok(());
            }
            let repo = gix::open(&repo_path_commit)
                .map_err(|e| Status::internal(format!("open repo for commit: {}", e)))?;

            let mut ref_edits: Vec<RefEdit> = Vec::new();
            for i in &accepted_indices_clone {
                let u = &pipeline_updates_for_txn[*i];
                let new_id = gix::ObjectId::from_hex(u.new_oid.as_bytes())
                    .map_err(|e| Status::invalid_argument(format!("parse new oid: {}", e)))?;
                let old_id = gix::ObjectId::from_hex(u.old_oid.as_bytes())
                    .map_err(|e| Status::invalid_argument(format!("parse old oid: {}", e)))?;

                let change = if new_id.is_null() {
                    let expected = if old_id.is_null() {
                        PreviousValue::Any
                    } else {
                        PreviousValue::MustExistAndMatch(gix::refs::Target::Object(old_id))
                    };
                    Change::Delete {
                        expected,
                        log: RefLog::AndReference,
                    }
                } else {
                    let previous_value = if old_id.is_null() {
                        PreviousValue::MustNotExist
                    } else {
                        PreviousValue::MustExistAndMatch(gix::refs::Target::Object(old_id))
                    };
                    Change::Update {
                        log: LogChange {
                            mode: RefLog::AndReference,
                            force_create_reflog: false,
                            message: "push".into(),
                        },
                        expected: previous_value,
                        new: gix::refs::Target::Object(new_id),
                    }
                };
                ref_edits.push(RefEdit {
                    change,
                    name: u
                        .ref_name
                        .as_str()
                        .try_into()
                        .map_err(|e: gix::refs::name::Error| {
                            Status::invalid_argument(format!("parse refname: {}", e))
                        })?,
                    deref: false,
                });
            }

            if !ref_edits.is_empty() {
                let file_lock_fail = gix::lock::acquire::Fail::AfterDurationWithBackoff(
                    std::time::Duration::from_millis(100),
                );
                let packed_lock_fail = gix::lock::acquire::Fail::AfterDurationWithBackoff(
                    std::time::Duration::from_millis(1000),
                );
                // Ref lock files are acquired here — this is the "prepared" state.
                let txn = repo
                    .refs
                    .transaction()
                    .prepare(ref_edits, file_lock_fail, packed_lock_fail)
                    .map_err(|e| Status::internal(format!("prepare ref transaction: {}", e)))?;

                if let Some(q) = quarantine {
                    crate::git::pack_server::promote_quarantine(&repo, q)
                        .map_err(|e| Status::internal(format!("promote quarantine: {}", e)))?;
                }
                txn.commit(None)
                    .map_err(|e| Status::internal(format!("commit ref transaction: {}", e)))?;
            }
            drop(snapshot_guard);
            info!(repo_id = %repo_id_commit, "receive_pack: refs committed");
            pipeline.run_post_receive(
                &repo_path_commit,
                &accepted_updates_for_callbacks,
                &repo_id_commit,
                &hook_ctx_post,
            );
            Ok::<(), Status>(())
        })
        .await
        .map_err(|e| Status::internal(format!("commit join: {}", e)))??;

        let report_status = build_report_status(
            &ref_updates,
            &pipeline_updates,
            &accepted_indices,
            &nff_rejected,
        );

        info!(
            repo_id = %repo_id,
            refs_accepted = accepted_indices.len(),
            refs_rejected = nff_rejected.len(),
            "receive_pack: complete"
        );

        Ok(Response::new(ReceivePackResponse { report_status }))
    }

    type UploadPackStream = CancellableUploadPackStream;

    async fn upload_pack(
        &self,
        request: Request<UploadPackRequest>,
    ) -> Result<Response<Self::UploadPackStream>, Status> {
        self.rpc_auth
            .verify(
                request.metadata(),
                "/gitstore.git.v1.GitService/UploadPack",
                request.get_ref(),
            )
            .await?;
        let req = request.into_inner();
        validate_request_authorization(
            req.authorization.as_ref(),
            &req.repository_id,
            &["repository.contents.read"],
        )?;
        let repo_path = resolve_repo_path(&self.data_root, &req.repository_id)?;
        let lock = get_or_insert_lock(&self.repo_locks, &req.repository_id);
        let guard = lock.read_owned().await;

        info!(repo_id = %req.repository_id, "upload_pack: start");

        let (tx, rx) = tokio::sync::mpsc::channel(16);
        let max_pack_size = self.push_limits.max_pack_size;
        let interrupt = Arc::new(AtomicBool::new(false));
        let stream = CancellableUploadPackStream {
            inner: tokio_stream::wrappers::ReceiverStream::new(rx),
            interrupt: interrupt.clone(),
        };

        crate::spawn_blocking(move || {
            let pack_server =
                crate::git::pack_server::HttpPackServer::new(repo_path, max_pack_size);
            let result = pack_server.handle_upload_pack_with_interrupt(&req.body, &interrupt);
            drop(guard);
            match result {
                Ok(pack_bytes) => {
                    const CHUNK_SIZE: usize = 64 * 1024;
                    let num_chunks = pack_bytes.len().div_ceil(CHUNK_SIZE);
                    for (i, chunk) in pack_bytes.chunks(CHUNK_SIZE).enumerate() {
                        let is_last = i + 1 == num_chunks;
                        let msg = Ok(UploadPackResponse {
                            chunk_index: i as u32,
                            data: chunk.to_vec(),
                            is_last,
                        });
                        if tx.blocking_send(msg).is_err() {
                            info!(repo_id = %req.repository_id, "upload_pack: consumer cancelled");
                            return;
                        }
                        info!(
                            chunk_index = i,
                            bytes = chunk.len(),
                            "upload_pack: chunk sent"
                        );
                    }
                    if pack_bytes.is_empty() {
                        let _ = tx.blocking_send(Ok(UploadPackResponse {
                            chunk_index: 0,
                            data: vec![],
                            is_last: true,
                        }));
                    }
                }
                Err(e) if interrupt.load(Ordering::Acquire) => {
                    info!(repo_id = %req.repository_id, error = %e, "upload_pack: generation cancelled");
                    let _ = tx.blocking_send(Err(Status::cancelled("upload-pack cancelled")));
                }
                Err(e) => {
                    error!(error = %e, "upload_pack error");
                    let _ = tx.blocking_send(Err(Status::internal(format!("upload-pack: {}", e))));
                }
            }
        });

        Ok(Response::new(stream))
    }

    async fn get_latest_tag(
        &self,
        request: Request<GetLatestTagRequest>,
    ) -> Result<Response<GetLatestTagResponse>, Status> {
        self.rpc_auth
            .verify(
                request.metadata(),
                "/gitstore.git.v1.GitService/GetLatestTag",
                request.get_ref(),
            )
            .await?;
        let req = request.into_inner();
        validate_request_authorization(
            req.authorization.as_ref(),
            &req.repository_id,
            &["repository.contents.read"],
        )?;
        let repo_path = resolve_repo_path(&self.data_root, &req.repository_id)?;
        let lock = get_or_insert_lock(&self.repo_locks, &req.repository_id);
        let _guard = lock.read().await;

        crate::spawn_blocking(move || {
            let repo = gix::open(&repo_path)
                .map_err(|e| Status::internal(format!("failed to open repo: {}", e)))?;

            let all_tags = list_tags(&repo)
                .map_err(|e| Status::internal(format!("failed to list tags: {}", e)))?;

            let prefix = if req.prefix.is_empty() {
                "v"
            } else {
                &req.prefix
            };

            let mut release_tags: Vec<String> = all_tags
                .into_iter()
                .filter(|t| t.starts_with(prefix))
                .collect();

            if release_tags.is_empty() {
                return Ok(Response::new(GetLatestTagResponse {
                    tag: None,
                    found: false,
                }));
            }

            release_tags.sort_by(|a, b| {
                let av = a.trim_start_matches(prefix);
                let bv = b.trim_start_matches(prefix);
                cmp_semver_str(av, bv)
            });

            let latest_name = release_tags.last().unwrap().clone();
            let commit_sha = crate::git::repo::get_tag_commit(&repo, &latest_name)
                .map_err(|e| Status::internal(format!("failed to get tag commit: {}", e)))?;
            let message = get_tag_message(&repo, &latest_name).unwrap_or_default();

            Ok(Response::new(GetLatestTagResponse {
                tag: Some(TagEntry {
                    name: latest_name,
                    commit_sha,
                    message,
                }),
                found: true,
            }))
        })
        .await
        .map_err(|e| Status::internal(format!("task join error: {}", e)))?
    }
}

// --- free functions ----------------------------------------------------------

/// Validate a single ref command's old_oid against the current ref tip.
/// Returns true if the command is a non-fast-forward (should be rejected).
fn record_push_ref_snapshot(commands: &[RefCommand]) {
    let span = tracing::Span::current();
    span.set_attribute("git.push.ref_command_count", commands.len() as i64);
    if let Some(command) = commands.first() {
        let old = gix::ObjectId::from_hex(command.old_oid.as_bytes());
        let new = gix::ObjectId::from_hex(command.new_oid.as_bytes());
        span.set_attribute("git.push.first_oid_valid", old.is_ok() && new.is_ok());
        if let (Ok(old), Ok(new)) = (old, new) {
            span.set_attribute("git.push.old_oid", old.to_string());
            span.set_attribute("git.push.new_oid", new.to_string());
        }
    }
}

fn validate_ref_command(
    repo: &gix::Repository,
    ref_name: &str,
    old_oid: &str,
    snapshot_span: Option<&tracing::Span>,
) -> Result<bool, Status> {
    let zero = "0000000000000000000000000000000000000000";
    if old_oid == zero {
        if let Some(span) = snapshot_span {
            span.set_attribute("git.push.current_lookup_attempted", false);
        }
        return Ok(false);
    }
    if let Some(span) = snapshot_span {
        span.set_attribute("git.push.current_lookup_attempted", true);
    }
    match repo.find_reference(ref_name) {
        Ok(r) => {
            let current_oid = r
                .target()
                .try_id()
                .map(|id| id.to_string())
                .unwrap_or_default();
            if let Some(span) = snapshot_span {
                span.set_attribute("git.push.current_lookup_succeeded", true);
                span.set_attribute("git.push.current_target_is_oid", !current_oid.is_empty());
                if !current_oid.is_empty() {
                    span.set_attribute("git.push.current_oid", current_oid.clone());
                }
            }
            if current_oid != old_oid {
                return Ok(true);
            }
            Ok(false)
        }
        Err(_) => {
            if let Some(span) = snapshot_span {
                span.set_attribute("git.push.current_lookup_succeeded", false);
            }
            Ok(false)
        }
    }
}

/// Build a report-status payload where every ref is rejected with the same reason.
fn build_rejection_status(all_ref_names: &[&str], reason: &str) -> Vec<u8> {
    let mut inner = Vec::new();
    let _ = write_pkt_line_buf(&mut inner, b"unpack ok\n");
    for ref_name in all_ref_names {
        let _ = write_pkt_line_buf(&mut inner, format!("ng {ref_name} {reason}\n").as_bytes());
    }
    inner.extend_from_slice(b"0000");
    let mut sideband_data = vec![0x01u8];
    sideband_data.extend_from_slice(&inner);
    let mut response = Vec::new();
    let _ = write_pkt_line_buf(&mut response, &sideband_data);
    response.extend_from_slice(b"0000");
    response
}

/// Build a report-status pkt-line payload.
///
/// `all_updates` — original full list of requested ref updates (for reporting).
/// `pipeline_updates` — subset that passed nff validation (passed to hook pipeline).
/// `accepted_indices` — indices into `pipeline_updates` that were accepted.
/// `nff_rejected` — ref names rejected as non-fast-forward before the pipeline.
fn build_report_status(
    all_updates: &[RefUpdate],
    pipeline_updates: &[RefUpdate],
    accepted_indices: &[usize],
    nff_rejected: &[String],
) -> Vec<u8> {
    use std::collections::HashSet;
    let accepted_names: HashSet<&str> = accepted_indices
        .iter()
        .map(|i| pipeline_updates[*i].ref_name.as_str())
        .collect();
    let nff_set: HashSet<&str> = nff_rejected.iter().map(|s| s.as_str()).collect();

    let mut inner = Vec::new();
    let _ = write_pkt_line_buf(&mut inner, b"unpack ok\n");
    for u in all_updates {
        if nff_set.contains(u.ref_name.as_str()) {
            let _ = write_pkt_line_buf(
                &mut inner,
                format!("ng {} non-fast-forward\n", u.ref_name).as_bytes(),
            );
        } else if accepted_names.contains(u.ref_name.as_str()) {
            let _ = write_pkt_line_buf(&mut inner, format!("ok {}\n", u.ref_name).as_bytes());
        } else {
            let _ = write_pkt_line_buf(
                &mut inner,
                format!("ng {} rejected by update hook\n", u.ref_name).as_bytes(),
            );
        }
    }
    inner.extend_from_slice(b"0000");

    let mut sideband_data = vec![0x01u8];
    sideband_data.extend_from_slice(&inner);

    let mut response = Vec::new();
    let _ = write_pkt_line_buf(&mut response, &sideband_data);
    response.extend_from_slice(b"0000");
    response
}

fn write_pkt_line_buf(out: &mut Vec<u8>, data: &[u8]) -> Result<(), ()> {
    let len = data.len() + 4;
    if len > 65516 {
        return Err(());
    }
    let hex = format!("{:04x}", len);
    out.extend_from_slice(hex.as_bytes());
    out.extend_from_slice(data);
    Ok(())
}

/// Compare two semver version strings (e.g. "1.2.3" vs "1.10.0").
fn cmp_semver_str(a: &str, b: &str) -> std::cmp::Ordering {
    let parse = |s: &str| -> (u64, u64, u64) {
        let mut parts = s.splitn(3, '.').map(|p| p.parse::<u64>().unwrap_or(0));
        (
            parts.next().unwrap_or(0),
            parts.next().unwrap_or(0),
            parts.next().unwrap_or(0),
        )
    };
    parse(a).cmp(&parse(b))
}

/// Returns the annotated tag message, or empty string for lightweight tags.
fn get_tag_message(repo: &gix::Repository, tag_name: &str) -> Option<String> {
    let mut reference = repo
        .find_reference(&format!("refs/tags/{}", tag_name))
        .ok()?;
    let tag = reference.peel_to_tag().ok()?;
    let decoded = tag.decode().ok()?;
    Some(decoded.message.to_string())
}

/// Walk a tree to find a blob at `path` (slash-separated).
fn find_blob_in_tree(
    repo: &gix::Repository,
    tree_id: gix::ObjectId,
    path: &str,
) -> Result<gix::ObjectId, Status> {
    let tree = repo
        .find_object(tree_id)
        .map_err(|e| Status::internal(e.to_string()))?
        .try_into_tree()
        .map_err(|_| Status::internal("expected a tree object"))?;

    let mut parts = path.splitn(2, '/');
    let first = parts.next().unwrap_or("");
    let rest = parts.next();

    let mut found = None;
    // Consume the complete tree so a matching prefix cannot hide corrupt trailing entries.
    for entry in tree.iter() {
        let entry = entry.map_err(|e| Status::internal(e.to_string()))?;
        if found.is_none() && entry.filename() == gix::bstr::BStr::new(first.as_bytes()) {
            found = Some((entry.oid().to_owned(), entry.mode()));
        }
    }
    let (oid, mode) =
        found.ok_or_else(|| Status::not_found(format!("file '{}' not found", path)))?;
    match rest {
        Some(remaining) => find_blob_in_tree(repo, oid, remaining),
        None => {
            use gix::object::tree::EntryKind;
            match mode.kind() {
                EntryKind::Blob | EntryKind::BlobExecutable => Ok(oid),
                _ => Err(Status::failed_precondition(format!(
                    "'{}' is not a file",
                    path
                ))),
            }
        }
    }
}

/// Verify the data_root is accessible at startup.
pub fn verify_data_root(data_root: &Path) -> anyhow::Result<()> {
    if !data_root.exists() {
        std::fs::create_dir_all(data_root)?;
    }
    Ok(())
}

// ---------------------------------------------------------------------------
#[cfg(test)]
mod tests {
    use super::*;
    use tempfile::TempDir;

    fn signed_test_request<T: prost::Message>(message: T) -> Request<T> {
        let name = std::any::type_name::<T>()
            .rsplit("::")
            .next()
            .unwrap()
            .trim_end_matches("Request");
        crate::auth::interceptor::sign_request(
            message,
            "fixture-key",
            "gitstore-git",
            &format!("/gitstore.git.v1.GitService/{name}"),
        )
        .unwrap()
    }

    fn signed_test_stream(
        chunks: Vec<ReceivePackRequest>,
    ) -> Request<impl tokio_stream::Stream<Item = ReceivePackRequest>> {
        let signed = signed_test_request(chunks[0].clone());
        let mut request = Request::new(tokio_stream::iter(chunks));
        *request.metadata_mut() = signed.metadata().clone();
        request
    }

    fn make_test_service(data_root: &std::path::Path) -> GitServiceImpl {
        GitServiceImpl::new(
            data_root.to_path_buf(),
            RPCVerifier::new("fixture-key", None, data_root).unwrap(),
        )
    }

    fn make_test_service_with_push_limits(
        data_root: &std::path::Path,
        push_limits: PushLimits,
    ) -> GitServiceImpl {
        GitServiceImpl::with_pipeline(
            data_root.to_path_buf(),
            Arc::new(HookPipeline::new(
                std::time::Duration::from_secs(10),
                Arc::new(NoopValidationHandler),
                Arc::new(NoopAdmissionHandler),
            )),
            push_limits,
            RPCVerifier::new("fixture-key", None, data_root).unwrap(),
        )
    }

    async fn make_grpc_client_with_push_limits(
        data_root: std::path::PathBuf,
        push_limits: PushLimits,
    ) -> proto::git_service_client::GitServiceClient<tonic::transport::Channel> {
        let svc = make_test_service_with_push_limits(&data_root, push_limits);
        make_grpc_client_for_service(svc).await
    }

    async fn make_grpc_client_for_service(
        svc: GitServiceImpl,
    ) -> proto::git_service_client::GitServiceClient<tonic::transport::Channel> {
        use proto::git_service_server::GitServiceServer;

        let listener = tokio::net::TcpListener::bind("127.0.0.1:0").await.unwrap();
        let addr = listener.local_addr().unwrap();
        let incoming = tokio_stream::wrappers::TcpListenerStream::new(listener);
        tokio::spawn(async move {
            tonic::transport::Server::builder()
                .add_service(crate::grpc::metrics::RpcMetricsService::new(
                    GitServiceServer::new(svc),
                ))
                .serve_with_incoming(incoming)
                .await
                .unwrap();
        });
        tokio::time::sleep(std::time::Duration::from_millis(10)).await;
        let url = format!("http://{}", addr);
        proto::git_service_client::GitServiceClient::connect(url)
            .await
            .unwrap()
    }

    /// Create a bare repo at the fanout path for `repo_id` with one commit.
    fn repo_with_commit(data_root: &std::path::Path, repo_id: &str) {
        let repo_path = fanout_path(data_root, repo_id).unwrap();
        std::fs::create_dir_all(repo_path.parent().unwrap()).unwrap();
        let repo = gix::init_bare(&repo_path).unwrap();

        let sig = gix::actor::Signature {
            name: "test".into(),
            email: "test@example.com".into(),
            time: gix::date::Time::now_local_or_utc(),
        };

        let content = b"---\nid: p1\n---\nhello";
        let blob_oid: gix::ObjectId = repo.write_blob(content).unwrap().detach();

        let sub_tree = repo
            .edit_tree(gix::ObjectId::empty_tree(gix::hash::Kind::Sha1))
            .unwrap()
            .upsert("p1.md", gix::object::tree::EntryKind::Blob, blob_oid)
            .unwrap()
            .write()
            .unwrap();

        let root_tree = repo
            .edit_tree(gix::ObjectId::empty_tree(gix::hash::Kind::Sha1))
            .unwrap()
            .upsert(
                "products",
                gix::object::tree::EntryKind::Tree,
                sub_tree.detach(),
            )
            .unwrap()
            .write()
            .unwrap();

        let mut time_buf = gix::date::parse::TimeBuf::default();
        let sig_ref = sig.to_ref(&mut time_buf);
        let commit_id = repo
            .commit_as(
                sig_ref,
                sig_ref,
                "HEAD",
                "init",
                root_tree.detach(),
                std::iter::empty::<gix::ObjectId>(),
            )
            .unwrap()
            .detach();

        // Force HEAD → refs/heads/main and create the branch ref
        use gix::refs::transaction::{Change, LogChange, PreviousValue, RefEdit};
        use gix::refs::Target;
        repo.edit_reference(RefEdit {
            change: Change::Update {
                log: LogChange {
                    mode: gix::refs::transaction::RefLog::AndReference,
                    force_create_reflog: false,
                    message: "init".into(),
                },
                expected: PreviousValue::Any,
                new: Target::Symbolic("refs/heads/main".try_into().unwrap()),
            },
            name: "HEAD".try_into().unwrap(),
            deref: false,
        })
        .unwrap();

        repo.edit_reference(RefEdit {
            change: Change::Update {
                log: LogChange {
                    mode: gix::refs::transaction::RefLog::AndReference,
                    force_create_reflog: false,
                    message: "init".into(),
                },
                expected: PreviousValue::Any,
                new: Target::Object(commit_id),
            },
            name: "refs/heads/main".try_into().unwrap(),
            deref: false,
        })
        .unwrap();

        // Create annotated tag v1.0.0
        let mut time_buf2 = gix::date::parse::TimeBuf::default();
        let sig_ref2 = sig.to_ref(&mut time_buf2);
        repo.tag(
            "v1.0.0",
            commit_id,
            gix::object::Kind::Commit,
            Some(sig_ref2),
            "release v1.0.0",
            PreviousValue::MustNotExist,
        )
        .unwrap();
    }

    /// Create a bare repo with one commit accessible as refs/heads/main.
    fn bare_repo_with_main(data_root: &std::path::Path, repo_id: &str) {
        let repo_path = fanout_path(data_root, repo_id).unwrap();
        std::fs::create_dir_all(repo_path.parent().unwrap()).unwrap();
        let repo = gix::init_bare(&repo_path).unwrap();

        let sig = gix::actor::Signature {
            name: "test".into(),
            email: "test@example.com".into(),
            time: gix::date::Time::now_local_or_utc(),
        };

        let blob_oid: gix::ObjectId = repo.write_blob(b"initial").unwrap().detach();
        let tree = repo
            .edit_tree(gix::ObjectId::empty_tree(gix::hash::Kind::Sha1))
            .unwrap()
            .upsert("README.md", gix::object::tree::EntryKind::Blob, blob_oid)
            .unwrap()
            .write()
            .unwrap();

        let mut time_buf = gix::date::parse::TimeBuf::default();
        let sig_ref = sig.to_ref(&mut time_buf);
        let commit_id = repo
            .commit_as(
                sig_ref,
                sig_ref,
                "HEAD",
                "init",
                tree.detach(),
                std::iter::empty::<gix::ObjectId>(),
            )
            .unwrap()
            .detach();

        use gix::refs::transaction::{Change, LogChange, PreviousValue, RefEdit};
        use gix::refs::Target;
        repo.edit_reference(RefEdit {
            change: Change::Update {
                log: LogChange {
                    mode: gix::refs::transaction::RefLog::AndReference,
                    force_create_reflog: false,
                    message: "init".into(),
                },
                expected: PreviousValue::Any,
                new: Target::Symbolic("refs/heads/main".try_into().unwrap()),
            },
            name: "HEAD".try_into().unwrap(),
            deref: false,
        })
        .unwrap();

        repo.edit_reference(RefEdit {
            change: Change::Update {
                log: LogChange {
                    mode: gix::refs::transaction::RefLog::AndReference,
                    force_create_reflog: false,
                    message: "init".into(),
                },
                expected: PreviousValue::Any,
                new: Target::Object(commit_id),
            },
            name: "refs/heads/main".try_into().unwrap(),
            deref: false,
        })
        .unwrap();
    }

    // Fixed UUIDv7 repository IDs used across tests
    const TEST_REPO_A: &str = "01960000-0000-7000-8000-000000000010";
    const TEST_REPO_B: &str = "01960000-0000-7000-8000-000000000011";
    const TEST_REPO_C: &str = "01960000-0000-7000-8000-000000000012";

    fn test_authorization(repo_id: &str, action: &str) -> Option<RequestAuthorization> {
        Some(RequestAuthorization {
            actor: Some(AuthContext {
                subject: "system:api".to_string(),
                issuer: "test".to_string(),
                auth_method: "internal".to_string(),
                service_account_uid: String::new(),
                groups: vec![],
                scopes: vec![],
            }),
            action: action.to_string(),
            resource_kind: "repository".to_string(),
            resource_name: String::new(),
            repository_id: repo_id.to_string(),
        })
    }

    #[test]
    fn reference_content_conflict_recognizes_existing_and_missing_expectations() {
        use gix::refs::file::transaction::prepare::{MustNotExist, ReferenceOutOfDate};
        let actual = gix::refs::Target::Object(gix::ObjectId::null(gix::hash::Kind::Sha1));
        assert!(is_reference_content_conflict(&gix::Error::from_error(
            ReferenceOutOfDate {
                full_name: "refs/heads/main".into(),
                actual: actual.clone()
            }
        )));
        assert!(is_reference_content_conflict(&gix::Error::from_error(
            MustNotExist {
                full_name: "refs/heads/main".into(),
                actual
            }
        )));
        assert!(!is_reference_content_conflict(&gix::Error::from_error(
            io::Error::new(io::ErrorKind::PermissionDenied, "permission denied")
        )));
    }

    #[test]
    fn canonical_authorization_rejects_legacy_and_spoofed_actors() {
        for action in [
            "repository.read.any",
            "repository.write.own",
            "repository.future",
            "product.read",
        ] {
            let envelope = test_authorization(TEST_REPO_A, action);
            assert!(
                validate_request_authorization(envelope.as_ref(), TEST_REPO_A, &[action]).is_err(),
                "{action}"
            );
        }
        for (subject, method, uid) in [
            ("system:api", "static-users", ""),
            ("alice", "internal", ""),
            ("serviceaccount:ns:controller", "serviceaccount-jwt", "uid"),
            ("system:serviceaccount:controller", "serviceaccount-jwt", ""),
            ("system:serviceaccount:controller", "oidc-jwt", "uid"),
            ("alice", "static-users", "uid"),
            ("anon", "static-users", ""),
        ] {
            let mut envelope = test_authorization(TEST_REPO_A, "repository.contents.read").unwrap();
            let actor = envelope.actor.as_mut().unwrap();
            actor.subject = subject.into();
            actor.auth_method = method.into();
            actor.service_account_uid = uid.into();
            assert!(
                validate_request_authorization(
                    Some(&envelope),
                    TEST_REPO_A,
                    &["repository.contents.read"]
                )
                .is_err(),
                "{subject}/{method}"
            );
        }
        let envelope = test_authorization(TEST_REPO_A, "repository.contents.read");
        assert!(validate_request_authorization(
            envelope.as_ref(),
            TEST_REPO_A,
            &["repository.contents.read"]
        )
        .is_ok());
        assert!(validate_request_authorization(
            envelope.as_ref(),
            TEST_REPO_B,
            &["repository.contents.read"]
        )
        .is_err());
        assert!(validate_request_authorization(
            envelope.as_ref(),
            TEST_REPO_A,
            &["repository.contents.write"]
        )
        .is_err());
    }

    #[test]
    fn anonymous_read_authorization_is_accepted() {
        let authorization = RequestAuthorization {
            actor: Some(AuthContext {
                subject: "anon".to_string(),
                issuer: "gitstore".to_string(),
                auth_method: "none".to_string(),
                service_account_uid: String::new(),
                groups: vec![],
                scopes: vec![],
            }),
            action: "repository.contents.read".to_string(),
            resource_kind: "repository".to_string(),
            resource_name: String::new(),
            repository_id: TEST_REPO_A.to_string(),
        };

        assert!(validate_request_authorization(
            Some(&authorization),
            TEST_REPO_A,
            &["repository.contents.read"],
        )
        .is_ok());
    }

    #[test]
    fn hook_actor_snapshot_is_bound_to_verified_receive() {
        let mut authorization =
            test_authorization(TEST_REPO_A, "repository.contents.write").unwrap();
        let actor = authorization.actor.as_mut().unwrap();
        actor.subject = "system:serviceaccount:controller".into();
        actor.auth_method = "serviceaccount-jwt".into();
        actor.service_account_uid = "enrolled-uid".into();
        actor.groups = vec![
            "system:authenticated".into(),
            "system:serviceaccounts".into(),
        ];
        validate_request_authorization(
            Some(&authorization),
            TEST_REPO_A,
            &["repository.contents.write"],
        )
        .unwrap();
        let push = PushContext {
            repository_id: TEST_REPO_A.into(),
            authorization: Some(authorization),
            ..Default::default()
        };
        let hook = HookContext::from(&push);
        let binding = hook.callback_authorization(vec![
            crate::git::hooks::validation_handler::catalog_proto::ReceiveRef {
                ref_name: "refs/heads/main".into(),
                old_commit_sha: "0".repeat(40),
                new_commit_sha: "a".repeat(40),
            },
        ]);
        assert_eq!(binding.repository_id, TEST_REPO_A);
        assert!(!binding.receive_id.is_empty());
        let actor = binding.actor.unwrap();
        assert_eq!(actor.service_account_uid, "enrolled-uid");
        assert_eq!(
            actor.groups,
            vec!["system:authenticated", "system:serviceaccounts"]
        );
        assert_eq!(binding.refs[0].new_commit_sha, "a".repeat(40));
    }

    #[test]
    fn anonymous_write_authorization_is_rejected() {
        let authorization = RequestAuthorization {
            actor: Some(AuthContext {
                subject: "anon".to_string(),
                issuer: "gitstore".to_string(),
                auth_method: "none".to_string(),
                service_account_uid: String::new(),
                groups: vec![],
                scopes: vec![],
            }),
            action: "repository.contents.write".to_string(),
            resource_kind: "repository".to_string(),
            resource_name: String::new(),
            repository_id: TEST_REPO_A.to_string(),
        };

        let error = validate_request_authorization(
            Some(&authorization),
            TEST_REPO_A,
            &["repository.contents.write"],
        )
        .expect_err("anonymous writes must be rejected");
        assert_eq!(error.code(), tonic::Code::PermissionDenied);
    }

    fn make_create_req(id: &str) -> Request<CreateRepositoryRequest> {
        signed_test_request(CreateRepositoryRequest {
            repository_id: id.to_string(),
            storage_class: String::new(),
            authorization: test_authorization(id, "repository.create"),
        })
    }

    // --- create/delete repository tests ---

    #[tokio::test]
    async fn test_create_repository_succeeds() {
        let dir = TempDir::new().unwrap();
        let svc = make_test_service(dir.path());
        let resp = svc
            .create_repository(make_create_req(TEST_REPO_A))
            .await
            .unwrap()
            .into_inner();
        assert_eq!(resp.repository_id, TEST_REPO_A);
        let expected = fanout_path(dir.path(), TEST_REPO_A).unwrap();
        assert!(expected.exists());
        assert!(resp.storage_path.ends_with(".git"));
    }

    #[tokio::test]
    async fn test_create_repository_fanout_dirs_created() {
        let dir = TempDir::new().unwrap();
        let svc = make_test_service(dir.path());
        svc.create_repository(make_create_req(TEST_REPO_A))
            .await
            .unwrap();
        // Strip hyphens: "01960000..." → l1="01", l2="96"
        assert!(dir.path().join("01").join("96").exists());
    }

    #[tokio::test]
    async fn test_create_repository_already_exists() {
        let dir = TempDir::new().unwrap();
        let svc = make_test_service(dir.path());
        svc.create_repository(make_create_req(TEST_REPO_B))
            .await
            .unwrap();
        let err = svc
            .create_repository(make_create_req(TEST_REPO_B))
            .await
            .unwrap_err();
        assert_eq!(err.code(), tonic::Code::AlreadyExists);
    }

    #[tokio::test]
    async fn test_create_existing_repository_does_not_exclude_readers() {
        let dir = TempDir::new().unwrap();
        let svc = make_test_service(dir.path());
        svc.create_repository(make_create_req(TEST_REPO_A))
            .await
            .unwrap();
        let operation = get_or_insert_lock(&svc.repo_locks, TEST_REPO_A);
        let snapshot = get_or_insert_snapshot_lock(&svc.repo_locks, TEST_REPO_A);
        let _operation_reader = operation.read().await;
        let _snapshot_reader = snapshot.read().await;

        let error = tokio::time::timeout(
            std::time::Duration::from_secs(5),
            svc.create_repository(make_create_req(TEST_REPO_A)),
        )
        .await
        .expect("an existing repository check must coexist with immutable readers")
        .unwrap_err();
        assert_eq!(error.code(), tonic::Code::AlreadyExists);
    }

    #[tokio::test]
    async fn test_create_existing_repository_still_waits_for_lifecycle_writer() {
        let dir = TempDir::new().unwrap();
        let svc = make_test_service(dir.path());
        svc.create_repository(make_create_req(TEST_REPO_A))
            .await
            .unwrap();
        let operation = get_or_insert_lock(&svc.repo_locks, TEST_REPO_A);
        let writer = operation.write().await;
        assert!(tokio::time::timeout(
            std::time::Duration::from_millis(20),
            svc.create_repository(make_create_req(TEST_REPO_A)),
        )
        .await
        .is_err());
        drop(writer);
        svc.delete_repository(signed_test_request(DeleteRepositoryRequest {
            repository_id: TEST_REPO_A.into(),
            authorization: test_authorization(TEST_REPO_A, "repository.delete"),
        }))
        .await
        .unwrap();
        assert_eq!(
            svc.create_repository(make_create_req(TEST_REPO_A))
                .await
                .unwrap_err()
                .code(),
            tonic::Code::FailedPrecondition,
        );
    }

    #[tokio::test]
    async fn test_create_existing_repository_requires_signature_and_authorization() {
        let dir = TempDir::new().unwrap();
        let svc = make_test_service(dir.path());
        svc.create_repository(make_create_req(TEST_REPO_A))
            .await
            .unwrap();
        let mut unsigned = make_create_req(TEST_REPO_A);
        unsigned
            .metadata_mut()
            .remove(crate::auth::interceptor::SIGNATURE_HEADER);
        assert_eq!(
            svc.create_repository(unsigned).await.unwrap_err().code(),
            tonic::Code::Unauthenticated,
        );
        let unauthorized = signed_test_request(CreateRepositoryRequest {
            repository_id: TEST_REPO_A.into(),
            authorization: test_authorization(TEST_REPO_A, "repository.contents.read"),
            ..Default::default()
        });
        assert_eq!(
            svc.create_repository(unauthorized)
                .await
                .unwrap_err()
                .code(),
            tonic::Code::PermissionDenied,
        );
    }

    #[tokio::test]
    async fn test_create_existing_repository_consumes_signed_nonce() {
        let dir = TempDir::new().unwrap();
        let svc = make_test_service(dir.path());
        svc.create_repository(make_create_req(TEST_REPO_A))
            .await
            .unwrap();
        let request = make_create_req(TEST_REPO_A);
        let mut replay = Request::new(request.get_ref().clone());
        *replay.metadata_mut() = request.metadata().clone();
        assert_eq!(
            svc.create_repository(request).await.unwrap_err().code(),
            tonic::Code::AlreadyExists,
        );
        assert_eq!(
            svc.create_repository(replay).await.unwrap_err().code(),
            tonic::Code::Unauthenticated,
        );
    }

    #[tokio::test]
    async fn test_concurrent_repository_creates_have_one_winner() {
        let dir = TempDir::new().unwrap();
        let svc = Arc::new(make_test_service(dir.path()));
        let mut tasks = tokio::task::JoinSet::new();
        for _ in 0..16 {
            let svc = Arc::clone(&svc);
            tasks.spawn(async move { svc.create_repository(make_create_req(TEST_REPO_A)).await });
        }
        let mut created = 0;
        let mut existing = 0;
        while let Some(result) = tasks.join_next().await {
            match result.unwrap() {
                Ok(_) => created += 1,
                Err(error) => {
                    assert_eq!(error.code(), tonic::Code::AlreadyExists);
                    existing += 1;
                }
            }
        }
        assert_eq!((created, existing), (1, 15));
    }

    #[tokio::test]
    async fn test_delete_repository_succeeds() {
        let dir = TempDir::new().unwrap();
        let svc = make_test_service(dir.path());
        svc.create_repository(make_create_req(TEST_REPO_C))
            .await
            .unwrap();
        let repo_path = fanout_path(dir.path(), TEST_REPO_C).unwrap();
        assert!(repo_path.exists());

        let resp = svc
            .delete_repository(signed_test_request(DeleteRepositoryRequest {
                repository_id: TEST_REPO_C.to_string(),
                authorization: test_authorization(TEST_REPO_C, "repository.delete"),
            }))
            .await
            .unwrap()
            .into_inner();
        assert_eq!(resp.repository_id, TEST_REPO_C);
        assert!(!repo_path.exists());
    }

    #[tokio::test]
    async fn test_delete_repository_missing_storage_is_idempotent_and_fenced() {
        let dir = TempDir::new().unwrap();
        let svc = make_test_service(dir.path());
        for _ in 0..2 {
            svc.delete_repository(signed_test_request(DeleteRepositoryRequest {
                repository_id: TEST_REPO_A.to_string(),
                authorization: test_authorization(TEST_REPO_A, "repository.delete"),
            }))
            .await
            .unwrap();
        }
        let replacement = make_test_service(dir.path());
        let err = replacement
            .create_repository(make_create_req(TEST_REPO_A))
            .await
            .unwrap_err();
        assert_eq!(err.code(), tonic::Code::FailedPrecondition);
        replacement
            .create_repository(make_create_req(TEST_REPO_B))
            .await
            .unwrap();
    }

    #[tokio::test]
    async fn test_delete_repository_tombstone_failure_preserves_storage() {
        let dir = TempDir::new().unwrap();
        repo_with_commit(dir.path(), TEST_REPO_A);
        let repo_path = fanout_path(dir.path(), TEST_REPO_A).unwrap();
        std::fs::create_dir(repo_path.with_extension("git.deleted")).unwrap();
        let svc = make_test_service(dir.path());
        let err = svc
            .delete_repository(signed_test_request(DeleteRepositoryRequest {
                repository_id: TEST_REPO_A.into(),
                authorization: test_authorization(TEST_REPO_A, "repository.delete"),
            }))
            .await
            .unwrap_err();
        assert_eq!(err.code(), tonic::Code::Internal);
        let repo = gix::open(&repo_path).unwrap();
        find_blob_in_tree(
            &repo,
            repo.head_commit().unwrap().tree_id().unwrap().detach(),
            "products/p1.md",
        )
        .unwrap();
    }

    #[tokio::test]
    async fn test_delete_repository_tombstone_survives_failed_removal_and_retry() {
        let dir = TempDir::new().unwrap();
        let svc = make_test_service(dir.path());
        let repo_path = fanout_path(dir.path(), TEST_REPO_A).unwrap();
        std::fs::create_dir_all(repo_path.parent().unwrap()).unwrap();
        // A non-directory forces removal to fail after the durable deletion intent.
        std::fs::write(&repo_path, b"incomplete storage").unwrap();
        let request = || {
            signed_test_request(DeleteRepositoryRequest {
                repository_id: TEST_REPO_A.into(),
                authorization: test_authorization(TEST_REPO_A, "repository.delete"),
            })
        };
        assert_eq!(
            svc.delete_repository(request()).await.unwrap_err().code(),
            tonic::Code::Internal
        );
        assert!(repo_path.with_extension("git.deleted").is_file());
        std::fs::remove_file(&repo_path).unwrap();
        repo_with_commit(dir.path(), TEST_REPO_A);
        let repo = gix::open(&repo_path).unwrap();
        let initial = repo.head_commit().unwrap().id().to_string();
        drop(repo);

        let replacement = make_test_service(dir.path());
        assert_eq!(
            replacement
                .create_repository(make_create_req(TEST_REPO_A))
                .await
                .unwrap_err()
                .code(),
            tonic::Code::FailedPrecondition
        );
        let err = replacement
            .delete_file(signed_test_request(DeleteFileRequest {
                repository_id: TEST_REPO_A.into(),
                path: "products/p1.md".into(),
                ref_name: Some("refs/heads/main".into()),
                expected_commit_sha: Some(initial),
                authorization: test_authorization(TEST_REPO_A, "repository.contents.write"),
                ..Default::default()
            }))
            .await
            .unwrap_err();
        assert_eq!(err.code(), tonic::Code::FailedPrecondition);
        replacement.delete_repository(request()).await.unwrap();
        assert!(!repo_path.exists());
        assert!(repo_path.with_extension("git.deleted").is_file());
        replacement.delete_repository(request()).await.unwrap();
        assert_eq!(
            replacement
                .create_repository(make_create_req(TEST_REPO_A))
                .await
                .unwrap_err()
                .code(),
            tonic::Code::FailedPrecondition
        );
        replacement
            .create_repository(make_create_req(TEST_REPO_B))
            .await
            .unwrap();
    }

    #[tokio::test]
    async fn test_create_and_delete_repository_share_retained_lock() {
        let dir = TempDir::new().unwrap();
        let svc = make_test_service(dir.path());
        let lock = get_or_insert_lock(&svc.repo_locks, TEST_REPO_A);
        let guard = lock.write().await;
        assert!(tokio::time::timeout(
            std::time::Duration::from_millis(20),
            svc.create_repository(make_create_req(TEST_REPO_A))
        )
        .await
        .is_err());
        assert!(!fanout_path(dir.path(), TEST_REPO_A).unwrap().exists());
        drop(guard);
        svc.create_repository(make_create_req(TEST_REPO_A))
            .await
            .unwrap();
        svc.delete_repository(signed_test_request(DeleteRepositoryRequest {
            repository_id: TEST_REPO_A.into(),
            authorization: test_authorization(TEST_REPO_A, "repository.delete"),
        }))
        .await
        .unwrap();
        assert!(Arc::ptr_eq(
            &lock,
            &get_or_insert_lock(&svc.repo_locks, TEST_REPO_A)
        ));
    }

    #[tokio::test]
    async fn test_get_file_unknown_repo_returns_failed_precondition() {
        let dir = TempDir::new().unwrap();
        let svc = make_test_service(dir.path());
        let err = svc
            .get_file(signed_test_request(GetFileRequest {
                repository_id: TEST_REPO_A.to_string(),
                path: "README.md".to_string(),
                r#ref: "HEAD".to_string(),
                authorization: test_authorization(TEST_REPO_A, "repository.contents.read"),
                unchanged_since_commit: None,
                allow_ancestor_receipt: false,
            }))
            .await
            .unwrap_err();
        assert_eq!(err.code(), tonic::Code::FailedPrecondition);
    }

    #[tokio::test]
    async fn test_invalid_repo_id_rejected() {
        let dir = TempDir::new().unwrap();
        let svc = make_test_service(dir.path());

        // Non-UUID names must be rejected with INVALID_ARGUMENT
        for bad_name in &["", "myrepo", "../etc", "a/b", "a\\b"] {
            let err = svc
                .get_file(signed_test_request(GetFileRequest {
                    repository_id: bad_name.to_string(),
                    path: "README.md".to_string(),
                    r#ref: "HEAD".to_string(),
                    authorization: test_authorization(bad_name, "repository.contents.read"),
                    unchanged_since_commit: None,
                    allow_ancestor_receipt: false,
                }))
                .await
                .unwrap_err();
            assert_eq!(
                err.code(),
                tonic::Code::InvalidArgument,
                "expected INVALID_ARGUMENT for id {:?}",
                bad_name
            );
        }
    }

    // Additional UUIDs for tests that need pre-seeded repos
    const TEST_REPO_D: &str = "01960000-0000-7000-8000-000000000020";
    const TEST_REPO_E: &str = "01960000-0000-7000-8000-000000000021";
    const TEST_REPO_F: &str = "01960000-0000-7000-8000-000000000022";
    const TEST_REPO_G: &str = "01960000-0000-7000-8000-000000000023";

    #[tokio::test]
    async fn test_get_files_signed_snapshot_bounds_missing_and_legacy_wire() {
        let root = TempDir::new().unwrap();
        repo_with_commit(root.path(), TEST_REPO_D);
        let repo = gix::open(fanout_path(root.path(), TEST_REPO_D).unwrap()).unwrap();
        let commit_sha = repo.head_commit().unwrap().id.to_string();
        let mut client = make_grpc_client(root.path().to_path_buf()).await;
        let request = |paths: Vec<String>, commit: &str| GetFilesRequest {
            repository_id: TEST_REPO_D.to_owned(),
            paths,
            commit_sha: commit.to_owned(),
            authorization: test_authorization(TEST_REPO_D, "repository.contents.read"),
            secondary_commit_sha: None,
            current_ref: None,
        };
        let message = request(
            vec!["missing.md".into(), "products/p1.md".into()],
            &commit_sha,
        );
        let signed = signed_test_request(message.clone());
        let mut replay = Request::new(signed.get_ref().clone());
        *replay.metadata_mut() = signed.metadata().clone();
        let response = client.get_files(signed).await.unwrap();
        for key in [
            "x-gitstore-file-read-authentication-ns",
            "x-gitstore-file-read-repository-lock-ns",
            "x-gitstore-file-read-blocking-queue-ns",
            "x-gitstore-file-read-payload-ns",
            "x-gitstore-file-read-repository-open-ns",
            "x-gitstore-file-read-revision-ns",
            "x-gitstore-file-read-target-tree-ns",
            "x-gitstore-file-read-blob-ns",
        ] {
            response
                .metadata()
                .get(key)
                .unwrap()
                .to_str()
                .unwrap()
                .parse::<u64>()
                .unwrap();
        }
        for key in [
            "x-gitstore-file-read-history-commit-ns",
            "x-gitstore-file-read-history-tree-ns",
            "x-gitstore-file-read-history-commits",
        ] {
            assert_eq!(response.metadata().get(key).unwrap(), "0");
        }
        let result = response.into_inner();
        assert_eq!(result.commit_sha, commit_sha);
        assert_eq!(result.files.len(), 2);
        assert_eq!(result.files[0].path, "missing.md");
        assert!(!result.files[0].found && result.files[0].content.is_empty());
        assert_eq!(result.files[1].path, "products/p1.md");
        assert!(result.files[1].found);
        assert_eq!(result.files[1].content, b"---\nid: p1\n---\nhello");
        assert_eq!(
            client.get_files(replay).await.unwrap_err().code(),
            tonic::Code::Unauthenticated
        );
        let mut tampered = signed_test_request(message);
        tampered.get_mut().paths[0] = "other.md".into();
        assert_eq!(
            client.get_files(tampered).await.unwrap_err().code(),
            tonic::Code::Unauthenticated
        );
        for (authorization, code) in [
            (None, tonic::Code::InvalidArgument),
            (
                test_authorization(TEST_REPO_D, "repository.contents.write"),
                tonic::Code::PermissionDenied,
            ),
            (
                test_authorization(TEST_REPO_E, "repository.contents.read"),
                tonic::Code::PermissionDenied,
            ),
        ] {
            let mut message = request(vec!["products/p1.md".into()], &commit_sha);
            message.authorization = authorization;
            assert_eq!(
                client
                    .get_files(signed_test_request(message))
                    .await
                    .unwrap_err()
                    .code(),
                code
            );
        }
        let mut missing_repository = request(vec!["products/p1.md".into()], &commit_sha);
        missing_repository.repository_id = TEST_REPO_E.into();
        missing_repository.authorization =
            test_authorization(TEST_REPO_E, "repository.contents.read");
        assert_eq!(
            client
                .get_files(signed_test_request(missing_repository))
                .await
                .unwrap_err()
                .code(),
            tonic::Code::FailedPrecondition
        );
        let missing_commit = "0".repeat(40);
        let blob_sha = result.files[1].blob_sha.clone();
        for (paths, commit, code) in [
            (
                vec!["products/p1.md".into()],
                missing_commit.as_str(),
                tonic::Code::FailedPrecondition,
            ),
            (
                vec!["products/p1.md".into()],
                blob_sha.as_str(),
                tonic::Code::FailedPrecondition,
            ),
            (
                Vec::new(),
                commit_sha.as_str(),
                tonic::Code::InvalidArgument,
            ),
            (
                (0..5).map(|i| format!("{i}.md")).collect(),
                commit_sha.as_str(),
                tonic::Code::InvalidArgument,
            ),
            (
                vec!["products/p1.md".into(); 2],
                commit_sha.as_str(),
                tonic::Code::InvalidArgument,
            ),
            (
                vec![String::new()],
                commit_sha.as_str(),
                tonic::Code::InvalidArgument,
            ),
            (
                vec!["products/p1.md".into()],
                "HEAD",
                tonic::Code::InvalidArgument,
            ),
            (
                vec!["products".into()],
                commit_sha.as_str(),
                tonic::Code::FailedPrecondition,
            ),
        ] {
            assert_eq!(
                client
                    .get_files(signed_test_request(request(paths, commit)))
                    .await
                    .unwrap_err()
                    .code(),
                code
            );
        }
        let legacy = client
            .get_file(signed_test_request(GetFileRequest {
                repository_id: TEST_REPO_D.to_owned(),
                path: "products/p1.md".into(),
                r#ref: commit_sha.clone(),
                unchanged_since_commit: None,
                authorization: test_authorization(TEST_REPO_D, "repository.contents.read"),
                allow_ancestor_receipt: false,
            }))
            .await
            .unwrap()
            .into_inner();
        assert_eq!(legacy.content, result.files[1].content);
    }

    #[tokio::test]
    async fn test_get_files_signed_pair_has_independent_immutable_receipts() {
        let root = TempDir::new().unwrap();
        repo_with_commit(root.path(), TEST_REPO_D);
        let repo = gix::open(fanout_path(root.path(), TEST_REPO_D).unwrap()).unwrap();
        let initial = repo.head_commit().unwrap().id;
        let old_blob = repo.write_blob(b"old").unwrap().detach();
        let new_blob = repo.write_blob(b"new").unwrap().detach();
        let empty_blob = repo.write_blob(b"").unwrap().detach();
        let signature = gix::actor::SignatureRef {
            name: "Test".into(),
            email: "test@example.com".into(),
            time: "0 +0000",
        };
        let old_tree = repo
            .write_object(&gix::objs::Tree {
                entries: vec![gix::objs::tree::Entry {
                    mode: gix::objs::tree::EntryKind::Blob.into(),
                    filename: "old.md".into(),
                    oid: old_blob,
                }],
            })
            .unwrap()
            .detach();
        let old = repo
            .commit_as(
                signature,
                signature,
                "refs/heads/main",
                "old snapshot",
                old_tree,
                [initial],
            )
            .unwrap()
            .detach();
        let new_tree = repo
            .write_object(&gix::objs::Tree {
                entries: vec![
                    gix::objs::tree::Entry {
                        mode: gix::objs::tree::EntryKind::Blob.into(),
                        filename: "empty.md".into(),
                        oid: empty_blob,
                    },
                    gix::objs::tree::Entry {
                        mode: gix::objs::tree::EntryKind::Blob.into(),
                        filename: "new.md".into(),
                        oid: new_blob,
                    },
                ],
            })
            .unwrap()
            .detach();
        let new = repo
            .commit_as(
                signature,
                signature,
                "refs/heads/main",
                "new snapshot",
                new_tree,
                [old],
            )
            .unwrap()
            .detach();
        let message = GetFilesRequest {
            repository_id: TEST_REPO_D.into(),
            paths: vec!["new.md".into(), "old.md".into(), "empty.md".into()],
            commit_sha: old.to_string(),
            secondary_commit_sha: Some(new.to_string()),
            current_ref: None,
            authorization: test_authorization(TEST_REPO_D, "repository.contents.read"),
        };
        let mut client = make_grpc_client(root.path().to_path_buf()).await;
        let result = client
            .get_files(signed_test_request(message.clone()))
            .await
            .unwrap()
            .into_inner();
        assert_eq!(result.commit_sha, old.to_string());
        assert_eq!(result.secondary_commit_sha, new.to_string());
        for files in [&result.files, &result.secondary_files] {
            assert_eq!(
                files.iter().map(|file| &file.path).collect::<Vec<_>>(),
                message.paths.iter().collect::<Vec<_>>()
            );
        }
        assert!(!result.files[0].found);
        assert_eq!(result.files[1].content, b"old");
        assert!(!result.files[2].found);
        assert_eq!(result.secondary_files[0].content, b"new");
        assert!(!result.secondary_files[1].found);
        assert!(result.secondary_files[2].found && result.secondary_files[2].content.is_empty());
        assert!(result.current_ref_commit_sha.is_empty());
        let mut current_request = message.clone();
        current_request.current_ref = Some("refs/heads/main".into());
        let current_result = client
            .get_files(signed_test_request(current_request.clone()))
            .await
            .unwrap()
            .into_inner();
        assert_eq!(current_result.current_ref_commit_sha, new.to_string());
        assert_eq!(current_result.files, result.files);
        assert_eq!(current_result.secondary_files, result.secondary_files);
        let mut tampered_ref = signed_test_request(current_request.clone());
        tampered_ref.get_mut().current_ref = Some("refs/heads/other".into());
        assert_eq!(
            client.get_files(tampered_ref).await.unwrap_err().code(),
            tonic::Code::Unauthenticated
        );
        for ref_name in ["", "HEAD", "refs/heads/missing"] {
            let mut invalid = current_request.clone();
            invalid.current_ref = Some(ref_name.into());
            assert_eq!(
                client
                    .get_files(signed_test_request(invalid))
                    .await
                    .unwrap_err()
                    .code(),
                if ref_name.starts_with("refs/") {
                    tonic::Code::Aborted
                } else {
                    tonic::Code::InvalidArgument
                }
            );
        }
        let mut no_secondary = current_request.clone();
        no_secondary.secondary_commit_sha = None;
        assert_eq!(
            client
                .get_files(signed_test_request(no_secondary))
                .await
                .unwrap_err()
                .code(),
            tonic::Code::InvalidArgument
        );
        let mut stale = current_request.clone();
        stale.secondary_commit_sha = Some(old.to_string());
        assert_eq!(
            client
                .get_files(signed_test_request(stale))
                .await
                .unwrap_err()
                .code(),
            tonic::Code::Aborted
        );
        repo.find_reference("refs/heads/main")
            .unwrap()
            .delete()
            .unwrap();
        assert_eq!(
            client
                .get_files(signed_test_request(current_request))
                .await
                .unwrap_err()
                .code(),
            tonic::Code::Aborted
        );
        let mut tampered = signed_test_request(message.clone());
        tampered.get_mut().secondary_commit_sha = Some(old.to_string());
        assert_eq!(
            client.get_files(tampered).await.unwrap_err().code(),
            tonic::Code::Unauthenticated
        );
        for (secondary, code) in [
            ("HEAD".to_string(), tonic::Code::InvalidArgument),
            (String::new(), tonic::Code::InvalidArgument),
            ("0".repeat(40), tonic::Code::FailedPrecondition),
            (old_blob.to_string(), tonic::Code::FailedPrecondition),
        ] {
            let mut request = message.clone();
            request.secondary_commit_sha = Some(secondary);
            assert_eq!(
                client
                    .get_files(signed_test_request(request))
                    .await
                    .unwrap_err()
                    .code(),
                code
            );
        }
        let new_id = new.to_string();
        let loose = fanout_path(root.path(), TEST_REPO_D)
            .unwrap()
            .join("objects")
            .join(&new_id[..2])
            .join(&new_id[2..]);
        std::fs::remove_file(&loose).unwrap();
        std::fs::write(&loose, b"corrupt compressed commit").unwrap();
        assert_eq!(
            client
                .get_files(signed_test_request(message))
                .await
                .unwrap_err()
                .code(),
            tonic::Code::Internal
        );
    }

    #[tokio::test]
    async fn test_get_files_enforces_actual_blob_and_encoded_response_bounds() {
        const MIB: usize = 1024 * 1024;
        for length in [4 * MIB - 2048, 4 * MIB, 4 * MIB + 1] {
            let root = TempDir::new().unwrap();
            repo_with_commit(root.path(), TEST_REPO_G);
            let repo = gix::open(fanout_path(root.path(), TEST_REPO_G).unwrap()).unwrap();
            let parent = repo.head_commit().unwrap().id;
            let blob = repo.write_blob(vec![b'x'; length]).unwrap().detach();
            let tree = repo
                .write_object(&gix::objs::Tree {
                    entries: (0..4)
                        .map(|i| gix::objs::tree::Entry {
                            mode: gix::objs::tree::EntryKind::Blob.into(),
                            filename: format!("{i}.md").into(),
                            oid: blob,
                        })
                        .collect(),
                })
                .unwrap()
                .detach();
            let signature = gix::actor::SignatureRef {
                name: "Test".into(),
                email: "test@example.com".into(),
                time: "0 +0000",
            };
            let commit = repo
                .commit_as(
                    signature,
                    signature,
                    "refs/heads/main",
                    "bounded snapshot",
                    tree,
                    [parent],
                )
                .unwrap()
                .detach();
            let mut client = make_grpc_client(root.path().to_path_buf())
                .await
                .max_decoding_message_size(16 * MIB);
            let response = client
                .get_files(signed_test_request(GetFilesRequest {
                    repository_id: TEST_REPO_G.to_owned(),
                    paths: (0..4).map(|i| format!("{i}.md")).collect(),
                    commit_sha: commit.to_string(),
                    authorization: test_authorization(TEST_REPO_G, "repository.contents.read"),
                    secondary_commit_sha: None,
                    current_ref: None,
                }))
                .await;
            if length < 4 * MIB {
                let result = response.unwrap().into_inner();
                assert_eq!(result.files.len(), 4);
                assert!(result
                    .files
                    .iter()
                    .all(|file| file.found && file.content.len() == length));
                let encoded = prost::Message::encoded_len(&result);
                assert!(encoded <= 16 * MIB && encoded > 16 * MIB - 10000);
            } else {
                let error = response.unwrap_err();
                assert_eq!(error.code(), tonic::Code::ResourceExhausted);
                assert!(error.message().contains(if length == 4 * MIB {
                    "response exceeds 16 MiB"
                } else {
                    "blob exceeds 4 MiB"
                }));
            }
        }
    }

    #[tokio::test]
    async fn test_get_files_pair_shares_exact_encoded_response_ceiling() {
        const MIB: usize = 1024 * 1024;
        let base = 4 * MIB - 2048;
        let sample = FileSnapshot {
            path: "0.md".into(),
            found: true,
            content: vec![b'x'; base],
            blob_sha: "a".repeat(40),
            size_bytes: base as u64,
        };
        let sample_response = GetFilesResponse {
            files: vec![
                sample.clone(),
                FileSnapshot {
                    path: "1.md".into(),
                    ..sample.clone()
                },
            ],
            secondary_files: vec![
                sample.clone(),
                FileSnapshot {
                    path: "1.md".into(),
                    ..sample
                },
            ],
            commit_sha: "a".repeat(40),
            secondary_commit_sha: "b".repeat(40),
            current_ref_commit_sha: String::new(),
        };
        let remaining = 16 * MIB - prost::Message::encoded_len(&sample_response);
        assert_eq!(remaining % 4, 0);
        let exact_length = base + remaining / 4;
        drop(sample_response);
        for length in [exact_length, exact_length + 1, 4 * MIB + 1] {
            let root = TempDir::new().unwrap();
            repo_with_commit(root.path(), TEST_REPO_G);
            let repo = gix::open(fanout_path(root.path(), TEST_REPO_G).unwrap()).unwrap();
            let parent = repo.head_commit().unwrap().id;
            let blob = repo.write_blob(vec![b'x'; length]).unwrap().detach();
            let tree = repo
                .write_object(&gix::objs::Tree {
                    entries: (0..2)
                        .map(|i| gix::objs::tree::Entry {
                            mode: gix::objs::tree::EntryKind::Blob.into(),
                            filename: format!("{i}.md").into(),
                            oid: blob,
                        })
                        .collect(),
                })
                .unwrap()
                .detach();
            let signature = gix::actor::SignatureRef {
                name: "Test".into(),
                email: "test@example.com".into(),
                time: "0 +0000",
            };
            let commit = repo
                .commit_as(
                    signature,
                    signature,
                    "refs/heads/main",
                    "paired ceiling",
                    tree,
                    [parent],
                )
                .unwrap()
                .detach();
            let message = GetFilesRequest {
                repository_id: TEST_REPO_G.into(),
                paths: vec!["0.md".into(), "1.md".into()],
                commit_sha: commit.to_string(),
                secondary_commit_sha: Some(commit.to_string()),
                current_ref: None,
                authorization: test_authorization(TEST_REPO_G, "repository.contents.read"),
            };
            let mut client = make_grpc_client(root.path().to_path_buf())
                .await
                .max_decoding_message_size(16 * MIB);
            let response = client.get_files(signed_test_request(message.clone())).await;
            if length == exact_length {
                let result = response.unwrap().into_inner();
                assert_eq!(prost::Message::encoded_len(&result), 16 * MIB);
                assert_eq!(result.files.len(), 2);
                assert_eq!(result.secondary_files.len(), 2);
                let mut with_receipt = message.clone();
                with_receipt.current_ref = Some("refs/heads/main".into());
                assert_eq!(
                    client
                        .get_files(signed_test_request(with_receipt))
                        .await
                        .unwrap_err()
                        .code(),
                    tonic::Code::ResourceExhausted,
                    "the current-ref receipt must fit the same encoded ceiling"
                );
            } else {
                let error = response.unwrap_err();
                assert_eq!(error.code(), tonic::Code::ResourceExhausted);
                assert!(error.message().contains(if length > 4 * MIB {
                    "blob exceeds 4 MiB"
                } else {
                    "response exceeds 16 MiB"
                }));
                if length <= 4 * MIB {
                    let ordinary = GetFilesRequest {
                        secondary_commit_sha: None,
                        ..message
                    };
                    assert!(client
                        .get_files(signed_test_request(ordinary))
                        .await
                        .is_ok());
                }
            }
        }
    }

    #[test]
    fn test_get_files_current_ref_waits_for_writer_and_rejects_supersession() {
        let runtime = tokio::runtime::Builder::new_current_thread()
            .enable_all()
            .build()
            .unwrap();
        runtime.block_on(async {
            for delete in [false, true] {
                let root = TempDir::new().unwrap();
                repo_with_commit(root.path(), TEST_REPO_D);
                let repo = gix::open(fanout_path(root.path(), TEST_REPO_D).unwrap()).unwrap();
                let commit = repo.head_commit().unwrap().id().detach();
                let service = make_test_service(root.path());
                let lock = get_or_insert_lock(&service.repo_locks, TEST_REPO_D);
                let writer = lock.write().await;
                let request = signed_test_request(GetFilesRequest {
                    repository_id: TEST_REPO_D.into(),
                    paths: vec!["products/p1.md".into(), "missing.md".into()],
                    commit_sha: commit.to_string(),
                    secondary_commit_sha: Some(commit.to_string()),
                    current_ref: Some("refs/heads/main".into()),
                    authorization: test_authorization(TEST_REPO_D, "repository.contents.read"),
                });
                let envelope = request
                    .metadata()
                    .get(crate::auth::interceptor::SIGNATURE_HEADER)
                    .unwrap()
                    .to_str()
                    .unwrap();
                let fields: Vec<_> = envelope.split(':').collect();
                let nonce = fields[2].to_owned();
                let mut reader = Box::pin(service.get_files(request));
                tokio::time::timeout(std::time::Duration::from_secs(5), async {
                    std::future::poll_fn(|cx| {
                        assert!(std::future::Future::poll(reader.as_mut(), cx).is_pending());
                        if service.rpc_auth.has_replay_reservation(&nonce).unwrap() {
                            std::task::Poll::Ready(())
                        } else {
                            cx.waker().wake_by_ref();
                            std::task::Poll::Pending
                        }
                    })
                    .await;
                })
                .await
                .unwrap();
                if delete {
                    repo.find_reference("refs/heads/main")
                        .unwrap()
                        .delete()
                        .unwrap();
                } else {
                    let signature = gix::actor::SignatureRef {
                        name: "Test".into(),
                        email: "test@example.com".into(),
                        time: "0 +0000",
                    };
                    repo.commit_as(
                        signature,
                        signature,
                        "refs/heads/main",
                        "concurrent replacement",
                        repo.head_commit().unwrap().tree_id().unwrap().detach(),
                        [commit],
                    )
                    .unwrap();
                }
                drop(writer);
                let error = reader.await.unwrap_err();
                assert_eq!(error.code(), tonic::Code::Aborted);
            }
        });
    }

    #[test]
    fn test_get_files_pair_cancellation_retains_queued_blocking_read_guard() {
        let runtime = tokio::runtime::Builder::new_current_thread()
            .enable_all()
            .max_blocking_threads(1)
            .build()
            .unwrap();
        runtime.block_on(async {
            let root = TempDir::new().unwrap();
            repo_with_commit(root.path(), TEST_REPO_D);
            let commit = gix::open(fanout_path(root.path(), TEST_REPO_D).unwrap())
                .unwrap()
                .head_commit()
                .unwrap()
                .id
                .to_string();
            let service = Arc::new(make_test_service(root.path()));
            let lock = get_or_insert_lock(&service.repo_locks, TEST_REPO_D);
            let writer = lock.write().await;
            let request = signed_test_request(GetFilesRequest {
                repository_id: TEST_REPO_D.into(),
                paths: vec!["products/p1.md".into(), "missing.md".into()],
                commit_sha: commit.clone(),
                secondary_commit_sha: Some(commit),
                current_ref: Some("refs/heads/main".into()),
                authorization: test_authorization(TEST_REPO_D, "repository.contents.read"),
            });
            let envelope = request
                .metadata()
                .get(crate::auth::interceptor::SIGNATURE_HEADER)
                .unwrap()
                .to_str()
                .unwrap();
            let fields: Vec<_> = envelope.split(':').collect();
            let nonce = fields[2].to_owned();
            let mut reader = Box::pin(service.get_files(request));
            std::future::poll_fn(|cx| {
                assert!(std::future::Future::poll(reader.as_mut(), cx).is_pending());
                std::task::Poll::Ready(())
            })
            .await;
            tokio::time::timeout(std::time::Duration::from_secs(5), async {
                while !service.rpc_auth.has_replay_reservation(&nonce).unwrap() {
                    tokio::task::yield_now().await;
                }
            })
            .await
            .unwrap();
            let (release, receive) = std::sync::mpsc::channel::<()>();
            let (started, ready) = tokio::sync::oneshot::channel();
            let blocker = crate::spawn_blocking(move || {
                let _ = started.send(());
                let _ = receive.recv();
            });
            tokio::time::timeout(std::time::Duration::from_secs(5), ready)
                .await
                .unwrap()
                .unwrap();
            drop(writer);
            std::future::poll_fn(|cx| {
                assert!(std::future::Future::poll(reader.as_mut(), cx).is_pending());
                std::task::Poll::Ready(())
            })
            .await;
            drop(reader);
            let retained = lock.try_write().is_err();
            drop(release);
            blocker.await.unwrap();
            let writer = tokio::time::timeout(std::time::Duration::from_secs(5), lock.write())
                .await
                .unwrap();
            drop(writer);
            assert!(
                retained,
                "cancelled caller released a queued snapshot job's read guard"
            );
        });
    }

    #[tokio::test]
    async fn test_receive_pack_snapshot_reads_pass_validation_but_exclude_publication() {
        use crate::git::hooks::{AdmissionDecision, ResourceBlob, ValidationHandler};

        struct PausedValidation {
            entered: Arc<tokio::sync::Notify>,
            proceed: Arc<tokio::sync::Notify>,
        }

        #[async_trait::async_trait]
        impl ValidationHandler for PausedValidation {
            async fn validate(
                &self,
                _blobs: &[ResourceBlob],
                _hook_ctx: &HookContext,
            ) -> anyhow::Result<AdmissionDecision> {
                self.entered.notify_one();
                self.proceed.notified().await;
                Ok(AdmissionDecision::Accept)
            }
        }

        let root = TempDir::new().unwrap();
        repo_with_commit(root.path(), TEST_REPO_D);
        let path = fanout_path(root.path(), TEST_REPO_D).unwrap();
        let old = gix::open(&path)
            .unwrap()
            .head_commit()
            .unwrap()
            .id
            .to_string();
        let entered = Arc::new(tokio::sync::Notify::new());
        let proceed = Arc::new(tokio::sync::Notify::new());
        let service = GitServiceImpl::with_pipeline(
            root.path().into(),
            Arc::new(HookPipeline::new(
                std::time::Duration::from_secs(10),
                Arc::new(PausedValidation {
                    entered: entered.clone(),
                    proceed: proceed.clone(),
                }),
                Arc::new(NoopAdmissionHandler),
            )),
            PushLimits::default(),
            RPCVerifier::new("fixture-key", None, root.path()).unwrap(),
        );
        let snapshot = get_or_insert_snapshot_lock(&service.repo_locks, TEST_REPO_D);
        let mut client = make_grpc_client_for_service(service).await;
        let mut pushing = client.clone();
        let update = delete_ref_cmd_with_ctx(
            TEST_REPO_D,
            "refs/heads/main",
            &old,
            minimal_push_ctx(TEST_REPO_D),
        );
        let push =
            tokio::spawn(
                async move { pushing.receive_pack(signed_test_stream(vec![update])).await },
            );
        tokio::time::timeout(std::time::Duration::from_secs(5), entered.notified())
            .await
            .unwrap();
        let response = tokio::time::timeout(
            std::time::Duration::from_secs(5),
            client.get_file(signed_test_request(GetFileRequest {
                repository_id: TEST_REPO_D.into(),
                path: "products/p1.md".into(),
                r#ref: "refs/heads/main".into(),
                authorization: test_authorization(TEST_REPO_D, "repository.contents.read"),
                unchanged_since_commit: Some(old.clone()),
                allow_ancestor_receipt: false,
            })),
        )
        .await
        .expect("history proof waited for the paused push validation")
        .unwrap()
        .into_inner();
        assert_eq!(response.ref_commit_sha, old);
        assert!(!response.content.is_empty());
        for service in [Service::GitUploadPack, Service::GitReceivePack] {
            let response = tokio::time::timeout(
                std::time::Duration::from_secs(5),
                client.info_refs(signed_test_request(InfoRefsRequest {
                    repository_id: TEST_REPO_D.into(),
                    service: service as i32,
                    authorization: test_authorization(
                        TEST_REPO_D,
                        if service == Service::GitUploadPack {
                            "repository.contents.read"
                        } else {
                            "repository.contents.write"
                        },
                    ),
                })),
            )
            .await
            .expect("ref advertisement waited for the paused push validation")
            .unwrap()
            .into_inner();
            assert!(String::from_utf8_lossy(&response.advertisement)
                .contains(&format!("{old} refs/heads/main")));
        }
        let read = snapshot.read().await;
        proceed.notify_one();
        tokio::time::timeout(std::time::Duration::from_secs(5), async {
            while snapshot.try_read().is_ok() {
                assert!(!push.is_finished(), "push bypassed a live snapshot reader");
                tokio::task::yield_now().await;
            }
        })
        .await
        .unwrap();
        assert_eq!(
            gix::open(&path).unwrap().head_id().unwrap().to_string(),
            old
        );
        drop(read);
        let report = tokio::time::timeout(std::time::Duration::from_secs(5), push)
            .await
            .unwrap()
            .unwrap()
            .unwrap()
            .into_inner()
            .report_status;
        assert!(String::from_utf8_lossy(&report).contains("ok refs/heads/main"));
        assert!(gix::open(path)
            .unwrap()
            .try_find_reference("refs/heads/main")
            .unwrap()
            .is_none());
    }

    #[tokio::test]
    async fn test_get_file_snapshot_does_not_wait_for_push_preparation() {
        let root = TempDir::new().unwrap();
        repo_with_commit(root.path(), TEST_REPO_D);
        let commit = gix::open(fanout_path(root.path(), TEST_REPO_D).unwrap())
            .unwrap()
            .head_commit()
            .unwrap()
            .id
            .to_string();
        let service = make_test_service(root.path());
        let operation = get_or_insert_lock(&service.repo_locks, TEST_REPO_D);
        let _preparation = operation.write().await;
        for receipt in [false, true] {
            let response = tokio::time::timeout(
                std::time::Duration::from_secs(5),
                service.get_file(signed_test_request(GetFileRequest {
                    repository_id: TEST_REPO_D.into(),
                    path: "products/p1.md".into(),
                    r#ref: "refs/heads/main".into(),
                    authorization: test_authorization(TEST_REPO_D, "repository.contents.read"),
                    unchanged_since_commit: Some(commit.clone()),
                    allow_ancestor_receipt: receipt,
                })),
            )
            .await
            .expect("immutable snapshot waited for a push that has not reached publication")
            .unwrap()
            .into_inner();
            assert_eq!(response.ref_commit_sha, commit);
            assert_eq!(response.at_ancestor, receipt);
            assert_eq!(response.content.is_empty(), receipt);
        }
    }

    #[tokio::test]
    async fn test_commit_file_can_publish_while_an_upload_snapshot_is_live() {
        let root = TempDir::new().unwrap();
        repo_with_commit(root.path(), TEST_REPO_D);
        let service = make_test_service(root.path());
        let operation = get_or_insert_lock(&service.repo_locks, TEST_REPO_D);
        let _upload_guard = operation.read().await;

        let response = tokio::time::timeout(
            std::time::Duration::from_secs(5),
            service.commit_file(signed_test_request(CommitFileRequest {
                repository_id: TEST_REPO_D.into(),
                path: "notes/concurrent.txt".into(),
                content: b"immutable upload objects remain available".to_vec(),
                commit_message: "concurrent backing write".into(),
                authorization: test_authorization(TEST_REPO_D, "repository.contents.write"),
                ..Default::default()
            })),
        )
        .await
        .expect("CommitFile must not wait for an immutable upload snapshot")
        .unwrap();
        let repo = gix::open(fanout_path(root.path(), TEST_REPO_D).unwrap()).unwrap();
        assert_eq!(
            repo.head_commit().unwrap().id().to_string(),
            response.into_inner().commit_sha
        );
    }

    #[tokio::test]
    async fn test_snapshot_gate_excludes_every_repository_mutator() {
        for operation in ["create", "delete", "commit", "delete_file", "tag"] {
            let root = TempDir::new().unwrap();
            if operation != "create" {
                repo_with_commit(root.path(), TEST_REPO_D);
            }
            let service = Arc::new(make_test_service(root.path()));
            let snapshot = get_or_insert_snapshot_lock(&service.repo_locks, TEST_REPO_D);
            let reader = snapshot.read().await;
            let writer = tokio::spawn(async move {
                let authorization = test_authorization(
                    TEST_REPO_D,
                    match operation {
                        "create" => "repository.create",
                        "delete" => "repository.delete",
                        _ => "repository.contents.write",
                    },
                );
                match operation {
                    "create" => service
                        .create_repository(signed_test_request(CreateRepositoryRequest {
                            repository_id: TEST_REPO_D.into(),
                            authorization,
                            ..Default::default()
                        }))
                        .await
                        .map(|_| ()),
                    "delete" => service
                        .delete_repository(signed_test_request(DeleteRepositoryRequest {
                            repository_id: TEST_REPO_D.into(),
                            authorization,
                        }))
                        .await
                        .map(|_| ()),
                    "commit" => service
                        .commit_file(signed_test_request(CommitFileRequest {
                            repository_id: TEST_REPO_D.into(),
                            path: "products/p1.md".into(),
                            content: b"replacement".to_vec(),
                            commit_message: "snapshot gate".into(),
                            authorization,
                            ..Default::default()
                        }))
                        .await
                        .map(|_| ()),
                    "delete_file" => service
                        .delete_file(signed_test_request(DeleteFileRequest {
                            repository_id: TEST_REPO_D.into(),
                            path: "products/p1.md".into(),
                            commit_message: "snapshot gate".into(),
                            authorization,
                            ..Default::default()
                        }))
                        .await
                        .map(|_| ()),
                    "tag" => service
                        .create_tag(signed_test_request(CreateTagRequest {
                            repository_id: TEST_REPO_D.into(),
                            tag_name: "snapshot-gate".into(),
                            authorization,
                            ..Default::default()
                        }))
                        .await
                        .map(|_| ()),
                    _ => unreachable!(),
                }
            });
            tokio::time::timeout(std::time::Duration::from_secs(5), async {
                loop {
                    assert!(
                        !writer.is_finished(),
                        "{operation} bypassed the snapshot gate"
                    );
                    // Tokio prevents another reader once the real mutator queues
                    // its writer, so this observes lock admission, not a delay.
                    if snapshot.try_read().is_err() {
                        break;
                    }
                    tokio::task::yield_now().await;
                }
            })
            .await
            .unwrap();
            drop(reader);
            tokio::time::timeout(std::time::Duration::from_secs(5), writer)
                .await
                .unwrap()
                .unwrap()
                .unwrap();
        }
    }

    #[tokio::test]
    async fn test_get_file_snapshot_waits_for_publication_and_ref_deletion() {
        for delete in [false, true] {
            let root = TempDir::new().unwrap();
            repo_with_commit(root.path(), TEST_REPO_D);
            let repo = gix::open(fanout_path(root.path(), TEST_REPO_D).unwrap()).unwrap();
            let commit = repo.head_commit().unwrap().id().detach();
            let service = make_test_service(root.path());
            let snapshot = get_or_insert_snapshot_lock(&service.repo_locks, TEST_REPO_D);
            let writer = snapshot.write().await;
            let request = signed_test_request(GetFileRequest {
                repository_id: TEST_REPO_D.into(),
                path: "products/p1.md".into(),
                r#ref: "refs/heads/main".into(),
                authorization: test_authorization(TEST_REPO_D, "repository.contents.read"),
                unchanged_since_commit: Some(commit.to_string()),
                allow_ancestor_receipt: true,
            });
            let envelope = request
                .metadata()
                .get(crate::auth::interceptor::SIGNATURE_HEADER)
                .unwrap()
                .to_str()
                .unwrap();
            let fields: Vec<_> = envelope.split(':').collect();
            let nonce = fields[2].to_owned();
            let mut reader = Box::pin(service.get_file(request));
            tokio::time::timeout(std::time::Duration::from_secs(5), async {
                std::future::poll_fn(|cx| {
                    assert!(std::future::Future::poll(reader.as_mut(), cx).is_pending());
                    if service.rpc_auth.has_replay_reservation(&nonce).unwrap() {
                        std::task::Poll::Ready(())
                    } else {
                        cx.waker().wake_by_ref();
                        std::task::Poll::Pending
                    }
                })
                .await;
            })
            .await
            .unwrap();
            let mut published = None;
            if delete {
                repo.find_reference("refs/heads/main")
                    .unwrap()
                    .delete()
                    .unwrap();
            } else {
                let signature = gix::actor::SignatureRef {
                    name: "Test".into(),
                    email: "test@example.com".into(),
                    time: "0 +0000",
                };
                published = Some(
                    repo.commit_as(
                        signature,
                        signature,
                        "refs/heads/main",
                        "concurrent publication",
                        repo.head_commit().unwrap().tree_id().unwrap().detach(),
                        [commit],
                    )
                    .unwrap()
                    .to_string(),
                );
            }
            drop(writer);
            let result = reader.await;
            if delete {
                assert_eq!(result.unwrap_err().code(), tonic::Code::Aborted);
            } else {
                let response = result.unwrap().into_inner();
                assert_eq!(response.ref_commit_sha, published.unwrap());
                assert!(!response.at_ancestor);
                assert!(!response.content.is_empty());
            }
        }
    }

    #[test]
    fn test_get_file_snapshot_cancellation_retains_queued_blocking_guard() {
        let runtime = tokio::runtime::Builder::new_current_thread()
            .enable_all()
            .max_blocking_threads(1)
            .build()
            .unwrap();
        runtime.block_on(async {
            let root = TempDir::new().unwrap();
            repo_with_commit(root.path(), TEST_REPO_D);
            let service = make_test_service(root.path());
            let snapshot = get_or_insert_snapshot_lock(&service.repo_locks, TEST_REPO_D);
            let writer = snapshot.write().await;
            let request = signed_test_request(GetFileRequest {
                repository_id: TEST_REPO_D.into(),
                path: "products/p1.md".into(),
                r#ref: "refs/heads/main".into(),
                authorization: test_authorization(TEST_REPO_D, "repository.contents.read"),
                unchanged_since_commit: None,
                allow_ancestor_receipt: false,
            });
            let envelope = request
                .metadata()
                .get(crate::auth::interceptor::SIGNATURE_HEADER)
                .unwrap()
                .to_str()
                .unwrap();
            let fields: Vec<_> = envelope.split(':').collect();
            let nonce = fields[2].to_owned();
            let mut reader = Box::pin(service.get_file(request));
            tokio::time::timeout(std::time::Duration::from_secs(5), async {
                std::future::poll_fn(|cx| {
                    assert!(std::future::Future::poll(reader.as_mut(), cx).is_pending());
                    if service.rpc_auth.has_replay_reservation(&nonce).unwrap() {
                        std::task::Poll::Ready(())
                    } else {
                        cx.waker().wake_by_ref();
                        std::task::Poll::Pending
                    }
                })
                .await;
            })
            .await
            .unwrap();
            let (release, receive) = std::sync::mpsc::channel::<()>();
            let (started, ready) = tokio::sync::oneshot::channel();
            let blocker = crate::spawn_blocking(move || {
                let _ = started.send(());
                let _ = receive.recv();
            });
            tokio::time::timeout(std::time::Duration::from_secs(5), ready)
                .await
                .unwrap()
                .unwrap();
            drop(writer);
            std::future::poll_fn(|cx| {
                assert!(std::future::Future::poll(reader.as_mut(), cx).is_pending());
                std::task::Poll::Ready(())
            })
            .await;
            drop(reader);
            let retained = snapshot.try_write().is_err();
            drop(release);
            blocker.await.unwrap();
            let writer = tokio::time::timeout(std::time::Duration::from_secs(5), snapshot.write())
                .await
                .unwrap();
            drop(writer);
            assert!(
                retained,
                "cancellation released an accepted snapshot job's guard"
            );
        });
    }

    #[tokio::test]
    async fn test_info_refs_snapshot_does_not_wait_for_push_preparation() {
        let root = TempDir::new().unwrap();
        repo_with_commit(root.path(), TEST_REPO_D);
        let commit = gix::open(fanout_path(root.path(), TEST_REPO_D).unwrap())
            .unwrap()
            .head_commit()
            .unwrap()
            .id
            .to_string();
        let service = make_test_service(root.path());
        let operation = get_or_insert_lock(&service.repo_locks, TEST_REPO_D);
        let _preparation = operation.write().await;
        for kind in [Service::GitUploadPack, Service::GitReceivePack] {
            let response = tokio::time::timeout(
                std::time::Duration::from_secs(5),
                service.info_refs(signed_test_request(InfoRefsRequest {
                    repository_id: TEST_REPO_D.into(),
                    service: kind as i32,
                    authorization: test_authorization(
                        TEST_REPO_D,
                        if kind == Service::GitUploadPack {
                            "repository.contents.read"
                        } else {
                            "repository.contents.write"
                        },
                    ),
                })),
            )
            .await
            .expect("ref advertisement waited for a push before publication")
            .unwrap()
            .into_inner();
            assert_eq!(response.service, kind as i32);
            assert!(String::from_utf8_lossy(&response.advertisement)
                .contains(&format!("{commit} refs/heads/main")));
        }
        let denied = service
            .info_refs(signed_test_request(InfoRefsRequest {
                repository_id: TEST_REPO_D.into(),
                service: Service::GitReceivePack as i32,
                authorization: test_authorization(TEST_REPO_D, "repository.contents.read"),
            }))
            .await
            .unwrap_err();
        assert_eq!(denied.code(), tonic::Code::PermissionDenied);
    }

    #[tokio::test]
    async fn test_info_refs_snapshot_waits_for_publication_and_ref_deletion() {
        for kind in [Service::GitUploadPack, Service::GitReceivePack] {
            for delete in [false, true] {
                let root = TempDir::new().unwrap();
                repo_with_commit(root.path(), TEST_REPO_D);
                let repo = gix::open(fanout_path(root.path(), TEST_REPO_D).unwrap()).unwrap();
                let commit = repo.head_commit().unwrap().id().detach();
                let service = make_test_service(root.path());
                let snapshot = get_or_insert_snapshot_lock(&service.repo_locks, TEST_REPO_D);
                let writer = snapshot.write().await;
                let request = signed_test_request(InfoRefsRequest {
                    repository_id: TEST_REPO_D.into(),
                    service: kind as i32,
                    authorization: test_authorization(
                        TEST_REPO_D,
                        if kind == Service::GitUploadPack {
                            "repository.contents.read"
                        } else {
                            "repository.contents.write"
                        },
                    ),
                });
                let envelope = request
                    .metadata()
                    .get(crate::auth::interceptor::SIGNATURE_HEADER)
                    .unwrap()
                    .to_str()
                    .unwrap();
                let fields: Vec<_> = envelope.split(':').collect();
                let nonce = fields[2].to_owned();
                let mut reader = Box::pin(service.info_refs(request));
                tokio::time::timeout(std::time::Duration::from_secs(5), async {
                    std::future::poll_fn(|cx| {
                        assert!(std::future::Future::poll(reader.as_mut(), cx).is_pending());
                        if service.rpc_auth.has_replay_reservation(&nonce).unwrap() {
                            std::task::Poll::Ready(())
                        } else {
                            cx.waker().wake_by_ref();
                            std::task::Poll::Pending
                        }
                    })
                    .await;
                })
                .await
                .unwrap();
                let mut published = None;
                if delete {
                    repo.find_reference("refs/heads/main")
                        .unwrap()
                        .delete()
                        .unwrap();
                } else {
                    let signature = gix::actor::SignatureRef {
                        name: "Test".into(),
                        email: "test@example.com".into(),
                        time: "0 +0000",
                    };
                    published = Some(
                        repo.commit_as(
                            signature,
                            signature,
                            "refs/heads/main",
                            "concurrent publication",
                            repo.head_commit().unwrap().tree_id().unwrap().detach(),
                            [commit],
                        )
                        .unwrap()
                        .to_string(),
                    );
                }
                drop(writer);
                let response = reader.await.unwrap().into_inner();
                assert_eq!(response.service, kind as i32);
                let advertisement = String::from_utf8_lossy(&response.advertisement);
                assert!(!advertisement.contains(&format!("{commit} refs/heads/main")));
                if let Some(published) = published {
                    assert!(advertisement.contains(&format!("{published} refs/heads/main")));
                }
            }
        }
    }

    #[test]
    fn test_info_refs_snapshot_cancellation_retains_queued_blocking_guard() {
        let runtime = tokio::runtime::Builder::new_current_thread()
            .enable_all()
            .max_blocking_threads(1)
            .build()
            .unwrap();
        runtime.block_on(async {
            for kind in [Service::GitUploadPack, Service::GitReceivePack] {
                let root = TempDir::new().unwrap();
                repo_with_commit(root.path(), TEST_REPO_D);
                let service = make_test_service(root.path());
                let snapshot = get_or_insert_snapshot_lock(&service.repo_locks, TEST_REPO_D);
                let writer = snapshot.write().await;
                let request = signed_test_request(InfoRefsRequest {
                    repository_id: TEST_REPO_D.into(),
                    service: kind as i32,
                    authorization: test_authorization(
                        TEST_REPO_D,
                        if kind == Service::GitUploadPack {
                            "repository.contents.read"
                        } else {
                            "repository.contents.write"
                        },
                    ),
                });
                let envelope = request
                    .metadata()
                    .get(crate::auth::interceptor::SIGNATURE_HEADER)
                    .unwrap()
                    .to_str()
                    .unwrap();
                let fields: Vec<_> = envelope.split(':').collect();
                let nonce = fields[2].to_owned();
                let mut reader = Box::pin(service.info_refs(request));
                tokio::time::timeout(std::time::Duration::from_secs(5), async {
                    std::future::poll_fn(|cx| {
                        assert!(std::future::Future::poll(reader.as_mut(), cx).is_pending());
                        if service.rpc_auth.has_replay_reservation(&nonce).unwrap() {
                            std::task::Poll::Ready(())
                        } else {
                            cx.waker().wake_by_ref();
                            std::task::Poll::Pending
                        }
                    })
                    .await;
                })
                .await
                .unwrap();
                let (release, receive) = std::sync::mpsc::channel::<()>();
                let (started, ready) = tokio::sync::oneshot::channel();
                let blocker = crate::spawn_blocking(move || {
                    let _ = started.send(());
                    let _ = receive.recv();
                });
                tokio::time::timeout(std::time::Duration::from_secs(5), ready)
                    .await
                    .unwrap()
                    .unwrap();
                drop(writer);
                std::future::poll_fn(|cx| {
                    assert!(std::future::Future::poll(reader.as_mut(), cx).is_pending());
                    std::task::Poll::Ready(())
                })
                .await;
                drop(reader);
                let retained = snapshot.try_write().is_err();
                drop(release);
                blocker.await.unwrap();
                let writer =
                    tokio::time::timeout(std::time::Duration::from_secs(5), snapshot.write())
                        .await
                        .unwrap();
                drop(writer);
                assert!(
                    retained,
                    "cancelled advertisement released an accepted snapshot job's guard"
                );
            }
        });
    }

    #[tokio::test]
    async fn test_get_file_happy_path() {
        let dir = TempDir::new().unwrap();
        repo_with_commit(dir.path(), TEST_REPO_D);

        let phases = [
            "authentication",
            "repository_lock",
            "blocking_queue",
            "payload",
        ];
        let counts = phases.map(|phase| {
            super::super::metrics::file_read_phase_seconds()
                .with_label_values(&[phase])
                .get_sample_count()
        });
        let svc = make_test_service(dir.path());
        let req = signed_test_request(GetFileRequest {
            repository_id: TEST_REPO_D.to_string(),
            path: "products/p1.md".to_string(),
            r#ref: "HEAD".to_string(),
            authorization: test_authorization(TEST_REPO_D, "repository.contents.read"),
            unchanged_since_commit: None,
            allow_ancestor_receipt: false,
        });
        let resp = svc.get_file(req).await.unwrap();
        for key in [
            "x-gitstore-file-read-authentication-ns",
            "x-gitstore-file-read-repository-lock-ns",
            "x-gitstore-file-read-blocking-queue-ns",
            "x-gitstore-file-read-payload-ns",
            "x-gitstore-file-read-repository-open-ns",
            "x-gitstore-file-read-revision-ns",
            "x-gitstore-file-read-target-tree-ns",
            "x-gitstore-file-read-blob-ns",
        ] {
            let duration = resp.metadata().get(key).unwrap().to_str().unwrap();
            assert!(duration.parse::<u64>().unwrap() > 0);
        }
        for key in [
            "x-gitstore-file-read-history-commit-ns",
            "x-gitstore-file-read-history-tree-ns",
            "x-gitstore-file-read-history-commits",
        ] {
            assert_eq!(resp.metadata().get(key).unwrap(), "0");
        }
        assert_eq!(resp.into_inner().content, b"---\nid: p1\n---\nhello");
        for (phase, before) in phases.into_iter().zip(counts) {
            assert!(
                super::super::metrics::file_read_phase_seconds()
                    .with_label_values(&[phase])
                    .get_sample_count()
                    > before,
                "GetFile must record its {phase} phase"
            );
        }
    }

    #[tokio::test]
    async fn test_get_file_unchanged_history_proof() {
        let dir = TempDir::new().unwrap();
        repo_with_commit(dir.path(), TEST_REPO_D);
        let repo = gix::open(fanout_path(dir.path(), TEST_REPO_D).unwrap()).unwrap();
        let original = resolve_ref_to_commit_id(&repo, "HEAD").unwrap();
        let svc = make_test_service(dir.path());
        let content = b"---\nid: p1\n---\nhello";
        let write = |path: &str, body: &[u8]| {
            signed_test_request(CommitFileRequest {
                repository_id: TEST_REPO_D.to_string(),
                path: path.to_string(),
                content: body.to_vec(),
                commit_message: "proof fixture".to_string(),
                author_name: "test".to_string(),
                author_email: "test@example.com".to_string(),
                authorization: test_authorization(TEST_REPO_D, "repository.contents.write"),
            })
        };
        svc.commit_file(write("products/other.md", b"unrelated"))
            .await
            .unwrap();
        let read = |ancestor: String| {
            signed_test_request(GetFileRequest {
                repository_id: TEST_REPO_D.to_string(),
                path: "products/p1.md".to_string(),
                r#ref: "HEAD".to_string(),
                authorization: test_authorization(TEST_REPO_D, "repository.contents.read"),
                unchanged_since_commit: Some(ancestor),
                allow_ancestor_receipt: false,
            })
        };
        let response = svc.get_file(read(original.to_string())).await.unwrap();
        assert_eq!(
            response
                .metadata()
                .get("x-gitstore-file-read-history-commits")
                .unwrap(),
            "2"
        );
        for key in [
            "x-gitstore-file-read-history-commit-ns",
            "x-gitstore-file-read-history-tree-ns",
        ] {
            assert!(
                response
                    .metadata()
                    .get(key)
                    .unwrap()
                    .to_str()
                    .unwrap()
                    .parse::<u64>()
                    .unwrap()
                    > 0
            );
        }
        let result = response.into_inner();
        assert_eq!(result.content, content);
        assert_eq!(result.unchanged_since_commit, original.to_string());
        assert_ne!(result.ref_commit_sha, original.to_string());
        svc.commit_file(write("products/p1.md", b"newer writer"))
            .await
            .unwrap();
        assert_eq!(
            svc.get_file(read(original.to_string()))
                .await
                .unwrap_err()
                .code(),
            tonic::Code::Aborted
        );
        svc.commit_file(write("products/p1.md", content))
            .await
            .unwrap();
        assert_eq!(
            svc.get_file(read(original.to_string()))
                .await
                .unwrap_err()
                .code(),
            tonic::Code::Aborted,
            "restoring identical bytes must not revive an older writer's audit"
        );
        assert_eq!(
            svc.get_file(read("not-an-object-id".to_string()))
                .await
                .unwrap_err()
                .code(),
            tonic::Code::InvalidArgument
        );
    }

    #[tokio::test]
    async fn test_current_file_proof_signed_wire_and_ref_errors() {
        let root = TempDir::new().unwrap();
        repo_with_commit(root.path(), TEST_REPO_D);
        let path = fanout_path(root.path(), TEST_REPO_D).unwrap();
        let repo = gix::open(&path).unwrap();
        let ancestor = repo.head_commit().unwrap().id.to_string();
        let mut client = make_grpc_client(root.path().to_path_buf()).await;
        let request = |reference: &str, repository: &str| {
            signed_test_request(GetFileRequest {
                repository_id: repository.into(),
                path: "products/p1.md".into(),
                r#ref: reference.into(),
                authorization: test_authorization(repository, "repository.contents.read"),
                unchanged_since_commit: Some(ancestor.clone()),
                allow_ancestor_receipt: false,
            })
        };
        let first = client
            .get_file(request("refs/heads/main", TEST_REPO_D))
            .await
            .unwrap();
        for header in [
            "x-gitstore-file-read-history-commit-ns",
            "x-gitstore-file-read-history-tree-ns",
        ] {
            assert_eq!(first.metadata().get(header).unwrap().to_str().unwrap(), "0");
        }
        assert_eq!(
            first
                .metadata()
                .get("x-gitstore-file-read-history-commits")
                .unwrap()
                .to_str()
                .unwrap(),
            "1"
        );
        let first = first.into_inner();
        assert_eq!(first.ref_commit_sha, ancestor);
        assert_eq!(first.unchanged_since_commit, ancestor);
        client
            .commit_file(signed_test_request(CommitFileRequest {
                repository_id: TEST_REPO_D.into(),
                path: "products/other.md".into(),
                content: b"other path".to_vec(),
                commit_message: "disjoint commit".into(),
                author_name: "Test".into(),
                author_email: "test@example.com".into(),
                authorization: test_authorization(TEST_REPO_D, "repository.contents.write"),
            }))
            .await
            .unwrap();
        let advanced = client
            .get_file(request("refs/heads/main", TEST_REPO_D))
            .await
            .unwrap()
            .into_inner();
        assert_ne!(advanced.ref_commit_sha, ancestor);
        assert_eq!(advanced.content, first.content);
        assert_eq!(advanced.unchanged_since_commit, ancestor);
        assert_eq!(
            client
                .get_file(request("refs/heads/missing", TEST_REPO_D))
                .await
                .unwrap_err()
                .code(),
            tonic::Code::Aborted
        );
        assert_eq!(
            client
                .get_file(request("refs/heads/main", TEST_REPO_E))
                .await
                .unwrap_err()
                .code(),
            tonic::Code::FailedPrecondition
        );
        std::fs::write(path.join("refs/heads/main"), b"invalid reference\n").unwrap();
        assert_eq!(
            client
                .get_file(request("refs/heads/main", TEST_REPO_D))
                .await
                .unwrap_err()
                .code(),
            tonic::Code::Internal,
            "corrupt ref storage cannot be treated as superseded work"
        );
    }

    #[tokio::test]
    async fn test_current_file_ancestor_receipt_signed_and_bounded_history() {
        let root = TempDir::new().unwrap();
        repo_with_commit(root.path(), TEST_REPO_D);
        let path = fanout_path(root.path(), TEST_REPO_D).unwrap();
        let repo = gix::open(&path).unwrap();
        let ancestor = repo.head_commit().unwrap().id.to_string();
        let mut client = make_grpc_client(root.path().to_path_buf()).await;
        let request = |file: &str| GetFileRequest {
            repository_id: TEST_REPO_D.into(),
            path: file.into(),
            r#ref: "refs/heads/main".into(),
            authorization: test_authorization(TEST_REPO_D, "repository.contents.read"),
            unchanged_since_commit: Some(ancestor.clone()),
            allow_ancestor_receipt: true,
        };
        let receipt = client
            .get_file(signed_test_request(request("products/p1.md")))
            .await
            .unwrap();
        for header in [
            "x-gitstore-file-read-history-commit-ns",
            "x-gitstore-file-read-history-tree-ns",
            "x-gitstore-file-read-target-tree-ns",
            "x-gitstore-file-read-blob-ns",
        ] {
            assert_eq!(
                receipt.metadata().get(header).unwrap().to_str().unwrap(),
                "0"
            );
        }
        let receipt = receipt.into_inner();
        assert!(receipt.at_ancestor);
        assert_eq!(receipt.ref_commit_sha, ancestor);
        assert_eq!(receipt.unchanged_since_commit, ancestor);
        assert_eq!(receipt.path, "products/p1.md");
        assert!(receipt.content.is_empty());
        assert!(receipt.blob_sha.is_empty());
        assert_eq!(receipt.size_bytes, 0);
        assert!(
            client
                .get_file(signed_test_request(request("products/missing.md")))
                .await
                .unwrap()
                .into_inner()
                .at_ancestor,
            "a ref-identity receipt does not claim that the path exists"
        );

        let mut ordinary = request("products/p1.md");
        ordinary.allow_ancestor_receipt = false;
        let ordinary = client
            .get_file(signed_test_request(ordinary))
            .await
            .unwrap()
            .into_inner();
        assert!(!ordinary.at_ancestor);
        assert!(!ordinary.content.is_empty());
        let mut tampered = signed_test_request(request("products/p1.md"));
        tampered.get_mut().allow_ancestor_receipt = false;
        assert_eq!(
            client.get_file(tampered).await.unwrap_err().code(),
            tonic::Code::Unauthenticated
        );
        for mut invalid in [
            GetFileRequest {
                unchanged_since_commit: None,
                ..request("products/p1.md")
            },
            GetFileRequest {
                r#ref: "HEAD".into(),
                ..request("products/p1.md")
            },
            GetFileRequest {
                unchanged_since_commit: Some("bad".into()),
                ..request("products/p1.md")
            },
            request(""),
            request("../outside"),
        ] {
            invalid.allow_ancestor_receipt = true;
            assert_eq!(
                client
                    .get_file(signed_test_request(invalid))
                    .await
                    .unwrap_err()
                    .code(),
                tonic::Code::InvalidArgument
            );
        }
        let mut denied = request("products/p1.md");
        denied.authorization = test_authorization(TEST_REPO_D, "repository.contents.write");
        assert_eq!(
            client
                .get_file(signed_test_request(denied))
                .await
                .unwrap_err()
                .code(),
            tonic::Code::PermissionDenied
        );
        let write = |file: &str, content: Vec<u8>| {
            signed_test_request(CommitFileRequest {
                repository_id: TEST_REPO_D.into(),
                path: file.into(),
                content,
                commit_message: "receipt history".into(),
                author_name: "Test".into(),
                author_email: "test@example.com".into(),
                authorization: test_authorization(TEST_REPO_D, "repository.contents.write"),
            })
        };
        client
            .commit_file(write("products/other.md", b"unrelated".to_vec()))
            .await
            .unwrap();
        let advanced = client
            .get_file(signed_test_request(request("products/p1.md")))
            .await
            .unwrap()
            .into_inner();
        assert!(!advanced.at_ancestor);
        assert_ne!(advanced.ref_commit_sha, ancestor);
        assert_eq!(advanced.content, ordinary.content);
        client
            .commit_file(write("products/p1.md", b"changed".to_vec()))
            .await
            .unwrap();
        client
            .commit_file(write("products/p1.md", ordinary.content))
            .await
            .unwrap();
        assert_eq!(
            client
                .get_file(signed_test_request(request("products/p1.md")))
                .await
                .unwrap_err()
                .code(),
            tonic::Code::Aborted
        );
        let mut missing = request("products/p1.md");
        missing.r#ref = "refs/heads/missing".into();
        assert_eq!(
            client
                .get_file(signed_test_request(missing))
                .await
                .unwrap_err()
                .code(),
            tonic::Code::Aborted
        );
        std::fs::write(path.join("refs/heads/main"), b"invalid reference\n").unwrap();
        assert_eq!(
            client
                .get_file(signed_test_request(request("products/p1.md")))
                .await
                .unwrap_err()
                .code(),
            tonic::Code::Internal
        );
    }

    #[test]
    fn test_file_lookup_rejects_corrupt_trailing_tree_entries() {
        use gix::objs::Write;

        let dir = TempDir::new().unwrap();
        repo_with_commit(dir.path(), TEST_REPO_D);
        let repo = gix::open(fanout_path(dir.path(), TEST_REPO_D).unwrap()).unwrap();
        let original = resolve_ref_to_commit_id(&repo, "HEAD").unwrap();
        let tree = repo
            .find_object(original)
            .unwrap()
            .try_into_commit()
            .unwrap()
            .tree()
            .unwrap();
        let mut data = tree.data.clone();
        data.extend_from_slice(b"malformed trailing entry");
        let corrupt = repo
            .objects
            .write_buf(gix::objs::Kind::Tree, &data)
            .unwrap();
        for path in ["products/p1.md", "products/missing.md", "missing.md"] {
            assert_eq!(
                find_blob_in_tree(&repo, corrupt, path).unwrap_err().code(),
                tonic::Code::Internal,
                "a matching or missing prefix must not hide malformed trailing tree data"
            );
        }
    }

    #[test]
    #[ignore = "native history read diagnostic, not an acceptance gate"]
    fn test_file_history_wide_tree_diagnostic() {
        let dir = TempDir::new().unwrap();
        repo_with_commit(dir.path(), TEST_REPO_D);
        let repo = gix::open(fanout_path(dir.path(), TEST_REPO_D).unwrap()).unwrap();
        let original = resolve_ref_to_commit_id(&repo, "HEAD").unwrap();
        let tree = repo
            .find_object(original)
            .unwrap()
            .try_into_commit()
            .unwrap()
            .tree()
            .unwrap();
        let decoded = tree.decode().unwrap();
        let products = decoded
            .entries
            .iter()
            .find(|entry| entry.filename == "products")
            .unwrap()
            .oid
            .to_owned();
        let blob = find_blob_in_tree(&repo, tree.id, "products/p1.md").unwrap();
        let mut entries: Vec<_> = (0..5_000)
            .map(|index| gix::objs::tree::Entry {
                mode: gix::objs::tree::EntryKind::Blob.into(),
                filename: format!("fixture-{index:05}").into(),
                oid: blob,
            })
            .collect();
        entries.push(gix::objs::tree::Entry {
            mode: gix::objs::tree::EntryKind::Tree.into(),
            filename: "products".into(),
            oid: products,
        });
        let sig = gix::actor::Signature {
            name: "test".into(),
            email: "test@example.com".into(),
            time: gix::date::Time::now_local_or_utc(),
        };
        let mut time_buf = gix::date::parse::TimeBuf::default();
        let sig_ref = sig.to_ref(&mut time_buf);
        let mut tip = original;
        for index in 0..96 {
            entries[0].oid = repo
                .write_blob(format!("unrelated {index}"))
                .unwrap()
                .detach();
            let tree = repo
                .write_object(&gix::objs::Tree {
                    entries: entries.clone(),
                })
                .unwrap()
                .detach();
            tip = repo
                .commit_as(
                    sig_ref,
                    sig_ref,
                    "refs/heads/wide-diagnostic",
                    format!("unrelated {index}"),
                    tree,
                    [tip],
                )
                .unwrap()
                .detach();
        }
        for round in 0..5 {
            let started = std::time::Instant::now();
            let work = verify_unchanged_file_history(&repo, tip, original, "products/p1.md", blob)
                .unwrap();
            assert_eq!(work.commits, 97);
            println!(
                "history_wide_tree round={round} commits=97 entries=5001 elapsed_us={} commit_us={} tree_us={}",
                started.elapsed().as_micros(),
                work.commit_reads.as_micros(),
                work.tree_reads.as_micros()
            );
        }
    }

    #[test]
    fn test_unchanged_history_rejects_unrelated_merges_and_exhausted_walks() {
        let dir = TempDir::new().unwrap();
        repo_with_commit(dir.path(), TEST_REPO_D);
        let repo = gix::open(fanout_path(dir.path(), TEST_REPO_D).unwrap()).unwrap();
        let original = resolve_ref_to_commit_id(&repo, "HEAD").unwrap();
        let tree = repo
            .find_object(original)
            .unwrap()
            .try_into_commit()
            .unwrap()
            .tree_id()
            .unwrap()
            .detach();
        let blob = find_blob_in_tree(&repo, tree, "products/p1.md").unwrap();
        let sig = gix::actor::Signature {
            name: "test".into(),
            email: "test@example.com".into(),
            time: gix::date::Time::now_local_or_utc(),
        };
        let mut time_buf = gix::date::parse::TimeBuf::default();
        let sig_ref = sig.to_ref(&mut time_buf);
        let make_commit = |parents: Vec<gix::ObjectId>, message: &str| {
            repo.commit_as(
                sig_ref,
                sig_ref,
                format!("refs/heads/proof-{}", message.replace(' ', "-")),
                message,
                tree,
                parents,
            )
            .unwrap()
            .detach()
        };
        let unrelated = make_commit(vec![], "unrelated root");
        assert_eq!(
            verify_unchanged_file_history(&repo, unrelated, original, "products/p1.md", blob)
                .unwrap_err()
                .code(),
            tonic::Code::Aborted
        );
        let merge = make_commit(vec![original, unrelated], "merge");
        assert_eq!(
            verify_unchanged_file_history(&repo, merge, original, "products/p1.md", blob)
                .unwrap_err()
                .code(),
            tonic::Code::Aborted
        );
        let mut tip = original;
        for n in 0..=DELETE_RECEIPT_HISTORY_LIMIT {
            tip = make_commit(vec![tip], &format!("disjoint {n}"));
        }
        let error = verify_unchanged_file_history(&repo, tip, original, "products/p1.md", blob)
            .unwrap_err();
        assert!(matches!(
            error.code(),
            tonic::Code::Aborted | tonic::Code::ResourceExhausted
        ));
    }

    #[tokio::test]
    async fn test_get_file_unknown_ref_returns_failed_precondition() {
        let dir = TempDir::new().unwrap();
        repo_with_commit(dir.path(), TEST_REPO_D);

        let svc = make_test_service(dir.path());
        let req = signed_test_request(GetFileRequest {
            repository_id: TEST_REPO_D.to_string(),
            path: "products/p1.md".to_string(),
            r#ref: "nonexistent-branch".to_string(),
            authorization: test_authorization(TEST_REPO_D, "repository.contents.read"),
            unchanged_since_commit: None,
            allow_ancestor_receipt: false,
        });
        let err = svc.get_file(req).await.unwrap_err();
        assert_eq!(err.code(), tonic::Code::FailedPrecondition);
    }

    #[tokio::test]
    async fn test_get_file_missing_file_returns_not_found() {
        let dir = TempDir::new().unwrap();
        repo_with_commit(dir.path(), TEST_REPO_D);

        let svc = make_test_service(dir.path());
        let req = signed_test_request(GetFileRequest {
            repository_id: TEST_REPO_D.to_string(),
            path: "products/nonexistent.md".to_string(),
            r#ref: "HEAD".to_string(),
            authorization: test_authorization(TEST_REPO_D, "repository.contents.read"),
            unchanged_since_commit: None,
            allow_ancestor_receipt: false,
        });
        let err = svc.get_file(req).await.unwrap_err();
        assert_eq!(err.code(), tonic::Code::NotFound);
    }

    #[test]
    fn test_full_object_id_resolution_preserves_refs_tags_and_storage_errors() {
        let dir = TempDir::new().unwrap();
        repo_with_commit(dir.path(), TEST_REPO_D);
        let path = fanout_path(dir.path(), TEST_REPO_D).unwrap();
        let repo = gix::open(&path).unwrap();
        let commit = resolve_ref_to_commit_id(&repo, "HEAD").unwrap();
        let hex = commit.to_string();
        let tag = repo.rev_parse_single("refs/tags/v1.0.0").unwrap().detach();
        for reference in [
            hex.as_str(),
            &hex.to_uppercase(),
            &hex[..7],
            "HEAD",
            "refs/heads/main",
            "v1.0.0",
            &tag.to_string(),
        ] {
            assert_eq!(resolve_ref_to_commit_id(&repo, reference).unwrap(), commit);
        }
        assert_eq!(
            resolve_ref_to_commit_id(&repo, &"0".repeat(40))
                .unwrap_err()
                .code(),
            tonic::Code::NotFound
        );
        let blob = repo.write_blob(b"not a commit").unwrap().detach();
        assert_eq!(
            resolve_ref_to_commit_id(&repo, &blob.to_string())
                .unwrap_err()
                .code(),
            tonic::Code::Internal
        );
        let tree = repo.find_commit(commit).unwrap().tree().unwrap().id;
        assert_eq!(
            resolve_ref_to_commit_id(&repo, &tree.to_string())
                .unwrap_err()
                .code(),
            tonic::Code::Internal
        );
        let hash_named_tag = format!("refs/tags/{hex}");
        repo.reference(
            hash_named_tag.as_str(),
            blob,
            gix::refs::transaction::PreviousValue::MustNotExist,
            "full object IDs remain immutable targets",
        )
        .unwrap();
        assert_eq!(resolve_ref_to_commit_id(&repo, &hex).unwrap(), commit);
        assert_eq!(
            resolve_ref_to_commit_id(&repo, &hash_named_tag)
                .unwrap_err()
                .code(),
            tonic::Code::Internal,
            "qualified hash-looking refs must still resolve as refs"
        );
        let object_path = path.join("objects").join(&hex[..2]).join(&hex[2..]);
        std::fs::remove_file(&object_path).unwrap();
        std::fs::write(object_path, b"unreadable compressed commit").unwrap();
        let repo = gix::open(path).unwrap();
        assert_eq!(
            resolve_ref_to_commit_id(&repo, &hex).unwrap_err().code(),
            tonic::Code::Internal,
            "corrupt storage must not become a missing ref or a cached success"
        );
    }

    #[tokio::test]
    async fn test_file_reads_distinguish_absence_from_unavailable_target() {
        use tokio_stream::StreamExt;

        let dir = TempDir::new().unwrap();
        repo_with_commit(dir.path(), TEST_REPO_D);
        let svc = make_test_service(dir.path());
        for (repository_id, reference, path, code) in [
            (
                TEST_REPO_A,
                "HEAD",
                "products/p1.md",
                tonic::Code::FailedPrecondition,
            ),
            (
                TEST_REPO_D,
                "missing-ref",
                "products/p1.md",
                tonic::Code::FailedPrecondition,
            ),
            (
                TEST_REPO_D,
                "0000000000000000000000000000000000000000",
                "products/p1.md",
                tonic::Code::FailedPrecondition,
            ),
            (
                TEST_REPO_D,
                "HEAD",
                "products/missing.md",
                tonic::Code::NotFound,
            ),
            (
                TEST_REPO_D,
                "HEAD",
                "products",
                tonic::Code::FailedPrecondition,
            ),
        ] {
            let err = svc
                .get_file(signed_test_request(GetFileRequest {
                    repository_id: repository_id.to_string(),
                    path: path.to_string(),
                    r#ref: reference.to_string(),
                    authorization: test_authorization(repository_id, "repository.contents.read"),
                    unchanged_since_commit: None,
                    allow_ancestor_receipt: false,
                }))
                .await
                .unwrap_err();
            assert_eq!(err.code(), code);

            let err = match svc
                .get_file_stream(signed_test_request(GetFileStreamRequest {
                    repository_id: repository_id.to_string(),
                    path: path.to_string(),
                    r#ref: reference.to_string(),
                    authorization: test_authorization(repository_id, "repository.contents.read"),
                }))
                .await
            {
                Err(err) => err,
                Ok(response) => response.into_inner().next().await.unwrap().unwrap_err(),
            };
            assert_eq!(err.code(), code);
        }
    }

    #[tokio::test]
    async fn test_list_files_returns_tree_entries() {
        let dir = TempDir::new().unwrap();
        repo_with_commit(dir.path(), TEST_REPO_D);

        let svc = make_test_service(dir.path());
        let req = signed_test_request(ListFilesRequest {
            repository_id: TEST_REPO_D.to_string(),
            r#ref: "HEAD".to_string(),
            path_prefix: "products/".to_string(),
            recursive: true,
            authorization: test_authorization(TEST_REPO_D, "repository.contents.read"),
        });
        let resp = svc.list_files(req).await.unwrap();
        let files = resp.into_inner().files;
        assert_eq!(files.len(), 1);
        assert_eq!(files[0].path, "products/p1.md");
    }

    #[tokio::test]
    async fn test_get_latest_tag_returns_correct_tag() {
        let dir = TempDir::new().unwrap();
        repo_with_commit(dir.path(), TEST_REPO_D);

        let svc = make_test_service(dir.path());
        let req = signed_test_request(GetLatestTagRequest {
            repository_id: TEST_REPO_D.to_string(),
            prefix: "v".to_string(),
            authorization: test_authorization(TEST_REPO_D, "repository.contents.read"),
        });
        let resp = svc.get_latest_tag(req).await.unwrap().into_inner();

        assert!(resp.found);
        let tag = resp.tag.unwrap();
        assert_eq!(tag.name, "v1.0.0");
        assert!(!tag.commit_sha.is_empty());
    }

    #[tokio::test]
    async fn test_get_latest_tag_empty_repo_returns_found_false() {
        let dir = TempDir::new().unwrap();
        // Create an empty repo directly at the fanout path
        let repo_path = fanout_path(dir.path(), TEST_REPO_A).unwrap();
        std::fs::create_dir_all(repo_path.parent().unwrap()).unwrap();
        gix::init_bare(&repo_path).unwrap();

        let svc = make_test_service(dir.path());
        let req = signed_test_request(GetLatestTagRequest {
            repository_id: TEST_REPO_A.to_string(),
            prefix: "v".to_string(),
            authorization: test_authorization(TEST_REPO_A, "repository.contents.read"),
        });
        let resp = svc.get_latest_tag(req).await.unwrap().into_inner();

        assert!(!resp.found);
        assert!(resp.tag.is_none());
    }

    #[test]
    fn test_cmp_semver_str() {
        use std::cmp::Ordering::*;
        assert_eq!(cmp_semver_str("1.0.0", "1.0.0"), Equal);
        assert_eq!(cmp_semver_str("1.0.0", "1.10.0"), Less);
        assert_eq!(cmp_semver_str("2.0.0", "1.9.9"), Greater);
        assert_eq!(cmp_semver_str("1.2.3", "1.2.10"), Less);
    }

    #[tokio::test]
    async fn test_commit_file_creates_real_commit() {
        let dir = TempDir::new().unwrap();
        bare_repo_with_main(dir.path(), TEST_REPO_E);

        let svc = make_test_service(dir.path());
        let req = signed_test_request(CommitFileRequest {
            repository_id: TEST_REPO_E.to_string(),
            path: "products/new.md".to_string(),
            content: b"---\nid: new\n---".to_vec(),
            commit_message: "add new product".to_string(),
            author_name: "Tester".to_string(),
            author_email: "test@example.com".to_string(),
            authorization: test_authorization(TEST_REPO_E, "repository.contents.write"),
        });
        let resp = svc.commit_file(req).await.unwrap().into_inner();
        assert!(!resp.commit_sha.is_empty());
        assert_eq!(resp.branch, "main");

        // Verify the file appears in the bare repo at HEAD
        let repo_path = fanout_path(dir.path(), TEST_REPO_E).unwrap();
        let repo = gix::open(repo_path).unwrap();
        let commit_id = resolve_ref_to_commit_id(&repo, "HEAD").unwrap();
        let tree_id = repo
            .find_object(commit_id)
            .unwrap()
            .try_into_commit()
            .unwrap()
            .tree_id()
            .unwrap()
            .detach();
        find_blob_in_tree(&repo, tree_id, "products/new.md").unwrap();
    }

    #[derive(Clone, Debug, Default)]
    struct CapturedSpans(Arc<std::sync::Mutex<Vec<opentelemetry_sdk::trace::SpanData>>>);

    fn enable_commit_reflog(repo_path: &Path) {
        let path = repo_path.join("config");
        let mut config = std::fs::read_to_string(&path).unwrap();
        config.push_str("\n[core]\nlogAllRefUpdates = true\n");
        std::fs::write(path, config).unwrap();
    }

    #[tokio::test(flavor = "multi_thread", worker_threads = 4)]
    async fn test_commit_file_ready_cohorts_preserve_each_authored_commit() {
        for (writes, same_path, cancelled) in [
            (8_usize, false, None),
            (8, true, None),
            (40, false, None),
            (8, false, Some(0)),
            (8, true, Some(3)),
        ] {
            let root = TempDir::new().unwrap();
            bare_repo_with_main(root.path(), TEST_REPO_E);
            let repo_path = fanout_path(root.path(), TEST_REPO_E).unwrap();
            enable_commit_reflog(&repo_path);
            let base = gix::open(&repo_path)
                .unwrap()
                .head_commit()
                .unwrap()
                .id()
                .detach();
            let service = Arc::new(make_test_service(root.path()));
            let snapshot = get_or_insert_snapshot_lock(&service.repo_locks, TEST_REPO_E);
            let writer = snapshot.write_owned().await;
            let queue = service
                .repo_locks
                .entry(TEST_REPO_E.into())
                .or_default()
                .commits
                .clone();
            let mut calls = Vec::new();
            for number in 0..writes {
                let path = if same_path {
                    "products/shared.md".to_string()
                } else {
                    format!("products/{number}.md")
                };
                let request = signed_test_request(CommitFileRequest {
                    repository_id: TEST_REPO_E.into(),
                    path: path.clone(),
                    content: format!("content {number}").into_bytes(),
                    author_name: format!("Author {number}"),
                    author_email: format!("author{number}@example.invalid"),
                    commit_message: format!("operation {number}"),
                    authorization: test_authorization(TEST_REPO_E, "repository.contents.write"),
                });
                let caller = service.clone();
                calls.push((
                    number,
                    path,
                    tokio::spawn(
                        async move { caller.commit_file(request).await.unwrap().into_inner() },
                    ),
                ));
                tokio::time::timeout(std::time::Duration::from_secs(5), async {
                    while queue.pending.lock().await.entries.len() != number + 1 {
                        tokio::task::yield_now().await;
                    }
                })
                .await
                .unwrap();
            }
            let invalid = service
                .commit_file(signed_test_request(CommitFileRequest {
                    repository_id: TEST_REPO_E.into(),
                    path: "../rejected.md".into(),
                    content: b"invalid".to_vec(),
                    authorization: test_authorization(TEST_REPO_E, "repository.contents.write"),
                    ..Default::default()
                }))
                .await
                .unwrap_err();
            assert_eq!(invalid.code(), tonic::Code::InvalidArgument);
            assert_eq!(queue.pending.lock().await.entries.len(), writes);
            if let Some(index) = cancelled {
                calls[index].2.abort();
                tokio::time::timeout(std::time::Duration::from_secs(5), async {
                    while queue.pending.lock().await.entries.len() != writes - 1 {
                        tokio::task::yield_now().await;
                    }
                })
                .await
                .unwrap();
            }
            drop(writer);
            let mut commits = Vec::new();
            for (number, path, call) in calls {
                if Some(number) == cancelled {
                    assert!(call.await.unwrap_err().is_cancelled());
                    continue;
                }
                commits.push((number, path, call.await.unwrap().commit_sha));
            }
            let repo = gix::open(&repo_path).unwrap();
            let mut parent = base;
            for (number, path, sha) in &commits {
                let id = gix::ObjectId::from_hex(sha.as_bytes()).unwrap();
                let commit = repo.find_object(id).unwrap().try_into_commit().unwrap();
                let decoded = commit.decode().unwrap();
                assert_eq!(decoded.parents().collect::<Vec<_>>(), vec![parent]);
                assert_eq!(
                    decoded.author().unwrap().name,
                    format!("Author {number}").as_str()
                );
                assert_eq!(decoded.message, format!("operation {number}").as_str());
                let blob =
                    find_blob_in_tree(&repo, commit.tree_id().unwrap().detach(), path).unwrap();
                assert_eq!(
                    repo.find_object(blob).unwrap().data,
                    format!("content {number}").as_bytes()
                );
                parent = id;
            }
            assert_eq!(repo.head_commit().unwrap().id().detach(), parent);
            let reflog = std::fs::read_to_string(repo_path.join("logs/refs/heads/main")).unwrap();
            assert_eq!(
                reflog.lines().count(),
                commits.len().div_ceil(COMMIT_FILE_COHORT_LIMIT)
            );
        }
    }

    #[tokio::test]
    async fn test_commit_file_cohort_initializes_unborn_head_and_rejects_corruption() {
        let root = TempDir::new().unwrap();
        let repo_path = fanout_path(root.path(), TEST_REPO_E).unwrap();
        std::fs::create_dir_all(repo_path.parent().unwrap()).unwrap();
        gix::init_bare(&repo_path).unwrap();
        std::fs::write(repo_path.join("HEAD"), b"ref: refs/heads/main\n").unwrap();
        let service = make_test_service(root.path());
        let request = || {
            signed_test_request(CommitFileRequest {
                repository_id: TEST_REPO_E.into(),
                path: "products/first.md".into(),
                content: b"first".to_vec(),
                authorization: test_authorization(TEST_REPO_E, "repository.contents.write"),
                ..Default::default()
            })
        };
        let response = service.commit_file(request()).await.unwrap().into_inner();
        let repo = gix::open(&repo_path).unwrap();
        let commit = repo.head_commit().unwrap();
        assert_eq!(commit.id().to_string(), response.commit_sha);
        assert_eq!(commit.decode().unwrap().parents().count(), 0);
        std::fs::write(repo_path.join("HEAD"), b"corrupt HEAD\n").unwrap();
        assert_eq!(
            service.commit_file(request()).await.unwrap_err().code(),
            tonic::Code::Internal
        );
        assert_eq!(
            std::fs::read(repo_path.join("HEAD")).unwrap(),
            b"corrupt HEAD\n"
        );
    }

    #[tokio::test]
    async fn test_commit_file_pending_capacity_is_explicit_and_recovers() {
        let root = TempDir::new().unwrap();
        bare_repo_with_main(root.path(), TEST_REPO_E);
        let service = make_test_service(root.path());
        let capacity = service
            .commit_slots
            .clone()
            .acquire_many_owned(COMMIT_FILE_PENDING_LIMIT as u32)
            .await
            .unwrap();
        let request = || {
            signed_test_request(CommitFileRequest {
                repository_id: TEST_REPO_E.into(),
                path: "products/capacity.md".into(),
                content: b"content".to_vec(),
                authorization: test_authorization(TEST_REPO_E, "repository.contents.write"),
                ..Default::default()
            })
        };
        assert_eq!(
            service.commit_file(request()).await.unwrap_err().code(),
            tonic::Code::ResourceExhausted
        );
        drop(capacity);
        service.commit_file(request()).await.unwrap();
    }

    #[tokio::test(flavor = "multi_thread", worker_threads = 4)]
    async fn test_commit_file_cohort_rebuilds_after_actual_ref_conflict() {
        let root = TempDir::new().unwrap();
        bare_repo_with_main(root.path(), TEST_REPO_E);
        let repo_path = fanout_path(root.path(), TEST_REPO_E).unwrap();
        let repo = gix::open(&repo_path).unwrap();
        let head = repo.head_commit().unwrap();
        let base = head.id().detach();
        let blob = repo.write_blob(b"external").unwrap().detach();
        let tree = repo
            .edit_tree(head.tree_id().unwrap().detach())
            .unwrap()
            .upsert(
                "products/external.md",
                gix::object::tree::EntryKind::Blob,
                blob,
            )
            .unwrap()
            .write()
            .unwrap()
            .detach();
        let signature = gix::actor::Signature {
            name: "External".into(),
            email: "external@example.invalid".into(),
            time: gix::date::Time::now_local_or_utc(),
        };
        let external = repo
            .write_object(&gix::objs::Commit {
                tree,
                parents: vec![base].into(),
                author: signature.clone(),
                committer: signature,
                encoding: None,
                message: "external writer".into(),
                extra_headers: vec![],
            })
            .unwrap()
            .detach();
        let ref_path = repo_path.join("refs/heads/main");
        let ref_lock = ref_path.with_extension("lock");
        let held = OpenOptions::new()
            .create_new(true)
            .write(true)
            .open(&ref_lock)
            .unwrap();
        let service = make_test_service(root.path());
        let writer = tokio::spawn(async move {
            service
                .commit_file(signed_test_request(CommitFileRequest {
                    repository_id: TEST_REPO_E.into(),
                    path: "products/cohort.md".into(),
                    content: b"cohort".to_vec(),
                    commit_message: "cohort candidate".into(),
                    authorization: test_authorization(TEST_REPO_E, "repository.contents.write"),
                    ..Default::default()
                }))
                .await
                .unwrap()
                .into_inner()
        });
        tokio::time::timeout(std::time::Duration::from_secs(5), async {
            loop {
                let mut prepared = false;
                for directory in std::fs::read_dir(repo_path.join("objects")).unwrap() {
                    let directory = directory.unwrap();
                    let prefix = directory.file_name().to_string_lossy().into_owned();
                    if prefix.len() != 2 || !directory.file_type().unwrap().is_dir() {
                        continue;
                    }
                    for entry in std::fs::read_dir(directory.path()).unwrap() {
                        let suffix = entry.unwrap().file_name().to_string_lossy().into_owned();
                        if suffix.len() != 38 {
                            continue;
                        }
                        let id = gix::ObjectId::from_hex(format!("{prefix}{suffix}").as_bytes())
                            .unwrap();
                        let object = repo.find_object(id).unwrap();
                        if object.kind == gix::objs::Kind::Commit {
                            let commit = object.try_into_commit().unwrap();
                            let decoded = commit.decode().unwrap();
                            prepared |= decoded.message == "cohort candidate"
                                && decoded.parents().collect::<Vec<_>>() == vec![base];
                        }
                    }
                }
                if prepared {
                    break;
                }
                tokio::task::yield_now().await;
            }
        })
        .await
        .unwrap();
        // The fixture owns the lock; advance its ref before allowing the real CAS.
        std::fs::write(&ref_path, format!("{external}\n")).unwrap();
        drop(held);
        std::fs::remove_file(ref_lock).unwrap();
        let result = writer.await.unwrap();
        let commit = repo
            .find_object(gix::ObjectId::from_hex(result.commit_sha.as_bytes()).unwrap())
            .unwrap()
            .try_into_commit()
            .unwrap();
        assert_eq!(
            commit.decode().unwrap().parents().collect::<Vec<_>>(),
            vec![external]
        );
        let tree = commit.tree_id().unwrap().detach();
        assert!(find_blob_in_tree(&repo, tree, "products/external.md").is_ok());
        assert!(find_blob_in_tree(&repo, tree, "products/cohort.md").is_ok());
    }

    #[tokio::test]
    async fn test_commit_file_cancelled_queue_releases_lifecycle_and_capacity() {
        let root = TempDir::new().unwrap();
        bare_repo_with_main(root.path(), TEST_REPO_E);
        let service = Arc::new(make_test_service(root.path()));
        let snapshot = get_or_insert_snapshot_lock(&service.repo_locks, TEST_REPO_E);
        let held = snapshot.write_owned().await;
        let operation = get_or_insert_lock(&service.repo_locks, TEST_REPO_E);
        let queue = service
            .repo_locks
            .entry(TEST_REPO_E.into())
            .or_default()
            .commits
            .clone();
        let caller = service.clone();
        let call = tokio::spawn(async move {
            caller
                .commit_file(signed_test_request(CommitFileRequest {
                    repository_id: TEST_REPO_E.into(),
                    path: "products/cancelled-queued.md".into(),
                    content: b"must not publish".to_vec(),
                    authorization: test_authorization(TEST_REPO_E, "repository.contents.write"),
                    ..Default::default()
                }))
                .await
        });
        tokio::time::timeout(std::time::Duration::from_secs(5), async {
            while queue.pending.lock().await.entries.len() != 1 {
                tokio::task::yield_now().await;
            }
        })
        .await
        .unwrap();
        call.abort();
        assert!(call.await.unwrap_err().is_cancelled());
        let _exclusive = tokio::time::timeout(std::time::Duration::from_secs(5), operation.write())
            .await
            .unwrap();
        assert_eq!(
            service.commit_slots.available_permits(),
            COMMIT_FILE_PENDING_LIMIT
        );
        assert!(!queue.pending.lock().await.running);
        drop(held);
        let repo = gix::open(fanout_path(root.path(), TEST_REPO_E).unwrap()).unwrap();
        let tree = repo.head_commit().unwrap().tree_id().unwrap().detach();
        assert_eq!(
            find_blob_in_tree(&repo, tree, "products/cancelled-queued.md")
                .unwrap_err()
                .code(),
            tonic::Code::NotFound
        );
    }

    impl opentelemetry_sdk::trace::SpanExporter for CapturedSpans {
        async fn export(
            &self,
            batch: Vec<opentelemetry_sdk::trace::SpanData>,
        ) -> opentelemetry_sdk::error::OTelSdkResult {
            self.0.lock().unwrap().extend(batch);
            Ok(())
        }
    }

    #[test]
    fn test_info_refs_phase_telemetry_separates_snapshot_and_blocking_queue_waits() {
        use opentelemetry::trace::TracerProvider;
        use tracing::Instrument;
        use tracing_subscriber::prelude::*;

        let captured = CapturedSpans::default();
        let provider = opentelemetry_sdk::trace::SdkTracerProvider::builder()
            .with_sampler(opentelemetry_sdk::trace::Sampler::AlwaysOn)
            .with_simple_exporter(captured.clone())
            .build();
        let subscriber = tracing_subscriber::registry().with(
            tracing_opentelemetry::layer().with_tracer(provider.tracer("advertisement phases")),
        );
        let runtime = tokio::runtime::Builder::new_current_thread()
            .enable_all()
            .max_blocking_threads(1)
            .build()
            .unwrap();
        let (snapshot_held_ms, blocking_held_ms) =
            tracing::subscriber::with_default(subscriber, || {
                runtime.block_on(async {
                    let root = TempDir::new().unwrap();
                    repo_with_commit(root.path(), TEST_REPO_D);
                    let service = make_test_service(root.path());
                    let snapshot = get_or_insert_snapshot_lock(&service.repo_locks, TEST_REPO_D);
                    let writer = snapshot.write().await;
                    let request = signed_test_request(InfoRefsRequest {
                        repository_id: TEST_REPO_D.into(),
                        service: Service::GitUploadPack as i32,
                        authorization: test_authorization(TEST_REPO_D, "repository.contents.read"),
                    });
                    let envelope = request
                        .metadata()
                        .get(crate::auth::interceptor::SIGNATURE_HEADER)
                        .unwrap()
                        .to_str()
                        .unwrap();
                    let fields: Vec<_> = envelope.split(':').collect();
                    let nonce = fields[2].to_owned();
                    let mut reader = Box::pin(
                        service
                            .info_refs(request)
                            .instrument(tracing::info_span!("advertisement.phases")),
                    );
                    tokio::time::timeout(std::time::Duration::from_secs(5), async {
                        std::future::poll_fn(|cx| {
                            assert!(std::future::Future::poll(reader.as_mut(), cx).is_pending());
                            if service.rpc_auth.has_replay_reservation(&nonce).unwrap() {
                                std::task::Poll::Ready(())
                            } else {
                                cx.waker().wake_by_ref();
                                std::task::Poll::Pending
                            }
                        })
                        .await;
                    })
                    .await
                    .unwrap();
                    let (release, receive) = std::sync::mpsc::channel::<()>();
                    let (started, ready) = tokio::sync::oneshot::channel();
                    let blocker = crate::spawn_blocking(move || {
                        let _ = started.send(());
                        let _ = receive.recv();
                    });
                    ready.await.unwrap();
                    std::future::poll_fn(|cx| {
                        assert!(std::future::Future::poll(reader.as_mut(), cx).is_pending());
                        std::task::Poll::Ready(())
                    })
                    .await;
                    let held = std::time::Instant::now();
                    tokio::time::sleep(std::time::Duration::from_millis(20)).await;
                    let snapshot_held_ms = held.elapsed().as_secs_f64() * 1000.0;
                    drop(writer);
                    std::future::poll_fn(|cx| {
                        assert!(std::future::Future::poll(reader.as_mut(), cx).is_pending());
                        std::task::Poll::Ready(())
                    })
                    .await;
                    assert!(snapshot.try_write().is_err());
                    let held = std::time::Instant::now();
                    tokio::time::sleep(std::time::Duration::from_millis(20)).await;
                    let blocking_held_ms = held.elapsed().as_secs_f64() * 1000.0;
                    drop(release);
                    blocker.await.unwrap();
                    reader.as_mut().await.unwrap();
                    drop(reader);
                    assert!(snapshot.try_write().is_ok());
                    (snapshot_held_ms, blocking_held_ms)
                })
            });
        provider.force_flush().unwrap();
        let spans = captured.0.lock().unwrap();
        let span = spans
            .iter()
            .find(|span| span.name == "advertisement.phases")
            .unwrap();
        let durations: std::collections::BTreeMap<_, _> = span
            .attributes
            .iter()
            .filter_map(|attribute| match attribute.value {
                opentelemetry::Value::F64(value) => Some((attribute.key.as_str(), value)),
                _ => None,
            })
            .collect();
        assert!(durations["git.advertisement.snapshot_acquisition_ms"] >= snapshot_held_ms);
        assert!(durations["git.advertisement.blocking_queue_ms"] >= blocking_held_ms);
        assert!(durations["git.advertisement.execution_ms"] >= 0.0);
        assert!(
            durations["git.advertisement.execution_ms"]
                < (span
                    .end_time
                    .duration_since(span.start_time)
                    .unwrap()
                    .as_secs_f64()
                    * 1000.0)
        );
    }

    #[test]
    fn test_ref_snapshot_telemetry_correlates_actual_git_objects_without_raw_inputs() {
        use opentelemetry::trace::TracerProvider;
        use tracing_subscriber::prelude::*;

        let captured = CapturedSpans::default();
        let provider = opentelemetry_sdk::trace::SdkTracerProvider::builder()
            .with_sampler(opentelemetry_sdk::trace::Sampler::AlwaysOn)
            .with_simple_exporter(captured.clone())
            .build();
        let subscriber = tracing_subscriber::registry().with(
            tracing_opentelemetry::layer().with_tracer(provider.tracer("ref snapshot contract")),
        );
        let dir = TempDir::new().unwrap();
        bare_repo_with_main(dir.path(), TEST_REPO_E);
        let repo_path = fanout_path(dir.path(), TEST_REPO_E).unwrap();
        let original = gix::open(&repo_path)
            .unwrap()
            .head_id()
            .unwrap()
            .to_string();
        let next = "1".repeat(40);
        let replacement = tracing::subscriber::with_default(subscriber, || {
            let pack = crate::git::pack_server::HttpPackServer::new(repo_path.clone(), 1024 * 1024);
            for (name, upload) in [("advertise.upload", true), ("advertise.receive", false)] {
                tracing::info_span!("ref.snapshot", operation = name).in_scope(|| {
                    let advertised = if upload {
                        pack.advertise_upload_pack_refs().unwrap()
                    } else {
                        pack.advertise_receive_pack_refs().unwrap()
                    };
                    assert!(advertised
                        .windows(original.len())
                        .any(|part| part == original.as_bytes()));
                });
            }
            tracing::info_span!("ref.snapshot", operation = "fetch").in_scope(|| {
                let mut request = Vec::new();
                for line in [
                    format!("want {original}\n"),
                    format!("want {next}\n"),
                    format!("have {original}\n"),
                    format!("have {next}\n"),
                ] {
                    write_pkt_line_buf(&mut request, line.as_bytes()).unwrap();
                }
                request.extend_from_slice(b"0000");
                let mut expected = Vec::new();
                write_pkt_line_buf(&mut expected, format!("ACK {original}\n").as_bytes()).unwrap();
                assert_eq!(pack.handle_upload_pack(&request).unwrap(), expected);
            });
            tracing::info_span!("ref.snapshot", operation = "push").in_scope(|| {
                record_push_ref_snapshot(&[
                    RefCommand {
                        ref_name: "refs/heads/private-marker".into(),
                        old_oid: original.clone(),
                        new_oid: next.clone(),
                    },
                    RefCommand {
                        ref_name: "private-marker".into(),
                        old_oid: "private-marker".into(),
                        new_oid: "private-marker".into(),
                    },
                ]);
            });
            tracing::info_span!("ref.snapshot", operation = "invalid").in_scope(|| {
                record_push_ref_snapshot(&[RefCommand {
                    ref_name: "private-marker".into(),
                    old_oid: "private-marker".into(),
                    new_oid: "private-marker".repeat(100_000),
                }]);
            });
            tracing::info_span!("ref.snapshot", operation = "empty").in_scope(|| {
                record_push_ref_snapshot(&[]);
            });
            tracing::info_span!("ref.snapshot", operation = "empty.fetch").in_scope(|| {
                assert_eq!(pack.handle_upload_pack(b"0000").unwrap(), b"0008NAK\n");
            });
            let runtime = tokio::runtime::Builder::new_current_thread()
                .enable_all()
                .build()
                .unwrap();
            let svc = make_test_service(dir.path());
            let replacement =
                tracing::info_span!("ref.snapshot", operation = "write").in_scope(|| {
                    runtime
                        .block_on(svc.commit_file(signed_test_request(CommitFileRequest {
                            repository_id: TEST_REPO_E.into(),
                            path: "private-marker.md".into(),
                            content: b"private-marker".to_vec(),
                            commit_message: "private-marker".into(),
                            author_name: "private-marker".into(),
                            author_email: "private-marker".into(),
                            authorization: test_authorization(
                                TEST_REPO_E,
                                "repository.contents.write",
                            ),
                        })))
                        .unwrap()
                        .into_inner()
                        .commit_sha
                });
            tracing::info_span!("ref.snapshot", operation = "validate").in_scope(|| {
                let span = tracing::Span::current();
                let dispatch = tracing::dispatcher::get_default(Clone::clone);
                let repo_path = repo_path.clone();
                let expected = original.clone();
                std::thread::spawn(move || {
                    tracing::dispatcher::with_default(&dispatch, || {
                        tracing::info_span!("unrelated.worker").in_scope(|| {
                            let repo = gix::open(repo_path).unwrap();
                            assert!(validate_ref_command(
                                &repo,
                                "refs/heads/main",
                                &expected,
                                Some(&span)
                            )
                            .unwrap());
                        });
                    });
                })
                .join()
                .unwrap();
            });
            tracing::info_span!("ref.snapshot", operation = "create").in_scope(|| {
                let span = tracing::Span::current();
                let repo = gix::open(&repo_path).unwrap();
                assert!(!validate_ref_command(
                    &repo,
                    "refs/heads/main",
                    &"0".repeat(40),
                    Some(&span)
                )
                .unwrap());
            });
            tracing::info_span!("ref.snapshot", operation = "missing").in_scope(|| {
                let span = tracing::Span::current();
                let repo = gix::open(&repo_path).unwrap();
                assert!(!validate_ref_command(
                    &repo,
                    "refs/heads/private-marker",
                    &original,
                    Some(&span)
                )
                .unwrap());
            });
            tracing::info_span!("ref.snapshot", operation = "unobserved").in_scope(|| {
                let repo = gix::open(&repo_path).unwrap();
                assert!(
                    !validate_ref_command(&repo, "refs/heads/main", &replacement, None).unwrap()
                );
            });
            replacement
        });
        provider.force_flush().unwrap();
        let spans = captured.0.lock().unwrap();
        let snapshots: std::collections::BTreeMap<_, _> = spans
            .iter()
            .filter(|span| span.name == "ref.snapshot")
            .map(|span| {
                let attributes: std::collections::BTreeMap<_, _> = span
                    .attributes
                    .iter()
                    .map(|attribute| (attribute.key.as_str(), attribute.value.to_string()))
                    .collect();
                (attributes["operation"].clone(), attributes)
            })
            .collect();
        assert_eq!(snapshots.len(), 12);
        for operation in ["advertise.upload", "advertise.receive"] {
            assert_eq!(snapshots[operation]["git.advertised_head_oid"], original);
        }
        let fetch = &snapshots["fetch"];
        assert_eq!(fetch["git.fetch.want_oid"], original);
        assert_eq!(fetch["git.fetch.have_oid"], original);
        assert_eq!(fetch["git.fetch.want_count"], "2");
        assert_eq!(fetch["git.fetch.have_count"], "2");
        assert_eq!(fetch["git.fetch.done"], "false");
        assert_eq!(snapshots["empty.fetch"]["git.fetch.want_count"], "0");
        assert!(!snapshots["empty.fetch"].contains_key("git.fetch.want_oid"));
        assert!(!snapshots["empty.fetch"].contains_key("git.fetch.have_oid"));
        let push = &snapshots["push"];
        assert_eq!(push["git.push.ref_command_count"], "2");
        assert_eq!(push["git.push.first_oid_valid"], "true");
        assert_eq!(push["git.push.old_oid"], original);
        assert_eq!(push["git.push.new_oid"], next);
        assert_eq!(snapshots["invalid"]["git.push.first_oid_valid"], "false");
        assert!(!snapshots["invalid"].contains_key("git.push.old_oid"));
        assert!(!snapshots["invalid"].contains_key("git.push.new_oid"));
        assert_eq!(snapshots["empty"]["git.push.ref_command_count"], "0");
        assert!(!snapshots["empty"].contains_key("git.push.old_oid"));
        assert_eq!(snapshots["write"]["git.write.parent_oid"], original);
        assert_eq!(snapshots["write"]["git.write.commit_oid"], replacement);
        assert_eq!(snapshots["validate"]["git.push.current_oid"], replacement);
        assert_eq!(
            snapshots["validate"]["git.push.current_lookup_succeeded"],
            "true"
        );
        assert!(!snapshots["unobserved"].contains_key("git.push.current_oid"));
        assert_eq!(
            snapshots["create"]["git.push.current_lookup_attempted"],
            "false"
        );
        assert!(!snapshots["create"].contains_key("git.push.current_oid"));
        assert_eq!(
            snapshots["missing"]["git.push.current_lookup_succeeded"],
            "false"
        );
        assert!(!snapshots["missing"].contains_key("git.push.current_oid"));
        assert!(spans
            .iter()
            .filter(|span| span.name == "unrelated.worker")
            .all(|span| !span
                .attributes
                .iter()
                .any(|attribute| attribute.key.as_str().starts_with("git.push."))));
        assert!(!format!("{spans:?}").contains("private-marker"));
    }

    #[test]
    fn test_repository_commit_lock_recovers_when_owner_releases_descriptor() {
        let dir = TempDir::new().unwrap();
        let path = dir.path().join("gitstore-commit.lock");
        let owner =
            acquire_repository_commit_lock(&path, std::time::Duration::from_millis(10)).unwrap();

        let error =
            acquire_repository_commit_lock(&path, std::time::Duration::from_millis(5)).unwrap_err();
        assert_eq!(error.kind(), io::ErrorKind::TimedOut);

        // Process termination closes its descriptors without unlinking this
        // file. Dropping the owner models that OS cleanup: the persistent path
        // must immediately be reusable by a replacement service process.
        drop(owner);
        assert!(path.exists());
        acquire_repository_commit_lock(&path, std::time::Duration::from_millis(10)).unwrap();
    }

    #[tokio::test]
    async fn test_commit_file_cancellation_retains_writer_until_publication_finishes() {
        let dir = TempDir::new().unwrap();
        bare_repo_with_main(dir.path(), TEST_REPO_E);
        let repo_path = fanout_path(dir.path(), TEST_REPO_E).unwrap();
        let marker = acquire_repository_commit_lock(
            &repo_path.join("gitstore-commit.lock"),
            std::time::Duration::from_millis(10),
        )
        .unwrap();
        let svc = Arc::new(make_test_service(dir.path()));
        let lock = get_or_insert_lock(&svc.repo_locks, TEST_REPO_E);
        let snapshot = get_or_insert_snapshot_lock(&svc.repo_locks, TEST_REPO_E);
        let pending_caller = svc.clone();
        let queue = svc
            .repo_locks
            .entry(TEST_REPO_E.into())
            .or_default()
            .commits
            .clone();
        let slots = svc.commit_slots.clone();
        let writer = tokio::spawn(async move {
            svc.commit_file(signed_test_request(CommitFileRequest {
                repository_id: TEST_REPO_E.into(),
                path: "products/cancelled.md".into(),
                content: b"committed despite caller cancellation".to_vec(),
                commit_message: "cancelled caller".into(),
                author_name: "Tester".into(),
                author_email: "test@example.com".into(),
                authorization: test_authorization(TEST_REPO_E, "repository.contents.write"),
            }))
            .await
        });
        tokio::time::timeout(std::time::Duration::from_secs(5), async {
            while snapshot.try_read().is_ok() {
                tokio::task::yield_now().await;
            }
        })
        .await
        .unwrap();
        let pending = tokio::spawn(async move {
            pending_caller
                .commit_file(signed_test_request(CommitFileRequest {
                    repository_id: TEST_REPO_E.into(),
                    path: "products/cancelled-pending.md".into(),
                    content: b"must not publish".to_vec(),
                    authorization: test_authorization(TEST_REPO_E, "repository.contents.write"),
                    ..Default::default()
                }))
                .await
        });
        tokio::time::timeout(std::time::Duration::from_secs(5), async {
            while queue.pending.lock().await.entries.len() != 1 {
                tokio::task::yield_now().await;
            }
        })
        .await
        .unwrap();
        pending.abort();
        assert!(pending.await.unwrap_err().is_cancelled());
        tokio::time::timeout(std::time::Duration::from_secs(5), async {
            while slots.available_permits() != COMMIT_FILE_PENDING_LIMIT - 1 {
                tokio::task::yield_now().await;
            }
        })
        .await
        .unwrap();
        assert!(queue.pending.lock().await.entries.is_empty());
        writer.abort();
        assert!(writer.await.unwrap_err().is_cancelled());
        let retained = lock.try_write().is_err();
        let retained_snapshot = snapshot.try_read().is_err();
        drop(marker);
        let guard = tokio::time::timeout(std::time::Duration::from_secs(5), lock.write())
            .await
            .unwrap();
        tokio::time::timeout(std::time::Duration::from_secs(5), async {
            loop {
                let repo = gix::open(&repo_path).unwrap();
                if let Ok(commit) = repo.head_commit() {
                    let tree = commit.tree_id().unwrap().detach();
                    if find_blob_in_tree(&repo, tree, "products/cancelled.md").is_ok() {
                        break;
                    }
                }
                tokio::task::yield_now().await;
            }
        })
        .await
        .unwrap();
        let repo = gix::open(&repo_path).unwrap();
        let commit = repo.head_commit().unwrap();
        let tree = commit.tree_id().unwrap().detach();
        let blob = find_blob_in_tree(&repo, tree, "products/cancelled.md").unwrap();
        assert_eq!(
            repo.find_object(blob).unwrap().data,
            b"committed despite caller cancellation"
        );
        assert_eq!(
            find_blob_in_tree(&repo, tree, "products/cancelled-pending.md")
                .unwrap_err()
                .code(),
            tonic::Code::NotFound
        );
        drop(guard);
        assert!(
            retained && retained_snapshot,
            "cancelled caller released a live publication's lifecycle guard or snapshot writer"
        );
    }

    #[tokio::test(flavor = "multi_thread", worker_threads = 4)]
    async fn test_commit_file_coordinates_across_service_instances() {
        const WRITES: usize = 16;

        let dir = TempDir::new().unwrap();
        bare_repo_with_main(dir.path(), TEST_REPO_E);

        // Each service instance owns a distinct in-process lock map, matching
        // the coordination boundary between production replicas.
        let services = [
            Arc::new(make_test_service(dir.path())),
            Arc::new(make_test_service(dir.path())),
        ];
        let barrier = Arc::new(tokio::sync::Barrier::new(WRITES));
        let mut handles = Vec::with_capacity(WRITES);

        for index in 0..WRITES {
            let service = Arc::clone(&services[index % services.len()]);
            let barrier = Arc::clone(&barrier);
            handles.push(tokio::spawn(async move {
                barrier.wait().await;
                service
                    .commit_file(signed_test_request(CommitFileRequest {
                        repository_id: TEST_REPO_E.to_string(),
                        path: format!("products/concurrent-{index}.md"),
                        content: format!("---\nid: concurrent-{index}\n---").into_bytes(),
                        commit_message: format!("add concurrent product {index}"),
                        author_name: "Tester".to_string(),
                        author_email: "test@example.com".to_string(),
                        authorization: test_authorization(TEST_REPO_E, "repository.contents.write"),
                    }))
                    .await
            }));
        }

        for handle in handles {
            handle.await.unwrap().unwrap();
        }

        let repo_path = fanout_path(dir.path(), TEST_REPO_E).unwrap();
        let repo = gix::open(repo_path).unwrap();
        let commit_id = resolve_ref_to_commit_id(&repo, "HEAD").unwrap();
        let tree_id = repo
            .find_object(commit_id)
            .unwrap()
            .try_into_commit()
            .unwrap()
            .tree_id()
            .unwrap()
            .detach();
        for index in 0..WRITES {
            find_blob_in_tree(&repo, tree_id, &format!("products/concurrent-{index}.md")).unwrap();
        }
    }

    #[tokio::test]
    async fn test_delete_file_receipt_crash_cuts_before_and_after_publication() {
        let dir = TempDir::new().unwrap();
        repo_with_commit(dir.path(), TEST_REPO_E);
        let repo_path = fanout_path(dir.path(), TEST_REPO_E).unwrap();
        let repo = gix::open(&repo_path).unwrap();
        let head = repo.head_commit().unwrap();
        let initial = head.id().detach();
        let request = DeleteFileRequest {
            repository_id: TEST_REPO_E.into(),
            path: "products/p1.md".into(),
            ref_name: Some("refs/heads/main".into()),
            expected_commit_sha: Some(initial.to_string()),
            authorization: test_authorization(TEST_REPO_E, "repository.contents.write"),
            ..Default::default()
        };
        let tree = repo
            .edit_tree(head.tree_id().unwrap().detach())
            .unwrap()
            .remove("products/p1.md")
            .unwrap()
            .write()
            .unwrap()
            .detach();
        let signature = gix::actor::Signature {
            name: "test".into(),
            email: "test@example.com".into(),
            time: gix::date::Time::now_local_or_utc(),
        };
        let deleted = repo
            .write_object(&gix::objs::Commit {
                tree,
                parents: vec![initial].into(),
                author: signature.clone(),
                committer: signature.clone(),
                encoding: None,
                message: "delete".into(),
                extra_headers: vec![],
            })
            .unwrap()
            .detach();
        let receipt = delete_receipt_ref(&repo, &request, initial).unwrap();
        prepare_delete_receipt(&repo, &receipt, deleted, &request.path).unwrap();
        let prepared_ref = format!("{receipt}-prepared");
        // Corrupt private evidence only in the fixture to exercise recovery
        // validation; neither receipt nor marker is writable through Git pushes.
        let wrong_tree = repo
            .write_object(&gix::objs::Commit {
                tree: head.tree_id().unwrap().detach(),
                parents: vec![initial].into(),
                author: signature.clone(),
                committer: signature.clone(),
                encoding: None,
                message: "not a deletion".into(),
                extra_headers: vec![],
            })
            .unwrap()
            .detach();
        for (receipt_target, marker_target) in [
            (initial, initial),
            (wrong_tree, wrong_tree),
            (deleted, initial),
        ] {
            repo.reference(
                receipt.as_str(),
                receipt_target,
                gix::refs::transaction::PreviousValue::Any,
                "invalid fixture",
            )
            .unwrap();
            repo.reference(
                prepared_ref.as_str(),
                marker_target,
                gix::refs::transaction::PreviousValue::Any,
                "invalid fixture",
            )
            .unwrap();
            let replacement = make_test_service(dir.path());
            assert_eq!(
                replacement
                    .delete_file(signed_test_request(request.clone()))
                    .await
                    .unwrap_err()
                    .code(),
                tonic::Code::FailedPrecondition
            );
            assert_eq!(repo.head_commit().unwrap().id().detach(), initial);
        }
        repo.reference(
            receipt.as_str(),
            deleted,
            gix::refs::transaction::PreviousValue::Any,
            "restore fixture",
        )
        .unwrap();
        repo.reference(
            prepared_ref.as_str(),
            deleted,
            gix::refs::transaction::PreviousValue::Any,
            "restore fixture",
        )
        .unwrap();
        let mut wrong_path = request.clone();
        wrong_path.path = "products/missing.md".into();
        assert_eq!(
            resume_prepared_delete_receipt(
                &repo,
                &receipt,
                "refs/heads/main",
                initial,
                &wrong_path.path
            )
            .unwrap_err()
            .code(),
            tonic::Code::FailedPrecondition
        );
        assert_eq!(repo.head_commit().unwrap().id().detach(), initial);
        let replacement = make_test_service(dir.path());
        assert_eq!(
            replacement
                .delete_file(signed_test_request(request.clone()))
                .await
                .unwrap()
                .into_inner()
                .commit_sha,
            deleted.to_string()
        );
        assert_eq!(repo.head_commit().unwrap().id().detach(), deleted);
        assert!(repo
            .try_find_reference(prepared_ref.as_str())
            .unwrap()
            .is_none());
        // The durable receipt preceded publication; a missing response cannot
        // leave a successful deletion with no recoverable operation evidence.
        let replacement = make_test_service(dir.path());
        assert_eq!(
            replacement
                .delete_file(signed_test_request(request))
                .await
                .unwrap()
                .into_inner()
                .commit_sha,
            deleted.to_string()
        );
        let head = repo.head_commit().unwrap();
        assert_eq!(head.id().detach(), deleted);
        assert_eq!(
            head.parent_ids().map(|id| id.detach()).collect::<Vec<_>>(),
            vec![initial]
        );
    }

    #[tokio::test]
    async fn test_delete_file_receipt_is_private_and_request_scoped() {
        let dir = TempDir::new().unwrap();
        repo_with_commit(dir.path(), TEST_REPO_E);
        let repo_path = fanout_path(dir.path(), TEST_REPO_E).unwrap();
        let repo = gix::open(&repo_path).unwrap();
        let initial = repo.head_commit().unwrap().id().detach();
        let request = DeleteFileRequest {
            repository_id: TEST_REPO_E.into(),
            path: "products/p1.md".into(),
            ref_name: Some("refs/heads/main".into()),
            expected_commit_sha: Some(initial.to_string()),
            authorization: test_authorization(TEST_REPO_E, "repository.contents.write"),
            ..Default::default()
        };
        let receipt_ref = delete_receipt_ref(&repo, &request, initial).unwrap();
        for field in ["repo", "ref", "path", "commit"] {
            let mut other = request.clone();
            let mut expected = initial;
            match field {
                "repo" => other.repository_id = TEST_REPO_A.into(),
                "ref" => other.ref_name = Some("refs/heads/other".into()),
                "path" => other.path = "products/other.md".into(),
                _ => expected = gix::ObjectId::null(gix::hash::Kind::Sha1),
            }
            assert_ne!(
                delete_receipt_ref(&repo, &other, expected).unwrap(),
                receipt_ref
            );
        }
        let svc = make_test_service(dir.path());
        let removed = svc
            .delete_file(signed_test_request(request))
            .await
            .unwrap()
            .into_inner()
            .commit_sha;
        let packs = crate::git::pack_server::HttpPackServer::new(repo_path, 0);
        for advertisement in [
            packs.advertise_upload_pack_refs().unwrap(),
            packs.advertise_receive_pack_refs().unwrap(),
        ] {
            assert!(!String::from_utf8_lossy(&advertisement).contains("refs/gitstore/"));
        }
        let mut client =
            make_grpc_client_with_push_limits(dir.path().to_path_buf(), PushLimits::default())
                .await;
        for (old, new, name) in [
            (removed.clone(), "0".repeat(40), receipt_ref.clone()),
            (removed.clone(), initial.to_string(), receipt_ref.clone()),
            (
                "0".repeat(40),
                initial.to_string(),
                format!("{DELETE_RECEIPT_PREFIX}forged"),
            ),
            ("0".repeat(40), initial.to_string(), "refs/gitstore".into()),
        ] {
            let mut chunk =
                delete_ref_cmd_with_ctx(TEST_REPO_E, &name, &old, minimal_push_ctx(TEST_REPO_E));
            chunk.ref_commands[0].new_oid = new;
            let response = client
                .receive_pack(signed_test_stream(vec![chunk]))
                .await
                .unwrap()
                .into_inner();
            assert!(String::from_utf8_lossy(&response.report_status)
                .contains("private GitStore operation refs cannot be pushed"));
        }
        assert_eq!(
            repo.find_reference(&receipt_ref).unwrap().id().to_string(),
            removed
        );
        assert!(repo
            .find_reference(&format!("{DELETE_RECEIPT_PREFIX}forged"))
            .is_err());
    }

    #[tokio::test]
    async fn test_delete_file_receipt_cannot_authorize_reset_or_unrelated_absence() {
        let dir = TempDir::new().unwrap();
        repo_with_commit(dir.path(), TEST_REPO_E);
        let repo = gix::open(fanout_path(dir.path(), TEST_REPO_E).unwrap()).unwrap();
        let initial = repo.head_commit().unwrap().id().detach();
        let request = DeleteFileRequest {
            repository_id: TEST_REPO_E.into(),
            path: "products/p1.md".into(),
            ref_name: Some("refs/heads/main".into()),
            expected_commit_sha: Some(initial.to_string()),
            commit_message: "guarded removal".into(),
            authorization: test_authorization(TEST_REPO_E, "repository.contents.write"),
            ..Default::default()
        };
        let svc = make_test_service(dir.path());
        svc.delete_file(signed_test_request(request.clone()))
            .await
            .unwrap();
        repo.reference(
            "refs/heads/main",
            initial,
            gix::refs::transaction::PreviousValue::Any,
            "force reset",
        )
        .unwrap();
        let replacement = make_test_service(dir.path());
        assert_eq!(
            replacement
                .delete_file(signed_test_request(request.clone()))
                .await
                .unwrap_err()
                .code(),
            tonic::Code::FailedPrecondition
        );
        assert_eq!(repo.head_commit().unwrap().id().detach(), initial);
        let mut unrelated = request.clone();
        unrelated.ref_name = None;
        unrelated.expected_commit_sha = None;
        unrelated.commit_message = "unrelated removal".into();
        replacement
            .delete_file(signed_test_request(unrelated))
            .await
            .unwrap();
        // A durable prepared receipt alone, with no ancestry proof of publication,
        // must not turn an unrelated removal into successful guarded deletion.
        assert_eq!(
            replacement
                .delete_file(signed_test_request(request))
                .await
                .unwrap_err()
                .code(),
            tonic::Code::FailedPrecondition
        );
    }

    #[tokio::test]
    async fn test_delete_file_receipt_retries_and_restored_content_conflict() {
        let dir = TempDir::new().unwrap();
        repo_with_commit(dir.path(), TEST_REPO_E);
        let repo = gix::open(fanout_path(dir.path(), TEST_REPO_E).unwrap()).unwrap();
        let initial = repo.head_commit().unwrap().id().to_string();
        let request = || {
            signed_test_request(DeleteFileRequest {
                repository_id: TEST_REPO_E.into(),
                path: "products/p1.md".into(),
                ref_name: Some("refs/heads/main".into()),
                expected_commit_sha: Some(initial.clone()),
                commit_message: "guarded removal".into(),
                authorization: test_authorization(TEST_REPO_E, "repository.contents.write"),
                ..Default::default()
            })
        };
        let svc = make_test_service(dir.path());
        let removed = svc
            .delete_file(request())
            .await
            .unwrap()
            .into_inner()
            .commit_sha;
        let replacement = make_test_service(dir.path());
        assert_eq!(
            replacement
                .delete_file(request())
                .await
                .unwrap()
                .into_inner()
                .commit_sha,
            removed
        );
        for path in ["products/unrelated.md", "products/p1.md"] {
            replacement
                .commit_file(signed_test_request(CommitFileRequest {
                    repository_id: TEST_REPO_E.into(),
                    path: path.into(),
                    content: b"newer content".to_vec(),
                    commit_message: "newer push".into(),
                    authorization: test_authorization(TEST_REPO_E, "repository.contents.write"),
                    ..Default::default()
                }))
                .await
                .unwrap();
            if path == "products/p1.md" {
                assert_eq!(
                    replacement.delete_file(request()).await.unwrap_err().code(),
                    tonic::Code::FailedPrecondition
                );
            } else {
                assert_eq!(
                    replacement
                        .delete_file(request())
                        .await
                        .unwrap()
                        .into_inner()
                        .commit_sha,
                    removed
                );
                let mut wrong_path = request().into_inner();
                wrong_path.path = "products/arbitrary.md".into();
                assert_eq!(
                    replacement
                        .delete_file(signed_test_request(wrong_path))
                        .await
                        .unwrap_err()
                        .code(),
                    tonic::Code::Aborted
                );
            }
        }
    }

    #[tokio::test]
    async fn test_delete_file_arbitrary_absence_is_not_a_receipt() {
        let dir = TempDir::new().unwrap();
        repo_with_commit(dir.path(), TEST_REPO_E);
        let repo = gix::open(fanout_path(dir.path(), TEST_REPO_E).unwrap()).unwrap();
        let initial = repo.head_commit().unwrap().id().to_string();
        let svc = make_test_service(dir.path());
        let mut request = DeleteFileRequest {
            repository_id: TEST_REPO_E.into(),
            path: "products/p1.md".into(),
            commit_message: "unrelated removal".into(),
            authorization: test_authorization(TEST_REPO_E, "repository.contents.write"),
            ..Default::default()
        };
        svc.delete_file(signed_test_request(request.clone()))
            .await
            .unwrap();
        request.ref_name = Some("refs/heads/main".into());
        request.expected_commit_sha = Some(initial);
        assert_eq!(
            svc.delete_file(signed_test_request(request.clone()))
                .await
                .unwrap_err()
                .code(),
            tonic::Code::Aborted
        );
        request.expected_commit_sha = Some(repo.head_commit().unwrap().id().to_string());
        assert_eq!(
            svc.delete_file(signed_test_request(request))
                .await
                .unwrap_err()
                .code(),
            tonic::Code::Aborted
        );
    }

    #[tokio::test]
    async fn test_delete_file_publish_rejects_concurrent_push() {
        let dir = TempDir::new().unwrap();
        repo_with_commit(dir.path(), TEST_REPO_E);
        let repo = gix::open(fanout_path(dir.path(), TEST_REPO_E).unwrap()).unwrap();
        let initial = repo.head_commit().unwrap().id().detach();
        let svc = make_test_service(dir.path());
        let pushed = svc
            .commit_file(signed_test_request(CommitFileRequest {
                repository_id: TEST_REPO_E.into(),
                path: "products/pushed.md".into(),
                content: b"concurrent push".to_vec(),
                commit_message: "push".into(),
                authorization: test_authorization(TEST_REPO_E, "repository.contents.write"),
                ..Default::default()
            }))
            .await
            .unwrap()
            .into_inner();
        let err = publish_delete_ref(&repo, "refs/heads/main", initial, initial, None).unwrap_err();
        assert_eq!(err.code(), tonic::Code::Aborted, "{err:?}");
        let head = repo.head_commit().unwrap();
        assert_eq!(head.id().to_string(), pushed.commit_sha);
        let tree = head.tree_id().unwrap().detach();
        find_blob_in_tree(&repo, tree, "products/p1.md").unwrap();
        find_blob_in_tree(&repo, tree, "products/pushed.md").unwrap();
        repo.find_reference("refs/heads/main")
            .unwrap()
            .delete()
            .unwrap();
        let err = publish_delete_ref(&repo, "refs/heads/main", initial, initial, None).unwrap_err();
        assert_eq!(err.code(), tonic::Code::Aborted, "{err:?}");
        assert!(repo.find_reference("refs/heads/main").is_err());
        use gix::refs::transaction::{Change, LogChange, PreviousValue, RefEdit};
        repo.edit_reference(RefEdit {
            change: Change::Update {
                log: LogChange::default(),
                expected: PreviousValue::MustNotExist,
                new: gix::refs::Target::Symbolic("refs/heads/other".try_into().unwrap()),
            },
            name: "refs/heads/main".try_into().unwrap(),
            deref: false,
        })
        .unwrap();
        let err = publish_delete_ref(&repo, "refs/heads/main", initial, initial, None).unwrap_err();
        assert_eq!(err.code(), tonic::Code::Aborted, "{err:?}");
        assert!(matches!(
            repo.find_reference("refs/heads/main").unwrap().target(),
            gix::refs::TargetRef::Symbolic(_)
        ));
        assert!(repo.find_reference("refs/heads/other").is_err());
    }

    #[tokio::test]
    async fn test_delete_file_preconditions_reject_without_removing_file() {
        let dir = TempDir::new().unwrap();
        repo_with_commit(dir.path(), TEST_REPO_E);
        let repo = gix::open(fanout_path(dir.path(), TEST_REPO_E).unwrap()).unwrap();
        let initial = repo.head_commit().unwrap().id().detach();
        let svc = make_test_service(dir.path());
        for (ref_name, expected_commit_sha, code) in [
            (Some("refs/heads/main"), None, tonic::Code::InvalidArgument),
            (
                None,
                Some(initial.to_string()),
                tonic::Code::InvalidArgument,
            ),
            (
                Some(""),
                Some(initial.to_string()),
                tonic::Code::InvalidArgument,
            ),
            (
                Some("HEAD"),
                Some(initial.to_string()),
                tonic::Code::InvalidArgument,
            ),
            (
                Some("refs/gitstore/deletions/forged"),
                Some(initial.to_string()),
                tonic::Code::InvalidArgument,
            ),
            (
                Some("refs/heads/main"),
                Some("bad-sha".into()),
                tonic::Code::InvalidArgument,
            ),
            (
                Some("refs/heads/missing"),
                Some(initial.to_string()),
                tonic::Code::Aborted,
            ),
            (
                Some("refs/heads/main"),
                Some("0000000000000000000000000000000000000001".into()),
                tonic::Code::Aborted,
            ),
        ] {
            let err = svc
                .delete_file(signed_test_request(DeleteFileRequest {
                    repository_id: TEST_REPO_E.into(),
                    path: "products/p1.md".into(),
                    commit_message: "delete".into(),
                    authorization: test_authorization(TEST_REPO_E, "repository.contents.write"),
                    ref_name: ref_name.map(str::to_owned),
                    expected_commit_sha,
                    ..Default::default()
                }))
                .await
                .unwrap_err();
            assert_eq!(err.code(), code);
            assert_eq!(repo.head_commit().unwrap().id().detach(), initial);
            find_blob_in_tree(
                &repo,
                repo.head_commit().unwrap().tree_id().unwrap().detach(),
                "products/p1.md",
            )
            .unwrap();
        }
    }

    #[tokio::test]
    async fn test_delete_file_guarded_explicit_ref_and_legacy_head() {
        for guarded in [false, true] {
            let dir = TempDir::new().unwrap();
            repo_with_commit(dir.path(), TEST_REPO_E);
            let repo = gix::open(fanout_path(dir.path(), TEST_REPO_E).unwrap()).unwrap();
            let initial = repo.head_commit().unwrap().id().detach();
            repo.reference(
                "refs/heads/catalog",
                initial,
                gix::refs::transaction::PreviousValue::MustNotExist,
                "branch",
            )
            .unwrap();
            let svc = make_test_service(dir.path());
            let result = svc
                .delete_file(signed_test_request(DeleteFileRequest {
                    repository_id: TEST_REPO_E.into(),
                    path: "products/p1.md".into(),
                    commit_message: "delete".into(),
                    authorization: test_authorization(TEST_REPO_E, "repository.contents.write"),
                    ref_name: guarded.then(|| "refs/heads/catalog".into()),
                    expected_commit_sha: guarded.then(|| initial.to_string()),
                    ..Default::default()
                }))
                .await
                .unwrap()
                .into_inner();
            let target = if guarded {
                "refs/heads/catalog"
            } else {
                "HEAD"
            };
            let commit = repo
                .find_object(resolve_ref_to_commit_id(&repo, target).unwrap())
                .unwrap()
                .try_into_commit()
                .unwrap();
            assert_eq!(commit.id().to_string(), result.commit_sha);
            assert_eq!(commit.parent_ids().next().unwrap().detach(), initial);
            assert_eq!(
                find_blob_in_tree(&repo, commit.tree_id().unwrap().detach(), "products/p1.md")
                    .unwrap_err()
                    .code(),
                tonic::Code::NotFound
            );
            let untouched = if guarded {
                "HEAD"
            } else {
                "refs/heads/catalog"
            };
            assert_eq!(resolve_ref_to_commit_id(&repo, untouched).unwrap(), initial);
        }
    }

    #[tokio::test]
    async fn test_delete_file_on_nonexistent_file_returns_not_found() {
        let dir = TempDir::new().unwrap();
        bare_repo_with_main(dir.path(), TEST_REPO_E);

        let svc = make_test_service(dir.path());
        let req = signed_test_request(DeleteFileRequest {
            repository_id: TEST_REPO_E.to_string(),
            path: "products/missing.md".to_string(),
            commit_message: "delete".to_string(),
            author_name: "T".to_string(),
            author_email: "t@t.com".to_string(),
            authorization: test_authorization(TEST_REPO_E, "repository.contents.write"),
            ..Default::default()
        });
        let err = svc.delete_file(req).await.unwrap_err();
        assert_eq!(err.code(), tonic::Code::NotFound);
    }

    #[tokio::test]
    async fn test_create_tag_already_exists_returns_already_exists() {
        let dir = TempDir::new().unwrap();
        repo_with_commit(dir.path(), TEST_REPO_D); // creates v1.0.0

        let svc = make_test_service(dir.path());
        let req = signed_test_request(CreateTagRequest {
            repository_id: TEST_REPO_D.to_string(),
            tag_name: "v1.0.0".to_string(),
            message: "duplicate".to_string(),
            target_commit_sha: "".to_string(),
            authorization: test_authorization(TEST_REPO_D, "repository.contents.write"),
        });
        let err = svc.create_tag(req).await.unwrap_err();
        assert_eq!(err.code(), tonic::Code::AlreadyExists);
    }

    #[tokio::test]
    async fn test_concurrent_repos_are_isolated() {
        let dir = TempDir::new().unwrap();
        bare_repo_with_main(dir.path(), TEST_REPO_F);
        bare_repo_with_main(dir.path(), TEST_REPO_G);

        let svc = std::sync::Arc::new(make_test_service(dir.path()));
        let svc_a = std::sync::Arc::clone(&svc);
        let svc_b = std::sync::Arc::clone(&svc);

        let h_a = tokio::spawn(async move {
            svc_a
                .commit_file(signed_test_request(CommitFileRequest {
                    repository_id: TEST_REPO_F.to_string(),
                    path: "file-a.md".to_string(),
                    content: b"a".to_vec(),
                    commit_message: "from a".to_string(),
                    author_name: "A".to_string(),
                    author_email: "a@a.com".to_string(),
                    authorization: test_authorization(TEST_REPO_F, "repository.contents.write"),
                }))
                .await
                .unwrap()
        });

        let h_b = tokio::spawn(async move {
            svc_b
                .commit_file(signed_test_request(CommitFileRequest {
                    repository_id: TEST_REPO_G.to_string(),
                    path: "file-b.md".to_string(),
                    content: b"b".to_vec(),
                    commit_message: "from b".to_string(),
                    author_name: "B".to_string(),
                    author_email: "b@b.com".to_string(),
                    authorization: test_authorization(TEST_REPO_G, "repository.contents.write"),
                }))
                .await
                .unwrap()
        });

        let (ra, rb) = tokio::join!(h_a, h_b);
        assert!(!ra.unwrap().into_inner().commit_sha.is_empty());
        assert!(!rb.unwrap().into_inner().commit_sha.is_empty());
    }

    // -----------------------------------------------------------------------
    // T031-T034, T045: receive_pack PushContext validation and policy enforcement
    // -----------------------------------------------------------------------

    const TEST_REPO_H: &str = "01960000-0000-7000-8000-000000000030";
    const TEST_REPO_I: &str = "01960000-0000-7000-8000-000000000031";

    #[test]
    fn test_upload_pack_cancellation_retains_queued_reader() {
        use tokio_stream::StreamExt;

        let runtime = tokio::runtime::Builder::new_current_thread()
            .enable_all()
            .max_blocking_threads(1)
            .build()
            .unwrap();
        runtime.block_on(async {
            for shallow in [false, true] {
                let root = TempDir::new().unwrap();
                repo_with_commit(root.path(), TEST_REPO_H);
                let tip = gix::open(fanout_path(root.path(), TEST_REPO_H).unwrap())
                    .unwrap()
                    .head_commit()
                    .unwrap()
                    .id
                    .to_string();
                let service = make_test_service(root.path());
                let lock = get_or_insert_lock(&service.repo_locks, TEST_REPO_H);
                let writer = lock.write().await;
                let want = format!("want {tip} side-band-64k shallow\n");
                let mut body = format!("{:04x}{want}", want.len() + 4).into_bytes();
                if shallow {
                    body.extend_from_slice(b"000ddeepen 1\n");
                }
                body.extend_from_slice(b"00000009done\n");
                let request = signed_test_request(UploadPackRequest {
                    repository_id: TEST_REPO_H.into(),
                    body,
                    authorization: test_authorization(TEST_REPO_H, "repository.contents.read"),
                });
                let envelope = request
                    .metadata()
                    .get(crate::auth::interceptor::SIGNATURE_HEADER)
                    .unwrap()
                    .to_str()
                    .unwrap();
                let fields: Vec<_> = envelope.split(':').collect();
                let nonce = fields[2].to_owned();
                let mut reader = Box::pin(service.upload_pack(request));
                std::future::poll_fn(|cx| {
                    assert!(std::future::Future::poll(reader.as_mut(), cx).is_pending());
                    std::task::Poll::Ready(())
                })
                .await;
                tokio::time::timeout(std::time::Duration::from_secs(5), async {
                    while !service.rpc_auth.has_replay_reservation(&nonce).unwrap() {
                        tokio::task::yield_now().await;
                    }
                })
                .await
                .unwrap();
                let (release, receive) = std::sync::mpsc::channel::<()>();
                let (started, ready) = tokio::sync::oneshot::channel();
                let blocker = crate::spawn_blocking(move || {
                    let _ = started.send(());
                    let _ = receive.recv();
                });
                tokio::time::timeout(std::time::Duration::from_secs(5), ready)
                    .await
                    .unwrap()
                    .unwrap();
                drop(writer);
                let mut stream = reader.await.unwrap().into_inner();
                let retained = lock.try_write().is_err();
                let interrupt = stream.interrupt.clone();
                assert!(!interrupt.load(Ordering::Acquire));
                interrupt.store(true, Ordering::Release);
                drop(release);
                blocker.await.unwrap();
                let error = tokio::time::timeout(
                    std::time::Duration::from_secs(5),
                    stream.next(),
                )
                .await
                .unwrap()
                .unwrap()
                .unwrap_err();
                assert_eq!(error.code(), tonic::Code::Cancelled);
                assert_eq!(error.message(), "upload-pack cancelled");
                assert!(stream.next().await.is_none());
                drop(stream);
                let writer =
                    tokio::time::timeout(std::time::Duration::from_secs(5), lock.write())
                        .await
                        .unwrap();
                drop(writer);
                assert!(
                    retained,
                    "shallow={shallow}: upload-pack released its queued reader before generation ended"
                );
            }
        });
    }

    #[test]
    fn test_upload_pack_backpressure_releases_reader_and_cancellation_drains_worker() {
        let runtime = tokio::runtime::Builder::new_current_thread()
            .enable_all()
            .max_blocking_threads(1)
            .build()
            .unwrap();
        runtime.block_on(async {
            let root = TempDir::new().unwrap();
            repo_with_commit(root.path(), TEST_REPO_H);
            let repo = gix::open(fanout_path(root.path(), TEST_REPO_H).unwrap()).unwrap();
            let parent = repo.head_commit().unwrap();
            let mut state = 0x1234_5678_90ab_cdefu64;
            let bytes: Vec<_> = (0..2 * 1024 * 1024)
                .map(|_| {
                    state ^= state << 13;
                    state ^= state >> 7;
                    state ^= state << 17;
                    state as u8
                })
                .collect();
            let blob = repo.write_blob(&bytes).unwrap().detach();
            let mut tree = repo.edit_tree(parent.tree_id().unwrap().detach()).unwrap();
            tree.upsert("payload.bin", gix::objs::tree::EntryKind::Blob, blob)
                .unwrap();
            let tree = tree.write().unwrap().detach();
            let sig = gix::actor::SignatureRef {
                name: "Test".into(),
                email: "test@example.com".into(),
                time: "0 +0000",
            };
            let tip = repo
                .commit_as(
                    sig,
                    sig,
                    "refs/heads/main",
                    "large fetch",
                    tree,
                    [parent.id],
                )
                .unwrap()
                .detach();
            let limits = PushLimits::default();
            let pack = crate::git::pack_server::build_pack_for_wants(
                &repo,
                &[tip.to_string()],
                &[],
                limits.max_pack_size,
            )
            .unwrap();
            assert!(
                pack.len() > 16 * 64 * 1024,
                "fixture must fill the response channel"
            );
            drop(pack);
            drop(bytes);
            let service = make_test_service(root.path());
            let lock = get_or_insert_lock(&service.repo_locks, TEST_REPO_H);
            for shallow in [false, true] {
                let want = format!("want {tip} side-band-64k shallow\n");
                let mut body = format!("{:04x}{want}", want.len() + 4).into_bytes();
                if shallow {
                    body.extend_from_slice(b"000ddeepen 1\n");
                }
                body.extend_from_slice(b"00000009done\n");
                let stream = service
                    .upload_pack(signed_test_request(UploadPackRequest {
                        repository_id: TEST_REPO_H.into(),
                        body,
                        authorization: test_authorization(TEST_REPO_H, "repository.contents.read"),
                    }))
                    .await
                    .unwrap()
                    .into_inner();
                tokio::time::timeout(std::time::Duration::from_secs(5), async {
                    while stream.inner.as_ref().len() != 16 {
                        tokio::task::yield_now().await;
                    }
                })
                .await
                .unwrap();
                let interrupt = stream.interrupt.clone();
                assert!(!interrupt.load(Ordering::Acquire));
                let writer = tokio::time::timeout(std::time::Duration::from_secs(5), lock.write())
                    .await
                    .expect("response backpressure must not retain the repository read guard");
                drop(writer);
                let drained = crate::spawn_blocking(|| ());
                assert!(!drained.is_finished());
                drop(stream);
                assert!(interrupt.load(Ordering::Acquire));
                tokio::time::timeout(std::time::Duration::from_secs(5), drained)
                    .await
                    .unwrap()
                    .unwrap();
            }
        });
    }

    #[tokio::test]
    async fn test_upload_pack_uses_configured_platform_bound() {
        use tokio_stream::StreamExt;

        let root = TempDir::new().unwrap();
        repo_with_commit(root.path(), TEST_REPO_H);
        let repo = gix::open(fanout_path(root.path(), TEST_REPO_H).unwrap()).unwrap();
        let parent = repo.head_commit().unwrap();
        let sig = gix::actor::SignatureRef {
            name: "Test".into(),
            email: "test@example.com".into(),
            time: "0 +0000",
        };
        let tip = repo
            .commit_as(
                sig,
                sig,
                "refs/heads/main",
                "shallow tip",
                parent.tree_id().unwrap().detach(),
                [parent.id],
            )
            .unwrap()
            .detach();
        let want = format!("want {tip} side-band-64k shallow\n");
        let request = |shallow| {
            let mut body = format!("{:04x}{want}", want.len() + 4).into_bytes();
            if shallow {
                body.extend_from_slice(b"000ddeepen 1\n");
            }
            body.extend_from_slice(b"00000009done\n");
            signed_test_request(UploadPackRequest {
                repository_id: TEST_REPO_H.to_owned(),
                body,
                authorization: test_authorization(TEST_REPO_H, "repository.contents.read"),
            })
        };
        let mut client =
            make_grpc_client_with_push_limits(root.path().to_path_buf(), PushLimits::default())
                .await;
        for shallow in [false, true] {
            let mut stream = client
                .upload_pack(request(shallow))
                .await
                .unwrap()
                .into_inner();
            let mut response = Vec::new();
            while let Some(chunk) = stream.next().await {
                response.extend_from_slice(&chunk.unwrap().data);
            }
            let expected = if shallow {
                let boundary = format!("shallow {tip}\n");
                format!("{:04x}{boundary}00000008NAK\n", boundary.len() + 4)
            } else {
                "0008NAK\n".to_owned()
            };
            assert!(response.starts_with(expected.as_bytes()));
            assert!(response.len() > expected.len() + 4);
        }

        for (shallow, ceiling, expected_error) in [
            (true, 1, "shallow traversal exceeds configured pack size"),
            (true, 20, "shallow pack exceeds configured pack size"),
            (false, 1, "pack exceeds configured pack size"),
            (false, 20, "pack exceeds configured pack size"),
        ] {
            let mut client = make_grpc_client_with_push_limits(
                root.path().to_path_buf(),
                PushLimits {
                    max_pack_size: ceiling,
                    ..PushLimits::default()
                },
            )
            .await;
            let mut stream = client
                .upload_pack(request(shallow))
                .await
                .unwrap()
                .into_inner();
            let error = stream.next().await.unwrap().unwrap_err();
            assert!(
                stream.next().await.is_none(),
                "oversized pack must not send partial data"
            );
            assert_eq!(error.code(), tonic::Code::Internal);
            assert!(error.message().contains(expected_error));
        }
    }

    /// Spawn an in-process tonic gRPC server and return a connected client.
    /// The server is bound to a random OS-assigned port.
    async fn make_grpc_client(
        data_root: std::path::PathBuf,
    ) -> proto::git_service_client::GitServiceClient<tonic::transport::Channel> {
        use proto::git_service_server::GitServiceServer;

        let svc = make_test_service(&data_root);
        let listener = tokio::net::TcpListener::bind("127.0.0.1:0").await.unwrap();
        let addr = listener.local_addr().unwrap();
        let incoming = tokio_stream::wrappers::TcpListenerStream::new(listener);
        tokio::spawn(async move {
            tonic::transport::Server::builder()
                .add_service(crate::grpc::metrics::RpcMetricsService::new(
                    GitServiceServer::new(svc),
                ))
                .serve_with_incoming(incoming)
                .await
                .unwrap();
        });
        // Brief pause to let the server start accepting.
        tokio::time::sleep(std::time::Duration::from_millis(10)).await;
        let url = format!("http://{}", addr);
        proto::git_service_client::GitServiceClient::connect(url)
            .await
            .unwrap()
    }

    /// Builds a minimal delete-only ReceivePackRequest (no PACK data, no push_context).
    fn delete_ref_cmd(repo_id: &str, ref_name: &str, old_oid: &str) -> ReceivePackRequest {
        ReceivePackRequest {
            repository_id: repo_id.to_string(),
            ref_commands: vec![RefCommand {
                old_oid: old_oid.to_string(),
                new_oid: "0000000000000000000000000000000000000000".to_string(),
                ref_name: ref_name.to_string(),
            }],
            pack_data: vec![],
            is_last: true,
            push_context: None,
            shallow_oids: vec![],
        }
    }

    /// Builds a minimal delete-only ReceivePackRequest with a valid push_context.
    fn delete_ref_cmd_with_ctx(
        repo_id: &str,
        ref_name: &str,
        old_oid: &str,
        push_ctx: PushContext,
    ) -> ReceivePackRequest {
        ReceivePackRequest {
            repository_id: repo_id.to_string(),
            ref_commands: vec![RefCommand {
                old_oid: old_oid.to_string(),
                new_oid: "0000000000000000000000000000000000000000".to_string(),
                ref_name: ref_name.to_string(),
            }],
            pack_data: vec![],
            is_last: true,
            push_context: Some(push_ctx),
            shallow_oids: vec![],
        }
    }

    /// Returns a minimal valid PushContext for `repo_id`.
    fn minimal_push_ctx(repo_id: &str) -> PushContext {
        PushContext {
            repository_id: repo_id.to_string(),
            namespace: "ns".to_string(),
            repository_name: "repo".to_string(),
            config_resource_version: String::new(),
            policy: None,
            authorization: test_authorization(repo_id, "repository.contents.write"),
        }
    }

    #[tokio::test]
    async fn test_receive_pack_shallow_boundaries_are_signed_bounded_and_live_commits() {
        let dir = TempDir::new().unwrap();
        bare_repo_with_main(dir.path(), TEST_REPO_H);
        let repo_path = fanout_path(dir.path(), TEST_REPO_H).unwrap();
        let (old_oid, blob_oid) = {
            let repo = gix::open(&repo_path).unwrap();
            (
                repo.head_id().unwrap().to_string(),
                repo.write_blob(b"not a commit").unwrap().to_string(),
            )
        };
        let mut client = make_grpc_client(dir.path().into()).await;
        for (boundaries, expected) in [
            (vec!["0".repeat(40)], tonic::Code::InvalidArgument),
            (vec!["a".repeat(39)], tonic::Code::InvalidArgument),
            (vec!["A".repeat(40)], tonic::Code::InvalidArgument),
            (vec!["x".repeat(40)], tonic::Code::InvalidArgument),
            (
                vec![old_oid.clone(), old_oid.clone()],
                tonic::Code::InvalidArgument,
            ),
            (vec!["a".repeat(40)], tonic::Code::FailedPrecondition),
            (vec![blob_oid], tonic::Code::InvalidArgument),
            (vec![old_oid.clone(); 4097], tonic::Code::ResourceExhausted),
        ] {
            let mut chunk = delete_ref_cmd_with_ctx(
                TEST_REPO_H,
                "refs/heads/main",
                &old_oid,
                minimal_push_ctx(TEST_REPO_H),
            );
            chunk.shallow_oids = boundaries;
            let error = client
                .receive_pack(signed_test_stream(vec![chunk]))
                .await
                .unwrap_err();
            assert_eq!(error.code(), expected);
            assert_eq!(
                gix::open(&repo_path)
                    .unwrap()
                    .head_id()
                    .unwrap()
                    .to_string(),
                old_oid
            );
        }
        let mut chunk = delete_ref_cmd_with_ctx(
            TEST_REPO_H,
            "refs/heads/main",
            &old_oid,
            minimal_push_ctx(TEST_REPO_H),
        );
        let signed = signed_test_request(chunk.clone());
        chunk.shallow_oids = vec![old_oid.clone()];
        let mut forged = Request::new(tokio_stream::iter(vec![chunk.clone()]));
        *forged.metadata_mut() = signed.metadata().clone();
        assert_eq!(
            client.receive_pack(forged).await.unwrap_err().code(),
            tonic::Code::Unauthenticated
        );
        assert_eq!(
            gix::open(&repo_path)
                .unwrap()
                .head_id()
                .unwrap()
                .to_string(),
            old_oid
        );

        let shallow_path = repo_path.join("shallow");
        std::fs::write(&shallow_path, format!("{old_oid}\n")).unwrap();
        assert_eq!(
            client
                .receive_pack(signed_test_stream(vec![chunk.clone()]))
                .await
                .unwrap_err()
                .code(),
            tonic::Code::FailedPrecondition,
        );
        std::fs::remove_file(&shallow_path).unwrap();
        let report = client
            .receive_pack(signed_test_stream(vec![chunk]))
            .await
            .unwrap()
            .into_inner()
            .report_status;
        assert!(String::from_utf8_lossy(&report).contains("ok refs/heads/main"));
        assert!(gix::open(&repo_path)
            .unwrap()
            .try_find_reference("refs/heads/main")
            .unwrap()
            .is_none());
        assert!(!shallow_path.exists());
    }

    #[tokio::test]
    async fn test_receive_pack_shallow_increment_preserves_complete_history() {
        let dir = TempDir::new().unwrap();
        let target_path = fanout_path(dir.path(), TEST_REPO_I).unwrap();
        std::fs::create_dir_all(target_path.parent().unwrap()).unwrap();
        gix::init_bare(&target_path).unwrap();
        let source = TempDir::new().unwrap();
        bare_repo_with_main(source.path(), TEST_REPO_H);
        let (old_oid, seed_pack, new_oid, increment_pack) = {
            let repo = gix::open(fanout_path(source.path(), TEST_REPO_H).unwrap()).unwrap();
            let old = repo.head_id().unwrap().detach();
            let seed = build_pack_bytes_from_repo(&repo, old);
            let sig = gix::actor::Signature {
                name: "test".into(),
                email: "test@example.com".into(),
                time: gix::date::Time::now_local_or_utc(),
            };
            let blob = repo
                .write_blob(b"shallow client increment")
                .unwrap()
                .detach();
            let tree = repo
                .edit_tree(repo.find_commit(old).unwrap().tree_id().unwrap().detach())
                .unwrap()
                .upsert("README.md", gix::object::tree::EntryKind::Blob, blob)
                .unwrap()
                .write()
                .unwrap()
                .detach();
            let mut time_buf = gix::date::parse::TimeBuf::default();
            let sig = sig.to_ref(&mut time_buf);
            let new = repo
                .commit_as(sig, sig, "HEAD", "increment", tree, [old])
                .unwrap()
                .detach();
            let pack = crate::git::pack_server::build_pack_for_wants(
                &repo,
                &[new.to_string()],
                &[old.to_string()],
                PushLimits::default().max_pack_size,
            )
            .unwrap();
            (old.to_string(), seed, new.to_string(), pack)
        };
        let mut client = make_grpc_client(dir.path().into()).await;
        let seed = ReceivePackRequest {
            repository_id: TEST_REPO_I.into(),
            ref_commands: vec![RefCommand {
                old_oid: "0".repeat(40),
                new_oid: old_oid.clone(),
                ref_name: "refs/heads/main".into(),
            }],
            pack_data: seed_pack,
            is_last: true,
            push_context: Some(minimal_push_ctx(TEST_REPO_I)),
            shallow_oids: vec![],
        };
        let report = client
            .receive_pack(signed_test_stream(vec![seed]))
            .await
            .unwrap()
            .into_inner()
            .report_status;
        assert!(String::from_utf8_lossy(&report).contains("ok refs/heads/main"));
        let mut increment = ReceivePackRequest {
            repository_id: TEST_REPO_I.into(),
            ref_commands: vec![RefCommand {
                old_oid: old_oid.clone(),
                new_oid: new_oid.clone(),
                ref_name: "refs/heads/main".into(),
            }],
            pack_data: increment_pack,
            is_last: true,
            push_context: Some(minimal_push_ctx(TEST_REPO_I)),
            shallow_oids: vec![new_oid.clone()],
        };
        assert_eq!(
            client
                .receive_pack(signed_test_stream(vec![increment.clone()]))
                .await
                .unwrap_err()
                .code(),
            tonic::Code::FailedPrecondition,
            "a boundary found only in quarantine must not be imported",
        );
        {
            let repo = gix::open(&target_path).unwrap();
            assert_eq!(
                repo.find_reference("refs/heads/main")
                    .unwrap()
                    .id()
                    .to_string(),
                old_oid
            );
            assert!(repo
                .try_find_object(gix::ObjectId::from_hex(new_oid.as_bytes()).unwrap())
                .unwrap()
                .is_none());
        }
        increment.shallow_oids = vec![old_oid.clone()];
        let report = client
            .receive_pack(signed_test_stream(vec![increment]))
            .await
            .unwrap()
            .into_inner()
            .report_status;
        assert!(String::from_utf8_lossy(&report).contains("ok refs/heads/main"));
        let repo = gix::open(&target_path).unwrap();
        assert_eq!(
            repo.find_reference("refs/heads/main")
                .unwrap()
                .id()
                .to_string(),
            new_oid
        );
        let commit = repo
            .find_commit(gix::ObjectId::from_hex(new_oid.as_bytes()).unwrap())
            .unwrap();
        assert_eq!(commit.parent_ids().next().unwrap().to_string(), old_oid);
        assert!(repo
            .find_commit(gix::ObjectId::from_hex(old_oid.as_bytes()).unwrap())
            .unwrap()
            .parent_ids()
            .next()
            .is_none());
        assert!(!target_path.join("shallow").exists());
    }

    #[tokio::test]
    async fn test_empty_receive_pack_shares_repository_read_guard() {
        let dir = TempDir::new().unwrap();
        bare_repo_with_main(dir.path(), TEST_REPO_A);
        let path = fanout_path(dir.path(), TEST_REPO_A).unwrap();
        let head = gix::open(&path).unwrap().head_id().unwrap().to_string();
        let svc = GitServiceImpl::new(
            dir.path().into(),
            RPCVerifier::new("fixture-key", None, dir.path()).unwrap(),
        );
        let lock = get_or_insert_lock(&svc.repo_locks, TEST_REPO_A);
        let read = lock.clone().read_owned().await;
        let mut client = make_grpc_client_for_service(svc).await;
        let request = ReceivePackRequest {
            repository_id: TEST_REPO_A.into(),
            push_context: Some(minimal_push_ctx(TEST_REPO_A)),
            shallow_oids: vec![head.clone()],
            is_last: true,
            ..Default::default()
        };
        let result = tokio::time::timeout(
            std::time::Duration::from_secs(1),
            client.receive_pack(signed_test_stream(vec![request])),
        )
        .await;
        drop(read);
        let report = result
            .expect("an empty receive-pack must not require an exclusive repository lock")
            .unwrap()
            .into_inner()
            .report_status;
        assert!(String::from_utf8_lossy(&report).contains("unpack ok"));
        assert!(!String::from_utf8_lossy(&report).contains("ok refs/"));
        assert_eq!(
            gix::open(&path).unwrap().head_id().unwrap().to_string(),
            head
        );
        assert!(
            lock.try_write().is_ok(),
            "completed request retained a guard"
        );
    }

    #[tokio::test]
    async fn test_empty_receive_pack_preserves_auth_and_shallow_validation() {
        let dir = TempDir::new().unwrap();
        bare_repo_with_main(dir.path(), TEST_REPO_A);
        let svc = GitServiceImpl::new(
            dir.path().into(),
            RPCVerifier::new("fixture-key", None, dir.path()).unwrap(),
        );
        let lock = get_or_insert_lock(&svc.repo_locks, TEST_REPO_A);
        let read = lock.clone().read_owned().await;
        let mut client = make_grpc_client_for_service(svc).await;
        let base = ReceivePackRequest {
            repository_id: TEST_REPO_A.into(),
            push_context: Some(minimal_push_ctx(TEST_REPO_A)),
            is_last: true,
            ..Default::default()
        };
        let mut unsigned = signed_test_stream(vec![base.clone()]);
        unsigned.metadata_mut().clear();
        assert_eq!(
            client.receive_pack(unsigned).await.unwrap_err().code(),
            tonic::Code::Unauthenticated
        );
        let mut denied = base.clone();
        denied
            .push_context
            .as_mut()
            .unwrap()
            .authorization
            .as_mut()
            .unwrap()
            .action = "repository.contents.read".into();
        assert_eq!(
            client
                .receive_pack(signed_test_stream(vec![denied]))
                .await
                .unwrap_err()
                .code(),
            tonic::Code::PermissionDenied
        );
        let mut invalid = base;
        invalid.shallow_oids = vec!["invalid-shallow-id".into()];
        assert_eq!(
            tokio::time::timeout(
                std::time::Duration::from_secs(1),
                client.receive_pack(signed_test_stream(vec![invalid])),
            )
            .await
            .expect("shallow validation must not require exclusive locking")
            .unwrap_err()
            .code(),
            tonic::Code::InvalidArgument
        );
        drop(read);
        assert!(lock.try_write().is_ok());
    }

    #[tokio::test]
    async fn test_nonempty_receive_pack_retains_exclusive_repository_guard() {
        let dir = TempDir::new().unwrap();
        bare_repo_with_main(dir.path(), TEST_REPO_A);
        let path = fanout_path(dir.path(), TEST_REPO_A).unwrap();
        let head = gix::open(&path).unwrap().head_id().unwrap().to_string();
        let svc = GitServiceImpl::new(
            dir.path().into(),
            RPCVerifier::new("fixture-key", None, dir.path()).unwrap(),
        );
        let lock = get_or_insert_lock(&svc.repo_locks, TEST_REPO_A);
        let read = lock.clone().read_owned().await;
        let mut client = make_grpc_client_for_service(svc).await;
        let request = delete_ref_cmd_with_ctx(
            TEST_REPO_A,
            "refs/heads/main",
            &head,
            minimal_push_ctx(TEST_REPO_A),
        );
        let mut pending =
            tokio::spawn(
                async move { client.receive_pack(signed_test_stream(vec![request])).await },
            );
        assert!(
            tokio::time::timeout(std::time::Duration::from_millis(100), &mut pending)
                .await
                .is_err(),
            "a real ref update must wait for existing repository readers"
        );
        assert_eq!(
            gix::open(&path).unwrap().head_id().unwrap().to_string(),
            head
        );
        drop(read);
        let report = tokio::time::timeout(std::time::Duration::from_secs(3), pending)
            .await
            .unwrap()
            .unwrap()
            .unwrap()
            .into_inner()
            .report_status;
        assert!(String::from_utf8_lossy(&report).contains("ok refs/heads/main"));
        assert!(gix::open(&path)
            .unwrap()
            .try_find_reference("refs/heads/main")
            .unwrap()
            .is_none());
        assert!(lock.try_write().is_ok());
    }

    #[tokio::test]
    async fn test_receive_pack_dispatches_once_after_publication_and_not_on_commit_failure() {
        use crate::git::hooks::{AdmissionDecision, AdmissionHandler};

        struct CaptureAdmission(tokio::sync::mpsc::Sender<(String, Vec<RefUpdate>, bool)>);

        #[async_trait::async_trait]
        impl AdmissionHandler for CaptureAdmission {
            async fn admit(
                &self,
                phase: &str,
                updates: &[RefUpdate],
                repository_id: &str,
                git_dir: &Path,
                hook_ctx: &HookContext,
            ) -> anyhow::Result<AdmissionDecision> {
                assert_eq!(phase, "post-receive");
                assert_eq!(hook_ctx.repository_id, repository_id);
                let absent = {
                    let repo = gix::open(git_dir)?;
                    repo.try_find_reference(&updates[0].ref_name)?.is_none()
                };
                self.0
                    .send((repository_id.into(), updates.to_vec(), absent))
                    .await?;
                Ok(AdmissionDecision::Accept)
            }
        }

        let dir = TempDir::new().unwrap();
        let (sender, mut receiver) = tokio::sync::mpsc::channel(2);
        let svc = GitServiceImpl::with_pipeline(
            dir.path().into(),
            Arc::new(HookPipeline::new(
                std::time::Duration::from_secs(10),
                Arc::new(NoopValidationHandler),
                Arc::new(CaptureAdmission(sender)),
            )),
            PushLimits::default(),
            RPCVerifier::new("fixture-key", None, dir.path()).unwrap(),
        );
        let mut client = make_grpc_client_for_service(svc).await;
        for repo_id in [TEST_REPO_A, TEST_REPO_B] {
            bare_repo_with_main(dir.path(), repo_id);
            let repo_path = fanout_path(dir.path(), repo_id).unwrap();
            let old_oid = gix::open(&repo_path)
                .unwrap()
                .head_id()
                .unwrap()
                .to_string();
            let lock_path = repo_path.join("refs/heads/main.lock");
            if repo_id == TEST_REPO_B {
                std::fs::write(&lock_path, b"held by test").unwrap();
            }
            let update = delete_ref_cmd_with_ctx(
                repo_id,
                "refs/heads/main",
                &old_oid,
                minimal_push_ctx(repo_id),
            );
            let result = client
                .receive_pack(signed_test_stream(vec![update.clone()]))
                .await;
            if repo_id == TEST_REPO_A {
                let report = result.unwrap().into_inner().report_status;
                assert!(String::from_utf8_lossy(&report).contains("ok refs/heads/main"));
                let (actual_repo, updates, absent) =
                    tokio::time::timeout(std::time::Duration::from_secs(5), receiver.recv())
                        .await
                        .unwrap()
                        .unwrap();
                assert_eq!(actual_repo, repo_id);
                assert!(absent, "callback preceded ref publication");
                assert_eq!(updates.len(), 1);
                assert_eq!(updates[0].old_oid, old_oid);
                assert_eq!(updates[0].new_oid, update.ref_commands[0].new_oid);
            } else {
                assert_eq!(result.unwrap_err().code(), tonic::Code::Internal);
                std::fs::remove_file(lock_path).unwrap();
                assert_eq!(
                    gix::open(repo_path).unwrap().head_id().unwrap().to_string(),
                    old_oid
                );
            }
        }
        assert!(
            tokio::time::timeout(std::time::Duration::from_millis(30), receiver.recv())
                .await
                .is_err(),
            "duplicate or failed-publication callback"
        );
    }

    /// Build a full pack file containing all objects reachable from `tip_oid` in `src_repo`.
    /// Returns raw PACK bytes suitable for inclusion in a ReceivePackRequest.
    fn build_pack_bytes_from_repo(src_repo: &gix::Repository, tip_oid: gix::ObjectId) -> Vec<u8> {
        crate::git::pack_server::build_pack_for_wants(
            src_repo,
            &[tip_oid.to_string()],
            &[],
            PushLimits::default().max_pack_size,
        )
        .expect("build pack for test")
    }

    #[tokio::test]
    async fn test_receive_pack_rejects_missing_push_context() {
        let dir = TempDir::new().unwrap();
        bare_repo_with_main(dir.path(), TEST_REPO_H);
        let mut client = make_grpc_client(dir.path().to_path_buf()).await;

        // Get current HEAD OID so we can form a valid ref command
        let repo_path = fanout_path(dir.path(), TEST_REPO_H).unwrap();
        let repo = gix::open(&repo_path).unwrap();
        let head_oid = repo.head_id().unwrap().to_string();

        // Send first chunk with NO push_context — must be rejected InvalidArgument
        let chunks = vec![delete_ref_cmd(TEST_REPO_H, "refs/heads/main", &head_oid)];
        let stream = signed_test_stream(chunks);
        let result = client.receive_pack(stream).await;
        let status = result.unwrap_err();
        assert_eq!(
            status.code(),
            tonic::Code::InvalidArgument,
            "missing push_context must return InvalidArgument, got: {:?}",
            status
        );
    }

    #[tokio::test]
    async fn test_receive_pack_rejects_inconsistent_repo_id() {
        let dir = TempDir::new().unwrap();
        bare_repo_with_main(dir.path(), TEST_REPO_H);
        let mut client = make_grpc_client(dir.path().to_path_buf()).await;

        let repo_path = fanout_path(dir.path(), TEST_REPO_H).unwrap();
        let repo = gix::open(&repo_path).unwrap();
        let head_oid = repo.head_id().unwrap().to_string();

        // push_context.repository_id differs from chunk.repository_id — must be rejected
        let mut ctx = minimal_push_ctx("different-uuid-that-does-not-match");
        ctx.repository_id = "different-uuid-that-does-not-match".to_string();
        let chunk = ReceivePackRequest {
            repository_id: TEST_REPO_H.to_string(),
            ref_commands: vec![RefCommand {
                old_oid: head_oid.clone(),
                new_oid: "0000000000000000000000000000000000000000".to_string(),
                ref_name: "refs/heads/main".to_string(),
            }],
            pack_data: vec![],
            is_last: true,
            push_context: Some(ctx),
            shallow_oids: vec![],
        };
        let result = client.receive_pack(signed_test_stream(vec![chunk])).await;
        let status = result.unwrap_err();
        assert_eq!(
            status.code(),
            tonic::Code::InvalidArgument,
            "mismatched push_context.repository_id must return InvalidArgument, got: {:?}",
            status
        );
    }

    #[tokio::test]
    async fn test_pack_size_limit_enforced() {
        let dir = TempDir::new().unwrap();
        bare_repo_with_main(dir.path(), TEST_REPO_I);
        let mut client = make_grpc_client(dir.path().to_path_buf()).await;

        // Build a push context with max_pack_size_bytes = 1 (tiny — any pack will exceed it)
        let ctx = PushContext {
            repository_id: TEST_REPO_I.to_string(),
            namespace: "ns".to_string(),
            repository_name: "repo".to_string(),
            config_resource_version: String::new(),
            policy: Some(PushPolicy {
                max_pack_size_bytes: 1,
                max_file_size_bytes: 0,
            }),
            authorization: test_authorization(TEST_REPO_I, "repository.contents.write"),
        };

        // Build a pack payload: minimal data > 1 byte to trip the limit
        // We'll send pack_data of 8 bytes (just enough to exceed 1-byte limit)
        let chunk = ReceivePackRequest {
            repository_id: TEST_REPO_I.to_string(),
            ref_commands: vec![RefCommand {
                old_oid: "0000000000000000000000000000000000000000".to_string(),
                new_oid: "a".repeat(40),
                ref_name: "refs/heads/feature".to_string(),
            }],
            pack_data: b"PACK\x00\x00\x00\x02".to_vec(), // 8 bytes > 1 byte limit
            is_last: true,
            push_context: Some(ctx),
            shallow_oids: vec![],
        };

        let result = client.receive_pack(signed_test_stream(vec![chunk])).await;
        let status = result.unwrap_err();
        assert_eq!(
            status.code(),
            tonic::Code::ResourceExhausted,
            "pack exceeding max_pack_size_bytes must return ResourceExhausted, got: {:?}",
            status
        );
    }

    #[tokio::test]
    async fn test_file_size_limit_enforced() {
        // Create a target repo (empty, to accept a new push) and a source repo with a commit.
        let dir = TempDir::new().unwrap();
        let target_id = TEST_REPO_I;

        // Initialise an empty bare target repo that can accept a push.
        let target_path = fanout_path(dir.path(), target_id).unwrap();
        std::fs::create_dir_all(target_path.parent().unwrap()).unwrap();
        gix::init_bare(&target_path).unwrap();

        // Build a source repo with a commit whose blob is 7 bytes ("initial") > 1 byte limit.
        let src_dir = TempDir::new().unwrap();
        bare_repo_with_main(src_dir.path(), TEST_REPO_H);
        let src_path = fanout_path(src_dir.path(), TEST_REPO_H).unwrap();
        let src_repo = gix::open(&src_path).unwrap();
        let tip_oid = src_repo.head_id().unwrap().detach();
        let pack_bytes = build_pack_bytes_from_repo(&src_repo, tip_oid);

        let mut client = make_grpc_client(dir.path().to_path_buf()).await;

        let ctx = PushContext {
            repository_id: target_id.to_string(),
            namespace: "ns".to_string(),
            repository_name: "repo".to_string(),
            config_resource_version: String::new(),
            policy: Some(PushPolicy {
                max_pack_size_bytes: 0,
                max_file_size_bytes: 1, // 1 byte — any blob will exceed this
            }),
            authorization: test_authorization(target_id, "repository.contents.write"),
        };

        let chunk = ReceivePackRequest {
            repository_id: target_id.to_string(),
            ref_commands: vec![RefCommand {
                old_oid: "0000000000000000000000000000000000000000".to_string(),
                new_oid: tip_oid.to_string(),
                ref_name: "refs/heads/main".to_string(),
            }],
            pack_data: pack_bytes,
            is_last: true,
            push_context: Some(ctx),
            shallow_oids: vec![],
        };

        let result = client.receive_pack(signed_test_stream(vec![chunk])).await;
        let status = result.unwrap_err();
        assert_eq!(
            status.code(),
            tonic::Code::ResourceExhausted,
            "blob exceeding max_file_size_bytes must return ResourceExhausted, got: {:?}",
            status
        );
    }

    #[tokio::test]
    async fn test_zero_policy_falls_back_to_ceiling_not_unlimited() {
        // A delete-only push skips pack staging entirely regardless of
        // policy, so a zero/absent repository policy here exercises no size
        // check either way — it must not be rejected for size reasons. The
        // effective-limit resolution itself (0/absent policy -> ceiling, not
        // "unlimited") is covered by test_ceiling_clamps_oversized_repository_policy.
        let dir = TempDir::new().unwrap();
        bare_repo_with_main(dir.path(), TEST_REPO_H);
        let mut client = make_grpc_client(dir.path().to_path_buf()).await;

        let repo_path = fanout_path(dir.path(), TEST_REPO_H).unwrap();
        let repo = gix::open(&repo_path).unwrap();
        let head_oid = repo.head_id().unwrap().to_string();

        let ctx = PushContext {
            repository_id: TEST_REPO_H.to_string(),
            namespace: "ns".to_string(),
            repository_name: "repo".to_string(),
            config_resource_version: String::new(),
            policy: Some(PushPolicy {
                max_pack_size_bytes: 0,
                max_file_size_bytes: 0,
            }),
            authorization: test_authorization(TEST_REPO_H, "repository.contents.write"),
        };
        let chunk = delete_ref_cmd_with_ctx(TEST_REPO_H, "refs/heads/main", &head_oid, ctx);
        let result = client.receive_pack(signed_test_stream(vec![chunk])).await;
        if let Err(status) = &result {
            assert_ne!(
                status.code(),
                tonic::Code::ResourceExhausted,
                "a delete-only push must never trigger size rejection"
            );
        }
    }

    #[tokio::test]
    async fn test_ceiling_clamps_oversized_repository_policy() {
        // The repository's own policy claims a 1GiB pack limit, but the
        // service's push_limits ceiling is 1 byte — effective limit must be
        // min(repository policy, ceiling) = the ceiling, not the repository
        // policy. Any non-trivial pack must therefore be rejected.
        let dir = TempDir::new().unwrap();
        bare_repo_with_main(dir.path(), TEST_REPO_I);
        let mut client = make_grpc_client_with_push_limits(
            dir.path().to_path_buf(),
            PushLimits {
                max_pack_size: 1,
                max_file_size: 1,
            },
        )
        .await;

        let ctx = PushContext {
            repository_id: TEST_REPO_I.to_string(),
            namespace: "ns".to_string(),
            repository_name: "repo".to_string(),
            config_resource_version: String::new(),
            policy: Some(PushPolicy {
                max_pack_size_bytes: 1024 * 1024 * 1024,
                max_file_size_bytes: 1024 * 1024 * 1024,
            }),
            authorization: test_authorization(TEST_REPO_I, "repository.contents.write"),
        };
        let chunk = ReceivePackRequest {
            repository_id: TEST_REPO_I.to_string(),
            ref_commands: vec![RefCommand {
                old_oid: "0000000000000000000000000000000000000000".to_string(),
                new_oid: "a".repeat(40),
                ref_name: "refs/heads/feature".to_string(),
            }],
            pack_data: b"PACK\x00\x00\x00\x02".to_vec(), // 8 bytes > 1-byte ceiling
            is_last: true,
            push_context: Some(ctx),
            shallow_oids: vec![],
        };

        let result = client.receive_pack(signed_test_stream(vec![chunk])).await;
        let status = result.unwrap_err();
        assert_eq!(
            status.code(),
            tonic::Code::ResourceExhausted,
            "a repository policy above the ceiling must still be clamped to the ceiling, got: {:?}",
            status
        );
    }

    #[tokio::test]
    async fn test_absent_repository_policy_uses_ceiling_as_effective_limit() {
        // No repository policy at all (None) — the effective limit must be
        // the service's ceiling, not "unlimited".
        let dir = TempDir::new().unwrap();
        bare_repo_with_main(dir.path(), TEST_REPO_I);
        let mut client = make_grpc_client_with_push_limits(
            dir.path().to_path_buf(),
            PushLimits {
                max_pack_size: 1,
                max_file_size: 1,
            },
        )
        .await;

        let ctx = PushContext {
            repository_id: TEST_REPO_I.to_string(),
            namespace: "ns".to_string(),
            repository_name: "repo".to_string(),
            config_resource_version: String::new(),
            policy: None,
            authorization: test_authorization(TEST_REPO_I, "repository.contents.write"),
        };
        let chunk = ReceivePackRequest {
            repository_id: TEST_REPO_I.to_string(),
            ref_commands: vec![RefCommand {
                old_oid: "0000000000000000000000000000000000000000".to_string(),
                new_oid: "a".repeat(40),
                ref_name: "refs/heads/feature".to_string(),
            }],
            pack_data: b"PACK\x00\x00\x00\x02".to_vec(), // 8 bytes > 1-byte ceiling
            is_last: true,
            push_context: Some(ctx),
            shallow_oids: vec![],
        };

        let result = client.receive_pack(signed_test_stream(vec![chunk])).await;
        let status = result.unwrap_err();
        assert_eq!(
            status.code(),
            tonic::Code::ResourceExhausted,
            "an absent repository policy must fall back to the ceiling, not unlimited, got: {:?}",
            status
        );
    }
}
