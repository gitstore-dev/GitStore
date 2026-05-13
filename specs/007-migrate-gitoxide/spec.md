# Feature Specification: Migrate gitstore-git-service from git2 to gitoxide

**Feature Branch**: `007-migrate-gitoxide`  
**Created**: 2026-05-12  
**Status**: Closed  
**Input**: GH#108 — Migrate gitstore-git-service from git2 to gitoxide

## Overview

`gitstore-git-service` currently depends on the `git2` crate, which wraps libgit2 — a C library requiring native toolchain setup for builds and runtime images. This feature replaces all `git2` usage with `gitoxide`, a pure-Rust implementation of the Git protocol. The migration also establishes the architectural foundation for multi-repository hosting: the service no longer boots with a single hardcoded `catalog.git` repository. Instead, repositories are created and deleted at runtime by callers, and every operation is scoped to a named repository. This positions the service as a general-purpose Git hosting platform rather than a single-repository backend.

## User Scenarios & Testing *(mandatory)*

### User Story 1 - Build and Deploy Without Native Git Library (Priority: P1)

An operator building or deploying `gitstore-git-service` wants to produce a self-contained binary with no external C library dependencies so that builds are reproducible and container images are smaller.

**Why this priority**: Removing the libgit2 native dependency is the primary driver of this migration. Every other story depends on the dependency swap being in place.

**Independent Test**: Build `gitstore-git-service` in a clean environment that has no libgit2 installed. The build succeeds and the resulting binary starts and serves requests normally.

**Acceptance Scenarios**:

1. **Given** a build environment with no libgit2 or its headers installed, **When** `gitstore-git-service` is compiled, **Then** the build succeeds without errors related to missing native libraries.
2. **Given** a minimal container image with no libgit2 runtime library, **When** `gitstore-git-service` is executed, **Then** it starts successfully and handles repository operations.
3. **Given** the `Cargo.toml` and lock file of `gitstore-git-service`, **When** all declared dependencies are audited, **Then** the `git2` crate and any libgit2-specific build dependencies are absent.

---

### User Story 2 - Create and Delete Repositories at Runtime (Priority: P1)

A caller (e.g. `gitstore-api`) wants to provision a new named repository on demand and delete it when it is no longer needed, without restarting the service or having a repository pre-created at boot.

**Why this priority**: The move to multi-repository hosting requires lifecycle management as a first-class capability. All other operations depend on repositories being provisioned before use.

**Independent Test**: Create a repository via the service, perform a file write and a ref listing, then delete the repository; all succeed without service restart.

**Acceptance Scenarios**:

1. **Given** no repository exists for a given name, **When** a create-repository request is issued, **Then** a new, empty bare repository is initialised at the corresponding path and the service confirms creation.
2. **Given** a repository exists, **When** a delete-repository request is issued, **Then** the repository and all its data are removed and subsequent operations on that name return a "not found" error.
3. **Given** a create-repository request for a name that already exists, **When** the request is processed, **Then** an "already exists" error is returned and the existing repository is untouched.
4. **Given** the service starts with an empty data directory, **When** it initialises, **Then** no repositories are created automatically — the data directory is prepared but no `catalog.git` or other default repository is provisioned.

---

### User Story 2b - Operate on a Named Repository (Priority: P1)

A caller performing file, ref, or tag operations wants to address any operation to a specific named repository so that the service can host many repositories without ambiguity.

**Why this priority**: Every existing RPC (GetFile, CommitFile, CreateTag, etc.) must carry a repository identifier once the hardcoded single-repo assumption is removed.

**Independent Test**: Create two repositories, write distinct files to each, and verify that reads on each repository return only that repository's contents.

**Acceptance Scenarios**:

1. **Given** two repositories `repo-a` and `repo-b`, **When** a file is committed to `repo-a`, **Then** listing files on `repo-b` does not include the file from `repo-a`.
2. **Given** a request carries an unknown repository name, **When** any operation is attempted, **Then** a "not found" error is returned immediately without performing any file system operations on other repositories.
3. **Given** concurrent requests targeting different repositories, **When** both are processed simultaneously, **Then** each is isolated and neither interferes with the other.

