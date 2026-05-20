// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package graph

import (
	"fmt"
	"testing"

	"github.com/gitstore-dev/gitstore/api/internal/graph/generated"
	"github.com/vektah/gqlparser/v2"
	"github.com/vektah/gqlparser/v2/ast"
)

func TestLookupOneOfSelectorsValidateExactlyOneKey(t *testing.T) {
	schema := generated.NewExecutableSchema(generated.Config{Resolvers: &Resolver{}}).Schema()

	tests := []struct {
		field     string
		natural   string
		selection string
	}{
		{field: "product", natural: `sku: "SKU-1"`, selection: "id"},
		{field: "category", natural: `slug: "category-1"`, selection: "id"},
		{field: "collection", natural: `slug: "collection-1"`, selection: "id"},
		{field: "namespace", natural: `identifier: "namespace-1"`, selection: "id"},
	}

	for _, tt := range tests {
		t.Run(tt.field+"/natural_key", func(t *testing.T) {
			assertQueryValidation(t, schema, fmt.Sprintf(`query { %s(by: {%s}) { %s } }`, tt.field, tt.natural, tt.selection), true)
		})

		t.Run(tt.field+"/id", func(t *testing.T) {
			assertQueryValidation(t, schema, fmt.Sprintf(`query { %s(by: {id: "gid"}) { %s } }`, tt.field, tt.selection), true)
		})

		t.Run(tt.field+"/no_selector_key", func(t *testing.T) {
			assertQueryValidation(t, schema, fmt.Sprintf(`query { %s(by: {}) { %s } }`, tt.field, tt.selection), false)
		})

		t.Run(tt.field+"/multiple_selector_keys", func(t *testing.T) {
			assertQueryValidation(t, schema, fmt.Sprintf(`query { %s(by: {id: "gid", %s}) { %s } }`, tt.field, tt.natural, tt.selection), false)
		})

		t.Run(tt.field+"/null_selector_key", func(t *testing.T) {
			assertQueryValidation(t, schema, fmt.Sprintf(`query { %s(by: {id: null}) { %s } }`, tt.field, tt.selection), false)
		})
	}
}

func assertQueryValidation(t *testing.T, schema *ast.Schema, query string, valid bool) {
	t.Helper()
	_, errs := gqlparser.LoadQuery(schema, query)
	if valid && len(errs) > 0 {
		t.Fatalf("expected query to validate, got %v", errs)
	}
	if !valid && len(errs) == 0 {
		t.Fatalf("expected query validation to fail")
	}
}
