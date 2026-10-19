// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package cataloggrpc_test

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	catalogv1 "github.com/gitstore-dev/gitstore/api/gen/gitstore/catalog/v1"
	"github.com/gitstore-dev/gitstore/api/internal/admission"
	"github.com/gitstore-dev/gitstore/api/internal/cataloggrpc"
	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	apiruntime "github.com/gitstore-dev/gitstore/api/internal/runtime"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	grpcstatus "google.golang.org/grpc/status"
)

func TestCommittedNamespaceDisjointCommitsPreserveRequestAudit(t *testing.T) {
	for _, advanceAt := range []int{1, 2, 3, 4} {
		t.Run(fmt.Sprintf("advance-at-check-%d", advanceAt), func(t *testing.T) {
			ctx := context.Background()
			store := newNamespacePolicyDatastore(t)
			seed, mine, theirs := strings.Repeat("a", 40), strings.Repeat("b", 40), strings.Repeat("c", 40)
			path := "namespaces/committed-disjoint.md"
			content := namespaceManifest("committed-disjoint", "Updated", "ORGANIZATION")
			files := map[string]map[string][]byte{
				seed: {path: namespaceManifest("committed-disjoint", "Original", "ORGANIZATION")},
				mine: {path: content},
				theirs: {
					path:                      content,
					"namespaces/unrelated.md": namespaceManifest("unrelated", "Unrelated", "ORGANIZATION"),
				},
			}
			current := seed
			reader := &repositoryHistoryReader{mockGitReader: newTreeGitReader(&current, files)}
			reader.proof = func(_ context.Context, repository, requestedPath, tip, ancestor string) ([]byte, error) {
				assert.Equal(t, testRepoID, repository)
				assert.Equal(t, path, requestedPath)
				require.Equal(t, mine, ancestor)
				require.Equal(t, theirs, tip)
				require.True(t, bytes.Equal(files[ancestor][path], files[tip][path]))
				return files[tip][path], nil
			}
			srv := newCatalogServer(t, store, reader)
			request := admission.CommittedManifestRequest{
				RepositoryID: testRepoID, Namespace: "gitstore-system", ActorSubject: "alice",
				CommitSHA: seed, RefName: "refs/heads/main", Path: path,
				Content: files[seed][path], Operation: admission.OperationCreate,
			}
			_, err := srv.AdmitCommittedManifest(ctx, request)
			require.NoError(t, err)
			current = mine
			checks := 0
			reader.resolveRefFunc = func(context.Context, string, string) (string, error) {
				checks++
				if checks >= advanceAt {
					current = theirs
				}
				return current, nil
			}
			request.CommitSHA, request.Content, request.Operation = mine, content, admission.OperationUpdate
			result, err := srv.AdmitCommittedManifest(ctx, request)
			require.NoError(t, err)
			require.GreaterOrEqual(t, checks, advanceAt)
			require.Equal(t, mine, result.CommitSHA)
			stored, err := store.GetNamespaceByName(ctx, "committed-disjoint")
			require.NoError(t, err)
			assert.Equal(t, "Updated", stored.Title)
			assert.Equal(t, mine, stored.GitCommitSHA)
			assert.Equal(t, "main@sha1:"+mine, stored.Revision)
			assert.Equal(t, "alice", stored.UpdateActor)
			assert.Equal(t, int64(2), stored.Generation)
		})
	}
}

func TestCommittedNamespaceHistoryFailuresFailClosed(t *testing.T) {
	for _, tc := range []struct {
		name       string
		content    []byte
		proofError error
		superseded bool
	}{
		{name: "same path changed", proofError: grpcstatus.Error(codes.Aborted, "changed"), superseded: true},
		{name: "deleted and restored", proofError: grpcstatus.Error(codes.Aborted, "history changed"), superseded: true},
		{name: "path removed", proofError: grpcstatus.Error(codes.NotFound, "missing"), superseded: true},
		{name: "content mismatch", content: []byte("different"), superseded: true},
		{name: "Git unavailable", proofError: grpcstatus.Error(codes.Unavailable, "unavailable")},
		{name: "history budget", proofError: grpcstatus.Error(codes.ResourceExhausted, "budget exhausted")},
		{name: "corrupt object", proofError: grpcstatus.Error(codes.Internal, "corrupt object")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := newNamespacePolicyDatastore(t)
			mine, theirs := strings.Repeat("a", 40), strings.Repeat("b", 40)
			current := theirs
			reader := &repositoryHistoryReader{
				mockGitReader: newTreeGitReader(&current, nil),
				proof: func(context.Context, string, string, string, string) ([]byte, error) {
					return tc.content, tc.proofError
				},
			}
			srv := newCatalogServer(t, store, reader)
			_, err := srv.AdmitCommittedManifest(context.Background(), admission.CommittedManifestRequest{
				RepositoryID: testRepoID, Namespace: "gitstore-system", ActorSubject: "alice",
				CommitSHA: mine, RefName: "refs/heads/main", Path: "namespaces/history-closed.md",
				Content:   namespaceManifest("history-closed", "Closed", "ORGANIZATION"),
				Operation: admission.OperationCreate,
			})
			require.Error(t, err)
			if tc.superseded {
				require.ErrorIs(t, err, admission.ErrCommittedManifestSuperseded)
			} else {
				require.NotErrorIs(t, err, admission.ErrCommittedManifestSuperseded)
			}
			_, err = store.GetNamespaceByName(context.Background(), "history-closed")
			require.ErrorIs(t, err, datastore.ErrNotFound)
		})
	}
}

