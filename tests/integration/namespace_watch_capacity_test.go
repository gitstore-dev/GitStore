// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package integration

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/bits"
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

const capacitySubscription = `subscription($cursor: String) {
  watchNamespaces(resourceVersion: $cursor) {
    type
    name
    resourceVersion
    namespace { metadata { name creationTimestamp } }
  }
}`

type capacityConfig struct {
	apiA, apiB, replacement, token string
	replacementTrigger             string
	duration                       time.Duration
	replacementDelay               time.Duration
	subscribers                    int
	replayEvents                   int
	replaySamples                  int
	replayCatchupTimeout           time.Duration
	burstInterval                  time.Duration
	burstSize                      int
	mutationWorkers                int
	mutationDrainTimeout           time.Duration
	allowMissingMetrics            bool
}

// TestNamespaceWatchDeploymentCapacity is deliberately deployment-driven. The
// two URLs must identify distinct API processes backed by the same Scylla
// deployment; no in-process journal or resolver substitute is used here.
func TestNamespaceWatchDeploymentCapacity(t *testing.T) {
	if os.Getenv("NAMESPACE_WATCH_CAPACITY_RUN") != "1" {
		t.Skip("run through make test-namespace-watch-capacity against a deployed two-replica stack")
	}
	cfg := loadCapacityConfig(t)
	require.NotEqual(t, cfg.apiA, cfg.apiB, "capacity gate requires two distinct API endpoints")
	readinessClient := &http.Client{Timeout: 2 * time.Second}
	require.True(t, endpointReady(readinessClient, cfg.apiA), "API A must pass /health and /ready before the capacity gate starts")
	require.True(t, endpointReady(readinessClient, cfg.apiB), "API B must pass /health and /ready before the capacity gate starts")

	client := &http.Client{Timeout: 15 * time.Second}
	runID := strconv.FormatInt(time.Now().UnixNano(), 36)
	replayPrefix := "nwcr-" + runID + "-"
	livePrefix := "nwcl-" + runID + "-"

	replayCursor := capacityBootstrapCursor(t, cfg.apiA, cfg.token)
	replayStarted := time.Now()
	createCapacityRange(t, client, cfg, replayPrefix, cfg.replayEvents)
	t.Logf("prepared %d acknowledged GraphQL transitions through both replicas in %s", cfg.replayEvents, time.Since(replayStarted))
	waitCapacityReplayCatchup(t, cfg, replayCursor, replayPrefix)

	replayDurations := runCapacityReplays(t, cfg, replayCursor, replayPrefix)
	replayP95 := percentileDuration(replayDurations, .95)
	require.LessOrEqual(t, replayP95, 5*time.Second, "10,000-event GraphQL replay p95 must be <=5s")

	maxTransitions := int(cfg.duration/(100*time.Millisecond)) +
		(int(cfg.duration/cfg.burstInterval)+2)*cfg.burstSize + 128
	subscribers := openCapacitySubscribers(t, cfg, maxTransitions)
	defer closeCapacitySubscribers(subscribers)

	readCtx, stopReaders := context.WithCancel(context.Background())
	defer stopReaders()
	readerErrors := make(chan error, len(subscribers))
	var readers sync.WaitGroup
	var latencyMu sync.Mutex
	latencies := make([]time.Duration, 0, 16*maxTransitions)
	for i, subscriber := range subscribers {
		readers.Add(1)
		go func(index int, state *capacitySubscriber) {
			defer readers.Done()
			if err := state.read(readCtx, livePrefix, index < 16, &latencyMu, &latencies); err != nil && readCtx.Err() == nil {
				readerErrors <- fmt.Errorf("subscriber %d: %w", index, err)
			}
		}(i, subscriber)
	}

	metricsStart := scrapeCapacityMetrics(t, client, cfg)
	soakStarted := time.Now()
	recoveryResult := make(chan capacityRecoveryResult, 1)
	go func() {
		recoveryResult <- coordinateCapacityReplacement(client, cfg, livePrefix, maxTransitions+1, soakStarted)
	}()
	workload := runCapacityMutationWorkload(t, client, cfg, livePrefix, maxTransitions)
	soakElapsed := time.Since(soakStarted)
	recovery := <-recoveryResult
	t.Logf("load accounting produced=%d enqueued=%d backpressured=%d attempted=%d admitted=%d acknowledged=%d failed=%d drain_timed_out=%t",
		workload.produced, workload.enqueued, workload.backpressured, workload.attempted, workload.admitted,
		workload.acknowledged.count(), workload.failed, workload.drainTimedOut)
	require.NoError(t, recovery.err)
	require.LessOrEqual(t, recovery.duration, 30*time.Second, "replacement endpoint must recover within 30s")
	metricsEnd := scrapeCapacityMetrics(t, client, cfg)
	metricsElapsed := time.Since(soakStarted)

	require.Greater(t, workload.attempted, 0)
	require.False(t, workload.drainTimedOut, "mutation workers must drain within the bounded shutdown window")
	require.Equal(t, workload.produced, workload.sustained+workload.bursts, "all produced work must be classified")
	require.Zero(t, workload.backpressured, "configured load must not be dropped by the bounded worker queue")
	require.Equal(t, workload.produced, workload.enqueued, "every produced transition must enter the bounded worker queue")
	require.Equal(t, workload.enqueued, workload.attempted, "every enqueued transition must be attempted before shutdown")
	require.Equal(t, workload.attempted, workload.admitted+workload.failed, "every attempt must be admitted or reported failed")
	require.Equal(t, workload.admitted, workload.acknowledged.count(), "admitted transitions must match acknowledged transitions")
	errorRate := float64(workload.failed) / float64(workload.attempted)
	require.Less(t, errorRate, .001, "internal GraphQL mutation error rate must remain below 0.1%%")
	expectedSustained := int(cfg.duration/(100*time.Millisecond)) - 2
	if expectedSustained > 0 {
		require.GreaterOrEqual(t, workload.sustained, expectedSustained, "must schedule 10 sustained transitions/second")
	}
	expectedBurstRounds := int(cfg.duration / cfg.burstInterval)
	if cfg.duration%cfg.burstInterval == 0 && expectedBurstRounds > 0 {
		// A tick exactly at the context deadline is not part of the measured window.
		expectedBurstRounds--
	}
	if expectedBurstRounds > 0 {
		require.GreaterOrEqual(t, workload.bursts, expectedBurstRounds*cfg.burstSize,
			"must produce every configured 100/s burst inside the measured window")
	}

	catchupDeadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(catchupDeadline) && capacityMissing(subscribers, workload.acknowledged) != 0 {
		time.Sleep(100 * time.Millisecond)
	}
	missing := capacityMissing(subscribers, workload.acknowledged)
	require.Zero(t, missing, "every subscriber must observe every acknowledged transition")

	latencyMu.Lock()
	observedLatencies := append([]time.Duration(nil), latencies...)
	latencyMu.Unlock()
	require.NotEmpty(t, observedLatencies)
	p95 := percentileDuration(observedLatencies, .95)
	p99 := percentileDuration(observedLatencies, .99)
	require.LessOrEqual(t, p95, time.Second, "event visibility p95 must be <=1s")
	require.LessOrEqual(t, p99, 3*time.Second, "event visibility p99 must be <=3s")

	assertCapacityMetrics(t, metricsStart, metricsEnd, metricsElapsed, recovery, cfg.allowMissingMetrics)

	stopReaders()
	closeCapacitySubscribers(subscribers)
	readers.Wait()
	close(readerErrors)
	for err := range readerErrors {
		require.NoError(t, err)
	}
	t.Logf("duration=%s load_elapsed=%s subscribers=%d produced=%d enqueued=%d backpressured=%d attempted=%d admitted=%d acknowledged=%d errors=%d error_rate=%.4f%% p95=%s p99=%s replay_samples=%d replay_p95=%s recovery=%s",
		cfg.duration, soakElapsed, cfg.subscribers, workload.produced, workload.enqueued, workload.backpressured,
		workload.attempted, workload.admitted, workload.acknowledged.count(), workload.failed,
		100*errorRate, p95, p99, len(replayDurations), replayP95, recovery.duration)
}

