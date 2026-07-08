# ADR 0004: Product Lifecycle

**Status**: Proposed

**Date**: 2026-06-26

**Audience**: GitStore API, controller, admission, and catalog authors.

## Context

`Product` is the non-sellable catalog parent. It groups `ProductVariant` records,
declares shared copy, classification, options, and media references. `Product` itself
is never the purchasable unit — that role belongs exclusively to `ProductVariant`.
See [ADR-0005](0005-product-variant-lifecycle.md) for the variant-first sellable
contract.

`Product` frontmatter (GH#77) is still an open issue in Phase 1. This ADR formalises
the lifecycle decisions so the implementation can close that issue with a stable
contract.

## Decision

`Product` is **Git-backed**. The desired state is a Markdown file with YAML
frontmatter pushed to a repository. The hydrated read model and status live in the
datastore.

### Storage classification

| Layer           | Owner                                     |
|-----------------|-------------------------------------------|
| Desired state   | Git frontmatter (Markdown file in repo)   |
| Hydrated record | Datastore (ScyllaDB/memDB)                |
| Status          | Datastore; controller-managed             |
| Finalizers      | Datastore; controller-managed             |

Git-authored fields: `apiVersion`, `kind`, `metadata.name`, `metadata.namespace`,
`metadata.labels`, `metadata.annotations`, `spec.*`, Markdown body.

Controller-managed fields (not author-writable): `metadata.uid`,
`metadata.resourceVersion`, `metadata.generation`, `metadata.creationTimestamp`,
`metadata.revision`, `metadata.ownerReferences`, `status.*`.

### Lifecycle rules

#### Create

**Git push path (canonical):**

1. Author creates `products/<name>.md` in a repository and pushes.
2. Pre-receive validates: envelope, `kind: Product`, `metadata.name` format,
   `spec.title` length, `spec.media[*].fileRef` fields present.
3. Post-receive admission: namespace and repository are `Active`; resource is stored
   in the datastore; `AdmissionAccepted=True`; `ownerReferences` written pointing at
   the repository record.
4. Controller reconciles: resolves `spec.categoryRef` (sets `CategoryResolved=True` or
   `False`), resolves `spec.media[*].fileRef` entries (sets `MediaResolved` condition),
   and then sets `Ready=True`.

**GraphQL mutation path:**

When a caller issues `createProduct` without specifying a repository name, the API
uses the `gitstore-system` repository within the mutation's namespace and commits the
product manifest to `products/<name>.md` using convention-based directory placement.
When the caller specifies a repository name, the file is committed into that
repository at `products/<name>.md`. In both cases the mutation delegates to git — the
API never writes the product record directly to the datastore.

The mutation returns after admission is confirmed (or after a configurable timeout with
a pending status). It does not wait for controller reconciliation of `categoryRef` or
`fileRef`.

#### Update

1. Author edits the product file and pushes, or issues `updateProduct` mutation.
2. `updateProduct` mutation: commits an updated manifest to the same repository path.
3. Admission re-validates the full spec; updates the datastore record;
   increments `resourceVersion` and `generation`.
4. Controller re-runs reference resolution if `spec.categoryRef` or `spec.media`
   changed.

Immutable fields in Phase 1: `metadata.name`, `metadata.namespace`.

Rename semantics: a rename is not an in-place update. Changing `metadata.name` is
treated as a delete of the old identity and a create of the new identity with a new
UID. The old variants and collection memberships are not automatically migrated.

#### Delete

1. Author deletes the product file and pushes, or issues `deleteProduct` mutation.
2. Before any record is removed, admission checks whether `ProductVariant` records
   exist whose `spec.productRef.name` points at this product.
   - If variants exist, the delete is **rejected** with
     `FailedPrecondition: product variants present`.
3. If no variants exist, the API adds the `gitstore.dev/foreground-deletion` finalizer and sets `metadata.deletionTimestamp`
   then marks the record `Terminating`.
4. Controller drains any remaining references (Collection selector results, media
   fileRef backlinks if tracked) and then removes the finalizer.
5. Once all finalizers are cleared, the datastore record is hard-deleted.

Callers that want to delete a product and all its variants must delete variants first,
confirm they are gone, and then delete the product.

### File location convention

When a repository name is not specified in a GraphQL mutation, products are placed at:

```
products/<metadata.name>.md
```

This convention is enforced by the API and must not be overridden by the caller in
Phase 1. File location is provenance, not identity — moving a file to a different
path in a manual push preserves the UID as long as `metadata.name` is unchanged.

### Git write path

```markdown
---
apiVersion: catalog.gitstore.dev/v1beta1
kind: Product
metadata:
  name: macbook-pro-15
  namespace: acme-store
  labels:
    gitstore.dev/brand: apple
    gitstore.dev/category: laptops
spec:
  title: MacBook Pro 15
  categoryRef:
    kind: CategoryTaxonomy
    name: laptops
  tags: [laptop, apple]
  options:
  - name: color
    values: [silver, space-black]
  media:
  - fileRef:
      kind: File
      name: macbook-pro-hero
---

Long-form product description in Markdown.
```

### GraphQL mutation delegation

| Mutation        | Phase 1 behaviour                                                                                                                           |
|-----------------|---------------------------------------------------------------------------------------------------------------------------------------------|
| `createProduct` | Commits `products/<name>.md` to the named repository (or `gitstore-system`); waits for admission; returns hydrated record.                  |
| `updateProduct` | Commits updated manifest to the same path; waits for admission.                                                                             |
| `deleteProduct` | Validates no variants exist; adds `foregroundDeletion` finalizer; sets `Terminating`.                                                       |
| `getProduct`    | Read-only datastore query.                                                                                                                  |
| `listProducts`  | Read-only datastore query, namespace-scoped.                                                                                                |

There is no direct-datastore write path for `Product` in Phase 1. All mutations
delegate to git.

### Validation and admission rules

| Phase       | Rule                                                                                                                                                                                              |
|-------------|---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------|
| Pre-receive | Envelope valid; `kind: Product`; `metadata.name` DNS format; `spec.title` ≤ 200 chars; `spec.media[*].fileRef.name` present when `media` is non-empty; `spec.options[*].name` unique within list. |
| Admission   | Namespace and repository `Active`; `metadata.namespace` matches inferred namespace; no cross-namespace refs in Phase 1.                                                                           |
| Controller  | `spec.categoryRef` resolution (async); `spec.media[*].fileRef` resolution (async, deferred to Phase 2 per GH#244).                                                                                |

Cross-namespace references in `spec.categoryRef` or `spec.media[*].fileRef` are
**rejected at admission time** in Phase 1. All referenced resources must be in the
same namespace as the product.

### Status and reconciliation behaviour

| Condition           | Meaning                                                                          |
|---------------------|----------------------------------------------------------------------------------|
| `AdmissionAccepted` | Product stored in datastore after push.                                          |
| `CategoryResolved`  | `spec.categoryRef` was found in the datastore.                                   |
| `MediaResolved`     | All `spec.media[*].fileRef` entries were found (deferred to Phase 2 per GH#244). |
| `Ready`             | Product is queryable and all required references are resolved.                   |
| `Terminating`       | `foregroundDeletion` finalizer present; variants must be deleted first.          |

`CategoryResolved=False` with reason `CategoryNotFound` is a non-blocking condition in
Phase 1 (push is accepted, controller retries). `CategoryResolved=False` with reason
`CrossNamespaceRef` is terminal — the reference must be corrected in git.

When a Product enters `Terminating`, its existing `ProductVariant` records become
stale. The controller sets `ProductResolved=False` on all affected variants with reason
`ProductTerminating`, which signals downstream flows that the variants are no longer
usable.

## Consequences

Positive:
- Product lifecycle is fully reviewable through git history.
- GraphQL mutations delegate to git — no separate write path.
- Deletion is safe: variants must drain before the product disappears.
- Convention-based directory placement reduces friction for API clients.

Negative:
- API-driven creates require admission to complete before the product is queryable;
  callers that need low-latency confirmation must poll or subscribe.
- Rename is not atomic; teams that rename products must handle the double-write window.

## Cross-references

- [ADR-0002](0002-namespace-lifecycle.md) — Namespace must be `Active`.
- [ADR-0003](0003-repository-lifecycle.md) — Repository must be `Active`; product
  carries `ownerReferences` pointing at the repository.
- [ADR-0005](0005-product-variant-lifecycle.md) — ProductVariant is the sellable unit;
  it references this product; deleting this product requires all variants to be gone.
- [ADR-0006](0006-category-taxonomy-lifecycle.md) — `spec.categoryRef` is resolved
  asynchronously by the controller.
- [ADR-0007](0007-collection-lifecycle.md) — Collections select products by label;
  label changes on this product trigger Collection re-evaluation.
- [ADR-0008](0008-file-lifecycle.md) — `spec.media[*].fileRef` references resolved
  asynchronously (Phase 2).

## Dependency graph position

```
Namespace (ADR-0002)
  └─► Repository (ADR-0003)
        └─► Product (this ADR)
              ├── spec.categoryRef → CategoryTaxonomy (ADR-0006)  [async]
              ├── spec.media[*].fileRef → File (ADR-0008)         [async, Phase 2]
              └─► ProductVariant (ADR-0005)  [ownerRef on variant]
```

## Alternatives considered

### Allow direct datastore writes for `createProduct` mutation

Rejected. Bypassing git would create a split source of truth: some products in git
history, others not. Downstream watch/reconcile semantics depend on git being the
single authoring source. The delegation-to-git model adds admission latency but
preserves the architecture invariant.

### Place products at the git root with no subdirectory convention

Rejected. Directory convention (`products/`) provides a predictable namespace for API
clients and prevents collisions between different resource kinds in a shared repository.
The path is provenance only (identity is `metadata.name`), so the convention adds no
semantic weight.
