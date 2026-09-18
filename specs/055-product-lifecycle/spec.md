# Feature Specification: Product Git-Backed Lifecycle, Durable Watch, and Reconciliation

**Feature Branch**: `055-product-deletion-safety`
**Created**: 2026-09-16
**Status**: Draft
**Input**: Extend Product deletion safety into the complete Product lifecycle: Git push and GraphQL mutation paths; GraphQL read, authentication, and authorization coverage; durable Product watches; Product controller reconciliation; owner references, blocking dependents, background deletion, finalizers, and `metadata.deletionTimestamp`.

## Clarifications

### Session 2026-09-16

- Product GraphQL create, update, and delete mutations do not accept a repository input. Creation uses the namespace's `gitstore-system` repository; existing-resource mutation routing is resolved server-side from admitted Product provenance.
- GraphQL Product creation uses the namespace's `gitstore-system` repository. A GraphQL update of a Product originally admitted from any Git repository uses stored provenance to update the original repository and original source path; mutation input still exposes no repository or path selector.
- Retirement is Product-only in this feature. A retired Product makes its child ProductVariants ineligible for newly prepared releases; ProductVariants do not gain an independently authored lifecycle state.
- This feature adds no public Product GraphQL query or endpoint. Product GraphQL is private management/catalog access; mutable Product reads and watches must not serve storefront traffic.
- Release preparation rejects a candidate that explicitly includes a retired Product or one of its child ProductVariants; it does not silently remove those entries.

## User Scenarios & Testing *(mandatory)*

### User Story 1 - Authors manage Products through one reviewable lifecycle (Priority: P1)

A catalog author can create, update, or request deletion of a Product through Git or GraphQL. Both routes have the same validation, admission, identity, and observable outcome, and neither becomes a separate direct-write source of truth.

**Why this priority**: Product is the next resource in the shipped Namespace → Repository ownership chain. A split write path would undermine auditability and reconciliation.

**Independent Test**: Create and update a Product through each entry point, then compare the admitted resource and authored revision. Submit an invalid update through each path and verify that neither changes the admitted Product.

**Acceptance Scenarios**:

1. **Given** an active namespace and repository, **When** an authorized author pushes a valid new Product manifest, **Then** the Product is admitted with a new immutable identity and admission-success status.
2. **Given** an existing Product, **When** an authorized author changes mutable desired state through Git or GraphQL, **Then** its identity remains stable, its generation advances, and its admitted revision is visible.
3. **Given** a GraphQL create, update, or deletion request with no repository input, **When** its resolved canonical Git change cannot be admitted, **Then** the request reports that failure and returns no partially applied Product.
4. **Given** a Product rename, **When** the name changes, **Then** it is deletion of the old identity plus creation of a new identity; dependents are never silently moved.

---

### User Story 2 - Consumers securely read and resume Product state (Priority: P1)

An authorized storefront, operator, or controller can look up a Product, page Products in its namespace, and resume Product change observation without missing admitted transitions. Every read surface consistently exposes desired state, system status, ownership, finalizers, and deletion timestamp.

**Why this priority**: Consumers must be able to distinguish active, terminating, and deleted Products, including after reconnecting to another replica.

**Independent Test**: Start Product observation, obtain a complete list, save the cursor, and resume through another API replica while creates, updates, status changes, and deletion transitions occur.

**Acceptance Scenarios**:

1. **Given** an authorized caller with no Product cursor, **When** it bootstraps, lists, and drains Product changes, **Then** it obtains a complete snapshot and durable resume cursor with no gap.
2. **Given** a retained Product cursor, **When** a caller resumes through any healthy API replica, **Then** it receives only later Product events in order or an explicit recovery response when continuity is unavailable.
3. **Given** an unauthorized caller, **When** it attempts Product lookup, list, node lookup, typed watch, or generic Product watch, **Then** it receives no Product payload, existence signal, or cursor.
4. **Given** a terminating Product, **When** it is read or delivered as a change, **Then** its deletion timestamp, finalizers, and terminating status are visible; permanent removal is delivered separately.

---

### User Story 3 - Product deletion never orphans ProductVariants (Priority: P1)

An operator can request deletion of a Product without leaving variants behind. ProductVariants record a blocking relationship to their resolved Product. A Product with a live blocking variant cannot begin deletion; an eligible Product enters a visible terminating state and background completion removes it only when a fresh dependent check proves it safe.

