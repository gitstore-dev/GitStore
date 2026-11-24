// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package validate

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/gitstore-dev/gitstore/translation"
	"github.com/santhosh-tekuri/jsonschema/v5"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

func translationCorpus(t *testing.T) (map[string]string, []struct {
	Name, Base, Old, New, Field string
}) {
	t.Helper()
	data, err := os.ReadFile("../cataloggrpc/testdata/translation/corpus.json")
	require.NoError(t, err)
	var corpus struct {
		Valid    map[string]string
		Negative []struct{ Name, Base, Old, New, Field string }
	}
	require.NoError(t, json.Unmarshal(data, &corpus))
	return corpus.Valid, corpus.Negative
}

func TestTranslationSourcePreservation(t *testing.T) {
	corpus, _ := translationCorpus(t)
	for name, source := range corpus {
		t.Run(name, func(t *testing.T) {
			parsed, body, err := NewParser().ParseResource(strings.NewReader(source))
			require.NoError(t, err)
			require.NotNil(t, parsed)
			require.NotNil(t, parsed.Translation)
			require.Equal(t, "Translation", parsed.Kind)
			delim := "\n---\n"
			if strings.Contains(source, "\r\n") {
				delim = "\r\n---\r\n"
			}
			_, expected, ok := strings.Cut(source, delim)
			require.True(t, ok)
			require.Equal(t, []byte(expected), body)
			require.Equal(t, "acme-store", parsed.Translation.Metadata.Namespace)
			var authored struct {
				Spec struct {
					Locale string `yaml:"locale"`
				} `yaml:"spec"`
			}
			frontmatter, _, err := translationEnvelope([]byte(source))
			require.NoError(t, err)
			require.NoError(t, yaml.Unmarshal(frontmatter, &authored))
			require.Equal(t, authored.Spec.Locale, parsed.Translation.Spec.Locale)
		})
	}
}

func TestTranslationStrictSourceRejection(t *testing.T) {
	corpus, negative := translationCorpus(t)
	for _, change := range negative {
		if change.Name == "missing-selector" {
			continue // Requires an available target, unlike descriptor eligibility.
		}
		t.Run(change.Name, func(t *testing.T) {
			source := strings.Replace(corpus[change.Base], change.Old, change.New, 1)
			parsed, _, err := NewParser().ParseResource(strings.NewReader(source))
			require.Error(t, err)
			require.Nil(t, parsed)
			require.NotContains(t, err.Error(), "Tragbarer Computer")
		})
	}
}

func TestTranslationYAMLAndFramingEdges(t *testing.T) {
	corpus, _ := translationCorpus(t)
	base := corpus["product-field"]
	for name, source := range map[string][]byte{
		"invalid-utf8":        append([]byte(base), 0xff),
		"alias":               []byte(strings.Replace(base, "  locale: de-DE", "  locale: &locale de-DE\n  patches: *locale", 1)),
		"merge":               []byte(strings.Replace(base, "spec:\n", "spec:\n  <<: {locale: de-DE}\n", 1)),
		"null-patches":        []byte(strings.Replace(base, "  patches:\n  - fieldPath: spec.title\n    translation: Tragbarer Computer\n", "  patches: null\n", 1)),
		"null-namespace":      []byte(strings.Replace(base, "  namespace: acme-store", "  namespace: null", 1)),
		"unknown-reference":   []byte(strings.Replace(base, "    name: laptop", "    name: laptop\n    extra: value", 1)),
		"missing-translation": []byte(strings.Replace(base, "    translation: Tragbarer Computer\n", "", 1)),
		"empty-pin":           []byte(strings.Replace(base, "    name: laptop", "    name: laptop\n    uid: \"\"", 1)),
		"oversize":            append([]byte(base), bytes.Repeat([]byte("x"), 1<<20)...),
	} {
		t.Run(name, func(t *testing.T) {
			parsed, _, err := NewParser().ParseResource(bytes.NewReader(source))
			require.Error(t, err)
			require.Nil(t, parsed)
		})
	}
	literal := strings.Replace(base, "translation: Tragbarer Computer", "translation: \"before---after\"", 1)
	parsed, body, err := NewParser().ParseResource(strings.NewReader(literal))
	require.NoError(t, err, "inline delimiter text is not the closing delimiter line")
	require.Equal(t, "before---after", parsed.Translation.Spec.Patches[0].Translation)
	require.Empty(t, body)
	optional := strings.Replace(base, "fieldPath: spec.title", "fieldPath: spec.options{color}.title", 1)
	optional = strings.Replace(optional, "translation: Tragbarer Computer", "translation: \"\"", 1)
	parsed, _, err = NewParser().ParseResource(strings.NewReader(optional))
	require.NoError(t, err, "an explicit empty optional title differs from missing or null text")
	require.Empty(t, parsed.Translation.Spec.Patches[0].Translation)
}

