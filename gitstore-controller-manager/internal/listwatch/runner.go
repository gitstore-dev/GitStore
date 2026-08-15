// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package listwatch

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/cenkalti/backoff/v5"
	"github.com/gitstore-dev/gitstore/controller-manager/internal/cache"
	"github.com/gitstore-dev/gitstore/controller-manager/internal/checkpoint"
	"github.com/gitstore-dev/gitstore/controller-manager/internal/health"
	"github.com/gitstore-dev/gitstore/controller-manager/internal/types"
	"go.uber.org/zap"
)

const (
	// defaultListRetryInitialInterval / MaxInterval / Multiplier configure
	// the unbounded exponential backoff used while retrying a failed List
	// call (FR-014). A List failure must never give up.
	defaultListRetryInitialInterval = 100 * time.Millisecond
	defaultListRetryMaxInterval     = 5 * time.Second
	defaultListRetryMultiplier      = 2.0

	// defaultFlushRetryInitialInterval configures the backoff used between
	// failed checkpoint Save attempts (FR-005 backpressure).
	defaultFlushRetryInitialInterval = 100 * time.Millisecond
	defaultFlushRetryMultiplier      = 2.0

	// defaultReconnectInitialInterval configures the backoff used between
	// failed/closed Watch reconnect attempts (FR-011). It escalates across
	// consecutive failures and resets once a watch stream opens and stays
	// open until closed by the caller (ctx cancellation) rather than by
	// error.
	defaultReconnectInitialInterval = 100 * time.Millisecond
	defaultReconnectMultiplier      = 2.0
)

const (
	defaultFlushIntervalEvents = 100
	defaultMaxBackoff          = 30 * time.Second
)

// Runner orchestrates the list-then-watch bootstrap and resume loop for a
// single registered kind. Exactly one Runner[T] instance runs per kind, on a
// single dedicated goroutine — this is what satisfies "at most one active
// list-or-watch loop per kind" without any additional locking.
type Runner[T any] struct {
	Kind        string
	ListWatcher ListWatcher[T]
	Cache       *cache.Cache[T] // mutable — Runner is the sole writer (Set/Delete/MarkSynced)
	Store       checkpoint.Store
	Enqueue     func(types.WorkItemKey) error
	// ReplayEnqueue receives related replay keys restored from the checkpoint.
	// It is used when this runner observes changes whose durable work belongs to
	// another controller kind.
	ReplayEnqueue func(types.WorkItemKey) error
	KeyFunc       func(T) types.WorkItemKey
	RevisionFunc  func(T) string // extracts resourceVersion from an object, for expiry-recovery diffing
	// DisableReplay prevents this runner from tracking its own keys. This is
	// useful for observer-only runners whose durable work is represented by
	// RelatedReplayKeys instead.
	DisableReplay bool

	FlushIntervalEvents int           // events between checkpoint persists; default 100
	MaxBackoff          time.Duration // cap on reconnect backoff; default 30s

	Log *zap.Logger

	// currentRV is the in-memory watch cursor, updated on every event. It is
	// the source of truth for same-process reconnects — never the last
	// value persisted to Store, which may lag by up to
	// FlushIntervalEvents-1 events.
	currentRV string

	eventsSinceFlush  int
	replayMu          sync.Mutex
	checkpointDirty   bool
	checkpointVersion uint64
	replayKeys        map[types.WorkItemKey]struct{}
	relatedReplayKeys map[types.WorkItemKey]struct{}
}

// Run blocks until ctx is cancelled or an unrecoverable error occurs.
// Exactly one Run call MUST be active per Kind at a time (FR-012) — the
// caller is responsible for not starting a second Runner for the same kind.
func (r *Runner[T]) Run(ctx context.Context) error {
	r.applyDefaults()
	defer r.finalFlush(ctx)

	pendingDedup, err := r.bootstrapOrResume(ctx)
	if err != nil {
		return err
	}

	return r.watchLoop(ctx, pendingDedup)
}

func (r *Runner[T]) applyDefaults() {
	if r.FlushIntervalEvents <= 0 {
		r.FlushIntervalEvents = defaultFlushIntervalEvents
	}
	if r.MaxBackoff <= 0 {
		r.MaxBackoff = defaultMaxBackoff
	}
}

