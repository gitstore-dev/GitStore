# Implementation Plan: GitStore - Git-Backed Ecommerce Engine

**Branch**: `001-git-backed-ecommerce` | **Date**: 2026-03-09 | **Spec**: [spec.md](./spec.md)
**Input**: Feature specification from `/specs/001-git-backed-ecommerce/spec.md`

**Note**: This template is filled in by the `/speckit.plan` command. See `.specify/templates/plan-template.md` for the execution workflow.

## Summary

GitStore is a self-contained git-backed ecommerce headless engine enabling catalog management through markdown files with YAML front-matter. The system comprises three main components: (1) a Rust-based built-in git server with pre-push validation and websocket notifications, (2) a Go-based GraphQL API layer with Relay support exposing catalog data, and (3) an Astro/React admin UI with drag-and-drop for category/collection ordering. Technical users and AI agents manage catalogs via git workflows, while non-technical users use the admin UI. The storefront reads from the latest release tag, supporting products, hierarchical categories, and flat collections.

## Technical Context

**Language/Version**:
- Rust 1.75+ (built-in git server)
- Go 1.21+ (GraphQL API layer)
- TypeScript 5.0+ (Admin UI - Astro with React)

**Primary Dependencies**:
- **Git Server (Rust)**: libgit2 bindings, tokio (async runtime), tungstenite (websocket), serde (serialization)
- **API Layer (Go)**: gqlgen (GraphQL code generation), graphql-relay-go (Relay support), go-git (git operations)
- **Admin UI (Astro/React)**: Astro 4.0+, React 18+, react-beautiful-dnd (drag-and-drop), Apollo Client (GraphQL), urql (alternative GraphQL client)

**Storage**:
- Git repositories (markdown files with YAML front-matter) - primary data store
- In-memory cache for parsed catalog data at API layer
- External CDN/object storage for product images (URLs in front-matter)

**Testing**:
- **Rust**: cargo test (unit), integration tests with test git repos
- **Go**: go test, gqlgen test harness for GraphQL contracts
- **Admin UI**: Vitest (unit), Playwright (E2E), React Testing Library

**Target Platform**:
- Linux server (primary deployment - Docker containers)
- macOS (development environment support)
- Browser (Admin UI - modern evergreen browsers)

**Project Type**: Distributed web service with three components (git server, GraphQL API, web admin UI)

**Performance Goals**:
- Storefront catalog queries: < 500ms for 1000+ products (Spec §SC-007)
- Storefront update latency: < 30 seconds from release tag creation (Spec §SC-002)
- Git push validation: < 5 seconds for 100 file push
- Websocket notification delivery: < 100ms from tag creation

**Constraints**:
- Single admin user model (no RBAC initially - Spec §FR-019)
- Product catalog size: up to 10,000 products (Spec Assumption 9)
- Git repository size: reasonable for git operations (< 500MB for markdown + metadata)
- Websocket connections: up to 100 concurrent storefront subscribers

**Scale/Scope**:
- Initial deployment: 1-10 catalog repositories
- Concurrent admin UI users: 1-5
- Storefront API consumers: 10-1000 concurrent requests
- Catalog updates: 1-10 per day typical

## Constitution Check

*GATE: Must pass before Phase 0 research. Re-check after Phase 1 design.*

### Principle I: Test-First Development (NON-NEGOTIABLE)
- ✅ **PASS**: Specification includes acceptance scenarios for all user stories (Spec §User Scenarios & Testing)
- ✅ **PASS**: Contract tests required for GraphQL API (Constitution mandates contracts → tests)
- ✅ **PASS**: Integration tests cover P1/P2/P3 user journeys
- ⚠️ **PLANNING REQUIRED**: Specific test file locations and naming conventions TBD in tasks.md

### Principle II: API-First Design
- ✅ **PASS**: GraphQL schema must be defined before implementation (contracts/ directory)
- ✅ **PASS**: Relay specification provides standardized contract patterns
- ⚠️ **PLANNING REQUIRED**: GraphQL schema design deferred to Phase 1

