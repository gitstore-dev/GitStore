// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

//go:build scylla

package scylla_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gitstore-dev/gitstore/api/internal/catalog"
	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	"github.com/gitstore-dev/gitstore/api/internal/datastore/scylla"
	"github.com/gitstore-dev/gitstore/translation"
	"github.com/gocql/gocql"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

func TestTranslationNativeTargetWriteDiscovery(t *testing.T) {
	cfg := testScyllaConfig(t)
	cfg.Keyspace += "_target_discovery"
	provisionKeyspace(scyllaAddr, cfg.Keyspace)
	t.Cleanup(func() { dropKeyspace(scyllaAddr, cfg.Keyspace) })
	first, err := scylla.New(cfg, zap.NewNop(), 0)
	require.NoError(t, err)
	t.Cleanup(func() { _ = first.Close() })
	second, err := scylla.New(cfg, zap.NewNop(), 0)
	require.NoError(t, err)
	t.Cleanup(func() { _ = second.Close() })
	type maintenance interface {
		ListTranslationTargetMaintenanceNamespaces(context.Context, string, int) (datastore.TranslationDependentPage, error)
		RecoverTranslationTargetMaintenance(context.Context, string, string, int) (datastore.TranslationDependentPage, error)
	}
	decorated := datastore.NewInstrumentedDatastore(second, "scylla", zap.NewNop())
	worker, ok := decorated.(datastore.TranslationTargetMaintenanceCapable).TranslationTargetMaintenanceStore().(maintenance)
	require.True(t, ok, "replacement stores must discover pending target work without a target-name hint")
	for _, step := range []string{"reserve-target-head", "publish-target-source", "stabilize-target-source", "retire-target-intent"} {
		t.Run(step, func(t *testing.T) {
			namespace := &datastore.Namespace{
				UID: newID(), Name: "discovery-" + newID(), Tier: datastore.NamespaceTierOrganization,
				CreationTimestamp: time.Now().UTC().Truncate(time.Millisecond),
			}
			require.NoError(t, first.CreateNamespace(t.Context(), namespace))
			product := newProduct(namespace.Name, "pending")
			product.Generation, product.ResourceVersion = 1, "1"
			restore := scylla.FailMutationBeforeForTest(first, step)
			err := first.CreateProduct(t.Context(), product)
			restore()
			require.Error(t, err)
			committed := step == "stabilize-target-source" || step == "retire-target-intent"
			if !committed {
				require.NoError(t, first.DeleteNamespace(t.Context(), namespace.UID))
			}
			cursor, found := "", false
			for turn := 0; turn < 80; turn++ {
				page, err := worker.ListTranslationTargetMaintenanceNamespaces(t.Context(), cursor, 1)
				require.NoError(t, err)
				require.LessOrEqual(t, page.Probes, datastore.MaxTranslationIndexProbes)
				require.LessOrEqual(t, len(page.Items), 1)
				for _, item := range page.Items {
					found = found || item.TranslationUID == namespace.UID
				}
				if page.Next == "" {
					break
				}
				require.NotEqual(t, cursor, page.Next)
				cursor = page.Next
			}
			require.True(t, found, "retained discovery must not depend on a live Namespace")
			cursor, recovered := "", 0
			for turn := 0; turn < 80; turn++ {
				page, err := worker.RecoverTranslationTargetMaintenance(t.Context(), namespace.UID, cursor, 1)
				require.NoError(t, err)
				require.LessOrEqual(t, page.Probes, datastore.MaxTranslationIndexProbes)
				require.LessOrEqual(t, len(page.Items), 1)
				recovered += len(page.Items)
				if page.Next == "" {
					break
				}
				require.NotEqual(t, cursor, page.Next)
				cursor = page.Next
			}
			require.Equal(t, 1, recovered)
			key := datastore.TranslationTargetKey{
				NamespaceUID: namespace.UID, APIVersion: product.APIVersion, Kind: product.Kind, Name: product.Name,
			}
			stable, err := second.(datastore.TranslationTargetWriteFenceStore).GetTranslationTargetFence(t.Context(), key)
			require.NoError(t, err)
			require.Equal(t, datastore.TranslationTargetStable, stable.Phase)
			if committed {
				require.NotNil(t, stable.Evidence)
				require.Equal(t, product.UID, stable.Evidence.UID)
			} else {
				require.Nil(t, stable.Evidence)
			}
			cursor = ""
			for turn := 0; turn < 80; turn++ {
				page, err := worker.RecoverTranslationTargetMaintenance(t.Context(), namespace.UID, cursor, 1)
				require.NoError(t, err)
				require.Empty(t, page.Items, "settled locators must retire without resetting the name token")
				if page.Next == "" {
					break
				}
				cursor = page.Next
			}
			unchanged, err := second.(datastore.TranslationTargetWriteFenceStore).GetTranslationTargetFence(t.Context(), key)
			require.NoError(t, err)
			require.Equal(t, stable, unchanged)
		})
	}
	t.Run("paused-pre-head-writer", func(t *testing.T) {
		namespace := &datastore.Namespace{
			UID: newID(), Name: "paused-head-" + newID(), Tier: datastore.NamespaceTierOrganization,
			CreationTimestamp: time.Now().UTC().Truncate(time.Millisecond),
		}
		require.NoError(t, first.CreateNamespace(t.Context(), namespace))
		product := newProduct(namespace.Name, "pending")
		product.Generation, product.ResourceVersion = 1, "1"
		key := datastore.TranslationTargetKey{
			NamespaceUID: namespace.UID, APIVersion: product.APIVersion, Kind: product.Kind, Name: product.Name,
		}
		var settled *datastore.TranslationTargetFence
		restore := scylla.RunMutationBeforeForTest(first, "reserve-target-head", func() {
			cursor := ""
			for turn := 0; turn < 80; turn++ {
				page, err := worker.RecoverTranslationTargetMaintenance(t.Context(), namespace.UID, cursor, 1)
				require.NoError(t, err)
				if page.Next == "" {
					break
				}
				cursor = page.Next
			}
			var err error
			settled, err = second.(datastore.TranslationTargetWriteFenceStore).GetTranslationTargetFence(t.Context(), key)
			require.NoError(t, err)
			require.Nil(t, settled.Evidence)
		})
		err := first.CreateProduct(t.Context(), product)
		restore()
		require.ErrorIs(t, err, datastore.ErrConflict)
		require.NotNil(t, settled)
		_, err = second.GetProduct(t.Context(), product.UID)
		require.ErrorIs(t, err, datastore.ErrNotFound)
		after, err := second.(datastore.TranslationTargetWriteFenceStore).GetTranslationTargetFence(t.Context(), key)
		require.NoError(t, err)
		require.Equal(t, settled, after, "a delayed original Begin cannot reset the recovered token")
	})
	t.Run("checkpointed-worker-turns", func(t *testing.T) {
		namespace := &datastore.Namespace{
			UID: newID(), Name: "worker-turn-" + newID(), Tier: datastore.NamespaceTierOrganization,
			CreationTimestamp: time.Now().UTC().Truncate(time.Millisecond),
		}
		require.NoError(t, first.CreateNamespace(t.Context(), namespace))
		product := newProduct(namespace.Name, "pending")
		product.Generation, product.ResourceVersion = 1, "1"
		key := datastore.TranslationTargetKey{
			NamespaceUID: namespace.UID, APIVersion: product.APIVersion, Kind: product.Kind, Name: product.Name,
		}
		restore := scylla.FailMutationBeforeForTest(first, "publish-target-source")
		err := first.CreateProduct(t.Context(), product)
		restore()
		require.Error(t, err)
		driver, ok := worker.(interface{ RecoverTranslationTargetMaintenanceTurn(context.Context) error })
		require.True(t, ok)
		replacement, ok := first.(interface{ RecoverTranslationTargetMaintenanceTurn(context.Context) error })
		require.True(t, ok)
		for turn := 0; turn < 320; turn++ {
			// Every turn changes process-local state owner. Discovery and both
			// cursor levels must come from the database, not an in-memory queue.
			if turn%2 == 0 {
				require.NoError(t, driver.RecoverTranslationTargetMaintenanceTurn(t.Context()))
			} else {
				require.NoError(t, replacement.RecoverTranslationTargetMaintenanceTurn(t.Context()))
			}
			head, err := second.(datastore.TranslationTargetWriteFenceStore).GetTranslationTargetFence(t.Context(), key)
			require.NoError(t, err)
			if head.Phase == datastore.TranslationTargetStable {
				require.Nil(t, head.Evidence)
				return
			}
		}
		t.Fatal("automatic bounded turns did not settle discoverable work")
	})
	t.Run("concurrent-worker-turns", func(t *testing.T) {
		namespace := &datastore.Namespace{
			UID: newID(), Name: "peer-turn-" + newID(), Tier: datastore.NamespaceTierOrganization,
			CreationTimestamp: time.Now().UTC().Truncate(time.Millisecond),
		}
		require.NoError(t, first.CreateNamespace(t.Context(), namespace))
		product := newProduct(namespace.Name, "pending")
		product.Generation, product.ResourceVersion = 1, "1"
		key := datastore.TranslationTargetKey{
			NamespaceUID: namespace.UID, APIVersion: product.APIVersion, Kind: product.Kind, Name: product.Name,
		}
		restore := scylla.FailMutationBeforeForTest(first, "publish-target-source")
		err := first.CreateProduct(t.Context(), product)
		restore()
		require.Error(t, err)
		outcomes := make(chan error, 2)
		var peers sync.WaitGroup
		for _, peer := range []datastore.TranslationTargetMaintenanceStore{
			worker.(datastore.TranslationTargetMaintenanceStore),
			first.(datastore.TranslationTargetMaintenanceCapable).TranslationTargetMaintenanceStore(),
		} {
			peers.Go(func() {
				for turn := 0; turn < 320; turn++ {
					err := peer.RecoverTranslationTargetMaintenanceTurn(t.Context())
					if err != nil && !errors.Is(err, datastore.ErrConflict) {
						outcomes <- err
						return
					}
					head, err := second.(datastore.TranslationTargetWriteFenceStore).GetTranslationTargetFence(t.Context(), key)
					if err != nil {
						outcomes <- err
						return
					}
					if head.Phase == datastore.TranslationTargetStable {
						outcomes <- nil
						return
					}
				}
				outcomes <- fmt.Errorf("concurrent maintenance did not reach pending source")
			})
		}
		peers.Wait()
		close(outcomes)
		for err := range outcomes {
			require.NoError(t, err)
		}
		stable, err := second.(datastore.TranslationTargetWriteFenceStore).GetTranslationTargetFence(t.Context(), key)
		require.NoError(t, err)
		require.Nil(t, stable.Evidence)
	})
	t.Run("closed-cell-compaction-and-delayed-publication", func(t *testing.T) {
		namespace := &datastore.Namespace{
			UID: newID(), Name: "compact-work-" + newID(), Tier: datastore.NamespaceTierOrganization,
			CreationTimestamp: time.Now().UTC().Truncate(time.Millisecond),
		}
		require.NoError(t, first.CreateNamespace(t.Context(), namespace))
		product := newProduct(namespace.Name, "committed")
		product.Generation, product.ResourceVersion = 1, "1"
		key := datastore.TranslationTargetKey{
			NamespaceUID: namespace.UID, APIVersion: product.APIVersion, Kind: product.Kind, Name: product.Name,
		}
		restore := scylla.FailMutationBeforeForTest(first, "stabilize-target-source")
		err := first.CreateProduct(t.Context(), product)
		restore()
		require.Error(t, err)
		pending, err := second.(datastore.TranslationTargetWriteFenceStore).GetTranslationTargetFence(t.Context(), key)
		require.NoError(t, err)
		placement, err := second.(datastore.TranslationIndexCapable).TranslationIndexStore().GetTranslationIndexReservation(t.Context(), pending.Intent.ID)
		require.NoError(t, err)
		drain := func() {
			cursor := ""
			for turn := 0; turn < 80; turn++ {
				page, err := worker.RecoverTranslationTargetMaintenance(t.Context(), namespace.UID, cursor, 1)
				require.NoError(t, err)
				if page.Next == "" {
					return
				}
				cursor = page.Next
			}
			t.Fatal("pending-cell drain did not finish")
		}
		drain()
		stable, err := second.(datastore.TranslationTargetWriteFenceStore).GetTranslationTargetFence(t.Context(), key)
		require.NoError(t, err)
		raw := newRawSessionForKeyspace(t, cfg.Keyspace)
		assertCompacted := func() {
			var uid gocql.UUID
			err := raw.Query(
				"SELECT uid FROM translation_target_maintenance_by_segment WHERE index_kind=? AND namespace_uid=? AND shard=? AND segment=? AND uid=?",
				string(placement.Index.Kind), mustTranslationUUID(t, namespace.UID), int8(placement.Index.Shard),
				int64(placement.Index.Segment), mustTranslationUUID(t, placement.Entry.TranslationUID),
			).Consistency(gocql.Serial).Scan(&uid)
			require.ErrorIs(t, err, gocql.ErrNotFound, "settled work must not consume the active row budget forever")
		}
		assertCompacted()
		applied, err := raw.Query(
			"INSERT INTO translation_target_maintenance_by_segment (index_kind,namespace_uid,shard,segment,uid,source_version,reference_epoch,slot,reservation_token,retired,creation_timestamp) VALUES (?,?,?,?,?,?,?,?,?,false,?) IF NOT EXISTS",
			string(placement.Index.Kind), mustTranslationUUID(t, namespace.UID), int8(placement.Index.Shard),
			int64(placement.Index.Segment), mustTranslationUUID(t, placement.Entry.TranslationUID),
			placement.Entry.SourceVersion, placement.Entry.ReferenceEpoch, int(placement.Entry.Slot),
			mustTranslationUUID(t, placement.Token), placement.Entry.CreationTimestamp,
		).Consistency(gocql.Quorum).SerialConsistency(gocql.Serial).MapScanCAS(map[string]any{})
		require.NoError(t, err)
		require.True(t, applied, "replay an exact delayed private-cell INSERT after compaction")
		drain()
		assertCompacted()
		after, err := second.(datastore.TranslationTargetWriteFenceStore).GetTranslationTargetFence(t.Context(), key)
		require.NoError(t, err)
		require.Equal(t, stable, after, "a closed locator cannot reserve or recover its target again")
	})
	for _, operation := range []string{"update", "invalidation", "delete", "delete-after-stable", "foreign-owner"} {
		t.Run(operation, func(t *testing.T) {
			namespace := &datastore.Namespace{
				UID: newID(), Name: "pending-" + newID(), Tier: datastore.NamespaceTierOrganization,
				CreationTimestamp: time.Now().UTC().Truncate(time.Millisecond),
			}
			require.NoError(t, first.CreateNamespace(t.Context(), namespace))
			product := newProduct(namespace.Name, "original")
			product.Generation, product.ResourceVersion = 1, "1"
			if operation == "invalidation" {
				product.APIVersion, product.Kind = "storage.gitstore.dev/v1beta1", "File"
			}
			if operation == "foreign-owner" {
				restore := scylla.FailMutationBeforeForTest(first, "reserve-target-head")
				err := writeNativeTranslationTarget(t.Context(), first, product, true, "")
				restore()
				require.Error(t, err)
				product.UID = newID()
				require.NoError(t, writeNativeTranslationTarget(t.Context(), first, product, true, ""))
			} else {
				require.NoError(t, writeNativeTranslationTarget(t.Context(), first, product, true, ""))
				if operation == "delete" || operation == "delete-after-stable" {
					step := "publish-target-retirement"
					if operation == "delete-after-stable" {
						step = "compact-target-retirement"
					}
					restore := scylla.FailMutationBeforeForTest(first, step)
					err := first.DeleteProduct(t.Context(), product.UID)
					restore()
					require.Error(t, err)
				} else {
					product.Body = "committed"
					if operation == "update" {
						product.Generation, product.ResourceVersion = 2, "2"
					}
					restore := scylla.FailMutationBeforeForTest(first, "stabilize-target-source")
					err := writeNativeTranslationTarget(t.Context(), first, product, false, "1")
					restore()
					require.Error(t, err)
				}
			}
			key := datastore.TranslationTargetKey{
				NamespaceUID: namespace.UID, APIVersion: product.APIVersion, Kind: product.Kind, Name: product.Name,
			}
			cursor, recovered := "", 0
			for turn := 0; turn < 80; turn++ {
				page, err := worker.RecoverTranslationTargetMaintenance(t.Context(), namespace.UID, cursor, 1)
				require.NoError(t, err)
				require.LessOrEqual(t, page.Probes, datastore.MaxTranslationIndexProbes)
				recovered += len(page.Items)
				if page.Next == "" {
					break
				}
				cursor = page.Next
			}
			require.Equal(t, 1, recovered)
			stable, err := second.(datastore.TranslationTargetWriteFenceStore).GetTranslationTargetFence(t.Context(), key)
			require.NoError(t, err)
			require.Equal(t, datastore.TranslationTargetStable, stable.Phase)
			if operation == "invalidation" || operation == "delete" || operation == "delete-after-stable" {
				require.Nil(t, stable.Evidence)
			} else {
				require.Equal(t, product.UID, stable.Evidence.UID)
				require.Equal(t, product.ResourceVersion, stable.Evidence.ResourceVersion)
			}
			if operation == "invalidation" {
				live, err := second.GetFile(t.Context(), product.UID)
				require.NoError(t, err)
				require.Equal(t, product.Body, live.Body, "invalidating proof must not retire a live resource")
			}
			if operation == "delete-after-stable" {
				raw := newRawSessionForKeyspace(t, cfg.Keyspace)
				var uid gocql.UUID
				err := raw.Query(
					"SELECT uid FROM products_by_key WHERE namespace=? AND creation_timestamp=? AND uid=?",
					product.Namespace, product.CreationTimestamp, mustTranslationUUID(t, product.UID),
				).Consistency(gocql.Serial).Scan(&uid)
				require.ErrorIs(t, err, gocql.ErrNotFound, "stable name absence must not discard unfinished physical retirement work")
			}
		})
	}
}

