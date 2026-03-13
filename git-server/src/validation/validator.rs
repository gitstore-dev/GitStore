// Validation orchestrator - coordinates validation of all catalog entities

use git2::{ObjectType, Oid, Repository};
use std::str;
use crate::models::parser::parse_markdown_with_frontmatter;
use crate::models::{Category, Collection, Product};
use crate::validation::category::{
    check_duplicate_slugs as check_duplicate_category_slugs, detect_circular_reference,
    validate_category, validate_parent_reference, CategoryValidationError,
};
use crate::validation::collection::{
    check_duplicate_slugs as check_duplicate_collection_slugs, validate_collection,
    validate_product_references, CollectionValidationError,
};
use crate::validation::errors::ValidationResult;
use crate::validation::product::{
    check_duplicate_skus, validate_category_reference, validate_collection_references,
    validate_product, ProductValidationError,
};
use anyhow::{Context, Result};
use tracing::{debug, info, warn};

/// Git push validator (public API)
pub struct Validator {}

impl Validator {
    pub fn new() -> Self {
        Self {}
    }

    /// Validate a git push operation
    ///
    /// Checks all modified/added files between old and new commits
    pub fn validate_push(
        &self,
        repo: &Repository,
        _old_commit_id: Option<&str>,
        new_commit_id: &str,
    ) -> Result<(), ValidationResult> {
        let new_oid = Oid::from_str(new_commit_id)
            .map_err(|e| ValidationResult::with_error("", &format!("Invalid commit ID: {}", e)))?;

        let new_commit = repo
            .find_commit(new_oid)
            .map_err(|e| ValidationResult::with_error("", &format!("Commit not found: {}", e)))?;

        // Get the tree of the new commit
        let new_tree = new_commit
            .tree()
            .map_err(|e| ValidationResult::with_error("", &format!("Failed to get tree: {}", e)))?;

        // Load all files from the new commit
        let mut validator = CatalogValidator::new();

        // Walk the tree and load all markdown files
        new_tree
            .walk(git2::TreeWalkMode::PreOrder, |root, entry| {
                if let Some(name) = entry.name() {
                    let file_path = if root.is_empty() {
                        name.to_string()
                    } else {
                        format!("{}/{}", root, name)
                    };

                    // Only process markdown files in catalog directories
                    if (file_path.starts_with("products/")
                        || file_path.starts_with("categories/")
                        || file_path.starts_with("collections/"))
                        && file_path.ends_with(".md")
                    {
                        if let Ok(object) = entry.to_object(repo) {
                            if object.kind() == Some(ObjectType::Blob) {
                                if let Some(blob) = object.as_blob() {
                                    if let Ok(content) = str::from_utf8(blob.content()) {
                                        if let Err(e) = validator.load_file(&file_path, content) {
                                            warn!(
                                                file = %file_path,
                                                error = %e,
                                                "Failed to load file during validation"
                                            );
                                        }
                                    }
                                }
                            }
                        }
                    }
                }
                git2::TreeWalkResult::Ok
            })
            .ok();

        // Run validation
        let result = validator.validate();

        if result.is_valid() {
            Ok(())
        } else {
            Err(result)
        }
    }
}

impl Default for Validator {
    fn default() -> Self {
        Self::new()
    }
}

/// Catalog validator that coordinates validation across all entity types
pub struct CatalogValidator {
    products: Vec<(String, Product)>,       // (file_path, product)
    categories: Vec<(String, Category)>,    // (file_path, category)
    collections: Vec<(String, Collection)>, // (file_path, collection)
}

impl Default for CatalogValidator {
    fn default() -> Self {
        Self::new()
    }
}

impl CatalogValidator {
    pub fn new() -> Self {
        Self {
            products: Vec::new(),
            categories: Vec::new(),
            collections: Vec::new(),
        }
    }

