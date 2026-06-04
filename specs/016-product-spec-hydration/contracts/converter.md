# Contract: `DatastoreProductToGraphQL`

**Package**: `github.com/gitstore-dev/gitstore/api/internal/graph`
**Function**: `DatastoreProductToGraphQL(p *datastore.Product) *model.Product`

## Signature (unchanged)

```go
func DatastoreProductToGraphQL(p *datastore.Product) *model.Product
```

## Behavioural Contract

### Preconditions

- `p` is a non-nil `*datastore.Product` as returned by `Datastore.GetProduct`, `GetProductByName`, or an element of `PageResult[Product].Items`.
- `p.Spec`, `p.Status`, and `p.OwnerRefs` are `json.RawMessage` and may be nil, empty, or a valid JSON blob.

### Postconditions

| Input field | Output field | Rule |
|---|---|---|
| `p.UID` | `Product.ID` | Base62-encoded node ID (`nodeKindProduct` prefix) |
| `p.Spec` = nil | `Product.Spec` | `&model.ProductSpec{Tags: []string{}, Media: []*model.MediaDefinition{}, Options: []*model.ProductOptionDefinition{}}` |
| `p.Spec` = valid JSON blob | `Product.Spec` | Fully populated `*model.ProductSpec` from `json.Unmarshal` |
| `p.Spec` = malformed JSON | `Product.Spec` | Empty spec (same as nil case); WARN log emitted with `uid` |
| `p.Status` = nil | `Product.Status` | `nil` |
| `p.Status` = valid JSON blob | `Product.Status` | Populated `*model.ProductStatus` from `json.Unmarshal` |
| `p.Status` = malformed JSON | `Product.Status` | `nil`; WARN log emitted with `uid` |
| `p.OwnerRefs` = nil | `Metadata.OwnerReferences` | `[]*model.OwnerReference{}` |
| `p.OwnerRefs` = valid JSON blob | `Metadata.OwnerReferences` | Populated `[]*model.OwnerReference` from `json.Unmarshal` |
| `p.OwnerRefs` = malformed JSON | `Metadata.OwnerReferences` | `[]*model.OwnerReference{}`; WARN log emitted |

### Invariants

- The function NEVER returns nil when `p` is non-nil.
- `Product.Spec` is NEVER nil — it is always a pointer to a (possibly empty) `model.ProductSpec`.
- `Product.Status` is nil if and only if `p.Status` is nil or empty.
- `Metadata.OwnerReferences` is NEVER nil — it is always a (possibly empty) slice.
- Collection fields (`Tags`, `Media`, `Options`) within `Product.Spec` are NEVER nil — they are always empty slices when unpopulated.
- `spec.categoryRef` is returned verbatim from the stored blob. Reference resolution (fetching the Category resource) is a GraphQL resolver concern (GH#82), not this function's.
- `status.resolved` sub-fields are returned verbatim from the stored blob. Cross-resource computation is deferred to GH#79/82/83.

### Error Handling

Deserialization errors are non-fatal. The function does not return an error. Instead:
- It returns the safe empty/nil value for the affected field.
- It emits a structured WARN log: `{"level":"warn","msg":"product blob unmarshal error","field":"spec|status|ownerRefs","uid":"<uid>","error":"<err>"}`.

This preserves partial responses (other fields remain correct) rather than surfacing a resolver error to clients.

## Callers

- `queryResolver.Product(ctx, by ProductBy)` — single product lookup. The `@oneOf` selector dispatches to either `Datastore.GetProduct(uid)` (via `products_by_uid`) or `Datastore.GetProductByName(ns, name)` (via `products_by_name`); both paths terminate here.
- `BuildProductConnection` (via `queryResolver.Products`) — paginated list (backed by Scylla `products_by_namespace` keyset pagination)
- `categoryResolver.Products` — products within a category
- `collectionResolver.Products` — products within a collection
- `nodeResolver.Node` (Relay) — global node lookup by encoded ID

The converter is agnostic to the storage layout: it operates on `*datastore.Product`. The Scylla backend's three-table denormalisation (see `data-model.md`) is encapsulated below the `Datastore` interface.

## Non-Goals

- Does NOT resolve `spec.categoryRef` to a Category resource.
- Does NOT compute `status.resolved.*` values.
- Does NOT validate field constraints — validation is the `validate.Parse` responsibility.
