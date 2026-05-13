# Feature Specification: Remove Git Binary Shell-Outs and Init Script

**Feature Branch**: `008-remove-git-shellouts`  
**Created**: 2026-05-13  
**Status**: Closed  
**Input**: User description: "implement GH#109. Remove the repository init script and associated documentation"

## User Scenarios & Testing *(mandatory)*

### User Story 1 - Git Clone/Fetch Without Binary (Priority: P1)

A developer clones or fetches a repository from the Git service. The operation completes successfully even though the `git` binary is not installed in the server container. All HTTP smart protocol negotiation happens entirely within the service process.

**Why this priority**: This is the most fundamental operation — without it, the service is non-functional. Removing the external binary dependency is the core deliverable of GH#109 and eliminates an entire class of runtime failures caused by missing or version-mismatched `git` binaries.

**Independent Test**: Can be fully tested by running `git clone <service-url>/catalog.git` and `git fetch` against a running git-service container that has no `git` binary installed, verifying the operations complete successfully.

**Acceptance Scenarios**:

1. **Given** the git-service container is running without the `git` binary installed, **When** a client runs `git clone http://<host>/catalog.git`, **Then** the repository is cloned successfully with all history and objects intact.
2. **Given** the git-service container is running without the `git` binary installed, **When** a client runs `git fetch origin`, **Then** the fetch completes and the local ref list is updated correctly.
3. **Given** the server is running in-process git handling, **When** `upload-pack` advertisement is requested, **Then** the response lists current server capabilities and all refs without invoking any external process.

---

### User Story 2 - Git Push Without Binary (Priority: P1)

A developer pushes commits to a repository. The push is accepted and persisted by the git service without delegating to an external `git` binary. Error responses for rejected pushes remain human-readable.

**Why this priority**: Push support is equally fundamental to the service's purpose and shares the same binary-removal requirement. Push rejection error messages must remain compatible with existing client workflows.

**Independent Test**: Can be fully tested by running `git push origin main` against a running git-service container that has no `git` binary installed, verifying objects are written and refs are updated.

**Acceptance Scenarios**:

1. **Given** a client with new commits, **When** `git push` is run against the service, **Then** all new objects are received, refs are updated, and the push exits with success.
2. **Given** a push that violates a server-side policy (e.g., non-fast-forward to a protected branch), **When** the push is attempted, **Then** the client receives a human-readable rejection message compatible with standard `git` client output expectations.
3. **Given** the git-service container is running without the `git` binary installed, **When** `receive-pack` advertisement is requested, **Then** the server advertises its capabilities without invoking any external process.

---

### User Story 3 - Slimmer Container Image (Priority: P2)

An operator builds and deploys the git-service container. The resulting image is smaller because the `git` package is no longer installed, reducing the attack surface and image size.

**Why this priority**: This is the immediate operational benefit of the removal: a lighter, more secure image. It is independently verifiable and delivers value even without changing day-to-day developer workflows.

**Independent Test**: Can be fully tested by building the git-service Docker image, running `docker run --rm <image> which git || echo "not found"`, and verifying the `git` binary is absent.

**Acceptance Scenarios**:

1. **Given** the updated Dockerfile, **When** the git-service image is built, **Then** the resulting image does not contain the `git` binary.
2. **Given** the built image, **When** an operator inspects installed packages, **Then** the `git` runtime package is absent from the image manifest.
3. **Given** the image without the `git` binary, **When** the service starts and handles normal clone/push/fetch traffic, **Then** no errors referencing a missing `git` binary appear in service logs.

---

### User Story 4 - Remove Demo Init Script and Docs (Priority: P3)

A developer setting up the project for the first time reads the documentation and does not encounter references to `scripts/init-demo-catalog.sh`. The script and all documentation sections that reference it are gone, leaving a cleaner, more accurate onboarding path.

**Why this priority**: The init script relies on having a local `git` binary to create bare repositories. Removing the binary dependency makes the init script non-functional, so it must be removed together with its documentation to avoid confusing new contributors.

**Independent Test**: Can be fully tested by auditing the repository for any file containing a reference to `init-demo-catalog.sh` or the script file itself and confirming none exist.

**Acceptance Scenarios**:

1. **Given** the updated repository, **When** a developer searches the codebase for `init-demo-catalog`, **Then** no matches are found in any file.
2. **Given** the updated documentation, **When** a developer reads `docs/developer-guide.md`, `docs/user-guide.md`, and `docs/docker-troubleshooting.md`, **Then** there are no instructions referencing the removed script.
3. **Given** the updated repository, **When** a developer lists the `scripts/` directory, **Then** `init-demo-catalog.sh` is not present.

---

### Edge Cases

- A partial in-process write (e.g., interrupted push) MUST be rolled back atomically; no partial object state is left on disk, and the client receives an explicit error.
- How does the service handle a clone of an empty repository (no commits yet)?
- What happens when `upload-pack` is called for a non-existent repository path?
- A push carrying a pack exceeding the configured maximum size MUST be rejected with a human-readable error before any write begins; no partial data is written to disk.

