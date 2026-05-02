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
    Json, Router,
};
use git2::Repository;
use serde::{Deserialize, Serialize};
use std::path::{Path as StdPath, PathBuf};
use std::sync::Arc;
use std::time::Instant;
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
    pub start_time: Instant,
}

/// Create HTTP git server routes
pub fn create_git_routes(state: GitServerState) -> Router {
    Router::new()
        // Smart HTTP protocol endpoints
        .route("/{repo}/info/refs", get(info_refs))
        .route("/{repo}/git-upload-pack", post(upload_pack))
        .route("/{repo}/git-receive-pack", post(receive_pack))
        // Health check endpoints
        .route("/health", get(health_check))
        .route("/ready", get(readiness_check))
        .route("/websocket/health", get(websocket_health))
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
                .args([
                    "upload-pack",
                    "--advertise-refs",
                    repo_path.to_str().unwrap(),
                ])
                .output()
                .map_err(|e| GitError::Internal(format!("Failed to run git-upload-pack: {}", e)))?;

            let mut body = Vec::new();
            body.extend_from_slice(b"001e# service=git-upload-pack\n0000");
            body.extend_from_slice(&output.stdout);

            Ok(Response::builder()
                .status(StatusCode::OK)
                .header(
                    header::CONTENT_TYPE,
                    "application/x-git-upload-pack-advertisement",
                )
                .body(Body::from(body))
                .unwrap())
        }
        "git-receive-pack" => {
            // For git push
            let output = std::process::Command::new("git")
                .args([
                    "receive-pack",
                    "--advertise-refs",
                    repo_path.to_str().unwrap(),
                ])
                .output()
                .map_err(|e| {
                    GitError::Internal(format!("Failed to run git-receive-pack: {}", e))
                })?;

            let mut body = Vec::new();
            body.extend_from_slice(b"001f# service=git-receive-pack\n0000");
            body.extend_from_slice(&output.stdout);

            Ok(Response::builder()
                .status(StatusCode::OK)
                .header(
                    header::CONTENT_TYPE,
                    "application/x-git-receive-pack-advertisement",
                )
                .body(Body::from(body))
                .unwrap())
        }
        _ => {
            // Dumb HTTP fallback
            let head = repository
                .head()
                .map_err(|e| GitError::Internal(format!("Failed to get HEAD: {}", e)))?;

            let refs = format!("ref: refs/heads/{}\n", head.shorthand().unwrap_or("main"));

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
        .args([
            "upload-pack",
            "--stateless-rpc",
            repo_path.to_str().unwrap(),
        ])
        .stdin(std::process::Stdio::piped())
        .stdout(std::process::Stdio::piped())
        .stderr(std::process::Stdio::piped())
        .spawn()
        .map_err(|e| GitError::Internal(format!("Failed to spawn git-upload-pack: {}", e)))?;

    // Write request body to stdin
    use std::io::Write;
    if let Some(ref mut stdin) = output.stdin {
        stdin.write_all(&body_bytes).map_err(|e| {
            GitError::Internal(format!("Failed to write to git-upload-pack: {}", e))
        })?;
    }

    let output = output
        .wait_with_output()
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
    let old_head = repository
        .head()
        .ok()
        .and_then(|h| h.target())
        .map(|oid| oid.to_string());

    // Execute git-receive-pack
    let mut child = std::process::Command::new("git")
        .args([
            "receive-pack",
            "--stateless-rpc",
            repo_path.to_str().unwrap(),
        ])
        .stdin(std::process::Stdio::piped())
        .stdout(std::process::Stdio::piped())
        .stderr(std::process::Stdio::piped())
        .spawn()
        .map_err(|e| GitError::Internal(format!("Failed to spawn git-receive-pack: {}", e)))?;

    // Write request body to stdin
    if let Some(mut stdin) = child.stdin.take() {
        use std::io::Write;
        stdin.write_all(&body_bytes).map_err(|e| {
            GitError::Internal(format!("Failed to write to git-receive-pack: {}", e))
        })?;
    }

    let output = child
        .wait_with_output()
        .map_err(|e| GitError::Internal(format!("git-receive-pack failed: {}", e)))?;

    if !output.status.success() {
        error!(
            stderr = %String::from_utf8_lossy(&output.stderr),
            "git-receive-pack failed"
        );
        return Err(GitError::ValidationFailed(
            String::from_utf8_lossy(&output.stderr).to_string(),
        ));
    }

    // Get new HEAD after push and collect tag names before any async operations
    let (new_head, tag_names) = {
        let repository = Repository::open(&repo_path)
            .map_err(|e| GitError::Internal(format!("Failed to reopen repo: {}", e)))?;

        let new_head = repository
            .head()
            .ok()
            .and_then(|h| h.target())
            .map(|oid| oid.to_string());

        // Run validation on pushed commits
        if let Some(new_oid_str) = &new_head {
            info!(old_head = ?old_head, new_head = %new_oid_str, "Validating pushed commits");

            match state
                .validator
                .validate_push(&repository, old_head.as_deref(), new_oid_str)
            {
                Ok(()) => {
                    info!("Validation passed");
                }
                Err(errors) => {
                    error!(?errors, "Validation failed");
                    return Err(GitError::ValidationFailed(
                        format_validation_errors_for_git(&errors),
                    ));
                }
            }
        }

        // Collect tag names before repository goes out of scope
        let tag_names: Vec<String> = repository
            .tag_names(None)
            .map(|tags| tags.iter().flatten().map(|s| s.to_string()).collect())
            .unwrap_or_default();

        (new_head, tag_names)
    };

    // Broadcast tag notifications (repository is now out of scope)
    if let Some(new_oid_str) = &new_head {
        for tag_name in tag_names {
            info!(tag = %tag_name, "Tag detected");

            // Use proper event format
            use crate::git::events;
            let event = events::GitEvent::release_created(tag_name.clone(), new_oid_str.clone());

            // Broadcast using proper JSON format
            match event.to_json() {
                Ok(json) => {
                    let broadcaster = state.broadcaster.read().await;
                    broadcaster.broadcast(&json).await;
                    info!(tag = %tag_name, "Broadcasted tag notification");
                }
                Err(e) => {
                    error!(tag = %tag_name, error = %e, "Failed to serialize event");
                }
            }
        }
    }

    Ok(Response::builder()
        .status(StatusCode::OK)
        .header(
            header::CONTENT_TYPE,
            "application/x-git-receive-pack-result",
        )
        .body(Body::from(output.stdout))
        .unwrap())
}

