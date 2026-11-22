// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package security

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/99designs/gqlgen/graphql"
	"github.com/gin-gonic/gin"
	"github.com/gitstore-dev/gitstore/api/internal/admin/graph/model"
	"github.com/gitstore-dev/gitstore/api/internal/auth"
	"github.com/gitstore-dev/gitstore/api/internal/auth/provider/rbaclocal"
	"github.com/gitstore-dev/gitstore/api/internal/config"
	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	"github.com/gitstore-dev/gitstore/api/internal/datastore/memdb"
	"github.com/gitstore-dev/gitstore/api/internal/gitclient"
	"github.com/gitstore-dev/gitstore/api/internal/testutil"
	"github.com/stretchr/testify/require"
	"github.com/vektah/gqlparser/v2/ast"
	"github.com/vektah/gqlparser/v2/gqlerror"
	"go.uber.org/zap"
)

func translationAuthorizationFixture(t *testing.T) (datastore.Datastore, *datastore.Namespace, *datastore.Translation) {
	t.Helper()
	const uid = "01960000-0000-7000-8000-000000000240"
	store, err := memdb.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	ns := &datastore.Namespace{UID: "01960000-0000-7000-8000-000000000241", Name: "acme", CreationActor: "namespace-owner"}
	require.NoError(t, store.CreateNamespace(t.Context(), ns))
	source := &datastore.Translation{
		APIVersion: "i18n.gitstore.dev/v1beta1", Kind: "Translation",
		UID: uid, Name: "laptop-de", Namespace: ns.Name, NamespaceID: ns.UID, CreationActor: "source-owner",
		Generation: 1, ResourceVersion: "1", CreationTimestamp: time.Now().UTC(),
		RawSource: []byte("accepted source"),
		Spec:      json.RawMessage(`{"locale":"de-DE","targetRef":{"apiVersion":"catalog.gitstore.dev/v1beta1","kind":"Product","name":"laptop"},"patches":[{"fieldPath":"spec.title","translation":"Titel"}]}`),
	}
	require.NoError(t, store.(datastore.TranslationCapable).TranslationStore().CreateTranslation(t.Context(), source))
	return store, ns, source
}

type translationAuthorizationFailedSource struct {
	datastore.TranslationStore
	failure error
}

func (s translationAuthorizationFailedSource) GetTranslation(context.Context, string) (*datastore.Translation, error) {
	return nil, s.failure
}

func (s translationAuthorizationFailedSource) GetTranslationByName(context.Context, string, string) (*datastore.Translation, error) {
	return nil, s.failure
}

type translationAuthorizationFailedStore struct {
	datastore.Datastore
	source datastore.TranslationStore
}

func (s translationAuthorizationFailedStore) TranslationStore() datastore.TranslationStore {
	return s.source
}

func TestTranslationAuthorizationSourceFailuresAreRetryableAndFailClosed(t *testing.T) {
	store, ns, source := translationAuthorizationFixture(t)
	id := base64.StdEncoding.EncodeToString([]byte("gid://GitStore/Translation/" + source.UID))
	for _, route := range []struct {
		name, field string
		args        map[string]any
	}{
		{"id", "translation", map[string]any{"by": model.TranslationBy{ID: &id}}},
		{"name", "translation", map[string]any{"by": model.TranslationBy{NamespacePath: &model.TranslationNamespacePath{Namespace: ns.Name, Name: source.Name}}}},
		{"node", "node", map[string]any{"id": id}},
		{"nodes", "nodes", map[string]any{"ids": []string{id}}},
	} {
		for _, failure := range []error{datastore.ErrConflict, errors.New("private storage failure")} {
			t.Run(route.name+"/"+failure.Error(), func(t *testing.T) {
				recorder := testutil.NewRecordingAuthZ(testutil.NewAllowAllAuthZ())
				broken := translationAuthorizationFailedStore{Datastore: store, source: translationAuthorizationFailedSource{
					TranslationStore: store.(datastore.TranslationCapable).TranslationStore(), failure: failure,
				}}
				middleware := NewAuthorizeWithStore(auth.NewProviderRegistry(nil, recorder, nil), broken, zap.NewNop())
				ctx := auth.ContextWithPrincipal(t.Context(), &auth.Principal{Subject: "reader", AuthMethod: "bearer"})
				ctx = graphql.WithFieldContext(ctx, &graphql.FieldContext{
					Object: "Query", Field: graphql.CollectedField{Field: &ast.Field{Name: route.field}}, Args: route.args,
				})
				called := false
				_, err := middleware.GraphQLFieldAuthorizer(ctx, func(context.Context) (any, error) {
					called = true
					return nil, nil
				})
				require.False(t, called)
				require.Empty(t, recorder.Calls, "unknown ownership must never become an empty-owner authorization")
				var response *gqlerror.Error
				require.ErrorAs(t, err, &response)
				require.Equal(t, "TRANSLATION_UNAVAILABLE", response.Extensions["code"])
				require.Equal(t, true, response.Extensions["retryable"])
				require.NotContains(t, response.Error(), "private")
			})
		}
	}
}