func TestRawNamespaceDisjointProvenanceRetainsAcceptedCommit(t *testing.T) {
	for _, tc := range []struct {
		name           string
		proofError     error
		differentBytes bool
		withoutHistory bool
		continuousTip  bool
		advanceAt      int
		superseded     bool
		expectedCode   codes.Code
	}{
		{name: "unchanged path"},
		{name: "advance at first ref check", advanceAt: 1},
		{name: "advance at second ref check", advanceAt: 2},
		{name: "advance at third ref check", advanceAt: 3},
		{name: "advance at fourth ref check", advanceAt: 4},
		{name: "continuously advancing disjoint tip", continuousTip: true},
		{name: "continuous tip without history reader", continuousTip: true, withoutHistory: true, superseded: true},
		{name: "continuous tip with later same-path rewrite", continuousTip: true, differentBytes: true, superseded: true},
		{name: "continuous tip with unavailable history", continuousTip: true, proofError: grpcstatus.Error(codes.Unavailable, "Git unavailable"), expectedCode: codes.Unavailable},
		{name: "same-path edit", proofError: grpcstatus.Error(codes.Aborted, "path was edited"), superseded: true},
		{name: "deleted and restored", proofError: grpcstatus.Error(codes.Aborted, "path was deleted"), superseded: true},
		{name: "missing path", proofError: grpcstatus.Error(codes.NotFound, "path absent"), superseded: true},
		{name: "proof bytes differ from accepted source", differentBytes: true, superseded: true},
		{name: "missing history reader", withoutHistory: true, superseded: true},
		{name: "Git unavailable", proofError: grpcstatus.Error(codes.Unavailable, "Git unavailable"), expectedCode: codes.Unavailable},
		{name: "history budget exhausted", proofError: grpcstatus.Error(codes.ResourceExhausted, "history budget"), expectedCode: codes.ResourceExhausted},
		{name: "history corrupt", proofError: grpcstatus.Error(codes.Internal, "corrupt history"), expectedCode: codes.Internal},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			store := newNamespacePolicyDatastore(t)
			seed, mine, theirs := strings.Repeat("a", 40), strings.Repeat("b", 40), strings.Repeat("c", 40)
			path := "namespaces/raw-provenance.md"
			original := namespaceManifest("raw-provenance", "Probe", "ORGANIZATION")
			rewrite := bytes.Replace(original, []byte("---\n"), []byte("---\n# provenance-only rewrite\n"), 1)
			files := map[string]map[string][]byte{
				seed: {path: original},
				mine: {path: rewrite},
				theirs: {
					path:                      rewrite,
					"namespaces/unrelated.md": namespaceManifest("unrelated", "Other", "ORGANIZATION"),
				},
			}
			if tc.differentBytes {
				files[theirs][path] = bytes.Replace(rewrite, []byte("# provenance-only rewrite"), []byte("# later same-path rewrite"), 1)
			}
			current := seed
			reader := &repositoryHistoryReader{mockGitReader: newTreeGitReader(&current, files)}
			reader.proof = func(_ context.Context, repository, requestedPath, tip, ancestor string) ([]byte, error) {
				require.Equal(t, testRepoID, repository)
				require.Equal(t, path, requestedPath)
				require.Equal(t, mine, ancestor)
				if !tc.continuousTip {
					require.Equal(t, theirs, tip)
				}
				return files[tip][path], tc.proofError
			}
			var gitReader cataloggrpc.GitReader = reader
			if tc.withoutHistory {
				gitReader = struct{ cataloggrpc.GitReader }{reader}
			}
			srv := newCatalogServer(t, store, gitReader)
			request := &catalogv1.AdmitResourcesRequest{
				RepositoryId: testRepoID, OldCommitSha: strings.Repeat("0", 40), NewCommitSha: seed,
				RefName: "refs/heads/main", ChangedPaths: []string{path},
				Authorization: &catalogv1.CallbackAuthorization{Actor: &catalogv1.CallbackActor{
					Subject: "alice", AuthMethod: "static-users",
				}},
			}
			_, err := srv.AdmitResources(ctx, request)
			require.NoError(t, err)
			before, err := store.GetNamespaceByName(ctx, "raw-provenance")
			require.NoError(t, err)
			current = theirs
			checks := 0
			if tc.advanceAt != 0 || tc.continuousTip {
				current = mine
				reader.resolveRefFunc = func(context.Context, string, string) (string, error) {
					checks++
					if tc.continuousTip {
						current = fmt.Sprintf("%040x", checks)
						files[current] = files[theirs]
					} else if checks >= tc.advanceAt {
						current = theirs
					}
					return current, nil
				}
			}
			if tc.continuousTip {
				srv = newCatalogServer(t, store, gitReader)
			}
			request.OldCommitSha, request.NewCommitSha = seed, mine
			request.Authorization.Actor.Subject = "bob"
			_, err = srv.AdmitResources(ctx, request)
			require.Equal(t, tc.expectedCode, grpcstatus.Code(err))
			require.GreaterOrEqual(t, checks, tc.advanceAt)
			if tc.continuousTip {
				require.Greater(t, checks, 1)
			}
			stored, readErr := store.GetNamespaceByName(ctx, "raw-provenance")
			require.NoError(t, readErr)
			if tc.superseded || tc.expectedCode != codes.OK {
				assert.Equal(t, before, stored)
				return
			}
			assert.Equal(t, mine, stored.GitCommitSHA)
			assert.Equal(t, "main@sha1:"+mine, stored.Revision)
			assert.Equal(t, before.Generation, stored.Generation)
			assert.NotEqual(t, before.ResourceVersion, stored.ResourceVersion)
			assert.Equal(t, "alice", stored.CreationActor)
			assert.Equal(t, "bob", stored.UpdateActor)
		})
	}
}

