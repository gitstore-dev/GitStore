# Feature Specification: Repository Git-Backed Lifecycle, Admission, and Reconciler

**Feature Branch**: `058-repository-git-backed-lifecycle`

**Created**: 2026-08-29
**Status**: Draft
**Input**: User description: "Repository Git-Backed Lifecycle, Admission, and Reconciler. Context: `Repository` has zero git-backed lifecycle today — `createRepository`/`renameRepository`/`transferRepository`/`deleteRepository` are 100% direct datastore writes with no admission dispatch case, no `gitstore-controller-manager/internal/repository` reconciler, and no finalizer/`Terminating` lifecycle, even though `docs/ADRs/0003-repository-lifecycle.md` (status: Proposed) already describes the git-backed design this spec implements. Adopt ADR-0003's Phase 1 scope: git-backed create (and, by the same admission mechanism, update) via a `Repository` manifest pushed to the owning namespace's own `gitstore-system` repository at `repositories/<name>.md`; a foreground-deletion finalizer and `Terminating` lifecycle reusing spec 041's existing `HasCatalogResources` drain check; and a new `gitstore-controller-manager/internal/repository` reconciler with `StorageProvisioned`/`Ready` conditions mirroring `internal/namespace/reconciler.go`. `renameRepository`/`transferRepository` are explicitly deferred and must return `Unimplemented`, matching ADR-0003's own Phase 1 recommendation — reversing the current shipped behavior, which contradicts that recommendation by performing real datastore-only rename/transfer today. No existing GitHub issue tracks this specifically; the closest related, already-closed issue is #249 (Repository Resource Contract, spec 045)."

## User Scenarios & Testing *(mandatory)*

### User Story 1 - Every repository is created and updated as a reviewable Git change (Priority: P1)

Today, `createRepository` writes directly to the datastore with no Git history — a repository's desired configuration cannot be reviewed, diffed, or rolled back the way every other catalog resource (Product, CategoryTaxonomy, Collection, Namespace) already can be. An operator authoring a new repository, or updating an existing one's default branch, visibility, or storage class (upgrade only), needs that change to go through the same reviewable, auditable Git path already adopted for Namespace (spec 046) and every git-backed catalog resource.

**Why this priority**: This is the foundational behavior change this spec exists to deliver, and the one ADR-0003 already documents but the shipped code contradicts. Every other tier of the ownership chain (`Namespace → Repository → Product/...`) is now git-backed except `Repository` itself, which blocks closing ADR-0003 and leaves a structurally inconsistent middle tier.

**Independent Test**: Can be fully tested by pushing a `Repository` manifest to `<namespace>/gitstore-system` for a brand-new name and confirming the repository becomes readable through the API afterward with the pushed spec values and an `AdmissionAccepted=True` condition, then pushing an updated manifest for the same name and confirming the read reflects the new mutable values with an advanced generation — all without needing controller reconciliation to exist yet.

**Acceptance Scenarios**:

1. **Given** a `Repository` manifest for a name that does not yet exist in a namespace, **When** it is pushed to that namespace's own `gitstore-system` repository at `repositories/<name>.md`, **Then** the push is admitted, a repository record is created with the pushed spec values, and its status exposes `AdmissionAccepted=True`.
2. **Given** an existing, non-bootstrap repository, **When** an updated manifest changing only mutable fields (`spec.visibility`, `spec.defaultBranch`, `spec.storageClass` upgrade-only) is pushed for it, **Then** the push is admitted, the repository's spec reflects the new values, and its generation has advanced.
3. **Given** an updated manifest that attempts to change an immutable field (`metadata.name`, `metadata.namespace`) or downgrade `spec.storageClass`, **When** it is pushed, **Then** admission rejects the change and the existing record is left unmodified.
4. **Given** the bootstrap `gitstore-system` repository that every namespace already has (auto-provisioned directly in the datastore at namespace-creation time, per spec 041), **When** the API starts up or a namespace is created, **Then** that repository continues to exist with no corresponding Git commit, and it is available as the authoring target described above.
5. **Given** a `Repository` manifest pushed to any repository other than the target namespace's own `gitstore-system` (including another namespace's `gitstore-system`), **When** the push is evaluated, **Then** it is rejected before admission, and no repository record is created or changed.
6. **Given** a `Repository` manifest pushed while its owning namespace is `Terminating`, **When** the push is evaluated, **Then** admission rejects it and no repository record is created or changed.

