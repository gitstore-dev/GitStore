# Contract: GraphQL Schema — Product Kubernetes-style Resource

**Feature**: 014-product-frontmatter  
**Date**: 2026-06-01  
**Strategy**: Full rewrite — the existing flat `Product` type and its subtypes are replaced with the Kubernetes-style schema. Alpha software: no backwards compatibility required.

The existing `product.graphqls` schema (flat `Product` with `sku`, `price`, `currency`, `inventoryStatus`, etc.) is replaced entirely. All reads go to the datastore which holds the full hydrated view.

---

## Replaced `Product` Type

```graphql
"""
Kubernetes-style product resource. All fields are read from the datastore,
which holds the fully hydrated view populated by the post-push ingest pipeline.
"""
type Product implements Node {
  id: ID!
  apiVersion: String!
  kind: String!
  metadata: ProductObjectMeta!
  spec: ProductSpec!
  status: ProductStatus
}

"""
Metadata for a product resource. Author-supplied fields (name, namespace, labels,
annotations) originate from the git file. System-assigned fields (uid,
resourceVersion, etc.) are written by the ingest pipeline.
"""
type ProductObjectMeta {
  name: String!
  namespace: String!
  labels: JSON
  annotations: JSON

  # System-assigned (read-only)
  uid: ID!
  resourceVersion: String!
  generation: Int!
  creationTimestamp: DateTime!
  revision: String
  ownerReferences: [OwnerReference!]!
}

type ProductSpec {
  title: String
  categoryRef: CatalogObjectReference
  tags: [String!]!
  media: [MediaDefinition!]!
  options: [ProductOptionDefinition!]!
}

type ProductStatus {
  observedGeneration: Int!
  lastAppliedRevision: String
  conditions: [ProductCondition!]!
  resolved: ResolvedProductDefinition
}

type ProductCondition {
  type: ProductConditionType!
  status: ConditionStatus!
  observedGeneration: Int
  lastTransitionTime: DateTime!
  reason: String
  message: String
}

enum ProductConditionType {
  PUBLISHED
  ADMISSION_ACCEPTED
  CATEGORY_RESOLVED
  OPTIONS_ACCEPTED
  VARIANTS_RESOLVED
  READY
}

enum ConditionStatus {
  TRUE
  FALSE
  UNKNOWN
}

"""
A pointer to another catalogue resource.
"""
type CatalogObjectReference {
  apiVersion: String
  kind: String
  name: String!
  namespace: String
  uid: ID
  resourceVersion: String
  fieldPath: String
}

type OwnerReference {
  apiVersion: String!
  kind: String!
  name: String!
  uid: ID!
}

type MediaDefinition {
  fileRef: FileReference!
}

type FileReference {
  name: String!
  kind: String!
  optional: Boolean!
}

type ProductOptionDefinition {
  name: String!
  title: String
  values: [String!]!
}

type ResolvedProductDefinition {
  category: ResolvedCategoryDefinition
  priceRange: [PriceRangeDefinition!]!
  totalInventory: Int!
  variantSummary: VariantSummaryDefinition
  defaultVariantRef: CatalogObjectReference
  media: [ResolvedFileDefinition!]!
}

type ResolvedCategoryDefinition {
  name: String!
  path: [String!]!
}

type PriceRangeDefinition {
  currencyCode: String!
  min: Decimal!
  max: Decimal!
}

type VariantSummaryDefinition {
  total: Int!
  ready: Int!
  unavailable: Int!
}

type ResolvedFileDefinition {
  name: String!
  url: String!
  contentType: String
}
```

---

## Replaced Query and Mutation Signatures

```graphql
type Query {
  """Fetch a product by name within a namespace."""
  product(namespace: String!, name: String!): Product

  """List products in a namespace."""
  products(
    namespace: String!
    first: Int
    after: String
    last: Int
    before: String
  ): ProductConnection!
}

type ProductConnection {
  edges: [ProductEdge!]!
  pageInfo: PageInfo!
  totalCount: Int!
}

type ProductEdge {
  cursor: String!
  node: Product!
}
```

**Removed query fields**: `product(by: ProductBy!)` (lookup by SKU is removed — `metadata.name` is the primary identifier). `products` (global listing without namespace) is removed — namespace is now required.

**Removed mutation types**: `CreateProductInput`, `UpdateProductInput`, `DeleteProductInput`, `CreateProductPayload`, `UpdateProductPayload`, `DeleteProductPayload`, `OptimisticLockConflict`. Products are authored via git push, not via GraphQL mutations. Mutation wiring is deferred to GH#185/186.

---

## Removed Types

The following types from the existing schema are removed:

- `ProductBy` (oneof lookup by id/sku — replaced by `product(namespace, name)`)
- `InventoryStatus` enum (inventory is a `status.resolved` concern)
- `CreateProductInput`, `UpdateProductInput`, `DeleteProductInput`
- `CreateProductPayload`, `UpdateProductPayload`, `DeleteProductPayload`
- `OptimisticLockConflict` (replaced by `metadata.resourceVersion` convention)

---

## Scalar and Type Reuse

- `DateTime` — existing scalar (RFC3339)
- `Decimal` — existing scalar (string-encoded decimal)
- `JSON` — existing scalar; used for `labels` and `annotations` (map[string]string at runtime)
- `PageInfo` — existing type
- `Node` — existing interface

---

## Schema Evolution Notes

- `CatalogObjectReference` is namespaced to avoid future collisions when other resource types (Category, Collection) define their own reference types.
- `ConditionStatus` and `ProductConditionType` are new enums with no conflict with removed enums.
- `OwnerReference` is product-scoped for now; can be promoted to a shared type when other resources need it.
- Mutations are intentionally absent — product lifecycle is git-driven. GH#185 will define any API-triggered operations (e.g. status patch).
- This schema defines the contract for GH#184. Resolvers that back `product` and `products` queries are wired in GH#185.
