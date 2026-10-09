// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package admission_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	catalogv1 "github.com/gitstore-dev/gitstore/api/gen/gitstore/catalog/v1"
	"github.com/gitstore-dev/gitstore/api/internal/admission"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type goldenCase struct {
	Name   string `json:"name"`
	Errors []struct {
		FilePath   string `json:"filePath"`
		Field      string `json:"field"`
		Constraint string `json:"constraint"`
		Message    string `json:"message"`
	} `json:"errors"`
	Expected string `json:"expected"`
}

func loadGoldenCases(t *testing.T) []goldenCase {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "tests", "fixtures", "admission-rejection-golden.json"))
	require.NoError(t, err)
	var fixture struct {
		Cases []goldenCase `json:"cases"`
	}
	require.NoError(t, json.Unmarshal(raw, &fixture))
	require.NotEmpty(t, fixture.Cases)
	return fixture.Cases
}

func TestFormatRejectionMatchesGoldenFixture(t *testing.T) {
	for _, tc := range loadGoldenCases(t) {
		t.Run(tc.Name, func(t *testing.T) {
			validationErrors := make([]*catalogv1.ValidationError, 0, len(tc.Errors))
			for _, e := range tc.Errors {
				validationErrors = append(validationErrors, &catalogv1.ValidationError{
					FilePath: e.FilePath, Field: e.Field, Constraint: e.Constraint, Message: e.Message,
				})
			}
			diagnostics := admission.FromValidationErrors(validationErrors)
			assert.Equal(t, tc.Expected, admission.FormatRejection(diagnostics))
			err := &admission.Error{Code: admission.CodeAdmissionRejected, Phase: admission.PhasePreReceive, Diagnostics: diagnostics}
			assert.Equal(t, tc.Expected, err.Error())
			assert.Equal(t, tc.Expected, err.ToGQLError().Message)
		})
	}
}

func TestFromValidationErrorsMapsConstraintToReason(t *testing.T) {
	for _, tc := range []struct {
		field, constraint, reason string
	}{
		{"spec.title", "required", "REQUIRED_FIELD"},
		{"apiversion", "eq", "INVALID_ENVELOPE"},
		{"kind", "eq", "INVALID_ENVELOPE"},
		{"", "envelope", "INVALID_ENVELOPE"},
		{"spec.title", "max=200", "INVALID_FIELD"},
		{"metadata.name", "dns-label", "INVALID_FIELD"},
		{"spec.parentRef.name", "self-parent", "SELF_PARENT"},
		{"spec.parentRef.name", "parent-terminating", "PARENT_TERMINATING"},
		{"spec.parentRef.namespace", "cross-namespace", "CROSS_NAMESPACE_REFERENCE"},
		{"metadata.name", "immutable", "IMMUTABLE_NAME"},
		{"metadata.namespace", "immutable", "IMMUTABLE_NAMESPACE"},
		{"spec.tier", "policy/tier-demotion", "POLICY_DENIED"},
		{"spec.title", "", "VALIDATION_FAILED"},
		{"spec.title", "something-new", "VALIDATION_FAILED"},
	} {
		t.Run(tc.constraint+"/"+tc.field, func(t *testing.T) {
			got := admission.FromValidationErrors([]*catalogv1.ValidationError{{
				FilePath: "categories/a.md", Field: tc.field, Constraint: tc.constraint, Message: "m",
			}})
			require.Len(t, got, 1)
			assert.Equal(t, tc.reason, got[0].Reason)
			assert.Equal(t, admission.LevelFailure, got[0].Level)
			assert.Equal(t, "categories/a.md", got[0].File)
			assert.Equal(t, tc.field, got[0].Field)
			assert.Equal(t, "m", got[0].Message)
		})
	}
}

func TestErrorEnvelopeHasOnlyFourKeys(t *testing.T) {
	diag := admission.Diagnostic{Reason: "SELF_PARENT", Message: "bad", Level: admission.LevelFailure, File: "categories/a.md", Field: "spec.parentRef.name"}
	for _, tc := range []struct {
		name string
		err  *admission.Error
		keys []string
	}{
		{"pre-receive", &admission.Error{Code: admission.CodeAdmissionRejected, Phase: admission.PhasePreReceive, CommitSHA: "ignored", Diagnostics: []admission.Diagnostic{diag}}, []string{"code", "diagnostics", "phase"}},
		{"post-receive", &admission.Error{Code: admission.CodeAdmissionRejected, Phase: admission.PhasePostReceive, CommitSHA: "abc", Diagnostics: []admission.Diagnostic{diag}}, []string{"code", "commit", "diagnostics", "phase"}},
		{"post-receive without commit", &admission.Error{Code: admission.CodeAdmissionRejected, Phase: admission.PhasePostReceive, Diagnostics: []admission.Diagnostic{diag}}, []string{"code", "diagnostics", "phase"}},
		{"phase ignored outside admission", &admission.Error{Code: admission.CodeConflict, Phase: admission.PhasePostReceive, CommitSHA: "abc", Diagnostics: []admission.Diagnostic{diag}}, []string{"code", "diagnostics"}},
		{"no diagnostics", &admission.Error{Code: admission.CodeNotFound}, []string{"code"}},
		{"forbidden never discloses", &admission.Error{Code: admission.CodeForbidden, Diagnostics: []admission.Diagnostic{diag}}, []string{"code"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ext := tc.err.ToGQLError().Extensions
			keys := make([]string, 0, len(ext))
			for k := range ext {
				keys = append(keys, k)
			}
			assert.ElementsMatch(t, tc.keys, keys)
			assert.Equal(t, string(tc.err.Code), ext["code"])
		})
	}
}

func TestErrorDiagnosticsOmitAbsentOptionalKeys(t *testing.T) {
	err := admission.NewError(admission.CodeFailedPrecondition, "CHILD_CATEGORIES_PRESENT", "category electronics has child categories")
	gqlErr := err.ToGQLError()
	assert.Equal(t, "category electronics has child categories", gqlErr.Message)
	diagnostics, ok := gqlErr.Extensions["diagnostics"].([]map[string]any)
	require.True(t, ok)
	require.Len(t, diagnostics, 1)
	assert.Equal(t, map[string]any{
		"reason":  "CHILD_CATEGORIES_PRESENT",
		"message": "category electronics has child categories",
		"level":   "FAILURE",
	}, diagnostics[0])
}

func TestErrorIsDiscoverableThroughWrapping(t *testing.T) {
	wrapped := fmt.Errorf("commit category: %w", admission.NewError(admission.CodeConflict, "SUPERSEDED", "superseded"))
	var target *admission.Error
	require.True(t, errors.As(wrapped, &target))
	assert.Equal(t, admission.CodeConflict, target.Code)
	assert.Equal(t, "SUPERSEDED", target.Reason())
}
