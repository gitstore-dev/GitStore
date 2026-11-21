// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package watchjournal

import (
	"context"
	"testing"
	"time"

	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	"github.com/stretchr/testify/require"
)

type sourceProgressJournal struct {
	Store
	progress map[string]datastore.ResourceCDCProgress
}

func (j sourceProgressJournal) LoadProgress(_ context.Context, key string) (datastore.ResourceCDCProgress, error) {
	if progress, ok := j.progress[key]; ok {
		return progress, nil
	}
	return datastore.ResourceCDCProgress{}, datastore.ErrNotFound
}

func TestSourceReadinessRequiresEveryCurrentEpochFrontier(t *testing.T) {
	now := time.Now().UTC()
	journal := sourceProgressJournal{progress: map[string]datastore.ResourceCDCProgress{}}
	sources := []string{"Namespace", "Repository", "RBAC", "ServiceAccount"}
	bounds := datastore.ResourceWatchBounds{Epoch: "current", ProgressAt: now}
	readiness := NewReadiness(5 * time.Second)
	for _, source := range sources {
		journal.progress[SourceReadFrontierKey(source)] = datastore.ResourceCDCProgress{Position: []byte("current"), UpdatedAt: now}
	}
	refresh := func() error { return RefreshSourceReadiness(t.Context(), journal, bounds, readiness, sources) }
	require.NoError(t, refresh())
	require.NoError(t, readiness.Check(now.Add(4*time.Second)))
	require.ErrorIs(t, readiness.Check(now.Add(6*time.Second)), ErrMaterializerNotReady, "no I/O or new event is needed to expire a quiet failed source")
	journal.progress[SourceReadFrontierKey("RBAC")] = datastore.ResourceCDCProgress{Position: []byte("current"), UpdatedAt: now.Add(-6 * time.Second)}
	require.NoError(t, refresh())
	require.ErrorIs(t, readiness.Check(now), ErrMaterializerNotReady, "unrelated fresh sources cannot hide a stalled RBAC stream")
	journal.progress[SourceReadFrontierKey("RBAC")] = datastore.ResourceCDCProgress{Position: []byte("old"), UpdatedAt: now}
	require.ErrorIs(t, refresh(), ErrMaterializerNotReady)
	require.ErrorIs(t, readiness.Check(now), ErrMaterializerNotReady)
	delete(journal.progress, SourceReadFrontierKey("RBAC"))
	require.ErrorIs(t, refresh(), ErrMaterializerNotReady)
}
