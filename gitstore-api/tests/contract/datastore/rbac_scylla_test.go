// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

//go:build scylla

package datastore_contract_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	"github.com/gitstore-dev/gitstore/api/internal/datastore/scylla/migrations"
	"github.com/gitstore-dev/gitstore/api/internal/rbac"
	"github.com/gitstore-dev/gitstore/api/internal/watchjournal"
	"github.com/google/uuid"
	scyllacdc "github.com/scylladb/scylla-cdc-go"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

type retainedRawRBACCapture struct {
	datastore.RBACStore
	state    datastore.RBACTierState
	payloads [][]byte
}

func (s retainedRawRBACCapture) CaptureRBACSnapshot(context.Context, string, int64) (datastore.RBACTierState, [][]byte, error) {
	return s.state, s.payloads, nil
}

func TestRBACNativeRawCaptureRemainsCompleteAfterLaterWrites(t *testing.T) {
	ds := newScyllaDatastoreInKeyspace(t, newScyllaTestKeyspace(t))
	store := rbacStore(t, datastore.NewInstrumentedDatastore(ds, "scylla", zap.NewNop()))
	tier := uuid.NewString()
	role := rbacRole(tier, "captured")
	role.Metadata.Annotations = map[string]string{"padding": strings.Repeat("x", 16000)}
	state, err := store.ApplyRBAC(t.Context(), rbacCreate(tier, rbac.TierState{}, 1, role))
	require.NoError(t, err)
	capturer, ok := store.(datastore.RBACSnapshotCapturer)
	require.True(t, ok)
	captured, payloads, err := capturer.CaptureRBACSnapshot(t.Context(), tier, rbac.MaxSnapshotBytes)
	require.NoError(t, err)
	require.Equal(t, state, captured)
	current, err := store.GetRBACByUID(t.Context(), role.Metadata.UID)
	require.NoError(t, err)
	current.Status = json.RawMessage(`{"observedGeneration":1}`)
	latest, err := store.ApplyRBAC(t.Context(), rbac.TierWrite{
		TierID: tier, ExpectedRevision: state.Revision,
		Changes: []rbac.Change{{Resource: current, ExpectedUID: current.Metadata.UID, ExpectedResourceVersion: current.Metadata.ResourceVersion}},
	})
	require.NoError(t, err)
	require.Greater(t, latest.Revision, captured.Revision)
	snapshot, err := datastore.ReadRBACSnapshot(t.Context(), retainedRawRBACCapture{state: captured, payloads: payloads}, tier)
	require.NoError(t, err)
	require.Equal(t, captured, snapshot.State)
	require.Len(t, snapshot.Resources, 1)
	require.Equal(t, fmt.Sprint(captured.Revision), snapshot.Resources[0].Metadata.ResourceVersion)
	require.Empty(t, snapshot.Resources[0].Status)
	live, err := datastore.ReadRBACSnapshot(t.Context(), store, tier)
	require.NoError(t, err)
	require.Equal(t, latest, live.State)
	require.JSONEq(t, string(current.Status), string(live.Resources[0].Status))
	_, _, err = capturer.CaptureRBACSnapshot(t.Context(), tier, 1)
	require.ErrorIs(t, err, rbac.ErrUnavailable)
}

func TestRBACNativeRawCaptureDuringReplicaMetadataWrites(t *testing.T) {
	for _, count := range []int{1, 96} {
		t.Run(fmt.Sprintf("rows=%d", count), func(t *testing.T) {
			testRBACNativeRawCaptureDuringReplicaMetadataWrites(t, count)
		})
	}
}