---

### User Story 2 - The `createRepository` mutation remains usable without requiring callers to run `git push` themselves, and gains an equivalent `updateRepository` mutation (Priority: P1)

A caller using the GraphQL API (an administrator, the admin console, or an automated integration) needs to create or update a repository without manually constructing a Git commit. `createRepository` continues to exist as a GraphQL mutation and gains a new `updateRepository` sibling; for every non-bootstrap repository, both now work by committing the equivalent manifest to the owning namespace's `gitstore-system` on the caller's behalf and returning the repository only once that commit has been admitted.

**Why this priority**: Equal in importance to User Story 1 — without this, "Git is canonical for Repository" would force every API caller to become a Git client, defeating the purpose of having a GraphQL API. This mirrors exactly how spec 046 made Namespace's Git-canonical write path transparent to existing `createNamespace`/`updateNamespace` callers.

**Independent Test**: Can be fully tested by calling `createRepository` for a new, non-bootstrap name and confirming both that a corresponding commit now exists in the namespace's `gitstore-system` and that the mutation's response reflects the admitted repository, then calling `updateRepository` and confirming a second commit exists and the response reflects the new mutable values — without needing to construct or push a manifest by hand.

**Acceptance Scenarios**:

1. **Given** a `createRepository` call for a new, non-bootstrap repository in an existing, non-`Terminating` namespace, **When** it is submitted, **Then** the API commits the equivalent manifest to that namespace's `gitstore-system`, waits for that commit to be admitted, and returns the resulting repository.
2. **Given** an `updateRepository` call for an existing repository changing only mutable fields, **When** it is submitted, **Then** the API commits an updated manifest to the owning namespace's `gitstore-system`, waits for admission, and returns the repository with its new spec values and advanced generation.
3. **Given** a `createRepository` or `updateRepository` call whose manifest would be rejected by pre-receive or admission validation (e.g., a malformed name, an attempted immutable-field change, or a target namespace that does not exist or is `Terminating`), **When** it is submitted, **Then** the mutation itself is rejected with a reason equivalent to the underlying validation or admission failure — the caller never receives a partially-applied result.
4. **Given** a `createRepository` or `updateRepository` call targeting the bootstrap `gitstore-system` repository name within any namespace, **When** it is submitted, **Then** it is rejected, since bootstrap repositories are managed only by namespace provisioning, not through this mutation path.

---

### User Story 3 - Deleting a repository is safe, ordered, and never silently loses catalog data (Priority: P1)

An operator deletes a repository. The system must never destroy the repository's record before confirming it is safe to do so, and must never leave it in an ambiguous, half-deleted state if something goes wrong partway through. A repository that still contains catalog resources (Product, ProductVariant, CategoryTaxonomy, Collection, File) must be rejected outright (unchanged from spec 041's existing `HasCatalogResources` check); an eligible repository enters a visible "in the process of being deleted" state before its record and its bare Git repository are finally removed, exactly mirroring how Namespace's finalizer-protected deletion already behaves (spec 046) and how ADR-0003 documents Repository's own deletion should behave.

**Why this priority**: Equal in importance to User Stories 1 and 2 — introducing Git-backed writes without an equally careful, non-destructive delete path would trade one class of safety problem (unreviewable writes) for another (unsafe deletes). This is the third leg of a complete lifecycle contract and must ship in the same increment.

**Independent Test**: Can be fully tested by deleting an empty, non-bootstrap repository and observing it pass through a visible in-progress deletion state before disappearing, then separately attempting to delete a repository that still contains catalog resources and confirming it is rejected outright without ever entering that in-progress state.

