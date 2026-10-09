// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package watchjournal

import (
	"errors"
	"fmt"
	"sync"
	"time"
)

var ErrMaterializerNotReady = errors.New("namespace watch materializer not ready")

type MaterializerStatus struct {
	LastProgressAt    time.Time
	JournalContinuous bool
}

type Readiness struct {
	mu     sync.RWMutex
	maxLag time.Duration
	status MaterializerStatus
}

func NewReadiness(maxLag time.Duration) *Readiness { return &Readiness{maxLag: maxLag} }

func (r *Readiness) Update(status MaterializerStatus) {
	r.mu.Lock()
	r.status = status
	r.mu.Unlock()
}

func (r *Readiness) Check(now time.Time) error {
	r.mu.RLock()
	status := r.status
	r.mu.RUnlock()
	if !status.JournalContinuous {
		return fmt.Errorf("%w: journal continuity is not proven", ErrMaterializerNotReady)
	}
	if status.LastProgressAt.IsZero() || now.Sub(status.LastProgressAt) > r.maxLag {
		return fmt.Errorf("%w: materializer lag exceeds %s", ErrMaterializerNotReady, r.maxLag)
	}
	return nil
}
