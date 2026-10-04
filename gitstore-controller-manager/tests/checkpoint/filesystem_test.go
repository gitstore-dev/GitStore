// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package checkpoint_test

import (
	"bufio"
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gitstore-dev/gitstore/controller-manager/internal/cache"
	"github.com/gitstore-dev/gitstore/controller-manager/internal/categorytaxonomy"
	"github.com/gitstore-dev/gitstore/controller-manager/internal/checkpoint"
	"github.com/gitstore-dev/gitstore/controller-manager/internal/manager"
	productcontroller "github.com/gitstore-dev/gitstore/controller-manager/internal/product"
	"github.com/gitstore-dev/gitstore/controller-manager/internal/status"
	"github.com/gitstore-dev/gitstore/controller-manager/internal/types"
	"github.com/stretchr/testify/require"
	"github.com/syndtr/goleveldb/leveldb"
)

func diskFixture(kind, name, version, group string) checkpoint.DiskItem {
	return checkpoint.DiskItem{
		Key:     types.WorkItemKey{Kind: kind, Namespace: "shop", Name: name},
		Version: version,
		Value:   []byte(fmt.Sprintf(`{"name":%q,"resourceVersion":%q}`, name, version)),
		Indexes: []string{group},
		Related: []types.WorkItemKey{{Kind: "CategoryTaxonomy", Namespace: "shop", Name: group}},
	}
}

func TestDiskStoreGenerationsPreservePendingWorkForEveryKind(t *testing.T) {
	for _, kind := range []string{"Product", "CategoryTaxonomy", "Namespace", "Repository"} {
		t.Run(kind, func(t *testing.T) {
			s, err := checkpoint.OpenDiskStore(t.TempDir(), kind)
			require.NoError(t, err)
			defer s.Close()
			a := diskFixture(kind, "a", "1", "old")
			b := diskFixture(kind, "b", "1", "old")
			gen, err := s.BeginSnapshot(t.Context())
			require.NoError(t, err)
			require.NoError(t, s.PutSnapshotPage(t.Context(), gen, []checkpoint.DiskItem{a, b}))
			require.NoError(t, s.FinishSnapshot(t.Context(), gen, "cursor-1"))
			_, _, oldToken, err := s.Read(t.Context(), a.Key)
			require.NoError(t, err)
			ack, err := s.Acknowledge(t.Context(), a.Key, oldToken)
			require.NoError(t, err)
			require.True(t, ack)
			gen, err = s.BeginSnapshot(t.Context())
			require.NoError(t, err)
			require.ErrorIs(t, s.Apply(t.Context(), a, false, true, "forbidden"), checkpoint.ErrSnapshotInProgress)
			require.NoError(t, s.PutSnapshotPage(t.Context(), gen, []checkpoint.DiskItem{a}))
			_, cursor, err := s.Active(t.Context())
			require.NoError(t, err)
			require.Equal(t, "cursor-1", cursor, "partial staging became active")
			require.NoError(t, s.FinishSnapshot(t.Context(), gen, "cursor-2"))
			pending, err := s.Pending(t.Context(), "", checkpoint.DiskPageItems)
			require.NoError(t, err)
			require.Len(t, pending, 1, "unchanged completed work was replayed or deleted work was lost")
			require.Equal(t, b.Key, pending[0].Key)
			a = diskFixture(kind, "a", "2", "new")
			require.NoError(t, s.Apply(t.Context(), a, false, true, "cursor-3"))
			ack, err = s.Acknowledge(t.Context(), a.Key, oldToken)
			require.NoError(t, err)
			require.False(t, ack, "an old completion erased a newer work obligation")
			indexed, _, err := s.IndexPage(t.Context(), "new", "", 1)
			require.NoError(t, err)
			require.Equal(t, []types.WorkItemKey{a.Key}, indexed)
			indexed, _, err = s.IndexPage(t.Context(), "old", "", 1)
			require.NoError(t, err)
			require.Empty(t, indexed)
		})
	}
}

func TestDiskStoreRelatedWorkIsPublishedAndAcknowledgedWithItsGeneration(t *testing.T) {
	s, err := checkpoint.OpenDiskStore(t.TempDir(), "Product")
	require.NoError(t, err)
	defer s.Close()
	a := diskFixture("Product", "a", "1", "old")
	gen, err := s.BeginSnapshot(t.Context())
	require.NoError(t, err)
	require.NoError(t, s.PutSnapshotPage(t.Context(), gen, []checkpoint.DiskItem{a}))
	work, err := s.RelatedPending(t.Context(), "", 256)
	require.NoError(t, err)
	require.Empty(t, work, "unpublished memberships leaked into dispatch")
	require.NoError(t, s.FinishSnapshot(t.Context(), gen, "first"))
	work, err = s.RelatedPending(t.Context(), "", 256)
	require.NoError(t, err)
	require.Len(t, work, 1)
	old := work[0]
	_, _, token, err := s.Read(t.Context(), a.Key)
	require.NoError(t, err)
	ack, err := s.Acknowledge(t.Context(), a.Key, token)
	require.NoError(t, err)
	require.True(t, ack)
	gen, err = s.BeginSnapshot(t.Context())
	require.NoError(t, err)
	a.Indexes = []string{"new"}
	a.Related[0].Name = "new"
	require.NoError(t, s.PutSnapshotPage(t.Context(), gen, []checkpoint.DiskItem{a}))
	work, err = s.RelatedPending(t.Context(), "", 256)
	require.NoError(t, err)
	require.Equal(t, []checkpoint.DiskWork{old}, work)
	require.NoError(t, s.FinishSnapshot(t.Context(), gen, "second"))
	work, err = s.RelatedPending(t.Context(), "", 256)
	require.NoError(t, err)
	require.Len(t, work, 2, "both sides of a snapshot membership change need reconciliation")
	ack, err = s.AcknowledgeRelated(t.Context(), old.Key, old.Token)
	require.NoError(t, err)
	require.False(t, ack, "a completion from the prior generation erased new related work")
	pending, err := s.Pending(t.Context(), "", 256)
	require.NoError(t, err)
	require.Len(t, pending, 1, "projection membership changed even with an unchanged body/version")
	for _, obligation := range work {
		ack, err = s.AcknowledgeRelated(t.Context(), obligation.Key, obligation.Token)
		require.NoError(t, err)
		require.True(t, ack)
	}
	work, err = s.RelatedPending(t.Context(), "", 256)
	require.NoError(t, err)
	require.Empty(t, work)
}

