// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package namespace_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/gitstore-dev/gitstore/api/internal/admission"
	"github.com/gitstore-dev/gitstore/api/internal/catalog"
	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	"github.com/gitstore-dev/gitstore/api/internal/datastore/memdb"
	namespaceadmission "github.com/gitstore-dev/gitstore/api/internal/namespace"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fixedIDs struct{ id string }

func (f fixedIDs) NewID() string { return f.id }

func TestApplyManifestPolicyRejections(t *testing.T) {
	ctx := context.Background()
	now := time.Now().UTC()

	t.Run("duplicate create", func(t *testing.T) {
		store := newAdmissionStore(t)
		seedNamespace(t, store, "acme", datastore.NamespaceTierUser, nil)
		_, _, err := namespaceadmission.ApplyManifest(ctx, store, fixedIDs{id: "new-id"}, namespaceResource("acme", "USER"), now, "main@sha1:a", "alice", admission.OperationCreate)
		require.ErrorIs(t, err, namespaceadmission.ErrNamespaceAlreadyExists)
	})

	t.Run("tier demotion", func(t *testing.T) {
		store := newAdmissionStore(t)
		seedNamespace(t, store, "acme", datastore.NamespaceTierOrganization, nil)
		_, _, err := namespaceadmission.ApplyManifest(ctx, store, fixedIDs{id: "new-id"}, namespaceResource("acme", "USER"), now, "main@sha1:a", "alice", admission.OperationUpdate)
		require.ErrorIs(t, err, namespaceadmission.ErrTierDemotion)
	})

	t.Run("terminating target", func(t *testing.T) {
		store := newAdmissionStore(t)
		deletedAt := now.Add(-time.Minute)
		seedNamespace(t, store, "acme", datastore.NamespaceTierUser, &deletedAt)
		_, _, err := namespaceadmission.ApplyManifest(ctx, store, fixedIDs{id: "new-id"}, namespaceResource("acme", "USER"), now, "main@sha1:a", "alice", admission.OperationUpdate)
		require.ErrorIs(t, err, namespaceadmission.ErrNamespaceTerminating)
	})
}

func TestApplyManifestDurableVersionCheckRemainsAuthoritative(t *testing.T) {
	ctx := context.Background()
	store := newAdmissionStore(t)
	seedNamespace(t, store, "acme", datastore.NamespaceTierUser, nil)
	racing := &conflictingNamespaceStore{Datastore: store}

	_, _, err := namespaceadmission.ApplyManifest(ctx, racing, fixedIDs{id: "new-id"}, namespaceResource("acme", "ORGANIZATION"), time.Now().UTC(), "main@sha1:b", "alice", admission.OperationUpdate)
	require.Error(t, err)
	assert.True(t, errors.Is(err, datastore.ErrConflict))

	current, getErr := store.GetNamespaceByName(ctx, "acme")
	require.NoError(t, getErr)
	assert.Equal(t, datastore.NamespaceTierUser, current.Tier)
}

func TestApplyManifestPersistsAdmissionAcceptedForAcceptedCreateAndUpdate(t *testing.T) {
	ctx := context.Background()
	store := newAdmissionStore(t)
	now := time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)

	created, createdNew, err := namespaceadmission.ApplyManifest(
		ctx,
		store,
		fixedIDs{id: "00000000-0000-0000-0000-000000000047"},
		namespaceResource("acme", "USER"),
		now,
		"main@sha1:create",
		"alice",
		admission.OperationCreate,
	)
	require.NoError(t, err)
	assert.True(t, createdNew)
	assertAdmissionAcceptedForGeneration(t, created, 1, "main@sha1:create")

	updated, createdNew, err := namespaceadmission.ApplyManifest(
		ctx,
		store,
		fixedIDs{id: "00000000-0000-0000-0000-000000000048"},
		namespaceResource("acme", "ORGANIZATION"),
		now.Add(time.Minute),
		"main@sha1:update",
		"alice",
		admission.OperationUpdate,
	)
	require.NoError(t, err)
	assert.False(t, createdNew)
	assertAdmissionAcceptedForGeneration(t, updated, 2, "main@sha1:update")

	persisted, err := store.GetNamespaceByName(ctx, "acme")
	require.NoError(t, err)
	assertAdmissionAcceptedForGeneration(t, persisted, 2, "main@sha1:update")
}

