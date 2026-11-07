# Data Model: Resource OwnerReferences and Deletion Semantics

**Feature**: [spec.md](./spec.md) | **Research**: [research.md](./research.md)

## Resource identity and metadata

| Entity | Identity / fields | Lifecycle rules |
|---|---|---|
| Namespaced resource | Kind, namespace, name, immutable UID, resource version | UID differentiates a deleted resource from a same-name recreation. Namespace is cluster-scoped; Repository and catalog resources are namespace-scoped. |
| OwnerReference | `apiVersion`, `kind`, `name`, `uid`, `blockOwnerDeletion`; internal `repositoryID` scope where required | System-managed. `repositoryID` routes reverse-index queries and is not authored metadata. |
| ServiceAccount | Cluster-scoped name and immutable UID; common GraphQL `ObjectMeta` | Resource root with no current resource-owner edge. Its projected `ownerReferences` and `finalizers` are non-null empty lists. Authentication `metadata.owner` is a principal. |
| Reverse dependent projection | namespace, owner repository scope, owner UID, blocking flag, dependent kind/UID, name, resource version | Query-first reverse lookup for catalog owner relationships. Updates replace prior projection entries; queries are bounded and keyset-paginated. |

## Ownership relationships

The dependent stores the reference to its owner. Rows marked `true` block
owner deletion; rows marked `false` permit owner deletion and are orphaned.

| Owner | Dependent | Blocking value | Source of relationship |
|---|---|---:|---|
| Namespace | Repository | `true` | Repository's containing namespace |
| Repository | Product | `true` | Product's admitted Git provenance |
| Repository | ProductVariant | `true` | Variant's admitted Git provenance |
| Repository | CategoryTaxonomy | `true` | Category's admitted Git provenance |
| Repository | Collection | `true` | Collection's admitted Git provenance |
| Repository | File | `true` | File's admitted Git provenance |
| Product | ProductVariant | `true` | Resolved `spec.productRef` |
| CategoryTaxonomy | child CategoryTaxonomy | `true` | Resolved `spec.parentRef` |
| CategoryTaxonomy | Product | `false` | Resolved `spec.categoryRef` |
| — | Namespace | — | Root; no owner reference |
| — | ServiceAccount | — | Cluster-scoped root; no owner reference |

A catalog dependent may carry multiple references simultaneously: its
Repository owner plus its resolved Product or CategoryTaxonomy owner. Updates
to one relationship must preserve the others. A reference is current only
when owner UID and namespace/repository scope match the resolved owner.

## Normal write lifecycle

- Create admission adds the applicable system-owned references.
- A legitimate update repairs stale or missing references on that resource
  and refreshes the reverse projection through the normal datastore write.
- Updates preserve unrelated owner references and remove only obsolete
  system-managed relationships.
- Resources that are not created or updated may retain incomplete historical
  metadata. This feature does not assert that every existing relationship has
  been enumerated or repaired.

## Deletion state and transitions

```text
active --request with live blocking dependent--> active (rejected / unchanged)
active --eligible request--> existing kind-specific deletion flow
terminating --blocking dependents remain--> terminating
terminating --no blockers and own finalization complete--> absent
owner removed --non-blocking dependent exists--> dependent survives orphaned
```

- `blockOwnerDeletion: true`: owner removal is prevented until the dependent
  is removed or its relationship is changed. Product and CategoryTaxonomy
  retain their timestamp/finalizer/CAS completion patterns. Namespace and
  Repository retain their existing synchronous safety preconditions.
- `blockOwnerDeletion: false`: owner removal may proceed. The dependent
  remains stored, its owner reference is cleared, and applicable resolved
  status is updated. Its authored reference (for example `spec.categoryRef`)
  is not silently rewritten.
- Neither state cascades deletion to dependents.
- ServiceAccount deletion remains immediate because the ownership matrix
  defines no ServiceAccount dependents.

## Persistence and concurrency

- Preserve the existing authoritative resource tables and
  `owner_reference_dependents` projection; no owner-reference migration is
  required.
- Authoritative resource updates use existing resource-version compare-and-set
  operations. Concurrent API replicas share datastore projections.
- Projection read/write failures are surfaced. This feature does not use a
  migration-completion marker to imply full historical coverage.
- Repository and Namespace deletion retains the current existence checks.
  Owner-reference tests must establish the same blocking outcome for the
  Repository-owned kinds, including File.
