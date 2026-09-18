# Data Model: Product Category and Readiness Reconciliation

No new datastore table or persisted schema migration. This feature adds
fields to an existing JSON status blob and one process-local, in-memory
controller index.

## Product status conditions (existing type, new condition instances)

`catalog.ProductStatus.Conditions` (`gitstore-api/internal/catalog/status.go`)
already declares `CategoryResolved` in its condition-type enum. This feature
adds the first writer of the `True` side and defines the reason vocabulary:

| Condition | Status | Reason | Written by |
|---|---|---|---|
| `CategoryResolved` | `True` | `CategoryFound` | This feature (controller, new) |
| `CategoryResolved` | `False` | `CategoryNotFound` | This feature (controller, new) |
| `CategoryResolved` | `False` | `CategoryDeleted` | Existing (`DecoupleCategoryProducts`, spec 055) — unchanged |
| `Ready` | `True` | `ProductReady` | This feature (controller, new) |
| `Ready` | `False` | `CategoryUnresolved` | This feature (controller, new) |

Conditions are merged by type (`mergeProductConditions`, already used by the
resolver) — this feature does not replace the merge mechanism, only adds new
condition instances flowing through it via `updateProductStatus`.

## Resolved category reference (new field on an existing type)

`catalog.ResolvedProductDefinition.Category` (`ResolvedCategoryDefinition`)
already exists with `Name`/`Path`. This feature adds `UID string` (JSON tag
`uid`), populated with the resolved CategoryTaxonomy's opaque Relay-encoded
id (the same value `CategoryTaxonomy.id` returns), never the raw internal
UID (FR-015/FR-016).

| Field | Type | Populated when | Cleared when |
|---|---|---|---|
| `resolved.category.name` | `string` | `CategoryResolved=True` | `CategoryResolved=False` |
| `resolved.category.uid` | `string` (opaque Relay id) | `CategoryResolved=True` | `CategoryResolved=False` |
| `resolved.category.path` | `[]string` | Not populated by this feature (existing field, unrelated) | — |

`resolved.category` as a whole is absent (`nil`) whenever `CategoryResolved=False`,
per FR-015 — the controller does not leave a stale reference from a
previously-resolved category once resolution fails.

## CategoryTaxonomy reference match / Product category index (new, in-memory only)

A new controller-local component,
`gitstore-controller-manager/internal/categorytaxonomy.productCategoryIndex`:

| Field | Type | Meaning |
|---|---|---|
| key | `(namespace string, categoryName string)` | The `spec.categoryRef.name` a Product references, scoped to its namespace |
| value | `map[types.WorkItemKey]struct{}` | The set of Product cache keys currently referencing that (namespace, categoryName) |

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
