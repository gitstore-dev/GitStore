// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package translation_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gitstore-dev/gitstore/controller-manager/internal/cache"
	"github.com/gitstore-dev/gitstore/controller-manager/internal/checkpoint"
	"github.com/gitstore-dev/gitstore/controller-manager/internal/graphqlclient"
	"github.com/gitstore-dev/gitstore/controller-manager/internal/listwatch"
	"github.com/gitstore-dev/gitstore/controller-manager/internal/manager"
	"github.com/gitstore-dev/gitstore/controller-manager/internal/translation"
	"github.com/gitstore-dev/gitstore/controller-manager/internal/types"
	"github.com/stretchr/testify/require"
)

func TestReconcilerQuarantineSurvivesReplacementAndUnchangedColdRelist(t *testing.T) {
	key := types.WorkItemKey{Kind: "Translation", Namespace: "shop", Name: "fr"}
	source := translation.Resource{Metadata: translation.Metadata{UID: "source-id", Namespace: key.Namespace,
		Name: key.Name, ResourceVersion: "1", Generation: 1, Revision: "accepted"},
		Spec: json.RawMessage(`{"locale":"fr","targetRef":{"apiVersion":"catalog.gitstore.dev/v1beta1","kind":"Product","name":"laptop"},"patches":[{"fieldPath":"spec.title","translation":"Titre"}]}`),
		Body: "exact\r\n"}
	var available atomic.Bool
	var writes atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Query     string         `json:"query"`
			Variables map[string]any `json:"variables"`
		}
		require.NoError(t, json.NewDecoder(r.Body).Decode(&request))
		if !available.Load() {
			require.NoError(t, json.NewEncoder(w).Encode(map[string]any{"errors": []any{
				map[string]any{"message": "forbidden", "extensions": map[string]any{"code": "FORBIDDEN"}},
			}}))
			return
		}
		var field string
		var value any
		switch {
		case strings.Contains(request.Query, "translationValidationSnapshot("):
			field = "translationValidationSnapshot"
			value = map[string]any{
				"sourceID": "source-id", "sourceVersion": "2", "sourceDigest": "source-digest", "namespaceUID": "namespace-uid",
				"binding": map[string]any{"translationID": "source-id", "sourceVersion": "2", "bindingVersion": "1",
					"referenceEpoch": "epoch", "namespaceUID": "namespace-uid", "boundUID": "target-uid"},
				"target": map[string]any{"namespace": "shop", "fields": map[string]any{"title": "Laptop"},
					"evidence": map[string]any{"apiVersion": "catalog.gitstore.dev/v1beta1", "kind": "Product",
						"namespace": "shop", "name": "laptop", "uid": "target-uid", "generation": 1,
						"resourceVersion": "1", "contentDigest": "material"}},
			}
		case strings.Contains(request.Query, "updateTranslationStatus("):
			input := request.Variables["input"].(map[string]any)
			require.Equal(t, "2", input["resourceVersion"])
			require.Equal(t, float64(2), input["observedGeneration"])
			require.Equal(t, "source-id", input["uid"])
			require.Equal(t, "TRUE", input["conditions"].([]any)[0].(map[string]any)["status"])
			writes.Add(1)
			field = "updateTranslationStatus"
			value = map[string]any{"translation": map[string]any{"metadata": map[string]any{"resourceVersion": "3"}}}
		default:
			t.Errorf("unexpected recovery operation: %s", request.Query)
			http.Error(w, "unexpected operation", http.StatusBadRequest)
			return
		}
		require.NoError(t, json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{field: value}}))
	}))
	defer server.Close()
	client := translation.NewGraphQLClient(graphqlclient.New(server.URL, graphqlclient.NewStaticToken("controller")))
	dir := t.TempDir()
	store, err := checkpoint.OpenDiskStore(dir, "Translation")
	require.NoError(t, err)
	var cancel context.CancelFunc
	var done chan error
	stop := func() {
		if cancel != nil {
			cancel()
			select {
			case err := <-done:
				require.NoError(t, err)
			case <-time.After(5 * time.Second):
				t.Fatal("quarantine fixture did not stop")
			}
			cancel = nil
		}
	}
	t.Cleanup(func() {
		stop()
		if store != nil {
			require.NoError(t, store.Close())
		}
	})
	relist := func() {
		t.Helper()
		raw, err := json.Marshal(source)
		require.NoError(t, err)
		generation, err := store.BeginSnapshot(t.Context())
		require.NoError(t, err)
		require.NoError(t, store.PutSnapshotPage(t.Context(), generation, []checkpoint.DiskItem{
			{Key: key, Version: source.Metadata.ResourceVersion, Value: raw},
		}))
		require.NoError(t, store.FinishSnapshot(t.Context(), generation, "journal@"+source.Metadata.ResourceVersion))
	}
	start := func() {
		gate := cache.New[translation.Resource]()
		gate.MarkSynced()
		runner := &listwatch.Runner[translation.Resource]{Kind: "Translation", Cache: gate, Disk: store,
			KeyFunc: func(resource translation.Resource) types.WorkItemKey {
				return types.WorkItemKey{Kind: "Translation", Namespace: resource.Metadata.Namespace, Name: resource.Metadata.Name}
			},
			RevisionFunc: func(resource translation.Resource) string { return resource.Metadata.ResourceVersion }}
		mgr := manager.New()
		require.NoError(t, mgr.Register(manager.ReconcilerRegistration{Kind: "Translation", Cache: gate, Disk: store,
			Reconciler:  translation.NewReconcilerWithLookup(runner.Lookup, client, client),
			MaxAttempts: 1, WorkerCount: 1, StallThreshold: time.Minute}))
		ctx, cancelWorker := context.WithCancel(t.Context())
		cancel, done = cancelWorker, make(chan error, 1)
		go func() { done <- mgr.Start(ctx) }()
	}
	relist()
	start()
	require.Eventually(t, func() bool { return store.Counts().Poison == 1 }, 5*time.Second, 5*time.Millisecond)
	stop()
	saved, err := store.WorkSchedule(t.Context(), key)
	require.NoError(t, err)
	require.False(t, saved.QuarantinedAt.IsZero())
	require.NoError(t, store.Close())
	store = nil
	store, err = checkpoint.OpenDiskStore(dir, "Translation")
	require.NoError(t, err)
	restored, err := store.WorkSchedule(t.Context(), key)
	require.NoError(t, err)
	require.Equal(t, saved, restored)
	relist()
	require.EqualValues(t, 1, store.Counts().Poison, "unchanged cold relists cannot erase quarantined obligations")
	source.Metadata.ResourceVersion, source.Metadata.Generation, source.Metadata.Revision = "2", 2, "updated"
	source.Body = "updated exact\r\n"
	relist()
	require.Zero(t, store.Counts().Poison, "a changed source releases the old poisoned attempt")
	available.Store(true)
	start()
	require.Eventually(t, func() bool { return store.Counts().Pending == 0 }, 5*time.Second, 5*time.Millisecond)
	stop()
	require.EqualValues(t, 1, writes.Load())
	require.Zero(t, store.Counts().Poison)
}

