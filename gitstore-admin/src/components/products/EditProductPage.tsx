import React, { useState, useEffect } from 'react';
import { ProductForm } from './ProductForm';
import { ConflictModal } from '../shared/ConflictModal';

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

interface Conflict {
  field: string;
  currentValue: string;
  incomingValue: string;
  diff?: string;
}

interface EditProductPageProps {
  productId: string;
}

/**
 * Edit product page component
 * Handles product updates with optimistic locking and conflict resolution
 */
export function EditProductPage({ productId }: EditProductPageProps) {
  const [product, setProduct] = useState<Product | null>(null);
  const [isLoading, setIsLoading] = useState(false);
  const [isFetching, setIsFetching] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [conflict, setConflict] = useState<Conflict | null>(null);
  const [pendingUpdate, setPendingUpdate] = useState<Product | null>(null);

  // TODO: Replace with actual GraphQL query when codegen runs
  // const { data, loading, error } = useGetProductByIdQuery({
  //   variables: { id: productId },
  // });
  // const [updateProduct, { loading: updating }] = useUpdateProductMutation();

  // Load product data
  useEffect(() => {
    const loadProduct = async () => {
      setIsFetching(true);
      setError(null);

      try {
        // TODO: Use GraphQL query
        // const result = await client.query({
        //   query: GetProductByIdDocument,
        //   variables: { id: productId },
        // });

        // Simulate API call with mock data
        console.log('Loading product:', productId);
        await new Promise(resolve => setTimeout(resolve, 500));

        const mockProduct: Product = {
          id: productId,
          title: 'Sample Product',
          sku: 'SKU-001',
          price: '29.99',
          compareAtPrice: '39.99',
          inventoryStatus: 'IN_STOCK',
          inventoryQuantity: 100,
          description: 'This is a sample product description',
          categoryId: 'cat_1',
          collectionIds: ['coll_1'],
          images: [],
          version: 'v1',
        };

        setProduct(mockProduct);
      } catch (err) {
        console.error('Failed to load product:', err);
        setError(err instanceof Error ? err.message : 'Failed to load product');
      } finally {
        setIsFetching(false);
      }
    };

    loadProduct();
  }, [productId]);

  const handleSubmit = async (updatedProduct: Product) => {
    setIsLoading(true);
    setError(null);
    setConflict(null);

    try {
      // TODO: Use GraphQL mutation
      // const result = await updateProduct({
      //   variables: {
      //     input: {
      //       clientMutationId: uuidv4(),
      //       id: productId,
      //       version: product.version, // Optimistic lock version
      //       title: updatedProduct.title,
      //       price: updatedProduct.price,
      //       compareAtPrice: updatedProduct.compareAtPrice,
      //       inventoryStatus: updatedProduct.inventoryStatus,
      //       inventoryQuantity: updatedProduct.inventoryQuantity,
      //       description: updatedProduct.description,
      //       categoryId: updatedProduct.categoryId,
      //       collectionIds: updatedProduct.collectionIds,
      //       images: updatedProduct.images,
      //       metadata: updatedProduct.metadata,
      //     },
      //   },
      // });

      // Simulate API call
      console.log('Updating product:', updatedProduct);
      await new Promise(resolve => setTimeout(resolve, 1000));

      // Simulate conflict detection (20% chance)
      const hasConflict = Math.random() < 0.2;

      if (hasConflict) {
        // Simulate a conflict response
        const mockConflict: Conflict = {
          field: 'title',
          currentValue: 'Sample Product (Modified by another user)',
          incomingValue: updatedProduct.title,
          diff: `- Sample Product (Modified by another user)\n+ ${updatedProduct.title}`,
        };

        setConflict(mockConflict);
        setPendingUpdate(updatedProduct);
        setIsLoading(false);
        return;
      }

      // Success - redirect to product list
      window.location.href = '/products';
    } catch (err) {
      console.error('Failed to update product:', err);
      setError(err instanceof Error ? err.message : 'Failed to update product');
      setIsLoading(false);
    }
  };

  const handleCancel = () => {
    // Navigate back to product list
    window.location.href = '/products';
  };

  const handleConflictResolve = async (resolution: 'overwrite' | 'cancel') => {
    if (resolution === 'cancel') {
      // Reload product to get latest version
      setConflict(null);
      setPendingUpdate(null);
      window.location.reload();
      return;
    }

    if (resolution === 'overwrite' && pendingUpdate) {
      // Force update with overwrite flag
      setConflict(null);
      setIsLoading(true);

      try {
        // TODO: Use GraphQL mutation with force flag
        // const result = await updateProduct({
        //   variables: {
        //     input: {
        //       ...pendingUpdate,
        //       force: true, // Override optimistic lock
        //     },
        //   },
        // });

        console.log('Force updating product:', pendingUpdate);
        await new Promise(resolve => setTimeout(resolve, 1000));

        // Success - redirect to product list
        window.location.href = '/products';
      } catch (err) {
        console.error('Failed to force update product:', err);
        setError(err instanceof Error ? err.message : 'Failed to update product');
        setIsLoading(false);
      }
    }
  };

  if (isFetching) {
    return (
      <div style={styles.loading}>
        <div>Loading product...</div>
      </div>
    );
  }

  if (error && !product) {
    return (
      <div style={styles.container}>
        <div style={styles.errorBanner}>
          <strong>Error:</strong> {error}
        </div>
        <button onClick={() => window.location.href = '/products'} style={styles.backButton}>
          Back to Products
        </button>
      </div>
    );
  }

  if (!product) {
    return (
      <div style={styles.container}>
        <div style={styles.errorBanner}>
          <strong>Error:</strong> Product not found
        </div>
        <button onClick={() => window.location.href = '/products'} style={styles.backButton}>
          Back to Products
        </button>
      </div>
    );
  }

  return (
    <div style={styles.container}>
      {error && (
        <div style={styles.errorBanner}>
          <strong>Error:</strong> {error}
        </div>
      )}

      <ProductForm
        product={product}
        onSubmit={handleSubmit}
        onCancel={handleCancel}
        isLoading={isLoading}
      />

      {conflict && (
        <ConflictModal
          conflict={conflict}
          onResolve={handleConflictResolve}
        />
      )}
    </div>
  );
}

const styles = {
  container: {
    padding: '2rem',
    maxWidth: '1440px',
    margin: '0 auto',
  } as React.CSSProperties,
  loading: {
    display: 'flex',
    justifyContent: 'center',
    alignItems: 'center',
    padding: '4rem',
    fontSize: '1.125rem',
    color: '#718096',
  } as React.CSSProperties,
  errorBanner: {
    padding: '1rem',
    marginBottom: '1.5rem',
    backgroundColor: '#fed7d7',
    color: '#c53030',
    borderRadius: '4px',
    borderLeft: '4px solid #e53e3e',
  } as React.CSSProperties,
  backButton: {
    padding: '0.75rem 1.5rem',
    backgroundColor: '#667eea',
    color: 'white',
    border: 'none',
    borderRadius: '4px',
    fontSize: '1rem',
    fontWeight: 500,
    cursor: 'pointer',
  } as React.CSSProperties,
};
