// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package app

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

type targetMaintenanceStub struct {
	datastore.TranslationTargetMaintenanceStore
	calls   atomic.Int64
	active  atomic.Bool
	started chan struct{}
}

func (s *targetMaintenanceStub) RecoverTranslationTargetMaintenanceTurn(ctx context.Context) error {
	if s.calls.Add(1) == 1 {
		return datastore.ErrConflict
	}
	s.active.Store(true)
	defer s.active.Store(false)
	close(s.started)
	<-ctx.Done()
	return ctx.Err()
}

type targetMaintenanceDatastore struct {
	datastore.Datastore
	maintenance *targetMaintenanceStub
	closed      atomic.Bool
	unsafeClose atomic.Bool
}

func (s *targetMaintenanceDatastore) TranslationTargetMaintenanceStore() datastore.TranslationTargetMaintenanceStore {
	if s.maintenance == nil {
		return nil
	}
	return s.maintenance
}

func (s *targetMaintenanceDatastore) Close() error {
	s.unsafeClose.Store(s.maintenance != nil && s.maintenance.active.Load())
	s.closed.Store(true)
	return nil
}

func TestTargetMaintenanceStartsRetriesAndStopsBeforeDatastore(t *testing.T) {
	worker := &targetMaintenanceStub{started: make(chan struct{})}
	store := &targetMaintenanceDatastore{maintenance: worker}
	server := &Server{store: store, log: zap.NewNop()}
	server.Start()
	t.Cleanup(server.Close)
	select {
	case <-worker.started:
	case <-time.After(2 * time.Second):
		t.Fatal("API lifecycle did not start and retry target maintenance")
	}
	server.Close()
	require.True(t, store.closed.Load())
	require.False(t, store.unsafeClose.Load(), "the worker must exit before its database is closed")
	require.EqualValues(t, 2, worker.calls.Load())
}

func TestTargetMaintenanceDoesNotInventMissingCapability(t *testing.T) {
	store := &targetMaintenanceDatastore{}
	server := &Server{store: store, log: zap.NewNop()}
	server.Start()
	server.Close()
	require.True(t, store.closed.Load())
}
