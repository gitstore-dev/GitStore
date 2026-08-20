// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package status

import (
	"context"
	"errors"
	"fmt"

	"github.com/gitstore-dev/gitstore/controller-manager/internal/graphqlclient"
	"github.com/gitstore-dev/gitstore/controller-manager/internal/types"
)

const updateResourceStatusMutation = `
mutation($input: UpdateResourceStatusInput!) {
  updateResourceStatus(input: $input) {
    object
    conflict { currentResourceVersion }
  }
}`

type updateResourceStatusResponse struct {
	UpdateResourceStatus struct {
		Object   map[string]any `json:"object"`
		Conflict *struct {
			CurrentResourceVersion string `json:"currentResourceVersion"`
		} `json:"conflict"`
	} `json:"updateResourceStatus"`
}

type graphqlResourceStatusClient struct {
	client *graphqlclient.Client
}

// NewGraphQLResourceStatusClient returns a kind-aware StatusClient using the
// generic updateResourceStatus mutation.
func NewGraphQLResourceStatusClient(client *graphqlclient.Client) StatusClient {
	return &graphqlResourceStatusClient{client: client}
}

func (c *graphqlResourceStatusClient) Apply(ctx context.Context, key types.WorkItemKey, patch *StatusPatch) error {
	input, err := toUpdateCategoryStatusInput(key, patch)
	if err != nil {
		return fmt.Errorf("graphqlResourceStatusClient: build input: %w", err)
	}
	input["kind"] = key.Kind

	var response updateResourceStatusResponse
	if err := c.client.Mutate(ctx, updateResourceStatusMutation, map[string]any{"input": input}, &response); err != nil {
		var gqlErr *graphqlclient.Error
		if errors.As(err, &gqlErr) && gqlErr.Extensions["code"] == "NOT_FOUND" {
			return fmt.Errorf("graphqlResourceStatusClient: %w: %w", types.ErrNotFound, err)
		}
		return fmt.Errorf("graphqlResourceStatusClient: updateResourceStatus: %w", err)
	}
	if response.UpdateResourceStatus.Conflict != nil {
		return fmt.Errorf(
			"graphqlResourceStatusClient: %w: current resourceVersion %q",
			types.ErrConflict,
			response.UpdateResourceStatus.Conflict.CurrentResourceVersion,
		)
	}
	return nil
}
