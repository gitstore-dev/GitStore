// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package cataloggrpc_test

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	catalogv1 "github.com/gitstore-dev/gitstore/api/gen/gitstore/catalog/v1"
	"github.com/gitstore-dev/gitstore/api/internal/cataloggrpc"
	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	"github.com/gitstore-dev/gitstore/api/internal/datastore/memdb"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// validProduct is a minimal valid product YAML frontmatter blob.
const validProduct = `---
apiVersion: catalog.gitstore.dev/v1beta1
kind: Product
metadata:
  name: widget
  namespace: gitstore
spec:
  title: Widget
---

A test product.
`

// T011a: blob with valid frontmatter → accepted=true, empty errors
func TestValidateResources_ValidBlob_Accepted(t *testing.T) {
	srv := cataloggrpc.NewServer(nil, nil)
	resp, err := srv.ValidateResources(context.Background(), &catalogv1.ValidateResourcesRequest{
		RepositoryId: "repo-1",
		Blobs: []*catalogv1.ResourceBlob{
			{Path: "products/widget.md", BlobOid: "abc", Content: []byte(validProduct)},
		},
	})
	require.NoError(t, err)
	assert.True(t, resp.Accepted, "expected accepted=true for valid blob")
	assert.Empty(t, resp.Errors, "expected no errors for valid blob")
}

// T011b: blob with `status:` key → accepted=false, error names `status` and `system-managed`
func TestValidateResources_StatusKey_Rejected(t *testing.T) {
	content := `---
apiVersion: catalog.gitstore.dev/v1beta1
kind: Product
metadata:
  name: bad
  namespace: gitstore
spec:
  title: Bad
status:
  phase: ready
---
`
	srv := cataloggrpc.NewServer(nil, nil)
	resp, err := srv.ValidateResources(context.Background(), &catalogv1.ValidateResourcesRequest{
		RepositoryId: "repo-1",
		Blobs: []*catalogv1.ResourceBlob{
			{Path: "products/bad.md", BlobOid: "abc", Content: []byte(content)},
		},
	})
	require.NoError(t, err)
	assert.False(t, resp.Accepted, "expected accepted=false for status key")
	require.NotEmpty(t, resp.Errors)
	msgs := collectMessages(resp.Errors)
	assert.True(t, containsSubstring(msgs, "status"), "expected 'status' in error message")
	assert.True(t, containsSubstring(msgs, "system-managed"), "expected 'system-managed' in error message")
}

// T011c: blob with `spec.title` > 200 chars → accepted=false, error names `spec.title` and limit
func TestValidateResources_TitleTooLong_Rejected(t *testing.T) {
	longTitle := strings.Repeat("x", 201)
	content := "---\napiVersion: catalog.gitstore.dev/v1beta1\nkind: Product\nmetadata:\n  name: long\n  namespace: gitstore\nspec:\n  title: " + longTitle + "\n---\n"
	srv := cataloggrpc.NewServer(nil, nil)
	resp, err := srv.ValidateResources(context.Background(), &catalogv1.ValidateResourcesRequest{
		RepositoryId: "repo-1",
		Blobs: []*catalogv1.ResourceBlob{
			{Path: "products/long.md", BlobOid: "abc", Content: []byte(content)},
		},
	})
	require.NoError(t, err)
	assert.False(t, resp.Accepted)
	require.NotEmpty(t, resp.Errors)
	msgs := collectMessages(resp.Errors)
	assert.True(t, containsSubstring(msgs, "spec.title") || containsSubstring(msgs, "title"), "expected title in error")
	assert.True(t, containsSubstring(msgs, "200") || containsSubstring(msgs, "maximum"), "expected length limit in error")
}

// T011d: two blobs one valid one invalid → accepted=false, only invalid blob produces errors
func TestValidateResources_TwoBlobsOneInvalid_AllProcessed(t *testing.T) {
	badContent := `---
apiVersion: catalog.gitstore.dev/v1beta1
kind: Product
metadata:
  name: bad
  namespace: gitstore
spec:
  title: Bad
status:
  phase: ready
---
`
	srv := cataloggrpc.NewServer(nil, nil)
	resp, err := srv.ValidateResources(context.Background(), &catalogv1.ValidateResourcesRequest{
		RepositoryId: "repo-1",
		Blobs: []*catalogv1.ResourceBlob{
			{Path: "products/widget.md", BlobOid: "aaa", Content: []byte(validProduct)},
			{Path: "products/bad.md", BlobOid: "bbb", Content: []byte(badContent)},
		},
	})
	require.NoError(t, err)
	assert.False(t, resp.Accepted, "expected rejected because one blob is invalid")
	// All errors should reference the invalid blob path
	for _, e := range resp.Errors {
		assert.Equal(t, "products/bad.md", e.FilePath, "error should reference the bad blob")
	}
}

// T011e: blob without `---` prefix → treated as no-op, no error
func TestValidateResources_NonfrontmatterBlob_NoError(t *testing.T) {
	content := []byte("This is a plain README without frontmatter.\n")
	srv := cataloggrpc.NewServer(nil, nil)
	resp, err := srv.ValidateResources(context.Background(), &catalogv1.ValidateResourcesRequest{
		RepositoryId: "repo-1",
		Blobs: []*catalogv1.ResourceBlob{
			{Path: "README.md", BlobOid: "abc", Content: content},
		},
	})
	require.NoError(t, err)
	assert.True(t, resp.Accepted, "non-frontmatter blobs must be no-ops")
	assert.Empty(t, resp.Errors)
}

