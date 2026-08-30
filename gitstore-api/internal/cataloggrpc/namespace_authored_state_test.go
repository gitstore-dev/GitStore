// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package cataloggrpc_test

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	catalogv1 "github.com/gitstore-dev/gitstore/api/gen/gitstore/catalog/v1"
	"github.com/gitstore-dev/gitstore/api/internal/catalog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAdmitResourcesNamespacePersistsCompleteAuthoredState(t *testing.T) {
	store := newNamespacePolicyDatastore(t)
	zero := strings.Repeat("0", 40)
	createCommit := strings.Repeat("5", 40)
	updateCommit := strings.Repeat("6", 40)
	path := "namespaces/git-authored-state.md"
	current := createCommit
	srv := newCatalogServer(t, store, newTreeGitReader(&current, map[string]map[string][]byte{
		createCommit: {
			path: detailedNamespaceManifest("git-authored-state", "catalog", "trunk", 1024, "# First body\n"),
		},
		updateCommit: {
			path: detailedNamespaceManifest("git-authored-state", "platform", "main", 2048, "# Second body\n"),
		},
	}))

	_, err := srv.AdmitResources(context.Background(), &catalogv1.AdmitResourcesRequest{
		RepositoryId: testRepoID,
		OldCommitSha: zero,
		NewCommitSha: createCommit,
		CommitSha:    createCommit,
		RefName:      "refs/heads/main",
		ChangedPaths: []string{path},
		ActorSubject: "alice",
	})
	require.NoError(t, err)
	created, err := store.GetNamespaceByName(context.Background(), "git-authored-state")
	require.NoError(t, err)
	assert.Equal(t, map[string]string{"team": "catalog"}, created.Labels)
	assert.Equal(t, map[string]string{"owner": "catalog"}, created.Annotations)
	assert.Equal(t, "# First body\n", created.Body)
	assert.Equal(t, path, created.SourcePath)
	assert.Equal(t, createCommit, created.GitCommitSHA)

	current = updateCommit
	_, err = srv.AdmitResources(context.Background(), &catalogv1.AdmitResourcesRequest{
		RepositoryId: testRepoID,
		OldCommitSha: createCommit,
		NewCommitSha: updateCommit,
		CommitSha:    updateCommit,
		RefName:      "refs/heads/main",
		ChangedPaths: []string{path},
		ActorSubject: "bob",
	})
	require.NoError(t, err)
	updated, err := store.GetNamespaceByName(context.Background(), "git-authored-state")
	require.NoError(t, err)
	assert.Equal(t, created.Generation+1, updated.Generation)
	assert.Equal(t, "2", updated.ResourceVersion)
	assert.Equal(t, map[string]string{"team": "platform"}, updated.Labels)
	assert.Equal(t, map[string]string{"owner": "platform"}, updated.Annotations)
	assert.Equal(t, "# Second body\n", updated.Body)
	assert.Equal(t, updateCommit, updated.GitCommitSHA)
	var spec catalog.NamespaceSpec
	require.NoError(t, json.Unmarshal(updated.Spec, &spec))
	require.NotNil(t, spec.RepositoryDefaults)
	require.NotNil(t, spec.PushPolicyDefaults)
	assert.Equal(t, "main", spec.RepositoryDefaults.DefaultBranch)
	assert.Equal(t, int64(2048), spec.PushPolicyDefaults.MaxPackSizeBytes)
}

