// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package schemavalidate

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestControllerLoadsOnlyAdminAndCommon(t *testing.T) {
	schema, err := load()
	require.NoError(t, err)
	require.NotNil(t, schema.Mutation)
	require.NotNil(t, schema.Subscription)
	require.Nil(t, schema.Types["StorefrontProduct"])
	for _, name := range []string{"Product", "Repository", "Node"} {
		definition := schema.Types[name]
		require.NotNil(t, definition)
		source := filepath.ToSlash(definition.Position.Src.Name)
		require.True(t, strings.Contains(source, "/admin/") || strings.Contains(source, "/common/"), "wrong source set for %s: %s", name, source)
	}
}
