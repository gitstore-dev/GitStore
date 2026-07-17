// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package githttp

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	gitv1 "github.com/gitstore-dev/gitstore/api/gen/gitstore/git/v1"
	"github.com/gitstore-dev/gitstore/api/internal/auth"
	"github.com/gitstore-dev/gitstore/api/internal/auth/provider/anonymous"
	"github.com/gitstore-dev/gitstore/api/internal/catalog"
	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	"github.com/gitstore-dev/gitstore/api/internal/gitclient"
	"github.com/gitstore-dev/gitstore/api/internal/middleware/security"
	apiruntime "github.com/gitstore-dev/gitstore/api/internal/runtime"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

// mockGitClient is a test double for GitClient.
type mockGitClient struct {
	infoRefsFunc    func(ctx context.Context, repoID string, service gitv1.Service) ([]byte, gitv1.Service, error)
	uploadPackFunc  func(ctx context.Context, repoID string, body []byte) (io.Reader, error)
	receivePackFunc func(ctx context.Context, repoID string, body io.Reader) ([]byte, error)
}

func TestMain(m *testing.M) {
	gin.SetMode(gin.TestMode)
	os.Exit(m.Run())
}

func newTestRegistry(t *testing.T) *auth.ProviderRegistry {
	t.Helper()

	registry := auth.NewProviderRegistry(
		auth.NewChainedAuthN(anonymous.New()),
		nil,
		nil,
	)
	return registry
}

func (m *mockGitClient) InfoRefs(ctx context.Context, repoID string, service gitv1.Service) ([]byte, gitv1.Service, error) {
	if m.infoRefsFunc != nil {
		return m.infoRefsFunc(ctx, repoID, service)
	}
	return nil, gitv1.Service_SERVICE_UNSPECIFIED, errors.New("not set up")
}

func (m *mockGitClient) UploadPack(ctx context.Context, repoID string, body []byte) (io.Reader, error) {
	if m.uploadPackFunc != nil {
		return m.uploadPackFunc(ctx, repoID, body)
	}
	return nil, errors.New("not set up")
}

func (m *mockGitClient) ReceivePack(ctx context.Context, repoID string, body io.Reader) ([]byte, error) {
	if m.receivePackFunc != nil {
		return m.receivePackFunc(ctx, repoID, body)
	}
	return nil, errors.New("not set up")
}

// mockResolver is an alias for RepoResolverFunc, used in tests to simulate (namespace, repo) → repo_id lookup.
type mockResolver = RepoResolverFunc

type requestContextKey struct{}

func requestWithContextMarker(req *http.Request) (*http.Request, string) {
	const marker = "request-context-marker"
	return req.WithContext(context.WithValue(req.Context(), requestContextKey{}, marker)), marker
}

func assertRequestContext(t *testing.T, ctx context.Context, want string) {
	t.Helper()
	if got, _ := ctx.Value(requestContextKey{}).(string); got != want {
		t.Fatalf("expected request context marker %q, got %q", want, got)
	}
}

// T006: infoRefsHandler — upload-pack advertisement
func TestInfoRefsHandler_UploadPack(t *testing.T) {
	advertisement := []byte("001e# service=git-upload-pack\n0000")
	client := &mockGitClient{
		infoRefsFunc: func(_ context.Context, _ string, svc gitv1.Service) ([]byte, gitv1.Service, error) {
			if svc != gitv1.Service_SERVICE_GIT_UPLOAD_PACK {
				t.Errorf("expected GIT_UPLOAD_PACK, got %v", svc)
			}
			return advertisement, gitv1.Service_SERVICE_GIT_UPLOAD_PACK, nil
		},
	}
	resolver := mockResolver(func(ns, repo string) (string, bool) {
		return "test-repo-id", true
	})
	registry := newTestRegistry(t)
	router := NewMux(SmartHttpDeps{
		GitClient:        client,
		RepoResolverFunc: resolver,
		Logger:           zap.NewNop(),
		Ids:              apiruntime.NewSequenceIDGenerator(),
		Registry:         registry,
	})

	req := httptest.NewRequest(http.MethodGet, "/gitstore/catalog/info/refs?service=git-upload-pack", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	resp := w.Result()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200, got %d", resp.StatusCode)
	}
	ct := resp.Header.Get("Content-Type")
	if ct != "application/x-git-upload-pack-advertisement" {
		t.Errorf("expected upload-pack Content-Type, got %q", ct)
	}
	body, _ := io.ReadAll(resp.Body)
	if !bytes.Contains(body, []byte("# service=git-upload-pack")) {
		t.Errorf("expected pkt-line service header in body, got: %q", body)
	}
}

