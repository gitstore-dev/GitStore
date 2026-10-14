// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package scylla

import (
	"context"
	"crypto/md5"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"testing/fstest"
	"time"

	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	"github.com/gocql/gocql"
	"github.com/google/uuid"
	"github.com/scylladb/gocqlx/v3/migrate"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFileCASWriterBatchesReadyRowsAndPreservesIndependentConflicts(t *testing.T) {
	ambiguous := errors.New("conditional batch outcome unknown")
	for _, tc := range []struct {
		name    string
		applied bool
		err     error
		singles int64
	}{
		{"applied", true, nil, 1},
		{"definite conflict", false, nil, 4},
		{"ambiguous failure", false, ambiguous, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			started, release := make(chan struct{}), make(chan struct{})
			var singles, batches atomic.Int64
			writer := newFileCASWriter(func(request *fileCASRequest) error {
				singles.Add(1)
				if request.uid[15] == 1 {
					close(started)
					<-release
				}
				if request.uid[15] == 3 {
					return datastore.ErrConflict
				}
				return nil
			}, func(ctx context.Context, group []*fileCASRequest) (bool, error) {
				batches.Add(1)
				assert.NoError(t, ctx.Err())
				assert.Len(t, group, 3)
				for i, request := range group {
					assert.Equal(t, byte(i+2), request.uid[15])
				}
				return tc.applied, tc.err
			})
			t.Cleanup(func() {
				select {
				case <-release:
				default:
					close(release)
				}
				writer.close()
			})
			done := make([]chan error, 4)
			for i := range done {
				done[i] = make(chan error, 1)
			}
			go func() { done[0] <- writer.submit(t.Context(), gocql.UUID{15: 1}, nil) }()
			<-started
			for i := 1; i < len(done); i++ {
				go func() { done[i] <- writer.submit(t.Context(), gocql.UUID{15: byte(i + 1)}, nil) }()
				require.Eventually(t, func() bool { return len(writer.requests) == i }, time.Second, time.Millisecond)
			}
			close(release)
			for i, result := range done {
				err := <-result
				switch {
				case i == 0:
					require.NoError(t, err)
				case tc.err != nil:
					require.ErrorIs(t, err, tc.err)
				case !tc.applied && i == 2:
					require.ErrorIs(t, err, datastore.ErrConflict)
				default:
					require.NoError(t, err)
				}
			}
			writer.close()
			require.Equal(t, tc.singles, singles.Load())
			require.EqualValues(t, 1, batches.Load())
			require.ErrorContains(t, writer.submit(t.Context(), gocql.UUID{15: 5}, nil), "closed")
		})
	}
}

func TestFileCASWriterNeverBatchesRepeatedUIDs(t *testing.T) {
	started, release := make(chan struct{}), make(chan struct{})
	writer := newFileCASWriter(func(request *fileCASRequest) error {
		if request.uid[15] == 1 {
			close(started)
			<-release
		}
		return nil
	}, func(_ context.Context, group []*fileCASRequest) (bool, error) {
		seen := make(map[gocql.UUID]bool)
		for _, request := range group {
			assert.False(t, seen[request.uid], "one row cannot have conflicting conditions inside a batch")
			seen[request.uid] = true
		}
		assert.LessOrEqual(t, len(group), 4)
		return true, nil
	})
	t.Cleanup(func() {
		select {
		case <-release:
		default:
			close(release)
		}
		writer.close()
	})
	done := make(chan error, 5)
	go func() { done <- writer.submit(t.Context(), gocql.UUID{15: 1}, nil) }()
	<-started
	for i, id := range []byte{2, 2, 3, 4} {
		go func() { done <- writer.submit(t.Context(), gocql.UUID{15: id}, nil) }()
		require.Eventually(t, func() bool { return len(writer.requests) == i+1 }, time.Second, time.Millisecond)
	}
	close(release)
	for range 5 {
		require.NoError(t, <-done)
	}
	writer.close()
}

