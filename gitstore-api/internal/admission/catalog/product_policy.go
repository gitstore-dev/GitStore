// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package catalog

import (
	"context"

	"github.com/gitstore-dev/gitstore/api/internal/admission"
	"go.uber.org/zap"
)

// ProductValidatingPolicy implements admission.ValidatingAdmissionPolicy for
// Kind == "Product". It is a stub with no checks in spec 027 — a named
// placeholder so that future rules are added by editing this file rather than
// creating new infrastructure.
type ProductValidatingPolicy struct {
	log *zap.Logger
}

// NewProductValidatingPolicy constructs the policy.
func NewProductValidatingPolicy(log *zap.Logger) *ProductValidatingPolicy {
	return &ProductValidatingPolicy{log: log}
}

func (p *ProductValidatingPolicy) Name() string { return "ProductValidatingPolicy" }

// Validate returns Allowed with no conditions. No checks in spec 027.
func (p *ProductValidatingPolicy) Validate(_ context.Context, _ admission.AdmissionRequest) admission.AdmissionDecision {
	return admission.DecisionAllow()
}
