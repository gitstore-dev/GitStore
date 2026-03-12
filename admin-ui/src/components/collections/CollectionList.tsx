import React, { useState, useEffect } from 'react';
import { DragDropContext, Droppable, Draggable, DropResult } from 'react-beautiful-dnd';

// Placeholder types until codegen runs
interface Collection {
  id: string;
  name: string;
  slug: string;
  description?: string | null;
  productIds: string[];
  displayOrder: number;
}

interface CollectionListProps {
  onEdit?: (collectionId: string) => void;
  onDelete?: (collectionId: string) => void;
}

/**
 * Collection list component displaying collections in a table with drag-and-drop reordering
 */
export function CollectionList({ onEdit, onDelete }: CollectionListProps) {
  const [collections, setCollections] = useState<Collection[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [searchQuery, setSearchQuery] = useState('');
  const [isDragging, setIsDragging] = useState(false);

  // TODO: Replace with actual GraphQL query when codegen runs
  // const { data, loading, error } = useGetCollectionsQuery();

  // Load collections
  useEffect(() => {
    const loadCollections = async () => {
      setLoading(true);
      setError(null);

      try {
        // TODO: Use GraphQL query
        // const result = await client.query({
        //   query: GetCollectionsDocument,
        // });

        // Simulate API call with mock data
        console.log('Loading collections');
        await new Promise(resolve => setTimeout(resolve, 500));

        const mockCollections: Collection[] = [
          {
            id: 'coll_1',
            name: 'Featured',
            slug: 'featured',
            description: 'Featured products handpicked by our team',
            productIds: ['prod_1', 'prod_2', 'prod_5'],
            displayOrder: 1,
          },
          {
            id: 'coll_2',
            name: 'New Arrivals',
            slug: 'new-arrivals',
            description: 'Latest products added to our store',
            productIds: ['prod_3', 'prod_4'],
            displayOrder: 2,
          },
          {
            id: 'coll_3',
            name: 'Best Sellers',
            slug: 'best-sellers',
            description: 'Our most popular products',
            productIds: ['prod_1', 'prod_3', 'prod_6', 'prod_7'],
            displayOrder: 3,
          },
          {
            id: 'coll_4',
            name: 'On Sale',
            slug: 'on-sale',
            description: 'Products currently on sale',
            productIds: [],
            displayOrder: 4,
          },
        ];

        setCollections(mockCollections);
      } catch (err) {
        console.error('Failed to load collections:', err);
        setError(err instanceof Error ? err.message : 'Failed to load collections');
      } finally {
        setLoading(false);
      }
    };

    loadCollections();
  }, []);

  const handleEdit = (collectionId: string) => {
    if (onEdit) {
      onEdit(collectionId);
    } else {
      window.location.href = `/collections/${collectionId}`;
    }
  };

  const handleDelete = async (collectionId: string) => {
    if (!confirm('Are you sure you want to delete this collection?')) {
      return;
    }

    try {
      // TODO: Use GraphQL mutation
      // await deleteCollectionMutation({ variables: { input: { id: collectionId } } });

      console.log('Deleting collection:', collectionId);

      // Remove from local state
      setCollections(collections.filter(coll => coll.id !== collectionId));

      if (onDelete) {
        onDelete(collectionId);
      }
    } catch (err) {
      console.error('Failed to delete collection:', err);
      alert('Failed to delete collection');
    }
  };

  const handleDragStart = () => {
    setIsDragging(true);
  };

  const handleDragEnd = async (result: DropResult) => {
    setIsDragging(false);

    if (!result.destination) {
      return;
    }

    const { source, destination } = result;

    // If dropped in same position, do nothing
    if (source.index === destination.index) {
      return;
    }

    // Reorder collections
    const reordered = Array.from(collections);
    const [removed] = reordered.splice(source.index, 1);
    reordered.splice(destination.index, 0, removed);

    // Update display order
    const updated = reordered.map((coll, index) => ({
      ...coll,
      displayOrder: index,
    }));

    // Optimistically update UI
    setCollections(updated);

    try {
      // TODO: Use GraphQL mutation
      // const [reorderCollections] = useReorderCollectionsMutation();
      // await reorderCollections({
      //   variables: {
      //     input: {
      //       clientMutationId: uuidv4(),
      //       collectionIds: updated.map(c => c.id),
      //     },
      //   },
      // });

      console.log('Collections reordered:', updated.map(c => c.id));
      await new Promise(resolve => setTimeout(resolve, 500));
    } catch (err) {
      console.error('Failed to reorder collections:', err);
      alert('Failed to reorder collections. Changes reverted.');
      window.location.reload();
    }
  };

  const filteredCollections = collections.filter(collection =>
    collection.name.toLowerCase().includes(searchQuery.toLowerCase()) ||
    collection.slug.toLowerCase().includes(searchQuery.toLowerCase())
  );

  if (loading) {
    return (
      <div style={styles.loading}>
        <div>Loading collections...</div>
      </div>
    );
  }

  if (error) {
    return (
      <div style={styles.error}>
        <p>Error loading collections: {error}</p>
      </div>
    );
  }

  return (
    <div style={styles.container}>
      <div style={styles.header}>
        <div style={styles.searchContainer}>
          <input
            type="text"
            placeholder="Search collections..."
            value={searchQuery}
            onChange={(e) => setSearchQuery(e.target.value)}
            style={styles.searchInput}
          />
        </div>
        <a href="/collections/new" style={styles.createButton}>
          + New Collection
        </a>
      </div>

      {filteredCollections.length === 0 ? (
        <div style={styles.empty}>
          <p>No collections found</p>
          <a href="/collections/new" style={styles.createButtonEmpty}>
            Create your first collection
          </a>
        </div>
      ) : (
        <DragDropContext onDragStart={handleDragStart} onDragEnd={handleDragEnd}>
          <div style={styles.tableContainer}>
            <table style={styles.table}>
              <thead>
                <tr>
                  <th style={styles.thDrag}></th>
                  <th style={styles.th}>Name</th>
                  <th style={styles.th}>Slug</th>
                  <th style={styles.th}>Products</th>
                  <th style={styles.th}>Order</th>
                  <th style={styles.thActions}>Actions</th>
                </tr>
              </thead>
              <Droppable droppableId="collections">
                {(provided, snapshot) => (
                  <tbody
                    ref={provided.innerRef}
                    {...provided.droppableProps}
                    style={{
                      backgroundColor: snapshot.isDraggingOver ? '#f0f4ff' : 'transparent',
                    }}
                  >
                    {filteredCollections.map((collection, index) => (
                      <Draggable key={collection.id} draggableId={collection.id} index={index}>
                        {(provided, snapshot) => (
                          <tr
                            ref={provided.innerRef}
                            {...provided.draggableProps}
                            key={collection.id}
                            style={{
                              ...styles.tr,
                              ...provided.draggableProps.style,
                              backgroundColor: snapshot.isDragging ? '#e6f2ff' : 'white',
                            }}
                          >
                            <td style={styles.tdDrag}>
                              <div {...provided.dragHandleProps} style={styles.dragHandle}>
                                ⋮⋮
                              </div>
                            </td>
                            <td style={styles.td}>
                    <div style={styles.nameCell}>
                      <div style={styles.collectionName}>{collection.name}</div>
                      {collection.description && (
                        <div style={styles.collectionDescription}>
                          {collection.description}
                        </div>
                      )}
                    </div>
                  </td>
                  <td style={styles.td}>
                    <code style={styles.slug}>{collection.slug}</code>
                  </td>
                  <td style={styles.td}>{collection.productIds.length}</td>
                  <td style={styles.td}>{collection.displayOrder}</td>
                  <td style={styles.tdActions}>
                    <div style={styles.actions}>
                      <button
                        onClick={() => handleEdit(collection.id)}
                        style={styles.actionButton}
                      >
                        Edit
                      </button>
                      <button
                        onClick={() => handleDelete(collection.id)}
                        style={styles.deleteButton}
                      >
                        Delete
                      </button>
                              </div>
                            </td>
                          </tr>
                        )}
                      </Draggable>
                    ))}
                    {provided.placeholder}
                  </tbody>
                )}
              </Droppable>
            </table>
          </div>
        </DragDropContext>
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
    margin: '2rem',
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
  thDrag: {
    width: '40px',
    padding: '1rem 0.5rem',
    backgroundColor: '#f7fafc',
    borderBottom: '1px solid #e2e8f0',
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
    transition: 'background-color 0.2s',
  } as React.CSSProperties,
  tdDrag: {
    padding: '0.5rem',
    textAlign: 'center',
  } as React.CSSProperties,
  dragHandle: {
    width: '24px',
    height: '24px',
    display: 'flex',
    alignItems: 'center',
    justifyContent: 'center',
    margin: '0 auto',
    color: '#a0aec0',
    cursor: 'grab',
    fontSize: '1rem',
    userSelect: 'none',
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
  nameCell: {
    display: 'flex',
    flexDirection: 'column',
    gap: '0.25rem',
  } as React.CSSProperties,
  collectionName: {
    fontWeight: 600,
    color: '#1a202c',
  } as React.CSSProperties,
  collectionDescription: {
    fontSize: '0.75rem',
    color: '#718096',
  } as React.CSSProperties,
  slug: {
    padding: '0.25rem 0.5rem',
    backgroundColor: '#f7fafc',
    borderRadius: '4px',
    fontSize: '0.75rem',
    fontFamily: 'monospace',
  } as React.CSSProperties,
  actions: {
    display: 'flex',
    justifyContent: 'flex-end',
    gap: '0.5rem',
  } as React.CSSProperties,
  actionButton: {
    padding: '0.5rem 1rem',
    color: '#667eea',
    backgroundColor: 'transparent',
    border: '1px solid #667eea',
    borderRadius: '4px',
    fontSize: '0.875rem',
    fontWeight: 500,
    cursor: 'pointer',
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
