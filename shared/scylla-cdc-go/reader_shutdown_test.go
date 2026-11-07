// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 GitStore contributors

package scyllacdc

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/gocql/gocql"
)

type shutdownGenerationSource struct {
	times  []time.Time
	queued chan struct{}
}

func (s *shutdownGenerationSource) getGenerationTimes(gocql.Consistency) ([]time.Time, error) {
	return s.times, nil
}

func (s *shutdownGenerationSource) getGeneration(at time.Time, _ gocql.Consistency) ([][]StreamID, error) {
	if at.Equal(s.times[len(s.times)-1]) {
		close(s.queued)
	}
	return [][]StreamID{{StreamID{1}}}, nil
}

func (s *shutdownGenerationSource) maybeUpgrade() (generationSource, error) {
	return s, nil
}

type shutdownProgress struct {
	noProgressManager
	queued  <-chan struct{}
	release <-chan struct{}
	err     error
}

func (p shutdownProgress) StartGeneration(ctx context.Context, _ time.Time) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-p.queued:
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-p.release:
		return p.err
	}
}

func TestReaderShutdownReleasesQueuedGeneration(t *testing.T) {
	for _, mode := range []string{"cancel", "consumer-error", "stop"} {
		t.Run(mode, func(t *testing.T) {
			now := time.Now()
			queued, release := make(chan struct{}), make(chan struct{})
			failure := errors.New("generation consumer failed")
			source := &shutdownGenerationSource{times: []time.Time{now, now.Add(time.Second), now.Add(2 * time.Second)}, queued: queued}
			reader := &Reader{
				config: &ReaderConfig{
					Logger:          noLogger{},
					ProgressManager: shutdownProgress{queued: queued, release: release, err: failure},
				},
				genFetcher: &generationFetcher{
					source: source, logger: noLogger{}, pushedFirst: true,
					generationCh: make(chan *generation, 1), stopCh: make(chan struct{}),
					refreshCh: make(chan struct{}, 1), fetchPeriod: time.Hour,
					maxFetchPeriod: time.Hour, clusterSizeFunc: func() (int, error) { return 1, nil },
				},
				stoppedCh: make(chan struct{}),
			}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			done := make(chan error, 1)
			go func() { done <- reader.Run(ctx) }()
			select {
			case <-queued:
			case <-time.After(time.Second):
				t.Fatal("next generation was not queued")
			}
			if mode == "cancel" {
				cancel()
			} else {
				if mode == "stop" {
					reader.Stop()
				}
				close(release)
			}
			select {
			case err := <-done:
				expected := failure
				if mode == "cancel" {
					expected = context.Canceled
				}
				if !errors.Is(err, expected) {
					t.Fatalf("reader returned %v, want %v", err, expected)
				}
			case <-time.After(time.Second):
				t.Fatal("reader left the generation producer blocked")
			}
		})
	}
}
