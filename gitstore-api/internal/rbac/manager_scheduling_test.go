// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package rbac

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/gitstore-dev/gitstore/api/internal/auth"
	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	apiruntime "github.com/gitstore-dev/gitstore/api/internal/runtime"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

type rebuildProgressJournal struct{ datastore.ResourceWatchJournal }

func (rebuildProgressJournal) Bounds(context.Context) (datastore.ResourceWatchBounds, error) {
	return datastore.ResourceWatchBounds{Epoch: "test-epoch"}, nil
}

type rebuildProgressPublication struct{ installed *bool }

func (p rebuildProgressPublication) Install() error { *p.installed = true; return nil }
func (rebuildProgressPublication) Close()           {}

func TestManagerFailedScanBackoffDoesNotDelayNewDirtyWork(t *testing.T) {
	for _, newer := range []string{"unchanged", "during", "after"} {
		t.Run(newer, func(t *testing.T) {
			first, next := make(chan struct{}), make(chan struct{})
			var manager *Manager
			var err error
			reads := 0
			manager, err = NewManager(ManagerDeps{
				Journal: rebuildProgressJournal{},
				ReadSnapshot: func(ctx context.Context, tier string) (Snapshot, error) {
					reads++
					if reads == 1 {
						if newer == "during" {
							manager.mu.Lock()
							err := manager.queueLocked(tier, 2, false, true)
							manager.mu.Unlock()
							if err != nil {
								return Snapshot{}, err
							}
						}
						close(first)
						return Snapshot{}, ErrUnavailable
					}
					close(next)
					<-ctx.Done()
					return Snapshot{}, ctx.Err()
				},
				Prepare: func(context.Context, auth.AuthorizationTier) (PreparedPublication, error) {
					t.Error("failed source scans cannot prepare or install a publication")
					return nil, ErrUnavailable
				},
				Invalidate: func(string) {}, CheckProgress: func(time.Time) error { return nil },
				Workers: 1,
			})
			require.NoError(t, err)
			manager.continuous = true
			manager.signalLocked()
			ctx, cancel := context.WithCancel(t.Context())
			done := make(chan struct{})
			go func() { defer close(done); manager.worker(ctx) }()
			t.Cleanup(func() {
				cancel()
				select {
				case <-done:
				case <-time.After(time.Second):
					t.Error("manager worker did not stop")
				}
			})
			select {
			case <-first:
			case <-time.After(time.Second):
				t.Fatal("source scan did not start")
			}
			if newer == "after" || newer == "unchanged" {
				require.Eventually(t, func() bool {
					manager.mu.Lock()
					defer manager.mu.Unlock()
					work := manager.work[ClusterTierID]
					return !work.running && work.retryAt.After(manager.deps.Clock.Now())
				}, 500*time.Millisecond, time.Millisecond, "unchanged failed work must retain its bounded retry delay")
			}
			if newer == "after" {
				manager.mu.Lock()
				err := manager.queueLocked(ClusterTierID, 2, false, true)
				manager.mu.Unlock()
				require.NoError(t, err)
			}
			if newer != "unchanged" {
				select {
				case <-next:
				case <-time.After(500 * time.Millisecond):
					t.Fatal("the failed older scan delayed newer work")
				}
			}
		})
	}
}