**Acceptance Scenarios**:

1. **Given** a repository with at least one catalog resource, **When** deletion is requested, **Then** it is rejected immediately, unchanged from the existing rule (spec 041), and the repository never enters an in-progress deletion state.
2. **Given** an empty, non-bootstrap repository, **When** deletion is requested, **Then** the repository is marked as being deleted and becomes visibly distinguishable from a normal, active repository, before its record is eventually removed.
3. **Given** a repository already marked as being deleted, **When** its record is finally removed, **Then** removal only happens once every condition required for safe removal has been satisfied — at minimum, `HasCatalogResources` remains false and the bare Git repository has been confirmed removed from the git-service filesystem — never before.
4. **Given** the bootstrap `gitstore-system` repository of any namespace, **When** deletion is requested, **Then** it is rejected outright while its owning namespace exists; it can never be deleted independently of namespace finalization.
5. **Given** a repository already marked as being deleted, **When** a second deletion request for the same repository is submitted, **Then** it is treated as redundant with the first (not a new, competing deletion), and does not produce a second, independent in-progress deletion attempt.

---

### User Story 4 - `renameRepository` and `transferRepository` return a clear, honest error instead of silently performing an unreviewable mutation, and are visibly signaled as phased out (Priority: P2)

Today, `renameRepository` and `transferRepository` perform real, direct datastore writes — behavior that contradicts ADR-0003's own Phase 1 recommendation ("Not supported in Phase 1. Returns `Unimplemented`"). Once Repository becomes git-backed, a caller invoking either mutation needs two things together: a clear, immediate `Unimplemented` error at runtime (the enforcement half — closing the gap between the documented design and the actual behavior), and a `@deprecated` schema signal visible through introspection (the advance-notice half — matching this project's established convention of proactively signaling phase-out via schema introspection, as spec 045 already did for Repository's own legacy flat output fields, and as the Namespace/CategoryTaxonomy contract specs did before it). Deprecation alone would leave callers unable to discover the change without hitting the runtime error first; the runtime error alone would leave schema-level tooling (codegen, linting, admin-console introspection) with no advance signal that these mutations are being phased out.

**Breaking change / supersession note**: This user story deliberately supersedes spec 045's Acceptance Scenario #4 ("Given the current `createRepository`, `renameRepository`, `transferRepository`, and `deleteRepository` operations, When the declarative contract is introduced, Then those operations continue to succeed and fail under exactly the same conditions as before") and its corresponding SC-003 ("100% of existing repository create, rename, transfer, delete, and read operations continue to succeed or fail under exactly the same conditions as before this schema is introduced"). That invariant held for spec 045 because spec 045 was a read-schema-only change (declarative envelope, no write-path change). Spec 058 is a write-path change, and intentionally breaks that invariant for `renameRepository`/`transferRepository` specifically: both mutations currently succeed via direct datastore writes, and after this spec ships they unconditionally return `Unimplemented` instead. This is recorded here explicitly so a future reader does not mistake spec 058 for an accidental regression against spec 045's stated invariant — it is a deliberate, ADR-0003-directed reversal, scoped to exactly these two mutations, and does not apply to `createRepository`/`deleteRepository`'s success/failure conditions (which remain governed by this spec's own admission rules, not spec 045's).

**Why this priority**: Lower urgency than User Stories 1–3 because it removes functionality rather than adding safety-critical behavior, but it is necessary to avoid a namespace/repository record diverging from Git with no corresponding manifest, which would break the "Git is canonical" invariant User Story 1 establishes.

**Independent Test**: Can be fully tested by calling `renameRepository` and `transferRepository` against an existing repository and confirming both return an `Unimplemented` error, with the repository's record, name, namespace, and Git manifest completely unchanged afterward.

**Acceptance Scenarios**:

