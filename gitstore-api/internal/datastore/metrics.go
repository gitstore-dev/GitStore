// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package datastore

import (
	"context"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

type NamespaceFencePhase uint8

const (
	NamespaceFenceLookup NamespaceFencePhase = iota
	NamespaceFenceQueue
	NamespaceFenceReserve
	NamespaceFenceMutation
	NamespaceFenceRelease
	NamespaceFenceReserveRead
	NamespaceFenceReserveCAS
	NamespaceFenceReserveBusyWait
	NamespaceFenceReserveConflictWait
)

type namespaceFenceObserverKey struct{}
type namespaceFenceObserver func(NamespaceFencePhase, time.Time, error)

type fileMutationCohortStarterKey struct{}
type fileMutationCohortStarter func(context.Context) (context.Context, func(error))
type fileMutationCohortObserverKey struct{}
type fileMutationCohortObserver func(context.Context, int, int)

// StartFileMutationCohort observes one shared reservation without changing its
// cancellation or admission context.
func StartFileMutationCohort(ctx context.Context) (context.Context, func(error)) {
	if start, ok := ctx.Value(fileMutationCohortStarterKey{}).(fileMutationCohortStarter); ok {
		return start(ctx)
	}
	return ctx, func(error) {}
}

// ObserveFileMutationCohort links each selected callback to its physical owner.
func ObserveFileMutationCohort(ctx, owner context.Context, index, count int) {
	if observe, ok := ctx.Value(fileMutationCohortObserverKey{}).(fileMutationCohortObserver); ok {
		observe(owner, index, count)
	}
}

type CatalogMutationPhase uint8

const (
	CatalogMutationParentCheck CatalogMutationPhase = iota
	CatalogMutationReadBefore
	CatalogMutationMembershipWrite
	CatalogMutationReadAfter
	FileMutationLocatorRead
	FileMutationAuthoritativeCAS
	FileMutationOwnerReferenceSync
	FileMutationConditionalBatch2
	FileMutationConditionalBatch3
	FileMutationConditionalBatch4
	FileMutationConditionalBatch5
	FileMutationConditionalBatch6
	FileMutationConditionalBatch7
	FileMutationConditionalBatch8
	FileMutationConditionalBatch9
	FileMutationConditionalBatch10
	FileMutationConditionalBatch11
	FileMutationConditionalBatch12
	FileMutationConditionalBatch13
	FileMutationConditionalBatch14
	FileMutationConditionalBatch15
	FileMutationConditionalBatch16
	FileMutationConditionalQueue
	FileMutationConditionalSingle
	FileMutationSnapshotRead
	FileProjectionReadQueue
	FileProjectionReadBatch1
	FileProjectionReadBatch2
	FileProjectionReadBatch3
	FileProjectionReadBatch4
	FileProjectionReadBatch5
	FileProjectionReadBatch6
	FileProjectionReadBatch7
	FileProjectionReadBatch8
	FileProjectionReadBatch9
	FileProjectionReadBatch10
	FileProjectionReadBatch11
	FileProjectionReadBatch12
	FileProjectionReadBatch13
	FileProjectionReadBatch14
	FileProjectionReadBatch15
	FileProjectionReadBatch16
)

type catalogMutationObserverKey struct{}
type catalogMutationObserver func(CatalogMutationPhase, time.Time, error)

// StartCatalogMutationPhase returns a completion hook for work handed to another
// goroutine, or nil when no observer is installed.
func StartCatalogMutationPhase(ctx context.Context, phase CatalogMutationPhase) func(error) {
	observer, ok := ctx.Value(catalogMutationObserverKey{}).(catalogMutationObserver)
	if !ok {
		return nil
	}
	start := time.Now()
	return func(err error) { observer(phase, start, err) }
}

func MeasureCatalogMutationPhase(ctx context.Context, phase CatalogMutationPhase, operation func() error) error {
	observer, ok := ctx.Value(catalogMutationObserverKey{}).(catalogMutationObserver)
	if !ok {
		return operation()
	}
	start := time.Now()
	err := operation()
	observer(phase, start, err)
	return err
}

// MeasureNamespaceFencePhase reports only work actually attempted; retained
// repair fences therefore have no release sample.
func MeasureNamespaceFencePhase(ctx context.Context, phase NamespaceFencePhase, operation func() error) error {
	observer, ok := ctx.Value(namespaceFenceObserverKey{}).(namespaceFenceObserver)
	if !ok {
		return operation()
	}
	start := time.Now()
	err := operation()
	observer(phase, start, err)
	return err
}

type datastoreMetrics struct {
	duration             *prometheus.HistogramVec
	errors               *prometheus.CounterVec
	projectionFailures   *prometheus.CounterVec
	compensationAttempts *prometheus.CounterVec
	compensationFailures *prometheus.CounterVec
	findings             *prometheus.CounterVec
}

func newMetrics(reg prometheus.Registerer) *datastoreMetrics {
	metrics := &datastoreMetrics{}
	metrics.duration = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Namespace: "gitstore",
		Subsystem: "datastore",
		Name:      "operation_duration_seconds",
		Help:      "Latency of datastore operations.",
		Buckets:   prometheus.DefBuckets,
	}, []string{"operation", "backend"})

	metrics.errors = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: "gitstore",
		Subsystem: "datastore",
		Name:      "operation_errors_total",
		Help:      "Total datastore operation errors.",
	}, []string{"operation", "backend"})

	metrics.projectionFailures = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: "gitstore",
		Subsystem: "datastore",
		Name:      "projection_write_failures_total",
		Help:      "Projection write failures requiring compensation or repair.",
	}, []string{"operation", "backend", "resource_kind", "projection"})

	metrics.compensationAttempts = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: "gitstore",
		Subsystem: "datastore",
		Name:      "compensation_attempts_total",
		Help:      "Mutation compensation attempts.",
	}, []string{"operation", "backend", "resource_kind", "projection"})

	metrics.compensationFailures = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: "gitstore",
		Subsystem: "datastore",
		Name:      "compensation_failures_total",
		Help:      "Mutation compensation failures requiring operator repair.",
	}, []string{"operation", "backend", "resource_kind", "projection"})

	metrics.findings = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: "gitstore",
		Subsystem: "datastore",
		Name:      "projection_findings_total",
		Help:      "Observed missing, dangling, duplicate, or stale projections.",
	}, []string{"operation", "backend", "resource_kind", "projection", "finding_type"})

	reg.MustRegister(
		metrics.duration,
		metrics.errors,
		metrics.projectionFailures,
		metrics.compensationAttempts,
		metrics.compensationFailures,
		metrics.findings,
	)
	return metrics
}
