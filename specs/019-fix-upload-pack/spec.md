# Feature Specification: Fix git clone and git fetch over HTTP

**Feature Branch**: `019-fix-upload-pack`
**Created**: 2026-06-05
**Status**: Closed
**Input**: User description: "git clone and git fetch fail because the upload-pack implementation returns NAK and closes the connection instead of sending a PACK file."

## User Scenarios & Testing *(mandatory)*

### User Story 1 — Catalog Author Clones a Repository (Priority: P1)

A catalog author runs `git clone http://<host>/<namespace>/<repo>.git` to obtain a local copy of a catalog repository that already contains commits. The clone completes successfully and the author has a fully-checked-out working copy.

**Why this priority**: Clone is the entry point for every catalog workflow. Without it, authors cannot push products, open a diff, or run any local tooling. Every other git interaction depends on it.

**Independent Test**: Run `git clone <url>` against a repository with at least one commit. The working copy is created with the full history; `git log` shows the expected commits.

**Acceptance Scenarios**:

1. **Given** a repository with one or more commits, **When** an author runs `git clone <url>`, **Then** the clone completes without error and the working directory matches the repository content at HEAD.
2. **Given** a repository with one or more commits and no prior local copy, **When** an author clones it, **Then** the server sends all required objects and the clone succeeds.
3. **Given** a repository with multiple branches, **When** an author clones it, **Then** all remote-tracking branches are visible locally.

---

### User Story 2 — Catalog Author Fetches New Commits (Priority: P1)

An author who already has a local clone runs `git fetch` or `git pull` to retrieve commits pushed by other authors. Only the objects the author does not already have are transferred.

**Why this priority**: Fetch is the other half of the read-path. Both clone and fetch break by the same root cause; the fix must handle both.

**Independent Test**: Clone a repo, push a new commit from a second client, then `git fetch` from the first. The new commit appears in `git log origin/main`.

**Acceptance Scenarios**:

1. **Given** a local clone at commit A and a new commit B on the server, **When** the author runs `git fetch`, **Then** commit B appears in the local remote-tracking refs.
2. **Given** a local clone that is fully up-to-date, **When** the author runs `git fetch`, **Then** the server reports nothing new and zero objects are transferred.
3. **Given** a local clone several commits behind, **When** the author runs `git fetch`, **Then** only the missing commits and their objects are transferred.

---

### Edge Cases

- What happens when the repository is empty (no commits)? The server returns an empty advertisement; the client reports a warning, not a fatal error.
- What happens when the pack is very large? The transfer completes without truncation or timeout.
- What happens if the client sends `have` lines for unknown objects? The server continues negotiation and sends a complete pack for the requested objects.

## Requirements *(mandatory)*

### Functional Requirements

- **FR-001**: The upload-pack endpoint MUST respond to a `want` + `done` request with a valid PACK stream containing all requested objects, even when the client sends no `have` lines.
- **FR-002**: The server MUST send a `NAK` acknowledgement before the PACK data when the client provides no `have` lines (standard fresh-clone behaviour).
- **FR-003**: The server MUST send only the objects the client is missing, computed from the `have` lines in the request.
- **FR-004**: The PACK stream MUST be wrapped in git sideband encoding (channel 1) as required by the git HTTP smart protocol.
- **FR-005**: An empty repository MUST advertise an empty ref list; cloning an empty repository MUST produce an empty local repository without a fatal error.
- **FR-006**: The upload-pack response MUST be correctly framed in pkt-line format throughout.
- **FR-007**: Push (receive-pack) behaviour MUST remain unchanged.

### Key Entities

- **PACK file**: A binary bundle of git objects (commits, trees, blobs, tags) transferred from server to client during clone or fetch.
- **want/have negotiation**: The protocol exchange where the client declares which commits it wants and which it already has, so the server can compute a minimal object set.
- **Sideband encoding**: The multiplexing layer wrapping pack data (channel 1), progress (channel 2), and errors (channel 3) in pkt-lines.

## Success Criteria *(mandatory)*

### Measurable Outcomes

- **SC-001**: `git clone <url>` against a repository with at least one commit completes successfully on the first attempt with zero error messages.
- **SC-002**: `git fetch` when already up-to-date transfers zero objects; `git fetch` when behind transfers only the missing objects.
- **SC-003**: All existing integration tests that exercise the git HTTP path pass without modification.
- **SC-004**: `git push` continues to work correctly; zero regressions introduced to the push path.

## Assumptions

- The fix is scoped to the in-process HTTP smart protocol implementation. No external git binary is involved.
- The git protocol version in use is v1. Protocol v2 is out of scope.
- Authentication and authorisation for clone/fetch are out of scope.

## Dependencies

- spec#012 (smart HTTP API) — closed; provides the HTTP routing layer this fix operates within.
- spec#013 (receive-pack hooks) — closed; push path must remain unaffected.
