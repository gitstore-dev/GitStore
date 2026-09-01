// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package integration

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestNamespaceWatchDocumentedConsumer executes the public
// bootstrap/list/drain/resume algorithm without relying on server internals.
func TestNamespaceWatchDocumentedConsumer(t *testing.T) {
	apiURL := strings.TrimSuffix(os.Getenv("NAMESPACE_WATCH_API_A"), "/")
	token := os.Getenv("NAMESPACE_WATCH_TOKEN")
	if apiURL == "" || token == "" {
		t.Skip("set NAMESPACE_WATCH_API_A and NAMESPACE_WATCH_TOKEN")
	}

	watch := openNamespaceWatch(t, apiURL, token, "__namespace_watch_bootstrap__")
	bookmark := readNamespaceWatchEvent(t, watch)
	require.Equal(t, "BOOKMARK", bookmark.Type)

	response := gqlQueryWithURL(t, apiURL, token, `
		query ListNamespaces($first: Int) {
			namespaces(first: $first) { edges { node { metadata { name } } } }
		}`, map[string]any{"first": 100})
	require.Empty(t, response.Errors, namespaceContractErrors(response.Errors))
	var snapshot struct {
		Namespaces struct {
			Edges []struct {
				Node struct {
					Metadata struct {
						Name string `json:"name"`
					} `json:"metadata"`
				} `json:"node"`
			} `json:"edges"`
		} `json:"namespaces"`
	}
	require.NoError(t, json.Unmarshal(response.Data, &snapshot))
	cache := make(map[string]bool, len(snapshot.Namespaces.Edges)+2)
	for _, edge := range snapshot.Namespaces.Edges {
		cache[edge.Node.Metadata.Name] = true
	}

	first := uniqueName("documented-consumer")
	createNamespaceThrough(t, apiURL, token, first)
	drained := readNamespaceWatchTransition(t, watch)
	require.Equal(t, "ADDED", drained.Type)
	require.Equal(t, first, drained.Name)
	cache[drained.Name] = true
	require.True(t, cache[first])
	_ = watch.Close()

	resumed := openNamespaceWatch(t, apiURL, token, drained.ResourceVersion)
	second := uniqueName("documented-resume")
	createNamespaceThrough(t, apiURL, token, second)
	next := readNamespaceWatchTransition(t, resumed)
	require.Equal(t, second, next.Name)
	require.NotEqual(t, drained.ResourceVersion, next.ResourceVersion)
}
