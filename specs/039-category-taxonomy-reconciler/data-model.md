# Data Model: CategoryTaxonomy Controller Reconciliation

No new datastore tables, columns, or GraphQL schema changes. This spec adds Go types internal to `gitstore-controller-manager` (a reconciler-local cache entity and two new client packages) and writes into fields already defined by spec 040's contract (`status.resolved.path`, `.depth`, `.childCount`, `.productCount`, and the `ParentResolved`/`Acyclic`/`Ready`/required-file-reference conditions already modeled by `catalog.Condition`).

## Controller-Manager-Side Types (new)

### CategoryTaxonomy (cache entity)

The type held in the reconciler's `cache.Cache[CategoryTaxonomy]`, populated by the `Runner[CategoryTaxonomy]`'s list-then-watch loop against `watchCategories`/`categories`.

| Field | Type | Notes |
|---|---|---|
| `UID` | `string` | |
| `Namespace` | `string` | |
| `Name` | `string` | |
| `Generation` | `int64` | |
| `ResourceVersion` | `string` | |
| `ParentRefName` | `string` | Empty = no parent (root candidate). Mirrors `spec.parentRef.name`. |
| `Status` | `status.ResourceStatus` | Existing type from spec 026/040 — carries `ObservedGeneration`, `LastAppliedRevision`, `Conditions`, `Resolved` (JSON bytes) as last observed, for `IsNoOp` comparison |

### ResolvedCategoryTaxonomy (JSON payload the reconciler marshals into `StatusPatch.Resolved`)

Mirrors `gitstore-api/internal/catalog.ResolvedCategoryTaxonomy` field-for-field (spec 040 R9's renamed shape) so the JSON round-trips identically on both sides:

| Field | Type | Notes |
|---|---|---|
| `Depth` | `int8` | Root = 0 |
| `Path` | `[]string` | Root-to-self order; single-element for a root category |
| `ChildCount` | `int64` | Direct children only |
| `ProductCount` | `int64` | Products whose `spec.categoryRef.name` names this category |

### CategoryTaxonomyListWatcher (new, `internal/listwatch`)

Satisfies the existing `listwatch.ListWatcher[CategoryTaxonomy]`/`Watcher[CategoryTaxonomy]` interfaces (spec 036) — see contracts/reconciler-contract.md.

### graphqlStatusClient (new, `internal/status`)

Satisfies the existing `status.StatusClient` interface (spec 026, extended by spec 040 R8) — see contracts/reconciler-contract.md.

## Relationships

- `CategoryTaxonomy.ParentRefName` is the adjacency pointer the reconciler walks (via its own `Cache[T]`) to compute `Path`/`Depth` — this is a controller-manager-local read of the same relationship `gitstore-api` materializes independently (and differently) via its own `ancestor_path` datastore column; the two computations are not required to be synchronized moment-to-moment (level-triggered, eventually consistent), only to converge to the same answer once both sides have processed the same admitted state.
- `ResolvedCategoryTaxonomy` (controller-manager-local Go struct) round-trips through JSON into `catalog.ResolvedCategoryTaxonomy` (gitstore-api-side Go struct) via `StatusPatch.Resolved` → `UpdateCategoryStatusInput.resolved: ResolvedCategoryTaxonomyInput` (spec 040's existing wire contract) — no new wire shape.
- A cycle (detected via R3's reimplemented DFS over the reconciler's own `Cache[T]` snapshot) suppresses `Path`/`Depth` recomputation (FR-008) but does not suppress `ParentResolved`/`Acyclic`/`Ready` condition updates — the conditions still transition normally even while the hierarchy fields are frozen.

## Validation Rules (from Functional Requirements)

- `Path`/`Depth` are recomputed and written only when the resource is not currently a cycle participant (FR-008).
- A status write is skipped entirely when the computed `StatusPatch` (via the existing `IsNoOp`, extended in spec 040 to compare `Resolved` byte-for-byte) matches the currently observed status — including `Path` array-equality, not set-equality (FR-013).
- `ChildCount`/`ProductCount` are always written as `0`, never omitted, when a category has no children/products (Edge Cases).
- The required-file-reference condition is `Unknown` (not `True`/`False`) for every `optional: false` media entry, given `File` (#79) is not yet queryable (research.md R5, spec Assumptions).
