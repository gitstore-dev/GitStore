# Data Model: Product Spec and Status Hydration

**Branch**: `016-product-spec-hydration` | **Date**: 2026-06-04

## Overview

This feature has two domains of change:

1. **Mapping layer** — `DatastoreProductToGraphQL` now hydrates `Spec`, `Status`, and `OwnerRefs` from their stored JSON blobs. No new in-memory entity types are introduced; existing GraphQL model structs are populated.
2. **ScyllaDB schema** — the `products` table is replaced with a Scylla-native, query-driven layout that supports CQL-side keyset pagination over `(creation_timestamp, uid)`. This is an **inline edit** of `migrations/001_initial_schema.cql` (alpha software; no consumers — see research.md Decision 3 for the migration strategy).

The persistence model fields in Go (`datastore.Product`) are unchanged.

---

## Existing Entity: `datastore.Product` (Go struct — unchanged)

The authoritative persistent record. The Go struct is the same; only the underlying CQL tables that store it change.

| Field | Type | Role |
|---|---|---|
| `UID` | `string` (UUID) | Identity — primary key in `products_by_uid`; clustering key in `products_by_namespace` |
| `Namespace` | `string` | Partition key in `products_by_namespace` and `products_by_name` |
| `Name` | `string` | Clustering key in `products_by_name` |
| `APIVersion` | `string` | Resource envelope |
| `Kind` | `string` | Resource envelope |
| `Generation` | `int64` | Optimistic concurrency |
| `ResourceVersion` | `string` | Opaque version token |
| `CreationTimestamp` | `time.Time` | Clustering key in `products_by_namespace`; cursor anchor |
| `Revision` | `string` | Git ref, e.g. `main@sha1:abc123` |
| `Labels` | `map[string]string` | Author-supplied classification |
| `Annotations` | `map[string]string` | Author-supplied metadata |
| `OwnerRefs` | `json.RawMessage` | JSON array of `OwnerReference`; nil if unset |
| `GitCommitSHA` | `string` | Git provenance |
| `GitRef` | `string` | Git provenance |
| `Spec` | `json.RawMessage` | **JSON blob of `ProductSpec` fields; nil if empty spec** |
| `Body` | `string` | Raw Markdown body |
| `Status` | `json.RawMessage` | **JSON blob of `ProductStatus`; nil until pipeline reconciles** |

---

## ScyllaDB Tables (new layout — inline edit of migration 001's `products` definitions)

ScyllaDB best practice: model tables around the read path. Each query gets a dedicated table; writes fan out via a `LOGGED BATCH`. This matches the existing pattern for `repositories`, `namespace_mappings`, etc.

### `products_by_namespace` — primary paginated read path

```cql
CREATE TABLE products_by_namespace (
    namespace          text,
    creation_timestamp timestamp,
    uid                uuid,
    name               text,
    api_version        text,
    kind               text,
    generation         bigint,
    resource_version   text,
    revision           text,
    labels             map<text, text>,
    annotations        map<text, text>,
    owner_refs         text,
    git_commit_sha     text,
    git_ref            text,
    spec               text,
    body               text,
    status             text,
    PRIMARY KEY ((namespace), creation_timestamp, uid)
) WITH CLUSTERING ORDER BY (creation_timestamp DESC, uid DESC);
```

- **Partition key**: `namespace` — every paginated `products(namespace)` query hits a single partition.
- **Clustering**: `(creation_timestamp DESC, uid DESC)` — newest-first, matches the existing `EncodeKeysetCursor` ordering used by `BuildProductConnection`.
- **Pagination**: `WHERE namespace = ? AND (creation_timestamp, uid) < (?, ?) LIMIT N+1` for forward; reverse predicate + `ORDER BY ASC` for backward. Implemented via the existing `buildPaginatedSelect` helper, parameterised over the partition key column.

### `products_by_name` — `GetProductByName(namespace, name)` lookup

```cql
CREATE TABLE products_by_name (
    namespace          text,
    name               text,
    uid                uuid,
    creation_timestamp timestamp,
    PRIMARY KEY ((namespace), name)
);
```

