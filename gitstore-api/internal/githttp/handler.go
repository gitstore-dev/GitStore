// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package githttp

import (
	"context"
	"errors"
	"io"
	"net/http"

	"github.com/gin-gonic/gin"
	gitv1 "github.com/gitstore-dev/gitstore/api/gen/gitstore/git/v1"
	"github.com/gitstore-dev/gitstore/api/internal/auth"
	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	"github.com/gitstore-dev/gitstore/api/internal/gitclient"
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
	GitClient GitClient
	Store     datastore.Datastore
	Logger    *zap.Logger
	Ids       apiruntime.IDGenerator
	Registry  *auth.ProviderRegistry
}

// handler holds the dependencies for Git smart HTTP request handlers.
type handler struct {
	git GitClient
	log *zap.Logger
}

func newHandler(git GitClient, log *zap.Logger) *handler {
	return &handler{git: git, log: log}
}

// gitPktLineError writes a Git pkt-line ERR response, logging any write failure.
func (h *handler) gitPktLineError(w http.ResponseWriter, status int, msg string) {
	if _, err := gitPktLineErrorRaw(w, status, msg); err != nil {
		h.log.Error("failed to write pkt-line error response", zap.Error(err))
	}
}

// writeGitClientError maps an error returned by the GitClient into an HTTP
// response. A gitclient.ErrAuthorizationDenied means RequestAuthorization
// rejected the request on its own invariants (e.g. anonymous principals are
// never approved for a write action); that must surface as 401 with
// WWW-Authenticate so Git retries the request with credentials instead of
// giving up. Any other error is a genuine transport/service failure and is
// reported as 503.
func (h *handler) writeGitClientError(c *gin.Context, err error) {
	if errors.Is(err, gitclient.ErrAuthorizationDenied) {
		c.Header("WWW-Authenticate", `Basic realm="GitStore"`)
		h.gitPktLineError(c.Writer, http.StatusUnauthorized, "authentication required")
		return
	}
	h.gitPktLineError(c.Writer, http.StatusServiceUnavailable, "service unavailable")
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
		h.writeGitClientError(c, err)
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
		h.writeGitClientError(c, err)
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
		h.writeGitClientError(c, err)
		return
	}

	c.Header("Cache-Control", "no-cache")
	c.Data(http.StatusOK, "application/x-git-receive-pack-result", reportStatus)

	h.log.Info("receive_pack complete", zap.String("repo_id", repoID), zap.Int("report_status_bytes", len(reportStatus)))
}

// NewMux creates the only supported Git smart-HTTP pipeline: datastore-backed
// repository resolution, tenant-aware authorization, and push context insertion.
func NewMux(deps SmartHttpDeps) http.Handler {
	if deps.Store == nil {
		panic("githttp: datastore is required")
	}
	h := newHandler(deps.GitClient, deps.Logger)
	r := gin.New()

	requestIdMiddleware := middleware.NewRequestId(deps.Ids)
	authenticateMiddleware := security.NewAuthenticate(deps.Registry, deps.Logger, prometheus.DefaultRegisterer)

	authorizeMiddleware := security.NewAuthorizeWithStore(deps.Registry, deps.Store, deps.Logger)

	r.Use(requestIdMiddleware.RequestIdInserter)
	r.Use(authenticateMiddleware.BasicAuthenticator)

	r.Use(RepoResolver(deps.Store, deps.Logger))
	r.Use(authorizeMiddleware.GitHttpAuthorizer)

	r.GET("/:namespace/:repo/info/refs", h.infoRefsHandler)
	r.POST("/:namespace/:repo/git-upload-pack", h.uploadPackHandler)
	r.POST("/:namespace/:repo/git-receive-pack", authorizeMiddleware.PushContextInserter, h.receivePackHandler)

	return r
}
