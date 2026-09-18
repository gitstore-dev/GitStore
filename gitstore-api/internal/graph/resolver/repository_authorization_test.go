// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package resolver_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/99designs/gqlgen/graphql"
	"github.com/gitstore-dev/gitstore/api/internal/auth"
	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	"github.com/gitstore-dev/gitstore/api/internal/datastore/memdb"
	"github.com/gitstore-dev/gitstore/api/internal/graph/model"
	"github.com/gitstore-dev/gitstore/api/internal/graph/resolver"
	"github.com/gitstore-dev/gitstore/api/internal/middleware/security"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/vektah/gqlparser/v2/ast"
	"go.uber.org/zap"
)

const (
	repositoryAuthzSourceNamespaceID = "01960000-0000-7000-8000-000000000130"
	repositoryAuthzTargetNamespaceID = "01960000-0000-7000-8000-000000000131"
	repositoryAuthzRepositoryID      = "01960000-0000-7000-8000-000000000132"
)

type repositoryAuthzCall struct {
	action   string
	resource auth.ResourceContext
}

type tenantOwnershipAuthz struct {
	mu    sync.Mutex
	calls []repositoryAuthzCall
}

func (a *tenantOwnershipAuthz) Name() string { return "tenant-ownership-test" }

func (a *tenantOwnershipAuthz) Authorize(_ context.Context, principal *auth.Principal, action string, resource auth.ResourceContext) (auth.Decision, error) {
	a.mu.Lock()
	a.calls = append(a.calls, repositoryAuthzCall{action: action, resource: resource})
	a.mu.Unlock()

	if principal.Subject != resource.OwnerSub {
		return auth.Deny(a.Name(), "cross-tenant access denied"), nil
	}
	return auth.Allow(a.Name(), "tenant owner"), nil
}

func (a *tenantOwnershipAuthz) reset() {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.calls = nil
}

func (a *tenantOwnershipAuthz) callsSnapshot() []repositoryAuthzCall {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]repositoryAuthzCall(nil), a.calls...)
}

type repositoryAuthzHarness struct {
	store            datastore.Datastore
	registry         *auth.ProviderRegistry
	authz            *tenantOwnershipAuthz
	sourceNamespace  *datastore.Namespace
	targetNamespace  *datastore.Namespace
	repository       *datastore.Repository
	repositoryNodeID string
}

func newRepositoryAuthzHarness(t *testing.T) *repositoryAuthzHarness {
	return newRepositoryAuthzHarnessWithTargetOwner(t, "alice")
}

func newRepositoryAuthzHarnessWithTargetOwner(t *testing.T, targetOwner string) *repositoryAuthzHarness {
	t.Helper()
	ctx := context.Background()
	store, err := memdb.New()
	require.NoError(t, err)

	source := &datastore.Namespace{
		UID:           repositoryAuthzSourceNamespaceID,
		Name:          "alice-source",
		Tier:          datastore.NamespaceTierUser,
		CreationActor: "alice",
		UpdateActor:   "alice",
	}
	target := &datastore.Namespace{
		UID:           repositoryAuthzTargetNamespaceID,
		Name:          "alice-target",
		Tier:          datastore.NamespaceTierUser,
		CreationActor: targetOwner,
		UpdateActor:   targetOwner,
	}
	require.NoError(t, store.CreateNamespace(ctx, source))
	require.NoError(t, store.CreateNamespace(ctx, target))

	repo := &datastore.Repository{
		UID:               repositoryAuthzRepositoryID,
		RepositoryID:      repositoryAuthzRepositoryID,
		Namespace:         source.Name,
		NamespaceID:       source.Name,
		Name:              "catalog",
		CreationActor:     "alice",
		UpdateActor:       "alice",
		CreationTimestamp: time.Date(2026, time.August, 19, 0, 0, 0, 0, time.UTC),
	}
	require.NoError(t, store.CreateRepository(ctx, repo))
	require.NoError(t, store.CreateNamespaceMapping(ctx, &datastore.NamespaceMapping{
		Namespace:    source.Name,
		Name:         repo.Name,
		RepositoryID: repo.UID,
	}))

	authz := &tenantOwnershipAuthz{}
	registry := auth.NewProviderRegistry(nil, authz, nil)
	repositoryNodeID, err := resolver.EncodeNodeID("Repository", repo.UID)
	require.NoError(t, err)

	return &repositoryAuthzHarness{
		store:            store,
		registry:         registry,
		authz:            authz,
		sourceNamespace:  source,
		targetNamespace:  target,
		repository:       repo,
		repositoryNodeID: repositoryNodeID,
	}
}

