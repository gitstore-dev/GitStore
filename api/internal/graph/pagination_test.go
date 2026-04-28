package graph

import (
	"testing"

	"github.com/gitstore-dev/gitstore/api/internal/catalog"
	"github.com/gitstore-dev/gitstore/api/internal/graph/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestEncodeCursor(t *testing.T) {
	cursor := encodeCursor(0)
	assert.NotEmpty(t, cursor)

	// Decode and verify
	index, err := decodeCursor(cursor)
	require.NoError(t, err)
	assert.Equal(t, 0, index)
}

func TestDecodeCursor(t *testing.T) {
	tests := []struct {
		name          string
		cursor        string
		expectedIndex int
		expectError   bool
	}{
		{
			name:          "valid cursor",
			cursor:        encodeCursor(5),
			expectedIndex: 5,
			expectError:   false,
		},
		{
			name:        "invalid base64",
			cursor:      "invalid!!!",
			expectError: true,
		},
		{
			name:        "invalid format",
			cursor:      encodeCursor(0)[:5], // Truncated
			expectError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			index, err := decodeCursor(tt.cursor)
			if tt.expectError {
				assert.Error(t, err)
			} else {
				require.NoError(t, err)
				assert.Equal(t, tt.expectedIndex, index)
			}
		})
	}
}

func TestPaginateProducts(t *testing.T) {
	// Create test products
	products := make([]*catalog.Product, 10)
	for i := 0; i < 10; i++ {
		products[i] = &catalog.Product{
			ID:    string(rune('A' + i)),
			SKU:   string(rune('A' + i)),
			Title: string(rune('A' + i)),
		}
	}

	t.Run("should return all products when no pagination args", func(t *testing.T) {
		conn, err := PaginateProducts(products, nil, nil, nil, nil)
		require.NoError(t, err)
		assert.Equal(t, 10, len(conn.Edges))
		assert.Equal(t, int32(10), conn.TotalCount)
		assert.False(t, conn.PageInfo.HasNextPage)
		assert.False(t, conn.PageInfo.HasPreviousPage)
	})

	t.Run("should paginate forward with first", func(t *testing.T) {
		first := int32(3)
		conn, err := PaginateProducts(products, &first, nil, nil, nil)
		require.NoError(t, err)
		assert.Equal(t, 3, len(conn.Edges))
		assert.Equal(t, "A", conn.Edges[0].Node.ID)
		assert.Equal(t, "B", conn.Edges[1].Node.ID)
		assert.Equal(t, "C", conn.Edges[2].Node.ID)
		assert.True(t, conn.PageInfo.HasNextPage)
		assert.False(t, conn.PageInfo.HasPreviousPage)
	})

	t.Run("should paginate forward with first and after", func(t *testing.T) {
		first := int32(3)
		after := encodeCursor(2) // After 'C'
		conn, err := PaginateProducts(products, &first, &after, nil, nil)
		require.NoError(t, err)
		assert.Equal(t, 3, len(conn.Edges))
		assert.Equal(t, "D", conn.Edges[0].Node.ID)
		assert.Equal(t, "E", conn.Edges[1].Node.ID)
		assert.Equal(t, "F", conn.Edges[2].Node.ID)
		assert.True(t, conn.PageInfo.HasNextPage)
		assert.True(t, conn.PageInfo.HasPreviousPage)
	})

	t.Run("should paginate backward with last", func(t *testing.T) {
		last := int32(3)
		conn, err := PaginateProducts(products, nil, nil, &last, nil)
		require.NoError(t, err)
		assert.Equal(t, 3, len(conn.Edges))
		assert.Equal(t, "H", conn.Edges[0].Node.ID)
		assert.Equal(t, "I", conn.Edges[1].Node.ID)
		assert.Equal(t, "J", conn.Edges[2].Node.ID)
		assert.False(t, conn.PageInfo.HasNextPage)
		assert.True(t, conn.PageInfo.HasPreviousPage)
	})

	t.Run("should paginate backward with last and before", func(t *testing.T) {
		last := int32(3)
		before := encodeCursor(7) // Before 'H'
		conn, err := PaginateProducts(products, nil, nil, &last, &before)
		require.NoError(t, err)
		assert.Equal(t, 3, len(conn.Edges))
		assert.Equal(t, "E", conn.Edges[0].Node.ID)
		assert.Equal(t, "F", conn.Edges[1].Node.ID)
		assert.Equal(t, "G", conn.Edges[2].Node.ID)
		assert.True(t, conn.PageInfo.HasNextPage)
		assert.True(t, conn.PageInfo.HasPreviousPage)
	})

	t.Run("should handle empty result set", func(t *testing.T) {
		emptyProducts := []*catalog.Product{}
		conn, err := PaginateProducts(emptyProducts, nil, nil, nil, nil)
		require.NoError(t, err)
		assert.Equal(t, 0, len(conn.Edges))
		assert.Equal(t, int32(0), conn.TotalCount)
		assert.Nil(t, conn.PageInfo.StartCursor)
		assert.Nil(t, conn.PageInfo.EndCursor)
	})

	t.Run("should handle first larger than total count", func(t *testing.T) {
		first := int32(20)
		conn, err := PaginateProducts(products, &first, nil, nil, nil)
		require.NoError(t, err)
		assert.Equal(t, 10, len(conn.Edges))
		assert.False(t, conn.PageInfo.HasNextPage)
	})

	t.Run("should handle after cursor at end", func(t *testing.T) {
		first := int32(5)
		after := encodeCursor(9) // After last item
		conn, err := PaginateProducts(products, &first, &after, nil, nil)
		require.NoError(t, err)
		assert.Equal(t, 0, len(conn.Edges))
		assert.False(t, conn.PageInfo.HasNextPage)
	})

	t.Run("should return error for invalid after cursor", func(t *testing.T) {
		first := int32(5)
		invalidCursor := "invalid"
		_, err := PaginateProducts(products, &first, &invalidCursor, nil, nil)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "invalid after cursor")
	})

	t.Run("should set cursors correctly", func(t *testing.T) {
		first := int32(5)
		conn, err := PaginateProducts(products, &first, nil, nil, nil)
		require.NoError(t, err)
		require.NotNil(t, conn.PageInfo.StartCursor)
		require.NotNil(t, conn.PageInfo.EndCursor)

		// Verify start cursor decodes to index 0
		startIndex, err := decodeCursor(*conn.PageInfo.StartCursor)
		require.NoError(t, err)
		assert.Equal(t, 0, startIndex)

		// Verify end cursor decodes to index 4
		endIndex, err := decodeCursor(*conn.PageInfo.EndCursor)
		require.NoError(t, err)
		assert.Equal(t, 4, endIndex)
	})
}

