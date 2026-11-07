// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package datastore

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/gitstore-dev/gitstore/api/internal/catalog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type deletionProbeStore struct {
	RepositoryStore
	page         *PageResult[Repository]
	err          error
	catalog      bool
	catalogCalls int
}

func (s *deletionProbeStore) ListRepositoriesByNamespace(_ context.Context, _ string, page PageParams) (*PageResult[Repository], error) {
	if page.First != 2 || page.After != "" || page.Last != 0 {
		panic("deletion probe must use one bounded first-two page")
	}
	return s.page, s.err
}

func (s *deletionProbeStore) HasCatalogResources(_ context.Context, _ string) (bool, error) {
	s.catalogCalls++
	return s.catalog, s.err
}

func TestNamespaceDeletionBlockedBoundedAndFailClosed(t *testing.T) {
	ns := &Namespace{UID: "namespace", Name: "shop"}
	system := &Repository{UID: "system", RepositoryID: "system", Namespace: "shop", Name: "gitstore-system"}
	for _, tc := range []struct {
		name    string
		items   []*Repository
		more    bool
		catalog bool
		blocked bool
	}{
		{name: "empty"},
		{name: "system only", items: []*Repository{system}},
		{name: "system catalog", items: []*Repository{system}, catalog: true, blocked: true},
		{name: "ordinary", items: []*Repository{{UID: "repo", Name: "catalog", Namespace: "shop"}}, blocked: true},
		{name: "two", items: []*Repository{system, system}, blocked: true},
		{name: "truncated", items: []*Repository{system}, more: true, blocked: true},
		{name: "unverified", items: []*Repository{{UID: "repo", Namespace: "shop", Name: "gitstore-system"}}, blocked: true},
		{name: "wrong namespace", items: []*Repository{{UID: "repo", RepositoryID: "repo", Namespace: "other", Name: "gitstore-system"}}, blocked: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := &deletionProbeStore{page: &PageResult[Repository]{Items: tc.items, HasNext: tc.more}, catalog: tc.catalog}
			blocked, err := NamespaceDeletionBlocked(context.Background(), store, ns)
			require.NoError(t, err)
			require.Equal(t, tc.blocked, blocked)
			require.LessOrEqual(t, store.catalogCalls, 1)
		})
	}
	store := &deletionProbeStore{err: ErrConflict}
	_, err := NamespaceDeletionBlocked(context.Background(), store, ns)
	require.ErrorIs(t, err, ErrConflict)
}

func TestInfrastructureStatusPatchesPreserveDeletionIntent(t *testing.T) {
	raw := json.RawMessage(`{"deletionIntent":{"uid":"intent"},"futureField":{"keep":true},"conditions":[{"type":"DeletionPending","status":"True","reason":"DeletionRequested","lastTransitionTime":"2026-01-01T00:00:00Z"}]}`)
	ns := &Namespace{ResourceVersion: "1", Status: raw}
	repository := &Repository{ResourceVersion: "1", Status: raw}
	require.NoError(t, ApplyNamespaceStatusPatch(ns, NamespaceStatusPatch{ResourceVersion: "1", Conditions: []catalog.Condition{}}))
	require.NoError(t, ApplyRepositoryStatusPatch(repository, RepositoryStatusPatch{ResourceVersion: "1", Conditions: []catalog.Condition{}}))
	for _, status := range []json.RawMessage{ns.Status, repository.Status} {
		var fields map[string]json.RawMessage
		require.NoError(t, json.Unmarshal(status, &fields))
		require.JSONEq(t, `{"uid":"intent"}`, string(fields["deletionIntent"]))
		require.JSONEq(t, `{"keep":true}`, string(fields["futureField"]))
		require.Contains(t, string(fields["conditions"]), `"DeletionPending"`)
		require.Contains(t, string(fields["conditions"]), `"True"`)
	}
}