### Principle III: Clear Contracts & Versioning
- ✅ **PASS**: GraphQL schema versioning strategy (schema evolution vs. breaking changes) TBD in contracts/
- ✅ **PASS**: Release tags follow semantic versioning (Spec Assumption 3)
- ⚠️ **PLANNING REQUIRED**: API deprecation policy needs documentation

### Principle IV: Observability & Debuggability
- ✅ **PASS**: Structured logging required across all components (Rust, Go, Astro)
- ✅ **PASS**: Validation errors logged with detail (Spec §FR-022)
- ✅ **PASS**: Request ID propagation across git→API→UI boundaries
- ⚠️ **PLANNING REQUIRED**: Metrics collection strategy (Prometheus, StatsD) TBD

### Principle V: User Story Driven Development
- ✅ **PASS**: Three prioritized user stories defined (P1: Git workflow, P2: Organization, P3: Admin UI)
- ✅ **PASS**: Each story has independent test criteria
- ✅ **PASS**: Acceptance scenarios in Given-When-Then format

### Principle VI: Incremental Delivery
- ✅ **PASS**: P1 (Git workflow + storefront query) delivers MVP
- ✅ **PASS**: P2 (Categories/Collections) independent of P3
- ✅ **PASS**: P3 (Admin UI) optional enhancement
- ⚠️ **PLANNING REQUIRED**: Feature flag strategy for incremental UI features TBD

### Principle VII: Simplicity & YAGNI
- ✅ **PASS**: No speculative multi-tenant features (explicit out-of-scope)
- ✅ **PASS**: Single admin user avoids RBAC complexity initially
- ⚠️ **REVIEW REQUIRED**: Three-language architecture (Rust/Go/TypeScript) adds complexity - see Complexity Tracking

**Overall Status**: ✅ **CONDITIONALLY PASS** - Proceed to Phase 0 research with planning items tracked

---

## Post-Design Constitution Re-Check

*GATE: Re-evaluated after Phase 1 design completion*

### Principle I: Test-First Development (NON-NEGOTIABLE)
- ✅ **PASS**: Contract tests can be generated from GraphQL schema (gqlgen test harness)
- ✅ **PASS**: Test file structure documented in developer-guide.md
- ✅ **PASS**: Integration test patterns defined in research.md

### Principle II: API-First Design
- ✅ **PASS**: GraphQL contracts fully defined in contracts/ directory
- ✅ **PASS**: Contracts reviewed and ready for code generation
- ✅ **PASS**: Contract-first workflow documented (define → test → implement)

### Principle III: Clear Contracts & Versioning
- ✅ **PASS**: Semantic versioning for release tags documented
- ✅ **PASS**: GraphQL schema evolution strategy defined (contracts/README.md)
- ✅ **PASS**: Deprecation policy documented

### Principle IV: Observability & Debuggability
- ✅ **PASS**: Structured logging libraries selected (tracing, zap/zerolog, pino)
- ✅ **PASS**: Request ID propagation pattern defined
- ⚠️ **DEFERRED**: Specific metrics strategy (Prometheus/StatsD) deferred to implementation

### Principle V: User Story Driven Development
- ✅ **PASS**: Quickstart demonstrates all three user stories
- ✅ **PASS**: Each story independently testable

### Principle VI: Incremental Delivery
- ✅ **PASS**: Quickstart demonstrates incremental workflow (P1 → P2 → P3)
- ⚠️ **DEFERRED**: Feature flag implementation strategy deferred to tasks

### Principle VII: Simplicity & YAGNI
- ✅ **PASS**: No premature abstractions in data model
- ✅ **PASS**: In-memory cache chosen over external dependencies
- ✅ **JUSTIFIED**: Polyglot architecture complexity documented in Complexity Tracking

**Final Status**: ✅ **PASS** - Ready for task generation (`/speckit.tasks`)

## Project Structure

### Documentation (this feature)

```text
specs/[###-feature]/
├── plan.md              # This file (/speckit.plan command output)
├── research.md          # Phase 0 output (/speckit.plan command)
├── data-model.md        # Phase 1 output (/speckit.plan command)
├── quickstart.md        # Phase 1 output (/speckit.plan command)
├── contracts/           # Phase 1 output (/speckit.plan command)
└── tasks.md             # Phase 2 output (/speckit.tasks command - NOT created by /speckit.plan)
```

