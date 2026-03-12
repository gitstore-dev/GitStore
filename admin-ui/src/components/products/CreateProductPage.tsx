import React, { useState } from 'react';
import { ProductForm } from './ProductForm';

// Placeholder types until codegen runs
interface Product {
  id?: string;
  title: string;
  sku: string;
  price: string;
  compareAtPrice?: string | null;
  inventoryStatus: string;
  inventoryQuantity: number;
  description?: string | null;
  categoryId?: string | null;
  collectionIds: string[];
  images: string[];
  metadata?: any;
  version?: string;
}

/**
 * Create product page component
 * Handles product creation with GraphQL mutation
 */
export function CreateProductPage() {
  const [isLoading, setIsLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);

  // TODO: Replace with actual GraphQL mutation when codegen runs
  // const [createProduct, { loading, error }] = useCreateProductMutation();

  const handleSubmit = async (product: Product) => {
    setIsLoading(true);
    setError(null);

    try {
      // TODO: Use GraphQL mutation
      // const result = await createProduct({
      //   variables: {
      //     input: {
      //       clientMutationId: uuidv4(),
      //       title: product.title,
      //       sku: product.sku,
      //       price: product.price,
      //       compareAtPrice: product.compareAtPrice,
      //       inventoryStatus: product.inventoryStatus,
      //       inventoryQuantity: product.inventoryQuantity,
      //       description: product.description,
      //       categoryId: product.categoryId,
      //       collectionIds: product.collectionIds,
      //       images: product.images,
      //       metadata: product.metadata,
      //     },
      //   },
      // });

      // Simulate API call
      console.log('Creating product:', product);
      await new Promise(resolve => setTimeout(resolve, 1000));

      // Redirect to product list on success
      window.location.href = '/products';
    } catch (err) {
      console.error('Failed to create product:', err);
      setError(err instanceof Error ? err.message : 'Failed to create product');
      setIsLoading(false);
    }
  };

  const handleCancel = () => {
    // Navigate back to product list
    window.location.href = '/products';
  };

  return (
    <div style={styles.container}>
      {error && (
        <div style={styles.errorBanner}>
          <strong>Error:</strong> {error}
        </div>
      )}

      <ProductForm
        onSubmit={handleSubmit}
        onCancel={handleCancel}
        isLoading={isLoading}
      />
    </div>
  );
}

const styles = {
  container: {
    padding: '2rem',
    maxWidth: '1440px',
    margin: '0 auto',
  } as React.CSSProperties,
  errorBanner: {
    padding: '1rem',
    marginBottom: '1.5rem',
    backgroundColor: '#fed7d7',
    color: '#c53030',
    borderRadius: '4px',
    borderLeft: '4px solid #e53e3e',
  } as React.CSSProperties,
};
