// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package security

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gin-contrib/cors"
	"github.com/gin-gonic/gin"
	gitv1 "github.com/gitstore-dev/gitstore/api/gen/gitstore/git/v1"
	"github.com/gitstore-dev/gitstore/api/internal/auth"
	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	"github.com/gitstore-dev/gitstore/api/internal/gitclient"
	"github.com/prometheus/client_golang/prometheus"
	"go.uber.org/zap"
	"golang.org/x/time/rate"
)

type Authenticate struct {
	registry   *auth.ProviderRegistry
	logger     *zap.Logger
	authCounts *prometheus.CounterVec
	// apiAuthnCounts backs gitstore_api_authn_requests_total{provider,outcome}
	// (spec 061 T013) — a per-provider, per-outcome counter for the generic
	// bearer-token AuthN path (GraphQL and other non-git-http API traffic),
	// distinct from authCounts' git-smart-HTTP-specific {outcome,service}
	// shape above.
	apiAuthnCounts *prometheus.CounterVec
}

type Authorize struct {
	registry *auth.ProviderRegistry
	store    datastoreGetter
	logger   *zap.Logger
}

// datastoreGetter is the minimal datastore interface needed by the security middleware.
type datastoreGetter interface {
	GetRepository(ctx context.Context, id string) (*datastore.Repository, error)
	GetNamespaceByName(ctx context.Context, name string) (*datastore.Namespace, error)
	GetNamespace(ctx context.Context, id string) (*datastore.Namespace, error)
	LookupRepository(ctx context.Context, namespace, name string) (*datastore.NamespaceMapping, error)
	GetCategoryTaxonomy(ctx context.Context, uid string) (*datastore.CategoryTaxonomy, error)
}

// authorizeRepositoryTenant is the common repository policy decision used by
// GraphQL and Git smart-HTTP. Ownership is deliberately derived from the
// stable principal subject rather than provider-specific roles.
func (a *Authorize) authorizeRepositoryTenant(ctx context.Context, principal *auth.Principal, operation string, repository *datastore.Repository, namespaces ...*datastore.Namespace) (string, error) {
	if a.registry == nil || a.registry.AuthZ() == nil {
		return "", errors.New("authorization service unavailable")
	}
	if principal == nil {
		principal = auth.Anonymous()
	}
	if repository == nil || len(namespaces) == 0 {
		return "", errors.New("repository authorization context is incomplete")
	}

	scope := "own"
	attrs := make(map[string]any, len(namespaces))
	owner := ""
	for index, namespace := range namespaces {
		if namespace == nil {
			return "", errors.New("repository authorization namespace is missing")
		}
		if index == 0 {
			owner = namespace.CreationActor
			attrs["namespace"] = namespace.Name
		} else {
			attrs["targetNamespace"] = namespace.Name
			attrs["targetOwnerSub"] = namespace.CreationActor
		}
		if namespace.CreationActor == "" || namespace.CreationActor != principal.Subject {
			scope = "any"
		}
	}
	action := "repository." + operation + "." + scope
	decision, err := a.registry.AuthZ().Authorize(ctx, principal, action, auth.ResourceContext{
		Kind: "repository", Name: repository.Name, OwnerSub: owner, Attrs: attrs,
	})
	if err != nil {
		return "", err
	}
	if decision.Outcome != auth.OutcomeAllow {
		return "", fmt.Errorf("permission denied: %s", decision.Reason)
	}
	return action, nil
}

type RateLimit struct {
	mu      sync.Mutex
	limit   rate.Limit
	burst   int
	clients map[string]*client
}

type client struct {
	limiter  *rate.Limiter
	lastSeen time.Time
}

type authenticatorFunc func(*gin.Context, auth.AuthRequest, *Authenticate) (context.Context, *auth.Principal, bool)

