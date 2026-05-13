// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

// HTTP Git Server Implementation
//
// Implements git push/pull over HTTP with smart protocol support
// Hook extension points are reserved for future policy enforcement

use axum::{
    body::{Body, Bytes},
    extract::{Path, Query, State},
    http::{header, StatusCode},
    response::{IntoResponse, Response},
    routing::{get, post},
    Json, Router,
};
use serde::{Deserialize, Serialize};
use std::path::{Path as StdPath, PathBuf};
use std::sync::Arc;
use std::time::Instant;
use tokio::sync::RwLock;
use tracing::{debug, error, info};

use crate::websocket::broadcast::Broadcaster;

/// Git server state shared across handlers
#[derive(Clone)]
pub struct GitServerState {
    pub data_root: PathBuf,
    pub broadcaster: Arc<RwLock<Broadcaster>>,
    pub start_time: Instant,
}

/// Validate a repo name segment from the URL path.
/// Rejects empty, path separators, and ".." components (mirrors FR-020).
fn validate_repo_name(name: &str) -> Result<(), StatusCode> {
    if name.is_empty() || name.contains('/') || name.contains('\\') || name.contains("..") {
        return Err(StatusCode::BAD_REQUEST);
    }
    Ok(())
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
async fn info_refs(
    State(state): State<GitServerState>,
    Path(repo): Path<String>,
    Query(query): Query<InfoRefsQuery>,
) -> Result<Response, GitError> {
    debug!(repo = %repo, "info_refs request");

    validate_repo_name(&repo).map_err(|_| GitError::NotFound("invalid repository name".into()))?;

    let service = query.service.as_deref().unwrap_or("");
    let repo_path = state.data_root.join(format!("{}.git", repo));

    // Validate the repo exists
    gix::open(&repo_path)
        .map_err(|e| GitError::NotFound(format!("Repository not found: {}", e)))?;

    match service {
        "git-upload-pack" => {
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
            // Dumb HTTP fallback: return the current HEAD branch name
            let repo = gix::open(&repo_path)
                .map_err(|e| GitError::Internal(format!("Failed to open repo: {}", e)))?;

            let head_name = repo
                .head_name()
                .map_err(|e| GitError::Internal(format!("Failed to get HEAD: {}", e)))?
                .map(|n| n.shorten().to_string())
                .unwrap_or_else(|| "main".to_string());

            let refs = format!("ref: refs/heads/{}\n", head_name);

            Ok(Response::builder()
                .status(StatusCode::OK)
                .header(header::CONTENT_TYPE, "text/plain")
                .body(Body::from(refs))
                .unwrap())
        }
    }
}

/// Handle POST /:repo/git-upload-pack
#[axum::debug_handler]
async fn upload_pack(
    State(state): State<GitServerState>,
    Path(repo): Path<String>,
    body_bytes: Bytes,
) -> Result<Response, GitError> {
    debug!(repo = %repo, "upload_pack request");

    validate_repo_name(&repo).map_err(|_| GitError::NotFound("invalid repository name".into()))?;
    let repo_path = state.data_root.join(format!("{}.git", repo));

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
#[axum::debug_handler]
async fn receive_pack(
    State(state): State<GitServerState>,
    Path(repo): Path<String>,
    body_bytes: Bytes,
) -> Result<Response, GitError> {
    info!(repo = %repo, "receive_pack request (git push)");

    validate_repo_name(&repo).map_err(|_| GitError::NotFound("invalid repository name".into()))?;
    let repo_path = state.data_root.join(format!("{}.git", repo));

    // Validate repository exists before spawning git-receive-pack
    gix::open(&repo_path)
        .map_err(|e| GitError::NotFound(format!("Repository not found: {}", e)))?;

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

    // Get new HEAD and tag names after push for broadcast
    let (new_head, tag_names) = {
        let repository = gix::open(&repo_path)
            .map_err(|e| GitError::Internal(format!("Failed to reopen repo: {}", e)))?;

        let new_head = repository.head_id().ok().map(|id| id.to_string());

        let tag_names: Vec<String> = repository
            .references()
            .ok()
            .map(|p| {
                p.tags()
                    .map(|tags| {
                        tags.filter_map(|r| r.ok())
                            .map(|r| r.name().shorten().to_string())
                            .collect::<Vec<_>>()
                    })
                    .unwrap_or_default()
            })
            .unwrap_or_default();

        (new_head, tag_names)
    };

    if let Some(new_oid_str) = &new_head {
        for tag_name in tag_names {
            info!(tag = %tag_name, "Tag detected");

            use crate::git::events;
            let event = events::GitEvent::release_created(tag_name.clone(), new_oid_str.clone());

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
async fn health_check() -> Json<HealthResponse> {
    Json(HealthResponse {
        status: "healthy".to_string(),
        version: env!("CARGO_PKG_VERSION").to_string(),
        timestamp: chrono::Utc::now().to_rfc3339(),
    })
}

/// Readiness check endpoint - detailed status
async fn readiness_check(
    State(state): State<GitServerState>,
) -> Result<Json<ReadinessResponse>, StatusCode> {
    let uptime = state.start_time.elapsed();

    let repo_check = check_repository(&state.data_root);

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

    let ws_check = check_websocket(&state.broadcaster).await;

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

/// Check if the data root directory is accessible.
fn check_repository(data_root: &StdPath) -> CheckStatus {
    if data_root.exists() {
        CheckStatus {
            status: "healthy".to_string(),
            message: "data directory accessible".to_string(),
        }
    } else {
        CheckStatus {
            status: "unhealthy".to_string(),
            message: "data directory not found".to_string(),
        }
    }
}

/// Check if websocket broadcaster is operational
async fn check_websocket(broadcaster: &Arc<RwLock<Broadcaster>>) -> CheckStatus {
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
                drop(broadcaster);
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
