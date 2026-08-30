// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package watchjournal

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type orderedMaterializerStore struct {
	mu         sync.Mutex
	calls      []string
	appendErr  error
	progress   datastore.NamespaceCDCProgress
	sequence   uint64
	journalKey string
}

func (s *orderedMaterializerStore) Append(_ context.Context, _ datastore.NamespaceWatchLease, event datastore.NamespaceWatchEvent, _ time.Duration) (datastore.NamespaceWatchEvent, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, "append")
	if s.appendErr != nil {
		return datastore.NamespaceWatchEvent{}, s.appendErr
	}
	s.sequence++
	event.Sequence = s.sequence
	event.Epoch = "018f47d2-cd4b-7a11-9c35-4b4c423d56cb"
	s.journalKey = event.DeduplicationKey
	return event, nil
}

func (s *orderedMaterializerStore) SaveProgress(_ context.Context, _ datastore.NamespaceWatchLease, progress datastore.NamespaceCDCProgress) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, "progress")
	s.progress = progress
	return nil
}

type concurrentMaterializerStore struct {
	active atomic.Int32
	max    atomic.Int32
}

func (s *concurrentMaterializerStore) Append(_ context.Context, _ datastore.NamespaceWatchLease, event datastore.NamespaceWatchEvent, _ time.Duration) (datastore.NamespaceWatchEvent, error) {
	active := s.active.Add(1)
	for {
		current := s.max.Load()
		if active <= current || s.max.CompareAndSwap(current, active) {
			break
		}
	}
	time.Sleep(20 * time.Millisecond)
	s.active.Add(-1)
	return event, nil
}

func (*concurrentMaterializerStore) SaveProgress(context.Context, datastore.NamespaceWatchLease, datastore.NamespaceCDCProgress) error {
	return nil
}

func TestClassifyCommittedNamespaceChanges(t *testing.T) {
	before := json.RawMessage(`{"Name":"shop","Labels":{"team":"catalog"}}`)
	after := json.RawMessage(`{"Name":"shop","Labels":{"team":"storefront"},"Title":"Shop"}`)

	tests := []struct {
		name   string
		change Change
		want   datastore.NamespaceWatchEventType
		ok     bool
	}{
		{name: "add", change: Change{Name: "shop", After: after}, want: datastore.NamespaceWatchAdded, ok: true},
		{name: "modify", change: Change{Name: "shop", Before: before, After: after}, want: datastore.NamespaceWatchModified, ok: true},
		{name: "delete", change: Change{Name: "shop", Before: before}, want: datastore.NamespaceWatchDeleted, ok: true},
		{name: "no effect", change: Change{Name: "shop"}, ok: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			event, ok := Classify(tt.change)
			assert.Equal(t, tt.ok, ok)
			assert.Equal(t, tt.want, event.Type)
			if tt.ok {
				wantLabel := "storefront"
				if tt.want == datastore.NamespaceWatchDeleted {
					wantLabel = "catalog"
				}
				assert.Equal(t, wantLabel, event.SelectorLabels["team"])
				if tt.want == datastore.NamespaceWatchModified {
					assert.Equal(t, "catalog", event.PreviousSelectorLabels["team"])
				}
			}
		})
	}
}

func TestMaterializerAppendsBeforeSavingProgress(t *testing.T) {
	store := &orderedMaterializerStore{}
	m := NewMaterializer(store, MaterializerConfig{EventTTL: 7 * 24 * time.Hour})
	lease := datastore.NamespaceWatchLease{Holder: "replica-a", FencingToken: 7}
	change := Change{StreamID: "stream-1", Position: []byte("position-1"), DeduplicationKey: "change-1", Name: "shop", After: json.RawMessage(`{"kind":"Namespace"}`)}

	_, err := m.Process(context.Background(), lease, change)
	require.NoError(t, err)
	assert.Equal(t, []string{"append", "progress"}, store.calls)
	assert.Equal(t, []byte("position-1"), store.progress.Position)
}

func TestMaterializeLeavesCheckpointToCDCAdapter(t *testing.T) {
	store := &orderedMaterializerStore{}
	m := NewMaterializer(store, MaterializerConfig{EventTTL: 7 * 24 * time.Hour})
	change := Change{StreamID: "stream-1", Position: []byte("position-1"), DeduplicationKey: "change-1", Name: "shop", After: json.RawMessage(`{"kind":"Namespace"}`)}

	_, err := m.Materialize(context.Background(), datastore.NamespaceWatchLease{}, change)
	require.NoError(t, err)
	assert.Equal(t, []string{"append"}, store.calls)
}

func TestMaterializerDoesNotAdvanceProgressWhenAppendFails(t *testing.T) {
	store := &orderedMaterializerStore{appendErr: errors.New("injected append failure")}
	m := NewMaterializer(store, MaterializerConfig{EventTTL: 7 * 24 * time.Hour})
	lease := datastore.NamespaceWatchLease{Holder: "replica-a", FencingToken: 7}
	change := Change{StreamID: "stream-1", Position: []byte("position-1"), DeduplicationKey: "change-1", Name: "shop", After: json.RawMessage(`{"kind":"Namespace"}`)}

	_, err := m.Process(context.Background(), lease, change)
	require.Error(t, err)
	assert.Equal(t, []string{"append"}, store.calls)
}

func TestMaterializerRecoveryPermitsSafeDuplicate(t *testing.T) {
	store := &orderedMaterializerStore{}
	m := NewMaterializer(store, MaterializerConfig{EventTTL: 7 * 24 * time.Hour})
	lease := datastore.NamespaceWatchLease{Holder: "replica-a", FencingToken: 7}
	change := Change{StreamID: "stream-1", Position: []byte("position-1"), DeduplicationKey: "change-1", Name: "shop", After: json.RawMessage(`{"kind":"Namespace"}`)}

	first, err := m.Process(context.Background(), lease, change)
	require.NoError(t, err)
	second, err := m.Process(context.Background(), lease, change)
	require.NoError(t, err)
	assert.Equal(t, first.DeduplicationKey, second.DeduplicationKey)
	assert.Greater(t, second.Sequence, first.Sequence)
}

func TestMaterializerSerializesCDCAndBookmarkAppends(t *testing.T) {
	store := &concurrentMaterializerStore{}
	materializer := NewMaterializer(store, MaterializerConfig{})
	lease := datastore.NamespaceWatchLease{Holder: "replica-a", FencingToken: 7}
	change := Change{StreamID: "stream-1", DeduplicationKey: "change-1", Name: "shop", After: json.RawMessage(`{"kind":"Namespace"}`)}

	start := make(chan struct{})
	var group sync.WaitGroup
	group.Add(2)
	go func() {
		defer group.Done()
		<-start
		_, _ = materializer.Process(context.Background(), lease, change)
	}()
	go func() {
		defer group.Done()
		<-start
		_, _ = materializer.AppendBookmark(context.Background(), lease)
	}()
	close(start)
	group.Wait()

	assert.Equal(t, int32(1), store.max.Load())
}