// NewAuthenticate creates a new Authenticate middleware.
// Pass a non-nil prometheus.Registerer to enable auth outcome metrics.
// Pass nil to skip metric registration (useful in tests that don't need counters).
func NewAuthenticate(registry *auth.ProviderRegistry, logger *zap.Logger, opts ...prometheus.Registerer) Authenticate {
	var counts *prometheus.CounterVec
	var apiCounts *prometheus.CounterVec
	if len(opts) > 0 && opts[0] != nil {
		counts = prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "gitstore_git_http_auth_requests_total",
			Help: "Total Git smart-HTTP authentication requests by outcome and service.",
		}, []string{"outcome", "service"})
		if err := opts[0].Register(counts); err != nil {
			// Already registered (e.g. multiple NewMux calls in tests or hot-reload) — reuse existing.
			var are prometheus.AlreadyRegisteredError
			if errors.As(err, &are) {
				counts = are.ExistingCollector.(*prometheus.CounterVec)
			}
		}
		apiCounts = prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "gitstore_api_authn_requests_total",
			Help: "Total bearer-token API authentication requests by provider and outcome.",
		}, []string{"provider", "outcome"})
		if err := opts[0].Register(apiCounts); err != nil {
			var are prometheus.AlreadyRegisteredError
			if errors.As(err, &are) {
				apiCounts = are.ExistingCollector.(*prometheus.CounterVec)
			}
		}
	}
	return Authenticate{
		registry:       registry,
		logger:         logger,
		authCounts:     counts,
		apiAuthnCounts: apiCounts,
	}
}

func NewAuthorize(registry *auth.ProviderRegistry, logger *zap.Logger) Authorize {
	return Authorize{
		registry: registry,
		logger:   logger,
	}
}

// NewAuthorizeWithStore creates an Authorize middleware with a datastore for PushContextInserter.
func NewAuthorizeWithStore(registry *auth.ProviderRegistry, store datastoreGetter, logger *zap.Logger) Authorize {
	return Authorize{
		registry: registry,
		store:    store,
		logger:   logger,
	}
}

func NewRateLimit(r float64, b int) *RateLimit {
	rl := &RateLimit{
		clients: make(map[string]*client),
		limit:   rate.Limit(r),
		burst:   b,
	}
	go rl.cleanup()
	return rl
}

// cleanup evicts entries that have not been seen for 10 minutes. Runs forever;
// the goroutine is intentionally leaked for the lifetime of the server process.
func (r *RateLimit) cleanup() {
	const ttl = 10 * time.Minute
	ticker := time.NewTicker(ttl)
	defer ticker.Stop()
	for range ticker.C {
		cutoff := time.Now().Add(-ttl)
		r.mu.Lock()
		for ip, c := range r.clients {
			if c.lastSeen.Before(cutoff) {
				delete(r.clients, ip)
			}
		}
		r.mu.Unlock()
	}
}

func (a *Authenticate) authenticator(c *gin.Context, authenticator authenticatorFunc) {
	if a.logger == nil {
		a.logger = zap.NewNop()
	}
	req := auth.AuthRequest{
		Header:     c.Request.Header,
		RemoteAddr: c.RemoteIP(),
	}
	ctx, principal, aborted := authenticator(c, req, a)
	if aborted {
		return
	}
	ctx = auth.ContextWithPrincipal(ctx, principal)
	c.Request = c.Request.WithContext(ctx)
	c.Next()
}

func bearerAuth(c *gin.Context, req auth.AuthRequest, a *Authenticate) (context.Context, *auth.Principal, bool) {
	// Store the raw Bearer token in context so the refreshToken resolver can
	// pass it to RefreshSession without re-reading the HTTP request.
	ctx := c.Request.Context()
	if authHeader := c.GetHeader("Authorization"); strings.HasPrefix(authHeader, "Bearer ") {
		ctx = auth.ContextWithRawToken(ctx, strings.TrimPrefix(authHeader, "Bearer "))
	}
	principal, decision, err := a.registry.AuthN().Authenticate(ctx, req)
	if err != nil {
		a.logger.Warn("auth chain error", zap.Error(err))
		a.recordAPIAuthN(decision.Provider, "error")
		c.AbortWithStatus(http.StatusUnauthorized)
		return ctx, principal, c.IsAborted()
	}
	if decision.Outcome == auth.OutcomeDeny {
		a.recordAPIAuthN(decision.Provider, "deny")
		c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"Unauthorized": decision.Reason})
		return ctx, principal, c.IsAborted()
	}
	a.recordAPIAuthN(decision.Provider, "allow")
	return ctx, principal, c.IsAborted()
}