/// Health check response
#[derive(Serialize)]
struct HealthResponse {
    status: String,
    version: String,
    timestamp: String,
}

/// Readiness check response
#[derive(Serialize)]
struct ReadinessResponse {
    status: String,
    version: String,
    timestamp: String,
    checks: ReadinessChecks,
}

/// Individual readiness checks
#[derive(Serialize)]
struct ReadinessChecks {
    repository: CheckStatus,
    uptime: CheckStatus,
    websocket: CheckStatus,
}

/// Status of an individual check
#[derive(Serialize)]
struct CheckStatus {
    status: String,
    message: String,
}

/// Health check endpoint - basic liveness
///
/// Returns 200 if service is running
async fn health_check() -> Json<HealthResponse> {
    Json(HealthResponse {
        status: "healthy".to_string(),
        version: env!("CARGO_PKG_VERSION").to_string(),
        timestamp: chrono::Utc::now().to_rfc3339(),
    })
}

/// Readiness check endpoint - detailed status
///
/// Returns 200 if service is ready to accept traffic, 503 otherwise
async fn readiness_check(
    State(state): State<GitServerState>,
) -> Result<Json<ReadinessResponse>, StatusCode> {
    let uptime = state.start_time.elapsed();

    // Check repository accessibility
    let repo_check = check_repository(&state.repo_path);

    // Check uptime (consider degraded if < 5 seconds)
    let uptime_check = if uptime.as_secs() < 5 {
        CheckStatus {
            status: "degraded".to_string(),
            message: "service warming up".to_string(),
        }
    } else {
        CheckStatus {
            status: "healthy".to_string(),
            message: "service operational".to_string(),
        }
    };

    // Check websocket broadcaster
    let ws_check = check_websocket(&state.broadcaster).await;

    // Determine overall status
    let overall_status = if repo_check.status == "unhealthy" || ws_check.status == "unhealthy" {
        "unhealthy"
    } else if uptime_check.status == "degraded" {
        "degraded"
    } else {
        "healthy"
    };

    let response = ReadinessResponse {
        status: overall_status.to_string(),
        version: env!("CARGO_PKG_VERSION").to_string(),
        timestamp: chrono::Utc::now().to_rfc3339(),
        checks: ReadinessChecks {
            repository: repo_check,
            uptime: uptime_check,
            websocket: ws_check,
        },
    };

    if overall_status == "unhealthy" {
        Err(StatusCode::SERVICE_UNAVAILABLE)
    } else {
        Ok(Json(response))
    }
}

