// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package validate

import (
	"context"
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/gitstore-dev/gitstore/api/internal/catalog"
	"github.com/gitstore-dev/gitstore/translation"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestTranslationRawTargetRejectsDuplicateMembersBeforeNormalization(t *testing.T) {
	for _, raw := range []string{
		`{"Title":"First","Title":"Second"}`,
		`{"title":"First","ignored":{"name":"first","name":"second"}}`,
	} {
		fields, err := TranslationTargetFields(t.Context(), []byte(raw), reflect.TypeFor[catalog.ProductSpec]())
		require.Equal(t, codes.Unavailable, status.Code(err))
		require.Nil(t, fields, "duplicate members cannot acquire last-write-wins semantics")
		require.NotContains(t, err.Error(), "Second")
	}
}

func TestTranslationRawTargetValueBudgetPrecedesFullDecode(t *testing.T) {
	raw := `{"title":"Original","ignored":[` + strings.Repeat("null,", translation.MaxTargetNodes-4) + `null]}`
	fields, err := TranslationTargetFields(t.Context(), []byte(raw), reflect.TypeFor[catalog.ProductSpec]())
	require.NoError(t, err, "the exact value-node ceiling must remain accepted")
	require.Equal(t, "Original", fields["title"])
	for _, overBudget := range []string{
		`{"title":"Original","ignored":[` + strings.Repeat("null,", translation.MaxTargetNodes) + `BROKEN]}`,
		`{"ignored":` + strings.Repeat("[", translation.MaxTargetDepth) + `BROKEN` + strings.Repeat("]", translation.MaxTargetDepth) + `}`,
	} {
		fields, err = TranslationTargetFields(t.Context(), []byte(overBudget), reflect.TypeFor[catalog.ProductSpec]())
		require.Equal(t, codes.ResourceExhausted, status.Code(err), "preflight must stop at the work bound before decoding the invalid tail")
		require.Nil(t, fields)
	}
}

func TestTranslationSuppliedTargetSnapshot(t *testing.T) {
	corpus, _ := translationCorpus(t)
	parsed, body, err := NewParser().ParseResource(strings.NewReader(corpus["product-combined"]))
	require.NoError(t, err)
	target := TranslationTargetSnapshot{
		APIVersion: "catalog.gitstore.dev/v1beta1", Kind: "Product", Namespace: "acme-store", Name: "laptop",
		UID: "00000000-0000-4000-8000-000000000001", ResourceVersion: "7",
		Spec: map[string]any{"title": "Original", "options": []any{
			map[string]any{"name": "other", "title": "Other"},
			map[string]any{"name": "color", "title": "Color"},
		}},
	}
	before, err := json.Marshal(target)
	require.NoError(t, err)
	observation, err := ValidateTranslationSnapshot(t.Context(), parsed.Translation, body, "acme-store", &target)
	require.NoError(t, err)
	require.True(t, observation.TargetResolved)
	require.Equal(t, []string{"spec.title", "spec.options{color}.title", "{body}"}, observation.ValidatedPaths)
	after, err := json.Marshal(target)
	require.NoError(t, err)
	require.Equal(t, before, after, "validation must not translate or otherwise change the supplied target")
	require.Contains(t, string(body), "\r\n")

	unresolved, err := ValidateTranslationSnapshot(t.Context(), parsed.Translation, body, "acme-store", nil)
	require.NoError(t, err)
	require.False(t, unresolved.TargetResolved)
	require.Equal(t, "Missing", unresolved.Reason)
	require.Nil(t, unresolved.ValidatedPaths)

	parsed.Translation.Spec.Patches[1].FieldPath = "spec.options{absent}.title"
	rejected, err := ValidateTranslationSnapshot(t.Context(), parsed.Translation, body, "acme-store", &target)
	require.Error(t, err)
	require.Nil(t, rejected, "one invalid patch cannot leave a validated subset")
	parsed.Translation.Spec.Patches[1].FieldPath = "spec.tags"
	rejected, err = ValidateTranslationSnapshot(t.Context(), parsed.Translation, body, "acme-store", nil)
	require.Error(t, err, "descriptor-decidable failures are rejected even without a target")
	require.Nil(t, rejected)
}

func TestTranslationSnapshotScopeAndPins(t *testing.T) {
	corpus, _ := translationCorpus(t)
	for _, name := range []string{"namespace-default", "foreign-source", "foreign-reference", "wrong-snapshot-name",
		"wrong-snapshot-kind", "wrong-snapshot-version", "wrong-snapshot-namespace", "uid-mismatch", "version-mismatch",
		"uid-not-allocated", "version-not-allocated", "matching-pins"} {
		t.Run(name, func(t *testing.T) {
			parsed, body, err := NewParser().ParseResource(strings.NewReader(corpus["product-field"]))
			require.NoError(t, err)
			resource := parsed.Translation
			target := TranslationTargetSnapshot{
				APIVersion: "catalog.gitstore.dev/v1beta1", Kind: "Product", Namespace: "acme-store", Name: "laptop",
				UID: "00000000-0000-4000-8000-000000000001", ResourceVersion: "7", Spec: map[string]any{"title": "Original"},
			}
			switch name {
			case "namespace-default":
				resource.Metadata.Namespace = ""
			case "foreign-source":
				resource.Metadata.Namespace = "other"
			case "foreign-reference":
				resource.Spec.TargetRef.Namespace = "other"
			case "wrong-snapshot-name":
				target.Name = "other"
			case "wrong-snapshot-kind":
				target.Kind = "Collection"
			case "wrong-snapshot-version":
				target.APIVersion = "catalog.gitstore.dev/v999"
			case "wrong-snapshot-namespace":
				target.Namespace = "other"
			case "uid-mismatch":
				resource.Spec.TargetRef.UID = "00000000-0000-4000-8000-000000000002"
			case "version-mismatch":
				resource.Spec.TargetRef.ResourceVersion = "07"
			case "uid-not-allocated":
				resource.Spec.TargetRef.UID = target.UID
				target.UID = ""
			case "version-not-allocated":
				resource.Spec.TargetRef.ResourceVersion = target.ResourceVersion
				target.ResourceVersion = ""
			case "matching-pins":
				resource.Spec.TargetRef.UID = target.UID
				resource.Spec.TargetRef.ResourceVersion = target.ResourceVersion
			}
			result, err := ValidateTranslationSnapshot(t.Context(), resource, body, "acme-store", &target)
			switch name {
			case "namespace-default", "matching-pins":
				require.NoError(t, err)
				require.True(t, result.TargetResolved)
			case "uid-not-allocated", "version-not-allocated":
				require.NoError(t, err)
				require.False(t, result.TargetResolved)
				require.Equal(t, "PinUnavailable", result.Reason)
				require.Nil(t, result.ValidatedPaths)
			default:
				require.Error(t, err)
				require.Nil(t, result)
				require.NotContains(t, err.Error(), "Tragbarer Computer")
			}
		})
	}
}

func TestTranslationSnapshotCancellationAndTargetWork(t *testing.T) {
	corpus, _ := translationCorpus(t)
	parsed, body, err := NewParser().ParseResource(strings.NewReader(corpus["product-field"]))
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	result, err := ValidateTranslationSnapshot(ctx, parsed.Translation, body, "acme-store", nil)
	require.ErrorIs(t, err, context.Canceled)
	require.Nil(t, result)
	target := &TranslationTargetSnapshot{
		APIVersion: "catalog.gitstore.dev/v1beta1", Kind: "Product", Namespace: "acme-store", Name: "laptop",
		Spec: map[string]any{"title": "Original", "operational": make([]any, translation.MaxTargetNodes)},
	}
	result, err = ValidateTranslationSnapshot(t.Context(), parsed.Translation, body, "acme-store", target)
	var issue *translation.Error
	require.ErrorAs(t, err, &issue)
	require.Equal(t, "TargetWorkUnavailable", issue.Reason)
	require.Nil(t, result, "unavailable target work cannot be reported as missing or validated")
}

func TestTranslationDeferredCorpusRemainsUnsupported(t *testing.T) {
	raw, err := os.ReadFile("../cataloggrpc/testdata/translation/corpus.json")
	require.NoError(t, err)
	var corpus struct {
		Valid    map[string]string
		Deferred map[string]struct{ Base, Old, New, Reason string }
	}
	require.NoError(t, json.Unmarshal(raw, &corpus))
	for name, change := range corpus.Deferred {
		t.Run(name, func(t *testing.T) {
			source := strings.Replace(corpus.Valid[change.Base], change.Old, change.New, 1)
			parsed, _, err := NewParser().ParseResource(strings.NewReader(source))
			var issue *translation.Error
			require.ErrorAs(t, err, &issue)
			require.Nil(t, parsed)
			require.Equal(t, change.Reason, issue.Reason)
		})
	}
}
