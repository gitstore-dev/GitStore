// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/gitstore-dev/gitstore/api/internal/app"
	authpkg "github.com/gitstore-dev/gitstore/api/internal/auth"
	"github.com/gitstore-dev/gitstore/api/internal/auth/provider/allowall"
	"github.com/gitstore-dev/gitstore/api/internal/auth/provider/anonymous"
	"github.com/gitstore-dev/gitstore/api/internal/auth/provider/staticusers"
	"github.com/gitstore-dev/gitstore/api/internal/config"
	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	"github.com/gitstore-dev/gitstore/api/internal/datastore/memdb"
	"github.com/gitstore-dev/gitstore/api/internal/gitclient"
	apiruntime "github.com/gitstore-dev/gitstore/api/internal/runtime"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"golang.org/x/crypto/bcrypt"
)

type mockGitWriter struct {
	mu             sync.Mutex
	commitCalls    []gitclient.CommitFileParams
	deleteCalls    []gitclient.DeleteFileParams
	createTagCalls []gitclient.CreateTagParams
}

func TestMain(m *testing.M) {
	gin.SetMode(gin.TestMode)
	os.Exit(m.Run())
}

func TestParseConfigFile(t *testing.T) {
	path, err := parseConfigFile([]string{"--config-file", "/config/shared.toml"})
	require.NoError(t, err)
	assert.Equal(t, "/config/shared.toml", path)
}

func (m *mockGitWriter) CommitFile(_ context.Context, p gitclient.CommitFileParams) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.commitCalls = append(m.commitCalls, p)
	return "deadbeef", nil
}

func (m *mockGitWriter) CommitFileForRepo(_ context.Context, _ string, p gitclient.CommitFileParams) (string, error) {
	return m.CommitFile(context.Background(), p)
}

func (m *mockGitWriter) ResolveRefForRepo(_ context.Context, _ string, _ string) (string, error) {
	return "deadbeef", nil
}

