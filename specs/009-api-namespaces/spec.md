# Feature Specification: API-Driven Namespace Lifecycle Management

**Feature Branch**: `009-api-namespaces`  
**Created**: 2026-05-15  
**Status**: Closed  
**Input**: User description: "GH#39 contains the details for our next spec. At the gitstore-api level we would introduce the concept of namespaces, gitstore-git-service only needs to know about how to write and read from bare repositories. In a future spec, we would add support for defining namespace via Markdown file with YAML (kubernetes style) frontmatter. In the future we would also add support for gitaly inspired hashed storage for git repositories (the current storage is based on repository names that is not ideal for renames)"

## Clarifications

### Session 2026-05-15

- Q: What is the relationship between namespace tiers? → A: User and organisation namespaces own repositories directly. Enterprise is a meta-layer that organises organisations and does not own repositories directly (mirrors GitHub model).
- Q: What happens when a namespace deletion is attempted while repositories still exist in it? → A: Block deletion; the caller must remove all repositories from the namespace first.
- Q: Who is permitted to create a namespace at each tier? → A: Tier-owner self-service: any authenticated user can create user-space or organisation namespaces; enterprise namespaces require an elevated platform role.
- Q: Is namespace identifier uniqueness global or per-tier? → A: Globally unique across all tiers — the same identifier cannot exist as both a user namespace and an organisation namespace.
- Q: How is enterprise-organisation membership expressed? → A: An organisation namespace may optionally declare a parent enterprise at creation time; reassignment to a different enterprise is out of scope for this spec.
- Q: Where are namespace deletion audit events stored? → A: Out of scope for this spec. A future spec will introduce namespace-as-file storage (Markdown + YAML frontmatter) where git naturally provides the full audit trail for create, update, and delete operations.

## User Scenarios & Testing *(mandatory)*

### User Story 1 - Create a Namespace (Priority: P1)

An authenticated user or platform administrator wants to create a new namespace. For user-space and organisation namespaces, any authenticated user may create one. For enterprise namespaces, only a caller with an elevated platform role may create one. They provide a namespace identifier and tier, and the system validates, persists, and returns the created namespace.

**Why this priority**: Namespace creation is the foundational capability. Without it, no other namespace-scoped operations (repository creation, listing, deletion) are possible.

**Independent Test**: Can be fully tested by submitting a create-namespace request and verifying the namespace is returned in subsequent list/get operations with correct metadata.

**Acceptance Scenarios**:

1. **Given** no namespace named `acme-corp` exists, **When** a permitted caller creates a namespace with identifier `acme-corp`, **Then** the namespace is persisted with audit fields (`created_at`, `created_by`) and returned with a success response.
2. **Given** a namespace named `acme-corp` already exists (regardless of tier), **When** a caller attempts to create another namespace with the same identifier at any tier, **Then** the system returns a deterministic conflict error.
3. **Given** a caller provides an invalid namespace identifier (e.g., contains spaces, exceeds max length, or uses a reserved name), **When** the create request is submitted, **Then** the system returns a descriptive validation error.

---

### User Story 2 - List and Retrieve Namespaces (Priority: P2)

A permitted caller wants to list all existing namespaces or retrieve a specific namespace by identifier, so they can inspect what namespaces are provisioned and their metadata.

**Why this priority**: Listing and retrieval are essential for clients building integrations or administrators verifying the current state of the platform.

**Independent Test**: Can be tested independently by creating one or more namespaces and verifying the list and get responses return correct identifiers and metadata.

**Acceptance Scenarios**:

1. **Given** several namespaces have been created, **When** a caller requests the list of namespaces, **Then** all namespaces are returned with their identifiers and audit metadata.
2. **Given** a namespace with identifier `acme-corp` exists, **When** a caller retrieves it by identifier, **Then** the namespace's full metadata is returned.
3. **Given** no namespace with identifier `unknown-ns` exists, **When** a caller attempts to retrieve it, **Then** the system returns a not-found error.

---

### User Story 3 - Delete a Namespace (Priority: P3)

An authorised caller wants to delete a namespace that is no longer needed. The deletion must be auditable and must reject unauthorised requests.

**Why this priority**: Lifecycle completeness requires deletion. However, creation and listing deliver immediate value for teams onboarding, so deletion is lower priority.

**Independent Test**: Can be tested by creating a namespace, deleting it, and verifying it no longer appears in list or get responses.

**Acceptance Scenarios**:

1. **Given** a namespace `acme-corp` exists with no repositories and the caller is authorised, **When** the caller requests deletion of `acme-corp`, **Then** the namespace is permanently removed.
2. **Given** a namespace `acme-corp` exists with one or more repositories, **When** an authorised caller attempts to delete it, **Then** the system returns an error listing the blocking repositories and the namespace remains intact.
3. **Given** a namespace `acme-corp` exists, **When** an unauthorised caller attempts to delete it, **Then** the system returns a permission-denied error and leaves the namespace intact.
4. **Given** namespace `unknown-ns` does not exist, **When** a caller attempts to delete it, **Then** the system returns a not-found error.

---

### Edge Cases

- What happens when a namespace identifier matches a reserved word (e.g., `admin`, `root`, `system`, `default`)?
- How does the system handle concurrent create requests for the same namespace identifier?
- What happens when a caller attempts to create a namespace with an identifier exceeding the maximum allowed length?
- How does the system respond when listing namespaces and none exist yet?
- Namespace deletion is blocked if any repositories exist within it; the caller receives a descriptive error indicating which repositories must be removed first.

