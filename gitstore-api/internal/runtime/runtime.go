// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package runtime

import (
	"sync"
	"time"

	"github.com/google/uuid"
)

// Clock supplies wall-clock time to business code.
type Clock interface {
	Now() time.Time
}

// SystemClock reads the process wall clock.
type SystemClock struct{}

// Now returns the current local time.
func (SystemClock) Now() time.Time {
	return time.Now()
}

// FixedClock returns a fixed instant. It is intended for deterministic tests.
type FixedClock struct {
	now time.Time
}

// NewFixedClock creates a clock fixed at now.
func NewFixedClock(now time.Time) FixedClock {
	return FixedClock{now: now}
}

// Now returns the fixed instant.
func (c FixedClock) Now() time.Time {
	return c.now
}

// IDGenerator supplies identifiers to business code.
type IDGenerator interface {
	NewID() string
	NewV7ID() (string, error)
}

// UUIDGenerator produces random UUID identifiers.
type UUIDGenerator struct{}

// NewID returns a random UUID string.
func (UUIDGenerator) NewID() string {
	return uuid.New().String()
}

// NewV7ID returns a UUIDv7 string.
func (UUIDGenerator) NewV7ID() (string, error) {
	id, err := uuid.NewV7()
	if err != nil {
		return "", err
	}
	return id.String(), nil
}

// SequenceIDGenerator returns preconfigured IDs in order. When exhausted, it
// falls back to UUIDGenerator so tests can supply only the values they assert.
type SequenceIDGenerator struct {
	mu       sync.Mutex
	ids      []string
	fallback UUIDGenerator
}

// NewSequenceIDGenerator creates an ID generator backed by ids.
func NewSequenceIDGenerator(ids ...string) *SequenceIDGenerator {
	return &SequenceIDGenerator{ids: append([]string(nil), ids...)}
}

// NewID returns the next configured ID or a random UUID string.
func (g *SequenceIDGenerator) NewID() string {
	if id, ok := g.pop(); ok {
		return id
	}
	return g.fallback.NewID()
}

// NewV7ID returns the next configured ID or a UUIDv7 string.
func (g *SequenceIDGenerator) NewV7ID() (string, error) {
	if id, ok := g.pop(); ok {
		return id, nil
	}
	return g.fallback.NewV7ID()
}

func (g *SequenceIDGenerator) pop() (string, bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if len(g.ids) == 0 {
		return "", false
	}
	id := g.ids[0]
	g.ids = g.ids[1:]
	return id, true
}