// ---- helpers ----

func collectMessages(errs []*catalogv1.ValidationError) []string {
	msgs := make([]string, len(errs))
	for i, e := range errs {
		msgs[i] = e.Message + " " + e.Field + " " + e.Constraint
	}
	return msgs
}

func containsSubstring(msgs []string, sub string) bool {
	for _, m := range msgs {
		if strings.Contains(strings.ToLower(m), strings.ToLower(sub)) {
			return true
		}
	}
	return false
}

// TestValidateResources_EmptyBlobs ensures empty input is accepted with no errors.
func TestValidateResources_EmptyBlobs_Accepted(t *testing.T) {
	srv := cataloggrpc.NewServer(nil, nil)
	resp, err := srv.ValidateResources(context.Background(), &catalogv1.ValidateResourcesRequest{
		RepositoryId: "repo-1",
		Blobs:        nil,
	})
	require.NoError(t, err)
	assert.True(t, resp.Accepted)
	assert.Empty(t, resp.Errors)
}

// ---------------------------------------------------------------------------
// T020: AdmitResources tests
// ---------------------------------------------------------------------------

// mockGitReader is a test double for the GitReader interface used by AdmitResources.
type mockGitReader struct {
	listFilesFunc func(ctx context.Context, repositoryID, prefix, ref string) ([]string, error)
	readFileFunc  func(ctx context.Context, repositoryID, path, ref string) ([]byte, error)
}

func (m *mockGitReader) ListFiles(ctx context.Context, repositoryID, prefix, ref string) ([]string, error) {
	return m.listFilesFunc(ctx, repositoryID, prefix, ref)
}

func (m *mockGitReader) ReadFile(ctx context.Context, repositoryID, path, ref string) ([]byte, error) {
	return m.readFileFunc(ctx, repositoryID, path, ref)
}

// makeProduct builds a valid product blob for a given name.
func makeProduct(name string) []byte {
	return []byte("---\napiVersion: catalog.gitstore.dev/v1beta1\nkind: Product\nmetadata:\n  name: " + name + "\n  namespace: gitstore\nspec:\n  title: " + name + "\n---\n")
}

// T020a: valid commit_sha with one product file → CreateProduct called with correct spec fields
func TestAdmitResources_NewProduct_Created(t *testing.T) {
	memStore := newTestDatastore(t)
	git := &mockGitReader{
		listFilesFunc: func(_ context.Context, _, _, _ string) ([]string, error) {
			return []string{"products/widget.md"}, nil
		},
		readFileFunc: func(_ context.Context, _, path, _ string) ([]byte, error) {
			return makeProduct("widget"), nil
		},
	}

	srv := cataloggrpc.NewServerForTest(memStore, git)
	_, err := srv.AdmitResources(context.Background(), &catalogv1.AdmitResourcesRequest{
		RepositoryId: "repo-1",
		CommitSha:    strings.Repeat("a", 40),
		RefName:      "refs/heads/main",
	})
	require.NoError(t, err)

	p, err := memStore.GetProductByName(context.Background(), "gitstore", "widget")
	require.NoError(t, err)
	assert.Equal(t, "widget", p.Name)
	assert.Equal(t, int64(1), p.Generation)
	assert.NotEmpty(t, p.UID)
	assert.False(t, p.CreationTimestamp.IsZero())
	assert.Contains(t, p.Revision, "main@sha1:")
}

// T020b: product already exists → UpdateProduct called; uid and creationTimestamp preserved,
// generation and resourceVersion incremented
func TestAdmitResources_ExistingProduct_Updated(t *testing.T) {
	memStore := newTestDatastore(t)
	git := &mockGitReader{
		listFilesFunc: func(_ context.Context, _, _, _ string) ([]string, error) {
			return []string{"products/widget.md"}, nil
		},
		readFileFunc: func(_ context.Context, _, _, _ string) ([]byte, error) {
			return makeProduct("widget"), nil
		},
	}

	srv := cataloggrpc.NewServerForTest(memStore, git)

	// First admission — creates the product
	_, err := srv.AdmitResources(context.Background(), &catalogv1.AdmitResourcesRequest{
		RepositoryId: "repo-1",
		CommitSha:    strings.Repeat("a", 40),
		RefName:      "refs/heads/main",
	})
	require.NoError(t, err)

	p1, err := memStore.GetProductByName(context.Background(), "gitstore", "widget")
	require.NoError(t, err)
	uid1 := p1.UID
	ts1 := p1.CreationTimestamp
	gen1 := p1.Generation

	// Second admission — updates the product
	_, err = srv.AdmitResources(context.Background(), &catalogv1.AdmitResourcesRequest{
		RepositoryId: "repo-1",
		CommitSha:    strings.Repeat("b", 40),
		RefName:      "refs/heads/main",
	})
	require.NoError(t, err)

	p2, err := memStore.GetProductByName(context.Background(), "gitstore", "widget")
	require.NoError(t, err)
	assert.Equal(t, uid1, p2.UID, "UID must be preserved on update")
	assert.Equal(t, ts1, p2.CreationTimestamp, "creationTimestamp must be preserved on update")
	assert.Greater(t, p2.Generation, gen1, "generation must be incremented")
}

