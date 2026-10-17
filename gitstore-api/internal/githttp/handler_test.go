// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package githttp

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	gitv1 "github.com/gitstore-dev/gitstore/api/gen/gitstore/git/v1"
	"github.com/gitstore-dev/gitstore/api/internal/auth"
	"github.com/gitstore-dev/gitstore/api/internal/auth/provider/anonymous"
	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	"github.com/gitstore-dev/gitstore/api/internal/gitclient"
	"github.com/gitstore-dev/gitstore/api/internal/middleware/security"
	apiruntime "github.com/gitstore-dev/gitstore/api/internal/runtime"
	"github.com/gitstore-dev/gitstore/api/internal/testutil"
	"github.com/gitstore-dev/gitstore/observability"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/trace"
	logpb "go.opentelemetry.io/proto/otlp/collector/logs/v1"
	tracepb "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
	"google.golang.org/protobuf/proto"
)

func TestGitHTTPTimingLogAndNativeExemplarsShareServerSpan(t *testing.T) {
	previousTraces, previousLogs, previousMetrics := otel.GetTracerProvider(), otel.GetLoggerProvider(), otel.GetMeterProvider()
	previousPropagation, previousErrors := otel.GetTextMapPropagator(), otel.GetErrorHandler()
	previousStageMetrics := defaultGitHTTPStageMetrics
	t.Cleanup(func() {
		otel.SetTracerProvider(previousTraces)
		otel.SetLoggerProvider(previousLogs)
		otel.SetMeterProvider(previousMetrics)
		otel.SetTextMapPropagator(previousPropagation)
		otel.SetErrorHandler(previousErrors)
		defaultGitHTTPStageMetrics = previousStageMetrics
	})
	for _, tc := range []struct {
		name             string
		tracing, sampled bool
	}{
		{name: "sampled", tracing: true, sampled: true},
		{name: "unsampled", tracing: true},
		{name: "disabled", sampled: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var mu sync.Mutex
			var traces tracepb.ExportTraceServiceRequest
			var logs logpb.ExportLogsServiceRequest
			collector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
				require.NoError(t, err)
				mu.Lock()
				defer mu.Unlock()
				switch r.URL.Path {
				case "/v1/traces":
					var batch tracepb.ExportTraceServiceRequest
					require.NoError(t, proto.Unmarshal(body, &batch))
					traces.ResourceSpans = append(traces.ResourceSpans, batch.ResourceSpans...)
				case "/v1/logs":
					var batch logpb.ExportLogsServiceRequest
					require.NoError(t, proto.Unmarshal(body, &batch))
					logs.ResourceLogs = append(logs.ResourceLogs, batch.ResourceLogs...)
				default:
					http.NotFound(w, r)
					return
				}
				w.Header().Set("Content-Type", "application/x-protobuf")
			}))
			defer collector.Close()
			core, local := observer.New(zap.InfoLevel)
			logger, shutdown, err := observability.InitTelemetry(t.Context(),
				observability.OTLPConfig{URI: collector.URL, TraceSampleRatio: 0}, observability.APIService, "test", zap.New(core))
			require.NoError(t, err)
			t.Cleanup(func() { _ = shutdown(context.Background()) })
			registry := prometheus.NewRegistry()
			defaultGitHTTPStageMetrics = newGitHTTPStageMetrics(registry)
			mux := NewMux(SmartHttpDeps{
				GitClient: &mockGitClient{infoRefsFunc: func(_ context.Context, _ string, service gitv1.Service) ([]byte, gitv1.Service, error) {
					return []byte("0000"), service, nil
				}},
				Logger: logger, Store: newTestStore(), Ids: apiruntime.NewSequenceIDGenerator(),
				Registry: newTestRegistry(t), Tracing: tc.tracing,
			})
			request := httptest.NewRequest(http.MethodGet, "/gitstore/catalog/info/refs?service=git-upload-pack", nil)
			flags := trace.TraceFlags(0)
			if tc.sampled {
				flags = trace.FlagsSampled
			}
			remote := trace.NewSpanContext(trace.SpanContextConfig{
				TraceID: trace.TraceID{1}, SpanID: trace.SpanID{2}, TraceFlags: flags, Remote: true,
			})
			observability.Inject(trace.ContextWithRemoteSpanContext(t.Context(), remote), request.Header)
			response := httptest.NewRecorder()
			mux.ServeHTTP(response, request)
			require.Equal(t, http.StatusOK, response.Code)
			require.NoError(t, shutdown(t.Context()))
			require.Equal(t, 1, local.FilterMessage("git_http_request_timing").Len(), "existing Info timing logs remain enabled")
			fields := local.FilterMessage("git_http_request_timing").All()[0].ContextMap()
			mu.Lock()
			defer mu.Unlock()
			foundLog := false
			for _, resource := range logs.ResourceLogs {
				for _, scope := range resource.ScopeLogs {
					for _, record := range scope.LogRecords {
						if record.Body.GetStringValue() != "git_http_request_timing" {
							continue
						}
						foundLog = true
						if tc.tracing {
							require.Equal(t, fields["trace_id"], hex.EncodeToString(record.TraceId))
							require.Equal(t, fields["span_id"], hex.EncodeToString(record.SpanId))
						} else {
							require.Empty(t, record.TraceId)
							require.Empty(t, record.SpanId)
						}
						allowed := map[string]bool{"operation": true, "status": true, "authentication_ms": true,
							"resolution_ms": true, "authorization_ms": true, "push_context_ms": true, "handler_ms": true, "total_ms": true}
						for _, attribute := range record.Attributes {
							require.True(t, allowed[attribute.Key], "only fixed operational fields, never trace IDs as ordinary attributes")
						}
						require.Len(t, record.Attributes, len(allowed))
					}
				}
			}
			require.True(t, foundLog)
			serverFound := false
			for _, resource := range traces.ResourceSpans {
				for _, scope := range resource.ScopeSpans {
					for _, span := range scope.Spans {
						if span.Name == "git.http.info_refs" {
							serverFound = true
							require.Equal(t, fields["span_id"], hex.EncodeToString(span.SpanId))
							parentID := remote.SpanID()
							require.Equal(t, parentID[:], span.ParentSpanId)
						}
					}
				}
			}
			require.Equal(t, tc.tracing && tc.sampled, serverFound)
			scrape := httptest.NewRequest(http.MethodGet, "/metrics", nil)
			scrape.Header.Set("Accept", "application/openmetrics-text; version=1.0.0")
			metrics := httptest.NewRecorder()
			promhttp.HandlerFor(registry, promhttp.HandlerOpts{EnableOpenMetrics: true}).ServeHTTP(metrics, scrape)
			require.Equal(t, tc.tracing && tc.sampled, strings.Contains(metrics.Body.String(), `trace_id="`))
			if tc.tracing && tc.sampled {
				require.Contains(t, metrics.Body.String(), `span_id="`+fields["span_id"].(string)+`"`)
				require.Contains(t, metrics.Body.String(), `trace_id="`+fields["trace_id"].(string)+`"`)
			}
			families, err := registry.Gather()
			require.NoError(t, err)
			for _, family := range families {
				for _, series := range family.Metric {
					require.Len(t, series.Label, 3)
					require.EqualValues(t, 1, series.Histogram.GetSampleCount())
				}
			}
		})
	}
}

// mockGitClient is a test double for GitClient.
type mockGitClient struct {
	infoRefsFunc    func(ctx context.Context, repoID string, service gitv1.Service) ([]byte, gitv1.Service, error)
	uploadPackFunc  func(ctx context.Context, repoID string, body []byte) (io.Reader, error)
	receivePackFunc func(ctx context.Context, repoID string, body io.Reader) ([]byte, error)
}

func TestMain(m *testing.M) {
	gin.SetMode(gin.TestMode)
	os.Exit(m.Run())
}

func newTestRegistry(t *testing.T) *auth.ProviderRegistry {
	t.Helper()

	registry := auth.NewProviderRegistry(
		auth.NewChainedAuthN(&stubAuthNProviderWithPrincipal{
			principal: &auth.Principal{Subject: "test-writer", AuthMethod: "basic"},
			decision:  auth.Allow("test-authn", "authenticated fixture"),
		}),
		testutil.NewAllowAllAuthZ(),
		nil,
	)
	return registry
}