1. **Given** an existing repository, **When** `renameRepository` is called against it, **Then** the mutation returns an `Unimplemented` error and the repository's `metadata.name` and Git manifest are unchanged.
2. **Given** an existing repository, **When** `transferRepository` is called against it, **Then** the mutation returns an `Unimplemented` error and the repository's `metadata.namespace` and Git manifest are unchanged.
3. **Given** a caller who previously relied on `renameRepository`/`transferRepository` succeeding, **When** either is called after this change ships, **Then** the caller can distinguish this rejection from a validation failure or a transient error, and can act on the ADR-0003-documented workaround (delete and recreate) instead.
4. **Given** a caller or tool introspecting the GraphQL schema, **When** it inspects the `renameRepository`/`transferRepository` mutation fields, **Then** each is marked `@deprecated` with a reason citing ADR-0003's Phase 2 deferral, independent of whether that caller ever actually invokes either mutation.

---

### User Story 5 - System-computed status can never be set or corrupted through a spec write, and reflects real admission/provisioning outcomes (Priority: P2)

A repository's system-computed status — its conditions, observed generation, and last-applied revision — must remain exclusively system-controlled, and must actually reflect what happened during admission and reconciliation, not a value a caller supplied or a value that was never updated. Today, `RepositoryStatus.conditions` is permanently empty because, as `docs/repository/repository-spec.md` states plainly, "no Repository controller, reconciler, status mutation, or condition-producing writer" exists yet.

**Why this priority**: Lower urgency than User Stories 1–3 because it is an integrity/observability guarantee layered on top of already-correct write and delete behavior, but it is what makes the status conditions introduced by this spec trustworthy rather than decorative.

**Independent Test**: Can be fully tested by reading a repository immediately after a manifest is admitted and confirming its status reflects the admission outcome using only system-computed values, then reading it again after the controller has provisioned the bare Git repository and confirming `StorageProvisioned=True`/`Ready=True` appear with no caller involvement, with no path by which a submitted manifest or mutation input could set any status field directly.

**Acceptance Scenarios**:

1. **Given** a manifest that has just been admitted, **When** the resulting repository's status is read, **Then** it reflects `AdmissionAccepted=True` and an `observedGeneration` matching the generation that was just admitted, using only system-computed values.
2. **Given** a repository manifest that includes a `status` block (which authors are told to omit), **When** it is pushed, **Then** any submitted `status` content is ignored and has no effect on the repository's actual status.
3. **Given** an admitted repository whose bare Git repository has not yet been provisioned on the git-service filesystem, **When** its status is read, **Then** `StorageProvisioned=False` and `Ready=False` until the controller-manager reconciler provisions it.

---

### Edge Cases

