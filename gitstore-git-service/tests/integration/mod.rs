// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

// Integration tests: full repository lifecycle without service restart (SC-008)

use gitstore::grpc::server::{proto, GitServiceImpl};
use proto::git_service_server::GitService;
use tempfile::TempDir;
use tonic::Request;

fn make_service() -> (GitServiceImpl, TempDir) {
    let dir = TempDir::new().expect("tempdir");
    let svc = GitServiceImpl::new(dir.path().to_path_buf());
    (svc, dir)
}

/// Full lifecycle: create → commit file → list files → list tags → delete.
/// Verifies SC-008: all steps succeed without restarting the service.
#[tokio::test]
async fn test_create_commit_list_delete() {
    let (svc, _dir) = make_service();

    // 1. Create repository
    svc.create_repository(Request::new(proto::CreateRepositoryRequest {
        repository_id: "integration-test".to_string(),
    }))
    .await
    .expect("create_repository");

    // 2. Commit an initial file
    svc.commit_file(Request::new(proto::CommitFileRequest {
        repository_id: "integration-test".to_string(),
        path: "README.md".to_string(),
        content: b"# Integration test\n".to_vec(),
        commit_message: "Initial commit".to_string(),
        author_name: "Test".to_string(),
        author_email: "test@example.com".to_string(),
    }))
    .await
    .expect("commit_file");

    // 3. List files — README.md must appear
    let list_resp = svc
        .list_files(Request::new(proto::ListFilesRequest {
            repository_id: "integration-test".to_string(),
            r#ref: String::new(),
            path_prefix: String::new(),
            recursive: true,
        }))
        .await
        .expect("list_files");
    let files = list_resp.into_inner().files;
    assert!(
        files.iter().any(|f| f.path == "README.md"),
        "README.md not found in listing: {:?}",
        files.iter().map(|f| &f.path).collect::<Vec<_>>()
    );

    // 4. List tags — should succeed (empty list is fine for a fresh repo)
    svc.list_tags(Request::new(proto::ListTagsRequest {
        repository_id: "integration-test".to_string(),
        prefix: String::new(),
    }))
    .await
    .expect("list_tags");

    // 5. Delete repository
    svc.delete_repository(Request::new(proto::DeleteRepositoryRequest {
        repository_id: "integration-test".to_string(),
    }))
    .await
    .expect("delete_repository");

    // 6. Subsequent operation on deleted repo returns NOT_FOUND
    let err = svc
        .get_file(Request::new(proto::GetFileRequest {
            repository_id: "integration-test".to_string(),
            path: "README.md".to_string(),
            r#ref: String::new(),
        }))
        .await
        .expect_err("expected NOT_FOUND after delete");
    assert_eq!(err.code(), tonic::Code::NotFound);
}

/// Two repositories created in the same service instance are isolated: a file
/// committed to repo-a does not appear in repo-b (FR-021, SC-010).
#[tokio::test]
async fn test_concurrent_repos_are_isolated() {
    let (svc, _dir) = make_service();

    for name in ["repo-a", "repo-b"] {
        svc.create_repository(Request::new(proto::CreateRepositoryRequest {
            repository_id: name.to_string(),
        }))
        .await
        .unwrap_or_else(|e| panic!("create {name}: {e}"));
    }

    // Write different files to each repo to verify isolation
    svc.commit_file(Request::new(proto::CommitFileRequest {
        repository_id: "repo-a".to_string(),
        path: "file-a.txt".to_string(),
        content: b"only in repo-a".to_vec(),
        commit_message: "add file-a".to_string(),
        author_name: "Test".to_string(),
        author_email: "test@example.com".to_string(),
    }))
    .await
    .expect("commit to repo-a");

    svc.commit_file(Request::new(proto::CommitFileRequest {
        repository_id: "repo-b".to_string(),
        path: "file-b.txt".to_string(),
        content: b"only in repo-b".to_vec(),
        commit_message: "add file-b".to_string(),
        author_name: "Test".to_string(),
        author_email: "test@example.com".to_string(),
    }))
    .await
    .expect("commit to repo-b");

    // repo-b must not contain file-a.txt
    let b_files = svc
        .list_files(Request::new(proto::ListFilesRequest {
            repository_id: "repo-b".to_string(),
            r#ref: String::new(),
            path_prefix: String::new(),
            recursive: true,
        }))
        .await
        .expect("list_files repo-b")
        .into_inner()
        .files;
    assert!(
        !b_files.iter().any(|f| f.path == "file-a.txt"),
        "file-a.txt leaked into repo-b: {:?}",
        b_files.iter().map(|f| &f.path).collect::<Vec<_>>()
    );

    // repo-a listing must contain the file
    let a_files = svc
        .list_files(Request::new(proto::ListFilesRequest {
            repository_id: "repo-a".to_string(),
            r#ref: String::new(),
            path_prefix: String::new(),
            recursive: true,
        }))
        .await
        .expect("list_files repo-a")
        .into_inner()
        .files;
    assert!(
        a_files.iter().any(|f| f.path == "file-a.txt"),
        "file-a.txt not in repo-a: {:?}",
        a_files.iter().map(|f| &f.path).collect::<Vec<_>>()
    );
    assert!(
        !a_files.iter().any(|f| f.path == "file-b.txt"),
        "file-b.txt leaked into repo-a: {:?}",
        a_files.iter().map(|f| &f.path).collect::<Vec<_>>()
    );
}

/// Delete on an unknown repository returns NOT_FOUND (FR-017).
#[tokio::test]
async fn test_delete_unknown_repo_returns_not_found() {
    let (svc, _dir) = make_service();

    let err = svc
        .delete_repository(Request::new(proto::DeleteRepositoryRequest {
            repository_id: "does-not-exist".to_string(),
        }))
        .await
        .expect_err("expected NOT_FOUND");
    assert_eq!(err.code(), tonic::Code::NotFound);
}

/// Invalid repository names are rejected with INVALID_ARGUMENT on create (FR-020).
#[tokio::test]
async fn test_invalid_repo_name_rejected_on_create() {
    let (svc, _dir) = make_service();

    for bad_name in ["", "../etc", "a/b", "a\\b"] {
        let err = svc
            .create_repository(Request::new(proto::CreateRepositoryRequest {
                repository_id: bad_name.to_string(),
            }))
            .await
            .expect_err(&format!("expected INVALID_ARGUMENT for {bad_name:?}"));
        assert_eq!(
            err.code(),
            tonic::Code::InvalidArgument,
            "wrong code for {bad_name:?}"
        );
    }
}