func newTestStore() datastore.Datastore {
	return &testutil.StubStore{
		GetNamespaceByNameFunc: func(_ context.Context, name string) (*datastore.Namespace, error) {
			if name != "gitstore" {
				return nil, datastore.ErrNotFound
			}
			return &datastore.Namespace{UID: "test-namespace-id", Name: name}, nil
		},
		LookupRepositoryFunc: func(_ context.Context, namespace, name string) (*datastore.NamespaceMapping, error) {
			if namespace != "gitstore" || name != "catalog" {
				return nil, datastore.ErrNotFound
			}
			return &datastore.NamespaceMapping{RepositoryID: "test-repo-id"}, nil
		},
		GetRepositoryFunc: func(_ context.Context, id string) (*datastore.Repository, error) {
			if id != "test-repo-id" {
				return nil, datastore.ErrNotFound
			}
			return &datastore.Repository{UID: id, Name: "catalog", Namespace: "gitstore"}, nil
		},
	}
}

func TestNewMuxRequiresDatastore(t *testing.T) {
	require.PanicsWithValue(t, "githttp: datastore is required", func() {
		NewMux(SmartHttpDeps{GitClient: &mockGitClient{}, Logger: zap.NewNop(),
			Ids: apiruntime.NewSequenceIDGenerator(), Registry: newTestRegistry(t)})
	})
}

func TestGitHTTPPhaseTimingAccounting(t *testing.T) {
	start := time.Now()
	timing := gitHTTPTiming{last: start}
	timing.advance(gitHTTPResolution, start.Add(time.Millisecond))
	timing.advance(gitHTTPAuthorization, start.Add(3*time.Millisecond))
	timing.advance(gitHTTPPushContext, start.Add(6*time.Millisecond))
	timing.advance(gitHTTPHandler, start.Add(10*time.Millisecond))
	timing.advance(gitHTTPHandler, start.Add(15*time.Millisecond))
	require.Equal(t, [gitHTTPPhaseCount]time.Duration{
		time.Millisecond, 2 * time.Millisecond, 3 * time.Millisecond,
		4 * time.Millisecond, 5 * time.Millisecond,
	}, timing.durations)
}

func TestGitHTTPStageMetricsOnlyRecordAttemptedWork(t *testing.T) {
	for _, tc := range []struct {
		name, outcome string
		status        int
		phases        []gitHTTPPhase
	}{
		{"advertisement", "success", 200, []gitHTTPPhase{gitHTTPAuthentication, gitHTTPResolution, gitHTTPAuthorization, gitHTTPHandler}},
		{"receive", "success", 200, []gitHTTPPhase{gitHTTPAuthentication, gitHTTPResolution, gitHTTPAuthorization, gitHTTPPushContext, gitHTTPHandler}},
		{"authentication denied", "client_error", 401, []gitHTTPPhase{gitHTTPAuthentication}},
		{"resolution failed", "client_error", 404, []gitHTTPPhase{gitHTTPAuthentication, gitHTTPResolution}},
		{"authorization failed", "server_error", 500, []gitHTTPPhase{gitHTTPAuthentication, gitHTTPResolution, gitHTTPAuthorization}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reg := prometheus.NewRegistry()
			metrics := newGitHTTPStageMetrics(reg)
			timing := gitHTTPTiming{}
			var total time.Duration
			for _, phase := range tc.phases {
				timing.attempted[phase] = true
				timing.durations[phase] = time.Duration(phase+1) * time.Millisecond
				total += timing.durations[phase]
			}
			timing.observe(metrics, "info_refs", tc.status, total)
			families, err := reg.Gather()
			require.NoError(t, err)
			require.Len(t, families, 1)
			require.Equal(t, "gitstore_git_http_stage_duration_seconds", families[0].GetName())
			require.Len(t, families[0].Metric, len(tc.phases)+1)
			var phaseSum float64
			for _, metric := range families[0].Metric {
				labels := map[string]string{}
				for _, label := range metric.Label {
					labels[label.GetName()] = label.GetValue()
				}
				require.Len(t, labels, 3)
				require.Equal(t, "info_refs", labels["operation"])
				require.Equal(t, tc.outcome, labels["outcome"])
				require.EqualValues(t, 1, metric.Histogram.GetSampleCount())
				if labels["phase"] == "total" {
					require.Equal(t, total.Seconds(), metric.Histogram.GetSampleSum())
				} else {
					phaseSum += metric.Histogram.GetSampleSum()
				}
			}
			require.InDelta(t, total.Seconds(), phaseSum, 1e-12)
		})
	}
}

func TestRepoResolverLeavesNamespaceValidationToAuthorization(t *testing.T) {
	var namespaceReads int
	store := &testutil.StubStore{
		GetNamespaceByNameFunc: func(context.Context, string) (*datastore.Namespace, error) {
			namespaceReads++
			return nil, errors.New("namespace facts belong to authorization")
		},
		LookupRepositoryFunc: func(_ context.Context, namespace, name string) (*datastore.NamespaceMapping, error) {
			require.Equal(t, "gitstore", namespace)
			require.Equal(t, "catalog", name)
			return &datastore.NamespaceMapping{RepositoryID: "repository-uid"}, nil
		},
	}
	router := gin.New()
	router.GET("/:namespace/:repo", RepoResolver(store, zap.NewNop()), func(c *gin.Context) {
		require.Equal(t, "repository-uid", c.MustGet(repoIDKey))
		c.Status(http.StatusOK)
	})
	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/gitstore/catalog.git", nil))
	require.Equal(t, http.StatusOK, response.Code)
	require.Zero(t, namespaceReads, "resolving a path must not duplicate the authorizer's current Namespace read")
}

func TestGitHTTPRequestNamespaceAndRepositoryFactsStayFresh(t *testing.T) {
	for _, route := range []struct {
		method, path, action string
	}{
		{http.MethodGet, "/gitstore/catalog.git/info/refs?service=git-upload-pack", auth.ActionRepositoryContentsRead},
		{http.MethodGet, "/gitstore/catalog.git/info/refs?service=git-receive-pack", auth.ActionRepositoryContentsWrite},
		{http.MethodPost, "/gitstore/catalog.git/git-upload-pack", auth.ActionRepositoryContentsRead},
		{http.MethodPost, "/gitstore/catalog.git/git-receive-pack", auth.ActionRepositoryContentsWrite},
	} {
		for _, state := range []struct {
			name                         string
			status, namespaceReads       int
			repositoryReads, policyCalls int
		}{
			{"current owner", http.StatusOK, 1, 1, 1},
			{"denied", http.StatusForbidden, 1, 1, 1},
			{"namespace missing", http.StatusNotFound, 1, 1, 0},
			{"namespace wrapped missing", http.StatusNotFound, 1, 1, 0},
			{"namespace nil", http.StatusNotFound, 1, 1, 0},
			{"namespace storage failure", http.StatusInternalServerError, 1, 1, 0},
			{"namespace canceled", http.StatusInternalServerError, 1, 1, 0},
			{"repository storage failure", http.StatusInternalServerError, 1, 1, 0},
			{"repository error and namespace missing", http.StatusInternalServerError, 1, 1, 0},
			{"repository moved", http.StatusNotFound, 1, 1, 0},
			{"repository moved and namespace failure", http.StatusNotFound, 1, 1, 0},
			{"repository renamed", http.StatusNotFound, 1, 1, 0},
		} {
			t.Run(route.path+"/"+state.name, func(t *testing.T) {
				store := newTestStore().(*testutil.StubStore)
				repository := &datastore.Repository{
					UID: "test-repo-id", Name: "catalog", Namespace: "gitstore", CreationActor: "old-owner",
				}
				resolve := store.LookupRepositoryFunc
				var mappings, namespaceReads, repositoryReads, dispatches int
				store.LookupRepositoryFunc = func(ctx context.Context, namespace, name string) (*datastore.NamespaceMapping, error) {
					mappings++
					mapping, err := resolve(ctx, namespace, name)
					repository.CreationActor = "current-owner"
					switch state.name {
					case "repository moved", "repository moved and namespace failure":
						repository.Namespace = "private-destination"
					case "repository renamed":
						repository.Name = "private-new-name"
					}
					return mapping, err
				}
				store.GetRepositoryFunc = func(_ context.Context, id string) (*datastore.Repository, error) {
					repositoryReads++
					require.Equal(t, repository.UID, id)
					if state.name == "repository storage failure" || state.name == "repository error and namespace missing" {
						return nil, errors.New("private-repository-failure")
					}
					return repository, nil
				}
				store.GetNamespaceByNameFunc = func(_ context.Context, name string) (*datastore.Namespace, error) {
					namespaceReads++
					require.Equal(t, "gitstore", name)
					switch state.name {
					case "namespace missing", "repository error and namespace missing":
						return nil, datastore.ErrNotFound
					case "namespace wrapped missing":
						return nil, fmt.Errorf("lookup: %w", datastore.ErrNotFound)
					case "namespace nil":
						return nil, nil
					case "namespace storage failure", "repository moved and namespace failure":
						return nil, errors.New("private-storage-error")
					case "namespace canceled":
						return nil, context.Canceled
					}
					return &datastore.Namespace{UID: "current-namespace", Name: name}, nil
				}
				provider := testutil.NewAllowAllAuthZ()
				if state.name == "denied" {
					provider = testutil.NewDenyAllAuthZ(t)
				}
				registry := auth.NewProviderRegistry(
					auth.NewChainedAuthN(&stubAuthNProviderWithPrincipal{
						principal: &auth.Principal{Subject: "current-owner", AuthMethod: "basic"},
						decision:  auth.Allow("test-authn", "authenticated"),
					}), provider, nil,
				)
				client := &mockGitClient{
					infoRefsFunc: func(_ context.Context, _ string, service gitv1.Service) ([]byte, gitv1.Service, error) {
						dispatches++
						return []byte("0000"), service, nil
					},
					uploadPackFunc: func(context.Context, string, []byte) (io.Reader, error) {
						dispatches++
						return strings.NewReader("0000"), nil
					},
					receivePackFunc: func(context.Context, string, io.Reader) ([]byte, error) {
						dispatches++
						return []byte("0000"), nil
					},
				}
				router := NewMux(SmartHttpDeps{
					GitClient: client, Store: store, Logger: zap.NewNop(),
					Ids: apiruntime.NewSequenceIDGenerator(), Registry: registry,
				})
				request := httptest.NewRequest(route.method, route.path, strings.NewReader("0000"))
				request.SetBasicAuth("current-owner", "private-password")
				response := httptest.NewRecorder()
				router.ServeHTTP(response, request)
				require.Equal(t, state.status, response.Code)
				require.Equal(t, 1, mappings)
				require.Equal(t, state.namespaceReads, namespaceReads)
				wantRepositoryReads := state.repositoryReads
				if state.status == http.StatusOK && strings.HasSuffix(route.path, "/git-receive-pack") {
					wantRepositoryReads++
				}
				require.Equal(t, wantRepositoryReads, repositoryReads)
				require.Len(t, provider.Calls, state.policyCalls)
				if state.policyCalls != 0 {
					require.Equal(t, route.action, provider.Action)
					require.Equal(t, "current-owner", provider.Resource.OwnerSub)
					require.Equal(t, "gitstore", provider.Resource.Attrs["namespace"])
				}
				if state.status == http.StatusOK {
					require.Equal(t, 1, dispatches)
				} else {
					require.Zero(t, dispatches, "missing or changed facts must fail before native dispatch")
				}
				require.NotContains(t, response.Body.String(), "private-")
			})
		}
	}
}

