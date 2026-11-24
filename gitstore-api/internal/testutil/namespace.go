// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package testutil

import (
	"testing"
	"time"

	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	"github.com/gitstore-dev/gitstore/api/internal/datastore/memdb"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func NewNamespacePolicyDatastore(t *testing.T, systemRepository, otherRepository string) datastore.Datastore {
	t.Helper()
	store, err := memdb.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	now := time.Now().UTC()
	for _, entry := range []struct{ name, title, actor string }{
		{"gitstore-system", "GitStore System", "system"},
		{"default", "Default", "system"},
		{"other", "Other", "test"},
	} {
		id := uuid.NewString()
		require.NoError(t, store.CreateNamespace(t.Context(), &datastore.Namespace{
			ID: id, UID: id, Name: entry.name, Title: entry.title, Tier: datastore.NamespaceTierOrganization,
			CreationTimestamp: now, CreationActor: entry.actor, UpdateTimestamp: now, UpdateActor: entry.actor,
		}))
	}
	require.NoError(t, store.CreateNamespace(t.Context(), &datastore.Namespace{
		UID: uuid.NewString(), Name: "gitstore", Tier: datastore.NamespaceTierOrganization, CreationActor: "test",
	}))
	for _, entry := range []struct{ id, namespace, name, actor string }{
		{systemRepository, "gitstore-system", "gitstore-system", "system"},
		{otherRepository, "other", "catalog", "test"},
	} {
		require.NoError(t, store.CreateRepository(t.Context(), &datastore.Repository{
			UID: entry.id, Namespace: entry.namespace, Name: entry.name, DefaultBranch: "main", StorageClass: "local",
			CreationTimestamp: now, CreationActor: entry.actor, UpdateTimestamp: now, UpdateActor: entry.actor,
		}))
	}
	return store
}