// T020c: two product files in one commit → both stored independently
func TestAdmitResources_TwoProducts_BothStored(t *testing.T) {
	memStore := newTestDatastore(t)
	git := &mockGitReader{
		listFilesFunc: func(_ context.Context, _, _, _ string) ([]string, error) {
			return []string{"products/alpha.md", "products/beta.md"}, nil
		},
		readFileFunc: func(_ context.Context, _, path, _ string) ([]byte, error) {
			if strings.Contains(path, "alpha") {
				return makeProduct("alpha"), nil
			}
			return makeProduct("beta"), nil
		},
	}

	srv := cataloggrpc.NewServerForTest(memStore, git)
	_, err := srv.AdmitResources(context.Background(), &catalogv1.AdmitResourcesRequest{
		RepositoryId: "repo-1",
		CommitSha:    strings.Repeat("a", 40),
		RefName:      "refs/heads/main",
	})
	require.NoError(t, err)

	_, err = memStore.GetProductByName(context.Background(), "gitstore", "alpha")
	require.NoError(t, err, "alpha must be stored")
	_, err = memStore.GetProductByName(context.Background(), "gitstore", "beta")
	require.NoError(t, err, "beta must be stored")
}

// T020d: one file parse failure in multi-product commit → failure logged, other product stored (FR-011)
func TestAdmitResources_OneParseFailure_OtherStored(t *testing.T) {
	memStore := newTestDatastore(t)
	git := &mockGitReader{
		listFilesFunc: func(_ context.Context, _, _, _ string) ([]string, error) {
			return []string{"products/good.md", "products/bad.md"}, nil
		},
		readFileFunc: func(_ context.Context, _, path, _ string) ([]byte, error) {
			if strings.Contains(path, "good") {
				return makeProduct("good"), nil
			}
			// bad product has status key — will fail parse
			return []byte("---\napiVersion: catalog.gitstore.dev/v1beta1\nkind: Product\nmetadata:\n  name: bad\n  namespace: gitstore\nspec:\n  title: Bad\nstatus:\n  phase: ready\n---\n"), nil
		},
	}

	srv := cataloggrpc.NewServerForTest(memStore, git)
	_, err := srv.AdmitResources(context.Background(), &catalogv1.AdmitResourcesRequest{
		RepositoryId: "repo-1",
		CommitSha:    strings.Repeat("a", 40),
		RefName:      "refs/heads/main",
	})
	require.NoError(t, err, "AdmitResources must not fail even when one product parse fails")

	_, err = memStore.GetProductByName(context.Background(), "gitstore", "good")
	require.NoError(t, err, "good product must be stored despite bad product failure")

	_, err = memStore.GetProductByName(context.Background(), "gitstore", "bad")
	assert.Error(t, err, "bad product must not be stored")
}

// T020e: stored product has AdmissionAccepted: True condition (FR-009)
func TestAdmitResources_AdmissionAcceptedConditionSet(t *testing.T) {
	memStore := newTestDatastore(t)
	git := &mockGitReader{
		listFilesFunc: func(_ context.Context, _, _, _ string) ([]string, error) {
			return []string{"products/widget.md"}, nil
		},
		readFileFunc: func(_ context.Context, _, _, _ string) ([]byte, error) {
			return makeProduct("widget"), nil
		},
	}

	srv := cataloggrpc.NewServerForTest(memStore, git)
	_, err := srv.AdmitResources(context.Background(), &catalogv1.AdmitResourcesRequest{
		RepositoryId: "repo-1",
		CommitSha:    strings.Repeat("a", 40),
		RefName:      "refs/heads/main",
	})
	require.NoError(t, err)

	p, err := memStore.GetProductByName(context.Background(), "gitstore", "widget")
	require.NoError(t, err)
	require.NotEmpty(t, p.Status, "status must be set")

	// Decode status JSON and check AdmissionAccepted condition
	var status struct {
		Conditions []struct {
			Type   string `json:"type"`
			Status string `json:"status"`
		} `json:"conditions"`
	}
	require.NoError(t, json.Unmarshal(p.Status, &status))
	var found bool
	for _, c := range status.Conditions {
		if c.Type == "AdmissionAccepted" && c.Status == "True" {
			found = true
			break
		}
	}
	assert.True(t, found, "AdmissionAccepted: True condition must be set")
}

// ---------------------------------------------------------------------------
// T019: ValidateResources — CategoryTaxonomy schema validation
// ---------------------------------------------------------------------------

const validCategoryTaxonomy = `---
apiVersion: catalog.gitstore.dev/v1beta1
kind: CategoryTaxonomy
metadata:
  name: electronics
  namespace: gitstore
spec:
  title: Electronics
---

A category for electronic products.
`

func TestValidateResources_CategoryTaxonomy_Accepted(t *testing.T) {
	srv := cataloggrpc.NewServer(nil, nil)
	resp, err := srv.ValidateResources(context.Background(), &catalogv1.ValidateResourcesRequest{
		RepositoryId: "repo-1",
		Blobs: []*catalogv1.ResourceBlob{
			{Path: "categories/electronics.md", BlobOid: "abc", Content: []byte(validCategoryTaxonomy)},
		},
	})
	require.NoError(t, err)
	assert.True(t, resp.Accepted)
	assert.Empty(t, resp.Errors)
}

