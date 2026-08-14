// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package contract_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gitstore-dev/gitstore/controller-manager/internal/graphqlclient"
	"github.com/gitstore-dev/gitstore/controller-manager/internal/listwatch"
)

// productNodeJSON builds a products-query node fixture matching
// graphql_listwatcher.go's productNodeJSON shape.
func productNodeJSON(uid, name, namespace, rv, categoryRef string) map[string]any {
	spec := map[string]any{}
	if categoryRef != "" {
		spec["categoryRef"] = map[string]any{"name": categoryRef}
	} else {
		spec["categoryRef"] = nil
	}
	return map[string]any{
		"metadata": map[string]any{
			"uid":             uid,
			"name":            name,
			"namespace":       namespace,
			"resourceVersion": rv,
		},
		"spec": spec,
	}
}

// T008: ProductListWatcher.List enumerates namespaces, then paginates
// products per namespace, returning every Product and the highest
// observed resourceVersion across all namespaces.
func TestProductList_EnumeratesNamespacesThenPaginatesProducts(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Query     string         `json:"query"`
			Variables map[string]any `json:"variables"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		w.Header().Set("Content-Type", "application/json")

		switch {
		case strings.Contains(req.Query, "namespaces("):
			_, _ = w.Write([]byte(`{"data":{"namespaces":{"edges":[
				{"cursor":"n1","node":{"identifier":"acme"}}
			],"pageInfo":{"hasNextPage":false,"endCursor":"n1"}}}}`))
		case strings.Contains(req.Query, "products("):
			ns, _ := req.Variables["namespace"].(string)
			if ns != "acme" {
				t.Errorf("unexpected namespace variable: %q", ns)
			}
			resp := map[string]any{
				"data": map[string]any{
					"products": map[string]any{
						"edges": []any{
							map[string]any{"cursor": "p1", "node": productNodeJSON("uid-1", "widget", "acme", "5", "electronics")},
						},
						"pageInfo": map[string]any{"hasNextPage": false, "endCursor": "p1"},
					},
				},
			}
			_ = json.NewEncoder(w).Encode(resp)
		default:
			t.Errorf("unexpected query: %s", req.Query)
		}
	}))
	defer srv.Close()

	client := graphqlclient.New(srv.URL, "test-token")
	lw := listwatch.NewProductListWatcher(client)

	resp, err := lw.List(context.Background())
	if err != nil {
		t.Fatalf("List failed: %v", err)
	}
	if len(resp.Items) != 1 {
		t.Fatalf("len(Items) = %d, want 1", len(resp.Items))
	}
	if resp.Items[0].Name != "widget" || resp.Items[0].CategoryRefName != "electronics" {
		t.Errorf("unexpected item: %+v", resp.Items[0])
	}
	if resp.ResourceVersion != "5" {
		t.Errorf("ResourceVersion = %q, want %q", resp.ResourceVersion, "5")
	}
}

func TestProductList_EmptyDatasetReturnsNonEmptySentinelResourceVersion(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Query string `json:"query"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(req.Query, "namespaces(") {
			_, _ = w.Write([]byte(`{"data":{"namespaces":{"edges":[],"pageInfo":{"hasNextPage":false,"endCursor":null}}}}`))
			return
		}
		t.Errorf("unexpected query: %s", req.Query)
	}))
	defer srv.Close()

	client := graphqlclient.New(srv.URL, "test-token")
	lw := listwatch.NewProductListWatcher(client)

	resp, err := lw.List(context.Background())
	if err != nil {
		t.Fatalf("List failed: %v", err)
	}
	if len(resp.Items) != 0 {
		t.Errorf("len(Items) = %d, want 0", len(resp.Items))
	}
	if resp.ResourceVersion == "" {
		t.Error("ResourceVersion must not be empty even for a zero-item list")
	}
}
