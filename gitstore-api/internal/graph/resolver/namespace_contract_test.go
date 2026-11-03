// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package resolver

import (
	"testing"
	"time"

	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	"github.com/gitstore-dev/gitstore/api/internal/graph/generated"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/vektah/gqlparser/v2/ast"
)

func namespaceContractSchema(t *testing.T) *ast.Schema {
	t.Helper()
	return generated.NewExecutableSchema(generated.Config{Resolvers: &Resolver{}}).Schema()
}

func namespaceContractFixture(id, name string) *datastore.Namespace {
	now := time.Date(2026, 8, 16, 1, 0, 0, 0, time.UTC)
	return &datastore.Namespace{
		ID:                id,
		Name:              name,
		Title:             "Acme Store",
		Tier:              datastore.NamespaceTierUser,
		CreationTimestamp: now,
		CreationActor:     "creator",
		UpdateTimestamp:   now.Add(time.Hour),
		UpdateActor:       "updater",
	}
}

func requireGraphQLField(t *testing.T, schema *ast.Schema, typeName, fieldName, fieldType string) *ast.FieldDefinition {
	t.Helper()
	definition := schema.Types[typeName]
	require.NotNil(t, definition, "missing GraphQL type %s", typeName)
	field := definition.Fields.ForName(fieldName)
	require.NotNil(t, field, "missing GraphQL field %s.%s", typeName, fieldName)
	assert.Equal(t, fieldType, field.Type.String(), "%s.%s type", typeName, fieldName)
	return field
}

func TestNamespaceDeclarativeSchemaContract(t *testing.T) {
	schema := namespaceContractSchema(t)

	for field, fieldType := range map[string]string{
		"id":         "ID!",
		"apiVersion": "String!",
		"kind":       "String!",
		"metadata":   "ObjectMeta!",
		"spec":       "NamespaceSpec!",
		"status":     "NamespaceStatus!",
	} {
		requireGraphQLField(t, schema, "Namespace", field, fieldType)
	}

	metadata := schema.Types["ObjectMeta"]
	require.NotNil(t, metadata)
	requireGraphQLField(t, schema, "ObjectMeta", "namespace", "String")
	for field, fieldType := range map[string]string{
		"name":              "String!",
		"labels":            "JSON",
		"annotations":       "JSON",
		"uid":               "ID!",
		"resourceVersion":   "String!",
		"generation":        "Int!",
		"creationTimestamp": "DateTime!",
		"revision":          "String",
		"ownerReferences":   "[OwnerReference!]!",
		"finalizers":        "[String!]!",
	} {
		requireGraphQLField(t, schema, "ObjectMeta", field, fieldType)
	}

	requireGraphQLField(t, schema, "ObjectMeta", "namespace", "String")

	for field, fieldType := range map[string]string{
		"title":              "String",
		"tier":               "NamespaceTier!",
		"repositoryDefaults": "NamespaceRepositoryDefaults",
		"pushPolicyDefaults": "NamespacePushPolicyDefaults",
	} {
		requireGraphQLField(t, schema, "NamespaceSpec", field, fieldType)
	}
	requireGraphQLField(t, schema, "NamespaceRepositoryDefaults", "visibility", "RepositoryVisibility")
	requireGraphQLField(t, schema, "NamespaceRepositoryDefaults", "defaultBranch", "String")
	requireGraphQLField(t, schema, "NamespacePushPolicyDefaults", "maxPackSizeBytes", "Long")
	requireGraphQLField(t, schema, "NamespacePushPolicyDefaults", "maxFileSizeBytes", "Long")
	requireGraphQLField(t, schema, "NamespacePushPolicyDefaults", "receivePackHooks", "ReceivePackHookDefaults")
	requireGraphQLField(t, schema, "NamespacePushPolicyDefaults", "schemaValidation", "SchemaValidationDefaults")
	requireGraphQLField(t, schema, "NamespacePushPolicyDefaults", "admissionControl", "AdmissionControlDefaults")

	requireGraphQLField(t, schema, "NamespaceStatus", "observedGeneration", "Int!")
	requireGraphQLField(t, schema, "NamespaceStatus", "lastAppliedRevision", "String")
	requireGraphQLField(t, schema, "NamespaceStatus", "conditions", "[Condition!]!")
	assert.Nil(t, schema.Types["NamespaceStatus"].Fields.ForName("phase"))
	assert.Nil(t, schema.Types["NamespaceStatus"].Fields.ForName("resolved"))

	require.NotNil(t, schema.Types["Long"])
	assert.Equal(t, ast.Scalar, schema.Types["Long"].Kind)

	for _, inputName := range []string{"CreateNamespaceInput", "UpdateNamespaceInput"} {
		for field, fieldType := range map[string]string{
			"apiVersion": "String!",
			"kind":       "String!",
			"metadata":   "ObjectMetaInput!",
			"spec":       "NamespaceSpecInput!",
		} {
			requireGraphQLField(t, schema, inputName, field, fieldType)
		}
	}
	requireGraphQLField(t, schema, "ObjectMetaInput", "name", "String!")
	requireGraphQLField(t, schema, "ObjectMetaInput", "labels", "JSON")
	requireGraphQLField(t, schema, "ObjectMetaInput", "annotations", "JSON")
	requireGraphQLField(t, schema, "NamespaceSpecInput", "title", "String")
	requireGraphQLField(t, schema, "NamespaceSpecInput", "tier", "NamespaceTier!")
	requireGraphQLField(t, schema, "Mutation", "updateNamespace", "UpdateNamespacePayload!")
}

func TestNamespaceUsesSharedMetadata(t *testing.T) {
	schema := namespaceContractSchema(t)
	requireGraphQLField(t, schema, "Namespace", "metadata", "ObjectMeta!")
	requireGraphQLField(t, schema, "Repository", "metadata", "ObjectMeta!")
	metadata := datastoreNamespaceToModel(namespaceContractFixture("00000000-0000-0000-0000-000000000001", "acme")).Metadata
	require.NotNil(t, metadata)
	assert.Nil(t, metadata.Namespace)
	assert.Equal(t, "acme", metadata.Name)
}

func TestNamespaceHasNoDeprecatedLegacyFields(t *testing.T) {
	schema := namespaceContractSchema(t)
	for _, fieldName := range []string{
		"identifier",
		"displayName",
		"tier",
		"createdAt",
		"createdBy",
		"updatedAt",
		"updatedBy",
	} {
		assert.Nil(t, schema.Types["Namespace"].Fields.ForName(fieldName), "Namespace.%s should have been removed", fieldName)
	}
	assert.Nil(t, schema.Types["NamespaceBy"].Fields.ForName("identifier"), "NamespaceBy.identifier should have been removed")
}

func TestRepositoryVisibilityContract(t *testing.T) {
	values := namespaceContractSchema(t).Types["RepositoryVisibility"]
	require.NotNil(t, values)
	assert.Equal(t, ast.Enum, values.Kind)
	assert.ElementsMatch(t, []string{"PUBLIC", "PRIVATE", "INTERNAL"}, []string{
		values.EnumValues[0].Name,
		values.EnumValues[1].Name,
		values.EnumValues[2].Name,
	})
}