    /// Parse and load a markdown file
    pub fn load_file(&mut self, file_path: &str, content: &str) -> Result<()> {
        // Determine entity type from file path
        if file_path.starts_with("products/") {
            let (product, body) = parse_markdown_with_frontmatter::<Product>(content)
                .context(format!("Failed to parse product file: {}", file_path))?;

            // Update body from markdown content
            let mut product = product;
            product.body = body;

            self.products.push((file_path.to_string(), product));
            debug!(file_path = file_path, "Loaded product");
        } else if file_path.starts_with("categories/") {
            let (category, body) = parse_markdown_with_frontmatter::<Category>(content)
                .context(format!("Failed to parse category file: {}", file_path))?;

            let mut category = category;
            category.body = body;

            self.categories.push((file_path.to_string(), category));
            debug!(file_path = file_path, "Loaded category");
        } else if file_path.starts_with("collections/") {
            let (collection, body) = parse_markdown_with_frontmatter::<Collection>(content)
                .context(format!("Failed to parse collection file: {}", file_path))?;

            let mut collection = collection;
            collection.body = body;

            self.collections.push((file_path.to_string(), collection));
            debug!(file_path = file_path, "Loaded collection");
        } else {
            debug!(file_path = file_path, "Skipping non-catalog markdown file");
        }

        Ok(())
    }

