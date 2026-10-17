// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package observability

import (
	"compress/gzip"
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"regexp"
	"runtime"
	runtimetrace "runtime/trace"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
	"go.uber.org/zap"
)

func TestRuntimeSpanCorrelatesOnlyRecordedSpans(t *testing.T) {
	file, err := os.CreateTemp(t.TempDir(), "runtime-task-*.trace")
	require.NoError(t, err)
	closed := false
	t.Cleanup(func() {
		if !closed {
			require.NoError(t, file.Close())
		}
	})
	require.NoError(t, runtimetrace.Start(file))
	t.Cleanup(runtimetrace.Stop)
	provider := sdktrace.NewTracerProvider(sdktrace.WithSampler(sdktrace.AlwaysSample()))
	t.Cleanup(func() { require.NoError(t, provider.Shutdown(context.Background())) })
	ctx, span := provider.Tracer("test").Start(context.Background(), "runtime-task")
	defer span.End()
	type privateKey struct{}
	ctx = context.WithValue(ctx, privateKey{}, "private-context-detail")
	ctx, cancel := context.WithCancel(ctx)
	AnnotateRuntimeSpan(ctx)
	require.Equal(t, span.SpanContext(), trace.SpanContextFromContext(ctx))
	require.Equal(t, "private-context-detail", ctx.Value(privateKey{}))
	cancel()
	require.ErrorIs(t, ctx.Err(), context.Canceled)
	span.End()
	for _, ignored := range []context.Context{
		ctx, context.Background(), trace.ContextWithRemoteSpanContext(context.Background(), span.SpanContext()),
	} {
		AnnotateRuntimeSpan(ignored)
	}
	unsampled := sdktrace.NewTracerProvider(sdktrace.WithSampler(sdktrace.NeverSample()))
	t.Cleanup(func() { require.NoError(t, unsampled.Shutdown(context.Background())) })
	unsampledCtx, unsampledSpan := unsampled.Tracer("test").Start(context.Background(), "ignored-runtime-task")
	AnnotateRuntimeSpan(unsampledCtx)
	unsampledSpan.End()
	runtimetrace.Stop()
	require.NoError(t, file.Close())
	closed = true
	info, err := os.Stat(file.Name())
	require.NoError(t, err)
	require.Less(t, info.Size(), int64(8<<20))
	commandCtx, stop := context.WithTimeout(t.Context(), 15*time.Second)
	defer stop()
	output, err := exec.CommandContext(commandCtx, "go", "tool", "trace", "-d=parsed", file.Name()).CombinedOutput()
	require.NoError(t, err, "%s", output)
	parsed := string(output)
	traceLog := regexp.MustCompile(`Task=(\d+) Category="otel.trace_id" Message="` + span.SpanContext().TraceID().String() + `"`)
	match := traceLog.FindStringSubmatch(parsed)
	require.Len(t, match, 2)
	require.NotEqual(t, "0", match[1])
	require.Regexp(t, `TaskBegin[^\n]* ID=`+match[1]+` Parent=[^\n]* Type="runtime-task"`, parsed)
	require.Regexp(t, `Task=`+match[1]+` Category="otel.span_id" Message="`+span.SpanContext().SpanID().String()+`"`, parsed)
	require.Equal(t, 1, strings.Count(parsed, `Category="otel.trace_id"`))
	require.Equal(t, 1, strings.Count(parsed, `Category="otel.span_id"`))
	require.Len(t, regexp.MustCompile(`TaskBegin[^\n]*Type="runtime-task"`).FindAllString(parsed, -1), 1)
	require.Regexp(t, `TaskEnd[^\n]* ID=`+match[1]+` Parent=`, parsed)
	require.NotContains(t, parsed, "ignored-runtime-task")
	require.NotContains(t, parsed, "private-context-detail")
}

func TestRuntimeSpanDisabledDoesNotAllocate(t *testing.T) {
	ctx := context.Background()
	require.Equal(t, float64(0), testing.AllocsPerRun(100, func() {
		AnnotateRuntimeSpan(ctx)
		require.False(t, TakeRuntimeTraceSample(ctx))
	}))
}

