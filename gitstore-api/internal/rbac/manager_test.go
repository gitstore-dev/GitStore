// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package rbac_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gitstore-dev/gitstore/api/internal/auth"
	"github.com/gitstore-dev/gitstore/api/internal/auth/provider/opa"
	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	"github.com/gitstore-dev/gitstore/api/internal/rbac"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

type managerJournal struct {
	datastore.ResourceWatchJournal
	mu       sync.Mutex
	epoch    string
	oldest   uint64
	events   []datastore.ResourceWatchEvent
	observed atomic.Uint64
}

func (j *managerJournal) Bounds(context.Context) (datastore.ResourceWatchBounds, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	return datastore.ResourceWatchBounds{Epoch: j.epoch, HighWater: uint64(len(j.events)), Oldest: max(1, j.oldest)}, nil
}

func (j *managerJournal) ReadAfter(_ context.Context, cursor datastore.ResourceWatchCursor, limit int) ([]datastore.ResourceWatchEvent, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if cursor.Epoch != j.epoch {
		return nil, datastore.ErrWatchCursorEpoch
	}
	if cursor.Sequence > uint64(len(j.events)) || cursor.Sequence+1 < j.oldest {
		return nil, datastore.ErrWatchRetentionExpired
	}
	end := min(len(j.events), int(cursor.Sequence)+limit)
	events := append([]datastore.ResourceWatchEvent(nil), j.events[cursor.Sequence:end]...)
	if len(events) > 0 {
		j.observed.Store(events[len(events)-1].Sequence)
	}
	return events, nil
}

func (j *managerJournal) append(kind string, payload []byte, types ...datastore.ResourceWatchEventType) {
	j.mu.Lock()
	defer j.mu.Unlock()
	eventType := datastore.ResourceWatchModified
	if len(types) > 0 {
		eventType = types[0]
	}
	j.events = append(j.events, datastore.ResourceWatchEvent{Epoch: j.epoch, Sequence: uint64(len(j.events) + 1),
		Kind: kind, Payload: payload, At: time.Now().UTC(), Type: eventType})
}

func managerSnapshot(t *testing.T, tier, namespace string, revision uint64) rbac.Snapshot {
	t.Helper()
	builtins, err := rbac.ReleaseBuiltins()
	require.NoError(t, err)
	origin := rbac.SnapshotOrigin{NamespaceUID: tier, Namespace: namespace, NamespaceVersion: "1", Active: true,
		RepositoryID: uuid.NewString(), GitRef: "refs/heads/main", GitRevision: fmt.Sprintf("main@sha1:%040x", revision),
		TreeDigest: fmt.Sprintf("%064x", revision)}
	if tier == rbac.ClusterTierID {
		origin.Root, origin.Namespace, origin.NamespaceUID = true, "gitstore-system", uuid.NewString()
	}
	return rbac.Snapshot{ModelVersion: rbac.ModelVersion, Origin: origin,
		CatalogDigest: auth.AuthorizationCatalogDigest(), BuiltinDigest: builtins.Digest(), ContentDigest: fmt.Sprintf("%064x", revision),
		State: rbac.TierState{TierID: tier, Revision: revision, Projection: rbac.ProjectionIdentity{
			RepositoryID: origin.RepositoryID, GitRef: origin.GitRef, GitRevision: origin.GitRevision, TreeDigest: origin.TreeDigest, Actor: "fixture"}}}
}

func startRBACManager(t *testing.T, journal *managerJournal, provider *opa.Provider, read func(context.Context, string) (rbac.Snapshot, error), healthy *atomic.Bool, resolve ...func(context.Context, string) (string, error)) *rbac.Manager {
	t.Helper()
	deps := rbac.ManagerDeps{Journal: journal, ReadSnapshot: read, Invalidate: provider.InvalidateTier,
		Prepare: func(ctx context.Context, tier auth.AuthorizationTier) (rbac.PreparedPublication, error) {
			return provider.PrepareTier(ctx, tier)
		},
		CheckProgress: func(time.Time) error {
			if healthy.Load() {
				return nil
			}
			return errors.New("source progress unavailable")
		}}
	if len(resolve) > 0 {
		deps.ResolveNamespace = resolve[0]
	}
	return runRBACManager(t, deps)
}

func runRBACManager(t *testing.T, deps rbac.ManagerDeps) *rbac.Manager {
	t.Helper()
	manager, err := rbac.NewManager(deps)
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- manager.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-done:
			require.ErrorIs(t, err, context.Canceled)
		case <-time.After(3 * time.Second):
			t.Error("RBAC workers did not stop after cancellation")
		}
	})
	return manager
}

