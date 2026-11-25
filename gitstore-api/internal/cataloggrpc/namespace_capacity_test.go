// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package cataloggrpc

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	catalogv1 "github.com/gitstore-dev/gitstore/api/gen/gitstore/catalog/v1"
	"github.com/gitstore-dev/gitstore/api/internal/admission"
	"github.com/gitstore-dev/gitstore/api/internal/auth"
	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	"github.com/gitstore-dev/gitstore/api/internal/testutil"
	"github.com/gitstore-dev/gitstore/api/internal/validate"
	"github.com/gitstore-dev/gitstore/translation"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/protobuf/proto"
)

const (
	namespaceCapacityFilesPerRequest      = 500
	namespaceCapacityManifestsPerRequest  = 50
	namespaceCapacityRequestsPerSecond    = 10
	namespaceCapacityConcurrency          = 20
	namespaceCapacityMinimumDuration      = 30 * time.Minute
	namespaceCapacityP95Limit             = 100 * time.Millisecond
	namespaceCapacityP99Limit             = 250 * time.Millisecond
	namespaceCapacityInternalErrorLimit   = 0.001
	namespaceCapacityRecoveryLimit        = 30 * time.Second
	namespaceCapacityRecoveryWindow       = 5 * time.Second
	namespaceCapacityCPUPercentLimit      = 80.0
	namespaceCapacityMemoryGrowthLimit    = 10.0
	namespaceCapacityGoroutineDriftLimit  = 5.0
	namespaceCapacityHelperEnvironmentKey = "GITSTORE_NAMESPACE_CAPACITY_HELPER"
	namespaceCapacityRepositoryID         = "00000000-0000-0000-0000-000000000001"
	namespaceCapacityServiceSecret        = "owned-capacity-helper-fixture"
)

type namespaceCapacityReplicaStats struct {
	CPUSeconds float64 `json:"cpuSeconds"`
	HeapAlloc  uint64  `json:"heapAlloc"`
	Goroutines int     `json:"goroutines"`
}

type namespaceCapacityReplica struct {
	name       string
	process    *exec.Cmd
	client     catalogv1.CatalogServiceClient
	connection *grpc.ClientConn
	controlURL string
}

type namespaceCapacitySegment struct {
	name       string
	started    time.Time
	startStats namespaceCapacityReplicaStats
	ended      time.Time
	endStats   namespaceCapacityReplicaStats
}

type namespaceCapacityLoadConfig struct {
	RequestsPerSecond int
	Workers           int
	QueueCapacity     int
	RequestCount      int
}

type namespaceCapacityRequestSample struct {
	Started           time.Time
	Completed         time.Time
	ReplicaName       string
	InternalError     bool
	IncorrectDecision bool
	Corpus            string
}

type namespaceCapacityRecorder struct {
	mu      sync.Mutex
	samples []namespaceCapacityRequestSample
}

type namespaceCapacityReplicaPool struct {
	mu       sync.RWMutex
	replicas []*namespaceCapacityReplica
}

type namespaceCapacityLoadResult struct {
	Scheduled int
	Ended     time.Time
}

func TestNamespaceCapacityThresholds(t *testing.T) {
	assert.Equal(t, 500, namespaceCapacityFilesPerRequest)
	assert.Equal(t, 50, namespaceCapacityManifestsPerRequest)
	assert.Equal(t, 10, namespaceCapacityRequestsPerSecond)
	assert.Equal(t, 20, namespaceCapacityConcurrency)
	assert.Equal(t, 30*time.Minute, namespaceCapacityMinimumDuration)
	assert.Equal(t, 100*time.Millisecond, namespaceCapacityP95Limit)
	assert.Equal(t, 250*time.Millisecond, namespaceCapacityP99Limit)
	assert.Equal(t, 0.001, namespaceCapacityInternalErrorLimit)
	assert.Equal(t, 30*time.Second, namespaceCapacityRecoveryLimit)
	assert.Equal(t, 80.0, namespaceCapacityCPUPercentLimit)
	assert.Equal(t, 10.0, namespaceCapacityMemoryGrowthLimit)
	assert.Equal(t, 5.0, namespaceCapacityGoroutineDriftLimit)
}

func TestNamespaceCapacityRequestFixtures(t *testing.T) {
	server := newNamespaceCapacityServer(t)
	valid, invalid := namespaceCapacityRequests()
	for _, tc := range []struct {
		name     string
		request  *catalogv1.ValidateResourcesRequest
		accepted bool
	}{
		{"valid", valid, true},
		{"invalid", invalid, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			require.Len(t, tc.request.Trees, 1)
			require.Len(t, tc.request.Trees[0].ProposedBlobs, namespaceCapacityFilesPerRequest)
			ctx, request, err := namespaceCapacitySignedRequest(t.Context(), tc.request)
			require.NoError(t, err)
			response, err := server.ValidateResources(ctx, request)
			require.NoError(t, err)
			assert.Equal(t, tc.accepted, response.Accepted)
			if !tc.accepted {
				assert.NotEmpty(t, response.Errors)
			}
		})
	}
}