/// Check if repository is accessible
fn check_repository(repo_path: &StdPath) -> CheckStatus {
    let catalog_path = repo_path.join("catalog.git");

    match Repository::open(&catalog_path) {
        Ok(_) => CheckStatus {
            status: "healthy".to_string(),
            message: "repository accessible".to_string(),
        },
        Err(e) => {
            error!(error = %e, "Repository check failed");
            CheckStatus {
                status: "unhealthy".to_string(),
                message: format!("repository unavailable: {}", e),
            }
        }
    }
}

/// Check if websocket broadcaster is operational
async fn check_websocket(broadcaster: &Arc<RwLock<Broadcaster>>) -> CheckStatus {
    // Simple check - can we acquire the lock?
    match tokio::time::timeout(std::time::Duration::from_secs(1), broadcaster.read()).await {
        Ok(_) => CheckStatus {
            status: "healthy".to_string(),
            message: "websocket operational".to_string(),
        },
        Err(_) => CheckStatus {
            status: "degraded".to_string(),
            message: "websocket lock timeout".to_string(),
        },
    }
}

/// Websocket health check response
#[derive(Serialize)]
struct WebsocketHealthResponse {
    status: String,
    active_connections: usize,
    timestamp: String,
}

/// Dedicated websocket health endpoint
async fn websocket_health(State(state): State<GitServerState>) -> Json<WebsocketHealthResponse> {
    let (status, active_connections) =
        match tokio::time::timeout(std::time::Duration::from_secs(1), state.broadcaster.read())
            .await
        {
            Ok(broadcaster) => {
                let count = broadcaster.connection_count().await;
                drop(broadcaster); // release read lock before returning
                ("healthy", count)
            }
            Err(_) => ("degraded", 0),
        };

    Json(WebsocketHealthResponse {
        status: status.to_string(),
        active_connections,
        timestamp: chrono::Utc::now().to_rfc3339(),
    })
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

/// Format validation errors in GitHub-style git output format.
///
/// Each line is prefixed with "remote: " so git clients display it cleanly.
fn format_validation_errors_for_git(
    errors: &crate::validation::errors::ValidationResult,
) -> String {
    let separator = "remote: ========================================";
    let mut lines = vec![
        separator.to_string(),
        "remote: GitStore Catalog Validation Failed".to_string(),
        separator.to_string(),
        "remote: ".to_string(),
    ];

    let mut sorted_files: Vec<&String> = errors.get_errors().keys().collect();
    sorted_files.sort();

    for file_path in sorted_files {
        let file_errors = &errors.get_errors()[file_path];
        if !file_path.is_empty() {
            lines.push(format!("remote: File: {}", file_path));
        }
        for err in file_errors {
            lines.push(format!("remote:   - {}", err));
        }
        lines.push("remote: ".to_string());
    }

    lines.push(format!(
        "remote: {} error(s) in {} file(s)",
        errors.error_count(),
        errors.get_errors().len()
    ));

    lines.join("\n")
}
