// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package rbac

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gitstore-dev/gitstore/controller-manager/internal/cache"
	"github.com/gitstore-dev/gitstore/controller-manager/internal/checkpoint"
	"github.com/gitstore-dev/gitstore/controller-manager/internal/manager"
	"github.com/gitstore-dev/gitstore/controller-manager/internal/types"
	"github.com/stretchr/testify/require"
)

type cleanupPollFixture struct {
	cursors []string
	err     error
	next    string
}

func (c *cleanupPollFixture) ReconcileNamespaceCleanup(_ context.Context, cursor string) (string, bool, error) {
	c.cursors = append(c.cursors, cursor)
	return c.next, true, c.err
}

func TestCleanupPollerRetainsCursorAcrossFailureAndNeverQuarantinesSweep(t *testing.T) {
	client := &cleanupPollFixture{next: "next-page"}
	poller := NewNamespaceCleanupPoller(client)
	key := types.WorkItemKey{Kind: NamespaceCleanupWorkKind, Name: "pending"}
	require.IsType(t, types.RequeueAfter{}, poller.Reconcile(t.Context(), key))
	client.err = errors.New("API unavailable")
	for range 30 {
		result := poller.Reconcile(t.Context(), key).(types.RequeueAfter)
		require.Positive(t, result.After)
		require.LessOrEqual(t, result.After, 15*time.Second)
		require.Equal(t, "next-page", client.cursors[len(client.cursors)-1])
	}
	client.err, client.next = nil, ""
	require.IsType(t, types.RequeueAfter{}, poller.Reconcile(t.Context(), key))
	require.IsType(t, types.RequeueAfter{}, poller.Reconcile(t.Context(), key))
	require.Empty(t, client.cursors[len(client.cursors)-1], "completed scans wrap to discover new jobs")
}

type cleanupDispatchFixture struct{ calls atomic.Int32 }

func (f *cleanupDispatchFixture) ReconcileNamespaceCleanup(context.Context, string) (string, bool, error) {
	f.calls.Add(1)
	return "", true, nil
}

func TestCleanupPollerDispatchesAndPersistsRetryWithoutResourceSnapshot(t *testing.T) {
	dir := t.TempDir()
	store, err := checkpoint.OpenDiskStore(dir, NamespaceCleanupWorkKind)
	require.NoError(t, err)
	defer store.Close()
	key := types.WorkItemKey{Kind: NamespaceCleanupWorkKind, Name: "pending"}
	require.NoError(t, store.Enqueue(t.Context(), key))
	gate := cache.New[Resource]()
	gate.MarkSynced()
	client := &cleanupDispatchFixture{}
	mgr := manager.New()
	require.NoError(t, mgr.Register(manager.ReconcilerRegistration{
		Kind: NamespaceCleanupWorkKind, Disk: store, Cache: gate, WorkerCount: 1,
		Reconciler: NewNamespaceCleanupPoller(client),
	}))
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- mgr.Start(ctx) }()
	stop := sync.OnceFunc(func() {
		cancel()
		require.NoError(t, <-done)
	})
	defer stop()
	require.Eventually(t, func() bool {
		schedule, err := store.WorkSchedule(t.Context(), key)
		return err == nil && schedule.Due > 0 && client.calls.Load() >= 2
	}, 5*time.Second, 5*time.Millisecond, "idle discovery must requeue through the real Manager")
	stop()
	schedule, err := store.WorkSchedule(t.Context(), key)
	require.NoError(t, err)
	require.True(t, schedule.QuarantinedAt.IsZero())
	require.NoError(t, store.Close())
	reopened, err := checkpoint.OpenDiskStore(dir, NamespaceCleanupWorkKind)
	require.NoError(t, err)
	defer reopened.Close()
	recovered, err := reopened.WorkSchedule(t.Context(), key)
	require.NoError(t, err)
	require.Equal(t, schedule.Due, recovered.Due)
	require.EqualValues(t, 1, reopened.Counts().Pending)
}