func TestTranslationNativeRecoversAbortedUpdateWithSourceCASProof(t *testing.T) {
	cfg := testScyllaConfig(t)
	cfg.Keyspace += "_target_abort"
	provisionKeyspace(scyllaAddr, cfg.Keyspace)
	t.Cleanup(func() { dropKeyspace(scyllaAddr, cfg.Keyspace) })
	first, err := scylla.New(cfg, zap.NewNop(), 0)
	require.NoError(t, err)
	t.Cleanup(func() { _ = first.Close() })
	namespace := &datastore.Namespace{
		UID: newID(), Name: "abort-" + newID(), Tier: datastore.NamespaceTierOrganization,
		CreationTimestamp: time.Now().UTC().Truncate(time.Millisecond),
	}
	require.NoError(t, first.CreateNamespace(t.Context(), namespace))
	product := newProduct(namespace.Name, "original")
	product.Generation, product.ResourceVersion = 1, "1"
	require.NoError(t, first.CreateProduct(t.Context(), product))
	raw := newRawSessionForKeyspace(t, cfg.Keyspace)
	fences := first.(datastore.TranslationTargetWriteFenceStore)
	key := datastore.TranslationTargetKey{
		NamespaceUID: namespace.UID, APIVersion: product.APIVersion, Kind: product.Kind, Name: product.Name,
	}
	digest, err := translation.TargetContentDigest(t.Context(), product.APIVersion, product.Kind, product.Spec, product.Body)
	require.NoError(t, err)
	initial, err := fences.GetTranslationTargetFence(t.Context(), key)
	require.NoError(t, err)
	var previous string
	require.NoError(t, raw.Query(
		"SELECT translation_source_identity FROM products_by_key WHERE namespace=? AND creation_timestamp=? AND uid=?",
		product.Namespace, product.CreationTimestamp, mustTranslationUUID(t, product.UID),
	).Consistency(gocql.Serial).Scan(&previous))
	proof, err := json.Marshal(struct {
		Namespace        string
		PreviousIdentity *string
	}{product.Namespace, &previous})
	require.NoError(t, err)
	pending, err := fences.BeginTranslationTargetWrite(t.Context(), key, datastore.TranslationTargetWriteIntent{
		ID: newID(), PreviousUID: product.UID, NextUID: product.UID, ExpectedResourceVersion: "1",
		SourceDigest: digest, CreationTimestamp: product.CreationTimestamp, SourceProof: string(proof),
	}, initial.Token)
	require.NoError(t, err)
	recovery, ok := first.(interface {
		RecoverTranslationTargetWrite(context.Context, datastore.TranslationTargetKey) error
	})
	require.True(t, ok)
	restore := scylla.FailMutationBeforeForTest(first, "publish-target-abort")
	err = recovery.RecoverTranslationTargetWrite(t.Context(), key)
	restore()
	require.Error(t, err, "source-CAS cancellation must remain recoverable before fence publication")
	stillPending, err := fences.GetTranslationTargetFence(t.Context(), key)
	require.NoError(t, err)
	require.Equal(t, pending, stillPending)
	var marker struct {
		datastore.TranslationTargetSourceIdentity
		Canceled bool
	}
	var persisted string
	require.NoError(t, raw.Query(
		"SELECT translation_source_identity FROM products_by_key WHERE namespace=? AND creation_timestamp=? AND uid=?",
		product.Namespace, product.CreationTimestamp, mustTranslationUUID(t, product.UID),
	).Consistency(gocql.Serial).Scan(&persisted))
	require.NoError(t, json.Unmarshal([]byte(persisted), &marker))
	require.Equal(t, pending.Token, marker.WriteToken)
	require.True(t, marker.Canceled)
	// A status advance with identical desired content must not disguise the
	// persisted cancellation as a successful material publication.
	require.NoError(t, raw.Query(
		"UPDATE products_by_key SET resource_version='2' WHERE namespace=? AND creation_timestamp=? AND uid=?",
		product.Namespace, product.CreationTimestamp, mustTranslationUUID(t, product.UID),
	).Exec())
	second, err := scylla.New(cfg, zap.NewNop(), 0)
	require.NoError(t, err)
	t.Cleanup(func() { _ = second.Close() })
	replacement := second.(interface {
		RecoverTranslationTargetWrite(context.Context, datastore.TranslationTargetKey) error
	})
	require.NoError(t, replacement.RecoverTranslationTargetWrite(t.Context(), key))
	stable, err := fences.GetTranslationTargetFence(t.Context(), key)
	require.NoError(t, err)
	require.Equal(t, datastore.TranslationTargetStable, stable.Phase)
	require.Equal(t, pending.Token, stable.Token)
	require.Equal(t, "2", stable.Evidence.ResourceVersion)
	require.Equal(t, digest, stable.Evidence.ContentDigest)
	late, err := raw.Query(
		"UPDATE products_by_key SET body='late writer' WHERE namespace=? AND creation_timestamp=? AND uid=? IF resource_version='2' AND translation_source_identity=?",
		product.Namespace, product.CreationTimestamp, mustTranslationUUID(t, product.UID), previous,
	).MapScanCAS(map[string]any{})
	require.NoError(t, err)
	require.False(t, late, "durable source cancellation, not a timeout, fences the paused writer")
	require.NoError(t, replacement.RecoverTranslationTargetWrite(t.Context(), key))
	desiredDigest, err := translation.TargetContentDigest(t.Context(), product.APIVersion, product.Kind, product.Spec, "committed")
	require.NoError(t, err)
	proof, err = json.Marshal(struct {
		Namespace        string
		PreviousIdentity *string
	}{product.Namespace, &persisted})
	require.NoError(t, err)
	committing, err := fences.BeginTranslationTargetWrite(t.Context(), key, datastore.TranslationTargetWriteIntent{
		ID: newID(), PreviousUID: product.UID, NextUID: product.UID, ExpectedResourceVersion: "2",
		SourceDigest: desiredDigest, CreationTimestamp: product.CreationTimestamp, SourceProof: string(proof),
	}, stable.Token)
	require.NoError(t, err)
	committedIdentity, err := json.Marshal(datastore.TranslationTargetSourceIdentity{
		NamespaceUID: namespace.UID, WriteToken: committing.Token, ContentDigest: desiredDigest,
	})
	require.NoError(t, err)
	require.NoError(t, raw.Query(
		"UPDATE products_by_key SET body='committed',resource_version='3',generation=2,translation_source_identity=? WHERE namespace=? AND creation_timestamp=? AND uid=?",
		string(committedIdentity), product.Namespace, product.CreationTimestamp, mustTranslationUUID(t, product.UID),
	).Exec())
	require.NoError(t, replacement.RecoverTranslationTargetWrite(t.Context(), key))
	committed, err := fences.GetTranslationTargetFence(t.Context(), key)
	require.NoError(t, err)
	require.Equal(t, committing.Token, committed.Token)
	require.Equal(t, datastore.TranslationTargetStable, committed.Phase)
	require.Equal(t, "3", committed.Evidence.ResourceVersion)
	require.Equal(t, int64(2), committed.Evidence.Generation)
	require.Equal(t, desiredDigest, committed.Evidence.ContentDigest)
	_, err = datastore.PlanTranslationTargetAbort(committed, committed.Token, stable.Evidence)
	require.ErrorIs(t, err, datastore.ErrConflict, "a completed source mutation cannot be restored as a canceled update")
	actual, err := second.GetProduct(t.Context(), product.UID)
	require.NoError(t, err)
	actual.Body, actual.Generation, actual.ResourceVersion = "actual native writer", 3, "4"
	require.NoError(t, second.UpdateProduct(t.Context(), actual, "3"))
	published, err := fences.GetTranslationTargetFence(t.Context(), key)
	require.NoError(t, err)
	require.NotEqual(t, committed.Token, published.Token, "actual proved material writers must reserve a fresh fence token")
	require.Equal(t, "4", published.Evidence.ResourceVersion)
	digest, err = translation.TargetContentDigest(t.Context(), actual.APIVersion, actual.Kind, actual.Spec, actual.Body)
	require.NoError(t, err)
	require.Equal(t, digest, published.Evidence.ContentDigest)
	require.NoError(t, raw.Query(
		"SELECT translation_source_identity FROM products_by_key WHERE namespace=? AND creation_timestamp=? AND uid=?",
		product.Namespace, product.CreationTimestamp, mustTranslationUUID(t, product.UID),
	).Consistency(gocql.Serial).Scan(&persisted))
	var finalIdentity datastore.TranslationTargetSourceIdentity
	require.NoError(t, json.Unmarshal([]byte(persisted), &finalIdentity))
	require.Equal(t, published.Token, finalIdentity.WriteToken)
	require.ErrorIs(t, second.DeleteProductWithResourceVersion(t.Context(), product.UID, "3"), datastore.ErrConflict)
	unchanged, err := fences.GetTranslationTargetFence(t.Context(), key)
	require.NoError(t, err)
	require.Equal(t, published, unchanged, "a stale delete must not reserve a name fence")

	beforeDelete := persisted
	canceled := false
	restore = scylla.RunMutationBeforeForTest(second, "retire-target-source", func() {
		canceled = true
		require.NoError(t, recovery.RecoverTranslationTargetWrite(t.Context(), key))
	})
	err = second.DeleteProductWithResourceVersion(t.Context(), product.UID, "4")
	restore()
	require.ErrorIs(t, err, datastore.ErrConflict)
	require.True(t, canceled)
	live, err := first.GetProduct(t.Context(), product.UID)
	require.NoError(t, err)
	require.Equal(t, actual.Body, live.Body)
	afterCancel, err := fences.GetTranslationTargetFence(t.Context(), key)
	require.NoError(t, err)
	require.Equal(t, datastore.TranslationTargetStable, afterCancel.Phase)
	require.NotEqual(t, published.Token, afterCancel.Token)
	require.Equal(t, published.Evidence, afterCancel.Evidence)
	late, err = raw.Query(
		"UPDATE products_by_key SET translation_retired=true WHERE namespace=? AND creation_timestamp=? AND uid=? IF resource_version='4' AND translation_retired=null AND translation_source_identity=?",
		product.Namespace, product.CreationTimestamp, mustTranslationUUID(t, product.UID), beforeDelete,
	).MapScanCAS(map[string]any{})
	require.NoError(t, err)
	require.False(t, late, "source cancellation must fence a paused delete even without a public-version change")

	restore = scylla.FailMutationBeforeForTest(second, "publish-target-retirement")
	err = second.DeleteProductWithResourceVersion(t.Context(), product.UID, "4")
	restore()
	require.Error(t, err)
	deleting, err := fences.GetTranslationTargetFence(t.Context(), key)
	require.NoError(t, err)
	require.Equal(t, datastore.TranslationTargetUpdating, deleting.Phase)
	require.True(t, deleting.Intent.Deleting)
	third, err := scylla.New(cfg, zap.NewNop(), 0)
	require.NoError(t, err)
	t.Cleanup(func() { _ = third.Close() })
	thirdRecovery := third.(interface {
		RecoverTranslationTargetWrite(context.Context, datastore.TranslationTargetKey) error
	})
	repair, err := scylla.OpenProjectionRepairService(cfg)
	require.NoError(t, err)
	defer repair.Close()
	plan, err := repair.Audit(t.Context())
	require.NoError(t, err)
	_, err = repair.Apply(t.Context(), plan)
	require.NoError(t, err)
	require.NoError(t, thirdRecovery.RecoverTranslationTargetWrite(t.Context(), key))
	absent, err := fences.GetTranslationTargetFence(t.Context(), key)
	require.NoError(t, err)
	require.Equal(t, datastore.TranslationTargetStable, absent.Phase)
	require.Equal(t, deleting.Token, absent.Token)
	require.Nil(t, absent.Evidence)
	_, err = third.GetProduct(t.Context(), product.UID)
	require.ErrorIs(t, err, datastore.ErrNotFound)
	var retired bool
	require.NoError(t, raw.Query(
		"SELECT retired FROM translation_target_tombstones_by_uid WHERE kind='Product' AND uid=?",
		mustTranslationUUID(t, product.UID),
	).Consistency(gocql.Serial).Scan(&retired))
	require.True(t, retired, "permanent UID retirement precedes source-proof compaction")
	var count int
	require.NoError(t, raw.Query(
		"SELECT count(*) FROM products_by_key WHERE namespace=? AND creation_timestamp=? AND uid=?",
		product.Namespace, product.CreationTimestamp, mustTranslationUUID(t, product.UID),
	).Scan(&count))
	require.Zero(t, count)
	require.NoError(t, thirdRecovery.RecoverTranslationTargetWrite(t.Context(), key))
}

