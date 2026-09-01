// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package scylla_test

import (
	"io/fs"
	"strings"
	"testing"

	"github.com/gitstore-dev/gitstore/api/internal/datastore/scylla/migrations"
	"github.com/stretchr/testify/require"
)

func TestNamespaceWatchMigrationIsAdditiveRollbackArtifact(t *testing.T) {
	entries, err := fs.ReadDir(migrations.Files, ".")
	require.NoError(t, err)
	var names []string
	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), ".cql") {
			names = append(names, entry.Name())
		}
	}
	require.Equal(t, "006_namespace_watch_cdc.cql", names[len(names)-1])

	raw, err := migrations.Files.ReadFile("006_namespace_watch_cdc.cql")
	require.NoError(t, err)
	cql := strings.ToUpper(string(raw))
	require.NotContains(t, cql, "DROP TABLE")
	require.NotContains(t, cql, "DROP COLUMN")
	require.NotContains(t, cql, "TRUNCATE")
	require.Contains(t, cql, "CREATE TABLE IF NOT EXISTS")
	require.Contains(t, cql, "ALTER TABLE NAMESPACES_BY_UID WITH CDC")

	// Application rollback is supported by leaving migration 006 installed:
	// pre-050 binaries ignore these additive tables and CDC metadata while the
	// fleet-wide watch ingress deny prevents them from serving weaker streams.
	for _, prior := range names[:len(names)-1] {
		_, err := migrations.Files.ReadFile(prior)
		require.NoErrorf(t, err, "prior migration %s must remain embedded", prior)
	}
}
