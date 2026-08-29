# Feature Specification: Namespace API Semantics: Spec Writes, Status Updates, Concurrency

**Feature Branch**: `046-namespace-api-semantics`

**Created**: 2026-08-17
**Status**: Closed
**Input**: User description: "Namespace API Semantics: Spec Writes, Status Updates, Concurrency (GH#172). Context: GH#172 is unblocked now that GH#171 (Namespace declarative .spec/.status schema) has shipped. Define namespace API semantics for .spec updates, status-subresource style .status updates, and optimistic concurrency behavior. Scope: define create/update/delete behavior and error contracts, define the status update path and ownership boundaries, define resourceVersion conflict semantics for concurrent writes, and provide API examples for successful and rejected operations. GH#172 blocks GH#174 (Namespace Watch Contract)."

## Clarifications

### Session 2026-08-17

- Q: What is the canonical write path for Namespace create, update, and delete? → A: Git is canonical (per `docs/ADRs/0002-namespace-lifecycle.md`, adopted in full). Two well-known **bootstrap namespaces** (`gitstore-system`, `default`) are created directly in the datastore at API startup, with no git commit. Every other namespace is authored as a manifest committed to the `gitstore-system` namespace's own `gitstore-system` repository (`gitstore-system/gitstore-system`) and admitted through the same pre-receive/post-receive pipeline already used for Product, ProductVariant, CategoryTaxonomy, and Collection.
- Q: How does this interact with spec 047 (GH#173), which assumed no "Terminating" lifecycle state exists? → A: This spec introduces the first real "Terminating"/finalizer implementation in the codebase. Spec 047 is updated in the same round to drop its now-stale "no Terminating state" assumption and align its validation/admission matrix with this spec's lifecycle rules.

## User Scenarios & Testing *(mandatory)*

### User Story 1 - Every non-bootstrap namespace is created and updated as a reviewable Git change (Priority: P1)

Today, `createNamespace` writes directly to the datastore with no Git history — a namespace's desired configuration cannot be reviewed, diffed, or rolled back the way every other catalog resource (Product, CategoryTaxonomy, Collection) already can be. An operator authoring a new namespace, or updating an existing one's title, tier-adjacent defaults, or policy defaults, needs that change to go through the same reviewable, auditable Git path as any other declarative GitStore resource.

**Why this priority**: This is the foundational behavior change GH#172 exists to deliver, and the one the recorded Clarification makes binding: Git becomes the canonical source of desired state for Namespace, matching the architecture already documented for every other resource kind and closing the gap called out in ADR-0002.

**Independent Test**: Can be fully tested by pushing a `Namespace` manifest to `gitstore-system/gitstore-system` for a brand-new name and confirming the namespace becomes readable through the API afterward with the pushed spec values and an `AdmissionAccepted=True` condition, then pushing an updated manifest for the same name and confirming the read reflects the new values with an advanced generation — all without needing controller reconciliation or watch behavior to exist yet.

**Acceptance Scenarios**:

1. **Given** a `Namespace` manifest for a name that does not yet exist, **When** it is pushed to `gitstore-system/gitstore-system`, **Then** the push is admitted, a namespace record is created with the pushed spec values, and its status exposes `AdmissionAccepted=True`.
2. **Given** an existing namespace, **When** an updated manifest for it is pushed to `gitstore-system/gitstore-system`, **Then** the push is admitted, the namespace's spec reflects the new values, and its generation has advanced.
3. **Given** the two well-known bootstrap namespaces (`gitstore-system`, `default`), **When** the API starts up, **Then** both exist in the datastore with no corresponding Git commit, and their own `gitstore-system` repositories exist so that `gitstore-system/gitstore-system` is available as the authoring target described above.
4. **Given** a `Namespace` manifest pushed to any repository other than `gitstore-system/gitstore-system`, **When** the push is evaluated, **Then** it is rejected before admission, and no namespace record is created or changed.

---

### User Story 2 - The `createNamespace` and `updateNamespace` mutations remain usable without requiring callers to run `git push` themselves (Priority: P1)

A caller using the GraphQL API (an administrator, the admin console, or an automated integration) needs to create or update a namespace without manually constructing a Git commit. `createNamespace` and `updateNamespace` continue to exist as GraphQL mutations, but for every namespace other than the two bootstrap namespaces, they now work by committing the equivalent manifest to `gitstore-system/gitstore-system` on the caller's behalf and returning the namespace only once that commit has been admitted.

**Why this priority**: Equal in importance to User Story 1 — without this, "Git is canonical" would force every API caller to become a Git client, which defeats the purpose of having a GraphQL API at all. This mutation-delegates-to-Git behavior is what makes the architectural change transparent to existing callers.

**Independent Test**: Can be fully tested by calling `createNamespace` for a new, non-bootstrap name and confirming both that a corresponding commit now exists in `gitstore-system/gitstore-system` and that the mutation's response reflects the admitted namespace, then calling `updateNamespace` and confirming a second commit exists and the response reflects the new values — without needing to construct or push a manifest by hand.

**Acceptance Scenarios**:

1. **Given** a `createNamespace` call for a new, non-bootstrap namespace, **When** it is submitted, **Then** the API commits the equivalent manifest to `gitstore-system/gitstore-system`, waits for that commit to be admitted, and returns the resulting namespace.
2. **Given** an `updateNamespace` call for an existing namespace, **When** it is submitted, **Then** the API commits an updated manifest to `gitstore-system/gitstore-system`, waits for admission, and returns the namespace with its new spec values and advanced generation.
3. **Given** a `createNamespace` or `updateNamespace` call whose manifest would be rejected by pre-receive or admission validation (e.g., a malformed name, or an attempted tier demotion), **When** it is submitted, **Then** the mutation itself is rejected with a reason equivalent to the underlying validation or admission failure — the caller never receives a partially-applied result.
4. **Given** a `createNamespace` or `updateNamespace` call for one of the two bootstrap namespaces, **When** it is submitted, **Then** it is rejected, since bootstrap namespaces are managed only at API startup, not through this mutation path.

---

### User Story 3 - Deleting a namespace is safe, ordered, and never silently loses in-progress work (Priority: P1)

An operator deletes a namespace. The system must never destroy the namespace's record before confirming it is safe to do so, and must never leave it in an ambiguous, half-deleted state if something goes wrong partway through. A namespace that still owns repositories must be rejected outright (unchanged from spec 041); an eligible namespace enters a visible "in the process of being deleted" state before its record is finally removed, exactly mirroring how every other finalizer-protected resource in this architecture is expected to behave.

**Why this priority**: Equal in importance to User Stories 1 and 2 — introducing Git-backed writes without an equally careful, non-destructive delete path would trade one class of safety problem (unreviewable writes) for another (unsafe deletes). This is the third leg of a complete lifecycle contract and must ship in the same increment.

**Independent Test**: Can be fully tested by deleting an empty, non-bootstrap namespace and observing it pass through a visible in-progress deletion state before disappearing, then separately attempting to delete a namespace that still owns a repository and confirming it is rejected outright without ever entering that in-progress state.

**Acceptance Scenarios**:

1. **Given** a namespace with at least one repository, **When** deletion is requested, **Then** it is rejected immediately, unchanged from the existing rule (spec 041), and the namespace never enters an in-progress deletion state.
2. **Given** an empty, non-bootstrap namespace, **When** deletion is requested, **Then** the namespace is marked as being deleted and becomes visibly distinguishable from a normal, active namespace, before its record is eventually removed.
3. **Given** a namespace already marked as being deleted, **When** its record is finally removed, **Then** removal only happens once every condition required for safe removal has been satisfied — never before.
4. **Given** one of the two bootstrap namespaces, **When** deletion is requested, **Then** it is rejected outright; bootstrap namespaces can never be deleted.
5. **Given** a namespace already marked as being deleted, **When** a second deletion request for the same namespace is submitted, **Then** it is treated as redundant with the first (not a new, competing deletion), and does not produce a second, independent in-progress deletion attempt.

---

### User Story 4 - System-computed status can never be set or corrupted through a spec write, and reflects real admission/reconciliation outcomes (Priority: P2)

A namespace's system-computed status — its conditions, observed generation, and last-applied revision — must remain exclusively system-controlled, and must actually reflect what happened during admission and reconciliation, not a value a caller supplied or a value that was never updated.

**Why this priority**: Lower urgency than User Stories 1–3 because it is an integrity/observability guarantee layered on top of already-correct write and delete behavior, but it is what makes the status conditions introduced by this spec trustworthy rather than decorative.

**Independent Test**: Can be fully tested by reading a namespace immediately after a manifest is admitted and confirming its status reflects the admission outcome using only values the system computed, with no path by which a submitted manifest or mutation input could set a status field directly.

**Acceptance Scenarios**:

1. **Given** a manifest that has just been admitted, **When** the resulting namespace's status is read, **Then** it reflects `AdmissionAccepted=True` and an `observedGeneration` matching the generation that was just admitted, using only system-computed values.
2. **Given** a namespace manifest that includes a `status` block (which authors are told to omit), **When** it is pushed, **Then** any submitted `status` content is ignored and has no effect on the namespace's actual status.

---

### Edge Cases

- What happens when two updates to the same namespace's manifest are pushed to `gitstore-system/gitstore-system` in quick succession? (Admission processes pushes in the order the underlying Git ref updates land; the namespace ends up reflecting whichever manifest was admitted last, with generation and resourceVersion advancing monotonically — no update is silently lost or applied out of order relative to the ref history.)
- What happens if `createNamespace`/`updateNamespace` successfully commits to `gitstore-system/gitstore-system` but the process crashes or times out before admission completes? (The commit already exists in Git and will be admitted on the next admission pass; the mutation caller sees a timeout/error rather than a false failure, and retrying is safe since admission is idempotent per commit.)
- What happens to a namespace's manifest file if the namespace is later deleted and, hypothetically, a namespace with the same name is created again afterward? (Out of scope for this spec to define name-reuse-after-deletion semantics beyond what already exists; the finalizer/Terminating protocol only concerns removing the *current* record, not reserving or releasing the name for future reuse.)
- What happens when a `Namespace` manifest is pushed to `gitstore-system/gitstore-system` for one of the two bootstrap namespace names? (Rejected at admission — bootstrap namespaces are datastore-only and are never re-admitted from Git.)
- What happens when the `gitstore-system/gitstore-system` repository itself does not yet exist (e.g., very first API startup)? (Bootstrap creates it as part of provisioning the `gitstore-system` bootstrap namespace's own system repository, before any namespace manifest can be pushed or committed.)

## Requirements *(mandatory)*

### Functional Requirements

- **FR-001**: The system MUST create exactly two bootstrap namespaces (`gitstore-system` and `default`) directly in the datastore at API startup, with no corresponding Git commit, and MUST provision each bootstrap namespace's own `gitstore-system` repository as part of that startup process.
- **FR-002**: The system MUST treat `gitstore-system/gitstore-system` as the sole valid authoring target for `Namespace` manifests, and MUST reject a `Namespace` manifest pushed to any other repository before admission is evaluated.
- **FR-003**: The system MUST admit a `Namespace` manifest pushed to `gitstore-system/gitstore-system` for a non-bootstrap, previously-unseen name by creating a corresponding namespace record with the manifest's spec values and an `AdmissionAccepted=True` condition.
- **FR-004**: The system MUST admit a `Namespace` manifest pushed to `gitstore-system/gitstore-system` for an existing non-bootstrap namespace by updating that namespace's spec to the manifest's values and advancing its generation.
- **FR-005**: The system MUST reject admission of a `Namespace` manifest for either bootstrap namespace name.
- **FR-006**: The system MUST reject admission of a `Namespace` manifest that attempts to demote an existing namespace's tier, using a reason distinguishable from other admission failures.
- **FR-007**: The `createNamespace` and `updateNamespace` GraphQL mutations MUST, for any non-bootstrap namespace, commit the equivalent manifest to `gitstore-system/gitstore-system` on the caller's behalf and MUST NOT return a result until that commit has been admitted (successfully or as a rejection the caller can act on).
- **FR-008**: The `createNamespace` and `updateNamespace` GraphQL mutations MUST reject any request targeting either bootstrap namespace name.
- **FR-009**: The system MUST ignore any `status` content included in an author-submitted `Namespace` manifest — status is never set by an authored manifest, only by the system's own admission and reconciliation.
- **FR-010**: The system MUST reject deletion of a namespace that owns one or more repositories, unchanged from the existing rule (spec 041), and MUST NOT allow such a namespace to enter an in-progress deletion state.
- **FR-011**: The system MUST reject deletion of either bootstrap namespace unconditionally.
- **FR-012**: The system MUST, upon an eligible deletion request, mark the namespace as being deleted (a deletion marker plus a foreground-deletion finalizer) before removing anything else, and MUST make that marked state visibly distinguishable from a normal, active namespace when read.
- **FR-013**: The system MUST NOT hard-delete a namespace's record until every condition required for safe removal (at minimum: the finalizer-drain condition already established by spec 041 — zero repositories) has been satisfied.
- **FR-014**: The system MUST treat a deletion request against a namespace already marked as being deleted as redundant with the namespace's existing in-progress deletion, not as a new, independently-tracked deletion attempt.
- **FR-015**: The system MUST expose, via status conditions, whether a namespace's own system repository has been successfully provisioned and whether the namespace is otherwise fully operational, independent of whether it is currently being deleted.
- **FR-016**: The system MUST advance a namespace's version marker on every successful admitted change (create, update, or status/condition change), and MUST advance its generation only for author-controlled spec changes (create or update), not for status-only or deletion-marker changes.

### Key Entities

- **Bootstrap namespace**: One of exactly two well-known namespaces (`gitstore-system`, `default`) created directly in the datastore at API startup with no Git history, and which can never be deleted or re-admitted from a manifest.
- **Namespace manifest**: The Git-authored desired-state representation of a non-bootstrap namespace, stored at a well-known path within `gitstore-system/gitstore-system` and admitted through the same pre-receive/admission pipeline as other catalog resources.
- **In-progress deletion (Terminating)**: The state a namespace enters once an eligible deletion request has been accepted and a foreground-deletion finalizer attached, prior to its record being finally removed. Distinguishable from a normal, active namespace when read.
- **Namespace status conditions**: System-computed observations about a namespace's admission and operational state (at minimum: admission acceptance, system-repository readiness, overall readiness, and in-progress-deletion state), never settable by an authored manifest or mutation input.

### Explicit mutation envelope contract

`createNamespace` and `updateNamespace` use the declarative resource envelope already used for Git-manifest authoring and NOT the legacy flat input shape. The authoritative request object is:

```graphql
input CreateNamespaceInput {
  apiVersion: String!
  kind: String!
  metadata: NamespaceMetadataInput!
  spec: NamespaceSpecInput!
}

input UpdateNamespaceInput {
  apiVersion: String!
  kind: String!
  metadata: NamespaceMetadataInput!
  spec: NamespaceSpecInput!
}
```

The required fields are intentionally aligned to the manifest schema:
- `apiVersion`: resource contract version for the Namespace kind
- `kind`: must be `Namespace`
- `metadata.name`: namespace identifier; bootstrap names are rejected
- `spec`: author-controlled desired state (`title`, `tier`, and defaults)

The public GraphQL signature remains unchanged from the existing API surface; the change is in the accepted request envelope and the internal delegation to `gitstore-system/gitstore-system`, not in the mutation names or response types.

### Status condition matrix

| Condition           | Source of truth                                   | Set by                | Read semantics                                                                        |
|---------------------|---------------------------------------------------|-----------------------|---------------------------------------------------------------------------------------|
| `AdmissionAccepted` | datastore `Status.conditions` / admission output  | admission pipeline    | `true` only after the namespace's current spec was successfully admitted              |
| `SystemRepoReady`   | datastore `Status.conditions` / controller output | controller reconciler | `true` when the namespace's per-namespace `gitstore-system` repo exists and is usable |
| `Ready`             | datastore `Status.conditions` / controller output | controller reconciler | aggregate operational readiness; `AdmissionAccepted && SystemRepoReady`               |
| `Terminating`       | `DeletionTimestamp` + `Finalizers`                | system deletion flow  | derived for reads while a namespace is undergoing foreground deletion                 |

Author-submitted manifests may include a `status` block, but it is ignored. The system owns the values for `status`, `observedGeneration`, and `resourceVersion` after admission.

## Success Criteria *(mandatory)*

### Measurable Outcomes

- **SC-001**: 100% of non-bootstrap namespace creates and updates, whether initiated via `git push` or via the `createNamespace`/`updateNamespace` mutations, result in a corresponding Git commit to `gitstore-system/gitstore-system` before the namespace's spec reflects the change.
- **SC-002**: 100% of `Namespace` manifests pushed to a repository other than `gitstore-system/gitstore-system` are rejected before admission, with zero instances of a namespace record being created or changed from such a push.
- **SC-003**: 100% of deletion attempts against a namespace that still owns repositories are rejected, with zero instances of such a namespace entering an in-progress deletion state.
- **SC-004**: 100% of deletion attempts against either bootstrap namespace are rejected, with zero exceptions.
- **SC-005**: 100% of eligible deletions pass through a visibly distinguishable in-progress deletion state before the namespace's record is removed, with zero instances of a namespace record disappearing without first being observable in that state.
- **SC-006**: 100% of successfully admitted namespace changes leave status fields reflecting only system-computed values, with zero instances of caller- or manifest-supplied content appearing in status.

## Assumptions

- This spec adopts `docs/ADRs/0002-namespace-lifecycle.md` in full as the authoritative lifecycle design, superseding this spec's own earlier draft (which had assumed a direct-datastore-write model with no Git involvement, before that ADR was located and reviewed).
- The **reconciliation** half of this lifecycle — the controller loop that watches for `AdmissionAccepted` namespaces, provisions each one's own per-namespace `gitstore-system` repository, sets `SystemRepoReady`/`Ready`, and drives the finalizer-drain-then-remove sequence for `Terminating` namespaces — is implemented as part of this spec's scope, since ADR-0002 does not function without it. It reuses the existing `ProvisionSystemRepository` logic (spec 041) and the existing controller-manager reconciler pattern (spec 026/039) rather than inventing a new one.
- The finalizer-drain condition for namespace deletion is exactly "zero repositories remain" (spec 041's existing `HasRepositories` check) — this spec does not depend on Repository having its own finalizer/Terminating implementation (ADR-0003), which remains unimplemented; it only depends on repository *existence*, which is already tracked today.
- Pre-receive/admission validation details beyond "which repository is a valid authoring target" and "tier demotion is rejected" (name-format validation, uniqueness, limits, condition vocabulary beyond `AdmissionAccepted`) are defined by spec 047 (GH#173), which is updated in this same round to align with this spec's lifecycle model instead of assuming no `Terminating` state exists.
- Watch/resume semantics for Namespace (GH#174) remain a separate, later specification and are not defined here, though this spec's admission and status model is what GH#174 will observe.
- Namespace name reuse after a full deletion, and any retention/audit-log requirements for deleted namespace manifests, are out of scope for this spec.
