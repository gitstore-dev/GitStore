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
	"strings"
	"syscall"
	"time"

	gqlhandler "github.com/99designs/gqlgen/graphql/handler"
	"github.com/99designs/gqlgen/graphql/handler/extension"
	"github.com/99designs/gqlgen/graphql/handler/lru"
	"github.com/99designs/gqlgen/graphql/handler/transport"
	"github.com/99designs/gqlgen/graphql/playground"
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
	"github.com/gitstore-dev/gitstore/api/internal/handler"
	"github.com/gitstore-dev/gitstore/api/internal/health"
	"github.com/gitstore-dev/gitstore/api/internal/middleware"
	apiruntime "github.com/gitstore-dev/gitstore/api/internal/runtime"
	"github.com/spf13/viper"
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

	gitClient, err := gitclient.NewClientWithAddr(cfg.Git.Grpc.Uri)
	if err != nil {
		store.Close()
		return nil, fmt.Errorf("connect git-service: %w", err)
	}

	authMiddleware, err := middleware.NewAuthMiddleware(middleware.AuthDeps{
		AdminUsername:     cfg.Auth.Admin.Username,
		AdminPasswordHash: cfg.Auth.Admin.Password,
		JWTSecret:         cfg.Auth.JWT.Secret,
		JWTDuration:       cfg.Auth.JWT.Duration,
		JWTIssuer:         cfg.Auth.JWT.Issuer,
	})
	if err != nil {
		gitClient.Close()
		store.Close()
		return nil, fmt.Errorf("create auth middleware: %w", err)
	}

	registry, rbacReloader, providerShutdowns, err := buildProviderRegistry(cfg, log)
	if err != nil {
		gitClient.Close()
		store.Close()
		return nil, fmt.Errorf("build auth provider registry: %w", err)
	}
	log.Info("auth providers ready",
		zap.String("authn_chain", strings.Join(cfg.Auth.AuthN.Chain, ",")),
		zap.String("authz_provider", cfg.Auth.AuthZ.Provider),
		zap.String("userdir_provider", cfg.Auth.UserDir.Provider),
	)

	gqlHandler, err := NewGraphQLHandler(store, gitClient, log, authMiddleware, registry, clock, nil)
	if err != nil {
		gitClient.Close()
		store.Close()
		return nil, err
	}

	healthHandler := health.NewHandler(health.HandlerDeps{
		Store:   store,
		Logger:  log,
		Version: version,
		Clock:   clock,
	})

	mux := http.NewServeMux()
	mux.Handle("/api/login", handler.NewLoginHandler(handler.LoginHandlerDeps{
		Auth:   authMiddleware,
		Logger: log,
		Clock:  clock,
	}))
	mux.Handle("/graphql", gqlHandler)
	mux.Handle("/playground", playground.Handler("GraphQL Playground", "/graphql"))
	mux.HandleFunc("/health", healthHandler.Health)
	mux.HandleFunc("/ready", healthHandler.Ready)

	var httpHandler http.Handler = mux
	httpHandler = middleware.CORSMiddleware(httpHandler)
	httpHandler = middleware.RequestIDMiddleware(httpHandler)

	httpServer := &http.Server{
		Addr:         fmt.Sprintf(":%d", cfg.Api.Port),
		Handler:      httpHandler,
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 15 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	gitResolver := func(namespace, repo string) (string, bool) {
		ctx := context.Background()
		ns, err := store.GetNamespaceByIdentifier(ctx, namespace)
		if err != nil || ns == nil {
			return "", false
		}
		mapping, err := store.LookupRepository(ctx, ns.ID, repo)
		if err != nil || mapping == nil {
			return "", false
		}
		return mapping.RepoID, true
	}
	gitMux := githttp.NewMux(gitClient, gitResolver, log, http.HandlerFunc(healthHandler.Health))
	gitServer := &http.Server{
		Addr:         fmt.Sprintf(":%d", cfg.Api.GitPort),
		Handler:      middleware.RequestIDMiddleware(gitMux),
		ReadTimeout:  0,
		WriteTimeout: 0,
		IdleTimeout:  60 * time.Second,
	}

	grpcListener, err := net.Listen("tcp", fmt.Sprintf(":%d", cfg.Api.GrpcPort))
	if err != nil {
		gitClient.Close()
		store.Close()
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
		grpcListener.Close()
		gitClient.Close()
		store.Close()
		return nil, err
	}
	catalogv1.RegisterCatalogServiceServer(grpcServer, catalogServer)

	return &Server{
		cfg:              cfg,
		log:              log,
		store:            store,
		gitClient:        gitClient,
		httpServer:       httpServer,
		gitServer:        gitServer,
		grpcServer:       grpcServer,
		grpcListener:     grpcListener,
		rbacReloader:     rbacReloader,
		providerShutdown: providerShutdowns,
	}, nil
}

