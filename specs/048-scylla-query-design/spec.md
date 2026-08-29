# Feature Specification: Scylla Query and Recovery Hardening

**Feature Branch**: `048-scylla-query-design`  
**Created**: 2026-08-19  
**Status**: In progress  
**Input**: User description: "Create a branch with the current branch as tip and implement #353 and the related Scylla hardening sub-issues."

## User Scenarios & Testing *(mandatory)*

### User Story 1 - Predictable Namespace and Repository Queries (Priority: P1)

As a GitStore operator, I need Namespace and Repository reads to remain predictable as the catalogue grows so that routine lookups and pagination do not create hot partitions, full-result scans, or unbounded memory usage.

**Why this priority**: These resources are on critical routing and authorization paths. Poor partitioning or unbounded listing behavior can degrade every repository operation.

**Independent Test**: Populate Namespaces and Repositories across multiple time periods and ownership boundaries, then verify direct lookups and bidirectional pagination return complete, globally ordered results without duplicate or missing records.

**Acceptance Scenarios**:

1. **Given** Namespaces created across at least three monthly boundaries, **When** a caller paginates forward and backward, **Then** every Namespace appears exactly once in global creation order.
2. **Given** a Namespace UID, **When** it is fetched directly, **Then** the authoritative record is returned without scanning unrelated Namespaces.
3. **Given** many Repositories owned by multiple Namespaces, **When** a caller fetches one Repository or lists one Namespace's Repositories, **Then** only records relevant to that access path are read.
4. **Given** a requested Repository page size, **When** the page is returned, **Then** work remains bounded by the requested page plus fixed pagination overhead rather than the total Repository count.
5. **Given** a Namespace or Repository manifest with Markdown content after its frontmatter, **When** the resource is admitted and queried, **Then** the Markdown body is returned unchanged.
6. **Given** any supported manifest-backed resource, **When** it is persisted and retrieved, **Then** the same canonical API version, kind, metadata, provenance, spec, body, and status fields are available regardless of resource kind.

---

### User Story 2 - Safe Concurrent Catalogue Writes (Priority: P1)

As a catalogue author, I need concurrent creates and denormalized updates to produce deterministic outcomes so that duplicate names or identifiers cannot silently overwrite resources.

**Why this priority**: Silent overwrite or partial projection state can corrupt catalogue identity and return contradictory results depending on the read path.

**Independent Test**: Submit concurrent creates for the same identity and inject failures between projection updates; verify one create succeeds, all competing creates receive the same conflict outcome, and recovery converges every read path on one authoritative resource.

**Acceptance Scenarios**:

1. **Given** two simultaneous creates with the same unique name, **When** both are processed, **Then** exactly one succeeds and the other receives a deterministic already-exists result.
2. **Given** two simultaneous creates with the same UID, **When** both are processed, **Then** no existing resource is overwritten.
3. **Given** a failure after the authoritative resource is written but before every query projection is updated, **When** the operation is retried or repaired, **Then** all projections converge without creating duplicates.
4. **Given** a partially completed multi-projection mutation, **When** operators inspect system health, **Then** the partial failure is visible and has an actionable recovery path.

---

### User Story 3 - Recoverable Repository Rename and Transfer (Priority: P1)

As a repository administrator, I need rename and transfer operations to recover safely from partial failure so that a Repository cannot retain duplicate, stale, or contradictory namespace mappings.

**Why this priority**: Repository mappings determine clone and push routing. Stale mappings can route traffic incorrectly or make one Repository reachable by multiple unintended paths.

**Independent Test**: Interrupt rename and transfer operations after each logical step, retry them, and verify the Repository ends with exactly one valid path while stale mappings are detected and repaired.

**Acceptance Scenarios**:

1. **Given** a rename interrupted after the new path is reserved, **When** the operation is retried, **Then** it completes without duplicate mappings.
2. **Given** a transfer interrupted after ownership changes but before the old mapping is removed, **When** recovery runs, **Then** only the target Namespace mapping remains active.
3. **Given** an existing stale mapping, **When** consistency checks run, **Then** the stale mapping is reported rather than silently ignored.
4. **Given** the same rename or transfer request is repeated, **When** the desired final state already exists, **Then** the operation succeeds idempotently without additional mappings.

---

### User Story 4 - Operable Denormalized Storage (Priority: P2)

As an operator, I need clear safeguards and repair procedures for tombstones, high-churn partitions, and dangling lookup records so that storage degradation can be detected before it affects users.

**Why this priority**: Query-first projections require operational controls; without them, normal churn and partial failures accumulate into latency and correctness risks.

**Independent Test**: Create high-churn and dangling-projection conditions, then verify monitoring identifies them and the documented repair procedure restores consistency.

**Acceptance Scenarios**:

1. **Given** a high-churn partition, **When** deletion and update volume crosses the documented safe threshold, **Then** operators receive a measurable signal identifying the affected resource type.
2. **Given** a lookup row whose authoritative record is absent, **When** it is encountered, **Then** the inconsistency is logged and counted rather than skipped silently.
3. **Given** stale projections, **When** the repair procedure is executed, **Then** valid projections are restored and dangling rows are removed without changing authoritative resource content.

---

### User Story 5 - Evidence-Based Product Selector Decision (Priority: P3)

As a catalogue architect, I need product label-selector redesign to remain deferred until Product and Collection controllers provide real access-pattern evidence so that the system does not commit prematurely to an unsuitable materialization model.

**Why this priority**: The existing scan is known but the proposed replacement is not accepted. Premature implementation would create migration cost without validated controller requirements.

**Independent Test**: Review the feature scope and resulting design records to verify no product selector projection is introduced and that explicit decision criteria are documented for the post-controller evaluation.

**Acceptance Scenarios**:

1. **Given** this hardening feature is completed, **When** storage changes are reviewed, **Then** the current product label-selector behavior remains unchanged.
2. **Given** Product and Collection controllers are available in the future, **When** selector redesign is reconsidered, **Then** the decision compares controller-maintained projections, inverted lookup structures, and external search using measured cardinality and access patterns.

### Edge Cases

- Pagination begins or ends exactly at a monthly Namespace bucket boundary.
- A page cursor references a bucket that contains no remaining records.
- A Repository is renamed to its current name or transferred to its current Namespace.
- A retry occurs after only one of several denormalized projections was updated.
- A uniqueness reservation exists but its authoritative resource does not.
- A stale mapping points to a deleted Repository or Namespace.
- Repair runs concurrently with a legitimate create, rename, transfer, or delete.
- Monitoring encounters a dangling lookup repeatedly before repair completes.
- A global Repository listing spans enough records to require multiple bounded partitions.

## Requirements *(mandatory)*

### Functional Requirements