func TestNamespaceCapacityTranslationCorpus(t *testing.T) {
	server := newNamespaceCapacityServer(t)
	for _, entry := range namespaceCapacityCases(t) {
		t.Run(entry.name, func(t *testing.T) {
			blobs := entry.request.Trees[0].ProposedBlobs
			require.Len(t, blobs, namespaceCapacityFilesPerRequest)
			namespaces, translations := 0, 0
			for _, blob := range blobs {
				if strings.HasPrefix(blob.Path, "namespaces/") {
					namespaces++
				}
				if strings.HasPrefix(blob.Path, "translations/") {
					translations++
				}
				if entry.nearLimit && blob.Path == entry.diagnosticPath {
					require.Len(t, blob.Content, 1<<20)
				}
			}
			require.Equal(t, namespaceCapacityManifestsPerRequest, namespaces)
			require.Positive(t, translations)
			ctx, request, err := namespaceCapacitySignedRequest(t.Context(), entry.request)
			require.NoError(t, err)
			response, err := server.ValidateResources(ctx, request)
			require.NoError(t, err)
			require.Equal(t, entry.accepted, response.Accepted, "%v", response.Errors)
			if !entry.accepted {
				require.NotEmpty(t, response.Errors)
				require.Equal(t, entry.diagnosticPath, response.Errors[0].FilePath)
			}
			for _, blob := range blobs {
				if !strings.HasPrefix(blob.Path, "translations/") {
					continue
				}
				principal := &auth.Principal{Subject: "capacity-validator", AuthMethod: "static-users", Groups: []string{"system:authenticated"}}
				diagnostics, err := server.ValidateManifest(auth.ContextWithPrincipal(t.Context(), principal), admission.ManifestValidationRequest{
					RepositoryID: entry.request.RepositoryId, Path: blob.Path, NewContent: blob.Content,
				})
				require.NoError(t, err)
				require.Equal(t, entry.accepted || blob.Path != entry.diagnosticPath, len(diagnostics) == 0,
					"standalone/full preflight parity for %s: %v", blob.Path, diagnostics)
			}
		})
	}
}

func TestNamespaceCapacityTranslationCorpusAcrossHelperProcesses(t *testing.T) {
	cases := namespaceCapacityCases(t)
	for _, name := range []string{"corpus-helper-a", "corpus-helper-b"} {
		replica := startNamespaceCapacityReplica(t, name)
		t.Cleanup(func() { stopNamespaceCapacityReplica(replica) })
		for _, entry := range cases {
			ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
			response, err := replica.validate(ctx, entry.request)
			cancel()
			require.NoError(t, err)
			require.Equal(t, entry.accepted, response.Accepted, "%s/%s: %v", name, entry.name, response.Errors)
		}
	}
}

func TestNamespaceCapacityMixedCorpusScheduling(t *testing.T) {
	cases := namespaceCapacityCases(t)
	client := namespaceCapacityScheduleClient{cases: cases}
	replicas := []*namespaceCapacityReplica{
		{name: "schedule-a", client: client}, {name: "schedule-b", client: client},
	}
	recorder := &namespaceCapacityRecorder{}
	config := namespaceCapacityLoadConfigFor(time.Second)
	result := runNamespaceCapacityLoad(t.Context(), time.Now(), &namespaceCapacityReplicaPool{replicas: replicas}, cases, config, recorder)
	require.Equal(t, config.RequestCount, result.Scheduled)
	coverage := map[string]int{}
	for _, sample := range recorder.snapshot() {
		require.False(t, sample.InternalError, "%s/%s", sample.ReplicaName, sample.Corpus)
		require.False(t, sample.IncorrectDecision, "%s/%s", sample.ReplicaName, sample.Corpus)
		coverage[sample.Corpus]++
	}
	for _, entry := range cases {
		require.Equal(t, 2, coverage[entry.name], "the existing offered driver must exercise every corpus class")
	}
}

type namespaceCapacityScheduleClient struct {
	catalogv1.CatalogServiceClient
	cases []namespaceCapacityCase
}

func (client namespaceCapacityScheduleClient) ValidateResources(_ context.Context, request *catalogv1.ValidateResourcesRequest, _ ...grpc.CallOption) (*catalogv1.ValidateResourcesResponse, error) {
	for _, entry := range client.cases {
		if proto.Equal(&catalogv1.ResourceValidationTree{ProposedBlobs: request.Trees[0].ProposedBlobs},
			&catalogv1.ResourceValidationTree{ProposedBlobs: entry.request.Trees[0].ProposedBlobs}) {
			response := &catalogv1.ValidateResourcesResponse{Accepted: entry.accepted}
			if !entry.accepted {
				response.Errors = []*catalogv1.ValidationError{{FilePath: entry.diagnosticPath}}
			}
			return response, nil
		}
	}
	return nil, fmt.Errorf("unrecognized scheduled corpus")
}

func TestNamespaceCapacityProcessCPUExcludesIdleRuntimeCapacity(t *testing.T) {
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		t.Skip("process CPU measurement is supported on Darwin and Linux")
	}
	previous := runtime.GOMAXPROCS(4)
	t.Cleanup(func() {
		runtime.GOMAXPROCS(previous)
	})

	started := time.Now()
	start := currentNamespaceCapacityStats()
	time.Sleep(100 * time.Millisecond)
	end := currentNamespaceCapacityStats()
	elapsed := time.Since(started).Seconds()

	cpuSeconds := end.CPUSeconds - start.CPUSeconds
	require.GreaterOrEqual(t, cpuSeconds, 0.0)
	assert.Less(t, cpuSeconds, elapsed*2,
		"process CPU must not include idle capacity from every GOMAXPROCS slot")
}

