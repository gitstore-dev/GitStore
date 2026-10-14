// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package scylla

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

const sagaTestRepositoryID = "11111111-1111-1111-1111-111111111111"

func TestCatalogFenceScopeMatchesOnlyActiveOwner(t *testing.T) {
	first, second := &scyllaDatastore{}, &scyllaDatastore{}
	var scope *catalogFenceScope
	require.False(t, scope.isActiveFor(first, "namespace"))
	scope = &catalogFenceScope{store: first, namespace: "namespace"}
	require.False(t, scope.isActiveFor(first, "namespace"))
	scope.active.Store(true)
	require.True(t, scope.isActiveFor(first, "namespace"))
	require.False(t, scope.isActiveFor(second, "namespace"))
	require.False(t, scope.isActiveFor(first, "other-namespace"))
	scope.active.Store(false)
	require.False(t, scope.isActiveFor(first, "namespace"))
}

func TestCatalogFenceRepositoryChecksCoalesceAndExpire(t *testing.T) {
	scope := &catalogFenceScope{}
	scope.active.Store(true)
	var calls atomic.Int64
	started, release := make(chan struct{}), make(chan struct{})
	check := func(context.Context) error {
		if calls.Add(1) == 1 {
			close(started)
		}
		<-release
		return nil
	}
	errs := make([]error, 16)
	var workers sync.WaitGroup
	for i := range errs {
		workers.Go(func() { errs[i] = scope.checkRepository(t.Context(), "repo", check) })
	}
	<-started
	close(release)
	workers.Wait()
	for _, err := range errs {
		require.NoError(t, err)
	}
	require.EqualValues(t, 1, calls.Load())
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	require.ErrorIs(t, scope.checkRepository(canceled, "repo", check), context.Canceled,
		"a checked parent cannot bypass the caller's cancellation")
	scope.active.Store(false)
	require.NoError(t, scope.checkRepository(t.Context(), "repo", check))
	require.EqualValues(t, 2, calls.Load(), "a retained scope cannot reuse a released reservation's proof")
}

func TestCatalogFenceRepositoryChecksDoNotRetainFailures(t *testing.T) {
	scope := &catalogFenceScope{}
	scope.active.Store(true)
	sentinel := errors.New("parent read failed")
	require.ErrorIs(t, scope.checkRepository(t.Context(), "repo", func(context.Context) error {
		return sentinel
	}), sentinel)
	require.Empty(t, scope.parents)
	require.NoError(t, scope.checkRepository(t.Context(), "repo", func(context.Context) error { return nil }))
	require.Len(t, scope.parents, 1)
}

func TestCatalogFenceRepositoryCheckCancellationDoesNotPoisonPeers(t *testing.T) {
	scope := &catalogFenceScope{}
	scope.active.Store(true)
	leaderCtx, cancel := context.WithCancel(t.Context())
	defer cancel()
	started, leaderDone, peerDone := make(chan struct{}), make(chan error, 1), make(chan error, 1)
	go func() {
		leaderDone <- scope.checkRepository(leaderCtx, "repo", func(ctx context.Context) error {
			close(started)
			<-ctx.Done()
			return ctx.Err()
		})
	}()
	<-started
	type callerKey struct{}
	peerCtx := context.WithValue(t.Context(), callerKey{}, "peer")
	go func() {
		peerDone <- scope.checkRepository(peerCtx, "repo", func(ctx context.Context) error {
			assert.Equal(t, "peer", ctx.Value(callerKey{}))
			return nil
		})
	}()
	cancel()
	require.ErrorIs(t, <-leaderDone, context.Canceled)
	require.NoError(t, <-peerDone)
	require.Len(t, scope.parents, 1, "only the peer's successful check may be retained")
}

func TestCatalogFenceRepositoryCheckWaitersKeepTheirOwnDeadline(t *testing.T) {
	scope := &catalogFenceScope{}
	scope.active.Store(true)
	started, release, done := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	defer func() {
		close(release)
		require.NoError(t, <-done)
	}()
	go func() {
		done <- scope.checkRepository(t.Context(), "repo", func(context.Context) error {
			close(started)
			<-release
			return nil
		})
	}()
	<-started
	peerCtx, cancel := context.WithTimeout(t.Context(), 10*time.Millisecond)
	defer cancel()
	require.ErrorIs(t, scope.checkRepository(peerCtx, "repo", func(context.Context) error {
		t.Error("a pending successful lookup must not duplicate work")
		return nil
	}), context.DeadlineExceeded)
	select {
	case <-done:
		t.Fatal("one canceled waiter released the active parent check")
	default:
	}
}

func TestCatalogFenceRepositoryChecksHaveAFixedBound(t *testing.T) {
	scope := &catalogFenceScope{}
	scope.active.Store(true)
	calls := 0
	check := func(context.Context) error { calls++; return nil }
	for range 2 {
		for i := range catalogFenceMaxCheckedRepositories + 2 {
			require.NoError(t, scope.checkRepository(t.Context(), fmt.Sprint(i), check))
		}
	}
	require.Len(t, scope.parents, catalogFenceMaxCheckedRepositories)
	require.Equal(t, catalogFenceMaxCheckedRepositories+4, calls,
		"parents beyond the fixed bound retain normal checks, not unchecked success")
}

func TestFileMutationBatchRejectsUnboundedOrAmbiguousCallbacks(t *testing.T) {
	store := &scyllaDatastore{}
	for _, names := range [][]string{nil, {}, {""}, {"a", "a"}, {"a", "b", "c", "d", "e"}} {
		require.ErrorIs(t, store.WithFileMutationBatch(t.Context(), "fixture", "repo", names,
			func(context.Context) error { t.Fatal("invalid File callback executed"); return nil }), datastore.ErrInvalidArgument)
	}
	require.ErrorIs(t, store.WithFileMutationBatch(t.Context(), "fixture", "", []string{"a"},
		func(context.Context) error { return nil }), datastore.ErrInvalidArgument)
	require.ErrorIs(t, store.WithFileMutationBatch(t.Context(), "fixture", "repo", []string{"a"}, nil), datastore.ErrInvalidArgument)
}

func QueuedNamespaceCatalogWritesForTest(store datastore.Datastore, namespaceUID string) int {
	s := store.(*scyllaDatastore)
	s.catalogQueue.mu.Lock()
	defer s.catalogQueue.mu.Unlock()
	entry := s.catalogQueue.entries[namespaceUID]
	if entry == nil {
		return 0
	}
	return len(entry.waiters)
}

func CatalogParentsActiveForTest(store datastore.Datastore, ctx context.Context, namespace, repositoryID string) error {
	return store.(*scyllaDatastore).catalogParentsActive(ctx, namespace, repositoryID)
}

