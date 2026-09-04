# Feature Specification: Repository OwnerReferences Backfill for Catalog Resources

**Feature Branch**: `054-repository-ownerreferences-backfill`
**Created**: 2026-08-20
**Status**: Draft
**Input**: User description: "Repository OwnerReferences Backfill for Catalog Resources (GH#376). Populate metadata.ownerReferences on every git-backed catalog resource (Product, ProductVariant, CategoryTaxonomy, Collection, File) pointing at its owning Repository, as already documented by ADR-0003 through ADR-0008 but implemented by no admission path today. In scope: every git-backed catalog resource gets a {kind: Repository, name, uid, blockOwnerDeletion: true} entry written at admission time; Repository rename/transfer bookkeeping for dependent ownerReferences entries is defined; existing HasCatalogResources-based deletion precondition (spec 041) is confirmed unchanged. Out of scope: Repository's own deletion-precondition mechanism, Namespace→Repository ownership bookkeeping, and spec 052's CategoryTaxonomy-internal ownerReferences mechanism (same field, different direction, complementary not overlapping)."

## Clarifications

### Session 2026-08-20

- Q: GH#376's title says "Backfill," but its acceptance criteria only describe admission-path writes for resources going through admission — does this spec require a one-time migration pass over every catalog resource that already exists in the datastore today? → A: No. A dedicated, one-time, fleet-wide migration is not built. Instead, the entry is written on Create (closing the gap for all future resources) and self-healed on Update (closing the gap for existing resources the next time they are pushed or updated through the GraphQL API). This is chosen because the issue's own acceptance criteria describe only admission-path behavior, and no per-kind controller/reconciler infrastructure exists today for three of the five in-scope kinds (`Product`, `ProductVariant`, `Collection` have no reconciler in `gitstore-controller-manager`; only `CategoryTaxonomy` and `Namespace` do) to host a bespoke migration pass without materially expanding this issue's scope into building new controller infrastructure it does not ask for.
- Q: Is the entry written only at resource Create, or also verified/corrected on every subsequent Update? → A: Both. Create writes it fresh as part of the same admission call that already resolves and validates the containing Repository. Update re-verifies and corrects it if it is missing or stale, at no additional lookup cost, because the Repository record is already fetched during the existing namespace/repository-`Active` precondition check that runs on every admission call today, not only on Create.
- Q: What is the rename/transfer bookkeeping mechanism for this entry? → A: None is built for Phase 1, because none is needed. `renameRepository` and `transferRepository` are both `Unimplemented` in Phase 1 (`docs/ADRs/0003-repository-lifecycle.md`); the only way a Repository's identity changes today is delete-then-recreate (a new UID), and `deleteRepository` already synchronously rejects when any catalog resource still exists (spec 041's `HasCatalogResources`). By construction, no dependent resource can ever be left holding an entry that points at a UID which has stopped existing — the dependent set is guaranteed empty at the moment a UID's lifetime ends. This is explicitly unlike `CategoryTaxonomy`'s `ancestorPath` recomputation-on-reparent (`docs/ADRs/0006-category-taxonomy-lifecycle.md`, spec 039), which exists precisely because re-parenting is an in-place update that preserves the child's UID while the referenced parent's identity changes underneath it — Repository has no equivalent in-place, live-dependent-preserving identity change in Phase 1. If a future Phase 2 `transferRepository` operation is built, it will need its own bookkeeping design at that time; this spec does not anticipate or build it.
- Q: Does writing this entry replace, duplicate, or otherwise change spec 041's `HasRepositories`/`HasCatalogResources` deletion-precondition check? → A: No. That check remains the sole, unchanged, already-shipped enforcement mechanism for Repository deletion safety. This spec is purely additive metadata — closing a documented-but-missing field — and does not alter what blocks or permits a repository deletion today.

## User Scenarios & Testing *(mandatory)*

### User Story 1 - Newly created catalog resources record which repository owns them (Priority: P1)

