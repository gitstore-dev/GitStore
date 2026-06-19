# Feature Specification: Namespace Types — Remove Enterprise

**Feature Branch**: `030-remove-enterprise-namespace`  
**Created**: 2026-06-19  
**Status**: Closed  
**Input**: gitstore-dev/GitStore#246 — Remove `enterprise` as a supported Namespace type and define namespace ownership as either `user` or `organization`.

## Overview

GitStore repository paths model the common two-tier namespace shape: `/{user}/{repository}` and `/{organization}/{repository}`. The `enterprise` type has been present in the Namespace type contract but does not map to any supported path shape — GitHub models enterprises at a separate top-level path (`/enterprises/{name}`) that is outside the namespace hierarchy. This feature removes `enterprise` from the namespace type contract entirely, leaving `user` and `organization` as the only valid namespace types.

## User Scenarios & Testing *(mandatory)*

### User Story 1 — API Contract Enforces Two-Type Namespace Model (Priority: P1)

An API client (developer, integration, CLI tool) that creates or queries namespaces receives a consistent, two-value type contract. Attempting to create a namespace with type `enterprise` is rejected with a clear error. Existing `user` and `organization` namespaces continue to work without disruption.

**Why this priority**: This is the core behavioural change. Until the API contract is updated, any client can still submit `enterprise` as a valid type, undermining the model. Resolving this first delivers the entire business value.

**Independent Test**: Send a namespace creation request with each of the three values (`user`, `organization`, `enterprise`) and verify that the first two succeed and the third is rejected with a descriptive validation error.

**Acceptance Scenarios**:

1. **Given** a valid namespace creation request with type `user`, **When** the request is submitted, **Then** the namespace is created successfully and returned with type `user`.
2. **Given** a valid namespace creation request with type `organization`, **When** the request is submitted, **Then** the namespace is created successfully and returned with type `organization`.
3. **Given** a namespace creation request with type `enterprise`, **When** the request is submitted, **Then** the request is rejected with a validation error that states `enterprise` is not a valid namespace type.
4. **Given** an existing `user` or `organization` namespace, **When** the namespace is retrieved, **Then** it is returned correctly with no change to its type or associated repositories.

---

### User Story 2 — Schema and Documentation Reflect Two-Type Model (Priority: P2)

A developer reading the API schema, generated types, or documentation sees exactly two namespace types — `user` and `organization` — with no mention of `enterprise`. Documentation explicitly notes that enterprise-level modeling, if ever required, belongs outside the namespace path model.

**Why this priority**: Schema and documentation consistency prevents new integrations from accidentally using `enterprise` and removes ambiguity for consumers. This story can be validated independently of runtime enforcement.

**Independent Test**: Inspect the published schema/type definitions and documentation to confirm `enterprise` is absent and only `user` and `organization` appear as valid values.

**Acceptance Scenarios**:

1. **Given** the published namespace type schema, **When** a developer inspects the valid enum values, **Then** only `user` and `organization` are listed.
2. **Given** the namespace documentation, **When** a developer reads about enterprise support, **Then** they find a clear statement that enterprise modeling belongs outside the namespace type enum.
3. **Given** any generated type definitions or client code, **When** they reference the namespace type, **Then** no `enterprise` value is present.

---

### User Story 3 — Test Suite Protects Against Regression (Priority: P3)

Automated tests confirm that `enterprise` cannot be re-introduced as a valid namespace type and that all existing test data uses only `user` or `organization`.

**Why this priority**: Regression protection ensures the contract stays clean as the codebase evolves. This story can be tested independently by running the test suite.

**Independent Test**: Run the full test suite and verify that all tests pass and that no fixture or test data references `enterprise` as a namespace type.

**Acceptance Scenarios**:

1. **Given** the updated test fixtures, **When** the test suite runs, **Then** no fixture references `enterprise` as a namespace type and all tests pass.
2. **Given** a dedicated regression test, **When** a namespace creation request with type `enterprise` is submitted, **Then** the test asserts the request is rejected with a validation error.
3. **Given** a future code change that re-adds `enterprise` to the namespace type enum, **When** the regression test runs, **Then** the test fails, alerting developers to the unintended change.

---

### Edge Cases

- What happens when an API client submits `enterprise` as the namespace type after this change? → The request MUST be rejected with a clear, human-readable validation error identifying `enterprise` as invalid.
- What if test fixtures contain `enterprise` namespace types? → All fixtures MUST be updated to use `user` or `organization`; any fixture that cannot be updated MUST be removed.
- What if a partial rollout leaves the schema updated but validation not yet enforced? → Both changes MUST land together in a single atomic update to avoid a window where the schema says one thing and the runtime another.
- Could existing stored namespaces have type `enterprise`? → Assumption: no production data contains `enterprise` namespace types (see Assumptions). If this assumption is false, a separate data migration must precede this change.

## Requirements *(mandatory)*

### Functional Requirements

- **FR-001**: The namespace type field MUST only accept `user` and `organization` as valid values; all other values MUST be rejected.
- **FR-002**: Any request to create a namespace with type `enterprise` MUST be rejected with a descriptive validation error before any persistence occurs.
- **FR-003**: The canonical namespace type enumeration and all API contracts MUST list exactly two values: `user` and `organization`.
- **FR-004**: All generated type definitions derived from the schema MUST reflect only `user` and `organization`; no `enterprise` value may appear.
- **FR-005**: All test fixtures and test data MUST be updated to replace any `enterprise` namespace type references with `user` or `organization` as appropriate.
- **FR-006**: A dedicated regression test MUST assert that submitting `enterprise` as a namespace type results in a validation error.
- **FR-007**: The `/{namespace}/{repository}` path structure MUST continue to function correctly for both `user` and `organization` namespace types with no behavioural change.
- **FR-008**: Namespace documentation and inline schema descriptions MUST state that enterprise-level modeling, if needed in the future, belongs outside the namespace path model.

### Key Entities

- **Namespace**: A first-level container that owns repositories. Identified by a unique name and a type — now strictly `user` or `organization`. Namespaces form the first segment of the repository path (`/{namespace}/{repository}`).
- **Namespace Type**: The classification of a namespace. Previously `user`, `organization`, or `enterprise`; after this change, exactly `user` or `organization`.

## Success Criteria *(mandatory)*

### Measurable Outcomes

- **SC-001**: 100% of requests that specify `enterprise` as a namespace type are rejected with a validation error — zero are accepted.
- **SC-002**: All automated tests pass after the change with zero remaining references to `enterprise` as a namespace type in fixtures or test data.
- **SC-003**: The namespace type contract — schema, generated types, and documentation — is consistent across all artefacts: each lists exactly two values (`user`, `organization`) with no discrepancies.
- **SC-004**: No regression in namespace creation or retrieval for `user` and `organization` namespaces — all existing passing tests continue to pass.

## Assumptions

- No production or staging data currently contains namespaces with type `enterprise`. If such records exist, a data migration must be scoped and completed before this change is deployed.
- The `enterprise` type has never been publicly documented or shipped in a stable API release, so removing it is not a breaking change for external consumers.
- Future enterprise-level modeling (if ever required) will be designed as a separate top-level resource outside the namespace path, consistent with the GitHub model (`/enterprises/{name}`).
- The scope is limited to removing `enterprise` from the type enum and contract. No new namespace types are introduced in this feature.

## Out of Scope

- Designing or implementing an enterprise resource or path model.
- Migrating any existing enterprise namespace records (tracked separately if needed).
- Changing the behaviour of `user` or `organization` namespaces in any way.
- Adding new namespace types beyond `user` and `organization`.
