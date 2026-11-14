// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package cataloggrpc_test

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	catalogv1 "github.com/gitstore-dev/gitstore/api/gen/gitstore/catalog/v1"
	"github.com/gitstore-dev/gitstore/api/internal/admission"
	"github.com/gitstore-dev/gitstore/api/internal/auth"
	"github.com/gitstore-dev/gitstore/api/internal/catalog"
	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	"github.com/gitstore-dev/gitstore/api/internal/gitclient"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCommittedNamespaceCallbackWaitsOnlyForUnavailableAuthorization(t *testing.T) {
	for _, test := range []struct {
		name        string
		failure     error
		cancelEarly bool
		created     bool
	}{
		{name: "publication recovers", failure: auth.ErrAuthorizationUnavailable, created: true},
		{name: "denial is not retried", failure: gitclient.ErrAuthorizationDenied},
		{name: "cancellation stops waiting", failure: auth.ErrAuthorizationUnavailable, cancelEarly: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			store := newNamespacePolicyDatastore(t)
			commit := strings.Repeat("a", 40)
			path := "namespaces/publication-retry.md"
			reader := newTreeGitReader(&commit, map[string]map[string][]byte{
				commit: {path: namespaceManifest("publication-retry", "Publication retry", "ORGANIZATION")},
			})
			calls := 0
			reader.resolveRefFunc = func(context.Context, string, string) (string, error) {
				calls++
				if calls == 1 {
					return "", fmt.Errorf("git authorization evaluation: %w", test.failure)
				}
				return commit, nil
			}
			srv := newCatalogServer(t, store, reader)
			ctx := t.Context()
			if test.cancelEarly {
				var cancel context.CancelFunc
				ctx, cancel = context.WithTimeout(ctx, 50*time.Millisecond)
				defer cancel()
			}
			_, err := srv.AdmitResources(ctx, &catalogv1.AdmitResourcesRequest{
				RepositoryId: testRepoID, NewCommitSha: commit, ChangedPaths: []string{path},
				Authorization: &catalogv1.CallbackAuthorization{Actor: &catalogv1.CallbackActor{
					Subject: "alice", AuthMethod: "static-users",
				}},
			})
			require.NoError(t, err)
			stored, err := store.GetNamespaceByName(t.Context(), "publication-retry")
			if test.created {
				require.NoError(t, err)
				require.Equal(t, commit, stored.GitCommitSHA)
				require.Greater(t, calls, 1)
			} else {
				require.ErrorIs(t, err, datastore.ErrNotFound)
				require.Equal(t, 1, calls)
			}
		})
	}
}

func TestCommittedUserNamespaceReadbackPreservesProtectedOwnerAnnotations(t *testing.T) {
	store := newNamespacePolicyDatastore(t)
	first, second := strings.Repeat("3", 40), strings.Repeat("4", 40)
	path := "namespaces/alice.md"
	current := first
	manifests := map[string]map[string][]byte{
		first:  {path: namespaceManifest("alice", "First", "USER")},
		second: {path: namespaceManifest("alice", "Second", "USER")},
	}
	srv := newCatalogServer(t, store, newTreeGitReader(&current, manifests))
	ctx := auth.ContextWithPrincipal(t.Context(), &auth.Principal{
		Subject: "alice", AuthMethod: "static-users", Issuer: "gitstore/static-users",
	})
	for _, commit := range []string{first, second} {
		current = commit
		operation := admission.OperationCreate
		if commit == second {
			operation = admission.OperationUpdate
		}
		result, err := srv.Server.AdmitCommittedManifest(ctx, admission.CommittedManifestRequest{
			RepositoryID: testRepoID, ActorSubject: "alice", RefName: "refs/heads/main",
			Path: path, CommitSHA: commit, Content: manifests[commit][path], Operation: operation,
		})
		require.NoError(t, err, "server-minted annotations are not an authored-content mismatch")
		require.Equal(t, commit, result.CommitSHA)
		stored, err := store.GetNamespaceByName(ctx, "alice")
		require.NoError(t, err)
		require.Equal(t, "static-users", stored.Annotations[datastore.OwnerAuthMethodAnnotationKey])
		require.Equal(t, "gitstore/static-users", stored.Annotations[datastore.OwnerIssuerAnnotationKey])
	}
}

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

		RefName:       "refs/heads/main",
		ChangedPaths:  []string{path},
		Authorization: &catalogv1.CallbackAuthorization{Actor: &catalogv1.CallbackActor{Subject: "alice", AuthMethod: "static-users"}},
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

		RefName:       "refs/heads/main",
		ChangedPaths:  []string{path},
		Authorization: &catalogv1.CallbackAuthorization{Actor: &catalogv1.CallbackActor{Subject: "bob", AuthMethod: "static-users"}},
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
	manifest := namespaceManifest("provenance-only-git", "Provenance", "ORGANIZATION")
	srv := newCatalogServer(t, store, newTreeGitReader(&current, map[string]map[string][]byte{
		createCommit: {path: manifest},
		descendantCommit: {
			path:      manifest,
			otherPath: namespaceManifest("provenance-only-other", "Other", "ORGANIZATION"),
		},
	}))

	request := &catalogv1.AdmitResourcesRequest{
		RepositoryId: testRepoID,
		OldCommitSha: zero,
		NewCommitSha: createCommit,

		RefName:       "refs/heads/main",
		ChangedPaths:  []string{path},
		Authorization: &catalogv1.CallbackAuthorization{Actor: &catalogv1.CallbackActor{Subject: "alice", AuthMethod: "static-users"}},
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

func TestAdmitResourcesNamespaceEmptyValuedLabelKeyChangeKeepsGeneration(t *testing.T) {
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
  tier: ORGANIZATION
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

		RefName:       "refs/heads/main",
		ChangedPaths:  []string{path},
		Authorization: &catalogv1.CallbackAuthorization{Actor: &catalogv1.CallbackActor{Subject: "alice", AuthMethod: "static-users"}},
	})
	require.NoError(t, err)
	created, err := store.GetNamespaceByName(context.Background(), "presence-aware-labels")
	require.NoError(t, err)

	current = second
	_, err = srv.AdmitResources(context.Background(), &catalogv1.AdmitResourcesRequest{
		RepositoryId: testRepoID,
		OldCommitSha: first,
		NewCommitSha: second,

		RefName:       "refs/heads/main",
		ChangedPaths:  []string{path},
		Authorization: &catalogv1.CallbackAuthorization{Actor: &catalogv1.CallbackActor{Subject: "bob", AuthMethod: "static-users"}},
	})
	require.NoError(t, err)
	updated, err := store.GetNamespaceByName(context.Background(), "presence-aware-labels")
	require.NoError(t, err)

	assert.Equal(t, created.Generation, updated.Generation)
	assert.Equal(t, "2", updated.ResourceVersion)
	assert.Equal(t, map[string]string{"new-key": ""}, updated.Labels)
}

