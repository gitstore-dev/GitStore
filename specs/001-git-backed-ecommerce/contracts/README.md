# GitStore GraphQL Contracts

**Status**: Draft (API-First Design Phase)
**Created**: 2026-03-09
**GraphQL Specification**: Relay-compatible

## Overview

This directory contains the GraphQL schema definitions for the GitStore API. Following the **API-First Design** principle (Constitution §II), these contracts are defined **before implementation** and serve as the contract between the GraphQL API server (Go) and clients (Admin UI, Storefront).

## Schema Files

| File | Description |
|------|-------------|
| `schema.graphql` | Root schema with Query/Mutation roots, Relay Node interface, scalars, enums |
| `product.graphql` | Product entity types, connections, and mutations |
| `category.graphql` | Category entity types (hierarchical), connections, and mutations |
| `collection.graphql` | Collection entity types, connections, publish mutation |

## Relay Specification Compliance

### Node Interface
All entities implement `Node` interface with globally unique IDs:
- Format: `[prefix]_[base62]` (e.g., `prod_abc123xyz`)
- Queryable via `node(id: ID!)` and `nodes(ids: [ID!]!)`

### Connection Pattern
Cursor-based pagination for lists:
- `edges`: List of edge objects with `cursor` and `node`
- `pageInfo`: Pagination metadata (`hasNextPage`, `hasPreviousPage`, cursors)
- `first/after` for forward pagination, `last/before` for backward

### Mutation Pattern
Standardized mutation input/payload structure:
- Input: `clientMutationId` for request tracking
- Payload: `clientMutationId` echo, entity result, errors array

## Key Features

### Optimistic Locking
All update mutations include `version: DateTime!` field for conflict detection:
- Client sends last known `updated_at` timestamp
- Server compares with current version
- If mismatch, returns `OptimisticLockConflict` with diff

### Error Handling
GraphQL's built-in error handling is used. Mutations that fail will return errors in the standard GraphQL `errors` response field:
```json
{
  "data": { "createProduct": null },
  "errors": [
    {
      "message": "SKU LAPTOP-001 already exists",
      "locations": [{"line": 2, "column": 3}],
      "path": ["createProduct"],
      "extensions": {
        "code": "VALIDATION_ERROR",
        "field": "sku"
      }
    }
  ]
}
```

### Filtering & Search
`ProductFilter` input supports:
- Category/collection filtering
- Price range filtering
- Inventory status filtering
- Full-text search (title, description, SKU)

## Example Queries

### Fetch Products by Category
```graphql
query ProductsByCategory($categoryId: ID!) {
  products(first: 20, filter: { categoryId: $categoryId }) {
    edges {
      cursor
      node {
        id
        sku
        title
        price
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

### Fetch Category Tree
```graphql
query CategoryTree {
  categories {
    id
    name
    slug
    depth
    path
    children {
      id
      name
      slug
    }
  }
}
```

### Create Product
```graphql
mutation CreateProduct($input: CreateProductInput!) {
  createProduct(input: $input) {
    clientMutationId
    product {
      id
      sku
      title
    }
  }
}
```

### Update with Optimistic Locking
```graphql
mutation UpdateProduct($input: UpdateProductInput!) {
  updateProduct(input: $input) {
    clientMutationId
    product {
      id
      title
      updatedAt
    }
    conflict {
      currentVersion
      attemptedVersion
      diff
    }
  }
}
```

### Publish Catalog
```graphql
mutation PublishCatalog($input: PublishCatalogInput!) {
  publishCatalog(input: $input) {
    clientMutationId
    catalogVersion {
      tag
      commit
      publishedAt
      stats {
        productCount
        categoryCount
        collectionCount
        orphanedReferences
      }
    }
  }
}
```

## Code Generation

### Go Server (gqlgen)
```bash
cd api
go run github.com/99designs/gqlgen generate
```

Configuration in `gqlgen.yml`:
```yaml
schema:
  - ../specs/001-git-backed-ecommerce/contracts/*.graphql
exec:
  filename: internal/graph/generated.go
model:
  filename: internal/graph/model/models_gen.go
resolver:
  filename: internal/graph/resolvers.go
```

### TypeScript Client (graphql-codegen)
```bash
cd admin-ui
npm run codegen
```

Configuration in `codegen.yml`:
```yaml
schema: ../specs/001-git-backed-ecommerce/contracts/*.graphql
generates:
  src/graphql/generated.ts:
    plugins:
      - typescript
      - typescript-operations
      - typescript-react-apollo
```

## Validation Rules (from data-model.md)

### Product
- SKU must be unique
- `price` must be positive, 2 decimal places
- `categoryId` must reference existing category (hard constraint)
- `collectionIds` should reference existing collections (soft constraint, orphans allowed)

### Category
- `slug` must be unique
- `parentId` must reference existing category or be null
- No circular parent references
- Max tree depth: 5 levels

### Collection
- `slug` must be unique
- `productIds` should reference existing products (soft constraint)
- Empty collections allowed

## Contract Evolution Strategy

### Backward Compatible Changes (MINOR version)
- Add new optional fields
- Add new queries/mutations
- Add new enum values (non-breaking)

### Breaking Changes (MAJOR version)
- Remove fields/types
- Change field types
- Make optional fields required
- Remove enum values

### Deprecation Process
1. Mark field as `@deprecated(reason: "Use X instead")`
2. Document in migration guide
3. Remove in next MAJOR version (min 6 months notice)

## Next Steps

1. **Review**: Stakeholder review of contracts ✅
2. **Generate**: Run code generation for Go resolvers
3. **Implement**: TDD approach - write contract tests first
4. **Validate**: Contract tests verify schema compliance
5. **Document**: Update quickstart.md with example queries

## References

- [GraphQL Spec](https://spec.graphql.org/)
- [Relay Specification](https://relay.dev/docs/guides/graphql-server-specification/)
- [gqlgen Documentation](https://gqlgen.com/)
- Constitution §II: API-First Design
- Constitution §III: Clear Contracts & Versioning