func basicAuth(c *gin.Context, req auth.AuthRequest, a *Authenticate) (context.Context, *auth.Principal, bool) {
	ctx := c.Request.Context()
	principal, decision, err := a.registry.AuthN().Authenticate(ctx, req)
	// Derive the service label here where the query string is available.
	svc := resolveService(c.Request.URL.Path, c.Query("service"))
	if err != nil {
		// Transient error (e.g. DB down, provider unreachable) — do not prompt for credentials.
		a.logger.Error("auth chain transient error", zap.Error(err))
		a.recordAuthService("error", svc)
		c.AbortWithStatus(http.StatusServiceUnavailable)
		return ctx, principal, c.IsAborted()
	}
	if decision.Outcome == auth.OutcomeDeny {
		a.logger.Warn("auth denied", zap.String("reason", decision.Reason))
		a.recordAuthService("deny", svc)
		// 401 + WWW-Authenticate so Git credential helpers prompt for credentials.
		c.Header("WWW-Authenticate", `Basic realm="GitStore"`)
		c.AbortWithStatus(http.StatusUnauthorized)
		return ctx, principal, c.IsAborted()
	}
	a.recordAuthService("allow", svc)
	return ctx, principal, c.IsAborted()
}

// resolveService maps a URL path + optional service query param to a metric label.
// For /info/refs the ?service= query param disambiguates upload-pack vs receive-pack.
func resolveService(path, serviceParam string) string {
	switch {
	case strings.HasSuffix(path, "/git-upload-pack"):
		return "upload_pack"
	case strings.HasSuffix(path, "/git-receive-pack"):
		return "receive_pack"
	case strings.HasSuffix(path, "/info/refs"):
		if serviceParam == "git-receive-pack" {
			return "receive_pack"
		}
		return "upload_pack"
	}
	return "unknown"
}

// recordAuthService increments the auth outcome counter if metrics are configured.
func (a *Authenticate) recordAuthService(outcome, service string) {
	if a.authCounts == nil {
		return
	}
	a.authCounts.WithLabelValues(outcome, service).Inc()
}

// recordAPIAuthN increments gitstore_api_authn_requests_total{provider,outcome}
// if metrics are configured (spec 061 T013). provider is the Decision's
// Provider field — the name of the specific AuthNProvider that produced the
// terminal outcome, or "chain" when every provider in the chain returned
// Challenge (see ChainedAuthN.Authenticate's doc comment).
func (a *Authenticate) recordAPIAuthN(provider, outcome string) {
	if a.apiAuthnCounts == nil {
		return
	}
	a.apiAuthnCounts.WithLabelValues(provider, outcome).Inc()
}

// Authenticator authenticates a request through the active provider chain.
// Anonymous access passes through as a Principal with AuthMethod "none"; denied
// credentials return 401 before the request reaches GraphQL.
func (a *Authenticate) Authenticator(c *gin.Context) {
	a.authenticator(c, bearerAuth)
}

// BasicAuthenticator authenticates a request through the active provider chain.
// Anonymous access passes through as a Principal with AuthMethod "none"; denied
// credentials return 401 before the request reaches git-service.
// Authenticates git smart http.
func (a *Authenticate) BasicAuthenticator(c *gin.Context) {
	a.authenticator(c, basicAuth)
}

// repoIDKey matches the constant defined in githttp/resolver.go.
// Duplicated here to avoid an import cycle; both must be kept in sync.
const repoIDKey = "repoID"
const approvedRepositoryActionKey = "approvedRepositoryAction"

