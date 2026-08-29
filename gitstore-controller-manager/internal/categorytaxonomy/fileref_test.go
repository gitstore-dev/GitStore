// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package categorytaxonomy

import "testing"

func TestComputeFileRefCondition_RequiredMediaProducesUnknownCondition(t *testing.T) {
	c := CategoryTaxonomy{
		Namespace: "acme",
		Name:      "electronics",
		Media:     []MediaRef{{Name: "missing-image", Optional: false}},
	}

	cond := computeFileRefCondition(c)

	if cond == nil {
		t.Fatal("expected a non-nil condition for an optional:false media entry")
	}
	if cond.Status != "UNKNOWN" {
		t.Errorf("Status = %q, want UNKNOWN", cond.Status)
	}
}

func TestComputeFileRefCondition_OptionalMediaProducesNoCondition(t *testing.T) {
	c := CategoryTaxonomy{
		Namespace: "acme",
		Name:      "electronics",
		Media:     []MediaRef{{Name: "nice-to-have", Optional: true}},
	}

	cond := computeFileRefCondition(c)

	if cond != nil {
		t.Errorf("expected nil condition for an optional:true-only media list, got %+v", cond)
	}
}

func TestComputeFileRefCondition_MixedMediaOnlyFlagsRequired(t *testing.T) {
	c := CategoryTaxonomy{
		Namespace: "acme",
		Name:      "electronics",
		Media: []MediaRef{
			{Name: "nice-to-have", Optional: true},
			{Name: "required-banner", Optional: false},
		},
	}

	cond := computeFileRefCondition(c)

	if cond == nil || cond.Status != "UNKNOWN" {
		t.Fatalf("expected an UNKNOWN condition when any required entry exists, got %+v", cond)
	}
}

func TestComputeFileRefCondition_NoMediaProducesNoCondition(t *testing.T) {
	c := CategoryTaxonomy{Namespace: "acme", Name: "electronics"}

	cond := computeFileRefCondition(c)

	if cond != nil {
		t.Errorf("expected nil condition when spec.media is empty, got %+v", cond)
	}
}
