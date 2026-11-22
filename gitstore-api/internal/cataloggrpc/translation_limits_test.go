// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package cataloggrpc

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	catalogv1 "github.com/gitstore-dev/gitstore/api/gen/gitstore/catalog/v1"
	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	"github.com/gitstore-dev/gitstore/api/internal/validate"
	"github.com/gitstore-dev/gitstore/translation"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"gopkg.in/yaml.v3"
)

func translationLimitSource(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile("testdata/translation/corpus.json")
	require.NoError(t, err)
	var corpus struct{ Valid map[string]string }
	require.NoError(t, json.Unmarshal(raw, &corpus))
	return corpus.Valid["product-field"]
}

func TestTranslationManifestSourceBudgets(t *testing.T) {
	base := translationLimitSource(t)
	var patches strings.Builder
	patches.WriteString("  patches:\n")
	for i := range translation.MaxPatches {
		fmt.Fprintf(&patches, "  - fieldPath: spec.options{option-%d}.title\n    translation: Text\n", i)
	}
	old := "  patches:\n  - fieldPath: spec.title\n    translation: Tragbarer Computer\n"
	atPatchLimit := strings.Replace(base, old, patches.String(), 1)
	atByteLimit := base + strings.Repeat("x", translation.MaxDocumentBytes-len(base))
	for _, test := range []struct {
		name, source, reason string
	}{
		{"byte-limit", atByteLimit, ""},
		{"over-byte-limit", atByteLimit + "x", "LimitExceeded"},
		{"patch-limit", atPatchLimit, ""},
		{"over-patch-limit", strings.TrimSuffix(atPatchLimit, "---\n") + "  - fieldPath: spec.title\n    translation: Text\n---\n", "LimitExceeded"},
		{"nodes", strings.Replace(base, old, "  patches:\n"+strings.Repeat("  - x\n", 65_536), 1), "LimitExceeded"},
		{"depth", strings.Replace(base, "spec:\n", "spec:\n  extra: "+strings.Repeat("[", 65)+"0"+strings.Repeat("]", 65)+"\n", 1), "LimitExceeded"},
		{"path-byte-limit", strings.Replace(base, "spec.title", "spec."+strings.Repeat("a", 1_019), 1), "UnsupportedField"},
		{"over-path-byte-limit", strings.Replace(base, "spec.title", "spec."+strings.Repeat("a", 1_020), 1), "InvalidPath"},
		{"segment-limit", strings.Replace(base, "spec.title", "spec."+strings.Repeat("a.", 15)+"a", 1), "UnsupportedField"},
		{"over-segment-limit", strings.Replace(base, "spec.title", "spec."+strings.Repeat("a.", 16)+"a", 1), "InvalidPath"},
		{"selector-limit", strings.Replace(base, "spec.title", "spec."+strings.Repeat("a{id}.", 7)+"a{id}", 1), "UnsupportedField"},
		{"over-selector-limit", strings.Replace(base, "spec.title", "spec."+strings.Repeat("a{id}.", 8)+"a{id}", 1), "InvalidPath"},
	} {
		t.Run(test.name, func(t *testing.T) {
			raw := []byte(test.source)
			before := bytes.Clone(raw)
			parsed, body, result, err := validateTranslationManifestSnapshot(t.Context(), validate.NewParser(), raw, "acme-store", nil)
			if test.reason == "" {
				require.NoError(t, err)
				require.NotNil(t, parsed)
				require.False(t, result.TargetResolved)
			} else {
				var issue *translation.Error
				require.ErrorAs(t, err, &issue)
				require.Equal(t, test.reason, issue.Reason)
				require.Nil(t, parsed)
				require.Nil(t, body)
				require.Nil(t, result)
			}
			require.Equal(t, before, raw)
		})
	}
}

func TestTranslationManifestTargetWorkBudgets(t *testing.T) {
	source := []byte(translationLimitSource(t))
	var nested any = "leaf"
	for range translation.MaxTargetDepth + 1 {
		nested = map[string]any{"nested": nested}
	}
	repeated := make([]any, 101)
	unit := strings.Repeat("x", 1<<20)
	for i := range repeated {
		repeated[i] = unit
	}
	for name, work := range map[string]any{
		"nodes": make([]any, translation.MaxTargetNodes),
		"depth": nested,
		"bytes": repeated,
	} {
		t.Run(name, func(t *testing.T) {
			target := &validate.TranslationTargetSnapshot{
				APIVersion: "catalog.gitstore.dev/v1beta1", Kind: "Product", Namespace: "acme-store", Name: "laptop",
				Spec: map[string]any{"title": "Original", "operational": work},
			}
			parsed, body, result, err := validateTranslationManifestSnapshot(t.Context(), validate.NewParser(), source, "acme-store", target)
			var issue *translation.Error
			require.ErrorAs(t, err, &issue)
			require.Equal(t, "TargetWorkUnavailable", issue.Reason)
			require.Nil(t, parsed)
			require.Nil(t, body)
			require.Nil(t, result, "work exhaustion is not absent-target or partial success")
			require.Equal(t, "Original", target.Spec["title"])
		})
	}
}

