// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package integration

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type repositoryVersionResource struct {
	ID       string `json:"id"`
	Metadata struct {
		Name            string `json:"name"`
		Namespace       string `json:"namespace"`
		UID             string `json:"uid"`
		ResourceVersion string `json:"resourceVersion"`
		Generation      int    `json:"generation"`
	} `json:"metadata"`
	Status struct {
		ObservedGeneration int                                `json:"observedGeneration"`
		Conditions         []namespaceContractStatusCondition `json:"conditions"`
	} `json:"status"`
}

func TestRepositoryVersionContract_CreateEstablishesIdentity(t *testing.T) {
	h := newNamespaceContractHarness(t)
	suffix := time.Now().UnixNano()
	namespace := fmt.Sprintf("repo-version-%d", suffix)
	h.createNamespace(namespace, "Repository Version")
	t.Cleanup(func() {
		h.cleanupNamespace(namespace)
	})

	created := repositoryVersionCreate(t, h, namespace, "catalog")
	t.Cleanup(func() {
		repositoryVersionDelete(t, h, created.ID)
	})

	assert.NotEmpty(t, created.Metadata.UID)
	assert.Equal(t, created.ID, created.Metadata.UID)
	assert.Equal(t, "1", created.Metadata.ResourceVersion)
	assert.Equal(t, 1, created.Metadata.Generation)
	assert.Equal(t, namespace, created.Metadata.Namespace)
	assert.Equal(t, 1, created.Status.ObservedGeneration)
	assert.NotNil(t, created.Status.Conditions)
	require.Len(t, created.Status.Conditions, 1)
	assert.Equal(t, "AdmissionAccepted", created.Status.Conditions[0].Type)
	assert.Equal(t, "TRUE", created.Status.Conditions[0].Status)

	unchanged := repositoryVersionQueryByID(t, h, created.ID)
	assert.Equal(t, created, unchanged)
}

func repositoryVersionSelection() string {
	return `{
		id
		metadata { name namespace uid resourceVersion generation }
		status { observedGeneration conditions { type status } }
	}`
}

func repositoryVersionCreate(t *testing.T, h *namespaceContractHarness, namespace, name string) repositoryVersionResource {
	t.Helper()
	resp := h.gql(
		`mutation($namespace: String!, $name: String!) {
			createRepository(input: {
				apiVersion: "gitstore.dev/v1beta1"
				kind: "Repository"
				metadata: {namespace: $namespace, name: $name}
				spec: {defaultBranch: "main", visibility: PRIVATE, storageClass: "standard"}
			}) {
				repository `+repositoryVersionSelection()+`
			}
		}`,
		map[string]any{"namespace": namespace, "name": name},
	)
	require.Empty(t, resp.Errors, namespaceContractErrors(resp.Errors))
	var data struct {
		CreateRepository struct {
			Repository repositoryVersionResource `json:"repository"`
		} `json:"createRepository"`
	}
	require.NoError(t, json.Unmarshal(resp.Data, &data))
	return data.CreateRepository.Repository
}

func repositoryVersionQueryByID(t *testing.T, h *namespaceContractHarness, repositoryID string) repositoryVersionResource {
	t.Helper()
	resp := h.gql(
		`query($repositoryID: ID!) {
			repository(by: {id: $repositoryID}) `+repositoryVersionSelection()+`
		}`,
		map[string]any{"repositoryID": repositoryID},
	)
	require.Empty(t, resp.Errors, namespaceContractErrors(resp.Errors))
	var data struct {
		Repository repositoryVersionResource `json:"repository"`
	}
	require.NoError(t, json.Unmarshal(resp.Data, &data))
	return data.Repository
}

func repositoryVersionDelete(t *testing.T, h *namespaceContractHarness, repositoryID string) {
	t.Helper()
	if errors := h.deleteRepositoryAndComplete(repositoryID); len(errors) > 0 {
		t.Logf("cleanup deleteRepository(%s): %s", repositoryID, namespaceContractErrors(errors))
	}
}