func TestDiskStoreSchedulingCountsAndQuarantineSurviveReplacement(t *testing.T) {
	dir := t.TempDir()
	s, err := checkpoint.OpenDiskStore(dir, "Product")
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })
	a := diskFixture("Product", "a", "1", "old")
	b := diskFixture("Product", "b", "1", "old")
	c := diskFixture("Product", "c", "1", "old")
	gen, err := s.BeginSnapshot(t.Context())
	require.NoError(t, err)
	require.NoError(t, s.PutSnapshotPage(t.Context(), gen, []checkpoint.DiskItem{a, b, c}))
	require.NoError(t, s.FinishSnapshot(t.Context(), gen, "first"))
	b = diskFixture("Product", "b", "2", "new")
	require.NoError(t, s.Apply(t.Context(), b, false, true, "second"))
	ready, err := s.Ready(t.Context(), 3)
	require.NoError(t, err)
	require.Len(t, ready, 3)
	require.Equal(t, b.Key, ready[0].Key, "live changes must not sit behind snapshot replay")
	_, _, bToken, err := s.Read(t.Context(), b.Key)
	require.NoError(t, err)
	deferred, err := s.DeferWork(t.Context(), b.Key, bToken, time.Hour, "", 0)
	require.NoError(t, err)
	require.True(t, deferred)
	_, _, aToken, err := s.Read(t.Context(), a.Key)
	require.NoError(t, err)
	deferred, err = s.DeferWork(t.Context(), a.Key, aToken, 0, "invalid configuration", 3)
	require.NoError(t, err)
	require.True(t, deferred)
	require.Equal(t, checkpoint.DiskCounts{Pending: 3, Poison: 1}, s.Counts())
	require.NoError(t, s.Close())
	s, err = checkpoint.OpenDiskStore(dir, "Product")
	require.NoError(t, err)
	ready, err = s.Ready(t.Context(), 3)
	require.NoError(t, err)
	require.Len(t, ready, 1)
	require.Equal(t, c.Key, ready[0].Key)
	gen, err = s.BeginSnapshot(t.Context())
	require.NoError(t, err)
	require.NoError(t, s.PutSnapshotPage(t.Context(), gen, []checkpoint.DiskItem{a, b, c}))
	require.NoError(t, s.FinishSnapshot(t.Context(), gen, "replacement"))
	require.Equal(t, checkpoint.DiskCounts{Pending: 3, Poison: 1}, s.Counts())
	oldCount, err := s.IndexCount(t.Context(), "old")
	require.NoError(t, err)
	require.EqualValues(t, 2, oldCount)
	newCount, err := s.IndexCount(t.Context(), "new")
	require.NoError(t, err)
	require.EqualValues(t, 1, newCount)
	ready, err = s.Ready(t.Context(), 3)
	require.NoError(t, err)
	require.Len(t, ready, 1)
	require.Equal(t, c.Key, ready[0].Key)
	poison, _, err := s.PoisonPage(t.Context(), "", 256)
	require.NoError(t, err)
	require.Len(t, poison, 1)
	require.Equal(t, 3, poison[0].Attempts)
	require.NoError(t, s.RequestFanout(t.Context(), "*"))
	for range 3 {
		_, err := s.ProcessFanout(t.Context())
		require.NoError(t, err)
	}
	require.EqualValues(t, 1, s.Counts().Poison, "resync must not bypass quarantine")
	require.NoError(t, s.Enqueue(t.Context(), a.Key))
	require.Equal(t, checkpoint.DiskCounts{Pending: 3}, s.Counts())
	ack, err := s.Acknowledge(t.Context(), b.Key, bToken)
	require.NoError(t, err)
	require.False(t, ack, "an old generation completion cleared a rescheduled obligation")
}

func TestDiskStoreFanoutRestartsItsCursorAfterGenerationReplacement(t *testing.T) {
	s, err := checkpoint.OpenDiskStore(t.TempDir(), "Product")
	require.NoError(t, err)
	defer s.Close()
	populate := func() uint64 {
		gen, err := s.BeginSnapshot(t.Context())
		require.NoError(t, err)
		for start := 0; start < 300; start += checkpoint.DiskPageItems {
			var page []checkpoint.DiskItem
			for i := start; i < min(300, start+checkpoint.DiskPageItems); i++ {
				page = append(page, diskFixture("Product", fmt.Sprintf("%03d", i), "1", "group"))
			}
			require.NoError(t, s.PutSnapshotPage(t.Context(), gen, page))
		}
		require.NoError(t, s.FinishSnapshot(t.Context(), gen, fmt.Sprint(gen)))
		return gen
	}
	populate()
	require.NoError(t, s.RequestFanout(t.Context(), "group"))
	progressed, err := s.ProcessFanout(t.Context())
	require.NoError(t, err)
	require.True(t, progressed)
	populate()
	for {
		page, err := s.Pending(t.Context(), "", checkpoint.DiskPageItems)
		require.NoError(t, err)
		if len(page) == 0 {
			break
		}
		for _, item := range page {
			ack, err := s.Acknowledge(t.Context(), item.Key, item.Token)
			require.NoError(t, err)
			require.True(t, ack)
		}
	}
	_, err = s.ProcessFanout(t.Context())
	require.NoError(t, err)
	require.EqualValues(t, checkpoint.DiskPageItems, s.Counts().Pending)
	_, found, token, err := s.Read(t.Context(), diskFixture("Product", "000", "1", "group").Key)
	require.NoError(t, err)
	require.True(t, found)
	require.Positive(t, token, "fanout skipped keys before its old generation cursor")
}

