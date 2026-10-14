// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package datastore_test

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/gitstore-dev/gitstore/api/internal/catalog"
	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	"github.com/gitstore-dev/gitstore/api/internal/datastore/memdb"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
)

type batchForwardingStore struct {
	datastore.Datastore
	namespace string
	calls     int
	phases    []datastore.NamespaceFencePhase
}

func TestInstrumentedDatastoreFileSnapshotsMetricsAndErrors(t *testing.T) {
	for _, failure := range []error{nil, errors.New("snapshot storage unavailable"), context.Canceled} {
		for _, operation := range []string{"GetFileSnapshots", "GetFileSnapshotsByName"} {
			t.Run(operation+"/"+fmt.Sprint(failure), func(t *testing.T) {
				wrapped, logs, registry := newTestInstrumented(t, &stubDatastore{getProductErr: failure})
				read := func() ([]*datastore.File, error) {
					if operation == "GetFileSnapshotsByName" {
						return wrapped.GetFileSnapshotsByName(t.Context(), "private-namespace", []string{"private-uid", "private-uid"})
					}
					return wrapped.GetFileSnapshots(t.Context(), []string{"private-uid", "private-uid"})
				}
				files, err := read()
				require.Equal(t, failure, err)
				require.Len(t, files, 2)
				require.EqualValues(t, 1, histogramObservationCount(t, registry, operation, "test-backend"))
				if failure != nil {
					require.EqualValues(t, 1, counterValue(t, registry, operation, "test-backend"))
					require.NotZero(t, logs.Len())
				}
				families, err := registry.Gather()
				require.NoError(t, err)
				for _, family := range families {
					for _, metric := range family.Metric {
						for _, label := range metric.Label {
							require.NotContains(t, label.GetValue(), "private-uid")
						}
					}
				}
			})
		}
	}
}

type fileBatchForwardingStore struct {
	batchForwardingStore
	repositoryID string
	names        []string
}

func (s *fileBatchForwardingStore) WithFileMutationBatch(ctx context.Context, namespace, repositoryID string, names []string, operation func(context.Context) error) error {
	s.repositoryID, s.names = repositoryID, names
	return s.WithCatalogMutationBatch(ctx, namespace, operation)
}

func TestInstrumentedDatastoreForwardsFileCohortsAndFallback(t *testing.T) {
	sentinel := errors.New("independent File callback failed")
	for _, fileCohorts := range []bool{false, true} {
		t.Run(fmt.Sprint(fileCohorts), func(t *testing.T) {
			base := &batchForwardingStore{}
			var backend datastore.Datastore = base
			fileBackend := &fileBatchForwardingStore{}
			if fileCohorts {
				backend = fileBackend
				base = &fileBackend.batchForwardingStore
			}
			wrapped, _, registry := newTestInstrumented(t, backend)
			err := wrapped.(datastore.FileMutationBatcher).WithFileMutationBatch(t.Context(), "private-namespace",
				"private-repository", []string{"private-file"}, func(context.Context) error { return sentinel })
			require.ErrorIs(t, err, sentinel)
			require.Equal(t, 1, base.calls)
			require.Equal(t, "private-namespace", base.namespace)
			if fileCohorts {
				require.Equal(t, "private-repository", fileBackend.repositoryID)
				require.Equal(t, []string{"private-file"}, fileBackend.names)
			}
			require.EqualValues(t, 1, histogramObservationCount(t, registry, "CatalogMutationBatch", "test-backend"))
			families, err := registry.Gather()
			require.NoError(t, err)
			for _, family := range families {
				for _, metric := range family.Metric {
					for _, label := range metric.Label {
						require.NotContains(t, label.GetValue(), "private-")
					}
				}
			}
		})
	}
}

func (s *batchForwardingStore) WithCatalogMutationBatch(ctx context.Context, namespace string, operation func(context.Context) error) error {
	s.namespace, s.calls = namespace, s.calls+1
	for _, phase := range s.phases {
		if err := datastore.MeasureNamespaceFencePhase(ctx, phase, func() error { return nil }); err != nil {
			return err
		}
	}
	return operation(ctx)
}