func TestNamespaceCapacityRecoveryRequiresSustainedUnderLoadTraffic(t *testing.T) {
	activated := time.Unix(1_700_000_000, 0)
	readinessOnly := []namespaceCapacityRequestSample{{
		Started:     activated,
		Completed:   activated.Add(10 * time.Millisecond),
		ReplicaName: "replica-1-replacement",
	}}
	_, recovered := namespaceCapacityRecovery(readinessOnly, activated)
	assert.False(t, recovered, "one readiness request is not load recovery")

	samples := make([]namespaceCapacityRequestSample, 0, 61)
	for sequence := 0; sequence <= 60; sequence++ {
		started := activated.Add(time.Duration(sequence) * time.Second / namespaceCapacityRequestsPerSecond)
		samples = append(samples, namespaceCapacityRequestSample{
			Started:     started,
			Completed:   started.Add(25 * time.Millisecond),
			ReplicaName: []string{"replica-1-replacement", "replica-2"}[sequence%2],
		})
	}
	recovery, recovered := namespaceCapacityRecovery(samples, activated)
	require.True(t, recovered)
	assert.LessOrEqual(t, recovery, namespaceCapacityRecoveryLimit)

	samples[25].InternalError = true
	_, recovered = namespaceCapacityRecovery(samples, activated)
	assert.False(t, recovered, "the recovery window must enforce the exact internal-error threshold")
}

func TestNamespaceCapacityLoadConfigurationUsesActiveWorkers(t *testing.T) {
	config := namespaceCapacityLoadConfigFor(namespaceCapacityMinimumDuration)
	assert.Equal(t, namespaceCapacityRequestsPerSecond, config.RequestsPerSecond)
	assert.Equal(t, namespaceCapacityConcurrency, config.Workers)
	assert.Equal(t, namespaceCapacityConcurrency, config.QueueCapacity)
	assert.Equal(t,
		int(namespaceCapacityMinimumDuration.Seconds())*namespaceCapacityRequestsPerSecond,
		config.RequestCount)

	jobs := make(chan int)
	workersReady, waitForWorkers := startNamespaceCapacityWorkers(config.Workers, jobs, func(int) {})
	for worker := 0; worker < config.Workers; worker++ {
		select {
		case <-workersReady:
		case <-time.After(time.Second):
			t.Fatalf("worker %d of %d did not become active", worker+1, config.Workers)
		}
	}
	close(jobs)
	waitForWorkers()
}

