// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package model

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestOwnerReferenceBlockOwnerDeletionLegacyDefault(t *testing.T) {
	var legacy OwnerReference
	require.NoError(t, json.Unmarshal([]byte(`{"apiVersion":"v1","kind":"CategoryTaxonomy","name":"parent","uid":"parent-uid"}`), &legacy))
	assert.False(t, legacy.BlockOwnerDeletion)
}
