// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package catalog

import (
	"fmt"
	"time"

	"github.com/shopspring/decimal"
)

func ValidateFileConditions(conditions []Condition) error {
	allowed := map[ConditionType]bool{
		ConditionAdmissionAccepted: true, ConditionSourceResolved: true,
		ConditionProcessingComplete: true, ConditionReady: true, ConditionTerminating: true,
	}
	for i, condition := range conditions {
		if !allowed[condition.Type] {
			return fmt.Errorf("file condition %d has unsupported type %q", i, condition.Type)
		}
		if condition.Status != ConditionTrue && condition.Status != ConditionFalse && condition.Status != ConditionUnknown {
			return fmt.Errorf("file condition %d has unsupported status %q", i, condition.Status)
		}
	}
	return nil
}

// ConditionType is the name of a product status condition.
type ConditionType = string

// ConditionStatus is the value of a product status condition.
type ConditionStatus = string

const (
	ConditionPublished          ConditionType = "Published"
	ConditionAdmissionAccepted  ConditionType = "AdmissionAccepted"
	ConditionCategoryResolved   ConditionType = "CategoryResolved"
	ConditionOptionsAccepted    ConditionType = "OptionsAccepted"
	ConditionVariantsResolved   ConditionType = "VariantsResolved"
	ConditionReady              ConditionType = "Ready"
	ConditionParentResolved     ConditionType = "ParentResolved"
	ConditionAcyclic            ConditionType = "Acyclic"
	ConditionSystemRepoReady    ConditionType = "SystemRepoReady"
	ConditionTerminating        ConditionType = "Terminating"
	ConditionSourceResolved     ConditionType = "SourceResolved"
	ConditionProcessingComplete ConditionType = "ProcessingComplete"

	// ProductVariant-specific condition types.
	ConditionProductResolved ConditionType = "ProductResolved"
	ConditionPricingAccepted ConditionType = "PricingAccepted"

	ConditionTrue    ConditionStatus = "True"
	ConditionFalse   ConditionStatus = "False"
	ConditionUnknown ConditionStatus = "Unknown"
)

// ProductStatus is the system-written state for a product. Never stored in git.
type ProductStatus struct {
	ObservedGeneration  int64                      `json:"observedGeneration"`
	LastAppliedRevision string                     `json:"lastAppliedRevision"`
	Conditions          []Condition                `json:"conditions"`
	Resolved            *ResolvedProductDefinition `json:"resolved,omitempty"`
}

// Condition is a named status signal following the Kubernetes condition convention.
type Condition struct {
	Type               ConditionType   `json:"type"               validate:"required,oneof=Published AdmissionAccepted CategoryResolved OptionsAccepted VariantsResolved Ready ParentResolved Acyclic ProductResolved PricingAccepted"`
	Status             ConditionStatus `json:"status"             validate:"required,oneof=True False Unknown"`
	ObservedGeneration int64           `json:"observedGeneration"`
	LastTransitionTime time.Time       `json:"lastTransitionTime"`
	Reason             string          `json:"reason,omitempty"`
	Message            string          `json:"message,omitempty"`
}

// ResolvedProductDefinition holds system-computed aggregates for a product.
type ResolvedProductDefinition struct {
	Category          *ResolvedCategoryDefinition `json:"category,omitempty"`
	PriceRange        []PriceRangeDefinition      `json:"priceRange,omitempty"`
	TotalInventory    int64                       `json:"totalInventory"`
	VariantSummary    *VariantSummaryDefinition   `json:"variantSummary,omitempty"`
	DefaultVariantRef *ObjectReference            `json:"defaultVariantRef,omitempty"`
	Media             []ResolvedFileDefinition    `json:"media,omitempty"`
}

// ResolvedCategoryDefinition is the resolved category identity and path.
type ResolvedCategoryDefinition struct {
	Name string   `json:"name"`
	Path []string `json:"path"`
}

// PriceRangeDefinition uses shopspring/decimal for monetary values, consistent
// with the rest of the API (scalar/scalars.go Decimal scalar binding).
type PriceRangeDefinition struct {
	CurrencyCode string          `json:"currencyCode"`
	Min          decimal.Decimal `json:"min"`
	Max          decimal.Decimal `json:"max"`
}

// VariantSummaryDefinition holds variant counts by readiness.
type VariantSummaryDefinition struct {
	Total       int64 `json:"total"`
	Ready       int64 `json:"ready"`
	Unavailable int64 `json:"unavailable"`
}

// ResolvedFileDefinition is a resolved media file with a CDN URL.
type ResolvedFileDefinition struct {
	Name             string                `json:"name"`
	URL              string                `json:"url"`
	ContentType      string                `json:"contentType,omitempty"`
	ResolvedVariants []ResolvedFileVariant `json:"resolvedVariants,omitempty"`
}

// SystemObjectMeta holds read-only fields the system assigns. Merged with
// ObjectMeta at read time to produce the full resource view.
type SystemObjectMeta struct {
	UID               string           `json:"uid"`
	ResourceVersion   string           `json:"resourceVersion"`
	Generation        int64            `json:"generation"`
	CreationTimestamp time.Time        `json:"creationTimestamp"`
	Revision          string           `json:"revision"`
	OwnerReferences   []OwnerReference `json:"ownerReferences,omitempty"`
}

// OwnerReference is a typed pointer to the resource that owns this object.
type OwnerReference struct {
	APIVersion         string `yaml:"apiVersion"         json:"apiVersion"`
	Kind               string `yaml:"kind"               json:"kind"`
	Name               string `yaml:"name"               json:"name"`
	UID                string `yaml:"uid"                json:"uid"`
	BlockOwnerDeletion bool   `yaml:"blockOwnerDeletion" json:"blockOwnerDeletion"`
	// RepositoryID is internal projection scope. It is system-populated so
	// cross-repository references in one namespace are indexed with the owner.
	RepositoryID string `yaml:"-" json:"repositoryID,omitempty"`
}

// CategoryTaxonomyStatus is the system-written state for a category taxonomy. Never stored in git.
type CategoryTaxonomyStatus struct {
	ObservedGeneration  int64                     `json:"observedGeneration"`
	LastAppliedRevision string                    `json:"lastAppliedRevision"`
	Conditions          []Condition               `json:"conditions"`
	Resolved            *ResolvedCategoryTaxonomy `json:"resolved,omitempty"`
}

// NamespaceStatus is the system-written state for a Namespace. Never stored in git.
type NamespaceStatus struct {
	ObservedGeneration  int64       `json:"observedGeneration"`
	LastAppliedRevision string      `json:"lastAppliedRevision"`
	Conditions          []Condition `json:"conditions"`
}

// ResolvedCategoryTaxonomy holds system-computed hierarchy aggregates for a category.
type ResolvedCategoryTaxonomy struct {
	Depth int8 `json:"depth"`
	// Path is the ancestor path from root to self, e.g.
	// []string{"electronics", "computers", "laptops"} (root-to-self order).
	// A root category's Path is a single-element slice containing its own
	// name. See specs/040-controller-watch-status-api/research.md R9/R10.
	Path         []string          `json:"path"`
	Ancestors    []ObjectReference `json:"ancestors,omitempty"`
	ChildCount   int64             `json:"childCount"`
	ProductCount int64             `json:"productCount"`
}
