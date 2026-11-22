// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package main

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gitstore-dev/gitstore/controller-manager/internal/cache"
	"github.com/gitstore-dev/gitstore/controller-manager/internal/categorytaxonomy"
	"github.com/gitstore-dev/gitstore/controller-manager/internal/checkpoint"
	"github.com/gitstore-dev/gitstore/controller-manager/internal/config"
	"github.com/gitstore-dev/gitstore/controller-manager/internal/graphqlclient"
	"github.com/gitstore-dev/gitstore/controller-manager/internal/listwatch"
	"github.com/gitstore-dev/gitstore/controller-manager/internal/manager"
	namespacecontroller "github.com/gitstore-dev/gitstore/controller-manager/internal/namespace"
	rbaccontroller "github.com/gitstore-dev/gitstore/controller-manager/internal/rbac"
	repositorycontroller "github.com/gitstore-dev/gitstore/controller-manager/internal/repository"
	"github.com/gitstore-dev/gitstore/controller-manager/internal/secret"
	"github.com/gitstore-dev/gitstore/controller-manager/internal/status"
	translationcontroller "github.com/gitstore-dev/gitstore/controller-manager/internal/translation"
	"github.com/gitstore-dev/gitstore/controller-manager/internal/types"
	"github.com/gitstore-dev/gitstore/secretmaterial"
	"github.com/gitstore-dev/gitstore/translation"
	"github.com/stretchr/testify/require"
	"github.com/syndtr/goleveldb/leveldb"
	"github.com/syndtr/goleveldb/leveldb/opt"
	"go.uber.org/zap"
)

func TestRBACNativeProcess(t *testing.T) {
	path := os.Getenv("GITSTORE_RBAC_NATIVE_TEST_CONFIG")
	if path == "" {
		t.Skip("owned native RBAC process fixture not selected")
	}
	require.Equal(t, "1", os.Getenv("GITSTORE_RBAC_NATIVE_TEST_OWNED"), "explicit ownership acknowledgement is required")
	args := os.Args
	os.Args = []string{"owned-rbac-controller", "--config-file", path}
	defer func() { os.Args = args }()
	run(true)
	if receipt := os.Getenv("GITSTORE_RBAC_NATIVE_TEST_CHECKPOINT_RECEIPT"); receipt != "" {
		dir := os.Getenv("GITSTORE_CONTROLLER__CHECKPOINT__DIR")
		require.True(t, filepath.IsAbs(dir))
		db, err := leveldb.OpenFile(filepath.Join(dir, "Namespace.disk-v2"), &opt.Options{ReadOnly: true})
		require.NoError(t, err, "inspect only after the process has closed its checkpoint writer")
		defer func() { require.NoError(t, db.Close()) }()
		schema, err := db.Get([]byte("schema"), nil)
		require.NoError(t, err)
		require.Equal(t, "2", string(schema))
		state := make(map[string]any)
		for _, key := range []string{"active", "generation", "staging"} {
			value, err := db.Get([]byte(key), nil)
			if errors.Is(err, leveldb.ErrNotFound) {
				state[key] = uint64(0)
				continue
			}
			require.NoError(t, err)
			require.Len(t, value, 8)
			state[key] = binary.BigEndian.Uint64(value)
		}
		cursor, err := db.Get([]byte("cursor"), nil)
		require.NoError(t, err)
		state["hasCursor"] = len(cursor) > 0
		data, err := json.Marshal(state)
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(receipt, data, 0600))
	}
}

func TestRBACPrimaryGroupPersistsWorkAcrossIndependentReplicaReplacement(t *testing.T) {
	for replica := range 2 {
		t.Run(fmt.Sprintf("replica-%d", replica), func(t *testing.T) {
			dir := t.TempDir()
			cfg := &config.Config{Controller: config.ControllerConfig{Checkpoint: config.CheckpointConfig{Dir: dir}}}
			ctx, cancel := context.WithCancel(t.Context())
			cancel()
			mgr := manager.New()
			var runners sync.WaitGroup
			client := graphqlclient.New("http://unused.invalid/admin/graphql", graphqlclient.NewStaticToken("enrolled"))
			closeStores, err := registerDiskControllersWithRBAC(ctx, &runners, mgr, cfg, zap.NewNop(), client, true)
			require.NoError(t, err)
			runners.Wait()
			for _, kind := range []string{"Role", "RoleBinding", "ClusterRole", "ClusterRoleBinding", rbaccontroller.NamespaceCleanupWorkKind} {
				namespace := ""
				if kind == "Role" || kind == "RoleBinding" {
					namespace = "shop"
				}
				key := types.WorkItemKey{Kind: kind, Namespace: namespace, Name: "pending"}
				require.True(t, mgr.KindStats()[kind].Registered)
				require.NoError(t, mgr.Enqueue(key))
			}
			require.NoError(t, closeStores())
			for _, kind := range []string{"Role", "RoleBinding", "ClusterRole", "ClusterRoleBinding", rbaccontroller.NamespaceCleanupWorkKind} {
				store, err := checkpoint.OpenDiskStore(dir, kind)
				require.NoError(t, err)
				work, err := store.Pending(t.Context(), "", 10)
				require.NoError(t, err)
				require.Len(t, work, 1)
				require.Equal(t, kind, work[0].Key.Kind)
				if kind == rbaccontroller.NamespaceCleanupWorkKind {
					progressed, err := store.ProcessFanout(t.Context())
					require.NoError(t, err, "an idle work-only poller must not require a resource snapshot")
					require.False(t, progressed)
				}
				require.NoError(t, store.Close())
			}
		})
	}
}