func TestInstrumentedDatastoreNamespaceFencePhasesAreBounded(t *testing.T) {
	phases := []datastore.NamespaceFencePhase{
		datastore.NamespaceFenceLookup, datastore.NamespaceFenceQueue, datastore.NamespaceFenceReserve,
		datastore.NamespaceFenceMutation, datastore.NamespaceFenceRelease, 255,
	}
	store := &batchForwardingStore{phases: phases}
	wrapped, logs, registry := newTestInstrumented(t, store)
	sentinel := errors.New("phase failure")
	err := wrapped.(datastore.CatalogMutationBatcher).WithCatalogMutationBatch(t.Context(), "private-namespace", func(ctx context.Context) error {
		return datastore.MeasureNamespaceFencePhase(ctx, datastore.NamespaceFenceMutation, func() error { return sentinel })
	})
	require.ErrorIs(t, err, sentinel)
	for _, operation := range []string{"NamespaceFenceLookup", "NamespaceFenceQueue", "NamespaceFenceReserve", "NamespaceFenceRelease"} {
		require.EqualValues(t, 1, histogramObservationCount(t, registry, operation, "test-backend"))
	}
	require.EqualValues(t, 2, histogramObservationCount(t, registry, "NamespaceFenceMutation", "test-backend"))
	require.EqualValues(t, 1, counterValue(t, registry, "NamespaceFenceMutation", "test-backend"))
	require.Equal(t, 1, logs.FilterMessage("invalid namespace fence metric phase").Len())
	families, err := registry.Gather()
	require.NoError(t, err)
	for _, family := range families {
		for _, metric := range family.GetMetric() {
			for _, label := range metric.GetLabel() {
				require.NotContains(t, label.GetValue(), "private-namespace")
				require.NotContains(t, label.GetValue(), "255")
			}
		}
	}
	calls := 0
	require.ErrorIs(t, datastore.MeasureNamespaceFencePhase(t.Context(), datastore.NamespaceFenceReserve, func() error {
		calls++
		return sentinel
	}), sentinel)
	require.Equal(t, 1, calls, "an uninstrumented backend must still execute and preserve failures")
}

func TestInstrumentedDatastoreCatalogMutationPhasesAreBounded(t *testing.T) {
	wrapped, logs, registry := newTestInstrumented(t, &batchForwardingStore{})
	phases := []datastore.CatalogMutationPhase{
		datastore.CatalogMutationParentCheck, datastore.CatalogMutationReadBefore,
		datastore.CatalogMutationMembershipWrite, datastore.CatalogMutationReadAfter,
		datastore.FileMutationLocatorRead, datastore.FileMutationAuthoritativeCAS,
		datastore.FileMutationOwnerReferenceSync,
		datastore.FileMutationConditionalBatch2, datastore.FileMutationConditionalBatch3,
		datastore.FileMutationConditionalBatch4,
	}
	sentinel := errors.New("mutation phase failure")
	require.NoError(t, wrapped.(datastore.CatalogMutationBatcher).WithCatalogMutationBatch(t.Context(),
		"private-namespace", func(ctx context.Context) error {
			for _, phase := range phases {
				for _, failure := range []error{nil, sentinel, datastore.ErrNotFound, context.Canceled} {
					require.Equal(t, failure, datastore.MeasureCatalogMutationPhase(ctx, phase,
						func() error { return failure }))
				}
			}
			return datastore.MeasureCatalogMutationPhase(ctx, 255, func() error { return nil })
		}))
	for _, operation := range []string{
		"CatalogParentCheck", "CatalogProjectionReadBefore", "CatalogMembershipWrite", "CatalogProjectionReadAfter",
		"FileUIDLocatorRead", "FileAuthoritativeCAS", "FileOwnerReferenceSync",
		"FileConditionalBatch2", "FileConditionalBatch3", "FileConditionalBatch4",
	} {
		require.EqualValues(t, 4, histogramObservationCount(t, registry, operation, "test-backend"))
		require.EqualValues(t, 2, counterValue(t, registry, operation, "test-backend"))
	}
	require.Equal(t, 1, logs.FilterMessage("invalid catalog mutation metric phase").Len())
	families, err := registry.Gather()
	require.NoError(t, err)
	for _, family := range families {
		for _, metric := range family.GetMetric() {
			for _, label := range metric.GetLabel() {
				require.NotContains(t, label.GetValue(), "private-namespace")
				require.NotContains(t, label.GetValue(), "255")
			}
		}
	}
	calls := 0
	require.ErrorIs(t, datastore.MeasureCatalogMutationPhase(t.Context(), datastore.FileMutationAuthoritativeCAS,
		func() error { calls++; return sentinel }), sentinel)
	require.Equal(t, 1, calls)
}

