// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package translation

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestTargetContentDigestTracksExactMaterial(t *testing.T) {
	ctx := t.Context()
	version := "catalog.gitstore.dev/v1beta1"
	spec := json.RawMessage(`{"title":"Original"}`)
	original, err := TargetContentDigest(ctx, version, "Product", spec, "body\n")
	if err != nil {
		t.Fatal(err)
	}
	repeated, err := TargetContentDigest(ctx, version, "Product", spec, "body\n")
	if err != nil || repeated != original {
		t.Fatalf("same material changed digest: %q, %v", repeated, err)
	}
	stored, err := TargetContentDigestStringSpec(ctx, version, "Product", string(spec), "body\n")
	if err != nil || stored != original {
		t.Fatalf("string-backed source changed material identity: %q, %v", stored, err)
	}
	for _, changed := range []struct {
		kind string
		spec json.RawMessage
		body string
	}{
		{"Collection", spec, "body\n"},
		{"Product", json.RawMessage(`{"title":"Changed"}`), "body\n"},
		{"Product", json.RawMessage(`{ "title": "Original" }`), "body\n"},
		{"Product", spec, "body\r\n"},
		{"Product", spec, "body"},
	} {
		digest, err := TargetContentDigest(ctx, version, changed.kind, changed.spec, changed.body)
		if err != nil || digest == original {
			t.Errorf("changed material retained digest: %+v, %v", changed, err)
		}
	}
	// Spec is opaque here: malformed targets still need a distinct source identity.
	first, err := TargetContentDigest(ctx, version, "Product", json.RawMessage("a"), "bc")
	if err != nil {
		t.Fatal(err)
	}
	second, err := TargetContentDigest(ctx, version, "Product", json.RawMessage("ab"), "c")
	if err != nil || first == second {
		t.Fatalf("component boundaries are ambiguous: %q, %q, %v", first, second, err)
	}
}

func TestTargetContentDigestBoundsAndCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := TargetContentDigest(ctx, "catalog.gitstore.dev/v1beta1", "Product", nil, "body"); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled digest: %v", err)
	}
	if _, err := TargetContentDigest(t.Context(), "storage.gitstore.dev/v1beta1", "Product", nil, "body"); err == nil {
		t.Fatal("unsupported GVK accepted")
	}
	body := strings.Repeat("x", MaxTargetBytes)
	if _, err := TargetContentDigest(t.Context(), "catalog.gitstore.dev/v1beta1", "Product", json.RawMessage("{}"), body); err == nil {
		t.Fatal("combined spec/body byte budget exceeded without failure")
	}
}

func TestPathsAndConflicts(t *testing.T) {
	for _, path := range []string{"spec.title", "spec.options{color}.title", "spec.options{size}.inline.values{m}.title", "spec.media{hero-image}.fileRef.name"} {
		if _, err := ParsePath(path); err != nil {
			t.Fatalf("%s: %v", path, err)
		}
	}
	for _, path := range []string{
		"", "title", "{body}", "spec", "spec.", "spec..title", "spec.options[0].title",
		"spec.options{*}.title", "spec.options{}.title", "spec.options{a.b}.title",
		"spec.options{a\\b}.title", "spec.options{a}{b}.title", "spec.options{a b}.title",
		"spec.title()", "spec.9title", "spec." + strings.Repeat("a.", 16) + "title",
		"spec." + strings.Repeat("a", 1020),
	} {
		if _, err := ParsePath(path); err == nil {
			t.Errorf("invalid path accepted: %s", path)
		}
	}
	for _, paths := range [][]string{
		{"spec.title", "spec.title"},
		{"spec.options", "spec.options{color}.title"},
		{"spec.options{color}", "spec.options{color}.title"},
	} {
		if err := ValidatePaths(paths); err == nil {
			t.Errorf("conflicting paths accepted: %v", paths)
		}
	}
	if err := ValidatePaths([]string{"spec.options{color}.title", "spec.options{size}.title"}); err != nil {
		t.Fatalf("distinct selected members overlap: %v", err)
	}
}

