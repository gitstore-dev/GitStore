// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package scylla

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type testFailureInjector struct {
	mu       sync.Mutex
	failures map[failureKey]error
}

type failureKey struct {
	step  string
	point failurePoint
}

func newTestFailureInjector() *testFailureInjector {
	return &testFailureInjector{failures: make(map[failureKey]error)}
}

func (i *testFailureInjector) fail(step string, point failurePoint) {
	i.mu.Lock()
	defer i.mu.Unlock()
	i.failures[failureKey{step: step, point: point}] = errors.New("injected failure")
}

func (i *testFailureInjector) Inject(step string, point failurePoint) error {
	i.mu.Lock()
	defer i.mu.Unlock()
	key := failureKey{step: step, point: point}
	err := i.failures[key]
	delete(i.failures, key)
	return err
}

func TestMutationExecutorCompensatesInReverseOrder(t *testing.T) {
	injector := newTestFailureInjector()
	injector.fail("second", failureAfter)
	executor := newMutationExecutor(injector)
	var calls []string

	err := executor.execute(context.Background(),
		mutationAction{
			Step:       datastore.MutationStep{Action: "first"},
			Apply:      func(context.Context) error { calls = append(calls, "apply-first"); return nil },
			Compensate: func(context.Context) error { calls = append(calls, "undo-first"); return nil },
		},
		mutationAction{
			Step:       datastore.MutationStep{Action: "second"},
			Apply:      func(context.Context) error { calls = append(calls, "apply-second"); return nil },
			Compensate: func(context.Context) error { calls = append(calls, "undo-second"); return nil },
		},
	)

	require.Error(t, err)
	assert.Equal(t, []string{"apply-first", "apply-second", "undo-second", "undo-first"}, calls)
}

func TestMutationExecutorReturnsRepairRequiredOnCompensationFailure(t *testing.T) {
	injector := newTestFailureInjector()
	injector.fail("second", failureBefore)
	executor := newMutationExecutor(injector)

	err := executor.execute(context.Background(),
		mutationAction{
			Step:       datastore.MutationStep{Operation: "create", ResourceKind: "Product", Projection: "products_by_name", Action: "first"},
			Apply:      func(context.Context) error { return nil },
			Compensate: func(context.Context) error { return errors.New("undo failed") },
		},
		mutationAction{
			Step:  datastore.MutationStep{Operation: "create", ResourceKind: "Product", Projection: "products_by_uid", Action: "second"},
			Apply: func(context.Context) error { return nil },
		},
	)

	assert.ErrorIs(t, err, datastore.ErrRepairRequired)
	var repair *datastore.RepairRequiredError
	require.ErrorAs(t, err, &repair)
	assert.Equal(t, "first", repair.Step.Action)
}