func TestInstrumentedDatastoreForwardsCatalogBatchAndErrors(t *testing.T) {
	backend := &batchForwardingStore{}
	wrapped := datastore.NewInstrumentedDatastoreWithRegistry(backend, "scylla", zap.NewNop(), prometheus.NewRegistry())
	batcher := wrapped.(datastore.CatalogMutationBatcher)
	sentinel := errors.New("batch mutation failed")
	calls := 0
	err := batcher.WithCatalogMutationBatch(t.Context(), "acme", func(ctx context.Context) error {
		require.NotNil(t, ctx)
		calls++
		return sentinel
	})
	require.ErrorIs(t, err, sentinel)
	require.Equal(t, "acme", backend.namespace)
	require.Equal(t, 1, backend.calls)
	require.Equal(t, 1, calls)
	store, err := memdb.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	wrapped = datastore.NewInstrumentedDatastoreWithRegistry(store, "memdb", zap.NewNop(), prometheus.NewRegistry())
	err = wrapped.(datastore.CatalogMutationBatcher).WithCatalogMutationBatch(t.Context(), "acme", func(context.Context) error {
		calls++
		return sentinel
	})
	require.ErrorIs(t, err, sentinel)
	require.Equal(t, 2, calls, "a backend without a parent fence retains individual mutation semantics")
}

func TestInstrumentedDatastoreForwardsRepositoryCompletion(t *testing.T) {
	store, err := memdb.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	wrapped := datastore.NewInstrumentedDatastoreWithRegistry(store, "memdb", zap.NewNop(), prometheus.NewRegistry())
	completion, ok := any(wrapped).(datastore.RepositoryDeletionStore)
	require.True(t, ok)
	require.ErrorIs(t, completion.CompleteRepositoryDeletion(context.Background(), "00000000-0000-0000-0000-000000000299", "1"), datastore.ErrNotFound)
}

func TestInstrumentedDatastoreForwardsCategoryCompletionIdentity(t *testing.T) {
	store, err := memdb.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	ctx := t.Context()
	now := time.Now().UTC()
	category := &datastore.CategoryTaxonomy{
		UID: "00000000-0000-0000-0000-000000000298", Namespace: "acme", Name: "category",
		ResourceVersion: "7", CreationTimestamp: now, DeletionTimestamp: &now,
		Finalizers: []string{datastore.CategoryTaxonomyForegroundDeletionFinalizer},
	}
	require.NoError(t, store.CreateCategoryTaxonomy(ctx, category))
	wrapped := datastore.NewInstrumentedDatastoreWithRegistry(store, "memdb", zap.NewNop(), prometheus.NewRegistry())
	completion := wrapped.(datastore.CategoryTaxonomyDeletionStore)
	_, err = completion.CompleteCategoryTaxonomyDeletion(ctx, category.Namespace, category.Name, category.ResourceVersion,
		"00000000-0000-0000-0000-000000000297")
	require.ErrorIs(t, err, datastore.ErrConflict)
	_, err = completion.CompleteCategoryTaxonomyDeletion(ctx, category.Namespace, category.Name, "stale", category.UID)
	require.ErrorIs(t, err, datastore.ErrConflict)
	deleted, err := completion.CompleteCategoryTaxonomyDeletion(ctx, category.Namespace, category.Name, category.ResourceVersion, category.UID)
	require.NoError(t, err)
	require.Equal(t, category.UID, deleted.UID)
}

// stubDatastore is a minimal Datastore stub for decorator tests.
type stubDatastore struct {
	getProductErr          error
	getProductVal          *datastore.Product
	getProductFinding      *datastore.ProjectionFinding
	consumeAssertionResult bool
}

