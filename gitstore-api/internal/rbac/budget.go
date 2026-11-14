// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package rbac

import (
	"sync"
	"sync/atomic"
)

const (
	TranslatedMemoryLimit = 512 << 20
	RebuildScratchLimit   = 64 << 20
)

type MemoryBudget struct {
	limit int64
	used  atomic.Int64
}

type MemoryReservation struct {
	mu     sync.Mutex
	budget *MemoryBudget
	bytes  int64
	closed bool
}

func NewMemoryBudget(limit int64) *MemoryBudget { return &MemoryBudget{limit: limit} }

func (b *MemoryBudget) Reserve(size int64) (*MemoryReservation, error) {
	reservation := &MemoryReservation{budget: b}
	if err := reservation.Grow(size); err != nil {
		return nil, err
	}
	return reservation, nil
}

func (b *MemoryBudget) Used() int64 { return b.used.Load() }

func (r *MemoryReservation) Grow(size int64) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed || r.budget == nil || size < 0 {
		return ErrUnavailable
	}
	for {
		current := r.budget.used.Load()
		if size > r.budget.limit-current {
			return ErrUnavailable
		}
		if r.budget.used.CompareAndSwap(current, current+size) {
			r.bytes += size
			return nil
		}
	}
}

func (r *MemoryReservation) Close() {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.closed {
		r.closed = true
		if r.budget != nil {
			r.budget.used.Add(-r.bytes)
		}
	}
}