func TestTranslationNativeSourceIdentityTravelsWithTargetCAS(t *testing.T) {
	cfg := testScyllaConfig(t)
	cfg.Keyspace += "_target_identity"
	provisionKeyspace(scyllaAddr, cfg.Keyspace)
	t.Cleanup(func() { dropKeyspace(scyllaAddr, cfg.Keyspace) })
	store, err := scylla.New(cfg, zap.NewNop(), 0)
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	namespace := &datastore.Namespace{
		UID: newID(), Name: "identity-" + newID(), Tier: datastore.NamespaceTierOrganization,
		CreationTimestamp: time.Now().UTC().Truncate(time.Millisecond),
	}
	require.NoError(t, store.CreateNamespace(t.Context(), namespace))
	raw := newRawSessionForKeyspace(t, cfg.Keyspace)
	tables := map[string]string{
		"Product": "products_by_key", "ProductVariant": "product_variants_by_namespace",
		"CategoryTaxonomy": "category_taxonomies_by_namespace", "Collection": "collections_by_namespace",
		"File": "files_by_namespace",
	}
	for _, kind := range []string{"Product", "ProductVariant", "CategoryTaxonomy", "Collection", "File"} {
		t.Run(kind, func(t *testing.T) {
			product := newProduct(namespace.Name, "material-"+kind)
			product.Kind, product.Generation, product.ResourceVersion = kind, 0, "1"
			product.Spec, product.Body = json.RawMessage(`{"title":"Original"}`), "original\n"
			if kind == "File" {
				product.APIVersion = "storage.gitstore.dev/v1beta1"
			}
			require.NoError(t, writeNativeTranslationTarget(t.Context(), store, product, true, ""))
			legacy := *product
			legacy.UID, legacy.Namespace = newID(), "missing-"+newID()
			require.NoError(t, writeNativeTranslationTarget(t.Context(), store, &legacy, true, ""),
				"private metadata must preserve existing source creation without a Namespace row")
			var legacyIdentity string
			require.NoError(t, raw.Query(
				"SELECT translation_source_identity FROM "+tables[kind]+" WHERE namespace=? AND creation_timestamp=? AND uid=?",
				legacy.Namespace, legacy.CreationTimestamp, mustTranslationUUID(t, legacy.UID),
			).Consistency(gocql.Serial).Scan(&legacyIdentity))
			var unproven datastore.TranslationTargetSourceIdentity
			require.NoError(t, json.Unmarshal([]byte(legacyIdentity), &unproven))
			require.Empty(t, unproven.NamespaceUID)
			require.Empty(t, unproven.WriteToken)
			readIdentity := func() datastore.TranslationTargetSourceIdentity {
				var encoded string
				require.NoError(t, raw.Query(
					"SELECT translation_source_identity FROM "+tables[kind]+" WHERE namespace=? AND creation_timestamp=? AND uid=?",
					product.Namespace, product.CreationTimestamp, mustTranslationUUID(t, product.UID),
				).Consistency(gocql.Serial).Scan(&encoded))
				var identity datastore.TranslationTargetSourceIdentity
				require.NoError(t, json.Unmarshal([]byte(encoded), &identity))
				return identity
			}
			initial := readIdentity()
			digest, err := translation.TargetContentDigest(t.Context(), product.APIVersion, product.Kind, product.Spec, product.Body)
			require.NoError(t, err)
			require.Equal(t, namespace.UID, initial.NamespaceUID)
			require.Equal(t, digest, initial.ContentDigest)
			require.Empty(t, initial.WriteToken, "legacy incomplete envelopes cannot claim native fence ownership")
			product.Body, product.Generation, product.ResourceVersion = "changed\r\n", 2, "2"
			require.ErrorIs(t, writeNativeTranslationTarget(t.Context(), store, product, false, "stale"), datastore.ErrConflict)
			require.Equal(t, initial, readIdentity(), "rejected source CAS cannot publish its desired digest")
			require.NoError(t, writeNativeTranslationTarget(t.Context(), store, product, false, "1"))
			changed := readIdentity()
			digest, err = translation.TargetContentDigest(t.Context(), product.APIVersion, product.Kind, product.Spec, product.Body)
			require.NoError(t, err)
			require.Equal(t, namespace.UID, changed.NamespaceUID)
			require.Equal(t, digest, changed.ContentDigest)
			require.NotEqual(t, initial.ContentDigest, changed.ContentDigest)
			product.Status, product.ResourceVersion = json.RawMessage(`{"observedGeneration":2}`), "3"
			require.NoError(t, writeNativeTranslationTarget(t.Context(), store, product, false, "2"))
			require.Equal(t, changed, readIdentity(), "status-only content keeps the exact material identity")
			marker := changed
			marker.WriteToken = newID()
			encodedMarker, err := json.Marshal(marker)
			require.NoError(t, err)
			restore := scylla.RunMutationBeforeForTest(store, "publish-target-source", func() {
				require.NoError(t, raw.Query(
					"UPDATE "+tables[kind]+" SET translation_source_identity=? WHERE namespace=? AND creation_timestamp=? AND uid=?",
					string(encodedMarker), product.Namespace, product.CreationTimestamp, mustTranslationUUID(t, product.UID),
				).Exec())
			})
			product.Body, product.ResourceVersion = "paused writer", "4"
			err = writeNativeTranslationTarget(t.Context(), store, product, false, "3")
			restore()
			require.ErrorIs(t, err, datastore.ErrConflict, "a paused source writer cannot overwrite a newer private ownership marker")
			require.Equal(t, marker, readIdentity())
			var body, version string
			require.NoError(t, raw.Query(
				"SELECT body,resource_version FROM "+tables[kind]+" WHERE namespace=? AND creation_timestamp=? AND uid=?",
				product.Namespace, product.CreationTimestamp, mustTranslationUUID(t, product.UID),
			).Consistency(gocql.Serial).Scan(&body, &version))
			require.Equal(t, "changed\r\n", body)
			require.Equal(t, "3", version)
			product.Body, product.ResourceVersion = body, version
			legacyNamespace := newID()
			previousNamespace := `{"NamespaceUID":"` + legacyNamespace + `"}`
			for _, prior := range []any{previousNamespace, "", nil} {
				require.NoError(t, raw.Query(
					"UPDATE "+tables[kind]+" SET translation_source_identity=? WHERE namespace=? AND creation_timestamp=? AND uid=?",
					prior, product.Namespace, product.CreationTimestamp, mustTranslationUUID(t, product.UID),
				).Exec())
				expected := product.ResourceVersion
				product.Generation++
				product.ResourceVersion, err = datastore.AdvanceTranslationIndexVersion(expected)
				require.NoError(t, err)
				product.Body += "legacy\n"
				require.NoError(t, writeNativeTranslationTarget(t.Context(), store, product, false, expected))
				current := readIdentity()
				if prior == previousNamespace {
					require.Equal(t, legacyNamespace, current.NamespaceUID, "mutation cannot replace original namespace ownership")
				} else {
					require.Empty(t, current.NamespaceUID, "legacy mutation cannot silently adopt today's namespace incarnation")
				}
				require.Empty(t, current.WriteToken)
				digest, err = translation.TargetContentDigest(t.Context(), product.APIVersion, product.Kind, product.Spec, product.Body)
				require.NoError(t, err)
				require.Equal(t, digest, current.ContentDigest)
			}
			fences := store.(datastore.TranslationTargetWriteFenceStore)
			key := datastore.TranslationTargetKey{
				NamespaceUID: namespace.UID, APIVersion: product.APIVersion, Kind: product.Kind, Name: product.Name,
			}
			digest, err = translation.TargetContentDigest(t.Context(), product.APIVersion, product.Kind, product.Spec, product.Body)
			require.NoError(t, err)
			seed, err := fences.BeginTranslationTargetWrite(t.Context(), key, datastore.TranslationTargetWriteIntent{
				ID: newID(), NextUID: product.UID, SourceDigest: digest, CreationTimestamp: product.CreationTimestamp,
			}, "")
			require.NoError(t, err)
			require.NoError(t, fences.CompleteTranslationTargetWrite(t.Context(), key, seed.Token, &catalog.TranslationTargetEvidence{
				APIVersion: product.APIVersion, Kind: product.Kind, Namespace: product.Namespace, Name: product.Name,
				UID: product.UID, Generation: product.Generation, ResourceVersion: product.ResourceVersion, ContentDigest: digest,
			}))
			seedIdentity, err := json.Marshal(datastore.TranslationTargetSourceIdentity{
				NamespaceUID: namespace.UID, WriteToken: seed.Token, ContentDigest: digest,
			})
			require.NoError(t, err)
			require.NoError(t, raw.Query(
				"UPDATE "+tables[kind]+" SET translation_source_identity=? WHERE namespace=? AND creation_timestamp=? AND uid=?",
				string(seedIdentity), product.Namespace, product.CreationTimestamp, mustTranslationUUID(t, product.UID),
			).Exec())
			expected := product.ResourceVersion
			product.Generation++
			product.ResourceVersion, err = datastore.AdvanceTranslationIndexVersion(expected)
			require.NoError(t, err)
			product.Body = "proved writer"
			require.NoError(t, writeNativeTranslationTarget(t.Context(), store, product, false, expected))
			published, err := fences.GetTranslationTargetFence(t.Context(), key)
			require.NoError(t, err)
			require.NotEqual(t, seed.Token, published.Token)
			require.Equal(t, product.ResourceVersion, published.Evidence.ResourceVersion)
			require.Equal(t, published.Token, readIdentity().WriteToken)
			expected = product.ResourceVersion
			product.Generation++
			product.ResourceVersion, err = datastore.AdvanceTranslationIndexVersion(expected)
			require.NoError(t, err)
			product.Body = "committed before interruption"
			restore = scylla.FailMutationBeforeForTest(store, "stabilize-target-source")
			err = writeNativeTranslationTarget(t.Context(), store, product, false, expected)
			restore()
			require.ErrorIs(t, err, datastore.ErrRepairRequired)
			interrupted, err := fences.GetTranslationTargetFence(t.Context(), key)
			require.NoError(t, err)
			require.Equal(t, datastore.TranslationTargetUpdating, interrupted.Phase)
			require.Equal(t, interrupted.Token, readIdentity().WriteToken)
			recovery := store.(interface {
				RecoverTranslationTargetWrite(context.Context, datastore.TranslationTargetKey) error
			})
			require.NoError(t, recovery.RecoverTranslationTargetWrite(t.Context(), key))
			recovered, err := fences.GetTranslationTargetFence(t.Context(), key)
			require.NoError(t, err)
			require.Equal(t, datastore.TranslationTargetStable, recovered.Phase)
			require.Equal(t, product.ResourceVersion, recovered.Evidence.ResourceVersion)
			require.Equal(t, interrupted.Token, recovered.Token)
			if kind == "File" {
				beforeStatus := readIdentity()
				statusResult, err := store.UpdateFileStatus(t.Context(), product.Namespace, product.Name, datastore.FileStatusPatch{ResourceVersion: product.ResourceVersion})
				require.NoError(t, err)
				product.ResourceVersion = statusResult.ResourceVersion
				require.Equal(t, beforeStatus, readIdentity(), "File status writes must preserve material identity")
				afterStatus, err := fences.GetTranslationTargetFence(t.Context(), key)
				require.NoError(t, err)
				require.Equal(t, recovered, afterStatus)
			}
			deletableIdentity, err := json.Marshal(readIdentity())
			require.NoError(t, err)
			for _, corrupt := range []string{
				"{", `{"NamespaceUID":"invalid"}`, string(make([]byte, 513)),
			} {
				require.NoError(t, raw.Query(
					"UPDATE "+tables[kind]+" SET translation_source_identity=? WHERE namespace=? AND creation_timestamp=? AND uid=?",
					corrupt, product.Namespace, product.CreationTimestamp, mustTranslationUUID(t, product.UID),
				).Exec())
				desired := *product
				desired.ResourceVersion, err = datastore.AdvanceTranslationIndexVersion(product.ResourceVersion)
				require.NoError(t, err)
				desired.Body = "cannot overwrite corrupt ownership"
				require.ErrorIs(t, writeNativeTranslationTarget(t.Context(), store, &desired, false, product.ResourceVersion), datastore.ErrRepairRequired)
				require.NoError(t, raw.Query(
					"SELECT body,resource_version FROM "+tables[kind]+" WHERE namespace=? AND creation_timestamp=? AND uid=?",
					product.Namespace, product.CreationTimestamp, mustTranslationUUID(t, product.UID),
				).Consistency(gocql.Serial).Scan(&body, &version))
				require.Equal(t, product.Body, body)
				require.Equal(t, product.ResourceVersion, version)
			}
			require.NoError(t, raw.Query(
				"UPDATE "+tables[kind]+" SET translation_source_identity=? WHERE namespace=? AND creation_timestamp=? AND uid=?",
				string(deletableIdentity), product.Namespace, product.CreationTimestamp, mustTranslationUUID(t, product.UID),
			).Exec())
			if kind == "Product" || kind == "CategoryTaxonomy" {
				if kind == "Product" {
					terminating, markErr := store.(datastore.ProductLifecycleStore).MarkProductTerminating(
						t.Context(), product.UID, product.ResourceVersion, "test/finalizer", time.Now().UTC())
					require.NoError(t, markErr)
					product.ResourceVersion = terminating.ResourceVersion
				} else {
					terminating, markErr := store.(datastore.CategoryTaxonomyDeletionStore).MarkCategoryTaxonomyDeletion(
						t.Context(), product.Namespace, product.Name, product.ResourceVersion, time.Now().UTC())
					require.NoError(t, markErr)
					product.ResourceVersion = terminating.ResourceVersion
				}
				terminating, err := fences.GetTranslationTargetFence(t.Context(), key)
				require.NoError(t, err)
				require.NotEqual(t, recovered.Token, terminating.Token, "termination must invalidate previous material evidence")
				require.Equal(t, product.ResourceVersion, terminating.Evidence.ResourceVersion)
				require.Equal(t, terminating.Token, readIdentity().WriteToken)
			}
			switch kind {
			case "Product":
				err = store.DeleteProduct(t.Context(), product.UID)
			case "ProductVariant":
				err = store.DeleteProductVariant(t.Context(), product.UID)
			case "CategoryTaxonomy":
				err = store.DeleteCategoryTaxonomy(t.Context(), product.UID)
			case "Collection":
				err = store.DeleteCollection(t.Context(), product.UID)
			case "File":
				err = store.DeleteFile(t.Context(), product.UID)
			}
			require.NoError(t, err)
			absent, err := fences.GetTranslationTargetFence(t.Context(), key)
			require.NoError(t, err)
			require.Equal(t, datastore.TranslationTargetStable, absent.Phase)
			require.Nil(t, absent.Evidence, "actual final deletion must vacate proved name evidence before source compaction")
			require.NotEqual(t, recovered.Token, absent.Token)
		})
	}
}

