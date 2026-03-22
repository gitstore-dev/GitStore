# GitStore User Guide

Welcome to GitStore, a git-backed ecommerce headless engine that lets you manage product catalogs using markdown files with YAML front-matter.

## Table of Contents

- [Overview](#overview)
- [Getting Started](#getting-started)
- [Catalog Structure](#catalog-structure)
- [Managing Products](#managing-products)
- [Managing Categories](#managing-categories)
- [Managing Collections](#managing-collections)
- [Publishing Changes](#publishing-changes)
- [Using the Admin UI](#using-the-admin-ui)
- [Querying the GraphQL API](#querying-the-graphql-api)
- [Troubleshooting](#troubleshooting)

## Overview

GitStore provides two ways to manage your product catalog:

1. **Git-based workflow**: Edit markdown files directly, commit changes, and push to publish
2. **Admin UI**: Use a web interface to manage products, categories, and collections

Both workflows use git as the source of truth, ensuring version control, auditability, and collaboration.

## Getting Started

### Prerequisites

- Git installed on your system
- Docker and Docker Compose (for running GitStore services)

### Quick Start

1. **Initialize a demo catalog**:
   ```bash
   ./scripts/init-demo-catalog.sh
   ```

2. **Start GitStore services**:
   ```bash
   docker compose up --build -d
   ```

3. **Wait for services to be healthy** (10-15 seconds):
   ```bash
   docker compose ps
   ```

4. **Clone the catalog repository**:
   ```bash
   git clone http://localhost:9418/catalog.git catalog-work
   cd catalog-work
   ```

5. **Create a release tag**:
   ```bash
   git tag -a v1.0.0 -m "Initial catalog release"
   git push origin v1.0.0
   ```

6. **Access the GraphQL playground**:
   Open http://localhost:4000/playground in your browser

## Catalog Structure

Your catalog repository follows this structure:

```
catalog.git/
├── products/
│   ├── prod_example_001.md
│   └── prod_example_002.md
├── categories/
│   ├── electronics.md
│   └── books.md
└── collections/
    ├── featured.md
    └── new-arrivals.md
```

## Managing Products

### Product File Format

Products are defined in markdown files with YAML front-matter:

```markdown
---
id: prod_macbook_001
sku: MBP-16-M3-2024
title: MacBook Pro 16" M3 Max
price: 3499.00
currency: USD
category_id: cat_computers_001
collection_ids:
  - coll_featured_001
  - coll_new_001
inventory_status: in_stock
inventory_quantity: 15
images:
  - https://cdn.example.com/images/macbook-pro-16-m3.jpg
metadata:
  brand: Apple
  processor: M3 Max
  ram: 36GB
  storage: 1TB SSD
created_at: 2026-01-15T10:00:00Z
updated_at: 2026-01-15T10:00:00Z
---

# MacBook Pro 16" with M3 Max

The most powerful MacBook Pro ever. Supercharged by the M3 Max chip.

## Features

- M3 Max chip with up to 40-core GPU
- 36GB unified memory
- 1TB SSD storage
- 16-inch Liquid Retina XDR display
```

### Required Fields

- `id`: Unique product identifier
- `sku`: Stock Keeping Unit (must be unique)
- `title`: Product name
- `price`: Product price (numeric)
- `currency`: ISO currency code (e.g., USD, EUR)
- `category_id`: Reference to a category

### Optional Fields

- `collection_ids`: Array of collection IDs the product belongs to
- `inventory_status`: One of `in_stock`, `low_stock`, `out_of_stock`
- `inventory_quantity`: Number of items available
- `images`: Array of image URLs (stored externally)
- `metadata`: Flexible key-value pairs for custom attributes

### Creating a Product

1. Create a new markdown file in `products/`:
   ```bash
   touch products/my-product.md
   ```

2. Add front-matter and description (see format above)

3. Commit and push:
   ```bash
   git add products/my-product.md
   git commit -m "Add new product: My Product"
   git push origin main
   ```

4. Create a release tag to publish:
   ```bash
   git tag -a v1.0.1 -m "Add My Product"
   git push origin v1.0.1
   ```

### Updating a Product

1. Edit the product markdown file
2. Update the `updated_at` timestamp
3. Commit and push changes
4. Create a new release tag

### Deleting a Product

1. Delete the product markdown file:
   ```bash
   git rm products/my-product.md
   ```

2. Commit and push:
   ```bash
   git commit -m "Remove product: My Product"
   git push origin main
   ```

3. Create a release tag to publish the deletion

## Managing Categories

### Category File Format

```markdown
---
id: cat_electronics_001
name: Electronics
slug: electronics
parent_id: null
display_order: 0
created_at: 2026-01-01T00:00:00Z
updated_at: 2026-01-01T00:00:00Z
---

All your electronic needs - from computers to accessories.
```

### Hierarchical Categories

Categories support parent-child relationships:

```markdown
---
id: cat_computers_001
name: Computers
slug: computers
parent_id: cat_electronics_001  # Child of Electronics
display_order: 0
---

Desktop computers, laptops, and workstations.
```

### Category Rules

- A product can belong to exactly one category
- Categories can be nested (parent-child relationships)
- Use `display_order` to control sort order
- `slug` is used in URLs and must be unique

## Managing Collections

### Collection File Format

```markdown
---
id: coll_featured_001
name: Featured Products
slug: featured
display_order: 0
created_at: 2026-01-01T00:00:00Z
updated_at: 2026-01-01T00:00:00Z
---

Our hand-picked selection of the best products.
```

### Adding Products to Collections

Products reference collections in their `collection_ids` array:

```yaml
collection_ids:
  - coll_featured_001
  - coll_new_001
```

### Collection Rules

- A product can belong to multiple collections
- Collections are flat (no hierarchy)
- Use collections for merchandising (e.g., "Winter Collection", "Best Sellers")

## Publishing Changes

GitStore reads data from **release tags**, not from the main branch directly.

### Creating a Release

```bash
# Make changes to your catalog files
git add .
git commit -m "Update product prices"

# Create and push an annotated tag
git tag -a v1.0.2 -m "Price updates for Q2"
git push origin v1.0.2
```

### Versioning Strategy

- Use semantic versioning: `v1.0.0`, `v1.0.1`, `v1.1.0`
- Major version: Breaking changes to catalog structure
- Minor version: New products or features
- Patch version: Updates to existing products

### Tag Notifications

When you push a tag, the git-server broadcasts a websocket notification to trigger catalog reload in the API.

## Using the Admin UI

The Admin UI provides a web interface for non-technical users.

### Accessing the Admin UI

Open http://localhost:3000 in your browser.

### Creating Products

1. Navigate to **Products** → **New Product**
2. Fill in the product details form
3. Select a category
4. Optionally add to collections
5. Click **Save Draft**
6. When ready to publish, click **Publish Catalog**

### Publishing from Admin UI

The Admin UI creates git commits and tags automatically:

1. Make your changes in the UI
2. Click **Publish** in the top navigation
3. Enter a version tag (e.g., `v1.0.3`)
4. Enter a commit message
5. Click **Publish Catalog**

This creates a git commit and tag behind the scenes.

### Handling Conflicts

If someone else has made changes via git, the Admin UI will warn you:

- **Merge conflicts**: Admin UI will reject the push. Resolve conflicts manually using git.
- **Concurrent edits**: If another user edited the same product, you'll see a diff and can choose to:
  - Overwrite (discard their changes)
  - Cancel (abandon your changes)
  - Merge manually

## Querying the GraphQL API

### GraphQL Playground

Open http://localhost:4000/playground to explore the API interactively.

### Example Queries

**Get all products**:
```graphql
query {
  products {
    edges {
      node {
        id
        sku
        title
        price
        category {
          name
        }
        collections {
          name
        }
      }
    }
  }
}
```

**Get a specific product by SKU**:
```graphql
query {
  product(sku: "MBP-16-M3-2024") {
    id
    title
    price
    inventoryStatus
  }
}
```

**Filter products by category**:
```graphql
query {
  products(filter: { categoryId: "cat_computers_001" }) {
    edges {
      node {
        id
        title
        price
      }
    }
  }
}
```

**Filter products by price range**:
```graphql
query {
  products(filter: { priceMin: "100", priceMax: "500" }) {
    edges {
      node {
        id
        title
        price
      }
    }
  }
}
```

**Get category hierarchy**:
```graphql
query {
  categories {
    id
    name
    parent {
      name
    }
    children {
      name
    }
  }
}
```

## Troubleshooting

### Products not appearing after push

**Problem**: Pushed changes but products don't appear in GraphQL queries.

**Solution**: Ensure you created a release tag:
```bash
git tag -a v1.0.x -m "Release message"
git push origin v1.0.x
```

GitStore only reads from release tags, not from main/HEAD.

### "Repository does not exist" error

**Problem**: GraphQL API returns "repository does not exist" error.

**Solution**: Verify the catalog repository is initialized:
```bash
ls -la demo-catalog/catalog.git/
```

Check Docker volume mounts in `docker-compose.yml`.

### Admin UI shows stale data

**Problem**: Admin UI doesn't reflect recent git changes.

**Solution**: The Admin UI caches data. To refresh:
1. Make git changes
2. Push and create a new tag
3. Wait for websocket notification (check `docker compose logs git-server`)
4. Refresh the Admin UI browser tab

### Merge conflicts when pushing from Admin UI

**Problem**: Admin UI says "Push failed: merge conflicts detected."

**Solution**: Someone else made git changes. You must resolve manually:
1. Clone or pull the latest changes
2. Resolve conflicts using git
3. Commit and push
4. Refresh the Admin UI

### Validation errors

**Problem**: Push rejected with validation errors.

**Solution**: Check the error message for specific issues:
- Missing required fields (id, sku, title, price, category_id)
- Invalid data types (price must be numeric)
- Invalid YAML syntax in front-matter

Fix the markdown files and try again.

### Images not loading

**Problem**: Product images return 404 errors.

**Solution**: GitStore doesn't store images in git. Host images externally:
- CDN (Cloudflare, Fastly)
- Cloud storage (S3, Google Cloud Storage, Cloudflare R2)
- Image hosting service

Update the `images` array in product front-matter with the correct URLs.

## Best Practices

### Version Control

- Write clear commit messages describing what changed
- Use meaningful release tag messages
- Don't force-push to main branch

### File Organization

- Use descriptive filenames: `macbook-pro-16-m3.md`, not `product1.md`
- Group related products in subdirectories if needed
- Keep markdown files readable for humans

### Performance

- For large catalogs (>10,000 products), consider pagination in queries
- Use GraphQL filters to reduce response size
- Host images on CDN, not in git

### Collaboration

- Coordinate with team members before making bulk changes
- Use branches for experimental catalog changes
- Review diffs before creating release tags

## Additional Resources

- [API Reference](./api-reference.md) - Detailed GraphQL schema documentation
- [Quickstart Guide](../specs/001-git-backed-ecommerce/quickstart.md) - Integration scenarios
- [Scripts README](../scripts/README.md) - Demo catalog initialization

## Support

For issues or questions:
- Check the logs: `docker compose logs`
- Review validation errors in API responses
- Consult the troubleshooting section above
