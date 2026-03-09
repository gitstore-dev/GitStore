# Research: GitStore Technology Stack & Patterns

**Date**: 2026-03-09
**Feature**: GitStore - Git-Backed Ecommerce Engine
**Phase**: Phase 0 - Technical Research

## Rust Git Server Research

### Decision: libgit2-rs for Git Operations
**Rationale**: libgit2-rs provides mature Rust bindings to libgit2, the industry-standard git implementation library. Offers full git protocol support, repository management, and validation hooks.

**Alternatives Considered**:
- **gitoxide (gix)**: Pure Rust git implementation, but less mature and missing some advanced features needed for pre-push validation hooks
- **git2**: Direct C bindings, but libgit2-rs provides safer idiomatic Rust API
- **Subprocess calls to git binary**: Rejected due to performance overhead and difficulty testing validation logic

**Key Capabilities**:
- Repository initialization and configuration
- Commit, tree, and blob operations
- Pre-receive hooks for validation
- Reference (tag/branch) management
- Custom transport protocols

### Decision: Tungstenite for Websocket Server
**Rationale**: Tungstenite is a lightweight, low-level websocket library for Rust with excellent performance and tokio async runtime integration.

**Alternatives Considered**:
- **tokio-tungstenite**: Wrapper around tungstenite with tokio integration (will use this)
- **ws-rs**: Older, synchronous library - rejected for async requirements
- **actix-web websocket**: Coupled to actix web framework - unnecessary dependency

**Implementation Pattern**:
- Broadcast channel for notification distribution
- Connection management with heartbeat/ping-pong
- Message format: JSON with event type and payload

### Decision: Serde + Serde_yaml for Front-Matter Parsing
**Rationale**: Serde is the de facto serialization framework in Rust, with serde_yaml providing robust YAML front-matter parsing.

**Validation Strategy**:
- Define schema types with `#[derive(Deserialize)]`
- Use serde validation attributes for required fields
- Custom validation logic for business rules (SKU uniqueness, category references)

---

## Go GraphQL API Research

### Decision: gqlgen for GraphQL Server
**Rationale**: gqlgen is a schema-first GraphQL library for Go that generates type-safe resolvers from GraphQL schema definitions, aligning with API-First Design principle.

**Alternatives Considered**:
- **graphql-go**: Reflection-based, less type-safe
- **thunder**: Abandoned, unmaintained project
- **Manual implementation**: Rejected due to complexity and lack of tooling

**Key Features**:
- Schema-first code generation
- Relay specification support via plugins
- DataLoader pattern for N+1 query prevention
- Subscription support (for future real-time features)

### Decision: graphql-relay-go for Relay Support
**Rationale**: Official Relay specification implementation for Go, provides node interface, cursor-based pagination, and connection patterns.

**Relay Patterns to Implement**:
- **Node interface**: Global object identification (Product, Category, Collection)
- **Connection pattern**: Cursor-based pagination for product lists
- **Mutation pattern**: Standardized input/output structure

### Decision: In-Memory Cache with Invalidation
**Rationale**: Git repositories are read-heavy, catalog updates infrequent (1-10/day). In-memory cache eliminates repeated git operations.

**Cache Strategy**:
- Load entire catalog from latest release tag into memory on startup
- Subscribe to git server websocket for invalidation events
- Atomic swap of cache on new release tag notification
- TTL fallback: re-read git every 5 minutes if websocket fails

**Alternatives Considered**:
- **Redis**: Unnecessary external dependency for current scale
- **Direct git reads per query**: Poor performance for 500ms query SLA
- **Persistent cache (SQLite)**: Added complexity, git is source of truth

---

## Admin UI Research

### Decision: Astro + React for Admin UI
**Rationale**: Astro provides optimal static site generation with islands architecture, React for interactive components (drag-drop). Minimal JavaScript shipped to client.

**Key Advantages**:
- Server-side rendering by default (fast initial load)
- Partial hydration (only interactive components load JS)
- React ecosystem for drag-drop libraries
- TypeScript support out of the box

**Alternatives Considered**:
- **Next.js**: Heavier runtime, server infrastructure requirements
- **Remix**: Focused on server-side routing, overkill for admin UI
- **SvelteKit**: Smaller ecosystem for drag-drop components
- **Vue.js**: React chosen due to larger drag-drop library ecosystem

### Decision: react-beautiful-dnd for Drag-and-Drop
**Rationale**: Industry-standard drag-and-drop library with excellent accessibility, touch support, and tree-based ordering capabilities.

**Implementation Patterns**:
- **Category Tree**: Nested droppable contexts for hierarchical ordering
- **Collection Ordering**: Simple list reordering with Droppable/Draggable
- **Product Assignment**: Drag products into categories/collections

**Alternatives Considered**:
- **dnd-kit**: Modern alternative with better performance, but less mature tree support
- **react-dnd**: Lower-level, requires more custom code
- **HTML5 Drag-Drop API**: Poor mobile support, accessibility issues

### Decision: Apollo Client for GraphQL
**Rationale**: Industry-standard GraphQL client with comprehensive caching, optimistic updates, and Relay pagination support.

**Key Features**:
- Normalized caching (by node ID)
- Optimistic UI updates for mutations
- Relay-style pagination helpers
- TypeScript code generation from schema

**Alternatives Considered**:
- **urql**: Lighter weight but less comprehensive caching
- **graphql-request**: Too minimal, lacks caching
- **Relay**: Over-engineered for admin UI, steep learning curve

