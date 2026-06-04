// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package graph

import (
	"fmt"
	"testing"

	"github.com/gitstore-dev/gitstore/api/internal/graph/generated"
	"github.com/vektah/gqlparser/v2"
	"github.com/vektah/gqlparser/v2/ast"
	"github.com/vektah/gqlparser/v2/validator/rules"
)

func TestLookupOneOfSelectorsValidateExactlyOneKey(t *testing.T) {
	schema := generated.NewExecutableSchema(generated.Config{Resolvers: &Resolver{}}).Schema()

	// product uses positional args (namespace, name), not a oneof selector.
	t.Run("product/valid_args", func(t *testing.T) {
		assertQueryValidation(t, schema, `query { product(namespace: "my-store", name: "my-product") { id } }`, true)
	})
	t.Run("product/missing_namespace", func(t *testing.T) {
		assertQueryValidation(t, schema, `query { product(name: "my-product") { id } }`, false)
	})
	t.Run("product/missing_name", func(t *testing.T) {
		assertQueryValidation(t, schema, `query { product(namespace: "my-store") { id } }`, false)
	})

	// category, collection, namespace still use oneof selectors.
	tests := []struct {
		field     string
		natural   string
		selection string
	}{
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
	_, errs := gqlparser.LoadQueryWithRules(schema, query, rules.NewDefaultRules())
	if valid && len(errs) > 0 {
		t.Fatalf("expected query to validate, got %v", errs)
	}
	if !valid && len(errs) == 0 {
		t.Fatalf("expected query validation to fail")
	}
}