func (s *stubDatastore) CreateFile(_ context.Context, _ *datastore.File) error {
	return s.getProductErr
}
func (s *stubDatastore) GetFile(_ context.Context, _ string) (*datastore.File, error) {
	return nil, s.getProductErr
}
func (s *stubDatastore) GetFileSnapshots(_ context.Context, ids []string) ([]*datastore.File, error) {
	return make([]*datastore.File, len(ids)), s.getProductErr
}
func (s *stubDatastore) GetFileSnapshotsByName(_ context.Context, _ string, names []string) ([]*datastore.File, error) {
	return make([]*datastore.File, len(names)), s.getProductErr
}
func (s *stubDatastore) GetFileByName(_ context.Context, _, _ string) (*datastore.File, error) {
	return nil, s.getProductErr
}
func (s *stubDatastore) ListFiles(_ context.Context, _ string, _ datastore.PageParams) (*datastore.PageResult[datastore.File], error) {
	return nil, s.getProductErr
}
func (s *stubDatastore) UpdateFile(_ context.Context, _ *datastore.File, _ string) error {
	return s.getProductErr
}
func (s *stubDatastore) DeleteFile(_ context.Context, _ string) error { return s.getProductErr }
func (s *stubDatastore) DeleteFileWithResourceVersion(_ context.Context, _, _ string) error {
	return s.getProductErr
}
func (s *stubDatastore) UpdateFileStatus(_ context.Context, _, _ string, _ datastore.FileStatusPatch) (*datastore.File, error) {
	return nil, s.getProductErr
}

func (s *stubDatastore) CreateProduct(_ context.Context, _ *datastore.Product) error {
	return s.getProductErr
}
func (s *stubDatastore) GetProduct(ctx context.Context, _ string) (*datastore.Product, error) {
	if s.getProductFinding != nil {
		datastore.ReportProjectionFinding(ctx, *s.getProductFinding)
	}
	return s.getProductVal, s.getProductErr
}
func (s *stubDatastore) GetProductByName(_ context.Context, _, _ string) (*datastore.Product, error) {
	return nil, s.getProductErr
}
func (s *stubDatastore) ListProducts(_ context.Context, _ string, _ datastore.PageParams) (*datastore.PageResult[datastore.Product], error) {
	return nil, s.getProductErr
}
func (s *stubDatastore) UpdateProduct(_ context.Context, _ *datastore.Product, expectedResourceVersion string) error {
	return s.getProductErr
}
func (s *stubDatastore) UpdateProductStatus(_ context.Context, _, _ string, _ datastore.ProductStatusPatch) (*datastore.Product, error) {
	return nil, s.getProductErr
}
func (s *stubDatastore) DeleteProduct(_ context.Context, _ string) error {
	return s.getProductErr
}
func (s *stubDatastore) DeleteProductWithResourceVersion(_ context.Context, _, _ string) error {
	return s.getProductErr
}