func TestTranslationRouteAuthorization(t *testing.T) {
	store, ns, source := translationAuthorizationFixture(t)
	uid := source.UID
	id := base64.StdEncoding.EncodeToString([]byte("gid://GitStore/Translation/" + uid))
	routes := []struct {
		object, field, action, owner string
		args                         map[string]any
	}{
		{"Query", "translation", auth.ActionTranslationRead, "source-owner", map[string]any{"by": model.TranslationBy{ID: &id}}},
		{"Query", "translation", auth.ActionTranslationRead, "source-owner", map[string]any{"by": model.TranslationBy{NamespacePath: &model.TranslationNamespacePath{Namespace: ns.Name, Name: source.Name}}}},
		{"Query", "translations", auth.ActionTranslationList, "namespace-owner", map[string]any{"namespace": ns.Name}},
		{"Query", "node", auth.ActionTranslationRead, "source-owner", map[string]any{"id": id}},
		{"Query", "nodes", auth.ActionTranslationRead, "source-owner", map[string]any{"ids": []string{id}}},
		{"Subscription", "watchTranslations", auth.ActionTranslationWatch, "", map[string]any{"namespace": ns.Name}},
		{"Subscription", "watchResources", auth.ActionTranslationWatch, "", map[string]any{"kind": "Translation", "namespace": ns.Name}},
		{"Mutation", "updateTranslationStatus", auth.ActionTranslationStatusWrite, "", map[string]any{"input": model.UpdateTranslationStatusInput{Name: source.Name, Namespace: ns.Name}}},
		{"Mutation", "updateResourceStatus", auth.ActionTranslationStatusWrite, "", map[string]any{"input": model.UpdateResourceStatusInput{Kind: "Translation", Name: source.Name, Namespace: ns.Name}}},
	}
	for _, route := range routes {
		for _, mode := range []string{"allow", "deny", "unavailable", "unavailable-snapshot"} {
			t.Run(route.object+"/"+route.field+"/"+mode, func(t *testing.T) {
				var provider auth.AuthZProvider = testutil.NewAllowAllAuthZ()
				if mode == "deny" {
					provider = testutil.NewDenyAllAuthZ(t)
				} else if mode == "unavailable" {
					provider = unavailableStatusAuthZ{}
				} else if mode == "unavailable-snapshot" {
					provider = unavailableSnapshotAuthZ{}
				}
				recorder := testutil.NewRecordingAuthZ(provider)
				middleware := NewAuthorizeWithStore(auth.NewProviderRegistry(nil, recorder, nil), store, zap.NewNop())
				ctx := auth.ContextWithPrincipal(t.Context(), &auth.Principal{Subject: "reader", AuthMethod: "bearer"})
				fc := &graphql.FieldContext{Object: route.object, Field: graphql.CollectedField{Field: &ast.Field{Name: route.field}}, Args: route.args}
				require.True(t, graphqlFieldRequiresAuthorization(fc))
				ctx = graphql.WithFieldContext(ctx, fc)
				called := false
				_, err := middleware.GraphQLFieldAuthorizer(ctx, func(next context.Context) (any, error) {
					called = true
					if route.field == "translation" {
						authorized, ok := AuthorizedTranslationRead(next)
						require.True(t, ok)
						require.Equal(t, uid, authorized.UID)
						_, shared := AuthorizedTranslationRead(graphql.WithFieldContext(next, &graphql.FieldContext{}))
						require.False(t, shared, "authorization must not leak to a sibling field")
					}
					if route.field == "node" || route.field == "nodes" {
						authorized, ok := AuthorizedTranslationNode(next, uid)
						require.True(t, ok)
						require.Equal(t, uid, authorized.UID)
						_, shared := AuthorizedTranslationNode(graphql.WithFieldContext(next, &graphql.FieldContext{}), uid)
						require.False(t, shared, "Node authorization must not leak to a sibling field")
					}
					return nil, nil
				})
				if mode == "allow" {
					require.NoError(t, err)
					require.True(t, called)
				} else {
					require.Error(t, err)
					require.False(t, called)
					require.NotContains(t, err.Error(), "private")
					if mode == "unavailable-snapshot" {
						var failure *gqlerror.Error
						require.ErrorAs(t, err, &failure)
						require.Equal(t, "AUTHORIZATION_UNAVAILABLE", failure.Extensions["code"])
						require.Equal(t, true, failure.Extensions["retryable"])
					}
				}

				resource := auth.ResourceContext{Kind: "translation", OwnerSub: route.owner, Attrs: map[string]any{"namespace": ns.Name}}
				if route.action == auth.ActionTranslationRead || route.action == auth.ActionTranslationStatusWrite {
					resource.Name = source.Name
				}
				assertAuthorizationCalls(t, recorder, testutil.AuthorizationCall{Subject: "reader", Action: route.action, Resource: resource})
			})
		}
	}
}

