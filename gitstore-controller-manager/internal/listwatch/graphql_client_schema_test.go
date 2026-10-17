// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package listwatch

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/gitstore-dev/gitstore/controller-manager/internal/cache"
	"github.com/gitstore-dev/gitstore/controller-manager/internal/categorytaxonomy"
	"github.com/gitstore-dev/gitstore/controller-manager/internal/product"
	"github.com/gitstore-dev/gitstore/controller-manager/internal/schemavalidate"
	"github.com/gitstore-dev/gitstore/controller-manager/internal/status"
	"github.com/gitstore-dev/gitstore/controller-manager/internal/types"
)

type observedProductStatusClient struct {
	patches []*status.StatusPatch
}

func (c *observedProductStatusClient) Apply(_ context.Context, _ types.WorkItemKey, patch *status.StatusPatch) error {
	c.patches = append(c.patches, patch)
	return nil
}

func TestProductWireStatusSettlesAndTracksCategoryPath(t *testing.T) {
	if !strings.Contains(productFields, "resolved { category { name uid path } }") {
		t.Fatal("both Product list and watch must request the complete resolved category path")
	}
	for _, pathJSON := range []string{"null", "[]", `["root","leaf"]`} {
		t.Run(pathJSON, func(t *testing.T) {
			var node productNodeJSON
			if err := json.Unmarshal([]byte(fmt.Sprintf(`{
				"metadata":{"uid":"product-id","namespace":"acme","name":"widget","resourceVersion":"7","generation":4},
				"spec":{"categoryRef":{"name":"leaf"}},
				"status":{"observedGeneration":4,"conditions":[
					{"type":"AdmissionAccepted","status":"TRUE","observedGeneration":4},
					{"type":"CategoryResolved","status":"TRUE","observedGeneration":4,
					 "reason":"CategoryFound","message":"spec.categoryRef resolves to an existing CategoryTaxonomy in this namespace."},
					{"type":"Ready","status":"TRUE","observedGeneration":4,
					 "reason":"ProductReady","message":"AdmissionAccepted and CategoryResolved are both True."}
				],"resolved":{"category":{"name":"leaf","uid":"category-id","path":%s}}}
			}`, pathJSON)), &node); err != nil {
				t.Fatal(err)
			}
			products := cache.New[categorytaxonomy.Product]()
			key := types.WorkItemKey{Kind: "Product", Namespace: "acme", Name: "widget"}
			products.Set(key, node.toProduct())
			categories := cache.New[categorytaxonomy.CategoryTaxonomy]()
			categoryKey := types.WorkItemKey{Kind: "CategoryTaxonomy", Namespace: "acme", Name: "leaf"}
			category := categorytaxonomy.CategoryTaxonomy{UID: "category-id", Namespace: "acme", Name: "leaf",
				Status: status.ResourceStatus{Resolved: json.RawMessage(`{"path":` + pathJSON + `}`)}}
			categories.Set(categoryKey, category)
			client := &observedProductStatusClient{}
			reconciler := product.NewReconciler(cache.AsReadOnly(products), cache.AsReadOnly(categories), client, nil)
			for range 5 {
				if _, ok := reconciler.Reconcile(t.Context(), key).(types.Success); !ok {
					t.Fatal("already reconciled Product did not settle")
				}
			}
			if len(client.patches) != 0 {
				t.Fatalf("wire status caused %d unnecessary writes", len(client.patches))
			}
			wantPath := []string{"anchor", "root", "leaf"}
			category.Status.Resolved = json.RawMessage(`{"path":["anchor","root","leaf"]}`)
			categories.Set(categoryKey, category)
			if _, ok := reconciler.Reconcile(t.Context(), key).(types.Success); !ok {
				t.Fatal("changed category path did not reconcile")
			}
			if len(client.patches) != 1 {
				t.Fatalf("changed category path caused %d writes, want one", len(client.patches))
			}
			var resolved struct {
				Category struct {
					Path []string `json:"path"`
				} `json:"category"`
			}
			if err := json.Unmarshal(client.patches[0].Resolved, &resolved); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(resolved.Category.Path, wantPath) {
				t.Fatalf("resolved path = %v, want %v", resolved.Category.Path, wantPath)
			}
			node.Metadata.ResourceVersion = "8"
			if err := json.Unmarshal(client.patches[0].Resolved, node.Status.Resolved); err != nil {
				t.Fatal(err)
			}
			products.Set(key, node.toProduct())
			for range 5 {
				reconciler.Reconcile(t.Context(), key)
			}
			if len(client.patches) != 1 {
				t.Fatal("Product resumed status writes after observing the updated path")
			}
		})
	}
}

// TestGraphQLOperationsMatchSchema validates every list/watch query,
// mutation and subscription string this package sends against the real
// schema (shared/schemas/*.graphqls), the same way gqlgen validates it
// server-side.
func TestGraphQLOperationsMatchSchema(t *testing.T) {
	schemavalidate.Validate(t, []schemavalidate.Operation{
		{Name: "categoriesListQuery", Query: categoriesListQuery},
		{Name: "watchCategoriesSubscription", Query: watchCategoriesSubscription},
		{Name: "namespacesListQuery", Query: namespacesListQuery},
		{Name: "productsListQueryByNamespace", Query: productsListQueryByNamespace},
		{Name: "watchProductsSubscription", Query: watchProductsSubscription},
		{Name: "namespacesControllerListQuery", Query: namespacesControllerListQuery},
		{Name: "watchNamespacesSubscription", Query: watchNamespacesSubscription},
		{Name: "repositoriesControllerListQuery", Query: repositoriesControllerListQuery},
		{Name: "watchRepositoriesSubscription", Query: watchRepositoriesSubscription},
	})
}
