// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package watchjournal

import (
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
	replayEvents     prometheus.Counter
	replayLatency    prometheus.Histogram
	bookmarkAge      prometheus.GaugeFunc
	bookmarkNanos    atomic.Int64
	deliveryLatency  prometheus.Histogram
	collectors       []prometheus.Collector
}

func NewMetrics(registerer prometheus.Registerer) (*Metrics, error) {
	m := &Metrics{
		leader:          prometheus.NewGauge(prometheus.GaugeOpts{Name: "gitstore_namespace_watch_materializer_leader", Help: "Whether this replica owns the fenced Namespace CDC materializer lease."}),
		journalOldest:   prometheus.NewGauge(prometheus.GaugeOpts{Name: "gitstore_namespace_watch_journal_oldest_sequence", Help: "Oldest retained Namespace journal sequence."}),
		journalHigh:     prometheus.NewGauge(prometheus.GaugeOpts{Name: "gitstore_namespace_watch_journal_high_water_sequence", Help: "Current Namespace journal high-water sequence."}),
		subscribers:     prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: "gitstore_namespace_watch_subscribers", Help: "Active Namespace watch subscribers."}, []string{"path"}),
		expired:         prometheus.NewCounterVec(prometheus.CounterOpts{Name: "gitstore_namespace_watch_expired_total", Help: "Namespace watches terminated because continuity was not provable."}, []string{"reason"}),
		overflow:        prometheus.NewCounter(prometheus.CounterOpts{Name: "gitstore_namespace_watch_overflow_total", Help: "Namespace subscriber buffer overflows."}),
		appendErrors:    prometheus.NewCounter(prometheus.CounterOpts{Name: "gitstore_namespace_watch_append_errors_total", Help: "Namespace journal append failures."}),
		replayEvents:    prometheus.NewCounter(prometheus.CounterOpts{Name: "gitstore_namespace_watch_replay_events_total", Help: "Namespace journal events replayed."}),
		replayLatency:   prometheus.NewHistogram(prometheus.HistogramOpts{Name: "gitstore_namespace_watch_replay_duration_seconds", Help: "Namespace journal replay duration."}),
		deliveryLatency: prometheus.NewHistogram(prometheus.HistogramOpts{Name: "gitstore_namespace_watch_delivery_latency_seconds", Help: "Namespace CDC-to-subscriber delivery latency."}),
	}
	m.cdcLag = prometheus.NewGaugeFunc(prometheus.GaugeOpts{Name: "gitstore_namespace_watch_cdc_lag_seconds", Help: "Age of the latest observed Namespace CDC position."}, func() float64 {
		nanos := m.cdcProgressNanos.Load()
		if nanos == 0 {
			return 0
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
			return 0
		}
		age := time.Since(time.Unix(0, nanos)).Seconds()
		if age < 0 {
			return 0
		}
		return age
	})
	m.collectors = []prometheus.Collector{m.leader, m.cdcLag, m.journalOldest, m.journalHigh, m.subscribers, m.expired, m.overflow, m.appendErrors, m.replayEvents, m.replayLatency, m.bookmarkAge, m.deliveryLatency}
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
func (m *Metrics) ObserveDelivery(eventAt, now time.Time) {
	if !eventAt.IsZero() && !now.Before(eventAt) {
		m.deliveryLatency.Observe(now.Sub(eventAt).Seconds())
	}
}
func (m *Metrics) SetBounds(bounds datastore.NamespaceWatchBounds, now time.Time) {
	m.journalOldest.Set(float64(bounds.Oldest))
	m.journalHigh.Set(float64(bounds.HighWater))
	if !bounds.ProgressAt.IsZero() && !now.Before(bounds.ProgressAt) {
		m.ObserveCDCProgress(bounds.ProgressAt)
	}
	if !bounds.UpdatedAt.IsZero() && !now.Before(bounds.UpdatedAt) {
		m.ObserveBookmark(bounds.UpdatedAt)
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
	if !at.IsZero() {
		m.cdcProgressNanos.Store(at.UnixNano())
	}
}
