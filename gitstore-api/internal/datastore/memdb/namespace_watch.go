// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package memdb

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/gitstore-dev/gitstore/api/internal/datastore"
)

func (m *memdbDatastore) NamespaceWatchJournal() datastore.NamespaceWatchJournal { return m }

func (m *memdbDatastore) Bounds(context.Context) (datastore.NamespaceWatchBounds, error) {
	m.namespaceWatchMu.RLock()
	defer m.namespaceWatchMu.RUnlock()
	oldest := uint64(0)
	updatedAt := time.Time{}
	if len(m.namespaceWatchEvents) > 0 {
		oldest = m.namespaceWatchEvents[0].Sequence
		updatedAt = m.namespaceWatchEvents[len(m.namespaceWatchEvents)-1].At
	}
	return datastore.NamespaceWatchBounds{
		Epoch:     m.namespaceWatchEpoch,
		Oldest:    oldest,
		HighWater: m.namespaceWatchSequence,
		UpdatedAt: updatedAt,
	}, nil
}

func (m *memdbDatastore) Append(_ context.Context, lease datastore.NamespaceWatchLease, event datastore.NamespaceWatchEvent, ttl time.Duration) (datastore.NamespaceWatchEvent, error) {
	m.namespaceWatchMu.Lock()
	defer m.namespaceWatchMu.Unlock()
	if !m.validLeaseLocked(lease, time.Now()) {
		return datastore.NamespaceWatchEvent{}, datastore.ErrStaleWatchLease
	}
	if event.At.IsZero() {
		event.At = time.Now().UTC()
	}
	m.pruneLocked(event.At.Add(-ttl))
	m.namespaceWatchSequence++
	event.Epoch = m.namespaceWatchEpoch
	event.Sequence = m.namespaceWatchSequence
	event.FencingToken = lease.FencingToken
	event.Payload = append([]byte(nil), event.Payload...)
	m.namespaceWatchEvents = append(m.namespaceWatchEvents, event)
	return cloneWatchEvent(event), nil
}

func (m *memdbDatastore) ReadAfter(_ context.Context, cursor datastore.NamespaceWatchCursor, limit int) ([]datastore.NamespaceWatchEvent, error) {
	m.namespaceWatchMu.RLock()
	defer m.namespaceWatchMu.RUnlock()
	if cursor.Epoch != "" && cursor.Epoch != m.namespaceWatchEpoch {
		return nil, datastore.ErrWatchCursorEpoch
	}
	if limit <= 0 || limit > 256 {
		limit = 256
	}
	out := make([]datastore.NamespaceWatchEvent, 0, limit)
	for _, event := range m.namespaceWatchEvents {
		if event.Sequence <= cursor.Sequence {
			continue
		}
		out = append(out, cloneWatchEvent(event))
		if len(out) == limit {
			break
		}
	}
	return out, nil
}

func (m *memdbDatastore) AcquireLease(_ context.Context, holder string, now time.Time, ttl time.Duration) (datastore.NamespaceWatchLease, bool, error) {
	m.namespaceWatchMu.Lock()
	defer m.namespaceWatchMu.Unlock()
	if m.namespaceWatchLease.Holder != "" && now.Before(m.namespaceWatchLease.ExpiresAt) {
		return datastore.NamespaceWatchLease{}, false, nil
	}
	m.namespaceWatchLease = datastore.NamespaceWatchLease{
		Holder:       holder,
		FencingToken: m.namespaceWatchLease.FencingToken + 1,
		ExpiresAt:    now.Add(ttl),
	}
	return m.namespaceWatchLease, true, nil
}

func (m *memdbDatastore) RenewLease(_ context.Context, lease datastore.NamespaceWatchLease, now time.Time, ttl time.Duration) (datastore.NamespaceWatchLease, bool, error) {
	m.namespaceWatchMu.Lock()
	defer m.namespaceWatchMu.Unlock()
	if !m.validLeaseLocked(lease, now) {
		return datastore.NamespaceWatchLease{}, false, nil
	}
	m.namespaceWatchLease.ExpiresAt = now.Add(ttl)
	return m.namespaceWatchLease, true, nil
}