func TestManagerRetryDoesNotInvalidateItsOwnSlowRebuild(t *testing.T) {
	provider, err := opa.NewProvider(t.Context())
	require.NoError(t, err)
	t.Cleanup(provider.Close)
	snapshot := managerSnapshot(t, rbac.ClusterTierID, "", 1)
	var reads atomic.Uint64
	var healthy atomic.Bool
	healthy.Store(true)
	startRBACManager(t, &managerJournal{epoch: uuid.NewString()}, provider,
		func(ctx context.Context, _ string) (rbac.Snapshot, error) {
			if reads.Add(1) == 1 {
				return rbac.Snapshot{}, rbac.ErrUnavailable
			}
			select {
			case <-ctx.Done():
				return rbac.Snapshot{}, ctx.Err()
			case <-time.After(350 * time.Millisecond):
				return snapshot, nil
			}
		}, &healthy)
	require.Eventually(t, func() bool {
		return providerReady(provider, rbac.ClusterTierID)
	}, 4*time.Second, 5*time.Millisecond, "a rebuild slower than the watch poll must recover after one failed attempt")
	require.EqualValues(t, 2, reads.Load(), "retry polling must not invent new dirty generations")
}

func TestManagerRepairsLostCallbacksWithoutRebuildingHealthyQuietTiers(t *testing.T) {
	provider, err := opa.NewProvider(t.Context())
	require.NoError(t, err)
	t.Cleanup(provider.Close)
	require.NoError(t, provider.RequireOriginProgress(5*time.Second))
	first := managerSnapshot(t, rbac.ClusterTierID, "", 1)
	second := managerSnapshot(t, rbac.ClusterTierID, "", 2)
	var changed, fail atomic.Bool
	var reads, probes atomic.Uint64
	runRBACManager(t, rbac.ManagerDeps{
		Journal: &managerJournal{epoch: uuid.NewString()}, Workers: 1,
		Invalidate: provider.InvalidateTier, CheckProgress: func(time.Time) error { return nil },
		ReadSnapshot: func(context.Context, string) (rbac.Snapshot, error) {
			reads.Add(1)
			if fail.Load() {
				return rbac.Snapshot{}, rbac.ErrUnavailable
			}
			if changed.Load() {
				return second, nil
			}
			return first, nil
		},
		Prepare: func(ctx context.Context, tier auth.AuthorizationTier) (rbac.PreparedPublication, error) {
			return provider.PrepareTier(ctx, tier)
		},
		CheckOrigin: func(_ context.Context, _ string, origin rbac.SnapshotOrigin) error {
			probes.Add(1)
			if fail.Load() || changed.Load() && origin != second.Origin {
				return rbac.ErrUnavailable
			}
			return nil
		},
		RecordOrigin: provider.RecordOriginProgress,
	})
	require.Eventually(t, func() bool { return probes.Load() >= 3 }, 4*time.Second, 5*time.Millisecond)
	require.EqualValues(t, 1, reads.Load(), "quiet tiers need identity checks, not full rescans")
	fail.Store(true)
	changed.Store(true)
	require.Eventually(t, func() bool { return !providerReady(provider, rbac.ClusterTierID) }, 2*time.Second, 5*time.Millisecond)
	require.Never(t, func() bool { return providerReady(provider, rbac.ClusterTierID) }, 200*time.Millisecond, 5*time.Millisecond)
	fail.Store(false)
	require.Eventually(t, func() bool {
		decision, err := provider.Authorize(t.Context(), &auth.Principal{Subject: "fixture", AuthMethod: "static-users"},
			"namespace.read", auth.ResourceContext{Kind: "namespace"})
		return err == nil && decision.AuthorizationSnapshot != nil && decision.AuthorizationSnapshot.Tiers[0].Revision == 2
	}, 3*time.Second, 5*time.Millisecond)
}

