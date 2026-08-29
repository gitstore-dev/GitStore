// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package categorytaxonomy

import (
	"context"
	"fmt"

	"github.com/gitstore-dev/gitstore/controller-manager/internal/graphqlclient"
	"github.com/gitstore-dev/gitstore/controller-manager/internal/types"
)

const categoryDeletionStatusMutation = `
mutation($input: UpdateCategoryStatusInput!) {
  updateCategoryStatus(input: $input) {
    conflict { currentResourceVersion }
    hasMoreProductDependents
  }
}`

// DeletionClient invokes lifecycle operations through the existing
// updateCategoryStatus subresource mutation. No parallel CategoryTaxonomy
// GraphQL mutation is introduced.
type DeletionClient interface {
	DecoupleProducts(ctx context.Context, namespace, name, resourceVersion string) (bool, error)
	CompleteDeletion(ctx context.Context, namespace, name, resourceVersion string) error
}

type graphqlDeletionClient struct {
	client *graphqlclient.Client
}

func NewGraphQLDeletionClient(client *graphqlclient.Client) DeletionClient {
	return &graphqlDeletionClient{client: client}
}

func (c *graphqlDeletionClient) DecoupleProducts(ctx context.Context, namespace, name, resourceVersion string) (bool, error) {
	var response struct {
		UpdateCategoryStatus struct {
			Conflict *struct {
				CurrentResourceVersion string `json:"currentResourceVersion"`
			} `json:"conflict"`
			HasMoreProductDependents bool `json:"hasMoreProductDependents"`
		} `json:"updateCategoryStatus"`
	}
	if err := c.client.Mutate(ctx, categoryDeletionStatusMutation, map[string]any{
		"input": map[string]any{
			"namespace":        namespace,
			"name":             name,
			"resourceVersion":  resourceVersion,
			"decoupleProducts": true,
		},
	}, &response); err != nil {
		return false, fmt.Errorf("category deletion client: decouple Products: %w", err)
	}
	if response.UpdateCategoryStatus.Conflict != nil {
		return false, fmt.Errorf("%w: current resourceVersion %q", types.ErrConflict, response.UpdateCategoryStatus.Conflict.CurrentResourceVersion)
	}
	return response.UpdateCategoryStatus.HasMoreProductDependents, nil
}

func (c *graphqlDeletionClient) CompleteDeletion(ctx context.Context, namespace, name, resourceVersion string) error {
	var response struct {
		UpdateCategoryStatus struct {
			Conflict *struct {
				CurrentResourceVersion string `json:"currentResourceVersion"`
			} `json:"conflict"`
		} `json:"updateCategoryStatus"`
	}
	if err := c.client.Mutate(ctx, categoryDeletionStatusMutation, map[string]any{
		"input": map[string]any{
			"namespace":        namespace,
			"name":             name,
			"resourceVersion":  resourceVersion,
			"completeDeletion": true,
		},
	}, &response); err != nil {
		return fmt.Errorf("category deletion client: complete deletion: %w", err)
	}
	if response.UpdateCategoryStatus.Conflict != nil {
		return fmt.Errorf("%w: current resourceVersion %q", types.ErrConflict, response.UpdateCategoryStatus.Conflict.CurrentResourceVersion)
	}
	return nil
}
