# GraphQL Backend Implementation Specification

**Feature ID**: 004-graphql-backend
**Status**: Draft
**Created**: 2026-03-12
**Branch**: 004-graphql-resolvers

## Overview

Enable E2E tests for the GitStore Admin UI by implementing the GraphQL backend resolvers and authentication system. The backend infrastructure (GraphQL schemas, resolver stubs, session management, HTTP server) is already in place but needs business logic implementation.

## Context

- **Current State**: E2E tests are complete and ready (PR #4 merged), but all fail because the GraphQL backend returns "not implemented" panics
- **Existing Infrastructure**: GraphQL schemas, gqlgen generated code, resolver stubs, session management (JWT), login handler, cache manager
- **Blocker**: Authentication endpoint rejects admin credentials, all CRUD resolver methods panic
- **Success Metric**: E2E test suite (30 tests across 11 scenarios) passes without failures

## User Scenarios

### Scenario 1: Admin Authentication
As an administrator, I need to log in to the Admin UI so that I can manage the product catalog securely.

**Flow**:
1. Admin opens Admin UI and navigates to login page
2. Admin enters username "admin" and password "admin"
3. System validates credentials and generates JWT token
4. System returns token with 24-hour expiry
5. Admin is redirected to products page

**Acceptance Criteria**:
- Login succeeds with correct credentials (admin/admin)
- Login fails with incorrect credentials (returns 401)
- JWT token includes username and isAdmin flag
- Token expires after 24 hours
- Failed login attempts log security events

### Scenario 2: Product Management
As an administrator, I need to create, edit, and delete products so that I can maintain the catalog.

**Flow**:
1. Admin creates new product with title, SKU, price, description
2. System validates required fields
3. System commits product to git repository
4. Admin searches for product by title
5. Admin edits product details
6. System detects version conflicts (optimistic locking)
7. Admin deletes product
8. System removes product from git

**Acceptance Criteria**:
- Product creation validates required fields (title, SKU, price)
- Product data persists in git repository
- Search returns matching products
- Concurrent edits detected via version field
- Delete removes product permanently
- All mutations include proper git commits

### Scenario 3: Category Organization
As an administrator, I need to organize products into categories and reorder them so that customers can browse effectively.

**Flow**:
1. Admin creates parent category "Electronics"
2. Admin creates child category "Laptops" under Electronics
3. Admin drags category to new position
4. System updates display order for all affected categories
5. Admin reloads page and sees preserved order

**Acceptance Criteria**:
- Categories support hierarchical structure (parent/child)
- Drag-and-drop updates display order
- Order persists after page reload
- Reordering commits to git
- Visual feedback during drag operations

## Functional Requirements

### FR1: Authentication System
**Priority**: Critical
**Description**: Implement working authentication with configurable credentials

- System accepts username/password via Basic Auth at `/api/login`
- System validates credentials against configured admin user (default: admin/admin)
- System generates JWT token with 24-hour expiry
- System includes username and isAdmin flag in token claims
- System returns token with expiry timestamp in ISO8601 format
- System rejects invalid credentials with 401 status
- System logs successful and failed authentication attempts

**Acceptance Criteria**:
- POST to `/api/login` with Basic Auth header "admin:admin" returns 200 with valid JWT token
- Invalid credentials return 401 with error message
- Token payload includes: `{token: string, expiresAt: string, username: string, isAdmin: boolean}`
- Token can be validated and decoded successfully
- Failed login attempts appear in server logs

### FR2: Product CRUD Operations
**Priority**: Critical
**Description**: Implement GraphQL mutations for product management

**Create Product**:
- Accepts: title, SKU, price, description, currency, inventoryStatus, inventoryQuantity, categoryId, collectionIds, images, metadata
- Validates: required fields (title, SKU, price must be present and non-empty)
- Returns: created product with generated ID and version field
- Commits: changes to git with message "Create product: {title}"

**Update Product**:
- Accepts: product ID, optional fields, version for optimistic locking
- Validates: version matches current version (detects concurrent edits)
- Returns: updated product or conflict object if version mismatch
- Commits: changes to git with message "Update product: {title}"

**Delete Product**:
- Accepts: product ID
- Validates: product exists
- Returns: deleted product ID
- Commits: deletion to git with message "Delete product: {title}"

**Query Products**:
- Supports pagination (first/after, last/before cursors)
- Supports filtering by title, SKU, category
- Returns: product connection with edges and page info

**Acceptance Criteria**:
- GraphQL mutation `createProduct` succeeds with valid input
- GraphQL mutation `updateProduct` detects version conflicts
- GraphQL mutation `deleteProduct` removes product permanently
- GraphQL query `products` returns paginated results
- All mutations commit to git repository
- Validation errors return clear messages

### FR3: Category CRUD Operations
**Priority**: Critical
**Description**: Implement GraphQL mutations for category management

**Create Category**:
- Accepts: name, slug, description, parentId, displayOrder, metadata
- Validates: unique slug, valid parentId if specified
- Returns: created category with generated ID and version
- Commits: changes to git with message "Create category: {name}"

**Update Category**:
- Accepts: category ID, optional fields, version
- Validates: version matches current version
- Returns: updated category or conflict object
- Commits: changes to git with message "Update category: {name}"

**Delete Category**:
- Accepts: category ID
- Validates: category has no children, no products assigned
- Returns: deleted category ID
- Commits: deletion to git with message "Delete category: {name}"

**Reorder Categories**:
- Accepts: array of category IDs in new order
- Updates: displayOrder field for each category
- Returns: updated categories with new displayOrder values
- Commits: changes to git with message "Reorder categories"

**Query Categories**:
- Returns: all categories in hierarchical structure
- Includes: parent/child relationships
- Sorted by: displayOrder within each level

**Acceptance Criteria**:
- GraphQL mutation `createCategory` creates category successfully
- GraphQL mutation `reorderCategories` updates display order
- Order persists after page reload
- Hierarchical structure maintains parent/child relationships
- Cannot delete category with children or assigned products

### FR4: Collection CRUD Operations
**Priority**: High
**Description**: Implement GraphQL mutations for collection management

**Create Collection**:
- Accepts: name, slug, description, productIds, displayOrder, metadata
- Validates: unique slug, valid product IDs
- Returns: created collection with generated ID
- Commits: changes to git with message "Create collection: {name}"

**Update Collection**:
- Accepts: collection ID, optional fields, version
- Validates: version matches current version
- Returns: updated collection or conflict object
- Commits: changes to git with message "Update collection: {name}"

**Delete Collection**:
- Accepts: collection ID
- Returns: deleted collection ID
- Commits: deletion to git with message "Delete collection: {name}"

**Reorder Collections**:
- Accepts: array of collection IDs in new order
- Updates: displayOrder field
- Returns: updated collections
- Commits: changes to git with message "Reorder collections"

**Query Collections**:
- Returns: all collections sorted by displayOrder
- Includes: product IDs for each collection

**Acceptance Criteria**:
- GraphQL mutation `createCollection` succeeds
- Collections can be reordered via drag-and-drop
- Product IDs validate against existing products
- Collection deletion does not affect products

### FR5: Catalog Publishing
**Priority**: High
**Description**: Implement catalog publishing to create git tags

**Publish Catalog**:
- Accepts: tag name, commit message
- Validates: no uncommitted changes
- Creates: git tag on current commit
- Pushes: tag to remote repository
- Returns: tag name, commit hash, timestamp

**Acceptance Criteria**:
- GraphQL mutation `publishCatalog` creates git tag
- Tag is pushed to remote repository
- Returns confirmation with tag details
- Fails gracefully if uncommitted changes exist

## Non-Functional Requirements

### NFR1: Performance
- GraphQL queries respond within 200ms for typical catalog size (1000 products)
- Authentication completes within 100ms
- Mutations commit to git within 500ms

### NFR2: Security
- JWT tokens use HS256 signing algorithm
- JWT secret loaded from environment variable (JWT_SECRET)
- Failed login attempts logged for security monitoring
- No sensitive data in GraphQL error messages

### NFR3: Data Integrity
- Optimistic locking prevents concurrent edit conflicts
- All mutations wrapped in git commits (atomic operations)
- Version field increments on each update
- Git repository maintains full audit trail

### NFR4: Error Handling
- Validation errors return clear, actionable messages
- GraphQL errors include field-level details
- Server errors logged with full context
- Client receives user-friendly error messages

## Success Criteria

1. **Authentication Success Rate**: 100% of valid login attempts succeed within 100ms
2. **E2E Test Pass Rate**: All 30 E2E tests (11 scenarios) pass without failures
3. **CRUD Operation Success**: 100% of valid CRUD operations complete successfully
4. **Git Commit Integrity**: 100% of mutations result in valid git commits
5. **Concurrent Edit Detection**: 100% of version conflicts detected and reported
6. **Error Message Clarity**: Users can resolve validation errors without developer assistance
7. **Response Time**: 95% of GraphQL requests complete within 200ms

## Key Entities

### Product
- ID (string, generated)
- SKU (string, unique, required)
- Title (string, required)
- Description (string, optional)
- Price (decimal, required)
- Currency (string, default: "USD")
- InventoryStatus (enum: IN_STOCK, OUT_OF_STOCK, BACKORDER)
- InventoryQuantity (integer)
- CategoryID (string, foreign key)
- CollectionIDs (array of strings)
- Images (array of URLs)
- Metadata (JSON object)
- Version (string, for optimistic locking)
- CreatedAt (timestamp)
- UpdatedAt (timestamp)

### Category
- ID (string, generated)
- Name (string, required)
- Slug (string, unique, required)
- Description (string, optional)
- ParentID (string, optional, foreign key)
- DisplayOrder (integer)
- Metadata (JSON object)
- Version (string)
- CreatedAt (timestamp)
- UpdatedAt (timestamp)

### Collection
- ID (string, generated)
- Name (string, required)
- Slug (string, unique, required)
- Description (string, optional)
- ProductIDs (array of strings)
- DisplayOrder (integer)
- Metadata (JSON object)
- Version (string)
- CreatedAt (timestamp)
- UpdatedAt (timestamp)

### User (Authentication)
- ID (string)
- Username (string, unique)
- IsAdmin (boolean)

## Assumptions

1. **Single Admin User**: Only one admin user exists with hardcoded credentials (admin/admin) for MVP
2. **Git Repository**: Git repository is initialized and accessible at configured path
3. **File System Access**: Server has read/write access to git repository directory
4. **Network**: Server can push to remote git repository (if configured)
5. **Concurrency**: Single-server deployment (no distributed locking needed)
6. **Data Format**: Products, categories, collections stored as JSON/YAML files in git
7. **Schema Stability**: GraphQL schema files already exist and are correct
8. **Type Mapping**: Generated model types (internal/graph/model) will be used, custom models (internal/models) will be adapted or replaced

## Dependencies

### Internal
- GraphQL schema files (shared/schemas/*.graphql)
- Generated gqlgen code (internal/graph/generated)
- Session management (internal/auth/session.go)
- Git client (internal/gitclient)
- Cache manager (internal/cache)
- Existing resolver stubs

### External
- Go 1.21+
- gqlgen library
- JWT library (github.com/golang-jwt/jwt/v5)
- Git CLI or libgit2 bindings
- E2E test suite (admin-ui/tests/e2e)

## Out of Scope

- Multi-user authentication and authorization
- User management (create/update/delete users)
- Role-based access control (RBAC)
- Product image upload/storage
- Email notifications
- Audit log UI
- Advanced search with full-text indexing
- Real-time collaboration features
- GraphQL subscriptions
- Multi-language support
- Payment integration
- Inventory management beyond simple quantity tracking

## Edge Cases

### Authentication Edge Cases
- Empty username or password → Return 401 with "Missing credentials"
- Malformed Basic Auth header → Return 401 with "Invalid authorization header"
- Expired JWT token used in request → Return 401 with "Token expired"
- Token with invalid signature → Return 401 with "Invalid token"

### Product CRUD Edge Cases
- Create product with duplicate SKU → Return validation error "SKU already exists"
- Update product with stale version → Return conflict object with current version and diff
- Delete product that doesn't exist → Return error "Product not found"
- Create product with invalid category ID → Return validation error "Category not found"
- Search with empty query → Return all products (paginated)

### Category Edge Cases
- Create category with duplicate slug → Return validation error "Slug already exists"
- Delete category with children → Return error "Cannot delete category with children"
- Delete category with assigned products → Return error "Cannot delete category with products"
- Reorder categories with invalid ID → Return error "Category not found"
- Create child category with non-existent parent → Return error "Parent category not found"
- Circular parent-child relationship → Prevented by single-level assignment (no recursive nesting)

### Collection Edge Cases
- Create collection with non-existent product IDs → Return validation error listing invalid IDs
- Update collection to remove all products → Allowed (empty collection valid)
- Reorder collections with duplicate IDs → Return error "Duplicate category IDs"

### Git Operation Edge Cases
- Git repository not initialized → Server logs error, mutations fail gracefully
- Git working directory has uncommitted changes → Mutations append to existing changes
- Git push fails (network error) → Mutation succeeds locally, logs push failure
- Concurrent git operations → Serialized via mutex lock

## Testing Strategy

### Unit Tests
- Resolver method tests with mock cache/git clients
- Authentication middleware tests
- Validation logic tests
- Optimistic locking tests

### Integration Tests
- GraphQL query/mutation tests against test database
- Git commit verification tests
- JWT token generation and validation tests

### E2E Tests
- Existing E2E test suite (30 tests, 11 scenarios)
- Product CRUD workflow (5 scenarios)
- Category drag-and-drop (6 scenarios)
- Authentication flow
- Validation error handling
- Concurrent edit conflicts

### Performance Tests
- Load test with 1000 products
- Concurrent mutation tests
- Query response time under load

## Implementation Notes

### Current Status (2026-03-12)
- ✅ GraphQL server enabled in main.go
- ✅ `/api/login` endpoint wired up
- ✅ Server compiles and runs
- ❌ Authentication rejects admin credentials (needs config)
- ❌ All resolver methods panic with "not implemented"
- ❌ Type system mismatch between internal/models and internal/graph/model

### Critical Path
1. Fix authentication credential validation
2. Implement product resolvers (create, update, delete, query)
3. Implement category resolvers (create, update, delete, reorder, query)
4. Run E2E tests and fix failures iteratively
5. Implement collection resolvers
6. Implement catalog publishing
7. Address type system issues (models vs generated types)

### Technical Challenges
- **Type System**: Two model packages exist (internal/models and internal/graph/model), need to decide which to use or create adapters
- **Git Integration**: Existing gitclient may need enhancement for CRUD operations
- **Optimistic Locking**: Version field management needs careful implementation
- **Error Handling**: GraphQL error responses need user-friendly formatting

## Related Documents

- E2E Test Status: `admin-ui/tests/e2e/STATUS.md`
- E2E Test Specs: `admin-ui/tests/e2e/product_crud.spec.ts`, `admin-ui/tests/e2e/category_reorder.spec.ts`
- GraphQL Schemas: `shared/schemas/*.graphql`
- Phase 5 Progress: `PHASE5_PROGRESS.md`
- Original Task Specification: `specs/001-git-backed-ecommerce/tasks.md`
