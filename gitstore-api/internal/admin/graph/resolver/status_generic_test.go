// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package resolver

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/vektah/gqlparser/v2/gqlerror"
)

func TestStatusConflictErrorUsesFoldedConflictEnvelope(t *testing.T) {
	err := statusConflictError("CategoryTaxonomy", "gitstore", "electronics", "42")
	var graphErr *gqlerror.Error
	require.ErrorAs(t, err, &graphErr)
	assert.Equal(t, "CONFLICT", graphErr.Extensions["code"])
	assert.NotContains(t, graphErr.Extensions, "resourceVersion")
	assert.NotContains(t, graphErr.Extensions, "phase")
	diagnostics, ok := graphErr.Extensions["diagnostics"].([]map[string]any)
	require.True(t, ok)
	require.Len(t, diagnostics, 1)
	assert.Equal(t, "RESOURCE_VERSION_CONFLICT", diagnostics[0]["reason"])
	assert.Equal(t, "FAILURE", diagnostics[0]["level"])
	assert.Contains(t, diagnostics[0]["message"], "42", "the current resource version moves into the message")
	assert.Contains(t, graphErr.Message, "gitstore/electronics")
}
