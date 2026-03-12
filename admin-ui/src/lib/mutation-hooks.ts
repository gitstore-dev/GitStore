/**
 * Custom hooks for mutations with optimistic updates
 * These hooks wrap Apollo useMutation with optimistic UI updates
 *
 * Usage example:
 *
 * const [createProduct] = useCreateProductWithOptimistic();
 *
 * await createProduct({
 *   variables: {
 *     input: {
 *       title: 'New Product',
 *       price: 99.99,
 *       ...
 *     }
 *   }
 * });
 */

import { useMutation, MutationHookOptions } from '@apollo/client';
import {
  optimisticCreateProduct,
  optimisticUpdateProduct,
  optimisticCreateCategory,
  optimisticUpdateCategory,
  optimisticCreateCollection,
  optimisticUpdateCollection,
  optimisticReorderCategories,
  optimisticReorderCollections,
  updateCacheAfterCreateProduct,
  updateCacheAfterDeleteProduct,
  updateCacheAfterDeleteCategory,
  updateCacheAfterDeleteCollection,
} from './optimistic-updates';

// TODO: Replace with generated types and documents from codegen
// import {
//   CreateProductDocument,
//   UpdateProductDocument,
//   DeleteProductDocument,
//   CreateCategoryDocument,
//   etc.
// } from '../generated/graphql';

/**
 * Hook for createProduct mutation with optimistic UI
 */
export function useCreateProductWithOptimistic(options?: MutationHookOptions<any, any>) {
  return useMutation<any, any>(
    // TODO: Replace with CreateProductDocument from codegen
    require('@apollo/client').gql`
      mutation CreateProduct($input: CreateProductInput!) {
        createProduct(input: $input) {
          clientMutationId
          product {
            id
            title
            slug
            sku
            price
            inventory
            status
            version
          }
        }
      }
    `,
    {
      ...options,
      optimisticResponse: (variables) => ({
        createProduct: {
          __typename: 'CreateProductPayload',
          clientMutationId: variables.input.clientMutationId,
          product: optimisticCreateProduct(variables.input),
        },
      }),
      update: (cache, { data }) => {
        if (data?.createProduct?.product) {
          updateCacheAfterCreateProduct(cache, data.createProduct.product);
        }
      },
    }
  );
}

/**
 * Hook for updateProduct mutation with optimistic UI
 */
export function useUpdateProductWithOptimistic(
  currentProduct: any,
  options?: MutationHookOptions<any, any>
) {
  return useMutation<any, any>(
    // TODO: Replace with UpdateProductDocument from codegen
    require('@apollo/client').gql`
      mutation UpdateProduct($input: UpdateProductInput!) {
        updateProduct(input: $input) {
          clientMutationId
          product {
            id
            title
            slug
            sku
            price
            inventory
            status
            version
          }
        }
      }
    `,
    {
      ...options,
      optimisticResponse: (variables) => ({
        updateProduct: {
          __typename: 'UpdateProductPayload',
          clientMutationId: variables.input.clientMutationId,
          product: optimisticUpdateProduct(currentProduct, variables.input),
        },
      }),
    }
  );
}

/**
 * Hook for deleteProduct mutation with optimistic UI
 */
export function useDeleteProductWithOptimistic(options?: MutationHookOptions<any, any>) {
  return useMutation<any, any>(
    // TODO: Replace with DeleteProductDocument from codegen
    require('@apollo/client').gql`
      mutation DeleteProduct($input: DeleteProductInput!) {
        deleteProduct(input: $input) {
          clientMutationId
          success
        }
      }
    `,
    {
      ...options,
      update: (cache, { data }, { variables }) => {
        if (data?.deleteProduct?.success) {
          updateCacheAfterDeleteProduct(cache, variables.input.id);
        }
      },
    }
  );
}

/**
 * Hook for createCategory mutation with optimistic UI
 */
export function useCreateCategoryWithOptimistic(options?: MutationHookOptions<any, any>) {
  return useMutation<any, any>(
    // TODO: Replace with CreateCategoryDocument from codegen
    require('@apollo/client').gql`
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
          }
        }
      }
    `,
    {
      ...options,
      optimisticResponse: (variables) => ({
        createCategory: {
          __typename: 'CreateCategoryPayload',
          clientMutationId: variables.input.clientMutationId,
          category: optimisticCreateCategory(variables.input),
        },
      }),
    }
  );
}

/**
 * Hook for updateCategory mutation with optimistic UI
 */
