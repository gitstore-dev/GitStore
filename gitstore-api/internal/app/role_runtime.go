// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package app

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/gitstore-dev/gitstore/api/internal/config"
	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	"github.com/gitstore-dev/gitstore/api/internal/gitclient"
	"github.com/gitstore-dev/gitstore/api/internal/health"
	"go.uber.org/zap"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/status"
)

var errRoleUnavailable = errors.New("role dependencies unavailable")

// Only idempotent bootstrap repair is retried. Authoring and admission requests
// are never replayed here, including when their response is lost.
type bootstrapRuntime struct {
	run      func(context.Context) error
	log      *zap.Logger
	complete atomic.Bool
	mu       sync.Mutex
	cancel   context.CancelFunc
	done     chan struct{}
	closed   bool
}

func newBootstrapRuntime(run func(context.Context) error, log *zap.Logger) *bootstrapRuntime {
	return &bootstrapRuntime{run: run, log: log}
}

func (b *bootstrapRuntime) start() {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed || b.done != nil {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	b.cancel, b.done = cancel, make(chan struct{})
	go func() {
		defer close(b.done)
		delay := time.Second
		for {
			attempt, cancel := context.WithTimeout(ctx, 30*time.Second)
			err := b.run(attempt)
			cancel()
			if err == nil {
				b.complete.Store(true)
				return
			}
			b.log.Warn("Bootstrap pending; listeners remain available", zap.Error(err))
			timer := time.NewTimer(delay)
			select {
			case <-ctx.Done():
				timer.Stop()
				return
			case <-timer.C:
			}
			delay = min(30*time.Second, delay*2)
		}
	}()
}

func (b *bootstrapRuntime) ready(context.Context) error {
	if !b.complete.Load() {
		return errRoleUnavailable
	}
	return nil
}

func (b *bootstrapRuntime) close() {
	b.mu.Lock()
	b.closed = true
	cancel, done := b.cancel, b.done
	b.mu.Unlock()
	if cancel != nil {
		cancel()
		<-done
	}
}

type roleReadiness struct {
	cfg           config.ApiConfig
	store         datastore.Datastore
	git           *gitclient.Client
	bootstrap     *bootstrapRuntime
	journal       func(context.Context) error
	authorization func(context.Context) error
	probeOnce     sync.Once
	probeSlots    chan struct{}
}

func (r *roleReadiness) check(ctx context.Context, role string) error {
	if !r.cfg.HasRole(role) || role == config.RoleStorefront {
		return errRoleUnavailable
	}
	r.probeOnce.Do(func() { r.probeSlots = make(chan struct{}, 4) })
	select {
	case r.probeSlots <- struct{}{}:
		defer func() { <-r.probeSlots }()
	default:
		return errRoleUnavailable
	}
	ctx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	if _, err := r.store.ListProducts(ctx, "", datastore.PageParams{First: 1}); err != nil {
		return errRoleUnavailable
	}
	if r.authorization != nil && r.authorization(ctx) != nil {
		return errRoleUnavailable
	}
	if r.git == nil || r.git.TransportReady(ctx) != nil {
		return errRoleUnavailable
	}
	if role == config.RoleAdmin || role == config.RoleCatalog {
		if r.bootstrap != nil && r.bootstrap.ready(ctx) != nil {
			return errRoleUnavailable
		}
		if r.journal != nil && r.journal(ctx) != nil {
			return errRoleUnavailable
		}
	}
	return nil
}

func (r *roleReadiness) aggregate(ctx context.Context) error {
	for _, role := range []string{config.RoleSmartHTTP, config.RoleCatalog, config.RoleAdmin, config.RoleStorefront} {
		if r.cfg.HasRole(role) && r.check(ctx, role) != nil {
			return errRoleUnavailable
		}
	}
	return nil
}

func registerRoleRoutes(router *gin.Engine, readiness *roleReadiness) {
	router.GET("/ready/:role", func(c *gin.Context) {
		role := c.Param("role")
		if !readiness.cfg.HasRole(role) {
			c.Status(http.StatusNotFound)
			return
		}
		ctx, cancel := context.WithTimeout(c.Request.Context(), time.Second)
		defer cancel()
		if readiness.check(ctx, role) != nil {
			writeRuntimeHealth(c, http.StatusServiceUnavailable, map[string]health.Check{role: {Status: health.StatusUnhealthy}})
			return
		}
		writeRuntimeHealth(c, http.StatusOK, map[string]health.Check{role: {Status: health.StatusHealthy}})
	})
}

func registerRuntimeRoutes(router *gin.Engine, readiness *roleReadiness) {
	router.GET("/health", func(c *gin.Context) { writeRuntimeHealth(c, http.StatusOK, nil) })
	router.GET("/ready", func(c *gin.Context) {
		ctx, cancel := context.WithTimeout(c.Request.Context(), time.Second)
		defer cancel()
		checks := map[string]health.Check{}
		code := http.StatusOK
		for _, role := range []string{config.RoleSmartHTTP, config.RoleCatalog, config.RoleAdmin, config.RoleStorefront} {
			if !readiness.cfg.HasRole(role) {
				continue
			}
			check := health.Check{Status: health.StatusHealthy}
			if readiness.check(ctx, role) != nil {
				check.Status, code = health.StatusUnhealthy, http.StatusServiceUnavailable
			}
			checks[role] = check
		}
		writeRuntimeHealth(c, code, checks)
	})
	registerRoleRoutes(router, readiness)
}

func writeRuntimeHealth(c *gin.Context, code int, checks map[string]health.Check) {
	state := health.StatusHealthy
	if code != http.StatusOK {
		state = health.StatusUnhealthy
	}
	c.JSON(code, health.HealthResponse{Status: state, Version: version, Timestamp: time.Now().UTC(), Checks: checks})
}

func bootstrapServingHandler(next http.Handler, ready func(context.Context) error) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/health" && request.URL.Path != "/ready" &&
			request.URL.Path != "/metrics" && !strings.HasPrefix(request.URL.Path, "/ready/") && ready(request.Context()) != nil {
			w.Header().Set("Retry-After", "1")
			http.Error(w, "bootstrap pending", http.StatusServiceUnavailable)
			return
		}
		next.ServeHTTP(w, request)
	})
}

