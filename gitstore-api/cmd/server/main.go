// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

// GraphQL API Server Main Entry Point

package main

import (
	"context"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	gqlhandler "github.com/99designs/gqlgen/graphql/handler"
	"github.com/99designs/gqlgen/graphql/playground"
	"github.com/gitstore-dev/gitstore/api/internal/cache"
	"github.com/gitstore-dev/gitstore/api/internal/catalog"
	"github.com/gitstore-dev/gitstore/api/internal/gitclient"
	"github.com/gitstore-dev/gitstore/api/internal/graph"
	"github.com/gitstore-dev/gitstore/api/internal/graph/generated"
	"github.com/gitstore-dev/gitstore/api/internal/handler"
	"github.com/gitstore-dev/gitstore/api/internal/health"
	"github.com/gitstore-dev/gitstore/api/internal/loader"
	"github.com/gitstore-dev/gitstore/api/internal/logger"
	"github.com/gitstore-dev/gitstore/api/internal/middleware"
	"github.com/gitstore-dev/gitstore/api/internal/websocket"
	"go.uber.org/zap"
)

func main() {
	// Parse command-line flags
	port := flag.Int("port", getEnvInt("GITSTORE_API_PORT", 4000), "API server port")
	gitWS := flag.String("git-ws", getEnv("GITSTORE_GIT_WS", "ws://localhost:8080"), "Git server websocket URL")
	gitGRPC := flag.String("git-grpc", getEnv("GITSTORE_GIT_GRPC", ""), "Git service gRPC address (host:port)")
	gitServerURL := flag.String("git-server-url", getEnv("GITSTORE_GIT_SERVER_URL", "http://localhost:9418"), "Git server HTTP URL")
	cacheTTL := flag.Int("cache-ttl", getEnvInt("GITSTORE_CACHE_TTL", 300), "Cache TTL in seconds")
	flag.Parse()

	// Initialize structured logging
	if err := logger.InitLogger(); err != nil {
		fmt.Fprintf(os.Stderr, "Failed to initialize logger: %v\n", err)
		os.Exit(1)
	}
	defer logger.Sync()

	logger.Log.Info("Starting GitStore GraphQL API",
		zap.Int("port", *port),
		zap.String("git_ws", *gitWS),
		zap.String("git_grpc", *gitGRPC),
		zap.String("git_server_url", *gitServerURL),
		zap.Int("cache_ttl", *cacheTTL),
	)

	// Dial git-service via gRPC
	if *gitGRPC == "" {
		logger.Log.Fatal("GITSTORE_GIT_GRPC is required; set to git-service host:port")
	}
	gitClient, err := gitclient.NewClientWithAddr(*gitGRPC)
	if err != nil {
		logger.Log.Fatal("Failed to connect to git-service", zap.Error(err))
	}
	defer gitClient.Close()

	// Create catalog loader backed by gRPC
	catalogLoader := catalog.NewGRPCLoader(gitClient, logger.Log)

	// Create cache manager
	cacheManager := cache.NewManager(
		catalogLoader,
		logger.Log,
		time.Duration(*cacheTTL)*time.Second,
	)

	// Pre-load catalog
	ctx := context.Background()
	logger.Log.Info("Pre-loading catalog...")
	if _, err := cacheManager.Get(ctx); err != nil {
		logger.Log.Error("Failed to load initial catalog",
			zap.Error(err),
			zap.String("grpc", *gitGRPC),
		)
		logger.Log.Warn("API will continue but queries will fail until catalog loads")
	} else {
		logger.Log.Info("Initial catalog loaded successfully")
	}

	// Start websocket client for git notifications
	wsClient := websocket.NewClient(*gitWS, func(event websocket.GitEvent) {
		logger.Log.Info("Received git event, invalidating cache",
			zap.String("event", event.Event),
			zap.String("tag", event.Tag),
		)
		cacheManager.Invalidate()

		// Trigger immediate reload
		go func() {
			if _, err := cacheManager.Get(context.Background()); err != nil {
				logger.Log.Error("Failed to reload catalog", zap.Error(err))
			}
		}()
	}, logger.Log)

	// Start websocket client in background
	wsCtx, wsCancel := context.WithCancel(context.Background())
	defer wsCancel()

	go func() {
		if err := wsClient.Start(wsCtx); err != nil && err != context.Canceled {
			logger.Log.Error("Websocket client error", zap.Error(err))
		}
	}()

	// Create auth middleware
	authMiddleware, err := middleware.NewAuthMiddleware()
	if err != nil {
		logger.Log.Fatal("Failed to create auth middleware", zap.Error(err))
	}

	// Create GraphQL resolver (backed by the same gRPC client used for reads)
	resolver := graph.NewResolver(cacheManager, gitClient, logger.Log)
	schema := generated.NewExecutableSchema(generated.Config{Resolvers: resolver})
	gqlServer := gqlhandler.NewDefaultServer(schema)

	// Wrap GraphQL handler with DataLoader middleware
	gqlHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Get current catalog for this request
		cat, err := cacheManager.Get(r.Context())
		if err != nil {
			http.Error(w, "Failed to load catalog", http.StatusInternalServerError)
			return
		}

		// Add DataLoaders to context
		ctx := loader.Middleware(cat, logger.Log)(r.Context())
		r = r.WithContext(ctx)

		// Serve GraphQL
		gqlServer.ServeHTTP(w, r)
	})

	// Create health check handler
	healthHandler := health.NewHandler(cacheManager, logger.Log, "1.0.0")

	// Create HTTP server
	mux := http.NewServeMux()

	// Authentication endpoints
	loginHandler := handler.NewLoginHandler(authMiddleware, logger.Log)
	mux.Handle("/api/login", loginHandler)

	// GraphQL endpoint (with DataLoader middleware)
	mux.Handle("/graphql", gqlHandler)

	// Playground endpoint
	mux.Handle("/playground", playground.Handler("GraphQL Playground", "/graphql"))

	// Health check endpoints
	mux.HandleFunc("/health", healthHandler.Health)
	mux.HandleFunc("/ready", healthHandler.Ready)

	// Apply middleware
	var httpHandler http.Handler = mux
	httpHandler = middleware.CORSMiddleware(httpHandler)
	httpHandler = middleware.RequestIDMiddleware(httpHandler)

	// Create HTTP server
	srv := &http.Server{
		Addr:         fmt.Sprintf(":%d", *port),
		Handler:      httpHandler,
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 15 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	// Start server in background
	go func() {
		logger.Log.Info("GraphQL API server starting", zap.Int("port", *port))
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logger.Log.Fatal("Server error", zap.Error(err))
		}
	}()

	logger.Log.Info("Server ready",
		zap.String("graphql", fmt.Sprintf("http://localhost:%d/graphql", *port)),
		zap.String("playground", fmt.Sprintf("http://localhost:%d/playground", *port)),
		zap.String("health", fmt.Sprintf("http://localhost:%d/health", *port)),
		zap.String("ready", fmt.Sprintf("http://localhost:%d/ready", *port)),
	)

	// Wait for interrupt signal
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)
	<-sigChan

	logger.Log.Info("Shutting down gracefully...")

	// Graceful shutdown
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := srv.Shutdown(shutdownCtx); err != nil {
		logger.Log.Error("Server shutdown error", zap.Error(err))
	}

	wsCancel()
	wsClient.Close()

	logger.Log.Info("Server stopped")
}

func getEnv(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

func getEnvInt(key string, fallback int) int {
	if value := os.Getenv(key); value != "" {
		var intVal int
		if _, err := fmt.Sscanf(value, "%d", &intVal); err == nil {
			return intVal
		}
	}
	return fallback
}