type ownerCatchupSource struct {
	pages [][]namespacecontroller.Namespace
	lists atomic.Int64
}

func (*ownerCatchupSource) List(context.Context) (listwatch.ListResponse[namespacecontroller.Namespace], error) {
	return listwatch.ListResponse[namespacecontroller.Namespace]{}, errors.New("owner catch-up must use streaming pages")
}

func (s *ownerCatchupSource) ListPages(_ context.Context, visit func([]namespacecontroller.Namespace) error) (string, error) {
	s.lists.Add(1)
	for _, page := range s.pages {
		if err := visit(page); err != nil {
			return "", err
		}
	}
	return "rwv1:test:1", nil
}

type ownerCatchupWatch struct {
	events chan listwatch.WatchEvent[namespacecontroller.Namespace]
}

func (w *ownerCatchupWatch) Events() <-chan listwatch.WatchEvent[namespacecontroller.Namespace] {
	return w.events
}
func (*ownerCatchupWatch) Err() error { return nil }
func (*ownerCatchupWatch) Stop()      {}

func (*ownerCatchupSource) Watch(context.Context, string) (listwatch.Watcher[namespacecontroller.Namespace], error) {
	watch := &ownerCatchupWatch{events: make(chan listwatch.WatchEvent[namespacecontroller.Namespace], 1)}
	watch.events <- listwatch.WatchEvent[namespacecontroller.Namespace]{Type: listwatch.Bookmark, ResourceVersion: "rwv1:test:2"}
	return watch, nil
}

func TestExistingUSERNamespaceCatchupPagesAndPendingWorkSurviveReplacement(t *testing.T) {
	for range 2 {
		root := t.TempDir()
		source := &ownerCatchupSource{pages: [][]namespacecontroller.Namespace{
			{{UID: "namespace-alice", Name: "alice", Tier: "USER", OwnerKind: "USER", OwnerSubject: "staff-subject-alice", ResourceVersion: "1"}},
			{{UID: "namespace-bob", Name: "bob", Tier: "USER", OwnerKind: "USER", OwnerSubject: "staff-subject-bob", ResourceVersion: "1"}},
		}}
		for range 2 {
			store, err := checkpoint.OpenDiskStore(root, "Namespace")
			require.NoError(t, err)
			ctx, cancel := context.WithCancel(t.Context())
			runner := newDiskRunner("Namespace", source, store, &config.Config{}, zap.NewNop(),
				func(namespace namespacecontroller.Namespace) types.WorkItemKey {
					return types.WorkItemKey{Kind: "Namespace", Name: namespace.Name}
				}, func(namespace namespacecontroller.Namespace) string { return namespace.ResourceVersion })
			done := make(chan error, 1)
			go func() { defer close(done); done <- runner.Run(ctx) }()
			t.Cleanup(func() { cancel(); <-done; _ = store.Close() })
			require.Eventually(t, func() bool {
				return runner.Cache.HasSynced() && !runner.Cache.RecoveryState().Recovering
			}, 5*time.Second, time.Millisecond)
			work, err := store.Pending(t.Context(), "", 10)
			require.NoError(t, err)
			require.Len(t, work, 2, "cold migration work cannot disappear before owner provisioning succeeds")
			for _, pending := range work {
				namespace, found, err := runner.Lookup(t.Context(), pending.Key)
				require.NoError(t, err)
				require.True(t, found)
				require.Equal(t, "USER", namespace.Tier)
				require.Equal(t, "staff-subject-"+namespace.Name, namespace.OwnerSubject,
					"persist the immutable subject, never replace it with a username")
			}
			require.EqualValues(t, 1, source.lists.Load(), "replacement resumes durable work without another full list")
			cancel()
			select {
			case err := <-done:
				require.ErrorIs(t, err, context.Canceled)
			case <-time.After(5 * time.Second):
				t.Fatal("catch-up runner did not stop before replacement")
			}
			require.NoError(t, store.Close())
		}
	}
}

