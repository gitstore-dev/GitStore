import React, { useState } from 'react';
import { MarkdownEditor } from '../shared/MarkdownEditor';
import { ProductSelector } from './ProductSelector';

// Placeholder types until codegen runs
interface Collection {
  id?: string;
  name: string;
  slug: string;
  description?: string | null;
  productIds: string[];
  displayOrder: number;
  metadata?: any;
  version?: string;
}

interface CollectionFormProps {
  collection?: Collection;
  onSubmit: (collection: Collection) => void | Promise<void>;
  onCancel: () => void;
  isLoading?: boolean;
}

/**
 * Collection form component for creating and editing collections
 * Handles name, slug, description, and display order
 * Product selection handled by separate ProductSelector component (T121)
 */
export function CollectionForm({ collection, onSubmit, onCancel, isLoading = false }: CollectionFormProps) {
  const [formData, setFormData] = useState<Collection>({
    name: '',
    slug: '',
    description: '',
    productIds: [],
    displayOrder: 0,
    ...collection,
  });

  const [errors, setErrors] = useState<Record<string, string>>({});

  const handleChange = (e: React.ChangeEvent<HTMLInputElement | HTMLTextAreaElement>) => {
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

  // Auto-generate slug from name
  const handleNameChange = (e: React.ChangeEvent<HTMLInputElement>) => {
    const name = e.target.value;
    setFormData(prev => ({
      ...prev,
      name,
      // Only auto-generate slug if it's a new collection or slug hasn't been manually changed
      slug: !collection?.id ? generateSlug(name) : prev.slug,
    }));
    if (errors.name) {
      setErrors(prev => ({ ...prev, name: '' }));
    }
  };

  const generateSlug = (name: string): string => {
    return name
      .toLowerCase()
      .replace(/[^a-z0-9]+/g, '-')
      .replace(/^-+|-+$/g, '');
  };

  const validate = (): boolean => {
    const newErrors: Record<string, string> = {};

    if (!formData.name.trim()) {
      newErrors.name = 'Name is required';
    }

    if (!formData.slug.trim()) {
      newErrors.slug = 'Slug is required';
    } else if (!/^[a-z0-9\-]+$/i.test(formData.slug)) {
      newErrors.slug = 'Slug can only contain letters, numbers, and hyphens';
    }

    if (formData.displayOrder < 0) {
      newErrors.displayOrder = 'Display order cannot be negative';
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
          <label htmlFor="name" style={styles.label}>
            Name <span style={styles.required}>*</span>
          </label>
          <input
            type="text"
            id="name"
            name="name"
            value={formData.name}
            onChange={handleNameChange}
            style={{ ...styles.input, ...(errors.name ? styles.inputError : {}) }}
            placeholder="Collection name"
            disabled={isLoading}
          />
          {errors.name && <span style={styles.errorText}>{errors.name}</span>}
        </div>

        <div style={styles.formGroup}>
          <label htmlFor="slug" style={styles.label}>
            Slug <span style={styles.required}>*</span>
          </label>
          <input
            type="text"
            id="slug"
            name="slug"
            value={formData.slug}
            onChange={handleChange}
            style={{ ...styles.input, ...(errors.slug ? styles.inputError : {}) }}
            placeholder="collection-slug"
            disabled={isLoading || !!collection?.id}
          />
          {errors.slug && <span style={styles.errorText}>{errors.slug}</span>}
          {collection?.id && <span style={styles.helpText}>Slug cannot be changed after creation</span>}
          {!collection?.id && <span style={styles.helpText}>Auto-generated from name</span>}
        </div>

        <div style={styles.formGroup}>
          <label htmlFor="description" style={styles.label}>
            Description
          </label>
          <MarkdownEditor
            value={formData.description || ''}
            onChange={(value) => setFormData(prev => ({ ...prev, description: value }))}
            placeholder="Collection description (supports Markdown)"
            disabled={isLoading}
            rows={8}
          />
        </div>
      </div>

      {/* Settings Section */}
      <div style={styles.section}>
        <h2 style={styles.sectionTitle}>Settings</h2>

        <div style={styles.formGroup}>
          <label htmlFor="displayOrder" style={styles.label}>
            Display Order <span style={styles.required}>*</span>
          </label>
          <input
            type="number"
            id="displayOrder"
            name="displayOrder"
            value={formData.displayOrder}
            onChange={handleNumberChange}
            style={{ ...styles.input, ...(errors.displayOrder ? styles.inputError : {}) }}
            placeholder="0"
            min="0"
            disabled={isLoading}
          />
          {errors.displayOrder && <span style={styles.errorText}>{errors.displayOrder}</span>}
          <span style={styles.helpText}>
            Lower numbers appear first (0 = first)
          </span>
        </div>
      </div>

      {/* Products Section */}
      <div style={styles.section}>
        <h2 style={styles.sectionTitle}>Products</h2>
        <ProductSelector
          selectedProductIds={formData.productIds}
          onChange={(productIds) => setFormData(prev => ({ ...prev, productIds }))}
          disabled={isLoading}
        />
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
          {isLoading ? 'Saving...' : collection?.id ? 'Update Collection' : 'Create Collection'}
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
