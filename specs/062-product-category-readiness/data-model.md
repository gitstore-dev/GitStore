# Data Model: Product Category and Readiness Reconciliation

No new datastore table or persisted schema migration. This feature adds
fields to an existing JSON status blob and one process-local, in-memory
controller index.

## Product status conditions (existing type, new condition instances)

`catalog.ProductStatus.Conditions` (`gitstore-api/internal/catalog/status.go`)
already declares `CategoryResolved` in its condition-type enum. This feature
adds the first writer of the `True` side and defines the reason vocabulary:

| Condition          | Status  | Reason               | Written by                                                             |
|--------------------|---------|----------------------|------------------------------------------------------------------------|
| `CategoryResolved` | `True`  | `CategoryFound`      | This feature (controller, new)                                         |
| `CategoryResolved` | `False` | `CategoryNotFound`   | This feature (controller, new)                                         |
| `CategoryResolved` | `False` | `CategoryDeleted`    | Existing (`DecoupleCategoryProducts`, spec 055) — unchanged, transient |
| `Ready`            | `True`  | `ProductReady`       | This feature (controller, new)                                         |
| `Ready`            | `False` | `CategoryUnresolved` | This feature (controller, new)                                         |

Conditions are merged by type (`mergeProductConditions`, already used by the
resolver) — this feature does not replace the merge mechanism, only adds new
condition instances flowing through it via `updateProductStatus`.

`CategoryDeleted` is only ever a transient value, never the converged state.
spec.md's FR-010 and its US3 acceptance scenario both require the converged
reason to be `CategoryNotFound`, not `CategoryDeleted`: `DecoupleCategoryProducts`
runs synchronously while the CategoryTaxonomy is still `Terminating` (before
`CompleteCategoryDeletion` removes it), writes `CategoryDeleted`, and publishes
a Product-updated event (`category.resolvers.go`'s `UpdateCategoryStatus`
resolver). That event re-enqueues the Product, whose next reconcile re-runs
the uniform resolution algorithm below and overwrites `CategoryDeleted` with
`CategoryNotFound` — the reason string this feature actually owns as steady
state. This requires the algorithm to treat a `Terminating` CategoryTaxonomy
as not-found (see "Reconciler resolution algorithm" below); without that, the
still-cached-but-terminating category would resolve as found and flip the
Product back to `CategoryResolved=True`, undoing the decouple write until
`CompleteCategoryDeletion` finally removes the category from the controller's
cache.

## Resolved category reference (new field on an existing type)

`catalog.ResolvedProductDefinition.Category` (`ResolvedCategoryDefinition`)
already exists with `Name`/`Path`. This feature adds `UID string` (JSON tag
`uid`), populated with the resolved CategoryTaxonomy's opaque Relay-encoded
id (the same value `CategoryTaxonomy.id` returns), never the raw internal
UID (FR-015/FR-016).

| Field                    | Type                       | Populated when                                            | Cleared when             |
|--------------------------|----------------------------|-----------------------------------------------------------|--------------------------|
| `resolved.category.name` | `string`                   | `CategoryResolved=True`                                   | `CategoryResolved=False` |
| `resolved.category.uid`  | `string` (opaque Relay id) | `CategoryResolved=True`                                   | `CategoryResolved=False` |
| `resolved.category.path` | `[]string`                 | Not populated by this feature (existing field, unrelated) | —                        |

`resolved.category` as a whole is absent (`nil`) whenever `CategoryResolved=False`,
per FR-015 — the controller does not leave a stale reference from a
previously-resolved category once resolution fails.

## CategoryTaxonomy reference match / Product category index (new, in-memory only)

A new controller-local component,
`gitstore-controller-manager/internal/categorytaxonomy.productCategoryIndex`:

| Field | Type                                      | Meaning                                                                            |
|-------|-------------------------------------------|------------------------------------------------------------------------------------|
| key   | `(namespace string, categoryName string)` | The `spec.categoryRef.name` a Product references, scoped to its namespace          |
| value | `map[types.WorkItemKey]struct{}`          | The set of Product cache keys currently referencing that (namespace, categoryName) |

