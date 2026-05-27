// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

// HTTP smart-protocol integration tests.
//
// Each test spins up a real Axum HTTP server bound to an ephemeral port and
// drives clone/fetch/push via the local `git` binary (available on the CI host
// — it is the *server image* that must not have it, not the test host).
//
// Tests are organised by user story:
//   US1: upload-pack (clone / fetch)  — T006
//   US2: receive-pack (push)          — T011

use std::net::SocketAddr;
use std::path::PathBuf;
use std::sync::Arc;
use std::time::Instant;

use tempfile::TempDir;
use tokio::sync::RwLock;

use gitstore::http_git_server::{create_git_routes, GitServerState};
use gitstore::websocket::server::WebsocketServer;

// HTTP tests use UUID repo IDs as URL path segments
const HTTP_CATALOG_ID: &str = "01960002-0000-7000-8000-000000000001";
const HTTP_EMPTY_ID: &str = "01960002-0000-7000-8000-000000000002";

// ---------------------------------------------------------------------------
// Test harness helpers
// ---------------------------------------------------------------------------

/// Spawn an Axum HTTP server on an OS-assigned ephemeral port.
/// Returns the bound address; the server runs until the test ends.
async fn start_test_server_with_pack_size(data_dir: PathBuf, max_pack_size: u64) -> SocketAddr {
    let ws_server = WebsocketServer::new("127.0.0.1:0".parse().unwrap());
    let broadcaster = ws_server.broadcaster();
    let state = GitServerState {
        data_root: data_dir,
        broadcaster: Arc::new(RwLock::new(broadcaster)),
        start_time: Instant::now(),
        max_pack_size,
    };

    let app = create_git_routes(state);
    let listener = tokio::net::TcpListener::bind("127.0.0.1:0")
        .await
        .expect("bind ephemeral port");
    let addr = listener.local_addr().expect("local addr");

    tokio::spawn(async move {
        axum::serve(listener, app).await.unwrap();
    });

    addr
}

async fn start_test_server(data_dir: PathBuf) -> SocketAddr {
    start_test_server_with_pack_size(data_dir, 52_428_800).await
}

/// Initialise a bare repo and seed it with one commit via the gRPC service.
async fn init_bare_repo_with_commit(data_dir: &std::path::Path, repo_id: &str) {
    use gitstore::grpc::server::proto::{self, git_service_server::GitService};
    use gitstore::grpc::server::GitServiceImpl;
    use tonic::Request;

    let svc = GitServiceImpl::new(data_dir.to_path_buf());

    svc.create_repository(Request::new(proto::CreateRepositoryRequest {
        repository_id: repo_id.to_string(),
        storage_class: String::new(),
    }))
    .await
    .expect("create_repository");

    svc.commit_file(Request::new(proto::CommitFileRequest {
        repository_id: repo_id.to_string(),
        path: "README.md".to_string(),
        content: b"# test\n".to_vec(),
        commit_message: "Initial commit".to_string(),
        author_name: "Test".to_string(),
        author_email: "test@example.com".to_string(),
    }))
    .await
    .expect("commit_file");
}

async fn init_bare_repo_empty(data_dir: &std::path::Path, repo_id: &str) {
    use gitstore::grpc::server::proto::{self, git_service_server::GitService};
    use gitstore::grpc::server::GitServiceImpl;
    use tonic::Request;

    let svc = GitServiceImpl::new(data_dir.to_path_buf());
    svc.create_repository(Request::new(proto::CreateRepositoryRequest {
        repository_id: repo_id.to_string(),
        storage_class: String::new(),
    }))
    .await
    .expect("create_repository");
}

/// Run a `git` subprocess and return (exit_success, stdout, stderr).
async fn run_git(args: &[&str], cwd: &std::path::Path) -> (bool, String, String) {
    let output = tokio::process::Command::new("git")
        .args(args)
        .current_dir(cwd)
        .env("GIT_TERMINAL_PROMPT", "0")
        .output()
        .await
        .expect("spawn git");
    (
        output.status.success(),
        String::from_utf8_lossy(&output.stdout).to_string(),
        String::from_utf8_lossy(&output.stderr).to_string(),
    )
}

