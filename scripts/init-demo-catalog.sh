#!/bin/bash
#
# Demo Catalog Initialization Script
# Creates a sample product catalog with categories, collections, and products
#
# Usage: ./scripts/init-demo-catalog.sh [--data-dir <catalog-path>]
#
# Example: ./scripts/init-demo-catalog.sh --data-dir ./test-catalog

set -e

# Parse command-line arguments
CLI_DATA_DIR=""
while [ "$#" -gt 0 ]; do
  case "$1" in
    --data-dir)
      if [ -z "$2" ]; then
        echo "Error: --data-dir requires a value"
        exit 1
      fi
      CLI_DATA_DIR="$2"
      shift 2
      ;;
    *)
      echo "Error: unexpected argument '$1'"
      echo "Usage: $(basename "$0") [--data-dir <catalog-path>]"
      exit 1
      ;;
  esac
done

# Resolve catalog base directory with precedence:
# 1) GITSTORE_DATA_DIR env var
# 2) --data-dir flag
# 3) ./demo-catalog
CATALOG_PATH="${GITSTORE_DATA_DIR:-${CLI_DATA_DIR:-./demo-catalog}}"
# Resolve to absolute path before any cd so relative paths remain valid.
CATALOG_PATH="$(mkdir -p "$CATALOG_PATH" && cd "$CATALOG_PATH" && pwd)"
CATALOG_REPO_PATH="$CATALOG_PATH/catalog.git"

echo "Initializing demo catalog at: $CATALOG_REPO_PATH"

# Skip if the bare repo already exists.
if [ -f "$CATALOG_REPO_PATH/HEAD" ]; then
    echo "Bare repository already exists at $CATALOG_REPO_PATH — skipping init."
    exit 0
fi

# Build the catalog in a temporary working directory, then clone it bare.
# A bare repo is required so that git push from clients is accepted.
WORK_DIR="$(mktemp -d)"
trap 'rm -rf "$WORK_DIR"' EXIT

cd "$WORK_DIR"

# Initialize a normal (non-bare) repo as the staging area.
git init -b main
# Ensure a git identity is set for environments that have none (e.g. CI runners).
git config user.email "gitstore-init@localhost"
git config user.name "GitStore Init"

echo "# Demo Catalog" > README.md
echo "" >> README.md
echo "Sample product catalog for GitStore demonstration." >> README.md
git add README.md
git commit -m "Initial commit"

# Create directory structure
mkdir -p products categories collections

echo "Creating categories..."

# Category: Electronics
cat > categories/electronics.md << 'EOF'
---
id: cat_electronics_001
name: Electronics
slug: electronics
display_order: 0
created_at: 2026-01-01T00:00:00Z
updated_at: 2026-01-01T00:00:00Z
---

All your electronic needs - from computers to accessories.
EOF

# Category: Computers (child of Electronics)
cat > categories/computers.md << 'EOF'
---
id: cat_computers_001
name: Computers
slug: computers
parent_id: cat_electronics_001
display_order: 0
created_at: 2026-01-01T00:00:00Z
updated_at: 2026-01-01T00:00:00Z
---

Desktop computers, laptops, and workstations.
EOF

# Category: Accessories (child of Electronics)
cat > categories/accessories.md << 'EOF'
---
id: cat_accessories_001
name: Accessories
slug: accessories
parent_id: cat_electronics_001
display_order: 1
created_at: 2026-01-01T00:00:00Z
updated_at: 2026-01-01T00:00:00Z
---

Computer accessories and peripherals.
EOF

# Category: Books
cat > categories/books.md << 'EOF'
---
id: cat_books_001
name: Books
slug: books
display_order: 1
created_at: 2026-01-01T00:00:00Z
updated_at: 2026-01-01T00:00:00Z
---

Technical books and learning resources.
EOF

echo "Creating collections..."

# Collection: Featured Products
cat > collections/featured.md << 'EOF'
---
id: coll_featured_001
name: Featured Products
slug: featured
display_order: 0
product_ids: []
created_at: 2026-01-01T00:00:00Z
updated_at: 2026-01-01T00:00:00Z
---

Our hand-picked selection of the best products.
EOF

# Collection: New Arrivals
cat > collections/new-arrivals.md << 'EOF'
---
id: coll_new_001
name: New Arrivals
slug: new-arrivals
display_order: 1
product_ids: []
created_at: 2026-01-01T00:00:00Z
updated_at: 2026-01-01T00:00:00Z
---

Recently added products to our catalog.
EOF

# Collection: Best Sellers
cat > collections/bestsellers.md << 'EOF'
---
id: coll_bestsellers_001
name: Best Sellers
slug: bestsellers
display_order: 2
product_ids: []
created_at: 2026-01-01T00:00:00Z
updated_at: 2026-01-01T00:00:00Z
---

Our most popular products.
EOF

echo "Creating products..."

