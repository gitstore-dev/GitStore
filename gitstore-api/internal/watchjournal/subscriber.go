// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package watchjournal

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/gitstore-dev/gitstore/api/internal/datastore"
)

type SubscriberConfig struct {
	ReadBatchSize       int
	MaxReplayEvents     int
	BufferSize          int
	PollMin             time.Duration
	PollMax             time.Duration
	MaxMaterializerLag  time.Duration
	BackpressureTimeout time.Duration
	Metrics             *Metrics
}

type Stream struct {
	Events <-chan datastore.NamespaceWatchEvent
	Errors <-chan error
}

type Subscriber struct {
	store Store
	cfg   SubscriberConfig

	mu   sync.Mutex
	live *journalTailer
}

func NewSubscriber(store Store, cfg SubscriberConfig) *Subscriber {
	if cfg.ReadBatchSize <= 0 || cfg.ReadBatchSize > DefaultReadBatchSize {
		cfg.ReadBatchSize = DefaultReadBatchSize
	}
	if cfg.MaxReplayEvents <= 0 {
		cfg.MaxReplayEvents = DefaultMaxReplay
	}
	if cfg.BufferSize <= 0 {
		cfg.BufferSize = DefaultBufferSize
	}
	if cfg.PollMin <= 0 {
		cfg.PollMin = 100 * time.Millisecond
	}
	if cfg.PollMax < cfg.PollMin {
		cfg.PollMax = 2 * time.Second
	}
	if cfg.BackpressureTimeout <= 0 {
		cfg.BackpressureTimeout = 30 * time.Second
	}
	return &Subscriber{store: store, cfg: cfg}
}

// Subscribe registers against one captured high water before returning.
func (s *Subscriber) Subscribe(ctx context.Context, rawCursor string) (*Stream, error) {
	return s.SubscribePath(ctx, rawCursor, "internal")
}

// SubscribePath registers a subscriber and attributes bounded metrics to one
// of the stable GraphQL paths (typed or generic).
func (s *Subscriber) SubscribePath(ctx context.Context, rawCursor, path string) (*Stream, error) {
	if s.store == nil {
		return nil, unavailable(errors.New("journal is not configured"))
	}
	bounds, err := s.store.Bounds(ctx)
	if err != nil {
		return nil, unavailable(err)
	}
	if s.cfg.Metrics != nil {
		s.cfg.Metrics.SetBounds(bounds, time.Now())
	}
	if s.cfg.MaxMaterializerLag > 0 && (bounds.ProgressAt.IsZero() || time.Since(bounds.ProgressAt) > s.cfg.MaxMaterializerLag) {
		return nil, unavailable(errors.New("materializer lag exceeds readiness bound"))
	}
	bootstrap := rawCursor == BootstrapCursor
	start := datastore.NamespaceWatchCursor{Epoch: bounds.Epoch, Sequence: bounds.HighWater}
	if rawCursor != "" && !bootstrap {
		cursor, parseErr := ParseCursor(rawCursor)
		if parseErr != nil {
			if errors.Is(parseErr, ErrIncompatibleCursor) {
				return nil, s.expired(ReasonIncompatibleCursor, "incompatible cursor version")
			}
			return nil, s.expired(ReasonInvalidCursor, "invalid cursor")
		}
		if cursor.Epoch != bounds.Epoch {
			return nil, s.expired(ReasonEpochMismatch, "cursor epoch mismatch")
		}
		if cursor.Sequence > bounds.HighWater {
			return nil, s.expired(ReasonInvalidCursor, "cursor is ahead of journal")
		}
		if bounds.Oldest > 0 && cursor.Sequence+1 < bounds.Oldest {
			return nil, s.expired(ReasonRetentionExpired, "cursor precedes retained history")
		}
		if bounds.HighWater-cursor.Sequence > uint64(s.cfg.MaxReplayEvents) {
			return nil, s.expired(ReasonReplayLimit, "resume exceeds replay limit")
		}
		start = datastore.NamespaceWatchCursor{Epoch: cursor.Epoch, Sequence: cursor.Sequence}
	}

	events := make(chan datastore.NamespaceWatchEvent, s.cfg.BufferSize)
	errorsOut := make(chan error, 1)
	live, replayHighWater, err := s.registerLive(ctx, start, bounds)
	if err != nil {
		return nil, err
	}
	if bootstrap {
		events <- datastore.NamespaceWatchEvent{
			Epoch: bounds.Epoch, Sequence: bounds.HighWater,
			Type: datastore.NamespaceWatchBookmark, At: bounds.UpdatedAt,
		}
	}
	if s.cfg.Metrics != nil {
		s.cfg.Metrics.IncSubscribers(path)
	}
	go s.run(ctx, start, replayHighWater, path, live, events, errorsOut)
	return &Stream{Events: events, Errors: errorsOut}, nil
}

