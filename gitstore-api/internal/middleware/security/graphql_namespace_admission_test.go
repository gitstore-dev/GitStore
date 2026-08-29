// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package security_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gitstore-dev/gitstore/api/internal/app"
	"github.com/gitstore-dev/gitstore/api/internal/auth"
	"github.com/gitstore-dev/gitstore/api/internal/auth/provider/anonymous"
	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	"github.com/gitstore-dev/gitstore/api/internal/datastore/memdb"
	apiruntime "github.com/gitstore-dev/gitstore/api/internal/runtime"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

type namespaceAdmissionAuthN struct{}

func (namespaceAdmissionAuthN) Name() string                  { return "namespace-admission-test" }
func (namespaceAdmissionAuthN) Capabilities() auth.Capability { return auth.CapAuthenticate }
func (namespaceAdmissionAuthN) Authenticate(context.Context, auth.AuthRequest) (*auth.Principal, auth.Decision, error) {
	return &auth.Principal{Subject: "mallory", AuthMethod: "test"}, auth.Allow("namespace-admission-test", "authenticated"), nil
}
func (namespaceAdmissionAuthN) RevokeSession(context.Context, string, time.Time) error {
	return auth.ErrNotSupported
}
func (namespaceAdmissionAuthN) RefreshSession(context.Context, string) (string, time.Time, error) {
	return "", time.Time{}, auth.ErrNotSupported
}
func (namespaceAdmissionAuthN) IssueSession(context.Context, string) (string, time.Time, error) {
	return "", time.Time{}, auth.ErrNotSupported
}

type namespaceAdmissionDenyAuthZ struct{}

func (namespaceAdmissionDenyAuthZ) Name() string { return "namespace-admission-deny" }
func (namespaceAdmissionDenyAuthZ) Authorize(context.Context, *auth.Principal, string, auth.ResourceContext) (auth.Decision, error) {
	return auth.Deny("namespace-admission-deny", "namespace access denied"), nil
}

func TestNamespaceAdmissionAuthorizationHidesCreateReason(t *testing.T) {
	store := newNamespaceAdmissionStore(t)
	handler := newNamespaceAdmissionHandler(t, store, auth.NewChainedAuthN(namespaceAdmissionAuthN{}))
	response := postNamespaceMutation(t, handler, `mutation {
		createNamespace(input: {
			apiVersion: "gitstore.dev/v1beta1"
			kind: "Namespace"
			metadata: {name: "Invalid Name"}
			spec: {tier: ORGANIZATION}
		}) { namespace { identifier } }
	}`)

	assertDeniedNamespaceResponse(t, response,
		"NAMESPACE_STRUCTURAL_VALIDATION_FAILED",
		"INVALID_IDENTIFIER",
		"STRUCTURAL",
	)
}

func TestNamespaceAdmissionAuthorizationHidesUpdateReason(t *testing.T) {
	store := newNamespaceAdmissionStore(t)
	now := time.Now().UTC()
	require.NoError(t, store.CreateNamespace(context.Background(), &datastore.Namespace{
		UID: uuid.NewString(), Name: "secret-update", Title: "Secret",
		Tier: datastore.NamespaceTierOrganization, CreationTimestamp: now, UpdateTimestamp: now,
		CreationActor: "alice", UpdateActor: "alice",
	}))
	handler := newNamespaceAdmissionHandler(t, store, auth.NewChainedAuthN(anonymous.New()))
	response := postNamespaceMutation(t, handler, `mutation {
		updateNamespace(input: {
			apiVersion: "gitstore.dev/v1beta1"
			kind: "Namespace"
			metadata: {name: "secret-update"}
			spec: {tier: USER}
		}) { namespace { identifier } }
	}`)

	assertDeniedNamespaceResponse(t, response,
		"NAMESPACE_POLICY_REJECTED",
		"TIER_DEMOTION",
		"POLICY",
	)
	assert.Contains(t, response.Errors[0].Message, "authentication required")
}

func TestNamespaceAdmissionAuthorizationHidesDeleteReasons(t *testing.T) {
	store := newNamespaceAdmissionStore(t)
	now := time.Now().UTC()
	require.NoError(t, store.CreateNamespace(context.Background(), &datastore.Namespace{
		UID: uuid.NewString(), Name: "gitstore-system", Title: "System",
		Tier: datastore.NamespaceTierOrganization, CreationTimestamp: now, UpdateTimestamp: now,
		CreationActor: "system", UpdateActor: "system",
	}))
	require.NoError(t, store.CreateRepository(context.Background(), &datastore.Repository{
		UID: uuid.NewString(), Namespace: "gitstore-system", Name: "gitstore-system",
		DefaultBranch: "main", StorageClass: "local",
		CreationTimestamp: now, UpdateTimestamp: now,
		CreationActor: "system", UpdateActor: "system",
	}))
	handler := newNamespaceAdmissionHandler(t, store, auth.NewChainedAuthN(namespaceAdmissionAuthN{}))
	response := postNamespaceMutation(t, handler, `mutation {
		deleteNamespace(input: {identifier: "gitstore-system"}) {
			deletedIdentifier
			outcome
		}
	}`)

	assertDeniedNamespaceResponse(t, response,
		"NAMESPACE_DELETION_BLOCKED",
		"BOOTSTRAP_NAMESPACE",
		"NAMESPACE_NOT_EMPTY",
		"TERMINATION_STARTED",
		"ALREADY_TERMINATING",
	)
}

type namespaceGraphQLResponse struct {
	Errors []struct {
		Message    string         `json:"message"`
		Extensions map[string]any `json:"extensions"`
	} `json:"errors"`
}

func newNamespaceAdmissionStore(t *testing.T) datastore.Datastore {
	t.Helper()
	store, err := memdb.New()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.Close()) })
	return store
}

func newNamespaceAdmissionHandler(
	t *testing.T,
	store datastore.Datastore,
	authn *auth.ChainedAuthN,
) http.Handler {
	t.Helper()
	registry := auth.NewProviderRegistry(authn, namespaceAdmissionDenyAuthZ{}, nil)
	handler, err := app.NewGraphQLHandler(app.GraphQLHandlerDeps{
		Store: store, Logger: zap.NewNop(), Registry: registry,
		IDs: apiruntime.NewSequenceIDGenerator(),
	})
	require.NoError(t, err)
	return handler
}

func postNamespaceMutation(t *testing.T, handler http.Handler, query string) namespaceGraphQLResponse {
	t.Helper()
	payload, err := json.Marshal(map[string]string{"query": query})
	require.NoError(t, err)
	request := httptest.NewRequest(http.MethodPost, "/graphql", strings.NewReader(string(payload)))
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	require.Equal(t, http.StatusOK, recorder.Code)

	var response namespaceGraphQLResponse
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &response))
	require.NotEmpty(t, response.Errors)
	return response
}

func assertDeniedNamespaceResponse(t *testing.T, response namespaceGraphQLResponse, secrets ...string) {
	t.Helper()
	require.NotEmpty(t, response.Errors)
	encoded, err := json.Marshal(response)
	require.NoError(t, err)
	body := string(encoded)
	message := response.Errors[0].Message
	assert.True(t,
		strings.Contains(message, "denied") || strings.Contains(message, "authentication required"),
		"expected an authentication or authorization denial, got %q", message,
	)
	for _, secret := range secrets {
		assert.NotContains(t, body, secret)
	}
	for _, key := range []string{"reason", "reasons", "phase"} {
		assert.NotContains(t, response.Errors[0].Extensions, key)
	}
}
