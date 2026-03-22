# Tasks: GitStore - Git-Backed Ecommerce Engine

**Input**: Design documents from `/specs/001-git-backed-ecommerce/`
**Prerequisites**: plan.md (required), spec.md (required for user stories), research.md, data-model.md, contracts/

**Tests**: Test-First Development (Constitution Principle I - NON-NEGOTIABLE). Tests MUST be written before implementation.

**Organization**: Tasks are grouped by user story to enable independent implementation and testing of each story.

## Format: `[ID] [P?] [Story?] Description`

- **[P]**: Can run in parallel (different files, no dependencies)
- **[Story]**: Which user story this task belongs to (e.g., US1, US2, US3)
- Include exact file paths in descriptions

## Path Conventions

Multi-service architecture:
- **Git Server**: `git-server/src/`, `git-server/tests/`
- **API**: `api/internal/`, `api/cmd/`, `api/tests/`
- **Admin UI**: `admin-ui/src/`, `admin-ui/tests/`
- **Shared**: `shared/schemas/`
- **Docker**: `docker/`, `compose.yml`

---

## Phase 1: Setup (Shared Infrastructure)

**Purpose**: Project initialization and basic structure

- [X] T001 Create root project structure with git-server/, api/, admin-ui/, shared/, docker/ directories
- [X] T002 [P] Initialize Rust project in git-server/ with Cargo.toml (dependencies: libgit2, tokio, tungstenite, serde, serde_yaml)
- [X] T003 [P] Initialize Go module in api/ with go.mod (dependencies: gqlgen, graphql-relay-go, go-git)
- [X] T004 [P] Initialize Node.js project in admin-ui/ with package.json (dependencies: astro, react, react-beautiful-dnd, urql)
- [X] T005 [P] Copy GraphQL schema files from specs/001-git-backed-ecommerce/contracts/ to shared/schemas/
- [X] T006 [P] Create docker/git-server.Dockerfile for Rust multi-stage build
- [X] T007 [P] Create docker/api.Dockerfile for Go multi-stage build
- [X] T008 [P] Create docker/admin-ui.Dockerfile for Node.js build
- [X] T009 Create compose.yml with services: git-server (ports 9418, 8080), api (port 4000), admin-ui (port 3000)
- [X] T010 [P] Configure gqlgen.yml in api/ pointing to shared/schemas/*.graphql
- [X] T011 [P] Configure astro.config.mjs in admin-ui/ with React integration
- [X] T012 Create README.md with quickstart instructions and architecture diagram

---

## Phase 2: Foundational (Blocking Prerequisites)

**Purpose**: Core infrastructure that MUST be complete before ANY user story can be implemented

**⚠️ CRITICAL**: No user story work can begin until this phase is complete

- [X] T013 [P] Implement structured logging setup in git-server/src/lib.rs using tracing crate
- [X] T014 [P] Implement structured logging setup in api/internal/logger/logger.go using zap
- [X] T015 [P] Implement structured logging setup in admin-ui/src/lib/logger.ts using console wrappers
- [X] T016 [P] Create base domain models in git-server/src/models/mod.rs (Product, Category, Collection structs)
- [X] T017 [P] Create YAML front-matter parser in git-server/src/models/parser.rs using serde_yaml
- [X] T018 [P] Create markdown file reader in git-server/src/models/reader.rs
- [X] T019 [P] Run gqlgen generate in api/ to generate GraphQL resolvers from schemas
- [X] T020 [P] Create base resolver stubs in api/internal/graph/resolver.go
- [X] T021 [P] Create request ID middleware in api/internal/middleware/request_id.go
- [X] T022 [P] Create CORS middleware in api/internal/middleware/cors.go
- [X] T023 [P] Create urql Client setup in admin-ui/src/lib/urql-client.ts with request ID headers
- [X] T024 Create environment configuration loading for all three services (.env file support)

**Checkpoint**: Foundation ready - user story implementation can now begin in parallel

---

## Phase 3: User Story 1 - Technical User Creates Product Catalog (Priority: P1) 🎯 MVP

**Goal**: Enable git-based catalog management with validation and storefront queries

**Independent Test**: Create markdown files, commit, push to git server, create release tag, verify products appear via GraphQL query

### Tests for User Story 1 (Test-First Development) ⚠️

> **🚨 BLOCKING REQUIREMENT (Constitution Principle I - NON-NEGOTIABLE):**
>
> All test tasks (T025-T029) MUST be completed and FAILING before ANY implementation tasks (T030-T053) can begin.
>
> This enforces Test-First Development. No implementation code may be written until corresponding tests exist and fail.

- [X] T025 [P] [US1] Write contract test for products query in api/tests/contract/products_test.go
- [X] T026 [P] [US1] Write contract test for product(sku) query in api/tests/contract/product_test.go
- [X] T027 [P] [US1] Write integration test for git push → validation → accept in git-server/tests/integration/push_validation_test.rs
- [X] T028 [P] [US1] Write integration test for release tag → websocket notification in git-server/tests/integration/tag_notification_test.rs
- [X] T029 [P] [US1] Write integration test for websocket → cache reload in api/tests/integration/cache_reload_test.go

### Implementation for User Story 1

#### Git Server (Rust) - Validation & Notifications

- [X] T030 [P] [US1] Implement git repository initialization in git-server/src/git/repo.rs
- [X] T031 [P] [US1] Implement pre-receive hook handler in git-server/src/git/hooks.rs
- [X] T032 [US1] Implement Product validation logic in git-server/src/validation/product.rs (required fields, SKU uniqueness, price validation)
- [X] T033 [US1] Implement validation orchestrator in git-server/src/validation/validator.rs (parses all markdown files in push)
- [X] T034 [US1] Implement validation error response formatting in git-server/src/validation/errors.rs
- [X] T035 [P] [US1] Implement websocket server setup in git-server/src/websocket/server.rs using tungstenite
- [X] T036 [P] [US1] Implement websocket connection manager in git-server/src/websocket/connections.rs
- [X] T037 [US1] Implement tag event detection in git-server/src/git/events.rs
- [X] T038 [US1] Implement websocket broadcast on tag creation in git-server/src/websocket/broadcast.rs
- [X] T039 [US1] Wire up git server main.rs with git protocol listener (port 9418) and websocket (port 8080)

#### GraphQL API (Go) - Catalog Queries

- [X] T040 [P] [US1] Implement git repository reader in api/internal/gitclient/reader.go (clone, checkout tag)
- [X] T041 [P] [US1] Implement markdown file parser in api/internal/gitclient/parser.go (YAML front-matter + body)
- [X] T042 [US1] Implement Product model mapping in api/internal/models/product.go
- [X] T043 [P] [US1] Implement in-memory cache structure in api/internal/cache/catalog.go (ProductsByID, ProductsBySKU maps)
- [X] T044 [US1] Implement catalog loader in api/internal/cache/loader.go (read git tag → parse → populate cache)
- [X] T045 [P] [US1] Implement websocket client in api/internal/websocket/client.go (connect to git-server:8080)
- [X] T046 [US1] Implement cache invalidation on websocket notification in api/internal/cache/invalidator.go
- [X] T047 [US1] Implement products query resolver in api/internal/graph/products.resolvers.go (Relay connection pattern)
- [X] T048 [US1] Implement product(sku) query resolver in api/internal/graph/product.resolvers.go
- [X] T049 [US1] Implement Node interface resolver for Product in api/internal/graph/node.resolvers.go
- [X] T050 [US1] Wire up API server in api/cmd/server/main.go (GraphQL endpoint, websocket client startup)

#### Logging & Observability

- [X] T051 [P] [US1] Add structured logging to git validation pipeline in git-server/src/validation/validator.rs
- [X] T052 [P] [US1] Add structured logging to cache loader in api/internal/cache/loader.go (log: tag, product count, duration)
- [X] T053 [P] [US1] Add error logging for invalid markdown files in api/internal/gitclient/parser.go

**Checkpoint**: At this point, User Story 1 should be fully functional and testable independently

---

## Phase 4: User Story 2 - Organize Products with Categories and Collections (Priority: P2)

**Goal**: Add hierarchical categories and flat collections for product organization

**Independent Test**: Create category/collection markdown files, associate products, verify relationships via GraphQL

### Tests for User Story 2 (Test-First Development) ⚠️

> **🚨 BLOCKING REQUIREMENT (Constitution Principle I - NON-NEGOTIABLE):**
>
> All test tasks (T054-T057) MUST be completed and FAILING before ANY implementation tasks (T058-T078) can begin.

- [X] T054 [P] [US2] Write contract test for categories query in api/tests/contract/categories_test.go
- [X] T055 [P] [US2] Write contract test for collections query in api/tests/contract/collections_test.go
- [X] T056 [P] [US2] Write integration test for category parent-child relationships in api/tests/integration/category_hierarchy_test.go
- [X] T057 [P] [US2] Write integration test for products query filtered by categoryId in api/tests/contract/products_by_category_test.go

### Implementation for User Story 2

#### Git Server (Rust) - Validation Extensions

- [X] T058 [P] [US2] Implement Category validation logic in git-server/src/validation/category.rs (slug uniqueness, parent references, circular detection)
- [X] T059 [P] [US2] Implement Collection validation logic in git-server/src/validation/collection.rs (slug uniqueness, product references)
- [X] T060 [US2] Update Product validation to check category_id references in git-server/src/validation/product.rs
- [X] T061 [US2] Add category/collection validation to orchestrator in git-server/src/validation/validator.rs

#### GraphQL API (Go) - Category & Collection Queries

- [X] T062 [P] [US2] Implement Category model mapping in api/internal/models/category.go
- [X] T063 [P] [US2] Implement Collection model mapping in api/internal/models/collection.go
- [X] T064 [US2] Extend cache structure with CategoryByID, CategoryBySlug, CollectionByID maps in api/internal/cache/catalog.go
- [X] T065 [US2] Update catalog loader to parse categories and collections in api/internal/cache/loader.go
- [X] T066 [US2] Implement category hierarchy builder in api/internal/models/category_tree.go (parent-child linking)
- [X] T067 [US2] Implement categories query resolver in api/internal/graph/categories.resolvers.go
- [X] T068 [US2] Implement category(slug) query resolver in api/internal/graph/category.resolvers.go
- [X] T069 [US2] Implement collections query resolver in api/internal/graph/collections.resolvers.go
- [X] T070 [US2] Implement collection(slug) query resolver in api/internal/graph/collection.resolvers.go
- [X] T071 [US2] Implement Product.category field resolver (single category lookup) in api/internal/graph/product.resolvers.go
- [X] T072 [US2] Implement Product.collections field resolver (multiple collection lookup) in api/internal/graph/product.resolvers.go
- [X] T073 [US2] Implement Category.products field resolver with subcategory product inclusion in api/internal/graph/category.resolvers.go
- [X] T074 [US2] Implement Collection.products field resolver in api/internal/graph/collection.resolvers.go
- [X] T075 [US2] Implement orphaned reference handling (mark as invalid, don't fail queries) in api/internal/models/references.go

#### DataLoader for N+1 Prevention

- [ ] T076 [P] [US2] Implement category DataLoader in api/internal/loader/category_loader.go (batch category lookups)
- [ ] T077 [P] [US2] Implement collection DataLoader in api/internal/loader/collection_loader.go (batch collection lookups)
- [ ] T078 [US2] Wire DataLoaders into GraphQL context in api/internal/graph/resolver.go

**Checkpoint**: At this point, User Stories 1 AND 2 should both work independently

---

## Phase 5: User Story 3 - Non-Technical User Manages Catalog via Admin UI (Priority: P3)

**Goal**: Provide web UI for CRUD operations with drag-and-drop ordering and git integration

**Independent Test**: Login to admin UI, create/edit/delete entities, drag-and-drop reorder, publish, verify git commits and storefront updates

**Note**: Admin UI uses **urql** instead of Apollo Client due to Astro compatibility issues. All GraphQL client references updated accordingly.

### Tests for User Story 3 (Test-First Development) ⚠️

> **🚨 BLOCKING REQUIREMENT (Constitution Principle I - NON-NEGOTIABLE):**
>
> All test tasks (T079-T083) MUST be completed and FAILING before ANY implementation tasks (T084-T125) can begin.

- [X] T079 [P] [US3] Write contract test for createProduct mutation in api/tests/contract/create_product_test.go
- [X] T080 [P] [US3] Write contract test for updateProduct mutation with optimistic locking in api/tests/contract/update_product_test.go
- [X] T081 [P] [US3] Write contract test for publishCatalog mutation in api/tests/contract/publish_catalog_test.go
- [X] T082 [P] [US3] Write E2E test for product CRUD workflow in admin-ui/tests/e2e/product_crud.spec.ts
- [X] T083 [P] [US3] Write E2E test for drag-and-drop category reordering in admin-ui/tests/e2e/category_reorder.spec.ts

### Implementation for User Story 3

#### GraphQL API (Go) - Mutations & Git Client

- [X] T084 [P] [US3] Implement markdown file generator in api/internal/gitclient/writer.go (struct → YAML front-matter + markdown body)
- [X] T085 [US3] Implement git commit builder in api/internal/gitclient/commit.go (stage files, create commit with message)
- [X] T086 [US3] Implement git push client in api/internal/gitclient/push.go (push to git-server with validation handling)
- [X] T087 [US3] Implement git tag creator in api/internal/gitclient/tag.go (create annotated release tag)
- [X] T088 [P] [US3] Implement optimistic lock version checker in api/internal/graph/version_check.go
- [X] T089 [P] [US3] Implement diff generator for conflicts in api/internal/graph/diff.go
- [X] T090 [US3] Implement createProduct mutation resolver in api/internal/graph/mutations.resolvers.go
- [X] T091 [US3] Implement updateProduct mutation resolver with optimistic locking in api/internal/graph/mutations.resolvers.go
- [X] T092 [US3] Implement deleteProduct mutation resolver in api/internal/graph/mutations.resolvers.go
- [X] T093 [US3] Implement createCategory mutation resolver in api/internal/graph/mutations.resolvers.go
- [X] T094 [US3] Implement updateCategory mutation resolver in api/internal/graph/mutations.resolvers.go
- [X] T095 [US3] Implement deleteCategory mutation resolver in api/internal/graph/mutations.resolvers.go
- [X] T096 [US3] Implement reorderCategories mutation resolver in api/internal/graph/mutations.resolvers.go
- [X] T097 [US3] Implement createCollection mutation resolver in api/internal/graph/mutations.resolvers.go
- [X] T098 [US3] Implement updateCollection mutation resolver in api/internal/graph/mutations.resolvers.go
- [X] T099 [US3] Implement deleteCollection mutation resolver in api/internal/graph/mutations.resolvers.go
- [X] T100 [US3] Implement reorderCollections mutation resolver in api/internal/graph/mutations.resolvers.go
- [X] T101 [US3] Implement publishCatalog mutation resolver (commit all changes → push → tag) in api/internal/graph/mutations.resolvers.go
- [X] T102 [P] [US3] Implement single admin user authentication middleware in api/internal/middleware/auth.go (bcrypt password check)
- [X] T103 [P] [US3] Implement session token management in api/internal/auth/session.go (JWT or opaque tokens)

#### Admin UI (Astro/React) - CRUD Interface

- [X] T104 [P] [US3] Create authentication page in admin-ui/src/pages/login.astro
- [X] T105 [P] [US3] Create auth context provider in admin-ui/src/lib/auth-context.tsx (session management)
- [X] T106 [P] [US3] Generate TypeScript types from GraphQL schema in admin-ui/src/graphql/generated.ts using graphql-codegen
- [X] T107 [P] [US3] Create GraphQL mutation hooks in admin-ui/src/graphql/mutations.ts (createProduct, updateProduct, etc.)
- [X] T108 [P] [US3] Create GraphQL query hooks in admin-ui/src/graphql/queries.ts (products, categories, collections)
- [X] T109 [US3] Create product list page in admin-ui/src/pages/products/index.astro
- [X] T110 [US3] Create product form component in admin-ui/src/components/products/ProductForm.tsx (title, SKU, price, category, collections)
- [X] T111 [US3] Create product create page in admin-ui/src/pages/products/new.astro
- [X] T112 [US3] Create product edit page in admin-ui/src/pages/products/[id].astro with optimistic lock handling
- [X] T113 [US3] Implement markdown editor component in admin-ui/src/components/shared/MarkdownEditor.tsx
- [X] T114 [US3] Create category list page with tree view in admin-ui/src/pages/categories/index.astro
- [X] T115 [US3] Create category form component in admin-ui/src/components/categories/CategoryForm.tsx
- [X] T116 [US3] Implement drag-and-drop category tree in admin-ui/src/components/categories/CategoryTree.tsx using react-beautiful-dnd
- [X] T117 [US3] Implement category reorder handler in admin-ui/src/components/categories/CategoryTree.tsx (calls reorderCategories mutation)
- [X] T118 [US3] Create collection list page in admin-ui/src/pages/collections/index.astro
- [X] T119 [US3] Create collection form component in admin-ui/src/components/collections/CollectionForm.tsx
- [X] T120 [US3] Implement drag-and-drop collection list in admin-ui/src/components/collections/CollectionList.tsx
- [X] T121 [US3] Implement collection product selector in admin-ui/src/components/collections/ProductSelector.tsx (multi-select)
- [X] T122 [US3] Create publish button component in admin-ui/src/components/shared/PublishButton.tsx
- [X] T123 [US3] Implement publish flow in admin-ui/src/lib/publish.ts (version input, confirmation, publishCatalog mutation)
- [X] T124 [P] [US3] Create conflict resolution modal in admin-ui/src/components/shared/ConflictModal.tsx (shows diff, allows overwrite/cancel)
- [X] T125 [P] [US3] Implement optimistic UI updates for mutations in admin-ui/src/lib/optimistic-updates.ts (urql cache updates)
- [X] T126 [P] [US3] Implement client-side validation in admin-ui/src/lib/validation.ts (validate required fields, formats, constraints before mutation submission to catch errors early and provide immediate feedback)

**Checkpoint**: All user stories should now be independently functional

**⚠️ MANUAL TESTING STATUS (2026-03-22)**:
- ✅ Admin UI functional
- ✅ GraphQL schema and resolvers operational
- ❌ **BLOCKING**: Repository access failing (T145-T146)
- ❌ **BLOCKING**: Websocket notifications not working (T147-T148)
- Phase 3 checkpoint cannot be validated until T145-T148 are resolved

---

## Phase 6: Polish & Cross-Cutting Concerns

**Purpose**: Improvements that affect multiple user stories

- [X] T127 [P] Add GraphQL filtering support including price range (ProductFilter with priceMin/priceMax parameters) to products query in api/internal/graph/products.resolvers.go
- [ ] T128 [P] Implement cursor pagination helpers in api/internal/graph/pagination.go (Relay connections)
- [ ] T129 [P] Add git repository size monitoring in git-server/src/git/metrics.rs
- [ ] T130 [P] Add catalog statistics to CatalogVersion type in api/internal/graph/catalog_version.resolvers.go
- [X] T131 [P] Create initialization script in scripts/init-demo-catalog.sh (creates sample products/categories/collections)
- [ ] T132 [P] Add graceful shutdown handling for websocket connections in git-server/src/websocket/server.rs
- [ ] T133 [P] Add connection pooling for git operations in api/internal/gitclient/pool.go
- [ ] T134 [P] Implement request rate limiting middleware in api/internal/middleware/rate_limit.go
- [X] T135 [P] Add health check endpoints for all three services (/health, /ready)
- [ ] T136 [P] Create Prometheus metrics exporters for api and git-server
- [ ] T137 [P] Add accessibility labels to admin UI components (ARIA attributes)
- [ ] T138 [P] Implement loading states for all async operations in admin UI
- [ ] T139 [P] Add error boundaries in admin UI React components
- [X] T140 [P] Create user documentation in docs/user-guide.md
- [X] T141 [P] Create API documentation in docs/api-reference.md
- [X] T142 [P] Validate quickstart.md examples against running system
- [ ] T143 [P] Write E2E integration test validating request ID propagation from admin-ui → api → git-server in tests/e2e/request_tracing.spec.ts (validates Constitution Principle IV - Observability)
- [X] T144 Run all tests across all three services (cargo test, go test, npm test)

---

## Phase 7: Bug Fixes & Investigation (Added 2026-03-22)

**Purpose**: Address issues discovered during manual testing with Docker compose setup

**Context**: Following scripts/README.md instructions, two critical issues were identified:
1. GraphQL API returns "repository does not exist" error when querying products
2. Websocket notifications not firing when creating git tags in Docker environment

### Investigation Tasks - Repository Not Found

- [X] T145 [CRITICAL] Investigate "repository does not exist" error in GraphQL API
  - **Location**: api/internal/gitclient/ or api/internal/cache/
  - **Symptoms**: GraphQL query fails with "failed to get products: failed to get catalog: failed to open repository: repository does not exist"
  - **Test Query**: `query { products { edges { node { id sku title } } } }`
  - **Environment**: Docker compose with GITSTORE_DATA_DIR volume mount
  - **Root Cause Found**: Architecture mismatch - API received git:// URL but used git.PlainOpen() expecting local path, plus missing volume mount
  - **Investigation Report**: specs/001-git-backed-ecommerce/T145-INVESTIGATION.md
  - **Status**: ✅ RESOLVED - Quick fix applied (shared volume)

- [X] T146 [CRITICAL] Fix repository initialization in Docker environment
  - **Depends on**: T145 investigation findings
  - **Solution Applied**: Option 2 (Shared Volume) quick fix
    - [X] Added volume mount to API service in compose.yml
    - [X] Changed GITSTORE_GIT_REPO from git:// URL to local path
    - [X] API now has read-only access to /data/repos
  - **Testing**: ✅ All pass conditions met (QUICK-TEST.md)
  - **Status**: ✅ QUICK FIX COMPLETE
  - **Future**: T152 will implement proper git protocol solution (Option 1)

### Investigation Tasks - Websocket Notifications

- [X] T147 [CRITICAL] Investigate websocket notification failure on tag creation
  - **Status**: ✅ RESOLVED
  - **Root Causes Found**:
    1. README workflow out of order (services not started before clone)
    2. Filesystem clone used instead of HTTP clone (bypassed git-server)
    3. Git binary missing from git-server Docker image
    4. Websocket message format mismatch (fixed to use GitEvent structure)
  - **Fixes Applied**: All issues resolved, websocket notifications working end-to-end

- [X] T148 [CRITICAL] Fix tag event detection and validation issues
  - **Status**: ✅ RESOLVED
  - **Issues Fixed**:
    1. Init script missing required `product_ids` field in collections (added `product_ids: []`)
    2. Websocket message format corrected to use `GitEvent::release_created()`
    3. README.md and init script documentation updated with correct workflow
  - **Remaining**: Validation error messages still use Rust debug format (see T153)

- [ ] T153 [MEDIUM] Improve git validation error message formatting
  - **Location**: git-server/src/http_git_server.rs:264-267
  - **Issue**: Currently returns Rust debug format to git client
  - **Current**:
    ```
    error: RPC failed; HTTP 422 curl 22 The requested URL returned error: 422
    send-pack: unexpected disconnect while reading sideband packet
    ```
  - **Should return** (GitHub-style):
    ```
    remote: ========================================
    remote: GitStore Catalog Validation Failed
    remote: ========================================
    remote:
    remote: File: products/prod_book_001.md
    remote:   - Invalid currency code: XYZ
    remote:   - Price must be >= 0
    remote:
    ! [remote rejected] main -> main (validation failed)
    ```
  - **Implementation**: Create `format_validation_errors_for_git()` helper function
  - **Priority**: Not blocking MVP, improves UX

- [ ] T149 [P] Add websocket notification health check
  - **Location**: git-server/src/websocket/server.rs and api/cmd/server/main.go
  - **Purpose**: Verify websocket connectivity at startup
  - **Implementation**:
    - [ ] API logs websocket connection status on startup
    - [ ] Git-server logs active websocket connections
    - [ ] Add /websocket/health endpoint to git-server
    - [ ] API retries websocket connection on failure with backoff

### Integration Testing

- [ ] T150 [P] Create end-to-end Docker compose test script
  - **Location**: tests/e2e/docker-test.sh
  - **Purpose**: Automated testing of Docker deployment workflow
  - **Steps**:
    1. Run scripts/init-demo-catalog.sh
    2. Start docker compose
    3. Wait for health checks
    4. Create git tag v1.0.0
    5. Query GraphQL API for products
    6. Verify products returned
    7. Verify API logs show websocket notification received
  - **Expected**: End-to-end workflow completes successfully

- [ ] T151 [P] Document Docker deployment troubleshooting
  - **Location**: docs/docker-troubleshooting.md or README.md section
  - **Content**:
    - [ ] Repository initialization checklist
    - [ ] Websocket connectivity verification steps
    - [ ] Volume mount path debugging
    - [ ] Common error messages and solutions
    - [ ] Log locations for each service

- [ ] T152 [P] Implement proper git protocol solution for catalog queries
  - **Purpose**: Replace shared volume quick fix with production-ready git protocol (like GitLab/Gitea)
  - **Location**: api/internal/catalog/loader.go
  - **Current Issue**: API reads from shared volume `/data/repos/` (tight coupling, can't scale)
  - **Changes**:
    - [ ] Detect if repoPath is git:// URL or local path
    - [ ] Implement git.Clone() for remote URLs
    - [ ] Cache cloned repository in temporary directory
    - [ ] Implement pull/update logic for repository refresh
    - [ ] Support both remote and local repository modes
  - **Benefits**: True microservices, remote git-server support, better scalability
  - **Testing**: Verify both git:// and /data/repos/ paths work
  - **Priority**: DEFERRED - Quick fix (T146) sufficient for MVP
  - **Documentation**: See T152-PROPER-GIT-PROTOCOL.md for detailed implementation plan

- [ ] T154 [MEDIUM] Use temporary clones for API mutations (GitLab/Gitea pattern)
  - **Purpose**: API should not maintain persistent working copy for mutations
  - **Current Issue**: API has persistent working directory (if implemented) - wrong architecture
  - **Location**: api/internal/gitclient/ mutation handlers
  - **Correct Pattern** (how GitLab/Gitea work):
    ```go
    func (s *MutationResolver) UpdateProduct(...) {
        // 1. Create temp directory
        tmpDir, _ := os.MkdirTemp("", "gitstore-*")
        defer os.RemoveAll(tmpDir)  // Auto-cleanup

        // 2. Clone from git-server
        git.PlainClone(tmpDir, false, &git.CloneOptions{
            URL: "git://git-server:9418/catalog.git",
        })

        // 3. Modify files in temp clone
        writeProductFile(tmpDir, product)

        // 4. Commit and push
        repo.Worktree().Commit("Update product", ...)
        repo.Push(...)

        // 5. Temp clone deleted automatically
    }
    ```
  - **Benefits**:
    - No persistent state in API (stateless, scalable)
    - Each mutation isolated (no file conflicts)
    - Matches industry standard (GitLab, Gitea, GitHub)
  - **Testing**: Verify mutations still work, check temp dirs cleaned up
  - **Priority**: DEFERRED - Current approach works for MVP, refactor for production

---

## MVP Scope Clarification (Updated 2026-03-22)

**Out of Scope for MVP** (Deferred to post-launch):
- ❌ Image URL validation and CDN integration (FR-020 partial - basic URL format check sufficient)
- ❌ AI agent validation testing (SC-006 - aspirational metric, not blocking)
- ❌ Advanced metrics and monitoring (T136 - Prometheus exporters)
- ❌ Accessibility enhancements (T137 - ARIA labels)
- ❌ Rate limiting (T134 - not needed for initial deployment scale)

**In Scope for MVP** (Must Complete):
- ✅ User Stories 1, 2, 3 (P1, P2, P3)
- ✅ Repository initialization and catalog loading (T145-T146 fixes)
- ✅ Websocket notifications (T147-T148 fixes)
- ✅ GraphQL filtering (T127 - required for storefront queries)
- ✅ Basic documentation (T140-T142)
- ✅ Health checks (T135 - already complete)
- ✅ Demo catalog initialization (T131 - already complete)

---

## Dependencies & Execution Order

### Phase Dependencies

- **Setup (Phase 1)**: No dependencies - can start immediately
- **Foundational (Phase 2)**: Depends on Setup completion - BLOCKS all user stories
- **User Stories (Phase 3-5)**: All depend on Foundational phase completion
  - User stories can then proceed in parallel (if staffed)
  - Or sequentially in priority order (P1 → P2 → P3)
- **Polish (Phase 6)**: Depends on all desired user stories being complete

### User Story Dependencies

- **User Story 1 (P1)**: Can start after Foundational (Phase 2) - No dependencies on other stories
- **User Story 2 (P2)**: Can start after Foundational (Phase 2) - No dependencies on US1 (can develop in parallel)
- **User Story 3 (P3)**: Can start after Foundational (Phase 2) - Uses US1 and US2 mutations but can stub for testing

### Within Each User Story

- Tests (Test-First) MUST be written and FAIL before implementation
- Git server components before API (API depends on git server running)
- API mutations before Admin UI (UI calls API)
- Core implementation before integration
- Story complete before moving to next priority

### Parallel Opportunities

- All Setup tasks marked [P] can run in parallel
- All Foundational tasks marked [P] can run in parallel (within Phase 2)
- Once Foundational phase completes, all user stories can start in parallel (if team capacity allows)
- All tests for a user story marked [P] can run in parallel
- Within a story, models/validators marked [P] can run in parallel
- Different user stories can be worked on in parallel by different team members

---

## Parallel Example: User Story 1

```bash
# Launch all tests for User Story 1 together (Test-First):
Task: "Write contract test for products query in api/tests/contract/products_test.go" [T025]
Task: "Write contract test for product(sku) query in api/tests/contract/product_test.go" [T026]
Task: "Write integration test for git push validation in git-server/tests/integration/push_validation_test.rs" [T027]

# After tests are written and failing, launch parallel implementations:
Task: "Implement git repository initialization in git-server/src/git/repo.rs" [T030]
Task: "Implement pre-receive hook handler in git-server/src/git/hooks.rs" [T031]
Task: "Implement websocket server setup in git-server/src/websocket/server.rs" [T035]
Task: "Implement git repository reader in api/internal/gitclient/reader.go" [T040]
```

---

## Implementation Strategy

### MVP First (User Story 1 Only)

1. Complete Phase 1: Setup
2. Complete Phase 2: Foundational (CRITICAL - blocks all stories)
3. Complete Phase 3: User Story 1
4. **STOP and VALIDATE**: Test User Story 1 independently (git push → storefront query)
5. Deploy/demo if ready

### Incremental Delivery

1. Complete Setup + Foundational → Foundation ready
2. Add User Story 1 → Test independently → Deploy/Demo (MVP!)
3. Add User Story 2 → Test independently → Deploy/Demo (categories/collections)
4. Add User Story 3 → Test independently → Deploy/Demo (admin UI)
5. Each story adds value without breaking previous stories

### Parallel Team Strategy

With multiple developers:

1. Team completes Setup + Foundational together
2. Once Foundational is done:
   - Developer/Team A: User Story 1 (git server + API queries)
   - Developer/Team B: User Story 2 (categories/collections)
   - Developer/Team C: User Story 3 (admin UI)
3. Stories complete and integrate independently

---

## Notes

- [P] tasks = different files, no dependencies
- [Story] label maps task to specific user story for traceability
- Each user story should be independently completable and testable
- Verify tests fail before implementing (Test-First Development - Constitution Principle I)
- Commit after each task or logical group
- Stop at any checkpoint to validate story independently
- Test-First is NON-NEGOTIABLE per Constitution - all tests written before implementation
- Avoid: vague tasks, same file conflicts, cross-story dependencies that break independence