func TestNamespaceValidationCapacity(t *testing.T) {
	if os.Getenv("GITSTORE_NAMESPACE_CAPACITY_RUN") != "1" {
		t.Skip("set GITSTORE_NAMESPACE_CAPACITY_RUN=1 or run make capacity TARGET=namespace PROFILE=validation MODE=production")
	}
	duration := namespaceCapacityDuration(t)
	if duration < namespaceCapacityMinimumDuration {
		t.Fatalf("GITSTORE_NAMESPACE_CAPACITY_DURATION=%s, want at least %s", duration, namespaceCapacityMinimumDuration)
	}

	cases := namespaceCapacityCases(t)
	validRequest := cases[0].request
	replicas := []*namespaceCapacityReplica{
		startNamespaceCapacityReplica(t, "replica-1"),
		startNamespaceCapacityReplica(t, "replica-2"),
	}
	pool := &namespaceCapacityReplicaPool{replicas: replicas}
	t.Cleanup(func() {
		for _, replica := range replicas {
			stopNamespaceCapacityReplica(replica)
		}
	})
	for _, replica := range replicas {
		warmNamespaceCapacityReplica(t, replica, validRequest)
	}

	segments := []namespaceCapacitySegment{
		{name: "replica-1", started: time.Now(), startStats: readNamespaceCapacityStats(t, replicas[0], true)},
		{name: "replica-2", started: time.Now(), startStats: readNamespaceCapacityStats(t, replicas[1], true)},
	}

	config := namespaceCapacityLoadConfigFor(duration)
	recorder := &namespaceCapacityRecorder{}
	loadContext, cancelLoad := context.WithCancel(context.Background())
	t.Cleanup(cancelLoad)
	started := time.Now()
	loadDone := make(chan namespaceCapacityLoadResult, 1)
	go func() {
		loadDone <- runNamespaceCapacityLoad(
			loadContext,
			started,
			pool,
			cases,
			config,
			recorder,
		)
	}()

	replacementTimer := time.NewTimer(duration / 2)
	defer replacementTimer.Stop()
	select {
	case result := <-loadDone:
		t.Fatalf("capacity load ended before replica replacement: scheduled=%d", result.Scheduled)
	case <-replacementTimer.C:
	}

	replacement := startNamespaceCapacityReplica(t, "replica-1-replacement")
	replicas = append(replicas, replacement)
	warmNamespaceCapacityReplica(t, replacement, validRequest)
	replacementStartStats := readNamespaceCapacityStats(t, replacement, true)
	replacementActivated := time.Now()
	oldReplica := pool.replace(0, replacement)
	segments = append(segments, namespaceCapacitySegment{
		name: replacement.name, started: replacementActivated, startStats: replacementStartStats,
	})

	replacementRecovery := waitForNamespaceCapacityRecovery(
		t,
		loadDone,
		recorder,
		replacementActivated,
	)
	segments[0].ended = time.Now()
	segments[0].endStats = readNamespaceCapacityStats(t, oldReplica, true)
	stopNamespaceCapacityReplica(oldReplica)

	loadResult := <-loadDone
	loadEnded := loadResult.Ended
	samples := recorder.snapshot()
	segments[1].ended = loadEnded
	segments[1].endStats = readNamespaceCapacityStats(t, replicas[1], true)
	segments[len(segments)-1].ended = loadEnded
	segments[len(segments)-1].endStats = readNamespaceCapacityStats(t, replacement, true)

	require.Equal(t, config.RequestCount, loadResult.Scheduled)
	require.Len(t, samples, config.RequestCount)
	latencies := make([]time.Duration, 0, len(samples))
	internalErrors := 0
	incorrectDecisions := 0
	for _, sample := range samples {
		latencies = append(latencies, sample.Completed.Sub(sample.Started))
		if sample.InternalError {
			internalErrors++
		}
		if sample.IncorrectDecision {
			incorrectDecisions++
		}
	}
	require.NotEmpty(t, latencies)
	sort.Slice(latencies, func(i, j int) bool { return latencies[i] < latencies[j] })
	p95 := namespaceCapacityPercentile(latencies, 0.95)
	p99 := namespaceCapacityPercentile(latencies, 0.99)
	errorRate := float64(internalErrors) / float64(len(samples))
	throughput := float64(len(samples)) / duration.Seconds()

	assert.LessOrEqual(t, p95, namespaceCapacityP95Limit)
	assert.LessOrEqual(t, p99, namespaceCapacityP99Limit)
	assert.Less(t, errorRate, namespaceCapacityInternalErrorLimit)
	assert.Zero(t, incorrectDecisions)
	assert.LessOrEqual(t, replacementRecovery, namespaceCapacityRecoveryLimit)
	assert.GreaterOrEqual(t, throughput, float64(namespaceCapacityRequestsPerSecond))
	for _, segment := range segments {
		assertNamespaceCapacityReplicaThresholds(t, segment)
	}
	for _, entry := range cases {
		var values []time.Duration
		for _, sample := range samples {
			if sample.Corpus == entry.name {
				values = append(values, sample.Completed.Sub(sample.Started))
			}
		}
		require.NotEmpty(t, values, "every corpus class must be exercised during the full window")
		sort.Slice(values, func(i, j int) bool { return values[i] < values[j] })
		t.Logf("namespace validation corpus=%s requests=%d p95=%s p99=%s",
			entry.name, len(values), namespaceCapacityPercentile(values, .95), namespaceCapacityPercentile(values, .99))
	}
	t.Logf(
		"namespace capacity passed: files=%d namespace_manifests=%d requests=%d duration=%s concurrency=%d throughput=%.2f p95=%s p99=%s internal_error_rate=%.5f recovery=%s",
		namespaceCapacityFilesPerRequest,
		namespaceCapacityManifestsPerRequest,
		len(samples),
		loadEnded.Sub(started),
		namespaceCapacityConcurrency,
		throughput,
		p95,
		p99,
		errorRate,
		replacementRecovery,
	)
}

func TestNamespaceCapacityReplicaProcess(t *testing.T) {
	if os.Getenv(namespaceCapacityHelperEnvironmentKey) != "1" {
		t.Skip("capacity helper process")
	}
	grpcListener, err := net.Listen("tcp", os.Getenv("GITSTORE_NAMESPACE_CAPACITY_GRPC_ADDR"))
	require.NoError(t, err)
	controlListener, err := net.Listen("tcp", os.Getenv("GITSTORE_NAMESPACE_CAPACITY_CONTROL_ADDR"))
	require.NoError(t, err)

	server := newNamespaceCapacityServer(t)
	grpcServer := grpc.NewServer()
	catalogv1.RegisterCatalogServiceServer(grpcServer, server)
	go func() {
		if serveErr := grpcServer.Serve(grpcListener); serveErr != nil {
			panic(serveErr)
		}
	}()

	control := http.NewServeMux()
	control.HandleFunc("/stats", func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Query().Get("gc") == "1" {
			runtime.GC()
		}
		writer.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(writer).Encode(currentNamespaceCapacityStats())
	})
	go func() {
		if serveErr := http.Serve(controlListener, control); serveErr != nil {
			panic(serveErr)
		}
	}()
	select {}
}

func namespaceCapacityDuration(t *testing.T) time.Duration {
	t.Helper()
	raw := strings.TrimSpace(os.Getenv("GITSTORE_NAMESPACE_CAPACITY_DURATION"))
	if raw == "" {
		return namespaceCapacityMinimumDuration
	}
	duration, err := time.ParseDuration(raw)
	require.NoError(t, err)
	return duration
}

func namespaceCapacityLoadConfigFor(duration time.Duration) namespaceCapacityLoadConfig {
	return namespaceCapacityLoadConfig{
		RequestsPerSecond: namespaceCapacityRequestsPerSecond,
		Workers:           namespaceCapacityConcurrency,
		QueueCapacity:     namespaceCapacityConcurrency,
		RequestCount:      int(duration.Seconds()) * namespaceCapacityRequestsPerSecond,
	}
}

