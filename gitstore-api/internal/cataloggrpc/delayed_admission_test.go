// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package cataloggrpc_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	catalogv1 "github.com/gitstore-dev/gitstore/api/gen/gitstore/catalog/v1"
	"github.com/gitstore-dev/gitstore/api/internal/admission"
	"github.com/gitstore-dev/gitstore/api/internal/auth"
	"github.com/gitstore-dev/gitstore/api/internal/catalog"
	"github.com/gitstore-dev/gitstore/api/internal/cataloggrpc"
	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	"github.com/gitstore-dev/gitstore/api/internal/testutil"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	grpcstatus "google.golang.org/grpc/status"
)

func TestAdmitResourcesDelayedUnchangedManifestSurvivesNewerCommit(t *testing.T) {
	for _, tc := range []struct {
		kind, directory string
		manifest        func(string) []byte
		lookup          func(context.Context, datastore.Datastore, string) error
	}{
		{"Product", "products", makeProduct, func(ctx context.Context, store datastore.Datastore, name string) error {
			_, err := store.GetProductByName(ctx, "gitstore", name)
			return err
		}},
		{"CategoryTaxonomy", "categories", makeCategoryTaxonomy, func(ctx context.Context, store datastore.Datastore, name string) error {
			_, err := store.GetCategoryTaxonomyByName(ctx, "gitstore", name)
			return err
		}},
	} {
		t.Run(tc.kind, func(t *testing.T) {
			store := newTestDatastore(t)
			zero, first, second := strings.Repeat("0", 40), strings.Repeat("a", 40), strings.Repeat("b", 40)
			earlierPath, laterPath := tc.directory+"/earlier.md", tc.directory+"/later.md"
			earlier, later := tc.manifest("earlier"), tc.manifest("later")
			current := second
			git := newTreeGitReader(&current, map[string]map[string][]byte{
				first:  {earlierPath: earlier},
				second: {earlierPath: earlier, laterPath: later},
			})
			newerServer, delayedServer := newCatalogServer(t, store, git), newCatalogServer(t, store, git)
			// The later callback only owns its delta. It cannot materialize the earlier
			// resource on behalf of a different callback actor.
			_, err := newerServer.AdmitResources(t.Context(), &catalogv1.AdmitResourcesRequest{
				RepositoryId: testRepoID, RefName: "refs/heads/main", OldCommitSha: first,
				NewCommitSha: second, ChangedPaths: []string{laterPath},
			})
			require.NoError(t, err)
			require.NoError(t, tc.lookup(t.Context(), store, "later"))
			// The first push was acknowledged, and its unchanged manifest remains in
			// the current tree. A delayed callback must not silently lose admission.
			_, err = delayedServer.AdmitResources(t.Context(), &catalogv1.AdmitResourcesRequest{
				RepositoryId: testRepoID, RefName: "refs/heads/main", OldCommitSha: zero,
				NewCommitSha: first, ChangedPaths: []string{earlierPath},
			})
			require.NoError(t, err)
			require.NoError(t, tc.lookup(t.Context(), store, "earlier"), "acknowledged unchanged manifest was lost after the branch advanced")
			switch tc.kind {
			case "Product":
				stored, err := store.GetProductByName(t.Context(), "gitstore", "earlier")
				require.NoError(t, err)
				require.Equal(t, first, stored.GitCommitSHA)
				require.NotEmpty(t, stored.UID)
			case "CategoryTaxonomy":
				stored, err := store.GetCategoryTaxonomyByName(t.Context(), "gitstore", "earlier")
				require.NoError(t, err)
				require.Equal(t, first, stored.GitCommitSHA)
				require.NotEmpty(t, stored.UID)
			}

		})
	}
}

func TestAdmitResourcesChangedPathsDoesNotEnumerateTree(t *testing.T) {
	store := newTestDatastore(t)
	commit := strings.Repeat("a", 40)
	lists := 0
	git := &mockGitReader{
		listFilesFunc: func(context.Context, string, string, string) ([]string, error) {
			lists++
			return nil, errors.New("delta admission must not enumerate the repository")
		},
		readFileFunc: func(context.Context, string, string, string) ([]byte, error) {
			return makeCategoryTaxonomy("delta"), nil
		},
		resolveRefFunc: func(context.Context, string, string) (string, error) { return commit, nil },
	}
	server := newCatalogServer(t, store, git)
	_, err := server.AdmitResources(t.Context(), &catalogv1.AdmitResourcesRequest{
		RepositoryId: testRepoID, RefName: "refs/heads/main", NewCommitSha: commit,
		ChangedPaths: []string{"categories/delta.md"},
	})
	require.NoError(t, err)
	require.Zero(t, lists, "a one-path admission must do bounded direct reads")
	_, err = store.GetCategoryTaxonomyByName(t.Context(), "gitstore", "delta")
	require.NoError(t, err)
}

