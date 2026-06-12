// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

// Package manager wires a work queue and worker pool to a Reconciler for each
// registered resource kind.
package manager

import (
	"context"
	"sync"
	"time"

	"github.com/gitstore-dev/gitstore/controller-manager/internal/health"
	"github.com/gitstore-dev/gitstore/controller-manager/internal/queue"
	"github.com/gitstore-dev/gitstore/controller-manager/internal/retry"
	"github.com/gitstore-dev/gitstore/controller-manager/internal/types"
	"github.com/gitstore-dev/gitstore/controller-manager/internal/worker"
	"go.uber.org/zap"
)

const (
	defaultMaxAttempts     = 5
	defaultInitialInterval = 500 * time.Millisecond
	defaultMaxInterval     = 30 * time.Second
	defaultMultiplier      = 2.0
	defaultStallThreshold  = 5 * time.Minute
	defaultWorkerCount     = 4
)

// kindState holds the per-kind runtime state.
type kindState struct {
	reg        ReconcilerRegistration
	q          *queue.Queue
	pool       *worker.Pool
	quarantine *retry.QuarantineStore

	mu          sync.Mutex
	lastSuccess time.Time
}

// Manager supervises one controller (queue + pool + reconciler) per registered kind.
type Manager struct {
	kinds map[string]*kindState
	log   *zap.Logger
}

// New creates an uninitialised Manager. Call Register for each kind, then Start.
func New() *Manager {
	return &Manager{
		kinds: make(map[string]*kindState),
		log:   zap.NewNop(),
	}
}

// WithLogger attaches a structured logger.
func (m *Manager) WithLogger(log *zap.Logger) *Manager {
	m.log = log
	return m
}

// Register wires a reconciler for the given resource kind.
// Must be called before Start.
func (m *Manager) Register(reg ReconcilerRegistration) {
	applyDefaults(&reg)
	m.kinds[reg.Kind] = &kindState{
		reg:        reg,
		q:          queue.New(1000, 0),
		pool:       worker.New(reg.WorkerCount),
		quarantine: retry.NewQuarantineStore(),
	}
	// Pre-initialise gauges so they appear in /metrics before the first poll.
	health.ActiveWorkers.WithLabelValues(reg.Kind).Set(0)
	health.QueueDepth.WithLabelValues(reg.Kind).Set(0)
	health.PoisonItemsTotal.WithLabelValues(reg.Kind).Set(0)
}

// Enqueue adds key to the queue of its kind.
// Returns ErrKindNotRegistered if no reconciler is registered for key.Kind.
func (m *Manager) Enqueue(key WorkItemKey) error {
	ks, ok := m.kinds[key.Kind]
	if !ok {
		return types.ErrKindNotRegistered
	}
	return ks.q.Enqueue(key)
}

// IsQuarantined reports whether the key is currently in the quarantine store.
func (m *Manager) IsQuarantined(key WorkItemKey) bool {
	ks, ok := m.kinds[key.Kind]
	if !ok {
		return false
	}
	_, exists := ks.quarantine.Get(key)
	return exists
}

// Requeue removes the key from quarantine and re-enqueues it with a fresh budget.
// Returns ErrKindNotRegistered or ErrNotFound if the key isn't quarantined.
func (m *Manager) Requeue(key WorkItemKey) error {
	ks, ok := m.kinds[key.Kind]
	if !ok {
		return types.ErrKindNotRegistered
	}
	_, exists := ks.quarantine.Get(key)
	if !exists {
		return types.ErrNotFound
	}
	ks.quarantine.Delete(key)
	return ks.q.Enqueue(key)
}

// KindStats returns a per-kind operational snapshot and updates Prometheus gauges.
func (m *Manager) KindStats() map[string]health.KindStat {
	out := make(map[string]health.KindStat, len(m.kinds))
	for kind, ks := range m.kinds {
		active := ks.pool.RunningWorkers()
		depth := ks.q.Len()
		poison := ks.quarantine.Len()

		health.ActiveWorkers.WithLabelValues(kind).Set(float64(active))
		health.QueueDepth.WithLabelValues(kind).Set(float64(depth))
		health.PoisonItemsTotal.WithLabelValues(kind).Set(float64(poison))

		ks.mu.Lock()
		lastSuccess := ks.lastSuccess
		ks.mu.Unlock()
		stalled := !lastSuccess.IsZero() && time.Since(lastSuccess) > ks.reg.StallThreshold

		out[kind] = health.KindStat{
			ActiveWorkers: active,
			QueueDepth:    depth,
			PoisonItems:   poison,
			Stalled:       stalled,
		}
	}
	return out
}