func TestTranslationNativeCreationPublishesSourceOwnedEvidence(t *testing.T) {
	cfg := testScyllaConfig(t)
	cfg.Keyspace += "_target_birth"
	provisionKeyspace(scyllaAddr, cfg.Keyspace)
	t.Cleanup(func() { dropKeyspace(scyllaAddr, cfg.Keyspace) })
	store, err := scylla.New(cfg, zap.NewNop(), 0)
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	namespace := &datastore.Namespace{
		UID: newID(), Name: "birth-" + newID(), Tier: datastore.NamespaceTierOrganization,
		CreationTimestamp: time.Now().UTC().Truncate(time.Millisecond),
	}
	require.NoError(t, store.CreateNamespace(t.Context(), namespace))
	fences := store.(datastore.TranslationTargetWriteFenceStore)
	for _, kind := range []string{"Product", "ProductVariant", "CategoryTaxonomy", "Collection", "File"} {
		t.Run(kind, func(t *testing.T) {
			product := newProduct(namespace.Name, "birth-"+kind)
			product.Kind, product.Generation, product.ResourceVersion = kind, 1, "1"
			product.Spec, product.Body = json.RawMessage(`{"title":"Birth"}`), "exact birth\r\n"
			if kind == "File" {
				product.APIVersion = "storage.gitstore.dev/v1beta1"
			}
			require.NoError(t, writeNativeTranslationTarget(t.Context(), store, product, true, ""))
			key := datastore.TranslationTargetKey{
				NamespaceUID: namespace.UID, APIVersion: product.APIVersion, Kind: kind, Name: product.Name,
			}
			born, err := fences.GetTranslationTargetFence(t.Context(), key)
			require.NoError(t, err)
			require.Equal(t, datastore.TranslationTargetStable, born.Phase)
			require.Equal(t, product.UID, born.Evidence.UID)
			require.Equal(t, product.CreationTimestamp, born.CreationTimestamp)
			product.Body, product.Generation, product.ResourceVersion = "updated birth", 2, "2"
			require.NoError(t, writeNativeTranslationTarget(t.Context(), store, product, false, "1"))
			updated, err := fences.GetTranslationTargetFence(t.Context(), key)
			require.NoError(t, err)
			require.NotEqual(t, born.Token, updated.Token)
			require.Equal(t, "2", updated.Evidence.ResourceVersion)
			if kind == "File" {
				product.Body = "legacy equal-version material"
				restore := scylla.FailMutationBeforeForTest(store, "stabilize-target-source")
				err := writeNativeTranslationTarget(t.Context(), store, product, false, "2")
				restore()
				require.ErrorIs(t, err, datastore.ErrRepairRequired)
				recovery := store.(interface {
					RecoverTranslationTargetWrite(context.Context, datastore.TranslationTargetKey) error
				})
				require.NoError(t, recovery.RecoverTranslationTargetWrite(t.Context(), key))
				invalidated, err := fences.GetTranslationTargetFence(t.Context(), key)
				require.NoError(t, err)
				require.NotEqual(t, updated.Token, invalidated.Token, "legacy material writes must still invalidate the previous fence")
				require.Nil(t, invalidated.Evidence, "non-advancing material cannot retain positive evidence")
				product.Body = "another legacy equal-version material"
				require.NoError(t, writeNativeTranslationTarget(t.Context(), store, product, false, "2"))
			}
			require.NoError(t, deleteNativeTranslationTarget(t.Context(), store, kind, product.UID))
			absent, err := fences.GetTranslationTargetFence(t.Context(), key)
			require.NoError(t, err)
			require.Nil(t, absent.Evidence)
			replacement := *product
			replacement.UID, replacement.Generation, replacement.ResourceVersion = newID(), 1, "1"
			require.NoError(t, writeNativeTranslationTarget(t.Context(), store, &replacement, true, ""))
			reused, err := fences.GetTranslationTargetFence(t.Context(), key)
			require.NoError(t, err)
			require.Equal(t, replacement.UID, reused.Evidence.UID)
			require.NotEqual(t, absent.Token, reused.Token)
		})
	}
}

func TestTranslationNativeLegacyWritersDoNotInferNamespaceOwnership(t *testing.T) {
	cfg := testScyllaConfig(t)
	cfg.Keyspace += "_target_legacy"
	provisionKeyspace(scyllaAddr, cfg.Keyspace)
	t.Cleanup(func() { dropKeyspace(scyllaAddr, cfg.Keyspace) })
	backend, err := scylla.New(cfg, zap.NewNop(), 0)
	require.NoError(t, err)
	t.Cleanup(func() { _ = backend.Close() })
	raw := newRawSessionForKeyspace(t, cfg.Keyspace)
	namespace := &datastore.Namespace{
		UID: newID(), Name: "legacy-" + newID(), Tier: datastore.NamespaceTierOrganization,
		CreationTimestamp: time.Now().UTC().Truncate(time.Millisecond),
	}
	require.NoError(t, backend.CreateNamespace(t.Context(), namespace))
	fences := backend.(datastore.TranslationTargetWriteFenceStore)
	for _, target := range []struct{ kind, table, uidTable, nameTable string }{
		{"Product", "products_by_key", "products_by_uid", "products_by_identity"},
		{"ProductVariant", "product_variants_by_namespace", "product_variants_by_uid", "product_variants_by_name"},
		{"CategoryTaxonomy", "category_taxonomies_by_namespace", "category_taxonomies_by_uid", "category_taxonomies_by_name"},
		{"Collection", "collections_by_namespace", "collections_by_uid", "collections_by_name"},
		{"File", "files_by_namespace", "files_by_uid", "files_by_name"},
	} {
		for _, state := range []string{"missing", "empty", "corrupt"} {
			t.Run(target.kind+"/"+state, func(t *testing.T) {
				product := newProduct(namespace.Name, "legacy-"+state)
				product.APIVersion, product.Kind = "catalog.gitstore.dev/v1beta1", target.kind
				if target.kind == "File" {
					product.APIVersion = "storage.gitstore.dev/v1beta1"
				}
				product.Generation, product.ResourceVersion = 1, "1"
				product.CreationTimestamp = product.CreationTimestamp.UTC().Truncate(time.Millisecond)
				var previous any
				switch state {
				case "empty":
					previous = ""
				case "corrupt":
					previous = `{"NamespaceUID":"not-a-uuid"}`
				}
				uid := mustTranslationUUID(t, product.UID)
				require.NoError(t, raw.Query(
					"INSERT INTO "+target.table+" (namespace,creation_timestamp,uid,name,api_version,kind,generation,resource_version,spec,body,translation_source_identity) VALUES (?,?,?,?,?,?,?,?,?,?,?)",
					namespace.Name, product.CreationTimestamp, uid, product.Name, product.APIVersion, target.kind,
					product.Generation, product.ResourceVersion, string(product.Spec), product.Body, previous,
				).Exec())
				require.NoError(t, raw.Query(
					"INSERT INTO "+target.uidTable+" (uid,namespace,creation_timestamp) VALUES (?,?,?)",
					uid, namespace.Name, product.CreationTimestamp,
				).Exec())
				require.NoError(t, raw.Query(
					"INSERT INTO "+target.nameTable+" (namespace,name,uid,creation_timestamp) VALUES (?,?,?,?)",
					namespace.Name, product.Name, uid, product.CreationTimestamp,
				).Exec())
				product.Generation, product.ResourceVersion, product.Body = 2, "2", "updated legacy material"
				err := writeNativeTranslationTarget(t.Context(), backend, product, false, "1")
				if state == "corrupt" {
					require.ErrorIs(t, err, datastore.ErrRepairRequired)
					var version string
					require.NoError(t, raw.Query(
						"SELECT resource_version FROM "+target.table+" WHERE namespace=? AND creation_timestamp=? AND uid=?",
						namespace.Name, product.CreationTimestamp, uid,
					).Scan(&version))
					require.Equal(t, "1", version, "unprovable ownership cannot mutate the source")
				} else {
					require.NoError(t, err)
					var identity string
					require.NoError(t, raw.Query(
						"SELECT translation_source_identity FROM "+target.table+" WHERE namespace=? AND creation_timestamp=? AND uid=?",
						namespace.Name, product.CreationTimestamp, uid,
					).Scan(&identity))
					var proof datastore.TranslationTargetSourceIdentity
					require.NoError(t, json.Unmarshal([]byte(identity), &proof))
					require.Empty(t, proof.NamespaceUID, "today's namespace cannot establish the legacy source's incarnation")
					require.Empty(t, proof.WriteToken)
					require.NoError(t, deleteNativeTranslationTarget(t.Context(), backend, target.kind, product.UID))
					var retired bool
					require.NoError(t, raw.Query(
						"SELECT retired FROM translation_target_tombstones_by_uid WHERE kind=? AND uid=?",
						target.kind, uid,
					).Scan(&retired))
					require.True(t, retired, "legacy deletion must retain immutable UID retirement")
				}
				_, err = fences.GetTranslationTargetFence(t.Context(), datastore.TranslationTargetKey{
					NamespaceUID: namespace.UID, APIVersion: product.APIVersion, Kind: target.kind, Name: product.Name,
				})
				require.ErrorIs(t, err, datastore.ErrNotFound, "unowned legacy material cannot publish positive evidence")
			})
		}
	}
}

