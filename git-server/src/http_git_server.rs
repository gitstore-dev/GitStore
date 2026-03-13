// HTTP Git Server Implementation
//
// Implements git push/pull over HTTP with smart protocol support
// Includes pre-receive hooks for validation

use axum::{
    body::{Body, Bytes},
    extract::{Path, Query, State},
    http::{header, StatusCode},
    response::{IntoResponse, Response},
    routing::{get, post},
    Router,
};
use serde::Deserialize;
use git2::Repository;
use std::path::PathBuf;
use std::sync::Arc;
use tokio::sync::RwLock;
use tracing::{debug, error, info};

use crate::validation::validator::Validator;
use crate::websocket::broadcast::Broadcaster;

/// Git server state shared across handlers
#[derive(Clone)]
pub struct GitServerState {
    pub repo_path: PathBuf,
    pub validator: Arc<Validator>,
    pub broadcaster: Arc<RwLock<Broadcaster>>,
}

/// Create HTTP git server routes
pub fn create_git_routes(state: GitServerState) -> Router {
    Router::new()
        // Smart HTTP protocol endpoints
        .route("/{repo}/info/refs", get(info_refs))
        .route("/{repo}/git-upload-pack", post(upload_pack))
        .route("/{repo}/git-receive-pack", post(receive_pack))
        // Health check
        .route("/health", get(health_check))
        .with_state(state)
}

/// Query parameters for info/refs endpoint
#[derive(Deserialize)]
struct InfoRefsQuery {
    service: Option<String>,
}

/// Handle GET /:repo/info/refs
///
/// Returns repository capabilities for git clone/fetch/push
async fn info_refs(
    State(state): State<GitServerState>,
    Path(repo): Path<String>,
    Query(query): Query<InfoRefsQuery>,
) -> Result<Response, GitError> {
    debug!(repo = %repo, "info_refs request");

    // Check service type from query parameter
    let service = query.service.as_deref().unwrap_or("");

    let repo_path = state.repo_path.join(&repo);
    let repository = Repository::open(&repo_path)
        .map_err(|e| GitError::NotFound(format!("Repository not found: {}", e)))?;

    match service {
        "git-upload-pack" => {
            // For git clone/fetch
            let output = std::process::Command::new("git")
                .args(["upload-pack", "--advertise-refs", repo_path.to_str().unwrap()])
                .output()
                .map_err(|e| GitError::Internal(format!("Failed to run git-upload-pack: {}", e)))?;

            let mut body = Vec::new();
            body.extend_from_slice(b"001e# service=git-upload-pack\n0000");
            body.extend_from_slice(&output.stdout);

            Ok(Response::builder()
                .status(StatusCode::OK)
                .header(header::CONTENT_TYPE, "application/x-git-upload-pack-advertisement")
                .body(Body::from(body))
                .unwrap())
        }
        "git-receive-pack" => {
            // For git push
            let output = std::process::Command::new("git")
                .args(["receive-pack", "--advertise-refs", repo_path.to_str().unwrap()])
                .output()
                .map_err(|e| GitError::Internal(format!("Failed to run git-receive-pack: {}", e)))?;

            let mut body = Vec::new();
            body.extend_from_slice(b"001f# service=git-receive-pack\n0000");
            body.extend_from_slice(&output.stdout);

            Ok(Response::builder()
                .status(StatusCode::OK)
                .header(header::CONTENT_TYPE, "application/x-git-receive-pack-advertisement")
                .body(Body::from(body))
                .unwrap())
        }
        _ => {
            // Dumb HTTP fallback
            let head = repository.head()
                .map_err(|e| GitError::Internal(format!("Failed to get HEAD: {}", e)))?;

            let refs = format!("ref: refs/heads/{}\n",
                head.shorthand().unwrap_or("main"));

            Ok(Response::builder()
                .status(StatusCode::OK)
                .header(header::CONTENT_TYPE, "text/plain")
                .body(Body::from(refs))
                .unwrap())
        }
    }
}

/// Handle POST /:repo/git-upload-pack
///
/// Serves git fetch/clone requests
#[axum::debug_handler]
async fn upload_pack(
    State(state): State<GitServerState>,
    Path(repo): Path<String>,
    body_bytes: Bytes,
) -> Result<Response, GitError> {
    debug!(repo = %repo, "upload_pack request");

    let repo_path = state.repo_path.join(&repo);

    // Execute git-upload-pack
    let mut output = std::process::Command::new("git")
        .args(["upload-pack", "--stateless-rpc", repo_path.to_str().unwrap()])
        .stdin(std::process::Stdio::piped())
        .stdout(std::process::Stdio::piped())
        .stderr(std::process::Stdio::piped())
        .spawn()
        .map_err(|e| GitError::Internal(format!("Failed to spawn git-upload-pack: {}", e)))?;

    // Write request body to stdin
    use std::io::Write;
    if let Some(ref mut stdin) = output.stdin {
        stdin.write_all(&body_bytes)
            .map_err(|e| GitError::Internal(format!("Failed to write to git-upload-pack: {}", e)))?;
    }

    let output = output.wait_with_output()
        .map_err(|e| GitError::Internal(format!("git-upload-pack failed: {}", e)))?;

    Ok(Response::builder()
        .status(StatusCode::OK)
        .header(header::CONTENT_TYPE, "application/x-git-upload-pack-result")
        .body(Body::from(output.stdout))
        .unwrap())
}