func loadCapacityConfig(t *testing.T) capacityConfig {
	t.Helper()
	duration := capacityEnvDuration(t, "NAMESPACE_WATCH_CAPACITY_DURATION", 60*time.Minute)
	cfg := capacityConfig{
		apiA:                 strings.TrimSuffix(os.Getenv("NAMESPACE_WATCH_API_A"), "/"),
		apiB:                 strings.TrimSuffix(os.Getenv("NAMESPACE_WATCH_API_B"), "/"),
		replacement:          strings.TrimSuffix(os.Getenv("NAMESPACE_WATCH_API_REPLACEMENT"), "/"),
		token:                os.Getenv("NAMESPACE_WATCH_TOKEN"),
		replacementTrigger:   os.Getenv("NAMESPACE_WATCH_REPLACEMENT_TRIGGER_FILE"),
		duration:             duration,
		replacementDelay:     capacityEnvDuration(t, "NAMESPACE_WATCH_CAPACITY_REPLACEMENT_DELAY", duration/2),
		subscribers:          capacityEnvInt(t, "NAMESPACE_WATCH_CAPACITY_SUBSCRIBERS", 1000),
		replayEvents:         capacityEnvInt(t, "NAMESPACE_WATCH_CAPACITY_REPLAY_EVENTS", 10000),
		replaySamples:        capacityEnvInt(t, "NAMESPACE_WATCH_CAPACITY_REPLAY_SAMPLES", 20),
		replayCatchupTimeout: capacityEnvDuration(t, "NAMESPACE_WATCH_CAPACITY_REPLAY_CATCHUP_TIMEOUT", 15*time.Minute),
		burstInterval:        capacityEnvDuration(t, "NAMESPACE_WATCH_CAPACITY_BURST_INTERVAL", time.Minute),
		burstSize:            capacityEnvInt(t, "NAMESPACE_WATCH_CAPACITY_BURST_SIZE", 100),
		mutationWorkers:      capacityEnvInt(t, "NAMESPACE_WATCH_CAPACITY_MUTATION_WORKERS", 20),
		mutationDrainTimeout: capacityMutationDrainTimeout,
		allowMissingMetrics:  os.Getenv("NAMESPACE_WATCH_CAPACITY_ALLOW_MISSING_METRICS") == "1",
	}
	require.NotEmpty(t, cfg.apiA, "NAMESPACE_WATCH_API_A is required")
	require.NotEmpty(t, cfg.apiB, "NAMESPACE_WATCH_API_B is required")
	require.NotEmpty(t, cfg.replacement, "NAMESPACE_WATCH_API_REPLACEMENT is required for rolling-replacement evidence")
	require.Contains(t, []string{cfg.apiA, cfg.apiB}, cfg.replacement, "replacement endpoint must identify one of the two loaded replicas")
	require.NotEmpty(t, cfg.replacementTrigger, "NAMESPACE_WATCH_REPLACEMENT_TRIGGER_FILE is required for deployment-harness coordination")
	_, triggerErr := os.Stat(cfg.replacementTrigger)
	require.ErrorIs(t, triggerErr, os.ErrNotExist, "replacement trigger must not exist before the gate starts")
	require.NotEmpty(t, cfg.token, "NAMESPACE_WATCH_TOKEN is required")
	require.Positive(t, cfg.duration)
	require.Positive(t, cfg.replacementDelay)
	require.Less(t, cfg.replacementDelay, cfg.duration)
	require.Positive(t, cfg.subscribers)
	require.Positive(t, cfg.replayEvents)
	require.Positive(t, cfg.replaySamples)
	require.Positive(t, cfg.replayCatchupTimeout)
	require.Positive(t, cfg.burstInterval)
	require.Positive(t, cfg.burstSize)
	require.Positive(t, cfg.mutationWorkers)
	return cfg
}

func waitCapacityReplayCatchup(t *testing.T, cfg capacityConfig, cursor, prefix string) {
	t.Helper()
	started := time.Now()
	deadline := started.Add(cfg.replayCatchupTimeout)
	seen := make(map[int]struct{}, cfg.replayEvents)
	var lastErr error
	for time.Now().Before(deadline) && len(seen) < cfg.replayEvents {
		attemptTimeout := min(30*time.Second, time.Until(deadline))
		conn, err := dialCapacityWatch(cfg.apiA, cfg.token, cursor, attemptTimeout)
		if err != nil {
			lastErr = err
			time.Sleep(250 * time.Millisecond)
			continue
		}
		_ = conn.SetReadDeadline(deadline)
		for len(seen) < cfg.replayEvents {
			event, readErr := readCapacityEvent(conn)
			if readErr != nil {
				lastErr = readErr
				break
			}
			if sequence, ok := capacitySequence(event.Name, prefix); ok {
				seen[sequence] = struct{}{}
			}
		}
		_ = conn.Close()
		if len(seen) < cfg.replayEvents {
			time.Sleep(250 * time.Millisecond)
		}
	}
	require.Lenf(t, seen, cfg.replayEvents,
		"durable journal did not catch up within %s before measured replay: last error: %v",
		cfg.replayCatchupTimeout, lastErr)
	t.Logf("durable journal caught up with %d replay transitions in %s", cfg.replayEvents, time.Since(started))
}

