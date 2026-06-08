// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

// Relay-style connection builders powered by generic datastore.PageResult.

package graph

import (
	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	"github.com/gitstore-dev/gitstore/api/internal/graph/model"
)

// toPageParams converts GraphQL pagination arguments to datastore.PageParams.
func toPageParams(first *int32, after *string, last *int32, before *string) datastore.PageParams {
	p := datastore.PageParams{}
	if first != nil {
		p.First = int(*first)
	}
	if after != nil {
		p.After = *after
	}
	if last != nil {
		p.Last = int(*last)
	}
	if before != nil {
		p.Before = *before
	}
	return p
}

// buildPageInfo constructs PageInfo from a PageResult.
func buildPageInfo[T any](result *datastore.PageResult[T], cursorFn func(*T) string) *model.PageInfo {
	pi := &model.PageInfo{
		HasNextPage:     result.HasNext,
		HasPreviousPage: result.HasPrevious,
	}
	if len(result.Items) > 0 {
		sc := cursorFn(result.Items[0])
		ec := cursorFn(result.Items[len(result.Items)-1])
		pi.StartCursor = &sc
		pi.EndCursor = &ec
	}
	return pi
}

// BuildProductConnectionFromSlice builds a paginated ProductConnection from a flat slice.
// It applies cursor-based keyset pagination (after/before) and honours first/last limits.
// Products must be pre-sorted by (CreationTimestamp DESC, UID DESC) for cursor seeks to be stable.
func BuildProductConnectionFromSlice(products []*datastore.Product, params datastore.PageParams) *model.ProductConnection {
	// Apply cursor filter
	if params.After != "" {
		if c, err := DecodeKeysetCursor(params.After); err == nil {
			for i, p := range products {
				if p.CreationTimestamp.Equal(c.CreatedAt) && p.UID == c.ID {
					products = products[i+1:]
					break
				}
			}
		}
	} else if params.Before != "" {
		if c, err := DecodeKeysetCursor(params.Before); err == nil {
			for i, p := range products {
				if p.CreationTimestamp.Equal(c.CreatedAt) && p.UID == c.ID {
					products = products[:i]
					break
				}
			}
		}
	}

	totalFiltered := len(products)
	limit := params.Limit()
	hasNext, hasPrevious := false, false

	if params.Last > 0 {
		if len(products) > limit {
			products = products[len(products)-limit:]
			hasPrevious = true
		}
		hasNext = params.Before != ""
	} else {
		if len(products) > limit {
			products = products[:limit]
			hasNext = true
		}
		hasPrevious = params.After != ""
	}

	edges := make([]*model.ProductEdge, len(products))
	for i, p := range products {
		edges[i] = &model.ProductEdge{
			Cursor: EncodeKeysetCursor(p.CreationTimestamp, p.UID),
			Node:   DatastoreProductToGraphQL(p),
		}
	}
	pi := &model.PageInfo{HasNextPage: hasNext, HasPreviousPage: hasPrevious}
	if len(products) > 0 {
		sc := EncodeKeysetCursor(products[0].CreationTimestamp, products[0].UID)
		ec := EncodeKeysetCursor(products[len(products)-1].CreationTimestamp, products[len(products)-1].UID)
		pi.StartCursor = &sc
		pi.EndCursor = &ec
	}
	return &model.ProductConnection{
		Edges:      edges,
		PageInfo:   pi,
		TotalCount: int32(totalFiltered),
	}
}

// BuildProductConnection converts a PageResult[Product] into a GraphQL ProductConnection.
func BuildProductConnection(result *datastore.PageResult[datastore.Product]) *model.ProductConnection {
	edges := make([]*model.ProductEdge, len(result.Items))
	for i, p := range result.Items {
		edges[i] = &model.ProductEdge{
			Cursor: EncodeKeysetCursor(p.CreationTimestamp, p.UID),
			Node:   DatastoreProductToGraphQL(p),
		}
	}
	return &model.ProductConnection{
		Edges:      edges,
		TotalCount: result.TotalCount,
		PageInfo:   buildPageInfo(result, func(p *datastore.Product) string { return EncodeKeysetCursor(p.CreationTimestamp, p.UID) }),
	}
}

// BuildCategoryConnection converts a PageResult[CategoryTaxonomy] into a GraphQL CategoryConnection.
func BuildCategoryConnection(result *datastore.PageResult[datastore.CategoryTaxonomy]) *model.CategoryConnection {
	edges := make([]*model.CategoryEdge, len(result.Items))
	for i, c := range result.Items {
		edges[i] = &model.CategoryEdge{
			Cursor: EncodeKeysetCursor(c.CreationTimestamp, c.UID),
			Node:   DatastoreCategoryTaxonomyToGraphQL(c),
		}
	}
	return &model.CategoryConnection{
		Edges:      edges,
		TotalCount: result.TotalCount,
		PageInfo:   buildPageInfo(result, func(c *datastore.CategoryTaxonomy) string { return EncodeKeysetCursor(c.CreationTimestamp, c.UID) }),
	}
}

