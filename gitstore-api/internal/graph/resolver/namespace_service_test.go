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
)

// ── createNamespace ────────────────────────────────────────────────────────────

func TestCreateNamespace_userTier_success(t *testing.T) {
	svc := newTestSvc(t, &mockGitWriter{})
	input := model.CreateNamespaceInput{
		Identifier: "acme-corp",
		Tier:       model.NamespaceTierUser,
	}
	ns, err := svc.CreateNamespace(context.Background(), input, "alice")
	require.NoError(t, err)
	require.NotNil(t, ns)
	assert.Equal(t, "acme-corp", ns.Identifier)
	assert.Equal(t, "alice", ns.CreatedBy)
	assert.NotEmpty(t, ns.ID)
}

func TestCreateNamespace_orgTier_success(t *testing.T) {
	svc := newTestSvc(t, &mockGitWriter{})
	input := model.CreateNamespaceInput{
		Identifier: "acme-engineering",
		Tier:       model.NamespaceTierOrganization,
	}
	ns, err := svc.CreateNamespace(context.Background(), input, "bob")
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
	_, err := svc.CreateNamespace(context.Background(), input, "alice")
	require.NoError(t, err)

	// second call with same identifier
	_, err = svc.CreateNamespace(context.Background(), input, "bob")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "already exists")
}

func TestCreateNamespace_invalidIdentifier_spaces(t *testing.T) {
	svc := newTestSvc(t, &mockGitWriter{})
	input := model.CreateNamespaceInput{
		Identifier: "invalid identifier",
		Tier:       model.NamespaceTierUser,
	}
	_, err := svc.CreateNamespace(context.Background(), input, "alice")
	assert.Error(t, err)
}

func TestCreateNamespace_uppercaseIdentifier_normalizedToLowercase(t *testing.T) {
	svc := newTestSvc(t, &mockGitWriter{})
	input := model.CreateNamespaceInput{
		Identifier: "InvalidNS",
		Tier:       model.NamespaceTierUser,
	}
	// uppercase is folded to lowercase before validation; "invalidns" is a valid identifier
	ns, err := svc.CreateNamespace(context.Background(), input, "alice")
	require.NoError(t, err)
	assert.Equal(t, "invalidns", ns.Identifier)
}

func TestCreateNamespace_invalidIdentifier_leadingHyphen(t *testing.T) {
	svc := newTestSvc(t, &mockGitWriter{})
	input := model.CreateNamespaceInput{
		Identifier: "-leading-hyphen",
		Tier:       model.NamespaceTierUser,
	}
	_, err := svc.CreateNamespace(context.Background(), input, "alice")
	assert.Error(t, err)
}

func TestCreateNamespace_reservedIdentifier_admin(t *testing.T) {
	svc := newTestSvc(t, &mockGitWriter{})
	input := model.CreateNamespaceInput{
		Identifier: "admin",
		Tier:       model.NamespaceTierUser,
	}
	_, err := svc.CreateNamespace(context.Background(), input, "alice")
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
	_, err := svc.CreateNamespace(context.Background(), input, "admin-user")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "enterprise")
}

func TestCreateNamespace_provisionsSystemRepository(t *testing.T) {
	svc := newTestSvc(t, &mockGitWriter{})
	ctx := context.Background()
	input := model.CreateNamespaceInput{Identifier: "provisions-system-repo", Tier: model.NamespaceTierUser}
	ns, err := svc.CreateNamespace(ctx, input, "alice")
	require.NoError(t, err)

	result, err := svc.Store().ListRepositoriesByNamespace(ctx, ns.ID, datastore.PageParams{})
	require.NoError(t, err)
	require.Len(t, result.Items, 1)
	assert.Equal(t, resolver.SystemRepositoryName, result.Items[0].Name)
}

func TestCreateNamespace_retriedSystemRepositoryProvisioning_noDuplicate(t *testing.T) {
	svc := newTestSvc(t, &mockGitWriter{})
	ctx := context.Background()
	input := model.CreateNamespaceInput{Identifier: "retried-system-repo", Tier: model.NamespaceTierUser}
	ns, err := svc.CreateNamespace(ctx, input, "alice")
	require.NoError(t, err)

	err = svc.ProvisionSystemRepository(ctx, ns.ID, "alice")
	require.NoError(t, err)

	result, err := svc.Store().ListRepositoriesByNamespace(ctx, ns.ID, datastore.PageParams{})
	require.NoError(t, err)
	assert.Len(t, result.Items, 1)
}

func TestCreateNamespace_retryAfterSystemRepositoryProvisioningFailure_resumes(t *testing.T) {
	writer := &mockGitWriter{createRepoErr: errors.New("provisioning failed")}
	svc := newTestSvc(t, writer)
	ctx := context.Background()
	input := model.CreateNamespaceInput{Identifier: "retry-partial-namespace", Tier: model.NamespaceTierUser}

	_, err := svc.CreateNamespace(ctx, input, "alice")
	require.ErrorContains(t, err, "failed to provision system repository")

	writer.createRepoErr = nil
	ns, err := svc.CreateNamespace(ctx, input, "alice")
	require.NoError(t, err)

	result, err := svc.Store().ListRepositoriesByNamespace(ctx, ns.ID, datastore.PageParams{})
	require.NoError(t, err)
	require.Len(t, result.Items, 1)
	assert.Equal(t, resolver.SystemRepositoryName, result.Items[0].Name)
}

// ── namespaces query ───────────────────────────────────────────────────────────

