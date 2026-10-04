// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

// Package manager wires a work queue and worker pool to a Reconciler for each
// registered resource kind.
package manager

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/cenkalti/backoff/v5"
	"github.com/gitstore-dev/gitstore/controller-manager/internal/cache"
	"github.com/gitstore-dev/gitstore/controller-manager/internal/checkpoint"
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

	mu                    sync.Mutex
	lastSuccess           time.Time
	startedAt             time.Time
	leases                map[WorkItemKey]uint64
	storageFailed         bool
	runnable              bool
	reportedWriteFailures uint64
}

// Manager supervises one controller (queue + pool + reconciler) per registered kind.
type Manager struct {
	mu    sync.RWMutex
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
// has already been registered. Safe to call concurrently.
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
	m.mu.Lock()
	defer m.mu.Unlock()
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
		leases:     make(map[WorkItemKey]uint64),
	}
	// Pre-initialise gauges so they appear in /metrics before the first poll.
	health.ActiveWorkers.WithLabelValues(reg.Kind).Set(0)
	health.QueueDepth.WithLabelValues(reg.Kind).Set(0)
	health.PoisonItemsTotal.WithLabelValues(reg.Kind).Set(0)
	health.StalledWorkers.WithLabelValues(reg.Kind).Set(0)
	health.ConflictRequeues.WithLabelValues(reg.Kind).Add(0)
	return nil
}

// Enqueue adds key to the queue of its kind.
// Returns ErrKindNotRegistered if no reconciler is registered for key.Kind.
func (m *Manager) Enqueue(key WorkItemKey) error {
	m.mu.RLock()
	ks, ok := m.kinds[key.Kind]
	m.mu.RUnlock()
	if !ok {
		return types.ErrKindNotRegistered
	}
	if ks.reg.Disk != nil {
		return ks.reg.Disk.Enqueue(context.Background(), key)
	}
	return ks.q.Enqueue(key)
}

// IsQuarantined reports whether the key is currently in the quarantine store.
func (m *Manager) IsQuarantined(key WorkItemKey) bool {
	m.mu.RLock()
	ks, ok := m.kinds[key.Kind]
	m.mu.RUnlock()
	if !ok {
		return false
	}
	if ks.reg.Disk != nil {
		state, err := ks.reg.Disk.WorkSchedule(context.Background(), key)
		if err != nil {
			m.storageError(ks, err)
			return true
		}
		return !state.QuarantinedAt.IsZero()
	}
	_, exists := ks.quarantine.Get(key)
	return exists
}

