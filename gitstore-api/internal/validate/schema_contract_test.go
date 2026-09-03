// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package validate_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestResourceSchemasRejectEmptyRequiredStrings(t *testing.T) {
	t.Parallel()

	_, currentFile, _, ok := runtime.Caller(0)
	require.True(t, ok)
	schemaFiles, err := filepath.Glob(filepath.Join(filepath.Dir(currentFile), "..", "..", "..", "schemas", "gitstore", "v1beta1", "*.schema.json"))
	require.NoError(t, err)
	require.NotEmpty(t, schemaFiles)

	for _, schemaFile := range schemaFiles {
		schemaFile := schemaFile
		t.Run(filepath.Base(schemaFile), func(t *testing.T) {
			raw, err := os.ReadFile(schemaFile)
			require.NoError(t, err)

			var schema map[string]any
			require.NoError(t, json.Unmarshal(raw, &schema))
			assertRequiredStringsAreConstrained(t, schema, "$")
		})
	}
}

func assertRequiredStringsAreConstrained(t *testing.T, node map[string]any, path string) {
	t.Helper()

	properties, _ := node["properties"].(map[string]any)
	for _, required := range stringSlice(node["required"]) {
		property, _ := properties[required].(map[string]any)
		if property["type"] != "string" {
			continue
		}
		_, hasPattern := property["pattern"]
		_, hasEnum := property["enum"]
		minLength, _ := property["minLength"].(float64)
		require.Truef(t, minLength >= 1 || hasPattern || hasEnum,
			"%s.%s is a required string but permits an empty value", path, required)
	}

	for name, child := range properties {
		if childNode, ok := child.(map[string]any); ok {
			assertRequiredStringsAreConstrained(t, childNode, path+".properties."+name)
		}
	}
	for name, child := range objectMap(node["$defs"]) {
		assertRequiredStringsAreConstrained(t, child, path+".$defs."+name)
	}
}

func stringSlice(value any) []string {
	values, _ := value.([]any)
	strings := make([]string, 0, len(values))
	for _, value := range values {
		if stringValue, ok := value.(string); ok {
			strings = append(strings, stringValue)
		}
	}
	return strings
}

func objectMap(value any) map[string]map[string]any {
	values, _ := value.(map[string]any)
	objects := make(map[string]map[string]any, len(values))
	for key, value := range values {
		if object, ok := value.(map[string]any); ok {
			objects[key] = object
		}
	}
	return objects
}