    /// Validate all loaded entities
    pub fn validate(&self) -> ValidationResult {
        let mut result = ValidationResult::new();

        info!(
            products = self.products.len(),
            categories = self.categories.len(),
            collections = self.collections.len(),
            "Starting catalog validation"
        );

        // Build lookup maps for reference validation
        let category_ids: Vec<String> = self
            .categories
            .iter()
            .map(|(_, cat)| cat.id.clone())
            .collect();

        let collection_ids: Vec<String> = self
            .collections
            .iter()
            .map(|(_, coll)| coll.id.clone())
            .collect();

        // Validate products
        for (file_path, product) in &self.products {
            // Basic product validation
            if let Err(errors) = validate_product(product) {
                for error in errors {
                    result.add_error(file_path, error.to_string());
                }
            }

            // Category reference validation
            if let Err(error) = validate_category_reference(&product.category_id, &category_ids) {
                result.add_error(file_path, error.to_string());
            }

            // Collection reference validation
            let errors = validate_collection_references(&product.collection_ids, &collection_ids);
            for error in errors {
                result.add_error(file_path, error.to_string());
            }
        }

        // Check for duplicate SKUs
        let all_products: Vec<Product> = self.products.iter().map(|(_, p)| p.clone()).collect();
        let dup_errors = check_duplicate_skus(&all_products);
        for error in dup_errors {
            // Find which files have the duplicate SKU
            if let ProductValidationError::DuplicateSKU { sku } = &error {
                for (file_path, product) in &self.products {
                    if product.sku == *sku {
                        result.add_error(file_path, error.to_string());
                    }
                }
            }
        }

        // Validate categories
        for (file_path, category) in &self.categories {
            // Basic category validation
            if let Err(errors) = validate_category(category) {
                for error in errors {
                    result.add_error(file_path, error.to_string());
                }
            }

            // Parent reference validation
            if let Some(parent_id) = &category.parent_id {
                if let Err(error) = validate_parent_reference(parent_id, &category_ids) {
                    result.add_error(file_path, error.to_string());
                }

                // Circular reference detection
                let category_hierarchy: Vec<(String, Option<String>)> = self
                    .categories
                    .iter()
                    .map(|(_, cat)| (cat.id.clone(), cat.parent_id.clone()))
                    .collect();

                if let Err(error) =
                    detect_circular_reference(&category.id, Some(parent_id), &category_hierarchy)
                {
                    result.add_error(file_path, error.to_string());
                }
            }
        }

        // Check for duplicate category slugs
        let all_categories: Vec<Category> =
            self.categories.iter().map(|(_, c)| c.clone()).collect();
        let cat_slug_errors = check_duplicate_category_slugs(&all_categories);
        for error in cat_slug_errors {
            // Find which files have the duplicate slug
            if let CategoryValidationError::DuplicateSlug { slug } = &error {
                for (file_path, category) in &self.categories {
                    if category.slug == *slug {
                        result.add_error(file_path, error.to_string());
                    }
                }
            }
        }

        // Validate collections
        let product_ids: Vec<String> = self
            .products
            .iter()
            .map(|(_, prod)| prod.id.clone())
            .collect();

        for (file_path, collection) in &self.collections {
            // Basic collection validation
            if let Err(errors) = validate_collection(collection) {
                for error in errors {
                    result.add_error(file_path, error.to_string());
                }
            }

            // Product reference validation
            let errors = validate_product_references(&collection.product_ids, &product_ids);
            for error in errors {
                result.add_error(file_path, error.to_string());
            }
        }

        // Check for duplicate collection slugs
        let all_collections: Vec<Collection> =
            self.collections.iter().map(|(_, c)| c.clone()).collect();
        let coll_slug_errors = check_duplicate_collection_slugs(&all_collections);
        for error in coll_slug_errors {
            // Find which files have the duplicate slug
            if let CollectionValidationError::DuplicateSlug { slug } = &error {
                for (file_path, collection) in &self.collections {
                    if collection.slug == *slug {
                        result.add_error(file_path, error.to_string());
                    }
                }
            }
        }

        if result.is_valid() {
            info!("Catalog validation passed");
        } else {
            warn!(
                error_count = result.error_count(),
                "Catalog validation failed"
            );
        }

        result
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn test_load_and_validate_valid_product() {
        let mut validator = CatalogValidator::new();

        let content = r#"---
id: prod_test123
sku: TEST-001
title: Test Product
price: 29.99
currency: USD
inventory_status: in_stock
inventory_quantity: 100
category_id: cat_electronics
collection_ids: []
images: []
created_at: 2026-03-09T10:00:00Z
updated_at: 2026-03-09T10:00:00Z
---

# Test Product

Description here.
"#;

        // Add a category for reference validation
        let category_content = r#"---
id: cat_electronics
name: Electronics
slug: electronics
parent_id: null
display_order: 1
created_at: 2026-03-09T10:00:00Z
updated_at: 2026-03-09T10:00:00Z
---

# Electronics
"#;

        validator
            .load_file("categories/electronics.md", category_content)
            .unwrap();
        validator
            .load_file("products/electronics/TEST-001.md", content)
            .unwrap();

        let result = validator.validate();
        assert!(result.is_valid());
    }

    #[test]
    fn test_validate_missing_category_reference() {
        let mut validator = CatalogValidator::new();

        let content = r#"---
id: prod_test123
sku: TEST-001
title: Test Product
price: 29.99
currency: USD
inventory_status: in_stock
category_id: cat_nonexistent
collection_ids: []
images: []
created_at: 2026-03-09T10:00:00Z
updated_at: 2026-03-09T10:00:00Z
---

# Test Product
"#;

        validator
            .load_file("products/TEST-001.md", content)
            .unwrap();

        let result = validator.validate();
        assert!(!result.is_valid());
        assert!(result.get_errors().contains_key("products/TEST-001.md"));
    }

    #[test]
    fn test_validate_duplicate_skus() {
        let mut validator = CatalogValidator::new();

        let product1 = r#"---
id: prod_test1
sku: DUP-001
title: Product 1
price: 10.0
currency: USD
inventory_status: in_stock
category_id: cat_test
collection_ids: []
images: []
created_at: 2026-03-09T10:00:00Z
updated_at: 2026-03-09T10:00:00Z
---

# Product 1
"#;

        let product2 = r#"---
id: prod_test2
sku: DUP-001
title: Product 2
price: 20.0
currency: USD
inventory_status: in_stock
category_id: cat_test
collection_ids: []
images: []
created_at: 2026-03-09T10:00:00Z
updated_at: 2026-03-09T10:00:00Z
---

# Product 2
"#;

        let category = r#"---
id: cat_test
name: Test
slug: test
parent_id: null
display_order: 1
created_at: 2026-03-09T10:00:00Z
updated_at: 2026-03-09T10:00:00Z
---

# Test
"#;

        validator.load_file("categories/test.md", category).unwrap();
        validator.load_file("products/prod1.md", product1).unwrap();
        validator.load_file("products/prod2.md", product2).unwrap();

        let result = validator.validate();
        assert!(!result.is_valid());
        assert_eq!(result.error_count(), 2); // Both files flagged
    }

    #[test]
    fn test_validate_category_circular_reference() {
        let mut validator = CatalogValidator::new();

        let cat1 = r#"---
id: cat_1
name: Category 1
slug: cat-1
parent_id: cat_3
display_order: 1
created_at: 2026-03-09T10:00:00Z
updated_at: 2026-03-09T10:00:00Z
---

# Category 1
"#;

        let cat2 = r#"---
id: cat_2
name: Category 2
slug: cat-2
parent_id: cat_1
display_order: 1
created_at: 2026-03-09T10:00:00Z
updated_at: 2026-03-09T10:00:00Z
---

# Category 2
"#;

        let cat3 = r#"---
id: cat_3
name: Category 3
slug: cat-3
parent_id: cat_2
display_order: 1
created_at: 2026-03-09T10:00:00Z
updated_at: 2026-03-09T10:00:00Z
---

# Category 3
"#;

        validator.load_file("categories/cat1.md", cat1).unwrap();
        validator.load_file("categories/cat2.md", cat2).unwrap();
        validator.load_file("categories/cat3.md", cat3).unwrap();

        let result = validator.validate();
        assert!(!result.is_valid());
        // All three categories have circular references
        assert!(result.error_count() >= 3);
    }

    #[test]
    fn test_validate_duplicate_category_slugs() {
        let mut validator = CatalogValidator::new();

        let cat1 = r#"---
id: cat_1
name: Category 1
slug: duplicate-slug
parent_id: null
display_order: 1
created_at: 2026-03-09T10:00:00Z
updated_at: 2026-03-09T10:00:00Z
---

# Category 1
"#;

        let cat2 = r#"---
id: cat_2
name: Category 2
slug: duplicate-slug
parent_id: null
display_order: 2
created_at: 2026-03-09T10:00:00Z
updated_at: 2026-03-09T10:00:00Z
---

# Category 2
"#;

        validator.load_file("categories/cat1.md", cat1).unwrap();
        validator.load_file("categories/cat2.md", cat2).unwrap();

        let result = validator.validate();
        assert!(!result.is_valid());
        assert_eq!(result.error_count(), 2); // Both files flagged
    }

    #[test]
    fn test_validate_collection_invalid_product_reference() {
        let mut validator = CatalogValidator::new();

        let category = r#"---
id: cat_test
name: Test
slug: test
parent_id: null
display_order: 1
created_at: 2026-03-09T10:00:00Z
updated_at: 2026-03-09T10:00:00Z
---

# Test
"#;

        let product = r#"---
id: prod_1
sku: TEST-001
title: Test Product
price: 10.0
currency: USD
inventory_status: in_stock
category_id: cat_test
collection_ids: []
images: []
created_at: 2026-03-09T10:00:00Z
updated_at: 2026-03-09T10:00:00Z
---

# Test Product
"#;

        let collection = r#"---
id: coll_featured
name: Featured
slug: featured
display_order: 1
product_ids:
  - prod_1
  - prod_nonexistent
created_at: 2026-03-09T10:00:00Z
updated_at: 2026-03-09T10:00:00Z
---

# Featured Collection
"#;

        validator.load_file("categories/test.md", category).unwrap();
        validator.load_file("products/test.md", product).unwrap();
        validator
            .load_file("collections/featured.md", collection)
            .unwrap();

        let result = validator.validate();
        assert!(!result.is_valid());
        // Should have error for invalid product reference
        assert!(result.get_errors().contains_key("collections/featured.md"));
    }

    #[test]
    fn test_validate_duplicate_collection_slugs() {
        let mut validator = CatalogValidator::new();

        let coll1 = r#"---
id: coll_1
name: Collection 1
slug: duplicate-slug
display_order: 1
product_ids: []
created_at: 2026-03-09T10:00:00Z
updated_at: 2026-03-09T10:00:00Z
---

# Collection 1
"#;

        let coll2 = r#"---
id: coll_2
name: Collection 2
slug: duplicate-slug
display_order: 2
product_ids: []
created_at: 2026-03-09T10:00:00Z
updated_at: 2026-03-09T10:00:00Z
---

# Collection 2
"#;

        validator.load_file("collections/coll1.md", coll1).unwrap();
        validator.load_file("collections/coll2.md", coll2).unwrap();

        let result = validator.validate();
        assert!(!result.is_valid());
        assert_eq!(result.error_count(), 2); // Both files flagged
    }

    #[test]
    fn test_validate_complete_catalog() {
        let mut validator = CatalogValidator::new();

        // Valid hierarchical categories
        let root_category = r#"---
id: cat_electronics
name: Electronics
slug: electronics
parent_id: null
display_order: 1
created_at: 2026-03-09T10:00:00Z
updated_at: 2026-03-09T10:00:00Z
---

# Electronics
"#;

        let child_category = r#"---
id: cat_laptops
name: Laptops
slug: laptops
parent_id: cat_electronics
display_order: 1
created_at: 2026-03-09T10:00:00Z
updated_at: 2026-03-09T10:00:00Z
---

# Laptops
"#;

        // Valid products
        let product1 = r#"---
id: prod_laptop1
sku: LAPTOP-001
title: Gaming Laptop
price: 1299.99
currency: USD
inventory_status: in_stock
inventory_quantity: 10
category_id: cat_laptops
collection_ids:
  - coll_featured
images: []
created_at: 2026-03-09T10:00:00Z
updated_at: 2026-03-09T10:00:00Z
---

# Gaming Laptop
"#;

        let product2 = r#"---
id: prod_laptop2
sku: LAPTOP-002
title: Business Laptop
price: 999.99
currency: USD
inventory_status: in_stock
inventory_quantity: 15
category_id: cat_laptops
collection_ids:
  - coll_featured
  - coll_sale
images: []
created_at: 2026-03-09T10:00:00Z
updated_at: 2026-03-09T10:00:00Z
---

# Business Laptop
"#;

        // Valid collections
        let featured_collection = r#"---
id: coll_featured
name: Featured Products
slug: featured
display_order: 1
product_ids:
  - prod_laptop1
  - prod_laptop2
created_at: 2026-03-09T10:00:00Z
updated_at: 2026-03-09T10:00:00Z
---

# Featured Products
"#;

        let sale_collection = r#"---
id: coll_sale
name: Sale Items
slug: sale
display_order: 2
product_ids:
  - prod_laptop2
created_at: 2026-03-09T10:00:00Z
updated_at: 2026-03-09T10:00:00Z
---

# Sale Items
"#;

        // Load all files
        validator
            .load_file("categories/electronics.md", root_category)
            .unwrap();
        validator
            .load_file("categories/laptops.md", child_category)
            .unwrap();
        validator
            .load_file("products/laptop1.md", product1)
            .unwrap();
        validator
            .load_file("products/laptop2.md", product2)
            .unwrap();
        validator
            .load_file("collections/featured.md", featured_collection)
            .unwrap();
        validator
            .load_file("collections/sale.md", sale_collection)
            .unwrap();

        let result = validator.validate();
        assert!(
            result.is_valid(),
            "Validation should pass for complete valid catalog"
        );
        assert_eq!(result.error_count(), 0);
    }
}