func runNamespaceCapacityLoad(
	ctx context.Context,
	started time.Time,
	pool *namespaceCapacityReplicaPool,
	cases []namespaceCapacityCase,
	config namespaceCapacityLoadConfig,
	recorder *namespaceCapacityRecorder,
) namespaceCapacityLoadResult {
	jobs := make(chan int, config.QueueCapacity)
	workersReady, waitForWorkers := startNamespaceCapacityWorkers(config.Workers, jobs, func(sequence int) {
		replica := pool.replicaFor(sequence)
		entry := cases[sequence%len(cases)]

		requestContext, cancel := context.WithTimeout(ctx, namespaceCapacityP99Limit*4)
		requestStarted := time.Now()
		response, err := replica.validate(requestContext, entry.request)
		requestCompleted := time.Now()
		cancel()

		sample := namespaceCapacityRequestSample{
			Started:       requestStarted,
			Completed:     requestCompleted,
			ReplicaName:   replica.name,
			InternalError: err != nil,
			Corpus:        entry.name,
		}
		if err == nil {
			sample.IncorrectDecision = response.GetAccepted() != entry.accepted
			if !entry.accepted {
				found := false
				for _, finding := range response.Errors {
					found = found || finding.FilePath == entry.diagnosticPath
				}
				sample.IncorrectDecision = sample.IncorrectDecision || !found
			}
		}
		recorder.record(sample)
	})
	for worker := 0; worker < config.Workers; worker++ {
		<-workersReady
	}

	scheduled := 0
	for sequence := 0; sequence < config.RequestCount; sequence++ {
		target := started.Add(time.Duration(sequence) * time.Second / time.Duration(config.RequestsPerSecond))
		if !waitForNamespaceCapacityTarget(ctx, target) {
			break
		}
		select {
		case jobs <- sequence:
			scheduled++
		case <-ctx.Done():
			close(jobs)
			waitForWorkers()
			return namespaceCapacityLoadResult{Scheduled: scheduled, Ended: time.Now()}
		}
	}
	close(jobs)
	waitForWorkers()
	return namespaceCapacityLoadResult{Scheduled: scheduled, Ended: time.Now()}
}

func startNamespaceCapacityWorkers(
	workerCount int,
	jobs <-chan int,
	execute func(int),
) (<-chan struct{}, func()) {
	workersReady := make(chan struct{}, workerCount)
	var workers sync.WaitGroup
	for worker := 0; worker < workerCount; worker++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			workersReady <- struct{}{}
			for sequence := range jobs {
				execute(sequence)
			}
		}()
	}
	return workersReady, workers.Wait
}

func waitForNamespaceCapacityTarget(ctx context.Context, target time.Time) bool {
	delay := time.Until(target)
	if delay <= 0 {
		return true
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-timer.C:
		return true
	case <-ctx.Done():
		return false
	}
}

func (p *namespaceCapacityReplicaPool) replicaFor(sequence int) *namespaceCapacityReplica {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.replicas[sequence%len(p.replicas)]
}

func (p *namespaceCapacityReplicaPool) replace(index int, replacement *namespaceCapacityReplica) *namespaceCapacityReplica {
	p.mu.Lock()
	defer p.mu.Unlock()
	previous := p.replicas[index]
	p.replicas[index] = replacement
	return previous
}

func (r *namespaceCapacityRecorder) record(sample namespaceCapacityRequestSample) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.samples = append(r.samples, sample)
}

func (r *namespaceCapacityRecorder) snapshot() []namespaceCapacityRequestSample {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]namespaceCapacityRequestSample(nil), r.samples...)
}

func namespaceCapacityRecovery(
	samples []namespaceCapacityRequestSample,
	activated time.Time,
) (time.Duration, bool) {
	recoveryDeadline := activated.Add(namespaceCapacityRecoveryLimit)
	sorted := make([]namespaceCapacityRequestSample, 0, len(samples))
	for _, sample := range samples {
		if !sample.Started.Before(activated) && !sample.Completed.After(recoveryDeadline) {
			sorted = append(sorted, sample)
		}
	}
	sort.Slice(sorted, func(i, j int) bool {
		return sorted[i].Completed.Before(sorted[j].Completed)
	})
	windowStartIndex := 0
	internalErrors := 0
	incorrectDecisions := 0
	replacementSuccesses := 0
	for candidateIndex, candidate := range sorted {
		if candidate.InternalError {
			internalErrors++
		}
		if candidate.IncorrectDecision {
			incorrectDecisions++
		}
		if candidate.ReplicaName == "replica-1-replacement" &&
			!candidate.InternalError && !candidate.IncorrectDecision {
			replacementSuccesses++
		}
		windowStarted := candidate.Completed.Add(-namespaceCapacityRecoveryWindow)
		for windowStartIndex <= candidateIndex &&
			sorted[windowStartIndex].Completed.Before(windowStarted) {
			expired := sorted[windowStartIndex]
			if expired.InternalError {
				internalErrors--
			}
			if expired.IncorrectDecision {
				incorrectDecisions--
			}
			if expired.ReplicaName == "replica-1-replacement" &&
				!expired.InternalError && !expired.IncorrectDecision {
				replacementSuccesses--
			}
			windowStartIndex++
		}
		if candidate.Completed.Before(activated.Add(namespaceCapacityRecoveryWindow)) {
			continue
		}
		completed := candidateIndex - windowStartIndex + 1
		if completed == 0 || replacementSuccesses == 0 || incorrectDecisions != 0 {
			continue
		}
		throughput := float64(completed) / namespaceCapacityRecoveryWindow.Seconds()
		errorRate := float64(internalErrors) / float64(completed)
		if throughput >= float64(namespaceCapacityRequestsPerSecond) &&
			errorRate < namespaceCapacityInternalErrorLimit {
			return candidate.Completed.Sub(activated), true
		}
	}
	return 0, false
}

