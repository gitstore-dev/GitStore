// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

// Package manager wires a work queue and worker pool to a Reconciler for each
// registered resource kind.
package manager

import (
	"context"
	"errors"
	"fmt"
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
	cache      syncChecker

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
// Returns an error if Kind is empty, Reconciler or Cache is nil, or the kind
// has already been registered. Must be called before Start.
func (m *Manager) Register(reg ReconcilerRegistration) error {
	if reg.Kind == "" {
		return fmt.Errorf("reconciler registration: Kind must not be empty")
	}
	if reg.Reconciler == nil {
		return fmt.Errorf("reconciler registration: Reconciler must not be nil for kind %q", reg.Kind)
	}
	if reg.Cache == nil {
		return fmt.Errorf("reconciler registration: Cache must not be nil for kind %q", reg.Kind)
	}
	if _, exists := m.kinds[reg.Kind]; exists {
		return fmt.Errorf("reconciler registration: kind %q already registered", reg.Kind)
	}
	applyDefaults(&reg)
	m.kinds[reg.Kind] = &kindState{
		reg:        reg,
		q:          queue.New(1000, 0),
		pool:       worker.New(reg.WorkerCount),
		quarantine: retry.NewQuarantineStore(),
		cache:      reg.Cache,
	}
	// Pre-initialise gauges so they appear in /metrics before the first poll.
	health.ActiveWorkers.WithLabelValues(reg.Kind).Set(0)
	health.QueueDepth.WithLabelValues(reg.Kind).Set(0)
	health.PoisonItemsTotal.WithLabelValues(reg.Kind).Set(0)
	return nil
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
			Registered:    true,
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
// Dispatch is held for each item until the cache reports HasSynced (T018).
func (m *Manager) runDispatchLoop(ctx context.Context, ks *kindState) {
	for {
		key, shutdown := ks.q.Dequeue()
		if shutdown {
			return
		}
		// Gate: spin until cache is warm or context is cancelled.
		for !ks.cache.HasSynced() {
			select {
			case <-ctx.Done():
				return
			default:
				time.Sleep(50 * time.Millisecond)
			}
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

	// Call reconciler via safeReconcile so panics are converted to TransientFailure.
	result := safeReconcile(ks.reg.Reconciler, key)(ctx)

	// Check for panic on the first call and emit structured log + counter.
	m.emitPanicLog(log, key, result)

	switch r := result.(type) {
	case types.Success:
		ks.mu.Lock()
		ks.lastSuccess = time.Now()
		ks.mu.Unlock()
		health.ReconcileTotal.WithLabelValues(key.Kind, "success").Inc()
		log.Debug("reconciled successfully")

	case types.TerminalFailure:
		log.Error("terminal reconcile failure — quarantining immediately", zap.Error(r.Err))
		ks.q.Forget(key)
		health.ReconcileTotal.WithLabelValues(key.Kind, "terminal_failure").Inc()
		ks.quarantine.Put(&retry.PoisonItem{
			Key:       key,
			Attempts:  1,
			LastError: r.Err.Error(),
		})

	case types.TransientFailure:
		// Honour BackoffHint: wait before the first retry attempt.
		if r.BackoffHint > 0 {
			select {
			case <-ctx.Done():
				return
			case <-time.After(r.BackoffHint):
			}
		}
		res, attempts, lastErr := retry.RunWithRetry(ctx, key, retryCfg, r.BackoffHint, log, func(rctx context.Context) error {
			inner := safeReconcile(ks.reg.Reconciler, key)(rctx)
			m.emitPanicLog(log, key, inner)
			switch iv := inner.(type) {
			case types.TransientFailure:
				return iv.Err
			case types.TerminalFailure:
				// Treat terminal during retry as permanent — exit loop with error so quarantine fires.
				return iv.Err
			default:
				return nil
			}
		})
		switch res {
		case retry.ResultOK:
			ks.mu.Lock()
			ks.lastSuccess = time.Now()
			ks.mu.Unlock()
			health.ReconcileTotal.WithLabelValues(key.Kind, "success").Inc()
			log.Debug("reconciled successfully after retries", zap.Int("attempts", attempts))
		case retry.ResultQuarantine:
			log.Error("reconciler quarantined after exhausting retries", zap.Int("attempts", attempts))
			ks.q.Forget(key)
			health.ReconcileTotal.WithLabelValues(key.Kind, "transient_failure").Inc()
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

	case types.RequeueAfter:
		health.ReconcileTotal.WithLabelValues(key.Kind, "requeue_after").Inc()
		time.AfterFunc(r.After, func() {
			_ = ks.q.Enqueue(key)
		})
	}
}

// emitPanicLog checks if result is a TransientFailure wrapping a PanicError and
// emits a structured ERROR log with the stack trace.
func (m *Manager) emitPanicLog(log *zap.Logger, key WorkItemKey, result types.ReconcileResult) {
	tf, ok := result.(types.TransientFailure)
	if !ok {
		return
	}
	var pe *PanicError
	if errors.As(tf.Err, &pe) {
		log.Error("reconciler panic recovered",
			zap.String("kind", key.Kind),
			zap.Any("panicValue", pe.Value),
			zap.ByteString("stacktrace", pe.Stack),
		)
		health.ReconcileTotal.WithLabelValues(key.Kind, "transient_failure").Inc()
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