func TestManagerRefusesFirstPublicationWhenCanonicalOriginAlreadyChanged(t *testing.T) {
	provider, err := opa.NewProvider(t.Context())
	require.NoError(t, err)
	t.Cleanup(provider.Close)
	require.NoError(t, provider.RequireOriginProgress(5*time.Second))
	snapshot := managerSnapshot(t, rbac.ClusterTierID, "", 1)
	var checks atomic.Uint64
	runRBACManager(t, rbac.ManagerDeps{
		Journal: &managerJournal{epoch: uuid.NewString()}, Workers: 1,
		Invalidate: provider.InvalidateTier, CheckProgress: func(time.Time) error { return nil },
		ReadSnapshot: func(context.Context, string) (rbac.Snapshot, error) { return snapshot, nil },
		Prepare: func(ctx context.Context, tier auth.AuthorizationTier) (rbac.PreparedPublication, error) {
			return provider.PrepareTier(ctx, tier)
		},
		CheckOrigin: func(context.Context, string, rbac.SnapshotOrigin) error {
			checks.Add(1)
			return rbac.ErrUnavailable
		},
		RecordOrigin: provider.RecordOriginProgress,
	})
	require.Never(t, func() bool { return providerReady(provider, rbac.ClusterTierID) }, 200*time.Millisecond, time.Millisecond)
	require.Greater(t, checks.Load(), uint64(0))
}

func TestManagerPendingRebuildsCannotStarveInstalledOriginChecks(t *testing.T) {
	provider, err := opa.NewProvider(t.Context())
	require.NoError(t, err)
	t.Cleanup(provider.Close)
	require.NoError(t, provider.RequireOriginProgress(400*time.Millisecond))
	cluster := managerSnapshot(t, rbac.ClusterTierID, "", 1)
	var reads, renewals atomic.Uint64
	manager := runRBACManager(t, rbac.ManagerDeps{
		Journal: &managerJournal{epoch: uuid.NewString()}, Workers: 1, MaxStaleness: 400 * time.Millisecond,
		Invalidate: provider.InvalidateTier, CheckProgress: func(time.Time) error { return nil },
		ReadSnapshot: func(ctx context.Context, tier string) (rbac.Snapshot, error) {
			if tier == rbac.ClusterTierID {
				return cluster, nil
			}
			reads.Add(1)
			select {
			case <-ctx.Done():
				return rbac.Snapshot{}, ctx.Err()
			case <-time.After(30 * time.Millisecond):
				return rbac.Snapshot{}, rbac.ErrUnavailable
			}
		},
		Prepare: func(ctx context.Context, tier auth.AuthorizationTier) (rbac.PreparedPublication, error) {
			return provider.PrepareTier(ctx, tier)
		},
		CheckOrigin: func(context.Context, string, rbac.SnapshotOrigin) error { return nil },
		RecordOrigin: func(tier string, identity rbac.ProjectionIdentity, at time.Time) error {
			renewals.Add(1)
			return provider.RecordOriginProgress(tier, identity, at)
		},
	})
	require.Eventually(t, func() bool { return providerReady(provider, rbac.ClusterTierID) }, time.Second, time.Millisecond)
	for range 64 {
		require.NoError(t, manager.RequestTier(uuid.NewString()))
	}
	require.Never(t, func() bool { return !providerReady(provider, rbac.ClusterTierID) },
		800*time.Millisecond, time.Millisecond, "queued namespace failures must not expire healthy cluster authority")
	require.Positive(t, renewals.Load(), "freshness must be renewed by real origin checks")
	require.Greater(t, reads.Load(), uint64(1), "origin checks must not starve queued rebuilds either")
}

func TestManagerCannotRenewOriginProofAcrossAnInvalidatedGeneration(t *testing.T) {
	provider, err := opa.NewProvider(t.Context())
	require.NoError(t, err)
	t.Cleanup(provider.Close)
	journal := &managerJournal{epoch: uuid.NewString()}
	first := managerSnapshot(t, rbac.ClusterTierID, "", 1)
	second := managerSnapshot(t, rbac.ClusterTierID, "", 2)
	var changed atomic.Bool
	var staleRenewals atomic.Uint64
	var checks atomic.Uint64
	started, release := make(chan struct{}), make(chan struct{})
	runRBACManager(t, rbac.ManagerDeps{
		Journal: journal, Workers: 1, Invalidate: provider.InvalidateTier,
		CheckProgress: func(time.Time) error { return nil },
		ReadSnapshot: func(context.Context, string) (rbac.Snapshot, error) {
			if changed.Load() {
				return second, nil
			}
			return first, nil
		},
		Prepare: func(ctx context.Context, tier auth.AuthorizationTier) (rbac.PreparedPublication, error) {
			return provider.PrepareTier(ctx, tier)
		},
		CheckOrigin: func(ctx context.Context, _ string, origin rbac.SnapshotOrigin) error {
			if origin == first.Origin && checks.Add(1) > 1 {
				close(started)
				select {
				case <-release:
				case <-ctx.Done():
					return ctx.Err()
				}
			}
			return nil
		},
		RecordOrigin: func(tier string, identity rbac.ProjectionIdentity, at time.Time) error {
			if identity == first.State.Projection {
				staleRenewals.Add(1)
			}
			return provider.RecordOriginProgress(tier, identity, at)
		},
	})
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("origin verification did not start")
	}
	changed.Store(true)
	payload, err := json.Marshal(datastore.RBACTierProgress{TierID: rbac.ClusterTierID, Revision: 2})
	require.NoError(t, err)
	journal.append("RBACTier", payload)
	require.Eventually(t, func() bool { return !providerReady(provider, rbac.ClusterTierID) }, time.Second, 5*time.Millisecond)
	close(release)
	require.Eventually(t, func() bool { return providerReady(provider, rbac.ClusterTierID) }, 3*time.Second, 5*time.Millisecond)
	require.Zero(t, staleRenewals.Load())
}