Today, creating a `Product`, `ProductVariant`, `CategoryTaxonomy`, or `Collection` — whether by pushing a manifest directly or by calling a GraphQL create mutation that delegates to git — results in a stored record whose `metadata.ownerReferences` is empty, even though every one of that resource kind's lifecycle documentation (ADR-0004 through ADR-0007) states that `ownerReferences` is written pointing at the repository as part of Create. An author or integrator has no reliable, indexed way to answer "which repository owns this resource" from the resource's own metadata.

**Why this priority**: This is the core correctness gap the issue exists to close. Every other user story in this spec depends on this write actually happening.

**Independent Test**: Can be fully tested by creating a new resource of any of the four kinds (via git push or the corresponding create mutation) against an `Active` namespace and repository, then reading back the created record's `metadata.ownerReferences` and confirming it contains the expected entry — no deletion, rename, or update behavior needs to exist yet.

**Acceptance Scenarios**:

1. **Given** an `Active` namespace and `Active` repository, **When** a `Product` manifest is pushed for the first time, **Then** the created record's `metadata.ownerReferences` contains an entry `{kind: Repository, name: <repository name>, uid: <repository uid>, blockOwnerDeletion: true}`.
2. **Given** the same setup, **When** a `createProduct` GraphQL mutation is used instead of a raw git push, **Then** the resulting record carries the identical entry — the delegation-to-git path produces the same outcome as a direct push, not a weaker or different one.
3. **Given** a `CategoryTaxonomy`, `ProductVariant`, or `Collection` is created via either path, **Then** the same entry shape is present on each — behavior is uniform across all four kinds, not kind-specific.

---

### User Story 2 - Repository deletion safety is unaffected by the new metadata (Priority: P1)

Operators and callers rely on the already-shipped precondition (spec 041) that rejects deleting a repository while any catalog resource still exists in it. This feature must not weaken, duplicate, bypass, or otherwise change that behavior merely because affected resources now also carry a Repository-pointing `ownerReferences` entry.

**Why this priority**: Equal to User Story 1 — a metadata-completeness change that accidentally altered deletion safety would be a regression disguised as an improvement.

**Independent Test**: Can be fully tested by attempting to delete a repository that has at least one dependent catalog resource (which, per User Story 1, now also carries the new entry) and confirming the delete is rejected via the same existing mechanism and reason as before this feature shipped, with no observable behavior change.

**Acceptance Scenarios**:

1. **Given** a repository with at least one `Product`, **When** `deleteRepository` is called, **Then** it is rejected exactly as it was before this feature existed, citing catalog resources present, regardless of whether that `Product` carries the new `ownerReferences` entry.
2. **Given** a repository with zero dependents, **When** `deleteRepository` is called, **Then** it proceeds exactly as before — no new check involving `ownerReferences` is introduced into this decision path.

---

### User Story 3 - Resources created before this capability shipped are corrected on their next touch (Priority: P2)

Resources already stored in the datastore before this feature ships have an empty `metadata.ownerReferences`. Rather than requiring a dedicated, fleet-wide migration pass, the next time such a resource is legitimately touched — pushed again with an edit, or updated via its GraphQL update mutation — admission fills in the missing entry as a side effect of the update it was already going to perform.

**Why this priority**: Lower than User Stories 1-2 because it is a completeness nicety for previously-created data, not a correctness gap in newly created data.

**Independent Test**: Can be fully tested by taking a resource stored without the entry (representing pre-feature data), pushing an unrelated spec change to it, and confirming the updated record now carries the entry — without needing any migration tooling to exist.

**Acceptance Scenarios**:

1. **Given** an existing `Product` with no Repository-pointing `ownerReferences` entry, **When** its manifest is edited and re-pushed (or its update mutation is called), **Then** the updated record's `ownerReferences` gains the entry, in addition to whatever other entries (e.g. spec 052's category-pointing entry) already existed there.
2. **Given** a resource that already carries a correct entry, **When** it is updated again for an unrelated reason, **Then** the entry is left unchanged — no duplicate or conflicting entry accumulates from repeated updates.

