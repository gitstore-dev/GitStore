// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package product

import (
	"context"
	"errors"
	"fmt"
	"github.com/gitstore-dev/gitstore/controller-manager/internal/graphqlclient"
	"github.com/gitstore-dev/gitstore/controller-manager/internal/types"
)

const completeProductDeletionMutation = `mutation($input: CompleteProductDeletionInput!) { completeProductDeletion(input: $input) { id } }`

type GraphQLCompletionClient struct{ client *graphqlclient.Client }

func NewGraphQLCompletionClient(client *graphqlclient.Client) *GraphQLCompletionClient {
	return &GraphQLCompletionClient{client: client}
}
func (c *GraphQLCompletionClient) CompleteDeletion(ctx context.Context, namespace, name, resourceVersion string) error {
	var response struct {
		CompleteProductDeletion struct {
			ID *string `json:"id"`
		} `json:"completeProductDeletion"`
	}
	if err := c.client.Mutate(ctx, completeProductDeletionMutation, map[string]any{"input": map[string]any{"namespace": namespace, "name": name, "resourceVersion": resourceVersion}}, &response); err != nil {
		var gqlErr *graphqlclient.Error
		if errors.As(err, &gqlErr) && gqlErr.Extensions["code"] == "RESOURCE_VERSION_CONFLICT" {
			return fmt.Errorf("product completion: %w", types.ErrConflict)
		}
		return err
	}
	return nil
}