// bootstrapOrResume decides between the resume path (valid checkpoint) and
// the bootstrap path (missing/corrupt/unreadable checkpoint — FR-007,
// FR-008), returning the transition-dedup set for keys already known as of
// the bootstrap list (empty on resume, since no list occurs).
func (r *Runner[T]) bootstrapOrResume(ctx context.Context) (map[types.WorkItemKey]string, error) {
	if rec, err := r.Store.Load(ctx, r.Kind); err == nil {
		if err := r.restoreCheckpoint(rec); err == nil {
			return map[types.WorkItemKey]string{}, nil
		}
	}

	listResp, err := r.retryList(ctx)
	if err != nil {
		return nil, err
	}

	pendingDedup := r.applyListSnapshot(listResp)
	if err := r.flushWithBackoff(ctx); err != nil {
		return nil, err
	}
	r.Cache.MarkSynced()
	for key := range pendingDedup {
		r.enqueue(key)
	}
	return pendingDedup, nil
}

func (r *Runner[T]) restoreCheckpoint(rec checkpoint.Record) error {
	var items []T
	if err := json.Unmarshal(rec.Snapshot, &items); err != nil {
		return fmt.Errorf("restore checkpoint snapshot: %w", err)
	}
	for _, key := range rec.ReplayKeys {
		if key.Kind != r.Kind {
			return fmt.Errorf("restore checkpoint: replay key kind %q does not match %q", key.Kind, r.Kind)
		}
	}

	replayKeys := make(map[types.WorkItemKey]struct{}, len(rec.ReplayKeys))
	relatedReplayKeys := make(map[types.WorkItemKey]struct{}, len(rec.RelatedReplayKeys))
	r.replayMu.Lock()
	r.replayKeys = replayKeys
	r.relatedReplayKeys = relatedReplayKeys
	r.replayMu.Unlock()
	for _, item := range items {
		r.Cache.Set(r.KeyFunc(item), item)
	}
	for _, key := range rec.ReplayKeys {
		replayKeys[key] = struct{}{}
	}
	for _, key := range rec.RelatedReplayKeys {
		relatedReplayKeys[key] = struct{}{}
	}
	r.currentRV = rec.ResourceVersion
	r.Cache.MarkSynced()
	for _, key := range rec.ReplayKeys {
		r.enqueue(key)
	}
	for _, key := range rec.RelatedReplayKeys {
		r.enqueueRelated(key)
	}
	return nil
}

// applyListSnapshot populates the cache from a ListResponse and returns the
// transition-dedup set: per key, the object revision observed at list time.
// It is consulted exactly once per key — on the first watch event for that
// key — to suppress a duplicate enqueue when the event describes the same
// state already captured by the list snapshot (SC-008). ResourceVersion is
// opaque and compared only for equality, never ordered (per spec
// Assumptions).
func (r *Runner[T]) applyListSnapshot(listResp ListResponse[T]) map[types.WorkItemKey]string {
	pendingDedup := make(map[types.WorkItemKey]string, len(listResp.Items))
	replayKeys := make(map[types.WorkItemKey]struct{}, len(listResp.Items))
	for _, item := range listResp.Items {
		key := r.KeyFunc(item)
		r.Cache.Set(key, item)
		pendingDedup[key] = r.RevisionFunc(item)
		if !r.DisableReplay {
			replayKeys[key] = struct{}{}
		}
	}
	r.replayMu.Lock()
	r.replayKeys = replayKeys
	r.checkpointDirty = true
	r.checkpointVersion++
	r.replayMu.Unlock()
	r.currentRV = listResp.ResourceVersion
	return pendingDedup
}

// retryList retries ListWatcher.List with unbounded exponential backoff
// until it succeeds or ctx is cancelled (FR-014). The watch stream, cache
// sync, and enqueue MUST NOT start until List succeeds.
func (r *Runner[T]) retryList(ctx context.Context) (ListResponse[T], error) {
	b := backoff.NewExponentialBackOff()
	b.InitialInterval = defaultListRetryInitialInterval
	b.MaxInterval = defaultListRetryMaxInterval
	b.Multiplier = defaultListRetryMultiplier

	return backoff.Retry(ctx, func() (ListResponse[T], error) {
		return r.ListWatcher.List(ctx)
	},
		backoff.WithBackOff(b),
		backoff.WithNotify(func(err error, d time.Duration) {
			r.log().Warn("list failed; retrying",
				zap.String("kind", r.Kind),
				zap.Duration("backoff", d),
				zap.Error(err),
			)
		}),
	)
}

