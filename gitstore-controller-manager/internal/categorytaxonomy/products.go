// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package categorytaxonomy

import (
	"context"
	"fmt"

	"github.com/gitstore-dev/gitstore/controller-manager/internal/cache"
	"github.com/gitstore-dev/gitstore/controller-manager/internal/graphqlclient"
	"github.com/gitstore-dev/gitstore/controller-manager/internal/types"
)

const productsListQuery = `
query($namespace: String!, $after: String) {
  products(namespace: $namespace, first: 100, after: $after) {
    edges {
      cursor
      node { spec { categoryRef { name } } }
    }
    pageInfo { hasNextPage endCursor }
  }
}`

type productsListResponse struct {
	Products struct {
		Edges []struct {
			Node struct {
				Spec struct {
					CategoryRef *struct {
						Name string `json:"name"`
					} `json:"categoryRef"`
				} `json:"spec"`
			} `json:"node"`
		} `json:"edges"`
		PageInfo struct {
			HasNextPage bool    `json:"hasNextPage"`
			EndCursor   *string `json:"endCursor"`
		} `json:"pageInfo"`
	} `json:"products"`
}

// Product is the cache entity type populated by ProductListWatcher's
// list-then-watch loop against watchProducts/products (spec 042). Carries
// only the fields the Product-cache-to-CategoryTaxonomy-enqueue glue needs
// (data-model.md) — not the full product resource.
type Product struct {
	UID             string
	Namespace       string
	Name            string
	ResourceVersion string
	// CategoryRefName is empty when the product has no category reference.
	// Mirrors spec.categoryRef.name.
	CategoryRefName string
}

// NewProductCounter returns a ProductCounter that paginates the existing
// products query for namespace and counts entries whose spec.categoryRef.name
// equals name, client-side (research.md R4 — no new gitstore-api schema
// surface for this spec).
func NewProductCounter(client *graphqlclient.Client) ProductCounter {
	return func(ctx context.Context, namespace, name string) (int64, error) {
		var count int64
		var after *string
		for {
			var resp productsListResponse
			vars := map[string]any{"namespace": namespace}
			if after != nil {
				vars["after"] = *after
			}
			if err := client.Query(ctx, productsListQuery, vars, &resp); err != nil {
				return 0, fmt.Errorf("categorytaxonomy: list products: %w", err)
			}
			for _, edge := range resp.Products.Edges {
				if edge.Node.Spec.CategoryRef != nil && edge.Node.Spec.CategoryRef.Name == name {
					count++
				}
			}
			if !resp.Products.PageInfo.HasNextPage || resp.Products.PageInfo.EndCursor == nil {
				break
			}
			after = resp.Products.PageInfo.EndCursor
		}
		return count, nil
	}
}

// NewProductCategoryEnqueueHandler returns a cache.EventHandler[Product]
// that translates Product cache changes into CategoryTaxonomy enqueues via
// enqueueCategory, so a Product add/delete/categoryRef-change re-triggers
// the CategoryTaxonomy reconciler's productCount computation for every
// affected category (research.md R1/R2). enqueueCategory is a no-op for an
// empty categoryName (a product with no categoryRef affects no category).
func NewProductCategoryEnqueueHandler(enqueueCategory func(namespace, categoryName string)) cache.EventHandler[Product] {
	enqueue := func(namespace, categoryName string) {
		if categoryName == "" {
			return
		}
		enqueueCategory(namespace, categoryName)
	}
	return cache.EventHandler[Product]{
		OnAdd: func(_ types.WorkItemKey, p Product) {
			enqueue(p.Namespace, p.CategoryRefName)
		},
		OnUpdate: func(_ types.WorkItemKey, old, current Product) {
			if old.CategoryRefName != current.CategoryRefName {
				enqueue(old.Namespace, old.CategoryRefName)
				enqueue(current.Namespace, current.CategoryRefName)
			}
		},
		OnDelete: func(_ types.WorkItemKey, p Product) {
			enqueue(p.Namespace, p.CategoryRefName)
		},
	}
}
