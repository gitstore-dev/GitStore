// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/gitstore-dev/gitstore/api/internal/app"
	"github.com/gitstore-dev/gitstore/api/internal/datastore/memdb"
	"github.com/gitstore-dev/gitstore/api/internal/gitclient"
	"github.com/gitstore-dev/gitstore/api/internal/middleware"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

type mockGitWriter struct {
	mu             sync.Mutex
	commitCalls    []gitclient.CommitFileParams
	deleteCalls    []gitclient.DeleteFileParams
	createTagCalls []gitclient.CreateTagParams
}

func (m *mockGitWriter) CommitFile(_ context.Context, p gitclient.CommitFileParams) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.commitCalls = append(m.commitCalls, p)
	return "deadbeef", nil
}

func (m *mockGitWriter) DeleteFile(_ context.Context, p gitclient.DeleteFileParams) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.deleteCalls = append(m.deleteCalls, p)
	return "cafe1234", nil
}

func (m *mockGitWriter) CreateTag(_ context.Context, p gitclient.CreateTagParams) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.createTagCalls = append(m.createTagCalls, p)
	return "tag123", nil
}

func (m *mockGitWriter) CreateRepository(_ context.Context, repositoryID, _ string) (string, error) {
	return "/data/00/00/" + repositoryID + ".git", nil
}

func (m *mockGitWriter) DeleteRepository(_ context.Context, _ string) error {
	return nil
}

func TestGraphQLHandlerAcceptsBearerTokenForNamespaceMutation(t *testing.T) {
	store, err := memdb.New()
	require.NoError(t, err)

	hash, err := middleware.HashPassword("admin123")
	require.NoError(t, err)
	authMiddleware, err := middleware.NewAuthMiddleware(middleware.AuthDeps{
		AdminUsername:     "admin",
		AdminPasswordHash: hash,
		JWTSecret:         "dev-secret",
		JWTDuration:       "2h",
		JWTIssuer:         "gitstore",
	})
	require.NoError(t, err)

	handler, err := app.NewGraphQLHandler(store, &mockGitWriter{}, zap.NewNop(), authMiddleware, nil, nil)
	require.NoError(t, err)

	loginReq := httptest.NewRequest(http.MethodPost, "/graphql", strings.NewReader(`{
		"query": "mutation { login(input: { username: \"admin\", password: \"admin123\" }) { session { token user { username isAdmin } } } }"
	}`))
	loginReq.Header.Set("Content-Type", "application/json")
	loginW := httptest.NewRecorder()

	handler.ServeHTTP(loginW, loginReq)

	require.Equal(t, http.StatusOK, loginW.Code)
	var loginResponse struct {
		Data struct {
			Login struct {
				Session struct {
					Token string `json:"token"`
					User  struct {
						Username string `json:"username"`
						IsAdmin  bool   `json:"isAdmin"`
					} `json:"user"`
				} `json:"session"`
			} `json:"login"`
		} `json:"data"`
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	require.NoError(t, json.Unmarshal(loginW.Body.Bytes(), &loginResponse))
	require.Empty(t, loginResponse.Errors)
	require.NotEmpty(t, loginResponse.Data.Login.Session.Token)
	assert.Equal(t, "admin", loginResponse.Data.Login.Session.User.Username)
	assert.True(t, loginResponse.Data.Login.Session.User.IsAdmin)

	req := httptest.NewRequest(http.MethodPost, "/graphql", strings.NewReader(`{
		"query": "mutation { createNamespace(input: { clientMutationId: \"create-alice\", identifier: \"alice\", tier: USER }) { clientMutationId namespace { identifier createdBy } } }"
	}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+loginResponse.Data.Login.Session.Token)
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	var response struct {
		Data struct {
			CreateNamespace struct {
				ClientMutationID string `json:"clientMutationId"`
				Namespace        struct {
					Identifier string `json:"identifier"`
					CreatedBy  string `json:"createdBy"`
				} `json:"namespace"`
			} `json:"createNamespace"`
		} `json:"data"`
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &response))
	require.Empty(t, response.Errors)
	assert.Equal(t, "create-alice", response.Data.CreateNamespace.ClientMutationID)
	assert.Equal(t, "alice", response.Data.CreateNamespace.Namespace.Identifier)
	assert.Equal(t, "admin", response.Data.CreateNamespace.Namespace.CreatedBy)

	listReq := httptest.NewRequest(http.MethodPost, "/graphql", strings.NewReader(`{
		"query": "query { namespaces(first: 10) { edges { cursor node { identifier } } pageInfo { hasNextPage endCursor } totalCount } }"
	}`))
	listReq.Header.Set("Content-Type", "application/json")
	listW := httptest.NewRecorder()

	handler.ServeHTTP(listW, listReq)

	require.Equal(t, http.StatusOK, listW.Code)
	var listResponse struct {
		Data struct {
			Namespaces struct {
				Edges []struct {
					Cursor string `json:"cursor"`
					Node   struct {
						Identifier string `json:"identifier"`
					} `json:"node"`
				} `json:"edges"`
				TotalCount int `json:"totalCount"`
			} `json:"namespaces"`
		} `json:"data"`
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	require.NoError(t, json.Unmarshal(listW.Body.Bytes(), &listResponse))
	require.Empty(t, listResponse.Errors)
	require.Len(t, listResponse.Data.Namespaces.Edges, 1)
	assert.NotEmpty(t, listResponse.Data.Namespaces.Edges[0].Cursor)
	assert.Equal(t, "alice", listResponse.Data.Namespaces.Edges[0].Node.Identifier)
	assert.Equal(t, 1, listResponse.Data.Namespaces.TotalCount)
}

func TestGraphQLHandlerRejectsNamespaceMutationWithoutBearerToken(t *testing.T) {
	store, err := memdb.New()
	require.NoError(t, err)

	hash, err := middleware.HashPassword("admin123")
	require.NoError(t, err)
	authMiddleware, err := middleware.NewAuthMiddleware(middleware.AuthDeps{
		AdminUsername:     "admin",
		AdminPasswordHash: hash,
		JWTSecret:         "dev-secret",
		JWTDuration:       "2h",
		JWTIssuer:         "gitstore",
	})
	require.NoError(t, err)

	handler, err := app.NewGraphQLHandler(store, &mockGitWriter{}, zap.NewNop(), authMiddleware, nil, nil)
	require.NoError(t, err)
	req := httptest.NewRequest(http.MethodPost, "/graphql", strings.NewReader(`{
		"query": "mutation { createNamespace(input: { identifier: \"alice\", tier: USER }) { namespace { identifier } } }"
	}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	var response struct {
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &response))
	require.NotEmpty(t, response.Errors)
	assert.Contains(t, response.Errors[0].Message, "authentication required")
}