- **FR-001**: The system MUST preserve bounded monthly Namespace listing partitions and globally ordered keyset pagination across partition boundaries.
- **FR-002**: The system MUST perform Namespace UID lookup against one authoritative record location.
- **FR-003**: The system MUST perform Namespace name lookup through an explicit name-to-UID projection rather than a general-purpose secondary lookup.
- **FR-004**: Namespace optimistic updates and resource-version deletions MUST target the same authoritative record used by direct lookup.
- **FR-005**: The system MUST provide a direct Repository UID lookup that does not depend on a global partition or secondary lookup.
- **FR-006**: The system MUST provide Namespace-scoped Repository listing with bounded server-side pagination and stable ordering.
- **FR-007**: Global Repository listing MUST distribute records across bounded partitions and MUST NOT place every Repository in one partition.
- **FR-008**: Primary Namespace and Repository read paths MUST NOT require full-result in-memory sorting or filtering of unrelated records.
- **FR-009**: Multi-projection catalogue mutations MUST use retryable, idempotent steps and MUST NOT claim cross-partition atomicity that cannot be guaranteed.
- **FR-010**: Concurrent creates for the same unique name or identifier MUST result in exactly one successful reservation and deterministic conflict results for all competitors.
- **FR-011**: The system MUST define compensation or reconciliation behavior for every mutation that can leave authoritative and query projections temporarily inconsistent.
- **FR-012**: Repository rename and transfer MUST be idempotent and recoverable after interruption at any logical step.
- **FR-013**: The system MUST detect duplicate or stale Repository mappings and expose them as consistency failures.
- **FR-014**: Projection write failures, repair attempts, dangling lookups, and mutation latency MUST be observable through structured operational signals.
- **FR-015**: Operators MUST have documented, resource-specific guidance for high-churn partitions, tombstone monitoring, compaction behavior, and safe deletion-grace configuration.
- **FR-016**: Operators MUST have a documented repair procedure that reconciles stale or missing query projections from authoritative records.
- **FR-017**: Shared datastore contract tests MUST verify cross-bucket pagination, concurrent uniqueness, optimistic updates, versioned deletion, partial-failure recovery, and idempotent rename/transfer behavior.
- **FR-018**: Storage-specific tests MUST verify partitioning and query shape without duplicating backend-independent behavioral assertions.
- **FR-019**: The Namespace query-first behavior delivered by the parent branch for issues #353 and #354 MUST remain regression protected.
- **FR-020**: Product label-selector storage redesign described by issue #359 MUST remain out of implementation scope until Product and Collection controllers exist and measured access-pattern evidence is available.
- **FR-021**: The future product selector decision record MUST compare at least controller-maintained projections, inverted lookup structures, and external search without presupposing a preferred solution.
- **FR-022**: Every implemented issue in the #352 initiative, except the explicitly deferred #359 redesign, MUST have traceable acceptance coverage and operational documentation.
- **FR-023**: Persisted resource identity MUST use `uid`, Repository references MUST use `repository_id`, and `namespace` MUST consistently represent the immutable Namespace name; any stored Namespace UUID MUST be named `namespace_uid`.
- **FR-024**: Namespace and Repository authoritative records MUST persist the raw Markdown body from their manifest separately from structured metadata and spec fields.
- **FR-025**: Namespace and Repository body changes MUST participate in resource generation and optimistic-concurrency semantics in the same way as spec changes.
- **FR-026**: Namespace and Repository reads, including connection results, MUST return the persisted Markdown body without normalization or loss.
- **FR-027**: Every authoritative manifest-backed resource row MUST physically persist the same canonical resource-envelope superset with compatible data types for API version, kind, identity, metadata, lifecycle, audit, Git provenance, spec, body, and status; fields without a value for a resource MAY be null or empty.
- **FR-028**: Canonical database field names MUST be `api_version`, `kind`, `namespace` where applicable, `uid`, `name`, `generation`, `resource_version`, `revision`, `creation_timestamp`, `creation_actor`, `update_timestamp`, `update_actor`, `labels`, `annotations`, `owner_references`, `finalizers`, `deletion_timestamp`, `repository_id` where applicable, `source_path`, `git_commit_sha`, `git_ref`, `spec`, `body`, and `status`.
- **FR-029**: Namespace and Repository authoritative rows MUST persist the complete canonical resource envelope rather than synthesizing omitted fields at read time.
- **FR-030**: Collection and CategoryTaxonomy authoritative rows MUST persist owner references using the same `owner_references` field as other manifest-backed resources.
- **FR-031**: Query-only projections MAY omit fields not required by their access pattern, but their reduced shape MUST be explicit and they MUST hydrate the authoritative resource before returning a complete API object.
- **FR-032**: Resource-specific query fields MUST supplement the canonical envelope without renaming or replacing its fields; Namespace is the only structural exception and omits parent `namespace` and authoring `repository_id`.

### Key Entities

