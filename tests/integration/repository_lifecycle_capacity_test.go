// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/require"
)

const (
	repositoryCapacitySequenceAnnotation = "capacity.gitstore.dev/sequence"
	repositoryCapacitySentAtAnnotation   = "capacity.gitstore.dev/sent-at"
	repositoryCapacityPaddingAnnotation  = "capacity.gitstore.dev/padding"
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
	mutationWorkers             int
	burstSize                   int
	burstInterval               time.Duration
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
	if os.Getenv("REPOSITORY_LIFECYCLE_CAPACITY_RUN") != "1" {
		t.Skip("run through make capacity TARGET=repository PROFILE=lifecycle MODE=production against a deployed two-API/two-controller stack")
	}
	cfg := loadRepositoryCapacityConfig(t)
	validateRepositoryCapacityScale(t, cfg)

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

	ensureRepositoryCapacityNamespace(t, cfg)
	runID := strconv.FormatInt(time.Now().UnixNano(), 36)
	poolPrefix := "rcp-" + runID + "-"
	createRepositoryCapacityPool(t, client, cfg, poolPrefix)
	assertRepositoryCrossReplicaRead(t, cfg, poolPrefix+"1")

	metricsStart := map[string]capacityProcessMetrics{
		"api_a": mustRepositoryCapacityMetrics(t, client, cfg.apiA),
		"api_b": mustRepositoryCapacityMetrics(t, client, cfg.apiB),
	}
	metricsStart = stabilizeRepositoryCapacityBaseline(t, client, cfg, metricsStart)

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
	maxTransitions := int(cfg.duration/(100*time.Millisecond)) +
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

	soakStarted := time.Now()
	replacementDone := make(chan capacityRecoveryResult, 1)
	if cfg.skipReplacement {
		replacementDone <- capacityRecoveryResult{}
	} else {
		go func() { replacementDone <- runRepositoryCapacityReplacement(cfg, client, soakStarted) }()
	}
	load := runRepositoryCapacityLoad(t, client, cfg, poolPrefix, livePrefix, maxTransitions)
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
	metricsEnd = stabilizeRepositoryCapacityMemory(t, client, cfg, metricsEnd)
	assertCapacityMetrics(t, metricsStart, metricsEnd, metricsElapsed, recovery, false)
	t.Logf("Repository lifecycle capacity passed: mode=%s replay=%d replay_p95=%s subscribers=%d admitted=%d duration=%s", cfg.mode, cfg.replayEvents, replayP95, cfg.subscribers, load.admitted, time.Since(soakStarted))
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
		baselineStabilization: capacityEnvDuration(t, "REPOSITORY_CAPACITY_BASELINE_STABILIZATION", 5*time.Minute),
		postLoadStabilization: capacityEnvDuration(t, "REPOSITORY_CAPACITY_POST_LOAD_STABILIZATION", 10*time.Minute),
		mode:                  mode, skipReplacement: os.Getenv("REPOSITORY_CAPACITY_SKIP_REPLACEMENT") == "1",
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

func validateRepositoryCapacityScale(t *testing.T, cfg repositoryCapacityConfig) {
	t.Helper()
	for key, value := range map[string]string{"REPOSITORY_API_A": cfg.apiA, "REPOSITORY_API_B": cfg.apiB, "REPOSITORY_CONTROLLER_A": cfg.controllerA, "REPOSITORY_CONTROLLER_B": cfg.controllerB, "REPOSITORY_TOKEN": cfg.token} {
		require.NotEmpty(t, value, "%s is required", key)
	}
	if !cfg.skipReplacement {
		require.Contains(t, []string{cfg.apiA, cfg.apiB}, cfg.replacement)
		require.NotEmpty(t, cfg.trigger)
		_, err := os.Stat(cfg.trigger)
		require.ErrorIs(t, err, os.ErrNotExist, "replacement trigger must be fresh")
	}
	if cfg.mode == capacityModeDiagnostic {
		return
	}
	require.GreaterOrEqual(t, cfg.duration, 60*time.Minute)
	require.GreaterOrEqual(t, cfg.subscribers, 1000)
	require.GreaterOrEqual(t, cfg.replayEvents, 10000)
	require.GreaterOrEqual(t, cfg.replaySamples, 20)
	require.GreaterOrEqual(t, cfg.resourcePool, 50)
	require.GreaterOrEqual(t, cfg.overflowTransitions, 1000)
	require.GreaterOrEqual(t, cfg.burstSize, 100)
	require.False(t, cfg.skipReplacement)
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
	ticker := time.NewTicker(100 * time.Millisecond)
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
	cursor := repositoryCapacityBootstrapCursor(t, cfg.overflowAPI, cfg)
	selection := `type name resourceVersion repository { metadata { annotations } }`
	for i := 0; i < 32; i++ {
		selection += fmt.Sprintf(` payload%d: repository { metadata { annotations } }`, i)
	}
	conn, err := dialRepositoryCapacityWatchSelection(cfg.overflowAPI, cfg, cursor, selection)
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
