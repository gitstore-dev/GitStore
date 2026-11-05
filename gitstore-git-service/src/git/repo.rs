// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

// Git repository management

use anyhow::{Context, Result};
use gix::refs::transaction::PreviousValue;
use std::path::{Path, PathBuf};
use tonic::Status;
use tracing::{debug, info};

// Share gix object-index state across short RPCs without retaining an
// unbounded repository map. References remain disk-backed; authorization and
// lifecycle locks stay at each caller. Config replacement invalidates a handle.
const OPEN_REPOSITORY_CACHE_CAPACITY: usize = 8;

type CachedRepository = (PathBuf, std::fs::Metadata, gix::ThreadSafeRepository);
static OPEN_REPOSITORIES: std::sync::OnceLock<
    std::sync::Mutex<std::collections::VecDeque<CachedRepository>>,
> = std::sync::OnceLock::new();

pub fn open_repository(path: &Path) -> Result<gix::Repository> {
    use std::os::unix::fs::MetadataExt;
    let metadata =
        std::fs::metadata(path.join("config")).context("read repository configuration identity")?;
    let cache = OPEN_REPOSITORIES.get_or_init(Default::default);
    {
        let mut entries = cache.lock().unwrap_or_else(|e| e.into_inner());
        if let Some(position) = entries.iter().position(|(p, old, repository)| {
            p == path
                && old.ino() == metadata.ino()
                && old.dev() == metadata.dev()
                && old.mtime() == metadata.mtime()
                && old.mtime_nsec() == metadata.mtime_nsec()
                && old.len() == metadata.len()
                && repository.objects.metrics().unused_slots >= 8
        }) {
            let entry = entries.remove(position).expect("matched cached repository");
            let repository = entry.2.to_thread_local();
            entries.push_back(entry);
            return Ok(repository);
        }
    }
    let repository = open_repository_uncached(path)?.into_sync();
    let result = repository.to_thread_local();
    let mut entries = cache.lock().unwrap_or_else(|e| e.into_inner());
    entries.retain(|(p, _, _)| p != path);
    while entries.len() >= OPEN_REPOSITORY_CACHE_CAPACITY {
        entries.pop_front();
    }
    entries.push_back((path.to_path_buf(), metadata, repository));
    Ok(result)
}

// Server repositories can accumulate many incremental packs. Reserve gix's
// supported index-slot ceiling without an extra discovery scan at open time.
// Quarantine readers use an independent store so rejected objects cannot remain
// mapped in the live cache after their temporary links are removed.
pub fn open_repository_uncached(path: &Path) -> Result<gix::Repository> {
    Ok(gix::open_opts(
        path,
        gix::open::Options::default()
            .object_store_slots(gix::odb::store::init::Slots::Given(32767)),
    )?)
}

pub fn forget_repository(path: &Path) {
    if let Some(cache) = OPEN_REPOSITORIES.get() {
        cache
            .lock()
            .unwrap_or_else(|e| e.into_inner())
            .retain(|(p, _, _)| p != path);
    }
}

/// Compute the two-level fanout path for a repository given its UUIDv7 `repo_id`.
///
/// Formula: `{data_root}/{hex[0..2]}/{hex[2..4]}/{repo_id}.git`
/// where `hex` is the repo_id with all hyphens stripped.
///
/// Returns `Status::invalid_argument` for any repo_id that is not exactly
/// a 36-character hyphenated UUID (8-4-4-4-12 format).
pub fn fanout_path(data_root: &Path, repo_id: &str) -> Result<PathBuf, Status> {
    // A canonical UUID is exactly 36 characters: 8-4-4-4-12 with hyphens
    if repo_id.len() != 36 {
        return Err(Status::invalid_argument(format!(
            "repo_id must be a 36-character UUID, got length {}",
            repo_id.len()
        )));
    }
    if repo_id.contains('/') || repo_id.contains('\\') {
        return Err(Status::invalid_argument(
            "repo_id must not contain path separators",
        ));
    }
    if repo_id.contains("..") {
        return Err(Status::invalid_argument(
            "repo_id must not contain '..' components",
        ));
    }
    // Strip hyphens and validate we have exactly 32 hex characters
    let hex: String = repo_id.chars().filter(|&c| c != '-').collect();
    if hex.len() != 32 || !hex.chars().all(|c| c.is_ascii_hexdigit()) {
        return Err(Status::invalid_argument(format!(
            "repo_id '{}' is not a valid UUID",
            repo_id
        )));
    }
    let l1 = &hex[0..2];
    let l2 = &hex[2..4];
    Ok(data_root.join(l1).join(l2).join(format!("{}.git", repo_id)))
}

/// Initialize a new bare repository at `path`.
/// Sets HEAD to refs/heads/main via a post-init edit_reference call.
///
/// gix::init_bare does not accept an initial_head option (unlike git2's
/// RepositoryInitOptions::initial_head). We force HEAD → refs/heads/main
/// with a symbolic ref edit after init. Remove this shim once gix provides
/// an init_opts equivalent. Tracked: specs/007-migrate-gitoxide/research.md §6.
pub fn create_repository(path: &Path) -> Result<()> {
    forget_repository(path);
    let repo = gix::init_bare(path)
        .with_context(|| format!("Failed to init bare repository at {}", path.display()))?;
    force_head_to_main(&repo)?;
    Ok(())
}

