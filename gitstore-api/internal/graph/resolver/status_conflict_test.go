// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package resolver

import (
	"context"
	"errors"
	"testing"

	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	"github.com/gitstore-dev/gitstore/api/internal/datastore/memdb"
	"github.com/gitstore-dev/gitstore/api/internal/graph/model"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"github.com/vektah/gqlparser/v2/gqlerror"
	"go.uber.org/zap"
)

func TestStatusConflictsAreGraphQLErrorsForNamespaceAndProduct(t *testing.T) {
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
	r, err := NewResolver(ResolverDeps{Store: store, Logger: zap.NewNop()})
	require.NoError(t, err)
	mutation := &mutationResolver{Resolver: r}

	for _, update := range []func() error{
		func() error {
			_, err := mutation.UpdateResourceStatus(ctx, model.UpdateResourceStatusInput{Kind: "Namespace", Name: "acme", ResourceVersion: "stale"})
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
