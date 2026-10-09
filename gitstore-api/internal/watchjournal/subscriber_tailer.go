// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package watchjournal

import (
	"context"
	"errors"
	"time"

	"github.com/gitstore-dev/gitstore/api/internal/datastore"
)

// journalTailer is the one durable-journal poller shared by every Namespace
// watch attached to a Resolver. Scylla remains the source of truth; the ring
// only avoids repeating the same live read for every local WebSocket.
type journalTailer struct {
	ctx         context.Context
	cancel      context.CancelFunc
	cursor      datastore.NamespaceWatchCursor
	ring        []datastore.NamespaceWatchEvent
	subscribers map[*liveSubscriber]struct{}
}

// liveSubscriber receives edge-triggered wakeups. It does not own an event
// queue: a healthy reader drains the shared bounded ring, while a lagging
// reader catches up from the durable journal before rejoining live delivery.
type liveSubscriber struct {
	tailer *journalTailer
	ready  chan struct{}
	errors chan error
}

func (s *Subscriber) registerLive(_ context.Context, cursor datastore.NamespaceWatchCursor, bounds datastore.NamespaceWatchBounds) (*liveSubscriber, uint64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	tailer := s.live
	if tailer != nil && tailer.cursor.Epoch != bounds.Epoch {
		return nil, 0, s.expired(ReasonEpochMismatch, "journal epoch changed")
	}
	if tailer == nil {
		tailerCtx, cancel := context.WithCancel(context.Background())
		tailer = &journalTailer{
			ctx:         tailerCtx,
			cancel:      cancel,
			cursor:      datastore.NamespaceWatchCursor{Epoch: bounds.Epoch, Sequence: bounds.HighWater},
			ring:        make([]datastore.NamespaceWatchEvent, 0, s.liveRingSize()),
			subscribers: make(map[*liveSubscriber]struct{}),
		}
		s.live = tailer
		go s.runTailer(tailer)
	}

	replayHighWater := bounds.HighWater
	if tailer.cursor.Sequence > replayHighWater {
		replayHighWater = tailer.cursor.Sequence
	}
	if replayHighWater-cursor.Sequence > uint64(s.cfg.MaxReplayEvents) {
		return nil, 0, s.expired(ReasonReplayLimit, "resume exceeds replay limit")
	}
	live := &liveSubscriber{
		tailer: tailer,
		ready:  make(chan struct{}, 1),
		errors: make(chan error, 1),
	}
	tailer.subscribers[live] = struct{}{}
	return live, replayHighWater, nil
}

func (s *Subscriber) unregisterLive(live *liveSubscriber) {
	if live == nil {
		return
	}
	s.mu.Lock()
	tailer := live.tailer
	delete(tailer.subscribers, live)
	if s.live == tailer && len(tailer.subscribers) == 0 {
		s.live = nil
		tailer.cancel()
	}
	s.mu.Unlock()
}

func (s *Subscriber) liveRingSize() int {
	size := s.cfg.ReadBatchSize * 2
	if size < s.cfg.BufferSize {
		size = s.cfg.BufferSize
	}
	return max(1, size)
}

func (s *Subscriber) runTailer(tailer *journalTailer) {
	delay := s.cfg.PollMin
	for {
		s.mu.Lock()
		if s.live != tailer {
			s.mu.Unlock()
			return
		}
		cursor := tailer.cursor
		s.mu.Unlock()

		batch, err := s.store.ReadAfter(tailer.ctx, cursor, s.cfg.ReadBatchSize)
		if err != nil {
			if tailer.ctx.Err() == nil {
				s.failTailer(tailer, subscriberReadError(err))
			}
			return
		}
		if len(batch) == 0 {
			select {
			case <-tailer.ctx.Done():
				return
			case <-time.After(delay):
			}
			bounds, boundsErr := s.store.Bounds(tailer.ctx)
			if boundsErr != nil {
				if tailer.ctx.Err() == nil {
					s.failTailer(tailer, unavailable(boundsErr))
				}
				return
			}
			if s.cfg.Metrics != nil {
				s.cfg.Metrics.SetBounds(bounds, time.Now())
			}
			if bounds.Epoch != cursor.Epoch {
				s.failTailer(tailer, expired(ReasonEpochMismatch, errors.New("journal epoch changed")))
				return
			}
			if s.cfg.MaxMaterializerLag > 0 && (bounds.ProgressAt.IsZero() || time.Since(bounds.ProgressAt) > s.cfg.MaxMaterializerLag) {
				s.failTailer(tailer, unavailable(errors.New("materializer lag exceeds readiness bound")))
				return
			}
			delay *= 2
			if delay > s.cfg.PollMax {
				delay = s.cfg.PollMax
			}
			continue
		}
		delay = s.cfg.PollMin
		if err := s.publishLiveBatch(tailer, batch); err != nil {
			s.failTailer(tailer, err)
			return
		}
	}
}

func (s *Subscriber) publishLiveBatch(tailer *journalTailer, batch []datastore.NamespaceWatchEvent) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.live != tailer {
		return context.Canceled
	}
	for _, event := range batch {
		if event.Sequence != tailer.cursor.Sequence+1 {
			return expired(ReasonJournalDiscontinuity, errors.New("missing journal sequence"))
		}
		tailer.cursor.Sequence = event.Sequence
		tailer.ring = append(tailer.ring, event)
		if overflow := len(tailer.ring) - s.liveRingSize(); overflow > 0 {
			copy(tailer.ring, tailer.ring[overflow:])
			tailer.ring = tailer.ring[:len(tailer.ring)-overflow]
		}
		for subscriber := range tailer.subscribers {
			select {
			case subscriber.ready <- struct{}{}:
			default:
			}
		}
	}
	return nil
}

func (s *Subscriber) liveBatch(live *liveSubscriber, cursor datastore.NamespaceWatchCursor) ([]datastore.NamespaceWatchEvent, uint64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	tailer := live.tailer
	if tailer.cursor.Epoch != cursor.Epoch {
		return nil, 0, expired(ReasonEpochMismatch, errors.New("journal epoch changed"))
	}
	if cursor.Sequence >= tailer.cursor.Sequence {
		return nil, 0, nil
	}
	if len(tailer.ring) == 0 || cursor.Sequence+1 < tailer.ring[0].Sequence {
		return nil, tailer.cursor.Sequence, nil
	}
	start := 0
	for start < len(tailer.ring) && tailer.ring[start].Sequence <= cursor.Sequence {
		start++
	}
	if start == len(tailer.ring) {
		return nil, tailer.cursor.Sequence, nil
	}
	batch := append([]datastore.NamespaceWatchEvent(nil), tailer.ring[start:]...)
	return batch, 0, nil
}

func (s *Subscriber) failTailer(tailer *journalTailer, err error) {
	s.mu.Lock()
	if s.live != tailer {
		s.mu.Unlock()
		return
	}
	s.live = nil
	tailer.cancel()
	subscribers := make([]*liveSubscriber, 0, len(tailer.subscribers))
	for subscriber := range tailer.subscribers {
		subscribers = append(subscribers, subscriber)
	}
	s.mu.Unlock()
	for _, subscriber := range subscribers {
		select {
		case subscriber.errors <- err:
		default:
		}
	}
}