func (h *repositoryAuthzHarness) authorizeField(ctx context.Context, object, field string, args map[string]any) error {
	middleware := security.NewAuthorizeWithStore(h.registry, h.store, zap.NewNop())
	ctx = graphql.WithFieldContext(ctx, &graphql.FieldContext{
		Object: object,
		Field:  graphql.CollectedField{Field: &ast.Field{Name: field}},
		Args:   args,
	})
	_, err := middleware.GraphQLFieldAuthorizer(ctx, func(context.Context) (any, error) { return nil, nil })
	return err
}

func TestRepositoryResolversDenyCrossTenantAccessBeforeMutationOrRead(t *testing.T) {
	h := newRepositoryAuthzHarness(t)
	ctx := auth.ContextWithPrincipal(context.Background(), &auth.Principal{
		Subject:    "bob",
		AuthMethod: "test",
	})
	first := int32(10)

	tests := []struct {
		name   string
		action string
		object string
		field  string
		args   map[string]any
	}{
		{
			name:   "create",
			action: "repository.create.any",
			object: "Mutation", field: "createRepository", args: map[string]any{"input": model.CreateRepositoryInput{
				APIVersion: "gitstore.dev/v1beta1",
				Kind:       "Repository",
				Metadata:   &model.MetadataInput{Name: "new-catalog", Namespace: h.sourceNamespace.Name},
				Spec:       &model.RepositorySpecInput{},
			}},
		},
		{
			name:   "update",
			action: "repository.update.any",
			object: "Mutation", field: "updateRepository", args: map[string]any{"input": model.UpdateRepositoryInput{
				APIVersion: "gitstore.dev/v1beta1",
				Kind:       "Repository",
				Metadata:   &model.MetadataInput{Name: h.repository.Name, Namespace: h.sourceNamespace.Name},
				Spec:       &model.RepositorySpecInput{},
			}},
		},
		{
			name:   "delete",
			action: "repository.delete.any",
			object: "Mutation", field: "deleteRepository", args: map[string]any{"input": model.DeleteRepositoryInput{
				ID: &h.repositoryNodeID,
			}},
		},
		{
			name:   "read by ID",
			action: "repository.read.any",
			object: "Query", field: "repository", args: map[string]any{"by": model.RepositoryBy{ID: &h.repositoryNodeID}},
		},
		{
			name:   "read by namespace path",
			action: "repository.read.any",
			object: "Query", field: "repository", args: map[string]any{"by": model.RepositoryBy{
				NamespacePath: &model.RepositoryNamespacePath{
					Namespace: h.sourceNamespace.Name,
					Name:      h.repository.Name,
				},
			}},
		},
		{
			name:   "read through node",
			action: "repository.read.any",
			object: "Query", field: "node", args: map[string]any{"id": h.repositoryNodeID},
		},
		{
			name:   "read through batched nodes",
			action: "repository.read.any",
			object: "Query", field: "nodes", args: map[string]any{"ids": []string{h.repositoryNodeID}},
		},
		{
			name:   "list namespace repositories",
			action: "repository.read.any",
			object: "Query", field: "repositories", args: map[string]any{"namespace": h.sourceNamespace.Name, "first": &first},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			h.authz.reset()

			err := h.authorizeField(ctx, test.object, test.field, test.args)

			require.EqualError(t, err, "input: permission denied: cross-tenant access denied")
			calls := h.authz.callsSnapshot()
			require.Len(t, calls, 1)
			assert.Equal(t, test.action, calls[0].action)
			assert.Equal(t, "repository", calls[0].resource.Kind)
			assert.Equal(t, "alice", calls[0].resource.OwnerSub)
			assert.Equal(t, h.sourceNamespace.Name, calls[0].resource.Attrs["namespace"])
		})
	}
}

