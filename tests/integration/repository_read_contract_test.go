// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package integration

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type repositoryReadContractResource struct {
	ID         string                          `json:"id"`
	APIVersion string                          `json:"apiVersion"`
	Kind       string                          `json:"kind"`
	Metadata   *repositoryReadContractMetadata `json:"metadata"`
	Spec       *repositoryReadContractSpec     `json:"spec"`
	Status     *repositoryReadContractStatus   `json:"status"`
	Body       *string                         `json:"body"`
}

type repositoryReadContractMetadata struct {
	Name              string                         `json:"name"`
	Namespace         string                         `json:"namespace"`
	Labels            map[string]any                 `json:"labels"`
	Annotations       map[string]any                 `json:"annotations"`
	UID               string                         `json:"uid"`
	ResourceVersion   string                         `json:"resourceVersion"`
	Generation        int                            `json:"generation"`
	CreationTimestamp string                         `json:"creationTimestamp"`
	Revision          *string                        `json:"revision"`
	OwnerReferences   []repositoryReadOwnerReference `json:"ownerReferences"`
	Finalizers        []string                       `json:"finalizers"`
}

type repositoryReadOwnerReference struct {
	APIVersion string `json:"apiVersion"`
	Kind       string `json:"kind"`
	Name       string `json:"name"`
	UID        string `json:"uid"`
}

type repositoryReadContractSpec struct {
	DefaultBranch string                            `json:"defaultBranch"`
	Visibility    string                            `json:"visibility"`
	PushPolicy    *repositoryReadContractPushPolicy `json:"pushPolicy"`
}

type repositoryReadContractPushPolicy struct {
	MaxPackSizeBytes int64           `json:"maxPackSizeBytes"`
	MaxFileSizeBytes int64           `json:"maxFileSizeBytes"`
	ReceivePackHooks json.RawMessage `json:"receivePackHooks"`
	SchemaValidation json.RawMessage `json:"schemaValidation"`
	AdmissionControl json.RawMessage `json:"admissionControl"`
}

type repositoryReadContractStatus struct {
	ObservedGeneration  int                               `json:"observedGeneration"`
	LastAppliedRevision *string                           `json:"lastAppliedRevision"`
	Conditions          []repositoryReadContractCondition `json:"conditions"`
	Resolved            *repositoryReadContractResolved   `json:"resolved"`
}

type repositoryReadContractCondition struct {
	Type   string `json:"type"`
	Status string `json:"status"`
}

type repositoryReadContractResolved struct {
	StoragePath  string `json:"storagePath"`
	StorageClass string `json:"storageClass"`
}

func TestRepositoryReadContract_LegacyAndCreatedRepositoriesAcrossReadPaths(t *testing.T) {
	h := newNamespaceContractHarness(t)
	namespace := uniqueName("repository-read-contract")
	h.createNamespace(namespace, "Repository Read Contract")
	t.Cleanup(func() {
		h.cleanupNamespace(namespace)
	})

	existing := repositoryReadContractQueryByPath(t, h, namespace, namespaceContractSystemRepository)
	createdName := uniqueName("repository-read-created")
	createdID := repositoryReadContractCreate(t, h, namespace, createdName)
	t.Cleanup(func() {
		repositoryReadContractDelete(t, h, createdID)
	})
	created := repositoryReadContractQueryByPath(t, h, namespace, createdName)

	assertRepositoryReadIntegrationShape(t, existing, namespace, namespaceContractSystemRepository)
	assertRepositoryReadIntegrationShape(t, created, namespace, createdName)
	assert.Equal(t, createdID, created.ID)

	byID := repositoryReadContractQueryByID(t, h, created.ID)
	assert.Equal(t, created, byID)

	byNode := repositoryReadContractQueryByNode(t, h, created.ID)
	assert.Equal(t, created, byNode)

	listed := repositoryReadContractList(t, h, namespace)
	listedByName := make(map[string]*repositoryReadContractResource, len(listed))
	for _, repository := range listed {
		if repository != nil {
			listedByName[repository.Metadata.Name] = repository
		}
	}
	assert.Equal(t, existing, listedByName[existing.Metadata.Name])
	assert.Equal(t, created, listedByName[created.Metadata.Name])
}

func TestRepositoryReadContract_HasNoLegacyDuplicateFields(t *testing.T) {
	h := newNamespaceContractHarness(t)
	resp := h.gqlAnonymous(`
		query {
			__type(name: "Repository") {
				fields(includeDeprecated: true) {
					name
				}
			}
		}
	`, nil)
	require.Empty(t, resp.Errors, namespaceContractErrors(resp.Errors))

	var data struct {
		Type struct {
			Fields []struct {
				Name string `json:"name"`
			} `json:"fields"`
		} `json:"__type"`
	}
	require.NoError(t, json.Unmarshal(resp.Data, &data))

	names := make(map[string]bool, len(data.Type.Fields))
	for _, field := range data.Type.Fields {
		names[field.Name] = true
	}

	for _, name := range []string{
		"name",
		"namespace",
		"defaultBranch",
		"storageClass",
		"storagePath",
		"createdAt",
		"createdBy",
		"updatedAt",
		"updatedBy",
	} {
		assert.False(t, names[name], "Repository.%s should have been removed", name)
	}
	assert.True(t, names["id"])
}

