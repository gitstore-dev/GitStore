// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package integration

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"math/bits"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/gitstore-dev/gitstore/secretmaterial"
	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/require"
)

const (
	repositoryCapacitySequenceAnnotation  = "capacity.gitstore.dev/sequence"
	repositoryCapacitySentAtAnnotation    = "capacity.gitstore.dev/sent-at"
	repositoryCapacityPaddingAnnotation   = "capacity.gitstore.dev/padding"
	repositoryCapacityMinimumOverflowWait = 31 * time.Second
)

type repositoryCapacityConfig struct {
	apiA, apiB                  string
	overflowAPI                 string
	controllerA, controllerB    string
	replacement, trigger, token string
	namespace                   string
	duration                    time.Duration
	replacementDelay            time.Duration
	subscribers                 int
	replayEvents                int
	replaySamples               int
	resourcePool                int
	overflowTransitions         int
	overflowBackpressureWait    time.Duration
	mutationWorkers             int
	burstSize                   int
	burstInterval               time.Duration
	transitionInterval          time.Duration
	baselineStabilization       time.Duration
	postLoadStabilization       time.Duration
	mode                        capacityMode
	skipReplacement             bool
	workload                    string
}

type repositoryCapacityEvent struct {
	Type            string `json:"type"`
	Name            string `json:"name"`
	ResourceVersion string `json:"resourceVersion"`
	Repository      *struct {
		Metadata struct {
			Annotations     map[string]any `json:"annotations"`
			Generation      int64          `json:"generation"`
			ResourceVersion string         `json:"resourceVersion"`
			Revision        string         `json:"revision"`
		} `json:"metadata"`
	} `json:"repository"`
}

type repositoryCapacitySubscriber struct {
	endpoint string
	cursor   string
	seen     *capacityBitset
	latency  []time.Duration
	mu       sync.Mutex
}

type repositoryCapacityLoadResult struct {
	produced, admitted, failed, backpressured                      int64
	acknowledged                                                   *capacityBitset
	maxSequence                                                    int
	attempts, retries                                              int64
	missedSchedules                                                int64
	attemptErrors                                                  map[string]int64
	drainTimedOut                                                  bool
	queueLatencies, lockLatencies, requestLatencies, httpLatencies []time.Duration
}

type repositoryCapacityAttemptObservation struct {
	attempts, retries int64
	errors            map[string]int64
	httpLatencies     []time.Duration
}

func runRepositoryLifecycleCapacity(t *testing.T) {
	t.Helper()
	require.NoError(t, validateSecretCapacityAvailability(os.Getenv("REPOSITORY_CAPACITY_SECRET_SCENARIO")))
	if os.Getenv("REPOSITORY_LIFECYCLE_CAPACITY_RUN") != "1" {
		t.Skip("run through make capacity TARGET=repository PROFILE=lifecycle MODE=alpha or MODE=production against a deployed two-API/two-controller stack")
	}
	cfg := loadRepositoryCapacityConfig(t)
	require.NoError(t, validateRepositoryCapacityScale(cfg))

	client := &http.Client{Timeout: 20 * time.Second}
	require.NotEqual(t, cfg.apiA, cfg.apiB, "API endpoints must identify distinct replicas")
	require.NotEqual(t, cfg.controllerA, cfg.controllerB, "controller endpoints must identify distinct replicas")
	require.True(t, endpointReady(client, cfg.apiA), "API A must be healthy and ready")
	require.True(t, endpointReady(client, cfg.apiB), "API B must be healthy and ready")
	require.True(t, endpointReady(client, cfg.overflowAPI), "overflow API must be healthy and ready")
	identityA := repositoryCapacityAPIIdentity(t, client, cfg.apiA)
	identityB := repositoryCapacityAPIIdentity(t, client, cfg.apiB)
	require.NotEqual(t, identityA, identityB, "API endpoints resolve to the same process instance")
	controllerBeforeA := requireRepositoryController(t, client, cfg.controllerA)
	controllerBeforeB := requireRepositoryController(t, client, cfg.controllerB)
	require.NotEqual(t,
		repositoryControllerIdentity(t, client, cfg.controllerA),
		repositoryControllerIdentity(t, client, cfg.controllerB),
		"controller endpoints resolve to the same process instance",
	)
	if cfg.workload != "" {
		runRepositoryWorkloadDiagnostic(t, client, cfg)
		return
	}

	var dataset secretCapacityDatasetObservations
	var secretFaultDriver *secretCapacityFaultDriver
	if secretCapacityRequested() {
		secretFaultDriver = prepareSecretCapacityFaultDriver(t, cfg)
		dataset = prepareSecretCapacityDataset(t, cfg)
		waitForSecretCapacityControllerRecovery(t, client, cfg)
	}
	ensureRepositoryCapacityNamespace(t, cfg)
	runID := strconv.FormatInt(time.Now().UnixNano(), 36)
	poolPrefix := "rcp-" + runID + "-"
	createRepositoryCapacityPool(t, client, cfg, poolPrefix)
	assertRepositoryCrossReplicaRead(t, cfg, poolPrefix+"1")
	assertRepositoryCapacityDeletionParity(t, client, cfg, "rcd-"+runID)
	var fileWorkload *secretCapacityFileWorkload
	if secretCapacityRequested() {
		fileWorkload = prepareSecretCapacityFileWorkload(t, cfg, poolPrefix, runID)
		fileWorkload.dataset = dataset
		secretFaultDriver.fixture.RuntimeMarkers = append([]string{}, fileWorkload.runtime.markers...)
		require.NoError(t, writeSecretCapacityPrivateJSON(filepath.Join(secretFaultDriver.root, "owned-fixture.json"), secretFaultDriver.fixture))
	}

	metricsStart := map[string]capacityProcessMetrics{
		"api_a": mustRepositoryCapacityMetrics(t, client, cfg.apiA),
		"api_b": mustRepositoryCapacityMetrics(t, client, cfg.apiB),
	}
	baselineStarted := time.Now()
	metricsStart = stabilizeRepositoryCapacityBaseline(t, client, cfg, metricsStart)
	baselineElapsed := time.Since(baselineStarted)

	replayCursor := repositoryCapacityBootstrapCursor(t, cfg.apiA, cfg)
	replayPrefix := "rcr-" + runID + "-"
	replaySequences := make([]int, cfg.replayEvents)
	for i := range replaySequences {
		replaySequences[i] = i + 1
	}
	replayAck := runRepositoryCapacityBatch(t, client, cfg, poolPrefix, replayPrefix, replaySequences, "")
	require.Equal(t, cfg.replayEvents, replayAck.count(), "every replay mutation must be acknowledged")
	replayDurations := make([]time.Duration, 0, cfg.replaySamples)
	for sample := 0; sample < cfg.replaySamples; sample++ {
		endpoint := cfg.apiA
		if sample%2 == 1 {
			endpoint = cfg.apiB
		}
		started := time.Now()
		seen, err := repositoryCapacityReplay(endpoint, cfg, replayCursor, replayPrefix, cfg.replayEvents, 5*time.Minute)
		require.NoError(t, err)
		require.Equal(t, cfg.replayEvents, seen.count(), "replay omitted acknowledged Repository transitions")
		replayDurations = append(replayDurations, time.Since(started))
	}
	replayP95 := percentileDuration(replayDurations, .95)
	if cfg.mode != capacityModeDiagnostic {
		require.LessOrEqual(t, replayP95, 5*time.Second, "10,000-event Repository replay p95 must be <=5s")
	}

	livePrefix := "rcl-" + runID + "-"
	maxTransitions := int(cfg.duration/cfg.transitionInterval) +
		(int(cfg.duration/cfg.burstInterval)+2)*cfg.burstSize + 128
	subscribers := openRepositoryCapacitySubscribers(t, cfg, maxTransitions)
	readCtx, cancelReaders := context.WithCancel(context.Background())
	readerErrors := make(chan error, len(subscribers))
	var readers sync.WaitGroup
	for _, subscriber := range subscribers {
		readers.Add(1)
		go func(state *repositoryCapacitySubscriber) {
			defer readers.Done()
			if err := state.read(readCtx, cfg, livePrefix); err != nil && readCtx.Err() == nil {
				readerErrors <- err
			}
		}(subscriber)
	}

	var secretControllersBefore [2]secretCapacityControllerSample
	var secretResourcesBefore, secretResourcesLoadEnd []secretCapacityResourceSnapshot
	var secretFaultOutcome secretCapacityFaultObservations
	var secretLogs *secretCapacityLogs
	var secretControllersAfter [2]secretCapacityControllerSample
	if fileWorkload != nil {
		secretControllersBefore = recordSecretCapacityControllers(t, client, cfg, "controllers-before-load.json")
		secretResourcesBefore = captureSecretCapacityResources(t, cfg, "before")
		secretLogs = &secretCapacityLogs{streams: make(map[string]secretCapacityLogStream), directory: filepath.Join(os.Getenv("CAPACITY_EVIDENCE_DIR"), "secret")}
		t.Cleanup(func() { require.NoError(t, secretLogs.stop()) })
		require.NoError(t, secretLogs.capture(t.Context(), secretResourcesBefore))
	}
	soakStarted := time.Now()
	var fileLoad <-chan secretCapacityFileLoadResult
	var secretFaults <-chan secretCapacityFaultResult
	if fileWorkload != nil {
		fileLoad = startSecretCapacityFileLoad(t, fileWorkload, cfg.duration, cfg.mode)
		secretFaults = startSecretCapacityFaults(t, secretFaultDriver, soakStarted)
	}
	replacementDone := make(chan capacityRecoveryResult, 1)
	if cfg.skipReplacement {
		replacementDone <- capacityRecoveryResult{}
	} else {
		go func() { replacementDone <- runRepositoryCapacityReplacement(cfg, client, soakStarted) }()
	}
	load := runRepositoryCapacityLoad(t, client, cfg, poolPrefix, livePrefix, maxTransitions)
	if fileLoad != nil {
		result := <-fileLoad
		assertSecretCapacityFileLoad(t, result)
		faults := <-secretFaults
		require.NoError(t, faults.err)
		require.True(t, faults.observations.Completed, "all owned scheduled faults must finish")
		secretFaultOutcome = faults.observations
		controllersAfter := recordSecretCapacityControllers(t, client, cfg, "controllers-after-load.json")
		secretControllersAfter = controllersAfter
		for i := range controllersAfter {
			progress, err := secretCapacityControllerProgress(secretControllersBefore[i], controllersAfter[i], i == 0)
			require.NoError(t, err)
			require.True(t, progress, "each controller must freshly authenticate and reconcile during load")
		}
		secretResourcesLoadEnd = captureSecretCapacityResources(t, cfg, "load-end")
		require.NoError(t, secretLogs.capture(t.Context(), secretResourcesLoadEnd))
	}
	require.Zero(t, load.failed, "Repository mutations failed under load")
	require.Zero(t, load.backpressured, "bounded mutation queue shed load")
	require.Zero(t, load.missedSchedules, "Repository offered schedule fell behind")
	require.False(t, load.drainTimedOut, "Repository backlog did not drain within its bounded deadline")
	require.Equal(t, load.produced, load.admitted, "all offered Repository transitions must be admitted")
	recovery := <-replacementDone
	require.NoError(t, recovery.err)
	waitRepositoryCapacitySubscribers(t, subscribers, load.acknowledged, 60*time.Second)
	cancelReaders()
	readers.Wait()
	close(readerErrors)
	for err := range readerErrors {
		require.NoError(t, err)
	}

	latencies := make([]time.Duration, 0)
	for i, subscriber := range subscribers {
		if i >= 16 {
			break
		}
		subscriber.mu.Lock()
		latencies = append(latencies, subscriber.latency...)
		subscriber.mu.Unlock()
	}
	require.NotEmpty(t, latencies)
	sort.Slice(latencies, func(i, j int) bool { return latencies[i] < latencies[j] })
	assertCapacityVisibility(t, cfg.mode, percentileDuration(latencies, .95), percentileDuration(latencies, .99))

	if cfg.overflowTransitions > 0 {
		runRepositoryCapacityOverflow(t, client, cfg, poolPrefix, "rco-"+runID+"-")
	}

	assertRepositoryCrossReplicaRead(t, cfg, poolPrefix+"1")
	waitForRepositoryControllerAdvance(t, client, cfg.controllerA, controllerBeforeA)
	waitForRepositoryControllerAdvance(t, client, cfg.controllerB, controllerBeforeB)
	require.Empty(t, repositoryControllerPoison(t, client, cfg.controllerA))
	require.Empty(t, repositoryControllerPoison(t, client, cfg.controllerB))
	metricsEnd := map[string]capacityProcessMetrics{
		"api_a": mustRepositoryCapacityMetrics(t, client, cfg.apiA),
		"api_b": mustRepositoryCapacityMetrics(t, client, cfg.apiB),
	}
	metricsElapsed := time.Since(soakStarted)
	postLoadStarted := time.Now()
	metricsEnd = stabilizeRepositoryCapacityMemory(t, client, cfg, metricsEnd)
	postLoadElapsed := time.Since(postLoadStarted)
	assertCapacityMetricsWithLimits(t, metricsStart, metricsEnd, metricsElapsed, recovery, false, repositoryCapacityMemoryLimits(cfg.mode))
	if fileWorkload != nil {
		stabilized := captureSecretCapacityResources(t, cfg, "stabilized")
		topologyCtx, cancelTopology := context.WithTimeout(t.Context(), 30*time.Second)
		topologyErr := validateSecretCapacityOwnedMounts(topologyCtx, secretFaultDriver.root, secretFaultDriver.fixture.Project)
		cancelTopology()
		require.NoError(t, topologyErr)
		require.NoError(t, secretLogs.stop())
		proof, err := secretCapacityResourceProofs(secretResourcesBefore, secretResourcesLoadEnd, stabilized, soakStarted, recovery, secretFaultOutcome)
		require.NoError(t, err)
		for _, process := range proof {
			require.Less(t, process.Cpu, .8)
			require.Less(t, float64(process.RssAfter)/float64(process.RssBefore), 1.1)
			if process.Role != "git" {
				require.LessOrEqual(t, float64(process.GoroutinesAfter)/float64(process.GoroutinesBefore), 1.1)
			}
		}
		require.NoError(t, writeSecretCapacityComponent(os.Getenv("CAPACITY_EVIDENCE_DIR"), "resources.json", secretCapacityResourceObservations{
			SchemaVersion: 1, Component: "secret-resources/v1", RunID: os.Getenv("CAPACITY_RUN_ID"), Mode: cfg.mode,
			Processes: proof, Before: secretResourcesBefore, LoadEnd: secretResourcesLoadEnd, Stabilized: stabilized,
			Baseline: baselineElapsed, PostLoad: postLoadElapsed, FilePool: len(fileWorkload.ids), Workers: len(fileWorkload.workers),
			ControllersBefore: secretControllersBefore[:], ControllersAfter: secretControllersAfter[:], RepositoryLifecyclePassed: true,
		}))
	}
	t.Logf("Repository lifecycle capacity passed: mode=%s replay=%d replay_p95=%s subscribers=%d admitted=%d duration=%s", cfg.mode, cfg.replayEvents, replayP95, cfg.subscribers, load.admitted, time.Since(soakStarted))
}

func validateSecretCapacityAvailability(value string) error {
	switch value {
	case "", "0", "1":
		return nil
	default:
		return errors.New("REPOSITORY_CAPACITY_SECRET_SCENARIO must be 0 or 1")
	}
}

func validateRepositoryCapacityWorkload(workload string, mode capacityMode, secretScenario bool) error {
	if workload == "" {
		return nil
	}
	if mode != capacityModeDiagnostic || secretScenario {
		return errors.New("REPOSITORY_CAPACITY_WORKLOAD comparisons require diagnostic mode and no full secret scenario")
	}
	switch workload {
	case "repository", "file", "combined":
		return nil
	default:
		return errors.New("REPOSITORY_CAPACITY_WORKLOAD must be repository, file or combined")
	}
}

func TestRepositoryCapacityWorkloadSelection(t *testing.T) {
	for _, mode := range []capacityMode{capacityModeDiagnostic, capacityModeAlpha, capacityModeProduction} {
		require.NoError(t, validateRepositoryCapacityWorkload("", mode, false))
		require.NoError(t, validateRepositoryCapacityWorkload("", mode, true))
		for _, workload := range []string{"repository", "file", "combined"} {
			if mode == capacityModeDiagnostic {
				require.NoError(t, validateRepositoryCapacityWorkload(workload, mode, false))
			} else {
				require.Error(t, validateRepositoryCapacityWorkload(workload, mode, false))
			}
			require.Error(t, validateRepositoryCapacityWorkload(workload, mode, true))
		}
	}
	require.Error(t, validateRepositoryCapacityWorkload("unknown", capacityModeDiagnostic, false))
}

func runRepositoryWorkloadDiagnostic(t *testing.T, client *http.Client, cfg repositoryCapacityConfig) {
	t.Helper()
	root, fixtureRunID, err := secretCapacityFixturePath()
	require.NoError(t, err)
	fixture, err := readSecretCapacityOwnedFixture(root, fixtureRunID)
	require.NoError(t, err)
	require.NoError(t, validateSecretCapacityOwnedMounts(t.Context(), root, fixture.Project))
	lockPath := filepath.Join(root, "active-run")
	lock, err := os.OpenFile(lockPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	require.NoError(t, err, "refuse concurrent diagnostics/faults against the owned fixture")
	require.NoError(t, lock.Close())
	t.Cleanup(func() { require.NoError(t, os.Remove(lockPath)) })
	for i, endpoint := range []string{cfg.apiA, cfg.apiB, cfg.controllerA, cfg.controllerB} {
		service, port := []string{"api-a", "api-b", "controller-manager-a", "controller-manager-b"}[i], "4000"
		if i >= 2 {
			port = "5001"
		}
		container, err := secretCapacityServiceContainer(t.Context(), fixture.Project, service)
		require.NoError(t, err)
		body, err := secretCapacityOwnedMetrics(t.Context(), container, port)
		require.NoError(t, err)
		internal, err := parseCapacityMetrics(body)
		require.NoError(t, err)
		external := mustRepositoryCapacityMetrics(t, client, endpoint)
		require.NotEmpty(t, internal.instanceID)
		require.Equal(t, internal.instanceID, external.instanceID, "diagnostic endpoint must identify the owned service")
	}
	waitForSecretCapacityControllerRecovery(t, client, cfg)
	ensureRepositoryCapacityNamespace(t, cfg)
	runID := strconv.FormatInt(time.Now().UnixNano(), 36)
	poolPrefix := "rcp-" + runID + "-"
	createRepositoryCapacityPool(t, client, cfg, poolPrefix)
	// Seed identical File/status work even in Repository-only comparisons.
	workload := prepareSecretCapacityFileWorkload(t, cfg, poolPrefix, runID)
	waitForSecretCapacityControllerRecovery(t, client, cfg)
	metrics := repositoryCapacityMetricSamples(t, client, cfg)
	_ = stabilizeRepositoryCapacityBaseline(t, client, cfg, metrics)
	before := captureSecretCapacityResources(t, cfg, "before")
	profiles, err := startRepositoryCapacityProfiles(t.Context(), fixture.Project, cfg.mode, cfg.duration)
	require.NoError(t, err)
	var fileLoad <-chan secretCapacityFileLoadResult
	started := time.Now()
	if cfg.workload != "repository" {
		fileLoad = startSecretCapacityFileLoad(t, workload, cfg.duration, cfg.mode)
	}
	var repositoryLoad repositoryCapacityLoadResult
	if cfg.workload != "file" {
		maxTransitions := int(cfg.duration/cfg.transitionInterval) +
			(int(cfg.duration/cfg.burstInterval)+2)*cfg.burstSize + 128
		repositoryLoad = runRepositoryCapacityLoad(t, client, cfg, poolPrefix, "rcl-"+runID+"-", maxTransitions)
	}
	var fileResult secretCapacityFileLoadResult
	if fileLoad != nil {
		fileResult = <-fileLoad
	}
	end := captureSecretCapacityResources(t, cfg, "load-end")
	require.NoError(t, <-profiles, "live profiling failed; diagnostic evidence is incomplete")
	post := repositoryCapacityMetricSamples(t, client, cfg)
	_ = stabilizeRepositoryCapacityMemory(t, client, cfg, post)
	stabilized := captureSecretCapacityResources(t, cfg, "stabilized")
	latency := func(values []time.Duration) secretCapacityLatency {
		if len(values) == 0 {
			return secretCapacityLatency{}
		}
		return secretCapacityLatency{int64(len(values)), percentileDuration(values, .95), percentileDuration(values, .99)}
	}
	if repositoryLoad.attemptErrors == nil {
		repositoryLoad.attemptErrors = map[string]int64{}
	}
	require.NoError(t, writeSecretCapacityComponent(os.Getenv("CAPACITY_EVIDENCE_DIR"), "workload-comparison.json", struct {
		SchemaVersion             int                              `json:"schemaVersion"`
		Component                 string                           `json:"component"`
		RunID                     string                           `json:"runID"`
		Mode                      capacityMode                     `json:"mode"`
		Workload                  string                           `json:"workload"`
		FixturePrefix             string                           `json:"fixturePrefix"`
		Repositories              int                              `json:"repositories"`
		Files                     int                              `json:"files"`
		Duration                  time.Duration                    `json:"duration"`
		Elapsed                   time.Duration                    `json:"elapsed"`
		RepositoryOffered         int64                            `json:"repositoryOffered"`
		RepositoryAcknowledged    int64                            `json:"repositoryAcknowledged"`
		RepositoryFailed          int64                            `json:"repositoryFailed"`
		RepositoryDropped         int64                            `json:"repositoryDropped"`
		RepositoryMissedSchedules int64                            `json:"repositoryMissedSchedules"`
		RepositoryHTTPAttempts    int64                            `json:"repositoryHTTPAttempts"`
		RepositoryHTTPRetries     int64                            `json:"repositoryHTTPRetries"`
		RepositoryDrainTimedOut   bool                             `json:"repositoryDrainTimedOut"`
		RepositoryAttemptErrors   map[string]int64                 `json:"repositoryAttemptErrors"`
		RepositoryStages          map[string]secretCapacityLatency `json:"repositoryStages"`
		Before                    []secretCapacityResourceSnapshot `json:"before"`
		LoadEnd                   []secretCapacityResourceSnapshot `json:"loadEnd"`
		Stabilized                []secretCapacityResourceSnapshot `json:"stabilized"`
	}{
		SchemaVersion: 1, Component: "repository-workload-comparison/v1", RunID: fixtureRunID,
		Mode: cfg.mode, Workload: cfg.workload, FixturePrefix: poolPrefix,
		Repositories: cfg.resourcePool, Files: len(workload.ids), Duration: cfg.duration, Elapsed: time.Since(started),
		RepositoryOffered: repositoryLoad.produced, RepositoryAcknowledged: repositoryLoad.admitted,
		RepositoryFailed: repositoryLoad.failed, RepositoryDropped: repositoryLoad.backpressured,
		RepositoryMissedSchedules: repositoryLoad.missedSchedules,
		RepositoryHTTPAttempts:    repositoryLoad.attempts, RepositoryHTTPRetries: repositoryLoad.retries,
		RepositoryDrainTimedOut: repositoryLoad.drainTimedOut,
		RepositoryAttemptErrors: repositoryLoad.attemptErrors,
		RepositoryStages: map[string]secretCapacityLatency{
			"queue_wait": latency(repositoryLoad.queueLatencies), "resource_lock": latency(repositoryLoad.lockLatencies),
			"mutation": latency(repositoryLoad.requestLatencies), "http_attempt": latency(repositoryLoad.httpLatencies),
		},
		Before: before, LoadEnd: end, Stabilized: stabilized,
	}))
	require.NoError(t, validateSecretCapacityOwnedMounts(t.Context(), root, fixture.Project))
	if fileLoad != nil {
		assertSecretCapacityFileLoad(t, fileResult)
	}
	if cfg.workload != "file" {
		require.Positive(t, repositoryLoad.produced)
		require.Zero(t, repositoryLoad.missedSchedules)
		require.False(t, repositoryLoad.drainTimedOut)
		require.Zero(t, repositoryLoad.failed)
		require.Zero(t, repositoryLoad.backpressured)
		require.Equal(t, repositoryLoad.produced, repositoryLoad.admitted)
	}
}

func startRepositoryCapacityProfiles(ctx context.Context, project string, mode capacityMode, duration time.Duration) (<-chan error, error) {
	done := make(chan error, 1)
	flag := os.Getenv("CAPACITY_PROFILING")
	if flag == "" || flag == "none" {
		done <- nil
		return done, nil
	}
	if flag != "go" || mode != capacityModeDiagnostic {
		return nil, errors.New("CAPACITY_PROFILING=go is supported only for owned diagnostic workloads")
	}
	seconds := 30
	if raw := os.Getenv("CAPACITY_PROFILE_SECONDS"); raw != "" {
		value, err := strconv.Atoi(raw)
		if err != nil || value < 1 || value > 60 {
			return nil, errors.New("CAPACITY_PROFILE_SECONDS must be 1..60")
		}
		seconds = value
	}
	if duration < time.Duration(seconds+10)*time.Second {
		return nil, errors.New("offered load must exceed the full profiling capture window")
	}
	port := os.Getenv("CAPACITY_API_PROFILE_PORT")
	if port == "" {
		port = "6060"
	}
	value, err := strconv.Atoi(port)
	if err != nil || value < 1 || value > 65535 {
		return nil, errors.New("CAPACITY_API_PROFILE_PORT must be a valid port")
	}
	root := os.Getenv("CAPACITY_EVIDENCE_DIR")
	if root == "" {
		return nil, errors.New("profiling requires an evidence directory")
	}
	directory := filepath.Join(root, "profiles")
	if err := os.MkdirAll(directory, 0700); err != nil {
		return nil, err
	}
	var containers [2]string
	for i, service := range []string{"api-a", "api-b"} {
		container, err := secretCapacityServiceContainer(ctx, project, service)
		if err != nil {
			return nil, err
		}
		containers[i] = container
	}
	go func() {
		captureCtx, cancel := context.WithTimeout(ctx, time.Duration(seconds+30)*time.Second)
		defer cancel()
		results := make(chan error, 2)
		for i, container := range containers {
			go func(i int, container string) {
				for _, profile := range []struct{ name, path string }{
					{"cpu.pprof", "profile?seconds=" + strconv.Itoa(seconds)},
					{"heap.pprof", "heap"}, {"allocs.pprof", "allocs"},
					{"block.pprof", "block"}, {"mutex.pprof", "mutex"},
					{"goroutine.pprof", "goroutine"}, {"execution.trace", "trace?seconds=3"},
				} {
					body, err := secretCapacityOwnedCommandOutput(captureCtx, &secretCapacityGitOutput{limit: 32 << 20},
						"docker", "exec", container, "wget", "-qO-",
						"http://127.0.0.1:"+port+"/debug/pprof/"+profile.path)
					if err == nil && (len(body) < 16 ||
						(strings.HasSuffix(profile.name, ".pprof") && (body[0] != 0x1f || body[1] != 0x8b))) {
						err = errors.New("profile response is not a binary capture")
					}
					if err == nil {
						err = os.WriteFile(filepath.Join(directory, fmt.Sprintf("api-%c-%s", 'a'+i, profile.name)), body, 0600)
					}
					clear(body)
					if err != nil {
						results <- fmt.Errorf("capture api-%c %s: %w", 'a'+i, profile.name, err)
						return
					}
				}
				results <- nil
			}(i, container)
		}
		result := errors.Join(<-results, <-results)
		manifestErr := writeSecretCapacityComponent(root, "profiling.json", struct {
			SchemaVersion int    `json:"schemaVersion"`
			Kind          string `json:"kind"`
			Seconds       int    `json:"cpuSeconds"`
			Replicas      int    `json:"replicas"`
			MaximumBytes  int    `json:"maximumBytesPerCapture"`
			Passed        bool   `json:"passed"`
		}{1, "owned-go-live-diagnostic", seconds, 2, 32 << 20, result == nil})
		done <- errors.Join(result, manifestErr)
	}()
	return done, nil
}

func TestRepositoryCapacityProfileValidation(t *testing.T) {
	for _, test := range []struct {
		name, flag, seconds, port, evidence string
		mode                                capacityMode
		duration                            time.Duration
		expected                            string
	}{
		{"unsupported", "perf", "", "", "", capacityModeDiagnostic, time.Minute, "supported only"},
		{"alpha", "go", "", "", "", capacityModeAlpha, time.Minute, "supported only"},
		{"production", "go", "", "", "", capacityModeProduction, time.Minute, "supported only"},
		{"zero", "go", "0", "", "", capacityModeDiagnostic, time.Minute, "1..60"},
		{"excess", "go", "61", "", "", capacityModeDiagnostic, time.Minute, "1..60"},
		{"invalid", "go", "invalid", "", "", capacityModeDiagnostic, time.Minute, "1..60"},
		{"short", "go", "30", "", "", capacityModeDiagnostic, time.Second, "capture window"},
		{"port-zero", "go", "1", "0", "", capacityModeDiagnostic, time.Minute, "valid port"},
		{"port-excess", "go", "1", "65536", "", capacityModeDiagnostic, time.Minute, "valid port"},
		{"port-invalid", "go", "1", "invalid", "", capacityModeDiagnostic, time.Minute, "valid port"},
		{"no-evidence", "go", "1", "6060", "", capacityModeDiagnostic, time.Minute, "evidence directory"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv("CAPACITY_PROFILING", test.flag)
			t.Setenv("CAPACITY_PROFILE_SECONDS", test.seconds)
			t.Setenv("CAPACITY_API_PROFILE_PORT", test.port)
			t.Setenv("CAPACITY_EVIDENCE_DIR", test.evidence)
			done, err := startRepositoryCapacityProfiles(context.Background(), "must-not-access-docker", test.mode, test.duration)
			require.ErrorContains(t, err, test.expected)
			require.Nil(t, done)
		})
	}
	for _, flag := range []string{"", "none"} {
		t.Run("disabled-"+flag, func(t *testing.T) {
			t.Setenv("CAPACITY_PROFILING", flag)
			done, err := startRepositoryCapacityProfiles(context.Background(), "must-not-access-docker", capacityModeProduction, 0)
			require.NoError(t, err)
			require.NoError(t, <-done)
		})
	}
}

