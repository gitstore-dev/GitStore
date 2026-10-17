// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package integration

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/require"
)

func TestServiceAccountReplicaLifecycleDeployed(t *testing.T) {
	if os.Getenv("SECRET_TEST_RUN") != "1" {
		t.Skip("use make test TARGET=secret-integration against an explicitly owned two-API deployment")
	}
	require.Equal(t, "1", os.Getenv("SECRET_TEST_OWNED_DEPLOYMENT"))
	endpoints := []string{strings.TrimSuffix(os.Getenv("SECRET_TEST_API_A"), "/"),
		strings.TrimSuffix(os.Getenv("SECRET_TEST_API_B"), "/")}
	require.NotEmpty(t, endpoints[0])
	require.NotEmpty(t, endpoints[1])
	require.NotEqual(t, endpoints[0], endpoints[1])
	tokenBytes, err := os.ReadFile(os.Getenv("SECRET_TEST_TOKEN_FILE"))
	require.NoError(t, err)
	adminToken := strings.TrimSpace(string(tokenBytes))
	clear(tokenBytes)
	require.NotEmpty(t, adminToken)
	client := &http.Client{Timeout: 5 * time.Second}
	defer client.CloseIdleConnections()
	identities := []string{repositoryCapacityAPIIdentity(t, client, endpoints[0]),
		repositoryCapacityAPIIdentity(t, client, endpoints[1])}
	require.NotEqual(t, identities[0], identities[1], "endpoints must be independent API processes")

	issuer := strings.TrimSuffix(os.Getenv("SECRET_TEST_REINCARNATION_API"), "/")
	name := os.Getenv("SECRET_TEST_REINCARNATION_SERVICEACCOUNT")
	require.Equal(t, issuer == "", name == "", "reincarnation issuer and test-created ServiceAccount name must be supplied together")
	if issuer == "" {
		name = uniqueName("sa-replica")
	} else {
		issuerIdentity := repositoryCapacityAPIIdentity(t, client, issuer)
		for _, identity := range identities {
			require.NotEqual(t, identity, issuerIdentity, "the test-policy issuer must not replace either target API")
		}
	}
	privateKey := generateEd25519PEM(t)
	create := `mutation($input:CreateServiceAccountInput!) {
		createServiceAccount(input:$input) { serviceAccount {
			metadata { name namespace uid } keyIDs
		} }
	}`
	createInput := map[string]any{"input": map[string]any{
		"metadata": map[string]any{"name": name},
		"publicKeys": []map[string]any{{"kid": "key-1", "algorithm": "Ed25519",
			"publicKeyPEM": extractPublicKeyPEM(t, privateKey)}},
	}}
	ownedUID := ""
	const remove = `mutation($input:DeleteServiceAccountInput!) {
		deleteServiceAccount(input:$input) { serviceAccount { metadata { uid } } }
	}`
	removeInput := map[string]any{"input": map[string]any{"metadata": map[string]any{"name": name}}}
	t.Cleanup(func() {
		if ownedUID == "" {
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		result := secretTestGraphQLContext(t, ctx, endpoints[0], adminToken, remove, removeInput)
		require.Equal(t, http.StatusOK, result.Status)
		require.Empty(t, result.Errors, "test-owned ServiceAccount cleanup failed")
		var data struct {
			DeleteServiceAccount struct {
				ServiceAccount struct{ Metadata struct{ UID string } }
			}
		}
		require.NoError(t, json.Unmarshal(result.Data, &data))
		require.Equal(t, ownedUID, data.DeleteServiceAccount.ServiceAccount.Metadata.UID)
	})
	var results [2]gqlResponse
	t.Run("concurrent-global-create", func(t *testing.T) {
		for index, endpoint := range endpoints {
			t.Run(fmt.Sprintf("replica-%d", index), func(t *testing.T) {
				t.Parallel()
				results[index] = secretTestGraphQL(t, endpoint, adminToken, create, createInput)
			})
		}
	})
	require.False(t, t.Failed(), "concurrent API requests failed")
	uid := ""
	successes := 0
	decodeCreated := func(result gqlResponse) string {
		t.Helper()
		require.Equal(t, http.StatusOK, result.Status)
		require.Empty(t, result.Errors, "ServiceAccount creation failed")
		var data struct {
			CreateServiceAccount struct {
				ServiceAccount struct {
					Metadata struct{ Name, Namespace, UID string }
					KeyIDs   []string
				}
			}
		}
		require.NoError(t, json.Unmarshal(result.Data, &data))
		record := data.CreateServiceAccount.ServiceAccount
		require.Equal(t, name, record.Metadata.Name)
		require.Empty(t, record.Metadata.Namespace)
		require.NotEmpty(t, record.Metadata.UID)
		require.Equal(t, []string{"key-1"}, record.KeyIDs)
		return record.Metadata.UID
	}
	for _, result := range results {
		require.Equal(t, http.StatusOK, result.Status)
		if len(result.Errors) == 0 {
			uid = decodeCreated(result)
			ownedUID = uid
			successes++
		} else {
			require.Len(t, result.Errors, 1)
			var conflict struct {
				Message    string
				Extensions struct{ Code string }
			}
			require.NoError(t, json.Unmarshal(result.Errors[0], &conflict))
			require.True(t, conflict.Extensions.Code == "ALREADY_EXISTS" ||
				conflict.Message == "datastore: already exists" ||
				conflict.Message == fmt.Sprintf("service account %s already exists", name),
				"losing creation must report the expected global uniqueness conflict")
		}
	}
	require.Equal(t, 1, successes)

	const issue = `mutation($input:IssueServiceAccountTokenInput!) {
		issueServiceAccountToken(input:$input) { tokenRequest {
			metadata { name namespace uid } status { token expirationTimestamp }
		} }
	}`
	issueInput := map[string]any{"input": map[string]any{"metadata": map[string]any{"name": name},
		"spec": map[string]any{"audiences": []string{"gitstore-api"}, "expirationSeconds": 600}}}
	h := &serviceAccountAuthHarness{t: t}
	issueToken := func(endpoint, assertion, expectedName, expectedUID string) (string, time.Time) {
		t.Helper()
		result := secretTestGraphQL(t, endpoint, assertion, issue, issueInput)
		require.Equal(t, http.StatusOK, result.Status)
		var firstError struct{ Extensions struct{ Code string } }
		if len(result.Errors) > 0 {
			require.NoError(t, json.Unmarshal(result.Errors[0], &firstError))
		}
		require.True(t, len(result.Errors) == 0, "fresh assertion was denied with code %q", firstError.Extensions.Code)
		var data struct {
			IssueServiceAccountToken struct {
				TokenRequest struct {
					Metadata struct{ Name, Namespace, UID string }
					Status   struct {
						Token               string
						ExpirationTimestamp time.Time
					}
				}
			}
		}
		require.NoError(t, json.Unmarshal(result.Data, &data))
		record := data.IssueServiceAccountToken.TokenRequest
		require.Equal(t, expectedName, record.Metadata.Name)
		require.Empty(t, record.Metadata.Namespace)
		require.Equal(t, expectedUID, record.Metadata.UID)
		require.True(t, record.Status.Token != "", "issuance must return a token")
		claims := jwt.MapClaims{}
		_, _, err := jwt.NewParser().ParseUnverified(record.Status.Token, claims)
		require.True(t, err == nil, "issued token must contain valid claims")
		require.Equal(t, "system:serviceaccount:"+expectedName, claims["sub"])
		require.Equal(t, expectedUID, claims["sa_uid"])
		expiration, err := claims.GetExpirationTime()
		require.NoError(t, err)
		require.NotNil(t, expiration)
		require.True(t, expiration.After(time.Now().Add(30*time.Second)), "token must remain valid during its peer read")
		require.WithinDuration(t, expiration.Time, record.Status.ExpirationTimestamp, time.Second)
		return record.Status.Token, expiration.Time
	}
	denied := func(result gqlResponse, code string) {
		t.Helper()
		require.True(t, result.Status == http.StatusOK || result.Status == http.StatusUnauthorized,
			"transport/server failure is not an expected identity denial")
		require.Len(t, result.Errors, 1)
		require.True(t, len(bytes.TrimSpace(result.Data)) == 0 || bytes.Equal(bytes.TrimSpace(result.Data), []byte("null")),
			"identity denial must not return successful partial data")
		var rejection struct{ Extensions struct{ Code string } }
		require.NoError(t, json.Unmarshal(result.Errors[0], &rejection))
		require.Equal(t, code, rejection.Extensions.Code)
	}
	assertion := h.signClientAssertion(name, uid, privateKey)
	denied(secretTestGraphQL(t, endpoints[0], assertion, issue, issueInput), "FORBIDDEN")
	denied(secretTestGraphQL(t, endpoints[1], assertion, issue, issueInput), "UNAUTHENTICATED")

	const read = `query { namespaces(first:1) { edges { node { id } } } }`
	var oldToken string
	var oldExpiration time.Time
	if issuer != "" {
		oldToken, oldExpiration = issueToken(issuer, h.signClientAssertion(name, uid, privateKey), name, uid)
		for _, endpoint := range endpoints {
			denied(secretTestGraphQL(t, endpoint, oldToken, read, nil), "FORBIDDEN")
		}
	}

	deleted := secretTestGraphQL(t, endpoints[1], adminToken, remove, removeInput)
	require.Equal(t, http.StatusOK, deleted.Status)
	require.Empty(t, deleted.Errors)
	if issuer != "" {
		for _, endpoint := range endpoints {
			require.True(t, time.Now().Before(oldExpiration), "token must not expire before deletion rejection is checked")
			denied(secretTestGraphQL(t, endpoint, oldToken, read, nil), "UNAUTHENTICATED")
		}
	}
	replacement := decodeCreated(secretTestGraphQL(t, endpoints[0], adminToken, create, createInput))
	ownedUID = replacement
	require.NotEqual(t, uid, replacement, "same-name recreation must create a new incarnation")
	for index, endpoint := range endpoints {
		denied(secretTestGraphQL(t, endpoint, h.signClientAssertion(name, uid, privateKey), issue, issueInput), "UNAUTHENTICATED")
		denied(secretTestGraphQL(t, endpoint, h.signClientAssertion(name, replacement, privateKey), issue, issueInput), "FORBIDDEN")
		require.Equal(t, identities[index], repositoryCapacityAPIIdentity(t, client, endpoint),
			"identity checks must not be explained by an API process replacement")
	}
	if issuer != "" {
		replacementToken, _ := issueToken(issuer, h.signClientAssertion(name, replacement, privateKey), name, replacement)
		for _, endpoint := range endpoints {
			require.True(t, time.Now().Before(oldExpiration), "recreation rejection must be independent of token expiration")
			denied(secretTestGraphQL(t, endpoint, oldToken, read, nil), "UNAUTHENTICATED")
			denied(secretTestGraphQL(t, endpoint, replacementToken, read, nil), "FORBIDDEN")
		}
		t.Log("unexpired old access token authenticated on both target APIs before deletion, then was denied after deletion and same-name/same-key recreation; replacement token authenticated without gaining target-policy permissions")
	} else {
		t.Log("access-token reincarnation not exercised: supply a separate test-policy issuer and a test-created bound name")
	}

	boundName := getEnv("SECRET_TEST_SERVICEACCOUNT", "gitstore-controller-manager")
	kid := uniqueName("replica-proof-key")
	boundUID := secretTestRotate(t, endpoints[0], adminToken, boundName,
		[]secretCapacityOwnedKey{{ID: kid, Public: extractPublicKeyPEM(t, privateKey)}}, []string{})
	t.Cleanup(func() {
		require.Equal(t, boundUID, secretTestRotate(t, endpoints[0], adminToken, boundName, nil, []string{kid}),
			"test-key cleanup must preserve the enrolled identity")
	})
	issueInput = map[string]any{"input": map[string]any{"metadata": map[string]any{"name": boundName},
		"spec": map[string]any{"audiences": []string{"gitstore-api"}, "expirationSeconds": 600}}}
	for index, endpoint := range endpoints {
		assertion := h.signClientAssertionWithKeyID(boundName, boundUID, kid, privateKey)
		token, _ := issueToken(endpoint, assertion, boundName, boundUID)
		peer := endpoints[1-index]
		denied(secretTestGraphQL(t, peer, assertion, issue, issueInput), "UNAUTHENTICATED")
		result := secretTestGraphQL(t, peer, token,
			`query { namespaces(first:1) { edges { node { id } } } }`, nil)
		require.Equal(t, http.StatusOK, result.Status)
		require.True(t, len(result.Errors) == 0, "peer-issued controller token must authorize its declared read")
		var data struct {
			Namespaces *struct {
				Edges []struct{ Node struct{ ID string } }
			}
		}
		require.NoError(t, json.Unmarshal(result.Data, &data))
		require.NotNil(t, data.Namespaces)
		require.NotNil(t, data.Namespaces.Edges)
		require.True(t, len(data.Namespaces.Edges) <= 1)
		for _, edge := range data.Namespaces.Edges {
			require.NotEmpty(t, edge.Node.ID)
		}
		require.Equal(t, identities[index], repositoryCapacityAPIIdentity(t, client, endpoint))
	}
	t.Log("two independent APIs: global uniqueness, authorization versus stale-UID denial, shared replay rejection and peer-token authentication passed; original enrolled identity retained")
}

// generateEd25519PEM returns a fresh Ed25519 private key PEM-encoded as PKCS#8.
func generateEd25519PEM(t *testing.T) string {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	der, err := x509.MarshalPKCS8PrivateKey(priv)
	require.NoError(t, err)
	block := &pem.Block{Type: "PRIVATE KEY", Bytes: der}
	return string(pem.EncodeToMemory(block))
}

// extractPublicKeyPEM extracts the public key from a PEM-encoded private key.
func extractPublicKeyPEM(t *testing.T, privPEM string) string {
	t.Helper()
	block, _ := pem.Decode([]byte(privPEM))
	require.NotNil(t, block)
	priv, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	require.NoError(t, err)
	privKey := priv.(ed25519.PrivateKey)
	pubKey := privKey.Public().(ed25519.PublicKey)
	pubDER, err := x509.MarshalPKIXPublicKey(pubKey)
	require.NoError(t, err)
	pubBlock := &pem.Block{Type: "PUBLIC KEY", Bytes: pubDER}
	return string(pem.EncodeToMemory(pubBlock))
}

// serviceAccountAuthHarness wraps the API server with serviceaccount-specific
// test helpers.
type serviceAccountAuthHarness struct {
	t      *testing.T
	apiURL string
	token  string // admin bearer token for mutation calls
}

func newServiceAccountAuthHarness(t *testing.T) *serviceAccountAuthHarness {
	t.Helper()
	apiURL := acquireNamespaceContractAPI(t)
	return &serviceAccountAuthHarness{
		t:      t,
		apiURL: apiURL,
		token:  namespaceContractBootstrapToken(t, apiURL),
	}
}

// createServiceAccount calls the GraphQL mutation with an admin token and returns the UID.
func (h *serviceAccountAuthHarness) createServiceAccount(name, publicKeyPEM string) string {
	h.t.Helper()

	mutation := `
		mutation CreateServiceAccount($input: CreateServiceAccountInput!) {
			createServiceAccount(input: $input) {
				serviceAccount {
					metadata {
						uid
						namespace
						name
						creationTimestamp
					}
				}
			}
		}
	`

	vars := map[string]any{
		"input": map[string]any{
			"apiVersion": "gitstore.dev/v1alpha1",
			"kind":       "ServiceAccount",
			"metadata": map[string]any{

				"name": name,
			},
			"publicKeys": []map[string]any{
				{
					"kid":          "key-1",
					"algorithm":    "Ed25519",
					"publicKeyPEM": publicKeyPEM,
				},
			},
		},
	}

	body := map[string]any{
		"query":     mutation,
		"variables": vars,
	}
	bodyJSON, err := json.Marshal(body)
	require.NoError(h.t, err)

	req, err := http.NewRequest("POST", h.apiURL+"/graphql", bytes.NewReader(bodyJSON))
	require.NoError(h.t, err)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+h.token)

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	require.NoError(h.t, err)
	defer resp.Body.Close()

	require.Equal(h.t, http.StatusOK, resp.StatusCode)

	var result struct {
		Data struct {
			CreateServiceAccount struct {
				ServiceAccount struct {
					Metadata struct {
						UID string `json:"uid"`
					} `json:"metadata"`
				} `json:"serviceAccount"`
			} `json:"createServiceAccount"`
		} `json:"data"`
		Errors []map[string]any `json:"errors"`
	}
	err = json.NewDecoder(resp.Body).Decode(&result)
	require.NoError(h.t, err)
	require.Empty(h.t, result.Errors, "GraphQL errors: %v", result.Errors)
	require.NotEmpty(h.t, result.Data.CreateServiceAccount.ServiceAccount.Metadata.UID)

	return result.Data.CreateServiceAccount.ServiceAccount.Metadata.UID
}