func TestTranslationSourceIndexesUseOnlyMatchingPrivateBinding(t *testing.T) {
	for _, scenario := range []string{"matching", "source-replaced", "source-updated", "unavailable"} {
		t.Run(scenario, func(t *testing.T) {
			source := translationcontroller.Resource{
				Metadata: translationcontroller.Metadata{UID: "opaque-source-id", Namespace: "shop", Name: "fr", ResourceVersion: "1", Generation: 1},
				Spec:     json.RawMessage(`{"targetRef":{"apiVersion":"catalog.gitstore.dev/v1beta1","kind":"Product","name":"laptop"}}`),
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if scenario == "unavailable" {
					http.Error(w, "unavailable", http.StatusServiceUnavailable)
					return
				}
				binding := map[string]any{"translationID": source.Metadata.UID, "sourceVersion": "1",
					"bindingVersion": "1", "referenceEpoch": "epoch", "namespaceUID": "raw-namespace", "boundUID": "raw-target"}
				if scenario == "source-replaced" {
					binding["translationID"] = "replacement-id"
				}
				if scenario == "source-updated" {
					binding["sourceVersion"] = "2"
				}
				require.NoError(t, json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"translationBinding": binding}}))
			}))
			defer server.Close()
			client := translationcontroller.NewGraphQLClient(graphqlclient.New(server.URL, graphqlclient.NewStaticToken("enrolled")))
			raw, err := json.Marshal(source)
			require.NoError(t, err)
			item := checkpoint.DiskItem{Key: types.WorkItemKey{Kind: "Translation", Namespace: "shop", Name: "fr"}, Version: "1", Value: raw}
			require.NoError(t, prepareTranslationSource(client)(t.Context(), source, &item))
			store, err := checkpoint.OpenDiskStore(t.TempDir(), "Translation")
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, store.Close()) })
			generation, err := store.BeginSnapshot(t.Context())
			require.NoError(t, err)
			require.NoError(t, store.PutSnapshotPage(t.Context(), generation, []checkpoint.DiskItem{item}))
			require.NoError(t, store.FinishSnapshot(t.Context(), generation, "journal@one"))
			work, err := store.Pending(t.Context(), "", 256)
			require.NoError(t, err)
			require.Len(t, work, 1)
			_, err = store.Acknowledge(t.Context(), item.Key, work[0].Token)
			require.NoError(t, err)
			target := translationcontroller.TargetObservation{
				Target: translationcontroller.DependencyTarget{NamespaceUID: "raw-namespace", APIVersion: "catalog.gitstore.dev/v1beta1",
					Kind: "Product", Name: "laptop", UID: "raw-target"},
				ResourceVersion: "1", Generation: 1, ContentDigest: "material",
			}
			require.NoError(t, translationcontroller.QueueTargetChange(t.Context(), store, nil, &target))
			for range 8 {
				progress, err := store.ProcessFanout(t.Context())
				require.NoError(t, err)
				if !progress {
					break
				}
			}
			if scenario == "matching" {
				require.EqualValues(t, 1, store.Counts().Pending)
			} else {
				require.Zero(t, store.Counts().Pending, "stale or unavailable bindings cannot supply raw namespace/UID identity")
				require.NoError(t, translationcontroller.QueueNamespaceChange(t.Context(), store, "shop"))
				for range 8 {
					progress, err := store.ProcessFanout(t.Context())
					require.NoError(t, err)
					if !progress {
						break
					}
				}
				require.EqualValues(t, 1, store.Counts().Pending, "unproved binding identity retains conservative namespace invalidation")
			}
		})
	}
}

type targetProofStub struct{ version string }

func (s *targetProofStub) ObserveTarget(_ context.Context, namespace, apiVersion, kind, name string) (*translationcontroller.TargetObservation, error) {
	return &translationcontroller.TargetObservation{
		Target:          translationcontroller.DependencyTarget{NamespaceUID: "raw-namespace", APIVersion: apiVersion, Kind: kind, Name: name, UID: "raw-" + kind},
		ResourceVersion: s.version, Generation: 1, ContentDigest: "material",
	}, nil
}

