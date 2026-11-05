// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package resolver

import (
	"testing"

	"github.com/gitstore-dev/gitstore/api/internal/admin/graph/generated"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/vektah/gqlparser/v2"
	"github.com/vektah/gqlparser/v2/ast"
	"github.com/vektah/gqlparser/v2/validator/rules"
)

const legacyNamespaceDeletionSchema = `
schema { mutation: Mutation }
type Mutation {
  deleteNamespace(input: DeleteNamespaceInput!): DeleteNamespacePayload!
}
input DeleteNamespaceInput {
  identifier: String!
}
type DeleteNamespacePayload {
  deletedIdentifier: String!
}
`

const legacyDeleteNamespaceSelection = `
mutation {
  deleteNamespace(input: {identifier: "acme"}) {
    deletedIdentifier
  }
}
`

const outcomeDeleteNamespaceSelection = `
mutation {
  deleteNamespace(input: {id: "gid://GitStore/Namespace/acme"}) {
    namespace { id }
    outcome
  }
}
`

func TestNamespaceGraphQLServerRejectsLegacyDeleteSelector(t *testing.T) {
	oldSchema, err := gqlparser.LoadSchema(&ast.Source{Name: "legacy-namespace.graphqls", Input: legacyNamespaceDeletionSchema})
	require.NoError(t, err)
	newSchema := generated.NewExecutableSchema(generated.Config{Resolvers: &Resolver{}}).Schema()

	assertNamespaceSelectionValid(t, oldSchema, legacyDeleteNamespaceSelection, true)
	assertNamespaceSelectionValid(t, newSchema, legacyDeleteNamespaceSelection, false)
	assertNamespaceSelectionValid(t, oldSchema, outcomeDeleteNamespaceSelection, false)
	assertNamespaceSelectionValid(t, newSchema, outcomeDeleteNamespaceSelection, true)
}

func TestNamespaceOutcomeActivationWaitsForFullAPIFleetConvergence(t *testing.T) {
	oldSchema, err := gqlparser.LoadSchema(&ast.Source{Name: "legacy-namespace.graphqls", Input: legacyNamespaceDeletionSchema})
	require.NoError(t, err)
	newSchema := generated.NewExecutableSchema(generated.Config{Resolvers: &Resolver{}}).Schema()

	assert.False(t, namespaceFleetSupportsOutcome(newSchema, oldSchema), "mixed fleets must keep clients on the legacy selection")
	assert.True(t, namespaceFleetSupportsOutcome(newSchema, newSchema), "clients may activate outcome only after every replica exposes it")
	assert.False(t, namespaceFleetSupportsOutcome(oldSchema, newSchema), "rollback must disable outcome before the first old replica returns")
}

func namespaceFleetSupportsOutcome(schemas ...*ast.Schema) bool {
	if len(schemas) == 0 {
		return false
	}
	for _, schema := range schemas {
		payload := schema.Types["DeleteNamespacePayload"]
		if payload == nil || payload.Fields.ForName("outcome") == nil {
			return false
		}
	}
	return true
}

func assertNamespaceSelectionValid(t *testing.T, schema *ast.Schema, query string, valid bool) {
	t.Helper()
	_, errs := gqlparser.LoadQueryWithRules(schema, query, rules.NewDefaultRules())
	if valid {
		require.Empty(t, errs)
		return
	}
	require.NotEmpty(t, errs)
}

// This frozen projection of the previous metadata schema tests client/schema
// compatibility. It does not emulate an old API binary's runtime validation.
const legacyMetadataSchema = `
schema { query: Query, mutation: Mutation }
type Query { namespace: Namespace }
type Mutation { createNamespace(input: CreateNamespaceInput!): CreateNamespacePayload! }
type Namespace { metadata: NamespaceMetadata! }
type NamespaceMetadata { name: String! }
type ObjectMeta { name: String!, namespace: String! }
input ObjectMetaInput { name: String!, namespace: String! = "" }
input NamespaceMetadataInput { name: String! }
input NamespaceSpecInput { tier: NamespaceTier! }
enum NamespaceTier { USER ORGANIZATION }
input CreateNamespaceInput { metadata: NamespaceMetadataInput!, spec: NamespaceSpecInput! }
type CreateNamespacePayload { namespace: Namespace }
`

func TestMetadataClientRolloutRequiresFleetConvergence(t *testing.T) {
	oldSchema, err := gqlparser.LoadSchema(&ast.Source{Name: "legacy-metadata.graphqls", Input: legacyMetadataSchema})
	require.NoError(t, err)
	newSchema := namespaceContractSchema(t)
	for _, schema := range []*ast.Schema{oldSchema, newSchema} {
		// Inline common fields and omission of cluster namespace work throughout rollout.
		assertNamespaceSelectionValid(t, schema, `mutation { createNamespace(input:{metadata:{name:"acme"},spec:{tier:USER}}){namespace{metadata{name}}}}`, true)
	}
	legacyFragment := `mutation { createNamespace(input:{metadata:{name:"acme"},spec:{tier:USER}}){namespace{metadata{...meta}}}} fragment meta on NamespaceMetadata {name}`
	sharedFragment := `mutation { createNamespace(input:{metadata:{name:"acme"},spec:{tier:USER}}){namespace{metadata{...meta}}}} fragment meta on ObjectMeta {name namespace}`
	assertNamespaceSelectionValid(t, oldSchema, legacyFragment, true)
	assertNamespaceSelectionValid(t, newSchema, legacyFragment, false)
	assertNamespaceSelectionValid(t, oldSchema, sharedFragment, false)
	assertNamespaceSelectionValid(t, newSchema, sharedFragment, true)
	assert.NotNil(t, oldSchema.Types["ObjectMetaInput"].Fields.ForName("namespace").DefaultValue)
	assert.Nil(t, newSchema.Types["ObjectMetaInput"].Fields.ForName("namespace").DefaultValue)
	assert.Nil(t, newSchema.Types["NamespaceMetadata"])
	assert.Nil(t, newSchema.Types["NamespaceMetadataInput"])
}
