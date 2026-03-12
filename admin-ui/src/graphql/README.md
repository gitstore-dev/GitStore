# GraphQL Operations

This directory contains GraphQL operations (queries and mutations) and generated TypeScript types.

## Structure

```
src/graphql/
├── queries.graphql       # All GraphQL query operations
├── mutations.graphql     # All GraphQL mutation operations
├── generated.ts          # Auto-generated TypeScript types and hooks
├── codegen.yml           # GraphQL Code Generator configuration
└── README.md             # This file
```

## Usage

### 1. Define GraphQL Operations

Add your queries and mutations to the `.graphql` files:

**queries.graphql:**
```graphql
query GetProduct($sku: String!) {
  product(sku: $sku) {
    id
    title
    sku
    price
  }
}
```

**mutations.graphql:**
```graphql
mutation CreateProduct($input: CreateProductInput!) {
  createProduct(input: $input) {
    product {
      id
      title
      sku
    }
  }
}
```

### 2. Generate TypeScript Types

Run the code generator:

```bash
npm run codegen
```

This will generate:
- TypeScript types for all GraphQL schema types
- React hooks for all queries and mutations
- Type-safe operation types

### 3. Use Generated Hooks

Import and use the generated hooks in your components:

```tsx
import { useGetProductQuery, useCreateProductMutation } from '../graphql/generated';

function ProductPage({ sku }: { sku: string }) {
  // Query hook
  const { data, loading, error } = useGetProductQuery({
    variables: { sku },
  });

  // Mutation hook
  const [createProduct, { loading: creating }] = useCreateProductMutation();

  const handleCreate = async () => {
    await createProduct({
      variables: {
        input: {
          title: 'New Product',
          sku: 'SKU-001',
          price: '19.99',
          inventoryStatus: 'IN_STOCK',
          inventoryQuantity: 100,
        },
      },
    });
  };

  if (loading) return <div>Loading...</div>;
  if (error) return <div>Error: {error.message}</div>;

  return (
    <div>
      <h1>{data?.product?.title}</h1>
      <p>SKU: {data?.product?.sku}</p>
      <p>Price: ${data?.product?.price}</p>
      <button onClick={handleCreate} disabled={creating}>
        Create Product
      </button>
    </div>
  );
}
```

## Generated Hooks

All hooks follow the naming pattern:
- **Queries**: `use[OperationName]Query`
  - Example: `GetProducts` → `useGetProductsQuery`
- **Mutations**: `use[OperationName]Mutation`
  - Example: `CreateProduct` → `useCreateProductMutation`

## Available Operations

### Queries
- `GetProducts` - List products with pagination and filtering
- `GetProduct` - Get single product by SKU
- `GetProductById` - Get single product by ID
- `GetCategories` - List all categories
- `GetCategory` - Get single category by slug
- `GetCategoryById` - Get single category by ID
- `GetCollections` - List all collections
- `GetCollection` - Get single collection by slug
- `GetCollectionById` - Get single collection by ID
- `GetCatalogVersion` - Get current catalog version info

### Mutations
- `CreateProduct` - Create new product
- `UpdateProduct` - Update existing product (with optimistic locking)
- `DeleteProduct` - Delete product
- `CreateCategory` - Create new category
- `UpdateCategory` - Update existing category (with optimistic locking)
- `DeleteCategory` - Delete category
- `ReorderCategories` - Reorder categories (drag-and-drop)
- `CreateCollection` - Create new collection
- `UpdateCollection` - Update existing collection (with optimistic locking)
- `DeleteCollection` - Delete collection
- `ReorderCollections` - Reorder collections (drag-and-drop)
- `PublishCatalog` - Publish changes (commit + push + tag)

## Optimistic Locking

Update mutations include version-based optimistic locking:

```tsx
const [updateProduct] = useUpdateProductMutation();

await updateProduct({
  variables: {
    input: {
      id: 'prod_abc123',
      version: currentVersion, // Required for optimistic locking
      title: 'Updated Title',
    },
  },
});
```

If the version doesn't match (concurrent modification), the mutation returns a `conflict` with:
- `field` - The conflicting field name
- `currentValue` - Current value in the database
- `incomingValue` - Value you tried to set
- `diff` - Unified diff for resolution

## Relay Pattern

All mutations follow the Relay pattern with:
- Input object with `clientMutationId` for request tracking
- Payload object with `clientMutationId` echoed back

```tsx
const [createProduct] = useCreateProductMutation();

await createProduct({
  variables: {
    input: {
      clientMutationId: 'unique-request-id',
      title: 'New Product',
      // ... other fields
    },
  },
});
```

## Configuration

The code generator is configured in `codegen.yml`:
- **Schema source**: `../shared/schemas/*.graphql`
- **Operations source**: `src/graphql/**/*.graphql`
- **Output**: `src/graphql/generated.ts`

### Custom Scalars

Custom scalars are mapped to TypeScript types:
- `DateTime` → `string` (ISO 8601 format)
- `Decimal` → `string` (to preserve precision)
- `JSON` → `any` (arbitrary JSON data)

## Troubleshooting

### Regenerating Types

If types are out of sync with schema:
```bash
npm run codegen
```

### Schema Not Found

Make sure GraphQL schema files exist in `../shared/schemas/`:
```bash
ls -la ../shared/schemas/*.graphql
```

### TypeScript Errors

After running codegen, restart your TypeScript server:
- VS Code: `Cmd+Shift+P` → "TypeScript: Restart TS Server"
- Or restart your editor

## Further Reading

- [GraphQL Code Generator Docs](https://www.the-guild.dev/graphql/codegen/docs/getting-started)
- [Apollo Client React Hooks](https://www.apollographql.com/docs/react/data/queries/)
- [Relay Specification](https://relay.dev/docs/guides/graphql-server-specification/)