func TestValidateResources_CategoryTaxonomy_MissingTitle_Rejected(t *testing.T) {
	content := `---
apiVersion: catalog.gitstore.dev/v1beta1
kind: CategoryTaxonomy
metadata:
  name: electronics
  namespace: gitstore
spec: {}
---
`
	srv := cataloggrpc.NewServer(nil, nil)
	resp, err := srv.ValidateResources(context.Background(), &catalogv1.ValidateResourcesRequest{
		RepositoryId: "repo-1",
		Blobs: []*catalogv1.ResourceBlob{
			{Path: "categories/electronics.md", BlobOid: "abc", Content: []byte(content)},
		},
	})
	require.NoError(t, err)
	assert.False(t, resp.Accepted)
	require.NotEmpty(t, resp.Errors)
	msgs := collectMessages(resp.Errors)
	assert.True(t, containsSubstring(msgs, "spec.title"), "expected spec.title in error")
}

func TestValidateResources_CategoryTaxonomy_MissingName_Rejected(t *testing.T) {
	content := `---
apiVersion: catalog.gitstore.dev/v1beta1
kind: CategoryTaxonomy
metadata: {}
spec:
  title: Electronics
---
`
	srv := cataloggrpc.NewServer(nil, nil)
	resp, err := srv.ValidateResources(context.Background(), &catalogv1.ValidateResourcesRequest{
		RepositoryId: "repo-1",
		Blobs: []*catalogv1.ResourceBlob{
			{Path: "categories/electronics.md", BlobOid: "abc", Content: []byte(content)},
		},
	})
	require.NoError(t, err)
	assert.False(t, resp.Accepted)
	require.NotEmpty(t, resp.Errors)
	msgs := collectMessages(resp.Errors)
	assert.True(t, containsSubstring(msgs, "metadata.name"), "expected metadata.name in error")
}

func TestValidateResources_CategoryTaxonomy_StatusKey_Rejected(t *testing.T) {
	content := `---
apiVersion: catalog.gitstore.dev/v1beta1
kind: CategoryTaxonomy
metadata:
  name: electronics
spec:
  title: Electronics
status:
  phase: ready
---
`
	srv := cataloggrpc.NewServer(nil, nil)
	resp, err := srv.ValidateResources(context.Background(), &catalogv1.ValidateResourcesRequest{
		RepositoryId: "repo-1",
		Blobs: []*catalogv1.ResourceBlob{
			{Path: "categories/electronics.md", BlobOid: "abc", Content: []byte(content)},
		},
	})
	require.NoError(t, err)
	assert.False(t, resp.Accepted)
	msgs := collectMessages(resp.Errors)
	assert.True(t, containsSubstring(msgs, "status"), "expected 'status' in error message")
}

func TestValidateResources_UnknownKind_Rejected(t *testing.T) {
	content := `---
apiVersion: catalog.gitstore.dev/v1beta1
kind: UnknownKind
metadata:
  name: foo
spec:
  title: Foo
---
`
	srv := cataloggrpc.NewServer(nil, nil)
	resp, err := srv.ValidateResources(context.Background(), &catalogv1.ValidateResourcesRequest{
		RepositoryId: "repo-1",
		Blobs: []*catalogv1.ResourceBlob{
			{Path: "things/foo.md", BlobOid: "abc", Content: []byte(content)},
		},
	})
	require.NoError(t, err)
	assert.False(t, resp.Accepted)
	msgs := collectMessages(resp.Errors)
	assert.True(t, containsSubstring(msgs, "not a recognized"), "expected 'not a recognized' in error")
}

func TestValidateResources_ProductAndCategoryTaxonomy_BothValidated(t *testing.T) {
	srv := cataloggrpc.NewServer(nil, nil)
	resp, err := srv.ValidateResources(context.Background(), &catalogv1.ValidateResourcesRequest{
		RepositoryId: "repo-1",
		Blobs: []*catalogv1.ResourceBlob{
			{Path: "products/widget.md", BlobOid: "abc", Content: []byte(validProduct)},
			{Path: "categories/electronics.md", BlobOid: "def", Content: []byte(validCategoryTaxonomy)},
		},
	})
	require.NoError(t, err)
	assert.True(t, resp.Accepted)
	assert.Empty(t, resp.Errors)
}

// ---------------------------------------------------------------------------
// T020: AdmitResources — CategoryTaxonomy admission
// ---------------------------------------------------------------------------

func makeCategoryTaxonomy(name string) []byte {
	return []byte(`---
apiVersion: catalog.gitstore.dev/v1beta1
kind: CategoryTaxonomy
metadata:
  name: ` + name + `
  namespace: gitstore
spec:
  title: ` + strings.Title(name) + `
---

Category body.
`)
}

func TestAdmitResources_CategoryTaxonomy_Created(t *testing.T) {
	memStore := newTestDatastore(t)
	git := &mockGitReader{
		listFilesFunc: func(_ context.Context, _, _, _ string) ([]string, error) {
			return []string{"categories/electronics.md"}, nil
		},
		readFileFunc: func(_ context.Context, _, _, _ string) ([]byte, error) {
			return makeCategoryTaxonomy("electronics"), nil
		},
	}

	srv := cataloggrpc.NewServerForTest(memStore, git)
	_, err := srv.AdmitResources(context.Background(), &catalogv1.AdmitResourcesRequest{
		RepositoryId: "repo-1",
		CommitSha:    strings.Repeat("a", 40),
		RefName:      "refs/heads/main",
	})
	require.NoError(t, err)

	got, err := memStore.GetCategoryTaxonomyByName(context.Background(), "gitstore", "electronics")
	require.NoError(t, err)
	assert.Equal(t, "electronics", got.Name)
	assert.Equal(t, int64(1), got.Generation)
	assert.NotEmpty(t, got.UID)
	assert.False(t, got.CreationTimestamp.IsZero())
}

