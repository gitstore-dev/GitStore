// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package scylla_test

import (
	"strings"
	"testing"

	"github.com/gitstore-dev/gitstore/api/internal/datastore/scylla/migrations"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRepositoryWatchMigrationEnablesAuthoritativeFullImageCDC(t *testing.T) {
	raw, err := migrations.Files.ReadFile("012_repository_watch_cdc.cql")
	require.NoError(t, err)
	cql := strings.ToLower(string(raw))

	assert.Contains(t, cql, "alter table repositories_by_uid with cdc")
	assert.Contains(t, cql, "'preimage': 'full'")
	assert.Contains(t, cql, "'postimage': 'true'")
	assert.Contains(t, cql, "'ttl': '1209600'")
	assert.NotContains(t, cql, "repository_watch_events", "Repository must reuse the generic journal")
	assert.NotContains(t, cql, "repository_watch_clock", "Repository must reuse the generic journal")
}