func TestAllFiveTranslationTargetHooksAndNamespaceLifetimeUseDurableIndexes(t *testing.T) {
	store, err := checkpoint.OpenDiskStore(t.TempDir(), "Translation")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.Close()) })
	generation, err := store.BeginSnapshot(t.Context())
	require.NoError(t, err)
	kinds := []string{"Product", "ProductVariant", "CategoryTaxonomy", "Collection", "File"}
	var items []checkpoint.DiskItem
	for _, kind := range kinds {
		apiVersion := "catalog.gitstore.dev/v1beta1"
		if kind == "File" {
			apiVersion = "storage.gitstore.dev/v1beta1"
		}
		source := translationcontroller.Resource{
			Metadata: translationcontroller.Metadata{UID: "source-" + kind, Namespace: "shop", Name: kind, ResourceVersion: "1", Generation: 1},
			Spec:     json.RawMessage(fmt.Sprintf(`{"targetRef":{"apiVersion":%q,"kind":%q,"name":"target"}}`, apiVersion, kind)),
		}
		uid := "raw-" + kind
		indexes, err := translationcontroller.SourceDependencyIndexes(source, &translationcontroller.BindingState{
			TranslationID: source.Metadata.UID, SourceVersion: "1", BindingVersion: "1", ReferenceEpoch: "epoch",
			NamespaceUID: "raw-namespace", BoundUID: &uid,
		})
		require.NoError(t, err)
		raw, err := json.Marshal(source)
		require.NoError(t, err)
		items = append(items, checkpoint.DiskItem{
			Key: types.WorkItemKey{Kind: "Translation", Namespace: "shop", Name: kind}, Version: "1", Value: raw, Indexes: indexes,
		})
	}
	require.NoError(t, store.PutSnapshotPage(t.Context(), generation, items))
	require.NoError(t, store.FinishSnapshot(t.Context(), generation, "translation-journal@one"))
	clear := func() {
		t.Helper()
		work, err := store.Pending(t.Context(), "", 256)
		require.NoError(t, err)
		for _, pending := range work {
			ok, err := store.Acknowledge(t.Context(), pending.Key, pending.Token)
			require.NoError(t, err)
			require.True(t, ok)
		}
	}
	drain := func() {
		t.Helper()
		for range 64 {
			progress, err := store.ProcessFanout(t.Context())
			require.NoError(t, err)
			if !progress {
				return
			}
		}
		t.Fatal("target handoff did not finish its bounded tasks")
	}
	clear()
	ns := &listwatch.Runner[namespacecontroller.Namespace]{}
	product := &listwatch.Runner[categorytaxonomy.Product]{}
	category := &listwatch.Runner[categorytaxonomy.CategoryTaxonomy]{}
	var targets []*listwatch.Runner[listwatch.TargetMetadataResource]
	for _, kind := range []string{"ProductVariant", "Collection", "File"} {
		targets = append(targets, &listwatch.Runner[listwatch.TargetMetadataResource]{Kind: kind})
	}
	proof := &targetProofStub{version: "1"}
	wireTranslationTargetHandoffs(translationcontroller.NewTargetHandoff(store, proof), store, ns, product, category, targets)
	productBefore := categorytaxonomy.Product{Namespace: "shop", Name: "target", UID: "opaque-product-id", ResourceVersion: "1"}
	categoryBefore := categorytaxonomy.CategoryTaxonomy{Namespace: "shop", Name: "target", UID: "opaque-category-id", ResourceVersion: "1"}
	require.NoError(t, product.BeforeDiskChange(t.Context(), nil, &productBefore))
	drain()
	work, err := store.Pending(t.Context(), "", 256)
	require.NoError(t, err)
	require.Len(t, work, 1)
	require.Equal(t, "Product", work[0].Key.Name)
	clear()
	require.NoError(t, category.BeforeDiskChange(t.Context(), nil, &categoryBefore))
	drain()
	work, err = store.Pending(t.Context(), "", 256)
	require.NoError(t, err)
	require.Len(t, work, 1)
	require.Equal(t, "CategoryTaxonomy", work[0].Key.Name)
	clear()
	for _, runner := range targets {
		target := listwatch.TargetMetadataResource{Kind: runner.Kind,
			Metadata: translationcontroller.Metadata{Namespace: "shop", Name: "target", UID: "opaque-" + runner.Kind, ResourceVersion: "1"}}
		require.NoError(t, runner.BeforeDiskChange(t.Context(), nil, &target))
		drain()
		work, err := store.Pending(t.Context(), "", 256)
		require.NoError(t, err)
		require.Len(t, work, 1)
		require.Equal(t, runner.Kind, work[0].Key.Name)
		clear()
		proof.version = "2"
		next := target
		next.Metadata.ResourceVersion = "2"
		require.NoError(t, runner.BeforeDiskChange(t.Context(), &target, &next))
		drain()
		require.Zero(t, store.Counts().Pending, "a status-only target event must not invalidate an unpinned source")
		proof.version = "1"
	}
	before := namespacecontroller.Namespace{UID: "old-namespace", Name: "shop"}
	after := namespacecontroller.Namespace{UID: "new-namespace", Name: "shop"}
	require.NoError(t, ns.BeforeDiskChange(t.Context(), &before, &after))
	drain()
	require.EqualValues(t, len(kinds), store.Counts().Pending, "namespace recreation must include old-incarnation dependencies")
	_, cursor, err := store.Active(t.Context())
	require.NoError(t, err)
	require.Equal(t, "translation-journal@one", cursor)
}