**Why this priority**: A ProductVariant cannot validly exist without its Product. This preserves the deletion-safety guarantee that introduced feature 055.

**Independent Test**: Request deletion by Git and GraphQL with a live variant, remove the variant, and repeat. Observe the timestamp/finalizer before final removal; attempt to create or newly resolve a variant while the Product is terminating and verify admission rejects it.

**Acceptance Scenarios**:

1. **Given** a Product with one or more live blocking ProductVariants, **When** deletion is requested through Git or GraphQL, **Then** it is rejected and the Product remains active.
2. **Given** a Product with no blocking variants, **When** deletion is requested, **Then** it receives a deletion timestamp and foreground-deletion finalizer, becomes visibly terminating, and background completion starts.
3. **Given** a terminating Product, **When** a new or newly resolved ProductVariant targets it, **Then** admission rejects the ProductVariant and does not create a new blocking relationship.
4. **Given** a terminating Product with no blocking variants at the final removal boundary, **When** background reconciliation completes, **Then** finalizers are cleared and the Product is permanently removed exactly once.
5. **Given** a repeated deletion request for a terminating Product, **When** it is received, **Then** it observes or resumes the existing workflow instead of creating a competing workflow.

---

### User Story 4 - Controllers converge Product lifecycle without breaking category reconciliation (Priority: P1)

Controller replicas observe Product changes, converge Product-owned lifecycle work, and report only system-owned status. Product create, delete, and category reassignment continue to trigger the existing CategoryTaxonomy count reconciliation without giving Product controllers ownership of category status.

**Why this priority**: Finalizer completion must survive request completion, retries, and replica replacement while preserving already shipped Product-to-CategoryTaxonomy behavior.

**Independent Test**: Run two controller replicas while Product mutations and deletion occur, replace one replica mid-reconcile, and verify lifecycle state and affected category counts converge without duplicate side effects.

**Acceptance Scenarios**:

1. **Given** a Product lifecycle transition, **When** one or more controller replicas observe it, **Then** retries converge to one Product status and deletion outcome.
2. **Given** a Product add, delete, or category reassignment, **When** reconciliation settles, **Then** every affected CategoryTaxonomy count is correct and unrelated categories are untouched.
3. **Given** a controller status write based on an old Product version, **When** another transition has advanced the Product, **Then** the stale write is rejected and the controller recomputes from current state.

---

### User Story 5 - Product access is least-privilege and auditable (Priority: P2)

Human users, service accounts, and controllers authenticate through configured identity providers and receive only the Product capabilities appropriate to their role and namespace/repository scope.

**Why this priority**: Product is catalog control-plane state; lifecycle work must not create an authorization or tenant-isolation bypass.

**Independent Test**: Exercise every Product entry point with a reader, author, controller, and unauthorized subject across two namespaces; verify outcomes and audit evidence.

**Acceptance Scenarios**:

1. **Given** a caller authorized only in one namespace, **When** it queries or watches another namespace's Products, **Then** it receives no protected state or cursor.
2. **Given** a Product author without status authority, **When** it writes desired state, **Then** it cannot set status, owner references, finalizers, deletion timestamp, UID, generation, or resource version.
3. **Given** a Product controller identity, **When** it writes status, **Then** it changes only documented controller-owned status and the decision is attributable to that identity.

---

### User Story 6 - Authors retire Products without rewriting published offers (Priority: P2)

An author can retire a Product as durable desired state without treating
retirement as deletion or changing an already-published offer. Retirement makes
the Product ineligible for new releases; deletion remains the separate,
variant-safe lifecycle described above. Public visibility remains determined by
the immutable release and publication selected for each target.

**Why this priority**: The proposed seller workflow and publication contracts
need a safe way to stop a Product entering future releases without losing
management history, SKU references, or an already-active snapshot.

**Independent Test**: Retire a Product that is present in an active publication,
attempt to include it in a new release, and verify that the active publication
is unchanged and the new release is rejected.

**Acceptance Scenarios**:

1. **Given** an active Product, **When** an authorized author marks it retired through Git or its equivalent GraphQL update, **Then** the intent is versioned and auditable, and the Product remains available to authorized management users.
2. **Given** a release candidate that explicitly includes a retired Product or one of its child ProductVariants, **When** a new release is prepared, **Then** preparation is rejected and neither entry is silently excluded or included in a new snapshot.
3. **Given** a Product present in an active public snapshot, **When** its current source state is retired or deleted, **Then** the already-active snapshot remains unchanged until a replacement publication or separately authorized emergency suppression takes effect.
4. **Given** a workflow or publication controller, **When** it evaluates Product eligibility, **Then** it uses the immutable release candidate and its applicable workflow facts, not mutable Product watch events as public-serving state.

