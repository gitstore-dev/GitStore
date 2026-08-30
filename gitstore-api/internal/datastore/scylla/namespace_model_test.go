// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package scylla

import (
	"strings"
	"testing"
	"time"

	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	"github.com/gitstore-dev/gitstore/api/internal/datastore/scylla/migrations"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNamespaceTablesMatchQueryAccessPatterns(t *testing.T) {
	assert.Equal(t, []string{"uid"}, NamespaceByUID.Metadata().PartKey)
	assert.Empty(t, NamespaceByUID.Metadata().SortKey)
	assert.Equal(t, []string{"name"}, NamespaceByName.Metadata().PartKey)
	assert.Equal(t, []string{"bucket"}, NamespaceByBucket.Metadata().PartKey)
	assert.Equal(t, []string{"creation_timestamp", "uid"}, NamespaceByBucket.Metadata().SortKey)
	assert.Equal(t, []string{"uid"}, RepositoryByUID.Metadata().PartKey)
	assert.Equal(t, []string{"namespace", "bucket"}, RepositoryByNamespace.Metadata().PartKey)
	assert.Equal(t, []string{"creation_timestamp", "uid"}, RepositoryByNamespace.Metadata().SortKey)
	assert.Equal(t, []string{"bucket"}, RepositoryByBucket.Metadata().PartKey)
	assert.Equal(t, []string{"creation_timestamp", "uid"}, RepositoryByBucket.Metadata().SortKey)
}

func TestAuthoritativeTablesUseCanonicalResourceEnvelope(t *testing.T) {
	required := []string{
		"api_version",
		"kind",
		"uid",
		"name",
		"generation",
		"resource_version",
		"revision",
		"creation_timestamp",
		"creation_actor",
		"update_timestamp",
		"update_actor",
		"labels",
		"annotations",
		"owner_references",
		"finalizers",
		"deletion_timestamp",
		"source_path",
		"git_commit_sha",
		"git_ref",
		"spec",
		"body",
		"status",
	}

	for name, metadata := range map[string][]string{
		"Namespace":        NamespaceByUID.Metadata().Columns,
		"Repository":       RepositoryByUID.Metadata().Columns,
		"Product":          ProductByNamespace.Metadata().Columns,
		"ProductVariant":   ProductVariantByNamespace.Metadata().Columns,
		"Collection":       Collection.Metadata().Columns,
		"CategoryTaxonomy": CategoryTaxonomy.Metadata().Columns,
		"File":             FileByNamespace.Metadata().Columns,
	} {
		t.Run(name, func(t *testing.T) {
			assert.Subset(t, metadata, required)
			if name != "Namespace" {
				assert.Contains(t, metadata, "namespace")
				assert.Contains(t, metadata, "repository_id")
			} else {
				assert.NotContains(t, metadata, "namespace")
				assert.NotContains(t, metadata, "repository_id")
				assert.Contains(t, metadata, "repository_creation_epoch")
				assert.Contains(t, metadata, "pending_repository_creations")
			}
		})
	}
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

	assert.Contains(t, schema, "CREATE TABLE IF NOT EXISTS namespaces_by_uid")
	assert.Contains(t, schema, "CREATE TABLE IF NOT EXISTS namespaces_by_name")
	assert.Contains(t, schema, "CREATE TABLE IF NOT EXISTS namespaces_by_bucket")
	assert.Contains(t, schema, "api_version          text")
	assert.Contains(t, schema, "owner_references     text")
	assert.Contains(t, schema, "body                 text")
	assert.Contains(t, schema, "creation_actor       text")
	assert.Contains(t, schema, "update_timestamp     timestamp")
	assert.Contains(t, schema, "update_actor         text")
	assert.Contains(t, schema, "PRIMARY KEY ((bucket), creation_timestamp, uid)")
	assert.NotContains(t, schema, "CREATE TABLE IF NOT EXISTS namespaces (")
	assert.NotContains(t, schema, "namespaces_by_id")
	assert.NotContains(t, schema, "ALTER TABLE")
	assert.NotContains(t, schema, "identifier")
	assert.NotContains(t, schema, "display_name")
	assert.NotContains(t, schema, "'all'")
	assert.Equal(t, strings.Count(schema, "CREATE TABLE IF NOT EXISTS"), strings.Count(schema, "gc_grace_seconds = 864000"))
	assert.NotContains(t, schema, "TimeWindowCompactionStrategy")
}

func TestScyllaSchemaIncludesOwnerReferenceProjectionMigration(t *testing.T) {
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
		"003_owner_reference_dependents.cql",
		"004_file_resource.cql",
		"005_namespace_repository_fence.cql",
	}, names)
}

func TestNamespaceRepositoryFenceMigration(t *testing.T) {
	content, err := migrations.Files.ReadFile("005_namespace_repository_fence.cql")
	require.NoError(t, err)
	assert.Contains(t, string(content), "ALTER TABLE namespaces_by_uid ADD repository_creation_epoch bigint")
	assert.Contains(t, string(content), "ALTER TABLE namespaces_by_uid ADD pending_repository_creations bigint")
	assert.Contains(t, NamespaceByUID.Metadata().Columns, "repository_creation_epoch")
	assert.Contains(t, NamespaceByUID.Metadata().Columns, "pending_repository_creations")
}

func TestFileMigrationDefinesAuthoritativeAndLookupTables(t *testing.T) {
	content, err := migrations.Files.ReadFile("004_file_resource.cql")
	require.NoError(t, err)
	schema := string(content)
	for _, tableName := range []string{"files_by_namespace", "files_by_name", "files_by_uid"} {
		assert.Contains(t, schema, "CREATE TABLE IF NOT EXISTS "+tableName)
	}
	for _, column := range []string{"api_version", "kind", "namespace", "uid", "name", "resource_version", "repository_id", "spec", "body", "status"} {
		assert.Contains(t, schema, column)
	}
	assert.Contains(t, schema, "PRIMARY KEY ((namespace), creation_timestamp, uid)")
}

func TestSecondaryIndexMigrationIsIntentionallyEmpty(t *testing.T) {
	content, err := migrations.Files.ReadFile("002_secondary_indexes.cql")
	require.NoError(t, err)
	schema := string(content)

	assert.NotContains(t, schema, "CREATE INDEX")
	assert.NotContains(t, schema, "CREATE CUSTOM INDEX")
	assert.Contains(t, schema, "Query-first tables")
}