func TestDiskStorePeriodicFanoutPreservesSweepProgress(t *testing.T) {
	s, err := checkpoint.OpenDiskStore(t.TempDir(), "Product")
	require.NoError(t, err)
	defer s.Close()
	gen, err := s.BeginSnapshot(t.Context())
	require.NoError(t, err)
	for start := 0; start < 300; start += checkpoint.DiskPageItems {
		var page []checkpoint.DiskItem
		for i := start; i < min(300, start+checkpoint.DiskPageItems); i++ {
			page = append(page, diskFixture("Product", fmt.Sprintf("%03d", i), "1", "group"))
		}
		require.NoError(t, s.PutSnapshotPage(t.Context(), gen, page))
	}
	require.NoError(t, s.FinishSnapshot(t.Context(), gen, "snapshot"))
	acknowledge := func() {
		for {
			page, err := s.Pending(t.Context(), "", checkpoint.DiskPageItems)
			require.NoError(t, err)
			if len(page) == 0 {
				return
			}
			for _, item := range page {
				ack, err := s.Acknowledge(t.Context(), item.Key, item.Token)
				require.NoError(t, err)
				require.True(t, ack)
			}
		}
	}
	acknowledge()
	require.NoError(t, s.RequestFanout(t.Context(), "*"))
	_, err = s.ProcessFanout(t.Context())
	require.NoError(t, err)
	require.EqualValues(t, checkpoint.DiskPageItems, s.Counts().Pending)
	acknowledge()
	require.NoError(t, s.RequestFanout(t.Context(), "*"))
	_, err = s.ProcessFanout(t.Context())
	require.NoError(t, err)
	require.EqualValues(t, 300-checkpoint.DiskPageItems, s.Counts().Pending)
}

func TestDiskStoreGarbageCollectionResumesAfterInterruptedPage(t *testing.T) {
	dir := t.TempDir()
	s, err := checkpoint.OpenDiskStore(dir, "Product")
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })
	gen, err := s.BeginSnapshot(t.Context())
	require.NoError(t, err)
	for i := range 300 {
		item := diskFixture("Product", fmt.Sprintf("%03d", i), "1", "group")
		require.NoError(t, s.PutSnapshotPage(t.Context(), gen, []checkpoint.DiskItem{item}))
	}
	require.NoError(t, s.FinishSnapshot(t.Context(), gen, "old"))
	gen, err = s.BeginSnapshot(t.Context())
	require.NoError(t, err)
	current := diskFixture("Product", "current", "2", "group")
	require.NoError(t, s.PutSnapshotPage(t.Context(), gen, []checkpoint.DiskItem{current}))
	require.NoError(t, s.FinishSnapshot(t.Context(), gen, "current"))
	counts := s.Counts()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	require.ErrorIs(t, s.CollectGarbage(ctx, func(int64) { cancel() }), context.Canceled)
	require.NoError(t, s.Close())
	s, err = checkpoint.OpenDiskStore(dir, "Product")
	require.NoError(t, err)
	var pages int64
	require.NoError(t, s.CollectGarbage(t.Context(), func(n int64) { pages = n }))
	require.Positive(t, pages)
	require.Equal(t, counts, s.Counts())
	item, found, _, err := s.Read(t.Context(), current.Key)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, current, item)
	pages = 0
	require.NoError(t, s.CollectGarbage(t.Context(), func(n int64) { pages = n }))
	require.Zero(t, pages)
}

func TestDiskStoreReadersDoNotBlockBehindSnapshotMerge(t *testing.T) {
	store, err := checkpoint.OpenDiskStore(t.TempDir(), "Product")
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	item := diskFixture("Product", "one", "1", "category")
	gen, err := store.BeginSnapshot(t.Context())
	require.NoError(t, err)
	require.NoError(t, store.PutSnapshotPage(t.Context(), gen, []checkpoint.DiskItem{item}))
	require.NoError(t, store.FinishSnapshot(t.Context(), gen, "first"))
	gen, err = store.BeginSnapshot(t.Context())
	require.NoError(t, err)
	require.NoError(t, store.PutSnapshotPage(t.Context(), gen, []checkpoint.DiskItem{item}))
	entered, release := make(chan struct{}), make(chan struct{})
	var once, resumeOnce sync.Once
	resume := func() { resumeOnce.Do(func() { close(release) }) }
	t.Cleanup(resume)
	done := make(chan error, 1)
	go func() {
		done <- store.FinishSnapshot(t.Context(), gen, "second", func(int64) {
			once.Do(func() { close(entered); <-release })
		})
	}()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("merge did not begin")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()
	_, _, _, err = store.Read(ctx, item.Key)
	require.ErrorIs(t, err, checkpoint.ErrSnapshotInProgress)
	_, err = store.IndexCount(ctx, "category")
	require.ErrorIs(t, err, checkpoint.ErrSnapshotInProgress)
	require.EqualValues(t, 1, store.Counts().Pending, "health must remain nonblocking")
	resume()
	require.NoError(t, <-done)
}