## Requirements *(mandatory)*

### Functional Requirements

- **FR-001**: System MUST allow permitted callers to create a namespace by providing a unique identifier, tier, optional display name, and (for organisation tier only) an optional parent enterprise identifier set at creation time.
- **FR-002**: System MUST validate namespace identifiers for format (alphanumeric with hyphens allowed, no spaces), minimum length of 1, maximum length of 63 characters, and must not be a reserved name.
- **FR-003**: System MUST enforce namespace identifier uniqueness globally across all tiers — the same identifier MUST NOT exist as both a user-space and an organisation namespace. Duplicate create requests MUST return a deterministic conflict response.
- **FR-004**: System MUST persist audit fields for each namespace: `created_at`, `created_by`, `updated_at`, `updated_by`.
- **FR-005**: System MUST allow permitted callers to list all namespaces with their identifiers and audit metadata.
- **FR-006**: System MUST allow permitted callers to retrieve a single namespace by its identifier.
- **FR-007**: System MUST allow authorised callers to delete a namespace. Deletion MUST be blocked if any repositories still exist within the namespace; the system MUST return a descriptive error identifying the blocking repositories.
- **FR-008**: System MUST enforce tier-scoped authorisation for namespace creation: any authenticated user MAY create user-space or organisation namespaces; only callers with an elevated platform role MAY create enterprise namespaces. Deletion of any namespace requires the same elevated role or namespace ownership.
- **FR-009**: System MUST support three namespace tiers with distinct roles: user-space (owns repositories directly), organisation (owns repositories directly), and enterprise (organises organisations, does not own repositories directly).
- **FR-010**: System MUST ensure namespaces provide full repository isolation — repositories in one namespace are not accessible via another namespace's context.
- **FR-011**: `gitstore-git-service` MUST expose only bare repository read/write capabilities; namespace logic MUST reside exclusively in `gitstore-api`.
- **FR-012**: Integration tests MUST cover create, duplicate-create, list, get, delete, and invalid-input scenarios.
- **FR-013**: API contract for namespace operations MUST be documented with request/response examples in `docs/`.

### Key Entities

- **Namespace**: The primary isolation boundary. Attributes: unique identifier (globally unique across all tiers), display name (optional), tier (user/org/enterprise), parent enterprise identifier (optional, applies to organisation tier only, set at creation time), `created_at`, `created_by`, `updated_at`, `updated_by`.
- **Namespace Tier**: Enumeration of `user`, `organisation`, `enterprise`. User and organisation namespaces own repositories directly. Enterprise namespaces organise organisations and do not own repositories directly.
- **Audit Event**: Out of scope for this spec. A future spec introducing namespace-as-file storage (Markdown + YAML frontmatter) will leverage git history as the audit trail for namespace lifecycle events.

## Success Criteria *(mandatory)*

### Measurable Outcomes

- **SC-001**: A permitted caller can create a new namespace in under 1 second under normal load.
- **SC-002**: Duplicate create requests consistently return a conflict response (100% deterministic, not flaky).
- **SC-003**: Namespace list and get operations return accurate results for all existing namespaces with no omissions.
- **SC-004**: Namespace deletion consistently returns success when the namespace is empty and the caller is authorised (100% deterministic).
- **SC-005**: Unauthorised callers are rejected 100% of the time for create and delete operations.
- **SC-006**: Integration test suite achieves full coverage of the acceptance scenarios defined in this spec, running to completion in under 2 minutes.

## Scope & Boundaries

**In scope**:
- Namespace create, list, get, and delete via API.
- Namespace identifier validation (format, uniqueness, reserved names).
- Three namespace tiers: user, organisation, enterprise.
- Audit fields on namespace records.
- Simple authorisation enforcement for create/delete.
- Integration tests and API documentation.

**Out of scope (future specs)**:
- Declarative namespace creation via Markdown files with YAML frontmatter (tracked in GH#40).
- Gitaly-inspired hashed storage for bare repositories (current name-based storage is unchanged in this spec).
- Cross-namespace repository sharing or federation.
- Reassignment of an organisation namespace from one enterprise to another.
- Namespace deletion auditing (deferred to a future spec that will leverage git history once namespace-as-file storage is introduced).
- Full enterprise IAM integration beyond current authorisation capabilities (tracked in GH#44, #45, #50).
- UI workflows for namespace management.

## Assumptions

- The current authorisation model in `gitstore-api` is sufficient to express "permitted to create/delete namespaces" without requiring a new IAM subsystem.
- Namespace identifiers follow DNS label conventions (alphanumeric + hyphens, max 63 chars) — a widely understood standard requiring no additional specification.
- `gitstore-git-service` already supports bare repository read/write; no changes to its storage layout are required by this spec.
- The datastore abstraction introduced in feature 006 is available and supports the persistence of namespace records.

## Dependencies

- Feature `006-api-datastore-abstraction`: datastore layer in `gitstore-api` for persisting namespace records.
- GH#39 (Initiative: Namespaces) and GH#119 (API-Driven Namespace Creation and Lifecycle) — parent initiative.
- GH#67 — supported by this feature (repository creation scoped to a namespace).
