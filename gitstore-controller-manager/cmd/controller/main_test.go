// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package main

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gitstore-dev/gitstore/controller-manager/internal/cache"
	"github.com/gitstore-dev/gitstore/controller-manager/internal/categorytaxonomy"
	"github.com/gitstore-dev/gitstore/controller-manager/internal/checkpoint"
	"github.com/gitstore-dev/gitstore/controller-manager/internal/config"
	"github.com/gitstore-dev/gitstore/controller-manager/internal/graphqlclient"
	"github.com/gitstore-dev/gitstore/controller-manager/internal/health"
	"github.com/gitstore-dev/gitstore/controller-manager/internal/listwatch"
	"github.com/gitstore-dev/gitstore/controller-manager/internal/manager"
	namespacecontroller "github.com/gitstore-dev/gitstore/controller-manager/internal/namespace"
	repositorycontroller "github.com/gitstore-dev/gitstore/controller-manager/internal/repository"
	"github.com/gitstore-dev/gitstore/controller-manager/internal/secret"
	"github.com/gitstore-dev/gitstore/controller-manager/internal/status"
	"github.com/gitstore-dev/gitstore/controller-manager/internal/types"
	"github.com/gitstore-dev/gitstore/observability"
	"github.com/gitstore-dev/gitstore/secretmaterial"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"
	logpb "go.opentelemetry.io/proto/otlp/collector/logs/v1"
	tracepb "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
	"google.golang.org/protobuf/proto"
)

type telemetryReconcileFunc func(context.Context, types.WorkItemKey) types.ReconcileResult

func (f telemetryReconcileFunc) Reconcile(ctx context.Context, key types.WorkItemKey) types.ReconcileResult {
	return f(ctx, key)
}

func restoreTelemetry(t *testing.T) {
	t.Helper()
	traces, logs, metrics := otel.GetTracerProvider(), otel.GetLoggerProvider(), otel.GetMeterProvider()
	propagator, handler := otel.GetTextMapPropagator(), otel.GetErrorHandler()
	t.Cleanup(func() {
		otel.SetTracerProvider(traces)
		otel.SetLoggerProvider(logs)
		otel.SetMeterProvider(metrics)
		otel.SetTextMapPropagator(propagator)
		otel.SetErrorHandler(handler)
	})
}

func telemetryAttribute(attrs []*commonpb.KeyValue, key string) string {
	for _, attr := range attrs {
		if attr.Key == key {
			return attr.Value.GetStringValue()
		}
	}
	return ""
}

func TestControllerProfilerStartupFailureAndCleanup(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer listener.Close()
	address := listener.Addr().String()
	cfg := &config.Config{Log: config.LogConfig{Level: "error", Format: "json"}}
	cfg.Controller.Profiling.URI = "http://" + address
	require.ErrorContains(t, run(cfg), "initialize capacity profiler", "an occupied profiler port must fail startup explicitly")
	cfg.Controller.Profiling.URI = ""
	require.ErrorContains(t, run(cfg), "build credential source", "disabled profiling must not interfere with startup")
	require.NoError(t, listener.Close())
	cfg.Controller.Profiling.URI = "http://" + address
	require.ErrorContains(t, run(cfg), "build credential source", "the profiler must start before credentials are constructed")
	rebound, err := net.Listen("tcp", address)
	require.NoError(t, err, "a later startup failure must shut down the profiler and release its listener")
	require.NoError(t, rebound.Close())
}

func TestControllerProfilerIsNotOnPublicMux(t *testing.T) {
	handler := buildMux(manager.New())
	for _, path := range []string{"/debug/pprof/", "/debug/pprof/heap", "/debug/pprof/profile?seconds=1"} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
		require.Equal(t, http.StatusNotFound, response.Code, path)
	}
}

