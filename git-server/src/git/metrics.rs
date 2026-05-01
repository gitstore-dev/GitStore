// Git repository size monitoring

use std::path::Path;
use tracing::{debug, warn};

/// Repository size metrics
#[derive(Debug, Clone)]
pub struct RepoMetrics {
    /// Total on-disk size of the repository in bytes
    pub total_bytes: u64,
    /// Number of loose objects
    pub loose_objects: usize,
    /// Number of pack files
    pub pack_files: usize,
}

impl RepoMetrics {
    /// Compute metrics for the repository at `repo_path`.
    ///
    /// `repo_path` should be the bare repository directory (e.g. `catalog.git`).
    pub fn collect(repo_path: &Path) -> Self {
        let total_bytes = dir_size(repo_path);
        let loose_objects = count_loose_objects(repo_path);
        let pack_files = count_pack_files(repo_path);

        debug!(
            path = %repo_path.display(),
            total_bytes,
            loose_objects,
            pack_files,
            "Collected repository metrics"
        );

        Self {
            total_bytes,
            loose_objects,
            pack_files,
        }
    }

    /// Return the total size in mebibytes
    pub fn total_mib(&self) -> f64 {
        self.total_bytes as f64 / (1024.0 * 1024.0)
    }
}

/// Recursively sum file sizes under `dir`
fn dir_size(dir: &Path) -> u64 {
    let mut size = 0u64;
    let Ok(entries) = std::fs::read_dir(dir) else {
        return size;
    };
    for entry in entries.flatten() {
        let path = entry.path();
        if path.is_dir() {
            size += dir_size(&path);
        } else {
            size += entry.metadata().map(|m| m.len()).unwrap_or(0);
        }
    }
    size
}

/// Count loose object files (files under objects/ excluding pack/ and info/)
fn count_loose_objects(repo_path: &Path) -> usize {
    let objects_dir = repo_path.join("objects");
    let Ok(entries) = std::fs::read_dir(&objects_dir) else {
        return 0;
    };

    let mut count = 0;
    for entry in entries.flatten() {
        let name = entry.file_name();
        let name_str = name.to_string_lossy();
        // Loose object dirs are two-character hex directories
        if name_str.len() == 2 && name_str.chars().all(|c| c.is_ascii_hexdigit()) {
            let sub = entry.path();
            if let Ok(sub_entries) = std::fs::read_dir(&sub) {
                count += sub_entries.count();
            }
        }
    }
    count
}

/// Count pack files under objects/pack/
fn count_pack_files(repo_path: &Path) -> usize {
    let pack_dir = repo_path.join("objects").join("pack");
    let Ok(entries) = std::fs::read_dir(&pack_dir) else {
        return 0;
    };
    entries
        .flatten()
        .filter(|e| {
            e.path()
                .extension()
                .map(|ext| ext == "pack")
                .unwrap_or(false)
        })
        .count()
}

/// Log repository metrics at INFO level and emit a warning if the repo exceeds `warn_threshold_mib`.
pub fn log_repo_metrics(repo_path: &Path, warn_threshold_mib: f64) {
    let metrics = RepoMetrics::collect(repo_path);
    tracing::info!(
        path = %repo_path.display(),
        total_mib = format!("{:.2}", metrics.total_mib()),
        loose_objects = metrics.loose_objects,
        pack_files = metrics.pack_files,
        "Repository size metrics"
    );
    if metrics.total_mib() > warn_threshold_mib {
        warn!(
            path = %repo_path.display(),
            total_mib = format!("{:.2}", metrics.total_mib()),
            threshold_mib = warn_threshold_mib,
            "Repository size exceeds warning threshold — consider running git gc"
        );
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::fs;
    use tempfile::TempDir;

    #[test]
    fn test_dir_size_empty() {
        let dir = TempDir::new().unwrap();
        assert_eq!(dir_size(dir.path()), 0);
    }

    #[test]
    fn test_dir_size_with_file() {
        let dir = TempDir::new().unwrap();
        fs::write(dir.path().join("test.txt"), b"hello world").unwrap();
        assert_eq!(dir_size(dir.path()), 11);
    }

    #[test]
    fn test_metrics_nonexistent_path() {
        let metrics = RepoMetrics::collect(Path::new("/nonexistent/path"));
        assert_eq!(metrics.total_bytes, 0);
        assert_eq!(metrics.loose_objects, 0);
        assert_eq!(metrics.pack_files, 0);
    }

    #[test]
    fn test_total_mib() {
        let metrics = RepoMetrics {
            total_bytes: 1024 * 1024,
            loose_objects: 0,
            pack_files: 0,
        };
        assert!((metrics.total_mib() - 1.0).abs() < f64::EPSILON);
    }
}