---

### User Story 3 - Discover and List References (Priority: P1)

A client calling `gitstore-git-service` via the smart HTTP or gRPC interface wants to enumerate repository references (branches, tags) and receive correct, up-to-date results after the migration.

**Why this priority**: Ref discovery is central to clone, fetch, and push handshakes; any regression here breaks the core Git transport.

**Independent Test**: Run existing ref-discovery and ref-listing tests; all pass with equivalent results to the pre-migration baseline.

**Acceptance Scenarios**:

1. **Given** a repository containing multiple branches and tags, **When** a ref listing is requested, **Then** all refs are returned with their correct object IDs.
2. **Given** a repository with no refs (empty repository), **When** a ref listing is requested, **Then** an empty list is returned without errors.
3. **Given** a repository where a ref has just been updated by a push, **When** refs are listed immediately after the push completes, **Then** the updated ref's object ID is reflected in the response.

---

### User Story 4 - Inspect Commits and Tags for Event Handling (Priority: P2)

An internal component that processes Git events (e.g., tag push notifications, commit metadata extraction) wants to read commit and tag objects correctly after the migration.

**Why this priority**: Tag event handling is the main consumer of commit/tag inspection; regressions here would break event delivery without breaking the transport layer.

**Independent Test**: Run existing tag-event and commit-inspection tests; all pass with equivalent results.

**Acceptance Scenarios**:

1. **Given** a repository with an annotated tag pointing to a commit, **When** the tag object is inspected, **Then** the tag message, tagger identity, and target commit metadata are returned correctly.
2. **Given** a repository with a lightweight tag, **When** the tag is resolved, **Then** the target commit's SHA, author, message, and timestamp are accessible.
3. **Given** a ref that does not point to a tag object (e.g., a branch), **When** tag-specific inspection is attempted, **Then** a clear error is returned distinguishing the object type mismatch.

---

### User Story 5 - Preserve Smart HTTP and WebSocket Behaviour (Priority: P1)

A Git client performing a clone or push through the smart HTTP or WebSocket interface of `gitstore-git-service` wants transport behaviour to remain unchanged after the migration.

**Why this priority**: Preserving external protocol behaviour is a hard requirement; any regression here breaks existing clients without warning.

**Independent Test**: Run a `git clone` and a `git push` against the migrated service and verify both complete successfully with correct data transferred.

**Acceptance Scenarios**:

1. **Given** a repository served by the migrated `gitstore-git-service`, **When** a Git client performs `git clone`, **Then** the clone completes with all objects and refs matching the server state.
2. **Given** a local commit ready to push, **When** a Git client performs `git push` to `gitstore-git-service`, **Then** the push is accepted, the ref is updated, and the new objects are stored correctly.
3. **Given** an active WebSocket session during a push, **When** the push completes, **Then** the session closes cleanly and the event stream reflects the new ref state.

---

### Edge Cases

- What happens when the repository name contains non-ASCII, Unicode, or path-separator characters? Names must be validated and rejected if they could produce path traversal or ambiguous filesystem paths.
- What happens if a repository is deleted while an operation is in flight on it? The in-flight operation must complete or fail cleanly; the service must not crash.
- What happens if two simultaneous create-repository requests use the same name? One must succeed and the other must receive an "already exists" error.
- What happens if the data directory is not writable? Repository creation must fail with a clear error; existing repositories must continue to be readable.
- What happens when an in-flight operation is in progress and the service is shut down gracefully? Existing shutdown behaviour must not regress.
- What happens when `gitoxide` encounters a Git object format that `git2` handled but `gitoxide` does not yet support? Any such gaps must be identified, documented, and either handled or explicitly deferred with a tracked issue.

## Requirements *(mandatory)*

### Functional Requirements