func TestControllerTelemetryStartupReconcileClientDelivery(t *testing.T) {
	restoreTelemetry(t)
	var mu sync.Mutex
	var spans tracepb.ExportTraceServiceRequest
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
			spans.ResourceSpans = append(spans.ResourceSpans, batch.ResourceSpans...)
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
	t.Setenv("GITSTORE_CONTROLLER__SERVICEACCOUNT__UID", "test-uid")
	t.Setenv("GITSTORE_OBSERVABILITY__OTLP__URI", collector.URL)
	t.Setenv("GITSTORE_OBSERVABILITY__OTLP__TRACE_SAMPLE_RATIO", "1")
	cfg, err := config.LoadFrom("../../../config/config.toml")
	require.NoError(t, err)
	core, local := observer.New(zap.InfoLevel)
	log, shutdown, err := initTelemetry(t.Context(), cfg, zap.New(core))
	require.NoError(t, err)
	t.Cleanup(func() { _ = shutdown(context.Background()) })
	propagated := make(chan trace.SpanContext, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := propagation.TraceContext{}.Extract(r.Context(), propagation.HeaderCarrier(r.Header))
		propagated <- trace.SpanContextFromContext(ctx)
		require.Empty(t, r.Header.Get("Baggage"))
		_, _ = io.WriteString(w, `{"data":{"ok":true}}`)
	}))
	defer server.Close()
	client := graphqlclient.New(server.URL, graphqlclient.NewStaticToken("private-credential"))
	mgr := manager.New().WithLogger(log)
	c := cache.New[string]()
	c.MarkSynced()
	called := make(chan struct{}, 1)
	reconciler := telemetryReconcileFunc(func(ctx context.Context, _ types.WorkItemKey) types.ReconcileResult {
		require.True(t, trace.SpanFromContext(ctx).IsRecording())
		require.NoError(t, client.Query(ctx, "query { privateResource }", map[string]any{"private": "body"}, nil))
		called <- struct{}{}
		return types.ResultOK()
	})
	require.NoError(t, mgr.Register(manager.ReconcilerRegistration{
		Kind: "Namespace", Reconciler: reconciler, Cache: c, WorkerCount: 1,
	}))
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	stopped := make(chan error, 1)
	go func() { stopped <- mgr.Start(ctx) }()
	before := testutil.ToFloat64(health.ReconcileTotal.WithLabelValues("Namespace", "success"))
	require.NoError(t, mgr.Enqueue(types.WorkItemKey{Kind: "Namespace", Namespace: "private-ns", Name: "private-name"}))
	select {
	case <-called:
	case <-time.After(3 * time.Second):
		t.Fatal("reconcile not dispatched")
	}
	require.Eventually(t, func() bool {
		return testutil.ToFloat64(health.ReconcileTotal.WithLabelValues("Namespace", "success")) == before+1
	}, time.Second, time.Millisecond)
	cancel()
	require.NoError(t, <-stopped)
	require.NoError(t, shutdown(t.Context()))
	remote := <-propagated
	mu.Lock()
	defer mu.Unlock()
	var reconcileID, clientParent, clientID, traceID []byte
	for _, resource := range spans.ResourceSpans {
		require.Equal(t, observability.ControllerService, telemetryAttribute(resource.Resource.Attributes, "service.name"))
		for _, scope := range resource.ScopeSpans {
			for _, span := range scope.Spans {
				require.NotContains(t, span.String(), "private")
				switch span.Name {
				case "controller.reconcile":
					reconcileID, traceID = span.SpanId, span.TraceId
					require.Equal(t, "success", telemetryAttribute(span.Attributes, "reconcile.result"))
				case "api.request":
					clientParent, clientID = span.ParentSpanId, span.SpanId
				}
			}
		}
	}
	require.NotEmpty(t, reconcileID)
	require.NotEmpty(t, clientID)
	require.Equal(t, reconcileID, clientParent)
	remoteID, remoteTrace := remote.SpanID(), remote.TraceID()
	require.Equal(t, clientID, remoteID[:])
	require.Equal(t, traceID, remoteTrace[:])
	linked := false
	for _, resource := range logs.ResourceLogs {
		require.Equal(t, observability.ControllerService, telemetryAttribute(resource.Resource.Attributes, "service.name"))
		for _, scope := range resource.ScopeLogs {
			for _, record := range scope.LogRecords {
				require.NotContains(t, record.String(), "private")
				if record.Body.GetStringValue() == "reconcile attempt completed" {
					linked = true
					require.Equal(t, reconcileID, record.SpanId)
					require.Equal(t, traceID, record.TraceId)
					require.Equal(t, "success", telemetryAttribute(record.Attributes, "result"))
				}
			}
		}
	}
	require.True(t, linked)
	require.Equal(t, 1, local.FilterMessage("reconcile attempt completed").Len())
	metricsHandler := health.NewMetricsHandler(mgr)
	request := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	request.Header.Set("Accept", "application/openmetrics-text; version=1.0.0")
	metrics := httptest.NewRecorder()
	metricsHandler.ServeHTTP(metrics, request)
	require.Contains(t, metrics.Header().Get("Content-Type"), "application/openmetrics-text")
	exemplarFound := false
	for _, line := range strings.Split(metrics.Body.String(), "\n") {
		if strings.HasPrefix(line, "gitstore_controller_reconcile_duration_seconds_bucket{") &&
			strings.Contains(line, `span_id="`+hex.EncodeToString(reconcileID)+`"`) {
			require.Contains(t, line, `trace_id="`+hex.EncodeToString(traceID)+`"`)
			require.Contains(t, line, `kind="Namespace"`)
			require.Contains(t, line, `result="success"`)
			exemplarFound = true
		}
	}
	require.True(t, exemplarFound, "the actual reconcile completion log and native metric must identify the same span")
	legacy := httptest.NewRecorder()
	metricsHandler.ServeHTTP(legacy, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	require.Contains(t, legacy.Header().Get("Content-Type"), "text/plain")
	require.NotContains(t, legacy.Body.String(), "trace_id")
}