func testRBACNativeRawCaptureDuringReplicaMetadataWrites(t *testing.T, count int) {
	t.Helper()
	first, second := newScyllaDatastores(t)
	writer, reader := rbacStore(t, first), rbacStore(t, second)
	tier := uuid.NewString()
	roles := make([]*rbac.Resource, count)
	for i := range roles {
		roles[i] = rbacRole(tier, fmt.Sprintf("changing-%03d", i))
		if count > 1 {
			roles[i].Metadata.Annotations = map[string]string{"padding": strings.Repeat("x", 16000)}
		}
	}
	role := roles[0]
	state, err := writer.ApplyRBAC(t.Context(), rbacCreate(tier, rbac.TierState{}, 1, roles...))
	require.NoError(t, err)
	capturer, ok := reader.(datastore.RBACSnapshotCapturer)
	require.True(t, ok)
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	started := make(chan struct{})
	go func() {
		close(started)
		for i := range 100 {
			current, err := writer.GetRBACByUID(ctx, role.Metadata.UID)
			if err != nil {
				done <- err
				return
			}
			current.Status = nil
			if i%2 == 0 {
				current.Status = json.RawMessage(`{"observedGeneration":1}`)
			}
			state, err = writer.ApplyRBAC(ctx, rbac.TierWrite{
				TierID: tier, ExpectedRevision: state.Revision,
				Changes: []rbac.Change{{Resource: current, ExpectedUID: current.Metadata.UID, ExpectedResourceVersion: current.Metadata.ResourceVersion}},
			})
			if err != nil {
				done <- err
				return
			}
		}
		done <- nil
	}()
	defer func() {
		cancel()
		select {
		case err := <-done:
			require.True(t, err == nil || errors.Is(err, context.Canceled), "writer: %v", err)
		case <-time.After(5 * time.Second):
			t.Error("owned metadata writer did not stop")
		}
	}()
	<-started
	for range 50 {
		captured, payloads, err := capturer.CaptureRBACSnapshot(t.Context(), tier, rbac.MaxSnapshotBytes)
		require.NoError(t, err, "one atomic partition read must not conflict with concurrent metadata updates")
		snapshot, err := datastore.ReadRBACSnapshot(t.Context(), retainedRawRBACCapture{state: captured, payloads: payloads}, tier)
		require.NoError(t, err)
		require.Len(t, snapshot.Resources, count)
		for _, resource := range snapshot.Resources {
			if resource.Metadata.UID == role.Metadata.UID {
				require.Equal(t, fmt.Sprint(captured.Revision), resource.Metadata.ResourceVersion)
			}
		}
	}
}

func TestRBACNativeRawCaptureWidePartition(t *testing.T) {
	ds := newScyllaDatastoreInKeyspace(t, newScyllaTestKeyspace(t))
	store := rbacStore(t, ds)
	tier := uuid.NewString()
	roles := make([]*rbac.Resource, 96)
	for i := range roles {
		roles[i] = rbacRole(tier, fmt.Sprintf("wide-%03d", i))
		roles[i].Metadata.Annotations = map[string]string{"padding": strings.Repeat("x", 16000)}
	}
	state, err := store.ApplyRBAC(t.Context(), rbacCreate(tier, rbac.TierState{}, 1, roles...))
	require.NoError(t, err)
	require.Greater(t, state.PayloadBytes, int64(1<<20))
	snapshot, err := datastore.ReadRBACSnapshot(t.Context(), store, tier)
	require.NoError(t, err)
	require.Equal(t, state, snapshot.State)
	require.Len(t, snapshot.Resources, len(roles), "server-sized pages must not truncate the captured partition")
	capturer, ok := store.(datastore.RBACSnapshotCapturer)
	require.True(t, ok)
	captured, payloads, err := capturer.CaptureRBACSnapshot(t.Context(), tier, state.PayloadBytes)
	require.NoError(t, err)
	require.Equal(t, state, captured)
	require.Len(t, payloads, len(roles), "smaller caller budgets must retain a complete fenced paginated capture")
	_, _, err = capturer.CaptureRBACSnapshot(t.Context(), tier, state.PayloadBytes-1)
	require.ErrorIs(t, err, rbac.ErrUnavailable)
}