func TestTranslationAuthoringRequiresOwningRepositoryWriteGrant(t *testing.T) {
	gin.SetMode(gin.TestMode)
	store, ns, _ := translationAuthorizationFixture(t)
	repository := &datastore.Repository{
		UID: "01960000-0000-7000-8000-000000000250", Name: "catalog",
		Namespace: ns.Name, NamespaceID: ns.Name, CreationActor: "repository-owner",
	}
	require.NoError(t, store.CreateRepository(t.Context(), repository))
	provider := translationIsolationProvider(t)
	for _, caller := range []struct {
		subject string
		groups  []string
		allowed bool
	}{
		{"reader", nil, false},
		{"repository-owner", []string{"translation-authors"}, true},
		{"other-author", []string{"translation-authors"}, false},
	} {
		t.Run(caller.subject, func(t *testing.T) {
			recorder := testutil.NewRecordingAuthZ(provider)
			middleware := NewAuthorizeWithStore(auth.NewProviderRegistry(nil, recorder, nil), store, zap.NewNop())
			router := gin.New()
			called := false
			router.POST("/:namespace/:repo/git-receive-pack", func(c *gin.Context) {
				c.Set(repoIDKey, repository.UID)
				c.Request = c.Request.WithContext(auth.ContextWithPrincipal(c.Request.Context(),
					&auth.Principal{Subject: caller.subject, Groups: caller.groups, AuthMethod: "static-users"}))
				c.Next()
			}, middleware.GitHttpAuthorizer, middleware.PushContextInserter, func(c *gin.Context) {
				called = true
				captured := gitclient.PushContextFromContext(c.Request.Context())
				require.NotNil(t, captured)
				require.Equal(t, repository.UID, captured.RepositoryId)
				require.Equal(t, "repository.contents.write", captured.Authorization.Action)
				require.Equal(t, caller.subject, captured.Authorization.Actor.Subject)
				c.Status(http.StatusNoContent)
			})
			response := httptest.NewRecorder()
			router.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/acme/catalog/git-receive-pack", nil))
			require.Equal(t, caller.allowed, called)
			if caller.allowed {
				require.Equal(t, http.StatusNoContent, response.Code)
			} else {
				require.Equal(t, http.StatusForbidden, response.Code)
			}
			require.Len(t, recorder.Calls, 1)
			require.Equal(t, "repository.contents.write", recorder.Action)
			require.Equal(t, "repository-owner", recorder.Resource.OwnerSub)
			require.Equal(t, ns.Name, recorder.Resource.Attrs["namespace"])
		})
	}
}

