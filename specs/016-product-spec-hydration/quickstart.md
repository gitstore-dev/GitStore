# Quickstart: Product Spec and Status Hydration

**Branch**: `016-product-spec-hydration` | **Date**: 2026-06-04

## What changes

Three correctness gaps are closed:

1. **Spec hydration** — `DatastoreProductToGraphQL` now deserialises the stored `Spec` JSON blob into `model.ProductSpec`. Previously returned an empty spec for every product.

2. **Status hydration** — `DatastoreProductToGraphQL` now deserialises the stored `Status` JSON blob into `*model.ProductStatus`. Previously always returned nil.

3. **Products cursor pagination (Scylla-native)** — the `products` table is replaced with three query-driven tables (`products_by_namespace`, `products_by_name`, `products_by_uid`). `ListProducts` is rewritten to use server-side CQL keyset pagination via `buildPaginatedSelect`. Previously ignored cursor arguments and always returned the first page.

4. **Unified single-product selector (`@oneOf`)** — the top-level query changes from `product(namespace, name): Product` to `product(by: ProductBy!): Product`, where `ProductBy` is a `@oneOf` input with `id` and `namespacePath { namespace, name }` arms. Mirrors the existing `RepositoryBy`/`NamespaceBy`/`CategoryBy`/`CollectionBy` pattern. The dead `ProductBy { id, sku }` block in `schema.graphqls` is removed.

Additionally, missing test coverage is added for spec field constraints at the ingest boundary (US4).

## Files to change

| File | Change |
|---|---|
| `shared/schemas/product.graphqls` | Replace `product(namespace, name): Product` with `product(by: ProductBy!): Product` |
| `shared/schemas/schema.graphqls` | Replace dead `input ProductBy { id, sku }` with new `@oneOf` definition + add `input ProductNamespacePath { namespace, name }` |
| `gitstore-api/internal/graph/product.resolvers.go` | Update `Product` resolver signature to take `ProductBy`; switch on selector arm to dispatch to `GetProductByUID` or `GetProductByName` |
| `gitstore-api/internal/graph/converters.go` | Unmarshal `Spec`, `Status`, `OwnerRefs` blobs; replace hardcoded empty spec; add `Status` field to returned `model.Product` |
| `gitstore-api/internal/graph/converters_test.go` | Add unit tests for hydrated converter (create if not present) |
| `gitstore-api/internal/graph/product_resolver_test.go` | Add resolver-level tests for `ProductBy.id` arm and `ProductBy.namespacePath` arm |
| `gitstore-api/internal/datastore/scylla/migrations/001_initial_schema.cql` | **Inline edit** (alpha; no consumers): replace `products` table + `products_by_uid` secondary index with three new tables — `products_by_namespace`, `products_by_name`, `products_by_uid` |
| `gitstore-api/internal/datastore/scylla/models.go` | Replace `Product` table model with `ProductByNamespace`, `ProductByName`, `ProductByUID` |
| `gitstore-api/internal/datastore/scylla/backend.go` | Rewrite `CreateProduct` / `UpdateProduct` / `DeleteProduct` to use `LOGGED BATCH` across the three tables; rewrite `GetProduct` / `GetProductByName` to do the two-step lookup; rewrite `ListProducts` to use `buildPaginatedSelect` |
| `gitstore-api/internal/datastore/scylla/pagination.go` | Generalise `buildPaginatedSelect` to take a partition column + bind value (currently hardcodes `bucket`) |
| `gitstore-api/internal/datastore/scylla/backend_test.go` | Update product tests for new tables; add cursor-pagination contract tests |
| `gitstore-api/tests/contract/datastore/contract_test.go` | Add spec/status round-trip assertions; add three-page forward + backward cursor traversal tests |
| `gitstore-api/internal/validate/validator_test.go` | Add `spec.title` max-length and `spec.media[].fileRef` empty tests |

## Key implementation notes

### `@oneOf` product selector (`product.graphqls` + `schema.graphqls`)

In `shared/schemas/product.graphqls` line 8, replace:

```graphql
product(namespace: String!, name: String!): Product
```

with:

```graphql
product(by: ProductBy!): Product
```

In `shared/schemas/schema.graphqls`, replace the existing `input ProductBy @oneOf { id, sku }` block (lines 102–108) with:

```graphql
"""
Selector for a single product lookup. Exactly one field must be set.
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

Run `go generate ./...` (or whatever invokes gqlgen) to regenerate `models_gen.go` and the resolver stub. Then update `product.resolvers.go`:

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

Compare to [category.resolvers.go:165](gitstore-api/internal/graph/category.resolvers.go#L165) for the established pattern.

### Spec hydration (`converters.go`)

Replace the hardcoded empty spec line:

```go
// Before
Spec: &model.ProductSpec{Tags: []string{}, Media: []*model.MediaDefinition{}, Options: []*model.ProductOptionDefinition{}},

