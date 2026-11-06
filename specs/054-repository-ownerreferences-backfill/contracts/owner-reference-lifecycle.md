# Contract: Owner References and Resource Deletion

**Feature**: [spec.md](../spec.md) | **Data model**: [data-model.md](../data-model.md)

## Resource metadata

The shared GraphQL `ObjectMeta.ownerReferences` field exposes an array of
`OwnerReference` values with `apiVersion`, `kind`, `name`, `uid`, and required
`blockOwnerDeletion`. The existing shape is retained. The value is
system-managed and cannot be supplied or altered through Git-authored metadata
or GraphQL mutation input.

All current resource kinds are covered: Namespace, Repository, Product,
ProductVariant, CategoryTaxonomy, Collection, File, and ServiceAccount.
Namespace and ServiceAccount are roots in the ownership matrix and expose an
empty array when they have no owner. ServiceAccount's principal `metadata.owner`
is not a resource owner reference.

The shared schema documentation for `OwnerReference` must describe the
blocking/orphaning contract below; it must not imply that references trigger
cascading deletion.

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

## Delete outcomes

| Dependent relationship to requested owner | Required outcome |
|---|---|
| One or more live `true` references | Reject deletion or retain the owner in its existing terminating/finalizer flow. Do not remove the owner while a blocker exists. |
| Only live `false` references | Permit owner removal when its own deletion preconditions are met. Preserve each dependent, clear its stale owner reference, and report unresolved desired references through existing status where applicable. |
| No dependents | Preserve the resource kind's existing deletion flow. |

No delete request cascades to a dependent. Foreground/background refer to
dependent propagation only: blocking versus orphaning. They do not determine
whether the owner's own removal is synchronous or asynchronous.

Specific compatibility rules:

- Namespace deletion retains its existing check that no Repository remains.
- Repository deletion retains its existing content precondition and must cover
  Product, ProductVariant, CategoryTaxonomy, Collection, and File.
- Product and CategoryTaxonomy retain their blocking-dependent checks and
  timestamp/finalizer/CAS completion; asynchronous finalization is not
  background propagation.
- CategoryTaxonomy deletion may orphan Products with non-blocking category
  references. The Product remains stored; `spec.categoryRef` is not rewritten,
  the owner reference is cleared, and resolution status is updated.
- ServiceAccount deletion remains immediate while ServiceAccount has no
  resource dependents in the ownership matrix.

## Admission and concurrency

- Every successful create, update, reparent, delete, and backfill operation
  leaves one reference per current relationship and removes obsolete
  system-managed references without modifying unrelated relationships.
- A new or reparented dependent cannot commit a blocking reference to an owner
  whose deletion has crossed its final removal boundary.
- A blocker check must be against the current owner UID and appropriate scope.
  Same-name recreation is a distinct owner lifetime.
- Projection read/write failures and incomplete backfill state are explicit
  errors. A failed or unsupported reverse lookup must never be treated as
  “no dependents.”
- Concurrent API replicas use existing optimistic concurrency and shared
  datastore projections. Controller retries and backfill retries are
  idempotent and safe across process replacement.

## Backfill and rollout

1. Deploy API writers that maintain the complete ownership matrix while
   retaining existing deletion guards.
2. Drain old API writers so every subsequent writer preserves the new
   metadata.
3. Run the bounded, resumable `gitctl` backfill against an explicitly selected
   deployment. It repairs resource metadata and lets normal writes refresh the
   reverse projection.
4. Verify all kinds and projections, then persist the completion marker.
   Indexed enforcement cannot be enabled before this step.
5. On interruption, resume from the durable cursor. Do not delete progress,
   migrated metadata, or projection rows during rollback. If verification
   fails, retain existing deletion guards and surface the failure.