func TestFileCASWriterRetainsBoundAndDrainsDuringClose(t *testing.T) {
	started, release := make(chan struct{}), make(chan struct{})
	writer := newFileCASWriter(func(request *fileCASRequest) error {
		if request.uid[15] == 1 {
			close(started)
			<-release
		}
		return nil
	}, func(_ context.Context, group []*fileCASRequest) (bool, error) {
		assert.Len(t, group, 16, "already accepted small rows should share one physical CAS")
		return true, nil
	})
	t.Cleanup(func() {
		select {
		case <-release:
		default:
			close(release)
		}
		writer.close()
	})
	done := make(chan error, 17)
	go func() { done <- writer.submit(t.Context(), gocql.UUID{15: 1}, nil) }()
	<-started
	for i := range 16 {
		go func() { done <- writer.submit(t.Context(), gocql.UUID{15: byte(i + 2)}, nil) }()
	}
	require.Eventually(t, func() bool { return len(writer.requests) == 16 }, time.Second, time.Millisecond)
	require.ErrorContains(t, writer.submit(t.Context(), gocql.UUID{15: 20}, nil), "queue exhausted")
	closed := make(chan struct{})
	go func() { writer.close(); close(closed) }()
	require.Eventually(t, func() bool {
		writer.mu.Lock()
		defer writer.mu.Unlock()
		return writer.closed
	}, time.Second, time.Millisecond)
	select {
	case <-closed:
		t.Fatal("writer closed before its accepted physical writes drained")
	default:
	}
	close(release)
	for range 17 {
		require.NoError(t, <-done)
	}
	<-closed
}

func TestFileCASWriterKeepsLargeValuesInFourRowBatches(t *testing.T) {
	started, release := make(chan struct{}), make(chan struct{})
	var batches atomic.Int64
	writer := newFileCASWriter(func(request *fileCASRequest) error {
		if request.uid[15] == 1 {
			close(started)
			<-release
		}
		return nil
	}, func(_ context.Context, group []*fileCASRequest) (bool, error) {
		batches.Add(1)
		assert.Len(t, group, 4)
		return true, nil
	})
	t.Cleanup(func() {
		select {
		case <-release:
		default:
			close(release)
		}
		writer.close()
	})
	done := make(chan error, 17)
	go func() { done <- writer.submit(t.Context(), gocql.UUID{15: 1}, nil) }()
	<-started
	values := []any{strings.Repeat("x", fileCASBatchPayloadLimit)}
	for i := range 16 {
		go func() { done <- writer.submit(t.Context(), gocql.UUID{15: byte(i + 2)}, values) }()
	}
	require.Eventually(t, func() bool { return len(writer.requests) == 16 }, time.Second, time.Millisecond)
	close(release)
	for range 17 {
		require.NoError(t, <-done)
	}
	writer.close()
	require.EqualValues(t, 4, batches.Load())
}

func TestFileCASWriterBatchCancellationKeepsLivePeers(t *testing.T) {
	for _, cancelAll := range []bool{false, true} {
		t.Run(fmt.Sprintf("all-%t", cancelAll), func(t *testing.T) {
			started, release := make(chan struct{}), make(chan struct{})
			firstCtx, cancelFirst := context.WithCancel(t.Context())
			peerCtx, cancelPeer := context.WithCancel(t.Context())
			defer cancelFirst()
			defer cancelPeer()
			writer := newFileCASWriter(func(*fileCASRequest) error {
				close(started)
				<-release
				return nil
			}, func(ctx context.Context, group []*fileCASRequest) (bool, error) {
				assert.Len(t, group, 2)
				cancelFirst()
				if cancelAll {
					cancelPeer()
					select {
					case <-ctx.Done():
					case <-time.After(time.Second):
						return false, errors.New("all-canceled conditional batch remained active")
					}
					return false, ctx.Err()
				}
				assert.NoError(t, ctx.Err(), "one caller cannot cancel a live peer's physical batch")
				return true, nil
			})
			t.Cleanup(func() {
				select {
				case <-release:
				default:
					close(release)
				}
				writer.close()
			})
			done := make([]chan error, 3)
			for i := range done {
				done[i] = make(chan error, 1)
			}
			go func() { done[0] <- writer.submit(t.Context(), gocql.UUID{15: 1}, nil) }()
			<-started
			go func() { done[1] <- writer.submit(firstCtx, gocql.UUID{15: 2}, nil) }()
			require.Eventually(t, func() bool { return len(writer.requests) == 1 }, time.Second, time.Millisecond)
			go func() { done[2] <- writer.submit(peerCtx, gocql.UUID{15: 3}, nil) }()
			require.Eventually(t, func() bool { return len(writer.requests) == 2 }, time.Second, time.Millisecond)
			close(release)
			require.NoError(t, <-done[0])
			for _, result := range done[1:] {
				if cancelAll {
					require.ErrorIs(t, <-result, context.Canceled)
				} else {
					require.NoError(t, <-result)
				}
			}
			writer.close()
		})
	}
}