func TestManagerNamespaceDemandIsCoalescedWithoutRestartingScans(t *testing.T) {
	provider, err := opa.NewProvider(t.Context())
	require.NoError(t, err)
	t.Cleanup(provider.Close)
	journal := &managerJournal{epoch: uuid.NewString()}
	tier := uuid.NewString()
	cluster, namespace := managerSnapshot(t, rbac.ClusterTierID, "", 1), managerSnapshot(t, tier, "shop", 1)
	var healthy atomic.Bool
	healthy.Store(true)
	var lookups, reads atomic.Uint64
	started, release := make(chan struct{}), make(chan struct{})
	manager := startRBACManager(t, journal, provider, func(ctx context.Context, requested string) (rbac.Snapshot, error) {
		if requested == rbac.ClusterTierID {
			return cluster, nil
		}
		reads.Add(1)
		close(started)
		select {
		case <-ctx.Done():
			return rbac.Snapshot{}, ctx.Err()
		case <-release:
			return namespace, nil
		}
	}, &healthy, func(context.Context, string) (string, error) {
		lookups.Add(1)
		return tier, nil
	})
	provider.SetNamespaceLoader(func(name string) { _ = manager.RequestNamespace(name) })
	require.Eventually(t, func() bool { return providerReady(provider, rbac.ClusterTierID) }, 2*time.Second, 5*time.Millisecond)
	_, err = provider.Authorize(t.Context(), nil, "product.read", auth.ResourceContext{Attrs: map[string]any{"namespace": "shop"}})
	require.ErrorIs(t, err, opa.ErrUnavailable)
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("lazy namespace scan did not start")
	}
	for range 100 {
		require.NoError(t, manager.RequestTier(tier))
		require.NoError(t, manager.RequestNamespace("shop"))
	}
	close(release)
	require.Eventually(t, func() bool { return providerReady(provider, tier) }, 2*time.Second, 5*time.Millisecond)
	require.EqualValues(t, 1, reads.Load(), "ordinary demand must not dirty an in-flight rebuild")
	require.NoError(t, manager.RequestNamespace("shop"))
	require.Never(t, func() bool { return reads.Load() > 1 }, 150*time.Millisecond, 5*time.Millisecond)
	require.LessOrEqual(t, lookups.Load(), uint64(2), "only an unresolved name may need another lookup")
}

func TestManagerBoundsWorkersQueueAndOneScanPerTier(t *testing.T) {
	provider, err := opa.NewProvider(t.Context())
	require.NoError(t, err)
	t.Cleanup(provider.Close)
	journal := &managerJournal{epoch: uuid.NewString()}
	var healthy atomic.Bool
	healthy.Store(true)
	var active, maximum atomic.Int64
	started := make(chan struct{}, 4)
	manager := startRBACManager(t, journal, provider, func(ctx context.Context, _ string) (rbac.Snapshot, error) {
		current := active.Add(1)
		defer active.Add(-1)
		for old := maximum.Load(); current > old && !maximum.CompareAndSwap(old, current); old = maximum.Load() {
		}
		started <- struct{}{}
		<-ctx.Done()
		return rbac.Snapshot{}, ctx.Err()
	}, &healthy)
	for range 3 {
		require.NoError(t, manager.RequestTier(uuid.NewString()))
	}
	for range 4 {
		select {
		case <-started:
		case <-time.After(2 * time.Second):
			t.Fatal("four bounded workers did not start")
		}
	}
	for range 1024 {
		require.NoError(t, manager.RequestTier(uuid.NewString()))
	}
	require.ErrorIs(t, manager.RequestTier(uuid.NewString()), rbac.ErrUnavailable)
	for range 100 {
		require.NoError(t, manager.RequestTier(rbac.ClusterTierID))
	}
	require.EqualValues(t, 4, maximum.Load())
	require.False(t, providerReady(provider, rbac.ClusterTierID))
}