// T006: infoRefsHandler — receive-pack advertisement
func TestInfoRefsHandler_ReceivePack(t *testing.T) {
	advertisement := []byte("001f# service=git-receive-pack\n0000")
	client := &mockGitClient{
		infoRefsFunc: func(_ context.Context, _ string, svc gitv1.Service) ([]byte, gitv1.Service, error) {
			return advertisement, gitv1.Service_SERVICE_GIT_RECEIVE_PACK, nil
		},
	}
	resolver := mockResolver(func(ns, repo string) (string, bool) {
		return "test-repo-id", true
	})
	registry := newTestRegistry(t)

	router := NewMux(SmartHttpDeps{
		GitClient:        client,
		RepoResolverFunc: resolver,
		Logger:           zap.NewNop(),
		Ids:              apiruntime.NewSequenceIDGenerator(),
		Registry:         registry,
	})
	req := httptest.NewRequest(http.MethodGet, "/gitstore/catalog/info/refs?service=git-receive-pack", nil)
	req.SetPathValue("namespace", "gitstore")
	req.SetPathValue("repo", "catalog")
	w := httptest.NewRecorder()

	router.ServeHTTP(w, req)

	resp := w.Result()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200, got %d", resp.StatusCode)
	}
	ct := resp.Header.Get("Content-Type")
	if ct != "application/x-git-receive-pack-advertisement" {
		t.Errorf("expected receive-pack Content-Type, got %q", ct)
	}
	body, _ := io.ReadAll(resp.Body)
	if !bytes.Contains(body, []byte("# service=git-receive-pack")) {
		t.Errorf("expected pkt-line service header in body, got: %q", body)
	}
}

// T007: uploadPackHandler — streams response without buffering
func TestUploadPackHandler_StreamsResponse(t *testing.T) {
	chunk1 := []byte("chunk-one-data")
	chunk2 := []byte("chunk-two-data")
	client := &mockGitClient{
		uploadPackFunc: func(_ context.Context, _ string, _ []byte) (io.Reader, error) {
			return io.MultiReader(bytes.NewReader(chunk1), bytes.NewReader(chunk2)), nil
		},
	}
	resolver := mockResolver(func(ns, repo string) (string, bool) {
		return "test-repo-id", true
	})
	registry := newTestRegistry(t)

	router := NewMux(SmartHttpDeps{
		GitClient:        client,
		RepoResolverFunc: resolver,
		Logger:           zap.NewNop(),
		Ids:              apiruntime.NewSequenceIDGenerator(),
		Registry:         registry,
	})
	body := strings.NewReader("0011want abc123\n0000")
	req := httptest.NewRequest(http.MethodPost, "/gitstore/catalog/git-upload-pack", body)
	w := httptest.NewRecorder()

	router.ServeHTTP(w, req)

	resp := w.Result()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200, got %d", resp.StatusCode)
	}
	ct := resp.Header.Get("Content-Type")
	if ct != "application/x-git-upload-pack-result" {
		t.Errorf("expected upload-pack-result Content-Type, got %q", ct)
	}
	if te := resp.Header.Get("Transfer-Encoding"); te != "" {
		t.Errorf("expected net/http to manage transfer encoding, got header %q", te)
	}
	respBody, _ := io.ReadAll(resp.Body)
	if !bytes.Contains(respBody, chunk1) || !bytes.Contains(respBody, chunk2) {
		t.Errorf("expected both chunks in response, got: %q", respBody)
	}
}

