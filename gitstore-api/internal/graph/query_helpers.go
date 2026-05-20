// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package graph

import (
	"context"

	"github.com/gitstore-dev/gitstore/api/internal/graph/model"
)

func (r *queryResolver) resolveNode(ctx context.Context, kind, rawID string) (model.Node, error) {
	switch kind {
	case nodeKindProduct:
		product, err := r.service.GetProductByID(ctx, rawID)
		if err != nil {
			return nil, nil
		}
		return CatalogProductToGraphQL(product), nil
	case nodeKindCategory:
		category, err := r.service.GetCategoryByID(ctx, rawID)
		if err != nil {
			return nil, nil
		}
		return CatalogCategoryToGraphQL(category), nil
	case nodeKindCollection:
		collection, err := r.service.GetCollectionByID(ctx, rawID)
		if err != nil {
			return nil, nil
		}
		return CatalogCollectionToGraphQL(collection), nil
	case nodeKindNamespace:
		namespace, err := r.service.GetNamespaceByID(ctx, rawID)
		if err != nil {
			return nil, nil
		}
		return datastoreNamespaceToModel(namespace), nil
	default:
		return nil, nil
	}
}

func copyProductFilter(filter *model.ProductFilter) *model.ProductFilter {
	if filter == nil {
		return nil
	}
	copied := *filter
	return &copied
}