### Edge Cases

- A same-push ProductVariant removal and Product deletion succeeds only if the proposed admitted state has no blocking variant; partial or ambiguous state fails safely.
- A ProductVariant that already blocks a Product continues to prevent its final deletion until separately removed. A new or newly resolved ProductVariant targeting a terminating Product is rejected and creates no new relationship.
- The existing non-blocking Product-to-CategoryTaxonomy relationship does not block Product deletion.
- A stale, malformed, expired, or unauthorized Product-watch cursor reveals no payload and gives an explicit safe recovery outcome where appropriate.
- A Product without a Git manifest remains readable as terminating until lifecycle finalizers complete; the same name cannot be reused before final removal.
- Retirement is not deletion, a finalizer state, or an emergency takedown. A
  retired Product stays available to authorized management reads and an active
  public snapshot is withdrawn only through its publication contract.
- Product watch events describe private current-catalog changes. They must not
  be used to update a storefront from a mutable source row after a release has
  been pinned.

## Requirements *(mandatory)*

### Functional Requirements

- **FR-001**: The system MUST make Git the canonical authored source of Product desired state. GraphQL Product creation MUST author its equivalent change in the namespace's `gitstore-system` repository. A GraphQL update MUST use the existing Product's admitted Git provenance to write the original repository and original source path. Each operation MUST await its authorized Git admission outcome rather than directly persisting desired state.
- **FR-002**: The system MUST accept Product create, update, and delete intent through authorized Git push and derive the operation from the old and proposed Product state.
- **FR-003**: The system MUST provide GraphQL Product create, update, and delete operations with the same validation, admission, ownership, and lifecycle outcomes as Git. These mutation inputs MUST NOT expose a repository field. `DeleteProductInput` MUST use the standard resource-delete shape, `id: ID`, as an opaque global Product Node ID rather than a raw UID or resource-specific namespace/name selector.
- **FR-004**: The system MUST preserve Product identity on mutable desired-state changes, allocate a new identity on delete-and-recreate or rename, and reject in-place namespace or name changes.
- **FR-005**: The system MUST expose complete Product envelopes through individual lookup, namespaced connection, node lookup, typed Product watch, and generic `watchResources(kind: "Product")`, including owner references, finalizers, and deletion timestamp.
- **FR-006**: The system MUST authorize Product list, lookup, node, mutation, status, typed-watch, and generic-watch requests before disclosing Product state, existence, or resume cursors.
- **FR-007**: The system MUST enforce distinct Product permissions for read, authoring, deletion, watch, and controller status actions across every configured authentication provider.
- **FR-008**: The system MUST replace Product's process-local watch history with the durable, replica-portable Resource Watch contract used by Namespace and Repository, while retaining both typed and generic Product subscription entry points.
- **FR-009**: The durable Product watch MUST provide race-free bootstrap/list/drain, opaque cursor resume, ordered additions and modifications, explicit final deletion, durable bookmarks, selector semantics, bounded replay, and explicit expiry or temporary-unavailability outcomes.
- **FR-010**: Every acknowledged Product admission, desired-state update, controller-status update, deletion-timestamp/finalizer transition, and final removal MUST be observable on the durable Product watch. Rejected, failed, and no-op requests MUST NOT appear as successful transitions.
- **FR-011**: The system MUST maintain system-owned owner references. A resolved ProductVariant MUST reference its Product with `blockOwnerDeletion: true`; the existing Product-to-CategoryTaxonomy relationship remains non-blocking.
- **FR-012**: Before accepting Product deletion through either entry point, the system MUST use the indexed owner-reference relationship to reject a Product that has any live blocking ProductVariant, without scanning product-reference names.
- **FR-013**: An eligible Product deletion MUST set `metadata.deletionTimestamp`, add a Product foreground-deletion finalizer, and expose terminating status before permanent removal. Background reconciliation MUST complete this work asynchronously and MUST NOT automatically delete ProductVariants.
- **FR-014**: Product final removal MUST perform a fresh, concurrency-safe blocking-dependent check at the removal boundary. A Product remains terminating whenever a live blocking ProductVariant exists, including one admitted after the original deletion request.
- **FR-015**: Product deletion MUST be idempotent for both active and terminating Products.
- **FR-030**: `deleteProduct` MUST return the current Product envelope and a mandatory shared `ResourceDeletionOutcome` that distinguishes `TERMINATION_STARTED` from `ALREADY_TERMINATING`. It MUST NOT introduce a legacy `deletedIdentifier` field.
- **FR-016**: A Product controller MUST reconcile lifecycle progress and Product-owned status with optimistic concurrency, preserve other system-owned status, and make external work idempotent across retry and replica handoff.
- **FR-017**: Product lifecycle changes MUST preserve the existing Product-to-CategoryTaxonomy fan-out: create, delete, and category reassignment enqueue only affected categories.
- **FR-018**: Product writes and admission MUST reject user-authored system state, including status, owner references, finalizers, deletion timestamp, UID, generation, and resource version.
- **FR-019**: The system MUST retain attributable audit records for Product authorization decisions and lifecycle-changing requests without recording secret material.
- **FR-020**: The feature MUST document rollout, recovery, watch-expiry, authorization-denial, terminating-read, and background-deletion behavior.
- **FR-021**: Product admission and Product lifecycle transitions MUST affect the private current catalog only. Admission success MUST NOT itself make a Product or ProductVariant publicly visible.
- **FR-022**: The system MUST support Git-authored Product retirement intent through `spec.lifecycle.state` with `ACTIVE` and `RETIRED` values. A retired Product and all of its child ProductVariants are ineligible for every newly prepared release, and the Product remains available to authorized management reads. ProductVariants MUST NOT gain an independently authored lifecycle state in this feature.
- **FR-029**: Release preparation MUST reject a candidate that explicitly includes a retired Product or one of its child ProductVariants. It MUST NOT silently exclude the entry, prepare a partial candidate, or defer this eligibility failure to publication approval.
- **FR-023**: Product retirement or deletion after a release snapshot has been prepared or activated MUST NOT rewrite that immutable snapshot, its release tag, or its public projection. Withdrawal requires a replacement publication or a separately authorized emergency-suppression capability.
- **FR-024**: Product lifecycle status MUST NOT use a source-resource `Published` condition as the authority for target-, revision-, or time-specific storefront visibility. Admission, readiness, and reference conditions remain source-health signals.
- **FR-025**: Product GraphQL lookup, lists, node lookup, relationships, counts, and watches MUST be private management/catalog surfaces and enforce the authorized current-catalog scope. This feature MUST NOT add a public Product GraphQL read surface; mutable Product reads and watches MUST NOT serve storefront traffic.
- **FR-026**: Product lifecycle reconciliation MUST NOT create, move, delete, or otherwise mutate release tags, release snapshots, publications, or workflow executions. It may expose source-health information needed by their controllers through the documented Product contract.
- **FR-027**: Product lifecycle changes MUST preserve the seller-workflow boundary: workflow approval and publication eligibility are separate durable facts with immutable inputs, not mutable Product status fields or ad hoc controller decisions.
- **FR-028**: ProductVariant admission MUST reject a new or newly resolved ProductVariant whose resolved Product has a deletion timestamp. Existing blocking ProductVariant owner references remain effective until their variants are separately removed.

