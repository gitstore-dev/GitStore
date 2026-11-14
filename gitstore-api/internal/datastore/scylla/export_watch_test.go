// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

//go:build scylla

package scylla

import (
	"context"

	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	"github.com/gocql/gocql"
)

// WatchBucketSizeForTest exposes the bucket size a store currently uses.
func WatchBucketSizeForTest(ds datastore.Datastore) int64 {
	return ds.(*scyllaDatastore).namespaceWatchBucketSize.Load()
}

func SaveCDCProgressBatchForTest(ctx context.Context, ds datastore.Datastore, lease datastore.ResourceWatchLease, source string, progress map[string]gocql.UUID) error {
	return ds.(*scyllaDatastore).saveCDCProgressBatch(ctx, lease, source, progress)
}

func NamespaceCDCDeletionReadyForTest(ctx context.Context, ds datastore.Datastore, namespace *datastore.Namespace, committed bool) (bool, error) {
	return ds.(*scyllaDatastore).namespaceCDCDeletionReady(ctx, namespace, committed)
}
