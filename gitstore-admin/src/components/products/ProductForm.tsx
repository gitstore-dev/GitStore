import React, { useState, useEffect } from 'react';
import { MarkdownEditor } from '../shared/MarkdownEditor';

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

interface Category {
  id: string;
  name: string;
  slug: string;
}

interface Collection {
  id: string;
  name: string;
  slug: string;
}

interface ProductFormProps {
  product?: Product;
  onSubmit: (product: Product) => void | Promise<void>;
  onCancel: () => void;
  isLoading?: boolean;
}

const INVENTORY_STATUSES = [
  { value: 'IN_STOCK', label: 'In Stock' },
  { value: 'OUT_OF_STOCK', label: 'Out of Stock' },
  { value: 'PREORDER', label: 'Pre-order' },
  { value: 'DISCONTINUED', label: 'Discontinued' },
];

/**
 * Product form component for creating and editing products
 * Handles all product fields including title, SKU, price, category, collections
 */
export function ProductForm({ product, onSubmit, onCancel, isLoading = false }: ProductFormProps) {
  const [formData, setFormData] = useState<Product>({
    title: '',
    sku: '',
    price: '',
    compareAtPrice: null,
    inventoryStatus: 'IN_STOCK',
    inventoryQuantity: 0,
    description: '',
    categoryId: null,
    collectionIds: [],
    images: [],
    ...product,
  });

  const [errors, setErrors] = useState<Record<string, string>>({});
  const [categories, setCategories] = useState<Category[]>([]);
  const [collections, setCollections] = useState<Collection[]>([]);
  const [imageInput, setImageInput] = useState('');

  // TODO: Replace with actual GraphQL queries when codegen runs
  // const { data: categoriesData } = useGetCategoriesQuery();
  // const { data: collectionsData } = useGetCollectionsQuery();

  // Mock data for now
  useEffect(() => {
    setCategories([
      { id: 'cat_1', name: 'Electronics', slug: 'electronics' },
      { id: 'cat_2', name: 'Clothing', slug: 'clothing' },
      { id: 'cat_3', name: 'Books', slug: 'books' },
    ]);
    setCollections([
      { id: 'coll_1', name: 'Featured', slug: 'featured' },
      { id: 'coll_2', name: 'New Arrivals', slug: 'new-arrivals' },
      { id: 'coll_3', name: 'Best Sellers', slug: 'best-sellers' },
    ]);
  }, []);

  const handleChange = (e: React.ChangeEvent<HTMLInputElement | HTMLTextAreaElement | HTMLSelectElement>) => {
    const { name, value } = e.target;
    setFormData(prev => ({
      ...prev,
      [name]: value,
    }));
    // Clear error when user starts typing
    if (errors[name]) {
      setErrors(prev => ({ ...prev, [name]: '' }));
    }
  };

  const handleNumberChange = (e: React.ChangeEvent<HTMLInputElement>) => {
    const { name, value } = e.target;
    setFormData(prev => ({
      ...prev,
      [name]: value ? parseInt(value, 10) : 0,
    }));
  };

  const handleCollectionToggle = (collectionId: string) => {
    setFormData(prev => ({
      ...prev,
      collectionIds: prev.collectionIds.includes(collectionId)
        ? prev.collectionIds.filter(id => id !== collectionId)
        : [...prev.collectionIds, collectionId],
    }));
  };

  const handleAddImage = () => {
    if (imageInput.trim()) {
      setFormData(prev => ({
        ...prev,
        images: [...prev.images, imageInput.trim()],
      }));
      setImageInput('');
    }
  };

  const handleRemoveImage = (index: number) => {
    setFormData(prev => ({
      ...prev,
      images: prev.images.filter((_, i) => i !== index),
    }));
  };

  const validate = (): boolean => {
    const newErrors: Record<string, string> = {};

    if (!formData.title.trim()) {
      newErrors.title = 'Title is required';
    }

    if (!formData.sku.trim()) {
      newErrors.sku = 'SKU is required';
    } else if (!/^[A-Z0-9\-]+$/i.test(formData.sku)) {
      newErrors.sku = 'SKU can only contain letters, numbers, and hyphens';
    }

    if (!formData.price.trim()) {
      newErrors.price = 'Price is required';
    } else if (isNaN(parseFloat(formData.price)) || parseFloat(formData.price) < 0) {
      newErrors.price = 'Price must be a valid positive number';
    }

    if (formData.compareAtPrice && formData.compareAtPrice.trim()) {
      const comparePrice = parseFloat(formData.compareAtPrice);
      const price = parseFloat(formData.price);
      if (isNaN(comparePrice) || comparePrice < 0) {
        newErrors.compareAtPrice = 'Compare price must be a valid positive number';
      } else if (comparePrice <= price) {
        newErrors.compareAtPrice = 'Compare price must be greater than price';
      }
    }

    if (formData.inventoryQuantity < 0) {
      newErrors.inventoryQuantity = 'Inventory quantity cannot be negative';
    }

    setErrors(newErrors);
    return Object.keys(newErrors).length === 0;
  };

  const handleSubmit = async (e: React.FormEvent) => {
    e.preventDefault();

    if (!validate()) {
      return;
    }

    try {
      await onSubmit(formData);
    } catch (error) {
      console.error('Form submission error:', error);
    }
  };

  return (
    <form onSubmit={handleSubmit} style={styles.form}>
      {/* Basic Information Section */}
      <div style={styles.section}>
        <h2 style={styles.sectionTitle}>Basic Information</h2>

        <div style={styles.formGroup}>
          <label htmlFor="title" style={styles.label}>
            Title <span style={styles.required}>*</span>
          </label>
          <input
            type="text"
            id="title"
            name="title"
            value={formData.title}
            onChange={handleChange}
            style={{ ...styles.input, ...(errors.title ? styles.inputError : {}) }}
            placeholder="Product title"
            disabled={isLoading}
          />
          {errors.title && <span style={styles.errorText}>{errors.title}</span>}
        </div>

        <div style={styles.formGroup}>
          <label htmlFor="sku" style={styles.label}>
            SKU <span style={styles.required}>*</span>
          </label>
          <input
            type="text"
            id="sku"
            name="sku"
            value={formData.sku}
            onChange={handleChange}
            style={{ ...styles.input, ...(errors.sku ? styles.inputError : {}) }}
            placeholder="SKU-001"
            disabled={isLoading || !!product?.id}
          />
          {errors.sku && <span style={styles.errorText}>{errors.sku}</span>}
          {product?.id && <span style={styles.helpText}>SKU cannot be changed after creation</span>}
        </div>

        <div style={styles.formGroup}>
          <label htmlFor="description" style={styles.label}>
            Description
          </label>
          <MarkdownEditor
            value={formData.description || ''}
            onChange={(value) => setFormData(prev => ({ ...prev, description: value }))}
            placeholder="Product description (supports Markdown)"
            disabled={isLoading}
            rows={10}
          />
        </div>
      </div>

      {/* Pricing Section */}
      <div style={styles.section}>
        <h2 style={styles.sectionTitle}>Pricing</h2>

        <div style={styles.row}>
          <div style={styles.formGroup}>
            <label htmlFor="price" style={styles.label}>
              Price <span style={styles.required}>*</span>
            </label>
            <div style={styles.inputGroup}>
              <span style={styles.inputAddon}>$</span>
              <input
                type="text"
                id="price"
                name="price"
                value={formData.price}
                onChange={handleChange}
                style={{ ...styles.inputWithAddon, ...(errors.price ? styles.inputError : {}) }}
                placeholder="29.99"
                disabled={isLoading}
              />
            </div>
            {errors.price && <span style={styles.errorText}>{errors.price}</span>}
          </div>

          <div style={styles.formGroup}>
            <label htmlFor="compareAtPrice" style={styles.label}>
              Compare at Price
            </label>
            <div style={styles.inputGroup}>
              <span style={styles.inputAddon}>$</span>
              <input
                type="text"
                id="compareAtPrice"
                name="compareAtPrice"
                value={formData.compareAtPrice || ''}
                onChange={handleChange}
                style={{ ...styles.inputWithAddon, ...(errors.compareAtPrice ? styles.inputError : {}) }}
                placeholder="39.99"
                disabled={isLoading}
              />
            </div>
            {errors.compareAtPrice && <span style={styles.errorText}>{errors.compareAtPrice}</span>}
            <span style={styles.helpText}>Shows as strikethrough price</span>
          </div>
        </div>
      </div>

      {/* Inventory Section */}
      <div style={styles.section}>
        <h2 style={styles.sectionTitle}>Inventory</h2>

        <div style={styles.row}>
          <div style={styles.formGroup}>
            <label htmlFor="inventoryStatus" style={styles.label}>
              Status <span style={styles.required}>*</span>
            </label>
            <select
              id="inventoryStatus"
              name="inventoryStatus"
              value={formData.inventoryStatus}
              onChange={handleChange}
              style={styles.select}
              disabled={isLoading}
            >
              {INVENTORY_STATUSES.map(status => (
                <option key={status.value} value={status.value}>
                  {status.label}
                </option>
              ))}
            </select>
          </div>

          <div style={styles.formGroup}>
            <label htmlFor="inventoryQuantity" style={styles.label}>
              Quantity <span style={styles.required}>*</span>
            </label>
            <input
              type="number"
              id="inventoryQuantity"
              name="inventoryQuantity"
              value={formData.inventoryQuantity}
              onChange={handleNumberChange}
              style={{ ...styles.input, ...(errors.inventoryQuantity ? styles.inputError : {}) }}
              placeholder="0"
              min="0"
              disabled={isLoading}
            />
            {errors.inventoryQuantity && <span style={styles.errorText}>{errors.inventoryQuantity}</span>}
          </div>
        </div>
      </div>

      {/* Organization Section */}
      <div style={styles.section}>
        <h2 style={styles.sectionTitle}>Organization</h2>

        <div style={styles.formGroup}>
          <label htmlFor="categoryId" style={styles.label}>
            Category
          </label>
          <select
            id="categoryId"
            name="categoryId"
            value={formData.categoryId || ''}
            onChange={handleChange}
            style={styles.select}
            disabled={isLoading}
          >
            <option value="">No category</option>
            {categories.map(category => (
              <option key={category.id} value={category.id}>
                {category.name}
              </option>
            ))}
          </select>
        </div>

        <div style={styles.formGroup}>
          <label style={styles.label}>Collections</label>
          <div style={styles.checkboxGroup}>
            {collections.map(collection => (
              <label key={collection.id} style={styles.checkboxLabel}>
                <input
                  type="checkbox"
                  checked={formData.collectionIds.includes(collection.id)}
                  onChange={() => handleCollectionToggle(collection.id)}
                  style={styles.checkbox}
                  disabled={isLoading}
                />
                <span>{collection.name}</span>
              </label>
            ))}
          </div>
        </div>
      </div>

      {/* Images Section */}
      <div style={styles.section}>
        <h2 style={styles.sectionTitle}>Images</h2>

        <div style={styles.formGroup}>
          <label htmlFor="imageInput" style={styles.label}>
            Add Image URL
          </label>
          <div style={styles.inputGroup}>
            <input
              type="text"
              id="imageInput"
              value={imageInput}
              onChange={(e) => setImageInput(e.target.value)}
              style={styles.inputWithButton}
              placeholder="https://example.com/image.jpg"
              disabled={isLoading}
              onKeyPress={(e) => {
                if (e.key === 'Enter') {
                  e.preventDefault();
                  handleAddImage();
                }
              }}
            />
            <button
              type="button"
              onClick={handleAddImage}
              style={styles.addButton}
              disabled={isLoading || !imageInput.trim()}
            >
              Add
            </button>
          </div>
        </div>

        {formData.images.length > 0 && (
          <div style={styles.imageList}>
            {formData.images.map((image, index) => (
              <div key={index} style={styles.imageItem}>
                <img src={image} alt={`Product ${index + 1}`} style={styles.imageThumb} />
                <div style={styles.imageUrl}>{image}</div>
                <button
                  type="button"
                  onClick={() => handleRemoveImage(index)}
                  style={styles.removeButton}
                  disabled={isLoading}
                >
                  Remove
                </button>
              </div>
            ))}
          </div>
        )}
      </div>

      {/* Form Actions */}
      <div style={styles.actions}>
        <button
          type="button"
          onClick={onCancel}
          style={styles.cancelButton}
          disabled={isLoading}
        >
          Cancel
        </button>
        <button
          type="submit"
          style={styles.submitButton}
          disabled={isLoading}
        >
          {isLoading ? 'Saving...' : product?.id ? 'Update Product' : 'Create Product'}
        </button>
      </div>
    </form>
  );
}