// watchLoop opens a watch stream at r.currentRV and processes events until
// ctx is cancelled. On a watch close, it either reconnects (transient,
// escalating backoff) or re-lists (expired cursor) and continues, per
// FR-009/FR-011. reconnectBackoff persists across the loop's iterations so
// consecutive transient failures escalate; it resets whenever a watch
// stream opens successfully.
func (r *Runner[T]) watchLoop(ctx context.Context, pendingDedup map[types.WorkItemKey]string) error {
	reconnectBackoff := backoff.NewExponentialBackOff()
	reconnectBackoff.InitialInterval = defaultReconnectInitialInterval
	reconnectBackoff.MaxInterval = r.MaxBackoff
	reconnectBackoff.Multiplier = defaultReconnectMultiplier
	reconnectBackoff.Reset()

	for {
		watcher, err := r.ListWatcher.Watch(ctx, r.currentRV)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if errors.Is(err, ErrWatchExpired) {
				newDedup, recoverErr := r.recoverFromExpiry(ctx)
				if recoverErr != nil {
					return recoverErr
				}
				pendingDedup = newDedup
				reconnectBackoff.Reset()
				continue
			}
			if !r.sleepBackoff(ctx, reconnectBackoff, err) {
				return ctx.Err()
			}
			continue
		}
		reconnectBackoff.Reset()

		closeErr, processErr := r.drainWatcher(ctx, watcher, pendingDedup)
		watcher.Stop()
		if processErr != nil {
			return processErr
		}

		if ctx.Err() != nil {
			return ctx.Err()
		}

		if errors.Is(closeErr, ErrWatchExpired) {
			newDedup, err := r.recoverFromExpiry(ctx)
			if err != nil {
				return err
			}
			pendingDedup = newDedup
			reconnectBackoff.Reset()
			continue
		}

		// Transient close (including a nil Err(), e.g. a clean Stop()) —
		// reconnect at the in-memory currentRV, never Store.Load (FR-011).
		if !r.sleepBackoff(ctx, reconnectBackoff, closeErr) {
			return ctx.Err()
		}
	}
}

// sleepBackoff waits for the next backoff interval, logging err. Returns
// false if ctx was cancelled during the wait.
func (r *Runner[T]) sleepBackoff(ctx context.Context, b *backoff.ExponentialBackOff, err error) bool {
	d := b.NextBackOff()
	r.log().Warn("watch closed; reconnecting",
		zap.String("kind", r.Kind),
		zap.Duration("backoff", d),
		zap.Error(err),
	)
	select {
	case <-ctx.Done():
		return false
	case <-time.After(d):
		return true
	}
}

// drainWatcher processes events from watcher until its channel closes or
// ctx is cancelled, returning watcher.Err() in the former case.
func (r *Runner[T]) drainWatcher(ctx context.Context, watcher Watcher[T], pendingDedup map[types.WorkItemKey]string) (error, error) {
	for {
		select {
		case <-ctx.Done():
			return nil, nil
		case ev, ok := <-watcher.Events():
			if !ok {
				return watcher.Err(), nil
			}
			if err := r.handleEvent(ctx, ev, pendingDedup); err != nil {
				return nil, err
			}
		}
	}
}

// recoverFromExpiry discards the stale cursor, re-lists with retry, writes a
// fresh checkpoint, and enqueues only resources whose revision differs from
// what's cached (or which are newly seen) — not every listed resource
// (FR-009, US3 AC2). It returns a dedup map mirroring the bootstrap
// transition dedup, so an immediate duplicate watch event for a
// just-enqueued key is not double-enqueued.
func (r *Runner[T]) recoverFromExpiry(ctx context.Context) (map[types.WorkItemKey]string, error) {
	r.log().Warn("watch cursor expired; re-listing", zap.String("kind", r.Kind))
	r.currentRV = ""
	r.markCheckpointDirty()

	listResp, err := r.retryList(ctx)
	if err != nil {
		return nil, err
	}

	pendingDedup := make(map[types.WorkItemKey]string, len(listResp.Items))
	listedKeys := make(map[types.WorkItemKey]struct{}, len(listResp.Items))
	for _, item := range listResp.Items {
		key := r.KeyFunc(item)
		newRev := r.RevisionFunc(item)
		listedKeys[key] = struct{}{}
		cached, ok := r.Cache.Get(key)
		r.Cache.Set(key, item)
		pendingDedup[key] = newRev
		if !ok || r.RevisionFunc(cached) != newRev {
			r.rememberForReplay(key)
			r.enqueue(key)
		}
	}
	for _, cached := range r.Cache.List() {
		key := r.KeyFunc(cached)
		if _, ok := listedKeys[key]; ok {
			continue
		}
		r.Cache.Delete(key)
		r.rememberForReplay(key)
		r.enqueue(key)
	}
	r.currentRV = listResp.ResourceVersion
	r.markCheckpointDirty()
	if err := r.flushWithBackoff(ctx); err != nil {
		return nil, err
	}

	return pendingDedup, nil
}

