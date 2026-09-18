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

const updateNamespaceStatusMutation = `
mutation($input: UpdateNamespaceStatusInput!) {
  updateNamespaceStatus(input: $input) {
    namespace { metadata { resourceVersion } }
  }
}`

type updateNamespaceStatusResponse struct {
	UpdateNamespaceStatus struct {
		Namespace struct {
			Metadata struct {
				ResourceVersion string `json:"resourceVersion"`
			} `json:"metadata"`
		} `json:"namespace"`
	} `json:"updateNamespaceStatus"`
}

// graphqlNamespaceStatusClient satisfies StatusClient by issuing the
// updateNamespaceStatus mutation through a graphqlclient.Client.
type graphqlNamespaceStatusClient struct {
	client *graphqlclient.Client
}

// NewGraphQLNamespaceStatusClient returns a StatusClient that writes
// Namespace status via the updateNamespaceStatus GraphQL mutation.
func NewGraphQLNamespaceStatusClient(client *graphqlclient.Client) StatusClient {
	return &graphqlNamespaceStatusClient{client: client}
}

func (c *graphqlNamespaceStatusClient) Apply(ctx context.Context, key types.WorkItemKey, patch *StatusPatch) error {
	input := map[string]any{
		"name":            key.Name,
		"resourceVersion": patch.ResourceVersion,
	}
	if patch.ObservedGeneration != nil {
		input["observedGeneration"] = *patch.ObservedGeneration
	}
	if patch.LastAppliedRevision != nil {
		input["lastAppliedRevision"] = *patch.LastAppliedRevision
	}
	if patch.Conditions != nil {
		input["conditions"] = toConditionInputs(patch.Conditions)
	}

	var resp updateNamespaceStatusResponse
	if err := c.client.Mutate(ctx, updateNamespaceStatusMutation, map[string]any{"input": input}, &resp); err != nil {
		var gqlErr *graphqlclient.Error
		if errors.As(err, &gqlErr) {
			switch gqlErr.Extensions["code"] {
			case "NOT_FOUND":
				return fmt.Errorf("graphqlNamespaceStatusClient: %w: %w", types.ErrNotFound, err)
			case "RESOURCE_VERSION_CONFLICT":
				return fmt.Errorf("graphqlNamespaceStatusClient: %w: current resourceVersion %q: %w", types.ErrConflict, gqlErr.Extensions["resourceVersion"], err)
			}
		}
		return fmt.Errorf("graphqlNamespaceStatusClient: updateNamespaceStatus: %w", err)
	}
	return nil
}
