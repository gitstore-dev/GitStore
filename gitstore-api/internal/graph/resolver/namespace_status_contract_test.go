// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package resolver

import (
	"context"
	"testing"
	"time"

	"github.com/gitstore-dev/gitstore/api/internal/admission"
	"github.com/gitstore-dev/gitstore/api/internal/catalog"
	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	"github.com/gitstore-dev/gitstore/api/internal/datastore/memdb"
	"github.com/gitstore-dev/gitstore/api/internal/graph/model"
	namespaceadmission "github.com/gitstore-dev/gitstore/api/internal/namespace"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

type namespaceStatusIDGenerator struct{}

func (namespaceStatusIDGenerator) NewID() string {
	return "00000000-0000-0000-0000-000000000047"
}

func TestNamespaceQueryReadsPersistedAdmissionAndDerivedTerminationStatus(t *testing.T) {
	ctx := context.Background()
	store, err := memdb.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })

	_, _, err = namespaceadmission.ApplyManifest(
		ctx,
		store,
		namespaceStatusIDGenerator{},
		&catalog.NamespaceResource{
			APIVersion: "gitstore.dev/v1beta1",
			Kind:       "Namespace",
			Metadata:   catalog.ObjectMeta{Name: "status-query"},
			Spec:       catalog.NamespaceSpec{Title: "Status Query", Tier: "USER"},
		},
		time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC),
		"main@sha1:accepted",
		"alice",
		admission.OperationCreate,
	)
	require.NoError(t, err)

	persisted, err := store.GetNamespaceByName(ctx, "status-query")
	require.NoError(t, err)
	expectedResourceVersion := persisted.ResourceVersion
	deletionRequestedAt := time.Date(2026, 8, 29, 13, 0, 0, 0, time.UTC)
	persisted.DeletionTimestamp = &deletionRequestedAt
	persisted.Finalizers = []string{datastore.NamespaceForegroundDeletionFinalizer}
	datastore.AdvanceNamespaceSystemVersion(persisted)
	require.NoError(t, store.UpdateNamespace(ctx, persisted, expectedResourceVersion))

	root, err := NewResolver(ResolverDeps{Store: store, Logger: zap.NewNop()})
	require.NoError(t, err)
	name := "status-query"
	got, err := root.Query().Namespace(ctx, model.NamespaceBy{Name: &name})
	require.NoError(t, err)

	require.NotNil(t, got.Status)
	assert.Equal(t, int32(1), got.Status.ObservedGeneration)
	require.NotNil(t, got.Status.LastAppliedRevision)
	assert.Equal(t, "main@sha1:accepted", *got.Status.LastAppliedRevision)
	require.Len(t, got.Status.Conditions, 2)

	admissionCondition := namespaceModelConditionByType(t, got.Status.Conditions, catalog.ConditionAdmissionAccepted)
	assert.Equal(t, model.ConditionStatusTrue, admissionCondition.Status)
	require.NotNil(t, admissionCondition.ObservedGeneration)
	assert.Equal(t, int32(1), *admissionCondition.ObservedGeneration)

	terminatingCondition := namespaceModelConditionByType(t, got.Status.Conditions, catalog.ConditionTerminating)
	assert.Equal(t, model.ConditionStatusTrue, terminatingCondition.Status)
	assert.Equal(t, deletionRequestedAt, terminatingCondition.LastTransitionTime)
}
