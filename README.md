# GitStore - Git-Backed Ecommerce Engine

A git-backed headless ecommerce engine where product catalogs are managed as markdown files with YAML front-matter.

## Architecture

```
┌─────────────┐   Git Protocol    ┌─────────────┐
│ Git Client  │   (push/pull)     │   Git       │
│   (CLI)     │──────────────────→│   Server    │
│             │←──────────────────│  (Rust)     │
└─────────────┘   Validation      └──────┬──────┘
                  Errors/Success          │
                                         │ Websocket
                                         │ Notification
                                         ↓
                                  ┌─────────────┐
                                  │  GraphQL    │
                                  │   API       │
                                  │   (Go)      │
                                  └──────┬──────┘
                                         │
                       ┌─────────────────┼─────────────────┐
                       │ GraphQL         │                 │ GraphQL
                       ↓                 ↓                 ↓
                ┌─────────────┐   ┌─────────────┐  ┌─────────────┐
                │  Admin UI   │   │ Storefront  │  │   Other     │
                │  (Astro)    │   │  (Consumer) │  │   Clients   │
                └─────────────┘   └─────────────┘  └─────────────┘
```

## Components

- **Git Server** (Rust): Built-in git repository with validation and websocket notifications
- **GraphQL API** (Go): Headless API with Relay support
- **Admin UI** (Astro/React): Drag-and-drop catalog management

## Quick Start

### Prerequisites

- Docker 24+
- Git 2.40+

### Start Services

```bash
# Clone repository
git clone https://github.com/yourorg/gitstore.git
cd gitstore

# Start all services
docker-compose up -d

# Check service health
docker-compose ps
```

**Expected Output**:
```
NAME                STATUS              PORTS
gitstore-git-server running             0.0.0.0:9418->9418/tcp, 0.0.0.0:8080->8080/tcp
gitstore-api        running             0.0.0.0:4000->4000/tcp
gitstore-admin-ui   running             0.0.0.0:3000->3000/tcp
```

### Access Services

- **GraphQL Playground**: http://localhost:4000/graphql
- **Admin UI**: http://localhost:3000
- **Git Repository**: `git://localhost:9418/catalog.git`

## Development Setup

### Prerequisites

- **Rust**: 1.75+ (`rustup install stable`)
- **Go**: 1.21+ (`go version`)
- **Node.js**: 18+ (`node --version`)
- **Docker**: 24+ (for local development)

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

## Usage

### Technical User - Git Workflow

```bash
# Clone catalog repository
git clone git://localhost:9418/catalog.git
cd catalog

# Create a product
mkdir -p products/electronics
cat > products/electronics/LAPTOP-001.md << 'EOF'
---
id: prod_laptop001
sku: LAPTOP-001
title: Premium Laptop
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

# Commit and push
git add products/electronics/LAPTOP-001.md
git commit -m "Add Premium Laptop (LAPTOP-001)"
git push origin main

# Create release tag
git tag -a v0.1.0 -m "Release v0.1.0: Initial catalog"
git push origin v0.1.0
```

### Query Products via GraphQL

```bash
curl http://localhost:4000/graphql \
  -H "Content-Type: application/json" \
  -d '{
    "query": "{ products(first: 5) { edges { node { sku title price } } } }"
  }'
```

## Testing

```bash
# Run all tests
docker-compose run --rm git-server cargo test
docker-compose run --rm api go test ./...
docker-compose run --rm admin-ui npm test

# Integration tests
cd git-server && cargo test --test integration
cd api && go test ./tests/integration/...
cd admin-ui && npm run test:e2e
```

## Documentation

- **Full Specification**: [specs/001-git-backed-ecommerce/spec.md](specs/001-git-backed-ecommerce/spec.md)
- **Implementation Plan**: [specs/001-git-backed-ecommerce/plan.md](specs/001-git-backed-ecommerce/plan.md)
- **Quickstart Guide**: [specs/001-git-backed-ecommerce/quickstart.md](specs/001-git-backed-ecommerce/quickstart.md)
- **Data Model**: [specs/001-git-backed-ecommerce/data-model.md](specs/001-git-backed-ecommerce/data-model.md)
- **GraphQL Contracts**: [shared/schemas/](shared/schemas/)

## License

MIT
