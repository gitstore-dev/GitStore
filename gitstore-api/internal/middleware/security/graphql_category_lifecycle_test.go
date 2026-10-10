// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package security

import (
	"context"
	"encoding/base64"
	"testing"

	"github.com/99designs/gqlgen/graphql"
	"github.com/gitstore-dev/gitstore/api/internal/auth"
	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	"github.com/gitstore-dev/gitstore/api/internal/graph/model"
	"github.com/gitstore-dev/gitstore/api/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/vektah/gqlparser/v2/ast"
	"github.com/vektah/gqlparser/v2/gqlerror"
	"go.uber.org/zap"
)

func storedCategory() *datastore.CategoryTaxonomy {
	return &datastore.CategoryTaxonomy{
		UID: "cat-uid", Name: "laptops", Namespace: "acme", RepositoryID: "repo-stored",
		CreationActor: "author", Annotations: map[string]string{datastore.OwnerAnnotationKey: "owner-sub"},
	}
}

func categoryAuthzStore() *testutil.StubStore {
	return &testutil.StubStore{
		GetCategoryTaxonomyFunc: func(context.Context, string) (*datastore.CategoryTaxonomy, error) {
			return storedCategory(), nil
		},
		GetCategoryTaxonomyByNameFunc: func(_ context.Context, namespace, name string) (*datastore.CategoryTaxonomy, error) {
			if namespace != "acme" || name != "laptops" {
				return nil, datastore.ErrNotFound
			}
			return storedCategory(), nil
		},
	}
}

func authorizeCategoryField(t *testing.T, principal *auth.Principal, field string, args map[string]any) (*testutil.RecordingAuthZ, bool, error) {
	t.Helper()
	authz := testutil.NewDenyAllAuthZ(t)
	mw := NewAuthorizeWithStore(auth.NewProviderRegistry(nil, authz, nil), categoryAuthzStore(), zap.NewNop())
	ctx := context.Background()
	if principal != nil {
		ctx = auth.ContextWithPrincipal(ctx, principal)
	}
	ctx = graphql.WithFieldContext(ctx, &graphql.FieldContext{Object: "Mutation", Field: graphql.CollectedField{Field: &ast.Field{Name: field}}, Args: args})
	called := false
	_, err := mw.GraphQLFieldAuthorizer(ctx, func(context.Context) (any, error) { called = true; return nil, nil })
	return authz, called, err
}

func TestCategoryMutationAuthorizationMatrix(t *testing.T) {
	categoryID := base64.StdEncoding.EncodeToString([]byte("gid://GitStore/Category/cat-uid"))
	// Caller-supplied fields deliberately differ from the stored record on
	// update: authorization must scope by what is stored.
	createMeta := &model.ObjectMetaInput{Namespace: "acme", Name: "laptops"}
	updateMeta := &model.ObjectMetaInput{Namespace: "acme", Name: "laptops", Annotations: map[string]any{datastore.OwnerAnnotationKey: "caller-claimed"}}
	for _, tc := range []struct {
		field, action, owner, repositoryID string
		args                               map[string]any
	}{
		{field: "createCategory", action: "categoryTaxonomy.create", args: map[string]any{"input": model.CreateCategoryInput{Metadata: createMeta}}},
		{field: "updateCategory", action: "categoryTaxonomy.update", owner: "owner-sub", repositoryID: "repo-stored", args: map[string]any{"input": model.UpdateCategoryInput{Metadata: updateMeta}}},
		{field: "deleteCategory", action: "categoryTaxonomy.delete", owner: "owner-sub", repositoryID: "repo-stored", args: map[string]any{"input": model.DeleteCategoryInput{ID: categoryID}}},
		{field: "completeCategoryDeletion", action: "categoryTaxonomy.purge", args: map[string]any{"input": model.CompleteCategoryDeletionInput{ID: "Z2lkOi8vR2l0U3RvcmUvQ2F0ZWdvcnkvY2F0ZWdvcnktMQ==", Namespace: "acme", Name: "laptops", ResourceVersion: "3"}}},
	} {
		t.Run(tc.field, func(t *testing.T) {
			authz, called, err := authorizeCategoryField(t, &auth.Principal{Subject: "denied", AuthMethod: "bearer"}, tc.field, tc.args)
			require.Error(t, err)
			assert.False(t, called, "authorization must run before any side effect")
			assert.Equal(t, tc.action, authz.Action)
			assert.Equal(t, "categoryTaxonomy", authz.Resource.Kind)
			assert.Equal(t, "laptops", authz.Resource.Name)
			assert.Equal(t, "acme", authz.Resource.Attrs["namespace"])
			if tc.owner != "" {
				assert.Equal(t, tc.owner, authz.Resource.OwnerSub, "scope comes from the stored record")
				assert.Equal(t, tc.repositoryID, authz.Resource.Attrs["repositoryID"])
			}
			var gqlErr *gqlerror.Error
			require.ErrorAs(t, err, &gqlErr)
			assert.Equal(t, "FORBIDDEN", gqlErr.Extensions["code"])
			assert.NotContains(t, gqlErr.Extensions, "diagnostics")
		})
	}
}