func TestGitHTTPRequestAuthorizationOverlapsFreshReads(t *testing.T) {
	for _, route := range []struct{ method, path string }{
		{http.MethodGet, "/gitstore/catalog.git/info/refs?service=git-upload-pack"},
		{http.MethodGet, "/gitstore/catalog.git/info/refs?service=git-receive-pack"},
		{http.MethodPost, "/gitstore/catalog.git/git-upload-pack"},
		{http.MethodPost, "/gitstore/catalog.git/git-receive-pack"},
	} {
		t.Run(route.path, func(t *testing.T) {
			store := newTestStore().(*testutil.StubStore)
			repositoryRead, namespaceRead := store.GetRepositoryFunc, store.GetNamespaceByNameFunc
			repositoryStarted, namespaceStarted := make(chan struct{}), make(chan struct{})
			var repositoryOnce sync.Once
			store.GetRepositoryFunc = func(ctx context.Context, id string) (*datastore.Repository, error) {
				repositoryOnce.Do(func() { close(repositoryStarted) })
				select {
				case <-namespaceStarted:
					return repositoryRead(ctx, id)
				case <-ctx.Done():
					return nil, ctx.Err()
				}
			}
			store.GetNamespaceByNameFunc = func(ctx context.Context, name string) (*datastore.Namespace, error) {
				close(namespaceStarted)
				select {
				case <-repositoryStarted:
					return namespaceRead(ctx, name)
				case <-ctx.Done():
					return nil, ctx.Err()
				}
			}
			router := NewMux(SmartHttpDeps{
				GitClient: &mockGitClient{
					infoRefsFunc: func(_ context.Context, _ string, service gitv1.Service) ([]byte, gitv1.Service, error) {
						return []byte("0000"), service, nil
					},
					uploadPackFunc: func(context.Context, string, []byte) (io.Reader, error) {
						return strings.NewReader("0000"), nil
					},
					receivePackFunc: func(context.Context, string, io.Reader) ([]byte, error) {
						return []byte("0000"), nil
					},
				}, Store: store, Logger: zap.NewNop(),
				Ids: apiruntime.NewSequenceIDGenerator(), Registry: newTestRegistry(t),
			})
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			request := httptest.NewRequest(route.method, route.path, strings.NewReader("0000")).WithContext(ctx)
			response := httptest.NewRecorder()
			router.ServeHTTP(response, request)
			require.Equal(t, http.StatusOK, response.Code, "both authorization reads must start before either needs to finish")
		})
	}
}

func TestGitHTTPRequestAuthorizationJoinsNamespaceReadOnRepositoryFailure(t *testing.T) {
	store := newTestStore().(*testutil.StubStore)
	namespaceStarted, namespaceFinished := make(chan struct{}), make(chan struct{})
	repositoryFinished, requestFinished := make(chan struct{}), make(chan struct{})
	store.GetRepositoryFunc = func(ctx context.Context, _ string) (*datastore.Repository, error) {
		select {
		case <-namespaceStarted:
		case <-ctx.Done():
		}
		close(repositoryFinished)
		return nil, errors.New("private-repository-failure")
	}
	store.GetNamespaceByNameFunc = func(ctx context.Context, _ string) (*datastore.Namespace, error) {
		close(namespaceStarted)
		defer close(namespaceFinished)
		<-ctx.Done()
		return nil, ctx.Err()
	}
	router := NewMux(SmartHttpDeps{
		GitClient: &mockGitClient{}, Store: store, Logger: zap.NewNop(),
		Ids: apiruntime.NewSequenceIDGenerator(), Registry: newTestRegistry(t),
	})
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	request := httptest.NewRequest(http.MethodGet, "/gitstore/catalog.git/info/refs?service=git-upload-pack", nil).WithContext(ctx)
	response := httptest.NewRecorder()
	go func() {
		defer close(requestFinished)
		router.ServeHTTP(response, request)
	}()
	select {
	case <-repositoryFinished:
	case <-ctx.Done():
		t.Fatal("repository read did not finish while the Namespace read was active")
	}
	select {
	case <-requestFinished:
		t.Fatal("authorization returned with a Namespace read still running")
	case <-time.After(20 * time.Millisecond):
	}
	cancel()
	select {
	case <-requestFinished:
	case <-time.After(2 * time.Second):
		t.Fatal("authorization did not finish after cancellation")
	}
	select {
	case <-namespaceFinished:
	default:
		t.Fatal("authorization did not join its Namespace read")
	}
	require.Equal(t, http.StatusInternalServerError, response.Code)
	require.NotContains(t, response.Body.String(), "private-")
}