func TestDiskStoreExclusiveLockAndReplicaIsolation(t *testing.T) {
	dir := t.TempDir()
	a, err := checkpoint.OpenDiskStore(dir, "Product")
	require.NoError(t, err)
	defer a.Close()
	duplicate, err := checkpoint.OpenDiskStore(dir, "Product")
	if duplicate != nil {
		require.NoError(t, duplicate.Close())
	}
	require.Error(t, err, "two processes must not mutate one checkpoint volume")
	for _, scope := range []struct{ dir, kind string }{
		{dir, "Namespace"},
		{filepath.Join(dir, "replica-b"), "Product"},
	} {
		b, err := checkpoint.OpenDiskStore(scope.dir, scope.kind)
		require.NoError(t, err)
		gen, err := b.BeginSnapshot(t.Context())
		require.NoError(t, err)
		require.NoError(t, b.FinishSnapshot(t.Context(), gen, "isolated"))
		require.NoError(t, b.Close())
	}
	gen, _, err := a.Active(t.Context())
	require.NoError(t, err)
	require.Zero(t, gen)
}

func TestDiskStoreConcurrentUpdateAndAcknowledgement(t *testing.T) {
	s, err := checkpoint.OpenDiskStore(t.TempDir(), "Product")
	require.NoError(t, err)
	defer s.Close()
	item := diskFixture("Product", "a", "1", "category")
	gen, err := s.BeginSnapshot(t.Context())
	require.NoError(t, err)
	require.NoError(t, s.PutSnapshotPage(t.Context(), gen, []checkpoint.DiskItem{item}))
	require.NoError(t, s.FinishSnapshot(t.Context(), gen, "first"))
	_, _, stale, err := s.Read(t.Context(), item.Key)
	require.NoError(t, err)
	var wg sync.WaitGroup
	for i := range 4 {
		wg.Go(func() {
			for j := range 100 {
				next := diskFixture("Product", "a", fmt.Sprintf("%d-%d", i, j), "category")
				if err := s.Apply(t.Context(), next, false, true, next.Version); err != nil {
					t.Error(err)
					return
				}
				got, found, token, err := s.Read(t.Context(), item.Key)
				if err != nil || !found || got.Version == "" || token <= stale {
					t.Errorf("incoherent read: found=%v token=%d err=%v", found, token, err)
					return
				}
				if ack, err := s.Acknowledge(t.Context(), item.Key, stale); err != nil || ack {
					t.Errorf("stale ack: acknowledged=%v err=%v", ack, err)
					return
				}
			}
		})
	}
	wg.Wait()
	work, err := s.Pending(t.Context(), "", 256)
	require.NoError(t, err)
	require.Len(t, work, 1)
}

func TestDiskStoreRejectsMetadataThatCouldDeleteActiveDataOrReuseWorkTokens(t *testing.T) {
	for _, metadata := range []string{"garbage", "sequence"} {
		t.Run(metadata, func(t *testing.T) {
			dir := t.TempDir()
			s, err := checkpoint.OpenDiskStore(dir, "Product")
			require.NoError(t, err)
			item := diskFixture("Product", "a", "1", "category")
			active, err := s.BeginSnapshot(t.Context())
			require.NoError(t, err)
			require.NoError(t, s.PutSnapshotPage(t.Context(), active, []checkpoint.DiskItem{item}))
			require.NoError(t, s.FinishSnapshot(t.Context(), active, "committed"))
			staging, err := s.BeginSnapshot(t.Context())
			require.NoError(t, err)
			require.NoError(t, s.Close())
			raw, err := leveldb.OpenFile(filepath.Join(dir, "Product.disk-v2"), nil)
			require.NoError(t, err)
			value := active
			if metadata == "sequence" {
				value = ^uint64(0)
			}
			require.NoError(t, raw.Put([]byte(metadata), binary.BigEndian.AppendUint64(nil, value), nil))
			require.NoError(t, raw.Close())
			s, err = checkpoint.OpenDiskStore(dir, "Product")
			require.NoError(t, err)
			defer s.Close()
			if metadata == "sequence" {
				require.Error(t, s.FinishSnapshot(t.Context(), staging, "must-not-publish"))
			} else {
				_, err := s.BeginSnapshot(t.Context())
				require.Error(t, err)
			}
			generation, cursor, err := s.Active(t.Context())
			require.NoError(t, err)
			require.Equal(t, active, generation)
			require.Equal(t, "committed", cursor)
			got, found, _, err := s.Read(t.Context(), item.Key)
			require.NoError(t, err)
			require.True(t, found)
			require.Equal(t, item, got)
		})
	}
}