func waitForNamespaceCapacityRecovery(
	t *testing.T,
	loadDone <-chan namespaceCapacityLoadResult,
	recorder *namespaceCapacityRecorder,
	activated time.Time,
) time.Duration {
	t.Helper()
	deadline := time.NewTimer(namespaceCapacityRecoveryLimit)
	defer deadline.Stop()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		if recovery, ok := namespaceCapacityRecovery(recorder.snapshot(), activated); ok {
			return recovery
		}
		select {
		case result := <-loadDone:
			t.Fatalf("capacity load ended before replacement recovered: scheduled=%d", result.Scheduled)
		case <-deadline.C:
			if recovery, ok := namespaceCapacityRecovery(recorder.snapshot(), activated); ok {
				return recovery
			}
			t.Fatalf("replacement throughput/error rate did not recover under load within %s", namespaceCapacityRecoveryLimit)
		case <-ticker.C:
		}
	}
}

type namespaceCapacityCase struct {
	name, diagnosticPath string
	request              *catalogv1.ValidateResourcesRequest
	accepted, nearLimit  bool
}

func namespaceCapacityCases(t *testing.T) []namespaceCapacityCase {
	t.Helper()
	raw, err := os.ReadFile("testdata/translation/corpus.json")
	require.NoError(t, err)
	var corpus struct{ Valid map[string]string }
	require.NoError(t, json.Unmarshal(raw, &corpus))
	keys := make([]string, 0, len(corpus.Valid))
	for key := range corpus.Valid {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	valid, invalid := namespaceCapacityRequests()
	require.LessOrEqual(t, len(keys), namespaceCapacityFilesPerRequest-namespaceCapacityManifestsPerRequest)
	offset := namespaceCapacityFilesPerRequest - len(keys)
	for i, key := range keys {
		source := strings.ReplaceAll(corpus.Valid[key], "acme-store", "gitstore-system")
		parsed, _, err := validate.NewParser().ParseResource(strings.NewReader(source))
		require.NoError(t, err)
		require.NotNil(t, parsed.Translation)
		name := fmt.Sprintf("capacity-translation-%02d", i)
		ending := "\n"
		if strings.Contains(source, "\r\n") {
			ending = "\r\n"
		}
		old := "  name: " + parsed.Translation.Metadata.Name + ending
		require.Contains(t, source, old)
		source = strings.Replace(source, old, "  name: "+name+ending, 1)
		blob := &catalogv1.ResourceBlob{Path: "translations/" + name + ".md", Content: []byte(source)}
		valid.Trees[0].ProposedBlobs[offset+i] = blob
		invalid.Trees[0].ProposedBlobs[offset+i] = blob
	}
	cases := []namespaceCapacityCase{
		{name: "normal", request: valid, accepted: true},
		{name: "namespace-negative", request: invalid, diagnosticPath: "namespaces/invalid.md"},
	}
	for _, entry := range []struct {
		name, source string
		nearLimit    bool
	}{
		{"translation-locale-negative", strings.Replace(corpus.Valid["product-field"], "  locale:", "  locale: invalid_ #", 1), false},
		{"translation-field-negative", strings.Replace(corpus.Valid["product-field"], "spec.title", "spec.unsupported", 1), false},
		{"translation-near-limit", corpus.Valid["product-combined"], true},
	} {
		request := proto.Clone(valid).(*catalogv1.ValidateResourcesRequest)
		source := strings.ReplaceAll(entry.source, "acme-store", "gitstore-system")
		if entry.nearLimit {
			source += strings.Repeat("x", translation.MaxDocumentBytes-len(source))
		}
		path := "translations/" + entry.name + ".md"
		request.Trees[0].ProposedBlobs[offset] = &catalogv1.ResourceBlob{Path: path, Content: []byte(source)}
		cases = append(cases, namespaceCapacityCase{
			name: entry.name, diagnosticPath: path, request: request,
			accepted: entry.nearLimit, nearLimit: entry.nearLimit,
		})
	}
	return cases
}

func newNamespaceCapacityServer(t *testing.T) *Server {
	t.Helper()
	store := testutil.NewNamespacePolicyDatastore(t, namespaceCapacityRepositoryID, "00000000-0000-0000-0000-000000000002")
	namespace, err := store.GetNamespaceByName(t.Context(), "gitstore-system")
	require.NoError(t, err)
	audit := store.(datastore.TranslationTargetAuditStore)
	var progress datastore.TranslationAuditProgress
	for range 64 {
		progress, err = audit.RepairTranslationTargetAuditTurn(t.Context(), namespace.UID, 256)
		require.NoError(t, err)
		if progress.Phase == "Verified" {
			break
		}
	}
	require.Equal(t, "Verified", progress.Phase, "missing-target facts require an actual completed coverage audit")
	server, err := NewServer(ServerDeps{
		Store: store, Logger: zap.NewNop(), ServiceSecret: namespaceCapacityServiceSecret,
		Registry: auth.NewProviderRegistry(nil, testutil.NewAllowAllAuthZ(), nil),
	})
	require.NoError(t, err)
	// Only the same-package helper exercises dormant validation, not activation.
	server.translationActivation.Enabled = true
	return server
}

func namespaceCapacitySignedRequest(ctx context.Context, request *catalogv1.ValidateResourcesRequest) (context.Context, *catalogv1.ValidateResourcesRequest, error) {
	request = proto.Clone(request).(*catalogv1.ValidateResourcesRequest)
	refs := make([]*catalogv1.ReceiveRef, 0, len(request.Trees))
	for _, tree := range request.Trees {
		if tree.Ref == nil {
			tree.Ref = &catalogv1.ReceiveRef{
				RefName: "refs/heads/capacity", OldCommitSha: strings.Repeat("0", 40), NewCommitSha: strings.Repeat("a", 40),
			}
		}
		refs = append(refs, tree.Ref)
	}
	request.Authorization = &catalogv1.CallbackAuthorization{
		RepositoryId: request.RepositoryId, ReceiveId: "owned-capacity-validation", Refs: refs,
		Actor: &catalogv1.CallbackActor{Subject: "capacity-validator", AuthMethod: "static-users", Groups: []string{"system:authenticated"}},
	}
	signature, err := auth.SignRPC(namespaceCapacityServiceSecret, "gitstore-api", catalogv1.CatalogService_ValidateResources_FullMethodName, request)
	if err != nil {
		return ctx, nil, err
	}
	headers := metadata.Pairs(auth.RPCSignatureMetadata, signature)
	return metadata.NewIncomingContext(metadata.NewOutgoingContext(ctx, headers), headers), request, nil
}

func (replica *namespaceCapacityReplica) validate(ctx context.Context, request *catalogv1.ValidateResourcesRequest) (*catalogv1.ValidateResourcesResponse, error) {
	ctx, request, err := namespaceCapacitySignedRequest(ctx, request)
	if err != nil {
		return nil, err
	}
	return replica.client.ValidateResources(ctx, request)
}

func namespaceCapacityBlob(path, name string) *catalogv1.ResourceBlob {
	return &catalogv1.ResourceBlob{Path: path, Content: []byte(fmt.Sprintf("---\napiVersion: gitstore.dev/v1beta1\nkind: Namespace\nmetadata:\n  name: %s\nspec:\n  title: %s\n  tier: ORGANIZATION\n---\n", name, name))}
}

func namespaceCapacityRequests() (*catalogv1.ValidateResourcesRequest, *catalogv1.ValidateResourcesRequest) {
	validBlobs := make([]*catalogv1.ResourceBlob, 0, namespaceCapacityFilesPerRequest)
	for i := 0; i < namespaceCapacityManifestsPerRequest; i++ {
		name := fmt.Sprintf("capacity-ns-%02d", i)
		validBlobs = append(validBlobs, namespaceCapacityBlob("namespaces/"+name+".md", name))
	}
	for i := namespaceCapacityManifestsPerRequest; i < namespaceCapacityFilesPerRequest; i++ {
		name := fmt.Sprintf("capacity-product-%03d", i)
		validBlobs = append(validBlobs, &catalogv1.ResourceBlob{
			Path: "products/" + name + ".md",
			Content: []byte(fmt.Sprintf(`---
apiVersion: catalog.gitstore.dev/v1beta1
kind: Product
metadata:
  name: %s
  namespace: gitstore-system
spec:
  title: Capacity Product %d
---
`, name, i)),
		})
	}
	invalidBlobs := append([]*catalogv1.ResourceBlob(nil), validBlobs...)
	invalidBlobs[0] = namespaceCapacityBlob("namespaces/invalid.md", "Invalid Name")
	return &catalogv1.ValidateResourcesRequest{
		RepositoryId: namespaceCapacityRepositoryID,
		Trees:        []*catalogv1.ResourceValidationTree{{ProposedBlobs: validBlobs}},
	}, &catalogv1.ValidateResourcesRequest{
		RepositoryId: namespaceCapacityRepositoryID,
		Trees:        []*catalogv1.ResourceValidationTree{{ProposedBlobs: invalidBlobs}},
	}
}

func startNamespaceCapacityReplica(t *testing.T, name string) *namespaceCapacityReplica {
	t.Helper()
	grpcAddress := reserveNamespaceCapacityAddress(t)
	controlAddress := reserveNamespaceCapacityAddress(t)
	executable, err := os.Executable()
	require.NoError(t, err)
	command := exec.Command(executable, "-test.run=^TestNamespaceCapacityReplicaProcess$", "-test.timeout=0")
	command.Env = append(os.Environ(),
		namespaceCapacityHelperEnvironmentKey+"=1",
		"GITSTORE_NAMESPACE_CAPACITY_GRPC_ADDR="+grpcAddress,
		"GITSTORE_NAMESPACE_CAPACITY_CONTROL_ADDR="+controlAddress,
	)
	command.Stdout = io.Discard
	command.Stderr = io.Discard
	require.NoError(t, command.Start())

	waitForNamespaceCapacityEndpoint(t, grpcAddress, command)
	waitForNamespaceCapacityEndpoint(t, controlAddress, command)
	connection, err := grpc.NewClient(grpcAddress, grpc.WithTransportCredentials(insecure.NewCredentials()))
	require.NoError(t, err)
	return &namespaceCapacityReplica{
		name: name, process: command, client: catalogv1.NewCatalogServiceClient(connection),
		connection: connection, controlURL: "http://" + controlAddress,
	}
}

func reserveNamespaceCapacityAddress(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	address := listener.Addr().String()
	require.NoError(t, listener.Close())
	return address
}

func waitForNamespaceCapacityEndpoint(t *testing.T, address string, command *exec.Cmd) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		connection, err := net.DialTimeout("tcp", address, 100*time.Millisecond)
		if err == nil {
			_ = connection.Close()
			return
		}
		if command.ProcessState != nil && command.ProcessState.Exited() {
			t.Fatalf("capacity replica exited before listening on %s", address)
		}
		time.Sleep(25 * time.Millisecond)
	}
	stopNamespaceCapacityReplica(&namespaceCapacityReplica{process: command})
	t.Fatalf("capacity replica did not listen on %s", address)
}