func TestSecretCapacityScenarioFlag(t *testing.T) {
	for _, value := range []string{"", "0", "1"} {
		require.NoError(t, validateSecretCapacityAvailability(value))
	}
	for _, value := range []string{"true", "2", "-1"} {
		require.Error(t, validateSecretCapacityAvailability(value))
	}
}

func repositoryCapacityMemoryLimits(mode capacityMode) capacityMemoryLimits {
	if mode == capacityModeProduction {
		return strictCapacityMemoryLimits
	}
	return capacityMemoryLimits{
		maxGrowthPercent: 75,
		maxResidentBytes: 256 * 1024 * 1024,
	}
}

func TestRepositoryCapacityMemoryLimits(t *testing.T) {
	t.Parallel()
	production := repositoryCapacityMemoryLimits(capacityModeProduction)
	require.Equal(t, float64(10), production.maxGrowthPercent)
	require.Zero(t, production.maxResidentBytes)
	require.True(t, capacityMemoryWithinLimits(9.99, 512*1024*1024, production))
	require.False(t, capacityMemoryWithinLimits(10, 128*1024*1024, production))

	for _, mode := range []capacityMode{capacityModeAlpha, capacityModeDiagnostic} {
		limits := repositoryCapacityMemoryLimits(mode)
		require.Equal(t, float64(75), limits.maxGrowthPercent)
		require.Equal(t, float64(256*1024*1024), limits.maxResidentBytes)
		require.True(t, capacityMemoryWithinLimits(74.99, 255*1024*1024, limits))
		require.False(t, capacityMemoryWithinLimits(75, 255*1024*1024, limits))
		require.False(t, capacityMemoryWithinLimits(50, 256*1024*1024, limits))
	}
}

func loadRepositoryCapacityConfig(t *testing.T) repositoryCapacityConfig {
	t.Helper()
	mode, err := parseCapacityMode(os.Getenv("MODE"))
	require.NoError(t, err)
	duration := capacityEnvDuration(t, "REPOSITORY_CAPACITY_DURATION", 60*time.Minute)
	delay := capacityEnvDuration(t, "REPOSITORY_CAPACITY_REPLACEMENT_DELAY", duration/2)
	return repositoryCapacityConfig{
		apiA: strings.TrimSuffix(os.Getenv("REPOSITORY_API_A"), "/"), apiB: strings.TrimSuffix(os.Getenv("REPOSITORY_API_B"), "/"),
		overflowAPI: strings.TrimSuffix(getEnv("REPOSITORY_OVERFLOW_API", os.Getenv("REPOSITORY_API_A")), "/"),
		controllerA: strings.TrimSuffix(os.Getenv("REPOSITORY_CONTROLLER_A"), "/"), controllerB: strings.TrimSuffix(os.Getenv("REPOSITORY_CONTROLLER_B"), "/"),
		replacement: strings.TrimSuffix(os.Getenv("REPOSITORY_API_REPLACEMENT"), "/"), trigger: os.Getenv("REPOSITORY_REPLACEMENT_TRIGGER_FILE"),
		token: repositoryCapacityToken(t), namespace: getEnv("REPOSITORY_CAPACITY_NAMESPACE", "repository-capacity"),
		duration: duration, replacementDelay: delay,
		subscribers: capacityEnvInt(t, "REPOSITORY_CAPACITY_SUBSCRIBERS", 1000), replayEvents: capacityEnvInt(t, "REPOSITORY_CAPACITY_REPLAY_EVENTS", 10000),
		replaySamples: capacityEnvInt(t, "REPOSITORY_CAPACITY_REPLAY_SAMPLES", 20), resourcePool: capacityEnvInt(t, "REPOSITORY_CAPACITY_RESOURCE_POOL", 50),
		overflowTransitions: capacityEnvInt(t, "REPOSITORY_CAPACITY_OVERFLOW_TRANSITIONS", 1000), mutationWorkers: capacityEnvInt(t, "REPOSITORY_CAPACITY_MUTATION_WORKERS", 20),
		burstSize: capacityEnvInt(t, "REPOSITORY_CAPACITY_BURST_SIZE", 100), burstInterval: capacityEnvDuration(t, "REPOSITORY_CAPACITY_BURST_INTERVAL", time.Minute),
		transitionInterval:       capacityEnvDuration(t, "REPOSITORY_CAPACITY_TRANSITION_INTERVAL", 100*time.Millisecond),
		overflowBackpressureWait: capacityEnvDuration(t, "REPOSITORY_CAPACITY_OVERFLOW_BACKPRESSURE_WAIT", repositoryCapacityMinimumOverflowWait),
		baselineStabilization:    capacityEnvDuration(t, "REPOSITORY_CAPACITY_BASELINE_STABILIZATION", 5*time.Minute),
		postLoadStabilization:    capacityEnvDuration(t, "REPOSITORY_CAPACITY_POST_LOAD_STABILIZATION", 10*time.Minute),
		mode:                     mode, skipReplacement: os.Getenv("REPOSITORY_CAPACITY_SKIP_REPLACEMENT") == "1",
		workload: os.Getenv("REPOSITORY_CAPACITY_WORKLOAD"),
	}
}

func stabilizeRepositoryCapacityBaseline(t *testing.T, client *http.Client, cfg repositoryCapacityConfig, result map[string]capacityProcessMetrics) map[string]capacityProcessMetrics {
	t.Helper()
	deadline := time.Now().Add(cfg.baselineStabilization)
	for time.Now().Before(deadline) {
		time.Sleep(min(time.Second, time.Until(deadline)))
		result = mergeCapacityBaselineSamples(result, repositoryCapacityMetricSamples(t, client, cfg))
	}
	return result
}

func stabilizeRepositoryCapacityMemory(t *testing.T, client *http.Client, cfg repositoryCapacityConfig, result map[string]capacityProcessMetrics) map[string]capacityProcessMetrics {
	t.Helper()
	deadline := time.Now().Add(cfg.postLoadStabilization)
	for time.Now().Before(deadline) {
		time.Sleep(min(time.Second, time.Until(deadline)))
		result = mergeCapacityMemorySamples(result, repositoryCapacityMetricSamples(t, client, cfg))
	}
	return result
}

func repositoryCapacityMetricSamples(t *testing.T, client *http.Client, cfg repositoryCapacityConfig) map[string]capacityProcessMetrics {
	t.Helper()
	result := make(map[string]capacityProcessMetrics, 2)
	for label, endpoint := range map[string]string{"api_a": cfg.apiA, "api_b": cfg.apiB} {
		metrics, err := fetchCapacityMetrics(client, endpoint)
		require.NoErrorf(t, err, "%s process metrics", label)
		result[label] = metrics
	}
	return result
}

func validateRepositoryCapacityScale(cfg repositoryCapacityConfig) error {
	if err := validateRepositoryCapacityWorkload(cfg.workload, cfg.mode, secretCapacityRequested()); err != nil {
		return err
	}
	if cfg.workload != "" && (!cfg.skipReplacement || cfg.resourcePool < 32 || cfg.resourcePool > 1000 || cfg.duration < time.Second || cfg.duration > time.Hour ||
		cfg.mutationWorkers < 1 || cfg.mutationWorkers > 32 || cfg.burstSize < 1 || cfg.burstSize > 100) {
		return errors.New("workload comparisons require explicit skipped replacement, 32..1000 repositories, duration 1s..1h and bounded workers/bursts")
	}
	for key, value := range map[string]string{"REPOSITORY_API_A": cfg.apiA, "REPOSITORY_API_B": cfg.apiB, "REPOSITORY_CONTROLLER_A": cfg.controllerA, "REPOSITORY_CONTROLLER_B": cfg.controllerB, "REPOSITORY_TOKEN": cfg.token} {
		if value == "" {
			return fmt.Errorf("%s is required", key)
		}
	}
	if !cfg.skipReplacement {
		if cfg.replacement != cfg.apiA && cfg.replacement != cfg.apiB {
			return errors.New("REPOSITORY_API_REPLACEMENT must identify API A or API B")
		}
		if cfg.trigger == "" {
			return errors.New("REPOSITORY_REPLACEMENT_TRIGGER_FILE is required")
		}
		_, err := os.Stat(cfg.trigger)
		if !errors.Is(err, os.ErrNotExist) {
			return errors.New("replacement trigger must be fresh")
		}
	}
	if cfg.transitionInterval <= 0 {
		return fmt.Errorf("%s evidence requires REPOSITORY_CAPACITY_TRANSITION_INTERVAL>0", cfg.mode)
	}
	if cfg.burstInterval <= 0 {
		return fmt.Errorf("%s evidence requires REPOSITORY_CAPACITY_BURST_INTERVAL>0", cfg.mode)
	}
	if cfg.overflowBackpressureWait <= 0 {
		return fmt.Errorf("%s evidence requires REPOSITORY_CAPACITY_OVERFLOW_BACKPRESSURE_WAIT>0", cfg.mode)
	}
	if cfg.mode == capacityModeDiagnostic {
		return nil
	}
	if cfg.overflowBackpressureWait < repositoryCapacityMinimumOverflowWait {
		return fmt.Errorf("%s evidence requires REPOSITORY_CAPACITY_OVERFLOW_BACKPRESSURE_WAIT>=%s", cfg.mode, repositoryCapacityMinimumOverflowWait)
	}
	if cfg.skipReplacement {
		return fmt.Errorf("REPOSITORY_CAPACITY_SKIP_REPLACEMENT is only valid in diagnostic mode")
	}
	maximumTransitionInterval := 100 * time.Millisecond
	if cfg.mode == capacityModeAlpha {
		maximumTransitionInterval = 500 * time.Millisecond
	}
	if cfg.transitionInterval > maximumTransitionInterval {
		return fmt.Errorf("%s evidence requires REPOSITORY_CAPACITY_TRANSITION_INTERVAL<=%s", cfg.mode, maximumTransitionInterval)
	}

	minimum := repositoryCapacityConfig{
		duration: 60 * time.Minute, subscribers: 1000, replayEvents: 10000,
		replaySamples: 20, resourcePool: 50, overflowTransitions: 1000, burstSize: 100,
	}
	if cfg.mode == capacityModeAlpha {
		minimum = repositoryCapacityConfig{
			duration: 10 * time.Minute, subscribers: 100, replayEvents: 1000,
			replaySamples: 5, resourcePool: 20, overflowTransitions: 256, burstSize: 20,
		}
	}
	for _, threshold := range []struct {
		name       string
		actual     int64
		minimum    int64
		formatUnit string
	}{
		{name: "REPOSITORY_CAPACITY_DURATION", actual: int64(cfg.duration), minimum: int64(minimum.duration), formatUnit: "duration"},
		{name: "REPOSITORY_CAPACITY_SUBSCRIBERS", actual: int64(cfg.subscribers), minimum: int64(minimum.subscribers)},
		{name: "REPOSITORY_CAPACITY_REPLAY_EVENTS", actual: int64(cfg.replayEvents), minimum: int64(minimum.replayEvents)},
		{name: "REPOSITORY_CAPACITY_REPLAY_SAMPLES", actual: int64(cfg.replaySamples), minimum: int64(minimum.replaySamples)},
		{name: "REPOSITORY_CAPACITY_RESOURCE_POOL", actual: int64(cfg.resourcePool), minimum: int64(minimum.resourcePool)},
		{name: "REPOSITORY_CAPACITY_OVERFLOW_TRANSITIONS", actual: int64(cfg.overflowTransitions), minimum: int64(minimum.overflowTransitions)},
		{name: "REPOSITORY_CAPACITY_BURST_SIZE", actual: int64(cfg.burstSize), minimum: int64(minimum.burstSize)},
	} {
		if threshold.actual < threshold.minimum {
			if threshold.formatUnit == "duration" {
				return fmt.Errorf("%s evidence requires %s>=%s", cfg.mode, threshold.name, time.Duration(threshold.minimum))
			}
			return fmt.Errorf("%s evidence requires %s>=%d", cfg.mode, threshold.name, threshold.minimum)
		}
	}
	return nil
}

func TestValidateRepositoryCapacityScale(t *testing.T) {
	t.Parallel()
	minimum := func(mode capacityMode) repositoryCapacityConfig {
		cfg := repositoryCapacityConfig{
			apiA: "http://api-a", apiB: "http://api-b",
			controllerA: "http://controller-a", controllerB: "http://controller-b",
			replacement: "http://api-b", trigger: t.TempDir() + "/replacement", token: "token",
			mode: mode, burstInterval: time.Minute, overflowBackpressureWait: repositoryCapacityMinimumOverflowWait,
		}
		if mode == capacityModeAlpha {
			cfg.duration, cfg.subscribers, cfg.replayEvents = 10*time.Minute, 100, 1000
			cfg.replaySamples, cfg.resourcePool, cfg.overflowTransitions, cfg.burstSize = 5, 20, 256, 20
			cfg.transitionInterval = 500 * time.Millisecond
			return cfg
		}
		cfg.duration, cfg.subscribers, cfg.replayEvents = 60*time.Minute, 1000, 10000
		cfg.replaySamples, cfg.resourcePool, cfg.overflowTransitions, cfg.burstSize = 20, 50, 1000, 100
		cfg.transitionInterval = 100 * time.Millisecond
		return cfg
	}

	for _, mode := range []capacityMode{capacityModeAlpha, capacityModeProduction} {
		mode := mode
		t.Run(string(mode)+" accepts its minimum", func(t *testing.T) {
			require.NoError(t, validateRepositoryCapacityScale(minimum(mode)))
		})
		for name, undersize := range map[string]func(*repositoryCapacityConfig){
			"duration":             func(cfg *repositoryCapacityConfig) { cfg.duration-- },
			"subscribers":          func(cfg *repositoryCapacityConfig) { cfg.subscribers-- },
			"replay events":        func(cfg *repositoryCapacityConfig) { cfg.replayEvents-- },
			"replay samples":       func(cfg *repositoryCapacityConfig) { cfg.replaySamples-- },
			"resource pool":        func(cfg *repositoryCapacityConfig) { cfg.resourcePool-- },
			"overflow transitions": func(cfg *repositoryCapacityConfig) { cfg.overflowTransitions-- },
			"burst size":           func(cfg *repositoryCapacityConfig) { cfg.burstSize-- },
			"overflow wait":        func(cfg *repositoryCapacityConfig) { cfg.overflowBackpressureWait-- },
		} {
			t.Run(string(mode)+" rejects undersized "+name, func(t *testing.T) {
				cfg := minimum(mode)
				undersize(&cfg)
				require.Error(t, validateRepositoryCapacityScale(cfg))
			})
		}
	}

	alpha := minimum(capacityModeAlpha)
	alpha.skipReplacement = true
	require.Error(t, validateRepositoryCapacityScale(alpha), "alpha replacement must remain mandatory")
	diagnostic := minimum(capacityModeAlpha)
	diagnostic.mode = capacityModeDiagnostic
	diagnostic.transitionInterval = 0
	require.Error(t, validateRepositoryCapacityScale(diagnostic), "diagnostic load interval must remain valid")
	diagnostic = minimum(capacityModeAlpha)
	diagnostic.mode = capacityModeDiagnostic
	diagnostic.burstInterval = 0
	require.Error(t, validateRepositoryCapacityScale(diagnostic), "diagnostic burst interval must remain valid")
	diagnostic = minimum(capacityModeAlpha)
	diagnostic.mode = capacityModeDiagnostic
	diagnostic.overflowBackpressureWait = 0
	require.Error(t, validateRepositoryCapacityScale(diagnostic), "diagnostic overflow wait must remain valid")

	for _, mode := range []capacityMode{capacityModeAlpha, capacityModeProduction} {
		mode := mode
		for name, interval := range map[string]time.Duration{
			"zero transition interval": 0,
			"slow transition interval": minimum(mode).transitionInterval + time.Millisecond,
		} {
			t.Run(string(mode)+" rejects "+name, func(t *testing.T) {
				cfg := minimum(mode)
				cfg.transitionInterval = interval
				require.Error(t, validateRepositoryCapacityScale(cfg))
			})
		}
		t.Run(string(mode)+" rejects zero burst interval", func(t *testing.T) {
			cfg := minimum(mode)
			cfg.burstInterval = 0
			require.Error(t, validateRepositoryCapacityScale(cfg))
		})
	}
}

func repositoryCapacityToken(t *testing.T) string {
	t.Helper()
	if token := strings.TrimSpace(os.Getenv("REPOSITORY_TOKEN")); token != "" {
		return token
	}
	contents, err := os.ReadFile(os.Getenv("REPOSITORY_TOKEN_FILE"))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(contents))
}

func ensureRepositoryCapacityNamespace(t *testing.T, cfg repositoryCapacityConfig) {
	t.Helper()
	response := gqlQueryWithURL(t, cfg.apiA, cfg.token, `query($name: String!) { namespace(by: {name: $name}) { metadata { name } } }`, map[string]any{"name": cfg.namespace})
	missing, err := repositoryCapacityNamespaceMissing(response, cfg.namespace)
	require.NoError(t, err)
	if missing {
		createNamespaceThrough(t, cfg.apiA, cfg.token, cfg.namespace)
	}
	client := &http.Client{Timeout: 5 * time.Second}
	defer client.CloseIdleConnections()
	require.NoError(t, waitRepositoryCapacityNamespaceReady(t.Context(), client, cfg))
}

func repositoryCapacityNamespaceMissing(response gqlResponse, name string) (bool, error) {
	invalid := errors.New("capacity: Namespace fixture lookup failed")
	if len(response.Errors) != 0 {
		if len(response.Errors) != 1 {
			return false, invalid
		}
		var failure struct {
			Extensions struct {
				Code string `json:"code"`
			} `json:"extensions"`
			Path []string `json:"path"`
		}
		if json.Unmarshal(response.Errors[0], &failure) != nil || failure.Extensions.Code != "NOT_FOUND" ||
			len(failure.Path) != 1 || failure.Path[0] != "namespace" {
			return false, invalid
		}
		return true, nil
	}
	var data map[string]json.RawMessage
	if json.Unmarshal(response.Data, &data) != nil {
		return false, invalid
	}
	raw, found := data["namespace"]
	if !found {
		return false, invalid
	}
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return true, nil
	}
	var current struct {
		Metadata struct {
			Name string `json:"name"`
		} `json:"metadata"`
	}
	if json.Unmarshal(raw, &current) != nil || current.Metadata.Name != name {
		return false, invalid
	}
	return false, nil
}

