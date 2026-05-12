# Feature Specification: API Datastore Abstraction with Config-Selected Runtime Backend

**Feature Branch**: `006-api-datastore-abstraction`  
**Created**: 2026-05-08  
**Status**: Closed  
**Input**: GH#100 (initiative) and subtasks GH#101 (go-memdb backend), GH#102 (ScyllaDB backend with migrations)

## Overview

The `gitstore-api` service currently couples its persistence logic directly to a storage implementation. This feature introduces a datastore abstraction layer so the API can switch between storage backends purely through configuration, with no code changes required at runtime. Two initial backends are delivered: an in-memory backend suitable for local development and testing, and a ScyllaDB backend for production workloads.

Inspired by [SpiceDB datastore patterns](https://github.com/authzed/spicedb/tree/main/internal/datastore).

## User Scenarios & Testing *(mandatory)*

### User Story 1 - Select Storage Backend via Config (Priority: P1)

An operator deploying `gitstore-api` wants to choose the storage backend (in-memory or ScyllaDB) by setting a configuration value, without rebuilding or modifying application code.

**Why this priority**: This is the core value of the entire initiative — all other stories depend on the abstraction being in place and backend-switchable.

**Independent Test**: Deploy `gitstore-api` twice with different config values; verify each starts successfully and responds to API calls using the correct backend.

**Acceptance Scenarios**:

1. **Given** `gitstore-api` is configured with the in-memory backend value, **When** the service starts, **Then** it initialises using the in-memory store and serves API requests normally.
2. **Given** `gitstore-api` is configured with the ScyllaDB backend value, **When** the service starts with a reachable ScyllaDB instance, **Then** it initialises using ScyllaDB and serves API requests normally.
3. **Given** `gitstore-api` is started with an unrecognised backend config value, **When** it attempts to boot, **Then** it fails immediately with a clear, actionable error message identifying the invalid value.

---

### User Story 2 - Use In-Memory Backend for Local Development (Priority: P2)

A developer running `gitstore-api` locally wants a zero-dependency storage option so they can work on API features without provisioning an external database.

**Why this priority**: Developer experience and test isolation are high-value; the in-memory backend is the simplest backend and unblocks all local development flows.

**Independent Test**: Start `gitstore-api` with the in-memory config value and execute all CRUD operations through the API; all operations succeed without any external service running.

**Acceptance Scenarios**:

1. **Given** `gitstore-api` uses the in-memory backend, **When** a resource is created via the API, **Then** it can be retrieved, updated, and deleted within the same process lifetime.
2. **Given** `gitstore-api` uses the in-memory backend, **When** the service is restarted, **Then** previously stored data is not present (data is not persisted across restarts — this is expected and documented).
3. **Given** `gitstore-api` runs its contract test suite against the in-memory backend, **When** all CRUD contract tests execute, **Then** all pass.

---

### User Story 3 - Use ScyllaDB Backend with Schema Migrations (Priority: P2)

An operator deploying `gitstore-api` to production wants durable, scalable storage backed by ScyllaDB, with automated schema initialisation and safe schema evolution.

**Why this priority**: This delivers production readiness. The migration mechanism is critical for safe long-term operation.

**Independent Test**: Start `gitstore-api` against a fresh ScyllaDB instance; the schema is created automatically. Restart after an applied migration; startup completes without errors and existing data is preserved.

**Acceptance Scenarios**:

1. **Given** `gitstore-api` is configured for ScyllaDB and a fresh (empty) ScyllaDB instance, **When** the service starts, **Then** the required schema is initialised automatically without manual intervention.
2. **Given** `gitstore-api` starts against a ScyllaDB instance where migrations are already fully applied, **When** it boots, **Then** it detects the up-to-date state, skips migration steps, and starts successfully.
3. **Given** `gitstore-api` starts against a ScyllaDB instance with pending migrations, **When** it boots, **Then** it applies pending migrations, logs each step, and starts successfully.
4. **Given** a ScyllaDB instance is unreachable at startup, **When** `gitstore-api` boots with the ScyllaDB backend configured, **Then** it fails fast with a clear error indicating the connectivity problem.
5. **Given** `gitstore-api` uses the ScyllaDB backend, **When** the full CRUD contract test suite runs against a live ScyllaDB instance, **Then** all tests pass.

---

### User Story 4 - Consistent Behaviour Across Backends (Priority: P3)

A developer writing API features wants the datastore contract to guarantee that the same CRUD operations produce the same observable results regardless of which backend is active.

**Why this priority**: Parity ensures the in-memory backend is a faithful test substitute for ScyllaDB, preventing bugs that only appear in production.

**Independent Test**: Run the same contract test suite against both backends; both pass with equivalent results.

**Acceptance Scenarios**:

1. **Given** the same sequence of create, read, update, and delete operations is executed, **When** run against the in-memory backend versus the ScyllaDB backend, **Then** the observable results (returned data, error types) are equivalent.
2. **Given** an operation violates a datastore invariant (e.g., creating a duplicate record), **When** it is attempted on either backend, **Then** the same category of error is surfaced to the caller.

---

### Edge Cases

- What happens when the backend config value is present but empty (blank string)?
- What happens when ScyllaDB is reachable at startup but loses connectivity mid-operation? The abstraction propagates the error immediately to the caller without internal retry; the caller is responsible for retry/fallback decisions.
- What happens when a migration partially applies before a crash — does the next startup recover or fail safely?
- What happens when two instances of `gitstore-api` start simultaneously against the same ScyllaDB schema (migration race condition)? A distributed lock stored in the migration state table serialises concurrent runs; the second instance waits until the first releases the lock, then proceeds (detecting no pending migrations).
- What happens when the in-memory backend is used in a test and the test does not clean up state between sub-tests?

## Requirements *(mandatory)*

### Functional Requirements

- **FR-001**: The system MUST expose a datastore abstraction (interface/contract) that all persistence paths in `gitstore-api` use instead of direct backend calls.
- **FR-002**: The system MUST select the active datastore backend at boot time based on a configuration value, with no code change required to switch backends.
- **FR-003**: The system MUST support exactly two datastore backends: an in-memory backend and a ScyllaDB backend.
- **FR-004**: The system MUST reject unrecognised datastore configuration values at startup with a clear, actionable error message before accepting any requests.
- **FR-005**: The in-memory backend MUST satisfy all CRUD operations required by the API, with data persisted only for the lifetime of the running process.
- **FR-006**: The in-memory backend MUST include schema/index definitions sufficient to support all required query patterns exposed by the abstraction.
- **FR-007**: The ScyllaDB backend MUST implement the same datastore interface contract as the in-memory backend.
- **FR-007a**: The datastore abstraction MUST NOT include any internal retry or reconnect logic; storage errors MUST be propagated immediately to the caller.
- **FR-008**: The ScyllaDB backend MUST run schema migrations automatically at startup: initialising a fresh schema on first run and applying pending migrations on subsequent runs.
- **FR-008a**: The ScyllaDB migration runner MUST acquire a distributed lock (stored in the migration state table) before applying any migrations, and release it upon completion or failure, to prevent concurrent migration application across multiple instances.
- **FR-009**: The ScyllaDB backend MUST treat a fully up-to-date schema as a no-op at startup (safe to restart without side effects).
- **FR-010**: The ScyllaDB backend MUST log migration progress (each step applied, any skip reason) at startup.
- **FR-011**: The system MUST include contract tests that validate behavioural parity across both backends for all CRUD operations.
- **FR-011a**: The datastore abstraction MUST emit per-operation latency and error-rate metrics at its boundary, labelled with the operation name and active backend type.
- **FR-011b**: The datastore abstraction MUST emit structured log entries on operation errors, including the operation name, backend type, and error detail.
- **FR-012**: Documentation MUST cover datastore configuration options, the extension points for adding future backends, and migration behaviour.
- **FR-013**: The ScyllaDB backend configuration MUST support optional credentials (username and password) and an optional TLS flag; TLS MUST default to disabled when not specified.
- **FR-014**: ScyllaDB credential and TLS settings MUST follow the same configuration conventions established in feature 005 (env/config keys).

### Key Entities

- **Datastore**: The abstraction layer that defines the persistence contract. Represents the set of operations (create, read, update, delete, query) that any backend must support. Has no affinity to a specific storage technology.
- **Datastore Backend**: A concrete implementation of the Datastore contract bound to a specific storage technology (in-memory or ScyllaDB). Selected at runtime via configuration.
- **Migration**: A versioned, ordered schema change applied to the ScyllaDB backend. Each migration has a unique version identifier and is applied exactly once.
- **Migration State**: The record of which migrations have been applied to a given ScyllaDB instance. Used at startup to determine which migrations are pending. Also holds the distributed migration lock used to serialise concurrent startup runs.

## Success Criteria *(mandatory)*

### Measurable Outcomes

- **SC-001**: Switching between the two supported storage backends requires only a configuration change — no code modification, rebuild, or redeployment of different binaries.
- **SC-002**: Starting `gitstore-api` with an invalid datastore config value produces a startup failure within 5 seconds with an error message that names the invalid value and lists valid options.
- **SC-003**: All existing API CRUD operations work correctly with both the in-memory backend and the ScyllaDB backend, as validated by a shared contract test suite that passes for both.
- **SC-004**: A fresh `gitstore-api` deployment against an empty ScyllaDB instance initialises its schema and becomes ready to serve requests without any manual schema setup.
- **SC-005**: Restarting `gitstore-api` against an already-migrated ScyllaDB instance completes startup without errors and without re-applying already-applied migrations.
- **SC-006**: The datastore abstraction is documented sufficiently that a developer can add a third backend without modifying existing backend code (open/closed for extension).
- **SC-007**: Per-operation latency and error-rate metrics are observable for both backends in production, enabling operators to detect storage degradation without access to application logs.

## Assumptions

- The existing `gitstore-api` persistence layer contains direct backend coupling that will be refactored; the interface boundary will be defined based on actual usage patterns in the current codebase.
- ScyllaDB is the chosen production-grade backend; no other distributed or SQL backends are in scope for this initiative.
- The in-memory backend is explicitly not designed for multi-instance or production use; it is a development and test convenience only.
- Migration strategy for ScyllaDB uses forward-only (up) migrations; rollback migrations are out of scope for this initiative.
- The configuration key and valid values (e.g., `memdb`, `scylladb`) will follow the existing config management conventions established in feature 005.
- Contract tests will be run as integration tests; the in-memory backend tests will run in CI without external dependencies, while ScyllaDB contract tests will require a ScyllaDB instance (e.g., via Docker Compose in CI).

## Clarifications

### Session 2026-05-09

- Q: When ScyllaDB loses connectivity mid-operation, should the abstraction retry internally or propagate the error immediately? → A: Immediately propagate the error to the caller; no retry inside the abstraction.
- Q: How should gitstore-api authenticate to ScyllaDB and is TLS required? → A: Credentials (username/password) and TLS flag both configurable; TLS defaults to off.
- Q: How should concurrent migration runs (multiple instances starting simultaneously) be handled? → A: Use a distributed lock stored in the ScyllaDB migration state table to serialise concurrent migration runs.
- Q: What observability signals should the datastore abstraction emit during normal operation? → A: Latency and error-rate metrics emitted per operation at the abstraction boundary; structured logs on errors.

## Dependencies

- Feature 005 (structured configuration management) must be complete and merged, as the datastore backend selection will use its config loading infrastructure.
- A ScyllaDB instance (or compatible emulator) must be available in the CI environment for ScyllaDB backend integration tests.
