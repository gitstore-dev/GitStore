// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package status

import (
	"context"
	"fmt"

	"github.com/gitstore-dev/gitstore/controller-manager/internal/graphqlclient"
	"github.com/gitstore-dev/gitstore/controller-manager/internal/types"
)

const updateProductStatusMutation = `
mutation($input: UpdateProductStatusInput!) {
  updateProductStatus(input: $input) {
    product { metadata { resourceVersion } }
  }
}`

type updateProductStatusResponse struct {
	UpdateProductStatus struct {
		Product struct {
			Metadata struct {
				ResourceVersion string `json:"resourceVersion"`
			} `json:"metadata"`
		} `json:"product"`
	} `json:"updateProductStatus"`
}

// graphqlProductStatusClient satisfies StatusClient by issuing the
// updateProductStatus mutation (spec 062) through a graphqlclient.Client.
type graphqlProductStatusClient struct {
	client *graphqlclient.Client
}

// NewGraphQLProductStatusClient returns a StatusClient that writes Product
// status via the updateProductStatus GraphQL mutation.
func NewGraphQLProductStatusClient(client *graphqlclient.Client) StatusClient {
	return &graphqlProductStatusClient{client: client}
}

func (c *graphqlProductStatusClient) Apply(ctx context.Context, key types.WorkItemKey, patch *StatusPatch) error {
	input, err := toStatusInput(key, patch)
	if err != nil {
		return fmt.Errorf("graphqlProductStatusClient: build input: %w", err)
	}

	var resp updateProductStatusResponse
	if err := c.client.Mutate(ctx, updateProductStatusMutation, map[string]any{"input": input}, &resp); err != nil {
		return statusMutationError("graphqlProductStatusClient", "updateProductStatus", err)
	}
	return nil
}