// ---------------------------------------------------------------------------
// US1: upload-pack (clone / fetch) — T006
// ---------------------------------------------------------------------------

/// SC-001, SC-002: clone must succeed against the in-process server.
/// FAILS until T007+T009 are implemented.
#[tokio::test]
async fn clone_succeeds_without_git_binary() {
    let data_dir = TempDir::new().expect("data dir");
    init_bare_repo_with_commit(data_dir.path(), HTTP_CATALOG_ID).await;

    let addr = start_test_server(data_dir.path().to_path_buf()).await;
    let clone_dir = TempDir::new().expect("clone dir");
    let url = format!("http://{}/{}", addr, HTTP_CATALOG_ID);

    let (ok, _out, err) = run_git(&["clone", &url, "catalog-work"], clone_dir.path()).await;

    assert!(ok, "git clone failed: {err}");

    let work_dir = clone_dir.path().join("catalog-work");
    assert!(
        work_dir.join("README.md").exists(),
        "README.md missing after clone"
    );
}

/// SC-002: fetch must succeed after an initial clone.
/// FAILS until T007+T009 are implemented.
#[tokio::test]
async fn fetch_succeeds_without_git_binary() {
    let data_dir = TempDir::new().expect("data dir");
    init_bare_repo_with_commit(data_dir.path(), HTTP_CATALOG_ID).await;

    let addr = start_test_server(data_dir.path().to_path_buf()).await;
    let clone_dir = TempDir::new().expect("clone dir");
    let url = format!("http://{}/{}", addr, HTTP_CATALOG_ID);

    let (ok, _, err) = run_git(&["clone", &url, "catalog-work"], clone_dir.path()).await;
    assert!(ok, "initial clone failed: {err}");

    let work_dir = clone_dir.path().join("catalog-work");
    let (ok, _, err) = run_git(&["fetch", "origin"], &work_dir).await;
    assert!(ok, "git fetch failed: {err}");
}

/// Edge case: clone of an empty repository must not panic the server.
/// FAILS until T007+T009 are implemented.
#[tokio::test]
async fn clone_empty_repo_succeeds() {
    let data_dir = TempDir::new().expect("data dir");
    init_bare_repo_empty(data_dir.path(), HTTP_EMPTY_ID).await;

    let addr = start_test_server(data_dir.path().to_path_buf()).await;
    let clone_dir = TempDir::new().expect("clone dir");
    let url = format!("http://{}/{}", addr, HTTP_EMPTY_ID);

    // git clone of an empty repo may exit non-zero with a warning; either is
    // acceptable — the key assertion is that the server does not return 500.
    let client = reqwest::Client::new();
    let info_url = format!(
        "http://{}/{}/info/refs?service=git-upload-pack",
        addr, HTTP_EMPTY_ID
    );
    let resp = client.get(&info_url).send().await.expect("HTTP request");
    assert_ne!(
        resp.status().as_u16(),
        500,
        "server must not panic on empty-repo advertisement"
    );

    let _ = run_git(&["clone", &url, "empty-work"], clone_dir.path()).await;
}

/// Edge case: upload-pack on a non-existent repository must return 404.
/// FAILS until T007+T009 are implemented.
#[tokio::test]
async fn upload_pack_on_nonexistent_repo_404() {
    let data_dir = TempDir::new().expect("data dir");
    let addr = start_test_server(data_dir.path().to_path_buf()).await;

    let client = reqwest::Client::new();
    // Use a valid UUID that doesn't exist
    let url = format!(
        "http://{}/01960099-0000-7000-8000-000000000099/info/refs?service=git-upload-pack",
        addr
    );
    let resp = client.get(&url).send().await.expect("HTTP request");
    assert_eq!(resp.status().as_u16(), 404, "expected 404 for missing repo");
}

// ---------------------------------------------------------------------------
// US2: receive-pack (push) — T011
// ---------------------------------------------------------------------------

