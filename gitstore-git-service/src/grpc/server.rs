// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

// gRPC service implementation for the GitService contract (gitstore.git.v1).

#![allow(clippy::result_large_err)]

use dashmap::DashMap;
use std::path::{Path, PathBuf};
use std::sync::Arc;
use tokio::sync::RwLock;
use tonic::{Request, Response, Status};

use crate::git::hooks::{HookPipeline, NoopAdmissionHandler, NoopValidationHandler, RefUpdate};
use crate::git::repo::{create_repository, delete_repository, fanout_path, list_tags};
use tracing::{error, info};

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

/// T047: Convert a proto PushContext into a HookContext for the pipeline.
impl From<&PushContext> for HookContext {
    fn from(ctx: &PushContext) -> Self {
        let actor = ctx.actor.as_ref();
        HookContext {
            actor_subject: actor.map(|a| a.subject.clone()).unwrap_or_default(),
            actor_auth_method: actor.map(|a| a.auth_method.clone()).unwrap_or_default(),
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
        }
    }
}

pub struct GitServiceImpl {
    pub data_root: Arc<PathBuf>,
    pub repo_locks: Arc<DashMap<String, Arc<RwLock<()>>>>,
    pub hook_pipeline: Arc<HookPipeline>,
}

impl GitServiceImpl {
    pub fn new(data_root: PathBuf) -> Self {
        use crate::config::{GitReceivePackHooks, HookToggle};
        let default_hooks = GitReceivePackHooks {
            pre_receive: HookToggle { enabled: false },
            update: HookToggle { enabled: false },
            post_receive: HookToggle { enabled: false },
            proc_receive: HookToggle { enabled: false },
            post_update: HookToggle { enabled: false },
            reference_transaction: HookToggle { enabled: false },
        };
        Self::with_pipeline(
            data_root,
            Arc::new(HookPipeline::new(
                default_hooks,
                "pre-receive".to_string(),
                std::time::Duration::from_secs(10),
                "post-receive".to_string(),
                "refs/heads/main".to_string(),
                Arc::new(NoopValidationHandler),
                Arc::new(NoopAdmissionHandler),
            )),
        )
    }