func TestManagerFailedOriginProofBackoffDoesNotDelayNewDirtyRevision(t *testing.T) {
	for _, batched := range []bool{false, true} {
		for _, newer := range []string{"unchanged", "during", "after", "same-revision"} {
			t.Run(fmt.Sprintf("batched=%t/%s", batched, newer), func(t *testing.T) {
				tier := ClusterTierID
				if batched {
					tier = uuid.NewString()
				}
				first, next := make(chan struct{}), make(chan struct{})
				var manager *Manager
				check := func() error {
					if newer == "during" || newer == "same-revision" {
						revision := uint64(1)
						if newer == "during" {
							revision = 2
						}
						manager.mu.Lock()
						err := manager.queueLocked(tier, revision, false, true)
						manager.mu.Unlock()
						if err != nil {
							return err
						}
					}
					close(first)
					return ErrUnavailable
				}
				var err error
				manager, err = NewManager(ManagerDeps{
					Journal: rebuildProgressJournal{},
					ReadSnapshot: func(ctx context.Context, _ string) (Snapshot, error) {
						close(next)
						<-ctx.Done()
						return Snapshot{}, ctx.Err()
					},
					Prepare: func(context.Context, auth.AuthorizationTier) (PreparedPublication, error) {
						t.Error("failed source scans cannot prepare a publication")
						return nil, ErrUnavailable
					},
					CheckOrigin: func(context.Context, string, SnapshotOrigin) error { return check() },
					CheckOrigins: func(context.Context, []OriginCheck) []error {
						return []error{check()}
					},
					RecordOrigin: func(string, ProjectionIdentity, time.Time) error {
						t.Error("failed origin proofs cannot renew publication freshness")
						return ErrUnavailable
					},
					Invalidate: func(string) {}, CheckProgress: func(time.Time) error { return nil },
					Workers: 1,
				})
				require.NoError(t, err)
				manager.work[ClusterTierID].pending = false
				manager.work[ClusterTierID].blocked = batched
				manager.work[tier] = &tierWork{generation: 1, revision: 1, installed: true}
				manager.pending = 0
				manager.continuous = true
				manager.signalLocked()
				ctx, cancel := context.WithCancel(t.Context())
				done := make(chan struct{})
				go func() { defer close(done); manager.worker(ctx) }()
				t.Cleanup(func() {
					cancel()
					select {
					case <-done:
					case <-time.After(time.Second):
						t.Error("manager worker did not stop")
					}
				})
				select {
				case <-first:
				case <-time.After(time.Second):
					t.Fatal("origin proof did not start")
				}
				if newer != "during" {
					require.Eventually(t, func() bool {
						manager.mu.Lock()
						defer manager.mu.Unlock()
						work := manager.work[tier]
						return !work.running && work.retryAt.After(manager.deps.Clock.Now())
					}, 500*time.Millisecond, time.Millisecond, "unchanged failed proofs must retain their retry delay")
				}
				if newer == "after" {
					manager.mu.Lock()
					err := manager.queueLocked(tier, 2, false, true)
					manager.mu.Unlock()
					require.NoError(t, err)
				}
				if newer == "during" || newer == "after" {
					select {
					case <-next:
					case <-time.After(500 * time.Millisecond):
						t.Fatal("the failed older origin proof delayed a newer durable revision")
					}
				}
			})
		}
	}
}

