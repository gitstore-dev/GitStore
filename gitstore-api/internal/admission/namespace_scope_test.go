// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package admission

import (
	"github.com/stretchr/testify/require"
	"testing"
)

func TestNamespaceScope(t *testing.T) {
	for _, kind := range []string{"Product", "CategoryTaxonomy", "Collection", "ProductVariant", "Repository", "File", "Namespace", "ServiceAccount"} {
		for _, value := range []*string{nil, new(""), new(" \t\n"), new("acme")} {
			t.Run(kind+"/"+namespaceTestLabel(value), func(t *testing.T) {
				cluster := kind == "Namespace" || kind == "ServiceAccount"
				err := ValidateNamespaceScope(kind, value)
				valid := cluster && value == nil || !cluster && value != nil && *value == "acme"
				if valid {
					require.Nil(t, err)
					return
				}
				require.NotNil(t, err)
				reason := ReasonNamespaceRequired
				if cluster {
					reason = ReasonNamespaceForbidden
				}
				require.Equal(t, reason, err.Reason())
				require.Equal(t, CodeBadUserInput, err.Code)
				require.Equal(t, "metadata.namespace", err.Diagnostics[0].Field)
				require.Equal(t, LevelFailure, err.Diagnostics[0].Level)
				require.Contains(t, err.Error(), kind)
			})
		}
	}
}

func namespaceTestLabel(value *string) string {
	if value == nil {
		return "null"
	}
	return *value
}
