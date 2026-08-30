// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package watchjournal

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/gitstore-dev/gitstore/api/internal/datastore"
)

// Change is one logical authoritative Namespace CDC transition.
type Change struct {
	StreamID         string
	Position         []byte
	DeduplicationKey string
	Name             string
	Before           json.RawMessage
	After            json.RawMessage
	At               time.Time
}

type MaterializerConfig struct {
	EventTTL         time.Duration
	BookmarkInterval time.Duration
	Clock            Clock
	Metrics          *Metrics
}

type Materializer struct {
	store MaterializerStore
	cfg   MaterializerConfig
}

func NewMaterializer(store MaterializerStore, cfg MaterializerConfig) *Materializer {
	if cfg.EventTTL <= 0 {
		cfg.EventTTL = 7 * 24 * time.Hour
	}
	if cfg.BookmarkInterval <= 0 {
		cfg.BookmarkInterval = 30 * time.Second
	}
	return &Materializer{store: store, cfg: cfg}
}

// Classify maps committed before/postimages without changing Namespace policy.
func Classify(change Change) (datastore.NamespaceWatchEvent, bool) {
	event := datastore.NamespaceWatchEvent{
		Name:             change.Name,
		DeduplicationKey: change.DeduplicationKey,
		At:               change.At,
	}
	switch {
	case len(change.Before) == 0 && len(change.After) > 0:
		event.Type = datastore.NamespaceWatchAdded
		event.Payload = append([]byte(nil), change.After...)
	case len(change.Before) > 0 && len(change.After) > 0:
		if jsonEqual(change.Before, change.After) {
			return datastore.NamespaceWatchEvent{}, false
		}
		event.Type = datastore.NamespaceWatchModified
		event.Payload = append([]byte(nil), change.After...)
	case len(change.Before) > 0 && len(change.After) == 0:
		event.Type = datastore.NamespaceWatchDeleted
	default:
		return datastore.NamespaceWatchEvent{}, false
	}
	return event, true
}

func jsonEqual(left, right json.RawMessage) bool {
	var l, r any
	if json.Unmarshal(left, &l) != nil || json.Unmarshal(right, &r) != nil {
		return string(left) == string(right)
	}
	lb, _ := json.Marshal(l)
	rb, _ := json.Marshal(r)
	return string(lb) == string(rb)
}

// Process appends before saving progress. A crash between those operations may
// duplicate on recovery but cannot skip an acknowledged transition.
func (m *Materializer) Process(ctx context.Context, lease datastore.NamespaceWatchLease, change Change) (datastore.NamespaceWatchEvent, error) {
	event, ok := Classify(change)
	if !ok {
		return datastore.NamespaceWatchEvent{}, nil
	}
	if event.At.IsZero() {
		if m.cfg.Clock != nil {
			event.At = m.cfg.Clock.Now()
		} else {
			event.At = time.Now().UTC()
		}
	}
	appended, err := m.store.Append(ctx, lease, event, m.cfg.EventTTL)
	if err != nil {
		if m.cfg.Metrics != nil {
			m.cfg.Metrics.IncAppendError()
		}
		return datastore.NamespaceWatchEvent{}, fmt.Errorf("append Namespace journal event: %w", err)
	}
	if m.cfg.Metrics != nil {
		m.cfg.Metrics.ObserveMaterialized(appended, time.Now())
	}
	progress := datastore.NamespaceCDCProgress{
		StreamID:  change.StreamID,
		Position:  append([]byte(nil), change.Position...),
		UpdatedAt: event.At,
	}
	if err := m.store.SaveProgress(ctx, lease, progress); err != nil {
		return appended, fmt.Errorf("save Namespace CDC progress: %w", err)
	}
	return appended, nil
}

// AppendBookmark advances the shared cursor while idle.
func (m *Materializer) AppendBookmark(ctx context.Context, lease datastore.NamespaceWatchLease) (datastore.NamespaceWatchEvent, error) {
	now := time.Now().UTC()
	if m.cfg.Clock != nil {
		now = m.cfg.Clock.Now()
	}
	appended, err := m.store.Append(ctx, lease, datastore.NamespaceWatchEvent{
		Type:             datastore.NamespaceWatchBookmark,
		At:               now,
		DeduplicationKey: "bookmark:" + now.UTC().Format(time.RFC3339Nano),
	}, m.cfg.EventTTL)
	if err != nil {
		if m.cfg.Metrics != nil {
			m.cfg.Metrics.IncAppendError()
		}
		return datastore.NamespaceWatchEvent{}, err
	}
	if m.cfg.Metrics != nil {
		m.cfg.Metrics.ObserveMaterialized(appended, time.Now())
	}
	return appended, nil
}
