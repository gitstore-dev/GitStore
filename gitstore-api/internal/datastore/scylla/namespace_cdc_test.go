// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package scylla

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	"github.com/gitstore-dev/gitstore/api/internal/datastore/memdb"
	"github.com/gitstore-dev/gitstore/api/internal/watchjournal"
	"github.com/gocql/gocql"
	scyllacdc "github.com/scylladb/scylla-cdc-go"
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

func TestNamespaceCDCSequencerRejectsNewStreamBehindPublishedFrontier(t *testing.T) {
	store := &sequencerStore{}
	sequencer := newNamespaceCDCSequencer(watchjournal.NewMaterializer(store, watchjournal.MaterializerConfig{}), datastore.NamespaceWatchLease{}, time.Millisecond)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- sequencer.Run(ctx) }()

	base := time.Now().UTC()
	progressed := make(chan string, 2)
	require.NoError(t, sequencer.Register(ctx, "stream-a"))
	require.NoError(t, sequencer.Submit(ctx, sequenceTestRequest("stream-a", "published", gocql.MinTimeUUID(base.Add(time.Second)), progressed)))
	require.NoError(t, sequencer.Unregister("stream-a"))
	assert.Equal(t, "published", <-progressed)

	require.NoError(t, sequencer.Register(ctx, "stream-b"))
	err := sequencer.Submit(ctx, sequenceTestRequest("stream-b", "older", gocql.MinTimeUUID(base), progressed))
	require.ErrorContains(t, err, "behind published frontier")
	require.ErrorContains(t, <-done, "behind published frontier")
}

func TestNamespaceCDCSequencerRejectsStreamBehindRestoredFrontier(t *testing.T) {
	base := time.Now().UTC()
	store := &sequencerStore{}
	sequencer := newNamespaceCDCSequencer(watchjournal.NewMaterializer(store, watchjournal.MaterializerConfig{}), datastore.NamespaceWatchLease{}, time.Millisecond)
	sequencer.publishedThrough = gocql.MinTimeUUID(base.Add(time.Second))
	sequencer.hasPublished = true
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- sequencer.Run(ctx) }()
	require.NoError(t, sequencer.Register(ctx, "new-stream"))
	err := sequencer.Submit(ctx, sequenceTestRequest("new-stream", "older", gocql.MinTimeUUID(base), make(chan string, 1)))
	require.ErrorContains(t, err, "behind published frontier")
	require.ErrorContains(t, <-done, "behind published frontier")
}

func TestNamespaceCDCSequencerSkipsUncommittedAdditionBeforeProgress(t *testing.T) {
	store := &sequencerStore{}
	sequencer := newNamespaceCDCSequencer(watchjournal.NewMaterializer(store, watchjournal.MaterializerConfig{}), datastore.NamespaceWatchLease{}, time.Millisecond)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- sequencer.Run(ctx) }()
	require.NoError(t, sequencer.Register(ctx, "stream-a"))

	progressed := make(chan string, 1)
	request := sequenceTestRequest("stream-a", "rolled-back", gocql.MinTimeUUID(time.Now().UTC()), progressed)
	request.shouldPublish = func(context.Context) (bool, error) { return false, nil }
	require.NoError(t, sequencer.Submit(ctx, request))
	require.NoError(t, sequencer.Unregister("stream-a"))
	assert.Equal(t, "rolled-back", <-progressed)
	store.mu.Lock()
	assert.Empty(t, store.names)
	store.mu.Unlock()
	cancel()
	assert.ErrorIs(t, <-done, context.Canceled)
}

func TestNamespaceWatchBucketUsesConfiguredSize(t *testing.T) {
	assert.Equal(t, int64(0), namespaceWatchBucket(2, 2))
	assert.Equal(t, int64(1), namespaceWatchBucket(3, 2))
	assert.Equal(t, int64(2), namespaceWatchBucket(6, 2))
}

func TestNamespaceCDCProgressManagerObservesEmptyQueryProgress(t *testing.T) {
	store, err := memdb.New()
	require.NoError(t, err)
	journal := store.(datastore.NamespaceWatchCapable).NamespaceWatchJournal()
	lease, acquired, err := journal.AcquireLease(context.Background(), "replica-a", time.Now().UTC(), time.Minute)
	require.NoError(t, err)
	require.True(t, acquired)
	observed := make(chan time.Time, 1)
	manager := &namespaceCDCProgressManager{
		journal: journal,
		lease:   lease,
		observeProgress: func(at time.Time) {
			observed <- at
		},
	}
	progressTime := gocql.MinTimeUUID(time.Now().UTC())

	err = manager.SaveProgress(context.Background(), time.Now().UTC(), "namespaces_by_uid", scyllacdc.StreamID("stream-a"), scyllacdc.Progress{LastProcessedRecordTime: progressTime})
	require.NoError(t, err)
	select {
	case at := <-observed:
		assert.False(t, at.IsZero())
	case <-time.After(time.Second):
		t.Fatal("CDC progress observation was not reported")
	}
}

func TestNamespaceCDCDeletionSuppressesUncommittedCreateRollback(t *testing.T) {
	ready, err := (*scyllaDatastore)(nil).namespaceCDCDeletionReady(context.Background(), &datastore.Namespace{}, false)
	require.NoError(t, err)
	assert.False(t, ready)
}

func TestNamespaceCDCCommitMarkerTransitions(t *testing.T) {
	namespace := &datastore.Namespace{Name: "catalog"}

	assert.Equal(t, namespaceCDCSuppress, namespaceCDCDispositionFor(nil, false, namespace, false),
		"the staged authoritative insert advances progress without publishing")
	assert.Equal(t, namespaceCDCPromotedAddition, namespaceCDCDispositionFor(namespace, false, namespace, true),
		"the projection commit marker is the public ADDED transition")
	assert.Equal(t, namespaceCDCRegular, namespaceCDCDispositionFor(nil, false, namespace, true),
		"legacy direct additions retain the projection-readiness gate")
	assert.Equal(t, namespaceCDCRegular, namespaceCDCDispositionFor(namespace, true, namespace, true),
		"ordinary committed updates remain MODIFIED transitions")
	assert.Equal(t, namespaceCDCRegular, namespaceCDCDispositionFor(namespace, false, nil, false),
		"rollback deletes remain governed by the deletion readiness gate")
}

func TestNamespaceCreateRollbackDetachesFromCanceledRequest(t *testing.T) {
	requestCtx, cancelRequest := context.WithCancel(context.Background())
	cancelRequest()

	called := false
	err := runNamespaceCreateRollback(requestCtx, context.Canceled, func(rollbackCtx context.Context) error {
		called = true
		require.NoError(t, rollbackCtx.Err())
		return nil
	})

	require.True(t, called)
	require.ErrorIs(t, err, context.Canceled)
}

func TestNamespaceCreateRollbackSurfacesRepairRequired(t *testing.T) {
	err := runNamespaceCreateRollback(context.Background(), context.Canceled, func(context.Context) error {
		return errors.New("cleanup could not be confirmed")
	})

	require.ErrorIs(t, err, datastore.ErrRepairRequired)
	require.ErrorIs(t, err, context.Canceled)
	var repair *datastore.RepairRequiredError
	require.ErrorAs(t, err, &repair)
	assert.Equal(t, "rollback_create", repair.Step.Action)
	assert.ErrorContains(t, repair.Compensation, "cleanup could not be confirmed")
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
