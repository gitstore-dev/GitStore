// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package scylla

import (
	"testing"
	"time"

	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	"github.com/gitstore-dev/gitstore/api/internal/datastore/scylla/migrations"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNamespaceTablesMatchQueryAccessPatterns(t *testing.T) {
	assert.Equal(t, []string{"id"}, NamespaceByID.Metadata().PartKey)
	assert.Empty(t, NamespaceByID.Metadata().SortKey)
	assert.Equal(t, []string{"name"}, NamespaceByName.Metadata().PartKey)
	assert.Equal(t, []string{"bucket"}, NamespaceByBucket.Metadata().PartKey)
	assert.Equal(t, []string{"creation_timestamp", "id"}, NamespaceByBucket.Metadata().SortKey)
	assert.Equal(t, []string{"creation_timestamp", "id"}, Repository.Metadata().SortKey)
}

func TestNamespaceBucketsForPage(t *testing.T) {
	now := time.Date(2026, time.April, 18, 0, 0, 0, 0, time.UTC)
	cursor := encodeKeysetCursor(
		time.Date(2026, time.February, 10, 0, 0, 0, 0, time.UTC),
		"00000000-0000-7000-8000-000000000001",
	)

	assert.Equal(t, []string{"2026-04", "2026-03", "2026-02", "2026-01"}, namespaceBucketsForPage(datastore.PageParams{First: 10}, now))
	assert.Equal(t, []string{"2026-01", "2026-02", "2026-03", "2026-04"}, namespaceBucketsForPage(datastore.PageParams{Last: 10}, now))
	assert.Equal(t, []string{"2026-02", "2026-01"}, namespaceBucketsForPage(datastore.PageParams{First: 10, After: cursor}, now))
	assert.Equal(t, []string{"2026-02", "2026-03", "2026-04"}, namespaceBucketsForPage(datastore.PageParams{Last: 10, Before: cursor}, now))
	assert.True(t, cursorInNamespaceBucket(cursor, "2026-02"))
	assert.False(t, cursorInNamespaceBucket(cursor, "2026-03"))
}

func TestInitialSchemaUsesQueryFirstNamespaceTables(t *testing.T) {
	content, err := migrations.Files.ReadFile("001_initial_schema.cql")
	require.NoError(t, err)
	schema := string(content)

	assert.Contains(t, schema, "CREATE TABLE IF NOT EXISTS namespaces_by_id")
	assert.Contains(t, schema, "CREATE TABLE IF NOT EXISTS namespaces_by_name")
	assert.Contains(t, schema, "CREATE TABLE IF NOT EXISTS namespaces_by_bucket")
	assert.Contains(t, schema, "creation_actor      text")
	assert.Contains(t, schema, "update_timestamp    timestamp")
	assert.Contains(t, schema, "update_actor        text")
	assert.Contains(t, schema, "PRIMARY KEY ((bucket), creation_timestamp, id)")
	assert.NotContains(t, schema, "CREATE TABLE IF NOT EXISTS namespaces (")
	assert.NotContains(t, schema, "ALTER TABLE")
	assert.NotContains(t, schema, "identifier")
	assert.NotContains(t, schema, "display_name")
}

func TestScyllaSchemaUsesTwoAlphaBaselineMigrations(t *testing.T) {
	entries, err := migrations.Files.ReadDir(".")
	require.NoError(t, err)

	var names []string
	for _, entry := range entries {
		if !entry.IsDir() {
			names = append(names, entry.Name())
		}
	}

	assert.ElementsMatch(t, []string{
		"001_initial_schema.cql",
		"002_secondary_indexes.cql",
	}, names)
}