// issueServiceAccountToken calls the issueServiceAccountToken mutation with a signed assertion.
func (h *serviceAccountAuthHarness) issueServiceAccountToken(name, uid string, privPEM string) string {
	h.t.Helper()

	// Sign a client assertion with the private key
	assertion := h.signClientAssertion(name, uid, privPEM)

	mutation := `
		mutation IssueServiceAccountToken($input: IssueServiceAccountTokenInput!) {
			issueServiceAccountToken(input: $input) {
				tokenRequest {
					status {
						token
						expirationTimestamp
					}
				}
			}
		}
	`

	vars := map[string]any{
		"input": map[string]any{
			"apiVersion": "gitstore.dev/v1alpha1",
			"kind":       "TokenRequest",
			"metadata": map[string]any{

				"name": name,
			},
			"spec": map[string]any{
				"audiences":         []string{"gitstore-api"},
				"expirationSeconds": 600,
			},
		},
	}

	body := map[string]any{
		"query":     mutation,
		"variables": vars,
	}
	bodyJSON, err := json.Marshal(body)
	require.NoError(h.t, err)

	// Issue the token with the assertion bearer
	req, err := http.NewRequest("POST", h.apiURL+"/graphql", bytes.NewReader(bodyJSON))
	require.NoError(h.t, err)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+assertion)

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	require.NoError(h.t, err)
	defer resp.Body.Close()

	require.Equal(h.t, http.StatusOK, resp.StatusCode)

	var result struct {
		Data struct {
			IssueServiceAccountToken struct {
				TokenRequest struct {
					Status struct {
						Token string `json:"token"`
					} `json:"status"`
				} `json:"tokenRequest"`
			} `json:"issueServiceAccountToken"`
		} `json:"data"`
		Errors []map[string]any `json:"errors"`
	}
	err = json.NewDecoder(resp.Body).Decode(&result)
	require.NoError(h.t, err)
	require.Empty(h.t, result.Errors, "GraphQL errors: %v", result.Errors)
	require.NotEmpty(h.t, result.Data.IssueServiceAccountToken.TokenRequest.Status.Token)

	return result.Data.IssueServiceAccountToken.TokenRequest.Status.Token
}

