// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package catalog

import (
	"context"

	"github.com/gitstore-dev/gitstore/api/internal/admission"
	catalogapi "github.com/gitstore-dev/gitstore/api/internal/catalog"
	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	"go.uber.org/zap"
)

// ProductValidatingPolicy implements category-target admission checks.
type ProductValidatingPolicy struct {
	store datastore.Datastore
	log   *zap.Logger
}

// NewProductValidatingPolicy constructs the policy.
func NewProductValidatingPolicy(log *zap.Logger, stores ...datastore.Datastore) *ProductValidatingPolicy {
	var store datastore.Datastore
	if len(stores) > 0 {
		store = stores[0]
	}
	return &ProductValidatingPolicy{store: store, log: log}
}

func (p *ProductValidatingPolicy) Name() string { return "ProductValidatingPolicy" }

// Validate rejects a Product that targets an already-terminating category.
// Missing categories remain legitimate unresolved references.
func (p *ProductValidatingPolicy) Validate(ctx context.Context, req admission.AdmissionRequest) admission.AdmissionDecision {
	resource, ok := req.Object.(*catalogapi.ProductResource)
	if !ok || resource == nil || resource.Spec.CategoryRef == nil || resource.Spec.CategoryRef.Name == "" || p.store == nil {
		return admission.DecisionAllow()
	}
	namespace := resource.Metadata.Namespace
	if namespace == "" {
		namespace = req.Namespace
	}
	category, err := p.store.GetCategoryTaxonomyByName(ctx, namespace, resource.Spec.CategoryRef.Name)
	if err == nil && category != nil && category.DeletionTimestamp != nil {
		return admission.DecisionDeny("CategoryTerminating", "spec.categoryRef.name")
	}
	return admission.DecisionAllow()
}