func translationIsolationProvider(t *testing.T) auth.AuthZProvider {
	t.Helper()
	path := filepath.Join(t.TempDir(), "policy.yaml")
	require.NoError(t, os.WriteFile(path, []byte(`apiVersion: rbac.authorization.gitstore.dev/v1beta1
kind: RbacLocalConfiguration
clusterRoles:
  sourceReader:
    rules: [{resources: [translation], verbs: [read, update]}]
  statusWriter:
    rules: [{resources: [translation/status], verbs: [write]}]
  gitAuthor:
    rules:
      - resources: [translation]
        verbs: [create, update, delete]
      - resources: [repository/contents]
        verbs: [write]
        when: [owner]
clusterRoleBindings:
  - roleRef: {kind: ClusterRole, name: sourceReader}
    subjects: [{kind: User, name: reader}]
  - roleRef: {kind: ClusterRole, name: statusWriter}
    subjects: [{kind: ServiceAccount, name: translation-controller}]
  - roleRef: {kind: ClusterRole, name: gitAuthor}
    subjects: [{kind: Group, name: translation-authors}]
`), 0o600))
	provider, err := rbaclocal.New(config.RBACConfig{PolicyFile: path}, zap.NewNop())
	require.NoError(t, err)
	return provider
}

func TestTranslationReadAndControllerStatusGrantsRemainSeparate(t *testing.T) {
	store, ns, source := translationAuthorizationFixture(t)
	provider := translationIsolationProvider(t)
	reader := &auth.Principal{Subject: "reader", AuthMethod: "static-users"}
	controller := &auth.Principal{
		Subject: "system:serviceaccount:translation-controller", AuthMethod: "serviceaccount-jwt",
		ServiceAccountUID: "01960000-0000-7000-8000-000000000242",
	}
	for _, route := range []struct {
		name, object, field, action string
		principal                   *auth.Principal
		args                        map[string]any
		allowed                     bool
	}{
		{"reader-source", "Query", "translation", auth.ActionTranslationRead, reader,
			map[string]any{"by": model.TranslationBy{NamespacePath: &model.TranslationNamespacePath{Namespace: ns.Name, Name: source.Name}}}, true},
		{"reader-list", "Query", "translations", auth.ActionTranslationList, reader,
			map[string]any{"namespace": ns.Name}, false},
		{"reader-watch", "Subscription", "watchTranslations", auth.ActionTranslationWatch, reader,
			map[string]any{"namespace": ns.Name}, false},
		{"reader-status", "Mutation", "updateTranslationStatus", auth.ActionTranslationStatusWrite, reader,
			map[string]any{"input": model.UpdateTranslationStatusInput{Namespace: ns.Name, Name: source.Name}}, false},
		{"controller-status", "Mutation", "updateTranslationStatus", auth.ActionTranslationStatusWrite, controller,
			map[string]any{"input": model.UpdateTranslationStatusInput{Namespace: ns.Name, Name: source.Name}}, true},
		{"controller-source", "Query", "translation", auth.ActionTranslationRead, controller,
			map[string]any{"by": model.TranslationBy{NamespacePath: &model.TranslationNamespacePath{Namespace: ns.Name, Name: source.Name}}}, false},
	} {
		t.Run(route.name, func(t *testing.T) {
			recorder := testutil.NewRecordingAuthZ(provider)
			middleware := NewAuthorizeWithStore(auth.NewProviderRegistry(nil, recorder, nil), store, zap.NewNop())
			ctx := auth.ContextWithPrincipal(t.Context(), route.principal)
			ctx = graphql.WithFieldContext(ctx, &graphql.FieldContext{
				Object: route.object, Field: graphql.CollectedField{Field: &ast.Field{Name: route.field}}, Args: route.args,
			})
			called := false
			_, err := middleware.GraphQLFieldAuthorizer(ctx, func(context.Context) (any, error) {
				called = true
				return nil, nil
			})
			require.Equal(t, route.allowed, called)
			if route.allowed {
				require.NoError(t, err)
			} else {
				var failure *gqlerror.Error
				require.ErrorAs(t, err, &failure)
				require.Equal(t, "FORBIDDEN", failure.Extensions["code"])
			}
			require.Len(t, recorder.Calls, 1)
			require.Equal(t, route.action, recorder.Action)
		})
	}
}

