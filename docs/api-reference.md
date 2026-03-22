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

The current version does not require authentication for read operations. Mutations will require authentication in future versions.

## Query Operations

### node

Fetch any object by its globally unique ID (Relay Node interface).

```graphql
query {
  node(id: "prod_macbook_001") {
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

**Returns**: `Node` (can be cast to Product, Category, or Collection)

---

### nodes

Fetch multiple objects by their IDs.

```graphql
query {
  nodes(ids: ["prod_macbook_001", "cat_electronics_001"]) {
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

### product

Get a single product by SKU.

```graphql
query {
  product(sku: "MBP-16-M3-2024") {
    id
    title
    price
    currency
  }
}
```

**Arguments**:
- `sku: String!` - Stock Keeping Unit

**Returns**: `Product` (nullable)

---

### productById

Get a single product by ID.

```graphql
query {
  productById(id: "prod_macbook_001") {
    id
    sku
    title
  }
}
```

**Arguments**:
- `id: ID!` - Product ID

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
      categoryId: "cat_computers_001"
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

Get a category by slug.

```graphql
query {
  category(slug: "electronics") {
    id
    name
    children {
      name
    }
  }
}
```

**Arguments**:
- `slug: String!` - URL-friendly category identifier

**Returns**: `Category` (nullable)

---

### categoryById

Get a category by ID.

```graphql
query {
  categoryById(id: "cat_electronics_001") {
    id
    name
    parent {
      name
    }
  }
}
```

**Arguments**:
- `id: ID!` - Category ID

**Returns**: `Category` (nullable)

---

### categories

Get all categories in hierarchical structure.

```graphql
query {
  categories {
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
```

**Returns**: `[Category!]!`

---

### collection

Get a collection by slug.

```graphql
query {
  collection(slug: "featured") {
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
- `slug: String!` - URL-friendly collection identifier

**Returns**: `Collection` (nullable)

---

### collectionById

Get a collection by ID.

```graphql
query {
  collectionById(id: "coll_featured_001") {
    id
    name
  }
}
```

**Arguments**:
- `id: ID!` - Collection ID

**Returns**: `Collection` (nullable)

---

### collections

Get all collections.

```graphql
query {
  collections {
    id
    name
    slug
    displayOrder
  }
}
```

**Returns**: `[Collection!]!`

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
      categoryId: "cat_electronics_001"
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
- `categoryId: ID!` - Category assignment
- `body: String` - Product description (markdown)
- `collectionIds: [ID!]` - Collections to add product to
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
      id: "prod_macbook_001"
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
- `id: ID!` - Product ID
- All other fields optional (only provided fields are updated)

**Returns**: `UpdateProductPayload!` with optional `conflict` field for concurrent edit detection

---

### deleteProduct

Delete a product.

```graphql
mutation {
  deleteProduct(
    input: {
      id: "prod_macbook_001"
      clientMutationId: "delete-product-1"
    }
  ) {
    clientMutationId
    deletedProductId
  }
}
```

**Input Fields**:
- `id: ID!` - Product ID to delete
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
      parentId: "cat_electronics_001"
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
- `parentId: ID` - Parent category for hierarchy
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
      id: "cat_electronics_001"
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
      id: "cat_electronics_001"
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
        "cat_electronics_001",
        "cat_books_001",
        "cat_clothing_001"
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
      id: "coll_featured_001"
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
      id: "coll_featured_001"
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
        "coll_featured_001",
        "coll_new_001",
        "coll_bestsellers_001"
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

### ProductConnection

Relay-style connection for cursor-based pagination.

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
filter: { categoryId: "cat_electronics_001" }
```

**By collection**:
```graphql
filter: { collectionId: "coll_featured_001" }
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
  categoryId: "cat_electronics_001"
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
  product(sku: "NONEXISTENT") {
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
  product(sku: $sku) {
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
  categories {
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
    "categoryId": "cat_accessories_001",
    "collectionIds": ["coll_featured_001", "coll_new_001"],
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

## Rate Limiting

The API currently does not enforce rate limits. Future versions will implement rate limiting with the following headers:

- `X-RateLimit-Limit`: Maximum requests per window
- `X-RateLimit-Remaining`: Remaining requests in window
- `X-RateLimit-Reset`: Window reset time (Unix timestamp)

## Versioning

The API follows the catalog version from the latest release tag. Breaking schema changes will be communicated via:

- Deprecation notices in schema
- Migration guides in documentation
- Backward-compatible transitions where possible

## Additional Resources

- [User Guide](./user-guide.md) - Getting started and usage examples
- [GraphQL Playground](http://localhost:4000/playground) - Interactive API explorer
- [Relay Specification](https://relay.dev/docs/guides/graphql-server-specification/) - Pagination and connection patterns
