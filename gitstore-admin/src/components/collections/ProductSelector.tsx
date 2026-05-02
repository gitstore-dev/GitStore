import React, { useState, useEffect } from 'react';
import { useQuery } from 'urql';

// GraphQL query for products
const PRODUCTS_QUERY = `
  query GetProductsForSelector {
    products(first: 1000) {
      edges {
        node {
          id
          title
          sku
          price
        }
      }
    }
  }
`;

// Product type from GraphQL
interface Product {
  id: string;
  title: string;
  sku?: string | null;
  price: string; // Decimal type from GraphQL
}

interface ProductSelectorProps {
  selectedProductIds: string[];
  onChange: (productIds: string[]) => void;
  disabled?: boolean;
}

/**
 * Product selector component for adding/removing products in collections
 * Provides search, filtering, and multi-select functionality
 */
export function ProductSelector({ selectedProductIds, onChange, disabled = false }: ProductSelectorProps) {
  const [searchQuery, setSearchQuery] = useState('');
  const [isAddingProducts, setIsAddingProducts] = useState(false);

  // Query products from GraphQL API
  const [{ data, fetching: loading, error: queryError }] = useQuery({
    query: PRODUCTS_QUERY,
  });

  const error = queryError ? queryError.message : null;
  const allProducts: Product[] = data?.products?.edges?.map((edge: any) => edge.node) || [];

  const handleAddProduct = (productId: string) => {
    if (!selectedProductIds.includes(productId)) {
      onChange([...selectedProductIds, productId]);
    }
  };

  const handleRemoveProduct = (productId: string) => {
    onChange(selectedProductIds.filter(id => id !== productId));
  };

  const selectedProducts = allProducts.filter(product =>
    selectedProductIds.includes(product.id)
  );

  const availableProducts = allProducts.filter(product =>
    !selectedProductIds.includes(product.id)
  );

  const filteredAvailableProducts = availableProducts.filter(product =>
    product.title.toLowerCase().includes(searchQuery.toLowerCase()) ||
    (product.sku && product.sku.toLowerCase().includes(searchQuery.toLowerCase()))
  );

  if (loading) {
    return (
      <div style={styles.loading}>
        <div>Loading products...</div>
      </div>
    );
  }

  if (error) {
    return (
      <div style={styles.error}>
        <p>Error loading products: {error}</p>
      </div>
    );
  }

  return (
    <div style={styles.container}>
      {/* Selected Products Section */}
      <div style={styles.section}>
        <h3 style={styles.sectionTitle}>
          Selected Products ({selectedProducts.length})
        </h3>
        {selectedProducts.length === 0 ? (
          <div style={styles.emptyState}>
            <p style={styles.emptyText}>No products selected</p>
            <p style={styles.helpText}>Click "Add Products" to select products for this collection</p>
          </div>
        ) : (
          <div style={styles.selectedList}>
            {selectedProducts.map(product => (
              <div key={product.id} style={styles.selectedItem}>
                <div style={styles.productInfo}>
                  <div style={styles.productTitle}>{product.title}</div>
                  <div style={styles.productMeta}>
                    {product.sku && <span style={styles.sku}>{product.sku}</span>}
                    <span style={styles.price}>${parseFloat(product.price).toFixed(2)}</span>
                  </div>
                </div>
                <button
                  type="button"
                  onClick={() => handleRemoveProduct(product.id)}
                  style={styles.removeButton}
                  disabled={disabled}
                  title="Remove product"
                >
                  ✕
                </button>
              </div>
            ))}
          </div>
        )}
      </div>

      {/* Add Products Section */}
      {!isAddingProducts ? (
        <button
          type="button"
          onClick={() => setIsAddingProducts(true)}
          style={styles.addButton}
          disabled={disabled}
        >
          + Add Products
        </button>
      ) : (
        <div style={styles.section}>
          <div style={styles.addHeader}>
            <h3 style={styles.sectionTitle}>Add Products</h3>
            <button
              type="button"
              onClick={() => {
                setIsAddingProducts(false);
                setSearchQuery('');
              }}
              style={styles.doneButton}
            >
              Done
            </button>
          </div>

          <div style={styles.searchContainer}>
            <input
              type="text"
              placeholder="Search products..."
              value={searchQuery}
              onChange={(e) => setSearchQuery(e.target.value)}
              style={styles.searchInput}
              disabled={disabled}
            />
          </div>

          {filteredAvailableProducts.length === 0 ? (
            <div style={styles.emptyState}>
              <p style={styles.emptyText}>
                {searchQuery ? 'No products found' : 'All products have been added'}
              </p>
            </div>
          ) : (
            <div style={styles.availableList}>
              {filteredAvailableProducts.map(product => (
                <div key={product.id} style={styles.availableItem}>
                  <div style={styles.productInfo}>
                    <div style={styles.productTitle}>{product.title}</div>
                    <div style={styles.productMeta}>
                      {product.sku && <span style={styles.sku}>{product.sku}</span>}
                      <span style={styles.price}>${parseFloat(product.price).toFixed(2)}</span>
                    </div>
                  </div>
                  <button
                    type="button"
                    onClick={() => handleAddProduct(product.id)}
                    style={styles.addItemButton}
                    disabled={disabled}
                  >
                    Add
                  </button>
                </div>
              ))}
            </div>
          )}
        </div>
      )}
    </div>
  );
}

