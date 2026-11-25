// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package cataloggrpc_test

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"

	catalogv1 "github.com/gitstore-dev/gitstore/api/gen/gitstore/catalog/v1"
	"github.com/gitstore-dev/gitstore/api/internal/admission"
	"github.com/gitstore-dev/gitstore/api/internal/auth"
	"github.com/gitstore-dev/gitstore/api/internal/cataloggrpc"
	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	"github.com/gitstore-dev/gitstore/api/internal/testutil"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestNamespaceFreeCategoryTreeAcrossReplicasAndStores(t *testing.T) {
	store := newTestDatastore(t)
	secondRepo := uuid.NewString()
	require.NoError(t, store.CreateNamespace(t.Context(), &datastore.Namespace{
		UID: uuid.NewString(), Name: "second-store", Tier: datastore.NamespaceTierOrganization, CreationActor: "test",
	}))
	require.NoError(t, store.CreateRepository(t.Context(), &datastore.Repository{
		UID: secondRepo, Namespace: "second-store", Name: "catalog", DefaultBranch: "main", StorageClass: "local", CreationActor: "test",
	}))
	withoutNamespace := func(source []byte) []byte {
		return bytes.ReplaceAll(source, []byte("  namespace: gitstore\n"), nil)
	}
	root := withoutNamespace(makeCategoryTaxonomy("shopify-aa"))
	child := bytes.Replace(withoutNamespace(makeCategoryTaxonomy("shopify-aa-1")),
		[]byte("spec:\n"), []byte("spec:\n  parentRef: {kind: CategoryTaxonomy, name: shopify-aa}\n"), 1)
	files := map[string][]byte{"categories/a-root.md": root, "categories/b-child.md": child}
	git := &mockGitReader{
		listFilesFunc: func(context.Context, string, string, string) ([]string, error) {
			return []string{"categories/a-root.md", "categories/b-child.md"}, nil
		},
		readFileFunc: func(_ context.Context, _, path, _ string) ([]byte, error) {
			return bytes.Clone(files[path]), nil
		},
	}
	replicas := []*catalogTestServer{newCatalogServer(t, store, git), newCatalogServer(t, store, git)}
	type target struct{ repo, namespace string }
	targets := []target{{testRepoID, "gitstore"}, {secondRepo, "second-store"}}
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for i, target := range targets {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := replicas[i].AdmitResources(t.Context(), &catalogv1.AdmitResourcesRequest{
				RepositoryId: target.repo, NewCommitSha: strings.Repeat("a", 40), RefName: "refs/heads/main",
			})
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}
	uids := map[string]bool{}
	for _, target := range targets {
		parent, err := store.GetCategoryTaxonomyByName(t.Context(), target.namespace, "shopify-aa")
		require.NoError(t, err)
		leaf, err := store.GetCategoryTaxonomyByName(t.Context(), target.namespace, "shopify-aa-1")
		require.NoError(t, err)
		require.Equal(t, target.repo, parent.RepositoryID)
		require.Equal(t, target.namespace, leaf.Namespace)
		require.Equal(t, parent.Name, leaf.ParentName)
		require.Equal(t, parent.Name+"/"+leaf.Name, leaf.AncestorPath)
		require.False(t, uids[parent.UID])
		uids[parent.UID] = true
		var spec struct {
			ParentRef struct {
				Name string `json:"name"`
			} `json:"parentRef"`
		}
		require.NoError(t, json.Unmarshal(leaf.Spec, &spec))
		require.Equal(t, parent.Name, spec.ParentRef.Name)
		// Replaying through another replica uses the same effective identity.
		_, err = replicas[1].AdmitResources(t.Context(), &catalogv1.AdmitResourcesRequest{
			RepositoryId: target.repo, NewCommitSha: strings.Repeat("a", 40), RefName: "refs/heads/main",
		})
		require.NoError(t, err)
		replayed, err := store.GetCategoryTaxonomyByName(t.Context(), target.namespace, parent.Name)
		require.NoError(t, err)
		require.Equal(t, parent.UID, replayed.UID)
		require.Equal(t, parent.Generation, replayed.Generation)
		require.Equal(t, parent.ResourceVersion, replayed.ResourceVersion)
		// Adding an explicit, equivalent namespace is not an immutable change.
		explicit := bytes.Replace(root, []byte("metadata:\n"), []byte("metadata:\n  namespace: "+target.namespace+"\n"), 1)
		findings, err := replicas[0].ValidateManifest(t.Context(), admission.ManifestValidationRequest{
			RepositoryID: target.repo, Path: "categories/a-root.md", OldContent: root, NewContent: explicit,
		})
		require.NoError(t, err)
		require.Empty(t, findings)
	}
	require.NotContains(t, string(root), "namespace:")
	require.Equal(t, root, files["categories/a-root.md"])
}

