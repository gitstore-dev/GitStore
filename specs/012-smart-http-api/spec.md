# Feature Specification: Move Git Smart HTTP Server into gitstore-api

**Feature Branch**: `012-smart-http-api`  
**Created**: 2026-05-30  
**Status**: Closed  
**Input**: GH#103 — Move Git Smart HTTP Server into gitstore-api; packfile must not be buffered in memory before forwarding to the gRPC service. WebSocket interaction in both gitstore-git-service and gitstore-api is removed (superseded by GH#139).

## Context

Currently, the Git smart HTTP server lives inside `gitstore-git-service`. When a client executes `git push`, the service receives the entire packfile body into memory before processing it. Additionally, `gitstore-git-service` maintains a separate WebSocket server to broadcast `GitEvent` notifications after a push.

`gitstore-api` holds a corresponding WebSocket client (`internal/websocket/` package) that connects to the git service WebSocket server, along with a `git.ws.uri` configuration key and `GITSTORE_GIT__WS__URI` environment variable.

GH#103 requires moving the Git smart HTTP transport into `gitstore-api` (port 5000) and delegating all repository operations to `gitstore-git-service` via gRPC. As part of this move, the packfile must never be fully buffered in memory — it must be streamed directly over the gRPC boundary. GH#139 will replace the WebSocket notification mechanism with a gRPC event stream; this feature removes both the WebSocket server (in gitstore-git-service) and the WebSocket client (in gitstore-api) in preparation for that.

## Clarifications

### Session 2026-05-30

- Q: Should WebSocket removal cover gitstore-api as well as gitstore-git-service? → A: Yes — remove the `internal/websocket/` client package, `gorilla/websocket` Go module dependency, `git.ws.uri` config field, `GITSTORE_GIT__WS__URI` env vars, and all call sites from gitstore-api.
- Q: What observability is required for push/fetch operations across the gRPC boundary? → A: Structured log entries at key lifecycle points — stream start, stream completion (chunk count), quarantine promotion result, ref update result, and any gRPC stream error — from both services.
- Q: What should happen when the gRPC stream is interrupted mid-transfer? → A: Discard quarantine and all staged objects; return a push error to the client so it can retry cleanly. No attempt to resume a broken stream.
- Q: How should concurrent pushes to the same ref be handled? → A: Reject the second push with a standard Git non-fast-forward error; the client must fetch and rebase before retrying. Last-writer-wins is not acceptable.
- Q: What is the performance bound for clone/fetch operations? → A: Clone or fetch of a ≤100 MB pack completes within 30 seconds under normal load.
- Q: What should gitstore-api do when gitstore-git-service is unavailable during a push? → A: Fail fast — return a Git error to the client immediately with no internal retry. The client surfaces the error promptly.

## User Scenarios & Testing *(mandatory)*

### User Story 1 - Large Push Completes Without OOM (Priority: P1)

A developer pushes a repository with a large history (many commits, many refs, large binary files). The push completes successfully without the service running out of memory or being terminated.

**Why this priority**: The core problem statement. A single large push that causes an out-of-memory condition is a correctness failure, not a performance concern. This is the blocking issue that motivated the feature.

**Independent Test**: Run `git push` against a repository whose packfile exceeds a configured memory threshold (e.g., 512 MB). Observe that the push succeeds and no out-of-memory event is reported in service logs or system metrics.

**Acceptance Scenarios**:

1. **Given** a repository with 50,000+ commits, **When** a developer runs `git push`, **Then** the push completes successfully and peak service memory usage does not grow proportionally to packfile size.
2. **Given** a packfile larger than the available heap of the git service process, **When** a developer runs `git push`, **Then** the push is not rejected due to memory exhaustion and object storage reflects the pushed commits.
3. **Given** two concurrent large pushes to different repositories, **When** both are in flight simultaneously, **Then** both complete correctly and neither causes the other to fail.

---

### User Story 2 - Git Smart HTTP Lives in gitstore-api on Port 5000 (Priority: P1)

A developer clones, fetches, and pushes to a repository using the URL `http://<host>:5000/<namespace>/<repo>[.git]`. The smart HTTP endpoints are served by `gitstore-api`; `gitstore-git-service` no longer has its own HTTP listener.