func waitRepositoryCapacityNamespaceReady(ctx context.Context, client *http.Client, cfg repositoryCapacityConfig) error {
	return waitRepositoryCapacityResourceReady(ctx, client, cfg, "Namespace", cfg.namespace)
}

func waitRepositoryCapacityResourceReady(ctx context.Context, client *http.Client, cfg repositoryCapacityConfig, kind, name string) error {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	query := `query($name:String!){resource:namespace(by:{name:$name}){
		metadata{name generation}
		status{observedGeneration conditions{type status observedGeneration}}
	}}`
	provisioned := "SystemRepoReady"
	variables := map[string]any{"name": name}
	switch kind {
	case "Namespace":
	case "Repository":
		query = `query($name:String!,$namespace:String!){resource:repository(by:{namespacePath:{namespace:$namespace,name:$name}}){
			metadata{name generation}
			status{observedGeneration conditions{type status observedGeneration}}
		}}`
		provisioned = "StorageProvisioned"
		variables["namespace"] = cfg.namespace
	default:
		return errors.New("capacity: unsupported fixture readiness kind")
	}
	for {
		allReady := true
		for _, endpoint := range []string{cfg.apiA, cfg.apiB} {
			var data map[string]json.RawMessage
			if err := secretCapacityGraphQL(ctx, client, strings.TrimRight(endpoint, "/"), cfg.token, query, variables, &data); err != nil {
				return err
			}
			raw, found := data["resource"]
			if !found {
				return errors.New("capacity: fixture readiness field is missing")
			}
			if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
				allReady = false
				continue
			}
			var current struct {
				Metadata *struct {
					Name       string `json:"name"`
					Generation *int   `json:"generation"`
				} `json:"metadata"`
				Status *struct {
					ObservedGeneration *int `json:"observedGeneration"`
					Conditions         *[]struct {
						Type               string `json:"type"`
						Status             string `json:"status"`
						ObservedGeneration int    `json:"observedGeneration"`
					} `json:"conditions"`
				} `json:"status"`
			}
			if json.Unmarshal(raw, &current) != nil || current.Metadata == nil || current.Metadata.Name != name ||
				current.Metadata.Generation == nil || *current.Metadata.Generation < 1 || current.Status == nil ||
				current.Status.ObservedGeneration == nil || current.Status.Conditions == nil {
				return errors.New("capacity: malformed fixture readiness observation")
			}
			ready := map[string]bool{}
			for _, condition := range *current.Status.Conditions {
				state := strings.ToUpper(condition.Status)
				if condition.Type == "" || (state != "TRUE" && state != "FALSE" && state != "UNKNOWN") {
					return errors.New("capacity: malformed fixture readiness condition")
				}
				if _, duplicate := ready[condition.Type]; duplicate {
					return errors.New("capacity: duplicate fixture readiness condition")
				}
				if condition.Type == "DeletionPending" && strings.EqualFold(condition.Status, "true") {
					return errors.New("capacity: fixture resource has deletion intent")
				}
				ready[condition.Type] = strings.EqualFold(condition.Status, "true") && condition.ObservedGeneration == *current.Metadata.Generation
			}
			if *current.Status.ObservedGeneration != *current.Metadata.Generation || !ready["Ready"] ||
				!ready["AdmissionAccepted"] || !ready[provisioned] {
				allReady = false
			}
		}
		if allReady {
			return nil
		}
		timer := time.NewTimer(200 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return fmt.Errorf("capacity: %s fixture provisioning deadline: %w", kind, ctx.Err())
		case <-timer.C:
		}
	}
}

func TestRepositoryCapacityNamespaceLookupRequiresExactAbsence(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		response         string
		missing, invalid bool
	}{
		{`{"data":{"namespace":{"metadata":{"name":"fixture"}}}}`, false, false},
		{`{"data":{"namespace":null}}`, true, false},
		{`{"data":null,"errors":[{"path":["namespace"],"extensions":{"code":"NOT_FOUND"}}]}`, true, false},
		{`{"errors":[{"path":["namespace"],"extensions":{"code":"FORBIDDEN"}}]}`, false, true},
		{`{"errors":[{"path":["repository"],"extensions":{"code":"NOT_FOUND"}}]}`, false, true},
		{`{"errors":[{"path":["namespace"],"extensions":{"code":"NOT_FOUND"}},{"extensions":{"code":"INTERNAL"}}]}`, false, true},
		{`{"data":{}}`, false, true},
		{`{"data":{"namespace":{"metadata":{"name":"another"}}}}`, false, true},
	} {
		var response gqlResponse
		require.NoError(t, json.Unmarshal([]byte(tc.response), &response))
		missing, err := repositoryCapacityNamespaceMissing(response, "fixture")
		require.Equal(t, tc.missing, missing, tc.response)
		if tc.invalid {
			require.Error(t, err, tc.response)
		} else {
			require.NoError(t, err, tc.response)
		}
	}
}

func TestRepositoryCapacityResourcesWaitForBothReplicaReadiness(t *testing.T) {
	t.Parallel()
	for _, kind := range []string{"Namespace", "Repository"} {
		t.Run(kind, func(t *testing.T) {
			ready := `{"data":{"resource":{"metadata":{"name":"fixture","generation":1},"status":{"observedGeneration":1,"conditions":[
		{"type":"AdmissionAccepted","status":"TRUE","observedGeneration":1},
		{"type":"SystemRepoReady","status":"TRUE","observedGeneration":1},
		{"type":"Ready","status":"TRUE","observedGeneration":1}]}}}}`
			if kind == "Repository" {
				ready = strings.ReplaceAll(ready, "SystemRepoReady", "StorageProvisioned")
			}
			var callsA, callsB atomic.Int64
			serverA := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				callsA.Add(1)
				_, _ = io.WriteString(w, ready)
			}))
			defer serverA.Close()
			serverB := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if callsB.Add(1) == 1 {
					_, _ = io.WriteString(w, `{"data":{"resource":null}}`)
					return
				}
				_, _ = io.WriteString(w, ready)
			}))
			defer serverB.Close()
			cfg := repositoryCapacityConfig{apiA: serverA.URL, apiB: serverB.URL, namespace: "fixture", token: "private-token"}
			require.NoError(t, waitRepositoryCapacityResourceReady(t.Context(), serverA.Client(), cfg, kind, "fixture"))
			require.Equal(t, int64(2), callsA.Load())
			require.Equal(t, int64(2), callsB.Load())
		})
	}
}

func TestRepositoryCapacityNamespaceReadinessFailsOnOperationErrors(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, response string
		status         int
	}{
		{"quota", `private response secret`, http.StatusTooManyRequests},
		{"operation", `{"errors":[{"message":"private response secret"}]}`, http.StatusOK},
		{"malformed", `{`, http.StatusOK},
		{"missing-field", `{"data":{}}`, http.StatusOK},
		{"missing-status", `{"data":{"resource":{"metadata":{"name":"fixture","generation":1}}}}`, http.StatusOK},
		{"wrong-namespace", `{"data":{"resource":{"metadata":{"name":"another","generation":1},"status":{"observedGeneration":1,"conditions":[]}}}}`, http.StatusOK},
		{"terminating", `{"data":{"resource":{"metadata":{"name":"fixture","generation":1},"status":{"observedGeneration":1,"conditions":[{"type":"DeletionPending","status":"TRUE","observedGeneration":1}]}}}}`, http.StatusOK},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls atomic.Int64
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				w.WriteHeader(tc.status)
				_, _ = io.WriteString(w, tc.response)
			}))
			defer server.Close()
			cfg := repositoryCapacityConfig{apiA: server.URL, apiB: server.URL, namespace: "fixture", token: "private-token"}
			err := waitRepositoryCapacityNamespaceReady(t.Context(), server.Client(), cfg)
			require.Error(t, err)
			require.NotContains(t, err.Error(), "private")
			require.Equal(t, int64(1), calls.Load())
		})
	}
}

func createRepositoryCapacityPool(t *testing.T, client *http.Client, cfg repositoryCapacityConfig, prefix string) {
	t.Helper()
	for i := 1; i <= cfg.resourcePool; i++ {
		endpoint := cfg.apiA
		if i%2 == 0 {
			endpoint = cfg.apiB
		}
		err := repositoryCapacityMutation(context.Background(), client, endpoint, cfg.token, true, cfg.namespace, prefix+strconv.Itoa(i), "seed", 0, "")
		require.NoError(t, err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	for i := 1; i <= cfg.resourcePool; i++ {
		require.NoError(t, waitRepositoryCapacityResourceReady(ctx, client, cfg, "Repository", prefix+strconv.Itoa(i)),
			"verify pool storage provisioning on both replicas before File authoring")
	}
}

func runRepositoryCapacityBatch(t *testing.T, client *http.Client, cfg repositoryCapacityConfig, poolPrefix, eventPrefix string, sequences []int, padding string) *capacityBitset {
	t.Helper()
	ack := newCapacityBitset(len(sequences) + 1)
	jobs := make(chan int, cfg.mutationWorkers*2)
	var wg sync.WaitGroup
	var errorMu sync.Mutex
	var firstErr error
	locks := make([]sync.Mutex, cfg.resourcePool)
	for worker := 0; worker < cfg.mutationWorkers; worker++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for sequence := range jobs {
				index := (sequence - 1) % cfg.resourcePool
				locks[index].Lock()
				endpoint := cfg.apiA
				if sequence%2 == 0 {
					endpoint = cfg.apiB
				}
				err := repositoryCapacityMutation(context.Background(), client, endpoint, cfg.token, false, cfg.namespace, poolPrefix+strconv.Itoa(index+1), eventPrefix, sequence, padding)
				locks[index].Unlock()
				if err != nil {
					errorMu.Lock()
					if firstErr == nil {
						firstErr = err
					}
					errorMu.Unlock()
					continue
				}
				ack.mark(sequence)
			}
		}()
	}
	for _, sequence := range sequences {
		jobs <- sequence
	}
	close(jobs)
	wg.Wait()
	require.NoError(t, firstErr)
	return ack
}

func runRepositoryCapacityLoad(t *testing.T, client *http.Client, cfg repositoryCapacityConfig, poolPrefix, eventPrefix string, maxTransitions int) repositoryCapacityLoadResult {
	t.Helper()
	require.Positive(t, cfg.duration)
	require.Positive(t, cfg.transitionInterval)
	require.Positive(t, cfg.burstInterval)
	expected := int64(cfg.duration/cfg.transitionInterval) + int64(cfg.duration/cfg.burstInterval)*int64(cfg.burstSize)
	require.Positive(t, expected)
	require.LessOrEqual(t, expected, int64(100_000))
	require.GreaterOrEqual(t, int64(maxTransitions), expected)
	result := repositoryCapacityLoadResult{acknowledged: newCapacityBitset(maxTransitions + 1)}
	type job struct {
		sequence  int
		offeredAt time.Time
	}
	jobs := make(chan job, repositoryCapacityQueueDepth(cfg.mutationWorkers, cfg.burstSize))
	workCtx, cancel := context.WithCancel(t.Context())
	defer cancel()
	locks := make([]sync.Mutex, cfg.resourcePool)
	var observations sync.Mutex
	result.attemptErrors = map[string]int64{}
	var wg sync.WaitGroup
	var produced, admitted, failed, backpressured atomic.Int64
	for worker := 0; worker < cfg.mutationWorkers; worker++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for item := range jobs {
				sequence := item.sequence
				queueWait := time.Since(item.offeredAt)
				index := (sequence - 1) % cfg.resourcePool
				lockStarted := time.Now()
				locks[index].Lock()
				lockWait := time.Since(lockStarted)
				endpoint := cfg.apiA
				if sequence%2 == 0 {
					endpoint = cfg.apiB
				}
				attempts := &repositoryCapacityAttemptObservation{errors: map[string]int64{}}
				requestStarted := time.Now()
				err := repositoryCapacityMutationObserved(workCtx, client, endpoint, cfg.token, false, cfg.namespace, poolPrefix+strconv.Itoa(index+1), eventPrefix, sequence, "", attempts)
				requestLatency := time.Since(requestStarted)
				locks[index].Unlock()
				observations.Lock()
				result.attempts += attempts.attempts
				result.retries += attempts.retries
				for class, count := range attempts.errors {
					result.attemptErrors[class] += count
				}
				result.queueLatencies = append(result.queueLatencies, queueWait)
				result.lockLatencies = append(result.lockLatencies, lockWait)
				result.requestLatencies = append(result.requestLatencies, requestLatency)
				result.httpLatencies = append(result.httpLatencies, attempts.httpLatencies...)
				observations.Unlock()
				if err != nil {
					failed.Add(1)
					continue
				}
				admitted.Add(1)
				result.acknowledged.mark(sequence)
			}
		}()
	}
	started := time.Now()
	sequence := 0
	enqueue := func(offeredAt time.Time, interval time.Duration) {
		sequence++
		produced.Add(1)
		if time.Since(offeredAt) >= interval {
			result.missedSchedules++
		}
		select {
		case jobs <- job{sequence, offeredAt}:
		default:
			backpressured.Add(1)
		}
	}
	transition, burst := cfg.transitionInterval, cfg.burstInterval
	for transition <= cfg.duration || burst <= cfg.duration {
		next := min(transition, burst)
		timer := time.NewTimer(max(time.Duration(0), time.Until(started.Add(next))))
		select {
		case <-workCtx.Done():
			timer.Stop()
			transition, burst = cfg.duration+1, cfg.duration+1
			continue
		case <-timer.C:
		}
		if transition == next {
			enqueue(started.Add(next), cfg.transitionInterval)
			transition += cfg.transitionInterval
		}
		if burst == next {
			for i := 0; i < cfg.burstSize; i++ {
				enqueue(started.Add(next), cfg.burstInterval)
			}
			burst += cfg.burstInterval
		}
	}
	close(jobs)
	drained := make(chan struct{})
	go func() {
		wg.Wait()
		close(drained)
	}()
	drain := time.NewTimer(30 * time.Second)
	select {
	case <-drained:
		drain.Stop()
	case <-drain.C:
		result.drainTimedOut = true
		cancel()
		<-drained
	}
	result.produced, result.admitted, result.failed, result.backpressured = produced.Load(), admitted.Load(), failed.Load(), backpressured.Load()
	result.maxSequence = sequence
	t.Logf("Repository load: scheduled=%d acknowledged=%d failed=%d dropped=%d missed=%d drainTimedOut=%t HTTPAttempts=%d retries=%d attemptErrors=%v",
		result.produced, result.admitted, result.failed, result.backpressured, result.missedSchedules, result.drainTimedOut, result.attempts, result.retries, result.attemptErrors)
	for stage, values := range map[string][]time.Duration{
		"queue_wait": result.queueLatencies, "resource_lock": result.lockLatencies,
		"mutation": result.requestLatencies, "http_attempt": result.httpLatencies,
	} {
		if len(values) != 0 {
			t.Logf("Repository stage %s: samples=%d p95=%s p99=%s", stage, len(values), percentileDuration(values, .95), percentileDuration(values, .99))
		}
	}
	require.Equal(t, expected, result.produced, "every scheduled Repository offer must be counted")
	require.Equal(t, result.produced, result.admitted+result.failed+result.backpressured)
	return result
}

func repositoryCapacityQueueDepth(workers, burstSize int) int {
	// Retain the bounded steady-state backlog while guaranteeing that one
	// required burst can be offered atomically without client-side shedding.
	return workers*4 + burstSize
}

func TestRepositoryCapacityQueueDepthIncludesOneFullBurst(t *testing.T) {
	t.Parallel()
	require.Equal(t, 180, repositoryCapacityQueueDepth(20, 100))
}

func repositoryCapacityMutation(ctx context.Context, client *http.Client, endpoint, token string, create bool, namespace, name, prefix string, sequence int, padding string) error {
	return repositoryCapacityMutationObserved(ctx, client, endpoint, token, create, namespace, name, prefix, sequence, padding, nil)
}

func repositoryCapacityMutationObserved(ctx context.Context, client *http.Client, endpoint, token string, create bool, namespace, name, prefix string, sequence int, padding string, observed *repositoryCapacityAttemptObservation) error {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	query := `mutation($input: UpdateRepositoryInput!) { updateRepository(input: $input) { repository { metadata { name resourceVersion annotations } } } }`
	operation := "updateRepository"
	if create {
		query = `mutation($input: CreateRepositoryInput!) { createRepository(input: $input) { repository { metadata { name resourceVersion annotations } } } }`
		operation = "createRepository"
	}
	annotations := map[string]any{}
	if sequence > 0 {
		annotations[repositoryCapacitySequenceAnnotation] = prefix + strconv.Itoa(sequence)
		annotations[repositoryCapacitySentAtAnnotation] = time.Now().UTC().Format(time.RFC3339Nano)
	}
	if padding != "" {
		annotations[repositoryCapacityPaddingAnnotation] = padding
	}
	input := map[string]any{
		"apiVersion": "gitstore.dev/v1beta1", "kind": "Repository",
		"metadata": map[string]any{"name": name, "namespace": namespace, "annotations": annotations},
		"spec":     map[string]any{"defaultBranch": fmt.Sprintf("capacity-%d", sequence%2), "visibility": "PRIVATE", "storageClass": "standard"},
	}
	payload, err := json.Marshal(map[string]any{"query": query, "variables": map[string]any{"input": input}})
	if err != nil {
		return err
	}
	for {
		req, reqErr := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimSuffix(endpoint, "/")+"/graphql", bytes.NewReader(payload))
		if reqErr != nil {
			return errors.New("capacity: invalid Repository request endpoint")
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+token)
		attemptStarted := time.Now()
		if observed != nil {
			observed.attempts++
		}
		response, doErr := client.Do(req)
		class := "transport"
		retryAfter := 100 * time.Millisecond
		if doErr == nil {
			contents, readErr := io.ReadAll(io.LimitReader(response.Body, 8*1024*1024+1))
			response.Body.Close()
			if readErr != nil || len(contents) > 8*1024*1024 {
				class = "response"
			} else if response.StatusCode != http.StatusOK {
				switch response.StatusCode {
				case http.StatusTooManyRequests:
					class = "quota"
				case http.StatusUnauthorized, http.StatusForbidden:
					class = "authentication"
				case http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
					class = "transport"
				default:
					class = "operation"
				}
				if delay := capacityRetryAfter(response.Header.Get("Retry-After"), time.Now()); delay > 0 {
					retryAfter = delay
				}
			} else {
				class = repositoryCapacityResponseClass(contents, operation, name, annotations)
				if class == "" {
					if observed != nil {
						observed.httpLatencies = append(observed.httpLatencies, time.Since(attemptStarted))
					}
					return nil
				}
			}
			doErr = fmt.Errorf("capacity: Repository mutation failed (%s)", class)
		} else {
			if ctx.Err() != nil {
				doErr = ctx.Err()
				class = secretCapacityFailureClass(doErr)
			} else {
				doErr = errors.New("capacity: Repository transport failed")
			}
		}
		if observed != nil {
			observed.httpLatencies = append(observed.httpLatencies, time.Since(attemptStarted))
			observed.errors[class]++
		}
		if (class != "conflict" && class != "quota" && class != "transport") || ctx.Err() != nil {
			return doErr
		}
		timer := time.NewTimer(retryAfter)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
			if observed != nil {
				observed.retries++
			}
		}
	}
}

func repositoryCapacityResponseClass(contents []byte, operation, name string, annotations map[string]any) string {
	var envelope gqlResponse
	if json.Unmarshal(contents, &envelope) != nil {
		return "response"
	}
	if len(envelope.Errors) != 0 {
		for _, raw := range envelope.Errors {
			var failure struct {
				Extensions struct {
					Code        string `json:"code"`
					Diagnostics []struct {
						Reason string `json:"reason"`
					} `json:"diagnostics"`
				} `json:"extensions"`
			}
			if json.Unmarshal(raw, &failure) != nil || failure.Extensions.Code != "CONFLICT" {
				return "operation"
			}
			for _, diagnostic := range failure.Extensions.Diagnostics {
				if diagnostic.Reason == "SUPERSEDED" {
					return "superseded"
				}
				if diagnostic.Reason != "RESOURCE_VERSION_CONFLICT" {
					return "operation"
				}
			}
		}
		return "conflict"
	}
	var data map[string]*struct {
		Repository *struct {
			Metadata struct {
				Name            string         `json:"name"`
				ResourceVersion string         `json:"resourceVersion"`
				Annotations     map[string]any `json:"annotations"`
			} `json:"metadata"`
		} `json:"repository"`
	}
	if json.Unmarshal(envelope.Data, &data) != nil || data[operation] == nil || data[operation].Repository == nil {
		return "response"
	}
	metadata := data[operation].Repository.Metadata
	if metadata.Name != name || metadata.ResourceVersion == "" {
		return "response"
	}
	for key, value := range annotations {
		actual, actualString := metadata.Annotations[key].(string)
		expected, expectedString := value.(string)
		if !actualString || !expectedString || actual != expected {
			return "response"
		}
	}
	return ""
}

func TestRepositoryCapacityResponseRequiresConfirmedMutation(t *testing.T) {
	t.Parallel()
	annotations := map[string]any{"capacity.gitstore.dev/sequence": "run-1"}
	for _, tc := range []struct {
		name, response, class string
	}{
		{"confirmed", `{"data":{"updateRepository":{"repository":{"metadata":{"name":"catalog","resourceVersion":"2","annotations":{"capacity.gitstore.dev/sequence":"run-1"}}}}}}`, ""},
		{"empty", `{}`, "response"},
		{"malformed", `{`, "response"},
		{"null-data", `{"data":null}`, "response"},
		{"null-payload", `{"data":{"updateRepository":null}}`, "response"},
		{"null-repository", `{"data":{"updateRepository":{"repository":null}}}`, "response"},
		{"wrong-operation", `{"data":{"createRepository":{"repository":{"metadata":{"name":"catalog","resourceVersion":"2"}}}}}`, "response"},
		{"wrong-name", `{"data":{"updateRepository":{"repository":{"metadata":{"name":"another","resourceVersion":"2"}}}}}`, "response"},
		{"missing-version", `{"data":{"updateRepository":{"repository":{"metadata":{"name":"catalog"}}}}}`, "response"},
		{"wrong-sequence", `{"data":{"updateRepository":{"repository":{"metadata":{"name":"catalog","resourceVersion":"2","annotations":{"capacity.gitstore.dev/sequence":"run-2"}}}}}}`, "response"},
		{"nonscalar-sequence", `{"data":{"updateRepository":{"repository":{"metadata":{"name":"catalog","resourceVersion":"2","annotations":{"capacity.gitstore.dev/sequence":["run-1"]}}}}}}`, "response"},
		{"conflict", `{"errors":[{"extensions":{"code":"CONFLICT"}}]}`, "conflict"},
		{"superseded", `{"errors":[{"extensions":{"code":"CONFLICT","diagnostics":[{"reason":"SUPERSEDED"}]}}]}`, "superseded"},
		{"mixed-errors", `{"errors":[{"extensions":{"code":"CONFLICT"}},{"extensions":{"code":"FORBIDDEN"}}]}`, "operation"},
		{"unknown-error", `{"errors":[{"message":"private provider details"}]}`, "operation"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.class, repositoryCapacityResponseClass([]byte(tc.response), "updateRepository", "catalog", annotations))
		})
	}
}

func repositoryCapacityConfirmTestMutation(w http.ResponseWriter, r *http.Request) {
	var payload struct {
		Variables struct {
			Input struct {
				Metadata map[string]any `json:"metadata"`
			} `json:"input"`
		} `json:"variables"`
	}
	if json.NewDecoder(r.Body).Decode(&payload) != nil {
		http.Error(w, "malformed test request", http.StatusBadRequest)
		return
	}
	payload.Variables.Input.Metadata["resourceVersion"] = "2"
	_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{
		"updateRepository": map[string]any{"repository": map[string]any{"metadata": payload.Variables.Input.Metadata}},
	}})
}

