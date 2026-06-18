// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package catalog_test

import (
	"context"
	"testing"

	"github.com/gitstore-dev/gitstore/api/internal/admission"
	admcatalog "github.com/gitstore-dev/gitstore/api/internal/admission/catalog"
	"github.com/gitstore-dev/gitstore/api/internal/catalog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

// --- ValidateSelectedOptions ---

func TestValidateSelectedOptions_AcceptsKnownNamesAndValues(t *testing.T) {
	parentSpec := []byte(`{"options":[{"name":"size","values":["S","M"]},{"name":"material","values":[]}]}`)
	selected := []catalog.SelectedOptionDefinition{
		{Name: "size", Value: "M"},
		{Name: "material", Value: "cotton"},
	}
	ok, msg := admcatalog.ValidateSelectedOptions(selected, parentSpec)
	assert.True(t, ok)
	assert.Empty(t, msg)
}

func TestValidateSelectedOptions_RejectsUnknownOptionName(t *testing.T) {
	parentSpec := []byte(`{"options":[{"name":"size","values":["S","M"]}]}`)
	selected := []catalog.SelectedOptionDefinition{{Name: "color", Value: "red"}}
	ok, msg := admcatalog.ValidateSelectedOptions(selected, parentSpec)
	assert.False(t, ok)
	assert.Contains(t, msg, `name "color"`)
}

func TestValidateSelectedOptions_RejectsUnknownOptionValue(t *testing.T) {
	parentSpec := []byte(`{"options":[{"name":"size","values":["S","M"]}]}`)
	selected := []catalog.SelectedOptionDefinition{{Name: "size", Value: "XL"}}
	ok, msg := admcatalog.ValidateSelectedOptions(selected, parentSpec)
	assert.False(t, ok)
	assert.Contains(t, msg, `value "XL"`)
	assert.Contains(t, msg, `option "size"`)
}

func TestValidateSelectedOptions_EmptyValuesListAcceptsAny(t *testing.T) {
	parentSpec := []byte(`{"options":[{"name":"material","values":[]}]}`)
	selected := []catalog.SelectedOptionDefinition{{Name: "material", Value: "anything"}}
	ok, msg := admcatalog.ValidateSelectedOptions(selected, parentSpec)
	assert.True(t, ok)
	assert.Empty(t, msg)
}

func TestValidateSelectedOptions_UnparseableParentSpec_Skips(t *testing.T) {
	parentSpec := []byte(`not valid json`)
	selected := []catalog.SelectedOptionDefinition{{Name: "size", Value: "M"}}
	ok, msg := admcatalog.ValidateSelectedOptions(selected, parentSpec)
	assert.True(t, ok, "unparseable parent spec must not false-reject")
	assert.Empty(t, msg)
}

// --- ValidateCELExpressions ---

func TestValidateCELExpressions_NilEnv_Skips(t *testing.T) {
	spec := catalog.ProductVariantSpec{
		Pricing: &catalog.PricingDefinition{
			PriceSet: &catalog.PriceSet{
				Prices: []catalog.PriceTemplate{{
					Eligibility: &catalog.EligibilityDefinition{
						Constraints: []catalog.PriceRuleConstraint{{Expression: "this is not valid {"}},
					},
				}},
			},
		},
	}
	ok, msg := admcatalog.ValidateCELExpressions(nil, spec)
	assert.True(t, ok, "nil env must skip CEL validation")
	assert.Empty(t, msg)
}

func TestValidateCELExpressions_NilPricing_Skips(t *testing.T) {
	ok, msg := admcatalog.ValidateCELExpressions(nil, catalog.ProductVariantSpec{})
	assert.True(t, ok)
	assert.Empty(t, msg)
}

func TestValidateCELExpressions_ValidExpression(t *testing.T) {
	env := newCELEnv(t)
	spec := catalog.ProductVariantSpec{
		Pricing: &catalog.PricingDefinition{
			PriceSet: &catalog.PriceSet{
				Prices: []catalog.PriceTemplate{{
					Eligibility: &catalog.EligibilityDefinition{
						Constraints: []catalog.PriceRuleConstraint{{Name: "valid", Expression: "true"}},
					},
				}},
			},
		},
	}
	ok, msg := admcatalog.ValidateCELExpressions(env, spec)
	assert.True(t, ok)
	assert.Empty(t, msg)
}

func TestValidateCELExpressions_InvalidExpression_ReturnsFieldPath(t *testing.T) {
	env := newCELEnv(t)
	spec := catalog.ProductVariantSpec{
		Pricing: &catalog.PricingDefinition{
			PriceSet: &catalog.PriceSet{
				Prices: []catalog.PriceTemplate{{
					Eligibility: &catalog.EligibilityDefinition{
						Constraints: []catalog.PriceRuleConstraint{{Name: "bad", Expression: "this is not valid CEL {"}},
					},
				}},
			},
		},
	}
	ok, msg := admcatalog.ValidateCELExpressions(env, spec)
	assert.False(t, ok)
	assert.Contains(t, msg, "pricing.priceSet.prices[0].eligibility.constraints[0]")
}

func TestValidateCELExpressions_NilEligibility_Skips(t *testing.T) {
	env := newCELEnv(t)
	spec := catalog.ProductVariantSpec{
		Pricing: &catalog.PricingDefinition{
			PriceSet: &catalog.PriceSet{
				Prices: []catalog.PriceTemplate{{Eligibility: nil}},
			},
		},
	}
	ok, msg := admcatalog.ValidateCELExpressions(env, spec)
	assert.True(t, ok)
	assert.Empty(t, msg)
}

// --- ProductVariantValidatingPolicy.Validate ---

func TestProductVariantValidatingPolicy_Name(t *testing.T) {
	p := admcatalog.NewProductVariantValidatingPolicy(nil, nil, zap.NewNop())
	assert.Equal(t, "ProductVariantValidatingPolicy", p.Name())
}

func TestProductVariantValidatingPolicy_WrongKind_ReturnsAllowed(t *testing.T) {
	p := admcatalog.NewProductVariantValidatingPolicy(nil, nil, zap.NewNop())
	req := admission.AdmissionRequest{Kind: "Product", Name: "x", Namespace: "ns"}
	d := p.Validate(context.Background(), req)
	_, ok := d.(admission.Allowed)
	assert.True(t, ok)
}

func TestProductVariantValidatingPolicy_NilProductRef_AllowedNoPRCondition(t *testing.T) {
	p := admcatalog.NewProductVariantValidatingPolicy(nil, nil, zap.NewNop())
	variant := &catalog.ProductVariantResource{
		Kind:     "ProductVariant",
		Metadata: catalog.ObjectMeta{Name: "v1", Namespace: "ns"},
		Spec:     catalog.ProductVariantSpec{SKU: "SKU-001"},
	}
	req := admission.AdmissionRequest{
		Kind:      "ProductVariant",
		Name:      "v1",
		Namespace: "ns",
		Object:    variant,
		Operation: admission.OperationCreate,
		Trigger:   admission.TriggerGitPush,
	}
	d := p.Validate(context.Background(), req)
	allowed, ok := d.(admission.Allowed)
	require.True(t, ok)
	// No ProductResolved condition when there is no productRef
	for _, c := range allowed.Conditions {
		assert.NotEqual(t, "ProductResolved", c.Type)
	}
}
