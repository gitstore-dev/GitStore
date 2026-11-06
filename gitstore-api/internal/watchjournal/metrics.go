// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package watchjournal

import (
	"context"
	"math"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gitstore-dev/gitstore/observability"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"

	"github.com/gitstore-dev/gitstore/api/internal/datastore"
)

// Metrics contains bounded-cardinality Namespace watch signals.
type Metrics struct {
	leader           metric.Float64Gauge
	cdcLag           metric.Float64ObservableGauge
	cdcProgressNanos atomic.Int64
	journalOldest    metric.Float64Gauge
	journalHigh      metric.Float64Gauge
	subscribers      metric.Int64ObservableGauge
	expired          metric.Float64Counter
	overflow         metric.Float64Counter
	appendErrors     metric.Float64Counter
	duplicates       metric.Float64Counter
	replayEvents     metric.Float64Counter
	replayLatency    metric.Float64Histogram
	bookmarkAge      metric.Float64ObservableGauge
	bookmarkNanos    atomic.Int64
	deliveryLatency  metric.Float64Histogram
	cdcDiscovery     metric.Float64Histogram
	materialize      metric.Float64Histogram
	batchSize        metric.Float64Histogram
	duplicateMu      sync.Mutex
	duplicateSeen    map[deliveryIdentity]struct{}
	duplicateKeys    map[string]int
	duplicateOrder   []deliveryIdentity
	duplicateCursor  int
	registration     metric.Registration
	subscriberCounts [3]atomic.Int64
	subscriberSeen   [3]atomic.Bool
}

type deliveryIdentity struct {
	epoch    string
	key      string
	sequence uint64
}

const duplicateTrackingLimit = 100000

var capacityLatencyBuckets = []float64{.001, .005, .01, .025, .05, .1, .25, .5, .75, 1, 1.5, 2, 3, 5}

