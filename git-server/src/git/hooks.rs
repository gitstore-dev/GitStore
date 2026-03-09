// Git hooks handler

use anyhow::{Context, Result};
use git2::{DiffOptions, Repository};
use std::path::Path;
use tracing::{debug, warn};

/// Information about a reference update
pub struct RefUpdate {
    pub ref_name: String,
    pub old_oid: String,
    pub new_oid: String,
}

/// Parse pre-receive hook input (from stdin)
/// Format: <old-oid> <new-oid> <ref-name>
pub fn parse_pre_receive_input(input: &str) -> Result<Vec<RefUpdate>> {
    let mut updates = Vec::new();

    for line in input.lines() {
        let parts: Vec<&str> = line.split_whitespace().collect();
        if parts.len() != 3 {
            warn!(line = line, "Invalid pre-receive input line");
            continue;
        }

        updates.push(RefUpdate {
            old_oid: parts[0].to_string(),
            new_oid: parts[1].to_string(),
            ref_name: parts[2].to_string(),
        });
    }

    Ok(updates)
}

/// Get list of modified markdown files in a commit range
pub fn get_modified_markdown_files(
    repo: &Repository,
    old_oid: &str,
    new_oid: &str,
) -> Result<Vec<String>> {
    let old_tree = if old_oid == "0000000000000000000000000000000000000000" {
        // Initial commit - no old tree
        None
    } else {
        let old_commit = repo.find_commit(git2::Oid::from_str(old_oid)?)?;
        Some(old_commit.tree()?)
    };

    let new_commit = repo.find_commit(git2::Oid::from_str(new_oid)?)?;
    let new_tree = new_commit.tree()?;

    let mut diff_opts = DiffOptions::new();
    let diff = repo.diff_tree_to_tree(old_tree.as_ref(), Some(&new_tree), Some(&mut diff_opts))?;

    let mut markdown_files = Vec::new();

    diff.foreach(
        &mut |delta, _progress| {
            if let Some(path) = delta.new_file().path() {
                if path.extension().and_then(|s| s.to_str()) == Some("md") {
                    markdown_files.push(path.to_string_lossy().to_string());
                }
            }
            true // Continue iteration
        },
        None,
        None,
        None,
    )?;

    debug!(
        old_oid = old_oid,
        new_oid = new_oid,
        file_count = markdown_files.len(),
        "Found modified markdown files"
    );

    Ok(markdown_files)
}

/// Get file content at a specific commit
pub fn get_file_content_at_commit(
    repo: &Repository,
    commit_oid: &str,
    file_path: &str,
) -> Result<String> {
    let commit = repo.find_commit(git2::Oid::from_str(commit_oid)?)?;
    let tree = commit.tree()?;

    let tree_entry = tree
        .get_path(Path::new(file_path))
        .context(format!("File not found: {}", file_path))?;

    let blob = repo
        .find_blob(tree_entry.id())
        .context("Failed to load blob")?;

    let content = std::str::from_utf8(blob.content()).context("File content is not valid UTF-8")?;

    Ok(content.to_string())
}

/// Check if update is a tag creation
pub fn is_tag_update(ref_name: &str) -> bool {
    ref_name.starts_with("refs/tags/")
}

/// Extract tag name from reference
pub fn get_tag_name(ref_name: &str) -> Option<String> {
    ref_name.strip_prefix("refs/tags/").map(|s| s.to_string())
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn test_parse_pre_receive_input() {
        let input = "abc123 def456 refs/heads/main\n\
                     000000 fed789 refs/tags/v1.0.0";

        let updates = parse_pre_receive_input(input).unwrap();

        assert_eq!(updates.len(), 2);
        assert_eq!(updates[0].old_oid, "abc123");
        assert_eq!(updates[0].new_oid, "def456");
        assert_eq!(updates[0].ref_name, "refs/heads/main");
        assert_eq!(updates[1].ref_name, "refs/tags/v1.0.0");
    }

    #[test]
    fn test_parse_invalid_input() {
        let input = "invalid line\nabc def\n";
        let updates = parse_pre_receive_input(input).unwrap();
        assert_eq!(updates.len(), 0);
    }

    #[test]
    fn test_is_tag_update() {
        assert!(is_tag_update("refs/tags/v1.0.0"));
        assert!(!is_tag_update("refs/heads/main"));
    }

    #[test]
    fn test_get_tag_name() {
        assert_eq!(get_tag_name("refs/tags/v1.0.0"), Some("v1.0.0".to_string()));
        assert_eq!(get_tag_name("refs/heads/main"), None);
    }
}