func TestInstrumentedDatastorePreservesProductLifecycleCapability(t *testing.T) {
	store, err := memdb.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	product := &datastore.Product{
		UID: "00000000-0000-0000-0000-000000000551", Namespace: "acme", Name: "widget",
		APIVersion: "catalog.gitstore.dev/v1beta1", Kind: "Product", ResourceVersion: "1", Generation: 1,
	}
	require.NoError(t, store.CreateProduct(context.Background(), product))
	wrapped := datastore.NewInstrumentedDatastoreWithRegistry(store, "memdb", zap.NewNop(), prometheus.NewRegistry())
	lifecycle, ok := wrapped.(datastore.ProductLifecycleStore)
	require.True(t, ok, "instrumentation must not erase Product deletion capability")

	terminating, err := lifecycle.MarkProductTerminating(context.Background(), product.UID, "1", "gitstore.dev/foreground-deletion", time.Now())
	require.NoError(t, err)
	require.NotNil(t, terminating.DeletionTimestamp)
	require.NoError(t, lifecycle.CompleteProductDeletion(context.Background(), product.UID, terminating.ResourceVersion))
}
func (s *stubDatastore) CreateCategoryTaxonomy(_ context.Context, _ *datastore.CategoryTaxonomy) error {
	return s.getProductErr
}
func (s *stubDatastore) GetCategoryTaxonomy(_ context.Context, _ string) (*datastore.CategoryTaxonomy, error) {
	return nil, s.getProductErr
}
func (s *stubDatastore) GetCategoryTaxonomyByName(_ context.Context, _, _ string) (*datastore.CategoryTaxonomy, error) {
	return nil, s.getProductErr
}
func (s *stubDatastore) ListCategoryTaxonomies(_ context.Context, _ string, _ datastore.PageParams) (*datastore.PageResult[datastore.CategoryTaxonomy], error) {
	return nil, s.getProductErr
}
func (s *stubDatastore) UpdateCategoryTaxonomy(_ context.Context, _ *datastore.CategoryTaxonomy, expectedResourceVersion string) error {
	return s.getProductErr
}
func (s *stubDatastore) DeleteCategoryTaxonomy(_ context.Context, _ string) error {
	return s.getProductErr
}
func (s *stubDatastore) UpdateCategoryTaxonomyStatus(_ context.Context, _, _ string, _ datastore.CategoryTaxonomyStatusPatch) (*datastore.CategoryTaxonomy, error) {
	return nil, s.getProductErr
}
func (s *stubDatastore) CreateProductVariant(_ context.Context, _ *datastore.ProductVariant) error {
	return s.getProductErr
}
func (s *stubDatastore) GetProductVariant(_ context.Context, _ string) (*datastore.ProductVariant, error) {
	return nil, s.getProductErr
}
func (s *stubDatastore) GetProductVariantByName(_ context.Context, _, _ string) (*datastore.ProductVariant, error) {
	return nil, s.getProductErr
}
func (s *stubDatastore) GetProductVariantBySKU(_ context.Context, _, _ string) (*datastore.ProductVariant, error) {
	return nil, s.getProductErr
}
func (s *stubDatastore) ListProductVariants(_ context.Context, _ string, _ datastore.PageParams) (*datastore.PageResult[datastore.ProductVariant], error) {
	return nil, s.getProductErr
}
func (s *stubDatastore) ListProductVariantsByProductRef(_ context.Context, _, _ string) ([]*datastore.ProductVariant, error) {
	return nil, s.getProductErr
}
func (s *stubDatastore) UpdateProductVariant(_ context.Context, _ *datastore.ProductVariant, expectedResourceVersion string) error {
	return s.getProductErr
}
func (s *stubDatastore) DeleteProductVariant(_ context.Context, _ string) error {
	return s.getProductErr
}
func (s *stubDatastore) DeleteProductVariantWithResourceVersion(_ context.Context, _, _ string) error {
	return s.getProductErr
}
func (s *stubDatastore) CreateCollection(_ context.Context, _ *datastore.Collection) error {
	return s.getProductErr
}
func (s *stubDatastore) GetCollection(_ context.Context, _ string) (*datastore.Collection, error) {
	return nil, s.getProductErr
}
func (s *stubDatastore) GetCollectionByName(_ context.Context, _, _ string) (*datastore.Collection, error) {
	return nil, s.getProductErr
}
func (s *stubDatastore) ListCollections(_ context.Context, _ string, _ datastore.PageParams) (*datastore.PageResult[datastore.Collection], error) {
	return nil, s.getProductErr
}
func (s *stubDatastore) UpdateCollection(_ context.Context, _ *datastore.Collection, expectedResourceVersion string) error {
	return s.getProductErr
}
func (s *stubDatastore) DeleteCollection(_ context.Context, _ string) error {
	return s.getProductErr
}
func (s *stubDatastore) DeleteCollectionWithResourceVersion(_ context.Context, _, _ string) error {
	return s.getProductErr
}
func (s *stubDatastore) ListProductsByLabelSelector(_ context.Context, _ string, _ catalog.LabelSelector) ([]*datastore.Product, error) {
	return nil, s.getProductErr
}
func (s *stubDatastore) CreateNamespace(_ context.Context, _ *datastore.Namespace) error {
	return s.getProductErr
}
func (s *stubDatastore) GetNamespace(_ context.Context, _ string) (*datastore.Namespace, error) {
	return nil, s.getProductErr
}
func (s *stubDatastore) GetNamespaceByName(_ context.Context, _ string) (*datastore.Namespace, error) {
	return nil, s.getProductErr
}
func (s *stubDatastore) ListNamespaces(_ context.Context, _ datastore.PageParams) (*datastore.PageResult[datastore.Namespace], error) {
	return nil, s.getProductErr
}
func (s *stubDatastore) UpdateNamespace(_ context.Context, _ *datastore.Namespace, _ string) error {
	return s.getProductErr
}
func (s *stubDatastore) MarkNamespaceDeletion(_ context.Context, _ *datastore.Namespace, _ string) error {
	return s.getProductErr
}
func (s *stubDatastore) DeleteNamespace(_ context.Context, _ string) error {
	return s.getProductErr
}
func (s *stubDatastore) DeleteNamespaceWithResourceVersion(_ context.Context, _, _ string) error {
	return s.getProductErr
}
func (s *stubDatastore) HasRepositories(_ context.Context, _ string) (bool, error) {
	return false, s.getProductErr
}
func (s *stubDatastore) CreateRepository(_ context.Context, _ *datastore.Repository) error {
	return s.getProductErr
}
func (s *stubDatastore) CreateRepositoryInActiveNamespace(_ context.Context, _ *datastore.Repository) error {
	return s.getProductErr
}
func (s *stubDatastore) GetRepository(_ context.Context, _ string) (*datastore.Repository, error) {
	return nil, s.getProductErr
}
func (s *stubDatastore) ListRepositoriesByNamespace(_ context.Context, _ string, _ datastore.PageParams) (*datastore.PageResult[datastore.Repository], error) {
	return nil, s.getProductErr
}
func (s *stubDatastore) UpdateRepository(_ context.Context, _ *datastore.Repository, _ string) error {
	return s.getProductErr
}
func (s *stubDatastore) DeleteRepository(_ context.Context, _ string) error {
	return s.getProductErr
}
func (s *stubDatastore) HasCatalogResources(_ context.Context, _ string) (bool, error) {
	return false, s.getProductErr
}
func (s *stubDatastore) CreateNamespaceMapping(_ context.Context, _ *datastore.NamespaceMapping) error {
	return s.getProductErr
}
func (s *stubDatastore) LookupRepository(_ context.Context, _, _ string) (*datastore.NamespaceMapping, error) {
	return nil, s.getProductErr
}
func (s *stubDatastore) LookupNamespaceByRepoID(_ context.Context, _ string) (*datastore.NamespaceMapping, error) {
	return nil, s.getProductErr
}
func (s *stubDatastore) RenameRepository(_ context.Context, _, _, _ string) error {
	return s.getProductErr
}
func (s *stubDatastore) DeleteNamespaceMapping(_ context.Context, _, _ string) error {
	return s.getProductErr
}

