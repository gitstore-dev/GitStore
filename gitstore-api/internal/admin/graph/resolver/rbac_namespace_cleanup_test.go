// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package resolver

import (
	"testing"

	"github.com/gitstore-dev/gitstore/api/internal/auth"
	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	"github.com/gitstore-dev/gitstore/api/internal/rbac"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestNamespaceCleanupGraphQLRequiresControllerAndGlobalPermissionAndKeepsReplacement(t *testing.T) {
	root, _, store := newRBACResourceFixture(t)
	parent, err := root.store.GetNamespaceByName(t.Context(), "shop")
	require.NoError(t, err)
	before, err := store.ReadRBACTier(t.Context(), parent.UID)
	require.NoError(t, err)
	job := rbac.NamespaceCleanup{NamespaceUID: uuid.NewString(), Namespace: "shop"}
	require.NoError(t, store.ScheduleRBACNamespaceCleanup(t.Context(), job))
	controller := &auth.Principal{Subject: "system:serviceaccount:gitstore-controller-manager",
		ServiceAccountUID: uuid.NewString(), AuthMethod: "serviceaccount-jwt"}
	root.rbacValidator.ManagedStore = root.store
	root.rbacValidator.ControllerSubject = controller.Subject
	require.NoError(t, root.store.CreateServiceAccount(t.Context(), &datastore.ServiceAccount{
		UID: controller.ServiceAccountUID, Name: "gitstore-controller-manager",
	}))
	query := `mutation($input:ReconcileRBACNamespaceCleanupsInput!) {
		reconcileRBACNamespaceCleanups(input:$input) { nextCursor pending }
	}`
	_, rejected := managedContractRequest(t, root, &auth.Principal{Subject: "alice", AuthMethod: "static-users",
		Groups: []string{"system:masters"}}, query, map[string]any{})
	require.Len(t, rejected, 1)
	require.Equal(t, "FORBIDDEN", rejected[0].Extensions["code"])
	_, rejected = managedContractRequest(t, root, controller, query, map[string]any{})
	require.Len(t, rejected, 1, "enrollment alone does not grant the maintenance permission")
	require.Equal(t, "FORBIDDEN", rejected[0].Extensions["code"])
	root.registry.AuthZ().(*rbacResourceProvider).allowLifecycle.Store(true)
	data, rejected := managedContractRequest(t, root, controller, query, map[string]any{})
	require.Empty(t, rejected)
	require.Equal(t, false, data["reconcileRBACNamespaceCleanups"]["pending"])
	after, err := store.ReadRBACTier(t.Context(), parent.UID)
	require.NoError(t, err)
	require.Equal(t, before, after, "a same-name replacement's tier must never be collected")
	retired, err := store.ReadRBACTier(t.Context(), job.NamespaceUID)
	require.NoError(t, err)
	require.True(t, retired.NamespaceRetired)
}