func TestAdmitResources_CategoryTaxonomy_Updated(t *testing.T) {
	memStore := newTestDatastore(t)
	git := &mockGitReader{
		listFilesFunc: func(_ context.Context, _, _, _ string) ([]string, error) {
			return []string{"categories/electronics.md"}, nil
		},
		readFileFunc: func(_ context.Context, _, _, _ string) ([]byte, error) {
			return makeCategoryTaxonomy("electronics"), nil
		},
	}

	srv := cataloggrpc.NewServerForTest(memStore, git)

	// First admission
	_, err := srv.AdmitResources(context.Background(), &catalogv1.AdmitResourcesRequest{
		RepositoryId: "repo-1",
		CommitSha:    strings.Repeat("a", 40),
		RefName:      "refs/heads/main",
	})
	require.NoError(t, err)

	c1, err := memStore.GetCategoryTaxonomyByName(context.Background(), "gitstore", "electronics")
	require.NoError(t, err)
	uid1 := c1.UID
	gen1 := c1.Generation

	// Second admission — update
	_, err = srv.AdmitResources(context.Background(), &catalogv1.AdmitResourcesRequest{
		RepositoryId: "repo-1",
		CommitSha:    strings.Repeat("b", 40),
		RefName:      "refs/heads/main",
	})
	require.NoError(t, err)

	c2, err := memStore.GetCategoryTaxonomyByName(context.Background(), "gitstore", "electronics")
	require.NoError(t, err)
	assert.Equal(t, uid1, c2.UID, "UID must be preserved on update")
	assert.Greater(t, c2.Generation, gen1, "generation must be incremented")
}

func TestAdmitResources_CategoryTaxonomy_AdmissionAcceptedCondition(t *testing.T) {
	memStore := newTestDatastore(t)
	git := &mockGitReader{
		listFilesFunc: func(_ context.Context, _, _, _ string) ([]string, error) {
			return []string{"categories/electronics.md"}, nil
		},
		readFileFunc: func(_ context.Context, _, _, _ string) ([]byte, error) {
			return makeCategoryTaxonomy("electronics"), nil
		},
	}

	srv := cataloggrpc.NewServerForTest(memStore, git)
	_, err := srv.AdmitResources(context.Background(), &catalogv1.AdmitResourcesRequest{
		RepositoryId: "repo-1",
		CommitSha:    strings.Repeat("a", 40),
		RefName:      "refs/heads/main",
	})
	require.NoError(t, err)

	c, err := memStore.GetCategoryTaxonomyByName(context.Background(), "gitstore", "electronics")
	require.NoError(t, err)

	var status struct {
		Conditions []struct {
			Type   string `json:"type"`
			Status string `json:"status"`
		} `json:"conditions"`
	}
	require.NoError(t, json.Unmarshal(c.Status, &status))
	var found bool
	for _, cond := range status.Conditions {
		if cond.Type == "AdmissionAccepted" && cond.Status == "True" {
			found = true
			break
		}
	}
	assert.True(t, found, "AdmissionAccepted: True condition must be set")
}

func TestAdmitResources_CategoryTaxonomy_RootAncestorPath(t *testing.T) {
	memStore := newTestDatastore(t)
	git := &mockGitReader{
		listFilesFunc: func(_ context.Context, _, _, _ string) ([]string, error) {
			return []string{"categories/electronics.md"}, nil
		},
		readFileFunc: func(_ context.Context, _, _, _ string) ([]byte, error) {
			return makeCategoryTaxonomy("electronics"), nil
		},
	}

	srv := cataloggrpc.NewServerForTest(memStore, git)
	_, err := srv.AdmitResources(context.Background(), &catalogv1.AdmitResourcesRequest{
		RepositoryId: "repo-1",
		CommitSha:    strings.Repeat("a", 40),
		RefName:      "refs/heads/main",
	})
	require.NoError(t, err)

	c, err := memStore.GetCategoryTaxonomyByName(context.Background(), "gitstore", "electronics")
	require.NoError(t, err)
	assert.Equal(t, "electronics", c.AncestorPath, "root category AncestorPath must equal its own name")
	assert.Equal(t, "", c.ParentName, "root category ParentName must be empty")
}

// ---------------------------------------------------------------------------
// T029: Cycle detection
// ---------------------------------------------------------------------------

func makeCategoryTaxonomyWithParent(name, parentName string) []byte {
	return []byte(`---
apiVersion: catalog.gitstore.dev/v1beta1
kind: CategoryTaxonomy
metadata:
  name: ` + name + `
  namespace: gitstore
spec:
  title: ` + name + `
  parentRef:
    name: ` + parentName + `
---
`)
}

