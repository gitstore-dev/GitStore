# GitStore Developer Guide

**Date**: 2026-03-09
**Target Audience**: Developers, DevOps, Technical Users
**Prerequisites**: Docker, Git, Basic GraphQL knowledge

## Overview

GitStore is a git-backed ecommerce headless engine with two core services:
1. **Git Service** (`gitstore-git-service`, Rust) - Git repository with validation and websocket notifications
2. **GraphQL API** (`gitstore-api`, Go) - Headless API with Relay support

> **Admin add-on**: For the optional web interface, see [docs/admin/](admin/).

## Quick Start (5 minutes)

### 1. Clone and Start Services

```bash
# Clone repository
git clone https://github.com/gitstore-dev/gitstore
cd gitstore

# Start all services with docker compose
export COMPOSE_BAKE=true
docker compose up --build -d

# Check service health
docker compose ps
```

**Expected Output**:
```
NAME                 STATUS              PORTS
gitstore-git-service running             0.0.0.0:9418->9418/tcp, 0.0.0.0:8080->8080/tcp
gitstore-api         running             0.0.0.0:4000->4000/tcp
```

### 2. Initialise Demo Catalogue

```bash
# Initialize with sample products
./scripts/init-demo-catalog.sh

# Output:
# ✓ Created 7 products in 4 categories
# ✓ Created 2 collections
# ✓ Created release tag v0.1.0
# ✓ Catalog published to http://localhost:4000/graphql
```

### 3. Access Services

- **GraphQL Playground**: http://localhost:4000/playground
- **Git Repository**: `http://localhost:9418/catalog.git`

### 4. Test GraphQL Query

Open http://localhost:4000/playground and run:

```graphql
query {
  products(first: 5) {
    edges {
      node {
        sku
        title
        price
        category {
          name
        }
      }
    }
  }
}
```

---

## User Journeys

### Journey 1: Technical User - Git Workflow (P1 MVP)

**Goal**: Create and publish a product catalogue using git

#### Step 1: Clone Catalogue Repository

```bash
git clone http://localhost:9418/catalog.git catalogue-work
cd catalogue-work
```

#### Step 2: Create a Product

```bash
mkdir -p products/electronics
cat > products/electronics/LAPTOP-001.md << 'EOF'
---
id: prod_laptop001
sku: LAPTOP-001
title: Premium Laptop
description: High-performance laptop for professionals
price: 1299.99
currency: USD
inventory_status: in_stock
inventory_quantity: 50
category_id: cat_electronics
collection_ids:
  - coll_featured
images:
  - https://cdn.example.com/laptop-001.jpg
metadata:
  brand: TechCorp
  weight_kg: 1.8
created_at: 2026-03-09T10:00:00Z
updated_at: 2026-03-09T10:00:00Z
---

# Premium Laptop

Professional-grade laptop with cutting-edge specs.

## Features
- Intel i7 processor
- 16GB RAM
- 512GB SSD
- 15.6" 4K display
EOF
```

#### Step 3: Commit and Push

```bash
git add products/electronics/LAPTOP-001.md
git commit -m "Add Premium Laptop (LAPTOP-001)"
git push origin main
```

**Expected Output**:
```
Counting objects: 4, done.
Delta compression using up to 8 threads.
Compressing objects: 100% (3/3), done.
Writing objects: 100% (4/4), 512 bytes | 512.00 KiB/s, done.
Total 4 (delta 1), reused 0 (delta 0)
✓ Validation passed: LAPTOP-001.md
✓ SKU unique: LAPTOP-001
✓ Category exists: cat_electronics
To http://localhost:9418/catalog.git
   abc1234..def5678  main -> main
```

> [!NOTE]
> Validation output is illustrative and may vary by git-server version/configuration.

#### Step 4: Create Release Tag

```bash
git tag -a v0.2.0 -m "Release v0.2.0: Added Premium Laptop"
git push origin v0.2.0
```

**Result**: Storefront typically updates within ~30 seconds via websocket notification, but timing may vary.

#### Step 5: Verify Product on Storefront

```bash
curl http://localhost:4000/graphql \
  -H "Content-Type: application/json" \
  -d '{
    "query": "{ product(sku: \"LAPTOP-001\") { title price } }"
  }'
```

**Expected Output**:
```json
{
  "data": {
    "product": {
      "title": "Premium Laptop",
      "price": "1299.99"
    }
  },
  "errors": []
}
```

---

### Journey 2: Organise with Categories & Collections (P2)

**Goal**: Create hierarchical categories and curated collections