**Why this priority**: This is the architectural prerequisite for GH#103. Without it the packfile streaming improvement cannot be wired through the new gRPC path.

**Independent Test**: Start the stack, run `git clone http://localhost:5000/gitstore/catalog`, `git fetch`, and `git push`. All three succeed. Verify that no HTTP port is open on `gitstore-git-service`.

**Acceptance Scenarios**:

1. **Given** the stack is running, **When** a developer runs `git clone http://localhost:5000/<namespace>/<repo>`, **Then** the clone completes with the correct commit history.
2. **Given** a cloned repository, **When** the developer runs `git push`, **Then** the push is accepted, objects appear in storage, and refs are updated correctly.
3. **Given** a request to `GET /{namespace}/{repo}/info/refs?service=git-upload-pack` on port 5000, **Then** the response contains the correct advertisement for that repository.
4. **Given** a request to `gitstore-git-service` on its former smart HTTP port, **Then** no HTTP listener responds (connection refused or 404 on any non-gRPC path).

---

### User Story 3 - WebSocket Removed from Both Services (Priority: P2)

The standalone WebSocket server in `gitstore-git-service` and the WebSocket client in `gitstore-api` are both removed. Neither service has any WebSocket listener, client library, configuration key, or environment variable related to WebSocket after this feature.

**Why this priority**: WebSocket removal is a clean-up milestone in preparation for GH#139. It reduces operational surface area and dependency footprint in both services, but does not block the core push functionality.

**Independent Test**: Start both services and verify no WebSocket port is open on the git service. Verify `gitstore-api` starts without any WebSocket client initialisation. Perform a `git push`; confirm the push succeeds and no WebSocket activity appears in logs from either service.

**Acceptance Scenarios**:

1. **Given** the git service is running, **When** a client attempts a WebSocket connection on the former WebSocket port, **Then** the connection is refused (no listener).
2. **Given** a successful `git push`, **When** the push completes, **Then** the push succeeds and no WebSocket broadcast error or connection attempt appears in logs from either service.
3. **Given** the git service binary, **When** it is built and started, **Then** no WebSocket-related dependencies are initialised.
4. **Given** the `gitstore-api` binary, **When** it is built and started, **Then** no WebSocket client package, `gorilla/websocket` dependency, or `git.ws.uri` configuration is present or referenced.

---

### Edge Cases

- What happens when the network connection drops mid-push, leaving a partial packfile stream in transit?
- How does the system handle a push whose refs advertise new tips but the packfile contains zero objects (force-update to an already-present SHA)?
- If `gitstore-git-service` is unavailable when `gitstore-api` receives a push or fetch, `gitstore-api` fails fast and returns a Git error to the client immediately with no internal retry.
- When two pushes target the same ref concurrently, the second push is rejected with a non-fast-forward error; the client must fetch and rebase before retrying.
- When the gRPC stream is interrupted mid-transfer, quarantine storage is discarded and a push error is returned to the client; no partial objects are promoted to live storage.

## Requirements *(mandatory)*

### Functional Requirements

