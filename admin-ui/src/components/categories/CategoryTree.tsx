import React, { useState } from 'react';

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

interface CategoryTreeProps {
  categories: Category[];
  onEdit?: (categoryId: string) => void;
  onDelete?: (categoryId: string) => void;
  onAddChild?: (parentId: string) => void;
}

/**
 * Category tree component displaying hierarchical category structure
 * Shows nested categories with expand/collapse and action buttons
 */
export function CategoryTree({ categories, onEdit, onDelete, onAddChild }: CategoryTreeProps) {
  const [expandedIds, setExpandedIds] = useState<Set<string>>(new Set());

  const toggleExpand = (id: string) => {
    setExpandedIds(prev => {
      const next = new Set(prev);
      if (next.has(id)) {
        next.delete(id);
      } else {
        next.add(id);
      }
      return next;
    });
  };

  const expandAll = () => {
    const allIds = new Set<string>();
    const collectIds = (cats: Category[]) => {
      cats.forEach(cat => {
        if (cat.children && cat.children.length > 0) {
          allIds.add(cat.id);
          collectIds(cat.children);
        }
      });
    };
    collectIds(categories);
    setExpandedIds(allIds);
  };

  const collapseAll = () => {
    setExpandedIds(new Set());
  };

  const renderCategory = (category: Category, level: number = 0) => {
    const hasChildren = category.children && category.children.length > 0;
    const isExpanded = expandedIds.has(category.id);

    return (
      <div key={category.id} style={styles.categoryContainer}>
        <div
          style={{
            ...styles.categoryRow,
            paddingLeft: `${level * 2 + 1}rem`,
          }}
        >
          {/* Expand/Collapse Button */}
          <button
            onClick={() => toggleExpand(category.id)}
            style={{
              ...styles.expandButton,
              visibility: hasChildren ? 'visible' : 'hidden',
            }}
            type="button"
          >
            {hasChildren && (isExpanded ? '▼' : '▶')}
          </button>

          {/* Category Info */}
          <div style={styles.categoryInfo}>
            <div style={styles.categoryName}>{category.name}</div>
            {category.description && (
              <div style={styles.categoryDescription}>{category.description}</div>
            )}
            <div style={styles.categoryMeta}>
              <span style={styles.metaItem}>Slug: {category.slug}</span>
              <span style={styles.metaItem}>Order: {category.displayOrder}</span>
              {hasChildren && (
                <span style={styles.metaItem}>
                  {category.children!.length} {category.children!.length === 1 ? 'child' : 'children'}
                </span>
              )}
            </div>
          </div>

          {/* Actions */}
          <div style={styles.actions}>
            {onAddChild && (
              <button
                onClick={() => onAddChild(category.id)}
                style={styles.actionButton}
                title="Add child category"
                type="button"
              >
                + Child
              </button>
            )}
            {onEdit && (
              <button
                onClick={() => onEdit(category.id)}
                style={styles.actionButton}
                title="Edit category"
                type="button"
              >
                Edit
              </button>
            )}
            {onDelete && (
              <button
                onClick={() => onDelete(category.id)}
                style={styles.deleteButton}
                title="Delete category"
                type="button"
              >
                Delete
              </button>
            )}
          </div>
        </div>

        {/* Children */}
        {hasChildren && isExpanded && (
          <div style={styles.childrenContainer}>
            {category.children!.map(child => renderCategory(child, level + 1))}
          </div>
        )}
      </div>
    );
  };

  if (categories.length === 0) {
    return (
      <div style={styles.empty}>
        <p>No categories yet</p>
        <a href="/categories/new" style={styles.createButton}>
          Create your first category
        </a>
      </div>
    );
  }

  return (
    <div style={styles.container}>
      {/* Toolbar */}
      <div style={styles.toolbar}>
        <div style={styles.toolbarLeft}>
          <button onClick={expandAll} style={styles.toolbarButton} type="button">
            Expand All
          </button>
          <button onClick={collapseAll} style={styles.toolbarButton} type="button">
            Collapse All
          </button>
        </div>
        <a href="/categories/new" style={styles.createButton}>
          + New Category
        </a>
      </div>

      {/* Tree */}
      <div style={styles.treeContainer}>
        {categories.map(category => renderCategory(category, 0))}
      </div>
    </div>
  );
}

