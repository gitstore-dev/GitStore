# Feature Specification: ProductVariant Full Lifecycle

**Feature Branch**: `067-product-variant-lifecycle`

**Created**: 2026-10-09

**Status**: Draft

**Input**: Complete ProductVariant lifecycle parity with Namespace, Repository, Product, and CategoryTaxonomy: Git push and Git-delegating GraphQL mutations, validation and admission, controller reconciliation, owner references, and finalizer-governed deletion. Include [#467](https://github.com/gitstore-dev/GitStore/issues/467), which reports stale parent resolution when a Product starts terminating.

## Clarifications

### Session 2026-10-09

- Use the next globally unused feature number, `067`, rather than the command's per-short-name `001`, because the existing planning scripts resolve feature directories by numeric prefix.
- Reject new submissions containing invalid selected options or pricing expressions. If a later parent change invalidates an existing variant, retain that variant and mark it not ready.

## User Scenarios & Testing *(mandatory)*

### User Story 1 - Authors manage variants through one auditable lifecycle (Priority: P1)

A catalog author creates, updates, or requests deletion of a ProductVariant through a Git push or a management mutation. Both entry points produce the same authored history, validation decisions, admitted identity, and lifecycle outcome. A mutation is not a second source of desired state.

**Why this priority**: ProductVariant is the purchasable SKU. Inconsistent write paths can admit contradictory product, option, or pricing information.

**Independent Test**: Create and update equivalent variants through each entry point, including a variant authored at a non-default path. Compare their desired state, revision, validation outcomes, and identity behavior.

**Acceptance Scenarios**:

1. **Given** an authorized author and active namespace/repository, **When** a valid variant is created through either entry point, **Then** it has one immutable UID, an admitted revision, repository ownership, and visible admission status.
2. **Given** a variant admitted from a user-chosen repository and path, **When** the author updates it through a management mutation, **Then** the original source is updated, not copied to another repository or path.
3. **Given** an existing variant, **When** its spec or Markdown body changes, **Then** its UID remains stable and its generation advances; a path-only move or labels/annotations-only change preserves generation while advancing resource version.
4. **Given** an invalid submission, an unauthorized submission, or a failed Git write, **When** either entry point processes it, **Then** the author receives an explicit failure and no successful admitted variant change is reported.
5. **Given** a proposed in-place parent-product change or cross-namespace reference, **When** it is submitted, **Then** it is rejected; changing the parent requires deleting the old variant and creating a new one.
6. **Given** concurrent submissions for the same identity, namespace SKU, or parent/selected-options combination, **When** they compete, **Then** at most one conflicting claim succeeds and the others receive explicit conflict outcomes.

---

### User Story 2 - Variants resolve their parent and remain truthfully ready (Priority: P1)

An author can push a Product and its variants together, or supply a missing Product later. The system converges the variants without requiring another variant edit. Readiness reflects the current admitted variant and parent, not the order in which resources happened to arrive.

**Why this priority**: Single-pass catalog authoring and trustworthy readiness are necessary for safe downstream use of the purchasable SKU.

**Independent Test**: Submit variants before and alongside their Product, then change the Product's options. Verify automatic resolution, option reevaluation, and eventual recovery without rewriting the variants.

**Acceptance Scenarios**:

1. **Given** a valid variant whose named Product is absent, **When** admission completes, **Then** the variant is retained with `ProductResolved=False`, reason `ProductNotFound`, and `Ready=False`, rather than being rejected merely because the parent is not yet present.
2. **Given** a Product and its variants in one push, **When** they are processed in any order, **Then** every valid variant eventually resolves the admitted Product UID and acquires the correct blocking Product owner reference.
3. **Given** a known Product, **When** a new variant submission contains an invalid option name/value or an invalid pricing expression, **Then** the submission is rejected and the previously admitted variant, if any, remains unchanged.
4. **Given** an admitted variant, **When** its parent's options change incompatibly, **Then** the variant remains present with failed option compatibility and `Ready=False`; restoring compatible options restores readiness automatically.
5. **Given** an unresolved variant and a lookup outage, **When** reconciliation cannot determine whether the Product exists, **Then** it reports an operational failure and retries, rather than presenting the outage as `ProductNotFound` or successful resolution.
6. **Given** two controllers processing duplicate or stale work, **When** a newer variant or Product version already exists, **Then** stale results cannot restore obsolete resolved data, readiness, or ownership.

---

### User Story 3 - Parent termination immediately invalidates variant resolution (Priority: P1)

An operator inspecting a variant must never be told that a terminating Product is an available parent. This closes #467 while preserving the existing rule that blocking variants prevent Product deletion; decoupling readiness does not mean deleting the variant or dropping its ownership protection.

**Why this priority**: Stale resolution misrepresents parent availability and can undermine deletion safety if corrected by removing the wrong relationship.

**Independent Test**: Seed a defensive race/legacy fixture containing a terminating Product and a still-linked variant. Observe the variant before the Product is removed, then repeat after controller replacement and with a stale reconciliation result.

**Acceptance Scenarios**:

1. **Given** a linked variant whose Product has a deletion timestamp, **When** the Product transition is observed or recovered after restart, **Then** the variant becomes `ProductResolved=False` with reason `ProductTerminating`, becomes `Ready=False`, and no longer exposes that Product as successfully resolved.
2. **Given** that same variant, **When** its resolution is invalidated, **Then** its authored parent reference and existing blocking Product owner reference remain intact and the Product cannot be finally removed while the blocker remains.
3. **Given** a terminating Product, **When** a new variant submission attempts to target it, **Then** admission rejects the submission and creates no new blocking relationship.
4. **Given** an already-admitted unresolved variant, **When** its named Product is discovered to be terminating, **Then** it remains unresolved with reason `ProductTerminating` and acquires no new Product owner reference.
5. **Given** a Product with a live blocking variant, **When** ordinary Product deletion is requested, **Then** the request is still rejected; this feature does not introduce cascading deletion to make the request succeed.
6. **Given** an old Product identity has disappeared and a Product with the same name is later created, **When** a previously linked variant is reconciled, **Then** it is not silently transferred to the replacement UID; the author must delete and recreate it. A variant that has never resolved may resolve the new Product normally.

---

### User Story 4 - Variant deletion is visible, safe, and recoverable (Priority: P1)

An author removes a variant through either authoring path. The variant becomes visibly terminating while cleanup runs, and is permanently removed only when deletion protections are satisfied. The operator can see why deletion is waiting.

**Why this priority**: Immediate projection removal bypasses finalizers, loses lifecycle evidence, and can release Product deletion protection prematurely.

**Independent Test**: Request variant deletion with and without a blocking dependent or an additional finalizer. Replace a controller during cleanup, retry deletion, and verify that parent deletion becomes eligible only after the variant is safely removed.

**Acceptance Scenarios**:

1. **Given** an eligible variant, **When** its manifest is deleted through a push or management mutation, **Then** the admitted record first exposes a deletion timestamp, foreground-deletion finalizer, and terminating state.
2. **Given** a terminating variant, **When** an author repeats deletion, **Then** the response identifies the already-started workflow rather than producing a competing deletion or a false not-found result.
3. **Given** a blocking dependent or uncompleted finalizer, **When** cleanup runs, **Then** the variant remains readable as terminating, stays not ready, and retains the ownership needed to protect its parent.
4. **Given** cleanup and all finalizers are complete and a fresh dependent check finds no blocker, **When** deletion finishes, **Then** the variant is removed exactly once and its final removal is observable separately from termination.
5. **Given** a deleted variant name, **When** the author recreates it after final removal, **Then** the new variant receives a new UID; recreation before final removal is rejected.
6. **Given** one change proposes deleting both a variant and its Product, **When** variant cleanup has not yet completed, **Then** Product deletion remains blocked. The system reports the outcome explicitly and permits retry after cleanup rather than purging either resource unsafely.

---

### User Story 5 - Authorized consumers can read and resume the complete lifecycle (Priority: P1)

Management clients and controllers can look up variants, page them by namespace or parent, and resume change observation through another service replica. Every read consistently distinguishes unresolved, ready, terminating, and finally deleted resources.

**Why this priority**: Reconciliation and operators need a complete, secure view of lifecycle transitions, not a process-local approximation.

**Independent Test**: Bootstrap a paged list and change stream, perform all lifecycle transitions, and resume from a saved cursor on another replica. Repeat with insufficient permissions and an expired cursor.

**Acceptance Scenarios**:

1. **Given** an authorized reader, **When** it uses identifier lookup, namespace listing, node lookup, or a Product's variant relationship, **Then** it receives consistent lifecycle metadata and status within the same authorized scope.
2. **Given** creates, updates, status changes, and deletions during list bootstrap, **When** a consumer finishes listing and drains changes, **Then** it obtains a complete view without missing acknowledged transitions.
3. **Given** a retained observation cursor, **When** the consumer reconnects through another replica, **Then** it resumes subsequent ordered changes; expiry or unavailable continuity yields an explicit recovery outcome.
4. **Given** an unauthorized or cross-namespace caller, **When** it requests any variant read, relationship, observation, or mutation surface, **Then** it receives no protected payload, existence disclosure, or usable cursor.
5. **Given** an author without lifecycle-system privileges, **When** it attempts to set status, ownership, finalizers, deletion timestamp, or identity/version fields, **Then** the write is rejected.
6. **Given** a controller permitted to report status but not permanently remove resources, **When** it attempts final deletion, **Then** the operation is denied independently of its status permission.

---

### User Story 6 - Operators recover safely under sustained catalog work (Priority: P2)

Operators can replace service processes, sustain catalog changes, and diagnose delayed resolution or deletion without losing acknowledged work or allowing one large Product family to starve unrelated variants.

**Why this priority**: Lifecycle parity must hold during real load and failures, not only for one resource on one process.

**Independent Test**: Run the production workload below with two API and two controller instances, replace one instance at a time, interrupt a dependency, and verify convergence and bounded resource use.

**Acceptance Scenarios**:

1. **Given** sustained authoring and observation, **When** an API or controller process is replaced or compatible versions overlap, **Then** accepted work survives, stale work cannot overwrite newer state, and lifecycle outcomes remain correct.
2. **Given** work exceeds configured capacity, **When** overload protection activates, **Then** callers receive explicit retryable outcomes, accepted work is recoverable, and unrelated healthy consumers keep progressing.
3. **Given** an interrupted parent reevaluation or deletion workflow, **When** dependencies recover, **Then** work resumes without a new author edit and meets the declared recovery deadline.
4. **Given** an operator investigating a stuck variant, **When** they inspect status and operational evidence, **Then** they can identify the resource, revision, failing phase, retry state, and authorization decision without exposing credentials.

### Edge Cases

- Missing parent is a supported unresolved state; an unreadable or unavailable parent is an operational error, not evidence of absence.
- A Product can be admitted after its variants, and parent option changes can arrive while variant resolution is in flight.
- A terminating Product plus a live variant is a defensive recovery case, not permission to weaken the existing Product deletion precondition.
- Existing variants invalidated by parent changes stay stored; new submissions with known invalid options or pricing are rejected.
- Empty selected-options combinations and differently ordered equivalent combinations follow the same per-parent uniqueness rule.
- Moving a source file preserves identity; renaming the resource is guarded deletion plus creation, not an in-place identity edit.
- Re-adding a manifest while the prior variant is terminating cannot cancel deletion or resurrect its UID.
- An old resolved parent UID cannot be replaced by a same-name Product merely because a stale event or name lookup found it.
- Finalizers owned by another component are preserved; a cleanup error or uncertain dependent lookup blocks final removal.
- Interrupted post-acceptance processing must remain visibly incomplete and recoverable, not silently appear admitted.
- Slow observers, expired cursors, status-only changes, repeated no-op work, and resume on another replica must have explicit, consistent outcomes.
- Repository or namespace termination must not permit new variants or bypass existing owner-deletion protections.

## Requirements *(mandatory)*

### Functional Requirements

- **FR-001**: Git MUST remain the canonical source of ProductVariant desired state. Authorized Git pushes and GraphQL create, update, and delete mutations MUST traverse equivalent validation and admission; mutations MUST NOT directly persist authored desired state.
- **FR-002**: GraphQL creation MUST use the namespace's `gitstore-system` repository. Updates and deletion MUST use the existing variant's admitted repository and source-path provenance, including non-system repositories and moved files. Mutation inputs MUST NOT add repository/path selectors; deletion MUST accept the standard opaque resource ID and distinguish termination started from already terminating.
- **FR-003**: Both authoring paths MUST report validation, authorization, conflict, Git-write, and admission failures explicitly. A known invalid submission MUST NOT replace previously admitted state or be reported as successful admission. If processing is interrupted after a Git change is accepted, the unresolved admission outcome MUST be observable and recoverable without inventing success.
- **FR-004**: ProductVariant identity MUST be namespace-scoped and independent of file path. Mutable spec/body changes advance generation; provenance-only and labels/annotations-only changes do not. Every persisted lifecycle change advances resource version, and delete/recreate assigns a new UID.
- **FR-005**: Namespace, identity, and parent-product reference MUST NOT be changed in place. Namespace and repository MUST be active for new admissions. Cross-namespace parent references and user-authored system metadata/status MUST be rejected.
- **FR-006**: Validation MUST preserve the existing variant field model, including pricing, inventory policy, media references, Markdown body, and selected options. Malformed documents, invalid option names/values against a known parent, and syntactically invalid pricing expressions MUST be rejected with field-specific reasons. Unavailable validation dependencies MUST produce operational failures, not successful validation.
- **FR-007**: SKU values, when supplied, MUST be unique within a namespace. Canonically equivalent selected-options combinations MUST be unique per parent within a namespace, independent of option ordering, including the empty combination. Concurrent submissions and deferred resolution MUST NOT create competing successful claims.
- **FR-008**: A missing Product MUST NOT alone prevent admission of an otherwise valid variant. The variant MUST remain unresolved and not ready until a same-namespace, non-terminating Product and option compatibility can be verified. Deferred validation failures MUST remain visible and MUST NOT promote the variant to ready.
- **FR-009**: Reconciliation MUST respond to variant and relevant Product changes, recover pending work after restart, reevaluate option compatibility, and maintain generation-aware conditions and resolved summaries without requiring a new author edit.
- **FR-010**: `Ready=True` MUST require successful admission, current parent resolution, compatible selected options, valid pricing, and a non-terminating variant. Unevaluated or failed required checks MUST NOT count as success. Parent changes invalidating an existing variant MUST clear readiness without deleting or rewriting authored desired state.
- **FR-011**: System-managed owner references MUST identify the admitting Repository and, once resolved, the Product UID. Both relationships MUST preserve the existing blocking deletion-safety contract. Product ownership MUST NOT be reassigned to a replacement UID with the same name for an already-linked variant.
- **FR-012**: When a Product is terminating, every existing linked variant MUST converge to `ProductResolved=False` with reason `ProductTerminating`, clear its successfully resolved Product summary, and become not ready before parent removal is required. The authored parent reference and existing blocking ownership MUST remain intact. This is the acceptance requirement for #467.
- **FR-013**: New submissions targeting a terminating Product MUST be rejected. Already-admitted unresolved variants MUST NOT establish a new relationship to it; they MUST expose `ProductTerminating` and remain not ready.
- **FR-014**: Product deletion MUST continue rejecting live blocking variants, and final Product removal MUST continue performing a fresh blocking-dependent check. This feature MUST NOT cascade-delete variants, remove owner references to evade that check, or create a Product-finalizer/variant-finalizer deadlock.
- **FR-015**: Variant deletion through either authoring path MUST enter a visible terminating state with deletion timestamp and foreground-deletion finalizer before permanent removal. A terminating variant MUST remain available to authorized management reads and MUST NOT be considered ready or accept ordinary desired-state updates.
- **FR-016**: Finalization MUST complete applicable existing membership/reference cleanup, preserve other components' finalizers, and verify blocking dependents at the removal boundary. Cleanup errors or uncertain lookups MUST prevent final removal. No nonexistent runtime subsystem may be treated as an implemented deletion guard.
- **FR-017**: Deletion, cleanup, status updates, and final removal MUST be duplicate-safe and reject stale concurrent writes. A blocking variant relationship MUST remain effective until safe variant removal; repeated deletion MUST resume or identify the same workflow. Final-removal authority MUST be separate from ordinary authoring and status authority.
- **FR-018**: Individual lookup, namespace connections, global node lookup, and the Product-to-variant connection MUST expose consistent desired state, provenance, status, owner references, finalizers, and deletion timestamp. Listings and relationships MUST be paginated without loading an entire namespace or Product family for one page.
- **FR-019**: ProductVariants MUST support the established durable resource-observation contract through typed and generic watches: gap-free bootstrap/list/drain, namespace and selector scoping, replica-portable opaque cursors, ordered replay, bookmarks, bounded delivery, and explicit expiry/unavailability recovery.
- **FR-020**: Every acknowledged admission, desired-state change, status/ownership change, termination/finalizer change, and final removal MUST be observable. Termination and final removal MUST remain distinct; failed requests and no-ops MUST NOT fabricate successful changes.
- **FR-021**: All variant entry points, including nested relationships, node lookup, typed/generic watches, status writes, and final deletion, MUST enforce configured pluggable authentication and authorization before exposing protected state. Human, service, and controller identities MUST remain namespace/repository isolated and least-privilege.
- **FR-022**: Reconciliation MUST preserve admission-owned status and unrelated computed summaries. Existing parent-category reconciliation and Product deletion behavior MUST remain correct; related counts or sums MUST NOT be computed through live whole-set queries on a read request.
- **FR-023**: Operators MUST receive attributable, credential-safe evidence of admission failure, unresolved or terminating parents, incompatible options, watch recovery, overload, cleanup blockers, and retry progress. Documentation MUST describe both authoring paths, condition meanings, finalization, permissions, rollout, and recovery.
- **FR-024**: Variant reads and watches MUST remain private current-catalog management surfaces. Admission/readiness MUST NOT publish an offer or mutate immutable release/publication state. Product retirement semantics remain unchanged; this feature adds no independently authored variant retirement state.

### Production Requirements *(mandatory for core-service or load-bearing changes)*

- **PR-001 Replica Safety**: Verify each affected admission, observation, ownership, status, and deletion path with at least two API and two controller replicas, including simultaneous writes, process replacement, and overlapping compatible versions. Rollout and rollback MUST identify unsafe mixed-version windows and prevent older writers from bypassing new protections. Git remains exactly one active process using retained storage; replacement MUST be non-overlapping and may involve downtime.
- **PR-002 Multi-User Security**: Run the full access matrix for readers, authors, status writers, deletion-completion identities, and unauthorized subjects in at least two namespaces and repositories, across supported authentication-provider configurations. No unauthorized payload, existence signal, cursor, or lifecycle write is permitted.
- **PR-003 Capacity**: The production envelope is at least 5,000,000 Products and 5,000,000 ProductVariants, including one Product with 10,000 variants, 1,000 concurrent variant observers, and 32 concurrent author clients. Sustain at least 5 Git pushes/second with up to 10 changed variant documents per push plus 10 management mutations/second for 60 minutes. Each document is at most 8 KiB in this workload and includes parent/options, two pricing rules with eligibility expressions, inventory policy, media references, and body content. Alternate creates, updates, deletions, parent-option changes, and rejected submissions. These are acceptance-workload bounds, not new general resource-size limits.
- **PR-004 Latency and Backpressure**: Under PR-003, successful authoring acknowledgements MUST achieve p95 <=2 seconds and p99 <=5 seconds; change visibility p95 <=1 second and p99 <=3 seconds; ordinary parent/readiness convergence p95 <=5 seconds and p99 <=15 seconds. A 10,000-variant parent change MUST converge within 60 seconds without starving unrelated work. Lists use at most 250 items per page. Planning MUST declare finite concurrency, pending-work, retry, timeout, and memory/disk budgets before implementation; the 60-minute run MUST stay within them. Five minutes at twice the offered load MUST produce explicit backpressure rather than lost accepted work or unbounded growth.
- **PR-005 Capacity Evidence**: Extend the existing `make capacity TARGET=repository PROFILE=lifecycle MODE=<diagnostic|alpha|production>` scenario, not a new public command family, with actual variant and parent work. Evidence MUST identify current-run fixtures, acknowledged revisions, full dataset counts, topology, workload, rejected versus unexpected failures, and measured outcomes. The correctness verifier MUST check UID stability, ownership, option/SKU uniqueness, #467 conditions, blocker safety, and watch continuity. Production requires all PR-003/004 thresholds, 10,000-event replay p95 <=5 seconds, and an unexpected-operation failure rate below 0.1%; diagnostic or smaller runs cannot certify production readiness.
- **PR-006 Fault Recovery**: Reuse `make chaos CHAOS_PROFILE=tests/chaos/profiles/controller-restart.json` and `make chaos CHAOS_PROFILE=tests/chaos/profiles/api-restart.json` against explicitly owned targets while the lifecycle workload runs. Include observation interruption, interrupted finalization, and dependency unavailability in the same acceptance exercise. Service readiness and pending lifecycle convergence MUST recover within 60 seconds after dependencies are restored; no acknowledged work may be lost and no resource may be removed with a live blocker.
- **PR-007 Recovery Integrity**: Repeated delivery, stale cursors, lost process-local state, and replacement during parent fan-out MUST lead either to safe resume or explicit relisting/recovery. Once normal offered load resumes after overload, the accepted backlog MUST drain within 60 seconds without duplicate external effects. Evidence MUST distinguish verified replica-safe paths from pre-existing gaps rather than claim blanket high availability.

### Key Entities

- **ProductVariant**: The namespace-scoped purchasable SKU, with authored parent reference, selected options, pricing, inventory policy, media references, descriptive body, immutable UID, and Git provenance.
- **Parent Product**: The non-sellable grouping resource whose identity, termination, and declared options determine whether a variant can resolve and be ready.
- **Ownership relationship**: A system-maintained link to a specific Repository or Product identity that protects that owner from deletion; it is distinct from a successful resolved-summary field.
- **Variant lifecycle state**: Admission, resolution/readiness, termination, cleanup, and final removal, with current-generation conditions, deletion timestamp, and finalizers.
- **Durable change observation**: An authorized sequence of admitted variant transitions that can be resumed across service replicas or explicitly recovered when continuity is unavailable.

## Success Criteria *(mandatory)*

### Measurable Outcomes

- **SC-001**: In the acceptance matrix, 100% of valid equivalent authoring operations produce equivalent desired state and auditable revisions; 100% of invalid or unauthorized submissions return explicit failure with no successful invalid change.
- **SC-002**: In all co-creation, deferred-parent, duplicate-delivery, and parent-option-change cases, variants converge without a second author edit, and no variant is reported ready with a missing/terminating parent or invalid required checks.
- **SC-003**: In 100% of #467 regression cases, variants stop reporting a terminating parent as resolved before that parent is removed, while retaining the ownership that protects it from unsafe deletion.
- **SC-004**: Across deletion, recreation, and concurrency tests, zero Products or variants are removed while a live blocker or unfinished finalizer remains; all eligible variants expose termination before final removal, and every completed recreation uses a new identity.
- **SC-005**: The complete access matrix produces zero unauthorized disclosures or lifecycle changes, including indirect relationship access and resumed observations.
- **SC-006**: With two serving instances and two reconciliation instances, replacing either one during active work loses zero acknowledged transitions and produces zero duplicate external effects or stale-state reversions.
- **SC-007**: At the production dataset and 60-minute workload, authors and observers meet PR-004 response/visibility targets, ordinary readiness converges within its stated percentiles, and the largest declared Product family converges within 60 seconds.
- **SC-008**: Under normal load, unexpected failures stay below 0.1%; during overload all accepted work remains recoverable, declared resource budgets remain respected, and the backlog clears within 60 seconds after normal load returns.
- **SC-009**: Every declared recovery exercise restores service and pending lifecycle progress within 60 seconds after dependency restoration, with zero lost accepted work and zero unsafe removals.

## Assumptions and Scope Boundaries

- This is specification work, not an assertion that current code already satisfies lifecycle parity or that production gates have passed. The capacity envelope is a planning baseline for this feature, not a measured result.
- The worktree is based on checkout `1a8ee64`, which includes the existing Product/category work. The primary checkout remains unchanged.
- [ADR 0005](../../docs/ADRs/0005-product-variant-lifecycle.md), [Product lifecycle](../055-product-lifecycle/spec.md), [ProductVariant catalog model](../024-product-variant/spec.md), and [the current variant reference](../../docs/products/product-variant-spec.md) supply the baseline. Later Product deletion safety takes precedence over older suggestions that parent deletion can simply orphan variants.
- GraphQL routing follows the newer Product convention: implicit system repository for creation and admitted provenance for existing resources. This intentionally replaces ADR 0005's older suggestion of a caller-selected repository.
- The confirmed validation decision supersedes the current behavior that admits invalid options/pricing with failed conditions. Missing-parent deferral remains supported; it is not permission to ignore known invalid data.
- Existing condition names, including `OptionsAccepted`, are retained where possible. The ADR's option-compatibility wording does not by itself require a second synonymous condition.
- SKU optionality and its existing warning-only immutability policy remain unchanged; this feature enforces uniqueness when a SKU is present but does not introduce a hard SKU-change ban.
- Variants with an unresolved parent are retained rather than automatically deleted. Product termination invalidates availability, not identity or deletion protection. Parent deletion is pure-block, never cascade.
- Full media materialization, stock availability calculation, inventory Reservations/Allocations, carts, checkout, orders, new Collection controllers, and release/publication implementation are out of scope. Existing cleanup integrations must remain correct; missing runtime subsystems must be documented rather than simulated or silently advertised as implemented.
- Existing authored pricing, inventory, and media data must be preserved; this feature does not add a pricing runtime or claim that admitting an inventory policy establishes available stock.
- No new public storefront surface, variant retirement state, broad refactoring of other resource kinds, Git replication, repository sharding, or zero-downtime Git failover is included.
- Implementation planning must identify remaining gaps in current variant admission, reads, durable observation, reconciliation, ownership, and finalization, reusing the established contracts rather than copying older incomplete behavior.
