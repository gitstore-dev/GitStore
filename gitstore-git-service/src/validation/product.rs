// Product validation logic

use crate::models::Product;
use thiserror::Error;
use tracing::debug;

#[derive(Error, Debug, PartialEq)]
pub enum ProductValidationError {
    #[error("Missing required field: {field}")]
    MissingField { field: String },

    #[error("Invalid price: {price}. Price must be positive")]
    InvalidPrice { price: f64 },

    #[error("Invalid inventory quantity: {quantity}. Quantity cannot be negative")]
    InvalidInventoryQuantity { quantity: i32 },

    #[error("Duplicate SKU: {sku}. SKU must be unique")]
    DuplicateSKU { sku: String },

    #[error("Invalid category reference: {category_id}. Category does not exist")]
    InvalidCategoryReference { category_id: String },

    #[error("Invalid collection reference: {collection_id}. Collection does not exist")]
    InvalidCollectionReference { collection_id: String },

    #[error("Invalid ID format: {id}. Must match pattern prod_[base62]")]
    InvalidIdFormat { id: String },

    #[error("Invalid SKU format: {sku}. SKU cannot be empty")]
    InvalidSKUFormat { sku: String },

    #[error("Invalid currency code: {currency}. Must be 3-letter ISO 4217 code")]
    InvalidCurrency { currency: String },
}

/// Validate a single product
pub fn validate_product(product: &Product) -> Result<(), Vec<ProductValidationError>> {
    let mut errors = Vec::new();

    // Validate ID format
    if !product.id.starts_with("prod_") {
        errors.push(ProductValidationError::InvalidIdFormat {
            id: product.id.clone(),
        });
    }

    // Validate SKU
    if product.sku.trim().is_empty() {
        errors.push(ProductValidationError::InvalidSKUFormat {
            sku: product.sku.clone(),
        });
    }

    // Validate title
    if product.title.trim().is_empty() {
        errors.push(ProductValidationError::MissingField {
            field: "title".to_string(),
        });
    }

    // Validate price
    if product.price < 0.0 {
        errors.push(ProductValidationError::InvalidPrice {
            price: product.price,
        });
    }

    // Validate inventory quantity
    if let Some(quantity) = product.inventory_quantity {
        if quantity < 0 {
            errors.push(ProductValidationError::InvalidInventoryQuantity { quantity });
        }
    }

    // Validate currency (basic check for 3-letter code)
    if product.currency.len() != 3 {
        errors.push(ProductValidationError::InvalidCurrency {
            currency: product.currency.clone(),
        });
    }

    // Validate category_id is not empty
    if product.category_id.trim().is_empty() {
        errors.push(ProductValidationError::MissingField {
            field: "category_id".to_string(),
        });
    }

    if errors.is_empty() {
        debug!(sku = %product.sku, "Product validation passed");
        Ok(())
    } else {
        debug!(sku = %product.sku, error_count = errors.len(), "Product validation failed");
        Err(errors)
    }
}

/// Check for duplicate SKUs across products
pub fn check_duplicate_skus(products: &[Product]) -> Vec<ProductValidationError> {
    let mut seen_skus = std::collections::HashSet::new();
    let mut errors = Vec::new();

    for product in products {
        if !seen_skus.insert(product.sku.clone()) {
            errors.push(ProductValidationError::DuplicateSKU {
                sku: product.sku.clone(),
            });
        }
    }

    errors
}

/// Validate category reference exists
pub fn validate_category_reference(
    category_id: &str,
    existing_categories: &[String],
) -> Result<(), ProductValidationError> {
    if existing_categories.contains(&category_id.to_string()) {
        Ok(())
    } else {
        Err(ProductValidationError::InvalidCategoryReference {
            category_id: category_id.to_string(),
        })
    }
}

