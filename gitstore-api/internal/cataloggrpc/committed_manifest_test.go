// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package cataloggrpc_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/gitstore-dev/gitstore/api/internal/admission"
	"github.com/gitstore-dev/gitstore/api/internal/catalog"
	"github.com/gitstore-dev/gitstore/api/internal/cataloggrpc"
	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	grpcstatus "google.golang.org/grpc/status"
)

type failCategoryCreateOnceStore struct {
	datastore.Datastore
	remaining atomic.Int32
}

func (s *failCategoryCreateOnceStore) CreateCategoryTaxonomy(ctx context.Context, category *datastore.CategoryTaxonomy) error {
	if s.remaining.CompareAndSwap(1, 0) {
		return errors.New("injected category create failure")
	}
	return s.Datastore.CreateCategoryTaxonomy(ctx, category)
}

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

func admitCommittedCategory(t *testing.T, srv *catalogTestServer, commit string, content []byte, op admission.Operation) (*admission.CommittedManifestResult, error) {
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

func TestAdmitCommittedManifestRefFailurePreservesCause(t *testing.T) {
	for _, failure := range []error{context.Canceled, context.DeadlineExceeded, errors.New("ref IO failure")} {
		t.Run(failure.Error(), func(t *testing.T) {
			store := newTestDatastore(t)
			srv := newCatalogServer(t, store, &mockGitReader{
				resolveRefFunc: func(context.Context, string, string) (string, error) {
					return "", failure
				},
				readFileFunc: func(context.Context, string, string, string) ([]byte, error) {
					t.Fatal("unresolved ref must not enter content repair")
					return nil, nil
				},
			})
			result, err := admitCommittedCategory(t, srv, strings.Repeat("b", 40),
				categoryManifestTitled("electronics", "Electronics"), admission.OperationCreate)
			require.Nil(t, result)
			require.ErrorIs(t, err, failure)
			require.NotErrorIs(t, err, admission.ErrCommittedManifestSuperseded)
			_, err = store.GetCategoryTaxonomyByName(t.Context(), "gitstore", "electronics")
			require.ErrorIs(t, err, datastore.ErrNotFound)
		})
	}
}

func TestAdmitCommittedManifest_RepairsCurrentTipAfterWriteFailureButPreservesError(t *testing.T) {
	base := newTestDatastore(t)
	store := &failCategoryCreateOnceStore{Datastore: base}
	store.remaining.Store(1)
	commit := strings.Repeat("f", 40)
	current := commit
	content := categoryManifestTitled("electronics", "Electronics")
	srv := newCatalogServer(t, store, newTreeGitReader(&current, map[string]map[string][]byte{
		commit: {"categories/electronics.md": content},
	}))

	_, err := admitCommittedCategory(t, srv, commit, content, admission.OperationCreate)
	require.Error(t, err, "the original mutation outcome is retained")
	assert.Zero(t, store.remaining.Load(), "the bounded repair retried the failed write")
	stored, lookupErr := base.GetCategoryTaxonomyByName(context.Background(), "gitstore", "electronics")
	require.NoError(t, lookupErr)
	assert.Equal(t, commit, stored.GitCommitSHA)
}

func TestAdmitCommittedManifest_UnavailableRuntimeDoesNotAttemptRepair(t *testing.T) {
	store := newTestDatastore(t)
	srv := newCatalogServer(t, store, nil)
	_, err := admitCommittedCategory(t, srv, strings.Repeat("a", 40), categoryManifestTitled("electronics", "Electronics"), admission.OperationCreate)
	require.EqualError(t, err, "committed admission is unavailable")
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

func TestAdmitCommittedManifest_UnchangedCatalogPathSurvivesUnrelatedPeerCommits(t *testing.T) {
	for _, tc := range []struct {
		kind, name, path string
		initial, update  []byte
	}{
		{
			kind: "CategoryTaxonomy", name: "electronics", path: "categories/electronics.md",
			initial: categoryManifestTitled("electronics", "Electronics"),
			update:  categoryManifestTitled("electronics", "Updated"),
		},
		{
			kind: "Product", name: "phone", path: "products/phone.md",
			initial: makeProduct("phone"),
			update:  []byte(strings.Replace(string(makeProduct("phone")), "title: phone", "title: Updated", 1)),
		},
	} {
		t.Run(tc.kind, func(t *testing.T) {
			store := newTestDatastore(t)
			created, updated := strings.Repeat("a", 40), strings.Repeat("c", 40)
			firstTip, secondTip := strings.Repeat("b", 40), strings.Repeat("d", 40)
			current := firstTip
			files := map[string]map[string][]byte{
				created:   {tc.path: tc.initial},
				firstTip:  {tc.path: tc.initial, "unrelated.md": []byte("first peer commit")},
				updated:   {tc.path: tc.update},
				secondTip: {tc.path: tc.update, "unrelated.md": []byte("second peer commit")},
			}
			reader := newTreeGitReader(&current, files)
			proofs := 0
			reader.readCurrentUnchangedFileFunc = func(_ context.Context, repositoryID, path, refName, ancestor string) ([]byte, error) {
				proofs++
				require.Equal(t, testRepoID, repositoryID)
				require.Equal(t, tc.path, path)
				require.Equal(t, "refs/heads/main", refName)
				require.Contains(t, []string{created, updated}, ancestor)
				return files[current][path], nil
			}
			replicas := []*catalogTestServer{
				newCatalogServer(t, store, reader),
				newCatalogServer(t, store, reader),
			}
			for i, phase := range []struct {
				commit, tip string
				content     []byte
				operation   admission.Operation
			}{
				{commit: created, tip: firstTip, content: tc.initial, operation: admission.OperationCreate},
				{commit: updated, tip: secondTip, content: tc.update, operation: admission.OperationUpdate},
			} {
				current = phase.tip
				result, err := replicas[i].AdmitCommittedManifest(t.Context(), admission.CommittedManifestRequest{
					RepositoryID: testRepoID, Namespace: "gitstore", ActorSubject: "alice",
					CommitSHA: phase.commit, RefName: "refs/heads/main", Path: tc.path,
					Content: phase.content, Operation: phase.operation,
				})
				require.NoError(t, err)
				require.NotNil(t, result)
				require.False(t, result.NoOp)
				require.Equal(t, phase.commit, result.CommitSHA)
			}
			require.GreaterOrEqual(t, proofs, 4, "both replicas must prove the caller's path before and after admission")

			read := func() (string, string, int64) {
				if tc.kind == "CategoryTaxonomy" {
					record, err := store.GetCategoryTaxonomyByName(t.Context(), "gitstore", tc.name)
					require.NoError(t, err)
					require.Contains(t, string(record.Spec), "Updated")
					return record.GitCommitSHA, record.Revision, record.Generation
				}
				record, err := store.GetProductByName(t.Context(), "gitstore", tc.name)
				require.NoError(t, err)
				require.Contains(t, string(record.Spec), "Updated")
				return record.GitCommitSHA, record.Revision, record.Generation
			}
			commit, revision, generation := read()
			require.Equal(t, updated, commit)
			require.Equal(t, "main@sha1:"+updated, revision, "an unrelated tip must not replace authored provenance")
			require.Equal(t, int64(2), generation)

			files[secondTip][tc.path] = tc.initial
			result, err := replicas[0].AdmitCommittedManifest(t.Context(), admission.CommittedManifestRequest{
				RepositoryID: testRepoID, Namespace: "gitstore", ActorSubject: "alice",
				CommitSHA: updated, RefName: "refs/heads/main", Path: tc.path,
				Content: tc.update, Operation: admission.OperationUpdate,
			})
			require.Nil(t, result)
			require.ErrorIs(t, err, admission.ErrCommittedManifestSuperseded)
			afterCommit, afterRevision, afterGeneration := read()
			require.Equal(t, commit, afterCommit)
			require.Equal(t, revision, afterRevision)
			require.Equal(t, generation, afterGeneration)

			for _, failure := range []error{
				grpcstatus.Error(codes.Aborted, "path history changed"),
				grpcstatus.Error(codes.NotFound, "path removed"),
				grpcstatus.Error(codes.PermissionDenied, "history denied"),
				context.DeadlineExceeded,
				context.Canceled,
			} {
				t.Run(failure.Error(), func(t *testing.T) {
					reader.readCurrentUnchangedFileFunc = func(context.Context, string, string, string, string) ([]byte, error) {
						return nil, failure
					}
					result, err := replicas[1].AdmitCommittedManifest(t.Context(), admission.CommittedManifestRequest{
						RepositoryID: testRepoID, Namespace: "gitstore", ActorSubject: "alice",
						CommitSHA: updated, RefName: "refs/heads/main", Path: tc.path,
						Content: tc.update, Operation: admission.OperationUpdate,
					})
					require.Nil(t, result)
					if grpcstatus.Code(failure) == codes.Aborted || grpcstatus.Code(failure) == codes.NotFound {
						require.ErrorIs(t, err, admission.ErrCommittedManifestSuperseded)
					} else {
						require.ErrorIs(t, err, failure)
						require.NotErrorIs(t, err, admission.ErrCommittedManifestSuperseded)
					}
					afterCommit, afterRevision, afterGeneration := read()
					require.Equal(t, commit, afterCommit)
					require.Equal(t, revision, afterRevision)
					require.Equal(t, generation, afterGeneration)
				})
			}
		})
	}
}

func TestAdmitCommittedManifest_UnchangedCatalogPathsConvergeConcurrentlyAcrossReplicas(t *testing.T) {
	store := newTestDatastore(t)
	categoryCommit, productCommit := strings.Repeat("a", 40), strings.Repeat("b", 40)
	current := strings.Repeat("c", 40)
	categoryContent := categoryManifestTitled("electronics", "Electronics")
	productContent := makeProduct("phone")
	reader := newTreeGitReader(&current, map[string]map[string][]byte{
		current: {"categories/electronics.md": categoryContent, "products/phone.md": productContent},
	})
	var proofs atomic.Int32
	reader.readCurrentUnchangedFileFunc = func(_ context.Context, _, path, _, _ string) ([]byte, error) {
		proofs.Add(1)
		if path == "categories/electronics.md" {
			return categoryContent, nil
		}
		return productContent, nil
	}
	replicas := []*catalogTestServer{newCatalogServer(t, store, reader), newCatalogServer(t, store, reader)}
	requests := []admission.CommittedManifestRequest{
		{
			RepositoryID: testRepoID, Namespace: "gitstore", ActorSubject: "alice",
			CommitSHA: categoryCommit, RefName: "refs/heads/main", Path: "categories/electronics.md",
			Content: categoryContent, Operation: admission.OperationCreate,
		},
		{
			RepositoryID: testRepoID, Namespace: "gitstore", ActorSubject: "alice",
			CommitSHA: productCommit, RefName: "refs/heads/main", Path: "products/phone.md",
			Content: productContent, Operation: admission.OperationCreate,
		},
	}
	type reply struct {
		result *admission.CommittedManifestResult
		err    error
		commit string
	}
	start := make(chan struct{})
	replies := make(chan reply, len(requests))
	for i, request := range requests {
		go func() {
			<-start
			result, err := replicas[i].AdmitCommittedManifest(t.Context(), request)
			replies <- reply{result: result, err: err, commit: request.CommitSHA}
		}()
	}
	close(start)
	for range requests {
		response := <-replies
		require.NoError(t, response.err)
		require.NotNil(t, response.result)
		require.Equal(t, response.commit, response.result.CommitSHA)
		require.False(t, response.result.NoOp)
	}
	require.GreaterOrEqual(t, proofs.Load(), int32(4))
	category, err := store.GetCategoryTaxonomyByName(t.Context(), "gitstore", "electronics")
	require.NoError(t, err)
	require.Equal(t, categoryCommit, category.GitCommitSHA)
	product, err := store.GetProductByName(t.Context(), "gitstore", "phone")
	require.NoError(t, err)
	require.Equal(t, productCommit, product.GitCommitSHA)
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