func TestRepositoryCapacityMutationRetriesOnlyTransientFailures(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, body string
		status     int
		retries    bool
	}{
		{"quota", "private response secret", http.StatusTooManyRequests, true},
		{"transport", "private response secret", http.StatusBadGateway, true},
		{"conflict", `{"errors":[{"extensions":{"code":"CONFLICT"}}]}`, http.StatusOK, true},
		{"authentication", "private response secret", http.StatusUnauthorized, false},
		{"forbidden", "private response secret", http.StatusForbidden, false},
		{"invalid-operation", "private response secret", http.StatusBadRequest, false},
		{"malformed", "{private response secret", http.StatusOK, false},
		{"empty-success", `{}`, http.StatusOK, false},
		{"policy-rejection", `{"errors":[{"message":"private response secret","extensions":{"code":"ADMISSION_REJECTED"}}]}`, http.StatusOK, false},
		{"superseded", `{"errors":[{"extensions":{"code":"CONFLICT","diagnostics":[{"reason":"SUPERSEDED"}]}}]}`, http.StatusOK, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls atomic.Int64
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if calls.Add(1) == 1 {
					w.WriteHeader(tc.status)
					_, _ = io.WriteString(w, tc.body)
					return
				}
				repositoryCapacityConfirmTestMutation(w, r)
			}))
			defer server.Close()
			observed := &repositoryCapacityAttemptObservation{errors: map[string]int64{}}
			err := repositoryCapacityMutationObserved(t.Context(), server.Client(), server.URL, "private-token", false, "namespace", "catalog", "run-", 1, "", observed)
			if tc.retries {
				require.NoError(t, err)
				require.Equal(t, int64(2), calls.Load())
				require.Equal(t, int64(1), observed.retries)
			} else {
				require.Error(t, err)
				require.NotContains(t, err.Error(), "private")
				require.Equal(t, int64(1), calls.Load())
				require.Zero(t, observed.retries)
			}
			require.Equal(t, calls.Load(), observed.attempts)
			require.Len(t, observed.httpLatencies, int(observed.attempts))
			var failures int64
			for _, count := range observed.errors {
				failures += count
			}
			require.Equal(t, int64(1), failures)
		})
	}
}

func TestRepositoryCapacityMutationCancellationInterruptsRetryDelay(t *testing.T) {
	t.Parallel()
	var calls atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Retry-After", "30")
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer server.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()
	observed := &repositoryCapacityAttemptObservation{errors: map[string]int64{}}
	started := time.Now()
	err := repositoryCapacityMutationObserved(ctx, server.Client(), server.URL, "private-token", false, "namespace", "catalog", "run-", 1, "", observed)
	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.Less(t, time.Since(started), time.Second)
	require.Equal(t, int64(1), calls.Load())
	require.Equal(t, int64(1), observed.errors["quota"])
	require.Zero(t, observed.retries)
}

func TestRepositoryCapacityLoadCountsEveryScheduledOffer(t *testing.T) {
	t.Parallel()
	for _, slow := range []bool{false, true} {
		t.Run(strconv.FormatBool(slow), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if slow {
					time.Sleep(120 * time.Millisecond)
				}
				repositoryCapacityConfirmTestMutation(w, r)
			}))
			defer server.Close()
			cfg := repositoryCapacityConfig{
				apiA: server.URL, apiB: server.URL, token: "private-token", namespace: "namespace",
				resourcePool: 1, mutationWorkers: 1, burstSize: 2, duration: 200 * time.Millisecond,
				transitionInterval: 50 * time.Millisecond, burstInterval: 100 * time.Millisecond,
			}
			if slow {
				cfg.transitionInterval = time.Millisecond
			}
			expected := int(cfg.duration/cfg.transitionInterval) + int(cfg.duration/cfg.burstInterval)*cfg.burstSize
			result := runRepositoryCapacityLoad(t, server.Client(), cfg, "catalog", "run-", expected)
			require.Equal(t, int64(expected), result.produced)
			require.Equal(t, result.produced, result.admitted+result.failed+result.backpressured)
			require.Zero(t, result.failed)
			require.Equal(t, result.admitted, result.attempts)
			require.Zero(t, result.retries)
			require.Empty(t, result.attemptErrors)
			require.Len(t, result.queueLatencies, int(result.admitted))
			require.Len(t, result.lockLatencies, int(result.admitted))
			require.Len(t, result.requestLatencies, int(result.admitted))
			require.Len(t, result.httpLatencies, int(result.attempts))
			if slow {
				require.Positive(t, result.backpressured)
			} else {
				require.Zero(t, result.backpressured)
			}
		})
	}
}

func repositoryCapacityBootstrapCursor(t *testing.T, endpoint string, cfg repositoryCapacityConfig) string {
	t.Helper()
	conn, err := dialRepositoryCapacityWatch(endpoint, cfg, "__repository_watch_bootstrap__")
	require.NoError(t, err)
	defer conn.Close()
	event, err := readRepositoryCapacityEvent(conn)
	require.NoError(t, err)
	require.Equal(t, "BOOKMARK", event.Type)
	require.NotEmpty(t, event.ResourceVersion)
	return event.ResourceVersion
}

func dialRepositoryCapacityWatch(endpoint string, cfg repositoryCapacityConfig, cursor string) (*websocket.Conn, error) {
	return dialRepositoryCapacityWatchWithTimeout(endpoint, cfg, cursor, 30*time.Second)
}

func dialRepositoryCapacityWatchWithTimeout(endpoint string, cfg repositoryCapacityConfig, cursor string, timeout time.Duration) (*websocket.Conn, error) {
	deadline := time.Now().Add(timeout)
	var lastErr error
	for {
		conn, err := dialRepositoryCapacityWatchOnce(endpoint, cfg, cursor)
		if err == nil {
			return conn, nil
		}
		lastErr = err
		if !time.Now().Before(deadline) {
			return nil, fmt.Errorf("Repository watch endpoint unavailable for %s: %w", timeout, lastErr)
		}
		time.Sleep(min(250*time.Millisecond, time.Until(deadline)))
	}
}

func dialRepositoryCapacityWatchOnce(endpoint string, cfg repositoryCapacityConfig, cursor string) (*websocket.Conn, error) {
	selection := `type name resourceVersion repository { metadata { annotations } }`
	return dialRepositoryCapacityWatchSelection(endpoint, cfg, cursor, selection)
}

func TestDialRepositoryCapacityWatchRetriesTransientHandshake(t *testing.T) {
	t.Parallel()
	var attempts atomic.Int64
	upgrader := websocket.Upgrader{
		Subprotocols: []string{"graphql-transport-ws"},
		CheckOrigin:  func(*http.Request) bool { return true },
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if attempts.Add(1) == 1 {
			http.Error(w, "rate limit exceeded", http.StatusTooManyRequests)
			return
		}
		conn, err := upgrader.Upgrade(w, r, nil)
		require.NoError(t, err)
		defer conn.Close()
		var message map[string]any
		require.NoError(t, conn.ReadJSON(&message))
		require.Equal(t, "connection_init", message["type"])
		require.NoError(t, conn.WriteJSON(map[string]any{"type": "connection_ack"}))
		require.NoError(t, conn.ReadJSON(&message))
		require.Equal(t, "subscribe", message["type"])
	}))
	defer server.Close()

	conn, err := dialRepositoryCapacityWatchWithTimeout(server.URL, repositoryCapacityConfig{
		token: "capacity-token", namespace: "capacity-namespace",
	}, "capacity-cursor", 2*time.Second)
	require.NoError(t, err)
	require.EqualValues(t, 2, attempts.Load())
	require.NoError(t, conn.Close())
}

func dialRepositoryCapacityWatchSelection(endpoint string, cfg repositoryCapacityConfig, cursor, selection string, selectors ...map[string]any) (*websocket.Conn, error) {
	wsURL := strings.Replace(strings.TrimSuffix(endpoint, "/"), "http", "ws", 1) + "/graphql"
	header := http.Header{"Authorization": []string{"Bearer " + cfg.token}}
	dialer := websocket.Dialer{Subprotocols: []string{"graphql-transport-ws"}, HandshakeTimeout: 10 * time.Second}
	conn, response, err := dialer.Dial(wsURL, header)
	if err != nil {
		return nil, capacityWebSocketHandshakeError(err, response)
	}
	if err = conn.WriteJSON(map[string]any{"type": "connection_init", "payload": map[string]any{"Authorization": "Bearer " + cfg.token}}); err != nil {
		conn.Close()
		return nil, err
	}
	var ack map[string]any
	if err = conn.ReadJSON(&ack); err != nil || ack["type"] != "connection_ack" {
		conn.Close()
		return nil, fmt.Errorf("repository watch connection acknowledgement: %v %v", ack, err)
	}
	var selector map[string]any
	if len(selectors) != 0 {
		selector = selectors[0]
	}
	query := fmt.Sprintf(`subscription($namespace: String!, $cursor: String, $selector: LabelSelectorInput) { watchRepositories(namespace: $namespace, resourceVersion: $cursor, selector: $selector) { %s } }`, selection)
	err = conn.WriteJSON(map[string]any{"id": "repository-capacity", "type": "subscribe", "payload": map[string]any{"query": query, "variables": map[string]any{"namespace": cfg.namespace, "cursor": cursor, "selector": selector}}})
	if err != nil {
		conn.Close()
		return nil, err
	}
	return conn, nil
}

func readRepositoryCapacityEvent(conn *websocket.Conn) (repositoryCapacityEvent, error) {
	for {
		var message struct {
			Type    string `json:"type"`
			Payload struct {
				Data struct {
					Watch repositoryCapacityEvent `json:"watchRepositories"`
				} `json:"data"`
				Errors []json.RawMessage `json:"errors"`
			} `json:"payload"`
		}
		if err := conn.ReadJSON(&message); err != nil {
			return repositoryCapacityEvent{}, err
		}
		switch message.Type {
		case "next":
			if len(message.Payload.Errors) > 0 {
				return repositoryCapacityEvent{}, fmt.Errorf("Repository watch errors: %s", message.Payload.Errors)
			}
			return message.Payload.Data.Watch, nil
		case "error":
			return repositoryCapacityEvent{}, fmt.Errorf("Repository watch terminal error: %v", message.Payload)
		case "complete":
			return repositoryCapacityEvent{}, errors.New("Repository watch completed")
		}
	}
}

func repositoryCapacityReplay(endpoint string, cfg repositoryCapacityConfig, cursor, prefix string, count int, timeout time.Duration) (*capacityBitset, error) {
	conn, err := dialRepositoryCapacityWatch(endpoint, cfg, cursor)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	_ = conn.SetReadDeadline(time.Now().Add(timeout))
	seen := newCapacityBitset(count + 1)
	for seen.count() < count {
		event, readErr := readRepositoryCapacityEvent(conn)
		if readErr != nil {
			return seen, readErr
		}
		if sequence, ok := repositoryCapacitySequence(event, prefix); ok && sequence <= count {
			seen.mark(sequence)
		}
	}
	return seen, nil
}

func openRepositoryCapacitySubscribers(t *testing.T, cfg repositoryCapacityConfig, maxTransitions int) []*repositoryCapacitySubscriber {
	t.Helper()
	states := make([]*repositoryCapacitySubscriber, 0, cfg.subscribers)
	for i := 0; i < cfg.subscribers; i++ {
		endpoint := cfg.apiA
		if i%2 == 1 {
			endpoint = cfg.apiB
		}
		states = append(states, &repositoryCapacitySubscriber{endpoint: endpoint, cursor: repositoryCapacityBootstrapCursor(t, endpoint, cfg), seen: newCapacityBitset(maxTransitions + 1)})
	}
	return states
}

func (s *repositoryCapacitySubscriber) read(ctx context.Context, cfg repositoryCapacityConfig, prefix string) error {
	for ctx.Err() == nil {
		conn, err := dialRepositoryCapacityWatch(s.endpoint, cfg, s.cursor)
		if err != nil {
			time.Sleep(100 * time.Millisecond)
			continue
		}
		for ctx.Err() == nil {
			_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
			event, readErr := readRepositoryCapacityEvent(conn)
			if readErr != nil {
				break
			}
			if event.ResourceVersion != "" {
				s.cursor = event.ResourceVersion
			}
			if sequence, ok := repositoryCapacitySequence(event, prefix); ok {
				first := s.seen.mark(sequence)
				if first && event.Repository != nil {
					if sent, ok := event.Repository.Metadata.Annotations[repositoryCapacitySentAtAnnotation].(string); ok {
						if sentAt, parseErr := time.Parse(time.RFC3339Nano, sent); parseErr == nil {
							s.mu.Lock()
							s.latency = append(s.latency, time.Since(sentAt))
							s.mu.Unlock()
						}
					}
				}
			}
		}
		conn.Close()
	}
	return ctx.Err()
}

func (b *capacityBitset) has(sequence int) bool {
	if sequence < 1 {
		return false
	}
	word := (sequence - 1) / 64
	if word >= len(b.words) {
		return false
	}
	mask := uint64(1) << uint((sequence-1)%64)
	return b.words[word].Load()&mask != 0
}

func repositoryCapacitySequence(event repositoryCapacityEvent, prefix string) (int, bool) {
	if event.Repository == nil {
		return 0, false
	}
	raw, ok := event.Repository.Metadata.Annotations[repositoryCapacitySequenceAnnotation].(string)
	if !ok || !strings.HasPrefix(raw, prefix) {
		return 0, false
	}
	sequence, err := strconv.Atoi(strings.TrimPrefix(raw, prefix))
	return sequence, err == nil && sequence > 0
}

func waitRepositoryCapacitySubscribers(t *testing.T, subscribers []*repositoryCapacitySubscriber, acknowledged *capacityBitset, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		missing := 0
		for _, subscriber := range subscribers {
			for sequence := 1; sequence < len(acknowledged.words)*64; sequence++ {
				if acknowledged.has(sequence) && !subscriber.seen.has(sequence) {
					missing++
				}
			}
		}
		if missing == 0 {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatal("Repository subscribers did not observe every acknowledged transition before the recovery deadline")
}

func runRepositoryCapacityReplacement(cfg repositoryCapacityConfig, client *http.Client, soakStarted time.Time) capacityRecoveryResult {
	time.Sleep(cfg.replacementDelay)
	label := "api_b"
	if cfg.replacement == cfg.apiA {
		label = "api_a"
	}
	before, err := repositoryCapacityAPIIdentityValue(client, cfg.replacement)
	if err != nil {
		return capacityRecoveryResult{err: err}
	}
	beforeStop, err := fetchCapacityMetrics(client, cfg.replacement)
	if err != nil {
		return capacityRecoveryResult{err: err}
	}
	result := capacityRecoveryResult{label: label, beforeStop: beforeStop, beforeStopAt: time.Since(soakStarted)}
	if err := os.WriteFile(cfg.trigger, []byte("replace repository capacity replica\n"), 0o600); err != nil {
		result.err = err
		return result
	}
	probe := &http.Client{Timeout: 500 * time.Millisecond}
	outageDeadline := time.Now().Add(30 * time.Second)
	for endpointReady(probe, cfg.replacement) && time.Now().Before(outageDeadline) {
		time.Sleep(100 * time.Millisecond)
	}
	if endpointReady(probe, cfg.replacement) {
		result.err = errors.New("replacement did not produce an observed API outage")
		return result
	}
	recoveryDeadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(recoveryDeadline) {
		if endpointReady(probe, cfg.replacement) {
			after, identityErr := repositoryCapacityAPIIdentityValue(client, cfg.replacement)
			if identityErr == nil && after != before {
				result.afterStart, result.err = fetchCapacityMetrics(client, cfg.replacement)
				result.afterStartAt = time.Since(soakStarted)
				result.duration = result.afterStartAt - result.beforeStopAt
				return result
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	result.err = errors.New("replacement API did not return with a new process identity within 30s")
	return result
}

func runRepositoryCapacityOverflow(t *testing.T, client *http.Client, cfg repositoryCapacityConfig, poolPrefix, eventPrefix string) {
	t.Helper()
	selection := `type name resourceVersion repository { metadata { annotations } }`
	for i := 0; i < 32; i++ {
		selection += fmt.Sprintf(` payload%d: repository { metadata { annotations } }`, i)
	}
	conn, err := openRepositoryCapacityOverflowSubscriber(cfg.overflowAPI, cfg, selection)
	require.NoError(t, err)
	defer conn.Close()
	if tcp, ok := conn.UnderlyingConn().(*net.TCPConn); ok {
		require.NoError(t, tcp.SetReadBuffer(1024), "constrain the deliberately slow consumer's receive window")
	}
	sequences := make([]int, cfg.overflowTransitions)
	for i := range sequences {
		sequences[i] = i + 1
	}
	padding := strings.Repeat("x", 16*1024)
	ack := runRepositoryCapacityBatch(t, client, cfg, poolPrefix, eventPrefix, sequences, padding)
	require.Equal(t, cfg.overflowTransitions, ack.count())
	// Do not read from the slow connection until the server's bounded delivery
	// timeout has elapsed. Reading immediately after the batch can drain the
	// resolver and transport buffers before SUBSCRIBER_OVERFLOW is emitted.
	time.Sleep(cfg.overflowBackpressureWait)
	require.NoError(t, conn.SetReadDeadline(time.Now().Add(60*time.Second)))
	for i := 0; i < cfg.overflowTransitions*4+100; i++ {
		var message struct {
			Type    string          `json:"type"`
			Payload json.RawMessage `json:"payload"`
		}
		err = conn.ReadJSON(&message)
		if err != nil {
			break
		}
		if message.Type == "error" || (message.Type == "next" && bytes.Contains(message.Payload, []byte("SUBSCRIBER_OVERFLOW"))) {
			require.Contains(t, string(message.Payload), "WATCH_EXPIRED")
			require.Contains(t, string(message.Payload), "SUBSCRIBER_OVERFLOW")
			return
		}
	}
	t.Fatal("slow Repository subscriber did not receive the bounded overflow terminal error")
}

// TestRepositoryLifecycle_OverflowOnly is an internal deployed diagnostic for
// iterating on the strict overflow contract without repeating the full soak.
// It remains gated so focused and in-process test runs cannot masquerade as
// live evidence.
func TestRepositoryLifecycle_OverflowOnly(t *testing.T) {
	if os.Getenv("REPOSITORY_LIFECYCLE_OVERFLOW_RUN") != "1" {
		t.Skip("set REPOSITORY_LIFECYCLE_OVERFLOW_RUN=1 against the deployed Repository capacity stack")
	}
	cfg := loadRepositoryCapacityConfig(t)
	require.NotEmpty(t, cfg.apiA)
	require.NotEmpty(t, cfg.apiB)
	require.NotEmpty(t, cfg.token)
	require.Positive(t, cfg.overflowTransitions)
	require.Positive(t, cfg.overflowBackpressureWait)

	ensureRepositoryCapacityNamespace(t, cfg)
	runID := strconv.FormatInt(time.Now().UnixNano(), 36)
	poolPrefix := "rcop-" + runID + "-"
	createRepositoryCapacityPool(t, http.DefaultClient, cfg, poolPrefix)
	runRepositoryCapacityOverflow(t, http.DefaultClient, cfg, poolPrefix, "rco-only-"+runID+"-")
}

func openRepositoryCapacityOverflowSubscriber(endpoint string, cfg repositoryCapacityConfig, selection string) (*websocket.Conn, error) {
	conn, err := dialRepositoryCapacityWatchSelection(endpoint, cfg, "__repository_watch_bootstrap__", selection)
	if err != nil {
		return nil, err
	}
	bookmark, err := readRepositoryCapacityEvent(conn)
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("wait for slow Repository subscriber registration: %w", err)
	}
	if bookmark.Type != "BOOKMARK" || bookmark.ResourceVersion == "" {
		conn.Close()
		return nil, fmt.Errorf("slow Repository subscriber registration returned %q with resourceVersion %q, want BOOKMARK", bookmark.Type, bookmark.ResourceVersion)
	}
	return conn, nil
}

func TestOpenRepositoryCapacityOverflowSubscriberWaitsForBookmark(t *testing.T) {
	t.Parallel()
	upgrader := websocket.Upgrader{
		Subprotocols: []string{"graphql-transport-ws"},
		CheckOrigin:  func(*http.Request) bool { return true },
	}
	subscribed := make(chan struct{})
	releaseBookmark := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		require.NoError(t, err)
		defer conn.Close()
		var message struct {
			Type    string `json:"type"`
			Payload struct {
				Variables struct {
					Cursor string `json:"cursor"`
				} `json:"variables"`
			} `json:"payload"`
		}
		require.NoError(t, conn.ReadJSON(&message))
		require.Equal(t, "connection_init", message.Type)
		require.NoError(t, conn.WriteJSON(map[string]any{"type": "connection_ack"}))
		require.NoError(t, conn.ReadJSON(&message))
		require.Equal(t, "subscribe", message.Type)
		require.Equal(t, "__repository_watch_bootstrap__", message.Payload.Variables.Cursor)
		close(subscribed)
		<-releaseBookmark
		require.NoError(t, conn.WriteJSON(map[string]any{
			"type": "next",
			"payload": map[string]any{"data": map[string]any{"watchRepositories": map[string]any{
				"type": "BOOKMARK", "resourceVersion": "rwv1:test:1",
			}}},
		}))
		<-r.Context().Done()
	}))
	defer server.Close()

	result := make(chan struct {
		conn *websocket.Conn
		err  error
	}, 1)
	go func() {
		conn, err := openRepositoryCapacityOverflowSubscriber(server.URL, repositoryCapacityConfig{
			token: "capacity-token", namespace: "capacity-namespace",
		}, `type resourceVersion`)
		result <- struct {
			conn *websocket.Conn
			err  error
		}{conn: conn, err: err}
	}()

	select {
	case <-subscribed:
	case <-time.After(2 * time.Second):
		t.Fatal("overflow subscriber did not send its subscription")
	}
	select {
	case <-result:
		t.Fatal("overflow subscriber returned before its registration BOOKMARK")
	default:
	}
	close(releaseBookmark)
	select {
	case opened := <-result:
		require.NoError(t, opened.err)
		require.NotNil(t, opened.conn)
		require.NoError(t, opened.conn.Close())
	case <-time.After(2 * time.Second):
		t.Fatal("overflow subscriber did not return after its registration BOOKMARK")
	}
}

