// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package watchjournal

import (
	"context"
	"os"
	"runtime"
	runtimemetrics "runtime/metrics"
	"sort"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

type capacityJournal struct {
	mu     sync.RWMutex
	epoch  string
	events []datastore.NamespaceWatchEvent
}

func newCapacityJournal() *capacityJournal { return &capacityJournal{epoch: uuid.NewString()} }

func (j *capacityJournal) add(at time.Time) datastore.NamespaceWatchEvent {
	j.mu.Lock()
	defer j.mu.Unlock()
	event := datastore.NamespaceWatchEvent{Epoch: j.epoch, Sequence: uint64(len(j.events) + 1), Type: datastore.NamespaceWatchBookmark, At: at}
	j.events = append(j.events, event)
	return event
}

func (j *capacityJournal) Bounds(context.Context) (datastore.NamespaceWatchBounds, error) {
	j.mu.RLock()
	defer j.mu.RUnlock()
	updated := time.Now().UTC()
	if len(j.events) > 0 {
		updated = j.events[len(j.events)-1].At
	}
	return datastore.NamespaceWatchBounds{Epoch: j.epoch, Oldest: 1, HighWater: uint64(len(j.events)), UpdatedAt: updated, ProgressAt: updated}, nil
}

func (j *capacityJournal) ReadAfter(_ context.Context, cursor datastore.NamespaceWatchCursor, limit int) ([]datastore.NamespaceWatchEvent, error) {
	j.mu.RLock()
	defer j.mu.RUnlock()
	start := int(cursor.Sequence)
	if start >= len(j.events) {
		return nil, nil
	}
	end := start + limit
	if end > len(j.events) {
		end = len(j.events)
	}
	return append([]datastore.NamespaceWatchEvent(nil), j.events[start:end]...), nil
}

func (*capacityJournal) Append(context.Context, datastore.NamespaceWatchLease, datastore.NamespaceWatchEvent, time.Duration) (datastore.NamespaceWatchEvent, error) {
	panic("unused")
}
func (*capacityJournal) AcquireLease(context.Context, string, time.Time, time.Duration) (datastore.NamespaceWatchLease, bool, error) {
	panic("unused")
}
func (*capacityJournal) RenewLease(context.Context, datastore.NamespaceWatchLease, time.Time, time.Duration) (datastore.NamespaceWatchLease, bool, error) {
	panic("unused")
}
func (*capacityJournal) ReleaseLease(context.Context, datastore.NamespaceWatchLease) error {
	panic("unused")
}
func (*capacityJournal) LoadProgress(context.Context, string) (datastore.NamespaceCDCProgress, error) {
	panic("unused")
}
func (*capacityJournal) SaveProgress(context.Context, datastore.NamespaceWatchLease, datastore.NamespaceCDCProgress) error {
	panic("unused")
}

func TestNamespaceWatchCapacity(t *testing.T) {
	if os.Getenv("GITSTORE_NAMESPACE_WATCH_CAPACITY_RUN") != "1" {
		t.Skip("set GITSTORE_NAMESPACE_WATCH_CAPACITY_RUN=1 for the 60-minute capacity gate")
	}
	duration := envDuration(t, "GITSTORE_NAMESPACE_WATCH_CAPACITY_DURATION", 60*time.Minute)
	subscriberCount := envInt(t, "GITSTORE_NAMESPACE_WATCH_CAPACITY_SUBSCRIBERS", 1000)
	journal := newCapacityJournal()
	for i := 0; i < 10000; i++ {
		journal.add(time.Now().UTC())
	}

	replay := NewSubscriber(journal, SubscriberConfig{ReadBatchSize: 256, MaxReplayEvents: 10000, BufferSize: 10001, PollMin: time.Millisecond, PollMax: time.Millisecond})
	replayCtx, stopReplay := context.WithCancel(context.Background())
	started := time.Now()
	replayStream, err := replay.Subscribe(replayCtx, EncodeCursor(journal.epoch, 0))
	require.NoError(t, err)
	for i := 0; i < 10000; i++ {
		select {
		case <-replayStream.Events:
		case err := <-replayStream.Errors:
			t.Fatalf("10,000-event replay failed: %v", err)
		case <-time.After(5 * time.Second):
			t.Fatal("10,000-event replay exceeded 5 seconds")
		}
	}
	replayElapsed := time.Since(started)
	stopReplay()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	type result struct {
		count atomic.Uint64
		err   atomic.Value
	}
	results := make([]result, subscriberCount)
	var latencyMu sync.Mutex
	latencies := make([]time.Duration, 0, 16*int(duration/(100*time.Millisecond)))
	var consumers sync.WaitGroup
	for i := range results {
		// Alternating Subscriber instances model two API replicas sharing one
		// durable journal while keeping every consumer independently buffered.
		subscriber := NewSubscriber(journal, SubscriberConfig{ReadBatchSize: 256, MaxReplayEvents: 100000, BufferSize: 64, PollMin: 20 * time.Millisecond, PollMax: 100 * time.Millisecond})
		stream, subscribeErr := subscriber.Subscribe(ctx, BootstrapCursor)
		require.NoError(t, subscribeErr)
		<-stream.Events // bootstrap BOOKMARK
		consumers.Add(1)
		go func(index int) {
			defer consumers.Done()
			for {
				select {
				case event, ok := <-stream.Events:
					if !ok {
						return
					}
					results[index].count.Add(1)
					if index < 16 {
						latencyMu.Lock()
						latencies = append(latencies, time.Since(event.At))
						latencyMu.Unlock()
					}
				case terminal := <-stream.Errors:
					if terminal != nil {
						results[index].err.Store(terminal)
					}
					return
				}
			}
		}(i)
	}
	runtime.GC()
	var memoryStart runtime.MemStats
	runtime.ReadMemStats(&memoryStart)
	cpuStart := processCPUSample()
	soakStarted := time.Now()

	producer := time.NewTicker(100 * time.Millisecond) // sustained 10/s
	defer producer.Stop()
	burst := time.NewTicker(time.Minute)
	defer burst.Stop()
	deadline := time.NewTimer(duration)
	defer deadline.Stop()
	produced := uint64(0)
produce:
	for {
		select {
		case now := <-producer.C:
			journal.add(now.UTC())
			produced++
		case <-burst.C:
			for i := 0; i < 100; i++ {
				journal.add(time.Now().UTC())
				produced++
				time.Sleep(10 * time.Millisecond) // 100/s burst
			}
		case <-deadline.C:
			break produce
		}
	}

	catchup := time.Now().Add(3 * time.Second)
	for time.Now().Before(catchup) {
		complete := true
		for i := range results {
			complete = complete && results[i].count.Load() == produced
		}
		if complete {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	consumers.Wait()
	wallSeconds := time.Since(soakStarted).Seconds()
	cpuPercent := normalizedUtilizedCPUPercent(cpuStart, processCPUSample(), wallSeconds, runtime.GOMAXPROCS(0))
	runtime.GC()
	var memoryEnd runtime.MemStats
	runtime.ReadMemStats(&memoryEnd)
	latencyMu.Lock()
	observed := append([]time.Duration(nil), latencies...)
	latencyMu.Unlock()
	sort.Slice(observed, func(i, j int) bool { return observed[i] < observed[j] })
	for i := range results {
		require.Nilf(t, results[i].err.Load(), "subscriber %d terminated", i)
		require.Equalf(t, produced, results[i].count.Load(), "subscriber %d missed events", i)
	}
	require.NotEmpty(t, observed)
	require.LessOrEqual(t, percentile(observed, 0.95), time.Second)
	require.LessOrEqual(t, percentile(observed, 0.99), 3*time.Second)
	require.LessOrEqual(t, replayElapsed, 5*time.Second)
	require.Less(t, cpuPercent, 80.0, "aggregate normalized CPU must remain below 80%%")
	require.LessOrEqual(t, memoryEnd.HeapAlloc, memoryStart.HeapAlloc+memoryStart.HeapAlloc/10, "retained heap growth must remain at or below 10%%")
	t.Logf("duration=%s subscribers=%d events=%d replay=%s p95=%s p99=%s cpu=%.2f%% heap_start=%d heap_end=%d", duration, subscriberCount, produced, replayElapsed, percentile(observed, .95), percentile(observed, .99), cpuPercent, memoryStart.HeapAlloc, memoryEnd.HeapAlloc)
}

type processCPUTime struct {
	total float64
	idle  float64
}

func processCPUSample() processCPUTime {
	samples := []runtimemetrics.Sample{
		{Name: "/cpu/classes/total:cpu-seconds"},
		{Name: "/cpu/classes/idle:cpu-seconds"},
	}
	runtimemetrics.Read(samples)
	return processCPUTime{total: samples[0].Value.Float64(), idle: samples[1].Value.Float64()}
}

func normalizedUtilizedCPUPercent(start, end processCPUTime, wallSeconds float64, gomaxprocs int) float64 {
	if wallSeconds <= 0 || gomaxprocs <= 0 {
		return 0
	}

	// runtime/metrics defines total as GOMAXPROCS integrated over wall time
	// and idle as the portion of that capacity unused by Go or the runtime.
	// Both are estimates, so clamp small under/overshoots to a percentage.
	utilizedSeconds := (end.total - start.total) - (end.idle - start.idle)
	percent := 100 * utilizedSeconds / (wallSeconds * float64(gomaxprocs))
	return min(100, max(0, percent))
}

func TestNormalizedUtilizedCPUPercent(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		start       processCPUTime
		end         processCPUTime
		wallSeconds float64
		gomaxprocs  int
		want        float64
	}{
		{name: "subtracts idle capacity", start: processCPUTime{total: 10, idle: 4}, end: processCPUTime{total: 18, idle: 10}, wallSeconds: 2, gomaxprocs: 4, want: 25},
		{name: "clamps idle overestimate", start: processCPUTime{total: 10, idle: 4}, end: processCPUTime{total: 18, idle: 13}, wallSeconds: 2, gomaxprocs: 4, want: 0},
		{name: "clamps capacity overestimate", start: processCPUTime{}, end: processCPUTime{total: 9}, wallSeconds: 2, gomaxprocs: 4, want: 100},
		{name: "zero wall duration", end: processCPUTime{total: 8}, gomaxprocs: 4, want: 0},
		{name: "zero processors", end: processCPUTime{total: 8}, wallSeconds: 2, want: 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, normalizedUtilizedCPUPercent(tt.start, tt.end, tt.wallSeconds, tt.gomaxprocs))
		})
	}
}

func percentile(values []time.Duration, p float64) time.Duration {
	index := int(float64(len(values)-1) * p)
	return values[index]
}

func envDuration(t *testing.T, key string, fallback time.Duration) time.Duration {
	t.Helper()
	if raw := os.Getenv(key); raw != "" {
		value, err := time.ParseDuration(raw)
		require.NoError(t, err)
		return value
	}
	return fallback
}

func envInt(t *testing.T, key string, fallback int) int {
	t.Helper()
	if raw := os.Getenv(key); raw != "" {
		value, err := strconv.Atoi(raw)
		require.NoError(t, err)
		return value
	}
	return fallback
}