func TestControllerTelemetryDisabledInvalidAndStartupFailure(t *testing.T) {
	restoreTelemetry(t)
	cfg := &config.Config{}
	log := zap.NewNop()
	before := otel.GetTracerProvider()
	resolved, shutdown, err := initTelemetry(t.Context(), cfg, log)
	require.NoError(t, err)
	require.Same(t, log, resolved)
	require.Same(t, before, otel.GetTracerProvider())
	require.NoError(t, shutdown(t.Context()))
	cfg.Observability.OTLP.URI = "http://user:private@collector"
	_, _, err = initTelemetry(t.Context(), cfg, log)
	require.Error(t, err)
	require.Same(t, before, otel.GetTracerProvider())

	var delivered atomic.Int64
	collector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		delivered.Add(1)
		w.Header().Set("Content-Type", "application/x-protobuf")
	}))
	defer collector.Close()
	cfg.Log.Level, cfg.Log.Format = "info", "json"
	cfg.Observability.OTLP.URI = collector.URL
	require.ErrorContains(t, run(cfg), "build credential source")
	require.Positive(t, delivered.Load(), "startup errors must drain telemetry instead of bypassing defers via Fatal")
}

func TestCategoryPathRelatedWorkSurvivesRestartWithoutCountFanout(t *testing.T) {
	dir := t.TempDir()
	store, err := checkpoint.OpenDiskStore(dir, "CategoryTaxonomy")
	require.NoError(t, err)
	category := categorytaxonomy.CategoryTaxonomy{
		UID: "leaf-id", Namespace: "shop", Name: "leaf", Generation: 1, ResourceVersion: "1",
		Status: status.ResourceStatus{Resolved: json.RawMessage(`{"path":["root","leaf"],"productCount":1}`)},
	}
	relatedKey := types.WorkItemKey{Kind: "ProductCategory", Namespace: "shop", Name: "leaf"}
	item := func() checkpoint.DiskItem {
		value, err := json.Marshal(category)
		require.NoError(t, err)
		version, err := categorytaxonomy.ProductRelationVersion(category)
		require.NoError(t, err)
		return checkpoint.DiskItem{
			Key:     types.WorkItemKey{Kind: "CategoryTaxonomy", Namespace: "shop", Name: "leaf"},
			Version: category.ResourceVersion, Value: value, Related: []types.WorkItemKey{relatedKey},
			RelatedVersion: version,
		}
	}
	generation, err := store.BeginSnapshot(t.Context())
	require.NoError(t, err)
	require.NoError(t, store.PutSnapshotPage(t.Context(), generation, []checkpoint.DiskItem{item()}))
	require.NoError(t, store.FinishSnapshot(t.Context(), generation, "cursor-1"))
	pending, err := store.RelatedPending(t.Context(), "", 10)
	require.NoError(t, err)
	require.Len(t, pending, 1)
	ok, err := store.AcknowledgeRelated(t.Context(), pending[0].Key, pending[0].Token)
	require.NoError(t, err)
	require.True(t, ok)
	category.ResourceVersion = "2"
	category.Status.Resolved = json.RawMessage(`{"path":["anchor","root","leaf"],"productCount":1}`)
	require.NoError(t, store.Apply(t.Context(), item(), false, false, "cursor-2"))
	require.NoError(t, store.Close())
	store, err = checkpoint.OpenDiskStore(dir, "CategoryTaxonomy")
	require.NoError(t, err)
	defer store.Close()
	pending, err = store.RelatedPending(t.Context(), "", 10)
	require.NoError(t, err)
	require.Len(t, pending, 1)
	require.Equal(t, relatedKey, pending[0].Key)
	ok, err = store.AcknowledgeRelated(t.Context(), pending[0].Key, pending[0].Token)
	require.NoError(t, err)
	require.True(t, ok)
	category.ResourceVersion = "3"
	category.Status.Resolved = json.RawMessage(`{"path":["anchor","root","leaf"],"productCount":2}`)
	require.NoError(t, store.Apply(t.Context(), item(), false, false, "cursor-3"))
	pending, err = store.RelatedPending(t.Context(), "", 10)
	require.NoError(t, err)
	require.Empty(t, pending, "a count-only status event must not fan out to referencing Products")
}

