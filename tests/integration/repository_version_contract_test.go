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

func TestRepositoryVersionContract_CreateRenameTransfer(t *testing.T) {
	h := newNamespaceContractHarness(t)
	suffix := time.Now().UnixNano()
	from := fmt.Sprintf("repo-version-from-%d", suffix)
	to := fmt.Sprintf("repo-version-to-%d", suffix)
	h.createNamespaceLegacy(from, "Repository Version From")
	h.createNamespaceLegacy(to, "Repository Version To")
	t.Cleanup(func() {
		h.cleanupNamespace(from)
		h.cleanupNamespace(to)
	})

	targetNamespaceID := repositoryVersionLookupNamespaceID(t, h, to)
	created := repositoryVersionCreate(t, h, from, "catalog")
	t.Cleanup(func() {
		repositoryVersionDelete(t, h, created.ID)
	})

	assert.Equal(t, created.ID, created.Metadata.UID)
	assert.Equal(t, "1", created.Metadata.ResourceVersion)
	assert.Equal(t, 1, created.Metadata.Generation)
	assert.Equal(t, from, created.Metadata.Namespace)
	assert.Zero(t, created.Status.ObservedGeneration)
	assert.NotNil(t, created.Status.Conditions)
	assert.Empty(t, created.Status.Conditions)

	renamed := repositoryVersionRename(t, h, created.ID, "catalog-renamed")
	assert.Equal(t, created.ID, renamed.ID)
	assert.Equal(t, created.Metadata.UID, renamed.Metadata.UID)
	assert.Equal(t, "2", renamed.Metadata.ResourceVersion)
	assert.Equal(t, 2, renamed.Metadata.Generation)
	assert.Equal(t, "catalog-renamed", renamed.Metadata.Name)
	assert.NotNil(t, renamed.Status.Conditions)

	transferred := repositoryVersionTransfer(t, h, created.ID, targetNamespaceID)
	assert.Equal(t, created.ID, transferred.ID)
	assert.Equal(t, created.Metadata.UID, transferred.Metadata.UID)
	assert.Equal(t, "3", transferred.Metadata.ResourceVersion)
	assert.Equal(t, 2, transferred.Metadata.Generation)
	assert.Equal(t, to, transferred.Metadata.Namespace)
	assert.NotNil(t, transferred.Status.Conditions)
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
			createRepository(input: {namespace: $namespace, name: $name}) {
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

func repositoryVersionRename(t *testing.T, h *namespaceContractHarness, repositoryID, newName string) repositoryVersionResource {
	t.Helper()
	resp := h.gql(
		`mutation($repositoryID: ID!, $newName: String!) {
			renameRepository(input: {repositoryId: $repositoryID, newName: $newName}) {
				repository `+repositoryVersionSelection()+`
			}
		}`,
		map[string]any{"repositoryID": repositoryID, "newName": newName},
	)
	require.Empty(t, resp.Errors, namespaceContractErrors(resp.Errors))
	var data struct {
		RenameRepository struct {
			Repository repositoryVersionResource `json:"repository"`
		} `json:"renameRepository"`
	}
	require.NoError(t, json.Unmarshal(resp.Data, &data))
	return data.RenameRepository.Repository
}

func repositoryVersionTransfer(t *testing.T, h *namespaceContractHarness, repositoryID, targetNamespaceID string) repositoryVersionResource {
	t.Helper()
	resp := h.gql(
		`mutation($repositoryID: ID!, $targetNamespaceID: ID!) {
			transferRepository(input: {repositoryId: $repositoryID, targetNamespaceId: $targetNamespaceID}) {
				repository `+repositoryVersionSelection()+`
			}
		}`,
		map[string]any{"repositoryID": repositoryID, "targetNamespaceID": targetNamespaceID},
	)
	require.Empty(t, resp.Errors, namespaceContractErrors(resp.Errors))
	var data struct {
		TransferRepository struct {
			Repository repositoryVersionResource `json:"repository"`
		} `json:"transferRepository"`
	}
	require.NoError(t, json.Unmarshal(resp.Data, &data))
	return data.TransferRepository.Repository
}

func repositoryVersionLookupNamespaceID(t *testing.T, h *namespaceContractHarness, identifier string) string {
	t.Helper()
	resp := h.gql(
		`query($identifier: String!) {
			namespace(by: {identifier: $identifier}) { id }
		}`,
		map[string]any{"identifier": identifier},
	)
	require.Empty(t, resp.Errors, namespaceContractErrors(resp.Errors))
	var data struct {
		Namespace struct {
			ID string `json:"id"`
		} `json:"namespace"`
	}
	require.NoError(t, json.Unmarshal(resp.Data, &data))
	return data.Namespace.ID
}

func repositoryVersionDelete(t *testing.T, h *namespaceContractHarness, repositoryID string) {
	t.Helper()
	resp := h.gql(
		`mutation($repositoryID: ID!) {
			deleteRepository(input: {repositoryId: $repositoryID}) { deletedRepositoryId }
		}`,
		map[string]any{"repositoryID": repositoryID},
	)
	if len(resp.Errors) > 0 {
		t.Logf("cleanup deleteRepository(%s): %s", repositoryID, namespaceContractErrors(resp.Errors))
	}
}