// Requeue removes the key from quarantine and re-enqueues it with a fresh budget.
// Returns ErrKindNotRegistered or ErrNotFound if the key isn't quarantined.
func (m *Manager) Requeue(key WorkItemKey) error {
	m.mu.RLock()
	ks, ok := m.kinds[key.Kind]
	m.mu.RUnlock()
	if !ok {
		return types.ErrKindNotRegistered
	}
	if ks.reg.Disk != nil {
		state, err := ks.reg.Disk.WorkSchedule(context.Background(), key)
		if err != nil {
			return err
		}
		if state.QuarantinedAt.IsZero() {
			return types.ErrNotFound
		}
		return ks.reg.Disk.Enqueue(context.Background(), key)
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
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make(map[string]health.KindStat, len(m.kinds))
	for kind, ks := range m.kinds {
		active := ks.pool.RunningWorkers()
		depth := ks.q.Len() + int(ks.pool.WaitingTasks())
		poison := ks.quarantine.Len()
		if ks.reg.Disk != nil {
			counts := ks.reg.Disk.Counts()
			poison = int(counts.Poison)
			depth = max(0, int(counts.Pending)-poison-int(active))
		}

		health.ActiveWorkers.WithLabelValues(kind).Set(float64(active))
		health.QueueDepth.WithLabelValues(kind).Set(float64(depth))
		health.PoisonItemsTotal.WithLabelValues(kind).Set(float64(poison))
		health.CheckpointReplayBacklog.WithLabelValues(kind).Set(float64(depth))

		ks.mu.Lock()
		if ks.reg.Disk != nil {
			lastWrite, failures := ks.reg.Disk.PersistenceStats()
			health.CheckpointLastWriteTimestamp.WithLabelValues(kind).Set(float64(lastWrite))
			health.CheckpointWriteFailuresTotal.WithLabelValues(kind).Add(float64(failures - ks.reportedWriteFailures))
			ks.reportedWriteFailures = failures
		}
		lastSuccess := ks.lastSuccess
		startedAt := ks.startedAt
		storageFailed := ks.storageFailed
		runnable := ks.runnable
		ks.mu.Unlock()
		stallBaseline := lastSuccess
		if stallBaseline.IsZero() {
			stallBaseline = startedAt
		}
		// An idle reconciler is healthy. A stale last-success timestamp only
		// indicates a stall while work is actively running or waiting to run.
		// Without this work-presence guard, quiet resource kinds permanently
		// degrade /health once StallThreshold elapses.
		hasWork := active > 0 || depth > 0
		if ks.reg.Disk != nil {
			hasWork = active > 0 || runnable
		}
		stalled := hasWork &&
			!stallBaseline.IsZero() && time.Since(stallBaseline) > ks.reg.StallThreshold
		var recovery cache.RecoveryState
		if recoveringCache, ok := ks.cache.(recoveryChecker); ok {
			recovery = recoveringCache.RecoveryState()
			if recovery.Recovering {
				stalled = time.Since(recovery.LastProgress) > ks.reg.StallThreshold
			} else if recovery.CompletedAt.After(stallBaseline) {
				stalled = hasWork && time.Since(recovery.CompletedAt) > ks.reg.StallThreshold
			}
		}
		recovering := 0.0
		if recovery.Recovering {
			recovering = 1
		}
		health.RecoveryInProgress.WithLabelValues(kind).Set(recovering)
		health.RecoveryPages.WithLabelValues(kind).Set(float64(recovery.Pages))
		health.RecoveryRows.WithLabelValues(kind).Set(float64(recovery.Rows))
		if !recovery.LastProgress.IsZero() {
			health.RecoveryLastProgress.WithLabelValues(kind).Set(float64(recovery.LastProgress.Unix()))
		}
		if stalled {
			health.StalledWorkers.WithLabelValues(kind).Set(1)
		} else {
			health.StalledWorkers.WithLabelValues(kind).Set(0)
		}
		if storageFailed {
			stalled = true
			health.StalledWorkers.WithLabelValues(kind).Set(1)
		}

		out[kind] = health.KindStat{
			ActiveWorkers: active,
			QueueDepth:    depth,
			PoisonItems:   poison,
			Stalled:       stalled,
			Registered:    true,
			Recovering:    recovery.Recovering || !ks.cache.HasSynced(),
			Recovery:      recovery,
		}
	}
	return out
}

// ListPoisonPage returns a bounded page with a kind/namespace/name cursor.
func (m *Manager) ListPoisonPage(ctx context.Context, kind, after string, limit int) ([]*retry.PoisonItem, string, error) {
	if limit < 1 || limit > checkpoint.DiskPageItems {
		return nil, "", errors.New("invalid poison page size")
	}
	m.mu.RLock()
	kinds := make(map[string]*kindState)
	var names []string
	for name, state := range m.kinds {
		if kind == "_all" || kind == name {
			names = append(names, name)
			kinds[name] = state
		}
	}
	m.mu.RUnlock()
	if len(names) == 0 && kind != "_all" {
		return nil, "", types.ErrKindNotRegistered
	}
	slices.Sort(names)
	afterKind, afterKey, _ := strings.Cut(after, "\x00")
	items := make([]*retry.PoisonItem, 0, limit)
	for _, name := range names {
		if after != "" && name < afterKind {
			continue
		}
		localAfter := ""
		if name == afterKind {
			localAfter = afterKey
		}
		state := kinds[name]
		if state.reg.Disk != nil {
			page, next, err := state.reg.Disk.PoisonPage(ctx, localAfter, limit-len(items))
			if err != nil {
				return nil, "", err
			}
			for _, item := range page {
				items = append(items, &retry.PoisonItem{Key: item.Key, Attempts: item.Attempts,
					LastError: item.LastError, QuarantinedAt: item.QuarantinedAt})
			}
			if len(items) == limit {
				return items, name + "\x00" + next, nil
			}
		} else {
			page := state.quarantine.List(name)
			slices.SortFunc(page, func(a, b *retry.PoisonItem) int {
				return strings.Compare(a.Key.Namespace+"\x00"+a.Key.Name, b.Key.Namespace+"\x00"+b.Key.Name)
			})
			for _, item := range page {
				key := item.Key.Namespace + "\x00" + item.Key.Name
				if key <= localAfter {
					continue
				}
				items = append(items, item)
				if len(items) == limit {
					return items, name + "\x00" + key, nil
				}
			}
		}
	}
	return items, "", ctx.Err()
}

// Start runs registered dispatchers and joins their active work on cancellation.
func (m *Manager) Start(ctx context.Context) error {
	m.mu.RLock()
	kinds := make([]*kindState, 0, len(m.kinds))
	for _, ks := range m.kinds {
		kinds = append(kinds, ks)
	}
	m.mu.RUnlock()

	var wg sync.WaitGroup
	for _, ks := range kinds {
		ks.mu.Lock()
		if ks.startedAt.IsZero() {
			ks.startedAt = time.Now()
		}
		ks.mu.Unlock()
		wg.Add(1)
		go func(ks *kindState) {
			defer wg.Done()
			var maintenance sync.WaitGroup
			if ks.reg.Disk != nil {
				maintenance.Go(func() { m.runDiskMaintenance(ctx, ks) })
			}
			m.runDispatchLoop(ctx, ks)
			maintenance.Wait()
		}(ks)
	}
	<-ctx.Done()
	for _, ks := range kinds {
		ks.q.ShutDown()
	}
	wg.Wait()
	for _, ks := range kinds {
		ks.pool.Stop(ctx)
	}
	return nil
}

// runDispatchLoop dequeues items and submits them to the worker pool.
// Dispatch is held for each item until the cache reports HasSynced (T018).
func (m *Manager) runDispatchLoop(ctx context.Context, ks *kindState) {
	if ks.reg.Disk != nil {
		m.runDiskDispatchLoop(ctx, ks)
		return
	}
	for {
		key, shutdown := ks.q.Dequeue()
		if shutdown {
			return
		}
		// Gate: block until the cache has completed its initial list or the
		// context is cancelled. SyncedCh is closed once and reused for all
		// items — no polling, no goroutine-per-item overhead.
		if !ks.cache.HasSynced() {
			select {
			case <-ctx.Done():
				return
			case <-ks.cache.SyncedCh():
			}
		}
		if recovery, ok := ks.cache.(recoveryChecker); ok {
			if err := recovery.WaitForRecovery(ctx); err != nil {
				ks.q.Done(key)
				return
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
	if recovery, ok := ks.cache.(recoveryChecker); ok {
		release, err := recovery.AcquireDispatch(ctx)
		if err != nil {
			return
		}
		defer release()
	}

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

	// Check for panic on the first call: emit structured log and increment the
	// transient_failure metric immediately (FR-004). The metric is owned here so
	// it is never double-counted regardless of whether the retry loop succeeds.
	if m.logPanic(log, key, result) {
		health.ReconcileTotal.WithLabelValues(key.Kind, "transient_failure").Inc()
	}

	switch r := result.(type) {
	case types.Success:
		ks.mu.Lock()
		ks.lastSuccess = time.Now()
		ks.mu.Unlock()
		if !m.complete(ctx, ks, key) {
			return
		}
		health.ReconcileTotal.WithLabelValues(key.Kind, "success").Inc()
		log.Debug("reconciled successfully")

	case types.TerminalFailure:
		log.Error("terminal reconcile failure — quarantining immediately", zap.Error(r.Err))
		ks.q.Forget(key)
		health.ReconcileTotal.WithLabelValues(key.Kind, "terminal_failure").Inc()
		m.quarantine(ctx, ks, &retry.PoisonItem{
			Key:       key,
			Attempts:  1,
			LastError: r.Err.Error(),
		})

	case types.TransientFailure:
		m.handleTransient(ctx, ks, key, r, retryCfg, log)

	case types.RequeueAfter:
		m.scheduleRequeue(ctx, ks, key, r.After)
	}
}

func (m *Manager) scheduleRequeue(ctx context.Context, ks *kindState, key WorkItemKey, after time.Duration) {
	health.ReconcileTotal.WithLabelValues(key.Kind, "requeue_after").Inc()
	if ks.reg.Disk != nil {
		_, err := ks.reg.Disk.DeferWork(ctx, key, m.lease(ks, key), max(0, after), "", 0)
		m.storageError(ks, err)
		return
	}
	time.AfterFunc(after, func() {
		if err := ks.q.Enqueue(key); err != nil {
			m.log.Warn("RequeueAfter lost — queue shut down before timer fired",
				zap.String("kind", key.Kind), zap.String("namespace", key.Namespace),
				zap.String("name", key.Name), zap.Duration("after", after), zap.Error(err))
		}
	})
}

func (m *Manager) requeueThrottled(ctx context.Context, ks *kindState, key WorkItemKey, delay time.Duration, log *zap.Logger) {
	log.Warn("API throttled reconciliation; deferring without quarantine", zap.Duration("backoff", delay))
	if ks.reg.Disk != nil {
		m.scheduleRequeue(ctx, ks, key, delay)
		return
	}
	health.ReconcileTotal.WithLabelValues(key.Kind, "requeue_after").Inc()
	// Hold only the bounded worker slot, not a timer/goroutine per pending key.
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return
	case <-timer.C:
	}
	if err := ks.q.Enqueue(key); err != nil {
		log.Warn("throttled reconciliation could not be requeued", zap.Error(err))
	}
}

// handleTransient applies the BackoffHint pre-delay, then drives the retry loop
// for a TransientFailure result from the initial reconcile call.
// The initial call is counted as attempt 1; RunWithRetry receives MaxAttempts-1
// so total invocations equal MaxAttempts exactly.
func (m *Manager) handleTransient(
	ctx context.Context,
	ks *kindState,
	key WorkItemKey,
	r types.TransientFailure,
	retryCfg retry.Config,
	log *zap.Logger,
) {
	if ctx.Err() != nil {
		return
	}
	if errors.Is(r.Err, checkpoint.ErrSnapshotInProgress) {
		m.scheduleRequeue(ctx, ks, key, max(time.Second, r.BackoffHint))
		return
	}
	if errors.Is(r.Err, types.ErrRateLimited) {
		m.requeueThrottled(ctx, ks, key, max(time.Second, r.BackoffHint), log)
		return
	}
	if r.BackoffHint > 0 {
		select {
		case <-ctx.Done():
			return
		case <-time.After(r.BackoffHint):
		}
	}

	retryAttempts := retryCfg.MaxAttempts - 1
	if retryAttempts <= 0 {
		log.Error("reconciler quarantined after exhausting retries", zap.Int("attempts", 1))
		ks.q.Forget(key)
		health.ReconcileTotal.WithLabelValues(key.Kind, "transient_failure").Inc()
		lastErrStr := ""
		if r.Err != nil {
			lastErrStr = r.Err.Error()
		}
		m.quarantine(ctx, ks, &retry.PoisonItem{Key: key, Attempts: 1, LastError: lastErrStr})
		return
	}

	retryCfg.MaxAttempts = retryAttempts
	res, attempts, lastErr := retry.RunWithRetry(ctx, key, retryCfg, 0, log, func(rctx context.Context) error {
		inner := safeReconcile(ks.reg.Reconciler, key)(rctx)
		_ = m.logPanic(log, key, inner)
		switch iv := inner.(type) {
		case types.TransientFailure:
			if errors.Is(iv.Err, types.ErrRateLimited) || errors.Is(iv.Err, checkpoint.ErrSnapshotInProgress) {
				return backoff.Permanent(iv.Err)
			}
			return iv.Err
		case types.RequeueAfter:
			return backoff.Permanent(&requeueDuringRetryError{after: iv.After})
		case types.TerminalFailure:
			// backoff.Permanent short-circuits the retry loop immediately so the
			// remaining budget is not consumed. errors.As unwraps through
			// PermanentError.Unwrap to find terminalDuringRetryError.
			return backoff.Permanent(&terminalDuringRetryError{cause: iv.Err})
		default:
			return nil
		}
	})

	if ctx.Err() != nil {
		return
	}
	if errors.Is(lastErr, checkpoint.ErrSnapshotInProgress) {
		m.scheduleRequeue(ctx, ks, key, time.Second)
		return
	}
	if errors.Is(lastErr, types.ErrRateLimited) {
		m.requeueThrottled(ctx, ks, key, time.Second, log)
		return
	}
	var deferred *requeueDuringRetryError
	if errors.As(lastErr, &deferred) {
		m.scheduleRequeue(ctx, ks, key, deferred.after)
		return
	}
	var tdr *terminalDuringRetryError
	if errors.As(lastErr, &tdr) {
		log.Error("terminal failure during retry — quarantining immediately", zap.Error(tdr.cause))
		ks.q.Forget(key)
		health.ReconcileTotal.WithLabelValues(key.Kind, "terminal_failure").Inc()
		m.quarantine(ctx, ks, &retry.PoisonItem{
			Key:       key,
			Attempts:  attempts + 1, // +1 for the initial call before RunWithRetry
			LastError: tdr.cause.Error(),
		})
		return
	}

	switch res {
	case retry.ResultOK:
		ks.mu.Lock()
		ks.lastSuccess = time.Now()
		ks.mu.Unlock()
		if !m.complete(ctx, ks, key) {
			return
		}
		health.ReconcileTotal.WithLabelValues(key.Kind, "success").Inc()
		log.Debug("reconciled successfully after retries", zap.Int("attempts", attempts+1))
	case retry.ResultQuarantine:
		log.Error("reconciler quarantined after exhausting retries", zap.Int("attempts", attempts+1))
		ks.q.Forget(key)
		health.ReconcileTotal.WithLabelValues(key.Kind, "transient_failure").Inc()
		lastErrStr := ""
		if lastErr != nil {
			lastErrStr = lastErr.Error()
		}
		m.quarantine(ctx, ks, &retry.PoisonItem{
			Key:       key,
			Attempts:  attempts + 1, // +1 for the initial call before RunWithRetry
			LastError: lastErrStr,
		})
	}
}

// logPanic checks if result is a TransientFailure wrapping a PanicError and
// emits a structured ERROR log with the stack trace. Returns true if a panic
// was detected so the caller can increment the metric exactly once (FR-004).
func (m *Manager) lease(ks *kindState, key WorkItemKey) uint64 {
	ks.mu.Lock()
	defer ks.mu.Unlock()
	return ks.leases[key]
}

func (m *Manager) storageError(ks *kindState, err error) {
	ks.mu.Lock()
	ks.storageFailed = err != nil
	ks.mu.Unlock()
	if err != nil {
		m.log.Error("durable controller work failed", zap.String("kind", ks.reg.Kind), zap.Error(err))
	}
}

func (m *Manager) complete(ctx context.Context, ks *kindState, key WorkItemKey) bool {
	if ks.reg.Disk != nil {
		_, err := ks.reg.Disk.Acknowledge(ctx, key, m.lease(ks, key))
		m.storageError(ks, err)
		return err == nil
	}
	if ks.reg.OnSuccess != nil {
		ks.reg.OnSuccess(key)
	}
	return true
}

func (m *Manager) quarantine(ctx context.Context, ks *kindState, item *retry.PoisonItem) {
	if ks.reg.Disk == nil {
		ks.quarantine.Put(item)
		return
	}
	failure := item.LastError
	if failure == "" {
		failure = "reconciliation failed without error detail"
	}
	_, err := ks.reg.Disk.DeferWork(ctx, item.Key, m.lease(ks, item.Key), 0, failure, item.Attempts)
	m.storageError(ks, err)
}

func (m *Manager) runDiskDispatchLoop(ctx context.Context, ks *kindState) {
	var workers sync.WaitGroup
	defer workers.Wait()
	completed := make(chan struct{}, 1)
	tick := time.NewTicker(25 * time.Millisecond)
	defer tick.Stop()
	for ctx.Err() == nil {
		if !ks.cache.HasSynced() {
			select {
			case <-ctx.Done():
				return
			case <-ks.cache.SyncedCh():
			}
		}
		if recovery, ok := ks.cache.(recoveryChecker); ok {
			if err := recovery.WaitForRecovery(ctx); err != nil {
				return
			}
		}
		ks.mu.Lock()
		available := ks.reg.WorkerCount - len(ks.leases)
		ks.mu.Unlock()
		if available > 0 {
			page, err := ks.reg.Disk.Ready(ctx, min(checkpoint.DiskPageItems, ks.reg.WorkerCount))
			if err != nil {
				if !errors.Is(err, checkpoint.ErrSnapshotInProgress) {
					m.storageError(ks, err)
				}
			} else {
				ks.mu.Lock()
				ks.runnable = len(page) > 0
				ks.mu.Unlock()
				for _, work := range page {
					ks.mu.Lock()
					_, reserved := ks.leases[work.Key]
					if !reserved && len(ks.leases) < ks.reg.WorkerCount {
						ks.leases[work.Key] = work.Token
					} else {
						reserved = true
					}
					ks.mu.Unlock()
					if reserved {
						continue
					}
					workers.Add(1)
					ks.pool.Submit(func() {
						defer workers.Done()
						defer func() {
							ks.mu.Lock()
							delete(ks.leases, work.Key)
							ks.mu.Unlock()
							select {
							case completed <- struct{}{}:
							default:
							}
						}()
						m.dispatch(ctx, ks, work.Key)
					})
				}
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		case <-completed:
		}
	}
}

func (m *Manager) runDiskMaintenance(ctx context.Context, ks *kindState) {
	var resync <-chan time.Time
	if ks.reg.ResyncInterval > 0 {
		ticker := time.NewTicker(ks.reg.ResyncInterval)
		defer ticker.Stop()
		resync = ticker.C
	}
	idle := time.NewTicker(100 * time.Millisecond)
	defer idle.Stop()
	for ctx.Err() == nil {
		if !ks.cache.HasSynced() {
			select {
			case <-ctx.Done():
				return
			case <-ks.cache.SyncedCh():
			}
		}
		if recovery, ok := ks.cache.(recoveryChecker); ok {
			if err := recovery.WaitForRecovery(ctx); err != nil {
				return
			}
		}
		select {
		case <-resync:
			if err := ks.reg.Disk.RequestFanout(ctx, "*"); err != nil {
				m.storageError(ks, err)
			}
		default:
		}
		progressed, err := m.maintainDiskPage(ctx, ks)
		if err != nil && !errors.Is(err, checkpoint.ErrSnapshotInProgress) {
			m.storageError(ks, err)
		}
		if progressed && err == nil {
			continue
		}
		select {
		case <-ctx.Done():
			return
		case <-idle.C:
		}
	}
}

func (m *Manager) maintainDiskPage(ctx context.Context, ks *kindState) (bool, error) {
	work, err := ks.reg.Disk.RelatedPending(ctx, "", checkpoint.DiskPageItems)
	if err != nil {
		return false, err
	}
	for _, related := range work {
		if ks.reg.RelatedEnqueue == nil {
			return false, fmt.Errorf("no durable related-work handler for %s", ks.reg.Kind)
		}
		if err := ks.reg.RelatedEnqueue(ctx, related.Key); err != nil {
			return false, err
		}
		if _, err := ks.reg.Disk.AcknowledgeRelated(ctx, related.Key, related.Token); err != nil {
			return false, err
		}
	}
	fanout, err := ks.reg.Disk.ProcessFanout(ctx)
	return len(work) > 0 || fanout, err
}

func (m *Manager) logPanic(log *zap.Logger, key WorkItemKey, result types.ReconcileResult) bool {
	tf, ok := result.(types.TransientFailure)
	if !ok {
		return false
	}
	var pe *PanicError
	if errors.As(tf.Err, &pe) {
		log.Error("reconciler panic recovered",
			zap.String("kind", key.Kind),
			zap.Any("panicValue", pe.Value),
			zap.ByteString("stacktrace", pe.Stack),
		)
		return true
	}
	return false
}

// terminalDuringRetryError is a sentinel returned from inside the RunWithRetry
// callback when the reconciler emits TerminalFailure mid-retry. It signals
// dispatch to quarantine immediately rather than waiting for budget exhaustion.
type terminalDuringRetryError struct{ cause error }

func (e *terminalDuringRetryError) Error() string { return e.cause.Error() }
func (e *terminalDuringRetryError) Unwrap() error { return e.cause }

type requeueDuringRetryError struct{ after time.Duration }

func (e *requeueDuringRetryError) Error() string { return "reconciliation deferred during retry" }

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