func TestTranslationNativeCreationRecoveryAndCompensation(t *testing.T) {
	cfg := testScyllaConfig(t)
	cfg.Keyspace += "_target_birth_recovery"
	provisionKeyspace(scyllaAddr, cfg.Keyspace)
	t.Cleanup(func() { dropKeyspace(scyllaAddr, cfg.Keyspace) })
	first, err := scylla.New(cfg, zap.NewNop(), 0)
	require.NoError(t, err)
	t.Cleanup(func() { _ = first.Close() })
	second, err := scylla.New(cfg, zap.NewNop(), 0)
	require.NoError(t, err)
	t.Cleanup(func() { _ = second.Close() })
	recovery := second.(interface {
		RecoverTranslationTargetWrite(context.Context, datastore.TranslationTargetKey) error
	})
	namespace := &datastore.Namespace{
		UID: newID(), Name: "birth-recovery-" + newID(), Tier: datastore.NamespaceTierOrganization,
		CreationTimestamp: time.Now().UTC().Truncate(time.Millisecond),
	}
	require.NoError(t, first.CreateNamespace(t.Context(), namespace))
	fences := second.(datastore.TranslationTargetWriteFenceStore)
	raw := newRawSessionForKeyspace(t, cfg.Keyspace)
	for _, step := range []string{"publish-target-source", "stabilize-target-source", "write-owner-references"} {
		t.Run(step, func(t *testing.T) {
			product := newProduct(namespace.Name, step)
			product.Generation, product.ResourceVersion = 1, "1"
			key := datastore.TranslationTargetKey{
				NamespaceUID: namespace.UID, APIVersion: product.APIVersion, Kind: product.Kind, Name: product.Name,
			}
			restore := scylla.FailMutationBeforeForTest(first, step)
			err := first.CreateProduct(t.Context(), product)
			restore()
			require.Error(t, err)
			if step == "publish-target-source" {
				restore = scylla.FailMutationBeforeForTest(second, "publish-target-abort")
				err = recovery.RecoverTranslationTargetWrite(t.Context(), key)
				restore()
				require.Error(t, err, "negative source proof must survive interrupted absence publication")
				var retired bool
				require.NoError(t, raw.Query(
					"SELECT translation_retired FROM products_by_key WHERE namespace=? AND creation_timestamp=? AND uid=?",
					product.Namespace, product.CreationTimestamp, mustTranslationUUID(t, product.UID),
				).Consistency(gocql.Serial).Scan(&retired))
				require.True(t, retired, "a missing source read alone cannot cancel a paused INSERT")
			}
			require.NoError(t, recovery.RecoverTranslationTargetWrite(t.Context(), key))
			stable, err := fences.GetTranslationTargetFence(t.Context(), key)
			require.NoError(t, err)
			require.Equal(t, datastore.TranslationTargetStable, stable.Phase)
			if step == "stabilize-target-source" {
				require.Equal(t, product.UID, stable.Evidence.UID)
				repair, err := scylla.OpenProjectionRepairService(cfg)
				require.NoError(t, err)
				defer repair.Close()
				plan, err := repair.Audit(t.Context())
				require.NoError(t, err)
				result, err := repair.Apply(t.Context(), plan)
				require.NoError(t, err, "remaining findings: %+v", result.Verification.Findings)
				live, err := second.GetProduct(t.Context(), product.UID)
				require.NoError(t, err)
				require.Equal(t, product.Name, live.Name)
			} else {
				require.Nil(t, stable.Evidence, "birth cancellation or rollback must leave stable name absence")
				_, err := second.GetProduct(t.Context(), product.UID)
				require.ErrorIs(t, err, datastore.ErrNotFound)
				replacement := *product
				replacement.UID = newID()
				require.NoError(t, second.CreateProduct(t.Context(), &replacement))
				reused, err := fences.GetTranslationTargetFence(t.Context(), key)
				require.NoError(t, err)
				require.Equal(t, replacement.UID, reused.Evidence.UID)
			}
		})
	}
	t.Run("paused-insert", func(t *testing.T) {
		product := newProduct(namespace.Name, "paused-insert")
		product.Generation, product.ResourceVersion = 1, "1"
		key := datastore.TranslationTargetKey{
			NamespaceUID: namespace.UID, APIVersion: product.APIVersion, Kind: product.Kind, Name: product.Name,
		}
		restore := scylla.RunMutationBeforeForTest(first, "publish-target-source", func() {
			require.NoError(t, recovery.RecoverTranslationTargetWrite(t.Context(), key))
		})
		err := first.CreateProduct(t.Context(), product)
		restore()
		require.ErrorIs(t, err, datastore.ErrRepairRequired, "a resumed INSERT cannot complete a durably canceled birth")
		canceled, err := fences.GetTranslationTargetFence(t.Context(), key)
		require.NoError(t, err)
		require.Nil(t, canceled.Evidence)
		replacement := *product
		replacement.UID = newID()
		require.NoError(t, second.CreateProduct(t.Context(), &replacement))
		newOwner, err := fences.GetTranslationTargetFence(t.Context(), key)
		require.NoError(t, err)
		require.Equal(t, replacement.UID, newOwner.Evidence.UID)
		require.NoError(t, raw.Query(
			"INSERT INTO products_by_uid (uid,namespace,creation_timestamp) VALUES (?,?,?)",
			mustTranslationUUID(t, product.UID), product.Namespace, product.CreationTimestamp,
		).Exec())
		_, err = second.GetProduct(t.Context(), product.UID)
		require.ErrorIs(t, err, datastore.ErrNotFound, "retained UID retirement, not missing lookup hints, hides the late source")
		repair, err := scylla.OpenProjectionRepairService(cfg)
		require.NoError(t, err)
		defer repair.Close()
		plan, err := repair.Audit(t.Context())
		require.NoError(t, err)
		result, err := repair.Apply(t.Context(), plan)
		require.NoError(t, err, "remaining findings: %+v", result.Verification.Findings)
		afterRepair, err := fences.GetTranslationTargetFence(t.Context(), key)
		require.NoError(t, err)
		require.Equal(t, newOwner, afterRepair, "retired old source cleanup must not vacate replacement evidence")
	})
	file := datastore.File(*newProduct(namespace.Name, "failed-file"))
	file.APIVersion, file.Kind, file.Generation, file.ResourceVersion = "storage.gitstore.dev/v1beta1", "File", 1, "1"
	file.OwnerReferences = json.RawMessage("{")
	require.Error(t, first.CreateFile(t.Context(), &file))
	fileFence, err := fences.GetTranslationTargetFence(t.Context(), datastore.TranslationTargetKey{
		NamespaceUID: namespace.UID, APIVersion: file.APIVersion, Kind: file.Kind, Name: file.Name,
	})
	require.NoError(t, err)
	require.Nil(t, fileFence.Evidence, "File owner-projection rollback must retire its UID and vacate positive evidence")
	for _, target := range []struct{ kind, table string }{
		{"Product", "products_by_key"}, {"ProductVariant", "product_variants_by_namespace"},
		{"CategoryTaxonomy", "category_taxonomies_by_namespace"}, {"Collection", "collections_by_namespace"},
		{"File", "files_by_namespace"},
	} {
		t.Run("source-cas-ownership/"+target.kind, func(t *testing.T) {
			product := newProduct(namespace.Name, "source-cas-owner-"+target.kind)
			product.Kind = target.kind
			if target.kind == "File" {
				product.APIVersion = "storage.gitstore.dev/v1beta1"
			}
			product.Generation, product.ResourceVersion, product.Body = 1, "1", "original material"
			require.NoError(t, writeNativeTranslationTarget(t.Context(), first, product, true, ""))
			var before, owner string
			require.NoError(t, raw.Query(
				"SELECT translation_epoch,translation_owner FROM translation_target_mutations_by_namespace WHERE namespace=?",
				namespace.Name,
			).Consistency(gocql.Serial).Scan(&before, &owner))
			require.NotEmpty(t, before, "every owned source CAS must advance its namespace clock")
			require.Empty(t, owner)
			called := false
			restore := scylla.RunMutationBeforeForTest(first, "commit-target-source", func() {
				called = true
				epoch, err := second.(datastore.TranslationTargetMutationEpochStore).
					ReadTranslationTargetMutationEpoch(t.Context(), namespace.UID)
				require.ErrorIs(t, err, datastore.ErrConflict, "a live grant is not an idle mutation observation")
				require.Empty(t, epoch)
				require.NoError(t, second.(interface {
					RecoverTranslationTargetMutation(context.Context, string) error
				}).RecoverTranslationTargetMutation(t.Context(), namespace.UID))
			})
			product.Generation, product.ResourceVersion, product.Body = 2, "2", "paused material"
			err := writeNativeTranslationTarget(t.Context(), first, product, false, "1")
			restore()
			require.True(t, called)
			require.ErrorIs(t, err, datastore.ErrConflict)
			var body, version string
			read := func() {
				require.NoError(t, raw.Query(
					"SELECT body,resource_version FROM "+target.table+" WHERE namespace=? AND creation_timestamp=? AND uid=?",
					namespace.Name, product.CreationTimestamp, mustTranslationUUID(t, product.UID),
				).Consistency(gocql.Serial).Scan(&body, &version))
			}
			read()
			require.Equal(t, "original material", body)
			require.Equal(t, "1", version)
			var after string
			require.NoError(t, raw.Query(
				"SELECT translation_epoch,translation_owner FROM translation_target_mutations_by_namespace WHERE namespace=?",
				namespace.Name,
			).Consistency(gocql.Serial).Scan(&after, &owner))
			require.NotEqual(t, before, after)
			require.Empty(t, owner)
			restore = scylla.RunMutationBeforeForTest(first, "commit-target-source", func() {
				require.NoError(t, second.(interface {
					RecoverTranslationTargetMutation(context.Context, string) error
				}).RecoverTranslationTargetMutation(t.Context(), namespace.UID))
			})
			err = deleteNativeTranslationTarget(t.Context(), first, target.kind, product.UID)
			restore()
			require.ErrorIs(t, err, datastore.ErrConflict)
			read()
			require.Equal(t, "original material", body)
			require.Equal(t, "1", version)

			restore = scylla.FailMutationBeforeForTest(first, "release-target-source")
			err = writeNativeTranslationTarget(t.Context(), first, product, false, "1")
			restore()
			require.Error(t, err)
			require.NoError(t, second.(interface {
				RecoverTranslationTargetMutation(context.Context, string) error
			}).RecoverTranslationTargetMutation(t.Context(), namespace.UID))
			require.NoError(t, recovery.RecoverTranslationTargetWrite(t.Context(), datastore.TranslationTargetKey{
				NamespaceUID: namespace.UID, APIVersion: product.APIVersion, Kind: target.kind, Name: product.Name,
			}))
			read()
			require.Equal(t, "paused material", body, "a committed CAS is retained when clock release was interrupted")
			require.Equal(t, "2", version)

			birth := *product
			birth.UID, birth.Name, birth.Generation, birth.ResourceVersion = newID(), "canceled-"+target.kind, 1, "1"
			restore = scylla.RunMutationBeforeForTest(first, "commit-target-source", func() {
				require.NoError(t, second.(interface {
					RecoverTranslationTargetMutation(context.Context, string) error
				}).RecoverTranslationTargetMutation(t.Context(), namespace.UID))
			})
			err = writeNativeTranslationTarget(t.Context(), first, &birth, true, "")
			restore()
			require.Error(t, err)
			var retired bool
			require.NoError(t, raw.Query(
				"SELECT retired FROM translation_target_tombstones_by_uid WHERE kind=? AND uid=?",
				target.kind, mustTranslationUUID(t, birth.UID),
			).Consistency(gocql.Serial).Scan(&retired))
			require.True(t, retired, "revocation must fence a source INSERT that has not run yet")
		})
	}
	t.Run("source-backfill-recovery", func(t *testing.T) {
		for _, kind := range []string{"Product", "ProductVariant", "CategoryTaxonomy", "Collection", "File"} {
			for _, window := range []string{"paused", "committed", "changed", "replaced"} {
				t.Run(kind+"/"+window, func(t *testing.T) {
					product := newProduct(namespace.Name, "backfill-"+kind+"-"+window)
					product.Kind, product.Generation, product.ResourceVersion = kind, 0, ""
					if kind == "File" {
						product.APIVersion = "storage.gitstore.dev/v1beta1"
					}
					require.NoError(t, writeNativeTranslationTarget(t.Context(), first, product, true, ""))
					product.Generation, product.ResourceVersion, product.Body = 1, "1", "retained\r\nlegacy body"
					require.NoError(t, writeNativeTranslationTarget(t.Context(), first, product, false, ""))
					hint := datastore.TranslationTargetSourceAuditHint{
						Target: datastore.TranslationTargetKey{
							NamespaceUID: namespace.UID, APIVersion: product.APIVersion, Kind: kind, Name: product.Name,
						},
						Namespace: namespace.Name, UID: product.UID, CreationTimestamp: product.CreationTimestamp, ResourceVersion: "1",
					}
					coverage := first.(datastore.TranslationTargetCoverageEpochStore)
					beforeCoverage, err := coverage.ReadTranslationTargetCoverageEpoch(t.Context(), namespace.UID)
					require.NoError(t, err)
					var restore func()
					if window != "committed" {
						restore = scylla.RunMutationBeforeForTest(first, "commit-target-source", func() {
							epoch, err := second.(datastore.TranslationTargetCoverageEpochStore).
								ReadTranslationTargetCoverageEpoch(t.Context(), namespace.UID)
							require.ErrorIs(t, err, datastore.ErrConflict)
							require.Empty(t, epoch)
							if window == "paused" {
								progress, err := second.(datastore.TranslationTargetAuditStore).
									RepairTranslationTargetAuditTurn(t.Context(), namespace.UID, 256)
								require.ErrorIs(t, err, datastore.ErrConflict)
								require.Empty(t, progress)
							} else {
								require.ErrorIs(t, second.(datastore.TranslationTargetSourceAuditStore).
									RecoverTranslationTargetSourceAudit(t.Context(), hint), datastore.ErrConflict)
							}
							if window == "changed" {
								product.Generation, product.ResourceVersion, product.Body = 2, "2", "intervening\r\nmaterial"
								require.NoError(t, writeNativeTranslationTarget(t.Context(), second, product, false, "1"))
							}
							if window == "replaced" {
								retiredUID := product.UID
								require.NoError(t, deleteNativeTranslationTarget(t.Context(), second, kind, retiredUID))
								replacement := *product
								replacement.UID, replacement.CreationTimestamp = newID(), time.Now().UTC().Truncate(time.Millisecond)
								replacement.Body = "replacement\r\nmaterial"
								require.NoError(t, writeNativeTranslationTarget(t.Context(), second, &replacement, true, ""))
								var retired bool
								require.NoError(t, raw.Query(
									"SELECT retired FROM translation_target_tombstones_by_uid WHERE kind=? AND uid=?",
									kind, mustTranslationUUID(t, retiredUID),
								).Consistency(gocql.Serial).Scan(&retired))
								require.True(t, retired)
								product = &replacement
							}
						})
					} else {
						restore = scylla.FailMutationBeforeForTest(first, "release-target-source")
					}
					err = first.(datastore.TranslationTargetSourceAuditStore).RecoverTranslationTargetSourceAudit(t.Context(), hint)
					restore()
					require.Error(t, err)
					require.NoError(t, second.(interface {
						RecoverTranslationTargetMutation(context.Context, string) error
					}).RecoverTranslationTargetMutation(t.Context(), namespace.UID))
					require.NoError(t, recovery.RecoverTranslationTargetWrite(t.Context(), hint.Target))
					afterCoverage, err := coverage.ReadTranslationTargetCoverageEpoch(t.Context(), namespace.UID)
					require.NoError(t, err)
					require.NotEqual(t, beforeCoverage, afterCoverage, "unproved-source backfill and its cancellation must invalidate coverage")
					if window == "paused" || window == "changed" {
						head, err := fences.GetTranslationTargetFence(t.Context(), hint.Target)
						require.NoError(t, err)
						require.Nil(t, head.Evidence, "canceling backfill cannot invent previously proven material")
						var retired bool
						require.NoError(t, raw.Query(
							"SELECT retired FROM translation_target_tombstones_by_uid WHERE kind=? AND uid=?",
							kind, mustTranslationUUID(t, product.UID),
						).Consistency(gocql.Serial).Scan(&retired))
						require.False(t, retired, "canceling proof construction cannot retire the existing source")
						hint.ResourceVersion = product.ResourceVersion
						require.NoError(t, second.(datastore.TranslationTargetSourceAuditStore).
							RecoverTranslationTargetSourceAudit(t.Context(), hint))
					}
					evidence, err := second.(datastore.TranslationTargetFenceStore).ReadTargetEvidence(t.Context(), hint.Target)
					require.NoError(t, err)
					require.Equal(t, product.UID, evidence.UID)
					require.Equal(t, product.ResourceVersion, evidence.ResourceVersion)
					var body string
					require.NoError(t, raw.Query(
						"SELECT body FROM "+map[string]string{
							"Product": "products_by_key", "ProductVariant": "product_variants_by_namespace",
							"CategoryTaxonomy": "category_taxonomies_by_namespace", "Collection": "collections_by_namespace", "File": "files_by_namespace",
						}[kind]+" WHERE namespace=? AND creation_timestamp=? AND uid=?",
						product.Namespace, product.CreationTimestamp, mustTranslationUUID(t, product.UID),
					).Consistency(gocql.Serial).Scan(&body))
					require.Equal(t, product.Body, body)
				})
			}
		}
	})
	t.Run("unowned-pre-feature-identity", func(t *testing.T) {
		for _, mode := range []string{"missing", "empty"} {
			t.Run(mode, func(t *testing.T) {
				namespace := &datastore.Namespace{
					UID: newID(), Name: "null-source-" + newID(), Tier: datastore.NamespaceTierOrganization,
					CreationTimestamp: time.Now().UTC().Truncate(time.Millisecond),
				}
				product := newProduct(namespace.Name, "null-source")
				product.Kind, product.APIVersion = "File", "storage.gitstore.dev/v1beta1"
				product.Generation, product.ResourceVersion, product.Body = 1, "1", "pre-feature material"
				require.NoError(t, writeNativeTranslationTarget(t.Context(), first, product, true, ""))
				var previous any
				if mode == "empty" {
					previous = ""
				}
				require.NoError(t, raw.Query(
					"UPDATE files_by_namespace SET translation_source_identity=? WHERE namespace=? AND creation_timestamp=? AND uid=?",
					previous, product.Namespace, product.CreationTimestamp, mustTranslationUUID(t, product.UID),
				).Exec())
				require.NoError(t, second.CreateNamespace(t.Context(), namespace))
				assertUnowned := func() {
					var encoded string
					require.NoError(t, raw.Query(
						"SELECT translation_source_identity FROM files_by_namespace WHERE namespace=? AND creation_timestamp=? AND uid=?",
						product.Namespace, product.CreationTimestamp, mustTranslationUUID(t, product.UID),
					).Consistency(gocql.Serial).Scan(&encoded))
					var source datastore.TranslationTargetSourceIdentity
					require.NoError(t, json.Unmarshal([]byte(encoded), &source))
					require.Empty(t, source.NamespaceUID, "source cancellation must not adopt today's Namespace")
				}
				restore := scylla.RunMutationBeforeForTest(first, "commit-target-source", func() {
					epoch, err := second.(datastore.TranslationTargetMutationEpochStore).
						ReadTranslationTargetMutationEpoch(t.Context(), namespace.UID)
					require.ErrorIs(t, err, datastore.ErrConflict)
					require.Empty(t, epoch)
					require.NoError(t, second.(interface {
						RecoverTranslationTargetMutation(context.Context, string) error
					}).RecoverTranslationTargetMutation(t.Context(), namespace.UID))
				})
				product.Body = "paused legacy material"
				err := writeNativeTranslationTarget(t.Context(), first, product, false, "1")
				restore()
				require.ErrorIs(t, err, datastore.ErrConflict)
				current, err := second.GetFile(t.Context(), product.UID)
				require.NoError(t, err)
				require.Equal(t, "pre-feature material", current.Body)
				assertUnowned()
				require.NoError(t, writeNativeTranslationTarget(t.Context(), second, product, false, "1"))
				current, err = second.GetFile(t.Context(), product.UID)
				require.NoError(t, err)
				require.Equal(t, product.Body, current.Body, "unowned legacy CRUD remains available after revocation")
				assertUnowned()
			})
		}
	})
	t.Run("unproved-same-version-aba", func(t *testing.T) {
		product := newProduct(namespace.Name, "unproved-aba")
		product.Kind, product.APIVersion = "File", "storage.gitstore.dev/v1beta1"
		product.Generation, product.ResourceVersion = 1, "1"
		require.NoError(t, writeNativeTranslationTarget(t.Context(), first, product, true, ""))
		product.Body = "original legacy material"
		require.NoError(t, writeNativeTranslationTarget(t.Context(), first, product, false, "1"))
		require.NoError(t, writeNativeTranslationTarget(t.Context(), first, product, false, "1"))
		paused := *product
		paused.Body = "paused legacy material"
		restore := scylla.RunMutationBeforeForTest(first, "commit-target-source", func() {
			require.NoError(t, second.(interface {
				RecoverTranslationTargetMutation(context.Context, string) error
			}).RecoverTranslationTargetMutation(t.Context(), namespace.UID))
			intervening := *product
			intervening.Body = "intervening legacy material"
			require.NoError(t, writeNativeTranslationTarget(t.Context(), second, &intervening, false, "1"))
			require.NoError(t, writeNativeTranslationTarget(t.Context(), second, product, false, "1"))
		})
		err := writeNativeTranslationTarget(t.Context(), first, &paused, false, "1")
		restore()
		require.ErrorIs(t, err, datastore.ErrConflict, "restoring equal-version material must not restore a paused writer's private CAS authority")
		current, err := second.GetFile(t.Context(), product.UID)
		require.NoError(t, err)
		require.Equal(t, product.Body, current.Body)
		require.Equal(t, "1", current.ResourceVersion)
	})
	t.Run("compensation-acquires-new-source-epoch", func(t *testing.T) {
		product := newProduct(namespace.Name, "compensation-source-clock")
		product.OwnerReferences = json.RawMessage("{")
		var before, after, owner string
		restore := scylla.RunMutationBeforeForTest(first, "retire-target-source", func() {
			require.NoError(t, raw.Query(
				"SELECT translation_epoch,translation_owner FROM translation_target_mutations_by_namespace WHERE namespace=?",
				namespace.Name,
			).Consistency(gocql.Serial).Scan(&before, &owner))
			require.NotEmpty(t, before)
			require.Empty(t, owner, "the creation source CAS has already released its own grant")
		})
		err := first.CreateProduct(t.Context(), product)
		restore()
		require.Error(t, err)
		require.NotEmpty(t, before, "the failed owner projection must compensate its committed source")
		require.NoError(t, raw.Query(
			"SELECT translation_epoch,translation_owner FROM translation_target_mutations_by_namespace WHERE namespace=?",
			namespace.Name,
		).Consistency(gocql.Serial).Scan(&after, &owner))
		require.NotEqual(t, before, after, "compensation cannot reuse the creation CAS's completed grant")
		require.Empty(t, owner)
	})
	t.Run("replacement-recovers-abandoned-source-grant", func(t *testing.T) {
		product := newProduct(namespace.Name, "abandoned-source-grant")
		product.Generation, product.ResourceVersion = 1, "1"
		restore := scylla.FailMutationBeforeForTest(first, "commit-target-source")
		err := first.CreateProduct(t.Context(), product)
		restore()
		require.Error(t, err)
		replacement := *product
		replacement.UID, replacement.Name = newID(), "replacement-source-grant"
		require.NoError(t, second.CreateProduct(t.Context(), &replacement))
		var retired bool
		require.NoError(t, raw.Query(
			"SELECT retired FROM translation_target_tombstones_by_uid WHERE kind=? AND uid=?",
			"Product", mustTranslationUUID(t, product.UID),
		).Consistency(gocql.Serial).Scan(&retired))
		require.True(t, retired, "waiting alone cannot release an abandoned source grant")
	})
	t.Run("malformed-source-grant-stays-fenced", func(t *testing.T) {
		product := newProduct(namespace.Name, "malformed-source-grant")
		product.Generation, product.ResourceVersion = 1, "1"
		restore := scylla.FailMutationBeforeForTest(first, "commit-target-source")
		err := first.CreateProduct(t.Context(), product)
		restore()
		require.Error(t, err)
		var original string
		require.NoError(t, raw.Query(
			"SELECT translation_owner FROM translation_target_mutations_by_namespace WHERE namespace=?",
			namespace.Name,
		).Consistency(gocql.Serial).Scan(&original))
		var foreign map[string]any
		require.NoError(t, json.Unmarshal([]byte(original), &foreign))
		foreign["NamespaceUID"] = newID()
		encoded, err := json.Marshal(foreign)
		require.NoError(t, err)
		for _, owner := range []string{"{", strings.Repeat("x", 4<<10+1), string(encoded)} {
			require.NoError(t, raw.Query(
				"UPDATE translation_target_mutations_by_namespace SET translation_owner=? WHERE namespace=?",
				owner, namespace.Name,
			).Exec())
			err := second.(interface {
				RecoverTranslationTargetMutation(context.Context, string) error
			}).RecoverTranslationTargetMutation(t.Context(), namespace.UID)
			require.ErrorIs(t, err, datastore.ErrRepairRequired)
			epoch, err := second.(datastore.TranslationTargetMutationEpochStore).
				ReadTranslationTargetMutationEpoch(t.Context(), namespace.UID)
			require.ErrorIs(t, err, datastore.ErrRepairRequired)
			require.Empty(t, epoch)
			var retained string
			require.NoError(t, raw.Query(
				"SELECT translation_owner FROM translation_target_mutations_by_namespace WHERE namespace=?",
				namespace.Name,
			).Consistency(gocql.Serial).Scan(&retained))
			require.Equal(t, owner, retained, "invalid ownership is not permission to clear pending work")
		}
		require.NoError(t, raw.Query(
			"UPDATE translation_target_mutations_by_namespace SET translation_owner=? WHERE namespace=?",
			original, namespace.Name,
		).Exec())
		require.NoError(t, raw.Query(
			"INSERT INTO namespace_catalog_fences_by_uid (uid,epoch,pending) VALUES (?,?,1)",
			mustTranslationUUID(t, namespace.UID), int64(1),
		).Exec())
		require.NoError(t, second.(interface {
			RecoverTranslationTargetMutation(context.Context, string) error
		}).RecoverTranslationTargetMutation(t.Context(), namespace.UID))
		var pending int64
		require.NoError(t, raw.Query(
			"SELECT pending FROM namespace_catalog_fences_by_uid WHERE uid=?",
			mustTranslationUUID(t, namespace.UID),
		).Consistency(gocql.Serial).Scan(&pending))
		require.EqualValues(t, 1, pending, "source-step recovery cannot release an unrelated callback-wide reservation")
		require.NoError(t, raw.Query(
			"UPDATE namespace_catalog_fences_by_uid SET pending=0 WHERE uid=?",
			mustTranslationUUID(t, namespace.UID),
		).Exec())
	})
	t.Run("independent-stores-share-source-clock", func(t *testing.T) {
		namespace := &datastore.Namespace{
			UID: newID(), Name: "shared-clock-" + newID(), Tier: datastore.NamespaceTierOrganization,
			CreationTimestamp: time.Now().UTC().Truncate(time.Millisecond),
		}
		require.NoError(t, first.CreateNamespace(t.Context(), namespace))
		var workers sync.WaitGroup
		results := make(chan error, 10)
		kinds := []string{"Product", "ProductVariant", "CategoryTaxonomy", "Collection", "File"}
		for i := range 10 {
			product := newProduct(namespace.Name, fmt.Sprintf("concurrent-source-%d", i))
			// Unproved births isolate this clock from target-discovery allocation contention.
			product.Kind, product.Generation, product.ResourceVersion = kinds[i%len(kinds)], 0, ""
			if product.Kind == "File" {
				product.APIVersion = "storage.gitstore.dev/v1beta1"
			}
			store := first
			if i%2 == 0 {
				store = second
			}
			workers.Go(func() {
				results <- writeNativeTranslationTarget(t.Context(), store, product, true, "")
			})
		}
		workers.Wait()
		close(results)
		for err := range results {
			require.NoError(t, err)
		}
		var epoch, owner string
		require.NoError(t, raw.Query(
			"SELECT translation_epoch,translation_owner FROM translation_target_mutations_by_namespace WHERE namespace=?",
			namespace.Name,
		).Consistency(gocql.Serial).Scan(&epoch, &owner))
		require.NotEmpty(t, epoch)
		require.Empty(t, owner)
	})
	for _, kind := range []string{"Product", "ProductVariant", "CategoryTaxonomy", "Collection", "File"} {
		t.Run("unproved-rollback/"+kind, func(t *testing.T) {
			product := newProduct(namespace.Name, "unproved-rollback-"+kind)
			product.Kind, product.Generation, product.ResourceVersion = kind, 0, ""
			if kind == "File" {
				product.APIVersion = "storage.gitstore.dev/v1beta1"
				product.OwnerReferences = json.RawMessage("{")
			}
			restore := scylla.FailMutationBeforeForTest(first, "write-owner-references")
			err := writeNativeTranslationTarget(t.Context(), first, product, true, "")
			restore()
			require.Error(t, err)
			var retired bool
			require.NoError(t, raw.Query(
				"SELECT retired FROM translation_target_tombstones_by_uid WHERE kind=? AND uid=?",
				kind, mustTranslationUUID(t, product.UID),
			).Consistency(gocql.Serial).Scan(&retired))
			require.True(t, retired, "rollback must permanently retire even a source without publishable target evidence")

			product.OwnerReferences = nil
			err = writeNativeTranslationTarget(t.Context(), second, product, true, "")
			require.ErrorIs(t, err, datastore.ErrAlreadyExists, "a replacement store cannot resurrect the rolled-back lifetime")
			product.UID, product.Generation, product.ResourceVersion = newID(), 1, "1"
			require.NoError(t, writeNativeTranslationTarget(t.Context(), second, product, true, ""))
			replacement, err := fences.GetTranslationTargetFence(t.Context(), datastore.TranslationTargetKey{
				NamespaceUID: namespace.UID, APIVersion: product.APIVersion, Kind: kind, Name: product.Name,
			})
			require.NoError(t, err)
			require.Equal(t, product.UID, replacement.Evidence.UID)
		})
		t.Run("late-namespace-birth/"+kind, func(t *testing.T) {
			namespace := &datastore.Namespace{
				UID: newID(), Name: "late-birth-" + newID(), Tier: datastore.NamespaceTierOrganization,
				CreationTimestamp: time.Now().UTC().Truncate(time.Millisecond),
			}
			product := newProduct(namespace.Name, "late-birth-"+kind)
			product.Kind, product.Generation, product.ResourceVersion = kind, 1, "1"
			if kind == "File" {
				product.APIVersion = "storage.gitstore.dev/v1beta1"
			}
			restore := scylla.RunMutationBeforeForTest(first, "prepare-target-source", func() {
				require.NoError(t, second.CreateNamespace(t.Context(), namespace))
			})
			err := writeNativeTranslationTarget(t.Context(), first, product, true, "")
			restore()
			require.ErrorIs(t, err, datastore.ErrConflict, "a paused legacy create cannot adopt a Namespace born after it started")
			_, err = fences.GetTranslationTargetFence(t.Context(), datastore.TranslationTargetKey{
				NamespaceUID: namespace.UID, APIVersion: product.APIVersion, Kind: kind, Name: product.Name,
			})
			require.ErrorIs(t, err, datastore.ErrNotFound)
		})
		t.Run("namespace-born-during-unowned-source/"+kind, func(t *testing.T) {
			namespace := &datastore.Namespace{
				UID: newID(), Name: "unowned-source-" + newID(), Tier: datastore.NamespaceTierOrganization,
				CreationTimestamp: time.Now().UTC().Truncate(time.Millisecond),
			}
			product := newProduct(namespace.Name, "unowned-source-"+kind)
			product.Kind, product.Generation, product.ResourceVersion = kind, 1, "1"
			if kind == "File" {
				product.APIVersion = "storage.gitstore.dev/v1beta1"
			}
			called := false
			restore := scylla.RunMutationBeforeForTest(first, "commit-target-source", func() {
				called = true
				require.NoError(t, second.CreateNamespace(t.Context(), namespace))
				epoch, err := second.(datastore.TranslationTargetMutationEpochStore).
					ReadTranslationTargetMutationEpoch(t.Context(), namespace.UID)
				require.ErrorIs(t, err, datastore.ErrConflict, "a newly born Namespace must observe an older unowned source grant")
				require.Empty(t, epoch)
				require.NoError(t, second.(interface {
					RecoverTranslationTargetMutation(context.Context, string) error
				}).RecoverTranslationTargetMutation(t.Context(), namespace.UID))
			})
			err := writeNativeTranslationTarget(t.Context(), first, product, true, "")
			restore()
			require.True(t, called)
			require.Error(t, err, "recovery must revoke the older source grant without adopting its birth")
		})
	}
}