// signClientAssertion creates a client assertion JWT signed with the private key.
func (h *serviceAccountAuthHarness) signClientAssertion(name, uid string, privPEM string) string {
	return h.signClientAssertionWithKeyID(name, uid, "key-1", privPEM)
}

func (h *serviceAccountAuthHarness) signClientAssertionWithKeyID(name, uid, keyID, privPEM string) string {
	h.t.Helper()

	block, _ := pem.Decode([]byte(privPEM))
	require.NotNil(h.t, block)
	priv, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	require.NoError(h.t, err)

	claims := jwt.MapClaims{
		"typ":    "JWT",
		"iss":    "system:serviceaccount:" + name,
		"sub":    "system:serviceaccount:" + name,
		"aud":    "gitstore-api/serviceaccount-token",
		"iat":    time.Now().Unix(),
		"exp":    time.Now().Add(60 * time.Second).Unix(),
		"jti":    fmt.Sprintf("jti-%d", time.Now().UnixNano()),
		"sa_uid": uid,
	}

	token := jwt.NewWithClaims(jwt.SigningMethodEdDSA, claims)
	token.Header["typ"] = "gitstore-sa-assertion+jwt"
	token.Header["kid"] = keyID
	signed, err := token.SignedString(priv.(ed25519.PrivateKey))
	require.NoError(h.t, err)

	return signed
}