- What happens when two updates to the same repository's manifest are pushed to the owning namespace's `gitstore-system` in quick succession? (Admission processes pushes in the order the underlying Git ref updates land; the repository ends up reflecting whichever manifest was admitted last, with generation and resourceVersion advancing monotonically — no update is silently lost or applied out of order relative to the ref history.)
- What happens if `createRepository`/`updateRepository` successfully commits to the owning namespace's `gitstore-system` but the process crashes or times out before admission completes? (The commit already exists in Git and will be admitted on the next admission pass; the mutation caller sees a timeout/error rather than a false failure, and retrying is safe since admission is idempotent per commit.)
- What happens when a `Repository` manifest at `repositories/<name>.md` targets the reserved bootstrap name `gitstore-system` within any namespace? (Rejected at admission — the bootstrap repository is datastore-only and is never re-admitted from Git, exactly mirroring how bootstrap namespaces are never re-admitted in spec 046.)
- What happens when a namespace's own `gitstore-system` repository has not yet finished being provisioned (e.g., immediately after namespace creation, before spec 041's/046's provisioning completes) and a `Repository` manifest is pushed for that namespace? (The push is rejected or deferred consistently with how any push to a not-yet-existent or not-yet-`Ready` repository already behaves elsewhere in the system; this spec does not introduce a new race, it inherits the existing namespace-system-repository-readiness behavior.)
- What happens to a repository's manifest file if the repository is later deleted and, hypothetically, a repository with the same name is created again afterward in the same namespace? (Out of scope for this spec to define name-reuse-after-deletion semantics beyond what already exists; the finalizer/`Terminating` protocol only concerns removing the *current* record, not reserving or releasing the name for future reuse.)
- What happens if `deleteRepository` is requested against a repository whose bare Git repository the git-service cannot currently reach (e.g., transient filesystem/service unavailability)? (Per ADR-0003, the repository remains `Terminating` and the controller retries storage removal with exponential backoff; the finalizer is never removed, and the record is never hard-deleted, until removal is confirmed.)
- What happens when `renameRepository`/`transferRepository` are called against a repository that is already `Terminating`? (Still rejected with `Unimplemented` — the deferred-implementation rejection takes precedence and is unconditional, independent of the target's deletion state.)

## Requirements *(mandatory)*

### Functional Requirements

- **FR-001**: The system MUST continue to auto-provision each namespace's own bootstrap `gitstore-system` repository directly in the datastore, with no corresponding Git commit, as part of namespace creation (unchanged from spec 041) — this remains the sole exception to Repository's otherwise fully git-backed lifecycle.
- **FR-002**: The system MUST treat a namespace's own `gitstore-system` repository as the sole valid authoring target for that namespace's `Repository` manifests, and MUST reject a `Repository` manifest pushed to any other repository — including another namespace's `gitstore-system` — before admission is evaluated.
- **FR-003**: The system MUST admit a `Repository` manifest pushed to the owning namespace's `gitstore-system` for a non-bootstrap, previously-unseen name by creating a corresponding repository record with the manifest's spec values and an `AdmissionAccepted=True` condition.
- **FR-004**: The system MUST admit a `Repository` manifest pushed for an existing non-bootstrap repository by updating only its mutable fields (`spec.visibility`, `spec.defaultBranch`, `spec.storageClass` upgrade-only) and advancing its generation, and MUST reject a manifest that attempts to change an immutable field (`metadata.name`, `metadata.namespace`) or downgrade `spec.storageClass`, using a reason distinguishable from other admission failures.
- **FR-005**: The system MUST reject admission of any `Repository` manifest targeting the reserved bootstrap name `gitstore-system`.
- **FR-006**: The system MUST reject admission of a `Repository` manifest whose owning namespace does not exist or is `Terminating`.
- **FR-007**: The `createRepository` and `updateRepository` GraphQL mutations MUST, for any non-bootstrap repository, commit the equivalent manifest to the owning namespace's `gitstore-system` on the caller's behalf and MUST NOT return a result until that commit has been admitted (successfully or as a rejection the caller can act on).
- **FR-008**: The `createRepository` and `updateRepository` GraphQL mutations MUST reject any request targeting the bootstrap `gitstore-system` repository name.
- **FR-009**: The system MUST ignore any `status` content included in an author-submitted `Repository` manifest — status is never set by an authored manifest, only by the system's own admission and reconciliation.
- **FR-010**: The `renameRepository` and `transferRepository` GraphQL mutations MUST return an `Unimplemented` error and MUST NOT create, update, or otherwise mutate any repository record or Git manifest, reversing their current direct-datastore-write behavior and matching ADR-0003's Phase 1 recommendation. In addition, both mutation fields MUST be marked `@deprecated` in the GraphQL schema, with a reason citing ADR-0003's Phase 2 deferral of a dedicated rename/transfer design — the runtime `Unimplemented` error is the enforcement half of this requirement; the `@deprecated` directive is the advance-notice half, and both MUST ship together, not independently. This FR deliberately supersedes spec 045's Acceptance Scenario #4 and SC-003 for these two mutations specifically (see User Story 4's Breaking change / supersession note) — an intentional, ADR-0003-directed regression against a prior spec's stated invariant, not an oversight.
- **FR-011**: The system MUST reject deletion of a repository that has one or more catalog resources (Product, ProductVariant, CategoryTaxonomy, Collection, File), unchanged from the existing rule (spec 041's `HasCatalogResources` check), and MUST NOT allow such a repository to enter an in-progress deletion state.
- **FR-012**: The system MUST reject deletion of any namespace's bootstrap `gitstore-system` repository unconditionally while its owning namespace exists.
- **FR-013**: The system MUST, upon an eligible deletion request, mark the repository as being deleted (a deletion marker plus a foreground-deletion finalizer) before removing anything else, and MUST make that marked state visibly distinguishable from a normal, active repository when read.
- **FR-014**: The system MUST NOT hard-delete a repository's record until every condition required for safe removal has been satisfied — at minimum: `HasCatalogResources` remains false, and the bare Git repository has been confirmed removed (or archived) from the git-service filesystem.
- **FR-015**: The system MUST treat a deletion request against a repository already marked as being deleted as redundant with the repository's existing in-progress deletion, not as a new, independently-tracked deletion attempt.
- **FR-016**: The system MUST expose, via status conditions, whether a repository's bare Git repository has been successfully provisioned on the git-service filesystem (`StorageProvisioned`) and whether the repository is otherwise fully operational (`Ready`), independent of whether it is currently being deleted.
- **FR-017**: The system MUST advance a repository's version marker on every successful admitted change (create, update, or status/condition change), and MUST advance its generation only for author-controlled spec changes (create or update), not for status-only or deletion-marker changes.
- **FR-018**: The system MUST NOT provision the bare Git repository on the git-service filesystem synchronously within the admission request path; provisioning MUST occur asynchronously via the controller-manager reconciler, mirroring how per-namespace system-repository provisioning is handled for Namespace (spec 046).

### Key Entities

- **Repository manifest**: The Git-authored desired-state representation of a non-bootstrap repository, stored at `repositories/<name>.md` within the owning namespace's own `gitstore-system` repository, and admitted through the same pre-receive/admission pipeline already used for Product, ProductVariant, CategoryTaxonomy, Collection, and Namespace.
- **Bootstrap repository**: The well-known `gitstore-system` repository that every namespace already has, auto-provisioned directly in the datastore at namespace-creation time (spec 041), which can never be deleted while its namespace exists and is never re-admitted from a manifest.
- **In-progress deletion (Terminating)**: The state a repository enters once an eligible deletion request has been accepted and a foreground-deletion finalizer attached, prior to its record and bare Git repository being finally removed. Distinguishable from a normal, active repository when read.
- **Repository status conditions**: System-computed observations about a repository's admission and operational state (at minimum: admission acceptance, storage-provisioning readiness, overall readiness, and in-progress-deletion state), never settable by an authored manifest or mutation input.

### Explicit mutation envelope contract

`createRepository` and `updateRepository` use the declarative resource envelope already established for `Repository` manifest authoring and for Namespace's own mutation contract (spec 046) — not the legacy flat input shape `createRepository` uses today. The authoritative request object is:

```graphql
input CreateRepositoryInput {
  apiVersion: String!
  kind: String!
  metadata: RepositoryMetadataInput!
  spec: RepositorySpecInput!
}

input UpdateRepositoryInput {
  apiVersion: String!
  kind: String!
  metadata: RepositoryMetadataInput!
  spec: RepositorySpecInput!
}
```

The required fields are intentionally aligned to the manifest schema:
- `apiVersion`: resource contract version for the `Repository` kind
- `kind`: must be `Repository`
- `metadata.name`: repository identifier; the bootstrap name `gitstore-system` is rejected
- `metadata.namespace`: owning namespace identifier; immutable after creation for `updateRepository`
- `spec`: author-controlled desired state (`defaultBranch`, `visibility`, `storageClass` upgrade-only)

`renameRepository`, `transferRepository`, and `deleteRepository` keep their existing input shapes (`RenameRepositoryInput`, `TransferRepositoryInput`, `DeleteRepositoryInput` are unchanged) — this spec does not remove either mutation field from the public schema. Two things change for `renameRepository`/`transferRepository` specifically (FR-010): their resolver *behavior* (now unconditional `Unimplemented`) and their schema *annotation* (`@deprecated(reason: "...")`, citing ADR-0003's Phase 2 deferral). Both changes ship together.

### Status condition matrix

| Condition            | Source of truth                                   | Set by                | Read semantics                                                                     |
|-----------------------|---------------------------------------------------|-----------------------|--------------------------------------------------------------------------------------|
| `AdmissionAccepted`  | datastore `Status.conditions` / admission output  | admission pipeline    | `true` only after the repository's current spec was successfully admitted           |
| `StorageProvisioned` | datastore `Status.conditions` / controller output | controller reconciler | `true` when the repository's bare Git repository exists on the git-service filesystem |
| `Ready`              | datastore `Status.conditions` / controller output | controller reconciler | aggregate operational readiness; `AdmissionAccepted && StorageProvisioned`           |
| `Terminating`        | `DeletionTimestamp` + `Finalizers`                | system deletion flow  | derived for reads while a repository is undergoing foreground deletion              |

Author-submitted manifests may include a `status` block, but it is ignored. The system owns the values for `status`, `observedGeneration`, and `resourceVersion` after admission. `StorageProvisioned` deliberately uses ADR-0003's own condition name (not `SystemRepoReady`, which is Namespace-specific to spec 046) since a `Repository`'s own storage is the bare Git repository on disk, not another repository.

## Success Criteria *(mandatory)*

### Measurable Outcomes

- **SC-001**: 100% of non-bootstrap repository creates and updates, whether initiated via `git push` or via the `createRepository`/`updateRepository` mutations, result in a corresponding Git commit to the owning namespace's `gitstore-system` before the repository's spec reflects the change.
- **SC-002**: 100% of `Repository` manifests pushed to a repository other than the target namespace's own `gitstore-system` are rejected before admission, with zero instances of a repository record being created or changed from such a push.
- **SC-003**: 100% of deletion attempts against a repository that still contains catalog resources are rejected, with zero instances of such a repository entering an in-progress deletion state.
- **SC-004**: 100% of deletion attempts against any namespace's bootstrap `gitstore-system` repository are rejected while that namespace exists, with zero exceptions.
- **SC-005**: 100% of eligible deletions pass through a visibly distinguishable in-progress deletion state before the repository's record is removed, with zero instances of a repository record disappearing without first being observable in that state.
- **SC-006**: 100% of successfully admitted repository changes leave status fields reflecting only system-computed values, with zero instances of caller- or manifest-supplied content appearing in status.
- **SC-007**: 100% of `renameRepository`/`transferRepository` calls after this change ships return `Unimplemented` with zero instances of a repository's name, namespace, or Git manifest being mutated by either call, and 100% of schema introspection queries against either mutation field surface a `@deprecated` directive with a non-empty reason.

## Assumptions

- This spec adopts `docs/ADRs/0003-repository-lifecycle.md`'s Phase 1 scope for create, update, and delete, mirroring the precedent set by spec 046's full adoption of ADR-0002 for Namespace — the narrower alternative (Git as an audit trail only, synchronous datastore write in the same call, no new admission kind or controller) is available but is not chosen here, for the same consistency reasons spec 046 recorded: keeping Repository's contract aligned with every other catalog resource's reviewable/auditable/rollback-capable behavior rather than leaving it, alone among the ownership chain `Namespace → Repository → Product/...`, as a direct-datastore-write special case.
- `renameRepository` and `transferRepository` are explicitly out of scope for implementation beyond returning `Unimplemented` and being marked `@deprecated` (FR-010); this reverses today's shipped behavior (which performs real datastore-only rename/transfer with no deprecation signal) to match ADR-0003's own Phase 1 recommendation, per explicit direction for this spec. ADR-0003's Phase 2 plan for a dedicated transfer operation, and any future rename design informed by `docs/implementation/010-repo-storage-identity.md`, remain out of scope here.
- **Supersedes spec 045's Acceptance Scenario #4 / SC-003 (breaking change, intentional)**: spec 045 stated that "the current `createRepository`, `renameRepository`, `transferRepository`, and `deleteRepository` operations ... continue to succeed and fail under exactly the same conditions as before" its declarative-contract change, and SC-003 measured 100% conformance to that invariant. Spec 045 was a read-schema-only change, so that invariant was true for it. Spec 058 is a write-path change and deliberately breaks that invariant for `renameRepository`/`transferRepository` only: both currently succeed via direct datastore writes; after this spec ships, both unconditionally return `Unimplemented`. This is an intentional, ADR-0003-directed regression against spec 045's stated invariant, not an oversight or a contradiction between the two specs — see User Story 4 and FR-010 for the full rationale. `createRepository`'s and `deleteRepository`'s success/failure conditions are governed by this spec's own admission and finalizer rules (User Stories 1–3), not by spec 045's invariant, and are not part of this supersession.
- `updateRepository` does not exist as a GraphQL mutation today (only `createRepository`, `renameRepository`, `transferRepository`, and `deleteRepository` are defined in `shared/schemas/repository.graphqls`). This spec adds it as a new mutation because the same admission mechanism this spec introduces for git-backed `create` inherently must also define what happens when a manifest for an existing name is re-pushed (i.e., "update"), and ADR-0003's own "Update" section and GraphQL mutation delegation table already describe this mutation's intended behavior. Introducing it now, rather than leaving update reachable only via `git push`, keeps Repository consistent with Namespace's mutation surface (spec 046) and closes a gap ADR-0003 already anticipated.
- The `Repository` datastore entity already carries the `Generation`, `ResourceVersion`, `Status`, `DeletionTimestamp`, and `Finalizers` fields needed for this lifecycle (added by spec 045, currently unused for any real write/condition/finalizer semantics) — no new persisted fields are required; this spec is the first to give those existing fields real lifecycle meaning for `Repository`, exactly as spec 046 was the first to do so for the equivalent `Namespace` fields.
- The finalizer-drain condition for repository deletion is exactly "zero catalog resources remain" (spec 041's existing `HasCatalogResources` check, scoped by `repositoryID`) plus confirmed bare-repository removal from the git-service filesystem — this spec does not depend on any other resource's own finalizer/`Terminating` implementation.
- `Suspended` (ADR-0003's operator-set "readable but push-rejected" condition) is out of scope for this spec; it is an orthogonal, later feature and is not required to implement create/update/delete lifecycle or the `StorageProvisioned`/`Ready` reconciler conditions this spec delivers.
- No existing GitHub issue tracks "Repository git-backed lifecycle" specifically as of this spec's creation. The closest related issues are #249 ("Repository Resource Contract: Kubernetes-style Declarative Markdown Schema", spec 045, closed — established the persisted fields this spec now gives lifecycle meaning to) and #173 ("Namespace Validation and Admission Matrix", spec 047's analog for Namespace, open) — neither tracks this spec's scope directly. A tracking issue is not created as part of writing this spec; one should be opened (or this spec linked to a new one) before `/speckit.plan`/`/speckit.tasks` work is treated as committed.
- "Repository Validation and Admission Matrix" (the structural-vs-policy validation rule catalogue mirroring spec 047 for Namespace) and "Repository Watch Contract" (mirroring GH#174's `watchNamespaces` pattern) both depend on this spec landing first and are explicitly out of scope here — they are separate, future specs, matching how spec 047 and GH#174 were sequenced after spec 046 for Namespace.
- Pre-receive/admission validation details beyond "which repository is a valid authoring target," "namespace must exist and not be `Terminating`," and "immutable-field/storage-class-downgrade rejection on update" (name-format validation, uniqueness, limits, full condition vocabulary beyond what this spec's status condition matrix defines) are left to the future "Repository Validation and Admission Matrix" spec noted above, consistent with how spec 047 was scoped relative to spec 046 for Namespace.
- Repository name reuse after a full deletion, and any retention/audit-log requirements for deleted repository manifests, are out of scope for this spec.