- **FR-001**: The `gitstore-git-service` build MUST NOT depend on the `git2` crate or any libgit2 native library after the migration.
- **FR-002**: All repository-open operations currently implemented via `git2` MUST be re-implemented using `gitoxide` equivalents with identical observable behaviour.
- **FR-003**: All repository-initialisation operations currently implemented via `git2` MUST be re-implemented using `gitoxide` equivalents with identical observable behaviour.
- **FR-004**: All ref-discovery and ref-listing operations currently implemented via `git2` MUST be re-implemented using `gitoxide` equivalents and return equivalent results.
- **FR-005**: All commit and tag inspection operations currently implemented via `git2` MUST be re-implemented using `gitoxide` equivalents and return equivalent data.
- **FR-006**: Internal abstractions over repository access MUST be updated so that no internal type from `git2` (e.g., `git2::Repository`, `git2::Reference`) is exposed across abstraction boundaries after migration.
- **FR-007**: The build configuration (`Cargo.toml`, build scripts, CI configuration) MUST be updated to remove `git2` and any libgit2-specific build requirements.
- **FR-008**: The existing HTTP smart protocol behaviour MUST be preserved without regression; all Git transport operations (upload-pack, receive-pack) that worked before migration MUST continue to work after.
- **FR-009**: The existing WebSocket behaviour related to Git events MUST be preserved without regression.
- **FR-010**: Existing tests for repository initialisation, ref listing, and tag/event handling MUST continue to pass after migration; any test that tested `git2`-specific internals MUST be updated to test equivalent `gitoxide`-backed behaviour.
- **FR-011**: Any intentionally deferred gaps between `git2` and `gitoxide` behaviour MUST be identified, documented in developer notes, and tracked as separate issues.
- **FR-012**: Build and runtime container images MUST no longer require libgit2 toolchain setup after the migration.
- **FR-013**: The migration MUST be delivered as a single, complete swap of `git2` for `gitoxide`; no incremental or feature-flagged hybrid state is permitted in the merged result. Backwards compatibility with the pre-migration internal API is not required.
- **FR-014**: Any structured log statements, tracing spans, or metrics that reference `git2`-specific types or error codes MUST be updated to equivalent `gitoxide`-backed signals; no observability signal present before migration may be silently dropped.
- **FR-015**: If a required `gitoxide` API is missing or insufficiently stable, a minimal in-repo shim implementation is permitted to fill the gap. The shim MUST be documented, the underlying gap MUST be tracked as a separate issue, and the shim MUST be removed once `gitoxide` upstream provides a stable equivalent.
- **FR-016**: The service MUST expose a `CreateRepository` operation that provisions a new, empty bare repository identified by a caller-supplied name. Attempting to create a repository whose name already exists MUST return an "already exists" error.
- **FR-017**: The service MUST expose a `DeleteRepository` operation that removes a named repository and all of its data. Attempting to delete a repository that does not exist MUST return a "not found" error.
- **FR-018**: Every existing operation (GetFile, GetFileStream, ListFiles, CommitFile, DeleteFile, CreateTag, ListTags, GetLatestTag) MUST accept a `repository_id` field that identifies which repository the operation targets. Requests with an unrecognised `repository_id` MUST return a "not found" error.
- **FR-019**: The service MUST NOT create any repository automatically at startup. The data directory is prepared but no default repository is provisioned.
- **FR-020**: Repository names MUST be validated; names that are empty, contain path-separator characters (`/`, `\`), or contain `..` components MUST be rejected with an "invalid argument" error.
- **FR-021**: Concurrent operations on different repositories MUST be fully isolated; a write lock on one repository MUST NOT block reads or writes on a different repository.

## Success Criteria *(mandatory)*

### Measurable Outcomes

- **SC-001**: `gitstore-git-service` builds successfully in a clean environment with no libgit2 headers or runtime libraries present.
- **SC-002**: All pre-existing tests for repository initialisation, ref listing, and tag/event handling pass without modification to test assertions after the migration.
- **SC-003**: A `git clone` and a `git push` performed by a standard Git client against the migrated service complete successfully and produce identical results to the pre-migration baseline.
- **SC-004**: The `git2` crate is absent from the final `Cargo.lock` of `gitstore-git-service` after the migration.
- **SC-005**: Container images built for `gitstore-git-service` after migration are smaller than or equal in size to pre-migration images (libgit2 removal does not increase image size).
- **SC-006**: Any behavioural gaps between `git2` and `gitoxide` discovered during migration are documented and tracked as separate issues before the migration PR is merged.
- **SC-007**: Core operations (ref listing, repository open, clone, push) on the migrated service perform within ±10% of the pre-migration baseline, as measured by existing or new benchmarks run in CI.
- **SC-008**: A caller can create a named repository, perform a file commit and a tag listing against it, and delete it — all without restarting the service — with each step completing successfully.
- **SC-009**: Operations targeting an unrecognised repository name return a "not found" error within the same latency budget as a successful operation (no unnecessary I/O before the error is returned).
- **SC-010**: Concurrent operations on two different named repositories complete independently without one blocking the other.

## Assumptions

- `gitoxide` provides equivalents for the majority of `git2` operations currently used in `gitstore-git-service`. Where a required API is missing or insufficiently stable, a minimal in-repo shim is acceptable; gaps are tracked and shims are removed once `gitoxide` upstream catches up.
- The HTTP smart protocol (`upload-pack`, `receive-pack`) is currently handled by shelling out to the `git` binary rather than through `git2` directly (removal of shell-outs is tracked in #109 and is out of scope here); therefore transport preservation is tested at the protocol level, not the library level.
- The existing test suite provides sufficient coverage of current `git2` usage patterns to serve as a migration regression baseline.
- The gRPC contract (proto) IS extended in this feature: `CreateRepository`, `DeleteRepository` RPCs are added and a `repository_id` field is added to every existing request message. This is an additive change; the proto package remains `gitstore.git.v1` and callers that do not set `repository_id` receive a "not found" error rather than silently targeting a default repository.
- CI has access to a standard Rust toolchain; no additional CI infrastructure is needed to build without libgit2.
- Security validation of pack-file and object handling is deferred to `gitoxide`'s built-in defaults; explicit parity auditing against `git2` is out of scope for this migration given the service's early-alpha status and controlled deployment environment.
- `gitstore-git-service` is early alpha; no backwards compatibility guarantees are made. Breaking internal changes introduced by the migration do not require deprecation notices or transition periods.

## Clarifications

### Session 2026-05-13

- Q: Should the service support multiple repositories or remain single-repository? → A: Multi-repository. No default repository at startup; every operation is scoped by a caller-supplied `repository_id`. The service is the foundation for a generic git hosting platform.

### Session 2026-05-12

- Q: Should `git2` be replaced all at once in a single PR or incrementally subsystem-by-subsystem? → A: Complete swap — `git2` is removed entirely in a single migration PR. No incremental or feature-flagged approach.
- Q: What is the acceptable performance regression budget for the migrated service? → A: No measurable regression — migrated service must perform within ±10% of the pre-migration baseline on clone/push/ref-list operations. Note: smart HTTP protocol will be moved into `gitstore-api` (GH#103), so transport performance preservation applies only to the current in-service-git-service implementation; it is not a long-term concern for this service.
- Q: Must `gitoxide` provide the same pack-file/object safety checks as `git2`, or is relying on `gitoxide` defaults acceptable? → A: Defer to `gitoxide` defaults — no explicit audit of safety-check parity is required for this migration.
- Q: Should existing log/trace/metric signals tied to `git2`-specific types be preserved, updated, or dropped? → A: Update to equivalent — any `git2`-specific observability signals must be updated to `gitoxide` equivalents; no signal may be silently dropped.
- Q: What happens if a required `gitoxide` API is missing or unstable during migration? → A: Custom shim acceptable — a minimal in-repo implementation may fill the gap; the gap and shim must be documented and tracked as a separate issue.

## Dependencies

- GH#109 (remove shell-out calls to `git` binary) is explicitly out of scope and tracked separately; this migration must not be blocked on or conflated with that work.
- GH#65 (gRPC contract design) is a downstream beneficiary of this migration but not a blocker.
- GH#103 (move smart HTTP protocol into `gitstore-api`) will supersede the smart protocol preservation requirements (FR-008, FR-009, User Story 5) after it lands; transport-level work in `gitstore-git-service` is therefore temporary and need not be over-engineered.