func (s *stubDatastore) CreateServiceAccount(_ context.Context, _ *datastore.ServiceAccount) error {
	return s.getProductErr
}
func (s *stubDatastore) GetServiceAccountByUID(_ context.Context, _ string) (*datastore.ServiceAccount, error) {
	return nil, s.getProductErr
}
func (s *stubDatastore) GetServiceAccountBySubject(_ context.Context, _ string) (*datastore.ServiceAccount, error) {
	return nil, s.getProductErr
}
func (s *stubDatastore) ListServiceAccounts(_ context.Context, _ datastore.PageParams) (*datastore.PageResult[datastore.ServiceAccount], error) {
	return nil, s.getProductErr
}
func (s *stubDatastore) UpdateServiceAccountKeys(_ context.Context, _ string, _ []datastore.ServiceAccountPublicKey, _ []string, _ string) (*datastore.ServiceAccount, error) {
	return nil, s.getProductErr
}
func (s *stubDatastore) SetServiceAccountDisabled(_ context.Context, _ string, _ bool) error {
	return s.getProductErr
}
func (s *stubDatastore) DeleteServiceAccount(_ context.Context, _ string) error {
	return s.getProductErr
}
func (s *stubDatastore) TryConsumeServiceAccountAssertion(_ context.Context, _ string, _ time.Time) (bool, error) {
	return s.consumeAssertionResult, s.getProductErr
}

func (s *stubDatastore) Close() error { return nil }

// newTestInstrumented creates an InstrumentedDatastore with an observer logger
// and a fresh Prometheus registry so tests don't collide with global metrics.
func newTestInstrumented(t *testing.T, stub datastore.Datastore) (datastore.Datastore, *observer.ObservedLogs, *prometheus.Registry) {
	t.Helper()
	core, logs := observer.New(zap.DebugLevel)
	log := zap.New(core)
	reg := prometheus.NewRegistry()
	return datastore.NewInstrumentedDatastoreWithRegistry(stub, "test-backend", log, reg), logs, reg
}

