// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package resolver

import (
	"strings"

	"github.com/gitstore-dev/gitstore/api/internal/catalog"
	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	"github.com/gitstore-dev/gitstore/api/internal/graph/model"
)

// toCategoryTaxonomyStatusPatch converts the GraphQL UpdateCategoryStatusInput
// into the datastore-level partial-merge patch. Fields left nil on the
// input are left nil on the patch — this is what "unchanged" means
// throughout the partial-merge contract (spec 040 FR-008).
func toCategoryTaxonomyStatusPatch(input model.UpdateCategoryStatusInput) datastore.CategoryTaxonomyStatusPatch {
	patch := datastore.CategoryTaxonomyStatusPatch{
		ResourceVersion: input.ResourceVersion,
	}
	if input.ObservedGeneration != nil {
		gen := int64(*input.ObservedGeneration)
		patch.ObservedGeneration = &gen
	}
	patch.LastAppliedRevision = input.LastAppliedRevision
	if input.Conditions != nil {
		patch.Conditions = toConditions(input.Conditions)
	}
	if input.Resolved != nil {
		patch.Resolved = toResolvedCategoryTaxonomy(input.Resolved)
	}
	return patch
}

func graphQLConditionStatusToCatalog(status model.ConditionStatus) catalog.ConditionStatus {
	switch strings.ToUpper(string(status)) {
	case "TRUE":
		return catalog.ConditionTrue
	case "FALSE":
		return catalog.ConditionFalse
	case "UNKNOWN":
		return catalog.ConditionUnknown
	default:
		return catalog.ConditionStatus(strings.TrimSpace(string(status)))
	}
}

func toConditions(in []*model.ConditionInput) []catalog.Condition {
	out := make([]catalog.Condition, 0, len(in))
	for _, c := range in {
		if c == nil {
			continue
		}
		cond := catalog.Condition{
			Type:               c.Type,
			Status:             graphQLConditionStatusToCatalog(c.Status),
			ObservedGeneration: int64(c.ObservedGeneration),
			LastTransitionTime: c.LastTransitionTime,
		}
		if c.Reason != nil {
			cond.Reason = *c.Reason
		}
		if c.Message != nil {
			cond.Message = *c.Message
		}
		out = append(out, cond)
	}
	return out
}

func toResolvedCategoryTaxonomy(in *model.ResolvedCategoryTaxonomyInput) *catalog.ResolvedCategoryTaxonomy {
	if in == nil {
		return nil
	}
	return &catalog.ResolvedCategoryTaxonomy{
		Depth:        int8(in.Depth),
		Path:         in.Path,
		ChildCount:   int64(in.ChildCount),
		ProductCount: int64(in.ProductCount),
	}
}