func TestFileMutationCohortsPreserveBoundsAndFIFOBarriers(t *testing.T) {
	for _, tc := range []struct {
		name string
		next *fileMutationRequest
		want int
	}{
		{"distinct callbacks", &fileMutationRequest{repositoryID: "repo-b", names: []string{"b", "c"}}, 4},
		{"same repository with disjoint names", &fileMutationRequest{repositoryID: "repo-a", names: []string{"b"}}, 4},
		{"same repository with ordered overlapping name", &fileMutationRequest{repositoryID: "repo-a", names: []string{"a"}}, 4},
		{"different repository with ordered overlapping name", &fileMutationRequest{repositoryID: "repo-b", names: []string{"a"}}, 4},
		{"lifecycle barrier", nil, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var queue namespaceCatalogQueue
			first := &fileMutationRequest{repositoryID: "repo-a", names: []string{"a"}}
			var finishes []func()
			requests := []*fileMutationRequest{first, tc.next,
				{repositoryID: "repo-c", names: []string{"d"}},
				{repositoryID: "repo-d", names: []string{"e"}},
				{repositoryID: "repo-e", names: []string{"f"}},
			}
			var channels []chan struct{}
			for _, request := range requests {
				ready, finish, err := queue.enqueue(t.Context(), "uid", request)
				require.NoError(t, err)
				finishes = append(finishes, finish)
				channels = append(channels, ready)
			}
			group := queue.selectFiles("uid", first)
			require.Len(t, group, tc.want)
			require.Empty(t, queue.selectFiles("uid", first), "selected followers cannot execute another cohort")
			for i, ready := range channels {
				select {
				case <-ready:
					require.Less(t, i, tc.want, "no callback may overtake a barrier or the four-callback bound")
				default:
					require.GreaterOrEqual(t, i, tc.want)
				}
			}
			for i := len(finishes) - 1; i >= 0; i-- {
				finishes[i]()
			}
			require.Zero(t, queue.count)
			require.Empty(t, queue.entries)
		})
	}
}

func TestFileMutationCohortOrdersOverlapsWithoutBlockingDisjointPeers(t *testing.T) {
	firstStarted, peerStarted := make(chan struct{}), make(chan struct{})
	firstRelease, secondRelease := make(chan struct{}), make(chan struct{})
	secondStarted := make(chan struct{})
	var firstDone, secondDone atomic.Bool
	group := []*fileMutationRequest{
		{ctx: t.Context(), names: []string{"a"}, operation: func(context.Context) error {
			close(firstStarted)
			<-firstRelease
			firstDone.Store(true)
			return nil
		}},
		{ctx: t.Context(), names: []string{"a", "b"}, operation: func(context.Context) error {
			assert.True(t, firstDone.Load(), "overlapping mutations must retain queue order")
			close(secondStarted)
			<-secondRelease
			secondDone.Store(true)
			return nil
		}},
		{ctx: t.Context(), names: []string{"c"}, operation: func(context.Context) error {
			close(peerStarted)
			return nil
		}},
		{ctx: t.Context(), names: []string{"b"}, operation: func(context.Context) error {
			assert.True(t, secondDone.Load(), "transitive overlapping mutations must retain queue order")
			return nil
		}},
	}
	var firstOnce, secondOnce sync.Once
	releaseFirst := func() { firstOnce.Do(func() { close(firstRelease) }) }
	releaseSecond := func() { secondOnce.Do(func() { close(secondRelease) }) }
	done := make(chan struct{})
	go func() {
		defer close(done)
		runFileMutationGroup(group, func(_ context.Context, apply func() error) error { return apply() }, nil)
	}()
	t.Cleanup(func() {
		releaseFirst()
		releaseSecond()
		<-done
	})
	for _, started := range []chan struct{}{firstStarted, peerStarted} {
		select {
		case <-started:
		case <-time.After(time.Second):
			t.Fatal("disjoint callbacks did not execute while the first callback was blocked")
		}
	}
	select {
	case <-secondStarted:
		t.Fatal("overlapping callback started before its predecessor completed")
	default:
	}
	releaseFirst()
	select {
	case <-secondStarted:
	case <-time.After(time.Second):
		t.Fatal("overlapping callback did not resume after its predecessor completed")
	}
	releaseSecond()
	<-done
	for _, request := range group {
		require.NoError(t, request.err)
	}
}

func TestFileMutationCohortOverlapsPropagateRepairButNotOrdinaryFailure(t *testing.T) {
	sentinel := errors.New("ordinary callback failure")
	repair := datastore.NewRepairRequiredError(datastore.MutationStep{Operation: "update_file"}, sentinel, nil)
	for _, tc := range []struct {
		name   string
		err    error
		writes int64
	}{
		{"ordinary failure permits next callback", sentinel, 1},
		{"repair blocks overlapping callback", repair, 0},
		{"cancellation permits live callback", context.Canceled, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var writes atomic.Int64
			group := []*fileMutationRequest{
				{ctx: t.Context(), names: []string{"same"}, operation: func(context.Context) error { return tc.err }},
				{ctx: t.Context(), names: []string{"same"}, operation: func(context.Context) error {
					writes.Add(1)
					return nil
				}},
			}
			runFileMutationGroup(group, func(_ context.Context, apply func() error) error { return apply() }, nil)
			require.Equal(t, tc.writes, writes.Load())
			if errors.Is(tc.err, datastore.ErrRepairRequired) {
				require.ErrorIs(t, group[1].err, datastore.ErrRepairRequired)
			} else {
				require.NoError(t, group[1].err)
			}
		})
	}
}

func TestFileMutationCohortCanceledOverlapDoesNotCancelLiveSuccessor(t *testing.T) {
	firstStarted, firstRelease := make(chan struct{}), make(chan struct{})
	secondCtx, cancelSecond := context.WithCancel(t.Context())
	defer cancelSecond()
	var writes atomic.Int64
	group := []*fileMutationRequest{
		{ctx: t.Context(), names: []string{"same"}, operation: func(context.Context) error {
			close(firstStarted)
			<-firstRelease
			return nil
		}},
		{ctx: secondCtx, names: []string{"same"}, operation: func(context.Context) error {
			t.Error("canceled overlapping callback executed")
			return nil
		}},
		{ctx: t.Context(), names: []string{"same"}, operation: func(context.Context) error {
			writes.Add(1)
			return nil
		}},
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		runFileMutationGroup(group, func(_ context.Context, apply func() error) error { return apply() }, nil)
	}()
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(firstRelease) }) }
	t.Cleanup(func() { release(); <-done })
	select {
	case <-firstStarted:
	case <-time.After(time.Second):
		t.Fatal("the first callback did not start")
	}
	cancelSecond()
	release()
	<-done
	require.NoError(t, group[0].err)
	require.ErrorIs(t, group[1].err, context.Canceled)
	require.NoError(t, group[2].err)
	require.EqualValues(t, 1, writes.Load())
}

