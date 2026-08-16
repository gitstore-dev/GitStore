// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package resolver

import (
	"testing"

	"github.com/gitstore-dev/gitstore/api/internal/graph/model"
	"github.com/stretchr/testify/assert"
)

func TestMatchesWatchSelector_NilSelectorMatchesEverything(t *testing.T) {
	assert.True(t, matchesWatchSelector(nil, map[string]string{"tier": "premium"}))
	assert.True(t, matchesWatchSelector(nil, nil))
}

func TestMatchesWatchSelector_EmptySelectorMatchesEverything(t *testing.T) {
	assert.True(t, matchesWatchSelector(&model.LabelSelectorInput{}, map[string]string{"tier": "premium"}))
}

func TestMatchesWatchSelector_MatchLabels(t *testing.T) {
	sel := &model.LabelSelectorInput{
		MatchLabels: map[string]any{"tier": "premium"},
	}
	assert.True(t, matchesWatchSelector(sel, map[string]string{"tier": "premium"}))
	assert.False(t, matchesWatchSelector(sel, map[string]string{"tier": "standard"}))
	assert.False(t, matchesWatchSelector(sel, map[string]string{}))
}

func TestMatchesWatchSelector_MatchExpressionsIn(t *testing.T) {
	sel := &model.LabelSelectorInput{
		MatchExpressions: []*model.LabelSelectorRequirementInput{
			{Key: "brand", Operator: model.LabelSelectorOperatorIn, Values: []string{"apple", "samsung"}},
		},
	}
	assert.True(t, matchesWatchSelector(sel, map[string]string{"brand": "apple"}))
	assert.False(t, matchesWatchSelector(sel, map[string]string{"brand": "sony"}))
}

func TestMatchesWatchSelector_MatchExpressionsNotIn(t *testing.T) {
	sel := &model.LabelSelectorInput{
		MatchExpressions: []*model.LabelSelectorRequirementInput{
			{Key: "brand", Operator: model.LabelSelectorOperatorNotIn, Values: []string{"apple"}},
		},
	}
	assert.True(t, matchesWatchSelector(sel, map[string]string{"brand": "sony"}))
	assert.False(t, matchesWatchSelector(sel, map[string]string{"brand": "apple"}))
}

func TestMatchesWatchSelector_MatchExpressionsExists(t *testing.T) {
	sel := &model.LabelSelectorInput{
		MatchExpressions: []*model.LabelSelectorRequirementInput{
			{Key: "brand", Operator: model.LabelSelectorOperatorExists},
		},
	}
	assert.True(t, matchesWatchSelector(sel, map[string]string{"brand": "sony"}))
	assert.False(t, matchesWatchSelector(sel, map[string]string{}))
}

func TestMatchesWatchSelector_MatchExpressionsDoesNotExist(t *testing.T) {
	sel := &model.LabelSelectorInput{
		MatchExpressions: []*model.LabelSelectorRequirementInput{
			{Key: "brand", Operator: model.LabelSelectorOperatorDoesNotExist},
		},
	}
	assert.True(t, matchesWatchSelector(sel, map[string]string{}))
	assert.False(t, matchesWatchSelector(sel, map[string]string{"brand": "sony"}))
}
