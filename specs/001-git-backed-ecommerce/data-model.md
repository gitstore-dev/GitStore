# Data Model: GitStore Catalog Entities

**Date**: 2026-03-09
**Feature**: GitStore - Git-Backed Ecommerce Engine
**Phase**: Phase 1 - Data Model Design

## Entity Relationship Diagram

```
┌─────────────────┐
│    Product      │
│─────────────────│
│ id: ID          │──────┐
│ sku: String!    │      │
│ title: String!  │      │ belongs to exactly one
│ price: Decimal! │      │
│ body: Markdown  │      ↓
│ inventory       │   ┌─────────────────┐
│ images: [URL]   │   │   Category      │
│ categoryId: ID! │───│─────────────────│
│ collectionIds   │   │ id: ID          │
│ metadata        │   │ name: String!   │
│ createdAt       │   │ description     │
│ updatedAt       │   │ parentId: ID?   │──┐ parent-child
└─────────────────┘   │ displayOrder    │  │ hierarchy
         │            │ slug: String!   │  │
         │            │ createdAt       │←─┘
         │            │ updatedAt       │
         │            └─────────────────┘
         │ belongs to multiple
         │
         ↓
┌─────────────────┐
│   Collection    │
│─────────────────│
│ id: ID          │
│ name: String!   │
│ description     │
│ productIds: [ID]│──→ references products
│ displayOrder    │
│ slug: String!   │
│ createdAt       │
│ updatedAt       │
└─────────────────┘
```

---

## Product Entity

### Markdown File Structure
```markdown
---
id: prod_abc123xyz
sku: WIDGET-001
title: Premium Widget
price: 29.99
currency: USD
inventory_status: in_stock
inventory_quantity: 100
category_id: cat_electronics
collection_ids:
  - coll_featured
  - coll_bestsellers
images:
  - https://cdn.example.com/images/widget-001-main.jpg
  - https://cdn.example.com/images/widget-001-alt.jpg
metadata:
  brand: WidgetCo
  weight_kg: 0.5
  dimensions_cm: [10, 15, 5]
created_at: 2026-03-01T10:00:00Z
updated_at: 2026-03-09T14:30:00Z
---

# Premium Widget

A high-quality widget for all your needs - extended markdown description with **rich formatting**.

## Features
- High durability
- Eco-friendly materials
- 2-year warranty

## Technical Specifications
- Weight: 500g
- Dimensions: 10cm x 15cm x 5cm
```

### Field Definitions

| Field | Type | Required | Constraints | Description |
|-------|------|----------|-------------|-------------|
| `id` | String (ID) | Yes | Format: `prod_[a-z0-9]{11}` | Unique product identifier |
| `sku` | String | Yes | Unique, 1-50 chars | Stock Keeping Unit |
| `title` | String | Yes | 1-200 chars | Product display name |
| `price` | Decimal | Yes | > 0, 2 decimal places | Product price |
| `currency` | String | No | ISO 4217 code | Currency code (default: USD) |
| `inventory_status` | Enum | Yes | `in_stock`, `out_of_stock`, `preorder`, `discontinued` | Stock status |
| `inventory_quantity` | Integer | No | >= 0 | Available quantity (optional tracking) |
| `category_id` | String (ID) | Yes | Must reference existing category | Primary category |
| `collection_ids` | [String] | No | Each must reference existing collection | Collections this product belongs to |
| `images` | [String] | No | Valid HTTP(S) URLs | Product image URLs (external CDN) |
| `metadata` | JSON | No | Free-form JSON object | Custom attributes (brand, dimensions, etc.) |
| `created_at` | DateTime | Yes | ISO 8601 format | Creation timestamp |
| `updated_at` | DateTime | Yes | ISO 8601 format | Last modification timestamp |

### Validation Rules

1. **SKU Uniqueness**: SKU must be unique across all products in catalog
2. **Category Reference**: `category_id` must reference an existing category, or validation fails
3. **Collection References**: All IDs in `collection_ids` must reference existing collections (orphaned refs marked invalid per Spec Clarifications)
4. **Price Validation**: Must be positive number with max 2 decimal places
5. **Image URLs**: Must be valid HTTP(S) URLs (no validation of actual resource existence)
6. **ID Format**: `prod_` prefix followed by 11 alphanumeric characters (base62 encoded)

### File Location
`products/[category-slug]/[sku].md`

Example: `products/electronics/WIDGET-001.md`

---

## Category Entity

### Markdown File Structure
```markdown
---
id: cat_electronics
name: Electronics
parent_id: null
display_order: 1
slug: electronics
created_at: 2026-03-01T09:00:00Z
updated_at: 2026-03-01T09:00:00Z
---

# Electronics

Browse our selection of electronic devices, from smartphones to smart home gadgets. Electronic devices and accessories for modern living.

## Subcategories
- Computers
- Mobile Devices
- Audio Equipment
```

