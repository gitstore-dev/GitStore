// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package datastore

import (
	"encoding/json"
	"testing"

	"github.com/gitstore-dev/gitstore/api/internal/catalog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

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
