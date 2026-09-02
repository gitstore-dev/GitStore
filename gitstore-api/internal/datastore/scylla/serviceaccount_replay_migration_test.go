// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package scylla_test

import (
	"io/fs"
	"strings"
	"testing"

	"github.com/gitstore-dev/gitstore/api/internal/datastore/scylla/migrations"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestServiceAccountAssertionReplayMigrationIsAdditiveRollbackArtifact(t *testing.T) {
	const migration = "009_service_account_assertion_replay.cql"
	entries, err := fs.ReadDir(migrations.Files, ".")
	require.NoError(t, err)

	var names []string
	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), ".cql") {
			names = append(names, entry.Name())
		}
	}
	require.Contains(t, names, migration)
	require.Contains(t, names, "008_service_account.cql")

	raw, err := migrations.Files.ReadFile(migration)
	require.NoError(t, err)
	cql := strings.ToUpper(string(raw))
	assert.Contains(t, cql, "CREATE TABLE IF NOT EXISTS SERVICE_ACCOUNT_ASSERTION_REPLAYS")
	assert.NotContains(t, cql, "DROP TABLE")
	assert.NotContains(t, cql, "DROP COLUMN")
	assert.NotContains(t, cql, "TRUNCATE")
}
