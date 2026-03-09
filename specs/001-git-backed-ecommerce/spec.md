# Feature Specification: GitStore - Git-Backed Ecommerce Engine

**Feature Branch**: `001-git-backed-ecommerce`
**Created**: 2026-03-09
**Status**: Draft
**Input**: User description: "Develop GitStore, a git-backed ecommerce headless engine. Users and AI agents can create product catalogues using markdown with front-matter. The markdown files can be pushed to the storefront via git commits and push. The following product catologues are supported: products, categories (category taxonomy), collections (to group products e.g. winter collection). Optionally non-technical users should be able to perform CRUD operations on product catalogues using an admin UI. The storefront shows data from the latest 'release' tag."

## Clarifications

### Session 2026-03-09

- Q: Product-category-collection relationship cardinality → A: A product can only belong to one category, but can belong to multiple collections
- Q: How should the system handle merge conflicts between admin UI commits and manual git edits? → A: Admin UI rejects push and requires manual conflict resolution (user must resolve conflicts via git, then admin UI refreshes)
- Q: How should the storefront behave when the latest release tag points to a commit with validation errors? → A: Storefront loads valid products and skips invalid ones, logging errors for review
- Q: How are orphaned references handled when categories or collections are deleted? → A: Products remain visible but orphaned references are marked as invalid/broken (products show without category/collection)
- Q: How should the admin UI handle concurrent editing when multiple users modify the same product? → A: Optimistic locking - warn user on save if product was modified since they started editing, show diff, let them choose to overwrite or merge
- Q: Git hosting architecture → A: GitStore is self-contained with built-in git engine (not third-party platforms like GitHub/GitLab). Built-in git provides validation on push and websocket endpoint for real-time notifications

## User Scenarios & Testing *(mandatory)*

### User Story 1 - Technical User Creates Product Catalog (Priority: P1)

A developer or AI agent creates a product catalog by writing markdown files with front-matter, committing them to a git repository, and pushing to make products available on the storefront.

**Why this priority**: This is the core value proposition - git-based catalog management. Without this, the system has no unique selling point. This story represents the MVP.

**Independent Test**: Can be fully tested by creating markdown files, committing them, pushing to a repository, creating a release tag, and verifying products appear on a storefront endpoint.

**Acceptance Scenarios**:

1. **Given** an empty git repository, **When** a user creates a product markdown file with front-matter (title, price, description), commits and pushes it to the built-in git engine, then creates a release tag, **Then** the product appears on the storefront
2. **Given** existing products in the repository, **When** a user modifies a product markdown file, commits, pushes, and creates a new release tag, **Then** the storefront reflects the updated product information
3. **Given** multiple product markdown files, **When** a user deletes a product file, commits, pushes, and creates a new release tag, **Then** the deleted product no longer appears on the storefront
4. **Given** an invalid markdown file with missing required front-matter fields, **When** the system processes the file, **Then** appropriate validation errors are provided

---

### User Story 2 - Organize Products with Categories and Collections (Priority: P2)

A catalog manager organizes products using category taxonomy (hierarchical) and collections (groupings like "Winter Collection" or "Best Sellers") via markdown files.

**Why this priority**: Product organization is essential for usability but depends on having products first. This adds merchandising capabilities.

**Independent Test**: Can be tested by creating category and collection markdown files, associating products with them, and verifying the relationships are reflected on the storefront.

**Acceptance Scenarios**:

1. **Given** a products catalog exists, **When** a user creates category markdown files with parent-child relationships, **Then** the storefront displays products organized in a hierarchical category structure
2. **Given** products exist, **When** a user creates a collection markdown file referencing specific product IDs, **Then** the storefront displays that collection with only the referenced products
3. **Given** a product is assigned to one category and multiple collections, **When** viewing the storefront, **Then** the product appears in its assigned category and all assigned collections
4. **Given** a category has subcategories, **When** viewing a parent category on the storefront, **Then** products from all subcategories are included

---

### User Story 3 - Non-Technical User Manages Catalog via Admin UI (Priority: P3)

A non-technical user (e.g., merchandiser, content editor) uses an admin interface to create, read, update, and delete products, categories, and collections without directly editing markdown files or using git commands.

**Why this priority**: This expands accessibility to non-technical users but the system delivers value without it. Technical users and AI agents can use the git interface.