func TestAdmitResources_IntraPushCycle_BothStoredWithAcyclicFalse(t *testing.T) {
	memStore := newTestDatastore(t)
	files := map[string][]byte{
		"categories/a.md": makeCategoryTaxonomyWithParent("a", "b"),
		"categories/b.md": makeCategoryTaxonomyWithParent("b", "a"),
	}
	git := &mockGitReader{
		listFilesFunc: func(_ context.Context, _, _, _ string) ([]string, error) {
			return []string{"categories/a.md", "categories/b.md"}, nil
		},
		readFileFunc: func(_ context.Context, _, path, _ string) ([]byte, error) {
			return files[path], nil
		},
	}

	srv := cataloggrpc.NewServerForTest(memStore, git)
	_, err := srv.AdmitResources(context.Background(), &catalogv1.AdmitResourcesRequest{
		RepositoryId: "repo-1",
		CommitSha:    strings.Repeat("a", 40),
		RefName:      "refs/heads/main",
	})
	require.NoError(t, err)

	for _, name := range []string{"a", "b"} {
		c, err := memStore.GetCategoryTaxonomyByName(context.Background(), "gitstore", name)
		require.NoError(t, err, "category %q must be stored", name)

		var status struct {
			Conditions []struct {
				Type   string `json:"type"`
				Status string `json:"status"`
			} `json:"conditions"`
		}
		require.NoError(t, json.Unmarshal(c.Status, &status))
		var acyclicCond *struct {
			Type   string `json:"type"`
			Status string `json:"status"`
		}
		for i := range status.Conditions {
			if status.Conditions[i].Type == "Acyclic" {
				acyclicCond = &status.Conditions[i]
				break
			}
		}
		require.NotNil(t, acyclicCond, "category %q must have Acyclic condition", name)
		assert.Equal(t, "False", acyclicCond.Status, "category %q in a cycle must have Acyclic=False", name)
	}
}

func TestAdmitResources_ValidChain_BothStoredWithAcyclicTrue(t *testing.T) {
	memStore := newTestDatastore(t)
	files := map[string][]byte{
		"categories/a.md": makeCategoryTaxonomy("a"),
		"categories/b.md": makeCategoryTaxonomyWithParent("b", "a"),
	}
	git := &mockGitReader{
		listFilesFunc: func(_ context.Context, _, _, _ string) ([]string, error) {
			return []string{"categories/a.md", "categories/b.md"}, nil
		},
		readFileFunc: func(_ context.Context, _, path, _ string) ([]byte, error) {
			return files[path], nil
		},
	}

	srv := cataloggrpc.NewServerForTest(memStore, git)
	_, err := srv.AdmitResources(context.Background(), &catalogv1.AdmitResourcesRequest{
		RepositoryId: "repo-1",
		CommitSha:    strings.Repeat("a", 40),
		RefName:      "refs/heads/main",
	})
	require.NoError(t, err)

	for _, name := range []string{"a", "b"} {
		c, err := memStore.GetCategoryTaxonomyByName(context.Background(), "gitstore", name)
		require.NoError(t, err)

		var status struct {
			Conditions []struct {
				Type   string `json:"type"`
				Status string `json:"status"`
			} `json:"conditions"`
		}
		require.NoError(t, json.Unmarshal(c.Status, &status))
		var acyclicCond *struct {
			Type   string `json:"type"`
			Status string `json:"status"`
		}
		for i := range status.Conditions {
			if status.Conditions[i].Type == "Acyclic" {
				acyclicCond = &status.Conditions[i]
				break
			}
		}
		require.NotNil(t, acyclicCond, "category %q must have Acyclic condition", name)
		assert.Equal(t, "True", acyclicCond.Status, "category %q in valid chain must have Acyclic=True", name)
	}
}

// ---------------------------------------------------------------------------
// T030: Ancestor path computation
// ---------------------------------------------------------------------------

func TestAdmitResources_RootCategory_AncestorPathEqualsName(t *testing.T) {
	memStore := newTestDatastore(t)
	git := &mockGitReader{
		listFilesFunc: func(_ context.Context, _, _, _ string) ([]string, error) {
			return []string{"categories/electronics.md"}, nil
		},
		readFileFunc: func(_ context.Context, _, _, _ string) ([]byte, error) {
			return makeCategoryTaxonomy("electronics"), nil
		},
	}
	srv := cataloggrpc.NewServerForTest(memStore, git)
	_, err := srv.AdmitResources(context.Background(), &catalogv1.AdmitResourcesRequest{
		RepositoryId: "repo-1",
		CommitSha:    strings.Repeat("a", 40),
		RefName:      "refs/heads/main",
	})
	require.NoError(t, err)

	c, err := memStore.GetCategoryTaxonomyByName(context.Background(), "gitstore", "electronics")
	require.NoError(t, err)
	assert.Equal(t, "electronics", c.AncestorPath)
}

func TestAdmitResources_ChildWithStoredParent_AncestorPathInherited(t *testing.T) {
	memStore := newTestDatastore(t)

	// First push: store parent
	git1 := &mockGitReader{
		listFilesFunc: func(_ context.Context, _, _, _ string) ([]string, error) {
			return []string{"categories/electronics.md"}, nil
		},
		readFileFunc: func(_ context.Context, _, _, _ string) ([]byte, error) {
			return makeCategoryTaxonomy("electronics"), nil
		},
	}
	srv := cataloggrpc.NewServerForTest(memStore, git1)
	_, err := srv.AdmitResources(context.Background(), &catalogv1.AdmitResourcesRequest{
		RepositoryId: "repo-1",
		CommitSha:    strings.Repeat("a", 40),
		RefName:      "refs/heads/main",
	})
	require.NoError(t, err)

	// Second push: child references stored parent
	git2 := &mockGitReader{
		listFilesFunc: func(_ context.Context, _, _, _ string) ([]string, error) {
			return []string{"categories/computers.md"}, nil
		},
		readFileFunc: func(_ context.Context, _, _, _ string) ([]byte, error) {
			return makeCategoryTaxonomyWithParent("computers", "electronics"), nil
		},
	}
	srv2 := cataloggrpc.NewServerForTest(memStore, git2)
	_, err = srv2.AdmitResources(context.Background(), &catalogv1.AdmitResourcesRequest{
		RepositoryId: "repo-1",
		CommitSha:    strings.Repeat("b", 40),
		RefName:      "refs/heads/main",
	})
	require.NoError(t, err)

	child, err := memStore.GetCategoryTaxonomyByName(context.Background(), "gitstore", "computers")
	require.NoError(t, err)
	assert.Equal(t, "electronics/computers", child.AncestorPath)
	assert.Equal(t, "electronics", child.ParentName)
}