func TestManagerCompleteScanCoversConcurrentProgressButNotIdentityChanges(t *testing.T) {
	for _, scenario := range []string{"covered", "newer-revision", "namespace", "repository"} {
		t.Run(scenario, func(t *testing.T) {
			origin := SnapshotOrigin{
				NamespaceUID: uuid.NewString(), Namespace: "gitstore-system", NamespaceVersion: "1",
				Active: true, Root: true, RepositoryID: uuid.NewString(), GitRef: "refs/heads/main",
				GitRevision: fmt.Sprintf("main@sha1:%040x", 2), TreeDigest: fmt.Sprintf("%064x", 2),
			}
			installed := false
			var manager *Manager
			var err error
			manager, err = NewManager(ManagerDeps{
				Journal: rebuildProgressJournal{},
				ReadSnapshot: func(context.Context, string) (Snapshot, error) {
					var payload any = datastore.RBACTierProgress{TierID: ClusterTierID, Revision: 2}
					kind := "RBACTier"
					switch scenario {
					case "newer-revision":
						payload = datastore.RBACTierProgress{TierID: ClusterTierID, Revision: 3}
					case "namespace":
						kind, payload = "Namespace", datastore.Namespace{
							UID: origin.NamespaceUID, Name: origin.Namespace, ResourceVersion: "2"}
					case "repository":
						kind, payload = "Repository", datastore.Repository{
							UID: origin.RepositoryID, Name: "gitstore-system", Namespace: origin.Namespace, DefaultBranch: "replacement"}
					}
					data, err := json.Marshal(payload)
					require.NoError(t, err)
					manager.mu.Lock()
					err = manager.eventLocked(datastore.ResourceWatchEvent{
						Kind: kind, Type: datastore.ResourceWatchModified, Payload: data})
					manager.mu.Unlock()
					require.NoError(t, err)
					return Snapshot{ModelVersion: ModelVersion, Origin: origin,
						State: TierState{TierID: ClusterTierID, Revision: 2}}, nil
				},
				Prepare: func(context.Context, auth.AuthorizationTier) (PreparedPublication, error) {
					return rebuildProgressPublication{&installed}, nil
				},
				Invalidate: func(string) {}, CheckProgress: func(time.Time) error { return nil },
			})
			require.NoError(t, err)
			manager.continuous, manager.cursor.Epoch = true, "test-epoch"
			tier, work, generation, probe := manager.take()
			require.False(t, probe)
			err = manager.rebuild(t.Context(), tier, work, generation)
			if scenario == "covered" {
				require.NoError(t, err)
				require.True(t, installed)
				require.Zero(t, manager.pending, "already covered progress must not force another full scan")
			} else {
				require.ErrorIs(t, err, errWorkSuperseded)
				require.False(t, installed, "a revision counter cannot cover a lifecycle or canonical-mapping change")
				require.True(t, work.pending)
			}
		})
	}
}

func TestManagerJournalDoesNotWarmUnrequestedNamespaces(t *testing.T) {
	manager, err := NewManager(ManagerDeps{
		Journal: struct{ datastore.ResourceWatchJournal }{},
		ReadSnapshot: func(context.Context, string) (Snapshot, error) {
			t.Fatal("journal events must not initiate an unrequested snapshot")
			return Snapshot{}, ErrUnavailable
		},
		Prepare: func(context.Context, auth.AuthorizationTier) (PreparedPublication, error) {
			t.Fatal("journal events must not prepare an unrequested tier")
			return nil, ErrUnavailable
		},
		Invalidate:    func(string) {},
		CheckProgress: func(time.Time) error { return nil },
		QueueSize:     4,
	})
	require.NoError(t, err)
	manager.work[ClusterTierID].namespace = "gitstore-system"
	inFlight := uuid.NewString()
	manager.work[inFlight] = &tierWork{running: true}
	for range 1000 {
		id := uuid.NewString()
		payload, err := json.Marshal(datastore.Namespace{UID: id, Name: "unrequested"})
		require.NoError(t, err)
		require.NoError(t, manager.eventLocked(datastore.ResourceWatchEvent{
			Kind: "Namespace", Type: datastore.ResourceWatchAdded, Payload: payload,
		}))
		payload, err = json.Marshal(datastore.RBACTierProgress{TierID: id, Revision: 7})
		require.NoError(t, err)
		require.NoError(t, manager.eventLocked(datastore.ResourceWatchEvent{
			Kind: "RBACTier", Type: datastore.ResourceWatchModified, Payload: payload,
		}))
	}
	require.Len(t, manager.work, 2, "unrequested source events must not populate serving state")
	require.Equal(t, 2, manager.pending, "only the root and fenced unidentified scan may occupy the bounded queue")
	require.Positive(t, manager.work[inFlight].generation, "unidentified in-flight snapshots must still be fenced")
	tier := uuid.NewString()
	require.NoError(t, manager.RequestTier(tier), "ordinary demand must still start a complete authoritative scan")
	require.Equal(t, 3, manager.pending)
	require.True(t, manager.work[tier].pending)
}