func TestRuntimeTraceSamplesAreExplicitBoundedAndRecorded(t *testing.T) {
	require.False(t, TakeRuntimeTraceSample(context.Background()))
	require.NoError(t, runtimetrace.Start(io.Discard))
	t.Cleanup(runtimetrace.Stop)
	runtimeTraceSamples.Store(64)
	t.Cleanup(func() { runtimeTraceSamples.Store(0) })
	provider := sdktrace.NewTracerProvider(sdktrace.WithSampler(sdktrace.AlwaysSample()))
	t.Cleanup(func() { require.NoError(t, provider.Shutdown(context.Background())) })
	ctx, span := provider.Tracer("test").Start(t.Context(), "sample")
	defer span.End()
	for _, ignored := range []context.Context{
		context.Background(), trace.ContextWithRemoteSpanContext(t.Context(), span.SpanContext()),
	} {
		require.False(t, TakeRuntimeTraceSample(ignored))
	}
	unsampled := sdktrace.NewTracerProvider(sdktrace.WithSampler(sdktrace.NeverSample()))
	t.Cleanup(func() { require.NoError(t, unsampled.Shutdown(context.Background())) })
	ignored, ignoredSpan := unsampled.Tracer("test").Start(t.Context(), "unsampled")
	require.False(t, TakeRuntimeTraceSample(ignored))
	ignoredSpan.End()
	require.EqualValues(t, 64, runtimeTraceSamples.Load())
	var claimed atomic.Int32
	var workers sync.WaitGroup
	for range 128 {
		workers.Go(func() {
			if TakeRuntimeTraceSample(ctx) {
				claimed.Add(1)
			}
		})
	}
	workers.Wait()
	require.EqualValues(t, 64, claimed.Load())
	require.Zero(t, runtimeTraceSamples.Load())
	require.False(t, TakeRuntimeTraceSample(ctx))
	span.End()
	runtimeTraceSamples.Store(1)
	require.False(t, TakeRuntimeTraceSample(ctx), "ended spans must not consume diagnostic samples")
	require.EqualValues(t, 1, runtimeTraceSamples.Load())
	runtimetrace.Stop()
	require.False(t, TakeRuntimeTraceSample(context.Background()))
}

func BenchmarkRuntimeSpanDisabled(b *testing.B) {
	provider := sdktrace.NewTracerProvider(sdktrace.WithSampler(sdktrace.AlwaysSample()))
	ctx, span := provider.Tracer("test").Start(context.Background(), "runtime-task")
	b.Cleanup(func() {
		span.End()
		require.NoError(b, provider.Shutdown(context.Background()))
	})
	b.ReportAllocs()
	for b.Loop() {
		AnnotateRuntimeSpan(ctx)
	}
}

func TestProfilingURIListenAddress(t *testing.T) {
	for uri, want := range map[string]string{
		"": "", "http://127.0.0.1:6060": "127.0.0.1:6060",
		"http://127.0.0.1:6060/": "127.0.0.1:6060", "http://[::1]:6061": "[::1]:6061",
	} {
		got, err := (ProfilingConfig{URI: uri}).ListenAddress()
		require.NoError(t, err)
		require.Equal(t, want, got)
	}
	for _, uri := range []string{
		"http://0.0.0.0:6060", "http://localhost:6060", "http://127.0.0.1:0",
		"http://[::]:6060", "http://127.0.0.1:65536", "127.0.0.1:6060",
		"https://127.0.0.1:6060", "http://127.0.0.1:6060/debug/pprof",
		"http://user:private@127.0.0.1:6060", "http://127.0.0.1:6060?",
		"http://127.0.0.1:6060#", "http://127.0.0.1:6060?private",
		"http://127.0.0.1:6060#private", "http://127.0.0.1:6060/" + strings.Repeat("x", 256),
	} {
		t.Run(uri, func(t *testing.T) {
			require.Error(t, (ProfilingConfig{URI: uri}).Validate())
			shutdown, err := StartCapacityProfiler(ProfilingConfig{URI: uri}, zap.NewNop())
			require.ErrorContains(t, err, "profiling.uri")
			require.NotContains(t, err.Error(), "private")
			require.Nil(t, shutdown)
		})
	}
}