**Independent Test**: Can be tested by logging into the admin UI, performing CRUD operations, and verifying that markdown files are generated/updated in the git repository and changes appear on the storefront after publishing.

**Acceptance Scenarios**:

1. **Given** access to the admin UI, **When** a user creates a new product through the interface, **Then** a corresponding markdown file is generated and committed to the repository
2. **Given** existing products in the admin UI, **When** a user edits product details, **Then** the corresponding markdown file is updated and committed
3. **Given** a product in the admin UI, **When** a user deletes it, **Then** the markdown file is removed and the deletion is committed
4. **Given** pending changes in the admin UI, **When** a user triggers a "publish" action, **Then** all changes are committed, pushed, and a new release tag is created
5. **Given** the admin UI, **When** a user creates categories with hierarchical relationships, **Then** category markdown files reflect the parent-child structure
6. **Given** the admin UI, **When** a user creates a collection and adds products to it, **Then** the collection markdown file references the selected products

---

### Edge Cases

- **Merge Conflicts**: When the git repository has merge conflicts between admin UI changes and direct git commits, the admin UI rejects the push operation and displays an error message: "Push failed: merge conflicts detected. Please resolve conflicts manually using git and refresh the admin UI." User must use git CLI to pull, resolve conflicts, commit, and push before the admin UI can proceed.
- **Malformed Markdown**: When markdown files contain invalid YAML front-matter syntax or malformed markdown, the system skips the file during catalog load, logs a detailed error (file path, line number, parse error), and continues processing other files. The file is not included in the catalog until fixed.
- **Invalid Front-Matter**: When required front-matter fields are missing or have invalid data types, the pre-push validation rejects the push with error message listing all validation failures per file.
- **Validation Errors in Release Tag**: When a release tag points to a commit with validation errors, the storefront loads all valid products and skips invalid ones, logging detailed error messages for each failed file to enable debugging.
- **Large Catalogs**: For catalogs exceeding 10,000 products, system must maintain sub-500ms query response time and complete catalog reload in under 3 seconds. Git repository size limit is 500MB; catalogs exceeding this require external image hosting (not storing images in git).
- **Non-existent References**: When a product references a non-existent category or collection ID, the reference is marked as "orphaned" in API responses with `{id: "cat_xyz", status: "not_found"}` and the product remains queryable without the missing relationship.
- **Deleted Categories/Collections**: When categories or collections are deleted, products with orphaned references remain visible on the storefront but their broken references are marked as invalid with status field, and products appear without the deleted category/collection assignment.
- **Concurrent Edits**: When multiple users edit the same product simultaneously via the admin UI, the system uses optimistic locking with version timestamps to detect conflicts on save, warns the user if the product was modified since editing began, shows a line-by-line diff of conflicting changes, and provides options: "Overwrite" (discard other user's changes), "Cancel" (abandon your changes), or "Merge Manually" (opens merge editor).
- **Binary Assets**: Product images are stored externally on CDN or cloud storage (S3, Cloudflare R2). Markdown front-matter contains only URLs. Maximum image URL length is 500 characters. System does not store binary data in git.
- **Manual Edits During Admin UI Use**: When markdown files are manually edited via git while admin UI sessions are active, the admin UI detects staleness on next save attempt (version mismatch), prompts user to refresh, and requires user to reload before making further changes. The manual git changes take precedence.

## Requirements *(mandatory)*

### Functional Requirements

- **FR-001**: System MUST parse markdown files with YAML front-matter to extract product catalog data
- **FR-002**: System MUST support product catalog entities: products, categories, and collections
- **FR-003**: System MUST read catalog data from the latest git release tag (not from HEAD or other branches)
- **FR-004**: System MUST validate that required front-matter fields are present for each entity type
- **FR-005**: System MUST support hierarchical category taxonomy with parent-child relationships
- **FR-006**: System MUST allow products to be assigned to exactly one category and multiple collections
- **FR-007**: System MUST expose catalog data through a headless interface (queryable endpoint) for storefronts
- **FR-008**: System MUST support product attributes including: title, description, price, SKU, inventory status
- **FR-009**: System MUST support category attributes including: name, description, parent category reference, display order
- **FR-010**: System MUST support collection attributes including: name, description, product references, display order
- **FR-011**: Admin UI MUST provide CRUD operations for products, categories, and collections
- **FR-012**: Admin UI MUST generate properly formatted markdown files with front-matter when users create/edit entities
- **FR-013**: Admin UI MUST commit changes to the git repository with descriptive commit messages
- **FR-014**: Admin UI MUST provide a "publish" mechanism to create release tags
- **FR-015**: System MUST handle incremental updates - only modified entities trigger repository updates
- **FR-016**: System MUST prevent invalid data from being committed (validation before commit)
- **FR-017**: System MUST provide meaningful error messages when markdown parsing or validation fails
- **FR-018**: System MUST support basic filtering on the storefront (by category, collection, price range, inventory status)
- **FR-019**: Admin UI MUST implement single admin user authentication with password-based login
- **FR-020**: System MUST support external storage for binary assets (product images hosted on CDN or cloud storage with URLs referenced in markdown front-matter)
- **FR-021**: Admin UI MUST reject push operations when merge conflicts are detected and display error message directing user to resolve conflicts manually via git
- **FR-022**: Storefront MUST load all valid products from a release tag and skip invalid files, logging detailed validation errors for each failed file
- **FR-023**: System MUST mark orphaned category/collection references as invalid when the referenced entity is deleted, allowing products to remain visible without the deleted assignment
- **FR-024**: Admin UI MUST implement optimistic locking for concurrent edits, detecting modifications made since editing began, displaying a diff of conflicting changes, and allowing users to choose whether to overwrite or manually merge
- **FR-025**: Built-in git engine MUST validate catalog data on push operations before accepting commits
- **FR-026**: Built-in git engine MUST provide websocket endpoint to broadcast real-time notifications when new release tags are created
- **FR-027**: Storefront MUST subscribe to git engine websocket and reload catalog data immediately upon receiving release tag notification

### Key Entities

- **Product**: Represents a sellable item with attributes like title, description, price, SKU, inventory status, images, and relationships to exactly one category and multiple collections. Each product is stored as a markdown file with front-matter.

- **Category**: Represents a hierarchical classification system for products. Contains name, description, optional parent category reference (for hierarchy), and display order. Categories can have subcategories forming a tree structure.

- **Collection**: Represents a curated grouping of products (e.g., "Winter Collection", "Best Sellers", "New Arrivals"). Contains name, description, list of product references, and display order. Unlike categories, collections are flat (non-hierarchical).

- **Release Tag**: A git tag that marks a specific commit as the published version of the catalog. The storefront always reads from the latest release tag.

- **Built-in Git Engine**: The internal git server component that manages catalog repositories, performs validation on push operations, and broadcasts real-time notifications via websocket when repository events occur (commits, tags, branch changes).

### Data Model & Validation Rules

#### Product Entity

**Required Front-Matter Fields**:
- `id` (string): Unique identifier with format `prod_[base62]` (e.g., "prod_abc123")
- `sku` (string): Stock keeping unit, alphanumeric with hyphens, max 50 chars, must be unique across catalog
- `title` (string): Product name, min 1 char, max 200 chars, cannot be empty or whitespace-only
- `price` (number): Decimal value, must be >= 0.00, precision to 2 decimal places
- `currency` (string): ISO 4217 3-letter code (e.g., "USD", "EUR", "GBP")
- `inventory_status` (enum): One of: "in_stock", "out_of_stock", "backorder", "discontinued"
- `category_id` (string): Reference to exactly one category ID (format: `cat_[base62]`)
- `created_at` (string): ISO 8601 datetime (e.g., "2026-03-09T10:00:00Z")
- `updated_at` (string): ISO 8601 datetime

**Optional Front-Matter Fields**:
- `inventory_quantity` (integer): Stock count, must be >= 0 if provided
- `collection_ids` (array of strings): References to collection IDs (format: `coll_[base62]`), can be empty array
- `images` (array of strings): Image URLs (HTTPS), max 10 images, each URL max 500 chars
- `metadata` (object): Key-value pairs for custom attributes (e.g., brand, weight_kg)

**Validation Rules**:
- SKU uniqueness enforced across entire catalog
- Product cannot exist without valid category_id reference
- Product with category_id referencing non-existent category will have orphaned reference marked
- Price must be non-negative
- Inventory quantity (if provided) must be non-negative integer
- Currency code must be valid ISO 4217 code (validation against known list)

**File Naming Convention**: `products/{category-slug}/{SKU}.md` (e.g., `products/electronics/LAPTOP-001.md`)

**Markdown Structure**:
```markdown
---
id: prod_abc123
sku: LAPTOP-001
title: Premium Laptop
price: 1299.99
currency: USD
inventory_status: in_stock
inventory_quantity: 50
category_id: cat_electronics
collection_ids:
  - coll_featured
  - coll_bestsellers
images:
  - https://cdn.example.com/laptop-001.jpg
metadata:
  brand: TechCorp
  weight_kg: 1.8
created_at: 2026-03-09T10:00:00Z
updated_at: 2026-03-09T10:00:00Z
---

# Premium Laptop

Product description in markdown format...
```

**Maximum File Size**: 1MB per product markdown file

#### Category Entity

**Required Front-Matter Fields**:
- `id` (string): Unique identifier with format `cat_[base62]` (e.g., "cat_electronics")
- `name` (string): Category name, min 1 char, max 100 chars
- `slug` (string): URL-friendly identifier, lowercase alphanumeric with hyphens, max 50 chars, must be unique
- `display_order` (integer): Sort order within parent (or root level), must be >= 0
- `created_at` (string): ISO 8601 datetime
- `updated_at` (string): ISO 8601 datetime

**Optional Front-Matter Fields**:
- `parent_id` (string): Reference to parent category ID (format: `cat_[base62]`), null/absent for root categories
- `description` (string): Category description, max 500 chars

**Validation Rules**:
- Slug uniqueness enforced across all categories
- Circular reference detection: category cannot reference itself or ancestor as parent
- Maximum hierarchy depth: 5 levels (root = level 0)
- Parent category must exist if parent_id is provided
- Display order must be non-negative integer

**File Naming Convention**: `categories/{slug}.md` (e.g., `categories/electronics.md`)

**Hierarchy Constraints**:
- Root categories have no parent_id
- Child categories reference parent via parent_id
- Deleting category with children requires either reassigning children or cascading delete (behavior specified in admin UI)

#### Collection Entity

**Required Front-Matter Fields**:
- `id` (string): Unique identifier with format `coll_[base62]` (e.g., "coll_featured")
- `name` (string): Collection name, min 1 char, max 100 chars
- `slug` (string): URL-friendly identifier, lowercase alphanumeric with hyphens, max 50 chars, must be unique
- `display_order` (integer): Sort order in collection listings, must be >= 0
- `product_ids` (array of strings): Product references (format: `prod_[base62]`), can be empty array
- `created_at` (string): ISO 8601 datetime
- `updated_at` (string): ISO 8601 datetime

**Optional Front-Matter Fields**:
- `description` (string): Collection description, max 500 chars

**Validation Rules**:
- Slug uniqueness enforced across all collections
- Product IDs in product_ids array are validated (must match format, but products may not exist - orphaned references allowed)
- Display order must be non-negative integer
- No hierarchy - collections are flat

**File Naming Convention**: `collections/{slug}.md` (e.g., `collections/winter-2026.md`)

#### Release Tag Format

**Tag Naming Convention**: Must use one of:
- Semantic versioning: `v{MAJOR}.{MINOR}.{PATCH}` (e.g., "v1.0.0", "v2.3.1")
- Date-based: `YYYY-MM-DD` (e.g., "2026-03-09")

**Latest Tag Selection**:
- For semantic versioning: Use highest version according to semver rules
- For date-based: Use most recent date
- Mixed tags in repository: Semantic version tags take precedence

**Tag Type**: Annotated tags (created with `git tag -a`) required for release tags

### Validation Error Messages

When validation fails, error messages MUST include:
- **File path**: Relative path from repository root
- **Error code**: Machine-readable code (e.g., "MISSING_REQUIRED_FIELD", "INVALID_FORMAT")
- **Field name**: Which field failed validation
- **Error description**: Human-readable explanation
- **Line number**: For YAML parsing errors, include line number in front-matter

**Example Error Format**:
```json
{
  "file": "products/electronics/LAPTOP-001.md",
  "line": 5,
  "code": "MISSING_REQUIRED_FIELD",
  "field": "price",
  "message": "Required field 'price' is missing in front-matter"
}
```

**Validation Rejection Behavior**:
- Pre-push validation fails entire push if ANY file has validation errors
- All validation errors across all files are collected and returned to user
- No partial acceptance of push (atomic operation)

### Git Engine Specifications

#### Git Protocol & Authentication

**Git Protocol**: Built-in git engine supports standard git protocol on port 9418
**Push Authentication**: Basic authentication with username/password (admin user credentials)
**Repository URL Format**: `git://localhost:9418/catalog.git` (development) or `git://{host}:9418/catalog.git` (production)

**Repository Initialization**:
- Automatic initialization on first access
- Bare repository structure
- Default branch: `main`
- Pre-receive hooks automatically installed for validation

#### Pre-Push Validation

**Validation Checks** (executed on push before accepting):
1. **Syntax Validation**: YAML front-matter parses correctly
2. **Required Fields**: All required fields present for entity type
3. **Format Validation**: Field values match expected format/type
4. **Uniqueness**: SKU uniqueness (products), slug uniqueness (categories/collections)
5. **Reference Integrity**: Category/collection IDs follow format (existence check is soft - allows orphans)
6. **Constraint Validation**: Price >= 0, inventory >= 0, currency code valid, etc.
7. **Hierarchy Validation**: Category circular reference detection, max depth check

**Validation Performance Target**: < 5 seconds for push containing 100 files

**Validation Response**:
- **Success**: Push accepted, returns commit SHA and success message
- **Failure**: Push rejected, returns JSON array of all validation errors across all files

**Partial Failure Handling**: Entire push rejected if ANY file fails validation (atomic operation)

#### Websocket Notifications

**Websocket Endpoint**: `ws://localhost:8080/notifications` (development) or `wss://{host}:8080/notifications` (production with TLS)

**Connection Lifecycle**:
1. Client connects to websocket endpoint
2. No authentication required for read-only notifications (optional: can add token-based auth)
3. Connection remains open indefinitely
4. Server sends ping every 30 seconds; client must respond with pong
5. On disconnect, client should reconnect with exponential backoff

**Message Format** (JSON):
```json
{
  "event": "release_tag_created",
  "tag": "v1.0.0",
  "commit_sha": "abc123def456",
  "timestamp": "2026-03-09T10:00:00Z"
}
```

**Event Types**:
- `release_tag_created`: New release tag pushed
- `release_tag_deleted`: Release tag removed (for rollback scenarios)

**Notification Payload**:
- `event`: Event type string
- `tag`: Tag name
- `commit_sha`: Commit SHA that tag points to
- `timestamp`: ISO 8601 datetime of event

**Delivery Guarantees**: At-least-once delivery (clients may receive duplicate notifications)

**Missed Notifications**: Clients reconnecting after disconnect should query current latest tag via API (no replay of missed notifications)

**Notification Delivery Target**: < 100ms from tag creation to websocket broadcast

**Websocket Connection Limits**: Up to 100 concurrent connections (configurable)

### Admin UI Specifications

#### Authentication & Session Management

**Authentication Method**: Password-based login for single admin user

**Password Requirements**:
- Minimum length: 12 characters
- Must contain: uppercase, lowercase, number, special character
- Password stored as bcrypt hash (cost factor 12)

**Session Management**:
- JWT-based session tokens
- Token expiry: 24 hours from issue
- Refresh token mechanism: Tokens auto-refresh on activity (sliding window)
- Concurrent sessions: Only 1 active session per admin user (new login invalidates old session)

**Session Timeout**: 24 hours of inactivity logs user out

#### Commit Message Format

**Generated Commit Messages** (from admin UI):
- Format: `{action}: {entity_type} {entity_identifier} - {change_summary}`
- Examples:
  - "create: product LAPTOP-001 - Premium Laptop"
  - "update: category electronics - Changed display order to 5"
  - "delete: collection winter-2026"
  - "publish: Release v1.2.0 with 15 product updates"

**Commit Author**: Admin UI commits use configured git author (e.g., "Admin UI <admin@gitstore.local>")

#### Publish Workflow

**Publish Steps**:
1. User clicks "Publish" button
2. Admin UI validates all pending changes locally
3. Admin UI commits all changes to local git (one commit per entity changed)
4. Admin UI prompts user for release tag name (auto-suggests next version)
5. User confirms tag name and optional release notes
6. Admin UI pushes all commits to git server
7. If push succeeds, admin UI creates annotated release tag
8. Admin UI pushes tag to git server
9. Git server broadcasts websocket notification
10. Admin UI shows success message with tag name

**Rollback on Failure**:
- If push fails (validation error), admin UI does NOT create tag
- User must fix validation errors and retry publish
- No automatic git reset - user can manually discard changes or fix and retry

**Publish Confirmation Dialog**:
- Shows list of changes to be published (files modified, added, deleted)
- Requires user confirmation before proceeding
- Allows user to enter custom release notes (stored in annotated tag message)

#### Concurrent Edit Detection

**Optimistic Locking Mechanism**: Version-based (using `updated_at` timestamp)

**Edit Session Flow**:
1. User opens entity for editing → Admin UI captures current `updated_at` timestamp
2. User makes changes in form
3. User clicks "Save"
4. Admin UI sends save request with original `updated_at` value
5. Backend checks if entity's current `updated_at` matches original
6. If mismatch detected → Conflict!

**Conflict Resolution UI**:
- **Detection**: Backend returns 409 Conflict with both versions
- **Diff Display**: Side-by-side or inline diff showing:
  - Original version (when user started editing)
  - Current version (other user's changes)
  - User's pending changes
- **User Options**:
  - "Use My Changes" → Overwrites other user's changes (updates `updated_at` to now)
  - "Discard My Changes" → Abandons user's edits, reloads current version
  - "Merge Manually" → Opens three-way merge editor (if time allows; otherwise show both versions and require user to choose)

**Conflict Message Example**: "This product was modified by another user at 2026-03-09 10:15:00. Review the differences below and choose how to proceed."

**Lock Timeout**: Edit sessions have no hard timeout, but conflict detection on save prevents overwrite

**Abandoned Sessions**: No automatic cleanup; conflicts detected on next user's save attempt

#### Admin UI State During Conflicts

**Git Merge Conflict State**:
- When admin UI detects push failure due to merge conflict:
  - Display modal dialog: "Unable to publish: Git merge conflict detected"
  - Provide instructions: "Resolve conflicts using git CLI, then refresh this page"
  - Disable "Publish" button until page refresh
  - Admin UI enters read-only mode for affected entities

**Refresh Mechanism**:
- User must pull latest changes via git CLI
- User resolves conflicts manually
- User commits resolution
- User refreshes admin UI (reload page or click "Refresh" button)
- Admin UI reloads catalog from git and exits read-only mode

### Storefront API & Caching

#### Catalog Reload Behavior

**Trigger**: Websocket notification received with `release_tag_created` event

**Reload Process**:
1. Storefront API receives websocket notification
2. API fetches commit SHA from tag reference
3. API checks out tag in git repository
4. API parses all markdown files in catalog
5. API validates files (skip invalid files, log errors)
6. API builds in-memory catalog structure (products, categories, collections)
7. API atomically swaps old catalog with new catalog
8. API is immediately available with new data

**Reload Target Time**: Complete process in < 30 seconds from tag creation to new data serving

**Downtime During Reload**: Zero downtime - old catalog serves requests until new catalog ready, then atomic swap

**Stale Data Window**: Maximum 30 seconds between tag creation and storefront reflecting changes (per SC-002)

**Failed Reload Handling**:
- If new catalog has validation errors, skip invalid files and load valid ones
- Log all validation failures with file paths and error details
- Storefront continues serving previous valid catalog if new catalog is completely invalid

#### Caching Strategy

**Cache Type**: In-memory cache (no external cache dependencies like Redis)

**Cache Structure**:
- Products indexed by: ID, SKU
- Categories indexed by: ID, slug
- Collections indexed by: ID, slug
- Category hierarchy tree pre-built in memory
- Collection-to-product mappings pre-built

**Cache Invalidation**:
- Full cache clear and reload on websocket notification
- No partial cache updates (atomic replacement)
- TTL-based expiration: None (event-driven invalidation only)

**Cache Size Limits**:
- Target: Support 10,000 products in memory
- Estimated memory usage: ~100MB for 10,000 products with metadata
- No LRU eviction - full catalog always in memory

**Cold Start**: On API startup, fetch latest release tag and load catalog before accepting requests

#### Orphaned Reference Representation

**API Response Format** (when product has orphaned category reference):
```json
{
  "id": "prod_abc123",
  "sku": "LAPTOP-001",
  "title": "Premium Laptop",
  "category": {
    "id": "cat_electronics",
    "status": "not_found"
  },
  "collections": [
    {
      "id": "coll_featured",
      "status": "not_found"
    }
  ]
}
```

**Status Field Values**:
- `"ok"`: Reference is valid, entity exists
- `"not_found"`: Referenced entity does not exist in current catalog

**Filtering Behavior**:
- Products with orphaned category references ARE included in "all products" queries
- Products with orphaned category references are NOT included in "products by category" queries
- Collections with orphaned product references skip the missing products (do not fail entire query)

#### Query Performance

**Performance Targets** (per SC-007):
- Query latency: < 500ms for catalogs with 1,000+ products
- Pagination: Relay-style cursor pagination (no offset-based pagination for large datasets)
- Filtering: Indexed fields (category_id, collection_ids, inventory_status, price) have O(1) or O(log n) lookup

**Filtering Fields** (per FR-018):
- `category`: Filter by category ID (includes subcategory products)
- `collection`: Filter by collection ID
- `priceMin` / `priceMax`: Price range filter
- `inventoryStatus`: Filter by status enum
- Multiple filters combined with AND logic

**Rapid Tag Creation Handling**:
- If multiple release tags created in quick succession, process only the latest
- Ignore intermediate tags to avoid unnecessary reloads
- Debounce mechanism: If notification received during active reload, queue latest tag for next reload

### Rollback Procedure

**Rollback Trigger**: Revert to previous catalog version

**Rollback Steps**:
1. Identify previous release tag in git history
2. Create new release tag pointing to previous commit (e.g., `v1.2.1` pointing to commit of `v1.2.0`)
3. Push new tag to git server
4. Websocket notification triggers storefront reload
5. Storefront loads catalog from previous commit

**Rollback Requirements**:
- Full git history retained (no force-push or history rewrite)
- Any release tag in history can be restored by creating new tag pointing to its commit
- Rollback is a forward operation (new tag), not destructive operation

**Rollback Naming Convention**: Use next semantic version (e.g., rollback from v1.2.0 to v1.1.0 creates v1.2.1 pointing to v1.1.0 commit)

## Success Criteria *(mandatory)*

### Measurable Outcomes

- **SC-001**: Technical users can create and publish a complete product catalog (100+ products with categories and collections) in under 30 minutes using git workflow
- **SC-002**: Storefront updates to reflect catalog changes within 30 seconds of a new release tag being created
- **SC-003**: Non-technical users can successfully create, edit, and publish products through the admin UI without any git knowledge in 90% of attempts
- **SC-004**: System successfully parses and validates 99.9% of properly formatted markdown files with front-matter
- **SC-005**: Catalog changes are version-controlled with full git history, enabling rollback to any previous release
- **SC-006**: AI agents can programmatically generate and commit catalog files that are accepted by the system in 95% of attempts
- **SC-007**: Storefront can serve catalog queries for 1,000+ products with response times under 500ms

## Assumptions

1. **Git Repository Architecture**: GitStore includes a built-in git engine that manages catalog repositories internally, providing validation on push and real-time websocket notifications (does not rely on external platforms like GitHub or GitLab)
2. **Markdown Format**: Using standard Markdown syntax with YAML front-matter (Jekyll/Hugo style)
3. **Release Tag Convention**: Using semantic versioning or date-based tags for releases (e.g., v1.0.0 or 2026-03-09)
4. **Single Source of Truth**: The git repository is the single source of truth; admin UI changes are persisted by committing to git
5. **Concurrent Editing**: Assuming relatively low concurrency - if concurrent editing is critical, this needs to be addressed in planning
6. **Image Hosting**: Product images and binary assets are likely stored externally (CDN, cloud storage) with URLs in markdown front-matter, unless clarified otherwise
7. **Storefront Implementation**: This specification covers the headless backend engine; actual storefront UI implementation is separate
8. **Admin UI Deployment**: Admin UI is a web application that requires authentication (details to be clarified)
9. **Data Volume**: Initial target is catalogs with up to 10,000 products; larger catalogs may require performance optimizations
10. **API Format**: Headless API will follow RESTful conventions or GraphQL (to be determined during planning)

## Out of Scope

The following are explicitly outside the scope of this feature:

- Payment processing and checkout functionality
- Shopping cart management
- Order management system
- Customer accounts and authentication
- Inventory management beyond status tracking
- Shipping and fulfillment
- Multi-currency and internationalization (i18n)
- Product reviews and ratings
- Advanced SEO features
- Analytics and reporting dashboards
- Email notifications
- Multi-tenant support
- Custom product attributes beyond standard fields (may be added in future iterations)