type capacitySubscriber struct {
	mu       sync.Mutex
	conn     *websocket.Conn
	endpoint string
	token    string
	cursor   string
	received *capacityBitset
}

func openCapacitySubscribers(t *testing.T, cfg capacityConfig, maxTransitions int) []*capacitySubscriber {
	t.Helper()
	states := make([]*capacitySubscriber, cfg.subscribers)
	errs := make(chan error, cfg.subscribers)
	var wg sync.WaitGroup
	for i := range states {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			endpoint := cfg.apiA
			if index%2 == 1 {
				endpoint = cfg.apiB
			}
			conn, err := dialCapacityWatch(endpoint, cfg.token, "__namespace_watch_bootstrap__", 30*time.Second)
			if err != nil {
				errs <- fmt.Errorf("subscriber %d bootstrap: %w", index, err)
				return
			}
			event, err := readCapacityEvent(conn)
			if err != nil || event.Type != "BOOKMARK" {
				_ = conn.Close()
				errs <- fmt.Errorf("subscriber %d bootstrap event type=%q: %w", index, event.Type, err)
				return
			}
			states[index] = &capacitySubscriber{
				conn: conn, endpoint: endpoint, token: cfg.token, cursor: event.ResourceVersion,
				received: newCapacityBitset(maxTransitions + 2),
			}
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		closeCapacitySubscribers(states)
		require.NoError(t, err)
	}
	for i, state := range states {
		require.NotNilf(t, state, "subscriber %d did not connect", i)
	}
	return states
}

func (s *capacitySubscriber) read(ctx context.Context, prefix string, sampleLatency bool, latencyMu *sync.Mutex, latencies *[]time.Duration) error {
	for {
		conn := s.connection()
		if err := conn.SetReadDeadline(time.Now().Add(45 * time.Second)); err != nil {
			return err
		}
		event, err := readCapacityEvent(conn)
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			if strings.Contains(err.Error(), "GraphQL watch") || strings.Contains(err.Error(), "completed before") {
				return err
			}
			if reconnectErr := s.reconnect(ctx, conn); reconnectErr != nil {
				return fmt.Errorf("resume %s from cursor %q after %v: %w", s.endpoint, s.cursorValue(), err, reconnectErr)
			}
			continue
		}
		if event.ResourceVersion != "" {
			s.setCursor(event.ResourceVersion)
		}
		sequence, ok := capacitySequence(event.Name, prefix)
		if !ok || !s.received.mark(sequence) {
			continue
		}
		if sampleLatency && !event.CreatedAt.IsZero() {
			latency := time.Since(event.CreatedAt)
			if latency < 0 {
				latency = 0
			}
			latencyMu.Lock()
			*latencies = append(*latencies, latency)
			latencyMu.Unlock()
		}
	}
}

func (s *capacitySubscriber) connection() *websocket.Conn {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.conn
}

func (s *capacitySubscriber) cursorValue() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cursor
}

func (s *capacitySubscriber) setCursor(cursor string) {
	s.mu.Lock()
	s.cursor = cursor
	s.mu.Unlock()
}

func (s *capacitySubscriber) reconnect(ctx context.Context, failed *websocket.Conn) error {
	_ = failed.Close()
	deadline := time.Now().Add(30 * time.Second)
	var lastErr error
	for time.Now().Before(deadline) {
		if ctx.Err() != nil {
			return nil
		}
		conn, err := dialCapacityWatch(s.endpoint, s.token, s.cursorValue(), 2*time.Second)
		if err == nil {
			if ctx.Err() != nil {
				_ = conn.Close()
				return nil
			}
			s.mu.Lock()
			s.conn = conn
			s.mu.Unlock()
			return nil
		}
		lastErr = err
	}
	return fmt.Errorf("replacement endpoint did not accept a resumed subscription within 30s: %w", lastErr)
}

func (s *capacitySubscriber) close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.conn != nil {
		_ = s.conn.Close()
	}
}

func closeCapacitySubscribers(subscribers []*capacitySubscriber) {
	for _, subscriber := range subscribers {
		if subscriber != nil {
			subscriber.close()
		}
	}
}

type capacityWireEvent struct {
	Type            string
	Name            string
	ResourceVersion string
	CreatedAt       time.Time
}

func dialCapacityWatch(apiURL, token, cursor string, timeout time.Duration) (*websocket.Conn, error) {
	deadline := time.Now().Add(timeout)
	var lastErr error
	for time.Now().Before(deadline) {
		wsURL := strings.Replace(apiURL, "http", "ws", 1) + "/graphql"
		dialer := websocket.Dialer{HandshakeTimeout: 10 * time.Second, Subprotocols: []string{"graphql-transport-ws"}}
		conn, _, err := dialer.Dial(wsURL, http.Header{"Authorization": []string{"Bearer " + token}})
		if err != nil {
			lastErr = err
			time.Sleep(250 * time.Millisecond)
			continue
		}
		_ = conn.SetReadDeadline(time.Now().Add(10 * time.Second))
		if err = conn.WriteJSON(map[string]any{"type": "connection_init"}); err == nil {
			var ack map[string]any
			err = conn.ReadJSON(&ack)
			if err == nil && ack["type"] != "connection_ack" {
				err = fmt.Errorf("connection init returned %v", ack["type"])
			}
		}
		if err == nil {
			err = conn.WriteJSON(map[string]any{
				"id": "namespace-capacity", "type": "subscribe",
				"payload": map[string]any{"query": capacitySubscription, "variables": map[string]any{"cursor": cursor}},
			})
		}
		if err == nil {
			_ = conn.SetReadDeadline(time.Time{})
			return conn, nil
		}
		lastErr = err
		_ = conn.Close()
	}
	return nil, fmt.Errorf("watch endpoint unavailable for %s: %w", timeout, lastErr)
}