func TestAdmitResources_CoCreation_ParentAndChildInSamePush(t *testing.T) {
	memStore := newTestDatastore(t)
	files := map[string][]byte{
		"categories/electronics.md": makeCategoryTaxonomy("electronics"),
		"categories/computers.md":   makeCategoryTaxonomyWithParent("computers", "electronics"),
	}
	git := &mockGitReader{
		listFilesFunc: func(_ context.Context, _, _, _ string) ([]string, error) {
			return []string{"categories/electronics.md", "categories/computers.md"}, nil
		},
		readFileFunc: func(_ context.Context, _, path, _ string) ([]byte, error) {
			return files[path], nil
		},
	}
	srv := cataloggrpc.NewServerForTest(memStore, git)
	_, err := srv.AdmitResources(context.Background(), &catalogv1.AdmitResourcesRequest{
		RepositoryId: "repo-1",
		CommitSha:    strings.Repeat("a", 40),
		RefName:      "refs/heads/main",
	})
	require.NoError(t, err)

	child, err := memStore.GetCategoryTaxonomyByName(context.Background(), "gitstore", "computers")
	require.NoError(t, err)
	assert.Equal(t, "electronics/computers", child.AncestorPath)
}

func TestAdmitResources_ChildWithMissingParent_TentativeRoot_ParentResolvedFalse(t *testing.T) {
	memStore := newTestDatastore(t)
	git := &mockGitReader{
		listFilesFunc: func(_ context.Context, _, _, _ string) ([]string, error) {
			return []string{"categories/computers.md"}, nil
		},
		readFileFunc: func(_ context.Context, _, _, _ string) ([]byte, error) {
			return makeCategoryTaxonomyWithParent("computers", "electronics"), nil
		},
	}
	srv := cataloggrpc.NewServerForTest(memStore, git)
	_, err := srv.AdmitResources(context.Background(), &catalogv1.AdmitResourcesRequest{
		RepositoryId: "repo-1",
		CommitSha:    strings.Repeat("a", 40),
		RefName:      "refs/heads/main",
	})
	require.NoError(t, err)

	c, err := memStore.GetCategoryTaxonomyByName(context.Background(), "gitstore", "computers")
	require.NoError(t, err)
	// Parent not found → tentative root, AncestorPath == name
	assert.Equal(t, "computers", c.AncestorPath)

	var status struct {
		Conditions []struct {
			Type   string `json:"type"`
			Status string `json:"status"`
		} `json:"conditions"`
	}
	require.NoError(t, json.Unmarshal(c.Status, &status))
	for _, cond := range status.Conditions {
		if cond.Type == "ParentResolved" {
			assert.Equal(t, "False", cond.Status)
			return
		}
	}
	t.Fatal("ParentResolved condition not found")
}

// ---------------------------------------------------------------------------
// T035: ValidateResources — Product single-category constraint
// ---------------------------------------------------------------------------

func TestValidateResources_Product_SingleCategoryRef_Accepted(t *testing.T) {
	blob := `---
apiVersion: catalog.gitstore.dev/v1beta1
kind: Product
metadata:
  name: widget
  namespace: gitstore
spec:
  title: Widget
  categoryRef:
    name: electronics
    kind: CategoryTaxonomy
---

body
`
	srv := cataloggrpc.NewServer(nil, nil)
	resp, err := srv.ValidateResources(context.Background(), &catalogv1.ValidateResourcesRequest{
		RepositoryId: "repo-1",
		Blobs:        []*catalogv1.ResourceBlob{{Path: "products/widget.md", Content: []byte(blob)}},
	})
	require.NoError(t, err)
	assert.True(t, resp.Accepted)
	assert.Empty(t, resp.Errors)
}

func TestValidateResources_Product_CategoryRefArray_Rejected(t *testing.T) {
	// YAML sequence cannot unmarshal into *ObjectReference — type mismatch.
	blob := `---
apiVersion: catalog.gitstore.dev/v1beta1
kind: Product
metadata:
  name: widget
  namespace: gitstore
spec:
  categoryRef:
    - name: electronics
    - name: computers
---
body
`
	srv := cataloggrpc.NewServer(nil, nil)
	resp, err := srv.ValidateResources(context.Background(), &catalogv1.ValidateResourcesRequest{
		RepositoryId: "repo-1",
		Blobs:        []*catalogv1.ResourceBlob{{Path: "products/widget.md", Content: []byte(blob)}},
	})
	require.NoError(t, err)
	assert.False(t, resp.Accepted)
	assert.NotEmpty(t, resp.Errors)
}