func stopNamespaceCapacityReplica(replica *namespaceCapacityReplica) {
	if replica == nil {
		return
	}
	if replica.connection != nil {
		_ = replica.connection.Close()
	}
	if replica.process == nil || replica.process.Process == nil || replica.process.ProcessState != nil {
		return
	}
	_ = replica.process.Process.Signal(os.Interrupt)
	waited := make(chan struct{})
	go func() {
		_ = replica.process.Wait()
		close(waited)
	}()
	select {
	case <-waited:
	case <-time.After(2 * time.Second):
		_ = replica.process.Process.Kill()
		<-waited
	}
}

func warmNamespaceCapacityReplica(
	t *testing.T,
	replica *namespaceCapacityReplica,
	request *catalogv1.ValidateResourcesRequest,
) {
	t.Helper()
	for i := 0; i < namespaceCapacityConcurrency; i++ {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		response, err := replica.validate(ctx, request)
		cancel()
		require.NoError(t, err)
		require.True(t, response.GetAccepted())
	}
}

func readNamespaceCapacityStats(
	t *testing.T,
	replica *namespaceCapacityReplica,
	collectGarbage bool,
) namespaceCapacityReplicaStats {
	t.Helper()
	url := replica.controlURL + "/stats"
	if collectGarbage {
		url += "?gc=1"
	}
	client := &http.Client{Timeout: 5 * time.Second}
	response, err := client.Get(url)
	require.NoError(t, err)
	defer response.Body.Close()
	require.Equal(t, http.StatusOK, response.StatusCode)
	var stats namespaceCapacityReplicaStats
	require.NoError(t, json.NewDecoder(response.Body).Decode(&stats))
	return stats
}