#### Step 1: Create Root Category

```bash
cat > categories/electronics.md << 'EOF'
---
id: cat_electronics
name: Electronics
description: Electronic devices and accessories
parent_id: null
display_order: 1
slug: electronics
created_at: 2026-03-09T09:00:00Z
updated_at: 2026-03-09T09:00:00Z
---

# Electronics

Browse our selection of electronic devices.
EOF
```

#### Step 2: Create Subcategory

```bash
cat > categories/computers.md << 'EOF'
---
id: cat_computers
name: Computers
description: Desktops, laptops, and accessories
parent_id: cat_electronics
display_order: 1
slug: computers
created_at: 2026-03-09T09:00:00Z
updated_at: 2026-03-09T09:00:00Z
---

# Computers

High-performance computing solutions.
EOF
```

#### Step 3: Create Collection

```bash
cat > collections/featured.md << 'EOF'
---
id: coll_featured
name: Featured Products
description: Our hand-picked selection
product_ids: []
display_order: 1
slug: featured
created_at: 2026-03-09T09:00:00Z
updated_at: 2026-03-09T09:00:00Z
---

# Featured Products

This week's featured selection.
EOF
```

> [!NOTE]
> Products reference collections via `collection_ids` in product files.

#### Step 4: Commit, Tag, and Push

```bash
git add categories/ collections/
git commit -m "Add Electronics category hierarchy and Featured collection"
git tag -a v0.3.0 -m "Release v0.3.0: Categories and collections"
git push origin main v0.3.0
```

#### Step 5: Query Category Tree

```graphql
query CategoryTree {
  categories {
    name
    slug
    children {
      name
      slug
    }
  }
}
```

**Expected Output**:
```json
{
  "data": {
    "categories": [
      {
        "name": "Electronics",
        "slug": "electronics",
        "children": [
          {
            "name": "Computers",
            "slug": "computers"
          }
        ]
      }
    ]
  },
  "errors": []
}
```

---

## Architecture Deep Dive

### Component Interaction Flow

```
┌─────────────┐   Git Protocol    ┌─────────────┐
│ Git Client  │   (push/pull)     │   Git       │
│   (CLI)     │──────────────────→│   Service   │
│             │←──────────────────│  (Rust)     │
└─────────────┘   Validation      └──────┬──────┘
                  Errors/Success         │
                                         │ Websocket
                                         │ Notification
                                         │ (new tag)
                                         ↓
                                  ┌─────────────┐
                                  │  GraphQL    │
                                  │   API       │
                                  │   (Go)      │
                                  └──────┬──────┘
                                         │
                       ┌─────────────────┼─────────────────┐
                       │ GraphQL         │                 │ GraphQL
                       │                 │                 │
                       ↓                 ↓                 ↓
                ┌─────────────┐   ┌─────────────┐  ┌─────────────┐
                │  Admin      │   │ Storefront  │  │   Other     │
                │  (Astro)    │   │  (Consumer) │  │   Clients   │
                └─────────────┘   └─────────────┘  └─────────────┘
                       │
                       │ GraphQL Mutations
                       │ (create/update/delete)
                       │ + publishCatalog
                       ↓
                ┌─────────────┐   Git Protocol    ┌─────────────┐
                │  GraphQL    │   (commit/tag)    │   Git       │
                │   API       │──────────────────→│   Service   │
                │   (Go)      │←──────────────────│  (Rust)     │
                └─────────────┘   Validation      └─────────────┘
```

### Data Flow: Create Product

**Path 1: Technical User (Git CLI)**
1. **Git Client**: User creates Markdown file locally
2. **Git Client**: `git commit` + `git push` to git server
3. **Git Service**: Pre-push validation (Rust) → Accept/Reject
4. **Git Client**: Receives success/failure
5. **Git Client**: `git tag v1.0.0` + `git push --tags`
6. **Git Service**: Tag created → Websocket broadcast
7. **GraphQL API**: Receives websocket → Invalidates cache → Reloads catalogue
8. **Storefront**: Queries API → Gets updated catalogue

**Path 2: Admin add-on (optional web UI)**

> See [docs/admin/](admin/) for the Admin add-on setup and usage.

---

## Development Setup

### Prerequisites

- **Rust**: 1.75+ (`rustup install stable`)
- **Go**: 1.21+ (`go version`)
- **Node.js**: 18+ (`node --version`)
- **Docker**: 24+ (for local development)
- **Git**: 2.40+

### Build from Source

#### Git Service (Rust)

