// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package resolver_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/gitstore-dev/gitstore/api/internal/auth"
	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	"github.com/gitstore-dev/gitstore/api/internal/datastore/memdb"
	"github.com/gitstore-dev/gitstore/api/internal/graph/model"
	"github.com/gitstore-dev/gitstore/api/internal/graph/resolver"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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
	root             *resolver.Resolver
	authz            *tenantOwnershipAuthz
	sourceNamespace  *datastore.Namespace
	targetNamespace  *datastore.Namespace
	repository       *datastore.Repository
	repositoryNodeID string
}

func newRepositoryAuthzHarness(t *testing.T) *repositoryAuthzHarness {
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
		CreationActor: "alice",
		UpdateActor:   "alice",
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
	root, err := resolver.NewResolver(resolver.ResolverDeps{
		Store:    store,
		Registry: auth.NewProviderRegistry(nil, authz, nil),
		Logger:   zap.NewNop(),
	})
	require.NoError(t, err)
	repositoryNodeID, err := resolver.EncodeNodeID("Repository", repo.UID)
	require.NoError(t, err)

	return &repositoryAuthzHarness{
		root:             root,
		authz:            authz,
		sourceNamespace:  source,
		targetNamespace:  target,
		repository:       repo,
		repositoryNodeID: repositoryNodeID,
	}
}

func TestRepositoryResolversDenyCrossTenantAccessBeforeMutationOrRead(t *testing.T) {
	h := newRepositoryAuthzHarness(t)
	ctx := auth.ContextWithPrincipal(context.Background(), &auth.Principal{
		Subject:    "bob",
		AuthMethod: "test",
	})
	targetNamespaceNodeID, err := resolver.EncodeNodeID("Namespace", h.targetNamespace.UID)
	require.NoError(t, err)
	first := int32(10)

	tests := []struct {
		name   string
		action string
		call   func() error
	}{
		{
			name:   "create",
			action: "repository.create.any",
			call: func() error {
				_, err := h.root.Mutation().CreateRepository(ctx, model.CreateRepositoryInput{
					Namespace: h.sourceNamespace.Name,
					Name:      "new-catalog",
				})
				return err
			},
		},
		{
			name:   "rename",
			action: "repository.rename.any",
			call: func() error {
				_, err := h.root.Mutation().RenameRepository(ctx, model.RenameRepositoryInput{
					RepositoryID: h.repositoryNodeID,
					NewName:      "renamed-catalog",
				})
				return err
			},
		},
		{
			name:   "transfer",
			action: "repository.transfer.any",
			call: func() error {
				_, err := h.root.Mutation().TransferRepository(ctx, model.TransferRepositoryInput{
					RepositoryID:      h.repositoryNodeID,
					TargetNamespaceID: targetNamespaceNodeID,
				})
				return err
			},
		},
		{
			name:   "delete",
			action: "repository.delete.any",
			call: func() error {
				_, err := h.root.Mutation().DeleteRepository(ctx, model.DeleteRepositoryInput{
					RepositoryID: h.repositoryNodeID,
				})
				return err
			},
		},
		{
			name:   "read by ID",
			action: "repository.read.any",
			call: func() error {
				_, err := h.root.Query().Repository(ctx, model.RepositoryBy{ID: &h.repositoryNodeID})
				return err
			},
		},
		{
			name:   "read by namespace path",
			action: "repository.read.any",
			call: func() error {
				_, err := h.root.Query().Repository(ctx, model.RepositoryBy{
					NamespacePath: &model.RepositoryNamespacePath{
						Namespace: h.sourceNamespace.Name,
						Name:      h.repository.Name,
					},
				})
				return err
			},
		},
		{
			name:   "read through node",
			action: "repository.read.any",
			call: func() error {
				_, err := h.root.Query().Node(ctx, h.repositoryNodeID)
				return err
			},
		},
		{
			name:   "list namespace repositories",
			action: "repository.read.any",
			call: func() error {
				_, err := h.root.Query().Repositories(ctx, h.sourceNamespace.Name, &first, nil, nil, nil)
				return err
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			h.authz.reset()

			err := test.call()

			require.EqualError(t, err, "input: permission denied: cross-tenant access denied")
			calls := h.authz.callsSnapshot()
			require.Len(t, calls, 1)
			assert.Equal(t, test.action, calls[0].action)
			assert.Equal(t, "repository", calls[0].resource.Kind)
			assert.Equal(t, "alice", calls[0].resource.OwnerSub)
			assert.Equal(t, h.sourceNamespace.Name, calls[0].resource.Attrs["namespace"])
			if test.name == "transfer" {
				assert.Equal(t, h.targetNamespace.Name, calls[0].resource.Attrs["targetNamespace"])
				assert.Equal(t, "alice", calls[0].resource.Attrs["targetOwnerSub"])
			}
		})
	}
}

func TestRepositoryResolverUsesOwnActionForTenantOwner(t *testing.T) {
	h := newRepositoryAuthzHarness(t)
	ctx := auth.ContextWithPrincipal(context.Background(), &auth.Principal{
		Subject:    "alice",
		AuthMethod: "test",
	})

	repository, err := h.root.Query().Repository(ctx, model.RepositoryBy{ID: &h.repositoryNodeID})

	require.NoError(t, err)
	require.NotNil(t, repository)
	calls := h.authz.callsSnapshot()
	require.Len(t, calls, 1)
	assert.Equal(t, "repository.read.own", calls[0].action)
	assert.Equal(t, "alice", calls[0].resource.OwnerSub)
}
