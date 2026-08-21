// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

use std::time::Duration;

use async_trait::async_trait;
use tracing::{error, info};

use super::{
    collect_blobs_from_tree, with_quarantine_repo, AdmissionDecision, HookContext, RefUpdate,
    ResourceBlob, ValidationHandler,
};
use crate::git::tree_diff::{
    collect_deleted_paths_from_trees, collect_paths_from_tree, get_tree_id,
};

pub mod catalog_proto {
    include!(concat!(
        env!("CARGO_MANIFEST_DIR"),
        "/gen/gitstore/catalog/v1/gitstore.catalog.v1.rs"
    ));
}

use catalog_proto::catalog_service_client::CatalogServiceClient;
use catalog_proto::{CategoryTaxonomyDeletionTree, CategoryTaxonomyDeletionValidationRequest};

const ZERO_OID: &str = "0000000000000000000000000000000000000000";

/// Validates only removals before receive-pack updates refs. The API receives
/// the complete old and proposed resource trees, so it can accept atomic child
/// deletion or reparenting without consulting stale admitted relationships.
pub struct CategoryTaxonomyDeletionHandler {
    client: CatalogServiceClient<tonic::transport::Channel>,
    timeout: Duration,
    repository_id: String,
}

impl CategoryTaxonomyDeletionHandler {
    pub fn new(
        client: CatalogServiceClient<tonic::transport::Channel>,
        timeout: Duration,
        repository_id: String,
    ) -> Self {
        Self {
            client,
            timeout,
            repository_id,
        }
    }

    pub async fn connect(
        url: &str,
        timeout: Duration,
        repository_id: String,
    ) -> anyhow::Result<Self> {
        let endpoint = tonic::transport::Endpoint::from_shared(url.to_string())?;
        Ok(Self::new(
            CatalogServiceClient::new(endpoint.connect_lazy()),
            timeout,
            repository_id,
        ))
    }

    async fn validate_trees(
        &self,
        trees: Vec<CategoryTaxonomyDeletionTree>,
        hook_ctx: &HookContext,
    ) -> anyhow::Result<AdmissionDecision> {
        let repository_id = if hook_ctx.repository_id.is_empty() {
            self.repository_id.clone()
        } else {
            hook_ctx.repository_id.clone()
        };
        let tree_count = trees.len();
        let start = std::time::Instant::now();
        let mut client = self.client.clone();
        let mut request = tonic::Request::new(CategoryTaxonomyDeletionValidationRequest {
            repository_id,
            trees,
        });
        request.set_timeout(self.timeout);
        let result = tokio::time::timeout(
            self.timeout,
            client.validate_category_taxonomy_deletion(request),
        )
        .await;
        let duration_ms = start.elapsed().as_millis() as u64;

        match result {
            Err(_) => {
                error!(
                    tree_count,
                    duration_ms,
                    outcome = "timeout",
                    "category_taxonomy_deletion_validation_complete"
                );
                Ok(AdmissionDecision::Reject(
                    "category deletion validation unavailable".to_string(),
                ))
            }
            Ok(Err(error)) => {
                error!(
                    tree_count,
                    duration_ms,
                    outcome = "service_unavailable",
                    error = %error,
                    "category_taxonomy_deletion_validation_complete"
                );
                Ok(AdmissionDecision::Reject(
                    "category deletion validation unavailable".to_string(),
                ))
            }
            Ok(Ok(response)) => {
                let response = response.into_inner();
                if response.accepted {
                    info!(
                        tree_count,
                        duration_ms,
                        outcome = "accepted",
                        "category_taxonomy_deletion_validation_complete"
                    );
                    Ok(AdmissionDecision::Accept)
                } else {
                    let reason = if response.reason.is_empty() {
                        "child categories present".to_string()
                    } else {
                        response.reason
                    };
                    info!(
                        tree_count,
                        duration_ms,
                        outcome = "rejected",
                        "category_taxonomy_deletion_validation_complete"
                    );
                    Ok(AdmissionDecision::Reject(reason))
                }
            }
        }
    }
}

#[async_trait]
impl ValidationHandler for CategoryTaxonomyDeletionHandler {
    async fn validate(
        &self,
        _blobs: &[ResourceBlob],
        _hook_ctx: &HookContext,
    ) -> anyhow::Result<AdmissionDecision> {
        Ok(AdmissionDecision::Accept)
    }

    async fn validate_receive(
        &self,
        git_dir: &std::path::Path,
        updates: &[RefUpdate],
        quarantine_dir: Option<&std::path::Path>,
        hook_ctx: &HookContext,
    ) -> anyhow::Result<AdmissionDecision> {
        let trees = with_quarantine_repo(git_dir, quarantine_dir, |repo| {
            collect_deletion_trees(repo, updates)
        })
        .map_err(anyhow::Error::msg)?;
        if trees.is_empty() {
            return Ok(AdmissionDecision::Accept);
        }
        self.validate_trees(trees, hook_ctx).await
    }
}

