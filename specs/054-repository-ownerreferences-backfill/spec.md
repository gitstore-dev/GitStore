# Feature Specification: Resource OwnerReferences and Deletion Semantics

**Feature Branch**: `054-repository-ownerreferences-backfill`
**Created**: 2026-08-20
**Status**: Draft (refreshed 2026-11-06)
**Input**: User request: "update the spec and rectify stale data. Also consider covering the following; owner references for all Resource types. Note that `ServiceAccount` is also a resource type. Ensure deletion behaviour is consistent across all resources, we currently support 2 types of deletes (foreground deletion and backgroud deletion) controlled by the `blockOwnerDeletion` flag."

## Clarifications

### Session 2026-11-06

- Scope is the current first-party resource set: `Namespace`, `Repository`, `Product`, `ProductVariant`, `CategoryTaxonomy`, `Collection`, `File`, and `ServiceAccount`. `ServiceAccount` is cluster-scoped and is not part of the GraphQL `Resource` union, but it is included in this resource-lifecycle contract.
- `blockOwnerDeletion: true` means a dependent blocks deletion of its referenced owner (foreground propagation). `false` means it does not block owner deletion (background propagation); the dependent survives and is orphaned. Neither value requests cascading deletion.
- Foreground/background propagation describes what happens to dependents when their owner is deleted. It is separate from asynchronous finalizer-driven completion, such as Product deletion, and does not replace each resource's existing authorization or lifecycle checks.
- Existing relationships are included in an idempotent, resumable backfill; enforcement cannot rely on the new relationship index until that backfill is complete. This supersedes the earlier decision to rely only on create/update admission.

## User Scenarios & Testing *(mandatory)*

### User Story 1 - Every resource reports its lifecycle owner (Priority: P1)

Authors and operators need system-managed ownership metadata that consistently identifies the resource relationships that matter to deletion. The contract covers every current resource kind, including root kinds that correctly have no owner and the cluster-scoped `ServiceAccount`.

**Why this priority**: Deletion safety and resource introspection depend on complete, trustworthy owner relationships rather than kind-specific assumptions or name-only scans.

**Independent Test**: Create or resolve each relationship in the ownership matrix, read both resources, and verify that the dependent contains exactly the expected owner reference. Verify that root resources and a ServiceAccount have no synthetic namespace or principal-based owner reference.

**Acceptance Scenarios**:

1. **Given** a Repository in a Namespace, **When** either resource is read, **Then** the Repository has one blocking owner reference to that Namespace.
2. **Given** a git-backed catalog resource, **When** it is admitted, **Then** it has one blocking owner reference to its containing Repository, in addition to any applicable Product or CategoryTaxonomy owner reference.
3. **Given** a ProductVariant, child CategoryTaxonomy, or categorized Product, **When** its relationship resolves, **Then** it has the corresponding reference and blocking value from the ownership matrix.
4. **Given** a Namespace or ServiceAccount, **When** it is read, **Then** it has no fabricated owner reference. A ServiceAccount's authentication `metadata.owner` is not a resource owner reference.
5. **Given** a caller supplies owner references in authored input, **When** the resource is admitted, **Then** system-managed ownership is preserved and caller-supplied values cannot forge or remove it.

### User Story 2 - Deleting an owner treats all dependents consistently (Priority: P1)

An operator can predict deletion behavior from the dependent's `blockOwnerDeletion` value regardless of the resource kind or whether deletion was requested through Git or GraphQL. A blocking dependent prevents owner removal; a non-blocking dependent survives owner removal as an orphan. Neither case silently cascades deletion.

**Why this priority**: Kind-specific interpretations of the same metadata can either strand data or unexpectedly destroy it.

**Independent Test**: Exercise deletion for an owner with a `true` dependent and with a `false` dependent through each supported entry point. Verify the blocking outcome, the orphaning outcome, and that no dependent is automatically deleted.

**Acceptance Scenarios**:

1. **Given** an owner with a live dependent whose reference has `blockOwnerDeletion: true`, **When** deletion is requested, **Then** the owner is not removed, the request reports the blocking dependent outcome, and all resources remain intact.
2. **Given** an owner with only live dependents whose references have `blockOwnerDeletion: false`, **When** deletion is requested, **Then** the owner may be removed and each dependent survives with the obsolete owner reference cleared and any applicable resolution status updated.
3. **Given** an owner with both blocking and non-blocking dependents, **When** deletion is requested, **Then** the blocking dependent prevents removal; non-blocking dependents are not cascaded or otherwise changed before the request can proceed.
4. **Given** a dependent is deleted or its relationship is changed, **When** the change is admitted, **Then** its old owner reference ceases to block deletion.
5. **Given** an eligible owner deletion that completes asynchronously, **When** it is observed before completion, **Then** existing timestamp/finalizer behavior remains visible; asynchronous completion is not treated as background propagation.

### User Story 3 - Existing resources become safe before enforcement (Priority: P1)

Operators can upgrade a deployment with resources created before complete owner references existed. The system backfills their actual ownership relationships before using owner-reference indexes as deletion gates, without missing blockers or depending on each resource being edited later.