func TestTranslationManifestYAMLWorkBoundaries(t *testing.T) {
	base := translationLimitSource(t)
	base = strings.Replace(base, "    translation: Tragbarer Computer\n",
		"    translation: Tragbarer Computer\n  - fieldPath: spec.options{color}.title\n    translation: Farbe\n", 1)
	var document yaml.Node
	require.NoError(t, yaml.Unmarshal([]byte(strings.Split(base, "---\n")[1]), &document))
	var nodes func(*yaml.Node) int
	nodes = func(node *yaml.Node) int {
		count := 1
		for _, child := range node.Content {
			count += nodes(child)
		}
		return count
	}
	var annotations strings.Builder
	annotations.WriteString("  annotations:\n")
	for i := 0; i < (65_536-nodes(&document)-2)/2; i++ {
		fmt.Fprintf(&annotations, "    note%d: value\n", i)
	}
	atNodes := strings.Replace(base, "metadata:\n", "metadata:\n"+annotations.String(), 1)
	require.NoError(t, yaml.Unmarshal([]byte(strings.Split(atNodes, "---\n")[1]), &document))
	require.Equal(t, 65_536, nodes(&document), "the fixture must exercise the exact YAML node ceiling")
	for _, test := range []struct {
		name, source, reason string
	}{
		{"at-nodes", atNodes, ""},
		{"over-nodes", strings.Replace(atNodes, "  annotations:\n", "  annotations:\n    extra: value\n", 1), "LimitExceeded"},
		{"at-depth", strings.Replace(base, "spec:\n", "spec:\n  extra: "+strings.Repeat("[", 61)+"0"+strings.Repeat("]", 61)+"\n", 1), "UnknownField"},
		{"over-depth", strings.Replace(base, "spec:\n", "spec:\n  extra: "+strings.Repeat("[", 62)+"0"+strings.Repeat("]", 62)+"\n", 1), "LimitExceeded"},
	} {
		t.Run(test.name, func(t *testing.T) {
			parsed, body, result, err := validateTranslationManifestSnapshot(t.Context(), validate.NewParser(), []byte(test.source), "acme-store", nil)
			if test.reason == "" {
				require.NoError(t, err)
				require.NotNil(t, parsed)
				require.NotNil(t, result)
				return
			}
			var issue *translation.Error
			require.ErrorAs(t, err, &issue)
			require.Equal(t, test.reason, issue.Reason)
			require.Nil(t, parsed)
			require.Nil(t, body)
			require.Nil(t, result)
		})
	}
}

type cancelingTranslationParser struct {
	calls       int
	cancelAfter int
	cancel      context.CancelFunc
	fail        error
}

func (p *cancelingTranslationParser) ParseResource(r io.Reader) (*validate.ParsedResource, []byte, error) {
	p.calls++
	if p.cancel != nil && (p.cancelAfter == 0 || p.calls == p.cancelAfter) {
		p.cancel()
	}
	if p.fail != nil {
		return nil, nil, p.fail
	}
	return validate.NewParser().ParseResource(r)
}

func TestTranslationManifestDiagnosticBudgets(t *testing.T) {
	var issues []error
	for i := range 40 {
		issues = append(issues, &translation.Error{
			Field: fmt.Sprintf("spec.patches[%d].fieldPath", i), Reason: "UnsupportedField",
			Message: strings.Repeat("\u00e9", 200),
		})
	}
	parser := &cancelingTranslationParser{fail: errors.Join(issues...)}
	server := &Server{parser: parser, log: zap.NewNop()}
	findings := server.validateResourceBlobs(t.Context(), "", []*catalogv1.ResourceBlob{{
		Path: "translation.md", Content: []byte(translationLimitSource(t)),
	}})
	require.Len(t, findings, 32)
	for i, finding := range findings {
		require.Equal(t, fmt.Sprintf("spec.patches[%d].fieldPath", i), finding.Field)
		require.LessOrEqual(t, len(finding.Message), 256)
		require.True(t, utf8.ValidString(finding.Message))
	}
	require.Contains(t, findings[31].Message, "additional issues omitted")
	require.NotContains(t, findings[30].Message, "additional issues omitted")
}

