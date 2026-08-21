// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package resolver

import "github.com/gitstore-dev/gitstore/api/internal/catalog"

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
