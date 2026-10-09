// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package resolver

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/vektah/gqlparser/v2"
	"github.com/vektah/gqlparser/v2/validator/rules"
)

func TestCreateCategorySchemaContract(t *testing.T) {
	schema := productContractSchema(t)
	requireGraphQLField(t, schema, "Mutation", "createCategory", "CreateCategoryPayload!")
	requireGraphQLField(t, schema, "CreateCategoryInput", "apiVersion", "String!")
	requireGraphQLField(t, schema, "CreateCategoryInput", "kind", "String!")
	requireGraphQLField(t, schema, "CreateCategoryInput", "metadata", "ObjectMetaInput!")
	requireGraphQLField(t, schema, "CreateCategoryInput", "spec", "CategorySpecInput!")
	requireGraphQLField(t, schema, "CreateCategoryInput", "body", "String")
	requireGraphQLField(t, schema, "CategorySpecInput", "title", "String!")
	requireGraphQLField(t, schema, "CategorySpecInput", "parentRef", "CatalogObjectReferenceInput")
	requireGraphQLField(t, schema, "CategorySpecInput", "media", "[MediaDefinitionInput!]")
	requireGraphQLField(t, schema, "CreateCategoryPayload", "category", "Category")
	assert.Equal(t, "catalog.gitstore.dev/v1beta1", schema.Types["CreateCategoryInput"].Fields.ForName("apiVersion").DefaultValue.Raw)
	assert.Equal(t, "CategoryTaxonomy", schema.Types["CreateCategoryInput"].Fields.ForName("kind").DefaultValue.Raw)
	for _, typeName := range []string{"CreateCategoryInput", "CreateCategoryPayload", "UpdateCategoryInput", "UpdateCategoryPayload"} {
		assert.Nil(t, schema.Types[typeName].Fields.ForName("clientMutationId"), "%s must not carry clientMutationId", typeName)
		assert.Nil(t, schema.Types[typeName].Fields.ForName("repository"), "%s must not select a repository", typeName)
		assert.Nil(t, schema.Types[typeName].Fields.ForName("path"), "%s must not select a path", typeName)
	}
}

func TestUpdateCategorySchemaContract(t *testing.T) {
	schema := productContractSchema(t)
	requireGraphQLField(t, schema, "Mutation", "updateCategory", "UpdateCategoryPayload!")
	requireGraphQLField(t, schema, "UpdateCategoryInput", "apiVersion", "String!")
	requireGraphQLField(t, schema, "UpdateCategoryInput", "kind", "String!")
	requireGraphQLField(t, schema, "UpdateCategoryInput", "metadata", "ObjectMetaInput!")
	requireGraphQLField(t, schema, "UpdateCategoryInput", "spec", "CategorySpecInput!")
	requireGraphQLField(t, schema, "UpdateCategoryInput", "body", "String")
	requireGraphQLField(t, schema, "UpdateCategoryPayload", "category", "Category")
}

func TestCategoryHasNoLegacyHierarchyFields(t *testing.T) {
	schema := productContractSchema(t)
	category := schema.Types["Category"]
	require.NotNil(t, category)
	assert.Nil(t, category.Fields.ForName("path"))
	assert.Nil(t, category.Fields.ForName("depth"))
	requireGraphQLField(t, schema, "ResolvedCategoryTaxonomy", "path", "[String!]!")
	requireGraphQLField(t, schema, "ResolvedCategoryTaxonomy", "depth", "Int!")

	_, errs := gqlparser.LoadQueryWithRules(schema, `query { category(by: {namespacePath: {namespace: "n", name: "c"}}) { path } }`, rules.NewDefaultRules())
	require.NotEmpty(t, errs, "selecting the removed path field must fail validation")
	_, errs = gqlparser.LoadQueryWithRules(schema, `query { category(by: {namespacePath: {namespace: "n", name: "c"}}) { status { resolved { path depth } } } }`, rules.NewDefaultRules())
	assert.Empty(t, errs)
}

func TestDeleteCategorySchemaContract(t *testing.T) {
	schema := productContractSchema(t)
	requireGraphQLField(t, schema, "DeleteCategoryInput", "id", "ID!")
	requireGraphQLField(t, schema, "DeleteCategoryPayload", "category", "Category")
	requireGraphQLField(t, schema, "DeleteCategoryPayload", "outcome", "ResourceDeletionOutcome!")
	assert.Nil(t, schema.Types["DeleteCategoryPayload"].Fields.ForName("deletedCategoryId"))
	assert.Nil(t, schema.Types["DeleteCategoryPayload"].Fields.ForName("orphanedProductIds"))

	requireGraphQLField(t, schema, "Mutation", "completeCategoryDeletion", "CompleteCategoryDeletionPayload!")
	requireGraphQLField(t, schema, "CompleteCategoryDeletionInput", "namespace", "String!")
	requireGraphQLField(t, schema, "CompleteCategoryDeletionInput", "name", "String!")
	requireGraphQLField(t, schema, "CompleteCategoryDeletionInput", "resourceVersion", "String!")
	requireGraphQLField(t, schema, "CompleteCategoryDeletionPayload", "id", "ID")

	completeDeletion := schema.Types["UpdateCategoryStatusInput"].Fields.ForName("completeDeletion")
	require.NotNil(t, completeDeletion)
	deprecated := completeDeletion.Directives.ForName("deprecated")
	require.NotNil(t, deprecated, "completeDeletion must be deprecated")
	assert.Contains(t, deprecated.Arguments.ForName("reason").Value.Raw, "completeCategoryDeletion")
}

func TestCategoryFilterSchemaContract(t *testing.T) {
	schema := productContractSchema(t)
	categories := schema.Query.Fields.ForName("categories")
	require.NotNil(t, categories)
	filter := categories.Arguments.ForName("filter")
	require.NotNil(t, filter)
	assert.Equal(t, "CategoryFilterInput", filter.Type.String())
	requireGraphQLField(t, schema, "CategoryFilterInput", "descendantOf", "String!")
	requireGraphQLField(t, schema, "CategoryFilterInput", "includeSelf", "Boolean")
	requireGraphQLField(t, schema, "CategoryFilterInput", "maxDepth", "Int")
	assert.Equal(t, "false", schema.Types["CategoryFilterInput"].Fields.ForName("includeSelf").DefaultValue.Raw)
}