func deleteNativeTranslationTarget(ctx context.Context, store datastore.Datastore, kind, uid string) error {
	switch kind {
	case "Product":
		return store.DeleteProduct(ctx, uid)
	case "ProductVariant":
		return store.DeleteProductVariant(ctx, uid)
	case "CategoryTaxonomy":
		return store.DeleteCategoryTaxonomy(ctx, uid)
	case "Collection":
		return store.DeleteCollection(ctx, uid)
	default:
		return store.DeleteFile(ctx, uid)
	}
}

func writeNativeTranslationTarget(ctx context.Context, store datastore.Datastore, product *datastore.Product, creating bool, expected string) error {
	switch product.Kind {
	case "Product":
		if creating {
			return store.CreateProduct(ctx, product)
		}
		return store.UpdateProduct(ctx, product, expected)
	case "Collection":
		resource := datastore.Collection(*product)
		if creating {
			return store.CreateCollection(ctx, &resource)
		}
		return store.UpdateCollection(ctx, &resource, expected)
	case "File":
		resource := datastore.File(*product)
		if creating {
			return store.CreateFile(ctx, &resource)
		}
		return store.UpdateFile(ctx, &resource, expected)
	case "CategoryTaxonomy":
		resource := &datastore.CategoryTaxonomy{
			UID: product.UID, Namespace: product.Namespace, Name: product.Name, Kind: product.Kind, APIVersion: product.APIVersion,
			CreationTimestamp: product.CreationTimestamp, Generation: product.Generation, ResourceVersion: product.ResourceVersion,
			Spec: product.Spec, Body: product.Body, Status: product.Status,
		}
		if creating {
			return store.CreateCategoryTaxonomy(ctx, resource)
		}
		return store.UpdateCategoryTaxonomy(ctx, resource, expected)
	default:
		resource := &datastore.ProductVariant{
			UID: product.UID, Namespace: product.Namespace, Name: product.Name, Kind: product.Kind, APIVersion: product.APIVersion,
			CreationTimestamp: product.CreationTimestamp, Generation: product.Generation, ResourceVersion: product.ResourceVersion,
			Spec: product.Spec, Body: product.Body, Status: product.Status, SKU: "sku-" + product.UID, ProductRefName: "parent",
		}
		if creating {
			return store.CreateProductVariant(ctx, resource)
		}
		return store.UpdateProductVariant(ctx, resource, expected)
	}
}

