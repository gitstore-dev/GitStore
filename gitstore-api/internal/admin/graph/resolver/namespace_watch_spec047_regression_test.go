// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package resolver

import (
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNamespaceResolversDoNotPublishProcessLocalWatchEvents(t *testing.T) {
	for _, path := range []string{"namespace.resolvers.go", "status_generic.go"} {
		raw, err := os.ReadFile(path)
		require.NoError(t, err)
		source := string(raw)
		assert.NotContains(t, source, "publishNamespaceEvent(", path)
		assert.NotContains(t, source, "publishNamespaceStatusEvent(", path)
		assert.NotContains(t, source, "publishNamespaceDeletedEvent(", path)
	}
}

func TestNamespaceWatchDoesNotReopenSpec047MutationContract(t *testing.T) {
	raw, err := os.ReadFile("namespace.resolvers.go")
	require.NoError(t, err)
	source := string(raw)
	for _, call := range []string{"service.CreateNamespace", "service.UpdateNamespace", "service.DeleteNamespace", "service.CompleteNamespaceDeletion"} {
		assert.True(t, strings.Contains(source, call), "shipped spec-047 service boundary %s must remain", call)
	}
}