func TestAdmitResourcesNamespaceProvenanceOnlyChangeKeepsGeneration(t *testing.T) {
	store := newNamespacePolicyDatastore(t)
	zero := strings.Repeat("0", 40)
	createCommit := strings.Repeat("7", 40)
	descendantCommit := strings.Repeat("8", 40)
	path := "namespaces/provenance-only-git.md"
	otherPath := "namespaces/provenance-only-other.md"
	current := createCommit
	manifest := namespaceManifest("provenance-only-git", "Provenance", "USER")
	srv := newCatalogServer(t, store, newTreeGitReader(&current, map[string]map[string][]byte{
		createCommit: {path: manifest},
		descendantCommit: {
			path:      manifest,
			otherPath: namespaceManifest("provenance-only-other", "Other", "USER"),
		},
	}))

	request := &catalogv1.AdmitResourcesRequest{
		RepositoryId: testRepoID,
		OldCommitSha: zero,
		NewCommitSha: createCommit,
		CommitSha:    createCommit,
		RefName:      "refs/heads/main",
		ChangedPaths: []string{path},
		ActorSubject: "alice",
	}
	_, err := srv.AdmitResources(context.Background(), request)
	require.NoError(t, err)
	created, err := store.GetNamespaceByName(context.Background(), "provenance-only-git")
	require.NoError(t, err)

	current = descendantCommit
	_, err = srv.AdmitResources(context.Background(), request)
	require.NoError(t, err)
	updated, err := store.GetNamespaceByName(context.Background(), "provenance-only-git")
	require.NoError(t, err)
	assert.Equal(t, created.Generation, updated.Generation)
	assert.Equal(t, "2", updated.ResourceVersion)
	assert.Equal(t, descendantCommit, updated.GitCommitSHA)
}

func TestAdmitResourcesNamespaceEmptyValuedLabelKeyChangeAdvancesGeneration(t *testing.T) {
	store := newNamespacePolicyDatastore(t)
	zero := strings.Repeat("0", 40)
	first := strings.Repeat("9", 40)
	second := strings.Repeat("a", 40)
	path := "namespaces/presence-aware-labels.md"
	current := first
	manifest := func(key string) []byte {
		return []byte(fmt.Sprintf(`---
apiVersion: gitstore.dev/v1beta1
kind: Namespace
metadata:
  name: presence-aware-labels
  labels:
    %s: ""
spec:
  title: Presence-aware labels
  tier: USER
---
`, key))
	}
	srv := newCatalogServer(t, store, newTreeGitReader(&current, map[string]map[string][]byte{
		first:  {path: manifest("old-key")},
		second: {path: manifest("new-key")},
	}))

	_, err := srv.AdmitResources(context.Background(), &catalogv1.AdmitResourcesRequest{
		RepositoryId: testRepoID,
		OldCommitSha: zero,
		NewCommitSha: first,
		CommitSha:    first,
		RefName:      "refs/heads/main",
		ChangedPaths: []string{path},
		ActorSubject: "alice",
	})
	require.NoError(t, err)
	created, err := store.GetNamespaceByName(context.Background(), "presence-aware-labels")
	require.NoError(t, err)

	current = second
	_, err = srv.AdmitResources(context.Background(), &catalogv1.AdmitResourcesRequest{
		RepositoryId: testRepoID,
		OldCommitSha: first,
		NewCommitSha: second,
		CommitSha:    second,
		RefName:      "refs/heads/main",
		ChangedPaths: []string{path},
		ActorSubject: "bob",
	})
	require.NoError(t, err)
	updated, err := store.GetNamespaceByName(context.Background(), "presence-aware-labels")
	require.NoError(t, err)

	assert.Equal(t, created.Generation+1, updated.Generation)
	assert.Equal(t, map[string]string{"new-key": ""}, updated.Labels)
}

func detailedNamespaceManifest(name, team, defaultBranch string, maxPack int64, body string) []byte {
	return []byte(fmt.Sprintf(`---
apiVersion: gitstore.dev/v1beta1
kind: Namespace
metadata:
  name: %s
  labels:
    team: %s
  annotations:
    owner: %s
spec:
  title: Authored state
  tier: USER
  repositoryDefaults:
    visibility: PRIVATE
    defaultBranch: %s
  pushPolicyDefaults:
    maxPackSizeBytes: %d
    maxFileSizeBytes: 512
---
%s`, name, team, team, defaultBranch, maxPack, body))
}
