// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

//go:build memdb

// Wires the contract suite against the memdb backend.

package datastore_contract_test

import (
	"testing"

	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	"github.com/gitstore-dev/gitstore/api/internal/datastore/memdb"
)

func newMemdbDatastore(t *testing.T) datastore.Datastore {
	t.Helper()
	store, err := memdb.New()
	if err != nil {
		t.Fatalf("failed to create memdb datastore: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func TestContractMemdb(t *testing.T) {
	RunContractSuite(t, newMemdbDatastore(t))
}

func TestPaginationMemdb(t *testing.T) {
	RunPaginationSuite(t, newMemdbDatastore(t))
}