/// SC-002: push must succeed against the in-process server.
/// FAILS until T012+T015+T016 are implemented.
#[tokio::test]
async fn push_succeeds_without_git_binary() {
    let data_dir = TempDir::new().expect("data dir");
    init_bare_repo_with_commit(data_dir.path(), HTTP_CATALOG_ID).await;

    let addr = start_test_server(data_dir.path().to_path_buf()).await;
    let clone_dir = TempDir::new().expect("clone dir");
    let url = format!("http://{}/{}", addr, HTTP_CATALOG_ID);

    let (ok, _, err) = run_git(&["clone", &url, "catalog-work"], clone_dir.path()).await;
    assert!(ok, "clone failed: {err}");

    let work_dir = clone_dir.path().join("catalog-work");
    run_git(&["config", "user.email", "test@example.com"], &work_dir).await;
    run_git(&["config", "user.name", "Test"], &work_dir).await;

    std::fs::write(work_dir.join("newfile.txt"), b"hello").expect("write");
    run_git(&["add", "newfile.txt"], &work_dir).await;
    let (ok, _, err) = run_git(&["commit", "-m", "add newfile"], &work_dir).await;
    assert!(ok, "commit failed: {err}");

    let (ok, _, err) = run_git(&["push", "origin", "main"], &work_dir).await;
    assert!(ok, "git push failed: {err}");
}

/// FR-005, SC-005: rejected push must produce a human-readable error message.
/// FAILS until T012+T015+T016 are implemented.
#[tokio::test]
async fn push_rejection_is_human_readable() {
    let data_dir = TempDir::new().expect("data dir");
    init_bare_repo_with_commit(data_dir.path(), HTTP_CATALOG_ID).await;

    let addr = start_test_server(data_dir.path().to_path_buf()).await;
    let url = format!("http://{}/{}", addr, HTTP_CATALOG_ID);

    let clone_a = TempDir::new().expect("clone a");
    let clone_b = TempDir::new().expect("clone b");

    let (ok, _, err) = run_git(&["clone", &url, "work"], clone_a.path()).await;
    assert!(ok, "clone a failed: {err}");
    let (ok, _, err) = run_git(&["clone", &url, "work"], clone_b.path()).await;
    assert!(ok, "clone b failed: {err}");

    let work_a = clone_a.path().join("work");
    let work_b = clone_b.path().join("work");

    for work in [&work_a, &work_b] {
        run_git(&["config", "user.email", "test@example.com"], work).await;
        run_git(&["config", "user.name", "Test"], work).await;
    }

    // Push from A — advances remote
    std::fs::write(work_a.join("a.txt"), b"from a").expect("write a");
    run_git(&["add", "a.txt"], &work_a).await;
    run_git(&["commit", "-m", "from a"], &work_a).await;
    let (ok, _, err) = run_git(&["push", "origin", "main"], &work_a).await;
    assert!(ok, "push a failed: {err}");

    // Push from B (diverged — non-fast-forward, must be rejected)
    std::fs::write(work_b.join("b.txt"), b"from b").expect("write b");
    run_git(&["add", "b.txt"], &work_b).await;
    run_git(&["commit", "-m", "from b"], &work_b).await;
    let (rejected, _, stderr) = run_git(&["push", "origin", "main"], &work_b).await;

    assert!(!rejected, "expected push to be rejected (non-fast-forward)");
    assert!(!stderr.is_empty(), "rejection must produce stderr output");
    assert!(
        stderr.is_ascii() || stderr.contains("rejected") || stderr.contains("failed"),
        "rejection message must be readable text, got: {stderr:?}"
    );
}

/// FR-013, SC-008: push exceeding max_pack_size must be rejected with HTTP 413.
/// FAILS until T014 is implemented.
#[tokio::test]
async fn push_over_size_limit_rejected_413() {
    let data_dir = TempDir::new().expect("data dir");
    // max_pack_size = 1 byte so any pack body triggers the limit
    let addr = start_test_server_with_pack_size(data_dir.path().to_path_buf(), 1).await;

    let client = reqwest::Client::new();
    let url = format!("http://{}/{}/git-receive-pack", addr, HTTP_CATALOG_ID);
    let resp = client
        .post(&url)
        .header("Content-Type", "application/x-git-receive-pack-request")
        .body(vec![0u8; 1024]) // 1 KB > 1 byte limit
        .send()
        .await
        .expect("HTTP request");

    assert_eq!(
        resp.status().as_u16(),
        413,
        "expected 413 Content Too Large, got {}",
        resp.status()
    );
}