func TestFileCASWriterSkipsCanceledQueuedRows(t *testing.T) {
	started, release := make(chan struct{}), make(chan struct{})
	var writes atomic.Int64
	writer := newFileCASWriter(func(request *fileCASRequest) error {
		writes.Add(1)
		assert.NotEqual(t, byte(2), request.uid[15], "an already-canceled queued row must not reach physical execution")
		if request.uid[15] == 1 {
			close(started)
			<-release
		}
		return nil
	}, func(context.Context, []*fileCASRequest) (bool, error) {
		t.Error("one live queued peer must use its independent single CAS")
		return false, errors.New("unexpected physical batch")
	})
	t.Cleanup(func() {
		select {
		case <-release:
		default:
			close(release)
		}
		writer.close()
	})
	done := make([]chan error, 3)
	for i := range done {
		done[i] = make(chan error, 1)
	}
	go func() { done[0] <- writer.submit(t.Context(), gocql.UUID{15: 1}, nil) }()
	<-started
	canceled, cancel := context.WithCancel(t.Context())
	defer cancel()
	go func() { done[1] <- writer.submit(canceled, gocql.UUID{15: 2}, nil) }()
	require.Eventually(t, func() bool { return len(writer.requests) == 1 }, time.Second, time.Millisecond)
	go func() { done[2] <- writer.submit(t.Context(), gocql.UUID{15: 3}, nil) }()
	require.Eventually(t, func() bool { return len(writer.requests) == 2 }, time.Second, time.Millisecond)
	cancel()
	close(release)
	require.NoError(t, <-done[0])
	require.ErrorIs(t, <-done[1], context.Canceled)
	require.NoError(t, <-done[2])
	writer.close()
	require.EqualValues(t, 2, writes.Load())
}

func TestFileUpdateBindingsPreserveChangesAndNormalization(t *testing.T) {
	now := time.Now().UTC()
	deleted := now.Add(time.Hour)
	previous := toFileRow(&datastore.File{
		UID: uuid.NewString(), Namespace: "namespace", Name: "file",
		APIVersion: "v1beta1", Kind: "File", Generation: 1, ResourceVersion: "1",
		CreationTimestamp: now, Revision: "revision", CreationActor: "creator",
		UpdateTimestamp: now, UpdateActor: "updater", Labels: map[string]string{"key": "value"},
		Annotations: map[string]string{"key": "value"}, OwnerReferences: json.RawMessage(`[]`),
		Finalizers: []string{"finalizer"}, DeletionTimestamp: &deleted, RepositoryID: uuid.NewString(),
		SourcePath: "path", GitCommitSHA: "commit", GitRef: "ref", Spec: json.RawMessage(`{}`),
		Body: "body", Status: json.RawMessage(`{}`),
	})
	same := *previous
	same.Labels = map[string]string{"key": "value"}
	same.Annotations = map[string]string{"key": "value"}
	same.Finalizers = []string{"finalizer"}
	sameDeleted := deleted
	same.DeletionTimestamp = &sameDeleted
	sameRepository := *previous.RepositoryID
	same.RepositoryID = &sameRepository
	values := fileUpdateValues(&same, previous)
	require.Len(t, values, 21)
	for i, value := range values {
		switch i {
		case 4:
			require.Equal(t, same.ResourceVersion, value)
		default:
			require.Equal(t, gocql.UnsetValue, value, "unchanged column %d", i)
		}
	}
	cleared := &fileRow{Namespace: previous.Namespace, UID: previous.UID, CreationTimestamp: previous.CreationTimestamp}
	values = fileUpdateValues(cleared, previous)
	for i, value := range values {
		require.NotEqual(t, gocql.UnsetValue, value, "changed/cleared column %d", i)
	}
	require.Equal(t, "", values[0])
	require.EqualValues(t, 0, values[3])
	require.Equal(t, map[string]string(nil), values[9])
	require.Equal(t, []string(nil), values[12])
	require.Equal(t, (*time.Time)(nil), values[13])
	require.Equal(t, (*gocql.UUID)(nil), values[14])
	require.Equal(t, "", values[19])
	empty := fileUpdateValues(cleared, cleared)
	for _, i := range []int{4, 11, 18, 20} {
		require.Equal(t, "", empty[i], "logical absence must retain explicit normalization for column %d", i)
	}
	require.Equal(t, "value", previous.Labels["key"], "binding preparation must not mutate snapshots")
	require.Equal(t, []string{"finalizer"}, previous.Finalizers)
}

