# ADR 0007: Collection Lifecycle

**Status**: Proposed

**Date**: 2026-06-26

**Audience**: GitStore API, controller, admission, and catalog authors.

## Context

`Collection` is a selector-driven product grouping for merchandising. A collection's
membership is determined by a `LabelSelector` applied to `Product` labels, not by
explicit membership lists. The controller continuously recomputes membership as product
labels change.

`Collection` frontmatter (GH#84) is already closed. This ADR governs the lifecycle
gaps: mutation delegation, snapshot semantics, and controller reconciliation semantics.

## Decision

`Collection` is **Git-backed**. Desired state is a Markdown file with YAML
frontmatter pushed to a repository. Status conditions and the cached membership
count are controller-managed fields in the datastore.

### Storage classification

| Layer                 | Owner                                   |
|-----------------------|-----------------------------------------|
| Desired state         | Git frontmatter (Markdown file in repo) |
| Hydrated record       | Datastore (ScyllaDB/memDB)              |
| Status                | Datastore; controller-managed           |

Git-authored fields: `apiVersion`, `kind`, `metadata.*` (non-system), `spec.title`,
`spec.selector`, `spec.targetRef`, `spec.media`, Markdown body.

Controller-managed fields (not author-writable): `metadata.uid`,
`metadata.resourceVersion`, `metadata.generation`, `metadata.ownerReferences`,
`status.*`, `status.resolved.memberCount`.

### Selector-based membership model

Collection membership is defined by `spec.selector` (a `LabelSelector`) applied to
`Product` records in the same namespace. Membership is never stored as a static list
in git or status — the live set is evaluated when `collection.products` is queried,
while the controller maintains only the cached `memberCount` summary.

`spec.targetRef` optionally constrains the selector to products that reference a
specific resource (e.g., a specific `CategoryTaxonomy`).

When `spec.selector` is absent or empty, the collection has zero members.

There is no mutation to explicitly add or remove a product from a collection. Authors
add products to a collection by setting the matching labels on the product. This is
intentional: it keeps collection membership decentralised and avoids a single-writer
bottleneck.

### Snapshot semantics

`collection.products` snapshots membership at the first page request. The resolver
evaluates the collection selector against the current products in the namespace, then
materialises an ordered membership set for that traversal. Subsequent pages reuse the
same snapshot rather than re-running selector evaluation.

The snapshot is query-owned, not controller-owned:

- it is not committed to git;
- it is not persisted as a `memberRefs` field in status;
- it may be represented by opaque cursor state or a transient in-process cache;
- a fresh query, or a fresh cursor, re-evaluates against current product state.

The controller still re-evaluates the selector on collection and product changes so it
can refresh `memberCount` and status conditions, but it does not materialise a stored
membership list.

### Lifecycle rules

#### Create

**Git push path (canonical):**

1. Author creates `collections/<name>.md` in a repository and pushes.
2. Pre-receive validates: envelope, `kind: Collection`, `spec.title` required,
   `spec.targetRef.kind` must be `"Product"` if present, media `fileRef.name` present
   when `media` is non-empty.
3. Post-receive admission:
   - Namespace and repository `Active`.
   - `ownerReferences` written pointing at repository.
   - `AdmissionAccepted=True`.
   - Controller is enqueued to evaluate `spec.selector` against current products.
4. Controller evaluates selector, writes `memberCount`, and sets
   `MembersResolved=True` (or `False` with reason `SelectorEvaluationFailed`).

**GraphQL mutation path:**

`createCollection` commits `collections/<name>.md` to the named repository (or
`gitstore-system`) and delegates to git admission. No direct datastore write.

#### Update

1. Author edits the collection file and pushes, or issues `updateCollection`.
2. `updateCollection` commits an updated manifest; waits for admission.
3. If `spec.selector` changes, controller re-evaluates membership after admission.
4. If `spec.media` changes, controller re-resolves media references (Phase 2).
5. Immutable fields in Phase 1: `metadata.name`, `metadata.namespace`.

#### Delete

1. Author deletes the collection file and pushes, or issues `deleteCollection`.
2. Collection deletion has no blocking dependents: no other resource owns or references
   a collection by identity in Phase 1.
3. The delete is admitted and the datastore record is removed as part of normal
   cleanup; there is no stored membership projection to drain.
4. Any cached status for the deleted collection is discarded.

Collections are deliberately lightweight dependents: they depend on Products but
Products do not depend on Collections. Deleting a collection does not affect any
product.

### Membership recomputation triggers

The controller re-evaluates a collection's selector when:

- The collection's `spec.selector` or `spec.targetRef` changes (collection update).
- A `Product` is admitted, updated, or deleted in the same namespace.
- A `Product`'s labels change (label edit triggers `resourceVersion` increment, which
  the controller watches).

Recomputation is level-triggered: the controller computes the current membership from
the current state of all matching products, not by replaying a sequence of events.
Query-time snapshotting is separate: each `collection.products` traversal snapshots once
and then paginates over that frozen set.

### File location convention

When a repository name is not specified in a GraphQL mutation:

```
collections/<metadata.name>.md
```

### Git write path

```markdown
---
apiVersion: catalog.gitstore.dev/v1beta1
kind: Collection
metadata:
  name: summer-laptops
  namespace: acme-store
spec:
  title: Summer Laptops
  selector:
    matchLabels:
      gitstore.dev/brand: apple
      gitstore.dev/category: laptops
  media:
  - fileRef:
      kind: File
      name: summer-laptops-hero
---

Summer laptop collection description.
```