func TestProfilerResolvedConfigurationListenerAndShutdown(t *testing.T) {
	reserved, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	address := reserved.Addr().String()
	t.Setenv("GITSTORE_CAPACITY_PROFILE_ADDR", address)
	shutdown, err := StartCapacityProfiler(ProfilingConfig{}, zap.NewNop())
	require.NoError(t, err, "disabled profiling must not inspect an obsolete environment variable")
	require.NoError(t, shutdown(t.Context()))
	cfg := ProfilingConfig{URI: "http://" + address}
	_, err = StartCapacityProfiler(cfg, zap.NewNop())
	require.ErrorContains(t, err, "listen for capacity profiles")
	require.NoError(t, reserved.Close())
	shutdown, err = StartCapacityProfiler(cfg, zap.NewNop())
	require.NoError(t, err)
	defer func() { require.NoError(t, shutdown(t.Context())) }()
	client := &http.Client{Timeout: 3 * time.Second}
	response, err := client.Get(cfg.URI + "/debug/pprof/heap")
	require.NoError(t, err)
	body, err := io.ReadAll(io.LimitReader(response.Body, 8<<20))
	require.NoError(t, response.Body.Close())
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, response.StatusCode)
	require.Greater(t, len(body), 16)
	require.Equal(t, []byte{0x1f, 0x8b}, body[:2])
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	require.NoError(t, shutdown(ctx))
	rebound, err := net.Listen("tcp", address)
	require.NoError(t, err, "shutdown must release the profiling listener")
	require.NoError(t, rebound.Close())
}

func TestProfilerImmediateShutdownReleasesListener(t *testing.T) {
	for range 10 {
		reserved, err := net.Listen("tcp", "127.0.0.1:0")
		require.NoError(t, err)
		address := reserved.Addr().String()
		require.NoError(t, reserved.Close())
		shutdown, err := StartCapacityProfiler(ProfilingConfig{URI: "http://" + address}, zap.NewNop())
		require.NoError(t, err)
		ctx, cancel := context.WithTimeout(t.Context(), time.Second)
		require.NoError(t, shutdown(ctx))
		cancel()
		rebound, err := net.Listen("tcp", address)
		require.NoError(t, err)
		require.NoError(t, rebound.Close())
	}
}

func TestCapacityProfilesAreBoundedAndBinary(t *testing.T) {
	handler := capacityProfileHandler()
	for _, path := range []string{
		"/debug/pprof/profile", "/debug/pprof/profile?seconds=0",
		"/debug/pprof/profile?seconds=61", "/debug/pprof/trace?seconds=6",
		"/debug/pprof/trace?seconds=-1", "/debug/pprof/heap?debug=1",
		"/debug/pprof/goroutine?debug=2", "/debug/pprof/block?seconds=1",
		"/debug/pprof/trace?seconds=1&sample_limit=65",
		"/debug/pprof/trace?seconds=1&sample_limit=-1",
		"/debug/pprof/trace?seconds=1&sample_limit=",
		"/debug/pprof/trace?seconds=1&sample_limit=x",
		"/debug/pprof/trace?seconds=1&sample_limit=1&sample_limit=2",
	} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
		require.Equal(t, http.StatusBadRequest, response.Code, path)
	}
	for _, name := range []string{"heap", "allocs", "block", "mutex", "goroutine"} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/debug/pprof/"+name, nil))
		require.Equal(t, http.StatusOK, response.Code, name)
		require.Greater(t, response.Body.Len(), 16, name)
		require.Equal(t, []byte{0x1f, 0x8b}, response.Body.Bytes()[:2], name)
	}
	for _, path := range []string{"/debug/pprof/", "/debug/pprof/cmdline", "/debug/pprof/symbol"} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
		require.Equal(t, http.StatusNotFound, response.Code)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/debug/pprof/heap", nil))
	require.Equal(t, http.StatusMethodNotAllowed, response.Code)
}

func TestCapacityCPUProfileRestoresMutexSampling(t *testing.T) {
	previous := runtime.SetMutexProfileFraction(7)
	defer runtime.SetMutexProfileFraction(previous)
	response := httptest.NewRecorder()
	capacityProfileHandler().ServeHTTP(response,
		httptest.NewRequest(http.MethodGet, "/debug/pprof/profile?seconds=1", nil))
	require.Equal(t, http.StatusOK, response.Code)
	profile, err := gzip.NewReader(response.Body)
	require.NoError(t, err)
	defer profile.Close()
	body, err := io.ReadAll(io.LimitReader(profile, 8<<20))
	require.NoError(t, err)
	require.NotEmpty(t, body)
	require.Equal(t, 7, runtime.SetMutexProfileFraction(-1), "sampling settings must be restored after capture")
}

