# GitStore Quickstart Guide

**Date**: 2026-03-09
**Target Audience**: Developers, DevOps, Technical Users
**Prerequisites**: Docker, Git, Basic GraphQL knowledge

## Overview

GitStore is a git-backed ecommerce headless engine with three main components:
1. **Git Server** (Rust) - Git repository with validation and websocket notifications
2. **GraphQL API** (Go) - Headless API with Relay support
3. **Admin UI** (Astro/React) - Drag-and-drop catalog management

## Quick Start (5 minutes)

### 1. Clone and Start Services

```bash
# Clone repository
git clone https://github.com/commerce-projects/gitstore
cd gitstore

# Start all services with docker compose
docker compose up -d

# Check service health
docker compose ps
```

**Expected Output**:
```
NAME                STATUS              PORTS
gitstore-git-service running            0.0.0.0:9418->9418/tcp, 0.0.0.0:8080->8080/tcp
gitstore-api        running             0.0.0.0:4000->4000/tcp
gitstore-admin      running             0.0.0.0:3000->3000/tcp
```

### 2. Initialize Demo Catalog

```bash
# Initialize with sample products
./scripts/init-demo-catalog.sh

# Output:
# ✓ Created 10 products in 3 categories
# ✓ Created 2 collections
# ✓ Created release tag v0.1.0
# ✓ Catalog published to http://localhost:4000/graphql
```

### 3. Access Services

- **GraphQL Playground**: http://localhost:4000/graphql
- **Admin UI**: http://localhost:3000
- **Git Repository**: `git://localhost:9418/catalog.git`

### 4. Test GraphQL Query

Open http://localhost:4000/graphql and run:

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

**Goal**: Create and publish a product catalog using git

#### Step 1: Clone Catalog Repository

```bash
git clone git://localhost:9418/catalog.git
cd catalog
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
To git://localhost:9418/catalog.git
   abc1234..def5678  main -> main
```

#### Step 4: Create Release Tag

```bash
git tag -a v0.2.0 -m "Release v0.2.0: Added Premium Laptop"
git push origin v0.2.0
```

**Result**: Storefront updates within 30 seconds (websocket notification)

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

### Journey 2: Organize with Categories & Collections (P2)

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
product_ids:
  - prod_laptop001
display_order: 1
slug: featured
created_at: 2026-03-09T09:00:00Z
updated_at: 2026-03-09T09:00:00Z
---

# Featured Products

This week's featured selection.
EOF
```

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
    depth
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
        "depth": 0,
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

### Journey 3: Admin UI Management (P3)

**Goal**: Non-technical user manages catalog via web interface

#### Step 1: Login to Admin UI

1. Navigate to http://localhost:3000
2. Login with credentials (default: `admin` / `password`)

#### Step 2: Create Product via UI

1. Click **Products** → **New Product**
2. Fill form:
   - **SKU**: `MOUSE-001`
   - **Title**: `Wireless Mouse`
   - **Price**: `29.99`
   - **Category**: Select "Computers" from dropdown
   - **Collections**: Check "Featured"
3. Click **Save Draft**

**Result**: Product saved locally, markdown file generated (not yet committed)

#### Step 3: Drag-and-Drop Category Ordering

1. Click **Categories**
2. Drag "Computers" above "Mobile Devices"
3. Categories reorder, `display_order` fields updated

#### Step 4: Publish Changes

1. Click **Publish** button
2. Enter version: `v0.4.0`
3. Enter message: `Added Wireless Mouse and reordered categories`
4. Click **Publish Catalog**

**Result**:
- Changes committed to git
- Push to git server (validation occurs)
- Release tag created
- Websocket notification triggers storefront reload
- Success message: "Catalog published as v0.4.0"

---

## Architecture Deep Dive

### Component Interaction Flow

```
┌─────────────┐   Git Protocol    ┌─────────────┐
│ Git Client  │   (push/pull)     │   Git       │
│   (CLI)     │──────────────────→│   Server    │
│             │←──────────────────│  (Rust)     │
└─────────────┘   Validation      └──────┬──────┘
                  Errors/Success          │
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
                │  Admin UI   │   │ Storefront  │  │   Other     │
                │  (Astro)    │   │  (Consumer) │  │   Clients   │
                └─────────────┘   └─────────────┘  └─────────────┘
                       │
                       │ GraphQL Mutations
                       │ (create/update/delete)
                       │ + publishCatalog
                       ↓
                ┌─────────────┐   Git Protocol    ┌─────────────┐
                │  GraphQL    │   (commit/tag)    │   Git       │
                │   API       │──────────────────→│   Server    │
                │   (Go)      │←──────────────────│  (Rust)     │
                └─────────────┘   Validation      └─────────────┘
