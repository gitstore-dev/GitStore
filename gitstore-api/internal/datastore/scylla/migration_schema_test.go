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

const fullImageCDC = "cdc = {'enabled': 'true', 'preimage': 'full', 'postimage': 'true', 'ttl': '1209600'}"

// createStatement returns the CREATE TABLE statement for table, up to its
// terminating semicolon.
func createStatement(t *testing.T, file, table string) string {
	t.Helper()
	raw, err := migrations.Files.ReadFile(file)
	require.NoError(t, err)
	cql := strings.ToLower(string(raw))
	start := strings.Index(cql, "create table if not exists "+table+" (")
	require.GreaterOrEqualf(t, start, 0, "%s must create %s", file, table)
	end := strings.Index(cql[start:], ";")
	require.Greater(t, end, 0)
	return cql[start : start+end]
}

func TestWatchedKindsEnableAuthoritativeFullImageCDC(t *testing.T) {
	for _, tc := range []struct{ file, table string }{
		{"002_namespace.cql", "namespaces_by_uid"},
		{"003_repository.cql", "repositories_by_uid"},
		{"004_category_taxonomy.cql", "category_taxonomies_by_namespace"},
		{"005_product.cql", "products_by_namespace"},
		{"008_file.cql", "files_by_namespace"},
	} {
		assert.Containsf(t, createStatement(t, tc.file, tc.table), fullImageCDC, "%s CDC", tc.table)
	}
}

func TestOnlyWatchedAuthoritativeTablesEnableCDC(t *testing.T) {
	entries, err := fs.ReadDir(migrations.Files, ".")
	require.NoError(t, err)
	count := 0
	for _, entry := range entries {
		raw, err := migrations.Files.ReadFile(entry.Name())
		require.NoError(t, err)
		count += strings.Count(strings.ToLower(string(raw)), "cdc = {")
	}
	assert.Equal(t, 5, count, "every CDC-enabled table must be a registered watch source")
}

func TestResourceWatchJournalIsSharedAcrossKinds(t *testing.T) {
	events := createStatement(t, "001_infra.cql", "resource_watch_events")
	for _, column := range []string{"kind text", "namespace text", "labels map<text, text>", "previous_labels map<text, text>", "event_timestamp timestamp"} {
		assert.Contains(t, events, column)
	}
	assert.Contains(t, events, "default_time_to_live = 604800")

	clock := createStatement(t, "001_infra.cql", "resource_watch_clock")
	for _, column := range []string{
		"lease_holder text static", "fencing_token bigint static", "bucket_size bigint static",
		"update_timestamp timestamp static", "bookmark_timestamp timestamp static",
		"cdc_progress_timestamp timestamp static", "lease_expiration_timestamp timestamp static",
		"progress_update_timestamp timestamp", "position blob",
	} {
		assert.Contains(t, clock, column)
	}

	entries, err := fs.ReadDir(migrations.Files, ".")
	require.NoError(t, err)
	for _, entry := range entries {
		raw, err := migrations.Files.ReadFile(entry.Name())
		require.NoError(t, err)
		cql := strings.ToLower(string(raw))
		assert.NotRegexpf(t, `\b[a-z_]+_watch_(events|clock)\b`, strings.ReplaceAll(cql, "resource_watch_", ""),
			"%s: every kind must reuse the shared resource journal", entry.Name())
	}
}

// gocqlx/migrate splits each file on ';' and silently skips any statement
// that starts with "--". A comment directly above a statement must therefore
// end with ';' to become its own skipped statement, and comments must not
// carry ';' mid-line.
func TestMigrationStatementsSurviveGocqlxSplitting(t *testing.T) {
	entries, err := fs.ReadDir(migrations.Files, ".")
	require.NoError(t, err)
	for _, entry := range entries {
		raw, err := migrations.Files.ReadFile(entry.Name())
		require.NoError(t, err)
		cql := strings.ToUpper(string(raw))
		assert.NotContainsf(t, cql, "DROP ", "%s", entry.Name())
		assert.NotContainsf(t, cql, "TRUNCATE", "%s", entry.Name())
		for _, statement := range strings.Split(string(raw), ";") {
			statement = strings.TrimSpace(statement)
			if statement == "" {
				continue
			}
			for _, line := range strings.Split(statement, "\n") {
				if strings.HasPrefix(statement, "--") {
					assert.Truef(t, strings.HasPrefix(strings.TrimSpace(line), "--") || strings.TrimSpace(line) == "",
						"%s: statement after comment would be skipped: %q", entry.Name(), statement)
				} else {
					assert.NotContainsf(t, line, "--", "%s: inline comment inside statement", entry.Name())
				}
			}
		}
	}
}

func TestCategoryAncestorIndexIsKeyedByNamespaceAndAncestor(t *testing.T) {
	stmt := strings.Join(strings.Fields(createStatement(t, "010_category_ancestor_index.cql", "category_ancestor_index")), " ")
	assert.Contains(t, stmt, "primary key ((namespace, ancestor), depth, descendant)")
	assert.Contains(t, stmt, "clustering order by (depth asc, descendant asc)")
	for _, column := range []string{"depth tinyint", "descendant_uid uuid", "resource_version text"} {
		assert.Contains(t, stmt, column)
	}
	assert.NotContains(t, stmt, "cdc", "the ancestor index is a derived projection, not a watch source")
}