---

### User Story 4 - File resources receive the same treatment once File admission exists (Priority: P3)

`File` is in scope per ADR-0008 and GH#376, but as of this writing no `File` admission path exists in the codebase yet (it is tracked separately). This requirement applies to `File` on the same terms as the other four kinds, to be honored by whatever admission path for `File` lands, without this spec building `File` admission itself.

**Why this priority**: Lowest — purely a forward-looking requirement with no exercisable code path today.

**Independent Test**: Not independently testable today, since no `File` admission path exists to exercise. Becomes testable the moment `File` admission lands, using the same test shape as User Story 1.

**Acceptance Scenarios**:

1. **Given** a `File` admission path exists (from whatever spec implements it), **When** a `File` resource is created, **Then** it carries the same `{kind: Repository, name, uid, blockOwnerDeletion: true}` entry as the other four kinds.

---

### Edge Cases

- What happens to the entry when a Repository is "renamed" (deleted, then a new one created reusing the old name)? Per the Clarifications above, no rewrite mechanism exists or is needed: delete already requires zero dependents, so no resource can carry a stale reference into the new repository's lifetime. Any resource later created under the new (recreated) repository receives a fresh entry pointing at the *new* UID at its own admission time, per User Story 1 — there is no continuity, stale or otherwise, between the old and new UID for any resource.
- What happens if a pushed manifest's frontmatter already contains a `metadata.ownerReferences` value, attempting to author it directly? `ownerReferences` is controller-managed, never git-authored, consistent with how `metadata.uid`, `metadata.resourceVersion`, and other system fields are already non-author-writable (ADR-0004 through ADR-0008). Admission MUST compute and write the Repository-pointing entry server-side and MUST NOT let an author-supplied value substitute for it.
- What happens to a resource's Repository-pointing entry when spec 052's category/product-direction entries are added to or removed from the same record? They coexist in the same `metadata.ownerReferences` array without conflict — each spec's write path only ever inspects or modifies the entry matching its own `kind` (`Repository` for this spec, `CategoryTaxonomy` for spec 052), leaving other entries in the array untouched.
- What happens if the containing namespace or repository is not `Active` at push time? No change from today's behavior: the push is already rejected before any resource record (and therefore any `ownerReferences` entry) is written. This spec adds a field write to an admission that has already been accepted; it introduces no new admission precondition of its own.
- What happens to a resource whose repository is deleted after the resource itself was already deleted? Not applicable — spec 041's `HasCatalogResources` check already guarantees no catalog resource can exist at the moment its repository is deleted, so no resource with a Repository-pointing entry can ever outlive the repository it points at.

## Requirements *(mandatory)*

### Functional Requirements

