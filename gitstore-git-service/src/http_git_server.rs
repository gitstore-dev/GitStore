// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

// HTTP Git Server Implementation
//
// Implements git push/pull over HTTP with smart protocol support
// Hook extension points are reserved for future policy enforcement

use axum::{
    body::{Body, Bytes},
    extract::{DefaultBodyLimit, Path, Query, State},
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

use crate::git::pack_server::HttpPackServer;
use crate::git::repo::fanout_path;
use crate::websocket::broadcast::Broadcaster;

/// Git server state shared across handlers
#[derive(Clone)]
pub struct GitServerState {
    pub data_root: PathBuf,
    pub broadcaster: Arc<RwLock<Broadcaster>>,
    pub start_time: Instant,
    pub max_pack_size: u64,
}

/// Build the fanout path for `repo_id` inside `data_root` and verify the
/// directory already exists.  Returns NOT_FOUND if absent, INVALID_ARGUMENT if
/// `repo_id` is not a valid 36-character UUID.
fn confine_repo_path(data_root: &StdPath, repo_id: &str) -> Result<PathBuf, GitError> {
    let candidate = fanout_path(data_root, repo_id)
        .map_err(|_| GitError::NotFound(format!("repository '{}' not found", repo_id)))?;
    if !candidate.exists() {
        return Err(GitError::NotFound(format!(
            "repository '{}' not found",
            repo_id
        )));
    }
    Ok(candidate)
}

/// Create HTTP git server routes
pub fn create_git_routes(state: GitServerState) -> Router {
    let max_pack = state.max_pack_size as usize;
    Router::new()
        // Smart HTTP protocol endpoints
        .route("/{repo}/info/refs", get(info_refs))
        .route("/{repo}/git-upload-pack", post(upload_pack))
        .route(
            "/{repo}/git-receive-pack",
            post(receive_pack).layer(DefaultBodyLimit::max(max_pack)),
        )
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

    let service = query.service.as_deref().unwrap_or("");
    let repo_path = confine_repo_path(&state.data_root, &repo)?;

    // Validate the repo exists
    gix::open(&repo_path)
        .map_err(|e| GitError::NotFound(format!("Repository not found: {}", e)))?;

    match service {
        "git-upload-pack" => {
            let body = HttpPackServer::new(repo_path, state.max_pack_size)
                .advertise_upload_pack_refs()
                .map_err(|e| GitError::Internal(format!("upload-pack advertise failed: {e}")))?;

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
            let body = HttpPackServer::new(repo_path, state.max_pack_size)
                .advertise_receive_pack_refs()
                .map_err(|e| GitError::Internal(format!("receive-pack advertise failed: {e}")))?;

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

    let repo_path = confine_repo_path(&state.data_root, &repo)?;

    let response = HttpPackServer::new(repo_path, state.max_pack_size)
        .handle_upload_pack(&body_bytes)
        .map_err(|e| GitError::Internal(format!("upload-pack failed: {e}")))?;

    Ok(Response::builder()
        .status(StatusCode::OK)
        .header(header::CONTENT_TYPE, "application/x-git-upload-pack-result")
        .body(Body::from(response))
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

    let repo_path = confine_repo_path(&state.data_root, &repo)?;

    let pack_response = HttpPackServer::new(repo_path.clone(), state.max_pack_size)
        .handle_receive_pack(&body_bytes)
        .map_err(|e| GitError::ValidationFailed(e.to_string()))?;

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
        .body(Body::from(pack_response))
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