# Product 1: MacBook Pro
cat > products/prod_macbook_001.md << 'EOF'
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
  - https://example.com/images/macbook-pro-16-m3.jpg
  - https://example.com/images/macbook-pro-16-m3-side.jpg
metadata:
  brand: Apple
  processor: M3 Max
  ram: 36GB
  storage: 1TB SSD
  display: 16-inch Liquid Retina XDR
created_at: 2026-01-15T10:00:00Z
updated_at: 2026-01-15T10:00:00Z
---

# MacBook Pro 16" with M3 Max

The most powerful MacBook Pro ever. Supercharged by the M3 Max chip, it delivers exceptional performance for demanding workflows.

## Features

- **M3 Max chip** - Up to 40-core GPU for unbelievable graphics performance
- **36GB unified memory** - Handle massive projects with ease
- **1TB SSD storage** - Lightning-fast storage for all your files
- **16-inch Liquid Retina XDR display** - Extreme Dynamic Range and stunning color
- **Up to 22 hours battery life** - All-day power for professional workflows
- **Advanced camera and audio** - 1080p FaceTime HD camera and studio-quality mics

Perfect for developers, video editors, 3D artists, and creative professionals.
EOF

# Product 2: ThinkPad X1 Carbon
cat > products/prod_thinkpad_001.md << 'EOF'
---
id: prod_thinkpad_001
sku: TP-X1-C11-I7
title: ThinkPad X1 Carbon Gen 11
price: 1899.00
currency: USD
category_id: cat_computers_001
collection_ids:
  - coll_featured_001
  - coll_bestsellers_001
inventory_status: in_stock
inventory_quantity: 25
images:
  - https://example.com/images/thinkpad-x1-carbon-11.jpg
metadata:
  brand: Lenovo
  processor: Intel Core i7-1365U
  ram: 32GB
  storage: 1TB SSD
  display: 14-inch WUXGA
created_at: 2026-01-10T09:00:00Z
updated_at: 2026-01-10T09:00:00Z
---

# ThinkPad X1 Carbon Gen 11

The ultimate business ultrabook. Lightweight, durable, and packed with enterprise features.

## Specifications

- Intel Core i7-1365U processor (10 cores)
- 32GB LPDDR5 RAM
- 1TB PCIe Gen 4 SSD
- 14" WUXGA (1920x1200) anti-glare display
- Integrated Intel Iris Xe Graphics
- Up to 19.5 hours battery life
- MIL-STD-810H tested durability
- Fingerprint reader and IR camera

Ideal for business professionals and remote workers.
EOF

# Product 3: Magic Mouse
cat > products/prod_magicmouse_001.md << 'EOF'
---
id: prod_magicmouse_001
sku: APPLE-MM-BLK
title: Apple Magic Mouse - Black
price: 99.00
currency: USD
category_id: cat_accessories_001
collection_ids:
  - coll_new_001
inventory_status: in_stock
inventory_quantity: 50
images:
  - https://example.com/images/magic-mouse-black.jpg
metadata:
  brand: Apple
  color: Black
  connectivity: Bluetooth
  rechargeable: true
created_at: 2026-01-20T11:30:00Z
updated_at: 2026-01-20T11:30:00Z
---

# Magic Mouse - Black

Wireless, rechargeable, and incredibly intuitive. The Magic Mouse features a Multi-Touch surface that lets you perform gestures.

## Features

- Multi-Touch surface for gestures
- Wireless Bluetooth connectivity
- Rechargeable lithium-ion battery
- Lightning port for charging
- Optimized for ergonomics
- Compatible with Mac and iPad

Includes USB-C to Lightning cable.
EOF

# Product 4: Mechanical Keyboard
cat > products/prod_keyboard_001.md << 'EOF'
---
id: prod_keyboard_001
sku: MECH-KB-RGB-001
title: RGB Mechanical Gaming Keyboard
price: 149.99
currency: USD
category_id: cat_accessories_001
collection_ids:
  - coll_bestsellers_001
inventory_status: in_stock
inventory_quantity: 75
images:
  - https://example.com/images/mechanical-keyboard-rgb.jpg
metadata:
  brand: KeyMaster
  switch_type: Cherry MX Red
  backlight: RGB
  layout: Full Size
created_at: 2026-01-12T14:00:00Z
updated_at: 2026-01-12T14:00:00Z
---

# RGB Mechanical Gaming Keyboard

Premium mechanical keyboard with Cherry MX Red switches and per-key RGB lighting.

## Features

- Cherry MX Red mechanical switches
- Per-key RGB backlighting
- Aluminum frame construction
- N-key rollover
- Detachable USB-C cable
- Programmable macro keys
- Windows and Mac compatible

Perfect for gaming and productivity.
EOF

# Product 5: Programming Book
cat > products/prod_book_001.md << 'EOF'
---
id: prod_book_001
sku: BOOK-GO-PROG-2024
title: "Mastering Go: Advanced Patterns and Best Practices"
price: 59.99
currency: USD
category_id: cat_books_001
collection_ids:
  - coll_new_001