func TestRepositoryStatusMutationUsesControllerWriteAction(t *testing.T) {
	h := newRepositoryAuthzHarness(t)
	ctx := auth.ContextWithPrincipal(context.Background(), &auth.Principal{
		Subject:    "bob",
		AuthMethod: "test",
	})

	for _, tc := range []struct {
		name  string
		field string
		input any
	}{
		{name: "status", field: "updateRepositoryStatus", input: model.UpdateRepositoryStatusInput{Namespace: h.sourceNamespace.Name, Name: h.repository.Name}},
		{name: "provision storage", field: "provisionRepositoryStorage", input: model.ProvisionRepositoryStorageInput{Namespace: h.sourceNamespace.Name, Name: h.repository.Name}},
		{name: "complete deletion", field: "completeRepositoryDeletion", input: model.CompleteRepositoryDeletionInput{Namespace: h.sourceNamespace.Name, Name: h.repository.Name, ResourceVersion: "1"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h.authz.reset()
			err := h.authorizeField(ctx, "Mutation", tc.field, map[string]any{"input": tc.input})
			require.EqualError(t, err, "input: permission denied: cross-tenant access denied")
			calls := h.authz.callsSnapshot()
			require.Len(t, calls, 1)
			assert.Equal(t, "repository.status.write", calls[0].action)
			assert.Equal(t, "repository", calls[0].resource.Kind)
			assert.Equal(t, h.repository.Name, calls[0].resource.Name)
			assert.Equal(t, h.sourceNamespace.Name, calls[0].resource.Attrs["namespace"])
		})
	}
}

func TestRepositoryNamespacePathAuthorizationDoesNotLeakSameNamedRepositoryAcrossNamespaces(t *testing.T) {
	h := newRepositoryAuthzHarnessWithTargetOwner(t, "bob")
	ctx := auth.ContextWithPrincipal(context.Background(), &auth.Principal{Subject: "alice", AuthMethod: "test"})
	targetRepository := &datastore.Repository{
		UID: "01960000-0000-7000-8000-000000000133", RepositoryID: "01960000-0000-7000-8000-000000000133",
		Namespace: h.targetNamespace.Name, NamespaceID: h.targetNamespace.Name, Name: h.repository.Name,
		CreationActor: "bob", UpdateActor: "bob", CreationTimestamp: time.Now().UTC(),
	}
	require.NoError(t, h.store.CreateRepository(context.Background(), targetRepository))
	require.NoError(t, h.store.CreateNamespaceMapping(context.Background(), &datastore.NamespaceMapping{
		Namespace: h.targetNamespace.Name, Name: targetRepository.Name, RepositoryID: targetRepository.UID,
	}))

	err := h.authorizeField(ctx, "Query", "repository", map[string]any{"by": model.RepositoryBy{
		NamespacePath: &model.RepositoryNamespacePath{Namespace: h.targetNamespace.Name, Name: h.repository.Name},
	}})
	require.EqualError(t, err, "input: permission denied: cross-tenant access denied")
	calls := h.authz.callsSnapshot()
	require.Len(t, calls, 1)
	assert.Equal(t, "repository.read.any", calls[0].action)
	assert.Equal(t, "catalog", calls[0].resource.Name)
	assert.Equal(t, "bob", calls[0].resource.OwnerSub)
	assert.Equal(t, h.targetNamespace.Name, calls[0].resource.Attrs["namespace"])
}

func TestRepositoryFieldAuthorizerUsesOwnActionForTenantOwner(t *testing.T) {
	h := newRepositoryAuthzHarness(t)
	ctx := auth.ContextWithPrincipal(context.Background(), &auth.Principal{
		Subject:    "alice",
		AuthMethod: "test",
	})

	err := h.authorizeField(ctx, "Query", "repository", map[string]any{"by": model.RepositoryBy{ID: &h.repositoryNodeID}})

	require.NoError(t, err)
	calls := h.authz.callsSnapshot()
	require.Len(t, calls, 1)
	assert.Equal(t, "repository.read.own", calls[0].action)
	assert.Equal(t, "alice", calls[0].resource.OwnerSub)
}
