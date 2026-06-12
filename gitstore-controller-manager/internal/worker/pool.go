// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

// Package worker wraps pond/v2 to provide per-kind bounded worker pools
// with live resize and exported metrics for the health surface.
package worker

import (
	"context"

	"github.com/alitto/pond/v2"
)

// Pool is a bounded worker pool for one resource kind.
type Pool struct {
	pool pond.Pool
}

// New creates a new Pool with the given maximum worker count.
func New(maxWorkers int) *Pool {
	return &Pool{pool: pond.NewPool(maxWorkers)}
}

// Submit enqueues fn for execution in the pool.
// Returns a Task whose Wait method blocks until fn completes.
func (p *Pool) Submit(fn func()) pond.Task {
	return p.pool.Submit(fn)
}

// Resize adjusts the maximum worker count without restarting the pool.
func (p *Pool) Resize(n int) {
	p.pool.Resize(n)
}

// RunningWorkers returns the number of goroutines actively executing tasks.
func (p *Pool) RunningWorkers() int64 {
	return p.pool.RunningWorkers()
}

// WaitingTasks returns the number of tasks waiting for a worker.
func (p *Pool) WaitingTasks() uint64 {
	return p.pool.WaitingTasks()
}

// Stop waits for all in-flight tasks to complete, then shuts down the pool.
func (p *Pool) Stop(ctx context.Context) {
	p.pool.StopAndWait()
}