- Two-step lookup: `(namespace, name) → (uid, creation_timestamp)` then read from `products_by_namespace` using the full clustering key.
- This view stores only the index columns; the canonical row lives in `products_by_namespace`. This avoids triple-storing every blob field.

### `products_by_uid` — `GetProduct(uid)` lookup

```cql
CREATE TABLE products_by_uid (
    uid                uuid,
    namespace          text,
    creation_timestamp timestamp,
    PRIMARY KEY (uid)
);
```

- Two-step lookup: `uid → (namespace, creation_timestamp)` then read from `products_by_namespace`.
- Replaces the secondary index `products_by_uid` from migration 001.

### Write fan-out

`CreateProduct`, `UpdateProduct`, and `DeleteProduct` execute a single CQL `BEGIN BATCH … APPLY BATCH` containing the corresponding mutations against all three tables. Logged batches give us atomic visibility across the three writes.

### Migration approach: inline edit of `001_initial_schema.cql`

Per user directive (alpha software, no consumers), the existing `001_initial_schema.cql` is edited in place: the `products` table definition and its `products_by_uid` secondary index are replaced with the three new `CREATE TABLE` statements above. No new migration file is added.

Existing development environments must wipe the Scylla keyspace and re-run migrations (`docker compose down -v` for the Scylla volume, then `make scylla`). Bootstrap data is re-ingested via `make bootstrap`. The migration checksum guard in `migration.go` will refuse to run the modified `001` against an already-migrated keyspace — this is the correct alarm for alpha; operators wipe and re-migrate.

---

## New Input Type: `ProductBy` + `ProductNamespacePath` (`@oneOf`)

The single-product query is unified behind a `@oneOf` selector, mirroring the established pattern (`namespace(by:)`, `repository(by:)`, `category(by:)`, `collection(by:)`). The existing `ProductBy {id, sku}` definition in `shared/schemas/schema.graphqls:105` is dead, wrong-shape code (no `sku` exists on products) and is replaced.

`name` is the Kubernetes-style identifier for a product — it plays the same role as `slug` does on Category/Collection. Products do not have a separate `slug` field; `metadata.name` is the URL-stable identifier and is unique per-namespace (not global). For that reason the non-ID lookup arm must carry both `namespace` and `name` together, which the composite child input enforces.

```graphql
"""
Selector for a single product lookup.
Exactly one field must be set.
"""
input ProductBy @oneOf {
  """Look up by globally unique Relay ID (encodes the product UID)."""
  id: ID

  """Look up by namespace identifier + product name (Kubernetes-style metadata.name)."""
  namespacePath: ProductNamespacePath
}

"""
Composite selector: namespace identifier (human-readable slug) + product name.
"""
input ProductNamespacePath {
  namespace: String!
  name: String!
}
```

### Field-resolution mapping

| Selector arm | Datastore call | Scylla read path |
|---|---|---|
| `id` (decoded UID) | `Datastore.GetProduct(uid)` | `products_by_uid` (1 hop) → `products_by_namespace` (1 hop) |
| `namespacePath` | `Datastore.GetProductByName(ns, name)` | `products_by_name` (1 hop) → `products_by_namespace` (1 hop) |

Both arms terminate at `DatastoreProductToGraphQL` (FR-011 — single authoritative mapping).

The Query field signature changes from:

```graphql
product(namespace: String!, name: String!): Product
```

to:

```graphql
product(by: ProductBy!): Product
```

This is a **breaking GraphQL change**. Acceptable per the alpha-software, no-consumers stance already adopted for the Scylla schema.

---

## Existing Entity: `model.ProductSpec` (GraphQL response — target of hydration)

Populated from `datastore.Product.Spec` by the converter. No struct changes.