- **FR-001**: `gitstore-api` MUST expose Git smart HTTP endpoints on port 5000, separately from the GraphQL/REST API on port 4000. Supported paths: `info/refs?service=git-upload-pack`, `info/refs?service=git-receive-pack`, `git-upload-pack`, `git-receive-pack` — with and without the trailing `.git` suffix.
- **FR-002**: `gitstore-api` MUST resolve the inbound `<namespace>/<repo>` path to a `repo_id` using the datastore abstraction before delegating to `gitstore-git-service`.
- **FR-003**: `gitstore-api` MUST forward pack data to `gitstore-git-service` over gRPC **without buffering the entire packfile in memory**. The pack payload MUST be forwarded as a stream of chunks.
- **FR-004**: `gitstore-git-service` MUST accept streaming packfile data over gRPC and write chunks progressively to quarantine storage, completing object staging without requiring the full pack to be assembled in memory.
- **FR-005**: `gitstore-git-service` MUST promote quarantined objects to live object storage and update refs atomically after all chunks have been received and validated.
- **FR-006**: `gitstore-git-service` MUST NOT expose a standalone HTTP listener for Git smart HTTP after this feature is complete.
- **FR-007**: The WebSocket server in `gitstore-git-service` MUST be removed entirely, including its dependencies, configuration keys, and connection-management code.
- **FR-008**: The WebSocket client in `gitstore-api` MUST be removed entirely, including the `internal/websocket/` package, the `gorilla/websocket` Go module dependency, the `git.ws.uri` configuration field, and the `GITSTORE_GIT__WS__URI` environment variable from all env files and documentation.
- **FR-009**: `gitstore-api` MUST return a proper Git smart HTTP 404 response (not a transport error) when the requested namespace or repository does not exist.
- **FR-010**: Both port 4000 and port 5000 MUST have passing health checks in the Docker Compose stack.
- **FR-011**: `git clone`, `git fetch`, and `git push` over `http://localhost:5000/<namespace>/<repo>[.git]` MUST succeed end-to-end in integration tests.
- **FR-012**: Both `gitstore-api` and `gitstore-git-service` MUST emit structured log entries at key lifecycle points of every push and fetch operation: stream start, stream completion (including chunk count), quarantine promotion result, ref update result, and any gRPC stream error.
- **FR-013**: If the gRPC stream is interrupted at any point before all chunks are received, `gitstore-git-service` MUST discard all quarantined objects for that transfer, promote nothing to live storage, and return an error to the caller. The client MUST receive a standard Git push error that allows a clean retry.
- **FR-014**: `gitstore-git-service` MUST reject a ref update when the expected current SHA does not match, returning a non-fast-forward error to the client. Concurrent pushes to the same ref MUST NOT silently overwrite each other.
- **FR-015**: `gitstore-api` MUST fail fast when `gitstore-git-service` is unreachable — returning a Git-protocol-level error to the client immediately with no internal retry loop.

### Key Entities

- **Packfile stream**: The sequence of binary chunks that make up a Git pack transfer, forwarded over the gRPC boundary one chunk at a time rather than as a single in-memory buffer.
- **Quarantine storage**: Temporary, isolated object storage where incoming pack objects are staged until ref validation succeeds, after which they are promoted to live storage.
- **Repository identity**: The `(namespace, repo-name) → repo_id` mapping resolved by `gitstore-api` before any gRPC call is made; `repo_id` is the only identifier passed to `gitstore-git-service`.

## Success Criteria *(mandatory)*

### Measurable Outcomes

- **SC-001**: A `git push` with a packfile of 1 GB or larger completes successfully, and peak memory growth in the git service during the push is bounded (does not scale linearly with packfile size).
- **SC-002**: `git clone` and `git fetch` of a repository whose pack is ≤100 MB complete within 30 seconds under normal load over port 5000.
- **SC-003**: Neither `gitstore-git-service` nor `gitstore-api` contains any WebSocket listener, client library, configuration reference, or environment variable after this feature is merged.
- **SC-004**: The Docker Compose health check for `gitstore-api` passes on both port 4000 and port 5000 within 30 seconds of service start.
- **SC-005**: CI integration tests covering clone, fetch, and push over smart HTTP pass on every pull request.

## Assumptions

- GH#65 (gRPC Git Service boundary) is in place before this feature is implemented; the gRPC interface is the only communication channel between `gitstore-api` and `gitstore-git-service` for Git operations.
- GH#100 and GH#70 (repository storage identity and namespace mapping) are available so `gitstore-api` can resolve `<namespace>/<repo>` to a `repo_id` at request time.
- GH#139 (gRPC GitEvent notification stream) is a separate initiative; this feature only removes both WebSocket endpoints and does not implement a replacement notification mechanism.
- Pack streaming chunk size is an operational tuning concern; a reasonable default will be chosen during implementation and does not require specification here.
- Authentication and authorisation on the Git transport are out of scope for this feature.

## Dependencies

- Blocked by: GH#65, GH#100, GH#70, GH#39
- Supersedes: GH#63 (standalone smart HTTP in git-service on port 5000)
- Precedes: GH#139 (both WebSocket ends are removed here; gRPC event stream is added there)
