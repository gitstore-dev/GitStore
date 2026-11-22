// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package catalog

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

func TestTranslationStatusPreservesResourceBaseline(t *testing.T) {
	var status TranslationStatus
	require.NoError(t, json.Unmarshal([]byte(`{"observedGeneration":3,"lastAppliedRevision":"main@sha1:translation","conditions":[{"type":"TargetResolved","status":"False","observedGeneration":3,"reason":"Missing"}],"resolved":{"canonicalLocale":"de-de","validatedFieldPaths":[],"descriptorDigest":"descriptor","sourceDigest":"translation"}}`), &status))
	encoded, err := json.Marshal(status)
	require.NoError(t, err)
	var fields map[string]any
	require.NoError(t, json.Unmarshal(encoded, &fields))
	require.Equal(t, "main@sha1:translation", fields["lastAppliedRevision"])
	require.EqualValues(t, 3, fields["observedGeneration"])
	require.Len(t, fields, 4)
	resolved := fields["resolved"].(map[string]any)
	require.Equal(t, "de-de", resolved["canonicalLocale"])
	require.Equal(t, "descriptor", resolved["descriptorDigest"])
	require.Equal(t, "translation", resolved["sourceDigest"])
	require.NotContains(t, fields, "observedRevision")
	require.NotContains(t, fields, "freshness")
	require.NotContains(t, fields, "checkedAt")
	require.NotContains(t, string(encoded), "appliedFieldPaths")
}

func TestTranslationSpecRoundTripDoesNotNormalizeAuthoredContent(t *testing.T) {
	var resource TranslationResource
	input := []byte("apiVersion: i18n.gitstore.dev/v1beta1\nkind: Translation\nmetadata:\n  name: MiXeD\nspec:\n  locale: de-DE\n  targetRef:\n    apiVersion: catalog.gitstore.dev/v1beta1\n    kind: Product\n    name: CaseSensitive\n  patches:\n  - fieldPath: spec.options{MiXeD}.title\n    translation: \"\"\n")
	require.NoError(t, yaml.Unmarshal(input, &resource))
	encoded, err := json.Marshal(resource.Spec)
	require.NoError(t, err)
	var stored TranslationSpec
	require.NoError(t, json.Unmarshal(encoded, &stored))
	require.Equal(t, resource.Spec, stored)
	require.Equal(t, "de-DE", stored.Locale)
	require.Equal(t, "CaseSensitive", stored.TargetRef.Name)
	require.Equal(t, "", stored.Patches[0].Translation)
}