/// Remove a repository and all its data.
pub fn delete_repository(path: &Path) -> Result<()> {
    forget_repository(path);
    std::fs::remove_dir_all(path)
        .with_context(|| format!("Failed to remove repository at {}", path.display()))
}

/// Force HEAD to point at refs/heads/main (symbolic ref).
fn force_head_to_main(repo: &gix::Repository) -> Result<()> {
    use gix::refs::transaction::{Change, LogChange, RefEdit};
    use gix::refs::Target;
    repo.edit_reference(RefEdit {
        change: Change::Update {
            log: LogChange {
                mode: gix::refs::transaction::RefLog::AndReference,
                force_create_reflog: false,
                message: "set HEAD to refs/heads/main".into(),
            },
            expected: PreviousValue::Any,
            new: Target::Symbolic("refs/heads/main".try_into()?),
        },
        name: "HEAD".try_into()?,
        deref: false,
    })
    .context("Failed to set HEAD to refs/heads/main")?;
    Ok(())
}

/// Initialize or open a bare git repository.
pub fn init_or_open_repository(path: &Path) -> Result<gix::Repository> {
    if path.exists() {
        debug!(path = %path.display(), "Opening existing repository");
        open_repository(path)
            .with_context(|| format!("Failed to open repository at {}", path.display()))
    } else {
        info!(path = %path.display(), "Initializing new bare repository");
        let repo = gix::init_bare(path)
            .with_context(|| format!("Failed to initialize repository at {}", path.display()))?;
        force_head_to_main(&repo)?;
        Ok(repo)
    }
}

/// Get the current HEAD commit.
pub fn get_head_commit(repo: &gix::Repository) -> Result<gix::Commit<'_>> {
    repo.head_commit().context("Failed to get HEAD commit")
}

/// List all tags in the repository.
pub fn list_tags(repo: &gix::Repository) -> Result<Vec<String>> {
    let platform = repo.references().context("Failed to access references")?;

    let tags = platform
        .tags()
        .context("Failed to iterate tags")?
        .filter_map(|r| r.ok())
        .map(|r| r.name().shorten().to_string())
        .collect();

    Ok(tags)
}

/// Check if a reference is an annotated tag starting with 'v' (release tag).
pub fn is_release_tag(repo: &gix::Repository, tag_name: &str) -> Result<bool> {
    if !tag_name.starts_with('v') {
        return Ok(false);
    }

    let mut reference = repo
        .find_reference(&format!("refs/tags/{}", tag_name))
        .context("Failed to find tag reference")?;

    // peel_to_tag takes &mut self in gix
    let is_annotated = reference.peel_to_tag().is_ok();
    Ok(is_annotated)
}