const styles = {
  container: {
    backgroundColor: 'white',
    borderRadius: '8px',
    boxShadow: '0 1px 3px rgba(0,0,0,0.1)',
    overflow: 'hidden',
  } as React.CSSProperties,
  toolbar: {
    display: 'flex',
    justifyContent: 'space-between',
    alignItems: 'center',
    padding: '1rem',
    borderBottom: '1px solid #e2e8f0',
    backgroundColor: '#f7fafc',
  } as React.CSSProperties,
  toolbarLeft: {
    display: 'flex',
    gap: '0.5rem',
  } as React.CSSProperties,
  toolbarButton: {
    padding: '0.5rem 1rem',
    backgroundColor: 'white',
    color: '#4a5568',
    border: '1px solid #e2e8f0',
    borderRadius: '4px',
    fontSize: '0.875rem',
    fontWeight: 500,
    cursor: 'pointer',
    transition: 'all 0.2s',
  } as React.CSSProperties,
  createButton: {
    padding: '0.5rem 1rem',
    backgroundColor: '#667eea',
    color: 'white',
    border: 'none',
    borderRadius: '4px',
    fontSize: '0.875rem',
    fontWeight: 500,
    textDecoration: 'none',
    cursor: 'pointer',
    transition: 'background 0.2s',
  } as React.CSSProperties,
  treeContainer: {
    padding: '1rem',
  } as React.CSSProperties,
  categoryContainer: {
    marginBottom: '0.5rem',
  } as React.CSSProperties,
  categoryRow: {
    display: 'flex',
    alignItems: 'flex-start',
    gap: '0.75rem',
    padding: '0.75rem',
    backgroundColor: '#f7fafc',
    borderRadius: '4px',
    border: '1px solid #e2e8f0',
    transition: 'all 0.2s',
  } as React.CSSProperties,
  expandButton: {
    width: '24px',
    height: '24px',
    padding: 0,
    backgroundColor: 'transparent',
    color: '#4a5568',
    border: 'none',
    cursor: 'pointer',
    fontSize: '0.75rem',
    display: 'flex',
    alignItems: 'center',
    justifyContent: 'center',
    flexShrink: 0,
  } as React.CSSProperties,
  categoryInfo: {
    flex: 1,
    minWidth: 0,
  } as React.CSSProperties,
  categoryName: {
    fontSize: '1rem',
    fontWeight: 600,
    color: '#1a202c',
    marginBottom: '0.25rem',
  } as React.CSSProperties,
  categoryDescription: {
    fontSize: '0.875rem',
    color: '#718096',
    marginBottom: '0.5rem',
  } as React.CSSProperties,
  categoryMeta: {
    display: 'flex',
    gap: '1rem',
    flexWrap: 'wrap',
  } as React.CSSProperties,
  metaItem: {
    fontSize: '0.75rem',
    color: '#a0aec0',
  } as React.CSSProperties,
  actions: {
    display: 'flex',
    gap: '0.5rem',
    flexShrink: 0,
  } as React.CSSProperties,
  actionButton: {
    padding: '0.5rem 1rem',
    backgroundColor: 'white',
    color: '#667eea',
    border: '1px solid #667eea',
    borderRadius: '4px',
    fontSize: '0.875rem',
    fontWeight: 500,
    cursor: 'pointer',
    transition: 'all 0.2s',
    whiteSpace: 'nowrap',
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
    whiteSpace: 'nowrap',
  } as React.CSSProperties,
  childrenContainer: {
    marginTop: '0.5rem',
  } as React.CSSProperties,
  empty: {
    textAlign: 'center',
    padding: '4rem',
    color: '#718096',
  } as React.CSSProperties,
};