inventory_status: in_stock
inventory_quantity: 100
images:
  - https://example.com/images/mastering-go-cover.jpg
metadata:
  author: Jane Developer
  publisher: TechPress
  pages: 450
  isbn: 978-1234567890
  format: Paperback
  language: English
created_at: 2026-01-18T16:00:00Z
updated_at: 2026-01-18T16:00:00Z
---

# Mastering Go: Advanced Patterns and Best Practices

A comprehensive guide to advanced Go programming techniques.

## What You'll Learn

- Concurrency patterns and goroutine best practices
- Advanced error handling strategies
- Performance optimization techniques
- Building scalable microservices
- Testing and debugging complex applications
- Production-ready code patterns

Includes real-world examples and case studies from production systems.

**Target Audience**: Intermediate to advanced Go developers
EOF

# Product 6: USB-C Hub
cat > products/prod_hub_001.md << 'EOF'
---
id: prod_hub_001
sku: USBC-HUB-7IN1
title: 7-in-1 USB-C Hub
price: 79.99
currency: USD
category_id: cat_accessories_001
collection_ids:
  - coll_featured_001
  - coll_bestsellers_001
inventory_status: low_stock
inventory_quantity: 8
images:
  - https://example.com/images/usbc-hub-7in1.jpg
metadata:
  brand: HubTech
  ports: 7
  power_delivery: 100W
created_at: 2026-01-08T13:00:00Z
updated_at: 2026-02-01T09:00:00Z
---

# 7-in-1 USB-C Hub

Expand your laptop's connectivity with this versatile USB-C hub.

## Ports

- 1x USB-C PD (100W pass-through charging)
- 2x USB 3.0 (5Gbps data transfer)
- 1x HDMI (4K@60Hz)
- 1x SD card reader
- 1x microSD card reader
- 1x Gigabit Ethernet

Aluminum construction matches MacBook design. Plug-and-play, no drivers required.
EOF

# Product 7: Out of Stock Item
cat > products/prod_monitor_001.md << 'EOF'
---
id: prod_monitor_001
sku: MON-4K-32-001
title: 32" 4K Professional Monitor
price: 899.00
currency: USD
category_id: cat_accessories_001
inventory_status: out_of_stock
inventory_quantity: 0
images:
  - https://example.com/images/4k-monitor-32.jpg
metadata:
  brand: ViewPro
  resolution: 3840x2160
  refresh_rate: 60Hz
  panel_type: IPS
created_at: 2026-01-05T10:00:00Z
updated_at: 2026-02-10T14:30:00Z
---

# 32" 4K Professional Monitor

High-resolution display for creative professionals.

## Specifications

- 32-inch 4K (3840x2160) IPS panel
- 99% sRGB color gamut
- HDR10 support
- USB-C with 65W power delivery
- Multiple inputs: HDMI 2.0, DisplayPort 1.4, USB-C

**Note**: Currently out of stock. Expected restock date: March 2026
EOF

echo "Committing catalog to git..."

git add .
git commit -m "Add demo catalog with categories, collections, and products

- 4 categories (Electronics, Computers, Accessories, Books)
- 3 collections (Featured, New Arrivals, Best Sellers)
- 7 products with various price points and inventory statuses
- Demonstrates category hierarchy (Computers and Accessories under Electronics)
- Shows product-category-collection relationships"

# Clone the staging repo as a bare repository into the final destination.
# A bare repo is required: git push from external clients is rejected by
# a non-bare repo that has the branch currently checked out.
mkdir -p "$(dirname "$CATALOG_REPO_PATH")"
git clone --bare "$WORK_DIR" "$CATALOG_REPO_PATH"

echo ""
echo "✅ Demo catalog initialized successfully at: $CATALOG_REPO_PATH"
echo ""
echo "Catalog contents:"
echo "  - 4 categories (with 1 hierarchy: Electronics > Computers/Accessories)"
echo "  - 3 collections (Featured, New Arrivals, Best Sellers)"
echo "  - 7 products (laptops, accessories, books)"
echo ""
echo "Next steps to use with GitStore:"
echo ""
echo "  1. Start GitStore services:"
echo "       docker compose up --build -d"
echo ""
echo "  2. Clone catalog via HTTP (not filesystem!):"
echo "       git clone http://localhost:9418/catalog.git catalog-work"
echo "       cd catalog-work"
echo ""
echo "  3. Create and push a release tag:"
echo "       git tag -a v1.0.0 -m 'Initial catalog release'"
echo "       git push origin v1.0.0"
echo ""
echo "  4. Verify websocket notification:"
echo "       docker compose logs git-service | grep -i broadcast"
echo ""
echo "  5. Query via GraphQL playground:"
echo "       http://localhost:4000/playground"
echo ""
echo "⚠️  Important: Clone from http://localhost:9418/ (not from filesystem)"
echo "   This ensures git push triggers websocket notifications."
echo ""
echo "Bare repository location: $CATALOG_REPO_PATH"