func currentNamespaceCapacityStats() namespaceCapacityReplicaStats {
	var memory runtime.MemStats
	runtime.ReadMemStats(&memory)
	return namespaceCapacityReplicaStats{
		CPUSeconds: namespaceCapacityProcessCPUSeconds(),
		HeapAlloc:  memory.HeapAlloc,
		Goroutines: runtime.NumGoroutine(),
	}
}

func namespaceCapacityPercentile(values []time.Duration, percentile float64) time.Duration {
	index := int(math.Ceil(float64(len(values))*percentile)) - 1
	if index < 0 {
		index = 0
	}
	if index >= len(values) {
		index = len(values) - 1
	}
	return values[index]
}

func assertNamespaceCapacityReplicaThresholds(t *testing.T, segment namespaceCapacitySegment) {
	t.Helper()
	elapsed := segment.ended.Sub(segment.started).Seconds()
	require.Greater(t, elapsed, 0.0)
	cpuPercent := (segment.endStats.CPUSeconds - segment.startStats.CPUSeconds) / elapsed * 100
	memoryGrowth := percentageGrowth(segment.startStats.HeapAlloc, segment.endStats.HeapAlloc)
	goroutineDrift := percentageDrift(segment.startStats.Goroutines, segment.endStats.Goroutines)
	assert.Less(t, cpuPercent, namespaceCapacityCPUPercentLimit, "%s CPU", segment.name)
	assert.Less(t, memoryGrowth, namespaceCapacityMemoryGrowthLimit, "%s retained memory", segment.name)
	assert.LessOrEqual(t, goroutineDrift, namespaceCapacityGoroutineDriftLimit, "%s goroutines", segment.name)
	t.Logf(
		"%s resources: cpu=%.2f%% retained_memory_growth=%.2f%% goroutine_drift=%.2f%%",
		segment.name, cpuPercent, memoryGrowth, goroutineDrift,
	)
}

func percentageGrowth(start, end uint64) float64 {
	if start == 0 || end <= start {
		return 0
	}
	return float64(end-start) / float64(start) * 100
}

func percentageDrift(start, end int) float64 {
	if start == 0 {
		return 0
	}
	return math.Abs(float64(end-start)) / float64(start) * 100
}
