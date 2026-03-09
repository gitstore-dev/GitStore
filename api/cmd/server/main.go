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

	"github.com/99designs/gqlgen/graphql/playground"
	"github.com/yourorg/gitstore/api/internal/cache"
	"github.com/yourorg/gitstore/api/internal/catalog"
	_ "github.com/yourorg/gitstore/api/internal/graph"
	"github.com/yourorg/gitstore/api/internal/logger"
	"github.com/yourorg/gitstore/api/internal/middleware"
	"github.com/yourorg/gitstore/api/internal/websocket"
	"go.uber.org/zap"
)

func main() {
	// Parse command-line flags
	port := flag.Int("port", getEnvInt("GITSTORE_API_PORT", 4000), "API server port")
	gitWS := flag.String("git-ws", getEnv("GITSTORE_GIT_WS", "ws://localhost:8080"), "Git server websocket URL")
	gitRepo := flag.String("git-repo", getEnv("GITSTORE_GIT_REPO", "/data/repos/catalog.git"), "Git repository path")
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

	// TODO: Create GraphQL resolver when gqlgen is run
	// resolver := graph.NewResolver(cacheManager)
	// srv := handler.NewDefaultServer(generated.NewExecutableSchema(generated.Config{Resolvers: resolver}))

	// Create HTTP server
	mux := http.NewServeMux()

	// GraphQL endpoint (placeholder)
	mux.HandleFunc("/graphql", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusServiceUnavailable)
		fmt.Fprintf(w, `{"errors":[{"message":"GraphQL server not yet implemented - gqlgen code generation required"}]}`)
	})

	// Playground endpoint
	mux.Handle("/playground", playground.Handler("GraphQL Playground", "/graphql"))

	// Health check endpoint
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		fmt.Fprintf(w, "OK")
	})

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
		zap.String("healthz", fmt.Sprintf("http://localhost:%d/healthz", *port)),
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