func TestProductionRegistrationUsesDiskForEveryKind(t *testing.T) {
	dir := t.TempDir()
	cfg := &config.Config{Controller: config.ControllerConfig{Checkpoint: config.CheckpointConfig{Dir: dir}}}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	mgr := manager.New()
	var runners sync.WaitGroup
	client := graphqlclient.New("http://unused.invalid/graphql", graphqlclient.NewStaticToken("test"))
	closeStores, err := registerDiskControllers(ctx, &runners, mgr, cfg, zap.NewNop(), client)
	require.NoError(t, err)
	t.Cleanup(func() { _ = closeStores() })
	runners.Wait()
	keys := []types.WorkItemKey{
		{Kind: "Namespace", Name: "probe"},
		{Kind: "Repository", Namespace: "shop", Name: "probe"},
		{Kind: "CategoryTaxonomy", Namespace: "shop", Name: "probe"},
		{Kind: "Product", Namespace: "shop", Name: "probe"},
	}
	for _, key := range keys {
		require.NoError(t, mgr.Enqueue(key))
		require.True(t, mgr.KindStats()[key.Kind].Registered)
	}
	require.NoError(t, closeStores())
	for _, key := range keys {
		store, err := checkpoint.OpenDiskStore(dir, key.Kind)
		require.NoError(t, err)
		pending, err := store.Pending(t.Context(), "", 1)
		require.NoError(t, err)
		require.Len(t, pending, 1)
		require.Equal(t, key, pending[0].Key, "production registration did not persist its work")
		require.NoError(t, store.Close())
	}
}