func TestPathBudgetBoundaries(t *testing.T) {
	for _, path := range []string{
		"spec." + strings.Repeat("a", 1019),
		"spec." + strings.Repeat("a.", 15) + "a",
		"spec." + strings.Repeat("a{x}.", 7) + "a{x}",
	} {
		if _, err := ParsePath(path); err != nil {
			t.Fatalf("path at the limit rejected: %v", err)
		}
	}
	if _, err := ParsePath("spec." + strings.Repeat("a{x}.", 8) + "a{x}"); err == nil {
		t.Fatal("ninth selector accepted")
	}
	if err := ValidatePaths([]string{"spec.options{color}.title", "spec.options"}); err == nil {
		t.Fatal("overlap depends on patch order")
	}
}

func TestDescriptorDecidableTextConstraintsWithoutTarget(t *testing.T) {
	for _, text := range []string{"", strings.Repeat("x", 201)} {
		spec := Spec{Locale: "de-DE", TargetRef: Reference{APIVersion: "catalog.gitstore.dev/v1beta1", Kind: "Product", Name: "absent"},
			Patches: []Patch{{FieldPath: "spec.title", Translation: text}}}
		if err := ValidateSpec(spec, "acme", nil); err == nil {
			t.Fatal("known Product text constraint deferred until target existence")
		}
	}
}

func TestEligibleFieldsAndReorder(t *testing.T) {
	options := []any{
		map[string]any{"name": "color", "title": "Color", "values": []any{"red"}},
		map[string]any{"name": "size", "title": "Size"},
	}

	target := map[string]any{"title": "Laptop", "options": options}
	patches := []Patch{{FieldPath: "spec.options{color}.title", Translation: ""}, {FieldPath: "spec.title", Translation: "Computer"}}
	before, err := ValidateFields("catalog.gitstore.dev/v1beta1", "Product", target, patches, nil)
	if err != nil {
		t.Fatal(err)
	}
	options[0], options[1] = options[1], options[0]
	after, err := ValidateFields("catalog.gitstore.dev/v1beta1", "Product", target, patches, nil)
	if err != nil || strings.Join(before, ",") != strings.Join(after, ",") {
		t.Fatalf("reorder changed selected fields: %v / %v (%v)", before, after, err)
	}
	if options[1].(map[string]any)["title"] != "Color" || target["title"] != "Laptop" {
		t.Fatal("validation mutated the target")
	}
	for _, path := range []string{
		"spec.options{missing}.title", "spec.options{color}", "spec.options{color}.name",
		"spec.options{color}.values", "spec.media{hero}.fileRef.name", "spec.lifecycle.state",
		"spec.options{size}.definitionRef.values{m}.title", "spec.options{size}.inline.values{m}.title",
	} {
		if _, err := ValidateFields("catalog.gitstore.dev/v1beta1", "Product", target, []Patch{{FieldPath: path, Translation: "text"}}, nil); err == nil {
			t.Errorf("ineligible/missing path accepted: %s", path)
		}
	}
	options[0] = options[1]
	if _, err := ValidateFields("catalog.gitstore.dev/v1beta1", "Product", target, patches, nil); err == nil {
		t.Fatal("ambiguous selector accepted")
	}
	for _, kind := range []string{"ProductVariant", "CategoryTaxonomy", "Collection"} {
		if _, err := ValidateFields("catalog.gitstore.dev/v1beta1", kind, map[string]any{"title": "Original"}, []Patch{{FieldPath: "spec.title", Translation: "Translated"}}, nil); err != nil {
			t.Fatalf("%s: %v", kind, err)
		}
	}
	if _, err := ValidateFields("storage.gitstore.dev/v1beta1", "File", nil, nil, []byte("description")); err != nil {
		t.Fatal(err)
	}
	if _, err := ValidateFields("storage.gitstore.dev/v1beta1", "File", nil, []Patch{{FieldPath: "spec.title", Translation: "Invalid"}}, nil); err == nil {
		t.Fatal("File title invented")
	}
	if _, err := ValidateFields("catalog.gitstore.dev/v1beta1", "Product", target, []Patch{{FieldPath: "spec.title", Translation: strings.Repeat("x", 201)}}, nil); err == nil {
		t.Fatal("Product title constraint bypassed")
	}
	for _, kind := range []string{"AttributeDefinition", "Translation", "CustomCatalogKind", "Namespace"} {
		if _, err := DescriptorFor("catalog.gitstore.dev/v1beta1", kind); err == nil {
			t.Errorf("unsupported target activated: %s", kind)
		}
	}
}