// QuarantineStore returns the poison-item store for the given kind.
// Returns nil if the kind is not registered.
func (m *Manager) QuarantineStore(kind string) *retry.QuarantineStore {
	ks, ok := m.kinds[kind]
	if !ok {
		return nil
	}
	return ks.quarantine
}

// AllPoisonItems returns all quarantined items across every registered kind.
func (m *Manager) AllPoisonItems() []*retry.PoisonItem {
	var out []*retry.PoisonItem
	for _, ks := range m.kinds {
		out = append(out, ks.quarantine.List("")...)
	}
	return out
}

// Start begins the dispatch loop for all registered kinds.
// It blocks until ctx is cancelled, then drains all queues and worker pools.
func (m *Manager) Start(ctx context.Context) error {
	var wg sync.WaitGroup
	for _, ks := range m.kinds {
		wg.Add(1)
		go func(ks *kindState) {
			defer wg.Done()
			m.runDispatchLoop(ctx, ks)
		}(ks)
	}
	<-ctx.Done()
	for _, ks := range m.kinds {
		ks.q.ShutDown()
	}
	wg.Wait()
	for _, ks := range m.kinds {
		ks.pool.Stop(ctx)
	}
	return nil
}

// runDispatchLoop dequeues items and submits them to the worker pool.
func (m *Manager) runDispatchLoop(ctx context.Context, ks *kindState) {
	for {
		key, shutdown := ks.q.Dequeue()
		if shutdown {
			return
		}
		ks.pool.Submit(func() {
			m.dispatch(ctx, ks, key)
		})
	}
}

// dispatch invokes the reconciler through the retry engine.
func (m *Manager) dispatch(ctx context.Context, ks *kindState, key WorkItemKey) {
	defer ks.q.Done(key)

	log := m.log.With(
		zap.String("kind", key.Kind),
		zap.String("namespace", key.Namespace),
		zap.String("name", key.Name),
	)
	log.Debug("reconciling")

	retryCfg := retry.Config{
		MaxAttempts:     ks.reg.MaxAttempts,
		InitialInterval: ks.reg.InitialInterval,
		MaxInterval:     ks.reg.MaxInterval,
		Multiplier:      ks.reg.Multiplier,
	}

	res, attempts, lastErr := retry.RunWithRetry(ctx, key, retryCfg, log, func(rctx context.Context) error {
		result, err := ks.reg.Reconciler.Reconcile(rctx, key)
		if err != nil {
			return err
		}
		if result.RequeueAfter > 0 {
			go func() {
				time.Sleep(result.RequeueAfter)
				_ = ks.q.Enqueue(key)
			}()
		} else if result.Requeue {
			_ = ks.q.Enqueue(key)
		}
		return nil
	})

	switch res {
	case retry.ResultOK:
		ks.mu.Lock()
		ks.lastSuccess = time.Now()
		ks.mu.Unlock()
		log.Debug("reconciled successfully", zap.Int("attempts", attempts))
	case retry.ResultQuarantine:
		log.Error("reconciler quarantined after exhausting retries",
			zap.Int("attempts", attempts),
		)
		// Forget the key before quarantine so that the deferred Done does not
		// re-enqueue it if another event arrived during the retry loop.
		ks.q.Forget(key)
		lastErrStr := ""
		if lastErr != nil {
			lastErrStr = lastErr.Error()
		}
		ks.quarantine.Put(&retry.PoisonItem{
			Key:       key,
			Attempts:  attempts,
			LastError: lastErrStr,
		})
	}
}

func applyDefaults(reg *ReconcilerRegistration) {
	if reg.MaxAttempts <= 0 {
		reg.MaxAttempts = defaultMaxAttempts
	}
	if reg.InitialInterval <= 0 {
		reg.InitialInterval = defaultInitialInterval
	}
	if reg.MaxInterval <= 0 {
		reg.MaxInterval = defaultMaxInterval
	}
	if reg.Multiplier <= 0 {
		reg.Multiplier = defaultMultiplier
	}
	if reg.StallThreshold <= 0 {
		reg.StallThreshold = defaultStallThreshold
	}
	if reg.WorkerCount <= 0 {
		reg.WorkerCount = defaultWorkerCount
	}
}