### Production Requirements *(mandatory for core-service or load-bearing changes)*

- **PR-001 Replica Safety**: With at least two API and two controller replicas, Product admission, watch replay, status updates, and deletion completion MUST remain correct during replacement and rolling upgrade. Duplicate delivery is permitted only when it cannot cause a duplicate side effect or lost transition.
- **PR-002 Multi-User Security**: Every Product operation MUST honor configured authentication and Product authorization, prevent cross-namespace and cross-repository disclosure, and retain attributable audit evidence.
- **PR-003 Capacity**: The lifecycle MUST support the five-million-product catalog scale, 1,000 concurrent Product subscriptions, sustained Product admission and reconciliation load, bounded list pages, and stated watch visibility and replay objectives.
- **PR-004 Backpressure**: Product replay, delivery, reconciliation, dependent checks, and background deletion MUST have bounded work, retry, timeout, and overload behavior. A slow subscriber or drain cannot stall unrelated Products or healthy subscribers.
- **PR-005 Capacity Evidence**: The root Makefile MUST expose `make capacity TARGET=product PROFILE=lifecycle MODE=<diagnostic|alpha|production>` evidence covering two API replicas, two controller replicas, Git push and GraphQL mutation traffic, Product-watch visibility, deletion races, and owner-reference correctness. Production evidence MUST enforce visibility p95 ≤1 second, p99 ≤3 seconds, 10,000-event replay p95 ≤5 seconds, and zero missing acknowledged transitions.
- **PR-006 Fault Recovery**: A reusable Product lifecycle chaos profile MUST prove recovery from API/controller replacement, watch-materializer interruption, and mid-deletion failure; it MUST restore readiness within 30 seconds and never finalize a Product with a blocking dependent.