func TestHandler_PropagatesRequestContextToGitClient(t *testing.T) {
	resolver := mockResolver(func(ns, repo string) (string, bool) {
		return "test-repo-id", true
	})
	registry := newTestRegistry(t)

	t.Run("info refs", func(t *testing.T) {
		client := &mockGitClient{
			infoRefsFunc: func(ctx context.Context, _ string, svc gitv1.Service) ([]byte, gitv1.Service, error) {
				assertRequestContext(t, ctx, "request-context-marker")
				return []byte("001e# service=git-upload-pack\n0000"), svc, nil
			},
		}
		router := NewMux(SmartHttpDeps{
			GitClient:        client,
			RepoResolverFunc: resolver,
			Logger:           zap.NewNop(),
			Ids:              apiruntime.NewSequenceIDGenerator(),
			Registry:         registry,
		})
		req := httptest.NewRequest(http.MethodGet, "/gitstore/catalog/info/refs?service=git-upload-pack", nil)
		req, _ = requestWithContextMarker(req)
		w := httptest.NewRecorder()

		router.ServeHTTP(w, req)

		if resp := w.Result(); resp.StatusCode != http.StatusOK {
			t.Fatalf("expected 200, got %d", resp.StatusCode)
		}
	})

	t.Run("upload pack", func(t *testing.T) {
		client := &mockGitClient{
			uploadPackFunc: func(ctx context.Context, _ string, _ []byte) (io.Reader, error) {
				assertRequestContext(t, ctx, "request-context-marker")
				return bytes.NewReader([]byte("pack-result")), nil
			},
		}
		router := NewMux(SmartHttpDeps{
			GitClient:        client,
			RepoResolverFunc: resolver,
			Logger:           zap.NewNop(),
			Ids:              apiruntime.NewSequenceIDGenerator(),
			Registry:         registry,
		})
		req := httptest.NewRequest(http.MethodPost, "/gitstore/catalog/git-upload-pack", strings.NewReader("0011want abc123\n0000"))
		req, _ = requestWithContextMarker(req)
		w := httptest.NewRecorder()

		router.ServeHTTP(w, req)

		if resp := w.Result(); resp.StatusCode != http.StatusOK {
			t.Fatalf("expected 200, got %d", resp.StatusCode)
		}
	})

	t.Run("receive pack", func(t *testing.T) {
		client := &mockGitClient{
			receivePackFunc: func(ctx context.Context, _ string, _ io.Reader) ([]byte, error) {
				assertRequestContext(t, ctx, "request-context-marker")
				return []byte("report-status"), nil
			},
		}
		router := NewMux(SmartHttpDeps{
			GitClient:        client,
			RepoResolverFunc: resolver,
			Logger:           zap.NewNop(),
			Ids:              apiruntime.NewSequenceIDGenerator(),
			Registry:         registry,
		})
		req := httptest.NewRequest(http.MethodPost, "/gitstore/catalog/git-receive-pack", strings.NewReader("pack-body"))
		req, _ = requestWithContextMarker(req)
		w := httptest.NewRecorder()

		router.ServeHTTP(w, req)

		if resp := w.Result(); resp.StatusCode != http.StatusOK {
			t.Fatalf("expected 200, got %d", resp.StatusCode)
		}
	})
}

// T008: receivePackHandler — pipes request body to gRPC without full buffering
func TestReceivePackHandler_PipesBodyToGRPC(t *testing.T) {
	var receivedBytes []byte
	reportStatus := []byte("0014unpack ok\n00000000")
	client := &mockGitClient{
		receivePackFunc: func(_ context.Context, _ string, body io.Reader) ([]byte, error) {
			receivedBytes, _ = io.ReadAll(body)
			return reportStatus, nil
		},
	}
	resolver := mockResolver(func(ns, repo string) (string, bool) {
		return "test-repo-id", true
	})
	registry := newTestRegistry(t)

	packData := []byte("0053\x00\x00\x00\x00\x00\x00\x00\x00refs/heads/main\x00\x00PACK...")
	router := NewMux(SmartHttpDeps{
		GitClient:        client,
		RepoResolverFunc: resolver,
		Logger:           zap.NewNop(),
		Ids:              apiruntime.NewSequenceIDGenerator(),
		Registry:         registry,
	})
	req := httptest.NewRequest(http.MethodPost, "/gitstore/catalog/git-receive-pack", bytes.NewReader(packData))
	req.SetPathValue("namespace", "gitstore")
	req.SetPathValue("repo", "catalog")
	w := httptest.NewRecorder()

	router.ServeHTTP(w, req)

	resp := w.Result()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200, got %d", resp.StatusCode)
	}
	ct := resp.Header.Get("Content-Type")
	if ct != "application/x-git-receive-pack-result" {
		t.Errorf("expected receive-pack-result Content-Type, got %q", ct)
	}
	if !bytes.Equal(receivedBytes, packData) {
		t.Errorf("handler must pipe body bytes unchanged; got %d bytes, want %d", len(receivedBytes), len(packData))
	}
	respBody, _ := io.ReadAll(resp.Body)
	if !bytes.Equal(respBody, reportStatus) {
		t.Errorf("expected report-status in response, got: %q", respBody)
	}
}

