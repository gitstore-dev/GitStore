// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package retry

import (
	"sync"
	"time"

	"github.com/gitstore-dev/gitstore/controller-manager/internal/types"
)

// PoisonItem records a key that has exhausted its retry budget.
type PoisonItem struct {
	Key           types.WorkItemKey
	Attempts      int
	LastError     string
	QuarantinedAt time.Time
}

// QuarantineStore is a thread-safe store for poison items.
type QuarantineStore struct {
	mu    sync.RWMutex
	items map[types.WorkItemKey]*PoisonItem
}

// NewQuarantineStore creates an empty QuarantineStore.
func NewQuarantineStore() *QuarantineStore {
	return &QuarantineStore{items: make(map[types.WorkItemKey]*PoisonItem)}
}

func (s *QuarantineStore) Put(item *PoisonItem) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if item.QuarantinedAt.IsZero() {
		item.QuarantinedAt = time.Now()
	}
	s.items[item.Key] = item
}

func (s *QuarantineStore) Get(key types.WorkItemKey) (*PoisonItem, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	item, ok := s.items[key]
	return item, ok
}

func (s *QuarantineStore) Delete(key types.WorkItemKey) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.items, key)
}

// List returns all poison items. Pass kind="" to list all; otherwise filters by kind.
func (s *QuarantineStore) List(kind string) []*PoisonItem {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []*PoisonItem
	for _, item := range s.items {
		if kind == "" || item.Key.Kind == kind {
			out = append(out, item)
		}
	}
	return out
}

func (s *QuarantineStore) Len() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.items)
}