### Key Entities

- **Product**: A Git-backed catalog resource with author-owned desired state and system-owned identity, lifecycle metadata, status, provenance, and ownership relationships.
- **ProductVariant blocking dependent**: A ProductVariant whose system-owned owner reference targets a Product with `blockOwnerDeletion: true`. It prevents final Product deletion but is never automatically deleted.
- **Product lifecycle state**: Active or terminating state, expressed by `metadata.deletionTimestamp`, finalizers, and system-owned status; final deletion is a distinct transition.
- **Durable Product watch event**: An ordered, resumable Product transition containing a full postimage for create/update, identity for deletion, or a bookmark cursor.
- **Product controller**: The authorized reconciler for Product lifecycle progress and Product-owned status. It does not own CategoryTaxonomy status.
- **Retirement intent**: Git-authored `spec.lifecycle.state` of `ACTIVE` or `RETIRED`. `RETIRED` excludes the Product and its variants from newly prepared releases without deleting the source record or rewriting active snapshots.
- **Publication snapshot**: An immutable, target-specific public catalog view. It is distinct from the private current Product catalog and is not driven by mutable Product watch events.

## Success Criteria *(mandatory)*

### Measurable Outcomes

- **SC-001**: 100% of valid Product creates and updates through Git or GraphQL produce one equivalent admitted Product and auditable revision; invalid requests create zero partial records.
- **SC-002**: 100% of Product reads, lists, node lookups, typed watches, and generic watches enforce authorized scope, with zero Product payloads or cursors disclosed to unauthorized callers.
- **SC-003**: 100% of deletion requests with a live blocking ProductVariant are rejected, and zero Products are finally removed while any blocking variant exists.
- **SC-004**: 100% of eligible Product deletions expose timestamp, finalizer, and terminating state before final removal; repeated delete requests create zero competing workflows.
- **SC-005**: In two-API/two-controller tests, 100% of acknowledged Product transitions are observed or explicitly recovered after replacement, with no missing transition and no duplicate external side effect.
- **SC-006**: Under production evidence, Product watch visibility p95 is ≤1 second, p99 is ≤3 seconds, and 10,000-event replay p95 is ≤5 seconds.
- **SC-007**: 100% of Product create, delete, and category-reassignment transitions leave all affected CategoryTaxonomy counts converged without changing an unrelated category.
- **SC-008**: 100% of Product retirement and deletion changes made after a release is active leave the active public snapshot unchanged until a replacement publication or authorized emergency suppression is applied.

## Assumptions and Scope Boundaries

- This specification is an **addition to and expansion of Product deletion safety**. It preserves the ProductVariant-to-Product pure-block rule and no-cascade decision from the original feature-055 draft.
- “Background deletion” means asynchronous, finalizer-governed completion after an accepted deletion request. It is not background propagation and does not delete ProductVariants.
- GraphQL Product creation follows the Namespace and Repository convention: the namespace's `gitstore-system` repository is its implicit Git authoring target. For an existing Product admitted through Git push, GraphQL update follows its persisted repository and source-path provenance, including for Products authored in a non-system repository.
- ProductVariant lifecycle beyond maintaining its Product owner reference is out of scope. Collection, inventory, pricing, and media contracts remain unchanged except where they observe Product transitions.
- The existing CategoryTaxonomy Product fan-out remains a separate controller responsibility and is preserved, not replaced, by Product lifecycle reconciliation.
- Product watch migration is additive: typed and generic Product subscriptions remain available while their backing stream moves to the durable contract.
- Public publication snapshots and their read endpoints are out of scope. This feature preserves their isolation from mutable Product state but does not implement public Product-serving APIs.
- The seller-workflow and publication documents are proposed designs. This feature
  establishes compatibility boundaries and Product-facing eligibility signals;
  it does not implement workflow definitions, releases, publications, public
  snapshots, scheduling, tag management, or emergency suppression.