func counterValue(t *testing.T, reg *prometheus.Registry, op, backend string) float64 {
	t.Helper()
	mfs, err := reg.Gather()
	require.NoError(t, err)
	for _, mf := range mfs {
		if mf.GetName() == "gitstore_datastore_operation_errors_total" {
			for _, m := range mf.GetMetric() {
				var opLabel, beLabel string
				for _, lp := range m.GetLabel() {
					if lp.GetName() == "operation" {
						opLabel = lp.GetValue()
					}
					if lp.GetName() == "backend" {
						beLabel = lp.GetValue()
					}
				}
				if opLabel == op && beLabel == backend {
					return m.GetCounter().GetValue()
				}
			}
		}
	}
	return 0
}

func histogramObservationCount(t *testing.T, reg *prometheus.Registry, op, backend string) uint64 {
	t.Helper()
	mfs, err := reg.Gather()
	require.NoError(t, err)
	for _, mf := range mfs {
		if mf.GetName() == "gitstore_datastore_operation_duration_seconds" {
			for _, m := range mf.GetMetric() {
				var opLabel, beLabel string
				for _, lp := range m.GetLabel() {
					if lp.GetName() == "operation" {
						opLabel = lp.GetValue()
					}
					if lp.GetName() == "backend" {
						beLabel = lp.GetValue()
					}
				}
				if opLabel == op && beLabel == backend {
					return m.GetHistogram().GetSampleCount()
				}
			}
		}
	}
	return 0
}

func labeledCounterValue(t *testing.T, reg *prometheus.Registry, metric string, labels map[string]string) float64 {
	t.Helper()
	mfs, err := reg.Gather()
	require.NoError(t, err)
	for _, mf := range mfs {
		if mf.GetName() != metric {
			continue
		}
		for _, m := range mf.GetMetric() {
			matches := true
			for key, want := range labels {
				found := false
				for _, lp := range m.GetLabel() {
					if lp.GetName() == key && lp.GetValue() == want {
						found = true
						break
					}
				}
				if !found {
					matches = false
					break
				}
			}
			if matches {
				return m.GetCounter().GetValue()
			}
		}
	}
	return 0
}

func TestInstrumentedDatastore_HistogramObservedOnSuccess(t *testing.T) {
	stub := &stubDatastore{getProductVal: &datastore.Product{UID: "p1"}}
	inst, _, reg := newTestInstrumented(t, stub)

	_, err := inst.GetProduct(context.Background(), "p1")
	require.NoError(t, err)

	assert.Equal(t, uint64(1), histogramObservationCount(t, reg, "GetProduct", "test-backend"))
}

func TestInstrumentedDatastore_ConsumesServiceAccountAssertion(t *testing.T) {
	stub := &stubDatastore{consumeAssertionResult: true}
	inst, _, reg := newTestInstrumented(t, stub)

	consumed, err := inst.TryConsumeServiceAccountAssertion(context.Background(), "digest", time.Now().Add(time.Minute))
	require.NoError(t, err)
	assert.True(t, consumed)
	assert.Equal(t, uint64(1), histogramObservationCount(t, reg, "TryConsumeServiceAccountAssertion", "test-backend"))
}

func TestInstrumentedDatastore_HistogramObservedOnError(t *testing.T) {
	stub := &stubDatastore{getProductErr: datastore.ErrNotFound}
	inst, _, reg := newTestInstrumented(t, stub)

	_, err := inst.GetProduct(context.Background(), "missing")
	require.Error(t, err)

	assert.Equal(t, uint64(1), histogramObservationCount(t, reg, "GetProduct", "test-backend"))
}

func TestInstrumentedDatastore_ErrorCounterIncrementedOnError(t *testing.T) {
	stub := &stubDatastore{getProductErr: errors.New("boom")}
	inst, _, reg := newTestInstrumented(t, stub)

	inst.GetProduct(context.Background(), "missing") //nolint:errcheck

	assert.Equal(t, float64(1), counterValue(t, reg, "GetProduct", "test-backend"))
}

func TestInstrumentedDatastore_ErrNotFoundDoesNotIncrementErrorCounter(t *testing.T) {
	stub := &stubDatastore{getProductErr: datastore.ErrNotFound}
	inst, _, reg := newTestInstrumented(t, stub)

	inst.GetProduct(context.Background(), "missing") //nolint:errcheck

	assert.Equal(t, float64(0), counterValue(t, reg, "GetProduct", "test-backend"))
}

