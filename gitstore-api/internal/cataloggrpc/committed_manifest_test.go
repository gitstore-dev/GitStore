// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package cataloggrpc_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/gitstore-dev/gitstore/api/internal/admission"
	"github.com/gitstore-dev/gitstore/api/internal/catalog"
	"github.com/gitstore-dev/gitstore/api/internal/cataloggrpc"
	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// denyCategoryTitlePolicy denies any CategoryTaxonomy titled "Denied".
type denyCategoryTitlePolicy struct{}

func (denyCategoryTitlePolicy) Name() string { return "test-deny-category-title" }

func (denyCategoryTitlePolicy) Validate(_ context.Context, req admission.AdmissionRequest) admission.AdmissionDecision {
	if res, ok := req.Object.(*catalog.CategoryTaxonomyResource); ok && res.Spec.Title == "Denied" {
		return admission.DecisionDeny("title Denied is not allowed", "spec.title")
	}
	return admission.DecisionAllow()
}

func withDenyCategoryTitle(deps *cataloggrpc.ServerDeps) {
	deps.ExtraValidatingPolicies = append(deps.ExtraValidatingPolicies, denyCategoryTitlePolicy{})
}

func categoryManifestTitled(name, title string) []byte {
	return []byte("---\napiVersion: catalog.gitstore.dev/v1beta1\nkind: CategoryTaxonomy\nmetadata:\n  name: " + name +
		"\n  namespace: gitstore\nspec:\n  title: " + title + "\n---\n")
}

func categoryCondition(t *testing.T, c *datastore.CategoryTaxonomy, conditionType catalog.ConditionType) *catalog.Condition {
	t.Helper()
	var status catalog.CategoryTaxonomyStatus
	require.NoError(t, json.Unmarshal(c.Status, &status))
	for i := range status.Conditions {
		if status.Conditions[i].Type == conditionType {
			return &status.Conditions[i]
		}
	}
	return nil
}

func admitCommittedCategory(t *testing.T, srv *cataloggrpc.Server, commit string, content []byte, op admission.Operation) (*admission.CommittedManifestResult, error) {
	t.Helper()
	return srv.AdmitCommittedManifest(context.Background(), admission.CommittedManifestRequest{
		RepositoryID: testRepoID, Namespace: "gitstore", ActorSubject: "alice",
		CommitSHA: commit, RefName: "refs/heads/main", Path: "categories/electronics.md", Content: content,
		Operation: op,
	})
}

func TestAdmitCommittedManifest_CategoryTaxonomyDenialIsPostReceiveRejection(t *testing.T) {
	store := newTestDatastore(t)
	accepted, denied := strings.Repeat("a", 40), strings.Repeat("b", 40)
	current := accepted
	srv := newCatalogServer(t, store, newTreeGitReader(&current, map[string]map[string][]byte{
		accepted: {"categories/electronics.md": categoryManifestTitled("electronics", "Electronics")},
		denied:   {"categories/electronics.md": categoryManifestTitled("electronics", "Denied")},
	}), withDenyCategoryTitle)

	result, err := admitCommittedCategory(t, srv, accepted, categoryManifestTitled("electronics", "Electronics"), admission.OperationCreate)
	require.NoError(t, err)
	assert.False(t, result.NoOp)
	assert.Equal(t, accepted, result.CommitSHA)

	current = denied
	_, err = admitCommittedCategory(t, srv, denied, categoryManifestTitled("electronics", "Denied"), admission.OperationUpdate)
	var admissionErr *admission.Error
	require.True(t, errors.As(err, &admissionErr), "got %v", err)
	assert.Equal(t, admission.CodeAdmissionRejected, admissionErr.Code)
	assert.Equal(t, admission.PhasePostReceive, admissionErr.Phase)
	assert.Equal(t, denied, admissionErr.CommitSHA)
	require.NotEmpty(t, admissionErr.Diagnostics)
	assert.Equal(t, "POLICY_DENIED", admissionErr.Diagnostics[0].Reason)

	stored, err := store.GetCategoryTaxonomyByName(context.Background(), "gitstore", "electronics")
	require.NoError(t, err)
	assert.Equal(t, accepted, stored.GitCommitSHA, "the last accepted generation is kept")
	assert.Contains(t, string(stored.Spec), "Electronics")
}

func TestAdmitCommittedManifest_CategoryTaxonomyIdenticalReadmissionIsNoOp(t *testing.T) {
	store := newTestDatastore(t)
	first, second := strings.Repeat("a", 40), strings.Repeat("c", 40)
	current := first
	content := categoryManifestTitled("electronics", "Electronics")
	srv := newCatalogServer(t, store, newTreeGitReader(&current, map[string]map[string][]byte{
		first:  {"categories/electronics.md": content},
		second: {"categories/electronics.md": content},
	}))
	_, err := admitCommittedCategory(t, srv, first, content, admission.OperationCreate)
	require.NoError(t, err)

	current = second
	result, err := admitCommittedCategory(t, srv, second, content, admission.OperationUpdate)
	require.NoError(t, err)
	assert.True(t, result.NoOp)
	stored, err := store.GetCategoryTaxonomyByName(context.Background(), "gitstore", "electronics")
	require.NoError(t, err)
	assert.Equal(t, int64(1), stored.Generation)
}

func TestAdmitCommittedManifest_CategoryTaxonomySupersededByDifferentHead(t *testing.T) {
	store := newTestDatastore(t)
	mine, theirs := strings.Repeat("a", 40), strings.Repeat("d", 40)
	current := theirs
	srv := newCatalogServer(t, store, newTreeGitReader(&current, map[string]map[string][]byte{
		mine:   {"categories/electronics.md": categoryManifestTitled("electronics", "Electronics")},
		theirs: {"categories/electronics.md": categoryManifestTitled("electronics", "Something Else")},
	}))
	_, err := admitCommittedCategory(t, srv, mine, categoryManifestTitled("electronics", "Electronics"), admission.OperationCreate)
	require.ErrorIs(t, err, admission.ErrCommittedManifestSuperseded)
	_, err = store.GetCategoryTaxonomyByName(context.Background(), "gitstore", "electronics")
	assert.ErrorIs(t, err, datastore.ErrNotFound)
}

func TestAdmitCommittedManifest_CategoryTaxonomyDeletionStartsTermination(t *testing.T) {
	store := newTestDatastore(t)
	created, removed := strings.Repeat("a", 40), strings.Repeat("e", 40)
	current := created
	srv := newCatalogServer(t, store, newTreeGitReader(&current, map[string]map[string][]byte{
		created: {"categories/electronics.md": categoryManifestTitled("electronics", "Electronics")},
		removed: {},
	}))
	_, err := admitCommittedCategory(t, srv, created, categoryManifestTitled("electronics", "Electronics"), admission.OperationCreate)
	require.NoError(t, err)

	current = removed
	result, err := srv.AdmitCommittedManifest(context.Background(), admission.CommittedManifestRequest{
		RepositoryID: testRepoID, Namespace: "gitstore", ActorSubject: "alice", CommitSHA: removed, RefName: "refs/heads/main",
		Path: "categories/electronics.md", Operation: admission.OperationDelete, Kind: "CategoryTaxonomy", Name: "electronics",
	})
	require.NoError(t, err)
	assert.Equal(t, "CategoryTaxonomy", result.Kind)
	stored, err := store.GetCategoryTaxonomyByName(context.Background(), "gitstore", "electronics")
	require.NoError(t, err)
	assert.NotNil(t, stored.DeletionTimestamp)
	assert.Contains(t, stored.Finalizers, datastore.CategoryTaxonomyForegroundDeletionFinalizer)
}
