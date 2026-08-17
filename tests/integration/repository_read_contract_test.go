// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package integration

import (
	"encoding/json"
	"strings"
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

	Name          string                           `json:"name"`
	Namespace     *repositoryReadContractNamespace `json:"namespace"`
	DefaultBranch string                           `json:"defaultBranch"`
	StorageClass  string                           `json:"storageClass"`
	StoragePath   string                           `json:"storagePath"`
	CreatedAt     string                           `json:"createdAt"`
	CreatedBy     string                           `json:"createdBy"`
	UpdatedAt     string                           `json:"updatedAt"`
	UpdatedBy     string                           `json:"updatedBy"`
}

type repositoryReadContractMetadata struct {
	Name              string `json:"name"`
	Namespace         string `json:"namespace"`
	UID               string `json:"uid"`
	ResourceVersion   string `json:"resourceVersion"`
	Generation        int    `json:"generation"`
	CreationTimestamp string `json:"creationTimestamp"`
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

type repositoryReadContractNamespace struct {
	Metadata struct {
		Name string `json:"name"`
	} `json:"metadata"`
}

func TestRepositoryReadContract_LegacyAndCreatedRepositoriesAcrossReadPaths(t *testing.T) {
	h := newNamespaceContractHarness(t)
	namespace := uniqueName("repository-read-contract")
	h.createNamespaceLegacy(namespace, "Repository Read Contract")
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

func TestRepositoryReadContract_DeprecatesOnlyLegacyDuplicateFields(t *testing.T) {
	h := newNamespaceContractHarness(t)
	resp := h.gqlAnonymous(`
		query {
			__type(name: "Repository") {
				fields(includeDeprecated: true) {
					name
					isDeprecated
					deprecationReason
				}
			}
		}
	`, nil)
	require.Empty(t, resp.Errors, namespaceContractErrors(resp.Errors))

	var data struct {
		Type struct {
			Fields []struct {
				Name              string  `json:"name"`
				IsDeprecated      bool    `json:"isDeprecated"`
				DeprecationReason *string `json:"deprecationReason"`
			} `json:"fields"`
		} `json:"__type"`
	}
	require.NoError(t, json.Unmarshal(resp.Data, &data))

	fields := make(map[string]struct {
		deprecated bool
		reason     *string
	}, len(data.Type.Fields))
	for _, field := range data.Type.Fields {
		fields[field.Name] = struct {
			deprecated bool
			reason     *string
		}{deprecated: field.IsDeprecated, reason: field.DeprecationReason}
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
		field, ok := fields[name]
		require.True(t, ok, "Repository.%s missing from introspection", name)
		assert.True(t, field.deprecated, "Repository.%s must be deprecated", name)
		require.NotNil(t, field.reason, "Repository.%s must provide migration guidance", name)
		assert.NotEmpty(t, strings.TrimSpace(*field.reason))
	}
	require.Contains(t, fields, "id")
	assert.False(t, fields["id"].deprecated, "Relay id must remain non-deprecated")
	assert.Nil(t, fields["id"].reason)
}

func repositoryReadContractSelection() string {
	return `{
		id
		apiVersion
		kind
		metadata {
			name
			namespace
			uid
			resourceVersion
			generation
			creationTimestamp
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
		name
		namespace {
			metadata {
				name
			}
		}
		defaultBranch
		storageClass
		storagePath
		createdAt
		createdBy
		updatedAt
		updatedBy
	}`
}

func repositoryReadContractQueryByPath(
	t *testing.T,
	h *namespaceContractHarness,
	namespace,
	name string,
) *repositoryReadContractResource {
	t.Helper()
	resp := h.gqlAnonymous(
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
	resp := h.gqlAnonymous(
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
	resp := h.gqlAnonymous(
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
	resp := h.gqlAnonymous(
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
			createRepository(input: {namespace: $namespace, name: $name, defaultBranch: "main"}) {
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
	resp := h.gql(`
		mutation($id: ID!) {
			deleteRepository(input: {repositoryId: $id}) {
				deletedRepositoryId
			}
		}
	`, map[string]any{"id": id})
	if len(resp.Errors) > 0 {
		t.Logf("cleanup deleteRepository(%s) errors: %s", id, namespaceContractErrors(resp.Errors))
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
	assert.Equal(t, got.ID, got.Metadata.UID)
	assert.NotEmpty(t, got.Metadata.ResourceVersion)
	assert.GreaterOrEqual(t, got.Metadata.Generation, 1)
	assert.NotEmpty(t, got.Metadata.CreationTimestamp)

	assert.Equal(t, got.DefaultBranch, got.Spec.DefaultBranch)
	assert.Equal(t, "PRIVATE", got.Spec.Visibility)
	assert.Equal(t, int64(0), got.Spec.PushPolicy.MaxPackSizeBytes)
	assert.Equal(t, int64(0), got.Spec.PushPolicy.MaxFileSizeBytes)
	assert.Equal(t, json.RawMessage("null"), got.Spec.PushPolicy.ReceivePackHooks)
	assert.Equal(t, json.RawMessage("null"), got.Spec.PushPolicy.SchemaValidation)
	assert.Equal(t, json.RawMessage("null"), got.Spec.PushPolicy.AdmissionControl)

	assert.NotNil(t, got.Status.Conditions)
	assert.NotEmpty(t, got.Status.Resolved.StoragePath)
	assert.Equal(t, got.StoragePath, got.Status.Resolved.StoragePath)
	assert.Equal(t, got.StorageClass, got.Status.Resolved.StorageClass)

	assert.Equal(t, name, got.Name)
	require.NotNil(t, got.Namespace)
	assert.Equal(t, namespace, got.Namespace.Metadata.Name)
	assert.Equal(t, got.Metadata.CreationTimestamp, got.CreatedAt)
	assert.NotEmpty(t, got.CreatedBy)
	assert.NotEmpty(t, got.UpdatedAt)
	assert.NotEmpty(t, got.UpdatedBy)
}