func TestGitHTTPRequestTimingBoundaries(t *testing.T) {
	const delay = 5 * time.Millisecond
	for _, tc := range []struct {
		name, operation, delayedPhase             string
		method, path                              string
		status                                    int
		namespaceReads, repositoryReads, mappings int
	}{
		{"authentication", "info_refs", "authentication_ms", http.MethodGet, "/gitstore/catalog/info/refs?service=git-receive-pack", 200, 1, 1, 1},
		{"resolution", "info_refs", "resolution_ms", http.MethodGet, "/gitstore/catalog/info/refs?service=git-receive-pack", 200, 1, 1, 1},
		{"authorization", "info_refs", "authorization_ms", http.MethodGet, "/gitstore/catalog/info/refs?service=git-receive-pack", 200, 1, 1, 1},
		{"push context", "receive_pack", "push_context_ms", http.MethodPost, "/gitstore/catalog/git-receive-pack", 200, 1, 2, 1},
		{"receive", "receive_pack", "handler_ms", http.MethodPost, "/gitstore/catalog/git-receive-pack", 200, 1, 2, 1},
		{"upload", "upload_pack", "handler_ms", http.MethodPost, "/gitstore/catalog/git-upload-pack", 200, 1, 1, 1},
		{"handler failure", "info_refs", "handler_ms", http.MethodGet, "/gitstore/catalog/info/refs?service=git-receive-pack", 503, 1, 1, 1},
		{"resolution failure", "info_refs", "resolution_ms", http.MethodGet, "/gitstore/catalog/info/refs?service=git-receive-pack", 404, 0, 0, 1},
		{"authentication denied", "info_refs", "authentication_ms", http.MethodGet, "/gitstore/catalog/info/refs?service=git-receive-pack", 401, 0, 0, 0},
		{"authorization denied", "info_refs", "authorization_ms", http.MethodGet, "/gitstore/catalog/info/refs?service=git-receive-pack", 403, 1, 1, 1},
		{"push context failure", "receive_pack", "push_context_ms", http.MethodPost, "/gitstore/catalog/git-receive-pack", 503, 1, 2, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			core, logs := observer.New(zap.InfoLevel)
			var namespaceReads, repositoryReads, mappings int
			store := newTestStore().(*testutil.StubStore)
			namespaceRead := store.GetNamespaceByNameFunc
			store.GetNamespaceByNameFunc = func(ctx context.Context, name string) (*datastore.Namespace, error) {
				namespaceReads++
				return namespaceRead(ctx, name)
			}
			repositoryRead := store.GetRepositoryFunc
			store.GetRepositoryFunc = func(ctx context.Context, id string) (*datastore.Repository, error) {
				repositoryReads++
				if (repositoryReads == 1 && tc.delayedPhase == "authorization_ms") ||
					(repositoryReads == 2 && tc.delayedPhase == "push_context_ms") {
					time.Sleep(delay)
				}
				if tc.name == "push context failure" && repositoryReads == 2 {
					return nil, errors.New("private-storage-error")
				}
				return repositoryRead(ctx, id)
			}
			mappingRead := store.LookupRepositoryFunc
			store.LookupRepositoryFunc = func(ctx context.Context, namespace, name string) (*datastore.NamespaceMapping, error) {
				mappings++
				if tc.delayedPhase == "resolution_ms" {
					time.Sleep(delay)
				}
				if tc.name == "resolution failure" {
					return nil, datastore.ErrNotFound
				}
				return mappingRead(ctx, namespace, name)
			}
			authN := &stubAuthNProviderWithPrincipal{
				principal: &auth.Principal{Subject: "test-writer", AuthMethod: "basic"},
				decision:  auth.Allow("test-authn", "authenticated fixture"),
			}
			if tc.delayedPhase == "authentication_ms" {
				authN.delay = delay
			}
			if tc.name == "authentication denied" {
				authN.decision = auth.Deny("test-authn", "denied fixture")
			}
			var authZ auth.AuthZProvider = testutil.NewAllowAllAuthZ()
			if tc.name == "authorization denied" {
				authZ = testutil.NewDenyAllAuthZ(t)
			}
			client := &mockGitClient{
				infoRefsFunc: func(_ context.Context, _ string, svc gitv1.Service) ([]byte, gitv1.Service, error) {
					if tc.delayedPhase == "handler_ms" {
						time.Sleep(delay)
					}
					if tc.name == "handler failure" {
						return nil, svc, errors.New("private-backend-error")
					}
					return []byte("0000"), svc, nil
				},
				receivePackFunc: func(_ context.Context, _ string, _ io.Reader) ([]byte, error) {
					if tc.delayedPhase == "handler_ms" {
						time.Sleep(delay)
					}
					return []byte("0000"), nil
				},
				uploadPackFunc: func(_ context.Context, _ string, _ []byte) (io.Reader, error) {
					time.Sleep(delay)
					return strings.NewReader("private-response"), nil
				},
			}
			router := NewMux(SmartHttpDeps{
				GitClient: client, Store: store, Logger: zap.New(core),
				Ids:      apiruntime.NewSequenceIDGenerator(),
				Registry: auth.NewProviderRegistry(auth.NewChainedAuthN(authN), authZ, nil),
			})
			separator := "?"
			if strings.Contains(tc.path, "?") {
				separator = "&"
			}
			req := httptest.NewRequest(tc.method, tc.path+separator+"private=secret-marker", strings.NewReader("private-body"))
			req.SetBasicAuth("test-writer", "private-password")
			w := httptest.NewRecorder()
			router.ServeHTTP(w, req)
			require.Equal(t, tc.status, w.Code)
			require.Equal(t, tc.namespaceReads, namespaceReads)
			require.Equal(t, tc.repositoryReads, repositoryReads)
			require.Equal(t, tc.mappings, mappings)
			entries := logs.FilterMessage("git_http_request_timing").All()
			require.Len(t, entries, 1)
			fields := entries[0].ContextMap()
			require.Len(t, fields, 8)
			require.Equal(t, tc.operation, fields["operation"])
			require.Equal(t, int64(tc.status), fields["status"])
			require.GreaterOrEqual(t, fields[tc.delayedPhase].(float64), float64(delay)/float64(time.Millisecond))
			var total float64
			for _, phase := range []string{"authentication_ms", "resolution_ms", "authorization_ms", "push_context_ms", "handler_ms"} {
				duration := fields[phase].(float64)
				require.GreaterOrEqual(t, duration, float64(0))
				total += duration
			}
			require.InDelta(t, total, fields["total_ms"], 0.000001)
			if tc.operation != "receive_pack" {
				require.Zero(t, fields["push_context_ms"])
			}
			switch tc.name {
			case "authentication denied":
				require.Zero(t, fields["resolution_ms"])
				fallthrough
			case "resolution failure":
				require.Zero(t, fields["authorization_ms"])
				fallthrough
			case "authorization denied", "push context failure":
				require.Zero(t, fields["handler_ms"])
			}
		})
	}
}

func TestGitHTTPRequestTimingConcurrentCancellation(t *testing.T) {
	core, logs := observer.New(zap.InfoLevel)
	var receives atomic.Int64
	client := &mockGitClient{
		receivePackFunc: func(ctx context.Context, _ string, _ io.Reader) ([]byte, error) {
			receives.Add(1)
			return nil, ctx.Err()
		},
	}
	router := NewMux(SmartHttpDeps{
		GitClient: client, Store: newTestStore(), Logger: zap.New(core),
		Ids: apiruntime.NewSequenceIDGenerator(), Registry: newTestRegistry(t),
	})
	var wg sync.WaitGroup
	for range 32 {
		wg.Go(func() {
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			req := httptest.NewRequest(http.MethodPost, "/gitstore/catalog/git-receive-pack", strings.NewReader("private-body")).WithContext(ctx)
			req.SetBasicAuth("test-writer", "private-password")
			w := httptest.NewRecorder()
			router.ServeHTTP(w, req)
			assert.Equal(t, http.StatusServiceUnavailable, w.Code)
		})
	}
	wg.Wait()
	require.EqualValues(t, 32, receives.Load())
	entries := logs.FilterMessage("git_http_request_timing").All()
	require.Len(t, entries, 32)
	for _, entry := range entries {
		fields := entry.ContextMap()
		require.Len(t, fields, 8)
		require.Equal(t, "receive_pack", fields["operation"])
		require.EqualValues(t, http.StatusServiceUnavailable, fields["status"])
		var total float64
		for _, phase := range []string{"authentication_ms", "resolution_ms", "authorization_ms", "push_context_ms", "handler_ms"} {
			total += fields[phase].(float64)
		}
		require.InDelta(t, total, fields["total_ms"], 0.000001)
	}
}

func (m *mockGitClient) InfoRefs(ctx context.Context, repoID string, service gitv1.Service) ([]byte, gitv1.Service, error) {
	if m.infoRefsFunc != nil {
		return m.infoRefsFunc(ctx, repoID, service)
	}
	return nil, gitv1.Service_SERVICE_UNSPECIFIED, errors.New("not set up")
}

func (m *mockGitClient) UploadPack(ctx context.Context, repoID string, body []byte) (io.Reader, error) {
	if m.uploadPackFunc != nil {
		return m.uploadPackFunc(ctx, repoID, body)
	}
	return nil, errors.New("not set up")
}

func (m *mockGitClient) ReceivePack(ctx context.Context, repoID string, body io.Reader) ([]byte, error) {
	if m.receivePackFunc != nil {
		return m.receivePackFunc(ctx, repoID, body)
	}
	return nil, errors.New("not set up")
}

type requestContextKey struct{}

func requestWithContextMarker(req *http.Request) (*http.Request, string) {
	const marker = "request-context-marker"
	return req.WithContext(context.WithValue(req.Context(), requestContextKey{}, marker)), marker
}

func assertRequestContext(t *testing.T, ctx context.Context, want string) {
	t.Helper()
	if got, _ := ctx.Value(requestContextKey{}).(string); got != want {
		t.Fatalf("expected request context marker %q, got %q", want, got)
	}
}