    pub fn with_pipeline(data_root: PathBuf, hook_pipeline: Arc<HookPipeline>) -> Self {
        Self {
            data_root: Arc::new(data_root),
            repo_locks: Arc::new(DashMap::new()),
            hook_pipeline,
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

/// Get or insert a per-repository lock.
fn get_or_insert_lock(repo_locks: &DashMap<String, Arc<RwLock<()>>>, id: &str) -> Arc<RwLock<()>> {
    repo_locks
        .entry(id.to_string())
        .or_insert_with(|| Arc::new(RwLock::new(())))
        .clone()
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

/// Resolve a ref to a gix commit (returned as ObjectId so it is not bound to repo lifetime).
/// Annotated tags are peeled to their target commit before conversion.
fn resolve_ref_to_commit_id(
    repo: &gix::Repository,
    ref_str: &str,
) -> Result<gix::ObjectId, Status> {
    let id = repo
        .rev_parse_single(ref_str.as_bytes())
        .map_err(|e| Status::not_found(format!("ref '{}' not found: {}", ref_str, e)))?;
    let commit = id
        .object()
        .map_err(|e| Status::internal(e.to_string()))?
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
        let req = request.into_inner();
        let repo_path = fanout_path(&self.data_root, &req.repository_id)?;

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

        get_or_insert_lock(&self.repo_locks, &req.repository_id);

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
    }

    async fn delete_repository(
        &self,
        request: Request<DeleteRepositoryRequest>,
    ) -> Result<Response<DeleteRepositoryResponse>, Status> {
        let req = request.into_inner();
        let repo_path = resolve_repo_path(&self.data_root, &req.repository_id)?;

        let lock = get_or_insert_lock(&self.repo_locks, &req.repository_id);
        let _guard = lock.write().await;

        info!(
            repo_id = %req.repository_id,
            storage_path = %repo_path.display(),
            "deleting repository"
        );

        delete_repository(&repo_path)
            .map_err(|e| Status::internal(format!("failed to delete repository: {}", e)))?;

        self.repo_locks.remove(&req.repository_id);

        Ok(Response::new(DeleteRepositoryResponse {
            repository_id: req.repository_id,
        }))
    }

    async fn get_file(
        &self,
        request: Request<GetFileRequest>,
    ) -> Result<Response<GetFileResponse>, Status> {
        let req = request.into_inner();
        let repo_path = resolve_repo_path(&self.data_root, &req.repository_id)?;
        let lock = get_or_insert_lock(&self.repo_locks, &req.repository_id);
        let _guard = lock.read().await;

        tokio::task::spawn_blocking(move || {
            let repo = gix::open(&repo_path)
                .map_err(|e| Status::internal(format!("failed to open repo: {}", e)))?;

            let ref_str = if req.r#ref.is_empty() {
                "HEAD".to_string()
            } else {
                req.r#ref.clone()
            };
            let commit_id = resolve_ref_to_commit_id(&repo, &ref_str)?;
            let commit = repo
                .find_object(commit_id)
                .map_err(|e| Status::internal(e.to_string()))?
                .try_into_commit()
                .map_err(|_| Status::internal("not a commit"))?;

            let tree_id = commit
                .tree_id()
                .map_err(|e| Status::internal(e.to_string()))?
                .detach();

            // Navigate to the file entry
            let entry_oid = find_blob_in_tree(&repo, tree_id, &req.path)?;

            let blob = repo
                .find_object(entry_oid)
                .map_err(|e| Status::internal(e.to_string()))?;
            let content = blob.data.clone();
            let size_bytes = content.len() as u64;
            let blob_sha = entry_oid.to_string();

            Ok(Response::new(GetFileResponse {
                path: req.path,
                content,
                blob_sha,
                size_bytes,
            }))
        })
        .await
        .map_err(|e| Status::internal(format!("task join error: {}", e)))?
    }

    type GetFileStreamStream =
        tokio_stream::wrappers::ReceiverStream<Result<GetFileStreamResponse, Status>>;

    async fn get_file_stream(
        &self,
        request: Request<GetFileStreamRequest>,
    ) -> Result<Response<Self::GetFileStreamStream>, Status> {
        let req = request.into_inner();
        let repo_path = resolve_repo_path(&self.data_root, &req.repository_id)?;
        let lock = get_or_insert_lock(&self.repo_locks, &req.repository_id);
        let _guard = lock.read().await;

        let (tx, rx) = tokio::sync::mpsc::channel(16);

        tokio::task::spawn_blocking(move || {
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
                    send(Err(e));
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
        let req = request.into_inner();
        let repo_path = resolve_repo_path(&self.data_root, &req.repository_id)?;
        let lock = get_or_insert_lock(&self.repo_locks, &req.repository_id);
        let _guard = lock.read().await;

        tokio::task::spawn_blocking(move || {
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
        let req = request.into_inner();
        let repo_path = resolve_repo_path(&self.data_root, &req.repository_id)?;
        let lock = get_or_insert_lock(&self.repo_locks, &req.repository_id);
        let _guard = lock.write().await;

        tokio::task::spawn_blocking(move || {
            validate_file_path(&req.path)?;

            let repo = gix::open(&repo_path)
                .map_err(|e| Status::internal(format!("failed to open repo: {}", e)))?;

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

            // Write blob
            let blob_oid: gix::ObjectId = repo
                .write_blob(&req.content)
                .map_err(|e| Status::internal(format!("write_blob: {}", e)))?
                .detach();

            // Get current HEAD state (may be empty repo)
            let maybe_head = repo.head_commit().ok();

            let (new_tree_id, parents): (gix::ObjectId, Vec<gix::ObjectId>) =
                if let Some(head_commit) = maybe_head {
                    let tree_id = head_commit
                        .tree_id()
                        .map_err(|e| Status::internal(e.to_string()))?
                        .detach();

                    let new_tree = repo
                        .edit_tree(tree_id)
                        .map_err(|e| Status::internal(format!("edit_tree: {}", e)))?
                        .upsert(
                            req.path.as_str(),
                            gix::object::tree::EntryKind::Blob,
                            blob_oid,
                        )
                        .map_err(|e| Status::internal(format!("upsert: {}", e)))?
                        .write()
                        .map_err(|e| Status::internal(format!("tree write: {}", e)))?;

                    let parent_id = head_commit.id().detach();
                    (new_tree.detach(), vec![parent_id])
                } else {
                    // Empty repo: build tree from scratch
                    let new_tree = repo
                        .edit_tree(gix::ObjectId::empty_tree(gix::hash::Kind::Sha1))
                        .map_err(|e| Status::internal(format!("edit_tree: {}", e)))?
                        .upsert(
                            req.path.as_str(),
                            gix::object::tree::EntryKind::Blob,
                            blob_oid,
                        )
                        .map_err(|e| Status::internal(format!("upsert: {}", e)))?
                        .write()
                        .map_err(|e| Status::internal(format!("tree write: {}", e)))?;
                    (new_tree.detach(), vec![])
                };

            let mut time_buf = gix::date::parse::TimeBuf::default();
            let sig_ref = sig.to_ref(&mut time_buf);
            let commit_id = repo
                .commit_as(
                    sig_ref,
                    sig_ref,
                    "HEAD",
                    &req.commit_message,
                    new_tree_id,
                    parents.iter().copied(),
                )
                .map_err(|e| Status::internal(format!("commit: {}", e)))?;

            Ok(Response::new(CommitFileResponse {
                commit_sha: commit_id.to_string(),
                branch: "main".to_string(),
            }))
        })
        .await
        .map_err(|e| Status::internal(format!("task join error: {}", e)))?
    }

    async fn delete_file(
        &self,
        request: Request<DeleteFileRequest>,
    ) -> Result<Response<DeleteFileResponse>, Status> {
        let req = request.into_inner();
        let repo_path = resolve_repo_path(&self.data_root, &req.repository_id)?;
        let lock = get_or_insert_lock(&self.repo_locks, &req.repository_id);
        let _guard = lock.write().await;

        tokio::task::spawn_blocking(move || {
            validate_file_path(&req.path)?;

            let repo = gix::open(&repo_path)
                .map_err(|e| Status::internal(format!("failed to open repo: {}", e)))?;

            let head_commit = repo
                .head_commit()
                .map_err(|e| Status::internal(format!("head commit: {}", e)))?;

            // Check the file exists in the tree first
            let tree_id = head_commit
                .tree_id()
                .map_err(|e| Status::internal(e.to_string()))?
                .detach();

            find_blob_in_tree(&repo, tree_id, &req.path)?;

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
            let commit_id = repo
                .commit_as(
                    sig_ref,
                    sig_ref,
                    "HEAD",
                    &req.commit_message,
                    new_tree.detach(),
                    std::iter::once(parent_id),
                )
                .map_err(|e| Status::internal(format!("commit: {}", e)))?;

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
        let req = request.into_inner();
        let repo_path = resolve_repo_path(&self.data_root, &req.repository_id)?;
        let lock = get_or_insert_lock(&self.repo_locks, &req.repository_id);
        let _guard = lock.write().await;

        tokio::task::spawn_blocking(move || {
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
        let req = request.into_inner();
        let repo_path = resolve_repo_path(&self.data_root, &req.repository_id)?;
        let lock = get_or_insert_lock(&self.repo_locks, &req.repository_id);
        let _guard = lock.read().await;

        tokio::task::spawn_blocking(move || {
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
        let req = request.into_inner();
        let repo_path = resolve_repo_path(&self.data_root, &req.repository_id)?;
        let lock = get_or_insert_lock(&self.repo_locks, &req.repository_id);
        let _guard = lock.read().await;
        let service_enum = req.service;

        let advertisement = tokio::task::spawn_blocking(move || {
            let pack_server = crate::git::pack_server::HttpPackServer::new(repo_path, 0);
            let svc = proto::Service::try_from(service_enum).unwrap_or(proto::Service::Unspecified);
            match svc {
                proto::Service::GitUploadPack => pack_server
                    .advertise_upload_pack_refs()
                    .map_err(|e| Status::internal(format!("upload-pack advertise: {}", e))),
                proto::Service::GitReceivePack => pack_server
                    .advertise_receive_pack_refs()
                    .map_err(|e| Status::internal(format!("receive-pack advertise: {}", e))),
                _ => Err(Status::invalid_argument("unknown service")),
            }
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
        let mut stream = request.into_inner();

        // First chunk carries repo_id + ref_commands + optional initial pack_data
        let first = stream
            .message()
            .await
            .map_err(|e| Status::internal(format!("recv first chunk: {}", e)))?
            .ok_or_else(|| Status::invalid_argument("empty stream"))?;

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

        // T041: Extract pack size limit before any pack I/O.
        let max_pack_size_bytes: i64 = push_ctx
            .policy
            .as_ref()
            .map(|p| p.max_pack_size_bytes)
            .unwrap_or(0);

        // T051: Build HookContext from validated PushContext.
        let hook_ctx = crate::git::hooks::HookContext::from(push_ctx);

        let repo_path = resolve_repo_path(&self.data_root, &repo_id)?;
        let lock = get_or_insert_lock(&self.repo_locks, &repo_id);
        let _guard = lock.write().await;

        info!(repo_id = %repo_id, "receive_pack: stream start");

        let ref_commands = first.ref_commands.clone();
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
                            // Limit exceeded — drop tx2 so stage_pack_from_reader sees EOF
                            // and will propagate an error.
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
            let q = tokio::task::spawn_blocking(move || {
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

        // T042: Enforce blob size limit on quarantined objects before any ref update.
        let max_file_size_bytes: i64 = first
            .push_context
            .as_ref()
            .and_then(|ctx| ctx.policy.as_ref())
            .map(|p| p.max_file_size_bytes)
            .unwrap_or(0);
        if max_file_size_bytes > 0 {
            if let Some(ref q) = quarantine {
                let pack_path = q.pack_path.clone();
                let index_path = q.index_path.clone();
                let limit = max_file_size_bytes as u64;
                tokio::task::spawn_blocking(move || {
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
        let (nff_rejected, pipeline_updates) = tokio::task::spawn_blocking(move || {
            let repo = gix::open(&repo_path_nff)
                .map_err(|e| Status::internal(format!("open repo for nff: {}", e)))?;
            let mut rejected = Vec::new();
            let mut valid = Vec::new();
            for update in &ref_updates_clone {
                let is_non_ff = validate_ref_command(&repo, &update.ref_name, &update.old_oid)
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

        // Build ref_edits, prepare the gix transaction (acquires lock files), run the
        // reference-transaction/prepared veto *while locks are held*, then commit or rollback.
        // All of this runs in spawn_blocking because gix::Repository is !Send.
        // block_in_place is used inside to drive the async veto call.
        let pipeline_updates_for_txn = pipeline_updates.clone();
        let accepted_indices_clone = accepted_indices.clone();
        let accepted_updates_clone = accepted_updates.clone();
        let accepted_updates_for_callbacks = accepted_updates.clone();
        let repo_path_commit = repo_path.clone();
        let pipeline_clone = Arc::clone(&pipeline);
        let hook_ctx_txn = hook_ctx.clone();
        let hook_ctx_post = hook_ctx.clone();
        // Result is either Ok(rt_committed) or Err(Status); we also need to know if the
        // reference-transaction veto fired so we can call the right observation callback.
        enum TxnOutcome {
            Committed,
            RejectedByHook(String), // veto reason
        }
        let txn_result: Result<TxnOutcome, Status> = tokio::task::spawn_blocking(move || {
            use gix::refs::transaction::{Change, LogChange, PreviousValue, RefEdit, RefLog};

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

            if ref_edits.is_empty() {
                return Ok(TxnOutcome::Committed);
            }

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

            // Run the veto hook while locks are held (matches the prepared state semantics).
            let veto = tokio::task::block_in_place(|| {
                tokio::runtime::Handle::current().block_on(
                    pipeline_clone.run_reference_transaction_prepared(
                        &repo_path_commit,
                        &accepted_updates_clone,
                        &hook_ctx_txn,
                    ),
                )
            });

            match veto {
                Err(rejection) => {
                    drop(txn); // releases all lock files
                    Ok(TxnOutcome::RejectedByHook(rejection.reason))
                }
                Ok(()) => {
                    if let Some(q) = quarantine {
                        crate::git::pack_server::promote_quarantine(&repo, q)
                            .map_err(|e| Status::internal(format!("promote quarantine: {}", e)))?;
                    }
                    txn.commit(None)
                        .map_err(|e| Status::internal(format!("commit ref transaction: {}", e)))?;
                    Ok(TxnOutcome::Committed)
                }
            }
        })
        .await
        .map_err(|e| Status::internal(format!("commit join: {}", e)))?;

        match txn_result? {
            TxnOutcome::Committed => {
                pipeline.run_reference_transaction_committed(
                    &repo_path,
                    &accepted_updates_for_callbacks,
                );
            }
            TxnOutcome::RejectedByHook(reason) => {
                pipeline
                    .run_reference_transaction_aborted(&repo_path, &accepted_updates_for_callbacks);
                let all_refs: Vec<&str> = ref_updates.iter().map(|u| u.ref_name.as_str()).collect();
                let report_status = build_rejection_status(&all_refs, &reason);
                return Ok(Response::new(ReceivePackResponse { report_status }));
            }
        }

        info!(repo_id = %repo_id, "receive_pack: refs committed");

        pipeline.run_post_receive(
            &repo_path,
            &accepted_updates_for_callbacks,
            &repo_id,
            &hook_ctx_post,
        );

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

    type UploadPackStream =
        tokio_stream::wrappers::ReceiverStream<Result<UploadPackResponse, Status>>;

    async fn upload_pack(
        &self,
        request: Request<UploadPackRequest>,
    ) -> Result<Response<Self::UploadPackStream>, Status> {
        let req = request.into_inner();
        let repo_path = resolve_repo_path(&self.data_root, &req.repository_id)?;
        let lock = get_or_insert_lock(&self.repo_locks, &req.repository_id);
        let _guard = lock.read().await;

        info!(repo_id = %req.repository_id, "upload_pack: start");

        let (tx, rx) = tokio::sync::mpsc::channel(16);

        tokio::task::spawn_blocking(move || {
            let pack_server = crate::git::pack_server::HttpPackServer::new(repo_path, 0);
            match pack_server.handle_upload_pack(&req.body) {
                Ok(pack_bytes) => {
                    const CHUNK_SIZE: usize = 64 * 1024;
                    let chunks: Vec<&[u8]> = pack_bytes.chunks(CHUNK_SIZE).collect();
                    let last = chunks.len().saturating_sub(1);
                    for (i, chunk) in chunks.iter().enumerate() {
                        let is_last = i == last;
                        let msg = Ok(UploadPackResponse {
                            chunk_index: i as u32,
                            data: chunk.to_vec(),
                            is_last,
                        });
                        info!(
                            chunk_index = i,
                            bytes = chunk.len(),
                            "upload_pack: chunk sent"
                        );
                        let _ = tx.blocking_send(msg);
                    }
                    if pack_bytes.is_empty() {
                        let _ = tx.blocking_send(Ok(UploadPackResponse {
                            chunk_index: 0,
                            data: vec![],
                            is_last: true,
                        }));
                    }
                }
                Err(e) => {
                    error!(error = %e, "upload_pack error");
                    let _ = tx.blocking_send(Err(Status::internal(format!("upload-pack: {}", e))));
                }
            }
        });

        Ok(Response::new(tokio_stream::wrappers::ReceiverStream::new(
            rx,
        )))
    }

    async fn get_latest_tag(
        &self,
        request: Request<GetLatestTagRequest>,
    ) -> Result<Response<GetLatestTagResponse>, Status> {
        let req = request.into_inner();
        let repo_path = resolve_repo_path(&self.data_root, &req.repository_id)?;
        let lock = get_or_insert_lock(&self.repo_locks, &req.repository_id);
        let _guard = lock.read().await;

        tokio::task::spawn_blocking(move || {
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
fn validate_ref_command(
    repo: &gix::Repository,
    ref_name: &str,
    old_oid: &str,
) -> Result<bool, Status> {
    let zero = "0000000000000000000000000000000000000000";
    if old_oid == zero {
        return Ok(false);
    }
    match repo.find_reference(ref_name) {
        Ok(r) => {
            let current_oid = r
                .target()
                .try_id()
                .map(|id| id.to_string())
                .unwrap_or_default();
            if current_oid != old_oid {
                return Ok(true);
            }
            Ok(false)
        }
        Err(_) => Ok(false),
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

    let decoded = tree.decode().map_err(|e| Status::internal(e.to_string()))?;

    let mut parts = path.splitn(2, '/');
    let first = parts.next().unwrap_or("");
    let rest = parts.next();

    for entry in &decoded.entries {
        if entry.filename == gix::bstr::BStr::new(first.as_bytes()) {
            let oid = gix::ObjectId::from(entry.oid);
            return match rest {
                Some(remaining) => find_blob_in_tree(repo, oid, remaining),
                None => {
                    use gix::object::tree::EntryKind;
                    match entry.mode.kind() {
                        EntryKind::Blob | EntryKind::BlobExecutable => Ok(oid),
                        _ => Err(Status::not_found(format!("'{}' is not a file", path))),
                    }
                }
            };
        }
    }

    Err(Status::not_found(format!("file '{}' not found", path)))
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

    fn make_test_service(data_root: &std::path::Path) -> GitServiceImpl {
        GitServiceImpl::new(data_root.to_path_buf())
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

    fn make_create_req(id: &str) -> Request<CreateRepositoryRequest> {
        Request::new(CreateRepositoryRequest {
            repository_id: id.to_string(),
            storage_class: String::new(),
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
    async fn test_delete_repository_succeeds() {
        let dir = TempDir::new().unwrap();
        let svc = make_test_service(dir.path());
        svc.create_repository(make_create_req(TEST_REPO_C))
            .await
            .unwrap();
        let repo_path = fanout_path(dir.path(), TEST_REPO_C).unwrap();
        assert!(repo_path.exists());

        let resp = svc
            .delete_repository(Request::new(DeleteRepositoryRequest {
                repository_id: TEST_REPO_C.to_string(),
            }))
            .await
            .unwrap()
            .into_inner();
        assert_eq!(resp.repository_id, TEST_REPO_C);
        assert!(!repo_path.exists());
    }

    #[tokio::test]
    async fn test_delete_repository_not_found() {
        let dir = TempDir::new().unwrap();
        let svc = make_test_service(dir.path());
        let err = svc
            .delete_repository(Request::new(DeleteRepositoryRequest {
                repository_id: TEST_REPO_A.to_string(),
            }))
            .await
            .unwrap_err();
        assert_eq!(err.code(), tonic::Code::NotFound);
    }

    #[tokio::test]
    async fn test_operation_on_unknown_repo_returns_not_found() {
        let dir = TempDir::new().unwrap();
        let svc = make_test_service(dir.path());
        let err = svc
            .get_file(Request::new(GetFileRequest {
                repository_id: TEST_REPO_A.to_string(),
                path: "README.md".to_string(),
                r#ref: "HEAD".to_string(),
            }))
            .await
            .unwrap_err();
        assert_eq!(err.code(), tonic::Code::NotFound);
    }

    #[tokio::test]
    async fn test_invalid_repo_id_rejected() {
        let dir = TempDir::new().unwrap();
        let svc = make_test_service(dir.path());

        // Non-UUID names must be rejected with INVALID_ARGUMENT
        for bad_name in &["", "myrepo", "../etc", "a/b", "a\\b"] {
            let err = svc
                .get_file(Request::new(GetFileRequest {
                    repository_id: bad_name.to_string(),
                    path: "README.md".to_string(),
                    r#ref: "HEAD".to_string(),
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
    async fn test_get_file_happy_path() {
        let dir = TempDir::new().unwrap();
        repo_with_commit(dir.path(), TEST_REPO_D);

        let svc = make_test_service(dir.path());
        let req = Request::new(GetFileRequest {
            repository_id: TEST_REPO_D.to_string(),
            path: "products/p1.md".to_string(),
            r#ref: "HEAD".to_string(),
        });
        let resp = svc.get_file(req).await.unwrap();
        assert_eq!(resp.into_inner().content, b"---\nid: p1\n---\nhello");
    }

    #[tokio::test]
    async fn test_get_file_unknown_ref_returns_not_found() {
        let dir = TempDir::new().unwrap();
        repo_with_commit(dir.path(), TEST_REPO_D);

        let svc = make_test_service(dir.path());
        let req = Request::new(GetFileRequest {
            repository_id: TEST_REPO_D.to_string(),
            path: "products/p1.md".to_string(),
            r#ref: "nonexistent-branch".to_string(),
        });
        let err = svc.get_file(req).await.unwrap_err();
        assert_eq!(err.code(), tonic::Code::NotFound);
    }

    #[tokio::test]
    async fn test_get_file_missing_file_returns_not_found() {
        let dir = TempDir::new().unwrap();
        repo_with_commit(dir.path(), TEST_REPO_D);

        let svc = make_test_service(dir.path());
        let req = Request::new(GetFileRequest {
            repository_id: TEST_REPO_D.to_string(),
            path: "products/nonexistent.md".to_string(),
            r#ref: "HEAD".to_string(),
        });
        let err = svc.get_file(req).await.unwrap_err();
        assert_eq!(err.code(), tonic::Code::NotFound);
    }

    #[tokio::test]
    async fn test_list_files_returns_tree_entries() {
        let dir = TempDir::new().unwrap();
        repo_with_commit(dir.path(), TEST_REPO_D);

        let svc = make_test_service(dir.path());
        let req = Request::new(ListFilesRequest {
            repository_id: TEST_REPO_D.to_string(),
            r#ref: "HEAD".to_string(),
            path_prefix: "products/".to_string(),
            recursive: true,
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
        let req = Request::new(GetLatestTagRequest {
            repository_id: TEST_REPO_D.to_string(),
            prefix: "v".to_string(),
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
        let req = Request::new(GetLatestTagRequest {
            repository_id: TEST_REPO_A.to_string(),
            prefix: "v".to_string(),
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
        let req = Request::new(CommitFileRequest {
            repository_id: TEST_REPO_E.to_string(),
            path: "products/new.md".to_string(),
            content: b"---\nid: new\n---".to_vec(),
            commit_message: "add new product".to_string(),
            author_name: "Tester".to_string(),
            author_email: "test@example.com".to_string(),
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

    #[tokio::test]
    async fn test_delete_file_on_nonexistent_file_returns_not_found() {
        let dir = TempDir::new().unwrap();
        bare_repo_with_main(dir.path(), TEST_REPO_E);

        let svc = make_test_service(dir.path());
        let req = Request::new(DeleteFileRequest {
            repository_id: TEST_REPO_E.to_string(),
            path: "products/missing.md".to_string(),
            commit_message: "delete".to_string(),
            author_name: "T".to_string(),
            author_email: "t@t.com".to_string(),
        });
        let err = svc.delete_file(req).await.unwrap_err();
        assert_eq!(err.code(), tonic::Code::NotFound);
    }

    #[tokio::test]
    async fn test_create_tag_already_exists_returns_already_exists() {
        let dir = TempDir::new().unwrap();
        repo_with_commit(dir.path(), TEST_REPO_D); // creates v1.0.0

        let svc = make_test_service(dir.path());
        let req = Request::new(CreateTagRequest {
            repository_id: TEST_REPO_D.to_string(),
            tag_name: "v1.0.0".to_string(),
            message: "duplicate".to_string(),
            target_commit_sha: "".to_string(),
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
                .commit_file(Request::new(CommitFileRequest {
                    repository_id: TEST_REPO_F.to_string(),
                    path: "file-a.md".to_string(),
                    content: b"a".to_vec(),
                    commit_message: "from a".to_string(),
                    author_name: "A".to_string(),
                    author_email: "a@a.com".to_string(),
                }))
                .await
                .unwrap()
        });

        let h_b = tokio::spawn(async move {
            svc_b
                .commit_file(Request::new(CommitFileRequest {
                    repository_id: TEST_REPO_G.to_string(),
                    path: "file-b.md".to_string(),
                    content: b"b".to_vec(),
                    commit_message: "from b".to_string(),
                    author_name: "B".to_string(),
                    author_email: "b@b.com".to_string(),
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
                .add_service(GitServiceServer::new(svc))
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
        }
    }

    /// Returns a minimal valid PushContext for `repo_id`.
    fn minimal_push_ctx(repo_id: &str) -> PushContext {
        PushContext {
            repository_id: repo_id.to_string(),
            namespace: "ns".to_string(),
            repository_name: "repo".to_string(),
            config_resource_version: String::new(),
            actor: None,
            policy: None,
        }
    }

    /// Build a full pack file containing all objects reachable from `tip_oid` in `src_repo`.
    /// Returns raw PACK bytes suitable for inclusion in a ReceivePackRequest.
    fn build_pack_bytes_from_repo(src_repo: &gix::Repository, tip_oid: gix::ObjectId) -> Vec<u8> {
        crate::git::pack_server::build_pack_for_wants(src_repo, &[tip_oid.to_string()], &[])
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
        let stream = tokio_stream::iter(chunks);
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
        };
        let result = client.receive_pack(tokio_stream::iter(vec![chunk])).await;
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
            actor: None,
            policy: Some(PushPolicy {
                max_pack_size_bytes: 1,
                max_file_size_bytes: 0,
            }),
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
        };

        let result = client.receive_pack(tokio_stream::iter(vec![chunk])).await;
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
            actor: None,
            policy: Some(PushPolicy {
                max_pack_size_bytes: 0,
                max_file_size_bytes: 1, // 1 byte — any blob will exceed this
            }),
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
        };

        let result = client.receive_pack(tokio_stream::iter(vec![chunk])).await;
        let status = result.unwrap_err();
        assert_eq!(
            status.code(),
            tonic::Code::ResourceExhausted,
            "blob exceeding max_file_size_bytes must return ResourceExhausted, got: {:?}",
            status
        );
    }

    #[tokio::test]
    async fn test_zero_limits_mean_unlimited() {
        let dir = TempDir::new().unwrap();
        bare_repo_with_main(dir.path(), TEST_REPO_H);
        let mut client = make_grpc_client(dir.path().to_path_buf()).await;

        let repo_path = fanout_path(dir.path(), TEST_REPO_H).unwrap();
        let repo = gix::open(&repo_path).unwrap();
        let head_oid = repo.head_id().unwrap().to_string();

        // Both limits = 0 (unlimited) — a delete-only push with a large push_context should succeed
        let ctx = PushContext {
            repository_id: TEST_REPO_H.to_string(),
            namespace: "ns".to_string(),
            repository_name: "repo".to_string(),
            config_resource_version: String::new(),
            actor: None,
            policy: Some(PushPolicy {
                max_pack_size_bytes: 0,
                max_file_size_bytes: 0,
            }),
        };
        let chunk = delete_ref_cmd_with_ctx(TEST_REPO_H, "refs/heads/main", &head_oid, ctx);
        let result = client.receive_pack(tokio_stream::iter(vec![chunk])).await;
        // With zero limits the push should not be rejected for size reasons;
        // it may fail for other reasons (e.g. ref update semantics) but not ResourceExhausted
        if let Err(status) = &result {
            assert_ne!(
                status.code(),
                tonic::Code::ResourceExhausted,
                "zero limits must never trigger size rejection"
            );
        }
    }
}
