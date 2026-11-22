// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package catalog

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/gitstore-dev/gitstore/translation"
	"github.com/stretchr/testify/require"
)

func TestTranslationRelatedDefinitionBoundaries(t *testing.T) {
	product := ProductSpec{Title: "Tee", Options: []ProductOptionDefinition{
		{Name: "size", Title: "Size", Values: []string{"s", "m"}},
	}}
	variant := ProductVariantSpec{Title: "Medium tee", SKU: "tee-m",
		ProductRef:      &ObjectReference{Kind: "Product", Name: "tee"},
		SelectedOptions: []SelectedOptionDefinition{{Name: "size", Value: "m"}}}
	before, err := json.Marshal([]any{product, variant})
	require.NoError(t, err)

	target := map[string]any{"title": product.Title, "options": []any{
		map[string]any{"name": product.Options[0].Name, "title": product.Options[0].Title, "values": []any{"s", "m"}},
	}}
	paths, err := translation.ValidateFields("catalog.gitstore.dev/v1beta1", "Product", target,
		[]translation.Patch{{FieldPath: "spec.options{size}.title", Translation: "Groesse"}}, nil)
	require.NoError(t, err)
	require.Equal(t, []string{"spec.options{size}.title"}, paths)

	for _, path := range []string{
		"spec.options{size}.values{m}.title",
		"spec.options{size}.inline.values{m}.title",
		"spec.options{size}.definitionRef.values{m}.title",
	} {
		t.Run(path, func(t *testing.T) {
			paths, err := translation.ValidateFields("catalog.gitstore.dev/v1beta1", "Product", target,
				[]translation.Patch{{FieldPath: path, Translation: "Mittel"}}, nil)
			require.Error(t, err)
			require.Nil(t, paths)
		})
	}

	extracted := map[string]any{"title": product.Title, "options": []any{
		map[string]any{"name": "size", "definitionRef": map[string]any{"name": "garment-size"}},
	}}
	paths, err = translation.ValidateFields("catalog.gitstore.dev/v1beta1", "Product", extracted,
		[]translation.Patch{{FieldPath: "spec.options{size}.title", Translation: "Groesse"}}, nil)
	require.Error(t, err, "extraction cannot silently follow a shared definition")
	require.Nil(t, paths)

	for _, uid := range []string{"00000000-0000-4000-8000-000000000001", "00000000-0000-4000-8000-000000000002"} {
		spec := translation.Spec{Locale: "de-DE", TargetRef: translation.Reference{
			APIVersion: "catalog.gitstore.dev/v1beta1", Kind: "AttributeDefinition", Name: "garment-size", UID: uid,
		}, Patches: []translation.Patch{{FieldPath: "spec.values{m}.title", Translation: "Mittel"}}}
		var issue *translation.Error
		require.True(t, errors.As(translation.ValidateSpec(spec, "shop", nil), &issue))
		require.Equal(t, "UnsupportedTarget", issue.Reason, "explicit defining-UID retargeting does not activate a deferred kind")
		require.Equal(t, uid, spec.TargetRef.UID)
	}

	after, err := json.Marshal([]any{product, variant})
	require.NoError(t, err)
	require.Equal(t, before, after, "validation must not alter source, canonical option values or variant selections")
}