func TestMergeInfrastructureStatusAllowsReceiptProgressOnly(t *testing.T) {
	old := json.RawMessage(`{"deletionIntent":{"uid":"u","repositoryID":"r","path":"p","ref":"main","actor":"a","expectedCommit":"old"},"conditions":[{"type":"DeletionPending","status":"True"}]}`)
	next := json.RawMessage(`{"deletionIntent":{"uid":"u","repositoryID":"r","path":"p","ref":"main","actor":"a","expectedCommit":"new","removalCommit":"removed"}}`)
	merged, err := MergeInfrastructureStatus(old, next)
	require.NoError(t, err)
	require.Contains(t, string(merged), `"removalCommit":"removed"`)
	require.Contains(t, string(merged), `"expectedCommit":"new"`)
	_, err = MergeInfrastructureStatus(merged, old)
	require.ErrorIs(t, err, ErrConflict)
	_, err = MergeInfrastructureStatus(old, json.RawMessage(`{"deletionIntent":{"uid":"replacement"}}`))
	require.ErrorIs(t, err, ErrConflict)
	preserved, err := PreserveDeletionStatus(old, next)
	require.NoError(t, err)
	require.NotContains(t, string(preserved), `"removalCommit"`)
}

func TestNormalizeNamespaceContract_CanonicalLegacyDefaults(t *testing.T) {
	ns := &Namespace{}

	NormalizeNamespaceContract(ns)

	assert.Equal(t, int64(1), ns.Generation)
	assert.Equal(t, "1", ns.ResourceVersion)
	var status struct {
		ObservedGeneration int64             `json:"observedGeneration"`
		Conditions         []json.RawMessage `json:"conditions"`
	}
	require.NoError(t, json.Unmarshal(ns.Status, &status))
	assert.Zero(t, status.ObservedGeneration)
	assert.NotNil(t, status.Conditions)
	assert.Empty(t, status.Conditions)
	assert.NotNil(t, ns.Finalizers)
}

func TestNormalizeNamespaceContract_PreservesValidState(t *testing.T) {
	deletionTimestamp := json.RawMessage(`{"observedGeneration":4,"conditions":[]}`)
	ns := &Namespace{
		Generation:      7,
		ResourceVersion: "12",
		Status:          deletionTimestamp,
		Finalizers:      []string{"gitstore.dev/foreground-deletion"},
	}

	NormalizeNamespaceContract(ns)

	assert.Equal(t, int64(7), ns.Generation)
	assert.Equal(t, "12", ns.ResourceVersion)
	assert.JSONEq(t, string(deletionTimestamp), string(ns.Status))
	assert.Equal(t, []string{"gitstore.dev/foreground-deletion"}, ns.Finalizers)
}

func TestAdvanceNamespaceSpecVersion_IncrementsGenerationAndResourceVersion(t *testing.T) {
	ns := &Namespace{}

	AdvanceNamespaceSpecVersion(ns)

	assert.Equal(t, int64(2), ns.Generation)
	assert.Equal(t, "2", ns.ResourceVersion)
}

func TestAdvanceNamespaceSystemVersion_PreservesGeneration(t *testing.T) {
	ns := &Namespace{Generation: 3, ResourceVersion: "9"}

	AdvanceNamespaceSystemVersion(ns)

	assert.Equal(t, int64(3), ns.Generation)
	assert.Equal(t, "10", ns.ResourceVersion)
}

func TestApplyNamespaceStatusPatch_PreservesAdmissionRevisionAndGeneration(t *testing.T) {
	ns := &Namespace{
		Generation:      3,
		ResourceVersion: "7",
		Status: json.RawMessage(`{
			"observedGeneration": 3,
			"lastAppliedRevision": "main@sha1:abc123",
			"conditions": [{"type":"AdmissionAccepted","status":"True","observedGeneration":3,"lastTransitionTime":"2026-01-01T00:00:00Z"}]
		}`),
	}
	conditions := []catalog.Condition{{
		Type:               catalog.ConditionReady,
		Status:             catalog.ConditionTrue,
		ObservedGeneration: 3,
	}}

	require.NoError(t, ApplyNamespaceStatusPatch(ns, NamespaceStatusPatch{
		ResourceVersion: "7",
		Conditions:      conditions,
	}))

	assert.Equal(t, int64(3), ns.Generation)
	assert.Equal(t, "8", ns.ResourceVersion)
	var status catalog.NamespaceStatus
	require.NoError(t, json.Unmarshal(ns.Status, &status))
	assert.Equal(t, int64(3), status.ObservedGeneration)
	assert.Equal(t, "main@sha1:abc123", status.LastAppliedRevision)
	assert.Equal(t, conditions, status.Conditions)
}
