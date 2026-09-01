// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package factory

import (
	"fmt"
	"time"

	"github.com/gitstore-dev/gitstore/api/internal/config"
	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	"github.com/gitstore-dev/gitstore/api/internal/datastore/memdb"
	"github.com/gitstore-dev/gitstore/api/internal/datastore/scylla"
	"go.uber.org/zap"
)

// NewDatastore constructs the active Datastore backend from cfg.
// Returns an error immediately if the backend value is unrecognised or
// if the backend cannot be initialised (e.g. ScyllaDB unreachable).
func NewDatastore(cfg config.DatastoreConfig, log *zap.Logger, watchConfig ...config.NamespaceWatchConfig) (datastore.Datastore, error) {
	var watch config.NamespaceWatchConfig
	if len(watchConfig) > 0 {
		watch = watchConfig[0]
	}
	switch cfg.Backend {
	case "memdb":
		return memdb.New(time.Duration(watch.JournalRetentionSeconds) * time.Second)
	case "scylla":
		return scylla.New(cfg.Scylla, log, watch.BucketSize)
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
		return nil, fmt.Errorf("datastore does not implement the Namespace watch journal capability")
	}
	return capable.NamespaceWatchJournal(), nil
}
