// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package resolver

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	"github.com/gitstore-dev/gitstore/api/internal/datastore/memdb"
	"github.com/gitstore-dev/gitstore/api/internal/graph/model"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

// TestUpdateFileStatusGeneric_ScopedStrictlyToNamespaceAndNameIdentity seeds
// two File rows that share the same name ("hero") in two different
// namespaces ("acme" and "other-tenant") and issues a generic
// updateResourceStatus mutation targeting only the "acme" one. It asserts
// that the "other-tenant" row's resourceVersion and status are completely
// untouched by the write — the generic File status path (spec 040's
// updateResourceStatus, reused by File per spec 051) is identified strictly
// by (namespace, name) and must never let one namespace's status write leak
// into or overwrite a same-named File belonging to a different namespace
// (spec 051 T041).
func TestUpdateFileStatusGeneric_ScopedStrictlyToNamespaceAndNameIdentity(t *testing.T) {
	store, err := memdb.New()
	require.NoError(t, err)
	defer store.Close()

	ctx := context.Background()
	require.NoError(t, store.CreateFile(ctx, &datastore.File{
		UID: uuid.New().String(), Namespace: "acme", Name: "hero", Kind: "File",
		APIVersion: "storage.gitstore.dev/v1beta1", ResourceVersion: "1",
		Status: json.RawMessage(`{"conditions":[]}`),
	}))
	require.NoError(t, store.CreateFile(ctx, &datastore.File{
		UID: uuid.New().String(), Namespace: "other-tenant", Name: "hero", Kind: "File",
		APIVersion: "storage.gitstore.dev/v1beta1", ResourceVersion: "1",
		Status: json.RawMessage(`{"conditions":[]}`),
	}))

	r, err := NewResolver(ResolverDeps{Store: store, Logger: zap.NewNop()})
	require.NoError(t, err)
	mr := &mutationResolver{Resolver: r}

	resolved := map[string]any{"resolvedVariants": []any{map[string]any{"name": "thumb", "url": "https://cdn/thumb"}}}
	payload, err := mr.UpdateResourceStatus(ctx, model.UpdateResourceStatusInput{
		Kind: "File", Namespace: "acme", Name: "hero", ResourceVersion: "1", Resolved: resolved,
	})
	require.NoError(t, err)
	require.Nil(t, payload.Conflict)
	require.NotNil(t, payload.Object)

	acme, err := store.GetFileByName(ctx, "acme", "hero")
	require.NoError(t, err)
	require.Equal(t, "2", acme.ResourceVersion)
	require.Contains(t, string(acme.Status), "thumb")

	otherTenant, err := store.GetFileByName(ctx, "other-tenant", "hero")
	require.NoError(t, err)
	require.Equal(t, "1", otherTenant.ResourceVersion, "a same-named File in a different namespace must be untouched by the write")
	require.NotContains(t, string(otherTenant.Status), "thumb")

	// A stale resourceVersion targeting the untouched namespace's row must
	// surface as its own independent conflict, proving the two identities
	// are tracked completely separately rather than sharing any state.
	conflict, err := mr.UpdateResourceStatus(ctx, model.UpdateResourceStatusInput{
		Kind: "File", Namespace: "other-tenant", Name: "hero", ResourceVersion: "2",
	})
	require.NoError(t, err)
	require.NotNil(t, conflict.Conflict)
	require.Equal(t, "1", conflict.Conflict.CurrentResourceVersion)
}
