// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

// Relay-style cursor pagination helpers using keyset-based cursors

package graph

import (
	"fmt"

	"github.com/gitstore-dev/gitstore/api/internal/catalog"
	"github.com/gitstore-dev/gitstore/api/internal/graph/model"
)

// PaginateProducts applies Relay-style cursor pagination to a product list using keyset cursors
func PaginateProducts(
	products []*catalog.Product,
	first *int32,
	after *string,
	last *int32,
	before *string,
) (*model.ProductConnection, error) {
	if len(products) == 0 {
		return &model.ProductConnection{
			Edges:      []*model.ProductEdge{},
			TotalCount: 0,
			PageInfo: &model.PageInfo{
				HasNextPage:     false,
				HasPreviousPage: false,
				StartCursor:     nil,
				EndCursor:       nil,
			},
		}, nil
	}

	// Build edges for all products with keyset cursors
	allEdges := make([]*model.ProductEdge, len(products))
	for i, p := range products {
		allEdges[i] = &model.ProductEdge{
			Cursor: EncodeKeysetCursor(p.CreatedAt, p.ID),
			Node:   CatalogProductToGraphQL(p),
		}
	}

	// Apply keyset-based slicing
	start, end, hasNextPage, hasPreviousPage, err := applyKeysetWindow(len(allEdges), first, after, last, before, func(i int) string {
		p := products[i]
		return EncodeKeysetCursor(p.CreatedAt, p.ID)
	})
	if err != nil {
		return nil, err
	}

	edges := allEdges[start:end]

	// Calculate pagination info
	var startCursor, endCursor *string
	if len(edges) > 0 {
		sc := edges[0].Cursor
		ec := edges[len(edges)-1].Cursor
		startCursor = &sc
		endCursor = &ec
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

// applyKeysetWindow applies keyset-based pagination window slicing
func applyKeysetWindow(
	totalCount int,
	first *int32,
	after *string,
	last *int32,
	before *string,
	getCursor func(int) string,
) (int, int, bool, bool, error) {
	if first != nil && *first < 0 {
		return 0, 0, false, false, nil
	}
	if last != nil && *last < 0 {
		return 0, 0, false, false, nil
	}

	start := 0
	end := totalCount

	// Apply 'after' cursor: find first position after the cursor
	if after != nil && *after != "" {
		if _, err := DecodeKeysetCursor(*after); err != nil {
			return 0, 0, false, false, fmt.Errorf("invalid after cursor: %w", err)
		}
		found := false
		for i := 0; i < totalCount; i++ {
			cmp, err := compareEncodedKeysetCursors(getCursor(i), *after)
			if err != nil {
				return 0, 0, false, false, err
			}
			if cmp > 0 {
				start = i
				found = true
				break
			}
		}
		if !found {
			start = end
		}
	}

	// Apply 'before' cursor: find first position at or after the cursor
	if before != nil && *before != "" {
		if _, err := DecodeKeysetCursor(*before); err != nil {
			return 0, 0, false, false, fmt.Errorf("invalid before cursor: %w", err)
		}
		for i := 0; i < totalCount; i++ {
			cmp, err := compareEncodedKeysetCursors(getCursor(i), *before)
			if err != nil {
				return 0, 0, false, false, err
			}
			if cmp >= 0 {
				end = i
				break
			}
		}
	}

	if start >= end {
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

	// Adjust hasPreviousPage/hasNextPage based on cursor position
	if after != nil && start > 0 {
		hasPreviousPage = true
	}
	if before != nil && end < totalCount {
		hasNextPage = true
	}

	return start, end, hasNextPage, hasPreviousPage, nil
}

func compareEncodedKeysetCursors(left, right string) (int, error) {
	lc, err := DecodeKeysetCursor(left)
	if err != nil {
		return 0, fmt.Errorf("invalid generated cursor: %w", err)
	}
	rc, err := DecodeKeysetCursor(right)
	if err != nil {
		return 0, fmt.Errorf("invalid pagination cursor: %w", err)
	}
	cmpTime := lc.CreatedAt.Compare(rc.CreatedAt)
	if cmpTime != 0 {
		return cmpTime, nil
	}
	switch {
	case lc.ID < rc.ID:
		return -1, nil
	case lc.ID > rc.ID:
		return 1, nil
	default:
		return 0, nil
	}
}

// PaginateCategories applies Relay-style cursor pagination to a category list using keyset cursors
func PaginateCategories(
	categories []*catalog.Category,
	first *int32,
	after *string,
	last *int32,
	before *string,
) (*model.CategoryConnection, error) {
	if len(categories) == 0 {
		return &model.CategoryConnection{
			Edges:      []*model.CategoryEdge{},
			TotalCount: 0,
			PageInfo: &model.PageInfo{
				HasNextPage:     false,
				HasPreviousPage: false,
				StartCursor:     nil,
				EndCursor:       nil,
			},
		}, nil
	}

	allEdges := make([]*model.CategoryEdge, len(categories))
	for i, c := range categories {
		allEdges[i] = &model.CategoryEdge{
			Cursor: EncodeKeysetCursor(c.CreatedAt, c.ID),
			Node:   CatalogCategoryToGraphQL(c),
		}
	}

	start, end, hasNextPage, hasPreviousPage, err := applyKeysetWindow(len(allEdges), first, after, last, before, func(i int) string {
		c := categories[i]
		return EncodeKeysetCursor(c.CreatedAt, c.ID)
	})
	if err != nil {
		return nil, err
	}
	edges := allEdges[start:end]

	var startCursor, endCursor *string
	if len(edges) > 0 {
		sc := edges[0].Cursor
		ec := edges[len(edges)-1].Cursor
		startCursor = &sc
		endCursor = &ec
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

// PaginateCollections applies Relay-style cursor pagination to a collection list using keyset cursors
func PaginateCollections(
	collections []*catalog.Collection,
	first *int32,
	after *string,
	last *int32,
	before *string,
) (*model.CollectionConnection, error) {
	if len(collections) == 0 {
		return &model.CollectionConnection{
			Edges:      []*model.CollectionEdge{},
			TotalCount: 0,
			PageInfo: &model.PageInfo{
				HasNextPage:     false,
				HasPreviousPage: false,
				StartCursor:     nil,
				EndCursor:       nil,
			},
		}, nil
	}

	allEdges := make([]*model.CollectionEdge, len(collections))
	for i, c := range collections {
		allEdges[i] = &model.CollectionEdge{
			Cursor: EncodeKeysetCursor(c.CreatedAt, c.ID),
			Node:   CatalogCollectionToGraphQL(c),
		}
	}

	start, end, hasNextPage, hasPreviousPage, err := applyKeysetWindow(len(allEdges), first, after, last, before, func(i int) string {
		c := collections[i]
		return EncodeKeysetCursor(c.CreatedAt, c.ID)
	})
	if err != nil {
		return nil, err
	}
	edges := allEdges[start:end]

	var startCursor, endCursor *string
	if len(edges) > 0 {
		sc := edges[0].Cursor
		ec := edges[len(edges)-1].Cursor
		startCursor = &sc
		endCursor = &ec
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

// PaginateNamespaces applies Relay-style cursor pagination to a namespace list using keyset cursors
func PaginateNamespaces(
	namespaces []*model.Namespace,
	first *int32,
	after *string,
	last *int32,
	before *string,
) (*model.NamespaceConnection, error) {
	if len(namespaces) == 0 {
		return &model.NamespaceConnection{
			Edges:      []*model.NamespaceEdge{},
			TotalCount: 0,
			PageInfo: &model.PageInfo{
				HasNextPage:     false,
				HasPreviousPage: false,
				StartCursor:     nil,
				EndCursor:       nil,
			},
		}, nil
	}

	allEdges := make([]*model.NamespaceEdge, len(namespaces))
	for i, ns := range namespaces {
		allEdges[i] = &model.NamespaceEdge{
			Cursor: EncodeKeysetCursor(ns.CreatedAt, ns.ID),
			Node:   ns,
		}
	}

	start, end, hasNextPage, hasPreviousPage, err := applyKeysetWindow(len(allEdges), first, after, last, before, func(i int) string {
		ns := namespaces[i]
		return EncodeKeysetCursor(ns.CreatedAt, ns.ID)
	})
	if err != nil {
		return nil, err
	}
	edges := allEdges[start:end]

	var startCursor, endCursor *string
	if len(edges) > 0 {
		sc := edges[0].Cursor
		ec := edges[len(edges)-1].Cursor
		startCursor = &sc
		endCursor = &ec
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
