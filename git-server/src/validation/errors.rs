// Validation error formatting for git hook output

use std::collections::HashMap;
use std::fmt;

/// Validation result that collects errors by file
#[derive(Debug, Default)]
pub struct ValidationResult {
    errors: HashMap<String, Vec<String>>,
}

impl ValidationResult {
    pub fn new() -> Self {
        Self::default()
    }

    /// Add an error for a specific file
    pub fn add_error(&mut self, file_path: &str, error_message: String) {
        self.errors
            .entry(file_path.to_string())
            .or_insert_with(Vec::new)
            .push(error_message);
    }

    /// Check if validation passed (no errors)
    pub fn is_valid(&self) -> bool {
        self.errors.is_empty()
    }

    /// Get total error count
    pub fn error_count(&self) -> usize {
        self.errors.values().map(|v| v.len()).sum()
    }

    /// Get errors grouped by file
    pub fn get_errors(&self) -> &HashMap<String, Vec<String>> {
        &self.errors
    }

    /// Format errors for git hook output (sent to client)
    pub fn format_for_git(&self) -> String {
        if self.is_valid() {
            return String::from("Validation passed");
        }

        let mut output = String::from("❌ Validation failed:\n\n");

        for (file_path, errors) in &self.errors {
            output.push_str(&format!("📄 {}\n", file_path));
            for error in errors {
                output.push_str(&format!("  ❌ {}\n", error));
            }
            output.push('\n');
        }

        output.push_str(&format!(
            "Total errors: {} in {} file(s)\n",
            self.error_count(),
            self.errors.len()
        ));

        output
    }

    /// Format errors as JSON for programmatic parsing
    pub fn format_as_json(&self) -> Result<String, serde_json::Error> {
        #[derive(serde::Serialize)]
        struct ErrorResponse {
            valid: bool,
            error_count: usize,
            errors: HashMap<String, Vec<String>>,
        }

        let response = ErrorResponse {
            valid: self.is_valid(),
            error_count: self.error_count(),
            errors: self.errors.clone(),
        };

        serde_json::to_string_pretty(&response)
    }
}

impl fmt::Display for ValidationResult {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        write!(f, "{}", self.format_for_git())
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn test_empty_result_is_valid() {
        let result = ValidationResult::new();
        assert!(result.is_valid());
        assert_eq!(result.error_count(), 0);
    }

    #[test]
    fn test_add_error() {
        let mut result = ValidationResult::new();
        result.add_error("test.md", "Error 1".to_string());
        result.add_error("test.md", "Error 2".to_string());

        assert!(!result.is_valid());
        assert_eq!(result.error_count(), 2);
    }

    #[test]
    fn test_multiple_files() {
        let mut result = ValidationResult::new();
        result.add_error("file1.md", "Error A".to_string());
        result.add_error("file2.md", "Error B".to_string());

        assert_eq!(result.error_count(), 2);
        assert_eq!(result.get_errors().len(), 2);
    }

    #[test]
    fn test_format_for_git() {
        let mut result = ValidationResult::new();
        result.add_error(
            "products/TEST-001.md",
            "Missing required field: title".to_string(),
        );
        result.add_error("products/TEST-001.md", "Invalid price: -10.0".to_string());

        let output = result.format_for_git();
        assert!(output.contains("Validation failed"));
        assert!(output.contains("products/TEST-001.md"));
        assert!(output.contains("Missing required field: title"));
        assert!(output.contains("Invalid price: -10.0"));
        assert!(output.contains("Total errors: 2"));
    }

    #[test]
    fn test_format_valid_result() {
        let result = ValidationResult::new();
        let output = result.format_for_git();
        assert_eq!(output, "Validation passed");
    }

    #[test]
    fn test_format_as_json() {
        let mut result = ValidationResult::new();
        result.add_error("test.md", "Test error".to_string());

        let json = result.format_as_json().unwrap();
        assert!(json.contains("\"valid\": false"));
        assert!(json.contains("\"error_count\": 1"));
        assert!(json.contains("test.md"));
    }

    #[test]
    fn test_display_trait() {
        let mut result = ValidationResult::new();
        result.add_error("test.md", "Error".to_string());

        let output = format!("{}", result);
        assert!(output.contains("Validation failed"));
    }
}
