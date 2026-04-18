# GitStore Scripts

Utility scripts for GitStore development and demonstration.

## init-demo-catalog.sh

Creates a sample product catalog with categories, collections, and products for demonstration and testing purposes.

### Usage

```bash
./scripts/init-demo-catalog.sh [--data-dir <catalog-path>]
```

**Arguments:**
- `--data-dir <catalog-path>` (optional): Data directory where `catalog.git` will be created

**Path Resolution Precedence:**
1. `GITSTORE_DATA_DIR` environment variable
2. `--data-dir` flag
3. `./demo-catalog` (default)

**Example:**

```bash
# Create demo catalog in default location
./scripts/init-demo-catalog.sh

# Create demo catalog in custom location
./scripts/init-demo-catalog.sh --data-dir ./my-catalog

# Environment variable takes precedence over CLI argument
GITSTORE_DATA_DIR=./from-env ./scripts/init-demo-catalog.sh
```

### What It Creates

The script initializes a git repository at `<CATALOG_PATH>/catalog.git` with:

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

1. **Start GitStore services:**
   ```bash
  # Start all services (git-service must be running before HTTP clone)
   docker compose up --build -d

   # Wait for services to be healthy (about 10-15 seconds)
   docker compose ps
   ```

2. **Clone the catalog repository via HTTP:**
   ```bash
   git clone http://localhost:9418/catalog.git catalog-work
   cd catalog-work
   ```

   **Important**:
  - Clone from `http://localhost:9418/` (git-service endpoint), NOT from filesystem path
  - Git service must be running before this step
   - This ensures `git push` triggers websocket notifications

3. **Create a release tag:**
   ```bash
   # Create annotated release tag
   git tag -a v1.0.0 -m "Initial catalog release"
   ```

4. **Push the tag (triggers notification):**
   ```bash
  # Push tag to git-service via HTTP (triggers websocket notification)
   git push origin v1.0.0

   # Check logs to verify notification
  docker compose logs git-service | grep -i "broadcast"
   # Should see: "Broadcasted tag notification tag=v1.0.0"
   ```

5. **Query via GraphQL:**

   Open http://localhost:4000/playground and run:

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

### Important: Bare Repository vs Working Copy

**Bare Repository** (`demo-catalog/catalog.git/`):
- Created by `init-demo-catalog.sh`
- Contains git objects and references (no working files)
- Used by the git service for serving via git protocol
- **Do NOT work directly in this directory**

**Working Copy** (`catalog-work/`):
- Cloned from bare repository
- Contains actual markdown files you can edit
- Used for making changes and creating tags
- Push changes back to bare repository

### Alternative: Quick Test Without Clone

For quick testing without a working copy:

```bash
# Initialize catalog
export GITSTORE_DATA_DIR=$(pwd)/demo-catalog
./scripts/init-demo-catalog.sh

# Tag directly in bare repo (works but not recommended for regular workflow)
cd $GITSTORE_DATA_DIR/catalog.git
git tag -a v1.0.0 HEAD -m "Initial release"
cd ../..

# Start services
docker compose up --build
```

**Note**: This works for initial testing but is not the recommended workflow for making catalog changes.

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
