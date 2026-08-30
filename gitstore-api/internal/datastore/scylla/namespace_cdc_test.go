// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package scylla

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	"github.com/gitstore-dev/gitstore/api/internal/watchjournal"
	"github.com/gocql/gocql"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type sequencerStore struct {
	mu       sync.Mutex
	names    []string
	sequence uint64
}

func (s *sequencerStore) Append(_ context.Context, _ datastore.NamespaceWatchLease, event datastore.NamespaceWatchEvent, _ time.Duration) (datastore.NamespaceWatchEvent, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sequence++
	event.Sequence = s.sequence
	s.names = append(s.names, event.Name)
	return event, nil
}

func (*sequencerStore) SaveProgress(context.Context, datastore.NamespaceWatchLease, datastore.NamespaceCDCProgress) error {
	return nil
}

func TestNamespaceCDCSequencerOrdersConcurrentStreams(t *testing.T) {
	store := &sequencerStore{}
	materializer := watchjournal.NewMaterializer(store, watchjournal.MaterializerConfig{})
	sequencer := newNamespaceCDCSequencer(materializer, datastore.NamespaceWatchLease{}, 20*time.Millisecond)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- sequencer.Run(ctx) }()
	require.NoError(t, sequencer.Register(ctx, "stream-a"))
	require.NoError(t, sequencer.Register(ctx, "stream-b"))

	base := time.Now().UTC()
	progressed := make(chan string, 2)
	late := sequenceTestRequest("stream-b", "late", gocql.MinTimeUUID(base.Add(time.Millisecond)), progressed)
	early := sequenceTestRequest("stream-a", "early", gocql.MinTimeUUID(base), progressed)
	require.NoError(t, sequencer.Submit(ctx, late))
	time.Sleep(5 * time.Millisecond)
	require.NoError(t, sequencer.Submit(ctx, early))
	require.NoError(t, sequencer.Unregister("stream-a"))
	require.NoError(t, sequencer.Unregister("stream-b"))
	assert.Equal(t, "early", <-progressed)
	assert.Equal(t, "late", <-progressed)

	store.mu.Lock()
	assert.Equal(t, []string{"early", "late"}, store.names)
	store.mu.Unlock()
	cancel()
	assert.ErrorIs(t, <-done, context.Canceled)
}

func TestNamespaceCDCSequencerFailsClosedWhenStreamMovesBackward(t *testing.T) {
	store := &sequencerStore{}
	sequencer := newNamespaceCDCSequencer(watchjournal.NewMaterializer(store, watchjournal.MaterializerConfig{}), datastore.NamespaceWatchLease{}, time.Millisecond)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- sequencer.Run(ctx) }()
	require.NoError(t, sequencer.Register(ctx, "stream-a"))

	base := time.Now().UTC()
	progressed := make(chan string, 1)
	require.NoError(t, sequencer.Submit(ctx, sequenceTestRequest("stream-a", "later", gocql.MinTimeUUID(base.Add(time.Second)), progressed)))
	err := sequencer.Submit(ctx, sequenceTestRequest("stream-a", "older", gocql.MinTimeUUID(base), progressed))
	require.ErrorContains(t, err, "moved backward")
	require.ErrorContains(t, <-done, "moved backward")
}

func TestNamespaceWatchBucketUsesConfiguredSize(t *testing.T) {
	assert.Equal(t, int64(0), namespaceWatchBucket(2, 2))
	assert.Equal(t, int64(1), namespaceWatchBucket(3, 2))
	assert.Equal(t, int64(2), namespaceWatchBucket(6, 2))
}

func sequenceTestRequest(streamID, name string, at gocql.UUID, progressed chan<- string) namespaceCDCSequenceRequest {
	return namespaceCDCSequenceRequest{
		streamID: streamID,
		cdcTime:  at,
		change: watchjournal.Change{
			StreamID: streamID, Position: at.Bytes(), DeduplicationKey: streamID + ":" + at.String(),
			Name: name, After: []byte(`{"kind":"Namespace"}`), At: at.Time().UTC(),
		},
		markProgress: func(context.Context) error {
			progressed <- name
			return nil
		},
	}
}