func assertRepositoryCapacityDeletionParity(t *testing.T, client *http.Client, cfg repositoryCapacityConfig, name string) {
	t.Helper()
	ctx := context.Background()
	gitURL := strings.TrimRight(os.Getenv("GIT_URL"), "/")
	require.NoError(t, validateSecretCapacityGitEndpoint(gitURL))
	var previousUID string
	directory := filepath.Join(t.TempDir(), "authoring")
	for _, mode := range []string{"mutation", "push"} {
		require.NoError(t, repositoryCapacityMutation(ctx, client, cfg.apiA, cfg.token, true, cfg.namespace, name, "deletion", 0, ""))
		response := gqlQueryWithURL(t, cfg.apiB, cfg.token, `query($namespace: String!, $name: String!) {
			repository(by: {namespacePath: {namespace: $namespace, name: $name}}) { id metadata { resourceVersion } }
		}`, map[string]any{"namespace": cfg.namespace, "name": name})
		require.Empty(t, response.Errors)
		var current struct {
			Repository struct {
				ID       string `json:"id"`
				Metadata struct {
					ResourceVersion string `json:"resourceVersion"`
				} `json:"metadata"`
			} `json:"repository"`
		}
		require.NoError(t, json.Unmarshal(response.Data, &current))
		require.NotEmpty(t, current.Repository.ID)
		if previousUID != "" {
			require.NotEqual(t, previousUID, current.Repository.ID)
			stale := gqlQueryWithURL(t, cfg.apiA, cfg.token, `mutation($ns: String!, $name: String!, $uid: ID!, $rv: String!) {
				completeRepositoryDeletion(input: {namespace: $ns, name: $name, id: $uid, resourceVersion: $rv}) { id }
			}`, map[string]any{"ns": cfg.namespace, "name": name, "uid": previousUID, "rv": current.Repository.Metadata.ResourceVersion})
			require.NotEmpty(t, stale.Errors, "stale completion must reject a same-name replacement")
			var staleError struct {
				Extensions struct {
					Code string `json:"code"`
				} `json:"extensions"`
			}
			require.NoError(t, json.Unmarshal(stale.Errors[0], &staleError))
			require.Equal(t, "CONFLICT", staleError.Extensions.Code)
		}
		path := "repositories/" + name + ".md"
		if mode == "mutation" {
			_, err := newSecretCapacityGitWorker(ctx, directory, gitURL+"/"+cfg.namespace+"/gitstore-system.git", cfg.namespace, name, cfg.token, 0)
			require.NoError(t, err)
		} else {
			_, err := secretCapacityGit(ctx, directory, cfg.token, "fetch", "--quiet", "--depth=1", "origin", "refs/heads/main")
			require.NoError(t, err)
			_, err = secretCapacityGit(ctx, directory, "", "checkout", "--quiet", "--detach", "FETCH_HEAD")
			require.NoError(t, err)
		}
		contents, err := secretCapacityGit(ctx, directory, "", "ls-tree", "--name-only", "HEAD", "--", path)
		require.NoError(t, err)
		require.Equal(t, path, strings.TrimSpace(string(contents)), "created manifest must exist")
		if mode == "mutation" {
			deleted := gqlQueryWithURL(t, cfg.apiB, cfg.token, `mutation($id: ID!) {
				deleteRepository(input: {id: $id}) { outcome repository { metadata { deletionTimestamp } } }
			}`, map[string]any{"id": current.Repository.ID})
			require.Empty(t, deleted.Errors)
			require.Contains(t, string(deleted.Data), "TERMINATION_STARTED")
		} else {
			_, err := secretCapacityGit(ctx, directory, "", "rm", "--", path)
			require.NoError(t, err)
			_, err = secretCapacityGit(ctx, directory, "", "commit", "--quiet", "-m", "Delete capacity Repository")
			require.NoError(t, err)
			_, err = secretCapacityGit(ctx, directory, cfg.token, "push", "--quiet", "origin", "HEAD:refs/heads/main")
			require.NoError(t, err)
		}
		_, err = secretCapacityGit(ctx, directory, cfg.token, "fetch", "--quiet", "--depth=1", "origin", "refs/heads/main")
		require.NoError(t, err)
		contents, err = secretCapacityGit(ctx, directory, "", "ls-tree", "--name-only", "FETCH_HEAD", "--", path)
		require.NoError(t, err)
		require.Empty(t, strings.TrimSpace(string(contents)), "initiation must remove the authoring manifest")
		require.Eventually(t, func() bool {
			for _, endpoint := range []string{cfg.apiA, cfg.apiB} {
				read := gqlQueryWithURL(t, endpoint, cfg.token, `query($id: ID!) { node(id: $id) { id } }`, map[string]any{"id": current.Repository.ID})
				if len(read.Errors) != 0 || !bytes.Contains(read.Data, []byte(`"node":null`)) {
					return false
				}
			}
			return true
		}, 60*time.Second, 250*time.Millisecond, "controllers must complete deletion on both replicas")
		previousUID = current.Repository.ID
	}
	t.Log("repository deletion parity: mutation/push manifest removal, controller completion, name reuse and stale UID rejection verified")
}

func assertRepositoryCrossReplicaRead(t *testing.T, cfg repositoryCapacityConfig, name string) {
	t.Helper()
	for _, endpoint := range []string{cfg.apiA, cfg.apiB} {
		response := gqlQueryWithURL(t, endpoint, cfg.token, `query($namespace: String!, $name: String!) { repository(by: {namespacePath: {namespace: $namespace, name: $name}}) { id metadata { uid name namespace } } }`, map[string]any{"namespace": cfg.namespace, "name": name})
		require.Empty(t, response.Errors)
		require.Contains(t, string(response.Data), `"name":"`+name+`"`)
	}
}

func repositoryCapacityAPIIdentity(t *testing.T, client *http.Client, endpoint string) string {
	t.Helper()
	identity, err := repositoryCapacityAPIIdentityValue(client, endpoint)
	require.NoError(t, err)
	require.NotEmpty(t, identity)
	return identity
}

func repositoryCapacityAPIIdentityValue(client *http.Client, endpoint string) (string, error) {
	response, err := client.Get(strings.TrimSuffix(endpoint, "/") + "/metrics")
	if err != nil {
		return "", err
	}
	defer response.Body.Close()
	contents, err := io.ReadAll(response.Body)
	if err != nil {
		return "", err
	}
	const prefix = `gitstore_api_process_instance_info{instance_id="`
	for _, line := range strings.Split(string(contents), "\n") {
		if strings.HasPrefix(line, prefix) {
			return strings.SplitN(strings.TrimPrefix(line, prefix), `"}`, 2)[0], nil
		}
	}
	return "", errors.New("gitstore_api_process_instance_info metric is absent")
}

func mustRepositoryCapacityMetrics(t *testing.T, client *http.Client, endpoint string) capacityProcessMetrics {
	t.Helper()
	metrics, err := fetchCapacityMetrics(client, endpoint)
	require.NoError(t, err)
	return metrics
}

type secretCapacityLoadConfig struct {
	duration, interval, burstInterval, burstWindow, drainTimeout time.Duration
	workers, queueLimit, burstSize                               int
}

func secretCapacityProductionLoadConfig() secretCapacityLoadConfig {
	return secretCapacityLoadConfig{
		duration: time.Hour, interval: 100 * time.Millisecond,
		burstInterval: time.Minute, burstSize: 100, burstWindow: time.Second,
		workers: 32, queueLimit: 256, drainTimeout: 30 * time.Second,
	}
}

type secretCapacityPushBatch struct {
	sequence  int64
	burst     int
	offeredAt time.Time
}

type secretCapacityPushResult struct {
	gitAttempted, acknowledged                         bool
	files, bytes                                       int
	referenceLatencies                                 []time.Duration
	ackLatency                                         time.Duration
	resolutionLatency                                  time.Duration
	resolutionFailed, projectionFailed                 bool
	projectionChecks                                   int64
	projectionErr                                      error
	err                                                error
	localGitLatency, gitHTTPLatency, projectionLatency time.Duration
	projectionReadLatency                              time.Duration
	projectionReadPeakLatency                          time.Duration
	projectionPolls                                    int64
	failureStage                                       string
}

type secretCapacityPushMeasurements struct {
	sustainedOffered, missedSchedules, completed, dropped, gitPushes, acknowledged, failed int64
	queuePeak, workersPeak, maxFiles, maxBytes                                             int
	offeredDuration                                                                        time.Duration
	bursts                                                                                 []secretCapacityBurst
	pushLatencies, referenceLatencies                                                      []time.Duration
	resolutionLatencies                                                                    []time.Duration
	localHealthyFailures, projectionLoss, crossReplicaChecks, typedFiles                   int64
	queueLatencies, localGitLatencies, gitHTTPLatencies, projectionLatencies               []time.Duration
	projectionReadLatencies                                                                []time.Duration
	projectionReadPeakLatencies                                                            []time.Duration
	projectionPolls                                                                        int64
	failureClasses                                                                         map[string]int64
	clockMaximumDrift                                                                      time.Duration
	timelines                                                                              []secretCapacityPushTimeline
}

const secretCapacityTimelineLimit = 2048

type secretCapacityPushTimeline struct {
	Sequence             int64         `json:"sequence"`
	Burst                int           `json:"burst"`
	Worker               int           `json:"worker"`
	OfferedAt            time.Time     `json:"offeredAt"`
	QueueWait            time.Duration `json:"queueWait"`
	ACK                  time.Duration `json:"ack"`
	WorkerOccupied       time.Duration `json:"workerOccupied"`
	LocalGit             time.Duration `json:"localGit"`
	GitHTTP              time.Duration `json:"gitHTTP"`
	ProjectionVisibility time.Duration `json:"projectionVisibility"`
	ProjectionRead       time.Duration `json:"projectionRead"`
	ProjectionReadPeak   time.Duration `json:"projectionReadPeak"`
	ProjectionPolls      int64         `json:"projectionPolls"`
	Acknowledged         bool          `json:"acknowledged"`
	ProjectionFailed     bool          `json:"projectionFailed"`
}

func (m *secretCapacityPushMeasurements) recordTimeline(row secretCapacityPushTimeline) {
	if row.Sequence > 0 && row.Sequence <= secretCapacityTimelineLimit && len(m.timelines) < secretCapacityTimelineLimit {
		m.timelines = append(m.timelines, row)
	}
}

func runSecretCapacityPushes(ctx context.Context, cfg secretCapacityLoadConfig,
	push func(context.Context, int, secretCapacityPushBatch) secretCapacityPushResult,
) (secretCapacityPushMeasurements, error) {
	var measured secretCapacityPushMeasurements
	if push == nil || cfg.duration <= 0 || cfg.duration > time.Hour || cfg.interval <= 0 ||
		cfg.burstInterval <= 0 || cfg.burstWindow <= 0 || cfg.burstWindow > cfg.burstInterval ||
		cfg.workers < 1 || cfg.workers > 32 || cfg.queueLimit < 1 || cfg.queueLimit > 256 ||
		cfg.burstSize < 1 || cfg.burstSize > 100 || cfg.drainTimeout <= 0 || cfg.drainTimeout > time.Minute {
		return measured, errors.New("secret capacity: invalid bounded push configuration")
	}
	sustainedCount := int64((cfg.duration-1)/cfg.interval + 1)
	burstCount := int((cfg.duration-1)/cfg.burstInterval + 1)
	if sustainedCount+int64(burstCount)*int64(cfg.burstSize) > 100_000 ||
		time.Duration(burstCount-1)*cfg.burstInterval+cfg.burstWindow > cfg.duration {
		return measured, errors.New("secret capacity: offered schedule exceeds bounds")
	}
	measured.bursts = make([]secretCapacityBurst, burstCount)
	workCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	queue := make(chan secretCapacityPushBatch, cfg.queueLimit)
	type completion struct {
		batch     secretCapacityPushBatch
		result    secretCapacityPushResult
		at        time.Time
		active    int
		worker    int
		queueWait time.Duration
	}
	results := make(chan completion, cfg.workers)
	var workers sync.WaitGroup
	var activity sync.Mutex
	active := 0
	for worker := range cfg.workers {
		workers.Add(1)
		go func(worker int) {
			defer workers.Done()
			for batch := range queue {
				queueWait := time.Since(batch.offeredAt)
				activity.Lock()
				active++
				concurrency := active
				activity.Unlock()
				result := push(workCtx, worker, batch)
				at := time.Now()
				activity.Lock()
				active--
				activity.Unlock()
				results <- completion{batch: batch, result: result, at: at, active: concurrency, worker: worker, queueWait: queueWait}
			}
		}(worker)
	}
	// Only the collector writes completed-operation observations. Schedule
	// observations below are merged after both sides have stopped.
	var completed secretCapacityPushMeasurements
	completed.timelines = make([]secretCapacityPushTimeline, 0, min(secretCapacityTimelineLimit, int(sustainedCount)+burstCount*cfg.burstSize))
	completed.failureClasses = map[string]int64{}
	burstCompleted := make([]time.Time, burstCount)
	collected := make(chan struct{})
	go func() {
		defer close(collected)
		for item := range results {
			r := item.result
			completed.recordTimeline(secretCapacityPushTimeline{
				Sequence: item.batch.sequence, Burst: item.batch.burst, Worker: item.worker,
				OfferedAt: item.batch.offeredAt.UTC(), QueueWait: item.queueWait, ACK: r.ackLatency,
				WorkerOccupied: item.at.Sub(item.batch.offeredAt) - item.queueWait,
				LocalGit:       r.localGitLatency, GitHTTP: r.gitHTTPLatency,
				ProjectionVisibility: r.projectionLatency, ProjectionRead: r.projectionReadLatency,
				ProjectionReadPeak: r.projectionReadPeakLatency, ProjectionPolls: r.projectionPolls,
				Acknowledged: r.acknowledged, ProjectionFailed: r.projectionFailed,
			})
			completed.completed++
			completed.queueLatencies = append(completed.queueLatencies, item.queueWait)
			if r.localGitLatency > 0 {
				completed.localGitLatencies = append(completed.localGitLatencies, r.localGitLatency)
			}
			if r.gitHTTPLatency > 0 {
				completed.gitHTTPLatencies = append(completed.gitHTTPLatencies, r.gitHTTPLatency)
			}
			if r.projectionLatency > 0 {
				completed.projectionLatencies = append(completed.projectionLatencies, r.projectionLatency)
			}
			if r.projectionReadLatency > 0 {
				completed.projectionReadLatencies = append(completed.projectionReadLatencies, r.projectionReadLatency)
			}
			if r.projectionReadPeakLatency > 0 {
				completed.projectionReadPeakLatencies = append(completed.projectionReadPeakLatencies, r.projectionReadPeakLatency)
			}
			completed.projectionPolls += r.projectionPolls
			if r.err != nil || !r.acknowledged {
				stage := r.failureStage
				if stage != "local_git" && stage != "git_http" {
					stage = "unclassified"
				}
				completed.failureClasses[stage+"/"+secretCapacityFailureClass(r.err)]++
			}
			if r.projectionFailed {
				class := secretCapacityFailureClass(r.projectionErr)
				if class == "unclassified" {
					class = "verification"
				}
				completed.failureClasses["projection/"+class]++
			}
			if r.resolutionFailed {
				completed.failureClasses["credential_resolution/provider"]++
			}
			completed.workersPeak = max(completed.workersPeak, item.active)
			completed.maxFiles = max(completed.maxFiles, r.files)
			completed.maxBytes = max(completed.maxBytes, r.bytes)
			completed.referenceLatencies = append(completed.referenceLatencies, r.referenceLatencies...)
			if r.resolutionLatency > 0 {
				completed.resolutionLatencies = append(completed.resolutionLatencies, r.resolutionLatency)
			}
			if r.resolutionFailed {
				completed.localHealthyFailures++
			}
			if r.projectionFailed {
				completed.projectionLoss++
			}
			completed.crossReplicaChecks += r.projectionChecks
			if r.gitAttempted {
				completed.gitPushes++
				completed.typedFiles += int64(r.files)
				latency := r.ackLatency
				if latency == 0 {
					latency = item.at.Sub(item.batch.offeredAt)
				}
				completed.pushLatencies = append(completed.pushLatencies, latency)
			}
			if r.err != nil || !r.acknowledged {
				completed.failed++
			} else {
				completed.acknowledged++
			}
			if item.batch.burst >= 0 && item.at.After(burstCompleted[item.batch.burst]) {
				burstCompleted[item.batch.burst] = item.at
			}
		}
	}()
	start := time.Now()
	observeClock := func(now time.Time) {
		drift := now.Round(0).Sub(start.Round(0)) - now.Sub(start)
		if drift < 0 {
			drift = -drift
		}
		measured.clockMaximumDrift = max(measured.clockMaximumDrift, drift)
	}
	var sequence, sustained, burstOffer int64
	burstLast := make([]time.Time, burstCount)
	offer := func(at time.Duration, burst int) bool {
		timer := time.NewTimer(max(time.Duration(0), time.Until(start.Add(at))))
		select {
		case <-ctx.Done():
			timer.Stop()
			return false
		case <-timer.C:
		}
		now := time.Now()
		observeClock(now)
		sequence++
		if now.Sub(start.Add(at)) > cfg.interval {
			measured.missedSchedules++
		}
		if burst < 0 {
			measured.sustainedOffered++
		} else {
			b := &measured.bursts[burst]
			if b.Offered == 0 {
				b.At = now.Sub(start)
			}
			b.Offered++
			b.OfferDuration = now.Sub(start.Add(b.At))
			burstLast[burst] = now
		}
		select {
		case queue <- secretCapacityPushBatch{sequence, burst, now}:
			measured.queuePeak = max(measured.queuePeak, len(queue))
		default:
			measured.dropped++
		}
		return true
	}
	for sustained < sustainedCount || burstOffer < int64(burstCount*cfg.burstSize) {
		sustainedAt := cfg.duration
		if sustained < sustainedCount {
			sustainedAt = time.Duration(sustained) * cfg.interval
		}
		burstAt := cfg.duration
		burst := int(burstOffer) / cfg.burstSize
		if burst < burstCount {
			burstAt = time.Duration(burst)*cfg.burstInterval +
				time.Duration(burstOffer%int64(cfg.burstSize))*cfg.burstWindow/time.Duration(cfg.burstSize)
		}
		if sustainedAt <= burstAt && sustained < sustainedCount {
			if !offer(sustainedAt, -1) {
				break
			}
			sustained++
		} else {
			if !offer(burstAt, burst) {
				break
			}
			burstOffer++
		}
	}
	timer := time.NewTimer(max(time.Duration(0), time.Until(start.Add(cfg.duration))))
	select {
	case <-ctx.Done():
		timer.Stop()
	case <-timer.C:
	}
	measured.offeredDuration = time.Since(start)
	close(queue)
	drainTimer := time.AfterFunc(cfg.drainTimeout, cancel)
	workers.Wait()
	drainTimer.Stop()
	close(results)
	<-collected
	observeClock(time.Now())
	measured.completed, measured.gitPushes, measured.acknowledged, measured.failed =
		completed.completed, completed.gitPushes, completed.acknowledged, completed.failed
	measured.workersPeak, measured.maxFiles, measured.maxBytes = completed.workersPeak, completed.maxFiles, completed.maxBytes
	measured.pushLatencies, measured.referenceLatencies = completed.pushLatencies, completed.referenceLatencies
	measured.resolutionLatencies, measured.localHealthyFailures = completed.resolutionLatencies, completed.localHealthyFailures
	measured.projectionLoss, measured.crossReplicaChecks, measured.typedFiles = completed.projectionLoss, completed.crossReplicaChecks, completed.typedFiles
	measured.queueLatencies, measured.localGitLatencies = completed.queueLatencies, completed.localGitLatencies
	measured.gitHTTPLatencies, measured.projectionLatencies = completed.gitHTTPLatencies, completed.projectionLatencies
	measured.projectionReadLatencies, measured.projectionPolls = completed.projectionReadLatencies, completed.projectionPolls
	measured.projectionReadPeakLatencies = completed.projectionReadPeakLatencies
	measured.failureClasses = completed.failureClasses
	measured.timelines = completed.timelines
	slowBurst := false
	for i := range measured.bursts {
		if !burstCompleted[i].IsZero() {
			measured.bursts[i].Drain = max(time.Duration(0), burstCompleted[i].Sub(burstLast[i]))
			slowBurst = slowBurst || measured.bursts[i].Drain > cfg.drainTimeout
		}
		slowBurst = slowBurst || measured.bursts[i].OfferDuration > cfg.burstWindow
	}
	if err := ctx.Err(); err != nil {
		return measured, err
	}
	unhealthy := measured.completed == 0 || float64(measured.failed)/float64(measured.completed) >= .001
	unhealthyResolution := len(measured.resolutionLatencies) > 0 &&
		float64(measured.localHealthyFailures)/float64(len(measured.resolutionLatencies)) >= .001
	if measured.dropped > 0 || unhealthy || unhealthyResolution || measured.projectionLoss > 0 ||
		measured.gitPushes != measured.completed || measured.missedSchedules > 0 || slowBurst || workCtx.Err() != nil ||
		measured.clockMaximumDrift > time.Second {
		return measured, errors.New("secret capacity: dropped, failed, late or undrained push workload")
	}
	return measured, nil
}

func secretCapacityWorkerFiles(worker int) []int {
	var files []int
	for file := worker * 100 / 32; file < (worker+1)*100/32; file++ {
		files = append(files, file)
	}
	return files
}

func secretCapacityFileName(runID string, file int) string {
	return fmt.Sprintf("scf-%s-%03d", runID, file)
}

type secretCapacityGitWorker struct {
	directory, namespace, runID, token string
	files                              []int
	tracked                            bool
}

func newSecretCapacityGitWorker(ctx context.Context, directory, remote, namespace, runID, token string, worker int) (*secretCapacityGitWorker, error) {
	if worker < 0 || worker >= 32 || !filepath.IsAbs(directory) {
		return nil, errors.New("secret capacity: invalid Git worker scope")
	}
	for _, name := range []string{namespace, secretCapacityFileName(runID, 99)} {
		if secretmaterial.ValidateSecretRef(secretmaterial.SecretRef{Kind: "SecretRef", Name: name}, "") != nil {
			return nil, errors.New("secret capacity: invalid namespace or File pool name")
		}
	}
	if _, err := secretCapacityGit(ctx, filepath.Dir(directory), token, "clone", "--quiet", "--no-tags", "--depth=1", "--", remote, directory); err != nil {
		return nil, err
	}
	for _, setting := range [][2]string{
		{"user.name", "GitStore Capacity"}, {"user.email", "capacity@gitstore.dev"},
		{"commit.gpgSign", "false"}, {"core.hooksPath", os.DevNull},
	} {
		if _, err := secretCapacityGit(ctx, directory, "", "config", setting[0], setting[1]); err != nil {
			return nil, err
		}
	}
	return &secretCapacityGitWorker{
		directory: directory, namespace: namespace, runID: runID, token: token,
		files: secretCapacityWorkerFiles(worker),
	}, nil
}

func (worker *secretCapacityGitWorker) push(ctx context.Context, batch secretCapacityPushBatch) (result secretCapacityPushResult) {
	localStarted := time.Now()
	result.failureStage = "local_git"
	defer func() {
		if !result.gitAttempted {
			result.localGitLatency = time.Since(localStarted)
		}
	}()
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if err := os.MkdirAll(filepath.Join(worker.directory, "files"), 0700); err != nil {
		result.err = errors.New("secret capacity: cannot create File directory")
		return result
	}
	paths := make([]string, 0, len(worker.files))
	for _, file := range worker.files {
		ref := secretmaterial.CredentialsRef{
			Kind: "CredentialsRef", Type: "aws-access-key/v1",
			SecretRef: secretmaterial.SecretRef{Kind: "SecretRef", Name: "capacity-media"},
		}
		start := time.Now()
		err := secretmaterial.ValidateCredentialsRef(ref, worker.namespace)
		result.referenceLatencies = append(result.referenceLatencies, time.Since(start))
		if err != nil {
			result.err = errors.New("secret capacity: invalid typed reference")
			return result
		}
		name := secretCapacityFileName(worker.runID, file)
		paths = append(paths, filepath.Join("files", name+".md"))
		data := []byte(fmt.Sprintf(`---
apiVersion: storage.gitstore.dev/v1beta1
kind: File
metadata:
  name: %s
  namespace: %s
  labels:
    secret-capacity-run: %s
  annotations:
    secret-capacity.gitstore.dev/sequence: "%d"
spec:
  contentType: image/jpeg
  type: gitstore.dev/media
  source:
    type: s3
    uri: s3://capacity-fixture/%s.jpg
    credentialsRef:
      kind: CredentialsRef
      type: aws-access-key/v1
      secretRef:
        kind: SecretRef
        name: capacity-media
---
Capacity metadata only; no payload operation is requested.
`, name, worker.namespace, worker.runID, batch.sequence, name))
		result.files++
		result.bytes += len(data)
		if result.files > 10 || result.bytes > 128*1024 {
			result.err = errors.New("secret capacity: File batch exceeds admission bounds")
			return result
		}
		if err := os.WriteFile(filepath.Join(worker.directory, "files", name+".md"), data, 0600); err != nil {
			result.err = errors.New("secret capacity: cannot write File manifest")
			return result
		}
	}
	if !worker.tracked {
		if _, err := secretCapacityGit(ctx, worker.directory, "", append([]string{"add", "--"}, paths...)...); err != nil {
			result.err = err
			return result
		}
	}
	args := append([]string{"commit", "--quiet", "--only", "-m", fmt.Sprintf("capacity File batch %d", batch.sequence), "--"}, paths...)
	if _, err := secretCapacityGit(ctx, worker.directory, "", args...); err != nil {
		result.err = err
		return result
	}
	worker.tracked = true
	result.gitAttempted = true
	result.localGitLatency = time.Since(localStarted)
	result.failureStage = "git_http"
	start := batch.offeredAt
	if start.IsZero() {
		start = time.Now()
	}
	httpStarted := time.Now()
	_, result.err = secretCapacityGit(ctx, worker.directory, worker.token, "push", "--quiet", "origin", "HEAD:refs/heads/main")
	result.gitHTTPLatency = time.Since(httpStarted)
	result.ackLatency = time.Since(start)
	result.acknowledged = result.err == nil
	return result
}

type secretCapacityGitOutput struct {
	buffer   bytes.Buffer
	overflow bool
	limit    int
}

