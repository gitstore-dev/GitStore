// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

// Package testutil provides test utilities for contract and integration tests
package testutil

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"testing"

	gitv1 "github.com/gitstore-dev/gitstore/api/gen/gitstore/git/v1"
	"github.com/gitstore-dev/gitstore/api/internal/gitclient"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"
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
	if err != nil {
		// If server is not available, skip the test
		t.Skipf("GraphQL server not available at %s: %v", serverURL, err)
	}
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

const grpcBufSize = 1 << 20 // 1 MiB

// StartGRPCStub starts an in-process gRPC stub server using bufconn.
// svc is a GitServiceServer implementation (e.g. a struct embedding UnimplementedGitServiceServer).
// Returns a *gitclient.Client dialled against the stub and a cleanup function.
//
// Use this for unit-level contract tests that don't need Docker:
//
//	client, cleanup := testutil.StartGRPCStub(t, &myStub{})
//	defer cleanup()
func StartGRPCStub(t *testing.T, svc gitv1.GitServiceServer) (*gitclient.Client, func()) {
	t.Helper()
	lis := bufconn.Listen(grpcBufSize)
	srv := grpc.NewServer()
	gitv1.RegisterGitServiceServer(srv, svc)
	go func() { _ = srv.Serve(lis) }()

	conn, err := grpc.NewClient(
		"passthrough:///bufconn",
		grpc.WithContextDialer(func(_ context.Context, _ string) (net.Conn, error) {
			return lis.DialContext(context.Background())
		}),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	require.NoError(t, err)

	client := gitclient.NewClientFromConn(conn)
	cleanup := func() {
		conn.Close()
		srv.Stop()
		lis.Close()
	}
	return client, cleanup
}