func TestListNamespaces_returnsAll(t *testing.T) {
	svc := newTestSvc(t, &mockGitWriter{})

	for _, id := range []string{"ns-alpha", "ns-beta", "ns-gamma"} {
		input := model.CreateNamespaceInput{Identifier: id, Tier: model.NamespaceTierUser}
		_, err := svc.CreateNamespace(context.Background(), input, "alice")
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
	created, err := svc.CreateNamespace(context.Background(), input, "alice")
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

// deleteSystemRepository removes the auto-provisioned system repository for
// ns so a subsequent DeleteNamespace call satisfies the has-repositories
// precondition (FR-001), matching quickstart.md's documented delete order.
func deleteSystemRepository(t *testing.T, svc *resolver.Service, ns *datastore.Namespace) {
	t.Helper()
	ctx := context.Background()
	m, err := svc.Store().LookupRepository(ctx, ns.ID, resolver.SystemRepositoryName)
	require.NoError(t, err)
	require.NoError(t, svc.DeleteRepository(ctx, m.RepoID, "test"))
}

func TestDeleteNamespace_owner_success(t *testing.T) {
	svc := newTestSvc(t, &mockGitWriter{})
	input := model.CreateNamespaceInput{Identifier: "to-delete", Tier: model.NamespaceTierUser}
	ns, err := svc.CreateNamespace(context.Background(), input, "alice")
	require.NoError(t, err)
	deleteSystemRepository(t, svc, ns)

	err = svc.DeleteNamespace(context.Background(), ns)
	require.NoError(t, err)

	_, err = svc.GetNamespaceByIdentifier(context.Background(), "to-delete")
	assert.Error(t, err)
}

func TestDeleteNamespace_admin_canDeleteAny(t *testing.T) {
	svc := newTestSvc(t, &mockGitWriter{})
	input := model.CreateNamespaceInput{Identifier: "owned-by-alice", Tier: model.NamespaceTierUser}
	ns, err := svc.CreateNamespace(context.Background(), input, "alice")
	require.NoError(t, err)
	deleteSystemRepository(t, svc, ns)

	// admin deletes alice's namespace
	err = svc.DeleteNamespace(context.Background(), ns)
	require.NoError(t, err)
}

func TestDeleteNamespace_withoutAuthorizationCheck_serviceAllowsDelete(t *testing.T) {
	svc := newTestSvc(t, &mockGitWriter{})
	input := model.CreateNamespaceInput{Identifier: "alices-ns", Tier: model.NamespaceTierUser}
	ns, err := svc.CreateNamespace(context.Background(), input, "alice")
	require.NoError(t, err)
	deleteSystemRepository(t, svc, ns)

	err = svc.DeleteNamespace(context.Background(), ns)
	require.NoError(t, err)
}

func TestDeleteNamespace_unknownIdentifier_notFound(t *testing.T) {
	svc := newTestSvc(t, &mockGitWriter{})
	err := svc.DeleteNamespace(context.Background(), &datastore.Namespace{ID: "does-not-exist", Identifier: "does-not-exist"})
	assert.Error(t, err)
}

func TestDeleteNamespace_withRepository_rejected(t *testing.T) {
	svc := newTestSvc(t, &mockGitWriter{})
	ctx := context.Background()
	input := model.CreateNamespaceInput{Identifier: "ns-with-repo", Tier: model.NamespaceTierUser}
	ns, err := svc.CreateNamespace(ctx, input, "alice")
	require.NoError(t, err)

	_, err = svc.CreateRepository(ctx, ns.ID, "some-repo", "main", "default", "alice")
	require.NoError(t, err)

	err = svc.DeleteNamespace(ctx, ns)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "contains repositories and cannot be deleted")

	_, err = svc.GetNamespaceByIdentifier(ctx, ns.Identifier)
	require.NoError(t, err)

	result, err := svc.Store().ListRepositoriesByNamespace(ctx, ns.ID, datastore.PageParams{})
	require.NoError(t, err)
	assert.Len(t, result.Items, 2) // auto-provisioned system repository + the created one
}

func TestDeleteNamespace_afterRepositoriesRemoved_succeeds(t *testing.T) {
	svc := newTestSvc(t, &mockGitWriter{})
	ctx := context.Background()
	input := model.CreateNamespaceInput{Identifier: "ns-repo-removed", Tier: model.NamespaceTierUser}
	ns, err := svc.CreateNamespace(ctx, input, "alice")
	require.NoError(t, err)

	repo, err := svc.CreateRepository(ctx, ns.ID, "temp-repo", "main", "default", "alice")
	require.NoError(t, err)

	require.NoError(t, svc.DeleteRepository(ctx, repo.ID, "alice"))
	deleteSystemRepository(t, svc, ns)

	err = svc.DeleteNamespace(ctx, ns)
	require.NoError(t, err)
}

func TestDeleteNamespace_recreatedIdentifierDoesNotDeleteReplacement(t *testing.T) {
	svc := newTestSvc(t, &mockGitWriter{})
	input := model.CreateNamespaceInput{Identifier: "auth-test-ns", Tier: model.NamespaceTierUser}
	authorized, err := svc.CreateNamespace(context.Background(), input, "alice")
	require.NoError(t, err)
	require.NoError(t, svc.Store().DeleteNamespace(context.Background(), authorized.ID))
	replacement, err := svc.CreateNamespace(context.Background(), input, "mallory")
	require.NoError(t, err)

	err = svc.DeleteNamespace(context.Background(), authorized)
	require.Error(t, err)

	got, err := svc.GetNamespaceByIdentifier(context.Background(), input.Identifier)
	require.NoError(t, err)
	assert.Equal(t, replacement.ID, got.ID)
}