func TestTranslationBodyDelimiterEdges(t *testing.T) {
	corpus, _ := translationCorpus(t)
	base := corpus["product-field"]
	require.True(t, strings.HasSuffix(base, "---\n"))
	for name, body := range map[string]string{
		"blank-lines":        "\n\ntext\n",
		"later-delimiter":    "---\nordinary body\n---",
		"mixed-line-endings": "\r\nbody\nnext\r\n",
		"unicode":            "\u00e9\u65e5\n",
	} {
		t.Run(name, func(t *testing.T) {
			parsed, actual, err := NewParser().ParseResource(strings.NewReader(base + body))
			require.NoError(t, err)
			require.NotNil(t, parsed.Translation)
			require.Equal(t, []byte(body), actual)
		})
	}
	_, body, err := NewParser().ParseResource(strings.NewReader(strings.TrimSuffix(base, "\n")))
	require.NoError(t, err)
	require.Empty(t, body, "a closing delimiter at EOF has no suffix")
	_, _, err = NewParser().ParseResource(strings.NewReader(strings.TrimSuffix(base, "---\n")))
	require.Error(t, err, "frontmatter must have a closing delimiter")
}

func TestTranslationSourceLimitStopsReading(t *testing.T) {
	corpus, _ := translationCorpus(t)
	prefix := corpus["product-field"]
	limited := prefix + strings.Repeat("x", translation.MaxDocumentBytes+1-len(prefix))
	reader := io.MultiReader(strings.NewReader(limited), unreadTranslationTail{})
	_, _, err := NewParser().ParseResource(reader)
	var issue *translation.Error
	require.ErrorAs(t, err, &issue)
	require.Equal(t, "LimitExceeded", issue.Reason)
}

func TestTranslationYAMLWorkLimits(t *testing.T) {
	corpus, _ := translationCorpus(t)
	base := corpus["product-field"]
	patches := "  patches:\n  - fieldPath: spec.title\n    translation: Tragbarer Computer\n"
	for name, replacement := range map[string]string{
		"node-count": "  patches:\n" + strings.Repeat("  - x\n", 65_536),
		"depth":      "  patches: " + strings.Repeat("[", 65) + "x" + strings.Repeat("]", 65) + "\n",
	} {
		t.Run(name, func(t *testing.T) {
			source := strings.Replace(base, patches, replacement, 1)
			require.NotEqual(t, base, source)
			_, _, err := NewParser().ParseResource(strings.NewReader(source))
			var issue *translation.Error
			require.ErrorAs(t, err, &issue)
			require.Equal(t, "LimitExceeded", issue.Reason)
		})
	}
}

func TestTranslationAuthoringSchema(t *testing.T) {
	const schemaURL = "https://schemas.gitstore.dev/i18n.gitstore.dev/v1beta1/translation.schema.json"
	data, err := os.ReadFile("../../../schemas/i18n.gitstore.dev/v1beta1/translation.schema.json")
	require.NoError(t, err)
	compiler := jsonschema.NewCompiler()
	require.NoError(t, compiler.AddResource(schemaURL, bytes.NewReader(data)))
	schema, err := compiler.Compile(schemaURL)
	require.NoError(t, err)
	corpus, _ := translationCorpus(t)
	for name, source := range corpus {
		t.Run(name, func(t *testing.T) {
			frontmatter, _, err := translationEnvelope([]byte(source))
			require.NoError(t, err)
			var document map[string]any
			require.NoError(t, yaml.Unmarshal(frontmatter, &document))
			require.NoError(t, schema.Validate(document))
			for _, mutate := range []func(map[string]any){
				func(d map[string]any) { d["status"] = map[string]any{} },
				func(d map[string]any) { d["spec"].(map[string]any)["locale"] = nil },
				func(d map[string]any) { d["spec"].(map[string]any)["patches"] = nil },
				func(d map[string]any) { d["spec"].(map[string]any)["freshness"] = "CURRENT" },
				func(d map[string]any) { d["metadata"].(map[string]any)["uid"] = "authored" },
				func(d map[string]any) {
					d["spec"].(map[string]any)["targetRef"].(map[string]any)["fieldPath"] = "spec.title"
				},
			} {
				require.NoError(t, yaml.Unmarshal(frontmatter, &document))
				mutate(document)
				require.Error(t, schema.Validate(document))
				document = nil
			}
		})
	}
}

type unreadTranslationTail struct{}

func (unreadTranslationTail) Read([]byte) (int, error) {
	return 0, errors.New("Translation parser read beyond its source budget")
}