func TestTranslationRegistrationRejectsIncompatibleConfigurationBeforeDiskEffects(t *testing.T) {
	for _, activation := range []config.TranslationConfig{
		{Enabled: true},
		{Enabled: true, DescriptorDigest: "unsupported"},
		{DescriptorDigest: "unsupported"},
	} {
		dir := filepath.Join(t.TempDir(), "checkpoints")
		cfg := &config.Config{Controller: config.ControllerConfig{
			Checkpoint: config.CheckpointConfig{Dir: dir}, Translation: activation,
		}}
		mgr := manager.New()
		var runners sync.WaitGroup
		closeStores, err := registerDiskControllersWithRBAC(t.Context(), &runners, mgr, cfg, zap.NewNop(), nil, false)
		require.ErrorContains(t, err, "controller.translation")
		require.Nil(t, closeStores)
		require.Empty(t, mgr.KindStats())
		_, err = os.Stat(dir)
		require.ErrorIs(t, err, os.ErrNotExist, "invalid configuration must not create checkpoint state")
	}
}

func TestTranslationRegistrationUsesConfiguredCapabilityAndIndependentDiskState(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		for replica := range 2 {
			t.Run(fmt.Sprintf("enabled=%t/replica=%d", enabled, replica), func(t *testing.T) {
				dir := t.TempDir()
				cfg := &config.Config{Controller: config.ControllerConfig{Checkpoint: config.CheckpointConfig{Dir: dir}}}
				cfg.Controller.Translation = config.TranslationConfig{Enabled: enabled, DescriptorDigest: translation.CapabilityDigest()}
				ctx, cancel := context.WithCancel(t.Context())
				cancel()
				mgr := manager.New()
				var runners sync.WaitGroup
				client := graphqlclient.New("http://unused.invalid/admin/graphql", graphqlclient.NewStaticToken("enrolled"))
				closeStores, err := registerDiskControllersWithRBAC(ctx, &runners, mgr, cfg, zap.NewNop(), client, false)
				require.NoError(t, err)
				runners.Wait()
				for _, kind := range []string{"Translation", "ProductVariant", "Collection", "File"} {
					require.Equal(t, enabled, mgr.KindStats()[kind].Registered)
				}
				key := types.WorkItemKey{Kind: "Translation", Namespace: "shop", Name: "pending"}
				if enabled {
					require.NoError(t, mgr.Enqueue(key))
				} else {
					require.ErrorIs(t, mgr.Enqueue(key), types.ErrKindNotRegistered)
				}
				require.NoError(t, closeStores())
				if !enabled {
					_, err := os.Stat(filepath.Join(dir, "Translation.disk-v2"))
					require.ErrorIs(t, err, os.ErrNotExist)
					return
				}
				store, err := checkpoint.OpenDiskStore(dir, "Translation")
				require.NoError(t, err)
				work, err := store.Pending(t.Context(), "", 10)
				require.NoError(t, err)
				require.Len(t, work, 1)
				require.Equal(t, key, work[0].Key)
				require.False(t, store.Stats().OldestPending.IsZero(), "Translation registration must retain pending age")
				require.Zero(t, store.Stats().PendingWithoutTimestamp)
				second, err := checkpoint.OpenDiskStore(dir, "Translation")
				require.Error(t, err, "replicas must never overlap writable checkpoint ownership")
				require.Nil(t, second)
				require.NoError(t, store.Close())
			})
		}
	}
}