func TestFileMutationCohortKeepsIndependentContextsAndErrors(t *testing.T) {
	type identityKey struct{}
	sentinel := errors.New("independent mutation failure")
	firstCtx, cancel := context.WithCancel(context.WithValue(t.Context(), identityKey{}, "first"))
	peerCtx := context.WithValue(t.Context(), identityKey{}, "peer")
	defer cancel()
	var writes atomic.Int64
	group := []*fileMutationRequest{
		{ctx: firstCtx, operation: func(context.Context) error { t.Error("canceled callback executed"); return nil }},
		{ctx: peerCtx, operation: func(ctx context.Context) error {
			require.Equal(t, "peer", ctx.Value(identityKey{}), "the leader's actor/context cannot replace a peer's")
			writes.Add(1)
			return sentinel
		}},
	}
	releases := 0
	runFileMutationGroup(group, func(ctx context.Context, apply func() error) error {
		cancel()
		require.NoError(t, ctx.Err(), "canceling one caller cannot cancel live peers")
		return executeNamespaceRepositoryReservation(ctx, func() error { return nil },
			func(releaseCtx context.Context) error {
				require.NoError(t, releaseCtx.Err())
				releases++
				return nil
			}, apply, datastore.MutationStep{}, "cohort completed")
	}, nil)
	require.ErrorIs(t, group[0].err, context.Canceled)
	require.NotErrorIs(t, group[0].err, sentinel)
	require.ErrorIs(t, group[1].err, sentinel)
	require.NotErrorIs(t, group[1].err, context.Canceled)
	require.EqualValues(t, 1, writes.Load())
	require.Equal(t, 1, releases)
}

func TestFileMutationCohortRepairAndReserveFailuresReachEveryCaller(t *testing.T) {
	sentinel := errors.New("fence failure")
	repair := datastore.NewRepairRequiredError(datastore.MutationStep{Operation: "create_file"}, sentinel, nil)
	for _, tc := range []struct {
		name                             string
		reserve, mutation, release, want error
		writes, releases                 int64
	}{
		{"reserve failure", sentinel, nil, nil, sentinel, 0, 0},
		{"ordinary mutation failure", nil, sentinel, nil, nil, 2, 1},
		{"repair retains fence", nil, repair, nil, datastore.ErrRepairRequired, 2, 0},
		{"release failure", nil, nil, sentinel, datastore.ErrRepairRequired, 2, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var writes, releases atomic.Int64
			group := []*fileMutationRequest{
				{ctx: t.Context(), operation: func(context.Context) error { writes.Add(1); return tc.mutation }},
				{ctx: t.Context(), operation: func(context.Context) error { writes.Add(1); return nil }},
			}
			runFileMutationGroup(group, func(ctx context.Context, apply func() error) error {
				return executeNamespaceRepositoryReservation(ctx, func() error { return tc.reserve },
					func(context.Context) error { releases.Add(1); return tc.release },
					apply, datastore.MutationStep{Operation: "catalog_lifecycle"}, "cohort completed")
			}, nil)
			require.Equal(t, tc.writes, writes.Load())
			require.Equal(t, tc.releases, releases.Load())
			if tc.want != nil {
				for _, request := range group {
					require.ErrorIs(t, request.err, tc.want)
				}
			} else {
				require.ErrorIs(t, group[0].err, sentinel)
				require.NoError(t, group[1].err, "one ordinary callback error cannot reject unrelated writes")
			}
		})
	}
}

func TestFileMutationCohortCancelsReserveOnlyAfterAllCallersCancel(t *testing.T) {
	firstCtx, cancelFirst := context.WithCancel(t.Context())
	peerCtx, cancelPeer := context.WithCancel(t.Context())
	defer cancelFirst()
	defer cancelPeer()
	group := []*fileMutationRequest{{ctx: firstCtx}, {ctx: peerCtx}}
	runFileMutationGroup(group, func(ctx context.Context, _ func() error) error {
		cancelFirst()
		require.NoError(t, ctx.Err())
		cancelPeer()
		select {
		case <-ctx.Done():
		case <-time.After(time.Second):
			t.Fatal("all-canceled cohort retained a live reservation context")
		}
		return ctx.Err()
	}, nil)
	for _, request := range group {
		require.ErrorIs(t, request.err, context.Canceled)
	}
}

func TestQueuedFileMutationDrainsSelectedOperationsOnCancellation(t *testing.T) {
	store := &scyllaDatastore{}
	release, err := store.catalogQueue.acquire(t.Context(), "uid")
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	active, finish := make(chan struct{}), make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- store.runQueuedFileMutation(ctx, "uid", "repo", []string{"file"}, func(context.Context) error {
			close(active)
			<-finish
			return context.Canceled
		}, func(group []*fileMutationRequest) []*fileMutationRequest {
			return runFileMutationGroup(group, func(_ context.Context, apply func() error) error { return apply() }, nil)
		})
	}()
	require.Eventually(t, func() bool {
		store.catalogQueue.mu.Lock()
		defer store.catalogQueue.mu.Unlock()
		return store.catalogQueue.count == 2
	}, time.Second, time.Millisecond)
	release()
	<-active
	cancel()
	select {
	case <-done:
		t.Fatal("selected mutation returned before its active operation drained")
	case <-time.After(20 * time.Millisecond):
	}
	close(finish)
	require.ErrorIs(t, <-done, context.Canceled)
	require.Zero(t, store.catalogQueue.count)
	require.Empty(t, store.catalogQueue.entries)
}