func providerReady(provider *opa.Provider, tier string) bool {
	readiness, ok := provider.AuthorizationReadiness(tier)
	return ok && readiness.Ready
}

func TestManagerCanonicalRepositoryChangesInvalidateInstalledTiers(t *testing.T) {
	for _, root := range []bool{false, true} {
		t.Run(fmt.Sprintf("root=%t", root), func(t *testing.T) {
			provider, err := opa.NewProvider(t.Context())
			require.NoError(t, err)
			t.Cleanup(provider.Close)
			journal := &managerJournal{epoch: uuid.NewString()}
			tier, name := uuid.NewString(), "shop"
			if root {
				tier, name = rbac.ClusterTierID, "gitstore-system"
			}
			snapshot := managerSnapshot(t, tier, name, 1)
			cluster := managerSnapshot(t, rbac.ClusterTierID, "", 1)
			var healthy, unavailable atomic.Bool
			healthy.Store(true)
			manager := startRBACManager(t, journal, provider, func(_ context.Context, requested string) (rbac.Snapshot, error) {
				if requested != tier {
					return cluster, nil
				}
				if unavailable.Load() {
					return rbac.Snapshot{}, rbac.ErrUnavailable
				}
				return snapshot, nil
			}, &healthy)
			require.NoError(t, manager.RequestTier(tier))
			require.Eventually(t, func() bool { return providerReady(provider, tier) }, 2*time.Second, 5*time.Millisecond)
			unavailable.Store(true)
			payload, err := json.Marshal(datastore.Repository{
				Name: "gitstore-system", Namespace: name, UID: snapshot.Origin.RepositoryID,
				DefaultBranch: "replacement",
			})
			require.NoError(t, err)
			journal.append("Repository", payload)
			require.Eventually(t, func() bool { return !providerReady(provider, tier) }, time.Second, 5*time.Millisecond)
			require.Never(t, func() bool { return providerReady(provider, tier) }, 200*time.Millisecond, 5*time.Millisecond)
		})
	}
}

func TestManagerHonorsLowerWorkerQueueAndTimeoutBounds(t *testing.T) {
	journal := &managerJournal{epoch: uuid.NewString()}
	started, finished := make(chan struct{}, 4), make(chan struct{}, 4)
	deps := rbac.ManagerDeps{
		Journal: journal, Invalidate: func(string) {}, CheckProgress: func(time.Time) error { return nil },
		ReadSnapshot: func(ctx context.Context, _ string) (rbac.Snapshot, error) {
			started <- struct{}{}
			<-ctx.Done()
			finished <- struct{}{}
			return rbac.Snapshot{}, ctx.Err()
		},
		Prepare: func(context.Context, auth.AuthorizationTier) (rbac.PreparedPublication, error) {
			t.Error("timed-out scans must not reach translation")
			return nil, rbac.ErrUnavailable
		},
		Workers: 1, QueueSize: 1, Timeout: 150 * time.Millisecond, MaxStaleness: time.Second,
	}
	manager, err := rbac.NewManager(deps)
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- manager.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Error("bounded manager did not stop")
		}
	})
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("configured worker did not start")
	}
	require.NoError(t, manager.RequestTier(uuid.NewString()))
	require.ErrorIs(t, manager.RequestTier(uuid.NewString()), rbac.ErrUnavailable)
	select {
	case <-started:
		t.Fatal("more than one configured worker started")
	case <-time.After(30 * time.Millisecond):
	}
	select {
	case <-finished:
	case <-time.After(time.Second):
		t.Fatal("configured scan timeout was not enforced")
	}
}