func TestMigrationHistoryRequiresEveryCompletedUnchangedFile(t *testing.T) {
	data := []byte("-- comment;\nCREATE TABLE test (id text PRIMARY KEY);\n")
	bundle := fstest.MapFS{"001.cql": &fstest.MapFile{Data: data}}
	valid := migrate.Info{Name: "001.cql", Checksum: fmt.Sprintf("%x", md5.Sum(data)), Done: 2, EndTime: time.Now()}
	require.NoError(t, validateMigrationHistory([]migrate.Info{valid}, bundle))
	for name, history := range map[string][]migrate.Info{
		"missing":    nil,
		"ahead":      {valid, valid},
		"renamed":    {{Name: "old.cql", Checksum: valid.Checksum, Done: 2, EndTime: valid.EndTime}},
		"modified":   {{Name: valid.Name, Checksum: "different", Done: 2, EndTime: valid.EndTime}},
		"partial":    {{Name: valid.Name, Checksum: valid.Checksum, Done: 1, EndTime: valid.EndTime}},
		"unfinished": {{Name: valid.Name, Checksum: valid.Checksum, Done: 2}},
	} {
		t.Run(name, func(t *testing.T) {
			require.Error(t, validateMigrationHistory(history, bundle))
		})
	}
	data = []byte("CREATE TABLE test (id text PRIMARY KEY)")
	bundle["001.cql"] = &fstest.MapFile{Data: data}
	valid.Checksum, valid.Done = fmt.Sprintf("%x", md5.Sum(data)), 1
	require.NoError(t, validateMigrationHistory([]migrate.Info{valid}, bundle), "match the library's optional final semicolon")
}

