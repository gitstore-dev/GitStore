// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

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
	start, end, hasNextPage, hasPreviousPage, err := applyCursorWindow(len(allEdges), first, after, last, before)
	if err != nil {
		return nil, false, false, err
	}
	return allEdges[start:end], hasNextPage, hasPreviousPage, nil
}

func applyCursorWindow(
	totalCount int,
	first *int32,
	after *string,
	last *int32,
	before *string,
) (int, int, bool, bool, error) {
	if first != nil && *first < 0 {
		return 0, 0, false, false, fmt.Errorf("first must be non-negative")
	}
	if last != nil && *last < 0 {
		return 0, 0, false, false, fmt.Errorf("last must be non-negative")
	}

	// Start with all edges
	start := 0
	end := totalCount

	// Apply 'after' cursor
	if after != nil {
		afterIndex, err := decodeCursor(*after)
		if err != nil {
			return 0, 0, false, false, fmt.Errorf("invalid after cursor: %w", err)
		}
		if afterIndex+1 < totalCount {
			start = afterIndex + 1
		} else {
			start = totalCount
		}
	}

	// Apply 'before' cursor
	if before != nil {
		beforeIndex, err := decodeCursor(*before)
		if err != nil {
			return 0, 0, false, false, fmt.Errorf("invalid before cursor: %w", err)
		}
		if beforeIndex < end {
			end = beforeIndex
		}
	}
	if end < start {
		end = start
	}

	// Apply 'first' limit
	hasNextPage := false
	if first != nil {
		limit := int(*first)
		if limit < end-start {
			end = start + limit
			hasNextPage = true
		}
	}

	// Apply 'last' limit
	hasPreviousPage := false
	if last != nil {
		limit := int(*last)
		if limit < end-start {
			start = end - limit
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

	return start, end, hasNextPage, hasPreviousPage, nil
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

// PaginateCategories applies Relay-style cursor pagination to a category list.
func PaginateCategories(
	categories []*catalog.Category,
	first *int32,
	after *string,
	last *int32,
	before *string,
) (*model.CategoryConnection, error) {
	allEdges := make([]*model.CategoryEdge, len(categories))
	for i, c := range categories {
		allEdges[i] = &model.CategoryEdge{
			Cursor: encodeCursor(i),
			Node:   CatalogCategoryToGraphQL(c),
		}
	}

	start, end, hasNextPage, hasPreviousPage, err := applyCursorWindow(len(allEdges), first, after, last, before)
	if err != nil {
		return nil, err
	}
	edges := allEdges[start:end]

	var startCursor, endCursor *string
	if len(edges) > 0 {
		start := edges[0].Cursor
		end := edges[len(edges)-1].Cursor
		startCursor = &start
		endCursor = &end
	}

	return &model.CategoryConnection{
		Edges:      edges,
		TotalCount: int32(len(categories)),
		PageInfo: &model.PageInfo{
			HasNextPage:     hasNextPage,
			HasPreviousPage: hasPreviousPage,
			StartCursor:     startCursor,
			EndCursor:       endCursor,
		},
	}, nil
}

// PaginateCollections applies Relay-style cursor pagination to a collection list.
func PaginateCollections(
	collections []*catalog.Collection,
	first *int32,
	after *string,
	last *int32,
	before *string,
) (*model.CollectionConnection, error) {
	allEdges := make([]*model.CollectionEdge, len(collections))
	for i, c := range collections {
		allEdges[i] = &model.CollectionEdge{
			Cursor: encodeCursor(i),
			Node:   CatalogCollectionToGraphQL(c),
		}
	}

	start, end, hasNextPage, hasPreviousPage, err := applyCursorWindow(len(allEdges), first, after, last, before)
	if err != nil {
		return nil, err
	}
	edges := allEdges[start:end]

	var startCursor, endCursor *string
	if len(edges) > 0 {
		start := edges[0].Cursor
		end := edges[len(edges)-1].Cursor
		startCursor = &start
		endCursor = &end
	}

	return &model.CollectionConnection{
		Edges:      edges,
		TotalCount: int32(len(collections)),
		PageInfo: &model.PageInfo{
			HasNextPage:     hasNextPage,
			HasPreviousPage: hasPreviousPage,
			StartCursor:     startCursor,
			EndCursor:       endCursor,
		},
	}, nil
}

// PaginateNamespaces applies Relay-style cursor pagination to a namespace list.
func PaginateNamespaces(
	namespaces []*model.Namespace,
	first *int32,
	after *string,
	last *int32,
	before *string,
) (*model.NamespaceConnection, error) {
	allEdges := make([]*model.NamespaceEdge, len(namespaces))
	for i, ns := range namespaces {
		allEdges[i] = &model.NamespaceEdge{
			Cursor: encodeCursor(i),
			Node:   ns,
		}
	}

	start, end, hasNextPage, hasPreviousPage, err := applyCursorWindow(len(allEdges), first, after, last, before)
	if err != nil {
		return nil, err
	}
	edges := allEdges[start:end]

	var startCursor, endCursor *string
	if len(edges) > 0 {
		start := edges[0].Cursor
		end := edges[len(edges)-1].Cursor
		startCursor = &start
		endCursor = &end
	}

	return &model.NamespaceConnection{
		Edges:      edges,
		TotalCount: int32(len(namespaces)),
		PageInfo: &model.PageInfo{
			HasNextPage:     hasNextPage,
			HasPreviousPage: hasPreviousPage,
			StartCursor:     startCursor,
			EndCursor:       endCursor,
		},
	}, nil
}
