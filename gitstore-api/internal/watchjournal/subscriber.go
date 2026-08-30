// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package watchjournal

import (
	"context"
	"errors"
	"time"

	"github.com/gitstore-dev/gitstore/api/internal/datastore"
)

type SubscriberConfig struct {
	ReadBatchSize      int
	MaxReplayEvents    int
	BufferSize         int
	PollMin            time.Duration
	PollMax            time.Duration
	MaxMaterializerLag time.Duration
	Metrics            *Metrics
}

type Stream struct {
	Events <-chan datastore.NamespaceWatchEvent
	Errors <-chan error
}

type Subscriber struct {
	store Store
	cfg   SubscriberConfig
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
	if s.cfg.MaxMaterializerLag > 0 && (bounds.UpdatedAt.IsZero() || time.Since(bounds.UpdatedAt) > s.cfg.MaxMaterializerLag) {
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
	if bootstrap {
		events <- datastore.NamespaceWatchEvent{
			Epoch: bounds.Epoch, Sequence: bounds.HighWater,
			Type: datastore.NamespaceWatchBookmark, At: bounds.UpdatedAt,
		}
	}
	if s.cfg.Metrics != nil {
		s.cfg.Metrics.IncSubscribers(path)
	}
	go s.run(ctx, start, bounds.HighWater, path, events, errorsOut)
	return &Stream{Events: events, Errors: errorsOut}, nil
}

func (s *Subscriber) run(ctx context.Context, cursor datastore.NamespaceWatchCursor, initialHighWater uint64, path string, events chan datastore.NamespaceWatchEvent, errorsOut chan error) {
	defer close(events)
	defer close(errorsOut)
	if s.cfg.Metrics != nil {
		defer s.cfg.Metrics.DecSubscribers(path)
	}
	delay := s.cfg.PollMin
	replayed := 0
	replayStarted := time.Now()
	for {
		batch, err := s.store.ReadAfter(ctx, cursor, s.cfg.ReadBatchSize)
		if err != nil {
			reason := ReasonJournalDiscontinuity
			if errors.Is(err, datastore.ErrWatchCursorEpoch) {
				reason = ReasonEpochMismatch
			}
			s.sendExpired(errorsOut, reason, err)
			return
		}
		if len(batch) == 0 {
			select {
			case <-ctx.Done():
				return
			case <-time.After(delay):
			}
			delay *= 2
			if delay > s.cfg.PollMax {
				delay = s.cfg.PollMax
			}
			continue
		}
		delay = s.cfg.PollMin
		for _, event := range batch {
			if event.Sequence != cursor.Sequence+1 {
				s.sendExpired(errorsOut, ReasonJournalDiscontinuity, errors.New("missing journal sequence"))
				return
			}
			if event.Sequence <= initialHighWater {
				replayed++
			}
			if replayed > s.cfg.MaxReplayEvents {
				s.sendExpired(errorsOut, ReasonReplayLimit, errors.New("replay limit exceeded"))
				return
			}
			select {
			case events <- event:
				cursor.Sequence = event.Sequence
				if s.cfg.Metrics != nil {
					s.cfg.Metrics.ObserveDelivery(event.At, time.Now())
				}
			default:
				if s.cfg.Metrics != nil {
					s.cfg.Metrics.IncOverflow()
				}
				s.sendExpired(errorsOut, ReasonSubscriberOverflow, errors.New("subscriber buffer full"))
				return
			}
		}
		if replayed > 0 && cursor.Sequence >= initialHighWater && s.cfg.Metrics != nil {
			s.cfg.Metrics.ObserveReplay(replayed, time.Since(replayStarted))
			replayed = 0
		}
	}
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