// handleEvent applies a single WatchEvent to the cache and, unless
// suppressed by the list-to-watch transition dedup or the event is a
// Bookmark, enqueues a work item. It also advances the flush counter and
// applies backpressure at the configured flush interval (FR-005, SC-004).
func (r *Runner[T]) handleEvent(ctx context.Context, ev WatchEvent[T], pendingDedup map[types.WorkItemKey]string) error {
	r.currentRV = ev.ResourceVersion
	r.eventsSinceFlush++
	r.markCheckpointDirty()

	if ev.Type != Bookmark {
		key := r.KeyFunc(ev.Object)

		suppressed := false
		if rev, ok := pendingDedup[key]; ok {
			delete(pendingDedup, key)
			if ev.Type != Deleted && rev == r.RevisionFunc(ev.Object) {
				// Exact same state already captured by the bootstrap list
				// and enqueued from it — update the cache but do not
				// double-enqueue.
				r.Cache.Set(key, ev.Object)
				suppressed = true
			}
		}

		if !suppressed {
			switch ev.Type {
			case Added, Modified:
				r.Cache.Set(key, ev.Object)
			case Deleted:
				r.Cache.Delete(key)
			}
			r.rememberForReplay(key)
			r.enqueue(key)
		}
	}

	if r.eventsSinceFlush >= r.FlushIntervalEvents {
		return r.flushWithBackoff(ctx)
	}
	return nil
}

// finalFlush attempts one best-effort Save on shutdown — it does not retry
// indefinitely since the process is exiting.
func (r *Runner[T]) finalFlush(ctx context.Context) {
	if !r.isCheckpointDirty() {
		return
	}
	rec, version, err := r.checkpointRecord()
	if err != nil {
		r.log().Warn("final checkpoint snapshot failed", zap.String("kind", r.Kind), zap.Error(err))
		return
	}
	if err := r.Store.Save(context.WithoutCancel(ctx), rec); err != nil {
		health.CheckpointWriteFailuresTotal.WithLabelValues(r.Kind).Inc()
		r.log().Warn("final checkpoint flush on shutdown failed", zap.String("kind", r.Kind), zap.Error(err))
		return
	}
	health.CheckpointLastWriteTimestamp.WithLabelValues(r.Kind).Set(float64(time.Now().Unix()))
	r.eventsSinceFlush = 0
	r.clearCheckpointDirty(version)
}

// flushWithBackoff persists the current checkpoint, retrying with backoff on
// failure. The caller MUST NOT consume further watch events while this is
// in progress — that is what bounds the replay window to FlushIntervalEvents
// even under a sustained write outage (FR-005, SC-004).
func (r *Runner[T]) flushWithBackoff(ctx context.Context) error {
	b := backoff.NewExponentialBackOff()
	b.InitialInterval = defaultFlushRetryInitialInterval
	b.MaxInterval = r.MaxBackoff
	b.Multiplier = defaultFlushRetryMultiplier
	b.Reset()

	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		rec, version, err := r.checkpointRecord()
		if err != nil {
			return err
		}
		err = r.Store.Save(ctx, rec)
		if err == nil {
			health.CheckpointLastWriteTimestamp.WithLabelValues(r.Kind).Set(float64(time.Now().Unix()))
			r.eventsSinceFlush = 0
			r.clearCheckpointDirty(version)
			return nil
		}

		health.CheckpointWriteFailuresTotal.WithLabelValues(r.Kind).Inc()
		d := b.NextBackOff()
		r.log().Warn("checkpoint write failed; pausing watch consumption",
			zap.String("kind", r.Kind),
			zap.Duration("backoff", d),
			zap.Error(err),
		)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(d):
		}
	}
}

