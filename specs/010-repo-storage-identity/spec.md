# Feature Specification: Repository Storage Identity and Path Strategy

**Feature Branch**: `010-repo-storage-identity`  
**Created**: 2026-05-26  
**Status**: Draft  
**Input**: GH#70 — GitLab-style repository storage identity and path strategy, with subtasks #71–#75. No backwards compatibility required (early ALPHA).

## Clarifications

### Session 2026-05-26

- Q: What is the key structure of the namespace-to-`repo_id` mapping — is namespace a storage concern or a routing/authorization concern? → A: Namespace is routing and authorization metadata only. Storage is keyed solely on `repo_id`. The mapping is keyed by `(namespace_id, repo-name)` → `repo_id`, mirroring GitLab Gitaly where group/namespace hierarchy is not part of the storage path.
- Q: What are the active protocol integration points for this initiative? → A: gRPC (#65, complete) is the sole inter-service communication mechanism. HTTP Smart HTTP moves to `gitstore-api` in #103; SSH is planned in `gitstore-api` in #104. Both are `gitstore-api`-side handlers that delegate to `gitstore-git-service` via gRPC. Neither is implemented yet. This initiative defines the lookup contract that both will consume.
- Q: Which service owns the `(namespace, repo-name) → repo_id` lookup? → A: `gitstore-api` holds and queries the mapping, resolves to `repo_id`, and passes the resolved `repo_id` in the gRPC call to `gitstore-git-service`. `gitstore-git-service` never sees a namespace string.
- Q: What type is `namespace_id` in the NamespaceMapping key? → A: Stable UUID assigned at namespace creation — same pattern as `repo_id`. This means a namespace rename requires no changes to NamespaceMapping records; only the namespace's own name record changes.
- Q: What migration complexity assumption applies? → A: No assumption; user does not care about migration complexity constraints in the current ALPHA. FR-011 retains the requirement that a migration path must exist but carries no operational complexity constraint.

---

## User Scenarios & Testing *(mandatory)*

### User Story 1 — Rename a repository without moving data (Priority: P1)

A namespace owner renames a repository from `acme/my-catalog` to `acme/product-catalog`. The physical storage on disk does not move and all outstanding git clones pointing at the new path continue to work immediately after the rename.

**Why this priority**: The rename-without-move guarantee is the foundational user-visible promise of the entire initiative. Without it, namespace management (feature #39/#170) is incomplete.

**Independent Test**: Rename a repository via the API, verify the old storage path remains unchanged on disk, and confirm that a fresh `git clone` using the new name succeeds.

**Acceptance Scenarios**:

1. **Given** a repository `acme/my-catalog` exists with commits, **When** the owner renames it to `acme/product-catalog`, **Then** the physical storage location is unchanged and `git clone acme/product-catalog` succeeds immediately.
2. **Given** a renamed repository, **When** a client tries to clone via the old name, **Then** the system returns a clear "repository not found" response, not a stale filesystem path error.

---

### User Story 2 — Resolve a repository by namespace path (Priority: P1)

A protocol handler in `gitstore-api` receives an inbound git request for `acme/my-catalog` (over HTTP Smart HTTP or, once implemented, SSH). `gitstore-api` resolves the namespace path to an internal `repo_id`, then delegates the git operation to `gitstore-git-service` via gRPC, passing the resolved `repo_id`. `gitstore-git-service` derives the storage path from `repo_id` and serves the operation.

**Why this priority**: This is the core lookup flow. Every git protocol request (clone, fetch, push) depends on it.

**Independent Test**: Send a `git fetch` over HTTP for a known namespace path and confirm the operation is delegated to `gitstore-git-service` with a pre-resolved `repo_id`, not a namespace string.

**Acceptance Scenarios**:

1. **Given** a repository with namespace path `alice/configs`, **When** a `git clone https://.../alice/configs.git` request arrives, **Then** the system resolves the internal ID, derives the storage path, and serves the pack data.
2. **Given** a repository whose namespace was transferred from `alice/configs` to `bob/configs`, **When** a new clone request for `bob/configs` arrives, **Then** the same internal ID and storage path are used without any filesystem move.
3. **Given** a request for `unknown/missing`, **When** the lookup finds no mapping, **Then** the system returns a "repository not found" response with a clear error message.

---

### User Story 3 — Transfer a repository to a different namespace (Priority: P2)

A platform administrator transfers a repository from one namespace to another (e.g., from a user namespace to an organisation namespace). The repository's internal identity is preserved, disk data is not copied, and access control is updated to reflect the new namespace.

**Why this priority**: Transfer is the second rename-class operation. It validates that namespace is a mutable label, not the storage ground truth.

**Independent Test**: Transfer a repository between two namespaces; verify the storage path is unchanged, the new namespace resolves to the same internal ID, and the old namespace mapping is invalidated.

**Acceptance Scenarios**:

1. **Given** `user-alice/app` transferred to `org-engineering/app`, **When** a clone is attempted via `org-engineering/app`, **Then** it succeeds and serves the same repository data as before.
2. **Given** the same transfer, **When** a clone is attempted via the old path `user-alice/app`, **Then** a clear "not found" response is returned.

---

### User Story 4 — Operator inspects storage location for a repository (Priority: P3)

An operator debugging a storage issue wants to find the physical path for a given repository. They can look up the repository's internal ID and derive or query the exact storage path without guessing from namespace strings.

**Why this priority**: Operational visibility supports day-to-day maintenance after the core routing works, but is not blocking user-facing flows.

**Independent Test**: Given a repository's namespace path, an operator can retrieve its internal ID and storage path via an admin query or CLI command.

**Acceptance Scenarios**:

1. **Given** repository `alice/configs`, **When** an operator runs an admin lookup, **Then** they receive the `repo_id` and the filesystem path within one minute.
2. **Given** a `repo_id`, **When** an operator queries the path resolver, **Then** a stable, deterministic path is returned regardless of any namespace renames that occurred.

---

### Edge Cases

- What happens when two namespace paths race to claim the same repository name simultaneously?
- How does the system behave if the storage path exists on disk but the namespace mapping record is missing (orphaned storage)?
- What is returned when the `repo_id` used in path derivation is malformed or truncated?
- How does the system handle a namespace path containing path-traversal sequences (e.g., `../etc`)?
- What happens during partial creation where the namespace mapping is written but the storage directory is not yet initialised?

## Requirements *(mandatory)*

### Functional Requirements

- **FR-001**: The system MUST maintain two distinct identity layers for every repository: an external namespace path (`namespace/repo-name`) and a stable internal identifier (`repo_id`).
- **FR-002**: The `repo_id` MUST be assigned at repository creation time and MUST NOT change for the lifetime of the repository.
- **FR-003**: The system MUST derive the physical storage path from `repo_id` using a deterministic fanout strategy (path-segmented layout), not from the namespace path.
- **FR-004**: Each namespace MUST maintain a mapping of `repo-name` to `repo_id` (keyed by `(namespace_id, repo-name)`, where `namespace_id` is the namespace's stable UUID). A repo rename updates the `repo-name` key; a transfer updates the `namespace_id` key; neither operation modifies storage. Storage resolution uses `repo_id` only; namespace is routing and authorization metadata.
- **FR-005**: `gitstore-api` MUST resolve the inbound `(namespace, repo-name)` to a `repo_id` before delegating any git operation to `gitstore-git-service` via gRPC. `gitstore-git-service` MUST accept only pre-resolved `repo_id` values; it MUST NOT receive or interpret namespace strings.
- **FR-006**: A rename operation MUST update only the namespace mapping and MUST NOT move or copy repository data on disk.
- **FR-007**: A transfer operation MUST update the namespace mapping and the ownership reference, and MUST NOT move or copy repository data on disk.
- **FR-008**: The system MUST return a clear "repository not found" response when a namespace-to-`repo_id` mapping does not exist.
- **FR-009**: The path resolver MUST produce collision-free, stable storage paths for distinct `repo_id` values.
- **FR-010**: The system MUST reject namespace path inputs that contain path-traversal sequences.
- **FR-011**: A migration path MUST exist for existing repositories currently stored using namespace-derived paths, covering both the data layout change and the mapping metadata records.
- **FR-012**: Operational documentation MUST describe the storage layout, the lookup flow, common failure modes, and debugging procedures.

### Key Entities

- **Repository**: A git repository with two identity layers: `namespace/repo-name` (external, mutable) and `repo_id` (internal, immutable stable identifier).
- **Namespace**: A logical owner scope (user, organisation, or enterprise). Has a stable `namespace_id` (UUID, assigned at creation, immutable) and a mutable human-readable slug. Holds a collection of repository name-to-`repo_id` mappings.
- **NamespaceMapping**: The record binding `(namespace_id, repo-name)` to a `repo_id`, where `namespace_id` is the stable UUID of the namespace. Created at repository creation; the `repo-name` component is updated on rename; the `namespace_id` component is updated (to the target namespace's UUID) on transfer; deleted on repository deletion. A namespace slug rename requires no mapping record changes.
- **StoragePath**: The deterministic filesystem path derived from `repo_id` using a fanout strategy (e.g., first two character segments of the ID as subdirectory levels).
- **StorageClass**: A tag on a repository indicating which storage root or tier it resides on (supports future multi-storage-root expansion).

## Success Criteria *(mandatory)*

### Measurable Outcomes

- **SC-001**: A repository rename completes without any data movement; operators can verify the physical storage path is identical before and after the rename.
- **SC-002**: A `git clone` using an updated namespace path succeeds within the same response-time envelope as a clone using the original path — no meaningful latency regression from the added lookup step.
- **SC-003**: The path resolver produces a stable, deterministic output for any given `repo_id` — the same input always yields the same output, verified by an automated test suite with no collisions observed.
- **SC-004**: The gRPC contract between `gitstore-api` and `gitstore-git-service` passes integration tests confirming that only `repo_id` values cross the service boundary — no namespace strings. The same lookup contract is consumed by the HTTP Smart HTTP layer (#103) and the SSH layer (#104) without modification.
- **SC-005**: A test-environment migration from the current namespace-path layout to internal-ID layout completes with zero data loss, confirmed by post-migration `git fsck` on all migrated repositories.
- **SC-006**: An operator can determine the physical location of any repository from its namespace path using documented admin tooling in under one minute.

## Assumptions

- `repo_id` will be a UUID; the exact variant (v4 or v7) is a planning-phase decision.
- Fanout depth follows GitLab convention (two-character segments), but the exact depth is confirmed during planning.
- The system uses a single storage root per node in the current ALPHA scope; multi-root sharding is explicitly out of scope.
- No backwards-compatible wire-protocol changes are required: the application is early ALPHA and existing clients are internal or test-only.
- The namespace mapping store uses the existing datastore abstraction from feature #006 (`go-memdb` for development, ScyllaDB for production).
- The gRPC interface between `gitstore-api` and `gitstore-git-service` was established in feature #65 and is the sole communication mechanism between the two services. `gitstore-git-service` is a pure git handler: no HTTP transport, no namespace awareness.
- All protocol entry points (HTTP Smart HTTP in #103, SSH in #104) live in `gitstore-api` and delegate to `gitstore-git-service` via gRPC. Neither protocol is fully implemented yet; this initiative defines the lookup contract they will consume.
- `gitstore-api` owns the NamespaceMapping store and performs all `(namespace, repo-name) → repo_id` resolution before making gRPC calls. `gitstore-git-service` operates only on `repo_id`.