- **Authoritative Resource Record**: The single source of truth for a Namespace, Repository, or catalogue resource, including identity and version state.
- **Manifest Body**: Raw Markdown content after frontmatter, preserved independently from structured metadata and spec fields.
- **Resource Envelope**: The common persisted API version, kind, identity, metadata, provenance, spec, body, and status contract shared by all manifest-backed resources.
- **Query Projection**: A denormalized representation optimized for one explicit lookup or listing access pattern.
- **Uniqueness Reservation**: The concurrency-safe claim that prevents multiple resources from acquiring the same unique identity.
- **Repository Mapping**: The active association between a Namespace path and a stable Repository identity.
- **Recovery Operation**: An idempotent sequence that completes, compensates, or reconciles a partially applied mutation.
- **Consistency Finding**: An observable dangling, duplicate, stale, or missing projection that requires repair.
- **Operational Threshold**: A documented limit or signal for high churn, tombstones, mutation latency, or repair backlog.

### Scope

**In scope**:

- Completion and regression protection for issues #353 and #354.
- Repository access-pattern redesign from issue #355.
- Recoverable catalogue mutation behavior from issues #356 and #357.
- Recoverable Repository rename and transfer from issue #358.
- Operational safeguards and repair procedures from issue #360.
- Explicit deferral criteria and future decision requirements for issue #359.

**Out of scope**:

- Implementing a new product label-selector materialization model.
- Introducing an external search service.
- Preserving upgrade compatibility with unpublished alpha storage layouts.
- Changing user-facing catalogue resource semantics unrelated to consistency or query behavior.

### Assumptions

- The current branch already contains the Namespace storage changes described by issues #353 and #354; this feature validates and protects them rather than replacing them again.
- GitStore remains alpha software, so storage baselines may be corrected without supporting migration from unpublished layouts.
- Authoritative records may be briefly ahead of query projections, provided the inconsistency is observable, retryable, and repairable.
- Product and Collection controller access patterns are not yet sufficiently mature to select the final product selector storage model.

### Dependencies

- The Namespace lifecycle and storage behavior present at the tip of the source branch.
- Existing datastore contract-test infrastructure and failure-injection facilities.
- Availability of operational logging and metrics surfaces for consistency findings.
- Product and Collection controller delivery before issue #359 can progress beyond decision criteria.

## Success Criteria *(mandatory)*

### Measurable Outcomes

- **SC-001**: Forward and backward Namespace pagination across at least three monthly boundaries returns 100% of expected records exactly once and in stable order.
- **SC-002**: Direct Namespace and Repository lookups read only the requested identity's authoritative location, with no scan of unrelated resources.
- **SC-003**: Namespace-scoped and global Repository pages perform work bounded by the requested page size plus fixed overhead, independent of total Repository count.
- **SC-004**: In 100 repeated concurrent-create trials per unique key type, exactly one request succeeds and every competing request receives the same already-exists outcome.
- **SC-005**: Failure injection after every logical step of create, update, rename, transfer, and delete demonstrates that retry or repair converges to one correct final state with no duplicate active projections.
- **SC-006**: 100% of encountered dangling lookup records produce an operator-visible signal containing the resource type and lookup identity.
- **SC-007**: The documented repair procedure restores all injected stale or missing projections while preserving authoritative resource content.
- **SC-008**: Operational guidance defines measurable warning thresholds and remediation steps for every high-churn or tombstone-producing table in scope.
- **SC-009**: All acceptance criteria from issues #353-#358 and #360 are covered by automated tests or documented operational verification.
- **SC-010**: Issue #359 remains unchanged at feature completion, with a documented evidence checklist for its future design decision.
- **SC-011**: Namespace and Repository Markdown bodies round-trip byte-for-byte through create, update, direct lookup, and paginated-list test scenarios.
- **SC-012**: Schema-contract tests verify 100% of authoritative manifest-backed tables expose the canonical envelope with the required names and compatible types.
