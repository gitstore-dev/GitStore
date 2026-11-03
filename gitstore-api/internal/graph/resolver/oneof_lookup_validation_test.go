// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package resolver

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/99designs/gqlgen/graphql/handler"
	"github.com/gitstore-dev/gitstore/api/internal/auth"
	"github.com/gitstore-dev/gitstore/api/internal/graph/generated"
	"github.com/gitstore-dev/gitstore/api/internal/graph/model"
	"github.com/gitstore-dev/gitstore/api/internal/middleware/security"
	"github.com/gitstore-dev/gitstore/api/internal/testutil"
	"github.com/stretchr/testify/require"
	"github.com/vektah/gqlparser/v2"
	"github.com/vektah/gqlparser/v2/ast"
	"github.com/vektah/gqlparser/v2/gqlerror"
	"github.com/vektah/gqlparser/v2/validator/rules"
	"go.uber.org/zap"
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
		{field: "collection", natural: `namespacePath: {namespace: "my-store", name: "collection-1"}`, selection: "id"},
		{field: "file", natural: `namespacePath: {namespace: "my-store", name: "hero"}`, selection: "id"},
		{field: "namespace", natural: `name: "namespace-1"`, selection: "id"},
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
	assertQueryValidation(t, schema, `query { file(namespace: "my-store", name: "hero") { id } }`, false)
	assertQueryValidation(t, schema, `query { file(by: {namespacePath: {namespace: "my-store"}}) { id } }`, false)
	assertQueryValidation(t, schema, `query { file(by: {namespacePath: {name: "hero"}}) { id } }`, false)
}

func TestFileLookupRejectsInvalidVariableSelectorsBeforeLookup(t *testing.T) {
	id := mustEncodeNodeID(nodeKindFile, globalIDTestFileUID)
	path := map[string]string{"namespace": "other-store", "name": "other-file"}
	for name, by := range map[string]map[string]any{
		"empty":            {},
		"both":             {"id": id, "namespacePath": path},
		"null id":          {"id": nil},
		"null path":        {"namespacePath": nil},
		"id and null path": {"id": id, "namespacePath": nil},
		"null id and path": {"id": nil, "namespacePath": path},
	} {
		t.Run(name, func(t *testing.T) {
			store, root := newGlobalIDTestResolver(t)
			seedGlobalIDTestData(t, t.Context(), store)
			counted := &globalIDCountedFileStore{Datastore: store}
			root.store = counted
			provider := testutil.NewAllowAllAuthZ()
			middleware := security.NewAuthorizeWithStore(auth.NewProviderRegistry(nil, provider, nil), counted, zap.NewNop())
			server := handler.NewDefaultServer(generated.NewExecutableSchema(generated.Config{Resolvers: root}))
			server.AroundFields(middleware.GraphQLFieldAuthorizer)
			payload, err := json.Marshal(map[string]any{
				"query":     `query($by: FileBy!) { detail: file(by: $by) { id } }`,
				"variables": map[string]any{"by": by},
			})
			require.NoError(t, err)
			request := httptest.NewRequest(http.MethodPost, "/graphql", bytes.NewReader(payload))
			request.Header.Set("Content-Type", "application/json")
			response := httptest.NewRecorder()
			server.ServeHTTP(response, request)
			var result struct {
				Data   map[string]any
				Errors gqlerror.List
			}
			require.NoError(t, json.Unmarshal(response.Body.Bytes(), &result))
			require.Nil(t, result.Data["detail"])
			require.Len(t, result.Errors, 1)
			require.Equal(t, "BAD_USER_INPUT", result.Errors[0].Extensions["code"])
			require.Zero(t, counted.reads.Load(), "invalid selectors must not perform either lookup")
			require.Empty(t, provider.Calls)
		})
	}
}

func TestFileResolverRejectsAmbiguousAndEmptySelectorsBeforeLookup(t *testing.T) {
	store, root := newGlobalIDTestResolver(t)
	seedGlobalIDTestData(t, t.Context(), store)
	counted := &globalIDCountedFileStore{Datastore: store}
	root.store = counted
	id := mustEncodeNodeID(nodeKindFile, globalIDTestFileUID)
	for _, by := range []model.FileBy{
		{},
		{ID: &id, NamespacePath: &model.FileNamespacePath{Namespace: "other-store", Name: "other-file"}},
	} {
		file, err := root.Query().File(t.Context(), by)
		require.Nil(t, file)
		var structured *gqlerror.Error
		require.ErrorAs(t, err, &structured)
		require.Equal(t, "BAD_USER_INPUT", structured.Extensions["code"])
	}
	require.Zero(t, counted.reads.Load())
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