func TestTranslationRetiredTargetRejectsDelayedProductAndRepair(t *testing.T) {
	cfg := testScyllaConfig(t)
	cfg.Keyspace += "_target_retirement"
	provisionKeyspace(scyllaAddr, cfg.Keyspace)
	t.Cleanup(func() { dropKeyspace(scyllaAddr, cfg.Keyspace) })
	first, err := scylla.New(cfg, zap.NewNop(), 0)
	require.NoError(t, err)
	t.Cleanup(func() { _ = first.Close() })
	namespace := &datastore.Namespace{
		UID: newID(), Name: "retired-" + newID(), Tier: datastore.NamespaceTierOrganization,
		CreationTimestamp: time.Now().UTC().Truncate(time.Millisecond),
	}
	require.NoError(t, first.CreateNamespace(t.Context(), namespace))
	product := newProduct(namespace.Name, "original")
	product.Generation, product.ResourceVersion = 1, "1"
	require.NoError(t, first.CreateProduct(t.Context(), product))
	require.NoError(t, first.DeleteProduct(t.Context(), product.UID))

	raw := newRawSessionForKeyspace(t, cfg.Keyspace)
	uid := mustTranslationUUID(t, product.UID)
	require.NoError(t, raw.Query(
		"INSERT INTO products_by_key (namespace,creation_timestamp,uid,name,api_version,kind,generation,resource_version) VALUES (?,?,?,?,?,?,?,?)",
		product.Namespace, product.CreationTimestamp, uid, product.Name, product.APIVersion, product.Kind, int64(1), "1",
	).Exec())
	require.NoError(t, raw.Query(
		"INSERT INTO products_by_uid (uid,namespace,creation_timestamp) VALUES (?,?,?)",
		uid, product.Namespace, product.CreationTimestamp,
	).Exec())
	require.NoError(t, raw.Query(
		"INSERT INTO products_by_identity (namespace,name,uid,creation_timestamp) VALUES (?,?,?,?)",
		product.Namespace, product.Name, uid, product.CreationTimestamp,
	).Exec())

	replacement, err := scylla.New(cfg, zap.NewNop(), 0)
	require.NoError(t, err)
	t.Cleanup(func() { _ = replacement.Close() })
	_, err = replacement.GetProduct(t.Context(), product.UID)
	require.ErrorIs(t, err, datastore.ErrNotFound, "retirement survives a delayed row write and store replacement")
	_, err = replacement.GetProductByName(t.Context(), product.Namespace, product.Name)
	require.ErrorIs(t, err, datastore.ErrNotFound)

	repair, err := scylla.OpenProjectionRepairService(cfg)
	require.NoError(t, err)
	defer repair.Close()
	plan, err := repair.Audit(t.Context())
	require.NoError(t, err)
	for _, action := range plan.Actions {
		if action.UID == product.UID {
			require.Nil(t, action.After, "repair must only remove retired-UID projections")
			require.True(t, action.RequireRetiredTargetUID)
		}
	}
	encoded, err := json.Marshal(plan)
	require.NoError(t, err)
	var saved scylla.RepairPlan
	require.NoError(t, json.Unmarshal(encoded, &saved))
	require.NoError(t, raw.Query(
		"DELETE FROM translation_target_tombstones_by_uid WHERE kind=? AND uid=?", "Product", uid,
	).Exec())
	_, err = repair.Apply(t.Context(), saved)
	require.ErrorIs(t, err, datastore.ErrRepairRequired, "a saved plan cannot substitute for current retirement authority")
	require.NoError(t, raw.Query(
		"INSERT INTO translation_target_tombstones_by_uid (kind,uid) VALUES (?,?)", "Product", uid,
	).Exec())
	_, err = repair.Apply(t.Context(), saved)
	require.NoError(t, err)
	_, err = replacement.GetProductByName(t.Context(), product.Namespace, product.Name)
	require.ErrorIs(t, err, datastore.ErrNotFound)
	var remaining gocql.UUID
	err = raw.Query("SELECT uid FROM products_by_key WHERE namespace=? AND creation_timestamp=? AND uid=?",
		product.Namespace, product.CreationTimestamp, uid).Scan(&remaining)
	require.ErrorIs(t, err, gocql.ErrNotFound, "retirement repair also removes the delayed physical row")
}

