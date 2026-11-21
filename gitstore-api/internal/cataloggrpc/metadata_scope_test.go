// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package cataloggrpc_test

import (
	"fmt"
	"testing"

	"github.com/gitstore-dev/gitstore/api/internal/admission"
	"github.com/stretchr/testify/require"
)

func TestManifestNamespaceScopeDiagnostics(t *testing.T) {
	server := newCatalogServer(t, newTestDatastore(t), nil)
	for _, kind := range []string{"Product", "CategoryTaxonomy", "Collection", "ProductVariant", "File", "Repository", "Namespace"} {
		values := []string{"", "  namespace: null\n", "  namespace: \"\"\n", "  namespace: \" \\t\"\n"}
		reason := admission.ReasonNamespaceRequired
		if kind == "Namespace" {
			values = []string{"  namespace: \"\"\n", "  namespace: \"  \"\n", "  namespace: acme\n"}
			reason = admission.ReasonNamespaceForbidden
		}
		for _, value := range values {
			t.Run(kind+"/"+value, func(t *testing.T) {
				content := []byte(fmt.Sprintf("---\napiVersion: gitstore.dev/v1beta1\nkind: %s\nmetadata:\n  name: test\n%sspec: {}\n---\n", kind, value))
				diagnostics, err := server.ValidateManifest(t.Context(), admission.ManifestValidationRequest{RepositoryID: testRepoID, Path: "test.md", NewContent: content})
				require.NoError(t, err)
				require.Len(t, diagnostics, 1)
				require.Equal(t, reason, diagnostics[0].Reason)
				require.Equal(t, "metadata.namespace", diagnostics[0].Field)
				require.Equal(t, "test.md", diagnostics[0].File)
				require.Equal(t, admission.LevelFailure, diagnostics[0].Level)
			})
		}
	}
}
