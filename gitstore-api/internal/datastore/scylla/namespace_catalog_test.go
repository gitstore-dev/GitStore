// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package scylla

import (
	"context"
	"testing"

	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	"github.com/stretchr/testify/require"
)

func TestCatalogBatchRejectsLifecycleWrites(t *testing.T) {
	store := &scyllaDatastore{}
	scope := &catalogFenceScope{store: store, namespace: "test"}
	scope.active.Store(true)
	ctx := context.WithValue(t.Context(), catalogFenceScopeKey{}, scope)
	called := false
	err := store.withNamespaceCatalogFence(ctx, "test", true, func() error {
		called = true
		return nil
	})
	require.ErrorIs(t, err, datastore.ErrInvalidArgument)
	require.False(t, called)
}

func WithNamespaceCatalogFenceForTest(ctx context.Context, store datastore.Datastore, namespace string, operation func(context.Context) error) error {
	return store.(*scyllaDatastore).withNamespaceCatalogFence(ctx, namespace, false, func() error { return operation(ctx) })
}

func ReleaseNamespaceCatalogForTest(ctx context.Context, store datastore.Datastore, uid string, epoch int64) error {
	return store.(*scyllaDatastore).releaseNamespaceCatalog(ctx, uid, epoch)
}
