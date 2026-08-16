// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package resolver

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNamespaceConsumersAvoidDeprecatedOutputSelections(t *testing.T) {
	root := repositoryRoot(t)
	deprecatedSelection := regexp.MustCompile(`(?s)\bnamespace\s*(?:\([^{}]*\))?\s*\{[^{}]*\b(identifier|displayName|tier|createdAt|createdBy|updatedAt|updatedBy)\b`)
	var offenders []string

	for _, relativeRoot := range []string{
		"gitstore-admin/src",
		"gitstore-api/internal/graph/resolver",
		"tests/integration",
	} {
		err := filepath.WalkDir(filepath.Join(root, relativeRoot), func(path string, entry fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if entry.IsDir() {
				return nil
			}
			if !strings.HasSuffix(path, ".go") && !strings.HasSuffix(path, ".graphql") && !strings.HasSuffix(path, ".ts") && !strings.HasSuffix(path, ".tsx") {
				return nil
			}
			if strings.HasSuffix(path, "generated.ts") ||
				strings.HasSuffix(path, "namespace_contract_test.go") ||
				strings.HasSuffix(path, "namespace_consumer_migration_test.go") {
				return nil
			}
			content, readErr := os.ReadFile(path)
			if readErr != nil {
				return readErr
			}
			if deprecatedSelection.Match(content) {
				relative, relativeErr := filepath.Rel(root, path)
				if relativeErr != nil {
					return relativeErr
				}
				offenders = append(offenders, relative)
			}
			return nil
		})
		require.NoError(t, err)
	}

	assert.Empty(t, offenders, "Namespace operations must select metadata/spec/status instead of deprecated flat outputs")
}

func TestNamespaceConsumerMigrationKeepsDeprecationWindow(t *testing.T) {
	schema := namespaceContractSchema(t)
	for _, fieldName := range []string{"identifier", "displayName", "tier", "createdAt", "createdBy", "updatedAt", "updatedBy"} {
		field := schema.Types["Namespace"].Fields.ForName(fieldName)
		require.NotNil(t, field)
		deprecation := field.Directives.ForName("deprecated")
		require.NotNil(t, deprecation)
		assert.NotEmpty(t, deprecation.Arguments.ForName("reason").Value.Raw)
	}
}

func repositoryRoot(t *testing.T) string {
	t.Helper()
	_, currentFile, _, ok := runtime.Caller(0)
	require.True(t, ok)
	return filepath.Clean(filepath.Join(filepath.Dir(currentFile), "..", "..", "..", ".."))
}