### Field Definitions

| Field | Type | Required | Constraints | Description |
|-------|------|----------|-------------|-------------|
| `id` | String (ID) | Yes | Format: `cat_[a-z0-9]{11}` | Unique category identifier |
| `name` | String | Yes | 1-100 chars | Category display name |
| `parent_id` | String (ID) | No | Must reference existing category or null | Parent category (null = root) |
| `display_order` | Integer | Yes | >= 0 | Sort order within parent |
| `slug` | String | Yes | Unique, URL-safe, 1-100 chars | URL-friendly identifier |
| `created_at` | DateTime | Yes | ISO 8601 format | Creation timestamp |
| `updated_at` | DateTime | Yes | ISO 8601 format | Last modification timestamp |

### Validation Rules

1. **Slug Uniqueness**: Slug must be unique across all categories
2. **Parent Reference**: If `parent_id` is not null, must reference existing category
3. **Circular Reference Prevention**: Category cannot be its own ancestor (direct or indirect)
4. **Max Depth**: Category tree depth limited to 5 levels (configurable)
5. **Display Order**: Must be non-negative integer (ties broken by name alphabetically)
6. **ID Format**: `cat_` prefix followed by 11 alphanumeric characters

### Hierarchical Structure Rules

- **Root Categories**: `parent_id: null`
- **Subcategories**: `parent_id` references parent category
- **Product Inheritance**: Products in subcategory appear in parent category queries (per Spec §US2 Scenario 4)
- **Orphan Handling**: If parent deleted, subcategories remain but parent reference marked invalid

### File Location
`categories/[slug].md`

Example:
- `categories/electronics.md` (root)
- `categories/computers.md` (child of electronics)

---

## Collection Entity

### Markdown File Structure
```markdown
---
id: coll_featured
name: Featured Products
product_ids:
  - prod_abc123xyz
  - prod_def456uvw
  - prod_ghi789rst
display_order: 1
slug: featured
created_at: 2026-03-01T09:00:00Z
updated_at: 2026-03-09T15:00:00Z
---

# Featured Products

Our hand-picked selection of featured items. This week's featured selection includes our bestselling items and new arrivals.

## Highlights
- Best sellers from all categories
- New seasonal arrivals
- Limited time offers
```

### Field Definitions

| Field | Type | Required | Constraints | Description |
|-------|------|----------|-------------|-------------|
| `id` | String (ID) | Yes | Format: `coll_[a-z0-9]{11}` | Unique collection identifier |
| `name` | String | Yes | 1-100 chars | Collection display name |
| `product_ids` | [String] | Yes | Each must reference product (or marked invalid) | Products in this collection |
| `display_order` | Integer | Yes | >= 0 | Sort order for collection listing |
| `slug` | String | Yes | Unique, URL-safe, 1-100 chars | URL-friendly identifier |
| `created_at` | DateTime | Yes | ISO 8601 format | Creation timestamp |
| `updated_at` | DateTime | Yes | ISO 8601 format | Last modification timestamp |

### Validation Rules

1. **Slug Uniqueness**: Slug must be unique across all collections
2. **Product References**: All IDs in `product_ids` should reference existing products
3. **Orphaned Products**: If product deleted, ID remains in list but marked invalid in API responses (per Spec Clarifications)
4. **Empty Collections**: Collections with no valid products are allowed (show as empty)
5. **Display Order**: Must be non-negative integer
6. **ID Format**: `coll_` prefix followed by 11 alphanumeric characters

### File Location
`collections/[slug].md`

Example: `collections/featured.md`

---

## Release Tag Entity

### Structure
Git tags with semantic versioning: `v[MAJOR].[MINOR].[PATCH]`

Example: `v1.0.0`, `v1.1.0`, `v2.0.0`

### Tag Metadata
```bash
git tag -a v1.0.0 -m "Initial catalog release
- 150 products
- 12 categories
- 5 collections"
```

### Selection Algorithm
**Latest Tag**: Most recent tag by semantic version comparison (not chronological date)

**Tie-Breaking**: If multiple tags on same commit, highest version number

**Fallback**: If no release tags exist, return error (storefront requires explicit release)

---

## Data Consistency Rules

### Referential Integrity

1. **Product → Category**: Product `category_id` MUST reference existing category at validation time
2. **Product → Collections**: Product `collection_ids` references are soft (orphans allowed, marked invalid)
3. **Category → Parent**: Category `parent_id` MUST reference existing category or be null
4. **No Cascade Delete**: Deleting category/collection does NOT delete products (orphans preserved)

### Validation Timing

