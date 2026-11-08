// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package kratos

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gitstore-dev/gitstore/api/internal/auth"
	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	"github.com/gitstore-dev/gitstore/api/internal/datastore/memdb"
	"github.com/gitstore-dev/gitstore/observability/observabilitytest"
	"github.com/google/uuid"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"
)

func TestServiceAccountConsumerFiniteRetryThenContinues(t *testing.T) {
	store, err := memdb.New()
	require.NoError(t, err)
	journal := store.(datastore.ResourceWatchCapable).ResourceWatchJournal()
	p, err := New(directoryConfig("http://127.0.0.1:1"))
	require.NoError(t, err)
	metrics := prometheus.NewRegistry()
	p.metrics = newMetrics(observabilitytest.Meter(t, metrics, "gitstore/userdir-delivery"))
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	var failed atomic.Int32
	applied := make(chan string, 8)
	done := make(chan struct{})
	go func() {
		defer close(done)
		p.runServiceAccountConsumer(ctx, journal, datastore.ResourceWatchCursor{}, func(_ context.Context, event datastore.ResourceWatchEvent) error {
			if event.Name == "failing" {
				failed.Add(1)
				return auth.ErrDirectoryUnavailable
			}
			applied <- event.Name
			return nil
		})
	}()
	require.NoError(t, store.CreateServiceAccount(t.Context(), &datastore.ServiceAccount{UID: uuid.NewString(), Name: "failing"}))
	require.NoError(t, store.CreateServiceAccount(t.Context(), &datastore.ServiceAccount{UID: uuid.NewString(), Name: "later"}))
	select {
	case name := <-applied:
		require.Equal(t, "later", name)
	case <-time.After(5 * time.Second):
		t.Fatal("retry exhaustion blocked later live delivery")
	}
	require.EqualValues(t, 3, failed.Load())
	require.Equal(t, float64(1), observabilitytest.Value(t, metrics,
		"gitstore_userdir_serviceaccount_delivery_total", map[string]string{"outcome": "exhausted"}))
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("consumer did not stop")
	}
}

func TestServiceAccountConsumerOverflowDropsAndContinues(t *testing.T) {
	store, err := memdb.New()
	require.NoError(t, err)
	journal := store.(datastore.ResourceWatchCapable).ResourceWatchJournal()
	p, err := New(directoryConfig("http://127.0.0.1:1"))
	require.NoError(t, err)
	p.config.Timeout = 2 * time.Second
	metrics := prometheus.NewRegistry()
	p.metrics = newMetrics(observabilitytest.Meter(t, metrics, "gitstore/userdir-overflow"))
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	entered, release := make(chan struct{}), make(chan struct{})
	applied := make(chan string, 256)
	done := make(chan struct{})
	go func() {
		defer close(done)
		p.runServiceAccountConsumer(ctx, journal, datastore.ResourceWatchCursor{}, func(ctx context.Context, event datastore.ResourceWatchEvent) error {
			if event.Name == "blocked" {
				close(entered)
				select {
				case <-release:
				case <-ctx.Done():
					return ctx.Err()
				}
			}
			applied <- event.Name
			return nil
		})
	}()
	require.NoError(t, store.CreateServiceAccount(t.Context(), &datastore.ServiceAccount{UID: uuid.NewString(), Name: "blocked"}))
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("first live event was not consumed")
	}
	for index := range 128 {
		name := fmt.Sprintf("queued-%d", index)
		require.NoError(t, store.CreateServiceAccount(t.Context(), &datastore.ServiceAccount{UID: uuid.NewString(), Name: name}))
	}
	require.Eventually(t, func() bool {
		return observabilitytest.Value(t, metrics, "gitstore_userdir_serviceaccount_delivery_total", map[string]string{"outcome": "overflow"}) > 0
	}, time.Second, 10*time.Millisecond)
	close(release)
	require.Eventually(t, func() bool { return len(applied) == 65 }, time.Second, 10*time.Millisecond)
	require.NoError(t, store.CreateServiceAccount(t.Context(), &datastore.ServiceAccount{UID: uuid.NewString(), Name: "after-overflow"}))
	require.Eventually(t, func() bool { return len(applied) == 66 }, time.Second, 10*time.Millisecond)
	cancel()
	<-done
}