/// Validate collection references exist
pub fn validate_collection_references(
    collection_ids: &[String],
    existing_collections: &[String],
) -> Vec<ProductValidationError> {
    let mut errors = Vec::new();

    for collection_id in collection_ids {
        if !existing_collections.contains(collection_id) {
            errors.push(ProductValidationError::InvalidCollectionReference {
                collection_id: collection_id.clone(),
            });
        }
    }

    errors
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::models::InventoryStatus;

    fn create_valid_product() -> Product {
        Product {
            id: "prod_test123".to_string(),
            sku: "TEST-001".to_string(),
            title: "Test Product".to_string(),
            price: 29.99,
            currency: "USD".to_string(),
            inventory_status: InventoryStatus::InStock,
            inventory_quantity: Some(100),
            category_id: "cat_electronics".to_string(),
            collection_ids: vec![],
            images: vec![],
            metadata: None,
            created_at: "2026-03-09T10:00:00Z".to_string(),
            updated_at: "2026-03-09T10:00:00Z".to_string(),
            body: "Test description".to_string(),
        }
    }

    #[test]
    fn test_validate_valid_product() {
        let product = create_valid_product();
        assert!(validate_product(&product).is_ok());
    }

    #[test]
    fn test_validate_invalid_price() {
        let mut product = create_valid_product();
        product.price = -10.0;

        let result = validate_product(&product);
        assert!(result.is_err());

        let errors = result.unwrap_err();
        assert!(errors
            .iter()
            .any(|e| matches!(e, ProductValidationError::InvalidPrice { .. })));
    }

    #[test]
    fn test_validate_invalid_id_format() {
        let mut product = create_valid_product();
        product.id = "invalid_id".to_string();

        let result = validate_product(&product);
        assert!(result.is_err());

        let errors = result.unwrap_err();
        assert!(errors
            .iter()
            .any(|e| matches!(e, ProductValidationError::InvalidIdFormat { .. })));
    }

    #[test]
    fn test_validate_empty_sku() {
        let mut product = create_valid_product();
        product.sku = "".to_string();

        let result = validate_product(&product);
        assert!(result.is_err());
    }

    #[test]
    fn test_validate_empty_title() {
        let mut product = create_valid_product();
        product.title = "".to_string();

        let result = validate_product(&product);
        assert!(result.is_err());

        let errors = result.unwrap_err();
        assert!(errors.iter().any(
            |e| matches!(e, ProductValidationError::MissingField { field } if field == "title")
        ));
    }

    #[test]
    fn test_validate_negative_inventory() {
        let mut product = create_valid_product();
        product.inventory_quantity = Some(-5);

        let result = validate_product(&product);
        assert!(result.is_err());

        let errors = result.unwrap_err();
        assert!(errors
            .iter()
            .any(|e| matches!(e, ProductValidationError::InvalidInventoryQuantity { .. })));
    }

    #[test]
    fn test_validate_invalid_currency() {
        let mut product = create_valid_product();
        product.currency = "US".to_string(); // Should be 3 letters

        let result = validate_product(&product);
        assert!(result.is_err());

        let errors = result.unwrap_err();
        assert!(errors
            .iter()
            .any(|e| matches!(e, ProductValidationError::InvalidCurrency { .. })));
    }

    #[test]
    fn test_check_duplicate_skus() {
        let products = vec![
            create_valid_product(),
            create_valid_product(), // Duplicate SKU
        ];

        let errors = check_duplicate_skus(&products);
        assert_eq!(errors.len(), 1);
        assert!(matches!(
            errors[0],
            ProductValidationError::DuplicateSKU { .. }
        ));
    }

    #[test]
    fn test_validate_category_reference() {
        let existing = vec!["cat_electronics".to_string(), "cat_books".to_string()];

        assert!(validate_category_reference("cat_electronics", &existing).is_ok());
        assert!(validate_category_reference("cat_nonexistent", &existing).is_err());
    }

    #[test]
    fn test_validate_collection_references() {
        let existing = vec!["coll_featured".to_string(), "coll_sale".to_string()];

        let valid_refs = vec!["coll_featured".to_string()];
        let errors = validate_collection_references(&valid_refs, &existing);
        assert_eq!(errors.len(), 0);

        let invalid_refs = vec!["coll_featured".to_string(), "coll_nonexistent".to_string()];
        let errors = validate_collection_references(&invalid_refs, &existing);
        assert_eq!(errors.len(), 1);
    }
}
