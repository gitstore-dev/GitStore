// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package catalog_test

import (
	"context"
	"testing"

	"github.com/gitstore-dev/gitstore/api/internal/admission"
	admcatalog "github.com/gitstore-dev/gitstore/api/internal/admission/catalog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

func TestCollectionValidatingPolicy_Name(t *testing.T) {
	p := admcatalog.NewCollectionValidatingPolicy(zap.NewNop())
	assert.Equal(t, "CollectionValidatingPolicy", p.Name())
}

func TestCollectionValidatingPolicy_Collection_ReturnsAllowedNoConditions(t *testing.T) {
	p := admcatalog.NewCollectionValidatingPolicy(zap.NewNop())
	req := admission.AdmissionRequest{Kind: "Collection", Name: "my-collection", Namespace: "ns", Operation: admission.OperationCreate}
	d := p.Validate(context.Background(), req)
	allowed, ok := d.(admission.Allowed)
	require.True(t, ok, "CollectionValidatingPolicy must return Allowed for Collection kind")
	assert.Empty(t, allowed.Conditions, "stub must emit no conditions")
}

func TestCollectionValidatingPolicy_NonCollectionKind_ReturnsAllowed(t *testing.T) {
	p := admcatalog.NewCollectionValidatingPolicy(zap.NewNop())
	for _, kind := range []string{"Product", "ProductVariant", "CategoryTaxonomy"} {
		req := admission.AdmissionRequest{Kind: kind, Name: "x", Namespace: "ns"}
		d := p.Validate(context.Background(), req)
		_, ok := d.(admission.Allowed)
		assert.True(t, ok, "must return Allowed for kind %s", kind)
	}
}
