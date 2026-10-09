// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package contract_test

import (
	"reflect"
	"testing"

	"github.com/gitstore-dev/gitstore/api/internal/catalog"
	"github.com/gitstore-dev/gitstore/api/internal/graph/generated"
	"github.com/gitstore-dev/gitstore/api/internal/graph/resolver"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

// Retirement is deliberately only desired catalog state in feature 055.  This
// contract protects that boundary until a later feature introduces release
// preparation and public-serving resources: retired intent must round-trip,
// variants must not grow an independently authored lifecycle, and no release
// or publication API may be implied by the Product schema.
func TestProductRetirementCompatibilityContract(t *testing.T) {
	retired := catalog.ProductResource{
		APIVersion: "catalog.gitstore.dev/v1beta1",
		Kind:       "Product",
		Metadata:   catalog.ObjectMeta{Name: "retired-widget", Namespace: "catalog"},
		Spec: catalog.ProductSpec{
			Lifecycle: catalog.ProductLifecycleSpec{State: "RETIRED"},
		},
	}

	encoded, err := yaml.Marshal(retired)
	require.NoError(t, err)
	var decoded catalog.ProductResource
	require.NoError(t, yaml.Unmarshal(encoded, &decoded))
	assert.Equal(t, "RETIRED", decoded.Spec.Lifecycle.State)

	_, variantHasLifecycle := reflect.TypeOf(catalog.ProductVariantSpec{}).FieldByName("Lifecycle")
	assert.False(t, variantHasLifecycle, "ProductVariants inherit parent retirement eligibility; they do not author lifecycle state")

	schema := generated.NewExecutableSchema(generated.Config{Resolvers: &resolver.Resolver{}}).Schema()
	for _, typeName := range []string{"Release", "ReleaseCandidate", "Publication", "PublicProduct"} {
		assert.Nil(t, schema.Types[typeName], "%s is outside the Product retirement feature", typeName)
	}
	for _, fieldName := range []string{"prepareRelease", "publishRelease", "suppressPublication"} {
		assert.Nil(t, schema.Mutation.Fields.ForName(fieldName), "%s is outside the Product retirement feature", fieldName)
	}
}