// BuildCollectionConnection converts a PageResult[Collection] into a GraphQL CollectionConnection.
func BuildCollectionConnection(result *datastore.PageResult[datastore.Collection]) *model.CollectionConnection {
	edges := make([]*model.CollectionEdge, len(result.Items))
	for i, c := range result.Items {
		edges[i] = &model.CollectionEdge{
			Cursor: EncodeKeysetCursor(c.CreationTimestamp, c.UID),
			Node:   DatastoreCollectionToGraphQL(c),
		}
	}
	return &model.CollectionConnection{
		Edges:      edges,
		TotalCount: result.TotalCount,
		PageInfo:   buildPageInfo(result, func(c *datastore.Collection) string { return EncodeKeysetCursor(c.CreationTimestamp, c.UID) }),
	}
}

// BuildVariantConnectionFromSlice builds a ProductVariantConnection from a flat slice with
// cursor-based keyset pagination (after/before) and first/last limits.
// Variants must be pre-sorted by (CreationTimestamp DESC, UID DESC).
func BuildVariantConnectionFromSlice(variants []*datastore.ProductVariant, params datastore.PageParams) *model.ProductVariantConnection {
	if params.After != "" {
		if c, err := DecodeKeysetCursor(params.After); err == nil {
			for i, v := range variants {
				if v.CreationTimestamp.Equal(c.CreatedAt) && v.UID == c.ID {
					variants = variants[i+1:]
					break
				}
			}
		}
	} else if params.Before != "" {
		if c, err := DecodeKeysetCursor(params.Before); err == nil {
			for i, v := range variants {
				if v.CreationTimestamp.Equal(c.CreatedAt) && v.UID == c.ID {
					variants = variants[:i]
					break
				}
			}
		}
	}

	totalFiltered := len(variants)
	limit := params.Limit()
	hasNext, hasPrevious := false, false

	if params.Last > 0 {
		if len(variants) > limit {
			variants = variants[len(variants)-limit:]
			hasPrevious = true
		}
		hasNext = params.Before != ""
	} else {
		if len(variants) > limit {
			variants = variants[:limit]
			hasNext = true
		}
		hasPrevious = params.After != ""
	}

	edges := make([]*model.ProductVariantEdge, len(variants))
	for i, v := range variants {
		edges[i] = &model.ProductVariantEdge{
			Cursor: EncodeKeysetCursor(v.CreationTimestamp, v.UID),
			Node:   DatastoreVariantToGraphQL(v),
		}
	}
	pi := &model.PageInfo{HasNextPage: hasNext, HasPreviousPage: hasPrevious}
	if len(variants) > 0 {
		sc := EncodeKeysetCursor(variants[0].CreationTimestamp, variants[0].UID)
		ec := EncodeKeysetCursor(variants[len(variants)-1].CreationTimestamp, variants[len(variants)-1].UID)
		pi.StartCursor = &sc
		pi.EndCursor = &ec
	}
	return &model.ProductVariantConnection{
		Edges:      edges,
		PageInfo:   pi,
		TotalCount: int32(totalFiltered),
	}
}

// BuildVariantConnection converts a PageResult[ProductVariant] into a GraphQL ProductVariantConnection.
func BuildVariantConnection(result *datastore.PageResult[datastore.ProductVariant]) *model.ProductVariantConnection {
	edges := make([]*model.ProductVariantEdge, len(result.Items))
	for i, v := range result.Items {
		edges[i] = &model.ProductVariantEdge{
			Cursor: EncodeKeysetCursor(v.CreationTimestamp, v.UID),
			Node:   DatastoreVariantToGraphQL(v),
		}
	}
	return &model.ProductVariantConnection{
		Edges:      edges,
		TotalCount: result.TotalCount,
		PageInfo:   buildPageInfo(result, func(v *datastore.ProductVariant) string { return EncodeKeysetCursor(v.CreationTimestamp, v.UID) }),
	}
}

// BuildNamespaceConnection converts a PageResult[Namespace] into a GraphQL NamespaceConnection.
func BuildNamespaceConnection(result *datastore.PageResult[datastore.Namespace]) *model.NamespaceConnection {
	edges := make([]*model.NamespaceEdge, len(result.Items))
	for i, ns := range result.Items {
		edges[i] = &model.NamespaceEdge{
			Cursor: EncodeKeysetCursor(ns.CreatedAt, ns.ID),
			Node:   DatastoreNamespaceToGraphQL(ns),
		}
	}
	return &model.NamespaceConnection{
		Edges:      edges,
		TotalCount: result.TotalCount,
		PageInfo:   buildPageInfo(result, func(ns *datastore.Namespace) string { return EncodeKeysetCursor(ns.CreatedAt, ns.ID) }),
	}
}

// BuildRepositoryConnection converts a PageResult[Repository] into a GraphQL RepositoryConnection.
func BuildRepositoryConnection(result *datastore.PageResult[datastore.Repository]) *model.RepositoryConnection {
	edges := make([]*model.RepositoryEdge, len(result.Items))
	for i, r := range result.Items {
		edges[i] = &model.RepositoryEdge{
			Cursor: EncodeKeysetCursor(r.CreatedAt, r.ID),
			Node:   DatastoreRepositoryToGraphQL(r),
		}
	}
	return &model.RepositoryConnection{
		Edges:      edges,
		TotalCount: result.TotalCount,
		PageInfo:   buildPageInfo(result, func(r *datastore.Repository) string { return EncodeKeysetCursor(r.CreatedAt, r.ID) }),
	}
}