func TestRBACNativeJournalCDC(t *testing.T) {
	ds := newScyllaDatastoreInKeyspace(t, newScyllaTestKeyspace(t))
	store := rbacStore(t, ds)
	journal := ds.(datastore.ResourceWatchCapable).ResourceWatchJournal()
	runner := ds.(interface {
		CatalogCDCKinds() []string
		RunCatalogCDC(context.Context, string, *watchjournal.Materializer, datastore.ResourceWatchLease, time.Duration, time.Duration, func()) error
	})
	require.Contains(t, runner.CatalogCDCKinds(), "RBAC")
	for _, kind := range []string{"Role", "ClusterRole", "RoleBinding", "ClusterRoleBinding"} {
		require.NotContains(t, runner.CatalogCDCKinds(), kind, "one table must have one reader")
	}
	lease, acquired, err := journal.AcquireLease(t.Context(), "rbac-journal-contract", time.Now().UTC(), 5*time.Minute)
	require.NoError(t, err)
	require.True(t, acquired)
	t.Cleanup(func() { require.NoError(t, journal.ReleaseLease(context.Background(), lease)) })
	var stop func()
	var done chan error
	start := func() {
		ctx, cancel := context.WithCancel(t.Context())
		ready := make(chan struct{})
		done = make(chan error, 1)
		currentDone := done
		go func() {
			currentDone <- runner.RunCatalogCDC(ctx, "RBAC", watchjournal.NewMaterializer(journal, watchjournal.MaterializerConfig{EventTTL: time.Hour}),
				lease, 14*24*time.Hour, 500*time.Millisecond, func() { close(ready) })
			close(currentDone)
		}()
		stop = func() {
			cancel()
			select {
			case err := <-currentDone:
				require.True(t, err == nil || errors.Is(err, context.Canceled), "CDC reader: %v", err)
			case <-time.After(5 * time.Second):
				t.Error("owned RBAC reader did not stop")
			}
		}
		select {
		case <-ready:
		case err := <-done:
			t.Fatalf("RBAC reader failed: %v", err)
		case <-time.After(20 * time.Second):
			t.Fatal("RBAC reader did not poll")
		}
	}
	t.Cleanup(func() {
		if stop != nil {
			stop()
		}
	})
	start()
	namespace := uuid.NewString()
	states := map[string]rbac.TierState{}
	resources := map[string]*rbac.Resource{}
	for _, tier := range []string{rbac.ClusterTierID, namespace} {
		kinds := []rbac.Kind{rbac.ClusterRole, rbac.ClusterRoleBinding}
		if tier == namespace {
			kinds = []rbac.Kind{rbac.Role, rbac.RoleBinding}
		}
		var rows []*rbac.Resource
		for _, kind := range kinds {
			resource := rbacRole(tier, strings.ToLower(string(kind)))
			resource.Kind = kind
			if !kind.Namespaced() {
				resource.Metadata.Namespace, resource.NamespaceUID = "", ""
			}
			if !kind.IsRole() {
				resource.Spec = json.RawMessage(`{"roleRef":{"kind":"ClusterRole","name":"reader"},"subjects":[{"kind":"User","name":"alice"}]}`)
			}
			rows = append(rows, resource)
			resources[resource.Metadata.UID] = resource
		}
		states[tier], err = store.ApplyRBAC(t.Context(), rbacCreate(tier, rbac.TierState{}, 1, rows...))
		require.NoError(t, err)
	}
	cursor := datastore.ResourceWatchCursor{}
	observed := map[string]bool{}
	progress := map[string]uint64{}
	metadataOnlyRevision := map[string]uint64{}
	key := func(uid string, eventType datastore.ResourceWatchEventType, version string) string {
		return uid + "/" + string(eventType) + "/" + version
	}
	wait := func(want map[string]bool) {
		t.Helper()
		deadline := time.NewTimer(45 * time.Second)
		defer deadline.Stop()
		for {
			events, err := journal.ReadAfter(t.Context(), cursor, 256)
			require.NoError(t, err)
			for _, event := range events {
				require.Equal(t, cursor.Sequence+1, event.Sequence)
				cursor = datastore.ResourceWatchCursor{Epoch: event.Epoch, Sequence: event.Sequence}
				if event.Type == datastore.ResourceWatchProgress {
					require.Equal(t, "RBACTier", event.Kind)
					require.Empty(t, event.Name)
					var state datastore.RBACTierProgress
					require.NoError(t, json.Unmarshal(event.Payload, &state))
					require.NotEqual(t, metadataOnlyRevision[state.TierID], state.Revision,
						"native Role-only metadata frames retain resource events without authorization progress")
					progress[state.TierID] = max(progress[state.TierID], state.Revision)
					continue
				}
				require.Contains(t, []string{"Role", "ClusterRole", "RoleBinding", "ClusterRoleBinding"}, event.Kind)
				var resource rbac.Resource
				require.NoError(t, json.Unmarshal(event.Payload, &resource))
				require.Equal(t, event.Kind, string(resource.Kind))
				require.Equal(t, event.Namespace, resource.Metadata.Namespace)
				observed[key(resource.Metadata.UID, event.Type, resource.Metadata.ResourceVersion)] = true
				if event.Type == datastore.ResourceWatchDeleted {
					require.Empty(t, resource.Spec)
				}
			}
			complete := true
			for identity := range want {
				complete = complete && observed[identity]
			}
			for tier, state := range states {
				complete = complete && progress[tier] >= state.Revision
			}
			if complete {
				return
			}
			select {
			case err := <-done:
				t.Fatalf("RBAC reader stopped: %v", err)
			case <-deadline.C:
				t.Fatalf("RBAC transitions not published; observed=%d tier progress=%v", len(observed), progress)
			case <-time.After(100 * time.Millisecond):
			}
		}
	}
	expected := map[string]bool{}
	for uid := range resources {
		expected[key(uid, datastore.ResourceWatchAdded, "1")] = true
	}
	wait(expected)
	bounds, err := journal.Bounds(t.Context())
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		frontier, err := journal.LoadProgress(t.Context(), watchjournal.SourceReadFrontierKey("RBAC"))
		return err == nil && string(frontier.Position) == bounds.Epoch && time.Since(frontier.UpdatedAt) < 5*time.Second
	}, 10*time.Second, 100*time.Millisecond, "quiet RBAC readiness must come from its complete native read frontier")
	stop()
	stop = nil
	for uid, resource := range resources {
		if !resource.Kind.IsRole() {
			continue
		}
		tier := rbac.ClusterTierID
		if resource.Kind.Namespaced() {
			tier = namespace
		}
		current, err := store.GetRBACByUID(t.Context(), uid)
		require.NoError(t, err)
		current.Status = json.RawMessage(`{"observedGeneration":1}`)
		states[tier], err = store.ApplyRBAC(t.Context(), rbac.TierWrite{
			TierID: tier, ExpectedRevision: states[tier].Revision,
			Changes: []rbac.Change{{Resource: current, ExpectedUID: uid, ExpectedResourceVersion: current.Metadata.ResourceVersion}},
		})
		require.NoError(t, err)
		metadataOnlyRevision[tier] = states[tier].Revision
		expected[key(uid, datastore.ResourceWatchModified, "2")] = true
	}
	for tier, state := range states {
		write := rbac.TierWrite{TierID: tier, ExpectedRevision: state.Revision}
		for uid, resource := range resources {
			if (tier == namespace) != resource.Kind.Namespaced() {
				continue
			}
			current, err := store.GetRBACByUID(t.Context(), uid)
			require.NoError(t, err)
			current.Status = json.RawMessage(`{"observedGeneration":1}`)
			write.Changes = append(write.Changes, rbac.Change{Resource: current, ExpectedUID: uid, ExpectedResourceVersion: current.Metadata.ResourceVersion})
			version, err := strconv.ParseUint(current.Metadata.ResourceVersion, 10, 64)
			require.NoError(t, err)
			expected[key(uid, datastore.ResourceWatchModified, fmt.Sprint(version+1))] = true
		}
		states[tier], err = store.ApplyRBAC(t.Context(), write)
		require.NoError(t, err)
	}
	start()
	wait(expected)
	bulk := make([]*rbac.Resource, rbac.MaxCandidateResources+1)
	for index := range bulk {
		bulk[index] = rbacRole(namespace, fmt.Sprintf("bulk-%03d", index))
		expected[key(bulk[index].Metadata.UID, datastore.ResourceWatchAdded, "1")] = true
	}
	states[namespace], err = store.ApplyRBAC(t.Context(), rbacCreate(namespace, states[namespace], 3, bulk...))
	require.NoError(t, err)
	wait(expected)
	for uid, resource := range resources {
		if resource.Kind != rbac.RoleBinding {
			continue
		}
		current, err := store.GetRBACByUID(t.Context(), uid)
		require.NoError(t, err)
		now := time.Now().UTC()
		current.Metadata.DeletionTimestamp = &now
		states[namespace], err = store.ApplyRBAC(t.Context(), rbac.TierWrite{TierID: namespace, ExpectedRevision: states[namespace].Revision,
			Changes: []rbac.Change{{Resource: current, ExpectedUID: uid, ExpectedResourceVersion: current.Metadata.ResourceVersion}}})
		require.NoError(t, err)
		current, err = store.GetRBACByUID(t.Context(), uid)
		require.NoError(t, err)
		states[namespace], err = store.ApplyRBAC(t.Context(), rbac.TierWrite{TierID: namespace, ExpectedRevision: states[namespace].Revision,
			Changes: []rbac.Change{{Resource: current, Remove: true, ExpectedUID: uid, ExpectedResourceVersion: current.Metadata.ResourceVersion}}})
		require.NoError(t, err)
		expected[key(uid, datastore.ResourceWatchDeleted, "")] = true
	}
	wait(expected)
}

