// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package watchjournal

import (
	"math"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	"github.com/prometheus/client_golang/prometheus"
)

// Metrics contains bounded-cardinality Namespace watch signals.
type Metrics struct {
	leader           prometheus.Gauge
	cdcLag           prometheus.GaugeFunc
	cdcProgressNanos atomic.Int64
	journalOldest    prometheus.Gauge
	journalHigh      prometheus.Gauge
	subscribers      *prometheus.GaugeVec
	expired          *prometheus.CounterVec
	overflow         prometheus.Counter
	appendErrors     prometheus.Counter
	duplicates       prometheus.Counter
	replayEvents     prometheus.Counter
	replayLatency    prometheus.Histogram
	bookmarkAge      prometheus.GaugeFunc
	bookmarkNanos    atomic.Int64
	deliveryLatency  prometheus.Histogram
	duplicateMu      sync.Mutex
	duplicateSeen    map[deliveryIdentity]struct{}
	duplicateKeys    map[string]int
	duplicateOrder   []deliveryIdentity
	duplicateCursor  int
	collectors       []prometheus.Collector
}

type deliveryIdentity struct {
	epoch    string
	key      string
	sequence uint64
}

const duplicateTrackingLimit = 100000

func NewMetrics(registerer prometheus.Registerer) (*Metrics, error) {
	m := &Metrics{
		leader:          prometheus.NewGauge(prometheus.GaugeOpts{Name: "gitstore_namespace_watch_materializer_leader", Help: "Whether this replica owns the fenced Namespace CDC materializer lease."}),
		journalOldest:   prometheus.NewGauge(prometheus.GaugeOpts{Name: "gitstore_namespace_watch_journal_oldest_sequence", Help: "Oldest retained Namespace journal sequence."}),
		journalHigh:     prometheus.NewGauge(prometheus.GaugeOpts{Name: "gitstore_namespace_watch_journal_high_water_sequence", Help: "Current Namespace journal high-water sequence."}),
		subscribers:     prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: "gitstore_namespace_watch_subscribers", Help: "Active Namespace watch subscribers."}, []string{"path"}),
		expired:         prometheus.NewCounterVec(prometheus.CounterOpts{Name: "gitstore_namespace_watch_expired_total", Help: "Namespace watches terminated because continuity was not provable."}, []string{"reason"}),
		overflow:        prometheus.NewCounter(prometheus.CounterOpts{Name: "gitstore_namespace_watch_overflow_total", Help: "Namespace subscriber buffer overflows."}),
		appendErrors:    prometheus.NewCounter(prometheus.CounterOpts{Name: "gitstore_namespace_watch_append_errors_total", Help: "Namespace journal append failures."}),
		duplicates:      prometheus.NewCounter(prometheus.CounterOpts{Name: "gitstore_namespace_watch_duplicates_total", Help: "Distinct Namespace journal cursors delivered with a previously observed deduplication key."}),
		replayEvents:    prometheus.NewCounter(prometheus.CounterOpts{Name: "gitstore_namespace_watch_replay_events_total", Help: "Namespace journal events replayed."}),
		replayLatency:   prometheus.NewHistogram(prometheus.HistogramOpts{Name: "gitstore_namespace_watch_replay_duration_seconds", Help: "Namespace journal replay duration."}),
		deliveryLatency: prometheus.NewHistogram(prometheus.HistogramOpts{Name: "gitstore_namespace_watch_delivery_latency_seconds", Help: "Namespace CDC-to-subscriber delivery latency."}),
	}
	m.cdcLag = prometheus.NewGaugeFunc(prometheus.GaugeOpts{Name: "gitstore_namespace_watch_cdc_lag_seconds", Help: "Age of the latest observed Namespace CDC position."}, func() float64 {
		nanos := m.cdcProgressNanos.Load()
		if nanos == 0 {
			return math.Inf(1)
		}
		age := time.Since(time.Unix(0, nanos)).Seconds()
		if age < 0 {
			return 0
		}
		return age
	})
	m.bookmarkAge = prometheus.NewGaugeFunc(prometheus.GaugeOpts{Name: "gitstore_namespace_watch_bookmark_age_seconds", Help: "Age of the latest durable Namespace bookmark."}, func() float64 {
		nanos := m.bookmarkNanos.Load()
		if nanos == 0 {
			return math.Inf(1)
		}
		age := time.Since(time.Unix(0, nanos)).Seconds()
		if age < 0 {
			return 0
		}
		return age
	})
	m.duplicateSeen = make(map[deliveryIdentity]struct{}, duplicateTrackingLimit)
	m.duplicateKeys = make(map[string]int, duplicateTrackingLimit)
	m.duplicateOrder = make([]deliveryIdentity, 0, duplicateTrackingLimit)
	m.collectors = []prometheus.Collector{m.leader, m.cdcLag, m.journalOldest, m.journalHigh, m.subscribers, m.expired, m.overflow, m.appendErrors, m.duplicates, m.replayEvents, m.replayLatency, m.bookmarkAge, m.deliveryLatency}
	for _, collector := range m.collectors {
		if err := registerer.Register(collector); err != nil {
			return nil, err
		}
	}
	return m, nil
}