**Why this priority**: Applying new deletion rules to incomplete metadata could permit unsafe deletion of an owner with an existing dependent.

**Independent Test**: Seed legacy resources for every applicable relationship, run or resume the backfill, and verify complete, duplicate-free references before enabling deletion enforcement. Interrupt and resume the operation to verify convergence.

**Acceptance Scenarios**:

1. **Given** a legacy resource with a resolvable owner relationship and no owner reference, **When** the backfill runs, **Then** the correct reference is added.
2. **Given** a legacy resource with an incorrect or duplicate system-managed reference, **When** the backfill runs, **Then** it converges to one correct reference without changing unrelated metadata.
3. **Given** the backfill is incomplete or cannot establish coverage, **When** an owner deletion is requested, **Then** the system does not treat an incomplete index as proof that no blocking dependents exist.
4. **Given** the backfill is interrupted and resumed, **When** it completes, **Then** every applicable existing relationship is represented exactly once.

### Edge Cases

- Root resources have an empty owner-reference list; absence of an owner is valid and does not mean metadata is incomplete.
- `ServiceAccount` is cluster-scoped. It has no Namespace owner reference, and its `metadata.owner` authentication subject must not be interpreted as a resource relationship. No current resource relationship to a ServiceAccount is implied; a future one must be explicitly specified.
- A catalog resource can have multiple owner references, such as its Repository and a Product or CategoryTaxonomy. Updating one relationship must not overwrite or reorder unrelated references.
- `File` is a current resource kind and is included in Repository ownership regardless of where its blob content is stored. A `fileRef` or other ordinary data reference is not automatically an owner relationship.
- A `Collection` is Repository-owned even though it has no additional owner edge in this contract.
- A blocking deletion check racing with dependent creation or reparenting must not allow the owner to be removed while a blocking reference is live. A dependent cannot successfully resolve to an owner already terminating or deleted.
- A non-blocking dependent remains available after orphaning; its desired-state reference is not silently rewritten. System-owned relationship metadata and resolution status are updated so the unresolved relationship is visible.
- Existing resource-specific authorization, deletion entry points, status ownership, finalizers, and storage cleanup remain in force. The ownership rule does not bypass them.
- Historical specs may describe older kind lists or synchronous preconditions. For current behavior, the ownership matrix and common blocking/orphaning contract in this specification are authoritative; this does not retroactively alter their historical records.

## Requirements *(mandatory)*

### Functional Requirements

- **FR-001**: The system MUST apply this contract to `Namespace`, `Repository`, `Product`, `ProductVariant`, `CategoryTaxonomy`, `Collection`, `File`, and `ServiceAccount`. Each resource kind MUST expose the common lifecycle metadata contract. Root resources MAY have an empty `ownerReferences` list.
- **FR-002**: The system MUST maintain the following system-owned relationships:

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

- **FR-003**: The system MUST identify each owner by the canonical resource identity (kind, name, and UID) and the applicable namespace or repository scope. A recreated resource with the same name MUST NOT satisfy a reference to a prior UID.
- **FR-004**: Owner references MUST be system-managed. Git-authored manifests and GraphQL inputs MUST NOT create, replace, reorder, or remove references or set `blockOwnerDeletion`; admission and lifecycle reconciliation MUST preserve unrelated system-managed references.
- **FR-005**: The ServiceAccount resource kind MUST be recognized by the shared metadata contract. ServiceAccounts MUST remain cluster-scoped, MUST NOT acquire a Namespace owner reference, and MUST NOT acquire owner references merely because they represent an authorization subject. No ServiceAccount relationship is created absent an explicitly defined lifecycle dependency.
- **FR-006**: For a live dependent with `blockOwnerDeletion: true`, the system MUST prevent removal of its owner until the dependent is deleted or the relationship is removed. Git and GraphQL deletion paths MUST provide the same blocking outcome and leave the owner and dependent intact when blocked.
- **FR-007**: A live dependent with `blockOwnerDeletion: false` MUST NOT prevent owner removal. The dependent MUST survive as an orphan, and the obsolete system-managed owner reference and any resolved-owner status MUST be cleared or updated. Its authored desired-state fields MUST NOT be silently rewritten.
- **FR-008**: Deleting an owner MUST NOT automatically delete any dependent, regardless of `blockOwnerDeletion`. Cascading deletion is outside this contract.
- **FR-009**: `blockOwnerDeletion` MUST govern dependent propagation only. It MUST NOT change authorization, validation, the dependent's own deletion rules, or the owner kind's unrelated lifecycle behavior. Asynchronous finalizer-driven completion MUST remain distinct from background propagation.
- **FR-010**: The blocking-dependent check and owner removal MUST be concurrency-safe across API replicas. A dependent create or relationship change racing with deletion MUST either be rejected or be included in the blocking check; it MUST NOT leave a live blocking reference to a removed owner.
- **FR-011**: Before enabling deletion decisions based on the complete owner-reference index, the system MUST run an idempotent, resumable backfill over all existing resources and relationships in FR-002. Enforcement MUST remain safe until backfill coverage is established; backfill failure MUST be visible and MUST NOT be treated as successful completion.
- **FR-012**: Create, update, reparent, delete, backfill, and retry operations MUST be idempotent with respect to owner references. Each applicable owner relationship MUST appear exactly once after the operation settles.
- **FR-013**: Deletion behavior and owner-reference metadata MUST be consistent across Git admission, GraphQL mutations, direct resource reads, and watches, subject to each kind's existing authorization and lifecycle contract.
- **FR-014**: The system MUST preserve Repository deletion coverage for all Repository-owned kinds in FR-002, including `File`; historical lists that omit `File` MUST NOT define current enforcement scope.
- **FR-015**: The system MUST preserve namespace scoping for namespaced resources and cluster scope for Namespace and ServiceAccount. It MUST reject relationships that cross an invalid namespace or Repository boundary.

