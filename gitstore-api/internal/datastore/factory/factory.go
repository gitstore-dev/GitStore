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
func NewDatastore(cfg config.DatastoreConfig, log *zap.Logger) (datastore.Datastore, error) {
	switch cfg.Backend {
	case "memdb":
		return memdb.New()
	case "scylla":
		return scylla.New(cfg.Scylla, log)
	default:
		return nil, fmt.Errorf("invalid datastore backend %q; valid values: memdb, scylla", cfg.Backend)
	}
}