func TestCatalogueRowsPreserveCanonicalEnvelope(t *testing.T) {
	now := time.Date(2026, time.August, 19, 12, 0, 0, 0, time.UTC)
	deletedAt := now.Add(time.Hour)
	uid := uuid.New().String()
	repositoryID := uuid.New().String()
	owners := json.RawMessage(`[{"uid":"owner"}]`)
	spec := json.RawMessage(`{"field":"value"}`)
	status := json.RawMessage(`{"observedGeneration":4}`)

	product := &datastore.Product{
		UID: uid, Namespace: "shop", Name: "product", APIVersion: "catalog/v1", Kind: "Product",
		Generation: 4, ResourceVersion: "7", Revision: "main@sha", CreationTimestamp: now,
		CreationActor: "creator", UpdateTimestamp: now.Add(time.Minute), UpdateActor: "updater",
		Labels: map[string]string{"a": "b"}, Annotations: map[string]string{"c": "d"},
		OwnerReferences: owners, Finalizers: []string{"finalizer"}, DeletionTimestamp: &deletedAt,
		RepositoryID: repositoryID, SourcePath: "products/product.md", GitCommitSHA: "sha", GitRef: "refs/heads/main",
		Spec: spec, Body: "body", Status: status,
	}
	productRow := toProductRow(product)
	assert.Equal(t, product.CreationActor, productRow.CreationActor)
	assert.Equal(t, string(owners), productRow.OwnerReferences)
	assert.Equal(t, product.Finalizers, productRow.Finalizers)
	assert.Equal(t, &deletedAt, productRow.DeletionTimestamp)
	require.NotNil(t, productRow.RepositoryID)
	assert.Equal(t, repositoryID, productRow.RepositoryID.String())
	assert.Equal(t, product, fromProductRow(productRow))

	category := &datastore.CategoryTaxonomy{
		UID: uid, Namespace: "shop", Name: "category", APIVersion: "catalog/v1", Kind: "CategoryTaxonomy",
		Generation: 4, ResourceVersion: "7", Revision: "main@sha", CreationTimestamp: now,
		CreationActor: "creator", UpdateTimestamp: now.Add(time.Minute), UpdateActor: "updater",
		Labels: product.Labels, Annotations: product.Annotations, OwnerReferences: owners,
		Finalizers: product.Finalizers, DeletionTimestamp: &deletedAt, ParentName: "parent", AncestorPath: "parent/category",
		RepositoryID: repositoryID, SourcePath: "categories/category.md", GitCommitSHA: "sha", GitRef: "refs/heads/main",
		Spec: spec, Body: "body", Status: status,
	}
	categoryRow := toCategoryTaxonomyRow(category)
	assert.Equal(t, string(owners), categoryRow.OwnerReferences)
	assert.Equal(t, category, fromCategoryTaxonomyRow(categoryRow))

	collection := &datastore.Collection{
		UID: uid, Namespace: "shop", Name: "collections_by_namespace", APIVersion: "catalog/v1", Kind: "Collection",
		Generation: 4, ResourceVersion: "7", Revision: "main@sha", CreationTimestamp: now,
		CreationActor: "creator", UpdateTimestamp: now.Add(time.Minute), UpdateActor: "updater",
		Labels: product.Labels, Annotations: product.Annotations, OwnerReferences: owners,
		Finalizers: product.Finalizers, DeletionTimestamp: &deletedAt, RepositoryID: repositoryID,
		SourcePath: "collections/collection.md", GitCommitSHA: "sha", GitRef: "refs/heads/main",
		Spec: spec, Body: "body", Status: status,
	}
	collectionRow := toCollectionRow(collection)
	assert.Equal(t, string(owners), collectionRow.OwnerReferences)
	assert.Equal(t, collection, fromCollectionRow(collectionRow))

	variant := &datastore.ProductVariant{
		UID: uid, Namespace: "shop", Name: "variant", APIVersion: "catalog/v1", Kind: "ProductVariant",
		Generation: 4, ResourceVersion: "7", Revision: "main@sha", CreationTimestamp: now,
		CreationActor: "creator", UpdateTimestamp: now.Add(time.Minute), UpdateActor: "updater",
		Labels: product.Labels, Annotations: product.Annotations, OwnerReferences: owners,
		Finalizers: product.Finalizers, DeletionTimestamp: &deletedAt, SKU: "sku", ProductRefName: "product",
		RepositoryID: repositoryID, SourcePath: "variants/variant.md", GitCommitSHA: "sha", GitRef: "refs/heads/main",
		Spec: spec, Body: "body", Status: status,
	}
	variantRow := toProductVariantRow(variant)
	assert.Equal(t, string(owners), variantRow.OwnerReferences)
	assert.Equal(t, variant, fromProductVariantRow(variantRow))
}

