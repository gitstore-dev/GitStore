// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package resolver

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"testing"

	"github.com/99designs/gqlgen/graphql/handler"
	"github.com/gitstore-dev/gitstore/api/internal/admission"
	"github.com/gitstore-dev/gitstore/api/internal/auth"
	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	"github.com/gitstore-dev/gitstore/api/internal/datastore/memdb"
	"github.com/gitstore-dev/gitstore/api/internal/graph/generated"
	"github.com/gitstore-dev/gitstore/api/internal/graph/model"
	"github.com/gitstore-dev/gitstore/api/internal/middleware/security"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

// Nil dependencies deliberately panic if a namespace lookup, authorization
// provider, commit, token issuer, or persistence is reached for invalid input.
func TestMetadataScopeGraphQLRejectsBeforeEffects(t *testing.T) {
	store, err := memdb.New()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.Close()) })
	newReplica := func() *handler.Server {
		server := handler.NewDefaultServer(generated.NewExecutableSchema(generated.Config{Resolvers: &Resolver{store: store}}))
		middleware := security.NewAuthorizeWithStore(nil, store, zap.NewNop())
		server.AroundFields(middleware.GraphQLFieldAuthorizer)
		return server
	}
	// Two independent API handlers share a datastore. The third handler is a
	// fresh replacement, with no process-local validation state carried over.
	servers := []*handler.Server{newReplica(), newReplica(), newReplica()}
	for _, operation := range []struct {
		name, extra string
		cluster     bool
	}{
		{"createProduct", `spec:{title:"test"}`, false},
		{"updateProduct", `spec:{title:"test"}`, false},
		{"createCategory", `spec:{title:"test"}`, false},
		{"updateCategory", `spec:{title:"test"}`, false},
		{"createRepository", `spec:{}`, false},
		{"updateRepository", `spec:{}`, false},
		{"createNamespace", `spec:{tier:USER}`, true},
		{"updateNamespace", `spec:{tier:USER}`, true},
		{"createServiceAccount", `publicKeys:[]`, true},
		{"rotateServiceAccountKey", `add:[],removeKids:[]`, true},
		{"deleteServiceAccount", ``, true},
		{"issueServiceAccountToken", `spec:{}`, true},
	} {
		values := []string{"", ",namespace:null", `,namespace:""`, `,namespace:"  \t"`}
		if operation.cluster {
			values = []string{`,namespace:""`, `,namespace:"  "`, `,namespace:"acme"`}
		}
		for _, value := range values {
			for replica, server := range servers {
				t.Run(fmt.Sprintf("replica-%d/%s%s", replica, operation.name, value), func(t *testing.T) {
					t.Parallel()
					query := fmt.Sprintf(`mutation { aliased:%s(input:{metadata:{name:"test"%s},%s}){__typename}}`, operation.name, value, operation.extra)
					body, err := json.Marshal(map[string]string{"query": query})
					require.NoError(t, err)
					request := httptest.NewRequest("POST", "/graphql", bytes.NewReader(body))
					request.Header.Set("Content-Type", "application/json")
					response := httptest.NewRecorder()
					server.ServeHTTP(response, request)
					var result struct {
						Errors []struct {
							Path       []any
							Extensions struct {
								Code        string
								Diagnostics []struct{ Reason, Field, Level, Message string }
							}
						}
					}
					require.NoError(t, json.Unmarshal(response.Body.Bytes(), &result))
					require.Len(t, result.Errors, 1, response.Body.String())
					failure := result.Errors[0]
					require.Equal(t, []any{"aliased"}, failure.Path)
					require.Equal(t, "BAD_USER_INPUT", failure.Extensions.Code)
					require.Len(t, failure.Extensions.Diagnostics, 1)
					diagnostic := failure.Extensions.Diagnostics[0]
					reason := "NAMESPACE_REQUIRED"
					if operation.cluster {
						reason = "NAMESPACE_FORBIDDEN"
					}
					require.Equal(t, reason, diagnostic.Reason)
					require.Equal(t, "metadata.namespace", diagnostic.Field)
					require.Equal(t, "FAILURE", diagnostic.Level)
				})
			}
		}
	}
	t.Cleanup(func() {
		namespaces, err := store.ListNamespaces(context.Background(), datastore.PageParams{First: 10})
		require.NoError(t, err)
		require.Empty(t, namespaces.Items)
	})
}

func TestMetadataScopeServiceEntryPointsRejectBeforeEffects(t *testing.T) {
	ctx := auth.ContextWithPrincipal(t.Context(), &auth.Principal{Subject: "admin"})
	service, resolver := &Service{}, &Resolver{}
	for _, value := range []*string{nil, new(""), new(" \t")} {
		metadata := &model.ObjectMetaInput{Name: "test", Namespace: value}
		for _, create := range []bool{true, false} {
			_, err := service.CommitProductManifest(ctx, "", "", metadata, nil, nil, "admin", create)
			require.IsType(t, &admission.Error{}, err)
			require.Equal(t, admission.ReasonNamespaceRequired, err.(*admission.Error).Reason())
			_, err = service.CommitRepositoryManifest(ctx, "", "", metadata, nil, "admin", create)
			require.IsType(t, &admission.Error{}, err)
			require.Equal(t, admission.ReasonNamespaceRequired, err.(*admission.Error).Reason())
			_, _, err = service.commitCategoryManifest(ctx, CategoryManifestInput{Metadata: metadata}, "admin", create)
			require.IsType(t, &admission.Error{}, err)
			require.Equal(t, admission.ReasonNamespaceRequired, err.(*admission.Error).Reason())
		}
	}
	for _, value := range []string{"", " \t", "acme"} {
		metadata := &model.ObjectMetaInput{Name: "test", Namespace: &value}
		_, err := service.CreateNamespace(ctx, model.CreateNamespaceInput{Metadata: metadata}, "admin")
		require.IsType(t, &admission.Error{}, err)
		require.Equal(t, admission.ReasonNamespaceForbidden, err.(*admission.Error).Reason())
		_, err = service.UpdateNamespace(ctx, model.UpdateNamespaceInput{Metadata: metadata}, "admin")
		require.IsType(t, &admission.Error{}, err)
		require.Equal(t, admission.ReasonNamespaceForbidden, err.(*admission.Error).Reason())
		_, err = resolver.CreateServiceAccount(ctx, &model.CreateServiceAccountInput{Metadata: metadata, PublicKeys: []*model.ServiceAccountPublicKeyInput{{}}})
		require.ErrorContains(t, err, "cluster-scoped")
		_, err = resolver.RotateServiceAccountKey(ctx, &model.RotateServiceAccountKeyInput{Metadata: metadata})
		require.ErrorContains(t, err, "cluster-scoped")
		_, err = resolver.DeleteServiceAccount(ctx, &model.DeleteServiceAccountInput{Metadata: metadata})
		require.ErrorContains(t, err, "cluster-scoped")
		_, err = resolver.IssueServiceAccountToken(ctx, &model.IssueServiceAccountTokenInput{Metadata: metadata, Spec: &model.TokenRequestSpecInput{}})
		require.ErrorContains(t, err, "cluster-scoped")
	}
}