func NewMetrics(meter metric.Meter) (*Metrics, error) {
	m := &Metrics{
		leader:          observability.MustInstrument(meter.Float64Gauge("gitstore_resource_watch_materializer_leader", metric.WithDescription("Whether this replica owns the fenced resource watch CDC materializer lease."))),
		journalOldest:   observability.MustInstrument(meter.Float64Gauge("gitstore_resource_watch_journal_oldest_sequence", metric.WithDescription("Oldest retained resource watch journal sequence."))),
		journalHigh:     observability.MustInstrument(meter.Float64Gauge("gitstore_resource_watch_journal_high_water_sequence", metric.WithDescription("Current resource watch journal high-water sequence."))),
		subscribers:     observability.MustInstrument(meter.Int64ObservableGauge("gitstore_resource_watch_subscribers", metric.WithDescription("Active resource watch subscribers."))),
		expired:         observability.MustInstrument(meter.Float64Counter("gitstore_resource_watch_expired_total", metric.WithDescription("Resource watches terminated because continuity was not provable."))),
		overflow:        observability.MustInstrument(meter.Float64Counter("gitstore_resource_watch_overflow_total", metric.WithDescription("Resource watch subscriber buffer overflows."))),
		appendErrors:    observability.MustInstrument(meter.Float64Counter("gitstore_resource_watch_append_errors_total", metric.WithDescription("Resource watch journal append failures."))),
		duplicates:      observability.MustInstrument(meter.Float64Counter("gitstore_resource_watch_duplicates_total", metric.WithDescription("Distinct resource watch journal cursors delivered with a previously observed deduplication key."))),
		replayEvents:    observability.MustInstrument(meter.Float64Counter("gitstore_resource_watch_replay_events_total", metric.WithDescription("Resource watch journal events replayed."))),
		replayLatency:   observability.MustInstrument(meter.Float64Histogram("gitstore_resource_watch_replay_duration_seconds", metric.WithDescription("Resource watch journal replay duration."), metric.WithUnit("s"), metric.WithExplicitBucketBoundaries(capacityLatencyBuckets...))),
		deliveryLatency: observability.MustInstrument(meter.Float64Histogram("gitstore_resource_watch_delivery_latency_seconds", metric.WithDescription("Resource watch CDC-to-subscriber delivery latency."), metric.WithUnit("s"), metric.WithExplicitBucketBoundaries(capacityLatencyBuckets...))),
		cdcDiscovery:    observability.MustInstrument(meter.Float64Histogram("gitstore_resource_watch_cdc_discovery_seconds", metric.WithDescription("Time from the authoritative CDC timestamp until materialization begins."), metric.WithUnit("s"), metric.WithExplicitBucketBoundaries(capacityLatencyBuckets...))),
		materialize:     observability.MustInstrument(meter.Float64Histogram("gitstore_resource_watch_materializer_stage_duration_seconds", metric.WithDescription("Resource watch materializer latency at bounded stages."), metric.WithUnit("s"), metric.WithExplicitBucketBoundaries(capacityLatencyBuckets...))),
		batchSize:       observability.MustInstrument(meter.Float64Histogram("gitstore_resource_watch_materializer_batch_size", metric.WithDescription("Number of public resource watch events in each materializer batch."), metric.WithExplicitBucketBoundaries(1, 2, 4, 8, 16, 32, 64, 128, 256))),
	}
	m.cdcLag = observability.MustInstrument(meter.Float64ObservableGauge("gitstore_resource_watch_cdc_lag_seconds", metric.WithUnit("s"), metric.WithDescription("Age of the latest observed resource watch CDC position.")))
	m.bookmarkAge = observability.MustInstrument(meter.Float64ObservableGauge("gitstore_resource_watch_bookmark_age_seconds", metric.WithUnit("s"), metric.WithDescription("Age of the latest durable resource watch bookmark.")))
	registration, err := meter.RegisterCallback(func(ctx context.Context, observer metric.Observer) error {
		age := func(nanos int64) float64 {
			if nanos == 0 {
				return math.Inf(1)
			}
			return max(0, time.Since(time.Unix(0, nanos)).Seconds())
		}
		observer.ObserveFloat64(m.cdcLag, age(m.cdcProgressNanos.Load()))
		observer.ObserveFloat64(m.bookmarkAge, age(m.bookmarkNanos.Load()))
		for i, path := range []string{"typed", "generic", "internal"} {
			if m.subscriberSeen[i].Load() {
				observer.ObserveInt64(m.subscribers, m.subscriberCounts[i].Load(), metric.WithAttributes(attribute.String("path", path)))
			}
		}
		return nil
	}, m.cdcLag, m.bookmarkAge, m.subscribers)
	if err != nil {
		return nil, err
	}
	m.registration = registration
	m.duplicateSeen = make(map[deliveryIdentity]struct{}, duplicateTrackingLimit)
	m.duplicateKeys = make(map[string]int, duplicateTrackingLimit)
	m.duplicateOrder = make([]deliveryIdentity, 0, duplicateTrackingLimit)
	return m, nil
}

// Close releases the SDK observation callback owned by this watch runtime.
func (m *Metrics) Close() error { return m.registration.Unregister() }

func (m *Metrics) SetLeader(active bool) {
	if active {
		m.leader.Record(context.Background(), 1)
	} else {
		m.leader.Record(context.Background(), 0)
	}
}
func subscriberPathIndex(path string) int {
	switch path {
	case "typed":
		return 0
	case "generic":
		return 1
	default:
		return 2
	}
}
func (m *Metrics) SetSubscribers(path string, count float64) {
	i := subscriberPathIndex(path)
	m.subscriberCounts[i].Store(int64(count))
	m.subscriberSeen[i].Store(true)
}
func (m *Metrics) IncSubscribers(path string) {
	i := subscriberPathIndex(path)
	m.subscriberCounts[i].Add(1)
	m.subscriberSeen[i].Store(true)
}
func (m *Metrics) DecSubscribers(path string) {
	i := subscriberPathIndex(path)
	m.subscriberCounts[i].Add(-1)
	m.subscriberSeen[i].Store(true)
}
func (m *Metrics) IncExpiry(reason Reason) {
	m.expired.Add(context.Background(), 1, metric.WithAttributes(attribute.String("reason", string(reason))))
}
func (m *Metrics) IncOverflow()    { m.overflow.Add(context.Background(), 1) }
func (m *Metrics) IncAppendError() { m.appendErrors.Add(context.Background(), 1) }
func (m *Metrics) ObserveReplay(events int, elapsed time.Duration) {
	m.replayEvents.Add(context.Background(), float64(events))
	m.replayLatency.Record(context.Background(), elapsed.Seconds())
}
func (m *Metrics) ObserveCDCDiscovery(at, now time.Time) {
	if !at.IsZero() && !now.Before(at) {
		m.cdcDiscovery.Record(context.Background(), now.Sub(at).Seconds())
	}
}
func (m *Metrics) ObserveMaterializerStage(stage string, elapsed time.Duration) {
	m.materialize.Record(context.Background(), elapsed.Seconds(), metric.WithAttributes(attribute.String("stage", stage)))
}
func (m *Metrics) ObserveMaterializerBatchSize(size int) {
	m.batchSize.Record(context.Background(), float64(size))
}
func (m *Metrics) ObserveDelivery(event datastore.NamespaceWatchEvent, now time.Time) {
	if !event.At.IsZero() && !now.Before(event.At) {
		m.deliveryLatency.Record(context.Background(), now.Sub(event.At).Seconds())
	}
	m.observeDuplicateDelivery(event)
}

