// Markdown file reader utilities

use anyhow::{Context, Result};
use std::fs;
use std::path::Path;

/// Read markdown file content from filesystem
pub fn read_markdown_file(path: &Path) -> Result<String> {
    fs::read_to_string(path)
        .with_context(|| format!("Failed to read markdown file: {}", path.display()))
}

/// Read all markdown files from a directory recursively
pub fn read_markdown_files_from_dir(dir: &Path) -> Result<Vec<(String, String)>> {
    let mut files = Vec::new();

    if !dir.exists() {
        return Ok(files);
    }

    visit_dirs(dir, dir, &mut files)?;

    Ok(files)
}

/// Recursively visit directories and collect markdown files
fn visit_dirs(root: &Path, dir: &Path, files: &mut Vec<(String, String)>) -> Result<()> {
    if dir.is_dir() {
        for entry in fs::read_dir(dir)? {
            let entry = entry?;
            let path = entry.path();

            if path.is_dir() {
                visit_dirs(root, &path, files)?;
            } else if path.extension().and_then(|s| s.to_str()) == Some("md") {
                let content = read_markdown_file(&path)?;
                let relative_path = path
                    .strip_prefix(root)
                    .unwrap_or(&path)
                    .to_string_lossy()
                    .to_string();
                files.push((relative_path, content));
            }
        }
    }

    Ok(())
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::fs;
    use tempfile::TempDir;

    #[test]
    fn test_read_markdown_file() {
        let temp_dir = TempDir::new().unwrap();
        let file_path = temp_dir.path().join("test.md");

        fs::write(&file_path, "# Test Content").unwrap();

        let content = read_markdown_file(&file_path).unwrap();
        assert_eq!(content, "# Test Content");
    }

    #[test]
    fn test_read_nonexistent_file() {
        let result = read_markdown_file(Path::new("/nonexistent/file.md"));
        assert!(result.is_err());
    }

    #[test]
    fn test_read_markdown_files_from_dir() {
        let temp_dir = TempDir::new().unwrap();

        // Create test structure
        fs::create_dir(temp_dir.path().join("products")).unwrap();
        fs::write(
            temp_dir.path().join("products/prod1.md"),
            "---\nid: prod1\n---\nProduct 1",
        )
        .unwrap();
        fs::write(
            temp_dir.path().join("products/prod2.md"),
            "---\nid: prod2\n---\nProduct 2",
        )
        .unwrap();

        let files = read_markdown_files_from_dir(temp_dir.path()).unwrap();
        assert_eq!(files.len(), 2);

        let paths: Vec<&str> = files.iter().map(|(p, _)| p.as_str()).collect();
        assert!(paths.contains(&"products/prod1.md"));
        assert!(paths.contains(&"products/prod2.md"));
    }

    #[test]
    fn test_read_from_nonexistent_dir() {
        let files = read_markdown_files_from_dir(Path::new("/nonexistent")).unwrap();
        assert_eq!(files.len(), 0);
    }

    #[test]
    fn test_read_recursive_directories() {
        let temp_dir = TempDir::new().unwrap();

        // Create nested structure
        fs::create_dir_all(temp_dir.path().join("products/electronics")).unwrap();
        fs::write(
            temp_dir.path().join("products/electronics/laptop.md"),
            "Laptop",
        )
        .unwrap();
        fs::write(temp_dir.path().join("products/book.md"), "Book").unwrap();

        let files = read_markdown_files_from_dir(temp_dir.path()).unwrap();
        assert_eq!(files.len(), 2);
    }
}
