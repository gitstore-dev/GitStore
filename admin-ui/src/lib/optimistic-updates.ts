import { ApolloCache } from '@apollo/client';

// TODO: Replace with generated types from codegen
interface Product {
  __typename: 'Product';
  id: string;
  title: string;
  slug: string;
  sku?: string | null;
  price: number;
  inventory: number;
  status: string;
  version: string;
  [key: string]: any;
}

interface Category {
  __typename: 'Category';
  id: string;
  name: string;
  slug: string;
  description?: string | null;
  parentId?: string | null;
  displayOrder: number;
  children?: Category[];
  version: string;
  [key: string]: any;
}

interface Collection {
  __typename: 'Collection';
  id: string;
  name: string;
  slug: string;
  description?: string | null;
  productIds: string[];
  displayOrder: number;
  version: string;
  [key: string]: any;
}

/**
 * Optimistic response for createProduct mutation
 */
export function optimisticCreateProduct(input: any): Product {
  return {
    __typename: 'Product',
    id: `temp-${Date.now()}`, // Temporary ID
    title: input.title,
    slug: input.slug,
    sku: input.sku || null,
    price: input.price,
    inventory: input.inventory || 0,
    status: input.status || 'draft',
    version: 'optimistic',
    description: input.description || null,
    images: input.images || [],
    categoryIds: input.categoryIds || [],
    metadata: input.metadata || {},
    createdAt: new Date().toISOString(),
    updatedAt: new Date().toISOString(),
  };
}

/**
 * Optimistic response for updateProduct mutation
 */
export function optimisticUpdateProduct(product: Product, updates: any): Product {
  return {
    ...product,
    ...updates,
    version: 'optimistic', // Will be replaced by server response
    updatedAt: new Date().toISOString(),
  };
}

/**
 * Update cache after createProduct mutation
 */
export function updateCacheAfterCreateProduct(
  cache: ApolloCache<any>,
  product: Product
) {
  // TODO: Update products query cache
  // This would read the existing products query and append the new product
  // For now, we'll let the cache handle it automatically via __typename
  cache.modify({
    fields: {
      products(existingProducts = { edges: [] }) {
        const newProductRef = cache.writeFragment({
          data: product,
          fragment: require('@apollo/client').gql`
            fragment NewProduct on Product {
              id
              title
              slug
              sku
              price
              inventory
              status
              version
            }
          `,
        });

        return {
          ...existingProducts,
          edges: [
            { __typename: 'ProductEdge', node: newProductRef, cursor: product.id },
            ...existingProducts.edges,
          ],
        };
      },
    },
  });
}

/**
 * Update cache after deleteProduct mutation
 */
export function updateCacheAfterDeleteProduct(
  cache: ApolloCache<any>,
  productId: string
) {
  cache.evict({ id: cache.identify({ __typename: 'Product', id: productId }) });
  cache.gc();
}

/**
 * Optimistic response for createCategory mutation
 */
export function optimisticCreateCategory(input: any): Category {
  return {
    __typename: 'Category',
    id: `temp-${Date.now()}`,
    name: input.name,
    slug: input.slug,
    description: input.description || null,
    parentId: input.parentId || null,
    displayOrder: input.displayOrder || 0,
    children: [],
    version: 'optimistic',
    createdAt: new Date().toISOString(),
    updatedAt: new Date().toISOString(),
  };
}

/**
 * Optimistic response for updateCategory mutation
 */
export function optimisticUpdateCategory(category: Category, updates: any): Category {
  return {
    ...category,
    ...updates,
    version: 'optimistic',
    updatedAt: new Date().toISOString(),
  };
}

/**
 * Update cache after deleteCategory mutation
 */
export function updateCacheAfterDeleteCategory(
  cache: ApolloCache<any>,
  categoryId: string
) {
  cache.evict({ id: cache.identify({ __typename: 'Category', id: categoryId }) });
  cache.gc();
}

/**
 * Optimistic response for reorderCategories mutation
 */
export function optimisticReorderCategories(
  categories: Category[],
  reorderedIds: string[]
): Category[] {
  const idToCategory = new Map(categories.map(cat => [cat.id, cat]));

  return reorderedIds.map((id, index) => {
    const category = idToCategory.get(id);
    if (!category) return null;

    return {
      ...category,
      displayOrder: index,
      version: 'optimistic',
      updatedAt: new Date().toISOString(),
    };
  }).filter(Boolean) as Category[];
}

/**
 * Optimistic response for createCollection mutation
 */
export function optimisticCreateCollection(input: any): Collection {
  return {
    __typename: 'Collection',
    id: `temp-${Date.now()}`,
    name: input.name,
    slug: input.slug,
    description: input.description || null,
    productIds: input.productIds || [],
    displayOrder: input.displayOrder || 0,
    version: 'optimistic',
    createdAt: new Date().toISOString(),
    updatedAt: new Date().toISOString(),
  };
}

/**
 * Optimistic response for updateCollection mutation
 */
export function optimisticUpdateCollection(collection: Collection, updates: any): Collection {
  return {
    ...collection,
    ...updates,
    version: 'optimistic',
    updatedAt: new Date().toISOString(),
  };
}

/**
 * Update cache after deleteCollection mutation
 */
export function updateCacheAfterDeleteCollection(
  cache: ApolloCache<any>,
  collectionId: string
) {
  cache.evict({ id: cache.identify({ __typename: 'Collection', id: collectionId }) });
  cache.gc();
}

/**
 * Optimistic response for reorderCollections mutation
 */
export function optimisticReorderCollections(
  collections: Collection[],
  reorderedIds: string[]
): Collection[] {
  const idToCollection = new Map(collections.map(coll => [coll.id, coll]));

  return reorderedIds.map((id, index) => {
    const collection = idToCollection.get(id);
    if (!collection) return null;

    return {
      ...collection,
      displayOrder: index,
      version: 'optimistic',
      updatedAt: new Date().toISOString(),
    };
  }).filter(Boolean) as Collection[];
}

/**
 * Helper to generate optimistic response for any mutation
 * This is a generic helper that can be used when specific helpers aren't needed
 */
export function optimisticMutationResponse<T>(
  typename: string,
  input: any,
  existingData?: T
): T {
  return {
    __typename: typename,
    ...existingData,
    ...input,
    version: 'optimistic',
    updatedAt: new Date().toISOString(),
  } as T;
}

/**
 * Rollback helper for failed optimistic updates
 * This would typically be called in the onError callback of a mutation
 */
export function rollbackOptimisticUpdate(
  cache: ApolloCache<any>,
  typename: string,
  id: string,
  previousData: any
) {
  cache.writeFragment({
    id: cache.identify({ __typename: typename, id }),
    fragment: require('@apollo/client').gql`
      fragment RollbackData on ${typename} {
        id
        version
      }
    `,
    data: previousData,
  });
}
