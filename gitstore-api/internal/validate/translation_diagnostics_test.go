// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package validate

import (
	"bytes"
	"errors"
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"

	catalogv1 "github.com/gitstore-dev/gitstore/api/gen/gitstore/catalog/v1"
	"github.com/gitstore-dev/gitstore/api/internal/admission"
	"github.com/gitstore-dev/gitstore/api/internal/testutil"
	"github.com/gitstore-dev/gitstore/translation"
	"github.com/stretchr/testify/require"
)

func TestTranslationNegativeMatrix(t *testing.T) {
	testutil.RunTranslationNegativeMatrix(t, "../cataloggrpc/testdata/translation/corpus.json",
		func(t *testing.T, content []byte, fields map[string]any) []*catalogv1.ValidationError {
			parsed, body, err := NewParser().ParseResource(bytes.NewReader(content))
			if err == nil {
				ref := parsed.Translation.Spec.TargetRef
				result, validationErr := ValidateTranslationSnapshot(t.Context(), parsed.Translation, body, "acme-store", &TranslationTargetSnapshot{
					APIVersion: ref.APIVersion, Kind: ref.Kind, Namespace: "acme-store", Name: ref.Name,
					UID: "00000000-0000-4000-8000-000000000001", ResourceVersion: "7", Spec: fields,
				})
				require.Nil(t, result, "a rejected document cannot return partially validated paths")
				err = validationErr
			}
			findings, typed := TranslationValidationErrors("translations/de.md", err)
			require.True(t, typed)
			return findings
		})
}

func TestTranslationDiagnosticsPreserveTypedLocationsAndReasons(t *testing.T) {
	valid, negative := translationCorpus(t)
	for _, change := range negative {
		if change.Name == "missing-selector" {
			continue
		}
		t.Run(change.Name, func(t *testing.T) {
			content := strings.Replace(valid[change.Base], change.Old, change.New, 1)
			parsed, body, err := NewParser().ParseResource(strings.NewReader(content))
			require.Error(t, err)
			require.Nil(t, parsed)
			require.Nil(t, body)
			var issue *translation.Error
			require.ErrorAs(t, err, &issue)
			require.True(t, strings.HasPrefix(issue.Field, change.Field), "expected %s, got %s", change.Field, issue.Field)
			findings, ok := TranslationValidationErrors("translations/de.md", fmt.Errorf("parse: %w", err))
			require.True(t, ok)
			require.Len(t, findings, 1)
			require.Equal(t, issue.Field, findings[0].Field)
			require.Equal(t, "translation/"+issue.Reason, findings[0].Constraint)
			require.NotContains(t, findings[0].Message, "Tragbarer Computer")
			diagnostics := admission.FromValidationErrors(findings)
			require.NotEqual(t, admission.ReasonValidationFailed, diagnostics[0].Reason)
		})
	}
}

func TestTranslationDiagnosticBudgetsAndExplicitTruncation(t *testing.T) {
	var issues []error
	for range 40 {
		issues = append(issues, &translation.Error{
			Field: "spec." + strings.Repeat("\u00e9", 300), Reason: "UnknownField",
			Message: strings.Repeat("\u00e9", 300),
		})
	}
	findings, ok := TranslationValidationErrors(strings.Repeat("x", 300), errors.Join(issues...))
	require.True(t, ok)
	require.Len(t, findings, 32)
	for _, finding := range findings {
		require.LessOrEqual(t, len(finding.Message), 256)
		require.LessOrEqual(t, len(finding.Field), 256)
		require.LessOrEqual(t, len(finding.FilePath), 256)
		require.True(t, utf8.ValidString(finding.Message))
		require.True(t, utf8.ValidString(finding.Field))
	}
	require.Contains(t, findings[31].Message, "additional issues omitted")
	require.NotContains(t, findings[30].Message, "additional issues omitted")
	_, ok = TranslationValidationErrors("product.md", errors.New("unrelated parser failure"))
	require.False(t, ok, "non-Translation diagnostics must retain their existing conversion")
}

func TestTranslationMalformedYAMLHasSafeTypedDiagnostics(t *testing.T) {
	valid, _ := translationCorpus(t)
	for _, content := range []string{
		strings.Replace(valid["product-field"], "locale: de-DE", "locale: [Tragbarer Computer", 1),
		strings.Replace(valid["product-field"], "spec:\n", "spec:\n  [\n", 1),
	} {
		require.True(t, IsTranslationDocument([]byte(content)))
		parsed, body, err := NewParser().ParseResource(strings.NewReader(content))
		require.Nil(t, parsed)
		require.Nil(t, body)
		var issue *translation.Error
		require.ErrorAs(t, err, &issue)
		require.Equal(t, "MalformedSource", issue.Reason)
		require.NotContains(t, err.Error(), "Tragbarer Computer")
	}
}