func (s *Subscriber) run(ctx context.Context, cursor datastore.NamespaceWatchCursor, replayHighWater uint64, path string, live *liveSubscriber, events chan datastore.NamespaceWatchEvent, errorsOut chan error) {
	defer close(events)
	defer close(errorsOut)
	defer s.unregisterLive(live)
	if s.cfg.Metrics != nil {
		defer s.cfg.Metrics.DecSubscribers(path)
	}
	replayed := 0
	replayStarted := time.Now()
	if err := s.replayThrough(ctx, &cursor, replayHighWater, events, &replayed); err != nil {
		s.sendTerminal(errorsOut, err)
		return
	}
	if replayed > 0 && s.cfg.Metrics != nil {
		s.cfg.Metrics.ObserveReplay(replayed, time.Since(replayStarted))
	}

	for {
		batch, catchupHighWater, err := s.liveBatch(live, cursor)
		if err != nil {
			s.sendTerminal(errorsOut, err)
			return
		}
		if catchupHighWater > cursor.Sequence {
			catchupStarted := time.Now()
			catchupEvents := 0
			if err := s.replayThrough(ctx, &cursor, catchupHighWater, events, &catchupEvents); err != nil {
				s.sendTerminal(errorsOut, err)
				return
			}
			if catchupEvents > 0 && s.cfg.Metrics != nil {
				s.cfg.Metrics.ObserveReplay(catchupEvents, time.Since(catchupStarted))
			}
			continue
		}
		if len(batch) == 0 {
			select {
			case <-ctx.Done():
				return
			case terminal := <-live.errors:
				if terminal != nil {
					s.sendTerminal(errorsOut, terminal)
				}
				return
			case <-live.ready:
			}
			continue
		}
		for _, event := range batch {
			if event.Sequence != cursor.Sequence+1 {
				s.sendExpired(errorsOut, ReasonJournalDiscontinuity, errors.New("missing journal sequence"))
				return
			}
			if err := s.deliver(ctx, events, event); err != nil {
				s.sendTerminal(errorsOut, err)
				return
			}
			cursor.Sequence = event.Sequence
		}
	}
}

func (s *Subscriber) replayThrough(ctx context.Context, cursor *datastore.NamespaceWatchCursor, highWater uint64, events chan<- datastore.NamespaceWatchEvent, replayed *int) error {
	for cursor.Sequence < highWater {
		batch, err := s.store.ReadAfter(ctx, *cursor, s.cfg.ReadBatchSize)
		if err != nil {
			return subscriberReadError(err)
		}
		if len(batch) == 0 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(s.cfg.PollMin):
			}
			continue
		}
		for _, event := range batch {
			if event.Sequence > highWater {
				break
			}
			if event.Sequence != cursor.Sequence+1 {
				return expired(ReasonJournalDiscontinuity, errors.New("missing journal sequence"))
			}
			(*replayed)++
			if *replayed > s.cfg.MaxReplayEvents {
				return expired(ReasonReplayLimit, errors.New("replay limit exceeded"))
			}
			if err := s.deliver(ctx, events, event); err != nil {
				return err
			}
			cursor.Sequence = event.Sequence
		}
	}
	return nil
}

func (s *Subscriber) deliver(ctx context.Context, events chan<- datastore.NamespaceWatchEvent, event datastore.NamespaceWatchEvent) error {
	timer := time.NewTimer(s.cfg.BackpressureTimeout)
	defer timer.Stop()
	select {
	case events <- event:
		if s.cfg.Metrics != nil {
			s.cfg.Metrics.ObserveDelivery(event, time.Now())
		}
		return nil
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return expired(ReasonSubscriberOverflow, errors.New("subscriber buffer full"))
	}
}

func subscriberReadError(err error) error {
	reason := ReasonJournalDiscontinuity
	if errors.Is(err, datastore.ErrWatchRetentionExpired) {
		reason = ReasonRetentionExpired
	} else if errors.Is(err, datastore.ErrWatchCursorEpoch) {
		reason = ReasonEpochMismatch
	}
	return expired(reason, err)
}

func (s *Subscriber) expired(reason Reason, detail string) error {
	if s.cfg.Metrics != nil {
		s.cfg.Metrics.IncExpiry(reason)
	}
	return cursorError(reason, detail)
}

func (s *Subscriber) sendExpired(out chan<- error, reason Reason, cause error) {
	if s.cfg.Metrics != nil {
		s.cfg.Metrics.IncExpiry(reason)
	}
	out <- expired(reason, cause)
}

func (s *Subscriber) sendTerminal(out chan<- error, err error) {
	if err == nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return
	}
	terminal, ok := AsTerminal(err)
	if !ok {
		err = unavailable(err)
		terminal, _ = AsTerminal(err)
	}
	if s.cfg.Metrics != nil && terminal != nil && terminal.Code == CodeExpired {
		s.cfg.Metrics.IncExpiry(terminal.Reason)
		if terminal.Reason == ReasonSubscriberOverflow {
			s.cfg.Metrics.IncOverflow()
		}
	}
	out <- err
}