func TestValidateResources_Product_CategoryRefEmptyName_Rejected(t *testing.T) {
	blob := `---
apiVersion: catalog.gitstore.dev/v1beta1
kind: Product
metadata:
  name: widget
  namespace: gitstore
spec:
  categoryRef:
    kind: CategoryTaxonomy
---
body
`
	srv := cataloggrpc.NewServer(nil, nil)
	resp, err := srv.ValidateResources(context.Background(), &catalogv1.ValidateResourcesRequest{
		RepositoryId: "repo-1",
		Blobs:        []*catalogv1.ResourceBlob{{Path: "products/widget.md", Content: []byte(blob)}},
	})
	require.NoError(t, err)
	assert.False(t, resp.Accepted)
	require.NotEmpty(t, resp.Errors)
	assert.Contains(t, strings.ToLower(resp.Errors[0].Message), "categoryref.name")
}

// ---------------------------------------------------------------------------
// T038: AdmitResources — CategoryTaxonomy media admission
// ---------------------------------------------------------------------------

func TestAdmitResources_CategoryTaxonomy_MediaPreservedInSpec(t *testing.T) {
	const blob = `---
apiVersion: catalog.gitstore.dev/v1beta1
kind: CategoryTaxonomy
metadata:
  name: electronics
spec:
  title: Electronics
  media:
    - fileRef:
        name: electronics-hero
        kind: ImageFile
---
body
`
	store := newTestDatastore(t)
	git := &mockGitReader{
		listFilesFunc: func(_ context.Context, _, _, _ string) ([]string, error) {
			return []string{"categories/electronics.md"}, nil
		},
		readFileFunc: func(_ context.Context, _, _ string, _ string) ([]byte, error) {
			return []byte(blob), nil
		},
	}
	srv := cataloggrpc.NewServerForTest(store, git)
	_, err := srv.AdmitResources(context.Background(), &catalogv1.AdmitResourcesRequest{
		RepositoryId: "repo-1",
		CommitSha:    "abc123",
		RefName:      "refs/heads/main",
	})
	require.NoError(t, err)

	c, err := store.GetCategoryTaxonomyByName(context.Background(), "repo-1", "electronics")
	require.NoError(t, err)
	require.NotNil(t, c)

	// Media must be present in the stored Spec JSON blob.
	var spec struct {
		Media []struct {
			FileRef struct {
				Name string `json:"name"`
				Kind string `json:"kind"`
			} `json:"fileRef"`
		} `json:"media"`
	}
	require.NoError(t, json.Unmarshal(c.Spec, &spec))
	require.Len(t, spec.Media, 1)
	assert.Equal(t, "electronics-hero", spec.Media[0].FileRef.Name)
	assert.Equal(t, "ImageFile", spec.Media[0].FileRef.Kind)
}

func TestAdmitResources_CategoryTaxonomy_RequiredMediaAdmitted(t *testing.T) {
	// optional:false media is admitted without push rejection — File existence
	// check is deferred to controller (GH#244).
	const blob = `---
apiVersion: catalog.gitstore.dev/v1beta1
kind: CategoryTaxonomy
metadata:
  name: electronics
spec:
  title: Electronics
  media:
    - fileRef:
        name: required-hero
        kind: ImageFile
        optional: false
---
body
`
	store := newTestDatastore(t)
	git := &mockGitReader{
		listFilesFunc: func(_ context.Context, _, _, _ string) ([]string, error) {
			return []string{"categories/electronics.md"}, nil
		},
		readFileFunc: func(_ context.Context, _, _ string, _ string) ([]byte, error) {
			return []byte(blob), nil
		},
	}
	srv := cataloggrpc.NewServerForTest(store, git)
	_, err := srv.AdmitResources(context.Background(), &catalogv1.AdmitResourcesRequest{
		RepositoryId: "repo-1",
		CommitSha:    "abc123",
		RefName:      "refs/heads/main",
	})
	require.NoError(t, err)

	c, err := store.GetCategoryTaxonomyByName(context.Background(), "repo-1", "electronics")
	require.NoError(t, err)
	assert.NotNil(t, c)
}

func TestAdmitResources_CategoryTaxonomy_OptionalMediaAdmitted(t *testing.T) {
	const blob = `---
apiVersion: catalog.gitstore.dev/v1beta1
kind: CategoryTaxonomy
metadata:
  name: electronics
spec:
  title: Electronics
  media:
    - fileRef:
        name: optional-hero
        kind: ImageFile
        optional: true
---
body
`
	store := newTestDatastore(t)
	git := &mockGitReader{
		listFilesFunc: func(_ context.Context, _, _, _ string) ([]string, error) {
			return []string{"categories/electronics.md"}, nil
		},
		readFileFunc: func(_ context.Context, _, _ string, _ string) ([]byte, error) {
			return []byte(blob), nil
		},
	}
	srv := cataloggrpc.NewServerForTest(store, git)
	_, err := srv.AdmitResources(context.Background(), &catalogv1.AdmitResourcesRequest{
		RepositoryId: "repo-1",
		CommitSha:    "abc123",
		RefName:      "refs/heads/main",
	})
	require.NoError(t, err)

	c, err := store.GetCategoryTaxonomyByName(context.Background(), "repo-1", "electronics")
	require.NoError(t, err)
	assert.NotNil(t, c)
}

// ---------------------------------------------------------------------------
// Test helpers
// ---------------------------------------------------------------------------

func newTestDatastore(t *testing.T) datastore.Datastore {
	t.Helper()
	store, err := memdb.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	return store
}

// ensure bytes import is used
var _ = bytes.NewReader