/// Handle POST /:repo/git-receive-pack
///
/// Handles git push with pre-receive validation hooks
#[axum::debug_handler]
async fn receive_pack(
    State(state): State<GitServerState>,
    Path(repo): Path<String>,
    body_bytes: Bytes,
) -> Result<Response, GitError> {
    info!(repo = %repo, "receive_pack request (git push)");

    let repo_path = state.repo_path.join(&repo);
    let repository = Repository::open(&repo_path)
        .map_err(|e| GitError::NotFound(format!("Repository not found: {}", e)))?;

    // Get old HEAD for validation
    let old_head = repository.head().ok()
        .and_then(|h| h.target())
        .map(|oid| oid.to_string());

    // Execute git-receive-pack
    let mut child = std::process::Command::new("git")
        .args(["receive-pack", "--stateless-rpc", repo_path.to_str().unwrap()])
        .stdin(std::process::Stdio::piped())
        .stdout(std::process::Stdio::piped())
        .stderr(std::process::Stdio::piped())
        .spawn()
        .map_err(|e| GitError::Internal(format!("Failed to spawn git-receive-pack: {}", e)))?;

    // Write request body to stdin
    if let Some(mut stdin) = child.stdin.take() {
        use std::io::Write;
        stdin.write_all(&body_bytes)
            .map_err(|e| GitError::Internal(format!("Failed to write to git-receive-pack: {}", e)))?;
    }

    let output = child.wait_with_output()
        .map_err(|e| GitError::Internal(format!("git-receive-pack failed: {}", e)))?;

    if !output.status.success() {
        error!(
            stderr = %String::from_utf8_lossy(&output.stderr),
            "git-receive-pack failed"
        );
        return Err(GitError::ValidationFailed(
            String::from_utf8_lossy(&output.stderr).to_string()
        ));
    }

    // Get new HEAD after push and collect tag names before any async operations
    let (new_head, tag_names) = {
        let repository = Repository::open(&repo_path)
            .map_err(|e| GitError::Internal(format!("Failed to reopen repo: {}", e)))?;

        let new_head = repository.head().ok()
            .and_then(|h| h.target())
            .map(|oid| oid.to_string());

        // Run validation on pushed commits
        if let Some(new_oid_str) = &new_head {
            info!(old_head = ?old_head, new_head = %new_oid_str, "Validating pushed commits");

            match state.validator.validate_push(&repository, old_head.as_deref(), new_oid_str) {
                Ok(()) => {
                    info!("Validation passed");
                }
                Err(errors) => {
                    error!(?errors, "Validation failed");
                    return Err(GitError::ValidationFailed(format!(
                        "Validation failed: {:?}",
                        errors
                    )));
                }
            }
        }

        // Collect tag names before repository goes out of scope
        let tag_names: Vec<String> = repository.tag_names(None)
            .map(|tags| {
                tags.iter().flatten()
                    .map(|s| s.to_string())
                    .collect()
            })
            .unwrap_or_default();

        (new_head, tag_names)
    };

    // Broadcast tag notifications (repository is now out of scope)
    if let Some(new_oid_str) = &new_head {
        for tag_name in tag_names {
            info!(tag = %tag_name, "Tag detected");

            // Broadcast tag notification via websocket
            let broadcaster = state.broadcaster.read().await;
            let message = format!(
                r#"{{"type":"tag","repository":"{}","tag":"{}","commit":"{}"}}"#,
                repo, tag_name, new_oid_str
            );
            broadcaster.broadcast(&message).await;
            info!(tag = %tag_name, "Broadcasted tag notification");
        }
    }

    Ok(Response::builder()
        .status(StatusCode::OK)
        .header(header::CONTENT_TYPE, "application/x-git-receive-pack-result")
        .body(Body::from(output.stdout))
        .unwrap())
}

/// Health check endpoint
async fn health_check() -> &'static str {
    "OK"
}

/// Git server errors
#[derive(Debug)]
pub enum GitError {
    NotFound(String),
    ValidationFailed(String),
    Internal(String),
}

impl IntoResponse for GitError {
    fn into_response(self) -> Response {
        let (status, message) = match self {
            GitError::NotFound(msg) => (StatusCode::NOT_FOUND, msg),
            GitError::ValidationFailed(msg) => (StatusCode::UNPROCESSABLE_ENTITY, msg),
            GitError::Internal(msg) => (StatusCode::INTERNAL_SERVER_ERROR, msg),
        };

        (status, message).into_response()
    }
}