func TestFileMutationCohortExtendsAfterGrantWithoutCrossingLifecycleBarrier(t *testing.T) {
	var queue namespaceCatalogQueue
	var firstDone atomic.Bool
	first := &fileMutationRequest{ctx: t.Context(), names: []string{"same"}, operation: func(context.Context) error {
		firstDone.Store(true)
		return nil
	}}
	_, _, err := queue.enqueue(t.Context(), "uid", first)
	require.NoError(t, err)
	initial := queue.selectFiles("uid", first)
	require.Len(t, initial, 1)
	late := &fileMutationRequest{ctx: t.Context(), names: []string{"same"}, operation: func(context.Context) error {
		assert.True(t, firstDone.Load(), "late overlapping callbacks retain their place after the earlier writer")
		return nil
	}}
	_, _, err = queue.enqueue(t.Context(), "uid", late)
	require.NoError(t, err)
	barrier, finishBarrier, err := queue.enqueue(t.Context(), "uid", nil)
	require.NoError(t, err)
	tail := &fileMutationRequest{ctx: t.Context(), names: []string{"tail"}}
	_, finishTail, err := queue.enqueue(t.Context(), "uid", tail)
	require.NoError(t, err)
	group := runFileMutationGroup(initial, func(_ context.Context, apply func() error) error {
		require.False(t, firstDone.Load())
		return apply()
	}, func(group []*fileMutationRequest) []*fileMutationRequest { return queue.extendFiles("uid", group) })
	require.Len(t, group, 2)
	require.Same(t, late, group[1])
	require.False(t, tail.selected, "selection must stop at the first lifecycle barrier")
	for i := len(group) - 1; i >= 0; i-- {
		require.NoError(t, group[i].err)
		group[i].finish()
	}
	select {
	case <-barrier:
	default:
		t.Fatal("the barrier did not resume after every selected callback drained")
	}
	finishBarrier()
	finishTail()
	require.Zero(t, queue.count)
	require.Empty(t, queue.entries)
}

func TestFileMutationCohortExtensionRetainsFourCallbackBound(t *testing.T) {
	var queue namespaceCatalogQueue
	first := &fileMutationRequest{ctx: t.Context(), operation: func(context.Context) error { return nil }}
	_, _, err := queue.enqueue(t.Context(), "uid", first)
	require.NoError(t, err)
	initial := queue.selectFiles("uid", first)
	for range 5 {
		request := &fileMutationRequest{ctx: t.Context(), operation: func(context.Context) error { return nil }}
		_, _, err := queue.enqueue(t.Context(), "uid", request)
		require.NoError(t, err)
	}
	group := runFileMutationGroup(initial, func(_ context.Context, apply func() error) error { return apply() },
		func(group []*fileMutationRequest) []*fileMutationRequest { return queue.extendFiles("uid", group) })
	require.Len(t, group, 4)
	for i := len(group) - 1; i >= 0; i-- {
		group[i].finish()
	}
	require.Equal(t, 2, queue.count)
}

func TestFileMutationCohortLatePeerSurvivesLeaderCancellationDuringExtension(t *testing.T) {
	firstCtx, cancel := context.WithCancel(t.Context())
	defer cancel()
	var fencedCtx context.Context
	first := &fileMutationRequest{ctx: firstCtx, operation: func(context.Context) error {
		t.Error("canceled leader executed")
		return nil
	}}
	peer := &fileMutationRequest{ctx: t.Context(), operation: func(context.Context) error {
		assert.NoError(t, fencedCtx.Err(), "canceling the leader cannot cancel a selected live peer")
		return nil
	}}
	group := runFileMutationGroup([]*fileMutationRequest{first}, func(ctx context.Context, apply func() error) error {
		fencedCtx = ctx
		return apply()
	}, func(group []*fileMutationRequest) []*fileMutationRequest {
		cancel()
		return append(group, peer)
	})
	require.Len(t, group, 2)
	require.ErrorIs(t, first.err, context.Canceled)
	require.NoError(t, peer.err)
}

func TestFileMutationCohortDoesNotExtendCanceledOrFailedReservation(t *testing.T) {
	sentinel := errors.New("reserve failed")
	for _, failed := range []bool{false, true} {
		t.Run(fmt.Sprintf("failed-%t", failed), func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			first := &fileMutationRequest{ctx: ctx}
			group := runFileMutationGroup([]*fileMutationRequest{first}, func(groupCtx context.Context, apply func() error) error {
				if failed {
					return sentinel
				}
				cancel()
				select {
				case <-groupCtx.Done():
				case <-time.After(time.Second):
					t.Fatal("canceled reservation context did not stop")
				}
				return apply()
			}, func(group []*fileMutationRequest) []*fileMutationRequest {
				t.Error("unconfirmed or fully canceled reservation selected additional callbacks")
				return group
			})
			require.Len(t, group, 1)
			if failed {
				require.ErrorIs(t, first.err, sentinel)
			} else {
				require.ErrorIs(t, first.err, context.Canceled)
			}
		})
	}
}

func TestFileMutationCohortLateCallbackSharesReleaseFailure(t *testing.T) {
	sentinel := errors.New("release failed")
	first := &fileMutationRequest{ctx: t.Context(), operation: func(context.Context) error { return nil }}
	late := &fileMutationRequest{ctx: t.Context(), operation: func(context.Context) error { return nil }}
	group := runFileMutationGroup([]*fileMutationRequest{first}, func(ctx context.Context, apply func() error) error {
		return executeNamespaceRepositoryReservation(ctx, func() error { return nil },
			func(context.Context) error { return sentinel }, apply, datastore.MutationStep{}, "cohort committed")
	}, func(group []*fileMutationRequest) []*fileMutationRequest { return append(group, late) })
	require.Len(t, group, 2)
	for _, request := range group {
		require.ErrorIs(t, request.err, datastore.ErrRepairRequired)
		var repair *datastore.RepairRequiredError
		require.ErrorAs(t, request.err, &repair)
		require.ErrorIs(t, repair.Compensation, sentinel)
	}
}

func TestQueuedFileMutationsCoalesceAndCancelWaitingRequests(t *testing.T) {
	store := &scyllaDatastore{}
	release, err := store.catalogQueue.acquire(t.Context(), "uid")
	require.NoError(t, err)
	var cohorts atomic.Int64
	var peak atomic.Int64
	execute := func(group []*fileMutationRequest) []*fileMutationRequest {
		cohorts.Add(1)
		peak.Store(max(peak.Load(), int64(len(group))))
		return runFileMutationGroup(group, func(_ context.Context, apply func() error) error { return apply() }, nil)
	}
	canceledCtx, cancel := context.WithCancel(t.Context())
	canceled := make(chan error, 1)
	go func() {
		canceled <- store.runQueuedFileMutation(canceledCtx, "uid", "canceled", []string{"canceled"},
			func(context.Context) error { t.Error("canceled waiting callback executed"); return nil }, execute)
	}()
	require.Eventually(t, func() bool {
		store.catalogQueue.mu.Lock()
		defer store.catalogQueue.mu.Unlock()
		return store.catalogQueue.count == 2
	}, time.Second, time.Millisecond)
	cancel()
	require.ErrorIs(t, <-canceled, context.Canceled)
	done := make(chan error, 8)
	for i := range 8 {
		go func() {
			name := fmt.Sprintf("file-%d", i)
			done <- store.runQueuedFileMutation(t.Context(), "uid", name, []string{name},
				func(context.Context) error { return nil }, execute)
		}()
	}
	require.Eventually(t, func() bool {
		store.catalogQueue.mu.Lock()
		defer store.catalogQueue.mu.Unlock()
		return store.catalogQueue.count == 9
	}, time.Second, time.Millisecond)
	release()
	for range 8 {
		require.NoError(t, <-done)
	}
	require.EqualValues(t, 2, cohorts.Load())
	require.EqualValues(t, 4, peak.Load())
	require.Zero(t, store.catalogQueue.count)
	require.Empty(t, store.catalogQueue.entries)
}

