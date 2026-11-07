# Contract: Owner References and Resource Deletion

**Feature**: [spec.md](../spec.md) | **Data model**: [data-model.md](../data-model.md)

## Resource metadata

`ObjectMeta.ownerReferences` is a system-managed array of `OwnerReference`
values with `apiVersion`, `kind`, `name`, `uid`, and required
`blockOwnerDeletion`. Git-authored metadata and GraphQL mutation input cannot
supply or alter these values.

The contract covers Namespace, Repository, Product, ProductVariant,
CategoryTaxonomy, Collection, File, and ServiceAccount. Namespace and
ServiceAccount are roots and expose an empty array when they have no owner.
ServiceAccount's principal `metadata.owner` is not a resource owner reference.

## Ownership matrix

| Dependent kind | Owner kind | `blockOwnerDeletion` |
|---|---|---:|
| Repository | Namespace | `true` |
| Product | Repository | `true` |
| ProductVariant | Repository | `true` |
| CategoryTaxonomy | Repository | `true` |
| Collection | Repository | `true` |
| File | Repository | `true` |
| ProductVariant | Product | `true` |
| CategoryTaxonomy | CategoryTaxonomy (parent) | `true` |
| Product | CategoryTaxonomy (category) | `false` |

Where a dependent has both Repository and domain ownership, both entries
coexist and are independently maintained. Repository-scoped reverse projection
keys are internal storage metadata and are not part of the public
OwnerReference value.

## Create and update

- Successful admission derives and writes the managed references from the
  current resource and resolved owners.
- A legitimate update repairs missing, stale, or duplicate managed references
  on the resource being updated. It preserves unrelated managed references.
- The normal datastore write refreshes the existing reverse dependent
  projection.
- This contract does not require a separate fleet-wide backfill. Resources
  not created or updated may retain incomplete historical metadata; this
  feature makes no full-coverage claim for them.

## Delete outcomes

| Dependent relationship to requested owner | Required outcome |
|---|---|
| One or more live `true` references | Reject deletion or retain the owner in its existing terminating/finalizer flow. Do not remove the owner while a blocker is present in the maintained projection. |
| Only live `false` references | Permit owner removal when its own deletion preconditions are met. Preserve each dependent, clear its stale owner reference, and report unresolved desired references through existing status where applicable. |
| No dependents | Preserve the resource kind's existing deletion flow. |

No delete request cascades to a dependent. Foreground/background refer to
dependent propagation only: blocking versus orphaning. They do not determine
whether the owner's own removal is synchronous or asynchronous.

Compatibility rules:

- Namespace deletion retains its existing check that no Repository remains.
- Repository deletion retains its existing content precondition and covers
  Product, ProductVariant, CategoryTaxonomy, Collection, and File.
- Product and CategoryTaxonomy retain their blocking-dependent checks and
  timestamp/finalizer/CAS completion.
- CategoryTaxonomy deletion may orphan Products with non-blocking category
  references. The Product remains stored; `spec.categoryRef` is not rewritten,
  the owner reference is cleared, and resolution status is updated.
- ServiceAccount deletion remains immediate while ServiceAccount has no
  resource dependents in the ownership matrix.

## Admission and concurrency

- Create, update, reparent, and delete operations are idempotent with respect
  to the maintained owner references and reverse projection.
- A new or reparented dependent cannot commit a blocking reference to an owner
  whose deletion has crossed its final removal boundary.
- A blocker check uses the current owner UID and appropriate scope. Same-name
  recreation is a distinct owner lifetime.
- Projection read/write failures are explicit errors and are never treated as
  an empty result.
- Concurrent API replicas use existing optimistic concurrency and shared
  datastore projections. Controller retries are idempotent and safe across
  process replacement.