func readCapacityEvent(conn *websocket.Conn) (capacityWireEvent, error) {
	for {
		var message struct {
			Type    string          `json:"type"`
			Payload json.RawMessage `json:"payload"`
		}
		if err := conn.ReadJSON(&message); err != nil {
			return capacityWireEvent{}, err
		}
		switch message.Type {
		case "ping":
			if err := conn.WriteJSON(map[string]any{"type": "pong"}); err != nil {
				return capacityWireEvent{}, err
			}
			continue
		case "error":
			return capacityWireEvent{}, fmt.Errorf("GraphQL watch error: %s", message.Payload)
		case "complete":
			return capacityWireEvent{}, errors.New("GraphQL watch completed before the gate finished")
		case "next":
			var payload struct {
				Data struct {
					Watch struct {
						Type            string `json:"type"`
						Name            string `json:"name"`
						ResourceVersion string `json:"resourceVersion"`
						Namespace       *struct {
							Metadata struct {
								CreationTimestamp time.Time `json:"creationTimestamp"`
							} `json:"metadata"`
						} `json:"namespace"`
					} `json:"watchNamespaces"`
				} `json:"data"`
				Errors []json.RawMessage `json:"errors"`
			}
			if err := json.Unmarshal(message.Payload, &payload); err != nil {
				return capacityWireEvent{}, err
			}
			if len(payload.Errors) != 0 {
				return capacityWireEvent{}, fmt.Errorf("GraphQL watch next errors: %s", payload.Errors)
			}
			event := capacityWireEvent{
				Type: payload.Data.Watch.Type, Name: payload.Data.Watch.Name,
				ResourceVersion: payload.Data.Watch.ResourceVersion,
			}
			if payload.Data.Watch.Namespace != nil {
				event.CreatedAt = payload.Data.Watch.Namespace.Metadata.CreationTimestamp
			}
			return event, nil
		}
	}
}

func capacityBootstrapCursor(t *testing.T, apiURL, token string) string {
	t.Helper()
	cursor, err := getCapacityBootstrapCursor(apiURL, token)
	require.NoError(t, err)
	return cursor
}

func getCapacityBootstrapCursor(apiURL, token string) (string, error) {
	conn, err := dialCapacityWatch(apiURL, token, "__namespace_watch_bootstrap__", 30*time.Second)
	if err != nil {
		return "", err
	}
	defer conn.Close()
	event, err := readCapacityEvent(conn)
	if err != nil {
		return "", err
	}
	if event.Type != "BOOKMARK" || event.ResourceVersion == "" {
		return "", fmt.Errorf("bootstrap returned type=%q cursor=%q", event.Type, event.ResourceVersion)
	}
	return event.ResourceVersion, nil
}

func runCapacityReplays(t *testing.T, cfg capacityConfig, cursor, prefix string) []time.Duration {
	t.Helper()
	durations := make([]time.Duration, cfg.replaySamples)
	errs := make(chan error, cfg.replaySamples)
	var wg sync.WaitGroup
	for i := range durations {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			endpoint := cfg.apiA
			if index%2 == 1 {
				endpoint = cfg.apiB
			}
			started := time.Now()
			conn, err := dialCapacityWatch(endpoint, cfg.token, cursor, 10*time.Second)
			if err != nil {
				errs <- fmt.Errorf("replay sample %d via %s: dial: %w", index, endpoint, err)
				return
			}
			defer conn.Close()
			_ = conn.SetReadDeadline(started.Add(10 * time.Second))
			seen := make(map[int]struct{}, cfg.replayEvents)
			for len(seen) < cfg.replayEvents {
				event, readErr := readCapacityEvent(conn)
				if readErr != nil {
					errs <- fmt.Errorf("replay sample %d via %s: read: %w", index, endpoint, readErr)
					return
				}
				if sequence, ok := capacitySequence(event.Name, prefix); ok {
					seen[sequence] = struct{}{}
				}
			}
			durations[index] = time.Since(started)
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}
	return durations
}

type capacityMutationJob struct {
	prefix string
	seq    int
}

type capacityWorkload struct {
	produced, enqueued, backpressured              int
	attempted, admitted, failed, sustained, bursts int
	drainTimedOut                                  bool
	acknowledged                                   *capacityBitset
}

const capacityMutationDrainTimeout = 30 * time.Second

func runCapacityMutationWorkload(t *testing.T, client *http.Client, cfg capacityConfig, prefix string, maxTransitions int) capacityWorkload {
	t.Helper()
	// Keep the queue deliberately small: it absorbs scheduler jitter but cannot
	// turn an under-capacity deployment into an unbounded post-soak drain.
	jobs := make(chan capacityMutationJob, max(1, cfg.mutationWorkers*2))
	acknowledged := newCapacityBitset(maxTransitions + 2)
	var sequence atomic.Int64
	var produced, enqueued, backpressured atomic.Int64
	var attempted, admitted, failed, sustained, bursts atomic.Int64
	workerCtx, cancelWorkers := context.WithCancel(context.Background())
	defer cancelWorkers()
	var workers sync.WaitGroup
	for range cfg.mutationWorkers {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for {
				if workerCtx.Err() != nil {
					return
				}
				job, ok := <-jobs
				if !ok {
					return
				}
				attempted.Add(1)
				primary, peer := cfg.apiA, cfg.apiB
				if job.seq%2 == 1 {
					primary, peer = peer, primary
				}
				name := capacityName(job.prefix, job.seq)
				if err := createCapacityNamespaceContext(workerCtx, client, primary, cfg.token, name); err != nil {
					peerErr := createCapacityNamespaceContext(workerCtx, client, peer, cfg.token, name)
					if peerErr != nil && !capacityNamespaceExistsContext(workerCtx, client, peer, cfg.token, name) {
						failed.Add(1)
						continue
					}
				}
				acknowledged.mark(job.seq)
				admitted.Add(1)
			}
		}()
	}

	ctx, cancelSchedule := context.WithTimeout(context.Background(), cfg.duration)
	defer cancelSchedule()
	enqueue := func(job capacityMutationJob) {
		produced.Add(1)
		select {
		case jobs <- job:
			enqueued.Add(1)
		default:
			backpressured.Add(1)
		}
	}
	var schedulers sync.WaitGroup
	schedulers.Add(2)
	go func() {
		defer schedulers.Done()
		ticker := time.NewTicker(100 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				seq := int(sequence.Add(1))
				sustained.Add(1)
				enqueue(capacityMutationJob{prefix: prefix, seq: seq})
			}
		}
	}()
	go func() {
		defer schedulers.Done()
		ticker := time.NewTicker(cfg.burstInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				burstTicker := time.NewTicker(10 * time.Millisecond)
				for range cfg.burstSize {
					select {
					case <-ctx.Done():
						burstTicker.Stop()
						return
					case <-burstTicker.C:
						seq := int(sequence.Add(1))
						bursts.Add(1)
						enqueue(capacityMutationJob{prefix: prefix, seq: seq})
					}
				}
				burstTicker.Stop()
			}
		}
	}()
	schedulers.Wait()
	close(jobs)
	workersDone := make(chan struct{})
	go func() {
		workers.Wait()
		close(workersDone)
	}()
	drainTimeout := cfg.mutationDrainTimeout
	if drainTimeout <= 0 {
		drainTimeout = capacityMutationDrainTimeout
	}
	drainTimer := time.NewTimer(drainTimeout)
	drainTimedOut := false
	select {
	case <-workersDone:
		// Go 1.25 timer channels do not retain stale values after Stop. Avoid an
		// unconditional drain here: if worker completion wins the select while
		// expiry is concurrently becoming ready, receiving can block forever.
		drainTimer.Stop()
	case <-drainTimer.C:
		drainTimedOut = true
		cancelWorkers()
		<-workersDone
	}
	return capacityWorkload{
		produced: int(produced.Load()), enqueued: int(enqueued.Load()), backpressured: int(backpressured.Load()),
		attempted: int(attempted.Load()), admitted: int(admitted.Load()), failed: int(failed.Load()), drainTimedOut: drainTimedOut,
		sustained: int(sustained.Load()), bursts: int(bursts.Load()), acknowledged: acknowledged,
	}
}

