# Feature Specification: GitStore - Production Readiness

**Feature Branch**: `002-production-readiness`
**Created**: 2026-05-01
**Status**: Draft
**Input**: User description: "Post-MVP production readiness improvements: implement Prometheus metrics exporters for api and git-server (T136), add ARIA accessibility labels to admin UI components (T137), implement proper git protocol solution replacing shared volume quick fix with production-ready API cloning from git-server (T152), and use temporary clones for API mutations following GitLab/Gitea pattern (T154)"

## Background

These tasks were deferred from `001-git-backed-ecommerce` (now closed) as non-blocking for MVP delivery. They represent the next layer of quality: observability, accessibility, and a production-grade deployment architecture.

## User Scenarios & Testing *(mandatory)*

### User Story 1 - Operator Observes System Health via Metrics (Priority: P1)

An operator deploying GitStore in production needs to monitor the health of the git-server and API services using their existing Prometheus/Grafana infrastructure.

**Why this priority**: Observability is the first requirement before any production deployment. Without metrics, operators cannot confidently run the system at scale or detect degradation.

**Independent Test**: Can be fully tested by scraping the `/metrics` endpoint on both services and verifying that product counts, request latencies, cache hit rates, and websocket connection counts appear in Prometheus format.

**Acceptance Scenarios**:

1. **Given** a running GitStore deployment, **When** an operator scrapes the metrics endpoint, **Then** the response contains Prometheus-formatted metrics for request counts, request latency, and error rates
2. **Given** a catalogue push event, **When** the push completes, **Then** metrics for git validation duration and push acceptance/rejection counts are updated
3. **Given** a websocket connection from the API to git-server, **When** the connection is established, **Then** active connection count appears in metrics
4. **Given** a catalogue reload triggered by a release tag, **When** reload completes, **Then** metrics for catalogue size (product/category/collection counts) and reload duration are updated

---

### User Story 2 - Operator Deploys Multiple API Instances (Priority: P2)

An operator running GitStore on Kubernetes or with multiple API replicas needs the API to clone the catalogue from the git-server over the network rather than reading from a shared filesystem volume.

**Why this priority**: The shared volume approach prevents horizontal scaling of the API and ties it to single-host deployments. This is the most significant architectural constraint remaining from the MVP quick fix.

**Independent Test**: Can be fully tested by removing the shared volume mount from the API service, setting `GITSTORE_GIT_REPO` to a `git://` URL, starting multiple API replicas, and verifying all instances serve the same catalogue after a release tag is pushed.

**Acceptance Scenarios**:

1. **Given** a `GITSTORE_GIT_REPO` configured as a `git://` URL, **When** the API starts up, **Then** it clones the catalogue repository from the git-server over the network without requiring a shared volume
2. **Given** a running API instance that has already cloned the catalogue, **When** a new release tag is created and a websocket notification is received, **Then** the API pulls the latest changes and reloads the catalogue without restarting
3. **Given** three API instances running simultaneously, **When** a new release tag is pushed, **Then** all three instances independently pull and reload the catalogue within 30 seconds

---

### User Story 3 - Developer Writes Safe API Mutations (Priority: P3)

A developer extending the API's write operations (mutations) needs each mutation to use an isolated temporary clone of the catalogue repository so that concurrent mutations do not conflict and no persistent working directory state accumulates.

**Why this priority**: Current mutations may use a persistent working directory. The temporary clone pattern prevents corruption under concurrent writes and is the standard pattern used by GitLab and Gitea. This is lower priority because the current approach works for MVP scale.

**Independent Test**: Can be fully tested by running concurrent product creation mutations and verifying no file conflicts occur, then checking that no orphaned temporary directories remain after mutations complete.

**Acceptance Scenarios**:

1. **Given** two concurrent `createProduct` mutations, **When** both execute simultaneously, **Then** both succeed without file conflicts and both products appear in the catalogue after the next release tag
2. **Given** a `updateProduct` mutation, **When** it completes (success or failure), **Then** no temporary clone directory remains on disk
3. **Given** a `deleteProduct` mutation that fails during the git push, **When** the error is returned to the caller, **Then** the temporary clone is cleaned up and no partial state persists

---

### User Story 4 - Non-Technical User Navigates Admin UI Accessibly (Priority: P4)

A user relying on assistive technology (screen reader, keyboard navigation) needs the admin UI to be navigable without a mouse, with all interactive elements properly labelled.

**Why this priority**: Accessibility is a quality baseline for production UIs. It is lower priority than deployment architecture but must ship before the system is considered production-complete.

**Independent Test**: Can be fully tested by running an automated accessibility audit (e.g., axe-core) against all admin UI pages and verifying zero critical ARIA violations, plus manual keyboard navigation through the product create/edit flow.

**Acceptance Scenarios**:

1. **Given** a screen reader user on the product list page, **When** they navigate the page, **Then** all buttons, links, form fields, and table headers have descriptive labels
2. **Given** a keyboard-only user on the product edit form, **When** they tab through fields, **Then** focus order follows logical reading order and all controls are reachable
3. **Given** a drag-and-drop category reorder tree, **When** a keyboard user interacts with it, **Then** an accessible alternative (arrow keys or move buttons) allows reordering without mouse
4. **Given** the conflict resolution modal, **When** it opens, **Then** focus moves to the modal, a descriptive heading announces the conflict, and all actions are keyboard-accessible