// After
Spec: specFromJSON(p.Spec),
```

Add a package-level helper:

```go
func specFromJSON(raw json.RawMessage) *model.ProductSpec {
    empty := &model.ProductSpec{Tags: []string{}, Media: []*model.MediaDefinition{}, Options: []*model.ProductOptionDefinition{}}
    if len(raw) == 0 {
        return empty
    }
    var s model.ProductSpec
    if err := json.Unmarshal(raw, &s); err != nil {
        // log WARN — see contract
        return empty
    }
    if s.Tags == nil { s.Tags = []string{} }
    if s.Media == nil { s.Media = []*model.MediaDefinition{} }
    if s.Options == nil { s.Options = []*model.ProductOptionDefinition{} }
    return &s
}
```

Similar helpers for `statusFromJSON` and `ownerRefsFromJSON`.

### Status hydration (`converters.go`)

Add `Status` field to the returned `model.Product`:

```go
Status: statusFromJSON(p.Status),
```

### Generalise `buildPaginatedSelect` (`scylla/pagination.go`)

Today it hardcodes `bucket = ?` because every existing caller (categories, collections, namespaces) uses a `bucket` partition. Products partition by `namespace`. Replace the hardcoded prefix with a `partitionCol string, partitionVal any` parameter (or wrap it in a small `partition := struct{ Col string; Val any }` if multiple bind values are needed later). Existing callers pass `"bucket", BucketAll`; the products caller passes `"namespace", ns`.

This is a refactor — the CQL emission logic is unchanged; only the partition predicate becomes a parameter.

### Schema change — inline edit of `migrations/001_initial_schema.cql`

Replace the existing `products` table definition (lines 1–41 of `001_initial_schema.cql`, including the trailing `CREATE INDEX … products_by_uid` line) with the three new tables below. Alpha software, no consumers — operators wipe the dev keyspace and re-migrate (`docker compose down -v` against the Scylla volume + `make scylla` + `make bootstrap`).

```sql
CREATE TABLE IF NOT EXISTS products_by_namespace (
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

CREATE TABLE IF NOT EXISTS products_by_name (
    namespace          text,
    name               text,
    uid                uuid,
    creation_timestamp timestamp,
    PRIMARY KEY ((namespace), name)
);

CREATE TABLE IF NOT EXISTS products_by_uid (
    uid                uuid,
    namespace          text,
    creation_timestamp timestamp,
    PRIMARY KEY (uid)
);
```

The migration runner's checksum guard (`migration.go`) will reject a modified `001` against an already-migrated keyspace; for alpha environments this is the correct alarm — wipe and re-migrate.

### Backend rewrite (`scylla/backend.go`)

`CreateProduct` (and `UpdateProduct`, `DeleteProduct`) write to all three tables in a single `LOGGED BATCH`:

```go
b := s.session.NewBatch(gocql.LoggedBatch).WithContext(ctx)
b.Query(insertByNamespace, args...)
b.Query(insertByName,      args...)
b.Query(insertByUID,       args...)
return s.session.ExecuteBatch(b)
```

`GetProductByName` does a two-step lookup: read `(uid, creation_timestamp)` from `products_by_name`, then `SELECT * FROM products_by_namespace WHERE namespace = ? AND creation_timestamp = ? AND uid = ?`. Same shape for `GetProduct(uid)`.

`ListProducts` uses the generalised `buildPaginatedSelect`:

```go
pq := buildPaginatedSelect(s.productByNamespaceTable, page,
    "namespace", namespace, /* extraWhere */ nil, /* extraArgs */ nil)
var rows []productRow
if err := s.session.Query(pq.Stmt, nil).Bind(pq.Args...).SelectRelease(&rows); err != nil { ... }
if page.Last > 0 { reverseRows(rows) }
return buildPageResult(products, limit, page), nil
```

No `paginateProductsInMemory` is introduced.

## Running tests

```bash
cd gitstore-api

# Unit tests (converter + validate)
go test -race ./internal/graph/... ./internal/validate/...

# Contract tests (requires running Scylla — use make scylla first)
make scylla
go test -race ./tests/contract/datastore/... -v
```

## Acceptance check

```bash
# US1: Spec hydration
go test ./internal/graph/... -run TestDatastoreProductToGraphQL_SpecHydration -v

# US2: Status hydration
go test ./internal/graph/... -run TestDatastoreProductToGraphQL_StatusHydration -v

# US3: Pagination (Scylla-native)
go test ./tests/contract/datastore/... -run TestPagination_Products -v

# US4: Ingest validation
go test ./internal/validate/... -run TestParse_SpecTitle -v
```