func TestTranslationReadDoesNotGrantTargetNodeReads(t *testing.T) {
	store, ns, _ := translationAuthorizationFixture(t)
	provider := translationIsolationProvider(t)
	for index, target := range []struct{ kind, action string }{
		{"Product", auth.ActionProductRead},
		{"ProductVariant", auth.ActionProductVariantRead},
		{"CategoryTaxonomy", auth.ActionCategoryTaxonomyRead},
		{"Collection", auth.ActionCollectionRead},
		{"File", auth.ActionFileRead},
	} {
		kind := target.kind
		t.Run(kind, func(t *testing.T) {
			uid := fmt.Sprintf("01960000-0000-7000-8000-%012d", index+300)
			const name = "private-target"
			switch kind {
			case "Product":
				require.NoError(t, store.CreateProduct(t.Context(), &datastore.Product{UID: uid, Name: name, Namespace: ns.Name}))
			case "ProductVariant":
				require.NoError(t, store.CreateProductVariant(t.Context(), &datastore.ProductVariant{
					UID: uid, Name: name, Namespace: ns.Name, ProductRefName: name, SKU: "fixture-target",
					Spec: json.RawMessage(`{"sku":"fixture-target","productRef":{"name":"private-target"}}`),
				}))
			case "CategoryTaxonomy":
				require.NoError(t, store.CreateCategoryTaxonomy(t.Context(), &datastore.CategoryTaxonomy{UID: uid, Name: name, Namespace: ns.Name}))
			case "Collection":
				require.NoError(t, store.CreateCollection(t.Context(), &datastore.Collection{UID: uid, Name: name, Namespace: ns.Name}))
			case "File":
				require.NoError(t, store.CreateFile(t.Context(), &datastore.File{UID: uid, Name: name, Namespace: ns.Name}))
			}
			nodeKind := kind
			if kind == "CategoryTaxonomy" {
				nodeKind = "Category"
			}
			id := base64.StdEncoding.EncodeToString([]byte("gid://GitStore/" + nodeKind + "/" + uid))
			for _, field := range []string{"node", "nodes"} {
				t.Run(field, func(t *testing.T) {
					recorder := testutil.NewRecordingAuthZ(provider)
					middleware := NewAuthorizeWithStore(auth.NewProviderRegistry(nil, recorder, nil), store, zap.NewNop())
					ctx := auth.ContextWithPrincipal(t.Context(), &auth.Principal{Subject: "reader", AuthMethod: "static-users"})
					ctx = graphql.WithFieldContext(ctx, &graphql.FieldContext{
						Object: "Query", Field: graphql.CollectedField{Field: &ast.Field{Name: field}},
						Args: map[string]any{"id": id, "ids": []string{id}},
					})
					called := false
					_, err := middleware.GraphQLFieldAuthorizer(ctx, func(context.Context) (any, error) {
						called = true
						return nil, nil
					})
					require.Error(t, err)
					require.False(t, called, "source-read authority cannot expose a target")
					require.NotEmpty(t, recorder.Calls, "the target kind must make its own provider decision")
					require.Equal(t, target.action, recorder.Calls[0].Action)
					for _, call := range recorder.Calls {
						require.NotEqual(t, auth.ActionTranslationRead, call.Action)
					}
				})
			}
		})
	}
}

func TestTranslationDeniedReadsHideExistence(t *testing.T) {
	store, ns, source := translationAuthorizationFixture(t)
	for _, field := range []string{"translation", "node", "nodes"} {
		t.Run(field, func(t *testing.T) {
			var previous *gqlerror.Error
			for _, present := range []bool{false, true} {
				uid, name := "01960000-0000-7000-8000-000000000299", "missing"
				if present {
					uid, name = source.UID, source.Name
				}
				id := base64.StdEncoding.EncodeToString([]byte("gid://GitStore/Translation/" + uid))
				middleware := NewAuthorizeWithStore(auth.NewProviderRegistry(nil, testutil.NewDenyAllAuthZ(t), nil), store, zap.NewNop())
				ctx := auth.ContextWithPrincipal(t.Context(), &auth.Principal{Subject: "outsider", AuthMethod: "static-users"})
				ctx = graphql.WithFieldContext(ctx, &graphql.FieldContext{
					Object: "Query", Field: graphql.CollectedField{Field: &ast.Field{Name: field}},
					Args: map[string]any{"id": id, "ids": []string{id},
						"by": model.TranslationBy{NamespacePath: &model.TranslationNamespacePath{Namespace: ns.Name, Name: name}}},
				})
				called := false
				_, err := middleware.GraphQLFieldAuthorizer(ctx, func(context.Context) (any, error) {
					called = true
					return nil, nil
				})
				require.False(t, called)
				var failure *gqlerror.Error
				require.ErrorAs(t, err, &failure)
				require.Equal(t, "FORBIDDEN", failure.Extensions["code"])
				if previous != nil {
					require.Equal(t, previous.Message, failure.Message)
					require.Equal(t, previous.Extensions, failure.Extensions)
				}
				previous = failure
			}
		})
	}
}
