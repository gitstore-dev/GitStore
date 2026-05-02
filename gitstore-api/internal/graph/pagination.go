// Relay-style cursor pagination helpers

package graph

import (
	"encoding/base64"
	"fmt"
	"strconv"
	"strings"

	"github.com/gitstore-dev/gitstore/api/internal/catalog"
	"github.com/gitstore-dev/gitstore/api/internal/graph/model"
)

// PaginateProducts applies Relay-style cursor pagination to a product list
func PaginateProducts(
	products []*catalog.Product,
	first *int32,
	after *string,
	last *int32,
	before *string,
) (*model.ProductConnection, error) {
	// Build edges for all products
	allEdges := make([]*model.ProductEdge, len(products))
	for i, p := range products {
		allEdges[i] = &model.ProductEdge{
			Cursor: encodeCursor(i),
			Node:   CatalogProductToGraphQL(p),
		}
	}

	// Apply cursor-based slicing
	edges, hasNextPage, hasPreviousPage, err := applyCursorPagination(
		allEdges,
		first,
		after,
		last,
		before,
	)
	if err != nil {
		return nil, err
	}

	// Calculate pagination info
	var startCursor, endCursor *string
	if len(edges) > 0 {
		start := edges[0].Cursor
		end := edges[len(edges)-1].Cursor
		startCursor = &start
		endCursor = &end
	}

	return &model.ProductConnection{
		Edges:      edges,
		TotalCount: int32(len(products)),
		PageInfo: &model.PageInfo{
			HasNextPage:     hasNextPage,
			HasPreviousPage: hasPreviousPage,
			StartCursor:     startCursor,
			EndCursor:       endCursor,
		},
	}, nil
}

// applyCursorPagination applies Relay cursor pagination to edges
func applyCursorPagination(
	allEdges []*model.ProductEdge,
	first *int32,
	after *string,
	last *int32,
	before *string,
) ([]*model.ProductEdge, bool, bool, error) {
	// Start with all edges
	edges := allEdges
	totalCount := len(allEdges)

	// Apply 'after' cursor
	if after != nil {
		afterIndex, err := decodeCursor(*after)
		if err != nil {
			return nil, false, false, fmt.Errorf("invalid after cursor: %w", err)
		}
		if afterIndex+1 < totalCount {
			edges = edges[afterIndex+1:]
		} else {
			edges = []*model.ProductEdge{}
		}
	}

	// Apply 'before' cursor
	if before != nil {
		beforeIndex, err := decodeCursor(*before)
		if err != nil {
			return nil, false, false, fmt.Errorf("invalid before cursor: %w", err)
		}
		// Adjust index if we've already sliced with 'after'
		adjustedIndex := beforeIndex
		if after != nil {
			afterIndex, _ := decodeCursor(*after)
			adjustedIndex = beforeIndex - (afterIndex + 1)
		}
		if adjustedIndex > 0 && adjustedIndex <= len(edges) {
			edges = edges[:adjustedIndex]
		}
	}

	// Apply 'first' limit
	hasNextPage := false
	if first != nil {
		limit := int(*first)
		if limit < len(edges) {
			edges = edges[:limit]
			hasNextPage = true
		}
	}

	// Apply 'last' limit
	hasPreviousPage := false
	if last != nil {
		limit := int(*last)
		if limit < len(edges) {
			edges = edges[len(edges)-limit:]
			hasPreviousPage = true
		}
	}

	// Check if there are more pages
	if after != nil {
		afterIndex, _ := decodeCursor(*after)
		// If we sliced after a cursor, there's always a previous page
		if afterIndex >= 0 {
			hasPreviousPage = true
		}
	}

	if before != nil {
		beforeIndex, _ := decodeCursor(*before)
		// If we sliced before a cursor and it's not the end, there's a next page
		if beforeIndex < totalCount-1 {
			hasNextPage = true
		}
	}

	return edges, hasNextPage, hasPreviousPage, nil
}

// encodeCursor creates a base64-encoded cursor from an index
func encodeCursor(index int) string {
	cursor := fmt.Sprintf("arrayconnection:%d", index)
	return base64.StdEncoding.EncodeToString([]byte(cursor))
}

// decodeCursor decodes a base64 cursor to an index
func decodeCursor(cursor string) (int, error) {
	decoded, err := base64.StdEncoding.DecodeString(cursor)
	if err != nil {
		return 0, fmt.Errorf("invalid base64 encoding: %w", err)
	}

	// Parse cursor format: "arrayconnection:INDEX"
	parts := strings.Split(string(decoded), ":")
	if len(parts) != 2 || parts[0] != "arrayconnection" {
		return 0, fmt.Errorf("invalid cursor format")
	}

	index, err := strconv.Atoi(parts[1])
	if err != nil {
		return 0, fmt.Errorf("invalid cursor index: %w", err)
	}

	return index, nil
}

// PaginateCategories applies pagination to categories (future extension)
func PaginateCategories(
	categories []*catalog.Category,
	first *int32,
	after *string,
	last *int32,
	before *string,
) ([]*catalog.Category, error) {
	// For now, return all categories (categories are typically small)
	// Future: implement cursor pagination if needed
	return categories, nil
}

// PaginateCollections applies pagination to collections (future extension)
func PaginateCollections(
	collections []*catalog.Collection,
	first *int32,
	after *string,
	last *int32,
	before *string,
) ([]*catalog.Collection, error) {
	// For now, return all collections (collections are typically small)
	// Future: implement cursor pagination if needed
	return collections, nil
}