const styles = {
  form: {
    maxWidth: '800px',
    margin: '0 auto',
  } as React.CSSProperties,
  section: {
    backgroundColor: 'white',
    borderRadius: '8px',
    padding: '2rem',
    marginBottom: '1.5rem',
    boxShadow: '0 1px 3px rgba(0,0,0,0.1)',
  } as React.CSSProperties,
  sectionTitle: {
    margin: '0 0 1.5rem',
    fontSize: '1.25rem',
    fontWeight: 600,
    color: '#1a202c',
  } as React.CSSProperties,
  formGroup: {
    marginBottom: '1.5rem',
  } as React.CSSProperties,
  row: {
    display: 'grid',
    gridTemplateColumns: '1fr 1fr',
    gap: '1.5rem',
  } as React.CSSProperties,
  label: {
    display: 'block',
    marginBottom: '0.5rem',
    fontSize: '0.875rem',
    fontWeight: 500,
    color: '#4a5568',
  } as React.CSSProperties,
  required: {
    color: '#e53e3e',
  } as React.CSSProperties,
  input: {
    width: '100%',
    padding: '0.75rem',
    border: '1px solid #e2e8f0',
    borderRadius: '4px',
    fontSize: '1rem',
    transition: 'border-color 0.2s',
  } as React.CSSProperties,
  inputError: {
    borderColor: '#e53e3e',
  } as React.CSSProperties,
  inputGroup: {
    display: 'flex',
    alignItems: 'stretch',
  } as React.CSSProperties,
  inputAddon: {
    display: 'flex',
    alignItems: 'center',
    padding: '0 0.75rem',
    backgroundColor: '#f7fafc',
    border: '1px solid #e2e8f0',
    borderRight: 'none',
    borderRadius: '4px 0 0 4px',
    fontSize: '1rem',
    color: '#4a5568',
  } as React.CSSProperties,
  inputWithAddon: {
    flex: 1,
    padding: '0.75rem',
    border: '1px solid #e2e8f0',
    borderRadius: '0 4px 4px 0',
    fontSize: '1rem',
  } as React.CSSProperties,
  inputWithButton: {
    flex: 1,
    padding: '0.75rem',
    border: '1px solid #e2e8f0',
    borderRight: 'none',
    borderRadius: '4px 0 0 4px',
    fontSize: '1rem',
  } as React.CSSProperties,
  textarea: {
    width: '100%',
    padding: '0.75rem',
    border: '1px solid #e2e8f0',
    borderRadius: '4px',
    fontSize: '1rem',
    fontFamily: 'inherit',
    resize: 'vertical',
  } as React.CSSProperties,
  select: {
    width: '100%',
    padding: '0.75rem',
    border: '1px solid #e2e8f0',
    borderRadius: '4px',
    fontSize: '1rem',
    backgroundColor: 'white',
  } as React.CSSProperties,
  checkboxGroup: {
    display: 'flex',
    flexDirection: 'column',
    gap: '0.75rem',
  } as React.CSSProperties,
  checkboxLabel: {
    display: 'flex',
    alignItems: 'center',
    gap: '0.5rem',
    fontSize: '0.875rem',
    color: '#4a5568',
    cursor: 'pointer',
  } as React.CSSProperties,
  checkbox: {
    width: '1rem',
    height: '1rem',
    cursor: 'pointer',
  } as React.CSSProperties,
  errorText: {
    display: 'block',
    marginTop: '0.25rem',
    fontSize: '0.875rem',
    color: '#e53e3e',
  } as React.CSSProperties,
  helpText: {
    display: 'block',
    marginTop: '0.25rem',
    fontSize: '0.75rem',
    color: '#a0aec0',
  } as React.CSSProperties,
  addButton: {
    padding: '0.75rem 1.5rem',
    backgroundColor: '#667eea',
    color: 'white',
    border: 'none',
    borderRadius: '0 4px 4px 0',
    fontSize: '1rem',
    fontWeight: 500,
    cursor: 'pointer',
  } as React.CSSProperties,
  imageList: {
    display: 'flex',
    flexDirection: 'column',
    gap: '1rem',
  } as React.CSSProperties,
  imageItem: {
    display: 'flex',
    alignItems: 'center',
    gap: '1rem',
    padding: '0.75rem',
    backgroundColor: '#f7fafc',
    borderRadius: '4px',
  } as React.CSSProperties,
  imageThumb: {
    width: '60px',
    height: '60px',
    objectFit: 'cover',
    borderRadius: '4px',
  } as React.CSSProperties,
  imageUrl: {
    flex: 1,
    fontSize: '0.875rem',
    color: '#4a5568',
    overflow: 'hidden',
    textOverflow: 'ellipsis',
    whiteSpace: 'nowrap',
  } as React.CSSProperties,
  removeButton: {
    padding: '0.5rem 1rem',
    backgroundColor: 'transparent',
    color: '#e53e3e',
    border: '1px solid #e53e3e',
    borderRadius: '4px',
    fontSize: '0.875rem',
    fontWeight: 500,
    cursor: 'pointer',
  } as React.CSSProperties,
  actions: {
    display: 'flex',
    justifyContent: 'flex-end',
    gap: '1rem',
    padding: '1.5rem',
    backgroundColor: 'white',
    borderRadius: '8px',
    boxShadow: '0 1px 3px rgba(0,0,0,0.1)',
  } as React.CSSProperties,
  cancelButton: {
    padding: '0.75rem 1.5rem',
    backgroundColor: 'transparent',
    color: '#718096',
    border: '1px solid #e2e8f0',
    borderRadius: '4px',
    fontSize: '1rem',
    fontWeight: 500,
    cursor: 'pointer',
  } as React.CSSProperties,
  submitButton: {
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
