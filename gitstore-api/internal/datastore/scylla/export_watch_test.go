// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

//go:build scylla

package scylla

import "github.com/gitstore-dev/gitstore/api/internal/datastore"

// WatchBucketSizeForTest exposes the bucket size a store currently uses.
func WatchBucketSizeForTest(ds datastore.Datastore) int64 {
	return ds.(*scyllaDatastore).namespaceWatchBucketSize.Load()
}