// T006: infoRefsHandler — upload-pack advertisement
func TestInfoRefsHandler_UploadPack(t *testing.T) {
	advertisement := []byte("001e# service=git-upload-pack\n0000")
	client := &mockGitClient{
		infoRefsFunc: func(_ context.Context, _ string, svc gitv1.Service) ([]byte, gitv1.Service, error) {
			if svc != gitv1.Service_SERVICE_GIT_UPLOAD_PACK {
				t.Errorf("expected GIT_UPLOAD_PACK, got %v", svc)
			}
			return advertisement, gitv1.Service_SERVICE_GIT_UPLOAD_PACK, nil
		},
	}
	registry := newTestRegistry(t)
	router := NewMux(SmartHttpDeps{
		GitClient: client,
		Store:     newTestStore(),
		Logger:    zap.NewNop(),
		Ids:       apiruntime.NewSequenceIDGenerator(),
		Registry:  registry,
	})

	req := httptest.NewRequest(http.MethodGet, "/gitstore/catalog/info/refs?service=git-upload-pack", nil)
	req.SetBasicAuth("test-writer", "test-password")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	resp := w.Result()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200, got %d", resp.StatusCode)
	}
	ct := resp.Header.Get("Content-Type")
	if ct != "application/x-git-upload-pack-advertisement" {
		t.Errorf("expected upload-pack Content-Type, got %q", ct)
	}
	body, _ := io.ReadAll(resp.Body)
	if !bytes.Contains(body, []byte("# service=git-upload-pack")) {
		t.Errorf("expected pkt-line service header in body, got: %q", body)
	}
}

// T006: infoRefsHandler — receive-pack advertisement
func TestInfoRefsHandler_ReceivePack(t *testing.T) {
	advertisement := []byte("001f# service=git-receive-pack\n0000")
	client := &mockGitClient{
		infoRefsFunc: func(_ context.Context, _ string, svc gitv1.Service) ([]byte, gitv1.Service, error) {
			return advertisement, gitv1.Service_SERVICE_GIT_RECEIVE_PACK, nil
		},
	}
	registry := newTestRegistry(t)

	router := NewMux(SmartHttpDeps{
		GitClient: client,
		Store:     newTestStore(),
		Logger:    zap.NewNop(),
		Ids:       apiruntime.NewSequenceIDGenerator(),
		Registry:  registry,
	})
	req := httptest.NewRequest(http.MethodGet, "/gitstore/catalog/info/refs?service=git-receive-pack", nil)
	req.SetBasicAuth("test-writer", "test-password")
	req.SetPathValue("namespace", "gitstore")
	req.SetPathValue("repo", "catalog")
	w := httptest.NewRecorder()

	router.ServeHTTP(w, req)

	resp := w.Result()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200, got %d", resp.StatusCode)
	}
	ct := resp.Header.Get("Content-Type")
	if ct != "application/x-git-receive-pack-advertisement" {
		t.Errorf("expected receive-pack Content-Type, got %q", ct)
	}
	body, _ := io.ReadAll(resp.Body)
	if !bytes.Contains(body, []byte("# service=git-receive-pack")) {
		t.Errorf("expected pkt-line service header in body, got: %q", body)
	}
}

// T007: uploadPackHandler — streams response without buffering
func TestUploadPackHandler_StreamsResponse(t *testing.T) {
	chunk1 := []byte("chunk-one-data")
	chunk2 := []byte("chunk-two-data")
	client := &mockGitClient{
		uploadPackFunc: func(_ context.Context, _ string, _ []byte) (io.Reader, error) {
			return io.MultiReader(bytes.NewReader(chunk1), bytes.NewReader(chunk2)), nil
		},
	}

	registry := newTestRegistry(t)

	router := NewMux(SmartHttpDeps{
		GitClient: client,
		Store:     newTestStore(),
		Logger:    zap.NewNop(),
		Ids:       apiruntime.NewSequenceIDGenerator(),
		Registry:  registry,
	})
	body := strings.NewReader("0011want abc123\n0000")
	req := httptest.NewRequest(http.MethodPost, "/gitstore/catalog/git-upload-pack", body)
	req.SetBasicAuth("test-writer", "test-password")
	w := httptest.NewRecorder()

	router.ServeHTTP(w, req)

	resp := w.Result()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200, got %d", resp.StatusCode)
	}
	ct := resp.Header.Get("Content-Type")
	if ct != "application/x-git-upload-pack-result" {
		t.Errorf("expected upload-pack-result Content-Type, got %q", ct)
	}
	if te := resp.Header.Get("Transfer-Encoding"); te != "" {
		t.Errorf("expected net/http to manage transfer encoding, got header %q", te)
	}
	respBody, _ := io.ReadAll(resp.Body)
	if !bytes.Contains(respBody, chunk1) || !bytes.Contains(respBody, chunk2) {
		t.Errorf("expected both chunks in response, got: %q", respBody)
	}
}

func TestUploadPackHandler_DecodesBoundedNegotiation(t *testing.T) {
	const limit = 1 << 20
	packet := []byte("0011want abc123\n0000")
	compress := func(body []byte) []byte {
		var buffer bytes.Buffer
		writer := gzip.NewWriter(&buffer)
		_, err := writer.Write(body)
		require.NoError(t, err)
		require.NoError(t, writer.Close())
		return buffer.Bytes()
	}
	for _, tc := range []struct {
		name, encoding string
		body, want     []byte
		status         int
	}{
		{"plain", "", packet, packet, http.StatusOK},
		{"identity", "identity", packet, packet, http.StatusOK},
		{"gzip", "gzip", compress(packet), packet, http.StatusOK},
		{"gzip_case", "GZip", compress(packet), packet, http.StatusOK},
		{"malformed", "gzip", []byte("not gzip"), nil, http.StatusBadRequest},
		{"truncated", "gzip", compress(packet)[:10], nil, http.StatusBadRequest},
		{"unsupported", "br", packet, nil, http.StatusUnsupportedMediaType},
		{"chained", "gzip, gzip", compress(packet), nil, http.StatusUnsupportedMediaType},
		{"expanded_limit", "gzip", compress(bytes.Repeat([]byte("x"), limit+1)), nil, http.StatusRequestEntityTooLarge},
		{"encoded_limit", "", bytes.Repeat([]byte("x"), limit+1), nil, http.StatusRequestEntityTooLarge},
		{"exact_limit", "gzip", compress(bytes.Repeat([]byte("x"), limit)), bytes.Repeat([]byte("x"), limit), http.StatusOK},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			client := &mockGitClient{uploadPackFunc: func(_ context.Context, _ string, body []byte) (io.Reader, error) {
				calls++
				require.Equal(t, tc.want, body)
				return strings.NewReader("0008NAK\n"), nil
			}}
			router := NewMux(SmartHttpDeps{
				GitClient: client, Store: newTestStore(), Logger: zap.NewNop(),
				Ids: apiruntime.NewSequenceIDGenerator(), Registry: newTestRegistry(t),
			})
			req := httptest.NewRequest(http.MethodPost, "/gitstore/catalog/git-upload-pack", bytes.NewReader(tc.body))
			req.SetBasicAuth("test-writer", "test-password")
			req.Header.Set("Content-Encoding", tc.encoding)
			response := httptest.NewRecorder()
			router.ServeHTTP(response, req)
			require.Equal(t, tc.status, response.Code)
			if tc.status == http.StatusOK {
				require.Equal(t, 1, calls)
			} else {
				require.Zero(t, calls, "invalid input must not reach the signed Git RPC")
			}
		})
	}
}