func TestCatalogueCreateCompensatesEveryFailureBoundary(t *testing.T) {
	tests := []struct {
		kind        string
		projections []string
	}{
		{kind: "Product", projections: []string{"name", "uid", "authoritative"}},
		{kind: "CategoryTaxonomy", projections: []string{"name", "uid", "authoritative"}},
		{kind: "Collection", projections: []string{"name", "uid", "authoritative"}},
		{kind: "ProductVariant", projections: []string{"name", "uid", "sku", "authoritative", "product-ref"}},
	}

	for _, test := range tests {
		for _, failedProjection := range test.projections {
			for _, point := range []failurePoint{failureBefore, failureAfter} {
				t.Run(test.kind+"/"+failedProjection+"/"+string(point), func(t *testing.T) {
					state := make(map[string]bool)
					injector := newTestFailureInjector()
					injector.fail("create-"+failedProjection, point)
					executor := newMutationExecutor(injector)

					err := executor.execute(context.Background(), createActions(test.kind, state, test.projections)...)

					require.Error(t, err)
					for _, projection := range test.projections {
						assert.False(t, state[projection], "projection %s was not compensated", projection)
					}
				})
			}
		}
	}
}

func TestCatalogueUpdateRollsForwardAfterProjectionFailure(t *testing.T) {
	tests := []struct {
		kind        string
		projections []string
	}{
		{kind: "Product", projections: []string{"name", "uid"}},
		{kind: "CategoryTaxonomy", projections: []string{"name", "uid"}},
		{kind: "Collection", projections: []string{"name", "uid"}},
		{kind: "ProductVariant", projections: []string{"name", "uid", "sku", "product-ref"}},
	}
	for _, test := range tests {
		for _, failedProjection := range test.projections {
			for _, point := range []failurePoint{failureBefore, failureAfter} {
				t.Run(test.kind+"/"+failedProjection+"/"+string(point), func(t *testing.T) {
					state := map[string]string{"authoritative": "old"}
					for _, projection := range test.projections {
						state[projection] = "old"
					}
					injector := newTestFailureInjector()
					injector.fail("converge-"+failedProjection, point)
					executor := newMutationExecutor(injector)

					err := executor.executeUpdate(
						context.Background(),
						"2",
						updateAuthoritativeAction(test.kind, state),
						updateProjectionActions(test.kind, state, test.projections)...,
					)

					require.NoError(t, err)
					for key, value := range state {
						assert.Equal(t, "new", value, "%s did not roll forward", key)
					}
				})
			}
		}
	}
}

func TestCatalogueUpdateReturnsRepairRequiredWithoutRollingBackAuthoritative(t *testing.T) {
	state := map[string]string{"authoritative": "old", "old-sku": "present", "new-sku": "missing"}
	executor := newMutationExecutor(nil)
	newSKUConverged := false

	err := executor.executeUpdate(
		context.Background(),
		"2",
		mutationAction{
			Step: datastore.MutationStep{ResourceKind: "ProductVariant", Projection: "authoritative", Action: "update-authoritative"},
			Apply: func(context.Context) error {
				state["authoritative"] = "new"
				return nil
			},
		},
		mutationAction{
			Step: datastore.MutationStep{ResourceKind: "ProductVariant", Projection: "new-sku", Action: "converge-sku"},
			Apply: func(context.Context) error {
				return errors.New("persistent projection failure")
			},
		},
		mutationAction{
			Step: datastore.MutationStep{ResourceKind: "ProductVariant", Projection: "old-sku", Action: "delete-old-sku"},
			Apply: func(context.Context) error {
				if !newSKUConverged {
					return errors.New("new sku has not converged")
				}
				state["old-sku"] = "deleted"
				return nil
			},
		},
	)

	require.ErrorIs(t, err, datastore.ErrRepairRequired)
	assert.Equal(t, "new", state["authoritative"])
	assert.Equal(t, "present", state["old-sku"])
	assert.Equal(t, "missing", state["new-sku"])
}

