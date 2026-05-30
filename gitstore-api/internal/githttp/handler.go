// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package githttp

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"

	gitv1 "github.com/gitstore-dev/gitstore/api/gen/gitstore/git/v1"
	"go.uber.org/zap"
)

// GitClient is the interface the handler uses to call the git service.
type GitClient interface {
	InfoRefs(ctx context.Context, repoID string, service gitv1.Service) ([]byte, gitv1.Service, error)
	UploadPack(ctx context.Context, repoID string, body []byte) (io.Reader, error)
	ReceivePack(ctx context.Context, repoID string, body io.Reader) ([]byte, error)
}

// RepoResolver resolves (namespace, repo) to a stable repository ID.
type RepoResolver func(namespace, repo string) (string, bool)

// handler holds the dependencies for Git smart HTTP request handlers.
type handler struct {
	git      GitClient
	resolver RepoResolver
	log      *zap.Logger
}

// newHandler creates a new handler with the given git client and resolver.
func newHandler(git GitClient, resolver RepoResolver) *handler {
	return &handler{
		git:      git,
		resolver: resolver,
		log:      zap.NewNop(),
	}
}

// newHandlerWithLogger creates a handler with a real logger.
func newHandlerWithLogger(git GitClient, resolver RepoResolver, log *zap.Logger) *handler {
	return &handler{git: git, resolver: resolver, log: log}
}

// resolveRepo strips ".git" suffix and looks up the repo_id.
func (h *handler) resolveRepo(namespace, repo string) (string, bool) {
	repo = strings.TrimSuffix(repo, ".git")
	return h.resolver(namespace, repo)
}

// gitPktLineError writes a Git pkt-line ERR response.
func gitPktLineError(w http.ResponseWriter, status int, msg string) {
	body := fmt.Sprintf("ERR %s", msg)
	pktLen := len(body) + 4
	line := fmt.Sprintf("%04x%s", pktLen, body)
	w.WriteHeader(status)
	w.Write([]byte(line)) //nolint:errcheck
}

// infoRefsHandler handles GET /{namespace}/{repo}/info/refs?service=git-*
func (h *handler) infoRefsHandler(w http.ResponseWriter, r *http.Request) {
	namespace := r.PathValue("namespace")
	repo := r.PathValue("repo")
	svcParam := r.URL.Query().Get("service")

	repoID, ok := h.resolveRepo(namespace, repo)
	if !ok {
		gitPktLineError(w, http.StatusNotFound, "repository not found")
		return
	}

	var service gitv1.Service
	var contentType string
	switch svcParam {
	case "git-upload-pack":
		service = gitv1.Service_GIT_UPLOAD_PACK
		contentType = "application/x-git-upload-pack-advertisement"
	case "git-receive-pack":
		service = gitv1.Service_GIT_RECEIVE_PACK
		contentType = "application/x-git-receive-pack-advertisement"
	default:
		gitPktLineError(w, http.StatusBadRequest, "unknown service")
		return
	}

	h.log.Info("info_refs start", zap.String("repo_id", repoID), zap.String("service", svcParam))

	advertisement, _, err := h.git.InfoRefs(r.Context(), repoID, service)
	if err != nil {
		h.log.Error("info_refs: git service error", zap.Error(err))
		gitPktLineError(w, http.StatusServiceUnavailable, "service unavailable")
		return
	}

	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)
	w.Write(advertisement) //nolint:errcheck

	h.log.Info("info_refs complete", zap.String("repo_id", repoID))
}

// uploadPackHandler handles POST /{namespace}/{repo}/git-upload-pack
func (h *handler) uploadPackHandler(w http.ResponseWriter, r *http.Request) {
	namespace := r.PathValue("namespace")
	repo := r.PathValue("repo")

	repoID, ok := h.resolveRepo(namespace, repo)
	if !ok {
		gitPktLineError(w, http.StatusNotFound, "repository not found")
		return
	}

	body, err := io.ReadAll(r.Body)
	if err != nil {
		h.log.Error("upload_pack: read body error", zap.Error(err))
		gitPktLineError(w, http.StatusBadRequest, "failed to read request body")
		return
	}

	h.log.Info("upload_pack start", zap.String("repo_id", repoID), zap.Int("body_bytes", len(body)))

	reader, err := h.git.UploadPack(r.Context(), repoID, body)
	if err != nil {
		h.log.Error("upload_pack: git service error", zap.Error(err))
		gitPktLineError(w, http.StatusServiceUnavailable, "service unavailable")
		return
	}

	w.Header().Set("Content-Type", "application/x-git-upload-pack-result")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Transfer-Encoding", "chunked")
	w.WriteHeader(http.StatusOK)

	totalBytes, err := io.Copy(w, reader)
	if err != nil {
		h.log.Error("upload_pack: stream error", zap.Error(err))
		return
	}

	h.log.Info("upload_pack complete", zap.String("repo_id", repoID), zap.Int64("total_bytes", totalBytes))
}

// receivePackHandler handles POST /{namespace}/{repo}/git-receive-pack
func (h *handler) receivePackHandler(w http.ResponseWriter, r *http.Request) {
	namespace := r.PathValue("namespace")
	repo := r.PathValue("repo")

	repoID, ok := h.resolveRepo(namespace, repo)
	if !ok {
		gitPktLineError(w, http.StatusNotFound, "repository not found")
		return
	}

	h.log.Info("receive_pack start", zap.String("repo_id", repoID))

	reportStatus, err := h.git.ReceivePack(r.Context(), repoID, r.Body)
	if err != nil {
		h.log.Error("receive_pack: git service error", zap.Error(err))
		gitPktLineError(w, http.StatusServiceUnavailable, "service unavailable")
		return
	}

	w.Header().Set("Content-Type", "application/x-git-receive-pack-result")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)
	w.Write(reportStatus) //nolint:errcheck

	h.log.Info("receive_pack complete", zap.String("repo_id", repoID), zap.Int("report_status_bytes", len(reportStatus)))
}

// NewMux creates an http.Handler with all Git smart HTTP routes registered.
func NewMux(git GitClient, resolver RepoResolver, log *zap.Logger, health http.Handler) http.Handler {
	h := newHandlerWithLogger(git, resolver, log)
	mux := http.NewServeMux()

	mux.HandleFunc("GET /{namespace}/{repo}/info/refs", h.infoRefsHandler)
	mux.HandleFunc("POST /{namespace}/{repo}/git-upload-pack", h.uploadPackHandler)
	mux.HandleFunc("POST /{namespace}/{repo}/git-receive-pack", h.receivePackHandler)

	if health != nil {
		mux.Handle("/health", health)
		mux.Handle("/ready", health)
	}

	return mux
}
