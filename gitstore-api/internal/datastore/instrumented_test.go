// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package datastore_test

import (
	"context"
	"errors"
	"testing"

	"github.com/gitstore-dev/gitstore/api/internal/catalog"
	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
)

// stubDatastore is a minimal Datastore stub for decorator tests.
type stubDatastore struct {
	getProductErr error
	getProductVal *datastore.Product
}

func (s *stubDatastore) CreateProduct(_ context.Context, _ *datastore.Product) error {
	return s.getProductErr
}
func (s *stubDatastore) GetProduct(_ context.Context, _ string) (*datastore.Product, error) {
	return s.getProductVal, s.getProductErr
}
func (s *stubDatastore) GetProductByName(_ context.Context, _, _ string) (*datastore.Product, error) {
	return nil, s.getProductErr
}
func (s *stubDatastore) ListProducts(_ context.Context, _ string, _ datastore.PageParams) (*datastore.PageResult[datastore.Product], error) {
	return nil, s.getProductErr
}
func (s *stubDatastore) UpdateProduct(_ context.Context, _ *datastore.Product) error {
	return s.getProductErr
}
func (s *stubDatastore) DeleteProduct(_ context.Context, _ string) error {
	return s.getProductErr
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
func (s *stubDatastore) UpdateCategoryTaxonomy(_ context.Context, _ *datastore.CategoryTaxonomy) error {
	return s.getProductErr
}
func (s *stubDatastore) DeleteCategoryTaxonomy(_ context.Context, _ string) error {
	return s.getProductErr
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
func (s *stubDatastore) UpdateProductVariant(_ context.Context, _ *datastore.ProductVariant) error {
	return s.getProductErr
}
func (s *stubDatastore) DeleteProductVariant(_ context.Context, _ string) error {
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
func (s *stubDatastore) UpdateCollection(_ context.Context, _ *datastore.Collection) error {
	return s.getProductErr
}
func (s *stubDatastore) DeleteCollection(_ context.Context, _ string) error {
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
func (s *stubDatastore) GetNamespaceByIdentifier(_ context.Context, _ string) (*datastore.Namespace, error) {
	return nil, s.getProductErr
}
func (s *stubDatastore) ListNamespaces(_ context.Context, _ datastore.PageParams) (*datastore.PageResult[datastore.Namespace], error) {
	return nil, s.getProductErr
}
func (s *stubDatastore) DeleteNamespace(_ context.Context, _ string) error {
	return s.getProductErr
}
func (s *stubDatastore) CreateRepository(_ context.Context, _ *datastore.Repository) error {
	return s.getProductErr
}
func (s *stubDatastore) GetRepository(_ context.Context, _ string) (*datastore.Repository, error) {
	return nil, s.getProductErr
}
func (s *stubDatastore) ListRepositoriesByNamespace(_ context.Context, _ string, _ datastore.PageParams) (*datastore.PageResult[datastore.Repository], error) {
	return nil, s.getProductErr
}
func (s *stubDatastore) UpdateRepository(_ context.Context, _ *datastore.Repository) error {
	return s.getProductErr
}
func (s *stubDatastore) DeleteRepository(_ context.Context, _ string) error {
	return s.getProductErr
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
func (s *stubDatastore) TransferRepository(_ context.Context, _, _, _ string) error {
	return s.getProductErr
}
func (s *stubDatastore) DeleteNamespaceMapping(_ context.Context, _, _ string) error {
	return s.getProductErr
}
func (s *stubDatastore) Close() error { return nil }

// newTestInstrumented creates an InstrumentedDatastore with an observer logger
// and a fresh Prometheus registry so tests don't collide with global metrics.
func newTestInstrumented(t *testing.T, stub datastore.Datastore) (datastore.Datastore, *observer.ObservedLogs, *prometheus.Registry) {
	t.Helper()
	core, logs := observer.New(zap.ErrorLevel)
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

func TestInstrumentedDatastore_HistogramObservedOnSuccess(t *testing.T) {
	stub := &stubDatastore{getProductVal: &datastore.Product{UID: "p1"}}
	inst, _, reg := newTestInstrumented(t, stub)

	_, err := inst.GetProduct(context.Background(), "p1")
	require.NoError(t, err)

	assert.Equal(t, uint64(1), histogramObservationCount(t, reg, "GetProduct", "test-backend"))
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
