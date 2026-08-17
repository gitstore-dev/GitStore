// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package datastore

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNormalizeRepositoryContract_CanonicalLegacyDefaults(t *testing.T) {
	repo := &Repository{}

	NormalizeRepositoryContract(repo)

	assert.Equal(t, int64(1), repo.Generation)
	assert.Equal(t, "1", repo.ResourceVersion)
	var status struct {
		ObservedGeneration int64             `json:"observedGeneration"`
		Conditions         []json.RawMessage `json:"conditions"`
	}
	require.NoError(t, json.Unmarshal(repo.Status, &status))
	assert.Zero(t, status.ObservedGeneration)
	assert.NotNil(t, status.Conditions)
	assert.Empty(t, status.Conditions)
}

func TestNormalizeRepositoryContract_NormalizesMalformedResourceVersions(t *testing.T) {
	for _, resourceVersion := range []string{"", "0", "-1", "not-a-number"} {
		t.Run(resourceVersion, func(t *testing.T) {
			repo := &Repository{Generation: -1, ResourceVersion: resourceVersion}
			NormalizeRepositoryContract(repo)
			assert.Equal(t, int64(1), repo.Generation)
			assert.Equal(t, "1", repo.ResourceVersion)
		})
	}
}

func TestNormalizeRepositoryContract_PreservesValidState(t *testing.T) {
	status := json.RawMessage(`{"observedGeneration":4,"conditions":[]}`)
	repo := &Repository{Generation: 7, ResourceVersion: "12", Status: status}

	NormalizeRepositoryContract(repo)

	assert.Equal(t, int64(7), repo.Generation)
	assert.Equal(t, "12", repo.ResourceVersion)
	assert.JSONEq(t, string(status), string(repo.Status))
}

func TestAdvanceRepositorySpecVersion_IncrementsGenerationAndResourceVersion(t *testing.T) {
	repo := &Repository{}

	AdvanceRepositorySpecVersion(repo)

	assert.Equal(t, int64(2), repo.Generation)
	assert.Equal(t, "2", repo.ResourceVersion)
}

func TestAdvanceRepositorySystemVersion_PreservesGeneration(t *testing.T) {
	repo := &Repository{Generation: 3, ResourceVersion: "9"}

	AdvanceRepositorySystemVersion(repo)

	assert.Equal(t, int64(3), repo.Generation)
	assert.Equal(t, "10", repo.ResourceVersion)
}
