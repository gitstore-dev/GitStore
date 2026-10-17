// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package githttp

import (
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	gitv1 "github.com/gitstore-dev/gitstore/api/gen/gitstore/git/v1"
	"github.com/gitstore-dev/gitstore/api/internal/auth"
	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	"github.com/gitstore-dev/gitstore/api/internal/gitclient"
	"github.com/gitstore-dev/gitstore/api/internal/middleware"
	"github.com/gitstore-dev/gitstore/api/internal/middleware/security"
	apiruntime "github.com/gitstore-dev/gitstore/api/internal/runtime"
	"github.com/gitstore-dev/gitstore/observability"
	"github.com/prometheus/client_golang/prometheus"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"
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
	Tracing   bool
}

// handler holds the dependencies for Git smart HTTP request handlers.
type handler struct {
	git     GitClient
	log     *zap.Logger
	metrics *prometheus.HistogramVec
	tracer  trace.Tracer
}

func newHandler(git GitClient, log *zap.Logger) *handler {
	return &handler{git: git, log: log, metrics: defaultGitHTTPStageMetrics}
}

type gitHTTPPhase uint8

const (
	gitHTTPAuthentication gitHTTPPhase = iota
	gitHTTPResolution
	gitHTTPAuthorization
	gitHTTPPushContext
	gitHTTPHandler
	gitHTTPPhaseCount
)

const gitHTTPTimingKey = "gitHTTPRequestTiming"

var defaultGitHTTPStageMetrics = newGitHTTPStageMetrics(prometheus.DefaultRegisterer)

func newGitHTTPStageMetrics(reg prometheus.Registerer) *prometheus.HistogramVec {
	metrics := prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Namespace: "gitstore",
		Subsystem: "git_http",
		Name:      "stage_duration_seconds",
		Help:      "Git smart HTTP latency at attempted middleware boundaries and complete requests.",
		Buckets:   []float64{.001, .005, .01, .025, .05, .1, .25, .5, .75, 1, 1.5, 2, 3, 5, 10, 30},
	}, []string{"operation", "phase", "outcome"})
	reg.MustRegister(metrics)
	return metrics
}

type gitHTTPTiming struct {
	phase     gitHTTPPhase
	last      time.Time
	durations [gitHTTPPhaseCount]time.Duration
	attempted [gitHTTPPhaseCount]bool
	root      trace.Span
	active    trace.Span
	tracer    trace.Tracer
}

func (t *gitHTTPTiming) advance(phase gitHTTPPhase, now time.Time) {
	t.durations[t.phase] += now.Sub(t.last)
	t.phase = phase
	t.last = now
	t.attempted[phase] = true
}

func (t *gitHTTPTiming) observe(metrics *prometheus.HistogramVec, operation string, status int, total time.Duration) {
	ctx := context.Background()
	if t.root != nil {
		ctx = trace.ContextWithSpan(ctx, t.root)
	}
	outcome := "success"
	if status >= 500 {
		outcome = "server_error"
	} else if status >= 400 {
		outcome = "client_error"
	}
	phases := [gitHTTPPhaseCount]string{"authentication", "resolution", "authorization", "push_context", "handler"}
	for phase, name := range phases {
		if t.attempted[phase] {
			observability.Observe(ctx, metrics.WithLabelValues(operation, name, outcome), t.durations[phase].Seconds())
		}
	}
	observability.Observe(ctx, metrics.WithLabelValues(operation, "total", outcome), total.Seconds())
}

func gitHTTPTimedStage(phase gitHTTPPhase, next gin.HandlerFunc) gin.HandlerFunc {
	return func(c *gin.Context) {
		timing := c.MustGet(gitHTTPTimingKey).(*gitHTTPTiming)
		now := time.Now()
		timing.advance(phase, now)
		if timing.tracer != nil {
			timing.active.End(trace.WithTimestamp(now))
			phases := [gitHTTPPhaseCount]string{"authentication", "resolution", "authorization", "push_context", "handler"}
			ctx := trace.ContextWithSpan(c.Request.Context(), timing.root)
			ctx, span := timing.tracer.Start(ctx, "git.http."+phases[phase], trace.WithTimestamp(now))
			timing.active = span
			c.Request = c.Request.WithContext(ctx)
		}
		next(c)
	}
}

