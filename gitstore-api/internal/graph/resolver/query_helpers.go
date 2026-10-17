// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package resolver

import (
	"context"
	"errors"
	"strings"
	"sync"

	"github.com/99designs/gqlgen/graphql"
	"github.com/gitstore-dev/gitstore/api/internal/admission"
	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	"github.com/gitstore-dev/gitstore/api/internal/graph/model"
	"github.com/gitstore-dev/gitstore/api/internal/middleware/security"
	"github.com/vektah/gqlparser/v2/ast"
	"github.com/vektah/gqlparser/v2/gqlerror"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

const nodeFetchFanout = 8

func (r *queryResolver) resolveNodes(ctx context.Context, ids []string) (result []model.Node, resultErr error) {
	if trace.SpanFromContext(ctx).IsRecording() {
		var span trace.Span
		ctx, span = otel.Tracer("gitstore/graphql").Start(ctx, "graphql.node_resolution")
		defer func() {
			if resultErr != nil {
				span.SetStatus(codes.Error, "node resolution failed")
			}
			span.End()
		}()
	}
	nodes := make([]model.Node, len(ids))
	errs := make([]error, len(ids))
	var next int
	var mu sync.Mutex
	var wg sync.WaitGroup
	for range min(nodeFetchFanout, len(ids)) {
		wg.Go(func() {
			for {
				if ctx.Err() != nil {
					return
				}
				mu.Lock()
				i := next
				next++
				mu.Unlock()
				if i >= len(ids) {
					return
				}
				kind, rawID, err := DecodeNodeID(ids[i])
				if err != nil {
					errs[i] = invalidGlobalIDError(err)
					continue
				}
				nodes[i], errs[i] = r.resolveNode(ctx, kind, rawID)
			}
		})
	}
	wg.Wait()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	for _, err := range errs {
		if err != nil {
			var structured *gqlerror.Error
			if !errors.As(err, &structured) ||
				(structured.Extensions["code"] != string(admission.CodeNotFound) &&
					structured.Extensions["code"] != string(admission.CodeBadUserInput)) {
				return nil, err
			}
		}
	}
	for i, err := range errs {
		if err != nil {
			var structured *gqlerror.Error
			errors.As(err, &structured)
			indexed := *structured
			indexed.Path = append(graphql.GetPath(ctx), ast.PathIndex(i))
			graphql.AddError(ctx, &indexed)
		}
	}
	return nodes, nil
}

func (r *queryResolver) resolveNode(ctx context.Context, kind, rawID string) (model.Node, error) {
	if err := security.AuthorizedNodeError(ctx, kind, rawID); err != nil {
		return nil, err
	}
	switch kind {
	case nodeKindFile:
		if file, ok := security.AuthorizedFileNode(ctx, rawID); ok {
			if file == nil {
				return nil, nodeLookupError(kind, datastore.ErrNotFound)
			}
			return DatastoreFileToGraphQL(file), nil
		}
		file, err := r.store.GetFile(ctx, rawID)
		if err != nil {
			return nil, nodeLookupError(kind, err)
		}
		return DatastoreFileToGraphQL(file), nil
	case nodeKindProduct:
		product, err := r.service.GetProductByUID(ctx, rawID)
		if err != nil {
			return nil, nodeLookupError(kind, err)
		}
		return DatastoreProductToGraphQL(product), nil
	case nodeKindProductVariant:
		variant, err := r.service.GetProductVariantByUID(ctx, rawID)
		if err != nil {
			return nil, nodeLookupError(kind, err)
		}
		return DatastoreVariantToGraphQL(variant), nil
	case nodeKindCategory:
		category, err := r.service.GetCategoryTaxonomyByUID(ctx, rawID)
		if err != nil {
			return nil, nodeLookupError(kind, err)
		}
		return DatastoreCategoryTaxonomyToGraphQL(category), nil
	case nodeKindCollection:
		collection, err := r.service.GetCollectionByUID(ctx, rawID)
		if err != nil {
			return nil, nodeLookupError(kind, err)
		}
		return DatastoreCollectionToGraphQL(collection), nil
	case nodeKindNamespace:
		namespace, err := r.service.GetNamespaceByID(ctx, rawID)
		if err != nil {
			return nil, nodeLookupError(kind, err)
		}
		return DatastoreNamespaceToGraphQL(namespace), nil
	case nodeKindRepository:
		repo, err := r.service.GetRepository(ctx, rawID)
		if err != nil {
			return nil, nodeLookupError(kind, err)
		}
		ns, err := r.service.GetNamespaceByID(ctx, repo.NamespaceID)
		if err != nil {
			return nil, nodeLookupError(kind, err)
		}
		return datastoreRepositoryToModel(repo, ns, r.storageDataDir), nil
	default:
		return nil, nil
	}
}

func nodeLookupError(kind string, err error) error {
	var structured *gqlerror.Error
	if !errors.As(err, &structured) || structured.Extensions["code"] == nil {
		errors.As(admissionMutationGraphQLError(err), &structured)
	}
	if structured != nil && structured.Extensions["code"] != nil {
		if structured.Extensions["code"] == string(admission.CodeNotFound) && structured.Extensions["diagnostics"] == nil {
			result := admission.ResourceNotFound(kind, structured.Message)
			result.Err = err
			return result
		}
		result := *structured
		result.Err = err
		return &result
	}
	if errors.Is(err, datastore.ErrNotFound) {
		result := admission.ResourceNotFound(kind, strings.ToLower(kind)+" not found")
		result.Err = err
		return result
	}
	return &gqlerror.Error{Message: "retrieve " + kind, Err: err}
}