func TestManagerRetainsNewerDirtyRevisionAcrossCapturedScan(t *testing.T) {
	provider, err := opa.NewProvider(t.Context())
	require.NoError(t, err)
	t.Cleanup(provider.Close)
	journal := &managerJournal{epoch: uuid.NewString()}
	first, newer := managerSnapshot(t, rbac.ClusterTierID, "", 1), managerSnapshot(t, rbac.ClusterTierID, "", 2)
	var healthy atomic.Bool
	healthy.Store(true)
	var reads atomic.Uint64
	started, release := make(chan struct{}), make(chan struct{})
	startRBACManager(t, journal, provider, func(ctx context.Context, _ string) (rbac.Snapshot, error) {
		if reads.Add(1) == 1 {
			close(started)
			select {
			case <-ctx.Done():
				return rbac.Snapshot{}, ctx.Err()
			case <-release:
				return first, nil
			}
		}
		return newer, nil
	}, &healthy)
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("snapshot scan did not start")
	}
	progress, err := json.Marshal(datastore.RBACTierProgress{TierID: rbac.ClusterTierID, Revision: 2})
	require.NoError(t, err)
	journal.append("RBACTier", progress)
	require.Eventually(t, func() bool { return journal.observed.Load() == 1 }, time.Second, 5*time.Millisecond)
	close(release)
	require.Never(t, func() bool {
		snapshot, ok := provider.AuthorizationSnapshot()
		return ok && snapshot.Tiers[0].Revision == 1
	}, 250*time.Millisecond, 5*time.Millisecond)
	require.Eventually(t, func() bool {
		snapshot, ok := provider.AuthorizationSnapshot()
		return ok && snapshot.Tiers[0].Revision == 2
	}, 3*time.Second, 10*time.Millisecond)
	quietReads := reads.Load()
	require.Never(t, func() bool { return !providerReady(provider, rbac.ClusterTierID) }, 250*time.Millisecond, 5*time.Millisecond)
	require.Equal(t, quietReads, reads.Load(), "healthy quiet tiers do not rebuild just because no resource changed")
}

func TestManagerLostEpochAndProgressRequireCompleteResync(t *testing.T) {
	provider, err := opa.NewProvider(t.Context())
	require.NoError(t, err)
	t.Cleanup(provider.Close)
	journal := &managerJournal{epoch: uuid.NewString()}
	snapshot := managerSnapshot(t, rbac.ClusterTierID, "", 1)
	var healthy, fail atomic.Bool
	healthy.Store(true)
	startRBACManager(t, journal, provider, func(context.Context, string) (rbac.Snapshot, error) {
		if fail.Load() {
			return rbac.Snapshot{}, rbac.ErrUnavailable
		}
		return snapshot, nil
	}, &healthy)
	require.Eventually(t, func() bool { return providerReady(provider, rbac.ClusterTierID) }, 2*time.Second, 5*time.Millisecond)
	healthy.Store(false)
	require.Eventually(t, func() bool { return !providerReady(provider, rbac.ClusterTierID) }, time.Second, 5*time.Millisecond)
	fail.Store(true)
	healthy.Store(true)
	journal.mu.Lock()
	journal.epoch = uuid.NewString()
	journal.mu.Unlock()
	require.Never(t, func() bool { return providerReady(provider, rbac.ClusterTierID) }, 250*time.Millisecond, 5*time.Millisecond)
	fail.Store(false)
	require.Eventually(t, func() bool { return providerReady(provider, rbac.ClusterTierID) }, 3*time.Second, 5*time.Millisecond)
}

func TestManagerNamespaceFailureAndTerminationDoNotPublishOtherUIDs(t *testing.T) {
	provider, err := opa.NewProvider(t.Context())
	require.NoError(t, err)
	t.Cleanup(provider.Close)
	journal := &managerJournal{epoch: uuid.NewString()}
	first, second := uuid.NewString(), uuid.NewString()
	snapshots := map[string]rbac.Snapshot{rbac.ClusterTierID: managerSnapshot(t, rbac.ClusterTierID, "", 1),
		first: managerSnapshot(t, first, "shop", 1)}
	var healthy atomic.Bool
	healthy.Store(true)
	manager := startRBACManager(t, journal, provider, func(_ context.Context, tier string) (rbac.Snapshot, error) {
		if snapshot, ok := snapshots[tier]; ok {
			return snapshot, nil
		}
		return rbac.Snapshot{}, rbac.ErrUnavailable
	}, &healthy)
	require.NoError(t, manager.RequestTier(first))
	require.NoError(t, manager.RequestTier(second))
	require.Eventually(t, func() bool { return providerReady(provider, first) }, 2*time.Second, 5*time.Millisecond)
	require.True(t, providerReady(provider, rbac.ClusterTierID))
	require.False(t, providerReady(provider, second))
	now := time.Now().UTC()
	payload, err := json.Marshal(datastore.Namespace{UID: first, Name: "shop", DeletionTimestamp: &now})
	require.NoError(t, err)
	journal.append("Namespace", payload)
	require.Eventually(t, func() bool { return !providerReady(provider, first) }, time.Second, 5*time.Millisecond)
	require.Never(t, func() bool { return providerReady(provider, first) }, 250*time.Millisecond, 5*time.Millisecond)
	require.True(t, providerReady(provider, rbac.ClusterTierID))
}