// T009: unknown namespace/repo returns 404 with Git pkt-line error
func TestHandler_UnknownRepo_Returns404(t *testing.T) {
	client := &mockGitClient{}
	resolver := mockResolver(func(ns, repo string) (string, bool) {
		return "", false
	})
	registry := newTestRegistry(t)

	router := NewMux(SmartHttpDeps{
		GitClient:        client,
		RepoResolverFunc: resolver,
		Logger:           zap.NewNop(),
		Ids:              apiruntime.NewSequenceIDGenerator(),
		Registry:         registry,
	})
	req := httptest.NewRequest(http.MethodGet, "/unknown/repo/info/refs?service=git-upload-pack", nil)
	w := httptest.NewRecorder()

	router.ServeHTTP(w, req)

	resp := w.Result()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("expected 404, got %d", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if !bytes.Contains(body, []byte("ERR repository not found")) {
		t.Errorf("expected 'ERR repository not found' in body, got: %q", body)
	}
}

// stubStore is a minimal Datastore stub for handler tests.
// Only GetNamespaceByIdentifier and LookupRepository are implemented.
type stubStore struct {
	getNamespaceByIdentifier func(ctx context.Context, identifier string) (*datastore.Namespace, error)
	lookupRepository         func(ctx context.Context, namespaceID, name string) (*datastore.NamespaceMapping, error)
	getRepository            func(ctx context.Context, id string) (*datastore.Repository, error)
}

func (s *stubStore) GetNamespaceByIdentifier(ctx context.Context, identifier string) (*datastore.Namespace, error) {
	if s.getNamespaceByIdentifier != nil {
		return s.getNamespaceByIdentifier(ctx, identifier)
	}
	return nil, datastore.ErrNotFound
}
func (s *stubStore) LookupRepository(ctx context.Context, namespaceID, name string) (*datastore.NamespaceMapping, error) {
	if s.lookupRepository != nil {
		return s.lookupRepository(ctx, namespaceID, name)
	}
	return nil, datastore.ErrNotFound
}
func (s *stubStore) GetRepository(ctx context.Context, id string) (*datastore.Repository, error) {
	if s.getRepository != nil {
		return s.getRepository(ctx, id)
	}
	return nil, datastore.ErrNotFound
}

// Implement the full Datastore interface as no-ops.
func (s *stubStore) CreateProduct(_ context.Context, _ *datastore.Product) error { return nil }
func (s *stubStore) GetProduct(_ context.Context, _ string) (*datastore.Product, error) {
	return nil, datastore.ErrNotFound
}
func (s *stubStore) GetProductByName(_ context.Context, _, _ string) (*datastore.Product, error) {
	return nil, datastore.ErrNotFound
}
func (s *stubStore) ListProducts(_ context.Context, _ string, _ datastore.PageParams) (*datastore.PageResult[datastore.Product], error) {
	return &datastore.PageResult[datastore.Product]{}, nil
}
func (s *stubStore) UpdateProduct(_ context.Context, _ *datastore.Product) error { return nil }
func (s *stubStore) DeleteProduct(_ context.Context, _ string) error             { return nil }
func (s *stubStore) CreateCategoryTaxonomy(_ context.Context, _ *datastore.CategoryTaxonomy) error {
	return nil
}
func (s *stubStore) GetCategoryTaxonomy(_ context.Context, _ string) (*datastore.CategoryTaxonomy, error) {
	return nil, datastore.ErrNotFound
}
func (s *stubStore) GetCategoryTaxonomyByName(_ context.Context, _, _ string) (*datastore.CategoryTaxonomy, error) {
	return nil, datastore.ErrNotFound
}
func (s *stubStore) ListCategoryTaxonomies(_ context.Context, _ string, _ datastore.PageParams) (*datastore.PageResult[datastore.CategoryTaxonomy], error) {
	return &datastore.PageResult[datastore.CategoryTaxonomy]{}, nil
}
func (s *stubStore) UpdateCategoryTaxonomy(_ context.Context, _ *datastore.CategoryTaxonomy) error {
	return nil
}
func (s *stubStore) DeleteCategoryTaxonomy(_ context.Context, _ string) error { return nil }
func (s *stubStore) CreateProductVariant(_ context.Context, _ *datastore.ProductVariant) error {
	return nil
}
func (s *stubStore) GetProductVariant(_ context.Context, _ string) (*datastore.ProductVariant, error) {
	return nil, datastore.ErrNotFound
}
func (s *stubStore) GetProductVariantByName(_ context.Context, _, _ string) (*datastore.ProductVariant, error) {
	return nil, datastore.ErrNotFound
}
func (s *stubStore) GetProductVariantBySKU(_ context.Context, _, _ string) (*datastore.ProductVariant, error) {
	return nil, datastore.ErrNotFound
}
func (s *stubStore) ListProductVariants(_ context.Context, _ string, _ datastore.PageParams) (*datastore.PageResult[datastore.ProductVariant], error) {
	return &datastore.PageResult[datastore.ProductVariant]{}, nil
}
func (s *stubStore) ListProductVariantsByProductRef(_ context.Context, _, _ string) ([]*datastore.ProductVariant, error) {
	return nil, nil
}
func (s *stubStore) UpdateProductVariant(_ context.Context, _ *datastore.ProductVariant) error {
	return nil
}
func (s *stubStore) DeleteProductVariant(_ context.Context, _ string) error { return nil }
func (s *stubStore) CreateCollection(_ context.Context, _ *datastore.Collection) error {
	return nil
}
func (s *stubStore) GetCollection(_ context.Context, _ string) (*datastore.Collection, error) {
	return nil, datastore.ErrNotFound
}
func (s *stubStore) GetCollectionByName(_ context.Context, _, _ string) (*datastore.Collection, error) {
	return nil, datastore.ErrNotFound
}
func (s *stubStore) ListCollections(_ context.Context, _ string, _ datastore.PageParams) (*datastore.PageResult[datastore.Collection], error) {
	return &datastore.PageResult[datastore.Collection]{}, nil
}
func (s *stubStore) UpdateCollection(_ context.Context, _ *datastore.Collection) error { return nil }
func (s *stubStore) DeleteCollection(_ context.Context, _ string) error                { return nil }
func (s *stubStore) ListProductsByLabelSelector(_ context.Context, _ string, _ catalog.LabelSelector) ([]*datastore.Product, error) {
	return nil, nil
}
func (s *stubStore) CreateNamespace(_ context.Context, _ *datastore.Namespace) error { return nil }
func (s *stubStore) GetNamespace(_ context.Context, _ string) (*datastore.Namespace, error) {
	return nil, datastore.ErrNotFound
}
func (s *stubStore) ListNamespaces(_ context.Context, _ datastore.PageParams) (*datastore.PageResult[datastore.Namespace], error) {
	return &datastore.PageResult[datastore.Namespace]{}, nil
}
func (s *stubStore) DeleteNamespace(_ context.Context, _ string) error                 { return nil }
func (s *stubStore) CreateRepository(_ context.Context, _ *datastore.Repository) error { return nil }
func (s *stubStore) ListRepositoriesByNamespace(_ context.Context, _ string, _ datastore.PageParams) (*datastore.PageResult[datastore.Repository], error) {
	return &datastore.PageResult[datastore.Repository]{}, nil
}
func (s *stubStore) UpdateRepository(_ context.Context, _ *datastore.Repository) error { return nil }
func (s *stubStore) DeleteRepository(_ context.Context, _ string) error                { return nil }
func (s *stubStore) CreateNamespaceMapping(_ context.Context, _ *datastore.NamespaceMapping) error {
	return nil
}
func (s *stubStore) LookupNamespaceByRepoID(_ context.Context, _ string) (*datastore.NamespaceMapping, error) {
	return nil, datastore.ErrNotFound
}
func (s *stubStore) RenameRepository(_ context.Context, _, _, _ string) error    { return nil }
func (s *stubStore) TransferRepository(_ context.Context, _, _, _ string) error  { return nil }
func (s *stubStore) DeleteNamespaceMapping(_ context.Context, _, _ string) error { return nil }
func (s *stubStore) Close() error                                                { return nil }

// stubAuthZProvider is a minimal AuthZProvider for tests.
type stubAuthZProvider struct {
	decision auth.Decision
	err      error
}

func (s *stubAuthZProvider) Name() string { return "stub-authz" }
func (s *stubAuthZProvider) Authorize(_ context.Context, _ *auth.Principal, _ string, _ auth.ResourceContext) (auth.Decision, error) {
	return s.decision, s.err
}

// T021: RepoResolver returns 404 pkt-line for unknown namespace/repo.
func TestRepoResolverNotFound(t *testing.T) {
	store := &stubStore{} // both lookups return ErrNotFound by default

	router := NewMuxWithStore(SmartHttpDeps{
		GitClient:        &mockGitClient{},
		RepoResolverFunc: func(_, _ string) (string, bool) { return "", false },
		Store:            store,
		Logger:           zap.NewNop(),
		Ids:              apiruntime.NewSequenceIDGenerator(),
		Registry:         newTestRegistry(t),
	})

	req := httptest.NewRequest(http.MethodGet, "/unknown-ns/unknown-repo/info/refs?service=git-upload-pack", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	resp := w.Result()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("expected 404, got %d", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if !bytes.Contains(body, []byte("ERR repository not found")) {
		t.Errorf("expected ERR repository not found in body, got: %q", body)
	}
}

// T022: RepoResolver stores repoID in gin context for known repo.
func TestRepoResolverSetsContext(t *testing.T) {
	const wantRepoID = "01960000-0000-7000-8000-000000000001"
	store := &stubStore{
		getNamespaceByIdentifier: func(_ context.Context, id string) (*datastore.Namespace, error) {
			return &datastore.Namespace{ID: "ns-id-1", Identifier: id}, nil
		},
		lookupRepository: func(_ context.Context, _, _ string) (*datastore.NamespaceMapping, error) {
			return &datastore.NamespaceMapping{RepoID: wantRepoID}, nil
		},
	}

	var capturedRepoID string
	client := &mockGitClient{
		infoRefsFunc: func(ctx context.Context, repoID string, _ gitv1.Service) ([]byte, gitv1.Service, error) {
			capturedRepoID = repoID
			return []byte("001e# service=git-upload-pack\n0000"), gitv1.Service_SERVICE_GIT_UPLOAD_PACK, nil
		},
	}

	router := NewMuxWithStore(SmartHttpDeps{
		GitClient:        client,
		RepoResolverFunc: func(_, _ string) (string, bool) { return "", false }, // legacy resolver not used
		Store:            store,
		Logger:           zap.NewNop(),
		Ids:              apiruntime.NewSequenceIDGenerator(),
		Registry:         newTestRegistry(t),
	})

	req := httptest.NewRequest(http.MethodGet, "/gitstore/catalog/info/refs?service=git-upload-pack", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d — store lookup may have failed", w.Code)
	}
	if capturedRepoID != wantRepoID {
		t.Errorf("expected repoID %q propagated to git client, got %q", wantRepoID, capturedRepoID)
	}
}

// T023: read-only principal attempting receive-pack is denied 403.
func TestGitHttpAuthorizerReadOnly(t *testing.T) {
	readOnlyPrincipal := &auth.Principal{Subject: "reader", AuthMethod: "basic", Roles: []string{"reader"}}
	stubAuthN := &stubAuthNProviderWithPrincipal{principal: readOnlyPrincipal, decision: auth.Allow("stub", "ok")}
	stubAuthZ := &stubAuthZProvider{decision: auth.Deny("stub-authz", "no write permission"), err: nil}

	registry := auth.NewProviderRegistry(
		auth.NewChainedAuthN(stubAuthN),
		stubAuthZ,
		nil,
	)

	const wantRepoID = "01960000-0000-7000-8000-000000000001"
	store := &stubStore{
		getNamespaceByIdentifier: func(_ context.Context, id string) (*datastore.Namespace, error) {
			return &datastore.Namespace{ID: "ns-id-1", Identifier: id}, nil
		},
		lookupRepository: func(_ context.Context, _, _ string) (*datastore.NamespaceMapping, error) {
			return &datastore.NamespaceMapping{RepoID: wantRepoID}, nil
		},
	}

	router := NewMuxWithStore(SmartHttpDeps{
		GitClient:        &mockGitClient{},
		RepoResolverFunc: func(_, _ string) (string, bool) { return "", false },
		Store:            store,
		Logger:           zap.NewNop(),
		Ids:              apiruntime.NewSequenceIDGenerator(),
		Registry:         registry,
	})

	req := httptest.NewRequest(http.MethodPost, "/gitstore/catalog/git-receive-pack", strings.NewReader("pack"))
	req.SetBasicAuth("reader", "password")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusForbidden {
		t.Errorf("expected 403 for read-only principal on receive-pack, got %d", w.Code)
	}
}

// T024: write-capable principal on receive-pack passes through.
func TestGitHttpAuthorizerWriteAllowed(t *testing.T) {
	writePrincipal := &auth.Principal{Subject: "writer", AuthMethod: "basic", Roles: []string{"writer"}}
	stubAuthN := &stubAuthNProviderWithPrincipal{principal: writePrincipal, decision: auth.Allow("stub", "ok")}
	stubAuthZ := &stubAuthZProvider{decision: auth.Allow("stub-authz", "write granted"), err: nil}

	registry := auth.NewProviderRegistry(
		auth.NewChainedAuthN(stubAuthN),
		stubAuthZ,
		nil,
	)

	const wantRepoID = "01960000-0000-7000-8000-000000000001"
	store := &stubStore{
		getNamespaceByIdentifier: func(_ context.Context, id string) (*datastore.Namespace, error) {
			return &datastore.Namespace{ID: "ns-id-1", Identifier: id}, nil
		},
		lookupRepository: func(_ context.Context, _, _ string) (*datastore.NamespaceMapping, error) {
			return &datastore.NamespaceMapping{RepoID: wantRepoID}, nil
		},
	}

	client := &mockGitClient{
		receivePackFunc: func(_ context.Context, _ string, _ io.Reader) ([]byte, error) {
			return []byte("0014unpack ok\n00000000"), nil
		},
	}

	router := NewMuxWithStore(SmartHttpDeps{
		GitClient:        client,
		RepoResolverFunc: func(_, _ string) (string, bool) { return "", false },
		Store:            store,
		Logger:           zap.NewNop(),
		Ids:              apiruntime.NewSequenceIDGenerator(),
		Registry:         registry,
	})

	req := httptest.NewRequest(http.MethodPost, "/gitstore/catalog/git-receive-pack", strings.NewReader("pack"))
	req.SetBasicAuth("writer", "password")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200 for write-capable principal on receive-pack, got %d", w.Code)
	}
}

// T025: GitHttpAuthorizer without RepoResolver having run returns 500.
// This test exercises the middleware directly without RepoResolver in chain.
func TestGitHttpAuthorizerMissingContext(t *testing.T) {
	stubAuthZ := &stubAuthZProvider{decision: auth.Allow("stub-authz", "ok"), err: nil}
	registry := auth.NewProviderRegistry(
		auth.NewChainedAuthN(anonymous.New()),
		stubAuthZ,
		nil,
	)

	authMiddleware := security.NewAuthorize(registry, zap.NewNop())
	r := gin.New()
	r.POST("/:namespace/:repo/git-receive-pack", authMiddleware.GitHttpAuthorizer, func(c *gin.Context) {
		c.Status(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodPost, "/gitstore/catalog/git-receive-pack", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusInternalServerError {
		t.Errorf("expected 500 when repoID not set in context, got %d", w.Code)
	}
}

// stubAuthNProviderWithPrincipal always returns a fixed principal.
type stubAuthNProviderWithPrincipal struct {
	principal *auth.Principal
	decision  auth.Decision
}

func (s *stubAuthNProviderWithPrincipal) Name() string { return "stub-authn" }
func (s *stubAuthNProviderWithPrincipal) Capabilities() auth.Capability {
	return auth.CapAuthenticate
}
func (s *stubAuthNProviderWithPrincipal) Authenticate(_ context.Context, _ auth.AuthRequest) (*auth.Principal, auth.Decision, error) {
	return s.principal, s.decision, nil
}
func (s *stubAuthNProviderWithPrincipal) RevokeSession(_ context.Context, _ string, _ time.Time) error {
	return nil
}
func (s *stubAuthNProviderWithPrincipal) RefreshSession(_ context.Context, _ string) (string, time.Time, error) {
	return "", time.Time{}, auth.ErrNotSupported
}
func (s *stubAuthNProviderWithPrincipal) IssueSession(_ context.Context, _ string) (string, time.Time, error) {
	return "", time.Time{}, auth.ErrNotSupported
}

// T036: receivePackHandler passes PushContext (stored in gin context by PushContextInserter) to ReceivePack.
func TestReceivePackAttachesPushContext(t *testing.T) {
	const repoID = "01960000-0000-7000-8000-000000000001"
	const nsID = "ns-id-1"

	store := &stubStore{
		getNamespaceByIdentifier: func(_ context.Context, id string) (*datastore.Namespace, error) {
			return &datastore.Namespace{ID: nsID, Identifier: id}, nil
		},
		lookupRepository: func(_ context.Context, _, _ string) (*datastore.NamespaceMapping, error) {
			return &datastore.NamespaceMapping{RepoID: repoID}, nil
		},
		getRepository: func(_ context.Context, _ string) (*datastore.Repository, error) {
			return &datastore.Repository{
				ID:               repoID,
				NamespaceID:      nsID,
				Name:             "catalog",
				MaxPackSizeBytes: 52428800,
				MaxFileSizeBytes: 10485760,
			}, nil
		},
	}

	var capturedCtx context.Context
	client := &mockGitClient{
		receivePackFunc: func(ctx context.Context, _ string, _ io.Reader) ([]byte, error) {
			capturedCtx = ctx
			return []byte("0014unpack ok\n00000000"), nil
		},
	}

	writePrincipal := &auth.Principal{Subject: "writer", AuthMethod: "basic", Roles: []string{"writer"}}
	stubAuthN := &stubAuthNProviderWithPrincipal{principal: writePrincipal, decision: auth.Allow("stub", "ok")}
	stubAuthZ := &stubAuthZProvider{decision: auth.Allow("stub-authz", "write ok"), err: nil}

	registry := auth.NewProviderRegistry(
		auth.NewChainedAuthN(stubAuthN),
		stubAuthZ,
		nil,
	)

	router := NewMuxWithStoreAndAuthz(SmartHttpDeps{
		GitClient:        client,
		RepoResolverFunc: func(_, _ string) (string, bool) { return "", false },
		Store:            store,
		Logger:           zap.NewNop(),
		Ids:              apiruntime.NewSequenceIDGenerator(),
		Registry:         registry,
	})

	req := httptest.NewRequest(http.MethodPost, "/gitstore/catalog/git-receive-pack", strings.NewReader("pack-body"))
	req.SetBasicAuth("writer", "password")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	require.NotNil(t, capturedCtx, "ReceivePack must have been called")
	pc := gitclient.PushContextFromContext(capturedCtx)
	require.NotNil(t, pc, "PushContext must be in context passed to ReceivePack")
	assert.Equal(t, repoID, pc.RepositoryId)
	assert.Equal(t, "writer", pc.Actor.Subject)
	assert.Equal(t, int64(52428800), pc.Policy.MaxPackSizeBytes)
}

// T010: gRPC unavailability returns 503 with Git pkt-line error, no retry
func TestHandler_GRPCUnavailable_Returns503(t *testing.T) {
	callCount := 0
	client := &mockGitClient{
		infoRefsFunc: func(_ context.Context, _ string, _ gitv1.Service) ([]byte, gitv1.Service, error) {
			callCount++
			return nil, gitv1.Service_SERVICE_UNSPECIFIED, errors.New("connection refused")
		},
	}
	resolver := mockResolver(func(ns, repo string) (string, bool) {
		return "test-repo-id", true
	})
	registry := newTestRegistry(t)

	router := NewMux(SmartHttpDeps{
		GitClient:        client,
		RepoResolverFunc: resolver,
		Logger:           zap.NewNop(),
		Ids:              apiruntime.NewSequenceIDGenerator(),
		Registry:         registry,
	})
	req := httptest.NewRequest(http.MethodGet, "/gitstore/catalog/info/refs?service=git-upload-pack", nil)
	w := httptest.NewRecorder()

	router.ServeHTTP(w, req)

	resp := w.Result()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("expected 503, got %d", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	defer func(Body io.ReadCloser) {
		err := Body.Close()
		if err != nil {
			t.Error(err)
		}
	}(resp.Body)
	if !bytes.Contains(body, []byte("ERR service unavailable")) {
		t.Errorf("expected 'ERR service unavailable' in body, got: %q", body)
	}
	if callCount != 1 {
		t.Errorf("expected exactly 1 attempt (no retry), got %d", callCount)
	}
}