const styles = {
  container: {
    display: 'flex',
    flexDirection: 'column',
    gap: '1rem',
  } as React.CSSProperties,
  section: {
    backgroundColor: '#f7fafc',
    borderRadius: '4px',
    border: '1px solid #e2e8f0',
    padding: '1.5rem',
  } as React.CSSProperties,
  sectionTitle: {
    margin: '0 0 1rem',
    fontSize: '1rem',
    fontWeight: 600,
    color: '#1a202c',
  } as React.CSSProperties,
  loading: {
    display: 'flex',
    justifyContent: 'center',
    alignItems: 'center',
    padding: '2rem',
    fontSize: '0.875rem',
    color: '#718096',
  } as React.CSSProperties,
  error: {
    padding: '1rem',
    backgroundColor: '#fed7d7',
    color: '#c53030',
    borderRadius: '4px',
    fontSize: '0.875rem',
  } as React.CSSProperties,
  emptyState: {
    textAlign: 'center',
    padding: '2rem 1rem',
  } as React.CSSProperties,
  emptyText: {
    margin: '0 0 0.5rem',
    color: '#718096',
    fontSize: '0.875rem',
  } as React.CSSProperties,
  helpText: {
    margin: 0,
    fontSize: '0.75rem',
    color: '#a0aec0',
  } as React.CSSProperties,
  selectedList: {
    display: 'flex',
    flexDirection: 'column',
    gap: '0.5rem',
  } as React.CSSProperties,
  selectedItem: {
    display: 'flex',
    justifyContent: 'space-between',
    alignItems: 'center',
    padding: '0.75rem',
    backgroundColor: 'white',
    border: '1px solid #e2e8f0',
    borderRadius: '4px',
    gap: '1rem',
  } as React.CSSProperties,
  productInfo: {
    flex: 1,
    minWidth: 0,
  } as React.CSSProperties,
  productTitle: {
    fontSize: '0.875rem',
    fontWeight: 500,
    color: '#1a202c',
    marginBottom: '0.25rem',
  } as React.CSSProperties,
  productMeta: {
    display: 'flex',
    gap: '0.75rem',
    fontSize: '0.75rem',
    color: '#718096',
  } as React.CSSProperties,
  sku: {
    fontFamily: 'monospace',
    backgroundColor: '#edf2f7',
    padding: '0.125rem 0.375rem',
    borderRadius: '2px',
  } as React.CSSProperties,
  price: {
    fontWeight: 500,
    color: '#2d3748',
  } as React.CSSProperties,
  removeButton: {
    padding: '0.375rem 0.5rem',
    backgroundColor: 'transparent',
    color: '#e53e3e',
    border: 'none',
    borderRadius: '4px',
    fontSize: '1rem',
    fontWeight: 600,
    cursor: 'pointer',
    transition: 'background-color 0.2s',
  } as React.CSSProperties,
  addButton: {
    padding: '0.75rem 1.5rem',
    backgroundColor: '#667eea',
    color: 'white',
    border: 'none',
    borderRadius: '4px',
    fontSize: '0.875rem',
    fontWeight: 500,
    cursor: 'pointer',
    transition: 'background-color 0.2s',
  } as React.CSSProperties,
  addHeader: {
    display: 'flex',
    justifyContent: 'space-between',
    alignItems: 'center',
    marginBottom: '1rem',
  } as React.CSSProperties,
  doneButton: {
    padding: '0.5rem 1rem',
    backgroundColor: '#48bb78',
    color: 'white',
    border: 'none',
    borderRadius: '4px',
    fontSize: '0.875rem',
    fontWeight: 500,
    cursor: 'pointer',
  } as React.CSSProperties,
  searchContainer: {
    marginBottom: '1rem',
  } as React.CSSProperties,
  searchInput: {
    width: '100%',
    padding: '0.75rem',
    border: '1px solid #e2e8f0',
    borderRadius: '4px',
    fontSize: '0.875rem',
    backgroundColor: 'white',
  } as React.CSSProperties,
  availableList: {
    display: 'flex',
    flexDirection: 'column',
    gap: '0.5rem',
    maxHeight: '400px',
    overflowY: 'auto',
  } as React.CSSProperties,
  availableItem: {
    display: 'flex',
    justifyContent: 'space-between',
    alignItems: 'center',
    padding: '0.75rem',
    backgroundColor: 'white',
    border: '1px solid #e2e8f0',
    borderRadius: '4px',
    gap: '1rem',
  } as React.CSSProperties,
  addItemButton: {
    padding: '0.5rem 1rem',
    backgroundColor: 'transparent',
    color: '#667eea',
    border: '1px solid #667eea',
    borderRadius: '4px',
    fontSize: '0.875rem',
    fontWeight: 500,
    cursor: 'pointer',
    transition: 'all 0.2s',
    whiteSpace: 'nowrap',
  } as React.CSSProperties,
};