func TestCatalogueUpdateConvergesAfterAuthoritativePostCommitFailure(t *testing.T) {
	state := map[string]string{"authoritative": "old", "name": "old"}
	injector := newTestFailureInjector()
	injector.fail("update-authoritative", failureAfter)
	executor := newMutationExecutor(injector)

	err := executor.executeUpdate(
		context.Background(),
		"2",
		mutationAction{
			Step: datastore.MutationStep{ResourceKind: "Product", Projection: "authoritative", Action: "update-authoritative"},
			Apply: func(context.Context) error {
				state["authoritative"] = "new"
				return nil
			},
		},
		mutationAction{
			Step: datastore.MutationStep{ResourceKind: "Product", Projection: "name", Action: "converge-name"},
			Apply: func(context.Context) error {
				state["name"] = "new"
				return nil
			},
		},
	)

	require.Error(t, err)
	assert.Equal(t, "new", state["authoritative"])
	assert.Equal(t, "new", state["name"])
}

func TestCatalogueUpdateAuthoritativeFailurePerformsNoProjectionWrites(t *testing.T) {
	for _, applyFailure := range []bool{false, true} {
		t.Run(map[bool]string{false: "before", true: "apply"}[applyFailure], func(t *testing.T) {
			state := map[string]string{"authoritative": "old", "name": "old"}
			injector := newTestFailureInjector()
			if !applyFailure {
				injector.fail("update-authoritative", failureBefore)
			}
			executor := newMutationExecutor(injector)
			authoritative := updateAuthoritativeAction("Product", state)
			if applyFailure {
				authoritative.Apply = func(context.Context) error {
					return errors.New("compare-and-set failed")
				}
			}

			err := executor.executeUpdate(
				context.Background(),
				"2",
				authoritative,
				updateProjectionActions("Product", state, []string{"name"})...,
			)

			require.Error(t, err)
			assert.Equal(t, "old", state["authoritative"])
			assert.Equal(t, "old", state["name"])
		})
	}
}

func TestCatalogueStaleVersionPerformsNoWrites(t *testing.T) {
	writes := 0
	err := datastore.ValidateCatalogUpdateVersion("3", "1", "2")
	if err == nil {
		writes++
	}

	require.ErrorIs(t, err, datastore.ErrConflict)
	assert.Zero(t, writes)
}

func TestCatalogueDeleteRestoresExactPriorStateAtEveryPrecommitBoundary(t *testing.T) {
	tests := []struct {
		kind        string
		projections []string
	}{
		{kind: "Product", projections: []string{"name", "uid"}},
		{kind: "CategoryTaxonomy", projections: []string{"name", "uid"}},
		{kind: "Collection", projections: []string{"name", "uid"}},
		{kind: "ProductVariant", projections: []string{"name", "uid", "sku", "product-ref"}},
	}
	for _, test := range tests {
		for _, failedStep := range append(append([]string(nil), test.projections...), "authoritative") {
			for _, point := range []failurePoint{failureBefore, failureAfter} {
				if failedStep == "authoritative" && point == failureAfter {
					continue
				}
				t.Run(test.kind+"/"+failedStep+"/"+string(point), func(t *testing.T) {
					state := map[string]bool{"authoritative": true}
					for _, projection := range test.projections {
						state[projection] = true
					}
					injector := newTestFailureInjector()
					injector.fail("delete-"+failedStep, point)
					executor := newMutationExecutor(injector)

					err := executor.executeDelete(
						context.Background(),
						deleteAuthoritativeAction(state, nil),
						deleteProjectionActions(state, test.projections)...,
					)

					require.Error(t, err)
					for key, present := range state {
						assert.True(t, present, "%s was not restored", key)
					}
				})
			}
		}
	}
}

func TestCatalogueDeleteAfterAuthoritativeFailureKeepsDesiredFinalState(t *testing.T) {
	state := map[string]bool{"authoritative": true, "name": true, "uid": true}
	injector := newTestFailureInjector()
	injector.fail("delete-authoritative", failureAfter)
	executor := newMutationExecutor(injector)

	err := executor.executeDelete(
		context.Background(),
		deleteAuthoritativeAction(state, nil),
		deleteProjectionActions(state, []string{"name", "uid"})...,
	)

	require.Error(t, err)
	assert.False(t, state["authoritative"])
	assert.False(t, state["name"])
	assert.False(t, state["uid"])
}

