// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package integration

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestProductAuthorization_TwoUserNamespaceIsolation verifies the Product
// lifecycle's end-to-end no-disclosure boundary. The harness AuthZ provider
// allows an owner to manage its own resources but denies a different subject
// before a resolver can reveal a Product in another namespace.
func TestProductAuthorization_TwoUserNamespaceIsolation(t *testing.T) {
	h := newNamespaceContractHarness(t)
	aliceToken := namespaceContractLogin(t, h, "alice", "admin123")
	bobToken := namespaceContractLogin(t, h, "bob", "admin123")
	aliceNamespace := uniqueName("product-alice")
	bobNamespace := uniqueName("product-bob")
	aliceProduct := uniqueName("widget-alice")
	bobProduct := uniqueName("widget-bob")

	createNamespaceAsUser(t, h, aliceToken, aliceNamespace)
	createNamespaceAsUser(t, h, bobToken, bobNamespace)
	t.Cleanup(func() {
		h.cleanupNamespace(aliceNamespace)
		h.cleanupNamespace(bobNamespace)
	})

	aliceID := createProductAsUser(t, h, aliceToken, aliceNamespace, aliceProduct)
	_ = createProductAsUser(t, h, bobToken, bobNamespace, bobProduct)

	assertProductVisibleToOwner(t, h, aliceToken, aliceNamespace, aliceProduct)
	assertProductVisibleToOwner(t, h, bobToken, bobNamespace, bobProduct)

	for _, tc := range []struct {
		name  string
		query string
		vars  map[string]any
	}{
		{
			name: "lookup",
			query: `query($namespace: String!, $name: String!) {
				product(by: {namespacePath: {namespace: $namespace, name: $name}}) { id }
			}`,
			vars: map[string]any{"namespace": aliceNamespace, "name": aliceProduct},
		},
		{
			name: "list",
			query: `query($namespace: String!) {
				products(namespace: $namespace, first: 10) { edges { node { id } } }
			}`,
			vars: map[string]any{"namespace": aliceNamespace},
		},
		{
			name:  "delete",
			query: `mutation($id: ID!) { deleteProduct(input: {id: $id}) { outcome } }`,
			vars:  map[string]any{"id": aliceID},
		},
	} {
		t.Run("bob_cannot_"+tc.name+"_alice_product", func(t *testing.T) {
			response := h.gqlWithToken(bobToken, tc.query, tc.vars)
			require.Len(t, response.Errors, 1, namespaceContractErrors(response.Errors))
			assert.Contains(t, namespaceContractErrors(response.Errors), "resource belongs to another user")
		})
	}
}

func createProductAsUser(t *testing.T, h *namespaceContractHarness, token, namespace, name string) string {
	t.Helper()
	response := h.gqlWithToken(token, `
		mutation($namespace: String!, $name: String!) {
			createProduct(input: {
				apiVersion: "catalog.gitstore.dev/v1beta1"
				kind: "Product"
				metadata: {namespace: $namespace, name: $name}
				spec: {title: "Authorization contract product"}
			}) {
				product { id metadata { namespace name } }
			}
		}
	`, map[string]any{"namespace": namespace, "name": name})
	require.Empty(t, response.Errors, namespaceContractErrors(response.Errors))
	var data struct {
		CreateProduct struct {
			Product struct {
				ID       string `json:"id"`
				Metadata struct {
					Namespace string `json:"namespace"`
					Name      string `json:"name"`
				} `json:"metadata"`
			} `json:"product"`
		} `json:"createProduct"`
	}
	require.NoError(t, json.Unmarshal(response.Data, &data))
	assert.Equal(t, namespace, data.CreateProduct.Product.Metadata.Namespace)
	assert.Equal(t, name, data.CreateProduct.Product.Metadata.Name)
	require.NotEmpty(t, data.CreateProduct.Product.ID)
	return data.CreateProduct.Product.ID
}

func assertProductVisibleToOwner(t *testing.T, h *namespaceContractHarness, token, namespace, name string) {
	t.Helper()
	response := h.gqlWithToken(token, `
		query($namespace: String!, $name: String!) {
			product(by: {namespacePath: {namespace: $namespace, name: $name}}) {
				metadata { namespace name }
			}
		}
	`, map[string]any{"namespace": namespace, "name": name})
	require.Empty(t, response.Errors, namespaceContractErrors(response.Errors))
}