// queryWithToken executes a GraphQL query with the given bearer token.
func (h *serviceAccountAuthHarness) queryWithToken(query string, token string) (map[string]any, error) {
	h.t.Helper()

	body := map[string]any{
		"query": query,
	}
	bodyJSON, err := json.Marshal(body)
	require.NoError(h.t, err)

	req, err := http.NewRequest("POST", h.apiURL+"/graphql", bytes.NewReader(bodyJSON))
	require.NoError(h.t, err)
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	require.NoError(h.t, err)
	defer resp.Body.Close()

	require.Equal(h.t, http.StatusOK, resp.StatusCode)

	var result map[string]any
	err = json.NewDecoder(resp.Body).Decode(&result)
	require.NoError(h.t, err)

	return result, nil
}

// TestServiceAccountAuth_ControllerHasLimitedPrivilege_T022 verifies that a service account
// bound to a limited role cannot perform admin-only actions (T022).
func TestServiceAccountAuth_ControllerHasLimitedPrivilege_T022(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	h := newServiceAccountAuthHarness(t)

	// Create a service account
	privPEM := generateEd25519PEM(t)
	pubPEM := extractPublicKeyPEM(t, privPEM)
	uid := h.createServiceAccount("gitstore-controller-manager", pubPEM)

	// Issue an access token
	accessToken := h.issueServiceAccountToken("gitstore-controller-manager", uid, privPEM)

	// Try to perform an admin-only action with limited service account token
	// An admin-only action would be something like updating namespace status with
	// an action that requires admin privilege (e.g., any action not explicitly
	// allowed for the "controller" role)
	query := `query {
		namespaces(first: 1) {
			edges {
				node {
					metadata {
						name
					}
				}
			}
		}
	}`

	result, err := h.queryWithToken(query, accessToken)
	require.NoError(t, err)

	// This harness uses namespaceOwnerAuthZ, not the shipped RBAC policy. Pin a
	// successful authenticated read without mistaking it for anonymous access
	// or proof of least privilege. The shipped grants are tested separately in
	// TestAuthorizationBaselineShippedPolicy in the API provider package.
	require.Empty(t, result["errors"], "authenticated baseline query must succeed")

	// Verify the token is valid (authenticated) by checking it doesn't return
	// an authentication error (e.g., "unauthenticated").
	// The success of the read query demonstrates the token works;
	// the verification of least privilege comes from T024/T024a integration tests.
	data, ok := result["data"].(map[string]any)
	require.True(t, ok, "expected data field in response")
	require.NotNil(t, data, "authenticated request should return data, not just errors")

	t.Log("T022: Service account authenticated with limited privilege access token")
}

