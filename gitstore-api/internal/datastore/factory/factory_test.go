// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package factory_test

import (
	"testing"

	"github.com/gitstore-dev/gitstore/api/internal/config"
	"github.com/gitstore-dev/gitstore/api/internal/datastore/factory"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

func TestNewDatastore_MemdbReturnsDatastore(t *testing.T) {
	cfg := config.DatastoreConfig{Backend: "memdb"}
	ds, err := factory.NewDatastore(cfg, zap.NewNop())
	require.NoError(t, err)
	require.NotNil(t, ds)
	ds.Close() //nolint:errcheck
}

func TestNewDatastore_UnknownBackendReturnsError(t *testing.T) {
	cfg := config.DatastoreConfig{Backend: "postgres"}
	ds, err := factory.NewDatastore(cfg, zap.NewNop())
	require.Error(t, err)
	assert.Nil(t, ds)
	assert.Contains(t, err.Error(), "postgres")
	assert.Contains(t, err.Error(), "memdb")
	assert.Contains(t, err.Error(), "scylla")
}

func TestNewDatastore_EmptyBackendReturnsError(t *testing.T) {
	cfg := config.DatastoreConfig{Backend: ""}
	ds, err := factory.NewDatastore(cfg, zap.NewNop())
	require.Error(t, err)
	assert.Nil(t, ds)
}
