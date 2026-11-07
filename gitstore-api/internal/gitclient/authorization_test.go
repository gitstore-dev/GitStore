// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package gitclient

import (
	"context"
	"testing"

	"github.com/gitstore-dev/gitstore/api/internal/auth"
	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	"github.com/gitstore-dev/gitstore/api/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type authorizationRepositoryFixture struct {
	repository *datastore.Repository
	err        error
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
