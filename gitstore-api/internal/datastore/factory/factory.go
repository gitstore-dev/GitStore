// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package factory

import (
	"fmt"

	"github.com/gitstore-dev/gitstore/api/internal/config"
	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	"github.com/gitstore-dev/gitstore/api/internal/datastore/memdb"
	"github.com/gitstore-dev/gitstore/api/internal/datastore/scylla"
	"go.uber.org/zap"
)

// NewDatastore constructs the active Datastore backend from cfg.
// Returns an error immediately if the backend value is unrecognised or
// if the backend cannot be initialised (e.g. ScyllaDB unreachable).
// watchConfig optionally overrides the durable watch journal's retention;
// the journal's bucket size is fixed at config.JournalBucketSize.
func NewDatastore(cfg config.DatastoreConfig, log *zap.Logger, watchConfig ...config.WatchJournalConfig) (datastore.Datastore, error) {
	var watch config.WatchJournalConfig
	if len(watchConfig) > 0 {
		watch = watchConfig[0]
	}
	switch cfg.Backend {
	case "memdb":
		return memdb.New(watch.Retention)
	case "scylla":
		return scylla.New(cfg.Scylla, log, config.JournalBucketSize)
	default:
		return nil, fmt.Errorf("invalid datastore backend %q; valid values: memdb, scylla", cfg.Backend)
	}
}

// NamespaceWatchJournal resolves the optional watch capability before callers
// wrap the datastore with instrumentation that intentionally exposes only the
// core Datastore interface.
func NamespaceWatchJournal(store datastore.Datastore) (datastore.NamespaceWatchJournal, error) {
	capable, ok := store.(datastore.NamespaceWatchCapable)
	if !ok || capable.NamespaceWatchJournal() == nil {
		return nil, fmt.Errorf("datastore does not implement the resource watch journal capability")
	}
	return capable.NamespaceWatchJournal(), nil
}

// ResourceWatchJournal resolves the generic durable-watch capability. New
// resource controllers and GraphQL projections use this rather than a
// Namespace-named accessor.
func ResourceWatchJournal(store datastore.Datastore) (datastore.ResourceWatchJournal, error) {
	capable, ok := store.(datastore.ResourceWatchCapable)
	if !ok || capable.ResourceWatchJournal() == nil {
		return nil, fmt.Errorf("datastore does not implement the resource watch journal capability")
	}
	return capable.ResourceWatchJournal(), nil
}