func TestCapacityMutationWorkloadAccountsForBackpressure(t *testing.T) {
	t.Run("keeps produced and admitted load equal when workers keep up", func(t *testing.T) {
		server := newCapacityMutationServer(t, 0)
		defer server.Close()
		cfg := capacityConfig{
			apiA: server.URL, apiB: server.URL, duration: 350 * time.Millisecond,
			burstInterval: 200 * time.Millisecond, burstSize: 3, mutationWorkers: 4,
		}

		workload := runCapacityMutationWorkload(t, server.Client(), cfg, "fast-", 64)

		require.False(t, workload.drainTimedOut)
		require.Zero(t, workload.backpressured)
		require.Equal(t, workload.produced, workload.enqueued)
		require.Equal(t, workload.enqueued, workload.attempted)
		require.Equal(t, workload.attempted, workload.admitted)
		require.Equal(t, workload.attempted, workload.acknowledged.count())
		require.Zero(t, workload.failed)
		require.GreaterOrEqual(t, workload.sustained, 2)
		require.GreaterOrEqual(t, workload.bursts, 3)
	})

	t.Run("reports queue saturation without stretching the scheduler", func(t *testing.T) {
		server := newCapacityMutationServer(t, 300*time.Millisecond)
		defer server.Close()
		cfg := capacityConfig{
			apiA: server.URL, apiB: server.URL, duration: 350 * time.Millisecond,
			burstInterval: 100 * time.Millisecond, burstSize: 50, mutationWorkers: 1,
		}
		started := time.Now()

		workload := runCapacityMutationWorkload(t, server.Client(), cfg, "slow-", 512)

		require.Less(t, time.Since(started), 2*time.Second)
		require.False(t, workload.drainTimedOut)
		require.Positive(t, workload.backpressured)
		require.Less(t, workload.enqueued, workload.produced)
		require.Equal(t, workload.enqueued, workload.attempted)
		require.Equal(t, workload.attempted, workload.admitted)
	})

	t.Run("cancels in-flight requests when the bounded drain expires", func(t *testing.T) {
		releaseHandler := make(chan struct{})
		server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
			select {
			case <-r.Context().Done():
			case <-releaseHandler:
			}
		}))
		t.Cleanup(func() {
			close(releaseHandler)
			server.Close()
		})
		cfg := capacityConfig{
			apiA: server.URL, apiB: server.URL, duration: 150 * time.Millisecond,
			burstInterval: 100 * time.Millisecond, burstSize: 20, mutationWorkers: 1,
			mutationDrainTimeout: 50 * time.Millisecond,
		}
		started := time.Now()

		workload := runCapacityMutationWorkload(t, server.Client(), cfg, "blocked-", 128)

		require.Less(t, time.Since(started), time.Second)
		require.True(t, workload.drainTimedOut)
		require.Positive(t, workload.attempted)
	})
}