func TestTargetSnapshotWorkIsBounded(t *testing.T) {
	deep := map[string]any{}
	cursor := deep
	for range 65 {
		child := map[string]any{}
		cursor["child"] = child
		cursor = child
	}
	chunk := strings.Repeat("9", 4<<20)
	repeated := make([]any, 26)
	numbers := make([]any, 26)
	for i := range repeated {
		repeated[i] = chunk
		numbers[i] = json.Number(chunk)
	}
	for name, extra := range map[string]any{
		"depth":        deep,
		"nodes":        make([]any, 65_537),
		"bytes":        repeated,
		"number-bytes": numbers,
	} {
		t.Run(name, func(t *testing.T) {
			paths, err := ValidateFields("catalog.gitstore.dev/v1beta1", "Product",
				map[string]any{"title": "Original", "other": extra},
				[]Patch{{FieldPath: "spec.title", Translation: "Translated"}}, nil)
			var issue *Error
			if !errors.As(err, &issue) || issue.Reason != "TargetWorkUnavailable" || paths != nil {
				t.Fatalf("unbounded target work was not an operational failure: %v / %v", paths, err)
			}
		})
	}
}

func TestFieldValidationBoundsSourceContent(t *testing.T) {
	for _, bodyOnly := range []bool{false, true} {
		t.Run(map[bool]string{false: "patch", true: "body"}[bodyOnly], func(t *testing.T) {
			patches := []Patch{{FieldPath: "spec.title", Translation: strings.Repeat("x", MaxDocumentBytes+1)}}
			var body []byte
			if bodyOnly {
				patches = nil
				body = []byte(strings.Repeat("x", MaxDocumentBytes+1))
			}
			paths, err := ValidateFields("catalog.gitstore.dev/v1beta1", "ProductVariant",
				map[string]any{"title": "Original"}, patches, body)
			var issue *Error
			if !errors.As(err, &issue) || issue.Reason != "LimitExceeded" || paths != nil {
				t.Fatalf("oversized content accepted: %v / %v", paths, err)
			}
		})
	}
}

func TestDeliveredTargetTextConstraints(t *testing.T) {
	for _, kind := range []string{"Product", "ProductVariant", "CategoryTaxonomy", "Collection"} {
		for _, text := range []string{"", strings.Repeat("\u754c", 200), strings.Repeat("\u754c", 201)} {
			_, err := ValidateFields("catalog.gitstore.dev/v1beta1", kind, map[string]any{"title": "Original"},
				[]Patch{{FieldPath: "spec.title", Translation: text}}, nil)
			wantError := text == "" || (kind == "Product" && len([]rune(text)) > 200)
			if (err != nil) != wantError {
				t.Fatalf("%s title constraint changed: length=%d, error=%v", kind, len([]rune(text)), err)
			}
		}
	}
}

func TestDescriptorDigestsTrackActualPolicy(t *testing.T) {
	for _, mutate := range []func(*descriptorPolicy){
		func(p *descriptorPolicy) { p.Fields[0].MaxRunes++ },
		func(p *descriptorPolicy) { p.Fields[0].MinRunes++ },
		func(p *descriptorPolicy) { p.Fields[1].Path = "spec.options{}.other" },
		func(p *descriptorPolicy) { p.Lists[0].IdentityPath = "name" },
		func(p *descriptorPolicy) { p.Boundaries[0] = "spec.otherRef" },
		func(p *descriptorPolicy) { p.Body = false },
		func(p *descriptorPolicy) { p.MaxNodes-- },
		func(p *descriptorPolicy) { p.MaxDepth-- },
		func(p *descriptorPolicy) { p.MaxBytes-- },
	} {
		descriptor, err := DescriptorFor("catalog.gitstore.dev/v1beta1", "Product")
		if err != nil {
			t.Fatal(err)
		}
		mutate(&descriptor.policy)
		if policyDigest(descriptor.policy) == descriptor.Digest {
			t.Fatal("material descriptor policy change retained its digest")
		}
		fresh, err := DescriptorFor("catalog.gitstore.dev/v1beta1", "Product")
		if err != nil || fresh.Digest != policyDigest(fresh.policy) || fresh.Digest != descriptor.Digest {
			t.Fatalf("returned descriptor mutated the compiled policy: %v", err)
		}
	}
}

