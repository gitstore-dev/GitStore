// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package app

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"time"

	gqlhandler "github.com/99designs/gqlgen/graphql/handler"
	"github.com/99designs/gqlgen/graphql/playground"
	catalogv1 "github.com/gitstore-dev/gitstore/api/gen/gitstore/catalog/v1"
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
	"go.uber.org/zap"
	"google.golang.org/grpc"
)

const version = "1.0.0"

// Server is the composed API runtime.
type Server struct {
	cfg          *config.Config
	log          *zap.Logger
	store        datastore.Datastore
	gitClient    *gitclient.Client
	httpServer   *http.Server
	gitServer    *http.Server
	grpcServer   *grpc.Server
	grpcListener net.Listener
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

	gqlHandler, err := NewGraphQLHandler(store, gitClient, log, authMiddleware, clock, nil)
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
		cfg:          cfg,
		log:          log,
		store:        store,
		gitClient:    gitClient,
		httpServer:   httpServer,
		gitServer:    gitServer,
		grpcServer:   grpcServer,
		grpcListener: grpcListener,
	}, nil
}

// NewGraphQLHandler builds a GraphQL HTTP handler.
func NewGraphQLHandler(store datastore.Datastore, writer resolver.GitWriter, log *zap.Logger, authMiddleware *middleware.AuthMiddleware, clock apiruntime.Clock, ids apiruntime.IDGenerator) (http.Handler, error) {
	rootResolver, err := resolver.NewResolver(resolver.ResolverDeps{
		Store:       store,
		GitWriter:   writer,
		Logger:      log,
		Clock:       clock,
		IDGenerator: ids,
	})
	if err != nil {
		return nil, err
	}
	rootResolver.WithAuthMiddleware(authMiddleware)
	schema := generated.NewExecutableSchema(generated.Config{Resolvers: rootResolver})
	gqlServer := gqlhandler.NewDefaultServer(schema)

	gqlHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gqlServer.ServeHTTP(w, r)
	})

	return authMiddleware.OptionalAuth(gqlHandler), nil
}

// Start starts all servers in background goroutines.
func (s *Server) Start() {
	go func() {
		s.log.Info("GraphQL API server starting", zap.Int("port", s.cfg.Api.Port))
		if err := s.httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			s.log.Fatal("Server error", zap.Error(err))
		}
	}()

	go func() {
		s.log.Info("Git smart HTTP server starting", zap.Int("port", s.cfg.Api.GitPort))
		if err := s.gitServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
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
