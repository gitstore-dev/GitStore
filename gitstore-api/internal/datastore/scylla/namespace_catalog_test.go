// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package scylla

import (
	"context"

	"github.com/gitstore-dev/gitstore/api/internal/datastore"
)

func WithNamespaceCatalogFenceForTest(ctx context.Context, store datastore.Datastore, namespace string, operation func(context.Context) error) error {
	return store.(*scyllaDatastore).withNamespaceCatalogFence(ctx, namespace, false, func() error { return operation(ctx) })
}

func ReleaseNamespaceCatalogForTest(ctx context.Context, store datastore.Datastore, uid string, epoch int64) error {
	return store.(*scyllaDatastore).releaseNamespaceCatalog(ctx, uid, epoch)
}