func TestCapacityProfileCancellationAndExclusion(t *testing.T) {
	server := httptest.NewServer(capacityProfileHandler())
	defer server.Close()
	peer := httptest.NewServer(capacityProfileHandler())
	defer peer.Close()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, server.URL+"/debug/pprof/trace?seconds=3&sample_limit=2", nil)
	require.NoError(t, err)
	completed := make(chan error, 1)
	go func() {
		for {
			response, err := server.Client().Do(request)
			if err != nil {
				completed <- err
				return
			}
			_, err = io.Copy(io.Discard, response.Body)
			_ = response.Body.Close()
			if response.StatusCode == http.StatusConflict {
				select {
				case <-ctx.Done():
					completed <- ctx.Err()
					return
				case <-time.After(10 * time.Millisecond):
					continue
				}
			}
			completed <- err
			return
		}
	}()
	require.Eventually(t, func() bool {
		response, err := peer.Client().Get(peer.URL + "/debug/pprof/heap")
		if err != nil {
			return false
		}
		defer response.Body.Close()
		return response.StatusCode == http.StatusConflict
	}, time.Second, 10*time.Millisecond, "exclusion must be process-wide, even across handlers")
	require.Eventually(t, func() bool {
		return runtimeTraceSamples.Load() == 2
	}, time.Second, time.Millisecond, "budget must activate after trace start, not merely after capture lock")
	cancel()
	select {
	case <-completed:
	case <-time.After(time.Second):
		t.Fatal("cancelled execution trace did not return promptly")
	}
	require.Eventually(t, func() bool {
		response, err := peer.Client().Get(peer.URL + "/debug/pprof/heap")
		if err != nil {
			return false
		}
		defer response.Body.Close()
		return response.StatusCode == http.StatusOK
	}, time.Second, 10*time.Millisecond)
	require.Zero(t, runtimeTraceSamples.Load(), "cancellation must revoke unused diagnostic samples")
}

func TestCapacityTraceStartFailureDoesNotActivateSamples(t *testing.T) {
	require.NoError(t, runtimetrace.Start(io.Discard))
	defer runtimetrace.Stop()
	response := httptest.NewRecorder()
	capacityProfileHandler().ServeHTTP(response, httptest.NewRequest(
		http.MethodGet, "/debug/pprof/trace?seconds=1&sample_limit=64", nil,
	))
	require.Equal(t, http.StatusInternalServerError, response.Code)
	require.Contains(t, response.Body.String(), "start execution trace")
	require.Zero(t, runtimeTraceSamples.Load())
	require.False(t, TakeRuntimeTraceSample(t.Context()))
}

func TestCapacityTraceRecordsAcceptedSampleBudget(t *testing.T) {
	for _, samples := range []string{"", "2"} {
		t.Run("samples-"+samples, func(t *testing.T) {
			path := "/debug/pprof/trace?seconds=1"
			if samples != "" {
				path += "&sample_limit=" + samples
			}
			response := httptest.NewRecorder()
			capacityProfileHandler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
			require.Equal(t, http.StatusOK, response.Code)
			require.Greater(t, response.Body.Len(), 16)
			require.Less(t, response.Body.Len(), 8<<20)
			file := t.TempDir() + "/capture.trace"
			require.NoError(t, os.WriteFile(file, response.Body.Bytes(), 0600))
			ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
			defer cancel()
			parsed, err := exec.CommandContext(ctx, "go", "tool", "trace", "-d=parsed", file).CombinedOutput()
			require.NoError(t, err, "%s", parsed)
			if samples == "" {
				samples = "0"
			}
			require.Equal(t, 1, strings.Count(string(parsed), `Category="gitstore.runtime.sample_limit"`))
			require.Contains(t, string(parsed), `Category="gitstore.runtime.sample_limit" Message="`+samples+`"`)
			require.Zero(t, runtimeTraceSamples.Load())
		})
	}
}
