// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package resolver_test

import (
	"context"
	"errors"
	"testing"

	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	"github.com/gitstore-dev/gitstore/api/internal/graph/model"
	"github.com/gitstore-dev/gitstore/api/internal/graph/resolver"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

type failingNamespaceLookupStore struct {
	datastore.Datastore
	name string
}

func (s *failingNamespaceLookupStore) GetNamespaceByName(ctx context.Context, name string) (*datastore.Namespace, error) {
	if name == s.name {
		return nil, errors.New("datastore unavailable")
	}
	return s.Datastore.GetNamespaceByName(ctx, name)
}

func TestUpdateNamespace_FailsClosedWhenOwnerGuardLookupFails(t *testing.T) {
	ctx := context.Background()
	seed := newTestSvc(t, &mockGitWriter{})
	_, err := seed.CreateNamespace(ctx, createNamespaceInput("owner-guard-lookup", model.NamespaceTierUser), "alice")
	require.NoError(t, err)

	writer := &mockGitWriter{}
	svc, err := resolver.NewService(resolver.ServiceDeps{
		Store:     &failingNamespaceLookupStore{Datastore: seed.Store(), name: "owner-guard-lookup"},
		GitWriter: writer,
		Logger:    zap.NewNop(),
	})
	require.NoError(t, err)

	_, err = svc.UpdateNamespace(ctx, updateNamespaceInput("owner-guard-lookup", model.NamespaceTierUser), "alice")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to validate Namespace operation")
}

func TestTransferNamespaceOwner_RejectsWhenOwnerChangedSinceAuthorization(t *testing.T) {
	ctx := context.Background()
	svc := newTestSvc(t, &mockGitWriter{})
	ns, err := svc.CreateNamespace(ctx, createNamespaceInput("owner-transfer-race", model.NamespaceTierUser), "alice")
	require.NoError(t, err)

	stale := *ns
	stale.Annotations = map[string]string{datastore.OwnerAnnotationKey: "mallory"}
	_, err = svc.TransferNamespaceOwner(ctx, &stale, "system:group:merchandiser", "mallory")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "owner changed since authorization")
}
