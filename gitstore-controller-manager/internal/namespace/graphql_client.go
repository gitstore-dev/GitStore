// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package namespace

import (
	"context"
	"errors"
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

const repositoriesExistQuery = `
query($namespace: String!) {
  repositories(namespace: $namespace, first: 1) {
    edges { cursor }
  }
}`

// completeNamespaceDeletionMutation selects only the fields
// CompleteNamespaceDeletionPayload actually declares ({ id }, since commit
// 84b7bb4 / #394 removed the payload's conflict field). A resourceVersion
// conflict is reported as a GraphQL error with a RESOURCE_VERSION_CONFLICT
// (or forward-compatible CONFLICT) extension code, not a payload field,
// matching every other status/completion mutation in this codebase (see
// graphqlclient.IsConflictCode).
const completeNamespaceDeletionMutation = `
mutation($input: CompleteNamespaceDeletionInput!) {
  completeNamespaceDeletion(input: $input) {
    id
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
			Edges []struct {
				Cursor string `json:"cursor"`
			} `json:"edges"`
		} `json:"repositories"`
	}
	if err := c.client.Query(ctx, repositoriesExistQuery, map[string]any{"namespace": namespace}, &response); err != nil {
		return false, fmt.Errorf("namespace repository client: list repositories: %w", err)
	}
	return len(response.Repositories.Edges) > 0, nil
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
			ID *string `json:"id"`
		} `json:"completeNamespaceDeletion"`
	}
	if err := c.client.Mutate(ctx, completeNamespaceDeletionMutation, map[string]any{
		"input": map[string]any{
			"identifier":      namespace,
			"resourceVersion": resourceVersion,
		},
	}, &response); err != nil {
		var gqlErr *graphqlclient.Error
		if errors.As(err, &gqlErr) && graphqlclient.IsConflictCode(gqlErr.Extensions["code"]) {
			return fmt.Errorf(
				"namespace deletion client: %w: current resourceVersion %q: %w",
				types.ErrConflict, gqlErr.Extensions["resourceVersion"], err,
			)
		}
		return fmt.Errorf("namespace deletion client: complete deletion: %w", err)
	}
	if response.CompleteNamespaceDeletion.ID == nil {
		return fmt.Errorf("namespace deletion client: completion returned no deleted identifier")
	}
	return nil
}
