// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package observability

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/pprof"
	"runtime"
	runtimetrace "runtime/trace"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"go.opentelemetry.io/otel/trace"
	"go.uber.org/zap"
)

var capacityProfileCapture sync.Mutex
var runtimeTraceSamples atomic.Int64

// TakeRuntimeTraceSample claims one explicitly requested diagnostic sample
// during this process's loopback execution capture, never during ordinary work.
func TakeRuntimeTraceSample(ctx context.Context) bool {
	if runtimeTraceSamples.Load() == 0 || !runtimetrace.IsEnabled() {
		return false
	}
	span := trace.SpanFromContext(ctx)
	if !span.IsRecording() || !span.SpanContext().IsSampled() {
		return false
	}
	for remaining := runtimeTraceSamples.Load(); remaining > 0; remaining = runtimeTraceSamples.Load() {
		if runtimeTraceSamples.CompareAndSwap(remaining, remaining-1) {
			return true
		}
	}
	return false
}

// AnnotateRuntimeSpan links the SDK's current runtime task to a sampled,
// recording span without creating another task or changing its lifetime.
func AnnotateRuntimeSpan(ctx context.Context) {
	if !runtimetrace.IsEnabled() {
		return
	}
	span := trace.SpanFromContext(ctx)
	if !span.IsRecording() || !span.SpanContext().IsSampled() {
		return
	}
	runtimetrace.Log(ctx, "otel.trace_id", span.SpanContext().TraceID().String())
	runtimetrace.Log(ctx, "otel.span_id", span.SpanContext().SpanID().String())
}

// StartCapacityProfiler exposes bounded profiles only on an explicitly selected
// loopback listener, separate from the service's public HTTP routers.
func StartCapacityProfiler(cfg ProfilingConfig, logger *zap.Logger) (func(context.Context) error, error) {
	address, err := cfg.ListenAddress()
	if err != nil {
		return nil, err
	}
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
	done := make(chan struct{})
	go func() {
		defer close(done)
		if err := server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("capacity profiling listener failed", zap.Error(err))
		}
	}()
	logger.Info("loopback capacity profiler enabled")
	return func(ctx context.Context) error {
		shutdownErr := server.Shutdown(ctx)
		// Shutdown can run before Serve has registered this listener.
		if err := listener.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
			shutdownErr = errors.Join(shutdownErr, err)
		}
		if shutdownErr != nil {
			shutdownErr = errors.Join(shutdownErr, server.Close())
		}
		select {
		case <-done:
			return shutdownErr
		case <-ctx.Done():
			return errors.Join(shutdownErr, ctx.Err())
		}
	}, nil
}

func capacityProfileHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /debug/pprof/profile", func(w http.ResponseWriter, r *http.Request) {
		seconds, err := strconv.Atoi(r.URL.Query().Get("seconds"))
		if err != nil || seconds < 1 || seconds > 60 {
			http.Error(w, "profile duration must be 1..60 seconds", http.StatusBadRequest)
			return
		}
		if !capacityProfileCapture.TryLock() {
			http.Error(w, "profile capture already active", http.StatusConflict)
			return
		}
		defer capacityProfileCapture.Unlock()
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
		samples := 0
		if values, present := r.URL.Query()["sample_limit"]; present {
			if len(values) != 1 {
				http.Error(w, "sample_limit must be one integer in 0..64", http.StatusBadRequest)
				return
			}
			samples, err = strconv.Atoi(values[0])
			if err != nil || samples < 0 || samples > 64 {
				http.Error(w, "sample_limit must be one integer in 0..64", http.StatusBadRequest)
				return
			}
		}
		if !capacityProfileCapture.TryLock() {
			http.Error(w, "profile capture already active", http.StatusConflict)
			return
		}
		defer capacityProfileCapture.Unlock()
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Content-Disposition", `attachment; filename="trace"`)
		if err := runtimetrace.Start(w); err != nil {
			w.Header().Del("Content-Type")
			w.Header().Del("Content-Disposition")
			http.Error(w, fmt.Sprintf("start execution trace: %v", err), http.StatusInternalServerError)
			return
		}
		runtimeTraceSamples.Store(int64(samples))
		runtimetrace.Log(r.Context(), "gitstore.runtime.sample_limit", strconv.Itoa(samples))
		defer func() {
			runtimeTraceSamples.Store(0)
			runtimetrace.Stop()
		}()
		timer := time.NewTimer(time.Duration(seconds) * time.Second)
		defer timer.Stop()
		select {
		case <-r.Context().Done():
		case <-timer.C:
		}
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
			if !capacityProfileCapture.TryLock() {
				http.Error(w, "profile capture already active", http.StatusConflict)
				return
			}
			defer capacityProfileCapture.Unlock()
			handler.ServeHTTP(w, r)
		})
	}
	return mux
}
