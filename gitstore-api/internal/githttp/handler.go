// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package githttp

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	gitv1 "github.com/gitstore-dev/gitstore/api/gen/gitstore/git/v1"
	"github.com/gitstore-dev/gitstore/api/internal/auth"
	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	"github.com/gitstore-dev/gitstore/api/internal/middleware"
	"github.com/gitstore-dev/gitstore/api/internal/middleware/security"
	apiruntime "github.com/gitstore-dev/gitstore/api/internal/runtime"
	"github.com/prometheus/client_golang/prometheus"
	"go.uber.org/zap"
)

// GitClient is the interface the handler uses to call the git service.
type GitClient interface {
	InfoRefs(ctx context.Context, repoID string, service gitv1.Service) ([]byte, gitv1.Service, error)
	UploadPack(ctx context.Context, repoID string, body []byte) (io.Reader, error)
	ReceivePack(ctx context.Context, repoID string, body io.Reader) ([]byte, error)
}

type SmartHttpDeps struct {
	GitClient        GitClient
	RepoResolverFunc RepoResolverFunc
	Store            datastore.Datastore
	Logger           *zap.Logger
	Ids              apiruntime.IDGenerator
	Registry         *auth.ProviderRegistry
}

// RepoResolverFunc resolves (namespace, repo) to a stable repository ID.
type RepoResolverFunc func(namespace, repo string) (string, bool)

// handler holds the dependencies for Git smart HTTP request handlers.
type handler struct {
	git GitClient
	log *zap.Logger
}

func newHandler(git GitClient, log *zap.Logger) *handler {
	return &handler{git: git, log: log}
}

// gitPktLineError writes a Git pkt-line ERR response.
func (h *handler) gitPktLineError(w http.ResponseWriter, status int, msg string) {
	body := fmt.Sprintf("ERR %s", msg)
	pktLen := len(body) + 4
	line := fmt.Sprintf("%04x%s", pktLen, body)
	w.WriteHeader(status)
	_, err := w.Write([]byte(line))
	if err != nil {
		h.log.Error("failed to write response", zap.Error(err))
		return
	}
}

// infoRefsHandler handles GET /{namespace}/{repo}/info/refs?service=git-*
func (h *handler) infoRefsHandler(c *gin.Context) {
	svcParam := c.Query("service")

	repoID := c.MustGet(repoIDKey).(string)

	var service gitv1.Service
	var contentType string
	switch svcParam {
	case "git-upload-pack":
		service = gitv1.Service_SERVICE_GIT_UPLOAD_PACK
		contentType = "application/x-git-upload-pack-advertisement"
	case "git-receive-pack":
		service = gitv1.Service_SERVICE_GIT_RECEIVE_PACK
		contentType = "application/x-git-receive-pack-advertisement"
	default:
		h.gitPktLineError(c.Writer, http.StatusBadRequest, "unknown service")
		return
	}

	h.log.Info("info_refs start", zap.String("repo_id", repoID), zap.String("service", svcParam))

	advertisement, _, err := h.git.InfoRefs(c.Request.Context(), repoID, service)
	if err != nil {
		h.log.Error("info_refs: git service error", zap.Error(err))
		h.gitPktLineError(c.Writer, http.StatusServiceUnavailable, "service unavailable")
		return
	}

	c.Header("Cache-Control", "no-cache")
	c.Data(http.StatusOK, contentType, advertisement)

	h.log.Info("info_refs complete", zap.String("repo_id", repoID))
}

// uploadPackHandler handles POST /{namespace}/{repo}/git-upload-pack
func (h *handler) uploadPackHandler(c *gin.Context) {
	repoID := c.MustGet(repoIDKey).(string)

	body, err := c.GetRawData()
	if err != nil {
		h.log.Error("upload_pack: read body error", zap.Error(err))
		h.gitPktLineError(c.Writer, http.StatusBadRequest, "failed to read request body")
		return
	}

	h.log.Info("upload_pack start", zap.String("repo_id", repoID), zap.Int("body_bytes", len(body)))

	reader, err := h.git.UploadPack(c.Request.Context(), repoID, body)
	if err != nil {
		h.log.Error("upload_pack: git service error", zap.Error(err))
		h.gitPktLineError(c.Writer, http.StatusServiceUnavailable, "service unavailable")
		return
	}

	c.Header("Content-Type", "application/x-git-upload-pack-result")
	c.Header("Cache-Control", "no-cache")
	c.Status(http.StatusOK)

	totalBytes, err := io.Copy(c.Writer, reader)
	if err != nil {
		h.log.Error("upload_pack: stream error", zap.Error(err))
		return
	}

	h.log.Info("upload_pack complete", zap.String("repo_id", repoID), zap.Int64("total_bytes", totalBytes))
}

