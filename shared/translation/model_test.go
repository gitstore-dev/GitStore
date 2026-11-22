// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package translation

import "testing"

func TestSpecReferenceValidation(t *testing.T) {
	base := Spec{Locale: "de-DE", TargetRef: Reference{APIVersion: "catalog.gitstore.dev/v1beta1", Kind: "Product", Name: "laptop"}, Patches: []Patch{{FieldPath: "spec.title", Translation: "Titel"}}}
	if err := ValidateSpec(base, "acme-store", nil); err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*Spec){
		func(s *Spec) { s.TargetRef.Namespace = "other" },
		func(s *Spec) { s.TargetRef.APIVersion = "wrong/v1" },
		func(s *Spec) { s.TargetRef.Kind = "Namespace" },
		func(s *Spec) { s.TargetRef.Name = "" },
		func(s *Spec) { s.Patches = nil },
		func(s *Spec) { s.Patches[0].FieldPath = "spec.lifecycle.state" },
	} {
		input := base
		input.Patches = append([]Patch(nil), base.Patches...)
		mutate(&input)
		if err := ValidateSpec(input, "acme-store", nil); err == nil {
			t.Fatal("invalid source/reference accepted")
		}
	}
	bodyOnly := base
	bodyOnly.Patches = nil
	if err := ValidateSpec(bodyOnly, "acme-store", []byte("body")); err != nil {
		t.Fatal(err)
	}
	base.TargetRef.Namespace = "acme-store"
	if err := ValidateSpec(base, "", nil); err != nil {
		t.Fatal("repository namespace defaults must be resolved by the caller:", err)
	}
}