func TestApplyManifestPersistsAndVersionsCompleteAuthoredState(t *testing.T) {
	ctx := context.Background()
	store := newAdmissionStore(t)
	now := time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)
	visibility := "PRIVATE"
	resource := &catalog.NamespaceResource{
		APIVersion: "gitstore.dev/v1beta1",
		Kind:       "Namespace",
		Metadata: catalog.ObjectMeta{
			Name:        "authored-state",
			Labels:      map[string]string{"team": "catalog"},
			Annotations: map[string]string{"owner": "alice"},
		},
		Spec: catalog.NamespaceSpec{
			Title: "Authored state",
			Tier:  "USER",
			RepositoryDefaults: &catalog.NamespaceRepositoryDefaults{
				Visibility:    visibility,
				DefaultBranch: "trunk",
			},
			PushPolicyDefaults: &catalog.NamespacePushPolicyDefaults{
				MaxPackSizeBytes: 1024,
				MaxFileSizeBytes: 256,
			},
		},
	}

	created, createdNew, err := namespaceadmission.ApplyManifest(
		ctx,
		store,
		fixedIDs{id: "00000000-0000-0000-0000-000000000047"},
		resource,
		now,
		"main@sha1:create",
		"alice",
		admission.OperationCreate,
	)
	require.NoError(t, err)
	assert.True(t, createdNew)
	assert.Equal(t, resource.APIVersion, created.APIVersion)
	assert.Equal(t, resource.Kind, created.Kind)
	assert.Equal(t, resource.Metadata.Labels, created.Labels)
	assert.Equal(t, resource.Metadata.Annotations, created.Annotations)
	assert.JSONEq(t, mustJSON(t, resource.Spec), string(created.Spec))

	updatedResource := *resource
	updatedResource.Metadata = resource.Metadata
	updatedResource.Metadata.Labels = map[string]string{"team": "platform"}
	updatedResource.Metadata.Annotations = map[string]string{"owner": "bob"}
	updatedResource.Spec = resource.Spec
	updatedResource.Spec.RepositoryDefaults = &catalog.NamespaceRepositoryDefaults{
		Visibility:    "INTERNAL",
		DefaultBranch: "main",
	}
	updatedResource.Spec.PushPolicyDefaults = &catalog.NamespacePushPolicyDefaults{
		MaxPackSizeBytes: 2048,
		MaxFileSizeBytes: 512,
	}

	updated, createdNew, err := namespaceadmission.ApplyManifest(
		ctx,
		store,
		fixedIDs{id: "00000000-0000-0000-0000-000000000048"},
		&updatedResource,
		now.Add(time.Minute),
		"main@sha1:create",
		"bob",
		admission.OperationUpdate,
	)
	require.NoError(t, err)
	assert.False(t, createdNew)
	assert.Equal(t, int64(2), updated.Generation)
	assert.Equal(t, "2", updated.ResourceVersion)
	assert.Equal(t, updatedResource.Metadata.Labels, updated.Labels)
	assert.Equal(t, updatedResource.Metadata.Annotations, updated.Annotations)
	assert.JSONEq(t, mustJSON(t, updatedResource.Spec), string(updated.Spec))
}

func TestApplyManifestEmptyValuedLabelKeyChangeAdvancesGeneration(t *testing.T) {
	ctx := context.Background()
	store := newAdmissionStore(t)
	now := time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)
	resource := namespaceResource("presence-aware-labels", "USER")
	resource.Metadata.Labels = map[string]string{"old-key": ""}

	created, _, err := namespaceadmission.ApplyManifest(
		ctx,
		store,
		fixedIDs{id: "00000000-0000-0000-0000-000000000050"},
		resource,
		now,
		"main@sha1:first",
		"alice",
		admission.OperationCreate,
	)
	require.NoError(t, err)

	updatedResource := *resource
	updatedResource.Metadata = resource.Metadata
	updatedResource.Metadata.Labels = map[string]string{"new-key": ""}
	updated, _, err := namespaceadmission.ApplyManifest(
		ctx,
		store,
		fixedIDs{id: "unused"},
		&updatedResource,
		now.Add(time.Minute),
		"main@sha1:second",
		"bob",
		admission.OperationUpdate,
	)
	require.NoError(t, err)

	assert.Equal(t, created.Generation+1, updated.Generation)
	assert.Equal(t, map[string]string{"new-key": ""}, updated.Labels)
}

