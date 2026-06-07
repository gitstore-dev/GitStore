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

	// product uses @oneOf selector (by: ProductBy!) — same convention as category/collection/namespace.
	t.Run("product/valid_args", func(t *testing.T) {
		assertQueryValidation(t, schema, `query { product(by: {namespacePath: {namespace: "my-store", name: "my-product"}}) { id } }`, true)
	})
	t.Run("product/valid_id", func(t *testing.T) {
		assertQueryValidation(t, schema, `query { product(by: {id: "gid"}) { id } }`, true)
	})
	t.Run("product/missing_namespace", func(t *testing.T) {
		assertQueryValidation(t, schema, `query { product(name: "my-product") { id } }`, false)
	})
	t.Run("product/missing_name", func(t *testing.T) {
		assertQueryValidation(t, schema, `query { product(namespace: "my-store") { id } }`, false)
	})
	t.Run("product/no_selector_key", func(t *testing.T) {
		assertQueryValidation(t, schema, `query { product(by: {}) { id } }`, false)
	})
	t.Run("product/multiple_selector_keys", func(t *testing.T) {
		assertQueryValidation(t, schema, `query { product(by: {id: "gid", namespacePath: {namespace: "ns", name: "n"}}) { id } }`, false)
	})

	t.Run("category/valid_namespace_path", func(t *testing.T) {
		assertQueryValidation(t, schema, `query { category(by: {namespacePath: {namespace: "my-store", name: "electronics"}}) { id } }`, true)
	})
	t.Run("category/valid_id", func(t *testing.T) {
		assertQueryValidation(t, schema, `query { category(by: {id: "gid"}) { id } }`, true)
	})
	t.Run("category/multiple_selector_keys", func(t *testing.T) {
		assertQueryValidation(t, schema, `query { category(by: {id: "gid", namespacePath: {namespace: "ns", name: "electronics"}}) { id } }`, false)
	})
	t.Run("category/legacy_name_invalid", func(t *testing.T) {
		assertQueryValidation(t, schema, `query { category(by: {name: "electronics"}) { id } }`, false)
	})
	t.Run("category/legacy_slug_invalid", func(t *testing.T) {
		assertQueryValidation(t, schema, `query { category(by: {slug: "electronics"}) { id } }`, false)
	})

	// collection and namespace still use oneof selectors.
	tests := []struct {
		field     string
		natural   string
		selection string
	}{
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