// GitHttpAuthorizer authorizes a Git Smart HTTP request after RepoResolver has run.
// Requires repoIDKey to be set in the gin context (abort 500 if missing).
// Determines required action from route path (and, for /info/refs, the
// ?service= query param since that endpoint is shared by both services):
//   - /git-upload-pack, or /info/refs?service=git-upload-pack   → "repository.read"
//   - /git-receive-pack, or /info/refs?service=git-receive-pack → "repository.write"
//
// Aborts with 401 on an anonymous deny so Git credential helpers can retry,
// and with 403 when an authenticated principal lacks permission.
func (a *Authorize) GitHttpAuthorizer(c *gin.Context) {
	if a.logger == nil {
		a.logger = zap.NewNop()
	}

	val, exists := c.Get(repoIDKey)
	if !exists {
		a.logger.Error("GitHttpAuthorizer: repoID not in context — RepoResolver must run first")
		c.AbortWithStatus(http.StatusInternalServerError)
		return
	}
	repoID, ok := val.(string)
	if !ok || repoID == "" {
		a.logger.Error("GitHttpAuthorizer: repoID in context is not a non-empty string")
		c.AbortWithStatus(http.StatusInternalServerError)
		return
	}

	principal := auth.PrincipalFromContext(c.Request.Context())
	if principal == nil {
		principal = auth.Anonymous()
	}

	// /info/refs is shared by both upload-pack and receive-pack discovery;
	// the actual service is disambiguated by the ?service= query param, not
	// the path, so a receive-pack discovery request must be treated as a
	// write to correctly reject anonymous access with a 401 (prompting Git
	// to retry with credentials) instead of falling through to a deeper
	// rejection that surfaces as a generic 503.
	operation := "read"
	switch {
	case strings.HasSuffix(c.FullPath(), "/git-receive-pack"):
		operation = "write"
	case strings.HasSuffix(c.FullPath(), "/info/refs") && c.Query("service") == "git-receive-pack":
		operation = "write"
	}

	if a.store == nil {
		// Legacy test/embedding callers do not provide a datastore. Preserve the
		// historical unsuffixed action there; production wiring always supplies a
		// store and therefore uses the tenant-aware contract below.
		authorize := a.registry.AuthZ()
		if authorize == nil {
			c.Next()
			return
		}
		decision, err := authorize.Authorize(c.Request.Context(), principal, "repository."+operation, auth.ResourceContext{Kind: "repository", Name: repoID})
		if err != nil {
			a.logger.Error("authz error", zap.Error(err))
			c.AbortWithStatus(http.StatusServiceUnavailable)
			return
		}
		if decision.Outcome == auth.OutcomeAllow {
			c.Next()
			return
		}
		if principal.AuthMethod == "none" {
			c.Header("WWW-Authenticate", `Basic realm="GitStore"`)
			c.AbortWithStatus(http.StatusUnauthorized)
		} else {
			c.AbortWithStatus(http.StatusForbidden)
		}
		return
	}
	repository, err := a.store.GetRepository(c.Request.Context(), repoID)
	if err != nil {
		a.logger.Error("GitHttpAuthorizer: resolve repository", zap.Error(err))
		c.AbortWithStatus(http.StatusInternalServerError)
		return
	}
	namespace, err := a.store.GetNamespaceByName(c.Request.Context(), repository.Namespace)
	if err != nil {
		a.logger.Error("GitHttpAuthorizer: resolve namespace", zap.Error(err))
		c.AbortWithStatus(http.StatusInternalServerError)
		return
	}
	action, err := a.authorizeRepositoryTenant(c.Request.Context(), principal, operation, repository, namespace)
	if err != nil {
		a.logger.Warn("authz denied", zap.String("action", action), zap.Error(err))
		if principal.AuthMethod == "none" {
			c.Header("WWW-Authenticate", `Basic realm="GitStore"`)
			c.AbortWithStatus(http.StatusUnauthorized)
			return
		}
		c.AbortWithStatus(http.StatusForbidden)
		return
	}
	c.Set(approvedRepositoryActionKey, action)
	if principal.AuthMethod == "none" {
		c.Request = c.Request.WithContext(auth.ContextWithAuthorizedAnonymous(c.Request.Context()))
	}
	c.Next()
}