func TestApplyManifestBodyAndProvenanceVersionSemantics(t *testing.T) {
	ctx := context.Background()
	store := newAdmissionStore(t)
	now := time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)
	resource := namespaceResource("body-provenance", "USER")

	created, createdNew, err := namespaceadmission.ApplyManifestOrdered(
		ctx,
		store,
		fixedIDs{id: "00000000-0000-0000-0000-000000000049"},
		resource,
		now,
		"main@sha1:first",
		"alice",
		namespaceadmission.ApplyManifestOptions{
			Operation:    admission.OperationCreate,
			Body:         []byte("# First body\n"),
			SourcePath:   "namespaces/body-provenance.md",
			GitCommitSHA: "first",
			GitRef:       "refs/heads/main",
		},
	)
	require.NoError(t, err)
	assert.True(t, createdNew)
	assert.Equal(t, "# First body\n", created.Body)
	assert.Equal(t, "namespaces/body-provenance.md", created.SourcePath)
	assert.Equal(t, "first", created.GitCommitSHA)
	assert.Equal(t, "refs/heads/main", created.GitRef)

	provenanceOnly, createdNew, err := namespaceadmission.ApplyManifestOrdered(
		ctx,
		store,
		fixedIDs{id: "unused"},
		resource,
		now.Add(time.Minute),
		"main@sha1:second",
		"alice",
		namespaceadmission.ApplyManifestOptions{
			Operation:    admission.OperationUpdate,
			Body:         []byte("# First body\n"),
			SourcePath:   "namespaces/body-provenance.md",
			GitCommitSHA: "second",
			GitRef:       "refs/heads/main",
		},
	)
	require.NoError(t, err)
	assert.False(t, createdNew)
	assert.Equal(t, created.Generation, provenanceOnly.Generation)
	assert.Equal(t, "2", provenanceOnly.ResourceVersion)
	assert.Equal(t, "second", provenanceOnly.GitCommitSHA)

	bodyUpdated, createdNew, err := namespaceadmission.ApplyManifestOrdered(
		ctx,
		store,
		fixedIDs{id: "unused"},
		resource,
		now.Add(2*time.Minute),
		"main@sha1:second",
		"alice",
		namespaceadmission.ApplyManifestOptions{
			Operation:    admission.OperationUpdate,
			Body:         []byte("# Second body\n"),
			SourcePath:   "namespaces/body-provenance.md",
			GitCommitSHA: "second",
			GitRef:       "refs/heads/main",
		},
	)
	require.NoError(t, err)
	assert.False(t, createdNew)
	assert.Equal(t, created.Generation+1, bodyUpdated.Generation)
	assert.Equal(t, "3", bodyUpdated.ResourceVersion)
	assert.Equal(t, "# Second body\n", bodyUpdated.Body)
}