func TestAdmitResourcesReadFailureCannotBecomeDeletion(t *testing.T) {
	store := newTestDatastore(t)
	first, second := strings.Repeat("a", 40), strings.Repeat("b", 40)
	path := "categories/preserve.md"
	content := makeCategoryTaxonomy("preserve")
	current := first
	git := newTreeGitReader(&current, map[string]map[string][]byte{
		first: {path: content}, second: {path: content},
	})
	server := newCatalogServer(t, store, git)
	admitDelta(t, server, strings.Repeat("0", 40), first)
	before, err := store.GetCategoryTaxonomyByName(t.Context(), "gitstore", "preserve")
	require.NoError(t, err)
	read := git.readFileFunc
	git.readFileFunc = func(ctx context.Context, repo, path, ref string) ([]byte, error) {
		if ref == second {
			return nil, grpcstatus.Error(codes.Unavailable, "temporary Git read failure")
		}
		return read(ctx, repo, path, ref)
	}
	current = second
	_, err = server.AdmitResources(t.Context(), &catalogv1.AdmitResourcesRequest{
		RepositoryId: testRepoID, RefName: "refs/heads/main", OldCommitSha: first,
		NewCommitSha: second, ChangedPaths: []string{path},
	})
	require.Error(t, err, "unreadable state is not proof that the manifest was removed")
	after, err := store.GetCategoryTaxonomyByName(t.Context(), "gitstore", "preserve")
	require.NoError(t, err)
	require.Equal(t, before.UID, after.UID)
	require.Nil(t, after.DeletionTimestamp)
}

func TestAdmitResourcesDelayedCreateCannotOverwriteOrResurrect(t *testing.T) {
	for _, mode := range []string{"modified", "removed", "already-admitted"} {
		t.Run(mode, func(t *testing.T) {
			store := newTestDatastore(t)
			first, second := strings.Repeat("a", 40), strings.Repeat("b", 40)
			path := "categories/guarded.md"
			original := makeCategoryTaxonomy("guarded")
			latest := original
			if mode == "modified" {
				latest = append(append([]byte{}, original...), []byte("newer body")...)
			}
			trees := map[string]map[string][]byte{first: {path: original}, second: {path: latest}}
			if mode == "removed" {
				trees[second] = map[string][]byte{}
			}
			current := second
			git := newTreeGitReader(&current, trees)
			server := newCatalogServer(t, store, git)
			if mode != "removed" {
				seed := second
				if mode == "already-admitted" {
					seed = first
				}
				current = seed
				admitDelta(t, server, strings.Repeat("0", 40), seed)
				current = second
			}
			var uid, version, body, source string
			if mode != "removed" {
				before, err := store.GetCategoryTaxonomyByName(t.Context(), "gitstore", "guarded")
				require.NoError(t, err)
				uid, version, body, source = before.UID, before.ResourceVersion, before.Body, before.GitCommitSHA
			}
			_, err := server.AdmitResources(t.Context(), &catalogv1.AdmitResourcesRequest{
				RepositoryId: testRepoID, RefName: "refs/heads/main", OldCommitSha: strings.Repeat("0", 40),
				NewCommitSha: first, ChangedPaths: []string{path},
			})
			require.NoError(t, err)
			after, err := store.GetCategoryTaxonomyByName(t.Context(), "gitstore", "guarded")
			if mode == "removed" {
				require.ErrorIs(t, err, datastore.ErrNotFound)
				return
			}
			require.NoError(t, err)
			require.Equal(t, uid, after.UID)
			require.Equal(t, version, after.ResourceVersion)
			require.Equal(t, body, after.Body)
			require.Equal(t, source, after.GitCommitSHA)
		})
	}
}