---

## Cross-Cutting Concerns

### Decision: Structured Logging Strategy
**Rust (git-server)**:
- `tracing` + `tracing-subscriber` for structured logging
- Log formats: JSON for production, pretty for development
- Spans for request tracing

**Go (API)**:
- `zap` or `zerolog` for structured logging
- Fields: request_id, user_id, operation, duration
- Correlation ID propagation from HTTP headers

**TypeScript (Admin UI)**:
- Client-side: `console` with structured prefixes for development
- Server-side (Astro): `pino` for structured logging
- Error boundary logging for React components

### Decision: Validation Strategy
**Git Server (Rust)**:
- Pre-push validation (blocking)
- Required field validation (front-matter schema)
- Referential integrity checks (category/collection existence)
- SKU uniqueness validation
- Return detailed error JSON on validation failure

**API Layer (Go)**:
- GraphQL input validation (gqlgen validators)
- Business rule validation in resolvers
- Return user-friendly error messages per GraphQL spec

**Admin UI (TypeScript/React)**:
- Client-side validation (immediate feedback)
- Form validation with React Hook Form + Zod schema
- Server-side validation as final gate

### Decision: Testing Strategy
**Rust (cargo test)**:
- Unit tests for validation logic
- Integration tests with temporary git repositories
- Property-based testing for YAML parsing (proptest)

**Go (go test)**:
- Contract tests for GraphQL schema (gqlgen test harness)
- Integration tests with mock git repositories
- Table-driven tests for resolver logic

**TypeScript (Vitest + Playwright)**:
- Component unit tests (React Testing Library)
- E2E tests for critical flows (Playwright)
- Visual regression tests for drag-drop UI

---

## Deployment Architecture

### Decision: Docker Containers + docker-compose
**Rationale**: Consistent deployment across development and production, service isolation, easy local development setup.

**Container Strategy**:
- **git-server**: Multi-stage build (Rust compile → slim runtime)
- **api**: Multi-stage build (Go compile → alpine runtime)
- **admin-ui**: Static build served by nginx or built-in Astro server

**docker-compose.yml Structure**:
```yaml
services:
  git-server:
    build: ./git-server
    ports: [9418:9418, 8080:8080]  # git protocol, websocket
    volumes: [./data/repos:/repos]

  api:
    build: ./api
    ports: [4000:4000]
    depends_on: [git-server]
    environment:
      GIT_SERVER_WS: ws://git-server:8080

  admin-ui:
    build: ./admin-ui
    ports: [3000:3000]
    environment:
      GRAPHQL_URL: http://api:4000/graphql
```

---

## Performance Optimizations

### Decision: DataLoader Pattern in Go API
**Rationale**: Prevents N+1 queries when loading products with categories/collections. Batches and caches entity lookups within request context.

**Implementation**:
- Use `graph-gophers/dataloader` or custom implementation
- Batch product category lookups
- Batch collection product references

### Decision: Cursor-Based Pagination (Relay Connections)
**Rationale**: Efficient pagination for large product catalogs, stable across data mutations.

**Implementation**:
- Cursor: base64-encoded product ID or offset
- Connection edges with cursor per item
- PageInfo with hasNextPage/hasPreviousPage

### Decision: Markdown Parsing Cache in Rust
**Rationale**: Avoid re-parsing markdown on every validation. Cache parsed structures keyed by file hash.

**Cache Invalidation**:
- File content hash as key (SHA-256)
- Evict on file modification
- LRU eviction for memory management

---

## Security Considerations

### Decision: Single Admin User with Bcrypt Password
**Rationale**: Spec requires single admin user (§FR-019). Bcrypt provides industry-standard password hashing.

**Implementation**:
- Password stored as bcrypt hash (cost factor 12)
- Session token (JWT or opaque token with expiration)
- HTTPS required for admin UI (TLS termination at reverse proxy)

**Future RBAC Path**:
- Database schema designed to support multiple users
- Role/permission fields nullable for now
- Migration path documented

### Decision: Git Push Authentication
**Rationale**: Prevent unauthorized catalog modifications.

**Implementation Options**:
- SSH keys (standard git authentication)
- HTTP Basic Auth over TLS
- Token-based authentication (custom git credential helper)

**Recommendation**: Start with SSH keys (leverage git ecosystem), add HTTP auth later if needed.

---

## Open Questions / Future Research

1. **Metrics Collection**: Prometheus vs StatsD vs OpenTelemetry - deferred to implementation
2. **Image CDN Integration**: Cloudflare, AWS S3, or bring-your-own - configurable endpoint
3. **GraphQL Subscription Strategy**: For real-time storefront updates - Phase 2 feature
4. **Multi-Repository Support**: Single git-server managing multiple catalogs - future enhancement
5. **Backup/Recovery Strategy**: Git replication, periodic snapshots - deployment concern

---

## Summary

All technical unknowns from plan.md resolved. Technology stack validated:
- ✅ Rust + libgit2 for git server (performance, safety)
- ✅ Go + gqlgen for GraphQL API (schema-first, type-safe)
- ✅ Astro + React for Admin UI (SSR, modern DX)
- ✅ Relay pagination for scalability
- ✅ Websocket for real-time notifications
- ✅ In-memory caching strategy defined
- ✅ Testing frameworks selected per language

Ready to proceed to Phase 1: Data Model & Contracts.