func TestManagerRejectsPartialSnapshotAndStopsCancelledScans(t *testing.T) {
	provider, err := opa.NewProvider(t.Context())
	require.NoError(t, err)
	t.Cleanup(provider.Close)
	journal := &managerJournal{epoch: uuid.NewString()}
	var healthy atomic.Bool
	healthy.Store(true)
	snapshot := managerSnapshot(t, rbac.ClusterTierID, "", 1)
	snapshot.ModelVersion = "partial"
	var reads atomic.Uint64
	startRBACManager(t, journal, provider, func(ctx context.Context, _ string) (rbac.Snapshot, error) {
		if reads.Add(1) == 1 {
			return snapshot, nil
		}
		<-ctx.Done()
		return rbac.Snapshot{}, ctx.Err()
	}, &healthy)
	require.Eventually(t, func() bool { return reads.Load() >= 2 }, 3*time.Second, 5*time.Millisecond)
	require.False(t, providerReady(provider, rbac.ClusterTierID))
}

func TestManagerNamespaceReuseIgnoresDelayedOldUIDDeletion(t *testing.T) {
	provider, err := opa.NewProvider(t.Context())
	require.NoError(t, err)
	t.Cleanup(provider.Close)
	journal := &managerJournal{epoch: uuid.NewString()}
	oldUID, newUID := uuid.NewString(), uuid.NewString()
	snapshots := map[string]rbac.Snapshot{
		rbac.ClusterTierID: managerSnapshot(t, rbac.ClusterTierID, "", 1),
		oldUID:             managerSnapshot(t, oldUID, "shop", 1),
		newUID:             managerSnapshot(t, newUID, "shop", 1),
	}
	var healthy atomic.Bool
	healthy.Store(true)
	manager := startRBACManager(t, journal, provider, func(_ context.Context, tier string) (rbac.Snapshot, error) {
		return snapshots[tier], nil
	}, &healthy)
	require.NoError(t, manager.RequestTier(oldUID))
	require.Eventually(t, func() bool { return providerReady(provider, oldUID) }, 2*time.Second, 5*time.Millisecond)
	payload, err := json.Marshal(datastore.Namespace{UID: newUID, Name: "shop"})
	require.NoError(t, err)
	journal.append("Namespace", payload, datastore.ResourceWatchAdded)
	require.Eventually(t, func() bool {
		return providerReady(provider, newUID) && !providerReady(provider, oldUID)
	}, 2*time.Second, 5*time.Millisecond)
	payload, err = json.Marshal(datastore.Namespace{UID: oldUID, Name: "shop"})
	require.NoError(t, err)
	journal.append("Namespace", payload, datastore.ResourceWatchDeleted)
	require.Eventually(t, func() bool { return journal.observed.Load() == 2 }, time.Second, 5*time.Millisecond)
	require.Never(t, func() bool {
		return !providerReady(provider, newUID) || providerReady(provider, oldUID)
	}, 200*time.Millisecond, 5*time.Millisecond)
}

func TestManagerRetentionLossCannotRestoreReadinessWithoutNewCompleteScan(t *testing.T) {
	provider, err := opa.NewProvider(t.Context())
	require.NoError(t, err)
	t.Cleanup(provider.Close)
	journal := &managerJournal{epoch: uuid.NewString()}
	snapshot := managerSnapshot(t, rbac.ClusterTierID, "", 1)
	var healthy, fail atomic.Bool
	var reads atomic.Uint64
	healthy.Store(true)
	startRBACManager(t, journal, provider, func(context.Context, string) (rbac.Snapshot, error) {
		reads.Add(1)
		if fail.Load() {
			return rbac.Snapshot{}, rbac.ErrUnavailable
		}
		return snapshot, nil
	}, &healthy)
	require.Eventually(t, func() bool { return providerReady(provider, rbac.ClusterTierID) }, 2*time.Second, 5*time.Millisecond)
	before := reads.Load()
	fail.Store(true)
	journal.mu.Lock()
	for range 4 {
		journal.events = append(journal.events, datastore.ResourceWatchEvent{Epoch: journal.epoch,
			Sequence: uint64(len(journal.events) + 1), Type: datastore.ResourceWatchBookmark, At: time.Now()})
	}
	journal.oldest = 4
	journal.mu.Unlock()
	require.Eventually(t, func() bool {
		return reads.Load() > before && !providerReady(provider, rbac.ClusterTierID)
	}, 2*time.Second, 5*time.Millisecond)
	require.Never(t, func() bool { return providerReady(provider, rbac.ClusterTierID) }, 200*time.Millisecond, 5*time.Millisecond)
	fail.Store(false)
	require.Eventually(t, func() bool { return providerReady(provider, rbac.ClusterTierID) }, 3*time.Second, 5*time.Millisecond)
}