func (m *Metrics) observeDuplicateDelivery(event datastore.NamespaceWatchEvent) {
	if event.DeduplicationKey == "" || event.Type == datastore.NamespaceWatchBookmark {
		return
	}
	identity := deliveryIdentity{epoch: event.Epoch, key: event.DeduplicationKey, sequence: event.Sequence}
	m.duplicateMu.Lock()
	defer m.duplicateMu.Unlock()
	if _, exists := m.duplicateSeen[identity]; exists {
		return
	}
	if m.duplicateKeys[identity.key] > 0 {
		m.duplicates.Add(context.Background(), 1)
	}
	if len(m.duplicateOrder) < duplicateTrackingLimit {
		m.duplicateOrder = append(m.duplicateOrder, identity)
	} else {
		evicted := m.duplicateOrder[m.duplicateCursor]
		delete(m.duplicateSeen, evicted)
		m.duplicateKeys[evicted.key]--
		if m.duplicateKeys[evicted.key] == 0 {
			delete(m.duplicateKeys, evicted.key)
		}
		m.duplicateOrder[m.duplicateCursor] = identity
		m.duplicateCursor = (m.duplicateCursor + 1) % duplicateTrackingLimit
	}
	m.duplicateSeen[identity] = struct{}{}
	m.duplicateKeys[identity.key]++
}
func (m *Metrics) SetBounds(bounds datastore.NamespaceWatchBounds, now time.Time) {
	m.journalOldest.Record(context.Background(), float64(bounds.Oldest))
	m.journalHigh.Record(context.Background(), float64(bounds.HighWater))
	if !bounds.ProgressAt.IsZero() && !now.Before(bounds.ProgressAt) {
		m.ObserveCDCProgress(bounds.ProgressAt)
	}
	if !bounds.BookmarkAt.IsZero() && !now.Before(bounds.BookmarkAt) {
		m.ObserveBookmark(bounds.BookmarkAt)
	}
}
func (m *Metrics) ObserveMaterialized(event datastore.NamespaceWatchEvent, now time.Time) {
	m.journalHigh.Record(context.Background(), float64(event.Sequence))
	if event.Type == datastore.NamespaceWatchBookmark && !event.At.IsZero() && !now.Before(event.At) {
		m.ObserveBookmark(event.At)
	}
}

// ObserveBookmark records the latest durable bookmark timestamp. The
// SDK callback derives age at collection time so a stalled bookmark producer is
// visible even when no subscriber opens and refreshes journal bounds.
func (m *Metrics) ObserveBookmark(at time.Time) {
	if !at.IsZero() {
		m.bookmarkNanos.Store(at.UnixNano())
	}
}

// ObserveCDCProgress records the persisted progress timestamp. The SDK callback
// computes its age at collection time, so an idle or stuck reader's lag
// continues increasing instead of freezing at the last observation.
func (m *Metrics) ObserveCDCProgress(at time.Time) {
	if at.IsZero() {
		return
	}
	next := at.UnixNano()
	for current := m.cdcProgressNanos.Load(); next > current; current = m.cdcProgressNanos.Load() {
		if m.cdcProgressNanos.CompareAndSwap(current, next) {
			return
		}
	}
}
