// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package catalog_test

import (
	"context"
	"testing"
	"time"

	"github.com/gitstore-dev/gitstore/api/internal/admission"
	admcatalog "github.com/gitstore-dev/gitstore/api/internal/admission/catalog"
	catalogapi "github.com/gitstore-dev/gitstore/api/internal/catalog"
	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	"github.com/gitstore-dev/gitstore/api/internal/datastore/memdb"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

func TestProductValidatingPolicy_Name(t *testing.T) {
	p := admcatalog.NewProductValidatingPolicy(zap.NewNop())
	assert.Equal(t, "ProductValidatingPolicy", p.Name())
}

func TestProductValidatingPolicy_RejectsTerminatingCategoryAndAllowsMissingTarget(t *testing.T) {
	store, err := memdb.New()
	require.NoError(t, err)
	category := &datastore.CategoryTaxonomy{
		UID: "00000000-0000-0000-0000-000000000050", Namespace: "ns", Name: "retiring",
		ResourceVersion: "1",
	}
	require.NoError(t, store.CreateCategoryTaxonomy(context.Background(), category))
	lifecycle := store.(datastore.CategoryTaxonomyDeletionStore)
	_, err = lifecycle.MarkCategoryTaxonomyDeletion(context.Background(), "ns", "retiring", "1", time.Now())
	require.NoError(t, err)

	policy := admcatalog.NewProductValidatingPolicy(zap.NewNop(), store)
	decision := policy.Validate(context.Background(), admission.AdmissionRequest{
		Kind: "Product", Namespace: "ns", Object: &catalogapi.ProductResource{
			Metadata: catalogapi.ObjectMeta{Namespace: "ns", Name: "new-product"},
			Spec:     catalogapi.ProductSpec{CategoryRef: &catalogapi.ObjectReference{Name: "retiring"}},
		},
	})
	denied, ok := decision.(admission.Denied)
	require.True(t, ok)
	assert.Equal(t, "CategoryTerminating", denied.Reason)

	decision = policy.Validate(context.Background(), admission.AdmissionRequest{
		Kind: "Product", Namespace: "ns", Object: &catalogapi.ProductResource{
			Metadata: catalogapi.ObjectMeta{Namespace: "ns", Name: "unresolved-product"},
			Spec:     catalogapi.ProductSpec{CategoryRef: &catalogapi.ObjectReference{Name: "not-yet-admitted"}},
		},
	})
	_, allowed := decision.(admission.Allowed)
	assert.True(t, allowed)
}

func TestProductValidatingPolicy_Product_ReturnsAllowedNoConditions(t *testing.T) {
	p := admcatalog.NewProductValidatingPolicy(zap.NewNop())
	req := admission.AdmissionRequest{Kind: "Product", Name: "my-product", Namespace: "ns", Operation: admission.OperationCreate}
	d := p.Validate(context.Background(), req)
	allowed, ok := d.(admission.Allowed)
	require.True(t, ok, "ProductValidatingPolicy must return Allowed for Product kind")
	assert.Empty(t, allowed.Conditions, "stub must emit no conditions")
}

func TestProductValidatingPolicy_NonProductKind_ReturnsAllowed(t *testing.T) {
	p := admcatalog.NewProductValidatingPolicy(zap.NewNop())
	for _, kind := range []string{"Collection", "ProductVariant", "CategoryTaxonomy"} {
		req := admission.AdmissionRequest{Kind: kind, Name: "x", Namespace: "ns"}
		d := p.Validate(context.Background(), req)
		_, ok := d.(admission.Allowed)
		assert.True(t, ok, "must return Allowed for kind %s", kind)
	}
}
