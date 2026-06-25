// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package resolver_test

import (
	"context"
	"testing"

	"github.com/gitstore-dev/gitstore/api/internal/auth/provider/allowall"
	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	"github.com/gitstore-dev/gitstore/api/internal/graph/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

// ── createNamespace ────────────────────────────────────────────────────────────

func TestCreateNamespace_userTier_success(t *testing.T) {
	svc := newTestSvc(t, &mockGitWriter{})
	input := model.CreateNamespaceInput{
		Identifier: "acme-corp",
		Tier:       model.NamespaceTierUser,
	}
	ns, err := svc.CreateNamespace(context.Background(), input, "alice", false)
	require.NoError(t, err)
	require.NotNil(t, ns)
	assert.Equal(t, "acme-corp", ns.Identifier)
	assert.Equal(t, "alice", ns.CreatedBy)
	assert.NotEmpty(t, ns.ID)
}

func TestCreateNamespace_orgTier_success(t *testing.T) {
	// Wire an allow-all authz so a non-admin can create ORGANIZATION namespaces
	// (the authz provider controls this; nil-authz fallback requires isAdmin=true).
	svc := newTestSvcWithAuthZ(t, &mockGitWriter{}, allowall.New(zap.NewNop()))
	input := model.CreateNamespaceInput{
		Identifier: "acme-engineering",
		Tier:       model.NamespaceTierOrganization,
	}
	ns, err := svc.CreateNamespace(context.Background(), input, "bob", false)
	require.NoError(t, err)
	require.NotNil(t, ns)
	assert.Equal(t, "acme-engineering", ns.Identifier)
}

func TestCreateNamespace_duplicateIdentifier_conflict(t *testing.T) {
	svc := newTestSvc(t, &mockGitWriter{})
	input := model.CreateNamespaceInput{
		Identifier: "duplicate-ns",
		Tier:       model.NamespaceTierUser,
	}
	_, err := svc.CreateNamespace(context.Background(), input, "alice", false)
	require.NoError(t, err)

	// second call with same identifier
	_, err = svc.CreateNamespace(context.Background(), input, "bob", false)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "already exists")
}

func TestCreateNamespace_invalidIdentifier_spaces(t *testing.T) {
	svc := newTestSvc(t, &mockGitWriter{})
	input := model.CreateNamespaceInput{
		Identifier: "invalid identifier",
		Tier:       model.NamespaceTierUser,
	}
	_, err := svc.CreateNamespace(context.Background(), input, "alice", false)
	assert.Error(t, err)
}

func TestCreateNamespace_uppercaseIdentifier_normalizedToLowercase(t *testing.T) {
	svc := newTestSvc(t, &mockGitWriter{})
	input := model.CreateNamespaceInput{
		Identifier: "InvalidNS",
		Tier:       model.NamespaceTierUser,
	}
	// uppercase is folded to lowercase before validation; "invalidns" is a valid identifier
	ns, err := svc.CreateNamespace(context.Background(), input, "alice", false)
	require.NoError(t, err)
	assert.Equal(t, "invalidns", ns.Identifier)
}

func TestCreateNamespace_invalidIdentifier_leadingHyphen(t *testing.T) {
	svc := newTestSvc(t, &mockGitWriter{})
	input := model.CreateNamespaceInput{
		Identifier: "-leading-hyphen",
		Tier:       model.NamespaceTierUser,
	}
	_, err := svc.CreateNamespace(context.Background(), input, "alice", false)
	assert.Error(t, err)
}

func TestCreateNamespace_reservedIdentifier_admin(t *testing.T) {
	svc := newTestSvc(t, &mockGitWriter{})
	input := model.CreateNamespaceInput{
		Identifier: "admin",
		Tier:       model.NamespaceTierUser,
	}
	_, err := svc.CreateNamespace(context.Background(), input, "alice", false)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "reserved")
}

func TestCreateNamespace_enterpriseTier_rejected(t *testing.T) {
	svc := newTestSvc(t, &mockGitWriter{})
	// ENTERPRISE is no longer a valid NamespaceTier value; the service must reject it
	// regardless of caller permissions. The raw string bypasses schema validation so the
	// service-layer guard is exercised directly.
	input := model.CreateNamespaceInput{
		Identifier: "acme-enterprise",
		Tier:       model.NamespaceTier("ENTERPRISE"),
	}
	_, err := svc.CreateNamespace(context.Background(), input, "admin-user", true)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "enterprise")
}