func TestDiskStoreRejectsOversizeAndCanceledPagesWithoutPublishing(t *testing.T) {
	s, err := checkpoint.OpenDiskStore(t.TempDir(), "Product")
	require.NoError(t, err)
	defer s.Close()
	gen, err := s.BeginSnapshot(t.Context())
	require.NoError(t, err)
	item := diskFixture("Product", "a", "1", "category")
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	require.ErrorIs(t, s.PutSnapshotPage(ctx, gen, []checkpoint.DiskItem{item}), context.Canceled)
	require.Error(t, s.PutSnapshotPage(t.Context(), gen, make([]checkpoint.DiskItem, checkpoint.DiskPageItems+1)))
	item.Value = []byte(`{"padding":"` + strings.Repeat("x", checkpoint.DiskItemBytes) + `"}`)
	require.Error(t, s.PutSnapshotPage(t.Context(), gen, []checkpoint.DiskItem{item}))
	var oversized []checkpoint.DiskItem
	for i := range 5 {
		item := diskFixture("Product", fmt.Sprint(i), "1", "category")
		item.Value = []byte(`{"padding":"` + strings.Repeat("x", checkpoint.DiskItemBytes-32) + `"}`)
		oversized = append(oversized, item)
	}
	require.Error(t, s.PutSnapshotPage(t.Context(), gen, oversized))
	active, _, err := s.Active(t.Context())
	require.NoError(t, err)
	require.Zero(t, active)
	require.NoError(t, s.FinishSnapshot(t.Context(), gen, "empty"))
	pending, err := s.Pending(t.Context(), "", 256)
	require.NoError(t, err)
	require.Empty(t, pending, "rejected page partially wrote work")
	_, exists, _, err := s.Read(t.Context(), oversized[0].Key)
	require.NoError(t, err)
	require.False(t, exists, "rejected page partially wrote data")
	other, err := checkpoint.OpenDiskStore(t.TempDir(), "Product")
	require.NoError(t, err)
	require.NoError(t, other.Close())
	_, _, err = other.Active(t.Context())
	require.Error(t, err, "closed storage must not look like an empty valid catalog")
}

func TestDiskStoreCrashHelper(t *testing.T) {
	dir := os.Getenv("GITSTORE_DISK_CRASH_HELPER")
	if dir == "" {
		t.Skip("subprocess crash helper")
	}
	s, err := checkpoint.OpenDiskStore(dir, "Product")
	require.NoError(t, err)
	a := diskFixture("Product", "a", "1", "first")
	gen, err := s.BeginSnapshot(t.Context())
	require.NoError(t, err)
	require.NoError(t, s.PutSnapshotPage(t.Context(), gen, []checkpoint.DiskItem{a}))
	require.NoError(t, s.FinishSnapshot(t.Context(), gen, "committed-1"))
	a = diskFixture("Product", "a", "2", "second")
	require.NoError(t, s.Apply(t.Context(), a, false, true, "committed-2"))
	gen, err = s.BeginSnapshot(t.Context())
	require.NoError(t, err)
	a = diskFixture("Product", "a", "3", "uncommitted")
	require.NoError(t, s.PutSnapshotPage(t.Context(), gen, []checkpoint.DiskItem{a}))
	fmt.Println("DISK-CRASH-READY")
	for {
		time.Sleep(time.Hour)
	}
}

func TestDiskStoreSurvivesProcessKillWithoutPublishingPartialSnapshot(t *testing.T) {
	dir := t.TempDir()
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestDiskStoreCrashHelper$")
	cmd.Env = append(os.Environ(), "GITSTORE_DISK_CRASH_HELPER="+dir)
	stdout, err := cmd.StdoutPipe()
	require.NoError(t, err)
	cmd.Stderr = os.Stderr
	require.NoError(t, cmd.Start())
	t.Cleanup(func() {
		if cmd.ProcessState == nil {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		}
	})
	scanner := bufio.NewScanner(stdout)
	ready := false
	for scanner.Scan() {
		if scanner.Text() == "DISK-CRASH-READY" {
			ready = true
			break
		}
	}
	require.True(t, ready, "child failed before durable writes")
	require.NoError(t, cmd.Process.Kill())
	require.Error(t, cmd.Wait())
	s, err := checkpoint.OpenDiskStore(dir, "Product")
	require.NoError(t, err)
	defer s.Close()
	_, cursor, err := s.Active(t.Context())
	require.NoError(t, err)
	require.Equal(t, "committed-2", cursor)
	item, found, token, err := s.Read(t.Context(), diskFixture("Product", "a", "2", "second").Key)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, "2", item.Version)
	require.Positive(t, token)
	gen, err := s.BeginSnapshot(t.Context())
	require.NoError(t, err)
	require.NoError(t, s.PutSnapshotPage(t.Context(), gen, []checkpoint.DiskItem{item}))
	require.NoError(t, s.FinishSnapshot(t.Context(), gen, "replacement"))
	pending, err := s.Pending(t.Context(), "", 1)
	require.NoError(t, err)
	require.Len(t, pending, 1)
	require.Equal(t, item.Key, pending[0].Key)
}

type diskCapacityStatusClient struct {
	writes atomic.Int64
}

func (s *diskCapacityStatusClient) Apply(ctx context.Context, _ types.WorkItemKey, _ *status.StatusPatch) error {
	s.writes.Add(1)
	return ctx.Err()
}

type diskCapacityReconciler func(context.Context, types.WorkItemKey) types.ReconcileResult

func (f diskCapacityReconciler) Reconcile(ctx context.Context, key types.WorkItemKey) types.ReconcileResult {
	return f(ctx, key)
}

