// YAML front-matter parser for markdown files

use anyhow::{Context, Result};
use serde::de::DeserializeOwned;

/// Parse markdown file with YAML front-matter
/// Returns (front-matter data, body content)
pub fn parse_markdown_with_frontmatter<T: DeserializeOwned>(content: &str) -> Result<(T, String)> {
    // Check for front-matter delimiter
    if !content.starts_with("---\n") {
        anyhow::bail!("Missing front-matter delimiter at start of file");
    }

    // Find end of front-matter
    let rest = &content[4..]; // Skip opening "---\n"
    let end_pos = rest
        .find("\n---\n")
        .context("Missing closing front-matter delimiter")?;

    // Split front-matter and body
    let frontmatter_str = &rest[..end_pos];
    let body = rest[end_pos + 5..].trim().to_string(); // Skip "\n---\n"

    // Parse YAML front-matter
    let frontmatter: T =
        serde_yaml::from_str(frontmatter_str).context("Failed to parse YAML front-matter")?;

    Ok((frontmatter, body))
}

#[cfg(test)]
mod tests {
    use super::*;
    use serde::Deserialize;

    #[derive(Debug, Deserialize, PartialEq)]
    struct TestFrontmatter {
        id: String,
        title: String,
        price: f64,
    }

    #[test]
    fn test_parse_valid_markdown() {
        let content = r#"---
id: prod_test
title: Test Product
price: 29.99
---

# Test Product

This is the body content.
"#;

        let result: Result<(TestFrontmatter, String)> = parse_markdown_with_frontmatter(content);
        assert!(result.is_ok());

        let (frontmatter, body) = result.unwrap();
        assert_eq!(frontmatter.id, "prod_test");
        assert_eq!(frontmatter.title, "Test Product");
        assert_eq!(frontmatter.price, 29.99);
        assert!(body.contains("This is the body content"));
    }

    #[test]
    fn test_parse_missing_opening_delimiter() {
        let content = r#"id: prod_test
title: Test Product
---

Body content
"#;

        let result: Result<(TestFrontmatter, String)> = parse_markdown_with_frontmatter(content);
        assert!(result.is_err());
        assert!(result
            .unwrap_err()
            .to_string()
            .contains("Missing front-matter delimiter"));
    }

    #[test]
    fn test_parse_missing_closing_delimiter() {
        let content = r#"---
id: prod_test
title: Test Product

Body content
"#;

        let result: Result<(TestFrontmatter, String)> = parse_markdown_with_frontmatter(content);
        assert!(result.is_err());
        assert!(result
            .unwrap_err()
            .to_string()
            .contains("Missing closing front-matter delimiter"));
    }

    #[test]
    fn test_parse_invalid_yaml() {
        let content = r#"---
id: prod_test
title: [invalid yaml structure
price: not_a_number
---

Body content
"#;

        let result: Result<(TestFrontmatter, String)> = parse_markdown_with_frontmatter(content);
        assert!(result.is_err());
    }

    #[test]
    fn test_parse_empty_body() {
        let content = r#"---
id: prod_test
title: Test Product
price: 29.99
---
"#;

        let result: Result<(TestFrontmatter, String)> = parse_markdown_with_frontmatter(content);
        assert!(result.is_ok());

        let (_, body) = result.unwrap();
        assert_eq!(body, "");
    }
}