func TestReconcilerDiskReplicasRetainConflictsAndResumeFanout(t *testing.T) {
	key := types.WorkItemKey{Kind: "Translation", Namespace: "acme", Name: "title"}
	source := translation.Resource{
		Metadata: translation.Metadata{UID: "source-id", Namespace: key.Namespace, Name: key.Name,
			ResourceVersion: "3", Generation: 2, Revision: "accepted-source"},
		Spec: json.RawMessage(`{"locale":"iw-IL","targetRef":{"apiVersion":"catalog.gitstore.dev/v1beta1","kind":"Product","name":"laptop"},"patches":[{"fieldPath":"spec.title","translation":"Titre"}]}`),
		Body: "exact source\r\n",
	}
	target := translation.DependencyTarget{NamespaceUID: "namespace-uid", APIVersion: "catalog.gitstore.dev/v1beta1",
		Kind: "Product", Name: "laptop", UID: "target-uid"}
	var mu sync.Mutex
	writes, conflicts, binds := 0, 0, 0
	var boundUID *string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Query     string         `json:"query"`
			Variables map[string]any `json:"variables"`
		}
		require.NoError(t, json.NewDecoder(r.Body).Decode(&request))
		mu.Lock()
		defer mu.Unlock()
		conflict := func() {
			conflicts++
			require.NoError(t, json.NewEncoder(w).Encode(map[string]any{"errors": []any{
				map[string]any{"message": "source changed", "extensions": map[string]any{"code": "CONFLICT"}},
			}}))
		}
		binding := func() map[string]any {
			return map[string]any{"translationID": source.Metadata.UID, "sourceVersion": source.Metadata.ResourceVersion,
				"bindingVersion": "binding-version", "referenceEpoch": "epoch", "namespaceUID": target.NamespaceUID, "boundUID": boundUID}
		}
		var field string
		var value any
		switch {
		case strings.Contains(request.Query, "translationValidationSnapshot("):
			if request.Variables["sourceID"] != source.Metadata.UID || request.Variables["sourceVersion"] != source.Metadata.ResourceVersion {
				conflict()
				return
			}
			field = "translationValidationSnapshot"
			value = map[string]any{
				"sourceID": source.Metadata.UID, "sourceVersion": source.Metadata.ResourceVersion,
				"sourceDigest": "accepted-source-digest", "namespaceUID": target.NamespaceUID, "binding": binding(),
				"observation": source.Status,
				"target": map[string]any{"namespace": key.Namespace, "fields": map[string]any{"title": "Laptop"},
					"evidence": map[string]any{"apiVersion": target.APIVersion, "kind": target.Kind, "namespace": key.Namespace,
						"name": target.Name, "uid": target.UID, "generation": 1, "revision": "target-revision",
						"resourceVersion": "7", "contentDigest": "target-material"}},
			}
		case strings.Contains(request.Query, "bindTranslationTarget("):
			input := request.Variables["input"].(map[string]any)
			if input["translationID"] != source.Metadata.UID || input["sourceVersion"] != source.Metadata.ResourceVersion {
				conflict()
				return
			}
			require.Equal(t, target.UID, input["targetUID"])
			if boundUID == nil {
				uid := target.UID
				boundUID = &uid
				binds++
			}
			field, value = "bindTranslationTarget", map[string]any{"binding": binding()}
		case strings.Contains(request.Query, "updateTranslationStatus("):
			input := request.Variables["input"].(map[string]any)
			if input["uid"] != source.Metadata.UID || input["resourceVersion"] != source.Metadata.ResourceVersion {
				conflict()
				return
			}
			require.Equal(t, float64(source.Metadata.Generation), input["observedGeneration"])
			require.Equal(t, source.Metadata.Revision, input["lastAppliedRevision"])
			raw, err := json.Marshal(input)
			require.NoError(t, err)
			require.NoError(t, json.Unmarshal(raw, &source.Status))
			source.Metadata.ResourceVersion, source.Status.ResourceVersion = "4", "4"
			writes++
			field, value = "updateTranslationStatus", map[string]any{
				"translation": map[string]any{"metadata": map[string]any{"resourceVersion": "4"}},
			}
		default:
			t.Errorf("unexpected operation; validation must never mutate target content: %s", request.Query)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		require.NoError(t, json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{field: value}}))
	}))
	t.Cleanup(server.Close)
	client := translation.NewGraphQLClient(graphqlclient.New(server.URL, graphqlclient.NewStaticToken("controller")))
	indexes, err := translation.DependencyIndexes(target, false)
	require.NoError(t, err)
	item := func() checkpoint.DiskItem {
		t.Helper()
		mu.Lock()
		defer mu.Unlock()
		projected := source
		projected.Status.Conditions, projected.Status.Resolved = nil, nil
		raw, err := json.Marshal(projected)
		require.NoError(t, err)
		return checkpoint.DiskItem{Key: key, Version: source.Metadata.ResourceVersion, Value: raw, Indexes: indexes}
	}
	directories := []string{t.TempDir(), t.TempDir()}
	stores := make([]*checkpoint.DiskStore, 2)
	cancel := make([]context.CancelFunc, 2)
	done := make([]chan error, 2)
	stop := func(replica int) {
		if cancel[replica] == nil {
			return
		}
		cancel[replica]()
		select {
		case err := <-done[replica]:
			require.NoError(t, err)
		case <-time.After(3 * time.Second):
			t.Fatal("Translation replica failed to stop bounded work")
		}
		cancel[replica] = nil
	}
	t.Cleanup(func() {
		for replica := range stores {
			stop(replica)
			if stores[replica] != nil {
				require.NoError(t, stores[replica].Close())
			}
		}
	})
	start := func(replica int) {
		gate := cache.New[translation.Resource]()
		gate.MarkSynced()
		runner := &listwatch.Runner[translation.Resource]{
			Kind: key.Kind, Cache: gate, Disk: stores[replica],
			KeyFunc: func(resource translation.Resource) types.WorkItemKey {
				return types.WorkItemKey{Kind: key.Kind, Namespace: resource.Metadata.Namespace, Name: resource.Metadata.Name}
			},
			RevisionFunc: func(resource translation.Resource) string { return resource.Metadata.ResourceVersion },
		}
		reconciler := translation.NewReconcilerWithLookup(runner.Lookup, client, client)
		mgr := manager.New()
		require.NoError(t, mgr.Register(manager.ReconcilerRegistration{
			Kind: key.Kind, Cache: gate, Disk: stores[replica], Reconciler: reconciler,
			WorkerCount: 1, MaxAttempts: 1, StallThreshold: time.Minute,
		}))
		ctx, cancelReplica := context.WithCancel(t.Context())
		cancel[replica], done[replica] = cancelReplica, make(chan error, 1)
		go func() { done[replica] <- mgr.Start(ctx) }()
	}
	for replica := range stores {
		stores[replica], err = checkpoint.OpenDiskStore(directories[replica], key.Kind)
		require.NoError(t, err)
		generation, err := stores[replica].BeginSnapshot(t.Context())
		require.NoError(t, err)
		require.NoError(t, stores[replica].PutSnapshotPage(t.Context(), generation, []checkpoint.DiskItem{item()}))
		require.NoError(t, stores[replica].FinishSnapshot(t.Context(), generation, "translation-journal@start"))
	}
	for replica := range stores {
		start(replica)
	}
	require.Eventually(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return conflicts >= 2 && stores[0].Counts().Pending+stores[1].Counts().Pending == 1
	}, 5*time.Second, 5*time.Millisecond)
	loser := 0
	if stores[0].Counts().Pending == 0 {
		loser = 1
	}
	stop(loser)
	schedule, err := stores[loser].WorkSchedule(t.Context(), key)
	require.NoError(t, err)
	require.Positive(t, schedule.Due)
	require.True(t, schedule.QuarantinedAt.IsZero())
	require.NoError(t, stores[loser].Close())
	stores[loser], err = checkpoint.OpenDiskStore(directories[loser], key.Kind)
	require.NoError(t, err)
	restored, err := stores[loser].WorkSchedule(t.Context(), key)
	require.NoError(t, err)
	require.Equal(t, schedule, restored, "replacement must preserve the captured retry deadline and obligation")
	mu.Lock()
	before := conflicts
	mu.Unlock()
	start(loser)
	require.Eventually(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return conflicts > before
	}, 3*time.Second, time.Millisecond)
	require.EqualValues(t, 1, stores[loser].Counts().Pending)
	require.NoError(t, stores[loser].Apply(t.Context(), item(), false, false, "translation-journal@refreshed"))
	require.Eventually(t, func() bool { return stores[loser].Counts().Pending == 0 }, 3*time.Second, time.Millisecond)
	stop(loser)

	unknown := translation.TargetObservation{Target: target, ResourceVersion: "7", Generation: 1}
	require.NoError(t, translation.QueueTargetChange(t.Context(), stores[loser], &unknown, &unknown))
	progress, err := stores[loser].ProcessFanout(t.Context())
	require.NoError(t, err)
	require.True(t, progress)
	require.EqualValues(t, 1, stores[loser].Counts().Pending)
	require.NoError(t, stores[loser].Close())
	stores[loser], err = checkpoint.OpenDiskStore(directories[loser], key.Kind)
	require.NoError(t, err)
	require.EqualValues(t, 1, stores[loser].Counts().Pending)
	start(loser)
	require.Eventually(t, func() bool { return stores[loser].Counts().Pending == 0 }, 3*time.Second, time.Millisecond)
	stop(loser)
	mu.Lock()
	applied, bound, settled := writes, binds, source
	mu.Unlock()
	require.Equal(t, 1, applied, "replacement workers must settle against redacted current status")
	require.Equal(t, 1, bound, "sticky binding must survive replica and process replacement")
	require.Equal(t, "accepted-source", settled.Status.LastAppliedRevision)
	require.Equal(t, "exact source\r\n", settled.Body)
	require.Equal(t, "TRUE", settled.Status.Conditions[0].Status)
	for _, store := range stores {
		require.Zero(t, store.Counts().Poison)
	}
}
