// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package namespace

import (
	"context"
	"fmt"

	"github.com/gitstore-dev/gitstore/controller-manager/internal/graphqlclient"
	"github.com/gitstore-dev/gitstore/controller-manager/internal/types"
)

const provisionNamespaceSystemRepositoryMutation = `
mutation($input: ProvisionNamespaceSystemRepositoryInput!) {
  provisionNamespaceSystemRepository(input: $input) {
    repository { metadata { name } }
  }
}`

const repositoriesCountQuery = `
query($namespace: String!) {
  repositories(namespace: $namespace, first: 1) {
    totalCount
  }
}`

const completeNamespaceDeletionMutation = `
mutation($input: CompleteNamespaceDeletionInput!) {
  completeNamespaceDeletion(input: $input) {
    deletedIdentifier
    conflict { currentResourceVersion }
  }
}`

// GraphQLRepositoryClient uses the controller-only bootstrap mutation to
// provision system repositories idempotently. This intentionally does not use
// createRepository: gitstore-system is system-managed and is not an author
// declarative Repository resource.
type GraphQLRepositoryClient struct {
	client *graphqlclient.Client
}

// NewGraphQLRepositoryClient returns a RepositoryClient backed by GraphQL.
func NewGraphQLRepositoryClient(client *graphqlclient.Client) *GraphQLRepositoryClient {
	return &GraphQLRepositoryClient{client: client}
}

// EnsureSystemRepository ensures the system-managed gitstore-system repository
// exists. The API owns the lookup/create race so every controller replica can
// invoke this operation safely.
func (c *GraphQLRepositoryClient) EnsureSystemRepository(ctx context.Context, namespace string) error {
	var response struct {
		ProvisionNamespaceSystemRepository struct {
			Repository *struct {
				Metadata struct {
					Name string `json:"name"`
				} `json:"metadata"`
			} `json:"repository"`
		} `json:"provisionNamespaceSystemRepository"`
	}
	err := c.client.Mutate(ctx, provisionNamespaceSystemRepositoryMutation, map[string]any{
		"input": map[string]any{
			"namespace": namespace,
		},
	}, &response)
	if err == nil && response.ProvisionNamespaceSystemRepository.Repository != nil {
		return nil
	}
	if err != nil {
		return fmt.Errorf("namespace repository client: provision system repository: %w", err)
	}
	return fmt.Errorf("namespace repository client: provision system repository returned no repository")
}

// HasRepositories reports whether the namespace currently owns any repository.
func (c *GraphQLRepositoryClient) HasRepositories(ctx context.Context, namespace string) (bool, error) {
	var response struct {
		Repositories struct {
			TotalCount int `json:"totalCount"`
		} `json:"repositories"`
	}
	if err := c.client.Query(ctx, repositoriesCountQuery, map[string]any{"namespace": namespace}, &response); err != nil {
		return false, fmt.Errorf("namespace repository client: list repositories: %w", err)
	}
	return response.Repositories.TotalCount > 0, nil
}

// GraphQLDeletionClient completes foreground Namespace deletion through the
// resource-version-guarded API mutation.
type GraphQLDeletionClient struct {
	client *graphqlclient.Client
}

// NewGraphQLDeletionClient returns a Namespace deletion client backed by GraphQL.
func NewGraphQLDeletionClient(client *graphqlclient.Client) *GraphQLDeletionClient {
	return &GraphQLDeletionClient{client: client}
}

func (c *GraphQLDeletionClient) CompleteDeletion(ctx context.Context, namespace, resourceVersion string) error {
	var response struct {
		CompleteNamespaceDeletion struct {
			DeletedIdentifier *string `json:"deletedIdentifier"`
			Conflict          *struct {
				CurrentResourceVersion string `json:"currentResourceVersion"`
			} `json:"conflict"`
		} `json:"completeNamespaceDeletion"`
	}
	if err := c.client.Mutate(ctx, completeNamespaceDeletionMutation, map[string]any{
		"input": map[string]any{
			"identifier":      namespace,
			"resourceVersion": resourceVersion,
		},
	}, &response); err != nil {
		return fmt.Errorf("namespace deletion client: complete deletion: %w", err)
	}
	if response.CompleteNamespaceDeletion.Conflict != nil {
		return fmt.Errorf(
			"namespace deletion client: %w: current resourceVersion %q",
			types.ErrConflict,
			response.CompleteNamespaceDeletion.Conflict.CurrentResourceVersion,
		)
	}
	if response.CompleteNamespaceDeletion.DeletedIdentifier == nil {
		return fmt.Errorf("namespace deletion client: completion returned no deleted identifier")
	}
	return nil
}