```

### Data Flow: Create Product

**Path 1: Technical User (Git CLI)**
1. **Git Client**: User creates markdown file locally
2. **Git Client**: `git commit` + `git push` to git server
3. **Git Server**: Pre-push validation (Rust) → Accept/Reject
4. **Git Client**: Receives success/failure
5. **Git Client**: `git tag v1.0.0` + `git push --tags`
6. **Git Server**: Tag created → Websocket broadcast
7. **GraphQL API**: Receives websocket → Invalidates cache → Reloads catalog
8. **Storefront**: Queries API → Gets updated catalog

**Path 2: Non-Technical User (Admin UI)**
1. **Admin UI**: User fills form → GraphQL mutation
2. **GraphQL API**: Validate input → Generate markdown → Git commit (internal)
3. **Admin UI**: Multiple edits accumulate (drafts in API memory)
4. **Admin UI**: Click "Publish" → `publishCatalog` mutation
5. **GraphQL API**: Git push to server + tag creation (git protocol)
6. **Git Server**: Pre-push validation → Accept/Reject
7. **Git Server**: Websocket broadcast → Release tag notification
8. **GraphQL API**: Receive websocket → Invalidate cache → Reload catalog
9. **Admin UI + Storefront**: Query API → Cached catalog with new product

---

## Development Setup

### Prerequisites

- **Rust**: 1.75+ (`rustup install stable`)
- **Go**: 1.21+ (`go version`)
- **Node.js**: 18+ (`node --version`)
- **Docker**: 24+ (for local development)
- **Git**: 2.40+

### Build from Source

#### Git Server (Rust)

```bash
cd git-server
cargo build --release
cargo test

# Run standalone
cargo run -- --port 9418 --ws-port 8080 --data-dir ./data
```

#### GraphQL API (Go)

```bash
cd api
go mod download
go generate ./...  # Run gqlgen code generation
go build -o bin/api ./cmd/server

# Run standalone
./bin/api --port 4000 --git-ws ws://localhost:8080
```

#### Admin UI (Astro/React)

```bash
cd admin-ui
npm install
npm run dev  # Development server

# Production build
npm run build
npm run preview
```

### Environment Variables

#### Git Server

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

#### Admin UI

```bash
GITSTORE_GRAPHQL_URL=http://api:4000/graphql
GITSTORE_AUTH_SECRET=your-secret-key
GITSTORE_SESSION_TIMEOUT=3600  # 1 hour
```

---

## Testing

### Contract Tests (GraphQL Schema)

```bash
cd api
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
cd git-server
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

### E2E Tests (Admin UI)

```bash
cd admin-ui
npm run test:e2e
```

**Example Test** (Playwright):
```typescript
test('create product via admin UI', async ({ page }) => {
  await page.goto('http://localhost:3000');
  await page.click('text=New Product');
  await page.fill('[name="sku"]', 'MOUSE-001');
  await page.fill('[name="title"]', 'Wireless Mouse');
  await page.click('text=Save Draft');
  await expect(page.locator('text=Product saved')).toBeVisible();
});
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
curl -X POST http://localhost:4000/admin/cache/invalidate
```

### Issue: Orphaned Product References

**Error in GraphQL response**:
```json
{
  "data": {
    "product": {
      "category": null,
      "errors": ["Category cat_invalid not found"]
    }
  }
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

1. **Read Specification**: [spec.md](./spec.md) - Full feature requirements
2. **Review Contracts**: [contracts/](./contracts/) - GraphQL schema
3. **Check Data Model**: [data-model.md](./data-model.md) - Entity definitions
4. **Implementation Plan**: [plan.md](./plan.md) - Technical roadmap
5. **Task Breakdown**: Run `/speckit.tasks` to generate implementation tasks

---

## Support & Resources

- **GitHub Issues**: https://github.com/commerce-projects/gitstore
- **Documentation**: https://docs.gitstore.dev
- **GraphQL Playground**: http://localhost:4000/graphql
- **Constitution**: See `.specify/memory/constitution.md` for development principles
