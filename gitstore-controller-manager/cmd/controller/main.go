// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/gitstore-dev/gitstore/controller-manager/internal/api"
	"github.com/gitstore-dev/gitstore/controller-manager/internal/config"
	"github.com/gitstore-dev/gitstore/controller-manager/internal/health"
	"github.com/gitstore-dev/gitstore/controller-manager/internal/manager"
	"go.uber.org/zap"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to load config: %v\n", err)
		os.Exit(1)
	}

	log, err := manager.InitLogger(cfg.Log.Level, cfg.Log.Format)
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to init logger: %v\n", err)
		os.Exit(1)
	}
	defer log.Sync() //nolint:errcheck

	mgr := manager.New().WithLogger(log)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	addr := fmt.Sprintf(":%d", cfg.Controller.Port)
	srv := &http.Server{
		Addr:    addr,
		Handler: buildMux(mgr),
	}

	go func() {
		log.Info("HTTP server listening", zap.String("addr", addr))
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Error("HTTP server error", zap.Error(err))
		}
	}()

	log.Info("controller-manager started", zap.String("apiURI", cfg.Controller.ApiURI))
	if err := mgr.Start(ctx); err != nil {
		log.Error("manager exited with error", zap.Error(err))
	}

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer shutdownCancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		log.Warn("HTTP server shutdown error", zap.Error(err))
	}
	log.Info("controller-manager stopped")
}

// buildMux returns the HTTP handler for the health/metrics and management surface.
func buildMux(mgr *manager.Manager) http.Handler {
	mux := http.NewServeMux()
	mux.Handle("GET /health", health.NewHandler(mgr))
	mux.Handle("GET /metrics", health.NewMetricsHandler(mgr))
	mux.HandleFunc("GET /controller/v1/poison/{kind}", api.ListPoisonHandler(mgr))
	mux.HandleFunc("POST /controller/v1/poison/{kind}/{namespace}/{name}/requeue", api.RequeuePoisonHandler(mgr))
	return mux
}