func runDiskControllerProbe(t *testing.T, store *checkpoint.DiskStore, target int) {
	t.Helper()
	before := store.Counts().Pending
	sink := new(diskCapacityStatusClient)
	reconciler := productcontroller.NewReconcilerWithLookup(
		func(ctx context.Context, key types.WorkItemKey) (categorytaxonomy.Product, bool, error) {
			item, found, _, err := store.Read(ctx, key)
			var product categorytaxonomy.Product
			if err != nil || !found {
				return product, found, err
			}
			err = json.Unmarshal(item.Value, &product)
			return product, err == nil, err
		},
		func(ctx context.Context, key types.WorkItemKey) (categorytaxonomy.CategoryTaxonomy, bool, error) {
			return categorytaxonomy.CategoryTaxonomy{UID: key.Name, Name: key.Name, Namespace: key.Namespace}, true, ctx.Err()
		}, sink, nil,
	)
	var calls, active, peak atomic.Int64
	probe := diskCapacityReconciler(func(ctx context.Context, key types.WorkItemKey) types.ReconcileResult {
		n := active.Add(1)
		defer active.Add(-1)
		for old := peak.Load(); n > old; old = peak.Load() {
			if peak.CompareAndSwap(old, n) {
				break
			}
		}
		if calls.Add(1) > int64(target) {
			return types.ResultAfter(time.Hour)
		}
		return reconciler.Reconcile(ctx, key)
	})
	gate := cache.New[categorytaxonomy.Product]()
	gate.MarkSynced()
	mgr := manager.New()
	require.NoError(t, mgr.Register(manager.ReconcilerRegistration{
		Kind: "Product", Cache: gate, Disk: store, Reconciler: probe, WorkerCount: 4,
		// This probe isolates dispatch/lookup/writeback memory. Durable
		// cross-kind delivery has separate generation/transfer contracts.
		RelatedEnqueue: func(ctx context.Context, _ types.WorkItemKey) error { return ctx.Err() },
	}))
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() {
		defer close(done)
		if err := mgr.Start(ctx); err != nil {
			t.Errorf("capacity controller: %v", err)
		}
	}()
	t.Cleanup(func() { cancel(); <-done })
	require.Eventually(t, func() bool {
		return store.Counts().Pending == before-uint64(target)
	}, 20*time.Second, time.Millisecond)
	cancel()
	<-done
	require.Empty(t, gate.List())
	require.LessOrEqual(t, peak.Load(), int64(4))
	require.EqualValues(t, target, sink.writes.Load())
	t.Logf("controller probe: acknowledged=%d status_writes=%d peak_active_workers=%d (stubbed status API)", target, sink.writes.Load(), peak.Load())
}

func checkpointDiskUsage(dir string) (logical, allocated int64, err error) {
	supported := true
	err = filepath.Walk(dir, func(_ string, info os.FileInfo, walkErr error) error {
		if os.IsNotExist(walkErr) {
			return nil // Compaction can unlink a table between enumeration and stat.
		}
		if walkErr != nil {
			return walkErr
		}
		if !info.IsDir() {
			logical += info.Size()
		}
		stat := reflect.Indirect(reflect.ValueOf(info.Sys()))
		if stat.IsValid() && stat.Kind() == reflect.Struct {
			blocks := stat.FieldByName("Blocks")
			if blocks.IsValid() && blocks.CanInt() {
				allocated += blocks.Int() * 512
				return nil
			}
		}
		supported = false
		return nil
	})
	if !supported {
		allocated = -1
	}
	return
}

