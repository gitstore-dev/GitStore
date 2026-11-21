# Feature Specification: Resource OwnerReferences and Deletion Semantics

**Feature Branch**: `054-repository-ownerreferences-backfill`
**Created**: 2026-08-20
**Status**: Draft (scope simplified 2026-11-07)
**Input**: User request: "update the spec and rectify stale data. Also consider covering the following; owner references for all Resource types. Note that `ServiceAccount` is also a resource type. Ensure deletion behaviour is consistent across all resources, we currently support 2 types of deletes (foreground deletion and backgroud deletion) controlled by the `blockOwnerDeletion` flag."

## Clarifications

### Session 2026-11-07

- The scope is the current first-party resource set: `Namespace`,
  `Repository`, `Product`, `ProductVariant`, `CategoryTaxonomy`, `Collection`,
  `File`, and `ServiceAccount`.
- `blockOwnerDeletion: true` means a dependent blocks removal of its owner.
  `false` allows owner removal and leaves the dependent as an orphan. Neither
  value requests cascading deletion.
- Owner references remain system-managed and are repaired as a resource is
  created or legitimately updated. A fleet-wide legacy-data backfill and a
  deletion gate tied to such a backfill are not required by this feature.
- Existing resources that are never created or updated are not guaranteed to
  have complete owner-reference metadata. This feature does not claim that the
  reverse projection is a complete inventory of all historical relationships.
- Foreground/background propagation is separate from asynchronous finalizer-
  driven completion and does not replace each resource's existing lifecycle
  checks.

## User Scenarios & Testing

### User Story 1 - Every resource reports its lifecycle owner (Priority: P1)

Authors and operators need system-managed ownership metadata that consistently
identifies the resource relationships relevant to deletion. The contract
covers every current resource kind, including root kinds that have no owner
and cluster-scoped `ServiceAccount`.

**Independent Test**: Create resources for each relationship in the ownership
matrix, read both resources, and verify that the dependent exposes exactly the
expected owner references. Verify that roots have no synthetic owner and
caller-supplied values cannot forge or remove system-managed references.

**Acceptance Scenarios**:

1. **Given** a Repository in a Namespace, **When** either resource is read,
   **Then** the Repository has one blocking owner reference to that Namespace.
2. **Given** a git-backed catalog resource, **When** it is admitted, **Then**
   it has one blocking owner reference to its containing Repository, in
   addition to any applicable Product or CategoryTaxonomy owner reference.
3. **Given** a ProductVariant, child CategoryTaxonomy, or categorized Product,
   **When** its relationship resolves, **Then** it has the corresponding
   reference and blocking value from the ownership matrix.
4. **Given** a resource with missing or stale managed references, **When** it
   is legitimately updated, **Then** its managed references are recomputed
   without requiring a separate fleet-wide migration.
5. **Given** a Namespace or ServiceAccount, **When** it is read, **Then** it
   has no fabricated owner reference. A ServiceAccount's authentication
   `metadata.owner` is not a resource owner reference.

### User Story 2 - Deleting an owner treats dependents consistently (Priority: P1)

An operator can predict deletion behavior from the dependent's
`blockOwnerDeletion` value regardless of resource kind or supported deletion
entry point. A blocking dependent prevents owner removal; a non-blocking
dependent survives owner removal as an orphan. Neither case silently cascades
deletion.

**Independent Test**: Exercise deletion for an owner with a `true` dependent
and with a `false` dependent through supported entry points. Verify the
blocking outcome, orphaning outcome, and that no dependent is automatically
deleted.

**Acceptance Scenarios**:

1. **Given** an owner with a live dependent whose reference has
   `blockOwnerDeletion: true`, **When** deletion is requested, **Then** the
   owner is not removed and both resources remain intact.
2. **Given** an owner with only live dependents whose references have
   `blockOwnerDeletion: false`, **When** deletion is requested, **Then** the
   owner may be removed and each dependent survives with its obsolete
   system-managed owner reference cleared and applicable resolution status
   updated.
3. **Given** an owner with both blocking and non-blocking dependents,
   **When** deletion is requested, **Then** the blocking dependent prevents
   removal and no dependent is cascaded.
4. **Given** a dependent is deleted or its relationship is changed,
   **When** the change is admitted, **Then** its old owner reference ceases to
   block deletion.
5. **Given** an eligible owner deletion that completes asynchronously,
   **When** it is observed before completion, **Then** existing timestamp and
   finalizer behavior remains visible; asynchronous completion is not treated
   as background propagation.

## Requirements

### Functional Requirements

- **FR-001**: Apply the common lifecycle metadata contract to `Namespace`,
  `Repository`, `Product`, `ProductVariant`, `CategoryTaxonomy`, `Collection`,
  `File`, and `ServiceAccount`. Root resources may have an empty
  `ownerReferences` list.
