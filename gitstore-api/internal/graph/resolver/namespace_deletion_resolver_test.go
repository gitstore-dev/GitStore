// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package resolver

import (
	"context"
	"testing"
	"time"

	"github.com/99designs/gqlgen/graphql"
	"github.com/gitstore-dev/gitstore/api/internal/auth"
	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	"github.com/gitstore-dev/gitstore/api/internal/datastore/memdb"
	"github.com/gitstore-dev/gitstore/api/internal/graph/model"
	"github.com/gitstore-dev/gitstore/api/internal/middleware/security"
	namespaceadmission "github.com/gitstore-dev/gitstore/api/internal/namespace"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/vektah/gqlparser/v2/ast"
	"github.com/vektah/gqlparser/v2/gqlerror"
	"go.uber.org/zap"
)

type deletionAuthZ struct{}

func (deletionAuthZ) Name() string { return "deletion-test" }
func (deletionAuthZ) Authorize(context.Context, *auth.Principal, string, auth.ResourceContext) (auth.Decision, error) {
	return auth.Allow("deletion-test", "allowed"), nil
}

func TestDeleteNamespaceResolverOutcomes(t *testing.T) {
	mutation, store := newNamespaceDeletionResolver(t)
	ctx := context.Background()
	ns := seedDeletionNamespace(t, store, "resolver-delete")

	payload, err := invokeDeleteNamespaceResolver(t, mutation, store, ctx, ns.Name)
	require.NoError(t, err)
	assert.Equal(t, model.NamespaceDeletionOutcomeTerminationStarted, payload.Outcome)

	payload, err = invokeDeleteNamespaceResolver(t, mutation, store, ctx, ns.Name)
	require.NoError(t, err)
	assert.Equal(t, model.NamespaceDeletionOutcomeAlreadyTerminating, payload.Outcome)
}

func TestDeleteNamespaceResolverReturnsDeterministicBlockers(t *testing.T) {
	mutation, store := newNamespaceDeletionResolver(t)
	ctx := context.Background()
	ns := seedDeletionNamespace(t, store, "gitstore-system")
	require.NoError(t, store.CreateRepository(ctx, &datastore.Repository{
		UID:               uuid.NewString(),
		RepositoryID:      uuid.NewString(),
		Namespace:         ns.Name,
		Name:              "system",
		CreationTimestamp: time.Now().UTC(),
	}))

	payload, err := invokeDeleteNamespaceResolver(t, mutation, store, ctx, ns.Name)
	assert.Nil(t, payload)
	var graphErr *gqlerror.Error
	require.ErrorAs(t, err, &graphErr)
	assert.Equal(t, namespaceadmission.CodeDeletionBlocked, graphErr.Extensions["code"])
	assert.Equal(t, []string{"BOOTSTRAP_NAMESPACE", "NAMESPACE_NOT_EMPTY"}, graphErr.Extensions["reasons"])
}

func newNamespaceDeletionResolver(t *testing.T) (*mutationResolver, datastore.Datastore) {
	t.Helper()
	store, err := memdb.New()
	require.NoError(t, err)
	root, err := NewResolver(ResolverDeps{Store: store, Logger: zap.NewNop()})
	require.NoError(t, err)
	return &mutationResolver{Resolver: root}, store
}

func seedDeletionNamespace(t *testing.T, store datastore.Datastore, name string) *datastore.Namespace {
	t.Helper()
	now := time.Now().UTC()
	ns := &datastore.Namespace{
		ID:                uuid.NewString(),
		UID:               uuid.NewString(),
		Name:              name,
		ResourceVersion:   "1",
		Generation:        1,
		CreationTimestamp: now,
		CreationActor:     "alice",
		UpdateTimestamp:   now,
		UpdateActor:       "alice",
	}
	ns.UID = ns.ID
	require.NoError(t, store.CreateNamespace(context.Background(), ns))
	return ns
}

func invokeDeleteNamespaceResolver(
	t *testing.T,
	mutation *mutationResolver,
	store datastore.Datastore,
	ctx context.Context,
	name string,
) (*model.DeleteNamespacePayload, error) {
	t.Helper()
	registry := auth.NewProviderRegistry(nil, deletionAuthZ{}, nil)
	authorizer := security.NewAuthorizeWithStore(registry, store, zap.NewNop())
	ctx = auth.ContextWithPrincipal(ctx, &auth.Principal{Subject: "alice", AuthMethod: "test"})
	ctx = graphql.WithFieldContext(ctx, &graphql.FieldContext{
		Object: "Mutation",
		Field:  graphql.CollectedField{Field: &ast.Field{Name: "deleteNamespace"}},
		Args: map[string]any{
			"input": model.DeleteNamespaceInput{Identifier: name},
		},
	})
	var payload *model.DeleteNamespacePayload
	_, err := authorizer.GraphQLFieldAuthorizer(ctx, func(nextCtx context.Context) (any, error) {
		var resolverErr error
		payload, resolverErr = mutation.DeleteNamespace(nextCtx, model.DeleteNamespaceInput{Identifier: name})
		return payload, resolverErr
	})
	return payload, err
}
