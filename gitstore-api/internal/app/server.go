// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package app

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	gqlhandler "github.com/99designs/gqlgen/graphql/handler"
	"github.com/99designs/gqlgen/graphql/handler/extension"
	"github.com/99designs/gqlgen/graphql/handler/lru"
	"github.com/99designs/gqlgen/graphql/handler/transport"
	"github.com/99designs/gqlgen/graphql/playground"
	"github.com/gin-gonic/gin"
	catalogv1 "github.com/gitstore-dev/gitstore/api/gen/gitstore/catalog/v1"
	"github.com/gitstore-dev/gitstore/api/internal/auth"
	"github.com/gitstore-dev/gitstore/api/internal/auth/provider/allowall"
	"github.com/gitstore-dev/gitstore/api/internal/auth/provider/anonymous"
	"github.com/gitstore-dev/gitstore/api/internal/auth/provider/rbaclocal"
	"github.com/gitstore-dev/gitstore/api/internal/auth/provider/staticadmin"
	"github.com/gitstore-dev/gitstore/api/internal/auth/provider/userdirnone"
	"github.com/gitstore-dev/gitstore/api/internal/cataloggrpc"
	"github.com/gitstore-dev/gitstore/api/internal/config"
	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	dsfactory "github.com/gitstore-dev/gitstore/api/internal/datastore/factory"
	"github.com/gitstore-dev/gitstore/api/internal/gitclient"
	"github.com/gitstore-dev/gitstore/api/internal/githttp"
	"github.com/gitstore-dev/gitstore/api/internal/graph/generated"
	"github.com/gitstore-dev/gitstore/api/internal/graph/resolver"
	"github.com/gitstore-dev/gitstore/api/internal/health"
	"github.com/gitstore-dev/gitstore/api/internal/middleware"
	"github.com/gitstore-dev/gitstore/api/internal/middleware/security"
	apiruntime "github.com/gitstore-dev/gitstore/api/internal/runtime"
	"github.com/vektah/gqlparser/v2/ast"
	"go.uber.org/zap"
	"google.golang.org/grpc"
)

const version = "1.0.0"

// policyReloader can reload its policy from disk.
type policyReloader interface {
	Reload() error
}

// providerShutdowner is implemented by auth providers that own background goroutines.
type providerShutdowner interface {
	Shutdown()
}

// Server is the composed API runtime.
type Server struct {
	cfg              *config.Config
	log              *zap.Logger
	store            datastore.Datastore
	gitClient        *gitclient.Client
	httpServer       *http.Server
	gitServer        *http.Server
	grpcServer       *grpc.Server
	grpcListener     net.Listener
	rbacReloader     policyReloader
	providerShutdown []providerShutdowner
}