func newCapacityMutationServer(t *testing.T, delay time.Duration) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if delay > 0 {
			time.Sleep(delay)
		}
		var request gqlRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Errorf("decode mutation request: %v", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		input, _ := request.Variables["input"].(map[string]any)
		metadata, _ := input["metadata"].(map[string]any)
		name, _ := metadata["name"].(string)
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"data":{"createNamespace":{"namespace":{"metadata":{"name":%q}}}}}`, name)
	}))
}

func createCapacityRange(t *testing.T, client *http.Client, cfg capacityConfig, prefix string, count int) {
	t.Helper()
	jobs := make(chan int)
	errs := make(chan error, count)
	var wg sync.WaitGroup
	for range cfg.mutationWorkers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for sequence := range jobs {
				if err := createCapacityNamespaceForReplay(client, cfg, capacityName(prefix, sequence), sequence); err != nil {
					errs <- fmt.Errorf("transition %d: %w", sequence, err)
				}
			}
		}()
	}
	for sequence := 1; sequence <= count; sequence++ {
		jobs <- sequence
	}
	close(jobs)
	wg.Wait()
	close(errs)
	for err := range errs {
		require.NoError(t, err, "all replay preparation transitions must be acknowledged")
	}
}

const (
	capacityReplayCreateTimeout    = 2 * time.Minute
	capacityReplayCreateMaxBackoff = 500 * time.Millisecond
)

func createCapacityNamespaceForReplay(client *http.Client, cfg capacityConfig, name string, sequence int) error {
	primary, peer := cfg.apiA, cfg.apiB
	if sequence%2 == 1 {
		primary, peer = peer, primary
	}
	retryCfg := cfg
	retryCfg.apiA, retryCfg.apiB = primary, peer
	return createCapacityNamespaceRetrying(client, retryCfg, name, capacityReplayCreateTimeout)
}

func createCapacityNamespaceRetrying(client *http.Client, cfg capacityConfig, name string, retryWindow time.Duration) error {
	if retryWindow <= 0 {
		return fmt.Errorf("create retry window expired for Namespace %q", name)
	}
	endpoints := [2]string{cfg.apiA, cfg.apiB}
	var lastErr error
	deadline := time.Now().Add(retryWindow)
	for attempt := 0; ; attempt++ {
		endpoint := endpoints[attempt%len(endpoints)]
		lastErr = createCapacityNamespace(client, endpoint, cfg.token, name)
		if lastErr == nil {
			return nil
		}
		if !capacityCreateRetryable(lastErr) && !capacityCreateAmbiguous(lastErr) {
			return lastErr
		}
		confirmationWindow := min(750*time.Millisecond, time.Until(deadline))
		if confirmationWindow > 0 && confirmCapacityNamespaceExists(cfg, name, confirmationWindow) {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("create remained retryable for %s: %w", retryWindow, lastErr)
		}
		backoff := time.Duration(1<<min(attempt, 4)) * 25 * time.Millisecond
		backoff = min(backoff, capacityReplayCreateMaxBackoff)
		time.Sleep(min(backoff, time.Until(deadline)))
	}
}

type capacityGraphQLErrors struct{ raw string }

func (e *capacityGraphQLErrors) Error() string { return "GraphQL errors: " + e.raw }

type capacityHTTPStatusError struct {
	status int
	body   string
}

func (e *capacityHTTPStatusError) Error() string { return fmt.Sprintf("HTTP %d: %s", e.status, e.body) }

func capacityCreateRetryable(err error) bool {
	var graphqlErr *capacityGraphQLErrors
	if !errors.As(err, &graphqlErr) {
		return false
	}
	return strings.Contains(graphqlErr.raw, `"code":"NAMESPACE_CONFLICT"`) ||
		strings.Contains(graphqlErr.raw, `"reason":"RESOURCE_VERSION_CONFLICT"`)
}

func capacityCreateAmbiguous(err error) bool {
	var statusErr *capacityHTTPStatusError
	if errors.As(err, &statusErr) {
		return statusErr.status == http.StatusRequestTimeout || statusErr.status == http.StatusTooManyRequests || statusErr.status >= 500
	}
	var netErr net.Error
	return errors.As(err, &netErr) || errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF)
}

func confirmCapacityNamespaceExists(cfg capacityConfig, name string, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	client := &http.Client{Timeout: 250 * time.Millisecond}
	for time.Now().Before(deadline) {
		if capacityNamespaceExists(client, cfg.apiA, cfg.token, name) || capacityNamespaceExists(client, cfg.apiB, cfg.token, name) {
			return true
		}
		time.Sleep(25 * time.Millisecond)
	}
	return false
}

func createCapacityNamespace(client *http.Client, apiURL, token, name string) error {
	return createCapacityNamespaceContext(context.Background(), client, apiURL, token, name)
}

func createCapacityNamespaceContext(ctx context.Context, client *http.Client, apiURL, token, name string) error {
	body, err := json.Marshal(gqlRequest{
		Query: `mutation($input: CreateNamespaceInput!) { createNamespace(input: $input) { namespace { metadata { name } } } }`,
		Variables: map[string]any{"input": map[string]any{
			"apiVersion": "gitstore.dev/v1beta1", "kind": "Namespace",
			"metadata": map[string]any{"name": name},
			"spec":     map[string]any{"title": name, "tier": "USER"},
		}},
	})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, apiURL+"/graphql", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	response, err := client.Do(req)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode/100 != 2 {
		contents, _ := io.ReadAll(io.LimitReader(response.Body, 4096))
		return &capacityHTTPStatusError{status: response.StatusCode, body: string(contents)}
	}
	var result gqlResponse
	if err := json.NewDecoder(response.Body).Decode(&result); err != nil {
		return err
	}
	if len(result.Errors) != 0 {
		return &capacityGraphQLErrors{raw: namespaceContractErrors(result.Errors)}
	}
	var data struct {
		CreateNamespace *struct {
			Namespace struct {
				Metadata struct {
					Name string `json:"name"`
				} `json:"metadata"`
			} `json:"namespace"`
		} `json:"createNamespace"`
	}
	if err := json.Unmarshal(result.Data, &data); err != nil {
		return err
	}
	if data.CreateNamespace == nil || data.CreateNamespace.Namespace.Metadata.Name != name {
		return fmt.Errorf("GraphQL mutation did not acknowledge Namespace %q", name)
	}
	return nil
}

func capacityNamespaceExists(client *http.Client, apiURL, token, name string) bool {
	return capacityNamespaceExistsContext(context.Background(), client, apiURL, token, name)
}

func capacityNamespaceExistsContext(ctx context.Context, client *http.Client, apiURL, token, name string) bool {
	body, err := json.Marshal(gqlRequest{
		Query:     `query($name: String!) { namespace(by: {identifier: $name}) { id } }`,
		Variables: map[string]any{"name": name},
	})
	if err != nil {
		return false
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, apiURL+"/graphql", bytes.NewReader(body))
	if err != nil {
		return false
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	response, err := client.Do(req)
	if err != nil {
		return false
	}
	defer response.Body.Close()
	if response.StatusCode/100 != 2 {
		return false
	}
	var result struct {
		Data struct {
			Namespace *struct {
				ID string `json:"id"`
			} `json:"namespace"`
		} `json:"data"`
	}
	return json.NewDecoder(response.Body).Decode(&result) == nil && result.Data.Namespace != nil && result.Data.Namespace.ID != ""
}

func TestCapacityReplayCreateRetriesOnlyConflicts(t *testing.T) {
	t.Run("conflict alternates to peer", func(t *testing.T) {
		var primaryCalls, peerCalls atomic.Int64
		primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var request gqlRequest
			_ = json.NewDecoder(r.Body).Decode(&request)
			if !strings.Contains(request.Query, "mutation") {
				_, _ = io.WriteString(w, `{"data":{"namespace":null}}`)
				return
			}
			primaryCalls.Add(1)
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"errors":[{"message":"superseded","extensions":{"code":"NAMESPACE_CONFLICT","reason":"RESOURCE_VERSION_CONFLICT"}}]}`)
		}))
		defer primary.Close()
		peer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var request gqlRequest
			_ = json.NewDecoder(r.Body).Decode(&request)
			if !strings.Contains(request.Query, "mutation") {
				_, _ = io.WriteString(w, `{"data":{"namespace":null}}`)
				return
			}
			peerCalls.Add(1)
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"data":{"createNamespace":{"namespace":{"metadata":{"name":"replay-name"}}}}}`)
		}))
		defer peer.Close()

		err := createCapacityNamespaceForReplay(http.DefaultClient, capacityConfig{apiA: primary.URL, apiB: peer.URL}, "replay-name", 2)
		require.NoError(t, err)
		require.EqualValues(t, 1, primaryCalls.Load())
		require.EqualValues(t, 1, peerCalls.Load())
	})

	t.Run("permanent GraphQL error is not retried", func(t *testing.T) {
		var peerCalls atomic.Int64
		primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"errors":[{"message":"invalid","extensions":{"code":"BAD_USER_INPUT"}}]}`)
		}))
		defer primary.Close()
		peer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			peerCalls.Add(1)
			w.WriteHeader(http.StatusInternalServerError)
		}))
		defer peer.Close()

		err := createCapacityNamespaceForReplay(http.DefaultClient, capacityConfig{apiA: primary.URL, apiB: peer.URL}, "replay-name", 2)
		require.Error(t, err)
		require.False(t, capacityCreateRetryable(err))
		require.Zero(t, peerCalls.Load())
	})

	t.Run("ambiguous response confirms committed Namespace", func(t *testing.T) {
		var peerCalls atomic.Int64
		primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var request gqlRequest
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				t.Errorf("decode request: %v", err)
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			if strings.Contains(request.Query, "mutation") {
				w.WriteHeader(http.StatusInternalServerError)
				_, _ = io.WriteString(w, `commit outcome unknown`)
				return
			}
			_, _ = io.WriteString(w, `{"data":{"namespace":{"id":"confirmed"}}}`)
		}))
		defer primary.Close()
		peer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			peerCalls.Add(1)
			w.WriteHeader(http.StatusInternalServerError)
		}))
		defer peer.Close()

		err := createCapacityNamespaceForReplay(http.DefaultClient, capacityConfig{apiA: primary.URL, apiB: peer.URL}, "replay-name", 2)
		require.NoError(t, err)
		require.Zero(t, peerCalls.Load())
	})
}

