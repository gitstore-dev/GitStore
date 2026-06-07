// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package catalog

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestMatchesLabels_NilSelector_ReturnsFalse(t *testing.T) {
	assert.False(t, MatchesLabels(nil, map[string]string{"k": "v"}))
}

func TestMatchesLabels_EmptySelector_ReturnsFalse(t *testing.T) {
	assert.False(t, MatchesLabels(&LabelSelector{}, map[string]string{"k": "v"}))
}

func TestMatchesLabels_MatchLabels_AllMatch(t *testing.T) {
	sel := &LabelSelector{MatchLabels: map[string]string{"brand": "apple", "type": "laptop"}}
	labels := map[string]string{"brand": "apple", "type": "laptop", "extra": "ignored"}
	assert.True(t, MatchesLabels(sel, labels))
}

func TestMatchesLabels_MatchLabels_OneMismatch_ReturnsFalse(t *testing.T) {
	sel := &LabelSelector{MatchLabels: map[string]string{"brand": "apple", "type": "laptop"}}
	labels := map[string]string{"brand": "apple", "type": "desktop"}
	assert.False(t, MatchesLabels(sel, labels))
}

func TestMatchesLabels_MatchLabels_MissingKey_ReturnsFalse(t *testing.T) {
	sel := &LabelSelector{MatchLabels: map[string]string{"brand": "apple"}}
	assert.False(t, MatchesLabels(sel, map[string]string{}))
}

func TestMatchesLabels_In_Match(t *testing.T) {
	sel := &LabelSelector{MatchExpressions: []LabelSelectorRequirement{
		{Key: "type", Operator: "In", Values: []string{"laptop", "notebook"}},
	}}
	assert.True(t, MatchesLabels(sel, map[string]string{"type": "laptop"}))
}

func TestMatchesLabels_In_NoMatch(t *testing.T) {
	sel := &LabelSelector{MatchExpressions: []LabelSelectorRequirement{
		{Key: "type", Operator: "In", Values: []string{"laptop", "notebook"}},
	}}
	assert.False(t, MatchesLabels(sel, map[string]string{"type": "desktop"}))
}

func TestMatchesLabels_In_MissingKey_ReturnsFalse(t *testing.T) {
	sel := &LabelSelector{MatchExpressions: []LabelSelectorRequirement{
		{Key: "type", Operator: "In", Values: []string{"laptop"}},
	}}
	assert.False(t, MatchesLabels(sel, map[string]string{}))
}

func TestMatchesLabels_NotIn_KeyAbsent_ReturnsTrue(t *testing.T) {
	sel := &LabelSelector{MatchExpressions: []LabelSelectorRequirement{
		{Key: "type", Operator: "NotIn", Values: []string{"desktop"}},
	}}
	assert.True(t, MatchesLabels(sel, map[string]string{"brand": "apple"}))
}

func TestMatchesLabels_NotIn_ValueInList_ReturnsFalse(t *testing.T) {
	sel := &LabelSelector{MatchExpressions: []LabelSelectorRequirement{
		{Key: "type", Operator: "NotIn", Values: []string{"desktop"}},
	}}
	assert.False(t, MatchesLabels(sel, map[string]string{"type": "desktop"}))
}

func TestMatchesLabels_NotIn_ValueNotInList_ReturnsTrue(t *testing.T) {
	sel := &LabelSelector{MatchExpressions: []LabelSelectorRequirement{
		{Key: "type", Operator: "NotIn", Values: []string{"desktop"}},
	}}
	assert.True(t, MatchesLabels(sel, map[string]string{"type": "laptop"}))
}

func TestMatchesLabels_Exists_KeyPresent_ReturnsTrue(t *testing.T) {
	sel := &LabelSelector{MatchExpressions: []LabelSelectorRequirement{
		{Key: "brand", Operator: "Exists"},
	}}
	assert.True(t, MatchesLabels(sel, map[string]string{"brand": "anything"}))
}

func TestMatchesLabels_Exists_KeyAbsent_ReturnsFalse(t *testing.T) {
	sel := &LabelSelector{MatchExpressions: []LabelSelectorRequirement{
		{Key: "brand", Operator: "Exists"},
	}}
	assert.False(t, MatchesLabels(sel, map[string]string{"other": "val"}))
}

func TestMatchesLabels_DoesNotExist_KeyAbsent_ReturnsTrue(t *testing.T) {
	sel := &LabelSelector{MatchExpressions: []LabelSelectorRequirement{
		{Key: "discontinued", Operator: "DoesNotExist"},
	}}
	assert.True(t, MatchesLabels(sel, map[string]string{"brand": "apple"}))
}

func TestMatchesLabels_DoesNotExist_KeyPresent_ReturnsFalse(t *testing.T) {
	sel := &LabelSelector{MatchExpressions: []LabelSelectorRequirement{
		{Key: "discontinued", Operator: "DoesNotExist"},
	}}
	assert.False(t, MatchesLabels(sel, map[string]string{"discontinued": "true"}))
}

func TestMatchesLabels_CombinedMatchLabelsAndExpressions(t *testing.T) {
	sel := &LabelSelector{
		MatchLabels: map[string]string{"brand": "apple"},
		MatchExpressions: []LabelSelectorRequirement{
			{Key: "type", Operator: "In", Values: []string{"laptop", "notebook"}},
			{Key: "discontinued", Operator: "DoesNotExist"},
		},
	}
	// All constraints satisfied
	assert.True(t, MatchesLabels(sel, map[string]string{"brand": "apple", "type": "laptop"}))
	// matchLabels fails
	assert.False(t, MatchesLabels(sel, map[string]string{"brand": "samsung", "type": "laptop"}))
	// In expression fails
	assert.False(t, MatchesLabels(sel, map[string]string{"brand": "apple", "type": "desktop"}))
	// DoesNotExist fails
	assert.False(t, MatchesLabels(sel, map[string]string{"brand": "apple", "type": "laptop", "discontinued": "true"}))
}
