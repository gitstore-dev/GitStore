// Collection validation logic

use crate::models::Collection;
use thiserror::Error;
use tracing::debug;

#[derive(Error, Debug, PartialEq)]
pub enum CollectionValidationError {
    #[error("Missing required field: {field}")]
    MissingField { field: String },

    #[error("Invalid ID format: {id}. Must match pattern coll_[base62]")]
    InvalidIdFormat { id: String },

    #[error("Invalid slug: {slug}. Slug cannot be empty")]
    InvalidSlug { slug: String },

    #[error("Duplicate slug: {slug}. Slug must be unique")]
    DuplicateSlug { slug: String },

    #[error("Invalid product reference: {product_id}. Product does not exist")]
    InvalidProductReference { product_id: String },

    #[error("Invalid display order: {display_order}. Display order cannot be negative")]
    InvalidDisplayOrder { display_order: i32 },
}

/// Validate a single collection
pub fn validate_collection(collection: &Collection) -> Result<(), Vec<CollectionValidationError>> {
    let mut errors = Vec::new();

    // Validate ID format
    if !collection.id.starts_with("coll_") {
        errors.push(CollectionValidationError::InvalidIdFormat {
            id: collection.id.clone(),
        });
    }

    // Validate name
    if collection.name.trim().is_empty() {
        errors.push(CollectionValidationError::MissingField {
            field: "name".to_string(),
        });
    }

    // Validate slug
    if collection.slug.trim().is_empty() {
        errors.push(CollectionValidationError::InvalidSlug {
            slug: collection.slug.clone(),
        });
    }

    // Validate display_order
    if collection.display_order < 0 {
        errors.push(CollectionValidationError::InvalidDisplayOrder {
            display_order: collection.display_order,
        });
    }

    if errors.is_empty() {
        debug!(id = %collection.id, "Collection validation passed");
        Ok(())
    } else {
        debug!(id = %collection.id, error_count = errors.len(), "Collection validation failed");
        Err(errors)
    }
}

/// Check for duplicate slugs across collections
pub fn check_duplicate_slugs(collections: &[Collection]) -> Vec<CollectionValidationError> {
    let mut seen_slugs = std::collections::HashSet::new();
    let mut errors = Vec::new();

    for collection in collections {
        if !seen_slugs.insert(collection.slug.clone()) {
            errors.push(CollectionValidationError::DuplicateSlug {
                slug: collection.slug.clone(),
            });
        }
    }

    errors
}

/// Validate product references exist
pub fn validate_product_references(
    product_ids: &[String],
    existing_products: &[String],
) -> Vec<CollectionValidationError> {
    let mut errors = Vec::new();

    for product_id in product_ids {
        if !existing_products.contains(product_id) {
            errors.push(CollectionValidationError::InvalidProductReference {
                product_id: product_id.clone(),
            });
        }
    }

    errors
}

#[cfg(test)]
mod tests {
    use super::*;

    fn create_valid_collection() -> Collection {
        Collection {
            id: "coll_test123".to_string(),
            name: "Test Collection".to_string(),
            slug: "test-collection".to_string(),
            display_order: 1,
            product_ids: vec!["prod_1".to_string(), "prod_2".to_string()],
            created_at: "2026-03-09T10:00:00Z".to_string(),
            updated_at: "2026-03-09T10:00:00Z".to_string(),
            body: "Test description".to_string(),
        }
    }

    #[test]
    fn test_validate_valid_collection() {
        let collection = create_valid_collection();
        assert!(validate_collection(&collection).is_ok());
    }

    #[test]
    fn test_validate_invalid_id_format() {
        let mut collection = create_valid_collection();
        collection.id = "invalid_id".to_string();

        let result = validate_collection(&collection);
        assert!(result.is_err());

        let errors = result.unwrap_err();
        assert!(errors.iter().any(|e| matches!(e, CollectionValidationError::InvalidIdFormat { .. })));
    }

    #[test]
    fn test_validate_empty_name() {
        let mut collection = create_valid_collection();
        collection.name = "".to_string();

        let result = validate_collection(&collection);
        assert!(result.is_err());
    }

    #[test]
    fn test_validate_empty_slug() {
        let mut collection = create_valid_collection();
        collection.slug = "".to_string();

        let result = validate_collection(&collection);
        assert!(result.is_err());

        let errors = result.unwrap_err();
        assert!(errors.iter().any(|e| matches!(e, CollectionValidationError::InvalidSlug { .. })));
    }

    #[test]
    fn test_validate_negative_display_order() {
        let mut collection = create_valid_collection();
        collection.display_order = -1;

        let result = validate_collection(&collection);
        assert!(result.is_err());

        let errors = result.unwrap_err();
        assert!(errors.iter().any(|e| matches!(e, CollectionValidationError::InvalidDisplayOrder { .. })));
    }

    #[test]
    fn test_check_duplicate_slugs() {
        let collections = vec![
            create_valid_collection(),
            create_valid_collection(), // Duplicate slug
        ];

        let errors = check_duplicate_slugs(&collections);
        assert_eq!(errors.len(), 1);
        assert!(matches!(errors[0], CollectionValidationError::DuplicateSlug { .. }));
    }

    #[test]
    fn test_validate_product_references() {
        let existing = vec!["prod_1".to_string(), "prod_2".to_string()];

        let valid_refs = vec!["prod_1".to_string()];
        let errors = validate_product_references(&valid_refs, &existing);
        assert_eq!(errors.len(), 0);

        let invalid_refs = vec!["prod_1".to_string(), "prod_nonexistent".to_string()];
        let errors = validate_product_references(&invalid_refs, &existing);
        assert_eq!(errors.len(), 1);
    }
}
