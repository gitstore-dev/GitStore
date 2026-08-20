// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package integration

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRepositoryAuthorization_TwoUserNamespaceIsolation(t *testing.T) {
	h := newNamespaceContractHarness(t)
	aliceNamespace := uniqueName("alice-namespace")
	bobNamespace := uniqueName("bob-namespace")
	aliceRepository := uniqueName("alice-repository")
	bobRepository := uniqueName("bob-repository")

	createNamespaceAsUser(t, h, "test-user:alice", aliceNamespace)
	createNamespaceAsUser(t, h, "test-user:bob", bobNamespace)
	aliceRepositoryID := createRepositoryAsUser(t, h, "test-user:alice", aliceNamespace, aliceRepository)
	bobRepositoryID := createRepositoryAsUser(t, h, "test-user:bob", bobNamespace, bobRepository)
	t.Cleanup(func() {
		repositoryReadContractDelete(t, h, aliceRepositoryID)
		repositoryReadContractDelete(t, h, bobRepositoryID)
		h.cleanupNamespace(aliceNamespace)
		h.cleanupNamespace(bobNamespace)
	})

	aliceRepositories := repositoriesAsUser(t, h, "test-user:alice", aliceNamespace)
	bobRepositories := repositoriesAsUser(t, h, "test-user:bob", bobNamespace)

	assertRepositoryNamespaceIsolation(t, aliceRepositories, aliceNamespace, aliceRepository, bobRepository, "alice")
	assertRepositoryNamespaceIsolation(t, bobRepositories, bobNamespace, bobRepository, aliceRepository, "bob")
}

type authzRepositoryNode struct {
	ID        string `json:"id"`
	CreatedBy string `json:"createdBy"`
	Metadata  struct {
		Name      string `json:"name"`
		Namespace string `json:"namespace"`
		UID       string `json:"uid"`
	} `json:"metadata"`
}

func createNamespaceAsUser(t *testing.T, h *namespaceContractHarness, token, namespace string) {
	t.Helper()
	resp := h.gqlWithToken(token, `
		mutation($namespace: String!) {
			createNamespace(input: {
				apiVersion: "gitstore.dev/v1beta1"
				kind: "Namespace"
				metadata: {name: $namespace}
				spec: {title: "User namespace", tier: USER}
			}) {
				namespace {
					id
					metadata {
						uid
						name
					}
					createdBy
				}
			}
		}
	`, map[string]any{"namespace": namespace})
	require.Empty(t, resp.Errors, namespaceContractErrors(resp.Errors))

	var data struct {
		CreateNamespace struct {
			Namespace struct {
				ID        string `json:"id"`
				CreatedBy string `json:"createdBy"`
				Metadata  struct {
					UID  string `json:"uid"`
					Name string `json:"name"`
				} `json:"metadata"`
			} `json:"namespace"`
		} `json:"createNamespace"`
	}
	require.NoError(t, json.Unmarshal(resp.Data, &data))
	assert.Equal(t, namespace, data.CreateNamespace.Namespace.Metadata.Name)
	assert.NotEmpty(t, data.CreateNamespace.Namespace.Metadata.UID)
	assert.NotEqual(t, data.CreateNamespace.Namespace.ID, data.CreateNamespace.Namespace.Metadata.UID)
	assert.Equal(t, token[len("test-user:"):], data.CreateNamespace.Namespace.CreatedBy)
}

func createRepositoryAsUser(
	t *testing.T,
	h *namespaceContractHarness,
	token,
	namespace,
	name string,
) string {
	t.Helper()
	resp := h.gqlWithToken(token, `
		mutation($namespace: String!, $name: String!) {
			createRepository(input: {namespace: $namespace, name: $name, defaultBranch: "main"}) {
				repository {
					id
					metadata {
						uid
						namespace
					}
					createdBy
				}
			}
		}
	`, map[string]any{"namespace": namespace, "name": name})
	require.Empty(t, resp.Errors, namespaceContractErrors(resp.Errors))

	var data struct {
		CreateRepository struct {
			Repository authzRepositoryNode `json:"repository"`
		} `json:"createRepository"`
	}
	require.NoError(t, json.Unmarshal(resp.Data, &data))
	repository := data.CreateRepository.Repository
	assert.Equal(t, namespace, repository.Metadata.Namespace)
	assert.NotEmpty(t, repository.Metadata.UID)
	assert.NotEqual(t, repository.ID, repository.Metadata.UID)
	assert.Equal(t, token[len("test-user:"):], repository.CreatedBy)
	return repository.ID
}

func repositoriesAsUser(
	t *testing.T,
	h *namespaceContractHarness,
	token,
	namespace string,
) []authzRepositoryNode {
	t.Helper()
	resp := h.gqlWithToken(token, `
		query($namespace: String!) {
			repositories(namespace: $namespace, first: 100) {
				edges {
					node {
						id
						metadata {
							name
							namespace
							uid
						}
						createdBy
					}
				}
			}
		}
	`, map[string]any{"namespace": namespace})
	require.Empty(t, resp.Errors, namespaceContractErrors(resp.Errors))

	var data struct {
		Repositories struct {
			Edges []struct {
				Node authzRepositoryNode `json:"node"`
			} `json:"edges"`
		} `json:"repositories"`
	}
	require.NoError(t, json.Unmarshal(resp.Data, &data))
	nodes := make([]authzRepositoryNode, 0, len(data.Repositories.Edges))
	for _, edge := range data.Repositories.Edges {
		nodes = append(nodes, edge.Node)
	}
	return nodes
}

func assertRepositoryNamespaceIsolation(
	t *testing.T,
	repositories []authzRepositoryNode,
	namespace,
	expectedName,
	forbiddenName,
	expectedActor string,
) {
	t.Helper()
	var found bool
	for _, repository := range repositories {
		assert.Equal(t, namespace, repository.Metadata.Namespace)
		assert.NotEqual(t, forbiddenName, repository.Metadata.Name)
		assert.NotEmpty(t, repository.Metadata.UID)
		assert.NotEqual(t, repository.ID, repository.Metadata.UID)
		if repository.Metadata.Name == expectedName {
			found = true
			assert.Equal(t, expectedActor, repository.CreatedBy)
		}
	}
	assert.True(t, found, "repository %q not found in namespace %q", expectedName, namespace)
}
