// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package resolver_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	"github.com/gitstore-dev/gitstore/api/internal/datastore/memdb"
	"github.com/gitstore-dev/gitstore/api/internal/graph/model"
	"github.com/gitstore-dev/gitstore/api/internal/graph/resolver"
	namespaceadmission "github.com/gitstore-dev/gitstore/api/internal/namespace"
	"github.com/google/uuid"
	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/vektah/gqlparser/v2/gqlerror"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"
)

type concurrentNamespaceDeleteStore struct {
	datastore.Datastore
	once sync.Once
}

func (s *concurrentNamespaceDeleteStore) MarkNamespaceDeletion(
	ctx context.Context,
	namespace *datastore.Namespace,
	expectedResourceVersion string,
) error {
	var markErr error
	s.once.Do(func() {
		type deletionMarker interface {
			MarkNamespaceDeletion(context.Context, *datastore.Namespace, string) error
		}
		markErr = s.Datastore.(deletionMarker).MarkNamespaceDeletion(ctx, namespace, expectedResourceVersion)
	})
	if markErr != nil {
		return markErr
	}
	return datastore.ErrNamespaceNotActive
}

func TestDeleteNamespaceBlockerMatrix(t *testing.T) {
	t.Run("bootstrap only", func(t *testing.T) {
		svc := newTestSvc(t, &mockGitWriter{})
		ctx := context.Background()
		ns, err := svc.GetNamespaceByName(ctx, "gitstore-system")
		require.NoError(t, err)
		repo, err := svc.Store().LookupRepository(ctx, ns.Name, "gitstore-system")
		require.NoError(t, err)
		require.NoError(t, svc.Store().DeleteRepository(ctx, repo.RepositoryID))

		_, err = svc.DeleteNamespace(ctx, ns)
		requireDeletionReasons(t, err, namespaceadmission.ReasonBootstrapNamespace)
	})

	t.Run("non-empty only", func(t *testing.T) {
		svc := newTestSvc(t, &mockGitWriter{})
		ctx := context.Background()
		ns, err := svc.CreateNamespace(ctx, createNamespaceInput("occupied", model.NamespaceTierUser), "alice")
		require.NoError(t, err)
		_, err = svc.CreateRepository(ctx, ns.ID, "catalog", "main", "default", "alice")
		require.NoError(t, err)

		_, err = svc.DeleteNamespace(ctx, ns)
		requireDeletionReasons(t, err, namespaceadmission.ReasonNamespaceNotEmpty)
	})

	t.Run("combined", func(t *testing.T) {
		svc := newTestSvc(t, &mockGitWriter{})
		ctx := context.Background()
		ns, err := svc.GetNamespaceByName(ctx, "gitstore-system")
		require.NoError(t, err)

		_, err = svc.DeleteNamespace(ctx, ns)
		requireDeletionReasons(t, err,
			namespaceadmission.ReasonBootstrapNamespace,
			namespaceadmission.ReasonNamespaceNotEmpty,
		)
	})
}

func TestDeleteNamespaceOutcomeMatrix(t *testing.T) {
	svc := newTestSvc(t, &mockGitWriter{})
	ctx := context.Background()
	ns, err := svc.CreateNamespace(ctx, createNamespaceInput("delete-outcomes", model.NamespaceTierUser), "alice")
	require.NoError(t, err)

	outcome, err := svc.DeleteNamespace(ctx, ns)
	require.NoError(t, err)
	assert.Equal(t, namespaceadmission.DeletionOutcomeTerminationStarted, outcome)
	first, err := svc.GetNamespaceByName(ctx, ns.Name)
	require.NoError(t, err)
	require.NotNil(t, first.DeletionTimestamp)
	assert.Contains(t, first.Finalizers, datastore.NamespaceForegroundDeletionFinalizer)
	assert.Equal(t, "2", first.ResourceVersion)

	outcome, err = svc.DeleteNamespace(ctx, first)
	require.NoError(t, err)
	assert.Equal(t, namespaceadmission.DeletionOutcomeAlreadyTerminating, outcome)
	second, err := svc.GetNamespaceByName(ctx, ns.Name)
	require.NoError(t, err)
	assert.Equal(t, first.ResourceVersion, second.ResourceVersion)
	assert.Equal(t, first.DeletionTimestamp, second.DeletionTimestamp)
}

func TestDeleteNamespaceConcurrentWinnerReturnsAlreadyTerminating(t *testing.T) {
	ctx := context.Background()
	base, err := memdb.New()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, base.Close()) })
	store := &concurrentNamespaceDeleteStore{Datastore: base}
	svc, err := resolver.NewService(resolver.ServiceDeps{
		Store:  store,
		Logger: zap.NewNop(),
	})
	require.NoError(t, err)
	now := time.Now().UTC()
	namespace := &datastore.Namespace{
		ID:                uuid.NewString(),
		Name:              "concurrent-delete",
		ResourceVersion:   "1",
		Generation:        1,
		CreationTimestamp: now,
		UpdateTimestamp:   now,
	}
	namespace.UID = namespace.ID
	require.NoError(t, base.CreateNamespace(ctx, namespace))

	outcome, err := svc.DeleteNamespace(ctx, namespace)

	require.NoError(t, err)
	assert.Equal(t, namespaceadmission.DeletionOutcomeAlreadyTerminating, outcome)
	current, err := base.GetNamespaceByName(ctx, namespace.Name)
	require.NoError(t, err)
	require.NotNil(t, current.DeletionTimestamp)
}

