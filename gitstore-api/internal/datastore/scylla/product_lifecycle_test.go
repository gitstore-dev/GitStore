// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package scylla

import (
	"testing"

	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	"github.com/stretchr/testify/assert"
)

var _ datastore.ProductLifecycleStore = (*scyllaDatastore)(nil)

func TestProductLifecycleStoreContractIsImplemented(t *testing.T) {
	assert.NotEmpty(t, ownerReferenceDependentsTable)
}

func TestProductVariantLifecycleUsesOwnerReferenceProjection(t *testing.T) {
	assert.Equal(t, "owner_reference_dependents", ownerReferenceDependentsTable)
}
