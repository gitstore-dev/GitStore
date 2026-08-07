// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package checkpoint

import (
	"context"
	"fmt"
	"sync"
)

// MemoryStore is an in-memory Store implementation for tests. It is not
// intended for production use.
type MemoryStore struct {
	mu      sync.RWMutex
	records map[string]Record
}

// NewMemoryStore creates an empty MemoryStore.
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{records: make(map[string]Record)}
}

// Load returns the stored Record for kind, or an error if none exists.
func (s *MemoryStore) Load(_ context.Context, kind string) (Record, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	rec, ok := s.records[kind]
	if !ok {
		return Record{}, fmt.Errorf("checkpoint: no record for kind %q", kind)
	}
	return rec, nil
}

// Save stores rec under rec.Kind, overwriting any previous value.
func (s *MemoryStore) Save(_ context.Context, rec Record) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.records[rec.Kind] = rec
	return nil
}