func TestProductionRegistrationUsesDiskForEveryKind(t *testing.T) {
	dir := t.TempDir()
	cfg := &config.Config{Controller: config.ControllerConfig{Checkpoint: config.CheckpointConfig{Dir: dir}}}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	mgr := manager.New()
	var runners sync.WaitGroup
	client := graphqlclient.New("http://unused.invalid/admin/graphql", graphqlclient.NewStaticToken("test"))
	closeStores, err := registerDiskControllers(ctx, &runners, mgr, cfg, zap.NewNop(), client)
	require.NoError(t, err)
	t.Cleanup(func() { _ = closeStores() })
	runners.Wait()
	keys := []types.WorkItemKey{
		{Kind: "Namespace", Name: "probe"},
		{Kind: "Repository", Namespace: "shop", Name: "probe"},
		{Kind: "CategoryTaxonomy", Namespace: "shop", Name: "probe"},
		{Kind: "Product", Namespace: "shop", Name: "probe"},
	}
	for _, key := range keys {
		require.NoError(t, mgr.Enqueue(key))
		require.True(t, mgr.KindStats()[key.Kind].Registered)
	}
	require.NoError(t, closeStores())
	for _, key := range keys {
		store, err := checkpoint.OpenDiskStore(dir, key.Kind)
		require.NoError(t, err)
		pending, err := store.Pending(t.Context(), "", 1)
		require.NoError(t, err)
		require.Len(t, pending, 1)
		require.Equal(t, key, pending[0].Key, "production registration did not persist its work")
		require.NoError(t, store.Close())
	}
}

func TestPoisonRequeueSupportsClusterScopedDiskKeys(t *testing.T) {
	store, err := checkpoint.OpenDiskStore(t.TempDir(), "Namespace")
	require.NoError(t, err)
	defer store.Close()
	key := types.WorkItemKey{Kind: "Namespace", Name: "example"}
	require.NoError(t, store.Enqueue(t.Context(), key))
	pending, err := store.Pending(t.Context(), "", 1)
	require.NoError(t, err)
	require.Len(t, pending, 1)
	ok, err := store.DeferWork(t.Context(), key, pending[0].Token, 0, "test failure", 1)
	require.NoError(t, err)
	require.True(t, ok)
	mgr := manager.New()
	require.NoError(t, mgr.Register(manager.ReconcilerRegistration{
		Kind: "Namespace", Disk: store,
		Cache: cache.New[string](),
		Reconciler: namespacecontroller.NewReconcilerWithLookup(
			func(context.Context, types.WorkItemKey) (namespacecontroller.Namespace, bool, error) {
				return namespacecontroller.Namespace{}, false, nil
			}, nil, nil, nil),
	}))
	response := httptest.NewRecorder()
	buildMux(mgr).ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/controller/v1/poison/Namespace/example/requeue", nil))
	require.Equal(t, http.StatusNoContent, response.Code)
	require.False(t, mgr.IsQuarantined(key))
}

func TestParseConfigFile(t *testing.T) {
	paths, err := parseConfigFiles([]string{"--config-file", "/config/shared.toml"})
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) != 1 || paths[0] != "/config/shared.toml" {
		t.Fatalf("paths = %v", paths)
	}
}

