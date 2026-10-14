// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package app

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"

	catalogv1 "github.com/gitstore-dev/gitstore/api/gen/gitstore/catalog/v1"
	"github.com/gitstore-dev/gitstore/api/internal/auth"
	"github.com/gitstore-dev/gitstore/api/internal/config"
	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	"github.com/gitstore-dev/gitstore/api/internal/datastore/memdb"
	apiruntime "github.com/gitstore-dev/gitstore/api/internal/runtime"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	sdklog "go.opentelemetry.io/otel/sdk/log"
	"go.uber.org/zap"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
)

func TestTelemetryDisabledUsesResolvedConfiguration(t *testing.T) {
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "http://127.0.0.1:1")
	t.Setenv("OTEL_TRACES_SAMPLER", "invalid")
	logger := zap.NewNop()
	provider := otel.GetTracerProvider()
	resolved, shutdown, err := InitTelemetry(context.Background(), config.OTLPConfig{}, logger)
	require.NoError(t, err)
	require.Same(t, logger, resolved)
	require.Same(t, provider, otel.GetTracerProvider())
	require.NoError(t, shutdown(context.Background()))
}

func TestTelemetryLogBodiesAreBoundedAndAccounted(t *testing.T) {
	record := sdklog.Record{}
	record.SetBody(attribute.StringValue(strings.Repeat("x", 4095) + "\u20ac" + strings.Repeat("y", 100)))
	before := testutil.ToFloat64(telemetryRecords.WithLabelValues("logs", "truncated"))
	require.NoError(t, (telemetryObserver{signal: "logs"}).OnEmit(context.Background(), &record))
	require.Equal(t, 4095, len(record.Body().AsString()))
	require.True(t, utf8.ValidString(record.Body().AsString()))
	require.Equal(t, before+1, testutil.ToFloat64(telemetryRecords.WithLabelValues("logs", "truncated")))
}
func TestTelemetryExportsUsingResolvedConfiguration(t *testing.T) {
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "http://127.0.0.1:1")
	t.Setenv("OTEL_TRACES_SAMPLER", "always_off")
	t.Setenv("OTEL_TRACES_SAMPLER_ARG", "0")
	var traces, logs atomic.Int64
	collector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, err := io.Copy(io.Discard, r.Body)
		if err != nil {
			http.Error(w, "read export", http.StatusBadRequest)
			return
		}
		switch r.URL.Path {
		case "/otlp/v1/traces":
			traces.Add(1)
		case "/otlp/v1/logs":
			logs.Add(1)
		default:
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/x-protobuf")
		w.WriteHeader(http.StatusOK)
	}))
	defer collector.Close()
	previousTraces := otel.GetTracerProvider()
	previousLogs := otel.GetLoggerProvider()
	previousMetrics := otel.GetMeterProvider()
	previousPropagation := otel.GetTextMapPropagator()
	previousErrors := otel.GetErrorHandler()
	t.Cleanup(func() {
		otel.SetTracerProvider(previousTraces)
		otel.SetLoggerProvider(previousLogs)
		otel.SetMeterProvider(previousMetrics)
		otel.SetTextMapPropagator(previousPropagation)
		otel.SetErrorHandler(previousErrors)
	})
	logger, shutdown, err := InitTelemetry(context.Background(),
		config.OTLPConfig{Endpoint: collector.URL + "/otlp", TraceSampleRatio: 1}, zap.NewNop())
	require.NoError(t, err)
	shutdownDone := false
	t.Cleanup(func() {
		if shutdownDone {
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		assert.NoError(t, shutdown(ctx))
	})
	_, span := otel.Tracer("config-test").Start(context.Background(), "resolved-config")
	require.True(t, span.IsRecording(), "GitStore sampling configuration must override SDK environment defaults")
	span.End()
	logger.Info("resolved configuration log")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	require.NoError(t, shutdown(ctx))
	shutdownDone = true
	assert.Positive(t, traces.Load())
	assert.Positive(t, logs.Load())
}

func TestProfilerUsesResolvedConfiguration(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer listener.Close()
	t.Setenv("GITSTORE_CAPACITY_PROFILE_ADDR", listener.Addr().String())
	shutdown, err := StartCapacityProfiler(config.ProfilingConfig{}, zap.NewNop())
	require.NoError(t, err, "the removed environment path must not open a listener")
	require.NoError(t, shutdown(context.Background()))
	_, err = StartCapacityProfiler(config.ProfilingConfig{ListenAddr: listener.Addr().String()}, zap.NewNop())
	require.ErrorContains(t, err, "listen for capacity profiles")
}

func TestCapacityProfilesAreBoundedAndBinary(t *testing.T) {
	handler := capacityProfileHandler()
	for _, path := range []string{
		"/debug/pprof/profile", "/debug/pprof/profile?seconds=0",
		"/debug/pprof/profile?seconds=61", "/debug/pprof/trace?seconds=6",
		"/debug/pprof/trace?seconds=-1", "/debug/pprof/heap?debug=1",
		"/debug/pprof/goroutine?debug=2", "/debug/pprof/block?seconds=1",
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
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/debug/pprof/heap", nil))
	require.Equal(t, http.StatusMethodNotAllowed, response.Code)
}

func TestCapacityProfileCancellationAndExclusion(t *testing.T) {
	server := httptest.NewServer(capacityProfileHandler())
	defer server.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, server.URL+"/debug/pprof/trace?seconds=3", nil)
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
		response, err := server.Client().Get(server.URL + "/debug/pprof/heap")
		if err != nil {
			return false
		}
		defer response.Body.Close()
		return response.StatusCode == http.StatusConflict
	}, time.Second, 10*time.Millisecond)
	cancel()
	select {
	case <-completed:
	case <-time.After(time.Second):
		t.Fatal("cancelled execution trace did not return promptly")
	}
	require.Eventually(t, func() bool {
		response, err := server.Client().Get(server.URL + "/debug/pprof/heap")
		if err != nil {
			return false
		}
		defer response.Body.Close()
		return response.StatusCode == http.StatusOK
	}, time.Second, 10*time.Millisecond)
}