func TestRBACIndependentScyllaWriters(t *testing.T) {
	first, second := newScyllaDatastores(t)
	runRBACQuotaRace(t, rbacStore(t, first), rbacStore(t, second))
}

func TestRBACNativeRepairAfterIndexFailure(t *testing.T) {
	keyspace := newScyllaTestKeyspace(t)
	store := rbacStore(t, newScyllaDatastoreInKeyspace(t, keyspace))
	session, err := openRootSession(scyllaAddr)
	require.NoError(t, err)
	defer session.Close()
	require.NoError(t, session.Query("DROP TABLE "+keyspace+".rbac_resources_by_id").Exec())
	require.NoError(t, session.AwaitSchemaAgreement(t.Context()))
	tier := uuid.NewString()
	roles := make([]*rbac.Resource, 9)
	for index := range roles {
		roles[index] = rbacRole(tier, fmt.Sprintf("repair-%02d", index))
	}
	write := rbacCreate(tier, rbac.TierState{}, 1, roles...)
	state, err := store.ApplyRBAC(t.Context(), write)
	require.Error(t, err)
	require.True(t, state.ProjectionPending)
	require.EqualValues(t, 1, state.Revision)
	snapshot, err := datastore.ReadRBACSnapshot(t.Context(), store, tier)
	require.NoError(t, err, "lookup failure cannot partially publish the authoritative candidate")
	require.Len(t, snapshot.Resources, len(roles))
	_, err = store.ApplyRBAC(t.Context(), rbacCreate(tier, state, 2, rbacRole(tier, "blocked")))
	require.ErrorIs(t, err, datastore.ErrProjectionNotReady)
	raw, err := migrations.Files.ReadFile("014_rbac.cql")
	require.NoError(t, err)
	restored := false
	for _, statement := range strings.Split(string(raw), ";") {
		if strings.HasPrefix(strings.TrimSpace(statement), "CREATE TABLE IF NOT EXISTS rbac_resources_by_id ") {
			require.NoError(t, session.Query(strings.Replace(statement, "rbac_resources_by_id", keyspace+".rbac_resources_by_id", 1)).Exec())
			restored = true
		}
	}
	require.True(t, restored)
	require.NoError(t, session.AwaitSchemaAgreement(t.Context()))
	other := rbacStore(t, newScyllaDatastoreInKeyspace(t, keyspace))
	results := make(chan error, 2)
	for _, replica := range []rbac.Store{store, other} {
		go func() { results <- replica.RepairRBACProjection(t.Context(), tier, state.Revision) }()
	}
	for range 2 {
		require.NoError(t, <-results)
	}
	replayed, err := store.ApplyRBAC(t.Context(), write)
	require.NoError(t, err)
	require.EqualValues(t, 1, replayed.Revision)
	require.False(t, replayed.ProjectionPending)
	for _, role := range roles {
		repaired, err := other.GetRBACByUID(t.Context(), role.Metadata.UID)
		require.NoError(t, err)
		require.Equal(t, role.Metadata.Name, repaired.Metadata.Name)
	}
}

