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
