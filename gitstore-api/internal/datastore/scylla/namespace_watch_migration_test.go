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

func TestNamespaceWatchMigrationContract(t *testing.T) {
	raw, err := migrations.Files.ReadFile("006_namespace_watch_cdc.cql")
	require.NoError(t, err)
	cql := strings.ToLower(string(raw))

	assert.Contains(t, cql, "alter table namespaces_by_uid with cdc")
	assert.Contains(t, cql, "'preimage': 'full'")
	assert.Contains(t, cql, "'postimage': 'true'")
	assert.Contains(t, cql, "'ttl': '1209600'")
	assert.Contains(t, cql, "create table if not exists namespace_watch_clock")
	assert.Contains(t, cql, "create table if not exists namespace_watch_events")
	assert.Contains(t, cql, "default_time_to_live = 604800")
	assert.Contains(t, cql, "holder text static")
	assert.Contains(t, cql, "fencing_token bigint static")
	assert.Contains(t, cql, "cdc_progress_at timestamp static")
	assert.Contains(t, cql, "position blob")
	assert.Contains(t, cql, "selector_labels text")
}
