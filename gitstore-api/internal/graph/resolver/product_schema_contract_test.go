// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package resolver

import (
	"testing"

	"github.com/gitstore-dev/gitstore/api/internal/graph/generated"
	"github.com/gitstore-dev/gitstore/api/internal/graph/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/vektah/gqlparser/v2/ast"
)

func productContractSchema(t *testing.T) *ast.Schema {
	t.Helper()
	return generated.NewExecutableSchema(generated.Config{Resolvers: &Resolver{}}).Schema()
}

func TestProductLifecycleSchemaContract(t *testing.T) {
	schema := productContractSchema(t)

	for _, inputName := range []string{"CreateProductInput", "UpdateProductInput"} {
		requireGraphQLField(t, schema, inputName, "apiVersion", "String!")
		requireGraphQLField(t, schema, inputName, "kind", "String!")
		requireGraphQLField(t, schema, inputName, "metadata", "ObjectMetaInput!")
		requireGraphQLField(t, schema, inputName, "spec", "ProductSpecInput!")
		assert.Nil(t, schema.Types[inputName].Fields.ForName("repository"), "%s must route through persisted provenance", inputName)
		assert.Nil(t, schema.Types[inputName].Fields.ForName("path"), "%s must route through persisted provenance", inputName)
	}

	requireGraphQLField(t, schema, "DeleteProductInput", "id", "ID")
	deleteID := schema.Types["DeleteProductInput"].Fields.ForName("id")
	require.NotNil(t, deleteID.Description)
	assert.Contains(t, deleteID.Description, "Opaque global Product Node ID")
	assert.Nil(t, schema.Types["DeleteProductInput"].Fields.ForName("uid"))
	assert.Nil(t, schema.Types["DeleteProductInput"].Fields.ForName("namespace"))
	assert.Nil(t, schema.Types["DeleteProductInput"].Fields.ForName("name"))
	requireGraphQLField(t, schema, "DeleteProductPayload", "product", "Product")
	requireGraphQLField(t, schema, "DeleteProductPayload", "outcome", "ResourceDeletionOutcome!")
	requireGraphQLField(t, schema, "Mutation", "createProduct", "CreateProductPayload!")
	requireGraphQLField(t, schema, "Mutation", "updateProduct", "UpdateProductPayload!")
	requireGraphQLField(t, schema, "Mutation", "deleteProduct", "DeleteProductPayload!")

	outcome := schema.Types["ResourceDeletionOutcome"]
	require.NotNil(t, outcome)
	assert.Equal(t, ast.Enum, outcome.Kind)
	assert.NotNil(t, outcome.EnumValues.ForName("TERMINATION_STARTED"))
	assert.NotNil(t, outcome.EnumValues.ForName("ALREADY_TERMINATING"))

	requireGraphQLField(t, schema, "ProductSpec", "lifecycle", "ProductLifecycleSpec!")
	requireGraphQLField(t, schema, "ProductSpecInput", "lifecycle", "ProductLifecycleSpecInput")
	requireGraphQLField(t, schema, "ProductLifecycleSpec", "state", "ProductLifecycleState!")
}

func TestProductLifecycleDefaultsActiveForLegacyProducts(t *testing.T) {
	spec := specFromJSON(nil)
	require.NotNil(t, spec.Lifecycle)
	assert.Equal(t, model.ProductLifecycleStateActive, spec.Lifecycle.State)
}