func TestCatalogueDeleteCompensationFailureRequiresRepair(t *testing.T) {
	state := map[string]bool{"authoritative": true, "name": true}
	executor := newMutationExecutor(nil)
	projection := mutationAction{
		Step: datastore.MutationStep{ResourceKind: "Collection", Projection: "name", Action: "delete-name"},
		Apply: func(context.Context) error {
			state["name"] = false
			return nil
		},
		Compensate: func(context.Context) error {
			return errors.New("restore failed")
		},
	}

	err := executor.executeDelete(
		context.Background(),
		deleteAuthoritativeAction(state, errors.New("delete failed")),
		projection,
	)

	require.ErrorIs(t, err, datastore.ErrRepairRequired)
	assert.True(t, state["authoritative"])
	assert.False(t, state["name"])
}

func TestConditionalDeleteConflictDoesNotMutateProjections(t *testing.T) {
	projectionDeletes := 0
	executor := newMutationExecutor(nil)
	authoritative := mutationAction{
		Step:  datastore.MutationStep{Operation: "delete", ResourceKind: "ProductVariant", Projection: "authoritative", Action: "delete-authoritative"},
		Apply: func(context.Context) error { return datastore.ErrConflict },
	}
	projection := mutationAction{
		Step: datastore.MutationStep{Operation: "delete", ResourceKind: "ProductVariant", Projection: "sku", Action: "delete-sku"},
		Apply: func(context.Context) error {
			projectionDeletes++
			return nil
		},
	}

	err := executor.executeConditionalDelete(context.Background(), "1", authoritative, projection)

	require.ErrorIs(t, err, datastore.ErrConflict)
	assert.Zero(t, projectionDeletes)
}

func createActions(kind string, state map[string]bool, projections []string) []mutationAction {
	actions := make([]mutationAction, 0, len(projections))
	for _, projection := range projections {
		projection := projection
		actions = append(actions, mutationAction{
			Step: datastore.MutationStep{
				Operation: "create", ResourceKind: kind, Projection: projection, Action: "create-" + projection,
			},
			Apply: func(context.Context) error {
				state[projection] = true
				return nil
			},
			Compensate: func(context.Context) error {
				state[projection] = false
				return nil
			},
		})
	}
	return actions
}

func updateAuthoritativeAction(kind string, state map[string]string) mutationAction {
	return mutationAction{
		Step: datastore.MutationStep{
			Operation: "update", ResourceKind: kind, Projection: "authoritative", Action: "update-authoritative",
		},
		Apply: func(context.Context) error {
			state["authoritative"] = "new"
			return nil
		},
	}
}

func updateProjectionActions(kind string, state map[string]string, projections []string) []mutationAction {
	actions := make([]mutationAction, 0, len(projections))
	for _, projection := range projections {
		projection := projection
		actions = append(actions, mutationAction{
			Step: datastore.MutationStep{
				Operation: "update", ResourceKind: kind, Projection: projection, Action: "converge-" + projection,
			},
			Apply: func(context.Context) error {
				state[projection] = "new"
				return nil
			},
		})
	}
	return actions
}

func deleteProjectionActions(state map[string]bool, projections []string) []mutationAction {
	actions := make([]mutationAction, 0, len(projections))
	for _, projection := range projections {
		projection := projection
		actions = append(actions, mutationAction{
			Step: datastore.MutationStep{Operation: "delete", Projection: projection, Action: "delete-" + projection},
			Apply: func(context.Context) error {
				state[projection] = false
				return nil
			},
			Compensate: func(context.Context) error {
				state[projection] = true
				return nil
			},
		})
	}
	return actions
}

func deleteAuthoritativeAction(state map[string]bool, applyErr error) mutationAction {
	return mutationAction{
		Step: datastore.MutationStep{Operation: "delete", Projection: "authoritative", Action: "delete-authoritative"},
		Apply: func(context.Context) error {
			if applyErr != nil {
				return applyErr
			}
			state["authoritative"] = false
			return nil
		},
	}
}