func TestManagerSharedRootPrecedesOlderNamespaceProofBacklog(t *testing.T) {
	for _, replacing := range []bool{false, true} {
		t.Run(map[bool]string{false: "proof", true: "replacement"}[replacing], func(t *testing.T) {
			now := time.Now()
			root := &tierWork{installed: true, checkAt: now.Add(-time.Second)}
			if replacing {
				root.installed, root.pending = false, true
			}
			manager := &Manager{
				deps: ManagerDeps{
					Clock: apiruntime.SystemClock{}, Workers: 4,
					CheckOrigin: func(context.Context, string, SnapshotOrigin) error { return nil },
				},
				continuous: true, probeNext: true,
				work: map[string]*tierWork{ClusterTierID: root},
			}
			if replacing {
				manager.pending = 1
			}
			for range 100 {
				manager.work[uuid.NewString()] = &tierWork{installed: true, checkAt: now.Add(-2 * time.Second)}
			}
			tier, work, _, probe := manager.take()
			require.Equal(t, ClusterTierID, tier,
				"every Namespace depends on the shared root; older local proofs must not consume its reserved capacity")
			require.Same(t, root, work)
			require.Equal(t, !replacing, probe)

			root.pending, root.running, root.installed = false, false, true
			root.checkAt = now.Add(time.Hour)
			tier, work, _, probe = manager.take()
			require.NotNil(t, work, "local proofs must still progress when the root is not due")
			require.NotEqual(t, ClusterTierID, tier)
			require.True(t, probe)
		})
	}
}

func TestManagerConcurrentProofBatchesShareTheBoundAndReserveRoot(t *testing.T) {
	now := time.Now()
	entered, release := make(chan int, 3), make(chan struct{})
	var once sync.Once
	t.Cleanup(func() { once.Do(func() { close(release) }) })
	manager := &Manager{
		deps: ManagerDeps{Clock: apiruntime.SystemClock{}, Workers: 4, MaxStaleness: 5 * time.Second,
			CheckOrigin:   func(context.Context, string, SnapshotOrigin) error { return nil },
			CheckProgress: func(time.Time) error { return nil },
			RecordOrigin:  func(string, ProjectionIdentity, time.Time) error { return nil },
			CheckOrigins: func(_ context.Context, checks []OriginCheck) []error {
				entered <- len(checks)
				<-release
				return make([]error, len(checks))
			}},
		continuous: true, wake: make(chan struct{}, 4), changed: make(chan struct{}),
		work: map[string]*tierWork{ClusterTierID: {installed: true, checkAt: now.Add(time.Hour)}},
	}
	for range 1000 {
		manager.work[uuid.NewString()] = &tierWork{installed: true, checkAt: now.Add(-time.Second)}
	}
	var workers sync.WaitGroup
	total := 0
	for range 3 {
		tier, work, generation, probe := manager.take()
		require.NotNil(t, work, "a batch must not count each captured tier as another occupied worker")
		require.NotEqual(t, ClusterTierID, tier)
		require.True(t, probe)
		workers.Go(func() { manager.probeBatch(t.Context(), tier, work, generation) })
		select {
		case count := <-entered:
			total += count
		case <-time.After(time.Second):
			t.Fatal("bounded workers did not enter their proof batches")
		}
	}
	require.LessOrEqual(t, total, MaxOriginChecks-MaxOriginChecks/manager.deps.Workers,
		"workers must leave one share of the ref bound for concurrent recovery/validation")
	require.Greater(t, total, 3, "available workers must still amortize signed proof RPCs")
	manager.mu.Lock()
	manager.work[ClusterTierID].checkAt = now.Add(-time.Second)
	manager.mu.Unlock()
	tier, _, _, probe := manager.take()
	require.Equal(t, ClusterTierID, tier)
	require.True(t, probe)
	once.Do(func() { close(release) })
	workers.Wait()
}