func TestPoisonRequeueSupportsClusterScopedDiskKeys(t *testing.T) {
	store, err := checkpoint.OpenDiskStore(t.TempDir(), "Namespace")
	require.NoError(t, err)
	defer store.Close()
	key := types.WorkItemKey{Kind: "Namespace", Name: "example"}
	require.NoError(t, store.Enqueue(t.Context(), key))
	pending, err := store.Pending(t.Context(), "", 1)
	require.NoError(t, err)
	require.Len(t, pending, 1)
	ok, err := store.DeferWork(t.Context(), key, pending[0].Token, 0, "test failure", 1)
	require.NoError(t, err)
	require.True(t, ok)
	mgr := manager.New()
	require.NoError(t, mgr.Register(manager.ReconcilerRegistration{
		Kind: "Namespace", Disk: store,
		Cache: cache.New[string](),
		Reconciler: namespacecontroller.NewReconcilerWithLookup(
			func(context.Context, types.WorkItemKey) (namespacecontroller.Namespace, bool, error) {
				return namespacecontroller.Namespace{}, false, nil
			}, nil, nil, nil),
	}))
	response := httptest.NewRecorder()
	buildMux(mgr).ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/controller/v1/poison/Namespace/example/requeue", nil))
	require.Equal(t, http.StatusNoContent, response.Code)
	require.False(t, mgr.IsQuarantined(key))
}

func TestParseConfigFile(t *testing.T) {
	paths, err := parseConfigFiles([]string{"--config-file", "/config/shared.toml"})
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) != 1 || paths[0] != "/config/shared.toml" {
		t.Fatalf("paths = %v", paths)
	}
}

func TestParseConfigFilesRepeated(t *testing.T) {
	paths, err := parseConfigFiles([]string{
		"--config-file", "/config/shared.toml",
		"--config-file", "/config/overlay.toml",
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"/config/shared.toml", "/config/overlay.toml"}
	if len(paths) != len(want) || paths[0] != want[0] || paths[1] != want[1] {
		t.Fatalf("paths = %v", paths)
	}
}

func TestBuildCredentialSourceUsesResolvedServiceAccountKey(t *testing.T) {
	_, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(privateKey)
	if err != nil {
		t.Fatal(err)
	}
	ref := secret.Ref{Kind: "SecretRef", Name: "controller-manager"}
	provider := secret.BootstrapProviderConfig{Type: secret.ProviderEnvironment, EnvVariable: "TEST_CONTROLLER_SIGNING_RECORD"}
	record, err := json.Marshal(map[string]any{
		"format": "serviceaccount-signing-key/v1",
		"values": map[string]string{
			"privateKey": base64.StdEncoding.EncodeToString(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})),
			"keyID":      base64.StdEncoding.EncodeToString([]byte("key-1")),
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv(provider.EnvVariable, string(record))

	source, err := buildCredentialSource(context.Background(), &config.Config{
		Controller: config.ControllerConfig{
			ApiURI: "http://api.example.test/graphql",
			ServiceAccount: config.ServiceAccountConfig{
				Name:   "gitstore-controller-manager",
				UID:    "sa-uid-1",
				KeyRef: ref,
			},
			SecretProviders: config.SecretProvidersConfig{Bootstrap: provider},
		},
	}, zap.NewNop())
	if err != nil {
		t.Fatalf("buildCredentialSource() error: %v", err)
	}
	if _, ok := source.(*graphqlclient.ServiceAccountSource); !ok {
		t.Errorf("source = %T, want *graphqlclient.ServiceAccountSource", source)
	}
	if readiness := credentialReadiness(source); readiness == nil || readiness.Ready() {
		t.Error("dynamic source must begin not ready before acquiring a token")
	}
}

func TestBuildCredentialSourceRejectsMissingCredentialConfiguration(t *testing.T) {
	if _, err := buildCredentialSource(context.Background(), &config.Config{}, zap.NewNop()); err == nil {
		t.Fatal("buildCredentialSource() error = nil")
	}
}

func TestBuildCredentialSourceFailsClosedOnInvalidRecords(t *testing.T) {
	record := func(format string, values map[string]string) []byte {
		t.Helper()
		encoded := make(map[string]string, len(values))
		for name, value := range values {
			encoded[name] = base64.StdEncoding.EncodeToString([]byte(value))
		}
		data, err := json.Marshal(map[string]any{"format": format, "values": encoded})
		if err != nil {
			t.Fatal(err)
		}
		return data
	}
	for _, tc := range []struct {
		name string
		data []byte
		want error
	}{
		{"absent", nil, secretmaterial.ErrNotFound},
		{"missing-id", record("serviceaccount-signing-key/v1", map[string]string{"privateKey": "MUST-NOT-LEAK"}), secretmaterial.ErrMissingKey},
		{"malformed-key", record("serviceaccount-signing-key/v1", map[string]string{"privateKey": "MUST-NOT-LEAK", "keyID": "id"}), secretmaterial.ErrInvalidRef},
		{"oversized-key", record("serviceaccount-signing-key/v1", map[string]string{"privateKey": strings.Repeat("x", secretmaterial.MaxItemBytes+1), "keyID": "id"}), secretmaterial.ErrValueTooLarge},
		{"runtime-record", record("secret-record/v1", map[string]string{"privateKey": "MUST-NOT-LEAK", "keyID": "id"}), secretmaterial.ErrUnsupportedType},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			if tc.data != nil {
				if err := os.WriteFile(filepath.Join(root, "controller.json"), tc.data, 0600); err != nil {
					t.Fatal(err)
				}
			}
			cfg := &config.Config{Controller: config.ControllerConfig{
				ApiURI: "http://must-not-call.invalid",
				ServiceAccount: config.ServiceAccountConfig{
					Name: "manager", UID: "uid",
					KeyRef: secret.Ref{Kind: "SecretRef", Name: "controller"},
				},
				SecretProviders: config.SecretProvidersConfig{Bootstrap: secret.BootstrapProviderConfig{
					Type: "file", BasePath: root,
				}},
			}}
			source, err := buildCredentialSource(context.Background(), cfg, zap.NewNop())
			if source != nil || !errors.Is(err, tc.want) {
				t.Fatalf("invalid bootstrap produced a source or wrong classification: %v", err)
			}
			if strings.Contains(err.Error(), "MUST-NOT-LEAK") {
				t.Fatal("bootstrap diagnostic leaked private material")
			}
		})
	}
}