func (r *Runner[T]) checkpointRecord() (checkpoint.Record, uint64, error) {
	snapshot, err := json.Marshal(r.Cache.List())
	if err != nil {
		return checkpoint.Record{}, 0, fmt.Errorf("marshal checkpoint snapshot: %w", err)
	}
	r.replayMu.Lock()
	defer r.replayMu.Unlock()
	version := r.checkpointVersion
	replayKeys := make([]types.WorkItemKey, 0, len(r.replayKeys))
	for key := range r.replayKeys {
		replayKeys = append(replayKeys, key)
	}
	return checkpoint.Record{
		Kind:              r.Kind,
		ResourceVersion:   r.currentRV,
		Snapshot:          snapshot,
		ReplayKeys:        replayKeys,
		RelatedReplayKeys: r.relatedReplayKeyListLocked(),
		WrittenAt:         time.Now(),
	}, version, nil
}

func (r *Runner[T]) rememberForReplay(key types.WorkItemKey) {
	if r.DisableReplay {
		return
	}
	r.replayMu.Lock()
	defer r.replayMu.Unlock()
	if r.replayKeys == nil {
		r.replayKeys = make(map[types.WorkItemKey]struct{})
	}
	r.replayKeys[key] = struct{}{}
	r.checkpointDirty = true
	r.checkpointVersion++
}

// RememberRelatedReplay records work owned by another controller kind. The
// caller should invoke this before enqueueing the related key so a checkpoint
// written for the observed event contains the dependent work as well.
func (r *Runner[T]) RememberRelatedReplay(key types.WorkItemKey) {
	r.replayMu.Lock()
	defer r.replayMu.Unlock()
	if r.relatedReplayKeys == nil {
		r.relatedReplayKeys = make(map[types.WorkItemKey]struct{})
	}
	if _, exists := r.relatedReplayKeys[key]; exists {
		return
	}
	r.relatedReplayKeys[key] = struct{}{}
	r.checkpointDirty = true
	r.checkpointVersion++
}

// MarkRelatedCompleted removes related work after its owning controller kind
// has reconciled successfully.
func (r *Runner[T]) MarkRelatedCompleted(key types.WorkItemKey) {
	r.replayMu.Lock()
	defer r.replayMu.Unlock()
	if _, exists := r.relatedReplayKeys[key]; !exists {
		return
	}
	delete(r.relatedReplayKeys, key)
	r.checkpointDirty = true
	r.checkpointVersion++
}

func (r *Runner[T]) relatedReplayKeyListLocked() []types.WorkItemKey {
	keys := make([]types.WorkItemKey, 0, len(r.relatedReplayKeys))
	for key := range r.relatedReplayKeys {
		keys = append(keys, key)
	}
	return keys
}

// MarkCompleted removes a successfully reconciled key from the durable replay
// set. It is safe to use as manager.ReconcilerRegistration.OnSuccess.
func (r *Runner[T]) MarkCompleted(key types.WorkItemKey) {
	r.replayMu.Lock()
	defer r.replayMu.Unlock()
	if _, ok := r.replayKeys[key]; !ok {
		return
	}
	delete(r.replayKeys, key)
	r.checkpointDirty = true
	r.checkpointVersion++
}

func (r *Runner[T]) markCheckpointDirty() {
	r.replayMu.Lock()
	r.checkpointDirty = true
	r.checkpointVersion++
	r.replayMu.Unlock()
}

func (r *Runner[T]) isCheckpointDirty() bool {
	r.replayMu.Lock()
	defer r.replayMu.Unlock()
	return r.checkpointDirty
}

func (r *Runner[T]) clearCheckpointDirty(version uint64) {
	r.replayMu.Lock()
	if r.checkpointVersion == version {
		r.checkpointDirty = false
	}
	r.replayMu.Unlock()
}

// enqueue calls Enqueue, logging (not panicking) on failure.
func (r *Runner[T]) enqueue(key types.WorkItemKey) {
	if err := r.Enqueue(key); err != nil {
		r.log().Warn("enqueue failed",
			zap.String("kind", key.Kind),
			zap.String("namespace", key.Namespace),
			zap.String("name", key.Name),
			zap.Error(err),
		)
	}
}

func (r *Runner[T]) enqueueRelated(key types.WorkItemKey) {
	if r.ReplayEnqueue == nil {
		return
	}
	if err := r.ReplayEnqueue(key); err != nil {
		r.log().Warn("related replay enqueue failed",
			zap.String("kind", key.Kind),
			zap.String("namespace", key.Namespace),
			zap.String("name", key.Name),
			zap.Error(err),
		)
	}
}

func (r *Runner[T]) log() *zap.Logger {
	if r.Log != nil {
		return r.Log
	}
	return zap.NewNop()
}
