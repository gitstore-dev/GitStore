// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package catalog

import (
	"context"

	"github.com/gitstore-dev/gitstore/api/internal/admission"
	"go.uber.org/zap"
)

// CollectionValidatingPolicy implements admission.ValidatingAdmissionPolicy for
// Kind == "Collection". It is a stub with no checks in spec 027 — a named
// placeholder so that future rules are added by editing this file rather than
// creating new infrastructure.
type CollectionValidatingPolicy struct {
	log *zap.Logger
}

// NewCollectionValidatingPolicy constructs the policy.
func NewCollectionValidatingPolicy(log *zap.Logger) *CollectionValidatingPolicy {
	return &CollectionValidatingPolicy{log: log}
}

func (p *CollectionValidatingPolicy) Name() string { return "CollectionValidatingPolicy" }

// Validate returns Allowed with no conditions. No checks in spec 027.
func (p *CollectionValidatingPolicy) Validate(_ context.Context, _ admission.AdmissionRequest) admission.AdmissionDecision {
	return admission.DecisionAllow()
}