func TestDiskStoreBoundedMemory(t *testing.T) {
	rows := 10000
	if value := os.Getenv("GITSTORE_CHECKPOINT_CAPACITY_ROWS"); value != "" {
		var err error
		rows, err = strconv.Atoi(value)
		require.NoError(t, err)
		require.GreaterOrEqual(t, rows, 10000)
		require.LessOrEqual(t, rows, 5000000)
	}
	dir := t.TempDir()
	s, err := checkpoint.OpenDiskStore(dir, "Product")
	require.NoError(t, err)
	gen, err := s.BeginSnapshot(t.Context())
	require.NoError(t, err)
	started := time.Now()
	var peakHeap atomic.Uint64
	var peakLogical, peakAllocated atomic.Int64
	diskErrors := make(chan error, 1)
	sampleDisk := func() (int64, int64) {
		logical, allocated, err := checkpointDiskUsage(dir)
		if err != nil {
			select {
			case diskErrors <- err:
			default:
			}
		}
		for old := peakLogical.Load(); logical > old; old = peakLogical.Load() {
			if peakLogical.CompareAndSwap(old, logical) {
				break
			}
		}
		for old := peakAllocated.Load(); allocated > old; old = peakAllocated.Load() {
			if peakAllocated.CompareAndSwap(old, allocated) {
				break
			}
		}
		return logical, allocated
	}
	sample := func() {
		var m runtime.MemStats
		runtime.ReadMemStats(&m)
		for old := peakHeap.Load(); m.HeapInuse > old; old = peakHeap.Load() {
			if peakHeap.CompareAndSwap(old, m.HeapInuse) {
				break
			}
		}
	}
	sampling, cancelSampling := context.WithCancel(t.Context())
	sampled := make(chan struct{})
	go func() {
		defer close(sampled)
		ticker := time.NewTicker(20 * time.Millisecond)
		defer ticker.Stop()
		diskTicker := time.NewTicker(time.Second)
		defer diskTicker.Stop()
		for {
			select {
			case <-sampling.Done():
				return
			case <-ticker.C:
				sample()
			case <-diskTicker.C:
				sampleDisk()
			}
		}
	}()
	t.Cleanup(func() {
		cancelSampling()
		<-sampled
		select {
		case err := <-diskErrors:
			t.Errorf("disk usage sampling failed: %v", err)
		default:
		}
	})
	t.Cleanup(func() { _ = s.Close() })
	writeSnapshot := func() {
		page := make([]checkpoint.DiskItem, 0, checkpoint.DiskPageItems)
		for i := range rows {
			name := fmt.Sprintf("fixture-%08d", i)
			group := fmt.Sprintf("category-%03d", i%100)
			item := diskFixture("Product", name, "1", group)
			item.Value = []byte(fmt.Sprintf(`{"UID":%q,"Namespace":"shop","Name":%q,"Generation":1,"ResourceVersion":"1","Finalizers":["gitstore.dev/foreground-deletion"],"DeletionTimestamp":null,"CategoryRefName":%q,"Status":{"ObservedGeneration":1,"Conditions":[{"Type":"AdmissionAccepted","Status":"TRUE","ObservedGeneration":1,"Reason":"Accepted","Message":"Resource has been admitted"},{"Type":"CategoryResolved","Status":"TRUE","ObservedGeneration":1,"Reason":"Resolved","Message":"Category resolved"},{"Type":"Ready","Status":"TRUE","ObservedGeneration":1,"Reason":"Ready","Message":"Resource is ready"}],"Resolved":null}}`, name, name, group))
			page = append(page, item)
			if len(page) == checkpoint.DiskPageItems || i == rows-1 {
				require.NoError(t, s.PutSnapshotPage(t.Context(), gen, page))
				clear(page)
				page = page[:0]
				sample()
			}
		}
	}
	writeSnapshot()
	require.NoError(t, s.FinishSnapshot(t.Context(), gen, "capacity-snapshot"))
	require.NoError(t, s.Close())
	s, err = checkpoint.OpenDiskStore(dir, "Product")
	require.NoError(t, err)
	verifyPending := func(want int) {
		after, counted := "", 0
		for {
			work, err := s.Pending(t.Context(), after, checkpoint.DiskPageItems)
			require.NoError(t, err)
			if len(work) == 0 {
				break
			}
			require.LessOrEqual(t, len(work), checkpoint.DiskPageItems)
			counted += len(work)
			after = work[len(work)-1].Cursor
			sample()
		}
		require.Equal(t, want, counted, "restart lost pending work")
		pending, _, err := s.WorkCounts(t.Context())
		require.NoError(t, err)
		require.Equal(t, uint64(want), pending, "durable pending counter drifted")
	}
	verifyData := func() {
		after, count := "", 0
		for {
			items, next, err := s.DataPage(t.Context(), after, checkpoint.DiskPageItems)
			require.NoError(t, err)
			if len(items) == 0 {
				break
			}
			for _, item := range items {
				require.Equal(t, fmt.Sprintf("fixture-%08d", count), item.Key.Name)
				require.Equal(t, "1", item.Version)
				count++
			}
			after = next
			sample()
		}
		require.Equal(t, rows, count, "restart lost resource projections")
	}
	verifyPending(rows)
	verifyData()
	indexed, _, err := s.IndexPage(t.Context(), "category-000", "", checkpoint.DiskPageItems)
	require.NoError(t, err)
	require.Len(t, indexed, min(rows/100, checkpoint.DiskPageItems))
	_, exists, _, err := s.Read(t.Context(), diskFixture("Product", fmt.Sprintf("fixture-%08d", rows-1), "1", "unused").Key)
	require.NoError(t, err)
	require.True(t, exists)
	logical, allocated := sampleDisk()
	if os.Getenv("GITSTORE_CHECKPOINT_CAPACITY_ROWS") != "" {
		require.GreaterOrEqual(t, allocated, int64(0), "capacity proof requires filesystem allocated-block measurements")
	}
	t.Logf("initial snapshot: rows=%d pending=%d peak_heap_bytes=%d logical_file_bytes=%d allocated_bytes=%d elapsed=%s",
		rows, rows, peakHeap.Load(), logical, allocated, time.Since(started))
	completed, err := s.Pending(t.Context(), "", checkpoint.DiskPageItems)
	require.NoError(t, err)
	runDiskControllerProbe(t, s, len(completed))
	gen, err = s.BeginSnapshot(t.Context())
	require.NoError(t, err)
	writeSnapshot()
	require.NoError(t, s.FinishSnapshot(t.Context(), gen, "capacity-replacement"))
	logical, allocated = sampleDisk()
	t.Logf("replacement before retirement: logical_file_bytes=%d allocated_bytes=%d", logical, allocated)
	require.NoError(t, s.Close())
	s, err = checkpoint.OpenDiskStore(dir, "Product")
	require.NoError(t, err)
	verifyPending(rows - len(completed))
	verifyData()
	_, cursor, err := s.Active(t.Context())
	require.NoError(t, err)
	require.Equal(t, "capacity-replacement", cursor)
	require.NoError(t, s.CollectGarbage(t.Context()))
	require.NoError(t, s.Close())
	s, err = checkpoint.OpenDiskStore(dir, "Product")
	require.NoError(t, err)
	verifyPending(rows - len(completed))
	logical, allocated = sampleDisk()
	sample()
	require.LessOrEqual(t, peakHeap.Load(), uint64(256<<20), "disk storage exceeded the measured Go-heap ceiling")
	t.Logf("replacement snapshot: rows=%d pending=%d peak_heap_bytes=%d elapsed=%s", rows, rows-len(completed), peakHeap.Load(), time.Since(started))
	t.Logf("disk usage after retirement/reopen: logical_file_bytes=%d allocated_bytes=%d sampled_peak_logical_file_bytes=%d sampled_peak_allocated_bytes=%d",
		logical, allocated, peakLogical.Load(), peakAllocated.Load())
}

