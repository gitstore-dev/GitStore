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

	assert.False(t, strings.HasPrefix(strings.TrimSpace(cql), "--"),
		"gocqlx/migrate skips a semicolon-delimited statement when it starts with a comment")
	assert.Contains(t, cql, "alter table repositories_by_uid with cdc")
	assert.Contains(t, cql, "'preimage': 'full'")
	assert.Contains(t, cql, "'postimage': 'true'")
	assert.Contains(t, cql, "'ttl': '1209600'")
	assert.NotContains(t, cql, "repository_watch_events", "Repository must reuse the generic journal")
	assert.NotContains(t, cql, "repository_watch_clock", "Repository must reuse the generic journal")
	for _, line := range strings.Split(cql, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "--") {
			assert.NotContains(t, line, ";", "gocqlx/migrate treats semicolons in line comments as statement delimiters")
		}
	}
}

func TestProductWatchMigrationEnablesAuthoritativeFullImageCDC(t *testing.T) {
	raw, err := migrations.Files.ReadFile("013_product_watch_cdc.cql")
	require.NoError(t, err)
	cql := strings.ToLower(string(raw))

	assert.False(t, strings.HasPrefix(strings.TrimSpace(cql), "--"))
	assert.Contains(t, cql, "alter table products_by_namespace with cdc")
	assert.Contains(t, cql, "'preimage': 'full'")
	assert.Contains(t, cql, "'postimage': 'true'")
	assert.Contains(t, cql, "'ttl': '1209600'")
	assert.NotContains(t, cql, "product_watch_events", "Product must reuse the generic journal")
}

func TestResourceWatchJournalMigrationAddsIdentityColumnsAtomically(t *testing.T) {
	raw, err := migrations.Files.ReadFile("011_resource_watch_journal.cql")
	require.NoError(t, err)
	cql := strings.ToLower(string(raw))

	assert.False(t, strings.HasPrefix(strings.TrimSpace(cql), "--"),
		"gocqlx/migrate skips a semicolon-delimited statement when it starts with a comment")
	assert.Equal(t, 1, strings.Count(cql, "alter table namespace_watch_events"),
		"Scylla schema propagation can lose back-to-back ALTER TABLE statements")
	assert.Contains(t, cql, "kind text")
	assert.Contains(t, cql, "namespace text")
	for _, line := range strings.Split(cql, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "--") {
			assert.NotContains(t, line, ";", "gocqlx/migrate treats semicolons in line comments as statement delimiters")
		}
	}
}
