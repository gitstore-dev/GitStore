# GitStore Developer Guide

**Date**: 2026-03-09
**Target Audience**: Developers, DevOps, Technical Users
**Prerequisites**: Docker, Git, Basic GraphQL knowledge

## Overview

GitStore is a git-backed ecommerce headless engine with two core services:
1. **Git Service** (`gitstore-git-service`, Rust) - Git protocol transport and websocket notifications
2. **GraphQL API** (`gitstore-api`, Go) - Headless API with Relay support and catalogue validation/policy hooks

> **Catalogue schema note**: `gitstore-git-service` does not parse or validate markdown/frontmatter content. Schema-aware validation and policy enforcement are owned by the API layer and future `git-receive-pack` hooks.

> **Admin**: For the optional web interface, see [docs/admin/](admin/).

## Quick Start (5 minutes)

### 1. Clone and Start Services

```bash
# Clone repository
git clone https://github.com/gitstore-dev/gitstore
cd gitstore

# Start all core services
make compose DETACH=1

# Check service health
make ps

# Create a starter namespace and repository
make bootstrap ADMIN_PASSWORD=<admin-password>
```

**Expected Output**:
```
NAME                 STATUS              PORTS
gitstore-git-service running             0.0.0.0:9418->9418/tcp, 0.0.0.0:8080->8080/tcp
gitstore-api         running             0.0.0.0:4000->4000/tcp
```

### 2. Access Services

- **GraphQL Playground**: http://localhost:4000/playground
- **Git Repository (example)**: use the clone URL printed by `make bootstrap`. Git HTTP transport is keyed by the internal repository ID.

### 3. Test GraphQL Query

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

#### Step 1: Create and Clone a Catalogue Repository

Provision a namespace and repository through the running API:

```bash
make bootstrap NAMESPACE=gitstore REPOSITORY=catalog ADMIN_PASSWORD=<admin-password>
```

Then clone over Smart HTTP using the clone URL printed by `make bootstrap`:

```bash
git clone http://localhost:9418/<repository-id> catalogue-work
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
To http://localhost:9418/catalog
   abc1234..def5678  main -> main
```

