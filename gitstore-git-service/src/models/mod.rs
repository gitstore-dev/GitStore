// Domain models for GitStore entities

pub mod parser;
pub mod reader;

use serde::{Deserialize, Serialize};
use std::collections::HashMap;

/// Product entity - represents a sellable item
#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct Product {
    /// Globally unique identifier (format: prod_[base62])
    pub id: String,

    /// Stock Keeping Unit (unique)
    pub sku: String,

    /// Product display name
    pub title: String,

    /// Product price
    pub price: f64,

    /// Currency code (ISO 4217)
    pub currency: String,

    /// Inventory status
    pub inventory_status: InventoryStatus,

    /// Available quantity (optional)
    pub inventory_quantity: Option<i32>,

    /// Primary category ID
    pub category_id: String,

    /// Collection IDs this product belongs to
    pub collection_ids: Vec<String>,

    /// Product image URLs
    pub images: Vec<String>,

    /// Custom metadata (free-form)
    pub metadata: Option<HashMap<String, serde_json::Value>>,

    /// Creation timestamp
    pub created_at: String,

    /// Last modification timestamp
    pub updated_at: String,

    /// Markdown body content (product description) - comes from markdown, not YAML
    #[serde(default)]
    pub body: String,
}

/// Category entity - hierarchical classification system
#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct Category {
    /// Globally unique identifier (format: cat_[base62])
    pub id: String,

    /// Category display name
    pub name: String,

    /// URL-friendly slug (unique)
    pub slug: String,

    /// Parent category ID (null for root)
    pub parent_id: Option<String>,

    /// Display order within parent
    pub display_order: i32,

    /// Creation timestamp
    pub created_at: String,

    /// Last modification timestamp
    pub updated_at: String,

    /// Markdown body content (category description) - comes from markdown, not YAML
    #[serde(default)]
    pub body: String,
}

/// Collection entity - flat grouping of products
#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct Collection {
    /// Globally unique identifier (format: coll_[base62])
    pub id: String,

    /// Collection display name
    pub name: String,

    /// URL-friendly slug (unique)
    pub slug: String,

    /// Display order for collection listing
    pub display_order: i32,

    /// Product IDs in this collection
    pub product_ids: Vec<String>,

    /// Creation timestamp
    pub created_at: String,

    /// Last modification timestamp
    pub updated_at: String,

    /// Markdown body content (collection description) - comes from markdown, not YAML
    #[serde(default)]
    pub body: String,
}

/// Inventory status enumeration
#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
#[serde(rename_all = "snake_case")]
pub enum InventoryStatus {
    InStock,
    OutOfStock,
    Preorder,
    Discontinued,
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn test_product_serialization() {
        let product = Product {
            id: "prod_test123".to_string(),
            sku: "TEST-001".to_string(),
            title: "Test Product".to_string(),
            price: 29.99,
            currency: "USD".to_string(),
            inventory_status: InventoryStatus::InStock,
            inventory_quantity: Some(100),
            category_id: "cat_test".to_string(),
            collection_ids: vec![],
            images: vec![],
            metadata: None,
            created_at: "2026-03-09T10:00:00Z".to_string(),
            updated_at: "2026-03-09T10:00:00Z".to_string(),
            body: "Test description".to_string(),
        };

        let yaml = serde_yaml::to_string(&product).unwrap();
        assert!(yaml.contains("TEST-001"));
    }

    #[test]
    fn test_category_serialization() {
        let category = Category {
            id: "cat_test".to_string(),
            name: "Test Category".to_string(),
            slug: "test-category".to_string(),
            parent_id: None,
            display_order: 1,
            created_at: "2026-03-09T10:00:00Z".to_string(),
            updated_at: "2026-03-09T10:00:00Z".to_string(),
            body: "Category description".to_string(),
        };

        let yaml = serde_yaml::to_string(&category).unwrap();
        assert!(yaml.contains("test-category"));
    }

    #[test]
    fn test_collection_serialization() {
        let collection = Collection {
            id: "coll_test".to_string(),
            name: "Test Collection".to_string(),
            slug: "test-collection".to_string(),
            display_order: 1,
            product_ids: vec!["prod_1".to_string(), "prod_2".to_string()],
            created_at: "2026-03-09T10:00:00Z".to_string(),
            updated_at: "2026-03-09T10:00:00Z".to_string(),
            body: "Collection description".to_string(),
        };

        let yaml = serde_yaml::to_string(&collection).unwrap();
        assert!(yaml.contains("test-collection"));
    }
}
