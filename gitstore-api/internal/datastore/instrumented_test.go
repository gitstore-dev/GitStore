// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package datastore_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gitstore-dev/gitstore/api/internal/catalog"
	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	"github.com/gitstore-dev/gitstore/api/internal/datastore/memdb"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
)

func TestInstrumentedDatastoreExemplarsIdentifyActualOperationAndPhaseSpans(t *testing.T) {
	previous := otel.GetTracerProvider()
	defer otel.SetTracerProvider(previous)
	for _, sampled := range []bool{false, true} {
		t.Run(fmt.Sprint(sampled), func(t *testing.T) {
			recorder := tracetest.NewSpanRecorder()
			provider := sdktrace.NewTracerProvider(sdktrace.WithSampler(sdktrace.ParentBased(sdktrace.NeverSample())),
				sdktrace.WithSpanProcessor(recorder))
			defer provider.Shutdown(t.Context())
			otel.SetTracerProvider(provider)
			flags := trace.TraceFlags(0)
			if sampled {
				flags = trace.FlagsSampled
			}
			remote := trace.NewSpanContext(trace.SpanContextConfig{
				TraceID: trace.TraceID{1}, SpanID: trace.SpanID{2}, TraceFlags: flags, Remote: true,
			})
			ctx, parent := provider.Tracer("test").Start(trace.ContextWithRemoteSpanContext(t.Context(), remote), "api.request")
			defer parent.End()
			wrapped, _, registry := newTestInstrumented(t, &batchForwardingStore{
				phases: []datastore.NamespaceFencePhase{datastore.NamespaceFenceLookup},
			})
			require.NoError(t, wrapped.(datastore.CatalogMutationBatcher).WithCatalogMutationBatch(ctx, "private-namespace", func(ctx context.Context) error {
				if err := datastore.MeasureCatalogMutationPhase(ctx, datastore.CatalogMutationParentCheck, func() error { return nil }); err != nil {
					return err
				}
				finish := datastore.StartCatalogMutationPhase(ctx, datastore.FileMutationConditionalQueue)
				require.NotNil(t, finish)
				done := make(chan struct{})
				go func() {
					finish(nil)
					close(done)
				}()
				<-done
				for _, phase := range []datastore.CatalogMutationPhase{datastore.FileProjectionReadQueue, datastore.FileProjectionReadBatch16} {
					require.NoError(t, datastore.MeasureCatalogMutationPhase(ctx, phase, func() error { return nil }))
				}
				for _, phase := range []datastore.NamespaceFencePhase{
					datastore.NamespaceFenceReserveRead, datastore.NamespaceFenceReserveCAS,
					datastore.NamespaceFenceReserveBusyWait, datastore.NamespaceFenceReserveConflictWait,
				} {
					require.NoError(t, datastore.MeasureNamespaceFencePhase(ctx, phase, func() error { return nil }))
				}
				return datastore.MeasureCatalogMutationPhase(ctx, datastore.FileMutationConditionalSingle, func() error { return nil })
			}))
			ids := map[string]trace.SpanContext{}
			for _, span := range recorder.Ended() {
				require.Equal(t, parent.SpanContext().SpanID(), span.Parent().SpanID())
				ids[strings.TrimPrefix(span.Name(), "datastore.")] = span.SpanContext()
			}
			families, err := registry.Gather()
			require.NoError(t, err)
			observations := 0
			for _, family := range families {
				if family.GetName() != "gitstore_datastore_operation_duration_seconds" {
					continue
				}
				for _, series := range family.Metric {
					observations++
					require.Len(t, series.Label, 2)
					var operation string
					for _, label := range series.Label {
						require.NotContains(t, label.GetValue(), "private-")
						if label.GetName() == "operation" {
							operation = label.GetValue()
						}
					}
					require.EqualValues(t, 1, series.Histogram.GetSampleCount())
					exemplars := 0
					for _, bucket := range series.Histogram.Bucket {
						if exemplar := bucket.Exemplar; exemplar != nil {
							exemplars++
							require.Len(t, exemplar.Label, 2)
							values := map[string]string{}
							for _, label := range exemplar.Label {
								values[label.GetName()] = label.GetValue()
							}
							require.Equal(t, map[string]string{
								"trace_id": ids[operation].TraceID().String(),
								"span_id":  ids[operation].SpanID().String(),
							}, values)
						}
					}
					require.Equal(t, sampled, exemplars == 1)
				}
			}
			require.Equal(t, 11, observations, "each operation, queue, physical read/write and reservation subphase must be observed once")
			request := httptest.NewRequest(http.MethodGet, "/metrics", nil)
			request.Header.Set("Accept", "application/openmetrics-text; version=1.0.0")
			response := httptest.NewRecorder()
			promhttp.HandlerFor(registry, promhttp.HandlerOpts{EnableOpenMetrics: true}).ServeHTTP(response, request)
			require.Equal(t, sampled, strings.Contains(response.Body.String(), `trace_id="`))
			if sampled {
				for _, operation := range []string{
					"CatalogMutationBatch", "NamespaceFenceLookup", "CatalogParentCheck", "FileConditionalQueue", "FileConditionalSingle",
					"FileProjectionReadQueue", "FileProjectionReadBatch16",
					"NamespaceFenceReserveRead", "NamespaceFenceReserveCAS", "NamespaceFenceReserveBusyWait", "NamespaceFenceReserveConflictWait",
				} {
					require.Contains(t, response.Body.String(), `span_id="`+ids[operation].SpanID().String()+`"`)
				}
			}
		})
	}
}