> [!NOTE]
> If policy hooks are enabled in your deployment, push output may also include hook diagnostics emitted by the API/policy layer.

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
    "query": "{ product(by: {sku: \"LAPTOP-001\"}) { title price } }"
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
    edges {
      node {
        name
        slug
        children {
          name
          slug
        }
      }
    }
  }
}
```

**Expected Output**:
```json
{
  "data": {
    "categories": {
      "edges": [
        {
          "node": {
            "name": "Electronics",
            "slug": "electronics",
            "children": [
              {
                "name": "Computers",
                "slug": "computers"
              }
            ]
          }
        }
      ]
    }
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
└─────────────┘ Hook / Policy Err.└──────┬──────┘
                  or Success             │
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
                └─────────────┘ Hook / Policy     └─────────────┘
```

### Data Flow: Create Product

**Path 1: Technical User (Git CLI)**
1. **Git Client**: User creates Markdown file locally
2. **Git Client**: `git commit` + `git push` to git server
3. **Git Service**: Git transport accepts/rejects based on git protocol state (no frontmatter schema rewrite)
4. **Git Client**: Receives success/failure
5. **Git Client**: `git tag v1.0.0` + `git push --tags`
6. **Git Service**: Tag created → Websocket broadcast
7. **GraphQL API**: Receives websocket → Invalidates cache → Reloads catalogue
8. **Storefront**: Queries API → Gets updated catalogue

**Path 2: Admin (optional web UI)**

> See [docs/admin/](admin/) for the Admin setup and usage.

---

## Development Setup

### Prerequisites

- **Rust**: 1.75+ (`rustup install stable`)
- **Go**: 1.21+ (`go version`)
- **Node.js**: 18+ (`node --version`)
- **Docker**: 24+ (for local development)
- **Git**: 2.40+
- **Make, curl, jq**: required for root command wrappers and bootstrap targets

### Build from Source

Run aggregate build and test checks from the repository root:

```bash
make build
make test
```

#### Git Service (Rust)

```bash
# Run standalone in the foreground
make git
```

#### GraphQL API (Go)

```bash
# Run standalone in the foreground.
# Requires gitstore-api/.env or shell env for required auth secrets.
make api
```

Run both native services together:

```bash
make dev
```

### Environment Variables

#### Git Service

```bash
GITSTORE_HTTP__PORT=9418
GITSTORE_WS__PORT=8080
GITSTORE_GIT__DATA_DIR=/data/repos
GITSTORE_LOG__LEVEL=info
GITSTORE_GRPC__PORT=50051
GITSTORE_GIT__REPO__MAX_FILE_SIZE=52428800  # 50MB
```

#### GraphQL API

```bash
GITSTORE_API_PORT=4000
GITSTORE_GIT__WS__URI=ws://git-service:8080
GITSTORE_GIT__GRPC__URI=dns:///git-service:50051
GITSTORE_CACHE__TTL=300  # 5 minutes
GITSTORE_LOG__LEVEL=info
```

#### Start ScyllaDB (optional, for database-backed development)

```bash
# Scylla services only
make scylla DETACH=1

# Full core stack with Scylla
make compose-scylla DETACH=1
```

Scylla-backed Go tests use an externally managed ScyllaDB instance. Start
ScyllaDB first, then point the tests at its CQL address:

```bash
cd gitstore-api
GITSTORE_TEST_SCYLLA_ADDR=127.0.0.1:9042 \
  go test -tags scylla -v -timeout 10m ./tests/contract/datastore/... ./internal/datastore/scylla/...
```

### Bootstrap and Clean-up

Bootstrap creates resources through the running GraphQL API. It is create-oriented: existing namespaces or repositories fail clearly instead of being updated.

```bash
# Login and cache a bearer token for later bootstrap commands
make bootstrap-token ADMIN_PASSWORD=<admin-password>

# Create namespace + repository
make bootstrap ADMIN_PASSWORD=<admin-password>

# Or create them separately
make bootstrap-namespace ADMIN_PASSWORD=<admin-password>
make bootstrap-repository ADMIN_PASSWORD=<admin-password>
```

Useful bootstrap variables include `API_URL`, `ADMIN_USERNAME`, `BOOTSTRAP_TOKEN`, `NAMESPACE`, `NAMESPACE_DISPLAY_NAME`, `NAMESPACE_TIER`, `REPOSITORY`, and `DEFAULT_BRANCH`.

For native local service data only:

```bash
make git-clean-data CONFIRM=1
```

For Docker Compose services:

```bash
make down
```

### Go Licence Headers

All Go source files in this repository should include this header near the top of the file:

```go
// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors
```

Run the repository license checks through Make:

```bash
make license-check
```

Install repository hooks once per clone:

```bash
./scripts/install-git-hooks.sh
```

This installs `.git/hooks/pre-commit`, which blocks commits when staged Go files are missing headers or use an outdated year, and `.git/hooks/commit-msg`, which rejects non-Conventional Commit messages.

CI also enforces this via:
- `.github/workflows/go-license-headers.yml`
- `.github/workflows/rust-license-headers.yml`
- `.github/workflows/js-license-headers.yml`

### IDE Setup (GoLand and VS Code)

- **VS Code**
  - Use the `gostorehdr` snippet from `.vscode/go.code-snippets` to insert the standard header quickly.
  - Use the `gitrusthdr` snippet from `.vscode/rust.code-snippets` for Rust headers.
  - Use the `gitjshdr` snippet from `.vscode/javascript-typescript.code-snippets` for JS/TS headers.
  - Run the task `go:check-license-headers-staged` before commit (or `go:check-license-headers-all` for a full scan) from `.vscode/tasks.json`.
  - Run `rust:check-license-headers-staged` (or `rust:check-license-headers-all`) for Rust files.
  - Run `js:check-license-headers-staged` (or `js:check-license-headers-all`) for JS/TS files.

- **GoLand / JetBrains IDEs**
  - Configure a Copyright profile with the same SPDX + copyright text.
  - Enable "Before Commit" execution of:
    - `./scripts/check-go-license-headers.sh --staged`
    - `./scripts/check-rust-license-headers.sh --staged`
    - `./scripts/check-js-license-headers.sh --staged`
  - Optionally add an External Tool that runs the same command for one-click validation.
  - Use Conventional Commits for the commit summary, for example `feat: add product search`.

---

## Testing

Run the standard Rust and Go test suites from the repository root:

```bash
make test
```

Before opening a PR, run the full readiness workflow:

```bash
make pr-ready
```

---

## Troubleshooting

### Issue: Git Push Rejected

**Error**:
```
! [remote rejected] main -> main (pre-receive hook declined)
```

**Cause**:
- A server-side hook rejected the ref update (for example branch/tag policy, signing policy, or API-managed catalogue rules)

**Solution**:
- Read hook output from the push response and from service logs
- Correct the issue in your local branch, then push again

### Issue: Websocket Notification Not Received

**Symptoms**: Storefront not updating after release tag

**Debug**:
```bash
# Check websocket connection
wscat -c ws://localhost:8080

# Check API logs
make logs SERVICE=api

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
du -sh /data/repos/<repository_id>.git

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
