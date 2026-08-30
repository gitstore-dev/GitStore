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
	"github.com/gitstore-dev/gitstore/api/internal/eventbus"
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

const version = "0.1.0-alpha.2" // x-release-please-version

// eventBusCapacity is the number of recent events retained per resource
// kind for watch-subscription resume (spec 040, research.md R3).
const eventBusCapacity = 1000

// defaultRateLimitPerSecond/defaultRateLimitBurst mirror config.Load's
// api.rate_limit_per_second/api.rate_limit_burst defaults, used as a
// fallback when NewGraphQLHandler is called directly with a zero value
// (e.g. by tests that don't go through config.Load).
const (
	defaultRateLimitPerSecond = 50
	defaultRateLimitBurst     = 100
)

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
	if err := ensureBootstrapResources(context.Background(), store, gitClient, clock, ids, log); err != nil {
		_ = gitClient.Close()
		_ = store.Close()
		return nil, fmt.Errorf("bootstrap resources: %w", err)
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

	// eventBus fans out CategoryTaxonomy admission events to GraphQL watch
	// subscriptions (spec 040). Shared between the gRPC admission path
	// (publisher) and the GraphQL resolvers (subscribers).
	eventBus := eventbus.New(eventBusCapacity)
	namespaceRepositoryFenceMode := resolver.NamespaceRepositoryFenceDisabled
	if cfg.NamespaceRepositoryFenceEnabled() {
		namespaceRepositoryFenceMode = resolver.NamespaceRepositoryFenceEnabled
	}

	gqlRouter, err := NewGraphQLHandler(GraphQLHandlerDeps{
		Store:                        store,
		GitWriter:                    gitClient,
		Logger:                       log,
		Registry:                     registry,
		Clock:                        clock,
		IDs:                          ids,
		EventBus:                     eventBus,
		NamespaceRepositoryFenceMode: namespaceRepositoryFenceMode,
		RateLimitPerSecond:           cfg.Api.RateLimitPerSecond,
		RateLimitBurst:               cfg.Api.RateLimitBurst,
	})
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
		EventBus:  eventBus,
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

// GraphQLHandlerDeps are the dependencies for NewGraphQLHandler.
type GraphQLHandlerDeps struct {
	Store     datastore.Datastore
	GitWriter resolver.GitWriter
	Logger    *zap.Logger
	Registry  *auth.ProviderRegistry
	Clock     apiruntime.Clock
	IDs       apiruntime.IDGenerator
	// EventBus backs the watchCategories/watchResources subscription
	// resolvers (spec 040). Optional — nil disables watch subscriptions.
	EventBus                     *eventbus.Bus
	NamespaceRepositoryFenceMode resolver.NamespaceRepositoryFenceMode
	// RateLimitPerSecond/RateLimitBurst configure the per-client-IP token
	// bucket guarding /graphql. A zero RateLimitPerSecond falls back to
	// defaultRateLimitPerSecond/defaultRateLimitBurst (the same defaults
	// config.Load applies), so callers that leave this zero-valued (e.g.
	// tests) keep working unchanged.
	RateLimitPerSecond float64
	RateLimitBurst     int
}

// NewGraphQLHandler builds a GraphQL HTTP handler.
func NewGraphQLHandler(deps GraphQLHandlerDeps) (*gin.Engine, error) {
	if deps.Registry == nil || deps.Registry.AuthN() == nil || deps.Registry.AuthZ() == nil {
		return nil, fmt.Errorf("app: authn and authz provider registry is required")
	}
	rootResolver, err := resolver.NewResolver(resolver.ResolverDeps{
		Store:                        deps.Store,
		GitWriter:                    deps.GitWriter,
		Registry:                     deps.Registry,
		Logger:                       deps.Logger,
		Clock:                        deps.Clock,
		IDGenerator:                  deps.IDs,
		EventBus:                     deps.EventBus,
		NamespaceRepositoryFenceMode: deps.NamespaceRepositoryFenceMode,
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

	authenticateMiddleware := security.NewAuthenticate(deps.Registry, deps.Logger)
	authorizeMiddleware := security.NewAuthorizeWithStore(deps.Registry, deps.Store, deps.Logger)
	gqlServer.AroundOperations(authenticateMiddleware.GraphQLAuthenticator)
	gqlServer.AroundOperations(authorizeMiddleware.GraphQLAuthorizer)
	gqlServer.AroundFields(authorizeMiddleware.GraphQLFieldAuthorizer)

	gqlHandler := gin.HandlerFunc(func(c *gin.Context) {
		ctx := security.ContextWithRemoteAddr(c.Request.Context(), c.RemoteIP())
		c.Request = c.Request.WithContext(ctx)
		gqlServer.ServeHTTP(c.Writer, c.Request)
	})

	rateLimitPerSecond := deps.RateLimitPerSecond
	if rateLimitPerSecond <= 0 {
		rateLimitPerSecond = defaultRateLimitPerSecond
	}
	rateLimitBurst := deps.RateLimitBurst
	if rateLimitBurst <= 0 {
		rateLimitBurst = defaultRateLimitBurst
	}
	rateLimitMiddleware := security.NewRateLimit(rateLimitPerSecond, rateLimitBurst)
	requestIdMiddleware := middleware.NewRequestId(deps.IDs)

	r := gin.New()
	r.Use(gin.Recovery())
	r.Use(requestIdMiddleware.RequestIdInserter)
	r.Use(security.CorsConfiguration())
	r.Use(rateLimitMiddleware.RateLimiter)
	r.GET("/playground", playgroundHandler)
	r.POST("/graphql", security.SecureHeaders, gqlHandler)
	// GET /graphql serves the graphql-transport-ws WebSocket upgrade for
	// subscriptions (spec 040) — transport.Websocket handles the upgrade
	// itself; a plain GET without the Upgrade header falls through to
	// gqlgen's own "must be POST" response.
	r.GET("/graphql", security.SecureHeaders, gqlHandler)
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
	// DecisionLogger is the required middleware that keeps every AuthZ decision
	// consistent with the pluggable auth architecture audit contract.
	authzProvider = auth.NewDecisionLogger(authzProvider, log)

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
