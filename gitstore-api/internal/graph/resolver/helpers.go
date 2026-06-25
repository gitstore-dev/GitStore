// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package resolver

import (
	"context"

	"github.com/gitstore-dev/gitstore/api/internal/auth"
	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	"github.com/gitstore-dev/gitstore/api/internal/graph/model"
)

// Helper functions for GraphQL resolvers

// namespaceFromContext extracts the namespace from the request context.
// Falls back to an empty string (which lists across all namespaces in memdb).
func namespaceFromContext(_ context.Context) string {
	return ""
}

// callerUsernameOrAnon extracts the caller username from auth context, or returns "anon".
func callerUsernameOrAnon(ctx context.Context, _ *mutationResolver) string {
	if p := auth.PrincipalFromContext(ctx); p != nil {
		return p.Subject
	}
	return "anon"
}

// getCatalogStats returns product/category/collection counts from the datastore.
func (r *Resolver) getCatalogStats(ctx context.Context) *model.CatalogStats {
	products, _ := r.service.GetProducts(ctx, "", datastore.PageParams{First: 1})
	categories, _ := r.service.GetCategoryTaxonomies(ctx, "", datastore.PageParams{First: 1})
	collections, _ := r.service.GetCollections(ctx, "", datastore.PageParams{First: 1})
	var pc, cc, colc int32
	if products != nil {
		pc = products.TotalCount
	}
	if categories != nil {
		cc = categories.TotalCount
	}
	if collections != nil {
		colc = collections.TotalCount
	}
	return &model.CatalogStats{
		ProductCount:       pc,
		CategoryCount:      cc,
		CollectionCount:    colc,
		OrphanedReferences: 0,
	}
}