func (output *secretCapacityGitOutput) Write(data []byte) (int, error) {
	size := len(data)
	limit := output.limit
	if limit == 0 {
		limit = 128 * 1024
	}
	remaining := limit - output.buffer.Len()
	if size > remaining {
		output.overflow = true
		data = data[:remaining]
	}
	_, _ = output.buffer.Write(data)
	return size, nil
}

type secretCapacityGitError struct {
	class string
	cause error
}

func (err *secretCapacityGitError) Error() string {
	return "secret capacity: Git command failed (" + err.class + ")"
}

func (err *secretCapacityGitError) Unwrap() error { return err.cause }

func secretCapacityFailureClass(err error) string {
	if errors.Is(err, context.DeadlineExceeded) {
		return "deadline"
	}
	if errors.Is(err, context.Canceled) {
		return "cancelled"
	}
	var gitErr *secretCapacityGitError
	if errors.As(err, &gitErr) {
		return gitErr.class
	}
	var statusErr *capacityHTTPStatusError
	if errors.As(err, &statusErr) {
		switch statusErr.status {
		case http.StatusTooManyRequests:
			return "quota"
		case http.StatusUnauthorized, http.StatusForbidden:
			return "authentication"
		default:
			return "transport"
		}
	}
	return "unclassified"
}

func classifySecretCapacityGitFailure(output []byte, overflow bool) string {
	if overflow {
		return "output_limit"
	}
	text := strings.ToLower(string(output))
	for _, classification := range []struct {
		class   string
		markers []string
	}{
		{"non_fast_forward", []string{"non-fast-forward", "(fetch first)"}},
		{"quota", []string{"returned error: 429", "http 429", "quota exceeded", "rate limit exceeded"}},
		{"authentication", []string{"authentication failed", "returned error: 401", "returned error: 403", "http 401", "http 403"}},
		{"rejection", []string{"[remote rejected]", "pre-receive hook declined"}},
		{"transport", []string{"could not resolve host", "failed to connect", "connection reset", "rpc failed", "returned error: 502", "returned error: 503", "returned error: 504"}},
	} {
		for _, marker := range classification.markers {
			if strings.Contains(text, marker) {
				return classification.class
			}
		}
	}
	return "command_failure"
}

func secretCapacityGit(ctx context.Context, directory, token string, args ...string) ([]byte, error) {
	return secretCapacityGitInspect(ctx, directory, token, nil, args...)
}

func secretCapacityGitInspect(ctx context.Context, directory, token string, inspect func([]byte), args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, "git", args...)
	command.Dir = directory
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	command.Cancel = func() error {
		err := syscall.Kill(-command.Process.Pid, syscall.SIGKILL)
		if errors.Is(err, syscall.ESRCH) {
			return os.ErrProcessDone
		}
		return err
	}
	command.WaitDelay = time.Second
	for _, variable := range os.Environ() {
		if !strings.HasPrefix(variable, "GIT_") {
			command.Env = append(command.Env, variable)
		}
	}
	command.Env = append(command.Env, "GIT_TERMINAL_PROMPT=0", "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL="+os.DevNull,
		"GIT_CONFIG_COUNT=1", "GIT_CONFIG_KEY_0=credential.helper", "GIT_CONFIG_VALUE_0=")
	if token != "" {
		// Git's command-scoped environment avoids argv and .git/config secrets.
		command.Env = append(command.Env, "GIT_CONFIG_COUNT=2", "GIT_CONFIG_KEY_1=http.extraHeader",
			"GIT_CONFIG_VALUE_1=Authorization: Bearer "+token)
	}
	var output secretCapacityGitOutput
	command.Stdout, command.Stderr = &output, &output
	err := command.Run()
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if inspect != nil && !output.overflow {
		inspect(output.buffer.Bytes())
	}
	if err != nil || output.overflow {
		return nil, &secretCapacityGitError{class: classifySecretCapacityGitFailure(output.buffer.Bytes(), output.overflow), cause: err}
	}
	return output.buffer.Bytes(), nil
}

func validateSecretCapacityGitEndpoint(endpoint string) error {
	parsed, err := url.Parse(endpoint)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" ||
		parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return errors.New("secret capacity: a credential-free HTTP Git endpoint is required")
	}
	return nil
}

type secretCapacityFileProjection struct {
	ID       string `json:"id"`
	Metadata struct {
		Name, Namespace string
		Annotations     map[string]string `json:"annotations"`
	} `json:"metadata"`
	Spec struct {
		ContentType string `json:"contentType"`
		Type        string `json:"type"`
		Source      struct {
			Type           string `json:"type"`
			URI            string `json:"uri"`
			CredentialsRef *struct {
				Kind      string `json:"kind"`
				Type      string `json:"type"`
				SecretRef struct {
					Kind      string  `json:"kind"`
					Name      string  `json:"name"`
					Key       *string `json:"key"`
					Namespace *string `json:"namespace"`
				} `json:"secretRef"`
			} `json:"credentialsRef"`
		} `json:"source"`
	} `json:"spec"`
}

const secretCapacityFileSelection = `id metadata { name namespace annotations }
	spec { contentType type source { type uri credentialsRef { kind type secretRef { kind name key namespace } } } }`

var errSecretCapacityFilePending = errors.New("secret capacity: seeded File projection not yet visible")

func secretCapacityWaitForFileIDs(ctx context.Context, endpoint, token, namespace, runID string) (map[string]string, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	for {
		ids, err := secretCapacityReadFileIDs(ctx, endpoint, token, namespace, runID)
		if !errors.Is(err, errSecretCapacityFilePending) {
			return ids, err
		}
		timer := time.NewTimer(200 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, fmt.Errorf("secret capacity: seeded File visibility deadline: %w", ctx.Err())
		case <-timer.C:
		}
	}
}

func secretCapacityReadFileIDs(ctx context.Context, endpoint, token, namespace, runID string) (map[string]string, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	client := &http.Client{Timeout: 5 * time.Second}
	defer client.CloseIdleConnections()
	ids := make(map[string]string, 100)
	seenIDs := make(map[string]bool, 100)
	for offset := 0; offset < 100; offset += 10 {
		var definitions, fields strings.Builder
		definitions.WriteString("$namespace:String!")
		variables := map[string]any{"namespace": namespace}
		for index := range 10 {
			fmt.Fprintf(&definitions, ",$name%d:String!", index)
			fmt.Fprintf(&fields, "file%d:file(namespace:$namespace,name:$name%d){%s}\n", index, index, secretCapacityFileSelection)
			variables[fmt.Sprintf("name%d", index)] = secretCapacityFileName(runID, offset+index)
		}
		query := "query(" + definitions.String() + "){" + fields.String() + "}"
		var data map[string]*secretCapacityFileProjection
		if err := secretCapacityGraphQL(ctx, client, endpoint, token, query, variables, &data); err != nil {
			return nil, fmt.Errorf("secret capacity: File pool lookup batch %d: %w", offset/10, err)
		}
		for index := range 10 {
			file, exists := data[fmt.Sprintf("file%d", index)]
			if !exists {
				return nil, errors.New("secret capacity: File lookup omitted a requested response field")
			}
			if file == nil {
				return nil, fmt.Errorf("%w at index %d", errSecretCapacityFilePending, offset+index)
			}
			name := secretCapacityFileName(runID, offset+index)
			if err := validateSecretCapacityFile(*file, namespace, name, 0); err != nil {
				return nil, fmt.Errorf("secret capacity: File pool projection at index %d: %w", offset+index, err)
			}
			if seenIDs[file.ID] {
				return nil, errors.New("secret capacity: File pool contains duplicate identities")
			}
			seenIDs[file.ID] = true
			ids[name] = file.ID
		}
	}
	return ids, nil
}

func validateSecretCapacityFile(file secretCapacityFileProjection, namespace, name string, sequence int64) error {
	ref := file.Spec.Source.CredentialsRef
	if ref == nil {
		return errors.New("secret capacity: acknowledged File is missing its credentials reference")
	}
	authoredRef := secretmaterial.CredentialsRef{
		Kind: ref.Kind, Type: ref.Type,
		SecretRef: secretmaterial.SecretRef{Kind: ref.SecretRef.Kind, Name: ref.SecretRef.Name, Key: ref.SecretRef.Key, Namespace: ref.SecretRef.Namespace},
	}
	if file.ID == "" || file.Metadata.Namespace != namespace || file.Metadata.Name != name ||
		file.Metadata.Annotations["secret-capacity.gitstore.dev/sequence"] != strconv.FormatInt(sequence, 10) ||
		file.Spec.ContentType != "image/jpeg" || file.Spec.Type != "gitstore.dev/media" ||
		file.Spec.Source.Type != "s3" || file.Spec.Source.URI != "s3://capacity-fixture/"+name+".jpg" ||
		secretmaterial.ValidateCredentialsRef(authoredRef, namespace) != nil ||
		ref.Type != "aws-access-key/v1" || ref.SecretRef.Name != "capacity-media" ||
		ref.SecretRef.Key != nil || ref.SecretRef.Namespace != nil {
		return errors.New("secret capacity: acknowledged File projection missing or changed")
	}
	return nil
}

func secretCapacityGraphQL(ctx context.Context, client *http.Client, endpoint, token, query string, variables map[string]any, destination any) error {
	return secretCapacityGraphQLBounded(ctx, client, endpoint, token, query, variables, destination, 1024*1024)
}

func secretCapacityGraphQLBounded(ctx context.Context, client *http.Client, endpoint, token, query string, variables map[string]any, destination any, limit int64) error {
	invalid := errors.New("secret capacity: GraphQL operation failed")
	if limit <= 0 || limit > 8*1024*1024 {
		return invalid
	}
	data, err := json.Marshal(gqlRequest{Query: query, Variables: variables})
	if err != nil {
		return invalid
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint+"/graphql", bytes.NewReader(data))
	if err != nil {
		return invalid
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer "+token)
	response, err := client.Do(request)
	if err != nil {
		if ctx.Err() != nil {
			return fmt.Errorf("secret capacity: GraphQL request canceled: %w", ctx.Err())
		}
		if errors.Is(err, context.DeadlineExceeded) {
			return errors.New("secret capacity: GraphQL request timed out")
		}
		return errors.New("secret capacity: GraphQL transport failed")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return &capacityHTTPStatusError{
			status: response.StatusCode, body: "secret capacity: GraphQL operation failed",
			retryAfter: capacityRetryAfter(response.Header.Get("Retry-After"), time.Now()),
		}
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, limit+1))
	if err != nil || int64(len(body)) > limit {
		return errors.New("secret capacity: GraphQL response unreadable or exceeds byte limit")
	}
	var envelope gqlResponse
	if json.Unmarshal(body, &envelope) != nil {
		return errors.New("secret capacity: malformed GraphQL response")
	}
	if len(envelope.Errors) != 0 {
		return fmt.Errorf("secret capacity: GraphQL returned %d operation errors (details redacted)", len(envelope.Errors))
	}
	if len(envelope.Data) == 0 || bytes.Equal(bytes.TrimSpace(envelope.Data), []byte("null")) || json.Unmarshal(envelope.Data, destination) != nil {
		return errors.New("secret capacity: missing or malformed GraphQL data")
	}
	return nil
}

func secretCapacityVerifyFileBatch(ctx context.Context, client *http.Client, endpoints []string, token string,
	worker *secretCapacityGitWorker, ids map[string]string, batch secretCapacityPushBatch,
) error {
	if len(endpoints) != 2 || endpoints[0] == endpoints[1] || token == "" {
		return errors.New("secret capacity: File verification requires two API endpoints and authorized credentials")
	}
	idsForBatch := make([]string, 0, len(worker.files))
	for _, file := range worker.files {
		id := ids[secretCapacityFileName(worker.runID, file)]
		if id == "" {
			return errors.New("secret capacity: File ID is missing from the established pool")
		}
		idsForBatch = append(idsForBatch, id)
	}
	for _, endpoint := range endpoints {
		for {
			var data struct {
				Nodes []*secretCapacityFileProjection `json:"nodes"`
			}
			err := secretCapacityGraphQL(ctx, client, endpoint, token,
				`query($ids:[ID!]!) { nodes(ids:$ids) { ... on File { `+secretCapacityFileSelection+` } } }`,
				map[string]any{"ids": idsForBatch}, &data)
			if err != nil {
				return err
			}
			valid := err == nil && len(data.Nodes) == len(worker.files)
			if valid {
				for i, file := range data.Nodes {
					if file == nil || file.ID != idsForBatch[i] ||
						validateSecretCapacityFile(*file, worker.namespace, secretCapacityFileName(worker.runID, worker.files[i]), batch.sequence) != nil {
						valid = false
						break
					}
				}
			}
			if valid {
				break
			}
			select {
			case <-ctx.Done():
				return fmt.Errorf("secret capacity: File projection did not converge: %w", ctx.Err())
			case <-time.After(50 * time.Millisecond):
			}
		}
	}
	return nil
}

type secretCapacityRuntime struct {
	resolver *secretmaterial.Resolver
	provider *secretmaterial.FileProvider
	ref      secretmaterial.CredentialsRef
	request  secretmaterial.ResolutionRequest
	markers  []string
}

func newSecretCapacityRuntime(directory, namespace string) (*secretCapacityRuntime, error) {
	if !filepath.IsAbs(directory) || secretmaterial.ValidateSecretRef(secretmaterial.SecretRef{Kind: "SecretRef", Name: namespace}, "") != nil {
		return nil, errors.New("secret capacity: invalid owned runtime provider scope")
	}
	recordDir := filepath.Join(directory, "capacity", namespace)
	if err := os.MkdirAll(recordDir, 0700); err != nil {
		return nil, errors.New("secret capacity: cannot create owned runtime provider directory")
	}
	access, secret := make([]byte, 32), make([]byte, 32)
	if _, err := rand.Read(access); err != nil {
		return nil, errors.New("secret capacity: cannot generate runtime fixture")
	}
	if _, err := rand.Read(secret); err != nil {
		clear(access)
		return nil, errors.New("secret capacity: cannot generate runtime fixture")
	}
	defer clear(access)
	defer clear(secret)
	markers := []string{base64.RawURLEncoding.EncodeToString(access), base64.RawURLEncoding.EncodeToString(secret)}
	record, err := json.Marshal(map[string]any{
		"format": "secret-record/v1",
		"values": map[string]string{
			"accessKeyId":     base64.StdEncoding.EncodeToString([]byte(markers[0])),
			"secretAccessKey": base64.StdEncoding.EncodeToString([]byte(markers[1])),
		},
	})
	if err != nil {
		return nil, errors.New("secret capacity: cannot encode runtime fixture")
	}
	defer clear(record)
	if err := os.WriteFile(filepath.Join(recordDir, "capacity-media.json"), record, 0600); err != nil {
		return nil, errors.New("secret capacity: cannot write owned runtime provider record")
	}
	provider, err := secretmaterial.NewFileProvider(directory, secretmaterial.FormatJSONRecord)
	if err != nil {
		return nil, err
	}
	resolver, err := secretmaterial.NewRuntimeResolver(provider, secretCapacityRuntimeBinding(namespace), nil)
	if err != nil {
		return nil, errors.Join(err, provider.Close())
	}
	return &secretCapacityRuntime{
		resolver: resolver, provider: provider, markers: markers,
		ref: secretmaterial.CredentialsRef{Kind: "CredentialsRef", Type: "aws-access-key/v1", SecretRef: secretmaterial.SecretRef{Kind: "SecretRef", Name: "capacity-media"}},
		request: secretmaterial.ResolutionRequest{Principal: "capacity-contract",
			Resource: secretmaterial.ResourceIdentity{Kind: "File", Namespace: namespace, Repository: "capacity", Name: "capacity-file"}},
	}, nil
}

func secretCapacityRuntimeBinding(namespace string) secretmaterial.RuntimeBinding {
	return secretmaterial.RuntimeBinding{Environment: "capacity", Namespace: namespace,
		Authorize: func(_ context.Context, request secretmaterial.ResolutionRequest, ref secretmaterial.SecretRef) error {
			if request.Principal != "capacity-contract" || ref.Name != "capacity-media" {
				return secretmaterial.ErrForbidden
			}
			return nil
		}}
}

func (runtime *secretCapacityRuntime) resolve(ctx context.Context) (time.Duration, error) {
	start := time.Now()
	material, err := runtime.resolver.ResolveCredentials(ctx, runtime.ref, runtime.request)
	material.Clear()
	return time.Since(start), err
}

type secretCapacityHeldProvider struct {
	secretmaterial.Provider
	entered chan struct{}
	release chan struct{}
	calls   atomic.Int64
	active  atomic.Int64
	peak    atomic.Int64
}

func (p *secretCapacityHeldProvider) Read(ctx context.Context, ref secretmaterial.SecretRef, scope secretmaterial.Scope) ([]byte, error) {
	p.calls.Add(1)
	active := p.active.Add(1)
	defer p.active.Add(-1)
	for peak := p.peak.Load(); active > peak && !p.peak.CompareAndSwap(peak, active); peak = p.peak.Load() {
	}
	select {
	case p.entered <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	select {
	case <-p.release:
		return p.Provider.Read(ctx, ref, scope)
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

type secretCapacityRuntimeProof struct {
	callers, peak                                     int
	saturationDenied, deadlineDenied                  int64
	namespaceDenied, bindingDenied, unsupportedDenied int64
	providerCalls, acceptedCalls                      int64
}

func (runtime *secretCapacityRuntime) proveBounds(ctx context.Context) (secretCapacityRuntimeProof, error) {
	var proof secretCapacityRuntimeProof
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	held := &secretCapacityHeldProvider{Provider: runtime.provider, entered: make(chan struct{}, 16), release: make(chan struct{})}
	resolver, err := secretmaterial.NewRuntimeResolver(held, secretCapacityRuntimeBinding(runtime.request.Resource.Namespace), nil)
	if err != nil {
		return proof, err
	}
	results := make(chan error, 16)
	for range 16 {
		proof.callers++
		go func() {
			material, err := resolver.ResolveCredentials(ctx, runtime.ref, runtime.request)
			material.Clear()
			results <- err
		}()
	}
	ready := true
	for range 16 {
		select {
		case <-held.entered:
		case <-ctx.Done():
			ready = false
		}
	}
	// The first 16 calls stay inside the provider while the other 16 callers
	// must fail immediately, rather than entering an internal waiting queue.
	overflow := make(chan error, 16)
	if ready {
		for range 16 {
			proof.callers++
			go func() {
				material, err := resolver.ResolveCredentials(ctx, runtime.ref, runtime.request)
				material.Clear()
				overflow <- err
			}()
		}
		for range 16 {
			if errors.Is(<-overflow, secretmaterial.ErrProviderUnavailable) {
				proof.saturationDenied++
			}
		}
	}
	close(held.release)
	for range 16 {
		if <-results == nil {
			proof.acceptedCalls++
		}
	}
	proof.peak, proof.providerCalls = int(held.peak.Load()), held.calls.Load()
	if !ready || proof.saturationDenied != 16 || proof.acceptedCalls != 16 || proof.providerCalls != 16 || proof.peak != 16 {
		return proof, errors.New("secret capacity: runtime provider contention, queue or retry contract failed")
	}
	request := runtime.request
	request.Resource.Namespace = "other-namespace"
	material, err := resolver.ResolveCredentials(ctx, runtime.ref, request)
	material.Clear()
	if errors.Is(err, secretmaterial.ErrForbidden) {
		proof.namespaceDenied++
	}
	request = runtime.request
	request.Principal = "unauthorized"
	material, err = resolver.ResolveCredentials(ctx, runtime.ref, request)
	material.Clear()
	if errors.Is(err, secretmaterial.ErrForbidden) {
		proof.bindingDenied++
	}
	unsupported := runtime.ref
	unsupported.Type = "unsupported/v1"
	material, err = resolver.ResolveCredentials(ctx, unsupported, runtime.request)
	material.Clear()
	if errors.Is(err, secretmaterial.ErrUnsupportedType) {
		proof.unsupportedDenied++
	}
	if proof.namespaceDenied != 1 || proof.bindingDenied != 1 || proof.unsupportedDenied != 1 || held.calls.Load() != 16 {
		return proof, errors.New("secret capacity: runtime authorization/type denial reached the provider")
	}
	deadlineProvider := &secretCapacityHeldProvider{Provider: runtime.provider, entered: make(chan struct{}, 1), release: make(chan struct{})}
	deadlineResolver, err := secretmaterial.NewRuntimeResolver(deadlineProvider, secretCapacityRuntimeBinding(runtime.request.Resource.Namespace), nil)
	if err != nil {
		return proof, err
	}
	short, stop := context.WithTimeout(ctx, 20*time.Millisecond)
	defer stop()
	start := time.Now()
	material, err = deadlineResolver.ResolveCredentials(short, runtime.ref, runtime.request)
	material.Clear()
	if errors.Is(err, context.DeadlineExceeded) && time.Since(start) < time.Second &&
		deadlineProvider.calls.Load() == 1 && deadlineProvider.active.Load() == 0 {
		proof.deadlineDenied++
	}
	if proof.deadlineDenied != 1 {
		return proof, errors.New("secret capacity: earlier caller deadline was not respected")
	}
	return proof, nil
}

func TestSecretCapacityRealRuntimeResolutionAndContention(t *testing.T) {
	runtime, err := newSecretCapacityRuntime(t.TempDir(), "capacity-test")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, runtime.provider.Close()) })
	duration, err := runtime.resolve(t.Context())
	require.NoError(t, err)
	require.Greater(t, duration, time.Duration(0))
	proof, err := runtime.proveBounds(t.Context())
	require.NoError(t, err)
	require.EqualValues(t, 16, proof.peak)
	require.EqualValues(t, 16, proof.saturationDenied)
	require.EqualValues(t, 1, proof.deadlineDenied)
	require.Equal(t, proof.acceptedCalls, proof.providerCalls)
}

type secretCapacityFileWorkload struct {
	workers      []*secretCapacityGitWorker
	runtime      *secretCapacityRuntime
	runtimeProof secretCapacityRuntimeProof
	dataset      secretCapacityDatasetObservations
	ids          map[string]string
	endpoints    []string
	token        string
}

func prepareSecretCapacityFileWorkload(t *testing.T, cfg repositoryCapacityConfig, poolPrefix, runID string) *secretCapacityFileWorkload {
	t.Helper()
	require.GreaterOrEqual(t, cfg.resourcePool, 32, "secret capacity needs 32 distinct authoring repositories on the singleton Git service")
	require.NoError(t, validateSecretCapacityGitEndpoint(gitURL))
	workload := &secretCapacityFileWorkload{
		endpoints: []string{cfg.apiA, cfg.apiB}, token: cfg.token,
	}
	root := t.TempDir()
	var err error
	workload.runtime, err = newSecretCapacityRuntime(filepath.Join(root, "runtime"), cfg.namespace)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, workload.runtime.provider.Close()) })
	workload.runtimeProof, err = workload.runtime.proveBounds(t.Context())
	require.NoError(t, err)
	for index := range 32 {
		remote := fmt.Sprintf("%s/%s/%s%d.git", gitURL, cfg.namespace, poolPrefix, index+1)
		worker, err := newSecretCapacityGitWorker(t.Context(), filepath.Join(root, "worker-"+strconv.Itoa(index)),
			remote, cfg.namespace, runID, cfg.token, index)
		require.NoError(t, err)
		seed := worker.push(t.Context(), secretCapacityPushBatch{sequence: 0})
		require.NoError(t, seed.err, "seed typed File pool before baseline")
		require.True(t, seed.acknowledged)
		workload.workers = append(workload.workers, worker)
	}
	workload.ids, err = secretCapacityWaitForFileIDs(t.Context(), cfg.apiA, cfg.token, cfg.namespace, runID)
	require.NoError(t, err)
	peerIDs, err := secretCapacityWaitForFileIDs(t.Context(), cfg.apiB, cfg.token, cfg.namespace, runID)
	require.NoError(t, err)
	require.Equal(t, workload.ids, peerIDs, "both APIs must project identical File identities before load")
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	client := &http.Client{Timeout: 5 * time.Second}
	defer client.CloseIdleConnections()
	for _, worker := range workload.workers {
		require.NoError(t, secretCapacityVerifyFileBatch(ctx, client, workload.endpoints, workload.token,
			worker, workload.ids, secretCapacityPushBatch{sequence: 0}),
			"verify seeded File projections through generic nodes on both APIs before load")
	}
	return workload
}

