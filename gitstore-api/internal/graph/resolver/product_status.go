// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package resolver

import (
	"context"

	"github.com/gitstore-dev/gitstore/api/internal/catalog"
)

func (r *mutationResolver) categoryMembershipUIDs(ctx context.Context, namespace string, resolved *catalog.ResolvedProductDefinition) ([]string, error) {
	if resolved == nil || resolved.Category == nil {
		return nil, nil
	}
	path := resolved.Category.Path
	if len(path) == 0 {
		path = []string{resolved.Category.Name}
	}
	uids := make([]string, 0, len(path))
	for _, name := range path {
		category, err := r.store.GetCategoryTaxonomyByName(ctx, namespace, name)
		if err != nil {
			return nil, err
		}
		// Projection keys are datastore identities. Relay IDs are API-boundary
		// encodings and must never be persisted into a Scylla index.
		uids = append(uids, category.UID)
	}
	return uids, nil
}

func mergeProductConditions(existing, incoming []catalog.Condition) []catalog.Condition {
	byType := make(map[catalog.ConditionType]catalog.Condition, len(existing)+len(incoming))
	order := make([]catalog.ConditionType, 0, len(existing)+len(incoming))
	for _, condition := range existing {
		if _, found := byType[condition.Type]; !found {
			order = append(order, condition.Type)
		}
		byType[condition.Type] = condition
	}
	for _, condition := range incoming {
		if _, found := byType[condition.Type]; !found {
			order = append(order, condition.Type)
		}
		byType[condition.Type] = condition
	}
	merged := make([]catalog.Condition, 0, len(order))
	for _, conditionType := range order {
		merged = append(merged, byType[conditionType])
	}
	return merged
}
