// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package categorytaxonomy

import (
	"context"
	"fmt"

	"github.com/gitstore-dev/gitstore/controller-manager/internal/graphqlclient"
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