func TestCatalogFenceRejectsLifecycleWritesInsideAdmissionBatch(t *testing.T) {
	store := &scyllaDatastore{}
	scope := &catalogFenceScope{store: store, namespace: "fixture"}
	scope.active.Store(true)
	ctx := context.WithValue(t.Context(), catalogFenceScopeKey{}, scope)
	called := false
	err := store.withNamespaceCatalogFence(ctx, "fixture", true, func() error {
		called = true
		return nil
	})
	require.ErrorIs(t, err, datastore.ErrInvalidArgument)
	require.False(t, called)
}

func TestNamespaceCatalogQueueIsBoundedAndDropsIdleKeys(t *testing.T) {
	var queue namespaceCatalogQueue
	releases := make([]func(), 0, namespaceCatalogMaxQueuedWrites)
	for i := range namespaceCatalogMaxQueuedWrites {
		release, err := queue.acquire(t.Context(), string(rune(i)))
		require.NoError(t, err)
		releases = append(releases, release)
	}
	_, err := queue.acquire(t.Context(), "overflow")
	require.ErrorContains(t, err, "write queue exhausted")
	for _, release := range releases {
		release()
	}
	require.Zero(t, queue.count)
	require.Empty(t, queue.entries, "idle namespace keys must not accumulate in process memory")
}

func TestNamespaceCatalogQueueCancellationAndIndependentNamespaces(t *testing.T) {
	var queue namespaceCatalogQueue
	first, err := queue.acquire(t.Context(), "first-uid")
	require.NoError(t, err)
	other, err := queue.acquire(t.Context(), "second-uid")
	require.NoError(t, err)
	other()
	waitCtx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() {
		release, err := queue.acquire(waitCtx, "first-uid")
		if release != nil {
			release()
		}
		done <- err
	}()
	require.Eventually(t, func() bool {
		queue.mu.Lock()
		defer queue.mu.Unlock()
		return queue.count == 2
	}, time.Second, time.Millisecond)
	cancel()
	select {
	case err := <-done:
		require.ErrorIs(t, err, context.Canceled)
	case <-time.After(time.Second):
		t.Fatal("canceled Namespace writer remained queued")
	}
	first()
	require.Zero(t, queue.count)
	require.Empty(t, queue.entries)
	_, err = queue.acquire(waitCtx, "canceled-uid")
	require.ErrorIs(t, err, context.Canceled)
	require.Empty(t, queue.entries)
}

func TestNamespaceCatalogQueueServesWaitingWritersBeforeNewArrivals(t *testing.T) {
	var queue namespaceCatalogQueue
	release, err := queue.acquire(t.Context(), "namespace-uid")
	require.NoError(t, err)
	type acquired struct {
		index   int
		release func()
		err     error
	}
	ready := make(chan acquired, 2)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	for i := range 2 {
		go func() {
			release, err := queue.acquire(ctx, "namespace-uid")
			ready <- acquired{i, release, err}
		}()
		require.Eventually(t, func() bool {
			queue.mu.Lock()
			defer queue.mu.Unlock()
			return queue.count == i+2
		}, time.Second, time.Millisecond)
	}
	release()
	for i := range 2 {
		select {
		case writer := <-ready:
			require.NoError(t, writer.err)
			require.Equal(t, i, writer.index)
			writer.release()
		case <-time.After(time.Second):
			t.Fatal("waiting Namespace writer did not acquire its turn")
		}
	}
	require.Zero(t, queue.count)
	require.Empty(t, queue.entries)
}

func TestNamespaceCatalogRetryJitterPreservesBounds(t *testing.T) {
	for _, backoff := range []time.Duration{10, 20, 40, 80, 100} {
		backoff *= time.Millisecond
		seen := make(map[time.Duration]bool)
		for range 1000 {
			delay := namespaceCatalogRetryDelay(backoff)
			require.GreaterOrEqual(t, delay, time.Millisecond)
			require.Less(t, delay, backoff)
			seen[delay] = true
		}
		require.Greater(t, len(seen), 1, "contending replicas must not use one synchronized retry delay")
	}
}

// InjectRepositoryCompletionFailureForTest exposes the existing injector to
// external-package Scylla integration tests without a production API.
func InjectRepositoryCompletionFailureForTest(store datastore.Datastore, step string, after bool) func() {
	s := store.(*scyllaDatastore)
	previous := s.mutations.injector
	injector := newTestFailureInjector()
	point := failureBefore
	if after {
		point = failureAfter
	}
	injector.fail(step, point)
	s.mutations.injector = injector
	return func() { s.mutations.injector = previous }
}

func TestInfrastructureCompletionRetainsRowAcrossCleanupFailure(t *testing.T) {
	rowExists, visible, cleaned := true, true, false
	transient := errors.New("transient mapping cleanup failure")
	attempts := 0
	complete := func() error {
		return runInfrastructureDeletionCompletion(func() error {
			require.True(t, rowExists, "intent/finalizer must remain during cleanup")
			visible = false
			attempts++
			if attempts == 1 {
				return transient
			}
			cleaned = true
			return nil
		}, func() error {
			require.True(t, cleaned, "no authoritative deletion before successful cleanup")
			rowExists = false
			return nil
		}, func() error {
			visible = true
			return nil
		})
	}
	require.ErrorIs(t, complete(), transient)
	require.True(t, rowExists)
	require.True(t, visible)
	require.NoError(t, complete())
	require.False(t, rowExists)
}

func TestInfrastructureCompletionConflictRestoresVisibility(t *testing.T) {
	restored := false
	err := runInfrastructureDeletionCompletion(func() error { return nil },
		func() error { return datastore.ErrConflict },
		func() error { restored = true; return nil })
	require.ErrorIs(t, err, datastore.ErrConflict)
	require.True(t, restored)
}

