// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package validate_test

import (
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/gitstore-dev/gitstore/api/internal/validate"
	"github.com/stretchr/testify/require"
)

func TestRepositoryNamespaceParsing(t *testing.T) {
	for _, tt := range []struct{ kind, group, spec string }{
		{"Product", "catalog", "{title: Product}"},
		{"CategoryTaxonomy", "catalog", "{title: Category, parentRef: {name: root}}"},
		{"Collection", "catalog", "{title: Collection}"},
		{"ProductVariant", "catalog", "{title: Variant, sku: abc, productRef: {name: product}}"},
		{"File", "storage", "{contentType: image/png, provider: git, source: {uri: 'git:///test.png'}}"},
		{"Repository", "", "{defaultBranch: main}"},
		{"Translation", "i18n", "{locale: de, targetRef: {apiVersion: catalog.gitstore.dev/v1beta1, kind: Product, name: product}}"},
	} {
		t.Run(tt.kind, func(t *testing.T) {
			group := tt.group
			if group != "" {
				group += "."
			}
			template := "---\napiVersion: " + group + "gitstore.dev/v1beta1\nkind: " + tt.kind + "\nmetadata:\n  name: sample\n%sspec: " + tt.spec + "\n---\n\nUnchanged description.\n"
			parser := validate.NewParser().WithRepositoryNamespace(func() (string, error) { return "store-a", nil })
			for _, authored := range []string{"", "  namespace: store-a\n"} {
				source := fmt.Sprintf(template, authored)
				parsed, body, err := parser.ParseResource(strings.NewReader(source))
				require.NoError(t, err)
				resource := reflect.ValueOf(parsed).Elem().FieldByName(tt.kind)
				if tt.kind == "CategoryTaxonomy" {
					resource = reflect.ValueOf(parsed.CategoryTaxonomy)
				}
				require.Equal(t, "store-a", resource.Elem().FieldByName("Metadata").FieldByName("Namespace").String())
				require.Equal(t, "\nUnchanged description.\n", string(body))
				if tt.kind == "Translation" {
					require.Equal(t, source, string(parsed.RawSource))
				}
			}
			for _, authored := range []string{"null", "\"\"", "\" \\t\"", "23", "store-b"} {
				_, _, err := parser.ParseResource(strings.NewReader(fmt.Sprintf(template, "  namespace: "+authored+"\n")))
				require.Error(t, err, authored)
			}
			_, _, err := validate.NewParser().WithRepositoryNamespace(func() (string, error) {
				return "", errors.New("repository unavailable")
			}).ParseResource(strings.NewReader(fmt.Sprintf(template, "")))
			require.ErrorContains(t, err, "repository unavailable")
		})
	}
}

func TestRepositoryNamespaceDoesNotRelaxClusterScopeOrCrossNamespaceReferences(t *testing.T) {
	parser := validate.NewParser().WithRepositoryNamespace(func() (string, error) {
		t.Fatal("cluster manifests must not resolve a namespace")
		return "", nil
	})
	for _, ns := range []string{"", "  namespace: null\n"} {
		parsed, _, err := parser.ParseResource(strings.NewReader("---\napiVersion: gitstore.dev/v1beta1\nkind: Namespace\nmetadata:\n  name: demo\n" + ns + "spec: {tier: Organization}\n---\n"))
		require.NoError(t, err)
		require.Empty(t, parsed.Namespace.Metadata.Namespace)
	}
	_, _, err := parser.ParseResource(strings.NewReader("---\napiVersion: gitstore.dev/v1beta1\nkind: Namespace\nmetadata: {name: demo, namespace: store-a}\nspec: {tier: Organization}\n---\n"))
	require.Error(t, err)
	parser = validate.NewParser().WithRepositoryNamespace(func() (string, error) { return "store-a", nil })
	_, _, err = parser.ParseResource(strings.NewReader("---\napiVersion: i18n.gitstore.dev/v1beta1\nkind: Translation\nmetadata: {name: sample}\nspec: {locale: de, targetRef: {apiVersion: catalog.gitstore.dev/v1beta1, kind: Product, namespace: store-b, name: product}}\n---\nDescription\n"))
	require.Error(t, err)
}
