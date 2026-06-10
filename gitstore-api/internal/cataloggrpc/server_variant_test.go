// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package cataloggrpc

import (
	"testing"

	"github.com/gitstore-dev/gitstore/api/internal/catalog"
	"github.com/stretchr/testify/assert"
)

func TestValidateSelectedOptions_AcceptsKnownNamesAndValues(t *testing.T) {
	parentSpec := []byte(`{"options":[{"name":"size","values":["S","M"]},{"name":"material","values":[]}]}`)
	selected := []catalog.SelectedOptionDefinition{
		{Name: "size", Value: "M"},
		{Name: "material", Value: "cotton"},
	}

	ok, msg := validateSelectedOptions(selected, parentSpec)
	assert.True(t, ok)
	assert.Empty(t, msg)
}

func TestValidateSelectedOptions_RejectsUnknownOptionName(t *testing.T) {
	parentSpec := []byte(`{"options":[{"name":"size","values":["S","M"]}]}`)
	selected := []catalog.SelectedOptionDefinition{{Name: "color", Value: "red"}}

	ok, msg := validateSelectedOptions(selected, parentSpec)
	assert.False(t, ok)
	assert.Contains(t, msg, `name "color"`)
}

func TestValidateSelectedOptions_RejectsUnknownOptionValue(t *testing.T) {
	parentSpec := []byte(`{"options":[{"name":"size","values":["S","M"]}]}`)
	selected := []catalog.SelectedOptionDefinition{{Name: "size", Value: "XL"}}

	ok, msg := validateSelectedOptions(selected, parentSpec)
	assert.False(t, ok)
	assert.Contains(t, msg, `value "XL"`)
	assert.Contains(t, msg, `option "size"`)
}
