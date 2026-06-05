# Research: Product Spec and Status Hydration

**Branch**: `016-product-spec-hydration` | **Date**: 2026-06-04

> **User directive (2026-06-04)**: ScyllaDB is the chosen production datastore. Let it do what it does best — push pagination, sorting, and filtering into CQL rather than working around the database in Go. If the application needs to change to suit ScyllaDB best practices (including schema redesign), do that. Performance is the priority.

## Decision 1: Spec and Status deserialization approach

**Decision**: Unmarshal `p.Spec` and `p.Status` (`json.RawMessage`) directly in `DatastoreProductToGraphQL` using `encoding/json`. No intermediate types needed — the target structs (`model.ProductSpec`, `model.ProductStatus`) are already defined in `models_gen.go` with correct JSON tags.

**Rationale**: The datastore stores both blobs as `json.RawMessage`. The GraphQL model types already match the stored shape (same field names, same JSON tags). A single `json.Unmarshal` call is the minimal correct implementation. The `catalog.ProductSpec` type (YAML tags, used by the parser) is a different struct from `model.ProductSpec` (JSON tags, used by GraphQL) — they must not be conflated.

**Nil/error handling**:
- Nil blob → return empty struct / nil pointer as appropriate (see Decision 2).
- Unmarshal error → log at WARN level with product UID; return nil/empty field. Do not propagate as a resolver error — a malformed blob is a datastore consistency issue, not a client error.

**Alternatives considered**: A dedicated `specFromJSON` helper — accepted; isolating the unmarshal + nil/empty-collection normalisation in one place keeps the converter readable and matches the per-field helper pattern already used elsewhere.

---

## Decision 2: Nil blob → empty vs nil field policy

**Decision**:

| Field | Nil blob result | Rationale |
|---|---|---|
| `p.Spec` | `&model.ProductSpec{Tags: []string{}, Media: []*model.MediaDefinition{}, Options: []*model.ProductOptionDefinition{}}` | FR-001: spec is never nil in the response; collection fields must be empty lists, not nil (GraphQL clients expect consistent types) |
| `p.Status` | `nil` | FR-002: status is explicitly nullable; a product with no recorded status returns null |
| `p.OwnerRefs` | `[]*model.OwnerReference{}` | Metadata.ownerReferences is non-nullable in the schema; return empty slice |

**Rationale**: Follows the spec contract exactly. The distinction between "spec always present, possibly empty" vs "status conditionally present" matches the Kubernetes resource model: spec is always authored, status is optionally reconciled.

---

## Decision 3: Products cursor pagination — ScyllaDB-native CQL keyset (schema migration)

**Decision**: Redesign the `products` table so CQL itself produces correctly ordered, cursor-paginated pages. Replace the current `PRIMARY KEY (namespace, name)` with a layout that supports `(creation_timestamp, uid)` keyset-tuple inequality on the server side, exactly as the categories/collections/namespaces tables already do.

**Why we are doing this (per user directive)**: Forcing pagination back into application code via `paginateInMemory` would mean fetching whole partitions, deserialising every row, and discarding most of them on every page request. Discord's published guidance — and ScyllaDB's own best-practice docs — are unambiguous: model the data so the access pattern (`paginate products in a namespace newest-first`) maps to a single contiguous range read with `LIMIT`. We will do the migration.

### Target schema