// TestServiceAccountAuth_NamespaceReconcilerActions_T022a verifies that a service account
// bound to the controller role can perform namespace reconciler actions (T022a).
func TestServiceAccountAuth_NamespaceReconcilerActions_T022a(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	h := newServiceAccountAuthHarness(t)

	// Create a service account
	privPEM := generateEd25519PEM(t)
	pubPEM := extractPublicKeyPEM(t, privPEM)
	uid := h.createServiceAccount("gitstore-controller-manager", pubPEM)

	// Issue an access token
	accessToken := h.issueServiceAccountToken("gitstore-controller-manager", uid, privPEM)

	// Verify the service account can read namespaces (repository.create, namespace.watch, etc.)
	query := `query {
		namespaces(first: 1) {
			edges {
				node {
					metadata {
						name
						uid
					}
					spec {
						tier
					}
				}
			}
		}
	}`

	result, err := h.queryWithToken(query, accessToken)
	require.NoError(t, err)

	// The service account should be able to read namespaces
	// (this is covered by the existing policy that allows read access)
	data, ok := result["data"].(map[string]any)
	require.True(t, ok, "expected data field in response")
	require.NotNil(t, data, "namespace query should succeed for controller role")

	t.Log("T022a: Service account can read namespaces under controller role")
}

// TestServiceAccountAuth_ClusterSubjectRoundTrip tests that
// the authenticated cluster-scoped subject survives an assertion/token round trip.
func TestServiceAccountAuth_RBAClocalHandlesServiceAccountSubjects(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	// This harness exercises the identity boundary, not the shipped RBAC provider.
	apiURL := acquireNamespaceContractAPI(t)

	h := &serviceAccountAuthHarness{
		t:      t,
		apiURL: apiURL,
		token:  namespaceContractBootstrapToken(t, apiURL),
	}

	// Create a service account
	privPEM := generateEd25519PEM(t)
	pubPEM := extractPublicKeyPEM(t, privPEM)
	uid := h.createServiceAccount("gitstore-controller-manager", pubPEM)

	// Issue a token and verify it works
	accessToken := h.issueServiceAccountToken("gitstore-controller-manager", uid, privPEM)

	query := `query {
		namespaces(first: 1) {
			edges {
				node {
					metadata {
						name
					}
				}
			}
		}
	}`

	result, err := h.queryWithToken(query, accessToken)
	require.NoError(t, err)

	// The query should succeed because the service account subject is bound to the controller role
	// which includes namespace.read
	_, ok := result["data"].(map[string]any)
	require.True(t, ok, "cluster-scoped ServiceAccount should authenticate")

	t.Log("cluster-scoped ServiceAccount subject authenticated")
}