export function useUpdateCategoryWithOptimistic(
  currentCategory: any,
  options?: MutationHookOptions<any, any>
) {
  return useMutation<any, any>(
    // TODO: Replace with UpdateCategoryDocument from codegen
    require('@apollo/client').gql`
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
          }
        }
      }
    `,
    {
      ...options,
      optimisticResponse: (variables) => ({
        updateCategory: {
          __typename: 'UpdateCategoryPayload',
          clientMutationId: variables.input.clientMutationId,
          category: optimisticUpdateCategory(currentCategory, variables.input),
        },
      }),
    }
  );
}

/**
 * Hook for deleteCategory mutation with optimistic UI
 */
export function useDeleteCategoryWithOptimistic(options?: MutationHookOptions<any, any>) {
  return useMutation<any, any>(
    // TODO: Replace with DeleteCategoryDocument from codegen
    require('@apollo/client').gql`
      mutation DeleteCategory($input: DeleteCategoryInput!) {
        deleteCategory(input: $input) {
          clientMutationId
          success
        }
      }
    `,
    {
      ...options,
      update: (cache, { data }, { variables }) => {
        if (data?.deleteCategory?.success) {
          updateCacheAfterDeleteCategory(cache, variables.input.id);
        }
      },
    }
  );
}

/**
 * Hook for reorderCategories mutation with optimistic UI
 */
export function useReorderCategoriesWithOptimistic(
  currentCategories: any[],
  options?: MutationHookOptions<any, any>
) {
  return useMutation<any, any>(
    // TODO: Replace with ReorderCategoriesDocument from codegen
    require('@apollo/client').gql`
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
    `,
    {
      ...options,
      optimisticResponse: (variables) => ({
        reorderCategories: {
          __typename: 'ReorderCategoriesPayload',
          clientMutationId: variables.input.clientMutationId,
          categories: optimisticReorderCategories(
            currentCategories,
            variables.input.categoryIds
          ),
        },
      }),
    }
  );
}

/**
 * Hook for createCollection mutation with optimistic UI
 */
export function useCreateCollectionWithOptimistic(options?: MutationHookOptions<any, any>) {
  return useMutation<any, any>(
    // TODO: Replace with CreateCollectionDocument from codegen
    require('@apollo/client').gql`
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
          }
        }
      }
    `,
    {
      ...options,
      optimisticResponse: (variables) => ({
        createCollection: {
          __typename: 'CreateCollectionPayload',
          clientMutationId: variables.input.clientMutationId,
          collection: optimisticCreateCollection(variables.input),
        },
      }),
    }
  );
}

/**
 * Hook for updateCollection mutation with optimistic UI
 */
export function useUpdateCollectionWithOptimistic(
  currentCollection: any,
  options?: MutationHookOptions<any, any>
) {
  return useMutation<any, any>(
    // TODO: Replace with UpdateCollectionDocument from codegen
    require('@apollo/client').gql`
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
          }
        }
      }
    `,
    {
      ...options,
      optimisticResponse: (variables) => ({
        updateCollection: {
          __typename: 'UpdateCollectionPayload',
          clientMutationId: variables.input.clientMutationId,
          collection: optimisticUpdateCollection(currentCollection, variables.input),
        },
      }),
    }
  );
}

/**
 * Hook for deleteCollection mutation with optimistic UI
 */
export function useDeleteCollectionWithOptimistic(options?: MutationHookOptions<any, any>) {
  return useMutation<any, any>(
    // TODO: Replace with DeleteCollectionDocument from codegen
    require('@apollo/client').gql`
      mutation DeleteCollection($input: DeleteCollectionInput!) {
        deleteCollection(input: $input) {
          clientMutationId
          success
        }
      }
    `,
    {
      ...options,
      update: (cache, { data }, { variables }) => {
        if (data?.deleteCollection?.success) {
          updateCacheAfterDeleteCollection(cache, variables.input.id);
        }
      },
    }
  );
}

/**
 * Hook for reorderCollections mutation with optimistic UI
 */
export function useReorderCollectionsWithOptimistic(
  currentCollections: any[],
  options?: MutationHookOptions<any, any>
) {
  return useMutation<any, any>(
    // TODO: Replace with ReorderCollectionsDocument from codegen
    require('@apollo/client').gql`
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
    `,
    {
      ...options,
      optimisticResponse: (variables) => ({
        reorderCollections: {
          __typename: 'ReorderCollectionsPayload',
          clientMutationId: variables.input.clientMutationId,
          collections: optimisticReorderCollections(
            currentCollections,
            variables.input.collectionIds
          ),
        },
      }),
    }
  );
}