func TestRepositoryReadContract_DirectNodeAndConnectionEnvelopeBodyParity(t *testing.T) {
	h := newNamespaceContractHarness(t)
	namespace := uniqueName("repository-envelope-parity")
	h.createNamespace(namespace, "Repository Envelope Parity")
	t.Cleanup(func() {
		h.cleanupNamespace(namespace)
	})

	name := uniqueName("repository-body")
	id := repositoryReadContractCreate(t, h, namespace, name)
	t.Cleanup(func() {
		repositoryReadContractDelete(t, h, id)
	})
	body := "# Repository body\n\nRaw **Markdown** is preserved.\n"
	h.setResourceBody("Repository", namespace, name, body)

	byPath := repositoryReadContractQueryByPath(t, h, namespace, name)
	byID := repositoryReadContractQueryByID(t, h, byPath.ID)
	byNode := repositoryReadContractQueryByNode(t, h, byPath.ID)
	listed := repositoryReadContractList(t, h, namespace)

	assert.Equal(t, byPath, byID)
	assert.Equal(t, byPath, byNode)
	var connected *repositoryReadContractResource
	for _, repository := range listed {
		if repository != nil && repository.Metadata != nil && repository.Metadata.Name == name {
			connected = repository
			break
		}
	}
	require.NotNil(t, connected)
	assert.Equal(t, byPath, connected)

	require.NotNil(t, byPath.Metadata)
	require.NotNil(t, byPath.Body)
	assert.Equal(t, body, *byPath.Body)
	assert.NotEmpty(t, byPath.Metadata.UID)
	assert.Equal(t, byPath.Metadata.UID, byPath.ID, "metadata.uid uses the Repository Relay encoding")
	assert.Equal(t, namespace, byPath.Metadata.Namespace)
	assert.NotNil(t, byPath.Metadata.Labels)
	assert.NotNil(t, byPath.Metadata.Annotations)
	assert.NotNil(t, byPath.Metadata.OwnerReferences)
	assert.NotNil(t, byPath.Metadata.Finalizers)
}

func repositoryReadContractSelection() string {
	return `{
		id
		apiVersion
		kind
		metadata {
			name
			namespace
			labels
			annotations
			uid
			resourceVersion
			generation
			creationTimestamp
			revision
			ownerReferences {
				apiVersion
				kind
				name
				uid
			}
			finalizers
		}
		spec {
			defaultBranch
			visibility
			pushPolicy {
				maxPackSizeBytes
				maxFileSizeBytes
				receivePackHooks {
					preReceive {
						enabled
					}
				}
				schemaValidation {
					phase
					timeoutSeconds
				}
				admissionControl {
					phase
					branchPattern
				}
			}
		}
		status {
			observedGeneration
			lastAppliedRevision
			conditions {
				type
				status
			}
			resolved {
				storagePath
				storageClass
			}
		}
		body
	}`
}

func repositoryReadContractQueryByPath(
	t *testing.T,
	h *namespaceContractHarness,
	namespace,
	name string,
) *repositoryReadContractResource {
	t.Helper()
	resp := h.gql(
		`query($namespace: String!, $name: String!) {
			repository(by: {namespacePath: {namespace: $namespace, name: $name}}) `+repositoryReadContractSelection()+`
		}`,
		map[string]any{"namespace": namespace, "name": name},
	)
	require.Empty(t, resp.Errors, namespaceContractErrors(resp.Errors))
	var data struct {
		Repository *repositoryReadContractResource `json:"repository"`
	}
	require.NoError(t, json.Unmarshal(resp.Data, &data))
	require.NotNil(t, data.Repository)
	return data.Repository
}

func repositoryReadContractQueryByID(
	t *testing.T,
	h *namespaceContractHarness,
	id string,
) *repositoryReadContractResource {
	t.Helper()
	resp := h.gql(
		`query($id: ID!) {
			repository(by: {id: $id}) `+repositoryReadContractSelection()+`
		}`,
		map[string]any{"id": id},
	)
	require.Empty(t, resp.Errors, namespaceContractErrors(resp.Errors))
	var data struct {
		Repository *repositoryReadContractResource `json:"repository"`
	}
	require.NoError(t, json.Unmarshal(resp.Data, &data))
	require.NotNil(t, data.Repository)
	return data.Repository
}