### Source Code (repository root)

```text
gitstore/
├── git-server/                  # Rust-based built-in git server
│   ├── src/
│   │   ├── lib.rs              # Library root
│   │   ├── main.rs             # Server entry point
│   │   ├── git/                # Git operations (libgit2 wrapper)
│   │   ├── validation/         # Pre-push validation logic
│   │   ├── websocket/          # Websocket notification server
│   │   └── models/             # Domain models (Product, Category, Collection)
│   ├── tests/
│   │   ├── integration/        # Integration tests with test git repos
│   │   └── unit/               # Unit tests
│   ├── Cargo.toml
│   └── Cargo.lock
│
├── api/                         # Go-based GraphQL API layer
│   ├── cmd/
│   │   └── server/             # API server entry point
│   ├── internal/
│   │   ├── graph/              # GraphQL resolvers (gqlgen generated)
│   │   ├── models/             # Domain models
│   │   ├── loader/             # DataLoader pattern for N+1 prevention
│   │   ├── cache/              # In-memory catalog cache
│   │   └── gitclient/          # Git repository reader
│   ├── tests/
│   │   ├── contract/           # GraphQL contract tests
│   │   └── integration/        # Integration tests
│   ├── go.mod
│   ├── go.sum
│   └── gqlgen.yml              # GraphQL code generation config
│
├── admin-ui/                    # Astro/React admin interface
│   ├── src/
│   │   ├── pages/              # Astro page routes
│   │   ├── components/         # React components
│   │   │   ├── products/      # Product CRUD components
│   │   │   ├── categories/    # Category tree + drag-drop
│   │   │   └── collections/   # Collection management + drag-drop
│   │   ├── graphql/            # GraphQL queries/mutations
│   │   ├── hooks/              # Custom React hooks
│   │   └── lib/                # Utilities (Apollo client, auth)
│   ├── tests/
│   │   ├── unit/               # Component unit tests (Vitest)
│   │   └── e2e/                # End-to-end tests (Playwright)
│   ├── astro.config.mjs
│   ├── package.json
│   └── tsconfig.json
│
├── shared/                      # Shared types and schemas
│   └── schemas/                # GraphQL schema files
│       ├── schema.graphql      # Main schema
│       ├── product.graphql     # Product type definitions
│       ├── category.graphql    # Category type definitions
│       └── collection.graphql  # Collection type definitions
│
├── docker/                      # Container configurations
│   ├── git-server.Dockerfile
│   ├── api.Dockerfile
│   └── admin-ui.Dockerfile
│
├── docker-compose.yml           # Local development orchestration
└── README.md                    # Project documentation
```

**Structure Decision**: Multi-service architecture with three independent components communicating via defined interfaces. Rust git-server operates independently, Go API reads from git repositories and subscribes to websocket, Admin UI consumes GraphQL API. This structure supports:
- Independent deployment and scaling of each service
- Language-specific tooling and ecosystems
- Clear separation of concerns (git ops, business logic, UI)
- Parallel development across teams/services

## Complexity Tracking

> **Fill ONLY if Constitution Check has violations that must be justified**

| Violation | Why Needed | Simpler Alternative Rejected Because |
|-----------|------------|-------------------------------------|
| Three-language polyglot architecture (Rust/Go/TypeScript) | Rust required for git server performance & safety (validation on push critical path). Go chosen for GraphQL ecosystem maturity (gqlgen, Relay support). TypeScript/Astro for modern UI DX with React. | Single-language (e.g., all Go) rejected: Rust provides superior git operations performance and memory safety for server-side validation. TypeScript rejected for backend: weak typing compared to Go for API contracts. All JavaScript/TypeScript rejected: insufficient git library ecosystem and validation performance. |
| Three separate services vs monolith | Independent scaling needs (git ops CPU-bound, API I/O-bound, UI static). Language specialization per domain. Deployment independence supports incremental delivery (P1 without P3). | Monolith rejected: Forces single language compromise, couples deployment of git-critical and UI-optional components, limits horizontal scaling, increases blast radius of failures. |