fn collect_deletion_trees(
    repo: &gix::Repository,
    updates: &[RefUpdate],
) -> Result<Vec<CategoryTaxonomyDeletionTree>, String> {
    let mut trees = Vec::new();

    for update in updates {
        if update.old_oid == ZERO_OID {
            continue;
        }
        let old_commit = gix::ObjectId::from_hex(update.old_oid.as_bytes())
            .map_err(|error| format!("parse old commit: {error}"))?;
        let old_tree =
            get_tree_id(repo, old_commit).ok_or_else(|| "resolve old commit tree".to_string())?;

        let mut deleted_paths = Vec::new();
        let proposed_blobs = if update.new_oid == ZERO_OID {
            collect_paths_from_tree(repo, old_tree, "", &mut deleted_paths);
            Vec::new()
        } else {
            let new_commit = gix::ObjectId::from_hex(update.new_oid.as_bytes())
                .map_err(|error| format!("parse new commit: {error}"))?;
            let new_tree = get_tree_id(repo, new_commit)
                .ok_or_else(|| "resolve proposed commit tree".to_string())?;
            collect_deleted_paths_from_trees(repo, old_tree, new_tree, "", &mut deleted_paths);
            if deleted_paths.is_empty() {
                continue;
            }
            let mut blobs = Vec::new();
            collect_blobs_from_tree(repo, new_tree, "", &mut blobs);
            blobs
        };

        if deleted_paths.is_empty() {
            continue;
        }
        let mut old_blobs = Vec::new();
        collect_blobs_from_tree(repo, old_tree, "", &mut old_blobs);
        trees.push(CategoryTaxonomyDeletionTree {
            old_blobs: old_blobs.into_iter().map(to_proto_blob).collect(),
            proposed_blobs: proposed_blobs.into_iter().map(to_proto_blob).collect(),
        });
    }
    Ok(trees)
}

fn to_proto_blob(blob: ResourceBlob) -> catalog_proto::ResourceBlob {
    catalog_proto::ResourceBlob {
        path: blob.path,
        blob_oid: blob.blob_oid,
        content: blob.content,
    }
}

#[cfg(test)]
mod tests {
    use std::path::{Path, PathBuf};
    use std::sync::{
        atomic::{AtomicUsize, Ordering},
        Arc,
    };

    use tonic::{transport::Server, Request, Response, Status};

    use super::*;
    use crate::git::hooks::category_taxonomy_deletion_handler::catalog_proto::{
        catalog_service_server::{CatalogService, CatalogServiceServer},
        AdmitResourcesRequest, AdmitResourcesResponse, CategoryTaxonomyDeletionValidationResponse,
        ValidateResourcesRequest, ValidateResourcesResponse,
    };

    struct MockCatalogService {
        calls: Arc<AtomicUsize>,
        accepted: bool,
    }

    #[tonic::async_trait]
    impl CatalogService for MockCatalogService {
        async fn validate_resources(
            &self,
            _request: Request<ValidateResourcesRequest>,
        ) -> Result<Response<ValidateResourcesResponse>, Status> {
            Ok(Response::new(ValidateResourcesResponse {
                accepted: true,
                errors: vec![],
            }))
        }

        async fn validate_category_taxonomy_deletion(
            &self,
            request: Request<CategoryTaxonomyDeletionValidationRequest>,
        ) -> Result<Response<CategoryTaxonomyDeletionValidationResponse>, Status> {
            self.calls.fetch_add(1, Ordering::SeqCst);
            let trees = request.into_inner().trees;
            assert_eq!(trees.len(), 1);
            assert!(trees[0].proposed_blobs.is_empty());
            assert_eq!(trees[0].old_blobs[0].path, "file.txt");
            Ok(Response::new(CategoryTaxonomyDeletionValidationResponse {
                accepted: self.accepted,
                reason: "child categories present".to_string(),
            }))
        }

        async fn admit_resources(
            &self,
            _request: Request<AdmitResourcesRequest>,
        ) -> Result<Response<AdmitResourcesResponse>, Status> {
            Ok(Response::new(AdmitResourcesResponse {}))
        }
    }

    async fn start_mock_server(calls: Arc<AtomicUsize>, accepted: bool) -> String {
        let listener = tokio::net::TcpListener::bind("127.0.0.1:0").await.unwrap();
        let address = listener.local_addr().unwrap();
        tokio::spawn(async move {
            Server::builder()
                .add_service(CatalogServiceServer::new(MockCatalogService {
                    calls,
                    accepted,
                }))
                .serve_with_incoming(tokio_stream::wrappers::TcpListenerStream::new(listener))
                .await
                .unwrap();
        });
        format!("http://{address}")
    }

    fn make_bare_repo(directory: &Path) -> PathBuf {
        let repository = directory.join("repo.git");
        gix::init_bare(&repository).unwrap();
        repository
    }