func newStorefrontServer(cfg *config.Config, log *zap.Logger) (*Server, error) {
	var cleanup []func()
	router, err := NewStorefrontHandler(cfg.Api.Storefront, cfg.Api.StorefrontRateLimit, cfg.Api.TrustedProxies, func(close func()) {
		cleanup = append(cleanup, close)
	})
	if err != nil {
		for _, close := range cleanup {
			close()
		}
		return nil, err
	}
	readiness := &roleReadiness{cfg: cfg.Api}
	endpoints, err := bindHTTPServers([]httpEndpoint{{name: "storefront", server: &http.Server{
		Addr: cfg.Api.BindAddress(config.RoleStorefront), Handler: runtimeHTTPHandler(router, readiness),
		ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 15 * time.Second, IdleTimeout: 60 * time.Second,
	}}})
	if err != nil {
		for _, close := range cleanup {
			close()
		}
		return nil, err
	}
	return &Server{cfg: cfg, log: log, httpServers: endpoints, endpointCleanup: cleanup, roleReady: readiness}, nil
}

func runtimeHTTPHandler(next http.Handler, readiness *roleReadiness) http.Handler {
	operations := gin.New()
	registerRuntimeRoutes(operations, readiness)
	mux := http.NewServeMux()
	mux.Handle("/health", operations)
	mux.Handle("/ready", operations)
	mux.Handle("/ready/", operations)
	mux.Handle("/", next)
	return mux
}

type catalogHealth struct {
	healthpb.UnimplementedHealthServer
	readiness *roleReadiness
}

func registerCatalogHealth(server *grpc.Server, readiness *roleReadiness) {
	healthpb.RegisterHealthServer(server, &catalogHealth{readiness: readiness})
}

// Liveness never calls a peer. Readiness checks only local state and the Git
// transport, never Git's dependency readiness (which includes this catalog).
func (h *catalogHealth) Check(ctx context.Context, request *healthpb.HealthCheckRequest) (*healthpb.HealthCheckResponse, error) {
	ctx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	ready := true
	switch request.Service {
	case "liveness":
	case "":
		ready = h.readiness.aggregate(ctx) == nil
	case config.RoleCatalog, config.RoleSmartHTTP, config.RoleAdmin, config.RoleStorefront:
		if !h.readiness.cfg.HasRole(request.Service) {
			return nil, status.Error(codes.NotFound, "role not selected")
		}
		ready = h.readiness.check(ctx, request.Service) == nil
	default:
		return nil, status.Error(codes.NotFound, "unknown service")
	}
	state := healthpb.HealthCheckResponse_NOT_SERVING
	if ready {
		state = healthpb.HealthCheckResponse_SERVING
	}
	return &healthpb.HealthCheckResponse{Status: state}, nil
}

func boundedHTTP(next http.Handler, budget config.RoleBudget) http.Handler {
	slots := make(chan struct{}, budget.InFlight)
	return http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		select {
		case slots <- struct{}{}:
			defer func() { <-slots }()
		default:
			w.Header().Set("Retry-After", "1")
			http.Error(w, "runtime capacity exhausted", http.StatusServiceUnavailable)
			return
		}
		ctx, cancel := context.WithTimeout(request.Context(), budget.Deadline)
		defer cancel()
		next.ServeHTTP(w, request.WithContext(ctx))
	})
}

func boundedCatalog(budget config.RoleBudget) grpc.UnaryServerInterceptor {
	slots := make(chan struct{}, budget.InFlight)
	return func(ctx context.Context, request any, info *grpc.UnaryServerInfo, next grpc.UnaryHandler) (any, error) {
		// Health must not compete with admission callbacks for worker slots.
		if strings.HasPrefix(info.FullMethod, "/grpc.health.v1.Health/") {
			return next(ctx, request)
		}
		select {
		case slots <- struct{}{}:
			defer func() { <-slots }()
		default:
			return nil, status.Error(codes.ResourceExhausted, "catalog capacity exhausted")
		}
		ctx, cancel := context.WithTimeout(ctx, budget.Deadline)
		defer cancel()
		return next(ctx, request)
	}
}
