// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package resolver

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/vektah/gqlparser/v2"
	"github.com/vektah/gqlparser/v2/validator/rules"
)

func TestTranslationNativeQueriesMatchGeneratedSchema(t *testing.T) {
	raw, err := os.ReadFile("../../../../../tests/integration/testdata/translation-queries.json")
	require.NoError(t, err)
	var queries map[string]string
	require.NoError(t, json.Unmarshal(raw, &queries))
	require.NotEmpty(t, queries)
	schema := productContractSchema(t)
	for name, query := range queries {
		t.Run(name, func(t *testing.T) {
			_, failures := gqlparser.LoadQueryWithRules(schema, query, rules.NewDefaultRules())
			require.Empty(t, failures)
		})
	}
}
