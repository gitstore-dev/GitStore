import React, { useState, useEffect } from 'react';
import { CategoryTree } from './CategoryTree';

// Placeholder types until codegen runs
interface Category {
  id: string;
  name: string;
  slug: string;
  description?: string | null;
  parentId?: string | null;
  displayOrder: number;
  children?: Category[];
}

interface CategoryListProps {
  onEdit?: (categoryId: string) => void;
  onDelete?: (categoryId: string) => void;
  onAddChild?: (parentId: string) => void;
}

/**
 * Category list component that loads categories and builds tree structure
 */
export function CategoryList({ onEdit, onDelete, onAddChild }: CategoryListProps) {
  const [categories, setCategories] = useState<Category[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);

  // TODO: Replace with actual GraphQL query when codegen runs
  // const { data, loading, error } = useGetCategoriesQuery();

  // Load categories and build tree structure
  useEffect(() => {
    const loadCategories = async () => {
      setLoading(true);
      setError(null);

      try {
        // TODO: Use GraphQL query
        // const result = await client.query({
        //   query: GetCategoriesDocument,
        // });

        // Simulate API call with mock hierarchical data
        console.log('Loading categories');
        await new Promise(resolve => setTimeout(resolve, 500));

        const mockCategories: Category[] = [
          {
            id: 'cat_1',
            name: 'Electronics',
            slug: 'electronics',
            description: 'Electronic devices and accessories',
            parentId: null,
            displayOrder: 1,
            children: [
              {
                id: 'cat_2',
                name: 'Computers',
                slug: 'computers',
                description: 'Desktop and laptop computers',
                parentId: 'cat_1',
                displayOrder: 1,
                children: [
                  {
                    id: 'cat_3',
                    name: 'Laptops',
                    slug: 'laptops',
                    parentId: 'cat_2',
                    displayOrder: 1,
                  },
                  {
                    id: 'cat_4',
                    name: 'Desktops',
                    slug: 'desktops',
                    parentId: 'cat_2',
                    displayOrder: 2,
                  },
                ],
              },
              {
                id: 'cat_5',
                name: 'Phones',
                slug: 'phones',
                description: 'Mobile phones and smartphones',
                parentId: 'cat_1',
                displayOrder: 2,
              },
            ],
          },
          {
            id: 'cat_6',
            name: 'Clothing',
            slug: 'clothing',
            description: 'Apparel and fashion',
            parentId: null,
            displayOrder: 2,
            children: [
              {
                id: 'cat_7',
                name: 'Men',
                slug: 'men',
                parentId: 'cat_6',
                displayOrder: 1,
              },
              {
                id: 'cat_8',
                name: 'Women',
                slug: 'women',
                parentId: 'cat_6',
                displayOrder: 2,
              },
            ],
          },
          {
            id: 'cat_9',
            name: 'Books',
            slug: 'books',
            description: 'Books and reading materials',
            parentId: null,
            displayOrder: 3,
          },
        ];

        setCategories(mockCategories);
      } catch (err) {
        console.error('Failed to load categories:', err);
        setError(err instanceof Error ? err.message : 'Failed to load categories');
      } finally {
        setLoading(false);
      }
    };

    loadCategories();
  }, []);

  const handleEdit = (categoryId: string) => {
    if (onEdit) {
      onEdit(categoryId);
    } else {
      window.location.href = `/categories/${categoryId}`;
    }
  };

  const handleDelete = async (categoryId: string) => {
    if (!confirm('Are you sure you want to delete this category?')) {
      return;
    }

    try {
      // TODO: Use GraphQL mutation
      // await deleteCategoryMutation({ variables: { input: { id: categoryId } } });

      console.log('Deleting category:', categoryId);

      // Remove from local state
      const removeCategory = (cats: Category[]): Category[] => {
        return cats
          .filter(cat => cat.id !== categoryId)
          .map(cat => ({
            ...cat,
            children: cat.children ? removeCategory(cat.children) : undefined,
          }));
      };

      setCategories(removeCategory(categories));

      if (onDelete) {
        onDelete(categoryId);
      }
    } catch (err) {
      console.error('Failed to delete category:', err);
      alert('Failed to delete category');
    }
  };

  const handleAddChild = (parentId: string) => {
    if (onAddChild) {
      onAddChild(parentId);
    } else {
      window.location.href = `/categories/new?parent=${parentId}`;
    }
  };

  if (loading) {
    return (
      <div style={styles.loading}>
        <div>Loading categories...</div>
      </div>
    );
  }

  if (error) {
    return (
      <div style={styles.error}>
        <p>Error loading categories: {error}</p>
      </div>
    );
  }

  return (
    <div style={styles.container}>
      <CategoryTree
        categories={categories}
        onEdit={handleEdit}
        onDelete={handleDelete}
        onAddChild={handleAddChild}
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
    margin: '2rem',
  } as React.CSSProperties,
};