func TestManagerNamespaceReplacementFencesAnUnidentifiedInFlightScan(t *testing.T) {
	provider, err := opa.NewProvider(t.Context())
	require.NoError(t, err)
	t.Cleanup(provider.Close)
	journal := &managerJournal{epoch: uuid.NewString()}
	oldUID, newUID := uuid.NewString(), uuid.NewString()
	cluster := managerSnapshot(t, rbac.ClusterTierID, "", 1)
	oldSnapshot, newSnapshot := managerSnapshot(t, oldUID, "shop", 1), managerSnapshot(t, newUID, "shop", 1)
	started, release := make(chan struct{}), make(chan struct{})
	var healthy, allowNew atomic.Bool
	var reads atomic.Uint64
	healthy.Store(true)
	manager := startRBACManager(t, journal, provider, func(ctx context.Context, tier string) (rbac.Snapshot, error) {
		switch tier {
		case rbac.ClusterTierID:
			return cluster, nil
		case newUID:
			if !allowNew.Load() {
				return rbac.Snapshot{}, rbac.ErrUnavailable
			}
			return newSnapshot, nil
		case oldUID:
			if reads.Add(1) != 1 {
				return rbac.Snapshot{}, rbac.ErrUnavailable
			}
			close(started)
			select {
			case <-ctx.Done():
				return rbac.Snapshot{}, ctx.Err()
			case <-release:
				return oldSnapshot, nil
			}
		default:
			return rbac.Snapshot{}, rbac.ErrUnavailable
		}
	}, &healthy)
	require.NoError(t, manager.RequestTier(oldUID))
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("old UID scan did not start")
	}
	payload, err := json.Marshal(datastore.Namespace{UID: newUID, Name: "shop"})
	require.NoError(t, err)
	journal.append("Namespace", payload, datastore.ResourceWatchAdded)
	require.Eventually(t, func() bool { return journal.observed.Load() == 1 }, time.Second, 5*time.Millisecond)
	close(release)
	require.Never(t, func() bool { return providerReady(provider, oldUID) }, 250*time.Millisecond, time.Millisecond)
	allowNew.Store(true)
	require.Eventually(t, func() bool { return providerReady(provider, newUID) }, 2*time.Second, 5*time.Millisecond)
}

func TestManagerScratchPressureFailsClosedBeforeSourceAllocation(t *testing.T) {
	provider, err := opa.NewProvider(t.Context())
	require.NoError(t, err)
	t.Cleanup(provider.Close)
	journal := &managerJournal{epoch: uuid.NewString()}
	scratch := rbac.NewMemoryBudget(rbac.RebuildScratchLimit)
	held, err := scratch.Reserve(rbac.RebuildScratchLimit)
	require.NoError(t, err)
	defer held.Close()
	snapshot := managerSnapshot(t, rbac.ClusterTierID, "", 1)
	var scans atomic.Uint64
	runRBACManager(t, rbac.ManagerDeps{
		Journal: journal, Scratch: scratch, Workers: 1,
		Invalidate: provider.InvalidateTier, CheckProgress: func(time.Time) error { return nil },
		ReadSnapshot: func(context.Context, string) (rbac.Snapshot, error) {
			scans.Add(1)
			return snapshot, nil
		},
		Prepare: func(ctx context.Context, tier auth.AuthorizationTier) (rbac.PreparedPublication, error) {
			return provider.PrepareTier(ctx, tier)
		},
	})
	require.Never(t, func() bool {
		return scans.Load() != 0 || providerReady(provider, rbac.ClusterTierID)
	}, 150*time.Millisecond, time.Millisecond)
	held.Close()
	require.Eventually(t, func() bool {
		return providerReady(provider, rbac.ClusterTierID) && scratch.Used() == 0
	}, 2*time.Second, 5*time.Millisecond)
}