    fn make_commit(repository_path: &Path, contents: &str) -> String {
        let repository = gix::open(repository_path).unwrap();
        let signature = gix::actor::Signature {
            name: "test".into(),
            email: "test@example.com".into(),
            time: gix::date::Time::now_local_or_utc(),
        };
        let blob = repository.write_blob(contents.as_bytes()).unwrap().detach();
        let parent = repository
            .head_commit()
            .ok()
            .map(|commit| commit.id().detach());
        let parent_tree = parent
            .and_then(|id| repository.find_object(id).ok())
            .and_then(|object| object.try_into_commit().ok())
            .and_then(|commit| commit.tree_id().ok())
            .map(|id| id.detach())
            .unwrap_or_else(|| gix::ObjectId::empty_tree(gix::hash::Kind::Sha1));
        let tree = repository
            .edit_tree(parent_tree)
            .unwrap()
            .upsert("file.txt", gix::object::tree::EntryKind::Blob, blob)
            .unwrap()
            .write()
            .unwrap()
            .detach();
        let mut time_buf = gix::date::parse::TimeBuf::default();
        let signature_ref = signature.to_ref(&mut time_buf);
        let parents: Vec<gix::ObjectId> = parent.into_iter().collect();
        repository
            .commit_as(
                signature_ref,
                signature_ref,
                "HEAD",
                contents,
                tree,
                parents,
            )
            .unwrap()
            .detach()
            .to_string()
    }

    fn make_empty_commit(repository_path: &Path) -> String {
        let repository = gix::open(repository_path).unwrap();
        let signature = gix::actor::Signature {
            name: "test".into(),
            email: "test@example.com".into(),
            time: gix::date::Time::now_local_or_utc(),
        };
        let parent = repository.head_commit().unwrap().id().detach();
        let tree = gix::ObjectId::empty_tree(gix::hash::Kind::Sha1);
        let mut time_buf = gix::date::parse::TimeBuf::default();
        let signature_ref = signature.to_ref(&mut time_buf);
        repository
            .commit_as(
                signature_ref,
                signature_ref,
                "HEAD",
                "delete file",
                tree,
                [parent],
            )
            .unwrap()
            .detach()
            .to_string()
    }

    #[tokio::test]
    async fn creates_and_updates_do_not_call_the_api() {
        let directory = tempfile::TempDir::new().unwrap();
        let repository = make_bare_repo(directory.path());
        let old_oid = make_commit(&repository, "first");
        let new_oid = make_commit(&repository, "second");
        let handler = CategoryTaxonomyDeletionHandler::connect(
            "http://127.0.0.1:1",
            Duration::from_millis(20),
            "repo-1".to_string(),
        )
        .await
        .unwrap();

        let create = handler
            .validate_receive(
                &repository,
                &[RefUpdate {
                    ref_name: "refs/heads/main".to_string(),
                    old_oid: ZERO_OID.to_string(),
                    new_oid: old_oid.clone(),
                }],
                None,
                &HookContext::default(),
            )
            .await
            .unwrap();
        let update = handler
            .validate_receive(
                &repository,
                &[RefUpdate {
                    ref_name: "refs/heads/main".to_string(),
                    old_oid,
                    new_oid,
                }],
                None,
                &HookContext::default(),
            )
            .await
            .unwrap();

        assert!(matches!(create, AdmissionDecision::Accept));
        assert!(matches!(update, AdmissionDecision::Accept));
    }

    #[tokio::test]
    async fn rejected_deletion_maps_to_pre_receive_rejection() {
        let directory = tempfile::TempDir::new().unwrap();
        let repository = make_bare_repo(directory.path());
        let old_oid = make_commit(
            &repository,
            "---\napiVersion: catalog.gitstore.dev/v1beta1\nkind: CategoryTaxonomy\nmetadata:\n  name: parent\nspec:\n  title: Parent\n---\n",
        );
        let new_oid = make_empty_commit(&repository);
        let calls = Arc::new(AtomicUsize::new(0));
        let address = start_mock_server(Arc::clone(&calls), false).await;
        let handler = CategoryTaxonomyDeletionHandler::connect(
            &address,
            Duration::from_secs(1),
            "repo-1".to_string(),
        )
        .await
        .unwrap();

        let decision = handler
            .validate_receive(
                &repository,
                &[RefUpdate {
                    ref_name: "refs/heads/main".to_string(),
                    old_oid,
                    new_oid,
                }],
                None,
                &HookContext::default(),
            )
            .await
            .unwrap();

        assert_eq!(calls.load(Ordering::SeqCst), 1);
        assert!(matches!(
            decision,
            AdmissionDecision::Reject(reason) if reason == "child categories present"
        ));
    }

    #[tokio::test]
    async fn unavailable_api_rejects_deletion() {
        let directory = tempfile::TempDir::new().unwrap();
        let repository = make_bare_repo(directory.path());
        let old_oid = make_commit(&repository, "first");
        let handler = CategoryTaxonomyDeletionHandler::connect(
            "http://127.0.0.1:1",
            Duration::from_millis(100),
            "repo-1".to_string(),
        )
        .await
        .unwrap();

        let decision = handler
            .validate_receive(
                &repository,
                &[RefUpdate {
                    ref_name: "refs/heads/main".to_string(),
                    old_oid,
                    new_oid: ZERO_OID.to_string(),
                }],
                None,
                &HookContext::default(),
            )
            .await
            .unwrap();

        assert!(matches!(
            decision,
            AdmissionDecision::Reject(reason) if reason == "category deletion validation unavailable"
        ));
    }
}
