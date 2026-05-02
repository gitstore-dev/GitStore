// Git event detection

use anyhow::Result;
use git2::Repository;
use serde::{Deserialize, Serialize};
use tracing::{debug, info};

/// Git event types that trigger notifications
#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
#[serde(tag = "event", rename_all = "snake_case")]
pub enum GitEvent {
    /// New release tag created
    ReleaseCreated {
        /// Tag name (e.g., "v1.0.0")
        tag: String,
        /// Commit SHA
        commit: String,
        /// ISO 8601 timestamp
        timestamp: String,
    },

    /// Release tag deleted
    ReleaseDeleted {
        /// Tag name
        tag: String,
        /// ISO 8601 timestamp
        timestamp: String,
    },
}

impl GitEvent {
    /// Create a release created event
    pub fn release_created(tag: String, commit: String) -> Self {
        let timestamp = chrono::Utc::now().to_rfc3339();
        Self::ReleaseCreated {
            tag,
            commit,
            timestamp,
        }
    }

    /// Create a release deleted event
    pub fn release_deleted(tag: String) -> Self {
        let timestamp = chrono::Utc::now().to_rfc3339();
        Self::ReleaseDeleted { tag, timestamp }
    }

    /// Serialize event to JSON string
    pub fn to_json(&self) -> Result<String> {
        Ok(serde_json::to_string(self)?)
    }

    /// Get the tag name from this event
    pub fn tag_name(&self) -> &str {
        match self {
            GitEvent::ReleaseCreated { tag, .. } => tag,
            GitEvent::ReleaseDeleted { tag, .. } => tag,
        }
    }
}

/// Detect if a tag creation should trigger a release event
pub fn should_notify_tag_creation(tag_name: &str, repo: &Repository) -> bool {
    // Only notify for tags starting with 'v' (version tags)
    if !tag_name.starts_with('v') {
        debug!(tag = tag_name, "Skipping non-version tag");
        return false;
    }

    // Check if it's an annotated tag (not lightweight)
    match repo.find_reference(&format!("refs/tags/{}", tag_name)) {
        Ok(reference) => {
            let is_annotated = reference.peel_to_tag().is_ok();
            if !is_annotated {
                debug!(tag = tag_name, "Skipping lightweight tag");
            }
            is_annotated
        }
        Err(_) => false,
    }
}

/// Create a release event from a tag creation
pub fn create_release_event(tag_name: &str, repo: &Repository) -> Result<GitEvent> {
    let reference = repo.find_reference(&format!("refs/tags/{}", tag_name))?;
    let commit = reference.peel_to_commit()?;
    let commit_sha = commit.id().to_string();

    info!(tag = tag_name, commit = %commit_sha, "Creating release event");

    Ok(GitEvent::release_created(tag_name.to_string(), commit_sha))
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn test_release_created_event() {
        let event = GitEvent::release_created("v1.0.0".to_string(), "abc123def456".to_string());

        match event {
            GitEvent::ReleaseCreated { tag, commit, .. } => {
                assert_eq!(tag, "v1.0.0");
                assert_eq!(commit, "abc123def456");
            }
            _ => panic!("Wrong event type"),
        }
    }

    #[test]
    fn test_release_deleted_event() {
        let event = GitEvent::release_deleted("v1.0.0".to_string());

        match event {
            GitEvent::ReleaseDeleted { tag, .. } => {
                assert_eq!(tag, "v1.0.0");
            }
            _ => panic!("Wrong event type"),
        }
    }

    #[test]
    fn test_event_to_json() {
        let event = GitEvent::release_created("v1.0.0".to_string(), "abc123".to_string());

        let json = event.to_json().unwrap();
        assert!(json.contains("release_created"));
        assert!(json.contains("v1.0.0"));
        assert!(json.contains("abc123"));
    }

    #[test]
    fn test_get_tag_name() {
        let event = GitEvent::release_created("v1.0.0".to_string(), "abc123".to_string());

        assert_eq!(event.tag_name(), "v1.0.0");
    }

    #[test]
    fn test_should_notify_non_version_tag() {
        // Create a temporary repo for testing
        use tempfile::TempDir;
        let temp_dir = TempDir::new().unwrap();
        let repo = Repository::init_bare(temp_dir.path()).unwrap();

        // Non-version tags should not trigger notifications
        assert!(!should_notify_tag_creation("latest", &repo));
        assert!(!should_notify_tag_creation("release-candidate", &repo));
    }
}