func TestApplyManifestRejectedUpdatesPreserveLastAcceptedStatusAndGeneration(t *testing.T) {
	ctx := context.Background()

	tests := []struct {
		name        string
		initialTier string
		rejected    *catalog.NamespaceResource
		prepare     func(t *testing.T, store datastore.Datastore, current *datastore.Namespace)
		wantErr     error
	}{
		{
			name:        "tier demotion",
			initialTier: "ORGANIZATION",
			rejected:    namespaceResource("acme", "USER"),
			wantErr:     namespaceadmission.ErrTierDemotion,
		},
		{
			name:        "terminating target",
			initialTier: "USER",
			rejected:    namespaceResource("acme", "ORGANIZATION"),
			prepare: func(t *testing.T, store datastore.Datastore, current *datastore.Namespace) {
				t.Helper()
				expectedResourceVersion := current.ResourceVersion
				deletedAt := time.Date(2026, 8, 29, 12, 30, 0, 0, time.UTC)
				current.DeletionTimestamp = &deletedAt
				datastore.AdvanceNamespaceSystemVersion(current)
				require.NoError(t, store.UpdateNamespace(ctx, current, expectedResourceVersion))
			},
			wantErr: namespaceadmission.ErrNamespaceTerminating,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := newAdmissionStore(t)
			_, _, err := namespaceadmission.ApplyManifest(
				ctx,
				store,
				fixedIDs{id: "00000000-0000-0000-0000-000000000047"},
				namespaceResource("acme", tt.initialTier),
				time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC),
				"main@sha1:accepted",
				"alice",
				admission.OperationCreate,
			)
			require.NoError(t, err)

			current, err := store.GetNamespaceByName(ctx, "acme")
			require.NoError(t, err)
			if tt.prepare != nil {
				tt.prepare(t, store, current)
				current, err = store.GetNamespaceByName(ctx, "acme")
				require.NoError(t, err)
			}
			acceptedGeneration := current.Generation
			acceptedResourceVersion := current.ResourceVersion
			acceptedStatus := append([]byte(nil), current.Status...)

			_, _, err = namespaceadmission.ApplyManifest(
				ctx,
				store,
				fixedIDs{id: "00000000-0000-0000-0000-000000000048"},
				tt.rejected,
				time.Date(2026, 8, 29, 13, 0, 0, 0, time.UTC),
				"main@sha1:rejected",
				"alice",
				admission.OperationUpdate,
			)
			require.ErrorIs(t, err, tt.wantErr)

			persisted, err := store.GetNamespaceByName(ctx, "acme")
			require.NoError(t, err)
			assert.Equal(t, acceptedGeneration, persisted.Generation)
			assert.Equal(t, acceptedResourceVersion, persisted.ResourceVersion)
			assert.JSONEq(t, string(acceptedStatus), string(persisted.Status))
			assertAdmissionAcceptedForGeneration(t, persisted, acceptedGeneration, "main@sha1:accepted")
		})
	}
}

func assertAdmissionAcceptedForGeneration(t *testing.T, namespace *datastore.Namespace, generation int64, revision string) {
	t.Helper()
	require.NotNil(t, namespace)
	assert.Equal(t, generation, namespace.Generation)

	var status catalog.NamespaceStatus
	require.NoError(t, json.Unmarshal(namespace.Status, &status))
	assert.Equal(t, generation, status.ObservedGeneration)
	assert.Equal(t, revision, status.LastAppliedRevision)
	require.Len(t, status.Conditions, 1)
	assert.Equal(t, catalog.ConditionAdmissionAccepted, status.Conditions[0].Type)
	assert.Equal(t, catalog.ConditionTrue, status.Conditions[0].Status)
	assert.Equal(t, generation, status.Conditions[0].ObservedGeneration)
}

type conflictingNamespaceStore struct {
	datastore.Datastore
}

func (s *conflictingNamespaceStore) UpdateNamespace(context.Context, *datastore.Namespace, string) error {
	return datastore.ErrConflict
}

func newAdmissionStore(t *testing.T) datastore.Datastore {
	t.Helper()
	store, err := memdb.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func seedNamespace(t *testing.T, store datastore.Datastore, name string, tier datastore.NamespaceTier, deletedAt *time.Time) {
	t.Helper()
	now := time.Now().UTC()
	require.NoError(t, store.CreateNamespace(context.Background(), &datastore.Namespace{
		UID:               "00000000-0000-0000-0000-000000000099",
		Name:              name,
		Tier:              tier,
		Title:             "Original",
		CreationTimestamp: now,
		CreationActor:     "test",
		UpdateTimestamp:   now,
		UpdateActor:       "test",
		DeletionTimestamp: deletedAt,
	}))
}

func namespaceResource(name, tier string) *catalog.NamespaceResource {
	return &catalog.NamespaceResource{
		APIVersion: "gitstore.dev/v1beta1",
		Kind:       "Namespace",
		Metadata:   catalog.ObjectMeta{Name: name},
		Spec:       catalog.NamespaceSpec{Title: "Updated", Tier: tier},
	}
}

func mustJSON(t *testing.T, value any) string {
	t.Helper()
	data, err := json.Marshal(value)
	require.NoError(t, err)
	return string(data)
}