func TestNamespaceDescendantAdmissionConvergesDisjointResources(t *testing.T) {
	store := newNamespacePolicyDatastore(t)
	zero := strings.Repeat("0", 40)
	older := strings.Repeat("1", 40)
	newer := strings.Repeat("2", 40)
	xPath := "namespaces/descendant-x.md"
	yPath := "namespaces/descendant-y.md"
	current := newer
	reader := newTreeGitReader(&current, map[string]map[string][]byte{
		zero:  {},
		older: {xPath: namespaceManifest("descendant-x", "X", "ORGANIZATION")},
		newer: {
			xPath: namespaceManifest("descendant-x", "X", "ORGANIZATION"),
			yPath: namespaceManifest("descendant-y", "Y", "ORGANIZATION"),
		},
	})
	srv := newCatalogServer(t, store, &repositoryHistoryReader{
		mockGitReader: reader,
		proof: func(ctx context.Context, repository, path, tip, ancestor string) ([]byte, error) {
			require.Equal(t, older, ancestor)
			require.Equal(t, newer, tip)
			return reader.ReadFile(ctx, repository, path, tip)
		},
	})

	_, err := srv.AdmitResources(context.Background(), &catalogv1.AdmitResourcesRequest{
		RepositoryId: testRepoID,
		OldCommitSha: zero,
		NewCommitSha: older,

		RefName:       "refs/heads/main",
		ChangedPaths:  []string{xPath},
		Authorization: &catalogv1.CallbackAuthorization{Actor: &catalogv1.CallbackActor{Subject: "alice", AuthMethod: "static-users"}},
	})
	require.NoError(t, err)
	_, err = srv.AdmitResources(context.Background(), &catalogv1.AdmitResourcesRequest{
		RepositoryId: testRepoID,
		OldCommitSha: older,
		NewCommitSha: newer,

		RefName:       "refs/heads/main",
		ChangedPaths:  []string{yPath},
		Authorization: &catalogv1.CallbackAuthorization{Actor: &catalogv1.CallbackActor{Subject: "bob", AuthMethod: "static-users"}},
	})
	require.NoError(t, err)

	x, err := store.GetNamespaceByName(context.Background(), "descendant-x")
	require.NoError(t, err)
	y, err := store.GetNamespaceByName(context.Background(), "descendant-y")
	require.NoError(t, err)
	assert.Equal(t, older, x.GitCommitSHA)
	assert.Equal(t, newer, y.GitCommitSHA)
}

