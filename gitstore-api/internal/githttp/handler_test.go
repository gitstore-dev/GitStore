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
	"strings"
	"testing"

	gitv1 "github.com/gitstore-dev/gitstore/api/gen/gitstore/git/v1"
)

// mockGitClient is a test double for GitClient.
type mockGitClient struct {
	infoRefsFunc    func(ctx context.Context, repoID string, service gitv1.Service) ([]byte, gitv1.Service, error)
	uploadPackFunc  func(ctx context.Context, repoID string, body []byte) (io.Reader, error)
	receivePackFunc func(ctx context.Context, repoID string, body io.Reader) ([]byte, error)
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

// mockResolver is an alias for RepoResolver, used in tests to simulate (namespace, repo) → repo_id lookup.
type mockResolver = RepoResolver

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

	h := newHandler(client, resolver)
	req := httptest.NewRequest(http.MethodGet, "/gitstore/catalog/info/refs?service=git-upload-pack", nil)
	req.SetPathValue("namespace", "gitstore")
	req.SetPathValue("repo", "catalog")
	w := httptest.NewRecorder()

	h.infoRefsHandler(w, req)

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

	h := newHandler(client, resolver)
	req := httptest.NewRequest(http.MethodGet, "/gitstore/catalog/info/refs?service=git-receive-pack", nil)
	req.SetPathValue("namespace", "gitstore")
	req.SetPathValue("repo", "catalog")
	w := httptest.NewRecorder()

	h.infoRefsHandler(w, req)

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

	h := newHandler(client, resolver)
	body := strings.NewReader("0011want abc123\n0000")
	req := httptest.NewRequest(http.MethodPost, "/gitstore/catalog/git-upload-pack", body)
	req.SetPathValue("namespace", "gitstore")
	req.SetPathValue("repo", "catalog")
	w := httptest.NewRecorder()

	h.uploadPackHandler(w, req)

	resp := w.Result()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200, got %d", resp.StatusCode)
	}
	ct := resp.Header.Get("Content-Type")
	if ct != "application/x-git-upload-pack-result" {
		t.Errorf("expected upload-pack-result Content-Type, got %q", ct)
	}
	respBody, _ := io.ReadAll(resp.Body)
	if !bytes.Contains(respBody, chunk1) || !bytes.Contains(respBody, chunk2) {
		t.Errorf("expected both chunks in response, got: %q", respBody)
	}
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

	packData := []byte("0053\x00\x00\x00\x00\x00\x00\x00\x00refs/heads/main\x00\x00PACK...")
	h := newHandler(client, resolver)
	req := httptest.NewRequest(http.MethodPost, "/gitstore/catalog/git-receive-pack", bytes.NewReader(packData))
	req.SetPathValue("namespace", "gitstore")
	req.SetPathValue("repo", "catalog")
	w := httptest.NewRecorder()

	h.receivePackHandler(w, req)

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

	h := newHandler(client, resolver)
	req := httptest.NewRequest(http.MethodGet, "/unknown/repo/info/refs?service=git-upload-pack", nil)
	req.SetPathValue("namespace", "unknown")
	req.SetPathValue("repo", "repo")
	w := httptest.NewRecorder()

	h.infoRefsHandler(w, req)

	resp := w.Result()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("expected 404, got %d", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if !bytes.Contains(body, []byte("ERR repository not found")) {
		t.Errorf("expected 'ERR repository not found' in body, got: %q", body)
	}
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

	h := newHandler(client, resolver)
	req := httptest.NewRequest(http.MethodGet, "/gitstore/catalog/info/refs?service=git-upload-pack", nil)
	req.SetPathValue("namespace", "gitstore")
	req.SetPathValue("repo", "catalog")
	w := httptest.NewRecorder()

	h.infoRefsHandler(w, req)

	resp := w.Result()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("expected 503, got %d", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if !bytes.Contains(body, []byte("ERR service unavailable")) {
		t.Errorf("expected 'ERR service unavailable' in body, got: %q", body)
	}
	if callCount != 1 {
		t.Errorf("expected exactly 1 attempt (no retry), got %d", callCount)
	}
}
