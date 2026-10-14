// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package app

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/pprof"
	"runtime"
	"strconv"
	"sync"
	"time"

	"github.com/gitstore-dev/gitstore/api/internal/config"
	"go.uber.org/zap"
)

// StartCapacityProfiler exposes bounded profiles only on an explicitly selected
// loopback listener. It is separate from the public API and Git HTTP routers.
func StartCapacityProfiler(cfg config.ProfilingConfig, logger *zap.Logger) (func(context.Context) error, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	address := cfg.ListenAddr
	if address == "" {
		return func(context.Context) error { return nil }, nil
	}
	listener, err := net.Listen("tcp", address)
	if err != nil {
		return nil, fmt.Errorf("listen for capacity profiles: %w", err)
	}
	server := &http.Server{
		Handler: capacityProfileHandler(), ReadHeaderTimeout: 2 * time.Second,
		WriteTimeout: 65 * time.Second, IdleTimeout: 5 * time.Second,
		MaxHeaderBytes: 8 * 1024,
	}
	go func() {
		if err := server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("capacity profiling listener failed", zap.Error(err))
		}
	}()
	logger.Info("loopback capacity profiler enabled")
	return server.Shutdown, nil
}

func capacityProfileHandler() http.Handler {
	var capture sync.Mutex
	mux := http.NewServeMux()
	mux.HandleFunc("GET /debug/pprof/profile", func(w http.ResponseWriter, r *http.Request) {
		seconds, err := strconv.Atoi(r.URL.Query().Get("seconds"))
		if err != nil || seconds < 1 || seconds > 60 {
			http.Error(w, "profile duration must be 1..60 seconds", http.StatusBadRequest)
			return
		}
		if !capture.TryLock() {
			http.Error(w, "profile capture already active", http.StatusConflict)
			return
		}
		defer capture.Unlock()
		previousMutexFraction := runtime.SetMutexProfileFraction(5)
		runtime.SetBlockProfileRate(1_000_000)
		defer func() {
			runtime.SetMutexProfileFraction(previousMutexFraction)
			runtime.SetBlockProfileRate(0)
		}()
		pprof.Profile(w, r)
	})
	mux.HandleFunc("GET /debug/pprof/trace", func(w http.ResponseWriter, r *http.Request) {
		seconds, err := strconv.Atoi(r.URL.Query().Get("seconds"))
		if err != nil || seconds < 1 || seconds > 5 {
			http.Error(w, "trace duration must be 1..5 seconds", http.StatusBadRequest)
			return
		}
		if !capture.TryLock() {
			http.Error(w, "profile capture already active", http.StatusConflict)
			return
		}
		defer capture.Unlock()
		pprof.Trace(w, r)
	})
	for _, profile := range []string{"heap", "allocs", "block", "mutex", "goroutine"} {
		handler := pprof.Handler(profile)
		mux.HandleFunc("GET /debug/pprof/"+profile, func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Query().Get("seconds") != "" {
				http.Error(w, "delta profiles are not supported", http.StatusBadRequest)
				return
			}
			if debug := r.URL.Query().Get("debug"); debug != "" && debug != "0" {
				http.Error(w, "only binary profiles are supported", http.StatusBadRequest)
				return
			}
			if !capture.TryLock() {
				http.Error(w, "profile capture already active", http.StatusConflict)
				return
			}
			defer capture.Unlock()
			handler.ServeHTTP(w, r)
		})
	}
	return mux
}
