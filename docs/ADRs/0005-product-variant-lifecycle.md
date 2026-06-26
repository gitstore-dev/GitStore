# ADR 0005: ProductVariant Lifecycle

**Status**: Proposed

**Date**: 2026-06-26

**Audience**: GitStore API, controller, admission, and catalog authors.

## Context

`ProductVariant` is the purchasable SKU. It is the only resource downstream
commerce flows — cart, checkout, inventory, pricing — must target. `Product`
is the non-sellable catalog parent that groups variants; it must never be used
as the purchasable unit.

This variant-first contract is documented in GH#143 and is a Phase 1 exit
criterion. This ADR formalises the lifecycle decisions so the implementation
can enforce that contract.

`ProductVariant` frontmatter (GH#83) is already closed. This ADR governs the
remaining lifecycle decisions not covered by the spec doc.

## Decision

`ProductVariant` is **Git-backed**. Desired state is a Markdown file with YAML
frontmatter pushed to a repository. Hydrated read model and status live in the
datastore.

### Storage classification

| Layer           | Owner                                    |
|-----------------|------------------------------------------|
| Desired state   | Git frontmatter (Markdown file in repo)  |
| Hydrated record | Datastore (ScyllaDB/memDB)               |
| Status          | Datastore; controller-managed            |
| Finalizers      | Datastore; controller-managed            |

Git-authored fields: `apiVersion`, `kind`, `metadata.*` (non-system), `spec.*`,
Markdown body.

Controller-managed fields (not author-writable): `metadata.uid`,
`metadata.resourceVersion`, `metadata.generation`, `metadata.creationTimestamp`,
`metadata.revision`, `metadata.ownerReferences`, `status.*`.

### Lifecycle rules

#### Create

**Git push path (canonical):**

1. Author creates `products/variants/<name>.md` (or places the file alongside the
   product manifest at the author's discretion — path is provenance only).
2. Pre-receive validates: envelope, `kind: ProductVariant`, `spec.productRef.name`
   required, `spec.sku` format, `spec.selectedOptions` consistency.
3. Post-receive admission:
   - Namespace and repository are `Active`.
   - `ownerReferences` written pointing at the repository.
   - If the referenced `spec.productRef.name` exists in the datastore or in the same
     push, `ProductResolved=True` and a secondary `ownerReference` pointing at the
     Product record is written.
   - If the referenced product does not yet exist, `ProductResolved=False` with reason
     `ProductNotFound` — the variant is stored and the controller resolves the reference
     asynchronously. This allows variants and products to be pushed in a single commit.
   - `AdmissionAccepted=True`.

**GraphQL mutation path:**

`createProductVariant` commits `products/variants/<name>.md` to the named repository
(or `gitstore-system`) and delegates to git admission. The mutation does not write
directly to the datastore.

#### Update

1. Author edits the variant file and pushes, or issues `updateProductVariant` mutation.
2. `updateProductVariant` commits an updated manifest to the same path.
3. Admission re-validates the full spec; updates the datastore record.
4. Immutable fields in Phase 1: `metadata.name`, `metadata.namespace`,
   `spec.productRef.name` (changing the parent product is a delete/recreate).
5. `spec.sku` is immutable after the first successful admission (changing an SKU
   identifier after it has been used in orders is a data integrity risk; not enforced
   in Phase 1 admission but flagged as a validation warning).

#### Delete

1. Author deletes the variant file and pushes, or issues `deleteProductVariant`
   mutation.
2. Admission checks whether any open `Reservation` or `Allocation` runtime records
   reference this variant. If so, the delete is **rejected** with
   `FailedPrecondition: active reservations present`. (Phase 1: this check is a
   best-effort warning, not a blocking rejection, because inventory runtime is not
   yet implemented. The rejection will be enforced when inventory is implemented in
   Phase 2.)
3. The API adds the `gitstore.dev/foreground-deletion` finalizer.
4. Controller removes the variant from any Collection membership projections (selector
   re-evaluation) and then removes the finalizer.
5. Once finalizers are cleared, the datastore record is hard-deleted.

#### Variant-first contract enforcement

The admission pipeline **must** reject any attempt to use a `Product` identity in a
cart line, checkout, order line, or inventory record. That enforcement belongs to the
runtime resource validation for those datastore-only resources (Phase 3). The
admission pipeline's role in Phase 1 is to ensure every `ProductVariant` has a valid
`spec.productRef` and that `ProductResolved` is a tracked condition.

### Co-creation semantics (push product and variants together)

When a product and its variants are pushed in the same commit:

1. The product is admitted first (alphabetical within push batch; or order determined
   by the admission pipeline's batch ordering — to be specified in GH#123).
2. If the product has been stored by the time variants are admitted, variant admission
   sets `ProductResolved=True` immediately.
3. If the product admission hasn't been processed yet, variant admission stores the
   variant with `ProductResolved=False` and enqueues a controller reconciliation job.
4. The controller resolves the reference on the next reconcile pass
   (level-triggered — no event ordering dependency).

### File location convention

When a repository name is not specified in a GraphQL mutation, variants are placed at:

```
products/variants/<metadata.name>.md
```

This is a sub-convention of the `products/` directory. Variant files may also be
placed alongside their parent product file by the author in a manual push; the path
is provenance only.

### Git write path

```markdown
---
apiVersion: catalog.gitstore.dev/v1beta1
kind: ProductVariant
metadata:
  name: macbook-pro-15-silver
  namespace: acme-store
  labels:
    gitstore.dev/sku: MBP15-SLV
spec:
  title: MacBook Pro 15 — Silver
  sku: MBP15-SLV
  productRef:
    kind: Product
    name: macbook-pro-15
  selectedOptions:
  - name: color
    value: silver
  pricing:
    priceListRef:
      kind: PriceList
      name: eu-retail
  inventory:
    tracked: true
    policy: deny_out_of_stock
  media:
  - fileRef:
      kind: File
      name: macbook-pro-15-silver-hero
---

Silver MacBook Pro 15 variant.
```

### GraphQL mutation delegation

| Mutation                  | Phase 1 behaviour                                                                                                      |
|---------------------------|------------------------------------------------------------------------------------------------------------------------|
| `createProductVariant`    | Commits `products/variants/<name>.md` to the named repository (or `gitstore-system`); waits for admission.            |
| `updateProductVariant`    | Commits updated manifest to the same path; waits for admission.                                                       |
| `deleteProductVariant`    | Checks for active reservations (Phase 1: warning); adds `foregroundDeletion` finalizer; sets `Terminating`.           |
| `getProductVariant`       | Read-only datastore query.                                                                                             |
| `listProductVariants`     | Read-only datastore query, namespace-scoped; filterable by `spec.productRef.name`.                                    |

There is no direct-datastore write path for `ProductVariant` in Phase 1.

### Validation and admission rules

| Phase        | Rule                                                                                                                               |
|--------------|------------------------------------------------------------------------------------------------------------------------------------|
| Pre-receive  | Envelope valid; `kind: ProductVariant`; `spec.productRef.name` required; `spec.sku` non-empty if present; `spec.selectedOptions[*].name` must match an option defined in the referenced product (deferred to controller in Phase 1 if co-created). |
| Admission    | Namespace and repository `Active`; no cross-namespace `productRef`; co-creation allowed (product may not exist yet in datastore at admission time). |
| Controller   | `spec.productRef` resolution (async); `spec.media[*].fileRef` resolution (async, deferred to Phase 2); `selectedOptions` compatibility check against parent product options (async). |

Cross-namespace `spec.productRef` is **rejected at admission time** in Phase 1.

### Status and reconciliation behaviour

| Condition           | Meaning                                                                                 |
|---------------------|-----------------------------------------------------------------------------------------|
| `AdmissionAccepted` | Variant stored in datastore.                                                            |
| `ProductResolved`   | `spec.productRef` was found; secondary `ownerReference` written.                       |
| `OptionsCompatible` | Selected options are compatible with the parent product's declared option set.         |
| `MediaResolved`     | All `spec.media[*].fileRef` entries found (deferred to Phase 2).                       |
| `Ready`             | Variant is queryable, product is resolved, options are compatible.                      |
| `Terminating`       | `foregroundDeletion` finalizer present; reservations must drain (Phase 2 enforcement). |

When `ProductResolved=False` and the parent product does not exist after a configurable
retry window, the controller sets a `Stale` condition with reason `ProductMissing`. The
variant is not deleted automatically — the author must fix the manifest.

When the parent product enters `Terminating`, all child variants have `ProductResolved`
set to `False` with reason `ProductTerminating`.

## Consequences

Positive:
- Variant-first contract is enforced structurally: `spec.productRef` is required.
- Co-creation semantics allow natural single-commit authoring.
- All writes flow through git admission; no split source of truth.

Negative:
- `spec.productRef` immutability after first admission means changing a variant's
  parent product requires delete/recreate — higher friction for catalog refactoring.
- `OptionsCompatible` check is deferred to controller-time in Phase 1 when the product
  and variants are co-created; there is a window where stored variants have
  incompatible options.

## Cross-references

- [ADR-0002](0002-namespace-lifecycle.md) — Namespace must be `Active`.
- [ADR-0003](0003-repository-lifecycle.md) — Repository must be `Active`;
  `ownerReferences` points at repository.
- [ADR-0004](0004-product-lifecycle.md) — Product is the required parent; deleting a
  product requires all variants to be gone first.
- [ADR-0006](0006-category-taxonomy-lifecycle.md) — Variants may reference category
  via parent product's `categoryRef`; this ADR does not add a direct category ref.
- [ADR-0008](0008-file-lifecycle.md) — `spec.media[*].fileRef` references resolved
  asynchronously (Phase 2).

## Dependency graph position

```
Namespace (ADR-0002)
  └─► Repository (ADR-0003)
        └─► Product (ADR-0004)
              └─► ProductVariant (this ADR)
                    ├── spec.productRef → Product (ADR-0004)      [required, async resolve]
                    └── spec.media[*].fileRef → File (ADR-0008)   [async, Phase 2]
```

## Alternatives considered

### Allow `spec.productRef` to be optional (variant without a product)

Rejected. The variant-first contract (GH#143) requires every variant to have exactly
one parent product. An orphan variant would have no home in the catalog hierarchy and
would break collection selector semantics that traverse the product–variant tree.

### Make `spec.productRef` mutable (allow reparenting)

Rejected for Phase 1. Reparenting a variant to a different product changes the option
compatibility contract, collection memberships, and potentially catalog namespace. The
safe path is delete/recreate. This decision can be revisited in Phase 2 with an
explicit reparent mutation that validates compatibility.