// NewGraphQLHandler builds a GraphQL HTTP handler.
func NewGraphQLHandler(store datastore.Datastore, writer resolver.GitWriter, log *zap.Logger, authMiddleware *middleware.AuthMiddleware, registry *auth.ProviderRegistry, clock apiruntime.Clock, ids apiruntime.IDGenerator) (http.Handler, error) {
	var authzProvider auth.AuthZProvider
	if registry != nil {
		authzProvider = registry.AuthZ()
	}
	rootResolver, err := resolver.NewResolver(resolver.ResolverDeps{
		Store:       store,
		GitWriter:   writer,
		AuthZ:       authzProvider,
		Registry:    registry,
		Logger:      log,
		Clock:       clock,
		IDGenerator: ids,
	})
	if err != nil {
		return nil, err
	}
	rootResolver.WithAuthMiddleware(authMiddleware)
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

	gqlHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gqlServer.ServeHTTP(w, r)
	})

	// Use registry-based chain if available; fall back to legacy OptionalAuth.
	if registry != nil {
		return middleware.ChainAuthMiddleware(registry, log)(gqlHandler), nil
	}
	return authMiddleware.OptionalAuth(gqlHandler), nil
}

// buildProviderRegistry constructs a ProviderRegistry from the application config.
// It reads authn chain, authz provider, and userdir provider from cfg and environment.
// The second return value is non-nil when rbac-local is active — callers may use it
// for SIGHUP-triggered policy reloads. The third return value lists providers that
// own background goroutines and must be shut down when the server stops.
func buildProviderRegistry(cfg *config.Config, log *zap.Logger) (*auth.ProviderRegistry, policyReloader, []providerShutdowner, error) {
	// Build a viper instance with the same env-var scheme so provider constructors
	// can read their config using viper.GetString("auth.xxx") with the GITSTORE__ prefix.
	v := viper.New()
	v.SetEnvPrefix("GITSTORE")
	v.SetEnvKeyReplacer(strings.NewReplacer(".", "__"))
	v.AutomaticEnv()

	// Seed values from the already-parsed config struct so defaults propagate even
	// when env vars are absent.
	v.SetDefault("auth.admin.username", cfg.Auth.Admin.Username)
	v.SetDefault("auth.admin.password_hash", cfg.Auth.Admin.Password)
	v.SetDefault("auth.jwt.secret", cfg.Auth.JWT.Secret)
	v.SetDefault("auth.jwt.duration", cfg.Auth.JWT.Duration)
	v.SetDefault("auth.jwt.issuer", cfg.Auth.JWT.Issuer)
	v.SetDefault("auth.authn.chain", cfg.Auth.AuthN.Chain)
	v.SetDefault("auth.authz.provider", cfg.Auth.AuthZ.Provider)
	v.SetDefault("auth.userdir.provider", cfg.Auth.UserDir.Provider)
	v.SetDefault("auth.rbac.policy_file", cfg.Auth.RBAC.PolicyFile)

	// Build AuthN providers in chain order.
	chain := v.GetStringSlice("auth.authn.chain")
	if len(chain) == 0 {
		chain = []string{"static-admin", "anonymous"}
	}

	var authnProviders []auth.AuthNProvider
	var shutdowns []providerShutdowner
	for _, name := range chain {
		switch name {
		case "static-admin":
			p, err := staticadmin.New(v, log)
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
	switch v.GetString("auth.authz.provider") {
	case "rbac-local":
		p, err := rbaclocal.New(v, log)
		if err != nil {
			return nil, nil, nil, fmt.Errorf("init rbac-local authz provider: %w", err)
		}
		authzProvider = p
		reloader = p
	case "allow-all", "":
		// Default to allow-all so existing deployments without explicit config are unaffected.
		authzProvider = allowall.New(log)
	default:
		return nil, nil, nil, fmt.Errorf("unknown authz provider %q", v.GetString("auth.authz.provider"))
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
		s.gitClient.Close()
	}
	if s.store != nil {
		s.store.Close()
	}
}
