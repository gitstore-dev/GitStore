// Category validation logic

use crate::models::Category;
use thiserror::Error;
use tracing::debug;

#[derive(Error, Debug, PartialEq)]
pub enum CategoryValidationError {
    #[error("Missing required field: {field}")]
    MissingField { field: String },

    #[error("Invalid ID format: {id}. Must match pattern cat_[base62]")]
    InvalidIdFormat { id: String },

    #[error("Invalid slug: {slug}. Slug cannot be empty")]
    InvalidSlug { slug: String },

    #[error("Duplicate slug: {slug}. Slug must be unique")]
    DuplicateSlug { slug: String },

    #[error("Invalid parent reference: {parent_id}. Parent category does not exist")]
    InvalidParentReference { parent_id: String },

    #[error("Circular reference detected: {category_id} cannot be its own ancestor")]
    CircularReference { category_id: String },

    #[error("Invalid display order: {display_order}. Display order cannot be negative")]
    InvalidDisplayOrder { display_order: i32 },
}

/// Validate a single category
pub fn validate_category(category: &Category) -> Result<(), Vec<CategoryValidationError>> {
    let mut errors = Vec::new();

    // Validate ID format
    if !category.id.starts_with("cat_") {
        errors.push(CategoryValidationError::InvalidIdFormat {
            id: category.id.clone(),
        });
    }

    // Validate name
    if category.name.trim().is_empty() {
        errors.push(CategoryValidationError::MissingField {
            field: "name".to_string(),
        });
    }

    // Validate slug
    if category.slug.trim().is_empty() {
        errors.push(CategoryValidationError::InvalidSlug {
            slug: category.slug.clone(),
        });
    }

    // Validate display_order
    if category.display_order < 0 {
        errors.push(CategoryValidationError::InvalidDisplayOrder {
            display_order: category.display_order,
        });
    }

    if errors.is_empty() {
        debug!(id = %category.id, "Category validation passed");
        Ok(())
    } else {
        debug!(id = %category.id, error_count = errors.len(), "Category validation failed");
        Err(errors)
    }
}

/// Check for duplicate slugs across categories
pub fn check_duplicate_slugs(categories: &[Category]) -> Vec<CategoryValidationError> {
    let mut seen_slugs = std::collections::HashSet::new();
    let mut errors = Vec::new();

    for category in categories {
        if !seen_slugs.insert(category.slug.clone()) {
            errors.push(CategoryValidationError::DuplicateSlug {
                slug: category.slug.clone(),
            });
        }
    }

    errors
}

/// Validate parent reference exists
pub fn validate_parent_reference(
    parent_id: &str,
    existing_categories: &[String],
) -> Result<(), CategoryValidationError> {
    if existing_categories.contains(&parent_id.to_string()) {
        Ok(())
    } else {
        Err(CategoryValidationError::InvalidParentReference {
            parent_id: parent_id.to_string(),
        })
    }
}

/// Detect circular references in category hierarchy
pub fn detect_circular_reference(
    category_id: &str,
    parent_id: Option<&str>,
    all_categories: &[(String, Option<String>)], // (id, parent_id)
) -> Result<(), CategoryValidationError> {
    if parent_id.is_none() {
        return Ok(());
    }

    let mut visited = std::collections::HashSet::new();
    visited.insert(category_id.to_string());

    let mut current_parent = parent_id.map(|s| s.to_string());

    // Walk up the tree
    while let Some(parent) = current_parent {
        if visited.contains(&parent) {
            // Circular reference detected
            return Err(CategoryValidationError::CircularReference {
                category_id: category_id.to_string(),
            });
        }

        visited.insert(parent.clone());

        // Find parent's parent
        current_parent = all_categories
            .iter()
            .find(|(id, _)| id == &parent)
            .and_then(|(_, parent_id)| parent_id.clone());
    }

    Ok(())
}

#[cfg(test)]
mod tests {
    use super::*;

    fn create_valid_category() -> Category {
        Category {
            id: "cat_test123".to_string(),
            name: "Test Category".to_string(),
            slug: "test-category".to_string(),
            parent_id: None,
            display_order: 1,
            created_at: "2026-03-09T10:00:00Z".to_string(),
            updated_at: "2026-03-09T10:00:00Z".to_string(),
            body: "Test description".to_string(),
        }
    }

    #[test]
    fn test_validate_valid_category() {
        let category = create_valid_category();
        assert!(validate_category(&category).is_ok());
    }

    #[test]
    fn test_validate_invalid_id_format() {
        let mut category = create_valid_category();
        category.id = "invalid_id".to_string();

        let result = validate_category(&category);
        assert!(result.is_err());

        let errors = result.unwrap_err();
        assert!(errors.iter().any(|e| matches!(e, CategoryValidationError::InvalidIdFormat { .. })));
    }

    #[test]
    fn test_validate_empty_name() {
        let mut category = create_valid_category();
        category.name = "".to_string();

        let result = validate_category(&category);
        assert!(result.is_err());
    }

    #[test]
    fn test_validate_empty_slug() {
        let mut category = create_valid_category();
        category.slug = "".to_string();

        let result = validate_category(&category);
        assert!(result.is_err());

        let errors = result.unwrap_err();
        assert!(errors.iter().any(|e| matches!(e, CategoryValidationError::InvalidSlug { .. })));
    }

    #[test]
    fn test_validate_negative_display_order() {
        let mut category = create_valid_category();
        category.display_order = -1;

        let result = validate_category(&category);
        assert!(result.is_err());

        let errors = result.unwrap_err();
        assert!(errors.iter().any(|e| matches!(e, CategoryValidationError::InvalidDisplayOrder { .. })));
    }

    #[test]
    fn test_check_duplicate_slugs() {
        let categories = vec![
            create_valid_category(),
            create_valid_category(), // Duplicate slug
        ];

        let errors = check_duplicate_slugs(&categories);
        assert_eq!(errors.len(), 1);
        assert!(matches!(errors[0], CategoryValidationError::DuplicateSlug { .. }));
    }

    #[test]
    fn test_validate_parent_reference() {
        let existing = vec!["cat_parent".to_string(), "cat_other".to_string()];

        assert!(validate_parent_reference("cat_parent", &existing).is_ok());
        assert!(validate_parent_reference("cat_nonexistent", &existing).is_err());
    }

    #[test]
    fn test_detect_circular_reference_direct() {
        let categories = vec![
            ("cat_1".to_string(), None),
            ("cat_2".to_string(), Some("cat_2".to_string())), // Self-reference
        ];

        let result = detect_circular_reference("cat_2", Some("cat_2"), &categories);
        assert!(result.is_err());
    }

    #[test]
    fn test_detect_circular_reference_indirect() {
        let categories = vec![
            ("cat_1".to_string(), Some("cat_3".to_string())),
            ("cat_2".to_string(), Some("cat_1".to_string())),
            ("cat_3".to_string(), Some("cat_2".to_string())), // Circular
        ];

        let result = detect_circular_reference("cat_1", Some("cat_3"), &categories);
        assert!(result.is_err());
    }

    #[test]
    fn test_detect_no_circular_reference() {
        let categories = vec![
            ("cat_1".to_string(), None),
            ("cat_2".to_string(), Some("cat_1".to_string())),
            ("cat_3".to_string(), Some("cat_2".to_string())),
        ];

        let result = detect_circular_reference("cat_3", Some("cat_2"), &categories);
        assert!(result.is_ok());
    }
}
