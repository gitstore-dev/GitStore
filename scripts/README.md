# GitStore Scripts

Utility scripts for GitStore development and demonstration.

## init-demo-catalog.sh

Creates a sample product catalog with categories, collections, and products for demonstration and testing purposes.

### Usage

```bash
./scripts/init-demo-catalog.sh [catalog-path]
```

**Arguments:**
- `catalog-path` (optional): Directory where catalog will be created. Default: `./demo-catalog`

**Example:**

```bash
# Create demo catalog in default location
./scripts/init-demo-catalog.sh

# Create demo catalog in custom location
./scripts/init-demo-catalog.sh ./my-catalog
```

### What It Creates

The script initializes a git repository with:

**Categories (4):**
- Electronics (root)
  - Computers (child)
  - Accessories (child)
- Books (root)

**Collections (3):**
- Featured Products
- New Arrivals
- Best Sellers

**Products (7):**
1. MacBook Pro 16" M3 Max - $3,499 (in stock)
2. ThinkPad X1 Carbon Gen 11 - $1,899 (in stock)
3. Apple Magic Mouse - $99 (in stock)
4. RGB Mechanical Keyboard - $149.99 (in stock)
5. Mastering Go Book - $59.99 (in stock)
6. 7-in-1 USB-C Hub - $79.99 (low stock)
7. 32" 4K Monitor - $899 (out of stock)

### Using with GitStore

After running the script:

1. **Point git-server to the catalog:**
   ```bash
   # In terminal
   export GITSTORE_DATA_DIR=/path/to/demo-catalog
   ```

2. **Create a release tag:**
   ```bash
   cd demo-catalog
   git tag -a v1.0.0 -m "Initial catalog release"
   ```

3. **Start GitStore services:**
   ```bash
   docker compose up
   ```

4. **Query via GraphQL:**
   ```graphql
   query {
     products {
       edges {
         node {
           id
           sku
           title
           price
           category { name }
           collections { name }
         }
       }
     }
   }
   ```

### File Structure

The catalog follows GitStore's markdown + YAML frontmatter format:

```markdown
---
id: prod_example_001
sku: EXAMPLE-SKU-001
title: Example Product
price: 99.99
currency: USD
category_id: cat_example_001
collection_ids:
  - coll_featured_001
inventory_status: in_stock
inventory_quantity: 50
---

# Product Description

Markdown content here...
```

### Use Cases

- **Quick Start**: Get started with GitStore quickly
- **Development**: Test features with realistic data
- **Demos**: Showcase GitStore capabilities
- **Testing**: Validate catalog loading and GraphQL queries
- **Documentation**: Understand data structure through examples

### Customization

Edit the script to:
- Add more products, categories, or collections
- Modify pricing and inventory
- Change metadata fields
- Adjust product descriptions
- Create different catalog structures

### Notes

- The script is idempotent - running it multiple times on an existing catalog will not duplicate data
- Generated catalogs include category hierarchy, collection associations, and various inventory statuses
- All timestamps use ISO 8601 format
- Product images use placeholder URLs (update to real CDN URLs in production)
