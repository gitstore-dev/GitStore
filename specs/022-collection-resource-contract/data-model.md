# Data Model: Collection Resource Contract with Label Selectors

**Branch**: `022-collection-resource-contract` | **Phase**: 1 | **Date**: 2026-06-07

---

## Entities

### `Collection` (Datastore Entity)

Replaces the legacy flat `Collection` struct. Follows the `CategoryTaxonomy` Kubernetes-style envelope pattern.

| Field | Type | Description |
|-------|------|-------------|
| `UID` | `string` (UUID) | System-generated unique identifier |
| `Namespace` | `string` | Namespace identifier (human-readable slug) |
| `Name` | `string` | DNS-label name, unique within namespace |
| `APIVersion` | `string` | Always `catalog.gitstore.dev/v1beta1` |
| `Kind` | `string` | Always `Collection` |
| `Generation` | `int64` | Increments on every spec change |
| `ResourceVersion` | `string` | Opaque concurrency token |
| `CreationTimestamp` | `time.Time` | Set once on first admission |
| `Revision` | `string` | Git ref + SHA, e.g. `main@sha1:abc123` |
| `Labels` | `map[string]string` | Author-supplied key-value labels |
| `Annotations` | `map[string]string` | Author-supplied annotations |
| `GitCommitSHA` | `string` | Commit SHA of the admitting push |
| `GitRef` | `string` | Branch/ref of the admitting push |
| `Spec` | `json.RawMessage` | JSON-encoded `CollectionSpec` |
| `Body` | `string` | Markdown content (collection description) |
| `Status` | `json.RawMessage` | JSON-encoded `CollectionStatus` |

**Identity**: `(Namespace, Name)` is the unique business key. `UID` is the stable system key.

**State transitions**:
- Created on first push admission → `Status.Conditions[Ready] = False` until selector evaluated.
- Updated on subsequent pushes → `Generation` incremented; `ResourceVersion` updated.
- Deleted via git delete (out of scope for this spec).

---

### `CollectionSpec` (Catalog Parse Type — Go)

Parsed from YAML frontmatter during `ParseResource`. Stored as JSON in the `Spec` column.

| Field | Type | Validation |
|-------|------|-----------|
| `Title` | `string` | Required, non-empty |
| `Selector` | `*LabelSelector` | Optional; absent = zero members |
| `Media` | `[]MediaDefinition` | Optional; reuses existing `MediaDefinition` type |

---

### `LabelSelector` (Catalog Parse Type — Go)

| Field | Type | Validation |
|-------|------|-----------|
| `MatchLabels` | `map[string]string` | Optional; all entries must match (AND) |
| `MatchExpressions` | `[]LabelSelectorRequirement` | Optional; all entries must match (AND) |

**Evaluation semantics**:
- Empty `LabelSelector` (both fields nil/empty) → zero members.
- `matchLabels` entries AND `matchExpressions` entries are combined with logical AND.
- A product is a member if and only if it satisfies all constraints.

---

### `LabelSelectorRequirement` (Catalog Parse Type — Go)

| Field | Type | Validation |
|-------|------|-----------|
| `Key` | `string` | Required |
| `Operator` | `string` | Required; one of `In`, `NotIn`, `Exists`, `DoesNotExist` |
| `Values` | `[]string` | Required non-empty for `In`/`NotIn`; must be empty for `Exists`/`DoesNotExist` |

---

### `ResolvedCollectionDefinition` (Status Sub-type — Go)

Stored as part of `CollectionStatus` JSON. Written at admission time.

| Field | Type | Description |
|-------|------|-------------|
| `MemberCount` | `int64` | Count of products satisfying the selector at last reconciliation |
| `Media` | `[]ResolvedFileDefinition` | Resolved media entries (reuses existing type) |

**Note**: `productRefs` is intentionally absent. Products are queried live via `collection.products(...)`.

---

### `CollectionStatus` (Status Envelope — Go)