// NewServer builds the API, Git HTTP, and catalog gRPC servers from config.
func NewServer(cfg *config.Config, log *zap.Logger) (*Server, error) {
	if cfg == nil {
		return nil, fmt.Errorf("app: config is required")
	}
	if log == nil {
		return nil, fmt.Errorf("app: logger is required")
	}
	clock := apiruntime.SystemClock{}

	store, err := dsfactory.NewDatastore(cfg.Datastore, log)
	if err != nil {
		return nil, fmt.Errorf("create datastore: %w", err)
	}
	store = datastore.NewInstrumentedDatastore(store, cfg.Datastore.Backend, log)
	ids := apiruntime.UUIDGenerator{}

	gitClient, err := gitclient.NewClientWithAddr(cfg.Git.Grpc.Uri, cfg.Auth.Grpc.HmacSecret)
	if err != nil {
		_ = store.Close()
		return nil, fmt.Errorf("connect git-service: %w", err)
	}
	registry, rbacReloader, providerShutdowns, err := buildProviderRegistry(cfg, log)
	if err != nil {
		_ = gitClient.Close()
		_ = store.Close()
		return nil, fmt.Errorf("build auth provider registry: %w", err)
	}
	log.Info("auth providers ready",
		zap.Strings("authn_chain", cfg.Auth.AuthN.Chain),
		zap.String("authz_provider", cfg.Auth.AuthZ.Provider),
		zap.String("userdir_provider", cfg.Auth.UserDir.Provider),
	)

	gqlRouter, err := NewGraphQLHandler(store, gitClient, log, registry, clock, ids)
	if err != nil {
		_ = gitClient.Close()
		_ = store.Close()
		return nil, err
	}

	router := healthHandler(gqlRouter, store, log, clock)
	var graphQlHandler http.Handler = router

	httpServer := &http.Server{
		Addr:         fmt.Sprintf(":%d", cfg.Api.Port),
		Handler:      graphQlHandler,
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 15 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	gitHttpHandler := githttp.NewMuxWithStoreAndAuthz(githttp.SmartHttpDeps{
		GitClient: gitClient,
		Store:     store,
		Logger:    log,
		Ids:       ids,
		Registry:  registry,
	})
	gitHttpServer := &http.Server{
		Addr:         fmt.Sprintf(":%d", cfg.Api.GitPort),
		Handler:      gitHttpHandler,
		ReadTimeout:  0,
		WriteTimeout: 0,
		IdleTimeout:  60 * time.Second,
	}

	grpcListener, err := net.Listen("tcp", fmt.Sprintf(":%d", cfg.Api.GrpcPort))
	if err != nil {
		_ = gitClient.Close()
		_ = store.Close()
		return nil, fmt.Errorf("listen on catalog gRPC port %d: %w", cfg.Api.GrpcPort, err)
	}
	grpcServer := grpc.NewServer()
	catalogServer, err := cataloggrpc.NewServer(cataloggrpc.ServerDeps{
		Store:     store,
		GitClient: gitClient,
		Logger:    log,
		Clock:     clock,
	})
	if err != nil {
		_ = grpcListener.Close()
		_ = gitClient.Close()
		_ = store.Close()
		return nil, err
	}
	catalogv1.RegisterCatalogServiceServer(grpcServer, catalogServer)

	return &Server{
		cfg:              cfg,
		log:              log,
		store:            store,
		gitClient:        gitClient,
		httpServer:       httpServer,
		gitServer:        gitHttpServer,
		grpcServer:       grpcServer,
		grpcListener:     grpcListener,
		rbacReloader:     rbacReloader,
		providerShutdown: providerShutdowns,
	}, nil
}

// NewGraphQLHandler builds a GraphQL HTTP handler.
func NewGraphQLHandler(store datastore.Datastore, writer resolver.GitWriter, log *zap.Logger, registry *auth.ProviderRegistry, clock apiruntime.Clock, ids apiruntime.IDGenerator) (*gin.Engine, error) {
	if registry == nil || registry.AuthN() == nil {
		return nil, fmt.Errorf("app: auth provider registry is required")
	}
	rootResolver, err := resolver.NewResolver(resolver.ResolverDeps{
		Store:       store,
		GitWriter:   writer,
		AuthZ:       registry.AuthZ(),
		Registry:    registry,
		Logger:      log,
		Clock:       clock,
		IDGenerator: ids,
	})
	if err != nil {
		return nil, err
	}
	schema := generated.NewExecutableSchema(generated.Config{Resolvers: rootResolver})
	gqlServer := gqlhandler.New(schema)

	gqlServer.AddTransport(transport.Websocket{
		KeepAlivePingInterval: 10 * time.Second,
	})
	gqlServer.AddTransport(transport.Options{})
	gqlServer.AddTransport(transport.GET{})
	gqlServer.AddTransport(transport.POST{})
	gqlServer.AddTransport(transport.MultipartForm{})

	gqlServer.SetQueryCache(lru.New[*ast.QueryDocument](1000))

	gqlServer.Use(extension.Introspection{})
	gqlServer.Use(extension.AutomaticPersistedQuery{
		Cache: lru.New[string](100),
	})

	gqlHandler := gin.HandlerFunc(func(c *gin.Context) {
		gqlServer.ServeHTTP(c.Writer, c.Request)
	})

	authenticateMiddleware := security.NewAuthenticate(registry, log)
	rateLimitMiddleware := security.NewRateLimit(10, 20)
	requestIdMiddleware := middleware.NewRequestId(ids)

	r := gin.New()
	r.Use(gin.Recovery())
	r.Use(requestIdMiddleware.RequestIdInserter)
	r.Use(security.CorsConfiguration())
	r.Use(rateLimitMiddleware.RateLimiter)
	r.GET("/playground", playgroundHandler)
	routerGroup := r.Group("/")
	routerGroup.Use(authenticateMiddleware.Authenticator)
	routerGroup.Use(security.SecureHeaders)
	routerGroup.POST("/graphql", gqlHandler)
	return r, nil
}

func playgroundHandler(c *gin.Context) {
	h := playground.Handler("GraphQL Playground", "/graphql")
	h.ServeHTTP(c.Writer, c.Request)
}

func healthHandler(router *gin.Engine, store datastore.Datastore, log *zap.Logger, clock apiruntime.Clock) *gin.Engine {
	healthHandler := health.NewHandler(health.HandlerDeps{
		Store:   store,
		Logger:  log,
		Version: version,
		Clock:   clock,
	})

	router.GET("/health", healthHandler.Health)
	router.GET("/ready", healthHandler.Ready)
	router.GET("/metrics", healthHandler.Metrics)
	return router
}

// buildProviderRegistry constructs a ProviderRegistry from the application config.
// It reads authn chain, authz provider, and userdir provider from the resolved config.
// The second return value is non-nil when rbac-local is active — callers may use it
// for SIGHUP-triggered policy reloads. The third return value lists providers that
// own background goroutines and must be shut down when the server stops.
func buildProviderRegistry(cfg *config.Config, log *zap.Logger) (*auth.ProviderRegistry, policyReloader, []providerShutdowner, error) {
	// Build AuthN providers in chain order.
	chain := cfg.Auth.AuthN.Chain
	if len(chain) == 0 {
		chain = []string{"static-admin", "anonymous"}
	}

	var authnProviders []auth.AuthNProvider
	var shutdowns []providerShutdowner
	for _, name := range chain {
		switch name {
		case "static-admin":
			p, err := staticadmin.New(cfg.Auth, log)
			if err != nil {
				return nil, nil, nil, fmt.Errorf("init static-admin provider: %w", err)
			}
			authnProviders = append(authnProviders, p)
			shutdowns = append(shutdowns, p)
		case "anonymous":
			authnProviders = append(authnProviders, anonymous.New())
		default:
			return nil, nil, nil, fmt.Errorf("unknown authn provider %q", name)
		}
	}

	// Build AuthZ provider.
	var authzProvider auth.AuthZProvider
	var reloader policyReloader
	switch cfg.Auth.AuthZ.Provider {
	case "rbac-local":
		p, err := rbaclocal.New(cfg.Auth.RBAC, log)
		if err != nil {
			return nil, nil, nil, fmt.Errorf("init rbac-local authz provider: %w", err)
		}
		authzProvider = p
		reloader = p
	case "allow-all", "":
		// Default to allow-all so existing deployments without explicit config are unaffected.
		authzProvider = allowall.New(log)
	default:
		return nil, nil, nil, fmt.Errorf("unknown authz provider %q", cfg.Auth.AuthZ.Provider)
	}

	// Build UserDir provider.
	userdirProvider := userdirnone.New()

	return auth.NewProviderRegistry(auth.NewChainedAuthN(authnProviders...), authzProvider, userdirProvider), reloader, shutdowns, nil
}

// Start starts all servers in background goroutines.
func (s *Server) Start() {
	// Listen for SIGHUP to trigger a live policy reload on rbac-local.
	if s.rbacReloader != nil {
		sigCh := make(chan os.Signal, 1)
		signal.Notify(sigCh, syscall.SIGHUP)
		go func() {
			for range sigCh {
				if err := s.rbacReloader.Reload(); err != nil {
					s.log.Error("rbac-local policy reload failed", zap.Error(err))
				} else {
					s.log.Info("rbac-local policy reloaded")
				}
			}
		}()
	}

	go func() {
		s.log.Info("GraphQL API server starting", zap.Int("port", s.cfg.Api.Port))
		if err := s.httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			s.log.Fatal("Server error", zap.Error(err))
		}
	}()

	go func() {
		s.log.Info("Git smart HTTP server starting", zap.Int("port", s.cfg.Api.GitPort))
		if err := s.gitServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			s.log.Error("Git HTTP server error", zap.Error(err))
		}
	}()

	go func() {
		s.log.Info("CatalogService gRPC server starting", zap.Int("port", s.cfg.Api.GrpcPort))
		if err := s.grpcServer.Serve(s.grpcListener); err != nil {
			s.log.Error("CatalogService gRPC server error", zap.Error(err))
		}
	}()

	s.log.Info("Server ready",
		zap.String("graphql", fmt.Sprintf("http://localhost:%d/graphql", s.cfg.Api.Port)),
		zap.String("playground", fmt.Sprintf("http://localhost:%d/playground", s.cfg.Api.Port)),
		zap.String("health", fmt.Sprintf("http://localhost:%d/health", s.cfg.Api.Port)),
		zap.String("ready", fmt.Sprintf("http://localhost:%d/ready", s.cfg.Api.Port)),
		zap.String("git_http", fmt.Sprintf("http://localhost:%d", s.cfg.Api.GitPort)),
		zap.String("catalog_grpc", fmt.Sprintf("localhost:%d", s.cfg.Api.GrpcPort)),
	)
}

// Shutdown gracefully stops all network servers.
func (s *Server) Shutdown(ctx context.Context) {
	if err := s.httpServer.Shutdown(ctx); err != nil {
		s.log.Error("Server shutdown error", zap.Error(err))
	}
	if err := s.gitServer.Shutdown(ctx); err != nil {
		s.log.Error("Git HTTP server shutdown error", zap.Error(err))
	}
	s.grpcServer.GracefulStop()
}

// Close releases non-server resources.
func (s *Server) Close() {
	for _, p := range s.providerShutdown {
		p.Shutdown()
	}
	if s.grpcListener != nil {
		_ = s.grpcListener.Close()
	}
	if s.gitClient != nil {
		_ = s.gitClient.Close()
	}
	if s.store != nil {
		_ = s.store.Close()
	}
}
