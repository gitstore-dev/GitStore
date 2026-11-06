# Data Model: Resource OwnerReferences and Deletion Semantics

**Feature**: [spec.md](./spec.md) | **Research**: [research.md](./research.md)

## Resource identity and metadata

| Entity | Identity / fields | Lifecycle rules |
|---|---|---|
| Namespaced resource | Kind, namespace, name, immutable UID, resource version | UID differentiates a deleted resource from a same-name recreation. Namespace is cluster-scoped; Repository and catalog resources are namespace-scoped. |
| OwnerReference | `apiVersion`, `kind`, `name`, `uid`, `blockOwnerDeletion`; internal `repositoryID` scope where required | System-managed. The GraphQL `OwnerReference` shape is already shared; `repositoryID` is internal to reverse-index routing, not authored metadata. |
| ServiceAccount | Cluster-scoped name and immutable UID; common GraphQL `ObjectMeta` | Resource root with no current resource-owner edge. Its projected `ownerReferences` and `finalizers` are non-null empty lists. Authentication `metadata.owner` is a principal, not an owner reference. |
| Reverse dependent projection | namespace, owner repository scope, owner UID, blocking flag, dependent kind/UID, name, resource version | Query-first reverse lookup for catalog owner relationships. Updates replace the dependent's prior projection entries; dependent queries are bounded and keyset-paginated. |
| Backfill progress | job identity, resource kind, last stable cursor, state, row counts, update time, fencing/lease state | Durable per-kind progress. Completion is recorded only after all kinds are processed and verification succeeds. Re-runs are idempotent. |

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
to one relationship must preserve the others. A reference is current only when
its owner UID and namespace/repository scope match the resolved owner.

## Deletion state and transitions

```text
active --request with live blocking dependent--> active (rejected / unchanged)
active --eligible request--> existing kind-specific deletion flow
terminating --blocking dependents remain--> terminating
terminating --no blockers and own finalization complete--> absent
owner removed --non-blocking dependent exists--> dependent survives orphaned
```

- `blockOwnerDeletion: true`: owner removal is prevented until the dependent is
  removed or its relationship is changed. Product and CategoryTaxonomy keep
  their current timestamp/finalizer/CAS completion patterns. Namespace and
  Repository keep their existing synchronous safety preconditions.
- `blockOwnerDeletion: false`: owner removal may proceed. The dependent remains
  stored, its owner reference is cleared, and any resolved-owner status is
  updated. Its authored reference (for example `spec.categoryRef`) is not
  silently rewritten.
- Neither state cascades deletion to dependents.
- ServiceAccount deletion remains immediate because the ownership matrix
  defines no ServiceAccount dependents.

## Backfill state transitions

```text
not-started -> running(kind, cursor) -> verifying -> complete
                  |                         |
                  +---- interrupted --------+
                            -> running(kind, cursor)
```

- The operator command processes a bounded page of one resource kind, writes
  corrected metadata with UID/resource-version preconditions, and advances a
  durable cursor only after the page is safely applied.
- Normal datastore writes synchronize the reverse projection; retries can
  safely reapply a page.
- If an owner cannot be resolved, the backfill records the condition and does
  not invent a reference. Verification must distinguish invalid legacy data
  from complete coverage; unresolved blockers prevent marking backfill
  complete until reconciled or explicitly excluded by a recorded policy.
- New API writers dual-write complete ownership while backfill runs. The
  operator starts the backfill only after old API writers have drained.
- Indexed deletion behavior is activated only after verification sets the
  durable completion marker. Before completion, existing deletion checks stay
  enabled; an unavailable or incomplete index is never interpreted as an empty
  dependent set.

## Persistence and concurrency

- Preserve the existing authoritative resource tables and
  `owner_reference_dependents` projection. Do not edit checksummed migration
  files; add an incremental Scylla migration for the backfill progress and
  completion state.
- The memdb backend implements the same progress and projection contracts for
  deterministic tests; production resumption is backed by shared Scylla state.
- Authoritative resource updates use existing resource-version compare-and-set
  operations. Backfill progress uses a durable lease/fencing token so multiple
  operators or retries cannot skip work.
- Projection errors are surfaced. Deletion fails closed when the owner
  projection is unreadable or cannot be proven complete.
- Repository and Namespace deletion retains the current existence checks. The
  reference metadata and tests must still establish the same blocking outcome
  for all Repository-owned kinds, including File.
