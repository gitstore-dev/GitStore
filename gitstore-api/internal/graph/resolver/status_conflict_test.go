// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package resolver

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/gitstore-dev/gitstore/api/internal/catalog"
	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	"github.com/gitstore-dev/gitstore/api/internal/datastore/memdb"
	"github.com/gitstore-dev/gitstore/api/internal/graph/model"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/vektah/gqlparser/v2/gqlerror"
	"go.uber.org/zap"
)

func TestStatusConflictsAreGraphQLErrorsForNamespaceRepositoryAndProduct(t *testing.T) {
	store, err := memdb.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	ctx := context.Background()
	require.NoError(t, store.CreateNamespace(ctx, &datastore.Namespace{
		UID: uuid.NewString(), APIVersion: "gitstore.dev/v1beta1", Kind: "Namespace", Name: "acme", ResourceVersion: "1",
	}))
	require.NoError(t, store.CreateProduct(ctx, &datastore.Product{
		UID: uuid.NewString(), APIVersion: "catalog.gitstore.dev/v1beta1", Kind: "Product", Namespace: "acme", Name: "widget", ResourceVersion: "1",
	}))
	repositoryID := uuid.NewString()
	require.NoError(t, store.CreateRepository(ctx, &datastore.Repository{
		UID: repositoryID, ID: repositoryID, RepositoryID: repositoryID, Namespace: "acme", NamespaceID: "acme", Name: "catalog", ResourceVersion: "1",
	}))
	require.NoError(t, store.CreateNamespaceMapping(ctx, &datastore.NamespaceMapping{Namespace: "acme", NamespaceID: "acme", Name: "catalog", RepositoryID: repositoryID, RepoID: repositoryID}))
	r, err := NewResolver(ResolverDeps{Store: store, Logger: zap.NewNop()})
	require.NoError(t, err)
	mutation := &mutationResolver{Resolver: r}

	for _, update := range []func() error{
		func() error {
			_, err := mutation.UpdateResourceStatus(ctx, model.UpdateResourceStatusInput{Kind: "Namespace", Name: "acme", ResourceVersion: "stale"})
			return err
		},
		func() error {
			_, err := mutation.UpdateResourceStatus(ctx, model.UpdateResourceStatusInput{Kind: "Repository", Namespace: "acme", Name: "catalog", ResourceVersion: "stale"})
			return err
		},
		func() error {
			_, err := mutation.UpdateProductStatus(ctx, model.UpdateProductStatusInput{Namespace: "acme", Name: "widget", ResourceVersion: "stale"})
			return err
		},
	} {
		err := update()
		var graphErr *gqlerror.Error
		require.True(t, errors.As(err, &graphErr))
		require.Equal(t, "RESOURCE_VERSION_CONFLICT", graphErr.Extensions["code"])
		require.Equal(t, "1", graphErr.Extensions["resourceVersion"])
	}
}

func TestUpdateProductStatusMergesSystemConditionsWithoutClobbering(t *testing.T) {
	store, err := memdb.New()
	require.NoError(t, err)
	ctx := context.Background()
	require.NoError(t, store.CreateNamespace(ctx, &datastore.Namespace{UID: uuid.NewString(), Name: "acme", ResourceVersion: "1"}))
	existing, err := json.Marshal(catalog.ProductStatus{Conditions: []catalog.Condition{{Type: catalog.ConditionAdmissionAccepted, Status: catalog.ConditionTrue}}})
	require.NoError(t, err)
	product := &datastore.Product{UID: uuid.NewString(), APIVersion: "catalog.gitstore.dev/v1beta1", Kind: "Product", Namespace: "acme", Name: "widget", ResourceVersion: "1", Status: existing}
	require.NoError(t, store.CreateProduct(ctx, product))
	r, err := NewResolver(ResolverDeps{Store: store, Logger: zap.NewNop()})
	require.NoError(t, err)
	_, err = (&mutationResolver{Resolver: r}).UpdateProductStatus(ctx, model.UpdateProductStatusInput{Namespace: "acme", Name: "widget", ResourceVersion: "1", Conditions: []*model.ConditionInput{{Type: string(catalog.ConditionCategoryResolved), Status: model.ConditionStatusTrue}}})
	require.NoError(t, err)
	persisted, err := store.GetProduct(ctx, product.UID)
	require.NoError(t, err)
	var status catalog.ProductStatus
	require.NoError(t, json.Unmarshal(persisted.Status, &status))
	require.Len(t, status.Conditions, 2)
	assert.Equal(t, catalog.ConditionAdmissionAccepted, status.Conditions[0].Type)
	assert.Equal(t, catalog.ConditionCategoryResolved, status.Conditions[1].Type)
}