func TestParseConfigFilesRepeated(t *testing.T) {
	paths, err := parseConfigFiles([]string{
		"--config-file", "/config/shared.toml",
		"--config-file", "/config/overlay.toml",
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"/config/shared.toml", "/config/overlay.toml"}
	if len(paths) != len(want) || paths[0] != want[0] || paths[1] != want[1] {
		t.Fatalf("paths = %v", paths)
	}
}

func TestBuildCredentialSourceUsesResolvedServiceAccountKey(t *testing.T) {
	_, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(privateKey)
	if err != nil {
		t.Fatal(err)
	}
	ref := secret.Ref{Kind: "SecretRef", Name: "controller-manager"}
	provider := secret.BootstrapProviderConfig{Type: secret.ProviderEnvironment, EnvVariable: "TEST_CONTROLLER_SIGNING_RECORD"}
	record, err := json.Marshal(map[string]any{
		"format": "serviceaccount-signing-key/v1",
		"values": map[string]string{
			"privateKey": base64.StdEncoding.EncodeToString(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})),
			"keyID":      base64.StdEncoding.EncodeToString([]byte("key-1")),
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv(provider.EnvVariable, string(record))

	source, err := buildCredentialSource(context.Background(), &config.Config{
		Controller: config.ControllerConfig{
			ApiURI: "http://api.example.test/admin/graphql",
			ServiceAccount: config.ServiceAccountConfig{
				Name:   "gitstore-controller-manager",
				UID:    "sa-uid-1",
				KeyRef: ref,
			},
			SecretProviders: config.SecretProvidersConfig{Bootstrap: provider},
		},
	}, zap.NewNop())
	if err != nil {
		t.Fatalf("buildCredentialSource() error: %v", err)
	}
	if _, ok := source.(*graphqlclient.ServiceAccountSource); !ok {
		t.Errorf("source = %T, want *graphqlclient.ServiceAccountSource", source)
	}
	if readiness := credentialReadiness(source); readiness == nil || readiness.Ready() {
		t.Error("dynamic source must begin not ready before acquiring a token")
	}
}

func TestBuildCredentialSourceRejectsMissingCredentialConfiguration(t *testing.T) {
	if _, err := buildCredentialSource(context.Background(), &config.Config{}, zap.NewNop()); err == nil {
		t.Fatal("buildCredentialSource() error = nil")
	}
}

func TestBuildCredentialSourceFailsClosedOnInvalidRecords(t *testing.T) {
	record := func(format string, values map[string]string) []byte {
		t.Helper()
		encoded := make(map[string]string, len(values))
		for name, value := range values {
			encoded[name] = base64.StdEncoding.EncodeToString([]byte(value))
		}
		data, err := json.Marshal(map[string]any{"format": format, "values": encoded})
		if err != nil {
			t.Fatal(err)
		}
		return data
	}
	for _, tc := range []struct {
		name string
		data []byte
		want error
	}{
		{"absent", nil, secretmaterial.ErrNotFound},
		{"missing-id", record("serviceaccount-signing-key/v1", map[string]string{"privateKey": "MUST-NOT-LEAK"}), secretmaterial.ErrMissingKey},
		{"malformed-key", record("serviceaccount-signing-key/v1", map[string]string{"privateKey": "MUST-NOT-LEAK", "keyID": "id"}), secretmaterial.ErrInvalidRef},
		{"oversized-key", record("serviceaccount-signing-key/v1", map[string]string{"privateKey": strings.Repeat("x", secretmaterial.MaxItemBytes+1), "keyID": "id"}), secretmaterial.ErrValueTooLarge},
		{"runtime-record", record("secret-record/v1", map[string]string{"privateKey": "MUST-NOT-LEAK", "keyID": "id"}), secretmaterial.ErrUnsupportedType},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			if tc.data != nil {
				if err := os.WriteFile(filepath.Join(root, "controller.json"), tc.data, 0600); err != nil {
					t.Fatal(err)
				}
			}
			cfg := &config.Config{Controller: config.ControllerConfig{
				ApiURI: "http://must-not-call.invalid",
				ServiceAccount: config.ServiceAccountConfig{
					Name: "manager", UID: "uid",
					KeyRef: secret.Ref{Kind: "SecretRef", Name: "controller"},
				},
				SecretProviders: config.SecretProvidersConfig{Bootstrap: secret.BootstrapProviderConfig{
					Type: "file", BasePath: root,
				}},
			}}
			source, err := buildCredentialSource(context.Background(), cfg, zap.NewNop())
			if source != nil || !errors.Is(err, tc.want) {
				t.Fatalf("invalid bootstrap produced a source or wrong classification: %v", err)
			}
			if strings.Contains(err.Error(), "MUST-NOT-LEAK") {
				t.Fatal("bootstrap diagnostic leaked private material")
			}
		})
	}
}

type repositoryRegistrationWatch struct {
	events chan listwatch.WatchEvent[repositorycontroller.Repository]
	once   sync.Once
}

func (w *repositoryRegistrationWatch) Events() <-chan listwatch.WatchEvent[repositorycontroller.Repository] {
	return w.events
}
func (w *repositoryRegistrationWatch) Err() error { return nil }
func (w *repositoryRegistrationWatch) Stop() {
	w.once.Do(func() { close(w.events) })
}

type repositoryRegistrationListWatcher struct {
	item repositorycontroller.Repository
}

func (w *repositoryRegistrationListWatcher) List(context.Context) (listwatch.ListResponse[repositorycontroller.Repository], error) {
	return listwatch.ListResponse[repositorycontroller.Repository]{
		Items:           []repositorycontroller.Repository{w.item},
		ResourceVersion: "rwv1:test:1",
	}, nil
}

func (w *repositoryRegistrationListWatcher) Watch(ctx context.Context, _ string) (listwatch.Watcher[repositorycontroller.Repository], error) {
	watch := &repositoryRegistrationWatch{events: make(chan listwatch.WatchEvent[repositorycontroller.Repository], 1)}
	watch.events <- listwatch.WatchEvent[repositorycontroller.Repository]{Type: listwatch.Bookmark, ResourceVersion: "rwv1:test:2"}
	go func() {
		<-ctx.Done()
		watch.Stop()
	}()
	return watch, nil
}