type repositoryRegistrationWatch struct {
	events chan listwatch.WatchEvent[repositorycontroller.Repository]
	once   sync.Once
}

func (w *repositoryRegistrationWatch) Events() <-chan listwatch.WatchEvent[repositorycontroller.Repository] {
	return w.events
}
func (w *repositoryRegistrationWatch) Err() error { return nil }
func (w *repositoryRegistrationWatch) Stop() {
	w.once.Do(func() { close(w.events) })
}

type repositoryRegistrationListWatcher struct {
	item repositorycontroller.Repository
}

func (w *repositoryRegistrationListWatcher) List(context.Context) (listwatch.ListResponse[repositorycontroller.Repository], error) {
	return listwatch.ListResponse[repositorycontroller.Repository]{
		Items:           []repositorycontroller.Repository{w.item},
		ResourceVersion: "rwv1:test:1",
	}, nil
}

func (w *repositoryRegistrationListWatcher) Watch(ctx context.Context, _ string) (listwatch.Watcher[repositorycontroller.Repository], error) {
	watch := &repositoryRegistrationWatch{events: make(chan listwatch.WatchEvent[repositorycontroller.Repository], 1)}
	watch.events <- listwatch.WatchEvent[repositorycontroller.Repository]{Type: listwatch.Bookmark, ResourceVersion: "rwv1:test:2"}
	go func() {
		<-ctx.Done()
		watch.Stop()
	}()
	return watch, nil
}