```cql
CREATE TABLE IF NOT EXISTS products_by_namespace (
    namespace          text,
    creation_timestamp timestamp,
    uid                uuid,
    name               text,                 -- promoted to a non-key column on this view
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

`namespace` is the partition key (single-partition scans for `products(namespace)`); `(creation_timestamp DESC, uid DESC)` is the clustering order — every paginated read becomes a sliced range scan with `WHERE namespace = ? AND (creation_timestamp, uid) < (?, ?) LIMIT N+1`. This is the same shape `buildPaginatedSelect` already emits for categories/collections/namespaces.

### Lookup tables (denormalised access paths)

`GetProductByName(namespace, name)` and `GetProduct(uid)` are both still required. We follow the established Scylla pattern of one table per query:

1. **`products_by_namespace`** — main paginated view (above).
2. **`products_by_name`** — `PRIMARY KEY ((namespace), name)`, columns `(uid, creation_timestamp)`. Two-step lookup: `(namespace, name) → uid + creation_timestamp`, then read from `products_by_namespace`.
3. **`products_by_uid`** — `PRIMARY KEY (uid)`, columns `(namespace, creation_timestamp)`. Two-step lookup: `uid → (namespace, creation_timestamp)`, then read from `products_by_namespace`.

Writes (`CreateProduct`, `UpdateProduct`, `DeleteProduct`) fan out to all three tables atomically using a `LOGGED BATCH`. Within a single request the batch is executed as one CQL statement — Scylla guarantees all-or-nothing visibility for the row mutations, which is sufficient for our consistency model (writes happen on the ingest hot path with a single coordinator). `Generation`/`ResourceVersion` continue to provide optimistic-concurrency protection at the API layer.

### Migration strategy (inline edit — alpha, no consumers)

Per user directive, this is alpha software with no consumers; the schema change is made **inline** rather than as a new migration file:

1. Edit `migrations/001_initial_schema.cql` in place: replace the `products` table definition with `products_by_namespace`, `products_by_name`, `products_by_uid`. Drop the legacy `products_by_uid` *secondary index* line (the new `products_by_uid` *table* supersedes it).
2. Edit `migrations/002_add_initial_indices.cql` in place: remove any `products`-table index definitions if present (none currently — products had its index inline in 001, which is also being removed).
3. Local/dev environments: drop the keyspace and re-run migrations (`make scylla` after a `docker compose down -v` for the Scylla volume). Bootstrap data is re-ingested via `make bootstrap`.
4. The migration runner's checksum guard (`migration.go`) will reject the modified `001` against an already-migrated environment — this is correct behaviour for alpha; operators wipe and re-migrate.
5. The `scyllaDatastore` is rewritten to read/write the three new tables. `paginateProductsInMemory` is **not** introduced; `buildPaginatedSelect` is reused unchanged.

### Performance model

- `products(namespace)` paginated query: single partition, single sliced range read, `LIMIT N+1`. O(N) network round-trip, O(N) bytes returned.
- `product(uid)` lookup: 2 round-trips (lookup table → main table). Acceptable; `product(uid)` is not on the critical paginated path.
- `product(namespace, name)`: 2 round-trips. Same as above.
- Write fan-out: 3 row inserts per `CreateProduct` in one logged batch. Small constant amplification. Far cheaper than the read-amplification of full-partition fetches on every paginated read.

### Memdb behaviour

The memdb backend is unaffected — it already paginates in memory via `paginateSlice`. Only the Scylla backend's contract changes shape; the `datastore.Datastore` interface is unchanged.

### Alternatives considered

- **In-memory keyset (the previous draft of this decision)**: Rejected per user directive. Pulls every row in a namespace into the API process for every paginated request — read-amplification scales with partition size, defeats the cluster.
- **`ALLOW FILTERING` on the existing `(namespace, name)` table**: Rejected. Triggers a full partition scan with predicate filtering — same wall-clock cost as in-memory but on the database side, plus violates Scylla best practices (the gocqlx team explicitly warns against `ALLOW FILTERING` in production code paths).
- **Materialized views (MV) instead of denormalised tables**: Rejected. ScyllaDB's MV implementation has known consistency caveats (asynchronous propagation, base-view divergence on failure) that make MVs unsuitable for the primary read path. Hand-maintained denormalised tables are the canonical Scylla pattern and what this codebase already uses for repositories/categories/collections.
- **Secondary index on `creation_timestamp`**: Rejected. Secondary indexes don't support clustering order; we'd be back to fetch-and-sort.

---

## Decision 4: OwnerRefs hydration

**Decision**: Unmarshal `p.OwnerRefs` into `[]*model.OwnerReference` in the converter. Nil/empty blob → return empty `[]*model.OwnerReference{}`.

**Rationale**: `Metadata.OwnerReferences` is already typed in `model.ProductObjectMeta` as `[]*model.OwnerReference`. The blob is written by the ingest pipeline as a JSON array. The converter was returning an empty hardcoded slice — now it returns the actual stored value.

**Alternatives considered**: Keeping `OwnerReferences` as a hardcoded empty slice (no change). Rejected — contradicts FR-001 and would hide pipeline-written ownership data.

---

## Decision 5a: Unify the single-product lookup behind `@oneOf` (`product(by: ProductBy!)`)

**Decision**: Replace `product(namespace: String!, name: String!): Product` with `product(by: ProductBy!): Product`, where `ProductBy` is a `@oneOf` input mirroring the existing `RepositoryBy` shape:

```graphql
input ProductBy @oneOf {
  """Look up by globally unique Relay ID (UID)."""
  id: ID

  """Look up by namespace identifier + product name."""
  namespacePath: ProductNamespacePath
}

