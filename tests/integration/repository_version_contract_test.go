// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package integration

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
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
	h.createNamespace(from, "Repository Version From")
	h.createNamespace(to, "Repository Version To")
	t.Cleanup(func() {
		h.cleanupNamespace(from)
		h.cleanupNamespace(to)
	})

	targetNamespaceID := repositoryVersionLookupNamespaceID(t, h, to)
	created := repositoryVersionCreate(t, h, from, "catalog")
	t.Cleanup(func() {
		repositoryVersionDelete(t, h, created.ID)
	})

	assert.NotEmpty(t, created.Metadata.UID)
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
	repositoryVersionAssertOnlyActivePath(t, h, created.ID,
		repositoryVersionPath{namespace: from, name: "catalog-renamed"},
		repositoryVersionPath{namespace: from, name: "catalog"},
		repositoryVersionPath{namespace: from, name: "catalog-renamed"},
	)

	repeatedRename := repositoryVersionRename(t, h, created.ID, "catalog-renamed")
	assert.Equal(t, renamed, repeatedRename)

	retriedName := "catalog-retried"
	repositoryVersionLoseMutationResponse(t, h,
		`mutation($repositoryID: ID!, $newName: String!) {
			renameRepository(input: {repositoryId: $repositoryID, newName: $newName}) {
				repository { id }
			}
		}`,
		map[string]any{"repositoryID": created.ID, "newName": retriedName},
	)
	committedRename := repositoryVersionQueryByID(t, h, created.ID)
	assert.Equal(t, "3", committedRename.Metadata.ResourceVersion)
	assert.Equal(t, 3, committedRename.Metadata.Generation)
	assert.Equal(t, retriedName, committedRename.Metadata.Name)
	assert.Equal(t, from, committedRename.Metadata.Namespace)

	retriedRename := repositoryVersionRename(t, h, created.ID, retriedName)
	assert.Equal(t, committedRename, retriedRename)
	repositoryVersionAssertOnlyActivePath(t, h, created.ID,
		repositoryVersionPath{namespace: from, name: retriedName},
		repositoryVersionPath{namespace: from, name: "catalog"},
		repositoryVersionPath{namespace: from, name: "catalog-renamed"},
		repositoryVersionPath{namespace: from, name: retriedName},
	)

	repositoryVersionLoseMutationResponse(t, h,
		`mutation($repositoryID: ID!, $targetNamespaceID: ID!) {
			transferRepository(input: {
				repositoryId: $repositoryID
				targetNamespaceId: $targetNamespaceID
			}) {
				repository { id }
			}
		}`,
		map[string]any{"repositoryID": created.ID, "targetNamespaceID": targetNamespaceID},
	)
	committedTransfer := repositoryVersionQueryByID(t, h, created.ID)
	assert.Equal(t, "4", committedTransfer.Metadata.ResourceVersion)
	assert.Equal(t, 3, committedTransfer.Metadata.Generation)
	assert.Equal(t, retriedName, committedTransfer.Metadata.Name)
	assert.Equal(t, to, committedTransfer.Metadata.Namespace)

	transferred := repositoryVersionTransfer(t, h, created.ID, targetNamespaceID)
	assert.Equal(t, committedTransfer, transferred)
	assert.Equal(t, created.ID, transferred.ID)
	assert.Equal(t, created.Metadata.UID, transferred.Metadata.UID)
	assert.Equal(t, "4", transferred.Metadata.ResourceVersion)
	assert.Equal(t, 3, transferred.Metadata.Generation)
	assert.Equal(t, to, transferred.Metadata.Namespace)
	assert.NotNil(t, transferred.Status.Conditions)

	repeatedTransfer := repositoryVersionTransfer(t, h, created.ID, targetNamespaceID)
	assert.Equal(t, transferred, repeatedTransfer)
	repositoryVersionAssertOnlyActivePath(t, h, created.ID,
		repositoryVersionPath{namespace: to, name: retriedName},
		repositoryVersionPath{namespace: from, name: "catalog"},
		repositoryVersionPath{namespace: from, name: "catalog-renamed"},
		repositoryVersionPath{namespace: from, name: retriedName},
		repositoryVersionPath{namespace: to, name: retriedName},
	)
}

type repositoryVersionPath struct {
	namespace string
	name      string
}

var errRepositoryVersionResponseLost = errors.New("repository mutation response lost")

type repositoryVersionResponseLossTransport struct {
	base http.RoundTripper
}

func (t repositoryVersionResponseLossTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	resp, err := t.base.RoundTrip(req)
	if err != nil {
		return nil, err
	}
	if _, err := io.Copy(io.Discard, resp.Body); err != nil {
		_ = resp.Body.Close()
		return nil, fmt.Errorf("read mutation response before simulated loss: %w", err)
	}
	if err := resp.Body.Close(); err != nil {
		return nil, fmt.Errorf("close mutation response before simulated loss: %w", err)
	}
	return nil, errRepositoryVersionResponseLost
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

func repositoryVersionQueryByPath(
	t *testing.T,
	h *namespaceContractHarness,
	path repositoryVersionPath,
) (*repositoryVersionResource, []json.RawMessage) {
	t.Helper()
	resp := h.gql(
		`query($namespace: String!, $name: String!) {
			repository(by: {namespacePath: {namespace: $namespace, name: $name}}) `+repositoryVersionSelection()+`
		}`,
		map[string]any{"namespace": path.namespace, "name": path.name},
	)
	var data struct {
		Repository *repositoryVersionResource `json:"repository"`
	}
	require.NoError(t, json.Unmarshal(resp.Data, &data))
	return data.Repository, resp.Errors
}

func repositoryVersionAssertOnlyActivePath(
	t *testing.T,
	h *namespaceContractHarness,
	repositoryID string,
	active repositoryVersionPath,
	paths ...repositoryVersionPath,
) {
	t.Helper()
	resolved := 0
	seen := make(map[repositoryVersionPath]struct{}, len(paths))
	for _, path := range paths {
		if _, ok := seen[path]; ok {
			continue
		}
		seen[path] = struct{}{}

		repository, errs := repositoryVersionQueryByPath(t, h, path)
		if path == active {
			require.Empty(t, errs, namespaceContractErrors(errs))
			require.NotNil(t, repository)
			assert.Equal(t, repositoryID, repository.ID)
			assert.Equal(t, path.namespace, repository.Metadata.Namespace)
			assert.Equal(t, path.name, repository.Metadata.Name)
			resolved++
			continue
		}
		assert.NotEmpty(t, errs, "old repository path %s/%s unexpectedly resolved", path.namespace, path.name)
		assert.Nil(t, repository, "old repository path %s/%s returned a repository", path.namespace, path.name)
	}
	assert.Equal(t, 1, resolved, "expected exactly one active repository path")
}

func repositoryVersionLoseMutationResponse(
	t *testing.T,
	h *namespaceContractHarness,
	query string,
	vars map[string]any,
) {
	t.Helper()
	body, err := json.Marshal(gqlRequest{Query: query, Variables: vars})
	require.NoError(t, err)
	req, err := http.NewRequest(http.MethodPost, h.apiURL+"/graphql", bytes.NewReader(body))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+h.token)

	client := &http.Client{
		Transport: repositoryVersionResponseLossTransport{base: http.DefaultTransport},
	}
	resp, err := client.Do(req)
	require.Nil(t, resp)
	require.ErrorIs(t, err, errRepositoryVersionResponseLost)
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
