# Feature Specification: Namespace/Repository Deletion Ordering and System Repository Bootstrap

**Feature Branch**: `041-namespace-repo-finalizers`  
**Created**: 2026-08-10  
**Status**: Closed  
**Input**: User description: "Implement foreground-finalizer deletion ordering and gitstore-system auto-provisioning for the Namespace/Repository control plane, closing GH#165 and GH#173 (Phase 1 exit criteria)."

## User Scenarios & Testing *(mandatory)*

### User Story 1 - Deleting a namespace that still has repositories is rejected (Priority: P1)

An operator or store owner attempts to delete a namespace. The namespace still has one or more repositories registered under it. The system must refuse the deletion and explain why, rather than silently deleting the namespace and leaving its repositories orphaned or silently destroying them.

**Why this priority**: This is the most severe gap today: namespace deletion currently succeeds unconditionally regardless of what it owns, which can silently strand or orphan an owner's repositories and everything under them. This is the highest-blast-radius operation in the control plane and must be the first thing made safe.

**Independent Test**: Can be fully tested by creating a namespace, creating a repository under it, then attempting to delete the namespace — the deletion must be rejected with a clear reason, and both the namespace and the repository must remain intact and queryable afterward.

**Acceptance Scenarios**:

1. **Given** a namespace with at least one repository, **When** an operator attempts to delete the namespace, **Then** the deletion is rejected, the namespace remains, and the repository remains unaffected.
2. **Given** a namespace with zero repositories (other than any system-managed repository still required by the platform), **When** an operator attempts to delete the namespace, **Then** the deletion succeeds.
3. **Given** a namespace that was just successfully deleted, **When** the operator queries for that namespace afterward, **Then** it is reported as not found.

---

### User Story 2 - Deleting a repository that still has catalog content is rejected (Priority: P1)

A store owner attempts to delete a repository. That repository still contains catalog resources (products, product variants, categories, or collections) that were pushed and admitted into it. The system must refuse the deletion rather than destroying the repository's git storage and stranding or silently discarding the catalog data that depended on it.

**Why this priority**: Equal in severity to User Story 1 — this is the other half of the same safety gap. Today, repository deletion destroys the underlying storage with no check for what still depends on it, which is an irreversible data-loss risk for any catalog content that has not been explicitly removed first.

**Independent Test**: Can be fully tested by creating a repository, pushing and admitting at least one catalog resource into it, then attempting to delete the repository — the deletion must be rejected, and the repository, its storage, and the catalog resource must all remain intact.

**Acceptance Scenarios**:

1. **Given** a repository containing at least one admitted catalog resource (product, product variant, category, or collection), **When** a store owner attempts to delete the repository, **Then** the deletion is rejected and the repository remains with its storage and catalog resources intact.
2. **Given** a repository containing zero catalog resources, **When** a store owner attempts to delete the repository, **Then** the deletion succeeds and the repository's storage is removed.
3. **Given** a repository that had catalog resources but all of them have since been removed (e.g., via git push deleting the corresponding files), **When** a store owner attempts to delete the repository, **Then** the deletion succeeds.

---

### User Story 3 - Every new namespace starts with its system repository in place (Priority: P2)

A user creates a new namespace. Immediately after creation, the namespace must already have its well-known system repository available, so that catalog mutations issued without specifying a target repository have somewhere to land without a separate manual setup step.

**Why this priority**: This removes a manual, easy-to-forget setup step and closes a documented behavior gap, but it does not carry the same data-loss risk as User Stories 1 and 2, so it is ordered after the two safety-critical deletion behaviors.

**Independent Test**: Can be fully tested by creating a new namespace and immediately querying for its well-known system repository — it must already exist without any additional setup action.

**Acceptance Scenarios**:

1. **Given** a brand-new namespace has just been created, **When** its repositories are listed, **Then** the well-known system repository is present.
2. **Given** a namespace whose system repository already exists (e.g., a retry of a partially-completed creation), **When** the namespace creation is attempted again, **Then** the system does not end up with two conflicting system repositories for that namespace.
3. **Given** a namespace was created before this capability existed and therefore lacks a system repository, **When** any operation depends on that repository being present, **Then** this specification's scope does not silently paper over the gap — see Assumptions.

---

### Edge Cases

- What happens when a namespace has repositories, but every one of those repositories is itself empty of catalog resources? (Namespace deletion is still rejected — repository *existence*, not repository *contents*, is what blocks namespace deletion. The owner must delete the repositories first.)
- What happens when two deletion requests for the same repository race each other, one after the last catalog resource was just removed? (Only one must succeed; the other must receive a clear "not found" or "already deleted" outcome, never a partial or corrupted deletion.)
- What happens if repository storage removal fails partway through a repository deletion that had already passed the catalog-resource check? (The repository and its metadata must remain intact and queryable — a failed deletion must not leave a partially-deleted, inconsistent repository record.)
- What happens when someone attempts to delete the well-known system repository itself while it still holds catalog resources? (Same rule as any other repository: rejected while catalog resources remain.)
- What happens when namespace creation is retried after a prior attempt partially succeeded (namespace row created, system repository provisioning failed)? (Retrying creation must reach a consistent end state without duplicating the namespace or leaving two system repositories.)