type batchForwardingStore struct {
	datastore.Datastore
	namespace string
	calls     int
	phases    []datastore.NamespaceFencePhase
}

func TestInstrumentedDatastoreFileCohortsLinkExactOwnerAndPhysicalPhases(t *testing.T) {
	previous := otel.GetTracerProvider()
	defer otel.SetTracerProvider(previous)
	recorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSampler(sdktrace.AlwaysSample()), sdktrace.WithSpanProcessor(recorder))
	defer provider.Shutdown(t.Context())
	otel.SetTracerProvider(provider)
	leaderCtx, leader := provider.Tracer("test").Start(t.Context(), "leader")
	followerCtx, follower := provider.Tracer("test").Start(t.Context(), "follower")
	wrapped, logs, registry := newTestInstrumented(t, &fileBatchForwardingStore{})
	batcher := wrapped.(datastore.FileMutationBatcher)
	sentinel := errors.New("private-namespace failure")
	require.ErrorIs(t, batcher.WithFileMutationBatch(leaderCtx, "private-namespace", "private-repository",
		[]string{"private-file"}, func(ctx context.Context) error {
			canceledLeader, cancelLeader := context.WithCancel(ctx)
			cancelLeader()
			group, cancelGroup := context.WithCancel(context.WithoutCancel(canceledLeader))
			defer cancelGroup()
			owner, finish := datastore.StartFileMutationCohort(group)
			require.NoError(t, owner.Err(), "a canceled leader cannot cancel other selected callbacks")
			datastore.ObserveFileMutationCohort(ctx, owner, 0, 2)
			require.NoError(t, batcher.WithFileMutationBatch(followerCtx, "private-namespace", "private-repository",
				[]string{"private-other-file"}, func(member context.Context) error {
					datastore.ObserveFileMutationCohort(member, owner, 1, 2)
					datastore.ObserveFileMutationCohort(member, owner, 4, 5)
					return datastore.MeasureCatalogMutationPhase(member, datastore.FileMutationConditionalSingle, func() error { return nil })
				}))
			for _, phase := range []datastore.NamespaceFencePhase{
				datastore.NamespaceFenceReserve, datastore.NamespaceFenceMutation, datastore.NamespaceFenceRelease,
			} {
				require.NoError(t, datastore.MeasureNamespaceFencePhase(owner, phase, func() error { return nil }))
			}
			cancelGroup()
			require.ErrorIs(t, owner.Err(), context.Canceled, "observation must preserve group cancellation")
			finish(sentinel)
			return sentinel
		}), sentinel)
	leader.End()
	follower.End()
	var owner sdktrace.ReadOnlySpan
	roots := map[trace.SpanID]sdktrace.ReadOnlySpan{}
	for _, span := range recorder.Ended() {
		if span.Name() == "datastore.FileMutationCohort" {
			require.Nil(t, owner)
			owner = span
		}
		if span.Name() == "leader" || span.Name() == "follower" {
			roots[span.SpanContext().SpanID()] = span
		}
		for _, attr := range span.Attributes() {
			require.NotContains(t, attr.Value.AsString(), "private-")
		}
		require.NotContains(t, span.Status().Description, "private-")
	}
	require.NotNil(t, owner, "a physical cohort needs its own unique span, not inferred temporal adjacency")
	require.Equal(t, leader.SpanContext().SpanID(), owner.Parent().SpanID())
	require.Len(t, owner.Links(), 2)
	require.Equal(t, 1, logs.FilterMessage("invalid File mutation cohort observation").Len())
	for _, link := range owner.Links() {
		root := roots[link.SpanContext.SpanID()]
		require.NotNil(t, root)
		require.Len(t, root.Links(), 1)
		require.Equal(t, owner.SpanContext(), root.Links()[0].SpanContext)
	}
	for _, span := range recorder.Ended() {
		switch span.Name() {
		case "datastore.NamespaceFenceReserve", "datastore.NamespaceFenceMutation", "datastore.NamespaceFenceRelease":
			require.Equal(t, owner.SpanContext().SpanID(), span.Parent().SpanID())
			require.False(t, span.StartTime().Before(owner.StartTime()))
			require.False(t, span.EndTime().After(owner.EndTime()))
		case "datastore.FileConditionalSingle":
			require.Equal(t, follower.SpanContext().SpanID(), span.Parent().SpanID(), "callback phases retain their own request context")
		}
	}
	require.EqualValues(t, 2, histogramObservationCount(t, registry, "CatalogMutationBatch", "test-backend"))
	require.EqualValues(t, 1, histogramObservationCount(t, registry, "NamespaceFenceReserve", "test-backend"))
	require.EqualValues(t, 1, histogramObservationCount(t, registry, "FileConditionalSingle", "test-backend"))
	ctx, finish := datastore.StartFileMutationCohort(t.Context())
	require.Equal(t, t.Context(), ctx)
	datastore.ObserveFileMutationCohort(ctx, ctx, 0, 1)
	finish(nil)
}

