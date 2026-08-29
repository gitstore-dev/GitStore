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

const systemRepositoryQuery = `
query($by: RepositoryBy!) {
  repository(by: $by) {
    metadata { name }
  }
}`

const createSystemRepositoryMutation = `
mutation($input: CreateRepositoryInput!) {
  createRepository(input: $input) {
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

// GraphQLRepositoryClient uses the existing repository query/create mutation
// to provision system repositories idempotently.
type GraphQLRepositoryClient struct {
	client *graphqlclient.Client
}

// NewGraphQLRepositoryClient returns a RepositoryClient backed by GraphQL.
func NewGraphQLRepositoryClient(client *graphqlclient.Client) *GraphQLRepositoryClient {
	return &GraphQLRepositoryClient{client: client}
}

// EnsureSystemRepository creates gitstore-system when it does not already exist.
func (c *GraphQLRepositoryClient) EnsureSystemRepository(ctx context.Context, namespace string) error {
	exists, err := c.systemRepositoryExists(ctx, namespace)
	if err != nil {
		return err
	}
	if exists {
		return nil
	}

	var response struct {
		CreateRepository struct {
			Repository *struct {
				Metadata struct {
					Name string `json:"name"`
				} `json:"metadata"`
			} `json:"repository"`
		} `json:"createRepository"`
	}
	err = c.client.Mutate(ctx, createSystemRepositoryMutation, map[string]any{
		"input": map[string]any{
			"namespace":     namespace,
			"name":          SystemRepositoryName,
			"defaultBranch": "main",
		},
	}, &response)
	if err == nil && response.CreateRepository.Repository != nil {
		return nil
	}

	// A concurrent reconcile may have created the repository after our lookup.
	if existsAfterCreate, lookupErr := c.systemRepositoryExists(ctx, namespace); lookupErr == nil && existsAfterCreate {
		return nil
	}
	if err != nil {
		return fmt.Errorf("namespace repository client: create system repository: %w", err)
	}
	return fmt.Errorf("namespace repository client: create system repository returned no repository")
}

func (c *GraphQLRepositoryClient) systemRepositoryExists(ctx context.Context, namespace string) (bool, error) {
	var response struct {
		Repository *struct {
			Metadata struct {
				Name string `json:"name"`
			} `json:"metadata"`
		} `json:"repository"`
	}
	if err := c.client.Query(ctx, systemRepositoryQuery, map[string]any{
		"by": map[string]any{
			"namespacePath": map[string]any{
				"namespace": namespace,
				"name":      SystemRepositoryName,
			},
		},
	}, &response); err != nil {
		if isNotFoundError(err) {
			// The repository genuinely does not exist yet — this is the
			// expected steady state before EnsureSystemRepository creates
			// it, not a hard failure.
			return false, nil
		}
		return false, fmt.Errorf("namespace repository client: query system repository: %w", err)
	}
	return response.Repository != nil, nil
}

// isNotFoundError reports whether err is a GraphQL error signaling that the
// queried repository does not exist. The primary signal is the "NOT_FOUND"
// extensions code gitstore-api's resolvers use for missing resources.
// During a rolling deployment, this controller may still query an
// older API replica whose repository(by: namespacePath) resolver predates
// that convention and returns only the plain message "repository not
// found" with no extensions code — that legacy shape is recognized too, so
// EnsureSystemRepository keeps working through a mixed-version rollout
// rather than requiring every old replica to drain first.
func isNotFoundError(err error) bool {
	var gqlErr *graphqlclient.Error
	if !errors.As(err, &gqlErr) || gqlErr == nil {
		return false
	}
	if code, _ := gqlErr.Extensions["code"].(string); code == "NOT_FOUND" {
		return true
	}
	return gqlErr.Extensions == nil && gqlErr.Message == "repository not found"
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