- **FR-001**: On Create admission — whether via a git push or a GraphQL create mutation that delegates to git — of a `Product`, `ProductVariant`, `CategoryTaxonomy`, or `Collection`, the system MUST write a `metadata.ownerReferences` entry `{kind: Repository, name, uid, blockOwnerDeletion: true}` on the created record, pointing at the resource's containing `Repository`, using the `Repository` record already resolved during the existing namespace/repository-`Active` precondition check performed for every admission — no additional datastore lookup is required or permitted solely for this write.
- **FR-002**: The same requirement as FR-001 applies to the `File` resource kind, to be satisfied by whatever admission path for `File` exists or later lands; this spec does not implement `File` admission itself, only requires that any such path also perform this write.
- **FR-003**: On Update admission of any of the five kinds in FR-001/FR-002, the system MUST also verify the Repository-pointing entry and write or correct it if it is missing or stale, so resources created before this capability existed become correct the next time they are pushed or updated, without requiring a dedicated migration mechanism.
- **FR-004**: The Repository-pointing entry MUST always use `blockOwnerDeletion: true`; this distinguishes it from spec 052's `blockOwnerDeletion: false` `Product`→`CategoryTaxonomy` entries, and both entry shapes MUST be able to coexist in the same `metadata.ownerReferences` array on the same record without conflict.
- **FR-005**: The system MUST NOT replace, duplicate, or otherwise modify the existing `HasRepositories`/`HasCatalogResources` deletion-precondition mechanism (spec 041). Repository deletion safety continues to be enforced exclusively by that existing check; this spec's writes are additive metadata only and MUST NOT be consulted as an alternative or additional gate for repository deletion.
- **FR-006**: The system MUST NOT introduce a controller-driven, rewrite-on-rename or rewrite-on-transfer mechanism for the Repository-pointing entry in this phase. This is justified because `renameRepository` and `transferRepository` are both `Unimplemented` in Phase 1 (ADR-0003), the only path to changing a Repository's identity is delete-then-recreate, and delete already requires zero dependents (spec 041) — so no dependent resource's entry can ever become stale relative to a UID that has stopped existing.
- **FR-007**: If a future Phase 2 operation introduces an in-place, live-dependent-preserving change to a Repository's identity (e.g. a `transferRepository` that does not require draining dependents first), that operation's own specification MUST define a dependent `ownerReferences` rewrite mechanism at that time, analogous to `CategoryTaxonomy`'s `ancestorPath` recomputation-on-reparent (ADR-0006, spec 039); this spec explicitly does not build or anticipate that mechanism.
- **FR-008**: `metadata.ownerReferences` on any resource in scope MUST NOT be author-writable via git frontmatter; the system MUST compute and write the Repository-pointing entry server-side on every Create and Update admission, overriding or ignoring any value present in the pushed manifest for this field, consistent with the existing non-author-writable status of other controller-managed metadata fields.
- **FR-009**: Writing or correcting the Repository-pointing entry MUST be additive to a resource's existing `metadata.ownerReferences` array — it MUST NOT remove, reorder, or overwrite any other entry already present (e.g. a spec 052 category/product-direction entry), and repeated writes for the same resource and repository MUST be idempotent (no duplicate Repository-kind entries accumulate).
- **FR-010**: A resource's own deletion behavior (per its existing kind-specific rules, ADR-0004 through ADR-0008, and spec 052 for `CategoryTaxonomy`) MUST be unaffected by carrying a Repository-pointing `ownerReferences` entry — that entry is metadata describing ownership upward toward the `Repository`, never a deletion gate on the resource carrying it.

### Production Requirements *(mandatory for core-service or load-bearing changes)*

- **PR-001 Replica Safety**: The entry's value is a pure function of the already-resolved `Repository` record and is written within the same per-request admission flow that already creates or updates the resource record; no cross-replica coordination, leader election, or distributed lock is introduced. Any replica handling a given push or mutation computes the identical entry from the same inputs.
- **PR-002 Multi-User Security**: No new authorization surface is introduced. The entry is always server-computed (FR-008), never accepted from git content or mutation input, so it cannot be forged by a pusher or caller; write access remains gated by whatever authorization already protects the underlying create/update admission path today.
- **PR-003 Capacity**: Each admitted resource gains at most one small, fixed-shape array entry (a few hundred bytes) appended to an existing JSON column; no additional per-push or per-mutation round trip is introduced, since the `Repository` record is already fetched for the existing Active-precondition check on every admission.
- **PR-004 Backpressure**: No new queue, worker, or retry path is introduced. If the `Repository` record cannot be resolved for some reason, admission MUST fail exactly as it already fails today when repository resolution fails — it MUST NOT silently proceed and admit the resource with a missing or empty entry.
- **PR-005 Recovery**: Recovery is self-healing rather than reconciliation-based (FR-003): if a resource's entry is ever found missing or stale (e.g. due to a partial failure during an earlier deploy of this capability), the next Update admission of that resource deterministically recomputes and corrects it; no persistent migration state or distributed reconciliation loop needs to be tracked across restarts.