Maintained by the existing Product-cache `OnAdd`/`OnUpdate`/`OnDelete`
handlers already wired in `registerProductWatch` (extended, not replaced,
to also update this index alongside the existing spec-042 count fan-out).
Consumed by a new CategoryTaxonomy-cache event handler: on `OnAdd` or an
`OnUpdate` where the category's name changed, look up the index for the
(namespace, new-name) — and, for a rename, also (namespace, old-name) — and
enqueue every Product key found. Rebuilt from scratch on controller
start/restart as the Product list-then-watch cache replays (no separate
persistence; replica-safe by construction since each replica derives it from
its own durable-watch-backed cache, per R3).

## Reconciler resolution algorithm (uniform — no admission fast path)

On every reconcile of an active (non-`Terminating`) Product, the resolution
step is the same regardless of *why* the reconciler was enqueued (initial
admission, spec update, or a CategoryTaxonomy-driven re-enqueue per R3):

1. Look up `CategoryRefName` (empty if the Product has no `categoryRef`) in
   the controller's own CategoryTaxonomy cache, scoped to the Product's
   namespace.
2. Found, and the found entry has no `DeletionTimestamp` and no
   foreground-deletion finalizer → `CategoryResolved=True`/`CategoryFound`,
   `resolved.category = {name, uid: found.UID}` (already the Relay-encoded
   id — see the note above the schema; the controller never encodes it
   itself).
3. Not found — including "no `categoryRef` at all" (a Product with no
   category can never be `CategoryResolved=True`) — **and** including a
   found entry that is `Terminating` (`DeletionTimestamp` set or the
   foreground-deletion finalizer present) → `CategoryResolved=False`/
   `CategoryNotFound`, `resolved.category = null`. Treating a `Terminating`
   category as not-found is what makes `CategoryDeleted` (above) converge to
   `CategoryNotFound` without ever flapping back through `True`: the
   Product-updated event `DecoupleCategoryProducts` publishes re-enqueues
   this Product while its category is still `Terminating` and still present
   in the controller's cache, so step 2's "found" check alone would
   incorrectly re-resolve it as `True` until `CompleteCategoryDeletion`
   finally removes the category from cache.
4. `updateProductStatus` is called with that condition + `resolved.category`
   every time step 1-3's outcome differs from the cached `ResourceStatus`
   (`StatusPatch.IsNoOp`, R5) — which, per R7, also synchronizes the owner
   reference to match.

This deliberately does **not** special-case "admission already resolved this
at push time" as a distinct fast path: the controller does not need to read
or trust `Product.OwnerReferences` at all to decide `CategoryResolved`. It
independently re-derives the same answer admission would have computed
(against its own CategoryTaxonomy cache, which it already needs for R3's
watch-driven convergence). This is simpler than branching on pre-existing
owner-reference state, avoids extending the lightweight
`categorytaxonomy.Product` cache entity with owner-reference data it does not
otherwise need, and is self-correcting: if admission's synchronous
resolution and the controller's cache-based resolution ever disagreed (e.g. a
race at push time), the controller's next reconcile converges to a single
answer either way.

## State transitions (Product, as observed by this feature's reconciler)

```text
AdmissionAccepted=True, CategoryResolved=<unset>
  --controller observes, categoryRef resolves--> CategoryResolved=True, Ready=True
  --controller observes, categoryRef does not resolve--> CategoryResolved=False (CategoryNotFound), Ready=False
                                                            --matching category later created/renamed in--> CategoryResolved=True, Ready=True
CategoryResolved=True, Ready=True
  --referenced category deleted--> CategoryResolved=False (CategoryNotFound), Ready=False
  --spec.categoryRef changed to a different existing category--> CategoryResolved=True, Ready=True (re-resolved against new ref)
  --deletionTimestamp + foreground-deletion finalizer set--> reconciler stops writing CategoryResolved/Ready (deletion path owns status from here, unchanged by this feature)
```
