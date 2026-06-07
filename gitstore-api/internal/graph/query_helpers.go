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
		product, err := r.service.GetProductByUID(ctx, rawID)
		if err != nil {
			return nil, nil
		}
		return DatastoreProductToGraphQL(product), nil
	case nodeKindCategory:
		category, err := r.service.GetCategoryTaxonomyByUID(ctx, rawID)
		if err != nil {
			return nil, nil
		}
		return DatastoreCategoryTaxonomyToGraphQL(category), nil
	case nodeKindCollection:
		collection, err := r.service.GetCollectionByUID(ctx, rawID)
		if err != nil {
			return nil, nil
		}
		return DatastoreCollectionToGraphQL(collection), nil
	case nodeKindNamespace:
		namespace, err := r.service.GetNamespaceByID(ctx, rawID)
		if err != nil {
			return nil, nil
		}
		return datastoreNamespaceToModel(namespace), nil
	case nodeKindRepository:
		repo, err := r.service.GetRepository(ctx, rawID)
		if err != nil {
			return nil, nil
		}
		ns, err := r.service.GetNamespaceByID(ctx, repo.NamespaceID)
		if err != nil {
			return nil, nil
		}
		return datastoreRepositoryToModel(repo, ns, r.storageDataDir), nil
	default:
		return nil, nil
	}
}
