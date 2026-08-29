# Data Model: CategoryTaxonomy Deletion Semantics

**Feature**: [spec.md](./spec.md) | **Research**: [research.md](./research.md)

## OwnerReference

| Field | Ownership | Rules |
|---|---|---|
| `apiVersion`, `kind`, `name`, `uid` | Controller/API managed | Identifies the resolved owner. |
| `blockOwnerDeletion` | Controller/API managed | `true` only for child CategoryTaxonomy → parent; `false` for Product → category. |

A CategoryTaxonomy has zero or one parent-category reference; a Product has zero or one category reference. Resolution replaces the managed entry and unresolved references remove it.

## Reverse owner-reference projection

| Field | Purpose |
|---|---|
| `namespace_id`, `repository_id` | Isolation and physical query partition |
| `owner_uid` | Lookup key |
| `dependent_uid`, `dependent_kind` | Stable dependent identity |
| `block_owner_deletion` | Blocking versus non-blocking policy |
| `resource_version` | Detect/reconcile stale projection updates |

Operations are `HasBlockingDependents(ownerUID)` with `LIMIT 1`, and
`ListNonBlockingProductDependents(ownerUID, cursor, limit)` with stable keyset pagination.

## CategoryTaxonomy lifecycle

| State | Marker | Controller behavior |
|---|---|---|
| Active | No deletion timestamp/finalizer | Resolve hierarchy and parent owner reference. |
| Terminating | Deletion timestamp + `gitstore.dev/foreground-deletion` + `Terminating=True` | Recheck children and decouple Products in bounded pages. |
| Completed | Finalizer removed; record deleted | Requires zero blocking dependents and matching resource version. |

Repeated marks are no-ops. Completion conflicts, missing records, and duplicate Product decoupling are idempotent.

## Product resolution

| Event | Owner reference | `CategoryResolved` |
|---|---|---|
| Category resolves | Add non-blocking category reference | `True` |
| Category normally absent | Remove reference | `False`, `CategoryNotFound` |
| Category terminating/deleted | Remove reference | `False`, `CategoryDeleted` |

The controller never changes Git-authored `spec.categoryRef`.

## Invariants

- Blocking owner-reference lookup rejects deletion; Product references never block.
- Admission rejects Product create/update targeting a terminating category.
- Final removal repeats the blocking lookup; no child is orphaned.
- Projection and source metadata remain scope-bound and controller managed.