func TestTranslationTargetRetirementRecoversInterruptedPublicationAndCompaction(t *testing.T) {
	cfg := testScyllaConfig(t)
	cfg.Keyspace += "_target_interrupted"
	provisionKeyspace(scyllaAddr, cfg.Keyspace)
	t.Cleanup(func() { dropKeyspace(scyllaAddr, cfg.Keyspace) })
	first, err := scylla.New(cfg, zap.NewNop(), 0)
	require.NoError(t, err)
	t.Cleanup(func() { _ = first.Close() })
	namespace := &datastore.Namespace{
		UID: newID(), Name: "interrupted-" + newID(), Tier: datastore.NamespaceTierOrganization,
		CreationTimestamp: time.Now().UTC().Truncate(time.Millisecond),
	}
	require.NoError(t, first.CreateNamespace(t.Context(), namespace))
	raw := newRawSessionForKeyspace(t, cfg.Keyspace)
	for _, step := range []string{"publish-target-retirement", "compact-target-retirement"} {
		t.Run(step, func(t *testing.T) {
			product := newProduct(namespace.Name, step)
			product.Generation, product.ResourceVersion = 1, "1"
			require.NoError(t, first.CreateProduct(t.Context(), product))
			restore := scylla.FailMutationBeforeForTest(first, step)
			err := first.DeleteProduct(t.Context(), product.UID)
			restore()
			require.Error(t, err)
			uid := mustTranslationUUID(t, product.UID)
			var sourceRetired, uidRetired bool
			require.NoError(t, raw.Query(
				"SELECT translation_retired FROM products_by_key WHERE namespace=? AND creation_timestamp=? AND uid=?",
				namespace.Name, product.CreationTimestamp, uid,
			).Scan(&sourceRetired))
			require.True(t, sourceRetired, "interruption must retain source-CAS outcome proof")
			require.NoError(t, raw.Query(
				"SELECT retired FROM translation_target_tombstones_by_uid WHERE kind=? AND uid=?", "Product", uid,
			).Scan(&uidRetired))
			require.Equal(t, step == "compact-target-retirement", uidRetired)
			shadowName, shadowCreated := "shadow-"+step, product.CreationTimestamp.Add(time.Second)
			require.NoError(t, raw.Query(
				"INSERT INTO products_by_key (namespace,creation_timestamp,uid,name,api_version,kind,generation,resource_version) VALUES (?,?,?,?,?,?,?,?)",
				namespace.Name, shadowCreated, uid, shadowName, product.APIVersion, product.Kind, int64(1), "1",
			).Exec())
			require.NoError(t, raw.Query(
				"INSERT INTO products_by_uid (uid,namespace,creation_timestamp) VALUES (?,?,?)", uid, namespace.Name, shadowCreated,
			).Exec())
			require.NoError(t, raw.Query(
				"INSERT INTO products_by_identity (namespace,name,uid,creation_timestamp) VALUES (?,?,?,?)",
				namespace.Name, shadowName, uid, shadowCreated,
			).Exec())
			replacement, err := scylla.New(cfg, zap.NewNop(), 0)
			require.NoError(t, err)
			t.Cleanup(func() { _ = replacement.Close() })
			_, err = replacement.GetProduct(t.Context(), product.UID)
			require.ErrorIs(t, err, datastore.ErrNotFound)
			_, err = replacement.GetProductByName(t.Context(), namespace.Name, product.Name)
			require.ErrorIs(t, err, datastore.ErrNotFound)
			_, err = replacement.GetProductByName(t.Context(), namespace.Name, shadowName)
			require.ErrorIs(t, err, datastore.ErrNotFound, "pending UID authority resolves the original retired key, never the delayed projection")
			repair, err := scylla.OpenProjectionRepairService(cfg)
			require.NoError(t, err)
			defer repair.Close()
			plan, err := repair.Audit(t.Context())
			require.NoError(t, err)
			_, err = repair.Apply(t.Context(), plan)
			require.NoError(t, err)
			require.NoError(t, raw.Query(
				"SELECT retired FROM translation_target_tombstones_by_uid WHERE kind=? AND uid=?", "Product", uid,
			).Scan(&uidRetired))
			require.True(t, uidRetired)
			var remaining gocql.UUID
			require.ErrorIs(t, raw.Query(
				"SELECT uid FROM products_by_key WHERE namespace=? AND creation_timestamp=? AND uid=?",
				namespace.Name, product.CreationTimestamp, uid,
			).Scan(&remaining), gocql.ErrNotFound)
			require.ErrorIs(t, raw.Query(
				"SELECT uid FROM products_by_key WHERE namespace=? AND creation_timestamp=? AND uid=?",
				namespace.Name, shadowCreated, uid,
			).Scan(&remaining), gocql.ErrNotFound)
		})
	}
}

func TestTranslationRetiredTargetPointReadsForEveryKind(t *testing.T) {
	cfg := testScyllaConfig(t)
	cfg.Keyspace += "_target_point"
	provisionKeyspace(scyllaAddr, cfg.Keyspace)
	t.Cleanup(func() { dropKeyspace(scyllaAddr, cfg.Keyspace) })
	backend, err := scylla.New(cfg, zap.NewNop(), 0)
	require.NoError(t, err)
	t.Cleanup(func() { _ = backend.Close() })
	raw := newRawSessionForKeyspace(t, cfg.Keyspace)
	namespace := &datastore.Namespace{
		UID: newID(), Name: "point-" + newID(), Tier: datastore.NamespaceTierOrganization,
		CreationTimestamp: time.Now().UTC().Truncate(time.Millisecond),
	}
	require.NoError(t, backend.CreateNamespace(t.Context(), namespace))
	var retiredRows []scylla.AuthoritativeResource
	for _, target := range []struct{ kind, table, uidTable, nameTable string }{
		{"Product", "products_by_key", "products_by_uid", "products_by_identity"},
		{"ProductVariant", "product_variants_by_namespace", "product_variants_by_uid", "product_variants_by_name"},
		{"CategoryTaxonomy", "category_taxonomies_by_namespace", "category_taxonomies_by_uid", "category_taxonomies_by_name"},
		{"Collection", "collections_by_namespace", "collections_by_uid", "collections_by_name"},
		{"File", "files_by_namespace", "files_by_uid", "files_by_name"},
	} {
		t.Run(target.kind, func(t *testing.T) {
			uid, name := newID(), "delayed"
			key, created := mustTranslationUUID(t, uid), time.Now().UTC().Truncate(time.Millisecond)
			apiVersion := "catalog.gitstore.dev/v1beta1"
			if target.kind == "File" {
				apiVersion = "storage.gitstore.dev/v1beta1"
			}
			require.NoError(t, raw.Query(
				"INSERT INTO "+target.table+" (namespace,creation_timestamp,uid,name,api_version,kind,generation,resource_version) VALUES (?,?,?,?,?,?,?,?)",
				namespace.Name, created, key, name, apiVersion, target.kind, int64(1), "1",
			).Exec())
			require.NoError(t, raw.Query(
				"INSERT INTO "+target.uidTable+" (uid,namespace,creation_timestamp) VALUES (?,?,?)", key, namespace.Name, created,
			).Exec())
			require.NoError(t, raw.Query(
				"INSERT INTO "+target.nameTable+" (namespace,name,uid,creation_timestamp) VALUES (?,?,?,?)", namespace.Name, name, key, created,
			).Exec())
			read := func() (byUID, byName error) {
				switch target.kind {
				case "Product":
					_, byUID = backend.GetProduct(t.Context(), uid)
					_, byName = backend.GetProductByName(t.Context(), namespace.Name, name)
				case "ProductVariant":
					_, byUID = backend.GetProductVariant(t.Context(), uid)
					_, byName = backend.GetProductVariantByName(t.Context(), namespace.Name, name)
				case "CategoryTaxonomy":
					_, byUID = backend.GetCategoryTaxonomy(t.Context(), uid)
					_, byName = backend.GetCategoryTaxonomyByName(t.Context(), namespace.Name, name)
				case "Collection":
					_, byUID = backend.GetCollection(t.Context(), uid)
					_, byName = backend.GetCollectionByName(t.Context(), namespace.Name, name)
				case "File":
					_, byUID = backend.GetFile(t.Context(), uid)
					_, byName = backend.GetFileByName(t.Context(), namespace.Name, name)
				}
				return
			}
			byUID, byName := read()
			require.NoError(t, byUID)
			require.NoError(t, byName)
			require.NoError(t, raw.Query(
				"INSERT INTO translation_target_tombstones_by_uid (kind,uid,retired,namespace,creation_timestamp) VALUES (?,?,false,?,?)",
				target.kind, key, namespace.Name, created,
			).Exec())
			byUID, byName = read()
			require.NoError(t, byUID, "a prepared UID claim must not retire a still-live source")
			require.NoError(t, byName)
			require.NoError(t, raw.Query(
				"UPDATE translation_target_tombstones_by_uid SET retired=true WHERE kind=? AND uid=?", target.kind, key,
			).Exec())
			byUID, byName = read()
			require.ErrorIs(t, byUID, datastore.ErrNotFound)
			require.ErrorIs(t, byName, datastore.ErrNotFound)
			require.NoError(t, raw.Query(
				"UPDATE "+target.table+" SET translation_retired=true WHERE namespace=? AND creation_timestamp=? AND uid=?",
				namespace.Name, created, key,
			).Exec())
			require.NoError(t, raw.Query(
				"DELETE FROM translation_target_tombstones_by_uid WHERE kind=? AND uid=?", target.kind, key,
			).Exec())
			byUID, byName = read()
			require.ErrorIs(t, byUID, datastore.ErrNotFound, "the source CAS proves retirement before the UID fact is published")
			require.ErrorIs(t, byName, datastore.ErrNotFound)
			if target.kind != "Product" {
				require.NoError(t, raw.Query(
					"INSERT INTO translation_target_tombstones_by_uid (kind,uid,retired,namespace,creation_timestamp) VALUES (?,?,false,?,?)",
					target.kind, key, namespace.Name, created,
				).Exec())
			}
			retiredRows = append(retiredRows, scylla.AuthoritativeResource{
				Kind: target.kind, UID: uid, Namespace: namespace.Name, Name: name, CreationTimestamp: created,
			})
			if target.kind != "Product" {
				var listErr error
				switch target.kind {
				case "ProductVariant":
					_, listErr = backend.ListProductVariants(t.Context(), namespace.Name, datastore.PageParams{First: 1})
				case "CategoryTaxonomy":
					_, listErr = backend.ListCategoryTaxonomies(t.Context(), namespace.Name, datastore.PageParams{First: 1})
				case "Collection":
					_, listErr = backend.ListCollections(t.Context(), namespace.Name, datastore.PageParams{First: 1})
				case "File":
					_, listErr = backend.ListFiles(t.Context(), namespace.Name, datastore.PageParams{First: 1})
				}
				require.ErrorIs(t, listErr, datastore.ErrRepairRequired, "a raw namespace page cannot publish a retired incarnation")
			}
		})
	}
	repair, err := scylla.OpenProjectionRepairService(cfg)
	require.NoError(t, err)
	defer repair.Close()
	plan, err := repair.Audit(t.Context())
	require.NoError(t, err)
	_, err = repair.Apply(t.Context(), plan)
	require.NoError(t, err)
	for _, row := range retiredRows {
		var retired bool
		require.NoError(t, raw.Query(
			"SELECT retired FROM translation_target_tombstones_by_uid WHERE kind=? AND uid=?", row.Kind, mustTranslationUUID(t, row.UID),
		).Scan(&retired))
		require.True(t, retired, "repair publishes permanent UID retirement before compacting source proof")
	}
	variants, err := backend.ListProductVariants(t.Context(), namespace.Name, datastore.PageParams{First: 1})
	require.NoError(t, err)
	require.Empty(t, variants.Items)
	categories, err := backend.ListCategoryTaxonomies(t.Context(), namespace.Name, datastore.PageParams{First: 1})
	require.NoError(t, err)
	require.Empty(t, categories.Items)
	collections, err := backend.ListCollections(t.Context(), namespace.Name, datastore.PageParams{First: 1})
	require.NoError(t, err)
	require.Empty(t, collections.Items)
	files, err := backend.ListFiles(t.Context(), namespace.Name, datastore.PageParams{First: 1})
	require.NoError(t, err)
	require.Empty(t, files.Items)
}