func (m *mockGitWriter) ReadFileForRepo(_ context.Context, _, _, _ string) ([]byte, error) {
	return nil, nil
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

func seedNamespaceAuthoringRepository(t *testing.T, store datastore.Datastore) {
	t.Helper()
	now := time.Now().UTC()
	namespace := &datastore.Namespace{
		UID:               "00000000-0000-7000-8000-000000000001",
		Name:              "gitstore-system",
		Title:             "GitStore System",
		Tier:              datastore.NamespaceTierOrganization,
		CreationTimestamp: now,
		CreationActor:     "system:bootstrap",
		UpdateTimestamp:   now,
		UpdateActor:       "system:bootstrap",
	}
	datastore.NormalizeNamespaceContract(namespace)
	require.NoError(t, store.CreateNamespace(context.Background(), namespace))
	repository := &datastore.Repository{
		UID:               "00000000-0000-7000-8000-000000000002",
		Namespace:         namespace.Name,
		Name:              "gitstore-system",
		DefaultBranch:     "main",
		StorageClass:      "default",
		CreationTimestamp: now,
		CreationActor:     "system:bootstrap",
		UpdateTimestamp:   now,
		UpdateActor:       "system:bootstrap",
	}
	datastore.NormalizeRepositoryContract(repository)
	require.NoError(t, store.CreateRepository(context.Background(), repository))
	require.NoError(t, store.CreateNamespaceMapping(context.Background(), &datastore.NamespaceMapping{
		Namespace:    namespace.Name,
		Name:         repository.Name,
		RepositoryID: repository.UID,
	}))
}

func newTestGraphQLRegistry(t *testing.T) *authpkg.ProviderRegistry {
	t.Helper()
	hash, err := bcrypt.GenerateFromPassword([]byte("admin123"), bcrypt.MinCost)
	require.NoError(t, err)

	usersFile := filepath.Join(t.TempDir(), "users.yaml")
	require.NoError(t, os.WriteFile(usersFile, []byte("version: v1\nusers:\n  - username: admin\n    password_hash: \""+string(hash)+"\"\n"), 0600))
	cfg := config.AuthConfig{
		StaticUsers: config.StaticUsersConfig{UsersFile: usersFile},
		JWT: config.JWTConfig{
			Secret:   "dev-secret",
			Issuer:   "gitstore",
			Duration: "2h",
		},
	}

	staticAdmin, err := staticusers.New(cfg, zap.NewNop())
	require.NoError(t, err)
	t.Cleanup(staticAdmin.Shutdown)

	return authpkg.NewProviderRegistry(
		authpkg.NewChainedAuthN(staticAdmin, anonymous.New()),
		allowall.New(zap.NewNop()),
		nil,
	)
}

func TestGraphQLHandlerRequiresAuthZProvider(t *testing.T) {
	registry := newTestGraphQLRegistry(t)
	registry.Swap(registry.AuthN(), nil, nil)

	handler, err := app.NewGraphQLHandler(app.GraphQLHandlerDeps{Logger: zap.NewNop(), Registry: registry})
	require.Error(t, err)
	assert.Nil(t, handler)
	assert.Contains(t, err.Error(), "authn and authz provider registry is required")
}

func TestGraphQLHandlerAcceptsBearerTokenForNamespaceMutation(t *testing.T) {
	store, err := memdb.New()
	require.NoError(t, err)
	seedNamespaceAuthoringRepository(t, store)

	handler, err := app.NewGraphQLHandler(app.GraphQLHandlerDeps{Store: store, GitWriter: &mockGitWriter{}, Logger: zap.NewNop(), Registry: newTestGraphQLRegistry(t), IDs: apiruntime.NewSequenceIDGenerator()})
	require.NoError(t, err)

	loginReq := httptest.NewRequest(http.MethodPost, "/graphql", strings.NewReader(`{
		"query": "mutation { login(input: { username: \"admin\", password: \"admin123\" }) { token { accessToken tokenType } } }"
	}`))
	loginReq.Header.Set("Content-Type", "application/json")
	loginW := httptest.NewRecorder()

	handler.ServeHTTP(loginW, loginReq)

	require.Equal(t, http.StatusOK, loginW.Code)
	var loginResponse struct {
		Data struct {
			Login struct {
				Token struct {
					AccessToken string `json:"accessToken"`
					TokenType   string `json:"tokenType"`
				} `json:"token"`
			} `json:"login"`
		} `json:"data"`
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	require.NoError(t, json.Unmarshal(loginW.Body.Bytes(), &loginResponse))
	require.Empty(t, loginResponse.Errors)
	require.NotEmpty(t, loginResponse.Data.Login.Token.AccessToken)
	assert.Equal(t, "Bearer", loginResponse.Data.Login.Token.TokenType)

	req := httptest.NewRequest(http.MethodPost, "/graphql", strings.NewReader(`{"query":"mutation { createNamespace(input: { apiVersion: \"gitstore.dev/v1beta1\", kind: \"Namespace\", metadata: { name: \"alice\" }, spec: { tier: USER } }) { namespace { identifier createdBy } } }"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+loginResponse.Data.Login.Token.AccessToken)
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	var response struct {
		Data struct {
			CreateNamespace struct {
				Namespace struct {
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
	require.Len(t, listResponse.Data.Namespaces.Edges, 2)
	assert.NotEmpty(t, listResponse.Data.Namespaces.Edges[0].Cursor)
	assert.Equal(t, "alice", listResponse.Data.Namespaces.Edges[0].Node.Identifier)
	assert.Equal(t, 2, listResponse.Data.Namespaces.TotalCount)
}

func TestGraphQLHandlerRejectsLoginWithInvalidCredentials(t *testing.T) {
	store, err := memdb.New()
	require.NoError(t, err)

	handler, err := app.NewGraphQLHandler(app.GraphQLHandlerDeps{Store: store, GitWriter: &mockGitWriter{}, Logger: zap.NewNop(), Registry: newTestGraphQLRegistry(t), IDs: apiruntime.NewSequenceIDGenerator()})
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodPost, "/graphql", strings.NewReader(`{
		"query": "mutation { login(input: { username: \"admin\", password: \"wrong\" }) { token { accessToken } } }"
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
	assert.Contains(t, response.Errors[0].Message, "invalid username or password")
}

func TestGraphQLHandlerRejectsNamespaceMutationWithoutBearerToken(t *testing.T) {
	store, err := memdb.New()
	require.NoError(t, err)

	handler, err := app.NewGraphQLHandler(app.GraphQLHandlerDeps{Store: store, GitWriter: &mockGitWriter{}, Logger: zap.NewNop(), Registry: newTestGraphQLRegistry(t), IDs: apiruntime.NewSequenceIDGenerator()})
	require.NoError(t, err)
	req := httptest.NewRequest(http.MethodPost, "/graphql", strings.NewReader(`{
		"query": "mutation { createNamespace(input: { apiVersion: \"gitstore.dev/v1beta1\", kind: \"Namespace\", metadata: { name: \"alice\" }, spec: { tier: USER } }) { namespace { identifier } } }"
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

func TestGraphQLHandlerRejectsMutationWithInvalidBearerToken(t *testing.T) {
	store, err := memdb.New()
	require.NoError(t, err)

	handler, err := app.NewGraphQLHandler(app.GraphQLHandlerDeps{Store: store, GitWriter: &mockGitWriter{}, Logger: zap.NewNop(), Registry: newTestGraphQLRegistry(t), IDs: apiruntime.NewSequenceIDGenerator()})
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodPost, "/graphql", strings.NewReader(`{
		"query": "mutation { createNamespace(input: { apiVersion: \"gitstore.dev/v1beta1\", kind: \"Namespace\", metadata: { name: \"alice\" }, spec: { tier: USER } }) { namespace { identifier } } }"
	}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer invalid-token")
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
	assert.Contains(t, response.Errors[0].Message, "invalid or expired credentials")
}