func TestRBACNativeFullCDCImages(t *testing.T) {
	keyspace := newScyllaTestKeyspace(t)
	store := rbacStore(t, newScyllaDatastoreInKeyspace(t, keyspace))
	session, err := openRootSession(scyllaAddr)
	require.NoError(t, err)
	defer session.Close()
	tier := uuid.NewString()
	role := rbacRole(tier, "cdc-images")
	state, err := store.ApplyRBAC(datastore.ContextWithRBACAuditActor(t.Context(), "authenticated-creator"),
		rbacCreate(tier, rbac.TierState{}, 1, role))
	require.NoError(t, err)
	current, err := store.GetRBACByUID(t.Context(), role.Metadata.UID)
	require.NoError(t, err)
	current.Status = json.RawMessage(`{"observedGeneration":1,"conditions":[]}`)
	_, err = store.ApplyRBAC(datastore.ContextWithRBACAuditActor(t.Context(), "authenticated-controller"), rbac.TierWrite{
		TierID: tier, ExpectedRevision: state.Revision,
		Changes: []rbac.Change{{Resource: current, ExpectedUID: current.Metadata.UID, ExpectedResourceVersion: current.Metadata.ResourceVersion}},
	})
	require.NoError(t, err)
	var created, updated time.Time
	var creator, updater string
	require.NoError(t, session.Query(fmt.Sprintf(`SELECT creation_timestamp,creation_actor,update_timestamp,update_actor
		FROM %s.rbac_resources_by_scope WHERE tier=? AND entry=?`, keyspace), tier, "Role/cdc-images").
		Scan(&created, &creator, &updated, &updater))
	require.Equal(t, "authenticated-creator", creator)
	require.Equal(t, "authenticated-controller", updater)
	require.False(t, created.IsZero())
	require.False(t, updated.Before(created))
	require.Eventually(t, func() bool {
		iter := session.Query(fmt.Sprintf(`SELECT "cdc$operation",payload FROM %s.rbac_resources_by_scope_scylla_cdc_log LIMIT 128`, keyspace)).
			WithContext(t.Context()).Iter()
		var operation int8
		var payload []byte
		pre, post := false, false
		for iter.Scan(&operation, &payload) {
			var resource rbac.Resource
			if json.Unmarshal(payload, &resource) != nil || resource.Metadata.UID != role.Metadata.UID {
				continue
			}
			if resource.Metadata.CreationActor != "authenticated-creator" {
				continue
			}
			pre = pre || (scyllacdc.OperationType(operation) == scyllacdc.PreImage && len(resource.Status) == 0 &&
				resource.Metadata.UpdateActor == "authenticated-creator")
			post = post || (scyllacdc.OperationType(operation) == scyllacdc.PostImage && len(resource.Status) != 0 &&
				resource.Metadata.UpdateActor == "authenticated-controller")
		}
		err := iter.Close()
		return err == nil && pre && post
	}, 15*time.Second, 100*time.Millisecond)
}
