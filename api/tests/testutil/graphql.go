// Package testutil provides test utilities for contract and integration tests
package testutil

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
)

// GraphQLRequest represents a GraphQL query request
type GraphQLRequest struct {
	Query     string                 `json:"query"`
	Variables map[string]interface{} `json:"variables,omitempty"`
}

// GraphQLResponse represents a GraphQL query response
type GraphQLResponse struct {
	Data       json.RawMessage        `json:"data"`
	Errors     []GraphQLError         `json:"errors,omitempty"`
	Extensions map[string]interface{} `json:"extensions,omitempty"`
}

// GraphQLError represents a GraphQL error
type GraphQLError struct {
	Message    string                 `json:"message"`
	Path       []interface{}          `json:"path,omitempty"`
	Extensions map[string]interface{} `json:"extensions,omitempty"`
}

// ExecuteGraphQL executes a GraphQL query against the test server
func ExecuteGraphQL(t *testing.T, serverURL string, query string, variables map[string]interface{}) *GraphQLResponse {
	t.Helper()

	req := GraphQLRequest{
		Query:     query,
		Variables: variables,
	}

	reqBody, err := json.Marshal(req)
	require.NoError(t, err, "Failed to marshal GraphQL request")

	httpReq, err := http.NewRequest("POST", serverURL+"/graphql", bytes.NewBuffer(reqBody))
	require.NoError(t, err, "Failed to create HTTP request")

	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(httpReq)
	require.NoError(t, err, "Failed to execute HTTP request")
	defer resp.Body.Close()

	require.Equal(t, http.StatusOK, resp.StatusCode, "Expected HTTP 200 OK")

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err, "Failed to read response body")

	var gqlResp GraphQLResponse
	err = json.Unmarshal(body, &gqlResp)
	require.NoError(t, err, "Failed to unmarshal GraphQL response")

	return &gqlResp
}

// UnmarshalData unmarshals the GraphQL response data into the target struct
func UnmarshalData(t *testing.T, resp *GraphQLResponse, target interface{}) {
	t.Helper()

	err := json.Unmarshal(resp.Data, target)
	require.NoError(t, err, "Failed to unmarshal GraphQL data")
}

// AssertNoErrors asserts that the GraphQL response has no errors
func AssertNoErrors(t *testing.T, resp *GraphQLResponse) {
	t.Helper()

	if len(resp.Errors) > 0 {
		t.Fatalf("GraphQL errors: %+v", resp.Errors)
	}
}

// GetTestServerURL returns the test server URL from environment or default
func GetTestServerURL() string {
	// TODO: Read from environment variable
	return "http://localhost:4000"
}
