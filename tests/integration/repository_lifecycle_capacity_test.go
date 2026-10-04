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
}

type repositoryCapacityEvent struct {
	Type            string `json:"type"`
	Name            string `json:"name"`
	ResourceVersion string `json:"resourceVersion"`
	Repository      *struct {
		Metadata struct {
			Annotations map[string]any `json:"annotations"`
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
	produced, admitted, failed, backpressured int64
	acknowledged                              *capacityBitset
	maxSequence                               int
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

	var dataset secretCapacityDatasetObservations
	var secretFaultDriver *secretCapacityFaultDriver
	if secretCapacityRequested() {
		secretFaultDriver = prepareSecretCapacityFaultDriver(t, cfg)
		dataset = prepareSecretCapacityDataset(t, cfg)
	}
	ensureRepositoryCapacityNamespace(t, cfg)
	runID := strconv.FormatInt(time.Now().UnixNano(), 36)
	poolPrefix := "rcp-" + runID + "-"
	createRepositoryCapacityPool(t, client, cfg, poolPrefix)
	assertRepositoryCrossReplicaRead(t, cfg, poolPrefix+"1")
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
	if len(response.Errors) == 0 && strings.Contains(string(response.Data), `"name":"`+cfg.namespace+`"`) {
		return
	}
	createNamespaceThrough(t, cfg.apiA, cfg.token, cfg.namespace)
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
	result := repositoryCapacityLoadResult{acknowledged: newCapacityBitset(maxTransitions + 1)}
	jobs := make(chan int, repositoryCapacityQueueDepth(cfg.mutationWorkers, cfg.burstSize))
	locks := make([]sync.Mutex, cfg.resourcePool)
	var wg sync.WaitGroup
	var produced, admitted, failed, backpressured atomic.Int64
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
				err := repositoryCapacityMutation(context.Background(), client, endpoint, cfg.token, false, cfg.namespace, poolPrefix+strconv.Itoa(index+1), eventPrefix, sequence, "")
				locks[index].Unlock()
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
	ticker := time.NewTicker(cfg.transitionInterval)
	burst := time.NewTicker(cfg.burstInterval)
	sequence := 0
	enqueue := func() {
		sequence++
		produced.Add(1)
		select {
		case jobs <- sequence:
		default:
			backpressured.Add(1)
		}
	}
	for time.Since(started) < cfg.duration {
		select {
		case <-ticker.C:
			enqueue()
		case <-burst.C:
			for i := 0; i < cfg.burstSize; i++ {
				enqueue()
			}
		}
	}
	ticker.Stop()
	burst.Stop()
	close(jobs)
	wg.Wait()
	result.produced, result.admitted, result.failed, result.backpressured = produced.Load(), admitted.Load(), failed.Load(), backpressured.Load()
	result.maxSequence = sequence
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
	query := `mutation($input: UpdateRepositoryInput!) { updateRepository(input: $input) { repository { metadata { name resourceVersion } } } }`
	if create {
		query = `mutation($input: CreateRepositoryInput!) { createRepository(input: $input) { repository { metadata { name resourceVersion } } } }`
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
	deadline := time.Now().Add(30 * time.Second)
	for {
		req, reqErr := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimSuffix(endpoint, "/")+"/graphql", bytes.NewReader(payload))
		if reqErr != nil {
			return reqErr
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+token)
		response, doErr := client.Do(req)
		if doErr == nil {
			contents, readErr := io.ReadAll(response.Body)
			response.Body.Close()
			if readErr == nil && response.StatusCode == http.StatusOK {
				var result struct {
					Errors []json.RawMessage `json:"errors"`
				}
				if json.Unmarshal(contents, &result) == nil && len(result.Errors) == 0 {
					return nil
				}
				doErr = fmt.Errorf("GraphQL errors: %s", result.Errors)
			} else if readErr != nil {
				doErr = readErr
			} else {
				doErr = fmt.Errorf("HTTP %s: %s", response.Status, contents)
			}
		}
		if time.Now().After(deadline) || ctx.Err() != nil {
			return doErr
		}
		time.Sleep(100 * time.Millisecond)
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

func dialRepositoryCapacityWatchSelection(endpoint string, cfg repositoryCapacityConfig, cursor, selection string) (*websocket.Conn, error) {
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
	query := fmt.Sprintf(`subscription($namespace: String!, $cursor: String) { watchRepositories(namespace: $namespace, resourceVersion: $cursor) { %s } }`, selection)
	err = conn.WriteJSON(map[string]any{"id": "repository-capacity", "type": "subscribe", "payload": map[string]any{"query": query, "variables": map[string]any{"namespace": cfg.namespace, "cursor": cursor}}})
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
	gitAttempted, acknowledged         bool
	files, bytes                       int
	referenceLatencies                 []time.Duration
	ackLatency                         time.Duration
	resolutionLatency                  time.Duration
	resolutionFailed, projectionFailed bool
	projectionChecks                   int64
	err                                error
}

type secretCapacityPushMeasurements struct {
	sustainedOffered, missedSchedules, completed, dropped, gitPushes, acknowledged, failed int64
	queuePeak, workersPeak, maxFiles, maxBytes                                             int
	offeredDuration                                                                        time.Duration
	bursts                                                                                 []secretCapacityBurst
	pushLatencies, referenceLatencies                                                      []time.Duration
	resolutionLatencies                                                                    []time.Duration
	localHealthyFailures, projectionLoss, crossReplicaChecks, typedFiles                   int64
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
		batch  secretCapacityPushBatch
		result secretCapacityPushResult
		at     time.Time
		active int
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
				activity.Lock()
				active++
				concurrency := active
				activity.Unlock()
				result := push(workCtx, worker, batch)
				at := time.Now()
				activity.Lock()
				active--
				activity.Unlock()
				results <- completion{batch, result, at, concurrency}
			}
		}(worker)
	}
	// Only the collector writes completed-operation observations. Schedule
	// observations below are merged after both sides have stopped.
	var completed secretCapacityPushMeasurements
	burstCompleted := make([]time.Time, burstCount)
	collected := make(chan struct{})
	go func() {
		defer close(collected)
		for item := range results {
			r := item.result
			completed.completed++
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
	measured.completed, measured.gitPushes, measured.acknowledged, measured.failed =
		completed.completed, completed.gitPushes, completed.acknowledged, completed.failed
	measured.workersPeak, measured.maxFiles, measured.maxBytes = completed.workersPeak, completed.maxFiles, completed.maxBytes
	measured.pushLatencies, measured.referenceLatencies = completed.pushLatencies, completed.referenceLatencies
	measured.resolutionLatencies, measured.localHealthyFailures = completed.resolutionLatencies, completed.localHealthyFailures
	measured.projectionLoss, measured.crossReplicaChecks, measured.typedFiles = completed.projectionLoss, completed.crossReplicaChecks, completed.typedFiles
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
		measured.gitPushes != measured.completed || measured.missedSchedules > 0 || slowBurst || workCtx.Err() != nil {
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
	return &secretCapacityGitWorker{directory, namespace, runID, token, secretCapacityWorkerFiles(worker)}, nil
}

func (worker *secretCapacityGitWorker) push(ctx context.Context, batch secretCapacityPushBatch) secretCapacityPushResult {
	var result secretCapacityPushResult
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if err := os.MkdirAll(filepath.Join(worker.directory, "files"), 0700); err != nil {
		result.err = errors.New("secret capacity: cannot create File directory")
		return result
	}
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
	for _, args := range [][]string{
		{"add", "--", "files"},
		{"commit", "--quiet", "-m", fmt.Sprintf("capacity File batch %d", batch.sequence)},
	} {
		if _, err := secretCapacityGit(ctx, worker.directory, "", args...); err != nil {
			result.err = err
			return result
		}
	}
	result.gitAttempted = true
	start := batch.offeredAt
	if start.IsZero() {
		start = time.Now()
	}
	_, result.err = secretCapacityGit(ctx, worker.directory, worker.token, "push", "--quiet", "origin", "HEAD:refs/heads/main")
	result.ackLatency = time.Since(start)
	result.acknowledged = result.err == nil
	return result
}

type secretCapacityGitOutput struct {
	buffer   bytes.Buffer
	overflow bool
}

func (output *secretCapacityGitOutput) Write(data []byte) (int, error) {
	size := len(data)
	remaining := 128*1024 - output.buffer.Len()
	if size > remaining {
		output.overflow = true
		data = data[:remaining]
	}
	_, _ = output.buffer.Write(data)
	return size, nil
}

func secretCapacityGit(ctx context.Context, directory, token string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, "git", args...)
	command.Dir = directory
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
	if err != nil || output.overflow {
		return nil, errors.New("secret capacity: Git command failed or exceeded output bounds")
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
			file := data[fmt.Sprintf("file%d", index)]
			if file == nil {
				return nil, fmt.Errorf("secret capacity: File pool lookup missing projection at index %d", offset+index)
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
		return fmt.Errorf("secret capacity: GraphQL HTTP status %d", response.StatusCode)
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
	workload.ids, err = secretCapacityReadFileIDs(t.Context(), cfg.apiA, cfg.token, cfg.namespace, runID)
	require.NoError(t, err)
	peerIDs, err := secretCapacityReadFileIDs(t.Context(), cfg.apiB, cfg.token, cfg.namespace, runID)
	require.NoError(t, err)
	require.Equal(t, workload.ids, peerIDs, "both APIs must project identical File identities before load")
	return workload
}

type secretCapacityFileLoadResult struct {
	measurements secretCapacityPushMeasurements
	err          error
}

func (workload *secretCapacityFileWorkload) run(ctx context.Context, duration time.Duration) secretCapacityFileLoadResult {
	client := &http.Client{Timeout: 5 * time.Second}
	defer client.CloseIdleConnections()
	cfg := secretCapacityProductionLoadConfig()
	cfg.duration = duration
	measurements, err := runSecretCapacityPushes(ctx, cfg, func(ctx context.Context, index int, batch secretCapacityPushBatch) secretCapacityPushResult {
		ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		resolution, resolutionErr := workload.runtime.resolve(ctx)
		worker := workload.workers[index]
		result := worker.push(ctx, batch)
		result.resolutionLatency, result.resolutionFailed = resolution, resolutionErr != nil
		if result.acknowledged {
			projectionErr := secretCapacityVerifyFileBatch(ctx, client, workload.endpoints, workload.token, worker, workload.ids, batch)
			result.projectionFailed = projectionErr != nil
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
		CrossReplicaChecks: m.crossReplicaChecks, LocalHealthyFailures: m.localHealthyFailures,
		Bursts: m.bursts, Reference: latency(m.referenceLatencies), Resolution: latency(m.resolutionLatencies), Push: latency(m.pushLatencies),
		RuntimeCallers: proof.callers, ProviderPeak: proof.peak, ProviderCalls: proof.providerCalls, AcceptedCalls: proof.acceptedCalls,
		SaturationDenied: proof.saturationDenied, DeadlineDenied: proof.deadlineDenied, NamespaceDenied: proof.namespaceDenied,
		BindingDenied: proof.bindingDenied, UnsupportedDenied: proof.unsupportedDenied,
	}
	return writeSecretCapacityComponent(directory, "file-workload.json", observations)
}

type secretCapacityFileObservations struct {
	SchemaVersion        int                   `json:"schemaVersion"`
	RunID                string                `json:"runID"`
	Component            string                `json:"component"`
	Mode                 capacityMode          `json:"mode"`
	WorkloadCompleted    bool                  `json:"workloadCompleted"`
	NominalDuration      time.Duration         `json:"nominalDuration"`
	OfferedDuration      time.Duration         `json:"offeredDuration"`
	SustainedOffered     int64                 `json:"sustainedOffered"`
	MissedSchedules      int64                 `json:"missedSchedules"`
	Completed            int64                 `json:"completed"`
	GitPushes            int64                 `json:"gitPushes"`
	Acknowledged         int64                 `json:"acknowledged"`
	Failed               int64                 `json:"failed"`
	Dropped              int64                 `json:"dropped"`
	TypedFiles           int64                 `json:"typedFiles"`
	QueuePeak            int                   `json:"queuePeak"`
	WorkersPeak          int                   `json:"workersPeak"`
	MaxFiles             int                   `json:"maxFilesPerPush"`
	MaxBytes             int                   `json:"maxBytesPerPush"`
	ProjectionLoss       int64                 `json:"projectionLoss"`
	CrossReplicaChecks   int64                 `json:"crossReplicaChecks"`
	LocalHealthyFailures int64                 `json:"localHealthyFailures"`
	Bursts               []secretCapacityBurst `json:"bursts"`
	Reference            secretCapacityLatency `json:"reference"`
	Resolution           secretCapacityLatency `json:"resolution"`
	Push                 secretCapacityLatency `json:"push"`
	RuntimeCallers       int                   `json:"runtimeCallers"`
	ProviderPeak         int                   `json:"providerPeak"`
	ProviderCalls        int64                 `json:"providerCalls"`
	AcceptedCalls        int64                 `json:"acceptedCalls"`
	SaturationDenied     int64                 `json:"saturationDenied"`
	DeadlineDenied       int64                 `json:"deadlineDenied"`
	NamespaceDenied      int64                 `json:"namespaceDenied"`
	BindingDenied        int64                 `json:"bindingDenied"`
	UnsupportedDenied    int64                 `json:"unsupportedDenied"`
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

func verifySecretCapacityDataset(ctx context.Context, client *http.Client, endpoints []string, token, namespace, manifest string,
	pageSize int, mode capacityMode,
) (secretCapacityDatasetObservations, error) {
	var observation secretCapacityDatasetObservations
	invalid := errors.New("secret capacity: incomplete or inconsistent offline Product dataset")
	if err := ctx.Err(); err != nil {
		return observation, err
	}
	if len(endpoints) != 2 || endpoints[0] == endpoints[1] || token == "" || pageSize < 1 || pageSize > 1000 ||
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
			err := secretCapacityGraphQLBounded(ctx, client, endpoint, token, `query($namespace:String!,$first:Int!,$after:String) {
				products(namespace:$namespace,first:$first,after:$after) {
					edges { cursor node { metadata { namespace name revision } spec { title } } }
					pageInfo { hasNextPage endCursor }
				}
			}`, map[string]any{"namespace": namespace, "first": pageSize, "after": after}, &data, 8*1024*1024)
			if err != nil {
				if ctx.Err() != nil {
					return secretCapacityDatasetObservations{}, ctx.Err()
				}
				return secretCapacityDatasetObservations{}, invalid
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
	remaining := int64(128*1024*1024) - writer.written
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
	names := []string{"gitstore-capacity-api-a", "gitstore-capacity-api-b",
		"gitstore-capacity-controller-manager-a", "gitstore-capacity-controller-manager-b", "gitstore-capacity-git-service"}
	if len(samples) != len(names) || logs.stopped {
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
		inspection, err := secretCapacityOwnedCommand(commandCtx, "docker", "inspect", "--format",
			"{{.Id}} {{.State.StartedAt}} {{.State.Running}}", names[i])
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
	const name = "gitstore-capacity-git-service"
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
		capacityEnvInt(t, "REPOSITORY_CAPACITY_SECRET_DATASET_PAGE_SIZE", 1000), cfg.mode)
	require.NoError(t, err, "verify acknowledged titled Products offline before any offered load")
	proof.RunID = os.Getenv("CAPACITY_RUN_ID")
	require.NotEmpty(t, proof.RunID)
	require.NoError(t, writeSecretCapacityComponent(os.Getenv("CAPACITY_EVIDENCE_DIR"), "dataset.json", proof))
	return proof
}
