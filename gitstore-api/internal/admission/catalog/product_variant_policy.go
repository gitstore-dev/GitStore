// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

// Package catalog provides ValidatingAdmissionPolicy implementations for
// catalog resource kinds.
package catalog

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/gitstore-dev/gitstore/api/internal/admission"
	"github.com/gitstore-dev/gitstore/api/internal/catalog"
	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	"github.com/google/cel-go/cel"
	"go.uber.org/zap"
)

// ProductVariantValidatingPolicy implements admission.ValidatingAdmissionPolicy
// for Kind == "ProductVariant". It emits ProductResolved, OptionsAccepted, and
// PricingAccepted conditions. It never returns Denied; all check results surface
// as False conditions.
type ProductVariantValidatingPolicy struct {
	store  datastore.Datastore
	celEnv *cel.Env
	log    *zap.Logger
}

// NewProductVariantValidatingPolicy constructs the policy.
// celEnv may be nil; CEL validation is skipped when nil.
// store may be nil; ProductResolved will always be false when nil.
func NewProductVariantValidatingPolicy(store datastore.Datastore, celEnv *cel.Env, log *zap.Logger) *ProductVariantValidatingPolicy {
	return &ProductVariantValidatingPolicy{store: store, celEnv: celEnv, log: log}
}

func (p *ProductVariantValidatingPolicy) Name() string { return "ProductVariantValidatingPolicy" }

// Validate checks the ProductVariant resource and returns Allowed with conditions.
func (p *ProductVariantValidatingPolicy) Validate(ctx context.Context, req admission.AdmissionRequest) admission.AdmissionDecision {
	if req.Kind != "ProductVariant" {
		return admission.DecisionAllow()
	}
	resource, ok := req.Object.(*catalog.ProductVariantResource)
	if !ok || resource == nil {
		return admission.DecisionAllow()
	}

	namespace := resource.Metadata.Namespace
	if namespace == "" {
		namespace = req.Namespace
	}

	var conditions []admission.AdmissionCondition

	// ProductResolved + OptionsAccepted — only when productRef is set
	if resource.Spec.ProductRef != nil && resource.Spec.ProductRef.Name != "" {
		productRefName := resource.Spec.ProductRef.Name
		productResolved := false
		optionsAccepted := true
		optionsMsg := ""

		if p.store != nil {
			if parent, err := p.store.GetProductByName(ctx, namespace, productRefName); err == nil && parent != nil {
				productResolved = true
				if len(resource.Spec.SelectedOptions) > 0 {
					optionsAccepted, optionsMsg = ValidateSelectedOptions(resource.Spec.SelectedOptions, parent.Spec)
					if !optionsAccepted && p.log != nil {
						p.log.Warn("admission: product_variant option incompatibility",
							zap.String("name", resource.Metadata.Name),
							zap.String("namespace", namespace),
							zap.String("product_ref", productRefName),
							zap.String("reason", optionsMsg))
					}
				}
			} else if p.log != nil {
				p.log.Info("admission: product_variant productRef deferred — product not yet in datastore",
					zap.String("name", resource.Metadata.Name),
					zap.String("namespace", namespace),
					zap.String("product_ref", productRefName))
			}
		}

		conditions = append(conditions, admission.AdmissionCondition{
			Type:   string(catalog.ConditionProductResolved),
			Status: productResolved,
		})

		optCond := admission.AdmissionCondition{
			Type:   string(catalog.ConditionOptionsAccepted),
			Status: optionsAccepted,
		}
		if !optionsAccepted && optionsMsg != "" {
			optCond.Reason = "IncompatibleOptions"
			optCond.Message = optionsMsg
		}
		conditions = append(conditions, optCond)
	}

	// PricingAccepted — CEL syntax validation
	pricingAccepted, pricingMsg := ValidateCELExpressions(p.celEnv, resource.Spec)
	if !pricingAccepted && p.log != nil {
		p.log.Warn("admission: product_variant CEL syntax error",
			zap.String("name", resource.Metadata.Name),
			zap.String("namespace", namespace),
			zap.String("reason", pricingMsg))
	}
	pricingCond := admission.AdmissionCondition{
		Type:   string(catalog.ConditionPricingAccepted),
		Status: pricingAccepted,
	}
	if !pricingAccepted && pricingMsg != "" {
		pricingCond.Reason = "InvalidCELExpression"
		pricingCond.Message = pricingMsg
	}
	conditions = append(conditions, pricingCond)

	return admission.DecisionAllow(conditions...)
}

// ValidateSelectedOptions checks that every selected option name exists in the
// parent product and, when the parent option declares allowed values, that the
// selected value is one of them.
// parentSpec is the raw JSON of the parent product's spec field from the datastore.
// Returns (true, "") on success, or (false, descriptive message) on first mismatch.
// If parentSpec cannot be unmarshalled, returns (true, "") — skip rather than false-reject.
func ValidateSelectedOptions(selected []catalog.SelectedOptionDefinition, parentSpec []byte) (ok bool, reason string) {
	var spec struct {
		Options []struct {
			Name   string   `json:"name"`
			Values []string `json:"values"`
		} `json:"options"`
	}
	if err := json.Unmarshal(parentSpec, &spec); err != nil {
		return true, ""
	}
	declared := make(map[string]map[string]struct{}, len(spec.Options))
	for _, o := range spec.Options {
		allowedValues := make(map[string]struct{}, len(o.Values))
		for _, v := range o.Values {
			allowedValues[v] = struct{}{}
		}
		declared[o.Name] = allowedValues
	}
	for _, so := range selected {
		allowedValues, exists := declared[so.Name]
		if !exists {
			return false, fmt.Sprintf("selectedOptions: name %q not found in parent product options", so.Name)
		}
		if len(allowedValues) > 0 {
			if _, ok := allowedValues[so.Value]; !ok {
				return false, fmt.Sprintf("selectedOptions: value %q for option %q not found in parent product option values", so.Value, so.Name)
			}
		}
	}
	return true, ""
}

// ValidateCELExpressions parses each CEL expression for syntax only (no evaluation).
// env may be nil (CEL unavailable); in that case all expressions are considered valid.
// Returns (true, "") if all are valid, or (false, message) on the first syntax error.
// message identifies the expression field path and the parse error.
func ValidateCELExpressions(env *cel.Env, spec catalog.ProductVariantSpec) (ok bool, reason string) {
	if env == nil || spec.Pricing == nil || spec.Pricing.PriceSet == nil {
		return true, ""
	}
	for i, pt := range spec.Pricing.PriceSet.Prices {
		if pt.Eligibility == nil {
			continue
		}
		for j, c := range pt.Eligibility.Constraints {
			if _, iss := env.Parse(c.Expression); iss != nil && iss.Err() != nil {
				return false, fmt.Sprintf("pricing.priceSet.prices[%d].eligibility.constraints[%d]: invalid CEL expression %q: %s",
					i, j, c.Expression, iss.Err())
			}
		}
	}
	return true, ""
}