| Field | JSON key | Nil blob default |
|---|---|---|
| `Title` | `title` | `nil` |
| `CategoryRef` | `categoryRef` | `nil` (pass-through stored reference — resolution deferred to GH#82) |
| `Tags` | `tags` | `[]string{}` |
| `Media` | `media` | `[]*model.MediaDefinition{}` |
| `Options` | `options` | `[]*model.ProductOptionDefinition{}` |

---

## Existing Entity: `model.ProductStatus` (GraphQL response — target of hydration)

Populated from `datastore.Product.Status` by the converter. Nil when no status blob stored.

| Field | JSON key | Notes |
|---|---|---|
| `ObservedGeneration` | `observedGeneration` | Must equal `metadata.generation` at write time (FR-010) |
| `LastAppliedRevision` | `lastAppliedRevision` | Optional; e.g. `main@sha1:abc123` |
| `Conditions` | `conditions` | `[]*model.ProductCondition`; empty list if pipeline ran but no conditions written |
| `Resolved` | `resolved` | `*model.ResolvedProductDefinition`; nil if not yet computed |

### `model.ProductCondition`

| Field | JSON key | Notes |
|---|---|---|
| `Type` | `type` | `ProductConditionType` enum |
| `Status` | `status` | `ConditionStatus`: `TRUE` / `FALSE` / `UNKNOWN` |
| `ObservedGeneration` | `observedGeneration` | Optional |
| `LastTransitionTime` | `lastTransitionTime` | Required |
| `Reason` | `reason` | Optional human-readable token |
| `Message` | `message` | Optional human-readable description |

### `model.ResolvedProductDefinition` (pass-through)

All sub-fields are hydrated verbatim from the stored blob. Cross-resource computation is deferred:

| Field | Owner |
|---|---|
| `Category` | GH#82 (CategoryTaxonomy) |
| `PriceRange` | GH#83 (ProductVariant) |
| `TotalInventory` | GH#83 (ProductVariant) |
| `VariantSummary` | GH#83 (ProductVariant) |
| `DefaultVariantRef` | GH#83 (ProductVariant) |
| `Media` | GH#79 (File frontmatter) |

---

## Converter Mapping: `DatastoreProductToGraphQL`

The single authoritative mapping point (FR-011). Current → target change:

```
datastore.Product.Spec (json.RawMessage)
  nil   → model.ProductSpec{Tags:[], Media:[], Options:[]}
  blob  → json.Unmarshal → model.ProductSpec{...populated...}
  error → log WARN(uid); return empty spec (non-fatal)

datastore.Product.Status (json.RawMessage)
  nil   → nil (*model.ProductStatus)
  blob  → json.Unmarshal → *model.ProductStatus{...populated...}
  error → log WARN(uid); return nil (non-fatal)

datastore.Product.OwnerRefs (json.RawMessage)
  nil   → []*model.OwnerReference{}
  blob  → json.Unmarshal → []*model.OwnerReference{...}
  error → log WARN(uid); return [] (non-fatal)
```

---

## Pagination: Products Keyset Cursor (CQL-native)

Server-side keyset pagination over `products_by_namespace`. Identical shape to categories/collections/namespaces.

**Sort order**: `creation_timestamp DESC, uid DESC` (clustering order on the table).

**Cursor format**: opaque base64-encoded `keyset|<RFC3339Nano timestamp>|<uid>` — same format as Categories/Collections/Namespaces via `EncodeKeysetCursor`. The cursor's `(timestamp, uid)` pair binds directly into the CQL tuple inequality.

**Forward pagination** (`first` / `after`):
- `SELECT … FROM products_by_namespace WHERE namespace = ? [AND (creation_timestamp, uid) < (?, ?)] LIMIT N+1`
- `hasNextPage = rows > N` (server returns up to N+1; trim the extra).

**Backward pagination** (`last` / `before`):
- `SELECT … FROM products_by_namespace WHERE namespace = ? [AND (creation_timestamp, uid) > (?, ?)] ORDER BY creation_timestamp ASC, uid ASC LIMIT N+1`
- Reverse the result locally before returning so callers always receive newest-first.
- `hasPreviousPage = rows > N`.

**Implementation note**: `buildPaginatedSelect` currently hardcodes `bucket = ?` as the partition predicate (categories/collections/namespaces all use a global `bucket` partition). For products the partition predicate is `namespace = ?`. The helper is being generalised to accept a configurable partition column + bind value rather than the hardcoded `bucket`.

The memdb backend continues to paginate via `paginateSlice` and is unaffected by the schema change.
