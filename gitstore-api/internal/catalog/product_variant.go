// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package catalog

import "time"

// ProductVariantResource is the top-level envelope parsed from a ProductVariant
// Markdown file's YAML frontmatter. Only author-writable fields are present here.
type ProductVariantResource struct {
	APIVersion string             `yaml:"apiVersion" validate:"required,eq=catalog.gitstore.dev/v1beta1"`
	Kind       string             `yaml:"kind"       validate:"required,eq=ProductVariant"`
	Metadata   ObjectMeta         `yaml:"metadata"   validate:"required"`
	Spec       ProductVariantSpec `yaml:"spec"`
}

// ProductVariantSpec is the author-controlled declarative specification for a variant.
type ProductVariantSpec struct {
	Title           string                     `yaml:"title"           validate:"required"`
	SKU             string                     `yaml:"sku"             validate:"required"`
	ProductRef      *ObjectReference           `yaml:"productRef"      validate:"required"`
	Inventory       *InventoryDefinition       `yaml:"inventory"`
	Pricing         *PricingDefinition         `yaml:"pricing"`
	SelectedOptions []SelectedOptionDefinition `yaml:"selectedOptions" validate:"omitempty,dive"`
	Media           []MediaDefinition          `yaml:"media"           validate:"omitempty,dive"`
}

// InventoryDefinition controls stock management behaviour for a variant.
type InventoryDefinition struct {
	Managed           bool              `yaml:"managed"`
	Policy            string            `yaml:"policy"            validate:"omitempty,oneof=deny backorder"`
	StockLocationRefs []ObjectReference `yaml:"stockLocationRefs" validate:"omitempty,dive"`
}

// PricingDefinition holds the variant's named price set.
type PricingDefinition struct {
	PriceSet *PriceSet `yaml:"priceSet"`
}

// PriceSet is a named collection of price templates.
type PriceSet struct {
	Name   string          `yaml:"name"   validate:"required"`
	Prices []PriceTemplate `yaml:"prices" validate:"omitempty,dive"`
}

// PriceTemplate defines a single price entry within a price set.
type PriceTemplate struct {
	Name           string                 `yaml:"name"`
	ValidFromTime  *time.Time             `yaml:"validFromTime"`
	ValidUntilTime *time.Time             `yaml:"validUntilTime"`
	Quantity       *QuantityDefinition    `yaml:"quantity"`
	CurrencyCode   string                 `yaml:"currencyCode"  validate:"required"`
	Amount         string                 `yaml:"amount"        validate:"required"`
	Strategy       *StrategyDefinition    `yaml:"strategy"      validate:"required"`
	Priority       int32                  `yaml:"priority"`
	Eligibility    *EligibilityDefinition `yaml:"eligibility"`
}

// QuantityDefinition constrains the minimum and maximum order quantity for a price.
// Max nil means unbounded.
type QuantityDefinition struct {
	Min int32  `yaml:"min"`
	Max *int32 `yaml:"max"`
}

// StrategyDefinition identifies the pricing strategy to apply.
type StrategyDefinition struct {
	Type string `yaml:"type" validate:"required"`
}

// EligibilityDefinition specifies the logical operator and CEL constraints for a price rule.
type EligibilityDefinition struct {
	Operator    string                `yaml:"operator"    validate:"required,oneof=All Any"`
	Constraints []PriceRuleConstraint `yaml:"constraints" validate:"omitempty,dive"`
}

// PriceRuleConstraint holds a named CEL expression evaluated at runtime against
// the cart context. Syntax is validated at admission time by the CEL parser.
type PriceRuleConstraint struct {
	Name       string `yaml:"name"`
	Expression string `yaml:"expression" validate:"required"`
}

// SelectedOptionDefinition is a single option choice (name + value) that
// identifies this variant within the parent product's option matrix.
type SelectedOptionDefinition struct {
	Name  string `yaml:"name"  validate:"required"`
	Value string `yaml:"value" validate:"required"`
}

// ProductVariantStatus is the system-written state for a variant. Never stored in git.
type ProductVariantStatus struct {
	ObservedGeneration  int64                             `json:"observedGeneration"`
	LastAppliedRevision string                            `json:"lastAppliedRevision"`
	Conditions          []Condition                       `json:"conditions"`
	Resolved            *ResolvedProductVariantDefinition `json:"resolved,omitempty"`
}

// ResolvedProductVariantDefinition holds system-computed aggregates for a variant.
type ResolvedProductVariantDefinition struct {
	Product             *ResolvedProductRef          `json:"product,omitempty"`
	SelectedOptionsHash string                       `json:"selectedOptionsHash,omitempty"`
	PriceSet            *ResolvedPriceSetDefinition  `json:"priceSet,omitempty"`
	Inventory           *ResolvedInventoryDefinition `json:"inventory,omitempty"`
	Media               []ResolvedFileDefinition     `json:"media,omitempty"`
}

// ResolvedProductRef is the resolved parent product identity.
type ResolvedProductRef struct {
	Name string `json:"name"`
	UID  string `json:"uid"`
}

// ResolvedPriceSetDefinition holds a compiled summary of the variant's price set.
type ResolvedPriceSetDefinition struct {
	Name                string   `json:"name"`
	Hash                string   `json:"hash,omitempty"`
	CompiledExpressions int32    `json:"compiledExpressions"`
	PriceCount          int64    `json:"priceCount"`
	Currencies          []string `json:"currencies,omitempty"`
	Strategies          []string `json:"strategies,omitempty"`
}

// ResolvedInventoryDefinition holds the resolved inventory state for a variant.
type ResolvedInventoryDefinition struct {
	Managed           bool   `json:"managed"`
	AvailableQuantity int64  `json:"availableQuantity"`
	Policy            string `json:"policy,omitempty"`
}