```bash
cd gitstore-git-service
cargo build --release
cargo test

# Run standalone
cargo run -- --port 9418 --ws-port 8080 --data-dir ./data
```

#### GraphQL API (Go)

```bash
cd gitstore-api
go mod download
go generate ./...  # Run gqlgen code generation
go build -o bin/api ./cmd/server

# Run standalone
./bin/api --port 4000 --git-ws ws://localhost:8080
```

### Environment Variables

#### Git Service

```bash
GITSTORE_GIT_PORT=9418
GITSTORE_WS_PORT=8080
GITSTORE_DATA_DIR=/data/repos
GITSTORE_LOG_LEVEL=info
GITSTORE_MAX_FILE_SIZE=52428800  # 50MB
```

#### GraphQL API

```bash
GITSTORE_API_PORT=4000
GITSTORE_GIT_WS=ws://git-service:8080
GITSTORE_GIT_REPO=/data/repos/catalog.git
GITSTORE_CACHE_TTL=300  # 5 minutes
GITSTORE_LOG_LEVEL=info
```

---

## Testing

> [!NOTE]
> The sample tests below are illustrative. Some CI tests are currently placeholders and may differ from the eventual production test suite.

### Contract Tests (GraphQL Schema)

```bash
cd gitstore-api
go test ./tests/contract/...
```

**Example Test**:
```go
func TestProductSchema(t *testing.T) {
    query := `{ product(sku: "TEST-001") { id title } }`
    resp := executeQuery(query)
    assert.NoError(t, resp.Errors)
    assert.Equal(t, "TEST-001", resp.Data.Product.SKU)
}
```

### Integration Tests (User Journeys)

```bash
cd gitstore-git-service
cargo test --test integration
```

**Example Test**:
```rust
#[test]
fn test_create_product_workflow() {
    let repo = TestRepo::new();
    let product = create_test_product("LAPTOP-001");
    repo.commit_file("products/electronics/LAPTOP-001.md", product);
    repo.push().unwrap();
    let tag = repo.tag("v0.1.0");
    assert_eq!(tag, "v0.1.0");
}
```

---

## Troubleshooting

### Issue: Git Push Rejected

**Error**:
```
! [remote rejected] main -> main (validation failed: SKU LAPTOP-001 already exists)
```

**Solution**:
- Check SKU uniqueness: `grep -r "LAPTOP-001" products/`
- Use different SKU or update existing product

### Issue: Websocket Notification Not Received

**Symptoms**: Storefront not updating after release tag

**Debug**:
```bash
# Check websocket connection
wscat -c ws://localhost:8080

# Check API logs
docker-compose logs api | grep websocket

# Manual cache invalidation
# TODO returns 404
curl -X POST http://localhost:4000/admin/cache/invalidate
```

### Issue: Orphaned Product References

**Error in GraphQL response**:
```json
{
  "data": {
    "product": {
      "category": null
    }
  },
  "errors": [
    {
      "message": "Category cat_invalid not found",
      "locations": [{ "line": 7, "column": 7 }],
      "path": ["product", "category"]
    }
  ]
}
```

**Solution**:
- Update product to reference valid category
- Or delete product if category deletion was intentional

---

## Performance Tuning

### Git Repository Size Management

```bash
# Check repository size
du -sh /data/repos/catalog.git

# Git garbage collection
git gc --aggressive --prune=now

# Compress markdown files
find products -name "*.md" -exec gzip {} \;
```

### API Cache Configuration

Adjust cache TTL based on update frequency:

```go
// High update frequency (every 5 minutes)
cache.TTL = 2 * time.Minute

// Low update frequency (once per day)
cache.TTL = 30 * time.Minute
```

### Database Queries

Use DataLoader to batch product category lookups:

```go
loader := dataloader.NewBatchedLoader(func(keys []string) []*Category {
    return repo.GetCategoriesByIDs(keys)
})
```

---

## Next Steps

1. **User Guide**: [user-guide.md](user-guide.md) - End-user and operator workflows
2. **API Reference**: [api-reference.md](api-reference.md) - GraphQL and service API details
3. **Architecture**: [architecture.md](architecture.md) - System components and data flow
4. **Storefront**: [storefront.md](storefront.md) - Consumer experience notes
5. **GraphQL Contracts**: [../shared/schemas/](../shared/schemas/) - Schema source of truth

---

## Support & Resources

- **GitHub Issues**: https://github.com/gitstore-dev/gitstore/issues
- **Documentation**: https://docs.gitstore.dev
- **GraphQL Playground**: http://localhost:4000/playground
- **Project Overview**: [README.md](../README.md)
