// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package datastore

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/gitstore-dev/gitstore/api/internal/catalog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func blockingCategoryOwnerReference(ownerUID, ownerName string) json.RawMessage {
	raw, _ := json.Marshal([]catalog.OwnerReference{{
		APIVersion: "catalog.gitstore.dev/v1beta1", Kind: "CategoryTaxonomy",
		Name: ownerName, UID: ownerUID, BlockOwnerDeletion: true,
	}})
	return raw
}

func terminatingCategoryFixture(uid, namespace, name, resourceVersion string, at time.Time) *CategoryTaxonomy {
	return &CategoryTaxonomy{
		UID: uid, Namespace: namespace, Name: name, ResourceVersion: resourceVersion,
		DeletionTimestamp: &at, Finalizers: []string{CategoryTaxonomyForegroundDeletionFinalizer},
	}
}

func TestDeletionFixturesProduceLifecycleCompatibleRecords(t *testing.T) {
	references := blockingCategoryOwnerReference("parent-uid", "parent")
	var decoded []catalog.OwnerReference
	require.NoError(t, json.Unmarshal(references, &decoded))
	require.Len(t, decoded, 1)
	assert.True(t, decoded[0].BlockOwnerDeletion)

	category := terminatingCategoryFixture("child-uid", "acme", "child", "7", time.Now().UTC())
	assert.NotNil(t, category.DeletionTimestamp)
	assert.Contains(t, category.Finalizers, CategoryTaxonomyForegroundDeletionFinalizer)
}
