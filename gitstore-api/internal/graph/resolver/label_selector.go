// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package resolver

import (
	"github.com/gitstore-dev/gitstore/api/internal/catalog"
	"github.com/gitstore-dev/gitstore/api/internal/graph/model"
)

// matchesWatchSelector reports whether labels satisfy selector, for the
// purposes of watch subscription filtering (spec 040 FR-007). Unlike
// catalog.MatchesLabels (used for Collection membership, where an
// empty/nil selector matches nothing), a nil selector here means "no
// filter" and matches everything — a watch caller that supplies no
// selector expects to see every resource of the kind.
func matchesWatchSelector(selector *model.LabelSelectorInput, labels map[string]string) bool {
	if selector == nil {
		return true
	}
	if len(selector.MatchLabels) == 0 && len(selector.MatchExpressions) == 0 {
		return true
	}
	return catalog.MatchesLabels(toCatalogLabelSelector(selector), labels)
}

func toCatalogLabelSelector(selector *model.LabelSelectorInput) *catalog.LabelSelector {
	if selector == nil {
		return nil
	}
	matchLabels := make(map[string]string, len(selector.MatchLabels))
	for _, kv := range selector.MatchLabels {
		if kv == nil {
			continue
		}
		matchLabels[kv.Key] = kv.Value
	}
	matchExpressions := make([]catalog.LabelSelectorRequirement, 0, len(selector.MatchExpressions))
	for _, req := range selector.MatchExpressions {
		if req == nil {
			continue
		}
		matchExpressions = append(matchExpressions, catalog.LabelSelectorRequirement{
			Key:      req.Key,
			Operator: catalogOperator(req.Operator),
			Values:   req.Values,
		})
	}
	return &catalog.LabelSelector{
		MatchLabels:      matchLabels,
		MatchExpressions: matchExpressions,
	}
}

// catalogOperator maps the GraphQL LabelSelectorOperator enum (upper-snake,
// e.g. "NOT_IN") to the PascalCase strings catalog.MatchesLabels switches
// on (e.g. "NotIn") — the two are NOT the same casing convention.
func catalogOperator(op model.LabelSelectorOperator) string {
	switch op {
	case model.LabelSelectorOperatorIn:
		return "In"
	case model.LabelSelectorOperatorNotIn:
		return "NotIn"
	case model.LabelSelectorOperatorExists:
		return "Exists"
	case model.LabelSelectorOperatorDoesNotExist:
		return "DoesNotExist"
	default:
		return ""
	}
}
