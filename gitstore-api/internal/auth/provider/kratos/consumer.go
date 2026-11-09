// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package kratos

import (
	"context"
	"errors"
	"time"

	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	"go.uber.org/zap"
)

const serviceAccountConsumerBuffer = 64
const serviceAccountConsumerAttempts = 3
const serviceAccountConsumerPoll = 100 * time.Millisecond

func (p *Provider) StartServiceAccountConsumer(journal datastore.ResourceWatchJournal, source ServiceAccountSource) error {
	p.consumerMu.Lock()
	defer p.consumerMu.Unlock()
	if p.consumerClosed || p.consumerDone != nil || journal == nil || source == nil {
		return ErrInvalidInput
	}
	ctx, cancel := context.WithTimeout(context.Background(), p.config.Timeout)
	bounds, err := journal.Bounds(ctx)
	cancel()
	ctx, cancel = context.WithCancel(context.Background())
	p.consumerCancel, p.consumerDone = cancel, make(chan struct{})
	go func() {
		defer close(p.consumerDone)
		// Fresh Scylla initializes its journal only after server construction.
		for err != nil {
			p.observeServiceAccountDelivery(ctx, "source_unavailable", time.Now())
			timer := time.NewTimer(serviceAccountConsumerPoll)
			select {
			case <-ctx.Done():
				timer.Stop()
				return
			case <-timer.C:
			}
			readCtx, stopRead := context.WithTimeout(ctx, p.config.Timeout)
			bounds, err = journal.Bounds(readCtx)
			stopRead()
		}
		p.runServiceAccountConsumer(ctx, journal, datastore.ResourceWatchCursor{Epoch: bounds.Epoch, Sequence: bounds.HighWater},
			func(ctx context.Context, event datastore.ResourceWatchEvent) error {
				return p.ApplyServiceAccountEvent(ctx, source, event)
			})
	}()
	return nil
}

func (p *Provider) Shutdown() {
	p.consumerMu.Lock()
	p.consumerClosed = true
	cancel, done := p.consumerCancel, p.consumerDone
	p.consumerMu.Unlock()
	if cancel != nil {
		cancel()
		<-done
	}
}

func (p *Provider) runServiceAccountConsumer(ctx context.Context, journal datastore.ResourceWatchJournal, cursor datastore.ResourceWatchCursor,
	apply func(context.Context, datastore.ResourceWatchEvent) error) {
	ctx, cancel := context.WithCancel(ctx)
	queue := make(chan datastore.ResourceWatchEvent, serviceAccountConsumerBuffer)
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			if ctx.Err() != nil {
				return
			}
			select {
			case <-ctx.Done():
				return
			case event := <-queue:
				p.metrics.deliveryBuffer.Record(ctx, int64(len(queue)))
				p.deliverServiceAccount(ctx, event, apply)
			}
		}
	}()
	defer func() { cancel(); <-done }()
	ticker := time.NewTicker(serviceAccountConsumerPoll)
	defer ticker.Stop()
	for {
		if ctx.Err() != nil {
			return
		}
		readCtx, stopRead := context.WithTimeout(ctx, p.config.Timeout)
		events, err := journal.ReadAfter(readCtx, cursor, 256)
		stopRead()
		if errors.Is(err, datastore.ErrWatchCursorEpoch) || errors.Is(err, datastore.ErrWatchRetentionExpired) {
			readCtx, stopRead = context.WithTimeout(ctx, p.config.Timeout)
			bounds, boundsErr := journal.Bounds(readCtx)
			stopRead()
			if boundsErr == nil {
				cursor = datastore.ResourceWatchCursor{Epoch: bounds.Epoch, Sequence: bounds.HighWater}
				p.observeServiceAccountDelivery(ctx, "discontinuity", time.Now())
			}
		} else if err == nil {
			for _, event := range events {
				cursor = datastore.ResourceWatchCursor{Epoch: event.Epoch, Sequence: event.Sequence}
				if event.Kind != "ServiceAccount" {
					continue
				}
				select {
				case queue <- event:
					p.metrics.deliveryBuffer.Record(ctx, int64(len(queue)))
				default:
					p.observeServiceAccountDelivery(ctx, "overflow", time.Now())
				}
			}
		} else if ctx.Err() == nil {
			p.observeServiceAccountDelivery(ctx, "source_unavailable", time.Now())
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (p *Provider) deliverServiceAccount(ctx context.Context, event datastore.ResourceWatchEvent, apply func(context.Context, datastore.ResourceWatchEvent) error) {
	started := time.Now()
	for attempt := range serviceAccountConsumerAttempts {
		if ctx.Err() != nil {
			p.observeServiceAccountDelivery(ctx, "cancelled", started)
			return
		}
		p.metrics.deliveryAttempts.Add(ctx, 1)
		attemptCtx, cancel := context.WithTimeout(ctx, p.config.Timeout)
		err := apply(attemptCtx, event)
		cancel()
		if ctx.Err() != nil {
			p.observeServiceAccountDelivery(ctx, "cancelled", started)
			return
		}
		if err == nil {
			p.observeServiceAccountDelivery(ctx, "applied", started)
			return
		}
		if attempt+1 == serviceAccountConsumerAttempts {
			p.observeServiceAccountDelivery(ctx, "exhausted", started)
			return
		}
		timer := time.NewTimer(time.Duration(attempt+1) * serviceAccountConsumerPoll)
		select {
		case <-ctx.Done():
			timer.Stop()
			p.observeServiceAccountDelivery(ctx, "cancelled", started)
			return
		case <-timer.C:
		}
	}
}

func (p *Provider) observeServiceAccountDelivery(ctx context.Context, outcome string, started time.Time) {
	options := metric.WithAttributes(attribute.String("outcome", outcome))
	p.metrics.deliveryOutcomes.Add(ctx, 1, options)
	p.metrics.deliveryDuration.Record(ctx, time.Since(started).Seconds(), options)
	if outcome == "exhausted" || outcome == "discontinuity" {
		p.log.Warn("workload directory live delivery discarded",
			zap.String("provider", "kratos"), zap.String("outcome", outcome))
	}
}