---

### Edge Cases

- **Git server unavailable at startup**: When the API starts and `GITSTORE_GIT_REPO` is a `git://` URL but the git-server is not yet ready, the API retries cloning with exponential backoff and does not accept requests until the catalogue is loaded
- **Partial clone during tag push**: When a release tag is pushed while an API instance is mid-clone on startup, the instance finishes the initial clone before processing the websocket notification
- **Metrics endpoint under load**: Metrics scraping must not block request handling; metric collection must be non-blocking
- **Temporary clone disk space**: If disk space is exhausted during a temporary clone for a mutation, the mutation fails with a clear error and clean-up is still attempted
- **ARIA in dynamically rendered content**: Dynamically inserted content (toast notifications, loading spinners, validation errors) must also carry appropriate ARIA live region or role attributes

## Requirements *(mandatory)*

### Functional Requirements

- **FR-001**: The git-server service MUST expose a Prometheus-compatible metrics endpoint reporting: HTTP request count by method/path/status, request latency histogram, active websocket connections, push validation count by outcome (accepted/rejected), and git operation duration
- **FR-002**: The API service MUST expose a Prometheus-compatible metrics endpoint reporting: GraphQL request count by operation/status, resolver latency histogram, catalogue reload count and duration, catalogue entity counts (products, categories, collections), and cache hit rate
- **FR-003**: Metrics collection MUST NOT block or measurably degrade request handling performance
- **FR-004**: The API catalogue loader MUST detect whether `GITSTORE_GIT_REPO` is a remote URL (`git://`, `http://`, `https://`) or a local filesystem path and behave accordingly
- **FR-005**: When `GITSTORE_GIT_REPO` is a remote URL, the API MUST clone the repository to a local cache on startup before accepting requests
- **FR-006**: When a websocket release tag notification is received and `GITSTORE_GIT_REPO` is a remote URL, the API MUST pull the latest changes from the remote before reloading the catalogue
- **FR-008**: Write mutations in the API MUST use temporary clones of the catalogue repository for each operation, with the temporary directory deleted after the operation completes (success or failure)
- **FR-009**: All interactive elements in the admin UI MUST have accessible names via ARIA labels, `aria-labelledby`, or associated `<label>` elements
- **FR-010**: All admin UI form fields MUST have programmatically associated labels visible to screen readers
- **FR-011**: The admin UI drag-and-drop category and collection reordering MUST provide a keyboard-accessible alternative interaction
- **FR-012**: Modal dialogues in the admin UI (conflict resolution, publish confirmation) MUST manage focus: move focus in on open, trap focus while open, and return focus to trigger element on close
- **FR-013**: Dynamically inserted admin UI content (notifications, validation errors, loading indicators) MUST use appropriate ARIA live regions or roles so screen readers announce changes

### Key Entities

- **Metrics Endpoint**: A scrape target exposing time-series metrics in Prometheus text exposition format. One endpoint per service (git-server, API).
- **Temporary Clone**: An ephemeral local copy of the catalogue git repository created for a single mutation operation and deleted immediately after.
- **Remote Catalogue Loader**: The updated API component that can clone and pull from a remote `git://` URL in addition to reading from a local path.

## Success Criteria *(mandatory)*

### Measurable Outcomes

- **SC-001**: Prometheus can successfully scrape both services and collect at least 10 distinct named metrics from each within 5 seconds of scrape
- **SC-002**: Metrics scraping adds less than 5ms overhead to the 99th-percentile request latency under normal load
- **SC-003**: Three API instances started concurrently against a single git-server all serve identical catalogue data within 60 seconds of startup, with no shared volume required
- **SC-004**: After a release tag push, all running API instances (regardless of replica count) reload the catalogue within 30 seconds
- **SC-005**: Concurrent write mutations (up to 10 simultaneous) complete without data corruption, measured by verifying all written entities appear correctly in the next catalogue reload
- **SC-006**: Zero temporary clone directories remain on disk after 100 consecutive mutations under normal conditions
- **SC-007**: Automated accessibility audit of all admin UI pages reports zero critical (level A) ARIA violations
- **SC-008**: A keyboard-only user can complete the full product create → publish workflow without using a mouse

## Assumptions

1. **Prometheus scrape interval**: Standard 15-second scrape interval; metrics do not need sub-second granularity
2. **Backward compatibility**: Existing single-host Docker Compose deployments using `GITSTORE_DATA_DIR` shared volume continue to work unchanged after this feature
3. **Git protocol support in git-server**: The git-server already supports `git://` clone and fetch operations (implemented in T030/T039); this feature adds the API-side clone logic
4. **Keyboard alternative for drag-and-drop**: Moving items up/down via keyboard buttons is an acceptable alternative to drag-and-drop for category/collection reordering
5. **Screen reader testing**: Automated axe-core scanning is the primary accessibility validation method; manual testing with specific screen readers (VoiceOver, NVDA) is aspirational

## Out of Scope

- Adding authentication to the metrics endpoint (metrics are internal; network-level access control is sufficient)
- Full WCAG 2.1 AA compliance audit beyond critical ARIA violations
- Distributed tracing (separate concern from metrics)
- Git repository replication or high-availability for the git-server itself
- Multi-region deployments
