# Feature Specification: Decouple API from Git Storage via gRPC Git Service

**Feature Branch**: `004-grpc-git-service`
**Created**: 2026-05-06
**Status**: Closed
**Input**: User description: "GH#65 — Replace filesystem-level coupling between gitstore-api and gitstore-git-service with a gRPC contract so all Git read/write operations are executed by gitstore-git-service. Supersedes User Story 2 (Operator Deploys Multiple API Instances) from specs/002-production-readiness and T152."

## Clarifications

### Session 2026-05-06

- Q: Does the git-service contract operate on raw git primitives (file bytes, git metadata) or structured catalogue entities? → A: Raw primitives — git-service returns raw file bytes and git metadata; the API owns all catalogue parsing and validation (handled by GH#105)
- Q: How are breaking contract changes managed — atomic co-deployment or independent rolling upgrades? → A: Contract carries a version identifier; both services must be deployed atomically on breaking changes (no independent rolling upgrades)
- Q: Are gRPC call metrics (per-RPC-method request count, latency histogram, error rate) in scope for this feature? → A: Yes — both API and git-service must expose per-RPC-method metrics via their existing Prometheus endpoints

## Background

The API currently reads the catalogue repository from a shared filesystem volume and uses an embedded git client library for mutations. This prevents horizontal scaling (multiple API replicas cannot share a single local working directory safely) and blurs the service boundary between the API and git-service. GH#65 establishes a gRPC service contract so that every git read and write the API performs goes through git-service, eliminating direct repository access from the API process entirely.

This supersedes the simpler network-clone approach described in `specs/002-production-readiness` User Story 2 / T152. That approach would have reduced shared-volume coupling at startup but left mutation paths and the service boundary unchanged. The gRPC contract addresses both concerns in one migration.

## User Scenarios & Testing *(mandatory)*

### User Story 1 - Operator Scales API Without Shared Storage (Priority: P1)

An operator running GitStore on Kubernetes needs to start multiple API replicas without providing a shared git volume. Each replica must independently serve the full catalogue.

**Why this priority**: The shared volume is the primary blocker for production deployments. Without it, operators can run GitStore on any container orchestration platform and scale the API tier horizontally.

**Independent Test**: Can be fully tested by removing the shared volume mount from the API service configuration, starting three API replicas pointing at a single git-service instance, pushing a product catalogue, and verifying all three replicas serve identical catalogue data.

**Acceptance Scenarios**:

1. **Given** an API configured to communicate with git-service via the service contract with no shared volume, **When** the API starts, **Then** it successfully loads the catalogue and begins accepting requests
2. **Given** three API replicas running against a single git-service, **When** a release tag is pushed, **Then** all three replicas independently receive the update notification and reload to reflect the new catalogue state
3. **Given** an API that has been running for an extended period without a shared volume, **When** an operator inspects the API process, **Then** no git repository directory is present on the API host filesystem

---

### User Story 2 - Developer Performs Write Mutations Through Service Boundary (Priority: P2)

A developer submitting write mutations (create, update, or delete catalogue entities) needs those operations to execute safely and concurrently through the git-service, without the API holding any local git working directory state.

**Why this priority**: Eliminating direct git manipulation from the API removes a class of concurrency bugs (concurrent mutations corrupting a shared working directory) and enforces the service boundary that makes each component independently deployable.

**Independent Test**: Can be fully tested by submitting ten simultaneous product-creation mutations against the API, then verifying that all ten products appear in the next released catalogue with no conflicts, errors, or orphaned state left on disk.

**Acceptance Scenarios**:

1. **Given** two concurrent `createProduct` mutations, **When** both reach the API simultaneously, **Then** both are forwarded to git-service, complete without conflict, and both products appear in the next catalogue release
2. **Given** a `deleteProduct` mutation that encounters an error during the operation, **When** git-service returns the error, **Then** the API surfaces the error to the caller and no partial state is committed to the repository
3. **Given** ten consecutive write mutations of mixed types, **When** all complete, **Then** no temporary or working-directory artefacts remain on the API host

---

### User Story 3 - Operator Retains Real-Time Catalogue Update Propagation (Priority: P3)

An operator who relies on release-tag-triggered catalogue reloads needs the existing websocket notification mechanism to continue working after the API no longer holds a local git working directory.

**Why this priority**: Operators have built deployment workflows around the push-to-reload behaviour. This must remain unaffected by the service boundary change, even though the underlying data-fetch mechanism changes.

**Independent Test**: Can be fully tested by pushing a release tag, observing the websocket notification arrive at each API instance, and confirming each instance fetches updated catalogue data from git-service and serves the new content within 30 seconds — with no shared volume present.

**Acceptance Scenarios**:

1. **Given** a running API instance with no shared volume, **When** a release tag is pushed and the git-service emits a websocket notification, **Then** the API fetches the updated catalogue via the service contract and begins serving the new content
2. **Given** a git-service that is temporarily unreachable when a notification arrives, **When** git-service becomes available again, **Then** the API retries fetching and reloads the catalogue without requiring a restart
3. **Given** multiple API replicas each receiving the same notification, **When** all replicas reload concurrently, **Then** no duplicate commits or conflicts are introduced into the repository

---

### User Story 4 - Developer Validates the Service Contract with Automated Tests (Priority: P4)

A developer adding a new git operation to the API needs integration tests that validate both sides of the service contract against a real git-service instance, providing confidence that changes to either side do not silently break the other.

**Why this priority**: Without contract tests the two services can drift independently. This user story ensures the service boundary is protected in CI from day one of the migration.

**Independent Test**: Can be fully tested by running the integration test suite in CI with a real git-service instance and verifying all read and write paths pass, including error and concurrency scenarios.

**Acceptance Scenarios**:

1. **Given** a CI pipeline running the integration test suite, **When** all tests pass, **Then** the suite covers at least: catalogue read, entity write (create/update/delete), tag push notification, and error path handling
2. **Given** a breaking change introduced to the git-service contract, **When** the integration test suite runs, **Then** at least one test fails and the breakage is surfaced before merge
3. **Given** a new git operation added to the API, **When** the developer writes a corresponding integration test, **Then** the test framework supports running it against a real git-service without manual setup

---

### Edge Cases

- **Git-service unavailable at API startup**: When the API starts but cannot reach git-service, it retries with exponential backoff and does not accept requests until the initial catalogue load succeeds
- **Notification arrives during in-flight mutation**: When a websocket notification arrives while a write mutation is in progress, the reload is queued and applied after the mutation completes
- **Concurrent catalogue reloads**: When multiple notifications arrive in rapid succession, the API coalesces them and performs a single reload rather than running parallel reloads
- **Large catalogue repositories**: Fetching catalogue data for very large repositories must not time out under default service contract call limits; the contract must support streaming or paginated responses for file listing

## Requirements *(mandatory)*

### Functional Requirements

- **FR-001**: The git-service MUST expose a versioned service contract covering: repository content read (file listing, raw file bytes retrieval), write operations (raw file commit, tag creation), and branch/tag enumeration. The contract operates on raw git objects and file bytes only — catalogue entity parsing and validation are the API's responsibility. The contract MUST carry an explicit version identifier; breaking changes require both services to be deployed atomically
- **FR-002**: The API MUST perform all git read operations (catalogue load, file retrieval, history inspection) exclusively through the git-service contract, with no direct repository filesystem access
- **FR-003**: The API MUST perform all git write operations (entity creation, update, deletion) exclusively through the git-service contract
- **FR-004**: The API MUST NOT require a git repository directory to be mounted or accessible on its host filesystem for any runtime operation
- **FR-005**: The API MUST NOT directly invoke a git client library for repository mutation or catalogue read paths
- **FR-006**: The API MUST continue to receive websocket notifications from git-service for release tag events and MUST fetch updated catalogue data via the service contract in response
- **FR-007**: The service contract implementation MUST support concurrent read and write operations without data corruption or ordering violations
- **FR-008**: The API-to-git-service integration MUST be covered by automated tests that run against a real git-service instance in CI, covering both read and write paths including error and concurrency scenarios
- **FR-009**: The API service container configuration MUST remove the shared git repository volume mount (`${GITSTORE_DATA_DIR:-git-data}:/data/repos:ro`); the API MUST function correctly with no volume mount to the git repository
- **FR-010**: Architecture and developer documentation MUST be updated to reflect the new service boundaries, runtime configuration, and local development setup
- **FR-011**: Both the API and git-service MUST expose per-RPC-method metrics via their existing Prometheus endpoints, covering: request count, latency histogram, and error rate per contract method

### Key Entities

- **Git Service Contract**: The defined interface of operations the git-service exposes and the API consumes — encompassing read operations (raw file bytes, directory listing, branch/tag enumeration) and write operations (raw file commit, tag creation). The contract is git-primitive only; catalogue entity parsing and validation live in the API (see GH#105)
- **API Git Client**: The API-side component that communicates with git-service via the defined contract, replacing all direct git library usage; responsible for connection management, retries, and error mapping
- **Catalogue Update Handler**: The API component that, upon receiving a websocket release tag notification, triggers a catalogue reload by fetching current state from git-service via the contract rather than pulling from a local working directory

## Success Criteria *(mandatory)*

### Measurable Outcomes

- **SC-001**: Three API instances started with no shared volume all serve identical catalogue data within 60 seconds of startup against a single git-service instance
- **SC-002**: Ten concurrent write mutations (mixed create, update, delete across catalogue entity types) complete without data loss, corruption, or conflict errors
- **SC-003**: After a release tag push, all running API instances (regardless of replica count) reload the catalogue within 30 seconds of receiving the notification
- **SC-004**: Zero git repository filesystem accesses are made by the API process under normal operation (verified by audit of API host filesystem state post-deployment)
- **SC-005**: The integration test suite covering API↔git-service read and write flows passes in CI across 10 consecutive runs with no flaky failures
- **SC-007**: Prometheus scrapes of both services expose per-RPC-method request count, latency histogram, and error rate metrics for all contract methods under normal operation
- **SC-006**: The updated Docker Compose configuration (with API volume mount removed) passes the end-to-end smoke test suite with no regressions

## Assumptions

1. **gRPC as transport**: gRPC is the chosen protocol for the service contract, established by the initiative scope in GH#65; the spec does not re-evaluate transport choice
2. **Websocket channel unchanged**: The websocket notification channel from git-service to API remains as-is; only the subsequent data-fetch step changes from local git pull to service contract call
3. **Temporary-clone isolation superseded**: Temporary-clone isolation for write mutations (T154 from `002-production-readiness`) is superseded by this spec. Mutation isolation is entirely git-service's responsibility; no temporary-clone logic is required in the API
4. **Git protocol operations exist in git-service**: The git-service already handles git protocol operations at the transport level; this feature adds the RPC contract layer and API client on top
5. **No backward compatibility required**: The application is in ALPHA stage. The API-side shared volume mount (`${GITSTORE_DATA_DIR:-git-data}:/data/repos:ro`) is removed unconditionally as part of this feature; existing deployments must update their configuration
6. **Atomic deployment**: git-service and API are always deployed together; the contract version identifier enforces this and no independent rolling upgrade path is required

## Out of Scope

- Replacing the KV-optimized read layer for latest tag/release metadata
- Rewriting catalogue query optimisations unrelated to API/git-service transport
- gRPC authentication, TLS, or mutual-TLS configuration (handled at infrastructure level)
- Git repository high-availability or replication for the git-service itself
- Multi-region deployments or global catalogue distribution