- **FR-002**: Maintain these system-owned relationships:

  | Owner | Dependent | `blockOwnerDeletion` |
  |---|---|---|
  | Namespace | Repository | `true` |
  | Repository | Product | `true` |
  | Repository | ProductVariant | `true` |
  | Repository | CategoryTaxonomy | `true` |
  | Repository | Collection | `true` |
  | Repository | File | `true` |
  | Product | ProductVariant | `true` |
  | CategoryTaxonomy | child CategoryTaxonomy | `true` |
  | CategoryTaxonomy | Product | `false` |

- **FR-003**: Identify each owner by canonical resource identity (kind, name,
  UID) and applicable namespace or repository scope. A same-name recreation
  MUST NOT satisfy a reference to a prior UID.
- **FR-004**: Git-authored manifests and GraphQL inputs MUST NOT create,
  replace, reorder, or remove system-managed owner references or set
  `blockOwnerDeletion`.
- **FR-005**: `ServiceAccount` MUST remain cluster-scoped, MUST NOT acquire a
  Namespace owner reference, and MUST NOT acquire owner references merely
  because it represents an authorization subject.
- **FR-006**: A live dependent with `blockOwnerDeletion: true` MUST prevent
  removal of its owner until the dependent is deleted or its relationship is
  removed. Git and GraphQL deletion paths MUST provide the same outcome.
- **FR-007**: A live dependent with `blockOwnerDeletion: false` MUST NOT
  prevent owner removal. The dependent MUST survive as an orphan; its
  authored desired-state fields MUST NOT be silently rewritten.
- **FR-008**: Deleting an owner MUST NOT automatically delete any dependent.
- **FR-009**: `blockOwnerDeletion` MUST govern dependent propagation only. It
  MUST NOT change authorization, validation, the dependent's own deletion
  rules, or unrelated lifecycle behavior.
- **FR-010**: Create and update admission MUST maintain owner references and
  the existing reverse projection idempotently. A legitimate update to an
  existing resource MUST repair that resource's stale or missing managed
  references while preserving unrelated system-managed references.
- **FR-011**: Owner checks and dependent writes MUST preserve existing
  concurrency protections across API replicas. A create or reparent racing
  with owner deletion MUST NOT leave a live blocking reference to a removed
  owner.
- **FR-012**: This feature MUST NOT require a fleet-wide scan, durable
  backfill state, or migration solely to establish owner-reference metadata.
  Untouched legacy resources are not represented as fully reconciled by this
  feature.
- **FR-013**: Preserve namespace scoping for namespaced resources and cluster
  scope for Namespace and ServiceAccount; reject relationships across an
  invalid namespace or Repository boundary.

### Production Requirements

- **PR-001 Replica Safety**: Concurrent API replicas MUST converge relationship
  writes and deletion checks through shared datastore state. Rolling upgrades
  MUST preserve existing checks; Git remains singleton-only.
- **PR-002 Multi-User Security**: Existing pluggable AuthN/AuthZ and tenant
  isolation MUST apply. Callers MUST NOT forge owner references or cross
  authorization boundaries using them.
- **PR-003 Capacity**: Normal admission MUST perform bounded relationship
  work and use the existing indexed projection rather than an unbounded
  request-path scan.
- **PR-004 Load**: Sustained push and reconciliation load MUST retain existing
  capacity bounds and surface datastore failures; no full-catalog scan is
  added to normal operations.

## Success Criteria

- **SC-001**: Each of the eight in-scope kinds exposes the common metadata
  contract; each relationship in FR-002 is maintained on create or update.
- **SC-002**: A live blocking reference prevents owner removal through every
  supported deletion entry point.
- **SC-003**: Non-blocking dependents survive owner removal as orphans; no
  owner deletion cascades to dependents.
- **SC-004**: A legitimate update repairs missing or stale managed references
  for the updated resource and preserves unrelated references.
- **SC-005**: Zero author-supplied owner references or
  `blockOwnerDeletion` values alter persisted system-managed relationship
  metadata.

## Assumptions

- The ownership matrix is the complete set of first-party lifecycle
  relationships in this feature. Ordinary references such as `fileRef` and
  ServiceAccount authentication ownership do not become resource owner
  references without an explicit lifecycle contract.
- `blockOwnerDeletion` is interpreted on the dependent's reference to its
  owner. `true` blocks owner deletion; `false` allows owner deletion and leaves
  the dependent alive as an orphan.
- Historical specs remain records of their original decisions. This
  specification governs the current first-party ownership matrix and does not
  imply full migration of untouched historical data.
