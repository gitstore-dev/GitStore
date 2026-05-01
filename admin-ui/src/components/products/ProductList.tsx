import React, { useState } from 'react';
import { useNavigate } from 'react-router-dom';
import { LoadingSpinner } from '../shared/LoadingSpinner';

// Placeholder types until codegen runs
interface Product {
  id: string;
  title: string;
  sku: string;
  price: string;
  compareAtPrice?: string | null;
  inventoryStatus: string;
  inventoryQuantity: number;
  categoryId?: string | null;
  images: string[];
}

interface ProductListProps {
  onDelete?: (productId: string) => void;
}

/**
 * Product list component with table view
 */
export function ProductList({ onDelete }: ProductListProps) {
  const [products, setProducts] = useState<Product[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [searchQuery, setSearchQuery] = useState('');

  // TODO: Replace with actual GraphQL query when codegen runs
  // const { data, loading, error } = useGetProductsQuery();

  // Mock data for now
  React.useEffect(() => {
    // Simulate loading
    setTimeout(() => {
      setProducts([
        {
          id: 'prod_1',
          title: 'Sample Product 1',
          sku: 'SKU-001',
          price: '29.99',
          compareAtPrice: '39.99',
          inventoryStatus: 'IN_STOCK',
          inventoryQuantity: 100,
          categoryId: 'cat_1',
          images: [],
        },
        {
          id: 'prod_2',
          title: 'Sample Product 2',
          sku: 'SKU-002',
          price: '49.99',
          inventoryStatus: 'IN_STOCK',
          inventoryQuantity: 50,
          images: [],
        },
      ]);
      setLoading(false);
    }, 500);
  }, []);

  const handleDelete = async (productId: string) => {
    if (!confirm('Are you sure you want to delete this product?')) {
      return;
    }

    try {
      // TODO: Use GraphQL mutation
      // await deleteProductMutation({ variables: { input: { id: productId } } });
      setProducts(products.filter(p => p.id !== productId));
      onDelete?.(productId);
    } catch (err) {
      console.error('Failed to delete product:', err);
      alert('Failed to delete product');
    }
  };

  const filteredProducts = products.filter(product =>
    product.title.toLowerCase().includes(searchQuery.toLowerCase()) ||
    product.sku.toLowerCase().includes(searchQuery.toLowerCase())
  );

  if (loading) {
    return <LoadingSpinner message="Loading products..." fullPage />;
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
      <div style={styles.header}>
        <div style={styles.searchContainer}>
          <input
            type="text"
            placeholder="Search products..."
            value={searchQuery}
            onChange={(e) => setSearchQuery(e.target.value)}
            style={styles.searchInput}
          />
        </div>
        <a href="/products/new" style={styles.createButton}>
          + New Product
        </a>
      </div>

      {filteredProducts.length === 0 ? (
        <div style={styles.empty}>
          <p>No products found</p>
          <a href="/products/new" style={styles.createButtonEmpty}>
            Create your first product
          </a>
        </div>
      ) : (
        <div style={styles.tableContainer}>
          <table style={styles.table}>
            <thead>
              <tr>
                <th style={styles.th}>Title</th>
                <th style={styles.th}>SKU</th>
                <th style={styles.th}>Price</th>
                <th style={styles.th}>Inventory</th>
                <th style={styles.th}>Status</th>
                <th style={styles.thActions}>Actions</th>
              </tr>
            </thead>
            <tbody>
              {filteredProducts.map((product) => (
                <tr key={product.id} style={styles.tr}>
                  <td style={styles.td}>
                    <div style={styles.titleCell}>
                      {product.images.length > 0 && (
                        <img
                          src={product.images[0]}
                          alt={product.title}
                          style={styles.thumbnail}
                        />
                      )}
                      <span>{product.title}</span>
                    </div>
                  </td>
                  <td style={styles.td}>
                    <code style={styles.sku}>{product.sku}</code>
                  </td>
                  <td style={styles.td}>
                    <div>
                      <div style={styles.price}>${product.price}</div>
                      {product.compareAtPrice && (
                        <div style={styles.comparePrice}>
                          ${product.compareAtPrice}
                        </div>
                      )}
                    </div>
                  </td>
                  <td style={styles.td}>{product.inventoryQuantity}</td>
                  <td style={styles.td}>
                    <span style={getStatusStyle(product.inventoryStatus)}>
                      {formatStatus(product.inventoryStatus)}
                    </span>
                  </td>
                  <td style={styles.tdActions}>
                    <div style={styles.actions}>
                      <a
                        href={`/products/edit?id=${encodeURIComponent(product.id)}`}
                        style={styles.actionLink}
                      >
                        Edit
                      </a>
                      <button
                        onClick={() => handleDelete(product.id)}
                        style={styles.deleteButton}
                      >
                        Delete
                      </button>
                    </div>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
    </div>
  );
}

function formatStatus(status: string): string {
  return status.replace(/_/g, ' ').replace(/\b\w/g, l => l.toUpperCase());
}

function getStatusStyle(status: string): React.CSSProperties {
  const baseStyle = {
    padding: '0.25rem 0.75rem',
    borderRadius: '9999px',
    fontSize: '0.75rem',
    fontWeight: 600,
  } as React.CSSProperties;

  switch (status) {
    case 'IN_STOCK':
      return { ...baseStyle, backgroundColor: '#c6f6d5', color: '#22543d' };
    case 'OUT_OF_STOCK':
      return { ...baseStyle, backgroundColor: '#fed7d7', color: '#742a2a' };
    case 'PREORDER':
      return { ...baseStyle, backgroundColor: '#bee3f8', color: '#2c5282' };
    case 'DISCONTINUED':
      return { ...baseStyle, backgroundColor: '#e2e8f0', color: '#4a5568' };
    default:
      return baseStyle;
  }
}

const styles = {
  container: {
    padding: '2rem',
    maxWidth: '1440px',
    margin: '0 auto',
  } as React.CSSProperties,
  header: {
    display: 'flex',
    justifyContent: 'space-between',
    alignItems: 'center',
    marginBottom: '2rem',
    gap: '1rem',
  } as React.CSSProperties,
  searchContainer: {
    flex: 1,
    maxWidth: '400px',
  } as React.CSSProperties,
  searchInput: {
    width: '100%',
    padding: '0.75rem 1rem',
    border: '1px solid #e2e8f0',
    borderRadius: '4px',
    fontSize: '1rem',
  } as React.CSSProperties,
  createButton: {
    padding: '0.75rem 1.5rem',
    backgroundColor: '#667eea',
    color: 'white',
    border: 'none',
    borderRadius: '4px',
    fontSize: '1rem',
    fontWeight: 500,
    textDecoration: 'none',
    cursor: 'pointer',
    transition: 'background 0.2s',
  } as React.CSSProperties,
  createButtonEmpty: {
    display: 'inline-block',
    padding: '0.75rem 1.5rem',
    backgroundColor: '#667eea',
    color: 'white',
    border: 'none',
    borderRadius: '4px',
    fontSize: '1rem',
    fontWeight: 500,
    textDecoration: 'none',
    marginTop: '1rem',
  } as React.CSSProperties,
  loading: {
    display: 'flex',
    justifyContent: 'center',
    alignItems: 'center',
    padding: '4rem',
    fontSize: '1.125rem',
    color: '#718096',
  } as React.CSSProperties,
  error: {
    padding: '2rem',
    backgroundColor: '#fed7d7',
    color: '#c53030',
    borderRadius: '4px',
  } as React.CSSProperties,
  empty: {
    textAlign: 'center',
    padding: '4rem',
    color: '#718096',
  } as React.CSSProperties,
  tableContainer: {
    backgroundColor: 'white',
    borderRadius: '8px',
    boxShadow: '0 1px 3px rgba(0,0,0,0.1)',
    overflow: 'hidden',
  } as React.CSSProperties,
  table: {
    width: '100%',
    borderCollapse: 'collapse',
  } as React.CSSProperties,
  th: {
    textAlign: 'left',
    padding: '1rem',
    backgroundColor: '#f7fafc',
    color: '#4a5568',
    fontWeight: 600,
    fontSize: '0.875rem',
    textTransform: 'uppercase',
    letterSpacing: '0.05em',
    borderBottom: '1px solid #e2e8f0',
  } as React.CSSProperties,
  thActions: {
    textAlign: 'right',
    padding: '1rem',
    backgroundColor: '#f7fafc',
    color: '#4a5568',
    fontWeight: 600,
    fontSize: '0.875rem',
    textTransform: 'uppercase',
    letterSpacing: '0.05em',
    borderBottom: '1px solid #e2e8f0',
  } as React.CSSProperties,
  tr: {
    borderBottom: '1px solid #e2e8f0',
  } as React.CSSProperties,
  td: {
    padding: '1rem',
    fontSize: '0.875rem',
    color: '#1a202c',
  } as React.CSSProperties,
  tdActions: {
    padding: '1rem',
    textAlign: 'right',
  } as React.CSSProperties,
  titleCell: {
    display: 'flex',
    alignItems: 'center',
    gap: '0.75rem',
  } as React.CSSProperties,
  thumbnail: {
    width: '40px',
    height: '40px',
    borderRadius: '4px',
    objectFit: 'cover',
  } as React.CSSProperties,
  sku: {
    padding: '0.25rem 0.5rem',
    backgroundColor: '#f7fafc',
    borderRadius: '4px',
    fontSize: '0.75rem',
    fontFamily: 'monospace',
  } as React.CSSProperties,
  price: {
    fontWeight: 600,
    color: '#1a202c',
  } as React.CSSProperties,
  comparePrice: {
    fontSize: '0.75rem',
    color: '#a0aec0',
    textDecoration: 'line-through',
  } as React.CSSProperties,
  actions: {
    display: 'flex',
    justifyContent: 'flex-end',
    gap: '0.5rem',
  } as React.CSSProperties,
  actionLink: {
    padding: '0.5rem 1rem',
    color: '#667eea',
    textDecoration: 'none',
    fontSize: '0.875rem',
    fontWeight: 500,
    border: '1px solid #667eea',
    borderRadius: '4px',
    transition: 'all 0.2s',
  } as React.CSSProperties,
  deleteButton: {
    padding: '0.5rem 1rem',
    backgroundColor: 'transparent',
    color: '#e53e3e',
    border: '1px solid #e53e3e',
    borderRadius: '4px',
    fontSize: '0.875rem',
    fontWeight: 500,
    cursor: 'pointer',
    transition: 'all 0.2s',
  } as React.CSSProperties,
};
