<!--
Sync Impact Report:
- Version change: UNINITIALIZED → 1.0.0
- Initial ratification of GitStore Constitution
- Principles defined: 7 core principles (Test-First, API-First, Clear Contracts, Observability, User Story Driven, Incremental Delivery, Simplicity)
- Added sections: Architecture Constraints, Development Workflow
- Templates requiring updates: ✅ All templates already aligned with these principles
- Follow-up TODOs: None - all placeholders filled
-->

# GitStore Constitution

## Core Principles

### I. Test-First Development (NON-NEGOTIABLE)

**Tests MUST be written before implementation code.** All user stories require contract tests and integration tests written first, verified to fail, then implementation follows (Red-Green-Refactor cycle). No implementation task may begin until its corresponding test task is complete and failing. This is strictly enforced and cannot be bypassed.

**Rationale:** Test-first development ensures requirements are testable, prevents scope creep, documents expected behavior, and catches regressions early. For a multi-service architecture with git validation on the critical path, test-first is essential to prevent data corruption and maintain system integrity.

### II. API-First Design

**All service boundaries MUST define contracts before implementation.** GraphQL schemas, API interfaces, and service contracts are defined, reviewed, and version-controlled before any resolver or handler code is written. Contracts serve as the single source of truth for service communication.

**Rationale:** In a polyglot architecture (Rust/Go/TypeScript), clear contracts prevent integration issues, enable parallel development across services, and provide type safety boundaries. Contract-first development allows frontend and backend teams to work independently.

### III. Clear Contracts & Versioning

**All public interfaces MUST follow semantic versioning.** Breaking changes require MAJOR version bump, new features MINOR, bug fixes PATCH. GraphQL schema changes follow schema evolution principles (additive changes preferred, deprecation before removal). Release tags MUST use semantic versioning (v1.0.0) or date-based tags (YYYY-MM-DD).

**Rationale:** Git-backed catalog management requires stable versioning for rollback capabilities. Storefronts depend on consistent API contracts. Clear versioning prevents breaking changes from reaching production and enables safe rollback to previous catalog versions.

### IV. Observability & Debuggability

**Structured logging MUST be implemented in all services.** Every service (git-server, API, admin-ui) requires structured logging with consistent format, request ID propagation across service boundaries, and error context capture. All validation failures, git operations, and catalog updates MUST be logged with sufficient detail for debugging.

**Rationale:** Multi-service architecture requires distributed tracing capabilities. Git operations and validation errors must be auditable. Request IDs enable end-to-end transaction tracking from admin UI → GraphQL API → git server.

### V. User Story Driven Development

**All work MUST map to user stories with independent test criteria.** Features are organized by user story (P1, P2, P3) with each story independently completable and testable. Tasks include [Story] labels (US1, US2, US3) for traceability. No speculative features outside defined user stories.

**Rationale:** User story organization enables incremental delivery, parallel development, and clear acceptance criteria. Each story delivers measurable user value and can be deployed independently without breaking other stories.

### VI. Incremental Delivery

**MVP MUST deliver minimal viable user story (P1).** Development follows P1 (git workflow) → P2 (categories/collections) → P3 (admin UI) priority order. Each user story adds value without requiring subsequent stories. Features can be deployed incrementally with P1 providing core functionality.

**Rationale:** GitStore's git-based approach provides value even without admin UI (P3). Technical users and AI agents can use the system with just P1. Incremental delivery reduces risk, enables earlier feedback, and allows prioritization adjustments based on user feedback.

### VII. Simplicity & YAGNI (You Aren't Gonna Need It)

**Start simple, justify complexity.** No speculative features, premature abstractions, or multi-tenant capabilities until proven necessary. Single admin user model initially (RBAC deferred). In-memory caching preferred over external dependencies. Architecture complexity (polyglot Rust/Go/TypeScript) MUST be justified with clear technical rationale.

**Rationale:** Complexity is a liability. Every dependency, abstraction, and feature adds maintenance burden. GitStore's core value proposition (git-backed catalogs) should not be obscured by premature optimization or speculative features.

## Architecture Constraints

### Multi-Service Architecture

GitStore comprises three independent services:

1. **Git Server (Rust)**: Built-in git engine with pre-push validation and websocket notifications
2. **GraphQL API (Go)**: Relay-compliant API layer exposing catalog data with in-memory caching
3. **Admin UI (Astro/React)**: Optional web interface for non-technical users

**Justification for Polyglot Architecture:**
- Rust provides superior performance and memory safety for git operations and validation (critical path)
- Go offers mature GraphQL ecosystem (gqlgen, Relay support) with strong typing for API contracts
- TypeScript/Astro provides modern UI development experience with React component ecosystem

**Alternatives Rejected:**
- Single-language monolith would compromise either git performance (ruling out Go/TypeScript) or GraphQL ecosystem maturity (ruling out Rust)
- All-JavaScript approach lacks sufficient git library ecosystem and validation performance

### Performance Targets

- Storefront catalog queries: < 500ms for 1000+ products
- Storefront update latency: < 30 seconds from release tag creation
- Git push validation: < 5 seconds for 100 file push
- Websocket notification delivery: < 100ms from tag creation

### Scale Constraints

- Product catalog size: up to 10,000 products initially
- Git repository size: < 500MB for markdown + metadata
- Websocket connections: up to 100 concurrent storefront subscribers
- Concurrent admin UI users: 1-5 initially (single admin user authentication)

## Development Workflow

### Test-First Workflow (Enforced)

1. Write contract tests for GraphQL schema operations
2. Write integration tests for cross-service interactions
3. Verify all tests FAIL (red)
4. Implement minimal code to pass tests (green)
5. Refactor while maintaining test passing state
6. Commit with tests included in same commit

### Task Execution Order

1. **Setup Phase**: Initialize project structure and dependencies
2. **Foundational Phase**: Core infrastructure (BLOCKS all user stories)
3. **User Story Phases**: Implement P1 → P2 → P3 in priority order
4. **Polish Phase**: Cross-cutting concerns after story completion

### Quality Gates

- All tests passing before commit
- No commented-out code except with TODO(reason) and issue link
- Structured logging implemented for all new endpoints/operations
- GraphQL schema changes reviewed for breaking changes
- Constitution compliance verified in PR review

## Governance

### Authority

This constitution supersedes all other development practices, conventions, and preferences. When team practices conflict with constitution principles, the constitution takes precedence.

### Amendment Process

1. Propose amendment with clear rationale and examples
2. Document impact on existing code and templates
3. Require team consensus for MAJOR version changes
4. Update all dependent templates (plan-template.md, spec-template.md, tasks-template.md)
5. Update constitution version following semantic versioning

### Compliance Review

- All PRs MUST include constitution compliance verification
- Complexity violations (Principle VII) MUST be justified in PR description
- Test-first violations (Principle I) result in automatic PR rejection
- Constitution violations can only be overridden by team consensus with documented justification

### Version Control

- Constitution changes MUST increment version number
- MAJOR: Principle removal, redefinition, or backward-incompatible governance change
- MINOR: New principle added or existing principle materially expanded
- PATCH: Clarifications, wording improvements, typo fixes

### Runtime Guidance

For day-to-day development guidance and agent-specific instructions, refer to agent context files (e.g., `.claude/CLAUDE.md`) which supplement but do not override constitution principles.

**Version**: 1.0.0 | **Ratified**: 2026-03-09 | **Last Amended**: 2026-03-09
