# Feature Specification: Pre-Receive Validation End-to-End

**Feature Branch**: `020-pre-receive-validation-e2e`
**Created**: 2026-06-06
**Status**: Closed
**Depends on**: 018-hook-pipeline-wiring, 019-fix-upload-pack

## Overview

The hook pipeline wiring (spec#018) introduced `SchemaValidationHandler` (pre-receive, blocking) and
`AdmissionControlHandler` (post-receive, fire-and-forget). Both handlers connect to the `CatalogService`
gRPC endpoint in `gitstore-api`. However, the full integration test suite
(`TestProductLifecycle_*` and `TestDocumentationExamples_ParseCorrectly`) still fails in CI:

- Invalid product pushes (bad title, status key, missing `fileRef.name`) are accepted instead of
  rejected.
- Valid product pushes are accepted but the product is never queryable via GraphQL afterwards.

Root cause: the `SchemaValidationHandler` and `AdmissionControlHandler` fall back to noop handlers
when the `CatalogService` gRPC endpoint is unreachable at startup, and CI does not guarantee
the `api` container's port 6000 is ready before the `git-service` starts. The end result is that
the real handlers are never wired for the CI integration test run.

This spec closes the end-to-end gap so that all six `TestProductLifecycle_*` and all four
`TestDocumentationExamples_ParseCorrectly` sub-tests pass reliably in CI against **both** the
in-memory datastore (memdb) and ScyllaDB.

## User Scenarios & Testing *(mandatory)*

### User Story 1 — Invalid product push is blocked at the gate (Priority: P1)

A developer pushes a commit containing a product file that violates the catalog schema (e.g.
`spec.title` exceeds 200 characters, a `status` key is present, or a media entry is missing
`fileRef.name`). The push must be rejected before any ref is committed, with a human-readable
error message that names the violating field and constraint.

**Why this priority**: This is the primary safety guarantee of the hook pipeline. Without it,
invalid catalog entries can be stored and corrupt downstream consumers.

**Independent Test**: Covered by `TestProductLifecycle_InvalidTitle_PushRejected`,
`TestProductLifecycle_StatusPresent_PushRejected`, `TestProductLifecycle_MissingFileRefName_PushRejected`,
and the three failing `TestDocumentationExamples_ParseCorrectly` sub-tests.

**Acceptance Scenarios**:

1. **Given** the git service and API are running and healthy, **When** a developer pushes a commit with `spec.title` longer than 200 characters, **Then** the push is rejected with an error message containing `spec.title` and `200`.
2. **Given** the above, **When** a developer pushes a commit with a `status` key in the product YAML, **Then** the push is rejected with an error message containing `status` and `system-managed`.
3. **Given** the above, **When** a developer pushes a commit with a media entry that has no `fileRef.name`, **Then** the push is rejected with an error message referencing `fileRef` or `fileRef.name`.
4. **Given** the above, **When** a developer pushes a structurally valid product file, **Then** the push succeeds.

---

### User Story 2 — Valid product push is stored and queryable (Priority: P1)

A developer pushes a commit containing a well-formed product file. Within a short window after the
push completes, the product must be queryable via the GraphQL API.

**Why this priority**: This is the happy-path contract that makes the git-backed catalog useful.
Without it, pushes appear to succeed but the catalog is never updated.

**Independent Test**: Covered by `TestProductLifecycle_ValidFile_AcceptedAndQueryable` and
`TestProductLifecycle_StatusHydration`.

**Acceptance Scenarios**:

1. **Given** a healthy stack, **When** a developer pushes a valid product file to the `main` branch, **Then** the push succeeds and the product is queryable via `product(by: {namespacePath: ...})` within 1 second.
2. **Given** the above, **When** the pushed product is queried, **Then** `spec.title` and `spec.tags` match the pushed values.
3. **Given** the above, **When** the product is queried for status, **Then** a `status` field is present (either null before reconciliation or an object with an `AdmissionAccepted: True` condition).

---

### User Story 3 — Documentation example files are validated on push (Priority: P2)

The example files under `docs/products/examples/` serve as living documentation for what the catalog
schema accepts and rejects. Pushing them through the hook pipeline confirms that documented
examples stay in sync with the enforced rules.

**Why this priority**: Keeps documentation honest and catches schema drift automatically.

**Independent Test**: Covered by `TestDocumentationExamples_ParseCorrectly` — `valid-product.md`
must be accepted; `invalid-status.md`, `invalid-title.md`, and `invalid-media.md` must be rejected
with the expected field name in the error.

**Acceptance Scenarios**:

1. **Given** a healthy stack, **When** `docs/products/examples/valid-product.md` is pushed, **Then** the push succeeds.
2. **Given** the above, **When** any of the three `invalid-*.md` examples are pushed, **Then** the push is rejected with an error containing the expected violation keyword (`system-managed`, `200`, or `fileref`).

---

### Edge Cases

- What happens when the `CatalogService` gRPC endpoint is unreachable after the git service has started? The git service must not degrade permanently — subsequent pushes after the API recovers must be validated normally.
- What happens when the git service starts before the API's gRPC port is ready? The git service must tolerate this and reconnect before the first push arrives.
- What happens when a push contains both valid and invalid product files in the same commit? All violations across all blobs must be collected and the entire push rejected (all-or-nothing for pre-receive).
- What happens when the validation call times out? The push must be rejected (fail-closed) with a clear timeout message.

## Requirements *(mandatory)*

### Functional Requirements

- **FR-001**: The CI integration test suite MUST pass all six `TestProductLifecycle_*` tests and all four `TestDocumentationExamples_ParseCorrectly` sub-tests reliably across consecutive runs against **both** the in-memory datastore backend and ScyllaDB.
- **FR-002**: The git service MUST connect to the `CatalogService` endpoint lazily and retry connections automatically after transient failures, so that a startup ordering race between the git service and the API does not permanently disable the hook pipeline.
- **FR-003**: The git service MUST NOT permanently fall back to a noop `SchemaValidationHandler` when an initial connection attempt fails — it MUST use the real handler for every push and surface connection errors per-push if the endpoint is unreachable at push time.
- **FR-004**: A push that fails pre-receive validation MUST be rejected with a human-readable error message naming the violating field and constraint, sourced from the `ValidateResources` response.
- **FR-005**: A push that passes pre-receive validation MUST trigger the post-receive `AdmitResources` call for the configured branch pattern, and the resulting product MUST be queryable via GraphQL within 1 second of the push completing.
- **FR-006**: The CI workflow MUST ensure the `CatalogService` gRPC port is accepting connections before the first test push is attempted.
- **FR-007**: All validation violations across all product blobs in a single push commit MUST be collected and reported in a single rejection message.
- **FR-008**: If a `ValidateResources` call exceeds the configured timeout, the push MUST be rejected with a message indicating that the validation service was unavailable (fail-closed).

### Key Entities

- **SchemaValidationHandler**: Blocking pre-receive component in the git service that calls `CatalogService.ValidateResources`; must use a lazy-connecting gRPC channel.
- **AdmissionControlHandler**: Post-receive fire-and-forget component that calls `CatalogService.AdmitResources` for matching branches.
- **CatalogService gRPC endpoint**: Hosted by `gitstore-api` on port 6000; must be reachable before integration tests begin pushing.
- **Integration test bootstrap**: CI setup that creates the test namespace/repository, seeds an initial commit, and waits for gRPC readiness before running tests.

## Success Criteria *(mandatory)*

### Measurable Outcomes

- **SC-001**: All 10 product-lifecycle and documentation-example integration tests pass in CI without flakiness across 5 consecutive runs against both the memdb and ScyllaDB datastore backends.
- **SC-002**: Invalid product pushes are rejected within the `git push` round-trip — the developer receives the field-scoped error message in standard `git push` output with no post-push cleanup required.
- **SC-003**: Valid product pushes result in a queryable catalog entry within 1 second of `git push` completing.
- **SC-004**: The git service starts and accepts pushes even when the API container is not yet listening on its gRPC port at the moment the git service process starts.
- **SC-005**: A temporary API restart does not permanently disable schema validation — the push immediately after API recovery is validated normally.

## Assumptions

- The `CatalogService` proto contract (already generated and committed) is stable for this spec; no schema changes are required.
- Both `SchemaValidationHandler` and `AdmissionControlHandler` already use `connect_lazy()` internally — no Rust service changes are needed to achieve startup-order resilience.
- The noop fallback in `main.rs` can only trigger if the `catalog_service.uri` config value is a malformed URL; it is not triggered by a temporarily unreachable server.
- The `admission_control.branch_pattern` (`refs/heads/main`) and `schema_validation.phase` (`pre-receive`) defaults are correct for CI.
- The 500 ms sleep in `TestProductLifecycle_ValidFile_AcceptedAndQueryable` and `TestProductLifecycle_StatusHydration` is sufficient for the fire-and-forget admission call under normal CI conditions.
- `compose.scylla.yml` already sets `GITSTORE_DATASTORE__BACKEND=scylla` and wires the API to the ScyllaDB cluster; no new compose changes are needed.
- The existing `gitstore` keyspace in the ScyllaDB overlay is sufficient for integration tests (the `scylla-init` service creates it at stack startup).
- No changes to the `CatalogService` proto, GraphQL schema, or any Rust/Go service code are required.

## Dependencies

- spec#018 — hook pipeline wiring (closed)
- spec#019 — upload-pack fix (closed)
- `docs/products/examples/` — must contain `valid-product.md`, `invalid-status.md`, `invalid-title.md`, `invalid-media.md` (confirmed present)