// TestRegisterRepositoryAcrossTwoControllerManagers exercises the production
// registration boundary twice: two independent managers, runners, caches,
// queues, and checkpoint stores consume the same Repository snapshot and call
// the shared idempotent API provisioning/status contracts. This is deliberately
// stronger than calling GraphQLStorageClient twice in isolation.
func TestRegisterRepositoryAcrossTwoControllerManagers(t *testing.T) {
	var provisionCalls atomic.Int64
	var statusCalls atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer controller-token" {
			http.Error(w, "missing controller authorization", http.StatusUnauthorized)
			return
		}
		var request struct {
			Query string `json:"query"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.Contains(request.Query, "provisionRepositoryStorage"):
			provisionCalls.Add(1)
			_, _ = w.Write([]byte(`{"data":{"provisionRepositoryStorage":{"repository":{"metadata":{"namespace":"acme","name":"catalog"}}}}}`))
		case strings.Contains(request.Query, "updateRepositoryStatus"):
			statusCalls.Add(1)
			_, _ = w.Write([]byte(`{"data":{"updateRepositoryStatus":{"repository":{"metadata":{"resourceVersion":"2"}}}}}`))
		default:
			http.Error(w, "unexpected GraphQL operation", http.StatusBadRequest)
		}
	}))
	defer server.Close()

	item := repositorycontroller.Repository{
		UID: "repository-1", Namespace: "acme", Name: "catalog", StorageClass: "standard",
		Generation: 1, ResourceVersion: "1",
		Status: status.ResourceStatus{Conditions: []*status.Condition{{
			Type: "AdmissionAccepted", Status: "TRUE", ObservedGeneration: 1,
		}}},
	}
	cfg := &config.Config{Controller: config.ControllerConfig{
		Reconcile:  config.ReconcileConfig{MaxAttempts: 1, StallThreshold: time.Minute},
		Checkpoint: config.CheckpointConfig{FlushIntervalEvents: 1},
		Watch:      config.WatchConfig{MaxBackoff: time.Millisecond},
	}}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 2)
	// runners is waited on before the test returns so each Runner's final
	// checkpoint flush finishes before t.TempDir cleanup removes its directory.
	var runners sync.WaitGroup
	var checkpointRoots []string
	for replica := 0; replica < 2; replica++ {
		mgr := manager.New().WithLogger(zap.NewNop())
		root := t.TempDir()
		checkpointRoots = append(checkpointRoots, root)
		store, err := checkpoint.NewFilesystemStore(root)
		if err != nil {
			t.Fatalf("replica %d checkpoint store: %v", replica, err)
		}
		client := graphqlclient.New(server.URL, graphqlclient.NewStaticToken("controller-token"))
		if _, err := registerRepository(
			ctx, &runners, mgr, store, cfg, zap.NewNop(), client,
			&repositoryRegistrationListWatcher{item: item},
			repositorycontroller.NewGraphQLStorageClient(client),
		); err != nil {
			t.Fatalf("replica %d register Repository: %v", replica, err)
		}
		if stat := mgr.KindStats()["Repository"]; !stat.Registered {
			t.Fatalf("replica %d Repository registration missing", replica)
		}
		go func() { done <- mgr.Start(ctx) }()
	}

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) && (provisionCalls.Load() < 2 || statusCalls.Load() < 2) {
		time.Sleep(10 * time.Millisecond)
	}
	if got := provisionCalls.Load(); got != 2 {
		t.Fatalf("provision calls = %d, want one from each registered controller manager", got)
	}
	if got := statusCalls.Load(); got != 2 {
		t.Fatalf("status calls = %d, want one from each registered controller manager", got)
	}

	cancel()
	for replica := 0; replica < 2; replica++ {
		select {
		case err := <-done:
			if err != nil {
				t.Fatalf("replica %d manager shutdown: %v", replica, err)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("replica %d manager did not stop", replica)
		}
	}
	runners.Wait()
	for _, root := range checkpointRoots {
		replacement, err := checkpoint.NewFilesystemStore(root)
		if err != nil {
			t.Fatal(err)
		}
		rec, err := replacement.Load(context.Background(), "Repository")
		if err != nil {
			t.Fatalf("final checkpoint not available to replacement: %v", err)
		}
		var restored []repositorycontroller.Repository
		if err := json.Unmarshal(rec.Snapshot, &restored); err != nil {
			t.Fatal(err)
		}
		if rec.ResourceVersion != "rwv1:test:2" || len(restored) != 1 || restored[0].UID != item.UID {
			t.Fatal("shutdown flush did not retain the replacement snapshot and cursor")
		}
	}
}