## Requirements *(mandatory)*

### Functional Requirements

- **FR-001**: The system MUST reject a namespace deletion request when that namespace has one or more repositories, and MUST leave the namespace and all its repositories unchanged when rejecting.
- **FR-002**: The system MUST report a clear, specific reason when rejecting a namespace deletion due to existing repositories, distinguishing this outcome from other failure reasons (e.g., "namespace not found").
- **FR-003**: The system MUST allow a namespace deletion to succeed once it has zero repositories.
- **FR-004**: The system MUST reject a repository deletion request when that repository has one or more admitted catalog resources (products, product variants, categories, or collections), and MUST leave the repository, its storage, and its catalog resources unchanged when rejecting.
- **FR-005**: The system MUST report a clear, specific reason when rejecting a repository deletion due to existing catalog resources, distinguishing this outcome from other failure reasons.
- **FR-006**: The system MUST allow a repository deletion to succeed once it has zero catalog resources.
- **FR-007**: The system MUST provision the well-known system repository for a namespace as part of that namespace's creation, without requiring a separate manual step.
- **FR-008**: The system MUST NOT create a duplicate or conflicting system repository if namespace creation is retried after the system repository already exists for that namespace.
- **FR-009**: The system MUST ensure that repository deletion, once it has passed the catalog-resource check, either fully completes (storage and metadata both removed) or fully preserves the prior state (nothing removed) — no partially-deleted repository record may result from a failure during deletion.
- **FR-010**: The deletion-ordering rule MUST apply uniformly to every repository, including the well-known system repository, with no special-case exemption.
- **FR-011**: The system MUST ensure that concurrent deletion attempts against the same namespace or repository resolve deterministically — at most one deletion attempt succeeds, and any other concurrent attempt receives a definitive, non-ambiguous outcome (e.g., not found or already deleted).

### Key Entities

- **Namespace**: A tenant or account boundary that owns one or more repositories. Deletable only when it owns zero repositories.
- **Repository**: A git repository declaration owned by exactly one namespace, storing catalog resources. Deletable only when it contains zero admitted catalog resources. Exactly one repository per namespace is the well-known "system" repository, used as the default destination for catalog mutations that don't specify a repository.
- **Catalog resource**: A product, product variant, category, or collection admitted into a repository. Its presence blocks deletion of the repository that holds it.

## Success Criteria *(mandatory)*

### Measurable Outcomes

- **SC-001**: 100% of namespace deletion attempts against a namespace that still owns at least one repository are rejected, with zero data loss to the namespace or its repositories.
- **SC-002**: 100% of repository deletion attempts against a repository that still holds at least one catalog resource are rejected, with zero data loss to the repository or its catalog resources.
- **SC-003**: 100% of newly created namespaces have their well-known system repository available immediately upon creation, with no additional manual action required.
- **SC-004**: Zero instances of duplicate or conflicting system repositories occur across repeated or retried namespace creation attempts.
- **SC-005**: Zero instances of a partially-deleted repository (storage removed but metadata remains, or vice versa) occur under normal operation or transient failure during deletion.

## Assumptions

- Cross-namespace reference rejection for catalog resource fields (`categoryRef`, `parentRef`, `fileRef`, `productRef`) is tracked separately and is **out of scope** for this specification, even though it is part of the same underlying issue (GH#173) referenced in the original request. This spec covers only the Namespace/Repository deletion-ordering and system-repository-bootstrap behaviors; cross-namespace reference validation for catalog resource fields was verified as unimplemented anywhere in the current codebase and is large enough in scope to warrant its own specification.
- "Catalog resources" for the purpose of blocking repository deletion means Product, ProductVariant, CategoryTaxonomy, and Collection records admitted against that repository. The File resource is explicitly out of scope for this spec (tracked separately) and is not included in the repository-deletion precondition check, since File does not yet exist as a queryable resource.
- Namespaces created before this capability exists and that lack a system repository are not retroactively backfilled by this specification; only namespace creation going forward is guaranteed to provision the system repository. Backfilling pre-existing namespaces, if needed, is a separate, explicit operational concern and not part of this spec's scope.
- Declarative `.spec`/`.status` schema definitions for Namespace and Repository (tracked in GH#170 and GH#249) are a separate specification; this spec only adds enforcement behavior to the existing create/delete operations and does not introduce or change either resource's declarative schema.
- A Namespace or Repository watch/reconcile loop (tracked in GH#174) is out of scope; the behaviors in this spec are enforced synchronously at the time of the create/delete request, not asynchronously by a controller.
