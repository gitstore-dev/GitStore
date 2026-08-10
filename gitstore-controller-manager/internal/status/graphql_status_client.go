// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package status

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/gitstore-dev/gitstore/controller-manager/internal/graphqlclient"
	"github.com/gitstore-dev/gitstore/controller-manager/internal/types"
)

const updateCategoryStatusMutation = `
mutation($input: UpdateCategoryStatusInput!) {
  updateCategoryStatus(input: $input) {
    category { metadata { resourceVersion } }
    conflict { currentResourceVersion }
  }
}`

type updateCategoryStatusResponse struct {
	UpdateCategoryStatus struct {
		Category *struct {
			Metadata struct {
				ResourceVersion string `json:"resourceVersion"`
			} `json:"metadata"`
		} `json:"category"`
		Conflict *struct {
			CurrentResourceVersion string `json:"currentResourceVersion"`
		} `json:"conflict"`
	} `json:"updateCategoryStatus"`
}

// graphqlStatusClient satisfies StatusClient by issuing the updateCategoryStatus
// mutation (spec 040's status-subresource contract) through a graphqlclient.Client.
type graphqlStatusClient struct {
	client *graphqlclient.Client
}

// NewGraphQLStatusClient returns a StatusClient that writes CategoryTaxonomy
// status via the updateCategoryStatus GraphQL mutation.
func NewGraphQLStatusClient(client *graphqlclient.Client) StatusClient {
	return &graphqlStatusClient{client: client}
}

// Apply issues updateCategoryStatus for key using patch. A non-null conflict
// in the response maps to types.ErrConflict. A NOT_FOUND-extension GraphQL
// error maps to types.ErrNotFound. Any other GraphQL error (including
// FORBIDDEN) is returned wrapped, without a sentinel.
func (c *graphqlStatusClient) Apply(ctx context.Context, key types.WorkItemKey, patch *StatusPatch) error {
	input, err := toUpdateCategoryStatusInput(key, patch)
	if err != nil {
		return fmt.Errorf("graphqlStatusClient: build input: %w", err)
	}

	var resp updateCategoryStatusResponse
	if err := c.client.Mutate(ctx, updateCategoryStatusMutation, map[string]any{"input": input}, &resp); err != nil {
		var gqlErr *graphqlclient.Error
		if errors.As(err, &gqlErr) && gqlErr.Extensions["code"] == "NOT_FOUND" {
			return fmt.Errorf("graphqlStatusClient: %w: %w", types.ErrNotFound, err)
		}
		return fmt.Errorf("graphqlStatusClient: updateCategoryStatus: %w", err)
	}

	if resp.UpdateCategoryStatus.Conflict != nil {
		return fmt.Errorf("graphqlStatusClient: %w: current resourceVersion %q", types.ErrConflict, resp.UpdateCategoryStatus.Conflict.CurrentResourceVersion)
	}
	return nil
}

func toUpdateCategoryStatusInput(key types.WorkItemKey, patch *StatusPatch) (map[string]any, error) {
	input := map[string]any{
		"name":            key.Name,
		"namespace":       key.Namespace,
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
	if patch.Resolved != nil {
		var resolved map[string]any
		if err := json.Unmarshal(patch.Resolved, &resolved); err != nil {
			return nil, fmt.Errorf("unmarshal patch.Resolved: %w", err)
		}
		input["resolved"] = resolved
	}
	return input, nil
}

func toConditionInputs(conditions []*Condition) []map[string]any {
	out := make([]map[string]any, 0, len(conditions))
	for _, c := range conditions {
		out = append(out, map[string]any{
			"type":               c.Type,
			"status":             c.Status,
			"observedGeneration": c.ObservedGeneration,
			"lastTransitionTime": c.LastTransitionTime.Format(time.RFC3339Nano),
			"reason":             c.Reason,
			"message":            c.Message,
		})
	}
	return out
}