func TestCategoryMutationsRequireAuthentication(t *testing.T) {
	for _, field := range []string{"createCategory", "updateCategory", "deleteCategory", "completeCategoryDeletion"} {
		t.Run(field, func(t *testing.T) {
			fc := &graphql.FieldContext{Object: "Mutation", Field: graphql.CollectedField{Field: &ast.Field{Name: field}}}
			assert.True(t, graphqlFieldRequiresAuthorization(fc))
			_, called, err := authorizeCategoryField(t, nil, field, map[string]any{})
			require.Error(t, err)
			assert.False(t, called)
		})
	}
}

func authorizeCategoryQuery(t *testing.T, field string, args map[string]any) (*testutil.RecordingAuthZ, bool, error) {
	t.Helper()
	authz := testutil.NewDenyAllAuthZ(t)
	mw := NewAuthorizeWithStore(auth.NewProviderRegistry(nil, authz, nil), categoryAuthzStore(), zap.NewNop())
	ctx := auth.ContextWithPrincipal(context.Background(), &auth.Principal{Subject: "denied", AuthMethod: "bearer"})
	ctx = graphql.WithFieldContext(ctx, &graphql.FieldContext{Object: "Query", Field: graphql.CollectedField{Field: &ast.Field{Name: field}}, Args: args})
	called := false
	_, err := mw.GraphQLFieldAuthorizer(ctx, func(context.Context) (any, error) { called = true; return nil, nil })
	return authz, called, err
}

func TestCategoryListAuthorizationCoversFilteredAndUnfilteredLists(t *testing.T) {
	for name, args := range map[string]map[string]any{
		"unfiltered": {"namespace": "acme"},
		"filtered":   {"namespace": "acme", "filter": model.CategoryFilterInput{DescendantOf: "computers"}},
	} {
		t.Run(name, func(t *testing.T) {
			authz, called, err := authorizeCategoryQuery(t, "categories", args)
			require.Error(t, err)
			assert.False(t, called, "the list resolver must not reveal which categories exist")
			assert.Equal(t, "categoryTaxonomy.list", authz.Action)
			assert.Equal(t, "categoryTaxonomy", authz.Resource.Kind)
			assert.Equal(t, "acme", authz.Resource.Attrs["namespace"])
		})
	}
}

func TestCategoryReadAuthorization(t *testing.T) {
	categoryID := base64.StdEncoding.EncodeToString([]byte("gid://GitStore/Category/cat-uid"))
	for name, by := range map[string]model.CategoryBy{
		"by name": {NamespacePath: &model.CategoryNamespacePath{Namespace: "acme", Name: "laptops"}},
		"by id":   {ID: &categoryID},
	} {
		t.Run(name, func(t *testing.T) {
			authz, called, err := authorizeCategoryQuery(t, "category", map[string]any{"by": by})
			require.Error(t, err)
			assert.False(t, called)
			assert.Equal(t, "categoryTaxonomy.read", authz.Action)
			assert.Equal(t, "laptops", authz.Resource.Name)
			assert.Equal(t, "acme", authz.Resource.Attrs["namespace"])
		})
	}
}

func TestCategoryNodeLookupIsAuthorizedAsRead(t *testing.T) {
	categoryID := base64.StdEncoding.EncodeToString([]byte("gid://GitStore/Category/cat-uid"))
	authz, called, err := authorizeCategoryQuery(t, "node", map[string]any{"id": categoryID})
	require.Error(t, err)
	assert.False(t, called)
	assert.Equal(t, "categoryTaxonomy.read", authz.Action)
	assert.Equal(t, "acme", authz.Resource.Attrs["namespace"])
}