func repositoryReadContractQueryByNode(
	t *testing.T,
	h *namespaceContractHarness,
	id string,
) *repositoryReadContractResource {
	t.Helper()
	resp := h.gql(
		`query($id: ID!) {
			node(id: $id) {
				... on Repository `+repositoryReadContractSelection()+`
			}
		}`,
		map[string]any{"id": id},
	)
	require.Empty(t, resp.Errors, namespaceContractErrors(resp.Errors))
	var data struct {
		Node *repositoryReadContractResource `json:"node"`
	}
	require.NoError(t, json.Unmarshal(resp.Data, &data))
	require.NotNil(t, data.Node)
	return data.Node
}

func repositoryReadContractList(
	t *testing.T,
	h *namespaceContractHarness,
	namespace string,
) []*repositoryReadContractResource {
	t.Helper()
	resp := h.gql(
		`query($namespace: String!) {
			repositories(namespace: $namespace, first: 20) {
				edges {
					node `+repositoryReadContractSelection()+`
				}
			}
		}`,
		map[string]any{"namespace": namespace},
	)
	require.Empty(t, resp.Errors, namespaceContractErrors(resp.Errors))
	var data struct {
		Repositories struct {
			Edges []struct {
				Node *repositoryReadContractResource `json:"node"`
			} `json:"edges"`
		} `json:"repositories"`
	}
	require.NoError(t, json.Unmarshal(resp.Data, &data))
	repositories := make([]*repositoryReadContractResource, 0, len(data.Repositories.Edges))
	for _, edge := range data.Repositories.Edges {
		repositories = append(repositories, edge.Node)
	}
	return repositories
}

func repositoryReadContractCreate(
	t *testing.T,
	h *namespaceContractHarness,
	namespace,
	name string,
) string {
	t.Helper()
	resp := h.gql(`
		mutation($namespace: String!, $name: String!) {
			createRepository(input: {
				apiVersion: "gitstore.dev/v1beta1"
				kind: "Repository"
				metadata: {namespace: $namespace, name: $name}
				spec: {defaultBranch: "main", visibility: PRIVATE, storageClass: "standard"}
			}) {
				repository {
					id
				}
			}
		}
	`, map[string]any{"namespace": namespace, "name": name})
	require.Empty(t, resp.Errors, namespaceContractErrors(resp.Errors))
	var data struct {
		CreateRepository struct {
			Repository struct {
				ID string `json:"id"`
			} `json:"repository"`
		} `json:"createRepository"`
	}
	require.NoError(t, json.Unmarshal(resp.Data, &data))
	require.NotEmpty(t, data.CreateRepository.Repository.ID)
	return data.CreateRepository.Repository.ID
}

func repositoryReadContractDelete(t *testing.T, h *namespaceContractHarness, id string) {
	t.Helper()
	if id == "" {
		return
	}
	if errors := h.deleteRepositoryAndComplete(id); len(errors) > 0 {
		t.Logf("cleanup deleteRepository(%s) errors: %s", id, namespaceContractErrors(errors))
	}
}

func assertRepositoryReadIntegrationShape(
	t *testing.T,
	got *repositoryReadContractResource,
	namespace,
	name string,
) {
	t.Helper()
	require.NotNil(t, got)
	require.NotNil(t, got.Metadata)
	require.NotNil(t, got.Spec)
	require.NotNil(t, got.Spec.PushPolicy)
	require.NotNil(t, got.Status)
	require.NotNil(t, got.Status.Resolved)

	assert.Equal(t, "gitstore.dev/v1beta1", got.APIVersion)
	assert.Equal(t, "Repository", got.Kind)
	assert.Equal(t, name, got.Metadata.Name)
	assert.Equal(t, namespace, got.Metadata.Namespace)
	assert.NotEmpty(t, got.ID)
	assert.Equal(t, got.ID, got.Metadata.UID, "metadata.uid uses the Repository Relay encoding")
	assert.NotEmpty(t, got.Metadata.ResourceVersion)
	assert.GreaterOrEqual(t, got.Metadata.Generation, 1)
	assert.NotEmpty(t, got.Metadata.CreationTimestamp)
	assert.NotNil(t, got.Metadata.Labels)
	assert.NotNil(t, got.Metadata.Annotations)
	assert.NotNil(t, got.Metadata.OwnerReferences)
	assert.NotNil(t, got.Metadata.Finalizers)

	assert.Equal(t, "PRIVATE", got.Spec.Visibility)
	assert.Equal(t, int64(0), got.Spec.PushPolicy.MaxPackSizeBytes)
	assert.Equal(t, int64(0), got.Spec.PushPolicy.MaxFileSizeBytes)
	assert.Equal(t, json.RawMessage("null"), got.Spec.PushPolicy.ReceivePackHooks)
	assert.Equal(t, json.RawMessage("null"), got.Spec.PushPolicy.SchemaValidation)
	assert.Equal(t, json.RawMessage("null"), got.Spec.PushPolicy.AdmissionControl)

	assert.NotNil(t, got.Status.Conditions)
	assert.NotEmpty(t, got.Status.Resolved.StoragePath)
}
