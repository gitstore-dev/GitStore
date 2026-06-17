# GitStore GraphQL API Reference

This reference documents the current GraphQL contract exposed by `gitstore-api`.

Catalogue reads are GraphQL-first. Catalogue writes are Git-driven today: author Markdown/frontmatter resources, commit them, and push through Git Smart HTTP. Product, category, collection, and variant GraphQL write operations are intentionally not documented as supported catalogue write APIs while Git-backed CRUD over GraphQL is being finalized.

## Endpoint

| Item | Value |
|---|---|
| GraphQL URL | `http://localhost:4000/graphql` |
| Playground | `http://localhost:4000/playground` |
| Method | `POST` |
| Content type | `application/json` |

## Authentication

Public read access depends on resolver and deployment policy. Protected mutations require a JWT bearer token:

```http
Authorization: Bearer <token>
```

Login:

```graphql
mutation Login {
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

Refresh:

```graphql
mutation RefreshToken {
  refreshToken(input: {}) {
    session {
      token
      expiresAt
    }
  }
}
```

Logout:

```graphql
mutation Logout {
  logout(input: {}) {
    success
  }
}
```

## Operation Summary

### Queries

| Operation | Purpose |
|---|---|
| `node(id: ID!)` | Fetch one Relay node by global ID |
| `nodes(ids: [ID!]!)` | Fetch multiple Relay nodes by global ID |
| `namespace(by: NamespaceBy!)` | Fetch one namespace |
| `namespaces(...)` | List namespaces |
| `repository(by: RepositoryBy!)` | Fetch one repository |
| `repositories(namespaceId: ID!, ...)` | List repositories in a namespace |
| `product(by: ProductBy!)` | Fetch one product resource |
| `products(namespace: String!, ...)` | List products in a namespace |
| `productVariant(by: ProductVariantBy!)` | Fetch one product variant resource |
| `productVariants(namespace: String!, ...)` | List product variants in a namespace |
| `category(by: CategoryBy!)` | Fetch one category resource |
| `categories(...)` | List categories |
| `collection(by: CollectionBy!)` | Fetch one collection resource |
| `collections(namespace: String!, ...)` | List collections in a namespace |
| `catalogVersion` | Legacy schema-continuity field for current catalogue version metadata |

### Mutations

| Operation | Purpose |
|---|---|
| `login(input: LoginInput!)` | Create a JWT session |
| `logout(input: LogoutInput!)` | End the current session |
| `refreshToken(input: RefreshTokenInput!)` | Refresh a JWT session |
| `createNamespace(input: CreateNamespaceInput!)` | Create a namespace |
| `deleteNamespace(input: DeleteNamespaceInput!)` | Delete an empty namespace |
| `createRepository(input: CreateRepositoryInput!)` | Create a repository in a namespace |
| `renameRepository(input: RenameRepositoryInput!)` | Rename a repository |
| `transferRepository(input: TransferRepositoryInput!)` | Move a repository to another namespace |
| `deleteRepository(input: DeleteRepositoryInput!)` | Delete a repository and its storage |

## Relay IDs

Types that implement `Node` expose opaque global IDs. Treat IDs as opaque strings and pass them back unchanged to `node`, `nodes`, selectors, filters, and mutation inputs typed as `ID`.

Human-readable selectors use namespace paths:

```graphql
query {
  product(
    by: {
      namespacePath: {
        namespace: "gitstore-test"
        name: "macbook-pro"
      }
    }
  ) {
    id
  }
}
```

## Query Operations

### node

```graphql
query GetNode($id: ID!) {
  node(id: $id) {
    id
    ... on Product {
      metadata {
        name
      }
      spec {
        title
      }
    }
  }
}
```

### nodes

```graphql
query GetNodes($ids: [ID!]!) {
  nodes(ids: $ids) {
    id
    ... on Namespace {
      identifier
    }
    ... on Repository {
      name
      defaultBranch
    }
  }
}
```

### namespace

```graphql
query GetNamespace {
  namespace(by: { identifier: "gitstore-test" }) {
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

### namespaces

```graphql
query ListNamespaces {
  namespaces(first: 20) {
    edges {
      cursor
      node {
        id
        identifier
        tier
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

### repository

```graphql
query GetRepository {
  repository(
    by: {
      namespacePath: {
        namespace: "gitstore-test"
        name: "catalog"
      }
    }
  ) {
    id
    name
    defaultBranch
    storageClass
    storagePath
    namespace {
      identifier
    }
  }
}
```

### repositories

```graphql
query ListRepositories($namespaceId: ID!) {
  repositories(namespaceId: $namespaceId, first: 20) {
    edges {
      cursor
      node {
        id
        name
        defaultBranch
      }
    }
    totalCount
  }
}
```

### product

`Product` is the non-sellable parent descriptor. SKU, pricing, and inventory are on `ProductVariant`.

```graphql
query GetProduct {
  product(
    by: {
      namespacePath: {
        namespace: "gitstore-test"
        name: "macbook-pro"
      }
    }
  ) {
    id
    apiVersion
    kind
    metadata {
      name
      namespace
      uid
      resourceVersion
      generation
      creationTimestamp
      labels
      annotations
    }
    spec {
      title
      tags
      categoryRef {
        name
        kind
      }
      options {
        name
        title
        values
      }
    }
    status {
      observedGeneration
      conditions {
        type
        status
        reason
        message
      }
    }
  }
}
```

### products

```graphql
query ListProducts {
  products(namespace: "gitstore-test", first: 10) {
    edges {
      cursor
      node {
        id
        metadata {
          name
        }
        spec {
          title
          tags
        }
      }
    }
    pageInfo {
      hasNextPage
      hasPreviousPage
      startCursor
      endCursor
    }
    totalCount
  }
}
```

### productVariant

```graphql
query GetProductVariant {
  productVariant(
    by: {
      namespacePath: {
        namespace: "gitstore-test"
        name: "macbook-pro-16-m4-64gb-1tb"
      }
    }
  ) {
    id
    apiVersion
    kind
    metadata {
      name
      namespace
      uid
      resourceVersion
      generation
    }
    spec {
      sku
      title
      productRef {
        name
      }
      selectedOptions {
        name
        value
      }
      pricing {
        priceSet {
          name
          prices {
            name
            currencyCode
            amount
            priority
            strategy {
              type
            }
          }
        }
      }
      inventory {
        managed
        policy
      }
    }
    status {
      conditions {
        type
        status
        reason
      }
      resolved {
        selectedOptionsHash
        priceSet {
          name
          priceCount
          currencies
          strategies
        }
      }
    }
  }
}
```

### productVariants

```graphql
query ListProductVariants {
  productVariants(namespace: "gitstore-test", first: 20) {
    edges {
      cursor
      node {
        id
        metadata {
          name
        }
        spec {
          sku
          title
        }
      }
    }
    totalCount
  }
}
```

### category

```graphql
query GetCategory {
  category(
    by: {
      namespacePath: {
        namespace: "gitstore-test"
        name: "laptops"
      }
    }
  ) {
    id
    apiVersion
    kind
    metadata {
      name
      namespace
    }
    spec {
      title
      parentRef {
        name
      }
    }
    path
    depth
    parent {
      metadata {
        name
      }
    }
    children {
      metadata {
        name
      }
    }
  }
}
```

### categories

```graphql
query ListCategories {
  categories(first: 20) {
    edges {
      cursor
      node {
        id
        metadata {
          name
        }
        spec {
          title
        }
        path
        depth
      }
    }
    totalCount
  }
}
```

### collection

```graphql
query GetCollection {
  collection(
    by: {
      namespacePath: {
        namespace: "gitstore-test"
        name: "featured-laptops"
      }
    }
  ) {
    id
    apiVersion
    kind
    metadata {
      name
      namespace
    }
    spec {
      title
      selector {
        matchLabels {
          key
          value
        }
      }
    }
    products(first: 10) {
      totalCount
    }
  }
}
```

### collections

```graphql
query ListCollections {
  collections(namespace: "gitstore-test", first: 20) {
    edges {
      cursor
      node {
        id
        metadata {
          name
        }
        spec {
          title
        }
        status {
          resolved {
            memberCount
          }
        }
      }
    }
    totalCount
  }
}
```

### catalogVersion

`catalogVersion` remains in the schema for continuity. Repository-scoped catalogue version semantics are still being clarified, so prefer resource queries for current catalogue state.

```graphql
query CatalogVersion {
  catalogVersion {
    tag
    commit
    publishedAt
    message
    stats {
      productCount
      categoryCount
      collectionCount
      orphanedReferences
    }
  }
}
```

## Mutation Operations

### login

See [Authentication](#authentication).

### logout

See [Authentication](#authentication).

### refreshToken

See [Authentication](#authentication).

### createNamespace

Creates a namespace. `ENTERPRISE` requires an admin token.

```graphql
mutation CreateNamespace {
  createNamespace(
    input: {
      clientMutationId: "create-gitstore-test"
      identifier: "gitstore-test"
      displayName: "GitStore Test"
      tier: USER
    }
  ) {
    clientMutationId
    namespace {
      id
      identifier
      displayName
      tier
    }
  }
}
```

Input fields:

| Field | Required | Notes |
|---|---|---|
| `identifier` | yes | Globally unique DNS-label namespace identifier |
| `displayName` | no | Human-friendly name |
| `tier` | yes | `USER`, `ORGANISATION`, or `ENTERPRISE` |
| `parentEnterpriseIdentifier` | no | Required only when linking an organisation to an enterprise |
| `clientMutationId` | no | Relay request tracking |

### deleteNamespace

Deletes an empty namespace. Deletion is blocked if repositories remain.

```graphql
mutation DeleteNamespace {
  deleteNamespace(
    input: {
      clientMutationId: "delete-gitstore-test"
      identifier: "gitstore-test"
    }
  ) {
    clientMutationId
    deletedIdentifier
  }
}
```

### createRepository

Creates a repository in a namespace.

```graphql
mutation CreateRepository($namespaceId: ID!) {
  createRepository(
    input: {
      clientMutationId: "create-catalog"
      namespaceId: $namespaceId
      name: "catalog"
      defaultBranch: "main"
    }
  ) {
    clientMutationId
    repository {
      id
      name
      defaultBranch
      storagePath
      namespace {
        identifier
      }
    }
  }
}
```

### renameRepository

```graphql
mutation RenameRepository($repositoryId: ID!) {
  renameRepository(
    input: {
      clientMutationId: "rename-catalog"
      repositoryId: $repositoryId
      newName: "summer-catalog"
    }
  ) {
    clientMutationId
    repository {
      id
      name
    }
  }
}
```

### transferRepository

```graphql
mutation TransferRepository($repositoryId: ID!, $targetNamespaceId: ID!) {
  transferRepository(
    input: {
      clientMutationId: "transfer-catalog"
      repositoryId: $repositoryId
      targetNamespaceId: $targetNamespaceId
    }
  ) {
    clientMutationId
    repository {
      id
      name
      namespace {
        identifier
      }
    }
  }
}
```

### deleteRepository

Deletes repository metadata and storage.

```graphql
mutation DeleteRepository($repositoryId: ID!) {
  deleteRepository(
    input: {
      clientMutationId: "delete-catalog"
      repositoryId: $repositoryId
    }
  ) {
    clientMutationId
    deletedRepositoryId
  }
}
```

## Catalogue Writes

Use Git for catalogue writes:

```bash
git add products variants categories collections
git commit -m "Update catalogue"
git push origin main
```

The API reference intentionally omits catalogue CRUD mutation docs. Some schema fields may remain for compatibility or transitional UI work, but they are not the supported write path for catalogue resources.

## Types

### Namespace

```graphql
type Namespace implements Node {
  id: ID!
  identifier: String!
  displayName: String
  tier: NamespaceTier!
  parentEnterpriseId: ID
  createdAt: DateTime!
  createdBy: String!
  updatedAt: DateTime!
  updatedBy: String!
}
```

### Repository

```graphql
type Repository implements Node {
  id: ID!
  name: String!
  namespace: Namespace!
  defaultBranch: String!
  storageClass: String!
  storagePath: String!
  createdAt: DateTime!
  createdBy: String!
  updatedAt: DateTime!
  updatedBy: String!
}
```

### Product

```graphql
type Product implements Node {
  id: ID!
  apiVersion: String!
  kind: String!
  metadata: ProductObjectMeta!
  spec: ProductSpec!
  status: ProductStatus
}
```

### ProductVariant

```graphql
type ProductVariant implements Node {
  id: ID!
  apiVersion: String!
  kind: String!
  metadata: ProductVariantObjectMeta!
  spec: ProductVariantSpec!
  status: ProductVariantStatus
  body: String
}
```

### Category

```graphql
type Category implements Node {
  id: ID!
  apiVersion: String
  kind: String
  metadata: CategoryObjectMeta!
  spec: CategorySpec!
  status: CategoryTaxonomyStatus
  body: String
  parent: Category
  children: [Category!]!
  products(first: Int, after: String, last: Int, before: String): ProductConnection!
  path: [String!]!
  depth: Int!
}
```

### Collection

```graphql
type Collection implements Node {
  id: ID!
  apiVersion: String
  kind: String
  metadata: CollectionObjectMeta!
  spec: CollectionSpec!
  status: CollectionStatus
  body: String
  products(first: Int, after: String, last: Int, before: String): ProductConnection!
}
```

## Scalars

| Scalar | Meaning |
|---|---|
| `DateTime` | ISO 8601 timestamp |
| `Decimal` | String-backed decimal for exact monetary values |
| `JSON` | Arbitrary JSON value |

## Pagination

Connection fields use Relay-style cursor pagination:

```graphql
query PageProducts($after: String) {
  products(namespace: "gitstore-test", first: 10, after: $after) {
    edges {
      cursor
      node {
        id
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

Use `first` + `after` for forward pagination and `last` + `before` for backward pagination.

## Error Handling

GraphQL errors use the standard response shape:

```json
{
  "errors": [
    {
      "message": "repository not found",
      "path": ["repository"],
      "extensions": {
        "code": "NOT_FOUND"
      }
    }
  ],
  "data": {
    "repository": null
  }
}
```

Single-resource queries return `null` when the resource is not found.

Common categories:

| Code | Meaning |
|---|---|
| `NOT_FOUND` | Requested resource does not exist |
| `VALIDATION_ERROR` | Input validation failed |
| `CONFLICT` | Requested change conflicts with current state |
| `INTERNAL_ERROR` | Server error |

## Related Docs

- [User Guide](user-guide.md)
- [Developer Guide](developer-guide.md)
- [Product Spec](products/product-spec.md)
- [ProductVariant Spec](products/product-variants.md)
- [CategoryTaxonomy Spec](categories/category-taxonomy.md)
- [Collection Spec](collections/collection-spec.md)
- [GraphQL schema files](../shared/schemas/)