type categoryProductForwardingStore struct {
	datastore.Datastore
	ctx          context.Context
	product      *datastore.Product
	categoryUIDs []string
	namespace    string
	categoryUID  string
	page         datastore.PageParams
	result       *datastore.PageResult[datastore.Product]
	failure      error
}

func (s *categoryProductForwardingStore) ReplaceCategoryProductMembership(ctx context.Context, product *datastore.Product, categoryUIDs []string) error {
	s.ctx, s.product, s.categoryUIDs = ctx, product, categoryUIDs
	return s.failure
}

func (s *categoryProductForwardingStore) ListCategoryProducts(ctx context.Context, namespace, categoryUID string, page datastore.PageParams) (*datastore.PageResult[datastore.Product], error) {
	s.ctx, s.namespace, s.categoryUID, s.page = ctx, namespace, categoryUID, page
	return s.result, s.failure
}

func TestInstrumentedDatastoreCategoryProductsForwarding(t *testing.T) {
	for _, failure := range []error{nil, datastore.ErrProjectionNotReady, context.Canceled, errors.New("membership storage unavailable")} {
		t.Run(fmt.Sprint(failure), func(t *testing.T) {
			result := &datastore.PageResult[datastore.Product]{}
			backend := &categoryProductForwardingStore{result: result, failure: failure}
			wrapped, logs, registry := newTestInstrumented(t, backend)
			index, ok := wrapped.(datastore.CategoryProductIndex)
			require.True(t, ok, "application instrumentation must retain the category membership capability")
			product := &datastore.Product{UID: "private-product"}
			categoryUIDs := []string{"private-category", "private-ancestor"}
			ctx, cancel := context.WithCancel(t.Context())
			if errors.Is(failure, context.Canceled) {
				cancel()
			}
			defer cancel()
			require.ErrorIs(t, index.ReplaceCategoryProductMembership(ctx, product, categoryUIDs), failure)
			require.Same(t, product, backend.product)
			require.Equal(t, categoryUIDs, backend.categoryUIDs)
			require.Equal(t, ctx.Err(), backend.ctx.Err())
			page := datastore.PageParams{Last: 17, Before: "private-cursor"}
			got, err := index.ListCategoryProducts(ctx, "private-namespace", "private-category", page)
			require.ErrorIs(t, err, failure)
			require.Same(t, result, got)
			require.Equal(t, "private-namespace", backend.namespace)
			require.Equal(t, "private-category", backend.categoryUID)
			require.Equal(t, page, backend.page)
			for _, operation := range []string{"ListCategoryProducts", "ReplaceCategoryProductMembership"} {
				require.EqualValues(t, 1, histogramObservationCount(t, registry, operation, "test-backend"))
				if failure != nil {
					require.EqualValues(t, 1, counterValue(t, registry, operation, "test-backend"))
				}
			}
			if failure != nil {
				require.Equal(t, 2, logs.Len())
			}
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

func TestInstrumentedDatastoreCategoryProductsUnsupportedFailsClosed(t *testing.T) {
	wrapped, logs, registry := newTestInstrumented(t, &stubDatastore{})
	index, ok := wrapped.(datastore.CategoryProductIndex)
	require.True(t, ok)
	err := index.ReplaceCategoryProductMembership(t.Context(), &datastore.Product{}, nil)
	require.ErrorIs(t, err, datastore.ErrProjectionNotReady)
	page, err := index.ListCategoryProducts(t.Context(), "namespace", "category", datastore.PageParams{First: 1})
	require.ErrorIs(t, err, datastore.ErrProjectionNotReady)
	require.Nil(t, page)
	require.Equal(t, 2, logs.Len())
	for _, operation := range []string{"ListCategoryProducts", "ReplaceCategoryProductMembership"} {
		require.EqualValues(t, 1, histogramObservationCount(t, registry, operation, "test-backend"))
		require.EqualValues(t, 1, counterValue(t, registry, operation, "test-backend"))
	}
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

type fileSnapshotBatchForwardingStore struct {
	fileBatchForwardingStore
	uids []string
}

func (s *fileSnapshotBatchForwardingStore) WithFileMutationBatchSnapshots(ctx context.Context, namespace, repositoryID string, names, uids []string, operation func(context.Context) error) error {
	s.uids = uids
	return s.WithFileMutationBatch(ctx, namespace, repositoryID, names, operation)
}

func TestInstrumentedDatastoreForwardsFileMutationSnapshotsAndOlderBackends(t *testing.T) {
	for _, supportsSnapshots := range []bool{false, true} {
		t.Run(fmt.Sprint(supportsSnapshots), func(t *testing.T) {
			next := &fileSnapshotBatchForwardingStore{}
			var backend datastore.Datastore = &next.fileBatchForwardingStore
			if supportsSnapshots {
				backend = next
			}
			wrapped, _, registry := newTestInstrumented(t, backend)
			sentinel := errors.New("snapshot callback failed")
			err := wrapped.(datastore.FileMutationSnapshotBatcher).WithFileMutationBatchSnapshots(t.Context(),
				"private-namespace", "private-repository", []string{"first", "second"}, []string{"uid-a", "uid-b"},
				func(ctx context.Context) error {
					return datastore.MeasureCatalogMutationPhase(ctx, datastore.FileMutationSnapshotRead, func() error { return sentinel })
				})
			require.ErrorIs(t, err, sentinel)
			require.Equal(t, 1, next.calls)
			require.Equal(t, []string{"first", "second"}, next.names)
			if supportsSnapshots {
				require.Equal(t, []string{"uid-a", "uid-b"}, next.uids)
			} else {
				require.Empty(t, next.uids)
			}
			require.EqualValues(t, 1, histogramObservationCount(t, registry, "CatalogMutationBatch", "test-backend"))
			require.EqualValues(t, 1, histogramObservationCount(t, registry, "FileMutationSnapshotRead", "test-backend"))
		})
	}
}

func TestInstrumentedDatastoreNamespaceFencePhasesAreBounded(t *testing.T) {
	phases := []datastore.NamespaceFencePhase{
		datastore.NamespaceFenceLookup, datastore.NamespaceFenceQueue, datastore.NamespaceFenceReserve,
		datastore.NamespaceFenceMutation, datastore.NamespaceFenceRelease, 255,
		datastore.NamespaceFenceReserveRead, datastore.NamespaceFenceReserveCAS,
		datastore.NamespaceFenceReserveBusyWait, datastore.NamespaceFenceReserveConflictWait,
	}
	store := &batchForwardingStore{phases: phases}
	wrapped, logs, registry := newTestInstrumented(t, store)
	sentinel := errors.New("phase failure")
	err := wrapped.(datastore.CatalogMutationBatcher).WithCatalogMutationBatch(t.Context(), "private-namespace", func(ctx context.Context) error {
		return datastore.MeasureNamespaceFencePhase(ctx, datastore.NamespaceFenceMutation, func() error { return sentinel })
	})
	require.ErrorIs(t, err, sentinel)
	for _, operation := range []string{
		"NamespaceFenceLookup", "NamespaceFenceQueue", "NamespaceFenceReserve", "NamespaceFenceRelease",
		"NamespaceFenceReserveRead", "NamespaceFenceReserveCAS", "NamespaceFenceReserveBusyWait", "NamespaceFenceReserveConflictWait",
	} {
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
		datastore.FileMutationConditionalQueue, datastore.FileMutationConditionalSingle,
		datastore.FileMutationSnapshotRead,
		datastore.FileProjectionReadQueue,
	}
	for phase := datastore.FileMutationConditionalBatch2; phase <= datastore.FileMutationConditionalBatch16; phase++ {
		phases = append(phases, phase)
	}
	for phase := datastore.FileProjectionReadBatch1; phase <= datastore.FileProjectionReadBatch16; phase++ {
		phases = append(phases, phase)
	}
	sentinel := errors.New("mutation phase failure")
	require.NoError(t, wrapped.(datastore.CatalogMutationBatcher).WithCatalogMutationBatch(t.Context(),
		"private-namespace", func(ctx context.Context) error {
			for _, phase := range phases {
				for _, failure := range []error{nil, sentinel, datastore.ErrNotFound, context.Canceled} {
					if phase == datastore.FileMutationConditionalQueue {
						finish := datastore.StartCatalogMutationPhase(ctx, phase)
						require.NotNil(t, finish)
						finish(failure)
						continue
					}
					require.Equal(t, failure, datastore.MeasureCatalogMutationPhase(ctx, phase,
						func() error { return failure }))
				}
			}
			return datastore.MeasureCatalogMutationPhase(ctx, 255, func() error { return nil })
		}))
	operations := []string{
		"CatalogParentCheck", "CatalogProjectionReadBefore", "CatalogMembershipWrite", "CatalogProjectionReadAfter",
		"FileUIDLocatorRead", "FileAuthoritativeCAS", "FileOwnerReferenceSync",
		"FileConditionalQueue", "FileConditionalSingle",
		"FileMutationSnapshotRead", "FileProjectionReadQueue",
	}
	for size := 2; size <= 16; size++ {
		operations = append(operations, fmt.Sprintf("FileConditionalBatch%d", size))
	}
	for size := 1; size <= 16; size++ {
		operations = append(operations, fmt.Sprintf("FileProjectionReadBatch%d", size))
	}
	for _, operation := range operations {
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
	require.Nil(t, datastore.StartCatalogMutationPhase(t.Context(), datastore.FileMutationConditionalQueue))
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
