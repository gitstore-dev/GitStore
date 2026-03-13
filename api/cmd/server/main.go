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
	"github.com/commerce-projects/gitstore/api/internal/cache"
	"github.com/commerce-projects/gitstore/api/internal/catalog"
	"github.com/commerce-projects/gitstore/api/internal/graph"
	"github.com/commerce-projects/gitstore/api/internal/graph/generated"
	"github.com/commerce-projects/gitstore/api/internal/handler"
	"github.com/commerce-projects/gitstore/api/internal/health"
	"github.com/commerce-projects/gitstore/api/internal/logger"
	"github.com/commerce-projects/gitstore/api/internal/middleware"
	"github.com/commerce-projects/gitstore/api/internal/websocket"
	"go.uber.org/zap"
)

func main() {
	// Parse command-line flags
	port := flag.Int("port", getEnvInt("GITSTORE_API_PORT", 4000), "API server port")
	gitWS := flag.String("git-ws", getEnv("GITSTORE_GIT_WS", "ws://localhost:8080"), "Git server websocket URL")
	gitRepo := flag.String("git-repo", getEnv("GITSTORE_GIT_REPO", "/data/repos/catalog.git"), "Git repository path")
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
		zap.String("git_repo", *gitRepo),
		zap.String("git_server_url", *gitServerURL),
		zap.Int("cache_ttl", *cacheTTL),
	)

	// Create catalog loader
	catalogLoader := catalog.NewLoader(*gitRepo, logger.Log)

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
			zap.String("repo", *gitRepo),
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

	// Create GraphQL resolver
	resolver := graph.NewResolver(cacheManager, *gitRepo, *gitServerURL)
	gqlServer := gqlhandler.NewDefaultServer(generated.NewExecutableSchema(generated.Config{Resolvers: resolver}))

	// Create health check handler
	healthHandler := health.NewHandler(cacheManager, logger.Log, "1.0.0")

	// Create HTTP server
	mux := http.NewServeMux()

	// Authentication endpoints
	loginHandler := handler.NewLoginHandler(authMiddleware, logger.Log)
	mux.Handle("/api/login", loginHandler)

	// GraphQL endpoint
	mux.Handle("/graphql", gqlServer)

	// Playground endpoint
	mux.Handle("/playground", playground.Handler("GraphQL Playground", "/graphql"))

	// Health check endpoints
	mux.HandleFunc("/health", healthHandler.Health)
	mux.HandleFunc("/ready", healthHandler.Ready)
	mux.HandleFunc("/healthz", healthHandler.Healthz) // Backwards compatibility

	// Apply middleware
	var handler http.Handler = mux
	handler = middleware.CORSMiddleware(handler)
	handler = middleware.RequestIDMiddleware(handler)

	// Create HTTP server
	srv := &http.Server{
		Addr:         fmt.Sprintf(":%d", *port),
		Handler:      handler,
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
