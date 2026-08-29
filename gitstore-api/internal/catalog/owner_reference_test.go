// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package catalog

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestOwnerReferenceBlockOwnerDeletionDefaultsFalse(t *testing.T) {
	var legacy OwnerReference
	require.NoError(t, json.Unmarshal([]byte(`{"apiVersion":"catalog.gitstore.dev/v1beta1","kind":"CategoryTaxonomy","name":"parent","uid":"parent-uid"}`), &legacy))
	assert.False(t, legacy.BlockOwnerDeletion)

	encoded, err := json.Marshal(OwnerReference{
		APIVersion:         "catalog.gitstore.dev/v1beta1",
		Kind:               "CategoryTaxonomy",
		Name:               "parent",
		UID:                "parent-uid",
		BlockOwnerDeletion: true,
	})
	require.NoError(t, err)
	assert.JSONEq(t, `{"apiVersion":"catalog.gitstore.dev/v1beta1","kind":"CategoryTaxonomy","name":"parent","uid":"parent-uid","blockOwnerDeletion":true}`, string(encoded))
}