// PushContextInserter is route-level middleware for POST /:namespace/:repo/git-receive-pack.
// It reads repoID from the gin context (set by RepoResolver), fetches the Repository record
// to get push policy limits, reads the authenticated Principal, builds a gitv1.PushContext,
// and stores it in the request context via context.WithValue so ReceivePack can attach it
// to the first gRPC chunk.
func (a *Authorize) PushContextInserter(c *gin.Context) {
	if a.logger == nil {
		a.logger = zap.NewNop()
	}
	if a.store == nil {
		a.logger.Error("PushContextInserter: store is nil — call NewAuthorizeWithStore")
		c.AbortWithStatus(http.StatusInternalServerError)
		return
	}

	val, exists := c.Get(repoIDKey)
	if !exists {
		a.logger.Error("PushContextInserter: repoID not in context")
		c.AbortWithStatus(http.StatusInternalServerError)
		return
	}
	repoID, ok := val.(string)
	if !ok || repoID == "" {
		c.AbortWithStatus(http.StatusInternalServerError)
		return
	}

	repo, err := a.store.GetRepository(c.Request.Context(), repoID)
	if err != nil {
		a.logger.Error("PushContextInserter: GetRepository failed", zap.Error(err))
		c.AbortWithStatus(http.StatusServiceUnavailable)
		return
	}

	principal := auth.PrincipalFromContext(c.Request.Context())
	if principal == nil {
		principal = auth.Anonymous()
	}
	action, exists := c.Get(approvedRepositoryActionKey)
	approvedAction, isString := action.(string)
	if !exists || !isString || approvedAction == "" {
		a.logger.Error("PushContextInserter: approved repository action missing")
		c.AbortWithStatus(http.StatusInternalServerError)
		return
	}

	pc := &gitv1.PushContext{
		RepositoryId:          repoID,
		Namespace:             strings.TrimSuffix(c.Param("namespace"), ".git"),
		RepositoryName:        repo.Name,
		ConfigResourceVersion: repo.UpdateTimestamp.UTC().Format(time.RFC3339Nano),
		Authorization: &gitv1.RequestAuthorization{
			Actor: &gitv1.AuthContext{
				Subject: principal.Subject, Issuer: principal.Issuer, AuthMethod: principal.AuthMethod,
				Roles: principal.Roles, Groups: principal.Groups, Scopes: principal.Scopes,
			},
			Action: approvedAction, ResourceKind: "repository", ResourceName: repo.Name, RepositoryId: repoID,
		},
		Policy: &gitv1.PushPolicy{
			MaxPackSizeBytes: repo.MaxPackSizeBytes,
			MaxFileSizeBytes: repo.MaxFileSizeBytes,
		},
	}

	ctx := gitclient.ContextWithPushContext(c.Request.Context(), pc)
	c.Request = c.Request.WithContext(ctx)
	c.Next()
}

// RateLimiter rate limiting prevents abuse, brute-force attacks, and resource exhaustion.
func (r *RateLimit) RateLimiter(c *gin.Context) {
	ip := c.ClientIP()
	r.mu.Lock()
	if _, exists := r.clients[ip]; !exists {
		r.clients[ip] = &client{limiter: rate.NewLimiter(r.limit, r.burst)}
	}
	cl := r.clients[ip]
	cl.lastSeen = time.Now()
	r.mu.Unlock()
	if !cl.limiter.Allow() {
		c.AbortWithStatusJSON(http.StatusTooManyRequests, gin.H{
			"error": "rate limit exceeded",
		})
		return
	}

	c.Next()
}

// CorsConfiguration Cross-Origin Resource Sharing (CORS) controls which
// external domains can make requests to the API
func CorsConfiguration() gin.HandlerFunc {
	return cors.New(cors.Config{
		AllowOrigins:     []string{"*"},
		AllowMethods:     []string{"GET", "POST", "PUT", "DELETE", "OPTIONS"},
		AllowHeaders:     []string{"Origin", "Content-Type", "Authorization", "X-Request-ID"},
		ExposeHeaders:    []string{"Content-Length", "X-Request-ID"},
		AllowCredentials: false,
		MaxAge:           12 * time.Hour,
	})
}

// SecureHeaders the use of security headers to protect this web application
// from common security vulnerabilities.
func SecureHeaders(c *gin.Context) {
	c.Header("X-Frame-Options", "DENY")
	c.Header("X-Content-Type-Options", "nosniff")
	c.Header("Strict-Transport-Security", "max-age=31536000; includeSubDomains")
	c.Header("Content-Security-Policy", "default-src 'self'")
	c.Header("Referrer-Policy", "strict-origin")
	c.Header("Permissions-Policy", "geolocation=(), camera=(), microphone=()")
	c.Next()
}