func (m *memdbDatastore) ReleaseLease(_ context.Context, lease datastore.NamespaceWatchLease) error {
	m.namespaceWatchMu.Lock()
	defer m.namespaceWatchMu.Unlock()
	if lease.Holder == m.namespaceWatchLease.Holder && lease.FencingToken == m.namespaceWatchLease.FencingToken {
		m.namespaceWatchLease.Holder = ""
		m.namespaceWatchLease.ExpiresAt = time.Time{}
	}
	return nil
}

func (m *memdbDatastore) LoadProgress(_ context.Context, streamID string) (datastore.NamespaceCDCProgress, error) {
	m.namespaceWatchMu.RLock()
	defer m.namespaceWatchMu.RUnlock()
	progress, ok := m.namespaceWatchProgress[streamID]
	if !ok {
		return datastore.NamespaceCDCProgress{}, datastore.ErrNotFound
	}
	progress.Position = append([]byte(nil), progress.Position...)
	return progress, nil
}

func (m *memdbDatastore) SaveProgress(_ context.Context, lease datastore.NamespaceWatchLease, progress datastore.NamespaceCDCProgress) error {
	m.namespaceWatchMu.Lock()
	defer m.namespaceWatchMu.Unlock()
	if !m.validLeaseLocked(lease, time.Now()) {
		return datastore.ErrStaleWatchLease
	}
	if progress.StreamID == "" {
		return fmt.Errorf("%w: CDC stream id is required", datastore.ErrInvalidArgument)
	}
	progress.Position = append([]byte(nil), progress.Position...)
	m.namespaceWatchProgress[progress.StreamID] = progress
	return nil
}

func (m *memdbDatastore) validLeaseLocked(lease datastore.NamespaceWatchLease, now time.Time) bool {
	return lease.Holder != "" &&
		lease.Holder == m.namespaceWatchLease.Holder &&
		lease.FencingToken == m.namespaceWatchLease.FencingToken &&
		now.Before(m.namespaceWatchLease.ExpiresAt)
}

func (m *memdbDatastore) pruneLocked(cutoff time.Time) {
	first := 0
	for first < len(m.namespaceWatchEvents) && m.namespaceWatchEvents[first].At.Before(cutoff) {
		first++
	}
	if first > 0 {
		m.namespaceWatchEvents = append([]datastore.NamespaceWatchEvent(nil), m.namespaceWatchEvents[first:]...)
	}
}

func cloneWatchEvent(event datastore.NamespaceWatchEvent) datastore.NamespaceWatchEvent {
	event.Payload = append([]byte(nil), event.Payload...)
	if event.SelectorLabels != nil {
		labels := make(map[string]string, len(event.SelectorLabels))
		for key, value := range event.SelectorLabels {
			labels[key] = value
		}
		event.SelectorLabels = labels
	}
	return event
}

// recordCommittedNamespace is the development-backend equivalent of Scylla
// CDC: it runs only after the memdb transaction commits and cannot invent an
// event for a rejected/conflicting/no-op operation.
func (m *memdbDatastore) recordCommittedNamespace(eventType datastore.NamespaceWatchEventType, namespace *datastore.Namespace) {
	if namespace == nil {
		return
	}
	now := time.Now().UTC()
	var payload []byte
	if eventType == datastore.NamespaceWatchAdded || eventType == datastore.NamespaceWatchModified {
		payload, _ = json.Marshal(normalizedNamespaceCopy(namespace))
	}
	m.namespaceWatchMu.Lock()
	defer m.namespaceWatchMu.Unlock()
	m.pruneLocked(now.Add(-7 * 24 * time.Hour))
	m.namespaceWatchSequence++
	m.namespaceWatchEvents = append(m.namespaceWatchEvents, datastore.NamespaceWatchEvent{
		Epoch: m.namespaceWatchEpoch, Sequence: m.namespaceWatchSequence,
		Type: eventType, Name: namespace.Name, Payload: payload,
		SelectorLabels:   cloneStringMap(namespace.Labels),
		DeduplicationKey: fmt.Sprintf("memdb:%s:%s:%d", eventType, namespace.UID, m.namespaceWatchSequence),
		At:               now,
	})
}
