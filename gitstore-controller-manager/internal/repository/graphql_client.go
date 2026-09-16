// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package repository

import (
	"context"
	"errors"
	"fmt"

	"github.com/gitstore-dev/gitstore/controller-manager/internal/graphqlclient"
	"github.com/gitstore-dev/gitstore/controller-manager/internal/types"
)

const completeRepositoryDeletionMutation = `
mutation($input: CompleteRepositoryDeletionInput!) {
  completeRepositoryDeletion(input: $input) {
    deletedRepositoryId
  }
}`

const provisionRepositoryStorageMutation = `
mutation($input: ProvisionRepositoryStorageInput!) {
  provisionRepositoryStorage(input: $input) {
    repository { metadata { name namespace } }
  }
}`

// GraphQLStorageClient asks the API to provision an already-admitted
// Repository. It intentionally has no Git-service credentials.
type GraphQLStorageClient struct {
	client *graphqlclient.Client
}

// NewGraphQLStorageClient returns the controller's API-backed storage client.
func NewGraphQLStorageClient(client *graphqlclient.Client) *GraphQLStorageClient {
	return &GraphQLStorageClient{client: client}
}

// EnsureStorage is safe for at-least-once reconciliation: provisioning is
// idempotent at the API/Git-service boundary.
func (c *GraphQLStorageClient) EnsureStorage(ctx context.Context, namespace, name string) error {
	var response struct {
		ProvisionRepositoryStorage struct {
			Repository *struct {
				Metadata struct {
					Name      string `json:"name"`
					Namespace string `json:"namespace"`
				} `json:"metadata"`
			} `json:"repository"`
		} `json:"provisionRepositoryStorage"`
	}
	if err := c.client.Mutate(ctx, provisionRepositoryStorageMutation, map[string]any{
		"input": map[string]any{"namespace": namespace, "name": name},
	}, &response); err != nil {
		return fmt.Errorf("repository storage client: provision storage: %w", err)
	}
	if response.ProvisionRepositoryStorage.Repository == nil {
		return fmt.Errorf("repository storage client: provision storage returned no repository")
	}
	return nil
}

// GraphQLCompletionClient completes foreground Repository deletion through the
// API. The API retains the Git-service credentials and performs storage removal
// before it clears the finalizer, so a controller cannot accidentally advance
// the finalizer boundary ahead of confirmed storage removal.
type GraphQLCompletionClient struct {
	client *graphqlclient.Client
}

// NewGraphQLCompletionClient returns a CompletionClient backed by GraphQL.
func NewGraphQLCompletionClient(client *graphqlclient.Client) *GraphQLCompletionClient {
	return &GraphQLCompletionClient{client: client}
}

// CompleteDeletion asks the API to recheck the deletion preconditions and
// atomically complete the Repository lifecycle transition. A conflict remains
// retryable work: a fresh watch event will replace the cache entry and enqueue
// its current resourceVersion.
func (c *GraphQLCompletionClient) CompleteDeletion(ctx context.Context, namespace, name, resourceVersion string) error {
	var response struct {
		CompleteRepositoryDeletion struct {
			DeletedRepositoryID *string `json:"deletedRepositoryId"`
		} `json:"completeRepositoryDeletion"`
	}
	if err := c.client.Mutate(ctx, completeRepositoryDeletionMutation, map[string]any{
		"input": map[string]any{
			"namespace":       namespace,
			"name":            name,
			"resourceVersion": resourceVersion,
		},
	}, &response); err != nil {
		var gqlErr *graphqlclient.Error
		if errors.As(err, &gqlErr) {
			switch gqlErr.Extensions["code"] {
			case "RESOURCE_VERSION_CONFLICT":
				return fmt.Errorf("repository completion client: %w: %w", types.ErrConflict, err)
			case "NOT_FOUND":
				// A previous at-least-once reconcile may already have completed
				// this deletion. Treat the desired terminal state as success.
				return nil
			}
		}
		return fmt.Errorf("repository completion client: complete deletion: %w", err)
	}
	return nil
}