func TestAdmitResourcesObjectMetaUpdatePreservesSystemOwnerReferences(t *testing.T) {
	ctx := context.Background()
	store := newNamespacePolicyDatastore(t)
	zero := strings.Repeat("0", 40)
	first := strings.Repeat("c", 40)
	second := strings.Repeat("d", 40)
	path := "namespaces/system-owner-reference.md"
	current := first
	manifest := func(label string) []byte {
		return []byte(fmt.Sprintf("---\napiVersion: gitstore.dev/v1beta1\nkind: Namespace\nmetadata:\n  name: system-owner-reference\n  labels:\n    team: %s\nspec:\n  title: System owner reference\n  tier: ORGANIZATION\n---\n", label))
	}
	srv := newCatalogServer(t, store, newTreeGitReader(&current, map[string]map[string][]byte{
		first:  {path: manifest("catalog")},
		second: {path: manifest("platform")},
	}))
	_, err := srv.AdmitResources(ctx, &catalogv1.AdmitResourcesRequest{Authorization: &catalogv1.CallbackAuthorization{Actor: &catalogv1.CallbackActor{Subject: "alice", AuthMethod: "static-users"}}, RepositoryId: testRepoID, OldCommitSha: zero, NewCommitSha: first, RefName: "refs/heads/main", ChangedPaths: []string{path}})
	require.NoError(t, err)
	namespace, err := store.GetNamespaceByName(ctx, "system-owner-reference")
	require.NoError(t, err)
	ownerReferences, err := json.Marshal([]catalog.OwnerReference{{APIVersion: "gitstore.dev/v1beta1", Kind: "Namespace", Name: "owner", UID: "owner-uid", BlockOwnerDeletion: true}})
	require.NoError(t, err)
	expected := namespace.ResourceVersion
	namespace.OwnerReferences = ownerReferences
	datastore.AdvanceNamespaceSystemVersion(namespace)
	require.NoError(t, store.UpdateNamespace(ctx, namespace, expected))

	current = second
	_, err = srv.AdmitResources(ctx, &catalogv1.AdmitResourcesRequest{Authorization: &catalogv1.CallbackAuthorization{Actor: &catalogv1.CallbackActor{Subject: "bob", AuthMethod: "static-users"}}, RepositoryId: testRepoID, OldCommitSha: first, NewCommitSha: second, RefName: "refs/heads/main", ChangedPaths: []string{path}})
	require.NoError(t, err)
	updated, err := store.GetNamespaceByName(ctx, "system-owner-reference")
	require.NoError(t, err)
	assert.Equal(t, int64(1), updated.Generation)
	assert.Equal(t, "3", updated.ResourceVersion)
	assert.JSONEq(t, string(ownerReferences), string(updated.OwnerReferences))
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
  tier: ORGANIZATION
  repositoryDefaults:
    visibility: PRIVATE
    defaultBranch: %s
  pushPolicyDefaults:
    maxPackSizeBytes: %d
    maxFileSizeBytes: 512
---
%s`, name, team, team, defaultBranch, maxPack, body))
}
