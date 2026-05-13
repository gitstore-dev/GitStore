// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

// Git repository management

use anyhow::{Context, Result};
use gix::refs::transaction::PreviousValue;
use std::path::Path;
use tracing::{debug, info};

/// Initialize a new bare repository at `path`.
/// Sets HEAD to refs/heads/main via a post-init edit_reference call.
///
/// gix::init_bare does not accept an initial_head option (unlike git2's
/// RepositoryInitOptions::initial_head). We force HEAD → refs/heads/main
/// with a symbolic ref edit after init. Remove this shim once gix provides
/// an init_opts equivalent. Tracked: specs/007-migrate-gitoxide/research.md §6.
pub fn create_repository(path: &Path) -> Result<()> {
    let repo = gix::init_bare(path)
        .with_context(|| format!("Failed to init bare repository at {}", path.display()))?;
    force_head_to_main(&repo)?;
    Ok(())
}

/// Remove a repository and all its data.
pub fn delete_repository(path: &Path) -> Result<()> {
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
        gix::open(path).with_context(|| format!("Failed to open repository at {}", path.display()))
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
    use tempfile::TempDir;

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
