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

type repositoryAdmissionResource struct {
	ID       string `json:"id"`
	Metadata struct {
		Name      string `json:"name"`
		Namespace string `json:"namespace"`
		UID       string `json:"uid"`
	} `json:"metadata"`
}

// TestRepositoryLifecycle_PushAdmissionReadIdentity verifies the production
// post-receive path: a pushed Repository manifest is admitted, then every
// public read envelope exposes the same Relay-encoded identity.  In
// particular, metadata.uid must never regress to the datastore UUID.
func TestRepositoryLifecycle_PushAdmissionReadIdentity(t *testing.T) {
	namespace := getEnv("NAMESPACE", "gitstore-test")
	name := uniqueName("repository-push-admission")
	h := newPushHelper(t)
	h.commitRepository(name+".md", validRepositoryFixture(name, namespace))
	if out, err := h.push(); err != nil {
		t.Fatalf("push Repository manifest: %v\n%s", err, out)
	}

	byPath := waitForAdmittedRepository(t, namespace, name)
	require.NotEmpty(t, byPath.ID)
	assert.Equal(t, byPath.ID, byPath.Metadata.UID, "metadata.uid must use the Repository Relay encoding")
	assert.Equal(t, name, byPath.Metadata.Name)
	assert.Equal(t, namespace, byPath.Metadata.Namespace)

	byNode := repositoryAdmissionQueryNode(t, byPath.ID)
	assert.Equal(t, byPath, byNode, "node(id:) must return the admitted Repository envelope")

	listed := repositoryAdmissionQueryConnection(t, namespace)
	var fromConnection *repositoryAdmissionResource
	for _, repository := range listed {
		if repository.Metadata.Name == name {
			fromConnection = repository
			break
		}
	}
	require.NotNil(t, fromConnection, "admitted Repository must appear in its namespace connection")
	assert.Equal(t, byPath, fromConnection, "connection must preserve the Repository Relay identity")
}

func validRepositoryFixture(name, namespace string) string {
	return fmt.Sprintf(`---
apiVersion: gitstore.dev/v1beta1
kind: Repository
metadata:
  name: %s
  namespace: %s
spec:
  defaultBranch: main
  visibility: PRIVATE
  storageClass: standard
---
`, name, namespace)
}

func waitForAdmittedRepository(t *testing.T, namespace, name string) *repositoryAdmissionResource {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		response := gqlQuery(t, `query($namespace: String!, $name: String!) {
			repository(by: {namespacePath: {namespace: $namespace, name: $name}}) {
				id
				metadata { name namespace uid }
			}
		}`, map[string]any{"namespace": namespace, "name": name})
		require.Empty(t, response.Errors, "repository direct lookup GraphQL errors: %s", response.Errors)

		var data struct {
			Repository *repositoryAdmissionResource `json:"repository"`
		}
		require.NoError(t, json.Unmarshal(response.Data, &data))
		if data.Repository != nil || time.Now().After(deadline) {
			require.NotNil(t, data.Repository, "Repository manifest was pushed but was not admitted")
			return data.Repository
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func repositoryAdmissionQueryNode(t *testing.T, id string) *repositoryAdmissionResource {
	t.Helper()
	response := gqlQuery(t, `query($id: ID!) {
		node(id: $id) {
			... on Repository {
				id
				metadata { name namespace uid }
			}
		}
	}`, map[string]any{"id": id})
	require.Empty(t, response.Errors, "Repository node(id:) GraphQL errors: %s", response.Errors)
	var data struct {
		Node *repositoryAdmissionResource `json:"node"`
	}
	require.NoError(t, json.Unmarshal(response.Data, &data))
	require.NotNil(t, data.Node)
	return data.Node
}

func repositoryAdmissionQueryConnection(t *testing.T, namespace string) []*repositoryAdmissionResource {
	t.Helper()
	response := gqlQuery(t, `query($namespace: String!) {
		repositories(namespace: $namespace, first: 100) {
			edges { node { id metadata { name namespace uid } } }
		}
	}`, map[string]any{"namespace": namespace})
	require.Empty(t, response.Errors, "Repository connection GraphQL errors: %s", response.Errors)
	var data struct {
		Repositories struct {
			Edges []struct {
				Node *repositoryAdmissionResource `json:"node"`
			} `json:"edges"`
		} `json:"repositories"`
	}
	require.NoError(t, json.Unmarshal(response.Data, &data))
	repositories := make([]*repositoryAdmissionResource, 0, len(data.Repositories.Edges))
	for _, edge := range data.Repositories.Edges {
		if edge.Node != nil {
			repositories = append(repositories, edge.Node)
		}
	}
	return repositories
}