func TestNamespaceRepositoryReservationRetainsFenceAfterRepairRequiredOperation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	operationErr := datastore.NewRepairRequiredError(
		datastore.MutationStep{Operation: "create_repository", Action: "converge_projection"},
		errors.New("projection failed"),
		errors.New("compensation failed"),
	)
	releases := 0

	err := executeNamespaceRepositoryReservation(
		ctx,
		func() error { return nil },
		func(releaseCtx context.Context) error {
			releases++
			assert.NoError(t, releaseCtx.Err())
			deadline, ok := releaseCtx.Deadline()
			assert.True(t, ok)
			assert.WithinDuration(t, time.Now().Add(namespaceRepositoryReleaseTimeout), deadline, time.Second)
			return nil
		},
		func() error {
			cancel()
			return operationErr
		},
		datastore.MutationStep{Operation: "create_repository"},
		"repository was created",
	)

	assert.Equal(t, 0, releases)
	assert.Equal(t, operationErr, err)
}

type namespaceReservationMetricStore struct {
	datastore.Datastore
	reserveErr error
	releaseErr error
	releases   int
}

func (s *namespaceReservationMetricStore) WithCatalogMutationBatch(ctx context.Context, _ string, operation func(context.Context) error) error {
	return executeNamespaceRepositoryReservation(ctx,
		func() error { return s.reserveErr },
		func(releaseCtx context.Context) error {
			s.releases++
			if err := releaseCtx.Err(); err != nil {
				return err
			}
			return s.releaseErr
		},
		func() error { return operation(ctx) },
		datastore.MutationStep{Operation: "catalog_lifecycle"},
		"catalog lifecycle mutation committed",
	)
}

func TestNamespaceRepositoryReservationPhaseMetricsPreserveOutcomes(t *testing.T) {
	sentinel := errors.New("fence phase failure")
	repair := datastore.NewRepairRequiredError(datastore.MutationStep{Operation: "create_file"}, sentinel, nil)
	for _, tc := range []struct {
		name                        string
		reserveErr, mutationErr     error
		releaseErr, wantErr         error
		cancel                      bool
		mutationCount, releaseCount uint64
	}{
		{name: "success", mutationCount: 1, releaseCount: 1},
		{name: "reserve failure", reserveErr: sentinel, wantErr: sentinel},
		{name: "mutation failure", mutationErr: sentinel, wantErr: sentinel, mutationCount: 1, releaseCount: 1},
		{name: "repair retains fence", mutationErr: repair, wantErr: repair, mutationCount: 1},
		{name: "release failure", releaseErr: sentinel, wantErr: datastore.ErrRepairRequired, mutationCount: 1, releaseCount: 1},
		{name: "cancelled mutation releases", cancel: true, mutationErr: context.Canceled, wantErr: context.Canceled, mutationCount: 1, releaseCount: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			registry := prometheus.NewRegistry()
			store := &namespaceReservationMetricStore{reserveErr: tc.reserveErr, releaseErr: tc.releaseErr}
			wrapped := datastore.NewInstrumentedDatastoreWithRegistry(store, "scylla", zap.NewNop(), registry)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			err := wrapped.(datastore.CatalogMutationBatcher).WithCatalogMutationBatch(ctx, "fixture", func(context.Context) error {
				if tc.cancel {
					cancel()
				}
				return tc.mutationErr
			})
			if tc.wantErr == nil {
				require.NoError(t, err)
			} else {
				require.ErrorIs(t, err, tc.wantErr)
			}
			require.EqualValues(t, tc.releaseCount, store.releases)
			families, err := registry.Gather()
			require.NoError(t, err)
			counts := map[string]uint64{}
			for _, family := range families {
				if family.GetName() != "gitstore_datastore_operation_duration_seconds" {
					continue
				}
				for _, metric := range family.GetMetric() {
					for _, label := range metric.GetLabel() {
						if label.GetName() == "operation" {
							counts[label.GetValue()] = metric.GetHistogram().GetSampleCount()
						}
					}
				}
			}
			require.EqualValues(t, 1, counts["NamespaceFenceReserve"])
			require.Equal(t, tc.mutationCount, counts["NamespaceFenceMutation"])
			require.Equal(t, tc.releaseCount, counts["NamespaceFenceRelease"])
			require.EqualValues(t, 1, counts["CatalogMutationBatch"])
		})
	}
}

type repositorySagaState struct {
	repository *datastore.Repository
	paths      map[repositoryPath]string
	reverse    *repositoryPath
	fail       map[string]error
	calls      []string
}

func newRepositorySagaState(source repositoryPath) *repositorySagaState {
	reverse := source
	return &repositorySagaState{
		repository: &datastore.Repository{
			UID:             sagaTestRepositoryID,
			Namespace:       source.namespace,
			Name:            source.name,
			Generation:      1,
			ResourceVersion: "1",
		},
		paths:   map[repositoryPath]string{source: sagaTestRepositoryID},
		reverse: &reverse,
		fail:    make(map[string]error),
	}
}

func (s *repositorySagaState) operations(injector failureInjector) repositorySagaOperations {
	return repositorySagaOperations{
		injector: injector,
		reserve: func(_ context.Context, path repositoryPath, owner string) error {
			s.calls = append(s.calls, repositorySagaReserveTarget)
			if err := s.takeFailure(repositorySagaReserveTarget); err != nil {
				return err
			}
			if existing, ok := s.paths[path]; ok && existing != owner {
				return datastore.ErrAlreadyExists
			}
			s.paths[path] = owner
			return nil
		},
		release: func(_ context.Context, path repositoryPath, owner string) error {
			if err := s.takeFailure("release_target"); err != nil {
				return err
			}
			if existing, ok := s.paths[path]; ok {
				if existing != owner {
					return datastore.ErrRepairRequired
				}
				delete(s.paths, path)
			}
			return nil
		},
		commit: func(
			_ context.Context,
			_ *datastore.Repository,
			source, target repositoryPath,
			version repositorySagaVersion,
		) (bool, error) {
			s.calls = append(s.calls, repositorySagaUpdateAuthoritative)
			if err := s.takeFailure(repositorySagaUpdateAuthoritative); err != nil {
				return false, err
			}
			if repositoryHasPath(s.repository, target) {
				return true, nil
			}
			if !repositoryHasPath(s.repository, source) {
				return false, datastore.ErrRepairRequired
			}
			s.repository.Namespace = target.namespace
			s.repository.Name = target.name
			if version == repositorySagaSystemVersion {
				datastore.AdvanceRepositorySystemVersion(s.repository)
			} else {
				datastore.AdvanceRepositorySpecVersion(s.repository)
			}
			return true, nil
		},
		updateReverse: func(_ context.Context, source, target repositoryPath, _ string) error {
			s.calls = append(s.calls, repositorySagaUpdateReverse)
			if err := s.takeFailure(repositorySagaUpdateReverse); err != nil {
				return err
			}
			if s.reverse != nil && *s.reverse != source && *s.reverse != target {
				return datastore.ErrRepairRequired
			}
			updated := target
			s.reverse = &updated
			return nil
		},
		cleanup: func(_ context.Context, path repositoryPath, owner string) error {
			s.calls = append(s.calls, repositorySagaCleanupOldPath)
			if err := s.takeFailure(repositorySagaCleanupOldPath); err != nil {
				return err
			}
			if existing, ok := s.paths[path]; ok {
				if existing != owner {
					return datastore.ErrRepairRequired
				}
				delete(s.paths, path)
			}
			return nil
		},
	}
}

