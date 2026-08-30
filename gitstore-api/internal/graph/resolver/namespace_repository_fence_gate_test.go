// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package resolver_test

import (
	"context"
	"testing"

	"github.com/gitstore-dev/gitstore/api/internal/graph/model"
	"github.com/gitstore-dev/gitstore/api/internal/graph/resolver"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/vektah/gqlparser/v2/gqlerror"
)

func TestNamespaceRepositoryFenceRolloutGateRejectsUnsafeMutations(t *testing.T) {
	ctx := context.Background()
	svc := newTestSvcWithFenceMode(t, &mockGitWriter{}, resolver.NamespaceRepositoryFenceDisabled)
	namespace, err := svc.CreateNamespace(ctx, createNamespaceInput("rollout-gated", model.NamespaceTierUser), "alice")
	require.NoError(t, err)

	_, err = svc.CreateRepository(ctx, namespace.Name, "catalog", "", "", "alice")
	assertNamespaceRepositoryFenceGateError(t, err, "CREATE_REPOSITORY")

	_, err = svc.TransferRepository(ctx, "01960000-0000-7000-8000-000000000099", namespace.Name, "alice")
	assertNamespaceRepositoryFenceGateError(t, err, "TRANSFER_REPOSITORY")

	_, err = svc.DeleteNamespace(ctx, namespace)
	assertNamespaceRepositoryFenceGateError(t, err, "DELETE_NAMESPACE")

	_, err = svc.CompleteNamespaceDeletion(ctx, namespace.Name, namespace.ResourceVersion)
	assertNamespaceRepositoryFenceGateError(t, err, "COMPLETE_NAMESPACE_DELETION")

	persisted, err := svc.GetNamespaceByName(ctx, namespace.Name)
	require.NoError(t, err)
	assert.Nil(t, persisted.DeletionTimestamp)
	hasRepositories, err := svc.Store().HasRepositories(ctx, namespace.Name)
	require.NoError(t, err)
	assert.False(t, hasRepositories)
}

func assertNamespaceRepositoryFenceGateError(t *testing.T, err error, operation string) {
	t.Helper()
	require.Error(t, err)
	var graphErr *gqlerror.Error
	require.ErrorAs(t, err, &graphErr)
	assert.Equal(t, "NAMESPACE_REPOSITORY_FENCE_DISABLED", graphErr.Extensions["code"])
	assert.Equal(t, "ROLLOUT_GATE_DISABLED", graphErr.Extensions["reason"])
	assert.Equal(t, operation, graphErr.Extensions["operation"])
}