/// Get commit SHA for a tag.
pub fn get_tag_commit(repo: &gix::Repository, tag_name: &str) -> Result<String> {
    let mut reference = repo
        .find_reference(&format!("refs/tags/{}", tag_name))
        .context("Failed to find tag reference")?;

    let commit = reference
        .peel_to_commit()
        .context("Failed to peel tag to commit")?;

    Ok(commit.id().to_string())
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::path::PathBuf;
    use tempfile::TempDir;

    #[test]
    fn cached_repository_observes_external_ref_and_object_writes() {
        let directory = TempDir::new().unwrap();
        create_repository(directory.path()).unwrap();
        let first = open_repository(directory.path()).unwrap();
        let external = gix::open(directory.path()).unwrap();
        let blob = external
            .write_blob(b"new external object")
            .unwrap()
            .detach();
        external
            .edit_reference(gix::refs::transaction::RefEdit {
                change: gix::refs::transaction::Change::Update {
                    log: gix::refs::transaction::LogChange::default(),
                    expected: PreviousValue::Any,
                    new: gix::refs::Target::Object(blob),
                },
                name: "refs/tags/external".try_into().unwrap(),
                deref: false,
            })
            .unwrap();
        let second = open_repository(directory.path()).unwrap();
        assert_eq!(
            second
                .find_reference("refs/tags/external")
                .unwrap()
                .target()
                .try_id(),
            Some(blob.as_ref())
        );
        assert_eq!(
            second.find_object(blob).unwrap().data,
            b"new external object"
        );
        assert_eq!(
            first.find_object(blob).unwrap().data,
            b"new external object"
        );
    }

    #[test]
    fn cached_repository_configuration_and_deletion_are_invalidated() {
        use std::io::Write;
        let directory = TempDir::new().unwrap();
        let path = directory.path().join("repo.git");
        create_repository(&path).unwrap();
        let before = open_repository(&path).unwrap();
        std::fs::OpenOptions::new()
            .append(true)
            .open(path.join("config"))
            .unwrap()
            .write_all(b"\n[gitstore]\ncacheTest = refreshed\n")
            .unwrap();
        let after = open_repository(&path).unwrap();
        assert!(!std::ptr::eq(
            before.objects.store_ref(),
            after.objects.store_ref()
        ));
        delete_repository(&path).unwrap();
        assert!(open_repository(&path).is_err());
        create_repository(&path).unwrap();
        let recreated = open_repository(&path).unwrap();
        assert!(!std::ptr::eq(
            after.objects.store_ref(),
            recreated.objects.store_ref()
        ));
    }

    #[test]
    fn cached_repository_count_stays_bounded() {
        let directory = TempDir::new().unwrap();
        for index in 0..OPEN_REPOSITORY_CACHE_CAPACITY + 3 {
            let path = directory.path().join(index.to_string());
            create_repository(&path).unwrap();
            open_repository(&path).unwrap();
        }
        assert!(
            OPEN_REPOSITORIES.get().unwrap().lock().unwrap().len()
                <= OPEN_REPOSITORY_CACHE_CAPACITY
        );
    }

    // ── fanout_path ───────────────────────────────────────────────────────────

    #[test]
    fn fanout_path_stability_same_uuid_same_path() {
        let root = PathBuf::from("/data");
        let id = "01960000-0000-7000-8000-000000000010";
        let p1 = fanout_path(&root, id).unwrap();
        let p2 = fanout_path(&root, id).unwrap();
        assert_eq!(p1, p2);
    }

    #[test]
    fn fanout_path_collision_free_distinct_uuids() {
        let root = PathBuf::from("/data");
        let id_a = "01960000-0000-7000-8000-000000000010";
        let id_b = "01960000-0000-7000-8000-000000000011";
        let pa = fanout_path(&root, id_a).unwrap();
        let pb = fanout_path(&root, id_b).unwrap();
        assert_ne!(pa, pb);
    }

    #[test]
    fn fanout_path_formula_prefix_extraction() {
        let root = PathBuf::from("/data");
        // Strip hyphens: "01960000000070008000000000000010" → l1="01" l2="96"
        let id = "01960000-0000-7000-8000-000000000010";
        let path = fanout_path(&root, id).unwrap();
        assert_eq!(
            path,
            PathBuf::from("/data/01/96/01960000-0000-7000-8000-000000000010.git")
        );
    }

    #[test]
    fn fanout_path_rejects_empty_string() {
        let root = PathBuf::from("/data");
        let err = fanout_path(&root, "").unwrap_err();
        assert_eq!(err.code(), tonic::Code::InvalidArgument);
    }

    #[test]
    fn fanout_path_rejects_wrong_length() {
        let root = PathBuf::from("/data");
        // 35 chars — one short
        let err = fanout_path(&root, "01960000-0000-7000-8000-00000000001").unwrap_err();
        assert_eq!(err.code(), tonic::Code::InvalidArgument);
    }

    #[test]
    fn fanout_path_rejects_slash_in_id() {
        let root = PathBuf::from("/data");
        // 36 chars but contains '/'
        let err = fanout_path(&root, "01960000-0000-7000-8000-0000000/0010").unwrap_err();
        assert_eq!(err.code(), tonic::Code::InvalidArgument);
    }

    #[test]
    fn fanout_path_rejects_dotdot_in_id() {
        let root = PathBuf::from("/data");
        // 36 chars but contains ".."
        let err = fanout_path(&root, "01960000-0000-7000-8000-000000..0010").unwrap_err();
        assert_eq!(err.code(), tonic::Code::InvalidArgument);
    }

    #[test]
    fn fanout_path_rejects_non_hex_uuid() {
        let root = PathBuf::from("/data");
        // 36 chars, right hyphen positions, but non-hex characters
        let err = fanout_path(&root, "ZZZZZZZZ-ZZZZ-ZZZZ-ZZZZ-ZZZZZZZZZZZZ").unwrap_err();
        assert_eq!(err.code(), tonic::Code::InvalidArgument);
    }

    // ── create/open/list ──────────────────────────────────────────────────────

    #[test]
    fn test_init_repository() {
        let temp_dir = TempDir::new().unwrap();
        let repo_path = temp_dir.path().join("test.git");

        create_repository(&repo_path).unwrap();
        assert!(repo_path.exists());
    }

    #[test]
    fn test_open_existing_repository() {
        let temp_dir = TempDir::new().unwrap();
        let repo_path = temp_dir.path().join("test.git");

        // Initialize first time
        create_repository(&repo_path).unwrap();

        // Open second time
        let repo = init_or_open_repository(&repo_path).unwrap();
        assert!(repo.is_bare());
    }

    #[test]
    fn test_list_tags_empty() {
        let temp_dir = TempDir::new().unwrap();
        let repo_path = temp_dir.path().join("test.git");
        create_repository(&repo_path).unwrap();
        let repo = gix::open(&repo_path).unwrap();

        let tags = list_tags(&repo).unwrap();
        assert_eq!(tags.len(), 0);
    }
}
