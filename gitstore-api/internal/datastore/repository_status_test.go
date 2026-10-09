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

func TestApplyRepositoryStatusPatch_AdvancesOnlySystemVersion(t *testing.T) {
	repository := &Repository{Generation: 3, ResourceVersion: "7", Status: json.RawMessage(`{"observedGeneration":1,"conditions":[]}`)}
	observed := int64(3)
	revision := "main@sha:123"
	resolved := &catalog.ResolvedRepositoryDefinition{StoragePath: "/var/lib/gitstore/acme/catalog.git", StorageClass: "ssd"}
	require.NoError(t, ApplyRepositoryStatusPatch(repository, RepositoryStatusPatch{
		ResourceVersion:     "7",
		ObservedGeneration:  &observed,
		LastAppliedRevision: &revision,
		Conditions:          []catalog.Condition{{Type: catalog.ConditionReady, Status: catalog.ConditionTrue, ObservedGeneration: observed}},
		Resolved:            resolved,
	}))

	assert.Equal(t, int64(3), repository.Generation)
	assert.Equal(t, "8", repository.ResourceVersion)
	assert.JSONEq(t, `{"observedGeneration":3,"lastAppliedRevision":"main@sha:123","conditions":[{"type":"Ready","status":"True","observedGeneration":3,"lastTransitionTime":"0001-01-01T00:00:00Z"}],"resolved":{"storagePath":"/var/lib/gitstore/acme/catalog.git","storageClass":"ssd"}}`, string(repository.Status))
}

func TestApplyRepositoryStatusPatch_RejectsStaleResourceVersion(t *testing.T) {
	repository := &Repository{Generation: 1, ResourceVersion: "2", Status: json.RawMessage(`{"observedGeneration":0,"conditions":[]}`)}
	assert.ErrorIs(t, ApplyRepositoryStatusPatch(repository, RepositoryStatusPatch{ResourceVersion: "1"}), ErrConflict)
}