func TestFilesystemStore_ReplacementPreservesGroupAndReplicaIsolation(t *testing.T) {
	root := t.TempDir()
	records := make(map[string]checkpoint.Record)
	for _, name := range []string{"products/replica-a", "products/replica-b", "categories/replica-a"} {
		dir := filepath.Join(root, name)
		store, err := checkpoint.NewFilesystemStore(dir)
		if err != nil {
			t.Fatal(err)
		}
		rec := checkpoint.Record{
			Kind: "Product", ResourceVersion: "cursor-" + name,
			Snapshot:          []byte(`[{"uid":"product-1","name":"widget"}]`),
			ReplayKeys:        []types.WorkItemKey{{Kind: "Product", Namespace: "shop", Name: "widget"}},
			RelatedReplayKeys: []types.WorkItemKey{{Kind: "CategoryTaxonomy", Namespace: "shop", Name: "tools"}},
			WrittenAt:         time.Now().UTC().Truncate(time.Second),
		}
		if err := store.Save(t.Context(), rec); err != nil {
			t.Fatal(err)
		}
		records[name] = rec
	}
	for name, want := range records {
		replacement, err := checkpoint.NewFilesystemStore(filepath.Join(root, name))
		if err != nil {
			t.Fatal(err)
		}
		got, err := replacement.Load(t.Context(), want.Kind)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatal("replacement lost its scoped snapshot, cursor or replay work")
		}
	}
}

func TestFilesystemStore_SaveThenLoad_RoundTrips(t *testing.T) {
	dir := t.TempDir()
	store, err := checkpoint.NewFilesystemStore(dir)
	if err != nil {
		t.Fatalf("NewFilesystemStore: %v", err)
	}

	want := checkpoint.Record{Kind: "Widget", ResourceVersion: "42", Snapshot: []byte("[]"), WrittenAt: time.Now().Truncate(time.Second)}
	if err := store.Save(context.Background(), want); err != nil {
		t.Fatalf("Save: %v", err)
	}

	got, err := store.Load(context.Background(), "Widget")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.Kind != want.Kind || got.ResourceVersion != want.ResourceVersion {
		t.Errorf("Load() = %+v, want %+v", got, want)
	}
}

func TestFilesystemStore_AtomicWrite_NoPartialFileOnCrash(t *testing.T) {
	dir := t.TempDir()
	store, err := checkpoint.NewFilesystemStore(dir)
	if err != nil {
		t.Fatalf("NewFilesystemStore: %v", err)
	}

	if err := store.Save(context.Background(), checkpoint.Record{Kind: "Widget", ResourceVersion: "1", Snapshot: []byte("[]")}); err != nil {
		t.Fatalf("Save: %v", err)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	for _, e := range entries {
		if filepath.Ext(e.Name()) == ".tmp" || filepath.Ext(filepath.Ext(e.Name())) == ".tmp" {
			t.Errorf("temp file left behind after Save: %s", e.Name())
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "Widget.checkpoint.json")); err != nil {
		t.Errorf("expected final checkpoint file to exist: %v", err)
	}
}

func TestFilesystemStore_CorruptFile_ReturnsError(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "Widget.checkpoint.json"), []byte("{not valid json"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	store, err := checkpoint.NewFilesystemStore(dir)
	if err != nil {
		t.Fatalf("NewFilesystemStore: %v", err)
	}

	if _, err := store.Load(context.Background(), "Widget"); err == nil {
		t.Error("expected error loading corrupt checkpoint file, got nil")
	}
}

func TestFilesystemStore_MissingFile_ReturnsError(t *testing.T) {
	dir := t.TempDir()
	store, err := checkpoint.NewFilesystemStore(dir)
	if err != nil {
		t.Fatalf("NewFilesystemStore: %v", err)
	}

	if _, err := store.Load(context.Background(), "DoesNotExist"); err == nil {
		t.Error("expected error loading missing checkpoint file, got nil")
	}
}

func TestFilesystemStore_OneFilePerKind_Isolated(t *testing.T) {
	dir := t.TempDir()
	store, err := checkpoint.NewFilesystemStore(dir)
	if err != nil {
		t.Fatalf("NewFilesystemStore: %v", err)
	}

	if err := store.Save(context.Background(), checkpoint.Record{Kind: "A", ResourceVersion: "1", Snapshot: []byte("[]")}); err != nil {
		t.Fatalf("Save A: %v", err)
	}
	if err := store.Save(context.Background(), checkpoint.Record{Kind: "B", ResourceVersion: "99", Snapshot: []byte("[]")}); err != nil {
		t.Fatalf("Save B: %v", err)
	}

	// Corrupt B's file; A must remain readable.
	if err := os.WriteFile(filepath.Join(dir, "B.checkpoint.json"), []byte("garbage"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	recA, err := store.Load(context.Background(), "A")
	if err != nil {
		t.Fatalf("Load A after B corrupted: %v", err)
	}
	if recA.ResourceVersion != "1" {
		t.Errorf("A.ResourceVersion = %q, want 1", recA.ResourceVersion)
	}

	if _, err := store.Load(context.Background(), "B"); err == nil {
		t.Error("expected B to be unreadable after corruption")
	}
}

func TestFilesystemStore_SemanticallyInvalidRecord_ReturnsError(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "Widget.checkpoint.json"), []byte(`{"Kind":"Other","ResourceVersion":"42","Snapshot":[]}`), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	store, err := checkpoint.NewFilesystemStore(dir)
	if err != nil {
		t.Fatalf("NewFilesystemStore: %v", err)
	}

	if _, err := store.Load(context.Background(), "Widget"); err == nil {
		t.Error("expected error loading checkpoint with mismatched kind")
	}
}

func TestFilesystemStore_MissingSnapshot_ReturnsError(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "Widget.checkpoint.json"), []byte(`{"Kind":"Widget","ResourceVersion":"42"}`), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	store, err := checkpoint.NewFilesystemStore(dir)
	if err != nil {
		t.Fatalf("NewFilesystemStore: %v", err)
	}

	if _, err := store.Load(context.Background(), "Widget"); err == nil {
		t.Error("expected error loading checkpoint without a snapshot")
	}
}
