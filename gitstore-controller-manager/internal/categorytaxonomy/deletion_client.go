// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package categorytaxonomy

import (
	"context"
	"errors"
	"fmt"

	"github.com/gitstore-dev/gitstore/controller-manager/internal/graphqlclient"
	"github.com/gitstore-dev/gitstore/controller-manager/internal/types"
)

// categoryDeletionStatusMutation queries only fields UpdateCategoryStatusPayload
// actually declares (category, hasMoreProductDependents) — a resourceVersion
// conflict is reported as a GraphQL error with a RESOURCE_VERSION_CONFLICT
// (or forward-compatible CONFLICT) extension (see mapConflictErr below), not
// a payload field, matching every other status mutation in this codebase
// (graphql_status_client.go et al.).
const categoryDeletionStatusMutation = `
mutation($input: UpdateCategoryStatusInput!) {
  updateCategoryStatus(input: $input) {
    hasMoreProductDependents
  }
}`

// completeCategoryDeletionMutation finishes a terminating category's
// foreground deletion.
const completeCategoryDeletionMutation = `
mutation($input: CompleteCategoryDeletionInput!) {
  completeCategoryDeletion(input: $input) {
    id
  }
}`

// mapConflictErr maps a RESOURCE_VERSION_CONFLICT or forward-compatible
// CONFLICT GraphQL error to types.ErrConflict; any other error (including a
// NOT_FOUND-coded one) passes through wrapped but otherwise unchanged.
func mapConflictErr(err error) error {
	var gqlErr *graphqlclient.Error
	if errors.As(err, &gqlErr) && graphqlclient.IsConflictCode(gqlErr.Extensions["code"]) {
		return fmt.Errorf("%w: current resourceVersion %q: %w", types.ErrConflict, gqlErr.Extensions["resourceVersion"], err)
	}
	return err
}

// DeletionClient invokes the controller-only lifecycle operations: the
// bounded Product drain through updateCategoryStatus, and final removal
// through completeCategoryDeletion.
type DeletionClient interface {
	DecoupleProducts(ctx context.Context, namespace, name, resourceVersion string) (bool, error)
	CompleteDeletion(ctx context.Context, namespace, name, resourceVersion, uid string) error
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
		return false, fmt.Errorf("category deletion client: decouple Products: %w", mapConflictErr(err))
	}
	return response.UpdateCategoryStatus.HasMoreProductDependents, nil
}

func (c *graphqlDeletionClient) CompleteDeletion(ctx context.Context, namespace, name, resourceVersion, uid string) error {
	var response struct {
		CompleteCategoryDeletion struct{} `json:"completeCategoryDeletion"`
	}
	if err := c.client.Mutate(ctx, completeCategoryDeletionMutation, map[string]any{
		"input": map[string]any{
			"id":              uid,
			"namespace":       namespace,
			"name":            name,
			"resourceVersion": resourceVersion,
		},
	}, &response); err != nil {
		return fmt.Errorf("category deletion client: complete deletion: %w", mapConflictErr(err))
	}
	return nil
}
