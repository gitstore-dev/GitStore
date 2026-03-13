/**
 * Custom hooks for mutations with urql
 * These hooks wrap urql useMutation for consistent error handling
 *
 * Usage example:
 *
 * const [createProductResult, createProduct] = useCreateProduct();
 *
 * await createProduct({
 *   input: {
 *     title: 'New Product',
 *     price: 99.99,
 *     ...
 *   }
 * });
 */

import { useMutation } from 'urql';

// TODO: Replace with generated types and documents from codegen
// import {
//   CreateProductDocument,
//   UpdateProductDocument,
//   DeleteProductDocument,
//   CreateCategoryDocument,
//   etc.
// } from '../generated/graphql';

/**
 * Hook for createProduct mutation
 */
export function useCreateProduct() {
  return useMutation(`
    mutation CreateProduct($input: CreateProductInput!) {
      createProduct(input: $input) {
        clientMutationId
        product {
          id
          title
          slug
          sku
          price
          currency
          body
          inventoryStatus
          inventoryQuantity
          images
          metadata
          version
          createdAt
          updatedAt
        }
      }
    }
  `);
}

/**
 * Hook for updateProduct mutation
 */
export function useUpdateProduct() {
  return useMutation(`
    mutation UpdateProduct($input: UpdateProductInput!) {
      updateProduct(input: $input) {
        clientMutationId
        product {
          id
          title
          slug
          sku
          price
          currency
          body
          inventoryStatus
          inventoryQuantity
          images
          metadata
          version
          createdAt
          updatedAt
        }
      }
    }
  `);
}

/**
 * Hook for deleteProduct mutation
 */
export function useDeleteProduct() {
  return useMutation(`
    mutation DeleteProduct($input: DeleteProductInput!) {
      deleteProduct(input: $input) {
        clientMutationId
        success
      }
    }
  `);
}

/**
 * Hook for createCategory mutation
 */
export function useCreateCategory() {
  return useMutation(`
    mutation CreateCategory($input: CreateCategoryInput!) {
      createCategory(input: $input) {
        clientMutationId
        category {
          id
          name
          slug
          description
          parentId
          displayOrder
          version
          createdAt
          updatedAt
        }
      }
    }
  `);
}

/**
 * Hook for updateCategory mutation
 */
export function useUpdateCategory() {
  return useMutation(`
    mutation UpdateCategory($input: UpdateCategoryInput!) {
      updateCategory(input: $input) {
        clientMutationId
        category {
          id
          name
          slug
          description
          parentId
          displayOrder
          version
          createdAt
          updatedAt
        }
      }
    }
  `);
}

/**
 * Hook for deleteCategory mutation
 */
export function useDeleteCategory() {
  return useMutation(`
    mutation DeleteCategory($input: DeleteCategoryInput!) {
      deleteCategory(input: $input) {
        clientMutationId
        success
      }
    }
  `);
}

/**
 * Hook for reorderCategories mutation
 */
export function useReorderCategories() {
  return useMutation(`
    mutation ReorderCategories($input: ReorderCategoriesInput!) {
      reorderCategories(input: $input) {
        clientMutationId
        categories {
          id
          displayOrder
          version
        }
      }
    }
  `);
}

/**
 * Hook for createCollection mutation
 */
export function useCreateCollection() {
  return useMutation(`
    mutation CreateCollection($input: CreateCollectionInput!) {
      createCollection(input: $input) {
        clientMutationId
        collection {
          id
          name
          slug
          description
          productIds
          displayOrder
          version
          createdAt
          updatedAt
        }
      }
    }
  `);
}

/**
 * Hook for updateCollection mutation
 */
export function useUpdateCollection() {
  return useMutation(`
    mutation UpdateCollection($input: UpdateCollectionInput!) {
      updateCollection(input: $input) {
        clientMutationId
        collection {
          id
          name
          slug
          description
          productIds
          displayOrder
          version
          createdAt
          updatedAt
        }
      }
    }
  `);
}

/**
 * Hook for deleteCollection mutation
 */
export function useDeleteCollection() {
  return useMutation(`
    mutation DeleteCollection($input: DeleteCollectionInput!) {
      deleteCollection(input: $input) {
        clientMutationId
        success
      }
    }
  `);
}

/**
 * Hook for reorderCollections mutation
 */
export function useReorderCollections() {
  return useMutation(`
    mutation ReorderCollections($input: ReorderCollectionsInput!) {
      reorderCollections(input: $input) {
        clientMutationId
        collections {
          id
          displayOrder
          version
        }
      }
    }
  `);
}