func TestAdmitResourcesDelayedManifestRechecksAuthorization(t *testing.T) {
	store := newTestDatastore(t)
	first, second := strings.Repeat("a", 40), strings.Repeat("b", 40)
	path := "categories/protected.md"
	content := makeCategoryTaxonomy("protected")
	current := second
	reader := newTreeGitReader(&current, map[string]map[string][]byte{
		first: {path: content}, second: {path: content},
	})
	deny := testutil.NewDenyAllAuthZ(t)
	server := newCatalogServer(t, store, reader, func(deps *cataloggrpc.ServerDeps) {
		deps.Registry = auth.NewProviderRegistry(nil, deny, nil)
	})
	_, err := server.AdmitResources(t.Context(), &catalogv1.AdmitResourcesRequest{
		RepositoryId: testRepoID, RefName: "refs/heads/main", OldCommitSha: strings.Repeat("0", 40),
		NewCommitSha: first, ChangedPaths: []string{path},
	})
	require.Equal(t, codes.PermissionDenied, grpcstatus.Code(err))
	_, err = store.GetCategoryTaxonomyByName(t.Context(), "gitstore", "protected")
	require.ErrorIs(t, err, datastore.ErrNotFound)
}

type advanceTipDenyPolicy struct{ advance func() }

func (advanceTipDenyPolicy) Name() string { return "advance-tip-before-denial" }
func (p advanceTipDenyPolicy) Validate(context.Context, admission.AdmissionRequest) admission.AdmissionDecision {
	p.advance()
	return admission.DecisionDeny("old snapshot denied", "spec")
}

func TestAdmitResourcesSupersededDenialCannotChangeNewerStatus(t *testing.T) {
	for _, tc := range []struct {
		kind, directory string
		manifest        func(string) []byte
	}{
		{"Product", "products", makeProduct}, {"CategoryTaxonomy", "categories", makeCategoryTaxonomy},
	} {
		t.Run(tc.kind, func(t *testing.T) {
			store := newTestDatastore(t)
			initial, first, second := strings.Repeat("c", 40), strings.Repeat("a", 40), strings.Repeat("b", 40)
			path := tc.directory + "/guarded.md"
			base := tc.manifest("guarded")
			current := initial
			reader := newTreeGitReader(&current, map[string]map[string][]byte{
				initial: {path: base},
				first:   {path: append(append([]byte{}, base...), []byte("older body")...)},
				second:  {path: append(append([]byte{}, base...), []byte("newer body")...)},
			})
			newer := newCatalogServer(t, store, reader)
			admitDelta(t, newer, strings.Repeat("0", 40), initial)
			older := newCatalogServer(t, store, reader, func(deps *cataloggrpc.ServerDeps) {
				deps.ExtraValidatingPolicies = []admission.ValidatingAdmissionPolicy{advanceTipDenyPolicy{advance: func() {
					current = second
					_, err := newer.AdmitResources(t.Context(), &catalogv1.AdmitResourcesRequest{
						RepositoryId: testRepoID, RefName: "refs/heads/main", OldCommitSha: first, NewCommitSha: second, ChangedPaths: []string{path},
					})
					require.NoError(t, err)
				}}}
			})
			current = first
			_, err := older.AdmitResources(t.Context(), &catalogv1.AdmitResourcesRequest{
				RepositoryId: testRepoID, RefName: "refs/heads/main", OldCommitSha: initial, NewCommitSha: first, ChangedPaths: []string{path},
			})
			require.NoError(t, err)
			var source string
			var raw []byte
			if tc.kind == "Product" {
				resource, err := store.GetProductByName(t.Context(), "gitstore", "guarded")
				require.NoError(t, err)
				source, raw = resource.GitCommitSHA, resource.Status
			} else {
				resource, err := store.GetCategoryTaxonomyByName(t.Context(), "gitstore", "guarded")
				require.NoError(t, err)
				source, raw = resource.GitCommitSHA, resource.Status
			}
			require.Equal(t, second, source)
			var status struct{ Conditions []catalog.Condition }
			require.NoError(t, json.Unmarshal(raw, &status))
			found := false
			for _, condition := range status.Conditions {
				if condition.Type == catalog.ConditionAdmissionAccepted {
					found = true
					require.Equal(t, catalog.ConditionTrue, condition.Status, "old denied snapshot overwrote accepted newer status")
				}
			}
			require.True(t, found)
		})
	}
}