func TestHandler_PropagatesRequestContextToGitClient(t *testing.T) {
	registry := newTestRegistry(t)

	t.Run("info refs", func(t *testing.T) {
		client := &mockGitClient{
			infoRefsFunc: func(ctx context.Context, _ string, svc gitv1.Service) ([]byte, gitv1.Service, error) {
				assertRequestContext(t, ctx, "request-context-marker")
				return []byte("001e# service=git-upload-pack\n0000"), svc, nil
			},
		}
		router := NewMux(SmartHttpDeps{
			GitClient: client,
			Store:     newTestStore(),
			Logger:    zap.NewNop(),
			Ids:       apiruntime.NewSequenceIDGenerator(),
			Registry:  registry,
		})
		req := httptest.NewRequest(http.MethodGet, "/gitstore/catalog/info/refs?service=git-upload-pack", nil)
		req.SetBasicAuth("test-writer", "test-password")
		req, _ = requestWithContextMarker(req)
		w := httptest.NewRecorder()

		router.ServeHTTP(w, req)

		if resp := w.Result(); resp.StatusCode != http.StatusOK {
			t.Fatalf("expected 200, got %d", resp.StatusCode)
		}
	})

	t.Run("upload pack", func(t *testing.T) {
		client := &mockGitClient{
			uploadPackFunc: func(ctx context.Context, _ string, _ []byte) (io.Reader, error) {
				assertRequestContext(t, ctx, "request-context-marker")
				return bytes.NewReader([]byte("pack-result")), nil
			},
		}
		router := NewMux(SmartHttpDeps{
			GitClient: client,
			Store:     newTestStore(),
			Logger:    zap.NewNop(),
			Ids:       apiruntime.NewSequenceIDGenerator(),
			Registry:  registry,
		})
		req := httptest.NewRequest(http.MethodPost, "/gitstore/catalog/git-upload-pack", strings.NewReader("0011want abc123\n0000"))
		req.SetBasicAuth("test-writer", "test-password")
		req, _ = requestWithContextMarker(req)
		w := httptest.NewRecorder()

		router.ServeHTTP(w, req)

		if resp := w.Result(); resp.StatusCode != http.StatusOK {
			t.Fatalf("expected 200, got %d", resp.StatusCode)
		}
	})

	t.Run("receive pack", func(t *testing.T) {
		client := &mockGitClient{
			receivePackFunc: func(ctx context.Context, _ string, _ io.Reader) ([]byte, error) {
				assertRequestContext(t, ctx, "request-context-marker")
				return []byte("report-status"), nil
			},
		}
		router := NewMux(SmartHttpDeps{
			GitClient: client,
			Store:     newTestStore(),
			Logger:    zap.NewNop(),
			Ids:       apiruntime.NewSequenceIDGenerator(),
			Registry:  registry,
		})
		req := httptest.NewRequest(http.MethodPost, "/gitstore/catalog/git-receive-pack", strings.NewReader("pack-body"))
		req.SetBasicAuth("test-writer", "test-password")
		req, _ = requestWithContextMarker(req)
		w := httptest.NewRecorder()

		router.ServeHTTP(w, req)

		if resp := w.Result(); resp.StatusCode != http.StatusOK {
			t.Fatalf("expected 200, got %d", resp.StatusCode)
		}
	})
}

// T008: receivePackHandler — pipes request body to gRPC without full buffering
func TestReceivePackHandler_PipesBodyToGRPC(t *testing.T) {
	var receivedBytes []byte
	reportStatus := []byte("0014unpack ok\n00000000")
	client := &mockGitClient{
		receivePackFunc: func(_ context.Context, _ string, body io.Reader) ([]byte, error) {
			receivedBytes, _ = io.ReadAll(body)
			return reportStatus, nil
		},
	}
	registry := newTestRegistry(t)

	packData := []byte("0053\x00\x00\x00\x00\x00\x00\x00\x00refs/heads/main\x00\x00PACK...")
	router := NewMux(SmartHttpDeps{
		GitClient: client,
		Store:     newTestStore(),
		Logger:    zap.NewNop(),
		Ids:       apiruntime.NewSequenceIDGenerator(),
		Registry:  registry,
	})
	req := httptest.NewRequest(http.MethodPost, "/gitstore/catalog/git-receive-pack", bytes.NewReader(packData))
	req.SetBasicAuth("test-writer", "test-password")
	req.SetPathValue("namespace", "gitstore")
	req.SetPathValue("repo", "catalog")
	w := httptest.NewRecorder()

	router.ServeHTTP(w, req)

	resp := w.Result()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200, got %d", resp.StatusCode)
	}
	ct := resp.Header.Get("Content-Type")
	if ct != "application/x-git-receive-pack-result" {
		t.Errorf("expected receive-pack-result Content-Type, got %q", ct)
	}
	if !bytes.Equal(receivedBytes, packData) {
		t.Errorf("handler must pipe body bytes unchanged; got %d bytes, want %d", len(receivedBytes), len(packData))
	}
	respBody, _ := io.ReadAll(resp.Body)
	if !bytes.Equal(respBody, reportStatus) {
		t.Errorf("expected report-status in response, got: %q", respBody)
	}
}