### Key Entities

- **Repository-pointing `metadata.ownerReferences` entry**: A controller-managed metadata entry, `{kind: Repository, name, uid, blockOwnerDeletion: true}`, recorded on every git-backed catalog resource (`Product`, `ProductVariant`, `CategoryTaxonomy`, `Collection`, `File`) identifying the `Repository` record that contains it. Complementary to, and coexists in the same array with, spec 052's `CategoryTaxonomy`-internal entries (parent/child, Product/Category) — different reference direction, same field and vocabulary.
- **`Repository` record (already-resolved)**: The existing datastore record fetched during every admission's namespace/repository-`Active` precondition check. This spec reuses that already-fetched record's `metadata.name` and `metadata.uid` as the source of the entry's `name`/`uid` fields — no new lookup is added.
- **Self-healing Update pass**: The mechanism by which a resource admitted before this capability existed gets its missing entry filled in, triggered by any subsequent legitimate Update admission of that resource (git push or update mutation), rather than by a dedicated migration job.

## Success Criteria *(mandatory)*

### Measurable Outcomes

- **SC-001**: 100% of `Product`, `ProductVariant`, `CategoryTaxonomy`, and `Collection` resources created after this feature ships have exactly one `Repository`-kind `ownerReferences` entry with `blockOwnerDeletion: true` pointing at their containing repository, verifiable by reading the resource's metadata immediately after creation.
- **SC-002**: 100% of repository deletion attempts against a repository with at least one live dependent continue to be rejected, with zero observable change in mechanism, timing, or reason compared to before this feature shipped.
- **SC-003**: 100% of pre-existing resources (created before this feature shipped) that are subsequently updated at least once end up with the correct entry, with zero instances of duplicate or conflicting Repository-kind entries appearing after repeated updates.
- **SC-004**: Zero instances of an author-supplied `ownerReferences` value from a pushed manifest surviving into a stored record's `metadata.ownerReferences` — the server-computed entry always wins.
- **SC-005**: Zero additional synchronous datastore lookups per resource admission attributable to this feature — the `Repository` record already fetched for the existing Active-precondition check is reused for every write.

## Assumptions

- **"Backfill" is satisfied by Create-plus-self-healing-Update, not a dedicated migration job**: GH#376's acceptance criteria describe only admission-path writes; no per-kind reconciler exists today for three of the five in-scope kinds, so building one solely to backfill this field would materially expand scope beyond what the issue asks for. Resources that are never subsequently touched after this feature ships may retain an empty entry indefinitely; this is treated as an acceptable, explicitly-scoped limitation, not a gap this spec must close.
- **`File` is in scope on a forward-looking basis only**: this spec's `File`-related requirement (FR-002) applies to whatever admission path for `File` exists or later lands; implementing `File` admission itself is out of scope and tracked separately.
- **Repository identity is immutable for the lifetime of a given UID in Phase 1**: because `renameRepository`/`transferRepository` are both `Unimplemented`, a `Repository`'s `metadata.name` and `metadata.uid` never change while any dependent resource can reference them, which is why no on-read staleness reconciliation beyond FR-003's self-healing is required.
- **No new status condition is introduced**: this spec only adds a metadata write; it does not add, change, or repurpose any `status.conditions` entry on any resource kind.
- **Cross-namespace ownership cannot occur**: existing admission already rejects pushes that would associate a resource with a repository outside its resolved namespace, so a Repository-pointing entry can never reference a `Repository` in a different namespace than the resource carrying it.
- **Namespace→Repository ownership bookkeeping is a separate, unaddressed concern**: per GH#376's explicit scope boundary, this spec does not examine or change whether `Repository` records themselves carry a correct `ownerReferences` entry pointing at their `Namespace`.