type secretCapacityFileLoadResult struct {
	measurements secretCapacityPushMeasurements
	err          error
}

type secretCapacityProjectionRequest struct {
	ctx    context.Context
	worker *secretCapacityGitWorker
	batch  secretCapacityPushBatch
	reply  chan error
	timing *secretCapacityProjectionTiming
}

type secretCapacityProjectionTiming struct {
	readNanos atomic.Int64
	peakNanos atomic.Int64
	polls     atomic.Int64
}

func runSecretCapacityProjectionVerifier(ctx context.Context, client *http.Client, endpoints []string, token string,
	ids map[string]string, requests <-chan secretCapacityProjectionRequest,
) {
	pending := make([]secretCapacityProjectionRequest, 0, 32)
	finish := func(err error) {
		for _, request := range pending {
			request.reply <- err
		}
		pending = pending[:0]
	}
	enqueue := func(request secretCapacityProjectionRequest) {
		if err := request.ctx.Err(); err != nil {
			request.reply <- err
			return
		}
		if request.worker == nil || len(request.worker.files) == 0 {
			request.reply <- errors.New("secret capacity: projection verification requires a nonempty File batch")
			return
		}
		if len(pending) == 32 {
			request.reply <- errors.New("secret capacity: projection verification queue exhausted")
			return
		}
		pending = append(pending, request)
	}
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			finish(ctx.Err())
			return
		case request, open := <-requests:
			if !open {
				finish(errors.New("secret capacity: projection verification request channel closed"))
				return
			}
			enqueue(request)
		case <-ticker.C:
			active := pending[:0]
			for _, request := range pending {
				if err := request.ctx.Err(); err != nil {
					request.reply <- err
				} else {
					active = append(active, request)
				}
			}
			pending = active
		drain:
			for range 32 {
				select {
				case request, open := <-requests:
					if !open {
						finish(errors.New("secret capacity: projection verification request channel closed"))
						return
					}
					enqueue(request)
				default:
					break drain
				}
			}
			if len(pending) == 0 {
				continue
			}
			if len(endpoints) != 2 || endpoints[0] == endpoints[1] || token == "" {
				finish(errors.New("secret capacity: File verification requires two API endpoints and authorized credentials"))
				continue
			}
			selected := make([]string, 0, 100)
			seen := make(map[string]bool)
			var selectionErr error
			for _, request := range pending {
				for _, file := range request.worker.files {
					id := ids[secretCapacityFileName(request.worker.runID, file)]
					if id == "" {
						selectionErr = errors.New("secret capacity: File ID is missing from the established pool")
					} else if !seen[id] {
						seen[id] = true
						selected = append(selected, id)
					}
				}
			}
			if selectionErr != nil {
				finish(selectionErr)
				continue
			}
			if len(selected) == 0 || len(selected) > 250 {
				finish(errors.New("secret capacity: projection verification batch exceeds ID bounds"))
				continue
			}
			replicas := make([]map[string]*secretCapacityFileProjection, 2)
			readErrors := make([]error, 2)
			readStarted := time.Now()
			var reads sync.WaitGroup
			for index, endpoint := range endpoints {
				reads.Go(func() {
					var data struct {
						Nodes []*secretCapacityFileProjection `json:"nodes"`
					}
					readErrors[index] = secretCapacityGraphQL(ctx, client, endpoint, token,
						`query($ids:[ID!]!) { nodes(ids:$ids) { ... on File { `+secretCapacityFileSelection+` } } }`,
						map[string]any{"ids": selected}, &data)
					if readErrors[index] != nil {
						return
					}
					if len(data.Nodes) != len(selected) {
						readErrors[index] = errors.New("secret capacity: projection verification response has incorrect cardinality")
						return
					}
					projected := make(map[string]*secretCapacityFileProjection, len(selected))
					for i, node := range data.Nodes {
						if node != nil {
							if node.ID != selected[i] {
								readErrors[index] = errors.New("secret capacity: projection verification response changed File identity")
								return
							}
							projected[node.ID] = node
						}
					}
					replicas[index] = projected
				})
			}
			reads.Wait()
			readElapsed := time.Since(readStarted)
			for _, request := range pending {
				if request.timing != nil {
					request.timing.readNanos.Add(int64(readElapsed))
					request.timing.peakNanos.Store(max(request.timing.peakNanos.Load(), int64(readElapsed)))
					request.timing.polls.Add(1)
				}
			}
			if readErr := errors.Join(readErrors...); readErr != nil {
				finish(readErr)
				continue
			}
			active = pending[:0]
			for _, request := range pending {
				if err := request.ctx.Err(); err != nil {
					request.reply <- err
					continue
				}
				verified := true
				for _, file := range request.worker.files {
					name := secretCapacityFileName(request.worker.runID, file)
					for _, replica := range replicas {
						node := replica[ids[name]]
						if node == nil || validateSecretCapacityFile(*node, request.worker.namespace, name, request.batch.sequence) != nil {
							verified = false
						}
					}
				}
				if verified {
					request.reply <- nil
				} else {
					active = append(active, request)
				}
			}
			pending = active
		}
	}
}

func (workload *secretCapacityFileWorkload) run(ctx context.Context, duration time.Duration) secretCapacityFileLoadResult {
	client := &http.Client{Timeout: 5 * time.Second}
	defer client.CloseIdleConnections()
	cfg := secretCapacityProductionLoadConfig()
	cfg.duration = duration
	verificationCtx, stopVerification := context.WithCancel(ctx)
	requests := make(chan secretCapacityProjectionRequest, cfg.workers)
	verificationDone := make(chan struct{})
	go func() {
		defer close(verificationDone)
		runSecretCapacityProjectionVerifier(verificationCtx, client, workload.endpoints, workload.token, workload.ids, requests)
	}()
	defer func() {
		stopVerification()
		<-verificationDone
	}()
	measurements, err := runSecretCapacityPushes(ctx, cfg, func(ctx context.Context, index int, batch secretCapacityPushBatch) secretCapacityPushResult {
		ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		resolution, resolutionErr := workload.runtime.resolve(ctx)
		worker := workload.workers[index]
		result := worker.push(ctx, batch)
		result.resolutionLatency, result.resolutionFailed = resolution, resolutionErr != nil
		if result.acknowledged {
			projectionStarted := time.Now()
			timing := &secretCapacityProjectionTiming{}
			request := secretCapacityProjectionRequest{ctx: ctx, worker: worker, batch: batch, reply: make(chan error, 1), timing: timing}
			var projectionErr error
			select {
			case requests <- request:
				select {
				case projectionErr = <-request.reply:
				case <-ctx.Done():
					projectionErr = ctx.Err()
				}
			case <-ctx.Done():
				projectionErr = ctx.Err()
			}
			result.projectionLatency = time.Since(projectionStarted)
			result.projectionReadLatency = time.Duration(timing.readNanos.Load())
			result.projectionReadPeakLatency = time.Duration(timing.peakNanos.Load())
			result.projectionPolls = timing.polls.Load()
			result.projectionFailed = projectionErr != nil
			result.projectionErr = projectionErr
			if projectionErr == nil {
				result.projectionChecks = int64(len(workload.endpoints))
			}
		}
		return result
	})
	return secretCapacityFileLoadResult{measurements, err}
}

func startSecretCapacityFileLoad(t *testing.T, workload *secretCapacityFileWorkload, duration time.Duration, mode capacityMode) <-chan secretCapacityFileLoadResult {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	result := make(chan secretCapacityFileLoadResult, 1)
	go func() {
		defer close(done)
		completed := workload.run(ctx, duration)
		writeErr := writeSecretCapacityFileObservations(os.Getenv("CAPACITY_EVIDENCE_DIR"), os.Getenv("CAPACITY_RUN_ID"),
			mode, duration, workload.runtimeProof, completed)
		completed.err = errors.Join(completed.err, writeErr)
		result <- completed
	}()
	t.Cleanup(func() {
		cancel()
		<-done
	})
	return result
}

func assertSecretCapacityFileLoad(t *testing.T, result secretCapacityFileLoadResult) {
	t.Helper()
	require.NoError(t, result.err, "real File workload failed; cannot substitute Repository metadata evidence")
	m := result.measurements
	require.Zero(t, m.projectionLoss)
	require.Zero(t, m.dropped)
	require.GreaterOrEqual(t, m.crossReplicaChecks, m.acknowledged*2)
	require.NotEmpty(t, m.referenceLatencies)
	require.NotEmpty(t, m.resolutionLatencies)
	require.NotEmpty(t, m.pushLatencies)
	require.LessOrEqual(t, percentileDuration(m.referenceLatencies, .95), 5*time.Millisecond)
	require.LessOrEqual(t, percentileDuration(m.referenceLatencies, .99), 20*time.Millisecond)
	require.LessOrEqual(t, percentileDuration(m.resolutionLatencies, .95), 10*time.Millisecond)
	require.LessOrEqual(t, percentileDuration(m.resolutionLatencies, .99), 50*time.Millisecond)
	require.LessOrEqual(t, percentileDuration(m.pushLatencies, .95), 2*time.Second)
	require.LessOrEqual(t, percentileDuration(m.pushLatencies, .99), 30*time.Second)
}

func secretCapacityRequested() bool {
	return os.Getenv("REPOSITORY_CAPACITY_SECRET_SCENARIO") == "1"
}

func waitForSecretCapacityControllerRecovery(t *testing.T, client *http.Client, cfg repositoryCapacityConfig) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Minute)
	defer cancel()
	for {
		var samples [2]secretCapacityControllerSample
		for i, endpoint := range []string{cfg.controllerA, cfg.controllerB} {
			var err error
			samples[i], err = sampleSecretCapacityController(ctx, client, endpoint)
			require.NoError(t, err, "observe controller recovery before baseline")
		}
		require.NoError(t, writeSecretCapacityComponent(os.Getenv("CAPACITY_EVIDENCE_DIR"), "controllers-recovery.json", struct {
			Controllers [2]secretCapacityControllerSample `json:"controllers"`
		}{samples}))
		for i, sample := range samples {
			require.True(t, sample.Healthy && sample.CredentialReady,
				"controller %d recovery is unhealthy or unauthenticated: %+v", i+1, sample.Kinds)
		}
		if samples[0].Ready && samples[1].Ready {
			return
		}
		timer := time.NewTimer(2 * time.Second)
		select {
		case <-ctx.Done():
			timer.Stop()
			t.Fatalf("controllers did not complete recovery before baseline: %v", ctx.Err())
		case <-timer.C:
		}
	}
}

func recordSecretCapacityControllers(t *testing.T, client *http.Client, cfg repositoryCapacityConfig, component string) [2]secretCapacityControllerSample {
	t.Helper()
	var samples [2]secretCapacityControllerSample
	for i, endpoint := range []string{cfg.controllerA, cfg.controllerB} {
		var err error
		samples[i], err = sampleSecretCapacityController(t.Context(), client, endpoint)
		require.NoError(t, err, "collect process-identified controller authentication and progress")
	}
	require.NotEqual(t, samples[0].ID, samples[1].ID, "controller samples must identify distinct replicas")
	runID := os.Getenv("CAPACITY_RUN_ID")
	require.NotEmpty(t, runID)
	observation := struct {
		SchemaVersion int                               `json:"schemaVersion"`
		Component     string                            `json:"component"`
		RunID         string                            `json:"runID"`
		Mode          capacityMode                      `json:"mode"`
		Controllers   [2]secretCapacityControllerSample `json:"controllers"`
	}{1, "secret-controller-snapshot/v1", runID, cfg.mode, samples}
	require.NoError(t, writeSecretCapacityComponent(os.Getenv("CAPACITY_EVIDENCE_DIR"), component, observation))
	for i, sample := range samples {
		require.True(t, sample.Healthy && sample.CredentialReady,
			"controller %d must be healthy and authenticated outside fault windows (healthy=%t credentialReady=%t)", i+1, sample.Healthy, sample.CredentialReady)
		require.True(t, sample.Ready, "controller %d must finish list/watch recovery before load or acceptance: %+v", i+1, sample.Kinds)
		require.Positive(t, sample.FreshTokens)
		require.Positive(t, sample.Reconciliations)
	}
	return samples
}

// This is a component observation, never a passing scenario envelope. The
// final verifier must still combine dataset, process and scheduled fault proof.
func writeSecretCapacityFileObservations(directory, runID string, mode capacityMode, nominal time.Duration,
	proof secretCapacityRuntimeProof, result secretCapacityFileLoadResult,
) error {
	if !filepath.IsAbs(directory) || runID == "" {
		return errors.New("secret capacity: component observations require an absolute evidence directory and run ID")
	}
	m := result.measurements
	totalOffered := m.sustainedOffered
	for _, burst := range m.bursts {
		totalOffered += burst.Offered
	}
	latency := func(values []time.Duration) secretCapacityLatency {
		if len(values) == 0 {
			return secretCapacityLatency{}
		}
		return secretCapacityLatency{int64(len(values)), percentileDuration(values, .95), percentileDuration(values, .99)}
	}
	observations := secretCapacityFileObservations{
		SchemaVersion: 1, RunID: runID, Component: "secret-file-workload/v1", Mode: mode,
		WorkloadCompleted: result.err == nil, NominalDuration: nominal, OfferedDuration: m.offeredDuration,
		SustainedOffered: m.sustainedOffered, MissedSchedules: m.missedSchedules, Completed: m.completed,
		GitPushes: m.gitPushes, Acknowledged: m.acknowledged, Failed: m.failed, Dropped: m.dropped,
		TypedFiles: m.typedFiles, QueuePeak: m.queuePeak, WorkersPeak: m.workersPeak,
		MaxFiles: m.maxFiles, MaxBytes: m.maxBytes, ProjectionLoss: m.projectionLoss,
		CrossReplicaChecks: m.crossReplicaChecks, LocalHealthyFailures: m.localHealthyFailures, ProjectionPolls: m.projectionPolls,
		Bursts: m.bursts, Reference: latency(m.referenceLatencies), Resolution: latency(m.resolutionLatencies), Push: latency(m.pushLatencies),
		RuntimeCallers: proof.callers, ProviderPeak: proof.peak, ProviderCalls: proof.providerCalls, AcceptedCalls: proof.acceptedCalls,
		SaturationDenied: proof.saturationDenied, DeadlineDenied: proof.deadlineDenied, NamespaceDenied: proof.namespaceDenied,
		BindingDenied: proof.bindingDenied, UnsupportedDenied: proof.unsupportedDenied,
		Stages: map[string]secretCapacityLatency{
			"queue_wait": latency(m.queueLatencies), "local_git": latency(m.localGitLatencies),
			"git_http": latency(m.gitHTTPLatencies), "projection_visibility": latency(m.projectionLatencies),
			"projection_read":      latency(m.projectionReadLatencies),
			"projection_read_peak": latency(m.projectionReadPeakLatencies),
		},
		FailureClasses:   m.failureClasses,
		ScheduledOffered: totalOffered, QueueAdmitted: totalOffered - m.dropped, ExecutionAttempts: m.completed,
		ClockMaximumDrift: m.clockMaximumDrift,
		Timelines:         m.timelines,
		TimelineLimit:     secretCapacityTimelineLimit,
	}
	if observations.FailureClasses == nil {
		observations.FailureClasses = map[string]int64{}
	}
	return writeSecretCapacityComponent(directory, "file-workload.json", observations)
}

type secretCapacityFileObservations struct {
	Timelines            []secretCapacityPushTimeline     `json:"timelines"`
	TimelineLimit        int                              `json:"timelineLimit"`
	ClockMaximumDrift    time.Duration                    `json:"clockMaximumDrift"`
	ScheduledOffered     int64                            `json:"scheduledOffered"`
	QueueAdmitted        int64                            `json:"queueAdmitted"`
	ExecutionAttempts    int64                            `json:"executionAttempts"`
	Stages               map[string]secretCapacityLatency `json:"stages"`
	FailureClasses       map[string]int64                 `json:"failureClasses"`
	SchemaVersion        int                              `json:"schemaVersion"`
	RunID                string                           `json:"runID"`
	Component            string                           `json:"component"`
	Mode                 capacityMode                     `json:"mode"`
	WorkloadCompleted    bool                             `json:"workloadCompleted"`
	NominalDuration      time.Duration                    `json:"nominalDuration"`
	OfferedDuration      time.Duration                    `json:"offeredDuration"`
	SustainedOffered     int64                            `json:"sustainedOffered"`
	MissedSchedules      int64                            `json:"missedSchedules"`
	Completed            int64                            `json:"completed"`
	GitPushes            int64                            `json:"gitPushes"`
	Acknowledged         int64                            `json:"acknowledged"`
	Failed               int64                            `json:"failed"`
	Dropped              int64                            `json:"dropped"`
	TypedFiles           int64                            `json:"typedFiles"`
	QueuePeak            int                              `json:"queuePeak"`
	WorkersPeak          int                              `json:"workersPeak"`
	MaxFiles             int                              `json:"maxFilesPerPush"`
	MaxBytes             int                              `json:"maxBytesPerPush"`
	ProjectionLoss       int64                            `json:"projectionLoss"`
	ProjectionPolls      int64                            `json:"projectionPolls"`
	CrossReplicaChecks   int64                            `json:"crossReplicaChecks"`
	LocalHealthyFailures int64                            `json:"localHealthyFailures"`
	Bursts               []secretCapacityBurst            `json:"bursts"`
	Reference            secretCapacityLatency            `json:"reference"`
	Resolution           secretCapacityLatency            `json:"resolution"`
	Push                 secretCapacityLatency            `json:"push"`
	RuntimeCallers       int                              `json:"runtimeCallers"`
	ProviderPeak         int                              `json:"providerPeak"`
	ProviderCalls        int64                            `json:"providerCalls"`
	AcceptedCalls        int64                            `json:"acceptedCalls"`
	SaturationDenied     int64                            `json:"saturationDenied"`
	DeadlineDenied       int64                            `json:"deadlineDenied"`
	NamespaceDenied      int64                            `json:"namespaceDenied"`
	BindingDenied        int64                            `json:"bindingDenied"`
	UnsupportedDenied    int64                            `json:"unsupportedDenied"`
}

func writeSecretCapacityComponent(directory, name string, observations any) error {
	if !filepath.IsAbs(directory) || filepath.Base(name) != name || filepath.Ext(name) != ".json" {
		return errors.New("secret capacity: invalid component evidence destination")
	}
	componentDir := filepath.Join(directory, "secret")
	if err := os.MkdirAll(componentDir, 0700); err != nil {
		return errors.New("secret capacity: cannot create component evidence directory")
	}
	file, err := os.CreateTemp(componentDir, ".component-*")
	if err != nil {
		return errors.New("secret capacity: cannot create component evidence")
	}
	defer os.Remove(file.Name())
	encodeErr := json.NewEncoder(file).Encode(observations)
	syncErr := file.Sync()
	closeErr := file.Close()
	if encodeErr != nil || syncErr != nil || closeErr != nil ||
		os.Rename(file.Name(), filepath.Join(componentDir, name)) != nil {
		return errors.New("secret capacity: cannot persist component observations")
	}
	return nil
}

const secretCapacityMaxDatasetRows = 10_000_000
const secretCapacityMaxManifestBytes = 8 * 1024 * 1024 * 1024

type secretCapacityProductAcknowledgment struct {
	Namespace    string `json:"namespace"`
	Name         string `json:"name"`
	Title        string `json:"title"`
	Revision     string `json:"revision"`
	Acknowledged bool   `json:"acknowledged"`
}

// A count plus a 256-bit modular sum of domain-separated row hashes allows
// comparison of differently ordered API pages without retaining millions of
// names. The acknowledgment stream separately rejects duplicate names.
type secretCapacityProductSet [4]uint64

func (set *secretCapacityProductSet) add(row secretCapacityProductAcknowledgment) {
	hash := sha256.New()
	_, _ = io.WriteString(hash, "gitstore-capacity-product/v1")
	var length [8]byte
	for _, value := range []string{row.Namespace, row.Name, row.Title, row.Revision} {
		binary.BigEndian.PutUint64(length[:], uint64(len(value)))
		_, _ = hash.Write(length[:])
		_, _ = io.WriteString(hash, value)
	}
	var digest [32]byte
	hash.Sum(digest[:0])
	var carry uint64
	for i := range set {
		set[i], carry = bits.Add64(set[i], binary.LittleEndian.Uint64(digest[i*8:(i+1)*8]), carry)
	}
}

func (set secretCapacityProductSet) String() string {
	var encoded [32]byte
	for i, value := range set {
		binary.LittleEndian.PutUint64(encoded[i*8:(i+1)*8], value)
	}
	return hex.EncodeToString(encoded[:])
}

type secretCapacityAcknowledgmentProof struct {
	rows, bytes int64
	set         secretCapacityProductSet
	fileSHA256  string
}

var secretCapacityCommitRevision = regexp.MustCompile(`^([^[:space:]@\x00]+@sha1:)?[0-9a-f]{40}$`)

func validSecretCapacityProduct(row secretCapacityProductAcknowledgment, namespace string) bool {
	return row.Namespace == namespace && row.Name != "" && len(row.Name) <= 253 &&
		!strings.ContainsAny(row.Name, "/\x00\r\n\t") && strings.TrimSpace(row.Title) != "" &&
		secretCapacityCommitRevision.MatchString(row.Revision)
}

func readSecretCapacityAcknowledgments(ctx context.Context, path, namespace string) (secretCapacityAcknowledgmentProof, error) {
	var proof secretCapacityAcknowledgmentProof
	invalid := errors.New("secret capacity: invalid, unordered or unacknowledged Product fixture manifest")
	if !filepath.IsAbs(path) || secretmaterial.ValidateSecretRef(secretmaterial.SecretRef{Kind: "SecretRef", Name: namespace}, "") != nil {
		return proof, invalid
	}
	root, err := os.OpenRoot(filepath.Dir(path))
	if err != nil {
		return proof, invalid
	}
	defer root.Close()
	file, err := openSecretCapacityArtifact(root, filepath.Base(path))
	if err != nil {
		return proof, invalid
	}
	defer file.Close()
	before, err := file.Stat()
	if err != nil || before.Size() <= 0 || before.Size() > secretCapacityMaxManifestBytes {
		return proof, invalid
	}
	hash := sha256.New()
	scanner := bufio.NewScanner(io.TeeReader(io.LimitReader(file, secretCapacityMaxManifestBytes+1), hash))
	scanner.Buffer(make([]byte, 4096), 4097)
	lastName := ""
	for scanner.Scan() {
		if err := ctx.Err(); err != nil {
			return secretCapacityAcknowledgmentProof{}, err
		}
		line := scanner.Bytes()
		var row secretCapacityProductAcknowledgment
		if len(line) > 4096 || !utf8.Valid(line) || decodeSecretCapacityJSON(line, &row) != nil ||
			!validSecretCapacityProduct(row, namespace) || !row.Acknowledged || row.Name <= lastName {
			return secretCapacityAcknowledgmentProof{}, invalid
		}
		proof.rows++
		if proof.rows > secretCapacityMaxDatasetRows {
			return secretCapacityAcknowledgmentProof{}, invalid
		}
		lastName = row.Name
		proof.set.add(row)
	}
	after, statErr := file.Stat()
	if scanner.Err() != nil || statErr != nil || proof.rows == 0 ||
		before.Size() != after.Size() || !before.ModTime().Equal(after.ModTime()) {
		return secretCapacityAcknowledgmentProof{}, invalid
	}
	proof.bytes, proof.fileSHA256 = before.Size(), hex.EncodeToString(hash.Sum(nil))
	return proof, nil
}

type secretCapacityDatasetReplica struct {
	Role        string `json:"role"`
	Rows        int64  `json:"rows"`
	Pages       int64  `json:"pages"`
	MaxPageRows int64  `json:"maxPageRows"`
	SetDigest   string `json:"setDigest"`
}

type secretCapacityDatasetObservations struct {
	SchemaVersion  int                            `json:"schemaVersion"`
	Component      string                         `json:"component"`
	RunID          string                         `json:"runID"`
	Mode           capacityMode                   `json:"mode"`
	Proof          secretCapacityDatasetProof     `json:"proof"`
	ManifestSHA256 string                         `json:"manifestSHA256"`
	ManifestBytes  int64                          `json:"manifestBytes"`
	SetDigest      string                         `json:"setDigest"`
	Replicas       []secretCapacityDatasetReplica `json:"replicas"`
	VerifiedAt     time.Time                      `json:"verifiedAt"`
	Elapsed        time.Duration                  `json:"elapsed"`
}

