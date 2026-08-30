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
	assert.Contains(t, cql, "alter table namespaces_by_uid add watch_committed boolean")
	assert.Contains(t, cql, "'preimage': 'full'")
	assert.Contains(t, cql, "'postimage': 'true'")
	assert.Contains(t, cql, "'ttl': '1209600'")
	assert.Contains(t, cql, "create table if not exists namespace_watch_clock")
	assert.Contains(t, cql, "create table if not exists namespace_watch_events")
	assert.Contains(t, cql, "default_time_to_live = 604800")
	assert.Contains(t, cql, "lease_holder text static")
	assert.Contains(t, cql, "fencing_token bigint static")
	assert.Contains(t, cql, "bucket_size bigint static")
	assert.Contains(t, cql, "update_timestamp timestamp static")
	assert.Contains(t, cql, "bookmark_timestamp timestamp static")
	assert.Contains(t, cql, "cdc_progress_timestamp timestamp static")
	assert.Contains(t, cql, "lease_expiration_timestamp timestamp static")
	assert.Contains(t, cql, "progress_update_timestamp timestamp")
	assert.Contains(t, cql, "position blob")
	assert.Contains(t, cql, "labels map<text, text>")
	assert.Contains(t, cql, "previous_labels map<text, text>")
	assert.Contains(t, cql, "event_timestamp timestamp")
	assert.NotContains(t, cql, "updated_at")
	assert.NotContains(t, cql, "cdc_progress_at")
	assert.NotContains(t, cql, "expires_at")
	assert.NotContains(t, cql, "progress_updated_at")
	assert.NotContains(t, cql, "selector_labels text")
	assert.NotContains(t, cql, "event_at")
}