type catalogMetricsStub struct {
	catalogv1.UnimplementedCatalogServiceServer
	code codes.Code
}

func (s *catalogMetricsStub) AdmitResources(context.Context, *catalogv1.AdmitResourcesRequest) (*catalogv1.AdmitResourcesResponse, error) {
	if s.code != codes.OK {
		return nil, status.Error(s.code, "fixture denial")
	}
	return &catalogv1.AdmitResourcesResponse{}, nil
}

func TestCatalogGRPCMetricsMeasureSuccessAndDenialWithoutResourceLabels(t *testing.T) {
	t.Parallel()
	for _, code := range []codes.Code{codes.OK, codes.PermissionDenied} {
		t.Run(code.String(), func(t *testing.T) {
			registry := prometheus.NewRegistry()
			server, err := newCatalogGRPCServer(&catalogMetricsStub{code: code}, registry)
			require.NoError(t, err)
			listener := bufconn.Listen(1 << 20)
			done := make(chan error, 1)
			go func() { done <- server.Serve(listener) }()
			t.Cleanup(func() {
				server.Stop()
				require.NoError(t, <-done)
				require.NoError(t, listener.Close())
			})
			conn, err := grpc.NewClient("passthrough:///catalog",
				grpc.WithTransportCredentials(insecure.NewCredentials()),
				grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) { return listener.DialContext(ctx) }))
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, conn.Close()) })
			ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
			defer cancel()
			_, err = catalogv1.NewCatalogServiceClient(conn).AdmitResources(ctx, &catalogv1.AdmitResourcesRequest{RepositoryId: "private-fixture-resource"})
			require.Equal(t, code, status.Code(err))
			families, err := registry.Gather()
			require.NoError(t, err)
			sampled := false
			for _, family := range families {
				for _, metric := range family.Metric {
					method := ""
					for _, label := range metric.Label {
						require.Contains(t, []string{"grpc_type", "grpc_service", "grpc_method", "grpc_code", "le"}, label.GetName())
						require.NotContains(t, label.GetValue(), "private-fixture-resource")
						if label.GetName() == "grpc_method" {
							method = label.GetValue()
						}
					}
					if family.GetName() == "gitstore_catalog_grpc_server_handling_seconds" && method == "AdmitResources" {
						require.EqualValues(t, 1, metric.Histogram.GetSampleCount())
						require.Positive(t, metric.Histogram.GetSampleSum())
						sampled = true
					}
				}
			}
			require.True(t, sampled, "the actual registered RPC server must measure both success and denial")
			_, err = newCatalogGRPCServer(&catalogMetricsStub{}, registry)
			require.ErrorContains(t, err, "register catalog gRPC metrics")
		})
	}
}

type bootstrapGitProvisioner struct {
	repositoryIDs []string
	actors        []*auth.Principal
}

func (p *bootstrapGitProvisioner) CreateRepository(ctx context.Context, repositoryID, _ string) (string, error) {
	p.repositoryIDs = append(p.repositoryIDs, repositoryID)
	p.actors = append(p.actors, auth.PrincipalFromContext(ctx))
	return "/repos/" + repositoryID, nil
}

func TestEnsureBootstrapResourcesCreatesExactlyTwoNamespacesAndRepositoriesIdempotently(t *testing.T) {
	store, err := memdb.New()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.Close()) })
	git := &bootstrapGitProvisioner{}
	ids := apiruntime.NewSequenceIDGenerator(
		"00000000-0000-7000-8000-000000000001",
		"00000000-0000-7000-8000-000000000002",
		"00000000-0000-7000-8000-000000000003",
		"00000000-0000-7000-8000-000000000004",
	)
	clock := apiruntime.NewFixedClock(time.Date(2026, 3, 26, 12, 0, 0, 0, time.UTC))

	require.NoError(t, ensureBootstrapResources(context.Background(), store, git, clock, ids, zap.NewNop()))
	require.NoError(t, ensureBootstrapResources(context.Background(), store, git, clock, ids, zap.NewNop()))

	namespaces, err := store.ListNamespaces(context.Background(), datastore.PageParams{})
	require.NoError(t, err)
	require.Len(t, namespaces.Items, 2)
	assert.Len(t, git.repositoryIDs, 2)
	for _, actor := range git.actors {
		require.NotNil(t, actor, "bootstrap must explicitly identify its internal backing actor")
		require.Equal(t, "system:api", actor.Subject)
		require.Equal(t, "internal", actor.AuthMethod)
	}

	for _, identifier := range []string{"gitstore-system", "default"} {
		namespace, getErr := store.GetNamespaceByName(context.Background(), identifier)
		require.NoError(t, getErr)
		assert.Equal(t, bootstrapActor, namespace.CreationActor)
		assert.Equal(t, datastore.NamespaceTierOrganization, namespace.Tier)
		assert.Equal(t, datastore.NamespaceInitialResourceVersion, namespace.ResourceVersion)

		mapping, lookupErr := store.LookupRepository(context.Background(), namespace.Name, bootstrapRepositoryName)
		require.NoError(t, lookupErr)
		repository, repoErr := store.GetRepository(context.Background(), mapping.RepositoryID)
		require.NoError(t, repoErr)
		assert.Equal(t, bootstrapRepositoryName, repository.Name)
		assert.Equal(t, namespace.Name, repository.Namespace)
	}
}
