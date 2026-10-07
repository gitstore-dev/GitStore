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

const updateRepositoryStatusMutation = `
mutation($input: UpdateRepositoryStatusInput!) {
  updateRepositoryStatus(input: $input) {
    repository { metadata { resourceVersion } }
  }
}`

type updateRepositoryStatusResponse struct {
	UpdateRepositoryStatus struct {
		Repository struct {
			Metadata struct {
				ResourceVersion string `json:"resourceVersion"`
			} `json:"metadata"`
		} `json:"repository"`
	} `json:"updateRepositoryStatus"`
}

// graphqlRepositoryStatusClient satisfies StatusClient by issuing the
// updateRepositoryStatus mutation (spec 058's Repository status-subresource
// contract) through a graphqlclient.Client.
type graphqlRepositoryStatusClient struct {
	client *graphqlclient.Client
}

// NewGraphQLRepositoryStatusClient returns a StatusClient that writes
// Repository status via the updateRepositoryStatus GraphQL mutation.
func NewGraphQLRepositoryStatusClient(client *graphqlclient.Client) StatusClient {
	return &graphqlRepositoryStatusClient{client: client}
}

func (c *graphqlRepositoryStatusClient) Apply(ctx context.Context, key types.WorkItemKey, patch *StatusPatch) error {
	input, err := toUpdateCategoryStatusInput(key, patch)
	if err != nil {
		return fmt.Errorf("graphqlRepositoryStatusClient: build input: %w", err)
	}

	var resp updateRepositoryStatusResponse
	if err := c.client.Mutate(ctx, updateRepositoryStatusMutation, map[string]any{"input": input}, &resp); err != nil {
		var gqlErr *graphqlclient.Error
		if errors.As(err, &gqlErr) {
			switch gqlErr.Extensions["code"] {
			case "NOT_FOUND":
				return fmt.Errorf("graphqlRepositoryStatusClient: %w: %w", types.ErrNotFound, err)
			case "RESOURCE_VERSION_CONFLICT":
				return fmt.Errorf("graphqlRepositoryStatusClient: %w: current resourceVersion %q: %w", types.ErrConflict, gqlErr.Extensions["resourceVersion"], err)
			}
		}
		return fmt.Errorf("graphqlRepositoryStatusClient: updateRepositoryStatus: %w", err)
	}
	return nil
}