func TestInstrumentedDatastore_ErrorCounterNotIncrementedOnSuccess(t *testing.T) {
	stub := &stubDatastore{getProductVal: &datastore.Product{UID: "p1"}}
	inst, _, reg := newTestInstrumented(t, stub)

	inst.GetProduct(context.Background(), "p1") //nolint:errcheck

	assert.Equal(t, float64(0), counterValue(t, reg, "GetProduct", "test-backend"))
}

func TestInstrumentedDatastore_ZapErrorLogOnFailure(t *testing.T) {
	stub := &stubDatastore{getProductErr: errors.New("boom")}
	inst, logs, _ := newTestInstrumented(t, stub)

	inst.GetProduct(context.Background(), "x") //nolint:errcheck

	require.Equal(t, 1, logs.Len())
	entry := logs.All()[0]
	assert.Equal(t, "datastore operation failed", entry.Message)

	fields := make(map[string]interface{})
	for _, f := range entry.Context {
		fields[f.Key] = f.String
	}
	assert.Equal(t, "GetProduct", fields["operation"])
	assert.Equal(t, "test-backend", fields["backend"])
}

func TestInstrumentedDatastore_NoLogOnErrNotFound(t *testing.T) {
	stub := &stubDatastore{getProductErr: datastore.ErrNotFound}
	inst, logs, _ := newTestInstrumented(t, stub)

	inst.GetProduct(context.Background(), "missing") //nolint:errcheck

	assert.Equal(t, 0, logs.Len())
}

func TestInstrumentedDatastore_NoLogOnSuccess(t *testing.T) {
	stub := &stubDatastore{getProductVal: &datastore.Product{UID: "p1"}}
	inst, logs, _ := newTestInstrumented(t, stub)

	inst.GetProduct(context.Background(), "p1") //nolint:errcheck

	assert.Equal(t, 0, logs.Len())
}

func TestInstrumentedDatastore_RepairRequiredMetricsUseBoundedLabels(t *testing.T) {
	stub := &stubDatastore{getProductErr: datastore.NewRepairRequiredError(
		datastore.MutationStep{
			Operation:    "create",
			ResourceKind: "Product",
			Projection:   "products_by_name",
			Action:       "reserve",
		},
		errors.New("primary"),
		errors.New("compensation"),
	)}
	inst, logs, reg := newTestInstrumented(t, stub)

	inst.GetProduct(context.Background(), "sensitive-resource-id") //nolint:errcheck

	assert.Equal(t, float64(1), labeledCounterValue(t, reg,
		"gitstore_datastore_projection_write_failures_total",
		map[string]string{"operation": "create", "backend": "test-backend", "projection": "products_by_name"}))
	assert.Equal(t, float64(1), labeledCounterValue(t, reg,
		"gitstore_datastore_compensation_failures_total",
		map[string]string{"operation": "create", "backend": "test-backend", "projection": "products_by_name"}))
	require.Equal(t, 1, logs.Len())
	assert.NotContains(t, logs.All()[0].ContextMap(), "resource_uid")
}

func TestInstrumentedDatastore_ProjectionFindingIsObserved(t *testing.T) {
	finding := datastore.ProjectionFinding{
		Operation:    "lookup",
		ResourceKind: "Product",
		ResourceUID:  "sensitive-resource-id",
		Projection:   "products_by_name",
		LookupKey:    "tenant/widget",
		Type:         datastore.FindingDangling,
	}
	stub := &stubDatastore{
		getProductVal:     &datastore.Product{UID: "p1"},
		getProductFinding: &finding,
	}
	inst, logs, reg := newTestInstrumented(t, stub)
	var callerObserved datastore.ProjectionFinding
	ctx := datastore.WithProjectionFindingObserver(context.Background(), func(observed datastore.ProjectionFinding) {
		callerObserved = observed
	})

	_, err := inst.GetProduct(ctx, "p1")
	require.NoError(t, err)

	assert.Equal(t, finding, callerObserved)
	assert.Equal(t, float64(1), labeledCounterValue(t, reg,
		"gitstore_datastore_projection_findings_total",
		map[string]string{
			"operation":     "lookup",
			"backend":       "test-backend",
			"resource_kind": "Product",
			"projection":    "products_by_name",
			"finding_type":  "dangling",
		}))
	require.Equal(t, 1, logs.Len())
	assert.Equal(t, "datastore projection inconsistency", logs.All()[0].Message)
}