func TestTranslationValidationStopsBetweenBlobsOnCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	parser := &cancelingTranslationParser{cancel: cancel}
	server := &Server{parser: parser, log: zap.NewNop(), translationActivation: TranslationActivation{Enabled: true}}
	content := []byte(translationLimitSource(t))
	response, err := server.validateResources(ctx, &catalogv1.ValidateResourcesRequest{Trees: []*catalogv1.ResourceValidationTree{{
		ProposedBlobs: []*catalogv1.ResourceBlob{{Path: "one.md", Content: content}, {Path: "two.md", Content: content}},
	}}})
	require.Equal(t, codes.Canceled, status.Code(err))
	require.Nil(t, response)
	require.Equal(t, 1, parser.calls, "cancellation must prevent parsing subsequent blobs")
}

func TestTranslationCandidateParsingHonorsCancellationWithoutPartialEntries(t *testing.T) {
	for _, mode := range []string{"before-parse", "between-blobs", "expired-deadline"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			parser := &cancelingTranslationParser{}
			expected, calls := error(context.Canceled), 0
			switch mode {
			case "before-parse":
				cancel()
			case "between-blobs":
				parser.cancel, calls = cancel, 1
			case "expired-deadline":
				var stop context.CancelFunc
				ctx, stop = context.WithDeadline(ctx, time.Now().Add(-time.Second))
				defer stop()
				expected = context.DeadlineExceeded
			}
			server := &Server{parser: parser}
			raw := []byte(translationLimitSource(t))
			entries, err := server.parseDeletionTreeEntries(ctx, []*catalogv1.ResourceBlob{
				{Path: "one.md", Content: raw}, {Path: "two.md", Content: raw},
			}, "acme-store")
			require.ErrorIs(t, err, expected)
			require.Nil(t, entries)
			require.Equal(t, calls, parser.calls, "canceled semantic reparsing must not continue through the candidate")
		})
	}
}

func TestTranslationPreflightCancellationAtEveryParseBoundary(t *testing.T) {
	raw, err := os.ReadFile("testdata/translation/corpus.json")
	require.NoError(t, err)
	var corpus struct{ Targets map[string]string }
	require.NoError(t, json.Unmarshal(raw, &corpus))
	run := func(t *testing.T, cancelAt int) int {
		t.Helper()
		f := newTranslationCallbackFixture(t)
		f.server.store = f.store.Datastore
		require.NoError(t, f.store.CreateProduct(f.ctx, &datastore.Product{
			APIVersion: "catalog.gitstore.dev/v1beta1", Kind: "Product", Namespace: "gitstore", Name: "laptop",
			UID: uuid.NewString(), ResourceVersion: "1", Generation: 1, CreationTimestamp: f.admCtx.Now,
			Spec: []byte(`{"Title":"Original","Options":[]}`),
		}))
		ctx, cancel := context.WithCancel(f.ctx)
		defer cancel()
		parser := &cancelingTranslationParser{cancelAfter: cancelAt}
		if cancelAt != 0 {
			parser.cancel = cancel
		}
		f.server.parser = parser
		response, err := f.server.validateResources(ctx, &catalogv1.ValidateResourcesRequest{
			RepositoryId: f.admCtx.RepositoryID,
			Trees: []*catalogv1.ResourceValidationTree{{ProposedBlobs: []*catalogv1.ResourceBlob{
				{Path: f.entry.path, Content: f.entry.parsed.RawSource},
				{Path: "products/laptop.md", Content: []byte(strings.ReplaceAll(corpus.Targets["Product"], "acme-store", "gitstore"))},
			}}},
		})
		if cancelAt == 0 {
			require.NoError(t, err)
			require.True(t, response.GetAccepted(), "%v", response.GetErrors())
		} else {
			require.Equal(t, codes.Canceled, status.Code(err))
			require.Nil(t, response, "no phase may publish an accepted or partially rejected result after cancellation")
			require.Equal(t, cancelAt, parser.calls, "later phases must not restart parsing after cancellation")
		}
		return parser.calls
	}
	parses := run(t, 0)
	require.Greater(t, parses, 1)
	for boundary := 1; boundary <= parses; boundary++ {
		t.Run(fmt.Sprintf("parse-%d", boundary), func(t *testing.T) { run(t, boundary) })
	}
}

func TestTranslationValidationExpiredDeadlinePrecedesSourceWork(t *testing.T) {
	ctx, cancel := context.WithDeadline(t.Context(), time.Now().Add(-time.Second))
	defer cancel()
	server := &Server{}
	response, err := server.validateResources(ctx, &catalogv1.ValidateResourcesRequest{Trees: []*catalogv1.ResourceValidationTree{{
		ProposedBlobs: []*catalogv1.ResourceBlob{{Path: "translation.md", Content: []byte(translationLimitSource(t))}},
	}}})
	require.Equal(t, codes.DeadlineExceeded, status.Code(err))
	require.Nil(t, response)
}
