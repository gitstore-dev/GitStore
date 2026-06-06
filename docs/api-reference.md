# GitStore GraphQL API Reference

Complete reference documentation for the GitStore GraphQL API.

## Table of Contents

- [Overview](#overview)
- [GraphQL Endpoint](#graphql-endpoint)
- [Authentication](#authentication)
- [Query Operations](#query-operations)
- [Mutation Operations](#mutation-operations)
- [Types](#types)
- [Scalars](#scalars)
- [Enums](#enums)
- [Filtering and Pagination](#filtering-and-pagination)
- [Error Handling](#error-handling)
- [Examples](#examples)
- [Controller Watch Stream (Proposal)](#controller-watch-stream-proposal)
- [Versioning](#versioning)

## Overview

GitStore provides a GraphQL API following the [Relay specification](https://relay.dev/docs/guides/graphql-server-specification/) for:

- **Queries**: Read operations for products, categories, collections
- **Mutations**: Write operations for managing catalog entities
- **Connections**: Cursor-based pagination for list queries
- **Node interface**: Global object identification

## GraphQL Endpoint

- **URL**: `http://localhost:4000/graphql`
- **Playground**: `http://localhost:4000/playground`
- **Method**: POST
- **Content-Type**: `application/json`

## Authentication

Read operations are public unless a resolver documents otherwise. Protected mutations require a JWT bearer token in the `Authorization` header:

```http
Authorization: Bearer <token>
```

Obtain a token with the GraphQL `login` mutation:

```graphql
mutation {
  login(input: { username: "admin", password: "<password>" }) {
    session {
      token
      expiresAt
      user {
        username
        isAdmin
      }
    }
  }
}
```

Namespace create and delete mutations require authentication. Creating `ENTERPRISE` namespaces requires `session.user.isAdmin == true`.

## Query Operations

### Global Node IDs

All GraphQL types that implement `Node` expose opaque global IDs. The ID format is base64-encoded `gid://GitStore/{NodeType}/{rawID}`. For example, product raw ID `123` is returned as `Z2lkOi8vR2l0U3RvcmUvUHJvZHVjdC8xMjM=`.

Clients should treat these values as opaque and pass them back unchanged to `node`, `nodes`, lookup selectors such as `product(by: {id: ...})`, filters, and mutation fields typed as `ID`. Business identifiers such as `product(by: {sku: ...})`, `category(by: {namespacePath: ...})`, `collection(by: {slug: ...})`, `namespace(by: {identifier: ...})`, and namespace `parentEnterpriseIdentifier` are not global IDs.

### node

Fetch any object by its globally unique ID (Relay Node interface).

```graphql
query {
  node(id: "Z2lkOi8vR2l0U3RvcmUvUHJvZHVjdC8xMjM=") {
    id
    ... on Product {
      title
      price
    }
  }
}
```

**Arguments**:
- `id: ID!` - Globally unique identifier

**Returns**: `Node` (can be cast to Product, Category, Collection, or Namespace)

---

### nodes

Fetch multiple objects by their IDs.

```graphql
query {
  nodes(ids: ["Z2lkOi8vR2l0U3RvcmUvUHJvZHVjdC8xMjM=", "Z2lkOi8vR2l0U3RvcmUvQ2F0ZWdvcnkvMTIz"]) {
    id
    ... on Product {
      title
    }
    ... on Category {
      name
    }
  }
}
```

**Arguments**:
- `ids: [ID!]!` - Array of globally unique identifiers

**Returns**: `[Node]!`

---

### namespace

Get a namespace by exactly one unique selector: `id` or `identifier`.

```graphql
query {
  namespace(by: {identifier: "acme-corp"}) {
    id
    identifier
    displayName
    tier
    parentEnterpriseId
    createdAt
    createdBy
    updatedAt
    updatedBy
  }
}
```

**Arguments**:
- `by: NamespaceBy!` - One of `id` (global ID) or `identifier`

**Returns**: `Namespace` (nullable)

---

### product

Get a single product by exactly one unique selector: `id` or `sku`.

```graphql
query {
  product(by: {sku: "MBP-16-M3-2024"}) {
    id
    title
    price
    currency
  }
}
```

**Arguments**:
- `by: ProductBy!` - One of `id` (global ID) or `sku`

**Returns**: `Product` (nullable)

---

### products

List products with filtering and cursor-based pagination.

```graphql
query {
  products(
    first: 10
    after: "cursor_abc"
    filter: {
      categoryId: "Z2lkOi8vR2l0U3RvcmUvQ2F0ZWdvcnkvMTIz"
      priceMin: "100"
      priceMax: "5000"
      inventoryStatus: IN_STOCK
    }
  ) {
    edges {
      cursor
      node {
        id
        title
        price
      }
    }
    pageInfo {
      hasNextPage
      endCursor
    }
    totalCount
  }
}
```

**Arguments**:
- `first: Int` - Number of items to return (forward pagination)
- `after: String` - Cursor to paginate after
- `last: Int` - Number of items to return (backward pagination)
- `before: String` - Cursor to paginate before
- `filter: ProductFilter` - Filter criteria

**Returns**: `ProductConnection!`

---

### category

Get a category by exactly one unique selector: `id` or `namespacePath`.

```graphql
query {
  category(by: {namespacePath: {namespace: "gitstore-test", name: "electronics"}}) {
    id
    metadata {
      name
    }
    children {
      metadata {
        name
      }
    }
  }
}
```

**Arguments**:
- `by: CategoryBy!` - One of `id` (global ID) or `namespacePath` (`namespace` + `name`)

**Returns**: `Category` (nullable)

---

### categories

Get categories in hierarchical structure with Relay cursor-based pagination.

```graphql
query {
  categories(first: 20) {
    edges {
      cursor
      node {
        id
        name
        displayOrder
        parent {
          name
        }
        children {
          name
        }
      }
    }
    pageInfo {
      hasNextPage
      endCursor
    }
    totalCount
  }
}
```

**Returns**: `CategoryConnection!`

---

### collection

Get a collection by exactly one unique selector: `id` or `slug`.

```graphql
query {
  collection(by: {slug: "featured"}) {
    id
    name
    products {
      edges {
        node {
          title
        }
      }
    }
  }
}
```

**Arguments**:
- `by: CollectionBy!` - One of `id` (global ID) or `slug`

**Returns**: `Collection` (nullable)

---

### collections

Get collections with Relay cursor-based pagination.

```graphql
query {
  collections(first: 20) {
    edges {
      cursor
      node {
        id
        name
        slug
        displayOrder
      }
    }
    pageInfo {
      hasNextPage
      endCursor
    }
    totalCount
  }
}
```

**Returns**: `CollectionConnection!`

---

### catalogVersion

Get the current catalog version (latest release tag).

```graphql
query {
  catalogVersion {
    tag
    commit
    publishedAt
    message
  }
}
```

**Returns**: `CatalogVersion!`

## Mutation Operations

### login

Authenticate and return a JWT session.

```graphql
mutation {
  login(input: { username: "admin", password: "<password>" }) {
    session {
      token
      expiresAt
      user {
        username
        isAdmin
      }
    }
  }
}
```

**Input Fields**:
- `username: String!` - Configured admin username
- `password: String!` - Configured admin password
- `clientMutationId: String` - Client-side mutation tracking

**Returns**: `LoginPayload!`

---

### createNamespace

Create a namespace. Requires authentication; `ENTERPRISE` requires an admin token.

```graphql
mutation {
  createNamespace(
    input: {
      clientMutationId: "create-acme-corp"
      identifier: "acme-corp"
      displayName: "Acme Corporation"
      tier: USER
    }
  ) {
    clientMutationId
    namespace {
      id
      identifier
      displayName
      tier
      createdAt
      createdBy
    }
  }
}
```

**Input Fields**:
- `clientMutationId: String` - Client-side mutation tracking
- `identifier: String!` - Globally unique namespace identifier
- `displayName: String` - Optional human-friendly display name
- `tier: NamespaceTier!` - `USER`, `ORGANISATION`, or `ENTERPRISE`
- `parentEnterpriseIdentifier: String` - Optional parent enterprise identifier for `ORGANISATION`

**Returns**: `CreateNamespacePayload!`

---

### deleteNamespace

Delete an empty namespace. Requires the namespace owner or an admin token.

```graphql
mutation {
  deleteNamespace(
    input: {
      clientMutationId: "delete-acme-corp"
      identifier: "acme-corp"
    }
  ) {
    clientMutationId
    deletedIdentifier
  }
}
```

**Input Fields**:
- `clientMutationId: String` - Client-side mutation tracking
- `identifier: String!` - Namespace identifier to delete

**Returns**: `DeleteNamespacePayload!`

---

### createProduct

Create a new product.

```graphql
mutation {
  createProduct(
    input: {
      title: "New Product"
      sku: "PROD-001"
      price: "99.99"
      currency: "USD"
      categoryId: "Z2lkOi8vR2l0U3RvcmUvQ2F0ZWdvcnkvMTIz"
      inventoryStatus: IN_STOCK
      inventoryQuantity: 50
      clientMutationId: "create-product-1"
    }
  ) {
    clientMutationId
    product {
      id
      title
    }
  }
}
```

**Input Fields**:
- `title: String!` - Product name
- `sku: String!` - Stock Keeping Unit (must be unique)
- `price: Decimal!` - Product price
- `currency: String!` - ISO currency code
- `categoryId: ID!` - Category global ID
- `body: String` - Product description (markdown)
- `collectionIds: [ID!]` - Collection global IDs to add product to
- `images: [String!]` - Array of image URLs
- `inventoryStatus: InventoryStatus` - Stock status
- `inventoryQuantity: Int` - Available quantity
- `metadata: JSON` - Custom attributes
- `clientMutationId: String` - Client-side mutation tracking

**Returns**: `CreateProductPayload!`

---

### updateProduct

Update an existing product.

```graphql
mutation {
  updateProduct(
    input: {
      id: "Z2lkOi8vR2l0U3RvcmUvUHJvZHVjdC8xMjM="
      title: "Updated Title"
      price: "3599.00"
      clientMutationId: "update-product-1"
    }
  ) {
    clientMutationId
    product {
      id
      title
      price
      updatedAt
    }
    conflict {
      field
      localValue
      remoteValue
    }
  }
}
```

**Input Fields**:
- `id: ID!` - Product global ID
- All other fields optional (only provided fields are updated)

**Returns**: `UpdateProductPayload!` with optional `conflict` field for concurrent edit detection

---

### deleteProduct

Delete a product.

```graphql
mutation {
  deleteProduct(
    input: {
      id: "Z2lkOi8vR2l0U3RvcmUvUHJvZHVjdC8xMjM="
      clientMutationId: "delete-product-1"
    }
  ) {
    clientMutationId
    deletedProductId
  }
}
```

**Input Fields**:
- `id: ID!` - Product global ID to delete
- `clientMutationId: String`

**Returns**: `DeleteProductPayload!`

---

### createCategory

Create a new category.

```graphql
mutation {
  createCategory(
    input: {
      name: "Laptops"
      slug: "laptops"
      parentId: "Z2lkOi8vR2l0U3RvcmUvQ2F0ZWdvcnkvMTIz"
      displayOrder: 1
      clientMutationId: "create-category-1"
    }
  ) {
    clientMutationId
    category {
      id
      name
      parent {
        name
      }
    }
  }
}
```

**Input Fields**:
- `name: String!` - Category name
- `slug: String!` - URL-friendly identifier
- `body: String` - Description (markdown)
- `parentId: ID` - Parent category global ID for hierarchy
- `displayOrder: Int` - Sort order
- `clientMutationId: String`

**Returns**: `CreateCategoryPayload!`

---

### updateCategory

Update an existing category.

```graphql
mutation {
  updateCategory(
    input: {
      id: "Z2lkOi8vR2l0U3RvcmUvQ2F0ZWdvcnkvMTIz"
      name: "Electronics & Gadgets"
      displayOrder: 0
    }
  ) {
    category {
      id
      name
      displayOrder
    }
  }
}
```

**Returns**: `UpdateCategoryPayload!`

---

### deleteCategory

Delete a category.

```graphql
mutation {
  deleteCategory(
    input: {
      id: "Z2lkOi8vR2l0U3RvcmUvQ2F0ZWdvcnkvMTIz"
    }
  ) {
    deletedCategoryId
  }
}
```

**Returns**: `DeleteCategoryPayload!`

---

### reorderCategories

Reorder categories by providing new display order.

```graphql
mutation {
  reorderCategories(
    input: {
      orderedIds: [
        "Z2lkOi8vR2l0U3RvcmUvQ2F0ZWdvcnkvMTIz",
        "Z2lkOi8vR2l0U3RvcmUvQ2F0ZWdvcnkvMTI0",
        "Z2lkOi8vR2l0U3RvcmUvQ2F0ZWdvcnkvMTI1"
      ]
    }
  ) {
    categories {
      id
      displayOrder
    }
  }
}
```

**Returns**: `ReorderCategoriesPayload!`

---

### createCollection

Create a new collection.

```graphql
mutation {
  createCollection(
    input: {
      name: "Summer Sale"
      slug: "summer-sale"
      displayOrder: 2
    }
  ) {
    collection {
      id
      name
    }
  }
}
```

**Returns**: `CreateCollectionPayload!`

---

### updateCollection

Update an existing collection.

```graphql
mutation {
  updateCollection(
    input: {
      id: "Z2lkOi8vR2l0U3RvcmUvQ29sbGVjdGlvbi8xMjM="
      name: "Featured Items"
    }
  ) {
    collection {
      id
      name
      updatedAt
    }
  }
}
```

**Returns**: `UpdateCollectionPayload!`

---

### deleteCollection

Delete a collection.

```graphql
mutation {
  deleteCollection(
    input: {
      id: "Z2lkOi8vR2l0U3RvcmUvQ29sbGVjdGlvbi8xMjM="
    }
  ) {
    deletedCollectionId
  }
}
```

**Returns**: `DeleteCollectionPayload!`

---

### reorderCollections

Reorder collections.

```graphql
mutation {
  reorderCollections(
    input: {
      orderedIds: [
        "Z2lkOi8vR2l0U3RvcmUvQ29sbGVjdGlvbi8xMjM=",
        "Z2lkOi8vR2l0U3RvcmUvQ29sbGVjdGlvbi8xMjQ=",
        "Z2lkOi8vR2l0U3RvcmUvQ29sbGVjdGlvbi8xMjU="
      ]
    }
  ) {
    collections {
      id
      displayOrder
    }
  }
}
```

**Returns**: `ReorderCollectionsPayload!`

---

### publishCatalog

Commit changes and create a release tag.

```graphql
mutation {
  publishCatalog(
    input: {
      version: "v1.0.5"
      message: "Add summer collection products"
    }
  ) {
    catalogVersion {
      tag
      commit
      publishedAt
    }
  }
}
```

**Input Fields**:
- `version: String!` - Release tag (e.g., "v1.0.5")
- `message: String!` - Commit message

**Returns**: `PublishCatalogPayload!`

## Types

### Product

```graphql
type Product implements Node {
  id: ID!
  sku: String!
  title: String!
  body: String
  price: Decimal!
  currency: String!
  category: Category!
  collections: [Collection!]!
  images: [String!]!
  inventoryStatus: InventoryStatus!
  inventoryQuantity: Int
  metadata: JSON
  createdAt: DateTime!
  updatedAt: DateTime!
}
```

### Category

```graphql
type Category implements Node {
  id: ID!
  name: String!
  slug: String!
  body: String
  parent: Category
  children: [Category!]!
  displayOrder: Int!
  products(
    first: Int
    after: String
    last: Int
    before: String
  ): ProductConnection!
  createdAt: DateTime!
  updatedAt: DateTime!
}
```

### Collection

```graphql
type Collection implements Node {
  id: ID!
  name: String!
  slug: String!
  body: String
  displayOrder: Int!
  products(
    first: Int
    after: String
    last: Int
    before: String
  ): ProductConnection!
  createdAt: DateTime!
  updatedAt: DateTime!
}
```

### Connections

Relay-style connections for cursor-based pagination.

```graphql
type ProductConnection {
  edges: [ProductEdge!]!
  pageInfo: PageInfo!
  totalCount: Int!
}

type ProductEdge {
  cursor: String!
  node: Product!
}

type CategoryConnection {
  edges: [CategoryEdge!]!
  pageInfo: PageInfo!
  totalCount: Int!
}

type CategoryEdge {
  cursor: String!
  node: Category!
}

type CollectionConnection {
  edges: [CollectionEdge!]!
  pageInfo: PageInfo!
  totalCount: Int!
}

type CollectionEdge {
  cursor: String!
  node: Collection!
}

type NamespaceConnection {
  edges: [NamespaceEdge!]!
  pageInfo: PageInfo!
  totalCount: Int!
}

type NamespaceEdge {
  cursor: String!
  node: Namespace!
}

type PageInfo {
  hasNextPage: Boolean!
  hasPreviousPage: Boolean!
  startCursor: String
  endCursor: String
}
```

### CatalogVersion

```graphql
type CatalogVersion {
  tag: String!
  commit: String!
  publishedAt: DateTime!
  message: String
  stats: CatalogStats
}

type CatalogStats {
  totalProducts: Int!
  totalCategories: Int!
  totalCollections: Int!
}
```

### ConflictInfo

Used for optimistic locking in update mutations.

```graphql
type ConflictInfo {
  field: String!
  localValue: String!
  remoteValue: String!
  timestamp: DateTime!
}
```

## Scalars

### Decimal

String-based decimal type for precise price representation.

```graphql
scalar Decimal
```

**Example**: `"99.99"`, `"1299.00"`

**Why string?** JavaScript's `Number` type loses precision for decimal values. Storing prices as strings preserves exact values.

### DateTime

ISO 8601 formatted date-time string.

```graphql
scalar DateTime
```

**Example**: `"2026-01-15T10:00:00Z"`

### JSON

Flexible JSON object for metadata.

```graphql
scalar JSON
```

**Example**:
```json
{
  "brand": "Apple",
  "processor": "M3 Max",
  "warranty_months": 12
}
```

## Enums

### InventoryStatus

```graphql
enum InventoryStatus {
  IN_STOCK
  OUT_OF_STOCK
  PREORDER
  DISCONTINUED
}
```

## Filtering and Pagination

### ProductFilter

```graphql
input ProductFilter {
  categoryId: ID
  collectionId: ID
  inventoryStatus: InventoryStatus
  priceMin: Decimal
  priceMax: Decimal
  search: String
}
```

**Filter Examples**:

**By category**:
```graphql
filter: { categoryId: "Z2lkOi8vR2l0U3RvcmUvQ2F0ZWdvcnkvMTIz" }
```

**By collection**:
```graphql
filter: { collectionId: "Z2lkOi8vR2l0U3RvcmUvQ29sbGVjdGlvbi8xMjM=" }
```

**By price range**:
```graphql
filter: { priceMin: "100", priceMax: "500" }
```

**By inventory status**:
```graphql
filter: { inventoryStatus: IN_STOCK }
```

**Multiple filters** (AND logic):
```graphql
filter: {
  categoryId: "Z2lkOi8vR2l0U3RvcmUvQ2F0ZWdvcnkvMTIz"
  priceMax: "1000"
  inventoryStatus: IN_STOCK
}
```

### Cursor-Based Pagination

**Forward pagination** (first N items):
```graphql
products(first: 10) {
  edges {
    cursor
    node { title }
  }
  pageInfo {
    hasNextPage
    endCursor
  }
}
```

**Next page**:
```graphql
products(first: 10, after: "cursor_from_previous_query") {
  # ...
}
```

**Backward pagination** (last N items):
```graphql
products(last: 10, before: "cursor") {
  # ...
}
```

## Error Handling

GraphQL errors follow the standard format:

```json
{
  "errors": [
    {
      "message": "Product with SKU 'INVALID-SKU' not found",
      "path": ["product"],
      "extensions": {
        "code": "NOT_FOUND"
      }
    }
  ],
  "data": {
    "product": null
  }
}
```

### Common Error Codes

- `NOT_FOUND` - Requested resource doesn't exist
- `VALIDATION_ERROR` - Input validation failed
- `CONFLICT` - Concurrent modification detected
- `INTERNAL_ERROR` - Server error

### Handling Null Results

Queries that fetch single entities return `null` if not found:

```graphql
query {
  product(by: {sku: "NONEXISTENT"}) {
    id  # Returns null if product not found
  }
}
```

Check for null before accessing nested fields:

```javascript
const result = await client.query({ query: GET_PRODUCT });
if (result.data.product) {
  console.log(result.data.product.title);
} else {
  console.log('Product not found');
}
```

## Examples

### Complete Product Query

```graphql
query GetProductDetails($sku: String!) {
  product(by: {sku: $sku}) {
    id
    sku
    title
    body
    price
    currency
    images
    inventoryStatus
    inventoryQuantity
    metadata
    category {
      id
      name
      slug
      parent {
        name
      }
    }
    collections {
      id
      name
      slug
    }
    createdAt
    updatedAt
  }
}
```

### Paginated Product List with Filters

```graphql
query ListProducts(
  $first: Int!
  $after: String
  $categoryId: ID
  $priceMin: Decimal
  $priceMax: Decimal
) {
  products(
    first: $first
    after: $after
    filter: {
      categoryId: $categoryId
      priceMin: $priceMin
      priceMax: $priceMax
      inventoryStatus: IN_STOCK
    }
  ) {
    edges {
      cursor
      node {
        id
        sku
        title
        price
        currency
        images
        category {
          name
        }
      }
    }
    pageInfo {
      hasNextPage
      endCursor
    }
    totalCount
  }
}
```

### Category Hierarchy

```graphql
query GetCategoryHierarchy {
  categories(first: 50) {
    edges {
      node {
        id
        name
        displayOrder
        parent {
          id
          name
        }
        children {
          id
          name
          displayOrder
        }
        products(first: 5) {
          totalCount
          edges {
            node {
              title
            }
          }
        }
      }
    }
  }
}
```

### Create Product with Collections

```graphql
mutation CreateProductComplete($input: CreateProductInput!) {
  createProduct(input: $input) {
    clientMutationId
    product {
      id
      sku
      title
      price
      category {
        name
      }
      collections {
        name
      }
    }
  }
}

# Variables:
{
  "input": {
    "title": "Wireless Mouse",
    "sku": "MOUSE-WIRELESS-001",
    "price": "29.99",
    "currency": "USD",
    "categoryId": "Z2lkOi8vR2l0U3RvcmUvQ2F0ZWdvcnkvMTIz",
    "collectionIds": ["Z2lkOi8vR2l0U3RvcmUvQ29sbGVjdGlvbi8xMjM=", "Z2lkOi8vR2l0U3RvcmUvQ29sbGVjdGlvbi8xMjQ="],
    "inventoryStatus": "IN_STOCK",
    "inventoryQuantity": 100,
    "images": ["https://cdn.example.com/mouse.jpg"],
    "metadata": {
      "brand": "TechMouse",
      "connectivity": "Bluetooth"
    },
    "clientMutationId": "create-mouse-1"
  }
}
```

### Update with Conflict Detection

```graphql
mutation UpdateProductWithConflictCheck($input: UpdateProductInput!) {
  updateProduct(input: $input) {
    clientMutationId
    product {
      id
      title
      price
      updatedAt
    }
    conflict {
      field
      localValue
      remoteValue
      timestamp
    }
  }
}
```

### Publish Changes

```graphql
mutation PublishCatalog {
  publishCatalog(
    input: {
      version: "v1.0.5"
      message: "Updated product prices for Q2 2026"
    }
  ) {
    catalogVersion {
      tag
      commit
      publishedAt
      message
    }
  }
}
```

## Controller Watch Stream (Proposal)

GitStore remains GraphQL-first, but controller loops for core resources and CRD kinds need Kubernetes-like watch semantics. The watch stream is exposed as GraphQL subscriptions over HTTP-compatible streaming transport (GraphQL-over-SSE).

### Event Model

- Event types follow `ADDED`, `MODIFIED`, and `DELETED`.
- Each event carries the full reconciled resource (`metadata`, `.spec`, `.status`).
- `metadata.resourceVersion` is monotonic and used as a resume token.

### Subscription Shape

```graphql
subscription WatchProducts($after: String) {
  watchProducts(afterResourceVersion: $after) {
    type
    resourceVersion
    object {
      metadata {
        uid
        resourceVersion
      }
      spec {
        title
        price
      }
      status {
        inventory
        lastReconciledAt
      }
    }
  }
}
```

### Resume and Recovery

Controllers should use a list-then-watch pattern:

1. Query current state snapshot.
2. Start subscription with `afterResourceVersion` from the snapshot.
3. On disconnect, reconnect with the last applied resource version.
4. If the server reports the resume point is too old, relist and restart the watch from a fresh snapshot.

### Controller Write-Back Pattern

- Controllers observe events from the stream.
- Controllers perform side effects out-of-band.
- Controllers write observed state via GraphQL status mutations.
- API persists the new status and emits the next watch event.

## Rate Limiting

The API currently does not enforce rate limits. Future versions will implement rate limiting with the following headers:

- `X-RateLimit-Limit`: Maximum requests per window
- `X-RateLimit-Remaining`: Remaining requests in window
- `X-RateLimit-Reset`: Window reset time (Unix timestamp)

## Versioning

The GraphQL API uses a single endpoint with schema evolution rather than versioned GraphQL paths.

For CRD-style kinds, the platform applies a hub-and-spoke conversion model:

- Each kind has one designated hub version (storage state), such as `gitstore.dev/v2`.
- Inbound manifests using older versions are converted to the hub version during the write pipeline.
- KV projections and core synthesised GraphQL types reflect hub-version shape.

### Conversion Hooks

When a kind introduces a breaking version, the owner provides WASI conversion hooks:

- Upgrade conversion (for example `v1 -> v2`)
- Downgrade conversion (for example `v2 -> v1`)

Write-time flow:

1. Client pushes a resource with non-hub `apiVersion`.
2. Orchestrator invokes the conversion hook.
3. Converted hub resource is validated and projected.
4. Read models remain normalised on hub version.

### GraphQL Backward Compatibility

Backward compatibility is maintained through field deprecation instead of endpoint versioning:

- Keep old fields available during migration windows.
- Mark legacy fields with `@deprecated(reason: "...")`.
- Resolve deprecated fields from hub state in resolver logic.

Example: if `price` is replaced by `pricingMatrix`, schema can expose both fields while clients migrate.

## Additional Resources

- [User Guide](user-guide.md) - Getting started and usage examples
- [GraphQL Playground](http://localhost:4000/playground) - Interactive API explorer
- [Relay Specification](https://relay.dev/docs/guides/graphql-server-specification/) - Pagination and connection patterns