func (m *Metrics) Describe(ch chan<- *prometheus.Desc) {
	for _, collector := range m.collectors {
		collector.Describe(ch)
	}
}
func (m *Metrics) Collect(ch chan<- prometheus.Metric) {
	for _, collector := range m.collectors {
		collector.Collect(ch)
	}
}
func (m *Metrics) SetLeader(active bool) {
	if active {
		m.leader.Set(1)
	} else {
		m.leader.Set(0)
	}
}
func (m *Metrics) SetSubscribers(path string, count float64) {
	m.subscribers.WithLabelValues(path).Set(count)
}
func (m *Metrics) IncSubscribers(path string) { m.subscribers.WithLabelValues(path).Inc() }
func (m *Metrics) DecSubscribers(path string) { m.subscribers.WithLabelValues(path).Dec() }
func (m *Metrics) IncExpiry(reason Reason)    { m.expired.WithLabelValues(string(reason)).Inc() }
func (m *Metrics) IncOverflow()               { m.overflow.Inc() }
func (m *Metrics) IncAppendError()            { m.appendErrors.Inc() }
func (m *Metrics) ObserveReplay(events int, elapsed time.Duration) {
	m.replayEvents.Add(float64(events))
	m.replayLatency.Observe(elapsed.Seconds())
}
func (m *Metrics) ObserveDelivery(event datastore.NamespaceWatchEvent, now time.Time) {
	if !event.At.IsZero() && !now.Before(event.At) {
		m.deliveryLatency.Observe(now.Sub(event.At).Seconds())
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
		m.duplicates.Inc()
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
	m.journalOldest.Set(float64(bounds.Oldest))
	m.journalHigh.Set(float64(bounds.HighWater))
	if !bounds.ProgressAt.IsZero() && !now.Before(bounds.ProgressAt) {
		m.ObserveCDCProgress(bounds.ProgressAt)
	}
	if !bounds.BookmarkAt.IsZero() && !now.Before(bounds.BookmarkAt) {
		m.ObserveBookmark(bounds.BookmarkAt)
	}
}
func (m *Metrics) ObserveMaterialized(event datastore.NamespaceWatchEvent, now time.Time) {
	m.journalHigh.Set(float64(event.Sequence))
	if event.Type == datastore.NamespaceWatchBookmark && !event.At.IsZero() && !now.Before(event.At) {
		m.ObserveBookmark(event.At)
	}
}

// ObserveBookmark records the latest durable bookmark timestamp. The
// GaugeFunc derives age at scrape time so a stalled bookmark producer is
// visible even when no subscriber opens and refreshes journal bounds.
func (m *Metrics) ObserveBookmark(at time.Time) {
	if !at.IsZero() {
		m.bookmarkNanos.Store(at.UnixNano())
	}
}

// ObserveCDCProgress records the persisted progress timestamp. The GaugeFunc
// computes its age when Prometheus scrapes, so an idle or stuck reader's lag
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