func TestExplicitSelectorIdentitiesAndReferenceBoundaries(t *testing.T) {
	for _, kind := range []string{"Product", "ProductVariant", "CategoryTaxonomy", "Collection"} {
		t.Run(kind, func(t *testing.T) {
			descriptor, err := DescriptorFor("catalog.gitstore.dev/v1beta1", kind)
			if err != nil {
				t.Fatal(err)
			}
			media := []any{
				map[string]any{"name": "decoy", "fileRef": map[string]any{"name": "hero-image"}},
				map[string]any{"name": "hero-image", "fileRef": map[string]any{"name": "other"}},
			}
			target := map[string]any{"title": "Original", "media": media}
			for range 2 {
				indexes, err := buildSelectorIndexes(t.Context(), target, descriptor.policy.Lists)
				if err != nil {
					t.Fatal(err)
				}
				member := indexes["spec.media"]["hero-image"]
				if member.value["name"] != "decoy" || member.ambiguous {
					t.Fatal("media identity guessed a local name instead of fileRef.name")
				}
				media[0], media[1] = media[1], media[0]
			}
			media[1] = media[0]
			indexes, err := buildSelectorIndexes(t.Context(), target, descriptor.policy.Lists)
			if err != nil || !indexes["spec.media"]["hero-image"].ambiguous {
				t.Fatalf("duplicate fileRef.name was not ambiguous: %v", err)
			}
			_, err = ValidateFields("catalog.gitstore.dev/v1beta1", kind, target,
				[]Patch{{FieldPath: "spec.media{hero-image}.fileRef.name", Translation: "forbidden"}}, nil)
			var issue *Error
			if !errors.As(err, &issue) || issue.Reason != "ReferenceBoundary" {
				t.Fatalf("media reference became eligible text: %v", err)
			}
		})
	}
	target := map[string]any{"options": []any{map[string]any{"name": "color", "key": "size", "title": ""}}}
	if _, err := ValidateFields("catalog.gitstore.dev/v1beta1", "Product", target,
		[]Patch{{FieldPath: "spec.options{color}.title", Translation: ""}}, nil); err != nil {
		t.Fatalf("optional empty option title rejected: %v", err)
	}
	if _, err := ValidateFields("catalog.gitstore.dev/v1beta1", "Product", target,
		[]Patch{{FieldPath: "spec.options{size}.title", Translation: ""}}, nil); err == nil {
		t.Fatal("proposal-only key identity was guessed")
	}
}

type cancelDuringValidation struct {
	context.Context
	cancel    context.CancelFunc
	remaining int
}

func (c *cancelDuringValidation) Err() error {
	c.remaining--
	if c.remaining == 0 {
		c.cancel()
	}
	return c.Context.Err()
}

func TestFieldValidationCancelsWithoutPartialResults(t *testing.T) {
	base, cancel := context.WithCancel(t.Context())
	defer cancel()
	ctx := &cancelDuringValidation{Context: base, cancel: cancel, remaining: 12}
	target := map[string]any{"title": "Original", "other": make([]any, 100)}
	paths, err := ValidateFieldsContext(ctx, "catalog.gitstore.dev/v1beta1", "Product", target,
		[]Patch{{FieldPath: "spec.title", Translation: "Translated"}}, nil)
	if !errors.Is(err, context.Canceled) || paths != nil {
		t.Fatalf("cancellation became partial validation success: %v / %v", paths, err)
	}
}

func TestMissingAndNonTextLeavesRejectTheWholePatchSet(t *testing.T) {
	for _, value := range []any{nil, true, float64(1), int64(1), []any{"text"}, map[string]any{"text": "value"}} {
		target := map[string]any{"title": "Original", "options": []any{map[string]any{"name": "color", "title": value}}}
		before, err := json.Marshal(target)
		if err != nil {
			t.Fatal(err)
		}
		paths, err := ValidateFields("catalog.gitstore.dev/v1beta1", "Product", target,
			[]Patch{{FieldPath: "spec.title", Translation: "Translated"}, {FieldPath: "spec.options{color}.title", Translation: ""}}, nil)
		var issue *Error
		if !errors.As(err, &issue) || issue.Reason != "FieldMissing" || paths != nil {
			t.Fatalf("non-text leaf or partial patch set accepted: %v / %v", paths, err)
		}
		after, err := json.Marshal(target)
		if err != nil || string(before) != string(after) {
			t.Fatal("failed validation mutated the target")
		}
	}
}