func TestApplyCursorPagination(t *testing.T) {
	// Create test edges
	edges := make([]*catalog.Product, 10)
	for i := 0; i < 10; i++ {
		edges[i] = &catalog.Product{
			ID:  string(rune('A' + i)),
			SKU: string(rune('A' + i)),
		}
	}

	// Convert to ProductEdges (simplified for testing)
	productEdges := make([]*model.ProductEdge, len(edges))
	for i, e := range edges {
		productEdges[i] = &model.ProductEdge{
			Cursor: encodeCursor(i),
			Node:   CatalogProductToGraphQL(e),
		}
	}

	t.Run("should slice with after cursor", func(t *testing.T) {
		after := encodeCursor(2)
		result, hasNext, hasPrev, err := applyCursorPagination(productEdges, nil, &after, nil, nil)
		require.NoError(t, err)
		assert.Equal(t, 7, len(result)) // Items D-J
		assert.False(t, hasNext)
		assert.True(t, hasPrev)
	})

	t.Run("should slice with before cursor", func(t *testing.T) {
		before := encodeCursor(7)
		result, hasNext, hasPrev, err := applyCursorPagination(productEdges, nil, nil, nil, &before)
		require.NoError(t, err)
		assert.Equal(t, 7, len(result)) // Items A-G
		assert.True(t, hasNext)
		assert.False(t, hasPrev)
	})

	t.Run("should combine after and before", func(t *testing.T) {
		after := encodeCursor(2)  // After C
		before := encodeCursor(7) // Before H
		result, hasNext, hasPrev, err := applyCursorPagination(productEdges, nil, &after, nil, &before)
		require.NoError(t, err)
		assert.Equal(t, 4, len(result)) // Items D, E, F, G
		assert.True(t, hasNext)
		assert.True(t, hasPrev)
	})

	t.Run("should apply first limit", func(t *testing.T) {
		first := int32(3)
		result, hasNext, hasPrev, err := applyCursorPagination(productEdges, &first, nil, nil, nil)
		require.NoError(t, err)
		assert.Equal(t, 3, len(result))
		assert.True(t, hasNext)
		assert.False(t, hasPrev)
	})

	t.Run("should apply last limit", func(t *testing.T) {
		last := int32(3)
		result, hasNext, hasPrev, err := applyCursorPagination(productEdges, nil, nil, &last, nil)
		require.NoError(t, err)
		assert.Equal(t, 3, len(result))
		assert.False(t, hasNext)
		assert.True(t, hasPrev)
	})
}
