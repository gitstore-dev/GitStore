// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package testutil

import (
	"bytes"
	"encoding/json"
	"os"
	"strings"
	"testing"

	catalogv1 "github.com/gitstore-dev/gitstore/api/gen/gitstore/catalog/v1"
	"github.com/gitstore-dev/gitstore/api/internal/admission"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

func RunTranslationNegativeMatrix(t *testing.T, path string, run func(*testing.T, []byte, map[string]any) []*catalogv1.ValidationError) {
	t.Helper()
	type change struct {
		Name, Base, Old, New, Field, Reason string
		TargetSpec                          map[string]any
	}
	var corpus struct {
		Valid          map[string]string
		Targets        map[string]string
		Negative       []change
		TargetNegative []change
	}
	raw, err := os.ReadFile(path)
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(raw, &corpus))
	for _, entry := range append(corpus.Negative, corpus.TargetNegative...) {
		t.Run(entry.Name, func(t *testing.T) {
			base, found := corpus.Valid[entry.Base]
			require.True(t, found)
			require.Contains(t, base, entry.Old)
			require.NotEmpty(t, entry.Reason, "each matrix row must pin its stable reason")
			content := []byte(strings.Replace(base, entry.Old, entry.New, 1))
			var source struct {
				Spec struct {
					TargetRef struct{ Kind string } `yaml:"targetRef"`
				}
			}
			require.NoError(t, yaml.Unmarshal([]byte(strings.SplitN(base, "\n---\n", 2)[0]), &source))
			var target struct{ Spec map[string]any }
			require.NoError(t, yaml.Unmarshal([]byte(strings.SplitN(corpus.Targets[source.Spec.TargetRef.Kind], "\n---\n", 2)[0]), &target))
			if entry.TargetSpec != nil {
				target.Spec = entry.TargetSpec
			}
			before, err := json.Marshal(target.Spec)
			require.NoError(t, err)
			original := bytes.Clone(content)
			findings := run(t, content, target.Spec)
			require.NotEmpty(t, findings)
			require.Equal(t, "translation/"+entry.Reason, findings[0].Constraint)
			require.True(t, strings.HasPrefix(findings[0].Field, entry.Field), "expected %s, got %s", entry.Field, findings[0].Field)
			require.Equal(t, "translations/de.md", findings[0].FilePath)
			diagnostics := admission.FromValidationErrors(findings)
			require.NotEqual(t, admission.ReasonValidationFailed, diagnostics[0].Reason)
			message := admission.FormatRejection(diagnostics)
			require.NotContains(t, message, "Tragbarer Computer")
			require.NotContains(t, message, "Portable computer")
			require.Equal(t, original, content)
			after, err := json.Marshal(target.Spec)
			require.NoError(t, err)
			require.Equal(t, before, after, "rejection cannot partially translate its target")
		})
	}
}