type capacityRecoveryResult struct {
	duration               time.Duration
	label                  string
	beforeStop, afterStart capacityProcessMetrics
	beforeStopAt           time.Duration
	afterStartAt           time.Duration
	err                    error
}

func coordinateCapacityReplacement(client *http.Client, cfg capacityConfig, prefix string, sequence int, soakStarted time.Time) capacityRecoveryResult {
	peerEndpoint := cfg.apiA
	label := "api_b"
	if cfg.replacement == cfg.apiA {
		peerEndpoint = cfg.apiB
		label = "api_a"
	}
	cursor, err := getCapacityBootstrapCursor(peerEndpoint, cfg.token)
	if err != nil {
		return capacityRecoveryResult{err: fmt.Errorf("capture peer cursor before replacement: %w", err)}
	}

	timer := time.NewTimer(cfg.replacementDelay)
	defer timer.Stop()
	<-timer.C
	beforeStop, err := fetchCapacityMetrics(client, cfg.replacement)
	if err != nil {
		return capacityRecoveryResult{err: fmt.Errorf("scrape replacement identity before trigger: %w", err)}
	}
	result := capacityRecoveryResult{label: label, beforeStop: beforeStop, beforeStopAt: time.Since(soakStarted)}
	trigger := fmt.Sprintf("replace %s start_time=%.0f requested_at=%s\n", cfg.replacement, beforeStop.processStart, time.Now().UTC().Format(time.RFC3339Nano))
	if err := os.WriteFile(cfg.replacementTrigger, []byte(trigger), 0o600); err != nil {
		result.err = fmt.Errorf("write replacement trigger %s: %w", cfg.replacementTrigger, err)
		return result
	}

	probeClient := &http.Client{Timeout: 500 * time.Millisecond}
	outageDeadline := time.Now().Add(30 * time.Second)
	for endpointReady(probeClient, cfg.replacement) && time.Now().Before(outageDeadline) {
		time.Sleep(100 * time.Millisecond)
	}
	if endpointReady(probeClient, cfg.replacement) {
		result.err = errors.New("replacement trigger did not produce an observed endpoint outage within 30s")
		return result
	}
	outageStarted := time.Now()

	recoveryDeadline := outageStarted.Add(30 * time.Second)
	for time.Now().Before(recoveryDeadline) {
		if endpointReady(probeClient, cfg.replacement) {
			afterStart, metricsErr := fetchCapacityMetrics(client, cfg.replacement)
			if metricsErr == nil && afterStart.processStart != beforeStop.processStart {
				result.afterStart = afterStart
				result.afterStartAt = time.Since(soakStarted)
				break
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	if result.afterStart.processStart == 0 {
		result.err = errors.New("replacement endpoint did not return with a changed process_start_time_seconds within 30s")
		return result
	}

	conn, err := dialCapacityWatch(cfg.replacement, cfg.token, cursor, 30*time.Second)
	if err != nil {
		result.err = fmt.Errorf("resume peer cursor through replacement: %w", err)
		return result
	}
	defer conn.Close()

	name := capacityName(prefix, sequence)
	retryCfg := cfg
	retryCfg.apiA, retryCfg.apiB = peerEndpoint, cfg.replacement
	if err := createCapacityNamespaceRetrying(client, retryCfg, name, time.Until(recoveryDeadline)); err != nil {
		result.err = fmt.Errorf("create post-replacement transition: %w", err)
		return result
	}
	_ = conn.SetReadDeadline(outageStarted.Add(30 * time.Second))
	for {
		event, readErr := readCapacityEvent(conn)
		if readErr != nil {
			result.err = fmt.Errorf("read post-replacement transition: %w", readErr)
			return result
		}
		if event.Name == name {
			result.duration = time.Since(outageStarted)
			return result
		}
	}
}

func endpointReady(client *http.Client, endpoint string) bool {
	for _, path := range []string{"/health", "/ready"} {
		response, err := client.Get(endpoint + path)
		if err != nil {
			return false
		}
		_ = response.Body.Close()
		if response.StatusCode/100 != 2 {
			return false
		}
	}
	return true
}

type capacityProcessMetrics struct {
	available    bool
	cpu          float64
	resident     float64
	processStart float64
	gomaxprocs   float64
}

func scrapeCapacityMetrics(t *testing.T, client *http.Client, cfg capacityConfig) map[string]capacityProcessMetrics {
	t.Helper()
	result := make(map[string]capacityProcessMetrics, 2)
	for label, endpoint := range map[string]string{"api_a": cfg.apiA, "api_b": cfg.apiB} {
		metrics, err := fetchCapacityMetrics(client, endpoint)
		if err != nil {
			t.Logf("%s process metrics unavailable at %s/metrics: %v", label, endpoint, err)
			continue
		}
		result[label] = metrics
	}
	return result
}

func fetchCapacityMetrics(client *http.Client, endpoint string) (capacityProcessMetrics, error) {
	request, err := http.NewRequest(http.MethodGet, endpoint+"/metrics", nil)
	if err != nil {
		return capacityProcessMetrics{}, err
	}
	response, err := client.Do(request)
	if err != nil {
		return capacityProcessMetrics{}, err
	}
	defer response.Body.Close()
	if response.StatusCode/100 != 2 {
		return capacityProcessMetrics{}, fmt.Errorf("metrics returned %s", response.Status)
	}
	values := map[string]float64{}
	scanner := bufio.NewScanner(response.Body)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) != 2 {
			continue
		}
		switch fields[0] {
		case "process_cpu_seconds_total", "process_resident_memory_bytes", "process_start_time_seconds", "go_sched_gomaxprocs_threads":
			values[fields[0]], _ = strconv.ParseFloat(fields[1], 64)
		}
	}
	if err := scanner.Err(); err != nil {
		return capacityProcessMetrics{}, err
	}
	cpu, hasCPU := values["process_cpu_seconds_total"]
	resident, hasResident := values["process_resident_memory_bytes"]
	processStart, hasStart := values["process_start_time_seconds"]
	gomaxprocs, hasGOMAXPROCS := values["go_sched_gomaxprocs_threads"]
	if !hasCPU || !hasResident || !hasStart || !hasGOMAXPROCS || gomaxprocs <= 0 {
		return capacityProcessMetrics{}, errors.New("required process metrics are absent")
	}
	return capacityProcessMetrics{
		available: true, cpu: cpu, resident: resident, processStart: processStart, gomaxprocs: gomaxprocs,
	}, nil
}

func assertCapacityMetrics(t *testing.T, start, end map[string]capacityProcessMetrics, elapsed time.Duration, recovery capacityRecoveryResult, allowMissing bool) {
	t.Helper()
	for _, label := range []string{"api_a", "api_b"} {
		before, beforeOK := start[label]
		after, afterOK := end[label]
		if !beforeOK || !afterOK || !before.available || !after.available {
			if allowMissing {
				t.Logf("%s CPU/resident thresholds explicitly skipped because /metrics was unavailable", label)
				continue
			}
			require.Failf(t, "required process metrics unavailable", "%s must expose process CPU, resident memory, and start time", label)
			continue
		}
		// process_cpu_seconds_total aggregates work across executing threads.
		// Dividing each segment by its reported GOMAXPROCS converts it to
		// utilized normalized CPU before applying the wall-time threshold.
		normalizedCPUSeconds := (after.cpu - before.cpu) / before.gomaxprocs
		measuredSeconds := elapsed.Seconds()
		memoryGrowth := capacityMemoryGrowth(before.resident, after.resident)
		if label == recovery.label {
			normalizedCPUSeconds = (recovery.beforeStop.cpu-before.cpu)/before.gomaxprocs +
				(after.cpu-recovery.afterStart.cpu)/recovery.afterStart.gomaxprocs
			measuredSeconds = recovery.beforeStopAt.Seconds() + (elapsed - recovery.afterStartAt).Seconds()
			memoryGrowth = max(
				capacityMemoryGrowth(before.resident, recovery.beforeStop.resident),
				capacityMemoryGrowth(recovery.afterStart.resident, after.resident),
			)
		}
		cpuPercent := 100 * normalizedCPUSeconds / measuredSeconds
		require.Lessf(t, cpuPercent, 80.0, "%s process CPU must remain below 80%%", label)
		require.Lessf(t, memoryGrowth, 10.0, "%s resident-memory growth must remain below 10%%", label)
		t.Logf("%s normalized_cpu=%.2f%% gomaxprocs=%.0f resident_start=%.0f resident_end=%.0f resident_growth=%.2f%%", label, cpuPercent, before.gomaxprocs, before.resident, after.resident, memoryGrowth)
	}
}

func capacityMemoryGrowth(start, end float64) float64 {
	if start <= 0 {
		return 0
	}
	return 100 * (end - start) / start
}

type capacityBitset struct {
	words []atomic.Uint64
}

func newCapacityBitset(max int) *capacityBitset {
	return &capacityBitset{words: make([]atomic.Uint64, (max+63)/64)}
}

func (b *capacityBitset) mark(sequence int) bool {
	if sequence < 1 {
		return false
	}
	word := (sequence - 1) / 64
	if word >= len(b.words) {
		return false
	}
	mask := uint64(1) << uint((sequence-1)%64)
	for {
		old := b.words[word].Load()
		if old&mask != 0 {
			return false
		}
		if b.words[word].CompareAndSwap(old, old|mask) {
			return true
		}
	}
}

func (b *capacityBitset) count() int {
	total := 0
	for i := range b.words {
		total += bits.OnesCount64(b.words[i].Load())
	}
	return total
}

func capacityMissing(subscribers []*capacitySubscriber, acknowledged *capacityBitset) int {
	missing := 0
	for _, subscriber := range subscribers {
		for i := range acknowledged.words {
			missing += bits.OnesCount64(acknowledged.words[i].Load() &^ subscriber.received.words[i].Load())
		}
	}
	return missing
}

func capacityName(prefix string, sequence int) string {
	return fmt.Sprintf("%s%08d", prefix, sequence)
}

func capacitySequence(name, prefix string) (int, bool) {
	if !strings.HasPrefix(name, prefix) {
		return 0, false
	}
	sequence, err := strconv.Atoi(strings.TrimPrefix(name, prefix))
	return sequence, err == nil && sequence > 0
}

func percentileDuration(values []time.Duration, percentile float64) time.Duration {
	ordered := append([]time.Duration(nil), values...)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i] < ordered[j] })
	return ordered[int(float64(len(ordered)-1)*percentile)]
}

func capacityEnvDuration(t *testing.T, key string, fallback time.Duration) time.Duration {
	t.Helper()
	if raw := os.Getenv(key); raw != "" {
		value, err := time.ParseDuration(raw)
		require.NoError(t, err)
		return value
	}
	return fallback
}

func capacityEnvInt(t *testing.T, key string, fallback int) int {
	t.Helper()
	if raw := os.Getenv(key); raw != "" {
		value, err := strconv.Atoi(raw)
		require.NoError(t, err)
		return value
	}
	return fallback
}