func TestManagerBatchesDueProofsWithoutRenewingChangedOrFailedTiers(t *testing.T) {
	now := time.Now()
	recorded := map[string]time.Time{}
	manager, err := NewManager(ManagerDeps{
		Journal:      struct{ datastore.ResourceWatchJournal }{},
		ReadSnapshot: func(context.Context, string) (Snapshot, error) { return Snapshot{}, ErrUnavailable },
		Prepare: func(context.Context, auth.AuthorizationTier) (PreparedPublication, error) {
			return nil, ErrUnavailable
		},
		Invalidate:    func(string) {},
		CheckProgress: func(time.Time) error { return nil },
		CheckOrigin:   func(context.Context, string, SnapshotOrigin) error { return nil },
		RecordOrigin: func(tier string, _ ProjectionIdentity, at time.Time) error {
			recorded[tier] = at
			return nil
		},
	})
	require.NoError(t, err)
	manager.continuous, manager.pending = true, 0
	root := manager.work[ClusterTierID]
	root.pending, root.installed, root.checkAt = false, true, now.Add(time.Hour)
	for i := range 40 {
		manager.work[uuid.NewString()] = &tierWork{installed: true, generation: 1,
			checkAt: now.Add(time.Duration(i-40) * time.Second), originAt: now.Add(-time.Second)}
	}

	var checked []OriginCheck
	var completed time.Time
	manager.deps.CheckOrigins = func(_ context.Context, checks []OriginCheck) []error {
		checked = checks
		require.LessOrEqual(t, len(checks), MaxOriginChecks/2)
		require.GreaterOrEqual(t, len(checks), 4)
		root.checkAt = now.Add(-time.Second)
		tier, work, _, probe := manager.take()
		require.Equal(t, ClusterTierID, tier, "a full local proof batch must leave the shared root runnable")
		require.Same(t, root, work)
		require.True(t, probe)
		root.running = false
		root.checkAt = now.Add(time.Hour)
		manager.work[checks[0].Tier].generation++
		results := make([]error, len(checks))
		results[1], results[2] = errOriginAdvanced, ErrUnavailable
		completed = time.Now()
		return results
	}
	tier, work, generation, probe := manager.take()
	require.True(t, probe)
	manager.probeBatch(t.Context(), tier, work, generation)
	require.Len(t, recorded, len(checked)-3)
	for i, check := range checked {
		current := manager.work[check.Tier]
		require.False(t, current.running)
		if i < 3 {
			require.NotContains(t, recorded, check.Tier)
			if i > 0 {
				require.True(t, current.pending, "changed refs and failed proofs require authoritative recovery")
			}
		} else {
			require.False(t, recorded[check.Tier].After(completed), "batch I/O must consume the original freshness budget")
			require.Equal(t, recorded[check.Tier].Add(manager.deps.MaxStaleness/2), current.checkAt)
		}
	}
}
func TestManagerProofBatchUsesCapacityWhileOtherWorkersRebuild(t *testing.T) {
	now := time.Now()
	for _, rebuilds := range []int{1, 2} {
		t.Run(fmt.Sprint(rebuilds), func(t *testing.T) {
			checked := 0
			manager := &Manager{
				deps: ManagerDeps{Clock: apiruntime.SystemClock{}, Workers: 4, MaxStaleness: 5 * time.Second,
					CheckOrigin:   func(context.Context, string, SnapshotOrigin) error { return nil },
					CheckProgress: func(time.Time) error { return nil },
					RecordOrigin:  func(string, ProjectionIdentity, time.Time) error { return nil },
					CheckOrigins: func(_ context.Context, checks []OriginCheck) []error {
						checked = len(checks)
						return make([]error, len(checks))
					}},
				continuous: true, wake: make(chan struct{}, 4), changed: make(chan struct{}),
				rebuilding: rebuilds, localRunning: rebuilds,
				work: map[string]*tierWork{ClusterTierID: {installed: true, checkAt: now.Add(time.Hour)}},
			}
			for range rebuilds {
				manager.work[uuid.NewString()] = &tierWork{running: true}
			}
			otherChecks := 0
			if rebuilds == 1 {
				otherChecks = MaxOriginChecks / manager.deps.Workers
				manager.localRunning++
				for range otherChecks {
					manager.work[uuid.NewString()] = &tierWork{running: true, installed: true}
				}
			}
			for range 1000 {
				manager.work[uuid.NewString()] = &tierWork{installed: true, checkAt: now.Add(-time.Second)}
			}
			tier, work, generation, probe := manager.take()
			require.NotNil(t, work)
			require.True(t, probe)
			manager.probeBatch(t.Context(), tier, work, generation)
			require.Greater(t, checked, MaxOriginChecks/manager.deps.Workers,
				"rebuilds cannot use their full batch shares; due Namespace proofs must use the released capacity")
			require.LessOrEqual(t, checked+rebuilds+otherChecks, MaxOriginChecks-MaxOriginChecks/manager.deps.Workers,
				"active rebuild checks and root/recovery capacity must remain reserved")
			expectedWorkers := rebuilds
			if otherChecks != 0 {
				expectedWorkers++
			}
			require.Equal(t, expectedWorkers, manager.localRunning)
			require.Zero(t, manager.pending)
		})
	}
}