func TestDeleteNamespaceRecreatedIdentifierProtection(t *testing.T) {
	svc := newTestSvc(t, &mockGitWriter{})
	ctx := context.Background()
	input := createNamespaceInput("replacement-safe", model.NamespaceTierUser)
	authorized, err := svc.CreateNamespace(ctx, input, "alice")
	require.NoError(t, err)
	require.NoError(t, svc.Store().DeleteNamespace(ctx, authorized.ID))
	replacement, err := svc.CreateNamespace(ctx, input, "mallory")
	require.NoError(t, err)

	_, err = svc.DeleteNamespace(ctx, authorized)
	require.Error(t, err)
	got, getErr := svc.GetNamespaceByName(ctx, input.Metadata.Name)
	require.NoError(t, getErr)
	assert.Equal(t, replacement.ID, got.ID)
	assert.Nil(t, got.DeletionTimestamp)
	assert.Equal(t, replacement.ResourceVersion, got.ResourceVersion)
}

func TestDeleteNamespaceRecordsBoundedOutcomesBlockersAndLogs(t *testing.T) {
	ctx := context.Background()
	store, err := memdb.New()
	require.NoError(t, err)
	registry := prometheus.NewRegistry()
	metrics := namespaceadmission.NewMetrics(registry)
	core, logs := observer.New(zapcore.InfoLevel)
	svc, err := resolver.NewService(resolver.ServiceDeps{
		Store:            store,
		Logger:           zap.New(core),
		NamespaceMetrics: metrics,
	})
	require.NoError(t, err)

	now := time.Now().UTC()
	eligible := &datastore.Namespace{
		ID:                uuid.NewString(),
		Name:              "observable-delete",
		ResourceVersion:   "1",
		Generation:        1,
		CreationTimestamp: now,
		UpdateTimestamp:   now,
	}
	eligible.UID = eligible.ID
	require.NoError(t, store.CreateNamespace(ctx, eligible))
	outcome, err := svc.DeleteNamespace(ctx, eligible)
	require.NoError(t, err)
	assert.Equal(t, namespaceadmission.DeletionOutcomeTerminationStarted, outcome)
	current, err := store.GetNamespaceByName(ctx, eligible.Name)
	require.NoError(t, err)
	outcome, err = svc.DeleteNamespace(ctx, current)
	require.NoError(t, err)
	assert.Equal(t, namespaceadmission.DeletionOutcomeAlreadyTerminating, outcome)

	blocked := &datastore.Namespace{
		ID:                uuid.NewString(),
		Name:              "gitstore-system",
		ResourceVersion:   "1",
		Generation:        1,
		CreationTimestamp: now,
		UpdateTimestamp:   now,
	}
	blocked.UID = blocked.ID
	require.NoError(t, store.CreateNamespace(ctx, blocked))
	repoID := uuid.NewString()
	require.NoError(t, store.CreateRepository(ctx, &datastore.Repository{
		UID:               repoID,
		RepositoryID:      repoID,
		Namespace:         blocked.Name,
		Name:              "system",
		CreationTimestamp: now,
		UpdateTimestamp:   now,
	}))
	_, err = svc.DeleteNamespace(ctx, blocked)
	requireDeletionReasons(t, err,
		namespaceadmission.ReasonBootstrapNamespace,
		namespaceadmission.ReasonNamespaceNotEmpty,
	)

	families, err := registry.Gather()
	require.NoError(t, err)
	assert.Equal(t, float64(1), deletionCounterValue(t, families,
		"gitstore_namespace_deletion_outcomes_total", "outcome", "TERMINATION_STARTED"))
	assert.Equal(t, float64(1), deletionCounterValue(t, families,
		"gitstore_namespace_deletion_outcomes_total", "outcome", "ALREADY_TERMINATING"))
	assert.Equal(t, float64(1), deletionCounterValue(t, families,
		"gitstore_namespace_deletion_rejections_total", "reason", "BOOTSTRAP_NAMESPACE"))
	assert.Equal(t, float64(1), deletionCounterValue(t, families,
		"gitstore_namespace_deletion_rejections_total", "reason", "NAMESPACE_NOT_EMPTY"))

	entries := logs.All()
	require.Len(t, entries, 3)
	assert.Equal(t, "TERMINATION_STARTED", entries[0].ContextMap()["outcome"])
	assert.Equal(t, "ALREADY_TERMINATING", entries[1].ContextMap()["outcome"])
	assert.Equal(t, []any{"BOOTSTRAP_NAMESPACE", "NAMESPACE_NOT_EMPTY"}, entries[2].ContextMap()["reasons"])
	assert.EqualValues(t, 2, entries[2].ContextMap()["blocker_count"])
}

func requireDeletionReasons(t *testing.T, err error, want ...namespaceadmission.Reason) {
	t.Helper()
	var graphErr *gqlerror.Error
	require.ErrorAs(t, err, &graphErr)
	assert.Equal(t, namespaceadmission.CodeDeletionBlocked, graphErr.Extensions["code"])
	values, ok := graphErr.Extensions["reasons"].([]string)
	require.True(t, ok)
	expected := make([]string, len(want))
	for i, reason := range want {
		expected[i] = string(reason)
	}
	assert.Equal(t, expected, values)
}

func deletionCounterValue(
	t *testing.T,
	families []*dto.MetricFamily,
	name, labelName, labelValue string,
) float64 {
	t.Helper()
	for _, family := range families {
		if family.GetName() != name {
			continue
		}
		for _, metric := range family.Metric {
			for _, label := range metric.Label {
				if label.GetName() == labelName && label.GetValue() == labelValue {
					return metric.GetCounter().GetValue()
				}
			}
		}
	}
	t.Fatalf("counter %s{%s=%q} not found", name, labelName, labelValue)
	return 0
}