- **Pre-Push (Git Server)**: All validation rules enforced before accepting commit
- **Post-Tag (API Load)**: Validation repeated when loading catalog from tag
- **Runtime (API Queries)**: Orphan references filtered/marked in query responses

### Conflict Resolution

- **Git Conflicts**: Rejected at push time, manual resolution required (per Spec Clarifications)
- **Concurrent Edits (Admin UI)**: Optimistic locking with version check (per Spec §FR-024)

---

## ID Generation Strategy

### Format
`[prefix]_[base62(11)]`

**Prefixes**:
- Products: `prod_`
- Categories: `cat_`
- Collections: `coll_`

**Base62 Encoding**: `[0-9a-zA-Z]` (62 characters)
- Collision resistance: 62^11 ≈ 5.2 × 10^19 unique IDs
- Lexicographically sortable
- URL-safe

### Generation
- **Rust (Git Server)**: `nanoid` crate with custom alphabet
- **Go (API)**: `teris-io/shortid` or custom implementation
- **Admin UI**: Server-generated, returned in mutation response

---

## Markdown Body Content

### Purpose
Markdown body (below front-matter) provides rich-text description for:
- SEO content
- Detailed product/category/collection descriptions
- Formatted documentation

### Parsing
- **Storage**: Stored as-is in markdown file (not parsed by git server)
- **API Response**: Raw markdown string returned to clients
- **Rendering**: Client-side markdown-to-HTML rendering (e.g., marked.js, remark)

### Constraints
- Max size: 50KB per file (validation limit)
- No HTML sanitization at server (client responsibility)

---

## State Transitions

### Product Lifecycle
```
Created (draft)
    ↓
Committed to repo
    ↓
Pushed to git server ← validation occurs
    ↓
[PASS] Included in commit
[FAIL] Push rejected with errors
    ↓
Release tag created
    ↓
Published to storefront (30s SLA)
    ↓
[Optional] Updated → repeat push cycle
    ↓
[Optional] Deleted → remove file, commit, tag
```

### Category/Collection Lifecycle
Same as products, with additional:
- **Hierarchy Changes**: Category parent reassignment requires validation
- **Reference Updates**: Collection product list modifications validated

---

## Storage Format Summary

| Entity | Directory | Filename Pattern | Example |
|--------|-----------|------------------|---------|
| Product | `products/[category-slug]/` | `[sku].md` | `products/electronics/WIDGET-001.md` |
| Category | `categories/` | `[slug].md` | `categories/electronics.md` |
| Collection | `collections/` | `[slug].md` | `collections/featured.md` |
| Release | Git tags | `v[MAJOR].[MINOR].[PATCH]` | `v1.0.0` |

---

## Query Patterns & Indexes

### Common Queries (API Layer)

1. **List all products**: Load all files from `products/**/*.md`
2. **Products by category**: Filter by `category_id` (includes subcategories)
3. **Products by collection**: Filter by ID in `collection_ids`
4. **Product by SKU**: Direct file lookup `products/[category-slug]/[sku].md`
5. **Category tree**: Load `categories/*.md` and build hierarchy
6. **Collection list**: Load `collections/*.md` and sort by `display_order`

### Indexing Strategy (In-Memory Cache)

```rust
struct CatalogCache {
    products_by_id: HashMap<String, Product>,
    products_by_sku: HashMap<String, Product>,
    products_by_category: HashMap<String, Vec<String>>,  // category_id -> product_ids
    products_by_collection: HashMap<String, Vec<String>>, // collection_id -> product_ids
    categories_by_id: HashMap<String, Category>,
    categories_by_slug: HashMap<String, Category>,
    collections_by_id: HashMap<String, Collection>,
    collections_by_slug: HashMap<String, Collection>,
    tag_version: String,
    loaded_at: DateTime,
}
```

---

## Migration & Versioning

### Schema Evolution
- **Backward Compatible**: Add optional fields to front-matter (old files remain valid)
- **Breaking Changes**: Require new MAJOR version tag, migration script provided
- **Field Deprecation**: Mark deprecated in docs, remove in next MAJOR version

### Example Migration
Adding `weight_kg` field (backward compatible):
1. Update validation schema to accept `weight_kg` (optional)
2. Old products without field remain valid
3. New products include field
4. API returns `null` for missing field (GraphQL nullable field)

---

## Summary

Data model complete with:
- ✅ Three core entities (Product, Category, Collection) defined
- ✅ Markdown + YAML front-matter format specified
- ✅ Validation rules documented (17 rules total)
- ✅ Referential integrity strategy (hard category, soft collection)
- ✅ File organization structure defined
- ✅ ID generation strategy (prefix + base62)
- ✅ State transitions and lifecycle documented
- ✅ Query patterns and indexing for API caching

Ready to proceed to Contracts definition (GraphQL schema).