func TestManagerReservesSharedRootWorkerAgainstNamespaceProbes(t *testing.T) {
	for _, rebuild := range []bool{false, true} {
		t.Run(map[bool]string{false: "proof", true: "rebuild"}[rebuild], func(t *testing.T) {
			now := time.Now()
			root := &tierWork{installed: true, checkAt: now.Add(time.Hour)}
			manager := &Manager{
				deps: ManagerDeps{Clock: apiruntime.SystemClock{}, Workers: 4,
					CheckOrigin: func(context.Context, string, SnapshotOrigin) error { return nil }},
				continuous: true,
				work:       map[string]*tierWork{ClusterTierID: root},
			}
			for range 20 {
				manager.work[uuid.NewString()] = &tierWork{installed: true, checkAt: now.Add(-time.Second)}
			}
			for range 3 {
				tier, work, _, probe := manager.take()
				require.NotNil(t, work)
				require.NotEqual(t, ClusterTierID, tier)
				require.True(t, probe)
			}
			_, work, _, _ := manager.take()
			require.Nil(t, work, "local proofs must not consume the worker reserved for shared root authority")
			if rebuild {
				root.installed, root.pending = false, true
				manager.pending++
			} else {
				root.checkAt = now.Add(-time.Second)
			}
			tier, work, _, probe := manager.take()
			require.Equal(t, ClusterTierID, tier)
			require.Same(t, root, work)
			require.Equal(t, !rebuild, probe, "root work must run while all local proof slots are occupied")
		})
	}
}

func TestManagerReservesNamespaceProofWorkerAgainstRebuilds(t *testing.T) {
	for _, workers := range []int{3, 4} {
		t.Run(fmt.Sprintf("workers-%d", workers), func(t *testing.T) {
			now := time.Now()
			root := &tierWork{installed: true, checkAt: now.Add(time.Hour)}
			local := &tierWork{installed: true, checkAt: now.Add(-time.Second)}
			manager := &Manager{
				deps: ManagerDeps{Clock: apiruntime.SystemClock{}, Workers: workers,
					CheckOrigin: func(context.Context, string, SnapshotOrigin) error { return nil }},
				continuous: true, rebuilding: workers - 2, localRunning: workers - 2, pending: 1,
				work: map[string]*tierWork{
					ClusterTierID:    root,
					uuid.NewString(): local,
					uuid.NewString(): {pending: true},
				},
			}
			for range workers - 2 {
				manager.work[uuid.NewString()] = &tierWork{running: true}
			}
			tier, work, _, probe := manager.take()
			require.NotEqual(t, ClusterTierID, tier)
			require.Same(t, local, work,
				"another rebuild must not consume the last worker allowed to verify Namespace origins")
			require.True(t, probe)
			require.Equal(t, workers-2, manager.rebuilding)
			require.Equal(t, workers-1, manager.localRunning)

			_, work, _, _ = manager.take()
			require.Nil(t, work, "the remaining worker must still be reserved for shared root authority")
			root.installed, root.pending = false, true
			manager.pending++
			tier, work, _, probe = manager.take()
			require.Equal(t, ClusterTierID, tier)
			require.Same(t, root, work)
			require.False(t, probe)
		})
	}
}