func (h *handler) requestTiming(c *gin.Context) {
	start := time.Now()
	timing := &gitHTTPTiming{phase: gitHTTPAuthentication, last: start}
	timing.attempted[gitHTTPAuthentication] = true
	operation := gitHTTPOperation(c.FullPath())
	if h.tracer != nil {
		ctx := propagation.TraceContext{}.Extract(c.Request.Context(), propagation.HeaderCarrier(c.Request.Header))
		ctx, timing.root = h.tracer.Start(ctx, "git.http."+operation,
			trace.WithSpanKind(trace.SpanKindServer), trace.WithTimestamp(start),
			trace.WithAttributes(attribute.String("git.operation", operation)))
		ctx, timing.active = h.tracer.Start(ctx, "git.http.authentication", trace.WithTimestamp(start))
		timing.tracer = h.tracer
		c.Request = c.Request.WithContext(ctx)
	}
	c.Set(gitHTTPTimingKey, timing)
	c.Next()
	end := time.Now()
	timing.advance(timing.phase, end)

	log := h.log
	if timing.root != nil {
		timing.active.End(trace.WithTimestamp(end))
		timing.root.SetAttributes(attribute.Int("http.response.status_code", c.Writer.Status()))
		if c.Writer.Status() >= 400 {
			timing.root.SetStatus(codes.Error, "HTTP request failed")
		}
		log = log.With(zap.String("trace_id", timing.root.SpanContext().TraceID().String()),
			zap.String("span_id", timing.root.SpanContext().SpanID().String()))
		log = observability.Logger(trace.ContextWithSpan(c.Request.Context(), timing.root), log)
		defer timing.root.End(trace.WithTimestamp(end))
	}
	timing.observe(h.metrics, operation, c.Writer.Status(), end.Sub(start))
	log.Info("git_http_request_timing",
		zap.String("operation", operation),
		zap.Int("status", c.Writer.Status()),
		zap.Float64("authentication_ms", float64(timing.durations[gitHTTPAuthentication])/float64(time.Millisecond)),
		zap.Float64("resolution_ms", float64(timing.durations[gitHTTPResolution])/float64(time.Millisecond)),
		zap.Float64("authorization_ms", float64(timing.durations[gitHTTPAuthorization])/float64(time.Millisecond)),
		zap.Float64("push_context_ms", float64(timing.durations[gitHTTPPushContext])/float64(time.Millisecond)),
		zap.Float64("handler_ms", float64(timing.durations[gitHTTPHandler])/float64(time.Millisecond)),
		zap.Float64("total_ms", float64(end.Sub(start))/float64(time.Millisecond)),
	)
}

func gitHTTPOperation(path string) string {
	switch path {
	case "/:namespace/:repo/info/refs":
		return "info_refs"
	case "/:namespace/:repo/git-upload-pack":
		return "upload_pack"
	case "/:namespace/:repo/git-receive-pack":
		return "receive_pack"
	default:
		return "other"
	}
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

const maxUploadPackNegotiationBytes = 1 << 20

// uploadPackHandler handles POST /{namespace}/{repo}/git-upload-pack
func (h *handler) uploadPackHandler(c *gin.Context) {
	repoID := c.MustGet(repoIDKey).(string)

	encoding := strings.ToLower(strings.TrimSpace(c.GetHeader("Content-Encoding")))
	if encoding != "" && encoding != "identity" && encoding != "gzip" {
		h.log.Warn("upload_pack: unsupported content encoding")
		h.gitPktLineError(c.Writer, http.StatusUnsupportedMediaType, "unsupported content encoding")
		return
	}
	body, err := io.ReadAll(io.LimitReader(c.Request.Body, maxUploadPackNegotiationBytes+1))
	if err != nil {
		h.log.Error("upload_pack: read body error", zap.Error(err))
		h.gitPktLineError(c.Writer, http.StatusBadRequest, "failed to read request body")
		return
	}
	if len(body) > maxUploadPackNegotiationBytes {
		h.log.Warn("upload_pack: encoded negotiation exceeds byte limit")
		h.gitPktLineError(c.Writer, http.StatusRequestEntityTooLarge, "negotiation exceeds byte limit")
		return
	}
	if encoding == "gzip" {
		reader, err := gzip.NewReader(bytes.NewReader(body))
		if err != nil {
			h.log.Warn("upload_pack: invalid gzip negotiation", zap.Error(err))
			h.gitPktLineError(c.Writer, http.StatusBadRequest, "invalid gzip negotiation")
			return
		}
		defer reader.Close()
		body, err = io.ReadAll(io.LimitReader(reader, maxUploadPackNegotiationBytes+1))
		if len(body) > maxUploadPackNegotiationBytes {
			h.log.Warn("upload_pack: decoded negotiation exceeds byte limit")
			h.gitPktLineError(c.Writer, http.StatusRequestEntityTooLarge, "negotiation exceeds byte limit")
			return
		}
		if err != nil {
			h.log.Warn("upload_pack: invalid gzip negotiation", zap.Error(err))
			h.gitPktLineError(c.Writer, http.StatusBadRequest, "invalid gzip negotiation")
			return
		}
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
	if deps.Tracing {
		h.tracer = otel.Tracer("gitstore/githttp")
	}
	r := gin.New()

	requestIdMiddleware := middleware.NewRequestId(deps.Ids)
	authenticateMiddleware := security.NewAuthenticate(deps.Registry, deps.Logger, prometheus.DefaultRegisterer)

	authorizeMiddleware := security.NewAuthorizeWithStore(deps.Registry, deps.Store, deps.Logger)

	r.Use(requestIdMiddleware.RequestIdInserter)
	r.Use(h.requestTiming)
	r.Use(authenticateMiddleware.BasicAuthenticator)

	r.Use(gitHTTPTimedStage(gitHTTPResolution, RepoResolver(deps.Store, deps.Logger)))
	r.Use(gitHTTPTimedStage(gitHTTPAuthorization, authorizeMiddleware.GitHttpAuthorizer))

	r.GET("/:namespace/:repo/info/refs", gitHTTPTimedStage(gitHTTPHandler, h.infoRefsHandler))
	r.POST("/:namespace/:repo/git-upload-pack", gitHTTPTimedStage(gitHTTPHandler, h.uploadPackHandler))
	r.POST("/:namespace/:repo/git-receive-pack",
		gitHTTPTimedStage(gitHTTPPushContext, authorizeMiddleware.PushContextInserter),
		gitHTTPTimedStage(gitHTTPHandler, h.receivePackHandler),
	)

	return r
}
