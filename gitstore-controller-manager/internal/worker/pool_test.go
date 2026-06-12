// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package worker_test

import (
	"context"
	"sync/atomic"
	"testing"

	"github.com/gitstore-dev/gitstore/controller-manager/internal/worker"
)

func TestPool_ExecutesTasks(t *testing.T) {
	p := worker.New(2)
	var count atomic.Int64
	for i := 0; i < 5; i++ {
		task := p.Submit(func() { count.Add(1) })
		task.Wait()
	}
	p.Stop(context.Background())
	if count.Load() != 5 {
		t.Errorf("expected 5 executions, got %d", count.Load())
	}
}

func TestPool_Resize(t *testing.T) {
	p := worker.New(1)
	p.Resize(4)
	// After resize the pool accepts tasks without panic.
	task := p.Submit(func() {})
	task.Wait()
	p.Stop(context.Background())
}

func TestPool_RunningWorkers(t *testing.T) {
	p := worker.New(2)
	var _ int64 = p.RunningWorkers() // assert return type is int64
	p.Stop(context.Background())
}
