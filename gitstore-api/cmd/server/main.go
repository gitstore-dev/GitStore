// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

// GraphQL API Server Main Entry Point

package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"net"

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
	"github.com/gitstore-dev/gitstore/api/internal/logger"
	"github.com/gitstore-dev/gitstore/api/internal/middleware"
	"go.uber.org/zap"
	"google.golang.org/grpc"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to load configuration: %v\n", err)
		os.Exit(1)
	}

	if err := logger.InitLogger(cfg.Log.Level, cfg.Log.Format); err != nil {
		fmt.Fprintf(os.Stderr, "Failed to initialize logger: %v\n", err)
		os.Exit(1)
	}
	defer logger.Sync()

	logger.Log.Info("Starting GitStore GraphQL API",
		zap.Int("port", cfg.Api.Port),
		zap.String("git.grpc.uri", cfg.Git.Grpc.Uri),
		zap.String("datastore_backend", cfg.Datastore.Backend),
	)

	// Create datastore backed by configured backend
	store, err := dsfactory.NewDatastore(cfg.Datastore, logger.Log)
	if err != nil {
		logger.Log.Fatal("Failed to create datastore", zap.Error(err))
	}
	store = datastore.NewInstrumentedDatastore(store, cfg.Datastore.Backend, logger.Log)
	defer store.Close()

	// Dial git-service via gRPC
	gitClient, err := gitclient.NewClientWithAddr(cfg.Git.Grpc.Uri)
	if err != nil {
		logger.Log.Fatal("Failed to connect to git-service", zap.Error(err))
	}
	defer gitClient.Close()

	// Create auth middleware
	authMiddleware, err := middleware.NewAuthMiddleware(
		cfg.Auth.Admin.Username,
		cfg.Auth.Admin.Password,
		cfg.Auth.JWT.Secret,
		cfg.Auth.JWT.Duration,
		cfg.Auth.JWT.Issuer,
	)
	if err != nil {
		logger.Log.Fatal("Failed to create auth middleware", zap.Error(err))
	}

	// Create GraphQL resolver
	gqlHandler := newGraphQLHandler(store, gitClient, logger.Log, authMiddleware)

	// Create health check handler
	healthHandler := health.NewHandler(store, logger.Log, "1.0.0")

	// Create HTTP server
	mux := http.NewServeMux()

	loginHandler := handler.NewLoginHandler(authMiddleware, logger.Log)
	mux.Handle("/api/login", loginHandler)
	mux.Handle("/graphql", gqlHandler)
	mux.Handle("/playground", playground.Handler("GraphQL Playground", "/graphql"))
	mux.HandleFunc("/health", healthHandler.Health)
	mux.HandleFunc("/ready", healthHandler.Ready)

	var httpHandler http.Handler = mux
	httpHandler = middleware.CORSMiddleware(httpHandler)
	httpHandler = middleware.RequestIDMiddleware(httpHandler)

	srv := &http.Server{
		Addr:         fmt.Sprintf(":%d", cfg.Api.Port),
		Handler:      httpHandler,
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 15 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	go func() {
		logger.Log.Info("GraphQL API server starting", zap.Int("port", cfg.Api.Port))
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logger.Log.Fatal("Server error", zap.Error(err))
		}
	}()

	// Git smart HTTP server on port cfg.Api.GitPort
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

	gitMux := githttp.NewMux(gitClient, gitResolver, logger.Log, http.HandlerFunc(healthHandler.Health))
	gitSrv := &http.Server{
		Addr:         fmt.Sprintf(":%d", cfg.Api.GitPort),
		Handler:      middleware.RequestIDMiddleware(gitMux),
		ReadTimeout:  0,
		WriteTimeout: 0,
		IdleTimeout:  60 * time.Second,
	}

	go func() {
		logger.Log.Info("Git smart HTTP server starting", zap.Int("port", cfg.Api.GitPort))
		if err := gitSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logger.Log.Error("Git HTTP server error", zap.Error(err))
		}
	}()

	// CatalogService gRPC server — receives hook pipeline callouts from gitstore-git-service.
	grpcLis, err := net.Listen("tcp", fmt.Sprintf(":%d", cfg.Api.GrpcPort))
	if err != nil {
		logger.Log.Fatal("Failed to listen on gRPC port", zap.Int("port", cfg.Api.GrpcPort), zap.Error(err))
	}
	grpcServer := grpc.NewServer()
	catalogv1.RegisterCatalogServiceServer(grpcServer, cataloggrpc.NewServerWithLogger(store, gitClient, logger.Log))
	go func() {
		logger.Log.Info("CatalogService gRPC server starting", zap.Int("port", cfg.Api.GrpcPort))
		if err := grpcServer.Serve(grpcLis); err != nil {
			logger.Log.Error("CatalogService gRPC server error", zap.Error(err))
		}
	}()

	logger.Log.Info("Server ready",
		zap.String("graphql", fmt.Sprintf("http://localhost:%d/graphql", cfg.Api.Port)),
		zap.String("playground", fmt.Sprintf("http://localhost:%d/playground", cfg.Api.Port)),
		zap.String("health", fmt.Sprintf("http://localhost:%d/health", cfg.Api.Port)),
		zap.String("ready", fmt.Sprintf("http://localhost:%d/ready", cfg.Api.Port)),
		zap.String("git_http", fmt.Sprintf("http://localhost:%d", cfg.Api.GitPort)),
		zap.String("catalog_grpc", fmt.Sprintf("localhost:%d", cfg.Api.GrpcPort)),
	)

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)
	<-sigChan

	logger.Log.Info("Shutting down gracefully...")

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := srv.Shutdown(shutdownCtx); err != nil {
		logger.Log.Error("Server shutdown error", zap.Error(err))
	}

	if err := gitSrv.Shutdown(shutdownCtx); err != nil {
		logger.Log.Error("Git HTTP server shutdown error", zap.Error(err))
	}

	grpcServer.GracefulStop()

	logger.Log.Info("Server stopped")
}

func newGraphQLHandler(store datastore.Datastore, writer resolver.GitWriter, log *zap.Logger, authMiddleware *middleware.AuthMiddleware) http.Handler {
	rootResolver := resolver.NewResolver(store, writer, log)
	rootResolver.WithAuthMiddleware(authMiddleware)
	schema := generated.NewExecutableSchema(generated.Config{Resolvers: rootResolver})
	gqlServer := gqlhandler.NewDefaultServer(schema)

	gqlHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gqlServer.ServeHTTP(w, r)
	})

	return authMiddleware.OptionalAuth(gqlHandler)
}