// T009: unknown namespace/repo returns 404 with Git pkt-line error
func TestHandler_UnknownRepo_Returns404(t *testing.T) {
	client := &mockGitClient{}
	registry := newTestRegistry(t)

	router := NewMux(SmartHttpDeps{
		GitClient: client,
		Store:     newTestStore(),
		Logger:    zap.NewNop(),
		Ids:       apiruntime.NewSequenceIDGenerator(),
		Registry:  registry,
	})
	req := httptest.NewRequest(http.MethodGet, "/unknown/repo/info/refs?service=git-upload-pack", nil)
	req.SetBasicAuth("test-writer", "test-password")
	w := httptest.NewRecorder()

	router.ServeHTTP(w, req)

	resp := w.Result()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("expected 404, got %d", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if !bytes.Contains(body, []byte("ERR repository not found")) {
		t.Errorf("expected 'ERR repository not found' in body, got: %q", body)
	}
}

// T021: RepoResolver returns 404 pkt-line for unknown namespace/repo.
func TestRepoResolverNotFound(t *testing.T) {
	store := &testutil.StubStore{} // both lookups return ErrNotFound by default

	router := NewMux(SmartHttpDeps{
		GitClient: &mockGitClient{},
		Store:     store,
		Logger:    zap.NewNop(),
		Ids:       apiruntime.NewSequenceIDGenerator(),
		Registry:  newTestRegistry(t),
	})

	req := httptest.NewRequest(http.MethodGet, "/unknown-ns/unknown-repo/info/refs?service=git-upload-pack", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	resp := w.Result()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("expected 404, got %d", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if !bytes.Contains(body, []byte("ERR repository not found")) {
		t.Errorf("expected ERR repository not found in body, got: %q", body)
	}
}

// T022: RepoResolver stores repoID in gin context for known repo.
func TestRepoResolverSetsContext(t *testing.T) {
	const wantRepoID = "01960000-0000-7000-8000-000000000001"
	var capturedNamespace string
	store := &testutil.StubStore{
		GetRepositoryFunc: func(_ context.Context, id string) (*datastore.Repository, error) {
			return &datastore.Repository{UID: id, Name: "catalog", Namespace: "gitstore"}, nil
		},
		GetNamespaceByNameFunc: func(_ context.Context, id string) (*datastore.Namespace, error) {
			return &datastore.Namespace{UID: "ns-id-1", Name: id}, nil
		},
		LookupRepositoryFunc: func(_ context.Context, namespace, _ string) (*datastore.NamespaceMapping, error) {
			capturedNamespace = namespace
			return &datastore.NamespaceMapping{RepositoryID: wantRepoID}, nil
		},
	}

	var capturedRepoID string
	client := &mockGitClient{
		infoRefsFunc: func(ctx context.Context, repoID string, _ gitv1.Service) ([]byte, gitv1.Service, error) {
			capturedRepoID = repoID
			return []byte("001e# service=git-upload-pack\n0000"), gitv1.Service_SERVICE_GIT_UPLOAD_PACK, nil
		},
	}
	registry := auth.NewProviderRegistry(
		auth.NewChainedAuthN(&stubAuthNProviderWithPrincipal{
			principal: &auth.Principal{Subject: "reader", AuthMethod: "basic"},
			decision:  auth.Allow("stub-authn", "authenticated"),
		}),
		testutil.NewAllowAllAuthZ(),
		nil,
	)

	router := NewMux(SmartHttpDeps{
		GitClient: client,
		Store:     store,
		Logger:    zap.NewNop(),
		Ids:       apiruntime.NewSequenceIDGenerator(),
		Registry:  registry,
	})

	req := httptest.NewRequest(http.MethodGet, "/gitstore/catalog/info/refs?service=git-upload-pack", nil)
	req.SetBasicAuth("reader", "password")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d — store lookup may have failed", w.Code)
	}
	if capturedRepoID != wantRepoID {
		t.Errorf("expected repoID %q propagated to git client, got %q", wantRepoID, capturedRepoID)
	}
	if capturedNamespace != "gitstore" {
		t.Errorf("expected immutable namespace name %q, got %q", "gitstore", capturedNamespace)
	}
}

// T023: read-only principal attempting receive-pack is denied 403.
func TestGitHttpAuthorizerReadOnly(t *testing.T) {
	readOnlyPrincipal := &auth.Principal{Subject: "reader", AuthMethod: "basic"}
	stubAuthN := &stubAuthNProviderWithPrincipal{principal: readOnlyPrincipal, decision: auth.Allow("stub", "ok")}
	stubAuthZ := testutil.NewDenyAllAuthZ(t)

	registry := auth.NewProviderRegistry(
		auth.NewChainedAuthN(stubAuthN),
		stubAuthZ,
		nil,
	)

	const wantRepoID = "01960000-0000-7000-8000-000000000001"
	store := &testutil.StubStore{
		GetRepositoryFunc: func(_ context.Context, id string) (*datastore.Repository, error) {
			return &datastore.Repository{UID: id, Name: "catalog", Namespace: "gitstore"}, nil
		},
		GetNamespaceByNameFunc: func(_ context.Context, id string) (*datastore.Namespace, error) {
			return &datastore.Namespace{UID: "ns-id-1", Name: id}, nil
		},
		LookupRepositoryFunc: func(_ context.Context, _, _ string) (*datastore.NamespaceMapping, error) {
			return &datastore.NamespaceMapping{RepositoryID: wantRepoID}, nil
		},
	}

	router := NewMux(SmartHttpDeps{
		GitClient: &mockGitClient{},
		Store:     store,
		Logger:    zap.NewNop(),
		Ids:       apiruntime.NewSequenceIDGenerator(),
		Registry:  registry,
	})

	req := httptest.NewRequest(http.MethodPost, "/gitstore/catalog/git-receive-pack", strings.NewReader("pack"))
	req.SetBasicAuth("reader", "password")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusForbidden {
		t.Errorf("expected 403 for read-only principal on receive-pack, got %d", w.Code)
	}
}

func TestGitHttpAuthorizerAnonymousDenyChallengesForCredentials(t *testing.T) {
	registry := auth.NewProviderRegistry(
		auth.NewChainedAuthN(anonymous.New()),
		testutil.NewDenyAllAuthZ(t),
		nil,
	)

	store := &testutil.StubStore{
		GetRepositoryFunc: func(_ context.Context, id string) (*datastore.Repository, error) {
			return &datastore.Repository{UID: id, Name: "catalog", Namespace: "gitstore"}, nil
		},
		GetNamespaceByNameFunc: func(_ context.Context, id string) (*datastore.Namespace, error) {
			return &datastore.Namespace{UID: "ns-id-1", Name: id}, nil
		},
		LookupRepositoryFunc: func(_ context.Context, _, _ string) (*datastore.NamespaceMapping, error) {
			return &datastore.NamespaceMapping{RepositoryID: "01960000-0000-7000-8000-000000000001"}, nil
		},
	}

	router := NewMux(SmartHttpDeps{
		GitClient: &mockGitClient{}, Store: store, Logger: zap.NewNop(),
		Ids: apiruntime.NewSequenceIDGenerator(), Registry: registry,
	})
	req := httptest.NewRequest(http.MethodGet, "/gitstore/catalog/info/refs?service=git-upload-pack", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusUnauthorized, w.Code)
	assert.Equal(t, `Basic realm="GitStore"`, w.Header().Get("WWW-Authenticate"))
}

// TestGitHttpAuthorizerAnonymousReceivePackDiscoveryChallengesForCredentials
// covers the info/refs discovery request for git-receive-pack (used by
// `git push` before the actual push), which is disambiguated from
// git-upload-pack discovery by the ?service= query param rather than the
// URL path. An anonymous request must be classified as a write and receive
// a 401 (so Git retries with embedded credentials), never a 503 — a
// regression previously missed because existing coverage only exercised
// anonymous-deny for the upload-pack (?service=git-upload-pack) discovery
// path and the standalone infoRefsHandler in isolation from the authorizer.
func TestGitHttpAuthorizerAnonymousReceivePackDiscoveryChallengesForCredentials(t *testing.T) {
	registry := auth.NewProviderRegistry(
		auth.NewChainedAuthN(anonymous.New()),
		testutil.NewDenyAllAuthZ(t),
		nil,
	)

	store := &testutil.StubStore{
		GetRepositoryFunc: func(_ context.Context, id string) (*datastore.Repository, error) {
			return &datastore.Repository{UID: id, Name: "catalog", Namespace: "gitstore"}, nil
		},
		GetNamespaceByNameFunc: func(_ context.Context, id string) (*datastore.Namespace, error) {
			return &datastore.Namespace{UID: "ns-id-1", Name: id}, nil
		},
		LookupRepositoryFunc: func(_ context.Context, _, _ string) (*datastore.NamespaceMapping, error) {
			return &datastore.NamespaceMapping{RepositoryID: "01960000-0000-7000-8000-000000000001"}, nil
		},
	}

	router := NewMux(SmartHttpDeps{
		GitClient: &mockGitClient{}, Store: store, Logger: zap.NewNop(),
		Ids: apiruntime.NewSequenceIDGenerator(), Registry: registry,
	})
	req := httptest.NewRequest(http.MethodGet, "/gitstore/catalog/info/refs?service=git-receive-pack", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusUnauthorized, w.Code)
	assert.Equal(t, `Basic realm="GitStore"`, w.Header().Get("WWW-Authenticate"))
}

func TestGitHttpAuthorizerMarksPolicyApprovedAnonymousRead(t *testing.T) {
	registry := auth.NewProviderRegistry(
		auth.NewChainedAuthN(anonymous.New()),
		testutil.NewAllowAllAuthZ(),
		nil,
	)
	const wantRepoID = "01960000-0000-7000-8000-000000000001"
	store := &testutil.StubStore{
		GetRepositoryFunc: func(_ context.Context, id string) (*datastore.Repository, error) {
			return &datastore.Repository{UID: id, Name: "catalog", Namespace: "gitstore"}, nil
		},
		GetNamespaceByNameFunc: func(_ context.Context, id string) (*datastore.Namespace, error) {
			return &datastore.Namespace{UID: "ns-id-1", Name: id}, nil
		},
		LookupRepositoryFunc: func(_ context.Context, _, _ string) (*datastore.NamespaceMapping, error) {
			return &datastore.NamespaceMapping{RepositoryID: wantRepoID}, nil
		},
	}
	client := &mockGitClient{
		infoRefsFunc: func(ctx context.Context, _ string, _ gitv1.Service) ([]byte, gitv1.Service, error) {
			assert.True(t, auth.AuthorizedAnonymousFromContext(ctx))
			return []byte("001e# service=git-upload-pack\n0000"), gitv1.Service_SERVICE_GIT_UPLOAD_PACK, nil
		},
	}
	router := NewMux(SmartHttpDeps{
		GitClient: client, Store: store, Logger: zap.NewNop(),
		Ids: apiruntime.NewSequenceIDGenerator(), Registry: registry,
	})

	w := httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/gitstore/catalog/info/refs?service=git-upload-pack", nil))
	assert.Equal(t, http.StatusOK, w.Code)
}

// TestGitHttpAuthorizerAllowAllAnonymousWriteChallengesForCredentials reproduces
// the scenario where the configured AuthZ provider is misconfigured (or
// intentionally permissive, e.g. "allow-all" in local dev) and approves an
// anonymous write at the GitHttpAuthorizer layer. gitclient.RequestAuthorization
// enforces a defense-in-depth invariant that anonymous principals are never
// approved for a write action regardless of that upstream decision, and the
// resulting error must surface as 401 (prompting Git to retry with
// credentials) rather than a blanket 503, or a plain `git push` against a
// dev stack with an over-permissive AuthZ provider hangs on a 503 that Git
// cannot recover from.
func TestGitHttpAuthorizerAllowAllAnonymousWriteChallengesForCredentials(t *testing.T) {
	registry := auth.NewProviderRegistry(
		auth.NewChainedAuthN(anonymous.New()),
		testutil.NewAllowAllAuthZ(),
		nil,
	)
	const wantRepoID = "01960000-0000-7000-8000-000000000001"
	store := &testutil.StubStore{
		GetRepositoryFunc: func(_ context.Context, id string) (*datastore.Repository, error) {
			return &datastore.Repository{UID: id, Name: "catalog", Namespace: "gitstore"}, nil
		},
		GetNamespaceByNameFunc: func(_ context.Context, id string) (*datastore.Namespace, error) {
			return &datastore.Namespace{UID: "ns-id-1", Name: id}, nil
		},
		LookupRepositoryFunc: func(_ context.Context, _, _ string) (*datastore.NamespaceMapping, error) {
			return &datastore.NamespaceMapping{RepositoryID: wantRepoID}, nil
		},
	}
	// Calls the real gitclient.RequestAuthorization so this test exercises the
	// actual defense-in-depth invariant rather than a stubbed-out approximation.
	client := &mockGitClient{
		infoRefsFunc: func(ctx context.Context, repoID string, service gitv1.Service) ([]byte, gitv1.Service, error) {
			action := "repository.contents.read"
			if service == gitv1.Service_SERVICE_GIT_RECEIVE_PACK {
				action = "repository.contents.write"
			}
			if _, err := gitclient.RequestAuthorization(ctx, action, repoID); err != nil {
				return nil, gitv1.Service_SERVICE_UNSPECIFIED, err
			}
			return []byte("001e# service=git-receive-pack\n0000"), gitv1.Service_SERVICE_GIT_RECEIVE_PACK, nil
		},
	}
	router := NewMux(SmartHttpDeps{
		GitClient: client, Store: store, Logger: zap.NewNop(),
		Ids: apiruntime.NewSequenceIDGenerator(), Registry: registry,
	})

	w := httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/gitstore/catalog/info/refs?service=git-receive-pack", nil))

	assert.Equal(t, http.StatusUnauthorized, w.Code)
	assert.Equal(t, `Basic realm="GitStore"`, w.Header().Get("WWW-Authenticate"))
}

// T024: write-capable principal on receive-pack passes through.
func TestGitHttpAuthorizerWriteAllowed(t *testing.T) {
	writePrincipal := &auth.Principal{Subject: "writer", AuthMethod: "basic"}
	stubAuthN := &stubAuthNProviderWithPrincipal{principal: writePrincipal, decision: auth.Allow("stub", "ok")}
	stubAuthZ := testutil.NewAllowAllAuthZ()

	registry := auth.NewProviderRegistry(
		auth.NewChainedAuthN(stubAuthN),
		stubAuthZ,
		nil,
	)

	const wantRepoID = "01960000-0000-7000-8000-000000000001"
	store := &testutil.StubStore{
		GetRepositoryFunc: func(_ context.Context, id string) (*datastore.Repository, error) {
			return &datastore.Repository{UID: id, Name: "catalog", Namespace: "gitstore"}, nil
		},
		GetNamespaceByNameFunc: func(_ context.Context, id string) (*datastore.Namespace, error) {
			return &datastore.Namespace{UID: "ns-id-1", Name: id}, nil
		},
		LookupRepositoryFunc: func(_ context.Context, _, _ string) (*datastore.NamespaceMapping, error) {
			return &datastore.NamespaceMapping{RepositoryID: wantRepoID}, nil
		},
	}

	client := &mockGitClient{
		receivePackFunc: func(_ context.Context, _ string, _ io.Reader) ([]byte, error) {
			return []byte("0014unpack ok\n00000000"), nil
		},
	}

	router := NewMux(SmartHttpDeps{
		GitClient: client,
		Store:     store,
		Logger:    zap.NewNop(),
		Ids:       apiruntime.NewSequenceIDGenerator(),
		Registry:  registry,
	})

	req := httptest.NewRequest(http.MethodPost, "/gitstore/catalog/git-receive-pack", strings.NewReader("pack"))
	req.SetBasicAuth("writer", "password")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200 for write-capable principal on receive-pack, got %d", w.Code)
	}
}

// T025: GitHttpAuthorizer without RepoResolver having run returns 500.
// This test exercises the middleware directly without RepoResolver in chain.
func TestGitHttpAuthorizerMissingContext(t *testing.T) {
	stubAuthZ := testutil.NewAllowAllAuthZ()
	registry := auth.NewProviderRegistry(
		auth.NewChainedAuthN(anonymous.New()),
		stubAuthZ,
		nil,
	)

	authMiddleware := security.NewAuthorize(registry, zap.NewNop())
	r := gin.New()
	r.POST("/:namespace/:repo/git-receive-pack", authMiddleware.GitHttpAuthorizer, func(c *gin.Context) {
		c.Status(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodPost, "/gitstore/catalog/git-receive-pack", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusInternalServerError {
		t.Errorf("expected 500 when repoID not set in context, got %d", w.Code)
	}
}

// stubAuthNProviderWithPrincipal always returns a fixed principal.
type stubAuthNProviderWithPrincipal struct {
	principal *auth.Principal
	decision  auth.Decision
	delay     time.Duration
}

func (s *stubAuthNProviderWithPrincipal) Name() string { return "stub-authn" }
func (s *stubAuthNProviderWithPrincipal) Capabilities() auth.Capability {
	return auth.CapAuthenticate
}
func (s *stubAuthNProviderWithPrincipal) Authenticate(_ context.Context, _ auth.AuthRequest) (*auth.Principal, auth.Decision, error) {
	time.Sleep(s.delay)
	return s.principal, s.decision, nil
}
func (s *stubAuthNProviderWithPrincipal) RevokeSession(_ context.Context, _ string, _ time.Time) error {
	return nil
}
func (s *stubAuthNProviderWithPrincipal) RefreshSession(_ context.Context, _ string) (string, time.Time, error) {
	return "", time.Time{}, auth.ErrNotSupported
}
func (s *stubAuthNProviderWithPrincipal) IssueSession(_ context.Context, _ string) (string, time.Time, error) {
	return "", time.Time{}, auth.ErrNotSupported
}

// T036: receivePackHandler passes PushContext (stored in gin context by PushContextInserter) to ReceivePack.
func TestReceivePackAttachesPushContext(t *testing.T) {
	const repoID = "01960000-0000-7000-8000-000000000001"
	const nsID = "ns-id-1"

	store := &testutil.StubStore{
		GetNamespaceByNameFunc: func(_ context.Context, id string) (*datastore.Namespace, error) {
			return &datastore.Namespace{UID: nsID, Name: id}, nil
		},
		LookupRepositoryFunc: func(_ context.Context, _, _ string) (*datastore.NamespaceMapping, error) {
			return &datastore.NamespaceMapping{RepositoryID: repoID}, nil
		},
		GetRepositoryFunc: func(_ context.Context, _ string) (*datastore.Repository, error) {
			return &datastore.Repository{
				UID:              repoID,
				Namespace:        "gitstore",
				Name:             "catalog",
				MaxPackSizeBytes: 52428800,
				MaxFileSizeBytes: 10485760,
			}, nil
		},
	}

	var capturedCtx context.Context
	client := &mockGitClient{
		receivePackFunc: func(ctx context.Context, _ string, _ io.Reader) ([]byte, error) {
			capturedCtx = ctx
			return []byte("0014unpack ok\n00000000"), nil
		},
	}

	writePrincipal := &auth.Principal{Subject: "writer", AuthMethod: "basic"}
	stubAuthN := &stubAuthNProviderWithPrincipal{principal: writePrincipal, decision: auth.Allow("stub", "ok")}
	stubAuthZ := testutil.NewAllowAllAuthZ()

	registry := auth.NewProviderRegistry(
		auth.NewChainedAuthN(stubAuthN),
		stubAuthZ,
		nil,
	)

	router := NewMux(SmartHttpDeps{
		GitClient: client,
		Store:     store,
		Logger:    zap.NewNop(),
		Ids:       apiruntime.NewSequenceIDGenerator(),
		Registry:  registry,
	})

	req := httptest.NewRequest(http.MethodPost, "/gitstore/catalog/git-receive-pack", strings.NewReader("pack-body"))
	req.SetBasicAuth("writer", "password")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	require.NotNil(t, capturedCtx, "ReceivePack must have been called")
	pc := gitclient.PushContextFromContext(capturedCtx)
	require.NotNil(t, pc, "PushContext must be in context passed to ReceivePack")
	assert.Equal(t, repoID, pc.RepositoryId)
	require.NotNil(t, pc.Authorization)
	require.NotNil(t, pc.Authorization.Actor)
	assert.Equal(t, "writer", pc.Authorization.Actor.Subject)
	assert.Equal(t, "basic", pc.Authorization.Actor.AuthMethod)
	assert.Equal(t, repoID, pc.Authorization.RepositoryId)
	assert.Equal(t, "repository.contents.write", pc.Authorization.Action)
	assert.Equal(t, "gitstore", pc.Namespace)
	assert.Equal(t, "catalog", pc.RepositoryName)
	assert.Equal(t, int64(52428800), pc.Policy.MaxPackSizeBytes)
	assert.Equal(t, int64(10485760), pc.Policy.MaxFileSizeBytes)
}

// T010: gRPC unavailability returns 503 with Git pkt-line error, no retry
func TestHandler_GRPCUnavailable_Returns503(t *testing.T) {
	callCount := 0
	client := &mockGitClient{
		infoRefsFunc: func(_ context.Context, _ string, _ gitv1.Service) ([]byte, gitv1.Service, error) {
			callCount++
			return nil, gitv1.Service_SERVICE_UNSPECIFIED, errors.New("connection refused")
		},
	}
	registry := newTestRegistry(t)

	router := NewMux(SmartHttpDeps{
		GitClient: client,
		Store:     newTestStore(),
		Logger:    zap.NewNop(),
		Ids:       apiruntime.NewSequenceIDGenerator(),
		Registry:  registry,
	})
	req := httptest.NewRequest(http.MethodGet, "/gitstore/catalog/info/refs?service=git-upload-pack", nil)
	w := httptest.NewRecorder()

	router.ServeHTTP(w, req)

	resp := w.Result()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("expected 503, got %d", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	defer func(Body io.ReadCloser) {
		err := Body.Close()
		if err != nil {
			t.Error(err)
		}
	}(resp.Body)
	if !bytes.Contains(body, []byte("ERR service unavailable")) {
		t.Errorf("expected 'ERR service unavailable' in body, got: %q", body)
	}
	if callCount != 1 {
		t.Errorf("expected exactly 1 attempt (no retry), got %d", callCount)
	}
}