func TestNamespaceDescendantAdmissionSameResourceUsesNewestContent(t *testing.T) {
	store := newNamespacePolicyDatastore(t)
	zero := strings.Repeat("0", 40)
	older := strings.Repeat("3", 40)
	newer := strings.Repeat("4", 40)
	path := "namespaces/descendant-same.md"
	current := newer
	srv := newCatalogServer(t, store, newTreeGitReader(&current, map[string]map[string][]byte{
		zero:  {},
		older: {path: namespaceManifest("descendant-same", "Older", "ORGANIZATION")},
		newer: {path: namespaceManifest("descendant-same", "Newer", "ORGANIZATION")},
	}))

	_, err := srv.AdmitResources(context.Background(), &catalogv1.AdmitResourcesRequest{
		RepositoryId: testRepoID,
		OldCommitSha: zero,
		NewCommitSha: newer,

		RefName:       "refs/heads/main",
		ChangedPaths:  []string{path},
		Authorization: &catalogv1.CallbackAuthorization{Actor: &catalogv1.CallbackActor{Subject: "bob", AuthMethod: "static-users"}},
	})
	require.NoError(t, err)

	namespace, err := store.GetNamespaceByName(context.Background(), "descendant-same")
	require.NoError(t, err)
	assert.Equal(t, "Newer", namespace.Title)
	assert.Equal(t, newer, namespace.GitCommitSHA)
	assert.Equal(t, "main@sha1:"+newer, namespace.Revision)
}

func TestNamespaceDescendantSameResourceLeavesAuditToExactHeadHandler(t *testing.T) {
	store := newNamespacePolicyDatastore(t)
	zero := strings.Repeat("0", 40)
	initial := strings.Repeat("5", 40)
	older := strings.Repeat("6", 40)
	newer := strings.Repeat("7", 40)
	path := "namespaces/descendant-audit-same.md"
	current := initial
	files := map[string]map[string][]byte{
		initial: {path: namespaceManifest("descendant-audit-same", "Initial", "ORGANIZATION")},
		older:   {path: namespaceManifest("descendant-audit-same", "Older", "ORGANIZATION")},
		newer:   {path: namespaceManifest("descendant-audit-same", "Newer", "ORGANIZATION")},
	}
	seedTime := time.Date(2026, 8, 29, 9, 0, 0, 0, time.UTC)
	staleTime := seedTime.Add(time.Hour)
	exactTime := staleTime.Add(time.Hour)

	seed := newCatalogServer(t, store, newTreeGitReader(&current, files), func(deps *cataloggrpc.ServerDeps) {
		deps.Clock = apiruntime.NewFixedClock(seedTime)
	})
	_, err := seed.AdmitResources(context.Background(), &catalogv1.AdmitResourcesRequest{
		RepositoryId: testRepoID,
		OldCommitSha: zero,
		NewCommitSha: initial,

		RefName:       "refs/heads/main",
		ChangedPaths:  []string{path},
		Authorization: &catalogv1.CallbackAuthorization{Actor: &catalogv1.CallbackActor{Subject: "seed", AuthMethod: "static-users"}},
	})
	require.NoError(t, err)

	current = newer
	stale := newCatalogServer(t, store, newTreeGitReader(&current, files), func(deps *cataloggrpc.ServerDeps) {
		deps.Clock = apiruntime.NewFixedClock(staleTime)
	})
	_, err = stale.AdmitResources(context.Background(), &catalogv1.AdmitResourcesRequest{
		RepositoryId: testRepoID,
		OldCommitSha: initial,
		NewCommitSha: older,

		RefName:       "refs/heads/main",
		ChangedPaths:  []string{path},
		Authorization: &catalogv1.CallbackAuthorization{Actor: &catalogv1.CallbackActor{Subject: "alice", AuthMethod: "static-users"}},
	})
	require.NoError(t, err)

	beforeExact, err := store.GetNamespaceByName(context.Background(), "descendant-audit-same")
	require.NoError(t, err)
	assert.Equal(t, "Initial", beforeExact.Title)
	assert.Equal(t, "seed", beforeExact.UpdateActor)
	assert.Equal(t, seedTime, beforeExact.UpdateTimestamp)

	exact := newCatalogServer(t, store, newTreeGitReader(&current, files), func(deps *cataloggrpc.ServerDeps) {
		deps.Clock = apiruntime.NewFixedClock(exactTime)
	})
	_, err = exact.AdmitResources(context.Background(), &catalogv1.AdmitResourcesRequest{
		RepositoryId: testRepoID,
		OldCommitSha: older,
		NewCommitSha: newer,

		RefName:       "refs/heads/main",
		ChangedPaths:  []string{path},
		Authorization: &catalogv1.CallbackAuthorization{Actor: &catalogv1.CallbackActor{Subject: "bob", AuthMethod: "static-users"}},
	})
	require.NoError(t, err)

	namespace, err := store.GetNamespaceByName(context.Background(), "descendant-audit-same")
	require.NoError(t, err)
	assert.Equal(t, "Newer", namespace.Title)
	assert.Equal(t, "bob", namespace.UpdateActor)
	assert.Equal(t, exactTime, namespace.UpdateTimestamp)
	assert.Equal(t, newer, namespace.GitCommitSHA)
}

