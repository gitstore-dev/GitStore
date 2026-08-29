// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package cataloggrpc_test

import (
	"context"
	"strings"
	"testing"
	"time"

	catalogv1 "github.com/gitstore-dev/gitstore/api/gen/gitstore/catalog/v1"
	"github.com/gitstore-dev/gitstore/api/internal/cataloggrpc"
	apiruntime "github.com/gitstore-dev/gitstore/api/internal/runtime"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNamespaceDescendantAdmissionConvergesDisjointResources(t *testing.T) {
	store := newNamespacePolicyDatastore(t)
	zero := strings.Repeat("0", 40)
	older := strings.Repeat("1", 40)
	newer := strings.Repeat("2", 40)
	xPath := "namespaces/descendant-x.md"
	yPath := "namespaces/descendant-y.md"
	current := newer
	srv := newCatalogServer(t, store, newTreeGitReader(&current, map[string]map[string][]byte{
		zero:  {},
		older: {xPath: namespaceManifest("descendant-x", "X", "USER")},
		newer: {
			xPath: namespaceManifest("descendant-x", "X", "USER"),
			yPath: namespaceManifest("descendant-y", "Y", "USER"),
		},
	}))

	_, err := srv.AdmitResources(context.Background(), &catalogv1.AdmitResourcesRequest{
		RepositoryId: testRepoID,
		OldCommitSha: zero,
		NewCommitSha: older,
		CommitSha:    older,
		RefName:      "refs/heads/main",
		ChangedPaths: []string{xPath},
		ActorSubject: "alice",
	})
	require.NoError(t, err)
	_, err = srv.AdmitResources(context.Background(), &catalogv1.AdmitResourcesRequest{
		RepositoryId: testRepoID,
		OldCommitSha: older,
		NewCommitSha: newer,
		CommitSha:    newer,
		RefName:      "refs/heads/main",
		ChangedPaths: []string{yPath},
		ActorSubject: "bob",
	})
	require.NoError(t, err)

	x, err := store.GetNamespaceByName(context.Background(), "descendant-x")
	require.NoError(t, err)
	y, err := store.GetNamespaceByName(context.Background(), "descendant-y")
	require.NoError(t, err)
	assert.Equal(t, newer, x.GitCommitSHA)
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
		older: {path: namespaceManifest("descendant-same", "Older", "USER")},
		newer: {path: namespaceManifest("descendant-same", "Newer", "ORGANIZATION")},
	}))

	_, err := srv.AdmitResources(context.Background(), &catalogv1.AdmitResourcesRequest{
		RepositoryId: testRepoID,
		OldCommitSha: zero,
		NewCommitSha: newer,
		CommitSha:    newer,
		RefName:      "refs/heads/main",
		ChangedPaths: []string{path},
		ActorSubject: "bob",
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
		initial: {path: namespaceManifest("descendant-audit-same", "Initial", "USER")},
		older:   {path: namespaceManifest("descendant-audit-same", "Older", "USER")},
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
		CommitSha:    initial,
		RefName:      "refs/heads/main",
		ChangedPaths: []string{path},
		ActorSubject: "seed",
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
		CommitSha:    older,
		RefName:      "refs/heads/main",
		ChangedPaths: []string{path},
		ActorSubject: "alice",
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
		CommitSha:    newer,
		RefName:      "refs/heads/main",
		ChangedPaths: []string{path},
		ActorSubject: "bob",
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
		older: {xPath: namespaceManifest("descendant-audit-x", "X", "USER")},
		newer: {
			xPath: namespaceManifest("descendant-audit-x", "X", "USER"),
			yPath: namespaceManifest("descendant-audit-y", "Y", "USER"),
		},
	}
	staleTime := time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)
	exactTime := staleTime.Add(time.Hour)

	stale := newCatalogServer(t, store, newTreeGitReader(&current, files), func(deps *cataloggrpc.ServerDeps) {
		deps.Clock = apiruntime.NewFixedClock(staleTime)
	})
	_, err := stale.AdmitResources(context.Background(), &catalogv1.AdmitResourcesRequest{
		RepositoryId: testRepoID,
		OldCommitSha: zero,
		NewCommitSha: older,
		CommitSha:    older,
		RefName:      "refs/heads/main",
		ChangedPaths: []string{xPath},
		ActorSubject: "alice",
	})
	require.NoError(t, err)

	x, err := store.GetNamespaceByName(context.Background(), "descendant-audit-x")
	require.NoError(t, err)
	assert.Equal(t, "alice", x.CreationActor)
	assert.Equal(t, "alice", x.UpdateActor)
	assert.Equal(t, staleTime, x.CreationTimestamp)
	assert.Equal(t, staleTime, x.UpdateTimestamp)
	assert.Equal(t, newer, x.GitCommitSHA)

	exact := newCatalogServer(t, store, newTreeGitReader(&current, files), func(deps *cataloggrpc.ServerDeps) {
		deps.Clock = apiruntime.NewFixedClock(exactTime)
	})
	_, err = exact.AdmitResources(context.Background(), &catalogv1.AdmitResourcesRequest{
		RepositoryId: testRepoID,
		OldCommitSha: older,
		NewCommitSha: newer,
		CommitSha:    newer,
		RefName:      "refs/heads/main",
		ChangedPaths: []string{yPath},
		ActorSubject: "bob",
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