// receivePackHandler handles POST /{namespace}/{repo}/git-receive-pack
func (h *handler) receivePackHandler(c *gin.Context) {
	repoID := c.MustGet(repoIDKey).(string)

	h.log.Info("receive_pack start", zap.String("repo_id", repoID))

	reportStatus, err := h.git.ReceivePack(c.Request.Context(), repoID, c.Request.Body)
	if err != nil {
		h.log.Error("receive_pack: git service error", zap.Error(err))
		h.gitPktLineError(c.Writer, http.StatusServiceUnavailable, "service unavailable")
		return
	}

	c.Header("Cache-Control", "no-cache")
	c.Data(http.StatusOK, "application/x-git-receive-pack-result", reportStatus)

	h.log.Info("receive_pack complete", zap.String("repo_id", repoID), zap.Int("report_status_bytes", len(reportStatus)))
}

// NewMux creates an http.Handler with all Git smart HTTP routes registered.
// When deps.Store is non-nil, RepoResolver and GitHttpAuthorizer middleware are wired in.
func NewMux(deps SmartHttpDeps) http.Handler {
	return NewMuxWithStore(deps)
}

// NewMuxWithStore creates an http.Handler with all Git smart HTTP routes registered,
// including RepoResolver and GitHttpAuthorizer middleware.
func NewMuxWithStore(deps SmartHttpDeps) http.Handler {
	return newMux(deps, false)
}

// NewMuxWithStoreAndAuthz is like NewMuxWithStore but also wires PushContextInserter
// on the receive-pack route. Used when the full push-policy pipeline is needed.
func NewMuxWithStoreAndAuthz(deps SmartHttpDeps) http.Handler {
	return newMux(deps, true)
}

func newMux(deps SmartHttpDeps, withPushCtx bool) http.Handler {
	h := newHandler(deps.GitClient, deps.Logger)
	r := gin.New()

	requestIdMiddleware := middleware.NewRequestId(deps.Ids)
	authenticateMiddleware := security.NewAuthenticate(deps.Registry, deps.Logger, prometheus.DefaultRegisterer)

	var authorizeMiddleware security.Authorize
	if deps.Store != nil {
		authorizeMiddleware = security.NewAuthorizeWithStore(deps.Registry, deps.Store, deps.Logger)
	} else {
		authorizeMiddleware = security.NewAuthorize(deps.Registry, deps.Logger)
	}

	r.Use(requestIdMiddleware.RequestIdInserter)
	r.Use(authenticateMiddleware.BasicAuthenticator)

	var repoResolverMW gin.HandlerFunc
	if deps.Store != nil {
		repoResolverMW = RepoResolver(deps.Store, deps.Logger)
	} else {
		// Legacy path: use the injected RepoResolverFunc and set repoIDKey manually.
		repoResolverMW = legacyRepoResolver(deps.RepoResolverFunc, deps.Logger)
	}
	r.Use(repoResolverMW)
	r.Use(authorizeMiddleware.GitHttpAuthorizer)

	r.GET("/:namespace/:repo/info/refs", h.infoRefsHandler)
	r.POST("/:namespace/:repo/git-upload-pack", h.uploadPackHandler)
	if withPushCtx && deps.Store != nil {
		r.POST("/:namespace/:repo/git-receive-pack",
			authorizeMiddleware.PushContextInserter,
			h.receivePackHandler,
		)
	} else {
		r.POST("/:namespace/:repo/git-receive-pack", h.receivePackHandler)
	}

	return r
}

// legacyRepoResolver wraps the old RepoResolverFunc in a gin middleware that sets repoIDKey.
func legacyRepoResolver(resolver RepoResolverFunc, log *zap.Logger) gin.HandlerFunc {
	return func(c *gin.Context) {
		namespace := c.Param("namespace")
		repo := strings.TrimSuffix(c.Param("repo"), ".git")
		repoID, ok := resolver(namespace, repo)
		if !ok {
			gitPktLineErrorRaw(c.Writer, http.StatusNotFound, "repository not found")
			c.Abort()
			return
		}
		c.Set(repoIDKey, repoID)
		c.Next()
	}
}