func TestNamespaceDescendantDisjointCommitKeepsRequestAudit(t *testing.T) {
	store := newNamespacePolicyDatastore(t)
	zero := strings.Repeat("0", 40)
	older := strings.Repeat("8", 40)
	newer := strings.Repeat("9", 40)
	xPath := "namespaces/descendant-audit-x.md"
	yPath := "namespaces/descendant-audit-y.md"
	current := newer
	files := map[string]map[string][]byte{
		older: {xPath: namespaceManifest("descendant-audit-x", "X", "ORGANIZATION")},
		newer: {
			xPath: namespaceManifest("descendant-audit-x", "X", "ORGANIZATION"),
			yPath: namespaceManifest("descendant-audit-y", "Y", "ORGANIZATION"),
		},
	}
	staleTime := time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)
	exactTime := staleTime.Add(time.Hour)

	reader := newTreeGitReader(&current, files)
	stale := newCatalogServer(t, store, &repositoryHistoryReader{
		mockGitReader: reader,
		proof: func(ctx context.Context, repository, path, tip, ancestor string) ([]byte, error) {
			require.Equal(t, older, ancestor)
			require.Equal(t, newer, tip)
			return reader.ReadFile(ctx, repository, path, tip)
		},
	}, func(deps *cataloggrpc.ServerDeps) {
		deps.Clock = apiruntime.NewFixedClock(staleTime)
	})
	_, err := stale.AdmitResources(context.Background(), &catalogv1.AdmitResourcesRequest{
		RepositoryId: testRepoID,
		OldCommitSha: zero,
		NewCommitSha: older,

		RefName:       "refs/heads/main",
		ChangedPaths:  []string{xPath},
		Authorization: &catalogv1.CallbackAuthorization{Actor: &catalogv1.CallbackActor{Subject: "alice", AuthMethod: "static-users"}},
	})
	require.NoError(t, err)

	x, err := store.GetNamespaceByName(context.Background(), "descendant-audit-x")
	require.NoError(t, err)
	assert.Equal(t, "alice", x.CreationActor)
	assert.Equal(t, "alice", x.UpdateActor)
	assert.Equal(t, staleTime, x.CreationTimestamp)
	assert.Equal(t, staleTime, x.UpdateTimestamp)
	assert.Equal(t, older, x.GitCommitSHA)

	exact := newCatalogServer(t, store, newTreeGitReader(&current, files), func(deps *cataloggrpc.ServerDeps) {
		deps.Clock = apiruntime.NewFixedClock(exactTime)
	})
	_, err = exact.AdmitResources(context.Background(), &catalogv1.AdmitResourcesRequest{
		RepositoryId: testRepoID,
		OldCommitSha: older,
		NewCommitSha: newer,

		RefName:       "refs/heads/main",
		ChangedPaths:  []string{yPath},
		Authorization: &catalogv1.CallbackAuthorization{Actor: &catalogv1.CallbackActor{Subject: "bob", AuthMethod: "static-users"}},
	})
	require.NoError(t, err)

	x, err = store.GetNamespaceByName(context.Background(), "descendant-audit-x")
	require.NoError(t, err)
	assert.Equal(t, "alice", x.UpdateActor)
	assert.Equal(t, staleTime, x.UpdateTimestamp)
	y, err := store.GetNamespaceByName(context.Background(), "descendant-audit-y")
	require.NoError(t, err)
	assert.Equal(t, "bob", y.CreationActor)
	assert.Equal(t, exactTime, y.CreationTimestamp)
}
