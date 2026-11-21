// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package gitclient

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/gitstore-dev/gitstore/api/internal/auth"
	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	"github.com/gitstore-dev/gitstore/api/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPinnedBackingWriteRetainsCapturedAuthorityWithoutBroadeningScope(t *testing.T) {
	registry := auth.NewProviderRegistry(nil, testutil.NewDenyAllAuthZ(t), nil)
	client := &Client{Registry: registry, Store: authorizationRepositoryFixture{
		repository: &datastore.Repository{Name: "gitstore-system", Namespace: "acme"},
	}}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	calls := 0
	pinned := ContextWithPinnedBackingAuthorization(ctx, "captured-repository",
		func(ctx context.Context, principal *auth.Principal, action string, resource auth.ResourceContext) (auth.Decision, error) {
			calls++
			require.Equal(t, "system:api", principal.Subject)
			require.Equal(t, "internal", principal.AuthMethod)
			require.Equal(t, auth.ActionRepositoryContentsWrite, action)
			require.Equal(t, "acme", resource.Attrs["namespace"])
			if err := ctx.Err(); err != nil {
				return auth.Decision{}, err
			}
			return auth.Decision{Outcome: auth.OutcomeAllow}, nil
		})
	internal := ContextForBackingWork(pinned)
	_, err := client.authorizeRequest(internal, auth.ActionRepositoryContentsWrite, "captured-repository")
	require.NoError(t, err, "live cache invalidation must not veto an already-captured backing write")
	require.Equal(t, 1, calls)

	for _, test := range []struct {
		ctx        context.Context
		action     string
		repository string
	}{
		{internal, auth.ActionRepositoryContentsWrite, "other-repository"},
		{internal, auth.ActionRepositoryContentsRead, "captured-repository"},
		{internal, auth.ActionRepositoryDelete, "captured-repository"},
		{auth.ContextWithPrincipal(pinned, &auth.Principal{Subject: "alice", AuthMethod: "static-users"}), auth.ActionRepositoryContentsWrite, "captured-repository"},
	} {
		_, err := client.authorizeRequest(test.ctx, test.action, test.repository)
		require.ErrorIs(t, err, ErrAuthorizationDenied)
	}
	require.Equal(t, 1, calls, "other actors, operations and repositories must retain live authorization")
	cancel()
	_, err = client.authorizeRequest(internal, auth.ActionRepositoryContentsWrite, "captured-repository")
	require.True(t, errors.Is(err, context.Canceled))
}

type authorizationRepositoryFixture struct {
	repository *datastore.Repository
	err        error
}

type namespaceCleanupRepositoryFixture struct {
	authorizationRepositoryFixture
	parent *datastore.Namespace
}

func (f namespaceCleanupRepositoryFixture) GetNamespaceByName(context.Context, string) (*datastore.Namespace, error) {
	return f.parent, nil
}

func TestNamespaceCleanupScopeComesOnlyFromTerminatingSystemStorage(t *testing.T) {
	for _, scenario := range []string{"cleanup", "live namespace", "live repository", "ordinary repository", "authored reserved name", "other operation"} {
		t.Run(scenario, func(t *testing.T) {
			now := time.Now()
			parent := &datastore.Namespace{Name: "shop", DeletionTimestamp: &now}
			repository := &datastore.Repository{UID: "repo", RepositoryID: "repo", Namespace: "shop",
				Name: "gitstore-system", DeletionTimestamp: &now}
			action := auth.ActionRepositoryDelete
			switch scenario {
			case "live namespace":
				parent.DeletionTimestamp = nil
			case "live repository":
				repository.DeletionTimestamp = nil
			case "ordinary repository":
				repository.Name = "catalog"
			case "authored reserved name":
				repository.SourcePath = "repositories/forged.md"
			case "other operation":
				action = auth.ActionRepositoryContentsWrite
			}
			recorder := testutil.NewAllowAllAuthZ()
			client := &Client{Registry: auth.NewProviderRegistry(nil, recorder, nil),
				Store: namespaceCleanupRepositoryFixture{authorizationRepositoryFixture{repository: repository}, parent}}
			envelope, err := client.authorizeRequest(ContextForBackingWork(t.Context()), action, "repo")
			require.NoError(t, err)
			require.Equal(t, "repo", envelope.RepositoryId)
			require.Equal(t, "shop", recorder.Resource.Attrs["namespace"])
			require.Equal(t, scenario == "cleanup", recorder.Resource.Attrs["namespaceCleanup"] == true)
		})
	}
}