func TestNamespaceFreeManifestCannotSelectForeignOrUnresolvedRepositoryScope(t *testing.T) {
	server := newCatalogServer(t, newTestDatastore(t), nil)
	for _, tt := range []struct {
		repo   string
		source []byte
	}{
		{testRepoID, bytes.ReplaceAll(makeCategoryTaxonomy("sample"), []byte("namespace: gitstore"), []byte("namespace: foreign"))},
		{uuid.NewString(), bytes.ReplaceAll(makeCategoryTaxonomy("sample"), []byte("  namespace: gitstore\n"), nil)},
	} {
		findings, err := server.ValidateManifest(t.Context(), admission.ManifestValidationRequest{
			RepositoryID: tt.repo, Path: "categories/sample.md", NewContent: tt.source,
		})
		require.NoError(t, err)
		require.NotEmpty(t, findings)
	}
}

func TestNamespaceInferenceDoesNotBypassAuthorization(t *testing.T) {
	store := newTestDatastore(t)
	content := bytes.ReplaceAll(makeCategoryTaxonomy("protected"), []byte("  namespace: gitstore\n"), nil)
	recorder := testutil.NewRecordingAuthZ(testutil.NewDenyAllAuthZ(t))
	reader := &mockGitReader{
		listFilesFunc: func(context.Context, string, string, string) ([]string, error) {
			return []string{"categories/protected.md"}, nil
		},
		readFileFunc: func(context.Context, string, string, string) ([]byte, error) { return bytes.Clone(content), nil },
	}
	server := newCatalogServer(t, store, reader, func(deps *cataloggrpc.ServerDeps) {
		deps.Registry = auth.NewProviderRegistry(nil, recorder, nil)
	})
	_, err := server.AdmitResources(t.Context(), &catalogv1.AdmitResourcesRequest{
		RepositoryId: testRepoID, NewCommitSha: strings.Repeat("a", 40), RefName: "refs/heads/main",
	})
	require.Equal(t, codes.PermissionDenied, status.Code(err))
	_, err = store.GetCategoryTaxonomyByName(t.Context(), "gitstore", "protected")
	require.ErrorIs(t, err, datastore.ErrNotFound)
}

func TestNamespaceFreeMovePreservesCategoryIdentity(t *testing.T) {
	store := newTestDatastore(t)
	first, second := strings.Repeat("a", 40), strings.Repeat("b", 40)
	current := first
	source := bytes.ReplaceAll(makeCategoryTaxonomy("movable"), []byte("  namespace: gitstore\n"), nil)
	reader := newTreeGitReader(&current, map[string]map[string][]byte{
		first:  {"categories/old.md": source},
		second: {"categories/moved.md": source},
	})
	server := newCatalogServer(t, store, reader)
	admitDelta(t, server, strings.Repeat("0", 40), first)
	before, err := store.GetCategoryTaxonomyByName(t.Context(), "gitstore", "movable")
	require.NoError(t, err)
	current = second
	admitDelta(t, server, first, second)
	after, err := store.GetCategoryTaxonomyByName(t.Context(), "gitstore", "movable")
	require.NoError(t, err)
	require.Equal(t, before.UID, after.UID)
	require.Equal(t, before.Generation, after.Generation)
	require.NotEqual(t, before.ResourceVersion, after.ResourceVersion)
	require.Equal(t, "categories/moved.md", after.SourcePath)
}