input ProductNamespacePath {
  namespace: String!
  name: String!
}
```

This subsumes both `GetProductByUID` and `GetProductByName` behind a single resolver. The redundant top-level `node(id: ID!)` route to a Product is preserved (Relay requirement) but the canonical product-typed entry point is unified.

**Rationale**:
- The new Scylla schema (Decision 3) gives us first-class server-side lookup paths for both `uid` and `(namespace, name)` via the `products_by_uid` and `products_by_name` lookup tables. With both lookups equally cheap on the storage side, exposing them through a single `@oneOf` selector is the natural API shape.
- It matches the existing convention already established in this codebase: `namespace(by: NamespaceBy!)`, `repository(by: RepositoryBy!)`, `category(by: CategoryBy!)`, `collection(by: CollectionBy!)`. Products are the last hold-out using positional `(namespace, name)` arguments.
- The current `ProductBy` definition in `schema.graphqls:105` (`{id, sku}`) is unused dead code with the wrong shape (no `sku` exists on products). This decision **replaces** that input rather than adding a parallel one.
- Validation is handled by the gqlgen-generated `@oneOf` enforcement: clients that pass zero or more than one field receive a structured GraphQL error before reaching the resolver.

**Resolver shape** (`product.resolvers.go`):

```go
func (r *queryResolver) Product(ctx context.Context, by model.ProductBy) (*model.Product, error) {
    switch {
    case by.ID != nil:
        rawID, err := decodeNodeID(nodeKindProduct, *by.ID)
        if err != nil { return nil, nil }
        p, err := r.service.GetProductByUID(ctx, rawID)
        if err != nil { return nil, nil }
        return DatastoreProductToGraphQL(p), nil
    case by.NamespacePath != nil:
        p, err := r.service.GetProductByName(ctx, by.NamespacePath.Namespace, by.NamespacePath.Name)
        if err != nil { return nil, nil }
        return DatastoreProductToGraphQL(p), nil
    default:
        return nil, fmt.Errorf("ProductBy: exactly one selector required")
    }
}
```

**Schema-source change**: In `shared/schemas/product.graphqls` line 8, replace:

```graphql
product(namespace: String!, name: String!): Product
```

with:

```graphql
product(by: ProductBy!): Product
```

In `shared/schemas/schema.graphqls` lines 102–108, replace the unused-and-wrong `ProductBy {id, sku}` definition with the redesigned shape above. Since products don't currently expose a slug field, omit `slug` from the `@oneOf` to avoid lying about a lookup path the storage layer can't satisfy. (If a product slug is added later — see GH#82 — extend `ProductBy` then.)

**Backwards compatibility**: This is a **breaking change** to the GraphQL contract. Acceptable because the project is alpha, no consumers exist (per the user's earlier directive on inline schema edits). All existing tests/resolvers/integration paths inside this repo are updated as part of this feature.

**Alternatives considered**:
- **Keep `product(namespace, name)` and add a separate `productById(id)`** — rejected. Adds a second top-level field for the same logical operation and breaks the established pattern (`namespace(by:)`, `repository(by:)`, etc.) used everywhere else in the schema.
- **Three-arm `@oneOf` with `slug` included** — rejected. Products have no slug field today (only categories and collections do). Lying in the type system is worse than waiting for GH#82 to add the field.

---

## Decision 5: Ingest-time spec field validation

**Decision**: Add three checks to `validateSpec` in `gitstore-api/internal/validate/validator.go`:
1. `spec.title` length ≤ 200 characters (matches the `validate:"omitempty,max=200"` struct tag on `catalog.ProductSpec.Title` — this is already enforced via struct tags, so no code change is needed here; confirm in implementation).
2. `spec.media[].fileRef` presence — each `MediaDefinition` must have a non-empty `fileRef.Name` and `fileRef.Kind` (already enforced by `validate:"required"` tags on `FileReference`).
3. Option name uniqueness — already enforced by `validateSpec`. No change needed.

**Resolution**: All three constraints are already enforced by existing struct tags and `validateSpec`. US4's implementation task is to write the missing test coverage for the `spec.title` max-length and `spec.media[].fileRef` cases, not to add new code.

**Rationale**: The `go-playground/validator` struct tags on `catalog.ProductSpec` were defined in spec#014 and enforced in spec#015. This feature's ingest validation story is satisfied by confirming (via tests) that those tags fire correctly.

---

## Gap Analysis: Missing test coverage (to be added in tasks)

| Scenario | Current gap |
|---|---|
| `DatastoreProductToGraphQL` with populated `Spec` blob returns correct fields | No test; converter always returns empty spec |
| `DatastoreProductToGraphQL` with nil `Spec` blob returns empty (not nil) spec | No test |
| `DatastoreProductToGraphQL` with populated `Status` blob returns correct conditions | No test; converter never returns status |
| `DatastoreProductToGraphQL` with nil `Status` blob returns nil status | No test |
| `DatastoreProductToGraphQL` with populated `OwnerRefs` blob returns correct references | No test |
| `ListProducts` (Scylla contract test) with `after` cursor returns next page | No test; current implementation repeats first page |
| `ListProducts` three-page forward traversal returns all products without overlap | No test |
| `ListProducts` with `before` cursor returns correct backward page | No test |
| `GetProductByName` after schema migration returns the same row written via `CreateProduct` | New test (denormalised lookup table) |
| `GetProduct(uid)` after schema migration returns the same row written via `CreateProduct` | New test (denormalised lookup table) |
| `UpdateProduct` keeps `products_by_namespace`, `products_by_name`, and `products_by_uid` in sync | New test (batch-write fan-out) |
| `DeleteProduct` removes the row from all three tables | New test (batch-write fan-out) |
| `spec.title` > 200 chars rejected at ingest | No test (struct tag enforces it, but not tested in validate package) |
| `spec.media[].fileRef.name` empty rejected at ingest | No test |