func (s *repositorySagaState) takeFailure(step string) error {
	err := s.fail[step]
	delete(s.fail, step)
	return err
}

func (s *repositorySagaState) assertConverged(t *testing.T, target repositoryPath) {
	t.Helper()
	require.Len(t, s.paths, 1)
	assert.Equal(t, sagaTestRepositoryID, s.paths[target])
	require.NotNil(t, s.reverse)
	assert.Equal(t, target, *s.reverse)
	assert.True(t, repositoryHasPath(s.repository, target))
}

func TestRepositoryMappingSagaOrdersAllAwaitedSteps(t *testing.T) {
	source := repositoryPath{namespace: "source", name: "catalog"}
	target := repositoryPath{namespace: "source", name: "renamed"}
	state := newRepositorySagaState(source)

	err := executeRepositoryMappingSaga(
		context.Background(),
		state.operations(nil),
		state.repository,
		source,
		target,
		repositorySagaSpecVersion,
	)

	require.NoError(t, err)
	assert.Equal(t, []string{
		repositorySagaReserveTarget,
		repositorySagaUpdateAuthoritative,
		repositorySagaUpdateReverse,
		repositorySagaCleanupOldPath,
	}, state.calls)
	state.assertConverged(t, target)
}

func TestRepositoryMappingSagaRejectsTargetOwnedByAnotherRepository(t *testing.T) {
	source := repositoryPath{namespace: "source", name: "catalog"}
	target := repositoryPath{namespace: "source", name: "renamed"}
	state := newRepositorySagaState(source)
	state.paths[target] = "22222222-2222-2222-2222-222222222222"

	err := executeRepositoryMappingSaga(
		context.Background(),
		state.operations(nil),
		state.repository,
		source,
		target,
		repositorySagaSpecVersion,
	)

	require.ErrorIs(t, err, datastore.ErrAlreadyExists)
	assert.True(t, repositoryHasPath(state.repository, source))
	assert.Equal(t, "1", state.repository.ResourceVersion)
	assert.Equal(t, "22222222-2222-2222-2222-222222222222", state.paths[target])
}

func TestRepositoryMappingSagaFailureInjectionRetryConverges(t *testing.T) {
	source := repositoryPath{namespace: "source", name: "catalog"}
	target := repositoryPath{namespace: "source", name: "renamed"}
	for _, step := range []string{
		repositorySagaReserveTarget,
		repositorySagaUpdateAuthoritative,
		repositorySagaUpdateReverse,
		repositorySagaCleanupOldPath,
	} {
		for _, point := range []failurePoint{failureBefore, failureAfter} {
			t.Run(step+"_"+string(point), func(t *testing.T) {
				state := newRepositorySagaState(source)
				injector := newTestFailureInjector()
				injector.fail(step, point)

				err := executeRepositoryMappingSaga(
					context.Background(),
					state.operations(injector),
					state.repository,
					source,
					target,
					repositorySagaSpecVersion,
				)
				require.Error(t, err)

				require.NoError(t, executeRepositoryMappingSaga(
					context.Background(),
					state.operations(nil),
					state.repository,
					source,
					target,
					repositorySagaSpecVersion,
				))
				state.assertConverged(t, target)
				assert.Equal(t, int64(2), state.repository.Generation)
				assert.Equal(t, "2", state.repository.ResourceVersion)
			})
		}
	}
}

func TestRepositoryMappingSagaApplyFailuresRetryConverges(t *testing.T) {
	source := repositoryPath{namespace: "source", name: "catalog"}
	target := repositoryPath{namespace: "target", name: "catalog"}
	for _, step := range []string{
		repositorySagaReserveTarget,
		repositorySagaUpdateAuthoritative,
		repositorySagaUpdateReverse,
		repositorySagaCleanupOldPath,
	} {
		t.Run(step, func(t *testing.T) {
			state := newRepositorySagaState(source)
			state.fail[step] = errors.New("injected apply failure")

			err := executeRepositoryMappingSaga(
				context.Background(),
				state.operations(nil),
				state.repository,
				source,
				target,
				repositorySagaSystemVersion,
			)
			require.Error(t, err)

			require.NoError(t, executeRepositoryMappingSaga(
				context.Background(),
				state.operations(nil),
				state.repository,
				source,
				target,
				repositorySagaSystemVersion,
			))
			state.assertConverged(t, target)
			assert.Equal(t, int64(1), state.repository.Generation)
			assert.Equal(t, "2", state.repository.ResourceVersion)
		})
	}
}

func TestRepositoryMappingSagaCompensatesReservationBeforeCommit(t *testing.T) {
	source := repositoryPath{namespace: "source", name: "catalog"}
	target := repositoryPath{namespace: "source", name: "renamed"}
	state := newRepositorySagaState(source)
	state.fail[repositorySagaUpdateAuthoritative] = errors.New("authoritative write failed")

	err := executeRepositoryMappingSaga(
		context.Background(),
		state.operations(nil),
		state.repository,
		source,
		target,
		repositorySagaSpecVersion,
	)

	require.Error(t, err)
	assert.NotContains(t, state.paths, target)
	assert.Contains(t, state.paths, source)
	assert.Equal(t, source, *state.reverse)
	assert.Equal(t, "1", state.repository.ResourceVersion)
}

func TestRepositoryMappingSagaCompensationFailureRequiresRepair(t *testing.T) {
	source := repositoryPath{namespace: "source", name: "catalog"}
	target := repositoryPath{namespace: "source", name: "renamed"}
	state := newRepositorySagaState(source)
	state.fail[repositorySagaUpdateAuthoritative] = errors.New("authoritative write failed")
	state.fail["release_target"] = errors.New("target release failed")

	err := executeRepositoryMappingSaga(
		context.Background(),
		state.operations(nil),
		state.repository,
		source,
		target,
		repositorySagaSpecVersion,
	)

	require.ErrorIs(t, err, datastore.ErrRepairRequired)
	assert.Equal(t, sagaTestRepositoryID, state.paths[target])
	assert.True(t, repositoryHasPath(state.repository, source))
}