func (f authorizationRepositoryFixture) GetRepository(context.Context, string) (*datastore.Repository, error) {
	return f.repository, f.err
}

func TestClientAuthorizationUsesStoredFactsAndLiveProvider(t *testing.T) {
	recorder := testutil.NewAllowAllAuthZ()
	registry := auth.NewProviderRegistry(nil, recorder, nil)
	client := &Client{Registry: registry, Store: authorizationRepositoryFixture{repository: &datastore.Repository{Name: "catalog", Namespace: "acme", CreationActor: "alice", Spec: []byte(`{"visibility":"PRIVATE"}`)}}}
	ctx := auth.ContextWithPrincipal(t.Context(), &auth.Principal{Subject: "alice", AuthMethod: "static-users"})
	_, err := client.authorizeRequest(ctx, auth.ActionRepositoryContentsRead, "repo-id")
	require.NoError(t, err)
	require.Equal(t, "catalog", recorder.Resource.Name)
	require.Equal(t, "alice", recorder.Resource.OwnerSub)
	require.Equal(t, map[string]any{"namespace": "acme", "visibility": "private"}, recorder.Resource.Attrs)
	registry.Swap(nil, testutil.NewDenyAllAuthZ(t), nil)
	_, err = client.authorizeRequest(ctx, auth.ActionRepositoryContentsRead, "repo-id")
	require.ErrorIs(t, err, ErrAuthorizationDenied)
	registry.Swap(nil, recorder, nil)
	client.Store = authorizationRepositoryFixture{}
	_, err = client.authorizeRequest(ctx, auth.ActionRepositoryContentsRead, "repo-id")
	require.Error(t, err, "missing authoritative facts cannot be silently omitted for a user")
	client.Registry = nil
	_, err = client.authorizeRequest(ContextForBackingWork(ctx), auth.ActionRepositoryCreate, "repo-id")
	require.ErrorIs(t, err, ErrAuthorizationDenied)
}

func TestRequestAuthorizationAllowsOnlyPolicyApprovedAnonymousRead(t *testing.T) {
	ctx := auth.ContextWithPrincipal(context.Background(), auth.Anonymous())

	_, err := RequestAuthorization(ctx, "repository.contents.read", "repo-id")
	require.Error(t, err)

	ctx = auth.ContextWithAuthorizedAnonymous(ctx)
	authorization, err := RequestAuthorization(ctx, "repository.contents.read", "repo-id")
	require.NoError(t, err)
	assert.Equal(t, "anon", authorization.Actor.Subject)
	assert.Equal(t, "none", authorization.Actor.AuthMethod)

	_, err = RequestAuthorization(ctx, "repository.contents.write", "repo-id")
	require.Error(t, err)
}

func TestRequestAuthorizationCanonicalActionsAndExplicitActor(t *testing.T) {
	for _, principal := range []*auth.Principal{
		{Subject: "system:api", AuthMethod: "static-users"},
		{Subject: "alice", AuthMethod: "internal"},
		{Subject: "system:serviceaccount:ci", AuthMethod: "serviceaccount-jwt"},
		{Subject: "alice", AuthMethod: "static-users", ServiceAccountUID: "uid"},
	} {
		_, err := RequestAuthorization(auth.ContextWithPrincipal(t.Context(), principal), "repository.contents.read", "repo-id")
		require.ErrorIs(t, err, ErrAuthorizationDenied)
	}
	_, err := RequestAuthorization(context.Background(), "repository.contents.read", "repo-id")
	require.ErrorIs(t, err, ErrAuthorizationDenied, "missing actor must never be promoted to system:api")
	ctx := auth.ContextWithPrincipal(t.Context(), &auth.Principal{Subject: "alice", AuthMethod: "static-users"})
	for _, action := range []string{"repository.read.any", "repository.write.own", "product.read", "repository.future"} {
		_, err := RequestAuthorization(ctx, action, "repo-id")
		require.ErrorIs(t, err, ErrAuthorizationDenied, action)
	}
	for _, action := range []string{"repository.contents.read", "repository.contents.write", "repository.create", "repository.delete"} {
		envelope, err := RequestAuthorization(ctx, action, "repo-id")
		require.NoError(t, err)
		require.Equal(t, "repository", envelope.ResourceKind)
		require.Equal(t, action, envelope.Action)
	}
}
