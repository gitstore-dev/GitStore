// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package resolver

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNamespaceWatchSchemaIsTypedAndClusterScoped(t *testing.T) {
	schema := namespaceContractSchema(t)
	field := requireGraphQLField(t, schema, "Subscription", "watchNamespaces", "NamespaceWatchEvent!")
	assert.Nil(t, field.Arguments.ForName("namespace"))
	assert.Equal(t, "LabelSelectorInput", field.Arguments.ForName("selector").Type.String())
	assert.Equal(t, "String", field.Arguments.ForName("resourceVersion").Type.String())

	event := schema.Types["NamespaceWatchEvent"]
	require.NotNil(t, event)
	requireGraphQLField(t, schema, "NamespaceWatchEvent", "type", "WatchEventType!")
	requireGraphQLField(t, schema, "NamespaceWatchEvent", "name", "String!")
	requireGraphQLField(t, schema, "NamespaceWatchEvent", "resourceVersion", "String!")
	requireGraphQLField(t, schema, "NamespaceWatchEvent", "namespace", "Namespace")
}
