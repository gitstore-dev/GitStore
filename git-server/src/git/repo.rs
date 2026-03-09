// Git repository management

use anyhow::{Context, Result};
use git2::{Repository, RepositoryInitOptions};
use std::path::Path;
use tracing::{debug, info};

/// Initialize or open a bare git repository
pub fn init_or_open_repository(path: &Path) -> Result<Repository> {
    if path.exists() {
        debug!(path = %path.display(), "Opening existing repository");
        Repository::open(path)
            .with_context(|| format!("Failed to open repository at {}", path.display()))
    } else {
        info!(path = %path.display(), "Initializing new bare repository");
        let mut opts = RepositoryInitOptions::new();
        opts.bare(true);
        opts.mkdir(true);

        Repository::init_opts(path, &opts)
            .with_context(|| format!("Failed to initialize repository at {}", path.display()))
    }
}

/// Get the current HEAD commit
pub fn get_head_commit(repo: &Repository) -> Result<git2::Commit> {
    let head = repo.head().context("Failed to get HEAD reference")?;

    let commit = head
        .peel_to_commit()
        .context("Failed to peel HEAD to commit")?;

    Ok(commit)
}

/// List all tags in the repository
pub fn list_tags(repo: &Repository) -> Result<Vec<String>> {
    let mut tags = Vec::new();

    repo.tag_foreach(|_oid, name| {
        if let Ok(name_str) = std::str::from_utf8(name) {
            // Remove 'refs/tags/' prefix
            if let Some(tag_name) = name_str.strip_prefix("refs/tags/") {
                tags.push(tag_name.to_string());
            }
        }
        true // Continue iteration
    })?;

    Ok(tags)
}

/// Check if a reference is an annotated tag starting with 'v' (release tag)
pub fn is_release_tag(repo: &Repository, tag_name: &str) -> Result<bool> {
    if !tag_name.starts_with('v') {
        return Ok(false);
    }

    let reference = repo
        .find_reference(&format!("refs/tags/{}", tag_name))
        .context("Failed to find tag reference")?;

    // Check if it's an annotated tag (not a lightweight tag)
    let is_annotated = reference.peel_to_tag().is_ok();

    Ok(is_annotated)
}

/// Get commit SHA for a tag
pub fn get_tag_commit(repo: &Repository, tag_name: &str) -> Result<String> {
    let reference = repo
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

        let repo = init_or_open_repository(&repo_path).unwrap();

        assert!(repo.is_bare());
        assert!(repo_path.exists());
    }

    #[test]
    fn test_open_existing_repository() {
        let temp_dir = TempDir::new().unwrap();
        let repo_path = temp_dir.path().join("test.git");

        // Initialize first time
        init_or_open_repository(&repo_path).unwrap();

        // Open second time
        let repo = init_or_open_repository(&repo_path).unwrap();
        assert!(repo.is_bare());
    }

    #[test]
    fn test_list_tags_empty() {
        let temp_dir = TempDir::new().unwrap();
        let repo_path = temp_dir.path().join("test.git");
        let repo = init_or_open_repository(&repo_path).unwrap();

        let tags = list_tags(&repo).unwrap();
        assert_eq!(tags.len(), 0);
    }
}