### GraphQL mutation delegation

| Mutation           | Phase 1 behaviour                                                                                    |
|--------------------|------------------------------------------------------------------------------------------------------|
| `createCollection` | Commits `collections/<name>.md` to the named repository (or `gitstore-system`); waits for admission. |
| `updateCollection` | Commits updated manifest; waits for admission.                                                       |
| `deleteCollection` | Commits the deletion; waits for admission cleanup; hard-deletes the record.                          |
| `getCollection`    | Read-only datastore query; includes controller-computed `memberCount`.                               |
| `listCollections`  | Read-only datastore query, namespace-scoped.                                                         |

There are no mutations to explicitly add or remove products from a collection. Product
membership is managed by editing product labels.

### Validation and admission rules

| Phase       | Rule                                                                                                                                                             |
|-------------|------------------------------------------------------------------------------------------------------------------------------------------------------------------|
| Pre-receive | Envelope valid; `kind: Collection`; `spec.title` required; `spec.targetRef.kind` must be `"Product"` if present; `media[*].fileRef.name` present when non-empty. |
| Admission   | Namespace and repository `Active`; no cross-namespace selector targets in Phase 1 (the selector applies within the namespace only).                              |
| Controller  | Selector evaluation; `memberCount` and status conditions update; `spec.media[*].fileRef` resolution (deferred to Phase 2).                                       |
| Query       | `collection.products` snapshots current selector matches on first page request and reuses that snapshot for subsequent pages in the traversal.                   |

### Status and reconciliation behaviour

| Condition           | Meaning                                                                        |
|---------------------|--------------------------------------------------------------------------------|
| `AdmissionAccepted` | Collection stored in datastore.                                                |
| `MembersResolved`   | Selector has been evaluated; `memberCount` is current.                         |
| `MediaResolved`     | All `spec.media[*].fileRef` entries found (deferred to Phase 2).               |
| `Ready`             | Selector evaluated and `memberCount` current.                                  |

`MembersResolved=False` with reason `SelectorEvaluationFailed` indicates a malformed
selector expression that could not be compiled. This is a terminal error that requires
a corrected manifest to be pushed. It is distinct from an empty result (a valid selector
that matches zero products is `MembersResolved=True` with `memberCount=0`).

### Edge cases

#### Product is deleted while in a collection

The controller detects the deletion (product `resourceVersion` change / deletion event)
and re-evaluates the collection's selector. The next reconciliation updates
`memberCount`; any new `collection.products` traversal excludes the deleted product.
An in-flight traversal that already snapshotted the product set continues to use its
frozen snapshot. No error condition is set on the collection — a collection with zero
members is valid.

#### Collection selector is updated to match different products

Controller re-evaluates the selector from scratch against all current products in the
namespace. New `collection.products` traversals observe the updated selector; any
already-started traversal continues to page through its original snapshot. There is no
partial update window within a single traversal.

#### Product label is changed so it no longer matches a collection

Same as above: the controller detects the product update and re-evaluates all
collections whose selectors might be affected. This is potentially a wide fan-out if
many collections reference the same label key. The controller must scope the re-evaluation
to collections in the same namespace. In-flight `collection.products` traversals are
stable until they complete.

## Consequences

Positive:
- Collection membership is always derived from current product state; no stale explicit
  lists to manage.
- Collection reads are stable within a single `collection.products` traversal.
- Authors manage membership by editing product labels — one consistent place for
  product metadata.

Negative:
- The first page of `collection.products` must materialise a snapshot, which can be
  more expensive than a plain live page read.
- Query-time snapshot state adds transient memory pressure when traversals are large.
- Wide label changes can trigger expensive multi-collection re-evaluation if many
  collections share label keys.

## Cross-references

- [ADR-0002](0002-namespace-lifecycle.md) — Namespace must be `Active`.
- [ADR-0003](0003-repository-lifecycle.md) — Repository must be `Active`.
- [ADR-0004](0004-product-lifecycle.md) — Products are the membership targets; product
  label changes trigger collection re-evaluation.
- [ADR-0005](0005-product-variant-lifecycle.md) — Variants are not directly collection
  members in Phase 1; membership is at the Product level.
- [ADR-0008](0008-file-lifecycle.md) — `spec.media[*].fileRef` resolved
  asynchronously (Phase 2).

## Dependency graph position

```
Namespace (ADR-0002)
  └─► Repository (ADR-0003)
        └─► Collection (this ADR)
              ├── spec.selector → Product labels (ADR-0004)   [evaluated at query/reconcile time]
              └── spec.media[*].fileRef → File (ADR-0008)     [async, Phase 2]
```

**No circular dependency risk:** Collection references products via selector, not via
a hard ref stored on the product. Products carry no back-reference to collections.
The dependency is one-way: Collection → Product (selector evaluation plus query-time
snapshotting).

## Alternatives considered

### Explicit membership list in git (author adds/removes products by name)

Rejected. An explicit list requires a write to the collection manifest every time a
product is added or removed, creating serialization contention in a team workflow where
multiple authors push new products concurrently. The selector model is decentralised
and scales naturally.

### Store `memberRefs` in git or status as a generated file

Rejected. `memberRefs` makes the membership set author-writable, conflicts with
controller updates, and creates noisy commit history or status churn every time product
labels change. It also duplicates the query-time snapshot that `collection.products`
already needs for stable pagination.