const secretCapacityDatasetMaxPageSize = 250

func verifySecretCapacityDataset(ctx context.Context, client *http.Client, endpoints []string, token, namespace, manifest string,
	pageSize int, mode capacityMode,
) (secretCapacityDatasetObservations, error) {
	var observation secretCapacityDatasetObservations
	invalid := errors.New("secret capacity: incomplete or inconsistent offline Product dataset")
	if err := ctx.Err(); err != nil {
		return observation, err
	}
	if pageSize < 1 || pageSize > secretCapacityDatasetMaxPageSize {
		return observation, fmt.Errorf("secret capacity: dataset page size must be between 1 and %d", secretCapacityDatasetMaxPageSize)
	}
	if len(endpoints) != 2 || endpoints[0] == endpoints[1] || token == "" ||
		(mode != capacityModeDiagnostic && mode != capacityModeAlpha && mode != capacityModeProduction) {
		return observation, invalid
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Minute)
	defer cancel()
	started := time.Now()
	acknowledged, err := readSecretCapacityAcknowledgments(ctx, manifest, namespace)
	if err != nil {
		return observation, err
	}
	if mode == capacityModeProduction && acknowledged.rows < 5_000_000 {
		return observation, errors.New("secret capacity: production requires five million acknowledged titled Product fixtures")
	}
	observation = secretCapacityDatasetObservations{
		SchemaVersion: 1, Component: "secret-dataset/v1", Mode: mode,
		ManifestSHA256: acknowledged.fileSHA256, ManifestBytes: acknowledged.bytes, SetDigest: acknowledged.set.String(),
	}
	for replica, endpoint := range endpoints {
		proof := secretCapacityDatasetReplica{Role: []string{"api_a", "api_b"}[replica]}
		var set secretCapacityProductSet
		var after *string
		for {
			if err := ctx.Err(); err != nil {
				return secretCapacityDatasetObservations{}, err
			}
			var data struct {
				Products struct {
					Edges []struct {
						Cursor string `json:"cursor"`
						Node   *struct {
							Metadata struct {
								Namespace string  `json:"namespace"`
								Name      string  `json:"name"`
								Revision  *string `json:"revision"`
							} `json:"metadata"`
							Spec struct {
								Title *string `json:"title"`
							} `json:"spec"`
						} `json:"node"`
					} `json:"edges"`
					PageInfo *struct {
						HasNextPage *bool   `json:"hasNextPage"`
						EndCursor   *string `json:"endCursor"`
					} `json:"pageInfo"`
				} `json:"products"`
			}
			var err error
			for attempt := 0; attempt < 8; attempt++ {
				err = secretCapacityGraphQLBounded(ctx, client, endpoint, token, `query($namespace:String!,$first:Int!,$after:String) {
				products(namespace:$namespace,first:$first,after:$after) {
					edges { cursor node { metadata { namespace name revision } spec { title } } }
					pageInfo { hasNextPage endCursor }
				}
			}`, map[string]any{"namespace": namespace, "first": pageSize, "after": after}, &data, 8*1024*1024)
				var statusErr *capacityHTTPStatusError
				if !errors.As(err, &statusErr) || statusErr.status != http.StatusTooManyRequests || attempt == 7 {
					break
				}
				delay := max(statusErr.retryAfter, min(100*time.Millisecond*time.Duration(1<<attempt), 5*time.Second))
				timer := time.NewTimer(delay)
				select {
				case <-ctx.Done():
					timer.Stop()
					return secretCapacityDatasetObservations{}, ctx.Err()
				case <-timer.C:
				}
			}
			if err != nil {
				if ctx.Err() != nil {
					return secretCapacityDatasetObservations{}, ctx.Err()
				}
				return secretCapacityDatasetObservations{}, fmt.Errorf("%w: %s page %d request: %w", invalid, proof.Role, proof.Pages+1, err)
			}
			page := data.Products
			if len(page.Edges) == 0 || len(page.Edges) > pageSize || page.PageInfo == nil ||
				page.PageInfo.HasNextPage == nil || page.PageInfo.EndCursor == nil {
				return secretCapacityDatasetObservations{}, invalid
			}
			proof.Pages++
			proof.MaxPageRows = max(proof.MaxPageRows, int64(len(page.Edges)))
			cursors := make(map[string]bool, len(page.Edges))
			for _, edge := range page.Edges {
				if edge.Node == nil || edge.Node.Spec.Title == nil || edge.Node.Metadata.Revision == nil ||
					edge.Cursor == "" || len(edge.Cursor) > 4096 || cursors[edge.Cursor] {
					return secretCapacityDatasetObservations{}, invalid
				}
				cursors[edge.Cursor] = true
				row := secretCapacityProductAcknowledgment{Namespace: edge.Node.Metadata.Namespace, Name: edge.Node.Metadata.Name,
					Title: *edge.Node.Spec.Title, Revision: *edge.Node.Metadata.Revision}
				if !validSecretCapacityProduct(row, namespace) {
					return secretCapacityDatasetObservations{}, invalid
				}
				set.add(row)
				proof.Rows++
			}
			if proof.Rows > acknowledged.rows || *page.PageInfo.EndCursor != page.Edges[len(page.Edges)-1].Cursor ||
				(after != nil && *page.PageInfo.EndCursor == *after) {
				return secretCapacityDatasetObservations{}, invalid
			}
			if !*page.PageInfo.HasNextPage {
				break
			}
			if proof.Rows == acknowledged.rows {
				return secretCapacityDatasetObservations{}, invalid
			}
			after = page.PageInfo.EndCursor
		}
		if proof.Rows != acknowledged.rows || set != acknowledged.set {
			return secretCapacityDatasetObservations{}, invalid
		}
		proof.SetDigest = set.String()
		observation.Replicas = append(observation.Replicas, proof)
	}
	primary := observation.Replicas[0]
	observation.Proof = secretCapacityDatasetProof{
		Method: "offline-paginated", Rows: primary.Rows, Titled: primary.Rows, Acknowledged: acknowledged.rows,
		Pages: primary.Pages, PageLimit: int64(pageSize), MaxPageRows: primary.MaxPageRows, CompletedBeforeLoad: true,
	}
	observation.VerifiedAt, observation.Elapsed = time.Now().UTC(), time.Since(started)
	return observation, nil
}

type secretCapacityResourceSnapshot struct {
	Role        string    `json:"role"`
	ID          string    `json:"id"`
	CPUSeconds  float64   `json:"cpuSeconds"`
	CPUCapacity float64   `json:"cpuCapacity"`
	RSS         uint64    `json:"rss"`
	Goroutines  int64     `json:"goroutines"`
	ObservedAt  time.Time `json:"observedAt"`
}

type secretCapacityResourceObservations struct {
	SchemaVersion             int                              `json:"schemaVersion"`
	Component                 string                           `json:"component"`
	RunID                     string                           `json:"runID"`
	Mode                      capacityMode                     `json:"mode"`
	Processes                 []secretCapacityProcessProof     `json:"processes"`
	Before                    []secretCapacityResourceSnapshot `json:"before"`
	LoadEnd                   []secretCapacityResourceSnapshot `json:"loadEnd"`
	Stabilized                []secretCapacityResourceSnapshot `json:"stabilized"`
	Baseline                  time.Duration                    `json:"baseline"`
	PostLoad                  time.Duration                    `json:"postLoad"`
	FilePool                  int                              `json:"filePool"`
	Workers                   int                              `json:"workers"`
	ControllersBefore         []secretCapacityControllerSample `json:"controllersBefore"`
	ControllersAfter          []secretCapacityControllerSample `json:"controllersAfter"`
	RepositoryLifecyclePassed bool                             `json:"repositoryLifecyclePassed"`
}

type secretCapacityLogWriter struct {
	file     *os.File
	written  int64
	overflow bool
}

func (writer *secretCapacityLogWriter) Write(data []byte) (int, error) {
	length := len(data)
	remaining := int64(secretCapacityArtifactLimit) - writer.written
	if int64(length) > remaining {
		writer.overflow = true
		data = data[:remaining]
	}
	n, err := writer.file.Write(data)
	writer.written += int64(n)
	if err != nil {
		return n, err
	}
	return length, nil
}

type secretCapacityLogStream struct {
	cancel context.CancelFunc
	done   <-chan error
}

type secretCapacityLogs struct {
	streams   map[string]secretCapacityLogStream
	directory string
	stopped   bool
}

func (logs *secretCapacityLogs) capture(ctx context.Context, samples []secretCapacityResourceSnapshot) error {
	services := []string{"api-a", "api-b", "controller-manager-a", "controller-manager-b", "capacity-git-service"}
	if len(samples) != len(services) || logs.stopped {
		return errors.New("secret capacity: invalid log capture scope")
	}
	for i, sample := range samples {
		if _, exists := logs.streams[sample.ID]; exists {
			continue
		}
		if len(logs.streams) >= 7 || !secretCapacityControllerID.MatchString(sample.ID) {
			return errors.New("secret capacity: unexpected process incarnation in log capture")
		}
		commandCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
		name, err := secretCapacityServiceContainer(commandCtx, os.Getenv("REPOSITORY_CAPACITY_PROJECT"), services[i])
		if err != nil {
			cancel()
			return err
		}
		inspection, err := secretCapacityOwnedCommand(commandCtx, "docker", "inspect", "--format",
			"{{.Id}} {{.State.StartedAt}} {{.State.Running}}", name)
		if err != nil {
			cancel()
			return err
		}
		fields := strings.Fields(string(inspection))
		if len(fields) != 3 || fields[2] != "true" {
			cancel()
			return errors.New("secret capacity: log source is not running")
		}
		build, err := secretCapacityOwnedCommand(commandCtx, "docker", "inspect", "--format",
			`{"containerID":{{json .Id}},"imageID":{{json .Image}},"imageReference":{{json .Config.Image}},"revision":{{json (index .Config.Labels "org.opencontainers.image.revision")}},"executable":{{json .Path}}}`, fields[0])
		if err != nil {
			cancel()
			return err
		}
		var provenance map[string]string
		if json.Unmarshal(build, &provenance) != nil || provenance["containerID"] != fields[0] ||
			!strings.HasPrefix(provenance["imageID"], "sha256:") {
			cancel()
			return errors.New("secret capacity: invalid process build provenance")
		}
		provenance["processID"] = sample.ID
		if err := writeSecretCapacityPrivateJSON(filepath.Join(logs.directory, sample.ID+"-build.json"), provenance); err != nil {
			cancel()
			return err
		}
		if i == 4 {
			digest := sha256.Sum256(inspection)
			if sample.ID != "git-"+hex.EncodeToString(digest[:]) {
				cancel()
				return errors.New("secret capacity: Git log source changed")
			}
		} else {
			port := "4000"
			if i >= 2 {
				port = "5001"
			}
			body, err := secretCapacityOwnedCommand(commandCtx, "docker", "exec", fields[0],
				"wget", "-qO-", "http://127.0.0.1:"+port+"/metrics")
			if err != nil {
				cancel()
				return err
			}
			metrics, err := parseCapacityMetrics(body)
			if err != nil || metrics.instanceID != sample.ID {
				cancel()
				return errors.New("secret capacity: endpoint and container log identities differ")
			}
		}
		cancel()
		file, err := os.OpenFile(filepath.Join(logs.directory, sample.ID+".log"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err != nil {
			return errors.New("secret capacity: cannot create process log artifact")
		}
		logCtx, cancel := context.WithCancel(ctx)
		command := exec.CommandContext(logCtx, "docker", "logs", "--follow", "--timestamps", "--since", fields[1], fields[0])
		writer := &secretCapacityLogWriter{file: file}
		command.Stdout, command.Stderr = writer, writer
		if err := command.Start(); err != nil {
			cancel()
			_ = file.Close()
			return errors.New("secret capacity: cannot start process log capture")
		}
		done := make(chan error, 1)
		go func() {
			waitErr := command.Wait()
			syncErr, closeErr := file.Sync(), file.Close()
			if (waitErr != nil && logCtx.Err() == nil) || syncErr != nil || closeErr != nil || writer.overflow {
				done <- errors.New("secret capacity: process log capture failed or exceeded artifact bounds")
			} else {
				done <- nil
			}
		}()
		logs.streams[sample.ID] = secretCapacityLogStream{cancel: cancel, done: done}
	}
	return nil
}

func (logs *secretCapacityLogs) stop() error {
	if logs.stopped {
		return nil
	}
	logs.stopped = true
	for _, stream := range logs.streams {
		stream.cancel()
	}
	var failures []error
	for _, stream := range logs.streams {
		failures = append(failures, <-stream.done)
	}
	return errors.Join(failures...)
}

func secretCapacityCPUSetSize(value string) (int, error) {
	total, previous := 0, -1
	for _, span := range strings.Split(value, ",") {
		parts := strings.Split(span, "-")
		if len(parts) > 2 {
			return 0, errors.New("invalid CPU set")
		}
		start, err := strconv.Atoi(parts[0])
		if err != nil || start <= previous || start < 0 {
			return 0, errors.New("invalid CPU set")
		}
		end := start
		if len(parts) == 2 {
			end, err = strconv.Atoi(parts[1])
			if err != nil || end < start {
				return 0, errors.New("invalid CPU set")
			}
		}
		if end > 4095 {
			return 0, errors.New("CPU set exceeds bounds")
		}
		total += end - start + 1
		previous = end
	}
	return total, nil
}

func sampleSecretCapacityGit(ctx context.Context) (secretCapacityResourceSnapshot, []byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	name, err := secretCapacityServiceContainer(ctx, os.Getenv("REPOSITORY_CAPACITY_PROJECT"), "capacity-git-service")
	if err != nil {
		return secretCapacityResourceSnapshot{}, nil, err
	}
	before, err := secretCapacityOwnedCommand(ctx, "docker", "inspect", "--format",
		"{{.Id}} {{.State.StartedAt}} {{.State.Running}}", name)
	if err != nil {
		return secretCapacityResourceSnapshot{}, nil, err
	}
	identity := strings.Fields(string(before))
	if len(identity) != 3 || identity[2] != "true" {
		return secretCapacityResourceSnapshot{}, nil, errors.New("secret capacity: Git process is not running")
	}
	body, err := secretCapacityOwnedCommand(ctx, "docker", "exec", name, "sh", "-ec",
		`printf 'cpu '; awk '$1 == "usage_usec" {print $2}' /sys/fs/cgroup/cpu.stat
printf 'rss '; awk '$1 == "VmRSS:" {print $2}' /proc/1/status
printf 'limit '; cat /sys/fs/cgroup/cpu.max
printf 'cpus '; cat /sys/fs/cgroup/cpuset.cpus.effective`)
	if err != nil {
		return secretCapacityResourceSnapshot{}, nil, err
	}
	after, err := secretCapacityOwnedCommand(ctx, "docker", "inspect", "--format",
		"{{.Id}} {{.State.StartedAt}} {{.State.Running}}", name)
	if err != nil || !bytes.Equal(before, after) {
		return secretCapacityResourceSnapshot{}, nil, errors.New("secret capacity: Git process changed during resource sampling")
	}
	values := make(map[string][]string)
	for _, line := range strings.Split(strings.TrimSpace(string(body)), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 || values[fields[0]] != nil {
			return secretCapacityResourceSnapshot{}, nil, errors.New("secret capacity: malformed Git resource sample")
		}
		values[fields[0]] = fields[1:]
	}
	if len(values) != 4 || len(values["cpu"]) != 1 || len(values["rss"]) != 1 ||
		len(values["limit"]) != 2 || len(values["cpus"]) != 1 {
		return secretCapacityResourceSnapshot{}, nil, errors.New("secret capacity: missing Linux cgroup-v2 Git resource counters")
	}
	cpu, cpuErr := strconv.ParseUint(values["cpu"][0], 10, 64)
	rss, rssErr := strconv.ParseUint(values["rss"][0], 10, 64)
	cpus, cpusErr := secretCapacityCPUSetSize(values["cpus"][0])
	if cpuErr != nil || rssErr != nil || cpusErr != nil || rss == 0 || rss > math.MaxUint64/1024 || cpus == 0 {
		return secretCapacityResourceSnapshot{}, nil, errors.New("secret capacity: invalid Git resource counters")
	}
	capacity := float64(cpus)
	if values["limit"][0] != "max" {
		quota, quotaErr := strconv.ParseUint(values["limit"][0], 10, 64)
		period, periodErr := strconv.ParseUint(values["limit"][1], 10, 64)
		if quotaErr != nil || periodErr != nil || quota == 0 || period == 0 {
			return secretCapacityResourceSnapshot{}, nil, errors.New("secret capacity: invalid Git CPU quota")
		}
		capacity = min(capacity, float64(quota)/float64(period))
	}
	digest := sha256.Sum256(before)
	return secretCapacityResourceSnapshot{Role: "git", ID: "git-" + hex.EncodeToString(digest[:]),
		CPUSeconds: float64(cpu) / 1e6, CPUCapacity: capacity, RSS: rss * 1024, ObservedAt: time.Now()}, body, nil
}

func captureSecretCapacityResources(t *testing.T, cfg repositoryCapacityConfig, phase string) []secretCapacityResourceSnapshot {
	t.Helper()
	client := &http.Client{Timeout: 5 * time.Second}
	defer client.CloseIdleConnections()
	directory := filepath.Join(os.Getenv("CAPACITY_EVIDENCE_DIR"), "secret")
	require.NoError(t, os.MkdirAll(directory, 0700))
	var result []secretCapacityResourceSnapshot
	for i, endpoint := range []string{cfg.apiA, cfg.apiB, cfg.controllerA, cfg.controllerB} {
		var body []byte
		metrics, err := fetchCapacityMetricsObserved(client, endpoint, func(data []byte) error {
			if !secretCapacityScanBlock(data, nil) {
				return errors.New("secret capacity: process metrics exposed credential material")
			}
			body = data
			return nil
		})
		require.NoError(t, err, "collect actual process resource counters")
		require.True(t, secretCapacityControllerID.MatchString(metrics.instanceID))
		require.Positive(t, metrics.resident)
		require.Positive(t, metrics.goroutines)
		require.Less(t, metrics.resident, float64(1<<53))
		require.Less(t, metrics.goroutines, float64(1<<53))
		require.Equal(t, math.Trunc(metrics.goroutines), metrics.goroutines)
		role := "api"
		if i >= 2 {
			role = "controller"
		}
		result = append(result, secretCapacityResourceSnapshot{Role: role, ID: metrics.instanceID,
			CPUSeconds: metrics.cpu, CPUCapacity: metrics.gomaxprocs, RSS: uint64(metrics.resident), Goroutines: int64(metrics.goroutines), ObservedAt: time.Now()})
		require.NoError(t, os.WriteFile(filepath.Join(directory, metrics.instanceID+"-"+phase+".metrics"), body, 0600))
	}
	git, body, err := sampleSecretCapacityGit(t.Context())
	require.NoError(t, err)
	require.True(t, secretCapacityScanBlock(body, nil))
	require.NoError(t, os.WriteFile(filepath.Join(directory, git.ID+"-"+phase+".metrics"), body, 0600))
	return append(result, git)
}

// The existing lifecycle gate compares both release-process segments with the
// original warmed baseline, not the replacement's artificially small startup heap.
func secretCapacityResourceProofs(before, loadEnd, stabilized []secretCapacityResourceSnapshot,
	started time.Time, apiRecovery capacityRecoveryResult, faults secretCapacityFaultObservations,
) ([]secretCapacityProcessProof, error) {
	if len(before) != 5 || len(loadEnd) != 5 || len(stabilized) != 5 || started.IsZero() {
		return nil, errors.New("secret capacity: incomplete resource sampling")
	}
	result := make([]secretCapacityProcessProof, 0, 5)
	for i, initial := range before {
		end, stable := loadEnd[i], stabilized[i]
		if initial.Role != end.Role || initial.Role != stable.Role || stable.ID != end.ID ||
			initial.RSS == 0 || stable.RSS == 0 || initial.CPUCapacity <= 0 || end.CPUCapacity != stable.CPUCapacity ||
			!end.ObservedAt.After(initial.ObservedAt) || !stable.ObservedAt.After(end.ObservedAt) {
			return nil, errors.New("secret capacity: invalid resource sample identity or baseline")
		}
		normalized := (end.CPUSeconds - initial.CPUSeconds) / initial.CPUCapacity
		seconds := end.ObservedAt.Sub(initial.ObservedAt).Seconds()
		peakRSS := stable.RSS
		samples := int64(2)
		if initial.ID != end.ID {
			switch i {
			case 1:
				if apiRecovery.label != "api_b" || apiRecovery.beforeStop.instanceID != initial.ID || apiRecovery.afterStart.instanceID != end.ID ||
					apiRecovery.afterStart.gomaxprocs != end.CPUCapacity {
					return nil, errors.New("secret capacity: API resource replacement lacks matching lifecycle proof")
				}
				normalized = (apiRecovery.beforeStop.cpu-initial.CPUSeconds)/initial.CPUCapacity +
					(end.CPUSeconds-apiRecovery.afterStart.cpu)/apiRecovery.afterStart.gomaxprocs
				seconds = started.Add(apiRecovery.beforeStopAt).Sub(initial.ObservedAt).Seconds() +
					end.ObservedAt.Sub(started.Add(apiRecovery.afterStartAt)).Seconds()
				peakRSS = max(peakRSS, uint64(apiRecovery.beforeStop.resident))
			case 2:
				old, replacement := faults.RestartBefore, faults.Replacement
				if old.ID != initial.ID || replacement.ID != end.ID || !faults.RestartConfirmed || float64(replacement.GOMAXPROCS) != end.CPUCapacity {
					return nil, errors.New("secret capacity: controller resource replacement lacks matching fault proof")
				}
				normalized = (old.CPUSeconds-initial.CPUSeconds)/initial.CPUCapacity +
					(end.CPUSeconds-replacement.CPUSeconds)/float64(replacement.GOMAXPROCS)
				seconds = old.ObservedAt.Sub(initial.ObservedAt).Seconds() + end.ObservedAt.Sub(replacement.ObservedAt).Seconds()
				peakRSS = max(peakRSS, uint64(old.RSS))
			default:
				return nil, errors.New("secret capacity: unplanned resource process replacement")
			}
			samples = 4
		} else if initial.CPUCapacity != end.CPUCapacity {
			return nil, errors.New("secret capacity: CPU capacity changed without process replacement")
		}
		if normalized < 0 || seconds <= 0 || math.IsNaN(normalized) || math.IsInf(normalized, 0) {
			return nil, errors.New("secret capacity: resource counters regressed or CPU capacity changed")
		}
		result = append(result, secretCapacityProcessProof{Role: initial.Role, ID: initial.ID, Samples: samples,
			Cpu: normalized / seconds, RssBefore: initial.RSS, RssAfter: peakRSS,
			GoroutinesBefore: initial.Goroutines, GoroutinesAfter: stable.Goroutines})
	}
	return result, nil
}

func prepareSecretCapacityDataset(t *testing.T, cfg repositoryCapacityConfig) secretCapacityDatasetObservations {
	t.Helper()
	client := &http.Client{Timeout: 20 * time.Second}
	defer client.CloseIdleConnections()
	proof, err := verifySecretCapacityDataset(t.Context(), client, []string{cfg.apiA, cfg.apiB}, cfg.token,
		getEnv("REPOSITORY_CAPACITY_SECRET_DATASET_NAMESPACE", cfg.namespace),
		os.Getenv("REPOSITORY_CAPACITY_SECRET_DATASET_MANIFEST"),
		capacityEnvInt(t, "REPOSITORY_CAPACITY_SECRET_DATASET_PAGE_SIZE", secretCapacityDatasetMaxPageSize), cfg.mode)
	require.NoError(t, err, "verify acknowledged titled Products offline before any offered load")
	proof.RunID = os.Getenv("CAPACITY_RUN_ID")
	require.NotEmpty(t, proof.RunID)
	require.NoError(t, writeSecretCapacityComponent(os.Getenv("CAPACITY_EVIDENCE_DIR"), "dataset.json", proof))
	return proof
}