### Production Requirements *(mandatory for core-service or load-bearing changes)*

- **PR-001 Replica Safety**: With concurrent API and controller replicas, relationship writes, deletion checks, backfill progress, and retries MUST converge to the same result. Rolling upgrades MUST NOT permit deletion based on an incomplete relationship view. Git remains singleton-only; this feature MUST NOT introduce overlapping Git writers, replication, or sharding.
- **PR-002 Multi-User Security**: Existing pluggable AuthN/AuthZ and tenant isolation MUST apply to relationship writes, backfill administration, and deletion. Callers MUST NOT forge owner references or use an owner reference to cross authorization boundaries.
- **PR-003 Capacity**: Normal admission MUST perform bounded relationship work; dependent checks MUST use the maintained relationship index rather than unbounded scans. Backfill MUST process bounded batches and MUST NOT hold unbounded resource sets in memory.
- **PR-004 Backpressure**: Backfill and orphan cleanup MUST have bounded batch sizes, concurrency, retries, and deadlines. Overload or storage errors MUST be surfaced and MUST NOT cause the system to assume there are no blocking dependents.
- **PR-005 Recovery**: Backfill and deletion progress MUST be resumable after process replacement or partial failure. Repeated execution MUST preserve data, avoid duplicate references, and never remove an owner while a blocking dependent exists.

### Key Entities

- **Resource identity**: A kind, name, UID, and scope identifying one resource lifetime; a name reused after deletion is a different identity.
- **Owner reference**: System-managed metadata on a dependent identifying an owner and whether that dependent blocks the owner's deletion.
- **Blocking dependent**: A live dependent with `blockOwnerDeletion: true`; it prevents owner removal but is never automatically deleted.
- **Non-blocking dependent**: A live dependent with `blockOwnerDeletion: false`; it survives owner removal as an orphan.
- **ServiceAccount**: A cluster-scoped authentication resource included in lifecycle metadata coverage. Its authentication subject is distinct from a resource owner relationship.
- **Owner-reference backfill**: A bounded, idempotent, resumable process that establishes the ownership relationships for resources that predate complete metadata.

## Success Criteria *(mandatory)*

### Measurable Outcomes

- **SC-001**: 100% of the eight in-scope resource kinds expose the common owner-reference metadata; roots have no fabricated owner, and each applicable relationship in FR-002 has exactly one correct reference.
- **SC-002**: 100% of deletion attempts with a live blocking dependent leave both owner and dependent intact, regardless of resource kind or supported deletion entry point.
- **SC-003**: 100% of owner deletions with only non-blocking dependents preserve every dependent, clear the obsolete owner reference, and report any unresolved relationship without cascading deletion.
- **SC-004**: 100% of pre-existing applicable relationships are represented before indexed deletion enforcement is enabled; interrupted backfills resume without duplicates or missed dependents.
- **SC-005**: Zero author-supplied owner references or `blockOwnerDeletion` values alter persisted system-managed relationship metadata.
- **SC-006**: Zero owners are removed while a live blocking reference to their UID exists, including during concurrent admission, reparenting, API/controller replacement, and retry.

## Assumptions

- The listed ownership matrix is the complete set of first-party lifecycle ownership relationships for this feature. Ordinary references such as `fileRef`, selectors, or `ServiceAccount` authorization ownership do not become owner references without an explicit lifecycle contract.
- `blockOwnerDeletion` is interpreted on the dependent's reference to its owner. `true` blocks owner deletion; `false` allows owner deletion and leaves the dependent alive as an orphan.
- The terms foreground/background propagation do not define whether a resource-specific deletion workflow is synchronous or finalizer-driven. Existing workflows may complete asynchronously, but their dependent-blocking and orphaning outcomes MUST follow FR-006 through FR-009.
- Historical specs 041, 052, and 055 remain records of their original decisions. Their stale resource lists and any inconsistent propagation behavior are superseded for the common ownership contract by this specification; implementation planning must identify any required compatibility changes.
- Backfill is required because the owner-reference index cannot provide a safe deletion decision until legacy relationships are represented. It must not be replaced by update-on-next-touch behavior.
