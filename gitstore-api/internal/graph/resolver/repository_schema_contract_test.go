// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package resolver

import (
	"strings"
	"testing"

	"github.com/gitstore-dev/gitstore/api/internal/graph/generated"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/vektah/gqlparser/v2/ast"
)

func repositoryContractSchema(t *testing.T) *ast.Schema {
	t.Helper()
	return generated.NewExecutableSchema(generated.Config{Resolvers: &Resolver{}}).Schema()
}

func TestRepositoryDeclarativeSchemaContract(t *testing.T) {
	schema := repositoryContractSchema(t)

	for field, fieldType := range map[string]string{
		"id":         "ID!",
		"apiVersion": "String!",
		"kind":       "String!",
		"metadata":   "ObjectMeta!",
		"spec":       "RepositorySpec!",
		"status":     "RepositoryStatus!",
	} {
		requireGraphQLField(t, schema, "Repository", field, fieldType)
	}

	for field, fieldType := range map[string]string{
		"defaultBranch": "String!",
		"visibility":    "RepositoryVisibility!",
		"pushPolicy":    "RepositoryPushPolicy!",
	} {
		requireGraphQLField(t, schema, "RepositorySpec", field, fieldType)
	}

	for field, fieldType := range map[string]string{
		"maxPackSizeBytes": "Long!",
		"maxFileSizeBytes": "Long!",
		"receivePackHooks": "ReceivePackHookDefaults",
		"schemaValidation": "SchemaValidationDefaults",
		"admissionControl": "AdmissionControlDefaults",
	} {
		requireGraphQLField(t, schema, "RepositoryPushPolicy", field, fieldType)
	}

	for field, fieldType := range map[string]string{
		"observedGeneration":  "Int!",
		"lastAppliedRevision": "String",
		"conditions":          "[Condition!]!",
		"resolved":            "ResolvedRepositoryDefinition!",
	} {
		requireGraphQLField(t, schema, "RepositoryStatus", field, fieldType)
	}
	requireGraphQLField(t, schema, "ResolvedRepositoryDefinition", "storagePath", "String!")
	requireGraphQLField(t, schema, "ResolvedRepositoryDefinition", "storageClass", "String!")

	require.NotNil(t, schema.Types["Long"])
	assert.Equal(t, ast.Scalar, schema.Types["Long"].Kind)
	require.NotNil(t, schema.Types["RepositoryVisibility"])
	assert.Equal(t, ast.Enum, schema.Types["RepositoryVisibility"].Kind)
}

func TestRepositoryUsesSharedMetadataAndConditionTypes(t *testing.T) {
	schema := repositoryContractSchema(t)

	requireGraphQLField(t, schema, "Repository", "metadata", "ObjectMeta!")
	requireGraphQLField(t, schema, "RepositoryStatus", "conditions", "[Condition!]!")
	assert.Nil(t, schema.Types["RepositoryMetadata"])
	assert.Nil(t, schema.Types["RepositoryCondition"])
}

func TestRepositoryLegacyFieldsAreDeprecated(t *testing.T) {
	schema := repositoryContractSchema(t)
	replacements := map[string]string{
		"name":          "metadata.name",
		"namespace":     "metadata.namespace",
		"defaultBranch": "spec.defaultBranch",
		"storageClass":  "status.resolved.storageClass",
		"storagePath":   "status.resolved.storagePath",
		"createdAt":     "metadata.creationTimestamp",
	}
	for fieldName, replacement := range replacements {
		field := schema.Types["Repository"].Fields.ForName(fieldName)
		require.NotNil(t, field)
		directive := field.Directives.ForName("deprecated")
		require.NotNil(t, directive, "Repository.%s must be deprecated", fieldName)
		reason := directive.Arguments.ForName("reason")
		require.NotNil(t, reason)
		assert.Contains(t, reason.Value.Raw, replacement)
		assert.Contains(t, reason.Value.Raw, "future major GraphQL API release")
	}

	for _, fieldName := range []string{"createdBy", "updatedAt", "updatedBy"} {
		field := schema.Types["Repository"].Fields.ForName(fieldName)
		require.NotNil(t, field)
		directive := field.Directives.ForName("deprecated")
		require.NotNil(t, directive, "Repository.%s must be deprecated", fieldName)
		reason := directive.Arguments.ForName("reason")
		require.NotNil(t, reason)
		assert.True(t, strings.Contains(reason.Value.Raw, "Legacy audit field"))
	}

	assert.Nil(t, schema.Types["Repository"].Fields.ForName("id").Directives.ForName("deprecated"))
}