// ── namespaces query ───────────────────────────────────────────────────────────

func TestListNamespaces_returnsAll(t *testing.T) {
	svc := newTestSvc(t, &mockGitWriter{})

	for _, id := range []string{"ns-alpha", "ns-beta", "ns-gamma"} {
		input := model.CreateNamespaceInput{Identifier: id, Tier: model.NamespaceTierUser}
		_, err := svc.CreateNamespace(context.Background(), input, "alice", false)
		require.NoError(t, err)
	}

	result, err := svc.ListNamespaces(context.Background(), datastore.PageParams{})
	require.NoError(t, err)
	assert.GreaterOrEqual(t, len(result.Items), 3)
}

// ── namespace query ────────────────────────────────────────────────────────────

func TestGetNamespaceByIdentifier_success(t *testing.T) {
	svc := newTestSvc(t, &mockGitWriter{})
	input := model.CreateNamespaceInput{Identifier: "lookup-me", Tier: model.NamespaceTierUser}
	created, err := svc.CreateNamespace(context.Background(), input, "alice", false)
	require.NoError(t, err)

	got, err := svc.GetNamespaceByIdentifier(context.Background(), "lookup-me")
	require.NoError(t, err)
	assert.Equal(t, created.ID, got.ID)
}

func TestGetNamespaceByIdentifier_notFound(t *testing.T) {
	svc := newTestSvc(t, &mockGitWriter{})
	_, err := svc.GetNamespaceByIdentifier(context.Background(), "does-not-exist")
	assert.Error(t, err)
}

// ── deleteNamespace ────────────────────────────────────────────────────────────

func TestDeleteNamespace_owner_success(t *testing.T) {
	svc := newTestSvc(t, &mockGitWriter{})
	input := model.CreateNamespaceInput{Identifier: "to-delete", Tier: model.NamespaceTierUser}
	_, err := svc.CreateNamespace(context.Background(), input, "alice", false)
	require.NoError(t, err)

	err = svc.DeleteNamespace(context.Background(), "to-delete", "alice", false)
	require.NoError(t, err)

	_, err = svc.GetNamespaceByIdentifier(context.Background(), "to-delete")
	assert.Error(t, err)
}

func TestDeleteNamespace_admin_canDeleteAny(t *testing.T) {
	svc := newTestSvc(t, &mockGitWriter{})
	input := model.CreateNamespaceInput{Identifier: "owned-by-alice", Tier: model.NamespaceTierUser}
	_, err := svc.CreateNamespace(context.Background(), input, "alice", false)
	require.NoError(t, err)

	// admin deletes alice's namespace
	err = svc.DeleteNamespace(context.Background(), "owned-by-alice", "admin-user", true)
	require.NoError(t, err)
}

func TestDeleteNamespace_nonOwner_nonAdmin_denied(t *testing.T) {
	svc := newTestSvc(t, &mockGitWriter{})
	input := model.CreateNamespaceInput{Identifier: "alices-ns", Tier: model.NamespaceTierUser}
	_, err := svc.CreateNamespace(context.Background(), input, "alice", false)
	require.NoError(t, err)

	err = svc.DeleteNamespace(context.Background(), "alices-ns", "bob", false)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "permission denied")
}

func TestDeleteNamespace_unknownIdentifier_notFound(t *testing.T) {
	svc := newTestSvc(t, &mockGitWriter{})
	err := svc.DeleteNamespace(context.Background(), "does-not-exist", "alice", false)
	assert.Error(t, err)
}

func TestDeleteNamespace_unauthenticated(t *testing.T) {
	// Unauthenticated check is in the resolver, not service layer.
	// Service requires callerUsername — empty caller cannot be owner.
	svc := newTestSvc(t, &mockGitWriter{})
	input := model.CreateNamespaceInput{Identifier: "auth-test-ns", Tier: model.NamespaceTierUser}
	_, err := svc.CreateNamespace(context.Background(), input, "alice", false)
	require.NoError(t, err)

	// empty caller is not the owner and not admin
	err = svc.DeleteNamespace(context.Background(), "auth-test-ns", "", false)
	assert.Error(t, err)
}
