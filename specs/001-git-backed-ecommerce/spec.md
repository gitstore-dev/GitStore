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

- When the git repository has merge conflicts between admin UI changes and direct git commits, the admin UI rejects the push operation and displays an error requiring manual conflict resolution via git tools
- How does the system handle malformed markdown or invalid front-matter syntax?
- When a release tag points to a commit with validation errors, the storefront loads all valid products and skips invalid ones, logging detailed error messages for each failed file to enable debugging
- How does the system handle large catalogs (10,000+ products) in terms of git repository size and storefront performance?
- What happens when a product references a non-existent category or collection?
- When categories or collections are deleted, products with orphaned references remain visible on the storefront but their broken references are marked as invalid, and products appear without the deleted category/collection assignment
- When multiple users edit the same product simultaneously via the admin UI, the system uses optimistic locking to detect conflicts on save, warns the user if the product was modified since editing began, shows a diff of changes, and allows the user to choose whether to overwrite or merge changes
- How does the system handle binary assets (product images) - are they stored in git or externally?
- What happens when someone manually edits markdown files while the admin UI is being used?

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