func TestServiceAccountConsumerStartsAtCurrentBoundary(t *testing.T) {
	store, err := memdb.New()
	require.NoError(t, err)
	require.NoError(t, store.CreateServiceAccount(t.Context(), &datastore.ServiceAccount{UID: uuid.NewString(), Name: "historical", ResourceVersion: "1"}))
	calls := make(chan string, 16)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			calls <- r.URL.Path
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.WriteHeader(http.StatusCreated)
	}))
	defer server.Close()
	p, err := New(directoryConfig(server.URL))
	require.NoError(t, err)
	require.NoError(t, p.StartServiceAccountConsumer(store.(datastore.ResourceWatchCapable).ResourceWatchJournal(), store))
	defer p.Shutdown()
	liveUID := uuid.NewString()
	require.NoError(t, store.CreateServiceAccount(t.Context(), &datastore.ServiceAccount{UID: liveUID, Name: "live", ResourceVersion: "1"}))
	select {
	case path := <-calls:
		require.Equal(t, "/admin/identities/by/external/gitstore:serviceaccount:"+liveUID, path)
	case <-time.After(time.Second):
		t.Fatal("live event was not consumed")
	}
	p.Shutdown()
}

type initializingDirectoryJournal struct {
	datastore.ResourceWatchJournal
	ready    atomic.Bool
	captured chan struct{}
}

func (j *initializingDirectoryJournal) Bounds(ctx context.Context) (datastore.ResourceWatchBounds, error) {
	if !j.ready.Load() {
		return datastore.ResourceWatchBounds{}, errors.New("materializer has not initialized the journal")
	}
	bounds, err := j.ResourceWatchJournal.Bounds(ctx)
	select {
	case j.captured <- struct{}{}:
	default:
	}
	return bounds, err
}

func TestServiceAccountConsumerWaitsForJournalInitializationWithoutReplay(t *testing.T) {
	store, err := memdb.New()
	require.NoError(t, err)
	journal := &initializingDirectoryJournal{
		ResourceWatchJournal: store.(datastore.ResourceWatchCapable).ResourceWatchJournal(),
		captured:             make(chan struct{}, 1),
	}

	calls := make(chan string, 16)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			calls <- r.URL.Path
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.WriteHeader(http.StatusCreated)
	}))
	defer server.Close()
	provider, err := New(directoryConfig(server.URL))
	require.NoError(t, err)
	require.NoError(t, provider.StartServiceAccountConsumer(journal, store),
		"best-effort delivery must not prevent the server from starting its journal producer")
	defer provider.Shutdown()
	require.NoError(t, store.CreateServiceAccount(t.Context(), &datastore.ServiceAccount{
		UID: uuid.NewString(), Name: "before-initialization", ResourceVersion: "1",
	}))
	journal.ready.Store(true)
	select {
	case <-journal.captured:
	case <-time.After(time.Second):
		t.Fatal("consumer did not capture the initialized journal boundary")
	}
	uid := uuid.NewString()
	require.NoError(t, store.CreateServiceAccount(t.Context(), &datastore.ServiceAccount{
		UID: uid, Name: "after-initialization", ResourceVersion: "1",
	}))
	select {
	case path := <-calls:
		require.Equal(t, "/admin/identities/by/external/gitstore:serviceaccount:"+uid, path)
	case <-time.After(time.Second):
		t.Fatal("later live event was not consumed")
	}

	waiting, err := New(directoryConfig(server.URL))
	require.NoError(t, err)
	journal.ready.Store(false)
	require.NoError(t, waiting.StartServiceAccountConsumer(journal, store))
	stopped := make(chan struct{})
	go func() {
		waiting.Shutdown()
		close(stopped)
	}()
	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Fatal("shutdown did not cancel journal initialization")
	}
}

func TestServiceAccountConsumerUsesInjectedLoggerWithoutIdentityFields(t *testing.T) {
	core, entries := observer.New(zapcore.WarnLevel)
	provider, err := New(directoryConfig("http://127.0.0.1:1"), zap.New(core))
	require.NoError(t, err)
	provider.deliverServiceAccount(t.Context(), datastore.ResourceWatchEvent{Name: "private-workload"},
		func(context.Context, datastore.ResourceWatchEvent) error { return auth.ErrDirectoryUnavailable })
	require.Equal(t, 1, entries.Len())
	require.Equal(t, map[string]any{"provider": "kratos", "outcome": "exhausted"},
		entries.All()[0].ContextMap())
}