## Requirements *(mandatory)*

### Functional Requirements

- **FR-001**: The git-service MUST handle `git upload-pack --advertise-refs`, `git upload-pack --stateless-rpc`, `git receive-pack --advertise-refs`, and `git receive-pack --stateless-rpc` entirely in-process, without delegating to the system `git` binary.
- **FR-002**: The git-service MUST support `git clone` over HTTP smart protocol without the `git` binary present in the runtime environment.
- **FR-003**: The git-service MUST support `git fetch` over HTTP smart protocol without the `git` binary present in the runtime environment.
- **FR-004**: The git-service MUST support `git push` over HTTP smart protocol without the `git` binary present in the runtime environment.
- **FR-005**: The git-service MUST produce human-readable error responses for rejected pushes that are compatible with standard `git` client output.
- **FR-006**: The git-service container image MUST NOT install the `git` runtime package as a Docker layer or dependency.
- **FR-007**: The CI pipeline MUST include a test path that proves the git-service operates correctly without the `git` binary available in the test environment.
- **FR-008**: The `scripts/init-demo-catalog.sh` script MUST be removed from the repository.
- **FR-009**: All documentation references to `scripts/init-demo-catalog.sh` MUST be removed or replaced with updated instructions that do not depend on the removed script.
- **FR-010**: No `std::process::Command` or equivalent shell-out to `git` MUST remain in git-service production source code.
- **FR-011**: In the event of a partial or interrupted push write, the git-service MUST atomically roll back any partial state and return an explicit error to the client; no partial object or ref update MUST remain on disk.
- **FR-012**: Each in-process protocol phase (`upload-pack advertise-refs`, `upload-pack stateless-rpc`, `receive-pack advertise-refs`, `receive-pack stateless-rpc`) MUST emit a structured log event recording repo path, operation name, duration, and outcome (success or error type).
- **FR-013**: The git-service MUST enforce a configurable maximum incoming pack size; any push exceeding this limit MUST be rejected with a human-readable error before any write operation begins, and no partial data MUST be persisted.

## Success Criteria *(mandatory)*

### Measurable Outcomes

- **SC-001**: The git-service container image operates fully without the `git` binary — verified by automated tests that run against a container with no `git` binary installed.
- **SC-002**: `git clone`, `git fetch`, and `git push` over HTTP smart protocol all pass end-to-end in CI on 100% of runs after this change.
- **SC-003**: The git-service container image size decreases by at least the size of the `git` package (~30 MB on typical Alpine/Debian base images).
- **SC-004**: Zero occurrences of `Command::new("git")` or equivalent shell-out patterns remain in the git-service production source tree — verified by automated search in CI.
- **SC-005**: Rejected push error messages are parseable and human-readable by standard `git` client tooling — verified by an automated test asserting message format.
- **SC-006**: No file in the repository references `init-demo-catalog.sh` — verified by automated search in CI.
- **SC-007**: `git clone`, `git fetch`, and `git push` each complete at p99 ≤ 2 seconds for a 10 MB repository under normal single-client load — measured in CI benchmarks.
- **SC-008**: A push exceeding the configured maximum pack size is rejected with a human-readable error and zero bytes written to disk — verified by an automated test.

## Clarifications

### Session 2026-05-13

- Q: What should happen when a push arrives during a partial in-process write? → A: Any partial write is rolled back atomically; client receives an explicit error (no partial state left on disk).
- Q: Should there be a measurable performance target for in-process clone/fetch/push? → A: Yes — p99 latency ≤ 2 s for a 10 MB repository under normal load.
- Q: What observability must the in-process protocol handling emit? → A: Each protocol phase (advertise-refs, stateless-rpc) MUST emit a structured log event with repo path, operation, duration, and outcome.
- Q: Should incoming pack streams be bounded to prevent resource exhaustion? → A: Yes — enforce a configurable maximum pack size; pushes exceeding the limit are rejected with a human-readable error before any write begins.

## Assumptions

- The existing `gix` (gitoxide) library already provides sufficient in-process primitives to replace all four shell-out call sites identified in `gitstore-git-service/src/http_git_server.rs`.
- The demo catalog scenario served by `scripts/init-demo-catalog.sh` is either no longer needed or will be replaced by a different onboarding mechanism in a future feature; this spec only covers removal.
- The documentation sections that reference `init-demo-catalog.sh` (`docs/developer-guide.md`, `docs/user-guide.md`, `docs/docker-troubleshooting.md`) will be updated to remove those references without necessarily providing a replacement workflow in this feature.
- Depends on Feature 007 (migrate gitoxide) being merged — gitoxide must be in place before the shell-outs can be replaced with in-process calls.

## Dependencies

- **Upstream**: GH#108 / Feature 007 — `gitoxide` (`gix`) must be the active Git library before shell-outs can be removed in-process.
- **Downstream**: GH#65 — Decoupling the API from the Git service via gRPC benefits from a self-contained, binary-free git-service.