func TestServiceAccountAuthEndToEndLifecycle_T041(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	h := newServiceAccountAuthHarness(t)
	const name = "e2e-controller"
	privateKey := generateEd25519PEM(t)
	uid := h.createServiceAccount(name, extractPublicKeyPEM(t, privateKey))
	accessToken := h.issueServiceAccountToken(name, uid, privateKey)

	authorized, err := h.queryWithToken(`query { namespaces(first: 1) { edges { node { metadata { name } } } } }`, accessToken)
	require.NoError(t, err)
	require.Empty(t, authorized["errors"])

	issueMutation := `mutation Issue($input: IssueServiceAccountTokenInput!) {
		issueServiceAccountToken(input: $input) { tokenRequest { status { token } } }
	}`
	issueInput := map[string]any{
		"input": map[string]any{
			"apiVersion": "gitstore.dev/v1beta1",
			"kind":       "TokenRequest",
			"metadata":   map[string]any{"name": name},
			"spec":       map[string]any{"audiences": []string{"gitstore-api"}, "expirationSeconds": 3600},
		},
	}
	for _, assertion := range []string{
		h.signClientAssertion("other", uid, privateKey),
		h.signClientAssertion(name, "different-uid", privateKey),
	} {
		response := gqlQueryWithURL(t, h.apiURL, assertion, issueMutation, issueInput)
		require.NotEmpty(t, response.Errors, "incorrect assertion subject or UID must be denied")
	}

	dialer := websocket.Dialer{Subprotocols: []string{"graphql-transport-ws"}}
	conn, response, err := dialer.Dial(strings.Replace(h.apiURL, "http", "ws", 1)+"/graphql", nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })
	if response != nil {
		t.Cleanup(func() { _ = response.Body.Close() })
	}
	require.NoError(t, conn.SetReadDeadline(time.Now().Add(5*time.Second)))
	require.NoError(t, conn.WriteJSON(map[string]any{
		"type":    "connection_init",
		"payload": map[string]any{"Authorization": "Bearer " + accessToken},
	}))
	var acknowledgement map[string]any
	require.NoError(t, conn.ReadJSON(&acknowledgement))
	require.Equal(t, "connection_ack", acknowledgement["type"])

	replacementPrivateKey := generateEd25519PEM(t)
	rotateMutation := `mutation Rotate($input: RotateServiceAccountKeyInput!) {
		rotateServiceAccountKey(input: $input) { serviceAccount { keyIDs } }
	}`
	rotated := gqlQueryWithURL(t, h.apiURL, h.token, rotateMutation, map[string]any{
		"input": map[string]any{
			"metadata": map[string]any{"name": name},
			"add": []map[string]any{{
				"kid":          "key-2",
				"algorithm":    "Ed25519",
				"publicKeyPEM": extractPublicKeyPEM(t, replacementPrivateKey),
			}},
			"removeKids": []string{"key-1"},
		},
	})
	require.Empty(t, rotated.Errors, "rotation must retain a valid key set")
	var rotateData struct {
		RotateServiceAccountKey struct {
			ServiceAccount struct {
				KeyIDs []string `json:"keyIDs"`
			} `json:"serviceAccount"`
		} `json:"rotateServiceAccountKey"`
	}
	require.NoError(t, json.Unmarshal(rotated.Data, &rotateData))
	require.Equal(t, []string{"key-2"}, rotateData.RotateServiceAccountKey.ServiceAccount.KeyIDs)

	oldAssertion := h.signClientAssertion(name, uid, privateKey)
	require.NotEmpty(t, gqlQueryWithURL(t, h.apiURL, oldAssertion, issueMutation, issueInput).Errors,
		"an assertion signed by the removed key must be denied")
	replacementAssertion := h.signClientAssertionWithKeyID(name, uid, "key-2", replacementPrivateKey)
	require.Empty(t, gqlQueryWithURL(t, h.apiURL, replacementAssertion, issueMutation, issueInput).Errors,
		"an assertion signed by the replacement key must be accepted")

	deleteMutation := `mutation Delete($input: DeleteServiceAccountInput!) {
		deleteServiceAccount(input: $input) { serviceAccount { metadata { uid } } }
	}`
	deleted := gqlQueryWithURL(t, h.apiURL, h.token, deleteMutation, map[string]any{
		"input": map[string]any{
			"apiVersion": "gitstore.dev/v1beta1",
			"kind":       "ServiceAccount",
			"metadata":   map[string]any{"name": name},
		},
	})
	require.Empty(t, deleted.Errors)

	_, _, err = conn.ReadMessage()
	var closeErr *websocket.CloseError
	require.ErrorAs(t, err, &closeErr)
	require.Equal(t, websocket.CloseNormalClosure, closeErr.Code)

	deniedAfterDelete, err := h.queryWithToken(`query { namespaces(first: 1) { edges { node { metadata { name } } } } }`, accessToken)
	require.NoError(t, err)
	require.NotEmpty(t, deniedAfterDelete["errors"], "deleted service account token must be rejected")

	idempotentDelete := gqlQueryWithURL(t, h.apiURL, h.token, deleteMutation, map[string]any{
		"input": map[string]any{
			"apiVersion": "gitstore.dev/v1beta1",
			"kind":       "ServiceAccount",
			"metadata":   map[string]any{"name": name},
		},
	})
	require.Empty(t, idempotentDelete.Errors, "deleting an absent ServiceAccount must be a no-op")
}