// TestRegisterRepositoryAcrossTwoControllerManagers exercises the production
// registration boundary twice: two independent managers, runners, caches,
// queues, and checkpoint stores consume the same Repository snapshot and call
// the shared idempotent API provisioning/status contracts. This is deliberately
// stronger than calling GraphQLStorageClient twice in isolation.
func TestRegisterRepositoryAcrossTwoControllerManagers(t *testing.T) {
	var provisionCalls atomic.Int64
	var statusCalls atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer controller-token" {
			http.Error(w, "missing controller authorization", http.StatusUnauthorized)
			return
		}
		var request struct {
			Query string `json:"query"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.Contains(request.Query, "provisionRepositoryStorage"):
			provisionCalls.Add(1)
			_, _ = w.Write([]byte(`{"data":{"provisionRepositoryStorage":{"repository":{"metadata":{"namespace":"acme","name":"catalog"}}}}}`))
		case strings.Contains(request.Query, "updateRepositoryStatus"):
			statusCalls.Add(1)
			_, _ = w.Write([]byte(`{"data":{"updateRepositoryStatus":{"repository":{"metadata":{"resourceVersion":"2"}}}}}`))
		default:
			http.Error(w, "unexpected GraphQL operation", http.StatusBadRequest)
		}
	}))
	defer server.Close()

	item := repositorycontroller.Repository{
		UID: "repository-1", Namespace: "acme", Name: "catalog", StorageClass: "standard",
		Generation: 1, ResourceVersion: "1",
		Status: status.ResourceStatus{Conditions: []*status.Condition{{
			Type: "AdmissionAccepted", Status: "TRUE", ObservedGeneration: 1,
		}}},
	}
	cfg := &config.Config{Controller: config.ControllerConfig{
		Reconcile:  config.ReconcileConfig{MaxAttempts: 1, StallThreshold: time.Minute},
		Checkpoint: config.CheckpointConfig{FlushIntervalEvents: 1},
		Watch:      config.WatchConfig{MaxBackoff: time.Millisecond},
	}}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 2)
	// runners is waited on before the test returns so each Runner's final
	// checkpoint flush finishes before t.TempDir cleanup removes its directory.
	var runners sync.WaitGroup
	var checkpointRoots []string
	for replica := 0; replica < 2; replica++ {
		mgr := manager.New().WithLogger(zap.NewNop())
		root := t.TempDir()
		checkpointRoots = append(checkpointRoots, root)
		store, err := checkpoint.NewFilesystemStore(root)
		if err != nil {
			t.Fatalf("replica %d checkpoint store: %v", replica, err)
		}
		client := graphqlclient.New(server.URL, graphqlclient.NewStaticToken("controller-token"))
		if _, err := registerRepository(
			ctx, &runners, mgr, store, cfg, zap.NewNop(), client,
			&repositoryRegistrationListWatcher{item: item},
			repositorycontroller.NewGraphQLStorageClient(client),
		); err != nil {
			t.Fatalf("replica %d register Repository: %v", replica, err)
		}
		if stat := mgr.KindStats()["Repository"]; !stat.Registered {
			t.Fatalf("replica %d Repository registration missing", replica)
		}
		go func() { done <- mgr.Start(ctx) }()
	}

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) && (provisionCalls.Load() < 2 || statusCalls.Load() < 2) {
		time.Sleep(10 * time.Millisecond)
	}
	if got := provisionCalls.Load(); got != 2 {
		t.Fatalf("provision calls = %d, want one from each registered controller manager", got)
	}
	if got := statusCalls.Load(); got != 2 {
		t.Fatalf("status calls = %d, want one from each registered controller manager", got)
	}

	cancel()
	for replica := 0; replica < 2; replica++ {
		select {
		case err := <-done:
			if err != nil {
				t.Fatalf("replica %d manager shutdown: %v", replica, err)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("replica %d manager did not stop", replica)
		}
	}
	runners.Wait()
	for _, root := range checkpointRoots {
		replacement, err := checkpoint.NewFilesystemStore(root)
		if err != nil {
			t.Fatal(err)
		}
		rec, err := replacement.Load(context.Background(), "Repository")
		if err != nil {
			t.Fatalf("final checkpoint not available to replacement: %v", err)
		}
		var restored []repositorycontroller.Repository
		if err := json.Unmarshal(rec.Snapshot, &restored); err != nil {
			t.Fatal(err)
		}
		if rec.ResourceVersion != "rwv1:test:2" || len(restored) != 1 || restored[0].UID != item.UID {
			t.Fatal("shutdown flush did not retain the replacement snapshot and cursor")
		}
	}
}