| Field | Type | Description |
|-------|------|-------------|
| `ObservedGeneration` | `int64` | Generation at which status was last computed |
| `LastAppliedRevision` | `string` | Git revision of last successful admission |
| `Conditions` | `[]Condition` | `SelectorAccepted`, `MembersResolved`, `Ready` |
| `Resolved` | `*ResolvedCollectionDefinition` | Computed membership count and media |

**Condition types**:

| Type | True when | False when |
|------|-----------|-----------|
| `SelectorAccepted` | Selector is syntactically valid | Selector has invalid operator or constraint violation |
| `MembersResolved` | Member count successfully computed | Datastore error during evaluation |
| `Ready` | Both above conditions True | Either above condition False |

---

## Datastore Interface

New methods added to the `Datastore` interface in `gitstore-api/internal/datastore/datastore.go`:

```
CreateCollection(ctx, *Collection) error
GetCollection(ctx, uid string) (*Collection, error)
GetCollectionByName(ctx, namespace, name string) (*Collection, error)
ListCollections(ctx, namespace string, page PageParams) (*PageResult[Collection], error)
UpdateCollection(ctx, *Collection) error
```

**`ListProductsByLabelSelector`** — new method for evaluating label selectors at query time:

```
ListProductsByLabelSelector(ctx, namespace string, selector LabelSelector) ([]*Product, error)
```

Returns all products in the namespace whose `Labels` satisfy the given `LabelSelector`. Used both at admission (to compute `memberCount`) and at `collection.products` query time (for paginated results).

---

## ScyllaDB Schema (Migration `003`)

Three tables following the `category_taxonomy` three-table pattern:

### `collection` (primary listing table)

```sql
PRIMARY KEY ((namespace), creation_timestamp DESC, uid DESC)
```

Columns: all fields from `Collection` entity above. `spec` and `status` stored as `text` (JSON).

### `collection_by_name` (lookup by namespace + name)

```sql
PRIMARY KEY ((namespace), name)
```

Columns: `namespace`, `name`, `uid`, `creation_timestamp`. Enables O(1) `GetCollectionByName`.

### `collection_by_uid` (lookup by UID)

```sql
PRIMARY KEY (uid)
```

Columns: `uid`, `namespace`, `creation_timestamp`. Enables O(1) `GetCollectionByUID`.

---

## Memdb Schema (Development)

Table `collection` with indexes:

| Index name | Fields | Unique |
|------------|--------|--------|
| `id` | `UID` | Yes |
| `name_namespace` | `Name`, `Namespace` | Yes |
| `namespace` | `Namespace` | No |

---

## `ParsedResource` Extension

`gitstore-api/internal/validate/validator.go`:

```go
type ParsedResource struct {
    Kind             string
    Product          *catalog.ProductResource
    CategoryTaxonomy *catalog.CategoryTaxonomyResource
    Collection       *catalog.CollectionResource       // NEW
}
```

`case "Collection":` added to `ParseResource` switch. Validates `CollectionSpec` using `validateCollectionSpec` (title required, selector operator/values constraints, media reuse).

---

## Label Selector Evaluation

Implemented in `gitstore-api/internal/catalog/selector.go` (new file):

```
// MatchesLabels reports whether the given labels satisfy the selector.
// An empty selector matches nothing (returns false).
func MatchesLabels(selector LabelSelector, labels map[string]string) bool
```

Called from:
1. `admitCollection` in `cataloggrpc/server.go` — to compute `memberCount` at admission.
2. `ListProductsByLabelSelector` in datastore implementations — to filter products at query time.
3. Unit tests — directly testable without datastore.

---

## Relationships

```
Namespace
  └── Collection (namespace-scoped)
        └── products(...) ──► Product[] (live label-selector query, not stored)
```

- A `Collection` belongs to exactly one `Namespace`.
- A `Product` may be a member of zero or more `Collection`s simultaneously.
- Deleting a `Collection` has no effect on any `Product`.
- `collection.products` is a live query; results reflect current product labels, not a stored list.
