// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

// Git hooks handler

use std::path::Path;

use anyhow::Result;
use tracing::warn;

/// Information about a reference update passed to receive lifecycle hooks.
#[derive(Clone)]
pub struct RefUpdate {
    pub ref_name: String,
    pub old_oid: String,
    pub new_oid: String,
}

/// Called before any ref is updated.
/// Non-Ok return rejects the entire push; Ok(()) allows it to proceed.
/// Future: validate push policy via gRPC callout to gitstore-api.
pub fn run_pre_receive(_git_dir: &Path, _updates: &[RefUpdate]) -> Result<()> {
    Ok(())
}

/// Called once per ref being updated.
/// Returns the indices of refs that are accepted; rejected indices are skipped.
/// Future: per-ref policy enforcement (protected branches, signed commits) via gRPC.
pub fn run_update_hooks(_git_dir: &Path, updates: &[RefUpdate]) -> Result<Vec<usize>> {
    Ok((0..updates.len()).collect())
}

/// Called after refs are committed. Exit code is ignored.
/// Future: trigger webhooks / event fan-out via gRPC callout to gitstore-api.
pub fn run_post_receive(_git_dir: &Path, _updates: &[RefUpdate]) {}

/// Check if update is a tag creation
pub fn is_tag_update(ref_name: &str) -> bool {
    ref_name.starts_with("refs/tags/")
}

/// Extract tag name from reference
pub fn get_tag_name(ref_name: &str) -> Option<String> {
    ref_name.strip_prefix("refs/tags/").map(|s| s.to_string())
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