func TestRepositoryMappingSagaPostCommitFailureRequiresRollForward(t *testing.T) {
	source := repositoryPath{namespace: "source", name: "catalog"}
	target := repositoryPath{namespace: "source", name: "renamed"}
	state := newRepositorySagaState(source)
	injector := newTestFailureInjector()
	injector.fail(repositorySagaUpdateReverse, failureBefore)

	err := executeRepositoryMappingSaga(
		context.Background(),
		state.operations(injector),
		state.repository,
		source,
		target,
		repositorySagaSpecVersion,
	)

	require.ErrorIs(t, err, datastore.ErrRepairRequired)
	assert.True(t, repositoryHasPath(state.repository, target))
	assert.Equal(t, source, *state.reverse)
	assert.Equal(t, sagaTestRepositoryID, state.paths[source])
	assert.Equal(t, sagaTestRepositoryID, state.paths[target])
}

func TestRepositoryMappingSagaNeverDeletesAnotherOwnersOldPath(t *testing.T) {
	source := repositoryPath{namespace: "source", name: "catalog"}
	target := repositoryPath{namespace: "source", name: "renamed"}
	state := newRepositorySagaState(source)
	state.repository.Namespace = target.namespace
	state.repository.Name = target.name
	state.repository.Generation = 2
	state.repository.ResourceVersion = "2"
	state.paths[target] = sagaTestRepositoryID
	state.paths[source] = "22222222-2222-2222-2222-222222222222"
	reverse := target
	state.reverse = &reverse

	err := executeRepositoryMappingSaga(
		context.Background(),
		state.operations(nil),
		state.repository,
		source,
		target,
		repositorySagaSpecVersion,
	)

	require.ErrorIs(t, err, datastore.ErrRepairRequired)
	assert.Equal(t, "22222222-2222-2222-2222-222222222222", state.paths[source])
	assert.Equal(t, sagaTestRepositoryID, state.paths[target])
}

func TestRepositoryMappingSagaRepeatedRenameAndTransferAreIdempotent(t *testing.T) {
	tests := []struct {
		name               string
		source             repositoryPath
		target             repositoryPath
		version            repositorySagaVersion
		expectedGeneration int64
	}{
		{
			name:               "rename advances spec version once",
			source:             repositoryPath{namespace: "source", name: "catalog"},
			target:             repositoryPath{namespace: "source", name: "renamed"},
			version:            repositorySagaSpecVersion,
			expectedGeneration: 2,
		},
		{
			name:               "transfer advances system version once",
			source:             repositoryPath{namespace: "source", name: "catalog"},
			target:             repositoryPath{namespace: "target", name: "catalog"},
			version:            repositorySagaSystemVersion,
			expectedGeneration: 1,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			state := newRepositorySagaState(test.source)
			operations := state.operations(nil)

			require.NoError(t, executeRepositoryMappingSaga(
				context.Background(), operations, state.repository, test.source, test.target, test.version,
			))
			require.NoError(t, executeRepositoryMappingSaga(
				context.Background(), operations, state.repository, test.source, test.target, test.version,
			))

			state.assertConverged(t, test.target)
			assert.Equal(t, test.expectedGeneration, state.repository.Generation)
			assert.Equal(t, "2", state.repository.ResourceVersion)
		})
	}
}

func TestRepositoryMappingSagaRemovesStaleOldMappingOwnedByRepository(t *testing.T) {
	source := repositoryPath{namespace: "source", name: "catalog"}
	target := repositoryPath{namespace: "target", name: "catalog"}
	state := newRepositorySagaState(source)
	state.repository.Namespace = target.namespace
	state.repository.Name = target.name
	state.repository.ResourceVersion = "2"
	state.paths[target] = sagaTestRepositoryID
	reverse := target
	state.reverse = &reverse

	require.NoError(t, executeRepositoryMappingSaga(
		context.Background(),
		state.operations(nil),
		state.repository,
		source,
		target,
		repositorySagaSystemVersion,
	))

	state.assertConverged(t, target)
	assert.Equal(t, "2", state.repository.ResourceVersion)
}

func TestRepositoryBucketsForPageIncludesAdjacentFutureBucket(t *testing.T) {
	now := time.Date(2026, time.August, 19, 20, 0, 0, 0, time.UTC)
	cursorTime := time.Date(2026, time.May, 11, 12, 0, 0, 0, time.UTC)
	buckets := repositoryBucketsForPage(datastore.PageParams{
		Last:   100,
		Before: encodeKeysetCursor(cursorTime, sagaTestRepositoryID),
	}, now)

	assert.Equal(t, []string{"2026-05", "2026-06", "2026-07", "2026-08", "2026-09"}, buckets)
}

func TestCollectRepositoryPageContinuesPastDanglingRows(t *testing.T) {
	created := time.Date(2026, time.August, 19, 20, 0, 0, 0, time.UTC)
	danglingOne := "11111111-1111-1111-1111-111111111111"
	danglingTwo := "22222222-2222-2222-2222-222222222222"
	liveOne := "33333333-3333-3333-3333-333333333333"
	liveTwo := "44444444-4444-4444-4444-444444444444"
	calls := 0

	items, err := collectRepositoryPage(
		context.Background(),
		[]string{"2026-08"},
		datastore.PageParams{First: 1},
		func(_ context.Context, bucket string, page datastore.PageParams) ([]repositoryIndexRow, error) {
			assert.Equal(t, "2026-08", bucket)
			calls++
			switch calls {
			case 1:
				assert.Empty(t, page.After)
				return []repositoryIndexRow{
					{Bucket: bucket, CreationTimestamp: created, UID: mustParseUUID(danglingOne)},
					{Bucket: bucket, CreationTimestamp: created.Add(-time.Second), UID: mustParseUUID(danglingTwo)},
				}, nil
			case 2:
				assert.NotEmpty(t, page.After)
				return []repositoryIndexRow{
					{Bucket: bucket, CreationTimestamp: created.Add(-2 * time.Second), UID: mustParseUUID(liveOne)},
					{Bucket: bucket, CreationTimestamp: created.Add(-3 * time.Second), UID: mustParseUUID(liveTwo)},
				}, nil
			default:
				t.Fatalf("unexpected fetch call %d", calls)
				return nil, nil
			}
		},
		func(_ context.Context, uid string) (*datastore.Repository, error) {
			switch uid {
			case danglingOne, danglingTwo:
				return nil, datastore.ErrNotFound
			default:
				return &datastore.Repository{UID: uid}, nil
			}
		},
	)

	require.NoError(t, err)
	require.Len(t, items, 2)
	assert.Equal(t, liveOne, items[0].UID)
	assert.Equal(t, liveTwo, items[1].UID)
	assert.Equal(t, 2, calls)
}
